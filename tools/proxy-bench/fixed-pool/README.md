# Fixed-pool proxy benchmark

Measure a Dragonfly Seed using deterministic objects and a reusable Range plan.
Each worker loops its own fixed shard with k6's constant arrival rate. Reuse the
inputs for repeat rounds, or change RATE to offer more load. The benchmark does
not find maximum QPS, manage experiments, clear caches or set quality targets.

## Delivery and prerequisites

| Material | Use |
| --- | --- |
| proxy-bench-tools image | `proxy-bench-prepare data`, `plan`, `seed-cache` |
| proxy-bench image | Original modes; `MODE=fixed-pool` selects the new worker |
| Existing dragonflyoss/file-server image | Nginx object source, including HEAD and Range |
| [deploy/](deploy/README.md) | Six ordinary editable Kubernetes YAML examples |

Use tools, worker and YAML from the same build. The fork's separate delivery
workflow supplies a deploy bundle, `build-info.txt` and `SHA256SUMS`; modern bundle
copies have their tools/worker images filled with that build's digests. Repository
examples still have image placeholders. The external FileServer is unchanged.
This development delivery does not change upstream's release process.

**The FP-01–FP-13 candidate described here has not been published or accepted as a
public delivery. Older releases use different inputs; do not run these instructions
with their images.** The [candidate VM walkthrough](https://gitcode.com/lvying6/dragonfly-install/blob/fixed_pool_refactor/Nydus全热场景benchamrk用户指南/VM入门-整改候选流程.md)
separates local rehearsal from the pending public-download check.

The Kubernetes path needs kubectl, usable source/input storage and an existing
Seed proxy. No Go installation or host preparation binary is required. Source
URLs must be reachable from the Seed; preparation and worker Pods must reach that
proxy. Reserve capacity and retention for the pool. A ready record describes a
past successful preparation, not current cache state after eviction or restarts.

## One Kubernetes path

Work on local copies of deploy/*.yaml. Choose a namespace through `kubectl -n`;
resources have no embedded namespace or active node selector. Adapt storage and
optional placement to volume topology. The examples use B=4 MiB, N=2, R=64 KiB,
W=1, RATE=10 and DURATION=30s for function checks, not capacity recommendations.

Set B/N in data.yaml, R/W in plan.yaml, and source/target in seed.yaml. The
source is `source.base_url`; `target.proxy` is the connection endpoint, while
`target.expected_server_ip` checks the actual responding Seed. Role hostnames
must resolve from the caller's network. For an external Seed, edit the source
Service to NodePort or provide another reachable endpoint. Review worker resources.

### 1. Generate objects

Create a namespace, and apply storage.yaml only when new PVCs are needed:

```bash
kubectl create namespace benchmark
kubectl -n benchmark apply -f deploy/storage.yaml
kubectl -n benchmark apply -f deploy/data.yaml
kubectl -n benchmark get job fixed-pool-data --watch
kubectl -n benchmark logs job/fixed-pool-data
```

For each watch below, press Ctrl+C at Complete or Failed, then read the log.
Continue only after success; for a Pod that cannot start, inspect its Events.
Logs show object count/bytes, elapsed time and the manifest path/SHA. Objects and
`dataset-manifest.json` are on the source PVC under `/data/dataset`. No stdout
report file is created. Generation does not need a source URL or running Nginx.

### 2. Publish the source and provide the manifest

```bash
kubectl -n benchmark apply -f deploy/file-server.yaml
kubectl -n benchmark rollout status statefulset/fixed-pool-source
```

Nginx mounts only `dataset/objects`; URLs are
`<source.base_url>/objects/sha256/<object SHA>`. It does not publish the manifest.
Place the selected manifest at `/inputs/dataset-manifest.json` on the input PVC,
or edit consumer paths/mounts. Use your existing storage/file-transfer method;
[the candidate walkthrough](https://gitcode.com/lvying6/dragonfly-install/blob/fixed_pool_refactor/Nydus全热场景benchamrk用户指南/操作附录-配置与核验.md)
shows an optional temporary-Pod copy. Transfer only the manifest, not object bodies.
Recreate the source Pod after replacing its mounted dataset directory. A source
Pod being Ready does not prove that the Seed can reach it.

### 3. Generate the plan and prepare the Seed independently

```bash
kubectl -n benchmark apply -f deploy/plan.yaml
kubectl -n benchmark get job fixed-pool-plan --watch
kubectl -n benchmark logs job/fixed-pool-plan
kubectl -n benchmark apply -f deploy/seed.yaml
kubectl -n benchmark get job fixed-pool-seed --watch
kubectl -n benchmark logs job/fixed-pool-seed
```

Plan reads the manifest and R/W, publishing `plan/request-plan.json` and `shards/`.
It does not access the source or Seed. For 0 < R <= B, each object contributes
floor(B/R) complete regions; leftover bytes are excluded and logged. There are no
short tail requests. RATE and DURATION are not plan inputs.

Seed preparation reads the manifest and target ConfigMap, not the plan. For each
object it streams a full GET, checks size/SHA and responding identity, then reads
an immediate small Range from the same task. Bodies are not written by this Pod.
Only complete success publishes `preparation/target-preparation.json`; no per-object
attachment or failure record is published. Ready does not prove background task
quiescence or that later requests can never fetch external data.

Use one preparer per output location. Existing owned outputs are rejected;
unrelated files are allowed. Reuse by passing valid existing artifacts downstream.
To redo an action, explicitly inspect/clean its outputs or select a new location.
After a failure fix the reported cause and rerun only that action; deleting its
Job does not clean PVCs or Seed cache. Do not edit ready/status or hashes by hand.

### 4. Run and read results

```bash
kubectl -n benchmark apply -f deploy/worker.yaml
kubectl -n benchmark get job fixed-pool-worker --watch
kubectl -n benchmark logs job/fixed-pool-worker
```

A worker reads four files: manifest, public plan, its own shard and the successful
preparation. It gets source/target from that record; no target ConfigMap mount is
needed. Checks reject invalid, damaged or mismatched inputs before starting k6.
The readable startup context identifies actual paths/SHA, B/N/R/W, target and load.

The shared proxy-bench report displays native request rate, failure rate, latency,
throughput and plan consumption. `PROXY_BENCH_FIXED_POOL_K6_SUMMARY_V1` preserves
the complete native summary and effective options;
`PROXY_BENCH_FIXED_POOL_WORKER_EXIT_V1` records k6's raw exit/timestamps/interruption.
There are no RUN_SPEC/WORKER_SPEC output records or required results volume.
Save Pod logs if needed, for example `kubectl -n benchmark logs -f
job/fixed-pool-worker > worker.log` on one shell line.

Job Complete means the process finished, not that requests succeeded or RATE was
delivered. Fixed-pool sets no default failure threshold; configured k6 thresholds
still apply. Dropped iterations never started and are not HTTP failures. Compare
actual QPS to RATE separately from request success. Missing metrics are unknown,
not zero. Check full iteration and connection time when sizing concurrent VUs.

## Reuse and multiple workers

| Change | What to execute |
| --- | --- |
| Repeat, or change RATE/DURATION | Recreate only the worker Job; RUN_ID is optional |
| Change R/W | Generate a new plan and select it in worker; retain data/preparation |
| Change source address | New Seed preparation; unchanged dataset/plan remain reusable |
| Change B/N | New dataset and matching plan/preparation; publish that source |
| Target/cache no longer valid | Prepare the chosen target again; retain plan for unchanged data |

For W workers, provide the same manifest, public plan and preparation, plus each
worker's relative shard. Use the same total RATE and duration, different
WORKER_ID values, and an optional common RUN_ID. The assigned local rates sum to
the total. Input distribution, node placement and overlapping windows belong to
the operator. Failed workers do not stop or receive compensation from healthy
workers. Do not average per-worker P99 into a global P99.

Apply ConfigMap edits before creating the Seed Job. Same-name Jobs must be deleted
and recreated to rerun. Extra deployments use another namespace or consistent
resource/selector/reference changes; no experiment manager is provided.

## Preparation CLI and optional development

These commands run inside the tools image, with the relevant directories mounted:

```bash
proxy-bench-prepare data --root /data/dataset --size 4194304 --count 2
proxy-bench-prepare plan --manifest /inputs/dataset-manifest.json \
  --range 409600 --workers 1 --out /inputs/plan
proxy-bench-prepare seed-cache --manifest /inputs/dataset-manifest.json \
  --target-config /config/target-config.yaml --out /inputs/preparation
```

The [target configuration example](examples/target-config.yaml) is for direct CLI
use; the Kubernetes path edits seed.yaml's ConfigMap instead.

| Parameter | Default or requirement |
| --- | --- |
| data root/size/count | Required; positive decimal bytes/count; no owned outputs present |
| generation-workers | 1; increase for available generation resources |
| plan manifest/range/out | Required; 0 < R <= B; output plan/shards must not exist |
| workers | 1; 1..M, where M=N*floor(B/R) |
| memory-budget/file-budget | Omitted: no optional budget; explicit positive bytes |
| seed manifest/target-config/out | Required; new success-record location |
| concurrency | 1; object preparation parallelism |
| request-timeout-ms/ready-timeout-ms | 1800000/60000; full-request/Range-check budgets |
| worker RATE | Required total req/s; each assigned rate must be positive |
| DURATION/TIMEOUT | 300s/30s; DURATION >= 1s, whole millisecond precision |
| GRACEFUL_STOP | TIMEOUT+5s; must exceed TIMEOUT |
| PRE_ALLOCATED_VUS/MAX_VUS | 64/512; positive, maximum >= initial allocation |
| WORKER_ID/RUN_ID | 0/omitted; select shard/optional observation label |

Budgets concern plan construction where that command runs, not future worker
capacity. Kubernetes memory/PVC limits remain separate. `--help` lists CLI options.
Developers can use `make build-proxy-bench-tools`, the parent Dockerfiles and
`make test-race` (standard Go testing). Integration regressions live in the
[separate test repository](https://github.com/system-thoughts/perf-tests-fixed-pool-tests),
consume built delivery materials and are not a user preflight.
