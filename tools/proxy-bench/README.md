# proxy-bench

Load-test the Dragonfly dfdaemon proxy with Vegeta. Responses are downloaded in
full and discarded as they arrive. The end-of-run report uses Vegeta's native
statistics, with benchmark success and payload throughput added.

## Run in Kubernetes

Deploy Dragonfly and the [file server](../file-server/file-server.yaml), then edit
`proxy-bench.yaml`: select an image from this branch's build record, the proxy,
mode and load settings. Start and read the Job as before:

```shell
kubectl apply -n dragonfly-system -f proxy-bench.yaml
kubectl logs -n dragonfly-system -f job/proxy-bench
```

Before another round, delete/recreate the Job. Request failures of at least 1%
produce exit code 99 and a Failed Job. This default threshold applies to the
three modes below; [fixed-pool](fixed-pool/README.md) has its own worker entrypoint
and no default failure threshold.

## Modes and configuration

All benchmark controls remain environment variables in the Job or container.

| Mode | Request construction |
| --- | --- |
| `repeat` | Same URL every request, normally served from the P2P cache |
| `random` | Unique query per request and run/Pod, creating distinct tasks |
| `sequential` | Interleaved streams read objects head to tail in ranged chunks |

| Name | Default | Meaning |
| --- | --- | --- |
| `HTTP_PROXY` | none | Proxy for ordinary modes; standard Go proxy environment rules apply |
| `MODE` | `repeat` | `repeat`, `random`, `sequential` |
| `TARGET_URL` | `http://file-server/4m` | Object URL; sequential defaults to `/1g` |
| `RATE` | `1000` | Target requests per second |
| `PRE_ALLOCATED_VUS` | `64` | Initial Vegeta worker goroutines; legacy parameter name retained |
| `MAX_VUS` | `512` | Maximum Vegeta workers, at least PRE_ALLOCATED_VUS |
| `DURATION` | `60s` | Window for starting requests |
| `TIMEOUT` | `30s` | Complete-request timeout; in-flight requests may finish after DURATION |
| `RANGE` | none | Optional Range for repeat/random, such as `0-1023` |
| `FILE_SIZE` | `1073741824` | Sequential object size, bytes |
| `CHUNK_SIZE` | `4194304` | Sequential bytes per request; last chunk is truncated |
| `STREAMS` | `128` | Sequential round-robin stream count |
| `URL_COUNT` | `32` | Sequential URL pool size |
| `SEED_CLIENT_CPUS` | `0` | Optional cores used by Seed, for payload core/Gbps |

The image still starts ordinary modes by default. No engine selector or new
required setting is needed. k6 CLI/JS, `K6_*` and k6 configuration files are no
longer interpreted. For optional local development:

```shell
make build-proxy-bench-worker-support
HTTP_PROXY=http://127.0.0.1:4001 MODE=sequential RATE=2000 MAX_VUS=1024 \
  ./bin/proxy-bench-worker-support original
```

## Read the report

Vegeta prints Requests `[total, rate, throughput]`, Duration, Latencies
`[min, mean, 50, 90, 95, 99, max]`, Bytes In/Out, Success, Status Codes and Error Set.
The following single-line `PROXY_BENCH_VEGETA_SUMMARY_V1` JSON retains native
metrics, effective options and benchmark counters. JSON durations/latencies use
nanoseconds; success values are ratios from 0 to 1.

- `native_summary.rate` is actual logical HTTP attempts per second using Vegeta's
  first/last request-start window, rather than configured RATE or server arrivals.
- `native_summary.throughput` is successful requests per second. These two rates
  use Vegeta's native denominators and have different meanings.
- `expected_status_success_ratio` requires the expected status and a complete
  response without errors: 206 for ranged requests, otherwise 200. Native Success
  accepts 2xx/3xx, so inspect both when a proxy ignores Range.
- `native_summary.latencies['99th']` is P99. Only whole-request latencies are
  reported; no connect/first-byte/download breakdown is synthesized.
- Bytes In and payload throughput count actual body bytes, including partial
  reads. They do not include headers, and empty saved bodies do not make them zero.
- The CPU-cost line, when configured and throughput is positive, uses payload Gbps.

At MAX_VUS, Vegeta waits for a worker. It can fall below target RATE and does not
produce k6 dropped iterations. Inspect actual rate, errors and generator resources.
Fewer than two samples produce an insufficient-samples notice; a no-request run
fails the ordinary-mode threshold. A successful process exit does not establish
experimental validity. OOM/SIGKILL may leave incomplete logs.

## Build and development images

```shell
make docker-build-proxy-bench
```

The fork workflow publishes tools and worker images to
`ghcr.io/system-thoughts/perf-tests/`. Builds from `fixed_pool_refactor_vegeta`,
`fixed_pool_refactor` and `nydus_hot_dev` use distinct
`<branch>-sha-<full-sha>-run-<run-id>-<attempt>` tags. Select matching images and
deployment material from one build; its `build-info.txt` records the immutable
digests and engine. Published builds do not update a shared latest tag.
