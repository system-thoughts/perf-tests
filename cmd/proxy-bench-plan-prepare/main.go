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
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/plan"
)

type report struct {
	SchemaVersion   int               `json:"schema_version"`
	ManifestSHA256  *string           `json:"manifest_sha256"`
	Plan            *contract.FileRef `json:"plan"`
	RegionCount     *uint64           `json:"region_count"`
	WorkerCount     *uint64           `json:"worker_count"`
	Reused          *bool             `json:"reused"`
	Estimates       *plan.Estimates   `json:"estimates"`
	ActualFileBytes *uint64           `json:"actual_file_bytes"`
	ElapsedMS       int64             `json:"elapsed_ms"`
	Errors          []command.Issue   `json:"errors"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	started := time.Now()
	r := report{SchemaVersion: 1}
	finish := func(err error) int {
		issues, code := command.Issues(ctx, "plan", err)
		r.Errors = issues
		r.ElapsedMS = time.Since(started).Milliseconds()
		return command.Output(stdout, stderr, r, code)
	}
	flags := flag.NewFlagSet("proxy-bench-plan-prepare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var name string
	var spec plan.Spec
	flags.StringVar(&name, "manifest", "", "required: path to the input dataset manifest")
	flags.StringVar(&spec.Directory, "plan-dir", "", "required: output directory holding reusable plans")
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
		return finish(contract.Tag(contract.InputInvalid, fmt.Errorf("manifest and plan-dir are required; no positional arguments")))
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
	manifest, data, err := contract.ReadManifest(name)
	if err != nil {
		return finish(err)
	}
	digest := contract.Digest(data)
	r.ManifestSHA256 = &digest
	if _, err := fmt.Fprintln(stderr, "Preparing access plan from the manifest only."); err != nil {
		return finish(err)
	}
	result, err := plan.Prepare(ctx, manifest, data, spec)
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
		r.Reused = &result.Reused
	}
	return finish(err)
}
