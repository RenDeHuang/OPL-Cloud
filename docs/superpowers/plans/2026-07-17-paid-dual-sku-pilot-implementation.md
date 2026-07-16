# Paid Dual-SKU Pilot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the code, security, recovery, release, and runtime-evidence gaps required to sell invited Basic and Pro Workspaces with optional Workspace-level automatic renewal.

**Architecture:** Keep the existing four owner lanes. Control Plane remains the durable launch, renewal, entitlement, and review state owner; Fabric remains the only Tencent/Kubernetes writer; Sub2API remains the only balance, Key, model-routing, and usage owner; Ledger validates and stores append-only evidence. Reuse the existing runtime-operation tables, workers, provider locks, and Vue application; add no service, queue, wallet, or frontend.

**Tech Stack:** Go 1.22, Ent/PostgreSQL, `net/http`, Vue 3/TypeScript, Node test runner, Kubernetes/TKE, Tencent CVM/CBS, Sub2API, GitHub Actions.

---

## Frozen Product Contract

- Invite-only paid Pilot, initially 1-10 accounts.
- Basic: 2c4g + 10GB, `52_580_000` USD micros/month.
- Pro: 8c16g + 100GB, `240_080_000` USD micros/month.
- Price version: `pilot-usd-2026-07-v1`, effective `2026-07-17T00:00:00Z`.
- Automatic renewal is one Workspace-level boolean, defaults off, and may be enabled by the owner.
- Workspace files at rest remain only on CBS. Platform PostgreSQL and Ledger never store Workspace file bodies, prompts, or model responses.
- OPL does not provide Workspace-file backups and never deletes CBS on ordinary expiry, release, QA, or rollback. Tencent's provider-side expiry retention is not an OPL recovery guarantee.
- Basic and Pro each require one retained non-customer Acceptance slot before sale.

## What Already Exists

| Capability | Existing implementation to reuse | Decision |
|---|---|---|
| Durable launch | `workspaceLaunchOperation`, `runWorkspaceLaunch`, RuntimeOperation persistence | Extend, do not replace |
| Debit/refund | Sub2API deterministic Redeem Code clients and monthly billing helpers | Reuse stable identities |
| Provider mutation | Fabric PREPAID CVM/CBS prepare, sync, renew, cleanup | Reuse; add stale apply convergence |
| Cross-Pod serialization | Fabric `OperationStore.WithPoolLock` and runtime claims | Reuse; no queue |
| Evidence | Ledger memory/PostgreSQL stores with idempotent receipts | Tighten owner-side schema |
| Identity | Session/CSRF, Account/User/Organization/Membership entities | Add one atomic invited-account transaction |
| Usage | Sub2API request usage and aggregate stats adapters | Keep response names stable |
| Credential access | Owner-only Runtime reveal/rotate POST routes | Keep; fix release QA client |
| Release | Immutable image workflows, grouped rollback, fixed-slot Acceptance | Generalize from one slot to Basic + Pro |

## NOT In Scope

- Serve, GPU, SSO, self-registration, public signup, multi-Workspace accounts, Connect, Package management, and cross-Zone HA.
- A second wallet, Gateway service, usage database, billing-fact table, queue, or frontend.
- Platform-managed Workspace file backup or copying CBS contents to PostgreSQL.
- Per-release Tencent purchases, renewals, or deletion. Provider mutations remain an explicit Acceptance workflow.
- Email infrastructure. The Pilot operator handles renewal reminders for 1-10 accounts using verified email and records an audit reference.
- Silent Pilot price changes. A future price version requires a separate accepted quote.

## Data Flow

```text
Vue Console
   |
   | POST /api/workspace-launches (one mutation key)
   v
Control Plane: validate owner + quote + Fabric read-only preflight
   |             + Sub2API mapped user + exactly one active Key + balance
   |
   | persist workspace.launch operation
   v
worker: debit -> PREPAID CVM -> PREPAID CBS -> attachment
        -> account Key Secret -> Runtime -> Workspace -> Ledger receipts
          |            |             |                       |
          v            v             v                       v
       Sub2API       Fabric      Kubernetes               Ledger
     balance owner  cloud writer  CBS-mounted runtime     evidence only

Workspace file body: browser/runtime <-> CBS mount only
Model request body: runtime -> HTTPS Gateway/provider, transient only
Platform PostgreSQL: identities, resource references, operation state, audit
```

## Renewal State Machine

```text
Workspace(autoRenew, priceVersion, paidThrough)
       |
       | hourly scan at paidThrough - 24h
       v
 scheduled --DB claim(workspaceId, paidThrough)--> claimed
    | owner disables before claim                     |
    +---------------------> cancelled                 v
                                                  debit_pending
                                                   /        \
                                    insufficient_balance    debited
                                          | retry same key       |
                                          | until paidThrough    v
                                          +----------------> provider_renewing
                                                               |
                                       CVM + CBS same IDs, deadlines read back
                                                               v
                                                           verifying
                                                          /         \
                                                   manual_review    active
                                                        |             |
                                                   operator           +-> one Ledger receipt
                                                   evidence               next period

At paidThrough without success: expired_unpaid -> stop compute, deny access,
do not call CBS delete. Reactivation is a separate explicit operation and may
reuse storage only after Fabric proves the original disk ID still exists.
```

## UI / Backend Ownership

The backend branch must not edit these files until the UI window supplies a commit hash:

- `apps/console-ui/src/App.vue`
- `apps/console-ui/src/api/console-read-api.ts`
- `apps/console-ui/src/console-model.ts`
- `apps/console-ui/src/styles.css`
- `tests/ui/vue-console-model.test.ts`
- `tests/ui/vue-console-surface.test.ts`

Backend contracts are frozen in [the shared handoff](/home/dev/.gstack/projects/RenDeHuang-OPL-Cloud/pilot-ui-backend-handoff.md). Exchange commits only; never copy uncommitted UI files between worktrees.

## Task 1: Make Ledger Enforce Its Own Trust Boundary

**Files:**
- Modify: `services/ledger/internal/ledger/types.go:535`
- Modify: `services/ledger/internal/ledger/memory_store.go:418`
- Modify: `services/ledger/internal/ledger/postgres_store.go:654`
- Modify: `services/ledger/internal/ledger/store_test.go`
- Modify: `services/ledger/internal/ledger/postgres_store_test.go`
- Modify: `services/ledger/internal/http/server_test.go`
- Modify: `services/control-plane/internal/clients/ledger.go`
- Modify: `services/control-plane/internal/clients/ledger_test.go`
- Modify: `services/control-plane/internal/server/customer_facts_test.go`
- Modify: `packages/contracts/opl-cloud-evidence-ledger-contract.json`
- Modify: `packages/contracts/opl-cloud-billing-ledger-contract.json`

- [ ] **Step 1: Write failing receipt-schema tests**

Add table tests which submit every billing receipt type without each required cost field, with fractional/negative money, and with nested mixed-case `apiKey`, `adminToken`, `rawSub2apiResponse`, `rawProviderResponse`, `password`, and `token` keys. Assert `ErrInvalidReceiptInput` in both memory and PostgreSQL stores.

```go
input := validBillingReceiptInput()
delete(input.Cost, "priceVersion")
if _, err := store.RecordReceipt(ctx, input); !errors.Is(err, ErrInvalidReceiptInput) {
    t.Fatalf("missing priceVersion error = %v", err)
}
```

- [ ] **Step 2: Write failing reconciliation-schema tests**

Require a non-empty idempotency key, `id`, status `ok|mismatch`, integer non-negative counts, an exception array with allowlisted codes and opaque resource IDs, and no sensitive keys. Invalid reports must not persist or block purchases. The Control Plane client must also reject a Ledger response whose report differs from the submitted report or whose status/guard fields are internally inconsistent; a malformed response must not replace the last valid purchase guard.

- [ ] **Step 3: Run RED tests**

Run: `cd services/ledger && go test ./internal/ledger ./internal/http -run 'Test(BillingReceiptSchema|ReceiptRejectsSensitive|ReconciliationSchema)' -count=1`

Run: `cd services/control-plane && go test ./internal/clients ./internal/server -run Reconciliation -count=1`

Expected: FAIL because billing cost and reconciliation report shapes are currently accepted.

- [ ] **Step 4: Add the minimum shared validators**

Implement `validateBillingCost`, a recursive case-insensitive forbidden-key walk, and `validateReconciliationInput`; call them from the common store methods so memory and PostgreSQL behavior cannot drift.

```go
func validateReceiptInput(input ReceiptInput) error {
    if /* existing base checks */ || containsForbiddenReceiptKey(input) {
        return ErrInvalidReceiptInput
    }
    if isBillingReceiptType(input.Type) && !validBillingCost(input.Cost) {
        return ErrInvalidReceiptInput
    }
    return nil
}
```

- [ ] **Step 5: Run GREEN tests and the full Ledger suite**

Run: `cd services/ledger && go test ./... -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add services/ledger services/control-plane/internal/clients services/control-plane/internal/server/customer_facts_test.go packages/contracts/opl-cloud-evidence-ledger-contract.json packages/contracts/opl-cloud-billing-ledger-contract.json
git commit -m "fix(ledger): enforce billing evidence schema"
```

## Task 2: Fail Closed on Gateway Key Pagination and Move Reveal to POST

**Files:**
- Modify: `services/control-plane/internal/clients/sub2api.go:252`
- Modify: `services/control-plane/internal/clients/sub2api_test.go`
- Modify: `services/control-plane/internal/server/routes_gateway.go:87`
- Modify: `services/control-plane/internal/server/console_tenant_isolation_test.go`
- Modify: `services/control-plane/internal/server/routes_workspace_launch.go:97`
- Modify: `services/control-plane/internal/server/workspace_launch_test.go`
- Modify: `packages/contracts/opl-cloud-launch-freeze-contract.json`

- [ ] **Step 1: Write failing pagination tests**

Cover inconsistent `total/pages/page_size`, changing metadata between pages, too many items, early empty pages, duplicate Key IDs, and a final collected count different from `total`.

```go
// Page 1 claims total=2/pages=2; page 2 changes total to 1.
if _, err := client.WorkspaceKey(ctx, 41); err == nil {
    t.Fatal("inconsistent pagination was accepted")
}
```

- [ ] **Step 2: Run RED client tests**

Run: `cd services/control-plane && go test ./internal/clients -run WorkspaceKey -count=1`

Expected: FAIL on metadata consistency/cardinality cases.

- [ ] **Step 3: Validate one coherent full listing**

Track first-page `total`, `pages`, and `page_size`; require every page to match, require `len(items) <= page_size`, reject duplicate IDs, and require collected item count to equal `total` before selecting exactly one active `opl-workspace` Key.

- [ ] **Step 4: Write failing route tests for the reveal contract**

Assert ordinary GET ignores/rejects `reveal=true` and never returns raw Key. Assert only owner + CSRF can call `POST /api/gateway/keys/opl-workspace/reveal`, response is `private, no-store`, member/operator/cross-account calls fail, and audit contains no Key.

- [ ] **Step 5: Implement POST reveal and launch preflight**

Keep `GET /api/gateway/summary` masked-only. Add the owner-only POST route using the session-derived account. In `POST /api/workspace-launches`, call `service.GatewaySummary` after the two Fabric read-only preflights and before balance/persistence; map missing/ambiguous Key to a clear conflict and all other failures to unavailable.

```go
if _, err := service.GatewaySummary(r.Context(), sub2APIUserID); err != nil {
    writeGatewaySummaryError(w, err)
    return
}
```

- [ ] **Step 6: Run GREEN tests**

Run: `cd services/control-plane && go test ./internal/clients ./internal/server -run 'Gateway|WorkspaceLaunch' -count=1`

Expected: PASS, with launch event order `fabric preflight x2 -> workspace Key -> balance -> persist`.

- [ ] **Step 7: Confirm 409 replay from authoritative balance history**

When create-and-redeem returns 409, query the existing bounded `BalanceHistory` adapter with the same code. Exactly one history item with the same user, type, used status, and signed integer amount confirms replay; a missing/ambiguous/mismatched item remains unknown/conflict. Apply the same rule to refund. This does not add an API or trust a 409 by itself.

```go
if httpError.StatusCode == http.StatusConflict {
    return c.confirmAdjustmentReplay(ctx, userID, code, -chargeUSDMicros)
}
```

Add tests for exact confirmation, different amount, missing item, duplicate item, and unavailable history. Update the monthly purchase/renewal tests to prove a confirmed replay never repeats provider mutation.

- [ ] **Step 8: Commit**

```bash
git add services/control-plane packages/contracts/opl-cloud-launch-freeze-contract.json
git commit -m "fix(gateway): validate keys before paid launch"
```

## Task 3: Create Invited Accounts Atomically

**Files:**
- Modify: `services/control-plane/internal/server/table_store.go:77`
- Modify: `services/control-plane/internal/server/memory_table_store.go`
- Modify: `services/control-plane/internal/server/ent_state_store.go`
- Modify: `services/control-plane/internal/server/auth_accounts.go:22`
- Modify: `services/control-plane/internal/server/routes_admin.go:55`
- Modify: `services/control-plane/ent/schema/shared.go:41`
- Create: `services/control-plane/migrations/202607170001_invited_account_identity.sql`
- Modify: `services/control-plane/migrations/migrations.go`
- Modify: `services/control-plane/migrations/migrations_test.go`
- Modify: `services/control-plane/internal/server/ent_state_store_test.go`
- Modify: `services/control-plane/internal/server/monthly_billing_test.go`
- Modify: `services/control-plane/internal/server/console_tenant_isolation_test.go`
- Modify: `packages/contracts/opl-cloud-management-contract.json`

- [ ] **Step 1: Write failing atomicity and normalization tests**

Assert one `POST /api/users` produces Account, User, Organization, and owner Membership; inject failure at each write in the memory store and assert no row survives. Assert trimmed/lowercase email uniqueness, password length at least 12, and disabled/deleted bootstrap users remain non-active after restart.

- [ ] **Step 2: Run RED tests**

Run: `cd services/control-plane && go test ./internal/server -run 'InvitedAccount|Bootstrap.*Disabled|PasswordStrength|NormalizedEmail' -count=1`

Expected: FAIL because `createUser` currently saves Account and User separately and creates no organization/membership.

- [ ] **Step 3: Add one store transaction method**

Extend `controlPlaneTableStore` with a single operation accepting four canonical rows. The memory implementation holds one lock and commits cloned maps only after all validation. The PostgreSQL implementation uses one Ent transaction and maps unique-email/Sub2API conflicts to stable domain errors.

```go
CreateInvitedAccount(ctx context.Context, account, user, organization, membership map[string]any) error
```

- [ ] **Step 4: Normalize input and enforce the database constraint**

Normalize email with `strings.ToLower(strings.TrimSpace(email))`, reject empty/invalid account IDs and weak passwords before any write, and add a unique index on `lower(btrim(email))`. The migration must fail closed if legacy rows collide.

- [ ] **Step 5: Preserve lifecycle state during bootstrap**

When a seed matches an existing user, never overwrite `disabled` or `deleted` with seed `active`; never restore revoked membership implicitly. Disabling/deleting an owner must revoke sessions and turn off the Workspace's future automatic renewal intent.

- [ ] **Step 6: Run PostgreSQL and route tests**

Run: `cd services/control-plane && go test ./internal/server ./migrations -count=1`

Expected: PASS. With `CONTROL_PLANE_TEST_DATABASE_URL` set, the transaction and unique-index cases must execute rather than skip.

- [ ] **Step 7: Commit**

```bash
git add services/control-plane packages/contracts/opl-cloud-management-contract.json
git commit -m "fix(control-plane): provision invited owners atomically"
```

## Task 4: Recover Stale Fabric Attachment, Runtime, and Secret Claims

**Files:**
- Modify: `services/fabric/internal/fabric/service.go:891`
- Modify: `services/fabric/internal/fabric/operation_store.go:139`
- Modify: `services/fabric/internal/fabric/operation_store_test.go`
- Modify: `services/fabric/internal/fabric/service_test.go`
- Modify: `services/fabric/internal/fabric/postgres_runtime_integration_test.go`
- Modify: `services/fabric/internal/fabric/tencent_provider.go:631`
- Modify: `services/fabric/internal/fabric/tencent_provider_test.go`
- Modify: `packages/contracts/opl-cloud-launch-freeze-contract.json`

- [ ] **Step 1: Replace the three anti-recovery tests with failing convergence tests**

For attachment, Runtime, and Gateway Secret: seed a matching `started` claim older than the stale threshold, simulate provider success followed by persistence failure, create a fresh Service, replay the same request, and assert one converged `succeeded` operation with stable IDs. A fresh claim must still return in-progress.

- [ ] **Step 2: Run RED tests**

Run: `cd services/fabric && go test ./internal/fabric -run 'Stale|IncompleteOperation' -count=1`

Expected: FAIL because persisted `started` is currently terminal in-progress.

- [ ] **Step 3: Add atomic stale reclaim with fencing**

Extend the existing runtime claim/store protocol with an atomic compare-and-swap reclaim based on the prior `StartedAt`. The new `StartedAt` is the fencing token: completion is accepted only from the currently stored token, so an old process returning late cannot overwrite the reclaimed result. The schema already stores `started_at`; no migration is needed.

```go
const runtimeClaimStaleAfter = 2 * time.Minute

if stored.Status == "started" && s.now().Sub(stored.StartedAt) < runtimeClaimStaleAfter {
    return ErrRuntimeOperationInProgress
}
reclaimed, won, err := operations.ReclaimRuntime(ctx, stored.ID, stored.StartedAt, s.now())
```

- [ ] **Step 4: Reapply only the three safe Kubernetes operations**

After winning reclaim, call the existing provider apply/readback with the same deterministic names and operation ID. Keep fresh claims in-progress and keep unsafe provider actions non-reclaimable. Derive Attachment's logical ID from the stable operation identity so readback cannot invent a second attachment ID. Do not add a queue or a second operation table.

- [ ] **Step 5: Prove cross-instance behavior with PostgreSQL**

Use two Service instances sharing `PostgresOperationStore`; only one may reclaim/reapply, the old owner's late save must fail its fence, and both callers must replay the same final result.

- [ ] **Step 6: Run full Fabric tests**

Run: `cd services/fabric && go test ./... -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add services/fabric packages/contracts/opl-cloud-launch-freeze-contract.json
git commit -m "fix(fabric): converge stale runtime claims"
```

## Task 5: Freeze Exact USD Prices and Complete Durable Launch Recovery

**Files:**
- Modify: `services/control-plane/internal/server/pricing.go`
- Modify: `services/control-plane/internal/server/pricing_monthly_test.go`
- Modify: `services/control-plane/internal/server/workspace_launch.go`
- Modify: `services/control-plane/internal/server/routes_workspace_launch.go`
- Modify: `services/control-plane/internal/server/workspace_launch_test.go`
- Modify: `services/control-plane/internal/server/billing_review_resolution_test.go`
- Modify: `packages/contracts/opl-cloud-pricing-contract.json`
- Modify: `packages/contracts/opl-cloud-launch-freeze-contract.json`
- Modify: `tests/contracts/monthly-billing-hard-cut.test.ts`
- Modify: `tests/contracts/launch-architecture-freeze.test.ts`

- [ ] **Step 1: Write failing price tests**

Assert Basic compute/storage/total `50_000_000 + 2_580_000 = 52_580_000`; Pro `214_280_000 + 25_800_000 = 240_080_000`; only Basic+10GB and Pro+100GB are valid Workspace launch combinations; all DTOs use `pilot-usd-2026-07-v1` and `USD`.

- [ ] **Step 2: Run RED price tests**

Run: `cd services/control-plane && go test ./internal/server -run Pricing -count=1`

Expected: FAIL on the old CNY-derived values/version.

- [ ] **Step 3: Replace conversion-derived Pilot prices with fixed integer USD facts**

Keep any provider CNY cost as internal evidence only. The customer quote and charge use the exact fixed USD micros above; the browser never recomputes totals.

- [ ] **Step 4: Add `autoRenew` to the launch fingerprint and response**

Require a boolean `autoRenew` in the POST body, include it in `RequestHash`, return it from list/detail, and pass it to both child purchase inputs only as a read-only compatibility projection until Workspace activation persists the canonical intent.

- [ ] **Step 5: Write failing manual-review resume tests**

Create a launch whose child compute enters manual review, resolve the child through the existing operator endpoint, run the launch worker again, and assert the same parent operation resumes from `compute` without a new debit or provider purchase.

- [ ] **Step 6: Make parent review state derived and resumable**

`manual_review` remains visible but is no longer an unconditionally terminal parent state. The worker skips it while the current child remains under review; once the same child becomes active/refunded/terminal, it either resumes the same phase or ends with the corresponding explicit terminal status.

- [ ] **Step 7: Run launch and billing tests**

Run: `cd services/control-plane && go test ./internal/server -run 'Pricing|WorkspaceLaunch|BillingReview' -count=1`

Expected: PASS with exact prices and no repeated side effects.

- [ ] **Step 8: Commit**

```bash
git add services/control-plane packages/contracts tests/contracts
git commit -m "feat(control-plane): freeze paid pilot launch contract"
```

## Task 6: Persist Canonical Workspace Renewal Intent

**Files:**
- Modify: `services/control-plane/ent/schema/shared.go:158`
- Modify generated Ent files under: `services/control-plane/ent/`
- Create: `services/control-plane/migrations/202607170002_workspace_renewal.sql`
- Modify: `services/control-plane/migrations/migrations.go`
- Modify: `services/control-plane/migrations/migrations_test.go`
- Modify: `services/control-plane/internal/server/ent_state_store.go`
- Modify: `services/control-plane/internal/server/table_store.go`
- Modify: `services/control-plane/internal/server/memory_table_store.go`
- Modify: `services/control-plane/internal/server/ent_state_store_test.go`
- Modify: `services/control-plane/internal/server/workspace_gateway.go`

- [ ] **Step 1: Write failing schema/backfill tests**

Assert Workspace's `billing_state_json` round-trips `autoRenew`, authorization actor/time, price version, component and total USD micros, period start, paid-through, billing anchor, next-renewal time, and renewal status. Matching legacy compute/storage switches migrate; mismatches become `manual_review` with renewal disabled.

- [ ] **Step 2: Run RED tests**

Run: `cd services/control-plane && go test ./internal/server ./migrations -run 'WorkspaceRenewal|LegacyAutoRenew' -count=1`

Expected: FAIL because Workspace has no canonical billing fields.

- [ ] **Step 3: Add Workspace-owned fields and migration**

Add one `billing_state_json` field to `workspaceFields`, generate Ent code with the repository's existing generator, register the migration, and map the allowlisted Workspace billing keys in `workspaceEntFields`. The JSON column belongs to the Workspace row and is sufficient at Pilot scale; do not add a new table. The database migration must be idempotent and fail closed on ambiguous legacy state.

- [ ] **Step 4: Persist launch activation facts**

When the durable launch reaches Workspace activation, save the accepted price snapshot and the common paid-through value. Provider deadlines for both child resources must be at least that value; otherwise enter manual review before exposing the Workspace as active.

- [ ] **Step 5: Run GREEN tests**

Run: `cd services/control-plane && go test ./internal/server ./migrations -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add services/control-plane
git commit -m "feat(control-plane): persist workspace renewal intent"
```

## Task 7: Implement One Workspace Renewal Operation

**Files:**
- Create: `services/control-plane/internal/server/workspace_renewal.go`
- Create: `services/control-plane/internal/server/workspace_renewal_test.go`
- Modify: `services/control-plane/internal/server/renewal_worker.go`
- Modify: `services/control-plane/internal/server/routes_workspace.go`
- Modify: `services/control-plane/internal/server/table_store.go`
- Modify: `services/control-plane/internal/server/memory_table_store.go`
- Modify: `services/control-plane/internal/server/ent_state_store.go`
- Modify: `services/control-plane/internal/server/server.go`
- Modify: `services/control-plane/internal/server/operational_alerts.go`
- Modify: `services/control-plane/internal/server/billing_projection.go`
- Modify: `services/control-plane/internal/clients/ledger.go`
- Modify: `services/control-plane/internal/clients/ledger_test.go`
- Modify: `packages/contracts/opl-cloud-billing-ledger-contract.json`
- Modify: `packages/contracts/opl-cloud-evidence-ledger-contract.json`

- [ ] **Step 1: Write the failing Workspace-level API tests**

Add `POST /api/workspaces/{id}/auto-renew` tests for owner/CSRF/mutation key, cross-tenant denial, before/after-claim disable semantics, late enable, and `409 workspace_reactivation_required` after expiry. Response fields are exactly `autoRenew`, `effectiveAfter`, `nextRenewalAt`, `paidThrough`, and `renewalStatus`.

- [ ] **Step 2: Write the failing worker/state-machine tests**

Cover every state in the approved diagram: single DB claim under concurrent workers, one combined Sub2API debit, no provider call on insufficient balance, same-period retry, stable CVM/CBS IDs, both deadline readbacks, receipt-only retry, manual review, refund, expiry without CBS delete, and restart from each persisted phase.

- [ ] **Step 3: Run RED tests**

Run: `cd services/control-plane && go test ./internal/server -run 'Workspace(AutoRenew|Renewal)' -count=1`

Expected: FAIL because the worker currently renews compute and storage independently.

- [ ] **Step 4: Add an atomic RuntimeOperation claim**

Implement a store method that inserts/loads one stable `workspace.renewal` operation keyed by `(workspaceId, paidThrough)` and rejects a different request hash. PostgreSQL's unique primary key is the concurrency authority; local locks are only an optimization.

- [ ] **Step 5: Implement the minimum durable saga**

Persist after every external side effect. Debit the combined Workspace total once with a stable period identity; renew existing compute and storage with stable child keys; read back both provider IDs/deadlines; update Workspace entitlement only after both are confirmed; append one Workspace renewal receipt containing the component price snapshot.

- [ ] **Step 6: Replace per-resource scanning**

`runMonthlyBillingOnce` scans Workspaces for renewal/expiry. Compute/storage remain provider facts and compatibility projections, not writable customer renewal intent. Delete the public per-resource auto-renew route.

- [ ] **Step 7: Implement expiry and fail-closed reactivation boundary**

At unpaid expiry, stop/destroy compute and deny proxy access; never call CBS delete. A post-expiry enable returns the reactivation-required conflict. Do not create an empty replacement volume under the old Workspace ID.

- [ ] **Step 8: Run focused and full tests**

Run: `cd services/control-plane && go test ./internal/server ./internal/clients -count=1`

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add services/control-plane packages/contracts
git commit -m "feat(control-plane): renew workspaces as one subscription"
```

## Task 8: Generalize Provider Acceptance to Basic and Pro

**Files:**
- Modify: `services/control-plane/internal/server/routes_provider_acceptance.go`
- Modify: `services/control-plane/internal/server/provider_acceptance_test.go`
- Modify: `tools/provider-acceptance.ts`
- Modify: `tests/production/provider-acceptance.test.ts`
- Modify: `.github/workflows/provider-acceptance.yml`
- Modify: `.github/workflows/verify-production-chain.yml`
- Modify: `packages/contracts/opl-cloud-deployment-contract.json`
- Modify: `packages/contracts/opl-cloud-launch-freeze-contract.json`

- [ ] **Step 1: Write failing dual-slot tests**

Freeze `verification-slot-basic-01` (2c4g+10GB) and `verification-slot-pro-01` (8c16g+100GB), separate reserved account IDs, separate idempotency keys, total lifetime purchase budget two, and no customer-product visibility.

- [ ] **Step 2: Prove inventory-first behavior**

For each slot, zero compliant candidates may create only after environment approval; one is adopted; multiple/ambiguous candidates stop. Quote above `maxApprovedProviderCost` or missing approval fails before purchase.

- [ ] **Step 3: Separate bootstrap from ordinary release**

The deployment workflow first deploys routes, then an independently dispatched and environment-approved Acceptance workflow creates/adopts slots. Ordinary deploy and verification require both slots but contain no purchase/delete/renew commands.

- [ ] **Step 4: Run tests**

Run: `npm test -- --test-name-pattern='Provider Acceptance|fixed slot|deploy workflow'`

Run: `cd services/control-plane && go test ./internal/server -run ProviderAcceptance -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/control-plane tools tests/production .github/workflows packages/contracts
git commit -m "feat(release): bootstrap basic and pro acceptance slots"
```

## Task 9: Close HTTP, Operator, Database, and Kubernetes Security Gaps

**Files:**
- Modify: `services/control-plane/cmd/control-plane/main.go`
- Modify: `services/control-plane/cmd/control-plane/main_test.go`
- Modify: `services/fabric/cmd/fabric/main.go`
- Modify: `services/fabric/cmd/fabric/main_test.go`
- Modify: `services/ledger/cmd/ledger/main.go`
- Modify: `services/ledger/cmd/ledger/main_test.go`
- Modify: `services/control-plane/internal/server/server.go`
- Modify: `services/control-plane/internal/server/server_test.go`
- Modify: `services/control-plane/internal/server/routes_admin.go`
- Modify: `services/control-plane/internal/server/routes_provider_acceptance.go`
- Modify: `services/control-plane/internal/server/ent_state_store.go`
- Modify: `services/fabric/internal/fabric/operation_store.go`
- Modify: `services/ledger/internal/ledger/postgres_store.go`
- Modify: `services/fabric/internal/fabric/tencent_provider.go`
- Modify: `services/fabric/internal/fabric/tencent_provider_test.go`
- Modify: `deploy/tke/opl-cloud.k8s.json`
- Modify: `tests/production/tke-kubernetes-manifest.test.ts`

- [ ] **Step 1: Write failing server and header tests**

Require finite ReadHeader/Read/Write/Idle timeouts on all three servers and CSP, HSTS, `nosniff`, frame-deny, and strict referrer headers on public Control Plane responses.

- [ ] **Step 2: Write failing operator-network tests**

Production operator routes require both an operator session/token and client IP inside `OPL_OPERATOR_CIDRS`. Missing/invalid CIDRs fail closed. Forwarded IP is trusted only from configured ingress proxy CIDRs.

- [ ] **Step 3: Write failing PostgreSQL TLS tests**

Production constructors reject `sslmode=disable`/missing TLS mode and accept `verify-full` (or an explicitly configured equivalent with CA verification). Test databases may opt into disable only through test-only constructors/environment.

- [ ] **Step 4: Write failing manifest/runtime isolation tests**

Require Fabric and Ledger NetworkPolicies; Workspace blocks metadata (`169.254.169.254`), cluster/private ranges, and Kubernetes API egress while allowing DNS, the OPL Gateway path, and required public HTTPS. Require `runAsNonRoot=true`, no privilege escalation, all capabilities dropped, and RuntimeDefault seccomp.

- [ ] **Step 5: Implement with standard-library/Kubernetes primitives**

Use `http.Server`, `net/netip`, PostgreSQL DSN parsing, and native NetworkPolicy/securityContext. Add no security middleware or CIDR dependency.

- [ ] **Step 6: Run service and manifest tests**

Run: `go test ./services/control-plane/... ./services/fabric/... ./services/ledger/... -count=1`

Run: `npm test -- --test-name-pattern='Kubernetes|security|readiness'`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add services deploy/tke tests/production
git commit -m "fix(security): harden paid pilot boundaries"
```

## Task 10: Make Readiness and Live QA Prove the Current Request

**Files:**
- Modify: `services/control-plane/internal/server/routes_core.go`
- Modify: `services/control-plane/internal/server/server_test.go`
- Modify: `tools/production-live-qa.ts`
- Modify: `tests/production/production-live-qa.test.ts`
- Modify: `tools/production-verifier.ts`
- Modify: `tests/production/production-verifier.test.ts`
- Modify: `.github/workflows/deploy-tke-production.yml`
- Modify: `tests/production/tke-deploy-workflow.test.ts`
- Modify: `packages/contracts/opl-cloud-deployment-contract.json`

- [ ] **Step 1: Write failing readiness tests**

Readiness must compare expected immutable references with actual ready Pod `status.containerStatuses[].imageID`, not only Deployment spec images. Missing/mixed image IDs are unready.

- [ ] **Step 2: Write failing live-QA contract tests**

Use Runtime credential POST reveal, never ordinary status. After the one model request, poll usage by the exact request ID and assert model, input/output Token counts, and integer `actualCostUsdMicros`; verify usage stats, Ledger receipt, stable CVM/CBS IDs, and no provider purchase/delete/renew.

- [ ] **Step 3: Remove the first-slot deployment deadlock**

Ordinary deploy may skip live QA only in an explicitly approved bootstrap mode that deploys endpoints and performs read-only readiness. It must not claim release completion. After Acceptance creates/adopts both slots, rerun the same digests through normal deploy and mandatory live QA.

- [ ] **Step 4: Run production-tool tests**

Run: `npm test -- --test-name-pattern='production live QA|production verifier|deploy workflow|readiness'`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/control-plane tools tests/production .github/workflows packages/contracts
git commit -m "fix(release): verify immutable paid pilot chain"
```

## Task 11: Update Truthful Contracts and Runbook

**Files:**
- Modify: `docs/invariants.md`
- Modify: `docs/status.md`
- Modify: `docs/runtime/production-runbook.md`
- Modify: `packages/contracts/opl-cloud-business-object-contract.json`
- Modify: `packages/contracts/opl-cloud-product-contract.json`
- Modify: `packages/contracts/opl-cloud-service-boundary-contract.json`
- Modify: `tests/contracts/*.test.ts`

- [ ] **Step 1: Write failing contract assertions first**

Assert exact USD prices/version, two fixed slots, Workspace-level renewal owner/state, POST-only reveals, CBS no-backup/zero-guaranteed-recovery wording, and ordinary release zero-provider-mutation rules.

- [ ] **Step 2: Update human and machine truth together**

State code-complete versus runtime-proven facts separately. Do not claim Provider Acceptance, renewal, restore, or production deployment until evidence exists.

- [ ] **Step 3: Run contract tests**

Run: `npm test -- --test-name-pattern='contract|architecture freeze|monthly billing'`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add docs packages/contracts tests/contracts
git commit -m "docs: freeze dual-sku paid pilot operations"
```

## Task 12: Integrate the UI Commit at the Frozen Boundary

**Files:**
- Cherry-pick: the UI window's clean commit
- Modify only as needed after cherry-pick: the six UI-owned files listed above
- Modify as needed: `apps/console-ui/src/api/console-api.ts`
- Modify as needed: focused UI tests under `tests/ui/`

- [ ] **Step 1: Obtain and inspect the UI commit hash**

Run: `git show --stat --oneline <ui-commit>`

Expected: one intentional UI commit, no backend/service files.

- [ ] **Step 2: Cherry-pick the commit**

Run: `git cherry-pick <ui-commit>`

Expected: no uncommitted-file copying and no loss of either branch's changes.

- [ ] **Step 3: Write/update failing UI contract tests**

Cover one durable launch POST and refresh recovery, POST-only Gateway reveal, Runtime reveal/rotate clearing plaintext, exact server price facts, Workspace auto-renew response states, and independent unavailable states for usage/stats/receipts/readiness.

- [ ] **Step 4: Implement only frozen client integration**

Delete browser-side compute/storage/attachment/workspace purchase sequencing and GET reveal. Do not redesign the UI in this backend branch.

- [ ] **Step 5: Run UI checks**

Run: `npm test -- --test-name-pattern='Vue|console|Gateway|Workspace'`

Run: `npm run typecheck && npm run lint && npm run build`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add apps/console-ui tests/ui
git commit -m "feat(console): connect paid workspace lifecycle"
```

## Task 13: Full Verification and Release Preparation

**Files:**
- No production code unless a failing check reveals a root cause
- Update evidence paths in: `docs/status.md`, `docs/runtime/production-runbook.md`

- [ ] **Step 1: Verify the worktree and prohibited mutations**

Run: `git status --short`

Run: `rg -n 'POSTPAID_BY_HOUR|reveal=true|delete.*(disk|cvm)|destroy.*verification' services tools .github deploy`

Expected: clean worktree after commits; no forbidden customer/verification behavior.

- [ ] **Step 2: Run all Node checks**

Run: `npm test && npm run typecheck && npm run lint && npm run build`

Expected: PASS.

- [ ] **Step 3: Run all Go checks**

Run: `go test ./services/control-plane/... ./services/fabric/... ./services/ledger/... ./services/internal/postgresmigrate/... -count=1`

Expected: PASS.

- [ ] **Step 4: Run real PostgreSQL suites without skips**

Set isolated test database URLs for Control Plane, Fabric, Ledger, and postgresmigrate, then run their integration tests. Assert the output contains no `SKIP` for database coverage.

- [ ] **Step 5: Run browser QA on desktop and mobile**

Start the local staging API/UI, use Playwright to cover login, Basic/Pro quote, launch recovery, reveal/clear, usage/stats, receipts, renewal toggle, keyboard operation, and responsive layout. Capture screenshots and console/network errors.

- [ ] **Step 6: Run pre-landing review**

Use `$gstack-review`; fix every P0/P1 finding with a failing regression test first.

- [ ] **Step 7: Ship the exact commit**

Use `$gstack-ship` to merge current main, rerun checks on the merge result, build immutable Cloud/Workspace digests from that exact commit, and create the PR. Do not deploy from `c46caff` or claim production equals `ee7d774`.

- [ ] **Step 8: Provider bootstrap and controlled deployment**

After product owner/operator supplies both reserved Acceptance accounts, approved slot budgets, signed terms, production PostgreSQL TLS/restore evidence, and maintenance windows:

1. Deploy endpoint/bootstrap mode with immutable digests.
2. Read-only Tencent inventory.
3. Manually approve Basic and Pro Acceptance create/adopt.
4. Rerun normal deploy with the same digests.
5. Prove Pod imageIDs, both SKU launches, Runtime login/WebSocket, one exact model request, usage/stats, receipts, one real renewal per slot, refund/reconciliation, stable provider IDs, and grouped rollback.
6. Observe one Basic and one Pro customer before expanding to ten accounts.

## Failure-Mode and Test Matrix

| Failure | Required behavior | Smallest proving test |
|---|---|---|
| Key pagination lies or changes | Fail before operation/debit | Sub2API client table test |
| Duplicate/missing active Key | Conflict before debit | launch route event-order test |
| Browser closes/retries | Same launch operation resumes | restart/replay test |
| Child manual review resolves | Same parent resumes | operator-resolution integration test |
| Fabric succeeds then crashes before save | stale same-name apply/readback converges | two-Service PostgreSQL test |
| Ledger gets malformed/malicious payload | reject before persistence | memory + PostgreSQL validator test |
| Two renewal workers claim one period | one operation/debit | concurrent PostgreSQL claim test |
| Balance consumed between read and debit | Sub2API atomic debit decides; no provider call | insufficient debit test |
| One provider renew succeeds, other unknown | no entitlement extension/refund guess; review | phase restart/manual-review test |
| Ledger unavailable after activation | retry receipt only | receipt-only replay test |
| Auto-renew disabled before/after claim | current/next period semantics match response | concurrency API test |
| Renewal unpaid at expiry | stop compute, deny access, no CBS delete | expiry provider-call test |
| Reactivation disk missing | unrecoverable; no empty replacement | readback test |
| Actual Pod image differs from spec | readiness false and rollback | readiness fixture test |
| Operator request outside allowlist | deny before handler | CIDR/auth matrix test |
| Source failure | explicit unavailable, never empty/zero/ready | route projection tests |

## Dependency and Commit Order

```text
Ledger boundary -------+
Gateway boundary ------+--> price/launch --> Workspace schema --> renewal saga
Atomic identity -------+                                          |
Fabric crash recovery -+                                          v
                                                     dual Acceptance + security
                                                                  |
UI commit (independent) ------------------------------------------+
                                                                  v
                                                     full verification/release
```

Backend Tasks 1-4 are independent of the UI working tree and may land first. Tasks 5-11 remain backend-only. Task 12 is the only planned UI merge point. Task 13 does not mutate Tencent resources until the explicitly approved Provider Acceptance stage.

## Self-Review

- Spec coverage: all approved P0s map to Tasks 1-13; runtime evidence remains an explicit external gate rather than a code claim.
- Placeholder scan: no implementation step contains TBD/TODO/fill-later language.
- Type consistency: `priceVersion` is the external Pilot term; existing `pricingVersion` storage fields must be migrated/aliased deliberately in Tasks 5-7, not silently mixed.
- Scope: no new service, queue, wallet, database, or frontend; existing locks, operations, stores, workers, and native platform controls are reused.
- UI safety: no UI-owned file changes before a clean UI commit hash exists.
