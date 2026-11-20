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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"syscall"
	"time"

	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/contract"
	"github.com/dragonflyoss/perf-tests/pkg/fixedpool/dataset"
)

const taskHeader = "X-Dragonfly-Task-ID"
const serverHeader = "X-Dragonfly-Server-IP"

// FixedHeaders is shared with later execution input generation; explicit proxy
// selection and a fixed identity prevent environment-dependent bypasses.
func FixedHeaders() http.Header {
	return http.Header{
		"X-Dragonfly-Use-P2p":                          []string{headerTrue},
		"X-Dragonfly-Enable-Task-Id-Based-Blob-Digest": []string{headerFalse},
		"X-Dragonfly-Prefetch":                         []string{headerFalse},
		"Accept-Encoding":                              []string{"identity"},
	}
}

func newClient(c Config) (*http.Client, *http.Transport, error) {
	proxy, err := url.Parse(c.Target.Proxy)
	if err != nil {
		return nil, nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxy)
	transport.DisableCompression = true
	// Temporary workaround for Seed caching Connection: close in task metadata.
	// Revert after the Dragonfly Client hop-by-hop header fix is deployed.
	transport.DisableKeepAlives = false
	client := &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return client, transport, nil
}

func request(ctx context.Context, client *http.Client, url string, size uint64, rangeRequest bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header = FixedHeaders()
	if rangeRequest {
		req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", min(uint64(65536), size)-1))
	}
	return client.Do(req)
}

func responseIdentity(resp *http.Response, c Config) (string, string, error) {
	task, server := resp.Header.Get(taskHeader), resp.Header.Get(serverHeader)
	if len(resp.Header.Values(taskHeader)) != 1 || task == "" || len(resp.Header.Values(serverHeader)) != 1 || net.ParseIP(server) == nil {
		return task, server, contract.Tag(Unsupported, fmt.Errorf("HTTP %d: missing/ambiguous Dragonfly task or server IP headers", resp.StatusCode))
	}
	if !net.ParseIP(server).Equal(net.ParseIP(c.Target.ExpectedServerIP)) {
		return task, server, contract.Tag(contract.PrepareFailed, fmt.Errorf("HTTP %d: server IP %q differs from expected %q", resp.StatusCode, server, c.Target.ExpectedServerIP))
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return task, server, contract.Tag(contract.PrepareFailed, fmt.Errorf("HTTP %d: unexpected Content-Encoding %q", resp.StatusCode, encoding))
	}
	return task, server, nil
}

func fullRead(ctx context.Context, client *http.Client, c Config, o contract.Object, size uint64, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := request(ctx, client, c.Source.BaseURL+"/objects/sha256/"+o.SHA256, size, false)
	if err != nil {
		return "", contract.Tag(contract.PrepareFailed, fmt.Errorf("full GET: %w", err))
	}
	task, err := readFullResponse(ctx, resp, c, o, size)
	closeErr := resp.Body.Close()
	if err == nil {
		err = closeErr
	}
	return task, err
}

func readFullResponse(ctx context.Context, resp *http.Response, c Config, o contract.Object, size uint64) (string, error) {
	if resp.StatusCode != http.StatusOK {
		return "", contract.Tag(contract.PrepareFailed, fmt.Errorf("full GET requires HTTP 200, got %d", resp.StatusCode))
	}
	task, _, err := responseIdentity(resp, c)
	if err != nil {
		return task, err
	}
	hash := sha256.New()
	n, err := io.CopyBuffer(hash, io.LimitReader(resp.Body, int64(size)+1), make([]byte, 64*1024))
	if err != nil {
		return task, contract.Tag(contract.PrepareFailed, fmt.Errorf("HTTP %d full GET after %d bytes: %w", resp.StatusCode, n, err))
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if uint64(n) != size || digest != o.SHA256 {
		return task, contract.Tag(contract.PrepareFailed, fmt.Errorf("HTTP %d full GET body mismatch: received %d, expected %d bytes; SHA-256 expected %s, actual %s", resp.StatusCode, n, size, o.SHA256, digest))
	}
	return task, ctx.Err()
}

func rangeCheck(ctx context.Context, client *http.Client, c Config, o contract.Object, size uint64, task string, timeout time.Duration) error {
	// Attempts, reads and backoffs share one budget.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	delays := [...]time.Duration{100 * time.Millisecond, 500 * time.Millisecond}
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return contract.Tag(Unverified, err)
		}
		err := rangeCheckOnce(ctx, client, c, o, size, task)
		if err == nil {
			return nil
		}
		log.Printf("range_check object_id=%d task_id=%s attempt=%d/3 failed: %v", o.ID, task, attempt, err)
		if ctx.Err() != nil {
			return contract.Tag(Unverified, fmt.Errorf("range check stopped after %d attempts (%v): %w", attempt, err, ctx.Err()))
		}
		if attempt > len(delays) || !retryableRangeError(err) {
			return fmt.Errorf("range check stopped after %d attempts: %w", attempt, err)
		}
		timer := time.NewTimer(delays[attempt-1])
		select {
		case <-ctx.Done():
			timer.Stop()
			return contract.Tag(Unverified, fmt.Errorf("range check stopped after %d attempts (%v): %w", attempt, err, ctx.Err()))
		case <-timer.C:
		}
	}
}

// Outcome categories are not retry policies. Only an unverified transport
// failure is recoverable; status, identity and data errors stay terminal.
func retryableRangeError(err error) bool {
	if contract.ErrorCode(err) != Unverified {
		return false
	}
	var networkError net.Error
	return (errors.As(err, &networkError) && networkError.Timeout()) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE)
}

func rangeCheckOnce(ctx context.Context, client *http.Client, c Config, o contract.Object, size uint64, task string) error {
	resp, err := request(ctx, client, c.Source.BaseURL+"/objects/sha256/"+o.SHA256, size, true)
	if err != nil {
		return contract.Tag(Unverified, fmt.Errorf("range check: %w", err))
	}
	err = readProbe(ctx, resp, c, o, size, task)
	closeErr := resp.Body.Close()
	if err == nil {
		err = contract.Tag(Unverified, closeErr)
	}
	return err
}

func readProbe(ctx context.Context, resp *http.Response, c Config, o contract.Object, objectSize uint64, expectedTask string) error {
	if resp.StatusCode != http.StatusPartialContent {
		return contract.Tag(contract.PrepareFailed, fmt.Errorf("range check requires HTTP 206, got %d", resp.StatusCode))
	}
	task, _, err := responseIdentity(resp, c)
	if err != nil {
		return err
	}
	if task != expectedTask {
		return contract.Tag(contract.PrepareFailed, fmt.Errorf("HTTP %d: task identity changed during range check", resp.StatusCode))
	}
	size := min(uint64(65536), objectSize)
	want := "bytes 0-" + strconv.FormatUint(size-1, 10) + "/" + strconv.FormatUint(objectSize, 10)
	if len(resp.Header.Values("Content-Range")) != 1 || resp.Header.Get("Content-Range") != want || resp.ContentLength != int64(size) {
		return contract.Tag(contract.PrepareFailed, fmt.Errorf("HTTP %d range check Content-Range/length mismatch: %q, %d", resp.StatusCode, resp.Header.Get("Content-Range"), resp.ContentLength))
	}
	// Readiness is established by the full object and Range bytes, not the
	// task's asynchronous completion state sampled before this response.
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(size)+1))
	if err != nil {
		return contract.Tag(Unverified, err)
	}
	var expected bytes.Buffer
	if err := dataset.Generate(ctx, &expected, o.ID, size); err != nil {
		return contract.Tag(Unverified, err)
	}
	if !bytes.Equal(body, expected.Bytes()) {
		return contract.Tag(contract.PrepareFailed, fmt.Errorf("HTTP %d range check deterministic bytes mismatch", resp.StatusCode))
	}
	if err := ctx.Err(); err != nil {
		return contract.Tag(Unverified, err)
	}
	return nil
}

func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
