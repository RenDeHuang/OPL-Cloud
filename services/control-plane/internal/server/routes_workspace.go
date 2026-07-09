package server

import (
	"net/http"

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
