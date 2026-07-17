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

type LedgerReceiptListClient interface {
	ListReceipts(ctx context.Context, query ReceiptQuery) (ReceiptPage, error)
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

type ReceiptQuery struct {
	AccountID string
	Cursor    string
	Limit     int
}

type ReceiptPage struct {
	Receipts   []Receipt `json:"receipts"`
	NextCursor string    `json:"nextCursor"`
	HasMore    bool      `json:"hasMore"`
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
	if err := c.post(ctx, "/ledger/receipts", input, idempotencyKey, &result); err != nil {
		return Receipt{}, err
	}
	if input.Type == "billing.workspace_renewed.v1" || input.Type == "billing.workspace_expired.v1" {
		submittedCost, submittedErr := json.Marshal(input.Cost)
		returnedCost, returnedErr := json.Marshal(result.Cost)
		if submittedErr != nil || returnedErr != nil || !bytes.Equal(submittedCost, returnedCost) || result.ReceiptID == "" ||
			result.Type != input.Type || result.Status != input.Status || result.Surface != input.Surface || result.AccountID != input.AccountID ||
			result.WorkspaceID != input.WorkspaceID || result.RequestID != input.RequestID {
			return Receipt{}, fmt.Errorf("invalid Ledger Workspace billing receipt response")
		}
	}
	return result, nil
}

func (c *ledgerHTTPClient) ListReceipts(ctx context.Context, query ReceiptQuery) (ReceiptPage, error) {
	if query.Limit == 0 {
		query.Limit = 50
	}
	if query.Limit < 1 || query.Limit > 100 {
		return ReceiptPage{}, fmt.Errorf("ledger receipt limit must be between 1 and 100")
	}
	values := url.Values{
		"accountId": {query.AccountID},
		"limit":     {fmt.Sprint(query.Limit)},
	}
	if query.Cursor != "" {
		values.Set("cursor", query.Cursor)
	}
	var result ReceiptPage
	err := c.get(ctx, "/ledger/receipts?"+values.Encode(), &result)
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
	var response struct {
		ID                 string         `json:"id"`
		Status             string         `json:"status"`
		Report             map[string]any `json:"report"`
		BlockNewWorkspaces *bool          `json:"blockNewWorkspaces"`
		Reason             string         `json:"reason"`
		Replayed           bool           `json:"replayed"`
	}
	if err := c.post(ctx, "/ledger/reconciliation", input, idempotencyKey, &response); err != nil {
		return ReconciliationResult{}, err
	}
	submitted, submittedErr := json.Marshal(input.Report)
	returned, returnedErr := json.Marshal(response.Report)
	reportID, idOK := response.Report["id"].(string)
	reportStatus, statusOK := response.Report["status"].(string)
	if submittedErr != nil || returnedErr != nil || !bytes.Equal(submitted, returned) || !idOK || reportID == "" || !statusOK || (reportStatus != "ok" && reportStatus != "mismatch") ||
		response.ID != reportID || response.Status != reportStatus || response.BlockNewWorkspaces == nil || *response.BlockNewWorkspaces != (reportStatus == "mismatch") || response.Reason != "operator_reconciliation" {
		return ReconciliationResult{}, fmt.Errorf("invalid ledger reconciliation response")
	}
	return ReconciliationResult{
		ID: response.ID, Status: response.Status, Report: response.Report, BlockNewWorkspaces: *response.BlockNewWorkspaces,
		Reason: response.Reason, Replayed: response.Replayed,
	}, nil
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
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		return fmt.Errorf("ledger request failed: status %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(body) > 1<<20 {
		return fmt.Errorf("ledger response too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("ledger response contains multiple JSON values")
	}
	return nil
}
