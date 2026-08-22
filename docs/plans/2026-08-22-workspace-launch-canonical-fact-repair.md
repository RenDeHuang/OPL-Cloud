# Workspace Launch Canonical Fact Repair Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Restore strict decoding for the known historical schema 3 `workspace.launch.v2` operation by recovering its persisted Fabric `specDigest` through an audited, idempotent, transaction-protected repair.

**Architecture:** Fabric exposes a narrow typed readback for an immutable persisted preflight binding. Control Plane builds a read-only repair preview from that authority and applies the exact `specDigest` plus `version` change through a dedicated CAS and audit transaction. Candidate A stops after strict readability is restored and leaves `manual_review` unchanged.

**Tech Stack:** Go HTTP services, typed internal clients, PostgreSQL/Ent transactions, Node contract tests, GitHub Actions Candidate workflow.

---

### Task 1: Fabric Persisted Preflight Readback

**Files:**
- Modify: `services/fabric/internal/fabric/workspace_launch_stage.go`
- Modify: `services/fabric/internal/http/server.go`
- Test: `services/fabric/internal/fabric/workspace_launch_stage_test.go`
- Test: `services/fabric/internal/http/server_test.go`

**Steps:**

1. Write failing service tests for confirmed, not-found, corrupt, and identity-mismatch bindings, including a provider spy that must receive zero calls.
2. Add exact readback request/result DTOs and a service method that point-reads `OperationStore.Get` and reuses `decodeWorkspaceLaunchPreflight`.
3. Run Fabric service tests and confirm they pass.
4. Write failing HTTP tests for the authenticated read-only route and exact JSON contract.
5. Register `POST /fabric/workspace-launches/preflight/read` without mutation capability or idempotency headers.
6. Run Fabric HTTP tests and confirm they pass.
7. Commit the Fabric owner change.

### Task 2: Control Plane Typed Fabric Client

**Files:**
- Modify: `services/control-plane/internal/clients/fabric_workspace_launch.go`
- Test: `services/control-plane/internal/clients/fabric_workspace_launch_test.go`
- Modify: `services/control-plane/internal/controlplane/workspace_launch.go`

**Steps:**

1. Write failing client serialization and response-validation tests for the new Fabric readback.
2. Extend `FabricWorkspaceLaunchClient` and `fabricHTTPClient` with the exact typed read method.
3. Add the thin Control Plane service delegation used by the server owner.
4. Run focused client/controlplane tests and confirm they pass.
5. Commit the typed cross-module boundary.

### Task 3: Repair Preview Core

**Files:**
- Create: `services/control-plane/internal/server/workspace_launch_canonical_fact_repair.go`
- Test: `services/control-plane/internal/server/workspace_launch_canonical_fact_repair_test.go`
- Modify: `services/control-plane/internal/server/routes_admin.go`

**Steps:**

1. Write failing tests for the exact eligible historical row and all rejection cases: wrong schema, multiple missing facts, legacy keys, wrong stage/status, invalid version, existing conflicting digest, and Fabric identity drift.
2. Implement the narrow raw-result classifier without changing the strict decoder.
3. Build the proposed row and require the current strict decoder plus exact semantic diff.
4. Define canonical preview evidence and deterministic `previewDigest`.
5. Add the protected GET preview route with bounded redacted output.
6. Run focused server tests and confirm preview performs zero writes and zero provider mutations.
7. Commit the preview core.

### Task 4: Dedicated Repair CAS And Audit

**Files:**
- Modify: `services/control-plane/internal/server/table_store.go`
- Modify: `services/control-plane/internal/server/ent_state_store_workspace.go`
- Modify: `services/control-plane/internal/server/memory_table_store_test.go`
- Modify: `services/control-plane/internal/server/workspace_launch_canonical_fact_repair.go`
- Modify: `services/control-plane/internal/server/routes_admin.go`
- Test: `services/control-plane/internal/server/workspace_launch_canonical_fact_repair_test.go`
- Test: `services/control-plane/internal/server/workspace_launch_persistence_postgres_test.go`

**Steps:**

1. Write failing memory-store tests for success, exact replay, conflicting replay, CAS drift, forbidden field change, and audit failure rollback semantics.
2. Define the single-purpose repair mutation and audit identity.
3. Implement the memory-store behavior for deterministic tests.
4. Write failing PostgreSQL tests for atomic operation/audit commit and concurrent single-writer behavior.
5. Implement the Ent transaction with row lock, exact old-result comparison, eligibility validation, strict desired decode, exact diff, and deterministic audit insert.
6. Add the protected POST apply route requiring exact body, version, preview digest, reason, and one `Idempotency-Key`.
7. Regenerate preview inside apply and perform strict post-write readback.
8. Run focused memory, route, and PostgreSQL tests.
9. Commit the Control Plane apply path.

### Task 5: Contract And Regression Gates

**Files:**
- Modify only if required: `packages/contracts/*`
- Test: `tests/contracts/current-product-boundary.test.ts`
- Test: existing Controlled Basic Pilot and operation diagnostics tests

**Steps:**

1. Add only the machine-contract assertions needed to keep the new typed boundary deterministic; do not create a generic repair contract.
2. Verify the exact 11-key `controlledBasicPilot.data` shape is unchanged.
3. Verify diagnostics continue classifying the pre-repair row and report zero failures after repair.
4. Run focused Go tests for Fabric and Control Plane.
5. Run `npm run verify:local`.
6. Run `npm run verify:local:full`.
7. Commit any necessary contract/test projection.

### Task 6: Review And Candidate A Delivery

**Files:**
- Modify: `docs/status.md`
- Modify: `docs/roadmap.md` only if a remaining Candidate B gap is not already represented

**Steps:**

1. Review the full diff for authority boundaries, secret exposure, mutation scope, and non-goals.
2. Update `docs/status.md` with local evidence and explicitly state that production repair and `manual_review` convergence remain unexecuted.
3. Run the final focused and aggregate gates again from a clean worktree.
4. Push `codex/workspace-launch-canonical-repair-a` and open a PR whose body follows Issue #356's structure: decision, current problem, confirmed facts, ownership, minimal implementation, ordering, prohibitions, acceptance, and terminal state.
5. Merge only after CI passes and review findings are resolved.
6. Remove all non-main local worktrees and branches after merge, leaving only `main` clean and aligned with `origin/main`.
7. Report the exact merged SHA/tree and ask the repository owner `gaofeng21cn` to generate the immutable Candidate. Do not dispatch Candidate as another actor.
