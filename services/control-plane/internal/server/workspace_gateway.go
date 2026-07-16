package server

import (
	"context"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/domain"
)

func (app *controlPlaneServer) workspaceStateRowsLocked(accountID string) []any {
	rows := app.listWorkspaces(accountID)
	output := make([]any, 0, len(rows))
	for _, row := range rows {
		workspace := app.workspaceResponse(cloneMap(row))
		output = append(output, workspace)
	}
	return output
}

func (app *controlPlaneServer) workspaceResponse(row map[string]any) map[string]any {
	response, _ := app.workspaceAccessResponse(row, time.Now().UTC())
	return response
}

func (app *controlPlaneServer) workspaceAccessResponse(row map[string]any, now time.Time) (map[string]any, bool) {
	response := workspaceResponse(row)
	storage, ok := app.getStorage(stringValue(response["storageId"]))
	if ok {
		switch stringValue(storage["status"]) {
		case "available", "ready", "bound", "attached":
		default:
			ok = false
		}
	}
	paidThrough, err := time.Parse(time.RFC3339, stringValue(storage["paidThrough"]))
	storageActive := ok && stringValue(storage["billingStatus"]) == "active" && err == nil && now.UTC().Before(paidThrough)
	if !storageActive {
		response["openable"] = false
		response["accessState"] = "disabled"
	}
	return response, storageActive
}

func (app *controlPlaneServer) saveWorkspaceProjection(workspace domain.WorkspaceProjection) error {
	access := map[string]any{}
	if workspace.RuntimeUsername != "" {
		access["account"] = workspace.RuntimeUsername
		access["username"] = workspace.RuntimeUsername
	}
	if workspace.CredentialStatus != "" {
		access["credentialStatus"] = workspace.CredentialStatus
	}
	if workspace.CredentialVersion != "" {
		access["credentialVersion"] = workspace.CredentialVersion
	}
	if workspace.CredentialSecretRef != "" {
		access["secretRef"] = workspace.CredentialSecretRef
	}
	row := map[string]any{
		"id":                         workspace.ID,
		"ownerAccountId":             workspace.AccountID,
		"ownerUserId":                workspace.OwnerID,
		"accountId":                  workspace.AccountID,
		"name":                       workspace.Name,
		"packageId":                  workspace.PackageID,
		"provider":                   workspace.Provider,
		"state":                      workspace.Status,
		"status":                     workspace.Status,
		"url":                        workspace.URL,
		"computeAllocationId":        workspace.ComputeID,
		"currentComputeAllocationId": workspace.ComputeID,
		"storageId":                  workspace.VolumeID,
		"attachmentId":               workspace.AttachmentID,
		"currentAttachmentId":        workspace.AttachmentID,
		"runtimeId":                  workspace.RuntimeID,
		"runtime":                    map[string]any{"serviceName": workspace.RuntimeServiceName, "status": workspace.Status, "ready": workspace.RuntimeReady},
		"receiptId":                  workspace.ReceiptID,
		"access":                     access,
	}
	return app.tables.SaveWorkspace(context.Background(), row)
}

func (app *controlPlaneServer) suspendWorkspacesForCompute(computeID string) error {
	for _, workspace := range app.listWorkspaces("") {
		if stringValue(workspace["currentComputeAllocationId"]) == computeID || stringValue(workspace["computeAllocationId"]) == computeID {
			workspace["currentComputeAllocationId"] = ""
			workspace["computeAllocationId"] = ""
			workspace["state"] = "suspended"
			workspace["status"] = "suspended"
			if err := app.tables.SaveWorkspace(context.Background(), workspace); err != nil {
				return err
			}
		}
	}
	return nil
}

func (app *controlPlaneServer) clearWorkspacesForAttachment(attachmentID string) error {
	for _, workspace := range app.listWorkspaces("") {
		if stringValue(workspace["currentAttachmentId"]) == attachmentID || stringValue(workspace["attachmentId"]) == attachmentID {
			workspace["currentAttachmentId"] = ""
			workspace["attachmentId"] = ""
			if stringValue(workspace["state"]) != "data_deleted" {
				workspace["state"] = "suspended"
				workspace["status"] = "suspended"
			}
			if err := app.tables.SaveWorkspace(context.Background(), workspace); err != nil {
				return err
			}
		}
	}
	return nil
}

func (app *controlPlaneServer) markWorkspacesStorageDestroyed(storageID string) error {
	for _, workspace := range app.listWorkspaces("") {
		if stringValue(workspace["storageId"]) == storageID {
			workspace["state"] = "data_deleted"
			workspace["status"] = "unrecoverable"
			workspace["currentComputeAllocationId"] = ""
			workspace["computeAllocationId"] = ""
			workspace["currentAttachmentId"] = ""
			workspace["attachmentId"] = ""
			if err := app.tables.SaveWorkspace(context.Background(), workspace); err != nil {
				return err
			}
		}
	}
	return nil
}

func (app *controlPlaneServer) getWorkspace(id string) (map[string]any, bool) {
	for _, workspace := range app.listWorkspaces("") {
		if stringValue(workspace["id"]) == id {
			return cloneMap(workspace), true
		}
	}
	return nil, false
}

func (app *controlPlaneServer) proxyWorkspace(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromPath(r.URL.Path)
	if workspaceID == "" {
		http.NotFound(w, r)
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, "/w/"+workspaceID)
	app.proxyWorkspaceTo(w, r, workspaceID, suffix)
}

func (app *controlPlaneServer) proxyWorkspaceRoot(w http.ResponseWriter, r *http.Request) {
	if !isWorkspaceRequest(r) {
		http.NotFound(w, r)
		return
	}
	workspaceID := workspaceIDFromGatewayRequest(r)
	if workspaceID == "" {
		http.NotFound(w, r)
		return
	}
	app.proxyWorkspaceTo(w, r, workspaceID, r.URL.Path)
}

func (app *controlPlaneServer) proxyWorkspaceTo(w http.ResponseWriter, r *http.Request, workspaceID string, proxyPath string) {
	workspace, ok := app.getWorkspace(workspaceID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if state := stringValue(workspace["state"]); state == "data_deleted" || state == "unrecoverable" || state == "storage_missing" || state == "destroyed" {
		writeError(w, http.StatusGone, "workspace_storage_destroyed")
		return
	}
	if stringValue(workspace["state"]) == "suspended" {
		writeError(w, http.StatusConflict, "workspace_suspended")
		return
	}
	response, storageActive := app.workspaceAccessResponse(cloneMap(workspace), time.Now().UTC())
	if !storageActive {
		writeError(w, http.StatusConflict, "workspace_storage_entitlement_inactive")
		return
	}
	if response["openable"] != true {
		writeError(w, http.StatusConflict, "workspace_runtime_not_ready")
		return
	}
	serviceName := stringValue(nested(workspace, "runtime", "serviceName"))
	if serviceName == "" {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/w/"+workspaceID) {
		setWorkspaceGatewayRouteCookie(w, workspaceID)
	}
	target, err := workspaceServiceTarget(serviceName)
	if err != nil {
		writeUpstreamError(w)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		if proxyPath == "" {
			proxyPath = "/"
		}
		req.URL.Path = proxyPath
		req.URL.RawPath = ""
		req.Host = target.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		writeUpstreamError(w)
	}
	proxy.ServeHTTP(w, r)
}
