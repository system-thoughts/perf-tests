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
	"time"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/command"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/dataset"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/plan"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/target"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		_, _ = fmt.Fprintln(stderr, "Usage: proxy-bench-prepare <data|plan|seed-cache> [options]; each action runs independently. Use <action> --help for options.")
		return 0
	}
	switch args[0] {
	case "data":
		return runData(ctx, args[1:], stdout, stderr)
	case "plan":
		return runPlan(ctx, args[1:], stdout, stderr)
	case "seed-cache":
		return runSeed(ctx, args[1:], stdout, stderr)
	default:
		err := contract.Tag(contract.InputInvalid, fmt.Errorf("unknown preparation action %q; use data, plan or seed-cache", args[0]))
		return finish(ctx, stderr, "prepare", err)
	}
}

func finish(ctx context.Context, stderr io.Writer, action string, err error) int {
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: %s: %v\n", action, contract.ErrorCode(err), err)
	}
	return command.ExitCode(ctx, err)
}

func runData(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	started := time.Now()
	flags := flag.NewFlagSet("proxy-bench-prepare data", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var spec dataset.Spec
	var generationWorkers uint64
	flags.StringVar(&spec.Root, "root", "", "required: source directory; one writer, existing owned outputs are not replaced")
	flags.StringVar(&spec.BaseURL, "base-url", "", "required: source HTTP base URL reachable by the Seed")
	command.DecimalFlag(flags, &spec.Size, "size", 0, "required: positive decimal object size in bytes")
	command.DecimalFlag(flags, &spec.Count, "count", 0, "required: positive decimal object count")
	command.DecimalFlag(flags, &generationWorkers, "generation-workers", 1, "optional: positive generation concurrency (default 1)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return finish(ctx, stderr, "data", contract.Tag(contract.InputInvalid, err))
	}
	if flags.NArg() != 0 || generationWorkers == 0 || generationWorkers > uint64(^uint(0)>>1) {
		return finish(ctx, stderr, "data", contract.Tag(contract.InputInvalid, fmt.Errorf("unexpected arguments or invalid generation-workers")))
	}
	spec.GenerationWorkers = int(generationWorkers)
	if _, err := fmt.Fprintln(stderr, "Preparing a new fixed dataset."); err != nil {
		return finish(ctx, stderr, "data", err)
	}
	result, err := dataset.Prepare(ctx, spec)
	if err != nil {
		err = contract.Input("DATA.root", spec.Root, err)
	}
	if _, writeErr := fmt.Fprintf(stdout, "Data generation: objects=%d bytes=%d elapsed_ms=%d\n", result.GeneratedObjects, result.GeneratedBytes, time.Since(started).Milliseconds()); err == nil {
		err = writeErr
	}
	if err == nil {
		_, err = fmt.Fprintf(stdout, "Data generated: B=%d N=%d source=%q manifest=%q sha256=%s\n", result.Manifest.ObjectSize, result.Manifest.ObjectCount, result.Manifest.BaseURL, result.ManifestPath, result.ManifestSHA256)
	}
	return finish(ctx, stderr, "data", err)
}

func runPlan(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	started := time.Now()
	flags := flag.NewFlagSet("proxy-bench-prepare plan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var name string
	var spec plan.Spec
	flags.StringVar(&name, "manifest", "", "required: path to the input dataset manifest")
	flags.StringVar(&spec.Directory, "out", "", "required: plan output directory; one writer, existing plan/shards are not replaced")
	command.DecimalFlag(flags, &spec.RangeBytes, "range", 0, "required: positive decimal Range size in bytes")
	command.DecimalFlag(flags, &spec.Workers, "workers", 1, "optional: number of load-generator shards (default 1)")
	command.DecimalFlag(flags, &spec.MemoryBudget, "memory-budget", 0, "optional: positive estimated plan-memory budget in bytes (omitted: no budget)")
	command.DecimalFlag(flags, &spec.FileBudget, "file-budget", 0, "optional: positive total plan-file budget in bytes (omitted: no budget)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return finish(ctx, stderr, "plan", contract.Tag(contract.InputInvalid, err))
	}
	if flags.NArg() != 0 || name == "" || spec.Directory == "" {
		return finish(ctx, stderr, "plan", contract.Tag(contract.InputInvalid, fmt.Errorf("manifest and out are required; no positional arguments")))
	}
	var badBudget bool
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "memory-budget" && spec.MemoryBudget == 0 || f.Name == "file-budget" && spec.FileBudget == 0 {
			badBudget = true
		}
	})
	if badBudget {
		return finish(ctx, stderr, "plan", contract.Tag(contract.InputInvalid, fmt.Errorf("explicit budgets must be positive bytes")))
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return finish(ctx, stderr, "plan", contract.Tag(contract.InputInvalid, contract.Input("MANIFEST", name, err)))
	}
	digest := contract.Digest(data)
	if _, err := fmt.Fprintln(stderr, "Preparing access plan from the manifest only."); err != nil {
		return finish(ctx, stderr, "plan", err)
	}
	result, err := plan.Prepare(ctx, data, spec)
	if err != nil {
		err = fmt.Errorf("MANIFEST %q OUT %q: %w", name, spec.Directory, err)
	}
	if result.Estimates.MemoryBytes != 0 {
		actual := "n/a"
		if result.ActualFileBytes != 0 {
			actual = fmt.Sprint(result.ActualFileBytes)
		}
		if _, writeErr := fmt.Fprintf(stdout, "Plan resources: estimated_memory_bytes=%d estimated_file_bytes=%d actual_file_bytes=%s elapsed_ms=%d\n", result.Estimates.MemoryBytes, result.Estimates.FileBytes, actual, time.Since(started).Milliseconds()); err == nil {
			err = writeErr
		}
	}
	if err == nil {
		_, err = fmt.Fprintf(stdout, "Plan generated: MANIFEST=%q sha256=%s B=%d N=%d R=%d W=%d regions=%d PLAN=%q sha256=%s\n", name, digest, result.ObjectSize, result.ObjectCount, spec.RangeBytes, spec.Workers, result.RegionCount, result.Path, result.SHA256)
	}
	return finish(ctx, stderr, "plan", err)
}

func runSeed(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("proxy-bench-prepare seed-cache", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var spec target.Spec
	flags.StringVar(&spec.Manifest, "manifest", "", "required: immutable dataset manifest")
	flags.StringVar(&spec.TargetConfig, "target-config", "", "required: target YAML with proxy and expected server IP")
	flags.StringVar(&spec.Out, "out", "", "required: output directory; one writer, existing records are not replaced")
	command.DecimalFlag(flags, &spec.Options.Concurrency, "concurrency", 1, "optional: concurrent object preparations (default 1)")
	command.DecimalFlag(flags, &spec.Options.RequestTimeoutMS, "request-timeout-ms", 1800000, "optional: full GET timeout in milliseconds (default 1800000)")
	command.DecimalFlag(flags, &spec.Options.ReadyTimeoutMS, "ready-timeout-ms", 60000, "optional: total Range check budget in milliseconds (default 60000)")
	err := flags.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err == nil && flags.NArg() != 0 {
		err = contract.Tag(contract.InputInvalid, fmt.Errorf("unexpected positional arguments"))
	}
	if err != nil {
		err = contract.Tag(contract.InputInvalid, err)
	}
	var result target.Result
	if err == nil {
		result, err = target.Prepare(ctx, spec)
	}
	code := command.ExitCode(ctx, err)
	if err != nil {
		finish(ctx, stderr, "seed-cache", fmt.Errorf("MANIFEST %q TARGET_CONFIG %q OUT %q: %w", spec.Manifest, spec.TargetConfig, spec.Out, err))
		_, _ = fmt.Fprintf(stderr, "After correcting the reported problem, rerun proxy-bench-prepare seed-cache with the same manifest/target; reuse a valid success record downstream, or explicitly clean conflicting output items or select another --out. This command does not clear the Seed cache.\n")
		return code
	}
	_, err = fmt.Fprintf(stdout, "Seed prepared: MANIFEST=%q sha256=%s objects=%d target=%s expected_server_ip=%s record=%q\n", spec.Manifest, result.ManifestSHA256, result.ObjectCount, result.Target.Proxy, result.Target.ExpectedServerIP, spec.Out+"/target-preparation.json")
	return finish(ctx, stderr, "seed-cache", err)
}
