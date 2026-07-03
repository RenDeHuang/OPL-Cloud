# OPL Console Developer Guide

## Product Truth

OPL Console is the commercial control plane. The current resource model is:

- `ComputeResource`: account-owned TKE node pool capacity for one-person-lab-app. In production it creates or expands a Tencent TKE node pool and records provider node pool ids, selected instance type, billing state, labels, and runtime scheduling metadata.
- `StorageVolume`: account-owned retained PVC/cloud storage.
- `StorageAttachment`: a storage volume mounted to a compute resource at a mount path such as `/data`. In TKE this is where OPL Cloud schedules the one-person-lab-app Deployment/Service onto the selected compute node pool.
- `Workspace`: URL token and WebUI entry composed from an attached compute/storage pair.
- `Wallet` and `Ledger`: billing records reference `computeId`, `storageId`, `attachmentId`, and `workspaceId`.

Workspace is not the only resource body. It is the access entry.

There is no compatibility layer. Active code, contracts, and tests describe only compute, storage, attachment, Workspace URL entry, billing, support, and admin operations.

## Local UI Demo

```bash
npm run demo:api
npm run demo:ui
```

Default demo accounts:

- Lab Owner: `owner@opl.local` / `OplOwnerPass2026!`
- Admin: `admin@opl.local` / `OplAdminPass2026!`

Local demo seeds the current chain: manual top-up, create compute, create storage, attach storage, create Workspace URL, record one sub2api request usage, and create one support ticket.

## Local Console Against Real TKE

Use this when the Console runs locally but provisions cloud resources in TKE:

```bash
export PATH="/path/to/tccli:/path/to/kubectl:$PATH"

OPL_RUNTIME_PROVIDER=tencent-tke \
OPL_WORKSPACE_IMAGE=<tcr>/<namespace>/one-person-lab-app:<tag> \
OPL_WORKSPACE_DOMAIN=<workspace-staging-domain> \
OPL_K8S_NAMESPACE=<namespace> \
OPL_INGRESS_CLASS=<ingress-class> \
OPL_WORKSPACE_STORAGE_CLASS=<storage-class> \
OPL_IMAGE_PULL_SECRET_NAME=<secret> \
TENCENT_DEPLOY_KUBECONFIG_REF=<kubeconfig> \
TENCENT_DEPLOY_CLUSTER_ID=<cls-...> \
TENCENT_TKE_REGION=<region> \
TENCENT_MUTATION_SECRET_ID=<secret-id> \
TENCENT_MUTATION_SECRET_KEY=<secret-key> \
OPL_TKE_NODEPOOL_AUTOSCALING_GROUP_PARA_JSON='{"MinSize":0,"MaxSize":1,"DesiredCapacity":1,"VpcId":"vpc-...","SubnetIds":["subnet-..."]}' \
OPL_TKE_NODEPOOL_LAUNCH_CONFIGURE_PARA_JSON='{"InstanceType":"${INSTANCE_TYPE}","SystemDisk":{"DiskType":"CLOUD_PREMIUM","DiskSize":"${SYSTEM_DISK_GB}"}}' \
npm run demo:api
```

In `tencent-tke` mode, `npm run demo:api` does not reset state unless `OPL_UIUX_DEMO_RESET=1` is explicit. The API calls `GET /api/runtime/readiness` internally before seeding real resources.

TKE node pool templates may use `${INSTANCE_TYPE}`, `${SYSTEM_DISK_GB}`, `${DATA_DISK_GB}`, `${COMPUTE_ID}`, `${ACCOUNT_ID}`, and `${PACKAGE_ID}` placeholders. Keep account ownership labels in the node pool and runtime manifests so cleanup can target only OPL Cloud resources.

## Public Staging E2E

Full commercial e2e must use a public Console URL and a public Workspace URL. Localhost does not count.

Expected chain:

1. Login.
2. Verify or top up wallet balance.
3. Create compute.
4. Create storage.
5. Attach storage to compute.
6. Confirm the TKE node pool is created or expanded and nodes are Ready.
7. Create Workspace URL.
8. Poll runtime status for Deployment, PVC, Service, Ingress, and Endpoints.
9. Open the public Workspace URL and receive HTTP 200 from one-person-lab-app.
10. Record sub2api/request usage.
11. Verify wallet, ledger, usage logs, and runtime evidence.
12. Detach and destroy resources.

Run after staging is configured:

```bash
npm run validate:production-manifest
npm run verify:production
```

## Required Env Vars

- `OPL_RUNTIME_PROVIDER`: `local-docker` or `tencent-tke`.
- `OPL_WORKSPACE_IMAGE`: pullable one-person-lab-app image.
- `OPL_WORKSPACE_DOMAIN`: public Workspace domain.
- `OPL_K8S_NAMESPACE`: Kubernetes namespace.
- `OPL_INGRESS_CLASS`: ingress class.
- `OPL_WORKSPACE_STORAGE_CLASS`: PVC storage class.
- `OPL_IMAGE_PULL_SECRET_NAME`: image pull secret.
- `TENCENT_DEPLOY_KUBECONFIG_REF`: kubeconfig path.
- `TENCENT_DEPLOY_CLUSTER_ID`: target TKE cluster id.
- `TENCENT_TKE_REGION`: Tencent Cloud region for TKE mutations.
- `TENCENT_MUTATION_SECRET_ID` / `TENCENT_MUTATION_SECRET_KEY`: Tencent credentials allowed to mutate TKE node pools.
- `OPL_TKE_NODEPOOL_AUTOSCALING_GROUP_PARA_JSON`: node pool autoscaling group template.
- `OPL_TKE_NODEPOOL_LAUNCH_CONFIGURE_PARA_JSON`: node pool launch configuration template.
- `DATABASE_URL`: required for durable shared staging state.

## Route Contract Rules

- `packages/contracts/opl-cloud-route-api-contract.json` contains only current commercial truth.
- Future routes live in route backlog or product docs, not active contract.
- Every enabled UI route must have a stable route id and routeTo path.
- Every implemented route must bind page module, API client, server route, permission, object kind, and service boundary.
- Lab Owner routes do not expose operator/Fabric/Ledger raw evidence.

## Compute Storage Billing Semantics

- Creating compute starts compute billing and reserves a compute hold.
- In TKE, creating compute creates or expands a node pool and records the provider node pool id.
- Creating storage starts storage billing and reserves a storage hold.
- Attaching storage records the mount relationship and schedules the one-person-lab-app runtime on the selected compute node pool.
- Workspace entry creates a URL token for an existing attachment.
- Destroying compute removes the runtime workload and deletes the provider node pool after storage is detached.

## Pre-Commit Checklist

```bash
node --test tests/contracts/route-api-contract.test.js
node --test tests/domain/resource-provisioning.test.js
node --test tests/providers/tencent-tke-provider.test.js tests/providers/local-docker-provider.test.js
node --test tests/ui/commercial-console-routes.test.js tests/ui/commercial-console-surface.test.js tests/ui/console-clickability-contract.test.js
npm run build
git diff --check
```

## Common Failures

- Image pull denied: make `OPL_WORKSPACE_IMAGE` pullable and verify `OPL_IMAGE_PULL_SECRET_NAME`.
- Localhost Workspace URL: staging e2e must use a public `OPL_WORKSPACE_DOMAIN`.
- Missing storage class: set `OPL_WORKSPACE_STORAGE_CLASS` to an available class.
- Ingress path not routing: check shared Ingress class and `/w/<workspaceId>` path.
- Leftover cloud resources: inventory first by OPL labels and account ids, then detach storage, destroy compute, and destroy storage. Do not delete the control plane, shared ingress, shared secrets, or the current one-person-lab-app runtime image unless the image tag is confirmed unused by OPL Cloud.
