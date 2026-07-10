package http

import (
	"encoding/json"
	"errors"
	"log"
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
			log.Printf("manual top-up failed: %v", err)
			writeError(w, http.StatusInternalServerError, "manual top-up failed")
			return
		}
		writeJSON(w, http.StatusCreated, result)
	})
	mux.HandleFunc("POST /ledger/holds", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		var input ledger.HoldInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		input.IdempotencyKey = idempotencyKey
		result, err := store.CreateHold(r.Context(), input)
		if errors.Is(err, ledger.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, ledger.ErrInsufficientBalance) {
			writeError(w, http.StatusPaymentRequired, err.Error())
			return
		}
		if errors.Is(err, ledger.ErrInvalidHoldInput) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			log.Printf("hold failed: %v", err)
			writeError(w, http.StatusInternalServerError, "hold failed")
			return
		}
		writeJSON(w, http.StatusCreated, result)
	})
	mux.HandleFunc("POST /ledger/holds/release", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		var input ledger.HoldReleaseInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		input.IdempotencyKey = idempotencyKey
		result, err := store.ReleaseHold(r.Context(), input)
		if errors.Is(err, ledger.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, ledger.ErrInsufficientFrozen) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if err != nil {
			log.Printf("hold release failed: %v", err)
			writeError(w, http.StatusInternalServerError, "hold release failed")
			return
		}
		writeJSON(w, http.StatusCreated, result)
	})
	mux.HandleFunc("POST /ledger/receipts", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		var input ledger.ReceiptInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		input.IdempotencyKey = idempotencyKey
		result, err := store.RecordReceipt(r.Context(), input)
		if errors.Is(err, ledger.ErrInvalidReceiptInput) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, ledger.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "receipt failed")
			return
		}
		writeJSON(w, http.StatusCreated, result)
	})
	mux.HandleFunc("GET /ledger/receipts/{id}", func(w http.ResponseWriter, r *http.Request) {
		result, err := store.Receipt(r.Context(), r.PathValue("id"))
		if errors.Is(err, ledger.ErrReceiptNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "receipt query failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /ledger/receipts/{id}/continuation", func(w http.ResponseWriter, r *http.Request) {
		result, err := store.Continuation(r.Context(), r.PathValue("id"))
		if errors.Is(err, ledger.ErrReceiptNotFound) || errors.Is(err, ledger.ErrContinuationNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "continuation query failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /ledger/artifacts", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		var input ledger.ArtifactInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		input.IdempotencyKey = idempotencyKey
		result, err := store.RecordArtifact(r.Context(), input)
		if errors.Is(err, ledger.ErrInvalidArtifactInput) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, ledger.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "artifact failed")
			return
		}
		writeJSON(w, http.StatusCreated, result)
	})
	mux.HandleFunc("GET /ledger/artifacts/{id}", func(w http.ResponseWriter, r *http.Request) {
		result, err := store.Artifact(r.Context(), r.PathValue("id"))
		if errors.Is(err, ledger.ErrArtifactNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "artifact query failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /ledger/reviews", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		var input ledger.ReviewInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		input.IdempotencyKey = idempotencyKey
		result, err := store.RecordReview(r.Context(), input)
		if errors.Is(err, ledger.ErrInvalidReviewInput) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, ledger.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "review failed")
			return
		}
		writeJSON(w, http.StatusCreated, result)
	})
	mux.HandleFunc("GET /ledger/reviews/{id}", func(w http.ResponseWriter, r *http.Request) {
		result, err := store.Review(r.Context(), r.PathValue("id"))
		if errors.Is(err, ledger.ErrReviewNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "review query failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /ledger/resource-settlements", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		var input ledger.ResourceSettlementInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		input.IdempotencyKey = idempotencyKey
		result, err := store.SettleResource(r.Context(), input)
		if errors.Is(err, ledger.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, ledger.ErrInsufficientBalance) {
			writeError(w, http.StatusPaymentRequired, err.Error())
			return
		}
		if err != nil {
			log.Printf("resource settlement failed: %v", err)
			writeError(w, http.StatusInternalServerError, "resource settlement failed")
			return
		}
		writeJSON(w, http.StatusCreated, result)
	})
	mux.HandleFunc("POST /ledger/reconciliation", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		var input ledger.ReconciliationInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		input.IdempotencyKey = idempotencyKey
		result, err := store.RecordReconciliation(r.Context(), input)
		if errors.Is(err, ledger.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "reconciliation failed")
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
	mux.HandleFunc("GET /ledger/entries", func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.ListLedgerEntries(r.Context(), "")
		writeReadResult(w, rows, err)
	})
	mux.HandleFunc("GET /ledger/accounts/{accountId}/entries", func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.ListLedgerEntries(r.Context(), strings.TrimSpace(r.PathValue("accountId")))
		writeReadResult(w, rows, err)
	})
	mux.HandleFunc("GET /ledger/wallet-transactions", func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.ListWalletTransactions(r.Context(), "")
		writeReadResult(w, rows, err)
	})
	mux.HandleFunc("GET /ledger/accounts/{accountId}/wallet-transactions", func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.ListWalletTransactions(r.Context(), strings.TrimSpace(r.PathValue("accountId")))
		writeReadResult(w, rows, err)
	})
	mux.HandleFunc("GET /ledger/topups", func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.ListManualTopUps(r.Context(), "")
		writeReadResult(w, rows, err)
	})
	mux.HandleFunc("GET /ledger/accounts/{accountId}/topups", func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.ListManualTopUps(r.Context(), strings.TrimSpace(r.PathValue("accountId")))
		writeReadResult(w, rows, err)
	})
	mux.HandleFunc("GET /ledger/resource-settlements", func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.ListResourceSettlements(r.Context(), "")
		writeReadResult(w, rows, err)
	})
	mux.HandleFunc("GET /ledger/accounts/{accountId}/resource-settlements", func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.ListResourceSettlements(r.Context(), strings.TrimSpace(r.PathValue("accountId")))
		writeReadResult(w, rows, err)
	})
	return mux
}

func writeReadResult(w http.ResponseWriter, body any, err error) {
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ledger read failed")
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
