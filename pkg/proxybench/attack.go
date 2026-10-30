/*
 * Copyright 2026 The Dragonfly Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Package proxybench adapts benchmark requests to Vegeta without retaining bodies.
package proxybench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

type Options struct {
	Mode            string        `json:"mode"`
	Target          string        `json:"target,omitempty"`
	Range           string        `json:"range,omitempty"`
	FileSize        uint64        `json:"file_size,omitempty"`
	ChunkSize       uint64        `json:"chunk_size,omitempty"`
	Streams         uint64        `json:"streams,omitempty"`
	URLCount        uint64        `json:"url_count,omitempty"`
	Proxy           string        `json:"proxy"`
	Rate            uint64        `json:"rate"`
	InitialWorkers  uint64        `json:"initial_workers"`
	MaxWorkers      uint64        `json:"max_workers"`
	Duration        time.Duration `json:"duration_ns"`
	Timeout         time.Duration `json:"timeout_ns"`
	GracefulStop    time.Duration `json:"graceful_stop_ns"`
	ExpectedStatus  uint16        `json:"expected_status"`
	FollowRedirects bool          `json:"follow_redirects"`
	SeedClientCPUs  float64       `json:"seed_client_cpus,omitempty"`
}

type Report struct {
	Engine        string          `json:"engine"`
	EngineVersion string          `json:"engine_version"`
	Options       Options         `json:"effective_options"`
	Metrics       *vegeta.Metrics `json:"native_summary"`
	Consumed      uint64          `json:"plan_consumed"`
	Succeeded     uint64          `json:"expected_status_successes"`
	SuccessRate   *float64        `json:"expected_status_success_ratio"`
	Canceled      uint64          `json:"canceled_requests"`
	PayloadGbps   float64         `json:"payload_gbps"`
	Interrupted   bool            `json:"interrupted"`
}

// Version is provenance only, never a runtime version gate.
func Version() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/tsenart/vegeta/v12" {
				return dep.Version
			}
		}
	}
	return "unknown"
}

var errWindowClosed = errors.New("benchmark request window closed")

type observation struct {
	bytes atomic.Uint64
}

type countingBody struct {
	io.ReadCloser
	count  *atomic.Uint64
	finish func()
}

func (b countingBody) Close() error {
	err := b.ReadCloser.Close()
	b.finish()
	return err
}

func (b countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.count.Add(uint64(n))
	return n, err
}

type transport struct {
	base   *http.Transport
	ctx    context.Context
	end    time.Time
	active sync.Map // Vegeta sequence -> observation, removed when its result is consumed.
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	seq := req.Header.Get("X-Vegeta-Seq")
	value, exists := t.active.Load(seq)
	if !exists {
		// The Targeter has already admitted this logical request. Let it drain
		// even if DURATION expires before the HTTP transport gets CPU time.
		if t.ctx.Err() != nil {
			return nil, errWindowClosed
		}
		value = &observation{}
		t.active.Store(seq, value)
	}
	ctx, cancel := context.WithCancel(req.Context())
	stop := context.AfterFunc(t.ctx, cancel)
	finish := func() { stop(); cancel() }
	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if resp != nil {
		resp.Body = countingBody{ReadCloser: resp.Body, count: &value.(*observation).bytes, finish: finish}
	} else {
		finish()
	}
	return resp, err
}

// Execute uses Vegeta's native pacer and worker pool. Only successfully generated
// targets consume an item; HTTP failures and transport resends never rewind it.
func Execute(ctx context.Context, o Options, next vegeta.Targeter) (Report, error) {
	r := Report{Engine: "vegeta", EngineVersion: Version(), Options: o, Metrics: &vegeta.Metrics{}}
	if o.Rate == 0 || o.Rate > uint64(^uint(0)>>1) || o.InitialWorkers == 0 || o.MaxWorkers < o.InitialWorkers || o.Duration <= 0 || o.Timeout <= 0 || o.GracefulStop < o.Timeout {
		return r, fmt.Errorf("invalid attack rate, workers, duration, timeout or graceful stop")
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.MaxIdleConnsPerHost = int(o.MaxWorkers)
	base.MaxIdleConns = int(o.MaxWorkers)
	base.DisableCompression = true
	if o.Mode == "fixed-pool" {
		proxy, err := url.Parse(o.Proxy)
		if err != nil || proxy.Host == "" {
			return r, fmt.Errorf("invalid fixed-pool proxy: %q", o.Proxy)
		}
		base.Proxy = http.ProxyURL(proxy)
	}
	defer base.CloseIdleConnections()
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	tr := &transport{base: base, ctx: requestCtx, end: time.Now().Add(o.Duration)}
	client := &http.Client{Transport: tr, Timeout: o.Timeout}
	if !o.FollowRedirects {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	a := vegeta.NewAttacker(vegeta.Workers(o.InitialWorkers), vegeta.MaxWorkers(o.MaxWorkers), vegeta.MaxBody(0), vegeta.Client(client))
	stop := time.AfterFunc(o.Duration, func() { a.Stop() })
	defer stop.Stop()
	drain := time.AfterFunc(o.Duration, func() {
		// Stop only ends scheduling. Give requests their full configured drain
		// window before canceling the shared HTTP context.
		timeout := time.AfterFunc(o.GracefulStop, cancel)
		defer timeout.Stop()
		<-requestCtx.Done()
	})
	defer drain.Stop()
	interruption := context.AfterFunc(ctx, func() { a.Stop() })
	defer interruption()
	var consumed uint64
	var targetErr error
	var mu sync.Mutex
	targeter := func(t *vegeta.Target) error {
		mu.Lock()
		defer mu.Unlock()
		if requestCtx.Err() != nil || !time.Now().Before(tr.end) {
			return errWindowClosed
		}
		if err := next(t); err != nil {
			targetErr = err
			return err
		}
		consumed++
		return nil
	}
	for result := range a.Attack(targeter, vegeta.Rate{Freq: int(o.Rate), Per: time.Second}, o.Duration, "") {
		value, sent := tr.active.LoadAndDelete(strconv.FormatUint(result.Seq, 10))
		if !sent {
			continue // A scheduling stop/target error is not an HTTP attempt.
		}
		// MaxBody(0) drains but leaves native BytesIn zero. Count actual Read
		// bytes, including partial bodies on failure, without buffering them.
		result.BytesIn = value.(*observation).bytes.Load()
		r.Metrics.Add(result)
		if result.Code == o.ExpectedStatus && result.Error == "" {
			r.Succeeded++
		}
		if requestCtx.Err() != nil && result.Code == 0 {
			r.Canceled++
		}
	}
	r.Consumed = consumed
	r.Interrupted = ctx.Err() != nil
	r.Metrics.Close()
	if r.Metrics.Requests > 0 {
		success := float64(r.Succeeded) / float64(r.Metrics.Requests)
		r.SuccessRate = &success
		if seconds := (r.Metrics.Duration + r.Metrics.Wait).Seconds(); seconds > 0 {
			r.PayloadGbps = float64(r.Metrics.BytesIn.Total) * 8 / seconds / 1e9
		}
	}
	return r, errors.Join(targetErr, ctx.Err())
}

func (r Report) WriteText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "\nproxy-bench (%s), vegeta %s\nTarget rate: %d req/s; workers: %d..%d; duration: %s; timeout: %s\n", r.Options.Mode, r.EngineVersion, r.Options.Rate, r.Options.InitialWorkers, r.Options.MaxWorkers, r.Options.Duration, r.Options.Timeout); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Target: %s; proxy: %s; Range: %s\n", r.Options.Target, r.Options.Proxy, r.Options.Range); err != nil {
		return err
	}
	if r.Options.Mode == sequential {
		if _, err := fmt.Fprintf(w, "Sequential: %d bytes, %d-byte chunks, %d streams, %d URLs\n", r.Options.FileSize, r.Options.ChunkSize, r.Options.Streams, r.Options.URLCount); err != nil {
			return err
		}
	}
	if err := vegeta.NewTextReporter(r.Metrics)(w); err != nil {
		return err
	}
	success := "n/a (no HTTP attempts)"
	if r.SuccessRate != nil {
		success = fmt.Sprintf("%.2f%% (%d/%d)", *r.SuccessRate*100, r.Succeeded, r.Metrics.Requests)
	}
	if _, err := fmt.Fprintf(w, "Expected-status success [HTTP %d and no error]: %s\nConsumed: %d plan entries\nPayload throughput: %.3f Gbps (response bodies)\n", r.Options.ExpectedStatus, success, r.Consumed, r.PayloadGbps); err != nil {
		return err
	}
	if r.Metrics.Requests < 2 {
		if _, err := fmt.Fprintln(w, "Rate/latency estimates: insufficient samples"); err != nil {
			return err
		}
	}
	if r.Options.SeedClientCPUs > 0 && r.PayloadGbps > 0 {
		_, err := fmt.Fprintf(w, "CPU cost: %.3f core/Gbps (payload)\n", r.Options.SeedClientCPUs/r.PayloadGbps)
		return err
	}
	return nil
}
