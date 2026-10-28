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
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
)

const finishedHeader = "X-Dragonfly-Task-Download-Finished"

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

func TestRangeResponseIntegrity(t *testing.T) {
	// Known object-zero prefix; generation itself is tested in dataset.
	body, err := hex.DecodeString("4a8d6296")
	if err != nil {
		t.Fatal(err)
	}
	object := contract.Object{ID: 0, Size: 4}
	config := Config{Target: Target{ExpectedServerIP: "192.0.2.10"}}
	for _, name := range []string{"valid", "missing-marker", "false-marker", "ambiguous-marker", "status", "task", "range", "length", "bytes", "bytes-without-marker", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resp := response(http.StatusPartialContent, body)
			resp.Header.Set("Content-Range", "bytes 0-3/4")
			resp.Header.Set(finishedHeader, headerTrue)
			switch name {
			case "cancelled":
				cancel()
			case "status":
				resp.StatusCode = http.StatusOK
			case "task":
				resp.Header.Set(taskHeader, "other-task")
			case "range":
				resp.Header.Set("Content-Range", "bytes 1-4/4")
			case "length":
				resp.ContentLength = 3
			case "missing-marker":
				resp.Header.Del(finishedHeader)
			case "false-marker":
				resp.Header.Set(finishedHeader, headerFalse)
			case "ambiguous-marker":
				resp.Header.Add(finishedHeader, headerTrue)
			case "bytes-without-marker":
				resp.Header.Del(finishedHeader)
				fallthrough
			case "bytes":
				resp.Body = io.NopCloser(strings.NewReader("xxxx"))
			}
			task := "object-task"
			result := ObjectResult{TaskID: &task}
			err := readProbe(ctx, resp, config, object, &result)
			if name == "valid" || name == "missing-marker" || name == "false-marker" || name == "ambiguous-marker" {
				if err != nil || result.FinishedCheckAt == nil {
					t.Fatalf("%+v %v", result, err)
				}
			} else if err == nil || result.FinishedCheckAt != nil {
				t.Fatalf("invalid completion accepted: %+v %v", result, err)
			}
			if name == "bytes-without-marker" && contract.ErrorCode(err) != contract.PrepareFailed {
				t.Fatalf("body mismatch must remain a data failure, got %v", err)
			}
			if name == "cancelled" && contract.ErrorCode(err) != Unverified {
				t.Fatalf("interrupted verification is not a content failure: %v", err)
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRangeTransportRecovery(t *testing.T) {
	const (
		budgetName = "budget"
		truncated  = "body-truncated-once"
		backoff    = "cancel-backoff"
		reset      = "reset-once"
		persistent = "persistent-reset"
		cancelled  = "cancelled"
	)
	for _, name := range []string{reset, truncated, persistent, budgetName, cancelled, backoff, "status", "identity", "range", "bytes", "unsupported", "permanent-transport"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			config := Config{Target: Target{ExpectedServerIP: "192.0.2.10"}, Prepare: Options{ReadyTimeoutMS: 2000}}
			if name == budgetName {
				config.Prepare.ReadyTimeoutMS = 20
			}
			attempts, wantAttempts := 0, 1
			switch name {
			case reset, truncated:
				wantAttempts = 2
			case persistent:
				wantAttempts = 3
			}
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				if req.Header.Get("Range") != "bytes=0-3" {
					t.Fatalf("recovery changed the request: %v", req.Header)
				}
				if name == cancelled {
					cancel()
				}
				if name == backoff {
					time.AfterFunc(10*time.Millisecond, cancel)
				}
				if name == persistent || name == budgetName || name == cancelled || name == backoff || (name == reset && attempts == 1) {
					return nil, syscall.ECONNRESET
				}
				if name == "permanent-transport" {
					return nil, errors.New("unsupported transport configuration")
				}
				resp := response(http.StatusPartialContent, []byte{0x4a, 0x8d, 0x62, 0x96})
				resp.Header.Set("Content-Range", "bytes 0-3/4")
				switch name {
				case truncated:
					if attempts == 1 {
						resp.Body = io.NopCloser(errorReader{})
					}
				case "status":
					resp.StatusCode = http.StatusServiceUnavailable
				case "identity":
					resp.Header.Set(taskHeader, "another-task")
				case "range":
					resp.Header.Set("Content-Range", "bytes 1-4/4")
				case "bytes":
					resp.Body = io.NopCloser(strings.NewReader("xxxx"))
				case "unsupported":
					resp.Header.Del(taskHeader)
				}
				return resp, nil
			})}
			task := "object-task"
			result := ObjectResult{TaskID: &task}
			started := time.Now()
			err := rangeCheck(ctx, client, config, contract.Object{ID: 0, Size: 4, URL: "http://object.test/0"}, &result, "range_check")
			success := name == reset || name == truncated
			if attempts != wantAttempts || (err == nil) != success || (result.FinishedCheckAt != nil) != success {
				t.Fatalf("attempts=%d want=%d result=%+v err=%v", attempts, wantAttempts, result, err)
			}
			if (name == budgetName || name == cancelled || name == backoff) && time.Since(started) > 500*time.Millisecond {
				t.Fatal("cancellation or shared budget did not interrupt retry backoff")
			}
		})
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestConfigDefaultsAndBusinessConstraints(t *testing.T) {
	dir := t.TempDir()
	original := "schema_version: 1\ntarget:\n  proxy: http://seed.test:4001\n  expected_server_ip: 192.0.2.10\n"
	path := filepath.Join(dir, "target.yaml")
	for _, test := range []struct{ name, suffix string }{
		{"defaults", ""}, {"custom", "prepare:\n  concurrency: 2\n  request_timeout_ms: 1000\n  ready_timeout_ms: 200\n"}, {"old-rules", "proxy_rules_ref: {}\n"}, {"zero-concurrency", "prepare:\n  concurrency: 0\n"},
		{"zero-ready-timeout", "prepare:\n  ready_timeout_ms: 0\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(original+test.suffix), 0600); err != nil {
				t.Fatal(err)
			}
			config, err := ReadConfig(path)
			if test.name == "defaults" || test.name == "custom" {
				if err != nil || config.Prepare.Concurrency == 0 || config.Prepare.RequestTimeoutMS == 0 || config.Prepare.ReadyTimeoutMS == 0 {
					t.Fatalf("%+v %v", config, err)
				}
			} else if err == nil {
				t.Fatal("accepted invalid business constraint")
			}
		})
	}
}

func TestRemovedInstanceConfigurationRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target.yaml")
	raw := "schema_version: 1\ntarget:\n  proxy: http://seed.test:4001\n  expected_server_ip: 192.0.2.10\n  instance_ref: old\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfig(path); err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "instance_ref") {
		t.Fatalf("missing field/path diagnostic: %v", err)
	}
}
