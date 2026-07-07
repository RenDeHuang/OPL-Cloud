package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

type LedgerClient interface {
	ManualTopUp(ctx context.Context, input ManualTopUpInput, idempotencyKey string) (ManualTopUpResult, error)
	CreateHold(ctx context.Context, input HoldInput, idempotencyKey string) (HoldResult, error)
	RecordEvidence(ctx context.Context, input EvidenceInput, idempotencyKey string) (EvidenceReceipt, error)
	SettleResource(ctx context.Context, input ResourceSettlementInput, idempotencyKey string) (ResourceSettlementResult, error)
	RecordReconciliation(ctx context.Context, input ReconciliationInput, idempotencyKey string) (ReconciliationResult, error)
}

type ManualTopUpInput struct {
	AccountID      string `json:"accountId"`
	AmountCents    int64  `json:"amountCents"`
	Currency       string `json:"currency"`
	OperatorUserID string `json:"operatorUserId"`
	Reason         string `json:"reason,omitempty"`
}

type ManualTopUpResult struct {
	TopUp             ManualTopUp       `json:"topUp"`
	LedgerEntry       LedgerEntry       `json:"ledgerEntry"`
	WalletTransaction WalletTransaction `json:"walletTransaction"`
	Wallet            Wallet            `json:"wallet"`
	Replayed          bool              `json:"replayed"`
}

type ManualTopUp struct {
	ID             string `json:"id"`
	AccountID      string `json:"accountId"`
	AmountCents    int64  `json:"amountCents"`
	Currency       string `json:"currency"`
	OperatorUserID string `json:"operatorUserId"`
	LedgerEntryID  string `json:"ledgerEntryId"`
	Reason         string `json:"reason,omitempty"`
}

type LedgerEntry struct {
	ID          string `json:"id"`
	AccountID   string `json:"accountId"`
	AmountCents int64  `json:"amountCents"`
	Currency    string `json:"currency"`
	Direction   string `json:"direction"`
	Source      string `json:"source"`
}

type WalletTransaction struct {
	ID            string `json:"id"`
	AccountID     string `json:"accountId"`
	LedgerEntryID string `json:"ledgerEntryId"`
	AmountCents   int64  `json:"amountCents"`
	BalanceCents  int64  `json:"balanceCents"`
	Currency      string `json:"currency"`
}

type Wallet struct {
	AccountID       string `json:"accountId"`
	BalanceCents    int64  `json:"balanceCents"`
	FrozenCents     int64  `json:"frozenCents"`
	AvailableCents  int64  `json:"availableCents"`
	TotalSpentCents int64  `json:"totalSpentCents"`
	Currency        string `json:"currency"`
}

type ResourceSettlementInput struct {
	AccountID    string `json:"accountId"`
	WorkspaceID  string `json:"workspaceId"`
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId"`
	AmountCents  int64  `json:"amountCents"`
	Currency     string `json:"currency"`
}

type ResourceSettlementResult struct {
	ID                  string `json:"id"`
	AccountID           string `json:"accountId"`
	WorkspaceID         string `json:"workspaceId"`
	ResourceType        string `json:"resourceType"`
	ResourceID          string `json:"resourceId"`
	AmountCents         int64  `json:"amountCents"`
	Currency            string `json:"currency"`
	Status              string `json:"status"`
	LedgerEntryID       string `json:"ledgerEntryId"`
	WalletTransactionID string `json:"walletTransactionId"`
	Wallet              Wallet `json:"wallet"`
	Replayed            bool   `json:"replayed"`
}

type ReconciliationInput struct {
	Report map[string]any `json:"report"`
}

type ReconciliationResult struct {
	ID                 string         `json:"id"`
	Status             string         `json:"status"`
	Report             map[string]any `json:"report"`
	BlockNewWorkspaces bool           `json:"blockNewWorkspaces"`
	Reason             string         `json:"reason"`
	Replayed           bool           `json:"replayed"`
}

type HoldInput struct {
	AccountID   string `json:"accountId"`
	WorkspaceID string `json:"workspaceId"`
	AmountCents int64  `json:"amountCents"`
	Currency    string `json:"currency"`
}

type HoldResult struct {
	ID          string `json:"id"`
	AccountID   string `json:"accountId"`
	WorkspaceID string `json:"workspaceId"`
	AmountCents int64  `json:"amountCents"`
	Currency    string `json:"currency"`
	Status      string `json:"status"`
}

type EvidenceInput struct {
	WorkspaceID       string `json:"workspaceId"`
	ProviderRequestID string `json:"providerRequestId"`
	RedactedURL       string `json:"redactedUrl"`
	TokenVersion      string `json:"tokenVersion"`
}

type EvidenceReceipt struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspaceId"`
}

type ledgerHTTPClient struct {
	baseURL string
	client  *http.Client
}

func NewLedgerHTTPClient(baseURL string, client *http.Client) LedgerClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &ledgerHTTPClient{baseURL: baseURL, client: client}
}

func (c *ledgerHTTPClient) ManualTopUp(ctx context.Context, input ManualTopUpInput, idempotencyKey string) (ManualTopUpResult, error) {
	var result ManualTopUpResult
	err := c.post(ctx, "/ledger/topups", input, idempotencyKey, &result)
	return result, err
}

func (c *ledgerHTTPClient) CreateHold(ctx context.Context, input HoldInput, idempotencyKey string) (HoldResult, error) {
	var result HoldResult
	err := c.post(ctx, "/ledger/holds", input, idempotencyKey, &result)
	return result, err
}

func (c *ledgerHTTPClient) RecordEvidence(ctx context.Context, input EvidenceInput, idempotencyKey string) (EvidenceReceipt, error) {
	var result EvidenceReceipt
	err := c.post(ctx, "/ledger/evidence", input, idempotencyKey, &result)
	return result, err
}

func (c *ledgerHTTPClient) SettleResource(ctx context.Context, input ResourceSettlementInput, idempotencyKey string) (ResourceSettlementResult, error) {
	var result ResourceSettlementResult
	err := c.post(ctx, "/ledger/resource-settlements", input, idempotencyKey, &result)
	return result, err
}

func (c *ledgerHTTPClient) RecordReconciliation(ctx context.Context, input ReconciliationInput, idempotencyKey string) (ReconciliationResult, error) {
	var result ReconciliationResult
	err := c.post(ctx, "/ledger/reconciliation", input, idempotencyKey, &result)
	return result, err
}

func (c *ledgerHTTPClient) post(ctx context.Context, path string, input any, idempotencyKey string, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return json.NewDecoder(res.Body).Decode(output)
}
