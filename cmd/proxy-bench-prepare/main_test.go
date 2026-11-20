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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/dataset"
)

func invoke(t *testing.T, action string, args []string) (int, string, string) {
	t.Helper()
	var out, log bytes.Buffer
	code := run(context.Background(), append([]string{action}, args...), &out, &log)
	return code, out.String(), log.String()
}
func TestDatasetOnlyCLI(t *testing.T) {
	root := filepath.Join(t.TempDir(), "source")
	args := []string{"--size", "64", "--count", "2", "--root", root, "--base-url", "http://source.example"}
	code, out, log := invoke(t, "data", args)
	if code != 0 || !strings.Contains(out, "objects=2 bytes=128") {
		t.Fatalf("%d %s %s", code, out, log)
	}
	manifest, _, err := contract.ReadManifest(filepath.Join(root, "dataset-manifest.json"))
	if err != nil || manifest.ObjectCount != 2 || manifest.ObjectSize != 64 {
		t.Fatalf("%+v %v", manifest, err)
	}
	code, _, log = invoke(t, "data", args)
	if code != 2 || !strings.Contains(log, contract.DataConflict) {
		t.Fatalf("%d %s", code, log)
	}
}
func TestRejectedOldAndInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"--range", "16"}, {"--count", "1.5"}, {"--count", "-1"}, {"--size", "0x40"}, {"extra"}, {"--generation-workers", "0"}} {
		code, _, log := invoke(t, "data", args)
		if code != 2 || !strings.Contains(log, contract.InputInvalid) {
			t.Fatalf("%v code=%d %s", args, code, log)
		}
	}
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
	code, _, log := invoke(t, "plan", []string{"--manifest", input, "--range", "3", "--out", plans})
	if code != 2 || !strings.Contains(log, contract.InputInvalid) || !strings.Contains(log, input) {
		t.Fatalf("%d %s", code, log)
	}
	args := []string{"--manifest", input, "--range", "16", "--workers", "3", "--out", plans}
	code, out, log := invoke(t, "plan", args)
	if code != 0 || !strings.Contains(out, "regions=8") {
		t.Fatalf("%d %s %s", code, out, log)
	}
	raw, err := os.ReadFile(filepath.Join(plans, "request-plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var plan contract.Plan
	if err := contract.DecodeRecord(raw, &plan); err != nil || plan.RegionCount != 8 || plan.WorkerCount != 3 {
		t.Fatalf("%+v %v", plan, err)
	}
	code, _, log = invoke(t, "plan", args)
	if code != 2 || !strings.Contains(log, contract.DataConflict) {
		t.Fatalf("%d %s", code, log)
	}
	code, out, log = invoke(t, "plan", append(args, "--file-budget", "1"))
	if code != 2 || !strings.Contains(log, contract.BudgetExceeded) || !strings.Contains(out, "estimated_file_bytes=") {
		t.Fatalf("%d %s %s", code, out, log)
	}
}
func TestPlanRejectsDatasetArguments(t *testing.T) {
	for _, args := range [][]string{{"--size", "64"}, {"extra"}} {
		code, _, log := invoke(t, "plan", args)
		if code != 2 || !strings.Contains(log, contract.InputInvalid) {
			t.Fatalf("%v %d %s", args, code, log)
		}
	}
}

func TestArgumentFailuresDoNotPrepare(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "missing-manifest.json")
	for _, args := range [][]string{nil, {"--unknown"}, {"unexpected"}, {"--concurrency", "0"}, {"--manifest", manifest, "--target-config", filepath.Join(dir, "target.yaml"), "--out", filepath.Join(dir, "preparation")}} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), append([]string{"seed-cache"}, args...), &stdout, &stderr)
		if code != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("args=%v code=%d stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
		}
		if len(args) > 0 && args[0] == "--manifest" && (!strings.Contains(stderr.String(), "MANIFEST") || !strings.Contains(stderr.String(), manifest)) {
			t.Fatal(stderr.String())
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
