# Workspace Launch Operation Diagnostics Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a strict, redacted operator-health diagnostic that identifies why the current workspace launch decoder rejected persisted runtime state.

**Architecture:** Attach stable internal categories to the existing strict decoder errors, then publish bounded structural summaries in a new health envelope beside `controlledBasicPilot`. Preserve all admission behavior and avoid any persistence or provider mutation.

**Tech Stack:** Go, `net/http`, `encoding/json`, SHA-256, existing Control Plane table store and test fixtures.

---

### Task 1: Specify Decoder Failure Categories

**Files:**
- Modify: `services/control-plane/internal/server/workspace_launch_reconciler.go`
- Test: `services/control-plane/internal/server/workspace_launch_reconciler_test.go`

1. Add table-driven tests for malformed JSON, schema mismatch, missing attempts,
   forbidden legacy fields, missing canonical facts, row identity mismatch, and
   status/stage mismatch.
2. Verify each error still matches `errInvalidWorkspaceLaunchOperation`.
3. Add a private categorized error wrapper and annotate decoder rejection sites.
4. Run the focused reconciler tests.

### Task 2: Build Redacted Structural Summaries

**Files:**
- Modify: `services/control-plane/internal/server/workspace_launch_admission.go`
- Test: `services/control-plane/internal/server/workspace_launch_monthly_preflight_test.go`

1. Add tests for identity hashing, bounded keys/counts, aggregate attempt and
   observation summaries, missing keys, and forbidden keys.
2. Assert raw IDs and arbitrary result values are absent.
3. Implement the bounded summary builder using structured JSON parsing.
4. Run the focused Pilot metrics tests.

### Task 3: Publish a Sibling Health Envelope

**Files:**
- Modify: `services/control-plane/internal/server/routes_admin.go`
- Test: `services/control-plane/internal/server/operator_projection_test.go`

1. Add a health-route test proving `controlledBasicPilot.data` retains its exact
   current keys.
2. Add a test for the sibling `workspaceLaunchOperationDiagnostics` envelope.
3. Wire the read-only summary into `operatorHealth`.
4. Run focused operator projection tests.

### Task 4: Verify the Control Plane

**Files:**
- Modify only if required by verification findings.

1. Run all focused decoder, Pilot, and operator health tests.
2. Run `go test ./internal/server` with the repository's applicable PostgreSQL
   test environment.
3. Run `npm run verify:local:full` because the behavior projects persisted state.
4. Run `git diff --check` and inspect the final diff for identity leakage.

### Task 5: Candidate and Protected Readback

**Files:**
- No additional source change unless verification finds a defect.

1. Build a new immutable Cloud Candidate from the exact accepted SHA.
2. Have Instance deploy the Candidate through its protected workflow.
3. Read the sibling diagnostic envelope without running admission.
4. Choose migration, canonical repair, or authorized disposable reset from the
   returned failure category.
5. After Cloud health reports no decode failures, rerun Pilot diagnostic and
   then Verify admission, stopping at `admission_ready`.
