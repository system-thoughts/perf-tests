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
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
	"gopkg.in/yaml.v3"
)

const (
	StatusReady = "ready"
	headerFalse = "false"
	headerTrue  = "true"
	Unsupported = "TARGET_UNSUPPORTED"
	Unverified  = "TARGET_UNVERIFIED"
)

type Target struct {
	Ref              string `json:"ref,omitempty"`
	Proxy            string `json:"proxy"`
	ExpectedServerIP string `json:"expected_server_ip"`
}

type Options struct {
	Concurrency      uint64 `json:"concurrency"`
	RequestTimeoutMS uint64 `json:"request_timeout_ms"`
	ReadyTimeoutMS   uint64 `json:"ready_timeout_ms"`
}

type Source struct {
	BaseURL string `json:"base_url"`
}

type Config struct {
	SchemaVersion int    `json:"schema_version"`
	Target        Target `json:"target"`
	Source        Source `json:"source"`
}

// ReadConfig validates and fingerprints the same file bytes.
func ReadConfig(name string) (c Config, digest string, err error) {
	defer func() {
		if err != nil {
			err = contract.Input("TARGET_CONFIG", name, err)
		}
	}()
	raw, err := os.ReadFile(name)
	if err != nil {
		return c, "", contract.Tag(contract.InputInvalid, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var node yaml.Node
	if err := decoder.Decode(&node); err != nil {
		return c, "", contract.Tag(contract.ContractMismatch, err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return c, "", contract.Tag(contract.ContractMismatch, fmt.Errorf("expected one YAML document"))
	}
	value, err := yamlValue(&node)
	if err != nil {
		return c, "", contract.Tag(contract.ContractMismatch, err)
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return c, "", contract.Tag(contract.ContractMismatch, fmt.Errorf("target config must be a mapping"))
	}
	data, err := json.Marshal(fields)
	if err == nil {
		err = contract.Decode(data, &c)
	}
	if err == nil {
		err = validateConfig(c)
	}
	if err != nil {
		return c, "", contract.Tag(contract.ContractMismatch, err)
	}
	c.Source.BaseURL, err = contract.NormalizeBaseURL(c.Source.BaseURL)
	if err != nil {
		return c, "", contract.Tag(contract.ContractMismatch, fmt.Errorf("source.base_url: %w", err))
	}
	return c, contract.Digest(raw), nil
}

func validateConfig(c Config) error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("schema_version: expected 1, actual %d", c.SchemaVersion)
	}
	return c.Target.Validate()
}

func (t Target) Validate() error {
	u, err := url.Parse(t.Proxy)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(t.Proxy, "#") {
		return fmt.Errorf("target.proxy: expected an HTTP proxy origin without credentials or path/query/fragment; actual %q", t.Proxy)
	}
	if u.Port() != "" {
		if n, err := strconv.ParseUint(u.Port(), 10, 16); err != nil || n == 0 {
			return fmt.Errorf("target.proxy: invalid port in %q", t.Proxy)
		}
	}
	if net.ParseIP(t.ExpectedServerIP) == nil {
		return fmt.Errorf("target.expected_server_ip: expected an IP address, actual %q", t.ExpectedServerIP)
	}
	return nil
}

func (o Options) validate() error {
	if o.Concurrency == 0 || o.Concurrency > uint64(^uint(0)>>1) || o.Concurrency > contract.MaxSafeInteger {
		return fmt.Errorf("prepare.concurrency: expected a positive JavaScript-safe native integer, actual %d", o.Concurrency)
	}
	for i, ms := range []uint64{o.RequestTimeoutMS, o.ReadyTimeoutMS} {
		if ms == 0 || ms > uint64((1<<63-1)/int64(time.Millisecond)) {
			return fmt.Errorf("prepare.%s: expected positive representable milliseconds, actual %d", []string{"request_timeout_ms", "ready_timeout_ms"}[i], ms)
		}
	}
	return nil
}

// Preserve scalar types and reject ambiguous YAML before applying the shared
// strict JSON contract checks. YAML aliases and implicit nulls are not inputs.
func yamlValue(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) != 1 {
			return nil, fmt.Errorf("empty YAML document")
		}
		return yamlValue(n.Content[0])
	case yaml.MappingNode:
		result := map[string]any{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Tag != "!!str" {
				return nil, fmt.Errorf("mapping keys must be strings")
			}
			if _, exists := result[key.Value]; exists {
				return nil, fmt.Errorf("duplicate YAML key %q", key.Value)
			}
			value, err := yamlValue(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			result[key.Value] = value
		}
		return result, nil
	case yaml.SequenceNode:
		result := []any{}
		for _, child := range n.Content {
			value, err := yamlValue(child)
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		return result, nil
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!str":
			return n.Value, nil
		case "!!bool":
			if n.Value == "true" {
				return true, nil
			}
			if n.Value == "false" {
				return false, nil
			}
		case "!!int":
			for _, c := range n.Value {
				if c < '0' || c > '9' {
					return nil, fmt.Errorf("require decimal nonnegative integers")
				}
			}
			value, err := strconv.ParseUint(n.Value, 10, 64)
			return value, err
		}
	}
	return nil, fmt.Errorf("unsupported YAML value at line %d", n.Line)
}
