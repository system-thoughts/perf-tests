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
	"path/filepath"
	"testing"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
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
func TestDatasetOnlyCLI(t *testing.T) {
	root := filepath.Join(t.TempDir(), "source")
	args := []string{"--size", "64", "--count", "2", "--root", root, "--base-url", "http://source.example"}
	code, r := invoke(t, args)
	if code != 0 || r.Manifest == nil || r.GeneratedObjects != 2 || r.GeneratedBytes != 128 || len(r.Errors) != 0 {
		t.Fatalf("new dataset: %+v code=%d", r, code)
	}
	code, r = invoke(t, args)
	if code != 2 || r.Manifest != nil || r.Errors[0].Code != contract.DataConflict {
		t.Fatalf("overwrote source: %+v code=%d", r, code)
	}
}
func TestRejectedOldAndInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"--manifest", "unused"}, {"--range", "16"}, {"--workers", "1"}, {"--plan-dir", "unused"}, {"--memory-budget", "100"}, {"--file-budget", "100"}, {"--count", "1.5"}, {"--count", "-1"}, {"--size", "0x40"}, {"extra"}, {"--generation-workers", "0"}} {
		code, r := invoke(t, args)
		if code != 2 || r.Manifest != nil || len(r.Errors) == 0 || r.Errors[0].Code != contract.InputInvalid {
			t.Fatalf("arguments %v: %+v code=%d", args, r, code)
		}
	}
}
