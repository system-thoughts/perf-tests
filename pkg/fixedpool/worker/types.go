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
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/target"
)

const Prefix = "PROXY_BENCH_FIXED_POOL_"

type Issue struct {
	Code     string  `json:"code"`
	Stage    string  `json:"stage"`
	Message  string  `json:"message"`
	WorkerID *uint64 `json:"worker_id,omitempty"`
	Path     string  `json:"path,omitempty"`
}
type Allocation struct {
	WorkerID    uint64 `json:"worker_id"`
	RegionCount uint64 `json:"region_count"`
	Rate        uint64 `json:"rate"`
}
type RunSpec struct {
	SchemaVersion     int               `json:"schema_version"`
	RunID             string            `json:"run_id"`
	Manifest          contract.FileRef  `json:"manifest"`
	TargetConfig      contract.FileRef  `json:"target_config"`
	TargetPreparation contract.FileRef  `json:"target_preparation"`
	Plan              contract.FileRef  `json:"plan"`
	Target            target.Target     `json:"target"`
	TotalRate         uint64            `json:"total_rate"`
	WorkerCount       uint64            `json:"worker_count"`
	DurationMS        int64             `json:"duration_ms"`
	TimeoutMS         int64             `json:"timeout_ms"`
	GracefulStopMS    int64             `json:"graceful_stop_ms"`
	Workers           []Allocation      `json:"workers"`
	ToolVersions      map[string]string `json:"tool_versions"`
}
type Spec struct {
	SchemaVersion    int               `json:"schema_version"`
	RunID            string            `json:"run_id"`
	WorkerID         uint64            `json:"worker_id"`
	PlanID           string            `json:"plan_id"`
	ManifestID       string            `json:"manifest_id"`
	Manifest         contract.FileRef  `json:"manifest"`
	Shard            contract.FileRef  `json:"shard"`
	RegionCount      uint64            `json:"region_count"`
	RangeBytes       uint64            `json:"range_bytes"`
	RegionsPerObject uint64            `json:"regions_per_object"`
	Proxy            string            `json:"proxy"`
	Headers          map[string]string `json:"headers"`
	Rate             uint64            `json:"rate"`
	DurationMS       int64             `json:"duration_ms"`
	TimeoutMS        int64             `json:"timeout_ms"`
	GracefulStopMS   int64             `json:"graceful_stop_ms"`
	PreAllocatedVUs  uint64            `json:"pre_allocated_vus"`
	MaxVUs           uint64            `json:"max_vus"`
}
