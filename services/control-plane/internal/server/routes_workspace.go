package server

import (
	"errors"
	"net/http"
	"time"

	"opl-cloud/services/control-plane/internal/controlplane"
)

func registerWorkspaceRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("GET /api/workspaces", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, app.state(accountID, nil)["workspaces"])
	}))
	mux.HandleFunc("POST /api/workspaces/runtime-status", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		workspaceID := stringField(input, "workspaceId", "")
		workspace, ok := app.getWorkspace(workspaceID)
		if !ok {
			writeError(w, http.StatusNotFound, "workspace_not_found")
			return
		}
		if !app.canAccessResource(r, workspace) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		unlock := app.lockEntitlementResources(
			firstNonEmpty(stringValue(workspace["currentComputeAllocationId"]), stringValue(workspace["computeAllocationId"])),
			stringValue(workspace["storageId"]),
			firstNonEmpty(stringValue(workspace["currentAttachmentId"]), stringValue(workspace["attachmentId"])),
		)
		defer unlock()
		workspace, ok = app.getWorkspace(workspaceID)
		if !ok {
			writeError(w, http.StatusNotFound, "workspace_not_found")
			return
		}
		if !app.canAccessResource(r, workspace) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
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
			writeUpstreamError(w)
			return
		}
		status := runtime.Status
		if !runtime.Ready && (status == "" || status == "running") {
			status = "unready"
		} else if runtime.Ready && status == "" {
			status = "running"
		}
		workspace["state"], workspace["status"] = status, status
		workspace["url"] = firstNonEmpty(runtime.URL, stringValue(workspace["url"]))
		workspace["runtimeId"] = firstNonEmpty(runtime.ID, stringValue(workspace["runtimeId"]))
		runtimeProjection := cloneMap(mapField(workspace, "runtime"))
		runtimeProjection["serviceName"] = firstNonEmpty(runtime.ServiceName, stringValue(runtimeProjection["serviceName"]))
		runtimeProjection["status"], runtimeProjection["ready"] = status, runtime.Ready
		workspace["runtime"] = runtimeProjection
		access := cloneMap(mapField(workspace, "access"))
		delete(access, "password")
		delete(access, "tokenStatus")
		delete(access, "requiresLogin")
		if runtime.Access.Username != "" {
			access["account"], access["username"] = runtime.Access.Username, runtime.Access.Username
		}
		if runtime.Access.CredentialStatus != "" {
			access["credentialStatus"] = runtime.Access.CredentialStatus
		}
		if runtime.Access.CredentialVersion != "" {
			access["credentialVersion"] = runtime.Access.CredentialVersion
		}
		if runtime.Access.SecretRef != "" {
			access["secretRef"] = runtime.Access.SecretRef
		}
		workspace["access"] = access
		if err := app.tables.SaveWorkspace(r.Context(), workspace); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, workspaceRuntimeStatusResponse(runtime))
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
		resumed, err := service.ResumeWorkspace(r.Context(), controlplane.ResumeWorkspaceInput{WorkspaceID: workspaceID, AccountID: accountID, Sub2APIUserID: sub2APIUserID, OwnerID: stringValue(workspace["ownerUserId"]), Name: stringValue(workspace["name"]), PackageID: stringValue(workspace["packageId"]), URL: stringValue(workspace["url"]), AttachmentID: attachmentID, ComputeID: computeID, VolumeID: storageID}, key)
		if err != nil {
			if failErr := app.tables.FailWorkspaceResume(r.Context(), workspaceID, operationID, "fabric_resume_failed"); failErr != nil {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
			writeUpstreamError(w, err)
			return
		}
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
		body := workspaceResponse(cloneMap(workspace))
		operation := map[string]any{"id": operationID, "operationId": operationID, "accountId": accountID, "workspaceId": workspaceID, "resourceId": workspaceID, "resourceKind": "workspace_runtime", "action": "workspace.resume", "provider": stringValue(workspace["provider"]), "providerRequestId": resumed.RuntimeID, "status": "succeeded", "result": encodeWorkspaceResumeOperation(workspaceResumeOperationResult{RequestHash: requestHash, Response: body}), "computeAllocationId": resumed.ComputeID, "storageId": resumed.VolumeID, "attachmentId": resumed.AttachmentID, "runtimeServiceName": resumed.RuntimeServiceName}
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
		ownerID := firstNonEmpty(stringField(input, "ownerId", ""), stringField(input, "ownerUserId", ""))
		name := firstNonEmpty(stringField(input, "name", ""), stringField(input, "workspaceName", "Workspace"))
		if existing, exists := app.getWorkspace(workspaceID); exists {
			if firstNonEmpty(stringValue(existing["accountId"]), stringValue(existing["ownerAccountId"])) != accountID ||
				stringValue(existing["attachmentId"]) != attachmentID || stringValue(existing["computeAllocationId"]) != computeID ||
				stringValue(existing["storageId"]) != storageID || stringValue(existing["name"]) != name || stringValue(existing["packageId"]) != packageID ||
				firstNonEmpty(stringValue(existing["ownerId"]), stringValue(existing["ownerUserId"])) != ownerID {
				writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
				return
			}
			writeJSON(w, http.StatusCreated, workspaceResponse(existing))
			return
		}
		sub2APIUserID, ok := app.mappedSub2APIUserID(w, r, accountID)
		if !ok {
			return
		}
		workspace, err := service.CreateWorkspace(r.Context(), controlplane.CreateWorkspaceInput{
			WorkspaceID:   workspaceID,
			AccountID:     accountID,
			Sub2APIUserID: sub2APIUserID,
			OwnerID:       ownerID,
			Name:          name,
			PackageID:     packageID,
			AttachmentID:  attachmentID,
			ComputeID:     computeID,
			VolumeID:      storageID,
		}, "workspace-create:"+workspaceID)
		if err != nil {
			writeUpstreamError(w)
			return
		}
		if err := app.saveWorkspaceProjection(workspace); err != nil {
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
}
