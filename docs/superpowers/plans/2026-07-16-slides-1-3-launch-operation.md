# Slides 1-3 Launch Operation Implementation Plan

> **Historical / Superseded - do not execute.** Current Pilot authority is the
> launch freeze, source-truth contracts, and the integration plan approved in chat.

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` to implement this plan task-by-task. Repository policy forbids subagents for this work.

**Goal:** Add the minimal manual identity completion and a server-owned, durable, one-submit Workspace launch with total quote and total balance preflight.

**Architecture:** Reuse the existing pricing functions, monthly purchase protocol, Fabric idempotency, primary Workspace identity, and `control_plane_runtime_operations`. A `workspace.launch` operation stores its request fingerprint, phase, stable child IDs, safe result references, and error code as JSON in the existing `result` column. The production-enabled provider reconciliation worker resumes pending launches after restart; no table, queue, or dependency is added.

**Tech Stack:** Go 1.22, `net/http`, Ent/PostgreSQL state store, existing Fabric/Ledger/Sub2API clients, Go tests.

---

## File Map

- Modify `services/control-plane/internal/server/pricing.go`: compose compute and storage previews into one checked total.
- Modify `services/control-plane/internal/server/routes_state.go`: keep the existing pricing preview endpoint and accept the Workspace quote request.
- Modify `services/control-plane/internal/server/auth_accounts.go`: operator password reset helper and session revocation.
- Modify `services/control-plane/internal/server/routes_admin.go`: operator-only reset route and audit.
- Create `services/control-plane/internal/server/workspace_launch.go`: launch DTO, persistence encoding, phase runner, and customer-safe projection.
- Create `services/control-plane/internal/server/routes_workspace_launch.go`: POST/list/detail HTTP routes.
- Modify `services/control-plane/internal/server/provider_reconcile_worker.go`: run pending launches on startup and each worker pass.
- Modify `services/control-plane/internal/server/server.go`: register launch routes only.
- Create `services/control-plane/internal/server/workspace_launch_test.go`: quote, preflight, replay, ordering, close/restart recovery, and tenant tests.
- Modify `services/control-plane/internal/server/server_test.go`: only shared fake methods required by the new focused test.
- Modify `packages/contracts/opl-cloud-management-contract.json`: record the reset API.
- Modify `packages/contracts/opl-cloud-launch-freeze-contract.json`: record CI implementation while leaving production evidence pending.
- Modify `docs/invariants.md`: describe the one-submit launch without claiming live completion.

Do not modify `apps/console-ui/**`, Ledger/Sub2API usage adapters,
`routes_gateway.go`, `routes_billing.go`, Runtime credential behavior, Fabric
provider code, deployment workflows, or production manifests.

### Task 1: Establish the clean baseline

- [ ] **Step 1: Verify the branch and worktree are isolated**

Run:

```bash
git status --short --branch
git merge-base HEAD integration/pilot-launch-b
```

Expected: branch `feat/slides-1-3-launch-operation`, no tracked changes, and the
merge base equals the integration HEAD used to create the worktree.

- [ ] **Step 2: Run the focused baseline**

Run:

```bash
(cd services/control-plane && go test ./internal/server ./internal/controlplane ./internal/clients)
```

Expected: PASS before edits.

### Task 2: Add a server-computed Workspace quote

- [ ] **Step 1: Write failing quote tests**

Add to `services/control-plane/internal/server/pricing_monthly_test.go`:

```go
func TestWorkspacePricingPreviewAddsComputeAndStorageOnce(t *testing.T) {
	preview, err := pricingPreviewResponse(map[string]any{
		"resourceType": "workspace", "packageId": "basic", "sizeGb": 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview["totalMonthlyPriceCnyCents"] != int64(36_800) || preview["totalChargeUsdMicros"] != int64(52_571_429) {
		t.Fatalf("workspace preview = %#v", preview)
	}
	if mapField(preview, "compute")["chargeUsdMicros"] != int64(50_000_000) || mapField(preview, "storage")["chargeUsdMicros"] != int64(2_571_429) {
		t.Fatalf("workspace components = %#v", preview)
	}
}

func TestWorkspacePricingPreviewRejectsInvalidStorage(t *testing.T) {
	if _, err := pricingPreviewResponse(map[string]any{
		"resourceType": "workspace", "packageId": "basic", "sizeGb": 11,
	}); !errors.Is(err, errInvalidPricingInput) {
		t.Fatalf("error = %v, want invalid pricing input", err)
	}
}
```

- [ ] **Step 2: Confirm the test fails**

Run:

```bash
(cd services/control-plane && go test ./internal/server -run 'TestWorkspacePricingPreview' -count=1)
```

Expected: FAIL because `workspace` is not an accepted resource type.

- [ ] **Step 3: Implement composition in `pricing.go`**

Add a Workspace branch before the existing compute/storage validation. It must
call the existing function twice and use checked integer addition:

```go
func workspacePricingPreview(catalog pricingCatalogData, input map[string]any) (map[string]any, error) {
	computeInput := cloneMap(input)
	computeInput["resourceType"] = "compute"
	storageInput := cloneMap(input)
	storageInput["resourceType"] = "storage"
	compute, err := pricingPreviewFromCatalog(catalog, computeInput)
	if err != nil {
		return nil, err
	}
	storage, err := pricingPreviewFromCatalog(catalog, storageInput)
	if err != nil {
		return nil, err
	}
	cny, ok := checkedAddInt64(int64(numberField(compute, "monthlyPriceCnyCents", 0)), int64(numberField(storage, "monthlyPriceCnyCents", 0)))
	if !ok {
		return nil, errInvalidPricingInput
	}
	usd, ok := checkedAddInt64(int64(numberField(compute, "chargeUsdMicros", 0)), int64(numberField(storage, "chargeUsdMicros", 0)))
	if !ok {
		return nil, errInvalidPricingInput
	}
	return map[string]any{
		"resourceType": "workspace", "pricingVersion": catalog.Version,
		"packageId": stringValue(compute["packageId"]), "currency": catalog.Currency,
		"billingUnit": catalog.BillingUnit, "compute": compute, "storage": storage,
		"totalMonthlyPriceCnyCents": cny, "totalChargeUsdMicros": usd,
	}, nil
}
```

Use `math.MaxInt64-value` in `checkedAddInt64`; do not add a numeric package.

- [ ] **Step 4: Run quote tests and commit**

Run:

```bash
(cd services/control-plane && go test ./internal/server -run 'TestWorkspacePricingPreview|TestMonthlyPricing' -count=1)
git add services/control-plane/internal/server/pricing.go services/control-plane/internal/server/pricing_monthly_test.go
git commit -m "feat(control-plane): quote complete workspaces"
```

Expected: PASS, then one focused commit.

### Task 3: Complete manual password lifecycle

- [ ] **Step 1: Write failing reset tests**

In `services/control-plane/internal/server/console_tenant_isolation_test.go`, add
one test that creates a user, logs in, resets the password through an operator
session, verifies the old session and password fail, and verifies the new
password succeeds. Also assert the response and audit contain no password or
hash.

Use this route shape:

```text
POST /api/users/{id}/reset-password
{"password":"NewCorrectHorseBatteryStaple!"}
```

- [ ] **Step 2: Confirm the route is absent**

Run:

```bash
(cd services/control-plane && go test ./internal/server -run TestOperatorPasswordResetRevokesSessions -count=1)
```

Expected: FAIL with 404.

- [ ] **Step 3: Add the helper and route**

Implement in `auth_accounts.go`:

```go
func (app *controlPlaneServer) resetUserPassword(ctx context.Context, userID, password string) (map[string]any, error) {
	user, err := app.findUserByID(ctx, userID)
	if err != nil || user == nil {
		return nil, errUserNotFound
	}
	if stringValue(user["status"]) != "active" {
		return nil, errUserDeleted
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	user["passwordHash"] = hash
	if err := app.revokeUserSessions(userID); err != nil {
		return nil, err
	}
	if err := app.tables.SaveUser(ctx, user); err != nil {
		return nil, err
	}
	return sanitizeUser(user), nil
}
```

Reuse the existing password validator in `hashPassword`; do not add reset tokens,
email, SSO, or a password-history table. The route is `protected(true)`, writes
an audit event, and never echoes the password.

- [ ] **Step 4: Run tests and commit**

Run:

```bash
(cd services/control-plane && go test ./internal/server -run 'PasswordReset|UserLifecycle|Login' -count=1)
git add services/control-plane/internal/server/auth_accounts.go services/control-plane/internal/server/routes_admin.go services/control-plane/internal/server/console_tenant_isolation_test.go
git commit -m "feat(control-plane): support operator password reset"
```

Expected: PASS.

### Task 4: Define the durable launch record and safe DTO

- [ ] **Step 1: Write encoding and projection tests**

Create `workspace_launch_test.go` with tests for:

```go
func TestWorkspaceLaunchOperationRoundTripsWithoutSecrets(t *testing.T) {
	input := workspaceLaunchOperation{
		RequestHash: "hash", Phase: "compute", AccountID: "acct-alpha",
		OwnerUserID: "usr-alpha", WorkspaceID: "ws-alpha", PackageID: "basic",
		StorageGB: 10, TotalChargeUSDMicros: 52_571_429,
		ComputeID: "ca-alpha", StorageID: "vol-alpha",
	}
	encoded := encodeWorkspaceLaunchOperation(input)
	decoded, err := decodeWorkspaceLaunchOperation(map[string]any{"result": encoded})
	if err != nil || decoded.RequestHash != input.RequestHash {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
	for _, forbidden := range []string{"password", "apiKey", "redeemCode", "rawProvider"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("encoded launch contains %q: %s", forbidden, encoded)
		}
	}
}
```

Also test that `workspaceLaunchResponse` omits `OwnerUserID`, internal child
idempotency keys, and internal dependency errors.

- [ ] **Step 2: Implement the record in `workspace_launch.go`**

Use one concrete struct, JSON encoding, and the existing RuntimeOperation fields.
The row identity is:

```go
map[string]any{
	"id": operation.ID, "operationId": operation.ID,
	"accountId": operation.AccountID, "workspaceId": operation.WorkspaceID,
	"resourceId": operation.WorkspaceID, "resourceKind": "workspace_launch",
	"action": "workspace.launch", "status": operation.Status,
	"result": encodeWorkspaceLaunchOperation(operation),
	"computeAllocationId": operation.ComputeID,
	"storageId": operation.StorageID, "attachmentId": operation.AttachmentID,
}
```

Derive every child ID once from the top-level operation ID. Do not derive a new
ID from time during a retry.

- [ ] **Step 3: Run the focused test and commit**

Run:

```bash
(cd services/control-plane && go test ./internal/server -run TestWorkspaceLaunchOperation -count=1)
git add services/control-plane/internal/server/workspace_launch.go services/control-plane/internal/server/workspace_launch_test.go
git commit -m "feat(control-plane): persist workspace launch state"
```

Expected: PASS.

### Task 5: Accept one launch after read-only total preflight

- [ ] **Step 1: Write the insufficient-total test**

The fake Sub2API balance must be one micro below the total. Assert:

- response is `409 monthly_balance_insufficient`;
- no charge/refund call occurred;
- no Fabric mutation occurred;
- no `workspace.launch` RuntimeOperation was saved.

- [ ] **Step 2: Write replay and fingerprint tests**

Assert same key/same body returns the original operation ID, while same key with
a changed package, storage size, or name returns 409 without side effects.

- [ ] **Step 3: Implement `POST /api/workspace-launches`**

The handler must execute this exact order:

```go
quote -> compute preflight -> storage preflight -> live balance -> SaveRuntimeOperation -> wake worker -> 202
```

Use `service.PreflightMonthlyResource` for both resources and
`service.Sub2APIBalance` for the total. Reuse `monthlyPreflightConfirmed`. The
request fingerprint includes account ID, owner user ID, normalized name,
package ID, storage GB, and pricing version.

The route must reject a reconciliation guard before preflight and must reject a
second primary Workspace or a different active launch for the account.

- [ ] **Step 4: Implement list/detail polling**

Register:

```text
GET /api/workspace-launches
GET /api/workspace-launches/{id}
```

Filter `ListRuntimeOperations` by both `action=workspace.launch` and the scoped
account ID. Unknown or cross-account IDs return 404.

- [ ] **Step 5: Run HTTP tests and commit**

Run:

```bash
(cd services/control-plane && go test ./internal/server -run 'WorkspaceLaunch.*(Preflight|Replay|Fingerprint|Tenant|List)' -count=1)
git add services/control-plane/internal/server/routes_workspace_launch.go services/control-plane/internal/server/server.go services/control-plane/internal/server/workspace_launch.go services/control-plane/internal/server/workspace_launch_test.go
git commit -m "feat(control-plane): accept durable workspace launches"
```

Expected: PASS.

### Task 6: Advance the complete chain in the worker

- [ ] **Step 1: Write the event-order test**

Use fakes to record calls and assert this order:

```text
launch compute preflight
launch storage preflight
launch total balance preflight
compute safety preflight
compute balance confirmation
compute debit
compute provider create/readback
storage safety preflight
storage balance confirmation
storage debit
storage provider create/readback
attachment
gateway secret
runtime
workspace receipt
```

The exact preflight ordering at submission remains compute preflight, storage
preflight, balance. The later per-resource preflights are expected safety
rechecks.

- [ ] **Step 2: Implement one phase per persisted transition**

`runWorkspaceLaunch` must:

- lock `workspace-launch:<accountID>` with the existing process lock;
- reload the operation after taking the lock;
- call `purchaseMonthlyResource` with stable child IDs;
- stop and persist `waiting` when a resource is still preparing;
- create and save the attachment with the existing Fabric method;
- call the existing Gateway Secret and Runtime preparation path;
- record the Workspace receipt separately;
- persist `succeeded` only after the projection and receipt exist;
- persist `manual_review` for a child billing review;
- persist a retryable safe error code for dependency failure.

Do not compensate a confirmed resource automatically. Existing confirmed-absence
refund behavior remains inside `purchaseMonthlyResource`.

- [ ] **Step 3: Resume launches from the existing worker**

At the start of `runProviderReconcileOnce`, call:

```go
if err := app.runWorkspaceLaunchesOnce(ctx, service); err != nil {
	errs = append(errs, err)
}
```

The POST route may start an immediate background attempt with
`context.Background()`. Restart recovery relies on the production-enabled
provider reconciliation worker's immediate startup pass.

- [ ] **Step 4: Prove browser-close and restart recovery**

Persist a launch at each phase, construct a new server/app over the same test
store, run one worker pass, and assert completed phases are not repeated. At
minimum, prove no second Sub2API charge, compute create, storage create, or
Workspace receipt.

- [ ] **Step 5: Run tests and commit**

Run:

```bash
(cd services/control-plane && go test ./internal/server -run 'WorkspaceLaunch.*(Order|Resume|Restart|ManualReview)' -count=1)
git add services/control-plane/internal/server/workspace_launch.go services/control-plane/internal/server/provider_reconcile_worker.go services/control-plane/internal/server/workspace_launch_test.go
git commit -m "feat(control-plane): resume workspace launches in background"
```

Expected: PASS.

### Task 7: Update contracts without overstating evidence

- [ ] **Step 1: Update machine contracts**

Add the password reset API to the management contract. Update launch stages 1-3
to say local implementation and tests exist, while live Sub2API/Tencent and
deployed browser evidence remain pending. Add the three Workspace launch routes
and the existing RuntimeOperation persistence choice to the freeze contract.

- [ ] **Step 2: Update `docs/invariants.md`**

State that one browser submission creates a durable launch and that the total
balance read is a preflight, not a hold. Do not mark Slides 4-10 complete.

- [ ] **Step 3: Run contract tests and commit**

Run:

```bash
npm test
git add docs/invariants.md packages/contracts/opl-cloud-management-contract.json packages/contracts/opl-cloud-launch-freeze-contract.json
git commit -m "docs: contract durable pilot launch"
```

Expected: all repository tests selected by `npm test` pass.

### Task 8: Full lane verification

- [ ] **Step 1: Run formatting**

Run:

```bash
gofmt -w services/control-plane/internal/server/auth_accounts.go services/control-plane/internal/server/routes_admin.go services/control-plane/internal/server/pricing.go services/control-plane/internal/server/workspace_launch.go services/control-plane/internal/server/routes_workspace_launch.go services/control-plane/internal/server/provider_reconcile_worker.go services/control-plane/internal/server/workspace_launch_test.go
```

- [ ] **Step 2: Run all Go tests**

Run:

```bash
(cd services/internal/postgresmigrate && go test ./...)
(cd services/ledger && go test ./...)
(cd services/fabric && go test ./...)
(cd services/control-plane && go test ./...)
```

Expected: PASS.

- [ ] **Step 3: Run repository tests and inspect scope**

Run:

```bash
npm test
git diff --check integration/pilot-launch-b...HEAD
git diff --stat integration/pilot-launch-b...HEAD
```

Expected: PASS, no whitespace errors, and no UI/customer-facts/Runtime-isolation
files in the diff.

- [ ] **Step 4: Rebase and hand off**

Run:

```bash
git rebase integration/pilot-launch-b
(cd services/internal/postgresmigrate && go test ./...)
(cd services/ledger && go test ./...)
(cd services/fabric && go test ./...)
(cd services/control-plane && go test ./...)
```

Expected: PASS on the current integration head. Do not merge from this worktree;
the root integration worktree owns the merge.
