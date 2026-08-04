package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"

	"opl-cloud/services/control-plane/internal/clients"
)

const recoveryAcceptanceCanaryErrorCode = "recovery_acceptance_canary_manual_review"

const recoveryAcceptanceCanaryConfigErrorCode = "recovery_acceptance_canary_configuration_invalid"

var recoveryAcceptanceCanaryNoncePattern = regexp.MustCompile(`^[a-f0-9]{32,128}$`)

var (
	errRecoveryAcceptanceCanaryDisabled        = errors.New("recovery_acceptance_canary_disabled")
	errRecoveryAcceptanceCanaryAccountDenied   = errors.New("recovery_acceptance_canary_account_not_allowlisted")
	errRecoveryAcceptanceCanaryApprovalInvalid = errors.New("recovery_acceptance_canary_approval_invalid")
	errRecoveryAcceptanceCanaryLaunchInvalid   = errors.New("recovery_acceptance_canary_launch_not_eligible")
	errRecoveryAcceptanceCanaryReplayConflict  = errors.New("recovery_acceptance_canary_replay_conflict")
)

type recoveryAcceptanceCanaryApproval struct {
	AccountID         string `json:"accountId"`
	LaunchOperationID string `json:"launchOperationId"`
	MergedMainSHA     string `json:"mergedMainSha"`
	CloudImageDigest  string `json:"cloudImageDigest"`
	ApprovalDigest    string `json:"approvalDigest"`
	Nonce             string `json:"nonce"`
}

type recoveryAcceptanceCanaryDigestMaterial struct {
	AccountID         string `json:"accountId"`
	LaunchOperationID string `json:"launchOperationId"`
	MergedMainSHA     string `json:"mergedMainSha"`
	CloudImageDigest  string `json:"cloudImageDigest"`
	Nonce             string `json:"nonce"`
}

func recoveryAcceptanceCanaryEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OPL_RECOVERY_ACCEPTANCE_CANARY_ENABLED"))) {
	case "1", "true":
		return true
	default:
		return false
	}
}

func recoveryAcceptanceCanaryAllowlisted(accountID string) bool {
	for _, item := range strings.Split(os.Getenv("OPL_RECOVERY_ACCEPTANCE_CANARY_ACCOUNT_IDS"), ",") {
		if strings.TrimSpace(item) == accountID {
			return true
		}
	}
	return false
}

func recoveryAcceptanceCanaryDigest(approval recoveryAcceptanceCanaryApproval) string {
	material := recoveryAcceptanceCanaryDigestMaterial{
		AccountID: approval.AccountID, LaunchOperationID: approval.LaunchOperationID,
		MergedMainSHA: approval.MergedMainSHA, CloudImageDigest: approval.CloudImageDigest, Nonce: approval.Nonce,
	}
	encoded, err := json.Marshal(material)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func parseRecoveryAcceptanceCanaryApproval(input map[string]any, operationID string) (recoveryAcceptanceCanaryApproval, error) {
	if !exactWorkspaceComputeClaimKeys(input, []string{"accountId", "launchOperationId", "mergedMainSha", "cloudImageDigest", "approvalDigest", "nonce"}) {
		return recoveryAcceptanceCanaryApproval{}, errRecoveryAcceptanceCanaryApprovalInvalid
	}
	approval := recoveryAcceptanceCanaryApproval{
		AccountID:         strings.TrimSpace(stringValue(input["accountId"])),
		LaunchOperationID: strings.TrimSpace(stringValue(input["launchOperationId"])),
		MergedMainSHA:     strings.TrimSpace(stringValue(input["mergedMainSha"])),
		CloudImageDigest:  strings.TrimSpace(stringValue(input["cloudImageDigest"])),
		ApprovalDigest:    strings.TrimSpace(stringValue(input["approvalDigest"])),
		Nonce:             strings.TrimSpace(stringValue(input["nonce"])),
	}
	if !strings.HasPrefix(approval.AccountID, "acct-") || approval.LaunchOperationID == "" ||
		!computeClaimMergedSHAPattern.MatchString(approval.MergedMainSHA) ||
		!computeClaimCloudDigestPattern.MatchString(approval.CloudImageDigest) ||
		!computeClaimApprovalDigestPattern.MatchString(approval.ApprovalDigest) ||
		!recoveryAcceptanceCanaryNoncePattern.MatchString(approval.Nonce) || approval.LaunchOperationID != operationID ||
		recoveryAcceptanceCanaryDigest(approval) != approval.ApprovalDigest {
		return recoveryAcceptanceCanaryApproval{}, errRecoveryAcceptanceCanaryApprovalInvalid
	}
	return approval, nil
}

func recoveryAcceptanceCanaryReleaseBinding() (string, string, error) {
	mergedSHA := strings.TrimSpace(os.Getenv("OPL_RELEASE_SHA"))
	cloudDigest := deployedImageDigest(os.Getenv("OPL_CLOUD_IMAGE"))
	if !computeClaimMergedSHAPattern.MatchString(mergedSHA) || !computeClaimCloudDigestPattern.MatchString(cloudDigest) {
		return "", "", errRecoveryAcceptanceCanaryApprovalInvalid
	}
	return mergedSHA, cloudDigest, nil
}

func recoveryAcceptanceCanaryMutationEvidenceConfirmed(evidence clients.ComputeClaimMutationEvidence, maximum int) bool {
	return evidence.Attempted > 0 && evidence.Attempted <= maximum && evidence.Confirmed == evidence.Attempted && evidence.Unknown == 0 && len(evidence.Missing) == 0
}

func recoveryAcceptanceCanaryLaunchEligible(app *controlPlaneServer, operation workspaceLaunchOperation) bool {
	if operation.Status != "preparing" || operation.Phase != "storage_fulfilling" || operation.ChargeAttempted == false || operation.ChargeConfirmation == nil ||
		operation.RecoveryPlan != nil || operation.RecoveryExecution != nil || operation.RecoveryCanaryDigest != "" ||
		operation.ComputeClaimProof == nil || operation.ComputeClaimProof.SchemaVersion != 1 || !operation.ComputeClaimProof.Eligible || operation.ComputeClaimProof.Reason != "none" ||
		operation.ComputeClaimProof.LaunchOperationID != operation.ID || operation.ComputeClaimProof.AccountID != operation.AccountID || operation.ComputeClaimProof.WorkspaceID != operation.WorkspaceID ||
		operation.ComputeClaimProof.ComputeAllocationID != operation.ComputeID || operation.ComputeClaimProof.StorageVolumeID != operation.StorageID || operation.ComputeClaimProof.StorageState != "storage_not_started" || operation.ComputeClaimProof.StorageProviderResourceID != "" ||
		operation.ComputeClaimProof.NodeOwnershipState != "target_owned" || operation.ComputeClaimProof.CVMOwnershipState != "target_owned" || operation.ComputeClaimProof.FailureStage != "" || operation.ComputeClaimProof.ProviderErrorClass != "" ||
		operation.ComputeClaimProof.Evidence == nil || !recoveryAcceptanceCanaryMutationEvidenceConfirmed(operation.ComputeClaimProof.Evidence.CVM, 5) || !recoveryAcceptanceCanaryMutationEvidenceConfirmed(operation.ComputeClaimProof.Evidence.Node, 1) ||
		operation.ComputeClaimProof.Sub2APIMutationCount != 0 || operation.ComputeClaimProof.TencentMutationCount != operation.ComputeClaimProof.Evidence.CVM.Attempted || operation.ComputeClaimProof.KubernetesMutationCount != operation.ComputeClaimProof.Evidence.Node.Attempted {
		return false
	}
	for _, stage := range workspaceLaunchContinuationStages {
		budget := operation.ContinuationAttemptBudgets[stage]
		if budget.Max != workspaceLaunchStageMax || budget.Attempted != 0 || budget.Confirmed != 0 || budget.Unknown != 0 {
			return false
		}
	}
	if _, exists := app.getStorage(operation.StorageID); exists {
		return false
	}
	return true
}

func recoveryAcceptanceCanaryResponse(operation workspaceLaunchOperation) map[string]any {
	return map[string]any{
		"schemaVersion":              1,
		"operationMode":              "recovery_acceptance_canary",
		"status":                     operation.Status,
		"phase":                      operation.Phase,
		"operationId":                operation.ID,
		"workspaceId":                operation.WorkspaceID,
		"approvalDigest":             operation.RecoveryCanaryDigest,
		"errorCode":                  operation.ErrorCode,
		"controlPlaneMutationCounts": map[string]int{"database": 1, "sub2api": 0, "tencent": 0, "kubernetes": 0},
	}
}

// configuredRecoveryAcceptanceCanary reads the process-local canary binding. The
// approval may be supplied as the dedicated JSON secret (the workflow envelope
// fields are ignored here); the persisted operation stores only its digest.
func configuredRecoveryAcceptanceCanary(operationID string) (recoveryAcceptanceCanaryApproval, bool, error) {
	configuredLaunchID := strings.TrimSpace(os.Getenv("OPL_RECOVERY_ACCEPTANCE_CANARY_LAUNCH_OPERATION_ID"))
	configuredAccountID := strings.TrimSpace(os.Getenv("OPL_RECOVERY_ACCEPTANCE_CANARY_ACCOUNT_ID"))
	raw := strings.TrimSpace(os.Getenv("OPL_RECOVERY_ACCEPTANCE_CANARY_APPROVAL_JSON"))
	if raw != "" {
		var envelope map[string]any
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			return recoveryAcceptanceCanaryApproval{}, configuredLaunchID == operationID, errRecoveryAcceptanceCanaryApprovalInvalid
		}
		input := make(map[string]any, 6)
		for _, key := range []string{"accountId", "launchOperationId", "mergedMainSha", "cloudImageDigest", "approvalDigest", "nonce"} {
			if value, ok := envelope[key]; ok {
				input[key] = value
			}
		}
		approval, err := parseRecoveryAcceptanceCanaryApproval(input, operationID)
		if err != nil {
			return recoveryAcceptanceCanaryApproval{}, configuredLaunchID == operationID || configuredLaunchID == "", err
		}
		if configuredLaunchID != "" && configuredLaunchID != approval.LaunchOperationID {
			return recoveryAcceptanceCanaryApproval{}, false, nil
		}
		if configuredAccountID != "" && configuredAccountID != approval.AccountID {
			return recoveryAcceptanceCanaryApproval{}, false, nil
		}
		return approval, approval.LaunchOperationID == operationID, nil
	}
	if configuredLaunchID == "" || configuredLaunchID != operationID || configuredAccountID == "" {
		return recoveryAcceptanceCanaryApproval{}, false, nil
	}
	mergedSHA, cloudDigest, err := recoveryAcceptanceCanaryReleaseBinding()
	if err != nil {
		return recoveryAcceptanceCanaryApproval{}, true, err
	}
	approval := recoveryAcceptanceCanaryApproval{
		AccountID: configuredAccountID, LaunchOperationID: configuredLaunchID,
		MergedMainSHA: mergedSHA, CloudImageDigest: cloudDigest,
		ApprovalDigest: strings.TrimSpace(os.Getenv("OPL_RECOVERY_ACCEPTANCE_CANARY_APPROVAL_DIGEST")),
		Nonce:          strings.TrimSpace(os.Getenv("OPL_RECOVERY_ACCEPTANCE_CANARY_NONCE")),
	}
	if approval.ApprovalDigest == "" || approval.Nonce == "" || recoveryAcceptanceCanaryDigest(approval) != approval.ApprovalDigest || !recoveryAcceptanceCanaryNoncePattern.MatchString(approval.Nonce) {
		return recoveryAcceptanceCanaryApproval{}, true, errRecoveryAcceptanceCanaryApprovalInvalid
	}
	return approval, true, nil
}

func (app *controlPlaneServer) persistRecoveryAcceptanceCanaryManualReview(ctx context.Context, operation *workspaceLaunchOperation, approvalDigest, errorCode string) error {
	if approvalDigest != "" && !computeClaimApprovalDigestPattern.MatchString(approvalDigest) {
		return errRecoveryAcceptanceCanaryApprovalInvalid
	}
	operation.Status, operation.Phase, operation.ErrorCode = "manual_review", "storage_fulfilling", errorCode
	operation.RecoveryCanaryDigest = approvalDigest
	releaseWorkspaceLaunchLease(operation)
	return app.persistWorkspaceLaunch(ctx, operation)
}

// enterRecoveryAcceptanceCanaryAtStorageBoundary is called immediately after
// authoritative compute/Node confirmation and before the storage budget can be
// reserved. It is process-configured for exactly one allowlisted launch, so a
// normal launch remains unchanged while the canary cannot race the worker.
func (app *controlPlaneServer) enterRecoveryAcceptanceCanaryAtStorageBoundary(ctx context.Context, operation *workspaceLaunchOperation) (bool, error) {
	if !recoveryAcceptanceCanaryEnabled() {
		return false, nil
	}
	approval, targeted, err := configuredRecoveryAcceptanceCanary(operation.ID)
	if !targeted {
		return false, nil
	}
	if err != nil {
		if persistErr := app.persistRecoveryAcceptanceCanaryManualReview(ctx, operation, "", recoveryAcceptanceCanaryConfigErrorCode); persistErr != nil {
			return true, persistErr
		}
		return true, nil
	}
	if !recoveryAcceptanceCanaryAllowlisted(approval.AccountID) || approval.AccountID != operation.AccountID {
		if persistErr := app.persistRecoveryAcceptanceCanaryManualReview(ctx, operation, "", recoveryAcceptanceCanaryConfigErrorCode); persistErr != nil {
			return true, persistErr
		}
		return true, nil
	}
	mergedSHA, cloudDigest, err := recoveryAcceptanceCanaryReleaseBinding()
	if err != nil || approval.MergedMainSHA != mergedSHA || approval.CloudImageDigest != cloudDigest {
		if persistErr := app.persistRecoveryAcceptanceCanaryManualReview(ctx, operation, "", recoveryAcceptanceCanaryConfigErrorCode); persistErr != nil {
			return true, persistErr
		}
		return true, nil
	}
	priorStatus, priorPhase, priorError := operation.Status, operation.Phase, operation.ErrorCode
	operation.Status, operation.Phase, operation.ErrorCode = "preparing", "storage_fulfilling", ""
	if !recoveryAcceptanceCanaryLaunchEligible(app, *operation) {
		operation.Status, operation.Phase, operation.ErrorCode = priorStatus, priorPhase, priorError
		if persistErr := app.persistRecoveryAcceptanceCanaryManualReview(ctx, operation, "", recoveryAcceptanceCanaryConfigErrorCode); persistErr != nil {
			return true, persistErr
		}
		return true, nil
	}
	if err := app.persistRecoveryAcceptanceCanaryManualReview(ctx, operation, approval.ApprovalDigest, recoveryAcceptanceCanaryErrorCode); err != nil {
		return true, err
	}
	return true, nil
}

func (app *controlPlaneServer) executeRecoveryAcceptanceCanary(ctx context.Context, operationID string, approval recoveryAcceptanceCanaryApproval) (map[string]any, error) {
	if !recoveryAcceptanceCanaryEnabled() {
		return nil, errRecoveryAcceptanceCanaryDisabled
	}
	if !recoveryAcceptanceCanaryAllowlisted(approval.AccountID) {
		return nil, errRecoveryAcceptanceCanaryAccountDenied
	}
	mergedSHA, cloudDigest, err := recoveryAcceptanceCanaryReleaseBinding()
	if err != nil || approval.MergedMainSHA != mergedSHA || approval.CloudImageDigest != cloudDigest {
		return nil, errRecoveryAcceptanceCanaryApprovalInvalid
	}
	operation, ok, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.AccountID != approval.AccountID {
		return nil, errRecoveryAcceptanceCanaryLaunchInvalid
	}
	if operation.RecoveryCanaryDigest != "" {
		if operation.RecoveryCanaryDigest != approval.ApprovalDigest || operation.Status != "manual_review" || operation.Phase != "storage_fulfilling" {
			return nil, errRecoveryAcceptanceCanaryReplayConflict
		}
		return recoveryAcceptanceCanaryResponse(operation), nil
	}
	unlock := app.lockResource("workspace-launch", operation.AccountID)
	defer unlock()
	operation, ok, err = app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.AccountID != approval.AccountID {
		return nil, errRecoveryAcceptanceCanaryLaunchInvalid
	}
	if operation.RecoveryCanaryDigest != "" {
		if operation.RecoveryCanaryDigest != approval.ApprovalDigest || operation.Status != "manual_review" || operation.Phase != "storage_fulfilling" {
			return nil, errRecoveryAcceptanceCanaryReplayConflict
		}
		return recoveryAcceptanceCanaryResponse(operation), nil
	}
	if !recoveryAcceptanceCanaryLaunchEligible(app, operation) {
		return nil, errRecoveryAcceptanceCanaryLaunchInvalid
	}
	operation.Status, operation.Phase, operation.ErrorCode = "manual_review", "storage_fulfilling", recoveryAcceptanceCanaryErrorCode
	operation.RecoveryCanaryDigest = approval.ApprovalDigest
	releaseWorkspaceLaunchLease(&operation)
	if err := app.persistWorkspaceLaunch(ctx, &operation); err != nil {
		return nil, err
	}
	return recoveryAcceptanceCanaryResponse(operation), nil
}
