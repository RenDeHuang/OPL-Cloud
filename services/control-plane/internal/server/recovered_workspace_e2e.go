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
	"strconv"
	"strings"
	"time"

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

type recoveredWorkspaceE2EApprovalCustomer struct {
	Email     string `json:"email"`
	AccountID string `json:"accountId"`
}

type recoveredWorkspaceE2EApprovalResources struct {
	ComputeAllocationID string `json:"computeAllocationId"`
	StorageID           string `json:"storageId"`
	AttachmentID        string `json:"attachmentId"`
	RuntimeID           string `json:"runtimeId"`
	ReceiptID           string `json:"receiptId"`
	WorkspaceAPIKeyID   string `json:"workspaceApiKeyId"`
	RuntimeServiceName  string `json:"runtimeServiceName"`
	WorkspaceURL        string `json:"workspaceUrl"`
}

type recoveredWorkspaceE2EApproval struct {
	SchemaVersion          int                                    `json:"schemaVersion"`
	ApprovalID             string                                 `json:"approvalId"`
	ExpiresAt              string                                 `json:"expiresAt"`
	Confirmation           string                                 `json:"confirmation"`
	MergedMainSHA          string                                 `json:"mergedMainSha"`
	CloudImageDigest       string                                 `json:"cloudImageDigest"`
	WorkspaceImageDigest   string                                 `json:"workspaceImageDigest"`
	RecoveryApprovalID     string                                 `json:"recoveryApprovalId"`
	RecoveryApprovalDigest string                                 `json:"recoveryApprovalDigest"`
	RecoveryBindingDigest  string                                 `json:"recoveryBindingDigest"`
	RecoveryKey            string                                 `json:"recoveryKey"`
	Customer               recoveredWorkspaceE2EApprovalCustomer  `json:"customer"`
	LaunchOperationID      string                                 `json:"launchOperationId"`
	WorkspaceID            string                                 `json:"workspaceId"`
	Resources              recoveredWorkspaceE2EApprovalResources `json:"resources"`
	ExpectedModel          string                                 `json:"expectedModel"`
	ModelRequestKey        string                                 `json:"modelRequestKey"`
	AllowedWrites          []string                               `json:"allowedWrites"`
	ForbiddenWrites        []string                               `json:"forbiddenWrites"`
}

type recoveredWorkspaceE2EAttemptRequest struct {
	Approval       recoveredWorkspaceE2EApproval `json:"approval"`
	ApprovalDigest string                        `json:"approvalDigest"`
}

func recoveredWorkspaceE2EApprovalDigest(approval recoveredWorkspaceE2EApproval) string {
	payload, err := json.Marshal(structToMap(approval))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func recoveredWorkspaceE2EAttemptBinding(approval recoveredWorkspaceE2EApproval, approvalDigest string) string {
	payload, err := json.Marshal(map[string]any{
		"approval":       structToMap(approval),
		"approvalDigest": approvalDigest,
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

func recoveredWorkspaceE2EApprovalShapeValid(approval recoveredWorkspaceE2EApproval, digest string, now time.Time) bool {
	expiresAt, err := time.Parse(time.RFC3339, approval.ExpiresAt)
	return err == nil && expiresAt.After(now.UTC()) && recoveredWorkspaceE2EApprovalBindingValid(approval, digest)
}

func recoveredWorkspaceE2EApprovalBindingValid(approval recoveredWorkspaceE2EApproval, digest string) bool {
	_, err := time.Parse(time.RFC3339, approval.ExpiresAt)
	return err == nil && approval.SchemaVersion == 1 &&
		validRecoveredWorkspaceE2EOpaque(approval.ApprovalID) && validRecoveredWorkspaceE2EOpaque(approval.RecoveryApprovalID) &&
		validRecoveredWorkspaceE2EOpaque(approval.RecoveryKey) && validRecoveredWorkspaceE2EOpaque(approval.ModelRequestKey) &&
		recoveredWorkspaceE2EOpaquePattern.MatchString(approval.ExpectedModel) && approval.Confirmation == recoveredWorkspaceE2EConfirmation &&
		computeClaimMergedSHAPattern.MatchString(approval.MergedMainSHA) && computeClaimCloudDigestPattern.MatchString(approval.CloudImageDigest) &&
		validWorkspaceImageIdentity(approval.WorkspaceImageDigest) && computeClaimApprovalDigestPattern.MatchString(approval.RecoveryApprovalDigest) &&
		computeClaimApprovalDigestPattern.MatchString(approval.RecoveryBindingDigest) &&
		computeClaimApprovalDigestPattern.MatchString(digest) && recoveredWorkspaceE2EApprovalDigest(approval) == digest &&
		approval.Customer.Email == normalizeEmail(approval.Customer.Email) && approval.Customer.Email != "" && approval.Customer.AccountID != "" &&
		approval.LaunchOperationID != "" && approval.WorkspaceID != "" &&
		equalWorkspaceComputeClaimStrings(approval.AllowedWrites, recoveredWorkspaceE2EAllowedWrites) &&
		equalWorkspaceComputeClaimStrings(approval.ForbiddenWrites, recoveredWorkspaceE2EForbiddenWrites)
}

func recoveredWorkspaceE2EResourcesMatch(approval recoveredWorkspaceE2EApproval, operation workspaceLaunchOperation, workspace map[string]any) bool {
	keyID, keyOK := positiveIntegerField(workspace, "workspaceApiKeyId")
	expectedURL := "https://" + workspaceDomain() + "/w/" + operation.WorkspaceID + "/"
	return keyOK && strconv.FormatInt(keyID, 10) == approval.Resources.WorkspaceAPIKeyID &&
		approval.Resources == (recoveredWorkspaceE2EApprovalResources{
			ComputeAllocationID: operation.ComputeID,
			StorageID:           operation.StorageID,
			AttachmentID:        operation.AttachmentID,
			RuntimeID:           operation.RuntimeID,
			ReceiptID:           operation.ReceiptID,
			WorkspaceAPIKeyID:   strconv.FormatInt(operation.WorkspaceAPIKeyID, 10),
			RuntimeServiceName:  operation.RuntimeServiceName,
			WorkspaceURL:        operation.URL,
		}) && operation.URL == expectedURL &&
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

func recoveredWorkspaceE2ERecoveryBindingMatches(approval recoveredWorkspaceE2EApproval, operation workspaceLaunchOperation) bool {
	recovery := operation.ComputeClaimApproval
	if recovery == nil || recovery.ApprovalDigest == "" || recovery.ApprovalDigest != workspaceComputeClaimApprovalDigest(*recovery) {
		return false
	}
	return recovery.SchemaVersion == 2 && recovery.ApprovalID == approval.RecoveryApprovalID &&
		recovery.ApprovalDigest == approval.RecoveryApprovalDigest && recovery.RecoveryKey == approval.RecoveryKey &&
		recovery.MergedMainSHA == approval.MergedMainSHA && recovery.CloudImageDigest == approval.CloudImageDigest &&
		recovery.WorkspaceImageDigest == approval.WorkspaceImageDigest && recovery.Customer.Email == approval.Customer.Email &&
		recovery.Customer.AccountID == approval.Customer.AccountID && recovery.Target == workspaceComputeClaimApprovalTargetFromOperation(operation) &&
		recovery.Resources == workspaceComputeClaimExpectedResources(operation, recovery.Resources.StorageState, recovery.Resources.StorageProviderResourceID)
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
	approval := request.Approval
	if !recoveredWorkspaceE2EApprovalShapeValid(approval, request.ApprovalDigest, time.Now().UTC()) ||
		approval.Customer.AccountID != accountID || approval.Customer.Email != normalizeEmail(stringValue(user["email"])) || approval.WorkspaceID != workspaceID {
		return productionE2EAttemptClaim{}, workspaceLaunchOperation{}, "recovered_workspace_e2e_approval_invalid", errors.New("recovered_workspace_e2e_approval_invalid")
	}
	row, found, err := app.tables.GetRuntimeOperation(ctx, approval.LaunchOperationID)
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
	if !recoveredWorkspaceE2ERecoveryBindingMatches(approval, operation) || !recoveredWorkspaceE2EResourcesMatch(approval, operation, workspace) {
		return productionE2EAttemptClaim{}, workspaceLaunchOperation{}, "recovered_workspace_e2e_binding_mismatch", errors.New("recovered_workspace_e2e_binding_mismatch")
	}
	binding := recoveredWorkspaceE2EAttemptBinding(approval, request.ApprovalDigest)
	if binding == "" {
		return productionE2EAttemptClaim{}, workspaceLaunchOperation{}, "recovered_workspace_e2e_approval_invalid", errors.New("recovered_workspace_e2e_approval_invalid")
	}
	return productionE2EAttemptClaim{
		ID: recoveredWorkspaceE2EAttemptID(operation), AccountID: accountID, WorkspaceID: workspaceID,
		URL: operation.URL, Binding: binding,
	}, operation, request.ApprovalDigest, nil
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
	writeJSON(w, http.StatusCreated, map[string]any{"attemptId": claim.ID, "status": "attempted", "approvalDigest": digest})
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
	approval := request.Approval
	digest := request.ApprovalDigest
	workspaceID := strings.TrimSpace(r.PathValue("workspaceId"))
	accountID := stringValue(user["accountId"])
	if !recoveredWorkspaceE2EApprovalBindingValid(approval, digest) || approval.WorkspaceID != workspaceID ||
		approval.Customer.AccountID != accountID || approval.Customer.Email != normalizeEmail(stringValue(user["email"])) {
		writeRecoveredWorkspaceE2EAttemptError(w, "recovered_workspace_e2e_approval_invalid")
		return
	}
	binding := recoveredWorkspaceE2EAttemptBinding(approval, digest)
	id := recoveredWorkspaceE2EAttemptIDFor(approval.LaunchOperationID, approval.WorkspaceID)
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
