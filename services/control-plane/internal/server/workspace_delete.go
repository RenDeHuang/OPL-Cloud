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

const (
	workspaceDeleteAction                  = "workspace.delete.v2"
	workspaceDeleteLegacyAction            = "workspace.delete.v1"
	workspaceDeleteReplayLease             = 30 * time.Second
	workspaceDeleteComputeReadbackBudget   = 8
	workspaceDeleteComputeReadbackInterval = time.Second
)

var (
	errWorkspaceDeleteCASConflict        = errors.New("workspace_delete_cas_conflict")
	errWorkspaceDeleteUnconfirmed        = errors.New("workspace_delete_unconfirmed")
	errWorkspaceDeletePending            = errors.New("workspace_delete_pending")
	errWorkspaceDeleteHistoricalConflict = errors.New("workspace_delete_historical_manual_review")
	errWorkspaceDeleteTerminalConflict   = errors.New("workspace_delete_terminal_conflict")
	errWorkspaceDeleteStateRead          = errors.New("workspace_delete_state_read_failed")
)

type workspaceDeleteOperation struct {
	SchemaVersion            int                                `json:"schemaVersion"`
	OperationID              string                             `json:"operationId"`
	RequestHash              string                             `json:"requestHash"`
	AccountID                string                             `json:"accountId"`
	OwnerUserID              string                             `json:"ownerUserId"`
	Sub2APIUserID            int64                              `json:"sub2apiUserId"`
	WorkspaceID              string                             `json:"workspaceId"`
	ResourceType             string                             `json:"resourceType"`
	ResourceID               string                             `json:"resourceId"`
	LaunchOperationID        string                             `json:"launchOperationId"`
	LaunchReceiptID          string                             `json:"launchReceiptId"`
	RuntimeID                string                             `json:"runtimeId"`
	RuntimeServiceName       string                             `json:"runtimeServiceName"`
	ComputeID                string                             `json:"computeId"`
	StorageID                string                             `json:"storageId"`
	AttachmentID             string                             `json:"attachmentId"`
	WorkspaceAPIKeyID        int64                              `json:"workspaceApiKeyId"`
	GatewaySecretRef         string                             `json:"gatewaySecretRef"`
	GatewayFingerprint       string                             `json:"gatewayFingerprint"`
	DeletionReceiptID        string                             `json:"deletionReceiptId,omitempty"`
	Phase                    string                             `json:"phase"`
	Status                   string                             `json:"status"`
	RuntimeStatus            string                             `json:"runtimeStatus,omitempty"`
	SecretStatus             string                             `json:"secretStatus,omitempty"`
	AttachmentStatus         string                             `json:"attachmentStatus,omitempty"`
	StorageStatus            string                             `json:"storageStatus,omitempty"`
	ComputeStatus            string                             `json:"computeStatus,omitempty"`
	ComputeReadbacks         int                                `json:"computeReadbacks,omitempty"`
	MaxComputeReadbacks      int                                `json:"maxComputeReadbacks,omitempty"`
	ComputeReadbackNotBefore string                             `json:"computeReadbackNotBefore,omitempty"`
	KeyStatus                string                             `json:"keyStatus,omitempty"`
	KeyDeleteAttempted       bool                               `json:"keyDeleteAttempted,omitempty"`
	KeyDeleteReplay          workspaceDeleteReplayAuthorization `json:"keyDeleteReplay,omitempty"`
	LastErrorCode            string                             `json:"lastErrorCode,omitempty"`
	CreatedAt                string                             `json:"createdAt"`
}

type workspaceDeleteLegacyOperation struct {
	OperationID              string                             `json:"operationId"`
	RequestHash              string                             `json:"requestHash"`
	AccountID                string                             `json:"accountId"`
	OwnerUserID              string                             `json:"ownerUserId"`
	Sub2APIUserID            int64                              `json:"sub2apiUserId"`
	WorkspaceID              string                             `json:"workspaceId"`
	LaunchOperationID        string                             `json:"launchOperationId"`
	RuntimeID                string                             `json:"runtimeId"`
	ComputeID                string                             `json:"computeId"`
	StorageID                string                             `json:"storageId"`
	AttachmentID             string                             `json:"attachmentId"`
	WorkspaceAPIKeyID        int64                              `json:"workspaceApiKeyId"`
	GatewaySecretRef         string                             `json:"gatewaySecretRef"`
	GatewayFingerprint       string                             `json:"gatewayFingerprint"`
	DebitCode                string                             `json:"debitCode"`
	PurchaseReceiptID        string                             `json:"purchaseReceiptId"`
	PurchaseReceipt          clients.ReceiptInput               `json:"purchaseReceipt"`
	RefundCode               string                             `json:"refundCode"`
	RefundReceiptID          string                             `json:"refundReceiptId,omitempty"`
	TotalUSDMicros           int64                              `json:"totalUsdMicros"`
	Phase                    string                             `json:"phase"`
	Status                   string                             `json:"status"`
	RuntimeStatus            string                             `json:"runtimeStatus,omitempty"`
	SecretStatus             string                             `json:"secretStatus,omitempty"`
	AttachmentStatus         string                             `json:"attachmentStatus,omitempty"`
	StorageStatus            string                             `json:"storageStatus,omitempty"`
	ComputeStatus            string                             `json:"computeStatus,omitempty"`
	ComputeReadbacks         int                                `json:"computeReadbacks,omitempty"`
	MaxComputeReadbacks      int                                `json:"maxComputeReadbacks,omitempty"`
	ComputeReadbackNotBefore string                             `json:"computeReadbackNotBefore,omitempty"`
	KeyStatus                string                             `json:"keyStatus,omitempty"`
	KeyDeleteAttempted       bool                               `json:"keyDeleteAttempted,omitempty"`
	RefundAttempted          bool                               `json:"refundAttempted,omitempty"`
	KeyDeleteReplay          workspaceDeleteReplayAuthorization `json:"keyDeleteReplay,omitempty"`
	RefundReplay             workspaceDeleteReplayAuthorization `json:"refundReplay,omitempty"`
	RefundConfirmation       map[string]any                     `json:"refundConfirmation,omitempty"`
	LastErrorCode            string                             `json:"lastErrorCode,omitempty"`
	CreatedAt                string                             `json:"createdAt"`
}

type workspaceDeleteReplayAuthorization struct {
	SchemaVersion     int    `json:"schemaVersion,omitempty"`
	AuthorizationID   string `json:"authorizationId,omitempty"`
	IdempotencyKey    string `json:"idempotencyKey,omitempty"`
	State             string `json:"state,omitempty"`
	LeaseGeneration   int    `json:"leaseGeneration,omitempty"`
	LeaseExpiresAt    string `json:"leaseExpiresAt,omitempty"`
	DispatchStartedAt string `json:"dispatchStartedAt,omitempty"`
	ConsumedAt        string `json:"consumedAt,omitempty"`
}

type workspaceDeleteStoreMutation struct {
	Create                 bool
	DeleteWorkspace        bool
	RequireWorkspaceAbsent bool
	ExpectedResult         string
	DesiredOperation       map[string]any
}

type workspaceDeleteGatewayIdentity struct {
	WorkspaceAPIKeyID  int64
	GatewaySecretRef   string
	GatewayFingerprint string
}

func (app *controlPlaneServer) deleteWorkspace(w http.ResponseWriter, r *http.Request, service *controlplane.Service) {
	authorizationID, ok := requiredMutationKey(w, r)
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
		legacy, legacyFound, legacyErr := app.workspaceDeleteLegacyOperation(r.Context(), workspaceID)
		if legacyErr != nil {
			writeJSON(w, http.StatusConflict, workspaceDeleteHistoricalConflictResponse(workspaceID, workspaceDeleteLegacyOperationID(workspaceID)))
			return
		}
		if legacyFound {
			if legacy.AccountID != stringValue(user["accountId"]) || legacy.OwnerUserID != stringValue(user["id"]) {
				writeError(w, http.StatusForbidden, "workspace_owner_required")
				return
			}
			if legacy.Sub2APIUserID != sub2APIUserID || !validWorkspaceDeleteLegacyTerminal(legacy) {
				writeJSON(w, http.StatusConflict, workspaceDeleteHistoricalConflictResponse(workspaceID, legacy.OperationID))
				return
			}
			if _, workspaceFound, readErr := app.tables.GetWorkspace(r.Context(), workspaceID); readErr != nil {
				writeError(w, http.StatusInternalServerError, "state_read_failed")
				return
			} else if workspaceFound {
				writeJSON(w, http.StatusConflict, workspaceDeleteHistoricalConflictResponse(workspaceID, legacy.OperationID))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"workspaceId": workspaceID, "operationId": legacy.OperationID, "status": "deleted", "historical": true})
			return
		}

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
			if errors.Is(err, errWorkspaceKeyRotationInProgress) {
				writeError(w, http.StatusConflict, errWorkspaceKeyRotationInProgress.Error())
				return
			}
			writeError(w, http.StatusBadGateway, "workspace_delete_identity_unconfirmed")
			return
		}
		if err := app.tables.ApplyWorkspaceDelete(r.Context(), workspaceDeleteStoreMutation{Create: true, DesiredOperation: workspaceDeleteOperationRow(operation)}); errors.Is(err, errWorkspaceDeleteCASConflict) {
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

	if operation.AccountID != stringValue(user["accountId"]) || operation.OwnerUserID != stringValue(user["id"]) {
		writeError(w, http.StatusForbidden, "workspace_owner_required")
		return
	}
	if operation.RequestHash != workspaceDeleteRequestHash(operation) || operation.Sub2APIUserID != sub2APIUserID {
		writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
		return
	}
	operation, err = app.runWorkspaceDelete(r.Context(), service, credential, authorizationID, operation)
	if err != nil {
		if errors.Is(err, errWorkspaceDeleteTerminalConflict) {
			if auditErr := app.saveWorkspaceDeleteTerminalConflictAudit(r, operation); auditErr != nil {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
			writeJSON(w, http.StatusConflict, workspaceDeleteTerminalConflictResponse(operation))
			return
		}
		if errors.Is(err, errWorkspaceDeleteStateRead) {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		if errors.Is(err, errWorkspaceDeletePending) {
			if retryAfter := workspaceDeleteComputeRetryAfter(operation, time.Now().UTC()); retryAfter > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			}
			writeJSON(w, http.StatusAccepted, workspaceDeletePendingResponse(operation))
			return
		}
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

func (app *controlPlaneServer) workspaceDeleteLegacyOperation(ctx context.Context, workspaceID string) (workspaceDeleteLegacyOperation, bool, error) {
	row, found, err := app.tables.GetRuntimeOperation(ctx, workspaceDeleteLegacyOperationID(workspaceID))
	if err != nil || !found {
		return workspaceDeleteLegacyOperation{}, found, err
	}
	operation, err := decodeWorkspaceDeleteLegacyOperation(row)
	return operation, err == nil, err
}

func decodeWorkspaceDeleteLegacyOperation(row map[string]any) (workspaceDeleteLegacyOperation, error) {
	var operation workspaceDeleteLegacyOperation
	if stringValue(row["action"]) != workspaceDeleteLegacyAction || json.Unmarshal([]byte(stringValue(row["result"])), &operation) != nil ||
		operation.OperationID == "" || operation.OperationID != workspaceDeleteLegacyOperationID(operation.WorkspaceID) || operation.OperationID != stringValue(row["id"]) ||
		operation.OperationID != stringValue(row["operationId"]) || operation.AccountID == "" || operation.AccountID != stringValue(row["accountId"]) ||
		operation.OwnerUserID == "" || operation.Sub2APIUserID <= 0 || operation.WorkspaceID == "" || operation.WorkspaceID != stringValue(row["workspaceId"]) ||
		operation.WorkspaceID != stringValue(row["resourceId"]) || stringValue(row["resourceKind"]) != "workspace" || operation.Status != stringValue(row["status"]) ||
		operation.ComputeID != stringValue(row["computeAllocationId"]) || operation.StorageID != stringValue(row["storageId"]) ||
		operation.AttachmentID != stringValue(row["attachmentId"]) || operation.RuntimeID != stringValue(row["runtimeId"]) ||
		operation.CreatedAt == "" || operation.CreatedAt != stringValue(row["createdAt"]) || !validWorkspaceDeleteLegacyIdentity(operation) ||
		operation.RequestHash == "" || operation.RequestHash != workspaceDeleteLegacyRequestHash(operation) {
		return workspaceDeleteLegacyOperation{}, errors.New("workspace_delete_legacy_operation_invalid")
	}
	return operation, nil
}

func workspaceDeleteLegacyRequestHash(operation workspaceDeleteLegacyOperation) string {
	purchaseReceipt, _ := json.Marshal(operation.PurchaseReceipt)
	return stableID(workspaceDeleteLegacyAction, operation.AccountID, operation.OwnerUserID, operation.WorkspaceID, operation.LaunchOperationID,
		operation.RuntimeID, operation.ComputeID, operation.StorageID, operation.AttachmentID, operation.GatewaySecretRef, operation.GatewayFingerprint,
		operation.DebitCode, operation.PurchaseReceiptID, string(purchaseReceipt), operation.RefundCode,
		strconv.FormatInt(operation.Sub2APIUserID, 10), strconv.FormatInt(operation.WorkspaceAPIKeyID, 10), strconv.FormatInt(operation.TotalUSDMicros, 10))
}

func validWorkspaceDeleteLegacyIdentity(operation workspaceDeleteLegacyOperation) bool {
	if operation.OperationID == "" || operation.OperationID != workspaceDeleteLegacyOperationID(operation.WorkspaceID) || operation.AccountID == "" || operation.OwnerUserID == "" ||
		operation.Sub2APIUserID <= 0 || operation.WorkspaceID == "" || operation.LaunchOperationID == "" || operation.RuntimeID == "" || operation.ComputeID == "" ||
		operation.StorageID == "" || operation.AttachmentID == "" || operation.WorkspaceAPIKeyID <= 0 || operation.GatewaySecretRef == "" || operation.GatewayFingerprint == "" ||
		operation.DebitCode == "" || operation.PurchaseReceiptID == "" || operation.RefundCode == "" || operation.RefundCode == operation.DebitCode || operation.TotalUSDMicros <= 0 ||
		operation.PurchaseReceipt.Type != "billing.workspace_purchased.v1" || operation.PurchaseReceipt.Status != "completed" ||
		operation.PurchaseReceipt.AccountID != operation.AccountID || operation.PurchaseReceipt.WorkspaceID != operation.WorkspaceID ||
		operation.PurchaseReceipt.RequestID != operation.LaunchOperationID || operation.CreatedAt == "" ||
		!validWorkspaceDeleteReplayAuthorization(operation.KeyDeleteReplay, operation.OperationID+":key") ||
		!validWorkspaceDeleteReplayAuthorization(operation.RefundReplay, operation.RefundCode) {
		return false
	}
	cost, execution := operation.PurchaseReceipt.Cost, operation.PurchaseReceipt.Execution
	return int64(numberField(cost, "sub2apiUserId", 0)) == operation.Sub2APIUserID && stringValue(cost["sub2apiRedeemCode"]) == operation.DebitCode &&
		int64(numberField(cost, "totalUsdMicros", 0)) == operation.TotalUSDMicros && stringValue(cost["resourceId"]) == operation.WorkspaceID &&
		stringValue(execution["runtimeId"]) == operation.RuntimeID && stringValue(execution["computeAllocationId"]) == operation.ComputeID &&
		stringValue(execution["storageId"]) == operation.StorageID && stringValue(execution["attachmentId"]) == operation.AttachmentID &&
		int64(numberField(execution, "workspaceApiKeyId", 0)) == operation.WorkspaceAPIKeyID
}

func validWorkspaceDeleteLegacyTerminal(operation workspaceDeleteLegacyOperation) bool {
	if operation.Phase != "complete" || operation.Status != "succeeded" || operation.RuntimeStatus != "absent" || operation.SecretStatus != "absent" ||
		operation.AttachmentStatus != "detached" || operation.KeyStatus != "absent" || operation.RefundReceiptID == "" || operation.LastErrorCode != "" ||
		!workspaceDeleteLegacyComputeTerminal(operation.ComputeStatus) {
		return false
	}
	if operation.StorageStatus != "destroyed" && operation.StorageStatus != "external_deleted" {
		return false
	}
	return stringValue(operation.RefundConfirmation["code"]) == operation.RefundCode &&
		int64(numberField(operation.RefundConfirmation, "userId", 0)) == operation.Sub2APIUserID &&
		int64(numberField(operation.RefundConfirmation, "refundUsdMicros", 0)) == operation.TotalUSDMicros &&
		stringValue(operation.RefundConfirmation["status"]) == "used"
}

func workspaceDeleteLegacyComputeTerminal(status string) bool {
	switch status {
	case "destroyed", "external_deleted", "deleted", "missing":
		return true
	default:
		return false
	}
}

func workspaceDeleteHistoricalConflictResponse(workspaceID, operationID string) map[string]any {
	return map[string]any{"workspaceId": workspaceID, "operationId": operationID, "status": "manual_review", "error": errWorkspaceDeleteHistoricalConflict.Error()}
}

func workspaceDeleteTerminalConflictResponse(operation workspaceDeleteOperation) map[string]any {
	return map[string]any{
		"workspaceId": operation.WorkspaceID, "operationId": operation.OperationID,
		"status": "manual_review", "error": errWorkspaceDeleteTerminalConflict.Error(),
	}
}

func (app *controlPlaneServer) saveWorkspaceDeleteTerminalConflictAudit(r *http.Request, operation workspaceDeleteOperation) error {
	action := "workspace.delete.terminal_conflict"
	after := map[string]any{"operationId": operation.OperationID, "error": errWorkspaceDeleteTerminalConflict.Error()}
	event := app.auditEvent(r, action, "workspace", operation.WorkspaceID, operation.AccountID, nil, after, "conflict")
	event["id"] = "audit-" + stableID(action, operation.OperationID, errWorkspaceDeleteTerminalConflict.Error())[:12]
	return app.tables.SaveAuditEvent(r.Context(), event)
}

func (app *controlPlaneServer) newWorkspaceDeleteOperation(ctx context.Context, service *controlplane.Service, workspace map[string]any, sub2APIUserID int64, now time.Time) (workspaceDeleteOperation, error) {
	workspaceID := stringValue(workspace["id"])
	accountID := firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"]))
	ownerUserID := firstNonEmpty(stringValue(workspace["ownerUserId"]), stringValue(workspace["ownerId"]))
	launch, found, err := app.canonicalWorkspaceLaunch(ctx, workspace, workspaceLaunchStableProjectionMatches)
	if err != nil || !found || launch.int64Fact("sub2apiUserId") != sub2APIUserID {
		return workspaceDeleteOperation{}, errWorkspaceDeleteUnconfirmed
	}
	gatewayIdentity, err := app.currentWorkspaceDeleteGatewayIdentity(ctx, workspace, launch)
	if err != nil {
		if errors.Is(err, errWorkspaceKeyRotationInProgress) {
			return workspaceDeleteOperation{}, err
		}
		return workspaceDeleteOperation{}, errWorkspaceDeleteUnconfirmed
	}
	current, err := workspaceLaunchPurchaseReceiptInput(launch)
	if err != nil {
		return workspaceDeleteOperation{}, errWorkspaceDeleteUnconfirmed
	}
	expected := []clients.ReceiptInput{current}
	if launch.raw["resourceBillingEnabled"] != nil && !launch.boolFact("resourceBillingEnabled") {
		expected = append(expected, workspaceLaunchLegacyCreatedReceiptInput(launch))
	} else {
		expected = append(expected, workspaceLaunchHistoricalChargedReceiptInput(current))
	}
	launchReceiptID := launch.stringFact("receiptId")
	receipt, err := service.BillingReceiptForAccount(ctx, accountID, workspaceID, launchReceiptID)
	if err != nil || receipt.ReceiptID != launchReceiptID || !workspaceDeleteLaunchReceiptMatches(receipt.ReceiptInput, expected) {
		return workspaceDeleteOperation{}, errWorkspaceDeleteUnconfirmed
	}
	operation := workspaceDeleteOperation{
		SchemaVersion: 2, OperationID: workspaceDeleteOperationID(workspaceID), AccountID: accountID, OwnerUserID: ownerUserID, Sub2APIUserID: sub2APIUserID,
		WorkspaceID: workspaceID, ResourceType: "workspace", ResourceID: workspaceID, LaunchOperationID: launch.ID, LaunchReceiptID: launchReceiptID,
		RuntimeID: launch.stringFact("runtimeId"), RuntimeServiceName: launch.stringFact("runtimeServiceName"), ComputeID: launch.stringFact("computeAllocationId"),
		StorageID: launch.stringFact("storageId"), AttachmentID: launch.stringFact("attachmentId"), WorkspaceAPIKeyID: gatewayIdentity.WorkspaceAPIKeyID,
		GatewaySecretRef: gatewayIdentity.GatewaySecretRef, GatewayFingerprint: gatewayIdentity.GatewayFingerprint,
		Phase: "claimed", Status: "running", CreatedAt: now.Format(time.RFC3339Nano),
	}
	operation.RequestHash = workspaceDeleteRequestHash(operation)
	if !validWorkspaceDeleteIdentity(operation) {
		return workspaceDeleteOperation{}, errWorkspaceDeleteUnconfirmed
	}
	return operation, nil
}

func (app *controlPlaneServer) currentWorkspaceDeleteGatewayIdentity(ctx context.Context, workspace map[string]any, launch workspaceLaunchReconcileOperation) (workspaceDeleteGatewayIdentity, error) {
	workspaceID := stringValue(workspace["id"])
	accountID := firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"]))
	currentKeyID, ok := positiveIntegerField(workspace, "workspaceApiKeyId")
	launchKeyID := launch.int64Fact("workspaceApiKeyId")
	if !ok || launchKeyID <= 0 {
		return workspaceDeleteGatewayIdentity{}, errWorkspaceDeleteUnconfirmed
	}
	rows, err := queryRuntimeOperations(ctx, app.tables, runtimeOperationQuery{AccountID: accountID, WorkspaceID: workspaceID, Action: "workspace.gateway_key.rotate"})
	if err != nil {
		return workspaceDeleteGatewayIdentity{}, errWorkspaceDeleteUnconfirmed
	}
	byNewKeyID := make(map[int64]workspaceKeyRotationOperation, len(rows))
	for _, row := range rows {
		if stringValue(row["status"]) != "succeeded" {
			return workspaceDeleteGatewayIdentity{}, errWorkspaceKeyRotationInProgress
		}
		rotation, decodeErr := decodeWorkspaceKeyRotation(row)
		if decodeErr != nil || stringValue(row["accountId"]) != accountID || stringValue(row["workspaceId"]) != workspaceID ||
			stringValue(row["action"]) != "workspace.gateway_key.rotate" || !workspaceKeyRotationSucceededEvidenceValid(rotation) {
			return workspaceDeleteGatewayIdentity{}, errWorkspaceDeleteUnconfirmed
		}
		if _, duplicate := byNewKeyID[rotation.NewKeyID]; duplicate {
			return workspaceDeleteGatewayIdentity{}, errWorkspaceDeleteUnconfirmed
		}
		byNewKeyID[rotation.NewKeyID] = rotation
	}
	if currentKeyID == launchKeyID {
		identity := workspaceDeleteGatewayIdentity{
			WorkspaceAPIKeyID: currentKeyID, GatewaySecretRef: launch.stringFact("gatewaySecretRef"), GatewayFingerprint: launch.stringFact("workspaceKeyFingerprint"),
		}
		if len(byNewKeyID) != 0 || identity.GatewaySecretRef == "" || identity.GatewayFingerprint == "" {
			return workspaceDeleteGatewayIdentity{}, errWorkspaceDeleteUnconfirmed
		}
		return identity, nil
	}
	currentRotation, ok := byNewKeyID[currentKeyID]
	if !ok {
		return workspaceDeleteGatewayIdentity{}, errWorkspaceDeleteUnconfirmed
	}
	visited := make(map[int64]struct{}, len(byNewKeyID))
	for keyID := currentKeyID; keyID != launchKeyID; {
		if _, cycle := visited[keyID]; cycle {
			return workspaceDeleteGatewayIdentity{}, errWorkspaceDeleteUnconfirmed
		}
		rotation, exists := byNewKeyID[keyID]
		if !exists {
			return workspaceDeleteGatewayIdentity{}, errWorkspaceDeleteUnconfirmed
		}
		visited[keyID] = struct{}{}
		keyID = rotation.OldKeyID
	}
	if len(visited) != len(byNewKeyID) {
		return workspaceDeleteGatewayIdentity{}, errWorkspaceDeleteUnconfirmed
	}
	return workspaceDeleteGatewayIdentity{
		WorkspaceAPIKeyID: currentKeyID, GatewaySecretRef: currentRotation.SecretRef, GatewayFingerprint: currentRotation.Fingerprint,
	}, nil
}

func workspaceDeleteLaunchReceiptMatches(actual clients.ReceiptInput, expected []clients.ReceiptInput) bool {
	for _, candidate := range expected {
		if workspaceLaunchReceiptInputMatches(actual, candidate) {
			return true
		}
	}
	return false
}

func workspaceDeleteOperationID(workspaceID string) string {
	return "workspace-delete-" + stableID(workspaceDeleteAction, workspaceID)[:18]
}

func workspaceDeleteLegacyOperationID(workspaceID string) string {
	return "workspace-delete-" + stableID(workspaceDeleteLegacyAction, workspaceID)[:18]
}

func workspaceDeleteRequestHash(operation workspaceDeleteOperation) string {
	return stableID(workspaceDeleteAction, strconv.Itoa(operation.SchemaVersion), operation.AccountID, operation.OwnerUserID, operation.WorkspaceID, operation.ResourceType, operation.ResourceID, operation.LaunchOperationID, operation.LaunchReceiptID,
		operation.RuntimeID, operation.RuntimeServiceName, operation.ComputeID, operation.StorageID, operation.AttachmentID,
		operation.GatewaySecretRef, operation.GatewayFingerprint, strconv.FormatInt(operation.Sub2APIUserID, 10), strconv.FormatInt(operation.WorkspaceAPIKeyID, 10))
}

func workspaceDeleteStageKey(operation workspaceDeleteOperation, stage string) string {
	return operation.OperationID + ":" + stage
}

func validWorkspaceDeleteReplayAuthorization(authorization workspaceDeleteReplayAuthorization, idempotencyKey string) bool {
	if authorization == (workspaceDeleteReplayAuthorization{}) {
		return true
	}
	if authorization.SchemaVersion != 1 || !validBillingReviewOpaqueID(authorization.AuthorizationID) || authorization.IdempotencyKey != idempotencyKey || authorization.LeaseGeneration <= 0 {
		return false
	}
	leaseExpiresAt, leaseErr := time.Parse(time.RFC3339Nano, authorization.LeaseExpiresAt)
	switch authorization.State {
	case "claimed":
		return leaseErr == nil && !leaseExpiresAt.IsZero() && authorization.DispatchStartedAt == "" && authorization.ConsumedAt == ""
	case "dispatched":
		_, dispatchErr := time.Parse(time.RFC3339Nano, authorization.DispatchStartedAt)
		return leaseErr == nil && !leaseExpiresAt.IsZero() && dispatchErr == nil && authorization.ConsumedAt == ""
	case "consumed":
		_, consumedErr := time.Parse(time.RFC3339Nano, authorization.ConsumedAt)
		dispatchValid := authorization.DispatchStartedAt == ""
		if !dispatchValid {
			_, dispatchErr := time.Parse(time.RFC3339Nano, authorization.DispatchStartedAt)
			dispatchValid = dispatchErr == nil
		}
		return leaseErr == nil && !leaseExpiresAt.IsZero() && dispatchValid && consumedErr == nil
	default:
		return false
	}
}

func workspaceDeleteOperationRow(operation workspaceDeleteOperation) map[string]any {
	encoded, _ := json.Marshal(operation)
	return map[string]any{
		"id": operation.OperationID, "operationId": operation.OperationID, "accountId": operation.AccountID, "workspaceId": operation.WorkspaceID,
		"resourceId": operation.WorkspaceID, "resourceKind": "workspace", "action": workspaceDeleteAction, "status": operation.Status, "result": string(encoded),
		"computeAllocationId": operation.ComputeID, "storageId": operation.StorageID, "attachmentId": operation.AttachmentID, "runtimeId": operation.RuntimeID,
		"createdAt": operation.CreatedAt,
	}
}

func validWorkspaceDeleteBaseIdentity(operation workspaceDeleteOperation) bool {
	return operation.SchemaVersion == 2 && operation.OperationID != "" && operation.OperationID == workspaceDeleteOperationID(operation.WorkspaceID) && operation.AccountID != "" && operation.OwnerUserID != "" &&
		operation.Sub2APIUserID > 0 && operation.WorkspaceID != "" && operation.LaunchOperationID != "" && operation.LaunchReceiptID != "" && operation.RuntimeID != "" &&
		operation.ResourceType == "workspace" && operation.ResourceID == operation.WorkspaceID &&
		operation.RuntimeServiceName != "" && operation.ComputeID != "" && operation.StorageID != "" && operation.AttachmentID != "" && operation.WorkspaceAPIKeyID > 0 &&
		operation.GatewaySecretRef != "" && operation.GatewayFingerprint != "" && operation.CreatedAt != ""
}

func validWorkspaceDeleteIdentity(operation workspaceDeleteOperation) bool {
	return validWorkspaceDeleteBaseIdentity(operation) && validWorkspaceDeleteReplayAuthorization(operation.KeyDeleteReplay, workspaceDeleteStageKey(operation, "key")) && validWorkspaceDeleteState(operation)
}

func validWorkspaceDeleteComputeReadbackState(operation workspaceDeleteOperation) bool {
	if operation.ComputeReadbacks == 0 && operation.MaxComputeReadbacks == 0 {
		return operation.ComputeStatus == "" && operation.ComputeReadbackNotBefore == ""
	}
	if operation.MaxComputeReadbacks != workspaceDeleteComputeReadbackBudget || operation.ComputeReadbacks <= 0 || operation.ComputeReadbacks > operation.MaxComputeReadbacks || operation.ComputeStatus == "" {
		return false
	}
	if operation.ComputeStatus == "destroying" {
		_, err := time.Parse(time.RFC3339Nano, operation.ComputeReadbackNotBefore)
		return operation.Phase == "storage_absent" && err == nil
	}
	if !workspaceDeleteComputeTerminal(operation.ComputeStatus) || operation.ComputeReadbackNotBefore != "" {
		return false
	}
	switch operation.Phase {
	case "compute_absent", "key_absent", "workspace_absent", "deletion_receipt_recorded", "complete":
		return true
	default:
		return false
	}
}

func workspaceDeletePhaseRank(phase string) (int, bool) {
	phases := []string{"claimed", "runtime_secret_absent", "attachment_absent", "storage_absent", "compute_absent", "key_absent", "workspace_absent", "deletion_receipt_recorded", "complete"}
	for rank, candidate := range phases {
		if phase == candidate {
			return rank, true
		}
	}
	return 0, false
}

func validWorkspaceDeleteState(operation workspaceDeleteOperation) bool {
	rank, ok := workspaceDeletePhaseRank(operation.Phase)
	if !ok || !validWorkspaceDeleteComputeReadbackState(operation) {
		return false
	}
	switch operation.Status {
	case "running":
		if operation.Phase == "complete" || operation.LastErrorCode != "" {
			return false
		}
	case "manual_review":
		if operation.Phase == "complete" || operation.LastErrorCode == "" {
			return false
		}
	case "succeeded":
		if operation.Phase != "complete" || operation.LastErrorCode != "" {
			return false
		}
	default:
		return false
	}

	runtimeAbsent := operation.RuntimeStatus == "absent" && operation.SecretStatus == "absent"
	attachmentAbsent := operation.AttachmentStatus == "absent"
	storageAbsent := operation.StorageStatus == "absent"
	computeAbsent := operation.ComputeStatus == "absent" && operation.ComputeReadbacks > 0 && operation.MaxComputeReadbacks == workspaceDeleteComputeReadbackBudget && operation.ComputeReadbackNotBefore == ""
	keyAbsent := operation.KeyStatus == "absent"
	if rank < 1 && (operation.RuntimeStatus != "" || operation.SecretStatus != "") || rank >= 1 && !runtimeAbsent ||
		rank < 2 && operation.AttachmentStatus != "" || rank >= 2 && !attachmentAbsent ||
		rank < 3 && operation.StorageStatus != "" || rank >= 3 && !storageAbsent ||
		rank < 4 && operation.ComputeStatus != "" && !(rank == 3 && operation.ComputeStatus == "destroying") || rank >= 4 && !computeAbsent ||
		rank < 5 && operation.KeyStatus != "" || rank >= 5 && !keyAbsent ||
		rank < 7 && operation.DeletionReceiptID != "" || rank >= 7 && operation.DeletionReceiptID == "" {
		return false
	}
	if rank < 4 && (operation.KeyDeleteAttempted || operation.KeyDeleteReplay != (workspaceDeleteReplayAuthorization{})) {
		return false
	}
	if !operation.KeyDeleteAttempted && operation.KeyDeleteReplay != (workspaceDeleteReplayAuthorization{}) {
		return false
	}
	if rank == 3 {
		if operation.ComputeStatus == "" {
			return operation.ComputeReadbacks == 0 && operation.MaxComputeReadbacks == 0 && operation.ComputeReadbackNotBefore == ""
		}
		return operation.ComputeStatus == "destroying"
	}
	if rank == 4 && operation.KeyDeleteReplay.State == "consumed" {
		return false
	}
	if rank >= 5 && operation.KeyDeleteReplay != (workspaceDeleteReplayAuthorization{}) && operation.KeyDeleteReplay.State != "consumed" {
		return false
	}
	return true
}

func validWorkspaceDeleteTransition(current, desired workspaceDeleteOperation, mutation workspaceDeleteStoreMutation) bool {
	currentRank, currentOK := workspaceDeletePhaseRank(current.Phase)
	desiredRank, desiredOK := workspaceDeletePhaseRank(desired.Phase)
	if !currentOK || !desiredOK || current.Phase == "complete" || desiredRank < currentRank || desiredRank > currentRank+1 || desired.ComputeReadbacks < current.ComputeReadbacks ||
		current.KeyDeleteAttempted && !desired.KeyDeleteAttempted || current.DeletionReceiptID != "" && desired.DeletionReceiptID != current.DeletionReceiptID {
		return false
	}
	deleteTransition := current.Phase == "key_absent" && desired.Phase == "workspace_absent"
	if mutation.DeleteWorkspace != deleteTransition {
		return false
	}
	requireAbsent := !mutation.DeleteWorkspace && desiredRank >= 6
	return mutation.RequireWorkspaceAbsent == requireAbsent
}

func decodeWorkspaceDeleteOperation(row map[string]any) (workspaceDeleteOperation, error) {
	var operation workspaceDeleteOperation
	if json.Unmarshal([]byte(stringValue(row["result"])), &operation) != nil {
		return workspaceDeleteOperation{}, errors.New("workspace_delete_operation_invalid")
	}
	validIdentity := validWorkspaceDeleteIdentity(operation)
	terminalConflictCandidate := operation.Phase == "complete" && validWorkspaceDeleteBaseIdentity(operation)
	if stringValue(row["action"]) != workspaceDeleteAction ||
		operation.OperationID == "" || operation.OperationID != stringValue(row["id"]) || operation.OperationID != stringValue(row["operationId"]) ||
		operation.AccountID == "" || operation.AccountID != stringValue(row["accountId"]) || operation.OwnerUserID == "" ||
		operation.WorkspaceID == "" || operation.WorkspaceID != stringValue(row["workspaceId"]) || operation.WorkspaceID != stringValue(row["resourceId"]) ||
		stringValue(row["resourceKind"]) != "workspace" || operation.Status != stringValue(row["status"]) || !validIdentity && !terminalConflictCandidate ||
		operation.RequestHash == "" || operation.RequestHash != workspaceDeleteRequestHash(operation) {
		return workspaceDeleteOperation{}, errors.New("workspace_delete_operation_invalid")
	}
	return operation, nil
}

func workspaceDeleteOperationIdentityMatches(row map[string]any, desired workspaceDeleteOperation) bool {
	current, err := decodeWorkspaceDeleteOperation(row)
	return err == nil && current.SchemaVersion == desired.SchemaVersion && current.OperationID == desired.OperationID && current.RequestHash == desired.RequestHash && current.AccountID == desired.AccountID &&
		current.OwnerUserID == desired.OwnerUserID && current.WorkspaceID == desired.WorkspaceID && current.Sub2APIUserID == desired.Sub2APIUserID &&
		current.ResourceType == desired.ResourceType && current.ResourceID == desired.ResourceID &&
		current.LaunchOperationID == desired.LaunchOperationID && current.LaunchReceiptID == desired.LaunchReceiptID && current.RuntimeID == desired.RuntimeID &&
		current.RuntimeServiceName == desired.RuntimeServiceName && current.ComputeID == desired.ComputeID && current.StorageID == desired.StorageID &&
		current.AttachmentID == desired.AttachmentID && current.WorkspaceAPIKeyID == desired.WorkspaceAPIKeyID && current.GatewaySecretRef == desired.GatewaySecretRef &&
		current.GatewayFingerprint == desired.GatewayFingerprint && current.CreatedAt == desired.CreatedAt
}

func validWorkspaceDeleteStoreMutation(mutation workspaceDeleteStoreMutation) (workspaceDeleteOperation, bool) {
	desired, err := decodeWorkspaceDeleteOperation(mutation.DesiredOperation)
	if err != nil || !validWorkspaceDeleteIdentity(desired) || mutation.DeleteWorkspace && mutation.RequireWorkspaceAbsent || mutation.Create && (mutation.ExpectedResult != "" || mutation.DeleteWorkspace || mutation.RequireWorkspaceAbsent) {
		return workspaceDeleteOperation{}, false
	}
	if !mutation.Create && mutation.ExpectedResult == "" {
		return workspaceDeleteOperation{}, false
	}
	return desired, true
}

func workspaceRenewalBlocksDelete(row map[string]any) bool {
	if stringValue(row["action"]) != "workspace.renewal" {
		return false
	}
	operation, err := decodeWorkspaceRenewalOperation(row)
	return err != nil || operation.Status == "manual_review" || !terminalWorkspaceRenewal(operation)
}

func workspaceKeyRotationBlocksDelete(row map[string]any) bool {
	if stringValue(row["action"]) != "workspace.gateway_key.rotate" {
		return false
	}
	operation, err := decodeWorkspaceKeyRotation(row)
	return err != nil || stringValue(row["status"]) != "succeeded" || !workspaceKeyRotationSucceededEvidenceValid(operation)
}

func workspaceDeleteBlocksRotation(row map[string]any) bool {
	action := stringValue(row["action"])
	if action == workspaceDeleteLegacyAction {
		return true
	}
	if action != workspaceDeleteAction {
		return false
	}
	operation, err := decodeWorkspaceDeleteOperation(row)
	return err != nil || operation.Status != "succeeded" || operation.Phase != "complete"
}

func workspaceDeleteWorkspaceProjectionMatches(operation workspaceDeleteOperation, row map[string]any, requireResources bool) bool {
	if row == nil || firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"])) != operation.AccountID ||
		firstNonEmpty(stringValue(row["ownerUserId"]), stringValue(row["ownerId"])) != operation.OwnerUserID {
		return false
	}
	if keyID, ok := positiveIntegerField(row, "workspaceApiKeyId"); !ok || keyID != operation.WorkspaceAPIKeyID {
		return false
	}
	return !requireResources ||
		firstNonEmpty(stringValue(row["currentComputeAllocationId"]), stringValue(row["computeAllocationId"])) == operation.ComputeID &&
			stringValue(row["storageId"]) == operation.StorageID &&
			firstNonEmpty(stringValue(row["currentAttachmentId"]), stringValue(row["attachmentId"])) == operation.AttachmentID &&
			stringValue(row["runtimeId"]) == operation.RuntimeID
}

func workspaceDeleteResourceProjectionMatches(operation workspaceDeleteOperation, kind string, row map[string]any) bool {
	if row == nil {
		return true
	}
	if firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"])) != operation.AccountID || stringValue(row["workspaceId"]) != operation.WorkspaceID {
		return false
	}
	if ownerID := firstNonEmpty(stringValue(row["ownerUserId"]), stringValue(row["ownerId"])); ownerID != "" && ownerID != operation.OwnerUserID {
		return false
	}
	switch kind {
	case "compute":
		return stringValue(row["id"]) == operation.ComputeID
	case "storage":
		return stringValue(row["id"]) == operation.StorageID
	case "attachment":
		return stringValue(row["id"]) == operation.AttachmentID && stringValue(row["computeAllocationId"]) == operation.ComputeID &&
			firstNonEmpty(stringValue(row["storageId"]), stringValue(row["volumeId"])) == operation.StorageID
	default:
		return false
	}
}

func workspaceDeleteBlocksRenewal(row map[string]any) bool {
	return stringValue(row["action"]) == workspaceDeleteAction
}

func (app *controlPlaneServer) runWorkspaceDelete(ctx context.Context, service *controlplane.Service, credential clients.SessionDelegatedCredential, authorizationID string, operation workspaceDeleteOperation) (workspaceDeleteOperation, error) {
	for {
		if operation.Phase == "complete" {
			if !validWorkspaceDeleteIdentity(operation) || operation.Status != "succeeded" || operation.RuntimeStatus != "absent" || operation.SecretStatus != "absent" ||
				operation.AttachmentStatus != "absent" || operation.StorageStatus != "absent" || operation.ComputeStatus != "absent" || operation.KeyStatus != "absent" || operation.DeletionReceiptID == "" {
				return operation, errWorkspaceDeleteTerminalConflict
			}
			_, found, err := app.tables.GetWorkspace(ctx, operation.WorkspaceID)
			if err != nil {
				return operation, errWorkspaceDeleteStateRead
			}
			if found {
				return operation, errWorkspaceDeleteTerminalConflict
			}
			return operation, nil
		}
		if !validWorkspaceDeleteIdentity(operation) {
			return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "workspace_delete_identity_mismatch")
		}
		switch operation.Phase {
		case "claimed":
			runtimeObservation, secretObservation, err := observeWorkspaceDeleteRuntimeAndSecret(ctx, service, operation)
			if err != nil {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_runtime_readback_unavailable")
			}
			if !workspaceDeleteRuntimeAndSecretAbsent(runtimeObservation, secretObservation) {
				if !workspaceDeleteRuntimeAndSecretReady(operation, runtimeObservation, secretObservation) {
					return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_runtime_identity_conflict")
				}
				runtime, destroyErr := service.DestroyWorkspaceRuntime(ctx, operation.AccountID, operation.WorkspaceID, workspaceDeleteStageKey(operation, "runtime"))
				runtimeObservation, secretObservation, err = observeWorkspaceDeleteRuntimeAndSecret(ctx, service, operation)
				var residualObservation clients.WorkspaceRuntimeDeleteObservation
				var residualErr error
				if err == nil && workspaceDeleteRuntimeAndSecretAbsent(runtimeObservation, secretObservation) {
					residualObservation, residualErr = service.ObserveWorkspaceDeleteRuntimeResiduals(ctx, operation.WorkspaceID)
				}
				if err == nil && residualErr == nil && workspaceDeleteRuntimeAndSecretAbsent(runtimeObservation, secretObservation) && workspaceDeleteRuntimeResidualsAbsent(residualObservation, operation.WorkspaceID) {
				} else if destroyErr != nil || runtime.ID != operation.RuntimeID || runtime.WorkspaceID != operation.WorkspaceID || runtime.Status != "destroyed" {
					return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_runtime_destroy_unconfirmed")
				} else {
					return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_runtime_absence_unconfirmed")
				}
			}
			next := operation
			next.Phase, next.Status, next.RuntimeStatus, next.SecretStatus, next.LastErrorCode = "runtime_secret_absent", "running", "absent", "absent", ""
			if err := app.persistWorkspaceDelete(ctx, operation, next, false, false); err != nil {
				return operation, err
			}
			operation = next
		case "runtime_secret_absent":
			attachment, err := service.DetachWorkspaceStorage(ctx, operation.AccountID, operation.WorkspaceID, operation.AttachmentID, workspaceDeleteStageKey(operation, "attachment"))
			if err != nil || !workspaceDeleteAttachmentMatches(operation, attachment) {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_attachment_unconfirmed")
			}
			next := operation
			next.Phase, next.Status, next.AttachmentStatus, next.LastErrorCode = "attachment_absent", "running", "absent", ""
			if err := app.persistWorkspaceDelete(ctx, operation, next, false, false); err != nil {
				return operation, err
			}
			operation = next
		case "attachment_absent":
			storage, err := service.DestroyWorkspaceStorage(ctx, operation.AccountID, operation.WorkspaceID, operation.StorageID, workspaceDeleteStageKey(operation, "storage"))
			if err != nil || !workspaceDeleteStorageMatches(operation, storage) {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_storage_unconfirmed")
			}
			next := operation
			next.Phase, next.Status, next.StorageStatus, next.LastErrorCode = "storage_absent", "running", "absent", ""
			if err := app.persistWorkspaceDelete(ctx, operation, next, false, false); err != nil {
				return operation, err
			}
			operation = next
		case "storage_absent":
			var compute clients.ComputeAllocation
			var err error
			readNow := time.Now().UTC()
			if operation.ComputeStatus == "destroying" {
				notBefore, parseErr := time.Parse(time.RFC3339Nano, operation.ComputeReadbackNotBefore)
				if parseErr != nil {
					return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_compute_readback_schedule_invalid")
				}
				if readNow.Before(notBefore) {
					return operation, errWorkspaceDeletePending
				}
				if operation.MaxComputeReadbacks != workspaceDeleteComputeReadbackBudget || operation.ComputeReadbacks >= operation.MaxComputeReadbacks {
					return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_compute_absence_unconfirmed")
				}
				claimed := operation
				claimed.ComputeReadbacks++
				claimed.ComputeReadbackNotBefore = readNow.Add(workspaceDeleteComputeReadbackInterval).Format(time.RFC3339Nano)
				if err := app.persistWorkspaceDelete(ctx, operation, claimed, false, false); err != nil {
					return operation, err
				}
				operation = claimed
				compute, err = service.WorkspaceDeleteComputeStatus(ctx, operation.ComputeID)
				if err != nil || !workspaceDeleteComputeIdentityMatches(operation, compute) {
					return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_compute_absence_unconfirmed")
				}
			} else {
				compute, err = service.DestroyWorkspaceCompute(ctx, operation.AccountID, operation.WorkspaceID, operation.ComputeID, workspaceDeleteStageKey(operation, "compute"))
				if err != nil || !workspaceDeleteComputeStartMatches(operation, compute) {
					return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_compute_destroy_unconfirmed")
				}
				claimed := operation
				claimed.ComputeStatus, claimed.MaxComputeReadbacks, claimed.ComputeReadbacks = "destroying", workspaceDeleteComputeReadbackBudget, 1
				claimed.ComputeReadbackNotBefore = readNow.Add(workspaceDeleteComputeReadbackInterval).Format(time.RFC3339Nano)
				if err := app.persistWorkspaceDelete(ctx, operation, claimed, false, false); err != nil {
					return operation, err
				}
				operation = claimed
				compute, err = service.WorkspaceDeleteComputeStatus(ctx, operation.ComputeID)
				if err != nil || !workspaceDeleteComputeIdentityMatches(operation, compute) {
					return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_compute_absence_unconfirmed")
				}
			}
			if compute.Status == "destroying" {
				if operation.ComputeReadbacks >= operation.MaxComputeReadbacks {
					return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_compute_absence_unconfirmed")
				}
				return operation, errWorkspaceDeletePending
			}
			if !workspaceDeleteComputeTerminal(compute.Status) {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "fabric_compute_absence_unconfirmed")
			}
			next := operation
			next.Phase, next.Status, next.ComputeStatus, next.ComputeReadbackNotBefore, next.LastErrorCode = "compute_absent", "running", "absent", "", ""
			if err := app.persistWorkspaceDelete(ctx, operation, next, false, false); err != nil {
				return operation, err
			}
			operation = next
		case "compute_absent":
			key, err := service.GatewayUserKey(ctx, credential, operation.Sub2APIUserID, operation.WorkspaceAPIKeyID)
			if errors.Is(err, clients.ErrSub2APIKeyNotFound) {
				next, advanceErr := app.confirmWorkspaceDeleteKeyAbsent(ctx, operation)
				if advanceErr != nil {
					return operation, advanceErr
				}
				operation = next
				continue
			}
			if err != nil || !workspaceDeleteKeyMatches(operation, key) {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "sub2api_key_readback_unconfirmed")
			}
			if operation.KeyDeleteAttempted {
				claimed, won, claimErr := app.claimWorkspaceDeleteReplay(ctx, operation, authorizationID)
				if claimErr != nil {
					return operation, claimErr
				}
				if !won {
					return claimed, errWorkspaceDeleteUnconfirmed
				}
				operation = claimed
			} else {
				attempted := operation
				attempted.Status, attempted.KeyDeleteAttempted, attempted.LastErrorCode = "running", true, ""
				if err := app.persistWorkspaceDelete(ctx, operation, attempted, false, false); err != nil {
					return operation, err
				}
				operation = attempted
			}
			key, err = service.GatewayUserKey(ctx, credential, operation.Sub2APIUserID, operation.WorkspaceAPIKeyID)
			if errors.Is(err, clients.ErrSub2APIKeyNotFound) {
				next, advanceErr := app.confirmWorkspaceDeleteKeyAbsent(ctx, operation)
				if advanceErr != nil {
					return operation, advanceErr
				}
				operation = next
				continue
			}
			if err != nil || !workspaceDeleteKeyMatches(operation, key) {
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "sub2api_key_readback_unconfirmed")
			}
			if operation.KeyDeleteReplay.AuthorizationID != "" {
				dispatched, dispatchErr := app.markWorkspaceDeleteReplayDispatch(ctx, operation)
				if dispatchErr != nil {
					return operation, dispatchErr
				}
				operation = dispatched
			}
			deleteErr := service.DeleteGatewayUserKeyIdempotent(ctx, credential, operation.Sub2APIUserID, operation.WorkspaceAPIKeyID, workspaceDeleteStageKey(operation, "key"))
			_, err = service.GatewayUserKey(ctx, credential, operation.Sub2APIUserID, operation.WorkspaceAPIKeyID)
			if !errors.Is(err, clients.ErrSub2APIKeyNotFound) {
				if deleteErr != nil {
					return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "sub2api_key_delete_unconfirmed")
				}
				return app.markWorkspaceDeleteUnconfirmed(ctx, operation, "sub2api_key_absence_unconfirmed")
			}
			next, advanceErr := app.confirmWorkspaceDeleteKeyAbsent(ctx, operation)
			if advanceErr != nil {
				return operation, advanceErr
			}
			operation = next
		case "key_absent":
			next := operation
			next.Phase, next.Status, next.LastErrorCode = "workspace_absent", "running", ""
			if err := app.persistWorkspaceDelete(ctx, operation, next, true, false); err != nil {
				return operation, err
			}
			operation = next
		case "workspace_absent":
			if _, found, err := app.tables.GetWorkspace(ctx, operation.WorkspaceID); err != nil || found {
				return operation, errWorkspaceDeleteUnconfirmed
			}
			next, err := app.recordWorkspaceDeletionReceipt(ctx, service, operation)
			if err != nil {
				return next, err
			}
			operation = next
		case "deletion_receipt_recorded":
			next := operation
			next.Phase, next.Status, next.LastErrorCode = "complete", "succeeded", ""
			if err := app.persistWorkspaceDelete(ctx, operation, next, false, true); err != nil {
				return operation, err
			}
			operation = next
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

func (app *controlPlaneServer) claimWorkspaceDeleteReplay(ctx context.Context, operation workspaceDeleteOperation, authorizationID string) (workspaceDeleteOperation, bool, error) {
	if !validBillingReviewOpaqueID(authorizationID) || operation.Status != "running" || operation.Phase != "compute_absent" || !operation.KeyDeleteAttempted {
		return operation, false, errWorkspaceDeleteCASConflict
	}
	now := time.Now().UTC()
	replay := operation.KeyDeleteReplay
	if replay.AuthorizationID != "" {
		if replay.AuthorizationID != authorizationID || replay.IdempotencyKey != workspaceDeleteStageKey(operation, "key") || replay.State != "claimed" {
			return operation, false, errWorkspaceDeleteUnconfirmed
		}
		leaseExpiresAt, err := time.Parse(time.RFC3339Nano, replay.LeaseExpiresAt)
		if err != nil {
			return operation, false, errWorkspaceDeleteCASConflict
		}
		if leaseExpiresAt.After(now) {
			return operation, false, nil
		}
		replay.LeaseGeneration++
	} else {
		replay = workspaceDeleteReplayAuthorization{SchemaVersion: 1, AuthorizationID: authorizationID, IdempotencyKey: workspaceDeleteStageKey(operation, "key"), State: "claimed", LeaseGeneration: 1}
	}
	replay.LeaseExpiresAt = now.Add(workspaceDeleteReplayLease).Format(time.RFC3339Nano)
	next := operation
	next.KeyDeleteReplay = replay
	if err := app.persistWorkspaceDelete(ctx, operation, next, false, false); err != nil {
		return operation, false, err
	}
	return next, true, nil
}

func (app *controlPlaneServer) markWorkspaceDeleteReplayDispatch(ctx context.Context, operation workspaceDeleteOperation) (workspaceDeleteOperation, error) {
	replay := operation.KeyDeleteReplay
	if replay.State != "claimed" || replay.DispatchStartedAt != "" || replay.ConsumedAt != "" {
		return operation, errWorkspaceDeleteCASConflict
	}
	replay.State = "dispatched"
	replay.DispatchStartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	next := operation
	next.KeyDeleteReplay = replay
	if err := app.persistWorkspaceDelete(ctx, operation, next, false, false); err != nil {
		return operation, err
	}
	return next, nil
}

func consumeWorkspaceDeleteReplay(operation *workspaceDeleteOperation, now time.Time) error {
	replay := operation.KeyDeleteReplay
	if replay.AuthorizationID == "" {
		return nil
	}
	if replay.State != "claimed" && replay.State != "dispatched" || replay.ConsumedAt != "" {
		return errWorkspaceDeleteCASConflict
	}
	replay.State = "consumed"
	replay.ConsumedAt = now.UTC().Format(time.RFC3339Nano)
	operation.KeyDeleteReplay = replay
	return nil
}

func (app *controlPlaneServer) confirmWorkspaceDeleteKeyAbsent(ctx context.Context, operation workspaceDeleteOperation) (workspaceDeleteOperation, error) {
	next := operation
	if err := consumeWorkspaceDeleteReplay(&next, time.Now()); err != nil {
		return operation, err
	}
	next.Phase, next.Status, next.KeyStatus, next.LastErrorCode = "key_absent", "running", "absent", ""
	if err := app.persistWorkspaceDelete(ctx, operation, next, false, false); err != nil {
		return operation, err
	}
	return next, nil
}

func (app *controlPlaneServer) markWorkspaceDeleteUnconfirmed(ctx context.Context, operation workspaceDeleteOperation, code string) (workspaceDeleteOperation, error) {
	next := operation
	next.Status, next.LastErrorCode = "manual_review", code
	requireAbsent := operation.Phase == "workspace_absent" || operation.Phase == "deletion_receipt_recorded" || operation.Phase == "complete"
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

func workspaceDeleteRuntimeResidualsAbsent(observation clients.WorkspaceRuntimeDeleteObservation, workspaceID string) bool {
	return observation.SchemaVersion == clients.WorkspaceRuntimeDeleteObservationSchemaVersion && observation.WorkspaceID == workspaceID &&
		observation.State == clients.WorkspaceOwnerObservationAbsent && len(observation.Residuals) == 0
}

func workspaceDeleteRuntimeAndSecretReady(operation workspaceDeleteOperation, runtime clients.WorkspaceRuntimeObservation, secret clients.WorkspaceRuntimeGatewaySecretObservation) bool {
	return runtime.SchemaVersion == clients.WorkspaceOwnerObservationSchemaVersion && runtime.State == clients.WorkspaceOwnerObservationReady && runtime.Runtime != nil &&
		runtime.WorkspaceID == operation.WorkspaceID && runtime.Runtime.WorkspaceID == operation.WorkspaceID && runtime.Runtime.ID == operation.RuntimeID &&
		secret.SchemaVersion == clients.WorkspaceOwnerObservationSchemaVersion && secret.State == clients.WorkspaceOwnerObservationReady && secret.Binding != nil &&
		secret.WorkspaceID == operation.WorkspaceID && secret.Binding.WorkspaceID == operation.WorkspaceID && secret.Binding.WorkspaceAPIKeyID == operation.WorkspaceAPIKeyID &&
		secret.Binding.SecretRef == operation.GatewaySecretRef && secret.Binding.Fingerprint == operation.GatewayFingerprint && secret.Binding.Bound
}

func workspaceDeleteKeyMatches(operation workspaceDeleteOperation, key clients.Sub2APIWorkspaceKey) bool {
	return key.ID == operation.WorkspaceAPIKeyID && key.UserID == operation.Sub2APIUserID && key.Name == workspaceReservedKeyName(operation.WorkspaceID) &&
		(key.Status == "active" || key.Status == "quota_exhausted")
}

func workspaceDeletionReceiptInput(operation workspaceDeleteOperation) clients.ReceiptInput {
	return clients.ReceiptInput{
		Type: "workspace.deleted.v1", Status: "completed", Surface: "control_plane", AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
		RequestID: operation.OperationID, InputRefs: map[string]any{"launchReceiptId": operation.LaunchReceiptID},
		Execution: map[string]any{
			"operationId": operation.OperationID, "resourceType": "workspace", "resourceId": operation.WorkspaceID,
			"computeAllocationId": operation.ComputeID, "storageId": operation.StorageID, "attachmentId": operation.AttachmentID,
			"runtimeId": operation.RuntimeID, "workspaceApiKeyId": operation.WorkspaceAPIKeyID, "workspaceKeyFingerprint": operation.GatewayFingerprint,
			"runtimeServiceName": operation.RuntimeServiceName, "gatewaySecretRef": operation.GatewaySecretRef,
		},
		OutputRefs: map[string]any{
			"runtimeStatus": "absent", "gatewaySecretStatus": "absent", "attachmentStatus": "absent", "storageStatus": "absent",
			"computeStatus": "absent", "workspaceKeyStatus": "absent", "workspaceStatus": "absent",
		},
		Owner: map[string]any{"accountId": operation.AccountID, "workspaceId": operation.WorkspaceID, "ownerUserId": operation.OwnerUserID},
	}
}

func (app *controlPlaneServer) recordWorkspaceDeletionReceipt(ctx context.Context, service *controlplane.Service, operation workspaceDeleteOperation) (workspaceDeleteOperation, error) {
	input := workspaceDeletionReceiptInput(operation)
	receipt, err := service.RecordMonthlyReceipt(ctx, input, operation.OperationID+":deletion-receipt")
	if err != nil || receipt.ReceiptID == "" || !workspaceLaunchReceiptInputMatches(receipt.ReceiptInput, input) {
		return operation, errWorkspaceDeleteUnconfirmed
	}
	next := operation
	next.Phase, next.Status, next.DeletionReceiptID, next.LastErrorCode = "deletion_receipt_recorded", "running", receipt.ReceiptID, ""
	if err := app.persistWorkspaceDelete(ctx, operation, next, false, true); err != nil {
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
	case "destroyed", "external_deleted", "deleted", "missing", "absent":
		return true
	default:
		return false
	}
}

func workspaceDeleteResponse(operation workspaceDeleteOperation, errorCode string) map[string]any {
	status := "deleted"
	if errorCode != "" {
		status = "manual_review"
	}
	response := map[string]any{"workspaceId": operation.WorkspaceID, "operationId": operation.OperationID, "status": status}
	if errorCode != "" {
		response["error"] = errorCode
		return response
	}
	response["accountId"] = operation.AccountID
	response["sub2apiUserId"] = operation.Sub2APIUserID
	response["launchOperationId"] = operation.LaunchOperationID
	response["launchReceiptId"] = operation.LaunchReceiptID
	response["deletionReceiptId"] = operation.DeletionReceiptID
	response["runtimeId"] = operation.RuntimeID
	response["workspaceApiKeyId"] = operation.WorkspaceAPIKeyID
	response["runtimeStatus"] = operation.RuntimeStatus
	response["secretStatus"] = operation.SecretStatus
	response["keyStatus"] = operation.KeyStatus
	return response
}

func workspaceDeletePendingResponse(operation workspaceDeleteOperation) map[string]any {
	return map[string]any{
		"workspaceId": operation.WorkspaceID, "operationId": operation.OperationID, "status": "pending", "phase": operation.Phase,
		"ownerStage": "compute", "computeStatus": operation.ComputeStatus, "computeReadbacks": operation.ComputeReadbacks, "maxComputeReadbacks": operation.MaxComputeReadbacks,
	}
}

func workspaceDeleteComputeRetryAfter(operation workspaceDeleteOperation, now time.Time) int {
	notBefore, err := time.Parse(time.RFC3339Nano, operation.ComputeReadbackNotBefore)
	if err != nil || !notBefore.After(now) {
		return 0
	}
	remaining := notBefore.Sub(now)
	seconds := int((remaining + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}
