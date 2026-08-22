# Workspace Launch Disposable Reset Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Safely abandon and clean one pre-commercial `workspace.launch.v2` operation without deleting audit or financial history.

**Architecture:** Control Plane owns a protected preview/apply state machine. Preview composes strict typed owner readbacks into a deterministic plan; apply persists resumable progress, calls existing owner mutations in dependency order, appends reset evidence, and CAS-terminalizes the retained launch operation.

**Tech Stack:** Go HTTP services, Control Plane persisted operations, Fabric typed APIs, Sub2API typed clients, Ledger receipts, PostgreSQL/Ent transactions, focused and aggregate verification.

---

### Task 1: Reset Classification And Plan Contract

**Files:**
- Create: `services/control-plane/internal/server/workspace_launch_disposable_reset.go`
- Test: `services/control-plane/internal/server/workspace_launch_disposable_reset_test.go`
- Modify: `services/control-plane/internal/server/workspace_launch_reconciler.go`

1. Add failing tests for the exact `debit/manual_review` eligible operation and rejection of wrong action, stage, status, Workspace projection, competing operations, invalid canonical identity, and non-disposable authority.
2. Define typed owner states, ordered plan steps, redacted preview DTO, and deterministic `resetPlanDigest`.
3. Keep the current strict decoder unchanged and add the minimum terminal reset evidence shape it must validate.
4. Run focused tests and commit.

### Task 2: Owner-authoritative Read-only Inventory

**Files:**
- Modify: `services/control-plane/internal/clients/fabric_workspace_launch.go`
- Modify: `services/control-plane/internal/clients/fabric.go`
- Modify: `services/control-plane/internal/clients/sub2api.go`
- Modify: `services/control-plane/internal/clients/ledger.go`
- Modify: `services/control-plane/internal/controlplane/workspace_launch.go`
- Modify: `services/control-plane/internal/server/workspace_launch_disposable_reset.go`
- Test: corresponding focused client/server tests

1. Add only missing typed read methods required to classify Fabric stages,
   runtime/Secret observations, Key, debit history, receipts, Workspace, and
   competing operations.
2. Compose independent reads concurrently with bounded cancellation.
3. Reject unknown, duplicate, conflict, identity drift, and over-broad receipt
   or resource matches.
4. Prove preview performs zero writes and zero provider calls; commit.

### Task 3: Protected Preview Route

**Files:**
- Modify: `services/control-plane/internal/server/routes_admin.go`
- Test: `services/control-plane/internal/server/workspace_launch_disposable_reset_test.go`

1. Add the operator GET route and exact redacted response contract.
2. Require explicit disposable authority configuration and no-store response.
3. Test authentication, unavailable owners, redaction, deterministic digest,
   and zero mutation counters.
4. Commit.

### Task 4: Resumable Reset Operation And Resource Cleanup

**Files:**
- Modify: `services/control-plane/internal/server/table_store.go`
- Modify: `services/control-plane/internal/server/ent_state_store_workspace.go`
- Modify: `services/control-plane/internal/server/memory_table_store_test.go`
- Modify: `services/control-plane/internal/server/workspace_launch_disposable_reset.go`
- Test: memory and PostgreSQL persistence tests

1. Persist one child reset operation with launch-version CAS, plan digest,
   deterministic step idempotency keys, observations, and mutation counts.
2. Implement runtime/Secret, attachment, storage, and compute cleanup through
   existing Fabric owner APIs in fixed order.
3. After each possible response loss, read owner truth before retrying.
4. Require final provider/Kubernetes absence before financial mutations.
5. Test crash/replay at each step, plan drift, competing writer, and zero-call
   absent resources; commit.

### Task 5: Key, Debit, And Ledger Convergence

**Files:**
- Modify: `services/control-plane/internal/server/workspace_launch_disposable_reset.go`
- Modify only as needed: typed Control Plane client/service files
- Test: focused Sub2API/Ledger integration tests

1. Delete only the exact confirmed test Workspace Key and confirm absence.
2. If debit is absent, record zero wallet mutation. If confirmed, execute one
   exact compensating refund and prove balance-history identity and amount.
3. Reject unknown/conflicting debit before Key deletion or terminalization.
4. Append, never delete, one Ledger reset receipt that binds the plan and owner
   readbacks.
5. Test exact replay and amount conservation; commit.

### Task 6: Atomic Terminalization, Audit, And Apply Route

**Files:**
- Modify: `services/control-plane/internal/server/routes_admin.go`
- Modify: `services/control-plane/internal/server/ent_state_store_workspace.go`
- Modify: `services/control-plane/internal/server/workspace_launch_disposable_reset.go`
- Test: route, memory, and PostgreSQL concurrency tests

1. Add POST apply requiring exact launch version, plan digest, reason, and one
   Idempotency-Key.
2. Regenerate preview before first mutation and reject plan drift.
3. After all owner facts converge, atomically CAS the retained launch operation
   to strict-decoder-compatible `failed`, persist reset evidence, and write the
   operator audit.
4. Return exact replay only after complete audit and operation identity checks.
5. Test concurrent single writer and audit rollback; commit.

### Task 7: Final Receipt And Pilot Regression

**Files:**
- Modify: `services/control-plane/internal/server/workspace_launch_admission.go`
- Modify: `services/control-plane/internal/server/operator_health_test.go`
- Modify: `docs/status.md`
- Modify: `docs/roadmap.md` only if a remaining production execution gap is not represented

1. Add final owner readback and redacted reset receipt counters.
2. Verify the terminal operation no longer contributes to `inFlight`,
   `manualReview`, failures, alerts, or `disableRequired`.
3. Verify the exact Controlled Basic Pilot envelope remains stable.
4. Run focused Go tests, `npm run verify:local`, and
   `npm run verify:local:full`.
5. Update current status without claiming production reset; commit.

### Task 8: Review And Candidate Delivery

1. Independently review authority, deletion scope, idempotency, financial
   conservation, append-only evidence, and secret redaction.
2. Push a `codex/` branch and open a PR structured like Issue #356.
3. Merge only after required CI and review findings pass.
4. Report merged SHA/tree for repository owner Candidate generation.
5. Stop before production execution; Instance protected runner performs preview,
   apply, final Pilot diagnostic, and retains the reset receipt.
