# Fixed-pool HTTP source

Publish a generated dataset as an HTTP source that a Dragonfly Seed can download.
The dataset is generated on a PVC, then served read-only by an independent nginx
FileServer. Generate it once and reuse the same source across test rounds.

This directory provides Kubernetes deployment materials. It does not introduce a
new deployment command or install Kubernetes/Dragonfly. The existing
`proxy-bench-prepare data` command runs inside the data Job.

## What this bundle provides

| File | Purpose |
|---|---|
| [source.env.example](source.env.example) | Shared settings for the dataset, source address, images and storage. |
| [dataset-job.tpl.yaml](dataset-job.tpl.yaml) | Generate objects and their manifest on the final PVC. |
| [file-server.tpl.yaml](file-server.tpl.yaml) | Serve the objects through an independent StatefulSet and Service. |
| [pvc.example.yaml](pvc.example.yaml) | Optional PVC for a new dataset; skip when using an existing PVC. |

The output is a reachable HTTP source and an exported `dataset-manifest.json`.
Pass that manifest to the separate plan and Seed-cache preparation commands.
This bundle does not run a benchmark or establish that a Seed is ready.

## Before starting

Provide a working cluster, a namespace, a source node and suitable storage.
The operator terminal needs kubectl, a POSIX shell, envsubst, Python 3 and
sha256sum. The tools themselves are Go binaries; Python is used only by the
operator-side configuration, Job status and result handoff in the walkthrough.

Provide two immutable image references that the source node can obtain:

- **TOOLS_IMAGE:** the preparation binary with three independent actions, built with the
  [preparation tools Dockerfile](../../../Dockerfile.tools).
- **FILE_SERVER_IMAGE:** nginx serving `/usr/share/nginx/html` on port 80, with
  HEAD and Range support. The existing
  [FileServer image](../../../../file-server/Dockerfile) provides this capability.

See [build instructions](../../README.md#deliverables-and-entrypoints). Building a local OCI archive
does not distribute or publish it; arrange image delivery before deploying.

## Configure this dataset

From this directory, copy `source.env.example` to `source.env`, then edit it.
The [deployment walkthrough](OPERATIONS.md) gives the commands in execution order.

| Setting | What to provide |
|---|---|
| SOURCE_NAME / NAMESPACE | An independent source name and an existing namespace. |
| SOURCE_NODE | The actual source node; there is no usable default. |
| TOOLS_IMAGE / FILE_SERVER_IMAGE | Actual `repository@sha256:...` references, not mutable tags. |
| PVC_NAME | The dataset volume; check capacity, placement and write permissions. |
| BASE_URL | The final HTTP address reachable from the Seed. |
| B / N | Object bytes and count; the small example is 4194304 bytes times four. |
| PVC_STORAGE_CLASS / PVC_CAPACITY | For a new PVC only; select values for your cluster. |

When BASE_URL is empty, the configuration derives
`http://SOURCE_NAME.NAMESPACE.svc.DNS_DOMAIN`. Use this only where the Seed can
resolve and reach that Service address. An external Seed needs a provisioned
endpoint and an explicit BASE_URL; setting a URL does not create an ingress or
port forward. Choose the address before generation: it becomes part of the
immutable manifest.

The example starts with generation concurrency 1, DNS domain `cluster.local`,
UID/GID/fsGroup 1000 and small resource requests/limits. These are functional
starting values to review against your storage and cluster, not capacity-test
defaults. Existing PVC users do not apply the new-PVC example.

## Deploy once per dataset

Follow [OPERATIONS.md](OPERATIONS.md) in one shell session:

| Step | Action | Result to check before continuing |
|---|---|---|
| 1 | Load/check source.env and render the templates. | Final BASE_URL and valid Job/server YAML. |
| 2 | Prepare storage; create a PVC only if needed. | Correct volume and an empty/absent dataset directory. |
| 3 | Run the data Job and preserve its actual result. | Successful Pod, original log and manifest reference. |
| 4 | Start nginx and export the referenced manifest. | Local manifest SHA-256 matches the generation result. |
| 5 | Pass the manifest to Seed preparation. | Every object's length, digest and target/Range checks pass. |

The walkthrough explains the longer configuration and JSON-checking snippets,
including their inputs and outputs. No separate source-body download or manual
HEAD/GET/Range check is required before Seed preparation.

## Interface guarantees

The generation Job and nginx take their PVC name from the same configuration.
Templates fix the generation root, read-only server mounts and object URL paths;
operators do not independently assemble those mappings. Start the server only
after the generation Job succeeds. Export the manifest named by that Job's actual
result and verify its digest; never scan for a "latest" manifest.

The walkthrough stops on generation failure, but these are separately applied
resources, not a controller that prevents manual out-of-order submission. Seed
preparation checks the pool through the selected Seed and reports object IDs,
failure stages and reasons. It streams bodies without saving another copy.
This does not certify an origin that was bypassed by an existing Seed cache.

Maintainers verify the published source's GET/HEAD/Range behavior and deployment
faults with [source regressions](https://github.com/system-thoughts/perf-tests-fixed-pool-tests#kubernetes-cases).
Benchmark users do not run these developer tests as a preparation step.

## Where the data goes

The Job mounts the PVC at `/data` and generates only `dataset/`. The PVC root may
contain `lost+found` or unrelated files; the dataset directory must be empty or
absent. Storage must support sequential mounts by the Job and the server;
ReadWriteOnce does not mean only one Pod, and ReadWriteMany is not required.

| PVC path | Server mount | Use |
|---|---|---|
| dataset/objects | /usr/share/nginx/html/objects, read-only | HTTP object downloads. |
| dataset | /fixed-pool-data, read-only | Export the manifest with kubectl. |

Objects are available as `BASE_URL/objects/sha256/DIGEST`. Other files already in
the FileServer image remain available. Manifests are not published as HTTP files;
export the exact path from the successful generation result, rather than scanning
for a "latest" manifest.

## Reuse and recovery

- Repeated rounds, RATE changes and R/W changes reuse the source bytes and URL.
  R/W changes need a separate access plan, not another data Job.
- B/N changes require a new dataset. Keep the old manifest and objects immutable.
- Server, mount or network failures require repair of the original endpoint.
  Restarting nginx does not regenerate data. Restore the original endpoint.
- Preserve failed generation logs and remnants before choosing a new empty
  dataset or inspecting ownership for manual cleanup. Do not adopt partial data.
- Deleting the completed Job or restarting the server does not delete the PVC.

Retain source.env, rendered resources, Job/Pod facts and imageIDs, generation
logs and the exported manifest. After publication, continue with
[plan preparation](../../README.md#2-generate-an-access-plan)
and [Seed preparation](../../README.md#3-prepare-the-selected-seed).
