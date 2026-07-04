# Go Provisioner Compute Allocation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the current OPL Cloud business chain where a package-level ComputePool opens one account-owned Tencent CVM ComputeAllocation, storage attaches to it, one-person-lab-app runs on it, and Console shows price/status/failure state.

**Architecture:** Node.js remains the commercial control plane for auth, account ownership, wallet holds, billing ledger, route/API, and UI. A small Go binary at `cmd/opl-tencent-provisioner` owns Tencent Cloud SDK mutations behind a JSON stdin/stdout contract. The Tencent TKE provider calls that binary for compute pool/allocation actions, then keeps using Kubernetes manifests for PVC, runtime deployment, ingress, and Workspace URL entry.

**Tech Stack:** Node.js `node:test`, React/Ant Design Console, Go 1.22, Tencent Cloud Go SDK modules `github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tke/v20180525` and `github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312`.

---

### Task 1: Add Go Provisioner JSON Contract

**Files:**
- Create: `cmd/opl-tencent-provisioner/go.mod`
- Create: `cmd/opl-tencent-provisioner/main.go`
- Create: `cmd/opl-tencent-provisioner/main_test.go`

- [ ] **Step 1: Write failing Go tests**

Create tests that run request handlers in process:

```go
func TestReadinessRequiresTencentEnv(t *testing.T) {
  response := handle(Request{Action: "readiness"}, map[string]string{})
  if response.Ok {
    t.Fatalf("expected readiness to fail without Tencent env")
  }
  if response.ErrorCode != "tencent_env_missing" {
    t.Fatalf("unexpected error code: %s", response.ErrorCode)
  }
}

func TestCreateComputeAllocationDryRunReturnsOwnership(t *testing.T) {
  env := map[string]string{
    "TENCENTCLOUD_SECRET_ID": "sid",
    "TENCENTCLOUD_SECRET_KEY": "skey",
    "TENCENTCLOUD_REGION": "ap-guangzhou",
    "TENCENT_DEPLOY_CLUSTER_ID": "cls-123",
  }
  response := handle(Request{
    Action: "create_compute_allocation",
    DryRun: true,
    AccountId: "pi-alpha",
    UserId: "usr-alpha",
    PackageId: "basic",
    Pool: ComputePoolInput{Id: "pool-basic-2c4g", InstanceType: "SA5.LARGE4"},
    Allocation: ComputeAllocationInput{Id: "compute-alpha"},
  }, env)
  if !response.Ok {
    t.Fatalf("expected ok response: %#v", response)
  }
  if response.PoolId != "pool-basic-2c4g" || response.InstanceId == "" || response.Status != "provisioning" {
    t.Fatalf("missing ownership fields: %#v", response)
  }
}
```

Run: `cd cmd/opl-tencent-provisioner && go test ./...`

Expected: FAIL because the module and handler do not exist yet.

- [ ] **Step 2: Implement minimal JSON CLI**

Implement:

```text
stdin JSON -> Request -> handle -> Response -> stdout JSON
```

Supported actions:

- `readiness`
- `create_compute_allocation`
- `destroy_compute_allocation`

Dry-run returns deterministic `operationId`, `nodePoolId`, `instanceId`, `nodeName`, `status`, and `providerData` without Tencent API calls.

- [ ] **Step 3: Add Tencent SDK imports without live calls in tests**

Keep SDK-dependent code behind a small `TencentClient` interface so unit tests use dry-run/fake behavior. The non-dry-run path must construct SDK clients from env and return normalized failures if required Tencent env is missing.

- [ ] **Step 4: Verify Go tests**

Run: `cd cmd/opl-tencent-provisioner && go test ./...`

Expected: PASS.

### Task 2: Add Node Provisioner Bridge

**Files:**
- Create: `packages/fabric/src/tencent-provisioner-client.js`
- Test: `tests/providers/tencent-provisioner-client.test.js`
- Modify: `packages/fabric/src/runtime-providers/tencent-tke.js`
- Modify: `tests/providers/tencent-tke-provider.test.js`

- [ ] **Step 1: Write failing bridge tests**

Test that Node sends JSON to the configured binary and maps response fields:

```js
test("TencentProvisionerClient invokes JSON stdin/stdout provisioner", async () => {
  const client = new TencentProvisionerClient({
    binPath: process.execPath,
    runnerScript: fixtureScript,
    env: { TENCENTCLOUD_REGION: "ap-guangzhou" }
  });
  const result = await client.createComputeAllocation({
    accountId: "pi-alpha",
    userId: "usr-alpha",
    packageId: "basic",
    pool: { id: "pool-basic-2c4g", instanceType: "SA5.LARGE4" },
    allocation: { id: "compute-alpha" }
  });
  assert.equal(result.instanceId, "ins-test");
});
```

Run: `node --test tests/providers/tencent-provisioner-client.test.js`

Expected: FAIL because the client does not exist.

- [ ] **Step 2: Implement bridge**

`TencentProvisionerClient` must:

- accept `OPL_TENCENT_PROVISIONER_BIN`;
- send one JSON request to stdin;
- parse one JSON response from stdout;
- throw `Error(errorCode)` with `safeMessage`, `providerRequestId`, `retryable`, and `providerData` attached when `ok:false`;
- support a dry-run flag via `OPL_TENCENT_PROVISIONER_DRY_RUN=true`.

- [ ] **Step 3: Wire TencentTkeProvider compute allocation**

`createComputeAllocation` must:

- build pool from package plan;
- call provisioner `create_compute_allocation`;
- return `providerResourceId` as `cvm/<instanceId>`;
- store `poolId`, `nodePoolId`, `instanceId`, `nodeName`, `operationId`, and provider-safe metadata;
- keep Kubernetes runtime deployment gated to attachment/runtime reconciliation.

`destroyComputeAllocation` must call provisioner `destroy_compute_allocation` before removing runtime Kubernetes resources.

- [ ] **Step 4: Verify provider tests**

Run: `node --test tests/providers/tencent-provisioner-client.test.js tests/providers/tencent-tke-provider.test.js`

Expected: PASS.

### Task 3: Persist Operation and Failure State

**Files:**
- Modify: `packages/console/src/services/resource-provisioning-service.js`
- Modify: `packages/console/src/services/console-read-model-service.js`
- Test: `tests/domain/resource-provisioning.test.js`

- [ ] **Step 1: Write failing domain tests**

Add tests for:

- compute allocation records `poolId`, `nodePoolId`, `instanceId`, `operationId`, `hourlyPrice`, `holdAmount`;
- failed provider response leaves the resource visible with `status:"failed"`, `safeMessage`, and operation failure state;
- storage can still be created without compute.

- [ ] **Step 2: Implement resource operation records**

Create runtime operation rows for compute/storage/attachment using `workspaceId:"resource"` plus:

```js
{
  resourceType: "compute_allocation",
  resourceId: allocationId,
  operationType: "create_compute_allocation",
  status: "running" | "completed" | "failed",
  safeMessage,
  providerRequestId
}
```

- [ ] **Step 3: Store pricing fields**

When creating resources, persist:

- `hourlyPrice`, `holdAmount`, `balanceImpact` for compute;
- `gbMonthPrice`, `hourlyEstimate`, `holdAmount`, `balanceImpact` for storage.

- [ ] **Step 4: Verify domain tests**

Run: `node --test tests/domain/resource-provisioning.test.js tests/billing/prepaid-ledger-billing.test.js`

Expected: PASS.

### Task 4: Make Console Creation Screens Commercially Legible

**Files:**
- Modify: `packages/console/ui/pages/resources/ResourceProvisioningPages.jsx`
- Modify: `packages/console/ui/pages/shared/formatters.js`
- Test: `tests/ui/commercial-console-surface.test.js`

- [ ] **Step 1: Write failing UI contract tests**

Assert compute and storage create pages include:

- hourly compute price;
- seven-day hold;
- balance after hold;
- storage GB-month price and hourly estimate;
- detail pages show operation id and safe failure message.

- [ ] **Step 2: Implement UI summaries**

Use current `state.wallet`, `state.packages`, selected package, and selected storage size to show price and hold before submit. Do not use role split login shortcuts or raw provider evidence on Lab Owner pages.

- [ ] **Step 3: Verify UI tests**

Run: `node --test tests/ui/commercial-console-surface.test.js tests/ui/commercial-console-routes.test.js`

Expected: PASS.

### Task 5: Build and Package Go Provisioner

**Files:**
- Modify: `Dockerfile`
- Modify: `packages/console/src/production-readiness.js`
- Modify: `tests/production/production-readiness.test.js`
- Modify: `tests/production/tke-control-plane-image.test.js`

- [ ] **Step 1: Write failing production tests**

Assert:

- Dockerfile builds `cmd/opl-tencent-provisioner` in a Go build stage;
- runtime image copies `/usr/local/bin/opl-tencent-provisioner`;
- readiness checks the configured provisioner binary exists as executable;
- readiness no longer treats `tccli` as a tool.

- [ ] **Step 2: Implement Docker build stage and readiness check**

Add Go build stage, copy binary into runtime image, and make `productionReadiness` validate `OPL_TENCENT_PROVISIONER_BIN` with executable access.

- [ ] **Step 3: Verify production tests**

Run: `node --test tests/production/production-readiness.test.js tests/production/tke-control-plane-image.test.js tests/production/tke-deploy-workflow.test.js`

Expected: PASS.

### Task 6: Local-To-Staging Business Chain E2E

**Files:**
- Create: `tests/e2e/local-business-chain.test.js`
- Create: `tests/helpers/fake-runtime-provider.js`

- [ ] **Step 1: Write failing local-to-staging-safe E2E**

Using a test-only fake Tencent runtime provider, test:

1. top up wallet;
2. create storage without compute;
3. create compute allocation;
4. attach storage;
5. create Workspace URL;
6. destroy compute;
7. create second compute allocation;
8. reattach same storage;
9. create second Workspace URL;
10. assert storage id is unchanged and ledger contains compute/storage/attachment/workspace references.

- [ ] **Step 2: Implement minimal provider/domain fixes**

Only fix gaps revealed by the E2E. Do not create paid Tencent resources from the test suite.

- [ ] **Step 3: Verify E2E and full focused suite**

Run:

```bash
cd cmd/opl-tencent-provisioner && go test ./...
cd ../..
node --test tests/providers/tencent-provisioner-client.test.js tests/providers/tencent-tke-provider.test.js tests/domain/resource-provisioning.test.js tests/e2e/local-business-chain.test.js tests/ui/commercial-console-surface.test.js tests/production/production-readiness.test.js tests/production/tke-control-plane-image.test.js
git diff --check
```

Expected: PASS.

### Task 7: Final Audit

**Files:**
- No fixed files.

- [ ] **Step 1: Audit retired implementation paths**

Run:

```bash
rg -n "tccli|runTccli|/api/compute-resources|createComputeResource|computeResources|createWorkspaceRuntime|stopServer\\(|restartServer\\(|destroyServer\\(|StorageBackup|storageBackups|migrateLegacyState" packages tests tools README.md DEV_GUIDE.md docs deploy .github --glob '!docs/history/**' --glob '!docs/superpowers/plans/**' --glob '!docs/superpowers/specs/**'
```

Expected: no matches.

- [ ] **Step 2: Commit implementation**

Run:

```bash
git add -A
git commit -m "feat: add Tencent Go provisioner allocation flow"
```

Expected: one local branch commit; no merge, push, deploy, or paid Tencent resource creation.
