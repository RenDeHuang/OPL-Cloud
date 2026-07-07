package server

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
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

func NewPersistentServer(service *controlplane.Service, store ReadModelStore) (http.Handler, error) {
	app, err := newRuntimeAppWithStore(store)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/w/", app.proxyWorkspace)
	mux.HandleFunc("/login", app.proxyWorkspaceRoot)
	mux.HandleFunc("/logout", app.proxyWorkspaceRoot)
	mux.HandleFunc("/api/", app.proxyWorkspaceRoot)
	mux.HandleFunc("/ws", app.proxyWorkspaceRoot)
	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		payload, sessionID, err := app.login(decodeJSON(r))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		http.SetCookie(w, sessionCookie(sessionID, 12*60*60))
		w.Header().Set("x-opl-csrf-token", stringValue(payload["csrfToken"]))
		writeJSON(w, http.StatusOK, payload)
	})
	mux.HandleFunc("POST /api/auth/operator-login", func(w http.ResponseWriter, r *http.Request) {
		expectedToken := strings.TrimSpace(os.Getenv("OPL_OPERATOR_SUMMARY_TOKEN"))
		if expectedToken == "" || r.Header.Get("x-opl-operator-token") != expectedToken {
			writeError(w, http.StatusUnauthorized, "operator_token_invalid")
			return
		}
		payload, sessionID, err := app.operatorLogin()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "operator_session_failed")
			return
		}
		http.SetCookie(w, sessionCookie(sessionID, 12*60*60))
		w.Header().Set("x-opl-csrf-token", stringValue(payload["csrfToken"]))
		writeJSON(w, http.StatusOK, payload)
	})
	mux.HandleFunc("GET /api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		payload, ok := app.session(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not_authenticated")
			return
		}
		writeJSON(w, http.StatusOK, payload)
	})
	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		app.logout(r)
		http.SetCookie(w, sessionCookie("", -1))
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		payload, ok := app.session(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not_authenticated")
			return
		}
		writeJSON(w, http.StatusOK, payload["user"])
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, app.state(r.URL.Query().Get("accountId")))
	})
	mux.HandleFunc("GET /api/management/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, app.managementState())
	})
	mux.HandleFunc("GET /api/operator/summary", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, app.operatorSummary())
	})
	mux.HandleFunc("GET /api/runtime/readiness", func(w http.ResponseWriter, r *http.Request) {
		readiness, err := service.RuntimeReadiness(r.Context())
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, readiness)
	})
	mux.HandleFunc("GET /api/production/readiness", func(w http.ResponseWriter, r *http.Request) {
		readiness, err := service.RuntimeReadiness(r.Context())
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		readiness["checks"] = []any{}
		writeJSON(w, http.StatusOK, readiness)
	})
	mux.HandleFunc("GET /api/overview", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"service": "control-plane", "workspaces": 0})
	})
	mux.HandleFunc("GET /api/workspaces", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, app.state(r.URL.Query().Get("accountId"))["workspaces"])
	})
	mux.HandleFunc("POST /api/workspaces/reset-token", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		workspace, ok, err := app.setWorkspaceAccess(stringField(input, "workspaceId", ""), "active")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read_model_persist_failed")
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "workspace_not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": workspace["id"], "tokenStatus": nested(workspace, "access", "tokenStatus"), "access": workspace["access"]})
	})
	mux.HandleFunc("POST /api/workspaces/delete-token", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		workspace, ok, err := app.setWorkspaceAccess(stringField(input, "workspaceId", ""), "disabled")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read_model_persist_failed")
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "workspace_not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": workspace["id"], "tokenStatus": nested(workspace, "access", "tokenStatus"), "access": workspace["access"]})
	})
	mux.HandleFunc("POST /api/workspaces/runtime-status", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		runtime, err := service.WorkspaceRuntimeStatus(r.Context(), stringField(input, "workspaceId", ""))
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, workspaceRuntimeStatusResponse(runtime))
	})
	mux.HandleFunc("POST /api/workspaces", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		attachmentID := stringField(input, "attachmentId", "")
		attachment, ok := app.getAttachment(attachmentID)
		if !ok {
			writeError(w, http.StatusBadRequest, "attached_compute_storage_required")
			return
		}
		computeID := stringValue(attachment["computeAllocationId"])
		storageID := stringValue(attachment["storageId"])
		workspace, err := service.CreateWorkspace(r.Context(), controlplane.CreateWorkspaceInput{
			AccountID:    stringField(input, "accountId", "acct-local"),
			OwnerID:      firstNonEmpty(stringField(input, "ownerId", ""), stringField(input, "ownerUserId", "")),
			Name:         firstNonEmpty(stringField(input, "name", ""), stringField(input, "workspaceName", "Workspace")),
			PackageID:    firstNonEmpty(stringField(input, "packageId", ""), stringValue(attachment["packageId"]), "basic"),
			AttachmentID: attachmentID,
			ComputeID:    computeID,
			VolumeID:     storageID,
		}, mutationKey(r, input))
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		if err := app.rememberWorkspaceProjection(workspace); err != nil {
			writeError(w, http.StatusInternalServerError, "read_model_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, workspaceResponse(structToMap(workspace)))
	})
	mux.HandleFunc("GET /api/billing/summary", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"currency": "CNY", "balanceCents": 0})
	})
	mux.HandleFunc("POST /api/billing/topups", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		idempotencyKey := firstNonEmpty(r.Header.Get("Idempotency-Key"), stringField(input, "idempotencyKey", ""))
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		result, err := service.ManualTopUp(r.Context(), controlplane.ManualTopUpInput{
			AccountID:      stringField(input, "accountId", "acct-local"),
			AmountCents:    moneyToCents(input),
			Currency:       stringField(input, "currency", "CNY"),
			OperatorUserID: stringField(input, "operatorUserId", "operator"),
			Reason:         stringField(input, "reason", ""),
		}, idempotencyKey)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		if err := app.rememberManualTopUp(result); err != nil {
			writeError(w, http.StatusInternalServerError, "read_model_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, manualTopUpResponse(result))
	})
	mux.HandleFunc("POST /api/billing/resource-settlements", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		idempotencyKey := firstNonEmpty(r.Header.Get("Idempotency-Key"), stringField(input, "idempotencyKey", ""), stringField(input, "sourceEventId", ""))
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		settlement := controlplane.ResourceSettlementInput{
			AccountID:    stringField(input, "accountId", "acct-local"),
			WorkspaceID:  stringField(input, "workspaceId", ""),
			ResourceType: stringField(input, "resourceType", "compute"),
			ResourceID:   firstNonEmpty(stringField(input, "resourceId", ""), stringField(input, "computeAllocationId", ""), stringField(input, "storageId", "")),
			AmountCents:  settlementAmountCents(input),
			Currency:     stringField(input, "currency", "CNY"),
		}
		result, err := service.SettleResource(r.Context(), settlement, idempotencyKey)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		result = completeSettlementResult(result, settlement)
		if err := app.rememberResourceSettlement(result); err != nil {
			writeError(w, http.StatusInternalServerError, "read_model_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, settlementResponse(result))
	})
	mux.HandleFunc("POST /api/billing/reconciliation", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
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
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, reconciliationResponse(result))
	})
	mux.HandleFunc("GET /api/compute-pools", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, computePools())
	})
	mux.HandleFunc("GET /api/compute-allocations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, app.state(r.URL.Query().Get("accountId"))["computeAllocations"])
	})
	mux.HandleFunc("POST /api/compute-allocations", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		compute, err := service.CreateComputeAllocation(r.Context(), controlplane.ComputeAllocationInput{
			AccountID:       stringField(input, "accountId", "acct-local"),
			WorkspaceID:     stringField(input, "workspaceId", ""),
			PackageID:       stringField(input, "packageId", "basic"),
			HoldAmountCents: computeHoldAmountCents(stringField(input, "packageId", "basic")),
		}, mutationKey(r, input))
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		body := computeResponse(structToMap(compute))
		if err := app.rememberCompute(body); err != nil {
			writeError(w, http.StatusInternalServerError, "read_model_persist_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, body)
	})
	mux.HandleFunc("GET /api/compute-allocations/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		compute, ok := app.getCompute(id)
		if ok && stringValue(compute["status"]) != "provisioning" {
			writeJSON(w, http.StatusOK, compute)
			return
		}
		fresh, err := service.GetComputeAllocation(r.Context(), id)
		if err == nil && fresh.ID != "" {
			body := computeResponse(structToMap(fresh))
			if err := app.rememberCompute(body); err != nil {
				writeError(w, http.StatusInternalServerError, "read_model_persist_failed")
				return
			}
			writeJSON(w, http.StatusOK, body)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "compute_allocation_not_found")
			return
		}
		writeJSON(w, http.StatusOK, compute)
	})
	mux.HandleFunc("POST /api/compute-allocations/{id}/destroy", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		id := strings.TrimSpace(r.PathValue("id"))
		existing, _ := app.getCompute(id)
		compute, err := service.DestroyComputeAllocation(r.Context(), destroyResourceInput(id, existing), mutationKey(r, input))
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		body := computeResponse(structToMap(compute))
		if err := app.rememberCompute(body); err != nil {
			writeError(w, http.StatusInternalServerError, "read_model_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	})
	mux.HandleFunc("POST /api/storage-volumes", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		storage, err := service.CreateStorageVolume(r.Context(), controlplane.StorageVolumeInput{
			AccountID:       stringField(input, "accountId", "acct-local"),
			WorkspaceID:     stringField(input, "workspaceId", ""),
			SizeGB:          int(numberField(input, "sizeGb", 10)),
			HoldAmountCents: storageHoldAmountCents(stringField(input, "packageId", "basic"), numberField(input, "sizeGb", 10)),
		}, mutationKey(r, input))
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		body := storageResponse(structToMap(storage))
		if err := app.rememberStorage(body); err != nil {
			writeError(w, http.StatusInternalServerError, "read_model_persist_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, body)
	})
	mux.HandleFunc("POST /api/storage-volumes/destroy", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		id := stringField(input, "storageId", "")
		existing, _ := app.getStorage(id)
		storage, err := service.DestroyStorageVolume(r.Context(), destroyResourceInput(id, existing), mutationKey(r, input))
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		body := storageResponse(structToMap(storage))
		if err := app.rememberStorage(body); err != nil {
			writeError(w, http.StatusInternalServerError, "read_model_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	})
	mux.HandleFunc("POST /api/storage-attachments", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		attachment, err := service.CreateStorageAttachment(r.Context(), controlplane.StorageAttachmentInput{
			WorkspaceID: stringField(input, "workspaceId", ""),
			ComputeID:   stringField(input, "computeAllocationId", ""),
			VolumeID:    stringField(input, "storageId", ""),
		}, mutationKey(r, input))
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		body := attachmentResponse(structToMap(attachment), input)
		if err := app.rememberAttachment(body, input); err != nil {
			writeError(w, http.StatusInternalServerError, "read_model_persist_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, body)
	})
	mux.HandleFunc("POST /api/storage-attachments/detach", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		attachment, err := service.DetachStorageAttachment(r.Context(), stringField(input, "attachmentId", ""), mutationKey(r, input))
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		body := attachmentResponse(structToMap(attachment), input)
		if err := app.rememberAttachment(body, input); err != nil {
			writeError(w, http.StatusInternalServerError, "read_model_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	})
	mux.HandleFunc("GET /api/support/tickets", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"tickets": []any{}})
	})
	mux.HandleFunc("POST /api/support/tickets", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		writeJSON(w, http.StatusCreated, map[string]any{"id": "ticket-local", "title": stringField(input, "title", "Support"), "status": "open"})
	})
	mux.HandleFunc("GET /api/ledger/task-receipts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"receipts": []any{}})
	})
	mux.HandleFunc("POST /api/organizations", func(w http.ResponseWriter, r *http.Request) {
		body, err := app.createOrganization(decodeJSON(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read_model_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, body)
	})
	mux.HandleFunc("POST /api/organizations/members", func(w http.ResponseWriter, r *http.Request) {
		body, err := app.createMembership(decodeJSON(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read_model_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, body)
	})
	mux.HandleFunc("POST /api/users", func(w http.ResponseWriter, r *http.Request) {
		body, err := app.createUser(decodeJSON(r))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, body)
	})
	mux.HandleFunc("POST /api/users/disable", func(w http.ResponseWriter, r *http.Request) {
		body, err := app.setUserStatus(decodeJSON(r), "disabled")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read_model_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	})
	mux.HandleFunc("POST /api/users/delete", func(w http.ResponseWriter, r *http.Request) {
		body, err := app.setUserStatus(decodeJSON(r), "deleted")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read_model_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	})
	mux.HandleFunc("POST /api/operator/cleanup-workspace-access", func(w http.ResponseWriter, r *http.Request) {
		result, err := app.cleanupWorkspaceAccess(decodeJSON(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read_model_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /api/admin/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"service": "control-plane", "status": "ok"})
	})
	return mux, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
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
	row["access"] = map[string]any{"tokenStatus": "active", "requiresLogin": false}
	return row
}

func billingStatusFor(row map[string]any) string {
	if status := stringValue(row["billingStatus"]); status != "" {
		return status
	}
	switch stringValue(row["status"]) {
	case "destroyed", "detached", "failed":
		return "stopped"
	default:
		return "active"
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
		"id":                  result.ID,
		"accountId":           result.AccountID,
		"workspaceId":         result.WorkspaceID,
		"resourceType":        result.ResourceType,
		"resourceId":          result.ResourceID,
		"amount":              float64(result.AmountCents) / 100,
		"amountCents":         result.AmountCents,
		"status":              result.Status,
		"ledgerEntryId":       result.LedgerEntryID,
		"walletTransactionId": result.WalletTransactionID,
		"wallet":              result.Wallet,
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
	return map[string]any{
		"provider":    "tencent-tke",
		"workspaceId": runtime.WorkspaceID,
		"runtimeId":   runtime.ID,
		"url":         runtime.URL,
		"serviceName": runtime.ServiceName,
		"status":      runtime.Status,
		"ready":       ready,
		"checks":      checks,
	}
}
