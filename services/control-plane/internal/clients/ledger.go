package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type LedgerClient interface {
	ManualTopUp(ctx context.Context, input ManualTopUpInput, idempotencyKey string) (ManualTopUpResult, error)
	CreateHold(ctx context.Context, input HoldInput, idempotencyKey string) (HoldResult, error)
	ActivateHold(ctx context.Context, input HoldActivationInput, idempotencyKey string) (HoldActivationResult, error)
	ReleaseHold(ctx context.Context, input HoldReleaseInput, idempotencyKey string) (HoldReleaseResult, error)
	RecordReceipt(ctx context.Context, input ReceiptInput, idempotencyKey string) (Receipt, error)
	Receipt(ctx context.Context, receiptID string) (Receipt, error)
	Artifact(ctx context.Context, artifactID string) (Artifact, error)
	Review(ctx context.Context, reviewID string) (Review, error)
	Continuation(ctx context.Context, receiptID string) (map[string]any, error)
	SettleResource(ctx context.Context, input ResourceSettlementInput, idempotencyKey string) (ResourceSettlementResult, error)
	RecordReconciliation(ctx context.Context, input ReconciliationInput, idempotencyKey string) (ReconciliationResult, error)
	Wallet(ctx context.Context, accountID string) (Wallet, error)
	ListLedgerEntries(ctx context.Context, accountID string) ([]LedgerEntry, error)
	ListWalletTransactions(ctx context.Context, accountID string) ([]WalletTransaction, error)
	ListManualTopUps(ctx context.Context, accountID string) ([]ManualTopUp, error)
	ListResourceSettlements(ctx context.Context, accountID string) ([]ResourceSettlementResult, error)
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
	ID             string `json:"id"`
	AccountID      string `json:"accountId"`
	AmountCents    int64  `json:"amountCents"`
	Currency       string `json:"currency"`
	Direction      string `json:"direction"`
	Source         string `json:"source"`
	OperatorUserID string `json:"operatorUserId"`
	Reason         string `json:"reason,omitempty"`
	CreatedAt      string `json:"createdAt"`
}

type WalletTransaction struct {
	ID              string `json:"id"`
	AccountID       string `json:"accountId"`
	LedgerEntryID   string `json:"ledgerEntryId"`
	AmountCents     int64  `json:"amountCents"`
	BalanceCents    int64  `json:"balanceCents"`
	FrozenCents     int64  `json:"frozenCents"`
	AvailableCents  int64  `json:"availableCents"`
	TotalSpentCents int64  `json:"totalSpentCents"`
	Currency        string `json:"currency"`
	CreatedAt       string `json:"createdAt"`
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
	AccountID               string         `json:"accountId"`
	WorkspaceID             string         `json:"workspaceId"`
	ResourceType            string         `json:"resourceType"`
	ResourceID              string         `json:"resourceId"`
	HoldID                  string         `json:"holdId"`
	AmountCents             int64          `json:"amountCents"`
	Currency                string         `json:"currency"`
	PricingVersion          string         `json:"pricingVersion,omitempty"`
	PriceSnapshot           map[string]any `json:"priceSnapshot,omitempty"`
	UsagePeriodStart        string         `json:"usagePeriodStart,omitempty"`
	UsagePeriodEnd          string         `json:"usagePeriodEnd,omitempty"`
	Quantity                float64        `json:"quantity,omitempty"`
	Unit                    string         `json:"unit,omitempty"`
	ProviderCostEvidenceRef string         `json:"providerCostEvidenceRef,omitempty"`
}

type ResourceSettlementResult struct {
	ID                      string         `json:"id"`
	AccountID               string         `json:"accountId"`
	WorkspaceID             string         `json:"workspaceId"`
	ResourceType            string         `json:"resourceType"`
	ResourceID              string         `json:"resourceId"`
	HoldID                  string         `json:"holdId"`
	HoldRemainingCents      int64          `json:"holdRemainingCents"`
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
	CreatedAt               string         `json:"createdAt"`
	Replayed                bool           `json:"replayed"`
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
	AccountID             string `json:"accountId"`
	WorkspaceID           string `json:"workspaceId"`
	ResourceType          string `json:"resourceType"`
	ResourceID            string `json:"resourceId"`
	AmountCents           int64  `json:"amountCents"`
	ActivationAmountCents int64  `json:"activationAmountCents,omitempty"`
	Currency              string `json:"currency"`
}

type HoldResult struct {
	ID                    string `json:"id"`
	AccountID             string `json:"accountId"`
	WorkspaceID           string `json:"workspaceId"`
	ResourceType          string `json:"resourceType"`
	ResourceID            string `json:"resourceId"`
	AmountCents           int64  `json:"amountCents"`
	ActivationAmountCents int64  `json:"activationAmountCents"`
	OriginalCents         int64  `json:"originalCents"`
	RemainingCents        int64  `json:"remainingCents"`
	ConsumedCents         int64  `json:"consumedCents"`
	ReleasedCents         int64  `json:"releasedCents"`
	Currency              string `json:"currency"`
	Status                string `json:"status"`
	Wallet                Wallet `json:"wallet"`
}

type HoldActivationInput struct {
	AccountID           string `json:"accountId"`
	WorkspaceID         string `json:"workspaceId"`
	ResourceType        string `json:"resourceType"`
	ResourceID          string `json:"resourceId"`
	HoldID              string `json:"holdId"`
	Currency            string `json:"currency"`
	ProviderEvidenceRef string `json:"providerEvidenceRef"`
}

type HoldActivationResult struct {
	HoldResult
	ActivationLedgerEntryID       string `json:"activationLedgerEntryId"`
	ActivationWalletTransactionID string `json:"activationWalletTransactionId"`
}

type HoldReleaseInput struct {
	AccountID    string `json:"accountId"`
	WorkspaceID  string `json:"workspaceId"`
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId"`
	HoldID       string `json:"holdId"`
	AmountCents  int64  `json:"amountCents"`
	Currency     string `json:"currency"`
	Reason       string `json:"reason,omitempty"`
}

type HoldReleaseResult struct {
	ID                  string `json:"id"`
	AccountID           string `json:"accountId"`
	WorkspaceID         string `json:"workspaceId"`
	ResourceType        string `json:"resourceType"`
	ResourceID          string `json:"resourceId"`
	HoldID              string `json:"holdId"`
	AmountCents         int64  `json:"amountCents"`
	Currency            string `json:"currency"`
	Status              string `json:"status"`
	LedgerEntryID       string `json:"ledgerEntryId"`
	WalletTransactionID string `json:"walletTransactionId"`
	Wallet              Wallet `json:"wallet"`
	Replayed            bool   `json:"replayed"`
}

type ReceiptInput struct {
	Type                string         `json:"type"`
	Status              string         `json:"status"`
	Surface             string         `json:"surface"`
	OrganizationID      string         `json:"organizationId,omitempty"`
	WorkspaceID         string         `json:"workspaceId"`
	ProjectID           string         `json:"projectId,omitempty"`
	TaskID              string         `json:"taskId,omitempty"`
	RequestID           string         `json:"requestId,omitempty"`
	ApprovalID          string         `json:"approvalId,omitempty"`
	JobID               string         `json:"jobId,omitempty"`
	ArtifactID          string         `json:"artifactId,omitempty"`
	ReviewID            string         `json:"reviewId,omitempty"`
	Execution           map[string]any `json:"execution,omitempty"`
	OutputRefs          map[string]any `json:"outputRefs,omitempty"`
	ReviewerChecks      map[string]any `json:"reviewerChecks,omitempty"`
	Continuation        map[string]any `json:"continuation,omitempty"`
	SupersedesReceiptID string         `json:"supersedesReceiptId,omitempty"`
}

type Receipt struct {
	ReceiptID           string         `json:"receiptId"`
	Status              string         `json:"status"`
	WorkspaceID         string         `json:"workspaceId"`
	ProjectID           string         `json:"projectId"`
	TaskID              string         `json:"taskId"`
	RequestID           string         `json:"requestId"`
	ApprovalID          string         `json:"approvalId"`
	JobID               string         `json:"jobId"`
	Execution           map[string]any `json:"execution"`
	ContinuationID      string         `json:"continuationId"`
	SupersedesReceiptID string         `json:"supersedesReceiptId"`
}

type Review struct {
	ReviewID             string         `json:"reviewId"`
	ReceiptID            string         `json:"receiptId"`
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
}

type Artifact struct {
	ArtifactID     string `json:"artifactId"`
	ReceiptID      string `json:"receiptId"`
	OrganizationID string `json:"organizationId"`
	WorkspaceID    string `json:"workspaceId"`
	ProjectID      string `json:"projectId"`
	TaskID         string `json:"taskId"`
	JobID          string `json:"jobId"`
	Digest         string `json:"digest"`
	MediaType      string `json:"mediaType"`
	SizeBytes      int64  `json:"sizeBytes"`
	StorageRef     string `json:"storageRef"`
}

type ledgerHTTPClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewLedgerHTTPClient(baseURL, token string, client *http.Client) LedgerClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &ledgerHTTPClient{baseURL: baseURL, token: token, client: client}
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

func (c *ledgerHTTPClient) ActivateHold(ctx context.Context, input HoldActivationInput, idempotencyKey string) (HoldActivationResult, error) {
	var result HoldActivationResult
	err := c.post(ctx, "/ledger/holds/activate", input, idempotencyKey, &result)
	return result, err
}

func (c *ledgerHTTPClient) ReleaseHold(ctx context.Context, input HoldReleaseInput, idempotencyKey string) (HoldReleaseResult, error) {
	var result HoldReleaseResult
	err := c.post(ctx, "/ledger/holds/release", input, idempotencyKey, &result)
	return result, err
}

func (c *ledgerHTTPClient) RecordReceipt(ctx context.Context, input ReceiptInput, idempotencyKey string) (Receipt, error) {
	var result Receipt
	err := c.post(ctx, "/ledger/receipts", input, idempotencyKey, &result)
	return result, err
}

func (c *ledgerHTTPClient) Receipt(ctx context.Context, receiptID string) (Receipt, error) {
	var result Receipt
	err := c.get(ctx, "/ledger/receipts/"+url.PathEscape(receiptID), &result)
	return result, err
}

func (c *ledgerHTTPClient) Artifact(ctx context.Context, artifactID string) (Artifact, error) {
	var result Artifact
	err := c.get(ctx, "/ledger/artifacts/"+url.PathEscape(artifactID), &result)
	return result, err
}

func (c *ledgerHTTPClient) Review(ctx context.Context, reviewID string) (Review, error) {
	var result Review
	err := c.get(ctx, "/ledger/reviews/"+url.PathEscape(reviewID), &result)
	return result, err
}

func (c *ledgerHTTPClient) Continuation(ctx context.Context, receiptID string) (map[string]any, error) {
	result := map[string]any{}
	err := c.get(ctx, "/ledger/receipts/"+url.PathEscape(receiptID)+"/continuation", &result)
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

func (c *ledgerHTTPClient) Wallet(ctx context.Context, accountID string) (Wallet, error) {
	var result Wallet
	err := c.get(ctx, "/ledger/accounts/"+accountID+"/wallet", &result)
	return result, err
}

func (c *ledgerHTTPClient) ListLedgerEntries(ctx context.Context, accountID string) ([]LedgerEntry, error) {
	var result []LedgerEntry
	err := c.get(ctx, accountPath(accountID, "entries"), &result)
	return result, err
}

func (c *ledgerHTTPClient) ListWalletTransactions(ctx context.Context, accountID string) ([]WalletTransaction, error) {
	var result []WalletTransaction
	err := c.get(ctx, accountPath(accountID, "wallet-transactions"), &result)
	return result, err
}

func (c *ledgerHTTPClient) ListManualTopUps(ctx context.Context, accountID string) ([]ManualTopUp, error) {
	var result []ManualTopUp
	err := c.get(ctx, accountPath(accountID, "topups"), &result)
	return result, err
}

func (c *ledgerHTTPClient) ListResourceSettlements(ctx context.Context, accountID string) ([]ResourceSettlementResult, error) {
	var result []ResourceSettlementResult
	err := c.get(ctx, accountPath(accountID, "resource-settlements"), &result)
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
	c.authorize(req)
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("ledger request failed: status %d: %s", res.StatusCode, string(body))
	}
	return json.NewDecoder(res.Body).Decode(output)
}

func (c *ledgerHTTPClient) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
}

func (c *ledgerHTTPClient) get(ctx context.Context, path string, output any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	c.authorize(req)
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("ledger request failed: status %d: %s", res.StatusCode, string(body))
	}
	return json.NewDecoder(res.Body).Decode(output)
}

func accountPath(accountID string, resource string) string {
	if accountID == "" {
		return "/ledger/" + resource
	}
	return "/ledger/accounts/" + accountID + "/" + resource
}
