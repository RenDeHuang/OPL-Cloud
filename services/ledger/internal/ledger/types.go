package ledger

import (
	"crypto/sha256"
	"encoding/hex"
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
var ErrInvalidReceiptInput = errors.New("invalid receipt input")
var ErrArtifactNotFound = errors.New("artifact not found")
var ErrInvalidArtifactInput = errors.New("invalid artifact input")
var ErrReviewNotFound = errors.New("review not found")
var ErrInvalidReviewInput = errors.New("invalid review input")

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

type Receipt struct {
	ReceiptInput
	ReceiptID string    `json:"receiptId"`
	CreatedAt time.Time `json:"createdAt"`
	Replayed  bool      `json:"replayed"`
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

func validateArtifactInput(input ArtifactInput) error {
	if input.WorkspaceID == "" || input.ProjectID == "" || input.TaskID == "" || input.JobID == "" || input.Digest == "" || input.MediaType == "" || input.SizeBytes < 0 || input.IdempotencyKey == "" || !isOpaqueReference(input.StorageRef) {
		return ErrInvalidArtifactInput
	}
	return nil
}

func validateReviewInput(input ReviewInput) error {
	if input.WorkspaceID == "" || input.ProjectID == "" || input.TaskID == "" || input.JobID == "" || input.ReviewerRef == "" || input.ReviewerVersion == "" || len(input.InputArtifactDigests) == 0 || len(input.Checks) == 0 || input.IdempotencyKey == "" || (input.Decision != "accepted" && input.Decision != "rejected") || containsForbiddenReceiptKey(input.Checks) {
		return ErrInvalidReviewInput
	}
	return nil
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
	allowedStatus := map[string]bool{"planned": true, "approved": true, "running": true, "completed": true, "failed": true, "cancelled": true, "review_required": true, "review_blocked": true}
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
