# Acceptance B Safe Launch Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Produce and deploy one immutable product release whose Acceptance B tooling can safely create exactly one approved Basic Workspace in an account with active general API usage.

**Architecture:** The installed Acceptance B approval is the deterministic identity boundary. Control Plane validates that approval against the deployed release, derives the exact Sub2API debit code, and returns a redacted GET-only reconciliation state. Both the standalone reconciler and the fresh-order writer consume the same read path; the writer repeats it immediately before its only Workspace POST.

**Tech Stack:** Go 1.22/1.25 services, Node.js 22 TypeScript scripts and `node:test`, JSON machine contracts, GitHub Actions release and Instance deployment workflows.

---

## Scope And Owners

- `product_chain` owns product implementation in this worktree.
- `deployment_chain` owns read-only release/Instance diagnostics; it receives a write set only if a product contract cannot be consumed by the current Instance workflow.
- `runtime_evidence` owns GET-only production evidence and does not edit source.
- The root controller alone merges, publishes, deploys, rotates production approval, or submits the Workspace order.
- Release optimization is owned by an independent parallel writer. It is not a dependency of this P0 checkpoint and must not overlap this product write set or alter Acceptance identity.

### Task 1: Add Approval-Bound Reconcile Tests

**Files:**
- Create: `services/control-plane/internal/server/account_reconcile_test.go`
- Modify: `services/control-plane/internal/server/workspace_launch_acceptance_b_admission_test.go`

**Acceptance gate:** A route cannot report `prepared` unless its result is bound to the installed approval, deployed release, exact launch identity, and exact Sub2API debit code.

**Step 1: Write the failing approval-binding tests**

Add table tests proving the shared admission helper rejects:

```go
approval release SHA != OPL_RELEASE_SHA
approval Cloud/Workspace digest != deployed image digest
approval customer != reconciled account/email
approval operation/workspace identity != derivation from idempotency key
expired approval
unexpected allowedWrites or forbiddenWrites
```

Retain the existing request-header/capability tests for the POST admission wrapper.

**Step 2: Write the failing reconcile route tests**

Build a focused fixture with one active local account, one active Sub2API user,
an available wallet, general keys, and an installed valid approval. Assert:

```go
wallet adjustment absent + unrelated historical debit + exact approved debit absent => prepared
exact approved debit present => partial/manual_review, never prepared
exact debit lookup unavailable => unknown
approved launch operation present => partial/manual_review
approved Workspace key present => partial/manual_review
approval/deployment mismatch => unknown
```

The DTO and artifact must not contain the raw email, account ID, operation ID,
Workspace ID, redeem code, token, or password.

**Step 3: Run the focused tests and verify failure**

Run:

```bash
cd services/control-plane
go test ./internal/server -run 'TestAcceptanceBAccountReconcile|TestProductionAcceptanceBApproval' -count=1
```

Expected: FAIL because reconciliation returns before the exact debit lookup and
there is no shared deployment-bound approval helper.

### Task 2: Implement Approval-Bound Control Plane GET Reconciliation

**Files:**
- Modify: `services/control-plane/internal/server/workspace_launch_admission.go`
- Modify: `services/control-plane/internal/server/account_reconcile.go`
- Test: `services/control-plane/internal/server/account_reconcile_test.go`
- Test: `services/control-plane/internal/server/workspace_launch_acceptance_b_admission_test.go`

**Acceptance gate:** The product must prove the approved Workspace identity from owner authorities without using wallet deltas or rejecting unrelated history.

**Step 1: Extract the deployment-bound approval predicate**

Extract the non-header portion of `productionAcceptanceBLaunchApproved` into a
helper that validates expiry, deployed SHA/digests, customer, deterministic
operation/Workspace identity, package, storage, provider target, and exact write
lists. Keep capability and approval headers in the POST-only wrapper.

**Step 2: Extend the redacted reconcile DTO**

Add only redacted fields such as:

```go
ApprovalState                  string `json:"approvalState"`
ApprovalIdentitySHA256         string `json:"approvalIdentitySha256"`
WorkspaceLaunchState           string `json:"workspaceLaunchState"`
WorkspaceDebitState            string `json:"workspaceDebitState"`
WorkspaceDebitIdentitySHA256   string `json:"workspaceDebitIdentitySha256"`
```

The identity digest is computed from stable non-secret inputs but the raw
operation, Workspace, and redeem-code values never leave the server.

**Step 3: Remove the absent-wallet-adjustment early return**

Keep `walletAdjustment=absent` as evidence and continue. A found incomplete or
conflicting adjustment remains `manual_review`; a succeeded adjustment remains
informational. Never recharge in this route.

**Step 4: Read the approved Workspace authorities**

After approval validation:

```go
operationID := approval.Launch.OperationID
workspaceID := approval.Launch.WorkspaceID
redeemCode := monthlyRedeemCode(monthlyEnvironment(), operationID)
history, err := service.FinancialBalanceHistoryByCodes(ctx, remote.ID, []string{redeemCode})
```

Classify exact launch, Workspace, reserved Workspace key, purchase receipt, and
debit as absent, present, conflict, or unknown. Do not call account-wide usage
or infer debit from balance. General keys are not Workspace footprint.

**Step 5: Define the only prepared state**

Return `prepared` only for a complete identity graph, active matching Sub2API
identity, available wallet, valid deployed approval, and all approved Workspace
authorities absent. All unknown/conflict cases fail closed.

**Step 6: Run focused Go tests**

Run the Task 1 command. Expected: PASS.

**Step 7: Commit**

```bash
git add services/control-plane/internal/server/account_reconcile.go \
  services/control-plane/internal/server/account_reconcile_test.go \
  services/control-plane/internal/server/workspace_launch_admission.go \
  services/control-plane/internal/server/workspace_launch_acceptance_b_admission_test.go
git commit -m "fix: bind Acceptance B reconcile to approved debit"
```

### Task 3: Make The Standalone Reconciler Fail Closed

**Files:**
- Modify: `tools/production-basic-acceptance-b-reconcile.ts`
- Modify: `tests/production/production-basic-acceptance-b-reconcile.test.ts`

**Acceptance gate:** GET-only evidence cannot promote `manual_review` into a successful retry state.

**Step 1: Write failing Node tests**

Add or reverse tests proving:

```text
safe_to_retry_absent is not a success status
manual_review + absent wallet adjustment is never promoted
prepared requires approvalState=bound and workspaceDebitState=absent
general keys and nonzero general usage do not block prepared
unrelated historical negative balance entries do not block prepared
approved debit present/conflict/unknown blocks prepared
sensitive raw approval/debit identity fields are rejected
```

**Step 2: Run the focused test and verify failure**

Run:

```bash
node --test tests/production/production-basic-acceptance-b-reconcile.test.ts
```

Expected: FAIL on the old success set and promotion branch.

**Step 3: Replace the promotion with strict DTO validation**

- Set `SUCCESS_STATUSES` to only `prepared`.
- Delete the `manual_review -> safe_to_retry_absent` branch.
- Validate the new redacted approval/debit fields.
- Keep complete Workspace-key pagination and kind classification.
- General key usage is evidence only; it is never compared with wallet delta.
- Export a read helper accepting existing admin/customer sessions so the writer
  can reuse the same logic without duplicate login or duplicate scans.

**Step 4: Run the focused test**

Expected: PASS.

**Step 5: Commit**

```bash
git add tools/production-basic-acceptance-b-reconcile.ts \
  tests/production/production-basic-acceptance-b-reconcile.test.ts
git commit -m "fix: fail closed on Acceptance B account state"
```

### Task 4: Add The Immediate Pre-POST Gate

**Files:**
- Modify: `tools/production-basic-acceptance-b.ts`
- Modify: `tests/production/production-basic-acceptance-b.test.ts`

**Acceptance gate:** A stale artifact or concurrent Workspace mutation cannot reach `POST /api/workspace-launches`.

**Step 1: Write failing ordering tests**

Assert the fresh-order path performs, in order:

```text
approval-bound GET reconcile
current pricing preview
current wallet GET
exact approved operation GET
at most one Workspace POST
```

Test that approved debit present, launch present with conflicting identity,
Workspace key present, receipt present, or authority unknown produces zero
Workspace POSTs. Test that general usage changes between reads while the wallet
remains above quote do not block the approved order.

**Step 2: Run the focused test and verify failure**

Run:

```bash
node --test tests/production/production-basic-acceptance-b.test.ts
```

Expected: FAIL because the old fresh-order path uses a local first-page
baseline instead of the approval-bound reconcile helper.

**Step 3: Replace the local baseline**

Call the shared reconciliation read helper with the already-authenticated admin
and customer sessions. Require exactly `prepared`, then read the current quote
and wallet. Keep the existing deterministic-operation GET and one-POST
submission function.

Do not add a second order path. Readback-only mode continues only the operation
fixed by the approval and never consumes `safe_to_retry_absent`.

**Step 4: Strengthen terminal debit evidence**

Replace amount-only matching with the redacted exact debit identity returned by
the authoritative reconcile/readback chain and the Ledger receipt
`chargeReference`. General usage and wallet delta remain non-authoritative.

**Step 5: Run both focused Node suites**

Run:

```bash
node --test tests/production/production-basic-acceptance-b-reconcile.test.ts \
  tests/production/production-basic-acceptance-b.test.ts
```

Expected: PASS with zero unexpected production writes in mocks.

**Step 6: Commit**

```bash
git add tools/production-basic-acceptance-b.ts \
  tests/production/production-basic-acceptance-b.test.ts
git commit -m "fix: recheck approved launch before Acceptance B POST"
```

### Task 5: Freeze The Machine Contract

**Files:**
- Modify: `packages/contracts/opl-cloud-deployment-contract.json`
- Test: relevant contract tests discovered by `rg` before editing

**Acceptance gate:** Future Acceptance changes cannot restore account-wide debit blocking, wallet-delta inference, or `safe_to_retry_absent` success.

**Step 1: Write or update the failing contract assertion**

Require the deployment contract to state:

```json
{
  "workspaceDebitAuthority": "exact_approval_bound_sub2api_history_code",
  "generalUsage": "allowed_and_non_blocking",
  "walletDelta": "not_debit_identity",
  "successStatus": "prepared_only",
  "prePostReadback": "required_immediately_before_single_workspace_post"
}
```

Use the repository's existing contract vocabulary and exact-key validation;
do not add a parallel aggregate billing model.

**Step 2: Run the contract test and verify failure**

Run the focused contract test selected from the repository. Expected: FAIL for
missing new fields.

**Step 3: Update only the Acceptance B contract sections**

Do not change wallet ownership, Ledger ownership, pricing, normal Workspace
launch, or release schema.

**Step 4: Run contract and product-boundary validation**

```bash
npm run validate:product-boundary
```

Expected: PASS without installing dependencies if the current environment
already provides the required runtime; otherwise perform one documented
lockfile-based install and reuse it for all remaining validation.

**Step 5: Commit**

```bash
git add packages/contracts/opl-cloud-deployment-contract.json <focused-test>
git commit -m "docs: require approval-bound Acceptance B evidence"
```

### Task 6: Run Local Gates And Review The Product Diff

**Files:**
- No new product files unless a failing gate identifies a root cause in the P0 write set.

**Acceptance gate:** A candidate cannot enter product `main` without focused, aggregate, and boundary evidence.

**Step 1: Run formatting and focused gates**

```bash
gofmt -w services/control-plane/internal/server/account_reconcile.go \
  services/control-plane/internal/server/account_reconcile_test.go \
  services/control-plane/internal/server/workspace_launch_admission.go \
  services/control-plane/internal/server/workspace_launch_acceptance_b_admission_test.go
node --test tests/production/production-basic-acceptance-b-reconcile.test.ts \
  tests/production/production-basic-acceptance-b.test.ts
(cd services/control-plane && go test ./internal/server -run 'TestAcceptanceBAccountReconcile|TestProductionAcceptanceBApproval' -count=1)
git diff --check
```

Expected: PASS.

**Step 2: Run aggregate local verification**

```bash
npm run verify:local:full
```

Expected: PASS. If PostgreSQL or another declared prerequisite is unavailable,
record the exact missing prerequisite and run every remaining equivalent local
gate; do not silently downgrade the required gate.

**Step 3: Run two-stage review**

First review spec compliance against the design. Then review code quality,
trust boundaries, pagination, secret redaction, and mutation cardinality. Fix
all findings and rerun affected gates.

**Step 4: Push a recoverable task branch**

```bash
git push -u origin codex/acceptance-b-safe-launch
```

Read back the remote SHA/tree. The task branch remains non-canonical.

### Task 7: Integrate And Publish One Immutable Cloud Product Release

**Files:**
- No release-performance workflow changes in this P0.

**Acceptance gate:** The canonical product SHA, Cloud image digest, and release assets form one immutable product release identity. Workspace image ownership remains outside the product release.

**Step 1: Integrate through the repository's protected-main process**

Create/review the PR, merge only after CI, then read back canonical `main` SHA
and tree. Do not release a task-branch SHA.

**Step 2: Select one unused version and dispatch the standard product release once**

The root controller supplies the exact merged SHA. The release publishes the
Cloud image only. Do not rerun a failed release without first classifying
whether the immutable tag or image target was used.

**Step 3: Verify release readback**

Require exact tag, product SHA, amd64+arm64 manifest, Cloud image digest,
release assets, checksums, and attestations. Release performance/cache work may
proceed in its independent writer lane but is not required by this checkpoint.

### Task 8: Deploy Through `opl-instance-medopl`

**Files:**
- Modify Instance files only if its existing contract requires a new product contract version or release pin receipt.

**Acceptance gate:** Production must run the exact approved release before any customer mutation.

**Step 1: Create a fresh Approval B envelope**

Bind the canonical product SHA, new Cloud digest, existing approved immutable
Workspace digest, customer account, one new launch idempotency key, derived
operation/Workspace IDs, Basic package, 10 GB, and provider target. Do not expose
the envelope in logs or artifacts.

The Workspace digest is supplied by the Instance/`one-person-lab-app` owner,
not the product release. Require the same digest from TCR, TKE workload config,
the installed Approval, and ready Workspace Pod image-ID readback.

**Step 2: Copy the release to TCR once**

Read back the TCR digest. A failed/unknown copy is reconciled by registry GET,
not blindly repeated.

**Step 3: Deploy once**

Deploy the new product release and rotated approval through the existing TKE
workflow. Verify rollout, Pod image IDs, approval SHA annotation, health, and
readiness. No account prepare, recharge, or Workspace POST occurs in this step.

### Task 9: Execute One Acceptance B Mutation

**Files:**
- No repository edits during the production mutation window.

**Acceptance gate:** Exactly one approved Workspace order reaches production and produces terminal three-authority evidence.

**Step 1: Run one GET-only reconcile**

Require `prepared` bound to the exact deployed release and approval. Do not
repeat full historical usage scans.

Use this exact production input matrix:

```text
operation_mode=acceptance_b_account_reconcile
approval_id=''
resume_run_id=''
confirm_account_provision=false
confirm_wallet_recharge=false
confirm_workspace_purchase=false
confirm_single_model_request=false
confirm_recovery_plan_execute=false
```

**Step 2: Enter the single-writer critical section**

The root controller alone dispatches `acceptance_b_fresh_order` once with
`confirm_workspace_purchase=true`. No other agent or workflow may perform a
production mutation concurrently.

Use this exact production input matrix:

```text
operation_mode=acceptance_b_fresh_order
approval_id=<exact installed approval id>
resume_run_id=''
confirm_account_provision=false
confirm_wallet_recharge=false
confirm_workspace_purchase=true
confirm_single_model_request=false
confirm_recovery_plan_execute=false
```

**Step 3: Resolve only the approved operation**

For success or an unknown response, use GET of the approved operation ID. Never
submit a successor order. Stop on `manual_review`, conflict, or unavailable
authority.

**Step 4: Verify terminal evidence**

Require active Workspace/runtime/URL, exactly one exact-code Sub2API debit at
the final quote, and exactly one matching Ledger purchase receipt. Only then
mark the MVP complete.
