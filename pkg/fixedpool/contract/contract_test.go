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

import (
	"encoding/json"
	"strings"
	"testing"
)

func validManifest() Manifest {
	sha := "66895a8785a1dee4951313348a79a5ee9def315d6728a9d303c8b6fe74a0fb13"
	return Manifest{SchemaVersion: 1, Generator: Generator{Version: ContentAlgorithm, Seed: ContentSeed, BlockBytes: 16}, ObjectSize: 64, ObjectCount: 1, BaseURL: "http://source.example", Objects: []Object{{ID: 0, Size: 64, SHA256: sha, RelativePath: "objects/sha256/" + sha, URL: "http://source.example/objects/sha256/" + sha}}}
}

func TestStrictManifest(t *testing.T) {
	data, err := Encode(validManifest())
	if err != nil {
		t.Fatal(err)
	}
	valid := string(data)
	cases := map[string]string{
		"unknown root":     strings.Replace(valid, `{"schema_version":1`, `{"extra":0,"schema_version":1`, 1),
		"unknown nested":   strings.Replace(valid, `"block_bytes":16`, `"extra":0,"block_bytes":16`, 1),
		"duplicate":        strings.Replace(valid, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		"duplicate nested": strings.Replace(valid, `"object_id":0`, `"object_id":0,"object_id":0`, 1),
		"missing zero ID":  strings.Replace(valid, `"object_id":0,`, "", 1),
		"missing block":    strings.Replace(valid, `,"block_bytes":16`, "", 1),
		"wrong case":       strings.Replace(valid, `"schema_version"`, `"SCHEMA_VERSION"`, 1),
		"case alias root":  strings.Replace(valid, `"schema_version":1`, `"schema_version":1,"SCHEMA_VERSION":1`, 1),
		"case alias inner": strings.Replace(valid, `"block_bytes":16`, `"block_bytes":16,"BLOCK_BYTES":16`, 1),
		"null":             strings.Replace(valid, `"object_id":0`, `"object_id":null`, 1),
		"float":            strings.Replace(valid, `"object_size_bytes":64`, `"object_size_bytes":64.0`, 1),
		"exponent":         strings.Replace(valid, `"object_size_bytes":64`, `"object_size_bytes":6.4e1`, 1),
		"negative":         strings.Replace(valid, `"object_count":1`, `"object_count":-1`, 1),
		"string number":    strings.Replace(valid, `"object_count":1`, `"object_count":"1"`, 1),
		"schema":           strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1),
		"seed":             strings.Replace(valid, ContentSeed, "other-seed", 1),
		"path traversal":   strings.Replace(valid, `"relative_path":"objects/sha256/`, `"relative_path":"../objects/sha256/`, 1),
		"second value":     valid + "{}",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			var m Manifest
			err := Decode([]byte(input), &m)
			if err == nil {
				err = ValidateManifest(m)
			}
			if err == nil {
				t.Fatalf("accepted invalid manifest: %s", input)
			}
		})
	}
	var got Manifest
	if err := Decode(data, &got); err != nil {
		t.Fatal(err)
	}
	if err := ValidateManifest(got); err != nil {
		t.Fatal(err)
	}
	encoded, err := Encode(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != valid || !strings.HasSuffix(valid, "\n") || strings.HasSuffix(valid, "\n\n") {
		t.Fatal("encoding is not deterministic with one LF")
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"range_bytes", "worker_count", "rate", "run_id"} {
		if _, ok := raw[forbidden]; ok {
			t.Fatal("manifest depends on run input", forbidden)
		}
	}
}

func TestNullableRecordsRemainStrict(t *testing.T) {
	// Explicit null means an unknown observation, not a missing required field.
	type record struct {
		Code    *int           `json:"exit_code"`
		State   string         `json:"state"`
		Native  map[string]any `json:"native"`
		Message string         `json:"message,omitempty"`
	}
	valid := `{"exit_code":null,"state":"unknown","native":{"metric":null}}`
	var got record
	if err := DecodeRecord([]byte(valid), &got); err != nil || got.Code != nil || got.State != "unknown" {
		t.Fatalf("%+v: %v", got, err)
	}
	for _, raw := range []string{
		`{"state":"unknown","native":{}}`,
		`{"exit_code":null,"state":null,"native":{}}`,
		`{"exit_code":null,"state":"unknown","native":null}`,
		`{"exit_code":null,"state":"unknown","native":{},"extra":1}`,
		`{"exit_code":null,"state":"unknown","STATE":"unknown","native":{}}`,
		`{"exit_code":null,"exit_code":0,"state":"unknown","native":{}}`,
		`{"exit_code":null,"state":"unknown","native":{"x":1,"x":2}}`,
		valid + `{}`,
	} {
		if err := DecodeRecord([]byte(raw), &got); err == nil {
			t.Fatalf("accepted ambiguous/incomplete record: %s", raw)
		}
	}
}

func TestSourceURL(t *testing.T) {
	for _, input := range []string{"", "https://source.example", "http://user:pass@source.example", "http://source.example?a=b", "http://source.example?", "http://source.example#", "http://source.example:0", "http://source.example:65536"} {
		if _, err := NormalizeBaseURL(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	for input, want := range map[string]string{"http://LOCALHOST.": "http://localhost.", "http://x.localhost": "http://x.localhost", "http://127.0.0.1": "http://127.0.0.1", "http://[::1]": "http://[::1]", "http://[::ffff:127.0.0.1]": "http://[::ffff:127.0.0.1]", "http://SOURCE.example:80/base/": "http://source.example/base", "http://source.example": "http://source.example", "http://[2001:db8::1]:8080/": "http://[2001:db8::1]:8080", "http://source.example/base%2Fname//v/": "http://source.example/base%2Fname//v"} {
		got, err := NormalizeBaseURL(input)
		if err != nil || got != want {
			t.Errorf("normalize %q = %q, %v; want %q", input, got, err, want)
		}
	}
}

func TestSingleWriterAndSpace(t *testing.T) {
	directory := t.TempDir()
	first, err := AcquireLock(directory, ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if second, err := AcquireLock(directory, ".lock"); err == nil {
		if err := second.Close(); err != nil {
			t.Fatal(err)
		}
		t.Fatal("concurrent writer acquired lock")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := AcquireLock(directory, ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RequireSpace(directory, ^uint64(0)); err == nil {
		t.Fatal("accepted impossible space request")
	}
}
