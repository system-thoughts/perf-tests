/*
 * Copyright 2026 The Dragonfly Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package proxybench

import (
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	vegeta "github.com/tsenart/vegeta/v12/lib"
)

const sequential = "sequential"

// Original retains repeat/random/sequential's environment interface and formulas.
func Original(environ []string) (o Options, next vegeta.Targeter, err error) {
	values := map[string]string{}
	for _, pair := range environ {
		key, value, _ := strings.Cut(pair, "=")
		values[key] = value
	}
	value := func(key, fallback string) string {
		if values[key] == "" {
			return fallback
		}
		return values[key]
	}
	integer := func(key string, fallback uint64) uint64 {
		if err != nil {
			return 0
		}
		raw := value(key, strconv.FormatUint(fallback, 10))
		n, parseErr := strconv.ParseUint(raw, 10, 53)
		if parseErr != nil || strings.Trim(raw, "0123456789") != "" || n == 0 {
			err = fmt.Errorf("%s must be a positive decimal integer", key)
		}
		return n
	}
	duration := func(key, fallback string) time.Duration {
		d, parseErr := time.ParseDuration(value(key, fallback))
		if parseErr != nil || d <= 0 {
			err = fmt.Errorf("%s must be a positive duration", key)
		}
		return d
	}
	o.Mode = value("MODE", "repeat")
	if o.Mode != "repeat" && o.Mode != "random" && o.Mode != sequential {
		return o, nil, fmt.Errorf("unknown MODE: %s", o.Mode)
	}
	o.Rate = integer("RATE", 1000)
	o.InitialWorkers = integer("PRE_ALLOCATED_VUS", 64)
	o.MaxWorkers = integer("MAX_VUS", 512)
	o.Duration = duration("DURATION", "60s")
	o.Timeout = duration("TIMEOUT", "30s")
	o.GracefulStop = o.Timeout
	o.FollowRedirects = true
	o.ExpectedStatus = http.StatusOK
	o.Proxy = value("HTTP_PROXY", values["http_proxy"])
	var cpuErr error
	o.SeedClientCPUs, cpuErr = strconv.ParseFloat(value("SEED_CLIENT_CPUS", "0"), 64)
	if cpuErr != nil || math.IsNaN(o.SeedClientCPUs) || math.IsInf(o.SeedClientCPUs, 0) || o.SeedClientCPUs < 0 {
		return o, nil, fmt.Errorf("SEED_CLIENT_CPUS must be a finite number >= 0")
	}
	fileSize := integer("FILE_SIZE", 1<<30)
	chunkSize := integer("CHUNK_SIZE", 4<<20)
	streams := integer("STREAMS", 128)
	urlCount := integer("URL_COUNT", 32)
	if err != nil {
		return o, nil, err
	}
	if o.MaxWorkers < o.InitialWorkers {
		return o, nil, fmt.Errorf("MAX_VUS must be >= PRE_ALLOCATED_VUS")
	}
	defaultURL := "http://file-server/4m"
	if o.Mode == sequential {
		defaultURL = "http://file-server/1g"
	}
	targetURL := value("TARGET_URL", defaultURL)
	u, parseErr := url.Parse(targetURL)
	if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return o, nil, fmt.Errorf("TARGET_URL must be an absolute HTTP(S) URL")
	}
	rangeValue := values["RANGE"]
	if o.Mode == sequential && rangeValue != "" {
		return o, nil, fmt.Errorf("RANGE is not supported in sequential mode")
	}
	if rangeValue != "" && !strings.HasPrefix(rangeValue, "bytes=") {
		rangeValue = "bytes=" + rangeValue
	}
	if o.Mode == sequential || rangeValue != "" {
		o.ExpectedStatus = http.StatusPartialContent
	}
	o.Target, o.Range = targetURL, rangeValue
	if o.Mode == sequential {
		o.FileSize, o.ChunkSize, o.Streams, o.URLCount = fileSize, chunkSize, streams, urlCount
	}
	host := value("HOSTNAME", "vegeta")
	if host == "vegeta" {
		if name, e := os.Hostname(); e == nil {
			host = name
		}
	}
	runID := host + "-" + uuid.NewString()
	chunks := (fileSize-1)/chunkSize + 1
	var iteration uint64
	return o, func(t *vegeta.Target) error {
		urlValue, requestRange := targetURL, rangeValue
		switch o.Mode {
		case "random":
			urlValue = fmt.Sprintf("%s?r=%s-%d", targetURL, runID, iteration)
		case sequential:
			stream, turn := iteration%streams, iteration/streams
			pass, chunk := turn/chunks, turn%chunks
			start := chunk * chunkSize
			end := start + min(chunkSize, fileSize-start) - 1
			urlValue = fmt.Sprintf("%s?r=%s-%d", targetURL, runID, (stream+pass)%urlCount)
			requestRange = fmt.Sprintf("bytes=%d-%d", start, end)
		}
		*t = vegeta.Target{Method: http.MethodGet, URL: urlValue, Header: http.Header{}}
		if requestRange != "" {
			t.Header.Set("Range", requestRange)
		}
		iteration++
		return nil
	}, nil
}
