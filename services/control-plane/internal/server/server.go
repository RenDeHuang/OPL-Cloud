package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func NewServer(service *controlplane.Service) http.Handler {
	handler, err := NewPersistentServer(service, nil)
	if err != nil {
		panic(err)
	}
	return handler
}

func NewPersistentServer(service *controlplane.Service, store StateStore) (http.Handler, error) {
	app, err := newControlPlaneAppWithStore(store)
	if err != nil {
		return nil, err
	}
	if settlementWorkerEnabled() {
		app.startPeriodicSettlementWorker(context.Background(), service, settlementWorkerInterval())
	}
	if archiveRetentionWorkerEnabled() {
		app.startArchiveRetentionWorker(context.Background(), archiveRetentionWorkerInterval())
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/w/", app.proxyWorkspace)
	mux.HandleFunc("/api/", app.proxyWorkspaceRoot)
	mux.HandleFunc("/ws", app.proxyWorkspaceRoot)
	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if !limitJSONBody(w, r) {
			return
		}
		input := decodeJSON(r)
		if app.loginRateLimited(r, input) {
			writeError(w, http.StatusTooManyRequests, "login_rate_limited")
			return
		}
		payload, sessionID, err := app.login(input)
		if err != nil {
			app.recordLoginFailure(r, input)
			writeError(w, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		app.clearLoginFailures(r, input)
		http.SetCookie(w, sessionCookie(sessionID, 12*60*60))
		w.Header().Set("x-opl-csrf-token", stringValue(payload["csrfToken"]))
		writeJSON(w, http.StatusOK, payload)
	})
	mux.HandleFunc("POST /api/auth/operator-login", func(w http.ResponseWriter, r *http.Request) {
		if !limitJSONBody(w, r) {
			return
		}
		input := map[string]any{"email": "operator"}
		if app.loginRateLimited(r, input) {
			writeError(w, http.StatusTooManyRequests, "login_rate_limited")
			return
		}
		expectedToken := strings.TrimSpace(os.Getenv("OPL_OPERATOR_SUMMARY_TOKEN"))
		if expectedToken == "" || r.Header.Get("x-opl-operator-token") != expectedToken {
			app.recordLoginFailure(r, input)
			writeError(w, http.StatusUnauthorized, "operator_token_invalid")
			return
		}
		payload, sessionID, err := app.operatorLogin()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "operator_session_failed")
			return
		}
		app.clearLoginFailures(r, input)
		http.SetCookie(w, sessionCookie(sessionID, 12*60*60))
		w.Header().Set("x-opl-csrf-token", stringValue(payload["csrfToken"]))
		writeJSON(w, http.StatusOK, payload)
	})
	mux.HandleFunc("GET /api/auth/me", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		payload, ok := app.session(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not_authenticated")
			return
		}
		writeJSON(w, http.StatusOK, payload)
	}))
	mux.HandleFunc("POST /api/auth/logout", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		if err := app.logout(r); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		http.SetCookie(w, sessionCookie("", -1))
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("GET /api/me", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		payload, ok := app.session(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not_authenticated")
			return
		}
		writeJSON(w, http.StatusOK, payload["user"])
	}))
	mux.HandleFunc("GET /api/state", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		if !app.syncRuntimeOperations(w, r, service) {
			return
		}
		if !app.syncLedgerFacts(w, r, service, accountID) {
			return
		}
		if !app.refreshFacts(w, r) {
			return
		}
		computePools, ok := fabricComputePools(w, r, service)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, app.state(accountID, computePools))
	}))
	mux.HandleFunc("GET /api/pricing/catalog", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, pricingCatalogResponse())
	}))
	mux.HandleFunc("POST /api/pricing/preview", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		if !limitJSONBody(w, r) {
			return
		}
		input := decodeJSON(r)
		accountID, ok := app.scopedAccountID(w, r, input)
		if !ok {
			return
		}
		wallet, err := service.Wallet(r.Context(), accountID)
		if err != nil {
			writeUpstreamError(w)
			return
		}
		writeJSON(w, http.StatusOK, pricingPreviewResponse(input, walletProjection(wallet)))
	}))
	mux.HandleFunc("GET /api/management/state", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		if !app.syncRuntimeOperations(w, r, service) {
			return
		}
		if !app.syncLedgerFacts(w, r, service, "") {
			return
		}
		if !app.refreshFacts(w, r) {
			return
		}
		computePools, ok := fabricComputePools(w, r, service)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, app.managementState(r.URL.Query().Get("includeDeleted") == "true", computePools))
	}))
	mux.HandleFunc("GET /api/operator/summary", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		if !app.syncRuntimeOperations(w, r, service) {
			return
		}
		if !app.syncLedgerFacts(w, r, service, "") {
			return
		}
		if !app.refreshFacts(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, app.operatorSummary())
	}))
	mux.HandleFunc("GET /api/runtime/readiness", func(w http.ResponseWriter, r *http.Request) {
		readiness, err := service.RuntimeReadiness(r.Context())
		if err != nil {
			writeUpstreamError(w)
			return
		}
		writeJSON(w, http.StatusOK, readiness)
	})
	mux.HandleFunc("GET /api/production/readiness", func(w http.ResponseWriter, r *http.Request) {
		readiness, err := service.RuntimeReadiness(r.Context())
		if err != nil {
			writeUpstreamError(w)
			return
		}
		readiness["checks"] = []any{}
		writeJSON(w, http.StatusOK, readiness)
	})
	mux.HandleFunc("GET /api/overview", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"service": "control-plane", "workspaces": 0})
	}))
	mux.HandleFunc("GET /api/workspaces", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		if !app.refreshFacts(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, app.state(accountID, nil)["workspaces"])
	}))
	mux.HandleFunc("POST /api/workspaces/reset-token", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		workspaceID := stringField(input, "workspaceId", "")
		before, exists := app.getWorkspace(workspaceID)
		if exists && !app.canAccessResource(r, before) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		workspace, ok, err := app.setWorkspaceAccess(workspaceID, "active")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "workspace_not_found")
			return
		}
		if err := app.appendAuditEvent(r, "workspace.reset_token", "workspace", workspaceID, stringValue(workspace["accountId"]), before, workspace, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": workspace["id"], "tokenStatus": nested(workspace, "access", "tokenStatus"), "access": workspace["access"]})
	}))
	mux.HandleFunc("POST /api/workspaces/delete-token", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		workspaceID := stringField(input, "workspaceId", "")
		before, exists := app.getWorkspace(workspaceID)
		if exists && !app.canAccessResource(r, before) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		workspace, ok, err := app.setWorkspaceAccess(workspaceID, "disabled")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "workspace_not_found")
			return
		}
		if err := app.appendAuditEvent(r, "workspace.delete_token", "workspace", workspaceID, stringValue(workspace["accountId"]), before, workspace, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": workspace["id"], "tokenStatus": nested(workspace, "access", "tokenStatus"), "access": workspace["access"]})
	}))
	mux.HandleFunc("POST /api/workspaces/runtime-status", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		workspaceID := stringField(input, "workspaceId", "")
		if workspace, ok := app.getWorkspace(workspaceID); ok && !app.canAccessResource(r, workspace) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		runtime, err := service.WorkspaceRuntimeStatus(r.Context(), workspaceID)
		if err != nil {
			writeUpstreamError(w)
			return
		}
		writeJSON(w, http.StatusOK, workspaceRuntimeStatusResponse(runtime))
	}))
	mux.HandleFunc("POST /api/workspaces", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		accountID, ok := app.scopedAccountID(w, r, input)
		if !ok {
			return
		}
		attachmentID := stringField(input, "attachmentId", "")
		attachment, ok := app.getAttachment(attachmentID)
		if !ok {
			writeError(w, http.StatusBadRequest, "attached_compute_storage_required")
			return
		}
		if !app.canAccessResource(r, attachment) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		computeID := stringValue(attachment["computeAllocationId"])
		storageID := stringValue(attachment["storageId"])
		workspace, err := service.CreateWorkspace(r.Context(), controlplane.CreateWorkspaceInput{
			AccountID:    accountID,
			OwnerID:      firstNonEmpty(stringField(input, "ownerId", ""), stringField(input, "ownerUserId", "")),
			Name:         firstNonEmpty(stringField(input, "name", ""), stringField(input, "workspaceName", "Workspace")),
			PackageID:    firstNonEmpty(stringField(input, "packageId", ""), stringValue(attachment["packageId"]), "basic"),
			AttachmentID: attachmentID,
			ComputeID:    computeID,
			VolumeID:     storageID,
		}, mutationKey(r, input))
		if err != nil {
			writeUpstreamError(w)
			return
		}
		if err := app.rememberWorkspaceProjection(workspace); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		body := workspaceResponse(structToMap(workspace))
		if err := app.appendAuditEvent(r, "workspace.create", "workspace", workspace.ID, workspace.AccountID, nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, body)
	}))
	mux.HandleFunc("GET /api/billing/summary", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		wallet, err := service.Wallet(r.Context(), accountID)
		if err != nil {
			writeUpstreamError(w)
			return
		}
		if err := app.applyLedgerFacts(accountID, wallet, nil, nil, nil, nil); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, walletProjection(wallet))
	}))
	mux.HandleFunc("POST /api/billing/topups", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirm") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		idempotencyKey := firstNonEmpty(r.Header.Get("Idempotency-Key"), stringField(input, "idempotencyKey", ""))
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		result, err := service.ManualTopUp(r.Context(), controlplane.ManualTopUpInput{
			AccountID:      stringField(input, "accountId", "acct-local"),
			AmountCents:    moneyToCents(input),
			Currency:       stringField(input, "currency", "CNY"),
			OperatorUserID: firstNonEmpty(app.sessionUserID(r), stringField(input, "operatorUserId", "operator")),
			Reason:         stringField(input, "reason", ""),
		}, idempotencyKey)
		if err != nil {
			writeUpstreamError(w)
			return
		}
		if err := app.rememberManualTopUp(result); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "billing.topup", "account", result.TopUp.AccountID, result.TopUp.AccountID, nil, manualTopUpResponse(result), "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, manualTopUpResponse(result))
	}))
	mux.HandleFunc("POST /api/billing/resource-settlements", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirm") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		idempotencyKey := firstNonEmpty(r.Header.Get("Idempotency-Key"), stringField(input, "idempotencyKey", ""), stringField(input, "sourceEventId", ""))
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		settlement := controlplane.ResourceSettlementInput{
			AccountID:               stringField(input, "accountId", "acct-local"),
			WorkspaceID:             stringField(input, "workspaceId", ""),
			ResourceType:            stringField(input, "resourceType", "compute"),
			ResourceID:              firstNonEmpty(stringField(input, "resourceId", ""), stringField(input, "computeAllocationId", ""), stringField(input, "storageId", "")),
			AmountCents:             settlementAmountCents(input),
			Currency:                stringField(input, "currency", "CNY"),
			PricingVersion:          stringField(input, "pricingVersion", ""),
			PriceSnapshot:           mapField(input, "priceSnapshot"),
			UsagePeriodStart:        stringField(input, "usagePeriodStart", ""),
			UsagePeriodEnd:          stringField(input, "usagePeriodEnd", ""),
			Quantity:                numberField(input, "quantity", 0),
			Unit:                    stringField(input, "unit", ""),
			ProviderCostEvidenceRef: stringField(input, "providerCostEvidenceRef", ""),
		}
		result, err := service.SettleResource(r.Context(), settlement, idempotencyKey)
		if err != nil {
			writeUpstreamError(w)
			return
		}
		result = completeSettlementResult(result, settlement)
		if err := app.rememberResourceSettlement(result); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		body := settlementResponse(result)
		if err := app.appendAuditEvent(r, "billing.settle_resource", "ledger_settlement", stringValue(body["id"]), result.AccountID, nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, body)
	}))
	mux.HandleFunc("POST /api/billing/reconciliation", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirm") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		report, _ := input["report"].(map[string]any)
		if report == nil {
			report = map[string]any{}
		}
		idempotencyKey := firstNonEmpty(r.Header.Get("Idempotency-Key"), stringField(input, "idempotencyKey", ""), stringField(report, "id", ""))
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		result, err := service.RecordReconciliation(r.Context(), controlplane.ReconciliationInput{Report: report}, idempotencyKey)
		if err != nil {
			writeUpstreamError(w)
			return
		}
		if err := app.rememberReconciliation(result); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "billing.reconciliation", "billing_reconciliation", stringField(report, "id", ""), "", nil, result, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, reconciliationResponse(result))
	}))
	mux.HandleFunc("GET /api/compute-pools", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		computePools, ok := fabricComputePools(w, r, service)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, computePools)
	}))
	mux.HandleFunc("GET /api/compute-allocations", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, app.state(accountID, nil)["computeAllocations"])
	}))
	mux.HandleFunc("POST /api/compute-allocations", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		accountID, ok := app.scopedAccountID(w, r, input)
		if !ok {
			return
		}
		if guard, blocked := app.reconciliationBlocksNewWorkspaces(); blocked {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "billing_reconciliation_blocked", "billingReconciliation": guard})
			return
		}
		compute, err := service.CreateComputeAllocation(r.Context(), controlplane.ComputeAllocationInput{
			AccountID:       accountID,
			WorkspaceID:     stringField(input, "workspaceId", ""),
			PackageID:       stringField(input, "packageId", "basic"),
			HoldAmountCents: computeHoldAmountCents(stringField(input, "packageId", "basic")),
		}, mutationKey(r, input))
		if err != nil {
			writeUpstreamError(w)
			return
		}
		body := computeResponse(structToMap(compute))
		if err := app.rememberCompute(body); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "compute.create", "compute_allocation", stringValue(body["id"]), accountID, nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, body)
	}))
	mux.HandleFunc("GET /api/compute-allocations/{id}", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		compute, ok := app.getCompute(id)
		if ok && stringValue(compute["status"]) != "provisioning" {
			if !app.canAccessResource(r, compute) {
				writeError(w, http.StatusForbidden, "account_scope_forbidden")
				return
			}
			writeJSON(w, http.StatusOK, compute)
			return
		}
		fresh, err := service.GetComputeAllocation(r.Context(), id)
		if err == nil && fresh.ID != "" {
			body := computeResponse(structToMap(fresh))
			if err := app.rememberCompute(body); err != nil {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
			writeJSON(w, http.StatusOK, body)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "compute_allocation_not_found")
			return
		}
		if !app.canAccessResource(r, compute) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		writeJSON(w, http.StatusOK, compute)
	}))
	mux.HandleFunc("POST /api/compute-allocations/{id}/sync", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		id := strings.TrimSpace(r.PathValue("id"))
		existing, ok := app.getCompute(id)
		if !ok {
			writeError(w, http.StatusNotFound, "compute_allocation_not_found")
			return
		}
		if !app.canAccessResource(r, existing) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		releaseInput := destroyResourceInput(id, existing)
		if stringValue(existing["holdReleaseId"]) != "" || billingStatusFor(existing) == "stopped" {
			releaseInput.HoldID = ""
			releaseInput.HoldAmountCents = 0
		}
		compute, err := service.SyncComputeAllocation(r.Context(), releaseInput, mutationKey(r, input))
		if err != nil {
			writeUpstreamError(w)
			return
		}
		body := computeResponse(mergeMaps(existing, structToMap(compute)))
		if err := app.rememberCompute(body); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "compute.sync", "compute_allocation", id, firstNonEmpty(stringValue(existing["accountId"]), stringValue(existing["ownerAccountId"]), stringValue(body["accountId"])), existing, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("POST /api/compute-allocations/{id}/destroy", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirm") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		existing, _ := app.getCompute(id)
		if existing["id"] != nil && !app.canAccessResource(r, existing) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		compute, err := service.DestroyComputeAllocation(r.Context(), destroyResourceInput(id, existing), mutationKey(r, input))
		if err != nil {
			writeUpstreamError(w)
			return
		}
		body := computeResponse(structToMap(compute))
		if err := app.rememberCompute(body); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "compute.destroy", "compute_allocation", id, firstNonEmpty(stringValue(existing["accountId"]), stringValue(existing["ownerAccountId"]), stringValue(body["accountId"])), existing, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("POST /api/storage-volumes", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		accountID, ok := app.scopedAccountID(w, r, input)
		if !ok {
			return
		}
		if guard, blocked := app.reconciliationBlocksNewWorkspaces(); blocked {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "billing_reconciliation_blocked", "billingReconciliation": guard})
			return
		}
		storage, err := service.CreateStorageVolume(r.Context(), controlplane.StorageVolumeInput{
			AccountID:       accountID,
			WorkspaceID:     stringField(input, "workspaceId", ""),
			SizeGB:          int(numberField(input, "sizeGb", 10)),
			HoldAmountCents: storageHoldAmountCents(stringField(input, "packageId", "basic"), numberField(input, "sizeGb", 10)),
		}, mutationKey(r, input))
		if err != nil {
			writeUpstreamError(w)
			return
		}
		body := storageResponse(structToMap(storage))
		if err := app.rememberStorage(body); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "storage.create", "storage_volume", stringValue(body["id"]), accountID, nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, body)
	}))
	mux.HandleFunc("POST /api/storage-volumes/destroy", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirmDataLoss") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		id := stringField(input, "storageId", "")
		existing, _ := app.getStorage(id)
		if existing["id"] != nil && !app.canAccessResource(r, existing) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		storage, err := service.DestroyStorageVolume(r.Context(), destroyResourceInput(id, existing), mutationKey(r, input))
		if err != nil {
			writeUpstreamError(w)
			return
		}
		body := storageResponse(structToMap(storage))
		if err := app.rememberStorage(body); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "storage.destroy", "storage_volume", id, firstNonEmpty(stringValue(existing["accountId"]), stringValue(existing["ownerAccountId"]), stringValue(body["accountId"])), existing, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("POST /api/storage-volumes/{id}/sync", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		id := strings.TrimSpace(r.PathValue("id"))
		existing, ok := app.getStorage(id)
		if !ok {
			writeError(w, http.StatusNotFound, "storage_volume_not_found")
			return
		}
		if !app.canAccessResource(r, existing) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		releaseInput := destroyResourceInput(id, existing)
		if stringValue(existing["holdReleaseId"]) != "" || billingStatusFor(existing) == "stopped" {
			releaseInput.HoldID = ""
			releaseInput.HoldAmountCents = 0
		}
		storage, err := service.SyncStorageVolume(r.Context(), releaseInput, mutationKey(r, input))
		if err != nil {
			writeUpstreamError(w)
			return
		}
		body := storageResponse(mergeMaps(existing, structToMap(storage)))
		if err := app.rememberStorage(body); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "storage.sync", "storage_volume", id, firstNonEmpty(stringValue(existing["accountId"]), stringValue(existing["ownerAccountId"]), stringValue(body["accountId"])), existing, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("POST /api/storage-attachments", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		accountID, ok := app.scopedAccountID(w, r, input)
		if !ok {
			return
		}
		compute, computeOK := app.getCompute(stringField(input, "computeAllocationId", ""))
		storage, storageOK := app.getStorage(stringField(input, "storageId", ""))
		if (computeOK && !app.canAccessResource(r, compute)) || (storageOK && !app.canAccessResource(r, storage)) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		accountID = firstNonEmpty(stringValue(compute["accountId"]), stringValue(compute["ownerAccountId"]), stringValue(storage["accountId"]), stringValue(storage["ownerAccountId"]), accountID)
		attachment, err := service.CreateStorageAttachment(r.Context(), controlplane.StorageAttachmentInput{
			WorkspaceID: stringField(input, "workspaceId", ""),
			ComputeID:   stringField(input, "computeAllocationId", ""),
			VolumeID:    stringField(input, "storageId", ""),
		}, mutationKey(r, input))
		if err != nil {
			writeUpstreamError(w)
			return
		}
		body := attachmentResponse(structToMap(attachment), input)
		body["accountId"] = accountID
		if err := app.rememberAttachment(body, input); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "attachment.create", "storage_attachment", stringValue(body["id"]), accountID, nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, body)
	}))
	mux.HandleFunc("POST /api/storage-attachments/detach", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		attachmentID := stringField(input, "attachmentId", "")
		existing, _ := app.getAttachment(attachmentID)
		if existing["id"] != nil && !app.canAccessResource(r, existing) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		attachment, err := service.DetachStorageAttachment(r.Context(), attachmentID, mutationKey(r, input))
		if err != nil {
			writeUpstreamError(w)
			return
		}
		body := attachmentResponse(structToMap(attachment), input)
		if err := app.rememberAttachment(body, input); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "attachment.detach", "storage_attachment", attachmentID, firstNonEmpty(stringValue(existing["accountId"]), stringValue(existing["ownerAccountId"])), existing, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("GET /api/support/tickets", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		user, _ := app.sessionUserContext(r)
		if r.URL.Query().Get("scope") == "all" && stringValue(user["role"]) != "admin" {
			writeError(w, http.StatusForbidden, "admin_required")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tickets": app.supportTickets(r.URL.Query().Get("scope") == "all", stringValue(user["accountId"]))})
	}))
	mux.HandleFunc("POST /api/support/tickets", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		accountID, ok := app.scopedAccountID(w, r, input)
		if !ok {
			return
		}
		user, ok := app.sessionUserContext(r)
		withSessionUserContext(input, user, ok)
		input["accountId"] = accountID
		body, err := app.createSupportMapping(input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := app.appendAuditEvent(r, "support.map_external_ticket", "support_ticket_mapping", stringValue(body["id"]), accountID, nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, body)
	}))
	mux.HandleFunc("GET /api/ledger/task-receipts", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"receipts": []any{}})
	}))
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
	mux.HandleFunc("POST /api/users", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		body, err := app.createUser(input)
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
	mux.HandleFunc("POST /api/operator/cleanup-workspace-access", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirm") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		result, err := app.cleanupWorkspaceAccess(input)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "operator.cleanup_workspace_access", "workspace_access_cleanup", "", "", nil, result, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
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
	mux.HandleFunc("/", app.consoleStatic)
	return mux, nil
}

func (app *controlPlaneApp) consoleStatic(w http.ResponseWriter, r *http.Request) {
	if isWorkspaceRequest(r) {
		app.proxyWorkspaceRoot(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	dist := consoleDistDir()
	if strings.HasPrefix(r.URL.Path, "/assets/") {
		http.FileServer(http.Dir(dist)).ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if data, err := os.ReadFile(filepath.Join(dist, "index.html")); err == nil {
		_, _ = w.Write(data)
		return
	}
	_, _ = w.Write([]byte(`<!doctype html><html><head><title>OPL Console</title></head><body><div id="root"></div></body></html>`))
}

func consoleDistDir() string {
	for _, dir := range []string{strings.TrimSpace(os.Getenv("OPL_CONSOLE_DIST_DIR")), "dist", "../../dist", "../../../../dist"} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
			return dir
		}
	}
	return "dist"
}

func (app *controlPlaneApp) protected(requiresAdmin bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload, ok := app.session(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not_authenticated")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if !limitJSONBody(w, r) {
				return
			}
			if r.Header.Get("x-opl-csrf") != stringValue(payload["csrfToken"]) {
				writeError(w, http.StatusForbidden, "csrf_token_invalid")
				return
			}
		}
		user, _ := payload["user"].(map[string]any)
		if requiresAdmin && stringValue(user["role"]) != "admin" {
			writeError(w, http.StatusForbidden, "admin_required")
			return
		}
		next(w, r)
	}
}

func (app *controlPlaneApp) syncRuntimeOperations(w http.ResponseWriter, r *http.Request, service *controlplane.Service) bool {
	operations, err := service.FabricOperations(r.Context())
	if err != nil {
		writeUpstreamError(w)
		return false
	}
	if err := app.rememberRuntimeOperations(operations); err != nil {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return false
	}
	return true
}

func (app *controlPlaneApp) syncLedgerFacts(w http.ResponseWriter, r *http.Request, service *controlplane.Service, accountID string) bool {
	entries, err := service.ListLedgerEntries(r.Context(), accountID)
	if err != nil {
		writeUpstreamError(w)
		return false
	}
	transactions, err := service.ListWalletTransactions(r.Context(), accountID)
	if err != nil {
		writeUpstreamError(w)
		return false
	}
	topups, err := service.ListManualTopUps(r.Context(), accountID)
	if err != nil {
		writeUpstreamError(w)
		return false
	}
	settlements, err := service.ListResourceSettlements(r.Context(), accountID)
	if err != nil {
		writeUpstreamError(w)
		return false
	}
	var wallet clients.Wallet
	if accountID != "" {
		wallet, err = service.Wallet(r.Context(), accountID)
		if err != nil {
			writeUpstreamError(w)
			return false
		}
	}
	if err := app.applyLedgerFacts(accountID, wallet, entries, transactions, topups, settlements); err != nil {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return false
	}
	return true
}

func (app *controlPlaneApp) refreshFacts(w http.ResponseWriter, r *http.Request) bool {
	if err := app.refreshFactsFromStore(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "state_load_failed")
		return false
	}
	return true
}

func fabricComputePools(w http.ResponseWriter, r *http.Request, service *controlplane.Service) ([]any, bool) {
	catalog, err := service.FabricCatalog(r.Context())
	if err != nil {
		writeUpstreamError(w)
		return nil, false
	}
	return computePoolsFromFabricCatalog(catalog), true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeUpstreamError(w http.ResponseWriter) {
	writeError(w, http.StatusBadGateway, "upstream_unavailable")
}

const maxJSONBodyBytes int64 = 1 << 20

func limitJSONBody(w http.ResponseWriter, r *http.Request) bool {
	if r.Body == nil {
		return true
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBodyBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json_body")
		return false
	}
	if int64(len(data)) > maxJSONBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "request_body_too_large")
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	return true
}

func writeUserLifecycleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUserNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errLastActiveAdmin), errors.Is(err, errUserDeleted):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
	}
}

func writeCreateUserError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUserExists):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

func requestIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

func withOperatorUserID(input map[string]any, userID string) {
	if userID != "" && stringValue(input["operatorUserId"]) == "" {
		input["operatorUserId"] = userID
	}
}

func withSessionUserContext(input map[string]any, user map[string]any, ok bool) {
	if !ok {
		return
	}
	if stringValue(input["userId"]) == "" {
		input["userId"] = stringValue(user["id"])
	}
	if stringValue(input["accountId"]) == "" {
		input["accountId"] = stringValue(user["accountId"])
	}
}

func (app *controlPlaneApp) scopedAccountID(w http.ResponseWriter, r *http.Request, input map[string]any) (string, bool) {
	user, ok := app.sessionUserContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "not_authenticated")
		return "", false
	}
	requested := r.URL.Query().Get("accountId")
	if input != nil {
		requested = firstNonEmpty(stringField(input, "accountId", ""), requested)
	}
	sessionAccount := stringValue(user["accountId"])
	if stringValue(user["role"]) == "admin" {
		return firstNonEmpty(requested, sessionAccount), true
	}
	if sessionAccount == "" || (requested != "" && requested != sessionAccount) {
		writeError(w, http.StatusForbidden, "account_scope_forbidden")
		return "", false
	}
	return sessionAccount, true
}

func decodeJSON(r *http.Request) map[string]any {
	var input map[string]any
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		return map[string]any{}
	}
	return input
}

func stringField(input map[string]any, key string, fallback string) string {
	if value, ok := input[key].(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func numberField(input map[string]any, key string, fallback float64) float64 {
	switch value := input[key].(type) {
	case float64:
		return value
	case int:
		return float64(value)
	default:
		return fallback
	}
}

func mapField(input map[string]any, key string) map[string]any {
	value, _ := input[key].(map[string]any)
	return cloneMap(value)
}

func confirmed(input map[string]any, key string) bool {
	value, ok := input[key].(bool)
	return ok && value
}

func moneyToCents(input map[string]any) int64 {
	if cents := numberField(input, "amountCents", -1); cents >= 0 {
		return int64(cents)
	}
	return int64(numberField(input, "amount", 0) * 100)
}

func mutationKey(r *http.Request, input map[string]any) string {
	return firstNonEmpty(r.Header.Get("Idempotency-Key"), stringField(input, "idempotencyKey", ""), stringField(input, "sourceEventId", ""), stableID(r.Method, r.URL.Path, time.Now().UTC().String()))
}

func structToMap(value any) map[string]any {
	data, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var output map[string]any
	if err := json.Unmarshal(data, &output); err != nil {
		return map[string]any{}
	}
	return output
}

func computeResponse(row map[string]any) map[string]any {
	if row == nil {
		row = map[string]any{}
	}
	row["ownerAccountId"] = firstNonEmpty(stringValue(row["ownerAccountId"]), stringValue(row["accountId"]))
	row["provider"] = firstNonEmpty(stringValue(row["provider"]), "tencent-tke")
	row["status"] = firstNonEmpty(stringValue(row["status"]), "running")
	row["billingStatus"] = billingStatusFor(row)
	row["cvmInstanceId"] = firstNonEmpty(stringValue(row["cvmInstanceId"]), stringValue(row["instanceId"]))
	if holdCents := numberField(row, "holdAmountCents", 0); holdCents > 0 {
		row["holdAmount"] = holdCents / 100
	}
	if serviceName := stringValue(row["serviceName"]); serviceName != "" {
		row["runtime"] = map[string]any{"serviceName": serviceName, "service": "service/" + serviceName}
	}
	return row
}

func storageResponse(row map[string]any) map[string]any {
	if row == nil {
		row = map[string]any{}
	}
	row["ownerAccountId"] = firstNonEmpty(stringValue(row["ownerAccountId"]), stringValue(row["accountId"]))
	row["provider"] = firstNonEmpty(stringValue(row["provider"]), "tencent-tke")
	if stringValue(row["status"]) == "ready" {
		row["status"] = "available"
	}
	row["status"] = firstNonEmpty(stringValue(row["status"]), "available")
	row["billingStatus"] = billingStatusFor(row)
	if holdCents := numberField(row, "holdAmountCents", 0); holdCents > 0 {
		row["holdAmount"] = holdCents / 100
	}
	if numberField(row, "sizeGb", 0) == 0 {
		row["sizeGb"] = 10
	}
	return row
}

func attachmentResponse(row map[string]any, input map[string]any) map[string]any {
	if row == nil {
		row = map[string]any{}
	}
	row["computeAllocationId"] = firstNonEmpty(stringValue(row["computeAllocationId"]), stringValue(row["computeId"]), stringField(input, "computeAllocationId", ""))
	row["storageId"] = firstNonEmpty(stringValue(row["storageId"]), stringValue(row["volumeId"]), stringField(input, "storageId", ""))
	row["mountPath"] = firstNonEmpty(stringValue(row["mountPath"]), stringField(input, "mountPath", "/data"))
	row["provider"] = firstNonEmpty(stringValue(row["provider"]), "tencent-tke")
	row["status"] = firstNonEmpty(stringValue(row["status"]), "attached")
	return row
}

func workspaceResponse(row map[string]any) map[string]any {
	if row == nil {
		row = map[string]any{}
	}
	row["ownerAccountId"] = firstNonEmpty(stringValue(row["ownerAccountId"]), stringValue(row["accountId"]))
	row["ownerUserId"] = firstNonEmpty(stringValue(row["ownerUserId"]), stringValue(row["ownerId"]))
	row["state"] = firstNonEmpty(stringValue(row["state"]), stringValue(row["status"]))
	row["currentComputeAllocationId"] = firstNonEmpty(stringValue(row["currentComputeAllocationId"]), stringValue(row["computeAllocationId"]))
	row["currentAttachmentId"] = firstNonEmpty(stringValue(row["currentAttachmentId"]), stringValue(row["attachmentId"]))
	if serviceName := stringValue(row["runtimeServiceName"]); serviceName != "" {
		row["runtime"] = map[string]any{"serviceName": serviceName}
	}
	access, _ := row["access"].(map[string]any)
	access = cloneMap(access)
	access["tokenStatus"] = firstNonEmpty(stringValue(access["tokenStatus"]), "active")
	access["requiresLogin"] = false
	if username := stringValue(row["runtimeUsername"]); username != "" {
		access["account"] = username
		access["username"] = username
	}
	if password := stringValue(row["runtimePassword"]); password != "" {
		access["password"] = password
	}
	if status := stringValue(row["credentialStatus"]); status != "" {
		access["credentialStatus"] = status
	}
	if version := stringValue(row["credentialVersion"]); version != "" {
		access["credentialVersion"] = version
	}
	if secretRef := stringValue(row["credentialSecretRef"]); secretRef != "" {
		access["secretRef"] = secretRef
	}
	row["access"] = access
	return row
}

func billingStatusFor(row map[string]any) string {
	if status := stringValue(row["billingStatus"]); status != "" {
		return status
	}
	status := stringValue(row["status"])
	if isTerminalResourceStatus(status) {
		return "stopped"
	}
	switch status {
	case "detached", "failed":
		return "stopped"
	default:
		return "active"
	}
}

func isTerminalResourceStatus(status string) bool {
	switch status {
	case "destroyed", "external_deleted", "deleted", "missing":
		return true
	default:
		return false
	}
}

func mergeMaps(base map[string]any, updates map[string]any) map[string]any {
	output := cloneMap(base)
	for key, value := range updates {
		if !emptyMergeValue(value) {
			output[key] = value
		}
	}
	return output
}

func emptyMergeValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	default:
		return false
	}
}

func manualTopUpResponse(result clients.ManualTopUpResult) map[string]any {
	return map[string]any{
		"id":                  result.TopUp.ID,
		"idempotent":          result.Replayed,
		"targetAccountId":     result.TopUp.AccountID,
		"amount":              float64(result.TopUp.AmountCents) / 100,
		"amountCents":         result.TopUp.AmountCents,
		"operatorUserId":      result.TopUp.OperatorUserID,
		"ledgerEntryId":       result.LedgerEntry.ID,
		"walletTransactionId": result.WalletTransaction.ID,
		"balance":             float64(result.Wallet.BalanceCents) / 100,
		"frozen":              float64(result.Wallet.FrozenCents) / 100,
		"available":           float64(result.Wallet.AvailableCents) / 100,
		"wallet":              result.Wallet,
		"status":              "completed",
	}
}

func settlementAmountCents(input map[string]any) int64 {
	if cents := numberField(input, "amountCents", -1); cents >= 0 {
		return int64(cents)
	}
	if amount := numberField(input, "amount", -1); amount >= 0 {
		return int64(amount * 100)
	}
	hours := numberField(input, "hours", 1)
	return int64(hours * 100)
}

func destroyResourceInput(id string, row map[string]any) controlplane.DestroyResourceInput {
	return controlplane.DestroyResourceInput{
		ID:              id,
		AccountID:       firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"]), "acct-local"),
		WorkspaceID:     stringValue(row["workspaceId"]),
		HoldID:          stringValue(row["holdId"]),
		HoldAmountCents: int64(numberField(row, "holdAmountCents", 0)),
	}
}

func computeHoldAmountCents(packageID string) int64 {
	plan := packageByID(packageID)
	return cents(priceField(plan, "computeHourly") * 24 * 7)
}

func storageHoldAmountCents(packageID string, sizeGB float64) int64 {
	plan := packageByID(packageID)
	return cents(priceField(plan, "storageGbMonth") * sizeGB / 30 * 7)
}

func packageByID(packageID string) map[string]any {
	for _, plan := range packageList() {
		row, _ := plan.(map[string]any)
		if stringValue(row["id"]) == packageID {
			return row
		}
	}
	first, _ := packageList()[0].(map[string]any)
	return first
}

func cents(amount float64) int64 {
	return int64(math.Round(amount * 100))
}

func priceField(plan map[string]any, key string) float64 {
	price, _ := plan["price"].(map[string]any)
	return numberField(price, key, 0)
}

func settlementResponse(result clients.ResourceSettlementResult) map[string]any {
	return map[string]any{
		"id":                      result.ID,
		"accountId":               result.AccountID,
		"workspaceId":             result.WorkspaceID,
		"resourceType":            result.ResourceType,
		"resourceId":              result.ResourceID,
		"amount":                  float64(result.AmountCents) / 100,
		"amountCents":             result.AmountCents,
		"status":                  result.Status,
		"ledgerEntryId":           result.LedgerEntryID,
		"walletTransactionId":     result.WalletTransactionID,
		"pricingVersion":          result.PricingVersion,
		"priceSnapshot":           result.PriceSnapshot,
		"usagePeriodStart":        result.UsagePeriodStart,
		"usagePeriodEnd":          result.UsagePeriodEnd,
		"quantity":                result.Quantity,
		"unit":                    result.Unit,
		"providerCostEvidenceRef": result.ProviderCostEvidenceRef,
		"wallet":                  result.Wallet,
	}
}

func completeSettlementResult(result clients.ResourceSettlementResult, input controlplane.ResourceSettlementInput) clients.ResourceSettlementResult {
	result.AccountID = firstNonEmpty(result.AccountID, input.AccountID)
	result.WorkspaceID = firstNonEmpty(result.WorkspaceID, input.WorkspaceID)
	result.ResourceType = firstNonEmpty(result.ResourceType, input.ResourceType)
	result.ResourceID = firstNonEmpty(result.ResourceID, input.ResourceID)
	if result.AmountCents == 0 {
		result.AmountCents = input.AmountCents
	}
	result.Currency = firstNonEmpty(result.Currency, input.Currency)
	result.PricingVersion = firstNonEmpty(result.PricingVersion, input.PricingVersion)
	if len(result.PriceSnapshot) == 0 {
		result.PriceSnapshot = cloneMap(input.PriceSnapshot)
	}
	result.UsagePeriodStart = firstNonEmpty(result.UsagePeriodStart, input.UsagePeriodStart)
	result.UsagePeriodEnd = firstNonEmpty(result.UsagePeriodEnd, input.UsagePeriodEnd)
	if result.Quantity == 0 {
		result.Quantity = input.Quantity
	}
	result.Unit = firstNonEmpty(result.Unit, input.Unit)
	result.ProviderCostEvidenceRef = firstNonEmpty(result.ProviderCostEvidenceRef, input.ProviderCostEvidenceRef)
	result.Wallet.AccountID = firstNonEmpty(result.Wallet.AccountID, result.AccountID)
	result.Wallet.Currency = firstNonEmpty(result.Wallet.Currency, result.Currency)
	return result
}

func reconciliationResponse(result clients.ReconciliationResult) map[string]any {
	return map[string]any{
		"id":     result.ID,
		"status": result.Status,
		"guard": map[string]any{
			"status":             result.Status,
			"blockNewWorkspaces": result.BlockNewWorkspaces,
			"reason":             result.Reason,
		},
		"report": result.Report,
	}
}

func workspaceRuntimeStatusResponse(runtime clients.WorkspaceRuntime) map[string]any {
	ready := runtime.Ready
	checks := runtime.Checks
	if len(checks) == 0 {
		ready = runtime.Status == "running"
		checks = []any{map[string]any{"name": "fabric_runtime_running", "ok": ready}}
	}
	body := map[string]any{
		"provider":    "tencent-tke",
		"workspaceId": runtime.WorkspaceID,
		"runtimeId":   runtime.ID,
		"url":         runtime.URL,
		"serviceName": runtime.ServiceName,
		"status":      runtime.Status,
		"ready":       ready,
		"checks":      checks,
	}
	if runtime.Access.Username != "" || runtime.Access.Password != "" {
		body["access"] = map[string]any{
			"account":           runtime.Access.Username,
			"username":          runtime.Access.Username,
			"password":          runtime.Access.Password,
			"credentialStatus":  runtime.Access.CredentialStatus,
			"credentialVersion": runtime.Access.CredentialVersion,
			"secretRef":         runtime.Access.SecretRef,
		}
	}
	return body
}
