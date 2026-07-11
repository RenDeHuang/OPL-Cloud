package ledger

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var ErrIdempotencyConflict = errors.New("idempotency key already used with different payload")
var ErrInsufficientBalance = errors.New("insufficient available balance")
var ErrInsufficientFrozen = errors.New("insufficient frozen balance")
var ErrInvalidHoldInput = errors.New("hold resource identity required")
var ErrReceiptNotFound = errors.New("receipt not found")
var ErrContinuationNotFound = errors.New("continuation not found")
var ErrContinuationIneligible = errors.New("continuation is not eligible")
var ErrInvalidReceiptInput = errors.New("invalid receipt input")
var ErrInvalidReceiptQuery = errors.New("invalid receipt query")
var ErrArtifactNotFound = errors.New("artifact not found")
var ErrInvalidArtifactInput = errors.New("invalid artifact input")
var ErrReviewNotFound = errors.New("review not found")
var ErrInvalidReviewInput = errors.New("invalid review input")
var ErrReviewPolicyNotFound = errors.New("review policy not found")
var ErrInvalidReviewPolicyInput = errors.New("invalid review policy input")
var ErrInvalidReviewGateInput = errors.New("invalid review gate input")
var ErrInvalidReceiptRetentionInput = errors.New("invalid receipt retention input")
var ErrReceiptRetentionShortening = errors.New("receipt retention cannot be shortened")
var ErrReceiptRetentionActive = errors.New("receipt retention is active")
var ErrReceiptLegalHold = errors.New("receipt is under legal hold")

const artifactReceiptType = "artifact.manifest.v1"
const reviewReceiptType = "review.result.v1"

type ManualTopUpInput struct {
	AccountID      string `json:"accountId"`
	AmountCents    int64  `json:"amountCents"`
	Currency       string `json:"currency"`
	OperatorUserID string `json:"operatorUserId"`
	IdempotencyKey string `json:"-"`
	Reason         string `json:"reason,omitempty"`
}

type Wallet struct {
	AccountID       string    `json:"accountId"`
	BalanceCents    int64     `json:"balanceCents"`
	FrozenCents     int64     `json:"frozenCents"`
	AvailableCents  int64     `json:"availableCents"`
	TotalSpentCents int64     `json:"totalSpentCents"`
	Currency        string    `json:"currency"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type LedgerEntry struct {
	ID             string    `json:"id"`
	AccountID      string    `json:"accountId"`
	AmountCents    int64     `json:"amountCents"`
	Currency       string    `json:"currency"`
	Direction      string    `json:"direction"`
	Source         string    `json:"source"`
	OperatorUserID string    `json:"operatorUserId"`
	Reason         string    `json:"reason,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

type WalletTransaction struct {
	ID              string    `json:"id"`
	AccountID       string    `json:"accountId"`
	LedgerEntryID   string    `json:"ledgerEntryId"`
	AmountCents     int64     `json:"amountCents"`
	BalanceCents    int64     `json:"balanceCents"`
	FrozenCents     int64     `json:"frozenCents"`
	AvailableCents  int64     `json:"availableCents"`
	TotalSpentCents int64     `json:"totalSpentCents"`
	Currency        string    `json:"currency"`
	CreatedAt       time.Time `json:"createdAt"`
}

type ManualTopUp struct {
	ID             string    `json:"id"`
	AccountID      string    `json:"accountId"`
	AmountCents    int64     `json:"amountCents"`
	Currency       string    `json:"currency"`
	OperatorUserID string    `json:"operatorUserId"`
	LedgerEntryID  string    `json:"ledgerEntryId"`
	Reason         string    `json:"reason,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

type ManualTopUpResult struct {
	TopUp             ManualTopUp       `json:"topUp"`
	LedgerEntry       LedgerEntry       `json:"ledgerEntry"`
	WalletTransaction WalletTransaction `json:"walletTransaction"`
	Wallet            Wallet            `json:"wallet"`
	Replayed          bool              `json:"replayed"`
}

type HoldInput struct {
	AccountID      string `json:"accountId"`
	WorkspaceID    string `json:"workspaceId"`
	ResourceType   string `json:"resourceType"`
	ResourceID     string `json:"resourceId"`
	AmountCents    int64  `json:"amountCents"`
	Currency       string `json:"currency"`
	IdempotencyKey string `json:"-"`
}

type HoldResult struct {
	ID                  string    `json:"id"`
	AccountID           string    `json:"accountId"`
	WorkspaceID         string    `json:"workspaceId"`
	ResourceType        string    `json:"resourceType"`
	ResourceID          string    `json:"resourceId"`
	AmountCents         int64     `json:"amountCents"`
	Currency            string    `json:"currency"`
	Status              string    `json:"status"`
	LedgerEntryID       string    `json:"ledgerEntryId"`
	WalletTransactionID string    `json:"walletTransactionId"`
	Wallet              Wallet    `json:"wallet"`
	CreatedAt           time.Time `json:"createdAt"`
	Replayed            bool      `json:"replayed"`
}

type HoldReleaseInput struct {
	AccountID      string `json:"accountId"`
	WorkspaceID    string `json:"workspaceId"`
	ResourceType   string `json:"resourceType"`
	ResourceID     string `json:"resourceId"`
	HoldID         string `json:"holdId"`
	AmountCents    int64  `json:"amountCents"`
	Currency       string `json:"currency"`
	Reason         string `json:"reason,omitempty"`
	IdempotencyKey string `json:"-"`
}

type HoldReleaseResult struct {
	ID                  string    `json:"id"`
	AccountID           string    `json:"accountId"`
	WorkspaceID         string    `json:"workspaceId"`
	ResourceType        string    `json:"resourceType"`
	ResourceID          string    `json:"resourceId"`
	HoldID              string    `json:"holdId"`
	AmountCents         int64     `json:"amountCents"`
	Currency            string    `json:"currency"`
	Status              string    `json:"status"`
	LedgerEntryID       string    `json:"ledgerEntryId"`
	WalletTransactionID string    `json:"walletTransactionId"`
	Wallet              Wallet    `json:"wallet"`
	CreatedAt           time.Time `json:"createdAt"`
	Replayed            bool      `json:"replayed"`
}

type ReceiptInput struct {
	Type                string         `json:"type"`
	Status              string         `json:"status"`
	Surface             string         `json:"surface"`
	OrganizationID      string         `json:"organizationId"`
	WorkspaceID         string         `json:"workspaceId"`
	ProjectID           string         `json:"projectId"`
	TaskID              string         `json:"taskId"`
	RequestID           string         `json:"requestId"`
	ApprovalID          string         `json:"approvalId"`
	JobID               string         `json:"jobId"`
	ArtifactID          string         `json:"artifactId"`
	ReviewID            string         `json:"reviewId"`
	ContinuationID      string         `json:"continuationId"`
	Actor               map[string]any `json:"actor"`
	Plan                map[string]any `json:"plan"`
	Execution           map[string]any `json:"execution"`
	Environment         map[string]any `json:"environment"`
	InputRefs           map[string]any `json:"inputRefs"`
	OutputRefs          map[string]any `json:"outputRefs"`
	ReviewerChecks      map[string]any `json:"reviewerChecks"`
	Cost                map[string]any `json:"cost"`
	Owner               map[string]any `json:"owner"`
	Continuation        map[string]any `json:"continuation"`
	SupersedesReceiptID string         `json:"supersedesReceiptId"`
	IdempotencyKey      string         `json:"-"`
}

type ReceiptRetention struct {
	RetainUntil      time.Time                 `json:"retainUntil,omitempty"`
	LegalHold        bool                      `json:"legalHold"`
	PrivacyRedaction *PrivacyRedactionEvidence `json:"privacyRedaction,omitempty"`
}

type PrivacyRedactionEvidence struct {
	AppliedAt time.Time `json:"appliedAt"`
	Reason    string    `json:"reason"`
	Eligible  bool      `json:"eligible"`
}

type ReceiptRetentionInput struct {
	ReceiptID      string    `json:"-"`
	RetainUntil    time.Time `json:"retainUntil,omitempty"`
	LegalHold      bool      `json:"legalHold"`
	IdempotencyKey string    `json:"-"`
}

type ReceiptPrivacyDeleteInput struct {
	ReceiptID      string `json:"-"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"-"`
}

type ReceiptRetentionResult struct {
	ReceiptID string           `json:"receiptId"`
	Retention ReceiptRetention `json:"retention"`
	Replayed  bool             `json:"replayed"`
}

type Receipt struct {
	ReceiptInput
	ReceiptID string           `json:"receiptId"`
	CreatedAt time.Time        `json:"createdAt"`
	Retention ReceiptRetention `json:"retention"`
	Replayed  bool             `json:"replayed"`
}

const (
	DefaultReceiptPageSize = 50
	MaxReceiptPageSize     = 100
)

type ReceiptQuery struct {
	OrganizationID string
	WorkspaceID    string
	ProjectID      string
	TaskID         string
	JobID          string
	Type           string
	Status         string
	Cursor         string
	Limit          int
}

type ReceiptPage struct {
	Receipts   []Receipt `json:"receipts"`
	NextCursor string    `json:"nextCursor"`
	HasMore    bool      `json:"hasMore"`
}

type receiptCursor struct {
	CreatedAt time.Time `json:"createdAt"`
	ReceiptID string    `json:"receiptId"`
}

func normalizeReceiptQuery(query ReceiptQuery) (ReceiptQuery, receiptCursor, error) {
	if query.Limit == 0 {
		query.Limit = DefaultReceiptPageSize
	}
	if query.Limit < 1 || query.Limit > MaxReceiptPageSize {
		return ReceiptQuery{}, receiptCursor{}, ErrInvalidReceiptQuery
	}
	if query.Cursor == "" {
		return query, receiptCursor{}, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(query.Cursor)
	if err != nil {
		return ReceiptQuery{}, receiptCursor{}, ErrInvalidReceiptQuery
	}
	var cursor receiptCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.CreatedAt.IsZero() || cursor.ReceiptID == "" {
		return ReceiptQuery{}, receiptCursor{}, ErrInvalidReceiptQuery
	}
	return query, cursor, nil
}

func encodeReceiptCursor(receipt Receipt) string {
	payload, _ := json.Marshal(receiptCursor{CreatedAt: receipt.CreatedAt, ReceiptID: receipt.ReceiptID})
	return base64.RawURLEncoding.EncodeToString(payload)
}

type ArtifactInput struct {
	OrganizationID string `json:"organizationId"`
	WorkspaceID    string `json:"workspaceId"`
	ProjectID      string `json:"projectId"`
	TaskID         string `json:"taskId"`
	JobID          string `json:"jobId"`
	Digest         string `json:"digest"`
	MediaType      string `json:"mediaType"`
	SizeBytes      int64  `json:"sizeBytes"`
	StorageRef     string `json:"storageRef"`
	IdempotencyKey string `json:"-"`
}

type Artifact struct {
	ArtifactInput
	ArtifactID string    `json:"artifactId"`
	ReceiptID  string    `json:"receiptId"`
	CreatedAt  time.Time `json:"createdAt"`
	Replayed   bool      `json:"replayed"`
}

type ReviewInput struct {
	OrganizationID       string         `json:"organizationId"`
	WorkspaceID          string         `json:"workspaceId"`
	ProjectID            string         `json:"projectId"`
	TaskID               string         `json:"taskId"`
	JobID                string         `json:"jobId"`
	ReviewerRef          string         `json:"reviewerRef"`
	ReviewerVersion      string         `json:"reviewerVersion"`
	InputArtifactDigests []string       `json:"inputArtifactDigests"`
	Checks               map[string]any `json:"checks"`
	Decision             string         `json:"decision"`
	IdempotencyKey       string         `json:"-"`
}

type Review struct {
	ReviewInput
	ReviewID  string    `json:"reviewId"`
	ReceiptID string    `json:"receiptId"`
	CreatedAt time.Time `json:"createdAt"`
	Replayed  bool      `json:"replayed"`
}

type ExecutionIdentity struct {
	OrganizationID string `json:"organizationId"`
	WorkspaceID    string `json:"workspaceId"`
	ProjectID      string `json:"projectId"`
	TaskID         string `json:"taskId"`
	JobID          string `json:"jobId"`
}

type RequiredReviewer struct {
	ReviewerRef     string `json:"reviewerRef"`
	ReviewerVersion string `json:"reviewerVersion"`
}

type ReviewPolicyInput struct {
	ExecutionIdentity
	Version            string             `json:"version"`
	RequiredReviewers  []RequiredReviewer `json:"requiredReviewers"`
	SupersedesPolicyID string             `json:"supersedesPolicyId,omitempty"`
	IdempotencyKey     string             `json:"-"`
}

type ReviewPolicy struct {
	ReviewPolicyInput
	PolicyID  string    `json:"policyId"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	Replayed  bool      `json:"replayed"`
}

type ReviewPolicyQuery struct {
	ExecutionIdentity
	Status string
}

type ReviewGateInput struct {
	ExecutionIdentity
	ReviewIDs []string `json:"reviewIds"`
}

type ReviewGateEvidence struct {
	ReviewerRef     string `json:"reviewerRef"`
	RequiredVersion string `json:"requiredVersion"`
	ReviewID        string `json:"reviewId,omitempty"`
	ActualVersion   string `json:"actualVersion,omitempty"`
}

type ReviewGateResult struct {
	PolicyID             string               `json:"policyId"`
	PolicyVersion        string               `json:"policyVersion"`
	Status               string               `json:"status"`
	ContinuationEligible bool                 `json:"continuationEligible"`
	Missing              []ReviewGateEvidence `json:"missing"`
	Pending              []ReviewGateEvidence `json:"pending"`
	Rejected             []ReviewGateEvidence `json:"rejected"`
	VersionMismatches    []ReviewGateEvidence `json:"versionMismatches"`
}

func validateArtifactInput(input ArtifactInput) error {
	if input.OrganizationID == "" || input.WorkspaceID == "" || input.ProjectID == "" || input.TaskID == "" || input.JobID == "" || input.Digest == "" || input.MediaType == "" || input.SizeBytes < 0 || input.IdempotencyKey == "" || !isOpaqueReference(input.StorageRef) {
		return ErrInvalidArtifactInput
	}
	return nil
}

func validateReviewInput(input ReviewInput) error {
	if input.OrganizationID == "" || input.WorkspaceID == "" || input.ProjectID == "" || input.TaskID == "" || input.JobID == "" || !isOpaqueReference(input.ReviewerRef) || !isOpaqueReference(input.ReviewerVersion) || len(input.InputArtifactDigests) == 0 || len(input.Checks) == 0 || input.IdempotencyKey == "" || (input.Decision != "accepted" && input.Decision != "pending" && input.Decision != "rejected") || containsForbiddenReceiptKey(input.Checks) {
		return ErrInvalidReviewInput
	}
	return nil
}

func reviewReceiptStatus(decision string) string {
	if decision == "rejected" {
		return "review_blocked"
	}
	if decision == "pending" {
		return "review_required"
	}
	return "completed"
}

func validateReviewPolicyInput(input ReviewPolicyInput) error {
	if !validExecutionIdentity(input.ExecutionIdentity) || !isOpaqueReference(input.Version) || input.IdempotencyKey == "" || len(input.RequiredReviewers) == 0 || (input.SupersedesPolicyID != "" && !isOpaqueReference(input.SupersedesPolicyID)) {
		return ErrInvalidReviewPolicyInput
	}
	seen := make(map[string]struct{}, len(input.RequiredReviewers))
	for _, required := range input.RequiredReviewers {
		if !isOpaqueReference(required.ReviewerRef) || !isOpaqueReference(required.ReviewerVersion) {
			return ErrInvalidReviewPolicyInput
		}
		if _, exists := seen[required.ReviewerRef]; exists {
			return ErrInvalidReviewPolicyInput
		}
		seen[required.ReviewerRef] = struct{}{}
	}
	return nil
}

func validExecutionIdentity(identity ExecutionIdentity) bool {
	return identity.OrganizationID != "" && identity.WorkspaceID != "" && identity.ProjectID != "" && identity.TaskID != "" && identity.JobID != ""
}

func sameExecutionIdentity(left, right ExecutionIdentity) bool {
	return left == right
}

func evaluateReviewGate(policy ReviewPolicy, reviews []Review) ReviewGateResult {
	result := ReviewGateResult{
		PolicyID: policy.PolicyID, PolicyVersion: policy.Version, Status: "accepted", ContinuationEligible: true,
		Missing: []ReviewGateEvidence{}, Pending: []ReviewGateEvidence{}, Rejected: []ReviewGateEvidence{}, VersionMismatches: []ReviewGateEvidence{},
	}
	for _, required := range policy.RequiredReviewers {
		var mismatch *Review
		var pending *Review
		var rejected *Review
		accepted := false
		for i := range reviews {
			review := &reviews[i]
			if review.ReviewerRef != required.ReviewerRef || !sameExecutionIdentity(policy.ExecutionIdentity, executionIdentityFromReview(*review)) {
				continue
			}
			if review.ReviewerVersion != required.ReviewerVersion {
				if mismatch == nil {
					mismatch = review
				}
				continue
			}
			switch review.Decision {
			case "accepted":
				accepted = true
			case "pending":
				pending = review
			case "rejected":
				rejected = review
			}
		}
		evidence := ReviewGateEvidence{ReviewerRef: required.ReviewerRef, RequiredVersion: required.ReviewerVersion}
		if rejected != nil {
			evidence.ReviewID = rejected.ReviewID
			evidence.ActualVersion = rejected.ReviewerVersion
			result.Rejected = append(result.Rejected, evidence)
			continue
		}
		if accepted {
			continue
		}
		if pending != nil {
			evidence.ReviewID = pending.ReviewID
			evidence.ActualVersion = pending.ReviewerVersion
			result.Pending = append(result.Pending, evidence)
			continue
		}
		if mismatch != nil {
			evidence.ReviewID = mismatch.ReviewID
			evidence.ActualVersion = mismatch.ReviewerVersion
			result.VersionMismatches = append(result.VersionMismatches, evidence)
			continue
		}
		result.Missing = append(result.Missing, evidence)
	}
	if len(result.Rejected) > 0 || len(result.VersionMismatches) > 0 {
		result.Status = "review_blocked"
		result.ContinuationEligible = false
	} else if len(result.Missing) > 0 || len(result.Pending) > 0 {
		result.Status = "review_required"
		result.ContinuationEligible = false
	}
	return result
}

func executionIdentityFromReview(review Review) ExecutionIdentity {
	return ExecutionIdentity{OrganizationID: review.OrganizationID, WorkspaceID: review.WorkspaceID, ProjectID: review.ProjectID, TaskID: review.TaskID, JobID: review.JobID}
}

func executionIdentityFromReceipt(receipt Receipt) ExecutionIdentity {
	return ExecutionIdentity{OrganizationID: receipt.OrganizationID, WorkspaceID: receipt.WorkspaceID, ProjectID: receipt.ProjectID, TaskID: receipt.TaskID, JobID: receipt.JobID}
}

func receiptForRead(receipt Receipt, gate ReviewGateResult, gateErr error) Receipt {
	if receipt.Continuation == nil && receipt.ContinuationID == "" {
		return receipt
	}
	if validExecutionIdentity(executionIdentityFromReceipt(receipt)) && gateErr == nil && gate.ContinuationEligible {
		return receipt
	}
	receipt.ContinuationID = ""
	receipt.Continuation = nil
	return receipt
}

func isOpaqueReference(value string) bool {
	if value == "" || len(value) > 255 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return true
}

func evidenceID(prefix, idempotencyKey string) string {
	digest := sha256.Sum256([]byte(idempotencyKey))
	return prefix + "-" + hex.EncodeToString(digest[:8])
}

func artifactFromReceipt(receipt Receipt) Artifact {
	return Artifact{
		ArtifactInput: ArtifactInput{
			OrganizationID: receipt.OrganizationID,
			WorkspaceID:    receipt.WorkspaceID,
			ProjectID:      receipt.ProjectID,
			TaskID:         receipt.TaskID,
			JobID:          receipt.JobID,
			Digest:         stringValue(receipt.OutputRefs["digest"]),
			MediaType:      stringValue(receipt.OutputRefs["mediaType"]),
			SizeBytes:      int64Value(receipt.OutputRefs["sizeBytes"]),
			StorageRef:     stringValue(receipt.OutputRefs["storageRef"]),
		},
		ArtifactID: receipt.ArtifactID,
		ReceiptID:  receipt.ReceiptID,
		CreatedAt:  receipt.CreatedAt,
		Replayed:   receipt.Replayed,
	}
}

func reviewFromReceipt(receipt Receipt) Review {
	checks, _ := receipt.ReviewerChecks["checks"].(map[string]any)
	return Review{
		ReviewInput: ReviewInput{
			OrganizationID:       receipt.OrganizationID,
			WorkspaceID:          receipt.WorkspaceID,
			ProjectID:            receipt.ProjectID,
			TaskID:               receipt.TaskID,
			JobID:                receipt.JobID,
			ReviewerRef:          stringValue(receipt.ReviewerChecks["reviewerRef"]),
			ReviewerVersion:      stringValue(receipt.ReviewerChecks["reviewerVersion"]),
			InputArtifactDigests: stringSlice(receipt.ReviewerChecks["inputArtifactDigests"]),
			Checks:               checks,
			Decision:             stringValue(receipt.ReviewerChecks["decision"]),
		},
		ReviewID:  receipt.ReviewID,
		ReceiptID: receipt.ReceiptID,
		CreatedAt: receipt.CreatedAt,
		Replayed:  receipt.Replayed,
	}
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func int64Value(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func finalizeReceiptContinuation(receipt *Receipt) {
	if receipt.ContinuationID == "" && len(receipt.Continuation) == 0 {
		return
	}
	continuation := make(map[string]any, len(receipt.Continuation)+1)
	for key, value := range receipt.Continuation {
		continuation[key] = value
	}
	continuationID, _ := continuation["continuationId"].(string)
	if continuationID == "" {
		continuationID = receipt.ContinuationID
	}
	if continuationID == "" {
		continuationID = "continuation-" + receipt.ReceiptID
	}
	receipt.ContinuationID = continuationID
	continuation["continuationId"] = continuationID
	receipt.Continuation = continuation
}

func continuationFromReceipt(receipt Receipt) (map[string]any, error) {
	continuation := make(map[string]any, len(receipt.Continuation)+4)
	for key, value := range receipt.Continuation {
		continuation[key] = value
	}
	continuationID, _ := continuation["continuationId"].(string)
	if continuationID == "" {
		continuationID = receipt.ContinuationID
	}
	if continuationID == "" {
		return nil, ErrContinuationNotFound
	}
	continuation["continuationId"] = continuationID
	continuation["receiptId"] = receipt.ReceiptID
	continuation["projectId"] = receipt.ProjectID
	continuation["taskId"] = receipt.TaskID
	return continuation, nil
}

func validateReceiptInput(input ReceiptInput) error {
	if input.Type == "" || input.Status == "" || input.Surface == "" || input.WorkspaceID == "" || input.IdempotencyKey == "" {
		return ErrInvalidReceiptInput
	}
	if (input.ContinuationID != "" || len(input.Continuation) > 0) && !validExecutionIdentity(executionIdentityFromReceipt(Receipt{ReceiptInput: input})) {
		return ErrInvalidReceiptInput
	}
	allowedStatus := map[string]bool{"planned": true, "approved": true, "running": true, "completed": true, "failed": true, "timed_out": true, "cancelled": true, "review_required": true, "review_blocked": true}
	if !allowedStatus[input.Status] || containsForbiddenReceiptKey(input) {
		return ErrInvalidReceiptInput
	}
	return nil
}

func containsForbiddenReceiptKey(value any) bool {
	forbidden := map[string]bool{"rawcredential": true, "credential": true, "password": true, "token": true, "secret": true, "signedurl": true, "presignedurl": true, "objectkey": true, "kubeconfig": true}
	switch typed := value.(type) {
	case ReceiptInput:
		return containsForbiddenReceiptKey(map[string]any{"actor": typed.Actor, "plan": typed.Plan, "execution": typed.Execution, "environment": typed.Environment, "inputRefs": typed.InputRefs, "outputRefs": typed.OutputRefs, "reviewerChecks": typed.ReviewerChecks, "cost": typed.Cost, "owner": typed.Owner, "continuation": typed.Continuation})
	case map[string]any:
		for key, child := range typed {
			if forbidden[strings.ToLower(key)] || containsForbiddenReceiptKey(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsForbiddenReceiptKey(child) {
				return true
			}
		}
	}
	return false
}

type ResourceSettlementInput struct {
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
	IdempotencyKey          string         `json:"-"`
}

type ResourceSettlementResult struct {
	ID                      string         `json:"id"`
	AccountID               string         `json:"accountId"`
	WorkspaceID             string         `json:"workspaceId"`
	ResourceType            string         `json:"resourceType"`
	ResourceID              string         `json:"resourceId"`
	AmountCents             int64          `json:"amountCents"`
	Currency                string         `json:"currency"`
	Status                  string         `json:"status"`
	LedgerEntryID           string         `json:"ledgerEntryId"`
	WalletTransactionID     string         `json:"walletTransactionId"`
	PricingVersion          string         `json:"pricingVersion"`
	PriceSnapshot           map[string]any `json:"priceSnapshot"`
	UsagePeriodStart        string         `json:"usagePeriodStart"`
	UsagePeriodEnd          string         `json:"usagePeriodEnd"`
	Quantity                float64        `json:"quantity"`
	Unit                    string         `json:"unit"`
	ProviderCostEvidenceRef string         `json:"providerCostEvidenceRef"`
	Wallet                  Wallet         `json:"wallet"`
	CreatedAt               time.Time      `json:"createdAt"`
	Replayed                bool           `json:"replayed"`
}

type ReconciliationInput struct {
	Report         map[string]any `json:"report"`
	IdempotencyKey string         `json:"-"`
}

type ReconciliationResult struct {
	ID                 string         `json:"id"`
	Status             string         `json:"status"`
	Report             map[string]any `json:"report"`
	BlockNewWorkspaces bool           `json:"blockNewWorkspaces"`
	Reason             string         `json:"reason"`
	CreatedAt          time.Time      `json:"createdAt"`
	Replayed           bool           `json:"replayed"`
}
