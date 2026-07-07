package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestHoldAndEvidenceHTTP(t *testing.T) {
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

	evidence := httptest.NewRequest(http.MethodPost, "/ledger/evidence", bytes.NewBufferString(`{"workspaceId":"ws-alpha","providerRequestId":"runtime-req-alpha","redactedUrl":"https://workspace.medopl.cn/w/ws-alpha/","tokenVersion":"v1"}`))
	evidence.Header.Set("Idempotency-Key", "http-evidence-once")
	evidenceRec := httptest.NewRecorder()
	server.ServeHTTP(evidenceRec, evidence)
	if evidenceRec.Code != http.StatusCreated {
		t.Fatalf("evidence status = %d, want %d: %s", evidenceRec.Code, http.StatusCreated, evidenceRec.Body.String())
	}
	var evidenceBody map[string]any
	if err := json.NewDecoder(evidenceRec.Body).Decode(&evidenceBody); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if evidenceBody["workspaceId"] != "ws-alpha" || evidenceBody["providerRequestId"] != "runtime-req-alpha" {
		t.Fatalf("unexpected evidence body: %#v", evidenceBody)
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
