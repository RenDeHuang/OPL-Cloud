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

func (app *controlPlaneServer) workspaceAccessResponse(row map[string]any, now time.Time) (map[string]any, string) {
	response := workspaceResponse(row)
	canonicalPaidThrough := now.UTC()
	canonicalComputeID, canonicalStorageID := stringValue(row["currentComputeAllocationId"]), stringValue(row["storageId"])
	if !providerAcceptanceWorkspaceBillingExempt(row) {
		state, present, err := normalizeWorkspaceBillingStateForWorkspace(row, row)
		if err != nil || !present {
			response["openable"], response["accessState"] = false, "disabled"
			return response, "workspace_billing_state_invalid"
		}
		if state.RenewalStatus != "active" {
			response["openable"], response["accessState"] = false, "disabled"
			return response, "workspace_billing_manual_review"
		}
		canonicalPaidThrough, _ = time.Parse(time.RFC3339, state.PaidThrough)
		canonicalComputeID, canonicalStorageID = state.ComputeAllocationID, state.StorageID
		if !now.UTC().Before(canonicalPaidThrough) {
			response["openable"], response["accessState"] = false, "disabled"
			return response, "workspace_billing_period_expired"
		}
	}
	accountID, workspaceID := firstNonEmpty(stringValue(response["accountId"]), stringValue(response["ownerAccountId"])), stringValue(response["id"])
	storage, ok := app.getStorage(canonicalStorageID)
	if ok {
		switch stringValue(storage["status"]) {
		case "available", "ready", "bound", "attached":
		default:
			ok = false
		}
	}
	paidThrough, err := time.Parse(time.RFC3339, stringValue(storage["paidThrough"]))
	storageActive := ok && app.resourceBelongsToAccount(storage, accountID) && stringValue(storage["workspaceId"]) == workspaceID &&
		stringValue(storage["billingStatus"]) == "active" && err == nil && now.UTC().Before(paidThrough) && !paidThrough.Before(canonicalPaidThrough)
	if !storageActive {
		response["openable"] = false
		response["accessState"] = "disabled"
		return response, "workspace_storage_entitlement_inactive"
	}

	compute, ok := app.getCompute(canonicalComputeID)
	if ok {
		switch stringValue(compute["status"]) {
		case "running", "ready", "available", "active":
		default:
			ok = false
		}
	}
	paidThrough, err = time.Parse(time.RFC3339, stringValue(compute["paidThrough"]))
	computeActive := ok &&
		app.resourceBelongsToAccount(compute, accountID) && stringValue(compute["workspaceId"]) == workspaceID &&
		stringValue(compute["billingStatus"]) == "active" && err == nil && now.UTC().Before(paidThrough) && !paidThrough.Before(canonicalPaidThrough)
	if !computeActive {
		response["openable"] = false
		response["accessState"] = "disabled"
		return response, "workspace_compute_entitlement_inactive"
	}

	attachment, ok := app.getAttachment(stringValue(row["currentAttachmentId"]))
	if ok {
		switch stringValue(attachment["status"]) {
		case "attached", "ready":
		default:
			ok = false
		}
	}
	attachmentActive := ok && app.resourceBelongsToAccount(attachment, accountID) && stringValue(attachment["workspaceId"]) == workspaceID &&
		firstNonEmpty(stringValue(attachment["computeAllocationId"]), stringValue(attachment["computeId"])) == canonicalComputeID &&
		firstNonEmpty(stringValue(attachment["storageId"]), stringValue(attachment["volumeId"])) == canonicalStorageID
	if !attachmentActive {
		response["openable"], response["accessState"] = false, "disabled"
		return response, "workspace_attachment_inactive"
	}
	return response, ""
}

func providerAcceptanceWorkspaceBillingExempt(row map[string]any) bool {
	accountID := firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"]))
	return row["customerProduct"] == false && stringValue(row["verificationSlotId"]) == providerAcceptanceSlotID &&
		accountID == providerAcceptanceAccountID && stringValue(row["id"]) == primaryWorkspaceID(providerAcceptanceAccountID) &&
		stringValue(row["computeAllocationId"]) == providerAcceptanceComputeID() && stringValue(row["currentComputeAllocationId"]) == providerAcceptanceComputeID() &&
		stringValue(row["storageId"]) == providerAcceptanceStorageID()
}

func (app *controlPlaneServer) saveWorkspaceProjection(workspace domain.WorkspaceProjection, acceptedBillingState map[string]any) error {
	return app.tables.SaveWorkspace(context.Background(), workspaceProjectionBillingRow(workspace, acceptedBillingState))
}

func workspaceProjectionBillingRow(workspace domain.WorkspaceProjection, acceptedBillingState map[string]any) map[string]any {
	row := workspaceProjectionRow(workspace)
	for key, value := range acceptedBillingState {
		row[key] = value
	}
	return row
}

func workspaceProjectionBillingResponseRow(workspace domain.WorkspaceProjection, acceptedBillingState map[string]any) map[string]any {
	row := structToMap(workspace)
	row["currentComputeAllocationId"] = workspace.ComputeID
	row["currentAttachmentId"] = workspace.AttachmentID
	for key, value := range acceptedBillingState {
		row[key] = value
	}
	return row
}

func workspaceAcceptedBillingState(row map[string]any) map[string]any {
	state, present, err := normalizeWorkspaceBillingStateForWorkspace(row, row)
	if err != nil || !present || state.RenewalStatus != "active" {
		return nil
	}
	return state.record()
}

func workspaceProjectionRow(workspace domain.WorkspaceProjection) map[string]any {
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
	return row
}

func (app *controlPlaneServer) suspendWorkspacesForCompute(computeID string) error {
	for _, workspace := range app.listWorkspaces("") {
		if stringValue(workspace["currentComputeAllocationId"]) == computeID || stringValue(workspace["computeAllocationId"]) == computeID {
			canonicalBilling := workspaceAcceptedBillingState(workspace) != nil
			workspace["currentComputeAllocationId"] = ""
			if canonicalBilling {
				workspace["autoRenew"] = false
			} else {
				workspace["computeAllocationId"] = ""
			}
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
			canonicalBilling := workspaceAcceptedBillingState(workspace) != nil
			workspace["currentAttachmentId"] = ""
			workspace["attachmentId"] = ""
			if canonicalBilling {
				workspace["autoRenew"] = false
			}
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
			canonicalBilling := workspaceAcceptedBillingState(workspace) != nil
			workspace["state"] = "data_deleted"
			workspace["status"] = "unrecoverable"
			workspace["currentComputeAllocationId"] = ""
			if canonicalBilling {
				workspace["autoRenew"] = false
			} else {
				workspace["computeAllocationId"] = ""
			}
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
	response, blockReason := app.workspaceAccessResponse(cloneMap(workspace), time.Now().UTC())
	if blockReason != "" {
		writeError(w, http.StatusConflict, blockReason)
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
