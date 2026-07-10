package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opl-cloud/services/ledger/internal/ledger"
)

func TestTopUpRequiresIdempotencyKey(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore())
	body := bytes.NewBufferString(`{"accountId":"acct-alpha","amountCents":1000,"currency":"CNY","operatorUserId":"usr-admin"}`)
	req := httptest.NewRequest(http.MethodPost, "/ledger/topups", body)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestTopUpAndWalletHTTP(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore())
	body := bytes.NewBufferString(`{"accountId":"acct-alpha","amountCents":1000,"currency":"CNY","operatorUserId":"usr-admin","reason":"operator_credit"}`)
	req := httptest.NewRequest(http.MethodPost, "/ledger/topups", body)
	req.Header.Set("Idempotency-Key", "http-topup-once")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("topup status = %d, want %d", rec.Code, http.StatusCreated)
	}

	walletReq := httptest.NewRequest(http.MethodGet, "/ledger/accounts/acct-alpha/wallet", nil)
	walletRec := httptest.NewRecorder()
	server.ServeHTTP(walletRec, walletReq)

	if walletRec.Code != http.StatusOK {
		t.Fatalf("wallet status = %d, want %d", walletRec.Code, http.StatusOK)
	}
	var wallet ledger.Wallet
	if err := json.NewDecoder(walletRec.Body).Decode(&wallet); err != nil {
		t.Fatalf("decode wallet: %v", err)
	}
	if wallet.BalanceCents != 1000 {
		t.Fatalf("balance = %d, want 1000", wallet.BalanceCents)
	}
}

func TestHoldAndReceiptHTTP(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore())
	topup := httptest.NewRequest(http.MethodPost, "/ledger/topups", bytes.NewBufferString(`{"accountId":"acct-alpha","amountCents":2000,"currency":"CNY","operatorUserId":"usr-admin","reason":"operator_credit"}`))
	topup.Header.Set("Idempotency-Key", "http-hold-topup")
	topupRec := httptest.NewRecorder()
	server.ServeHTTP(topupRec, topup)
	if topupRec.Code != http.StatusCreated {
		t.Fatalf("topup status = %d, want %d: %s", topupRec.Code, http.StatusCreated, topupRec.Body.String())
	}

	hold := httptest.NewRequest(http.MethodPost, "/ledger/holds", bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","resourceType":"compute","resourceId":"compute-alpha","amountCents":1000,"currency":"CNY"}`))
	hold.Header.Set("Idempotency-Key", "http-hold-once")
	holdRec := httptest.NewRecorder()
	server.ServeHTTP(holdRec, hold)
	if holdRec.Code != http.StatusCreated {
		t.Fatalf("hold status = %d, want %d: %s", holdRec.Code, http.StatusCreated, holdRec.Body.String())
	}
	var holdBody map[string]any
	if err := json.NewDecoder(holdRec.Body).Decode(&holdBody); err != nil {
		t.Fatalf("decode hold: %v", err)
	}
	if holdBody["accountId"] != "acct-alpha" || holdBody["workspaceId"] != "ws-alpha" || holdBody["resourceType"] != "compute" || holdBody["resourceId"] != "compute-alpha" || holdBody["status"] != "held" {
		t.Fatalf("unexpected hold body: %#v", holdBody)
	}

	receipt := httptest.NewRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(`{"type":"workspace.created","status":"completed","surface":"workspace","organizationId":"org-alpha","workspaceId":"ws-alpha","projectId":"project-alpha","taskId":"task-alpha","jobId":"job-alpha","execution":{"providerRequestId":"runtime-req-alpha"},"outputRefs":{"redactedUrl":"https://workspace.medopl.cn/w/ws-alpha/"},"continuation":{"continuationId":"continuation-alpha"}}`))
	receipt.Header.Set("Idempotency-Key", "http-receipt-once")
	receiptRec := httptest.NewRecorder()
	server.ServeHTTP(receiptRec, receipt)
	if receiptRec.Code != http.StatusCreated {
		t.Fatalf("receipt status = %d, want %d: %s", receiptRec.Code, http.StatusCreated, receiptRec.Body.String())
	}
	var receiptBody map[string]any
	if err := json.NewDecoder(receiptRec.Body).Decode(&receiptBody); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if receiptBody["workspaceId"] != "ws-alpha" || receiptBody["projectId"] != "project-alpha" || receiptBody["status"] != "completed" {
		t.Fatalf("unexpected receipt body: %#v", receiptBody)
	}

	getReceipt := httptest.NewRequest(http.MethodGet, "/ledger/receipts/"+receiptBody["receiptId"].(string), nil)
	getReceiptRec := httptest.NewRecorder()
	server.ServeHTTP(getReceiptRec, getReceipt)
	if getReceiptRec.Code != http.StatusOK {
		t.Fatalf("get receipt status = %d, want %d: %s", getReceiptRec.Code, http.StatusOK, getReceiptRec.Body.String())
	}

	legacy := httptest.NewRequest(http.MethodPost, "/ledger/evidence", bytes.NewBufferString(`{}`))
	legacyRec := httptest.NewRecorder()
	server.ServeHTTP(legacyRec, legacy)
	if legacyRec.Code != http.StatusNotFound {
		t.Fatalf("legacy evidence route status = %d, want %d", legacyRec.Code, http.StatusNotFound)
	}
}

func TestContinuationHTTP(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore())
	receipt := httptest.NewRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(`{"type":"execution.receipt.v1","status":"completed","surface":"workspace","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","continuation":{"continuationId":"continuation-alpha","taskVersion":2}}`))
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

	req := httptest.NewRequest(http.MethodGet, "/ledger/receipts/"+receiptBody["receiptId"].(string)+"/continuation", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("continuation status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode continuation: %v", err)
	}
	if body["continuationId"] != "continuation-alpha" || body["receiptId"] != receiptBody["receiptId"] || body["projectId"] != "project-alpha" || body["taskId"] != "task-alpha" {
		t.Fatalf("unexpected continuation: %#v", body)
	}
}

func TestContinuationHTTPReturnsNotFoundWhenReceiptHasNone(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore())
	receipt := httptest.NewRequest(http.MethodPost, "/ledger/receipts", bytes.NewBufferString(`{"type":"execution.receipt.v1","status":"completed","surface":"workspace","workspaceId":"workspace-alpha"}`))
	receipt.Header.Set("Idempotency-Key", "http-no-continuation-receipt")
	receiptRec := httptest.NewRecorder()
	server.ServeHTTP(receiptRec, receipt)
	var receiptBody map[string]any
	if err := json.NewDecoder(receiptRec.Body).Decode(&receiptBody); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/ledger/receipts/"+receiptBody["receiptId"].(string)+"/continuation", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("continuation status = %d, want %d: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestArtifactAndReviewHTTP(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore())
	artifactReq := httptest.NewRequest(http.MethodPost, "/ledger/artifacts", bytes.NewBufferString(`{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","jobId":"job-alpha","digest":"sha256:abc123","mediaType":"application/json","sizeBytes":42,"storageRef":"storage-artifact-alpha"}`))
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
	server.ServeHTTP(getArtifactRec, httptest.NewRequest(http.MethodGet, "/ledger/artifacts/"+artifact.ArtifactID, nil))
	if getArtifactRec.Code != http.StatusOK {
		t.Fatalf("get artifact status = %d: %s", getArtifactRec.Code, getArtifactRec.Body.String())
	}

	reviewReq := httptest.NewRequest(http.MethodPost, "/ledger/reviews", bytes.NewBufferString(`{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","jobId":"job-alpha","reviewerRef":"reviewer-rca","reviewerVersion":"1.0.0","inputArtifactDigests":["sha256:abc123"],"checks":{"schema":"passed"},"decision":"accepted"}`))
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
	server.ServeHTTP(getReviewRec, httptest.NewRequest(http.MethodGet, "/ledger/reviews/"+review.ReviewID, nil))
	if getReviewRec.Code != http.StatusOK {
		t.Fatalf("get review status = %d: %s", getReviewRec.Code, getReviewRec.Body.String())
	}
}

func TestEvidenceHTTPMapsInputNotFoundAndConflict(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore())
	invalidReq := httptest.NewRequest(http.MethodPost, "/ledger/artifacts", bytes.NewBufferString(`{"workspaceId":"workspace-alpha","storageRef":"https://example.test/result?token=secret"}`))
	invalidReq.Header.Set("Idempotency-Key", "invalid-artifact")
	invalidRec := httptest.NewRecorder()
	server.ServeHTTP(invalidRec, invalidReq)
	if invalidRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid artifact status = %d, want %d", invalidRec.Code, http.StatusBadRequest)
	}

	notFoundRec := httptest.NewRecorder()
	server.ServeHTTP(notFoundRec, httptest.NewRequest(http.MethodGet, "/ledger/reviews/missing", nil))
	if notFoundRec.Code != http.StatusNotFound {
		t.Fatalf("missing review status = %d, want %d", notFoundRec.Code, http.StatusNotFound)
	}

	body := `{"workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","jobId":"job-alpha","digest":"sha256:abc123","mediaType":"application/json","sizeBytes":42,"storageRef":"storage-artifact-alpha"}`
	first := httptest.NewRequest(http.MethodPost, "/ledger/artifacts", bytes.NewBufferString(body))
	first.Header.Set("Idempotency-Key", "conflicting-artifact")
	server.ServeHTTP(httptest.NewRecorder(), first)
	second := httptest.NewRequest(http.MethodPost, "/ledger/artifacts", bytes.NewBufferString(strings.Replace(body, "abc123", "different", 1)))
	second.Header.Set("Idempotency-Key", "conflicting-artifact")
	conflictRec := httptest.NewRecorder()
	server.ServeHTTP(conflictRec, second)
	if conflictRec.Code != http.StatusConflict {
		t.Fatalf("conflicting artifact status = %d, want %d: %s", conflictRec.Code, http.StatusConflict, conflictRec.Body.String())
	}
}

func TestReleaseHoldHTTP(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore())
	topup := httptest.NewRequest(http.MethodPost, "/ledger/topups", bytes.NewBufferString(`{"accountId":"acct-alpha","amountCents":2000,"currency":"CNY","operatorUserId":"usr-admin","reason":"operator_credit"}`))
	topup.Header.Set("Idempotency-Key", "http-release-topup")
	topupRec := httptest.NewRecorder()
	server.ServeHTTP(topupRec, topup)
	if topupRec.Code != http.StatusCreated {
		t.Fatalf("topup status = %d, want %d: %s", topupRec.Code, http.StatusCreated, topupRec.Body.String())
	}

	hold := httptest.NewRequest(http.MethodPost, "/ledger/holds", bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","resourceType":"compute","resourceId":"compute-alpha","amountCents":1000,"currency":"CNY"}`))
	hold.Header.Set("Idempotency-Key", "http-release-hold")
	holdRec := httptest.NewRecorder()
	server.ServeHTTP(holdRec, hold)
	if holdRec.Code != http.StatusCreated {
		t.Fatalf("hold status = %d, want %d: %s", holdRec.Code, http.StatusCreated, holdRec.Body.String())
	}
	var holdBody ledger.HoldResult
	if err := json.NewDecoder(holdRec.Body).Decode(&holdBody); err != nil {
		t.Fatalf("decode hold: %v", err)
	}

	release := httptest.NewRequest(http.MethodPost, "/ledger/holds/release", bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","resourceType":"compute","resourceId":"compute-alpha","holdId":"`+holdBody.ID+`","amountCents":600,"currency":"CNY","reason":"destroy_compute"}`))
	release.Header.Set("Idempotency-Key", "http-release-once")
	releaseRec := httptest.NewRecorder()
	server.ServeHTTP(releaseRec, release)
	if releaseRec.Code != http.StatusCreated {
		t.Fatalf("release status = %d, want %d: %s", releaseRec.Code, http.StatusCreated, releaseRec.Body.String())
	}
	var releaseBody ledger.HoldReleaseResult
	if err := json.NewDecoder(releaseRec.Body).Decode(&releaseBody); err != nil {
		t.Fatalf("decode release: %v", err)
	}
	if releaseBody.Status != "released" || releaseBody.Wallet.BalanceCents != 2000 || releaseBody.Wallet.FrozenCents != 400 || releaseBody.Wallet.AvailableCents != 1600 {
		t.Fatalf("unexpected release body: %#v", releaseBody)
	}

	walletReq := httptest.NewRequest(http.MethodGet, "/ledger/accounts/acct-alpha/wallet", nil)
	walletRec := httptest.NewRecorder()
	server.ServeHTTP(walletRec, walletReq)
	var wallet ledger.Wallet
	if err := json.NewDecoder(walletRec.Body).Decode(&wallet); err != nil {
		t.Fatalf("decode wallet: %v", err)
	}
	if wallet.BalanceCents != 2000 || wallet.FrozenCents != 400 || wallet.AvailableCents != 1600 {
		t.Fatalf("unexpected wallet: %#v", wallet)
	}
}

func TestSettlementAndReconciliationHTTP(t *testing.T) {
	server := NewServer(ledger.NewMemoryStore())
	topup := httptest.NewRequest(http.MethodPost, "/ledger/topups", bytes.NewBufferString(`{"accountId":"acct-alpha","amountCents":5000,"currency":"CNY","operatorUserId":"usr-admin"}`))
	topup.Header.Set("Idempotency-Key", "http-settlement-topup")
	topupRec := httptest.NewRecorder()
	server.ServeHTTP(topupRec, topup)
	if topupRec.Code != http.StatusCreated {
		t.Fatalf("topup status = %d, want %d: %s", topupRec.Code, http.StatusCreated, topupRec.Body.String())
	}

	settlement := httptest.NewRequest(http.MethodPost, "/ledger/resource-settlements", bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","amountCents":1200,"currency":"CNY","resourceType":"compute","resourceId":"compute-alpha"}`))
	settlement.Header.Set("Idempotency-Key", "http-settlement-once")
	settlementRec := httptest.NewRecorder()
	server.ServeHTTP(settlementRec, settlement)
	if settlementRec.Code != http.StatusCreated {
		t.Fatalf("settlement status = %d, want %d: %s", settlementRec.Code, http.StatusCreated, settlementRec.Body.String())
	}
	var settlementBody map[string]any
	if err := json.NewDecoder(settlementRec.Body).Decode(&settlementBody); err != nil {
		t.Fatalf("decode settlement: %v", err)
	}
	if settlementBody["accountId"] != "acct-alpha" || settlementBody["resourceType"] != "compute" || settlementBody["status"] != "settled" {
		t.Fatalf("unexpected settlement body: %#v", settlementBody)
	}

	reconciliation := httptest.NewRequest(http.MethodPost, "/ledger/reconciliation", bytes.NewBufferString(`{"report":{"id":"recon-alpha","status":"ok"}}`))
	reconciliation.Header.Set("Idempotency-Key", "http-reconciliation-once")
	reconciliationRec := httptest.NewRecorder()
	server.ServeHTTP(reconciliationRec, reconciliation)
	if reconciliationRec.Code != http.StatusCreated {
		t.Fatalf("reconciliation status = %d, want %d: %s", reconciliationRec.Code, http.StatusCreated, reconciliationRec.Body.String())
	}
	var reconciliationBody map[string]any
	if err := json.NewDecoder(reconciliationRec.Body).Decode(&reconciliationBody); err != nil {
		t.Fatalf("decode reconciliation: %v", err)
	}
	if reconciliationBody["id"] != "recon-alpha" || reconciliationBody["status"] != "ok" {
		t.Fatalf("unexpected reconciliation body: %#v", reconciliationBody)
	}
}
