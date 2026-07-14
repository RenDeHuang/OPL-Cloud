package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	_ "github.com/mattn/go-sqlite3"

	controlplaneenttest "opl-cloud/services/control-plane/ent/enttest"
	"opl-cloud/services/control-plane/internal/clients"
)

func NewTestEntStateStore(t *testing.T, path string) StateStore {
	t.Helper()
	client := controlplaneenttest.Open(t, dialect.SQLite, path+"?_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	return &postgresEntStateStore{client: client}
}

func TestEntStateStoreSub2APIMappingAndMonthlyEntitlementRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewTestEntStateStore(t, t.TempDir()+"/monthly.sqlite")
	if err := store.SaveAccount(ctx, map[string]any{"id": "acct-monthly", "status": "active", "sub2apiUserId": int64(41)}); err != nil {
		t.Fatalf("save account mapping: %v", err)
	}
	accounts, err := store.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	account := recordByID(accounts, "acct-monthly")
	if int64(numberField(account, "sub2apiUserId", 0)) != 41 {
		t.Fatalf("account mapping = %#v", account)
	}

	monthly := map[string]any{
		"accountId":                  "acct-monthly",
		"billingStatus":              "active",
		"billingOperationId":         "billing-op-41",
		"billingOperationStartedAt":  "2026-07-14T00:00:00Z",
		"sub2apiRedeemCode":          "opl:test:billing-op-41:charge:v1",
		"pricingVersion":             pricingCatalogVersion,
		"monthlyPriceCnyCents":       int64(35000),
		"chargeUsdMicros":            int64(50_000_000),
		"billingAnchorDay":           int64(14),
		"periodStart":                "2026-07-14T00:00:00Z",
		"paidThrough":                "2026-08-14T00:00:00Z",
		"autoRenew":                  true,
		"lastRenewalAttemptAt":       "2026-07-14T00:00:00Z",
		"lastBillingError":           "",
		"lastReceiptId":              "receipt-41",
		"postChargeBalanceUsdMicros": int64(0),
		"postChargeBalanceKnown":     true,
	}
	compute := mergeMaps(monthly, map[string]any{"id": "compute-monthly", "packageId": "basic"})
	storage := mergeMaps(monthly, map[string]any{"id": "storage-monthly", "packageId": "basic", "sizeGb": 30})
	if err := store.SaveCompute(ctx, compute); err != nil {
		t.Fatalf("save monthly compute: %v", err)
	}
	if err := store.SaveStorage(ctx, storage); err != nil {
		t.Fatalf("save monthly storage: %v", err)
	}

	computes, err := store.ListComputes(ctx, "acct-monthly")
	if err != nil {
		t.Fatalf("list monthly compute: %v", err)
	}
	storages, err := store.ListStorages(ctx, "acct-monthly")
	if err != nil {
		t.Fatalf("list monthly storage: %v", err)
	}
	for kind, row := range map[string]map[string]any{
		"compute": recordByID(computes, "compute-monthly"),
		"storage": recordByID(storages, "storage-monthly"),
	} {
		if row["billingOperationId"] != "billing-op-41" || int64(numberField(row, "monthlyPriceCnyCents", 0)) != 35000 || int64(numberField(row, "chargeUsdMicros", 0)) != 50_000_000 || row["paidThrough"] != "2026-08-14T00:00:00Z" || row["autoRenew"] != true {
			t.Fatalf("%s monthly fields = %#v", kind, row)
		}
		if row["postChargeBalanceKnown"] != true || int64(numberField(row, "postChargeBalanceUsdMicros", 0)) != 0 {
			t.Fatalf("%s zero post-charge balance is not known: %#v", kind, row)
		}
	}
}

func TestEntStateStoreBillingOperationReplayConflictsOnAmountOrPeriod(t *testing.T) {
	ctx := context.Background()
	for name, store := range map[string]StateStore{
		"memory": newMemoryTableStore(),
		"ent":    NewTestEntStateStore(t, t.TempDir()+"/billing-claim.sqlite"),
	} {
		t.Run(name, func(t *testing.T) {
			operation := map[string]any{
				"id":                   "compute-claim-41",
				"accountId":            "acct-alpha",
				"packageId":            "basic",
				"billingStatus":        "preparing",
				"billingOperationId":   "billing-op-claim-41",
				"pricingVersion":       pricingCatalogVersion,
				"monthlyPriceCnyCents": int64(35000),
				"chargeUsdMicros":      int64(50_000_000),
				"periodStart":          "2026-07-14T00:00:00Z",
				"paidThrough":          "2026-08-14T00:00:00Z",
			}
			claimed, fresh, err := store.ClaimResourceBillingOperation(ctx, "compute", operation)
			if err != nil || !fresh || claimed["billingOperationId"] != operation["billingOperationId"] {
				t.Fatalf("first claim fresh=%v row=%#v err=%v", fresh, claimed, err)
			}
			if _, fresh, err := store.ClaimResourceBillingOperation(ctx, "compute", operation); err != nil || fresh {
				t.Fatalf("same operation replay fresh=%v err=%v", fresh, err)
			}
			for field, value := range map[string]any{
				"chargeUsdMicros": int64(49_000_000),
				"paidThrough":     "2026-09-14T00:00:00Z",
			} {
				conflict := cloneMap(operation)
				conflict[field] = value
				if _, _, err := store.ClaimResourceBillingOperation(ctx, "compute", conflict); !errors.Is(err, errIdempotencyConflict) {
					t.Fatalf("%s conflict error = %v", field, err)
				}
			}
		})
	}
}

func recordByID(rows []map[string]any, id string) map[string]any {
	for _, row := range rows {
		if stringValue(row["id"]) == id {
			return row
		}
	}
	return nil
}

func TestEntStateStoreNeverPersistsWorkspacePassword(t *testing.T) {
	store := NewTestEntStateStore(t, t.TempDir()+"/workspace-secret.sqlite")
	if err := store.SaveWorkspace(context.Background(), map[string]any{
		"id": "ws-alpha", "accountId": "acct-alpha",
		"access": map[string]any{"username": "opl", "password": "must-not-persist", "secretRef": "opl-compute-alpha-env"},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListWorkspaces(context.Background(), "acct-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if password := stringValue(nested(rows[0], "access", "password")); password != "" {
		t.Fatalf("Workspace password persisted: %q", password)
	}
}

func TestEntStateStoreWorkspaceResumeCommitRollsBackAllFacts(t *testing.T) {
	ctx := context.Background()
	store := NewTestEntStateStore(t, t.TempDir()+"/resume-transaction.sqlite")
	original := map[string]any{"id": "workspace-alpha", "accountId": "acct-alpha", "state": "suspended", "status": "suspended"}
	if err := store.SaveWorkspace(ctx, original); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	running := cloneMap(original)
	running["state"], running["status"] = "running", "running"
	err := store.CommitWorkspaceResume(ctx, running, map[string]any{"id": "audit-resume", "action": "workspace.resume", "resourceKind": "workspace", "resourceId": "workspace-alpha", "result": "succeeded"}, map[string]any{"action": "workspace.resume"})
	if err == nil {
		t.Fatal("resume commit with invalid operation unexpectedly succeeded")
	}
	workspaces, _ := store.ListWorkspaces(ctx, "")
	audits, _ := store.ListAuditEvents(ctx, "")
	operations, _ := store.ListRuntimeOperations(ctx)
	if len(workspaces) != 1 || workspaces[0]["state"] != "suspended" || len(audits) != 0 || len(operations) != 0 {
		t.Fatalf("failed resume commit was not atomic: workspaces=%#v audits=%#v operations=%#v", workspaces, audits, operations)
	}
}

func TestEntStateStoreWorkspaceResumeClaimIsRetryableAndExclusive(t *testing.T) {
	ctx := context.Background()
	store := NewTestEntStateStore(t, t.TempDir()+"/resume-claim.sqlite")
	if err := store.SaveWorkspace(ctx, map[string]any{"id": "workspace-alpha", "accountId": "acct-alpha", "state": "suspended", "status": "suspended"}); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	lease := time.Now().UTC().Add(time.Minute)
	operation := map[string]any{"id": "resume-alpha", "operationId": "resume-alpha", "workspaceId": "workspace-alpha", "resourceId": "workspace-alpha", "resourceKind": "workspace_runtime", "action": "workspace.resume", "status": "started", "result": encodeWorkspaceResumeOperation(workspaceResumeOperationResult{RequestHash: "hash-alpha", LeaseExpiresAt: &lease})}
	if _, replayed, err := store.ClaimWorkspaceResume(ctx, "workspace-alpha", operation); err != nil || replayed {
		t.Fatalf("claim = replayed:%v err:%v", replayed, err)
	}
	if _, _, err := store.ClaimWorkspaceResume(ctx, "workspace-alpha", operation); !errors.Is(err, errWorkspaceResumeInProgress) {
		t.Fatalf("same-key concurrent claim error = %v", err)
	}
	different := cloneMap(operation)
	different["id"], different["operationId"] = "resume-other", "resume-other"
	different["result"] = encodeWorkspaceResumeOperation(workspaceResumeOperationResult{RequestHash: "hash-other", LeaseExpiresAt: &lease})
	if _, _, err := store.ClaimWorkspaceResume(ctx, "workspace-alpha", different); !errors.Is(err, errWorkspaceResumeInProgress) {
		t.Fatalf("different-key concurrent claim error = %v", err)
	}
	if err := store.FailWorkspaceResume(ctx, "workspace-alpha", "resume-alpha", "fabric_failed"); err != nil {
		t.Fatalf("fail claim: %v", err)
	}
	workspaces, _ := store.ListWorkspaces(ctx, "")
	operations, _ := store.ListRuntimeOperations(ctx)
	if len(workspaces) != 1 || workspaces[0]["state"] != "suspended" || len(operations) != 1 || operations[0]["status"] != "retryable" {
		t.Fatalf("retryable state: workspaces=%#v operations=%#v", workspaces, operations)
	}
	if _, replayed, err := store.ClaimWorkspaceResume(ctx, "workspace-alpha", operation); err != nil || replayed {
		t.Fatalf("retry claim = replayed:%v err:%v", replayed, err)
	}
}

func TestEntStateStorePersistsExecutionIdentityAndApproval(t *testing.T) {
	store := NewTestEntStateStore(t, t.TempDir()+"/execution.sqlite")
	ctx := context.Background()
	if err := store.SaveProjectTaskSyncHead(ctx, map[string]any{
		"id":             "project-alpha",
		"kind":           "project",
		"organizationId": "org-alpha",
		"workspaceId":    "workspace-alpha",
		"localAliasId":   "local-project-alpha",
		"version":        int64(1),
		"status":         "active",
	}); err != nil {
		t.Fatalf("save project identity: %v", err)
	}
	if err := store.SaveProjectTaskSyncHead(ctx, map[string]any{
		"id":             "task-alpha",
		"kind":           "task",
		"organizationId": "org-alpha",
		"workspaceId":    "workspace-alpha",
		"projectId":      "project-alpha",
		"localAliasId":   "local-task-alpha",
		"version":        int64(1),
		"status":         "draft",
	}); err != nil {
		t.Fatalf("save task identity: %v", err)
	}
	if err := store.SaveExecutionRequest(ctx, map[string]any{
		"id":             "request-alpha",
		"organizationId": "org-alpha",
		"workspaceId":    "workspace-alpha",
		"projectId":      "project-alpha",
		"taskId":         "task-alpha",
		"actorUserId":    "usr-alpha",
		"approvalId":     "approval-alpha",
		"approvalStatus": "approved",
		"approvedBy":     "usr-reviewer",
		"status":         "approved",
		"environmentRef": "environment-alpha",
		"idempotencyKey": "request-once",
		"version":        int64(2),
	}); err != nil {
		t.Fatalf("save execution request: %v", err)
	}

	heads, err := store.ListProjectTaskSyncHeads(ctx)
	headsByID := map[string]map[string]any{}
	for _, head := range heads {
		headsByID[stringValue(head["id"])] = head
	}
	if err != nil || len(heads) != 2 || headsByID["project-alpha"]["projectId"] != "project-alpha" || headsByID["task-alpha"]["taskId"] != "task-alpha" {
		t.Fatalf("unexpected sync heads: %#v, %v", heads, err)
	}
	requests, err := store.ListExecutionRequests(ctx)
	if err != nil || len(requests) != 1 || requests[0]["requestId"] != "request-alpha" || requests[0]["approvalStatus"] != "approved" || requests[0]["version"] != int64(2) {
		t.Fatalf("unexpected execution requests: %#v, %v", requests, err)
	}
}

func TestEntStateStorePersistsWorkspaceSyncEvents(t *testing.T) {
	path := t.TempDir() + "/workspace-sync.sqlite"
	store := NewTestEntStateStore(t, path).(*postgresEntStateStore)
	ctx := context.Background()
	events := []map[string]any{
		{
			"id":             "mutation-alpha",
			"operationId":    "operation-alpha",
			"workspaceId":    "workspace-alpha",
			"cursor":         int64(1001),
			"entityKind":     "project",
			"projectId":      "project-alpha",
			"clientId":       "client-alpha",
			"actorUserId":    "user-alpha",
			"baseVersion":    int64(1),
			"serverVersion":  int64(2),
			"operation":      "replace",
			"status":         "accepted",
			"payload":        map[string]any{"title": "Cloud title"},
			"contentDigest":  "sha256:alpha",
			"idempotencyKey": "mutation-once",
			"requestHash":    "hash-alpha",
			"occurredAt":     "2026-07-11T00:00:00Z",
		},
		{
			"id":             "mutation-conflict",
			"operationId":    "operation-conflict",
			"workspaceId":    "workspace-alpha",
			"cursor":         int64(1002),
			"entityKind":     "project",
			"projectId":      "project-alpha",
			"clientId":       "client-beta",
			"actorUserId":    "user-beta",
			"baseVersion":    int64(1),
			"serverVersion":  int64(2),
			"operation":      "replace",
			"status":         "conflict",
			"payload":        map[string]any{"current": map[string]any{"title": "Cloud title"}, "incoming": map[string]any{"title": "Offline title"}},
			"idempotencyKey": "mutation-conflict-once",
			"requestHash":    "hash-conflict",
			"conflictId":     "conflict-alpha",
			"occurredAt":     "2026-07-11T00:01:00Z",
		},
	}
	for _, event := range events {
		if err := store.SaveWorkspaceSyncEvent(ctx, event); err != nil {
			t.Fatalf("save sync event: %v", err)
		}
	}
	if err := store.client.Close(); err != nil {
		t.Fatalf("close sync event store: %v", err)
	}
	store = NewTestEntStateStore(t, path).(*postgresEntStateStore)

	stored, err := store.ListWorkspaceSyncEvents(ctx, "workspace-alpha", 0, 10)
	if err != nil {
		t.Fatalf("list sync events: %v", err)
	}
	if len(stored) != 2 || stored[0]["id"] != "mutation-alpha" || stored[1]["conflictId"] != "conflict-alpha" {
		t.Fatalf("unexpected sync events: %#v", stored)
	}
	payload, ok := stored[1]["payload"].(map[string]any)
	if !ok || payload["current"] == nil || payload["incoming"] == nil || stored[0]["cursor"] != int64(1001) || stored[0]["requestHash"] != "hash-alpha" || stored[0]["operationId"] != "operation-alpha" || stored[0]["actorUserId"] != "user-alpha" || stored[0]["occurredAt"] != "2026-07-11T00:00:00Z" {
		t.Fatalf("sync event fields were not preserved: %#v", stored)
	}
}

func TestEntStateStoreUpdatesExecutionRequestWithoutRecreatingIt(t *testing.T) {
	store := NewTestEntStateStore(t, t.TempDir()+"/execution-update.sqlite").(*postgresEntStateStore)
	ctx := context.Background()
	row := map[string]any{
		"id":             "request-alpha",
		"organizationId": "org-alpha",
		"workspaceId":    "workspace-alpha",
		"projectId":      "project-alpha",
		"taskId":         "task-alpha",
		"actorUserId":    "usr-alpha",
		"status":         "awaiting_approval",
		"idempotencyKey": "request-once",
	}
	if err := store.SaveExecutionRequest(ctx, row); err != nil {
		t.Fatalf("save execution request: %v", err)
	}
	before, err := store.client.ExecutionRequest.Get(ctx, "request-alpha")
	if err != nil {
		t.Fatal(err)
	}
	row["status"] = "approved"
	row["approvalStatus"] = "approved"
	if err := store.SaveExecutionRequest(ctx, row); err != nil {
		t.Fatalf("update execution request: %v", err)
	}
	after, err := store.client.ExecutionRequest.Get(ctx, "request-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !after.CreatedAt.Equal(before.CreatedAt) || after.Status != "approved" {
		t.Fatalf("request was recreated instead of updated: before=%#v after=%#v", before, after)
	}
}

func TestEntStateStoreUpdatesResourcesWithoutRecreatingThem(t *testing.T) {
	store := NewTestEntStateStore(t, t.TempDir()+"/resource-update.sqlite").(*postgresEntStateStore)
	ctx := context.Background()
	createdAt := "2026-07-01T00:00:00Z"

	compute := map[string]any{
		"id": "compute-alpha", "accountId": "acct-alpha", "status": "provisioning",
		"lastProviderSyncError": "provider temporarily unavailable", "createdAt": createdAt,
	}
	if err := store.SaveCompute(ctx, compute); err != nil {
		t.Fatal(err)
	}
	delete(compute, "createdAt")
	compute["status"], compute["lastProviderSyncError"] = "running", ""
	if err := store.SaveCompute(ctx, compute); err != nil {
		t.Fatal(err)
	}
	storedCompute, err := store.client.ComputeAllocation.Get(ctx, "compute-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if storedCompute.CreatedAt.Format(time.RFC3339) != createdAt || storedCompute.Status != "running" || storedCompute.LastProviderSyncError != "" {
		t.Fatalf("compute was recreated or not updated: %#v", storedCompute)
	}

	storage := map[string]any{
		"id": "storage-alpha", "accountId": "acct-alpha", "status": "creating",
		"lastProviderSyncError": "provider temporarily unavailable", "createdAt": createdAt,
	}
	if err := store.SaveStorage(ctx, storage); err != nil {
		t.Fatal(err)
	}
	delete(storage, "createdAt")
	storage["status"], storage["lastProviderSyncError"] = "available", ""
	if err := store.SaveStorage(ctx, storage); err != nil {
		t.Fatal(err)
	}
	storedStorage, err := store.client.StorageVolume.Get(ctx, "storage-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if storedStorage.CreatedAt.Format(time.RFC3339) != createdAt || storedStorage.Status != "available" || storedStorage.LastProviderSyncError != "" {
		t.Fatalf("storage was recreated or not updated: %#v", storedStorage)
	}
}

func TestEntStateStoreRejectsExecutionIdentityOverwrite(t *testing.T) {
	store := NewTestEntStateStore(t, t.TempDir()+"/execution-conflict.sqlite")
	ctx := context.Background()
	row := map[string]any{
		"id":             "request-alpha",
		"organizationId": "org-alpha",
		"workspaceId":    "workspace-alpha",
		"projectId":      "project-alpha",
		"taskId":         "task-alpha",
		"actorUserId":    "usr-alpha",
		"environmentRef": "environment-alpha",
		"status":         "awaiting_approval",
		"idempotencyKey": "request-once",
	}
	if err := store.SaveExecutionRequest(ctx, row); err != nil {
		t.Fatal(err)
	}
	row["environmentRef"] = "environment-beta"
	if err := store.SaveExecutionRequest(ctx, row); !errors.Is(err, errIdempotencyConflict) {
		t.Fatalf("overwrite error = %v, want errIdempotencyConflict", err)
	}
}

func TestControlPlaneAdminFactsSurviveServerRestart(t *testing.T) {
	store := NewTestEntStateStore(t, t.TempDir()+"/admin-facts.sqlite")
	first, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.tables.SaveAccount(context.Background(), map[string]any{"id": "acct-alpha", "status": "active"}); err != nil {
		t.Fatal(err)
	}
	if err := first.tables.SaveUser(context.Background(), map[string]any{"id": "usr-alpha", "email": "alpha@example.com", "accountId": "acct-alpha", "role": "owner", "status": "active"}); err != nil {
		t.Fatal(err)
	}
	organization, err := first.createOrganization(map[string]any{"name": "Research Lab", "billingAccountId": "acct-alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.createMembership(map[string]any{"organizationId": organization["id"], "userId": "usr-alpha", "accountId": "acct-alpha", "role": "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := first.rememberRuntimeOperations([]clients.FabricOperation{{ID: "fabric-op-alpha", OperationID: "operation-alpha", WorkspaceID: "ws-alpha", ResourceID: "compute-alpha", ResourceKind: "compute_allocation", Status: "failed", ErrorCode: "compute_machine_unavailable", RedactedProviderPayload: map[string]any{"costTags": map[string]any{"opl_operation_id": "operation-alpha"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := first.rememberReconciliation(clients.ReconciliationResult{ID: "reconcile-alpha", Status: "mismatch", BlockNewWorkspaces: true, Reason: "provider_cost_gap"}); err != nil {
		t.Fatal(err)
	}

	restarted, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	state := restarted.managementState(true, nil)
	if len(state["organizations"].([]any)) != 1 || len(state["memberships"].([]any)) != 1 || len(state["runtimeOperations"].([]any)) != 1 {
		t.Fatalf("admin facts did not survive restart: %#v", state)
	}
	operation := state["runtimeOperations"].([]any)[0].(map[string]any)
	payload := operation["redactedProviderPayload"].(map[string]any)
	if payload["costTags"].(map[string]any)["opl_operation_id"] != "operation-alpha" {
		t.Fatalf("runtime evidence did not survive restart: %#v", operation)
	}
	if operation["errorCode"] != "compute_machine_unavailable" {
		t.Fatalf("runtime error code did not survive restart: %#v", operation)
	}
	reconciliation := state["billingReconciliation"].(map[string]any)
	guard := reconciliation["guard"].(map[string]any)
	if guard["blockNewWorkspaces"] != true {
		t.Fatalf("reconciliation did not survive restart: %#v", reconciliation)
	}
}
