package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

var errWorkspaceLaunchStageAdapterUnavailable = errors.New("workspace_launch_stage_adapter_unavailable")

type controlPlaneWorkspaceLaunchStageAdapter struct {
	app           *controlPlaneServer
	service       *controlplane.Service
	keyCredential clients.SessionDelegatedCredential
	keyUserID     int64
}

func (a *controlPlaneWorkspaceLaunchStageAdapter) ReadStage(ctx context.Context, operation workspaceLaunchReconcileOperation) (workspaceLaunchStageObservation, error) {
	if a == nil || a.app == nil || a.service == nil {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, errWorkspaceLaunchStageAdapterUnavailable
	}
	switch operation.Stage {
	case "key":
		return a.readWorkspaceLaunchKey(ctx, operation)
	case "debit":
		return a.readWorkspaceLaunchDebit(ctx, operation)
	case "ensure_compute_allocation", "storage", "attachment", "secret", "runtime":
		return a.readWorkspaceLaunchFabricStage(ctx, operation)
	case "activation":
		return a.readWorkspaceLaunchActivation(ctx, operation)
	case "receipt":
		return a.readWorkspaceLaunchReceipt(ctx, operation)
	default:
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, errInvalidWorkspaceLaunchOperation
	}
}

func (a *controlPlaneWorkspaceLaunchStageAdapter) CanMutateStage(operation workspaceLaunchReconcileOperation) bool {
	if a == nil || a.app == nil || a.service == nil {
		return false
	}
	return operation.Stage != "key" || a.workspaceLaunchKeyMutationCredentialValid(operation)
}

func (a *controlPlaneWorkspaceLaunchStageAdapter) MutateStage(ctx context.Context, operation workspaceLaunchReconcileOperation, idempotencyKey string) error {
	if a == nil || a.app == nil || a.service == nil || idempotencyKey == "" {
		return errWorkspaceLaunchStageAdapterUnavailable
	}
	switch operation.Stage {
	case "key":
		return a.mutateWorkspaceLaunchKey(ctx, operation, idempotencyKey)
	case "debit":
		return a.mutateWorkspaceLaunchDebit(ctx, operation)
	case "ensure_compute_allocation", "storage", "attachment", "secret", "runtime":
		return a.mutateWorkspaceLaunchFabricStage(ctx, operation, idempotencyKey)
	case "activation":
		return a.mutateWorkspaceLaunchActivation(ctx, operation, idempotencyKey)
	case "receipt":
		return a.mutateWorkspaceLaunchReceipt(ctx, operation, idempotencyKey)
	default:
		return errInvalidWorkspaceLaunchOperation
	}
}

func (app *controlPlaneServer) workspaceLaunchReconciler(service *controlplane.Service, credential clients.SessionDelegatedCredential, userID int64) *WorkspaceLaunchReconciler {
	return NewWorkspaceLaunchReconciler(app.tables, &controlPlaneWorkspaceLaunchStageAdapter{
		app: app, service: service, keyCredential: credential, keyUserID: userID,
	})
}

func (app *controlPlaneServer) createWorkspaceLaunch(ctx context.Context, service *controlplane.Service, credential clients.SessionDelegatedCredential, userID int64, command workspaceLaunchReconcileCreate) (workspaceLaunchReconcileOperation, error) {
	return app.workspaceLaunchReconciler(service, credential, userID).Create(ctx, command)
}

func (app *controlPlaneServer) resumeWorkspaceLaunch(ctx context.Context, service *controlplane.Service, operationID string, authorization workspaceLaunchResumeAuthorization) (workspaceLaunchReconcileOperation, error) {
	row, found, err := app.tables.GetRuntimeOperation(ctx, operationID)
	if err != nil {
		return workspaceLaunchReconcileOperation{}, err
	} else if !found {
		return workspaceLaunchReconcileOperation{}, errBillingReviewNotFound
	}
	operation, decodeErr := decodeWorkspaceLaunchReconcileOperation(row)
	if decodeErr != nil {
		operation, err = app.migrateLegacyWorkspaceLaunchForResume(ctx, service, row)
		if err != nil {
			return workspaceLaunchReconcileOperation{}, err
		}
	}
	if existing, _, found := operation.resumeAuthorizationByID(authorization.AuthorizationID); found && authorization.AuthorizedAt == "" {
		authorization.AuthorizedAt = existing.AuthorizedAt
	}
	if authorization.AuthorizedAt == "" {
		authorization.AuthorizedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return app.workspaceLaunchReconciler(service, clients.SessionDelegatedCredential{}, 0).Resume(ctx, operationID, authorization)
}

type workspaceLaunchLegacyResumeFacts struct {
	SchemaVersion           int    `json:"schemaVersion"`
	RequestHash             string `json:"requestHash"`
	AccountID               string `json:"accountId"`
	OwnerUserID             string `json:"ownerUserId"`
	Sub2APIUserID           int64  `json:"sub2apiUserId"`
	WorkspaceID             string `json:"workspaceId"`
	Name                    string `json:"name"`
	PackageID               string `json:"packageId"`
	StorageGB               int    `json:"sizeGb"`
	AutoRenew               bool   `json:"autoRenew"`
	PriceVersion            string `json:"priceVersion"`
	TotalChargeUSDMicros    int64  `json:"totalChargeUsdMicros"`
	WorkspaceImageDigest    string `json:"workspaceImageDigest"`
	WorkspaceKeyGroupID     int64  `json:"workspaceKeyGroupId"`
	WorkspaceAPIKeyID       int64  `json:"workspaceApiKeyId"`
	WorkspaceKeyFingerprint string `json:"workspaceKeyFingerprint"`
	PreChargeBalanceMicros  int64  `json:"preChargeBalanceUsdMicros"`
	ComputeAllocationID     string `json:"computeAllocationId"`
	StorageID               string `json:"storageId"`
	AttachmentID            string `json:"attachmentId"`
	AttachmentOperationID   string `json:"attachmentOperationId"`
	GatewaySecretRef        string `json:"gatewaySecretRef"`
	WorkspaceOperationID    string `json:"workspaceOperationId"`
	RuntimeID               string `json:"runtimeId"`
	AcceptanceBCapacitySlot bool   `json:"acceptanceBCapacitySlot"`
}

func (app *controlPlaneServer) migrateLegacyWorkspaceLaunchForResume(ctx context.Context, service *controlplane.Service, row map[string]any) (workspaceLaunchReconcileOperation, error) {
	if app == nil || app.tables == nil || service == nil || stringValue(row["status"]) != "manual_review" || !isWorkspaceLaunchAction(stringValue(row["action"])) {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchLegacyMigrationBlocked
	}
	var legacy workspaceLaunchLegacyResumeFacts
	if json.Unmarshal([]byte(stringValue(row["result"])), &legacy) != nil || legacy.SchemaVersion != 2 {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchLegacyMigrationBlocked
	}
	operationID := firstNonEmpty(stringValue(row["operationId"]), stringValue(row["id"]))
	if operationID == "" || stringValue(row["id"]) != operationID ||
		stringValue(row["accountId"]) != legacy.AccountID || stringValue(row["workspaceId"]) != legacy.WorkspaceID ||
		stringValue(row["resourceId"]) != legacy.WorkspaceID {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchLegacyMigrationBlocked
	}
	account, found, err := app.tables.GetAccount(ctx, legacy.AccountID)
	if err != nil || !found {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchLegacyMigrationBlocked
	}
	owner, err := app.findUserByID(ctx, legacy.OwnerUserID)
	if err != nil || !ownsActiveAccount(account, owner) {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchLegacyMigrationBlocked
	}
	active, err := app.hasActiveCustomerMembership(ctx, owner)
	if err != nil || !active {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchLegacyMigrationBlocked
	}
	sub2APIUserID, err := app.sub2APIUserID(ctx, legacy.AccountID)
	if err != nil || sub2APIUserID != legacy.Sub2APIUserID || int64(numberField(account, "sub2apiUserId", 0)) != legacy.Sub2APIUserID {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchLegacyMigrationBlocked
	}
	createdAt, err := time.Parse(time.RFC3339Nano, stringValue(row["createdAt"]))
	if err != nil || createdAt.IsZero() {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchLegacyMigrationBlocked
	}
	stages := workspaceLaunchLegacyBindingStages(legacy)
	if len(stages) == 0 {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchLegacyMigrationBlocked
	}
	binding, err := service.ReadLegacyWorkspaceLaunchBinding(ctx, clients.LegacyWorkspaceLaunchBindingInput{
		SchemaVersion: 2, LaunchOperationID: operationID, AccountID: legacy.AccountID, WorkspaceID: legacy.WorkspaceID,
		RequestHash: legacy.RequestHash, PackageID: legacy.PackageID, SizeGB: legacy.StorageGB,
		WorkspaceImageDigest: legacy.WorkspaceImageDigest, WorkspaceAPIKeyID: legacy.WorkspaceAPIKeyID,
		WorkspaceKeyFingerprint: legacy.WorkspaceKeyFingerprint, Stages: stages,
	})
	if err != nil || binding.State != "ready" {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchLegacyMigrationBlocked
	}
	command := workspaceLaunchReconcileCreate{
		OperationID: operationID, RequestHash: legacy.RequestHash, AccountID: legacy.AccountID, OwnerUserID: legacy.OwnerUserID,
		Sub2APIUserID: legacy.Sub2APIUserID, WorkspaceKeyGroupID: legacy.WorkspaceKeyGroupID,
		WorkspaceID: legacy.WorkspaceID, Name: legacy.Name, PackageID: legacy.PackageID, StorageGB: legacy.StorageGB,
		AutoRenew: legacy.AutoRenew, PriceVersion: legacy.PriceVersion, TotalChargeUSDMicros: legacy.TotalChargeUSDMicros,
		ProviderProfileRef: binding.ProviderProfileRef, PreflightBindingRef: binding.PreflightBindingRef,
		WorkspaceImageDigest: legacy.WorkspaceImageDigest, PreChargeBalanceMicros: legacy.PreChargeBalanceMicros,
		AcceptanceBCapacitySlot: legacy.AcceptanceBCapacitySlot, CreatedAt: createdAt,
	}
	return app.workspaceLaunchReconciler(service, clients.SessionDelegatedCredential{}, 0).MigrateLegacy(ctx, row, command, &binding)
}

func workspaceLaunchLegacyBindingStages(legacy workspaceLaunchLegacyResumeFacts) []clients.LegacyWorkspaceLaunchStageIdentity {
	if strings.TrimSpace(legacy.ComputeAllocationID) == "" {
		return nil
	}
	stages := []clients.LegacyWorkspaceLaunchStageIdentity{{Stage: "ensure_compute_allocation", ResourceRef: legacy.ComputeAllocationID}}
	if strings.TrimSpace(legacy.StorageID) == "" {
		return stages
	}
	stages = append(stages, clients.LegacyWorkspaceLaunchStageIdentity{Stage: "storage", ResourceRef: legacy.StorageID})
	if strings.TrimSpace(legacy.AttachmentID) == "" || strings.TrimSpace(legacy.AttachmentOperationID) == "" {
		return stages
	}
	stages = append(stages, clients.LegacyWorkspaceLaunchStageIdentity{Stage: "attachment", ResourceRef: legacy.AttachmentID})
	if strings.TrimSpace(legacy.GatewaySecretRef) == "" {
		return stages
	}
	stages = append(stages, clients.LegacyWorkspaceLaunchStageIdentity{Stage: "secret", ResourceRef: legacy.GatewaySecretRef})
	if strings.TrimSpace(legacy.RuntimeID) == "" || strings.TrimSpace(legacy.WorkspaceOperationID) == "" {
		return stages
	}
	return append(stages, clients.LegacyWorkspaceLaunchStageIdentity{Stage: "runtime", ResourceRef: legacy.RuntimeID})
}

func (app *controlPlaneServer) runWorkspaceLaunchesOnce(ctx context.Context, service *controlplane.Service) error {
	rows, err := queryRuntimeOperations(ctx, app.tables, runtimeOperationQuery{
		Action: workspaceLaunchAction, ExcludedStatuses: []string{"succeeded", "refunded", "failed", "manual_review"},
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, row := range rows {
		operation, decodeErr := decodeWorkspaceLaunchReconcileOperation(row)
		if decodeErr != nil {
			errs = append(errs, decodeErr)
			continue
		}
		if err := app.runWorkspaceLaunch(ctx, service, operation.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (app *controlPlaneServer) runWorkspaceLaunch(ctx context.Context, service *controlplane.Service, operationID string) error {
	_, err := app.workspaceLaunchReconciler(service, clients.SessionDelegatedCredential{}, 0).Reconcile(ctx, operationID)
	return err
}
