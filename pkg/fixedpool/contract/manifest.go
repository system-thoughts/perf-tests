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
	"path/filepath"
)

// ReadManifest hashes and parses the same input bytes; it never opens object files.
func ReadManifest(name string) (Manifest, []byte, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return Manifest{}, nil, Input("MANIFEST", name, Tag(InputInvalid, err))
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		return manifest, nil, Input("MANIFEST", name, err)
	}
	return manifest, data, nil
}

func ParseManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	err := Decode(data, &manifest)
	if err == nil {
		err = ValidateManifest(manifest)
	}
	if err != nil {
		return manifest, Tag(ContractMismatch, err)
	}
	return manifest, nil
}

// Input preserves error classification while identifying the consumed file.
func Input(input, path string, err error) error {
	if absolute, absErr := filepath.Abs(path); absErr == nil {
		path = absolute
	}
	return fmt.Errorf("%s %q: %w", input, path, err)
}
