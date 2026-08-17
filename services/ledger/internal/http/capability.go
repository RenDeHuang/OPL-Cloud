package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const ledgerCapabilityHeader = "X-OPL-Ledger-Capability"

type ledgerCapabilityScope struct {
	AccountID    string
	WorkspaceID  string
	ResourceKind string
	ResourceID   string
	Action       string
	OperationID  string
}

type ledgerCapabilityClaims struct {
	Version      int    `json:"version"`
	Caller       string `json:"caller"`
	AccountID    string `json:"accountId"`
	WorkspaceID  string `json:"workspaceId"`
	ResourceKind string `json:"resourceKind"`
	ResourceID   string `json:"resourceId"`
	Action       string `json:"action"`
	OperationID  string `json:"operationId"`
	ExpiresAt    int64  `json:"expiresAt"`
	BodySHA256   string `json:"bodySha256"`
}

func ledgerCapabilityScopeForRequest(r *http.Request, body []byte) (ledgerCapabilityScope, bool) {
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	scope := ledgerCapabilityScope{OperationID: strings.TrimSpace(r.Header.Get("Idempotency-Key"))}
	var input map[string]any
	if len(body) > 0 && json.Unmarshal(body, &input) != nil {
		return ledgerCapabilityScope{}, false
	}
	value := func(name string) string {
		if input == nil {
			return ""
		}
		result, _ := input[name].(string)
		return strings.TrimSpace(result)
	}
	scope.AccountID, scope.WorkspaceID = value("accountId"), value("workspaceId")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/ledger/receipts":
		scope.ResourceKind, scope.ResourceID, scope.Action = "receipt", scope.OperationID, "record_receipt"
	case r.Method == http.MethodGet && r.URL.Path == "/ledger/receipts":
		scope.AccountID = strings.TrimSpace(r.URL.Query().Get("accountId"))
		scope.ResourceKind, scope.ResourceID, scope.Action = "receipt_collection", scope.AccountID, "list_receipts"
		scope.OperationID = requestOperationID(r)
	case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "ledger" && parts[1] == "receipts":
		scope.AccountID, scope.WorkspaceID = strings.TrimSpace(r.URL.Query().Get("accountId")), strings.TrimSpace(r.URL.Query().Get("workspaceId"))
		scope.ResourceKind, scope.ResourceID, scope.Action = "receipt", parts[2], "read_receipt"
		scope.OperationID = requestOperationID(r)
	case r.Method == http.MethodPost && len(parts) == 4 && parts[0] == "ledger" && parts[1] == "receipts" && parts[3] == "retention":
		scope.AccountID, scope.WorkspaceID = strings.TrimSpace(r.URL.Query().Get("accountId")), strings.TrimSpace(r.URL.Query().Get("workspaceId"))
		scope.ResourceKind, scope.ResourceID, scope.Action = "receipt", parts[2], "update_receipt_retention"
	case r.Method == http.MethodPost && len(parts) == 4 && parts[0] == "ledger" && parts[1] == "receipts" && parts[3] == "privacy-delete":
		scope.AccountID, scope.WorkspaceID = strings.TrimSpace(r.URL.Query().Get("accountId")), strings.TrimSpace(r.URL.Query().Get("workspaceId"))
		scope.ResourceKind, scope.ResourceID, scope.Action = "receipt", parts[2], "privacy_delete_receipt"
	case r.Method == http.MethodPost && r.URL.Path == "/ledger/reconciliation":
		report, _ := input["report"].(map[string]any)
		scope.AccountID, scope.WorkspaceID = strings.TrimSpace(stringValue(report, "accountId")), strings.TrimSpace(stringValue(report, "workspaceId"))
		scope.ResourceKind, scope.ResourceID, scope.Action = "reconciliation", strings.TrimSpace(stringValue(report, "id")), "record_reconciliation"
	}
	return scope, scope.ResourceKind != "" && scope.ResourceID != "" && scope.Action != "" && scope.OperationID != ""
}

func stringValue(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return value
}

func requestOperationID(r *http.Request) string {
	hash := sha256.Sum256([]byte(r.Method + " " + r.URL.RequestURI()))
	return "request:" + hex.EncodeToString(hash[:])
}

func preverifyLedgerCapability(raw, key string, expected ledgerCapabilityScope, body []byte, now time.Time) (ledgerCapabilityClaims, bool) {
	parts := strings.Split(raw, ".")
	if key == "" || len(parts) != 2 {
		return ledgerCapabilityClaims{}, false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ledgerCapabilityClaims{}, false
	}
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return ledgerCapabilityClaims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ledgerCapabilityClaims{}, false
	}
	var claims ledgerCapabilityClaims
	if json.Unmarshal(payload, &claims) != nil {
		return ledgerCapabilityClaims{}, false
	}
	digest := sha256.Sum256(body)
	return claims, claims.Version == 1 && claims.Caller == "control-plane" && claims.ResourceKind == expected.ResourceKind && claims.ResourceID == expected.ResourceID &&
		claims.Action == expected.Action && claims.OperationID == expected.OperationID && claims.ExpiresAt > now.Unix() &&
		claims.ExpiresAt <= now.Add(2*time.Minute).Unix() && claims.BodySHA256 == hex.EncodeToString(digest[:])
}

func queryWithOwner(path, accountID, workspaceID string) string {
	parsed, err := url.Parse(path)
	if err != nil {
		return path
	}
	values := parsed.Query()
	if accountID != "" {
		values.Set("accountId", accountID)
	}
	if workspaceID != "" {
		values.Set("workspaceId", workspaceID)
	}
	parsed.RawQuery = values.Encode()
	return parsed.String()
}
