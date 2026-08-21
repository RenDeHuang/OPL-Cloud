package http

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/ledger/internal/ledger"
)

func TestServerAuthenticatesEverythingExceptGetHealthz(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	tests := []struct {
		name          string
		method        string
		path          string
		authorization string
		want          int
	}{
		{name: "health", method: http.MethodGet, path: "/healthz", want: http.StatusOK},
		{name: "readiness", method: http.MethodGet, path: "/readyz", want: http.StatusOK},
		{name: "health wrong method", method: http.MethodPost, path: "/healthz", want: http.StatusUnauthorized},
		{name: "readiness wrong method", method: http.MethodPost, path: "/readyz", want: http.StatusUnauthorized},
		{name: "business anonymous", method: http.MethodGet, path: "/ledger/receipts", want: http.StatusUnauthorized},
		{name: "unknown anonymous", method: http.MethodGet, path: "/missing", want: http.StatusUnauthorized},
		{name: "wrong token", method: http.MethodGet, path: "/ledger/receipts", authorization: "Bearer wrong", want: http.StatusUnauthorized},
		{name: "authenticated", method: http.MethodGet, path: "/ledger/receipts", authorization: "Bearer internal-secret", want: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Authorization", tt.authorization)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestLedgerCapabilityRejectsUnscopedBearerAndAcceptsBoundRequest(t *testing.T) {
	const key = "ledger-capability-key-for-http-tests-32-chars"
	store := ledger.NewMemoryStore()
	server := NewServerWithAuth(store, "internal-secret", key)
	body := `{"type":"execution.receipt.v1","status":"completed","surface":"workspace","accountId":"acct-alpha","workspaceId":"ws-alpha"}`
	req := testRequest(http.MethodPost, "/ledger/receipts", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer internal-secret")
	req.Header.Set("Idempotency-Key", "ledger-capability-once")
	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, req)
	if unauthorized.Code != http.StatusForbidden {
		t.Fatalf("missing capability status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}

	capability := testLedgerCapability(t, key, ledgerCapabilityClaims{Version: 1, Caller: "control-plane", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ResourceKind: "receipt", ResourceID: "ledger-capability-once", Action: "record_receipt", OperationID: "ledger-capability-once", ExpiresAt: time.Now().Add(time.Minute).Unix()}, []byte(body))
	req = testRequest(http.MethodPost, "/ledger/receipts", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer internal-secret")
	req.Header.Set("Idempotency-Key", "ledger-capability-once")
	req.Header.Set(ledgerCapabilityHeader, capability)
	accepted := httptest.NewRecorder()
	server.ServeHTTP(accepted, req)
	if accepted.Code != http.StatusCreated {
		t.Fatalf("valid capability status=%d body=%s", accepted.Code, accepted.Body.String())
	}
}

func TestLedgerCapabilityIsPreverifiedBeforeOwnerLookup(t *testing.T) {
	const key = "ledger-capability-key-for-http-tests-32-chars"
	store := &callCountingStore{Store: ledger.NewMemoryStore()}
	server := NewServerWithAuth(store, "internal-secret", key)
	for _, path := range []string{
		"/ledger/receipts/receipt-alpha?accountId=acct-alpha&workspaceId=ws-alpha",
	} {
		req := testRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	if store.calls != 0 {
		t.Fatalf("invalid capabilities reached owner lookup: calls=%d", store.calls)
	}
}

func TestLedgerReceiptReadCapabilityAcceptsExactAccountAndOptionalWorkspace(t *testing.T) {
	const key = "ledger-capability-key-for-http-tests-32-chars"
	store := ledger.NewMemoryStore()
	server := NewServerWithAuth(store, "internal-secret", key)
	for _, receiptType := range []string{"billing.workspace_purchased.v1", "billing.workspace_refunded.v1"} {
		receipt, err := store.RecordReceipt(context.Background(), capabilityWorkspaceReceiptInput(receiptType))
		if err != nil {
			t.Fatalf("record %s: %v", receiptType, err)
		}
		for _, test := range []struct {
			name, accountID, workspaceID string
			wantStatus                   int
		}{
			{name: "account only", accountID: "acct-alpha", wantStatus: http.StatusOK},
			{name: "exact workspace", accountID: "acct-alpha", workspaceID: "workspace-alpha", wantStatus: http.StatusOK},
			{name: "empty account", wantStatus: http.StatusForbidden},
			{name: "wrong account", accountID: "acct-other", wantStatus: http.StatusForbidden},
			{name: "wrong workspace", accountID: "acct-alpha", workspaceID: "workspace-other", wantStatus: http.StatusForbidden},
		} {
			t.Run(receiptType+"/"+test.name, func(t *testing.T) {
				path := queryWithOwner("/ledger/receipts/"+receipt.ReceiptID, test.accountID, test.workspaceID)
				req := testRequest(http.MethodGet, path, nil)
				req.Header.Set("Authorization", "Bearer internal-secret")
				claims := ledgerCapabilityClaims{
					Version: 1, Caller: "control-plane", AccountID: test.accountID, WorkspaceID: test.workspaceID,
					ResourceKind: "receipt", ResourceID: receipt.ReceiptID, Action: "read_receipt",
					OperationID: requestOperationID(req), ExpiresAt: time.Now().Add(time.Minute).Unix(),
				}
				req.Header.Set(ledgerCapabilityHeader, testLedgerCapability(t, key, claims, nil))
				rec := httptest.NewRecorder()
				server.ServeHTTP(rec, req)
				if rec.Code != test.wantStatus {
					t.Fatalf("status=%d want=%d body=%s", rec.Code, test.wantStatus, rec.Body.String())
				}
			})
		}
	}
}

func capabilityWorkspaceReceiptInput(receiptType string) ledger.ReceiptInput {
	cost := map[string]any{
		"priceVersion": "pilot-usd-2026-07-v1", "currency": "USD", "billingUnit": "calendar_month",
		"totalUsdMicros": int64(52_580_000), "sub2apiUserId": int64(41), "sub2apiRedeemCode": "opl:workspace:charge:v1",
		"postChargeBalanceUsdMicros": int64(947_420_000), "periodStart": "2026-07-20T00:00:00Z", "paidThrough": "2026-08-20T00:00:00Z",
		"resourceType": "workspace", "resourceId": "workspace-alpha",
		"components": map[string]any{
			"compute": map[string]any{"resourceType": "compute", "resourceId": "compute-alpha", "chargeUsdMicros": int64(50_000_000)},
			"storage": map[string]any{"resourceType": "storage", "resourceId": "storage-alpha", "sizeGb": int64(10), "chargeUsdMicros": int64(2_580_000)},
		},
	}
	if receiptType == "billing.workspace_refunded.v1" {
		delete(cost, "postChargeBalanceUsdMicros")
		cost["sub2apiRefundCode"], cost["refundUsdMicros"] = "opl:workspace:refund:v1", int64(52_580_000)
	}
	input := ledger.ReceiptInput{
		Type: receiptType, Status: "completed", Surface: "control_plane", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha",
		Cost: cost, IdempotencyKey: "capability-" + receiptType,
	}
	if receiptType == "billing.workspace_purchased.v1" {
		input.RequestID = "workspace-launch-capability"
		input.IdempotencyKey = input.RequestID + ":purchase-receipt"
		input.Execution = map[string]any{
			"operationId": "workspace-launch-capability", "resourceType": "workspace", "resourceId": "workspace-alpha",
			"computeAllocationId": "compute-alpha", "storageId": "storage-alpha", "attachmentId": "attachment-alpha", "runtimeId": "runtime-alpha",
			"workspaceApiKeyId": int64(9), "workspaceKeyFingerprint": "sha256:alpha", "runtimeServiceName": "runtime-alpha", "gatewaySecretRef": "secret-alpha",
		}
		input.Owner = map[string]any{"accountId": "acct-alpha", "workspaceId": "workspace-alpha", "ownerUserId": "usr-alpha"}
	}
	return input
}

func workspaceLifecycleHTTPReceiptInput(receiptType string) ledger.ReceiptInput {
	input := capabilityWorkspaceReceiptInput("billing.workspace_purchased.v1")
	input.Type = receiptType
	input.IdempotencyKey = ""
	if receiptType == "workspace.created" {
		input.Cost = nil
		return input
	}
	if receiptType == "workspace.deleted.v1" {
		input.RequestID = "workspace-delete-http"
		input.Execution["operationId"] = input.RequestID
		input.InputRefs = map[string]any{"launchReceiptId": "receipt-launch-http"}
		input.OutputRefs = map[string]any{
			"runtimeStatus": "absent", "gatewaySecretStatus": "absent", "attachmentStatus": "absent", "storageStatus": "absent",
			"computeStatus": "absent", "workspaceKeyStatus": "absent", "workspaceStatus": "absent",
		}
		input.Cost = nil
	}
	return input
}

func testLedgerCapability(t *testing.T, key string, claims ledgerCapabilityClaims, body []byte) string {
	t.Helper()
	digest := sha256.Sum256(body)
	claims.BodySHA256 = hex.EncodeToString(digest[:])
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

type unavailableReadinessStore struct {
	ledger.Store
}

func (unavailableReadinessStore) Ready(context.Context) error {
	return errors.New("postgres unavailable")
}

func TestReadyzFailsClosedWhenStoreIsUnavailable(t *testing.T) {
	server := NewServer(unavailableReadinessStore{Store: ledger.NewMemoryStore()}, "internal-secret")
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"status":"unavailable"`) {
		t.Fatalf("readiness status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestReconciliationHTTP(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	req := testRequest(http.MethodPost, "/ledger/reconciliation", bytes.NewBufferString(`{"report":{"id":"recon-alpha","status":"mismatch","counts":{"billingOperations":1,"matched":0,"exceptions":1},"exceptions":[{"resourceType":"compute","resourceId":"compute-alpha","code":"ledger_receipt_missing"}]}}`))
	req.Header.Set("Idempotency-Key", "http-reconciliation-once")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"blockNewWorkspaces":true`) {
		t.Fatalf("reconciliation status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestJSONBodiesRejectTrailingDataWithoutPersistence(t *testing.T) {
	validReceipt := `{"type":"execution.receipt.v1","status":"completed","surface":"workspace","workspaceId":"workspace-trailing"}`
	validReconciliation := `{"report":{"id":"recon-trailing","status":"ok","counts":{"billingOperations":0,"matched":0,"exceptions":0},"exceptions":[]}}`
	for _, suffix := range []string{` {}`, ` trailing-garbage`} {
		t.Run(strings.TrimSpace(suffix), func(t *testing.T) {
			store := ledger.NewMemoryStore()
			server := NewServer(store, "internal-secret")

			receipt := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(validReceipt+suffix))
			receipt.Header.Set("Idempotency-Key", "trailing-receipt")
			receiptRec := httptest.NewRecorder()
			server.ServeHTTP(receiptRec, receipt)
			if receiptRec.Code != http.StatusBadRequest {
				t.Fatalf("trailing receipt status=%d body=%s", receiptRec.Code, receiptRec.Body.String())
			}

			reconciliation := testRequest(http.MethodPost, "/ledger/reconciliation", bytes.NewBufferString(validReconciliation+suffix))
			reconciliation.Header.Set("Idempotency-Key", "trailing-reconciliation")
			reconciliationRec := httptest.NewRecorder()
			server.ServeHTTP(reconciliationRec, reconciliation)
			if reconciliationRec.Code != http.StatusBadRequest {
				t.Fatalf("trailing reconciliation status=%d body=%s", reconciliationRec.Code, reconciliationRec.Body.String())
			}

			validReceiptRequest := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(validReceipt))
			validReceiptRequest.Header.Set("Idempotency-Key", "trailing-receipt")
			validReceiptRec := httptest.NewRecorder()
			server.ServeHTTP(validReceiptRec, validReceiptRequest)
			if validReceiptRec.Code != http.StatusCreated {
				t.Fatalf("valid receipt after trailing rejection status=%d body=%s", validReceiptRec.Code, validReceiptRec.Body.String())
			}

			validReconciliationRequest := testRequest(http.MethodPost, "/ledger/reconciliation", bytes.NewBufferString(validReconciliation))
			validReconciliationRequest.Header.Set("Idempotency-Key", "trailing-reconciliation")
			validReconciliationRec := httptest.NewRecorder()
			server.ServeHTTP(validReconciliationRec, validReconciliationRequest)
			if validReconciliationRec.Code != http.StatusCreated {
				t.Fatalf("valid reconciliation after trailing rejection status=%d body=%s", validReconciliationRec.Code, validReconciliationRec.Body.String())
			}
		})
	}
}

type callCountingStore struct {
	ledger.Store
	calls int
}

func (s *callCountingStore) Receipt(ctx context.Context, id string) (ledger.Receipt, error) {
	s.calls++
	return s.Store.Receipt(ctx, id)
}

func (s *callCountingStore) RecordReceipt(context.Context, ledger.ReceiptInput) (ledger.Receipt, error) {
	s.calls++
	return ledger.Receipt{}, nil
}

func (s *callCountingStore) UpdateReceiptRetention(context.Context, ledger.ReceiptRetentionInput) (ledger.ReceiptRetentionResult, error) {
	s.calls++
	return ledger.ReceiptRetentionResult{}, nil
}

func (s *callCountingStore) PrivacyDeleteReceipt(context.Context, ledger.ReceiptPrivacyDeleteInput) (ledger.ReceiptRetentionResult, error) {
	s.calls++
	return ledger.ReceiptRetentionResult{}, nil
}

func (s *callCountingStore) RecordReconciliation(context.Context, ledger.ReconciliationInput) (ledger.ReconciliationResult, error) {
	s.calls++
	return ledger.ReconciliationResult{}, nil
}

func TestJSONBodyLimitAppliesBeforeLedgerStoreCall(t *testing.T) {
	oversizedBody := `{"padding":"` + strings.Repeat("x", int(maxJSONBodyBytes)) + `"}`
	tests := []struct {
		name string
		path string
	}{
		{name: "receipt", path: "/ledger/receipts"},
		{name: "retention", path: "/ledger/receipts/receipt-alpha/retention"},
		{name: "privacy delete", path: "/ledger/receipts/receipt-alpha/privacy-delete"},
		{name: "reconciliation", path: "/ledger/reconciliation"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &callCountingStore{Store: ledger.NewMemoryStore()}
			server := NewServer(store, "internal-secret")
			req := testRequest(http.MethodPost, test.path, strings.NewReader(oversizedBody))
			req.Header.Set("Idempotency-Key", "oversized-body")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), `"error":"request body too large"`) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if store.calls != 0 {
				t.Fatalf("store calls=%d, want 0", store.calls)
			}
		})
	}
}

func TestJSONBodyLimitPrecedesMalformedJSONError(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	req := testRequest(http.MethodPost, "/ledger/receipts", strings.NewReader(`}`+strings.Repeat("x", int(maxJSONBodyBytes))))
	req.Header.Set("Idempotency-Key", "oversized-malformed-body")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), `"error":"request body too large"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestJSONBodyLimitPreservesMissingIdempotencyKeyError(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	req := testRequest(http.MethodPost, "/ledger/receipts", strings.NewReader(strings.Repeat("x", int(maxJSONBodyBytes)+1)))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error":"missing Idempotency-Key"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestJSONBodyLimitPreservesAuthenticationOrder(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	req := httptest.NewRequest(http.MethodPost, "/ledger/receipts", strings.NewReader(strings.Repeat("x", int(maxJSONBodyBytes)+1)))
	req.Header.Set("Idempotency-Key", "unauthorized-oversized-body")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"error":"unauthorized"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestReceiptAcceptsExactJSONBodyLimit(t *testing.T) {
	prefix := `{"type":"execution.receipt.v1","status":"completed","surface":"workspace","workspaceId":"workspace-alpha","outputRefs":{"padding":"`
	suffix := `"}}`
	payload := prefix + strings.Repeat("x", int(maxJSONBodyBytes)-len(prefix)-len(suffix)) + suffix
	if len(payload) != int(maxJSONBodyBytes) {
		t.Fatalf("payload size=%d, want %d", len(payload), maxJSONBodyBytes)
	}
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	req := testRequest(http.MethodPost, "/ledger/receipts", strings.NewReader(payload))
	req.Header.Set("Idempotency-Key", "exact-limit-body")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLegacyResourceBillingReceiptWritesRejectedHTTP(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	body := `{"type":"%s","status":"completed","surface":"control_plane","accountId":"acct-alpha","workspaceId":"workspace-alpha","cost":{"pricingVersion":"pricing-v1","monthlyPriceCnyCents":35000,"chargeUsdMicros":50000000,"sub2apiUserId":41,"sub2apiRedeemCode":"opl:test:billing-alpha:charge:v1","periodStart":"2026-07-01T00:00:00Z","paidThrough":"2026-08-01T00:00:00Z","resourceType":"compute","resourceId":"compute-alpha"}}`
	for _, receiptType := range []string{
		"billing.resource_purchased.v1",
		"billing.resource_renewed.v1",
		"billing.resource_expired.v1",
		"billing.resource_refunded.v1",
		"billing.charge_review_required.v1",
	} {
		t.Run(receiptType, func(t *testing.T) {
			req := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(fmt.Sprintf(body, receiptType)))
			req.Header.Set("Idempotency-Key", "http-retired-"+receiptType)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("retired billing receipt status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestWalletAdjustmentReceipt(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	body := `{"type":"gateway.wallet_adjustment.v1","status":"completed","surface":"control_plane","accountId":"acct-alpha","requestId":"wallet-adjustment-alpha","actor":{"userId":"usr-admin"},"execution":{"operationId":"wallet-adjustment-alpha","kind":"debit","amountUsdMicros":2500000},"inputRefs":{"balanceHistoryRef":"sub2api:balance-history:41:history-alpha"},"owner":{"accountId":"acct-alpha"}}`
	req := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(body))
	req.Header.Set("Idempotency-Key", "wallet-adjustment-alpha:receipt")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"type":"gateway.wallet_adjustment.v1"`) {
		t.Fatalf("wallet adjustment receipt status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWorkspaceGatewayKeyRotationReceiptSchemaHTTP(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	body := `{"type":"workspace.gateway_key_rotated.v1","status":"completed","surface":"control_plane","accountId":"acct-alpha","workspaceId":"workspace-alpha","execution":{"operationId":"workspace-key-rotate-alpha","oldKeyId":9,"newKeyId":19},"outputRefs":{"secretFingerprint":"sha256:replacement"},"owner":{"userId":"usr-alpha"}}`
	valid := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(body))
	valid.Header.Set("Idempotency-Key", "http-workspace-key-rotation")
	validRec := httptest.NewRecorder()
	server.ServeHTTP(validRec, valid)
	if validRec.Code != http.StatusCreated {
		t.Fatalf("valid Workspace Key rotation receipt status=%d body=%s", validRec.Code, validRec.Body.String())
	}
	invalid := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(strings.Replace(body, `"secretFingerprint":"sha256:replacement"`, `"fingerprint":"sha256:replacement"`, 1)))
	invalid.Header.Set("Idempotency-Key", "http-workspace-key-rotation-invalid")
	invalidRec := httptest.NewRecorder()
	server.ServeHTTP(invalidRec, invalid)
	if invalidRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid Workspace Key rotation receipt status=%d body=%s", invalidRec.Code, invalidRec.Body.String())
	}
}

func TestWorkspaceBillingReceiptSchemaHTTP(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	body := `{"type":"billing.workspace_renewed.v1","status":"completed","surface":"control_plane","accountId":"acct-alpha","workspaceId":"workspace-alpha","cost":{"priceVersion":"pilot-usd-2026-07-v1","currency":"USD","billingUnit":"calendar_month","totalUsdMicros":52580000,"sub2apiUserId":41,"sub2apiRedeemCode":"opl:workspace-renewal:charge:v1","postChargeBalanceUsdMicros":47420000,"periodStart":"2026-08-31T09:30:00Z","paidThrough":"2026-09-30T09:30:00Z","resourceType":"workspace","resourceId":"workspace-alpha","components":{"compute":{"resourceType":"compute","resourceId":"compute-alpha","chargeUsdMicros":50000000},"storage":{"resourceType":"storage","resourceId":"storage-alpha","sizeGb":10,"chargeUsdMicros":2580000}}}}`
	valid := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(body))
	valid.Header.Set("Idempotency-Key", "http-workspace-billing-schema")
	validRec := httptest.NewRecorder()
	server.ServeHTTP(validRec, valid)
	if validRec.Code != http.StatusCreated {
		t.Fatalf("valid Workspace billing receipt status=%d body=%s", validRec.Code, validRec.Body.String())
	}
	invalid := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(strings.Replace(body, `"totalUsdMicros":52580000`, `"totalUsdMicros":52579999`, 1)))
	invalid.Header.Set("Idempotency-Key", "http-workspace-billing-total-mismatch")
	invalidRec := httptest.NewRecorder()
	server.ServeHTTP(invalidRec, invalid)
	if invalidRec.Code != http.StatusBadRequest {
		t.Fatalf("mismatched Workspace billing receipt status=%d body=%s", invalidRec.Code, invalidRec.Body.String())
	}
	crossWorkspace := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(strings.Replace(body, `"workspaceId":"workspace-alpha"`, `"workspaceId":"workspace-other"`, 1)))
	crossWorkspace.Header.Set("Idempotency-Key", "http-workspace-billing-cross-workspace")
	crossWorkspaceRec := httptest.NewRecorder()
	server.ServeHTTP(crossWorkspaceRec, crossWorkspace)
	if crossWorkspaceRec.Code != http.StatusBadRequest {
		t.Fatalf("cross-Workspace billing receipt status=%d body=%s", crossWorkspaceRec.Code, crossWorkspaceRec.Body.String())
	}
	refundedBody := `{"type":"billing.workspace_refunded.v1","status":"completed","surface":"control_plane","accountId":"acct-alpha","workspaceId":"workspace-alpha","cost":{"priceVersion":"pilot-usd-2026-07-v1","currency":"USD","billingUnit":"calendar_month","totalUsdMicros":52580000,"sub2apiUserId":41,"sub2apiRedeemCode":"opl:workspace-renewal:charge:v1","sub2apiRefundCode":"opl:workspace-renewal:refund:v1","refundUsdMicros":52580000,"periodStart":"2026-07-31T09:30:00Z","paidThrough":"2026-08-31T09:30:00Z","resourceType":"workspace","resourceId":"workspace-alpha","components":{"compute":{"resourceType":"compute","resourceId":"compute-alpha","chargeUsdMicros":50000000},"storage":{"resourceType":"storage","resourceId":"storage-alpha","sizeGb":10,"chargeUsdMicros":2580000}}}}`
	refunded := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(refundedBody))
	refunded.Header.Set("Idempotency-Key", "http-workspace-refunded-schema")
	refundedRec := httptest.NewRecorder()
	server.ServeHTTP(refundedRec, refunded)
	if refundedRec.Code != http.StatusCreated {
		t.Fatalf("valid Workspace refund receipt status=%d body=%s", refundedRec.Code, refundedRec.Body.String())
	}
}

func TestWorkspacePurchasedReceipt(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	body := `{"type":"billing.workspace_purchased.v1","status":"completed","surface":"control_plane","accountId":"acct-alpha","workspaceId":"workspace-alpha","requestId":"workspace-launch-alpha","execution":{"operationId":"workspace-launch-alpha","resourceType":"workspace","resourceId":"workspace-alpha","computeAllocationId":"compute-alpha","storageId":"storage-alpha","attachmentId":"attachment-alpha","runtimeId":"runtime-alpha","workspaceApiKeyId":9,"workspaceKeyFingerprint":"sha256:alpha","runtimeServiceName":"runtime-alpha","gatewaySecretRef":"secret-alpha"},"owner":{"accountId":"acct-alpha","workspaceId":"workspace-alpha","ownerUserId":"usr-alpha"},"cost":{"priceVersion":"pilot-usd-2026-07-v1","currency":"USD","billingUnit":"calendar_month","totalUsdMicros":52580000,"sub2apiUserId":41,"sub2apiRedeemCode":"opl:workspace-launch:charge:v1","postChargeBalanceUsdMicros":947420000,"periodStart":"2026-07-20T00:00:00Z","paidThrough":"2026-08-20T00:00:00Z","resourceType":"workspace","resourceId":"workspace-alpha","components":{"compute":{"resourceType":"compute","resourceId":"compute-alpha","chargeUsdMicros":50000000},"storage":{"resourceType":"storage","resourceId":"storage-alpha","sizeGb":10,"chargeUsdMicros":2580000}}}}`
	post := func(key, payload string) *httptest.ResponseRecorder {
		t.Helper()
		req := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(payload))
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}

	if rec := post("workspace-launch-alpha:purchase-receipt", body); rec.Code != http.StatusCreated {
		t.Fatalf("valid Workspace purchase receipt status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := post("workspace-launch-alpha:purchase-receipt", strings.Replace(body, `"totalUsdMicros":52580000`, `"totalUsdMicros":52579999`, 1)); rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatched Workspace purchase receipt status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := post("workspace-launch-alpha:purchase-receipt", strings.Replace(body, `"resourceId":"workspace-alpha"`, `"resourceId":"workspace-other"`, 1)); rec.Code != http.StatusBadRequest {
		t.Fatalf("cross-Workspace purchase receipt status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWorkspaceLifecycleReceiptCanonicalIdempotencyAndCardinalityHTTP(t *testing.T) {
	post := func(t *testing.T, server http.Handler, input ledger.ReceiptInput, key string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		req := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewReader(body))
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}

	for _, input := range []ledger.ReceiptInput{
		workspaceLifecycleHTTPReceiptInput("workspace.created"),
		workspaceLifecycleHTTPReceiptInput("workspace.deleted.v1"),
	} {
		t.Run(input.Type+" wrong key", func(t *testing.T) {
			store := ledger.NewMemoryStore()
			rec := post(t, NewServer(store, "internal-secret"), input, input.RequestID+":other-receipt")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("wrong canonical key status=%d body=%s", rec.Code, rec.Body.String())
			}
			page, err := store.ListReceipts(context.Background(), ledger.ReceiptQuery{AccountID: input.AccountID})
			if err != nil || len(page.Receipts) != 0 {
				t.Fatalf("wrong key persisted receipts=%#v err=%v", page.Receipts, err)
			}
		})
	}

	t.Run("same RequestID cannot use another header key", func(t *testing.T) {
		store := ledger.NewMemoryStore()
		server := NewServer(store, "internal-secret")
		input := workspaceLifecycleHTTPReceiptInput("workspace.created")
		if rec := post(t, server, input, input.RequestID+":purchase-receipt"); rec.Code != http.StatusCreated {
			t.Fatalf("first receipt status=%d body=%s", rec.Code, rec.Body.String())
		}
		if rec := post(t, server, input, input.RequestID+":second-purchase-receipt"); rec.Code != http.StatusBadRequest {
			t.Fatalf("alternate header key status=%d body=%s", rec.Code, rec.Body.String())
		}
		page, err := store.ListReceipts(context.Background(), ledger.ReceiptQuery{AccountID: input.AccountID})
		if err != nil || len(page.Receipts) != 1 {
			t.Fatalf("same RequestID receipts=%#v err=%v", page.Receipts, err)
		}
	})

	for _, firstType := range []string{"billing.workspace_purchased.v1", "workspace.created"} {
		t.Run("mixed launch variants after "+firstType, func(t *testing.T) {
			store := ledger.NewMemoryStore()
			server := NewServer(store, "internal-secret")
			first := workspaceLifecycleHTTPReceiptInput(firstType)
			secondType := "workspace.created"
			if firstType == secondType {
				secondType = "billing.workspace_purchased.v1"
			}
			second := workspaceLifecycleHTTPReceiptInput(secondType)
			key := first.RequestID + ":purchase-receipt"
			if rec := post(t, server, first, key); rec.Code != http.StatusCreated {
				t.Fatalf("first launch receipt status=%d body=%s", rec.Code, rec.Body.String())
			}
			if rec := post(t, server, second, key); rec.Code != http.StatusConflict {
				t.Fatalf("mixed launch receipt status=%d body=%s", rec.Code, rec.Body.String())
			}
			page, err := store.ListReceipts(context.Background(), ledger.ReceiptQuery{AccountID: first.AccountID})
			if err != nil || len(page.Receipts) != 1 {
				t.Fatalf("mixed launch receipts=%#v err=%v", page.Receipts, err)
			}
		})
	}
}

func TestReceiptHTTPPreservesLargeIntegerCost(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	req := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(`{"type":"billing.reconciliation.v1","status":"completed","surface":"control_plane","workspaceId":"workspace-alpha","cost":{"pricingVersion":"pricing-v1","monthlyPriceCnyCents":9007199254740993,"chargeUsdMicros":50000000,"sub2apiUserId":41,"sub2apiRedeemCode":"opl:test:billing-alpha:charge:v1","periodStart":"2026-07-01T00:00:00Z","paidThrough":"2026-08-01T00:00:00Z","resourceType":"compute","resourceId":"compute-alpha"}}`))
	req.Header.Set("Idempotency-Key", "http-large-integer-cost")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("large integer receipt status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"monthlyPriceCnyCents":9007199254740993`) {
		t.Fatalf("large integer receipt changed: %s", rec.Body.String())
	}
	var created struct {
		ReceiptID string `json:"receiptId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	read := httptest.NewRecorder()
	server.ServeHTTP(read, testRequest(http.MethodGet, "/ledger/receipts/"+created.ReceiptID, nil))
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"monthlyPriceCnyCents":9007199254740993`) {
		t.Fatalf("persisted large integer status=%d body=%s", read.Code, read.Body.String())
	}
}

func TestReconciliationHTTPPreservesInt64Boundary(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	post := func(key, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := testRequest(http.MethodPost, "/ledger/reconciliation", bytes.NewBufferString(body))
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	maxInt64 := `{"report":{"id":"recon-max-int64","status":"ok","counts":{"billingOperations":9223372036854775807,"matched":9223372036854775807,"exceptions":0},"exceptions":[]}}`
	for _, rec := range []*httptest.ResponseRecorder{post("recon-max-int64", maxInt64), post("recon-max-int64", maxInt64)} {
		if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"billingOperations":9223372036854775807`) {
			t.Fatalf("MaxInt64 reconciliation status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	overflow := post("recon-overflow", `{"report":{"id":"recon-overflow","status":"ok","counts":{"billingOperations":9223372036854775808,"matched":9223372036854775808,"exceptions":0},"exceptions":[]}}`)
	if overflow.Code != http.StatusBadRequest {
		t.Fatalf("overflow reconciliation status=%d body=%s", overflow.Code, overflow.Body.String())
	}
	for name, body := range map[string]string{
		"fraction":   `{"report":{"id":"recon-fraction","status":"ok","counts":{"billingOperations":1.0,"matched":1.0,"exceptions":0},"exceptions":[]}}`,
		"scientific": `{"report":{"id":"recon-scientific","status":"ok","counts":{"billingOperations":1e3,"matched":1e3,"exceptions":0},"exceptions":[]}}`,
	} {
		if rec := post("recon-"+name, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s reconciliation status=%d body=%s", name, rec.Code, rec.Body.String())
		}
	}
	firstLarge := post("recon-distinct-large-integers", `{"report":{"id":"recon-distinct-large-integers","status":"ok","counts":{"billingOperations":9007199254740992,"matched":9007199254740992,"exceptions":0},"exceptions":[]}}`)
	if firstLarge.Code != http.StatusCreated || !strings.Contains(firstLarge.Body.String(), `"billingOperations":9007199254740992`) {
		t.Fatalf("first large reconciliation status=%d body=%s", firstLarge.Code, firstLarge.Body.String())
	}
	secondLarge := post("recon-distinct-large-integers", `{"report":{"id":"recon-distinct-large-integers","status":"ok","counts":{"billingOperations":9007199254740993,"matched":9007199254740993,"exceptions":0},"exceptions":[]}}`)
	if secondLarge.Code != http.StatusConflict {
		t.Fatalf("distinct large reconciliation status=%d body=%s", secondLarge.Code, secondLarge.Body.String())
	}
}

func TestReceiptRejectsSensitiveHTTP(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	req := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(`{"type":"execution.receipt.v1","status":"completed","surface":"workspace","workspaceId":"workspace-alpha","outputRefs":{"nested":[{"RAWPROVIDERRESPONSE":{"credential":"must-not-persist"}}]}}`))
	req.Header.Set("Idempotency-Key", "http-sensitive-receipt")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "must-not-persist") {
		t.Fatalf("sensitive receipt status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestReconciliationSchemaHTTP(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	req := testRequest(http.MethodPost, "/ledger/reconciliation", bytes.NewBufferString(`{"report":{"id":"recon-alpha","status":"ok"}}`))
	req.Header.Set("Idempotency-Key", "http-invalid-reconciliation")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid reconciliation status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func testRequest(method, path string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Authorization", "Bearer internal-secret")
	return req
}

func TestReceiptRetentionAndPrivacyHTTP(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	create := func(key, body string) ledger.Receipt {
		t.Helper()
		req := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(body))
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create receipt status = %d: %s", rec.Code, rec.Body.String())
		}
		var receipt ledger.Receipt
		if err := json.NewDecoder(rec.Body).Decode(&receipt); err != nil {
			t.Fatal(err)
		}
		return receipt
	}
	seeded := create("http-retention-seed", `{"type":"execution.receipt.v1","status":"completed","surface":"workspace","workspaceId":"workspace-retention","actor":{"email":"person@example.test"},"retention":{"legalHold":true,"privacyRedaction":{"eligible":true,"reason":"caller supplied"}}}`)
	if seeded.Retention.LegalHold || seeded.Retention.PrivacyRedaction != nil {
		t.Fatalf("receipt create accepted caller retention = %#v", seeded.Retention)
	}

	retention := testRequest(http.MethodPost, "/ledger/receipts/"+seeded.ReceiptID+"/retention", bytes.NewBufferString(`{"retainUntil":"2099-01-02T03:04:05Z","legalHold":true}`))
	retention.Header.Set("Idempotency-Key", "http-retention-update")
	retentionRec := httptest.NewRecorder()
	server.ServeHTTP(retentionRec, retention)
	if retentionRec.Code != http.StatusOK {
		t.Fatalf("retention status = %d: %s", retentionRec.Code, retentionRec.Body.String())
	}
	detailRec := httptest.NewRecorder()
	server.ServeHTTP(detailRec, testRequest(http.MethodGet, "/ledger/receipts/"+seeded.ReceiptID, nil))
	if detailRec.Code != http.StatusOK || !strings.Contains(detailRec.Body.String(), `"retainUntil":"2099-01-02T03:04:05Z"`) || !strings.Contains(detailRec.Body.String(), `"legalHold":true`) {
		t.Fatalf("receipt detail status = %d: %s", detailRec.Code, detailRec.Body.String())
	}

	privacy := create("http-privacy-seed", `{"type":"execution.receipt.v1","status":"completed","surface":"workspace","organizationId":"org-privacy","workspaceId":"workspace-privacy","projectId":"project-privacy","taskId":"task-privacy","jobId":"job-privacy","continuationId":"continuation-privacy","actor":{"email":"person@example.test"},"owner":{"name":"Person"},"environment":{"environmentRef":"env-alpha"},"inputRefs":{"digest":"sha256:input"},"outputRefs":{"digest":"sha256:output"},"continuation":{"freeForm":"personal note"}}`)
	privacyReq := testRequest(http.MethodPost, "/ledger/receipts/"+privacy.ReceiptID+"/privacy-delete", bytes.NewBufferString(`{"reason":"verified account deletion"}`))
	privacyReq.Header.Set("Idempotency-Key", "http-privacy-delete")
	privacyRec := httptest.NewRecorder()
	server.ServeHTTP(privacyRec, privacyReq)
	if privacyRec.Code != http.StatusOK {
		t.Fatalf("privacy delete status = %d: %s", privacyRec.Code, privacyRec.Body.String())
	}
	var redaction ledger.ReceiptRetentionResult
	if err := json.NewDecoder(privacyRec.Body).Decode(&redaction); err != nil {
		t.Fatal(err)
	}
	redactedRec := httptest.NewRecorder()
	server.ServeHTTP(redactedRec, testRequest(http.MethodGet, "/ledger/receipts/"+privacy.ReceiptID, nil))
	var redacted ledger.Receipt
	if err := json.NewDecoder(redactedRec.Body).Decode(&redacted); err != nil {
		t.Fatal(err)
	}
	if redacted.Actor != nil || redacted.Owner != nil || redacted.Continuation != nil || redacted.Environment["environmentRef"] != "env-alpha" || redacted.InputRefs["digest"] != "sha256:input" || redacted.OutputRefs["digest"] != "sha256:output" || redaction.Retention.PrivacyRedaction == nil {
		t.Fatalf("privacy boundary = %#v", redacted)
	}
}

func TestReceiptHTTPRejectsContinuationWithoutFullIdentity(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	req := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(`{"type":"workspace.created","status":"completed","surface":"workspace","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","jobId":"job-alpha","continuation":{"continuationId":"continuation-alpha"}}`))
	req.Header.Set("Idempotency-Key", "invalid-legacy-continuation")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "continuation-alpha") {
		t.Fatalf("invalid continuation response = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestReceiptListHTTPIsAuthenticatedFilteredAndPaginated(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	for i, body := range []string{
		`{"type":"execution.receipt.v1","status":"completed","surface":"workspace","organizationId":"org-alpha","workspaceId":"ws-alpha","projectId":"project-alpha","taskId":"task-alpha","jobId":"job-alpha"}`,
		`{"type":"execution.receipt.v1","status":"completed","surface":"workspace","organizationId":"org-alpha","workspaceId":"ws-alpha","projectId":"project-alpha","taskId":"task-alpha","jobId":"job-alpha"}`,
		`{"type":"execution.receipt.v1","status":"failed","surface":"workspace","organizationId":"org-other","workspaceId":"ws-alpha"}`,
	} {
		req := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(body))
		req.Header.Set("Idempotency-Key", fmt.Sprintf("list-receipt-%d", i))
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %d status = %d: %s", i, rec.Code, rec.Body.String())
		}
	}

	path := "/ledger/receipts?organizationId=org-alpha&workspaceId=ws-alpha&projectId=project-alpha&taskId=task-alpha&jobId=job-alpha&type=execution.receipt.v1&status=completed&limit=1"
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, testRequest(http.MethodGet, path, nil))
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first status = %d: %s", firstRec.Code, firstRec.Body.String())
	}
	var first ledger.ReceiptPage
	if err := json.NewDecoder(firstRec.Body).Decode(&first); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if len(first.Receipts) != 1 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page = %#v", first)
	}
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, testRequest(http.MethodGet, path+"&cursor="+url.QueryEscape(first.NextCursor), nil))
	var second ledger.ReceiptPage
	if err := json.NewDecoder(secondRec.Body).Decode(&second); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if secondRec.Code != http.StatusOK || len(second.Receipts) != 1 || second.HasMore || second.Receipts[0].ReceiptID == first.Receipts[0].ReceiptID {
		t.Fatalf("second status/page = %d %#v", secondRec.Code, second)
	}

	anonymous := httptest.NewRecorder()
	server.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/ledger/receipts", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", anonymous.Code)
	}
}

func TestReceiptListHTTPRejectsInvalidPagination(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	for _, path := range []string{"/ledger/receipts?limit=0", "/ledger/receipts?limit=101", "/ledger/receipts?cursor=invalid"} {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, testRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400: %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestReceiptHTTPPreservesOpaqueProvenance(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	body := `{"type":"execution.receipt.v1","status":"completed","surface":"workspace","organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","jobId":"job-alpha","artifactId":"artifact-alpha","reviewId":"review-alpha","outputRefs":{"digest":"sha256:output"},"reviewerChecks":{"decision":"accepted"},"continuationId":"continuation-alpha","continuation":{"taskVersion":2,"freeForm":"opaque"}}`
	req := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(body))
	req.Header.Set("Idempotency-Key", "http-opaque-provenance")
	createdRec := httptest.NewRecorder()
	server.ServeHTTP(createdRec, req)
	if createdRec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createdRec.Code, createdRec.Body.String())
	}
	var created ledger.Receipt
	if err := json.NewDecoder(createdRec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	assertProvenance := func(receipt ledger.Receipt) {
		t.Helper()
		if receipt.ArtifactID != "artifact-alpha" || receipt.ReviewID != "review-alpha" || receipt.OutputRefs["digest"] != "sha256:output" || receipt.ReviewerChecks["decision"] != "accepted" || receipt.ContinuationID != "continuation-alpha" || receipt.Continuation["freeForm"] != "opaque" {
			t.Fatalf("opaque provenance changed: %#v", receipt)
		}
	}
	assertProvenance(created)

	detailRec := httptest.NewRecorder()
	server.ServeHTTP(detailRec, testRequest(http.MethodGet, "/ledger/receipts/"+created.ReceiptID, nil))
	if detailRec.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detailRec.Code, detailRec.Body.String())
	}
	var detail ledger.Receipt
	if err := json.NewDecoder(detailRec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	assertProvenance(detail)

	listRec := httptest.NewRecorder()
	server.ServeHTTP(listRec, testRequest(http.MethodGet, "/ledger/receipts?workspaceId=workspace-alpha&jobId=job-alpha", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var page ledger.ReceiptPage
	if err := json.NewDecoder(listRec.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Receipts) != 1 {
		t.Fatalf("list page=%#v", page)
	}
	assertProvenance(page.Receipts[0])
}
