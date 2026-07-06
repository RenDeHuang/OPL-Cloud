package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type MemoryStore struct {
	mu          sync.Mutex
	wallets     map[string]Wallet
	idempotency map[string]idempotencyRecord
	nextID      int64
}

type idempotencyRecord struct {
	payloadHash string
	result      ManualTopUpResult
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		wallets:     map[string]Wallet{},
		idempotency: map[string]idempotencyRecord{},
	}
}

func (s *MemoryStore) ManualTopUp(_ context.Context, input ManualTopUpInput) (ManualTopUpResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	payloadHash, err := hashManualTopUp(input)
	if err != nil {
		return ManualTopUpResult{}, err
	}
	if existing, ok := s.idempotency[input.IdempotencyKey]; ok {
		if existing.payloadHash != payloadHash {
			return ManualTopUpResult{}, ErrIdempotencyConflict
		}
		result := existing.result
		result.Replayed = true
		return result, nil
	}

	now := time.Now().UTC()
	wallet := s.wallets[input.AccountID]
	if wallet.AccountID == "" {
		wallet = Wallet{AccountID: input.AccountID, Currency: input.Currency}
	}
	wallet.BalanceCents += input.AmountCents
	wallet.Currency = input.Currency
	wallet.UpdatedAt = now

	entry := LedgerEntry{
		ID:             s.newID("le"),
		AccountID:      input.AccountID,
		AmountCents:    input.AmountCents,
		Currency:       input.Currency,
		Direction:      "credit",
		Source:         "manual_topup",
		OperatorUserID: input.OperatorUserID,
		Reason:         input.Reason,
		CreatedAt:      now,
	}
	tx := WalletTransaction{
		ID:            s.newID("wtx"),
		AccountID:     input.AccountID,
		LedgerEntryID: entry.ID,
		AmountCents:   input.AmountCents,
		BalanceCents:  wallet.BalanceCents,
		Currency:      input.Currency,
		CreatedAt:     now,
	}
	topup := ManualTopUp{
		ID:             s.newID("mtu"),
		AccountID:      input.AccountID,
		AmountCents:    input.AmountCents,
		Currency:       input.Currency,
		OperatorUserID: input.OperatorUserID,
		LedgerEntryID:  entry.ID,
		Reason:         input.Reason,
		CreatedAt:      now,
	}

	result := ManualTopUpResult{TopUp: topup, LedgerEntry: entry, WalletTransaction: tx, Wallet: wallet}
	s.wallets[input.AccountID] = wallet
	s.idempotency[input.IdempotencyKey] = idempotencyRecord{payloadHash: payloadHash, result: result}
	return result, nil
}

func (s *MemoryStore) Wallet(_ context.Context, accountID string) (Wallet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	wallet := s.wallets[accountID]
	if wallet.AccountID == "" {
		wallet = Wallet{AccountID: accountID, Currency: "CNY"}
	}
	return wallet, nil
}

func (s *MemoryStore) newID(prefix string) string {
	s.nextID++
	return fmt.Sprintf("%s_%06d", prefix, s.nextID)
}

func hashManualTopUp(input ManualTopUpInput) (string, error) {
	payload := struct {
		AccountID      string `json:"accountId"`
		AmountCents    int64  `json:"amountCents"`
		Currency       string `json:"currency"`
		OperatorUserID string `json:"operatorUserId"`
		Reason         string `json:"reason,omitempty"`
	}{
		AccountID:      input.AccountID,
		AmountCents:    input.AmountCents,
		Currency:       input.Currency,
		OperatorUserID: input.OperatorUserID,
		Reason:         input.Reason,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
