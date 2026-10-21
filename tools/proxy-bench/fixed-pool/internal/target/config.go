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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/contract"
	"gopkg.in/yaml.v3"
)

const (
	StatusReady      = "ready"
	StatusFailed     = "failed"
	StatusUnverified = "unverified"
	headerFalse      = "false"
	headerTrue       = "true"
	Method           = "http-full-body-and-finished-v1"
	Unsupported      = "TARGET_UNSUPPORTED"
	Unverified       = "TARGET_UNVERIFIED"
)

type Target struct {
	Ref              string `json:"ref"`
	Proxy            string `json:"proxy"`
	ExpectedServerIP string `json:"expected_server_ip"`
	InstanceRef      string `json:"instance_ref"`
}

type Options struct {
	Concurrency      uint64 `json:"concurrency"`
	RequestTimeoutMS uint64 `json:"request_timeout_ms"`
	ReadyTimeoutMS   uint64 `json:"ready_timeout_ms"`
}

type Config struct {
	SchemaVersion int              `json:"schema_version"`
	Target        Target           `json:"target"`
	ProxyRulesRef contract.FileRef `json:"proxy_rules_ref"`
	Prepare       Options          `json:"prepare"`
}

type ProxyRules struct {
	SchemaVersion       int      `json:"schema_version"`
	FilteredQueryParams []string `json:"filtered_query_params"`
	RewriteURL          bool     `json:"rewrite_url"`
	UseTLS              bool     `json:"use_tls"`
}

func ReadConfig(name string) (Config, string, error) {
	var c Config
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
	defaults := map[string]any{"concurrency": uint64(1), "request_timeout_ms": uint64(1800000), "ready_timeout_ms": uint64(60000)}
	if opts, exists := fields["prepare"]; exists {
		options, ok := opts.(map[string]any)
		if !ok {
			return c, "", contract.Tag(contract.ContractMismatch, fmt.Errorf("prepare must be a mapping"))
		}
		for key, value := range defaults {
			if _, exists := options[key]; !exists {
				options[key] = value
			}
		}
	} else {
		fields["prepare"] = defaults
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
	rulesPath := c.ProxyRulesRef.Path
	if !filepath.IsAbs(rulesPath) {
		rulesPath = filepath.Join(filepath.Dir(name), rulesPath)
	}
	raw, err = os.ReadFile(rulesPath)
	if err != nil {
		return c, "", contract.Tag(contract.InputInvalid, err)
	}
	if contract.Digest(raw) != c.ProxyRulesRef.SHA256 {
		return c, "", contract.Tag(contract.ContractMismatch, fmt.Errorf("proxy rules SHA-256 mismatch"))
	}
	var rules ProxyRules
	if err := contract.Decode(raw, &rules); err != nil {
		return c, "", contract.Tag(contract.ContractMismatch, err)
	}
	if rules.SchemaVersion != 1 || rules.RewriteURL || rules.UseTLS {
		return c, "", contract.Tag(Unsupported, fmt.Errorf("require proxy rules schema 1, rewrite_url=false and use_tls=false"))
	}
	seen := map[string]bool{}
	for _, key := range rules.FilteredQueryParams {
		if strings.TrimSpace(key) == "" || seen[key] {
			return c, "", contract.Tag(contract.ContractMismatch, fmt.Errorf("invalid filtered_query_params"))
		}
		seen[key] = true
	}
	identity, err := json.Marshal(struct {
		Mode             string `json:"mode"`
		TaskType         string `json:"task_type"`
		PieceStrategy    string `json:"piece_strategy"`
		ProxyRulesSHA256 string `json:"proxy_rules_sha256"`
	}{"url-v1", "standard", "auto", c.ProxyRulesRef.SHA256})
	if err != nil {
		return c, "", err
	}
	return c, contract.Digest(identity), nil
}

func validateConfig(c Config) error {
	u, err := url.Parse(c.Target.Proxy)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(c.Target.Proxy, "#") {
		return fmt.Errorf("target.proxy must be an HTTP proxy origin without credentials or path/query/fragment")
	}
	if u.Port() != "" {
		if n, err := strconv.ParseUint(u.Port(), 10, 16); err != nil || n == 0 {
			return fmt.Errorf("invalid proxy port")
		}
	}
	if c.SchemaVersion != 1 || c.Target.Ref == "" || c.Target.InstanceRef == "" || net.ParseIP(c.Target.ExpectedServerIP) == nil || c.ProxyRulesRef.Path == "" || !contract.ValidDigest(c.ProxyRulesRef.SHA256) {
		return fmt.Errorf("invalid schema, target identity/IP or proxy rules reference")
	}
	if c.Prepare.Concurrency == 0 || c.Prepare.Concurrency > uint64(^uint(0)>>1) || c.Prepare.Concurrency > contract.MaxSafeInteger {
		return fmt.Errorf("prepare.concurrency must be a positive native integer")
	}
	for _, ms := range []uint64{c.Prepare.RequestTimeoutMS, c.Prepare.ReadyTimeoutMS} {
		if ms == 0 || ms > uint64((1<<63-1)/int64(time.Millisecond)) {
			return fmt.Errorf("prepare timeouts must be positive representable milliseconds")
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
