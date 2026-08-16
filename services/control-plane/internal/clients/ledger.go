package clients

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type LedgerClient interface {
	RecordReceipt(ctx context.Context, input ReceiptInput, idempotencyKey string) (Receipt, error)
	Receipt(ctx context.Context, receiptID string) (Receipt, error)
	RecordReconciliation(ctx context.Context, input ReconciliationInput, idempotencyKey string) (ReconciliationResult, error)
}

type LedgerScopedReceiptClient interface {
	ReceiptForAccount(ctx context.Context, accountID, workspaceID, receiptID string) (Receipt, error)
}

type LedgerReceiptListClient interface {
	ListReceipts(ctx context.Context, query ReceiptQuery) (ReceiptPage, error)
}

type LedgerReadinessClient interface {
	Ready(ctx context.Context) error
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
	Actor               map[string]any `json:"actor,omitempty"`
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
	AccountID  string
	TypePrefix string
	Cursor     string
	Limit      int
}

type ReceiptPage struct {
	Receipts   []Receipt `json:"receipts"`
	NextCursor string    `json:"nextCursor"`
	HasMore    bool      `json:"hasMore"`
}

type ledgerHTTPClient struct {
	baseURL       string
	token         string
	capabilityKey string
	client        *http.Client
}

func NewLedgerHTTPClient(baseURL, token string, client *http.Client) LedgerClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &ledgerHTTPClient{baseURL: baseURL, token: token, client: client}
}

func NewLedgerHTTPClientWithCapability(baseURL, token, capabilityKey string, client *http.Client) LedgerClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &ledgerHTTPClient{baseURL: baseURL, token: token, capabilityKey: capabilityKey, client: client}
}

func (c *ledgerHTTPClient) RecordReceipt(ctx context.Context, input ReceiptInput, idempotencyKey string) (Receipt, error) {
	var result Receipt
	if err := c.post(ctx, "/ledger/receipts", input, idempotencyKey, &result); err != nil {
		return Receipt{}, err
	}
	if input.Type == "billing.workspace_purchased.v1" || input.Type == "billing.workspace_renewed.v1" || input.Type == "billing.workspace_expired.v1" || input.Type == "billing.workspace_refunded.v1" || input.Type == "workspace.gateway_key_rotated.v1" || input.Type == "gateway.wallet_adjustment.v1" {
		submitted, submittedErr := json.Marshal(input)
		returned, returnedErr := json.Marshal(result.ReceiptInput)
		if submittedErr != nil || returnedErr != nil || !bytes.Equal(submitted, returned) || result.ReceiptID == "" {
			return Receipt{}, fmt.Errorf("invalid Ledger receipt response")
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
	if query.TypePrefix != "" {
		values.Set("typePrefix", query.TypePrefix)
	}
	if query.Cursor != "" {
		values.Set("cursor", query.Cursor)
	}
	var result ReceiptPage
	err := c.getScoped(ctx, "/ledger/receipts?"+values.Encode(), query.AccountID, "", &result)
	return result, err
}

func (c *ledgerHTTPClient) Receipt(ctx context.Context, receiptID string) (Receipt, error) {
	if c.capabilityKey != "" {
		return Receipt{}, fmt.Errorf("ledger receipt scope required")
	}
	return c.ReceiptForAccount(ctx, "", "", receiptID)
}

func (c *ledgerHTTPClient) ReceiptForAccount(ctx context.Context, accountID, workspaceID, receiptID string) (Receipt, error) {
	var result Receipt
	values := url.Values{}
	if accountID != "" {
		values.Set("accountId", accountID)
	}
	if workspaceID != "" {
		values.Set("workspaceId", workspaceID)
	}
	path := "/ledger/receipts/" + url.PathEscape(receiptID)
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	err := c.getScoped(ctx, path, accountID, workspaceID, &result)
	return result, err
}

func (c *ledgerHTTPClient) Ready(ctx context.Context) error {
	var result struct {
		Status string `json:"status"`
	}
	if err := c.get(ctx, "/readyz", &result); err != nil {
		return err
	}
	if result.Status != "ready" {
		return fmt.Errorf("invalid Ledger readiness response")
	}
	return nil
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
	capability, err := signLedgerCapability(c.capabilityKey, ledgerClientScopeFromInput(path, input, idempotencyKey), body, time.Now())
	if err != nil {
		return err
	}
	if capability != "" {
		req.Header.Set("X-OPL-Ledger-Capability", capability)
	}
	c.authorize(req)
	return c.do(req, output)
}

func (c *ledgerHTTPClient) get(ctx context.Context, path string, output any) error {
	return c.getScoped(ctx, path, "", "", output)
}

func (c *ledgerHTTPClient) getScoped(ctx context.Context, path, accountID, workspaceID string, output any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	c.authorize(req)
	capability, err := signLedgerCapability(c.capabilityKey, ledgerClientScope{AccountID: accountID, WorkspaceID: workspaceID, ResourceKind: ledgerResourceKind(path), ResourceID: ledgerResourceID(path), Action: ledgerAction(path), OperationID: ledgerRequestOperation(req)}, nil, time.Now())
	if err != nil {
		return err
	}
	if capability != "" {
		req.Header.Set("X-OPL-Ledger-Capability", capability)
	}
	return c.do(req, output)
}

type ledgerClientScope struct{ AccountID, WorkspaceID, ResourceKind, ResourceID, Action, OperationID string }

func signLedgerCapability(key string, scope ledgerClientScope, body []byte, now time.Time) (string, error) {
	if key == "" {
		return "", nil
	}
	digest := sha256.Sum256(body)
	claims := map[string]any{"version": 1, "caller": "control-plane", "accountId": scope.AccountID, "workspaceId": scope.WorkspaceID, "resourceKind": scope.ResourceKind, "resourceId": scope.ResourceID, "action": scope.Action, "operationId": scope.OperationID, "expiresAt": now.Add(time.Minute).Unix(), "bodySha256": hex.EncodeToString(digest[:])}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func ledgerClientScopeFromInput(path string, input any, operationID string) ledgerClientScope {
	scope := ledgerClientScope{ResourceKind: ledgerResourceKind(path), ResourceID: operationID, Action: ledgerAction(path), OperationID: operationID}
	if value, ok := input.(ReceiptInput); ok {
		scope.AccountID, scope.WorkspaceID = value.AccountID, value.WorkspaceID
	}
	if value, ok := input.(ReconciliationInput); ok {
		scope.ResourceID = stringField(value.Report, "id")
		scope.AccountID = stringField(value.Report, "accountId")
		scope.WorkspaceID = stringField(value.Report, "workspaceId")
	}
	if raw, err := json.Marshal(input); err == nil {
		var fields map[string]any
		if json.Unmarshal(raw, &fields) == nil {
			if account, ok := fields["accountId"].(string); ok {
				scope.AccountID = account
			}
			if workspace, ok := fields["workspaceId"].(string); ok {
				scope.WorkspaceID = workspace
			}
		}
	}
	return scope
}

func stringField(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return value
}
func ledgerResourceKind(path string) string {
	if strings.Contains(path, "reconciliation") {
		return "reconciliation"
	}
	if strings.Contains(path, "review-policies") {
		if strings.Contains(path, "?") {
			return "review_policy_collection"
		}
		return "review_policy"
	}
	if strings.Contains(path, "review-gates/evaluate") {
		return "review_gate"
	}
	if strings.Contains(path, "artifacts") {
		if strings.Contains(path, "?") {
			return "artifact_collection"
		}
		return "artifact"
	}
	if strings.Contains(path, "reviews") {
		if strings.Contains(path, "?") {
			return "review_collection"
		}
		return "review"
	}
	if strings.Contains(path, "receipts?") {
		return "receipt_collection"
	}
	return "receipt"
}
func ledgerResourceID(path string) string {
	parsed, err := url.Parse(path)
	if err == nil && strings.HasSuffix(strings.Trim(parsed.Path, "/"), "receipts") {
		return parsed.Query().Get("accountId")
	}
	if err == nil && strings.HasSuffix(strings.Trim(parsed.Path, "/"), "review-policies") {
		return parsed.Query().Get("workspaceId")
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 3 {
		return parts[2]
	}
	return ""
}
func ledgerAction(path string) string {
	if strings.Contains(path, "/continuation") {
		return "read_continuation"
	}
	if strings.Contains(path, "reconciliation") {
		return "record_reconciliation"
	}
	if strings.Contains(path, "review-gates/evaluate") {
		return "evaluate_review_gate"
	}
	if strings.Contains(path, "review-policies") {
		if strings.Contains(path, "?") {
			return "list_review_policies"
		}
		if strings.Count(path, "/") > 2 {
			return "read_review_policy"
		}
		return "create_review_policy"
	}
	if strings.Contains(path, "artifacts") {
		if strings.Count(path, "/") > 2 {
			return "read_artifact"
		}
		return "record_artifact"
	}
	if strings.Contains(path, "reviews") {
		if strings.Contains(path, "?") {
			return "list_reviews"
		}
		if strings.Count(path, "/") > 2 {
			return "read_review"
		}
		return "record_review"
	}
	if strings.Contains(path, "receipts?") {
		return "list_receipts"
	}
	if strings.Contains(path, "receipts/") {
		return "read_receipt"
	}
	return "record_receipt"
}
func ledgerRequestOperation(req *http.Request) string {
	hash := sha256.Sum256([]byte(req.Method + " " + req.URL.RequestURI()))
	return "request:" + hex.EncodeToString(hash[:])
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
