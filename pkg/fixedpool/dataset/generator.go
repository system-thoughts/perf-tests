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
	"crypto/aes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
)

const writeBufferBytes = 1 << 20

// Generate streams reproducible bytes; request geometry never enters the key,
// counter, or write buffer. The public fixed key is not a confidentiality feature.
func Generate(ctx context.Context, writer io.Writer, objectID, size uint64) error {
	if size == 0 || size > contract.MaxSafeInteger {
		return fmt.Errorf("object size outside exact integer range")
	}
	key := sha256.Sum256([]byte(contract.ContentSeed))
	cipher, err := aes.NewCipher(key[:])
	if err != nil {
		return err
	}
	buffer := make([]byte, writeBufferBytes)
	var counter [aes.BlockSize]byte
	binary.BigEndian.PutUint64(counter[:8], objectID)
	var block uint64
	for remaining := size; remaining > 0; {
		if err := ctx.Err(); err != nil {
			return err
		}
		length := min(uint64(len(buffer)), remaining)
		for offset := uint64(0); offset < length; offset += aes.BlockSize {
			binary.BigEndian.PutUint64(counter[8:], block)
			cipher.Encrypt(buffer[offset:offset+aes.BlockSize], counter[:])
			block++
		}
		written, err := writer.Write(buffer[:length])
		if err != nil {
			return err
		}
		if uint64(written) != length {
			return io.ErrShortWrite
		}
		remaining -= length
	}
	return nil
}
