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
	"time"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
	"github.com/google/uuid"
)

type Result struct {
	SchemaVersion  int      `json:"schema_version"`
	PreparationID  string   `json:"preparation_id,omitempty"`
	ManifestSHA256 string   `json:"manifest_sha256"`
	Target         Target   `json:"target"`
	Prepare        *Options `json:"prepare,omitempty"`
	StartedAt      string   `json:"started_at,omitempty"`
	EndedAt        string   `json:"ended_at,omitempty"`
	Status         string   `json:"status"`
	ObjectCount    uint64   `json:"object_count,omitempty"`
}

type Spec struct {
	Manifest, TargetConfig, Out string
	Options                     Options
}

func Prepare(ctx context.Context, s Spec) (result Result, err error) {
	if s.Manifest == "" || s.TargetConfig == "" || s.Out == "" {
		return result, contract.Tag(contract.InputInvalid, fmt.Errorf("manifest, target-config and out are required"))
	}
	if err := s.Options.validate(); err != nil {
		return result, contract.Tag(contract.InputInvalid, err)
	}
	m, raw, err := contract.ReadManifest(s.Manifest)
	if err != nil {
		return result, err
	}
	c, _, err := ReadConfig(s.TargetConfig)
	if err != nil {
		return result, err
	}
	lock, err := contract.AcquireLock(s.Out, ".prepare.lock")
	if err != nil {
		return result, contract.Tag(contract.DataConflict, err)
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	if err := contract.RequireEmpty(s.Out, ".prepare.lock"); err != nil {
		return result, err
	}
	started := timestamp()
	client, transport, err := newClient(c)
	if err != nil {
		return result, err
	}
	defer transport.CloseIdleConnections()
	var mu sync.Mutex
	var failure error
	tasks := map[string]uint64{}
	next := 0
	var wait sync.WaitGroup
	for range min(s.Options.Concurrency, m.ObjectCount) {
		wait.Go(func() {
			for {
				mu.Lock()
				if failure != nil || ctx.Err() != nil || next == len(m.Objects) {
					mu.Unlock()
					return
				}
				o := m.Objects[next]
				next++
				mu.Unlock()
				stage := "full_get"
				task, err := fullRead(ctx, client, c, o, time.Duration(s.Options.RequestTimeoutMS)*time.Millisecond)
				if err == nil {
					mu.Lock()
					if other, exists := tasks[task]; exists {
						err = contract.Tag(contract.PrepareFailed, fmt.Errorf("objects %d and %d share task %s", other, o.ID, task))
					} else {
						tasks[task] = o.ID
					}
					mu.Unlock()
				}
				if err == nil {
					stage = "range_check"
					err = rangeCheck(ctx, client, c, o, task, time.Duration(s.Options.ReadyTimeoutMS)*time.Millisecond)
				}
				if err != nil {
					mu.Lock()
					if failure == nil {
						failure = fmt.Errorf("object_id=%d stage=%s url=%q task_id=%q expected_server_ip=%q: %w", o.ID, stage, o.URL, task, c.Target.ExpectedServerIP, err)
					}
					mu.Unlock()
					return
				}
			}
		})
	}
	wait.Wait()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if failure != nil {
		return result, failure
	}
	result = Result{SchemaVersion: 1, PreparationID: uuid.NewString(), ManifestSHA256: contract.Digest(raw), Target: c.Target, Prepare: &s.Options, StartedAt: started, EndedAt: timestamp(), Status: StatusReady, ObjectCount: m.ObjectCount}
	data, err := contract.Encode(result)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = publish(s.Out, "target-preparation.json", result.PreparationID, data)
	}
	if err != nil {
		result.Status = ""
	}
	return result, err
}

func publish(directory, name, id string, data []byte) error {
	temporary := filepath.Join(directory, "."+name+".tmp-"+id)
	if err := contract.WriteFile(temporary, data); err != nil {
		return err
	}
	destination := filepath.Join(directory, name)
	if err := os.Link(temporary, destination); err != nil {
		return err
	}
	if err := contract.SyncDir(directory); err != nil {
		return errors.Join(err, os.Remove(destination))
	}
	// The complete record is published. Cleanup cannot turn it into a failed preparation.
	_ = os.Remove(temporary)
	return nil
}
