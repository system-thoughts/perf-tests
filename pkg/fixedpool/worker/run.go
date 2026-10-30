/*
 * Copyright 2026 The Dragonfly Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/plan"
	"github.com/dragonflyoss/perf-tests/pkg/proxybench"
	vegeta "github.com/tsenart/vegeta/v12/lib"
)

func Targets(spec Spec) (vegeta.Targeter, error) {
	m, _, err := contract.ReadManifest(spec.Manifest.Path)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(spec.Shard.Path)
	if err != nil {
		return nil, err
	}
	var shard []uint32
	if err = json.Unmarshal(raw, &shard); err != nil {
		return nil, err
	}
	if uint64(len(shard)) != spec.RegionCount || len(shard) == 0 {
		return nil, fmt.Errorf("shard length mismatch")
	}
	p := contract.Plan{RegionCount: spec.RegionsPerObject * m.ObjectCount, RegionsPerObject: spec.RegionsPerObject, RangeBytes: spec.RangeBytes}
	headers := http.Header{}
	for key, value := range spec.Headers {
		headers.Set(key, value)
	}
	var iteration uint64
	return func(t *vegeta.Target) error {
		g := uint64(shard[iteration%spec.RegionCount])
		object, start, end, err := plan.Region(p, g)
		if err != nil || object >= uint64(len(m.Objects)) {
			return fmt.Errorf("invalid fixed-pool plan mapping: region %d", g)
		}
		*t = vegeta.Target{Method: http.MethodGet, URL: m.Objects[object].URL, Header: headers.Clone()}
		t.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		iteration++
		return nil
	}, nil
}

// Run consumes only the private, validated worker input. It never prepares data.
func Run(ctx context.Context, name string, out io.Writer) error {
	raw, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	var spec Spec
	if err = contract.DecodeRecord(raw, &spec); err != nil {
		return err
	}
	next, err := Targets(spec)
	if err != nil {
		return err
	}
	o := proxybench.Options{Mode: "fixed-pool", Proxy: spec.Proxy, Rate: spec.Rate, InitialWorkers: spec.PreAllocatedVUs, MaxWorkers: spec.MaxVUs, Duration: time.Duration(spec.DurationMS) * time.Millisecond, Timeout: time.Duration(spec.TimeoutMS) * time.Millisecond, GracefulStop: time.Duration(spec.GracefulStopMS) * time.Millisecond, ExpectedStatus: http.StatusPartialContent}
	o.Target = "dataset " + spec.ManifestID
	o.Range = fmt.Sprintf("%d bytes per plan entry", spec.RangeBytes)
	report, runErr := proxybench.Execute(ctx, o, next)
	if reportErr := report.WriteText(out); reportErr != nil {
		return errors.Join(runErr, reportErr)
	}
	envelope := struct {
		SchemaVersion int    `json:"schema_version"`
		RunID         string `json:"run_id"`
		WorkerID      uint64 `json:"worker_id"`
		ManifestID    string `json:"manifest_id"`
		PlanID        string `json:"plan_id"`
		proxybench.Report
	}{1, spec.RunID, spec.WorkerID, spec.ManifestID, spec.PlanID, report}
	return errors.Join(runErr, Emit(out, "VEGETA_SUMMARY", envelope))
}
