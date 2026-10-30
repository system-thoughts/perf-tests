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

	"github.com/dragonflyoss/perf-tests/pkg/proxybench"
	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/contract"
	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/plan"
	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/target"
)

type Environment struct {
	Manifest, Plan, Preparation, TargetConfig, Proxy, RunID, ResultDir string
	WorkerID, Rate, PreAllocatedVUs, MaxVUs                            uint64
	Duration, Timeout, GracefulStop                                    time.Duration
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var decimal = regexp.MustCompile(`^[0-9]+$`)

func ReadEnvironment(environ []string) (e Environment, err error) {
	values := map[string]string{}
	for _, pair := range environ {
		// The shared image carries this telemetry flag for its ordinary k6 mode.
		// It has no effect on Vegeta; other k6 options must not be ignored.
		key, value, _ := strings.Cut(pair, "=")
		values[key] = value
		if key == "MODE" || key == "RANGE" || key == "TARGET_URL" || key == "FILE_SIZE" || key == "CHUNK_SIZE" || key == "STREAMS" || key == "URL_COUNT" || key == "SEED_CLIENT_CPUS" || key == "TAG" || key == "APPLICATION" || key == "CONTENT_ID" || key == "CONTENT_FOR_TASK_ID" || key == "PIECE_LENGTH" || key == "PREFETCH" || key == "HEADERS" || (strings.HasPrefix(key, "K6_") && key != "K6_NO_USAGE_REPORT") || strings.HasPrefix(key, "PROXY_BENCH_") || strings.HasPrefix(key, "X_DRAGONFLY_") {
			return e, fmt.Errorf("%s is not supported by fixed-pool", key)
		}
	}
	for _, key := range []string{"MANIFEST", "PLAN", "TARGET_PREPARATION", "TARGET_CONFIG", "HTTP_PROXY", "RUN_ID", "RATE"} {
		if values[key] == "" {
			return e, fmt.Errorf("%s is required", key)
		}
	}
	e.Manifest = values["MANIFEST"]
	e.Plan = values["PLAN"]
	e.Preparation = values["TARGET_PREPARATION"]
	e.TargetConfig = values["TARGET_CONFIG"]
	e.Proxy = values["HTTP_PROXY"]
	e.RunID = values["RUN_ID"]
	if !identifier.MatchString(e.RunID) {
		return e, fmt.Errorf("RUN_ID must be 1..128 ASCII letters/digits/._-, starting with a letter or digit")
	}
	if value, exists := values["RESULT_DIR"]; exists && value == "" {
		return e, fmt.Errorf("RESULT_DIR must not be empty")
	}
	e.ResultDir = values["RESULT_DIR"]
	if e.ResultDir == "" {
		e.ResultDir = "/results"
	}
	if e.ResultDir, err = filepath.Abs(e.ResultDir); err != nil {
		return e, err
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
		err = fmt.Errorf("DURATION must be at least 1s for fixed-pool")
	}
	if err == nil && (e.MaxVUs < e.PreAllocatedVUs || e.GracefulStop <= e.Timeout) {
		err = fmt.Errorf("MAX_VUS must be >= PRE_ALLOCATED_VUS and GRACEFUL_STOP must exceed TIMEOUT")
	}
	return e, err
}

func PrepareInput(ctx context.Context, e Environment, scripts string, stdout io.Writer) error {
	started := time.Now()
	m, manifestRaw, err := contract.ReadManifest(e.Manifest)
	if err != nil {
		return err
	}
	manifestID := contract.Digest(manifestRaw)
	p, planRaw, _, err := plan.LoadWorker(ctx, e.Plan, m, manifestID, e.WorkerID)
	if err != nil {
		return contract.Tag(contract.ContractMismatch, fmt.Errorf("PLAN: %w", err))
	}
	c, identity, err := target.ReadConfig(e.TargetConfig)
	if err != nil {
		return err
	}
	if e.Proxy != c.Target.Proxy {
		return contract.Tag(contract.ContractMismatch, fmt.Errorf("HTTP_PROXY differs from target.proxy"))
	}
	prepRaw, err := os.ReadFile(e.Preparation)
	if err != nil {
		return contract.Tag(contract.InputInvalid, err)
	}
	if err = CheckPreparation(prepRaw, e.Preparation, m, manifestID, c, identity); err != nil {
		return contract.Tag(contract.ContractMismatch, err)
	}
	counts := make([]uint64, p.WorkerCount)
	for i, s := range p.Shards {
		counts[i] = s.RegionCount
	}
	rates, err := plan.AllocateRates(counts, e.Rate)
	if err != nil {
		return contract.Tag(contract.InputInvalid, err)
	}
	versions := map[string]string{"worker_support": "fixed-pool-v1", "go": runtime.Version(), "vegeta": proxybench.Version()}
	for _, name := range []string{"run-benchmark.sh"} {
		raw, err := os.ReadFile(filepath.Join(scripts, name))
		if err != nil {
			return err
		}
		versions[name+"_sha256"] = contract.Digest(raw)
	}
	configRaw, err := os.ReadFile(e.TargetConfig)
	if err != nil {
		return err
	}
	run := RunSpec{SchemaVersion: 1, RunID: e.RunID, Manifest: contract.FileRef{Path: "dataset-manifest.json", SHA256: manifestID}, TargetConfig: contract.FileRef{Path: "target-config.yaml", SHA256: contract.Digest(configRaw)}, TargetPreparation: contract.FileRef{Path: "target-preparation.json", SHA256: contract.Digest(prepRaw)}, Plan: contract.FileRef{Path: "plan/request-plan.json", SHA256: contract.Digest(planRaw)}, IdentitySHA256: identity, Target: c.Target, TotalRate: e.Rate, WorkerCount: p.WorkerCount, DurationMS: e.Duration.Milliseconds(), TimeoutMS: e.Timeout.Milliseconds(), GracefulStopMS: e.GracefulStop.Milliseconds(), Workers: []Allocation{}, ToolVersions: versions}
	for i, s := range p.Shards {
		run.Workers = append(run.Workers, Allocation{s.WorkerID, s.RegionCount, rates[i]})
	}
	headers := map[string]string{}
	for key, value := range target.FixedHeaders() {
		headers[key] = value[0]
	}
	objects := p.ObjectsFile
	objects.Path, err = filepath.Abs(filepath.Join(filepath.Dir(e.Plan), objects.Path))
	if err != nil {
		return err
	}
	shard := contract.FileRef{Path: p.Shards[e.WorkerID].Path, SHA256: p.Shards[e.WorkerID].SHA256}
	shard.Path, err = filepath.Abs(filepath.Join(filepath.Dir(e.Plan), shard.Path))
	if err != nil {
		return err
	}
	spec := Spec{SchemaVersion: 1, RunID: e.RunID, WorkerID: e.WorkerID, PlanID: run.Plan.SHA256, ManifestID: manifestID, ObjectsFile: objects, Shard: shard, RegionCount: counts[e.WorkerID], RangeBytes: p.RangeBytes, RegionsPerObject: p.RegionsPerObject, Proxy: e.Proxy, Headers: headers, Rate: rates[e.WorkerID], DurationMS: run.DurationMS, TimeoutMS: run.TimeoutMS, GracefulStopMS: run.GracefulStopMS, PreAllocatedVUs: e.PreAllocatedVUs, MaxVUs: e.MaxVUs}
	if err = claim(e.ResultDir); err != nil {
		return err
	}
	if err = Publish(e.ResultDir, "run-spec.json", run); err != nil {
		return err
	}
	if err = Publish(e.ResultDir, "worker-spec.json", spec); err != nil {
		return err
	}
	if err = Emit(stdout, "RUN_SPEC", run); err != nil {
		return err
	}
	if err = Emit(stdout, "WORKER_SPEC", spec); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Input checks complete: regions=%d shard_regions=%d objects=%d elapsed_ms=%d\n", p.RegionCount, spec.RegionCount, m.ObjectCount, time.Since(started).Milliseconds())
	return err
}
func claim(directory string) error {
	info, err := os.Stat(directory)
	if err == nil && !info.IsDir() {
		return contract.Tag(contract.DataConflict, fmt.Errorf("RESULT_DIR already exists and is not a directory"))
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(directory, ".worker-claim"), 0700); err != nil {
		if os.IsExist(err) {
			return contract.Tag(contract.DataConflict, err)
		}
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(entries) != 1 {
		return contract.Tag(contract.DataConflict, fmt.Errorf("RESULT_DIR must be empty; existing results are never overwritten"))
	}
	return nil
}
