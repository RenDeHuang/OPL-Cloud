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
	receipts    map[string]Receipt
	entries     []LedgerEntry
	walletTx    []WalletTransaction
	topups      []ManualTopUp
	settlements []ResourceSettlementResult
	nextID      int64
}

type idempotencyRecord struct {
	payloadHash string
	result      any
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		wallets:     map[string]Wallet{},
		idempotency: map[string]idempotencyRecord{},
		receipts:    map[string]Receipt{},
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
		result := existing.result.(ManualTopUpResult)
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
	wallet.AvailableCents = wallet.BalanceCents - wallet.FrozenCents
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
		ID:              s.newID("wtx"),
		AccountID:       input.AccountID,
		LedgerEntryID:   entry.ID,
		AmountCents:     input.AmountCents,
		BalanceCents:    wallet.BalanceCents,
		FrozenCents:     wallet.FrozenCents,
		AvailableCents:  wallet.AvailableCents,
		TotalSpentCents: wallet.TotalSpentCents,
		Currency:        input.Currency,
		CreatedAt:       now,
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
	s.entries = append(s.entries, entry)
	s.walletTx = append(s.walletTx, tx)
	s.topups = append(s.topups, topup)
	s.idempotency[input.IdempotencyKey] = idempotencyRecord{payloadHash: payloadHash, result: result}
	return result, nil
}

func (s *MemoryStore) CreateHold(_ context.Context, input HoldInput) (HoldResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if input.ResourceType == "" || input.ResourceID == "" || input.AmountCents <= 0 {
		return HoldResult{}, ErrInvalidHoldInput
	}
	payloadHash, err := hashJSON(struct {
		AccountID    string `json:"accountId"`
		WorkspaceID  string `json:"workspaceId"`
		ResourceType string `json:"resourceType"`
		ResourceID   string `json:"resourceId"`
		AmountCents  int64  `json:"amountCents"`
		Currency     string `json:"currency"`
	}{input.AccountID, input.WorkspaceID, input.ResourceType, input.ResourceID, input.AmountCents, input.Currency})
	if err != nil {
		return HoldResult{}, err
	}
	if existing, ok := s.idempotency[input.IdempotencyKey]; ok {
		if existing.payloadHash != payloadHash {
			return HoldResult{}, ErrIdempotencyConflict
		}
		result := existing.result.(HoldResult)
		result.Replayed = true
		return result, nil
	}

	now := time.Now().UTC()
	wallet := s.wallets[input.AccountID]
	if wallet.AccountID == "" {
		wallet = Wallet{AccountID: input.AccountID, Currency: input.Currency}
	}
	wallet.Currency = input.Currency
	wallet.AvailableCents = wallet.BalanceCents - wallet.FrozenCents
	if wallet.AvailableCents < input.AmountCents {
		return HoldResult{}, ErrInsufficientBalance
	}
	wallet.FrozenCents += input.AmountCents
	wallet.AvailableCents = wallet.BalanceCents - wallet.FrozenCents
	wallet.UpdatedAt = now

	entry := LedgerEntry{
		ID:          s.newID("le"),
		AccountID:   input.AccountID,
		AmountCents: input.AmountCents,
		Currency:    input.Currency,
		Direction:   "hold",
		Source:      input.ResourceType + "_hold",
		Reason:      input.ResourceID,
		CreatedAt:   now,
	}
	tx := WalletTransaction{
		ID:              s.newID("wtx"),
		AccountID:       input.AccountID,
		LedgerEntryID:   entry.ID,
		AmountCents:     input.AmountCents,
		BalanceCents:    wallet.BalanceCents,
		FrozenCents:     wallet.FrozenCents,
		AvailableCents:  wallet.AvailableCents,
		TotalSpentCents: wallet.TotalSpentCents,
		Currency:        input.Currency,
		CreatedAt:       now,
	}
	result := HoldResult{
		ID:                  s.newID("hold"),
		AccountID:           input.AccountID,
		WorkspaceID:         input.WorkspaceID,
		ResourceType:        input.ResourceType,
		ResourceID:          input.ResourceID,
		AmountCents:         input.AmountCents,
		Currency:            input.Currency,
		Status:              "held",
		LedgerEntryID:       entry.ID,
		WalletTransactionID: tx.ID,
		Wallet:              wallet,
		CreatedAt:           now,
	}
	s.wallets[input.AccountID] = wallet
	s.entries = append(s.entries, entry)
	s.walletTx = append(s.walletTx, tx)
	s.idempotency[input.IdempotencyKey] = idempotencyRecord{payloadHash: payloadHash, result: result}
	return result, nil
}

func (s *MemoryStore) ReleaseHold(_ context.Context, input HoldReleaseInput) (HoldReleaseResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	payloadHash, err := hashHoldRelease(input)
	if err != nil {
		return HoldReleaseResult{}, err
	}
	if existing, ok := s.idempotency[input.IdempotencyKey]; ok {
		if existing.payloadHash != payloadHash {
			return HoldReleaseResult{}, ErrIdempotencyConflict
		}
		result := existing.result.(HoldReleaseResult)
		result.Replayed = true
		return result, nil
	}

	now := time.Now().UTC()
	wallet := s.wallets[input.AccountID]
	if wallet.AccountID == "" {
		wallet = Wallet{AccountID: input.AccountID, Currency: input.Currency}
	}
	if wallet.FrozenCents < input.AmountCents {
		return HoldReleaseResult{}, ErrInsufficientFrozen
	}
	wallet.FrozenCents -= input.AmountCents
	wallet.Currency = input.Currency
	wallet.AvailableCents = wallet.BalanceCents - wallet.FrozenCents
	wallet.UpdatedAt = now

	entry := LedgerEntry{
		ID:          s.newID("le"),
		AccountID:   input.AccountID,
		AmountCents: input.AmountCents,
		Currency:    input.Currency,
		Direction:   "release",
		Source:      input.ResourceType + "_hold_released",
		Reason:      input.Reason,
		CreatedAt:   now,
	}
	tx := WalletTransaction{
		ID:              s.newID("wtx"),
		AccountID:       input.AccountID,
		LedgerEntryID:   entry.ID,
		AmountCents:     0,
		BalanceCents:    wallet.BalanceCents,
		FrozenCents:     wallet.FrozenCents,
		AvailableCents:  wallet.AvailableCents,
		TotalSpentCents: wallet.TotalSpentCents,
		Currency:        input.Currency,
		CreatedAt:       now,
	}
	result := HoldReleaseResult{
		ID:                  s.newID("hrel"),
		AccountID:           input.AccountID,
		WorkspaceID:         input.WorkspaceID,
		ResourceType:        input.ResourceType,
		ResourceID:          input.ResourceID,
		HoldID:              input.HoldID,
		AmountCents:         input.AmountCents,
		Currency:            input.Currency,
		Status:              "released",
		LedgerEntryID:       entry.ID,
		WalletTransactionID: tx.ID,
		Wallet:              wallet,
		CreatedAt:           now,
	}
	s.wallets[input.AccountID] = wallet
	s.entries = append(s.entries, entry)
	s.walletTx = append(s.walletTx, tx)
	s.idempotency[input.IdempotencyKey] = idempotencyRecord{payloadHash: payloadHash, result: result}
	return result, nil
}

func (s *MemoryStore) RecordReceipt(_ context.Context, input ReceiptInput) (Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateReceiptInput(input); err != nil {
		return Receipt{}, err
	}

	hashInput := input
	hashInput.IdempotencyKey = ""
	payloadHash, err := hashJSON(hashInput)
	if err != nil {
		return Receipt{}, err
	}
	if existing, ok := s.idempotency[input.IdempotencyKey]; ok {
		if existing.payloadHash != payloadHash {
			return Receipt{}, ErrIdempotencyConflict
		}
		result := existing.result.(Receipt)
		result.Replayed = true
		return result, nil
	}

	receipt := Receipt{ReceiptInput: input, ReceiptID: s.newID("receipt"), CreatedAt: time.Now().UTC()}
	receipt.IdempotencyKey = ""
	s.receipts[receipt.ReceiptID] = receipt
	s.idempotency[input.IdempotencyKey] = idempotencyRecord{payloadHash: payloadHash, result: receipt}
	return receipt, nil
}

func (s *MemoryStore) Receipt(_ context.Context, receiptID string) (Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt, ok := s.receipts[receiptID]
	if !ok {
		return Receipt{}, ErrReceiptNotFound
	}
	return receipt, nil
}

func (s *MemoryStore) SettleResource(_ context.Context, input ResourceSettlementInput) (ResourceSettlementResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	payloadHash, err := hashJSON(struct {
		AccountID               string         `json:"accountId"`
		WorkspaceID             string         `json:"workspaceId"`
		ResourceType            string         `json:"resourceType"`
		ResourceID              string         `json:"resourceId"`
		AmountCents             int64          `json:"amountCents"`
		Currency                string         `json:"currency"`
		PricingVersion          string         `json:"pricingVersion"`
		PriceSnapshot           map[string]any `json:"priceSnapshot"`
		UsagePeriodStart        string         `json:"usagePeriodStart"`
		UsagePeriodEnd          string         `json:"usagePeriodEnd"`
		Quantity                float64        `json:"quantity"`
		Unit                    string         `json:"unit"`
		ProviderCostEvidenceRef string         `json:"providerCostEvidenceRef"`
	}{input.AccountID, input.WorkspaceID, input.ResourceType, input.ResourceID, input.AmountCents, input.Currency, input.PricingVersion, input.PriceSnapshot, input.UsagePeriodStart, input.UsagePeriodEnd, input.Quantity, input.Unit, input.ProviderCostEvidenceRef})
	if err != nil {
		return ResourceSettlementResult{}, err
	}
	if existing, ok := s.idempotency[input.IdempotencyKey]; ok {
		if existing.payloadHash != payloadHash {
			return ResourceSettlementResult{}, ErrIdempotencyConflict
		}
		result := existing.result.(ResourceSettlementResult)
		result.Replayed = true
		return result, nil
	}

	now := time.Now().UTC()
	wallet := s.wallets[input.AccountID]
	if wallet.AccountID == "" {
		wallet = Wallet{AccountID: input.AccountID, Currency: input.Currency}
	}
	if wallet.BalanceCents < input.AmountCents {
		return ResourceSettlementResult{}, ErrInsufficientBalance
	}
	released := minInt64(wallet.FrozenCents, input.AmountCents)
	wallet.BalanceCents -= input.AmountCents
	wallet.FrozenCents -= released
	wallet.TotalSpentCents += input.AmountCents
	wallet.Currency = input.Currency
	wallet.AvailableCents = wallet.BalanceCents - wallet.FrozenCents
	wallet.UpdatedAt = now

	entry := LedgerEntry{ID: s.newID("le"), AccountID: input.AccountID, AmountCents: input.AmountCents, Currency: input.Currency, Direction: "debit", Source: input.ResourceType + "_settlement", Reason: input.WorkspaceID, CreatedAt: now}
	tx := WalletTransaction{ID: s.newID("wtx"), AccountID: input.AccountID, LedgerEntryID: entry.ID, AmountCents: -input.AmountCents, BalanceCents: wallet.BalanceCents, FrozenCents: wallet.FrozenCents, AvailableCents: wallet.AvailableCents, TotalSpentCents: wallet.TotalSpentCents, Currency: input.Currency, CreatedAt: now}
	result := ResourceSettlementResult{ID: s.newID("settle"), AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, AmountCents: input.AmountCents, Currency: input.Currency, Status: "settled", LedgerEntryID: entry.ID, WalletTransactionID: tx.ID, PricingVersion: input.PricingVersion, PriceSnapshot: cloneAnyMap(input.PriceSnapshot), UsagePeriodStart: input.UsagePeriodStart, UsagePeriodEnd: input.UsagePeriodEnd, Quantity: input.Quantity, Unit: input.Unit, ProviderCostEvidenceRef: input.ProviderCostEvidenceRef, Wallet: wallet, CreatedAt: now}
	s.wallets[input.AccountID] = wallet
	s.entries = append(s.entries, entry)
	s.walletTx = append(s.walletTx, tx)
	s.settlements = append(s.settlements, result)
	s.idempotency[input.IdempotencyKey] = idempotencyRecord{payloadHash: payloadHash, result: result}
	return result, nil
}

func (s *MemoryStore) RecordReconciliation(_ context.Context, input ReconciliationInput) (ReconciliationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	payloadHash, err := hashJSON(input.Report)
	if err != nil {
		return ReconciliationResult{}, err
	}
	if existing, ok := s.idempotency[input.IdempotencyKey]; ok {
		if existing.payloadHash != payloadHash {
			return ReconciliationResult{}, ErrIdempotencyConflict
		}
		result := existing.result.(ReconciliationResult)
		result.Replayed = true
		return result, nil
	}

	id := stringFromAny(input.Report["id"])
	if id == "" {
		id = s.newID("recon")
	}
	status := stringFromAny(input.Report["status"])
	if status == "" {
		status = "ok"
	}
	result := ReconciliationResult{ID: id, Status: status, Report: input.Report, BlockNewWorkspaces: status != "ok", Reason: "operator_reconciliation", CreatedAt: time.Now().UTC()}
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
	wallet.AvailableCents = wallet.BalanceCents - wallet.FrozenCents
	return wallet, nil
}

func (s *MemoryStore) ListLedgerEntries(_ context.Context, accountID string) ([]LedgerEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	output := []LedgerEntry{}
	for _, entry := range s.entries {
		if accountID == "" || entry.AccountID == accountID {
			output = append(output, entry)
		}
	}
	return output, nil
}

func (s *MemoryStore) ListWalletTransactions(_ context.Context, accountID string) ([]WalletTransaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	output := []WalletTransaction{}
	for _, tx := range s.walletTx {
		if accountID == "" || tx.AccountID == accountID {
			output = append(output, tx)
		}
	}
	return output, nil
}

func (s *MemoryStore) ListManualTopUps(_ context.Context, accountID string) ([]ManualTopUp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	output := []ManualTopUp{}
	for _, topup := range s.topups {
		if accountID == "" || topup.AccountID == accountID {
			output = append(output, topup)
		}
	}
	return output, nil
}

func (s *MemoryStore) ListResourceSettlements(_ context.Context, accountID string) ([]ResourceSettlementResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	output := []ResourceSettlementResult{}
	for _, settlement := range s.settlements {
		if accountID == "" || settlement.AccountID == accountID {
			output = append(output, settlement)
		}
	}
	return output, nil
}

func (s *MemoryStore) newID(prefix string) string {
	s.nextID++
	return fmt.Sprintf("%s_%06d", prefix, s.nextID)
}

func cloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
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
	return hashJSON(payload)
}

func hashHoldRelease(input HoldReleaseInput) (string, error) {
	return hashJSON(struct {
		AccountID    string `json:"accountId"`
		WorkspaceID  string `json:"workspaceId"`
		ResourceType string `json:"resourceType"`
		ResourceID   string `json:"resourceId"`
		HoldID       string `json:"holdId"`
		AmountCents  int64  `json:"amountCents"`
		Currency     string `json:"currency"`
		Reason       string `json:"reason,omitempty"`
	}{
		AccountID:    input.AccountID,
		WorkspaceID:  input.WorkspaceID,
		ResourceType: input.ResourceType,
		ResourceID:   input.ResourceID,
		HoldID:       input.HoldID,
		AmountCents:  input.AmountCents,
		Currency:     input.Currency,
		Reason:       input.Reason,
	})
}

func hashJSON(payload any) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func minInt64(a int64, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func stringFromAny(value any) string {
	text, _ := value.(string)
	return text
}
