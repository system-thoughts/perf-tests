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
	"syscall"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/command"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/target"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("proxy-bench-seed-cache-prepare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var spec target.Spec
	flags.StringVar(&spec.Manifest, "manifest", "", "required: immutable dataset manifest")
	flags.StringVar(&spec.TargetConfig, "target-config", "", "required: target YAML and proxy rules reference")
	flags.StringVar(&spec.Out, "out", "", "required: new or empty output directory; never overwrite results")
	parseErr := flags.Parse(args)
	if errors.Is(parseErr, flag.ErrHelp) {
		return 0
	}
	if parseErr == nil && flags.NArg() != 0 {
		parseErr = fmt.Errorf("unexpected positional arguments")
	}
	if parseErr != nil {
		issues, code := command.Issues(ctx, "target_prepare", contract.Tag(contract.InputInvalid, parseErr))
		result := target.NewResult()
		for _, issue := range issues {
			result.Errors = append(result.Errors, target.Issue{Code: issue.Code, Stage: issue.Stage, Message: issue.Message})
		}
		return command.Output(stdout, stderr, result, code)
	}
	result, err := target.Prepare(ctx, spec)
	_, code := command.Issues(ctx, "target_prepare", err)
	return command.Output(stdout, stderr, result, code)
}
