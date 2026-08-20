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
var errWorkspaceKeyRotationDraining = errors.New("workspace_key_rotation_draining")
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
	response, _ := app.workspaceAccessResponse(context.Background(), row, time.Now().UTC())
	return response
}

func (app *controlPlaneServer) workspaceAccessResponse(ctx context.Context, row map[string]any, now time.Time) (map[string]any, string) {
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
	if _, canonical, err := app.canonicalWorkspaceLaunchForAccess(ctx, row); err != nil {
		response["openable"], response["accessState"] = false, "disabled"
		return response, "workspace_runtime_truth_unavailable"
	} else if canonical {
		return response, ""
	}
	accountID, workspaceID := firstNonEmpty(stringValue(response["accountId"]), stringValue(response["ownerAccountId"])), stringValue(response["id"])
	storage, ok, err := app.tables.GetStorage(ctx, canonicalStorageID)
	if err != nil {
		ok = false
	}
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

	compute, ok, err := app.tables.GetCompute(ctx, canonicalComputeID)
	if err != nil {
		ok = false
	}
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

	attachment, ok, err := app.tables.GetAttachment(ctx, stringValue(row["currentAttachmentId"]))
	if err != nil {
		ok = false
	}
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

func workspaceProjectionBillingRow(workspace domain.WorkspaceProjection, acceptedBillingState map[string]any) map[string]any {
	row := workspaceProjectionRow(workspace)
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
	RequestHash               string         `json:"requestHash"`
	Phase                     string         `json:"phase"`
	OldKeyID                  int64          `json:"oldKeyId"`
	NewKeyID                  int64          `json:"newKeyId,omitempty"`
	ReplacementName           string         `json:"replacementName"`
	RetiredName               string         `json:"retiredName"`
	ReplacementCreateStarted  bool           `json:"replacementCreateStarted,omitempty"`
	ReplacementGroupID        int64          `json:"replacementGroupId,omitempty"`
	BudgetSnapshotCaptured    bool           `json:"budgetSnapshotCaptured,omitempty"`
	OldQuotaUSDMicros         int64          `json:"oldQuotaUsdMicros,omitempty"`
	OldQuotaUsedUSDMicros     int64          `json:"oldQuotaUsedUsdMicros,omitempty"`
	ReplacementQuotaUSDMicros int64          `json:"replacementQuotaUsdMicros,omitempty"`
	RateLimit5hUSDMicros      int64          `json:"rateLimit5hUsdMicros,omitempty"`
	RateLimit1dUSDMicros      int64          `json:"rateLimit1dUsdMicros,omitempty"`
	RateLimit7dUSDMicros      int64          `json:"rateLimit7dUsdMicros,omitempty"`
	Usage5hUSDMicros          int64          `json:"usage5hUsdMicros,omitempty"`
	Usage1dUSDMicros          int64          `json:"usage1dUsdMicros,omitempty"`
	Usage7dUSDMicros          int64          `json:"usage7dUsdMicros,omitempty"`
	BudgetCapturedAt          string         `json:"budgetCapturedAt,omitempty"`
	SecretRef                 string         `json:"secretRef,omitempty"`
	Fingerprint               string         `json:"fingerprint,omitempty"`
	RuntimeID                 string         `json:"runtimeId,omitempty"`
	ReceiptID                 string         `json:"receiptId,omitempty"`
	CompletedAt               string         `json:"completedAt,omitempty"`
	AuditEvent                map[string]any `json:"auditEvent"`
}

func encodeWorkspaceKeyRotation(operation workspaceKeyRotationOperation) string {
	payload, _ := json.Marshal(operation)
	return string(payload)
}

func decodeWorkspaceKeyRotation(row map[string]any) (workspaceKeyRotationOperation, error) {
	var operation workspaceKeyRotationOperation
	if err := json.Unmarshal([]byte(stringValue(row["result"])), &operation); err != nil || operation.RequestHash == "" || operation.Phase == "" || operation.OldKeyID <= 0 || operation.ReplacementName == "" || operation.RetiredName == "" ||
		!validWorkspaceKeyRotationAudit(operation.AuditEvent, stringValue(row["id"]), stringValue(row["accountId"]), stringValue(row["workspaceId"])) {
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
	row, found, err := app.tables.GetRuntimeOperation(ctx, operationID)
	if err != nil {
		return workspaceKeyRotationOperation{}, false, errWorkspaceKeyRotationState
	}
	if !found {
		return workspaceKeyRotationOperation{}, false, nil
	}
	operation, decodeErr := decodeWorkspaceKeyRotation(row)
	if decodeErr != nil || stringValue(row["accountId"]) != accountID || stringValue(row["workspaceId"]) != workspaceID || stringValue(row["action"]) != "workspace.gateway_key.rotate" {
		return workspaceKeyRotationOperation{}, false, errWorkspaceKeyRotationState
	}
	if operation.RequestHash != requestHash {
		return workspaceKeyRotationOperation{}, false, errIdempotencyConflict
	}
	status := stringValue(row["status"])
	if status == "succeeded" {
		if !workspaceKeyRotationSucceededEvidenceValid(operation) {
			return workspaceKeyRotationOperation{}, false, errWorkspaceKeyRotationState
		}
		return operation, true, nil
	}
	if status != "started" && status != "manual_review" || operation.Phase == "succeeded" {
		return workspaceKeyRotationOperation{}, false, errWorkspaceKeyRotationState
	}
	return operation, false, nil
}

func (app *controlPlaneServer) claimWorkspaceKeyRotation(ctx context.Context, service *controlplane.Service, credential clients.SessionDelegatedCredential, userID int64, operationID, accountID, workspaceID, requestHash string, oldKeyID int64, auditEvent map[string]any) (workspaceKeyRotationOperation, bool, error) {
	if existing, complete, err := app.workspaceKeyRotation(ctx, operationID, accountID, workspaceID, requestHash); err != nil || existing.Phase != "" {
		return existing, complete, err
	}
	budgetOperations, err := queryRuntimeOperations(ctx, app.tables, runtimeOperationQuery{
		WorkspaceID: workspaceID, Action: workspaceGatewayBudgetAction, ExcludedStatuses: []string{"succeeded"},
	})
	if err != nil {
		return workspaceKeyRotationOperation{}, false, errWorkspaceKeyRotationState
	}
	if len(budgetOperations) != 0 {
		return workspaceKeyRotationOperation{}, false, errWorkspaceGatewayBudgetInProgress
	}
	operations, err := queryRuntimeOperations(ctx, app.tables, runtimeOperationQuery{
		WorkspaceID: workspaceID, Action: "workspace.gateway_key.rotate", ExcludedStatuses: []string{"succeeded"},
	})
	if err != nil {
		return workspaceKeyRotationOperation{}, false, errWorkspaceKeyRotationState
	}
	for _, row := range operations {
		if stringValue(row["workspaceId"]) == workspaceID && stringValue(row["action"]) == "workspace.gateway_key.rotate" && stringValue(row["status"]) != "succeeded" {
			return workspaceKeyRotationOperation{}, false, errWorkspaceKeyRotationInProgress
		}
	}
	keys, err := workspaceRotationKeys(ctx, service, credential, userID, "opl-workspace", workspaceReservedKeyName(workspaceID))
	if err != nil {
		return workspaceKeyRotationOperation{}, false, err
	}
	if !workspaceRotationInitialKeysValid(keys, oldKeyID, workspaceReservedKeyName(workspaceID)) {
		return workspaceKeyRotationOperation{}, false, errWorkspaceKeyRotationConflict
	}
	oldKey, err := service.GatewayUserKey(ctx, credential, userID, oldKeyID)
	if err != nil {
		return workspaceKeyRotationOperation{}, false, err
	}
	if !workspaceRotationOldKeyAdmissible(oldKey, userID, oldKeyID, workspaceID) {
		return workspaceKeyRotationOperation{}, false, errWorkspaceKeyRotationConflict
	}
	operation := workspaceKeyRotationOperation{
		RequestHash: requestHash, Phase: "replacement_check", OldKeyID: oldKeyID,
		ReplacementName: workspaceRotationReplacementName(operationID),
		RetiredName:     "opl-workspace-retired-" + stableID(operationID)[:12],
		AuditEvent:      auditEvent,
	}
	if err := app.tables.ClaimWorkspaceKeyRotation(ctx, workspaceKeyRotationRow(operationID, accountID, workspaceID, "started", operation)); err != nil {
		return workspaceKeyRotationOperation{}, false, err
	}
	return operation, false, nil
}

func validWorkspaceKeyRotationClaim(row map[string]any) (workspaceKeyRotationOperation, bool) {
	operation, err := decodeWorkspaceKeyRotation(row)
	return operation, err == nil && stringValue(row["id"]) != "" && stringValue(row["id"]) == stringValue(row["operationId"]) &&
		stringValue(row["accountId"]) != "" && stringValue(row["workspaceId"]) != "" && stringValue(row["resourceId"]) == stringValue(row["workspaceId"]) &&
		stringValue(row["resourceKind"]) == "workspace_gateway_key" && stringValue(row["action"]) == "workspace.gateway_key.rotate" && stringValue(row["status"]) == "started" &&
		operation.Phase == "replacement_check" && operation.NewKeyID == 0
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
	auditEvent := app.newWorkspaceKeyRotationAudit(r, operationID, accountID, workspaceID, oldKeyID)
	claimUnlock := app.lockWorkspaceGatewayMutation(workspaceID)
	claimed, complete, err := app.claimWorkspaceKeyRotation(r.Context(), service, credential, userID, operationID, accountID, workspaceID, requestHash, oldKeyID, auditEvent)
	claimUnlock()
	if err != nil {
		writeWorkspaceKeyRotationError(w, err)
		return
	}
	if complete {
		app.writeWorkspaceKeyRotationResponse(w, operationID, workspaceID, claimed)
		return
	}
	operationUnlock := app.lockResource("workspace-key-rotation", operationID)
	defer operationUnlock()
	operation, complete, err := app.workspaceKeyRotation(r.Context(), operationID, accountID, workspaceID, requestHash)
	if err != nil {
		writeWorkspaceKeyRotationError(w, err)
		return
	}
	if complete {
		app.writeWorkspaceKeyRotationResponse(w, operationID, workspaceID, operation)
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

func (app *controlPlaneServer) lockWorkspaceGatewayMutation(workspaceID string) func() {
	return app.lockResource("workspace-gateway-mutation", workspaceID)
}

func writeWorkspaceKeyRotationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errWorkspaceKeyRotationInProgress), errors.Is(err, errWorkspaceKeyRotationDraining), errors.Is(err, errWorkspaceGatewayBudgetInProgress), errors.Is(err, errWorkspaceKeyRotationConflict), errors.Is(err, errWorkspaceAPIKeyCASConflict), errors.Is(err, errIdempotencyConflict):
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
	for range 18 {
		switch operation.Phase {
		case "replacement_check":
			keys, err := workspaceRotationKeys(ctx, service, credential, userID, "opl-workspace", workspaceReservedKeyName(workspaceID))
			if err != nil {
				return operation, err
			}
			if !workspaceRotationInitialKeysValid(keys, operation.OldKeyID, workspaceReservedKeyName(workspaceID)) {
				return operation, errWorkspaceKeyRotationConflict
			}
			oldKey, err := service.GatewayUserKey(ctx, credential, userID, operation.OldKeyID)
			if err != nil {
				return operation, err
			}
			if !workspaceRotationOldKeyAdmissible(oldKey, userID, operation.OldKeyID, workspaceID) {
				return operation, errWorkspaceKeyRotationConflict
			}
			operation.Phase = "old_key_disable"
			if err := app.persistWorkspaceKeyRotation(ctx, operationID, accountID, workspaceID, "started", operation); err != nil {
				return operation, err
			}
		case "old_key_disable":
			oldKey, err := service.GatewayUserKey(ctx, credential, userID, operation.OldKeyID)
			if err != nil {
				return operation, err
			}
			if oldKey.ExpiresAt != nil || !workspaceRotationOldKeyMatches(oldKey, userID, operation.OldKeyID, workspaceID, oldKey.Status) || (oldKey.Status != "active" && oldKey.Status != "disabled") {
				return operation, errWorkspaceKeyRotationConflict
			}
			if oldKey.Status == "active" {
				disabled := false
				if _, err := service.UpdateGatewayUserKey(ctx, credential, userID, operation.OldKeyID, clients.Sub2APIUpdateKeyInput{Enabled: &disabled}); err != nil {
					return operation, err
				}
				oldKey, err = service.GatewayUserKey(ctx, credential, userID, operation.OldKeyID)
				if err != nil {
					return operation, err
				}
			}
			if !workspaceRotationOldKeyMatches(oldKey, userID, operation.OldKeyID, workspaceID, "disabled") || oldKey.ExpiresAt != nil {
				return operation, errWorkspaceKeyRotationConflict
			}
			operation.Phase = "old_key_drain"
			if err := app.persistWorkspaceKeyRotation(ctx, operationID, accountID, workspaceID, "started", operation); err != nil {
				return operation, err
			}
		case "old_key_drain":
			oldKey, err := service.GatewayUserKey(ctx, credential, userID, operation.OldKeyID)
			if err != nil {
				return operation, err
			}
			if !workspaceRotationOldKeyMatches(oldKey, userID, operation.OldKeyID, workspaceID, "disabled") || oldKey.ExpiresAt != nil {
				return operation, errWorkspaceKeyRotationConflict
			}
			if oldKey.CurrentConcurrency != 0 {
				return operation, errWorkspaceKeyRotationDraining
			}
			if !workspaceRotationBudgetTransferable(oldKey) {
				return operation, errWorkspaceKeyRotationConflict
			}
			operation.BudgetSnapshotCaptured = true
			operation.OldQuotaUSDMicros, operation.OldQuotaUsedUSDMicros = oldKey.QuotaUSDMicros, oldKey.QuotaUsedUSDMicros
			operation.ReplacementQuotaUSDMicros = oldKey.QuotaUSDMicros
			if oldKey.QuotaUSDMicros > 0 {
				operation.ReplacementQuotaUSDMicros -= oldKey.QuotaUsedUSDMicros
			}
			operation.RateLimit5hUSDMicros, operation.RateLimit1dUSDMicros, operation.RateLimit7dUSDMicros = oldKey.RateLimit5hUSDMicros, oldKey.RateLimit1dUSDMicros, oldKey.RateLimit7dUSDMicros
			operation.Usage5hUSDMicros, operation.Usage1dUSDMicros, operation.Usage7dUSDMicros = oldKey.Usage5hUSDMicros, oldKey.Usage1dUSDMicros, oldKey.Usage7dUSDMicros
			operation.BudgetCapturedAt = time.Now().UTC().Format(time.RFC3339Nano)
			operation.Phase = "replacement_create"
			if err := app.persistWorkspaceKeyRotation(ctx, operationID, accountID, workspaceID, "started", operation); err != nil {
				return operation, err
			}
		case "replacement_create":
			if !workspaceRotationBudgetSnapshotValid(operation) {
				return operation, errWorkspaceKeyRotationState
			}
			codexGroupID, groupErr := workspaceCodexGroupID(ctx, service, credential, userID)
			if groupErr != nil {
				return operation, groupErr
			}
			if operation.ReplacementGroupID != 0 && operation.ReplacementGroupID != codexGroupID {
				return operation, errWorkspaceKeyRotationConflict
			}
			operation.ReplacementGroupID = codexGroupID
			keys, err := workspaceRotationKeys(ctx, service, credential, userID, operation.ReplacementName)
			if err != nil {
				return operation, err
			}
			matches := workspaceKeysNamed(keys, operation.ReplacementName)
			if len(matches) > 1 || len(matches) == 1 && !operation.ReplacementCreateStarted {
				return operation, errWorkspaceKeyRotationConflict
			}
			if len(matches) == 1 {
				if !workspaceRotationReplacementPolicyMatches(matches[0], userID, operation, operation.ReplacementName) {
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
				created, err := service.CreateGatewayUserKey(ctx, credential, userID, clients.Sub2APICreateKeyInput{
					Name: operation.ReplacementName, GroupID: codexGroupID, QuotaUSDMicros: operation.ReplacementQuotaUSDMicros,
					RateLimit5hUSDMicros: operation.RateLimit5hUSDMicros, RateLimit1dUSDMicros: operation.RateLimit1dUSDMicros,
					RateLimit7dUSDMicros: operation.RateLimit7dUSDMicros,
				}, operationID+":replacement")
				if err != nil {
					return operation, err
				}
				if !workspaceRotationReplacementPolicyMatches(created, userID, operation, operation.ReplacementName) {
					return operation, errWorkspaceKeyRotationConflict
				}
				operation.NewKeyID = created.ID
			}
			operation.Phase = "replacement_policy_readback"
			if err := app.persistWorkspaceKeyRotation(ctx, operationID, accountID, workspaceID, "started", operation); err != nil {
				return operation, err
			}
		case "replacement_policy_readback":
			if !workspaceRotationBudgetSnapshotValid(operation) || operation.NewKeyID <= 0 {
				return operation, errWorkspaceKeyRotationState
			}
			codexGroupID, err := workspaceCodexGroupID(ctx, service, credential, userID)
			if err != nil {
				return operation, err
			}
			if operation.ReplacementGroupID != codexGroupID {
				return operation, errWorkspaceKeyRotationConflict
			}
			readback, err := service.GatewayUserKey(ctx, credential, userID, operation.NewKeyID)
			if err != nil {
				return operation, err
			}
			if !workspaceRotationReplacementPolicyMatches(readback, userID, operation, operation.ReplacementName) {
				return operation, errWorkspaceKeyRotationConflict
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
				AccountID: accountID, WorkspaceID: workspaceID, WorkspaceAPIKeyID: operation.NewKeyID,
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
				if oldKey.Name != "opl-workspace" && oldKey.Name != workspaceReservedKeyName(workspaceID) || oldKey.Status != "disabled" {
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
			if newKey.Name != canonicalName || !workspaceRotationReplacementStaticPolicyMatches(newKey, userID, operation) {
				if newKey.Name != operation.ReplacementName || !workspaceRotationReplacementStaticPolicyMatches(newKey, userID, operation) {
					return operation, errWorkspaceKeyRotationConflict
				}
				if _, err := service.UpdateGatewayUserKey(ctx, credential, userID, operation.NewKeyID, clients.Sub2APIUpdateKeyInput{Name: &canonicalName}); err != nil {
					return operation, err
				}
				readback, err := service.GatewayUserKey(ctx, credential, userID, operation.NewKeyID)
				if err != nil {
					return operation, err
				}
				if readback.Name != canonicalName || !workspaceRotationReplacementStaticPolicyMatches(readback, userID, operation) {
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
			operation.Phase = "complete"
			if err := app.persistWorkspaceKeyRotation(ctx, operationID, accountID, workspaceID, "started", operation); err != nil {
				return operation, err
			}
		case "complete":
			if !app.workspaceKeyRotationConverged(ctx, service, credential, userID, workspaceID, operation) {
				return operation, errWorkspaceKeyRotationConflict
			}
			audit := cloneMap(operation.AuditEvent)
			audit["after"] = map[string]any{
				"operationId": operationID, "oldKeyId": operation.OldKeyID, "newKeyId": operation.NewKeyID,
				"fingerprint": operation.Fingerprint, "receiptId": operation.ReceiptID,
			}
			audit["result"] = "succeeded"
			exists, err := app.auditIdentityExists(ctx, accountID, audit)
			if err != nil || !exists && app.tables.SaveAuditEvent(ctx, audit) != nil {
				return operation, errWorkspaceKeyRotationState
			}
			operation.Phase = "succeeded"
			if err := app.persistWorkspaceKeyRotation(ctx, operationID, accountID, workspaceID, "succeeded", operation); err != nil {
				return operation, err
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

func workspaceCodexGroupID(ctx context.Context, service *controlplane.Service, credential clients.SessionDelegatedCredential, userID int64) (int64, error) {
	groups, err := service.GatewayUserGroups(ctx, credential, userID)
	if err != nil {
		return 0, errWorkspaceCodexGroupUnavailable
	}
	var matches []clients.Sub2APIGroup
	for _, group := range groups {
		if group.ID > 0 && group.Name == "Codex" && strings.EqualFold(strings.TrimSpace(group.Status), "active") {
			matches = append(matches, group)
		}
	}
	if len(matches) != 1 {
		return 0, errWorkspaceCodexGroupUnavailable
	}
	return matches[0].ID, nil
}

func workspaceKeyCodexGroupMatches(key clients.Sub2APIWorkspaceKey, groupID int64) bool {
	return key.GroupID != nil && *key.GroupID == groupID
}

func workspaceRotationReplacementName(operationID string) string {
	return "opl-workspace-replacement-" + stableID(operationID)[:12]
}

func (app *controlPlaneServer) newWorkspaceKeyRotationAudit(r *http.Request, operationID, accountID, workspaceID string, oldKeyID int64) map[string]any {
	event := app.auditEvent(r, "workspace.gateway_key.rotate", "workspace_gateway_key", workspaceID, accountID,
		map[string]any{"workspaceApiKeyId": strconv.FormatInt(oldKeyID, 10)}, map[string]any{"operationId": operationID}, "started")
	event["id"] = "audit-" + stableID(operationID, "workspace.gateway_key.rotate")[:12]
	return event
}

func validWorkspaceKeyRotationAudit(event map[string]any, operationID, accountID, workspaceID string) bool {
	return stringValue(event["id"]) == "audit-"+stableID(operationID, "workspace.gateway_key.rotate")[:12] &&
		stringValue(event["action"]) == "workspace.gateway_key.rotate" && stringValue(event["resourceKind"]) == "workspace_gateway_key" &&
		stringValue(event["resourceId"]) == workspaceID && stringValue(event["actorAccountId"]) == accountID &&
		stringValue(event["targetAccountId"]) == accountID && stringValue(event["actorUserId"]) != "" && stringValue(event["createdAt"]) != ""
}

func workspaceKeyRotationSucceededEvidenceValid(operation workspaceKeyRotationOperation) bool {
	completedAt, err := time.Parse(time.RFC3339Nano, operation.CompletedAt)
	return operation.Phase == "succeeded" && operation.NewKeyID > 0 && operation.ReplacementGroupID > 0 &&
		operation.SecretRef != "" && operation.Fingerprint != "" && operation.ReceiptID != "" &&
		err == nil && !completedAt.IsZero() && workspaceRotationBudgetSnapshotValid(operation)
}

func workspaceRuntimeGatewaySecretMatches(binding clients.WorkspaceRuntimeGatewaySecretBinding, operation workspaceKeyRotationOperation, workspaceID string) bool {
	return binding.Bound && binding.WorkspaceID == workspaceID && binding.WorkspaceAPIKeyID == operation.NewKeyID &&
		binding.SecretRef == operation.SecretRef && binding.Fingerprint == operation.Fingerprint
}

func workspaceRotationKeys(ctx context.Context, service *controlplane.Service, credential clients.SessionDelegatedCredential, userID int64, names ...string) ([]clients.Sub2APIWorkspaceKey, error) {
	keys := make([]clients.Sub2APIWorkspaceKey, 0, len(names))
	seen := map[int64]bool{}
	for _, name := range names {
		matches, err := service.GatewayWorkspaceKeysForConvergence(ctx, credential, userID, name)
		if err != nil {
			return nil, err
		}
		for _, key := range matches {
			if key.ID <= 0 || key.UserID != userID || key.Name != name || seen[key.ID] {
				return nil, errWorkspaceKeyRotationConflict
			}
			seen[key.ID] = true
			keys = append(keys, key)
		}
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

func workspaceRotationOldKeyMatches(key clients.Sub2APIWorkspaceKey, userID, oldKeyID int64, workspaceID, status string) bool {
	return key.ID == oldKeyID && key.UserID == userID && key.Status == status &&
		(key.Name == "opl-workspace" || key.Name == workspaceReservedKeyName(workspaceID))
}

func workspaceRotationOldKeyAdmissible(key clients.Sub2APIWorkspaceKey, userID, oldKeyID int64, workspaceID string) bool {
	return workspaceRotationOldKeyMatches(key, userID, oldKeyID, workspaceID, "active") && key.ExpiresAt == nil &&
		workspaceRotationBudgetTransferable(key)
}

func workspaceRotationBudgetTransferable(key clients.Sub2APIWorkspaceKey) bool {
	if key.QuotaUSDMicros < 0 || key.QuotaUsedUSDMicros < 0 ||
		key.RateLimit5hUSDMicros < 0 || key.RateLimit1dUSDMicros < 0 || key.RateLimit7dUSDMicros < 0 ||
		key.Usage5hUSDMicros < 0 || key.Usage1dUSDMicros < 0 || key.Usage7dUSDMicros < 0 || key.CurrentConcurrency < 0 {
		return false
	}
	if key.QuotaUSDMicros > 0 && key.QuotaUsedUSDMicros >= key.QuotaUSDMicros {
		return false
	}
	return (key.RateLimit5hUSDMicros == 0 || key.Usage5hUSDMicros == 0) &&
		(key.RateLimit1dUSDMicros == 0 || key.Usage1dUSDMicros == 0) &&
		(key.RateLimit7dUSDMicros == 0 || key.Usage7dUSDMicros == 0)
}

func workspaceRotationBudgetSnapshotValid(operation workspaceKeyRotationOperation) bool {
	capturedAt, err := time.Parse(time.RFC3339Nano, operation.BudgetCapturedAt)
	if !operation.BudgetSnapshotCaptured || err != nil || capturedAt.IsZero() ||
		operation.OldQuotaUSDMicros < 0 || operation.OldQuotaUsedUSDMicros < 0 || operation.ReplacementQuotaUSDMicros < 0 ||
		operation.RateLimit5hUSDMicros < 0 || operation.RateLimit1dUSDMicros < 0 || operation.RateLimit7dUSDMicros < 0 ||
		operation.Usage5hUSDMicros < 0 || operation.Usage1dUSDMicros < 0 || operation.Usage7dUSDMicros < 0 {
		return false
	}
	if operation.OldQuotaUSDMicros == 0 {
		if operation.ReplacementQuotaUSDMicros != 0 {
			return false
		}
	} else if operation.OldQuotaUsedUSDMicros >= operation.OldQuotaUSDMicros ||
		operation.ReplacementQuotaUSDMicros != operation.OldQuotaUSDMicros-operation.OldQuotaUsedUSDMicros {
		return false
	}
	return (operation.RateLimit5hUSDMicros == 0 || operation.Usage5hUSDMicros == 0) &&
		(operation.RateLimit1dUSDMicros == 0 || operation.Usage1dUSDMicros == 0) &&
		(operation.RateLimit7dUSDMicros == 0 || operation.Usage7dUSDMicros == 0)
}

func workspaceRotationReplacementPolicyMatches(key clients.Sub2APIWorkspaceKey, userID int64, operation workspaceKeyRotationOperation, name string) bool {
	if key.ID <= 0 || key.UserID != userID || key.Name != name ||
		len(key.IPWhitelist) != 0 || len(key.IPBlacklist) != 0 || key.QuotaUsedUSDMicros != 0 ||
		key.Usage5hUSDMicros != 0 || key.Usage1dUSDMicros != 0 || key.Usage7dUSDMicros != 0 {
		return false
	}
	if operation.NewKeyID == 0 {
		operation.NewKeyID = key.ID
	}
	return key.Status == "active" && workspaceRotationReplacementStaticPolicyMatches(key, userID, operation)
}

func workspaceRotationReplacementStaticPolicyMatches(key clients.Sub2APIWorkspaceKey, userID int64, operation workspaceKeyRotationOperation) bool {
	if !workspaceRotationBudgetSnapshotValid(operation) || operation.NewKeyID <= 0 || operation.ReplacementGroupID <= 0 ||
		key.ID != operation.NewKeyID || key.UserID != userID || key.ExpiresAt != nil || !workspaceKeyCodexGroupMatches(key, operation.ReplacementGroupID) ||
		len(key.IPWhitelist) != 0 || len(key.IPBlacklist) != 0 ||
		key.QuotaUSDMicros != operation.ReplacementQuotaUSDMicros || key.QuotaUsedUSDMicros < 0 ||
		key.RateLimit5hUSDMicros != operation.RateLimit5hUSDMicros || key.RateLimit1dUSDMicros != operation.RateLimit1dUSDMicros ||
		key.RateLimit7dUSDMicros != operation.RateLimit7dUSDMicros ||
		key.Usage5hUSDMicros < 0 || key.Usage1dUSDMicros < 0 || key.Usage7dUSDMicros < 0 {
		return false
	}
	return key.Status == "active" || key.Status == "quota_exhausted"
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
	keys, err := workspaceRotationKeys(ctx, service, credential, userID, workspaceReservedKeyName(workspaceID))
	if err != nil {
		return false
	}
	oldKeyPresent := false
	if _, oldErr := service.GatewayUserKey(ctx, credential, userID, operation.OldKeyID); oldErr == nil {
		oldKeyPresent = true
	} else if !errors.Is(oldErr, clients.ErrSub2APIKeyNotFound) {
		return false
	}
	workspace, ok := app.getWorkspace(workspaceID)
	binding, bindErr := service.WorkspaceRuntimeGatewaySecret(ctx, workspaceID)
	return ok && bindErr == nil && !oldKeyPresent && len(keys) == 1 && workspaceRotationReplacementStaticPolicyMatches(keys[0], userID, operation) &&
		int64(numberField(workspace, "workspaceApiKeyId", 0)) == operation.NewKeyID &&
		workspaceRuntimeGatewaySecretMatches(binding, operation, workspaceID)
}

func (app *controlPlaneServer) proxyWorkspace(w http.ResponseWriter, r *http.Request, service *controlplane.Service) {
	workspaceID := workspaceIDFromPath(r.URL.Path)
	if workspaceID == "" {
		http.NotFound(w, r)
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, "/w/"+workspaceID)
	app.proxyWorkspaceTo(w, r, service, workspaceID, suffix)
}

func (app *controlPlaneServer) proxyWorkspaceRoot(w http.ResponseWriter, r *http.Request, service *controlplane.Service) {
	if !isWorkspaceRequest(r) {
		http.NotFound(w, r)
		return
	}
	workspaceID := workspaceIDFromGatewayRequest(r)
	if workspaceID == "" {
		http.NotFound(w, r)
		return
	}
	app.proxyWorkspaceTo(w, r, service, workspaceID, r.URL.Path)
}

func (app *controlPlaneServer) proxyWorkspaceTo(w http.ResponseWriter, r *http.Request, service *controlplane.Service, workspaceID string, proxyPath string) {
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
	response, blockReason := app.workspaceAccessResponse(r.Context(), cloneMap(workspace), time.Now().UTC())
	if blockReason != "" {
		writeError(w, http.StatusConflict, blockReason)
		return
	}
	if response["openable"] != true {
		writeError(w, http.StatusConflict, "workspace_runtime_not_ready")
		return
	}
	operation, err := app.succeededWorkspaceLaunchForAccess(r.Context(), workspace)
	if err != nil || service == nil {
		writeError(w, http.StatusConflict, "workspace_runtime_truth_unavailable")
		return
	}
	serviceName := operation.stringFact("runtimeServiceName")
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
		stripWorkspaceProxyCredentials(req)
		if proxyPath == "" {
			proxyPath = "/"
		}
		req.URL.Path = proxyPath
		req.URL.RawPath = ""
		req.Host = target.Host
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		response.Header.Del("Set-Cookie")
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		writeUpstreamError(w)
	}
	proxy.ServeHTTP(w, r)
}

func stripWorkspaceProxyCredentials(r *http.Request) {
	for _, header := range []string{"Authorization", "Cookie", "X-OPL-CSRF", "X-OPL-CSRF-Token"} {
		r.Header.Del(header)
	}
}

func (app *controlPlaneServer) succeededWorkspaceLaunchForAccess(ctx context.Context, workspace map[string]any) (workspaceLaunchReconcileOperation, error) {
	operation, found, err := app.canonicalWorkspaceLaunchForAccess(ctx, workspace)
	if err != nil || !found {
		return workspaceLaunchReconcileOperation{}, errors.New("workspace_runtime_truth_unavailable")
	}
	return operation, nil
}

func (app *controlPlaneServer) canonicalWorkspaceLaunchForAccess(ctx context.Context, workspace map[string]any) (workspaceLaunchReconcileOperation, bool, error) {
	return app.canonicalWorkspaceLaunch(ctx, workspace, workspaceLaunchProjectionMatches)
}

func (app *controlPlaneServer) canonicalWorkspaceLaunch(ctx context.Context, workspace map[string]any, matches func(workspaceLaunchReconcileOperation, map[string]any) bool) (workspaceLaunchReconcileOperation, bool, error) {
	workspaceID := stringValue(workspace["id"])
	rows, err := queryRuntimeOperations(ctx, app.tables, runtimeOperationQuery{
		WorkspaceID: workspaceID, Action: workspaceLaunchAction,
	})
	if err != nil {
		return workspaceLaunchReconcileOperation{}, false, err
	}
	if len(rows) == 0 {
		return workspaceLaunchReconcileOperation{}, false, nil
	}
	if len(rows) != 1 {
		return workspaceLaunchReconcileOperation{}, true, errors.New("workspace_runtime_truth_unavailable")
	}
	operation, err := decodeWorkspaceLaunchReconcileOperation(rows[0])
	if err != nil || operation.Status != "succeeded" || operation.Stage != "succeeded" || operation.stringFact("receiptId") == "" ||
		operation.stringFact("receiptOperationId") != operation.ID+":purchase-receipt" ||
		!matches(operation, workspace) {
		return workspaceLaunchReconcileOperation{}, true, errors.New("workspace_runtime_truth_unavailable")
	}
	return operation, true, nil
}
