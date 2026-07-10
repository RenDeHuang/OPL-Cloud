package ledger

import (
	"context"
	"errors"
	"testing"
)

func TestManualTopUpReplayReturnsExistingReceipt(t *testing.T) {
	store := NewMemoryStore()
	input := ManualTopUpInput{
		AccountID:      "acct-alpha",
		AmountCents:    20000,
		Currency:       "CNY",
		OperatorUserID: "usr-admin",
		IdempotencyKey: "topup-once",
		Reason:         "operator_credit",
	}

	first, err := store.ManualTopUp(context.Background(), input)
	if err != nil {
		t.Fatalf("first topup failed: %v", err)
	}
	second, err := store.ManualTopUp(context.Background(), input)
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}
	if !second.Replayed {
		t.Fatalf("expected replayed result")
	}
	if first.LedgerEntry.ID != second.LedgerEntry.ID {
		t.Fatalf("expected same ledger entry on replay")
	}

	wallet, err := store.Wallet(context.Background(), "acct-alpha")
	if err != nil {
		t.Fatalf("wallet failed: %v", err)
	}
	if wallet.BalanceCents != 20000 {
		t.Fatalf("balance = %d, want 20000", wallet.BalanceCents)
	}
}

func TestManualTopUpSameKeyDifferentPayloadConflicts(t *testing.T) {
	store := NewMemoryStore()
	input := ManualTopUpInput{
		AccountID:      "acct-alpha",
		AmountCents:    20000,
		Currency:       "CNY",
		OperatorUserID: "usr-admin",
		IdempotencyKey: "topup-once",
		Reason:         "operator_credit",
	}
	if _, err := store.ManualTopUp(context.Background(), input); err != nil {
		t.Fatalf("topup failed: %v", err)
	}

	input.AmountCents = 30000
	_, err := store.ManualTopUp(context.Background(), input)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestReceiptRejectsMissingIdentityAndSecretContent(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	if _, err := store.RecordReceipt(ctx, ReceiptInput{WorkspaceID: "ws-alpha", IdempotencyKey: "invalid-receipt"}); !errors.Is(err, ErrInvalidReceiptInput) {
		t.Fatalf("missing receipt fields error = %v, want ErrInvalidReceiptInput", err)
	}
	_, err := store.RecordReceipt(ctx, ReceiptInput{Type: "workspace.created", Status: "completed", Surface: "workspace", WorkspaceID: "ws-alpha", Actor: map[string]any{"secret": "must-not-persist"}, IdempotencyKey: "secret-receipt"})
	if !errors.Is(err, ErrInvalidReceiptInput) {
		t.Fatalf("secret receipt error = %v, want ErrInvalidReceiptInput", err)
	}
}

func TestReleaseHoldReducesFrozenWithoutDebitingBalance(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	if _, err := store.ManualTopUp(ctx, ManualTopUpInput{AccountID: "acct-alpha", AmountCents: 2000, Currency: "CNY", OperatorUserID: "usr-admin", IdempotencyKey: "topup-release"}); err != nil {
		t.Fatalf("topup failed: %v", err)
	}
	hold, err := store.CreateHold(ctx, HoldInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ResourceType: "compute", ResourceID: "compute-alpha", AmountCents: 1000, Currency: "CNY", IdempotencyKey: "hold-release"})
	if err != nil {
		t.Fatalf("hold failed: %v", err)
	}

	releaseInput := HoldReleaseInput{
		AccountID:      "acct-alpha",
		WorkspaceID:    "ws-alpha",
		ResourceType:   "compute",
		ResourceID:     "compute-alpha",
		HoldID:         hold.ID,
		AmountCents:    600,
		Currency:       "CNY",
		Reason:         "destroy_compute",
		IdempotencyKey: "release-once",
	}
	released, err := store.ReleaseHold(ctx, releaseInput)
	if err != nil {
		t.Fatalf("release failed: %v", err)
	}
	if released.Status != "released" || released.Wallet.BalanceCents != 2000 || released.Wallet.FrozenCents != 400 || released.Wallet.AvailableCents != 1600 || released.Wallet.TotalSpentCents != 0 {
		t.Fatalf("unexpected release wallet: %#v", released)
	}

	replayed, err := store.ReleaseHold(ctx, releaseInput)
	if err != nil {
		t.Fatalf("release replay failed: %v", err)
	}
	if !replayed.Replayed || replayed.ID != released.ID {
		t.Fatalf("expected same replayed release, got %#v", replayed)
	}

	releaseInput.AmountCents = 700
	_, err = store.ReleaseHold(ctx, releaseInput)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestHoldCanBeAccountResourceScopedBeforeWorkspaceExists(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	if _, err := store.ManualTopUp(ctx, ManualTopUpInput{AccountID: "acct-alpha", AmountCents: 2000, Currency: "CNY", OperatorUserID: "usr-admin", IdempotencyKey: "topup-unbound-hold"}); err != nil {
		t.Fatalf("topup failed: %v", err)
	}
	hold, err := store.CreateHold(ctx, HoldInput{AccountID: "acct-alpha", ResourceType: "compute", ResourceID: "compute-alpha", AmountCents: 1000, Currency: "CNY", IdempotencyKey: "hold-unbound-workspace"})
	if err != nil {
		t.Fatalf("hold without workspace failed: %v", err)
	}
	if hold.WorkspaceID != "" || hold.Wallet.FrozenCents != 1000 {
		t.Fatalf("unexpected hold: %#v", hold)
	}
	release, err := store.ReleaseHold(ctx, HoldReleaseInput{AccountID: "acct-alpha", ResourceType: "compute", ResourceID: "compute-alpha", HoldID: hold.ID, AmountCents: 1000, Currency: "CNY", Reason: "destroy_compute", IdempotencyKey: "release-unbound-workspace"})
	if err != nil {
		t.Fatalf("release without workspace failed: %v", err)
	}
	if release.WorkspaceID != "" || release.Wallet.FrozenCents != 0 || release.Wallet.BalanceCents != 2000 {
		t.Fatalf("unexpected release: %#v", release)
	}
}

func TestCreateHoldRequiresResourceIdentity(t *testing.T) {
	store := NewMemoryStore()
	_, err := store.CreateHold(context.Background(), HoldInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", AmountCents: 1000, Currency: "CNY", IdempotencyKey: "hold-missing-resource"})
	if !errors.Is(err, ErrInvalidHoldInput) {
		t.Fatalf("expected invalid hold input, got %v", err)
	}
}

func TestLedgerMutationsReturnStableAuditFacts(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	topup, err := store.ManualTopUp(ctx, ManualTopUpInput{AccountID: "acct-alpha", AmountCents: 5000, Currency: "CNY", OperatorUserID: "usr-admin", Reason: "manual_adjustment", IdempotencyKey: "audit-topup"})
	if err != nil {
		t.Fatalf("topup failed: %v", err)
	}
	if topup.TopUp.ID == "" || topup.LedgerEntry.ID == "" || topup.WalletTransaction.ID == "" || topup.WalletTransaction.LedgerEntryID != topup.LedgerEntry.ID || topup.LedgerEntry.Source != "manual_topup" {
		t.Fatalf("topup must return linked audit facts, got %#v", topup)
	}

	hold, err := store.CreateHold(ctx, HoldInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ResourceType: "compute", ResourceID: "compute-alpha", AmountCents: 2000, Currency: "CNY", IdempotencyKey: "audit-hold"})
	if err != nil {
		t.Fatalf("hold failed: %v", err)
	}
	if hold.ID == "" || hold.LedgerEntryID == "" || hold.WalletTransactionID == "" || hold.Status != "held" || hold.Wallet.FrozenCents != 2000 {
		t.Fatalf("hold must return linked audit facts, got %#v", hold)
	}

	release, err := store.ReleaseHold(ctx, HoldReleaseInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ResourceType: "compute", ResourceID: "compute-alpha", HoldID: hold.ID, AmountCents: 500, Currency: "CNY", Reason: "destroy_compute", IdempotencyKey: "audit-release"})
	if err != nil {
		t.Fatalf("release failed: %v", err)
	}
	if release.ID == "" || release.LedgerEntryID == "" || release.WalletTransactionID == "" || release.Status != "released" || release.Wallet.BalanceCents != 5000 || release.Wallet.FrozenCents != 1500 || release.Wallet.TotalSpentCents != 0 {
		t.Fatalf("release must return linked audit facts without debiting balance, got %#v", release)
	}

	settlementInput := ResourceSettlementInput{
		AccountID:               "acct-alpha",
		WorkspaceID:             "ws-alpha",
		ResourceType:            "compute",
		ResourceID:              "compute-alpha",
		AmountCents:             1200,
		Currency:                "CNY",
		PricingVersion:          "pricing-2026-07",
		PriceSnapshot:           map[string]any{"priceBasis": "hourly", "userPriceCents": float64(1200), "providerCostEstimateCents": float64(700)},
		UsagePeriodStart:        "2026-07-08T00:00:00Z",
		UsagePeriodEnd:          "2026-07-08T01:00:00Z",
		Quantity:                1,
		Unit:                    "hour",
		ProviderCostEvidenceRef: "tencent-bill-row-001",
		IdempotencyKey:          "audit-settlement",
	}
	settlement, err := store.SettleResource(ctx, settlementInput)
	if err != nil {
		t.Fatalf("settlement failed: %v", err)
	}
	if settlement.ID == "" || settlement.LedgerEntryID == "" || settlement.WalletTransactionID == "" || settlement.Status != "settled" || settlement.Wallet.BalanceCents != 3800 || settlement.Wallet.FrozenCents != 300 || settlement.Wallet.TotalSpentCents != 1200 {
		t.Fatalf("settlement must return linked audit facts and debit balance, got %#v", settlement)
	}
	if settlement.PricingVersion != "pricing-2026-07" || settlement.PriceSnapshot["priceBasis"] != "hourly" || settlement.UsagePeriodStart == "" || settlement.UsagePeriodEnd == "" || settlement.Quantity != 1 || settlement.Unit != "hour" || settlement.ProviderCostEvidenceRef == "" {
		t.Fatalf("settlement must preserve price and provider evidence snapshot, got %#v", settlement)
	}

	receipt, err := store.RecordReceipt(ctx, ReceiptInput{Type: "workspace.created", Status: "completed", Surface: "workspace", OrganizationID: "org-alpha", WorkspaceID: "ws-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha", Execution: map[string]any{"providerRequestId": "provider-request-alpha"}, OutputRefs: map[string]any{"redactedUrl": "https://workspace.example.test/w/ws-alpha"}, Continuation: map[string]any{"continuationId": "continuation-alpha"}, IdempotencyKey: "audit-receipt"})
	if err != nil {
		t.Fatalf("receipt failed: %v", err)
	}
	if receipt.ReceiptID == "" || receipt.ProjectID != "project-alpha" || receipt.Status != "completed" {
		t.Fatalf("general receipt must preserve execution identity, got %#v", receipt)
	}
	loaded, err := store.Receipt(ctx, receipt.ReceiptID)
	if err != nil || loaded.JobID != "job-alpha" {
		t.Fatalf("load receipt: %#v, %v", loaded, err)
	}

	reconciliation, err := store.RecordReconciliation(ctx, ReconciliationInput{Report: map[string]any{"id": "recon-alpha", "status": "mismatch"}, IdempotencyKey: "audit-reconciliation"})
	if err != nil {
		t.Fatalf("reconciliation failed: %v", err)
	}
	if reconciliation.ID != "recon-alpha" || reconciliation.Status != "mismatch" || !reconciliation.BlockNewWorkspaces {
		t.Fatalf("reconciliation mismatch must block new workspaces, got %#v", reconciliation)
	}

	replayed, err := store.SettleResource(ctx, settlementInput)
	if err != nil {
		t.Fatalf("settlement replay failed: %v", err)
	}
	if !replayed.Replayed || replayed.ID != settlement.ID || replayed.LedgerEntryID != settlement.LedgerEntryID || replayed.WalletTransactionID != settlement.WalletTransactionID {
		t.Fatalf("settlement replay must return the same facts, got %#v", replayed)
	}
	if replayed.PriceSnapshot["providerCostEstimateCents"] != float64(700) {
		t.Fatalf("settlement replay lost price snapshot: %#v", replayed.PriceSnapshot)
	}

	settlementInput.AmountCents = 1300
	_, err = store.SettleResource(ctx, settlementInput)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected settlement idempotency conflict, got %v", err)
	}
}

func TestWalletTransactionsCarryAfterSnapshot(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	topup, err := store.ManualTopUp(ctx, ManualTopUpInput{AccountID: "acct-alpha", AmountCents: 5000, Currency: "CNY", OperatorUserID: "usr-admin", IdempotencyKey: "snapshot-topup"})
	if err != nil {
		t.Fatalf("topup failed: %v", err)
	}
	if topup.WalletTransaction.BalanceCents != 5000 || topup.WalletTransaction.AvailableCents != 5000 || topup.WalletTransaction.TotalSpentCents != 0 {
		t.Fatalf("topup wallet transaction missing after snapshot: %#v", topup.WalletTransaction)
	}
	hold, err := store.CreateHold(ctx, HoldInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ResourceType: "compute", ResourceID: "compute-alpha", AmountCents: 2000, Currency: "CNY", IdempotencyKey: "snapshot-hold"})
	if err != nil {
		t.Fatalf("hold failed: %v", err)
	}
	if hold.Wallet.FrozenCents != 2000 || hold.Wallet.AvailableCents != 3000 {
		t.Fatalf("hold wallet missing after snapshot: %#v", hold.Wallet)
	}
	settlement, err := store.SettleResource(ctx, ResourceSettlementInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ResourceType: "compute", ResourceID: "compute-alpha", AmountCents: 1200, Currency: "CNY", PricingVersion: "pricing-2026-07", PriceSnapshot: map[string]any{"priceBasis": "hourly"}, UsagePeriodStart: "2026-07-08T00:00:00Z", UsagePeriodEnd: "2026-07-08T01:00:00Z", Quantity: 1, Unit: "hour", ProviderCostEvidenceRef: "tencent-bill-row-001", IdempotencyKey: "snapshot-settlement"})
	if err != nil {
		t.Fatalf("settlement failed: %v", err)
	}
	if settlement.Wallet.BalanceCents != 3800 || settlement.Wallet.FrozenCents != 800 || settlement.Wallet.AvailableCents != 3000 || settlement.Wallet.TotalSpentCents != 1200 {
		t.Fatalf("settlement wallet missing after snapshot: %#v", settlement.Wallet)
	}
}
