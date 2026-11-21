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

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
)

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestMissingScriptRejected(t *testing.T) {
	var out strings.Builder
	if code := run(context.Background(), nil, &out, io.Discard); code != 2 || !strings.Contains(out.String(), "INPUT_INVALID") {
		t.Fatalf("%d %s", code, &out)
	}
}

func TestInputErrorClassification(t *testing.T) {
	err := contract.Tag(contract.ContractMismatch, errors.New("input mismatch"))
	if got := inputError(context.Background(), brokenWriter{}, err); got != 2 {
		t.Fatal(got)
	}
	var out strings.Builder
	if got := inputError(context.Background(), &out, errors.New("publication failed")); got != 3 || !strings.Contains(out.String(), "WORKER_FAILED") || strings.Contains(out.String(), "PREPARE_FAILED") {
		t.Fatalf("code=%d output=%s", got, out.String())
	}
}
