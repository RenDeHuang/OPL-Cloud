package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func registerRecoveryRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("POST /api/workspaces/{workspaceId}/backups", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		workspaceID := strings.TrimSpace(r.PathValue("workspaceId"))
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
		input := decodeJSON(r)
		requestHash := stableID(string(mustJSON(input)))
		if existing, ok := replayedWorkspaceBackup(r, app, key, requestHash); ok {
			writeJSON(w, http.StatusCreated, workspaceBackupResponse(existing))
			return
		}
		accountID := firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"]))
		storageID := stringValue(workspace["storageId"])
		if storageID == "" {
			writeError(w, http.StatusUnprocessableEntity, "workspace_storage_unavailable")
			return
		}
		snapshot, err := service.CreateStorageSnapshot(r.Context(), clients.StorageSnapshotInput{AccountID: accountID, WorkspaceID: workspaceID, VolumeID: storageID}, key)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		backupID := "backup-" + stableID(workspaceID, key)[:18]
		manifest := map[string]any{
			"schemaVersion": 1, "backupId": backupID, "workspaceId": workspaceID, "storageId": storageID,
			"syncCursor": input["syncCursor"], "projectVersions": input["projectVersions"],
			"artifactIds": input["artifactIds"], "receiptIds": input["receiptIds"], "continuationIds": input["continuationIds"],
			"runtimeTemplateRef": input["runtimeTemplateRef"], "environmentRef": input["environmentRef"],
			"sizeGb": snapshot.SizeGB, "createdAt": now,
		}
		manifest["checksum"] = stableID(string(mustJSON(manifest)))
		row := map[string]any{"id": backupID, "backupId": backupID, "accountId": accountID, "workspaceId": workspaceID, "storageId": storageID, "snapshotId": snapshot.ID, "status": snapshot.Status, "idempotencyKey": key, "requestHash": requestHash, "manifestJson": string(mustJSON(manifest)), "createdAt": now, "updatedAt": now}
		if err := app.tables.SaveWorkspaceBackup(r.Context(), row); err != nil {
			writeError(w, http.StatusConflict, "backup_idempotency_conflict")
			return
		}
		writeJSON(w, http.StatusCreated, workspaceBackupResponse(row))
	}))

	mux.HandleFunc("GET /api/workspaces/{workspaceId}/backups", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		workspaceID := strings.TrimSpace(r.PathValue("workspaceId"))
		workspace, ok := app.getWorkspace(workspaceID)
		if !ok || !app.canAccessResource(r, workspace) {
			writeError(w, http.StatusNotFound, "workspace_not_found")
			return
		}
		rows, err := app.tables.ListWorkspaceBackups(r.Context(), workspaceID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, workspaceBackupResponse(row))
		}
		writeJSON(w, http.StatusOK, out)
	}))

	mux.HandleFunc("GET /api/workspace-backups/{backupId}/export", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		backup, ok := authorizedWorkspaceBackup(w, r, app)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, workspaceBackupManifest(backup))
	}))

	mux.HandleFunc("POST /api/workspace-backups/{backupId}/restore", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		backup, ok := authorizedWorkspaceBackup(w, r, app)
		if !ok {
			return
		}
		key, ok := executionMutationKey(w, r)
		if !ok {
			return
		}
		input := decodeJSON(r)
		targetStorageID := stringField(input, "targetStorageId", "")
		if targetStorageID == "" {
			writeError(w, http.StatusBadRequest, "target_storage_id_required")
			return
		}
		volume, err := service.RestoreStorageSnapshot(r.Context(), stringValue(backup["snapshotId"]), clients.StorageRestoreInput{AccountID: stringValue(backup["accountId"]), WorkspaceID: stringValue(backup["workspaceId"]), TargetVolumeID: targetStorageID}, key)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		backup["restoredStorageId"], backup["status"], backup["updatedAt"] = volume.ID, "restoring", time.Now().UTC().Format(time.RFC3339Nano)
		if volume.Status == "ready" {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			storage := map[string]any{"id": volume.ID, "accountId": backup["accountId"], "workspaceId": backup["workspaceId"], "name": "Restored storage", "provider": volume.Provider, "providerResourceId": volume.ProviderResourceID, "providerRequestId": volume.ProviderRequestID, "status": "available", "providerStatus": "available", "desiredStatus": "available", "sizeGb": volume.SizeGB, "storageClass": volume.StorageClass, "billingStatus": "pending", "createdAt": now, "updatedAt": now}
			if err := app.tables.SaveStorage(r.Context(), storage); err != nil {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
			workspace, exists := app.getWorkspace(stringValue(backup["workspaceId"]))
			if !exists {
				writeError(w, http.StatusNotFound, "workspace_not_found")
				return
			}
			workspace["storageId"], workspace["currentComputeAllocationId"], workspace["currentAttachmentId"], workspace["state"] = volume.ID, "", "", "suspended"
			if err := app.tables.SaveWorkspace(r.Context(), workspace); err != nil {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
			backup["status"] = "restored"
		}
		if err := app.tables.SaveWorkspaceBackup(r.Context(), backup); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"backupId": backup["id"], "workspaceId": backup["workspaceId"], "storageId": volume.ID, "status": backup["status"]})
	}))

	mux.HandleFunc("POST /api/workspace-backups/{backupId}/clone", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		_, ok := authorizedWorkspaceBackup(w, r, app)
		if !ok {
			return
		}
		_, ok = executionMutationKey(w, r)
		if !ok {
			return
		}
		writeError(w, http.StatusConflict, errPrimaryWorkspaceExists.Error())
	}))

	mux.HandleFunc("POST /api/workspace-backups/{backupId}/destroy", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		backup, ok := authorizedWorkspaceBackup(w, r, app)
		if !ok {
			return
		}
		key, ok := executionMutationKey(w, r)
		if !ok {
			return
		}
		snapshot, err := service.DestroyStorageSnapshot(r.Context(), stringValue(backup["snapshotId"]), key)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		backup["status"], backup["updatedAt"] = snapshot.Status, time.Now().UTC().Format(time.RFC3339Nano)
		if err := app.tables.SaveWorkspaceBackup(r.Context(), backup); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"backupId": backup["id"], "status": backup["status"]})
	}))
}

func replayedWorkspaceBackup(r *http.Request, app *controlPlaneServer, key, requestHash string) (map[string]any, bool) {
	rows, err := app.tables.ListWorkspaceBackups(r.Context(), "")
	if err != nil {
		return nil, false
	}
	for _, row := range rows {
		if stringValue(row["idempotencyKey"]) == key && stringValue(row["requestHash"]) == requestHash {
			return row, true
		}
	}
	return nil, false
}

func authorizedWorkspaceBackup(w http.ResponseWriter, r *http.Request, app *controlPlaneServer) (map[string]any, bool) {
	rows, err := app.tables.ListWorkspaceBackups(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "state_read_failed")
		return nil, false
	}
	for _, row := range rows {
		if stringValue(row["id"]) != strings.TrimSpace(r.PathValue("backupId")) {
			continue
		}
		workspace, ok := app.getWorkspace(stringValue(row["workspaceId"]))
		if !ok || !app.canAccessResource(r, workspace) {
			writeError(w, http.StatusNotFound, "workspace_backup_not_found")
			return nil, false
		}
		return row, true
	}
	writeError(w, http.StatusNotFound, "workspace_backup_not_found")
	return nil, false
}

func workspaceBackupResponse(row map[string]any) map[string]any {
	manifest := workspaceBackupManifest(row)
	return map[string]any{"backupId": firstNonEmpty(stringValue(row["backupId"]), stringValue(row["id"])), "workspaceId": row["workspaceId"], "storageId": row["storageId"], "status": row["status"], "createdAt": row["createdAt"], "manifest": manifest}
}

func workspaceBackupManifest(row map[string]any) map[string]any {
	manifest := map[string]any{}
	_ = json.Unmarshal([]byte(stringValue(row["manifestJson"])), &manifest)
	return manifest
}

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}
