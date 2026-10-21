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
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/contract"
	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/worker"
)

type NativeSummary struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	WorkerID      uint64 `json:"worker_id"`
	ManifestID    string `json:"manifest_id"`
	PlanID        string `json:"plan_id"`
	// Accepted for older summaries only; engine metadata never controls results.
	K6Version        any            `json:"k6_version,omitempty"`
	Native           map[string]any `json:"native_summary"`
	EffectiveOptions map[string]any `json:"effective_options,omitempty"`
}
type Coverage struct {
	UniqueRegions uint64  `json:"unique_regions"`
	LogicalBytes  uint64  `json:"logical_bytes"`
	Ratio         float64 `json:"ratio"`
}
type Count struct {
	Count *float64 `json:"count"`
}
type WorkerResult struct {
	WorkerID          uint64              `json:"worker_id"`
	Metrics           map[string]*float64 `json:"metrics"`
	PlanConsumed      Count               `json:"plan_consumed"`
	Iterations        Count               `json:"iterations"`
	HTTPRequests      Count               `json:"http_reqs"`
	SuccessfulSamples *float64            `json:"successful_samples"`
	FailedSamples     *float64            `json:"failed_samples"`
	Coverage          *Coverage           `json:"coverage"`
}
type Result struct {
	SchemaVersion  int                         `json:"schema_version"`
	RunID          string                      `json:"run_id"`
	References     map[string]contract.FileRef `json:"references"`
	ResultComplete bool                        `json:"result_complete"`
	ExecutionState string                      `json:"execution_state"`
	Workers        []WorkerResult              `json:"workers"`
	Aggregate      map[string]*float64         `json:"aggregate"`
	Coverage       *Coverage                   `json:"coverage"`
	Errors         []worker.Issue              `json:"errors"`
}

func addIssue(r *Result, spec worker.Spec, code, stage, message string) {
	r.Errors = append(r.Errors, worker.Issue{Code: code, Stage: stage, Message: message, WorkerID: &spec.WorkerID})
	if code == "RESULT_INCOMPLETE" {
		r.ResultComplete = false
	}
}
func mapValue(v any, keys ...string) any {
	for _, key := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[key]
	}
	return v
}
func number(v any) *float64 {
	n, ok := v.(float64)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return nil
	}
	return &n
}
func metric(n NativeSummary, name, key string) *float64 {
	return number(mapValue(n.Native, "metrics", name, "values", key))
}
func sampleCount(n NativeSummary, key string) *float64 {
	value := metric(n, "http_req_failed", key)
	if value != nil && (*value != math.Trunc(*value) || *value > float64(contract.MaxSafeInteger)) {
		return nil
	}
	return value
}
func counter(n NativeSummary, name string) *float64 {
	v := metric(n, name, "count")
	if v != nil && (*v != math.Trunc(*v) || *v > float64(contract.MaxSafeInteger)) {
		return nil
	}
	return v
}

func droppedCount(n NativeSummary, issues []worker.Issue) *float64 {
	value := counter(n, "dropped_iterations")
	metrics, _ := n.Native["metrics"].(map[string]any)
	_, present := metrics["dropped_iterations"]
	if value != nil || present {
		return value
	}
	// k6 omits the whole metric when no dropped-iteration samples were emitted.
	// Regressions check this behavior against the engine shipped by the image.
	// Missing local execution facts do not invalidate otherwise complete native
	// metrics; missing native fields must never be silently converted to zero.
	for _, issue := range issues {
		if issue.Code == "RESULT_INCOMPLETE" && (issue.Stage == "summary" || issue.Stage == "metrics") {
			return nil
		}
	}
	zero := float64(0)
	return &zero
}

// Native configuration can replace scenarios or partition their iteration IDs.
// These observations qualify coverage; they never reject engine configuration.
func continuousScenario(n *NativeSummary) bool {
	if n == nil || n.EffectiveOptions == nil {
		// Legacy summaries predate the effective-options snapshot.
		return true
	}
	if n.EffectiveOptions["executionSegment"] != nil {
		return false
	}
	scenarios, ok := n.EffectiveOptions["scenarios"].(map[string]any)
	return ok && len(scenarios) == 1
}

// Derive maps native values without recomputing rates or combining distributions.
func Derive(run worker.RunSpec, spec worker.Spec, n *NativeSummary, execution *worker.Execution) Result {
	r := Result{SchemaVersion: 1, RunID: run.RunID, References: map[string]contract.FileRef{}, ResultComplete: true, ExecutionState: worker.Unknown, Workers: []WorkerResult{}, Errors: []worker.Issue{}}
	w := WorkerResult{WorkerID: spec.WorkerID, Metrics: map[string]*float64{}}
	if execution == nil {
		addIssue(&r, spec, "RESULT_INCOMPLETE", "execution", "worker-execution.json missing or invalid")
	} else {
		r.Errors = append(r.Errors, execution.Errors...)
		if execution.SchemaVersion != 1 || execution.RunID != spec.RunID || execution.WorkerID != spec.WorkerID {
			addIssue(&r, spec, "RESULT_INCOMPLETE", "execution", "execution identity mismatch")
			execution = nil
		}
	}
	if execution != nil {
		switch {
		case execution.TerminationReason == worker.Interrupted:
			r.ExecutionState = worker.Interrupted
		case execution.TerminationReason == worker.StartFailed:
			r.ExecutionState = worker.Failed
		case execution.ExitCode == nil:
			r.ExecutionState = worker.Unknown
			addIssue(&r, spec, "RESULT_INCOMPLETE", "execution", "k6 exit code unknown")
		case *execution.ExitCode != 0:
			r.ExecutionState = worker.Failed
		case execution.TerminationReason == worker.Completed:
			r.ExecutionState = worker.Completed
		default:
			r.ExecutionState = worker.Unknown
			addIssue(&r, spec, "RESULT_INCOMPLETE", "execution", "unknown termination_reason")
		}
		if execution.ProcessStartedAt == nil || execution.ProcessEndedAt == nil {
			addIssue(&r, spec, "RESULT_INCOMPLETE", "execution", "local process timestamps unavailable")
		} else {
			start, e1 := time.Parse(time.RFC3339Nano, *execution.ProcessStartedAt)
			end, e2 := time.Parse(time.RFC3339Nano, *execution.ProcessEndedAt)
			if e1 != nil || e2 != nil || end.Before(start) {
				addIssue(&r, spec, "RESULT_INCOMPLETE", "execution", "local process timestamps invalid")
			}
		}
	}
	if n != nil && (n.SchemaVersion != 1 || n.RunID != spec.RunID || n.WorkerID != spec.WorkerID || n.ManifestID != spec.ManifestID || n.PlanID != spec.PlanID || n.Native == nil) {
		n = nil
	}
	if n == nil {
		addIssue(&r, spec, "RESULT_INCOMPLETE", "summary", "k6-summary.json missing, invalid or mismatched")
	}
	fields := []struct{ output, metric, key string }{
		{"ACTUAL_QPS", "http_reqs", "rate"}, {"REQUESTS_TOTAL", "http_reqs", "count"},
		{"P99_MS", "http_req_duration", "p(99)"}, {"P50_MS", "http_req_duration", "med"}, {"P95_MS", "http_req_duration", "p(95)"}, {"MEAN_MS", "http_req_duration", "avg"}, {"MAX_MS", "http_req_duration", "max"},
		{"DATA_RECEIVED_BYTES", "data_received", "count"},
	}
	for _, field := range fields {
		var value *float64
		if n != nil {
			if field.key == "count" {
				value = counter(*n, field.metric)
			} else {
				value = metric(*n, field.metric, field.key)
			}
		}
		w.Metrics[field.output] = value
		if n != nil && value == nil {
			addIssue(&r, spec, "RESULT_INCOMPLETE", "metrics", field.metric+".values."+field.key+" unavailable")
		}
	}
	w.Metrics["SUCCESS_PCT"] = nil
	w.Metrics["TOOL_DURATION_S"] = nil
	w.Metrics["DROPPED_ITERATIONS"] = nil
	if n != nil {
		w.PlanConsumed.Count = counter(*n, "plan_consumed")
		w.Iterations.Count = counter(*n, "iterations")
		w.HTTPRequests.Count = counter(*n, "http_reqs")
		for name, value := range map[string]*float64{"plan_consumed": w.PlanConsumed.Count, "iterations": w.Iterations.Count, "http_reqs": w.HTTPRequests.Count} {
			if value == nil {
				addIssue(&r, spec, "RESULT_INCOMPLETE", "metrics", name+".values.count unavailable")
			}
		}
		failed := metric(*n, "http_req_failed", "rate")
		w.SuccessfulSamples = sampleCount(*n, "fails")
		w.FailedSamples = sampleCount(*n, "passes")
		if failed == nil || *failed > 1 || w.SuccessfulSamples == nil || w.FailedSamples == nil {
			addIssue(&r, spec, "RESULT_INCOMPLETE", "metrics", "http_req_failed rate/fails/passes unavailable")
		} else if *w.SuccessfulSamples+*w.FailedSamples > 0 {
			v := (1 - *failed) * 100
			w.Metrics["SUCCESS_PCT"] = &v
		}
		duration := number(mapValue(n.Native, "state", "testRunDurationMs"))
		if duration == nil {
			addIssue(&r, spec, "RESULT_INCOMPLETE", "metrics", "state.testRunDurationMs unavailable")
		} else {
			seconds := *duration / 1000
			w.Metrics["TOOL_DURATION_S"] = &seconds
		}
		dropped := droppedCount(*n, r.Errors)
		w.Metrics["DROPPED_ITERATIONS"] = dropped
		if dropped == nil {
			addIssue(&r, spec, "RESULT_INCOMPLETE", "metrics", "dropped_iterations.values.count unavailable")
		}
		if w.HTTPRequests.Count != nil && *w.HTTPRequests.Count == 0 {
			w.Metrics["SUCCESS_PCT"] = nil
			for _, key := range []string{"P99_MS", "P50_MS", "P95_MS", "MEAN_MS", "MAX_MS"} {
				w.Metrics[key] = nil
			}
			addIssue(&r, spec, "NO_SAMPLES", "metrics", "no HTTP samples; latency and success percentage unknown")
		}
	}
	countsValid := w.PlanConsumed.Count != nil && w.Iterations.Count != nil && w.HTTPRequests.Count != nil && *w.PlanConsumed.Count == *w.Iterations.Count && *w.Iterations.Count == *w.HTTPRequests.Count
	if r.ExecutionState == worker.Completed && !countsValid {
		r.ExecutionState = worker.Unknown
		if w.PlanConsumed.Count != nil && w.Iterations.Count != nil && w.HTTPRequests.Count != nil {
			addIssue(&r, spec, "WORKER_FAILED", "execution", "normal exit has inconsistent consumed/iterations/http_reqs counters")
		}
	}
	if r.ExecutionState == worker.Completed && countsValid && r.ResultComplete && continuousScenario(n) {
		unique := min(uint64(*w.PlanConsumed.Count), spec.RegionCount)
		if spec.RangeBytes == 0 || unique > contract.MaxSafeInteger/spec.RangeBytes {
			addIssue(&r, spec, "COVERAGE_UNVERIFIED", "coverage", "logical byte coverage exceeds the exact integer contract")
		} else {
			w.Coverage = &Coverage{UniqueRegions: unique, LogicalBytes: unique * spec.RangeBytes, Ratio: float64(unique) / float64(spec.RegionCount)}
		}
	} else {
		addIssue(&r, spec, "COVERAGE_UNVERIFIED", "coverage", "coverage requires normal completion, consistent consumption and one unsegmented scenario")
	}
	r.Workers = append(r.Workers, w)
	if run.WorkerCount == 1 {
		r.Aggregate = w.Metrics
		r.Coverage = w.Coverage
	}
	return r
}

func Write(directory string, execution worker.Execution, stdout io.Writer) int {
	run, spec, err := worker.ReadSpec(directory)
	if err != nil {
		return fail(stdout, err, execution.Errors)
	}
	if err = worker.Publish(directory, "worker-execution.json", execution); err != nil {
		_ = worker.Emit(stdout, "WORKER_EXIT", execution)
		return fail(stdout, err, execution.Errors)
	}
	if err = worker.Emit(stdout, "WORKER_EXIT", execution); err != nil {
		return fail(stdout, err, execution.Errors)
	}
	var native NativeSummary
	var n *NativeSummary
	if worker.ReadRecord(filepath.Join(directory, "k6-summary.json"), &native) == nil {
		n = &native
	}
	result := Derive(run, spec, n, &execution)
	for _, name := range []string{"run-spec.json", "worker-spec.json", "k6-summary.json", "worker-execution.json"} {
		raw, err := os.ReadFile(filepath.Join(directory, name))
		if err == nil {
			result.References[name] = contract.FileRef{Path: name, SHA256: contract.Digest(raw)}
		}
	}
	if err = worker.Publish(directory, "worker-summary.json", result); err != nil {
		return fail(stdout, err, result.Errors)
	}
	if err = worker.Emit(stdout, "WORKER_RESULT", result); err != nil {
		return fail(stdout, err, result.Errors)
	}
	if _, err = fmt.Fprintf(stdout, "Worker %d: execution=%s result_complete=%t\n", spec.WorkerID, result.ExecutionState, result.ResultComplete); err != nil {
		return fail(stdout, err, result.Errors)
	}
	// Display exactly the mapped JSON values, including nulls.
	for _, key := range []string{"ACTUAL_QPS", "REQUESTS_TOTAL", "SUCCESS_PCT", "P99_MS", "P50_MS", "P95_MS", "MEAN_MS", "MAX_MS", "TOOL_DURATION_S", "DATA_RECEIVED_BYTES", "DROPPED_ITERATIONS"} {
		text := "null"
		if value := result.Workers[0].Metrics[key]; value != nil {
			text = fmt.Sprintf("%.12g", *value)
		}
		if _, err = fmt.Fprintf(stdout, "%s=%s\n", key, text); err != nil {
			return fail(stdout, err, result.Errors)
		}
	}
	return worker.ExitCode(result.Errors)
}
func fail(stdout io.Writer, err error, previous []worker.Issue) int {
	issues := append(append([]worker.Issue{}, previous...), worker.Issue{Code: "WORKER_FAILED", Stage: "report", Message: err.Error()})
	_ = worker.Emit(stdout, "ERROR", issues)
	return worker.ExitCode(issues)
}
