package server

import (
	"net/http"
	"net/http/httputil"
	"strings"

	"opl-cloud/services/control-plane/internal/domain"
)

func (app *controlPlaneApp) workspaceStateRowsLocked(accountID string) []any {
	rows := accountValues(app.resources.workspaces, accountID)
	output := make([]any, 0, len(rows))
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		workspace := workspaceResponse(cloneMap(row))
		workspace["billing"] = app.workspaceBillingLocked(workspace)
		output = append(output, workspace)
	}
	return output
}

func (app *controlPlaneApp) workspaceBillingLocked(workspace map[string]any) map[string]any {
	workspaceID := stringValue(workspace["id"])
	compute := app.resources.computes[stringValue(workspace["currentComputeAllocationId"])]
	storage := app.resources.storages[stringValue(workspace["storageId"])]
	return map[string]any{
		"activeHourlyEstimate": activeHourlyForResource(compute) + activeHourlyForResource(storage),
		"currentChargeTotal":   resourceDebitTotal(app.billing.ledger, firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"])), workspaceID),
	}
}

func (app *controlPlaneApp) setWorkspaceAccess(workspaceID string, tokenStatus string) (map[string]any, bool, error) {
	app.mu.Lock()
	defer app.mu.Unlock()
	workspace := app.resources.workspaces[workspaceID]
	if workspace == nil {
		return nil, false, nil
	}
	access, _ := workspace["access"].(map[string]any)
	access = cloneMap(access)
	access["tokenStatus"] = tokenStatus
	access["requiresLogin"] = false
	workspace["access"] = access
	return cloneMap(workspace), true, app.persistLocked()
}

func (app *controlPlaneApp) rememberWorkspaceProjection(workspace domain.WorkspaceProjection) error {
	app.mu.Lock()
	defer app.mu.Unlock()

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
	app.resources.workspaces[workspace.ID] = map[string]any{
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
	return app.persistLocked()
}

func (app *controlPlaneApp) suspendWorkspacesForComputeLocked(computeID string) {
	for _, workspace := range app.resources.workspaces {
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
		}
	}
}

func (app *controlPlaneApp) clearWorkspacesForAttachmentLocked(attachmentID string) {
	for _, workspace := range app.resources.workspaces {
		if stringValue(workspace["currentAttachmentId"]) == attachmentID || stringValue(workspace["attachmentId"]) == attachmentID {
			workspace["currentAttachmentId"] = ""
			workspace["attachmentId"] = ""
			if stringValue(workspace["state"]) != "data_deleted" {
				workspace["state"] = "suspended"
				workspace["status"] = "suspended"
			}
		}
	}
}

func (app *controlPlaneApp) markWorkspacesStorageDestroyedLocked(storageID string) {
	for _, workspace := range app.resources.workspaces {
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
		}
	}
}

func (app *controlPlaneApp) getWorkspace(id string) (map[string]any, bool) {
	app.mu.Lock()
	defer app.mu.Unlock()
	workspace, ok := app.resources.workspaces[id]
	return cloneMap(workspace), ok
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
	app.mu.Lock()
	workspace := cloneMap(app.resources.workspaces[workspaceID])
	app.mu.Unlock()
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
