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
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/plan"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/target"
	"github.com/dragonflyoss/perf-tests/pkg/proxybench"
)

type Environment struct {
	Manifest, Plan, Preparation, TargetConfig, RunID string
	WorkerID, Rate, PreAllocatedVUs, MaxVUs          uint64
	Duration, Timeout, GracefulStop                  time.Duration
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
	for _, key := range []string{"MANIFEST", "PLAN", "TARGET_PREPARATION", "TARGET_CONFIG", "RUN_ID", "RATE"} {
		if values[key] == "" {
			return e, fmt.Errorf("%s is required", key)
		}
	}
	e.Manifest = values["MANIFEST"]
	e.Plan = values["PLAN"]
	e.Preparation = values["TARGET_PREPARATION"]
	e.TargetConfig = values["TARGET_CONFIG"]
	e.RunID = values["RUN_ID"]
	if !identifier.MatchString(e.RunID) {
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
		err = fmt.Errorf("DURATION must be at least 1s")
	}
	if err == nil && (e.MaxVUs < e.PreAllocatedVUs || e.GracefulStop <= e.Timeout) {
		err = fmt.Errorf("MAX_VUS must be >= PRE_ALLOCATED_VUS and GRACEFUL_STOP must exceed TIMEOUT")
	}
	return e, err
}

func PrepareInput(ctx context.Context, e Environment, scripts, output string, stdout io.Writer) error {
	started := time.Now()
	m, manifestRaw, err := contract.ReadManifest(e.Manifest)
	if err != nil {
		return err
	}
	manifestID := contract.Digest(manifestRaw)
	p, planRaw, _, err := plan.LoadWorker(ctx, e.Plan, m, manifestID, e.WorkerID)
	if err != nil {
		return contract.Tag(contract.ContractMismatch, contract.Input("PLAN", e.Plan, err))
	}
	c, err := target.ReadConfig(e.TargetConfig)
	if err != nil {
		return err
	}
	prepRaw, err := os.ReadFile(e.Preparation)
	if err != nil {
		return contract.Tag(contract.InputInvalid, contract.Input("TARGET_PREPARATION", e.Preparation, err))
	}
	if err = CheckPreparation(prepRaw, e.Preparation, m, manifestID, c); err != nil {
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
	versions := map[string]string{"worker_support": "fixed-pool-vegeta-v1", "go": runtime.Version(), "vegeta": proxybench.Version()}
	script, err := os.ReadFile(filepath.Join(scripts, "run-benchmark.sh"))
	if err != nil {
		return err
	}
	versions["run-benchmark.sh_sha256"] = contract.Digest(script)
	configRaw, err := os.ReadFile(e.TargetConfig)
	if err != nil {
		return err
	}
	run := RunSpec{SchemaVersion: 1, RunID: e.RunID, Manifest: contract.FileRef{Path: "dataset-manifest.json", SHA256: manifestID}, TargetConfig: contract.FileRef{Path: "target-config.yaml", SHA256: contract.Digest(configRaw)}, TargetPreparation: contract.FileRef{Path: "target-preparation.json", SHA256: contract.Digest(prepRaw)}, Plan: contract.FileRef{Path: "plan/request-plan.json", SHA256: contract.Digest(planRaw)}, Target: c.Target, TotalRate: e.Rate, WorkerCount: p.WorkerCount, DurationMS: e.Duration.Milliseconds(), TimeoutMS: e.Timeout.Milliseconds(), GracefulStopMS: e.GracefulStop.Milliseconds(), Workers: []Allocation{}, ToolVersions: versions}
	for i, s := range p.Shards {
		run.Workers = append(run.Workers, Allocation{s.WorkerID, s.RegionCount, rates[i]})
	}
	headers := map[string]string{}
	for key, value := range target.FixedHeaders() {
		headers[key] = value[0]
	}
	manifest := contract.FileRef{Path: e.Manifest, SHA256: manifestID}
	manifest.Path, err = filepath.Abs(e.Manifest)
	if err != nil {
		return err
	}
	shard := contract.FileRef{Path: p.Shards[e.WorkerID].Path, SHA256: p.Shards[e.WorkerID].SHA256}
	shard.Path, err = filepath.Abs(filepath.Join(filepath.Dir(e.Plan), shard.Path))
	if err != nil {
		return err
	}
	spec := Spec{SchemaVersion: 1, RunID: e.RunID, WorkerID: e.WorkerID, PlanID: run.Plan.SHA256, ManifestID: manifestID, Manifest: manifest, Shard: shard, RegionCount: counts[e.WorkerID], RangeBytes: p.RangeBytes, RegionsPerObject: p.RegionsPerObject, Proxy: c.Target.Proxy, Headers: headers, Rate: rates[e.WorkerID], DurationMS: run.DurationMS, TimeoutMS: run.TimeoutMS, GracefulStopMS: run.GracefulStopMS, PreAllocatedVUs: e.PreAllocatedVUs, MaxVUs: e.MaxVUs}
	// This file is private to one invocation. It is consumed only after the
	// helper succeeds; it is neither a published result nor a reusable input.
	raw, err := contract.Encode(spec)
	if err != nil {
		return err
	}
	if err = os.WriteFile(output, raw, 0600); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(filepath.Dir(output), "proxy"), []byte(c.Target.Proxy+"\n"), 0600); err != nil {
		return err
	}
	if err = Emit(stdout, "RUN_SPEC", run); err != nil {
		return err
	}
	if err = Emit(stdout, "WORKER_SPEC", spec); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Inputs checked: MANIFEST=%q sha256=%s B=%d N=%d source=%q PLAN=%q sha256=%s R=%d W=%d WORKER_ID=%d shard=%q count=%d TARGET_CONFIG=%q TARGET_PREPARATION=%q elapsed_ms=%d\n", manifest.Path, manifestID, m.ObjectSize, m.ObjectCount, m.BaseURL, e.Plan, spec.PlanID, p.RangeBytes, p.WorkerCount, e.WorkerID, shard.Path, spec.RegionCount, e.TargetConfig, e.Preparation, time.Since(started).Milliseconds())
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
