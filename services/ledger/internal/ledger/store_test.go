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
