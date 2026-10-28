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
		issues, code := command.Issues(ctx, "prepare", err)
		diagnostic(stderr, "prepare", err)
		return command.Output(stdout, stderr, struct {
			Errors []command.Issue `json:"errors"`
		}{issues}, code)
	}
}

func diagnostic(w io.Writer, action string, err error) {
	if err != nil {
		_, _ = fmt.Fprintf(w, "%s: %v\n", action, err)
	}
}

type dataReport struct {
	SchemaVersion    int               `json:"schema_version"`
	Manifest         *contract.FileRef `json:"manifest"`
	GeneratedObjects uint64            `json:"generated_objects"`
	GeneratedBytes   uint64            `json:"generated_bytes"`
	ElapsedMS        int64             `json:"elapsed_ms"`
	Errors           []command.Issue   `json:"errors"`
}

func runData(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	started := time.Now()
	r := dataReport{SchemaVersion: 1}
	finish := func(err error) int {
		issues, code := command.Issues(ctx, "dataset", err)
		r.Errors = issues
		r.ElapsedMS = time.Since(started).Milliseconds()
		diagnostic(stderr, "data", err)
		return command.Output(stdout, stderr, r, code)
	}
	flags := flag.NewFlagSet("proxy-bench-prepare data", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var spec dataset.Spec
	var generationWorkers uint64
	flags.StringVar(&spec.Root, "root", "", "required: empty source data directory (single writer)")
	flags.StringVar(&spec.BaseURL, "base-url", "", "required: source HTTP base URL reachable by the Seed")
	command.DecimalFlag(flags, &spec.Size, "size", 0, "required: positive decimal object size in bytes")
	command.DecimalFlag(flags, &spec.Count, "count", 0, "required: positive decimal object count")
	command.DecimalFlag(flags, &generationWorkers, "generation-workers", 1, "optional: positive generation concurrency (default 1)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return finish(contract.Tag(contract.InputInvalid, err))
	}
	if flags.NArg() != 0 || generationWorkers == 0 || generationWorkers > uint64(^uint(0)>>1) {
		return finish(contract.Tag(contract.InputInvalid, fmt.Errorf("unexpected arguments or invalid generation-workers")))
	}
	spec.GenerationWorkers = int(generationWorkers)
	if _, err := fmt.Fprintln(stderr, "Preparing a new fixed dataset."); err != nil {
		return finish(err)
	}
	result, err := dataset.Prepare(ctx, spec)
	if err != nil {
		err = contract.Input("DATA.root", spec.Root, err)
	}
	r.GeneratedObjects, r.GeneratedBytes = result.GeneratedObjects, result.GeneratedBytes
	if err == nil {
		r.Manifest = &contract.FileRef{Path: result.ManifestPath, SHA256: result.ManifestSHA256}
		_, _ = fmt.Fprintf(stderr, "Data generated: B=%d N=%d source=%q manifest=%q sha256=%s\n", result.Manifest.ObjectSize, result.Manifest.ObjectCount, result.Manifest.BaseURL, result.ManifestPath, result.ManifestSHA256)
	}
	return finish(err)
}

type planReport struct {
	SchemaVersion   int               `json:"schema_version"`
	ManifestSHA256  *string           `json:"manifest_sha256"`
	Plan            *contract.FileRef `json:"plan"`
	RegionCount     *uint64           `json:"region_count"`
	WorkerCount     *uint64           `json:"worker_count"`
	Estimates       *plan.Estimates   `json:"estimates"`
	ActualFileBytes *uint64           `json:"actual_file_bytes"`
	ElapsedMS       int64             `json:"elapsed_ms"`
	Errors          []command.Issue   `json:"errors"`
}

func runPlan(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	started := time.Now()
	r := planReport{SchemaVersion: 1}
	finish := func(err error) int {
		issues, code := command.Issues(ctx, "plan", err)
		r.Errors = issues
		r.ElapsedMS = time.Since(started).Milliseconds()
		diagnostic(stderr, "plan", err)
		return command.Output(stdout, stderr, r, code)
	}
	flags := flag.NewFlagSet("proxy-bench-prepare plan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var name string
	var spec plan.Spec
	flags.StringVar(&name, "manifest", "", "required: path to the input dataset manifest")
	flags.StringVar(&spec.Directory, "out", "", "required: new or empty output directory for this plan")
	command.DecimalFlag(flags, &spec.RangeBytes, "range", 0, "required: positive decimal Range size in bytes")
	command.DecimalFlag(flags, &spec.Workers, "workers", 1, "optional: number of load-generator shards (default 1)")
	command.DecimalFlag(flags, &spec.MemoryBudget, "memory-budget", 0, "optional: positive estimated plan-memory budget in bytes (omitted: no budget)")
	command.DecimalFlag(flags, &spec.FileBudget, "file-budget", 0, "optional: positive total plan-file budget in bytes (omitted: no budget)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return finish(contract.Tag(contract.InputInvalid, err))
	}
	if flags.NArg() != 0 || name == "" || spec.Directory == "" {
		return finish(contract.Tag(contract.InputInvalid, fmt.Errorf("manifest and out are required; no positional arguments")))
	}
	var badBudget bool
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "memory-budget" && spec.MemoryBudget == 0 || f.Name == "file-budget" && spec.FileBudget == 0 {
			badBudget = true
		}
	})
	if badBudget {
		return finish(contract.Tag(contract.InputInvalid, fmt.Errorf("explicit budgets must be positive bytes")))
	}
	if spec.Workers > 0 && spec.Workers <= contract.MaxRegions {
		r.WorkerCount = &spec.Workers
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return finish(contract.Tag(contract.InputInvalid, contract.Input("MANIFEST", name, err)))
	}
	digest := contract.Digest(data)
	r.ManifestSHA256 = &digest
	if _, err := fmt.Fprintln(stderr, "Preparing access plan from the manifest only."); err != nil {
		return finish(err)
	}
	result, err := plan.Prepare(ctx, data, spec)
	if err != nil {
		err = fmt.Errorf("MANIFEST %q OUT %q: %w", name, spec.Directory, err)
	}
	if result.RegionCount != 0 {
		r.RegionCount = &result.RegionCount
	}
	if result.Estimates.MemoryBytes != 0 {
		r.Estimates = &result.Estimates
	}
	if result.ActualFileBytes != 0 {
		r.ActualFileBytes = &result.ActualFileBytes
	}
	if err == nil {
		r.Plan = &contract.FileRef{Path: result.Path, SHA256: result.SHA256}
		_, _ = fmt.Fprintf(stderr, "Plan generated: MANIFEST=%q sha256=%s B=%d N=%d R=%d W=%d regions=%d PLAN=%q sha256=%s\n", name, digest, result.ObjectSize, result.ObjectCount, spec.RangeBytes, spec.Workers, result.RegionCount, result.Path, result.SHA256)
	}
	return finish(err)
}

func runSeed(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("proxy-bench-prepare seed-cache", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var spec target.Spec
	flags.StringVar(&spec.Manifest, "manifest", "", "required: immutable dataset manifest")
	flags.StringVar(&spec.TargetConfig, "target-config", "", "required: target YAML with proxy and expected server IP")
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
		diagnostic(stderr, "seed-cache", parseErr)
		_, _ = fmt.Fprintln(stderr, "Correct the input options (proxy-bench-prepare seed-cache --help), then rerun with valid inputs and a new or empty --out.")
		return command.Output(stdout, stderr, result, code)
	}
	result, err := target.Prepare(ctx, spec)
	_, code := command.Issues(ctx, "target_prepare", err)
	if err != nil {
		diagnostic(stderr, "seed-cache", fmt.Errorf("MANIFEST %q TARGET_CONFIG %q OUT %q: %w", spec.Manifest, spec.TargetConfig, spec.Out, err))
		for _, issue := range result.Errors {
			if issue.ObjectID != nil && issue.Stage != "target_prepare" {
				_, _ = fmt.Fprintf(stderr, "object_id=%d stage=%s code=%s: %s\n", *issue.ObjectID, issue.Stage, issue.Code, issue.Message)
				break
			}
		}
		_, _ = fmt.Fprintf(stderr, "After correcting the reported problem, rerun proxy-bench-prepare seed-cache --manifest %q --target-config %q --out <new-or-empty-directory>. Failed records are not ready inputs; this command does not clear the Seed cache.\n", spec.Manifest, spec.TargetConfig)
	} else {
		_, _ = fmt.Fprintf(stderr, "Seed prepared: MANIFEST=%q sha256=%s objects=%d target=%s expected_server_ip=%s OUT=%q\n", spec.Manifest, *result.ManifestSHA256, *result.ObjectCount, result.Target.Proxy, result.Target.ExpectedServerIP, spec.Out)
	}
	return command.Output(stdout, stderr, result, code)
}
