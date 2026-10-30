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

package report

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/contract"
	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/worker"
)

func fixture(t *testing.T) (worker.RunSpec, worker.Spec, *NativeSummary, *worker.Execution) {
	t.Helper()
	// Native rates deliberately differ from configured RATE/DURATION.
	raw := `{"schema_version":1,"run_id":"test","worker_id":0,"manifest_id":"m","plan_id":"p","engine":"vegeta","engine_version":"v12.13.0","plan_consumed":20,"expected_status_successes":15,"expected_status_success_ratio":0.75,"native_summary":{"duration":5000000000,"wait":250000000,"requests":20,"rate":3.8095238095238093,"success":1,"latencies":{"50th":12000000,"95th":40000000,"99th":91000000,"mean":17000000,"max":100000000},"bytes_in":{"total":100001}}}`
	var n NativeSummary
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	timestamp := "2026-10-30T00:00:00Z"
	code := 0
	return worker.RunSpec{SchemaVersion: 1, RunID: "test", WorkerCount: 1}, worker.Spec{SchemaVersion: 1, RunID: "test", ManifestID: "m", PlanID: "p", RegionCount: 3, RangeBytes: 1024}, &n, &worker.Execution{SchemaVersion: 1, RunID: "test", ProcessStartedAt: &timestamp, ProcessEndedAt: &timestamp, ExitCode: &code, TerminationReason: worker.Completed, Errors: []worker.Issue{}}
}
func TestNativeMappingAndCycles(t *testing.T) {
	run, spec, n, e := fixture(t)
	r := Derive(run, spec, n, e)
	if !r.ResultComplete || len(r.Errors) != 0 || r.ExecutionState != worker.Completed {
		t.Fatalf("%+v", r)
	}
	for key, want := range map[string]float64{"ACTUAL_QPS": 3.8095238095238093, "REQUESTS_TOTAL": 20, "SUCCESS_PCT": 75, "P99_MS": 91, "P50_MS": 12, "P95_MS": 40, "MEAN_MS": 17, "MAX_MS": 100, "TOOL_DURATION_S": 5.25, "DATA_RECEIVED_BYTES": 100001} {
		if got := r.Workers[0].Metrics[key]; got == nil || *got != want {
			t.Errorf("%s=%v want %v", key, got, want)
		}
	}
	if r.Workers[0].Metrics["DROPPED_ITERATIONS"] != nil || *r.Workers[0].SuccessfulSamples != 15 || *r.Workers[0].FailedSamples != 5 || r.Coverage.UniqueRegions != 3 || r.Coverage.LogicalBytes != 3072 {
		t.Fatal(r)
	}
	// Version is provenance, not a gate.
	n.EngineVersion = "other"
	if !Derive(run, spec, n, e).ResultComplete {
		t.Fatal("version gate")
	}
}
func TestMissingNativeValuesRemainUnknown(t *testing.T) {
	for _, mode := range []string{"rate", "requests", "duration", "wait", "latencies", "bytes_in", "wrong-run", "no-summary", "no-execution", "fractional-count", "missing-consumed", "missing-success", "invalid-success"} {
		t.Run(mode, func(t *testing.T) {
			run, spec, n, e := fixture(t)
			switch mode {
			case "wrong-run":
				n.RunID = "other"
			case "no-summary":
				n = nil
			case "no-execution":
				e = nil
			case "fractional-count":
				n.Native["requests"] = 1.5
			case "missing-consumed":
				n.Consumed = nil
			case "missing-success":
				n.Succeeded = nil
			case "invalid-success":
				v := 2.0
				n.SuccessRate = &v
			default:
				delete(n.Native, mode)
			}
			r := Derive(run, spec, n, e)
			if r.ResultComplete || r.Coverage != nil || worker.ExitCode(r.Errors) != 4 {
				t.Fatalf("%+v", r)
			}
		})
	}
}
func TestExecutionAndCoverage(t *testing.T) {
	for _, mode := range []string{"interrupted", "failed", "inconsistent", "multi-worker", "zero"} {
		t.Run(mode, func(t *testing.T) {
			run, spec, n, e := fixture(t)
			switch mode {
			case "interrupted":
				e.TerminationReason = worker.Interrupted
				n.Interrupted = true
			case "failed":
				code := 3
				e.ExitCode = &code
			case "inconsistent":
				*n.Consumed = 19
			case "multi-worker":
				run.WorkerCount = 2
			case "zero":
				*n.Consumed = 0
				*n.Succeeded = 0
				n.SuccessRate = nil
				n.Native["requests"] = float64(0)
			}
			r := Derive(run, spec, n, e)
			switch mode {
			case "multi-worker":
				if r.Aggregate != nil || r.Coverage != nil || r.Workers[0].Coverage == nil {
					t.Fatal(r)
				}
			case "zero":
				if !r.ResultComplete || r.Coverage.UniqueRegions != 0 || r.Workers[0].Metrics["P99_MS"] != nil || r.Workers[0].Metrics["SUCCESS_PCT"] != nil {
					t.Fatal(r)
				}
			default:
				if r.Coverage != nil || r.ExecutionState == worker.Completed {
					t.Fatal(r)
				}
			}
		})
	}
}

func TestPublicationFailurePreservesPrimaryError(t *testing.T) {
	for _, code := range []string{"INTERRUPTED", "CONTRACT_MISMATCH", "RESULT_INCOMPLETE"} {
		want := 3
		if code == "INTERRUPTED" {
			want = 130
		}
		if code == "CONTRACT_MISMATCH" {
			want = 2
		}
		// Missing specs forces a publication/report failure; the original execution
		// error must remain present and its higher priority must not be discarded.
		var output strings.Builder
		got := Write(t.TempDir(), worker.Execution{Errors: []worker.Issue{{Code: code, Stage: "execution", Message: "original"}}}, &output)
		if got != want || !strings.Contains(output.String(), code) || !strings.Contains(output.String(), "WORKER_FAILED") {
			t.Fatalf("%s code=%d output=%s", code, got, output.String())
		}
	}
}

func TestVegetaRunAndArchive(t *testing.T) {
	dir := t.TempDir()
	var requests []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.String()+" "+r.Header.Get("Range"))
		if len(requests)%4 == 0 {
			w.WriteHeader(http.StatusBadGateway)
		} else {
			w.WriteHeader(http.StatusPartialContent)
		}
		_, _ = w.Write([]byte("12345678"))
	}))
	defer proxy.Close()
	objects := contract.FileRef{Path: filepath.Join(dir, "objects.json")}
	shard := contract.FileRef{Path: filepath.Join(dir, "shard.json")}
	write := func(name string, v any) {
		t.Helper()
		if err := worker.Publish(dir, name, v); err != nil {
			t.Fatal(err)
		}
	}
	write("objects.json", []string{"http://source.test/one", "http://source.test/two"})
	write("shard.json", []uint32{3, 0, 2, 1})
	spec := worker.Spec{SchemaVersion: 1, RunID: "test", ManifestID: "m", PlanID: "p", ObjectsFile: objects, Shard: shard, RegionCount: 4, RegionsPerObject: 2, RangeBytes: 8, Proxy: proxy.URL, Headers: map[string]string{}, Rate: 20, PreAllocatedVUs: 1, MaxVUs: 1, DurationMS: 1000, TimeoutMS: 100, GracefulStopMS: 200}
	write("worker-spec.json", spec)
	write("run-spec.json", worker.RunSpec{SchemaVersion: 1, RunID: "test", WorkerCount: 1, Workers: []worker.Allocation{}, ToolVersions: map[string]string{}, Manifest: contract.FileRef{SHA256: "m"}, Plan: contract.FileRef{SHA256: "p"}})
	var out strings.Builder
	if err := worker.Run(context.Background(), filepath.Join(dir, "worker-spec.json"), &out); err != nil {
		t.Fatal(err)
	}
	_, _, _, e := fixture(t)
	if code := Write(dir, *e, &out); code != 0 {
		t.Fatalf("code=%d: %s", code, out.String())
	}
	var result Result
	if err := worker.ReadRecord(filepath.Join(dir, "worker-summary.json"), &result); err != nil {
		t.Fatal(err)
	}
	n := len(requests)
	if n < 10 || !result.ResultComplete || *result.Aggregate["REQUESTS_TOTAL"] != float64(n) || *result.Aggregate["DATA_RECEIVED_BYTES"] != float64(n*8) || *result.Aggregate["SUCCESS_PCT"] != 100*(1-float64(n/4)/float64(n)) {
		t.Fatalf("n=%d result=%+v", n, result)
	}
	cycle := []string{"http://source.test/two bytes=8-15", "http://source.test/one bytes=0-7", "http://source.test/two bytes=0-7", "http://source.test/one bytes=8-15"}
	for i, got := range requests {
		if want := cycle[i%4]; got != want {
			t.Fatalf("request %d: %s want %s", i, got, want)
		}
	}
	if _, ok := result.References["vegeta-summary.json"]; !ok {
		t.Fatal("missing native archive reference")
	}
	if err := worker.Run(context.Background(), filepath.Join(dir, "worker-spec.json"), io.Discard); err == nil {
		t.Fatal("overwrote existing native summary")
	}
}
