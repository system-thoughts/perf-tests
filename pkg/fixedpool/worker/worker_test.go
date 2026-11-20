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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/target"
)

func baseEnv() []string {
	return []string{"MANIFEST=m", "PLAN=p", "TARGET_PREPARATION=t", "RATE=100"}
}
func TestEnvironmentDefaultsAndRejections(t *testing.T) {
	e, err := ReadEnvironment(baseEnv())
	if err != nil {
		t.Fatal(err)
	}
	if e.RunID != "" || e.WorkerID != 0 || e.Duration != 300*time.Second || e.Timeout != 30*time.Second || e.GracefulStop != 35*time.Second || e.PreAllocatedVUs != 64 || e.MaxVUs != 512 {
		t.Fatal(e)
	}
	for _, extra := range []string{"RATE=0", "RATE=+100", "RATE=1.5", "RATE=2147483648", "WORKER_ID=-1", "RUN_ID=../x", "DURATION=0s", "DURATION=1us", "DURATION=100ms", "DURATION=999ms", "TIMEOUT=bad", "TIMEOUT=9223372036854ms", "GRACEFUL_STOP=30s", "MAX_VUS=63", "PRE_ALLOCATED_VUS=0", "MODE=", "RANGE=0-10", "TARGET_URL=http://x", "PROXY_BENCH_UNKNOWN=x", "X_DRAGONFLY_TAG=x"} {
		t.Run(extra, func(t *testing.T) {
			if _, err := ReadEnvironment(append(baseEnv(), extra)); err == nil {
				t.Fatal("accepted invalid environment")
			}
		})
	}
	for _, extra := range []string{"RUN_ID=", "RUN_ID=custom-1", "K6_THRESHOLDS={}", "K6_NO_CONNECTION_REUSE=true", "K6_SCENARIOS={}"} {
		if _, err := ReadEnvironment(append(baseEnv(), extra)); err != nil {
			t.Fatalf("native k6 environment rejected: %v", err)
		}
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
	m := contract.Manifest{SchemaVersion: 1, Generator: contract.Generator{Version: contract.ContentAlgorithm, Seed: contract.ContentSeed, BlockBytes: 16}, ObjectSize: 64, ObjectCount: 2, Objects: []contract.Object{}}
	for i, ch := range []string{"a", "b"} {
		sha := strings.Repeat(ch, 64)
		m.Objects = append(m.Objects, contract.Object{ID: uint64(i), SHA256: sha})
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	manifestRaw := save(t, manifestPath, m)
	manifestID := contract.Digest(manifestRaw)
	shardRaw := save(t, filepath.Join(dir, "0.json"), []uint32{2, 0, 5, 1})
	p := contract.Plan{SchemaVersion: 1, ManifestSHA256: manifestID, RangeBytes: 16, RegionsPerObject: 4, RegionCount: 8, Algorithm: contract.OrderAlgorithm, Seed: contract.OrderSeed, WorkerCount: 2, Shards: []contract.Shard{{WorkerID: 0, RegionCount: 4, Path: "shards/0.json", SHA256: contract.Digest(shardRaw)}, {WorkerID: 1, RegionCount: 4, Path: "shards/1.json", SHA256: strings.Repeat("c", 64)}}}
	if err := os.Mkdir(filepath.Join(dir, "shards"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "0.json"), filepath.Join(dir, "shards/0.json")); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(dir, "request-plan.json")
	planRaw := save(t, planPath, p)
	prepared := target.Target{Proxy: "http://seed.test:4001", ExpectedServerIP: "192.0.2.1"}
	prepPath := filepath.Join(dir, "target-preparation.json")
	save(t, prepPath, target.Result{SchemaVersion: 1, ManifestSHA256: manifestID, Target: prepared, Source: target.Source{BaseURL: "http://origin.test"}, Status: target.StatusReady})
	env, err := ReadEnvironment(baseEnv())
	if err != nil {
		t.Fatal(err)
	}
	env.Manifest = manifestPath
	env.Plan = planPath
	env.Preparation = prepPath
	output := filepath.Join(dir, "worker-input.json")
	var logs bytes.Buffer
	if err := PrepareInput(ctx, env, output, &logs); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var spec Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.BaseURL != "http://origin.test" || spec.Manifest != manifestPath || spec.ManifestID != manifestID || spec.Proxy != prepared.Proxy || spec.Rate != 50 || spec.RegionCount != 4 || spec.WorkerID != 0 || spec.PlanID != contract.Digest(planRaw) {
		t.Fatalf("%+v", spec)
	}
	proxy, err := os.ReadFile(filepath.Join(dir, "proxy"))
	if err != nil || string(proxy) != prepared.Proxy+"\n" {
		t.Fatalf("proxy=%q err=%v", proxy, err)
	}
	if strings.Contains(logs.String(), Prefix) || strings.Count(logs.String(), "fixed-pool inputs checked") != 1 || !strings.Contains(logs.String(), "local_rate=50") {
		t.Fatal(logs.String())
	}
	if !strings.Contains(logs.String(), "B=64 N=2") || !strings.Contains(logs.String(), "R=16 W=2") {
		t.Fatal(logs.String())
	}
	if err := PrepareInput(ctx, env, filepath.Join(dir, "missing/input.json"), io.Discard); err == nil {
		t.Fatal("ignored output failure")
	}
}

func TestPreparationBindings(t *testing.T) {
	manifestID := strings.Repeat("a", 64)
	original := target.Result{SchemaVersion: 1, ManifestSHA256: manifestID, Target: target.Target{Proxy: "http://seed.test:4001", ExpectedServerIP: "192.0.2.1"}, Source: target.Source{BaseURL: "http://origin.test"}, Status: target.StatusReady}
	for _, field := range []string{"valid", "valid-null-observations", "status", "manifest_sha256", "proxy", "source", "schema_version"} {
		t.Run(field, func(t *testing.T) {
			value := original
			switch field {
			case "status":
				value.Status = "failed"
			case "manifest_sha256":
				value.ManifestSHA256 = strings.Repeat("b", 64)
			case "proxy":
				value.Target.Proxy = "abc"
			case "source":
				value.Source.BaseURL = "https://unsupported.test"
			case "schema_version":
				value.SchemaVersion = 0
			}
			raw, err := contract.Encode(value)
			if err != nil {
				t.Fatal(err)
			}
			if field == "valid-null-observations" {
				raw = bytes.Replace(raw, []byte(`"schema_version":1`), []byte(`"started_at":null,"ended_at":null,"preparation_id":null,"prepare":null,"object_count":null,"schema_version":1`), 1)
			}
			actual, err := CheckPreparation(raw, manifestID)
			if (err == nil) != strings.HasPrefix(field, "valid") {
				t.Fatalf("%+v %v", actual, err)
			}
			if strings.HasPrefix(field, "valid") && (actual.Target != original.Target || actual.Source != original.Source) {
				t.Fatal(actual)
			}
		})
	}
}
