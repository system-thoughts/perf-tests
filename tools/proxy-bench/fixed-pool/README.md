# proxy-bench fixed-pool

The fixed-pool mode adds explicitly prepared objects and reusable Range access
plans to proxy-bench. This directory delivers dataset and plan preparation, the Kubernetes source
bundle, full-pool Seed-cache preparation, and single-worker k6 execution with
automatic native and derived reports.

## Build

Use the repository's Go 1.25 toolchain; no new Go dependencies are required.

```bash
make build-proxy-bench-data-prepare build-proxy-bench-plan-prepare build-proxy-bench-seed-cache-prepare
./bin/proxy-bench-data-prepare --help
./bin/proxy-bench-plan-prepare --help
./bin/proxy-bench-seed-cache-prepare --help
```

`make build-proxy-bench-tools` creates static preparation and worker-support binaries under
`bin/proxy-bench-tools/{amd64,arm64}/`. `make docker-build-proxy-bench-tools`
exports a local multi-platform OCI archive at `bin/proxy-bench-tools-image.tar`;
it does not publish an image. For a single-platform Docker daemon image, use
Buildx with `--platform linux/amd64 --load` (or the native arm64 platform) and
`--build-arg TARGETARCH=amd64` when building the tools Dockerfile manually.

Linux amd64 and arm64 builds are supported. Cross-compilation establishes
buildability; native arm64 execution and performance require separate validation.

## Command parameters

Sizes and budgets use decimal integer byte counts, without unit suffixes.
Required arguments have no usable defaults; internal zero/empty values indicate
missing input. Missing or invalid input produces a structured error and exit 2.
`--help` or `-h` prints usage and exits 0.

Dataset command:

| Argument | Required/default | Purpose and rationale |
|---|---|---|
| `--size` | Required | Object bytes; explicitly choose the experiment's object size. |
| `--count` | Required | Object count; explicitly choose dataset scale. |
| `--root` | Required | Empty business directory; explicitly choose storage placement. |
| `--base-url` | Required | Final source HTTP address; deployment determines reachability and URL identity. |
| `--generation-workers` | Default `1` | Start sequentially; concurrency changes cost, not content. |

Plan command:

| Argument | Required/default | Purpose and rationale |
|---|---|---|
| `--manifest` | Required | Select a specific dataset; never infer the newest manifest. |
| `--range` | Required | Range bytes; explicitly choose the request geometry. |
| `--workers` | Default `1` | Start with one load generator; a larger count creates explicit shards. |
| `--plan-dir` | Required | Explicitly choose plan storage and reuse location. |
| `--memory-budget` | Omitted by default | Estimated plan-memory ceiling; no guessed machine limit. |
| `--file-budget` | Omitted by default | Total plan-file ceiling; excludes source/Seed data capacity. |

Both budget arguments must be positive when supplied. Internal zero represents
an omitted budget; explicitly passing zero is invalid. Omission still reports
estimates and checks output free space before generating a new plan. Memory
budgeting is an estimate check, not an OS/container memory limit or a runtime
RSS monitor. Budgets apply to the Go preparation process and all plan files,
not individual k6 workers. Preparation can run elsewhere; these checks do not
prove that the load generators have sufficient resources. File budgeting checks
the estimate before generation and the total
actual file lengths before publication. See the formulas and limits below.

## Create a dataset

Use decimal positive byte sizes and counts, an empty source directory, and a real
HTTP source address when serving the objects. This example generates URLs without
connecting to them. Credentials, queries, and fragments
are rejected. The source must be reachable from the Seed network. Local URLs
are accepted for data preparation; native k6 proxy routing bypasses literal
localhost/loopback destinations, so use a proxyable source hostname for Seed
measurements. Clearing NO_PROXY does not change that native exception.

```bash
./bin/proxy-bench-data-prepare \
  --size 64 --count 2 --root /data/dataset \
  --base-url http://source.example.test > dataset-result.json
```

The command generates all objects and publishes an immutable manifest. It accepts
no manifest, Range size, shard count, plan directory, or plan budget. To change
object size or count, create another dataset in a new directory. There is no
append, shrink, subset, inventory management, or cross-pool object reuse.
`--generation-workers` defaults to 1 and only changes generation concurrency.

Stdout is one JSON object with `schema_version`, `manifest:{path,sha256}`,
`generated_objects`, `generated_bytes`, `elapsed_ms`, and `errors`. A failure before
publication leaves `manifest:null`; generated counts include complete objects.
Progress and diagnostic text go to stderr. `--help` prints usage instead.

Objects reside at `ROOT/objects/sha256/CONTENT_SHA256`; snapshots reside at
`ROOT/manifests/MANIFEST_ID/dataset-manifest.json`. The manifest ID hashes the raw
UTF-8 file bytes. Published files are read-only. [Publish the HTTP origin](#publish-the-http-origin)
before later Seed preparation.

## Publish the HTTP origin

Origin publication is required before Seed cache preparation. The dataset
command writes files and URLs; it does not start nginx, upload data, or operate
Kubernetes. Independent plan preparation only needs a manifest copy and can run
before the origin is online.

The existing [file-server image](../../file-server/Dockerfile) runs nginx and
embeds zero-filled `/4m`, `/1g`, and other fixtures. Its original
[StatefulSet and Service](../../file-server/file-server.yaml) serve those fixtures
without a data-volume mount. Reuse that static HTTP serving capability with
externally generated objects. Create separate fixed-pool resources with distinct
names, labels, and Service selectors to preserve the original FileServer.

The Kubernetes source bundle supplies a shared configuration,
data-generation Job, FileServer/Service templates, an optional PVC example, and
step-by-step rendering and recovery instructions. Use [the source bundle](deploy/source/README.md) for configuration, whitelisted
rendering, sequential Job/server deployment, log extraction, manifest export,
and failure diagnosis. The following layout is also the standalone data command
integration contract.

Use a dedicated business directory inside the source PVC:

| Location | Path |
|---|---|
| Writable PVC mount in the generation task | `/data` |
| Dataset command root | `/data/dataset` |
| Generated objects | `/data/dataset/objects/sha256/DIGEST` |
| Generated manifest | `/data/dataset/manifests/MANIFEST_ID/dataset-manifest.json` |
| nginx object directory | `/usr/share/nginx/html/objects` |
| Read-only dataset mount for manifest retrieval | `/fixed-pool-data` |

Only `/data/dataset` must be empty or absent; the PVC root may contain unrelated
entries such as `lost+found`. Generate on the final source volume to avoid copying
the object pool between machines. If using another location, transfer the files
without changing bytes or paths before serving them.

Before target cache preparation:

1. Choose storage, permissions, and a stable address reachable from the Seed.
   For an in-cluster Seed, an example is
   `http://SOURCE_NAME.NAMESPACE.svc.cluster.local`; adjust the DNS domain or use
   an explicit routable address for an external Seed. Determine the final address
   once and use it as `--base-url` before generation; generation makes no HTTP
   requests to it.
2. Run data generation separately from the HTTP server. The Job directly
   invokes the existing command with `completions=1`, `parallelism=1`,
   `restartPolicy=Never`, and `backoffLimit=0`. Preserve the actual generation
   Pod's stdout JSON and stderr. Do not publish the source after failed
   generation, and do not use a generation initContainer on the server.
3. After successful generation and release of its volume mount, start a separate
   FileServer and Service on the same PVC. Follow the PVC access mode and storage
   topology; RWX is not required, and RWO does not mean only one Pod. Use the
   documented FileServer profile: nginx root /usr/share/nginx/html and port 80.
   The supplied templates keep that mapping fixed.
4. Retrieve the manifest identified by the generation result as described below,
   verifying its raw digest. Provision the selected endpoint and proceed directly
   to Seed preparation, which validates all objects through the selected Seed.
   No separate source verification or saved body copy is required. Maintainers
   cover source GET/HEAD/Range behavior and deployment faults in source regressions.

The service container mounts only the objects under the HTTP root and also
mounts the dataset outside that root for manifest retrieval:

```yaml
# Separate fixed-pool file-server container
volumeMounts:
  - name: fixed-pool-source
    mountPath: /usr/share/nginx/html/objects
    subPath: dataset/objects
    readOnly: true
  - name: fixed-pool-source
    mountPath: /fixed-pool-data
    subPath: dataset
    readOnly: true

# Its Pod spec
volumes:
  - name: fixed-pool-source
    persistentVolumeClaim:
      claimName: fixed-pool-source
```

This adds `BASE_URL/objects/sha256/DIGEST` while preserving the image's original
fixtures. Manifests are not published as HTTP files. Use the returned
`manifest.path`, not a scan for the newest manifest: a generation path
`/data/dataset/manifests/ID/dataset-manifest.json` maps to
`/fixed-pool-data/manifests/ID/dataset-manifest.json` in the service container.
For example, with `SERVICE_POD` and `MANIFEST_ID` taken from the actual deployment
and successful generation result:

```bash
kubectl exec -n "$NAMESPACE" "$SERVICE_POD" -- \
  cat "/fixed-pool-data/manifests/$MANIFEST_ID/dataset-manifest.json" \
  > dataset-manifest.json
sha256sum dataset-manifest.json
```

Require a successful export and compare the digest to the generation result's
`manifest.sha256` before passing this copy to plan or Seed preparation. Exporting
the manifest does not require copying the source objects. A workstation path is
not automatically available on a container host or cluster node. With Docker,
bind-mount the same object and dataset directories read-only at the nginx and
manifest locations above, and publish a port reachable from the Seed.

Keep the source URL and bytes stable. Server, mount, or network failures require
repairing the service, not rerunning successful data generation. Restarting the
server never regenerates objects. Changes to RATE or R/W reuse the source;
B/N changes require a new dataset. A changed source address must not silently
rewrite the published manifest: restore the original endpoint, or create a new
dataset for the new address. Generation failures retain artifacts under the
existing failure rules; do not automatically adopt them. Deleting a Job or
restarting a service must not delete the PVC. Archive the rendered resources,
image digests, generation logs, exported manifest, and HTTP check evidence.

## Prepare or reuse an access plan

Pass the manifest path from the dataset result to the independent plan command:

```bash
./bin/proxy-bench-plan-prepare \
  --manifest /data/dataset/manifests/RETURNED_ID/dataset-manifest.json \
  --range 16 --workers 3 --plan-dir /data/fixed-plans > plan-result.json
```

Only the manifest is needed: source object files, a source root, target
configuration, and Seed access are unnecessary. A copied manifest works without
the original body directory. B/N and object URLs come from the validated input.
Object size must be divisible by Range size; invalid geometry is rejected
without selecting a different Range. The default worker count is 1.
R/W changes generate a separate plan. RATE and
duration are absent from preparation; rate allocation is shared Go code for the
later worker entry point.

Stdout contains `schema_version`, `manifest_sha256`, `plan:{path,sha256}`,
`region_count`, `worker_count`, `reused`, `estimates:{memory_bytes,file_bytes}`,
`actual_file_bytes`, `elapsed_ms`, and `errors`. On failure `plan` is null;
unavailable inputs or statistics are null and errors explain the failure.
Correcting plan inputs and rerunning does not regenerate data or prepare a Seed.

Plans reside in `PLAN_DIR/INPUT_KEY/` with `request-plan.json`, `object-urls.json`,
and `shards/WORKER_ID.json`. The input key hashes the manifest's actual byte
digest, R, W, fixed algorithm, and seed. `plan.sha256` hashes the actual plan file
and is distinct from that lookup key. Repeating identical inputs reuses the plan
after checking references, checksums, counts and region bounds. Canonical order,
coverage and uniqueness are checked once before a new plan is published.
Consumers trust published artifacts; coordinated edits to data and their hashes
are outside the integrity check. Keep published plans immutable.
A changed manifest at the same path is a new input; moving identical bytes does
not change identity. Corrupt or missing existing artifacts are rejected without
overwriting or automatically repairing them.

## Contracts, budgets, and failures

[schemas](schemas) describes version 1 contracts and command results. Shared Go
validation also checks fields, duplicate keys, nulls, types, fixed seeds,
geometry, identity relationships, and shard membership. Fixed fixtures live in
[testdata/vectors.json](testdata/vectors.json). Generated manifests and plans use
compact fixed-order JSON with one LF and no embedded self-hash. Imported valid
manifest JSON is identified by its original bytes, without re-encoding.

The plan command accepts optional positive `--memory-budget` and `--file-budget`
byte counts. Given manifest byte length D, total regions M, and workers W:

- Estimated memory: `4*M + ceil(M/8) + 2*D + 512*W + 64*1024*1024` bytes.
- Estimated files: `11*M + 512*W + 2*D + 1024*1024` bytes.

Configured budgets are checked before allocating the permutation. Actual plan,
URL index, and shard lengths are checked before publication. Without budgets,
estimates and free-space checks still run. These estimates do not guarantee peak
Go/k6 memory. Production pool sizes and loading costs require calibration.
M is limited to `2^32-1`; JS-facing integers are limited to `2^53-1`.

The producer holds the O(M) permutation and validates the complete published
shard set before publication. Worker startup reads common metadata and only its
own shard, checking references, checksums, worker ID, count and integer bounds;
it never reconstructs the global permutation or allocates a global uniqueness
bitmap. Metadata uses O(N+W) space and shard validation streams O(Mw) entries.
k6 SharedArray loads O(Mw+N) entries. Reuse checks all shard files for integrity
but does not regenerate the permutation. Measure helper and k6 resource costs
separately; generation budgets do not establish sufficient worker resources.

Errors contain `code`, `stage`, and `message`. Exit status is 0 for completed
preparation, 2 for invalid input, contract mismatch, budget excess or data
conflict, 3 for preparation/I/O/publication failure, and 130 for interruption.
An interruption takes precedence while other available errors are retained.

One writer locks each output directory. On a dataset failure inspect
`ROOT/.fixed-pool-incomplete`, its `failed.json`, and any published but
unregistered objects. A retry refuses to adopt residual artifacts. Confirm
ownership before removing the failed dataset or choose another empty directory.
Incomplete plans use `.incomplete-INPUT_KEY`; inspect them before cleanup or use a
new plan directory. Partial object bytes may exceed completed-object counters.
There is no automatic recovery or block-level resume.

## Artifact compatibility

Content and order seeds (`nydus-range-content-v1`, `nydus-range-order-v1`) and
algorithm versions remain fixed computation identifiers. Renaming this mode does
not change object bytes, digests, URLs, or existing manifest identity.

The URL-index name is now `object-urls.json`, so new plan bytes and IDs differ
from the original prototype. Keep published artifacts immutable and use a new
plan output directory when an old incompatible plan occupies the input key.
There are no old command aliases or automatic migrations. Plan checks do not
read or hash source bodies; keep source storage controlled. Later Seed
preparation performs full target-body verification.

## Prepare a target Seed cache

After publishing the source, copy
[the target configuration](examples/target-config.yaml), fill the actual proxy,
expected server IP and observed instance reference. No Client version is
requested or used for admission. Preparation checks actual response identity,
body and completion evidence; record software versions separately in experiment
notes when comparing results. Earlier candidate configurations containing
`client_version` must drop that input and generate a new preparation record in
a new directory. Keep historical ready records unchanged.
Record the **effective** proxy rules in `proxy-rules.json`, including the actual
filtered query parameter list, `rewrite_url=false`, and `use_tls=false`. Hash its
original bytes and place that digest in `proxy_rules_ref.sha256`. A relative
rules path resolves against the target configuration directory. The tool checks
this declaration; it does not log in to the Seed to prove the rules or instance.

```bash
./bin/proxy-bench-seed-cache-prepare \
  --manifest dataset-manifest.json --target-config target-config.yaml \
  --out /results/NEW_PREPARATION > target-command-result.json
```

All three flags are required and have no usable defaults. `--out` is new or
empty and existing results are never overwritten. `--help`/`-h` exits 0 without
JSON. Optional YAML `prepare` fields default individually to `concurrency: 1`,
`request_timeout_ms: 1800000`, and `ready_timeout_ms: 60000`: conservative
preparation starting points, outside measurement. Values must be positive;
timeouts use integer milliseconds. Concurrency is bounded by object count.

The HTTP client explicitly selects the proxy, ignores ambient proxy bypass
settings, disables redirects/decompression and connection-reuse retries, sends
fixed URL identity headers and prefetch=false, and never supplies a piece length.
Each object requires a complete 200 response with exact length/SHA, matching
Dragonfly task/server identity, and a deterministic 206 prefix probe with the
same task and a true download-finished marker. After all full reads, a second
pool-wide probe pass checks completion again. Each probe is attempted once;
missing completion evidence is not repaired by polling or downloading more.
The true marker describes state at request start and cannot replace full-body
verification. Different objects must not unexpectedly share a task.
A task finished timestamp can also be set by a Range-only download while other
pieces are still missing. Integration validation must inspect actual piece
coverage; the timestamp does not replace complete-object verification.

The command streams bodies without saving a second copy. On failure it stops
new object dispatch and lets already started requests finish within their
configured timeouts; interruption cancels current requests. Retry explicitly
with a new output directory/preparation ID; cached Seed data is not deleted.

`target-object-results.json` contains every object, known task/IP/received bytes,
actual digest and timestamps, status and reason. Unknown fields are null.
`target-preparation.json` is published last and binds the exact manifest digest,
fixed identity/rules digest, target, method, effective prepare options and the
object evidence file digest. Stdout has the same preparation record (including
structured input errors); archive it separately. Status is `ready`, `failed`
(known failure), or `unverified` (evidence unavailable/unsupported), and only a
successful full pool can be ready. Exit codes remain 0/2/3/130; unsupported or
unverified targets exit 3. Preserve failed evidence, then fix and retry rather
than overwriting it.

The worker consumes a ready preparation summary by checking dataset/target/identity
bindings, status, object count and evidence-file digest. It streams that digest;
it does not parse every saved object result or repeat body verification. Keep the
summary and referenced evidence unchanged. Full body and completion checks belong
to `proxy-bench-seed-cache-prepare`; regressions protect that producer behavior.

These records prove the preparation-end condition only. Operators must verify
the actual automatic piece length and Range alignment on first integration or
configuration/version changes, using independent target state. The command does
not claim to observe actual P or monitor ongoing GC, restarts, page cache, origin
traffic or background jobs. Freeze target/storage/retention conditions and
record the instance externally before measuring. Source checks and ordinary
HTTP 200 responses alone do not establish a ready Seed.

## Run a prepared single-worker test

Use `proxy-bench-fixed-pool.yaml` after preparing the dataset, a **W=1** plan,
HTTP source and ready Seed evidence. No object bodies are mounted in the worker.
The new Job uses the same proxy-bench image with an explicit command; the image's
original entrypoint and repeat/random/sequential modes remain available.

Use an image built from a fixed-pool-capable revision and pin its digest in the
Job. The existing published `dragonflyoss/proxy-bench:v2.0.0` image does not
contain this entrypoint. This development delivery has no published fixed-pool
image; a builder must provide one to the test cluster. Reuse that image across
runs with the same code; testers do not need to rebuild it for each test.

If no suitable image is available, the builder can build one for a local native
amd64 Docker daemon with Go and Docker Buildx:

```bash
make build-proxy-bench-tools
docker buildx build --platform linux/amd64 --load -t localhost/proxy-bench:fixed-pool \
  -f tools/proxy-bench/Dockerfile .
```

Use the native arm64 platform on arm64. Make the image available to your cluster
and pin its digest in the Job. An operator may use `examples/input-pvc.yaml` to create an input PVC and a
temporary transfer Pod, copy metadata with tar through `kubectl exec -i`, then
delete only the helper Pod. This is a separate operator action; the worker never
creates it. On a node-local ReadWriteOnce volume, schedule the worker on the
volume's node. The input PVC must contain this metadata layout:

```text
/inputs/
  dataset-manifest.json
  plan/request-plan.json
  plan/object-urls.json
  plan/shards/0.json
  target-config.yaml
  proxy-rules.json
  target-preparation.json
  target-object-results.json
```

Copy the entire prepared plan directory into `plan/` and the preparation's two
JSON files together. Preserve the target configuration's rules reference.
From the repository root, copy the Job template to a separate file for this run:

```bash
JOB_YAML="$(mktemp "$HOME/proxy-bench-fixed-pool.XXXXXX.yaml")"
cp tools/proxy-bench/fixed-pool/proxy-bench-fixed-pool.yaml "$JOB_YAML"
```

Edit the copy's image, PVC name, proxy, total RATE, DURATION and RUN_ID. The
commands below retain the template's Job name; if you change it, set JOB_NAME to
that actual name before submitting. Set NAMESPACE to the input PVC's namespace:

```bash
JOB_NAME=proxy-bench-fixed-pool-run-001
: "${NAMESPACE:?set the input PVC namespace}"
kubectl -n "$NAMESPACE" apply -f "$JOB_YAML"
kubectl -n "$NAMESPACE" get pods -l "job-name=$JOB_NAME"
```

Wait for the container to start or finish before collecting its logs. A short Job
may already be Completed; it need not become Ready again. For Pending,
ContainerCreating or ImagePullBackOff, inspect Pod events first. Then collect:

```bash
kubectl -n "$NAMESPACE" logs -f "job/$JOB_NAME" > worker-0.log
kubectl -n "$NAMESPACE" get job "$JOB_NAME"
```

The Job checks the saved input relationships before running k6. It does not
contact Seed for a new preparation check or create other Jobs. The template
uses a read-only input PVC, writable result emptyDir, no service-account token,
no elevated capabilities, no restart and no Job-level retry. Preserve the Job
logs before deleting it; use a writable PVC instead of emptyDir if result files
must survive Pod deletion.

Each iteration calls `http.get` once, with no business-layer retry or redirect.
Connection reuse remains enabled. The native k6/Go HTTP transport can transparently
resend an idempotent GET when a reused connection fails before response headers.
This uses the same URL and Range and does not consume another plan item. Native
`http_reqs`, QPS and duration retain their original semantics; physical GET counts
observed at the proxy can differ. The controlled regression includes this case.

### Worker configuration

| Environment | Requirement/default | Purpose |
|---|---|---|
| MANIFEST, PLAN | Required, no default | Dataset and request-plan paths |
| TARGET_CONFIG, TARGET_PREPARATION | Required, no default | Target/rules and ready evidence |
| HTTP_PROXY | Required, equal to target.proxy | Actual HTTP proxy; bypass variables are removed |
| RATE | Required positive integer, at most 2147483647 | Total run rate; allocated by shard size |
| RUN_ID | Required, 1..128 letters/digits/._-, starts alphanumeric | New identity for each run |
| WORKER_ID | 0 | Select own shard; below plan W |
| DURATION | 300s | Starting duration; freeze for experimental comparisons |
| TIMEOUT | 30s | Per-request timeout |
| GRACEFUL_STOP | TIMEOUT + 5s | Engineering allowance; explicit value must exceed TIMEOUT |
| PRE_ALLOCATED_VUS, MAX_VUS | 64, 512 | Existing proxy-bench starting points; max must cover preallocation |
| RESULT_DIR | /results | Writable, initially empty; existing results are never overwritten |

Durations must be positive and representable as whole milliseconds. DURATION must
be at least 1s, as required by k6's constant-arrival-rate executor. An explicitly
empty or malformed value is rejected. MODE/RANGE, legacy load knobs, extra
identity overrides and unknown `PROXY_BENCH_*` variables are rejected. Native
`K6_*` options are accepted. No empty k6 configuration file is supplied.
The entry clears environment proxy bypass variables. Native localhost/loopback
exceptions still apply; choose a source hostname that routes through the Seed.
The internal worker-support subcommands run automatically.

k6 applies its native file, script, environment and CLI precedence. Testers own
their local configuration and environment. `worker-spec.json` records benchmark
script inputs, while `k6-summary.json.effective_options` captures k6's consolidated
and derived test options once in handleSummary. Native null members are preserved;
this snapshot is not a dump of all process settings. If the engine
fails before handleSummary, no snapshot is available. External thresholds can
make k6 exit nonzero; the worker preserves the engine failure and raw exit code.
Multiple scenarios or an execution segment leave coverage unverified rather than
being rejected. The original proxy-bench uses the same native configuration layers.

The runtime image selects its k6 dependency in [Dockerfile](../Dockerfile).
Version selection is a build and maintainer-regression concern. The shipped worker
never calls `k6 version` and does not require, record or compare k6 versions when
preparing inputs or deriving results. New summaries omit `k6_version`; older
summaries may contain it, and the reader ignores that metadata. `tool_versions`
retains helper/Go and script identifiers, without a required k6 entry.
Required native metrics and fields are still checked for result completeness.
Dependency upgrades must pass both controlled suites using the Dockerfile's engine;
API or summary changes can require adaptation. Runtime success alone does not
establish compatibility of an untested engine.

### Read the result

The worker writes run-spec, worker-spec, k6-summary, worker-execution and
worker-summary JSON files into RESULT_DIR. Logs contain each record once, using
`PROXY_BENCH_FIXED_POOL_` followed by `RUN_SPEC_V1`, `WORKER_SPEC_V1`,
`K6_SUMMARY_V1`, `WORKER_EXIT_V1` or `WORKER_RESULT_V1`, then a space and JSON.
An input/output error may additionally use `ERROR_V1`. Schemas are in `schemas/`.

The readable metric rows and JSON use the same native k6 values. QPS includes
failed requests; success means k6's expected status 206 with no transport failure.
No request-body hash or per-request length check is added. Known failed requests
still consume their plan positions. Only a complete normal execution with
consistent counters establishes exact logical coverage. Interrupted or uncertain
runs retain metrics and use null coverage with a reason.

Exit 0 means the action produced its required results, not that capacity or
service-quality criteria passed. Input errors exit 2, execution/publication
failures 3, missing/invalid required results 4, and handled interruption 130;
priority is 130, 2, 3, 4, 0. All errors and the raw k6 exit code are retained.
The fixed-pool script has no built-in performance thresholds. SIGKILL, OOM or
node loss can prevent final records; obtain Pod status separately.

## Run independent workers

Prepare a plan with `--workers W`, then copy the existing Job template once per
worker. Each Job must have a distinct name, `WORKER_ID` in `0..W-1`, a separate
writable result volume, and the same RUN_ID, total RATE, DURATION, target and
byte-identical common inputs. Keep `backoffLimit: 0` and `restartPolicy: Never`.
Do not use Job parallelism to create copies with the default worker ID 0.

For two equal shards and RATE=21, set **RATE=21 in both Jobs**. Their effective
worker-spec rates are 11 and 10, respectively. Each worker loops only its own
shard. It never waits for, restarts or compensates for another worker.
Metadata may be shared read-only; node-local RWO volumes require compatible
placement. No object bodies need to be copied to a worker.

Collect each Job's logs and Pod status separately. Check that worker IDs are
complete and unique, run-spec snapshots agree, and actual execution intervals
overlap as intended. Same RUN_ID and DURATION alone do not establish overlap.
Missing, duplicate, mixed-run or truncated records must not be treated as a
complete multi-worker experiment. A rerun uses a new RUN_ID for all workers.

With W>1, top-level `aggregate` and `coverage` are null. Read this worker's
values from `workers[0].metrics` and `workers[0].coverage`; its coverage ratio
uses its own shard size. `result_complete` describes this worker only. Preserve
each worker's native QPS, P99/P50, actual duration and exit facts. There is no
cross-worker report or summarize command. An operator may choose a separate
aggregation method; averaging per-worker percentiles does not yield an overall
percentile, and a missing worker is not a zero-valued worker.

## Controlled execution regression

For execution triggers, prerequisites, scope and retained results, see
[the regression guide](tests/README.md). Run both suites before merging fixed-pool
changes or submitting a phase for acceptance. These are functional checks, not a
capacity benchmark or a replacement for real Seed and Kubernetes validation.

With the preparation binaries, worker-support and the Dockerfile's k6 on PATH:

```bash
python3 tools/proxy-bench/fixed-pool/tests/integration.py --work-dir /tmp/fixed-pool-regression-new
```

The independent-process regression uses the same prerequisites and preserves
two/three-worker request records, native results and fault cases:

```bash
python3 tools/proxy-bench/fixed-pool/tests/multi_worker.py --work-dir /tmp/fixed-pool-multi-new
```

It checks loops, uneven shards and integer rates, invalid input, interruption,
missing summaries, dropped iterations and intentionally staggered starts. Test
copies label requests with worker/iteration headers; production scripts do not.

This retains normal, transparent transport resend, wrong-status, redirect, truncated-body, timeout, mixed,
same-length wrong-body, dropped, dynamic-VU, interrupted, threshold-failed and
script-abort runs, plus mismatched-input and incomplete-result cases. Numbering
checks use an explicitly instrumented **test copy** with an iteration header;
the production script has no per-request trace or iteration tag. The gap case
pauses between acquiring an iteration number and incrementing the Counter.
The controlled proxy implements preparation headers for protocol tests only;
it is not a substitute for validation against a real Seed.
