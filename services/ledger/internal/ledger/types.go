package ledger

import (
	"errors"
	"strings"
	"time"
)

var ErrIdempotencyConflict = errors.New("idempotency key already used with different payload")
var ErrInsufficientBalance = errors.New("insufficient available balance")
var ErrInsufficientFrozen = errors.New("insufficient frozen balance")
var ErrInvalidHoldInput = errors.New("hold resource identity required")
var ErrReceiptNotFound = errors.New("receipt not found")
var ErrInvalidReceiptInput = errors.New("invalid receipt input")

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
