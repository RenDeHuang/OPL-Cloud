# Fabric Provider Decoupling Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make Local-Docker and Tencent/TKE Provider plans, resource specifications, writes, and readback owned by Fabric while keeping one Control Plane Workspace Launch flow.

**Architecture:** Extend the existing Fabric catalog/preflight/stage contracts instead of replacing them. Fabric resolves a deployment-owned Provider Profile into an immutable Provider Binding containing the canonical Provider plan and digest; Control Plane stores only the opaque binding identity and continues to run the existing reconciler. Keep the existing Provider facade and capability interfaces, but separate plan resolution from compute, storage, attachment, Runtime, and readback responsibilities inside Fabric.

**Tech Stack:** Go modules (`services/fabric`, `services/control-plane`), typed JSON HTTP contracts, Fabric operation store, Tencent SDK/provisioner, Docker and Linux project-quota adapters, TypeScript Console, focused Go/Node tests.

---

### Task 1: Extend the catalog and launch-binding contracts

**Files:**
- Modify: `packages/contracts/opl-cloud-fabric-resource-catalog-contract.json`
- Modify: `packages/contracts/opl-cloud-fabric-launch-binding-contract.json`
- Test: `services/fabric/internal/http/server_test.go`
- Test: `services/control-plane/internal/clients/fabric_workspace_launch_test.go`

**Step 1: Write the failing contract assertions**

Add assertions that the catalog exposes only product-level package fields to Console consumers, while the launch preflight/stage contract carries `providerProfileRef`, `providerBindingRef`, and `specDigest`. Add a negative assertion for provider resource fields in the Console projection.

**Step 2: Run the focused tests to verify the assertions fail**

Run: `go test ./services/fabric/internal/http ./services/control-plane/internal/clients`

Expected: FAIL because the current catalog/preflight response has no immutable Provider spec digest and the current catalog shape still exposes provider resource dimensions.

**Step 3: Update the JSON contracts and golden vectors**

Define the Provider binding identity and canonical spec digest without changing the existing stage identity fields. Keep `resources` as provider-neutral resource identities only. Update schema versions only when the current machine boundary changes.

**Step 4: Run the focused tests again**

Run: `go test ./services/fabric/internal/http ./services/control-plane/internal/clients`

Expected: PASS.

**Step 5: Commit**

```bash
git add packages/contracts/opl-cloud-fabric-resource-catalog-contract.json packages/contracts/opl-cloud-fabric-launch-binding-contract.json services/fabric/internal/http/server_test.go services/control-plane/internal/clients/fabric_workspace_launch_test.go
git commit -m "feat(contracts): bind immutable provider workspace plans"
```

### Task 2: Add Fabric Provider plan resolution and immutable binding persistence

**Files:**
- Modify: `services/fabric/internal/fabric/provider_port.go`
- Modify: `services/fabric/internal/fabric/types.go`
- Modify: `services/fabric/internal/fabric/workspace_launch_stage.go`
- Modify: `services/fabric/internal/fabric/launch_stage_binding.go`
- Modify: `services/fabric/internal/fabric/service.go`
- Test: `services/fabric/internal/fabric/workspace_launch_stage_test.go`
- Test: `services/fabric/internal/fabric/launch_stage_binding_test.go`

**Step 1: Write the failing binding tests**

Cover:

- preflight persists the canonical Provider plan and digest;
- repeated identical preflight returns the same binding;
- stage validation requires the exact binding reference and digest;
- a changed current Provider profile cannot replace an existing binding;
- missing or malformed binding fails closed.

**Step 2: Run the focused tests to verify failure**

Run: `go test ./services/fabric/internal/fabric -run 'TestWorkspaceLaunch.*Binding|Test.*Provider.*Plan'`

Expected: FAIL because preflight currently persists only the input and Provider profile name.

**Step 3: Implement the minimal Fabric Core capability**

Add an internal plan resolver capability and an immutable binding payload stored by the existing Fabric operation store. Canonicalize the provider-owned plan using structured JSON, calculate a lowercase SHA-256 digest, and retain the exact canonical plan in the Fabric-owned operation payload. Keep the public `Provider` facade and existing stage operation identity unchanged.

**Step 4: Run the focused tests to verify success**

Run: `go test ./services/fabric/internal/fabric -run 'TestWorkspaceLaunch.*Binding|Test.*Provider.*Plan'`

Expected: PASS.

**Step 5: Commit**

```bash
git add services/fabric/internal/fabric/provider_port.go services/fabric/internal/fabric/types.go services/fabric/internal/fabric/workspace_launch_stage.go services/fabric/internal/fabric/launch_stage_binding.go services/fabric/internal/fabric/service.go services/fabric/internal/fabric/workspace_launch_stage_test.go services/fabric/internal/fabric/launch_stage_binding_test.go
git commit -m "feat(fabric): persist immutable provider plan bindings"
```

### Task 3: Move Local-Docker plan ownership behind the Provider profile

**Files:**
- Modify: `services/fabric/internal/fabric/local_docker_provider.go`
- Modify: `services/fabric/internal/fabric/local_docker_workspace_launch.go`
- Modify: `services/fabric/internal/fabric/local_docker_runtime.go`
- Test: `services/fabric/internal/fabric/local_docker_integration_test.go`
- Test: `services/fabric/internal/fabric/local_docker_capacity_test.go`
- Test: `services/fabric/internal/fabric/local_docker_runtime_secret_readback_test.go`

**Step 1: Write the failing profile tests**

Cover:

- Local-Docker startup rejects an absent or invalid provider plan profile;
- catalog and runtime limits use the loaded plan rather than literals;
- existing host-capacity and project-quota admission remains unchanged;
- the admitted local plan is included in the immutable binding/readback.

**Step 2: Run the focused tests to verify failure**

Run: `go test ./services/fabric/internal/fabric -run 'TestLocalDocker.*(Profile|Catalog|Capacity|Quota)|TestWorkspaceLaunch.*LocalDocker'`

Expected: FAIL because `Descriptor()` currently constructs `basic/pro` resource dimensions directly.

**Step 3: Implement the minimal Local-Docker adapter change**

Load a deployment-owned Local-Docker plan profile through the existing provider constructor/configuration path. Remove Provider-owned CPU, memory, disk, and quota defaults from generic launch inputs. Keep PR #367 quota and reservation code intact; pass the resolved plan into the existing runtime/storage admission and readback paths.

**Step 4: Run the focused tests to verify success**

Run: `go test ./services/fabric/internal/fabric -run 'TestLocalDocker.*(Profile|Catalog|Capacity|Quota)|TestWorkspaceLaunch.*LocalDocker'`

Expected: PASS.

**Step 5: Commit**

```bash
git add services/fabric/internal/fabric/local_docker_provider.go services/fabric/internal/fabric/local_docker_workspace_launch.go services/fabric/internal/fabric/local_docker_runtime.go services/fabric/internal/fabric/local_docker_integration_test.go services/fabric/internal/fabric/local_docker_capacity_test.go services/fabric/internal/fabric/local_docker_runtime_secret_readback_test.go
git commit -m "feat(fabric): resolve local docker plans from provider profile"
```

### Task 4: Move Tencent/TKE plan and storage ownership behind the Provider profile

**Files:**
- Modify: `services/fabric/internal/fabric/tencent_provider.go`
- Modify: `services/fabric/internal/fabric/tencent_provider_compute.go`
- Modify: `services/fabric/internal/fabric/tencent_provider_storage.go`
- Modify: `services/fabric/cmd/opl-tencent-provisioner/main.go`
- Test: `services/fabric/internal/fabric/tencent_provider_test.go`
- Test: `services/fabric/internal/fabric/tencent_workspace_launch_vertical_test.go`
- Test: `services/fabric/cmd/opl-tencent-provisioner/main_test.go`

**Step 1: Write the failing Tencent profile and drift tests**

Cover:

- missing or invalid instance type, NodePool, zone, or CBS profile fails closed;
- package shape is read from one Provider plan source rather than `packagePlan` literals and duplicate provisioner logic;
- preflight binds exact NodePool, instance type, zone, CBS disk type, charge type, and renewal facts;
- a changed current profile cannot change an existing binding;
- scale retry reuses the persisted absolute target and baseline inventory.

**Step 2: Run the focused tests to verify failure**

Run: `go test ./services/fabric/internal/fabric ./services/fabric/cmd/opl-tencent-provisioner -run 'Test(Tencent|WorkspaceSKU|Bootstrap|Compute|CBS|Storage).*'`

Expected: FAIL because the current implementation still contains package shape and CBS defaults in Provider/provisioner code.

**Step 3: Implement the minimal Tencent adapter change**

Load a single deployment-owned Tencent Provider Profile, derive the catalog and plan from it, and pass the resolved plan into the existing provisioner request. Keep live inventory, quota, price, protected-resource checks, exact NodePool baseline/target logic, CVM/TKE/VPC identity checks, static CBS binding, and Runtime readback. Remove only duplicate or implicit spec defaults; do not redesign TKE topology or the shared Ingress.

**Step 4: Run the focused tests to verify success**

Run: `go test ./services/fabric/internal/fabric ./services/fabric/cmd/opl-tencent-provisioner -run 'Test(Tencent|WorkspaceSKU|Bootstrap|Compute|CBS|Storage).*'`

Expected: PASS.

**Step 5: Commit**

```bash
git add services/fabric/internal/fabric/tencent_provider.go services/fabric/internal/fabric/tencent_provider_compute.go services/fabric/internal/fabric/tencent_provider_storage.go services/fabric/cmd/opl-tencent-provisioner/main.go services/fabric/internal/fabric/tencent_provider_test.go services/fabric/internal/fabric/tencent_workspace_launch_vertical_test.go services/fabric/cmd/opl-tencent-provisioner/main_test.go
git commit -m "feat(fabric): bind tencent plans and storage profiles"
```

### Task 5: Remove Provider-specific resource specification from Control Plane and Console

**Files:**
- Modify: `services/control-plane/internal/clients/fabric.go`
- Modify: `services/control-plane/internal/clients/fabric_workspace_launch.go`
- Modify: `services/control-plane/internal/server/workspace_launch_fabric_stages.go`
- Modify: `services/control-plane/internal/server/workspace_launch_reconciler.go`
- Modify: `apps/console-ui/src/api/dtos.ts`
- Modify: `apps/console-ui/src/api/console-read-api.ts`
- Modify: `apps/console-ui/src/pages/CustomerPages.tsx`
- Test: `services/control-plane/internal/clients/fabric_workspace_launch_test.go`
- Test: `services/control-plane/internal/server/workspace_launch_reconciler_test.go`
- Test: `tests/ui/customer-console-flow.test.ts`

**Step 1: Write the failing boundary tests**

Assert that:

- Control Plane stage input contains opaque Provider binding identity but no Docker/Tencent spec fields;
- Control Plane preserves the binding across replay/readback;
- Console catalog DTOs contain product fields only;
- an unknown or unavailable package is rejected before debit/provider mutation.

**Step 2: Run the focused tests to verify failure**

Run: `go test ./services/control-plane/internal/clients ./services/control-plane/internal/server -run 'Test.*(WorkspaceLaunch|Catalog|Provider)'`

Expected: FAIL because the current catalog DTO still projects provider dimensions and stage request hashing has no Provider spec digest.

**Step 3: Implement the minimal caller change**

Keep the existing Workspace Launch Reconciler and stage order. Persist only `providerProfileRef`, `providerBindingRef`, and `specDigest` in Control Plane launch facts. Keep product billing in Control Plane; do not move infrastructure pricing policy into Console or generic Control Plane DTOs.

**Step 4: Run focused Go and UI tests**

Run: `go test ./services/control-plane/internal/clients ./services/control-plane/internal/server -run 'Test.*(WorkspaceLaunch|Catalog|Provider)'`

Run: `npm test -- --runInBand tests/ui/customer-console-flow.test.ts`

Expected: PASS.

**Step 5: Commit**

```bash
git add services/control-plane/internal/clients/fabric.go services/control-plane/internal/clients/fabric_workspace_launch.go services/control-plane/internal/server/workspace_launch_fabric_stages.go services/control-plane/internal/server/workspace_launch_reconciler.go apps/console-ui/src/api/dtos.ts apps/console-ui/src/api/console-read-api.ts apps/console-ui/src/pages/CustomerPages.tsx services/control-plane/internal/clients/fabric_workspace_launch_test.go services/control-plane/internal/server/workspace_launch_reconciler_test.go tests/ui/customer-console-flow.test.ts
git commit -m "feat: keep provider infrastructure out of launch callers"
```

### Task 6: Run aggregate gates and update evidence documents

**Files:**
- Modify: `docs/architecture.md`
- Modify: `docs/implementation-architecture.md`
- Modify: `docs/status.md`
- Modify: `docs/roadmap.md`
- Test: all focused tests from Tasks 2-5

**Step 1: Run focused provider and contract tests**

Run: `go test ./services/fabric/...`

Run: `go test ./services/control-plane/...`

Run: `npm test -- --runInBand tests/ui/customer-console-flow.test.ts`

Expected: PASS without live Tencent mutations.

**Step 2: Update current documentation projections**

Record the implemented Provider ownership, immutable plan binding, and remaining production/Instance qualification gap in the canonical documentation owners. Do not claim live TKE readiness from source tests.

**Step 3: Run the applicable aggregate gate**

Run: `npm run verify:local:full`

Expected: PASS with no skipped required PostgreSQL, Control Plane capacity, or Local-Docker tests.

**Step 4: Inspect the final diff and worktree**

Run: `git diff --check`

Run: `git status --short --branch`

Confirm that no files under the PR #367 worktree changed and that no production workflow, push, or PR was created.

**Step 5: Commit documentation and verification evidence**

```bash
git add docs/architecture.md docs/implementation-architecture.md docs/status.md docs/roadmap.md
git commit -m "docs: record fabric provider ownership and binding evidence"
```
