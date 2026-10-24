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

## Download a CI build

The **Fixed-pool regression** workflow runs for relevant branch pushes and pull
requests. After the tests pass, it uploads `fixed-pool-<commit SHA>` under the
workflow run's **Artifacts** section. A failed build does not upload a distribution;
regression diagnostics are retained separately. Artifacts expire after 14 days.

Download and unzip that artifact, then verify its contents:

```bash
sha256sum -c SHA256SUMS
```

The distribution contains Linux amd64/arm64 tool archives, `proxy-bench.oci.tar`
(the load-generator image), `proxy-bench-tools.oci.tar` (preparation commands),
source, build metadata and the license. Choose the tool archive for your machine;
tar preserves executable permissions. OCI archives contain both architectures and
must be imported or published through your container runtime before use. They are
not registry URLs. The FileServer image remains a separate deployment input.

`build-info.json` identifies the source commit, workflow run, Go toolchain and k6
base image. The two image metadata files contain image digests. Source archives
include maintainer tests; neither runtime nor preparation images contain them.
These are build artifacts, not an automatic GitHub Release, registry publication
or deployment. Cross-building an ARM image does not establish ARM native results.

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
body and Range evidence; record software versions separately in experiment
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
same task. After all full reads, a second pool-wide pass checks every object's
Range response again. Neither pass polls task state or repeats the full-object download.
Different objects must not unexpectedly share a task.

Ready means verified cache data, not completion of the original request's internal
bookkeeping. `X-Dragonfly-Task-Download-Finished` snapshots state at request start;
it can be absent after the full body has arrived and is not a readiness gate.
A Range-only download can also set task.finished_at while other pieces are missing.
Neither marker replaces full-object verification or proves background activity ended.

The command streams bodies without saving a second copy. A recoverable Range
transport failure (such as a reset connection or truncated response) gets at
most three attempts, including the first, with 100 ms and 500 ms backoffs.
Each Range check shares one `ready_timeout_ms` budget across requests, body
reads and backoffs; the default is 60 seconds total, not per attempt. A request
that consumes that budget gets no further attempt. The same URL, Range, task
and target checks apply on every attempt. Other preparation workers continue
while one worker retries. Attempt failures and recovery are written to stderr.
HTTP status, identity, range and content errors are terminal. Full GETs are
never automatically retried.

After a terminal failure or exhausted Range recovery, stop new object dispatch
and let already started requests finish within their configured timeouts.
Evidence distinguishes the actual failing object from objects not dispatched
because of that failure. Interruption cancels requests and backoff waits.
Retry the command explicitly with a new output directory/preparation ID;
cached Seed data is not deleted. Preparation retries do not change the worker's
no-business-retry measurement policy.

`target-object-results.json` contains every object, known task/IP/received bytes,
actual digest and timestamps, status and reason. The historical field
`finished_check_at` records this tool's successful Range check, not the Seed's
task completion time. Unknown fields are null.
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
summary and referenced evidence unchanged. The historical method identifier
`http-full-body-and-finished-v1` is retained for compatibility; it does not
require a task-finished header. Record the tools image digest/source revision
to identify the implementation. The record shape and worker method check are
unchanged, so this fix does not require a paired worker upgrade. Existing ready
records remain usable when their bindings and target conditions remain valid.
Failed/unverified records cannot be promoted or edited into ready. Full body
and Range checks belong to `proxy-bench-seed-cache-prepare`; regressions protect
that producer behavior.

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
uses a read-only input PVC, no service-account token, no elevated capabilities,
no restart and no Job-level retry. The container needs a writable temporary
directory for its private k6 input; no result volume is mounted. Preserve the
Job logs before deleting it.

Each iteration calls `http.get` once, with no business-layer retry or redirect.
Connection reuse remains enabled. The native k6/Go HTTP transport can transparently
resend an idempotent GET when a reused connection fails before response headers.
This uses the same URL and Range and does not consume another plan item. Native
`http_reqs`, QPS and duration retain their original semantics; physical GET counts
observed at the proxy can differ.

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

Durations must be positive and representable as whole milliseconds. DURATION must
be at least 1s, as required by k6's constant-arrival-rate executor. An explicitly
empty or malformed value is rejected. MODE/RANGE, legacy load knobs, extra
identity overrides and unknown `PROXY_BENCH_*` variables are rejected. Native
`K6_*` options are accepted. No empty k6 configuration file is supplied.
The entry clears environment proxy bypass variables. Native localhost/loopback
exceptions still apply; choose a source hostname that routes through the Seed.
The internal worker-support subcommands run automatically.

k6 applies its native file, script, environment and CLI precedence. Testers own
their local configuration and environment. `WORKER_SPEC_V1` records benchmark
script inputs, while `K6_SUMMARY_V1.effective_options` captures k6's consolidated
and derived test options once in handleSummary. Native null members are preserved;
this snapshot is not a dump of all process settings. If the engine
fails before handleSummary, no snapshot is available. External thresholds can
make k6 exit nonzero; the worker preserves the engine failure and raw exit code.
Multiple scenarios or an execution segment prevent inferring exact coverage.
The original proxy-bench uses the same native configuration layers.

The runtime image selects its k6 dependency in [Dockerfile](../Dockerfile).
Version selection is a build and maintainer-regression concern. The shipped worker
never probes, records or compares k6 versions. Dependency upgrades must pass the
relevant external regressions using the delivered engine.

### Read the result

Collect the Job log with `kubectl logs job/<job-name> > worker.log`. The shared
proxy-bench report displays requests, failures, latency (including P90/P95/P99),
and throughput in the same format as the original modes. The title identifies the
mode. Consumed shows plan entries consumed and this worker's shard size; repeated
accesses and subsequent HTTP failures can be included, so it is not unique or
successful coverage. Without configured
thresholds it displays NOT EVALUATED, not a passing verdict. The log also contains
four JSON records, each prefixed with `PROXY_BENCH_FIXED_POOL_`:

| Record | Contents |
| --- | --- |
| RUN_SPEC_V1 | Run identity, shared input references, target and rate allocation |
| WORKER_SPEC_V1 | Worker ID, shard size, Range size and script inputs |
| K6_SUMMARY_V1 | Effective options and the unmodified native summary |
| WORKER_EXIT_V1 | Local start attempt/end times, raw child exit and termination reason |

Input errors can also use `ERROR_V1`. No result directory, result volume or
five-file archive is required. A small private k6 input is written under TMPDIR
(default `/tmp`) and cleaned on exit. Both worker Job examples mount a temporary emptyDir at `/tmp`, so the private
input also works with a read-only root filesystem, enabled by default in both
examples. This is not a result volume.
The temporary directory must be writable.
SIGKILL/OOM can prevent cleanup or final log records; obtain Pod status separately.

Read native `http_reqs` count/rate, `http_req_duration` percentiles,
`http_req_failed`, `dropped_iterations`, `plan_consumed` and actual duration from
`K6_SUMMARY_V1.native_summary`. There is no second metric model or completeness
flag. Missing values retain their native representation; missing logs are not zero.
QPS includes failures; expected status is 206, with no per-request body validation.

For a normal exit with complete logs, one unsegmented fixed-pool scenario and
matching plan_consumed/iterations/http_reqs counts, unique logical regions equal
`min(plan_consumed.count, WORKER_SPEC_V1.region_count)`. Multiply by range_bytes
for logical bytes; divide by region_count for this shard's ratio. Interrupted,
segmented or inconsistent runs cannot establish exact coverage. Coverage does not
prove successful bodies, cache residency or physical I/O.

Before k6 starts, input errors exit 2 and internal I/O errors exit 3. After startup,
the entry returns the native k6 code, including threshold failure 99 and command
startup failures 126/127. A handled INT/TERM is forwarded to the child and returns
130; the exit log retains the actual child code. Exit 0 does not establish log
completeness, capacity or service quality. No performance thresholds are added.

## Run independent workers

Prepare a plan with `--workers W`, then copy the existing Job template once per
worker. Each Job must have a distinct name, `WORKER_ID` in `0..W-1`, a writable
temporary directory, and the same RUN_ID, total RATE, DURATION, target and
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

Preserve each worker's native request rate, percentiles, duration and exit facts.
There is no cross-worker report or summarize command. An operator may choose a
separate aggregation method; averaging per-worker percentiles does not yield an
overall percentile, and a missing worker is not a zero-valued worker.

## Maintainer tests

Run `make test-race` from the repository root after changing Go code. The unit
tests cover deterministic objects, region/Range mapping, permutation, sharding,
rate allocation, preparation responses/readiness and required input bindings.
They use Go's standard testing package and require no Dragonfly cluster.

Integration/E2E tests are maintained in
[perf-tests-fixed-pool-tests](https://github.com/system-thoughts/perf-tests-fixed-pool-tests).
They consume built images and matching deployment material. Its README lists the
cases, prerequisites, execution commands and retained evidence. Run the relevant
cases after changing the source bundle, preparation, worker or image dependencies;
run the relevant full set before accepting a candidate. Controlled process tests
observe real k6 requests and lifecycle behavior; Kubernetes cases use the images
with a real source and Seed. These checks establish functionality, not capacity.
