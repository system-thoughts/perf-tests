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

package proxybench

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

func testOptions(proxy string) Options {
	return Options{Mode: "fixed-pool", Proxy: proxy, Rate: 20, InitialWorkers: 2, MaxWorkers: 4, Duration: 150 * time.Millisecond, Timeout: time.Second, GracefulStop: 2 * time.Second, ExpectedStatus: http.StatusPartialContent}
}

func targeter(t *vegeta.Target) error {
	*t = vegeta.Target{Method: http.MethodGet, URL: "http://source.invalid/object", Header: http.Header{"Range": {"bytes=0-1048575"}}}
	return nil
}

func TestNativeMetricsAndExpectedStatusWithDiscardedBodies(t *testing.T) {
	for _, status := range []int{206, 200, 302, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			payload := strings.Repeat("b", 1<<20)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Host != "source.invalid" || r.Header.Get("Range") != "bytes=0-1048575" {
					t.Error("proxy or request mapping changed")
				}
				w.Header().Set("Location", "http://source.invalid/redirected")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, payload)
			}))
			defer server.Close()
			report, err := Execute(context.Background(), testOptions(server.URL), targeter)
			if err != nil {
				t.Fatal(err)
			}
			m := report.Metrics
			if m.Requests < 2 || m.BytesIn.Total != m.Requests*uint64(len(payload)) || m.Latencies.P99 <= 0 || m.Rate <= 0 {
				t.Fatalf("missing native statistics: %+v", m)
			}
			if m.StatusCodes["302"] > 0 && m.Requests != uint64(m.StatusCodes["302"]) {
				t.Fatal("fixed-pool followed redirects")
			}
			want := 0.0
			if status == 206 {
				want = 1
			}
			if report.SuccessRate == nil || *report.SuccessRate != want {
				t.Fatalf("status %d: %+v", status, report)
			}
			if status == 200 && m.Success != 1 {
				t.Fatal("overwrote Vegeta native success semantics")
			}
			var output bytes.Buffer
			if err = report.WriteText(&output); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "50, 90, 95, 99") || !strings.Contains(output.String(), "Expected-status success") {
				t.Fatal(output.String())
			}
		})
	}
}

func TestPartialBodiesAndTimeoutsAreCounted(t *testing.T) {
	for _, slow := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if slow {
				<-r.Context().Done()
				return
			}
			w.Header().Set("Content-Length", "4096")
			w.Header().Set("Connection", "close")
			w.WriteHeader(206)
			_, _ = io.WriteString(w, strings.Repeat("x", 1024))
		}))
		o := testOptions(server.URL)
		o.Timeout, o.GracefulStop = 30*time.Millisecond, 60*time.Millisecond
		report, err := Execute(context.Background(), o, targeter)
		server.Close()
		if err != nil || report.Metrics.Requests == 0 || report.SuccessRate == nil || *report.SuccessRate != 0 || len(report.Metrics.Errors) == 0 {
			t.Fatalf("missing failures: %+v, %v", report, err)
		}
		if !slow && report.Metrics.BytesIn.Total != report.Metrics.Requests*1024 {
			t.Fatalf("lost partial body bytes: %+v", report.Metrics)
		}
	}
}

func TestConcurrencyDrainAndInterruption(t *testing.T) {
	var active, peak, count atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		count.Add(1)
		select {
		case <-time.After(100 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(206)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	o := testOptions(server.URL)
	o.Rate, o.InitialWorkers, o.MaxWorkers = 200, 2, 2
	started := time.Now()
	report, err := Execute(context.Background(), o, targeter)
	if err != nil || peak.Load() != 2 || report.Metrics.Requests != uint64(count.Load()) || *report.SuccessRate != 1 {
		t.Fatalf("concurrency/counts: %+v peak=%d count=%d err=%v", report, peak.Load(), count.Load(), err)
	}
	if report.Metrics.Rate >= float64(o.Rate) || time.Since(started) < o.Duration {
		t.Fatal("reported target rate or skipped drain")
	}
	before := count.Load()
	time.Sleep(30 * time.Millisecond)
	if count.Load() != before {
		t.Fatal("requests survived execution")
	}
	ctx, cancel := context.WithCancel(context.Background())
	o.Duration = 5 * time.Second
	timer := time.AfterFunc(50*time.Millisecond, cancel)
	defer timer.Stop()
	started = time.Now()
	report, err = Execute(ctx, o, targeter)
	if !errors.Is(err, context.Canceled) || !report.Interrupted || report.Canceled == 0 || time.Since(started) > time.Second {
		t.Fatalf("interruption: %+v %v", report, err)
	}
}

func TestTargetFailureDoesNotInventHTTPRequests(t *testing.T) {
	want := errors.New("bad target")
	o := testOptions("http://127.0.0.1:1")
	r, err := Execute(context.Background(), o, func(*vegeta.Target) error { return want })
	if !errors.Is(err, want) || r.Metrics.Requests != 0 || r.SuccessRate != nil || r.Consumed != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestAdmittedRequestDrainsAfterDuration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(206)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	o := testOptions(server.URL)
	o.Rate, o.InitialWorkers, o.MaxWorkers, o.Duration = 100, 1, 1, 40*time.Millisecond
	r, err := Execute(context.Background(), o, func(t *vegeta.Target) error {
		time.Sleep(80 * time.Millisecond)
		return targeter(t)
	})
	if err != nil || r.Consumed != 1 || r.Metrics.Requests != 1 || r.SuccessRate == nil || *r.SuccessRate != 1 {
		t.Fatalf("admitted plan item was lost at duration boundary: %+v %v", r, err)
	}
}

func TestOriginalModesAndConfiguration(t *testing.T) {
	base := []string{"TARGET_URL=http://source.test/blob", "FILE_SIZE=10", "CHUNK_SIZE=4", "STREAMS=2", "URL_COUNT=2"}
	for _, extra := range []string{"RATE=0", "RATE=+1", "DURATION=bad", "TIMEOUT=0s", "MAX_VUS=1", "SEED_CLIENT_CPUS=NaN", "MODE=unknown"} {
		if _, _, err := Original(append(append([]string{}, base...), extra)); err == nil {
			t.Fatalf("accepted %s", extra)
		}
	}
	o, next, err := Original(append(base, "MODE=sequential"))
	if err != nil || o.ExpectedStatus != 206 {
		t.Fatal(o, err)
	}
	wantRanges := []string{"bytes=0-3", "bytes=0-3", "bytes=4-7", "bytes=4-7", "bytes=8-9", "bytes=8-9", "bytes=0-3"}
	var urls []string
	for _, want := range wantRanges {
		var target vegeta.Target
		if err = next(&target); err != nil || target.Header.Get("Range") != want {
			t.Fatalf("%+v %v", target, err)
		}
		urls = append(urls, target.URL)
	}
	if urls[0] != urls[2] || urls[0] != urls[4] || urls[6] != urls[1] || urls[0] == urls[1] {
		t.Fatal(urls)
	}
	_, next, err = Original(append(base, "MODE=random"))
	if err != nil {
		t.Fatal(err)
	}
	var a, b vegeta.Target
	_ = next(&a)
	_ = next(&b)
	if a.URL == b.URL {
		t.Fatal("random mode reused a task")
	}
}
