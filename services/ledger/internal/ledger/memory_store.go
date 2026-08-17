package ledger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type MemoryStore struct {
	mu                        sync.Mutex
	idempotency               map[string]idempotencyRecord
	reconciliationIdempotency map[string]idempotencyRecord
	receipts                  map[string]Receipt
	nextID                    int64
}

func (s *MemoryStore) Ready(context.Context) error {
	return nil
}

type idempotencyRecord struct {
	payloadHash string
	result      any
}

func cloneMemoryValue[T any](value T) T {
	payload, _ := json.Marshal(value)
	var clone T
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	_ = decoder.Decode(&clone)
	return clone
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		idempotency:               map[string]idempotencyRecord{},
		reconciliationIdempotency: map[string]idempotencyRecord{},
		receipts:                  map[string]Receipt{},
	}
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
		receiptID := existing.result.(string)
		result, ok := s.receipts[receiptID]
		if !ok {
			return Receipt{}, ErrReceiptNotFound
		}
		result = cloneMemoryValue(result)
		result.Replayed = true
		return result, nil
	}

	receipt := Receipt{ReceiptInput: input, ReceiptID: s.newID("receipt"), CreatedAt: time.Now().UTC()}
	receipt.IdempotencyKey = ""
	receipt = cloneMemoryValue(receipt)
	s.receipts[receipt.ReceiptID] = receipt
	s.idempotency[input.IdempotencyKey] = idempotencyRecord{payloadHash: payloadHash, result: receipt.ReceiptID}
	return cloneMemoryValue(receipt), nil
}

func (s *MemoryStore) Receipt(_ context.Context, receiptID string) (Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt, ok := s.receipts[receiptID]
	if !ok {
		return Receipt{}, ErrReceiptNotFound
	}
	return cloneMemoryValue(receipt), nil
}

func (s *MemoryStore) UpdateReceiptRetention(_ context.Context, input ReceiptRetentionInput) (ReceiptRetentionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.ReceiptID == "" || input.IdempotencyKey == "" || (input.RetainUntil.IsZero() && !input.LegalHold) {
		return ReceiptRetentionResult{}, ErrInvalidReceiptRetentionInput
	}
	payloadHash, err := hashJSON(struct {
		ReceiptID   string    `json:"receiptId"`
		RetainUntil time.Time `json:"retainUntil"`
		LegalHold   bool      `json:"legalHold"`
	}{input.ReceiptID, input.RetainUntil, input.LegalHold})
	if err != nil {
		return ReceiptRetentionResult{}, err
	}
	key := "receipt-retention:" + input.IdempotencyKey
	if existing, ok := s.idempotency[key]; ok {
		if existing.payloadHash != payloadHash {
			return ReceiptRetentionResult{}, ErrIdempotencyConflict
		}
		result := cloneMemoryValue(existing.result.(ReceiptRetentionResult))
		result.Replayed = true
		return result, nil
	}
	receipt, ok := s.receipts[input.ReceiptID]
	if !ok {
		return ReceiptRetentionResult{}, ErrReceiptNotFound
	}
	if !input.RetainUntil.IsZero() && !receipt.Retention.RetainUntil.IsZero() && input.RetainUntil.Before(receipt.Retention.RetainUntil) {
		return ReceiptRetentionResult{}, ErrReceiptRetentionShortening
	}
	if input.RetainUntil.After(receipt.Retention.RetainUntil) {
		receipt.Retention.RetainUntil = input.RetainUntil
	}
	receipt.Retention.LegalHold = receipt.Retention.LegalHold || input.LegalHold
	receipt = cloneMemoryValue(receipt)
	s.receipts[input.ReceiptID] = receipt
	result := ReceiptRetentionResult{ReceiptID: receipt.ReceiptID, Retention: receipt.Retention}
	s.idempotency[key] = idempotencyRecord{payloadHash: payloadHash, result: cloneMemoryValue(result)}
	return cloneMemoryValue(result), nil
}

func (s *MemoryStore) PrivacyDeleteReceipt(_ context.Context, input ReceiptPrivacyDeleteInput) (ReceiptRetentionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.ReceiptID == "" || input.Reason == "" || input.IdempotencyKey == "" {
		return ReceiptRetentionResult{}, ErrInvalidReceiptRetentionInput
	}
	payloadHash, err := hashJSON(struct {
		ReceiptID string `json:"receiptId"`
		Reason    string `json:"reason"`
	}{input.ReceiptID, input.Reason})
	if err != nil {
		return ReceiptRetentionResult{}, err
	}
	key := "receipt-privacy:" + input.IdempotencyKey
	if existing, ok := s.idempotency[key]; ok {
		if existing.payloadHash != payloadHash {
			return ReceiptRetentionResult{}, ErrIdempotencyConflict
		}
		result := cloneMemoryValue(existing.result.(ReceiptRetentionResult))
		result.Replayed = true
		return result, nil
	}
	receipt, ok := s.receipts[input.ReceiptID]
	if !ok {
		return ReceiptRetentionResult{}, ErrReceiptNotFound
	}
	if receipt.Retention.LegalHold {
		return ReceiptRetentionResult{}, ErrReceiptLegalHold
	}
	if receipt.Retention.RetainUntil.After(time.Now().UTC()) {
		return ReceiptRetentionResult{}, ErrReceiptRetentionActive
	}
	if receipt.Retention.PrivacyRedaction == nil {
		receipt.Actor = nil
		receipt.Owner = nil
		receipt.Continuation = nil
		receipt.Retention.PrivacyRedaction = &PrivacyRedactionEvidence{AppliedAt: time.Now().UTC(), Reason: input.Reason, Eligible: true}
	}
	receipt = cloneMemoryValue(receipt)
	s.receipts[input.ReceiptID] = receipt
	result := ReceiptRetentionResult{ReceiptID: receipt.ReceiptID, Retention: receipt.Retention}
	s.idempotency[key] = idempotencyRecord{payloadHash: payloadHash, result: cloneMemoryValue(result)}
	return cloneMemoryValue(result), nil
}

func (s *MemoryStore) ListReceipts(_ context.Context, query ReceiptQuery) (ReceiptPage, error) {
	query, cursor, err := normalizeReceiptQuery(query)
	if err != nil {
		return ReceiptPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	receipts := make([]Receipt, 0, query.Limit+1)
	for _, receipt := range s.receipts {
		if (query.AccountID != "" && receipt.AccountID != query.AccountID) ||
			(query.OrganizationID != "" && receipt.OrganizationID != query.OrganizationID) ||
			(query.WorkspaceID != "" && receipt.WorkspaceID != query.WorkspaceID) ||
			(query.ProjectID != "" && receipt.ProjectID != query.ProjectID) ||
			(query.TaskID != "" && receipt.TaskID != query.TaskID) ||
			(query.JobID != "" && receipt.JobID != query.JobID) ||
			(query.Type != "" && receipt.Type != query.Type) ||
			(query.TypePrefix != "" && !strings.HasPrefix(receipt.Type, query.TypePrefix)) ||
			(query.Status != "" && receipt.Status != query.Status) ||
			(!cursor.CreatedAt.IsZero() && (receipt.CreatedAt.After(cursor.CreatedAt) || (receipt.CreatedAt.Equal(cursor.CreatedAt) && receipt.ReceiptID >= cursor.ReceiptID))) {
			continue
		}
		receipts = append(receipts, cloneMemoryValue(receipt))
	}
	sort.Slice(receipts, func(i, j int) bool {
		if receipts[i].CreatedAt.Equal(receipts[j].CreatedAt) {
			return receipts[i].ReceiptID > receipts[j].ReceiptID
		}
		return receipts[i].CreatedAt.After(receipts[j].CreatedAt)
	})
	hasMore := len(receipts) > query.Limit
	if hasMore {
		receipts = receipts[:query.Limit]
	}
	page := ReceiptPage{Receipts: receipts, HasMore: hasMore}
	if hasMore {
		page.NextCursor = encodeReceiptCursor(receipts[len(receipts)-1])
	}
	return page, nil
}

func (s *MemoryStore) RecordReconciliation(_ context.Context, input ReconciliationInput) (ReconciliationResult, error) {
	if err := validateReconciliationInput(input); err != nil {
		return ReconciliationResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	payloadHash, err := hashJSON(input.Report)
	if err != nil {
		return ReconciliationResult{}, err
	}
	if existing, ok := s.reconciliationIdempotency[input.IdempotencyKey]; ok {
		if existing.payloadHash != payloadHash {
			return ReconciliationResult{}, ErrIdempotencyConflict
		}
		result, ok := existing.result.(ReconciliationResult)
		if !ok || validateReconciliationResult(result) != nil {
			return ReconciliationResult{}, ErrInvalidReconciliationInput
		}
		result = cloneMemoryValue(result)
		result.Replayed = true
		return result, nil
	}

	id := stringFromAny(input.Report["id"])
	status := stringFromAny(input.Report["status"])
	result := ReconciliationResult{ID: id, Status: status, Report: cloneMemoryValue(input.Report), BlockNewWorkspaces: status == "mismatch", Reason: "operator_reconciliation", CreatedAt: time.Now().UTC()}
	s.reconciliationIdempotency[input.IdempotencyKey] = idempotencyRecord{payloadHash: payloadHash, result: cloneMemoryValue(result)}
	return cloneMemoryValue(result), nil
}

func (s *MemoryStore) newID(prefix string) string {
	s.nextID++
	return fmt.Sprintf("%s_%06d", prefix, s.nextID)
}

func hashJSON(payload any) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func stringFromAny(value any) string {
	text, _ := value.(string)
	return text
}
