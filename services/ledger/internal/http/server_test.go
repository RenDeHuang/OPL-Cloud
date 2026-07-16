package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
		{name: "health wrong method", method: http.MethodPost, path: "/healthz", want: http.StatusUnauthorized},
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

func TestRetiredCommercialRoutesAreAbsent(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/ledger/topups"},
		{http.MethodPost, "/ledger/holds"},
		{http.MethodGet, "/ledger/holds/hold-alpha"},
		{http.MethodPost, "/ledger/holds/activate"},
		{http.MethodPost, "/ledger/holds/release"},
		{http.MethodPost, "/ledger/resource-settlements"},
		{http.MethodGet, "/ledger/accounts/acct-alpha/wallet"},
		{http.MethodGet, "/ledger/entries"},
		{http.MethodGet, "/ledger/wallet-transactions"},
		{http.MethodGet, "/ledger/topups"},
		{http.MethodGet, "/ledger/resource-settlements"},
	} {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, testRequest(tc.method, tc.path, bytes.NewBufferString(`{}`)))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
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

func TestBillingReceiptSchemaHTTP(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	req := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(`{"type":"billing.resource_purchased.v1","status":"completed","surface":"control_plane","workspaceId":"workspace-alpha","cost":{"monthlyPriceCnyCents":35000,"chargeUsdMicros":50000000,"sub2apiUserId":41,"sub2apiRedeemCode":"opl:test:billing-alpha:charge:v1","periodStart":"2026-07-01T00:00:00Z","paidThrough":"2026-08-01T00:00:00Z","resourceType":"compute","resourceId":"compute-alpha"}}`))
	req.Header.Set("Idempotency-Key", "http-invalid-billing-schema")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid billing receipt status=%d body=%s", rec.Code, rec.Body.String())
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

func TestContinuationHTTP(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	receipt := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(`{"type":"execution.receipt.v1","status":"completed","surface":"workspace","organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","jobId":"job-alpha","continuation":{"continuationId":"continuation-alpha","taskVersion":2}}`))
	receipt.Header.Set("Idempotency-Key", "http-continuation-receipt")
	receiptRec := httptest.NewRecorder()
	server.ServeHTTP(receiptRec, receipt)
	if receiptRec.Code != http.StatusCreated {
		t.Fatalf("receipt status = %d, want %d: %s", receiptRec.Code, http.StatusCreated, receiptRec.Body.String())
	}
	var receiptBody map[string]any
	if err := json.NewDecoder(receiptRec.Body).Decode(&receiptBody); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}

	req := testRequest(http.MethodGet, "/ledger/receipts/"+receiptBody["receiptId"].(string)+"/continuation", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("continuation status = %d, want %d: %s", rec.Code, http.StatusConflict, rec.Body.String())
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

func TestContinuationHTTPReturnsNotFoundWhenReceiptHasNone(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	receipt := testRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(`{"type":"execution.receipt.v1","status":"completed","surface":"workspace","workspaceId":"workspace-alpha"}`))
	receipt.Header.Set("Idempotency-Key", "http-no-continuation-receipt")
	receiptRec := httptest.NewRecorder()
	server.ServeHTTP(receiptRec, receipt)
	var receiptBody map[string]any
	if err := json.NewDecoder(receiptRec.Body).Decode(&receiptBody); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}

	req := testRequest(http.MethodGet, "/ledger/receipts/"+receiptBody["receiptId"].(string)+"/continuation", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("continuation status = %d, want %d: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestArtifactAndReviewHTTP(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	artifactReq := testRequest(http.MethodPost, "/ledger/artifacts", bytes.NewBufferString(`{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","jobId":"job-alpha","digest":"sha256:abc123","mediaType":"application/json","sizeBytes":42,"storageRef":"storage-artifact-alpha"}`))
	artifactReq.Header.Set("Idempotency-Key", "http-artifact-once")
	artifactRec := httptest.NewRecorder()
	server.ServeHTTP(artifactRec, artifactReq)
	if artifactRec.Code != http.StatusCreated {
		t.Fatalf("artifact status = %d, want %d: %s", artifactRec.Code, http.StatusCreated, artifactRec.Body.String())
	}
	var artifact ledger.Artifact
	if err := json.NewDecoder(artifactRec.Body).Decode(&artifact); err != nil {
		t.Fatalf("decode artifact: %v", err)
	}
	getArtifactRec := httptest.NewRecorder()
	server.ServeHTTP(getArtifactRec, testRequest(http.MethodGet, "/ledger/artifacts/"+artifact.ArtifactID, nil))
	if getArtifactRec.Code != http.StatusOK {
		t.Fatalf("get artifact status = %d: %s", getArtifactRec.Code, getArtifactRec.Body.String())
	}

	reviewReq := testRequest(http.MethodPost, "/ledger/reviews", bytes.NewBufferString(`{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","jobId":"job-alpha","reviewerRef":"reviewer-rca","reviewerVersion":"1.0.0","inputArtifactDigests":["sha256:abc123"],"checks":{"schema":"passed"},"decision":"accepted"}`))
	reviewReq.Header.Set("Idempotency-Key", "http-review-once")
	reviewRec := httptest.NewRecorder()
	server.ServeHTTP(reviewRec, reviewReq)
	if reviewRec.Code != http.StatusCreated {
		t.Fatalf("review status = %d, want %d: %s", reviewRec.Code, http.StatusCreated, reviewRec.Body.String())
	}
	var review ledger.Review
	if err := json.NewDecoder(reviewRec.Body).Decode(&review); err != nil {
		t.Fatalf("decode review: %v", err)
	}
	getReviewRec := httptest.NewRecorder()
	server.ServeHTTP(getReviewRec, testRequest(http.MethodGet, "/ledger/reviews/"+review.ReviewID, nil))
	if getReviewRec.Code != http.StatusOK {
		t.Fatalf("get review status = %d: %s", getReviewRec.Code, getReviewRec.Body.String())
	}
}

func TestReviewPolicyAndGateHTTP(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	policyReq := testRequest(http.MethodPost, "/ledger/review-policies", bytes.NewBufferString(`{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","jobId":"job-alpha","version":"1","requiredReviewers":[{"reviewerRef":"reviewer-rca","reviewerVersion":"1.0.0"}]}`))
	policyReq.Header.Set("Idempotency-Key", "policy-http")
	policyRec := httptest.NewRecorder()
	server.ServeHTTP(policyRec, policyReq)
	if policyRec.Code != http.StatusCreated {
		t.Fatalf("create policy status = %d body=%s", policyRec.Code, policyRec.Body.String())
	}
	var policy ledger.ReviewPolicy
	if err := json.Unmarshal(policyRec.Body.Bytes(), &policy); err != nil || policy.PolicyID == "" {
		t.Fatalf("decode policy = %#v, %v", policy, err)
	}

	listRec := httptest.NewRecorder()
	server.ServeHTTP(listRec, testRequest(http.MethodGet, "/ledger/review-policies?jobId=job-alpha&status=active", nil))
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), policy.PolicyID) {
		t.Fatalf("list policies status = %d body=%s", listRec.Code, listRec.Body.String())
	}

	gateRec := httptest.NewRecorder()
	server.ServeHTTP(gateRec, testRequest(http.MethodPost, "/ledger/review-gates/evaluate", bytes.NewBufferString(`{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","jobId":"job-alpha","reviewIds":[]}`)))
	if gateRec.Code != http.StatusOK || !strings.Contains(gateRec.Body.String(), `"status":"review_required"`) || !strings.Contains(gateRec.Body.String(), `"continuationEligible":false`) {
		t.Fatalf("evaluate gate status = %d body=%s", gateRec.Code, gateRec.Body.String())
	}
}

func TestEvidenceHTTPMapsInputNotFoundAndConflict(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore(), "internal-secret")
	invalidReq := testRequest(http.MethodPost, "/ledger/artifacts", bytes.NewBufferString(`{"workspaceId":"workspace-alpha","storageRef":"https://example.test/result?token=secret"}`))
	invalidReq.Header.Set("Idempotency-Key", "invalid-artifact")
	invalidRec := httptest.NewRecorder()
	server.ServeHTTP(invalidRec, invalidReq)
	if invalidRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid artifact status = %d, want %d", invalidRec.Code, http.StatusBadRequest)
	}

	notFoundRec := httptest.NewRecorder()
	server.ServeHTTP(notFoundRec, testRequest(http.MethodGet, "/ledger/reviews/missing", nil))
	if notFoundRec.Code != http.StatusNotFound {
		t.Fatalf("missing review status = %d, want %d", notFoundRec.Code, http.StatusNotFound)
	}

	body := `{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","jobId":"job-alpha","digest":"sha256:abc123","mediaType":"application/json","sizeBytes":42,"storageRef":"storage-artifact-alpha"}`
	first := testRequest(http.MethodPost, "/ledger/artifacts", bytes.NewBufferString(body))
	first.Header.Set("Idempotency-Key", "conflicting-artifact")
	server.ServeHTTP(httptest.NewRecorder(), first)
	second := testRequest(http.MethodPost, "/ledger/artifacts", bytes.NewBufferString(strings.Replace(body, "abc123", "different", 1)))
	second.Header.Set("Idempotency-Key", "conflicting-artifact")
	conflictRec := httptest.NewRecorder()
	server.ServeHTTP(conflictRec, second)
	if conflictRec.Code != http.StatusConflict {
		t.Fatalf("conflicting artifact status = %d, want %d: %s", conflictRec.Code, http.StatusConflict, conflictRec.Body.String())
	}
}
