# Workspace Delete Lifecycle Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Deliver a durable provider-neutral Workspace Delete that permanently removes all owned resources, records non-financial evidence, and never performs an automatic refund.

**Architecture:** Hard-cut the durable command to `workspace.delete.v2`; Control Plane verifies the immutable Launch and its exact Receipt, runs the existing owner-specific resource stages, removes the Workspace, then records `workspace.deleted.v1`. Ledger validates evidence only, while Fabric proves provider absence. Historical v1 Delete rows are never reinterpreted or mutated.

**Tech Stack:** Go services and tests, JSON machine contracts, TypeScript contract tests, Docker integration tests, Tencent SDK fake-adapter tests, PostgreSQL integration gate.

---

### Task 1: Define The Delete V2 Cross-Module Contract

**Files:**
- Modify: `packages/contracts/opl-cloud-management-contract.json`
- Modify: `packages/contracts/opl-cloud-billing-ledger-contract.json`
- Modify: `packages/contracts/opl-cloud-evidence-ledger-contract.json`
- Modify: `tests/contracts/monthly-billing-hard-cut.test.ts`

1. Change the contract test to require `workspace.delete.v2`, provider-neutral
   resource identity, the no-refund stage list, and a strict
   `workspace.deleted.v1` Receipt with only `launchReceiptId` as input.
2. Assert Delete has no debit, refund, wallet adjustment, refund Receipt, or
   `SupersedesReceiptID` identity and that Refund remains a separate supported
   financial Receipt type.
3. Run `node --test --experimental-strip-types tests/contracts/monthly-billing-hard-cut.test.ts`
   and confirm it fails against the old contracts.
4. Make the three contract documents express the new operation, terminal
   identity, Receipt shape, response-loss rule, and historical-v1 hard cut.
5. Run the focused contract test and `npm run test:source`.
6. Commit with `test(contracts): define workspace delete v2`.

### Task 2: Unify Launch Identity And Validate Deletion Evidence

**Files:**
- Modify: `services/control-plane/internal/server/workspace_launch_activation.go`
- Modify: `services/control-plane/internal/server/workspace_launch_account_stages.go`
- Modify: `services/control-plane/internal/server/workspace_launch_reconciler_test.go`
- Modify: `services/control-plane/internal/clients/ledger.go`
- Modify: `services/control-plane/internal/clients/ledger_test.go`
- Modify: `services/ledger/internal/ledger/types.go`
- Modify: `services/ledger/internal/ledger/store_test.go`
- Modify: `services/ledger/internal/http/server_test.go`
- Modify if required: `services/ledger/internal/ledger/postgres_store_test.go`

1. Add failing Control Plane tests proving new `workspace.created` and
   `billing.workspace_purchased.v1` Receipts carry the same fulfillment
   identity while only the charged Receipt has `Cost`.
2. Add failing Ledger tests for the exact `workspace.deleted.v1` positive shape
   and for cost/refund/supersedes/extra-input/missing-absence/identity failures.
3. Add failing Ledger client tests proving Launch and Delete Receipts require a
   complete exact response round-trip.
4. Run focused Control Plane and Ledger tests and confirm the new cases fail.
5. Extract one provider-neutral Launch fulfillment identity builder and use it
   for both new Launch Receipt types. Keep a named exact legacy
   `workspace.created` matcher for already-persisted schema-v3 Launches; do not
   rewrite any stored Receipt.
6. Add strict Ledger validators for the two Launch shapes and
   `workspace.deleted.v1`. Require empty `Cost` and empty
   `SupersedesReceiptID` for created/deleted Receipts.
7. Extend Control Plane Ledger client exact round-trip validation to
   `workspace.created` and `workspace.deleted.v1`.
8. Run `go test ./internal/ledger -count=1` and `go test ./internal/http -count=1`
   from `services/ledger`, plus the focused Launch/client tests from
   `services/control-plane`.
9. Commit with `feat(ledger): record workspace lifecycle evidence`.

### Task 3: Replace The Refund Delete State Machine

**Files:**
- Modify: `services/control-plane/internal/server/workspace_delete.go`
- Modify: `services/control-plane/internal/server/workspace_delete_test.go`
- Modify: `services/control-plane/internal/server/ent_state_store_workspace.go`
- Modify: `services/control-plane/internal/server/workspace_renewal.go`
- Modify: `services/control-plane/internal/server/renewal_worker.go`
- Modify if required: `services/control-plane/internal/server/table_store.go`
- Modify if required: `services/control-plane/internal/server/memory_table_store_test.go`
- Modify if required: `services/control-plane/internal/server/workspace_renewal_test.go`

1. Replace refund-oriented completion tests with charged and zero-cost Delete
   tests that expect zero financial history/refund calls and one deletion
   Receipt after Workspace absence.
2. Add failing tests for exact Launch Receipt admission, legacy-created Receipt
   read compatibility, historical v1 completed replay, historical v1 active
   conflict before mutation, Receipt-only recovery after Workspace removal,
   and Delete/Renewal durable mutual exclusion.
3. Run the focused Delete/Renewal tests and confirm they fail under v1.
4. Introduce deterministic `workspace.delete.v2` identity and remove debit,
   amount, refund code, refund replay, refund confirmation, and refund Receipt
   fields and branches from the current implementation.
5. Build the v2 claim from the succeeded immutable Launch, its exact charged or
   zero-cost Receipt, and matching Workspace projection. Check v1 before claim
   and fail closed according to the approved historical rules.
6. Preserve the proven Runtime/Secret, attachment, storage, compute, and Key
   readback/replay code while renaming terminal phases to absence semantics.
7. Atomically delete the Workspace with `workspace_absent`, then record the
   deletion Receipt and persist `deletion_receipt_recorded`/`complete` while
   requiring Workspace absence.
8. Make Delete claim reject non-terminal Renewal and Renewal claim/worker reject
   or skip non-terminal v2 Delete in the same durable store transaction.
9. Run all Control Plane Delete/Renewal unit tests, then PostgreSQL-specific CAS
   and restart tests.
10. Commit with `feat(control-plane): complete workspace delete lifecycle`.

### Task 4: Make Tencent Delete Prove Permanent Provider Absence

**Files:**
- Modify: `services/fabric/cmd/opl-tencent-provisioner/main.go`
- Modify: `services/fabric/cmd/opl-tencent-provisioner/main_test.go`
- Modify: `services/fabric/internal/fabric/tencent_provider_storage.go`
- Modify: `services/fabric/internal/fabric/tencent_provider_compute.go`
- Modify: `services/fabric/internal/fabric/tencent_provider_test.go`

1. Replace the retained-CBS test with failing tests for exact CBS deletion,
   already-absent idempotency, ownership mismatch with zero mutation, response
   loss followed by absence, attached/still-present/invalid readback failures.
2. Add failing compute tests proving TKE Machine absence alone is insufficient
   and both Machine and CVM must be absent for `external_deleted`.
3. Run the focused Fabric/provisioner tests and confirm the cases fail.
4. Add `destroy_storage_volume` to `TencentClient`, `cbsNativeAPI`, live action
   dispatch, and mutation admission. Reuse exact pre-read validation, require
   `UNATTACHED`, call `TerminateDisks` once, and bound `DescribeDisks` readback to
   authoritative absence.
5. Make `TencentProvider.DestroyStorageVolume` delete PV/PVC, invoke the new
   action, copy readback facts, and return success only for
   `external_deleted/NOT_FOUND`.
6. After `DeleteClusterMachines(... terminate)` proves Machine absence, perform
   bounded exact CVM readback and report `external_deleted` only when CVM is
   also absent. Preserve those facts in `TencentProvider`.
7. Run all focused Tencent provider and provisioner tests. Verify tests use
   fakes and no environment enables real Tencent mutations.
8. Commit with `fix(fabric): prove tencent workspace resources deleted`.

### Task 5: Prove Local-Docker Capacity Reuse End To End

**Files:**
- Modify: `services/fabric/internal/fabric/local_docker_integration_test.go`
- Modify only if a test exposes a defect: the exact owning Local-Docker file

1. Extend `TestLocalDockerWorkspaceCorePath` or add one adjacent real-Docker
   test that fills a bounded host allocation with Workspace A, restarts the
   Fabric provider/service over the same storage root, deletes A in owner order,
   proves Runtime reservation/storage/compute absence, and creates Workspace B
   using the released CPU, memory, quota, directory, and network capacity.
2. Run the new test with
   `OPL_FABRIC_LOCAL_DOCKER_INTEGRATION=1 go test ./internal/fabric -run TestLocalDockerWorkspaceCorePath -count=1`
   and confirm a deliberately retained A blocks B before the deletion step.
3. Run the final test normally and require it to pass. Do not add fallback
   capacity inference, historical-runtime migration, or cleanup outside the
   test-owned resources.
4. Run all focused Local-Docker capacity, storage, Secret, and integration unit
   tests.
5. Commit with `test(fabric): prove local docker delete releases capacity`.

### Task 6: Reconcile SSOT And Run The Complete Gate

**Files:**
- Modify: `docs/architecture.md`
- Modify: `docs/invariants.md`
- Modify: `docs/implementation-architecture.md`
- Modify: `docs/status.md`
- Modify: `docs/roadmap.md`
- Modify any lower-layer current document found by exact `workspace delete` or
  `automatic refund` search only when it is an active duplicate writer

1. Update architecture and invariants to separate Delete, Cancel Renewal, and
   Refund, name v2/Receipt identities, require lifecycle mutual exclusion, and
   require provider-authoritative permanent absence.
2. Update implementation architecture to describe the exact delivered call
   path and remove the stale statement that Control Plane never invokes Fabric
   destroy operations.
3. Update status only with evidence actually produced by this branch. Remove the
   completed roadmap gap or narrow it to any objectively unproven outcome; do
   not claim deployment or production adoption.
4. Run `rg` across active docs/contracts/source for old Delete refund phases and
   reconcile every current writer while leaving historical provenance intact.
5. Run `gofmt` on changed Go files, `git diff --check`, all focused tests, and
   `npm run verify:local`.
6. Run `npm run verify:local:full`; require one complete green run, including
   PostgreSQL and the real-Docker lifecycle test. Any unrelated one-off failure
   may be diagnosed with a focused rerun, but completion still requires a final
   uninterrupted green full gate.
7. Inspect the final diff for scope, generated churn, secrets, real Tencent
   mutation enablement, and duplicate SSOT statements.
8. Commit with `docs: reconcile workspace delete lifecycle evidence`.
