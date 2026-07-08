# Launch Audit Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Harden OPL Cloud manual-operations launch so Ledger and Fabric remain auditable facts, Control Plane remains a projection/orchestrator, and Console displays backend truth without compatibility layers.

**Architecture:** Ledger owns money facts and price snapshots. Fabric owns resource operation facts and durable resource state. Control Plane reads those facts, projects them for Console, and protects gateway/auth boundaries.

**Tech Stack:** Go services in `services/control-plane`, `services/ledger`, `services/fabric`; React/TypeScript Console in `apps/console-ui`; Node tests/tools in `tests/**` and `tools/**`.

---

## Phase Gate

Before every phase, run:

```bash
rg -n "resourceUsageLogs|requestUsageLogs|usage_logs|resource_usage_logs|request_usage_logs|payment order|self-service payment|compat|compatibility|fallback|demo|token=|priceSnapshot|price_snapshot|fabric_operations|control_plane_read_model|one-person-lab-app|TODO|FIXME" docs packages services apps tests tools --glob '!docs/history/**'
```

Classify hits:

- Active code/docs that contradict current truth must be removed or rewritten.
- Historical docs under `docs/history/**` can remain.
- Tests enforcing old compatibility behavior must be deleted or rewritten.

After every phase, run the smallest relevant eval plus:

```bash
git diff --check
```

If the same blocker repeats three times, stop and report the blocker.

## Phase 1: Gateway/Auth Hardening

**Purpose:** Do not leak Workspace URL tokens, and do not leave operator auth easier to brute force than normal login.

**Files:**
- Modify: `services/control-plane/internal/server/runtime.go`
- Modify: `services/control-plane/internal/server/server.go`
- Test: `services/control-plane/internal/server/server_test.go`

**Implementation:**
- [ ] Add test that `/w/<id>/?token=<token>` sets cookies and redirects to `/w/<id>/` without `token`.
- [ ] Add test that repeated failed `operator-login` returns `429`.
- [ ] Add test that oversized JSON body returns `413` on write APIs.
- [ ] Add test that provider/internal errors returned through Control Plane are stable codes, not raw provider strings.
- [ ] Implement redirect after setting gateway cookies.
- [ ] Reuse the existing per-process login limiter for operator login.
- [ ] Add `http.MaxBytesReader` in JSON decoding.
- [ ] Add one error sanitizer helper for upstream/provider/internal errors.

**Eval:**

```bash
cd services/control-plane
go test -count=1 ./internal/server -run 'Test.*Workspace.*Token|Test.*Operator.*Rate|Test.*Body|Test.*Error'
```

## Phase 2: Ledger Forensic Audit

**Purpose:** Every balance, frozen, available, and spent change must be explainable from Ledger rows.

**Files:**
- Modify: `services/ledger/internal/ledger/types.go`
- Modify: `services/ledger/internal/ledger/store.go`
- Modify: `services/ledger/internal/ledger/memory_store.go`
- Modify: `services/ledger/internal/ledger/postgres_store.go`
- Modify: `services/ledger/internal/http/server.go`
- Test: `services/ledger/internal/ledger/store_test.go`
- Test: `services/ledger/internal/http/server_test.go`
- Modify: `packages/contracts/opl-cloud-billing-ledger-contract.json`

**Implementation:**
- [ ] Add wallet-after fields to wallet transactions: frozen, available, total spent.
- [ ] Add settlement evidence fields: pricing version, price snapshot, usage period, quantity, unit, provider cost evidence ref.
- [ ] Require settlement inputs to carry the price/evidence snapshot.
- [ ] Persist the fields in memory and Postgres stores.
- [ ] Return the fields through HTTP responses.
- [ ] Update the contract to match the actual schema.

**Eval:**

```bash
cd services/ledger
go test -count=1 ./internal/ledger ./internal/http
```

## Phase 3: Fabric Durable Resource State

**Purpose:** Fabric operation facts already exist; current resource state must also survive service restart.

**Files:**
- Modify: `services/fabric/internal/fabric/types.go`
- Modify: `services/fabric/internal/fabric/operation_store.go`
- Modify: `services/fabric/internal/fabric/service.go`
- Modify: `services/fabric/internal/http/server.go`
- Test: `services/fabric/internal/fabric/service_test.go`
- Test: `services/fabric/internal/fabric/operation_store_test.go`
- Test: `services/fabric/internal/http/server_test.go`

**Implementation:**
- [ ] Add operation-store backed resource replay for compute, storage, attachment, and runtime latest state.
- [ ] Load replayed state when creating Fabric service with an operation store.
- [ ] Keep current maps as in-process cache only.
- [ ] Add tests that a new service with the same operation store can read resources created by the old service.

**Eval:**

```bash
cd services/fabric
go test -count=1 ./internal/fabric ./internal/http
```

## Phase 4: Provider Cost Tags

**Purpose:** Tencent bills must be traceable back to OPL account/workspace/resource/operation IDs.

**Files:**
- Modify: `services/fabric/internal/fabric/tencent_provider.go`
- Modify: `services/fabric/internal/fabric/types.go`
- Modify: `services/ledger/ops/reconcile-tencent-bills.ts`
- Test: `services/fabric/internal/fabric/tencent_provider_test.go`
- Test: `tests/billing/billing-reconciliation.test.ts`

**Implementation:**
- [ ] Add OPL cost tag fields for account, workspace, resource, operation.
- [ ] Include tags in provider metadata/payload where this code generates Tencent/K8s resources.
- [ ] Ensure reconciler can normalize those tags into resource identity.
- [ ] Tests must assert tags are present and do not include tokens/secrets.

**Eval:**

```bash
cd services/fabric && go test -count=1 ./internal/fabric -run 'Test.*Tencent|Test.*Tag'
npm test -- --run tests/billing/billing-reconciliation.test.ts
```

## Phase 5: Control Plane Truth Reads

**Purpose:** Console projections should read Ledger/Fabric facts instead of inventing money/resource state.

**Files:**
- Modify: `services/control-plane/internal/clients/ledger.go`
- Modify: `services/ledger/internal/http/server.go`
- Modify: `services/control-plane/internal/controlplane/service.go`
- Modify: `services/control-plane/internal/server/runtime.go`
- Test: `services/control-plane/internal/clients/ledger_test.go`
- Test: `services/control-plane/internal/server/server_test.go`

**Implementation:**
- [ ] Add Ledger read endpoints for wallet, ledger entries, wallet transactions, and settlements.
- [ ] Add Control Plane LedgerClient read methods.
- [ ] Refresh read model money fields from Ledger read methods when building state.
- [ ] Keep projection fields explicitly generated by Control Plane.
- [ ] Tests must prove `/api/state` and `/api/management/state` expose Ledger facts after refresh.

**Eval:**

```bash
cd services/control-plane
go test -count=1 ./internal/clients ./internal/controlplane ./internal/server -run 'Test.*Ledger|Test.*State|Test.*Management'
```

## Phase 6: Admin Evidence UI

**Purpose:** Admin can locate why money moved and which provider operation/resource caused it without guessing.

**Files:**
- Modify: `apps/console-ui/src/pages/admin/AdminOverviewPage.tsx`
- Modify: `apps/console-ui/src/pages/billing/BillingPage.tsx`
- Modify: `apps/console-ui/src/pages/shared/formatters.ts`
- Test: `tests/ui/commercial-console-surface.test.ts`
- Test: `tests/production/production-verifier.test.ts`

**Implementation:**
- [ ] Display settlement price snapshot and wallet-after fields on admin ledger.
- [ ] Display Fabric operation ID, provider request ID, and cost tag fields on admin resource views.
- [ ] Keep the UI layout stable; no shell redesign.
- [ ] Tests must forbid resurrecting `resourceUsageLogs`.

**Eval:**

```bash
npm test -- --run tests/ui/commercial-console-surface.test.ts tests/production/production-verifier.test.ts
```

## Phase 7: Rollout E2E Gate

**Purpose:** Production verification must prove the new audit facts, not just route reachability.

**Files:**
- Modify: `tools/production-verifier.ts`
- Modify: `.github/workflows/verify-production-chain.yml`
- Modify: `artifacts/phase10-rollout-evidence.md`
- Test: `tests/production/production-verifier.test.ts`

**Implementation:**
- [ ] Verifier checks clean Workspace URL after token handoff.
- [ ] Verifier checks Ledger settlement price snapshot and wallet-after fields.
- [ ] Verifier checks Fabric durable operation/resource evidence.
- [ ] Verifier checks provider tag fields are present in backend state.
- [ ] Record rollout command and expected GitHub workflow sequence in evidence doc.

**Eval:**

```bash
npm test -- --run tests/production/production-verifier.test.ts
```

## Final Gates

Run before merge:

```bash
npm test
npm run build
(cd services/control-plane && go test -count=1 ./...)
(cd services/ledger && go test -count=1 ./...)
(cd services/fabric && go test -count=1 ./...)
sentrux check .
git diff --check
```

Merge back to `commercial-chain-closure`, remove the feature worktree, push, then trigger the GitHub rollout sequence:

1. `Release OPL Cloud Image`
2. `Deploy TKE Production`
3. `Diagnose TKE Production`
4. `Verify Production Chain`
