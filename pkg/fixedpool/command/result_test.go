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

package command

import (
	"context"
	"errors"
	"testing"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
)

func TestExitClassificationAndInterruptionPriority(t *testing.T) {
	for _, c := range []string{contract.InputInvalid, contract.ContractMismatch, contract.BudgetExceeded, contract.DataConflict} {
		if code := ExitCode(context.Background(), contract.Tag(c, errors.New("reason"))); code != 2 {
			t.Fatal(c, code)
		}
	}
	for _, err := range []error{errors.New("I/O failure"), contract.Tag("TARGET_UNSUPPORTED", errors.New("unsupported"))} {
		if code := ExitCode(context.Background(), err); code != 3 {
			t.Fatal(code)
		}
	}
	if code := ExitCode(context.Background(), nil); code != 0 {
		t.Fatal(code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, err := range []error{nil, errors.New("failure"), contract.Tag(contract.InputInvalid, errors.New("invalid"))} {
		if code := ExitCode(ctx, err); code != 130 {
			t.Fatal("interrupt lost priority", code)
		}
	}
}
