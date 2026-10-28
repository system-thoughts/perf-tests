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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
)

func Load(ctx context.Context, name string, manifest contract.Manifest, manifestSHA string) (contract.Plan, []byte, uint64, error) {
	return load(ctx, name, manifest, manifestSHA, nil)
}

// LoadWorker validates common metadata and only the selected shard's bytes.
// Other workers' files need not be mounted in this process.
func LoadWorker(ctx context.Context, name string, manifest contract.Manifest, manifestSHA string, worker uint64) (contract.Plan, []byte, uint64, error) {
	return loadSelected(ctx, name, manifest, manifestSHA, nil, &worker)
}
func load(ctx context.Context, name string, manifest contract.Manifest, manifestSHA string, expected []uint32) (contract.Plan, []byte, uint64, error) {
	return loadSelected(ctx, name, manifest, manifestSHA, expected, nil)
}
func loadSelected(ctx context.Context, name string, manifest contract.Manifest, manifestSHA string, expected []uint32, selected *uint64) (p contract.Plan, raw []byte, actual uint64, err error) {
	raw, err = os.ReadFile(name)
	if err != nil {
		return p, nil, 0, contract.Input("PLAN", name, err)
	}
	if err := contract.Decode(raw, &p); err != nil {
		return p, nil, 0, contract.Input("PLAN", name, err)
	}
	if p.SchemaVersion != 1 || p.Algorithm != contract.OrderAlgorithm || p.Seed != contract.OrderSeed || p.ManifestSHA256 != manifestSHA {
		return p, nil, 0, contract.Input("PLAN", name, fmt.Errorf("schema/algorithm/seed or manifest_sha256 mismatch: expected manifest %s, actual %s", manifestSHA, p.ManifestSHA256))
	}
	perObject, total, err := Geometry(manifest.ObjectSize, manifest.ObjectCount, p.RangeBytes, p.WorkerCount)
	if err != nil {
		return p, nil, 0, err
	}
	if p.RegionsPerObject != perObject || p.RegionCount != total || uint64(len(p.Shards)) != p.WorkerCount {
		return p, nil, 0, contract.Input("PLAN", name, fmt.Errorf("region metadata mismatch: expected regions_per_object=%d region_count=%d worker_count=%d, actual %d/%d/%d", perObject, total, p.WorkerCount, p.RegionsPerObject, p.RegionCount, len(p.Shards)))
	}
	if selected != nil && *selected >= p.WorkerCount {
		return p, nil, 0, contract.Input("PLAN", name, fmt.Errorf("WORKER_ID %d outside worker_count %d", *selected, p.WorkerCount))
	}
	directory := filepath.Dir(name)
	actual = uint64(len(raw))
	// Only publication supplies the producer's permutation. Consumer and worker
	// startup check artifact integrity without repeating the generation algorithm.
	var seen []byte
	if expected != nil {
		seen = make([]byte, int((total+7)/8))
	}
	for worker, shard := range p.Shards {
		if shard.WorkerID != uint64(worker) || shard.RegionCount != ShardCount(total, p.WorkerCount, uint64(worker)) || shard.Path != "shards/"+strconv.Itoa(worker)+".json" || !contract.ValidDigest(shard.SHA256) {
			return p, nil, 0, fmt.Errorf("shard %d metadata mismatch", worker)
		}
		if selected != nil && *selected != uint64(worker) {
			continue
		}
		length, err := checkShard(ctx, filepath.Join(directory, filepath.FromSlash(shard.Path)), shard, total, seen, expected, p.WorkerCount)
		if err != nil {
			return p, nil, 0, contract.Input(fmt.Sprintf("PLAN.shards[%d]", worker), filepath.Join(directory, filepath.FromSlash(shard.Path)), err)
		}
		actual += length
	}
	return p, raw, actual, nil
}

func checkShard(ctx context.Context, name string, shard contract.Shard, total uint64, seen []byte, expected []uint32, workers uint64) (length uint64, err error) {
	info, err := os.Lstat(name)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("shard must be a regular file")
	}
	f, err := os.Open(name)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	hash := sha256.New()
	decoder := json.NewDecoder(io.TeeReader(f, hash))
	decoder.UseNumber()
	start, err := decoder.Token()
	if err != nil || start != json.Delim('[') {
		return 0, fmt.Errorf("expected shard JSON array")
	}
	var count uint64
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		token, err := decoder.Token()
		if err != nil {
			return 0, err
		}
		number, ok := token.(json.Number)
		if !ok {
			return 0, fmt.Errorf("region must be an integer")
		}
		g, err := strconv.ParseUint(string(number), 10, 32)
		if err != nil || g >= total || count >= shard.RegionCount {
			return 0, fmt.Errorf("region/count outside shard bounds")
		}
		if expected != nil && uint64(expected[shard.WorkerID+count*workers]) != g {
			return 0, fmt.Errorf("region %d violates fixed shard order or membership", g)
		}
		if seen != nil {
			mask := byte(1 << (g % 8))
			if seen[g/8]&mask != 0 {
				return 0, fmt.Errorf("duplicate region %d", g)
			}
			seen[g/8] |= mask
		}
		count++
	}
	if _, err := decoder.Token(); err != nil {
		return 0, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return 0, fmt.Errorf("unexpected data after shard array")
	}
	if count != shard.RegionCount || hex.EncodeToString(hash.Sum(nil)) != shard.SHA256 {
		return 0, fmt.Errorf("shard count/sha256 mismatch: expected count=%d sha256=%s, actual count=%d sha256=%s", shard.RegionCount, shard.SHA256, count, hex.EncodeToString(hash.Sum(nil)))
	}
	return uint64(info.Size()), nil
}
