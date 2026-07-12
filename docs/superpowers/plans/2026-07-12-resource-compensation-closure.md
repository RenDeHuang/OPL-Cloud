# Resource Compensation Closure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close resource hold release, periodic settlement isolation, and Workspace Runtime compensation across Control Plane, Fabric, and Ledger.

**Architecture:** Preserve Control Plane commercial fields when applying Fabric provider facts, then reuse Ledger's idempotent hold release from explicit destroy and provider reconciliation. Add one internal Fabric Runtime destroy operation for receipt-failure compensation, and keep batch settlement progressing while aggregating per-resource errors.

**Tech Stack:** Go 1.24, `net/http`, `errors.Join`, Ent/PostgreSQL stores, existing Go test suites.

---

### Task 1: Preserve Commercial Resource Facts

**Files:**
- Modify: `services/control-plane/internal/server/admin_ops.go:199-255`
- Test: `services/control-plane/internal/server/server_test.go`

- [ ] **Step 1: Write failing projection regression tests**

Seed compute and storage rows containing hold, pricing, owner, and name fields. Apply succeeded Fabric operations with provider-only payloads. Assert commercial fields remain and provider fields update:

```go
if compute["holdId"] != "hold-compute" || compute["pricingVersion"] != "pricing-v1" {
	t.Fatalf("Fabric operation erased Control Plane facts: %#v", compute)
}
if compute["nodeName"] != "node-from-fabric" || compute["status"] != "running" {
	t.Fatalf("Fabric provider facts were not applied: %#v", compute)
}
```

- [ ] **Step 2: Run `cd services/control-plane && go test ./internal/server -run 'TestRememberRuntimeOperationPreserves(Compute|Storage)CommercialFacts' -count=1`**

Expected: FAIL because the stored row is replaced.

- [ ] **Step 3: Merge provider facts over existing facts**

For compute, storage, and attachment, load the existing row and use it as the merge base:

```go
if existing, ok := app.getCompute(id); ok {
	row = computeResponse(mergeMaps(existing, row))
}
```

- [ ] **Step 4: Run the focused test and `go test ./internal/server -count=1`**

Expected: PASS.

- [ ] **Step 5: Commit `admin_ops.go` and `server_test.go` as `fix(console): preserve commercial resource facts`**

### Task 2: Release Holds for Destroyed and Failed Resources

**Files:**
- Modify: `services/control-plane/internal/controlplane/service.go:466-594`
- Modify: `services/control-plane/internal/server/provider_reconcile_worker.go:57-105`
- Test: `services/control-plane/internal/controlplane/service_test.go`
- Test: `services/control-plane/internal/server/settlement_worker_test.go`
- Test: `services/control-plane/internal/server/server_test.go`

- [ ] **Step 1: Write failing destroy-after-operation tests**

Create compute and storage, ingest Fabric succeeded operations, then destroy them. Capture Ledger release calls and assert the original hold ID and amount are submitted once:

```go
if release.HoldID != createdHoldID || release.AmountCents != createdHoldAmount {
	t.Fatalf("destroy release = %#v", release)
}
```

- [ ] **Step 2: Write a failing asynchronous compute failure test**

Seed a provisioning compute with an active hold and a failed Fabric operation. Run provider reconciliation twice. Assert one release and persisted `holdReleaseId` plus `billingStatus=stopped`.

- [ ] **Step 3: Run `cd services/control-plane && go test ./internal/server ./internal/controlplane -run 'Test(DestroyAfterFabricOperationReleasesHold|ProviderReconcileReleasesFailedComputeHold)' -count=1`**

Expected: FAIL because failed computes are terminal and skipped.

- [ ] **Step 4: Reuse one idempotent Control Plane hold-release method**

Refactor the existing Ledger call so compute destroy, storage destroy, external deletion, and failed creation share:

```go
func (s *Service) ReleaseResourceHold(ctx context.Context, input DestroyResourceInput, resourceType, reason, key string) (clients.HoldReleaseResult, error) {
	return s.ledger.ReleaseHold(ctx, clients.HoldReleaseInput{
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID,
		ResourceType: resourceType, ResourceID: input.ID, HoldID: input.HoldID,
		AmountCents: input.HoldAmountCents, Currency: "CNY", Reason: reason,
	}, key+":hold-release")
}
```

- [ ] **Step 5: Reconcile failed operations before normal provider sync**

List and remember Fabric operations. For each failed compute with an unreleased hold, call the shared method with stable key `provider-reconcile:compute:<id>`, save wallet/release fields, and stop billing. Aggregate compensation errors instead of aborting other resources.

- [ ] **Step 6: Run focused tests, then `go test ./internal/controlplane ./internal/server -count=1`**

Expected: PASS and exactly one release per hold.

- [ ] **Step 7: Commit the Task 2 files as `fix(billing): release terminal resource holds`**

### Task 3: Isolate Periodic Settlement Failures

**Files:**
- Modify: `services/control-plane/internal/server/settlement_worker.go:57-81`
- Test: `services/control-plane/internal/server/settlement_worker_test.go`

- [ ] **Step 1: Write a failing continuation test**

Make the fake Ledger fail its first settlement and accept its second. Assert two calls, a saved second projection, and a returned error containing the first failure.

- [ ] **Step 2: Run `cd services/control-plane && go test ./internal/server -run TestPeriodicSettlementContinuesAfterResourceFailure -count=1`**

Expected: FAIL with one call because the loop returns immediately.

- [ ] **Step 3: Aggregate errors while continuing**

```go
var errs []error
for _, input := range inputs {
	result, err := service.SettleResource(ctx, input, periodicSettlementKey(input))
	if err != nil {
		errs = append(errs, fmt.Errorf("settle %s: %w", input.ResourceID, err))
		continue
	}
	// Persist this successful resource; append persistence errors and continue.
}
return errors.Join(errs...)
```

- [ ] **Step 4: Run `go test ./internal/server -run 'TestPeriodicSettlement' -count=1`**

Expected: PASS.

- [ ] **Step 5: Commit as `fix(billing): isolate periodic settlement failures`**

### Task 4: Compensate Workspace Receipt Failure

**Files:**
- Modify: `services/fabric/internal/fabric/types.go`
- Modify: `services/fabric/internal/fabric/service.go:17-33,515-600`
- Modify: `services/fabric/internal/fabric/tencent_provider.go:383-452`
- Modify: `services/fabric/internal/http/server.go:273-285`
- Modify: `services/control-plane/internal/clients/fabric.go:13-31`
- Modify: `services/control-plane/internal/controlplane/service.go:602-635`
- Test: `services/fabric/internal/fabric/service_test.go`
- Test: `services/fabric/internal/fabric/tencent_provider_test.go`
- Test: `services/fabric/internal/http/server_test.go`
- Test: `services/control-plane/internal/clients/fabric_test.go`
- Test: `services/control-plane/internal/controlplane/service_test.go`

- [ ] **Step 1: Write failing Fabric Runtime destroy tests**

Assert provider cleanup deletes the labeled Deployment, Service, and Secret with `--ignore-not-found=true`, does not mutate the shared Ingress, returns status `destroyed` without credentials, and replays the same idempotency key without a second provider call.

- [ ] **Step 2: Write failing Control Plane compensation tests**

Make `RecordReceipt` fail after Runtime creation. Assert `DestroyWorkspaceRuntime(workspaceID, key+":runtime-compensation")` is called and no Workspace is returned. When cleanup also fails, assert `errors.Is` matches both errors.

- [ ] **Step 3: Run focused Fabric and Control Plane tests**

```bash
cd services/fabric && go test ./internal/fabric ./internal/http -run 'TestDestroyWorkspaceRuntime' -count=1
cd ../control-plane && go test ./internal/controlplane ./internal/clients -run 'TestCreateWorkspaceCompensatesReceiptFailure|TestFabricHTTPClientDestroysWorkspaceRuntime' -count=1
```

Expected: build/test failure because the destroy contract is absent.

- [ ] **Step 4: Add the narrow destroy contract**

Add `DestroyWorkspaceRuntime` to Provider, Fabric service, authenticated HTTP route, and typed client. Record `destroy_workspace_runtime` in the operation store and return metadata only:

```go
return WorkspaceRuntime{
	WorkspaceID: workspaceID,
	Status: "destroyed",
	ProviderRequestID: providerRequestID("runtime-destroy", idempotencyKey),
}, nil
```

- [ ] **Step 5: Compensate receipt failure in `CreateWorkspace`**

```go
receipt, err := s.ledger.RecordReceipt(ctx, receiptInput, idempotencyKey+":receipt")
if err != nil {
	_, cleanupErr := s.fabric.DestroyWorkspaceRuntime(ctx, workspaceID, idempotencyKey+":runtime-compensation")
	return domain.WorkspaceProjection{}, errors.Join(err, cleanupErr)
}
```

- [ ] **Step 6: Run focused tests, then full Fabric and Control Plane Go tests**

Expected: PASS.

- [ ] **Step 7: Commit as `fix(workspace): compensate failed receipt creation`**

### Task 5: Full Verification and Temporary File Cleanup

**Files:**
- Verify only; do not add generated artifacts.

- [ ] **Step 1: Run `gofmt` on modified Go files**

```bash
gofmt -w $(git diff --name-only main...HEAD | grep '\.go$')
```

- [ ] **Step 2: Run the complete gate**

```bash
npm test
npm run typecheck
npm run build
(cd services/control-plane && go test ./... -count=1)
(cd services/fabric && go test ./... -count=1)
(cd services/ledger && go test ./... -count=1)
git diff --check
```

- [ ] **Step 3: Remove only implementation-created temporary files**

Inspect `git status --short --ignored` and staged paths. Keep source, tests, design, and plan only. Do not stage `.runtime`, `dist`, coverage, logs, editor files, dependency directories, or scratch tests.

- [ ] **Step 4: Commit this plan as `docs: add resource compensation implementation plan` if still uncommitted**

- [ ] **Step 5: Verify `git status --short` is clean and review `git log --oneline main..HEAD` plus `git diff --stat main...HEAD`**
