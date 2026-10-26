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
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
)

func TestContentVectorsAndTails(t *testing.T) {
	data, err := os.ReadFile("../testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Size   uint64   `json:"size_bytes"`
		Prefix string   `json:"prefix_hex"`
		SHA256 []string `json:"sha256"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	prefix, err := hex.DecodeString(fixture.Prefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefix) != 16 {
		t.Fatalf("fixture prefix length %d !=16", len(prefix))
	}
	for id, want := range fixture.SHA256 {
		var body bytes.Buffer
		if err := Generate(context.Background(), &body, uint64(id), fixture.Size); err != nil {
			t.Fatal(err)
		}
		if body.Len() != 64 || len(body.Bytes()[:16]) != 16 || id == 0 && !bytes.Equal(body.Bytes()[:16], prefix) || contract.Digest(body.Bytes()) != want {
			t.Fatalf("object %d vector mismatch", id)
		}
	}
	var whole bytes.Buffer
	if err := Generate(context.Background(), &whole, 0, writeBufferBytes+17); err != nil {
		t.Fatal(err)
	}
	for _, size := range []uint64{1, 15, 16, 17, writeBufferBytes - 1, writeBufferBytes, writeBufferBytes + 1} {
		var body bytes.Buffer
		if err := Generate(context.Background(), &body, 0, size); err != nil {
			t.Fatal(err)
		}
		if uint64(body.Len()) != size || !bytes.Equal(body.Bytes(), whole.Bytes()[:size]) {
			t.Fatalf("incorrect partial block at size %d", size)
		}
	}
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

type cancelWriter struct {
	cancel context.CancelFunc
	bytes  int
}

func (w *cancelWriter) Write(data []byte) (int, error) {
	w.bytes += len(data)
	w.cancel()
	return len(data), nil
}

func TestGeneratorWriteFailureAndCancellation(t *testing.T) {
	if err := Generate(context.Background(), shortWriter{}, 0, 64); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &cancelWriter{cancel: cancel}
	if err := Generate(ctx, w, 0, writeBufferBytes*2); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	if w.bytes != writeBufferBytes {
		t.Fatalf("wrote %d bytes after cancellation", w.bytes)
	}
}

func newPool(t *testing.T, count uint64, workers int) (string, Result) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "source")
	result, err := Prepare(context.Background(), Spec{Root: root, Size: 64, Count: count, BaseURL: "http://SOURCE.example:80/base/", GenerationWorkers: workers})
	if err != nil {
		t.Fatal(err)
	}
	return root, result
}

func read(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestIndependentDatasetContentAndConcurrency(t *testing.T) {
	root, original := newPool(t, 2, 1)
	other, parallel := newPool(t, 2, 4)
	third, larger := newPool(t, 3, 2)
	before := read(t, original.ManifestPath)
	if !bytes.Equal(before, read(t, parallel.ManifestPath)) {
		t.Fatal("manifest changes with concurrency")
	}
	if larger.GeneratedObjects != 3 || larger.GeneratedBytes != 192 {
		t.Fatalf("new dataset report: %+v", larger)
	}
	for i, object := range original.Manifest.Objects {
		for _, pool := range []string{other, third} {
			if !bytes.Equal(read(t, filepath.Join(root, object.RelativePath)), read(t, filepath.Join(pool, object.RelativePath))) {
				t.Fatal("pool count or concurrency changes common bytes")
			}
		}
		if object != larger.Manifest.Objects[i] {
			t.Fatal("N changes common object identity")
		}
	}
	if !bytes.Equal(before, read(t, original.ManifestPath)) {
		t.Fatal("another dataset modified original")
	}
}

func TestExistingDatasetAndNamespaceRejected(t *testing.T) {
	root, old := newPool(t, 2, 1)
	before := read(t, old.ManifestPath)
	for _, count := range []uint64{1, 2, 3} {
		if _, err := Prepare(context.Background(), Spec{Root: root, Size: 64, Count: count, BaseURL: "http://source.example", GenerationWorkers: 1}); contract.ErrorCode(err) != contract.DataConflict {
			t.Fatalf("accepted reuse or resize count=%d: %v", count, err)
		}
	}
	external := t.TempDir()
	linkRoot := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(linkRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(linkRoot, "objects")); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), Spec{Root: linkRoot, Size: 64, Count: 1, BaseURL: "http://source.example", GenerationWorkers: 1}); err == nil {
		t.Fatal("accepted populated namespace symlink")
	}
	entries, err := os.ReadDir(external)
	if err != nil || len(entries) != 0 {
		t.Fatal("wrote outside source root", err)
	}
	if !bytes.Equal(before, read(t, old.ManifestPath)) {
		t.Fatal("rejected command changed old manifest")
	}
}

func TestObjectConflictDoesNotOverwrite(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	directory := filepath.Join(root, "objects/sha256")
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(directory, "66895a8785a1dee4951313348a79a5ee9def315d6728a9d303c8b6fe74a0fb13")
	if err := os.WriteFile(name, []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeObject(context.Background(), root, state, 0, 64, "http://source.example"); contract.ErrorCode(err) != contract.DataConflict {
		t.Fatalf("object conflict: %v", err)
	}
	if string(read(t, name)) != "sentinel" {
		t.Fatal("overwrote conflicting object")
	}
}

func TestInterruptedNewDatasetAndExplicitCleanup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	spec := Spec{Root: root, Size: 64, Count: 2, BaseURL: "http://source.example", GenerationWorkers: 2}
	result, err := Prepare(ctx, spec)
	if !errors.Is(err, context.Canceled) || result.ManifestPath != "" {
		t.Fatalf("interrupted publication: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".fixed-pool-incomplete/failed.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), spec); contract.ErrorCode(err) != contract.DataConflict {
		t.Fatalf("adopted incomplete dataset: %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), spec); err != nil {
		t.Fatal("new empty directory cannot retry", err)
	}
}
