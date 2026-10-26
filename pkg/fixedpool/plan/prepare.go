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

package plan

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
)

type Spec struct {
	Directory    string
	RangeBytes   uint64
	Workers      uint64
	MemoryBudget uint64
	FileBudget   uint64
}

type Estimates struct {
	MemoryBytes uint64 `json:"memory_bytes"`
	FileBytes   uint64 `json:"file_bytes"`
}

type Result struct {
	Path            string    `json:"path"`
	SHA256          string    `json:"sha256"`
	RegionCount     uint64    `json:"region_count"`
	Estimates       Estimates `json:"estimates"`
	ActualFileBytes uint64    `json:"actual_file_bytes"`
	Reused          bool      `json:"reused"`
}

func Estimate(manifestBytes, total, workers uint64) (Estimates, error) {
	if total == 0 || total > contract.MaxRegions || workers == 0 || workers > total {
		return Estimates{}, fmt.Errorf("invalid resource geometry")
	}
	baseMemory := 4*total + (total+7)/8 + 512*workers + (64 << 20)
	baseFiles := 11*total + 512*workers + (1 << 20)
	if manifestBytes > (math.MaxUint64-max(baseMemory, baseFiles))/2 {
		return Estimates{}, fmt.Errorf("resource estimate overflows uint64")
	}
	return Estimates{MemoryBytes: baseMemory + 2*manifestBytes, FileBytes: baseFiles + 2*manifestBytes}, nil
}

func Prepare(ctx context.Context, manifest contract.Manifest, manifestData []byte, spec Spec) (result Result, err error) {
	if err := contract.ValidateManifest(manifest); err != nil {
		return result, contract.Tag(contract.ContractMismatch, err)
	}
	var decoded contract.Manifest
	if err := contract.Decode(manifestData, &decoded); err != nil {
		return result, contract.Tag(contract.ContractMismatch, err)
	}
	if !reflect.DeepEqual(manifest, decoded) {
		return result, contract.Tag(contract.ContractMismatch, fmt.Errorf("manifest value does not match supplied bytes"))
	}
	perObject, total, err := Geometry(manifest.ObjectSize, manifest.ObjectCount, spec.RangeBytes, spec.Workers)
	if err != nil {
		return result, contract.Tag(contract.InputInvalid, err)
	}
	result.RegionCount = total
	result.Estimates, err = Estimate(uint64(len(manifestData)), total, spec.Workers)
	if err != nil {
		return result, err
	}
	if spec.MemoryBudget != 0 && result.Estimates.MemoryBytes > spec.MemoryBudget {
		return result, contract.Tag(contract.BudgetExceeded, fmt.Errorf("estimated memory %d exceeds memory-budget %d", result.Estimates.MemoryBytes, spec.MemoryBudget))
	}
	if spec.FileBudget != 0 && result.Estimates.FileBytes > spec.FileBudget {
		return result, contract.Tag(contract.BudgetExceeded, fmt.Errorf("estimated files %d exceed file-budget %d", result.Estimates.FileBytes, spec.FileBudget))
	}
	if spec.Directory == "" {
		return result, contract.Tag(contract.InputInvalid, fmt.Errorf("plan-dir is required"))
	}
	directory, err := filepath.Abs(spec.Directory)
	if err != nil {
		return result, err
	}
	lock, err := contract.AcquireLock(directory, ".proxy-bench-plan.lock")
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	manifestSHA := contract.Digest(manifestData)
	// The directory is keyed by inputs for direct reuse without scanning history.
	// plan_id remains the SHA-256 of request-plan.json, not this directory key.
	key, err := contract.Encode(struct {
		Manifest  string `json:"manifest_sha256"`
		Range     uint64 `json:"range_bytes"`
		Workers   uint64 `json:"worker_count"`
		Algorithm string `json:"algorithm"`
		Seed      string `json:"seed"`
	}{manifestSHA, spec.RangeBytes, spec.Workers, contract.OrderAlgorithm, contract.OrderSeed})
	if err != nil {
		return result, err
	}
	final := filepath.Join(directory, contract.Digest(key))
	temporary := filepath.Join(directory, ".incomplete-"+contract.Digest(key))
	if _, err := os.Lstat(temporary); !os.IsNotExist(err) {
		return result, contract.Tag(contract.DataConflict, fmt.Errorf("unfinished plan at %s; inspect and clean before retrying", temporary))
	}
	if info, err := os.Lstat(final); err == nil {
		if !info.IsDir() {
			return result, contract.Tag(contract.DataConflict, fmt.Errorf("existing plan directory conflict at %s", final))
		}
		loaded, raw, actual, err := Load(ctx, filepath.Join(final, "request-plan.json"), manifest, manifestSHA)
		if err != nil {
			return result, contract.Tag(contract.ContractMismatch, fmt.Errorf("existing plan conflict: %w", err))
		}
		if loaded.RangeBytes != spec.RangeBytes || loaded.WorkerCount != spec.Workers {
			return result, contract.Tag(contract.ContractMismatch, fmt.Errorf("existing plan geometry conflict"))
		}
		if spec.FileBudget != 0 && actual > spec.FileBudget {
			return result, contract.Tag(contract.BudgetExceeded, fmt.Errorf("actual plan files exceed file-budget"))
		}
		result.Path, result.SHA256, result.ActualFileBytes, result.Reused = filepath.Join(final, "request-plan.json"), contract.Digest(raw), actual, true
		return result, nil
	} else if !os.IsNotExist(err) {
		return result, err
	}
	if err := contract.RequireSpace(directory, result.Estimates.FileBytes); err != nil {
		return result, err
	}
	if err := os.Mkdir(temporary, 0700); err != nil {
		return result, err
	}
	if err := os.Mkdir(filepath.Join(temporary, "shards"), 0755); err != nil {
		return result, err
	}
	defer func() {
		if err != nil {
			if _, statErr := os.Lstat(temporary); statErr == nil {
				err = fmt.Errorf("%w; unfinished plan retained at %s", err, temporary)
			} else {
				err = fmt.Errorf("%w; inspect publication at %s", err, final)
			}
		}
	}()
	p := contract.Plan{SchemaVersion: 1, ManifestSHA256: manifestSHA, RangeBytes: spec.RangeBytes, RegionsPerObject: perObject, RegionCount: total, Algorithm: contract.OrderAlgorithm, Seed: contract.OrderSeed, WorkerCount: spec.Workers, Shards: make([]contract.Shard, int(spec.Workers))}
	urls := make([]string, len(manifest.Objects))
	for i, object := range manifest.Objects {
		urls[i] = object.URL
	}
	data, err := contract.Encode(urls)
	if err != nil {
		return result, err
	}
	if err := contract.WriteFile(filepath.Join(temporary, "object-urls.json"), data); err != nil {
		return result, err
	}
	p.ObjectsFile = contract.FileRef{Path: "object-urls.json", SHA256: contract.Digest(data)}
	values, err := permutation(ctx, total)
	if err != nil {
		return result, err
	}
	for worker := uint64(0); worker < spec.Workers; worker++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		path := "shards/" + strconv.FormatUint(worker, 10) + ".json"
		digest, err := writeShard(ctx, filepath.Join(temporary, filepath.FromSlash(path)), values, worker, spec.Workers)
		if err != nil {
			return result, err
		}
		p.Shards[worker] = contract.Shard{WorkerID: worker, RegionCount: ShardCount(total, spec.Workers, worker), Path: path, SHA256: digest}
	}
	data, err = contract.Encode(p)
	if err != nil {
		return result, err
	}
	if err := contract.WriteFile(filepath.Join(temporary, "request-plan.json"), data); err != nil {
		return result, err
	}
	_, _, actual, err := load(ctx, filepath.Join(temporary, "request-plan.json"), manifest, manifestSHA, values)
	if err != nil {
		return result, err
	}
	result.ActualFileBytes = actual
	if err := publish(temporary, final, actual, spec.FileBudget); err != nil {
		return result, err
	}
	result.Path, result.SHA256, result.ActualFileBytes = filepath.Join(final, "request-plan.json"), contract.Digest(data), actual
	return result, nil
}

// publish is the actual-byte gate, independent of the conservative preflight
// estimate. A failed postflight never makes the staged plan reusable.
func publish(temporary, final string, actual, budget uint64) error {
	if budget != 0 && actual > budget {
		return contract.Tag(contract.BudgetExceeded, fmt.Errorf("actual plan files %d exceed file-budget %d", actual, budget))
	}
	if err := contract.SyncDir(filepath.Join(temporary, "shards")); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0755); err != nil {
		return err
	}
	if err := contract.SyncDir(temporary); err != nil {
		return err
	}
	if _, err := os.Lstat(final); !os.IsNotExist(err) {
		return fmt.Errorf("plan publication conflict at %s", final)
	}
	if err := os.Rename(temporary, final); err != nil {
		return err
	}
	return contract.SyncDir(filepath.Dir(final))
}

func writeShard(ctx context.Context, name string, values []uint32, worker, workers uint64) (digest string, err error) {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0444)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	hash := sha256.New()
	buffer := bufio.NewWriterSize(io.MultiWriter(f, hash), 64<<10)
	if err := buffer.WriteByte('['); err != nil {
		return "", err
	}
	var encoded [10]byte
	for position := worker; position < uint64(len(values)); position += workers {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if position != worker {
			if err := buffer.WriteByte(','); err != nil {
				return "", err
			}
		}
		if _, err := buffer.Write(strconv.AppendUint(encoded[:0], uint64(values[position]), 10)); err != nil {
			return "", err
		}
	}
	if _, err := buffer.WriteString("]\n"); err != nil {
		return "", err
	}
	if err := buffer.Flush(); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
