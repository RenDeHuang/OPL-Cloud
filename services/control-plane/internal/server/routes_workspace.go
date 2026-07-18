package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/controlplane"
	"opl-cloud/services/control-plane/internal/domain"
)

func registerWorkspaceRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("GET /api/workspaces", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		user, ok := app.sessionUserContext(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not_authenticated")
			return
		}
		rows, err := app.tables.ListWorkspaces(r.Context(), stringValue(user["accountId"]))
		if err != nil {
			writeSourceEnvelope(w, http.StatusInternalServerError, "control-plane", "unavailable", nil)
			return
		}
		items := make([]any, 0, len(rows))
		for _, row := range rows {
			item, ok := workspaceSourceProjection(row)
			if !ok {
				writeSourceEnvelope(w, http.StatusInternalServerError, "control-plane", "unavailable", nil)
				return
			}
			items = append(items, item)
		}
		status := "available"
		if len(items) == 0 {
			status = "empty"
		}
		writeSourceEnvelope(w, http.StatusOK, "control-plane", status, map[string]any{"items": items, "total": len(items)})
	}))
	mux.HandleFunc("POST /api/workspaces/runtime-status", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		workspaceID := stringField(input, "workspaceId", "")
		user, ok := app.sessionUserContext(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not_authenticated")
			return
		}
		accountID := stringValue(user["accountId"])
		workspace, ok, err := app.workspaceForSource(r.Context(), accountID, workspaceID)
		if err != nil {
			writeSourceEnvelope(w, http.StatusInternalServerError, "fabric", "unavailable", nil)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "workspace_not_found")
			return
		}
		if !app.canAccessResource(r, workspace) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		if !app.workspaceAccessAllowed(w, workspace) {
			return
		}
		unlock := app.lockEntitlementResources(
			firstNonEmpty(stringValue(workspace["currentComputeAllocationId"]), stringValue(workspace["computeAllocationId"])),
			stringValue(workspace["storageId"]),
			firstNonEmpty(stringValue(workspace["currentAttachmentId"]), stringValue(workspace["attachmentId"])),
		)
		defer unlock()
		workspace, ok, err = app.workspaceForSource(r.Context(), accountID, workspaceID)
		if err != nil {
			writeSourceEnvelope(w, http.StatusInternalServerError, "fabric", "unavailable", nil)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "workspace_not_found")
			return
		}
		if !app.canAccessResource(r, workspace) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		if !app.workspaceAccessAllowed(w, workspace) {
			return
		}
		switch stringValue(workspace["state"]) {
		case "suspended", "stopped":
			writeError(w, http.StatusConflict, "workspace_suspended")
			return
		case "data_deleted", "unrecoverable", "storage_missing", "destroyed":
			writeError(w, http.StatusGone, "workspace_storage_destroyed")
			return
		}
		runtime, err := service.WorkspaceRuntimeStatus(r.Context(), workspaceID)
		if err != nil {
			writeSourceEnvelope(w, http.StatusBadGateway, "fabric", "unavailable", nil)
			return
		}
		body, ok := workspaceRuntimeStatusResponse(runtime, workspaceID)
		if !ok {
			writeSourceEnvelope(w, http.StatusBadGateway, "fabric", "unavailable", nil)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeSourceEnvelope(w, http.StatusOK, "fabric", "available", body)
	}))
	mux.HandleFunc("POST /api/workspaces/{workspaceId}/runtime-credentials/reveal", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspaceId")
		if _, ok := app.ownedWorkspaceForCredentialCommand(w, r, workspaceID); !ok {
			return
		}
		runtime, err := service.WorkspaceRuntimeStatus(r.Context(), workspaceID)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		if !runtime.Ready || runtime.Status == "not_found" || runtime.Access.Password == "" {
			writeError(w, http.StatusConflict, "workspace_credentials_unavailable")
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, workspaceRuntimeCredentialResponse(runtime))
	}))
	mux.HandleFunc("POST /api/workspaces/{workspaceId}/runtime-credentials/rotate", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		workspaceID := r.PathValue("workspaceId")
		workspace, ok := app.ownedWorkspaceForCredentialCommand(w, r, workspaceID)
		if !ok {
			return
		}
		key, ok := requiredMutationKey(w, r)
		if !ok {
			return
		}
		unlock := app.lockResource("runtime-credential", workspaceID)
		defer unlock()
		workspace, ok = app.ownedWorkspaceForCredentialCommand(w, r, workspaceID)
		if !ok {
			return
		}
		if app.workspaceResponse(cloneMap(workspace))["openable"] != true {
			writeError(w, http.StatusConflict, "workspace_not_running")
			return
		}
		accountID := firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"]))
		sub2APIUserID, ok := app.mappedSub2APIUserID(w, r, accountID)
		if !ok {
			return
		}
		runtime, receipt, err := service.RotateWorkspaceCredential(r.Context(), controlplane.RotateWorkspaceCredentialInput{
			WorkspaceID: workspaceID, AccountID: accountID, Sub2APIUserID: sub2APIUserID,
			OwnerID:   firstNonEmpty(stringValue(workspace["ownerUserId"]), stringValue(workspace["ownerId"])),
			ComputeID: firstNonEmpty(stringValue(workspace["currentComputeAllocationId"]), stringValue(workspace["computeAllocationId"])),
			VolumeID:  stringValue(workspace["storageId"]),
		}, key)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		access := cloneMap(mapField(workspace, "access"))
		delete(access, "password")
		access["account"], access["username"] = runtime.Access.Username, runtime.Access.Username
		access["credentialStatus"] = runtime.Access.CredentialStatus
		access["credentialVersion"] = runtime.Access.CredentialVersion
		access["secretRef"] = runtime.Access.SecretRef
		workspace["access"] = access
		workspace["runtimeId"] = firstNonEmpty(runtime.ID, stringValue(workspace["runtimeId"]))
		runtimeProjection := cloneMap(mapField(workspace, "runtime"))
		runtimeProjection["serviceName"] = firstNonEmpty(runtime.ServiceName, stringValue(runtimeProjection["serviceName"]))
		runtimeProjection["status"], runtimeProjection["ready"] = runtime.Status, runtime.Ready
		workspace["runtime"] = runtimeProjection
		if err := app.tables.SaveWorkspace(r.Context(), workspace); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		body := workspaceRuntimeCredentialResponse(runtime)
		body["receiptId"] = receipt.ReceiptID
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("POST /api/workspaces/{workspaceId}/gateway-secret/rotate", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		key, ok := requiredMutationKey(w, r)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		workspace, ok := app.ownedWorkspaceForCredentialCommand(w, r, workspaceID)
		if !ok {
			return
		}
		accountID := firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"]))
		unlock := app.lockResource("gateway-secret", accountID)
		defer unlock()
		workspace, ok = app.ownedWorkspaceForCredentialCommand(w, r, workspaceID)
		if !ok {
			return
		}
		if app.workspaceResponse(cloneMap(workspace))["openable"] != true {
			writeError(w, http.StatusConflict, "workspace_not_running")
			return
		}
		requestHash := stableID(accountID, workspaceID, string(mustJSON(input)))
		operationID := "gateway-secret-" + stableID(workspaceID, key)[:18]
		operations, err := app.tables.ListRuntimeOperations(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		for _, operation := range operations {
			if stringValue(operation["id"]) != operationID {
				continue
			}
			if stringValue(operation["accountId"]) != accountID || stringValue(operation["workspaceId"]) != workspaceID || stringValue(operation["action"]) != "workspace.gateway_secret.rotate" {
				writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
				return
			}
			result, err := decodeWorkspaceGatewaySecretOperation(operation)
			if err != nil || stringValue(operation["status"]) != "succeeded" {
				writeError(w, http.StatusInternalServerError, "state_read_failed")
				return
			}
			if result.RequestHash != requestHash {
				writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
				return
			}
			w.Header().Set("Cache-Control", "private, no-store")
			writeJSON(w, http.StatusOK, map[string]any{"operationId": operationID, "workspaceId": workspaceID, "status": "succeeded", "secretRef": result.SecretRef, "fingerprint": result.Fingerprint})
			return
		}
		sub2APIUserID, ok := app.mappedSub2APIUserID(w, r, accountID)
		if !ok {
			return
		}
		secret, err := service.SyncWorkspaceGatewaySecret(r.Context(), accountID, sub2APIUserID, operationID)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		result := workspaceGatewaySecretOperationResult{RequestHash: requestHash, SecretRef: secret.SecretRef, Fingerprint: secret.Fingerprint}
		operation := map[string]any{
			"id": operationID, "operationId": operationID, "accountId": accountID, "workspaceId": workspaceID,
			"resourceId": workspaceID, "resourceKind": "gateway_secret", "action": "workspace.gateway_secret.rotate",
			"provider": "tencent-tke", "status": "succeeded", "result": encodeWorkspaceGatewaySecretOperation(result),
		}
		if err := app.tables.SaveRuntimeOperation(r.Context(), operation); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{"operationId": operationID, "workspaceId": workspaceID, "status": "succeeded", "secretRef": result.SecretRef, "fingerprint": result.Fingerprint})
	}))
	mux.HandleFunc("POST /api/workspaces/{workspaceId}/auto-renew", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		key, ok := requiredMutationKey(w, r)
		if !ok {
			return
		}
		autoRenew, ok := input["autoRenew"].(bool)
		if !ok {
			writeError(w, http.StatusBadRequest, "autoRenew_required")
			return
		}
		if autoRenew {
			writeError(w, http.StatusConflict, "autoRenew_unavailable")
			return
		}
		workspaceID := r.PathValue("workspaceId")
		workspace, ok := app.getWorkspace(workspaceID)
		if !ok {
			writeError(w, http.StatusNotFound, "workspace_not_found")
			return
		}
		if !app.canAccessResource(r, workspace) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		user, ok := app.sessionUserContext(r)
		if !ok || stringValue(user["role"]) != "owner" || firstNonEmpty(stringValue(workspace["ownerUserId"]), stringValue(workspace["ownerId"])) != stringValue(user["id"]) {
			writeError(w, http.StatusForbidden, "workspace_owner_required")
			return
		}
		operationID := workspaceAutoRenewCommandID(workspaceID, key)
		requestHash := workspaceAutoRenewRequestHash(workspaceID, autoRenew)
		for range 3 {
			workspace, ok = app.getWorkspace(workspaceID)
			if !ok || !app.canAccessResource(r, workspace) {
				writeError(w, http.StatusForbidden, "account_scope_forbidden")
				return
			}
			operations, err := app.tables.ListRuntimeOperations(r.Context())
			if err != nil {
				writeError(w, http.StatusInternalServerError, "state_read_failed")
				return
			}
			for _, operation := range operations {
				if stringValue(operation["id"]) != operationID {
					continue
				}
				result, err := decodeWorkspaceAutoRenewCommand(operation)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "state_read_failed")
					return
				}
				if result.RequestHash != requestHash {
					writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
					return
				}
				writeJSON(w, http.StatusOK, result.Response)
				return
			}
			update, response, err := planWorkspaceRenewalIntent(workspace, user, operations, autoRenew, key, time.Now().UTC())
			if errors.Is(err, errWorkspaceReactivationRequired) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			if err != nil {
				writeError(w, http.StatusConflict, "workspace_billing_state_invalid")
				return
			}
			before := workspaceRenewalIntentState(workspace["autoRenew"] == true, stringValue(workspace["authorizedBy"]), stringValue(workspace["authorizedAt"]))
			after := workspaceRenewalIntentState(update.WorkspacePatch.AutoRenew, update.WorkspacePatch.AuthorizedBy, update.WorkspacePatch.AuthorizedAt)
			update.AuditEvent = bindWorkspaceAutoRenewAudit(update.CommandOperation, app.auditEvent(r, "workspace.auto_renew", "workspace", workspaceID, stringValue(workspace["accountId"]), before, after, "succeeded"))
			if err := app.tables.ApplyWorkspaceRenewalIntent(r.Context(), update); errors.Is(err, errWorkspaceRenewalCASConflict) {
				continue
			} else if err != nil {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
			writeJSON(w, http.StatusOK, response)
			return
		}
		writeError(w, http.StatusConflict, errWorkspaceRenewalCASConflict.Error())
	}))
	mux.HandleFunc("POST /api/workspaces/{workspaceId}/resume", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		workspaceID := r.PathValue("workspaceId")
		workspace, ok := app.getWorkspace(workspaceID)
		if !ok {
			writeError(w, http.StatusNotFound, "workspace_not_found")
			return
		}
		if !app.canAccessResource(r, workspace) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		user, ok := app.sessionUserContext(r)
		if !ok || stringValue(user["role"]) != "owner" || firstNonEmpty(stringValue(workspace["ownerUserId"]), stringValue(workspace["ownerId"])) != stringValue(user["id"]) {
			writeError(w, http.StatusForbidden, "workspace_owner_required")
			return
		}
		key, ok := executionMutationKey(w, r)
		if !ok {
			return
		}
		requestHash := stableID(string(mustJSON(input)))
		operationID := "resume-" + stableID(workspaceID, key)[:18]
		operations, err := app.tables.ListRuntimeOperations(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		for _, operation := range operations {
			if stringValue(operation["id"]) != operationID {
				continue
			}
			result, err := decodeWorkspaceResumeOperation(operation)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "state_read_failed")
				return
			}
			if result.RequestHash != requestHash {
				writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
				return
			}
			if stringValue(operation["status"]) == "succeeded" && result.Response != nil {
				writeJSON(w, http.StatusOK, result.Response)
				return
			}
			if stringValue(operation["status"]) == "receipt_pending" && result.Workspace != nil {
				before := cloneMap(workspace)
				resumed, err := service.RecordWorkspaceResumedReceipt(r.Context(), *result.Workspace, key)
				if err != nil {
					writeUpstreamError(w, err)
					return
				}
				workspace = applyWorkspaceRuntimeProjection(workspace, resumed)
				body := app.workspaceResponse(cloneMap(workspace))
				result.LeaseExpiresAt = nil
				result.Response, result.Workspace = body, &resumed
				operation = cloneMap(operation)
				operation["status"] = "succeeded"
				operation["result"] = encodeWorkspaceResumeOperation(result)
				audit := app.auditEvent(r, "workspace.resume", "workspace", workspaceID, firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"])), before, body, "succeeded")
				if err := app.tables.CommitWorkspaceResume(r.Context(), workspace, audit, operation); err != nil {
					writeError(w, http.StatusInternalServerError, "state_persist_failed")
					return
				}
				writeJSON(w, http.StatusOK, body)
				return
			}
			break
		}
		accountID := firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"]))
		sub2APIUserID, ok := app.mappedSub2APIUserID(w, r, accountID)
		if !ok {
			return
		}
		storageID := stringValue(workspace["storageId"])
		computeID := stringField(input, "computeAllocationId", "")
		attachmentID := stringField(input, "attachmentId", "")
		unlock := app.lockEntitlementResources(computeID, storageID, attachmentID)
		defer unlock()
		workspace, ok = app.getWorkspace(workspaceID)
		if !ok {
			writeError(w, http.StatusNotFound, "workspace_not_found")
			return
		}
		storage, storageOK := app.getStorage(storageID)
		compute, computeOK := app.getCompute(computeID)
		attachment, attachmentOK := app.getAttachment(attachmentID)
		if !storageOK || !computeOK || !attachmentOK {
			writeError(w, http.StatusConflict, "resume_resources_not_ready")
			return
		}
		storageStatus := stringValue(storage["status"])
		computeStatus := stringValue(compute["status"])
		attachmentStatus := stringValue(attachment["status"])
		if !app.resourceBelongsToAccount(storage, accountID) || !app.resourceBelongsToAccount(compute, accountID) || !app.resourceBelongsToAccount(attachment, accountID) ||
			stringValue(storage["workspaceId"]) != workspaceID || stringValue(compute["workspaceId"]) != workspaceID || stringValue(attachment["workspaceId"]) != workspaceID ||
			firstNonEmpty(stringValue(attachment["computeAllocationId"]), stringValue(attachment["computeId"])) != computeID || firstNonEmpty(stringValue(attachment["storageId"]), stringValue(attachment["volumeId"])) != storageID {
			writeError(w, http.StatusConflict, "resume_resource_mismatch")
			return
		}
		if (storageStatus != "ready" && storageStatus != "available") || (computeStatus != "running" && computeStatus != "ready" && computeStatus != "available" && computeStatus != "active") || (attachmentStatus != "attached" && attachmentStatus != "ready") {
			writeError(w, http.StatusConflict, "resume_resources_not_ready")
			return
		}
		if !ensureMonthlyEntitlements(w, time.Now(), compute, storage) {
			return
		}
		before := cloneMap(workspace)
		leaseExpiresAt := time.Now().UTC().Add(2 * time.Minute)
		claimOperation := map[string]any{
			"id": operationID, "operationId": operationID, "accountId": accountID, "workspaceId": workspaceID, "resourceId": workspaceID, "resourceKind": "workspace_runtime", "action": "workspace.resume",
			"provider": stringValue(workspace["provider"]), "status": "started", "computeAllocationId": computeID, "storageId": storageID, "attachmentId": attachmentID,
			"result": encodeWorkspaceResumeOperation(workspaceResumeOperationResult{RequestHash: requestHash, LeaseExpiresAt: &leaseExpiresAt}),
		}
		replayed, replay, err := app.tables.ClaimWorkspaceResume(r.Context(), workspaceID, claimOperation)
		if err != nil {
			switch {
			case errors.Is(err, errIdempotencyConflict):
				writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
			case errors.Is(err, errWorkspaceResumeInProgress):
				writeError(w, http.StatusConflict, errWorkspaceResumeInProgress.Error())
			case errors.Is(err, errWorkspaceNotSuspended):
				writeError(w, http.StatusConflict, errWorkspaceNotSuspended.Error())
			default:
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
			}
			return
		}
		if replay {
			writeJSON(w, http.StatusOK, replayed)
			return
		}
		state := firstNonEmpty(stringValue(workspace["state"]), stringValue(workspace["status"]))
		if state != "suspended" && state != "stopped" {
			_ = app.tables.FailWorkspaceResume(r.Context(), workspaceID, operationID, "workspace_not_suspended")
			writeError(w, http.StatusConflict, "workspace_not_suspended")
			return
		}
		resumed, err := service.PrepareWorkspaceResume(r.Context(), controlplane.ResumeWorkspaceInput{WorkspaceID: workspaceID, AccountID: accountID, Sub2APIUserID: sub2APIUserID, OwnerID: stringValue(workspace["ownerUserId"]), Name: stringValue(workspace["name"]), PackageID: stringValue(workspace["packageId"]), URL: stringValue(workspace["url"]), AttachmentID: attachmentID, ComputeID: computeID, VolumeID: storageID}, key)
		if err != nil {
			if failErr := app.tables.FailWorkspaceResume(r.Context(), workspaceID, operationID, "fabric_resume_failed"); failErr != nil {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
			writeUpstreamError(w, err)
			return
		}
		resumed, err = service.RecordWorkspaceResumedReceipt(r.Context(), resumed, key)
		if err != nil {
			workspace = applyWorkspaceRuntimeProjection(workspace, resumed)
			body := app.workspaceResponse(cloneMap(workspace))
			operation := map[string]any{"id": operationID, "operationId": operationID, "accountId": accountID, "workspaceId": workspaceID, "resourceId": workspaceID, "resourceKind": "workspace_runtime", "action": "workspace.resume", "provider": stringValue(workspace["provider"]), "providerRequestId": resumed.RuntimeID, "status": "receipt_pending", "result": encodeWorkspaceResumeOperation(workspaceResumeOperationResult{RequestHash: requestHash, Response: body, Workspace: &resumed}), "computeAllocationId": resumed.ComputeID, "storageId": resumed.VolumeID, "attachmentId": resumed.AttachmentID, "runtimeServiceName": resumed.RuntimeServiceName}
			if saveErr := app.tables.SaveRuntimeOperation(r.Context(), operation); saveErr != nil {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
			if saveErr := app.tables.SaveWorkspace(r.Context(), workspace); saveErr != nil {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
			writeUpstreamError(w, err)
			return
		}
		workspace = applyWorkspaceRuntimeProjection(workspace, resumed)
		body := app.workspaceResponse(cloneMap(workspace))
		operation := map[string]any{"id": operationID, "operationId": operationID, "accountId": accountID, "workspaceId": workspaceID, "resourceId": workspaceID, "resourceKind": "workspace_runtime", "action": "workspace.resume", "provider": stringValue(workspace["provider"]), "providerRequestId": resumed.RuntimeID, "status": "succeeded", "result": encodeWorkspaceResumeOperation(workspaceResumeOperationResult{RequestHash: requestHash, Response: body, Workspace: &resumed}), "computeAllocationId": resumed.ComputeID, "storageId": resumed.VolumeID, "attachmentId": resumed.AttachmentID, "runtimeServiceName": resumed.RuntimeServiceName}
		audit := app.auditEvent(r, "workspace.resume", "workspace", workspaceID, accountID, before, body, "succeeded")
		if err := app.tables.CommitWorkspaceResume(r.Context(), workspace, audit, operation); err != nil {
			_ = app.tables.FailWorkspaceResume(r.Context(), workspaceID, operationID, "resume_commit_failed")
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("POST /api/workspaces", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		accountID, ok := app.scopedAccountID(w, r, input)
		if !ok {
			return
		}
		user, ok := app.sessionUserContext(r)
		if !ok || stringValue(user["role"]) != "owner" {
			writeError(w, http.StatusForbidden, "workspace_owner_required")
			return
		}
		ownerID := stringValue(user["id"])
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
		unlock := app.lockEntitlementResources(computeID, storageID, attachmentID)
		defer unlock()
		attachment, ok = app.getAttachment(attachmentID)
		if !ok || stringValue(attachment["computeAllocationId"]) != computeID || stringValue(attachment["storageId"]) != storageID {
			writeError(w, http.StatusConflict, "attached_compute_storage_required")
			return
		}
		compute, computeOK := app.getCompute(computeID)
		storage, storageOK := app.getStorage(storageID)
		if !computeOK || !storageOK || !ensureMonthlyEntitlements(w, time.Now(), compute, storage) {
			if !computeOK || !storageOK {
				writeError(w, http.StatusConflict, "monthly_entitlement_inactive")
			}
			return
		}
		workspaceID := stringValue(attachment["workspaceId"])
		packageID := stringValue(compute["packageId"])
		if workspaceID == "" || stringValue(compute["workspaceId"]) != workspaceID || stringValue(storage["workspaceId"]) != workspaceID ||
			packageID == "" || stringValue(storage["packageId"]) != packageID || (stringValue(attachment["packageId"]) != "" && stringValue(attachment["packageId"]) != packageID) {
			writeError(w, http.StatusConflict, "compute_storage_workspace_mismatch")
			return
		}
		name := firstNonEmpty(stringField(input, "name", ""), stringField(input, "workspaceName", "Workspace"))
		requestHash := stableID(accountID, ownerID, name, packageID, attachmentID, computeID, storageID)
		operationID := "create-" + stableID(workspaceID)[:18]
		idempotencyKey := "workspace-create:" + workspaceID
		operations, err := app.tables.ListRuntimeOperations(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		var workspace domain.WorkspaceProjection
		var acceptedBillingState map[string]any
		retryReceipt, retryPrepare := false, false
		for _, operation := range operations {
			if stringValue(operation["id"]) != operationID {
				continue
			}
			result, err := decodeWorkspaceCreateOperation(operation)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "state_read_failed")
				return
			}
			if result.RequestHash != requestHash {
				writeError(w, http.StatusConflict, errPrimaryWorkspaceExists.Error())
				return
			}
			if result.AcceptedBillingState != nil {
				acceptedBillingState = workspaceAcceptedBillingState(workspaceProjectionBillingRow(result.Workspace, result.AcceptedBillingState))
			} else if existing, exists := app.getWorkspace(result.Workspace.ID); exists {
				acceptedBillingState = workspaceAcceptedBillingState(existing)
			}
			if acceptedBillingState == nil {
				writeError(w, http.StatusConflict, "workspace_billing_state_invalid")
				return
			}
			switch stringValue(operation["status"]) {
			case "succeeded":
				existing, exists := app.getWorkspace(result.Workspace.ID)
				if !exists || !workspaceCreateProjectionCompatible(existing, result.Workspace, acceptedBillingState, false) {
					writeError(w, http.StatusConflict, errPrimaryWorkspaceExists.Error())
					return
				}
				writeJSON(w, http.StatusCreated, app.workspaceResponse(existing))
				return
			case "receipt_pending":
				workspace, retryReceipt = result.Workspace, true
			case "started":
				if result.LeaseExpiresAt != nil && result.LeaseExpiresAt.After(time.Now().UTC()) {
					writeError(w, http.StatusConflict, errPrimaryWorkspaceExists.Error())
					return
				}
				workspace, retryPrepare = result.Workspace, true
			case "retryable":
				workspace, retryPrepare = result.Workspace, true
			default:
				writeError(w, http.StatusInternalServerError, "state_read_failed")
				return
			}
			break
		}
		if retryReceipt {
			if existing, exists := app.getWorkspace(workspace.ID); exists && !workspaceCreateProjectionCompatible(existing, workspace, acceptedBillingState, true) {
				writeError(w, http.StatusConflict, errPrimaryWorkspaceExists.Error())
				return
			}
			if err := app.saveWorkspaceProjection(workspace, acceptedBillingState); err != nil {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
			workspace, err = service.RecordWorkspaceCreatedReceipt(r.Context(), workspace, idempotencyKey)
			if err != nil {
				writeUpstreamError(w, err)
				return
			}
		} else {
			if !retryPrepare {
				if existing, exists := app.getWorkspace(workspaceID); exists {
					if firstNonEmpty(stringValue(existing["accountId"]), stringValue(existing["ownerAccountId"])) != accountID ||
						stringValue(existing["attachmentId"]) != attachmentID || firstNonEmpty(stringValue(existing["computeAllocationId"]), stringValue(existing["currentComputeAllocationId"])) != computeID ||
						stringValue(existing["storageId"]) != storageID || stringValue(existing["name"]) != name || stringValue(existing["packageId"]) != packageID ||
						firstNonEmpty(stringValue(existing["ownerId"]), stringValue(existing["ownerUserId"])) != ownerID {
						writeError(w, http.StatusConflict, errPrimaryWorkspaceExists.Error())
						return
					}
					if workspaceAcceptedBillingState(existing) == nil {
						writeError(w, http.StatusConflict, "workspace_billing_state_invalid")
						return
					}
					writeJSON(w, http.StatusCreated, app.workspaceResponse(existing))
					return
				}
				storageGB, validStorageGB := requiredPositiveInteger(storage, "sizeGb")
				billingState, billingCode := workspaceBillingStateFromChildren(compute, storage, workspaceBillingChildIdentity{
					AccountID: accountID, OwnerUserID: ownerID, WorkspaceID: workspaceID, PackageID: packageID,
					ComputeID: computeID, StorageID: storageID, StorageGB: storageGB,
				})
				if !validStorageGB || billingCode != "" {
					writeError(w, http.StatusConflict, "workspace_billing_state_invalid")
					return
				}
				acceptedBillingState = workspaceAcceptedBillingState(billingState)
				if acceptedBillingState == nil {
					writeError(w, http.StatusConflict, "workspace_billing_state_invalid")
					return
				}
				workspace = domain.WorkspaceProjection{
					ID: workspaceID, AccountID: accountID, OwnerID: ownerID, Name: name, PackageID: packageID, Status: "provisioning",
					ComputeID: computeID, VolumeID: storageID, AttachmentID: attachmentID,
				}
			}
			leaseExpiresAt := time.Now().UTC().Add(2 * time.Minute)
			claimResult := workspaceCreateOperationResult{RequestHash: requestHash, LeaseExpiresAt: &leaseExpiresAt, Workspace: workspace, AcceptedBillingState: acceptedBillingState}
			claimOperation := workspaceCreateOperationRow(operationID, "started", claimResult)
			if err := app.tables.ClaimWorkspaceCreate(r.Context(), workspaceProjectionBillingRow(workspace, acceptedBillingState), claimOperation); err != nil {
				if errors.Is(err, errPrimaryWorkspaceExists) {
					writeError(w, http.StatusConflict, errPrimaryWorkspaceExists.Error())
					return
				}
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
			sub2APIUserID, ok := app.mappedSub2APIUserID(w, r, accountID)
			if !ok {
				claimResult.LeaseExpiresAt = nil
				_ = app.tables.SaveRuntimeOperation(r.Context(), workspaceCreateOperationRow(operationID, "retryable", claimResult))
				return
			}
			prepared, err := service.PrepareWorkspace(r.Context(), controlplane.CreateWorkspaceInput{
				WorkspaceID:   workspaceID,
				AccountID:     accountID,
				Sub2APIUserID: sub2APIUserID,
				OwnerID:       ownerID,
				Name:          name,
				PackageID:     packageID,
				AttachmentID:  attachmentID,
				ComputeID:     computeID,
				VolumeID:      storageID,
			}, idempotencyKey)
			if err != nil {
				claimResult.LeaseExpiresAt = nil
				if saveErr := app.tables.SaveRuntimeOperation(r.Context(), workspaceCreateOperationRow(operationID, "retryable", claimResult)); saveErr != nil {
					writeError(w, http.StatusInternalServerError, "state_persist_failed")
					return
				}
				writeUpstreamError(w, err)
				return
			}
			workspace = prepared
			result := workspaceCreateOperationResult{RequestHash: requestHash, Workspace: workspace, AcceptedBillingState: acceptedBillingState}
			if err := app.tables.SaveRuntimeOperation(r.Context(), workspaceCreateOperationRow(operationID, "receipt_pending", result)); err != nil {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
			if err := app.saveWorkspaceProjection(workspace, acceptedBillingState); err != nil {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
			workspace, err = service.RecordWorkspaceCreatedReceipt(r.Context(), workspace, idempotencyKey)
			if err != nil {
				writeUpstreamError(w, err)
				return
			}
		}
		result := workspaceCreateOperationResult{RequestHash: requestHash, Workspace: workspace, AcceptedBillingState: acceptedBillingState}
		if existing, exists := app.getWorkspace(workspace.ID); exists && !workspaceCreateProjectionCompatible(existing, workspace, acceptedBillingState, false) {
			writeError(w, http.StatusConflict, errPrimaryWorkspaceExists.Error())
			return
		}
		if err := app.tables.SaveRuntimeOperation(r.Context(), workspaceCreateOperationRow(operationID, "succeeded", result)); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.saveWorkspaceProjection(workspace, acceptedBillingState); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		body := app.workspaceResponse(workspaceProjectionBillingResponseRow(workspace, acceptedBillingState))
		if err := app.appendAuditEvent(r, "workspace.create", "workspace", workspace.ID, workspace.AccountID, nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, body)
	}))
}

func (app *controlPlaneServer) workspaceForSource(ctx context.Context, accountID, workspaceID string) (map[string]any, bool, error) {
	rows, err := app.tables.ListWorkspaces(ctx, accountID)
	if err != nil {
		return nil, false, err
	}
	workspace := findRecord(rows, workspaceID)
	return workspace, workspace != nil, nil
}

func workspaceSourceProjection(row map[string]any) (map[string]any, bool) {
	item := map[string]any{}
	for _, key := range []string{"id", "ownerAccountId", "ownerUserId", "state", "createdAt", "updatedAt"} {
		value := stringValue(row[key])
		if value == "" {
			return nil, false
		}
		item[key] = value
	}
	if _, err := time.Parse(time.RFC3339, stringValue(item["createdAt"])); err != nil {
		return nil, false
	}
	if _, err := time.Parse(time.RFC3339, stringValue(item["updatedAt"])); err != nil {
		return nil, false
	}
	for _, key := range []string{"name", "url", "storageId", "currentComputeAllocationId", "currentAttachmentId", "runtimeId"} {
		if value := stringValue(row[key]); value != "" {
			item[key] = value
		}
	}
	for _, key := range []string{"packageId", "priceVersion", "currency", "renewalStatus"} {
		if raw, exists := row[key]; exists {
			value, ok := raw.(string)
			if !ok || strings.TrimSpace(value) == "" {
				return nil, false
			}
			item[key] = value
		}
	}
	if packageID := stringValue(item["packageId"]); packageID != "" && packageID != "basic" && packageID != "pro" {
		return nil, false
	}
	if currency := stringValue(item["currency"]); currency != "" && currency != "USD" {
		return nil, false
	}
	for _, key := range []string{"storageGb", "totalUsdMicros"} {
		if _, exists := row[key]; exists {
			value, ok := requiredNonNegativeInteger(row, key)
			if !ok || (key == "storageGb" && value == 0) {
				return nil, false
			}
			item[key] = value
		}
	}
	if raw, exists := row["autoRenew"]; exists {
		value, ok := raw.(bool)
		if !ok {
			return nil, false
		}
		item["autoRenew"] = value
	}
	for _, key := range []string{"periodStart", "paidThrough"} {
		if raw, exists := row[key]; exists {
			value, ok := raw.(string)
			if !ok {
				return nil, false
			}
			if _, err := time.Parse(time.RFC3339, value); err != nil {
				return nil, false
			}
			item[key] = value
		}
	}
	return item, true
}

func workspaceCreateProjectionCompatible(existing map[string]any, projection domain.WorkspaceProjection, acceptedBillingState map[string]any, allowClaim bool) bool {
	expected := workspaceProjectionBillingRow(projection, acceptedBillingState)
	if firstNonEmpty(stringValue(existing["accountId"]), stringValue(existing["ownerAccountId"])) != projection.AccountID ||
		firstNonEmpty(stringValue(existing["ownerUserId"]), stringValue(existing["ownerId"])) != projection.OwnerID ||
		stringValue(existing["name"]) != projection.Name || stringValue(existing["packageId"]) != projection.PackageID ||
		stringValue(existing["computeAllocationId"]) != projection.ComputeID || stringValue(existing["storageId"]) != projection.VolumeID ||
		firstNonEmpty(stringValue(existing["attachmentId"]), stringValue(existing["currentAttachmentId"])) != projection.AttachmentID || !workspaceBillingStateMatchesLaunch(existing, expected) {
		return false
	}
	if allowClaim && firstNonEmpty(stringValue(existing["state"]), stringValue(existing["status"])) == "provisioning" && stringValue(existing["runtimeId"]) == "" {
		return stringValue(existing["currentComputeAllocationId"]) == projection.ComputeID && stringValue(existing["currentAttachmentId"]) == projection.AttachmentID
	}
	return stringValue(existing["state"]) == projection.Status && stringValue(existing["status"]) == projection.Status &&
		stringValue(existing["currentComputeAllocationId"]) == projection.ComputeID && stringValue(existing["currentAttachmentId"]) == projection.AttachmentID &&
		stringValue(existing["runtimeId"]) == projection.RuntimeID && stringValue(existing["url"]) == projection.URL &&
		firstNonEmpty(stringValue(existing["runtimeServiceName"]), stringValue(nested(existing, "runtime", "serviceName"))) == projection.RuntimeServiceName
}

func (app *controlPlaneServer) ownedWorkspaceForCredentialCommand(w http.ResponseWriter, r *http.Request, workspaceID string) (map[string]any, bool) {
	workspace, workspaceOK := app.getWorkspace(workspaceID)
	user, userOK := app.sessionUserContext(r)
	if !workspaceOK || !userOK || !app.canAccessResource(r, workspace) ||
		firstNonEmpty(stringValue(workspace["ownerUserId"]), stringValue(workspace["ownerId"])) != stringValue(user["id"]) {
		writeError(w, http.StatusForbidden, "workspace_owner_required")
		return nil, false
	}
	if !app.workspaceAccessAllowed(w, workspace) {
		return nil, false
	}
	return workspace, true
}

func (app *controlPlaneServer) workspaceAccessAllowed(w http.ResponseWriter, workspace map[string]any) bool {
	_, reason := app.workspaceAccessResponse(cloneMap(workspace), time.Now().UTC())
	if reason != "" {
		writeError(w, http.StatusConflict, reason)
		return false
	}
	return true
}

func workspaceCreateOperationRow(operationID, status string, result workspaceCreateOperationResult) map[string]any {
	workspace := result.Workspace
	return map[string]any{
		"id": operationID, "operationId": operationID, "accountId": workspace.AccountID, "workspaceId": workspace.ID,
		"resourceId": workspace.ID, "resourceKind": "workspace_runtime", "action": "workspace.create", "provider": workspace.Provider,
		"providerRequestId": workspace.RuntimeID, "status": status, "result": encodeWorkspaceCreateOperation(result),
		"computeAllocationId": workspace.ComputeID, "storageId": workspace.VolumeID, "attachmentId": workspace.AttachmentID, "runtimeServiceName": workspace.RuntimeServiceName,
	}
}

func applyWorkspaceRuntimeProjection(workspace map[string]any, resumed domain.WorkspaceProjection) map[string]any {
	workspace["state"], workspace["status"] = resumed.Status, resumed.Status
	workspace["computeAllocationId"], workspace["currentComputeAllocationId"] = resumed.ComputeID, resumed.ComputeID
	workspace["attachmentId"], workspace["currentAttachmentId"] = resumed.AttachmentID, resumed.AttachmentID
	workspace["runtimeId"] = resumed.RuntimeID
	workspace["runtime"] = map[string]any{"serviceName": resumed.RuntimeServiceName, "status": resumed.Status, "ready": resumed.RuntimeReady}
	workspace["runtimeServiceName"], workspace["serviceName"] = resumed.RuntimeServiceName, resumed.RuntimeServiceName
	workspace["receiptId"] = resumed.ReceiptID
	workspace["url"] = resumed.URL
	access := cloneMap(mapField(workspace, "access"))
	delete(access, "tokenStatus")
	delete(access, "requiresLogin")
	if resumed.RuntimeUsername != "" {
		access["account"], access["username"] = resumed.RuntimeUsername, resumed.RuntimeUsername
	}
	if resumed.CredentialStatus != "" {
		access["credentialStatus"] = resumed.CredentialStatus
	}
	if resumed.CredentialVersion != "" {
		access["credentialVersion"] = resumed.CredentialVersion
	}
	if resumed.CredentialSecretRef != "" {
		access["secretRef"] = resumed.CredentialSecretRef
	}
	workspace["access"] = access
	return workspace
}
