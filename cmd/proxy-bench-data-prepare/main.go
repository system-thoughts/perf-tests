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
)

type report struct {
	SchemaVersion    int               `json:"schema_version"`
	Manifest         *contract.FileRef `json:"manifest"`
	GeneratedObjects uint64            `json:"generated_objects"`
	GeneratedBytes   uint64            `json:"generated_bytes"`
	ElapsedMS        int64             `json:"elapsed_ms"`
	Errors           []command.Issue   `json:"errors"`
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
		issues, code := command.Issues(ctx, "dataset", err)
		r.Errors = issues
		r.ElapsedMS = time.Since(started).Milliseconds()
		return command.Output(stdout, stderr, r, code)
	}
	flags := flag.NewFlagSet("proxy-bench-data-prepare", flag.ContinueOnError)
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
	r.GeneratedObjects, r.GeneratedBytes = result.GeneratedObjects, result.GeneratedBytes
	if err == nil {
		r.Manifest = &contract.FileRef{Path: result.ManifestPath, SHA256: result.ManifestSHA256}
	}
	return finish(err)
}
