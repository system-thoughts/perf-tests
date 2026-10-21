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
	"encoding/json"
	"strings"
	"testing"

	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/worker"
)

func fixture(t *testing.T) (worker.RunSpec, worker.Spec, *NativeSummary, *worker.Execution) {
	t.Helper()
	// Values intentionally differ from configured RATE/DURATION. Never derive QPS
	// from requests / configured duration or infer sample counts from percentages.
	raw := `{"schema_version":1,"run_id":"test","worker_id":0,"manifest_id":"m","plan_id":"p","k6_version":"2.3.0","native_summary":{"state":{"testRunDurationMs":5250},"metrics":{"http_reqs":{"values":{"count":20,"rate":3.8095238095238093}},"plan_consumed":{"values":{"count":20}},"iterations":{"values":{"count":20}},"http_req_failed":{"values":{"rate":0.25,"passes":5,"fails":15}},"http_req_duration":{"values":{"med":12,"p(95)":40,"p(99)":91,"avg":17,"max":100}},"data_received":{"values":{"count":100001}}}}}`
	var n NativeSummary
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	timestamp := "2026-10-19T00:00:00Z"
	code := 0
	return worker.RunSpec{SchemaVersion: 1, RunID: "test", WorkerCount: 1}, worker.Spec{SchemaVersion: 1, RunID: "test", ManifestID: "m", PlanID: "p", RegionCount: 3, RangeBytes: 1024}, &n, &worker.Execution{SchemaVersion: 1, RunID: "test", ProcessStartedAt: &timestamp, ProcessEndedAt: &timestamp, ExitCode: &code, TerminationReason: worker.Completed, Errors: []worker.Issue{}}
}

func TestResultsDoNotCheckEngineVersion(t *testing.T) {
	for _, mode := range []string{"different-version", "mismatched", "missing-summary-version", "uninterpreted-metadata", "missing-native-field"} {
		t.Run(mode, func(t *testing.T) {
			run, spec, n, execution := fixture(t)
			n.K6Version = "9.8.7-fixture"
			run.ToolVersions = map[string]string{"k6": "legacy-engine"}
			switch mode {
			case "mismatched":
				run.ToolVersions["k6"] = "other-engine"
			case "missing-summary-version":
				n.K6Version = ""
			case "uninterpreted-metadata":
				n.K6Version = map[string]any{"old": true}
			case "missing-native-field":
				delete(n.Native, "state")
			}
			result := Derive(run, spec, n, execution)
			if result.ResultComplete != (mode != "missing-native-field") {
				t.Fatalf("%s: %+v", mode, result)
			}
		})
	}
}
func TestNativeMappingAndCycles(t *testing.T) {
	run, spec, n, e := fixture(t)
	r := Derive(run, spec, n, e)
	if !r.ResultComplete || len(r.Errors) != 0 || r.ExecutionState != worker.Completed {
		t.Fatalf("%+v", r)
	}
	for key, want := range map[string]float64{"ACTUAL_QPS": 3.8095238095238093, "REQUESTS_TOTAL": 20, "SUCCESS_PCT": 75, "P99_MS": 91, "P50_MS": 12, "P95_MS": 40, "MEAN_MS": 17, "MAX_MS": 100, "TOOL_DURATION_S": 5.25, "DATA_RECEIVED_BYTES": 100001, "DROPPED_ITERATIONS": 0} {
		if got := r.Workers[0].Metrics[key]; got == nil || *got != want {
			t.Errorf("%s=%v want %v", key, got, want)
		}
	}
	if *r.Workers[0].SuccessfulSamples != 15 || *r.Workers[0].FailedSamples != 5 || r.Coverage.UniqueRegions != 3 || r.Coverage.LogicalBytes != 3072 || r.Coverage.Ratio != 1 {
		t.Fatal(r)
	}
}
func TestUnknownCoverageEvenEqualCountsWhenInterrupted(t *testing.T) {
	for _, reason := range []string{worker.Interrupted, worker.Failed} {
		run, spec, n, e := fixture(t)
		e.TerminationReason = reason
		code := 130
		if reason == worker.Failed {
			code = 108
		}
		e.ExitCode = &code
		positions := map[int]bool{}
		for _, k := range []int{0, 2, 3} {
			positions[k%3] = true
		}
		if len(positions) != 2 {
			t.Fatal("counterexample must cover only two positions")
		}
		// Equal Counter/iterations/http counts cannot disprove holes such as IDs
		// 0,2,3 for Mw=3: count=3 yet only positions 0 and 2 consumed.
		for _, name := range []string{"http_reqs", "iterations", "plan_consumed"} {
			n.Native["metrics"].(map[string]any)[name].(map[string]any)["values"].(map[string]any)["count"] = float64(3)
		}
		r := Derive(run, spec, n, e)
		if r.Coverage != nil || r.Workers[0].Coverage != nil || !r.ResultComplete || r.ExecutionState != reason {
			t.Fatalf("%+v", r)
		}
		if len(r.Errors) != 1 || r.Errors[0].Code != "COVERAGE_UNVERIFIED" {
			t.Fatal(r.Errors)
		}
	}
}
func TestDroppedOmissionIsOnlyMissingZeroException(t *testing.T) {
	for _, mode := range []string{"missing-count", "fractional-count", "null-metric", "nonzero", "missing-duration", "wrong-run", "no-summary", "no-execution"} {
		t.Run(mode, func(t *testing.T) {
			run, spec, n, e := fixture(t)
			metrics := n.Native["metrics"].(map[string]any)
			switch mode {
			case "missing-count":
				metrics["dropped_iterations"] = map[string]any{"values": map[string]any{}}
			case "fractional-count":
				metrics["http_reqs"].(map[string]any)["values"].(map[string]any)["count"] = 1.5
			case "null-metric":
				metrics["dropped_iterations"] = nil
			case "nonzero":
				metrics["dropped_iterations"] = map[string]any{"values": map[string]any{"count": float64(17)}}
			case "missing-duration":
				delete(n.Native, "state")
			case "wrong-run":
				n.RunID = "other"
			case "no-summary":
				n = nil
			case "no-execution":
				e = nil
			}
			r := Derive(run, spec, n, e)
			if mode == "nonzero" {
				if !r.ResultComplete || *r.Workers[0].Metrics["DROPPED_ITERATIONS"] != 17 {
					t.Fatal(r)
				}
				return
			}
			if r.ResultComplete || r.Coverage != nil || worker.ExitCode(r.Errors) != 4 {
				t.Fatalf("%+v", r)
			}
			if mode == "no-execution" {
				dropped := r.Workers[0].Metrics["DROPPED_ITERATIONS"]
				if dropped == nil || *dropped != 0 {
					t.Fatal("complete native summary establishes zero dropped independently of execution facts")
				}
			} else if r.Workers[0].Metrics["DROPPED_ITERATIONS"] != nil {
				t.Fatal("missing or invalid native data filled with zero")
			}
		})
	}
}
func TestInterruptedConsumptionAndZeroSamples(t *testing.T) {
	run, spec, n, e := fixture(t)
	n.Native["metrics"].(map[string]any)["iterations"].(map[string]any)["values"].(map[string]any)["count"] = float64(19)
	r := Derive(run, spec, n, e)
	if r.ExecutionState == worker.Completed || r.Coverage != nil || worker.ExitCode(r.Errors) != 3 {
		t.Fatal(r)
	}
	run, spec, n, e = fixture(t)
	for _, name := range []string{"http_reqs", "iterations", "plan_consumed"} {
		n.Native["metrics"].(map[string]any)[name].(map[string]any)["values"].(map[string]any)["count"] = float64(0)
	}
	n.Native["metrics"].(map[string]any)["http_reqs"].(map[string]any)["values"].(map[string]any)["rate"] = float64(0)
	for key := range n.Native["metrics"].(map[string]any)["http_req_duration"].(map[string]any)["values"].(map[string]any) {
		n.Native["metrics"].(map[string]any)["http_req_duration"].(map[string]any)["values"].(map[string]any)[key] = float64(0)
	}
	vals := n.Native["metrics"].(map[string]any)["http_req_failed"].(map[string]any)["values"].(map[string]any)
	vals["passes"] = float64(0)
	vals["fails"] = float64(0)
	vals["rate"] = float64(0)
	r = Derive(run, spec, n, e)
	if !r.ResultComplete || r.Coverage.UniqueRegions != 0 || r.Workers[0].Metrics["SUCCESS_PCT"] != nil || r.Workers[0].Metrics["P99_MS"] != nil {
		t.Fatal(r)
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

func TestNativeOptionsQualifyCoverageWithoutRejectingRun(t *testing.T) {
	for _, options := range []map[string]any{
		{"executionSegment": "1/2:1", "scenarios": map[string]any{"fixed_pool": map[string]any{}}},
		{"scenarios": map[string]any{"one": map[string]any{}, "two": map[string]any{}}},
	} {
		run, spec, n, execution := fixture(t)
		n.EffectiveOptions = options
		result := Derive(run, spec, n, execution)
		if !result.ResultComplete || result.ExecutionState != worker.Completed || result.Coverage != nil {
			t.Fatalf("native options should qualify coverage only: %+v", result)
		}
	}
}
