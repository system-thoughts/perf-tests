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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/worker"
	"github.com/dragonflyoss/perf-tests/pkg/proxybench"
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
	_ = worker.Emit(stdout, "ERROR", []worker.Issue{{Code: code, Stage: "input", Message: err.Error()}})
	if ctx.Err() != nil {
		return 130
	}
	switch code {
	case contract.InputInvalid, contract.ContractMismatch, contract.BudgetExceeded, contract.DataConflict:
		return 2
	default:
		return 3
	}
}
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "run" || args[0] == "original") {
		return execute(ctx, args, stdout, stderr)
	}
	if len(args) == 0 || args[0] != "prepare-input" {
		return inputError(ctx, stdout, contract.Tag(contract.InputInvalid, fmt.Errorf("internal subcommand required: prepare-input")))
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	scripts := flags.String("script-dir", "/home/k6/fixed-pool", "internal: directory containing the executed workload")
	output := flags.String("output", "", "internal: temporary worker input file")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return inputError(ctx, stdout, contract.Tag(contract.InputInvalid, err))
	}
	if flags.NArg() != 0 || *output == "" {
		return inputError(ctx, stdout, contract.Tag(contract.InputInvalid, fmt.Errorf("output file required; positional arguments are unsupported")))
	}
	env, err := worker.ReadEnvironment(os.Environ())
	if err != nil {
		return inputError(ctx, stdout, contract.Tag(contract.InputInvalid, err))
	}
	if err = worker.PrepareInput(ctx, env, *scripts, *output, stdout); err != nil {
		return inputError(ctx, stdout, err)
	}
	return 0
}

func execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	var spec *string
	if args[0] == "run" {
		spec = flags.String("spec", "", "internal: validated worker input")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return inputError(ctx, stdout, contract.Tag(contract.InputInvalid, err))
	}
	if flags.NArg() != 0 || (spec != nil && *spec == "") {
		return inputError(ctx, stdout, contract.Tag(contract.InputInvalid, fmt.Errorf("unexpected arguments or missing --spec")))
	}
	var err error
	thresholdFailed := false
	if spec != nil {
		err = worker.Run(ctx, *spec, stdout)
	} else {
		options, next, inputErr := proxybench.Original(os.Environ())
		if inputErr != nil {
			return inputError(ctx, stdout, contract.Tag(contract.InputInvalid, inputErr))
		}
		report, runErr := proxybench.Execute(ctx, options, next)
		err = errors.Join(runErr, report.WriteText(stdout))
		if _, writeErr := fmt.Fprint(stdout, "PROXY_BENCH_VEGETA_SUMMARY_V1 "); writeErr != nil {
			err = errors.Join(err, writeErr)
		}
		err = errors.Join(err, json.NewEncoder(stdout).Encode(report))
		thresholdFailed = report.Metrics.Requests == 0 || float64(report.Metrics.Requests-report.Succeeded)/float64(report.Metrics.Requests) >= 0.01
		result := "PASSED"
		if thresholdFailed || report.SuccessRate == nil {
			result = "FAILED"
			thresholdFailed = true
		}
		_, writeErr := fmt.Fprintf(stdout, "Result: %s (failure rate < 1%%)\n", result)
		err = errors.Join(err, writeErr)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
	}
	if ctx.Err() != nil {
		return 130
	}
	if err != nil {
		return 3
	}
	if thresholdFailed {
		return 99
	}
	return 0
}
