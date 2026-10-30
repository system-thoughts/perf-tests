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
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/contract"
	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/dataset"
	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/plan"
	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/target"
)

func baseEnv() []string {
	return []string{"MANIFEST=m", "PLAN=p", "TARGET_PREPARATION=t", "TARGET_CONFIG=c", "HTTP_PROXY=http://seed:4001", "RATE=100", "RUN_ID=run-1"}
}
func TestEnvironmentDefaultsAndRejections(t *testing.T) {
	e, err := ReadEnvironment(baseEnv())
	if err != nil {
		t.Fatal(err)
	}
	if e.WorkerID != 0 || e.Duration != 300*time.Second || e.Timeout != 30*time.Second || e.GracefulStop != 35*time.Second || e.PreAllocatedVUs != 64 || e.MaxVUs != 512 || e.ResultDir != "/results" {
		t.Fatal(e)
	}
	for _, extra := range []string{"RESULT_DIR=", "RATE=0", "RATE=+100", "RATE=1.5", "RATE=2147483648", "WORKER_ID=-1", "RUN_ID=../x", "DURATION=0s", "DURATION=1us", "DURATION=100ms", "DURATION=999ms", "TIMEOUT=bad", "TIMEOUT=9223372036854ms", "GRACEFUL_STOP=30s", "MAX_VUS=63", "PRE_ALLOCATED_VUS=0", "MODE=", "RANGE=0-10", "TARGET_URL=http://x", "PROXY_BENCH_UNKNOWN=x", "X_DRAGONFLY_TAG=x"} {
		t.Run(extra, func(t *testing.T) {
			if _, err := ReadEnvironment(append(baseEnv(), extra)); err == nil {
				t.Fatal("accepted invalid environment")
			}
		})
	}
	for _, extra := range []string{"K6_THRESHOLDS={}", "K6_NO_CONNECTION_REUSE=true", "K6_SCENARIOS={}"} {
		if _, err := ReadEnvironment(append(baseEnv(), extra)); err == nil {
			t.Fatal("accepted unsupported K6 environment")
		}
	}
	if _, err := ReadEnvironment(append(baseEnv(), "K6_NO_USAGE_REPORT=true")); err != nil {
		t.Fatalf("image telemetry default rejected: %v", err)
	}
	e, err = ReadEnvironment(append(baseEnv(), "TIMEOUT=100ms", "DURATION=5s"))
	if err != nil || e.GracefulStop != 5100*time.Millisecond {
		t.Fatalf("%+v %v", e, err)
	}
	for _, value := range []string{"1s", "1001ms"} {
		if _, err := ReadEnvironment(append(baseEnv(), "DURATION="+value)); err != nil {
			t.Fatalf("rejected supported duration %s: %v", value, err)
		}
	}
}
func TestExitPriority(t *testing.T) {
	cases := []struct {
		codes []string
		want  int
	}{{[]string{}, 0}, {[]string{"COVERAGE_UNVERIFIED"}, 0}, {[]string{"RESULT_INCOMPLETE"}, 4}, {[]string{"RESULT_INCOMPLETE", "WORKER_FAILED"}, 3}, {[]string{"WORKER_FAILED", "INPUT_INVALID", "RESULT_INCOMPLETE"}, 2}, {[]string{"INPUT_INVALID", "INTERRUPTED", "WORKER_FAILED"}, 130}}
	for _, c := range cases {
		issues := []Issue{}
		for _, code := range c.codes {
			issues = append(issues, Issue{Code: code})
		}
		if ExitCode(issues) != c.want {
			t.Fatal(c)
		}
	}
}

func TestResultClaimDistinguishesConflictsFromIO(t *testing.T) {
	dir := t.TempDir()
	if err := claim(dir); err != nil {
		t.Fatal(err)
	}
	if err := claim(dir); err == nil || contract.ErrorCode(err) != contract.DataConflict {
		t.Fatalf("existing claim must be a data conflict: %v", err)
	}
	occupied := t.TempDir()
	if err := os.WriteFile(filepath.Join(occupied, "existing-result"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := claim(occupied); err == nil || contract.ErrorCode(err) != contract.DataConflict {
		t.Fatalf("existing result must be a data conflict: %v", err)
	}
	// An unusable ancestor fails directory creation, rather than claiming old
	// output. Leave it untagged so the worker maps I/O failure to exit 3.
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := claim(blocked); err == nil || contract.ErrorCode(err) != contract.DataConflict {
		t.Fatalf("existing output file must be a data conflict: %v", err)
	}
	if err := claim(filepath.Join(blocked, "results")); err == nil || contract.ErrorCode(err) == contract.DataConflict {
		t.Fatalf("I/O failure mislabeled as existing results: %v", err)
	}
}
func save(t *testing.T, name string, value any) []byte {
	t.Helper()
	raw, err := contract.Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(name, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestPreparedInputsAreBoundAndLocalShardOnly(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	data, err := dataset.Prepare(ctx, dataset.Spec{Root: filepath.Join(dir, "dataset"), Size: 64, Count: 2, BaseURL: "http://origin.test", GenerationWorkers: 1})
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := os.ReadFile(data.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Prepare(ctx, data.Manifest, manifestRaw, plan.Spec{Directory: filepath.Join(dir, "plans"), RangeBytes: 16, Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	rules := save(t, filepath.Join(dir, "proxy-rules.json"), target.ProxyRules{SchemaVersion: 1, FilteredQueryParams: []string{}})
	config := target.Config{SchemaVersion: 1, Target: target.Target{Ref: "seed", Proxy: "http://seed.test:4001", ExpectedServerIP: "192.0.2.1", InstanceRef: "test-instance"}, ProxyRulesRef: contract.FileRef{Path: "proxy-rules.json", SHA256: contract.Digest(rules)}, Prepare: target.Options{Concurrency: 1, RequestTimeoutMS: 1000, ReadyTimeoutMS: 1000}}
	// JSON is a strict subset of YAML, accepted by the target-config reader.
	configPath := filepath.Join(dir, "target-config.yaml")
	save(t, configPath, config)
	_, identity, err := target.ReadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	begin := "2026-10-19T00:00:00Z"
	full := "2026-10-19T00:00:01Z"
	finished := "2026-10-19T00:00:02Z"
	end := "2026-10-19T00:00:03Z"
	objects := target.ObjectResults{SchemaVersion: 1, Objects: []target.ObjectResult{}}
	for i, o := range data.Manifest.Objects {
		task := strings.Repeat(string(rune('a'+i)), 64)
		digest := o.SHA256
		size := o.Size
		ip := config.Target.ExpectedServerIP
		objects.Objects = append(objects.Objects, target.ObjectResult{ObjectID: o.ID, TaskID: &task, ServerIP: &ip, ReceivedBytes: &size, SHA256: &digest, FullReadCompletedAt: &full, FinishedCheckAt: &finished, Status: target.StatusReady})
	}
	evidenceRaw := save(t, filepath.Join(dir, "target-object-results.json"), objects)
	evidenceRef := contract.FileRef{Path: "target-object-results.json", SHA256: contract.Digest(evidenceRaw)}
	manifestID := data.ManifestSHA256
	prep := target.Result{SchemaVersion: 1, PreparationID: "test-preparation", ManifestSHA256: &manifestID, IdentitySHA256: &identity, PieceStrategy: "auto", Target: &config.Target, Prepare: &config.Prepare, Method: target.Method, StartedAt: begin, EndedAt: end, Status: target.StatusReady, ObjectCount: &data.Manifest.ObjectCount, EvidenceFile: &evidenceRef, Errors: []target.Issue{}}
	prepPath := filepath.Join(dir, "target-preparation.json")
	prepRaw := save(t, prepPath, prep)
	if err = CheckPreparation(prepRaw, prepPath, data.Manifest, manifestID, config, identity); err != nil {
		t.Fatal(err)
	}
	// Opaque evidence need not be semantically audited by every worker. Its
	// published hash must still match, even if summary bindings are unchanged.
	opaque := []byte("producer-owned evidence\n")
	if err = os.WriteFile(filepath.Join(dir, evidenceRef.Path), opaque, 0600); err != nil {
		t.Fatal(err)
	}
	if err = CheckPreparation(prepRaw, prepPath, data.Manifest, manifestID, config, identity); err == nil {
		t.Fatal("ignored evidence digest")
	}
	prep.EvidenceFile.SHA256 = contract.Digest(opaque)
	prepRaw = save(t, prepPath, prep)
	if err = CheckPreparation(prepRaw, prepPath, data.Manifest, manifestID, config, identity); err != nil {
		t.Fatalf("worker re-audited producer evidence: %v", err)
	}
	for _, field := range []string{"status", "manifest_sha256", "identity_sha256", "object_count", "method", "schema_version", "unknown"} {
		var value map[string]any
		if err = json.Unmarshal(prepRaw, &value); err != nil {
			t.Fatal(err)
		}
		switch field {
		case "object_count", "schema_version":
			value[field] = 17
		default:
			value[field] = "wrong"
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = CheckPreparation(raw, prepPath, data.Manifest, manifestID, config, identity); err == nil {
			t.Fatalf("accepted changed %s", field)
		}
	}
	// Worker zero needs no peer shard file.
	if err = os.Remove(filepath.Join(filepath.Dir(p.Path), "shards/1.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = plan.LoadWorker(ctx, p.Path, data.Manifest, manifestID, 0); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err = os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	// Preparing inputs must never invoke the engine, including a version probe.
	probe := filepath.Join(dir, "k6-invoked")
	if err = os.WriteFile(filepath.Join(bin, "k6"), []byte("#!/bin/sh\nprintf invoked > '"+probe+"'\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	scripts := filepath.Join(dir, "scripts")
	if err = os.MkdirAll(filepath.Join(scripts, "lib"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"run-benchmark.sh"} {
		contents := []byte("fixture")
		if err = os.WriteFile(filepath.Join(scripts, name), contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	env, err := ReadEnvironment(baseEnv())
	if err != nil {
		t.Fatal(err)
	}
	env.Manifest = data.ManifestPath
	env.Plan = p.Path
	env.Preparation = prepPath
	env.TargetConfig = configPath
	env.Proxy = config.Target.Proxy
	env.ResultDir = filepath.Join(dir, "results")
	if err = PrepareInput(ctx, env, scripts, io.Discard); err != nil {
		t.Fatal(err)
	}
	run, spec, err := ReadSpec(env.ResultDir)
	if err != nil {
		t.Fatal(err)
	}
	if run.TotalRate != 100 || run.WorkerCount != 2 || spec.Rate != 50 || spec.RegionCount != 4 || filepath.IsAbs(run.Manifest.Path) {
		t.Fatalf("%+v %+v", run, spec)
	}
	if _, exists := run.ToolVersions["k6"]; exists {
		t.Fatal("input preparation must not add engine version metadata")
	}
	if _, err = os.Stat(probe); !os.IsNotExist(err) {
		t.Fatalf("input preparation invoked k6: %v", err)
	}
	if err = PrepareInput(ctx, env, scripts, io.Discard); err == nil || contract.ErrorCode(err) != contract.DataConflict {
		t.Fatal("overwrote existing worker results", err)
	}
}
