package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type settlementWorkerLedger struct {
	fakeLedgerClient
	settlements []clients.ResourceSettlementInput
	keys        []string
	releases    []clients.HoldReleaseInput
}

func (l *settlementWorkerLedger) SettleResource(_ context.Context, input clients.ResourceSettlementInput, idempotencyKey string) (clients.ResourceSettlementResult, error) {
	l.settlements = append(l.settlements, input)
	l.keys = append(l.keys, idempotencyKey)
	return clients.ResourceSettlementResult{
		ID:                  "settlement-" + input.ResourceID,
		AccountID:           input.AccountID,
		WorkspaceID:         input.WorkspaceID,
		ResourceType:        input.ResourceType,
		ResourceID:          input.ResourceID,
		AmountCents:         input.AmountCents,
		Currency:            input.Currency,
		Status:              "settled",
		LedgerEntryID:       "ledger-" + input.ResourceID,
		WalletTransactionID: "wallet-" + input.ResourceID,
		PricingVersion:      input.PricingVersion,
		PriceSnapshot:       input.PriceSnapshot,
		UsagePeriodStart:    input.UsagePeriodStart,
		UsagePeriodEnd:      input.UsagePeriodEnd,
		Quantity:            input.Quantity,
		Unit:                input.Unit,
		Wallet:              clients.Wallet{AccountID: input.AccountID, BalanceCents: 10000, AvailableCents: 9000, Currency: "CNY"},
	}, nil
}

func (l *settlementWorkerLedger) ReleaseHold(_ context.Context, input clients.HoldReleaseInput, _ string) (clients.HoldReleaseResult, error) {
	l.releases = append(l.releases, input)
	return clients.HoldReleaseResult{
		ID: "release-" + input.ResourceType + "-" + input.ResourceID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID,
		ResourceType: input.ResourceType, ResourceID: input.ResourceID, HoldID: input.HoldID, AmountCents: input.AmountCents,
		Status: "released", Wallet: clients.Wallet{AccountID: input.AccountID, BalanceCents: 10000, AvailableCents: 10000, Currency: "CNY"},
	}, nil
}

func TestPeriodicSettlementWorkerSettlesActiveResources(t *testing.T) {
	app := newControlPlaneAppEmpty()
	mustStore(t, app.tables.SaveCompute(context.Background(), freshBillableResource(map[string]any{"id": "compute-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "packageId": "basic", "status": "running"})))
	mustStore(t, app.tables.SaveStorage(context.Background(), freshBillableResource(map[string]any{"id": "storage-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "packageId": "basic", "status": "available", "sizeGb": 10})))
	ledger := &settlementWorkerLedger{}
	service := controlPlaneServiceForTest(ledger)
	now := time.Date(2026, 7, 9, 12, 30, 0, 0, time.UTC)

	if err := app.runPeriodicSettlementOnce(context.Background(), service, now); err != nil {
		t.Fatalf("run settlement worker: %v", err)
	}
	if len(ledger.settlements) != 2 {
		t.Fatalf("settlement count = %d, want 2: %#v", len(ledger.settlements), ledger.settlements)
	}
	if ledger.settlements[0].AccountID != "acct-alpha" || ledger.settlements[0].ResourceType != "compute" || ledger.settlements[0].AmountCents <= 0 {
		t.Fatalf("compute settlement missing facts: %#v", ledger.settlements[0])
	}
	if ledger.settlements[1].ResourceType != "storage" || ledger.settlements[1].ResourceID != "storage-alpha" || ledger.settlements[1].AmountCents <= 0 {
		t.Fatalf("storage settlement missing facts: %#v", ledger.settlements[1])
	}
	if ledger.keys[0] == ledger.keys[1] || ledger.keys[0] == "" || ledger.settlements[0].UsagePeriodEnd != "2026-07-09T12:00:00Z" {
		t.Fatalf("settlements must use stable per-period idempotency: keys=%#v settlements=%#v", ledger.keys, ledger.settlements)
	}
}

func TestPeriodicSettlementContinuesAfterResourceFailure(t *testing.T) {
	app := newControlPlaneAppEmpty()
	mustStore(t, app.tables.SaveCompute(context.Background(), freshBillableResource(map[string]any{"id": "compute-alpha", "accountId": "acct-alpha", "packageId": "basic", "status": "running"})))
	mustStore(t, app.tables.SaveStorage(context.Background(), freshBillableResource(map[string]any{"id": "storage-alpha", "accountId": "acct-beta", "packageId": "basic", "status": "available", "sizeGb": 10})))
	ledger := &failFirstSettlementLedger{}
	service := controlPlaneServiceForTest(ledger)

	err := app.runPeriodicSettlementOnce(context.Background(), service, time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "insufficient balance") || len(ledger.keys) != 2 {
		t.Fatalf("worker stopped early: keys=%#v err=%v", ledger.keys, err)
	}
	rows, listErr := app.tables.ListLedger(context.Background(), "")
	if listErr != nil || len(rows) != 1 {
		t.Fatalf("successful later settlement was not projected: rows=%#v err=%v", rows, listErr)
	}
}

type failFirstSettlementLedger struct {
	settlementWorkerLedger
	failed bool
}

func (l *failFirstSettlementLedger) SettleResource(ctx context.Context, input clients.ResourceSettlementInput, key string) (clients.ResourceSettlementResult, error) {
	if !l.failed {
		l.failed = true
		l.settlements = append(l.settlements, input)
		l.keys = append(l.keys, key)
		return clients.ResourceSettlementResult{}, errors.New("insufficient balance")
	}
	return l.settlementWorkerLedger.SettleResource(ctx, input, key)
}

func TestPeriodicSettlementWorkerDoesNotDuplicateControlPlaneProjectionsOnReplay(t *testing.T) {
	app := newControlPlaneAppEmpty()
	mustStore(t, app.tables.SaveCompute(context.Background(), freshBillableResource(map[string]any{"id": "compute-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "packageId": "basic", "status": "running"})))
	ledger := &settlementWorkerLedger{}
	service := controlPlaneServiceForTest(ledger)
	now := time.Date(2026, 7, 9, 12, 30, 0, 0, time.UTC)

	if err := app.runPeriodicSettlementOnce(context.Background(), service, now); err != nil {
		t.Fatalf("first settlement worker run: %v", err)
	}
	if err := app.runPeriodicSettlementOnce(context.Background(), service, now); err != nil {
		t.Fatalf("second settlement worker run: %v", err)
	}
	if len(ledger.keys) != 1 {
		t.Fatalf("worker must skip already-settled resources for the same period, got keys %#v", ledger.keys)
	}
	ledgerRows, err := app.tables.ListLedger(context.Background(), "")
	if err != nil {
		t.Fatalf("list ledger projection: %v", err)
	}
	if len(ledgerRows) != 1 {
		t.Fatalf("control-plane ledger projection duplicated replayed settlement: %#v", ledgerRows)
	}
	walletTxRows, err := app.tables.ListWalletTransactions(context.Background(), "")
	if err != nil {
		t.Fatalf("list wallet transaction projection: %v", err)
	}
	if len(walletTxRows) != 1 {
		t.Fatalf("control-plane wallet transaction projection duplicated replayed settlement: %#v", walletTxRows)
	}
	compute, ok := app.getCompute("compute-alpha")
	if !ok {
		t.Fatalf("compute not persisted")
	}
	if compute["settlementId"] != "settlement-compute-alpha" || compute["usagePeriodEnd"] != "2026-07-09T12:00:00Z" {
		t.Fatalf("resource missing last settlement metadata: %#v", compute)
	}
}

func TestPeriodicSettlementWorkerSkipsResourcesWithoutFreshProviderSync(t *testing.T) {
	app := newControlPlaneAppEmpty()
	mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{
		"id":                 "compute-stale",
		"accountId":          "acct-alpha",
		"packageId":          "basic",
		"status":             "running",
		"lastProviderSyncAt": "2026-01-01T00:00:00Z",
	}))
	mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{
		"id":                 "storage-failed",
		"accountId":          "acct-alpha",
		"packageId":          "basic",
		"status":             "available",
		"providerStatus":     "sync_failed",
		"lastProviderSyncAt": time.Now().UTC().Format(time.RFC3339Nano),
	}))
	ledger := &settlementWorkerLedger{}
	service := controlPlaneServiceForTest(ledger)

	if err := app.runPeriodicSettlementOnce(context.Background(), service, time.Now().UTC()); err != nil {
		t.Fatalf("run settlement worker: %v", err)
	}
	if len(ledger.settlements) != 0 {
		t.Fatalf("stale or failed provider state must not be billed: %#v", ledger.settlements)
	}
}

func TestProviderReconcileWorkerPersistsExternalDeleteAndRelease(t *testing.T) {
	app := newControlPlaneAppEmpty()
	mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{
		"id":                 "compute-alpha",
		"accountId":          "acct-alpha",
		"workspaceId":        "ws-alpha",
		"packageId":          "basic",
		"status":             "running",
		"billingStatus":      "active",
		"holdId":             "hold-compute-alpha",
		"holdAmountCents":    int64(7862),
		"lastProviderSyncAt": "2026-01-01T00:00:00Z",
	}))
	ledger := &settlementWorkerLedger{}
	service := controlPlaneServiceForTest(ledger)

	if err := app.runProviderReconcileOnce(context.Background(), service, time.Now().UTC()); err != nil {
		t.Fatalf("run provider reconcile: %v", err)
	}
	compute, ok := app.getCompute("compute-alpha")
	if !ok {
		t.Fatalf("compute not persisted")
	}
	if compute["status"] != "external_deleted" || compute["providerStatus"] != "missing" || compute["billingStatus"] != "stopped" {
		t.Fatalf("provider reconcile must persist external delete facts: %#v", compute)
	}
	if compute["holdReleaseId"] != "release-compute-compute-alpha" || compute["externalDeletedAt"] == "" || compute["lastProviderSyncAt"] == "" {
		t.Fatalf("provider reconcile missing release/sync evidence: %#v", compute)
	}
}

func TestProviderReconcileReleasesFailedComputeHold(t *testing.T) {
	app := newControlPlaneAppEmpty()
	mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{
		"id": "compute-failed", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "packageId": "basic",
		"status": "provisioning", "billingStatus": "pending", "holdId": "hold-compute-failed", "holdAmountCents": int64(7862),
	}))
	ledger := &settlementWorkerLedger{}
	fabric := &failedComputeOperationFabric{fakeFabricClient: fakeFabricClient{}, operations: []clients.FabricOperation{{
		ID: "fabric-failed", OperationID: "operation-failed", Action: "create_compute_allocation", ResourceKind: "compute_allocation",
		ResourceID: "compute-failed", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "failed",
		RedactedProviderPayload: map[string]any{"resource": map[string]any{
			"id": "compute-failed", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "status": "failed",
		}},
	}}}
	service := controlplane.NewService(ledger, fabric)

	if err := app.runProviderReconcileOnce(context.Background(), service, time.Now().UTC()); err != nil {
		t.Fatalf("first provider reconcile: %v", err)
	}
	if err := app.runProviderReconcileOnce(context.Background(), service, time.Now().UTC()); err != nil {
		t.Fatalf("second provider reconcile: %v", err)
	}
	compute, _ := app.getCompute("compute-failed")
	if len(ledger.releases) != 1 || ledger.releases[0].HoldID != "hold-compute-failed" || ledger.releases[0].AmountCents != 7862 || compute["holdReleaseId"] != "release-compute-compute-failed" || compute["billingStatus"] != "stopped" {
		t.Fatalf("failed compute hold was not released once: releases=%#v compute=%#v", ledger.releases, compute)
	}
}

type failedComputeOperationFabric struct {
	fakeFabricClient
	operations []clients.FabricOperation
}

func (f *failedComputeOperationFabric) ListOperations(context.Context) ([]clients.FabricOperation, error) {
	return f.operations, nil
}

func freshBillableResource(row map[string]any) map[string]any {
	row["providerStatus"] = firstNonEmpty(stringValue(row["providerStatus"]), stringValue(row["status"]))
	row["lastProviderSyncAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	return row
}

func controlPlaneServiceForTest(ledger clients.LedgerClient) *controlplane.Service {
	return controlplane.NewService(ledger, &fakeFabricClient{})
}
