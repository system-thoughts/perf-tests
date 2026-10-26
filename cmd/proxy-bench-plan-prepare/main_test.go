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
	"testing"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/dataset"
)

func invoke(t *testing.T, args []string) (int, report) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr)
	var result report
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
	code, bad := invoke(t, []string{"--manifest", input, "--range", "3", "--plan-dir", plans})
	if code != 2 || bad.Plan != nil || bad.RegionCount != nil || bad.Estimates != nil || bad.ManifestSHA256 == nil {
		t.Fatalf("failure: %+v code=%d", bad, code)
	}
	args := []string{"--manifest", input, "--range", "16", "--workers", "3", "--plan-dir", plans}
	code, good := invoke(t, args)
	if code != 0 || good.Plan == nil || good.Reused == nil || *good.Reused || good.RegionCount == nil || *good.RegionCount != 8 {
		t.Fatalf("plan without body: %+v code=%d", good, code)
	}
	code, reused := invoke(t, args)
	if code != 0 || reused.Plan == nil || !*reused.Reused || reused.Plan.SHA256 != good.Plan.SHA256 {
		t.Fatalf("reuse: %+v code=%d", reused, code)
	}
	code, budget := invoke(t, append(args, "--file-budget", "1"))
	if code != 2 || budget.Plan != nil || budget.Errors[0].Code != contract.BudgetExceeded || budget.Estimates == nil || budget.ActualFileBytes != nil {
		t.Fatalf("budget report: %+v code=%d", budget, code)
	}
}
func TestPlanRejectsDatasetArguments(t *testing.T) {
	for _, args := range [][]string{{"--size", "64"}, {"--count", "2"}, {"--root", "unused"}, {"--base-url", "http://source.example"}, {"--generation-workers", "2"}, {"--rate", "100"}, {"extra"}} {
		code, r := invoke(t, args)
		if code != 2 || r.Plan != nil || r.ManifestSHA256 != nil || len(r.Errors) == 0 {
			t.Fatalf("arguments %v: %+v code=%d", args, r, code)
		}
	}
}
