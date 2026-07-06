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
