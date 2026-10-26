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

package dataset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
)

type Spec struct {
	Root              string
	Size              uint64
	Count             uint64
	BaseURL           string
	GenerationWorkers int
}

type Result struct {
	ManifestPath     string            `json:"manifest_path"`
	ManifestSHA256   string            `json:"manifest_sha256"`
	GeneratedObjects uint64            `json:"generated_objects"`
	GeneratedBytes   uint64            `json:"generated_bytes"`
	TemporaryState   string            `json:"temporary_state"`
	Manifest         contract.Manifest `json:"-"`
}

func Prepare(ctx context.Context, spec Spec) (result Result, err error) {
	if spec.Root == "" || spec.Size == 0 || spec.Size > contract.MaxSafeInteger || spec.Count == 0 || spec.Count > contract.MaxSafeInteger || spec.GenerationWorkers < 1 {
		return result, contract.Tag(contract.InputInvalid, fmt.Errorf("root, positive JavaScript-safe size/count and positive generation-workers are required"))
	}
	root, err := filepath.Abs(spec.Root)
	if err != nil {
		return result, err
	}
	lock, err := contract.AcquireLock(root, ".proxy-bench-data.lock")
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	state := filepath.Join(root, ".fixed-pool-incomplete")
	if _, err := os.Lstat(state); !os.IsNotExist(err) {
		return result, contract.Tag(contract.DataConflict, fmt.Errorf("unfinished preparation at %s; inspect and clean unpublished artifacts before retrying", state))
	}
	manifest, err := initialManifest(root, spec)
	if err != nil {
		return result, err
	}
	if spec.Count > math.MaxUint64/spec.Size {
		return result, contract.Tag(contract.InputInvalid, fmt.Errorf("dataset byte count overflows uint64"))
	}
	if err := contract.RequireSpace(root, spec.Count*spec.Size); err != nil {
		return result, err
	}
	if err := os.Mkdir(state, 0700); err != nil {
		return result, err
	}
	result.TemporaryState = state
	defer func() {
		if err == nil || result.TemporaryState == "" {
			return
		}
		failure := struct {
			SchemaVersion int      `json:"schema_version"`
			Error         string   `json:"error"`
			Published     []string `json:"published_objects"`
		}{SchemaVersion: 1, Error: err.Error(), Published: []string{}}
		for _, object := range manifest.Objects {
			if object.SHA256 != "" {
				failure.Published = append(failure.Published, object.RelativePath)
			}
		}
		data, encodeErr := contract.Encode(failure)
		if encodeErr == nil {
			err = errors.Join(err, contract.WriteFile(filepath.Join(state, "failed.json"), data))
		}
		err = fmt.Errorf("%w; unfinished artifacts retained at %s", err, state)
	}()
	for _, directory := range []string{filepath.Join(root, "objects", "sha256"), filepath.Join(root, "manifests")} {
		if err := os.MkdirAll(directory, 0755); err != nil {
			return result, err
		}
	}
	for _, directory := range []string{filepath.Join(root, "objects"), filepath.Join(root, "manifests"), root} {
		if err := contract.SyncDir(directory); err != nil {
			return result, err
		}
	}
	manifest.Objects = append(manifest.Objects, make([]contract.Object, int(spec.Count))...)
	if err := generateObjects(ctx, root, state, spec.GenerationWorkers, &manifest); err != nil {
		for _, object := range manifest.Objects {
			if object.SHA256 != "" {
				result.GeneratedObjects++
			}
		}
		result.GeneratedBytes = result.GeneratedObjects * manifest.ObjectSize
		return result, err
	}
	result.GeneratedObjects, result.GeneratedBytes = spec.Count, spec.Count*manifest.ObjectSize
	manifest.ObjectCount = spec.Count
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := contract.ValidateManifest(manifest); err != nil {
		return result, err
	}
	data, err := contract.Encode(manifest)
	if err != nil {
		return result, err
	}
	digest := contract.Digest(data)
	staged := filepath.Join(state, "dataset-manifest.json")
	if err := contract.WriteFile(staged, data); err != nil {
		return result, err
	}
	directory := filepath.Join(root, "manifests", digest)
	if err := os.Mkdir(directory, 0755); err != nil {
		if os.IsExist(err) {
			return result, contract.Tag(contract.DataConflict, fmt.Errorf("manifest publication conflict: %w", err))
		}
		return result, err
	}
	name := filepath.Join(directory, "dataset-manifest.json")
	if err := os.Link(staged, name); err != nil {
		return result, err
	}
	if err := contract.SyncDir(directory); err != nil {
		return result, err
	}
	if err := contract.SyncDir(filepath.Dir(directory)); err != nil {
		return result, err
	}
	result.ManifestPath, result.ManifestSHA256, result.Manifest = name, digest, manifest
	if err := os.RemoveAll(state); err != nil {
		return result, err
	}
	result.TemporaryState = ""
	return result, contract.SyncDir(root)
}

func initialManifest(root string, spec Spec) (contract.Manifest, error) {
	base, err := contract.NormalizeBaseURL(spec.BaseURL)
	if err != nil {
		return contract.Manifest{}, contract.Tag(contract.InputInvalid, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return contract.Manifest{}, err
	}
	for _, entry := range entries {
		if entry.Name() != ".proxy-bench-data.lock" {
			return contract.Manifest{}, contract.Tag(contract.DataConflict, fmt.Errorf("new dataset requires an empty business directory; found %s", entry.Name()))
		}
	}
	return contract.Manifest{SchemaVersion: 1, Generator: contract.Generator{Version: contract.ContentAlgorithm, Seed: contract.ContentSeed, BlockBytes: contract.BlockBytes}, ObjectSize: spec.Size, BaseURL: base, Objects: []contract.Object{}}, nil
}

func generateObjects(ctx context.Context, root, state string, workers int, manifest *contract.Manifest) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan uint64)
	var group sync.WaitGroup
	var once sync.Once
	var firstErr error
	for range min(workers, len(manifest.Objects)) {
		group.Go(func() {
			for id := range jobs {
				object, err := writeObject(ctx, root, state, id, manifest.ObjectSize, manifest.BaseURL)
				if err != nil {
					once.Do(func() { firstErr = err; cancel() })
					return
				}
				manifest.Objects[id] = object
			}
		})
	}
dispatch:
	for id := uint64(0); id < uint64(len(manifest.Objects)); id++ {
		select {
		case jobs <- id:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	group.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

func writeObject(ctx context.Context, root, state string, id, size uint64, base string) (object contract.Object, err error) {
	name := filepath.Join(state, fmt.Sprintf("object-%016x.part", id))
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return object, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	digest := sha256.New()
	if err := Generate(ctx, io.MultiWriter(f, digest), id, size); err != nil {
		return object, err
	}
	if err := f.Chmod(0444); err != nil {
		return object, err
	}
	if err := f.Sync(); err != nil {
		return object, err
	}
	sha := hex.EncodeToString(digest.Sum(nil))
	relative := "objects/sha256/" + sha
	destination := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.Link(name, destination); err != nil {
		if os.IsExist(err) {
			return object, contract.Tag(contract.DataConflict, fmt.Errorf("object publication conflict at %s: %w", destination, err))
		}
		return object, err
	}
	if err := contract.SyncDir(filepath.Dir(destination)); err != nil {
		return object, err
	}
	if err := os.Remove(name); err != nil {
		return object, err
	}
	return contract.Object{ID: id, Size: size, SHA256: sha, RelativePath: relative, URL: base + "/" + relative}, nil
}
