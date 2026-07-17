package server

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"opl-cloud/services/control-plane/internal/controlplane"
)

var billingReviewEvidenceRefPattern = regexp.MustCompile(`^case-[0-9]{8}-[a-z0-9]{3,16}$`)

func registerAdminRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("POST /api/organizations", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		body, err := app.createOrganization(decodeJSON(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "organization.create", "organization", stringValue(body["id"]), stringValue(body["billingAccountId"]), nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, body)
	}))
	mux.HandleFunc("POST /api/organizations/members", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		body, err := app.createMembership(decodeJSON(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "organization.member_add", "organization_membership", stringValue(body["id"]), "", nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, body)
	}))
	mux.HandleFunc("POST /api/organizations/members/{id}/revoke", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		body, err := app.revokeMembership(r.Context(), r.PathValue("id"))
		if err != nil {
			if errors.Is(err, errMembershipNotFound) {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "organization.member_revoke", "organization_membership", stringValue(body["id"]), stringValue(body["accountId"]), nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("POST /api/users", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		body, err := app.createUser(r.Context(), service, input)
		if err != nil {
			writeCreateUserError(w, err)
			return
		}
		if err := app.appendAuditEvent(r, "user.create", "user", stringValue(body["id"]), stringValue(body["accountId"]), nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, body)
	}))
	mux.HandleFunc("POST /api/users/{id}/reset-password", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		body, err := app.resetUserPassword(r.Context(), r.PathValue("id"), stringField(decodeJSON(r), "password", ""))
		if err != nil {
			if errors.Is(err, errMissingPassword) || errors.Is(err, errWeakPassword) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeUserLifecycleError(w, err)
			return
		}
		if err := app.appendAuditEvent(r, "user.password_reset", "user", stringValue(body["id"]), stringValue(body["accountId"]), nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("POST /api/users/disable", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		withOperatorUserID(input, app.sessionUserID(r))
		body, err := app.disableUser(input)
		if err != nil {
			writeUserLifecycleError(w, err)
			return
		}
		if err := app.appendAuditEvent(r, "user.disable", "user", stringValue(body["id"]), stringValue(body["accountId"]), nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("POST /api/users/delete", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirm") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		withOperatorUserID(input, app.sessionUserID(r))
		body, err := app.softDeleteUser(input)
		if err != nil {
			writeUserLifecycleError(w, err)
			return
		}
		if err := app.appendAuditEvent(r, "user.delete", "user", stringValue(body["id"]), stringValue(body["accountId"]), nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("GET /api/operator/archive", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		result, err := app.archiveState(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "archive_state_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}))
	mux.HandleFunc("POST /api/operator/archive-terminal-resources", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirm") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		result, err := app.archiveTerminalResources(r.Context(), input)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "operator.archive_terminal_resources", "archive_job", stringValue(result["id"]), "", nil, result, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}))
	mux.HandleFunc("POST /api/operator/billing-reviews/{resourceType}/{id}/resolve", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !billingReviewRequestShapeValid(input) {
			writeError(w, http.StatusBadRequest, errInvalidBillingReview.Error())
			return
		}
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		if !validBillingReviewOpaqueID(key) {
			writeError(w, http.StatusBadRequest, "invalid_idempotency_key")
			return
		}
		evidenceRef := strings.TrimSpace(stringValue(input["evidenceRef"]))
		if !validBillingReviewEvidenceRef(evidenceRef) {
			writeError(w, http.StatusBadRequest, "invalid_evidence_ref")
			return
		}
		resolution := billingReviewResolutionInput{
			ResourceType: strings.TrimSpace(r.PathValue("resourceType")), ResourceID: strings.TrimSpace(r.PathValue("id")),
			AccountID: strings.TrimSpace(stringValue(input["accountId"])), BillingOperationID: strings.TrimSpace(stringValue(input["billingOperationId"])),
			Decision: strings.TrimSpace(stringValue(input["decision"])), EvidenceRef: evidenceRef, IdempotencyKey: key, Reviewer: app.sessionUserID(r),
		}
		var result map[string]any
		var err error
		if resolution.ResourceType == "workspace" {
			result, err = app.resolveWorkspaceRenewalReview(r.Context(), service, resolution)
		} else {
			result, err = app.resolveMonthlyBillingReview(r.Context(), service, resolution)
		}
		if err != nil {
			writeBillingReviewResolutionError(w, err)
			return
		}
		if err := app.appendBillingReviewResolutionAudit(r, key, result); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}))
}

func billingReviewRequestShapeValid(input map[string]any) bool {
	if len(input) != 4 {
		return false
	}
	for _, key := range []string{"accountId", "billingOperationId", "decision", "evidenceRef"} {
		value, ok := input[key].(string)
		if !ok || value == "" || value != strings.TrimSpace(value) {
			return false
		}
	}
	return true
}

func validBillingReviewEvidenceRef(value string) bool {
	return billingReviewEvidenceRefPattern.MatchString(value)
}

func validBillingReviewOpaqueID(value string) bool {
	if len(value) < 3 || len(value) > 48 || value != compactID(value) {
		return false
	}
	lower := strings.ToLower(value)
	for _, forbidden := range []string{"api-key", "apikey", "bearer", "credential", "password", "secret", "token"} {
		if strings.Contains(lower, forbidden) {
			return false
		}
	}
	return true
}

func writeBillingReviewResolutionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errInvalidBillingReview):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, errBillingReviewNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errIdempotencyConflict), errors.Is(err, errBillingReviewNotPending), errors.Is(err, errBillingReviewIdentity), errors.Is(err, errBillingReviewChargeFact), errors.Is(err, errBillingReviewProviderFact):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errBillingReviewReceipt), errors.Is(err, errBillingReviewRefund):
		writeError(w, http.StatusBadGateway, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
	}
}
