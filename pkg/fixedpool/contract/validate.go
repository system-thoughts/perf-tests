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
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func NormalizeBaseURL(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("base_url: %w", err)
	}
	if u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(value, "#") {
		return "", fmt.Errorf("base_url must be HTTP without credentials, query or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("base_url must have a host")
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return "", fmt.Errorf("base_url has invalid port")
		}
		port = strconv.FormatUint(n, 10)
		if port == "80" {
			port = ""
		}
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	// Preserve escaped separators and repeated slashes: origin servers may give
	// them different meanings. Only the literal trailing join separator is removed.
	u.RawPath = strings.TrimRight(u.EscapedPath(), "/")
	u.Path, err = url.PathUnescape(u.RawPath)
	if err != nil {
		return "", fmt.Errorf("base_url path: %w", err)
	}
	return u.String(), nil
}

func ValidateManifest(m Manifest) error {
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("manifest.schema_version: expected %d, actual %d", SchemaVersion, m.SchemaVersion)
	}
	if m.Generator != (Generator{Version: ContentAlgorithm, Seed: ContentSeed, BlockBytes: BlockBytes}) {
		return fmt.Errorf("manifest.generator: expected %+v, actual %+v", Generator{Version: ContentAlgorithm, Seed: ContentSeed, BlockBytes: BlockBytes}, m.Generator)
	}
	if m.ObjectSize == 0 || m.ObjectSize > MaxSafeInteger {
		return fmt.Errorf("manifest.object_size_bytes: expected 1..%d, actual %d", MaxSafeInteger, m.ObjectSize)
	}
	if m.ObjectCount == 0 || m.ObjectCount > MaxSafeInteger || m.ObjectCount != uint64(len(m.Objects)) {
		return fmt.Errorf("manifest.object_count: expected a positive count <= %d equal to len(objects)=%d, actual %d", MaxSafeInteger, len(m.Objects), m.ObjectCount)
	}
	seen := make(map[string]bool, len(m.Objects))
	for i, object := range m.Objects {
		switch {
		case object.ID != uint64(i):
			return fmt.Errorf("manifest.objects[%d].object_id: expected %d, actual %d", i, i, object.ID)
		case !ValidDigest(object.SHA256):
			return fmt.Errorf("manifest.objects[%d].sha256: expected 64 lowercase hex digits, actual %q", i, object.SHA256)
		}
		if seen[object.SHA256] {
			return fmt.Errorf("manifest.objects[%d].sha256: duplicate %q", i, object.SHA256)
		}
		seen[object.SHA256] = true
	}
	return nil
}
