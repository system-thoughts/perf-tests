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
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/command"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/worker"
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
	return command.ExitCode(ctx, err)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return inputError(ctx, stdout, contract.Tag(contract.InputInvalid, fmt.Errorf("internal: benchmark script required")))
	}
	env, err := worker.ReadEnvironment(os.Environ())
	if err != nil {
		return inputError(ctx, stdout, contract.Tag(contract.InputInvalid, err))
	}
	spec, err := worker.PrepareInput(ctx, env, stdout)
	if err != nil {
		return inputError(ctx, stdout, err)
	}
	raw, err := contract.Encode(spec)
	if err != nil {
		return inputError(ctx, stdout, err)
	}
	if ctx.Err() != nil {
		return 130
	}
	child := exec.Command("k6", append([]string{"run", "--no-color", args[0]}, args[1:]...)...)
	for _, pair := range os.Environ() {
		key, _, _ := strings.Cut(pair, "=")
		switch key {
		case "HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy", "WORKER_SPEC", "K6_NO_USAGE_REPORT":
		default:
			child.Env = append(child.Env, pair)
		}
	}
	child.Env = append(child.Env, "HTTP_PROXY="+spec.Proxy, "WORKER_SPEC="+string(raw), "K6_NO_USAGE_REPORT=true")
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, stdout, stderr
	if os.Getenv("RANGE") != "" {
		fmt.Fprintln(stderr, "fixed-pool: RANGE is ignored; the actual Range size comes from PLAN (see startup inputs).")
	}
	started := time.Now().UTC()
	code, interrupted := 0, false
	if err := child.Start(); err != nil {
		fmt.Fprintf(stderr, "k6 start: %v\n", err)
		code = 126
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			code = 127
		}
	} else {
		done := make(chan error, 1)
		go func() { done <- child.Wait() }()
		var waitErr error
		select {
		case waitErr = <-done:
		case <-ctx.Done():
			interrupted = true
			_ = child.Process.Signal(syscall.SIGTERM)
			waitErr = <-done
		}
		if waitErr != nil {
			var exited *exec.ExitError
			if errors.As(waitErr, &exited) {
				code = exited.ExitCode()
				if status, ok := exited.Sys().(syscall.WaitStatus); ok && status.Signaled() {
					code = 128 + int(status.Signal())
				}
			} else {
				code = 3
				fmt.Fprintf(stderr, "k6 wait: %v\n", waitErr)
			}
		}
	}
	interrupted = interrupted || ctx.Err() != nil
	_ = worker.Emit(stdout, "WORKER_EXIT", struct {
		Started     string `json:"process_started_at"`
		Ended       string `json:"process_ended_at"`
		Exit        int    `json:"exit_code"`
		Interrupted bool   `json:"interrupted"`
	}{started.Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), code, interrupted})
	if interrupted {
		return 130
	}
	return code
}
