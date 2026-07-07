package ledger

import (
	"errors"
	"time"
)

var ErrIdempotencyConflict = errors.New("idempotency key already used with different payload")
var ErrInsufficientBalance = errors.New("insufficient available balance")

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
	ID            string    `json:"id"`
	AccountID     string    `json:"accountId"`
	LedgerEntryID string    `json:"ledgerEntryId"`
	AmountCents   int64     `json:"amountCents"`
	BalanceCents  int64     `json:"balanceCents"`
	Currency      string    `json:"currency"`
	CreatedAt     time.Time `json:"createdAt"`
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
	AmountCents    int64  `json:"amountCents"`
	Currency       string `json:"currency"`
	IdempotencyKey string `json:"-"`
}

type HoldResult struct {
	ID                  string    `json:"id"`
	AccountID           string    `json:"accountId"`
	WorkspaceID         string    `json:"workspaceId"`
	AmountCents         int64     `json:"amountCents"`
	Currency            string    `json:"currency"`
	Status              string    `json:"status"`
	LedgerEntryID       string    `json:"ledgerEntryId"`
	WalletTransactionID string    `json:"walletTransactionId"`
	Wallet              Wallet    `json:"wallet"`
	CreatedAt           time.Time `json:"createdAt"`
	Replayed            bool      `json:"replayed"`
}

type EvidenceInput struct {
	WorkspaceID       string `json:"workspaceId"`
	ProviderRequestID string `json:"providerRequestId"`
	RedactedURL       string `json:"redactedUrl"`
	TokenVersion      string `json:"tokenVersion"`
	IdempotencyKey    string `json:"-"`
}

type EvidenceReceipt struct {
	ID                string    `json:"id"`
	WorkspaceID       string    `json:"workspaceId"`
	ProviderRequestID string    `json:"providerRequestId"`
	RedactedURL       string    `json:"redactedUrl"`
	TokenVersion      string    `json:"tokenVersion"`
	CreatedAt         time.Time `json:"createdAt"`
	Replayed          bool      `json:"replayed"`
}

type ResourceSettlementInput struct {
	AccountID      string `json:"accountId"`
	WorkspaceID    string `json:"workspaceId"`
	ResourceType   string `json:"resourceType"`
	ResourceID     string `json:"resourceId"`
	AmountCents    int64  `json:"amountCents"`
	Currency       string `json:"currency"`
	IdempotencyKey string `json:"-"`
}

type ResourceSettlementResult struct {
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
