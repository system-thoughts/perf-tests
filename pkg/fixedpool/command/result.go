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
	"encoding/json"
	"fmt"
	"io"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
)

type Issue struct {
	Code    string `json:"code"`
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

func Issues(ctx context.Context, stage string, err error) ([]Issue, int) {
	issues := []Issue{}
	code := 0
	if err != nil {
		category := contract.ErrorCode(err)
		code = 3
		switch category {
		case contract.InputInvalid, contract.ContractMismatch, contract.BudgetExceeded, contract.DataConflict:
			code = 2
		}
		issues = append(issues, Issue{Code: category, Stage: stage, Message: err.Error()})
	}
	if ctx.Err() != nil {
		issues = append(issues, Issue{Code: "INTERRUPTED", Stage: stage, Message: ctx.Err().Error()})
		code = 130
	}
	return issues, code
}

func Output(stdout, stderr io.Writer, result any, code int) int {
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		_, _ = fmt.Fprintf(stderr, "write result: %v\n", err)
		if code == 130 {
			return code
		}
		return 3
	}
	return code
}
