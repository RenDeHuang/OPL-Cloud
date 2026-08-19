# Local-Docker Runtime Reservation Reconciliation Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Reconcile existing Local-Docker runtimes against their durable immutable reservations instead of the current Provider profile.

**Architecture:** Keep capacity admission under the existing storage-root flock and make lock acquisition honor the caller context. Change the runtime inventory validation source of truth: validate an existing reservation against exact live labels and cgroups, or recover a missing reservation from positive exact live cgroup limits. Never resolve an existing runtime through the current Provider profile.

**Tech Stack:** Go, Docker inspect readback, Fabric Local-Docker adapter, focused Go tests.

---

### Task 1: Specify immutable reservation reconciliation

**Files:**
- Modify: `services/fabric/internal/fabric/local_docker_capacity_test.go`

1. Add a failing test where an existing `2c4g` reservation and live container are inspected by a Provider whose current `basic` profile is `4c8g`; verify the old reservation is counted and a new request uses its own admitted values.
2. Add a failing test where a bounded live container has no reservation; verify its exact live limits are persisted even when the current profile differs.
3. Add a failing test where an unbounded legacy container has no reservation; verify inventory fails and no reservation is synthesized.
4. Add a failing test where a non-canonical container name carries otherwise valid runtime labels; verify it cannot escape capacity accounting.
5. Add a failing Runtime status test where an existing reservation remains `2c4g` after the current profile changes to `4c8g`.
6. Run the focused tests and confirm the profile-drift and canonical-identity tests fail under the current implementation.

### Task 2: Make reservation the inventory authority

**Files:**
- Modify: `services/fabric/internal/fabric/local_docker_runtime_capacity.go`
- Modify: `services/fabric/internal/fabric/local_docker_runtime.go`

1. Add a helper that converts a valid reservation into exact runtime cgroup limits.
2. Add one locked reconciliation helper that requires the canonical container name and deterministic Workspace/resource identity before loading or recovering a reservation.
3. Require reservation Workspace, resource, and package identities to match the live labels and reject duplicate resource identities in inventory.
4. Compare `NanoCPUs`, `Memory`, and `MemorySwap` to the reservation values without consulting the current profile.
5. When the reservation is absent, require positive exact live limits and persist those values; reject unbounded or incomplete live limits.
6. Make public Local-Docker Runtime status use the same locked reservation reconciliation before its existing storage, Secret, network, and health readback.
7. Keep the complete status readback inside that lock and make lock acquisition return the caller's context error when its deadline expires.
8. Run the focused tests and confirm they pass, including a held-lock deadline test.

### Task 3: Verify the Fabric boundary

**Files:**
- Modify if required: `docs/implementation-architecture.md`
- Modify if required: `docs/status.md`
- Modify if required: `docs/roadmap.md`

1. Run all Local-Docker capacity and integration unit tests without the real-Docker environment flag.
2. Run `go test ./internal/fabric -count=1`.
3. Run `npm run verify:local`, then retire the four historical test Runtimes only through the separately authorized local-environment operation before rerunning `npm run verify:local:full`.
4. Confirm the real-Docker path no longer stops at `local_docker_runtime_reservation_inventory_invalid`; record any later aggregate failure exactly and require one final green full gate at integration.
5. Run `git diff --check` and inspect the exact diff.
6. Keep historical resource retirement outside this source change; do not add migration or cleanup behavior to Fabric reservation reconciliation.
