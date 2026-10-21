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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/contract"
	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/dataset"
)

type fixture struct {
	active     int
	maxActive  int
	fullStarts int
	startGate  chan struct{}
	spec       Spec
	manifest   contract.Manifest
	bodies     map[string][]byte
	mu         sync.Mutex
	requests   []string
	probes     map[string]int
	mode       string
}

func newFixture(t *testing.T, mode string, count uint64) *fixture {
	t.Helper()
	dir := t.TempDir()
	d, err := dataset.Prepare(context.Background(), dataset.Spec{Root: filepath.Join(dir, "dataset"), BaseURL: "http://origin.invalid", Size: 65552, Count: count, GenerationWorkers: 1})
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := contract.ReadManifest(d.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{manifest: m, bodies: map[string][]byte{}, probes: map[string]int{}, mode: mode}
	for _, o := range m.Objects {
		data, err := os.ReadFile(filepath.Join(dir, "dataset", o.RelativePath))
		if err != nil {
			t.Fatal(err)
		}
		f.bodies["/"+o.RelativePath] = data
	}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	rules := []byte("{\"schema_version\":1,\"filtered_query_params\":[],\"rewrite_url\":false,\"use_tls\":false}\n")
	if err := os.WriteFile(filepath.Join(dir, "rules.json"), rules, 0600); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("schema_version: 1\ntarget:\n  ref: seed-one\n  proxy: %s\n  expected_server_ip: 192.0.2.10\n  instance_ref: test-instance\nproxy_rules_ref:\n  path: rules.json\n  sha256: %s\n", server.URL, contract.Digest(rules))
	name := filepath.Join(dir, "target.yaml")
	if err := os.WriteFile(name, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	f.startGate = make(chan struct{})
	f.spec = Spec{Manifest: d.ManifestPath, TargetConfig: name, Out: filepath.Join(dir, "result")}
	return f
}

func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	if f.mode == "concurrent" && r.Header.Get("Range") == "" {
		f.fullStarts++
		if f.fullStarts == 2 {
			close(f.startGate)
		}
	}
	f.requests = append(f.requests, r.Header.Get("Range")+" "+r.URL.String())
	body, exists := f.bodies[r.URL.Path]
	probe := r.Header.Get("Range") != ""
	if probe {
		f.probes[r.URL.Path]++
	}
	probeCount := f.probes[r.URL.Path]
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.active--; f.mu.Unlock() }()
	if f.mode == "concurrent" {
		select {
		case <-f.startGate:
		case <-r.Context().Done():
			return
		}
	}
	if !exists {
		http.NotFound(w, r)
		return
	}
	if !r.URL.IsAbs() || r.Header.Get("X-Dragonfly-Use-P2P") != headerTrue || r.Header.Get("X-Dragonfly-Enable-Task-ID-Based-Blob-Digest") != headerFalse || r.Header.Get("X-Dragonfly-Prefetch") != headerFalse || r.Header.Get("Accept-Encoding") != "identity" || r.Header.Get("X-Dragonfly-Piece-Length") != "" || r.Header.Get("X-Dragonfly-Tag") != "" || r.Header.Get("X-Dragonfly-Application") != "" {
		http.Error(w, "invalid request identity", http.StatusBadRequest)
		return
	}
	if f.mode == "redirect" {
		http.Redirect(w, r, "http://must-not-follow.invalid", http.StatusFound)
		return
	}
	if f.mode == "timeout" || f.mode == "cancel" || (f.mode == "probe-timeout" && probe) {
		<-r.Context().Done()
		return
	}
	task := strings.TrimPrefix(r.URL.Path, "/objects/sha256/")
	if f.mode == "duplicate" {
		task = "shared-task"
	}
	if f.mode == "changed-task" && probe {
		task = "another-task"
	}
	if f.mode != "plain-http" {
		w.Header().Set(taskHeader, task)
		w.Header().Set(serverHeader, "192.0.2.10")
	}
	if f.mode == "wrong-ip" {
		w.Header().Set(serverHeader, "192.0.2.11")
	}
	if f.mode == "duplicate-header" {
		w.Header().Add(taskHeader, "ambiguous-task")
	}
	if f.mode == "compressed" {
		w.Header().Set("Content-Encoding", "gzip")
	}
	if f.mode == "wrong-body" {
		// A true task marker cannot substitute for full-body integrity.
		w.Header().Set(finishedHeader, headerTrue)
		body = bytes.Repeat([]byte{42}, len(body))
	}
	if !probe {
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeader(http.StatusOK)
		if f.mode == "truncated" {
			_, _ = w.Write(body[:len(body)-1])
		} else {
			_, _ = w.Write(body)
		}
		return
	}
	if f.mode != "missing-marker" && (f.mode != "collected-before-final" || probeCount != 2) {
		w.Header().Set(finishedHeader, headerTrue)
	}
	if f.mode == "false-marker" {
		w.Header().Set(finishedHeader, headerFalse)
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-65535/%d", len(body)))
	w.Header().Set("Content-Length", "65536")
	if f.mode == "wrong-content-range" {
		w.Header().Set("Content-Range", "bytes 1-65536/65552")
	}
	if f.mode == "wrong-length" {
		w.Header().Set("Content-Length", "65535")
	}
	if f.mode == "probe-200" {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusPartialContent)
	}
	switch f.mode {
	case "wrong-probe-bytes":
		_, _ = w.Write(bytes.Repeat([]byte{42}, 65536))
	case "wrong-length":
		_, _ = w.Write(body[:65535])
	default:
		_, _ = w.Write(body[:65536])
	}
}

func TestReadyRequiresFullBodyAndBothChecks(t *testing.T) {
	f := newFixture(t, "ok", 3)
	// A manifest origin must still use the explicitly configured proxy even when
	// ambient proxy variables would bypass all hosts.
	t.Setenv("NO_PROXY", "*")
	t.Setenv("HTTP_PROXY", "http://invalid.invalid:9")
	r, err := Prepare(context.Background(), f.spec)
	if err != nil || r.Status != StatusReady || len(r.Errors) != 0 || r.Prepare.Concurrency != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if r.EvidenceFile == nil {
		t.Fatal("missing evidence reference")
	}
	data, err := os.ReadFile(filepath.Join(f.spec.Out, r.EvidenceFile.Path))
	if err != nil {
		t.Fatal(err)
	}
	if contract.Digest(data) != r.EvidenceFile.SHA256 {
		t.Fatal("evidence hash mismatch")
	}
	var results ObjectResults
	if err := json.Unmarshal(data, &results); err != nil {
		t.Fatal(err)
	}
	for _, o := range results.Objects {
		if o.Status != StatusReady || o.Error != nil || o.FullReadCompletedAt == nil || o.FinishedCheckAt == nil || *o.ReceivedBytes != 65552 {
			t.Fatalf("%+v", o)
		}
	}
	if len(f.requests) != 9 {
		t.Fatalf("requests=%v", f.requests)
	}
	for _, count := range f.probes {
		if count != 2 {
			t.Fatalf("probe count %d", count)
		}
	}
	before, err := os.ReadFile(filepath.Join(f.spec.Out, "target-preparation.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), f.spec); contract.ErrorCode(err) != contract.DataConflict {
		t.Fatalf("must reject overwrite: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(f.spec.Out, "target-preparation.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("changed existing result")
	}
}

func TestCannotPublishReadyForInvalidEvidence(t *testing.T) {
	cases := map[string]string{"plain-http": StatusUnverified, "wrong-ip": StatusFailed, "wrong-body": StatusFailed, "truncated": StatusFailed, "redirect": StatusFailed, "changed-task": StatusFailed, "duplicate": StatusFailed, "missing-marker": StatusUnverified, "false-marker": StatusUnverified, "collected-before-final": StatusUnverified, "wrong-content-range": StatusFailed, "wrong-length": StatusFailed, "probe-200": StatusFailed, "wrong-probe-bytes": StatusFailed, "compressed": StatusFailed, "duplicate-header": StatusUnverified}
	for mode, want := range cases {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, mode, 2)
			r, err := Prepare(context.Background(), f.spec)
			if err == nil || r.Status != want || len(r.Errors) == 0 {
				t.Fatalf("got status %s want %s: %+v %v", r.Status, want, r, err)
			}
			data, readErr := os.ReadFile(filepath.Join(f.spec.Out, "target-preparation.json"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			var saved Result
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Status == StatusReady {
				t.Fatal("published false ready")
			}
			if mode == "collected-before-final" && len(f.requests) != 5 {
				t.Fatalf("final failure must not retry: %v", f.requests)
			}
			if mode == "wrong-body" && len(f.requests) != 1 {
				t.Fatalf("dispatch continued after failure: %v", f.requests)
			}
		})
	}
}

func TestTimeoutCancellationAndNewPreparation(t *testing.T) {
	f := newFixture(t, "timeout", 2)
	data, err := os.ReadFile(f.spec.TargetConfig)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("prepare:\n  request_timeout_ms: 20\n  ready_timeout_ms: 20\n")...)
	if err := os.WriteFile(f.spec.TargetConfig, data, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Prepare(context.Background(), f.spec)
	if err == nil || r.Status != StatusFailed {
		t.Fatalf("%+v %v", r, err)
	}
	probeFixture := newFixture(t, "probe-timeout", 1)
	probeConfig, readErr := os.ReadFile(probeFixture.spec.TargetConfig)
	if readErr != nil {
		t.Fatal(readErr)
	}
	probeConfig = append(probeConfig, []byte("prepare:\n  ready_timeout_ms: 20\n")...)
	if err := os.WriteFile(probeFixture.spec.TargetConfig, probeConfig, 0600); err != nil {
		t.Fatal(err)
	}
	probeResult, probeErr := Prepare(context.Background(), probeFixture.spec)
	if probeErr == nil || probeResult.Status != StatusUnverified {
		t.Fatalf("probe timeout must be unverified: %+v %v", probeResult, probeErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.spec.Out += "-cancelled"
	r2, err := Prepare(ctx, f.spec)
	if err == nil || r2.Status != StatusUnverified || r2.PreparationID == r.PreparationID {
		t.Fatalf("%+v %v", r2, err)
	}
}

func TestConfigDefaultsAndStrictInputs(t *testing.T) {
	f := newFixture(t, "ok", 1)
	c, id, err := ReadConfig(f.spec.TargetConfig)
	if err != nil || !contract.ValidDigest(id) || c.Prepare != (Options{1, 1800000, 60000}) {
		t.Fatalf("%+v %s %v", c, id, err)
	}
	original, err := os.ReadFile(f.spec.TargetConfig)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []string{
		string(original) + "schema_version: 1\n",
		strings.Replace(string(original), "schema_version: 1", "schema_version: \"1\"", 1),
		strings.Replace(string(original), "schema_version: 1", "schema_version: 1.0", 1),
		string(original) + "unknown: 1\n",
		string(original) + "prepare: null\n",
		string(original) + "prepare:\n  concurrency: 0\n",
		string(original) + "prepare:\n  ready_timeout_ms: 0\n",
		string(original) + "---\nschema_version: 1\n",
		strings.Replace(string(original), "192.0.2.10", "not-an-ip", 1),
	}
	for i, raw := range mutations {
		if err := os.WriteFile(f.spec.TargetConfig, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadConfig(f.spec.TargetConfig); err == nil {
			t.Fatalf("accepted invalid config %d", i)
		}
	}
}

func TestPreparationNeedsNoClientVersion(t *testing.T) {
	f := newFixture(t, "ok", 1)
	result, err := Prepare(context.Background(), f.spec)
	if err != nil || result.Status != StatusReady {
		t.Fatalf("preparation without client metadata: %+v %v", result, err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "client_version") {
		t.Fatal("emitted obsolete client metadata")
	}
}

func TestConcurrentRequestsAreBounded(t *testing.T) {
	f := newFixture(t, "concurrent", 5)
	data, err := os.ReadFile(f.spec.TargetConfig)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("prepare:\n  concurrency: 2\n")...)
	if err := os.WriteFile(f.spec.TargetConfig, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := Prepare(ctx, f.spec)
	if err != nil || r.Status != StatusReady {
		t.Fatalf("%+v %v", r, err)
	}
	if f.maxActive != 2 {
		t.Fatalf("observed maximum active requests %d, expected 2", f.maxActive)
	}
	if len(f.requests) != 15 {
		t.Fatalf("requests %d", len(f.requests))
	}
}
