package server

import (
	"context"
	"net/http"
	"net/http/httputil"
	"strings"

	"opl-cloud/services/control-plane/internal/domain"
)

func (app *controlPlaneApp) workspaceStateRowsLocked(accountID string) []any {
	rows := app.listWorkspaces(accountID)
	output := make([]any, 0, len(rows))
	for _, row := range rows {
		workspace := workspaceResponse(cloneMap(row))
		workspace["billing"] = app.workspaceBillingLocked(workspace)
		output = append(output, workspace)
	}
	return output
}

func (app *controlPlaneApp) workspaceBillingLocked(workspace map[string]any) map[string]any {
	workspaceID := stringValue(workspace["id"])
	compute, _ := app.getCompute(stringValue(workspace["currentComputeAllocationId"]))
	storage, _ := app.getStorage(stringValue(workspace["storageId"]))
	return map[string]any{
		"activeHourlyEstimate": activeHourlyForResource(compute) + activeHourlyForResource(storage),
		"currentChargeTotal":   resourceDebitTotal(rowsToRecords(app.listLedger(firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"])))), firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"])), workspaceID),
	}
}

func (app *controlPlaneApp) setWorkspaceAccess(workspaceID string, tokenStatus string) (map[string]any, bool, error) {
	workspace, ok := app.getWorkspace(workspaceID)
	if !ok {
		return nil, false, nil
	}
	access, _ := workspace["access"].(map[string]any)
	access = cloneMap(access)
	access["tokenStatus"] = tokenStatus
	access["requiresLogin"] = false
	workspace["access"] = access
	return cloneMap(workspace), true, app.tables.SaveWorkspace(context.Background(), workspace)
}

func (app *controlPlaneApp) rememberWorkspaceProjection(workspace domain.WorkspaceProjection) error {
	access := map[string]any{"tokenStatus": "active", "requiresLogin": false}
	if workspace.RuntimeUsername != "" {
		access["account"] = workspace.RuntimeUsername
		access["username"] = workspace.RuntimeUsername
	}
	if workspace.RuntimePassword != "" {
		access["password"] = workspace.RuntimePassword
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
		"runtime":                    map[string]any{"serviceName": workspace.RuntimeServiceName},
		"evidenceId":                 workspace.EvidenceID,
		"access":                     access,
	}
	return app.tables.SaveWorkspace(context.Background(), row)
}

func (app *controlPlaneApp) suspendWorkspacesForCompute(computeID string) error {
	for _, workspace := range app.listWorkspaces("") {
		if stringValue(workspace["currentComputeAllocationId"]) == computeID || stringValue(workspace["computeAllocationId"]) == computeID {
			workspace["currentComputeAllocationId"] = ""
			workspace["computeAllocationId"] = ""
			workspace["state"] = "suspended"
			workspace["status"] = "suspended"
			access, _ := workspace["access"].(map[string]any)
			access = cloneMap(access)
			access["tokenStatus"] = "suspended"
			access["requiresLogin"] = false
			workspace["access"] = access
			if err := app.tables.SaveWorkspace(context.Background(), workspace); err != nil {
				return err
			}
		}
	}
	return nil
}

func (app *controlPlaneApp) clearWorkspacesForAttachment(attachmentID string) error {
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

func (app *controlPlaneApp) markWorkspacesStorageDestroyed(storageID string) error {
	for _, workspace := range app.listWorkspaces("") {
		if stringValue(workspace["storageId"]) == storageID {
			workspace["state"] = "data_deleted"
			workspace["status"] = "unrecoverable"
			workspace["currentComputeAllocationId"] = ""
			workspace["computeAllocationId"] = ""
			workspace["currentAttachmentId"] = ""
			workspace["attachmentId"] = ""
			access, _ := workspace["access"].(map[string]any)
			access = cloneMap(access)
			access["tokenStatus"] = "disabled"
			access["requiresLogin"] = false
			workspace["access"] = access
			if err := app.tables.SaveWorkspace(context.Background(), workspace); err != nil {
				return err
			}
		}
	}
	return nil
}

func (app *controlPlaneApp) getWorkspace(id string) (map[string]any, bool) {
	for _, workspace := range app.listWorkspaces("") {
		if stringValue(workspace["id"]) == id {
			return cloneMap(workspace), true
		}
	}
	return nil, false
}

func (app *controlPlaneApp) proxyWorkspace(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromPath(r.URL.Path)
	if workspaceID == "" {
		http.NotFound(w, r)
		return
	}
	if token := r.URL.Query().Get("token"); token != "" {
		setWorkspaceGatewayCookies(w, workspaceID, token)
		cleanURL := *r.URL
		query := cleanURL.Query()
		query.Del("token")
		cleanURL.RawQuery = query.Encode()
		http.Redirect(w, r, cleanURL.String(), http.StatusFound)
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, "/w/"+workspaceID)
	app.proxyWorkspaceTo(w, r, workspaceID, suffix)
}

func (app *controlPlaneApp) proxyWorkspaceRoot(w http.ResponseWriter, r *http.Request) {
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

func (app *controlPlaneApp) proxyWorkspaceTo(w http.ResponseWriter, r *http.Request, workspaceID string, proxyPath string) {
	workspace, _ := app.getWorkspace(workspaceID)
	if stringValue(workspace["state"]) == "data_deleted" || stringValue(nested(workspace, "access", "tokenStatus")) == "disabled" {
		writeError(w, http.StatusGone, "workspace_storage_destroyed")
		return
	}
	if stringValue(workspace["state"]) == "suspended" || stringValue(nested(workspace, "access", "tokenStatus")) == "suspended" {
		writeError(w, http.StatusConflict, "workspace_suspended")
		return
	}
	serviceName := stringValue(nested(workspace, "runtime", "serviceName"))
	if serviceName == "" {
		http.NotFound(w, r)
		return
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
