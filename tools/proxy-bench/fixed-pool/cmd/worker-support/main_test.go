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
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/contract"
)

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestInternalInterfaceRejectsUnknownArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"prepare-input", "--rate", "5"}, {"prepare-input", "extra"}, {"write-report", "--unknown"}} {
		var out, diagnostic strings.Builder
		if got := run(context.Background(), args, &out, &diagnostic); got != 2 || !strings.Contains(out.String(), "INPUT_INVALID") {
			t.Fatalf("%v code=%d out=%s", args, got, out.String())
		}
	}
	for _, name := range []string{"prepare-input", "run", "write-report"} {
		if got := run(context.Background(), []string{name, "--help"}, io.Discard, io.Discard); got != 0 {
			t.Fatal(got)
		}
	}
}
func TestInputAndPublicationErrorPriorities(t *testing.T) {
	err := contract.Tag(contract.ContractMismatch, errors.New("input mismatch"))
	if got := inputError(context.Background(), brokenWriter{}, err); got != 2 {
		t.Fatal(got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := inputError(ctx, brokenWriter{}, err); got != 130 {
		t.Fatal(got)
	}
	var out strings.Builder
	if got := inputError(context.Background(), &out, errors.New("publication failed")); got != 3 || !strings.Contains(out.String(), "WORKER_FAILED") || strings.Contains(out.String(), "PREPARE_FAILED") {
		t.Fatalf("code=%d output=%s", got, out.String())
	}
}
