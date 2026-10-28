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

const (
	SchemaVersion           = 1
	ContentAlgorithm        = "aes256-counter-v1"
	ContentSeed             = "nydus-range-content-v1"
	BlockBytes              = 16
	OrderAlgorithm          = "fisher-yates-aes32-v1"
	OrderSeed               = "nydus-range-order-v1"
	MaxSafeInteger   uint64 = 1<<53 - 1
	MaxRegions       uint64 = 1<<32 - 1
	MaxRate          uint64 = 1<<31 - 1
)

type Generator struct {
	Version    string `json:"version"`
	Seed       string `json:"seed"`
	BlockBytes uint64 `json:"block_bytes"`
}

type Object struct {
	ID           uint64 `json:"object_id"`
	Size         uint64 `json:"size_bytes"`
	SHA256       string `json:"sha256"`
	RelativePath string `json:"relative_path"`
	URL          string `json:"url"`
}

type Manifest struct {
	SchemaVersion int       `json:"schema_version"`
	Generator     Generator `json:"generator"`
	ObjectSize    uint64    `json:"object_size_bytes"`
	ObjectCount   uint64    `json:"object_count"`
	BaseURL       string    `json:"base_url"`
	Objects       []Object  `json:"objects"`
}

type FileRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Shard struct {
	WorkerID    uint64 `json:"worker_id"`
	RegionCount uint64 `json:"region_count"`
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
}

type Plan struct {
	SchemaVersion    int     `json:"schema_version"`
	ManifestSHA256   string  `json:"manifest_sha256"`
	RangeBytes       uint64  `json:"range_bytes"`
	RegionsPerObject uint64  `json:"regions_per_object"`
	RegionCount      uint64  `json:"region_count"`
	Algorithm        string  `json:"algorithm"`
	Seed             string  `json:"seed"`
	WorkerCount      uint64  `json:"worker_count"`
	Shards           []Shard `json:"shards"`
}
