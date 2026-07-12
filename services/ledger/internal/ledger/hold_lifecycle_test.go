package ledger

import (
	"context"
	"errors"
	"testing"
)

func TestHoldActivationConsumesFirstHourAndKeepsGuarantee(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	_, _ = store.ManualTopUp(ctx, ManualTopUpInput{AccountID: "acct-a", AmountCents: 20000, Currency: "CNY", IdempotencyKey: "topup"})
	hold, err := store.CreateHold(ctx, HoldInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a", AmountCents: 16900, ActivationAmountCents: 100, Currency: "CNY", IdempotencyKey: "hold"})
	if err != nil {
		t.Fatal(err)
	}
	active, err := store.ActivateHold(ctx, HoldActivationInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a", HoldID: hold.ID, Currency: "CNY", ProviderEvidenceRef: "fabric:op-a", IdempotencyKey: "activate"})
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != "active" || active.RemainingCents != 16800 || active.ConsumedCents != 100 {
		t.Fatalf("activation = %#v", active)
	}
	if active.Wallet.BalanceCents != 19900 || active.Wallet.FrozenCents != 16800 || active.Wallet.AvailableCents != 3100 || active.Wallet.TotalSpentCents != 100 {
		t.Fatalf("wallet = %#v", active.Wallet)
	}
}

func TestHoldActivationRejectsMissingProviderEvidence(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	_, _ = store.ManualTopUp(ctx, ManualTopUpInput{AccountID: "acct-a", AmountCents: 200, Currency: "CNY", IdempotencyKey: "topup-no-evidence"})
	hold, _ := store.CreateHold(ctx, HoldInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a", AmountCents: 200, ActivationAmountCents: 100, Currency: "CNY", IdempotencyKey: "hold-no-evidence"})
	_, err := store.ActivateHold(ctx, HoldActivationInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a", HoldID: hold.ID, Currency: "CNY", IdempotencyKey: "activate-no-evidence"})
	if !errors.Is(err, ErrInvalidHoldInput) {
		t.Fatalf("missing evidence error = %v", err)
	}
}

func TestReleaseHoldReleasesLedgerRemainingWithoutDebitingBalance(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	_, _ = store.ManualTopUp(ctx, ManualTopUpInput{AccountID: "acct-a", AmountCents: 20000, Currency: "CNY", IdempotencyKey: "topup-release"})
	hold, _ := store.CreateHold(ctx, HoldInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a", AmountCents: 16900, ActivationAmountCents: 100, Currency: "CNY", IdempotencyKey: "hold-release"})
	_, _ = store.ActivateHold(ctx, HoldActivationInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a", HoldID: hold.ID, Currency: "CNY", ProviderEvidenceRef: "fabric:op-a", IdempotencyKey: "activate-release"})
	release, err := store.ReleaseHold(ctx, HoldReleaseInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a", HoldID: hold.ID, AmountCents: 1, Currency: "CNY", IdempotencyKey: "release"})
	if err != nil {
		t.Fatal(err)
	}
	if release.AmountCents != 16800 || release.Wallet.BalanceCents != 19900 || release.Wallet.FrozenCents != 0 || release.Wallet.AvailableCents != 19900 {
		t.Fatalf("release = %#v", release)
	}
}

func TestReleaseHoldReplaysExistingReleaseAcrossIdempotencyKeys(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	_, _ = store.ManualTopUp(ctx, ManualTopUpInput{AccountID: "acct-a", AmountCents: 1000, Currency: "CNY", IdempotencyKey: "topup-release-replay"})
	hold, _ := store.CreateHold(ctx, HoldInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a", AmountCents: 400, Currency: "CNY", IdempotencyKey: "hold-release-replay"})
	first, err := store.ReleaseHold(ctx, HoldReleaseInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a", HoldID: hold.ID, Currency: "CNY", Reason: "compute_create_failed", IdempotencyKey: "release-from-reconcile"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ReleaseHold(ctx, HoldReleaseInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a", HoldID: hold.ID, Currency: "CNY", Reason: "destroy_compute", IdempotencyKey: "release-from-destroy"})
	if err != nil || !second.Replayed || second.ID != first.ID || second.Wallet.BalanceCents != 1000 || second.Wallet.FrozenCents != 0 {
		t.Fatalf("semantic release replay = %#v first=%#v err=%v", second, first, err)
	}
}

func TestPostgresReleaseHoldReplaysExistingReleaseAcrossIdempotencyKeys(t *testing.T) {
	db := openLedgerTestPostgres(t)
	store := NewPostgresStore(db)
	ctx := context.Background()
	if err := store.Install(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = store.ManualTopUp(ctx, ManualTopUpInput{AccountID: "acct-pg", AmountCents: 1000, Currency: "CNY", IdempotencyKey: "topup-release-replay"})
	hold, _ := store.CreateHold(ctx, HoldInput{AccountID: "acct-pg", ResourceType: "compute", ResourceID: "ca-pg", AmountCents: 400, Currency: "CNY", IdempotencyKey: "hold-release-replay"})
	first, err := store.ReleaseHold(ctx, HoldReleaseInput{AccountID: "acct-pg", ResourceType: "compute", ResourceID: "ca-pg", HoldID: hold.ID, Currency: "CNY", Reason: "compute_create_failed", IdempotencyKey: "release-from-reconcile"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ReleaseHold(ctx, HoldReleaseInput{AccountID: "acct-pg", ResourceType: "compute", ResourceID: "ca-pg", HoldID: hold.ID, Currency: "CNY", Reason: "destroy_compute", IdempotencyKey: "release-from-destroy"})
	if err != nil || !second.Replayed || second.ID != first.ID || second.Wallet.BalanceCents != 1000 || second.Wallet.FrozenCents != 0 {
		t.Fatalf("postgres semantic release replay = %#v first=%#v err=%v", second, first, err)
	}
}

func TestReleaseExhaustedHoldCompletesWithZeroAmount(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	_, _ = store.ManualTopUp(ctx, ManualTopUpInput{AccountID: "acct-a", AmountCents: 200, Currency: "CNY", IdempotencyKey: "topup-exhausted"})
	hold, _ := store.CreateHold(ctx, HoldInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a", AmountCents: 200, ActivationAmountCents: 100, Currency: "CNY", IdempotencyKey: "hold-exhausted"})
	_, _ = store.ActivateHold(ctx, HoldActivationInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a", HoldID: hold.ID, Currency: "CNY", ProviderEvidenceRef: "fabric:machine-a", IdempotencyKey: "activate-exhausted"})
	_, _ = store.SettleResource(ctx, ResourceSettlementInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a", HoldID: hold.ID, AmountCents: 100, Currency: "CNY", IdempotencyKey: "settle-exhausted"})

	release, err := store.ReleaseHold(ctx, HoldReleaseInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a", HoldID: hold.ID, Currency: "CNY", IdempotencyKey: "release-exhausted"})
	if err != nil || release.AmountCents != 0 || store.holds[hold.ID].Status != "released" {
		t.Fatalf("exhausted release = %#v hold=%#v err=%v", release, store.holds[hold.ID], err)
	}
}

func TestSettlementUsesAvailableBeforeOwningHoldAndNeverAnotherHold(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	_, _ = store.ManualTopUp(ctx, ManualTopUpInput{AccountID: "acct-a", AmountCents: 1000, Currency: "CNY", IdempotencyKey: "topup-settle"})
	holdA, _ := store.CreateHold(ctx, HoldInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a", AmountCents: 400, ActivationAmountCents: 100, Currency: "CNY", IdempotencyKey: "hold-a"})
	holdB, _ := store.CreateHold(ctx, HoldInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-b", AmountCents: 400, ActivationAmountCents: 100, Currency: "CNY", IdempotencyKey: "hold-b"})
	_, _ = store.ActivateHold(ctx, HoldActivationInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a", HoldID: holdA.ID, Currency: "CNY", ProviderEvidenceRef: "fabric:a", IdempotencyKey: "activate-a"})
	_, _ = store.ActivateHold(ctx, HoldActivationInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-b", HoldID: holdB.ID, Currency: "CNY", ProviderEvidenceRef: "fabric:b", IdempotencyKey: "activate-b"})

	result, err := store.SettleResource(ctx, ResourceSettlementInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a", HoldID: holdA.ID, AmountCents: 300, Currency: "CNY", ProviderCostEvidenceRef: "fabric:a", IdempotencyKey: "settle-a"})
	if err != nil {
		t.Fatal(err)
	}
	if result.HoldRemainingCents != 200 {
		t.Fatalf("own hold remaining = %d, want 200", result.HoldRemainingCents)
	}
	if got := store.holds[holdB.ID].RemainingCents; got != 300 {
		t.Fatalf("other hold remaining = %d, want 300", got)
	}
	beforeWallet := store.wallets["acct-a"]
	beforeA := store.holds[holdA.ID]
	_, err = store.SettleResource(ctx, ResourceSettlementInput{AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a", HoldID: holdA.ID, AmountCents: 201, Currency: "CNY", ProviderCostEvidenceRef: "fabric:a", IdempotencyKey: "settle-insufficient"})
	if !errors.Is(err, ErrInsufficientResourceHold) {
		t.Fatalf("error = %v", err)
	}
	if store.wallets["acct-a"] != beforeWallet || store.holds[holdA.ID].RemainingCents != beforeA.RemainingCents {
		t.Fatal("failed settlement mutated money")
	}
}
