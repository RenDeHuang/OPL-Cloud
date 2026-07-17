package http

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"opl-cloud/services/ledger/internal/ledger"
)

func NewServer(store ledger.Store, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /ledger/receipts", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		var input ledger.ReceiptInput
		if err := decodeJSONBody(r, &input); err != nil {
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
			writeError(w, http.StatusBadRequest, "invalid JSON body")
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
			writeError(w, http.StatusBadRequest, "invalid JSON body")
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
	mux.HandleFunc("GET /ledger/receipts/{id}/continuation", func(w http.ResponseWriter, r *http.Request) {
		result, err := store.Continuation(r.Context(), r.PathValue("id"))
		if errors.Is(err, ledger.ErrReceiptNotFound) || errors.Is(err, ledger.ErrContinuationNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if errors.Is(err, ledger.ErrContinuationIneligible) {
			writeError(w, http.StatusConflict, err.Error())
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
		if err := decodeJSONBody(r, &input); err != nil {
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
		if err := decodeJSONBody(r, &input); err != nil {
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
	mux.HandleFunc("POST /ledger/review-policies", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		var input ledger.ReviewPolicyInput
		if err := decodeJSONBody(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		input.IdempotencyKey = idempotencyKey
		result, err := store.CreateReviewPolicy(r.Context(), input)
		if errors.Is(err, ledger.ErrInvalidReviewPolicyInput) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, ledger.ErrReviewPolicyNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if errors.Is(err, ledger.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "review policy failed")
			return
		}
		writeJSON(w, http.StatusCreated, result)
	})
	mux.HandleFunc("GET /ledger/review-policies", func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		result, err := store.ListReviewPolicies(r.Context(), ledger.ReviewPolicyQuery{
			ExecutionIdentity: ledger.ExecutionIdentity{OrganizationID: values.Get("organizationId"), WorkspaceID: values.Get("workspaceId"), ProjectID: values.Get("projectId"), TaskID: values.Get("taskId"), JobID: values.Get("jobId")},
			Status:            values.Get("status"),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "review policy list failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /ledger/review-policies/{id}", func(w http.ResponseWriter, r *http.Request) {
		result, err := store.ReviewPolicy(r.Context(), r.PathValue("id"))
		if errors.Is(err, ledger.ErrReviewPolicyNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "review policy query failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /ledger/review-gates/evaluate", func(w http.ResponseWriter, r *http.Request) {
		var input ledger.ReviewGateInput
		if err := decodeJSONBody(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		result, err := store.EvaluateReviewGate(r.Context(), input)
		if errors.Is(err, ledger.ErrInvalidReviewGateInput) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, ledger.ErrReviewPolicyNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "review gate evaluation failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /ledger/reconciliation", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		var input ledger.ReconciliationInput
		if err := decodeJSONBody(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
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
	return authenticate(mux, token)
}

func decodeJSONBody(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
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

func authenticate(next http.Handler, token string) http.Handler {
	want := sha256.Sum256([]byte("Bearer " + token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
