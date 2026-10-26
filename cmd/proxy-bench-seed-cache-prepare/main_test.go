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
	"strings"
	"testing"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/target"
)

func TestArgumentFailuresAreStructuredAndDoNotPrepare(t *testing.T) {
	for _, args := range [][]string{nil, {"--unknown"}, {"unexpected"}, {"--manifest", "missing-file"}} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), args, &stdout, &stderr)
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
	if code := run(context.Background(), []string{"--help"}, &stdout, &stderr); code != 0 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "target-config") {
		t.Fatalf("help: %d %s %s", code, stdout.String(), stderr.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stdout.Reset()
	stderr.Reset()
	if code := run(ctx, []string{"--unknown"}, &stdout, &stderr); code != 130 {
		t.Fatalf("interruption priority code %d", code)
	}
}
