# Compute Narrative Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove active OPL Cloud narrative that treats Workspace or per-user node pools as the compute resource, and replace it with the approved ComputePool + ComputeAllocation model.

**Architecture:** This cleanup only changes active narrative surfaces: docs, machine contracts, and tests that define current commercial truth. Runtime provider implementation, UI behavior, and Go SDK provisioner implementation are intentionally left for the next plan so this commit establishes one consistent truth before code migration.

**Tech Stack:** Markdown docs, JSON contracts, Node.js `node:test` contract tests.

---

### Task 1: Remove Retired Active Contracts

**Files:**
- Delete: `packages/contracts/opl-cloud-storage-backup-contract.json`
- Delete: `packages/contracts/opl-cloud-workspace-lifecycle-contract.json`
- Modify: `packages/contracts/README.md`
- Delete: `tests/domain/storage-backup-recovery.test.js`
- Delete: `tests/domain/workspace-lifecycle.test.js`
- Modify: `tests/README.md`

- [ ] **Step 1: Delete retired storage backup and workspace lifecycle contracts**

Run:

```bash
rm packages/contracts/opl-cloud-storage-backup-contract.json packages/contracts/opl-cloud-workspace-lifecycle-contract.json
```

Expected: files are removed from the active contract set.

- [ ] **Step 2: Delete tests that protect retired active contracts**

Run:

```bash
rm tests/domain/storage-backup-recovery.test.js tests/domain/workspace-lifecycle.test.js
```

Expected: active tests no longer protect storage backup or Workspace lifecycle as current commercial truth.

- [ ] **Step 3: Update package and test READMEs**

Replace references to storage backup and Workspace lifecycle with ComputePool, ComputeAllocation, StorageVolume, StorageAttachment, and Workspace URL entry.

- [ ] **Step 4: Verify retired contract names are absent from active surfaces**

Run:

```bash
rg -n "storage backup|StorageBackup|workspace lifecycle|Workspace lifecycle|stop, restart|recreate from retained storage" README.md DEV_GUIDE.md docs packages/contracts tests --glob '!docs/history/**'
```

Expected: no active docs/contracts/tests describe retired backup or Workspace lifecycle as current product truth.

### Task 2: Rewrite Business Object Contract

**Files:**
- Modify: `packages/contracts/opl-cloud-business-object-contract.json`
- Modify: `tests/contracts/business-object-contract.test.js`

- [ ] **Step 1: Replace ComputeResource with ComputePool and ComputeAllocation**

Update active object kinds so current compute truth is:

```json
[
  "ComputePool",
  "ComputeAllocation",
  "StorageVolume",
  "StorageAttachment",
  "Workspace"
]
```

`ComputePool` has list/read/evidence capability. `ComputeAllocation` has list/detail/read/write/action/evidence capability.

- [ ] **Step 2: Narrow Workspace capabilities**

Workspace should be URL/runtime access only, not compute/storage lifecycle ownership.

- [ ] **Step 3: Update business object tests**

Change the required object assertions from `ComputeResource` to `ComputePool` and `ComputeAllocation`. Add an assertion that `ComputeResource` is not in the active contract.

- [ ] **Step 4: Run contract test**

Run:

```bash
node --test tests/contracts/business-object-contract.test.js
```

Expected: PASS.

### Task 3: Rewrite Route Contract Narrative

**Files:**
- Modify: `packages/contracts/opl-cloud-route-api-contract.json`
- Modify: `packages/contracts/opl-cloud-route-backlog.json`
- Modify: `tests/contracts/route-api-contract.test.js`

- [ ] **Step 1: Replace compute routes**

Active route contract must describe:

```text
compute-pools.list          GET /api/compute-pools
compute-allocations.list   GET /api/state
compute-allocations.create POST /api/compute-allocations
compute-allocations.detail GET /api/compute-allocations/:id
compute-allocations.destroy POST /api/compute-allocations/:id/destroy
```

Do not keep `POST /api/compute-resources` in the active route contract.

- [ ] **Step 2: Keep storage and attachment routes current but tie them to allocation language**

Storage remains independent. Attachment references `computeAllocationId`, not `computeId`.

- [ ] **Step 3: Move retired route names to backlog only if needed**

Backlog may mention retired route ids as removed history, but active contract must not include compatibility routes.

- [ ] **Step 4: Update route contract tests**

Change expected active route ids and object kinds. Add assertions that active route contract does not contain `ComputeResource`, `/api/compute-resources`, storage backup routes, or Workspace lifecycle routes.

- [ ] **Step 5: Run route contract test**

Run:

```bash
node --test tests/contracts/route-api-contract.test.js
```

Expected: PASS.

### Task 4: Rewrite Active Product Docs

**Files:**
- Modify: `README.md`
- Modify: `DEV_GUIDE.md`
- Modify: `docs/invariants.md`
- Modify: `docs/product/console-workspace-v1.md`
- Modify: `docs/runtime/tke-production-deployment.md`
- Modify: `docs/project.md`
- Modify: `docs/status.md`
- Modify: `packages/README.md`

- [ ] **Step 1: Update product chain prose**

Use this chain everywhere active docs describe OPL Cloud:

```text
select package -> ensure ComputePool -> open one dedicated CVM ComputeAllocation -> create StorageVolume -> attach storage -> deploy one-person-lab-app -> create Workspace URL -> bill compute/storage/request usage
```

- [ ] **Step 2: Remove stale lifecycle language**

Remove active claims about stopping/restarting Workspace compute, restoring storage backups, and Workspace owning compute/storage lifecycle.

- [ ] **Step 3: Add UI price/status expectations**

Docs must state that compute and storage creation screens show price, hold, balance impact, provisioning state, and failure details.

- [ ] **Step 4: Verify active docs have one narrative**

Run:

```bash
rg -n "ComputeResource|compute resource|Workspace lifecycle|storage backup|stop, restart|recreate from retained storage|maps to Deployment/Service|tccli" README.md DEV_GUIDE.md docs packages/README.md --glob '!docs/history/**' --glob '!docs/superpowers/specs/**' --glob '!docs/superpowers/plans/**'
```

Expected: no active docs use the retired commercial narrative.

### Task 5: Remove tccli from Active Deployment Contract Narrative

**Files:**
- Modify: `packages/contracts/opl-cloud-deployment-contract.json`
- Modify: `tests/production/production-readiness.test.js`
- Modify: `tests/production/tke-control-plane-image.test.js`
- Modify: `tests/production/tke-deploy-workflow.test.js`

- [ ] **Step 1: Replace tccli requirement with Go provisioner requirement**

Deployment contract should require `OPL_TENCENT_PROVISIONER_BIN` and no `tccli` required tool.

- [ ] **Step 2: Update tests that currently require tccli**

Tests should assert readiness requires the provisioner boundary, not `tccli`.

- [ ] **Step 3: Run production narrative tests**

Run:

```bash
node --test tests/production/production-readiness.test.js tests/production/tke-control-plane-image.test.js tests/production/tke-deploy-workflow.test.js
```

Expected: PASS.

### Task 6: Final Narrative Audit

**Files:**
- No new files.

- [ ] **Step 1: Search active surfaces for retired terms**

Run:

```bash
rg -n "ComputeResource|compute resource|computeResources|createComputeResource|destroyComputeResource|StorageBackup|storage backup|workspace lifecycle|Workspace lifecycle|tccli|runTccli|CreateClusterNodePool|DeleteClusterNodePool|maps to Deployment/Service|stop, restart|recreate from retained storage" README.md DEV_GUIDE.md docs packages/contracts tests --glob '!docs/history/**' --glob '!docs/superpowers/specs/**' --glob '!docs/superpowers/plans/**'
```

Expected: matches only in implementation tests explicitly marked for replacement in the next implementation plan, or no matches. Active docs/contracts must have no matches.

- [ ] **Step 2: Run focused tests**

Run:

```bash
node --test tests/contracts/business-object-contract.test.js tests/contracts/route-api-contract.test.js tests/ui/commercial-console-surface.test.js
```

Expected: PASS or fail only where UI implementation still uses the old route/action names. If UI tests fail for implementation mismatch, update the tests to protect the new narrative and leave implementation migration for the next plan.

- [ ] **Step 3: Commit cleanup**

Run:

```bash
git add README.md DEV_GUIDE.md docs packages/contracts tests
git commit -m "docs: retire workspace compute narrative"
```

Expected: one commit containing only narrative cleanup, contract cleanup, and test cleanup.
