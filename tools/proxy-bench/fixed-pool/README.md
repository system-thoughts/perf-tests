# Fixed-pool Range benchmark

Fixed-pool adds a deterministic Range workload to proxy-bench. Prepare immutable
objects, generate an access plan, prepare the chosen Dragonfly Seed, then run one
or more independent workers. k6 supplies request scheduling and native metrics;
workers do not prepare data or coordinate other workers during measurement.

Use matching `proxy-bench-tools`, `proxy-bench` and Kubernetes material from the
same development branch/build. `fixed_pool_refactor` uses the interface below;
`nydus_hot_dev` retains the previous preparation commands and plan format. These
are development interfaces: the new branch does not provide old command aliases
or an old-plan converter. The existing file-server image is shared by both lines.

## Deliverables and entrypoints

| Deliverable | What runs there |
| --- | --- |
| proxy-bench-tools image | `proxy-bench-prepare data`, `plan`, `seed-cache` |
| proxy-bench image | k6, fixed-pool scripts and internal worker-support; the original proxy-bench entrypoint remains |
| file-server image | Existing Nginx GET/HEAD/Range server, with generated objects mounted read-only |
| [deploy/](deploy/) | Matching Kubernetes templates; the current handoff/report workflow is retained during this refactor |

Each preparation action runs independently. There is no all-in-one command,
experiment workspace, latest-plan lookup, cache-clearing action or file distributor.
Select input/output locations yourself. All preparation actions require a new or
empty business output directory; nonempty output is refused, never reused or
removed automatically. To reuse a valid artifact, pass it directly to its consumer.
To regenerate, choose a new location or explicitly clean the old one yourself.

For local development:

```bash
make build-proxy-bench-prepare build-proxy-bench-worker-support
./bin/proxy-bench-prepare --help
./bin/proxy-bench-prepare plan --help
```

`make build-proxy-bench-tools` builds both executables for linux/amd64 and
linux/arm64. Their Dockerfiles are [tools](../Dockerfile.tools) and
[worker](../Dockerfile). Tests and fixture vectors are not copied into the images.

## 1. Generate source objects

Inside a tools container with writable source storage:

```bash
proxy-bench-prepare data --root /data/dataset \
  --base-url http://file-server.benchmark.svc.cluster.local \
  --size 65536 --count 2
```

| Option | Default / purpose |
| --- | --- |
| `--root` | Required, new/empty object storage directory |
| `--base-url` | Required HTTP source base reachable from the Seed; no credentials, query or fragment |
| `--size` | Required positive decimal bytes per object (B) |
| `--count` | Required positive decimal object count (N) |
| `--generation-workers` | 1; optional generation concurrency, without changing bytes or identities |

The command generates deterministic bytes and computes each object's SHA-256.
Objects are published as `objects/sha256/<digest>`; the completed manifest is
`ROOT/dataset-manifest.json`. Its `objects[]` is ordered by object_id and records
URL, relative path, size and digest. B/N and URLs belong to this manifest, not the
plan or worker configuration. No source objects are baked into a new image.
Use a source hostname/address that the worker actually proxies: k6's Go transport
bypasses literal localhost/loopback origins even with HTTP_PROXY. URL acceptance
by data preparation does not prove the later measurement uses the Seed.

Mount `objects/` at the corresponding Nginx HTTP path, using the existing
[source bundle](deploy/source/README.md). The manifest is published only after all
objects succeed. Failure retains unfinished state and reports its path; do not
adopt it as a completed dataset. The command's JSON result on stdout returns
`manifest.path` and `manifest.sha256` on success, otherwise `manifest: null`.

## 2. Generate an access plan

Only the manifest is needed; object bodies and the Seed need not be accessible:

```bash
proxy-bench-prepare plan --manifest /inputs/dataset-manifest.json \
  --range 16384 --workers 2 --out /inputs/plan
```

| Option | Default / purpose |
| --- | --- |
| `--manifest` | Required path to the selected completed data manifest |
| `--range` | Required positive decimal Range length R; B must be divisible by R |
| `--workers` | 1; number of independent worker shards W, between 1 and total regions M |
| `--out` | Required new/empty output for this plan, not a container for historical plans |
| `--memory-budget` | Omitted: no memory-budget gate; an explicit value must be positive bytes |
| `--file-budget` | Omitted: no file-budget gate; an explicit value must be positive bytes |

The output contains `request-plan.json` and `shards/<worker_id>.json` (plus
internal writer state). No URL-list copy is generated. The descriptor binds the
manifest digest and records R, region count, algorithm/seed, W and each shard's
path, digest and count. Shards contain global region numbers. URLs remain in the
manifest. The descriptor is published last; incomplete output is not a valid plan.

The producer checks fixed order, uniqueness and coverage before publishing.
Consumers check the descriptor and selected shard's reference, digest, number,
count and bounds; they do not regenerate the full permutation. A worker need not
have other workers' shard files. Preserve relative references when moving a plan.

Changing R or W requires another plan at a location you choose. Changing only
RATE or DURATION does not. Retrying the generator at an existing output fails,
even for identical inputs; directly select that plan to reuse it.

Plan budgets constrain preparation, not each load generator. No remote resource
probe is performed. For manifest bytes D, the conservative estimates are:

- memory: `4M + ceil(M/8) + 2D + 512W + 64MiB`;
- files: `11M + 512W + 1MiB` (the URL copy is no longer stored).

Preflight also checks output free space; an explicit file budget is checked again
against actual plan bytes before publication. Estimates cannot prevent every OOM
or concurrent disk-space change. Worker initialization and VU costs need separate
calibration. Neither budgets nor invalid R cause automatic workload resizing.

## 3. Prepare the selected Seed

Provide one [target-config.yaml](examples/target-config.yaml). The required target
fields are `proxy` (HTTP proxy origin) and `expected_server_ip` (daemon's reported
serving IP, potentially different from a Service address). `target.ref` is an
optional display name and is not a validity gate. No instance label, proxy-rules
file or manually calculated rule digest is required. Removing declarations does
not add support for URL rewriting, HTTPS sources or query-dependent task identity.

```bash
proxy-bench-prepare seed-cache --manifest /inputs/dataset-manifest.json \
  --target-config /inputs/target-config.yaml --out /inputs/preparation
```

Optional `prepare` fields default to concurrency 1, request_timeout_ms 1800000,
and ready_timeout_ms 60000. Each object is read fully through the chosen proxy
and checked for status, task/serving identity, length and SHA-256; one immediate
prefix Range then checks status 206, identity, Content-Range, length and bytes.
There is no second pool-wide Range pass and no upload gRPC check.

The finished response header is not a readiness gate. Only retryable Range
transport failures get up to three attempts (100/500ms backoff), sharing the
ready-time budget; full GET and data/identity errors are not retried. Final failure
stops new dispatch, leaves in-flight work bounded, and prevents pool readiness.

The producer writes `target-preparation.json` plus `target-object-results.json`.
The former records selected manifest, target, method, effective prepare settings,
status and evidence reference; the latter retains per-object observations and
errors. `finished_check_at` is the historical name for the successful immediate
Range time, not Dragonfly's task completion time. Failed preparation can also
produce these files: existence is not proof of `status: ready`.

Ready proves this preparation's content/Range checks under sufficient capacity,
cache retention and stable-target conditions. It is not an independent final
snapshot of all local pieces, a guarantee against future eviction, or proof that
all background downloads are idle. Metadata-only upload verification is deferred
until the server-side cancellation issue is resolved and revalidated.

After correcting a failure, rerun the whole selected manifest into a new/empty
output. The tool does not clear Seed tasks or pieces. Correct cache may be reused;
if wrong bytes were previously cached, fixing the origin alone may be insufficient.
Use task-specific diagnostics and environment recovery, not an automatic cache wipe.

## 4. Run a worker

Use the matching [worker template](deploy/worker/worker-job.tpl.yaml) or
[standalone example](proxy-bench-fixed-pool.yaml). Each worker receives explicit
paths to its selected inputs, mounted read-only:

- manifest, public plan descriptor and this worker's shard;
- target configuration, preparation record and its referenced evidence.

These are six files in the minimal worker input set. There is no required global
directory tree; different nodes can use different absolute paths. Do not mount
source object bodies into the worker. Input checks reject failed/unverified ready
records and data/target/reference mismatches before k6 starts. They do not replay
preparation or re-audit every object in producer-owned evidence.

| Environment | Requirement / script default |
| --- | --- |
| MANIFEST, PLAN | Required selected manifest and request-plan.json paths |
| TARGET_CONFIG, TARGET_PREPARATION | Required selected target configuration and preparation record |
| RUN_ID, RATE | Required run label and total positive integer requests/second |
| WORKER_ID | 0; must exist in the plan |
| DURATION | 300s, at least 1s; positive whole-millisecond precision |
| TIMEOUT | 30s, positive whole-millisecond precision |
| GRACEFUL_STOP | TIMEOUT + 5s, strictly greater than TIMEOUT |
| PRE_ALLOCATED_VUS, MAX_VUS | 64 / 512; positive, max >= preallocated |

Do not separately configure worker HTTP_PROXY: the entrypoint derives it from
validated target.proxy and exports it for k6. Native k6 option precedence remains;
the summary records effective options. The benchmark does not check k6 or Client
version numbers. Regressions use the engine from the Dockerfile's worker image.

Workers derive their rates from the common RATE and shard sizes. One iteration
consumes the next local region, wrapping in fixed order, and makes one business
GET. There is no business retry or redirect; connection reuse can cause a native
transport resend of an idempotent request. URL/Range mapping is unchanged.

## Results, errors and compatibility

Use worker stdout logs for the shared proxy-bench text report and the four JSON
records: `RUN_SPEC_V1`, `WORKER_SPEC_V1`, `K6_SUMMARY_V1`, `WORKER_EXIT_V1`, each
prefixed `PROXY_BENCH_FIXED_POOL_`. They retain input identities, worker/rate,
effective k6 options, native metrics (including plan_consumed) and process exit.
No result volume or Go report-conversion command is required. Job completion is
not an HTTP quality threshold; fixed-pool does not add the original mode's 1% gate.

Preparation commands emit one JSON result on stdout and diagnostics on stderr.
Diagnostics identify actual input paths/fields and applicable expected/actual
values; seed-cache also explains how to rerun after correction. The current Job
templates still redirect stdout to per-Job report files; stderr is visible through
kubectl logs. Removing that report handoff is the next deployment-refactor stage.

Exit codes: help/success 0, invalid input/conflicting output/budget 2, execution
failure 3, cancellation 130. Missing, invalid or wrong inputs do not silently
trigger regeneration, repair, a different plan or a different target.

Independent JSON Schema files are no longer shipped. Go definitions and runtime
checks remain authoritative; the old standalone schemas cannot be used for
editor/validator integration. Historical preparation records may contain an
instance label and rule-identity digest: these are readable metadata, not current
configuration or validity gates. Data/target/status/method/evidence checks remain.
Old plan descriptors containing objects_file require the old delivery line; make
a new plan with the new tool instead of editing published bytes or IDs.

Go units run via `make test-race`. Integration tests are maintained in
[perf-tests-fixed-pool-tests](https://github.com/system-thoughts/perf-tests-fixed-pool-tests)
and consume built images and matching deployment material, not product source.
They are developer regressions, not prerequisites for each benchmark run.
