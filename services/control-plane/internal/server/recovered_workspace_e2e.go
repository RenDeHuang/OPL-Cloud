package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"

	"opl-cloud/services/control-plane/internal/controlplane"
)

const recoveredWorkspaceE2EConfirmation = "CONFIRM_SINGLE_MODEL_REQUEST_FOR_RECOVERED_WORKSPACE"

var recoveredWorkspaceE2EAllowedWrites = []string{
	"control_plane_e2e_attempt_reservation",
	"single_workspace_model_request",
	"control_plane_e2e_attempt_completion",
}

var recoveredWorkspaceE2EForbiddenWrites = []string{
	"launch", "debit", "recharge", "refund", "scale", "create_cvm", "create_cbs", "tencent", "kubernetes",
}

var recoveredWorkspaceE2EOpaquePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{2,127}$`)

type recoveredWorkspaceE2EAttemptRequest struct {
	SchemaVersion     int    `json:"schemaVersion"`
	ApprovalID        string `json:"approvalId"`
	LaunchOperationID string `json:"launchOperationId"`
	PlanID            string `json:"planId"`
	PlanDigest        string `json:"planDigest"`
	Decision          string `json:"decision"`
	Confirmation      string `json:"confirmation"`
	ExpectedModel     string `json:"expectedModel"`
	ModelRequestKey   string `json:"modelRequestKey"`
}

func recoveredWorkspaceE2ERequestDigest(request recoveredWorkspaceE2EAttemptRequest) string {
	payload, err := json.Marshal(structToMap(request))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func recoveredWorkspaceE2EAttemptBinding(request recoveredWorkspaceE2EAttemptRequest, requestDigest string) string {
	payload, err := json.Marshal(map[string]any{
		"request":       structToMap(request),
		"requestDigest": requestDigest,
	})
	if err != nil {
		return ""
	}
	return string(payload)
}

func recoveredWorkspaceE2EAttemptID(operation workspaceLaunchOperation) string {
	return recoveredWorkspaceE2EAttemptIDFor(operation.ID, operation.WorkspaceID)
}

func recoveredWorkspaceE2EAttemptIDFor(launchOperationID, workspaceID string) string {
	return "production-e2e-recovered-" + stableID("recovered-workspace-e2e-v1", launchOperationID, workspaceID)[:18]
}

func decodeRecoveredWorkspaceE2EAttemptRequest(r *http.Request) (recoveredWorkspaceE2EAttemptRequest, error) {
	var request recoveredWorkspaceE2EAttemptRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return recoveredWorkspaceE2EAttemptRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return recoveredWorkspaceE2EAttemptRequest{}, errors.New("invalid_json_body")
	}
	return request, nil
}

func validRecoveredWorkspaceE2EOpaque(value string) bool {
	value = strings.TrimSpace(value)
	return recoveredWorkspaceE2EOpaquePattern.MatchString(value) &&
		!strings.Contains(value, "password") && !strings.Contains(value, "secret") &&
		!strings.Contains(value, "token") && !strings.Contains(value, "credential")
}

func recoveredWorkspaceE2ERequestValid(request recoveredWorkspaceE2EAttemptRequest) bool {
	return request.SchemaVersion == 2 && validRecoveredWorkspaceE2EOpaque(request.ApprovalID) &&
		validRecoveredWorkspaceE2EOpaque(request.LaunchOperationID) && validRecoveredWorkspaceE2EOpaque(request.PlanID) &&
		computeClaimApprovalDigestPattern.MatchString(request.PlanDigest) && request.Decision == "continue" &&
		request.Confirmation == recoveredWorkspaceE2EConfirmation && recoveredWorkspaceE2EOpaquePattern.MatchString(request.ExpectedModel) &&
		validRecoveredWorkspaceE2EOpaque(request.ModelRequestKey)
}

func recoveredWorkspaceE2EResourcesMatch(operation workspaceLaunchOperation, workspace map[string]any) bool {
	_, keyOK := positiveIntegerField(workspace, "workspaceApiKeyId")
	expectedURL := "https://" + workspaceDomain() + "/w/" + operation.WorkspaceID + "/"
	return keyOK && operation.ComputeID != "" && operation.StorageID != "" && operation.AttachmentID != "" && operation.RuntimeID != "" &&
		operation.ReceiptID != "" && operation.WorkspaceAPIKeyID > 0 && operation.RuntimeServiceName != "" && operation.URL == expectedURL &&
		firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"])) == operation.AccountID &&
		stringValue(workspace["ownerUserId"]) == operation.OwnerUserID &&
		stringValue(workspace["id"]) == operation.WorkspaceID &&
		firstNonEmpty(stringValue(workspace["currentComputeAllocationId"]), stringValue(workspace["computeAllocationId"])) == operation.ComputeID &&
		stringValue(workspace["storageId"]) == operation.StorageID &&
		firstNonEmpty(stringValue(workspace["currentAttachmentId"]), stringValue(workspace["attachmentId"])) == operation.AttachmentID &&
		stringValue(workspace["runtimeId"]) == operation.RuntimeID &&
		firstNonEmpty(stringValue(workspace["runtimeServiceName"]), stringValue(nested(workspace, "runtime", "serviceName"))) == operation.RuntimeServiceName &&
		stringValue(workspace["url"]) == operation.URL &&
		firstNonEmpty(stringValue(workspace["purchaseReceiptId"]), stringValue(workspace["receiptId"])) == operation.ReceiptID
}

func recoveredWorkspaceE2EPlanExecutionMatches(request recoveredWorkspaceE2EAttemptRequest, operation workspaceLaunchOperation) bool {
	plan, execution := operation.RecoveryPlan, operation.RecoveryExecution
	return plan != nil && execution != nil && plan.PlanID == request.PlanID && plan.PlanDigest == request.PlanDigest &&
		plan.Status == "completed" && plan.OperationID == operation.ID && plan.URL == operation.URL && plan.ReceiptID == operation.ReceiptID &&
		execution.PlanID == request.PlanID && execution.PlanDigest == request.PlanDigest && execution.Decision == request.Decision &&
		execution.Status == "completed" && execution.ExecutionID != "" && execution.RunIdentity != "" && execution.ApprovalDigest != "" &&
		execution.CompletedAt != "" && execution.LeaseToken == "" && execution.LeaseExpiresAt == ""
}

func (app *controlPlaneServer) recoveredWorkspaceE2EAttemptClaim(ctx context.Context, r *http.Request, workspaceID string, request recoveredWorkspaceE2EAttemptRequest) (productionE2EAttemptClaim, workspaceLaunchOperation, string, error) {
	user, ok := app.sessionUserContext(r)
	if !ok {
		return productionE2EAttemptClaim{}, workspaceLaunchOperation{}, "not_authenticated", errors.New("not_authenticated")
	}
	workspace, found, err := app.tables.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return productionE2EAttemptClaim{}, workspaceLaunchOperation{}, "state_read_failed", err
	}
	accountID, ownerID := stringValue(user["accountId"]), stringValue(user["id"])
	if !found || accountID == "" || firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"])) != accountID ||
		firstNonEmpty(stringValue(workspace["ownerUserId"]), stringValue(workspace["ownerId"])) != ownerID {
		return productionE2EAttemptClaim{}, workspaceLaunchOperation{}, "workspace_owner_required", errors.New("workspace_owner_required")
	}
	if !recoveredWorkspaceE2ERequestValid(request) {
		return productionE2EAttemptClaim{}, workspaceLaunchOperation{}, "recovered_workspace_e2e_approval_invalid", errors.New("recovered_workspace_e2e_approval_invalid")
	}
	row, found, err := app.tables.GetRuntimeOperation(ctx, request.LaunchOperationID)
	if err != nil {
		return productionE2EAttemptClaim{}, workspaceLaunchOperation{}, "state_read_failed", err
	}
	if !found || stringValue(row["action"]) != workspaceLaunchAction {
		return productionE2EAttemptClaim{}, workspaceLaunchOperation{}, "recovered_workspace_e2e_resource_closure_required", errors.New("recovered_workspace_e2e_resource_closure_required")
	}
	operation, err := decodeWorkspaceLaunchOperation(row)
	if err != nil || operation.Status != "succeeded" || operation.Phase != "succeeded" || operation.AccountID != accountID ||
		operation.OwnerUserID != ownerID || operation.WorkspaceID != workspaceID || app.workspaceResponse(cloneMap(workspace))["openable"] != true {
		return productionE2EAttemptClaim{}, workspaceLaunchOperation{}, "recovered_workspace_e2e_resource_closure_required", errors.New("recovered_workspace_e2e_resource_closure_required")
	}
	if !recoveredWorkspaceE2EPlanExecutionMatches(request, operation) || !recoveredWorkspaceE2EResourcesMatch(operation, workspace) {
		return productionE2EAttemptClaim{}, workspaceLaunchOperation{}, "recovered_workspace_e2e_binding_mismatch", errors.New("recovered_workspace_e2e_binding_mismatch")
	}
	digest := recoveredWorkspaceE2ERequestDigest(request)
	binding := recoveredWorkspaceE2EAttemptBinding(request, digest)
	if binding == "" {
		return productionE2EAttemptClaim{}, workspaceLaunchOperation{}, "recovered_workspace_e2e_approval_invalid", errors.New("recovered_workspace_e2e_approval_invalid")
	}
	return productionE2EAttemptClaim{
		ID: recoveredWorkspaceE2EAttemptID(operation), AccountID: accountID, WorkspaceID: workspaceID,
		URL: operation.URL, Binding: binding,
	}, operation, digest, nil
}

func writeRecoveredWorkspaceE2EAttemptError(w http.ResponseWriter, code string) {
	switch code {
	case "not_authenticated":
		writeError(w, http.StatusUnauthorized, code)
	case "workspace_owner_required":
		writeError(w, http.StatusForbidden, code)
	case "recovered_workspace_e2e_approval_invalid":
		writeError(w, http.StatusBadRequest, code)
	case "recovered_workspace_e2e_resource_closure_required", "recovered_workspace_e2e_binding_mismatch", "model_result_unknown":
		writeError(w, http.StatusConflict, code)
	default:
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
	}
}

func (app *controlPlaneServer) reserveRecoveredWorkspaceE2EAttempt(service *controlplane.Service, w http.ResponseWriter, r *http.Request) {
	request, err := decodeRecoveredWorkspaceE2EAttemptRequest(r)
	if err != nil {
		writeRecoveredWorkspaceE2EAttemptError(w, "recovered_workspace_e2e_approval_invalid")
		return
	}
	claim, operation, digest, err := app.recoveredWorkspaceE2EAttemptClaim(r.Context(), r, strings.TrimSpace(r.PathValue("workspaceId")), request)
	if err != nil {
		writeRecoveredWorkspaceE2EAttemptError(w, digest)
		return
	}
	if service == nil {
		writeRecoveredWorkspaceE2EAttemptError(w, "recovered_workspace_e2e_resource_closure_required")
		return
	}
	input := workspaceActivationTruthInputFromLaunch(operation)
	truth, truthErr := service.WorkspaceActivationTruth(r.Context(), input)
	if truthErr != nil || !workspaceActivationTruthMatchesLaunch(truth, input) {
		writeRecoveredWorkspaceE2EAttemptError(w, "recovered_workspace_e2e_resource_closure_required")
		return
	}
	if _, err := app.tables.ReserveProductionE2EAttempt(r.Context(), claim); errors.Is(err, errProductionE2EAttemptAlreadyExists) {
		writeRecoveredWorkspaceE2EAttemptError(w, "model_result_unknown")
		return
	} else if err != nil {
		writeRecoveredWorkspaceE2EAttemptError(w, "state_persist_failed")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"attemptId": claim.ID, "status": "attempted", "approvalDigest": digest,
		"executionId": operation.RecoveryExecution.ExecutionID, "runId": operation.RecoveryExecution.RunIdentity,
	})
}

func (app *controlPlaneServer) completeRecoveredWorkspaceE2EAttempt(w http.ResponseWriter, r *http.Request) {
	request, err := decodeRecoveredWorkspaceE2EAttemptRequest(r)
	if err != nil {
		writeRecoveredWorkspaceE2EAttemptError(w, "recovered_workspace_e2e_approval_invalid")
		return
	}
	user, ok := app.sessionUserContext(r)
	if !ok {
		writeRecoveredWorkspaceE2EAttemptError(w, "not_authenticated")
		return
	}
	digest := recoveredWorkspaceE2ERequestDigest(request)
	workspaceID := strings.TrimSpace(r.PathValue("workspaceId"))
	accountID := stringValue(user["accountId"])
	if !recoveredWorkspaceE2ERequestValid(request) || accountID == "" {
		writeRecoveredWorkspaceE2EAttemptError(w, "recovered_workspace_e2e_approval_invalid")
		return
	}
	binding := recoveredWorkspaceE2EAttemptBinding(request, digest)
	id := recoveredWorkspaceE2EAttemptIDFor(request.LaunchOperationID, workspaceID)
	record, found, err := app.tables.GetProductionE2EAttempt(r.Context(), id)
	if err != nil {
		writeRecoveredWorkspaceE2EAttemptError(w, "state_persist_failed")
		return
	}
	if !found || stringValue(record["accountId"]) != accountID || stringValue(record["workspaceId"]) != workspaceID ||
		stringValue(record["reason"]) != recoveredWorkspaceE2EAttemptReason || stringValue(record["result"]) != binding {
		writeRecoveredWorkspaceE2EAttemptError(w, "model_result_unknown")
		return
	}
	record, err = app.tables.CompleteProductionE2EAttempt(r.Context(), id, binding)
	if errors.Is(err, errProductionE2EAttemptNotFound) || errors.Is(err, errProductionE2EAttemptBindingMismatch) {
		writeRecoveredWorkspaceE2EAttemptError(w, "model_result_unknown")
		return
	}
	if err != nil {
		writeRecoveredWorkspaceE2EAttemptError(w, "state_persist_failed")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"attemptId": id, "status": stringValue(record["status"]), "approvalDigest": digest})
}
