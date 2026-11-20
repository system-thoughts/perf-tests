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

package worker

import (
	"fmt"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/target"
)

// CheckPreparation consumes the selected success record, not live cache state.
func CheckPreparation(raw []byte, manifestID string) (target.Result, error) {
	var r target.Result
	if err := contract.DecodeRecord(raw, &r); err != nil {
		return r, err
	}
	if r.SchemaVersion != 1 || r.Status != target.StatusReady {
		return r, fmt.Errorf("schema_version/status: expected 1/ready, actual %d/%q", r.SchemaVersion, r.Status)
	}
	if r.ManifestSHA256 != manifestID {
		return r, fmt.Errorf("manifest_sha256: expected %s, actual %s", manifestID, r.ManifestSHA256)
	}
	if err := r.Target.Validate(); err != nil {
		return r, err
	}
	base, err := contract.NormalizeBaseURL(r.Source.BaseURL)
	if err != nil {
		return r, fmt.Errorf("source.base_url: %w", err)
	}
	r.Source.BaseURL = base
	return r, nil
}
