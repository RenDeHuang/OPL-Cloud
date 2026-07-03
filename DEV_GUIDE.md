# OPL Console Developer Guide

## Product Truth

OPL Console is the commercial control plane. The current resource model is:

- `ComputeResource`: account-owned TKE compute runtime for one-person-lab-app.
- `StorageVolume`: account-owned retained PVC/cloud storage.
- `StorageAttachment`: a storage volume mounted to a compute resource at a mount path such as `/data`.
- `Workspace`: URL token and WebUI entry composed from an attached compute/storage pair.
- `Wallet` and `Ledger`: billing records reference `computeId`, `storageId`, `attachmentId`, and `workspaceId`.

Workspace is not the only resource body. It is the access entry.

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
OPL_RUNTIME_PROVIDER=tencent-tke \
OPL_WORKSPACE_IMAGE=<tcr>/<namespace>/one-person-lab-app:<tag> \
OPL_WORKSPACE_DOMAIN=<workspace-staging-domain> \
OPL_K8S_NAMESPACE=<namespace> \
OPL_INGRESS_CLASS=<ingress-class> \
OPL_WORKSPACE_STORAGE_CLASS=<storage-class> \
OPL_IMAGE_PULL_SECRET_NAME=<secret> \
TENCENT_DEPLOY_KUBECONFIG_REF=<kubeconfig> \
npm run demo:api
```

In `tencent-tke` mode, `npm run demo:api` does not reset state unless `OPL_UIUX_DEMO_RESET=1` is explicit. The API calls `GET /api/runtime/readiness` internally before seeding real resources.

## Public Staging E2E

Full commercial e2e must use a public Console URL and a public Workspace URL. Localhost does not count.

Expected chain:

1. Login.
2. Verify or top up wallet balance.
3. Create compute.
4. Create storage.
5. Attach storage to compute.
6. Create Workspace URL.
7. Poll runtime status for Deployment, PVC, Service, Ingress, and Endpoints.
8. Open the public Workspace URL and receive HTTP 200 from one-person-lab-app.
9. Record sub2api/request usage.
10. Verify wallet, ledger, usage logs, and runtime evidence.
11. Detach and destroy resources.

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
- `DATABASE_URL`: required for durable shared staging state.

## Route Contract Rules

- `packages/contracts/opl-cloud-route-api-contract.json` contains only current commercial truth.
- Future routes live in route backlog or product docs, not active contract.
- Every enabled UI route must have a stable route id and routeTo path.
- Every implemented route must bind page module, API client, server route, permission, object kind, and service boundary.
- Lab Owner routes do not expose operator/Fabric/Ledger raw evidence.

## Compute Storage Billing Semantics

- Creating compute starts compute billing and reserves a compute hold.
- Creating storage starts storage billing and reserves a storage hold.
- Attaching storage does not create a new resource; it records the mount relationship.
- Workspace entry creates a URL token for an existing attachment.
- Stopping compute is not a commercial owner action in the current TKE model.

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
- Leftover cloud resources: detach storage, destroy compute, then destroy storage.
