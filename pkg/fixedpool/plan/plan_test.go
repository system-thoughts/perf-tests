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
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/dataset"
)

func TestPermutationAndRateVectors(t *testing.T) {
	data, err := os.ReadFile("../testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Permutation []uint32 `json:"permutation"`
		Rates       []uint64 `json:"rates"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	got, err := Permutation(8)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, fixture.Permutation) {
		t.Fatalf("permutation=%v want %v", got, fixture.Permutation)
	}
	rates, err := AllocateRates([]uint64{3, 3, 2}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rates, fixture.Rates) {
		t.Fatalf("rates=%v", rates)
	}
	for _, test := range []struct {
		counts []uint64
		rate   uint64
		want   []uint64
	}{
		{[]uint64{2, 1}, 2, []uint64{1, 1}},
		{[]uint64{1, 1, 1}, 5, []uint64{2, 2, 1}},
		{[]uint64{1}, 1, []uint64{1}},
		{[]uint64{1 << 31, 1<<31 - 1}, contract.MaxRate, []uint64{1 << 30, 1<<30 - 1}},
	} {
		got, err := AllocateRates(test.counts, test.rate)
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Fatalf("counts=%v rate=%d: %v %v", test.counts, test.rate, got, err)
		}
	}
	for count := uint64(1); count < 200; count++ {
		got, err := Permutation(count)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[uint32]bool{}
		for _, g := range got {
			if uint64(g) >= count || seen[g] {
				t.Fatalf("invalid permutation at M=%d", count)
			}
			seen[g] = true
		}
	}
}

func TestIntegerAndResourceBoundaries(t *testing.T) {
	for _, g := range [][4]uint64{{0, 1, 1, 1}, {64, 0, 16, 1}, {64, 2, 0, 1}, {64, 2, 3, 1}, {64, 2, 16, 9}, {contract.MaxSafeInteger + 1, 1, 1, 1}, {contract.MaxSafeInteger, 1, 1, 1}, {1, contract.MaxRegions + 1, 1, 1}, {2, contract.MaxRegions, 1, 1}} {
		if _, _, err := Geometry(g[0], g[1], g[2], g[3]); err == nil {
			t.Fatalf("accepted geometry %v", g)
		}
	}
	for _, g := range [][4]uint64{{1, contract.MaxRegions, 1, 1}, {contract.MaxSafeInteger, 1, contract.MaxSafeInteger, 1}} {
		if _, _, err := Geometry(g[0], g[1], g[2], g[3]); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		counts []uint64
		rate   uint64
	}{{[]uint64{2, 1}, 1}, {[]uint64{0}, 1}, {[]uint64{contract.MaxRegions, 1}, 100}, {[]uint64{3, 1}, 2}, {[]uint64{1}, contract.MaxRate + 1}} {
		if _, err := AllocateRates(test.counts, test.rate); err == nil {
			t.Fatalf("accepted rate %v/%d", test.counts, test.rate)
		}
	}
	est, err := Estimate(1000, 8, 3)
	if err != nil {
		t.Fatal(err)
	}
	if est.MemoryBytes != 67112433 || est.FileBytes != 1052200 {
		t.Fatalf("estimate=%+v", est)
	}
	if _, err := Estimate(^uint64(0), 8, 3); err == nil {
		t.Fatal("estimate overflow accepted")
	}
	p := contract.Plan{RegionCount: 8, RegionsPerObject: 4, RangeBytes: 16}
	for _, v := range [][4]uint64{{0, 0, 0, 15}, {3, 0, 48, 63}, {4, 1, 0, 15}, {7, 1, 48, 63}} {
		object, offset, end, err := Region(p, v[0])
		if err != nil || [3]uint64{object, offset, end} != [3]uint64{v[1], v[2], v[3]} {
			t.Fatalf("region %d=%d/%d/%d %v", v[0], object, offset, end, err)
		}
	}
	if _, _, _, err := Region(p, 8); err == nil {
		t.Fatal("out-of-bounds region accepted")
	}
}

func fixture(t *testing.T) (contract.Manifest, []byte, Spec) {
	t.Helper()
	r, err := dataset.Prepare(context.Background(), dataset.Spec{Root: filepath.Join(t.TempDir(), "source"), Size: 64, Count: 2, BaseURL: "http://source.example", GenerationWorkers: 1})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(r.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	return r.Manifest, data, Spec{Directory: filepath.Join(t.TempDir(), "plans"), RangeBytes: 16, Workers: 3}
}

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPlanPublishReuseAndGeometryChanges(t *testing.T) {
	m, data, spec := fixture(t)
	result, err := Prepare(context.Background(), m, data, spec)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Dir(result.Path))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("published plan directory must permit other container UIDs to read/traverse: %v, %v", info, err)
	}
	p, raw, actual, err := Load(context.Background(), result.Path, m, contract.Digest(data))
	if err != nil {
		t.Fatal(err)
	}
	if contract.Digest(raw) != result.SHA256 || actual != result.ActualFileBytes {
		t.Fatal("published result inconsistent")
	}
	expected := [][]uint32{{2, 1, 4}, {0, 3, 6}, {5, 7}}
	for w, shard := range p.Shards {
		var got []uint32
		if err := json.Unmarshal(readFile(t, filepath.Join(filepath.Dir(result.Path), shard.Path)), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, expected[w]) {
			t.Fatalf("worker %d=%v", w, got)
		}
	}
	for _, forbidden := range []string{`"rate"`, `"run_id"`, `"target"`} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatal("run parameter in reusable plan")
		}
	}
	reused, err := Prepare(context.Background(), m, data, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Reused || reused.Path != result.Path || reused.SHA256 != result.SHA256 {
		t.Fatal("plan was not reused")
	}
	spec.Workers = 2
	changed, err := Prepare(context.Background(), m, data, spec)
	if err != nil {
		t.Fatal(err)
	}
	if changed.SHA256 == result.SHA256 {
		t.Fatal("W did not change plan")
	}
	var reconstructed [8]uint32
	for w := 0; w < 2; w++ {
		var values []uint32
		if err := json.Unmarshal(readFile(t, filepath.Join(filepath.Dir(changed.Path), "shards", string(rune('0'+w))+".json")), &values); err != nil {
			t.Fatal(err)
		}
		for i, g := range values {
			reconstructed[w+i*2] = g
		}
	}
	if reconstructed != [8]uint32{2, 0, 5, 1, 3, 7, 4, 6} {
		t.Fatal("W changed global order")
	}
	spec.RangeBytes = 32
	changedRange, err := Prepare(context.Background(), m, data, spec)
	if err != nil {
		t.Fatal(err)
	}
	if changedRange.RegionCount != 4 || changedRange.SHA256 == changed.SHA256 {
		t.Fatal("R did not rebuild region set")
	}
	t.Logf("M=8 W=3 estimated_memory=%d estimated_files=%d actual_files=%d", result.Estimates.MemoryBytes, result.Estimates.FileBytes, result.ActualFileBytes)
}

func TestPreflightAndActualBudgets(t *testing.T) {
	for _, kind := range []string{"memory", "files"} {
		t.Run(kind, func(t *testing.T) {
			m, data, spec := fixture(t)
			if kind == "memory" {
				spec.MemoryBudget = 1
			} else {
				spec.FileBudget = 1
			}
			result, err := Prepare(context.Background(), m, data, spec)
			if err == nil || result.Estimates.MemoryBytes == 0 || result.Estimates.FileBytes == 0 {
				t.Fatal("budget did not fail with estimates", err)
			}
			if _, err := os.Stat(spec.Directory); !os.IsNotExist(err) {
				t.Fatal("preflight created a plan directory")
			}
		})
	}
	// Exercise the real postflight publication gate with measured bytes. The
	// production preflight is conservative, so a tiny valid plan normally fails
	// an undersized budget before reaching this independent gate.
	temporary := filepath.Join(t.TempDir(), "staging")
	if err := os.MkdirAll(filepath.Join(temporary, "shards"), 0755); err != nil {
		t.Fatal(err)
	}
	content := []byte("[0,1,2]\n")
	if err := os.WriteFile(filepath.Join(temporary, "shards/0.json"), content, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(temporary, "shards/0.json"))
	if err != nil {
		t.Fatal(err)
	}
	actual := uint64(info.Size())
	final := temporary + "-published"
	if err := publish(temporary, final, actual, actual-1); err == nil {
		t.Fatal("postflight accepted actual size above budget")
	}
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatal("postflight published an over-budget plan")
	}
	if err := publish(temporary, final, actual, actual); err != nil {
		t.Fatal(err)
	}
}

func overwrite(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.Chmod(name, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCorruptPlanRejected(t *testing.T) {
	for _, kind := range []string{"seed", "missing zero worker_id", "unknown", "duplicate", "objects hash", "shard hash", "duplicate region", "out of bounds", "wrong count"} {
		t.Run(kind, func(t *testing.T) {
			m, data, spec := fixture(t)
			result, err := Prepare(context.Background(), m, data, spec)
			if err != nil {
				t.Fatal(err)
			}
			body := readFile(t, result.Path)
			switch kind {
			case "seed":
				overwrite(t, result.Path, bytes.Replace(body, []byte(contract.OrderSeed), []byte("other-seed"), 1))
			case "missing zero worker_id":
				overwrite(t, result.Path, bytes.Replace(body, []byte(`"worker_id":0,`), nil, 1))
			case "unknown":
				overwrite(t, result.Path, bytes.Replace(body, []byte(`"schema_version":1`), []byte(`"extra":1,"schema_version":1`), 1))
			case "duplicate":
				overwrite(t, result.Path, bytes.Replace(body, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1))
			case "objects hash":
				overwrite(t, filepath.Join(filepath.Dir(result.Path), "object-urls.json"), []byte("[]\n"))
			case "shard hash":
				overwrite(t, filepath.Join(filepath.Dir(result.Path), "shards/0.json"), []byte("[1,2,4]\n"))
			case "duplicate region", "out of bounds", "wrong count":
				var p contract.Plan
				if err := contract.Decode(body, &p); err != nil {
					t.Fatal(err)
				}
				shard := []byte("[2,2,4]\n")
				if kind == "out of bounds" {
					shard = []byte("[2,1,8]\n")
				}
				if kind == "wrong count" {
					shard = []byte("[2,1]\n")
				}
				p.Shards[0].SHA256 = contract.Digest(shard)
				overwrite(t, filepath.Join(filepath.Dir(result.Path), "shards/0.json"), shard)
				encoded, err := contract.Encode(p)
				if err != nil {
					t.Fatal(err)
				}
				overwrite(t, result.Path, encoded)
			}
			if kind == "duplicate region" {
				expected, err := Permutation(8)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, _, err := load(context.Background(), result.Path, m, contract.Digest(data), expected); err == nil {
					t.Fatal("publication accepted duplicate")
				}
				return
			}
			if _, _, _, err := Load(context.Background(), result.Path, m, contract.Digest(data)); err == nil {
				t.Fatal("corrupt plan accepted")
			}
			if _, err := Prepare(context.Background(), m, data, spec); err == nil {
				t.Fatal("corrupt plan silently regenerated")
			}
		})
	}
}

func TestCancelledPlanIsNotPublished(t *testing.T) {
	m, data, spec := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Prepare(ctx, m, data, spec); err == nil {
		t.Fatal("cancelled plan succeeded")
	}
	entries, err := os.ReadDir(spec.Directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".incomplete-") {
			t.Fatal("cancelled plan was published")
		}
	}
	if _, err := Prepare(context.Background(), m, data, spec); err == nil {
		t.Fatal("unfinished plan adopted")
	}
}

// Canonical order is proved at publication, not regenerated by consumers.
func TestPublicationAndConsumptionValidation(t *testing.T) {
	m, data, spec := fixture(t)
	result, err := Prepare(context.Background(), m, data, spec)
	if err != nil {
		t.Fatal(err)
	}
	body := readFile(t, result.Path)
	var p contract.Plan
	if err := contract.Decode(body, &p); err != nil {
		t.Fatal(err)
	}
	shard := []byte("[1,2,4]\n")
	p.Shards[0].SHA256 = contract.Digest(shard)
	overwrite(t, filepath.Join(filepath.Dir(result.Path), "shards/0.json"), shard)
	encoded, err := contract.Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	overwrite(t, result.Path, encoded)
	if _, _, _, err := Load(context.Background(), result.Path, m, contract.Digest(data)); err != nil {
		t.Fatalf("consumer repeated canonical order validation: %v", err)
	}
	expected, err := Permutation(p.RegionCount)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := load(context.Background(), result.Path, m, contract.Digest(data), expected); err == nil {
		t.Fatal("publication accepted wrong fixed shard order")
	}
	// Changing bytes without updating the published digest remains an error.
	overwrite(t, filepath.Join(filepath.Dir(result.Path), "shards/0.json"), []byte("[2,1,4]\n"))
	if _, _, _, err := LoadWorker(context.Background(), result.Path, m, contract.Digest(data), 0); err == nil {
		t.Fatal("consumer ignored shard integrity")
	}
}

func TestRawManifestIdentity(t *testing.T) {
	m, data, spec := fixture(t)
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		t.Fatal(err)
	}
	result, err := Prepare(context.Background(), m, pretty.Bytes(), spec)
	if err != nil {
		t.Fatalf("valid raw JSON bytes rejected: %v", err)
	}
	var p contract.Plan
	if err := contract.Decode(readFile(t, result.Path), &p); err != nil {
		t.Fatal(err)
	}
	if p.ManifestSHA256 != contract.Digest(pretty.Bytes()) {
		t.Fatal("identity was re-encoded instead of hashing the actual input")
	}
}
