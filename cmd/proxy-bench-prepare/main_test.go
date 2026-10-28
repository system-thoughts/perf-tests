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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/dataset"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/target"
)

func invokeData(t *testing.T, args []string) (int, dataReport) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), append([]string{"data"}, args...), &stdout, &stderr)
	var result dataReport
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not one report: %q; stderr=%s", stdout.String(), stderr.String())
	}
	return code, result
}
func TestDatasetOnlyCLI(t *testing.T) {
	root := filepath.Join(t.TempDir(), "source")
	args := []string{"--size", "64", "--count", "2", "--root", root, "--base-url", "http://source.example"}
	code, r := invokeData(t, args)
	if code != 0 || r.Manifest == nil || r.GeneratedObjects != 2 || r.GeneratedBytes != 128 || len(r.Errors) != 0 {
		t.Fatalf("new dataset: %+v code=%d", r, code)
	}
	code, r = invokeData(t, args)
	if code != 2 || r.Manifest != nil || r.Errors[0].Code != contract.DataConflict {
		t.Fatalf("overwrote source: %+v code=%d", r, code)
	}
}
func TestRejectedOldAndInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"--manifest", "unused"}, {"--range", "16"}, {"--workers", "1"}, {"--plan-dir", "unused"}, {"--memory-budget", "100"}, {"--file-budget", "100"}, {"--count", "1.5"}, {"--count", "-1"}, {"--size", "0x40"}, {"extra"}, {"--generation-workers", "0"}} {
		code, r := invokeData(t, args)
		if code != 2 || r.Manifest != nil || len(r.Errors) == 0 || r.Errors[0].Code != contract.InputInvalid {
			t.Fatalf("arguments %v: %+v code=%d", args, r, code)
		}
	}
}

func invokePlan(t *testing.T, args []string) (int, planReport) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), append([]string{"plan"}, args...), &stdout, &stderr)
	var result planReport
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not one report: %q; stderr=%s", stdout.String(), stderr.String())
	}
	return code, result
}
func TestIndependentPlanAndRetry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "source")
	pool, err := dataset.Prepare(context.Background(), dataset.Spec{Root: root, Size: 64, Count: 2, BaseURL: "http://source.example", GenerationWorkers: 1})
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "dataset-manifest.json")
	data, err := os.ReadFile(pool.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	plans := filepath.Join(t.TempDir(), "plans")
	code, bad := invokePlan(t, []string{"--manifest", input, "--range", "3", "--out", plans})
	if code != 2 || bad.Plan != nil || bad.RegionCount != nil || bad.Estimates != nil || bad.ManifestSHA256 == nil {
		t.Fatalf("failure: %+v code=%d", bad, code)
	}
	args := []string{"--manifest", input, "--range", "16", "--workers", "3", "--out", plans}
	code, good := invokePlan(t, args)
	if code != 0 || good.Plan == nil || good.RegionCount == nil || *good.RegionCount != 8 {
		t.Fatalf("plan without body: %+v code=%d", good, code)
	}
	code, conflict := invokePlan(t, args)
	if code != 2 || conflict.Plan != nil || conflict.Errors[0].Code != contract.DataConflict {
		t.Fatalf("existing output accepted: %+v code=%d", conflict, code)
	}
	code, budget := invokePlan(t, append(args, "--file-budget", "1"))
	if code != 2 || budget.Plan != nil || budget.Errors[0].Code != contract.BudgetExceeded || budget.Estimates == nil || budget.ActualFileBytes != nil {
		t.Fatalf("budget report: %+v code=%d", budget, code)
	}
}
func TestPlanRejectsDatasetArguments(t *testing.T) {
	for _, args := range [][]string{{"--size", "64"}, {"--count", "2"}, {"--root", "unused"}, {"--base-url", "http://source.example"}, {"--generation-workers", "2"}, {"--rate", "100"}, {"extra"}} {
		code, r := invokePlan(t, args)
		if code != 2 || r.Plan != nil || r.ManifestSHA256 != nil || len(r.Errors) == 0 {
			t.Fatalf("arguments %v: %+v code=%d", args, r, code)
		}
	}
}

func TestArgumentFailuresAreStructuredAndDoNotPrepare(t *testing.T) {
	for _, args := range [][]string{nil, {"--unknown"}, {"unexpected"}, {"--manifest", "missing-file"}} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), append([]string{"seed-cache"}, args...), &stdout, &stderr)
		var result target.Result
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if code != 2 || result.SchemaVersion != 1 || result.Status != target.StatusUnverified || len(result.Errors) == 0 || result.EvidenceFile != nil {
			t.Fatalf("args=%v code=%d result=%+v", args, code, result)
		}
	}
}

func TestHelpAndCancellationExitPriority(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"seed-cache", "--help"}, &stdout, &stderr); code != 0 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "target-config") {
		t.Fatalf("help: %d %s %s", code, stdout.String(), stderr.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stdout.Reset()
	stderr.Reset()
	if code := run(ctx, []string{"seed-cache", "--unknown"}, &stdout, &stderr); code != 130 {
		t.Fatalf("interruption priority code %d", code)
	}
}

func TestRootHelpAndUnknownAction(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"help"}} {
		var out, log bytes.Buffer
		if code := run(context.Background(), args, &out, &log); code != 0 || out.Len() != 0 || !strings.Contains(log.String(), "seed-cache") {
			t.Fatalf("help: %d %s %s", code, &out, &log)
		}
	}
	var out, log bytes.Buffer
	if code := run(context.Background(), []string{"unknown"}, &out, &log); code != 2 || !strings.Contains(log.String(), "unknown preparation action") {
		t.Fatalf("unknown: %d %s", code, &log)
	}
}
