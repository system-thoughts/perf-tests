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

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
)

// ExitCode classifies shared preparation and worker-input failures.
func ExitCode(ctx context.Context, err error) int {
	if ctx.Err() != nil {
		return 130
	}
	if err == nil {
		return 0
	}
	switch contract.ErrorCode(err) {
	case contract.InputInvalid, contract.ContractMismatch, contract.BudgetExceeded, contract.DataConflict:
		return 2
	default:
		return 3
	}
}
