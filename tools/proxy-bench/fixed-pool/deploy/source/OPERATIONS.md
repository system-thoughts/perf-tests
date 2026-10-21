# Fixed-pool source: deployment walkthrough

Start with [the source overview](README.md) for capabilities, prerequisites and
configuration. Run these steps from this directory in one shell session. Fill
only the environment-specific inputs; the Python snippets check or read those
inputs and actual results, rather than generating replacement evidence.

All steps apply to a new dataset, except the explicitly optional new-PVC step.
For an existing unchanged source, use the reuse/recovery rules in the overview.

## 1. Copy and edit the configuration

The copy is a template, not a ready-to-run environment. Fill images, node, PVC,
final BASE_URL and B/N before loading it. Review the file as trusted shell input.

```sh
set -eu
cp -n source.env.example source.env
# Edit source.env before continuing: images, node, PVC and experiment settings.
```

## 2. Load settings, check them, then render

`set -a` exports the loaded settings for envsubst and Python. The BASE_URL default
is applied once; it does not provision a network endpoint. Confirm the resulting
address is the one that the Seed will use.

```sh
set -a
. ./source.env
set +a
: "${DNS_DOMAIN:=cluster.local}"
: "${BASE_URL:=http://${SOURCE_NAME}.${NAMESPACE}.svc.${DNS_DOMAIN}}"
export DNS_DOMAIN BASE_URL
```

This check rejects missing values, mutable image references and nonpositive B/N
or generation concurrency before any cluster resources are created. Correct
source.env when it fails; do not bypass the check.

```sh
python3 - <<'PY'
import os, re
required = '''SOURCE_NAME NAMESPACE PVC_NAME TOOLS_IMAGE FILE_SERVER_IMAGE
SOURCE_NODE B N GENERATION_WORKERS DATA_UID DATA_GID FS_GROUP DATA_CPU_REQUEST
DATA_CPU_LIMIT DATA_MEMORY_REQUEST DATA_MEMORY_LIMIT SERVER_CPU_REQUEST
SERVER_CPU_LIMIT SERVER_MEMORY_REQUEST SERVER_MEMORY_LIMIT'''.split()
for name in required:
    if not os.environ.get(name):
        raise SystemExit('missing configuration: ' + name)
for name in ['TOOLS_IMAGE', 'FILE_SERVER_IMAGE']:
    if not re.fullmatch(r'[^\s@]+@sha256:[a-f0-9]{64}', os.environ[name]):
        raise SystemExit(name + ' must be repository@sha256:...')
for name in ['B', 'N', 'GENERATION_WORKERS']:
    if not re.fullmatch(r'[0-9]+', os.environ[name]) or int(os.environ[name]) == 0:
        raise SystemExit(name + ' must be a positive decimal integer')
print('Final BASE_URL=' + os.environ['BASE_URL'])
PY
```

The whitelist names the variables envsubst may replace. Rendering writes local
Job and server YAML to a new RENDERED directory, then checks for unexpanded
variables. The final commands check the namespace/node and ask the Kubernetes
API to validate the resources without creating the Job or server.

```sh
RENDERED="$(mktemp -d "${TMPDIR:-/tmp}/fixed-pool-source.XXXXXX")"
export RENDERED
VARS='${SOURCE_NAME} ${NAMESPACE} ${BASE_URL} ${B} ${N} ${GENERATION_WORKERS}
${PVC_NAME} ${PVC_STORAGE_CLASS} ${PVC_CAPACITY} ${TOOLS_IMAGE} ${FILE_SERVER_IMAGE}
${SOURCE_NODE} ${DATA_UID} ${DATA_GID} ${FS_GROUP} ${DATA_CPU_REQUEST}
${DATA_CPU_LIMIT} ${DATA_MEMORY_REQUEST} ${DATA_MEMORY_LIMIT}
${SERVER_CPU_REQUEST} ${SERVER_CPU_LIMIT} ${SERVER_MEMORY_REQUEST} ${SERVER_MEMORY_LIMIT}'
envsubst "$VARS" < dataset-job.tpl.yaml > "$RENDERED/dataset-job.yaml"
envsubst "$VARS" < file-server.tpl.yaml > "$RENDERED/file-server.yaml"
python3 - "$RENDERED" <<'PY'
import pathlib, re, sys
for path in pathlib.Path(sys.argv[1]).glob('*.yaml'):
    if re.search(r'\$\{[A-Z_]+\}', path.read_text()):
        raise SystemExit('unsubstituted variable in ' + str(path))
PY
cp source.env "$RENDERED/source.env"
kubectl get namespace "$NAMESPACE"
kubectl get node "$SOURCE_NODE"
kubectl apply --dry-run=server -f "$RENDERED/dataset-job.yaml"
kubectl apply --dry-run=server -f "$RENDERED/file-server.yaml"
```

## 3. Prepare the PVC

For a new PVC only, fill PVC_STORAGE_CLASS/PVC_CAPACITY, inspect the rendered
resource and run this block. Skip it for an existing PVC: do not apply the
optional example over that volume.

```sh
: "${PVC_CAPACITY:?set the new PVC capacity}"
envsubst "$VARS" < pvc.example.yaml > "$RENDERED/pvc.yaml"
kubectl apply --dry-run=server -f "$RENDERED/pvc.yaml"
kubectl apply -f "$RENDERED/pvc.yaml"
```

Save the actual PVC configuration and inspect capacity, access mode, node affinity
and permissions. Generation runs with the configured UID/GID. A
WaitForFirstConsumer claim may bind only when the Job is scheduled.

```sh
kubectl get pvc -n "$NAMESPACE" "$PVC_NAME" -o yaml > "$RENDERED/pvc-before.yaml"
```

## 4. Generate data and preserve the actual result

Apply only the data Job. It directly invokes proxy-bench-data-prepare on the final
PVC; it does not start an HTTP server. Stop on failure and keep the Job/Pod facts,
logs and residual files. Deleting a failed Job does not empty its dataset.

```sh
kubectl apply -f "$RENDERED/dataset-job.yaml"
if ! python3 - "$NAMESPACE" "$SOURCE_NAME" <<'PY'
import json, subprocess, sys, time
namespace, source = sys.argv[1:]
job = source + '-data'
deadline = time.monotonic() + 1800
while time.monotonic() < deadline:
    result = subprocess.run(['kubectl', '-n', namespace, 'get', 'job', job, '-o', 'json'],
                            capture_output=True, text=True, timeout=30)
    if result.returncode:
        raise SystemExit(result.stderr)
    status = json.loads(result.stdout).get('status', {})
    failed = [c for c in status.get('conditions', [])
              if c['type'] in ('Failed', 'FailureTarget') and c['status'] == 'True']
    if status.get('failed', 0) or failed:
        raise SystemExit(f'Generation Job {namespace}/{job} failed: {failed}; inspect Pod logs/events')
    if status.get('succeeded', 0) == 1:
        print(f'Generation Job {namespace}/{job} succeeded')
        break
    time.sleep(2)
else:
    raise SystemExit(f'Generation Job {namespace}/{job} timed out; inspect Pod scheduling/events')
PY
then
  kubectl get job,pods -n "$NAMESPACE" -o wide
  kubectl describe job -n "$NAMESPACE" "${SOURCE_NAME}-data" > "$RENDERED/failed-job.txt" 2>&1 || true
  kubectl logs -n "$NAMESPACE" "job/${SOURCE_NAME}-data" > "$RENDERED/failed-generation.log" 2>&1 || true
  exit 1
fi
```

The wait observes failure as well as success: with backoffLimit=0 it stops when
the first failed Pod is reported, rather than waiting 30 minutes for Complete.
An unschedulable or image-pull-blocked Pod can still reach the deadline; use Pod
events to locate those infrastructure failures. Save Job/Pod facts and require
one successful generation Pod with container exit
0. More than one actual Pod requires inspection, not selection of a "latest"
result. Read the log from that Pod and keep it unchanged.

```sh
kubectl get job -n "$NAMESPACE" "${SOURCE_NAME}-data" -o json > "$RENDERED/job.json"
kubectl get pods -n "$NAMESPACE" -l "batch.kubernetes.io/job-name=${SOURCE_NAME}-data" \
  -o json > "$RENDERED/generation-pods.json"
GEN_POD="$(python3 - "$RENDERED/generation-pods.json" <<'PY'
import json, sys
items = json.load(open(sys.argv[1]))['items']
assert len(items) == 1, 'inspect multiple actual generation Pods before proceeding'
p = items[0]
assert p['status']['phase'] == 'Succeeded'
assert p['status']['containerStatuses'][0]['state']['terminated']['exitCode'] == 0
print(p['metadata']['name'])
PY
)"
kubectl logs -n "$NAMESPACE" "$GEN_POD" > "$RENDERED/generation.log"
```

Kubectl logs merges stdout and stderr. The next snippet locates exactly one
original data-result JSON line, checks success, and saves its original bytes as
dataset-result.json. It does not manufacture a manifest reference from a directory
scan or treat diagnostic text as JSON.

```sh
python3 - "$RENDERED" <<'PY'
import json, pathlib, sys
r = pathlib.Path(sys.argv[1])
# kubectl logs combines stdout and stderr. Keep the complete log, and extract
# exactly one original JSON result line without treating diagnostics as JSON.
matches = []
for line in (r/'generation.log').read_bytes().splitlines(keepends=True):
    try:
        value = json.loads(line)
    except (ValueError, UnicodeDecodeError):
        continue
    if isinstance(value, dict) and 'generated_objects' in value and 'manifest' in value:
        matches.append((line, value))
assert len(matches) == 1
line, result = matches[0]
assert result['schema_version'] == 1 and result['errors'] == [] and result['manifest']
(r/'dataset-result.json').write_bytes(line)
PY
```

Only after preserving those facts, delete the completed Job to release its volume
mount before starting the server. The PVC and dataset remain.

```sh
# Release the completed Job's volume before starting the server. The PVC stays.
kubectl delete job -n "$NAMESPACE" "${SOURCE_NAME}-data" --wait=true
```

Controller re-execution is still possible. Generator locking and conflict checks
protect the dataset; do not assume a Job ran exactly once or adopt residual data.

## 5. Start the server and export the manifest

The server reads the same dataset through the read-only mounts described in the
overview. Save its actual Pod/imageID. The supplied FileServer must serve the
documented root and port. Pod readiness is deployment status; the subsequent
Seed preparation validates the objects it reads.

```sh
kubectl apply -f "$RENDERED/file-server.yaml"
kubectl rollout status -n "$NAMESPACE" "statefulset/${SOURCE_NAME}" --timeout=5m
SERVER_POD="${SOURCE_NAME}-0"
kubectl get pod -n "$NAMESPACE" "$SERVER_POD" -o json > "$RENDERED/server-pod.json"
```

Read the manifest ID from the actual successful Job result. The generation path
is /data/dataset/manifests/ID/dataset-manifest.json; the server sees the same file
at /fixed-pool-data/manifests/ID/dataset-manifest.json. Export it unchanged and
compare its raw SHA-256 to the generation result before handing it to other tools.

```sh
MANIFEST_ID="$(python3 - "$RENDERED/dataset-result.json" <<'PY'
import json, re, sys
r = json.load(open(sys.argv[1]))
digest = r['manifest']['sha256']
assert re.fullmatch('[a-f0-9]{64}', digest)
assert r['manifest']['path'] == '/data/dataset/manifests/' + digest + '/dataset-manifest.json'
print(digest)
PY
)"
kubectl exec -n "$NAMESPACE" "$SERVER_POD" -- \
  cat "/fixed-pool-data/manifests/$MANIFEST_ID/dataset-manifest.json" \
  > "$RENDERED/dataset-manifest.json"
printf '%s  %s\n' "$MANIFEST_ID" "$RENDERED/dataset-manifest.json" | sha256sum -c -
```

## 6. Continue with Seed preparation

Provision the previously selected external endpoint if applicable, then pass the
exported manifest to [Seed preparation](../../README.md#prepare-a-target-seed-cache).
There is no separate source-body download or manual HTTP verification step.
The command validates full-pool lengths, digests and target completion, streams
bodies without storing another copy, and records per-object errors.

For failures, inspect target-preparation.json and target-object-results.json:

| Failure | Where to look next |
| --- | --- |
| Proxy connection or request timeout | target.proxy, Seed process, Seed-to-origin DNS/routing and Seed logs. |
| full_get HTTP status failure | Object URL, nginx/Seed logs, Service selectors and PVC mounts. |
| full_get length or digest mismatch | Manifest entry, mounted dataset and received_bytes/SHA256. |
| Wrong server IP, task identity or completion marker | Target configuration, actual Seed instance and its task state. |

An error reports the observed boundary, not a root cause inferred from an HTTP
status. Preserve failed output and retry into a new preparation directory after
repair. Source repair does not rerun successful generation. Keep configuration,
resources, Pod/image facts, generation logs and manifest with the dataset.
