package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"opl-cloud/services/ledger/internal/ledger"
)

func NewServer(store ledger.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /ledger/topups", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		var input ledger.ManualTopUpInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		input.IdempotencyKey = idempotencyKey
		result, err := store.ManualTopUp(r.Context(), input)
		if errors.Is(err, ledger.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "manual top-up failed")
			return
		}
		writeJSON(w, http.StatusCreated, result)
	})
	mux.HandleFunc("GET /ledger/accounts/{accountId}/wallet", func(w http.ResponseWriter, r *http.Request) {
		accountID := strings.TrimSpace(r.PathValue("accountId"))
		if accountID == "" {
			writeError(w, http.StatusBadRequest, "missing account id")
			return
		}
		wallet, err := store.Wallet(r.Context(), accountID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "wallet lookup failed")
			return
		}
		writeJSON(w, http.StatusOK, wallet)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
