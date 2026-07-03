# TKE Resource Model And E2E Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rework OPL Console from a Workspace-as-resource model to a commercial TKE resource model with separate compute, storage, attachment, Workspace URL, billing, and public staging e2e verification.

**Architecture:** Workspace becomes the user entry/work environment composed from provisioned resources. ComputeResource, StorageVolume, and StorageAttachment become first-class commercial objects owned by an account; Workspace references an attachment and exposes URL/token/runtime readiness. TKE is the primary runtime target, while local Docker remains a development adapter for the same object model.

**Tech Stack:** Node.js, React, Ant Design Pro, TKE, Kubernetes Deployment/PVC/Service/Ingress, Postgres-compatible store, node:test.

---

## File Structure

- Modify `packages/contracts/opl-cloud-route-api-contract.json`: replace Workspace lifecycle routes with current compute/storage/attachment/workspace routes.
- Modify `packages/contracts/opl-cloud-business-object-contract.json`: add `ComputeResource`, `StorageVolume`, `StorageAttachment`; narrow `Workspace` to URL/runtime entry.
- Modify `packages/console/ui/routes/opl-routes.js`: add route ids and menus for compute, storage, attachments, and Workspace entry.
- Modify `packages/console/ui/routes/opl-actions.js`: replace stop/restart action ids with create/destroy compute, create/destroy storage, attach/detach, create/open Workspace URL.
- Create `packages/console/ui/api/resources-api.js`: UI API client for compute/storage/attachment APIs.
- Modify `packages/console/api/routes/index.js` and create `packages/console/api/routes/resource-routes.js`: server route map for new resource APIs.
- Create `packages/console/src/services/resource-provisioning-service.js`: account-scoped commercial resource service.
- Modify `packages/console/src/services/workspace-lifecycle-service.js`: keep Workspace URL/token/runtime entry creation only.
- Modify `packages/fabric/src/runtime-providers/tencent-tke.js`: split provider methods into compute/storage/attachment/workspace entry operations.
- Modify `packages/fabric/src/runtime-providers/local-docker.js`: keep local parity for tests and development.
- Modify `packages/console/src/store.js`: persist `computeResources`, `storageVolumes`, `storageAttachments`.
- Modify UI pages under `packages/console/ui/pages/`: replace stop/restart surface with Compute, Storage, Attachment, Workspace flows.
- Modify `tools/production-verifier.js`: verify public staging e2e through the new chain.
- Create `DEV_GUIDE.md`: developer/agent guide for local, TKE-from-local, staging, and production verification.

## Route Contract Target

Current Lab Owner routes should become:

- `compute.list` -> `/console/compute`
- `compute.create` -> `/console/compute/new`
- `compute.detail` -> `/console/compute/:id`
- `storage.list` -> `/console/storage`
- `storage.create` -> `/console/storage/new`
- `storage.detail` -> `/console/storage/:id`
- `attachment.list` -> `/console/attachments`
- `attachment.create` -> `/console/attachments/new`
- `workspace.list` -> `/console/workspaces`
- `workspace.create` -> `/console/workspaces/new`
- `workspace.detail` -> `/console/workspaces/:id`

Current Lab Owner APIs should become:

- `POST /api/compute-resources`
- `POST /api/compute-resources/destroy`
- `POST /api/storage-volumes`
- `POST /api/storage-volumes/destroy`
- `POST /api/storage-attachments`
- `POST /api/storage-attachments/detach`
- `POST /api/workspaces`
- `POST /api/workspaces/reset-token`
- `POST /api/workspaces/delete-token`
- `POST /api/workspaces/runtime-status`

Remove these from Lab Owner current route contract:

- `POST /api/workspaces/stop-server`
- `POST /api/workspaces/restart-server`
- `POST /api/workspaces/destroy-server`
- `POST /api/workspaces/destroy-disk`

These may remain only as admin/operator cleanup or migration internals if they have a removal condition; they must not be the commercial Lab Owner resource model.

## Task 1: Contract Redesign

**Files:**
- Modify: `packages/contracts/opl-cloud-route-api-contract.json`
- Modify: `packages/contracts/opl-cloud-business-object-contract.json`
- Modify: `tests/contracts/route-api-contract.test.js`
- Modify: `tests/contracts/business-object-contract.test.js`

- [ ] **Step 1: Write failing route contract assertions**

Add assertions that the active route contract includes compute, storage, attachment, and Workspace entry routes, and excludes stop/restart commercial routes.

Run:

```bash
node --test tests/contracts/route-api-contract.test.js
```

Expected: FAIL because route ids such as `compute.list` and APIs such as `POST /api/compute-resources` are missing.

- [ ] **Step 2: Write failing business object assertions**

Add assertions that current object kinds include `ComputeResource`, `StorageVolume`, `StorageAttachment`, and `Workspace`, with `Workspace` no longer carrying all resource lifecycle capabilities.

Run:

```bash
node --test tests/contracts/business-object-contract.test.js
```

Expected: FAIL because the business contract still uses `WorkspaceCompute` and `WorkspaceStorage`.

- [ ] **Step 3: Update contract JSON**

Replace Workspace-only lifecycle entries with the route target listed above. Keep backlog content in `packages/contracts/opl-cloud-route-backlog.json` for future/non-current routes only.

- [ ] **Step 4: Verify contract tests**

Run:

```bash
node --test tests/contracts/route-api-contract.test.js tests/contracts/business-object-contract.test.js
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/contracts tests/contracts
git commit -m "refactor: define resource route contracts"
```

## Task 2: Resource Domain Model And Store

**Files:**
- Modify: `packages/console/src/store.js`
- Create: `packages/console/src/services/resource-provisioning-service.js`
- Modify: `packages/console/src/opl-cloud.js`
- Modify: `tests/domain/workspace-lifecycle.test.js`
- Create: `tests/domain/resource-provisioning.test.js`
- Modify: `tests/persistence/postgres-store.test.js`

- [ ] **Step 1: Write failing domain tests**

Create tests proving:

- account can create compute independently;
- account can create storage independently;
- storage can attach to compute;
- Workspace can be created only from an attached storage/compute pair;
- billing ledger/resource usage logs reference `computeId`, `storageId`, and `attachmentId`.

Run:

```bash
node --test tests/domain/resource-provisioning.test.js
```

Expected: FAIL because service and state fields do not exist.

- [ ] **Step 2: Extend state shape**

Add top-level state arrays/maps:

```js
computeResources: [],
storageVolumes: [],
storageAttachments: []
```

Persist them in Postgres with dedicated tables and state JSON, following the existing `resource_usage_logs` pattern.

- [ ] **Step 3: Implement service methods**

Add service methods:

```js
createComputeResource({ accountId, userId, packageId })
destroyComputeResource({ accountId, computeId, confirm })
createStorageVolume({ accountId, userId, packageId, sizeGb })
destroyStorageVolume({ accountId, storageId, confirmDataLoss })
attachStorage({ accountId, computeId, storageId, mountPath })
detachStorage({ accountId, attachmentId, confirm })
```

Keep these account-scoped. Do not allow cross-account attachment.

- [ ] **Step 4: Narrow Workspace creation**

Change Workspace creation to require `attachmentId`, not create compute/storage internally. Workspace stores:

```js
{
  id,
  ownerAccountId,
  attachmentId,
  computeId,
  storageId,
  url,
  access,
  runtime
}
```

- [ ] **Step 5: Verify domain and persistence**

Run:

```bash
node --test tests/domain/resource-provisioning.test.js tests/persistence/postgres-store.test.js
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add packages/console/src tests/domain tests/persistence
git commit -m "feat: add account scoped compute and storage resources"
```

## Task 3: Fabric Provider Split For TKE

**Files:**
- Modify: `packages/fabric/src/runtime-providers/tencent-tke.js`
- Modify: `packages/fabric/src/runtime-providers/local-docker.js`
- Modify: `tests/providers/tencent-tke-provider.test.js`
- Modify: `tests/providers/local-docker-provider.test.js`

- [ ] **Step 1: Write failing provider tests**

Update provider tests to expect:

- create storage -> PVC;
- create compute -> Deployment/Service or prepared runtime compute;
- attach storage -> Deployment volume mount to PVC;
- create Workspace entry -> Ingress path/URL token route;
- runtime status verifies Deployment image, PVC bound, Service endpoints, and Ingress route.

Run:

```bash
node --test tests/providers/tencent-tke-provider.test.js tests/providers/local-docker-provider.test.js
```

Expected: FAIL because provider methods are currently Workspace-centric.

- [ ] **Step 2: Implement TKE provider methods**

Split current `createWorkspaceRuntime` into:

```js
createStorageVolume(input)
createComputeResource(input)
attachStorage(input)
createWorkspaceEntry(input)
destroyComputeResource(input)
destroyStorageVolume(input)
detachStorage(input)
runtimeStatus(input)
```

For TKE, attachment means reconciling the Deployment pod template with the PVC mount. If Kubernetes must recreate pods to apply the mount, record that as runtime evidence; do not market it as a cost-saving stop/start.

- [ ] **Step 3: Keep local Docker parity**

Local Docker should create directories for storage, compose service for compute, compose volume mounts for attachment, and URL/token entry through the local API gateway.

- [ ] **Step 4: Verify provider tests**

Run:

```bash
node --test tests/providers/tencent-tke-provider.test.js tests/providers/local-docker-provider.test.js
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/fabric tests/providers
git commit -m "feat: split fabric resources for tke"
```

## Task 4: API Routes And UI

**Files:**
- Create: `packages/console/api/routes/resource-routes.js`
- Modify: `packages/console/api/routes/index.js`
- Create: `packages/console/ui/api/resources-api.js`
- Modify: `packages/console/ui/routes/opl-routes.js`
- Modify: `packages/console/ui/routes/opl-actions.js`
- Modify: `packages/console/ui/pages/ConsolePage.jsx`
- Create/modify pages under `packages/console/ui/pages/resources/`
- Modify: `tests/ui/commercial-console-routes.test.js`
- Modify: `tests/ui/commercial-console-surface.test.js`
- Modify: `tests/ui/console-clickability-contract.test.js`

- [ ] **Step 1: Write failing API route tests**

Assert `apiRouteManifest` contains the new resource APIs and no longer exposes stop/restart as Lab Owner current routes.

Run:

```bash
node --test tests/contracts/route-api-contract.test.js
```

Expected: FAIL until routes are wired.

- [ ] **Step 2: Implement API clients and route handlers**

Add UI client functions:

```js
createComputeResource(input, csrfToken)
createStorageVolume(input, csrfToken)
attachStorage(input, csrfToken)
createWorkspace(input, csrfToken)
```

Wire them to API route handlers and service methods.

- [ ] **Step 3: Replace UI flows**

Replace Lab Owner flow with:

1. Create compute.
2. Create storage.
3. Attach storage to compute.
4. Create Workspace URL from attachment.
5. Open Workspace URL.

Remove `停止计算` and `启动计算并挂载存储` from Lab Owner UI.

- [ ] **Step 4: Verify UI route and surface tests**

Run:

```bash
node --test tests/ui/commercial-console-routes.test.js tests/ui/commercial-console-surface.test.js tests/ui/console-clickability-contract.test.js
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/console/api packages/console/ui tests/ui tests/contracts
git commit -m "feat: expose resource provisioning console"
```

## Task 5: Local Console Creates Cloud Resources

**Files:**
- Modify: `tools/start-uiux-demo-api.js`
- Modify: `README.md`
- Create/modify: `DEV_GUIDE.md`
- Modify: `tests/ui/uiux-demo-preview-contract.test.js`

- [ ] **Step 1: Add documented local-to-TKE mode**

Support running local Console/API against real TKE:

```bash
OPL_RUNTIME_PROVIDER=tencent-tke \
OPL_WORKSPACE_IMAGE=<tcr>/<namespace>/one-person-lab-app:<tag> \
OPL_WORKSPACE_DOMAIN=<workspace-staging-domain> \
OPL_K8S_NAMESPACE=<namespace> \
OPL_INGRESS_CLASS=<ingress-class> \
OPL_WORKSPACE_STORAGE_CLASS=<storage-class> \
OPL_IMAGE_PULL_SECRET_NAME=<secret> \
npm run demo:api
```

This mode must not reset production/staging state unless `OPL_UIUX_DEMO_RESET=1` is explicit.

- [ ] **Step 2: Add readiness preflight**

Before allowing real TKE resource creation, verify env, kubeconfig, storage class, image pull secret, ingress domain, and `one-person-lab-app` image are available.

- [ ] **Step 3: Verify local-to-TKE route**

Run:

```bash
npm run demo:api
curl http://127.0.0.1:8791/api/runtime/readiness
```

Expected: readiness is green only when TKE resources are correctly configured.

- [ ] **Step 4: Commit**

```bash
git add tools README.md DEV_GUIDE.md tests/ui
git commit -m "docs: document local tke resource e2e"
```

## Task 6: Public Staging E2E

**Files:**
- Modify: `tools/production-verifier.js`
- Modify: `tests/production/production-verifier.test.js`
- Modify: `docs/runtime/production-runbook.md`
- Modify: `DEV_GUIDE.md`

- [ ] **Step 1: Rewrite verifier chain**

The verifier should perform:

1. Login as operator/owner.
2. Manual top-up or verify prepaid balance.
3. Create compute resource.
4. Create storage volume.
5. Attach storage to compute.
6. Create Workspace URL.
7. Poll runtime status until Deployment/PVC/Service/Ingress/Endpoints are ready.
8. Open public Workspace URL and assert HTTP 200 from `one-person-lab-app`.
9. Record a sub2api/request usage debit.
10. Verify wallet, ledger, usage logs, and evidence ledger.
11. Destroy Workspace entry, detach storage, destroy compute, destroy storage.
12. Verify cleanup and no active public URL remains.

- [ ] **Step 2: Require public URL**

Fail verifier if Console base URL or Workspace URL is localhost/127.0.0.1. Full commercial e2e must run against a public staging URL.

- [ ] **Step 3: Verify tests**

Run:

```bash
node --test tests/production/production-verifier.test.js
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add tools tests/production docs/runtime DEV_GUIDE.md
git commit -m "feat: verify public resource provisioning e2e"
```

## Task 7: DEV_GUIDE

**Files:**
- Create: `DEV_GUIDE.md`

- [ ] **Step 1: Create developer guide**

Sections:

- Project truth: OPL Console, TKE resource model, Workspace entry model.
- Local UI demo.
- Local Console against real TKE resources.
- Public staging e2e.
- Required env vars.
- Demo accounts and secret policy.
- Route contract rules.
- Compute/storage/attachment billing semantics.
- Pre-commit checklist.
- Common failures: image pull denied, localhost Workspace URL, missing storage class, ingress path not routing, leftover cloud resources.

- [ ] **Step 2: Verify docs mention active commands**

Run:

```bash
rg -n "demo:api|demo:ui|verify:production|OPL_RUNTIME_PROVIDER|OPL_WORKSPACE_IMAGE|compute|storage|attachment" DEV_GUIDE.md
```

Expected: all terms are present.

- [ ] **Step 3: Commit**

```bash
git add DEV_GUIDE.md
git commit -m "docs: add developer guide"
```

## Verification Gate

Run before merge:

```bash
npm test
npm run build
git diff --check
```

Run before claiming public e2e:

```bash
npm run validate:production-manifest
npm run verify:production
```

The public e2e is not complete unless the verifier opens a non-localhost Workspace URL and receives a successful response from the real `one-person-lab-app` runtime.

## Rationale

The old stop/restart server model is not a reliable commercial abstraction for TKE. Customer-facing controls should map to resources customers buy and can reason about: compute, storage, attachment, Workspace URL, and ledger records. TKE implementation can recreate pods during attachment or runtime reconciliation, but that is provider behavior, not the product model.
