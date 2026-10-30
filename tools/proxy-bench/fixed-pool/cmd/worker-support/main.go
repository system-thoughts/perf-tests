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
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/contract"
	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/report"
	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/worker"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
func inputError(ctx context.Context, stdout io.Writer, err error) int {
	code := contract.ErrorCode(err)
	var failure *contract.Failure
	if !errors.As(err, &failure) {
		code = "WORKER_FAILED"
	}
	issues := []worker.Issue{{Code: code, Stage: "input", Message: err.Error()}}
	if ctx.Err() != nil {
		issues = append(issues, worker.Issue{Code: "INTERRUPTED", Stage: "input", Message: ctx.Err().Error()})
	}
	if err := worker.Emit(stdout, "ERROR", issues); err != nil {
		issues = append(issues, worker.Issue{Code: "WORKER_FAILED", Stage: "publish", Message: err.Error()})
	}
	return worker.ExitCode(issues)
}
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return inputError(ctx, stdout, contract.Tag(contract.InputInvalid, fmt.Errorf("internal subcommand required: prepare-input or write-report")))
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	var scriptDir, specPath, started, ended, reason string
	var code int
	switch args[0] {
	case "prepare-input":
		flags.StringVar(&scriptDir, "script-dir", "/home/k6/fixed-pool", "internal: directory containing the executed workload")
	case "run":
		flags.StringVar(&specPath, "spec", "", "internal: validated private worker spec")
	case "write-report":
		flags.StringVar(&started, "started-at", "", "internal: local child start UTC RFC3339")
		flags.StringVar(&ended, "ended-at", "", "internal: local child end UTC RFC3339")
		flags.StringVar(&reason, "reason", "", "internal: completed, failed, interrupted or start_failed")
		flags.IntVar(&code, "exit-code", -1, "internal: raw child exit code")
	default:
		return inputError(ctx, stdout, contract.Tag(contract.InputInvalid, fmt.Errorf("unsupported internal subcommand")))
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return inputError(ctx, stdout, contract.Tag(contract.InputInvalid, err))
	}
	if flags.NArg() != 0 {
		return inputError(ctx, stdout, contract.Tag(contract.InputInvalid, fmt.Errorf("unexpected positional arguments")))
	}
	if args[0] == "run" {
		if err := worker.Run(ctx, specPath, stdout); err != nil {
			return inputError(ctx, stdout, err)
		}
		return 0
	}
	if args[0] == "prepare-input" {
		env, err := worker.ReadEnvironment(os.Environ())
		if err != nil {
			return inputError(ctx, stdout, contract.Tag(contract.InputInvalid, err))
		}
		if err = worker.PrepareInput(ctx, env, scriptDir, stdout); err != nil {
			return inputError(ctx, stdout, err)
		}
		return 0
	}
	directory := os.Getenv("RESULT_DIR")
	if directory == "" {
		directory = "/results"
	}
	_, spec, err := worker.ReadSpec(directory)
	if err != nil {
		return inputError(ctx, stdout, contract.Tag(contract.ContractMismatch, err))
	}
	for _, timestamp := range []string{started, ended} {
		if _, err = time.Parse(time.RFC3339Nano, timestamp); err != nil {
			return inputError(ctx, stdout, contract.Tag(contract.InputInvalid, fmt.Errorf("invalid local execution timestamp")))
		}
	}
	if code < 0 || code > 255 {
		return inputError(ctx, stdout, contract.Tag(contract.InputInvalid, fmt.Errorf("raw exit code outside [0,255]")))
	}
	if reason != worker.Completed && reason != worker.Failed && reason != worker.Interrupted && reason != worker.StartFailed {
		return inputError(ctx, stdout, contract.Tag(contract.InputInvalid, fmt.Errorf("unknown termination reason")))
	}
	final := filepath.Join(directory, "vegeta-summary.json")
	_, statErr := os.Stat(final)
	execution := worker.Execution{SchemaVersion: 1, RunID: spec.RunID, WorkerID: spec.WorkerID, ProcessStartedAt: &started, ProcessEndedAt: &ended, ExitCode: &code, TerminationReason: reason, SummaryPresent: statErr == nil, Errors: []worker.Issue{}}
	switch {
	case reason == worker.Interrupted || code == 105 || code == 130 || ctx.Err() != nil:
		execution.TerminationReason = worker.Interrupted
		execution.Errors = append(execution.Errors, worker.Issue{Code: "INTERRUPTED", Stage: "execution", Message: "local worker interrupted", WorkerID: &spec.WorkerID})
	case reason == worker.StartFailed:
		execution.ProcessStartedAt = nil
		execution.ExitCode = nil
		execution.Errors = append(execution.Errors, worker.Issue{Code: "START_FAILED", Stage: "execution", Message: "Vegeta child could not start", WorkerID: &spec.WorkerID})
	case code != 0 || reason == worker.Failed:
		execution.Errors = append(execution.Errors, worker.Issue{Code: "WORKER_FAILED", Stage: "execution", Message: fmt.Sprintf("Vegeta exited with code %d", code), WorkerID: &spec.WorkerID})
	}
	return report.Write(directory, execution, stdout)
}
