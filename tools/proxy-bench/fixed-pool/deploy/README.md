# Fixed-pool Kubernetes examples

Read the [complete workflow](../README.md#one-complete-kubernetes-path) first.
These are ordinary editable YAML resources, not templates or an orchestrator.

| File | Use |
| --- | --- |
| storage.yaml | Optional source and metadata PVCs |
| data.yaml | Generate the selected dataset on the source volume |
| file-server.yaml | Serve only generated object bodies with existing Nginx |
| plan.yaml | Generate a plan from an explicitly provided manifest file |
| seed.yaml | Shared target ConfigMap and independent Seed-preparation Job |
| worker.yaml | Consume valid inputs and run one worker |

Use kubectl -n for namespace selection; placement is optional. Edit storage, source/target
and workload values. Published candidate bundles fill matching image digests;
repository examples require replacing image placeholders.
Provide the manifest at the configured input path using your own file-transfer or
mounting method. Neither the source workload nor a preparation Job downloads or
selects it. The two preparation Jobs are independent. Apply resources in the
workflow's order rather than applying this whole directory and assuming ordering.

Use one preparer per location. Owned outputs must not exist; unrelated files are
allowed. Job deletion does not remove outputs from a PVC. stdout/stderr go to Pod
logs, while necessary artifacts remain on their
volumes. Save logs only when needed. Existing resource names are one-deployment
examples; additional deployments use operator-selected names or namespaces.
