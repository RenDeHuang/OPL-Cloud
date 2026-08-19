# Open MVP Provider And Gateway Budget Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Deliver provider-driven Basic/Pro availability and a Workspace-scoped Sub2API budget workflow for the customer-owned open MVP.

**Architecture:** Fabric provider profiles remain the package availability authority, Control Plane owns launch admission and Workspace-scoped budget orchestration, and Console consumes only Control Plane APIs. Sub2API remains the live Key/quota/wallet authority; Key rotation freezes the old Key and transfers only provable remaining quota under the same Workspace lock, never creating an implicit unlimited replacement.

**Tech Stack:** Go, Ent/PostgreSQL, TypeScript/React, Node test runner, typed HTTP JSON contracts, Sub2API client.

---

### Task 1: Prove and close provider package availability

**Files:**
- Modify: `apps/console-ui/src/pages/CustomerPages.tsx`
- Modify: `services/control-plane/internal/server/server_test.go`
- Modify: `services/control-plane/internal/server/pricing_monthly_test.go`
- Modify: `services/fabric/internal/fabric/local_docker_provider_test.go`
- Modify: `services/fabric/internal/fabric/tencent_provider_test.go`
- Modify: `tests/ui/customer-console-flow.test.ts`

1. Add failing tests for Basic-only, Pro-only, and Basic+Pro provider profiles.
2. Add failing customer-owned tests proving available packages admit launch and
   both packages quote zero Cloud resource fees.
3. Add a failing UI test proving `available=false` catalog rows are absent.
4. Filter unavailable rows before rendering package controls without adding a
   second availability policy.
5. Run the focused Fabric, Control Plane, and UI tests and commit.

### Task 2: Add Workspace-scoped Gateway budget API

**Files:**
- Create: `services/control-plane/internal/server/workspace_gateway_budget.go`
- Create: `services/control-plane/internal/server/workspace_gateway_budget_test.go`
- Modify: `services/control-plane/internal/server/routes_workspace.go`
- Modify: `services/control-plane/internal/server/workspace_gateway.go`
- Modify: `services/control-plane/internal/server/source_truth_gateway_test.go`

1. Add failing GET tests for exact session-account Workspace ownership, exact
   `workspaceApiKeyId` binding, missing Key, and unavailable live readback.
2. Add failing PATCH tests for every allowed field, reset operations, rejected
   unknown fields, and cross-account access.
3. Implement strict request decoding and customer-safe response projection.
4. Serialize PATCH with the existing Workspace resource lock and call the
   existing Sub2API client instead of persisting budget state.
5. Keep generic Workspace-reserved Key rename/group/delete protections intact.
6. Run focused Control Plane tests and commit.

### Task 3: Preserve budget through Workspace Key rotation

**Files:**
- Modify: `services/control-plane/internal/server/workspace_gateway.go`
- Modify: `services/control-plane/internal/server/workspace_gateway_budget_test.go`
- Modify: the existing Workspace Gateway rotation test owner identified by the
  current call path.

1. Add failing tests proving an active Key with quota `Q` and final usage `U`
   creates a replacement with quota `Q-U`, while `U >= Q` never creates one.
2. Add failing tests proving rotation disables the old Key, observes zero
   concurrency, and captures final counters before replacement creation.
3. Add failing admission tests for non-zero finite rolling-window usage,
   disabled/quota-exhausted/expired status, and explicit expiry.
4. Reuse the Workspace lock for budget mutation and rotation and reject either
   operation while the other has non-terminal durable evidence.
5. Persist only recovery evidence, create the replacement with the admitted
   remaining quota and rolling limits, and verify exact live policy readback
   before Secret/binding transition.
6. Extend every-phase response-loss and restart tests through disable, drain,
   snapshot, and replacement policy readback; run focused tests and commit.

### Task 4: Connect Console and admit the public interface

**Files:**
- Modify: `apps/console-ui/src/api/dtos.ts`
- Modify: `apps/console-ui/src/api/workspaces-api.ts`
- Modify: `apps/console-ui/src/app/use-console-controller.ts`
- Modify: `apps/console-ui/src/pages/CustomerPages.tsx`
- Modify: `packages/contracts/opl-cloud-console-source-truth-contract.json`
- Modify: `tests/contracts/source-truth-contract.test.ts`
- Modify: `tests/ui/customer-console-flow.test.ts`
- Modify: `tests/ui/gateway-request-lifecycle.test.ts`

1. Add failing contract and adapter tests for the GET/PATCH routes and exact
   allowlists.
2. Add typed DTOs and same-origin Workspace API methods.
3. Add controller load/update state with explicit source failure handling.
4. Add Workspace detail budget controls for quotas, rolling limits, enabled
   state, and explicit resets.
5. Run typecheck, focused Node tests, and focused Control Plane tests; commit.

### Task 5: Integrated verification and review

**Files:**
- Modify only files required by verified defects.

1. Run `npm run typecheck` and all focused changed-area tests.
2. Run `npm run verify:local:full` once on the integrated candidate.
3. Review the diff for SSOT, authority, secret exposure, cross-account access,
   race, idempotency, and rotation-continuity violations.
4. Fix verified defects, rerun their focused tests, then rerun the aggregate
   gate only if an aggregate-covered behavior changed after the first run.
5. Record exact test evidence and remaining runtime qualification gaps.
