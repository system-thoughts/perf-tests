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
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/contract"
)

func response(status int, body []byte) *http.Response {
	resp := &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}
	resp.Header.Set(taskHeader, "object-task")
	resp.Header.Set(serverHeader, "192.0.2.10")
	return resp
}

func TestFullResponseIntegrity(t *testing.T) {
	// Independent SHA-256 vector for the three bytes "abc".
	object := contract.Object{ID: 0, Size: 3, SHA256: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}
	config := Config{Target: Target{ExpectedServerIP: "192.0.2.10"}}
	for _, name := range []string{"valid", "status", "identity", "ambiguous-task", "encoding", "digest", "length", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			resp := response(http.StatusOK, []byte("abc"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "status":
				resp.StatusCode = http.StatusPartialContent
			case "identity":
				resp.Header.Set(serverHeader, "192.0.2.11")
			case "ambiguous-task":
				resp.Header.Add(taskHeader, "other-task")
			case "encoding":
				resp.Header.Set("Content-Encoding", "gzip")
			case "digest":
				resp.Body = io.NopCloser(strings.NewReader("xyz"))
			case "length":
				resp.Body = io.NopCloser(strings.NewReader("abcd"))
			case "cancelled":
				cancel()
			}
			var result ObjectResult
			err := readFullResponse(ctx, resp, config, object, &result)
			if name == "valid" {
				if err != nil || result.FullReadCompletedAt == nil || result.ReceivedBytes == nil || *result.ReceivedBytes != 3 || result.SHA256 == nil || *result.SHA256 != object.SHA256 {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if err == nil || result.FullReadCompletedAt != nil {
				t.Fatalf("invalid response completed: %+v %v", result, err)
			}
		})
	}
}

func TestFinishedResponseRequiresIdentityMarkerAndRange(t *testing.T) {
	// Known object-zero prefix; generation itself is tested in dataset.
	body, err := hex.DecodeString("4a8d6296")
	if err != nil {
		t.Fatal(err)
	}
	object := contract.Object{ID: 0, Size: 4}
	config := Config{Target: Target{ExpectedServerIP: "192.0.2.10"}}
	for _, name := range []string{"valid", "status", "task", "range", "length", "marker", "ambiguous-marker", "bytes"} {
		t.Run(name, func(t *testing.T) {
			resp := response(http.StatusPartialContent, body)
			resp.Header.Set("Content-Range", "bytes 0-3/4")
			resp.Header.Set(finishedHeader, headerTrue)
			switch name {
			case "status":
				resp.StatusCode = http.StatusOK
			case "task":
				resp.Header.Set(taskHeader, "other-task")
			case "range":
				resp.Header.Set("Content-Range", "bytes 1-4/4")
			case "length":
				resp.ContentLength = 3
			case "marker":
				resp.Header.Set(finishedHeader, headerFalse)
			case "ambiguous-marker":
				resp.Header.Add(finishedHeader, headerTrue)
			case "bytes":
				resp.Body = io.NopCloser(strings.NewReader("xxxx"))
			}
			task := "object-task"
			result := ObjectResult{TaskID: &task}
			err := readProbe(context.Background(), resp, config, object, &result)
			if name == "valid" {
				if err != nil || result.FinishedCheckAt == nil {
					t.Fatalf("%+v %v", result, err)
				}
			} else if err == nil || result.FinishedCheckAt != nil {
				t.Fatalf("invalid completion accepted: %+v %v", result, err)
			}
		})
	}
}

func TestReadinessRequiresEveryObjectAndActiveContext(t *testing.T) {
	for _, test := range []struct {
		name     string
		statuses []string
		cancel   bool
		want     string
	}{
		{"ready", []string{StatusReady, StatusReady}, false, StatusReady},
		{"unfinished", []string{StatusReady, StatusUnverified}, false, StatusUnverified},
		{"failed", []string{StatusReady, StatusFailed}, false, StatusFailed},
		{"failed-before-unfinished", []string{StatusFailed, StatusUnverified}, false, StatusFailed},
		{"cancelled", []string{StatusReady, StatusReady}, true, StatusUnverified},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.cancel {
				cancel()
			}
			objects := ObjectResults{}
			for id, status := range test.statuses {
				objects.Objects = append(objects.Objects, ObjectResult{ObjectID: uint64(id), Status: status})
			}
			result := NewResult()
			err := summarize(ctx, &objects, &result)
			if result.Status != test.want || (err == nil) != (test.want == StatusReady) || (len(result.Errors) == 0) != (test.want == StatusReady) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestConfigDefaultsAndBusinessConstraints(t *testing.T) {
	dir := t.TempDir()
	rules := []byte(`{"schema_version":1,"filtered_query_params":[],"rewrite_url":false,"use_tls":false}`)
	if err := os.WriteFile(filepath.Join(dir, "rules.json"), rules, 0600); err != nil {
		t.Fatal(err)
	}
	original := fmt.Sprintf("schema_version: 1\ntarget:\n  ref: seed\n  proxy: http://seed.test:4001\n  expected_server_ip: 192.0.2.10\n  instance_ref: test-instance\nproxy_rules_ref:\n  path: rules.json\n  sha256: %s\n", contract.Digest(rules))
	path := filepath.Join(dir, "target.yaml")
	for _, test := range []struct{ name, suffix string }{
		{"defaults", ""}, {"zero-concurrency", "prepare:\n  concurrency: 0\n"},
		{"zero-ready-timeout", "prepare:\n  ready_timeout_ms: 0\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(original+test.suffix), 0600); err != nil {
				t.Fatal(err)
			}
			config, identity, err := ReadConfig(path)
			if test.name == "defaults" {
				if err != nil || !contract.ValidDigest(identity) || config.Prepare.Concurrency == 0 || config.Prepare.RequestTimeoutMS == 0 || config.Prepare.ReadyTimeoutMS == 0 {
					t.Fatalf("%+v %s %v", config, identity, err)
				}
			} else if err == nil {
				t.Fatal("accepted invalid business constraint")
			}
		})
	}
}
