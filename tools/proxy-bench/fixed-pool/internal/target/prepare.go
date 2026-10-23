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

package target

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/contract"
	"github.com/google/uuid"
)

type Issue struct {
	Code     string  `json:"code"`
	Stage    string  `json:"stage"`
	Message  string  `json:"message"`
	ObjectID *uint64 `json:"object_id,omitempty"`
}

type ObjectResult struct {
	ObjectID            uint64  `json:"object_id"`
	TaskID              *string `json:"task_id"`
	ServerIP            *string `json:"server_ip"`
	ReceivedBytes       *uint64 `json:"received_bytes"`
	SHA256              *string `json:"sha256"`
	FullReadCompletedAt *string `json:"full_read_completed_at"`
	FinishedCheckAt     *string `json:"finished_check_at"`
	Status              string  `json:"status"`
	Error               *Issue  `json:"error"`
}

type ObjectResults struct {
	SchemaVersion int            `json:"schema_version"`
	Objects       []ObjectResult `json:"objects"`
}

type Result struct {
	SchemaVersion  int               `json:"schema_version"`
	PreparationID  string            `json:"preparation_id"`
	ManifestSHA256 *string           `json:"manifest_sha256"`
	IdentitySHA256 *string           `json:"identity_sha256"`
	PieceStrategy  string            `json:"piece_strategy"`
	Target         *Target           `json:"target"`
	Prepare        *Options          `json:"prepare"`
	Method         string            `json:"method"`
	StartedAt      string            `json:"started_at"`
	EndedAt        string            `json:"ended_at"`
	Status         string            `json:"status"`
	ObjectCount    *uint64           `json:"object_count"`
	EvidenceFile   *contract.FileRef `json:"evidence_file"`
	Errors         []Issue           `json:"errors"`
}

type Spec struct{ Manifest, TargetConfig, Out string }

// NewResult initializes the same failure-safe envelope for parsing and runtime errors.
func NewResult() Result {
	return Result{SchemaVersion: 1, PreparationID: uuid.NewString(), PieceStrategy: "auto", Method: Method, StartedAt: timestamp(), EndedAt: timestamp(), Status: StatusUnverified, Errors: []Issue{}}
}

func Prepare(ctx context.Context, s Spec) (result Result, err error) {
	result = NewResult()
	result.EndedAt = ""
	defer func() {
		if result.EndedAt == "" {
			result.EndedAt = timestamp()
		}
		if err != nil && len(result.Errors) == 0 {
			result.Errors = append(result.Errors, Issue{Code: contract.ErrorCode(err), Stage: "target_prepare", Message: err.Error()})
		}
	}()
	if s.Manifest == "" || s.TargetConfig == "" || s.Out == "" {
		return result, contract.Tag(contract.InputInvalid, fmt.Errorf("manifest, target-config and out are required"))
	}
	m, raw, err := contract.ReadManifest(s.Manifest)
	if err != nil {
		return result, err
	}
	digest := contract.Digest(raw)
	result.ManifestSHA256 = &digest
	result.ObjectCount = &m.ObjectCount
	c, identity, err := ReadConfig(s.TargetConfig)
	if err != nil {
		return result, err
	}
	result.IdentitySHA256 = &identity
	result.Target = &c.Target
	result.Prepare = &c.Prepare
	lock, err := contract.AcquireLock(s.Out, ".prepare.lock")
	if err != nil {
		return result, contract.Tag(contract.DataConflict, err)
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	entries, err := os.ReadDir(s.Out)
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if entry.Name() != ".prepare.lock" {
			return result, contract.Tag(contract.DataConflict, fmt.Errorf("out must be empty; existing entry %s", entry.Name()))
		}
	}
	client, transport, err := newClient(c)
	if err != nil {
		return result, err
	}
	defer transport.CloseIdleConnections()
	objects := ObjectResults{SchemaVersion: 1, Objects: make([]ObjectResult, len(m.Objects))}
	for i, o := range m.Objects {
		objects.Objects[i] = ObjectResult{ObjectID: o.ID, Status: StatusUnverified}
	}
	var mu sync.Mutex
	seenTasks := map[string]uint64{}
	runPhase := func(final bool) {
		var wait sync.WaitGroup
		next := 0
		stopped := false
		for range min(c.Prepare.Concurrency, m.ObjectCount) {
			wait.Go(func() {
				for {
					mu.Lock()
					if stopped || ctx.Err() != nil || next >= len(m.Objects) {
						mu.Unlock()
						return
					}
					index := next
					next++
					mu.Unlock()
					o := m.Objects[index]
					r := &objects.Objects[index]
					stage := "full_get"
					var failure error
					if !final {
						failure = fullRead(ctx, client, c, o, r)
						if failure == nil {
							mu.Lock()
							if other, exists := seenTasks[*r.TaskID]; exists && other != o.ID {
								failure = contract.Tag(contract.PrepareFailed, fmt.Errorf("objects %d and %d unexpectedly share task %s", other, o.ID, *r.TaskID))
							} else {
								seenTasks[*r.TaskID] = o.ID
							}
							mu.Unlock()
						}
					}
					if failure == nil {
						stage = "finished_check"
						if final {
							stage = "final_check"
							r.FinishedCheckAt = nil
						}
						failure = finishedCheck(ctx, client, c, o, r)
					}
					mu.Lock()
					if failure != nil {
						code := contract.ErrorCode(failure)
						r.Status = StatusFailed
						if code == Unverified || code == Unsupported || ctx.Err() != nil {
							r.Status = StatusUnverified
						}
						r.Error = &Issue{Code: code, Stage: stage, Message: failure.Error(), ObjectID: &r.ObjectID}
						stopped = true
					} else {
						r.Status = StatusReady
					}
					mu.Unlock()
				}
			})
		}
		wait.Wait()
	}
	runPhase(false)
	allReady := func() bool {
		for _, r := range objects.Objects {
			if r.Status != StatusReady {
				return false
			}
		}
		return ctx.Err() == nil
	}
	if allReady() {
		for i := range objects.Objects {
			objects.Objects[i].Status = StatusUnverified
			objects.Objects[i].FinishedCheckAt = nil
		}
		runPhase(true)
	}
	err = summarize(ctx, &objects, &result)
	result.EndedAt = timestamp()
	data, encodeErr := contract.Encode(objects)
	if encodeErr != nil {
		return result, errors.Join(err, encodeErr)
	}
	if writeErr := publish(s.Out, "target-object-results.json", result.PreparationID, data); writeErr != nil {
		return result, publicationFailure(&result, errors.Join(err, writeErr))
	}
	result.EvidenceFile = &contract.FileRef{Path: "target-object-results.json", SHA256: contract.Digest(data)}
	data, encodeErr = contract.Encode(result)
	if encodeErr != nil {
		return result, publicationFailure(&result, errors.Join(err, encodeErr))
	}
	if writeErr := publish(s.Out, "target-preparation.json", result.PreparationID, data); writeErr != nil {
		return result, publicationFailure(&result, errors.Join(err, writeErr))
	}
	return result, err
}

func publicationFailure(r *Result, err error) error {
	r.Status = StatusUnverified
	r.Errors = append(r.Errors, Issue{Code: contract.PrepareFailed, Stage: "publish", Message: err.Error()})
	return err
}

func publish(directory, name, id string, data []byte) error {
	temporary := filepath.Join(directory, "."+name+".tmp-"+id)
	if err := contract.WriteFile(temporary, data); err != nil {
		return err
	}
	// Hard-link publication is atomic and refuses to replace a destination that
	// appeared after the output-directory check. Retain failed temporaries.
	if err := os.Link(temporary, filepath.Join(directory, name)); err != nil {
		return err
	}
	if err := os.Remove(temporary); err != nil {
		return err
	}
	return contract.SyncDir(directory)
}

// summarize decides readiness from all object outcomes and cancellation.
func summarize(ctx context.Context, objects *ObjectResults, result *Result) error {
	result.Status = StatusReady
	for i := range objects.Objects {
		r := &objects.Objects[i]
		if r.Status != StatusReady {
			if r.Error == nil {
				r.Error = &Issue{Code: Unverified, Stage: "target_prepare", Message: "not completed after cancellation or an earlier object failure", ObjectID: &r.ObjectID}
			}
			result.Errors = append(result.Errors, *r.Error)
			if r.Status == StatusFailed {
				result.Status = StatusFailed
			} else if result.Status == StatusReady {
				result.Status = StatusUnverified
			}
		}
	}
	if ctx.Err() != nil {
		result.Errors = append(result.Errors, Issue{Code: "INTERRUPTED", Stage: "target_prepare", Message: ctx.Err().Error()})
		if result.Status == StatusReady {
			result.Status = StatusUnverified
		}
	}
	if result.Status != StatusReady {
		if result.Status == StatusFailed {
			return contract.Tag(contract.PrepareFailed, fmt.Errorf("target preparation failed; inspect per-object evidence"))
		}
		return contract.Tag(Unverified, fmt.Errorf("target preparation is %s; inspect per-object evidence", result.Status))
	}
	return nil
}
