package ledger

import (
	"errors"
	"time"
)

var ErrIdempotencyConflict = errors.New("idempotency key already used with different payload")

type ManualTopUpInput struct {
	AccountID      string `json:"accountId"`
	AmountCents    int64  `json:"amountCents"`
	Currency       string `json:"currency"`
	OperatorUserID string `json:"operatorUserId"`
	IdempotencyKey string `json:"-"`
	Reason         string `json:"reason,omitempty"`
}

type Wallet struct {
	AccountID    string    `json:"accountId"`
	BalanceCents int64     `json:"balanceCents"`
	Currency     string    `json:"currency"`
	UpdatedAt    time.Time `json:"updatedAt"`
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
