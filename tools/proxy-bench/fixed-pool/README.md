# Fixed-pool proxy benchmark

Measure a Dragonfly Seed with a deterministic object pool and a reusable Range
access plan. Each worker visits its assigned regions in a fixed order, loops the
same shard and uses k6's constant arrival rate. Reuse the inputs for repeated
rounds or change only RATE to offer more load. This tool does not search for a
maximum QPS, clear host caches, manage experiments or decide whether results meet
an application's latency target.

## Deliverables and prerequisites

| Deliverable | Responsibility |
| --- | --- |
| proxy-bench-tools image | `proxy-bench-prepare data`, `plan`, `seed-cache` |
| proxy-bench image | Original modes and the fixed-pool worker entrypoint |
| Existing dragonflyoss/file-server image | Nginx HTTP object source, including HEAD and Range |
| [deploy/](deploy/README.md) | Ordinary editable Kubernetes examples; no env files or rendering helper |

Use tools, worker and YAML from the same build. The fork's development workflow
publishes the first two images to GHCR and supplies a matching deploy bundle;
`build-info.txt` lists the image digests. The file-server image remains the reused
Docker Hub image. Development branches have separate build-specific releases.
These examples do not change upstream's release process.

For the published k6 baseline, select the matching assets from
[fixed_pool_refactor-59c90682dad6](https://github.com/system-thoughts/perf-tests/releases/tag/fixed_pool_refactor-59c90682dad6).
A step-by-step [Chinese VM walkthrough](https://gitcode.com/lvying6/dragonfly-install/blob/fixed_pool_refactor/Nydus全热场景benchamrk用户指南/VM入门-跑通一次完整测试.md)
explains the inputs, YAML edits, manifest handoff, results and repeated runs.
Documentation-only commits after that release do not require rebuilding its images.

The main path needs kubectl, usable storage and an existing Dragonfly Seed HTTP
proxy. No Go installation or host preparation executable is required. The source
URL must be reachable from the Seed, and the preparation/worker Pods must reach
that proxy. Keep enough Seed capacity and retention for the entire pool; ready
records do not monitor subsequent eviction, restarts or external changes.

## One complete Kubernetes path

Work on local copies of the six deploy YAML files. They use namespace `benchmark`,
source hostname `source-node`, load hostname `load-node`, two sample PVC names and
small functional settings: B=4 MiB, N=2, R=64 KiB, W=1, RATE=10 and DURATION=30s.
These are examples, not performance recommendations. Edit the following first:

- Replace image placeholders with the matching tools/worker image references.
- Set namespace, node names, storage capacity/class and any existing PVC references.
- Set the final source URL in data.yaml. `cluster.local` is an example DNS domain;
  an external Seed may need an independently provided NodePort or other endpoint.
- Set the proxy and expected server IP once in seed.yaml's target ConfigMap.
  The former is the connection address; the latter checks the responding Seed.
- Choose data size/count, Range size, shard count, rate and round identifier.
  B must be divisible by R. Review worker resources for the intended load.

### 1. Generate objects on the source volume

Create the namespace if needed. Apply storage.yaml only when creating new PVCs;
with existing storage, edit the Job/server references instead. An omitted
storageClassName uses the cluster default; `""` explicitly requests no class.

```bash
kubectl create namespace benchmark
kubectl apply -f deploy/storage.yaml
kubectl apply -f deploy/data.yaml
kubectl -n benchmark get job fixed-pool-data --watch
kubectl -n benchmark logs job/fixed-pool-data
```

For each watch, press Ctrl+C after Complete or Failed, then fetch the log.
Do not continue a failed action. Watching first also avoids requesting logs while
the container is still starting. Continue after the Job succeeds. Do not only wait for Complete while ignoring a
Failed condition; inspect Pod events for scheduling, image and mount failures.
The command prints its JSON result and diagnostics to the Pod log. Objects and
`dataset-manifest.json` are generated under `/data/dataset` on the source PVC.
No reports directory or separate stdout report is created.

### 2. Publish the object source

```bash
kubectl apply -f deploy/file-server.yaml
kubectl -n benchmark rollout status statefulset/fixed-pool-source
```

Only `dataset/objects` is mounted under Nginx's HTTP root. The manifest is not
published over HTTP or copied by this workload. A server restart does not generate
objects. Recreate the server Pod after replacing its mounted dataset directory;
repeated rounds and changes to R/W/RATE do not require it. Pod Ready does not
certify that the source is reachable from the Seed.

### 3. Provide the chosen manifest as an input file

Obtain the manifest at the generation result's path and put it in storage readable
by plan, Seed preparation and worker. The examples read
`/inputs/dataset-manifest.json` from the metadata PVC. Change those paths/mounts
when using another location. The tool does not fetch, distribute or select files.

For example, an operator can mount the source PVC in a temporary Pod, copy the
manifest to the workstation, then mount the metadata PVC and copy it there.
Use an existing storage access method if available. A minimal optional access Pod
is shown below; it is an environment operation, not an additional delivered tool.
Set its image to the same tools image, node to the volume's node and claim to the
volume to access. It mounts the selected volume at `/files`.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: fixed-pool-files
  namespace: benchmark
spec:
  restartPolicy: Never
  automountServiceAccountToken: false
  nodeSelector:
    kubernetes.io/hostname: source-node
  securityContext:
    runAsNonRoot: true
    runAsUser: 1000
    runAsGroup: 1000
    fsGroup: 1000
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: files
      image: REPLACE_WITH_TOOLS_IMAGE
      command: ["/bin/sleep", "3600"]
      securityContext:
        readOnlyRootFilesystem: true
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
      volumeMounts:
        - name: files
          mountPath: /files
  volumes:
    - name: files
      persistentVolumeClaim:
        claimName: fixed-pool-source
```

Save it as access.yaml. First use the source volume:

```bash
kubectl apply -f access.yaml
kubectl -n benchmark wait --for=condition=Ready pod/fixed-pool-files --timeout=120s
kubectl -n benchmark cp fixed-pool-files:/files/dataset/dataset-manifest.json ./dataset-manifest.json
kubectl -n benchmark delete pod fixed-pool-files --wait=true
```

Edit access.yaml's claim to `fixed-pool-inputs` and node to the metadata volume's
node, then upload the selected file. No object bodies pass through this path.

```bash
kubectl apply -f access.yaml
kubectl -n benchmark wait --for=condition=Ready pod/fixed-pool-files --timeout=120s
kubectl -n benchmark cp --no-preserve ./dataset-manifest.json fixed-pool-files:/files/dataset-manifest.json
kubectl -n benchmark delete pod fixed-pool-files --wait=true
```

This example has real copy and temporary-Pod costs, including on a single worker.
It is not a requirement to use this transfer method or directory layout.

### 4. Generate the access plan and prepare the Seed independently

```bash
kubectl apply -f deploy/plan.yaml
kubectl -n benchmark get job fixed-pool-plan --watch
kubectl -n benchmark logs job/fixed-pool-plan
kubectl apply -f deploy/seed.yaml
kubectl -n benchmark get job fixed-pool-seed --watch
kubectl -n benchmark logs job/fixed-pool-seed
```

Plan generation reads the chosen manifest and R/W, then publishes
`/inputs/plan/request-plan.json` and `shards/`. It needs neither source access nor
a ready Seed. Seed preparation does not read the plan. It performs each object's
full GET and verifies size/SHA-256 and responding identity, then an immediate small
Range. Bodies are streamed, not saved by the preparation Pod. Success publishes
`/inputs/preparation/target-preparation.json` with `status=ready` and its object
evidence. This proves the preparation's data checks, not background task quiescence
or the absence of every possible later cold fetch.

Both commands require a new or empty output directory. They neither choose old
results nor overwrite them. Failed preparation records may exist, but cannot be
used as ready input. Fix the reported problem and rerun only the failed action,
with a new/empty output; update the worker's selected path when needed. No default
Seed-cache deletion or plan regeneration accompanies that retry.

### 5. Run and read the native results

```bash
kubectl apply -f deploy/worker.yaml
kubectl -n benchmark get job fixed-pool-worker --watch
kubectl -n benchmark logs job/fixed-pool-worker
```

The worker checks its explicit inputs, then starts k6. Missing, damaged or mismatched
inputs prevent pressure and identify the input/path in the error. Logs identify
the actual B/N, R/W, manifest/plan and target; a valid older dataset is not rejected
because the operator may have intended another one.

The shared proxy-bench text report shows requests/QPS, failure rate, latency
percentiles, throughput and consumed plan items. Four prefixed JSON records retain
run/worker facts, effective engine options, native summary and raw exit. There is
no separate report command or required output volume. Job Complete means the
process completed; it does not mean requests succeeded or the experiment is valid.
No default 1% failure threshold is installed by fixed-pool. k6's own configured
thresholds and exit codes still apply.

Request success is separate from load delivery: 100% successful executed requests
does not mean the actual request rate reached RATE. Dropped iterations were never
started and are not HTTP failures. For concurrency sizing, inspect the full
iteration time as well as connecting time; `http_req_duration` excludes the initial
connection. Capacity acceptance belongs to the operator's test policy.

Save logs if useful, during execution or afterwards. This is optional:

```bash
kubectl -n benchmark logs -f job/fixed-pool-worker > worker.log
```

## Reuse, changes and multiple workers

| Change | What to execute |
| --- | --- |
| Same workload, another round | New RUN_ID; delete/recreate the worker Job, retaining valid inputs |
| Increase RATE | Only change worker rate and round; no new dataset/plan/preparation |
| Change R or W | Independent plan Job with a new/empty output; point worker at that plan |
| Retry failed Seed preparation | Correct the cause, use a new/empty preparation output and rerun only Seed |
| Change dataset B/N or source URLs | Explicit new data and matching plan/preparation; publish the selected source |
| Change actual target | Prepare that target and select its matching evidence |

Default names describe one deployment, not a one-per-namespace tool restriction.
For another set use a different namespace or update resource names, selectors,
volume/config references and source URLs. Updating a ConfigMap does not rerun a
Job; apply the config before creating the execution that needs it. Do not rely on
live configuration changes during a measurement.

For W workers, generate one W-shard plan. Each worker receives the same manifest,
public plan, target and ready evidence, plus its own shard. Use the same total
RATE/RUN_ID and different WORKER_ID values. Storage/path choices and startup/log
collection are the operator's responsibility. Suitable shared storage is optional;
with node-local copies, transfer metadata explicitly, retaining relative shard
and evidence paths. Do not copy source bodies to workers. To copy directory
contents, create the destination parent first and distinguish `dir` from `dir/.`.

Each process independently selects its shard and assigned rate. It does not wait
for peers, compensate for a failed peer or aggregate percentiles. Check actual
execution overlap. Sum counts/rates only over compatible windows; per-worker P99
values cannot be averaged to obtain an overall P99.

## Preparation CLI and optional development

The same tools image contains one executable and three independent actions:

```bash
proxy-bench-prepare data --root /data/dataset --size 4194304 --count 2 \
  --base-url http://source.example
proxy-bench-prepare plan --manifest /inputs/dataset-manifest.json \
  --range 65536 --workers 1 --out /inputs/plan
proxy-bench-prepare seed-cache --manifest /inputs/dataset-manifest.json \
  --target-config /config/target-config.yaml --out /inputs/preparation
```

These are container commands. Select the executable with your container runtime;
mount the required directories. A host binary is optional, not part of the normal
Kubernetes path. `--help` on each action lists its options.

For direct CLI use, see the [target configuration example](examples/target-config.yaml).
The Kubernetes path uses the target ConfigMap in seed.yaml instead.

| Input/control | Default or requirement |
| --- | --- |
| data root/size/count/base-url | Required; positive decimal bytes/count, new/empty root |
| generation-workers | 1, limiting preparation resource use; change if appropriate |
| plan manifest/range/out | Required file, positive decimal Range bytes, new/empty directory |
| plan workers | 1; 1..M shards, B%R=0 |
| memory-budget/file-budget | Omitted: no optional budget; explicit value must be positive bytes |
| target proxy/expected_server_ip | Required; no assumed target or component version |
| target ref | Optional display name, does not bind readiness |
| prepare concurrency | 1; avoids assuming target preparation capacity |
| request_timeout_ms/ready_timeout_ms | 1800000/60000; bounded full request and immediate Range check budget |

A plan contains a public descriptor plus shards; no URL-list copy or standalone
JSON Schema files are required. Workers obtain URLs from the manifest. Necessary
format, digest, local-shard and ready checks remain in the Go consumers. Budgets
are estimates for plan construction at its execution location, not checks of
future workers or a replacement for Kubernetes resource limits.

Developers may use `make build-proxy-bench-tools` and the two Dockerfiles in the
parent directory. Unit tests use standard Go testing via `make test-race`.
Integration regressions live in the separate
[fixed-pool test repository](https://github.com/system-thoughts/perf-tests-fixed-pool-tests).
They consume built images and matching deploy YAML, and are not a user preflight.
