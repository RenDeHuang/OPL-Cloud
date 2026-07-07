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

func TestCreateHoldRequiresResourceIdentity(t *testing.T) {
	store := NewMemoryStore()
	_, err := store.CreateHold(context.Background(), HoldInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", AmountCents: 1000, Currency: "CNY", IdempotencyKey: "hold-missing-resource"})
	if !errors.Is(err, ErrInvalidHoldInput) {
		t.Fatalf("expected invalid hold input, got %v", err)
	}
}
