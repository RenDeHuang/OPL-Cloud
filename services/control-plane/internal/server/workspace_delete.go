package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
	OperationID      string `json:"operationId"`
	RequestHash      string `json:"requestHash"`
	AccountID        string `json:"accountId"`
	OwnerUserID      string `json:"ownerUserId"`
	WorkspaceID      string `json:"workspaceId"`
	ComputeID        string `json:"computeId"`
	StorageID        string `json:"storageId"`
	AttachmentID     string `json:"attachmentId"`
	Phase            string `json:"phase"`
	Status           string `json:"status"`
	RuntimeStatus    string `json:"runtimeStatus,omitempty"`
	AttachmentStatus string `json:"attachmentStatus,omitempty"`
	StorageStatus    string `json:"storageStatus,omitempty"`
	ComputeStatus    string `json:"computeStatus,omitempty"`
	LastErrorCode    string `json:"lastErrorCode,omitempty"`
	CreatedAt        string `json:"createdAt"`
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
	user, ok := app.sessionUserContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "not_authenticated")
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
		operation = newWorkspaceDeleteOperation(workspace, time.Now().UTC())
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

	requestHash := workspaceDeleteRequestHash(operation.AccountID, operation.OwnerUserID, workspaceID)
	if operation.AccountID != stringValue(user["accountId"]) || operation.OwnerUserID != stringValue(user["id"]) {
		writeError(w, http.StatusForbidden, "workspace_owner_required")
		return
	}
	if operation.RequestHash != requestHash {
		writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
		return
	}

	operation, err = app.runWorkspaceDelete(r.Context(), service, operation)
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

func newWorkspaceDeleteOperation(workspace map[string]any, now time.Time) workspaceDeleteOperation {
	workspaceID := stringValue(workspace["id"])
	accountID := firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"]))
	ownerUserID := firstNonEmpty(stringValue(workspace["ownerUserId"]), stringValue(workspace["ownerId"]))
	return workspaceDeleteOperation{
		OperationID: workspaceDeleteOperationID(workspaceID),
		RequestHash: workspaceDeleteRequestHash(accountID, ownerUserID, workspaceID),
		AccountID:   accountID, OwnerUserID: ownerUserID, WorkspaceID: workspaceID,
		ComputeID:    firstNonEmpty(stringValue(workspace["currentComputeAllocationId"]), stringValue(workspace["computeAllocationId"])),
		StorageID:    stringValue(workspace["storageId"]),
		AttachmentID: firstNonEmpty(stringValue(workspace["currentAttachmentId"]), stringValue(workspace["attachmentId"])),
		Phase:        "claimed", Status: "running", CreatedAt: now.Format(time.RFC3339Nano),
	}
}

func workspaceDeleteOperationID(workspaceID string) string {
	return "workspace-delete-" + stableID(workspaceDeleteAction, workspaceID)[:18]
}

func workspaceDeleteRequestHash(accountID, ownerUserID, workspaceID string) string {
	return stableID(workspaceDeleteAction, accountID, ownerUserID, workspaceID)
}

func workspaceDeleteStageKey(operation workspaceDeleteOperation, stage string) string {
	return operation.OperationID + ":" + stage
}

func workspaceDeleteOperationRow(operation workspaceDeleteOperation) map[string]any {
	encoded, _ := json.Marshal(operation)
	return map[string]any{
		"id": operation.OperationID, "operationId": operation.OperationID,
		"accountId": operation.AccountID, "workspaceId": operation.WorkspaceID,
		"resourceId": operation.WorkspaceID, "resourceKind": "workspace", "action": workspaceDeleteAction,
		"status": operation.Status, "result": string(encoded),
		"computeAllocationId": operation.ComputeID, "storageId": operation.StorageID, "attachmentId": operation.AttachmentID,
		"createdAt": operation.CreatedAt,
	}
}

func decodeWorkspaceDeleteOperation(row map[string]any) (workspaceDeleteOperation, error) {
	var operation workspaceDeleteOperation
	if stringValue(row["action"]) != workspaceDeleteAction || json.Unmarshal([]byte(stringValue(row["result"])), &operation) != nil ||
		operation.OperationID == "" || operation.OperationID != stringValue(row["id"]) || operation.OperationID != stringValue(row["operationId"]) ||
		operation.AccountID == "" || operation.AccountID != stringValue(row["accountId"]) || operation.OwnerUserID == "" ||
		operation.WorkspaceID == "" || operation.WorkspaceID != stringValue(row["workspaceId"]) || operation.WorkspaceID != stringValue(row["resourceId"]) ||
		stringValue(row["resourceKind"]) != "workspace" || operation.Status != stringValue(row["status"]) || operation.RequestHash == "" || operation.CreatedAt == "" {
		return workspaceDeleteOperation{}, errors.New("workspace_delete_operation_invalid")
	}
	return operation, nil
}

func workspaceDeleteOperationIdentityMatches(row map[string]any, desired workspaceDeleteOperation) bool {
	current, err := decodeWorkspaceDeleteOperation(row)
	return err == nil && current.OperationID == desired.OperationID && current.RequestHash == desired.RequestHash &&
		current.AccountID == desired.AccountID && current.OwnerUserID == desired.OwnerUserID && current.WorkspaceID == desired.WorkspaceID &&
		current.ComputeID == desired.ComputeID && current.StorageID == desired.StorageID && current.AttachmentID == desired.AttachmentID &&
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

func (app *controlPlaneServer) runWorkspaceDelete(ctx context.Context, service *controlplane.Service, operation workspaceDeleteOperation) (workspaceDeleteOperation, error) {
	for {
		if operation.ComputeID == "" || operation.StorageID == "" || operation.AttachmentID == "" {
			return app.markWorkspaceDeleteUnconfirmed(ctx, operation)
		}
		switch operation.Phase {
		case "claimed":
			runtime, err := service.DestroyWorkspaceRuntime(ctx, operation.AccountID, operation.WorkspaceID, workspaceDeleteStageKey(operation, "runtime"))
			if err != nil || runtime.WorkspaceID != operation.WorkspaceID || runtime.Status != "destroyed" {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation)
			}
			next := operation
			next.Phase, next.Status, next.RuntimeStatus, next.LastErrorCode = "runtime_destroyed", "running", runtime.Status, ""
			if err := app.persistWorkspaceDelete(ctx, operation, next, false, false); err != nil {
				return operation, err
			}
			operation = next
		case "runtime_destroyed":
			attachment, err := service.DetachWorkspaceStorage(ctx, operation.AccountID, operation.WorkspaceID, operation.AttachmentID, workspaceDeleteStageKey(operation, "attachment"))
			if err != nil || !workspaceDeleteAttachmentMatches(operation, attachment) {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation)
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
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation)
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
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation)
			}
			compute, err = awaitWorkspaceDeleteComputeReadback(ctx, service, operation)
			if err != nil {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation)
			}
			next := operation
			next.Phase, next.Status, next.ComputeStatus, next.LastErrorCode = "compute_destroyed", "running", compute.Status, ""
			if err := app.persistWorkspaceDelete(ctx, operation, next, false, false); err != nil {
				return operation, err
			}
			operation = next
		case "compute_destroyed":
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
			if operation.Status != "succeeded" {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation)
			}
			_, found, err := app.tables.GetWorkspace(ctx, operation.WorkspaceID)
			if err != nil || found {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation)
			}
			return operation, nil
		default:
			return app.markWorkspaceDeleteUnconfirmed(ctx, operation)
		}
	}
}

func (app *controlPlaneServer) persistWorkspaceDelete(ctx context.Context, current, next workspaceDeleteOperation, deleteWorkspace, requireAbsent bool) error {
	return app.tables.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{
		DeleteWorkspace: deleteWorkspace, RequireWorkspaceAbsent: requireAbsent,
		ExpectedResult: stringValue(workspaceDeleteOperationRow(current)["result"]), DesiredOperation: workspaceDeleteOperationRow(next),
	})
}

func (app *controlPlaneServer) markWorkspaceDeleteUnconfirmed(ctx context.Context, operation workspaceDeleteOperation) (workspaceDeleteOperation, error) {
	next := operation
	next.Status, next.LastErrorCode = "manual_review", "fabric_cleanup_unconfirmed"
	requireAbsent := operation.Phase == "workspace_deleted" || operation.Phase == "complete"
	if err := app.persistWorkspaceDelete(ctx, operation, next, false, requireAbsent); err != nil {
		return operation, err
	}
	return next, errWorkspaceDeleteUnconfirmed
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
