package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

type MemoryStore struct {
	mu                      sync.Mutex
	idempotency             map[string]idempotencyRecord
	reviewPolicyIdempotency map[string]idempotencyRecord
	receipts                map[string]Receipt
	reviewPolicies          map[string]ReviewPolicy
	nextID                  int64
}

type idempotencyRecord struct {
	payloadHash string
	result      any
}

func cloneMemoryValue[T any](value T) T {
	payload, _ := json.Marshal(value)
	var clone T
	_ = json.Unmarshal(payload, &clone)
	return clone
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		idempotency:             map[string]idempotencyRecord{},
		reviewPolicyIdempotency: map[string]idempotencyRecord{},
		receipts:                map[string]Receipt{},
		reviewPolicies:          map[string]ReviewPolicy{},
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
	finalizeReceiptContinuation(&receipt)
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
	gate, err := s.evaluateReviewGateLocked(ReviewGateInput{ExecutionIdentity: executionIdentityFromReceipt(receipt), ReviewIDs: stringSlice(receipt.Continuation["reviewIds"])})
	return receiptForRead(cloneMemoryValue(receipt), gate, err), nil
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
			(query.Status != "" && receipt.Status != query.Status) ||
			(!cursor.CreatedAt.IsZero() && (receipt.CreatedAt.After(cursor.CreatedAt) || (receipt.CreatedAt.Equal(cursor.CreatedAt) && receipt.ReceiptID >= cursor.ReceiptID))) {
			continue
		}
		gate, gateErr := s.evaluateReviewGateLocked(ReviewGateInput{ExecutionIdentity: executionIdentityFromReceipt(receipt), ReviewIDs: stringSlice(receipt.Continuation["reviewIds"])})
		receipts = append(receipts, receiptForRead(cloneMemoryValue(receipt), gate, gateErr))
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

func (s *MemoryStore) Continuation(ctx context.Context, receiptID string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt, ok := s.receipts[receiptID]
	if !ok {
		return nil, ErrReceiptNotFound
	}
	continuation, err := continuationFromReceipt(receipt)
	if err != nil {
		return nil, err
	}
	identity := executionIdentityFromReceipt(receipt)
	if !validExecutionIdentity(identity) {
		return nil, ErrContinuationIneligible
	}
	gate, err := s.evaluateReviewGateLocked(ReviewGateInput{ExecutionIdentity: identity, ReviewIDs: stringSlice(continuation["reviewIds"])})
	if errors.Is(err, ErrReviewPolicyNotFound) {
		return nil, ErrContinuationIneligible
	}
	if err != nil {
		return nil, err
	}
	if !gate.ContinuationEligible {
		return nil, ErrContinuationIneligible
	}
	return continuation, nil
}

func (s *MemoryStore) RecordArtifact(ctx context.Context, input ArtifactInput) (Artifact, error) {
	if err := validateArtifactInput(input); err != nil {
		return Artifact{}, err
	}
	receipt, err := s.RecordReceipt(ctx, ReceiptInput{
		Type: artifactReceiptType, Status: "completed", Surface: "ledger",
		OrganizationID: input.OrganizationID, WorkspaceID: input.WorkspaceID, ProjectID: input.ProjectID, TaskID: input.TaskID, JobID: input.JobID,
		ArtifactID:     evidenceID("artifact", input.IdempotencyKey),
		OutputRefs:     map[string]any{"digest": input.Digest, "mediaType": input.MediaType, "sizeBytes": input.SizeBytes, "storageRef": input.StorageRef},
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return Artifact{}, err
	}
	return artifactFromReceipt(receipt), nil
}

func (s *MemoryStore) Artifact(_ context.Context, artifactID string) (Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, receipt := range s.receipts {
		if receipt.Type == artifactReceiptType && receipt.ArtifactID == artifactID {
			return artifactFromReceipt(receipt), nil
		}
	}
	return Artifact{}, ErrArtifactNotFound
}

func (s *MemoryStore) RecordReview(ctx context.Context, input ReviewInput) (Review, error) {
	if err := validateReviewInput(input); err != nil {
		return Review{}, err
	}
	status := reviewReceiptStatus(input.Decision)
	receipt, err := s.RecordReceipt(ctx, ReceiptInput{
		Type: reviewReceiptType, Status: status, Surface: "ledger",
		OrganizationID: input.OrganizationID, WorkspaceID: input.WorkspaceID, ProjectID: input.ProjectID, TaskID: input.TaskID, JobID: input.JobID,
		ReviewID:       evidenceID("review", input.IdempotencyKey),
		ReviewerChecks: map[string]any{"reviewerRef": input.ReviewerRef, "reviewerVersion": input.ReviewerVersion, "inputArtifactDigests": input.InputArtifactDigests, "checks": input.Checks, "decision": input.Decision},
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return Review{}, err
	}
	return reviewFromReceipt(receipt), nil
}

func (s *MemoryStore) Review(_ context.Context, reviewID string) (Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, receipt := range s.receipts {
		if receipt.Type == reviewReceiptType && receipt.ReviewID == reviewID {
			return reviewFromReceipt(receipt), nil
		}
	}
	return Review{}, ErrReviewNotFound
}

func (s *MemoryStore) CreateReviewPolicy(_ context.Context, input ReviewPolicyInput) (ReviewPolicy, error) {
	if err := validateReviewPolicyInput(input); err != nil {
		return ReviewPolicy{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hashInput := input
	hashInput.IdempotencyKey = ""
	payloadHash, err := hashJSON(hashInput)
	if err != nil {
		return ReviewPolicy{}, err
	}
	if existing, ok := s.reviewPolicyIdempotency[input.IdempotencyKey]; ok {
		if existing.payloadHash != payloadHash {
			return ReviewPolicy{}, ErrIdempotencyConflict
		}
		policy, ok := existing.result.(ReviewPolicy)
		if !ok {
			return ReviewPolicy{}, ErrIdempotencyConflict
		}
		if current, ok := s.reviewPolicies[policy.PolicyID]; ok {
			policy = current
		}
		policy.Replayed = true
		return policy, nil
	}
	if input.SupersedesPolicyID != "" {
		previous, ok := s.reviewPolicies[input.SupersedesPolicyID]
		if !ok {
			return ReviewPolicy{}, ErrReviewPolicyNotFound
		}
		if previous.Status != "active" || !sameExecutionIdentity(previous.ExecutionIdentity, input.ExecutionIdentity) || previous.Version == input.Version {
			return ReviewPolicy{}, ErrInvalidReviewPolicyInput
		}
	} else {
		for _, existing := range s.reviewPolicies {
			if existing.Status == "active" && sameExecutionIdentity(existing.ExecutionIdentity, input.ExecutionIdentity) {
				return ReviewPolicy{}, ErrInvalidReviewPolicyInput
			}
		}
	}
	policy := ReviewPolicy{ReviewPolicyInput: input, PolicyID: evidenceID("review-policy", input.IdempotencyKey), Status: "active", CreatedAt: time.Now().UTC()}
	policy.IdempotencyKey = ""
	s.reviewPolicies[policy.PolicyID] = policy
	if input.SupersedesPolicyID != "" {
		previous := s.reviewPolicies[input.SupersedesPolicyID]
		previous.Status = "superseded"
		s.reviewPolicies[previous.PolicyID] = previous
	}
	s.reviewPolicyIdempotency[input.IdempotencyKey] = idempotencyRecord{payloadHash: payloadHash, result: policy}
	return policy, nil
}

func (s *MemoryStore) ReviewPolicy(_ context.Context, policyID string) (ReviewPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	policy, ok := s.reviewPolicies[policyID]
	if !ok {
		return ReviewPolicy{}, ErrReviewPolicyNotFound
	}
	return policy, nil
}

func (s *MemoryStore) ListReviewPolicies(_ context.Context, query ReviewPolicyQuery) ([]ReviewPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	policies := make([]ReviewPolicy, 0, len(s.reviewPolicies))
	for _, policy := range s.reviewPolicies {
		if (query.OrganizationID != "" && policy.OrganizationID != query.OrganizationID) || (query.WorkspaceID != "" && policy.WorkspaceID != query.WorkspaceID) || (query.ProjectID != "" && policy.ProjectID != query.ProjectID) || (query.TaskID != "" && policy.TaskID != query.TaskID) || (query.JobID != "" && policy.JobID != query.JobID) || (query.Status != "" && policy.Status != query.Status) {
			continue
		}
		policies = append(policies, policy)
	}
	sort.Slice(policies, func(i, j int) bool {
		if policies[i].CreatedAt.Equal(policies[j].CreatedAt) {
			return policies[i].PolicyID > policies[j].PolicyID
		}
		return policies[i].CreatedAt.After(policies[j].CreatedAt)
	})
	return policies, nil
}

func (s *MemoryStore) EvaluateReviewGate(_ context.Context, input ReviewGateInput) (ReviewGateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evaluateReviewGateLocked(input)
}

func (s *MemoryStore) evaluateReviewGateLocked(input ReviewGateInput) (ReviewGateResult, error) {
	if !validExecutionIdentity(input.ExecutionIdentity) {
		return ReviewGateResult{}, ErrInvalidReviewGateInput
	}
	var active ReviewPolicy
	for _, policy := range s.reviewPolicies {
		if policy.Status == "active" && sameExecutionIdentity(policy.ExecutionIdentity, input.ExecutionIdentity) {
			active = policy
			break
		}
	}
	if active.PolicyID == "" {
		return ReviewGateResult{}, ErrReviewPolicyNotFound
	}
	wanted := make(map[string]struct{}, len(input.ReviewIDs))
	for _, id := range input.ReviewIDs {
		wanted[id] = struct{}{}
	}
	reviews := make([]Review, 0, len(wanted))
	for _, receipt := range s.receipts {
		if _, ok := wanted[receipt.ReviewID]; ok && receipt.Type == reviewReceiptType {
			reviews = append(reviews, reviewFromReceipt(receipt))
		}
	}
	return evaluateReviewGate(active, reviews), nil
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
	if existing, ok := s.idempotency[input.IdempotencyKey]; ok {
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
	s.idempotency[input.IdempotencyKey] = idempotencyRecord{payloadHash: payloadHash, result: cloneMemoryValue(result)}
	return cloneMemoryValue(result), nil
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
