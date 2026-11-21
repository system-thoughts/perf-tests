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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
)

const finishedHeader = "X-Dragonfly-Task-Download-Finished"

func TestPreparationReusesHTTPConnection(t *testing.T) {
	body := []byte{0x4a, 0x8d, 0x62, 0x96}
	requests := make(chan *http.Request, 2)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests <- req
		w.Header().Set(taskHeader, "object-task")
		w.Header().Set(serverHeader, "192.0.2.10")
		if req.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-3/4")
			w.WriteHeader(http.StatusPartialContent)
		}
		_, _ = w.Write(body)
	}))
	defer proxy.Close()
	config := Config{
		Source: Source{BaseURL: "http://object.test"}, Target: Target{Proxy: proxy.URL, ExpectedServerIP: "192.0.2.10"},
	}
	client, transport, err := newClient(config)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	object := contract.Object{ID: 0, SHA256: contract.Digest(body)}
	task, err := fullRead(context.Background(), client, config, object, 4, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := rangeCheck(context.Background(), client, config, object, 4, task, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	first, second := <-requests, <-requests
	for _, req := range []*http.Request{first, second} {
		if req.Close || req.Header.Get("Connection") != "" {
			t.Errorf("preparation requested connection closure: close=%v headers=%v", req.Close, req.Header)
		}
	}
	if first.RemoteAddr != second.RemoteAddr {
		t.Errorf("full GET and Range check used different connections: %s, %s", first.RemoteAddr, second.RemoteAddr)
	}
}

func response(status int, body []byte) *http.Response {
	resp := &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}
	resp.Header.Set(taskHeader, "object-task")
	resp.Header.Set(serverHeader, "192.0.2.10")
	return resp
}

func TestFullResponseIntegrity(t *testing.T) {
	// Independent SHA-256 vector for the three bytes "abc".
	object := contract.Object{ID: 0, SHA256: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}
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
			task, err := readFullResponse(ctx, resp, config, object, 3)
			if name == "valid" {
				if err != nil || task != "object-task" {
					t.Fatalf("task=%q err=%v", task, err)
				}
			} else if err == nil {
				t.Fatalf("invalid response completed: %q %v", task, err)
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
	object := contract.Object{ID: 0}
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
			err := readProbe(ctx, resp, config, object, 4, "object-task")
			if name == "valid" || name == "missing-marker" || name == "false-marker" || name == "ambiguous-marker" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatalf("invalid completion accepted: %v", err)
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
	for _, mode := range []string{"ready", "unrelated", "bad-second-object", "cancelled", "conflict"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			bodies := [][]byte{{0x4a, 0x8d, 0x62, 0x96}, {0xaa, 0x79, 0x30, 0xca}}
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				i := 0
				if strings.HasSuffix(req.URL.Path, contract.Digest(bodies[1])) {
					i = 1
				}
				w.Header().Set(taskHeader, fmt.Sprintf("task-%d", i))
				w.Header().Set(serverHeader, "192.0.2.10")
				w.Header().Set("Content-Length", "4")
				if req.Header.Get("Range") != "" {
					w.Header().Set("Content-Range", "bytes 0-3/4")
					w.WriteHeader(http.StatusPartialContent)
				}
				body := bodies[i]
				if mode == "bad-second-object" && i == 1 {
					body = []byte("xxxx")
				}
				_, _ = w.Write(body)
			}))
			defer proxy.Close()
			m := contract.Manifest{SchemaVersion: 1, Generator: contract.Generator{Version: contract.ContentAlgorithm, Seed: contract.ContentSeed, BlockBytes: 16}, ObjectSize: 4, ObjectCount: 2, Objects: []contract.Object{}}
			for i, body := range bodies {
				sha := contract.Digest(body)
				m.Objects = append(m.Objects, contract.Object{ID: uint64(i), SHA256: sha})
			}
			raw, _ := contract.Encode(m)
			manifest := filepath.Join(dir, "manifest.json")
			if err := os.WriteFile(manifest, raw, 0600); err != nil {
				t.Fatal(err)
			}
			config := Config{Source: Source{BaseURL: "http://objects.test"}, Target: Target{Proxy: proxy.URL, ExpectedServerIP: "192.0.2.10"}}
			out := filepath.Join(dir, "out")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			record := filepath.Join(out, "target-preparation.json")
			if mode == "unrelated" {
				if err := os.Mkdir(out, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(out, "notes"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "cancelled" {
				cancel()
			}
			if mode == "conflict" {
				if err := os.Mkdir(out, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(record, []byte("existing"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := Prepare(ctx, Spec{Manifest: manifest, Config: config, Out: out, Options: Options{Concurrency: 1, RequestTimeoutMS: 2000, ReadyTimeoutMS: 2000}})
			data, readErr := os.ReadFile(record)
			if mode == "ready" || mode == "unrelated" {
				if err != nil || readErr != nil || result.Status != StatusReady {
					t.Fatalf("%+v %v %v", result, err, readErr)
				}
				if _, err := os.Stat(filepath.Join(out, "target-object-results.json")); !os.IsNotExist(err) {
					t.Fatalf("unexpected object report: %v", err)
				}
			} else if mode == "conflict" {
				if err == nil || string(data) != "existing" {
					t.Fatalf("overwrote existing record: %q %v", data, err)
				}
			} else if err == nil || !os.IsNotExist(readErr) || result.Status == StatusReady {
				t.Fatalf("failed preparation published ready: %+v %v %v", result, err, readErr)
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
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				config := Config{Target: Target{ExpectedServerIP: "192.0.2.10"}}
				budget := 2 * time.Second
				if name == budgetName {
					budget = 20 * time.Millisecond
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
				started := time.Now()
				err := rangeCheck(ctx, client, config, contract.Object{ID: 0}, 4, "object-task", budget)
				success := name == reset || name == truncated
				if attempts != wantAttempts || (err == nil) != success {
					t.Fatalf("attempts=%d want=%d err=%v", attempts, wantAttempts, err)
				}
				if name == budgetName || name == cancelled || name == backoff {
					want := context.Canceled
					if name == budgetName {
						want = context.DeadlineExceeded
					}
					if !errors.Is(err, want) || attempts != 1 {
						t.Fatalf("budget/cancellation lost: attempts=%d err=%v", attempts, err)
					}
				}
				if (name == budgetName || name == backoff) && time.Since(started) >= 100*time.Millisecond {
					t.Fatal("cancellation did not interrupt the first 100ms backoff")
				}
			})
		})
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestTargetAddresses(t *testing.T) {
	for _, test := range []struct {
		proxy, ip string
		valid     bool
	}{
		{"http://seed.test:4001", "192.0.2.10", true},
		{"abc", "192.0.2.10", false},
		{"http://:4001", "192.0.2.10", false},
		{"http://seed.test:4001", "bad", false},
	} {
		if err := (Target{Proxy: test.proxy, ExpectedServerIP: test.ip}).Validate(); (err == nil) != test.valid {
			t.Fatalf("%+v: %v", test, err)
		}
	}
	for _, input := range []string{"", "https://source.example", "http://user:pass@source.example", "http://source.example?a=b", "http://source.example?", "http://source.example#", "http://source.example:0", "http://source.example:65536"} {
		if _, err := contract.NormalizeBaseURL(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	for input, want := range map[string]string{"http://LOCALHOST.": "http://localhost.", "http://x.localhost": "http://x.localhost", "http://127.0.0.1": "http://127.0.0.1", "http://[::1]": "http://[::1]", "http://[::ffff:127.0.0.1]": "http://[::ffff:127.0.0.1]", "http://SOURCE.example:80/base/": "http://source.example/base", "http://source.example": "http://source.example", "http://[2001:db8::1]:8080/": "http://[2001:db8::1]:8080", "http://source.example/base%2Fname//v/": "http://source.example/base%2Fname//v"} {
		got, err := contract.NormalizeBaseURL(input)
		if err != nil || got != want {
			t.Fatalf("%q: %q %v; want %s", input, got, err, want)
		}
	}
}
