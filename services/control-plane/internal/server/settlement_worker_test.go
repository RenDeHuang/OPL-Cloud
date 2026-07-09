package server

import (
	"context"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type settlementWorkerLedger struct {
	fakeLedgerClient
	settlements []clients.ResourceSettlementInput
	keys        []string
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

func TestPeriodicSettlementWorkerSettlesActiveResources(t *testing.T) {
	app := newControlPlaneAppEmpty()
	app.computes["compute-alpha"] = map[string]any{"id": "compute-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "packageId": "basic", "status": "running"}
	app.storages["storage-alpha"] = map[string]any{"id": "storage-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "packageId": "basic", "status": "available", "sizeGb": 10}
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

func TestPeriodicSettlementWorkerDoesNotDuplicateControlPlaneProjectionsOnReplay(t *testing.T) {
	app := newControlPlaneAppEmpty()
	app.computes["compute-alpha"] = map[string]any{"id": "compute-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "packageId": "basic", "status": "running"}
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
	if len(app.ledger) != 1 {
		t.Fatalf("control-plane ledger projection duplicated replayed settlement: %#v", app.ledger)
	}
	if len(app.walletTx) != 1 {
		t.Fatalf("control-plane wallet transaction projection duplicated replayed settlement: %#v", app.walletTx)
	}
	compute := app.computes["compute-alpha"]
	if compute["settlementId"] != "settlement-compute-alpha" || compute["usagePeriodEnd"] != "2026-07-09T12:00:00Z" {
		t.Fatalf("resource missing last settlement metadata: %#v", compute)
	}
}

func controlPlaneServiceForTest(ledger clients.LedgerClient) *controlplane.Service {
	return controlplane.NewService(ledger, &fakeFabricClient{})
}
