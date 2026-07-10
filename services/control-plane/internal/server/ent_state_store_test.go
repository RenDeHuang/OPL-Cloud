package server

import (
	"context"
	"errors"
	"testing"

	"entgo.io/ent/dialect"
	_ "github.com/mattn/go-sqlite3"

	controlplaneenttest "opl-cloud/services/control-plane/ent/enttest"
	"opl-cloud/services/control-plane/ent/pricingitem"
	"opl-cloud/services/control-plane/internal/clients"
)

func NewTestEntStateStore(t *testing.T, path string) StateStore {
	t.Helper()
	client := controlplaneenttest.Open(t, dialect.SQLite, path+"?_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	return &postgresEntStateStore{client: client}
}

func TestEntStateStorePricingCatalogReadsPricingTables(t *testing.T) {
	ctx := context.Background()
	store := NewTestEntStateStore(t, t.TempDir()+"/pricing.sqlite").(*postgresEntStateStore)

	if _, err := store.PricingCatalog(ctx); err != nil {
		t.Fatalf("seed pricing catalog: %v", err)
	}
	if _, err := store.client.PricingItem.Update().
		Where(
			pricingitem.CatalogVersion(pricingCatalogVersion),
			pricingitem.PackageID("basic"),
			pricingitem.ResourceType("compute"),
		).
		SetUnitPrice(2.5).
		SetUnitPriceCents(250).
		Save(ctx); err != nil {
		t.Fatalf("update pricing item: %v", err)
	}

	catalog, err := store.PricingCatalog(ctx)
	if err != nil {
		t.Fatalf("read pricing catalog: %v", err)
	}
	basic := packageByIDFromCatalog(catalog, "basic")
	if basic.ComputeHourly != 2.5 {
		t.Fatalf("pricing catalog must read DB item price, got %#v", basic)
	}
}

func TestEntStateStoreIgnoresDuplicateEventProjectionIDs(t *testing.T) {
	store := NewTestEntStateStore(t, t.TempDir()+"/duplicate-events.sqlite")
	row := map[string]any{"id": "ledger-alpha", "accountId": "acct-alpha", "type": "compute_debit", "amountCents": int64(-100)}
	if err := store.SaveLedgerEntry(context.Background(), row); err != nil {
		t.Fatalf("save ledger projection: %v", err)
	}
	if err := store.SaveLedgerEntry(context.Background(), row); err != nil {
		t.Fatalf("duplicate event projections should not break table persistence: %v", err)
	}
}

func TestEntStateStorePersistsWalletTransactionWalletAfter(t *testing.T) {
	store := NewTestEntStateStore(t, t.TempDir()+"/wallet-after.sqlite")
	if err := store.SaveWalletTransaction(context.Background(), map[string]any{
		"id":              "wallet-tx-alpha",
		"accountId":       "acct-alpha",
		"type":            "compute_debit",
		"ledgerEntryId":   "ledger-alpha",
		"amountCents":     int64(-100),
		"balanceCents":    int64(900),
		"frozenCents":     int64(10),
		"availableCents":  int64(890),
		"totalSpentCents": int64(100),
		"currency":        "CNY",
		"metadata": map[string]any{
			"computeAllocationId": "compute-alpha",
		},
	}); err != nil {
		t.Fatalf("save wallet transaction projection: %v", err)
	}
	loaded, err := store.ListWalletTransactions(context.Background(), "acct-alpha")
	if err != nil {
		t.Fatal(err)
	}
	tx := loaded[0]
	for key, want := range map[string]int64{
		"balanceCents":    900,
		"frozenCents":     10,
		"availableCents":  890,
		"totalSpentCents": 100,
	} {
		if got := int64(numberField(tx, key, 0)); got != want {
			t.Fatalf("%s = %d, want %d in %#v", key, got, want, tx)
		}
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
	if err != nil || len(heads) != 1 || heads[0]["localAliasId"] != "local-project-alpha" {
		t.Fatalf("unexpected sync heads: %#v, %v", heads, err)
	}
	requests, err := store.ListExecutionRequests(ctx)
	if err != nil || len(requests) != 1 || requests[0]["approvalStatus"] != "approved" || requests[0]["version"] != int64(2) {
		t.Fatalf("unexpected execution requests: %#v, %v", requests, err)
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
	organization, err := first.createOrganization(map[string]any{"name": "Research Lab", "billingAccountId": "acct-alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.createMembership(map[string]any{"organizationId": organization["id"], "userId": "usr-admin", "accountId": "acct-alpha", "role": "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := first.rememberRuntimeOperations([]clients.FabricOperation{{ID: "fabric-op-alpha", OperationID: "operation-alpha", WorkspaceID: "ws-alpha", ResourceID: "compute-alpha", ResourceKind: "compute_allocation", Status: "succeeded", RedactedProviderPayload: map[string]any{"costTags": map[string]any{"opl_operation_id": "operation-alpha"}}}}); err != nil {
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
	reconciliation := state["billingReconciliation"].(map[string]any)
	guard := reconciliation["guard"].(map[string]any)
	if guard["blockNewWorkspaces"] != true {
		t.Fatalf("reconciliation did not survive restart: %#v", reconciliation)
	}
}
