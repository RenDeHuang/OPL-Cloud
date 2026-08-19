package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

var errWorkspaceLaunchRepairNotEligible = errors.New("workspace_launch_repair_not_eligible")

var workspaceLaunchRuntimeRepairStableFacts = map[string]map[string]struct{}{
	"key": {
		"workspaceApiKeyId": {}, "workspaceKeyGroupId": {}, "workspaceKeyFingerprint": {},
	},
	"debit": {
		"chargeAttempted": {}, "chargeConfirmation": {}, "preChargeBalanceUsdMicros": {},
		"postChargeBalanceUsdMicros": {}, "postChargeBalanceKnown": {},
	},
	"ensure_compute_allocation": {"computeAllocationId": {}, "computeBindingRef": {}},
	"storage":                   {"storageId": {}, "storageBindingRef": {}},
	"attachment":                {"attachmentId": {}, "attachmentBindingRef": {}},
	"secret": {
		"gatewaySecretRef": {}, "gatewaySecretVersion": {}, "secretBindingRef": {}, "workspaceKeyStatus": {},
	},
}

func workspaceLaunchRuntimeRepairEligible(operation workspaceLaunchReconcileOperation) bool {
	if operation.Status != "manual_review" || operation.Stage != "runtime" || !operation.boolFact("resourceBillingEnabled") {
		return false
	}
	for _, stage := range []string{"key", "debit", "ensure_compute_allocation", "storage", "attachment", "secret"} {
		attempt, observation := operation.Attempts[stage], operation.Observations[stage]
		if attempt.Max != 1 || attempt.Attempted != 1 || attempt.Confirmed != 1 || attempt.Unknown != 0 || attempt.Status != "confirmed" ||
			attempt.IdempotencyKey != workspaceLaunchStageIdempotencyKey(operationWithStage(operation, stage), 1) || observation.State != workspaceLaunchStageReady {
			return false
		}
		encodedFacts, err := validateWorkspaceLaunchStageFacts(stage, observation.Facts, false)
		if err != nil {
			return false
		}
		for field := range workspaceLaunchRuntimeRepairStableFacts[stage] {
			encoded, exists := encodedFacts[field]
			if !exists {
				return false
			}
			if string(encoded) != string(operation.raw[field]) {
				return false
			}
		}
	}
	runtimeAttempt, runtimeObservation := operation.Attempts["runtime"], operation.Observations["runtime"]
	if runtimeAttempt.Max != 1 || runtimeAttempt.Attempted != 1 || runtimeAttempt.Confirmed != 0 || runtimeAttempt.Unknown != 1 ||
		runtimeAttempt.Status != "unknown" || runtimeAttempt.IdempotencyKey != workspaceLaunchStageIdempotencyKey(operationWithStage(operation, "runtime"), 1) ||
		runtimeObservation.State != workspaceLaunchStageUnknown {
		return false
	}
	for _, stage := range []string{"activation", "receipt"} {
		attempt := operation.Attempts[stage]
		if attempt.Attempted != 0 || attempt.Confirmed != 0 || attempt.Unknown != 0 || attempt.Status != "" {
			return false
		}
		if observation, exists := operation.Observations[stage]; exists && observation.State != "" {
			return false
		}
	}
	if continuation, exists := operation.FreshContinuationAuthorizations["runtime"]; exists && continuation.Status != "failed" {
		return false
	}
	return true
}

// repairWorkspaceLaunchRuntime replaces only the failed Runtime of an already
// paid Launch. The original Key, debit, Compute, Storage, Attachment and Secret
// identities are read from the persisted Launch and cannot be supplied by the
// operator.
func (app *controlPlaneServer) repairWorkspaceLaunchRuntime(ctx context.Context, service *controlplane.Service, operationID string, launchVersion int, authorizationID, authorizedBy, reason, imageDigest string) (workspaceLaunchReconcileOperation, error) {
	if app == nil || service == nil || operationID == "" || launchVersion <= 0 || authorizationID == "" || strings.TrimSpace(authorizedBy) == "" || reason == "" || imageDigest == "" {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchRepairNotEligible
	}
	unlock := app.lockResource("workspace-launch-repair", operationID)
	defer unlock()
	row, found, err := app.tables.GetRuntimeOperation(ctx, operationID)
	if err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	if !found {
		return workspaceLaunchReconcileOperation{}, errBillingReviewNotFound
	}
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	if operation.RuntimeRepair != nil && operation.RuntimeRepair.AuthorizationID == authorizationID && operation.RuntimeRepair.AuthorizedBy == strings.TrimSpace(authorizedBy) &&
		operation.RuntimeRepair.LaunchVersion == launchVersion && operation.RuntimeRepair.Reason == reason && operation.RuntimeRepair.ImageDigest == imageDigest {
		if operation.Status == "pending" && (operation.Stage == "activation" || operation.Stage == "receipt") {
			return reconcileWorkspaceLaunchRuntimeRepairTail(ctx, app.workspaceLaunchReconciler(service, clients.SessionDelegatedCredential{}, 0), operation)
		}
		return operation, nil
	}
	if operation.Version != launchVersion || !workspaceLaunchRuntimeRepairEligible(operation) || !workspaceImageReferenceWithDigest(imageDigest) {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchRepairNotEligible
	}

	repairOperationID := operation.ID + ":runtime-repair:" + authorizationID
	input := clients.WorkspaceRuntimeInput{
		AccountID: operation.stringFact("accountId"), WorkspaceID: operation.stringFact("workspaceId"),
		ComputeID: operation.stringFact("computeAllocationId"), VolumeID: operation.stringFact("storageId"),
		AttachmentID: operation.stringFact("attachmentId"), AttachmentOperationID: operation.stringFact("attachmentBindingRef"),
		RuntimeOperationID: repairOperationID + ":create", PreviousRuntimeOperationID: operation.Attempts["runtime"].IdempotencyKey,
		ImageID: imageDigest, GatewaySecretRef: operation.stringFact("gatewaySecretRef"),
	}
	runtime, err := service.RepairWorkspaceRuntime(ctx, input, repairOperationID)
	if err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	if !runtime.Ready || runtime.ID == "" || runtime.URL == "" || runtime.ServiceName == "" || runtime.WorkspaceID != operation.stringFact("workspaceId") ||
		runtime.OperationID != input.RuntimeOperationID || runtime.ImageID != imageDigest {
		return workspaceLaunchReconcileOperation{}, fmt.Errorf("workspace_runtime_repair_not_ready")
	}

	resources := workspaceLaunchFabricResources(operation)
	resources.RuntimeID, resources.RuntimeServiceName, resources.RuntimeURL = runtime.ID, runtime.ServiceName, runtime.URL
	resources.RuntimeUsername = firstNonEmpty(runtime.Access.Username, operation.stringFact("runtimeUsername"))
	resources.RuntimeCredentialStatus = firstNonEmpty(runtime.Access.CredentialStatus, operation.stringFact("credentialStatus"))
	resources.RuntimeCredentialVersion = firstNonEmpty(runtime.Access.CredentialVersion, operation.stringFact("credentialVersion"))
	resources.RuntimeCredentialSecretRef = firstNonEmpty(runtime.Access.SecretRef, operation.stringFact("credentialSecretRef"))
	resources.RuntimeBindingRef = runtime.OperationID
	facts, err := workspaceLaunchFabricStageFacts("runtime", resources, operation)
	if err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	reduced, err := reduceWorkspaceLaunchStageObservation(&operation, workspaceLaunchStageObservation{State: workspaceLaunchStageReady, Facts: facts})
	if err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	attempt := operation.Attempts["runtime"]
	attempt.Confirmed, attempt.Unknown, attempt.Status = 1, 0, "confirmed"
	operation.Attempts["runtime"] = attempt
	operation.Observations["runtime"] = reduced
	if continuation, exists := operation.FreshContinuationAuthorizations["runtime"]; exists {
		continuation.Status = "consumed"
		continuation.ConsumedAt = app.workspaceLaunchReconciler(service, clients.SessionDelegatedCredential{}, 0).clockNow().Format(time.RFC3339Nano)
		operation.FreshContinuationAuthorizations["runtime"] = continuation
	}
	operation.RuntimeRepair = &workspaceLaunchRuntimeRepair{
		AuthorizationID: authorizationID, LaunchVersion: launchVersion, AuthorizedBy: strings.TrimSpace(authorizedBy),
		AuthorizedAt: time.Now().UTC().Format(time.RFC3339Nano), Reason: reason, ImageDigest: imageDigest,
	}
	operation.Stage, operation.Status = "activation", "pending"
	operation.ResumeAuthorization = nil
	operation.ResumeAuthorizationConsumedAt = ""
	reconciler := app.workspaceLaunchReconciler(service, clients.SessionDelegatedCredential{}, 0)
	updated, err := reconciler.persist(ctx, operation)
	if err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	return reconcileWorkspaceLaunchRuntimeRepairTail(ctx, reconciler, updated)
}

func reconcileWorkspaceLaunchRuntimeRepairTail(ctx context.Context, reconciler *WorkspaceLaunchReconciler, operation workspaceLaunchReconcileOperation) (workspaceLaunchReconcileOperation, error) {
	for _, stage := range []string{"activation", "receipt"} {
		if operation.Status != "pending" || operation.Stage != stage {
			break
		}
		var err error
		operation, err = reconciler.Reconcile(ctx, operation.ID)
		if err != nil {
			return workspaceLaunchReconcileOperation{}, err
		}
	}
	return operation, nil
}

func workspaceImageReferenceWithDigest(value string) bool {
	repository, digest, ok := strings.Cut(strings.TrimSpace(value), "@")
	return ok && repository != "" && !strings.Contains(repository, "@") && workspaceImageDigestPattern.MatchString(digest)
}
