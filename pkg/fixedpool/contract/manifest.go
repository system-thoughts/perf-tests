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

package contract

import (
	"fmt"
	"os"
)

// ReadManifest hashes and parses the same input bytes; it never opens object files.
func ReadManifest(name string) (Manifest, []byte, error) {
	var manifest Manifest
	data, err := os.ReadFile(name)
	if err != nil {
		return manifest, nil, Tag(InputInvalid, err)
	}
	if err := Decode(data, &manifest); err != nil {
		return manifest, nil, Tag(ContractMismatch, fmt.Errorf("manifest: %w", err))
	}
	if err := ValidateManifest(manifest); err != nil {
		return manifest, nil, Tag(ContractMismatch, err)
	}
	return manifest, data, nil
}
