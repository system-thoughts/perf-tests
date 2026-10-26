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
	"crypto/aes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
)

func Geometry(size, count, rangeBytes, workers uint64) (perObject, total uint64, err error) {
	if size == 0 || size > contract.MaxSafeInteger || count == 0 || count > contract.MaxSafeInteger || rangeBytes == 0 || rangeBytes > contract.MaxSafeInteger || size%rangeBytes != 0 {
		return 0, 0, fmt.Errorf("positive exact size/count/range required, with size divisible by range")
	}
	perObject = size / rangeBytes
	if perObject > contract.MaxRegions || count > contract.MaxRegions/perObject {
		return 0, 0, fmt.Errorf("region_count exceeds uint32 limit")
	}
	total = count * perObject
	if workers == 0 || workers > total {
		return 0, 0, fmt.Errorf("workers must be between 1 and region_count")
	}
	return perObject, total, nil
}

func Permutation(count uint64) ([]uint32, error) {
	return permutation(context.Background(), count)
}

func permutation(ctx context.Context, count uint64) ([]uint32, error) {
	if count == 0 || count > contract.MaxRegions {
		return nil, fmt.Errorf("region_count outside uint32 range")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := sha256.Sum256([]byte(contract.OrderSeed))
	cipher, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	var counter, stream [aes.BlockSize]byte
	position := len(stream)
	next := func() uint64 {
		if position == len(stream) {
			cipher.Encrypt(stream[:], counter[:])
			for i := len(counter) - 1; i >= 0; i-- {
				counter[i]++
				if counter[i] != 0 {
					break
				}
			}
			position = 0
		}
		value := binary.BigEndian.Uint32(stream[position : position+4])
		position += 4
		return uint64(value)
	}
	values := make([]uint32, int(count))
	for i := range values {
		if i%65536 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		values[i] = uint32(i)
	}
	for i := count - 1; i > 0; i-- {
		if i%65536 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		n := i + 1
		limit := (uint64(1) << 32) / n * n
		x := next()
		for x >= limit {
			x = next()
		}
		j := x % n
		values[i], values[j] = values[j], values[i]
	}
	return values, nil
}

func ShardCount(total, workers, worker uint64) uint64 {
	return (total-1-worker)/workers + 1
}

// AllocateRates assigns the remaining units before rejecting a zero rate.
// Premature rejection after floor() would incorrectly reject M=3,W=2,RATE=2.
func AllocateRates(counts []uint64, totalRate uint64) ([]uint64, error) {
	if len(counts) == 0 || totalRate == 0 || totalRate > contract.MaxRate || totalRate < uint64(len(counts)) {
		return nil, fmt.Errorf("total RATE must be between worker_count and 2^31-1")
	}
	var total uint64
	for _, count := range counts {
		if count == 0 || count > contract.MaxRegions-total {
			return nil, fmt.Errorf("invalid shard region counts")
		}
		total += count
	}
	type remainder struct {
		id    int
		value uint64
	}
	order := make([]remainder, len(counts))
	rates := make([]uint64, len(counts))
	left := totalRate
	for i, count := range counts {
		product := totalRate * count
		rates[i] = product / total
		left -= rates[i]
		order[i] = remainder{id: i, value: product % total}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].value == order[j].value {
			return order[i].id < order[j].id
		}
		return order[i].value > order[j].value
	})
	for i := uint64(0); i < left; i++ {
		rates[order[i].id]++
	}
	for _, rate := range rates {
		if rate == 0 {
			return nil, fmt.Errorf("RATE allocation leaves a worker below 1 req/s")
		}
	}
	return rates, nil
}

func Region(p contract.Plan, g uint64) (object, offset, end uint64, err error) {
	if g >= p.RegionCount || p.RegionsPerObject == 0 || p.RangeBytes == 0 || p.RangeBytes > contract.MaxSafeInteger {
		return 0, 0, 0, fmt.Errorf("invalid region geometry")
	}
	index := g % p.RegionsPerObject
	if index > (contract.MaxSafeInteger-p.RangeBytes)/p.RangeBytes {
		return 0, 0, 0, fmt.Errorf("region offset/end outside exact integer range")
	}
	offset = index * p.RangeBytes
	return g / p.RegionsPerObject, offset, offset + p.RangeBytes - 1, nil
}
