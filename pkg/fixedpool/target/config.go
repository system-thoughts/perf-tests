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
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
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
	Target Target
	Source Source
}

func (t Target) Validate() error {
	u, err := url.Parse(t.Proxy)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(t.Proxy, "#") {
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
