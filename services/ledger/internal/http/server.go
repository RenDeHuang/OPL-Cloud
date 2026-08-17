package http

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"opl-cloud/services/ledger/internal/ledger"
)

func NewServer(store ledger.Store, token string) http.Handler {
	return NewServerWithAuth(store, token, "")
}

func NewServerWithAuth(store ledger.Store, token, capabilityKey string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		readiness, ok := store.(ledger.ReadinessStore)
		if !ok || readiness.Ready(r.Context()) != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("POST /ledger/receipts", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		var input ledger.ReceiptInput
		if err := decodeJSONBody(r, &input); err != nil {
			writeJSONBodyError(w, err)
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
	mux.HandleFunc("GET /ledger/receipts", func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		query := ledger.ReceiptQuery{
			AccountID:      values.Get("accountId"),
			OrganizationID: values.Get("organizationId"),
			WorkspaceID:    values.Get("workspaceId"),
			ProjectID:      values.Get("projectId"),
			TaskID:         values.Get("taskId"),
			JobID:          values.Get("jobId"),
			Type:           values.Get("type"),
			TypePrefix:     values.Get("typePrefix"),
			Status:         values.Get("status"),
			Cursor:         values.Get("cursor"),
		}
		if rawLimit := values.Get("limit"); rawLimit != "" {
			limit, err := strconv.Atoi(rawLimit)
			if err != nil || limit < 1 || limit > ledger.MaxReceiptPageSize {
				writeError(w, http.StatusBadRequest, ledger.ErrInvalidReceiptQuery.Error())
				return
			}
			query.Limit = limit
		}
		result, err := store.ListReceipts(r.Context(), query)
		if errors.Is(err, ledger.ErrInvalidReceiptQuery) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "receipt list failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
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
	mux.HandleFunc("POST /ledger/receipts/{id}/retention", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		var input ledger.ReceiptRetentionInput
		if err := decodeJSONBody(r, &input); err != nil {
			writeJSONBodyError(w, err)
			return
		}
		input.ReceiptID = r.PathValue("id")
		input.IdempotencyKey = idempotencyKey
		result, err := store.UpdateReceiptRetention(r.Context(), input)
		switch {
		case errors.Is(err, ledger.ErrInvalidReceiptRetentionInput):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, ledger.ErrReceiptNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, ledger.ErrIdempotencyConflict), errors.Is(err, ledger.ErrReceiptRetentionShortening), errors.Is(err, ledger.ErrReceiptLegalHold):
			writeError(w, http.StatusConflict, err.Error())
		case err != nil:
			writeError(w, http.StatusInternalServerError, "receipt retention update failed")
		default:
			writeJSON(w, http.StatusOK, result)
		}
	})
	mux.HandleFunc("POST /ledger/receipts/{id}/privacy-delete", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		var input ledger.ReceiptPrivacyDeleteInput
		if err := decodeJSONBody(r, &input); err != nil {
			writeJSONBodyError(w, err)
			return
		}
		input.ReceiptID = r.PathValue("id")
		input.IdempotencyKey = idempotencyKey
		result, err := store.PrivacyDeleteReceipt(r.Context(), input)
		switch {
		case errors.Is(err, ledger.ErrInvalidReceiptRetentionInput):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, ledger.ErrReceiptNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, ledger.ErrIdempotencyConflict), errors.Is(err, ledger.ErrReceiptRetentionActive), errors.Is(err, ledger.ErrReceiptLegalHold):
			writeError(w, http.StatusConflict, err.Error())
		case err != nil:
			writeError(w, http.StatusInternalServerError, "receipt privacy delete failed")
		default:
			writeJSON(w, http.StatusOK, result)
		}
	})
	mux.HandleFunc("POST /ledger/reconciliation", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		var input ledger.ReconciliationInput
		if err := decodeJSONBody(r, &input); err != nil {
			writeJSONBodyError(w, err)
			return
		}
		input.IdempotencyKey = idempotencyKey
		result, err := store.RecordReconciliation(r.Context(), input)
		if errors.Is(err, ledger.ErrInvalidReconciliationInput) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
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
	return authorizeLedgerRequests(mux, store, token, capabilityKey)
}

const maxJSONBodyBytes int64 = 1 << 20

var errJSONBodyTooLarge = errors.New("JSON body too large")

func decodeJSONBody(r *http.Request, target any) error {
	payload, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBodyBytes+1))
	if err != nil {
		return err
	}
	if int64(len(payload)) > maxJSONBodyBytes {
		return errJSONBodyTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("JSON body contains multiple values")
	}
	return nil
}

func writeJSONBodyError(w http.ResponseWriter, err error) {
	if errors.Is(err, errJSONBodyTooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid JSON body")
}

func authenticate(next http.Handler, token string) http.Handler {
	want := sha256.Sum256([]byte("Bearer " + token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") {
			next.ServeHTTP(w, r)
			return
		}
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if token == "" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func authorizeLedgerRequests(next http.Handler, store ledger.Store, token, capabilityKey string) http.Handler {
	return authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capabilityKey == "" || (r.Method == http.MethodGet && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz")) {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBodyBytes+1))
		if err != nil || int64(len(body)) > maxJSONBodyBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		scope, ok := ledgerCapabilityScopeForRequest(r, body)
		if !ok {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		claims, ok := preverifyLedgerCapability(r.Header.Get(ledgerCapabilityHeader), capabilityKey, scope, body, time.Now().UTC())
		if !ok {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		if err := enrichLedgerOwnerScope(r, store, &scope); err != nil {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		if !ledgerOwnerClaimsMatch(claims, scope) {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	}), token)
}

func ledgerOwnerClaimsMatch(claims ledgerCapabilityClaims, scope ledgerCapabilityScope) bool {
	if claims.Action == "read_receipt" {
		return claims.AccountID != "" && claims.AccountID == scope.AccountID &&
			(claims.WorkspaceID == "" || claims.WorkspaceID == scope.WorkspaceID)
	}
	return claims.AccountID == scope.AccountID && claims.WorkspaceID == scope.WorkspaceID
}

func enrichLedgerOwnerScope(r *http.Request, store ledger.Store, scope *ledgerCapabilityScope) error {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "ledger" {
		return nil
	}
	var accountID, workspaceID string
	switch {
	case parts[1] == "receipts" && len(parts) >= 3:
		result, err := store.Receipt(r.Context(), parts[2])
		if err != nil {
			return err
		}
		accountID, workspaceID = result.AccountID, result.WorkspaceID
	}
	if accountID != "" && scope.AccountID != "" && accountID != scope.AccountID {
		return errors.New("owner mismatch")
	}
	if workspaceID != "" && scope.WorkspaceID != "" && workspaceID != scope.WorkspaceID {
		return errors.New("owner mismatch")
	}
	if accountID != "" {
		scope.AccountID = accountID
	}
	if workspaceID != "" {
		scope.WorkspaceID = workspaceID
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
