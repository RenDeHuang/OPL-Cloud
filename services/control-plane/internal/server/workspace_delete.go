package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const workspaceDeleteAction = "workspace.delete.v1"

var (
	errWorkspaceDeleteCASConflict = errors.New("workspace_delete_cas_conflict")
	errWorkspaceDeleteUnconfirmed = errors.New("workspace_delete_unconfirmed")
)

type workspaceDeleteOperation struct {
	OperationID        string               `json:"operationId"`
	RequestHash        string               `json:"requestHash"`
	AccountID          string               `json:"accountId"`
	OwnerUserID        string               `json:"ownerUserId"`
	Sub2APIUserID      int64                `json:"sub2apiUserId"`
	WorkspaceID        string               `json:"workspaceId"`
	LaunchOperationID  string               `json:"launchOperationId"`
	RuntimeID          string               `json:"runtimeId"`
	ComputeID          string               `json:"computeId"`
	StorageID          string               `json:"storageId"`
	AttachmentID       string               `json:"attachmentId"`
	WorkspaceAPIKeyID  int64                `json:"workspaceApiKeyId"`
	GatewaySecretRef   string               `json:"gatewaySecretRef"`
	GatewayFingerprint string               `json:"gatewayFingerprint"`
	DebitCode          string               `json:"debitCode"`
	PurchaseReceiptID  string               `json:"purchaseReceiptId"`
	PurchaseReceipt    clients.ReceiptInput `json:"purchaseReceipt"`
	RefundCode         string               `json:"refundCode"`
	RefundReceiptID    string               `json:"refundReceiptId,omitempty"`
	TotalUSDMicros     int64                `json:"totalUsdMicros"`
	Phase              string               `json:"phase"`
	Status             string               `json:"status"`
	RuntimeStatus      string               `json:"runtimeStatus,omitempty"`
	SecretStatus       string               `json:"secretStatus,omitempty"`
	AttachmentStatus   string               `json:"attachmentStatus,omitempty"`
	StorageStatus      string               `json:"storageStatus,omitempty"`
	ComputeStatus      string               `json:"computeStatus,omitempty"`
	KeyStatus          string               `json:"keyStatus,omitempty"`
	KeyDeleteAttempted bool                 `json:"keyDeleteAttempted,omitempty"`
	RefundAttempted    bool                 `json:"refundAttempted,omitempty"`
	RefundConfirmation map[string]any       `json:"refundConfirmation,omitempty"`
	LastErrorCode      string               `json:"lastErrorCode,omitempty"`
	CreatedAt          string               `json:"createdAt"`
}

type workspaceDeleteStoreMutation struct {
	Create                 bool
	DeleteWorkspace        bool
	RequireWorkspaceAbsent bool
	ExpectedResult         string
	DesiredOperation       map[string]any
}

func (app *controlPlaneServer) deleteWorkspace(w http.ResponseWriter, r *http.Request, service *controlplane.Service) {
	_, ok := requiredMutationKey(w, r)
	if !ok {
		return
	}
	user, sub2APIUserID, credential, ok := app.gatewayUserContext(w, r)
	if !ok {
		return
	}
	workspaceID := strings.TrimSpace(r.PathValue("workspaceId"))
	if workspaceID == "" {
		writeError(w, http.StatusNotFound, "workspace_not_found")
		return
	}

	unlock := app.lockResource("workspace-delete", workspaceID)
	defer unlock()

	operation, found, err := app.workspaceDeleteOperation(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "state_read_failed")
		return
	}
	if !found {
		workspace, workspaceFound, readErr := app.tables.GetWorkspace(r.Context(), workspaceID)
		if readErr != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		if !workspaceFound {
			writeError(w, http.StatusNotFound, "workspace_not_found")
			return
		}
		accountID := firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"]))
		ownerUserID := firstNonEmpty(stringValue(workspace["ownerUserId"]), stringValue(workspace["ownerId"]))
		if accountID != stringValue(user["accountId"]) || !app.canAccessResource(r, workspace) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		if ownerUserID == "" || ownerUserID != stringValue(user["id"]) {
			writeError(w, http.StatusForbidden, "workspace_owner_required")
			return
		}
		operation, err = app.newWorkspaceDeleteOperation(r.Context(), service, workspace, sub2APIUserID, time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusBadGateway, "workspace_delete_identity_unconfirmed")
			return
		}
		if err := app.tables.ApplyWorkspaceDelete(r.Context(), workspaceDeleteStoreMutation{
			Create: true, DesiredOperation: workspaceDeleteOperationRow(operation),
		}); errors.Is(err, errWorkspaceDeleteCASConflict) {
			operation, found, err = app.workspaceDeleteOperation(r.Context(), workspaceID)
			if err != nil || !found {
				writeError(w, http.StatusConflict, errWorkspaceDeleteCASConflict.Error())
				return
			}
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
	}

	requestHash := workspaceDeleteRequestHash(operation)
	if operation.AccountID != stringValue(user["accountId"]) || operation.OwnerUserID != stringValue(user["id"]) {
		writeError(w, http.StatusForbidden, "workspace_owner_required")
		return
	}
	if operation.RequestHash != requestHash {
		writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
		return
	}

	if operation.Sub2APIUserID != sub2APIUserID {
		writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
		return
	}
	operation, err = app.runWorkspaceDelete(r.Context(), service, credential, operation)
	if err != nil {
		if errors.Is(err, errWorkspaceDeleteUnconfirmed) {
			writeJSON(w, http.StatusBadGateway, workspaceDeleteResponse(operation, "workspace_delete_unconfirmed"))
			return
		}
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return
	}
	writeJSON(w, http.StatusOK, workspaceDeleteResponse(operation, ""))
}

func (app *controlPlaneServer) workspaceDeleteOperation(ctx context.Context, workspaceID string) (workspaceDeleteOperation, bool, error) {
	row, found, err := app.tables.GetRuntimeOperation(ctx, workspaceDeleteOperationID(workspaceID))
	if err != nil || !found {
		return workspaceDeleteOperation{}, found, err
	}
	operation, err := decodeWorkspaceDeleteOperation(row)
	return operation, err == nil, err
}

func (app *controlPlaneServer) newWorkspaceDeleteOperation(ctx context.Context, service *controlplane.Service, workspace map[string]any, sub2APIUserID int64, now time.Time) (workspaceDeleteOperation, error) {
	workspaceID := stringValue(workspace["id"])
	accountID := firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"]))
	ownerUserID := firstNonEmpty(stringValue(workspace["ownerUserId"]), stringValue(workspace["ownerId"]))
	launch, err := app.succeededWorkspaceLaunchForAccess(ctx, workspace)
	if err != nil || launch.int64Fact("sub2apiUserId") != sub2APIUserID {
		return workspaceDeleteOperation{}, errWorkspaceDeleteUnconfirmed
	}
	purchaseInput, err := workspaceLaunchPurchaseReceiptInput(launch)
	if err != nil {
		return workspaceDeleteOperation{}, errWorkspaceDeleteUnconfirmed
	}
	purchaseReceiptID := launch.stringFact("receiptId")
	purchase, err := service.BillingReceiptForAccount(ctx, accountID, workspaceID, purchaseReceiptID)
	if err != nil || purchase.ReceiptID != purchaseReceiptID || !workspaceLaunchReceiptInputMatches(purchase.ReceiptInput, purchaseInput) {
		return workspaceDeleteOperation{}, errWorkspaceDeleteUnconfirmed
	}
	debitCode, totalUSDMicros := launch.stringFact("sub2apiRedeemCode"), launch.int64Fact("totalChargeUsdMicros")
	history, err := service.FinancialBalanceHistoryByCodes(ctx, sub2APIUserID, []string{debitCode})
	if err != nil || !workspaceDeleteDebitHistoryMatches(sub2APIUserID, debitCode, totalUSDMicros, history) {
		return workspaceDeleteOperation{}, errWorkspaceDeleteUnconfirmed
	}
	operation := workspaceDeleteOperation{
		OperationID: workspaceDeleteOperationID(workspaceID), AccountID: accountID, OwnerUserID: ownerUserID, Sub2APIUserID: sub2APIUserID,
		WorkspaceID: workspaceID, LaunchOperationID: launch.ID, RuntimeID: launch.stringFact("runtimeId"),
		ComputeID: launch.stringFact("computeAllocationId"), StorageID: launch.stringFact("storageId"), AttachmentID: launch.stringFact("attachmentId"),
		WorkspaceAPIKeyID: launch.int64Fact("workspaceApiKeyId"), GatewaySecretRef: launch.stringFact("gatewaySecretRef"), GatewayFingerprint: launch.stringFact("workspaceKeyFingerprint"),
		DebitCode: debitCode, PurchaseReceiptID: purchaseReceiptID, PurchaseReceipt: purchaseInput,
		TotalUSDMicros: totalUSDMicros, Phase: "claimed", Status: "running", CreatedAt: now.Format(time.RFC3339Nano),
	}
	operation.RefundCode = monthlyRefundCode(monthlyEnvironment(), operation.OperationID)
	operation.RequestHash = workspaceDeleteRequestHash(operation)
	if !validWorkspaceDeleteIdentity(operation) {
		return workspaceDeleteOperation{}, errWorkspaceDeleteUnconfirmed
	}
	return operation, nil
}

func workspaceDeleteOperationID(workspaceID string) string {
	return "workspace-delete-" + stableID(workspaceDeleteAction, workspaceID)[:18]
}

func workspaceDeleteRequestHash(operation workspaceDeleteOperation) string {
	purchaseReceipt, _ := json.Marshal(operation.PurchaseReceipt)
	return stableID(workspaceDeleteAction, operation.AccountID, operation.OwnerUserID, operation.WorkspaceID, operation.LaunchOperationID,
		operation.RuntimeID, operation.ComputeID, operation.StorageID, operation.AttachmentID, operation.GatewaySecretRef, operation.GatewayFingerprint,
		operation.DebitCode, operation.PurchaseReceiptID, string(purchaseReceipt), operation.RefundCode,
		strconv.FormatInt(operation.Sub2APIUserID, 10), strconv.FormatInt(operation.WorkspaceAPIKeyID, 10), strconv.FormatInt(operation.TotalUSDMicros, 10))
}

func workspaceDeleteStageKey(operation workspaceDeleteOperation, stage string) string {
	return operation.OperationID + ":" + stage
}

func workspaceDeleteDebitHistoryMatches(userID int64, code string, amount int64, history map[string]clients.Sub2APIBalanceHistoryEntry) bool {
	entry, found := history[code]
	return len(history) == 1 && found && entry.Code == code && entry.Type == "balance" && entry.Status == "used" && entry.UsedBy != nil && *entry.UsedBy == userID &&
		entry.UsedAt != nil && !entry.UsedAt.IsZero() && amount > 0 && entry.ValueUSDMicros == -amount
}

func workspaceDeleteOperationRow(operation workspaceDeleteOperation) map[string]any {
	encoded, _ := json.Marshal(operation)
	return map[string]any{
		"id": operation.OperationID, "operationId": operation.OperationID,
		"accountId": operation.AccountID, "workspaceId": operation.WorkspaceID,
		"resourceId": operation.WorkspaceID, "resourceKind": "workspace", "action": workspaceDeleteAction,
		"status": operation.Status, "result": string(encoded),
		"computeAllocationId": operation.ComputeID, "storageId": operation.StorageID, "attachmentId": operation.AttachmentID, "runtimeId": operation.RuntimeID,
		"createdAt": operation.CreatedAt,
	}
}

func validWorkspaceDeleteIdentity(operation workspaceDeleteOperation) bool {
	if operation.OperationID == "" || operation.OperationID != workspaceDeleteOperationID(operation.WorkspaceID) || operation.AccountID == "" || operation.OwnerUserID == "" ||
		operation.Sub2APIUserID <= 0 || operation.WorkspaceID == "" || operation.LaunchOperationID == "" || operation.RuntimeID == "" || operation.ComputeID == "" ||
		operation.StorageID == "" || operation.AttachmentID == "" || operation.WorkspaceAPIKeyID <= 0 || operation.GatewaySecretRef == "" || operation.GatewayFingerprint == "" ||
		operation.DebitCode == "" || operation.PurchaseReceiptID == "" || operation.RefundCode == "" || operation.RefundCode == operation.DebitCode || operation.TotalUSDMicros <= 0 ||
		operation.PurchaseReceipt.Type != "billing.workspace_purchased.v1" || operation.PurchaseReceipt.Status != "completed" ||
		operation.PurchaseReceipt.AccountID != operation.AccountID || operation.PurchaseReceipt.WorkspaceID != operation.WorkspaceID ||
		operation.PurchaseReceipt.RequestID != operation.LaunchOperationID || operation.CreatedAt == "" {
		return false
	}
	cost := operation.PurchaseReceipt.Cost
	execution := operation.PurchaseReceipt.Execution
	return int64(numberField(cost, "sub2apiUserId", 0)) == operation.Sub2APIUserID && stringValue(cost["sub2apiRedeemCode"]) == operation.DebitCode &&
		int64(numberField(cost, "totalUsdMicros", 0)) == operation.TotalUSDMicros && stringValue(cost["resourceId"]) == operation.WorkspaceID &&
		stringValue(execution["runtimeId"]) == operation.RuntimeID && stringValue(execution["computeAllocationId"]) == operation.ComputeID &&
		stringValue(execution["storageId"]) == operation.StorageID && stringValue(execution["attachmentId"]) == operation.AttachmentID &&
		int64(numberField(execution, "workspaceApiKeyId", 0)) == operation.WorkspaceAPIKeyID
}

func decodeWorkspaceDeleteOperation(row map[string]any) (workspaceDeleteOperation, error) {
	var operation workspaceDeleteOperation
	if stringValue(row["action"]) != workspaceDeleteAction || json.Unmarshal([]byte(stringValue(row["result"])), &operation) != nil ||
		operation.OperationID == "" || operation.OperationID != stringValue(row["id"]) || operation.OperationID != stringValue(row["operationId"]) ||
		operation.AccountID == "" || operation.AccountID != stringValue(row["accountId"]) || operation.OwnerUserID == "" ||
		operation.WorkspaceID == "" || operation.WorkspaceID != stringValue(row["workspaceId"]) || operation.WorkspaceID != stringValue(row["resourceId"]) ||
		stringValue(row["resourceKind"]) != "workspace" || operation.Status != stringValue(row["status"]) || !validWorkspaceDeleteIdentity(operation) ||
		operation.RequestHash == "" || operation.RequestHash != workspaceDeleteRequestHash(operation) {
		return workspaceDeleteOperation{}, errors.New("workspace_delete_operation_invalid")
	}
	return operation, nil
}

func workspaceDeleteOperationIdentityMatches(row map[string]any, desired workspaceDeleteOperation) bool {
	current, err := decodeWorkspaceDeleteOperation(row)
	return err == nil && current.OperationID == desired.OperationID && current.RequestHash == desired.RequestHash &&
		current.AccountID == desired.AccountID && current.OwnerUserID == desired.OwnerUserID && current.WorkspaceID == desired.WorkspaceID &&
		current.Sub2APIUserID == desired.Sub2APIUserID && current.LaunchOperationID == desired.LaunchOperationID && current.RuntimeID == desired.RuntimeID &&
		current.ComputeID == desired.ComputeID && current.StorageID == desired.StorageID && current.AttachmentID == desired.AttachmentID &&
		current.WorkspaceAPIKeyID == desired.WorkspaceAPIKeyID && current.GatewaySecretRef == desired.GatewaySecretRef && current.GatewayFingerprint == desired.GatewayFingerprint &&
		current.DebitCode == desired.DebitCode && current.PurchaseReceiptID == desired.PurchaseReceiptID && current.RefundCode == desired.RefundCode &&
		current.TotalUSDMicros == desired.TotalUSDMicros && workspaceLaunchReceiptInputMatches(current.PurchaseReceipt, desired.PurchaseReceipt) &&
		current.CreatedAt == desired.CreatedAt
}

func validWorkspaceDeleteStoreMutation(mutation workspaceDeleteStoreMutation) (workspaceDeleteOperation, bool) {
	desired, err := decodeWorkspaceDeleteOperation(mutation.DesiredOperation)
	if err != nil || mutation.DeleteWorkspace && mutation.RequireWorkspaceAbsent || mutation.Create && (mutation.ExpectedResult != "" || mutation.DeleteWorkspace || mutation.RequireWorkspaceAbsent) {
		return workspaceDeleteOperation{}, false
	}
	if !mutation.Create && mutation.ExpectedResult == "" {
		return workspaceDeleteOperation{}, false
	}
	return desired, true
}

func (app *controlPlaneServer) runWorkspaceDelete(ctx context.Context, service *controlplane.Service, credential clients.SessionDelegatedCredential, operation workspaceDeleteOperation) (workspaceDeleteOperation, error) {
	for {
		if !validWorkspaceDeleteIdentity(operation) {
			return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "workspace_delete_identity_mismatch")
		}
		switch operation.Phase {
		case "claimed":
			runtimeObservation, secretObservation, err := observeWorkspaceDeleteRuntimeAndSecret(ctx, service, operation)
			if err != nil {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_runtime_readback_unavailable")
			}
			if workspaceDeleteRuntimeAndSecretAbsent(runtimeObservation, secretObservation) {
				next := operation
				next.Phase, next.Status, next.RuntimeStatus, next.SecretStatus, next.LastErrorCode = "runtime_destroyed", "running", "absent", "absent", ""
				if err := app.persistWorkspaceDelete(ctx, operation, next, false, false); err != nil {
					return operation, err
				}
				operation = next
				continue
			}
			if !workspaceDeleteRuntimeAndSecretReady(operation, runtimeObservation, secretObservation) {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_runtime_identity_conflict")
			}
			runtime, destroyErr := service.DestroyWorkspaceRuntime(ctx, operation.AccountID, operation.WorkspaceID, workspaceDeleteStageKey(operation, "runtime"))
			runtimeObservation, secretObservation, err = observeWorkspaceDeleteRuntimeAndSecret(ctx, service, operation)
			if err == nil && workspaceDeleteRuntimeAndSecretAbsent(runtimeObservation, secretObservation) {
				// Authoritative owner absence resolves a lost or contradictory transport response.
			} else if destroyErr != nil || runtime.ID != operation.RuntimeID || runtime.WorkspaceID != operation.WorkspaceID || runtime.Status != "destroyed" {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_runtime_destroy_unconfirmed")
			} else {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_runtime_absence_unconfirmed")
			}
			next := operation
			next.Phase, next.Status, next.RuntimeStatus, next.SecretStatus, next.LastErrorCode = "runtime_destroyed", "running", "absent", "absent", ""
			if err := app.persistWorkspaceDelete(ctx, operation, next, false, false); err != nil {
				return operation, err
			}
			operation = next
		case "runtime_destroyed":
			attachment, err := service.DetachWorkspaceStorage(ctx, operation.AccountID, operation.WorkspaceID, operation.AttachmentID, workspaceDeleteStageKey(operation, "attachment"))
			if err != nil || !workspaceDeleteAttachmentMatches(operation, attachment) {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_attachment_unconfirmed")
			}
			next := operation
			next.Phase, next.Status, next.AttachmentStatus, next.LastErrorCode = "attachment_detached", "running", attachment.Status, ""
			if err := app.persistWorkspaceDelete(ctx, operation, next, false, false); err != nil {
				return operation, err
			}
			operation = next
		case "attachment_detached":
			storage, err := service.DestroyWorkspaceStorage(ctx, operation.AccountID, operation.WorkspaceID, operation.StorageID, workspaceDeleteStageKey(operation, "storage"))
			if err != nil || !workspaceDeleteStorageMatches(operation, storage) {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_storage_unconfirmed")
			}
			next := operation
			next.Phase, next.Status, next.StorageStatus, next.LastErrorCode = "storage_destroyed", "running", storage.Status, ""
			if err := app.persistWorkspaceDelete(ctx, operation, next, false, false); err != nil {
				return operation, err
			}
			operation = next
		case "storage_destroyed":
			compute, err := service.DestroyWorkspaceCompute(ctx, operation.AccountID, operation.WorkspaceID, operation.ComputeID, workspaceDeleteStageKey(operation, "compute"))
			if err != nil || !workspaceDeleteComputeStartMatches(operation, compute) {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_compute_destroy_unconfirmed")
			}
			compute, err = awaitWorkspaceDeleteComputeReadback(ctx, service, operation)
			if err != nil {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_compute_absence_unconfirmed")
			}
			next := operation
			next.Phase, next.Status, next.ComputeStatus, next.LastErrorCode = "compute_destroyed", "running", compute.Status, ""
			if err := app.persistWorkspaceDelete(ctx, operation, next, false, false); err != nil {
				return operation, err
			}
			operation = next
		case "compute_destroyed":
			key, err := service.GatewayUserKey(ctx, credential, operation.Sub2APIUserID, operation.WorkspaceAPIKeyID)
			if errors.Is(err, clients.ErrSub2APIKeyNotFound) {
				next := operation
				next.Phase, next.Status, next.KeyStatus, next.LastErrorCode = "key_deleted", "running", "absent", ""
				if err := app.persistWorkspaceDelete(ctx, operation, next, false, false); err != nil {
					return operation, err
				}
				operation = next
				continue
			}
			if err != nil || !workspaceDeleteKeyMatches(operation, key) {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "sub2api_key_readback_unconfirmed")
			}
			if operation.KeyDeleteAttempted {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "sub2api_key_delete_unconfirmed")
			}
			attempted := operation
			attempted.Status, attempted.KeyDeleteAttempted, attempted.LastErrorCode = "running", true, ""
			if err := app.persistWorkspaceDelete(ctx, operation, attempted, false, false); err != nil {
				return operation, err
			}
			operation = attempted
			deleteErr := service.DeleteGatewayUserKey(ctx, credential, operation.Sub2APIUserID, operation.WorkspaceAPIKeyID)
			_, err = service.GatewayUserKey(ctx, credential, operation.Sub2APIUserID, operation.WorkspaceAPIKeyID)
			if !errors.Is(err, clients.ErrSub2APIKeyNotFound) {
				if deleteErr != nil {
					return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "sub2api_key_delete_unconfirmed")
				}
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "sub2api_key_absence_unconfirmed")
			}
			next := operation
			next.Phase, next.Status, next.KeyStatus, next.LastErrorCode = "key_deleted", "running", "absent", ""
			if err := app.persistWorkspaceDelete(ctx, operation, next, false, false); err != nil {
				return operation, err
			}
			operation = next
		case "key_deleted":
			next, err := app.refundWorkspaceDelete(ctx, service, credential, operation)
			if err != nil {
				return next, err
			}
			operation = next
		case "refund_confirmed":
			next, err := app.recordWorkspaceDeleteRefundReceipt(ctx, service, operation)
			if err != nil {
				return next, err
			}
			operation = next
		case "refund_receipt_recorded":
			next := operation
			next.Phase, next.Status, next.LastErrorCode = "workspace_deleted", "running", ""
			if err := app.persistWorkspaceDelete(ctx, operation, next, true, false); err != nil {
				return operation, err
			}
			operation = next
		case "workspace_deleted":
			next := operation
			next.Phase, next.Status, next.LastErrorCode = "complete", "succeeded", ""
			if err := app.persistWorkspaceDelete(ctx, operation, next, false, true); err != nil {
				return operation, err
			}
			operation = next
		case "complete":
			if operation.Status != "succeeded" || operation.RuntimeStatus != "absent" || operation.SecretStatus != "absent" || operation.KeyStatus != "absent" ||
				operation.RefundReceiptID == "" || !workspaceDeleteRefundConfirmationMatches(operation) {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "workspace_delete_terminal_evidence_invalid")
			}
			_, found, err := app.tables.GetWorkspace(ctx, operation.WorkspaceID)
			if err != nil || found {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "workspace_absence_unconfirmed")
			}
			return operation, nil
		default:
			return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "workspace_delete_phase_invalid")
		}
	}
}

func (app *controlPlaneServer) persistWorkspaceDelete(ctx context.Context, current, next workspaceDeleteOperation, deleteWorkspace, requireAbsent bool) error {
	return app.tables.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{
		DeleteWorkspace: deleteWorkspace, RequireWorkspaceAbsent: requireAbsent,
		ExpectedResult: stringValue(workspaceDeleteOperationRow(current)["result"]), DesiredOperation: workspaceDeleteOperationRow(next),
	})
}

func (app *controlPlaneServer) markWorkspaceDeleteUnconfirmed(ctx context.Context, operation workspaceDeleteOperation, code string) (workspaceDeleteOperation, error) {
	next := operation
	next.Status, next.LastErrorCode = "manual_review", code
	requireAbsent := operation.Phase == "workspace_deleted" || operation.Phase == "complete"
	if err := app.persistWorkspaceDelete(ctx, operation, next, false, requireAbsent); err != nil {
		return operation, err
	}
	return next, errWorkspaceDeleteUnconfirmed
}

func observeWorkspaceDeleteRuntimeAndSecret(ctx context.Context, service *controlplane.Service, operation workspaceDeleteOperation) (clients.WorkspaceRuntimeObservation, clients.WorkspaceRuntimeGatewaySecretObservation, error) {
	runtime, err := service.ObserveWorkspaceDeleteRuntime(ctx, operation.WorkspaceID)
	if err != nil {
		return clients.WorkspaceRuntimeObservation{}, clients.WorkspaceRuntimeGatewaySecretObservation{}, err
	}
	secret, err := service.ObserveWorkspaceDeleteRuntimeGatewaySecret(ctx, operation.WorkspaceID)
	return runtime, secret, err
}

func workspaceDeleteRuntimeAndSecretAbsent(runtime clients.WorkspaceRuntimeObservation, secret clients.WorkspaceRuntimeGatewaySecretObservation) bool {
	return runtime.SchemaVersion == clients.WorkspaceOwnerObservationSchemaVersion && secret.SchemaVersion == clients.WorkspaceOwnerObservationSchemaVersion &&
		runtime.State == clients.WorkspaceOwnerObservationAbsent && secret.State == clients.WorkspaceOwnerObservationAbsent && runtime.Runtime == nil && secret.Binding == nil
}

func workspaceDeleteRuntimeAndSecretReady(operation workspaceDeleteOperation, runtime clients.WorkspaceRuntimeObservation, secret clients.WorkspaceRuntimeGatewaySecretObservation) bool {
	return runtime.SchemaVersion == clients.WorkspaceOwnerObservationSchemaVersion && runtime.State == clients.WorkspaceOwnerObservationReady && runtime.Runtime != nil &&
		runtime.WorkspaceID == operation.WorkspaceID && runtime.Runtime.WorkspaceID == operation.WorkspaceID && runtime.Runtime.ID == operation.RuntimeID &&
		secret.SchemaVersion == clients.WorkspaceOwnerObservationSchemaVersion && secret.State == clients.WorkspaceOwnerObservationReady && secret.Binding != nil &&
		secret.WorkspaceID == operation.WorkspaceID && secret.Binding.WorkspaceID == operation.WorkspaceID && secret.Binding.WorkspaceAPIKeyID == operation.WorkspaceAPIKeyID &&
		secret.Binding.SecretRef == operation.GatewaySecretRef && secret.Binding.Fingerprint == operation.GatewayFingerprint && secret.Binding.Bound
}

func workspaceDeleteKeyMatches(operation workspaceDeleteOperation, key clients.Sub2APIWorkspaceKey) bool {
	return key.ID == operation.WorkspaceAPIKeyID && key.UserID == operation.Sub2APIUserID && key.Name == workspaceReservedKeyName(operation.WorkspaceID) && key.Status == "active"
}

func workspaceDeleteRefundEntryMatches(operation workspaceDeleteOperation, entry clients.Sub2APIBalanceHistoryEntry) bool {
	return entry.Code == operation.RefundCode && entry.Type == "balance" && entry.Status == "used" && entry.UsedBy != nil && *entry.UsedBy == operation.Sub2APIUserID &&
		entry.UsedAt != nil && !entry.UsedAt.IsZero() && entry.ValueUSDMicros == operation.TotalUSDMicros
}

func workspaceDeleteRefundHistory(operation workspaceDeleteOperation, history map[string]clients.Sub2APIBalanceHistoryEntry) (clients.Sub2APIBalanceHistoryEntry, bool, bool) {
	if len(history) == 0 {
		return clients.Sub2APIBalanceHistoryEntry{}, false, true
	}
	entry, found := history[operation.RefundCode]
	return entry, found, len(history) == 1 && found && workspaceDeleteRefundEntryMatches(operation, entry)
}

func workspaceDeleteRefundConfirmationMatches(operation workspaceDeleteOperation) bool {
	return stringValue(operation.RefundConfirmation["code"]) == operation.RefundCode &&
		int64(numberField(operation.RefundConfirmation, "userId", 0)) == operation.Sub2APIUserID &&
		int64(numberField(operation.RefundConfirmation, "refundUsdMicros", 0)) == operation.TotalUSDMicros &&
		stringValue(operation.RefundConfirmation["status"]) == "used"
}

func (app *controlPlaneServer) refundWorkspaceDelete(ctx context.Context, service *controlplane.Service, credential clients.SessionDelegatedCredential, operation workspaceDeleteOperation) (workspaceDeleteOperation, error) {
	unlockWallet := app.lockResource("sub2api-wallet", operation.AccountID)
	defer unlockWallet()
	runtime, secret, err := observeWorkspaceDeleteRuntimeAndSecret(ctx, service, operation)
	if err != nil || !workspaceDeleteRuntimeAndSecretAbsent(runtime, secret) {
		return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_absence_drift")
	}
	if _, err := service.GatewayUserKey(ctx, credential, operation.Sub2APIUserID, operation.WorkspaceAPIKeyID); !errors.Is(err, clients.ErrSub2APIKeyNotFound) {
		return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "sub2api_key_absence_drift")
	}
	history, err := service.FinancialBalanceHistoryByCodes(ctx, operation.Sub2APIUserID, []string{operation.RefundCode})
	if err != nil {
		return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "sub2api_refund_history_unavailable")
	}
	if _, found, valid := workspaceDeleteRefundHistory(operation, history); !valid {
		return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "sub2api_refund_identity_conflict")
	} else if found {
		return app.confirmWorkspaceDeleteRefund(ctx, operation)
	}
	if operation.RefundAttempted {
		return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "sub2api_refund_unconfirmed")
	}
	attempted := operation
	attempted.Status, attempted.RefundAttempted, attempted.LastErrorCode = "running", true, ""
	if err := app.persistWorkspaceDelete(ctx, operation, attempted, false, false); err != nil {
		return operation, err
	}
	operation = attempted
	history, err = service.FinancialBalanceHistoryByCodes(ctx, operation.Sub2APIUserID, []string{operation.RefundCode})
	if err != nil {
		return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "sub2api_refund_history_unavailable")
	}
	if _, found, valid := workspaceDeleteRefundHistory(operation, history); !valid {
		return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "sub2api_refund_identity_conflict")
	} else if found {
		return app.confirmWorkspaceDeleteRefund(ctx, operation)
	}
	refund, refundErr := service.RefundSub2API(ctx, clients.Sub2APIRefundInput{
		UserID: operation.Sub2APIUserID, Code: operation.RefundCode, RefundUSDMicros: operation.TotalUSDMicros,
		Notes: "OPL Workspace delete refund " + operation.WorkspaceID,
	})
	responseMatches := refund.Code == operation.RefundCode && refund.UserID == operation.Sub2APIUserID && refund.RefundUSDMicros == operation.TotalUSDMicros && refund.Status == "used"
	history, err = service.FinancialBalanceHistoryByCodes(ctx, operation.Sub2APIUserID, []string{operation.RefundCode})
	_, found, valid := workspaceDeleteRefundHistory(operation, history)
	if err != nil || !found || !valid {
		if refundErr == nil && !responseMatches {
			return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "sub2api_refund_response_conflict")
		}
		return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "sub2api_refund_unconfirmed")
	}
	return app.confirmWorkspaceDeleteRefund(ctx, operation)
}

func (app *controlPlaneServer) confirmWorkspaceDeleteRefund(ctx context.Context, operation workspaceDeleteOperation) (workspaceDeleteOperation, error) {
	next := operation
	next.Phase, next.Status, next.LastErrorCode = "refund_confirmed", "running", ""
	next.RefundConfirmation = map[string]any{
		"code": operation.RefundCode, "userId": operation.Sub2APIUserID, "refundUsdMicros": operation.TotalUSDMicros, "status": "used",
	}
	if err := app.persistWorkspaceDelete(ctx, operation, next, false, false); err != nil {
		return operation, err
	}
	return next, nil
}

func workspaceDeleteRefundReceiptInput(operation workspaceDeleteOperation) clients.ReceiptInput {
	cost := cloneMap(operation.PurchaseReceipt.Cost)
	cost["sub2apiUserId"], cost["sub2apiRedeemCode"] = operation.Sub2APIUserID, operation.DebitCode
	cost["sub2apiRefundCode"], cost["refundUsdMicros"] = operation.RefundCode, operation.TotalUSDMicros
	execution := cloneMap(operation.PurchaseReceipt.Execution)
	execution["resourceType"], execution["resourceId"], execution["reason"] = "workspace", operation.WorkspaceID, "workspace_deleted"
	execution["runtimeId"], execution["workspaceApiKeyId"] = operation.RuntimeID, operation.WorkspaceAPIKeyID
	execution["debitCode"], execution["purchaseReceiptId"] = operation.DebitCode, operation.PurchaseReceiptID
	execution["refundConfirmation"] = cloneMap(operation.RefundConfirmation)
	return clients.ReceiptInput{
		Type: "billing.workspace_refunded.v1", Status: "completed", Surface: "control_plane", AccountID: operation.AccountID,
		WorkspaceID: operation.WorkspaceID, RequestID: operation.OperationID, Execution: execution, Cost: cost,
		Owner:               map[string]any{"accountId": operation.AccountID, "workspaceId": operation.WorkspaceID, "ownerUserId": operation.OwnerUserID},
		SupersedesReceiptID: operation.PurchaseReceiptID,
	}
}

func (app *controlPlaneServer) recordWorkspaceDeleteRefundReceipt(ctx context.Context, service *controlplane.Service, operation workspaceDeleteOperation) (workspaceDeleteOperation, error) {
	if !workspaceDeleteRefundConfirmationMatches(operation) {
		return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "sub2api_refund_evidence_invalid")
	}
	input := workspaceDeleteRefundReceiptInput(operation)
	receipt, err := service.RecordMonthlyReceipt(ctx, input, operation.OperationID+":refund-receipt")
	if err != nil || receipt.ReceiptID == "" || !workspaceLaunchReceiptInputMatches(receipt.ReceiptInput, input) {
		return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "ledger_refund_receipt_unconfirmed")
	}
	next := operation
	next.Phase, next.Status, next.RefundReceiptID, next.LastErrorCode = "refund_receipt_recorded", "running", receipt.ReceiptID, ""
	if err := app.persistWorkspaceDelete(ctx, operation, next, false, false); err != nil {
		return operation, err
	}
	return next, nil
}

func workspaceDeleteAttachmentMatches(operation workspaceDeleteOperation, attachment clients.StorageAttachment) bool {
	return attachment.ID == operation.AttachmentID && attachment.WorkspaceID == operation.WorkspaceID && attachment.ComputeID == operation.ComputeID &&
		attachment.VolumeID == operation.StorageID && attachment.Status == "detached"
}

func workspaceDeleteStorageMatches(operation workspaceDeleteOperation, storage clients.StorageVolume) bool {
	if storage.ID != operation.StorageID || storage.WorkspaceID != operation.WorkspaceID {
		return false
	}
	switch storage.Status {
	case "destroyed", "external_deleted":
		return true
	default:
		return false
	}
}

func workspaceDeleteComputeIdentityMatches(operation workspaceDeleteOperation, compute clients.ComputeAllocation) bool {
	return compute.ID == operation.ComputeID && compute.WorkspaceID == operation.WorkspaceID
}

func workspaceDeleteComputeStartMatches(operation workspaceDeleteOperation, compute clients.ComputeAllocation) bool {
	return workspaceDeleteComputeIdentityMatches(operation, compute) && (compute.Status == "destroying" || workspaceDeleteComputeTerminal(compute.Status))
}

func workspaceDeleteComputeTerminal(status string) bool {
	switch status {
	case "destroyed", "external_deleted", "deleted", "missing":
		return true
	default:
		return false
	}
}

func awaitWorkspaceDeleteComputeReadback(ctx context.Context, service *controlplane.Service, operation workspaceDeleteOperation) (clients.ComputeAllocation, error) {
	const attempts = 8
	for attempt := 0; attempt < attempts; attempt++ {
		compute, err := service.WorkspaceDeleteComputeStatus(ctx, operation.ComputeID)
		if err != nil || !workspaceDeleteComputeIdentityMatches(operation, compute) {
			return compute, errWorkspaceDeleteUnconfirmed
		}
		if workspaceDeleteComputeTerminal(compute.Status) {
			return compute, nil
		}
		if compute.Status != "destroying" || attempt == attempts-1 {
			return compute, errWorkspaceDeleteUnconfirmed
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return clients.ComputeAllocation{}, ctx.Err()
		case <-timer.C:
		}
	}
	return clients.ComputeAllocation{}, errWorkspaceDeleteUnconfirmed
}

func workspaceDeleteResponse(operation workspaceDeleteOperation, errorCode string) map[string]any {
	status := "deleted"
	if errorCode != "" {
		status = "manual_review"
	}
	response := map[string]any{"workspaceId": operation.WorkspaceID, "operationId": operation.OperationID, "status": status}
	if errorCode != "" {
		response["error"] = errorCode
	}
	return response
}
