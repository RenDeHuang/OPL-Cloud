package server

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"

	"opl-cloud/services/control-plane/internal/clients"
)

var (
	errInvalidWorkspaceLaunchOperation = errors.New("invalid_workspace_launch_operation")
	errWorkspaceLaunchInProgress       = errors.New("workspace_launch_in_progress")
	errWorkspaceLaunchCASConflict      = errors.New("workspace_launch_cas_conflict")
	errWorkspaceCodexGroupUnavailable  = errors.New("apiKey.codexGroupUnavailable")
)

const (
	workspaceLaunchAction = "workspace.launch.v2"

	workspaceKeyCodexGroupBound = "codex_group_bound"
)

var workspaceImageDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type workspaceLaunchDescriptor struct {
	OperationID          string
	RequestHash          string
	WorkspaceID          string
	WorkspaceImageDigest string
}

func newWorkspaceLaunchDescriptor(accountID, ownerUserID, name, packageID string, storageGB int, autoRenew bool, priceVersion, key string) (workspaceLaunchDescriptor, error) {
	operationID := workspaceLaunchOperationID(accountID, key)
	workspaceID := "ws-" + stableID("workspace-launch-v2", accountID, operationID)[:18]
	imageDigest := currentWorkspaceImageDigest()
	if operationID == "" || imageDigest == "" {
		return workspaceLaunchDescriptor{}, errInvalidWorkspaceLaunchOperation
	}
	return workspaceLaunchDescriptor{
		OperationID: operationID,
		RequestHash: stableID("workspace-launch-v2", accountID, ownerUserID, name, packageID, strconv.Itoa(storageGB), strconv.FormatBool(autoRenew), priceVersion, imageDigest),
		WorkspaceID: workspaceID, WorkspaceImageDigest: imageDigest,
	}, nil
}

func workspaceLaunchOperationID(accountID, key string) string {
	return "workspace-launch-" + stableID(accountID, key)[:18]
}

func currentWorkspaceImageDigest() string {
	value := strings.TrimSpace(os.Getenv("OPL_WORKSPACE_IMAGE"))
	repository, digest, ok := strings.Cut(value, "@")
	if ok && strings.TrimSpace(repository) != "" && !strings.Contains(repository, "@") && workspaceImageDigestPattern.MatchString(digest) {
		return value
	}
	return ""
}

func isWorkspaceLaunchAction(action string) bool {
	return action == workspaceLaunchAction || action == "workspace.launch"
}

func terminalWorkspaceLaunchStatus(status string) bool {
	switch status {
	case "succeeded", "failed", "refunded":
		return true
	default:
		return false
	}
}

func workspaceLaunchHasAcceptanceBCapacitySlot(row map[string]any) bool {
	var result struct {
		AcceptanceBCapacitySlot bool `json:"acceptanceBCapacitySlot"`
	}
	return json.Unmarshal([]byte(stringValue(row["result"])), &result) == nil && result.AcceptanceBCapacitySlot
}

func workspaceBillingStateMatchesLaunch(workspace, expected map[string]any) bool {
	currentJSON, currentErr := encodeWorkspaceBillingState(workspace)
	expectedJSON, expectedErr := encodeWorkspaceBillingState(expected)
	return currentErr == nil && expectedErr == nil && currentJSON == expectedJSON
}

func workspaceLaunchResponse(row map[string]any) (map[string]any, error) {
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		return nil, err
	}
	return workspaceLaunchReconcileResponse(operation, row)
}

func workspaceLaunchReconcileResponse(operation workspaceLaunchReconcileOperation, row map[string]any) (map[string]any, error) {
	if operation.SchemaVersion != workspaceLaunchReconcileSchemaVersion || operation.raw == nil {
		return nil, errInvalidWorkspaceLaunchOperation
	}
	workspaceAPIKeyID := ""
	if value := operation.int64Fact("workspaceApiKeyId"); value > 0 {
		workspaceAPIKeyID = strconv.FormatInt(value, 10)
	}
	response := map[string]any{
		"operationId": operation.ID, "schemaVersion": operation.SchemaVersion, "version": operation.Version,
		"status": operation.Status, "stage": operation.Stage, "phase": operation.Stage,
		"accountId": operation.stringFact("accountId"), "workspaceId": operation.stringFact("workspaceId"),
		"name": operation.stringFact("name"), "packageId": operation.stringFact("packageId"), "sizeGb": operation.intFact("sizeGb"),
		"autoRenew": operation.boolFact("autoRenew"), "priceVersion": operation.stringFact("priceVersion"),
		"currency": pricingCurrency, "totalChargeUsdMicros": operation.int64Fact("totalChargeUsdMicros"),
		"computeAllocationId": operation.stringFact("computeAllocationId"), "storageId": operation.stringFact("storageId"),
		"attachmentId": operation.stringFact("attachmentId"), "workspaceApiKeyId": workspaceAPIKeyID,
		"workspaceKeyStatus": operation.stringFact("workspaceKeyStatus"), "workspaceKeyFingerprint": operation.stringFact("workspaceKeyFingerprint"),
		"runtimeServiceName": operation.stringFact("runtimeServiceName"), "url": operation.stringFact("url"), "receiptId": operation.stringFact("receiptId"),
		"continuationAttemptBudgets": operation.Attempts,
	}
	if operation.Status == "manual_review" {
		response["failureStage"] = operation.Stage
	}
	if operation.ResumeAuthorization != nil {
		response["resumeAuthorization"] = operation.ResumeAuthorization
		response["resumeAuthorizationConsumedAt"] = operation.ResumeAuthorizationConsumedAt
	}
	if row != nil {
		response["createdAt"], response["updatedAt"] = row["createdAt"], row["updatedAt"]
	} else {
		response["createdAt"] = operation.CreatedAt
	}
	return response, nil
}

func workspaceLaunchReconcileRequestMatches(operation workspaceLaunchReconcileOperation, accountID, ownerUserID, name, packageID string, storageGB int, autoRenew bool) bool {
	return operation.stringFact("accountId") == accountID && operation.stringFact("ownerUserId") == ownerUserID &&
		operation.stringFact("name") == name && operation.stringFact("packageId") == packageID &&
		operation.intFact("sizeGb") == storageGB && operation.boolFact("autoRenew") == autoRenew
}

func workspaceLaunchPreflightConfirmed(input clients.WorkspaceLaunchPreflightInput, result clients.WorkspaceLaunchPreflight) bool {
	return result.SchemaVersion == clients.WorkspaceLaunchFabricSchemaVersion && result.Available && result.Reason == "none" &&
		result.LaunchOperationID == input.LaunchOperationID && result.RequestHash == input.RequestHash &&
		strings.TrimSpace(result.ProviderProfileRef) != "" && strings.TrimSpace(result.BindingRef) != ""
}
