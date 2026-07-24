package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
	"opl-cloud/services/control-plane/internal/domain"
)

var errWorkspaceKeyRotationInProgress = errors.New("workspace_key_rotation_in_progress")
var errWorkspaceKeyRotationConflict = errors.New("workspace_key_rotation_conflict")
var errWorkspaceKeyRotationState = errors.New("workspace_key_rotation_state_failed")

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
		canonicalPaidThrough, _ := time.Parse(time.RFC3339, state.PaidThrough)
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
	storageActive := ok && stringValue(storage["id"]) == canonicalStorageID &&
		app.resourceBelongsToAccount(storage, accountID) && stringValue(storage["workspaceId"]) == workspaceID
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
	computeActive := ok && stringValue(compute["id"]) == canonicalComputeID &&
		app.resourceBelongsToAccount(compute, accountID) && stringValue(compute["workspaceId"]) == workspaceID
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
	if row["customerProduct"] != false {
		return false
	}
	accountID := firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"]))
	for _, slot := range providerAcceptanceSlots {
		computeID := stringValue(row["computeAllocationId"])
		if stringValue(row["verificationSlotId"]) == slot.ID && accountID == slot.AccountID && stringValue(row["id"]) == primaryWorkspaceID(slot.AccountID) &&
			(computeID == "" || computeID == providerAcceptanceComputeID(slot)) &&
			stringValue(row["currentComputeAllocationId"]) == providerAcceptanceComputeID(slot) &&
			stringValue(row["storageId"]) == providerAcceptanceStorageID(slot) {
			return true
		}
	}
	return false
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
	if workspace.WorkspaceAPIKeyID > 0 {
		row["workspaceApiKeyId"] = workspace.WorkspaceAPIKeyID
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
	workspace, ok, err := app.workspaceByID(context.Background(), id)
	return cloneMap(workspace), ok && err == nil
}

type workspaceKeyRotationOperation struct {
	RequestHash              string `json:"requestHash"`
	Phase                    string `json:"phase"`
	OldKeyID                 int64  `json:"oldKeyId"`
	NewKeyID                 int64  `json:"newKeyId,omitempty"`
	ReplacementName          string `json:"replacementName"`
	RetiredName              string `json:"retiredName"`
	ReplacementCreateStarted bool   `json:"replacementCreateStarted,omitempty"`
	SecretRef                string `json:"secretRef,omitempty"`
	Fingerprint              string `json:"fingerprint,omitempty"`
	RuntimeID                string `json:"runtimeId,omitempty"`
	ReceiptID                string `json:"receiptId,omitempty"`
	CompletedAt              string `json:"completedAt,omitempty"`
}

func encodeWorkspaceKeyRotation(operation workspaceKeyRotationOperation) string {
	payload, _ := json.Marshal(operation)
	return string(payload)
}

func decodeWorkspaceKeyRotation(row map[string]any) (workspaceKeyRotationOperation, error) {
	var operation workspaceKeyRotationOperation
	if err := json.Unmarshal([]byte(stringValue(row["result"])), &operation); err != nil || operation.RequestHash == "" || operation.Phase == "" || operation.OldKeyID <= 0 || operation.ReplacementName == "" || operation.RetiredName == "" {
		return workspaceKeyRotationOperation{}, errWorkspaceKeyRotationState
	}
	return operation, nil
}

func workspaceKeyRotationRow(operationID, accountID, workspaceID, status string, operation workspaceKeyRotationOperation) map[string]any {
	return map[string]any{
		"id": operationID, "operationId": operationID, "accountId": accountID, "workspaceId": workspaceID,
		"resourceId": workspaceID, "resourceKind": "workspace_gateway_key", "action": "workspace.gateway_key.rotate",
		"provider": "sub2api", "status": status, "result": encodeWorkspaceKeyRotation(operation),
	}
}

func (app *controlPlaneServer) persistWorkspaceKeyRotation(ctx context.Context, operationID, accountID, workspaceID, status string, operation workspaceKeyRotationOperation) error {
	if err := app.tables.SaveRuntimeOperation(ctx, workspaceKeyRotationRow(operationID, accountID, workspaceID, status, operation)); err != nil {
		return errWorkspaceKeyRotationState
	}
	return nil
}

func (app *controlPlaneServer) workspaceKeyRotation(ctx context.Context, operationID, accountID, workspaceID, requestHash string) (workspaceKeyRotationOperation, bool, error) {
	operations, err := app.tables.ListRuntimeOperations(ctx)
	if err != nil {
		return workspaceKeyRotationOperation{}, false, errWorkspaceKeyRotationState
	}
	for _, row := range operations {
		if stringValue(row["id"]) != operationID {
			continue
		}
		operation, decodeErr := decodeWorkspaceKeyRotation(row)
		if decodeErr != nil || stringValue(row["accountId"]) != accountID || stringValue(row["workspaceId"]) != workspaceID || stringValue(row["action"]) != "workspace.gateway_key.rotate" {
			return workspaceKeyRotationOperation{}, false, errWorkspaceKeyRotationState
		}
		if operation.RequestHash != requestHash {
			return workspaceKeyRotationOperation{}, false, errIdempotencyConflict
		}
		return operation, stringValue(row["status"]) == "succeeded", nil
	}
	return workspaceKeyRotationOperation{}, false, nil
}

func (app *controlPlaneServer) claimWorkspaceKeyRotation(ctx context.Context, operationID, accountID, workspaceID, requestHash string, oldKeyID int64) (workspaceKeyRotationOperation, bool, error) {
	if existing, complete, err := app.workspaceKeyRotation(ctx, operationID, accountID, workspaceID, requestHash); err != nil || existing.Phase != "" {
		return existing, complete, err
	}
	operations, err := app.tables.ListRuntimeOperations(ctx)
	if err != nil {
		return workspaceKeyRotationOperation{}, false, errWorkspaceKeyRotationState
	}
	for _, row := range operations {
		if stringValue(row["workspaceId"]) == workspaceID && stringValue(row["action"]) == "workspace.gateway_key.rotate" && stringValue(row["status"]) != "succeeded" {
			return workspaceKeyRotationOperation{}, false, errWorkspaceKeyRotationInProgress
		}
	}
	operation := workspaceKeyRotationOperation{
		RequestHash: requestHash, Phase: "replacement_check", OldKeyID: oldKeyID,
		ReplacementName: workspaceRotationReplacementName(operationID),
		RetiredName:     "opl-workspace-retired-" + stableID(operationID)[:12],
	}
	if err := app.persistWorkspaceKeyRotation(ctx, operationID, accountID, workspaceID, "started", operation); err != nil {
		return workspaceKeyRotationOperation{}, false, err
	}
	return operation, false, nil
}

func (app *controlPlaneServer) rotateWorkspaceGatewayKey(w http.ResponseWriter, r *http.Request, service *controlplane.Service) {
	var input struct{}
	if decodeStrictGatewayRequest(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "invalid_workspace_key_rotation_request")
		return
	}
	idempotencyKey, ok := requiredMutationKey(w, r)
	if !ok {
		return
	}
	workspaceID := r.PathValue("workspaceId")
	workspace, ok := app.ownedWorkspaceForCredentialCommand(w, r, workspaceID)
	if !ok {
		return
	}
	user, userID, credential, ok := app.gatewayUserContext(w, r)
	if !ok {
		return
	}
	accountID := firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"]))
	ownerID := firstNonEmpty(stringValue(workspace["ownerUserId"]), stringValue(workspace["ownerId"]))
	if stringValue(user["accountId"]) != accountID || stringValue(user["id"]) != ownerID {
		writeError(w, http.StatusForbidden, "workspace_owner_required")
		return
	}
	oldKeyID, ok := requiredPositiveInteger(workspace, "workspaceApiKeyId")
	if !ok {
		writeError(w, http.StatusConflict, errWorkspaceKeyRotationConflict.Error())
		return
	}
	operationID := "workspace-key-rotate-" + stableID(workspaceID, idempotencyKey)[:18]
	requestHash := stableID("workspace-key-rotation-v1", workspaceID, string(mustJSON(input)))
	claimUnlock := app.lockResource("workspace-key-rotation-claim", workspaceID)
	_, _, err := app.claimWorkspaceKeyRotation(r.Context(), operationID, accountID, workspaceID, requestHash, oldKeyID)
	claimUnlock()
	if err != nil {
		writeWorkspaceKeyRotationError(w, err)
		return
	}
	operationUnlock := app.lockResource("workspace-key-rotation", operationID)
	defer operationUnlock()
	operation, _, err := app.workspaceKeyRotation(r.Context(), operationID, accountID, workspaceID, requestHash)
	if err != nil {
		writeWorkspaceKeyRotationError(w, err)
		return
	}
	operation, err = app.runWorkspaceKeyRotation(r, service, credential, userID, operationID, accountID, workspaceID, ownerID, operation)
	if err != nil {
		if errors.Is(err, errWorkspaceKeyRotationConflict) || errors.Is(err, errWorkspaceAPIKeyCASConflict) {
			if persistErr := app.persistWorkspaceKeyRotation(r.Context(), operationID, accountID, workspaceID, "manual_review", operation); persistErr != nil {
				writeWorkspaceKeyRotationError(w, persistErr)
				return
			}
		}
		writeWorkspaceKeyRotationError(w, err)
		return
	}
	app.writeWorkspaceKeyRotationResponse(w, operationID, workspaceID, operation)
}

func writeWorkspaceKeyRotationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errWorkspaceKeyRotationInProgress), errors.Is(err, errWorkspaceKeyRotationConflict), errors.Is(err, errWorkspaceAPIKeyCASConflict), errors.Is(err, errIdempotencyConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errWorkspaceKeyRotationState):
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
	default:
		writeUpstreamError(w, err)
	}
}

func (app *controlPlaneServer) writeWorkspaceKeyRotationResponse(w http.ResponseWriter, operationID, workspaceID string, operation workspaceKeyRotationOperation) {
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"operationId": operationID, "workspaceId": workspaceID, "status": "succeeded",
		"workspaceApiKeyId": strconv.FormatInt(operation.NewKeyID, 10), "fingerprint": operation.Fingerprint,
		"updatedAt": operation.CompletedAt, "receiptId": operation.ReceiptID,
	})
}

func (app *controlPlaneServer) runWorkspaceKeyRotation(r *http.Request, service *controlplane.Service, credential clients.SessionDelegatedCredential, userID int64, operationID, accountID, workspaceID, ownerID string, operation workspaceKeyRotationOperation) (workspaceKeyRotationOperation, error) {
	ctx := r.Context()
	for range 12 {
		switch operation.Phase {
		case "replacement_check":
			keys, err := workspaceRotationKeys(ctx, service, credential, userID)
			if err != nil {
				return operation, err
			}
			if !workspaceRotationInitialKeysValid(keys, operation.OldKeyID, workspaceReservedKeyName(workspaceID)) {
				return operation, errWorkspaceKeyRotationConflict
			}
			operation.Phase = "replacement_create"
			if err := app.persistWorkspaceKeyRotation(ctx, operationID, accountID, workspaceID, "started", operation); err != nil {
				return operation, err
			}
		case "replacement_create":
			keys, err := workspaceRotationKeys(ctx, service, credential, userID)
			if err != nil {
				return operation, err
			}
			matches := workspaceKeysNamed(keys, operation.ReplacementName)
			if len(matches) > 1 || len(matches) == 1 && !operation.ReplacementCreateStarted {
				return operation, errWorkspaceKeyRotationConflict
			}
			if len(matches) == 1 {
				if matches[0].Status != "active" || matches[0].ID <= 0 {
					return operation, errWorkspaceKeyRotationConflict
				}
				operation.NewKeyID = matches[0].ID
			} else {
				if !operation.ReplacementCreateStarted {
					operation.ReplacementCreateStarted = true
					if err := app.persistWorkspaceKeyRotation(ctx, operationID, accountID, workspaceID, "started", operation); err != nil {
						return operation, err
					}
				}
				created, err := service.CreateGatewayUserKey(ctx, credential, userID, clients.Sub2APICreateKeyInput{Name: operation.ReplacementName}, operationID+":replacement")
				if err != nil {
					return operation, err
				}
				if created.ID <= 0 || created.UserID != userID || created.Name != operation.ReplacementName || created.Status != "active" {
					return operation, errWorkspaceKeyRotationConflict
				}
				readback, err := service.GatewayUserKey(ctx, credential, userID, created.ID)
				if err != nil {
					return operation, err
				}
				if readback.ID != created.ID || readback.UserID != userID || readback.Name != operation.ReplacementName || readback.Status != "active" {
					return operation, errWorkspaceKeyRotationConflict
				}
				operation.NewKeyID = readback.ID
			}
			operation.Phase = "secret_write"
			if err := app.persistWorkspaceKeyRotation(ctx, operationID, accountID, workspaceID, "started", operation); err != nil {
				return operation, err
			}
		case "secret_write":
			secret, err := service.SyncWorkspaceGatewayReplacementSecret(ctx, credential, accountID, workspaceID, userID, operation.NewKeyID, operation.ReplacementName, operationID)
			if err != nil {
				return operation, err
			}
			operation.SecretRef, operation.Fingerprint, operation.Phase = secret.SecretRef, secret.Fingerprint, "runtime_bind"
			if err := app.persistWorkspaceKeyRotation(ctx, operationID, accountID, workspaceID, "started", operation); err != nil {
				return operation, err
			}
		case "runtime_bind":
			binding, err := service.BindWorkspaceRuntimeGatewaySecret(ctx, clients.WorkspaceRuntimeGatewaySecretInput{
				WorkspaceID: workspaceID, WorkspaceAPIKeyID: operation.NewKeyID,
				SecretRef: operation.SecretRef, Fingerprint: operation.Fingerprint,
			}, operationID+":runtime-bind")
			if err != nil {
				return operation, err
			}
			if !workspaceRuntimeGatewaySecretMatches(binding, operation, workspaceID) {
				return operation, errWorkspaceKeyRotationConflict
			}
			operation.Phase = "runtime_readback"
			if err := app.persistWorkspaceKeyRotation(ctx, operationID, accountID, workspaceID, "started", operation); err != nil {
				return operation, err
			}
		case "runtime_readback":
			binding, err := service.WorkspaceRuntimeGatewaySecret(ctx, workspaceID)
			if err != nil {
				return operation, err
			}
			if !workspaceRuntimeGatewaySecretMatches(binding, operation, workspaceID) {
				return operation, errWorkspaceKeyRotationConflict
			}
			operation.Phase = "workspace_commit"
			if err := app.persistWorkspaceKeyRotation(ctx, operationID, accountID, workspaceID, "started", operation); err != nil {
				return operation, err
			}
		case "workspace_commit":
			if err := app.tables.CompareAndSwapWorkspaceAPIKey(ctx, workspaceID, operation.OldKeyID, operation.NewKeyID); err != nil {
				return operation, err
			}
			operation.Phase = "retire_old"
			if err := app.persistWorkspaceKeyRotation(ctx, operationID, accountID, workspaceID, "started", operation); err != nil {
				return operation, err
			}
		case "retire_old":
			oldKey, err := service.GatewayUserKey(ctx, credential, userID, operation.OldKeyID)
			if err != nil {
				return operation, err
			}
			if oldKey.Name != operation.RetiredName || oldKey.Status != "disabled" {
				if oldKey.Name != "opl-workspace" && oldKey.Name != workspaceReservedKeyName(workspaceID) || oldKey.Status != "active" {
					return operation, errWorkspaceKeyRotationConflict
				}
				disabled, retiredName := false, operation.RetiredName
				if _, err := service.UpdateGatewayUserKey(ctx, credential, userID, operation.OldKeyID, clients.Sub2APIUpdateKeyInput{Name: &retiredName, Enabled: &disabled}); err != nil {
					return operation, err
				}
				readback, err := service.GatewayUserKey(ctx, credential, userID, operation.OldKeyID)
				if err != nil {
					return operation, err
				}
				if readback.Name != operation.RetiredName || readback.Status != "disabled" {
					return operation, errWorkspaceKeyRotationConflict
				}
			}
			operation.Phase = "promote_new"
			if err := app.persistWorkspaceKeyRotation(ctx, operationID, accountID, workspaceID, "started", operation); err != nil {
				return operation, err
			}
		case "promote_new":
			newKey, err := service.GatewayUserKey(ctx, credential, userID, operation.NewKeyID)
			if err != nil {
				return operation, err
			}
			canonicalName := workspaceReservedKeyName(workspaceID)
			if newKey.Name != canonicalName || newKey.Status != "active" {
				if newKey.Name != operation.ReplacementName || newKey.Status != "active" {
					return operation, errWorkspaceKeyRotationConflict
				}
				enabled := true
				if _, err := service.UpdateGatewayUserKey(ctx, credential, userID, operation.NewKeyID, clients.Sub2APIUpdateKeyInput{Name: &canonicalName, Enabled: &enabled}); err != nil {
					return operation, err
				}
				readback, err := service.GatewayUserKey(ctx, credential, userID, operation.NewKeyID)
				if err != nil {
					return operation, err
				}
				if readback.Name != canonicalName || readback.Status != "active" {
					return operation, errWorkspaceKeyRotationConflict
				}
			}
			operation.Phase = "delete_old"
			if err := app.persistWorkspaceKeyRotation(ctx, operationID, accountID, workspaceID, "started", operation); err != nil {
				return operation, err
			}
		case "delete_old":
			oldKey, err := service.GatewayUserKey(ctx, credential, userID, operation.OldKeyID)
			if err == nil {
				if oldKey.Name != operation.RetiredName || oldKey.Status != "disabled" {
					return operation, errWorkspaceKeyRotationConflict
				}
				if err := service.DeleteGatewayUserKey(ctx, credential, userID, operation.OldKeyID); err != nil {
					return operation, err
				}
				_, err = service.GatewayUserKey(ctx, credential, userID, operation.OldKeyID)
			}
			if !errors.Is(err, clients.ErrSub2APIKeyNotFound) {
				return operation, err
			}
			operation.Phase = "receipt"
			if err := app.persistWorkspaceKeyRotation(ctx, operationID, accountID, workspaceID, "started", operation); err != nil {
				return operation, err
			}
		case "receipt":
			receipt, err := service.RecordWorkspaceGatewayKeyRotation(ctx, accountID, workspaceID, ownerID, operationID, operation.OldKeyID, operation.NewKeyID, operation.Fingerprint)
			if err != nil {
				return operation, err
			}
			operation.ReceiptID = receipt.ReceiptID
			operation.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
			audit := app.auditEvent(r, "workspace.gateway_key.rotate", "workspace_gateway_key", workspaceID, accountID, nil, map[string]any{
				"operationId": operationID, "oldKeyId": operation.OldKeyID, "newKeyId": operation.NewKeyID,
				"fingerprint": operation.Fingerprint, "receiptId": operation.ReceiptID,
			}, "succeeded")
			audit["id"], audit["createdAt"] = "audit-"+stableID(operationID, "workspace.gateway_key.rotate")[:12], operation.CompletedAt
			if err := app.tables.SaveAuditEvent(ctx, audit); err != nil {
				return operation, errWorkspaceKeyRotationState
			}
			operation.Phase = "complete"
			if err := app.persistWorkspaceKeyRotation(ctx, operationID, accountID, workspaceID, "succeeded", operation); err != nil {
				return operation, err
			}
		case "complete":
			if !app.workspaceKeyRotationConverged(ctx, service, credential, userID, workspaceID, operation) {
				return operation, errWorkspaceKeyRotationConflict
			}
			return operation, nil
		default:
			return operation, errWorkspaceKeyRotationState
		}
	}
	return operation, errWorkspaceKeyRotationState
}

func workspaceReservedKeyName(workspaceID string) string {
	return "opl-workspace-" + stableID(workspaceID)[:12]
}

func workspaceRotationReplacementName(operationID string) string {
	return "opl-workspace-replacement-" + stableID(operationID)[:12]
}

func workspaceRuntimeGatewaySecretMatches(binding clients.WorkspaceRuntimeGatewaySecretBinding, operation workspaceKeyRotationOperation, workspaceID string) bool {
	return binding.Bound && binding.WorkspaceID == workspaceID && binding.WorkspaceAPIKeyID == operation.NewKeyID &&
		binding.SecretRef == operation.SecretRef && binding.Fingerprint == operation.Fingerprint
}

func workspaceRotationKeys(ctx context.Context, service *controlplane.Service, credential clients.SessionDelegatedCredential, userID int64) ([]clients.Sub2APIWorkspaceKey, error) {
	keys, err := service.GatewayUserKeys(ctx, credential, userID)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	for _, key := range keys {
		if key.ID <= 0 || key.UserID != userID || key.Name == "" || seen[key.ID] {
			return nil, errWorkspaceKeyRotationConflict
		}
		seen[key.ID] = true
	}
	return keys, nil
}

func workspaceRotationInitialKeysValid(keys []clients.Sub2APIWorkspaceKey, oldKeyID int64, canonicalName string) bool {
	found := false
	for _, key := range keys {
		if key.Name == canonicalName && key.ID != oldKeyID {
			return false
		}
		if key.ID == oldKeyID {
			if found || key.Status != "active" || key.Name != "opl-workspace" && key.Name != canonicalName {
				return false
			}
			found = true
		}
	}
	return found
}

func workspaceKeysNamed(keys []clients.Sub2APIWorkspaceKey, name string) []clients.Sub2APIWorkspaceKey {
	matches := make([]clients.Sub2APIWorkspaceKey, 0, 1)
	for _, key := range keys {
		if key.Name == name {
			matches = append(matches, key)
		}
	}
	return matches
}

func (app *controlPlaneServer) workspaceKeyRotationConverged(ctx context.Context, service *controlplane.Service, credential clients.SessionDelegatedCredential, userID int64, workspaceID string, operation workspaceKeyRotationOperation) bool {
	keys, err := workspaceRotationKeys(ctx, service, credential, userID)
	if err != nil {
		return false
	}
	canonical := make([]clients.Sub2APIWorkspaceKey, 0, 1)
	oldKeyPresent := false
	for _, key := range keys {
		if key.ID == operation.OldKeyID {
			oldKeyPresent = true
		}
		if key.Name == workspaceReservedKeyName(workspaceID) {
			canonical = append(canonical, key)
		}
	}
	workspace, ok := app.getWorkspace(workspaceID)
	binding, bindErr := service.WorkspaceRuntimeGatewaySecret(ctx, workspaceID)
	return ok && bindErr == nil && !oldKeyPresent && len(canonical) == 1 && canonical[0].ID == operation.NewKeyID && canonical[0].Status == "active" &&
		int64(numberField(workspace, "workspaceApiKeyId", 0)) == operation.NewKeyID &&
		workspaceRuntimeGatewaySecretMatches(binding, operation, workspaceID)
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
