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
		issues, code := Issues(context.Background(), "plan", contract.Tag(c, errors.New("reason")))
		if code != 2 || len(issues) != 1 || issues[0].Code != c || issues[0].Stage != "plan" {
			t.Fatal(issues, code)
		}
	}
	if _, code := Issues(context.Background(), "dataset", errors.New("I/O failure")); code != 3 {
		t.Fatal(code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	issues, code := Issues(ctx, "dataset", contract.Tag(contract.InputInvalid, errors.New("invalid")))
	if code != 130 || len(issues) != 2 {
		t.Fatal("interrupt lost priority or other error", issues, code)
	}
}
