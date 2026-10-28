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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/target"
)

// CheckPreparation checks the published ready record and evidence integrity.
// Full object semantics are established by preparation, not re-audited by each worker.
func CheckPreparation(raw []byte, name string, m contract.Manifest, manifestID string, c target.Config) error {
	var r target.Result
	if err := contract.DecodeRecord(raw, &r); err != nil {
		return fmt.Errorf("TARGET_PREPARATION: %w", err)
	}
	if r.SchemaVersion != 1 || r.PreparationID == "" || r.Status != target.StatusReady || len(r.Errors) != 0 {
		return fmt.Errorf("target-preparation requires schema=1, preparation_id, status=ready and no errors; actual schema=%d id=%q status=%q errors=%d", r.SchemaVersion, r.PreparationID, r.Status, len(r.Errors))
	}
	if r.ManifestSHA256 == nil || *r.ManifestSHA256 != manifestID {
		actual := "<missing>"
		if r.ManifestSHA256 != nil {
			actual = *r.ManifestSHA256
		}
		return fmt.Errorf("target-preparation manifest_sha256 mismatch: expected %s, actual %s", manifestID, actual)
	}
	if r.PieceStrategy != "auto" || r.Method != target.Method {
		return fmt.Errorf("target-preparation piece_strategy/method mismatch")
	}
	if r.Target == nil || r.Target.Proxy != c.Target.Proxy || r.Target.ExpectedServerIP != c.Target.ExpectedServerIP {
		return fmt.Errorf("target-preparation target.proxy/expected_server_ip mismatch: expected %s/%s, actual %+v", c.Target.Proxy, c.Target.ExpectedServerIP, r.Target)
	}
	if r.ObjectCount == nil || *r.ObjectCount != m.ObjectCount || r.Prepare == nil || r.Prepare.Concurrency == 0 || r.Prepare.RequestTimeoutMS == 0 || r.Prepare.ReadyTimeoutMS == 0 {
		return fmt.Errorf("target-preparation object_count/prepare invalid")
	}
	if r.EvidenceFile == nil || r.EvidenceFile.Path != "target-object-results.json" || !contract.ValidDigest(r.EvidenceFile.SHA256) {
		return fmt.Errorf("target-preparation evidence_file invalid")
	}
	digest, err := evidenceDigest(filepath.Join(filepath.Dir(name), r.EvidenceFile.Path))
	if err != nil {
		return contract.Input("TARGET_PREPARATION.evidence_file", filepath.Join(filepath.Dir(name), r.EvidenceFile.Path), err)
	}
	if digest != r.EvidenceFile.SHA256 {
		return contract.Input("TARGET_PREPARATION.evidence_file", filepath.Join(filepath.Dir(name), r.EvidenceFile.Path), fmt.Errorf("sha256 mismatch: expected %s, actual %s", r.EvidenceFile.SHA256, digest))
	}
	return nil
}

// Hash as a stream: ready evidence can be large, and workers do not parse it.
func evidenceDigest(name string) (digest string, err error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
