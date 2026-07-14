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
	RecordReceipt(ctx context.Context, input ReceiptInput, idempotencyKey string) (Receipt, error)
	Receipt(ctx context.Context, receiptID string) (Receipt, error)
	Artifact(ctx context.Context, artifactID string) (Artifact, error)
	Review(ctx context.Context, reviewID string) (Review, error)
	Continuation(ctx context.Context, receiptID string) (map[string]any, error)
	RecordReconciliation(ctx context.Context, input ReconciliationInput, idempotencyKey string) (ReconciliationResult, error)
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

type ReceiptInput struct {
	Type                string         `json:"type"`
	Status              string         `json:"status"`
	Surface             string         `json:"surface"`
	AccountID           string         `json:"accountId,omitempty"`
	OrganizationID      string         `json:"organizationId,omitempty"`
	WorkspaceID         string         `json:"workspaceId"`
	ProjectID           string         `json:"projectId,omitempty"`
	TaskID              string         `json:"taskId,omitempty"`
	RequestID           string         `json:"requestId,omitempty"`
	ApprovalID          string         `json:"approvalId,omitempty"`
	JobID               string         `json:"jobId,omitempty"`
	ArtifactID          string         `json:"artifactId,omitempty"`
	ReviewID            string         `json:"reviewId,omitempty"`
	Plan                map[string]any `json:"plan,omitempty"`
	Execution           map[string]any `json:"execution,omitempty"`
	Environment         map[string]any `json:"environment,omitempty"`
	InputRefs           map[string]any `json:"inputRefs,omitempty"`
	OutputRefs          map[string]any `json:"outputRefs,omitempty"`
	ReviewerChecks      map[string]any `json:"reviewerChecks,omitempty"`
	Cost                map[string]any `json:"cost,omitempty"`
	Owner               map[string]any `json:"owner,omitempty"`
	Continuation        map[string]any `json:"continuation,omitempty"`
	SupersedesReceiptID string         `json:"supersedesReceiptId,omitempty"`
}

type Receipt struct {
	ReceiptInput
	ReceiptID      string `json:"receiptId"`
	ContinuationID string `json:"continuationId"`
	CreatedAt      string `json:"createdAt"`
	Replayed       bool   `json:"replayed"`
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
	c.authorize(req)
	return c.do(req, output)
}

func (c *ledgerHTTPClient) get(ctx context.Context, path string, output any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	c.authorize(req)
	return c.do(req, output)
}

func (c *ledgerHTTPClient) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
}

func (c *ledgerHTTPClient) do(req *http.Request, output any) error {
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		return fmt.Errorf("ledger request failed: status %d: %s", res.StatusCode, string(body))
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(output)
}
