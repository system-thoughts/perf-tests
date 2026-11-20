/*
 *     Copyright 2026 The Dragonfly Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package worker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/plan"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/target"
)

type Environment struct {
	Manifest, Plan, Preparation, RunID      string
	WorkerID, Rate, PreAllocatedVUs, MaxVUs uint64
	Duration, Timeout, GracefulStop         time.Duration
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var decimal = regexp.MustCompile(`^[0-9]+$`)

func ReadEnvironment(environ []string) (e Environment, err error) {
	values := map[string]string{}
	for _, pair := range environ {
		key, value, _ := strings.Cut(pair, "=")
		values[key] = value
		if key == "MODE" || key == "RANGE" || key == "TARGET_URL" || key == "FILE_SIZE" || key == "CHUNK_SIZE" || key == "STREAMS" || key == "URL_COUNT" || key == "SEED_CLIENT_CPUS" || key == "TAG" || key == "APPLICATION" || key == "CONTENT_ID" || key == "CONTENT_FOR_TASK_ID" || key == "PIECE_LENGTH" || key == "PREFETCH" || key == "HEADERS" || strings.HasPrefix(key, "PROXY_BENCH_") || strings.HasPrefix(key, "X_DRAGONFLY_") {
			return e, fmt.Errorf("%s is not supported by fixed-pool", key)
		}
	}
	for _, key := range []string{"MANIFEST", "PLAN", "TARGET_PREPARATION", "RATE"} {
		if values[key] == "" {
			return e, fmt.Errorf("%s is required", key)
		}
	}
	e.Manifest = values["MANIFEST"]
	e.Plan = values["PLAN"]
	e.Preparation = values["TARGET_PREPARATION"]
	e.RunID = values["RUN_ID"]
	if e.RunID != "" && !identifier.MatchString(e.RunID) {
		return e, fmt.Errorf("RUN_ID must be 1..128 ASCII letters/digits/._-, starting with a letter or digit")
	}
	integer := func(key string, fallback, minValue, maxValue uint64) uint64 {
		if err != nil {
			return 0
		}
		raw, exists := values[key]
		if !exists {
			return fallback
		}
		n, parseErr := strconv.ParseUint(raw, 10, 64)
		if !decimal.MatchString(raw) || parseErr != nil || n < minValue || n > maxValue {
			err = fmt.Errorf("%s must be a decimal integer in [%d,%d]", key, minValue, maxValue)
		}
		return n
	}
	e.WorkerID = integer("WORKER_ID", 0, 0, contract.MaxRegions)
	e.Rate = integer("RATE", 0, 1, contract.MaxRate)
	e.PreAllocatedVUs = integer("PRE_ALLOCATED_VUS", 64, 1, contract.MaxRate)
	e.MaxVUs = integer("MAX_VUS", 512, 1, contract.MaxRate)
	duration := func(key string, fallback time.Duration) time.Duration {
		if err != nil {
			return 0
		}
		raw, exists := values[key]
		if !exists {
			return fallback
		}
		d, parseErr := time.ParseDuration(raw)
		if parseErr != nil || d <= 0 || d%time.Millisecond != 0 {
			err = fmt.Errorf("%s must be a positive duration with whole millisecond precision", key)
		}
		return d
	}
	e.Duration = duration("DURATION", 300*time.Second)
	e.Timeout = duration("TIMEOUT", 30*time.Second)
	if e.Timeout > time.Duration(1<<63-1)-5*time.Second {
		return e, fmt.Errorf("TIMEOUT leaves no representable graceful stop")
	}
	e.GracefulStop = duration("GRACEFUL_STOP", e.Timeout+5*time.Second)
	if err == nil && e.Duration < time.Second {
		err = fmt.Errorf("DURATION must be at least 1s for the constant-arrival-rate executor")
	}
	if err == nil && (e.MaxVUs < e.PreAllocatedVUs || e.GracefulStop <= e.Timeout) {
		err = fmt.Errorf("MAX_VUS must be >= PRE_ALLOCATED_VUS and GRACEFUL_STOP must exceed TIMEOUT")
	}
	return e, err
}

func PrepareInput(ctx context.Context, e Environment, output string, stdout io.Writer) error {
	started := time.Now()
	m, manifestRaw, err := contract.ReadManifest(e.Manifest)
	if err != nil {
		return err
	}
	manifestID := contract.Digest(manifestRaw)
	p, planRaw, _, err := plan.LoadWorker(ctx, e.Plan, m, manifestID, e.WorkerID)
	if err != nil {
		return contract.Tag(contract.ContractMismatch, err)
	}
	prepRaw, err := os.ReadFile(e.Preparation)
	if err != nil {
		return contract.Tag(contract.InputInvalid, contract.Input("TARGET_PREPARATION", e.Preparation, err))
	}
	prepared, err := CheckPreparation(prepRaw, manifestID)
	if err != nil {
		return contract.Tag(contract.ContractMismatch, contract.Input("TARGET_PREPARATION", e.Preparation, err))
	}
	counts := make([]uint64, p.WorkerCount)
	for i, s := range p.Shards {
		counts[i] = s.RegionCount
	}
	rates, err := plan.AllocateRates(counts, e.Rate)
	if err != nil {
		return contract.Tag(contract.InputInvalid, err)
	}
	headers := map[string]string{}
	for key, value := range target.FixedHeaders() {
		headers[key] = value[0]
	}
	manifestPath, err := filepath.Abs(e.Manifest)
	if err != nil {
		return err
	}
	shardPath, err := filepath.Abs(filepath.Join(filepath.Dir(e.Plan), p.Shards[e.WorkerID].Path))
	if err != nil {
		return err
	}
	spec := Spec{RunID: e.RunID, WorkerID: e.WorkerID, PlanID: contract.Digest(planRaw), ManifestID: manifestID, Manifest: manifestPath, Shard: shardPath, RegionCount: counts[e.WorkerID], RangeBytes: p.RangeBytes, RegionsPerObject: p.RegionsPerObject, Proxy: prepared.Proxy, Headers: headers, Rate: rates[e.WorkerID], DurationMS: e.Duration.Milliseconds(), TimeoutMS: e.Timeout.Milliseconds(), GracefulStopMS: e.GracefulStop.Milliseconds(), PreAllocatedVUs: e.PreAllocatedVUs, MaxVUs: e.MaxVUs}
	// This file is private to one invocation. It is consumed only after the
	// helper succeeds; it is neither a published result nor a reusable input.
	raw, err := contract.Encode(spec)
	if err != nil {
		return err
	}
	if err = os.WriteFile(output, raw, 0600); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(filepath.Dir(output), "proxy"), []byte(prepared.Proxy+"\n"), 0600); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "fixed-pool inputs checked (%d ms)\n  Dataset: path=%q sha256=%s B=%d N=%d source=%q\n  Plan: path=%q sha256=%s R=%d W=%d regions=%d\n  Preparation: path=%q sha256=%s (recorded ready; current cache state is not rechecked)\n  Target: proxy=%q expected_server_ip=%s\n  Worker: id=%d shard=%q sha256=%s regions=%d\n  Load: total_rate=%d local_rate=%d duration=%s timeout=%s graceful_stop=%s pre_allocated_vus=%d max_vus=%d\n", time.Since(started).Milliseconds(), manifestPath, manifestID, m.ObjectSize, m.ObjectCount, m.BaseURL, e.Plan, spec.PlanID, p.RangeBytes, p.WorkerCount, p.RegionCount, e.Preparation, contract.Digest(prepRaw), prepared.Proxy, prepared.ExpectedServerIP, e.WorkerID, shardPath, p.Shards[e.WorkerID].SHA256, spec.RegionCount, e.Rate, spec.Rate, e.Duration, e.Timeout, e.GracefulStop, e.PreAllocatedVUs, e.MaxVUs)
	if err == nil && e.RunID != "" {
		_, err = fmt.Fprintf(stdout, "  Run ID: %s\n", e.RunID)
	}
	return err
}

func Emit(w io.Writer, kind string, value any) error {
	raw, err := contract.Encode(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s%s_V1 %s", Prefix, kind, raw)
	return err
}
