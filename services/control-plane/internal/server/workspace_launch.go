package server

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
	"opl-cloud/services/control-plane/internal/domain"
)

var (
	errInvalidWorkspaceLaunchOperation = errors.New("invalid_workspace_launch_operation")
	errWorkspaceLaunchInProgress       = errors.New("workspace_launch_in_progress")
	errWorkspaceLaunchCASConflict      = errors.New("workspace_launch_cas_conflict")
)

const (
	workspaceLaunchAction        = "workspace.launch.v2"
	workspaceLaunchSchemaVersion = 2
)

func isWorkspaceLaunchAction(action string) bool {
	return action == workspaceLaunchAction || action == "workspace.launch"
}

type workspaceLaunchOperation struct {
	ID                         string         `json:"-"`
	Status                     string         `json:"-"`
	CreatedAt                  string         `json:"-"`
	PersistedResult            string         `json:"-"`
	SchemaVersion              int            `json:"schemaVersion"`
	RequestHash                string         `json:"requestHash"`
	Phase                      string         `json:"phase"`
	AccountID                  string         `json:"accountId"`
	OwnerUserID                string         `json:"ownerUserId"`
	WorkspaceID                string         `json:"workspaceId"`
	Name                       string         `json:"name"`
	PackageID                  string         `json:"packageId"`
	StorageGB                  int            `json:"sizeGb"`
	AutoRenew                  bool           `json:"autoRenew"`
	PriceVersion               string         `json:"priceVersion"`
	TotalChargeUSDMicros       int64          `json:"totalChargeUsdMicros"`
	PeriodStart                string         `json:"periodStart,omitempty"`
	PaidThrough                string         `json:"paidThrough,omitempty"`
	BillingAnchorDay           int            `json:"billingAnchorDay,omitempty"`
	ComputeID                  string         `json:"computeAllocationId"`
	ComputeNodePoolID          string         `json:"computeNodePoolId"`
	StorageID                  string         `json:"storageId"`
	AttachmentID               string         `json:"attachmentId,omitempty"`
	AttachmentOperationID      string         `json:"attachmentOperationId"`
	WorkspaceOperationID       string         `json:"workspaceOperationId"`
	WorkspaceAPIKeyID          int64          `json:"workspaceApiKeyId"`
	RedeemCode                 string         `json:"sub2apiRedeemCode"`
	RefundCode                 string         `json:"sub2apiRefundCode,omitempty"`
	ChargeAttempted            bool           `json:"chargeAttempted,omitempty"`
	ChargeConfirmation         map[string]any `json:"chargeConfirmation,omitempty"`
	PreChargeBalanceUSDMicros  int64          `json:"preChargeBalanceUsdMicros,omitempty"`
	PostChargeBalanceUSDMicros int64          `json:"postChargeBalanceUsdMicros,omitempty"`
	PostChargeBalanceKnown     bool           `json:"postChargeBalanceKnown,omitempty"`
	RefundAttempted            bool           `json:"refundAttempted,omitempty"`
	RefundConfirmation         map[string]any `json:"refundConfirmation,omitempty"`
	RefundReason               string         `json:"refundReason,omitempty"`
	RefundReceiptID            string         `json:"refundReceiptId,omitempty"`
	LeaseToken                 string         `json:"leaseToken,omitempty"`
	LeaseExpiresAt             string         `json:"leaseExpiresAt,omitempty"`
	GatewaySecretRef           string         `json:"gatewaySecretRef,omitempty"`
	WorkspaceKeyStatus         string         `json:"workspaceKeyStatus,omitempty"`
	WorkspaceKeyFingerprint    string         `json:"workspaceKeyFingerprint,omitempty"`
	RuntimeID                  string         `json:"runtimeId,omitempty"`
	RuntimeReady               bool           `json:"runtimeReady,omitempty"`
	RuntimeServiceName         string         `json:"runtimeServiceName,omitempty"`
	RuntimeUsername            string         `json:"runtimeUsername,omitempty"`
	CredentialStatus           string         `json:"credentialStatus,omitempty"`
	CredentialVersion          string         `json:"credentialVersion,omitempty"`
	CredentialSecretRef        string         `json:"credentialSecretRef,omitempty"`
	URL                        string         `json:"url,omitempty"`
	ReceiptID                  string         `json:"receiptId,omitempty"`
	ErrorCode                  string         `json:"errorCode,omitempty"`
}

type workspaceLaunchClaimCAS struct {
	AccountID               string
	ExpectedOperationResult string
	DesiredOperation        map[string]any
}

type workspaceLaunchPersistCAS struct {
	OperationID             string
	ExpectedOperationResult string
	DesiredOperation        map[string]any
}

func encodeWorkspaceLaunchOperation(operation workspaceLaunchOperation) string {
	payload, _ := json.Marshal(operation)
	return string(payload)
}

func newWorkspaceLaunchOperation(accountID, ownerUserID, name, packageID string, storageGB int, autoRenew bool, priceVersion string, totalChargeUSDMicros int64, key string) workspaceLaunchOperation {
	operationID := "workspace-launch-" + stableID(accountID, key)[:18]
	workspaceID := "ws-" + stableID("workspace-launch-v2", accountID, operationID)[:18]
	now := time.Now().UTC()
	return workspaceLaunchOperation{
		ID: operationID, Status: "debit_pending", CreatedAt: now.Format(time.RFC3339Nano), Phase: "debit_pending", SchemaVersion: workspaceLaunchSchemaVersion,
		RequestHash: stableID("workspace-launch-v2", accountID, ownerUserID, name, packageID, strconv.Itoa(storageGB), strconv.FormatBool(autoRenew), priceVersion),
		AccountID:   accountID, OwnerUserID: ownerUserID, WorkspaceID: workspaceID, Name: name, PackageID: packageID,
		StorageGB: storageGB, AutoRenew: autoRenew, PriceVersion: priceVersion, TotalChargeUSDMicros: totalChargeUSDMicros,
		PeriodStart: now.Format(time.RFC3339Nano), PaidThrough: nextBillingMonth(now, now.Day()).Format(time.RFC3339Nano), BillingAnchorDay: now.Day(),
		ComputeID:             resourceIDForMutation("ca", accountID, operationID+":compute"),
		StorageID:             resourceIDForMutation("vol", accountID, operationID+":storage"),
		AttachmentOperationID: operationID + ":attachment", WorkspaceOperationID: operationID + ":workspace",
		RedeemCode: monthlyRedeemCode(monthlyEnvironment(), operationID), RefundCode: monthlyRefundCode(monthlyEnvironment(), operationID),
	}
}

func decodeWorkspaceLaunchOperation(row map[string]any) (workspaceLaunchOperation, error) {
	var operation workspaceLaunchOperation
	if err := json.Unmarshal([]byte(stringValue(row["result"])), &operation); err != nil {
		return workspaceLaunchOperation{}, errInvalidWorkspaceLaunchOperation
	}
	result := stringValue(row["result"])
	operation.ID = firstNonEmpty(stringValue(row["operationId"]), stringValue(row["id"]))
	operation.Status, operation.CreatedAt, operation.PersistedResult = stringValue(row["status"]), stringValue(row["createdAt"]), result
	if operation.RefundCode == "" {
		operation.RefundCode = monthlyRefundCode(monthlyEnvironment(), operation.ID)
	}
	if operation.PeriodStart == "" {
		operation.PeriodStart = operation.CreatedAt
	}
	if start, err := time.Parse(time.RFC3339, operation.PeriodStart); err == nil {
		if operation.BillingAnchorDay == 0 {
			operation.BillingAnchorDay = start.Day()
		}
		if operation.PaidThrough == "" {
			operation.PaidThrough = nextBillingMonth(start, operation.BillingAnchorDay).Format(time.RFC3339Nano)
		}
	}
	keyPending := operation.Phase == "key_pending" && operation.WorkspaceAPIKeyID == 0
	if operation.SchemaVersion != workspaceLaunchSchemaVersion || operation.ID == "" || operation.Status == "" || operation.RequestHash == "" || operation.AccountID == "" || operation.OwnerUserID == "" ||
		operation.WorkspaceID == "" || operation.PriceVersion == "" || operation.PackageID == "" || operation.StorageGB <= 0 || operation.TotalChargeUSDMicros <= 0 ||
		operation.WorkspaceAPIKeyID < 0 || operation.WorkspaceAPIKeyID == 0 && !keyPending || operation.RedeemCode == "" {
		return workspaceLaunchOperation{}, errInvalidWorkspaceLaunchOperation
	}
	for field, want := range map[string]string{
		"accountId": operation.AccountID, "workspaceId": operation.WorkspaceID, "resourceId": operation.WorkspaceID,
		"resourceKind": "workspace_launch", "action": workspaceLaunchAction,
	} {
		if got := stringValue(row[field]); got != "" && got != want {
			return workspaceLaunchOperation{}, errInvalidWorkspaceLaunchOperation
		}
	}
	return operation, nil
}

func workspaceLaunchOperationRow(operation workspaceLaunchOperation) map[string]any {
	return map[string]any{
		"id": operation.ID, "operationId": operation.ID, "accountId": operation.AccountID, "workspaceId": operation.WorkspaceID,
		"resourceId": operation.WorkspaceID, "resourceKind": "workspace_launch", "action": workspaceLaunchAction, "status": operation.Status,
		"result": encodeWorkspaceLaunchOperation(operation), "computeAllocationId": operation.ComputeID, "storageId": operation.StorageID,
		"attachmentId": operation.AttachmentID, "runtimeServiceName": operation.RuntimeServiceName, "createdAt": operation.CreatedAt,
		"workspaceApiKeyId": operation.WorkspaceAPIKeyID,
	}
}

func workspaceLaunchClaimIdentityMatches(current, desired map[string]any) bool {
	existing, existingErr := decodeWorkspaceLaunchOperation(current)
	next, nextErr := decodeWorkspaceLaunchOperation(desired)
	return existingErr == nil && nextErr == nil && existing.ID == next.ID && existing.AccountID == next.AccountID &&
		existing.WorkspaceID == next.WorkspaceID && existing.RequestHash == next.RequestHash
}

func workspaceLaunchResponse(row map[string]any) (map[string]any, error) {
	operation, err := decodeWorkspaceLaunchOperation(row)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"operationId": operation.ID, "status": operation.Status, "phase": operation.Phase,
		"accountId": operation.AccountID, "workspaceId": operation.WorkspaceID, "name": operation.Name,
		"packageId": operation.PackageID, "sizeGb": operation.StorageGB, "autoRenew": operation.AutoRenew, "priceVersion": operation.PriceVersion,
		"currency": pricingCurrency, "totalChargeUsdMicros": operation.TotalChargeUSDMicros,
		"computeAllocationId": operation.ComputeID, "storageId": operation.StorageID, "attachmentId": operation.AttachmentID,
		"workspaceApiKeyId":  strconv.FormatInt(operation.WorkspaceAPIKeyID, 10),
		"workspaceKeyStatus": operation.WorkspaceKeyStatus, "workspaceKeyFingerprint": operation.WorkspaceKeyFingerprint,
		"runtimeServiceName": operation.RuntimeServiceName, "url": operation.URL, "receiptId": operation.ReceiptID,
		"errorCode": operation.ErrorCode, "createdAt": row["createdAt"], "updatedAt": row["updatedAt"],
	}, nil
}

func (app *controlPlaneServer) runWorkspaceLaunchesOnce(ctx context.Context, service *controlplane.Service) error {
	rows, err := app.tables.ListRuntimeOperations(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, row := range rows {
		if stringValue(row["action"]) != workspaceLaunchAction {
			continue
		}
		operation, err := decodeWorkspaceLaunchOperation(row)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if terminalWorkspaceLaunchStatus(operation.Status) || operation.Status == "manual_review" {
			continue
		}
		if err := app.runWorkspaceLaunch(ctx, service, stringValue(row["id"])); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (app *controlPlaneServer) runWorkspaceLaunch(ctx context.Context, service *controlplane.Service, operationID string) error {
	operation, ok, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || terminalWorkspaceLaunchStatus(operation.Status) || operation.Status == "manual_review" {
		return err
	}
	unlock := app.lockResource("workspace-launch", operation.AccountID)
	defer unlock()
	operation, ok, err = app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || terminalWorkspaceLaunchStatus(operation.Status) || operation.Status == "manual_review" {
		return err
	}
	if operation.Phase == "key_pending" {
		return nil
	}
	unlockAccount := app.lockResource("account", operation.AccountID)
	defer unlockAccount()
	if operation.LeaseExpiresAt != "" {
		expiresAt, err := time.Parse(time.RFC3339, operation.LeaseExpiresAt)
		if err != nil {
			return app.manualReviewWorkspaceLaunchDebit(ctx, &operation, "workspace_launch_lease_invalid")
		}
		if expiresAt.After(time.Now().UTC()) {
			return nil
		}
	}
	operation.LeaseToken = stableID(operation.ID, operation.PersistedResult, time.Now().UTC().Format(time.RFC3339Nano))
	operation.LeaseExpiresAt = time.Now().UTC().Add(workspaceRenewalLeaseDuration).Format(time.RFC3339Nano)
	desired := workspaceLaunchOperationRow(operation)
	if err := app.tables.ClaimWorkspaceLaunch(ctx, workspaceLaunchClaimCAS{
		AccountID: operation.AccountID, ExpectedOperationResult: operation.PersistedResult, DesiredOperation: desired,
	}); errors.Is(err, errWorkspaceLaunchCASConflict) {
		return nil
	} else if err != nil {
		return err
	}
	operation.PersistedResult = stringValue(desired["result"])

	if operation.Phase == "debit_pending" {
		owner, err := app.findUserByID(ctx, operation.OwnerUserID)
		if err != nil {
			return app.retryWorkspaceLaunchDebit(ctx, &operation, "workspace_launch_owner_state_unavailable", err)
		}
		ownerActive := owner != nil && stringValue(owner["accountId"]) == operation.AccountID
		if ownerActive {
			ownerActive, err = app.hasActiveCustomerMembership(ctx, owner)
			if err != nil {
				return app.retryWorkspaceLaunchDebit(ctx, &operation, "workspace_launch_owner_state_unavailable", err)
			}
		}
		if !ownerActive {
			return app.manualReviewWorkspaceLaunchDebit(ctx, &operation, "workspace_launch_owner_identity_mismatch")
		}
		if !operation.ChargeAttempted && operation.ChargeConfirmation == nil {
			if code, preflightErr := verifyWorkspaceLaunchPreflight(ctx, service, operation); preflightErr != nil {
				return app.retryWorkspaceLaunchDebit(ctx, &operation, code, preflightErr)
			}
		}
		return app.debitWorkspaceLaunch(ctx, service, &operation)
	}
	return app.fulfillWorkspaceLaunch(ctx, service, &operation)
}

func (app *controlPlaneServer) fulfillWorkspaceLaunch(ctx context.Context, service *controlplane.Service, operation *workspaceLaunchOperation) error {
	for range 10 {
		switch operation.Phase {
		case "debited":
			operation.Status, operation.Phase, operation.ErrorCode = "preparing", "compute_fulfilling", ""
			if err := app.persistWorkspaceLaunch(ctx, operation); err != nil {
				return err
			}
		case "compute_fulfilling", "storage_fulfilling":
			resourceType := "compute"
			if operation.Phase == "storage_fulfilling" {
				resourceType = "storage"
			}
			outcome, err := app.fulfillWorkspaceLaunchResource(ctx, service, operation, resourceType)
			if err != nil {
				return err
			}
			switch outcome {
			case "ready":
				if resourceType == "compute" {
					operation.Phase = "storage_fulfilling"
				} else {
					operation.Phase = "attaching"
				}
				operation.Status, operation.ErrorCode = "preparing", ""
				if err := app.persistWorkspaceLaunch(ctx, operation); err != nil {
					return err
				}
			case "absent":
				if resourceType == "compute" {
					storage, err := service.SyncMonthlyStorage(ctx, operation.StorageID)
					if err != nil {
						var upstream *clients.FabricHTTPError
						var response struct {
							Error string `json:"error"`
						}
						if errors.As(err, &upstream) && json.Unmarshal([]byte(upstream.Body), &response) == nil && response.Error == "storage_volume_not_found" {
							return app.refundWorkspaceLaunch(ctx, service, operation, "fabric_compute_and_storage_confirmed_absent")
						}
						return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "fabric_storage_readback_unconfirmed_blocks_refund")
					}
					storageFacts := structToMap(storage)
					if !workspaceLaunchResourceIdentityMatches("storage", storageFacts, *operation) {
						return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "fabric_storage_readback_unconfirmed_blocks_refund")
					}
					if !monthlyResourceConfirmedAbsent("storage", storageFacts) {
						return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "fabric_storage_presence_blocks_refund")
					}
					return app.refundWorkspaceLaunch(ctx, service, operation, "fabric_compute_and_storage_confirmed_absent")
				}
				return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "fabric_storage_confirmed_absent_after_compute_created")
			case "waiting":
				return app.waitWorkspaceLaunchFulfillment(ctx, operation)
			default:
				return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "fabric_"+resourceType+"_fulfillment_unconfirmed")
			}
		case "attaching":
			if attachment, ok := app.workspaceLaunchAttachment(*operation); ok {
				operation.AttachmentID = stringValue(attachment["id"])
			} else {
				created, err := service.CreateStorageAttachment(ctx, controlplane.StorageAttachmentInput{
					WorkspaceID: operation.WorkspaceID, ComputeID: operation.ComputeID, VolumeID: operation.StorageID,
				}, operation.AttachmentOperationID)
				if err != nil {
					return app.retryWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_attachment_retryable", err)
				}
				if created.ID == "" || created.WorkspaceID != operation.WorkspaceID || created.ComputeID != operation.ComputeID || created.VolumeID != operation.StorageID ||
					created.Status != "attached" || created.ProviderRequestID == "" || created.ProviderAttachmentID == "" {
					return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_attachment_identity_mismatch")
				}
				body := attachmentResponse(structToMap(created), map[string]any{
					"computeAllocationId": operation.ComputeID, "storageId": operation.StorageID, "workspaceId": operation.WorkspaceID,
				})
				body["accountId"], body["packageId"], body["operationId"] = operation.AccountID, operation.PackageID, operation.AttachmentOperationID
				if err := app.saveAttachmentFact(body, body); err != nil {
					return app.retryWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_attachment_persist_retryable", err)
				}
				operation.AttachmentID = created.ID
			}
			operation.Status, operation.Phase, operation.ErrorCode = "preparing", "secret_writing", ""
			if err := app.persistWorkspaceLaunch(ctx, operation); err != nil {
				return err
			}
		case "secret_writing":
			userID, err := app.sub2APIUserID(ctx, operation.AccountID)
			if err != nil {
				return app.retryWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_account_mapping_unavailable", err)
			}
			secret, err := service.SyncWorkspaceGatewaySecretByID(ctx, operation.AccountID, operation.WorkspaceID, userID, operation.WorkspaceAPIKeyID, workspaceReservedKeyName(operation.WorkspaceID), operation.WorkspaceOperationID+":secret")
			if err != nil {
				return app.retryWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_secret_retryable", err)
			}
			if secret.SecretRef == "" || secret.Version == "" || secret.Fingerprint == "" {
				return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_secret_readback_invalid")
			}
			operation.GatewaySecretRef, operation.WorkspaceKeyStatus, operation.WorkspaceKeyFingerprint = secret.SecretRef, "configured", secret.Fingerprint
			operation.Status, operation.Phase, operation.ErrorCode = "preparing", "runtime_starting", ""
			if err := app.persistWorkspaceLaunch(ctx, operation); err != nil {
				return err
			}
		case "runtime_starting":
			userID, err := app.sub2APIUserID(ctx, operation.AccountID)
			if err != nil {
				return app.retryWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_account_mapping_unavailable", err)
			}
			workspace, err := service.PrepareWorkspace(ctx, controlplane.CreateWorkspaceInput{
				WorkspaceID: operation.WorkspaceID, AccountID: operation.AccountID, Sub2APIUserID: userID, WorkspaceAPIKeyID: operation.WorkspaceAPIKeyID, WorkspaceAPIKeyName: workspaceReservedKeyName(operation.WorkspaceID),
				OwnerID: operation.OwnerUserID, Name: operation.Name, PackageID: operation.PackageID, AttachmentID: operation.AttachmentID,
				ComputeID: operation.ComputeID, VolumeID: operation.StorageID, GatewaySecretRef: operation.GatewaySecretRef,
			}, operation.WorkspaceOperationID)
			if err != nil {
				if errors.Is(err, controlplane.ErrWorkspaceRuntimeIdentityMismatch) {
					return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_runtime_identity_mismatch")
				}
				if errors.Is(err, controlplane.ErrWorkspaceRuntimeReadbackInvalid) {
					return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_runtime_readback_invalid")
				}
				return app.retryWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_runtime_retryable", err)
			}
			if !workspaceProjectionMatchesLaunch(workspace, *operation) || !workspaceRuntimeAttemptMatches(workspace, *operation) {
				return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_runtime_readback_invalid")
			}
			operation.RuntimeID, operation.RuntimeReady = workspace.RuntimeID, workspace.RuntimeReady
			operation.RuntimeServiceName, operation.RuntimeUsername = workspace.RuntimeServiceName, workspace.RuntimeUsername
			operation.CredentialStatus, operation.CredentialVersion, operation.CredentialSecretRef = workspace.CredentialStatus, workspace.CredentialVersion, workspace.CredentialSecretRef
			operation.URL = workspace.URL
			if !workspace.RuntimeReady || workspace.Status != "running" {
				return app.waitWorkspaceLaunchFulfillment(ctx, operation)
			}
			if !workspaceProjectionConfiguredForLaunch(workspace) {
				return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_runtime_readback_invalid")
			}
			operation.Status, operation.Phase, operation.ErrorCode = "preparing", "activating", ""
			if err := app.persistWorkspaceLaunch(ctx, operation); err != nil {
				return err
			}
		case "activating":
			if err := app.verifyWorkspaceLaunchProviderReadback(ctx, service, operation); err != nil {
				return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, err.Error())
			}
			billingState, reviewCode := app.workspaceLaunchBillingState(ctx, *operation)
			if reviewCode != "" {
				return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, reviewCode)
			}
			if existing, ok := app.getWorkspace(operation.WorkspaceID); ok {
				if !workspaceMatchesLaunch(existing, *operation) || !workspaceBillingStateMatchesLaunch(existing, billingState) || stringValue(existing["runtimeId"]) != operation.RuntimeID {
					return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_activation_identity_mismatch")
				}
			} else {
				workspaceRow := workspaceProjectionRow(workspaceProjectionFromLaunch(*operation))
				for key, value := range billingState {
					workspaceRow[key] = value
				}
				if _, err := app.tables.ActivateWorkspace(ctx, workspaceRow); errors.Is(err, errWorkspaceActivationConflict) {
					return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_activation_conflict")
				} else if err != nil {
					return app.retryWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_activation_retryable", err)
				}
			}
			operation.Status, operation.Phase, operation.ErrorCode = "preparing", "receipt_pending", ""
			if err := app.persistWorkspaceLaunch(ctx, operation); err != nil {
				return err
			}
		case "receipt_pending":
			return app.recordWorkspaceLaunchPurchaseReceipt(ctx, service, operation)
		case "refund_pending":
			return app.refundWorkspaceLaunch(ctx, service, operation, operation.RefundReason)
		case "succeeded", "refunded":
			return nil
		default:
			return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_phase_invalid")
		}
	}
	return app.retryWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_transition_limit", errors.New("workspace launch transition limit"))
}

func (app *controlPlaneServer) fulfillWorkspaceLaunchResource(ctx context.Context, service *controlplane.Service, operation *workspaceLaunchOperation, resourceType string) (string, error) {
	row := workspaceLaunchResourceRow(*operation, resourceType)
	var prepared any
	var prepareErr error
	if resourceType == "storage" {
		prepared, prepareErr = service.PrepareMonthlyStorage(ctx, clients.StorageVolumeInput{
			ID: operation.StorageID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, ComputeID: operation.ComputeID,
			Zone: stringValue(row["zone"]), SizeGB: operation.StorageGB,
		}, operation.ID+":storage")
	} else {
		prepared, prepareErr = service.PrepareMonthlyCompute(ctx, clients.ComputeAllocationInput{
			ID: operation.ComputeID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, PackageID: operation.PackageID, NodePoolID: operation.ComputeNodePoolID,
		}, operation.ID+":compute")
	}
	preparedFacts := structToMap(prepared)
	if prepareErr == nil && !workspaceLaunchResourceIdentityMatches(resourceType, preparedFacts, *operation) {
		return "unknown", nil
	}

	var readback any
	var readErr error
	if resourceType == "storage" {
		readback, readErr = service.SyncMonthlyStorage(ctx, operation.StorageID)
	} else {
		readback, readErr = service.SyncMonthlyCompute(ctx, operation.ComputeID)
	}
	facts := structToMap(readback)
	if !workspaceLaunchResourceIdentityMatches(resourceType, facts, *operation) {
		return "unknown", nil
	}
	candidate := mergeMaps(row, facts)
	stripWorkspaceLaunchResourceBilling(candidate)
	if resourceType == "storage" {
		if err := app.tables.SaveStorage(ctx, candidate); err != nil {
			return "", err
		}
	} else if err := app.tables.SaveCompute(ctx, candidate); err != nil {
		return "", err
	}
	if monthlyResourceConfirmedAbsent(resourceType, candidate) {
		return "absent", nil
	}
	if readErr != nil {
		return "unknown", nil
	}
	if monthlyResourceInProgress(candidate) {
		if prepareErr != nil {
			return "unknown", nil
		}
		return "waiting", nil
	}
	expected := workspaceLaunchProviderExpectation(*operation, resourceType)
	if !monthlyPurchaseReadbackConfirmed(resourceType, expected, facts) {
		return "unknown", nil
	}
	return "ready", nil
}

func workspaceLaunchResourceRow(operation workspaceLaunchOperation, resourceType string) map[string]any {
	id := operation.ComputeID
	if resourceType == "storage" {
		id = operation.StorageID
	}
	row := map[string]any{
		"id": id, "accountId": operation.AccountID, "ownerUserId": operation.OwnerUserID, "workspaceId": operation.WorkspaceID,
		"name": operation.Name, "packageId": operation.PackageID, "resourceType": resourceType, "operationId": operation.ID + ":" + resourceType,
		"status": "provisioning", "desiredStatus": monthlyDesiredStatus(resourceType), "providerStatus": "pending", "autoRenew": false,
	}
	if resourceType == "storage" {
		row["sizeGb"], row["computeAllocationId"] = operation.StorageGB, operation.ComputeID
		row["zone"] = monthlyComputeLaunchZone()
	} else {
		row["zone"] = monthlyComputeLaunchZone()
		row["nodePoolId"] = operation.ComputeNodePoolID
	}
	return row
}

func workspaceLaunchResourceIdentityMatches(resourceType string, facts map[string]any, operation workspaceLaunchOperation) bool {
	id := operation.ComputeID
	if resourceType == "storage" {
		id = operation.StorageID
	}
	return stringValue(facts["id"]) == id && stringValue(facts["accountId"]) == operation.AccountID && stringValue(facts["workspaceId"]) == operation.WorkspaceID
}

func workspaceLaunchProviderExpectation(operation workspaceLaunchOperation, resourceType string) map[string]any {
	expected := workspaceLaunchResourceRow(operation, resourceType)
	expected["periodStart"], expected["paidThrough"] = operation.PeriodStart, operation.PaidThrough
	return expected
}

func verifyWorkspaceLaunchPreflight(ctx context.Context, service *controlplane.Service, operation workspaceLaunchOperation) (string, error) {
	zone := monthlyComputeLaunchZone()
	inputs := []clients.MonthlyPreflightInput{
		{ResourceType: "compute", PackageID: operation.PackageID, Zone: zone},
		{ResourceType: "storage", PackageID: operation.PackageID, SizeGB: operation.StorageGB, Zone: zone},
	}
	for _, input := range inputs {
		result, err := service.PreflightMonthlyResource(ctx, input)
		code := "fabric_" + input.ResourceType + "_preflight_failed"
		if err != nil {
			return code, err
		}
		if !monthlyPreflightConfirmed(input, result) || input.ResourceType == "compute" && result.NodePoolID != operation.ComputeNodePoolID {
			return code, errors.New(code)
		}
	}
	return "", nil
}

func (app *controlPlaneServer) verifyWorkspaceLaunchProviderReadback(ctx context.Context, service *controlplane.Service, operation *workspaceLaunchOperation) error {
	for _, resourceType := range []string{"compute", "storage"} {
		var facts map[string]any
		var readErr error
		if resourceType == "compute" {
			result, err := service.SyncMonthlyCompute(ctx, operation.ComputeID)
			facts, readErr = structToMap(result), err
		} else {
			result, err := service.SyncMonthlyStorage(ctx, operation.StorageID)
			facts, readErr = structToMap(result), err
		}
		if readErr != nil || !workspaceLaunchResourceIdentityMatches(resourceType, facts, *operation) {
			return errors.New("workspace_launch_provider_readback_invalid")
		}
		var current map[string]any
		if resourceType == "compute" {
			current, _ = app.getCompute(operation.ComputeID)
		} else {
			current, _ = app.getStorage(operation.StorageID)
		}
		if current == nil {
			return errors.New("workspace_launch_provider_readback_invalid")
		}
		expected := workspaceLaunchProviderExpectation(*operation, resourceType)
		if !monthlyPurchaseReadbackConfirmed(resourceType, expected, facts) {
			return errors.New("workspace_launch_provider_readback_invalid")
		}
		updated := mergeMaps(current, facts)
		stripWorkspaceLaunchResourceBilling(updated)
		var saveErr error
		if resourceType == "compute" {
			saveErr = app.tables.SaveCompute(ctx, updated)
		} else {
			saveErr = app.tables.SaveStorage(ctx, updated)
		}
		if saveErr != nil {
			return errors.New("workspace_launch_provider_readback_invalid")
		}
	}
	return nil
}

func stripWorkspaceLaunchResourceBilling(row map[string]any) {
	for _, key := range []string{
		"billingOperationId", "billingOperationStartedAt", "billingStatus", "sub2apiRedeemCode", "sub2apiRefundCode",
		"priceVersion", "currency", "billingUnit", "pricingVersion", "priceSnapshot", "monthlyPriceCnyCents", "chargeUsdMicros", "postChargeBalanceUsdMicros",
		"postChargeBalanceKnown", "periodStart", "paidThrough", "billingAnchorDay", "lastReceiptId", "lastBillingError",
	} {
		delete(row, key)
	}
}

func workspaceProjectionConfiguredForLaunch(workspace domain.WorkspaceProjection) bool {
	return workspace.RuntimeID != "" && workspace.RuntimeServiceName != "" && workspace.URL != "" &&
		workspace.CredentialStatus == "configured" && workspace.CredentialVersion != "" && workspace.CredentialSecretRef != ""
}

func workspaceRuntimeAttemptMatches(workspace domain.WorkspaceProjection, operation workspaceLaunchOperation) bool {
	for _, pair := range [][2]string{
		{operation.RuntimeID, workspace.RuntimeID}, {operation.RuntimeServiceName, workspace.RuntimeServiceName}, {operation.URL, workspace.URL},
		{operation.CredentialVersion, workspace.CredentialVersion}, {operation.CredentialSecretRef, workspace.CredentialSecretRef},
	} {
		if pair[0] != "" && pair[0] != pair[1] {
			return false
		}
	}
	return true
}

func workspaceProjectionFromLaunch(operation workspaceLaunchOperation) domain.WorkspaceProjection {
	return domain.WorkspaceProjection{
		ID: operation.WorkspaceID, AccountID: operation.AccountID, OwnerID: operation.OwnerUserID, Name: operation.Name, PackageID: operation.PackageID,
		Provider: "tencent-tke", URL: operation.URL, Status: "running", ComputeID: operation.ComputeID, VolumeID: operation.StorageID,
		AttachmentID: operation.AttachmentID, RuntimeID: operation.RuntimeID, RuntimeServiceName: operation.RuntimeServiceName,
		WorkspaceAPIKeyID: operation.WorkspaceAPIKeyID, RuntimeReady: operation.RuntimeReady, RuntimeUsername: operation.RuntimeUsername,
		CredentialStatus: operation.CredentialStatus, CredentialVersion: operation.CredentialVersion, CredentialSecretRef: operation.CredentialSecretRef,
	}
}

type workspaceBillingChildIdentity struct {
	AccountID, OwnerUserID, WorkspaceID, PackageID, ComputeID, StorageID string
	StorageGB                                                            int64
}

func workspaceBillingStateFromChildren(compute, storage map[string]any, identity workspaceBillingChildIdentity) (map[string]any, string) {
	if stringValue(compute["id"]) != identity.ComputeID || stringValue(storage["id"]) != identity.StorageID ||
		stringValue(compute["accountId"]) != identity.AccountID || stringValue(storage["accountId"]) != identity.AccountID ||
		stringValue(compute["workspaceId"]) != identity.WorkspaceID || stringValue(storage["workspaceId"]) != identity.WorkspaceID ||
		stringValue(compute["ownerUserId"]) != identity.OwnerUserID || stringValue(storage["ownerUserId"]) != identity.OwnerUserID ||
		stringValue(compute["packageId"]) != identity.PackageID || stringValue(storage["packageId"]) != identity.PackageID ||
		stringValue(storage["computeAllocationId"]) != identity.ComputeID || stringValue(compute["billingStatus"]) != "active" || stringValue(storage["billingStatus"]) != "active" {
		return nil, "workspace_launch_billing_identity_mismatch"
	}
	storageGB, validStorageGB := requiredPositiveInteger(storage, "sizeGb")
	if !validStorageGB || storageGB != identity.StorageGB {
		return nil, "workspace_launch_billing_identity_mismatch"
	}
	compute = cloneMap(compute)
	storage = cloneMap(storage)
	if !monthlyPriceSnapshotAvailable(compute) || !monthlyPriceSnapshotAvailable(storage) ||
		stringValue(compute["priceVersion"]) != pricingCatalogVersion || stringValue(storage["priceVersion"]) != pricingCatalogVersion ||
		compute["currency"] != pricingCurrency || storage["currency"] != pricingCurrency {
		return nil, "workspace_launch_billing_price_mismatch"
	}
	quote, err := workspacePricingPreview(defaultPricingCatalog(), map[string]any{"packageId": identity.PackageID, "sizeGb": identity.StorageGB})
	if err != nil {
		return nil, "workspace_launch_billing_price_mismatch"
	}
	computePrice, validComputePrice := requiredPositiveInteger(compute, "chargeUsdMicros")
	storagePrice, validStoragePrice := requiredPositiveInteger(storage, "chargeUsdMicros")
	expectedCompute, expectedComputeOK := requiredPositiveInteger(mapField(quote, "compute"), "chargeUsdMicros")
	expectedStorage, expectedStorageOK := requiredPositiveInteger(mapField(quote, "storage"), "chargeUsdMicros")
	total, validTotal := checkedAddInt64(computePrice, storagePrice)
	if !validComputePrice || !validStoragePrice || !expectedComputeOK || !expectedStorageOK || !validTotal ||
		computePrice != expectedCompute || storagePrice != expectedStorage || stringValue(quote["priceVersion"]) != pricingCatalogVersion {
		return nil, "workspace_launch_billing_price_mismatch"
	}
	computeStart, computeStartErr := time.Parse(time.RFC3339, stringValue(compute["periodStart"]))
	storageStart, storageStartErr := time.Parse(time.RFC3339, stringValue(storage["periodStart"]))
	computePaid, computePaidErr := time.Parse(time.RFC3339, stringValue(compute["paidThrough"]))
	storagePaid, storagePaidErr := time.Parse(time.RFC3339, stringValue(storage["paidThrough"]))
	computeAnchor, computeAnchorOK := requiredPositiveInteger(compute, "billingAnchorDay")
	storageAnchor, storageAnchorOK := requiredPositiveInteger(storage, "billingAnchorDay")
	if computeStartErr != nil || storageStartErr != nil || computePaidErr != nil || storagePaidErr != nil || !computeAnchorOK || !storageAnchorOK || computeAnchor > 31 || computeAnchor != storageAnchor {
		return nil, "workspace_launch_billing_period_mismatch"
	}
	periodStart := computeStart
	if storageStart.After(periodStart) {
		periodStart = storageStart
	}
	paidThrough := computePaid
	if storagePaid.Before(paidThrough) {
		paidThrough = storagePaid
	}
	if !paidThrough.After(periodStart) {
		return nil, "workspace_launch_billing_period_mismatch"
	}
	computeDeadline, computeDeadlineErr := monthlyProviderDeadline(compute)
	storageDeadline, storageDeadlineErr := monthlyProviderDeadline(storage)
	if computeDeadlineErr != nil || storageDeadlineErr != nil || computeDeadline.Before(paidThrough) || storageDeadline.Before(paidThrough) ||
		!monthlyPurchaseReadbackConfirmed("compute", compute, compute) || !monthlyPurchaseReadbackConfirmed("storage", storage, storage) {
		return nil, "workspace_launch_provider_deadline_invalid"
	}
	state := map[string]any{
		"ownerUserId": identity.OwnerUserID, "currentComputeAllocationId": identity.ComputeID,
		"autoRenew": false, "authorizedBy": "", "authorizedAt": "",
		"packageId": identity.PackageID, "storageGb": identity.StorageGB,
		"priceVersion": pricingCatalogVersion, "currency": pricingCurrency, "billingUnit": pricingBillingUnit,
		"computeUsdMicros": computePrice, "storageUsdMicros": storagePrice, "totalUsdMicros": total,
		"periodStart": periodStart.UTC().Format(time.RFC3339Nano), "paidThrough": paidThrough.UTC().Format(time.RFC3339Nano),
		"nextRenewalAt": paidThrough.UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano), "billingAnchorDay": computeAnchor,
		"renewalStatus": "active", "computeAllocationId": identity.ComputeID, "storageId": identity.StorageID,
	}
	if err := validateWorkspaceBillingState(state); err != nil {
		return nil, "workspace_launch_billing_state_invalid"
	}
	return state, ""
}

func (app *controlPlaneServer) workspaceLaunchBillingState(ctx context.Context, operation workspaceLaunchOperation) (map[string]any, string) {
	_ = ctx
	compute, computeOK := app.getCompute(operation.ComputeID)
	storage, storageOK := app.getStorage(operation.StorageID)
	if !computeOK || !storageOK || !workspaceLaunchResourceIdentityMatches("compute", compute, operation) || !workspaceLaunchResourceIdentityMatches("storage", storage, operation) {
		return nil, "workspace_launch_billing_identity_mismatch"
	}
	if !monthlyPurchaseReadbackConfirmed("compute", workspaceLaunchProviderExpectation(operation, "compute"), compute) ||
		!monthlyPurchaseReadbackConfirmed("storage", workspaceLaunchProviderExpectation(operation, "storage"), storage) {
		return nil, "workspace_launch_provider_readback_invalid"
	}
	components, computePrice, storagePrice, err := workspaceLaunchComponents(operation)
	if err != nil || components == nil {
		return nil, "workspace_launch_billing_price_mismatch"
	}
	periodStart, startErr := time.Parse(time.RFC3339, operation.PeriodStart)
	paidThrough, paidErr := time.Parse(time.RFC3339, operation.PaidThrough)
	if startErr != nil || paidErr != nil || !paidThrough.After(periodStart) || operation.BillingAnchorDay < 1 || operation.BillingAnchorDay > 31 {
		return nil, "workspace_launch_billing_period_mismatch"
	}
	for _, resource := range []map[string]any{compute, storage} {
		deadline, err := monthlyProviderDeadline(resource)
		if err != nil || deadline.Before(paidThrough) {
			return nil, "workspace_launch_provider_deadline_invalid"
		}
	}
	state := map[string]any{
		"ownerUserId": operation.OwnerUserID, "currentComputeAllocationId": operation.ComputeID,
		"autoRenew": false, "authorizedBy": "", "authorizedAt": "", "packageId": operation.PackageID, "storageGb": int64(operation.StorageGB),
		"priceVersion": operation.PriceVersion, "currency": pricingCurrency, "billingUnit": pricingBillingUnit,
		"computeUsdMicros": computePrice, "storageUsdMicros": storagePrice, "totalUsdMicros": operation.TotalChargeUSDMicros,
		"periodStart": periodStart.UTC().Format(time.RFC3339Nano), "paidThrough": paidThrough.UTC().Format(time.RFC3339Nano),
		"nextRenewalAt": paidThrough.UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano), "billingAnchorDay": int64(operation.BillingAnchorDay),
		"renewalStatus": "active", "computeAllocationId": operation.ComputeID, "storageId": operation.StorageID,
	}
	if err := validateWorkspaceBillingState(state); err != nil {
		return nil, "workspace_launch_billing_state_invalid"
	}
	return state, ""
}

func workspaceLaunchComponents(operation workspaceLaunchOperation) (map[string]any, int64, int64, error) {
	quote, err := workspacePricingPreview(defaultPricingCatalog(), map[string]any{"packageId": operation.PackageID, "sizeGb": operation.StorageGB})
	if err != nil || stringValue(quote["priceVersion"]) != operation.PriceVersion {
		return nil, 0, 0, errInvalidWorkspaceLaunchOperation
	}
	computePrice, computeOK := requiredPositiveInteger(mapField(quote, "compute"), "chargeUsdMicros")
	storagePrice, storageOK := requiredPositiveInteger(mapField(quote, "storage"), "chargeUsdMicros")
	total, totalOK := checkedAddInt64(computePrice, storagePrice)
	if !computeOK || !storageOK || !totalOK || total != operation.TotalChargeUSDMicros {
		return nil, 0, 0, errInvalidWorkspaceLaunchOperation
	}
	return map[string]any{
		"compute": map[string]any{"resourceType": "compute", "resourceId": operation.ComputeID, "chargeUsdMicros": computePrice},
		"storage": map[string]any{"resourceType": "storage", "resourceId": operation.StorageID, "sizeGb": int64(operation.StorageGB), "chargeUsdMicros": storagePrice},
	}, computePrice, storagePrice, nil
}

func workspaceBillingStateMatchesLaunch(workspace, expected map[string]any) bool {
	currentJSON, currentErr := encodeWorkspaceBillingState(workspace)
	expectedJSON, expectedErr := encodeWorkspaceBillingState(expected)
	return currentErr == nil && expectedErr == nil && currentJSON == expectedJSON
}

func terminalWorkspaceLaunchStatus(status string) bool {
	return status == "succeeded" || status == "refunded" || status == "failed"
}

func (app *controlPlaneServer) workspaceLaunchOperation(ctx context.Context, operationID string) (workspaceLaunchOperation, bool, error) {
	rows, err := app.tables.ListRuntimeOperations(ctx)
	if err != nil {
		return workspaceLaunchOperation{}, false, err
	}
	for _, row := range rows {
		if stringValue(row["id"]) != operationID || stringValue(row["action"]) != workspaceLaunchAction {
			continue
		}
		operation, err := decodeWorkspaceLaunchOperation(row)
		return operation, err == nil, err
	}
	return workspaceLaunchOperation{}, false, nil
}

func (app *controlPlaneServer) recoverWorkspaceLaunchReview(ctx context.Context, service *controlplane.Service, input billingReviewResolutionInput) (map[string]any, error) {
	if input.ResourceType != "workspace_launch" || input.ResourceID == "" || input.ResourceID != input.BillingOperationID || input.AccountID == "" || input.IdempotencyKey == "" || input.Reviewer == "" {
		return nil, errInvalidBillingReview
	}
	operation, ok, err := app.workspaceLaunchOperation(ctx, input.ResourceID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errBillingReviewNotFound
	}
	if operation.AccountID != input.AccountID {
		return nil, errBillingReviewIdentity
	}

	unlock := app.lockResource("workspace-launch", operation.AccountID)
	defer unlock()
	operation, ok, err = app.workspaceLaunchOperation(ctx, input.ResourceID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errBillingReviewNotFound
	}
	if operation.AccountID != input.AccountID {
		return nil, errBillingReviewIdentity
	}
	if terminalWorkspaceLaunchStatus(operation.Status) {
		return workspaceLaunchRecoveryResponse(operation)
	}
	if operation.Status != "manual_review" {
		return nil, errBillingReviewNotPending
	}

	unlockAccount := app.lockResource("account", operation.AccountID)
	defer unlockAccount()
	if operation.Phase == "receipt_pending" {
		_ = app.recordWorkspaceLaunchPurchaseReceipt(ctx, service, &operation)
		return app.currentWorkspaceLaunchRecoveryResponse(ctx, operation.ID)
	}
	if operation.Phase == "refund_pending" && operation.RefundConfirmation != nil {
		userID, userErr := app.sub2APIUserID(ctx, operation.AccountID)
		if userErr != nil {
			return app.keepWorkspaceLaunchReview(ctx, &operation, "workspace_launch_refund_account_unmapped")
		}
		_ = app.recordWorkspaceLaunchRefundReceipt(ctx, service, &operation, userID)
		return app.currentWorkspaceLaunchRecoveryResponse(ctx, operation.ID)
	}

	userID, err := app.sub2APIUserID(ctx, operation.AccountID)
	if err != nil || !workspaceLaunchChargeConfirmed(operation, userID) {
		return app.keepWorkspaceLaunchReview(ctx, &operation, "workspace_launch_charge_unconfirmed")
	}
	computeState, storageState, err := app.workspaceLaunchRecoveryResourceStates(ctx, service, operation)
	if err != nil {
		return nil, err
	}

	switch {
	case computeState == "ready" && storageState == "absent":
		operation.Status, operation.Phase, operation.ErrorCode = "preparing", "storage_fulfilling", ""
	case computeState == "ready" && storageState == "ready":
		operation.Status, operation.Phase, operation.ErrorCode = "preparing", "attaching", ""
	case computeState == "absent" && storageState == "absent":
		_ = app.refundWorkspaceLaunch(ctx, service, &operation, "fabric_compute_and_storage_confirmed_absent")
		return app.currentWorkspaceLaunchRecoveryResponse(ctx, operation.ID)
	case computeState == "absent" && storageState == "ready":
		return app.keepWorkspaceLaunchReview(ctx, &operation, "workspace_launch_compute_absent_storage_present")
	default:
		return app.keepWorkspaceLaunchReview(ctx, &operation, "workspace_launch_provider_state_unknown")
	}
	releaseWorkspaceLaunchLease(&operation)
	if err := app.persistWorkspaceLaunch(ctx, &operation); err != nil {
		return nil, err
	}
	_ = app.fulfillWorkspaceLaunch(ctx, service, &operation)
	return app.currentWorkspaceLaunchRecoveryResponse(ctx, operation.ID)
}

func workspaceLaunchChargeConfirmed(operation workspaceLaunchOperation, userID int64) bool {
	return operation.ChargeAttempted && operation.PostChargeBalanceKnown && operation.PreChargeBalanceUSDMicros > operation.TotalChargeUSDMicros &&
		operation.PostChargeBalanceUSDMicros == operation.PreChargeBalanceUSDMicros-operation.TotalChargeUSDMicros &&
		monthlyChargeConfirmationMatches(operation.ChargeConfirmation, operation.RedeemCode, userID, operation.TotalChargeUSDMicros)
}

func (app *controlPlaneServer) workspaceLaunchRecoveryResourceStates(ctx context.Context, service *controlplane.Service, operation workspaceLaunchOperation) (string, string, error) {
	truth, err := service.MonthlyProviderTruth(ctx, operation.ComputeID, operation.StorageID)
	if err != nil {
		return "unknown", "unknown", nil
	}
	computeState, err := app.workspaceLaunchRecoveryResourceState(ctx, operation, "compute", truth.ComputeState, structToMap(truth.Compute))
	if err != nil {
		return "", "", err
	}
	storageState, err := app.workspaceLaunchRecoveryResourceState(ctx, operation, "storage", truth.StorageState, structToMap(truth.Storage))
	return computeState, storageState, err
}

func (app *controlPlaneServer) workspaceLaunchRecoveryResourceState(ctx context.Context, operation workspaceLaunchOperation, resourceType, state string, facts map[string]any) (string, error) {
	if (state != "ready" && state != "absent") || !workspaceLaunchResourceIdentityMatches(resourceType, facts, operation) ||
		(state == "ready" && !monthlyPurchaseReadbackConfirmed(resourceType, workspaceLaunchProviderExpectation(operation, resourceType), facts)) ||
		(state == "absent" && !monthlyResourceConfirmedAbsent(resourceType, facts)) {
		return "unknown", nil
	}
	row := mergeMaps(workspaceLaunchResourceRow(operation, resourceType), facts)
	stripWorkspaceLaunchResourceBilling(row)
	if resourceType == "compute" {
		if err := app.tables.SaveCompute(ctx, row); err != nil {
			return "", err
		}
	} else if err := app.tables.SaveStorage(ctx, row); err != nil {
		return "", err
	}
	return state, nil
}

func (app *controlPlaneServer) keepWorkspaceLaunchReview(ctx context.Context, operation *workspaceLaunchOperation, code string) (map[string]any, error) {
	operation.Status, operation.ErrorCode = "manual_review", code
	releaseWorkspaceLaunchLease(operation)
	if err := app.persistWorkspaceLaunch(ctx, operation); err != nil {
		return nil, err
	}
	return workspaceLaunchRecoveryResponse(*operation)
}

func (app *controlPlaneServer) currentWorkspaceLaunchRecoveryResponse(ctx context.Context, operationID string) (map[string]any, error) {
	operation, ok, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errBillingReviewNotFound
	}
	return workspaceLaunchRecoveryResponse(operation)
}

func workspaceLaunchRecoveryResponse(operation workspaceLaunchOperation) (map[string]any, error) {
	result, err := workspaceLaunchResponse(workspaceLaunchOperationRow(operation))
	if err != nil {
		return nil, err
	}
	result["resourceType"], result["billingOperationId"] = "workspace", operation.ID
	result["allowedActions"] = []string{}
	if operation.Status == "manual_review" {
		result["allowedActions"] = []string{"recover_workspace_launch"}
	}
	return result, nil
}

func releaseWorkspaceLaunchLease(operation *workspaceLaunchOperation) {
	operation.LeaseToken, operation.LeaseExpiresAt = "", ""
}

func (app *controlPlaneServer) persistWorkspaceLaunch(ctx context.Context, operation *workspaceLaunchOperation) error {
	desired := workspaceLaunchOperationRow(*operation)
	if err := app.tables.PersistWorkspaceLaunch(ctx, workspaceLaunchPersistCAS{
		OperationID: operation.ID, ExpectedOperationResult: operation.PersistedResult, DesiredOperation: desired,
	}); err != nil {
		return err
	}
	operation.PersistedResult = stringValue(desired["result"])
	return nil
}

func (app *controlPlaneServer) retryWorkspaceLaunchDebit(ctx context.Context, operation *workspaceLaunchOperation, code string, cause error) error {
	if cause == nil {
		cause = errors.New(code)
	}
	operation.Status, operation.Phase, operation.ErrorCode = "unknown", "debit_pending", code
	releaseWorkspaceLaunchLease(operation)
	return errors.Join(cause, app.persistWorkspaceLaunch(ctx, operation))
}

func (app *controlPlaneServer) manualReviewWorkspaceLaunchDebit(ctx context.Context, operation *workspaceLaunchOperation, code string) error {
	operation.Status, operation.ErrorCode = "manual_review", code
	releaseWorkspaceLaunchLease(operation)
	return errors.Join(errors.New(code), app.persistWorkspaceLaunch(ctx, operation))
}

func (app *controlPlaneServer) debitWorkspaceLaunch(ctx context.Context, service *controlplane.Service, operation *workspaceLaunchOperation) error {
	unlockWallet := app.lockResource("sub2api-wallet", operation.AccountID)
	defer unlockWallet()
	userID, err := app.sub2APIUserID(ctx, operation.AccountID)
	if err != nil {
		return app.retryWorkspaceLaunchDebit(ctx, operation, errMonthlyAccountUnmapped.Error(), err)
	}
	key, err := service.Sub2APIWorkspaceKeyByID(ctx, userID, operation.WorkspaceAPIKeyID)
	if err != nil || key.ID != operation.WorkspaceAPIKeyID || key.UserID != userID || key.Name != workspaceReservedKeyName(operation.WorkspaceID) || key.Status != "active" {
		return app.retryWorkspaceLaunchDebit(ctx, operation, "gateway_key_unavailable", err)
	}
	if operation.ChargeConfirmation == nil {
		var charge clients.Sub2APICharge
		if operation.ChargeAttempted || operation.Status == "unknown" {
			history, historyErr := service.Sub2APIBalanceHistory(ctx, userID)
			row := map[string]any{"sub2apiRedeemCode": operation.RedeemCode, "chargeUsdMicros": operation.TotalChargeUSDMicros}
			switch code := sub2APIReconciliationCode(row, userID, history); {
			case historyErr != nil || code == "sub2api_charge_missing":
				charge, err = service.ChargeSub2API(ctx, clients.Sub2APIChargeInput{
					UserID: userID, Code: operation.RedeemCode, ChargeUSDMicros: operation.TotalChargeUSDMicros, Notes: "OPL Workspace launch " + operation.WorkspaceID,
				})
			case code != "":
				return app.manualReviewWorkspaceLaunchDebit(ctx, operation, code)
			default:
				charge = clients.Sub2APICharge{Code: operation.RedeemCode, UserID: userID, ChargeUSDMicros: operation.TotalChargeUSDMicros, Status: "used"}
			}
		} else {
			balance, balanceErr := service.Sub2APIBalance(ctx, userID)
			if balanceErr != nil {
				return app.retryWorkspaceLaunchDebit(ctx, operation, "sub2api_balance_unavailable", balanceErr)
			}
			if balance.USDMicros <= operation.TotalChargeUSDMicros {
				operation.Status, operation.Phase, operation.ErrorCode = "insufficient", "debit_pending", errMonthlyInsufficientBalance.Error()
				releaseWorkspaceLaunchLease(operation)
				if err := app.persistWorkspaceLaunch(ctx, operation); err != nil {
					return err
				}
				return errMonthlyInsufficientBalance
			}
			operation.PreChargeBalanceUSDMicros, operation.ChargeAttempted = balance.USDMicros, true
			if err := app.persistWorkspaceLaunch(ctx, operation); err != nil {
				return err
			}
			charge, err = service.ChargeSub2API(ctx, clients.Sub2APIChargeInput{
				UserID: userID, Code: operation.RedeemCode, ChargeUSDMicros: operation.TotalChargeUSDMicros, Notes: "OPL Workspace launch " + operation.WorkspaceID,
			})
		}
		if err != nil {
			if errors.Is(err, clients.ErrSub2APIChargeUnknown) {
				return app.retryWorkspaceLaunchDebit(ctx, operation, "sub2api_charge_unconfirmed", err)
			}
			if errors.Is(err, errMonthlyInsufficientBalance) {
				operation.Status, operation.Phase, operation.ErrorCode = "insufficient", "debit_pending", errMonthlyInsufficientBalance.Error()
				releaseWorkspaceLaunchLease(operation)
				return errors.Join(err, app.persistWorkspaceLaunch(ctx, operation))
			}
			return app.manualReviewWorkspaceLaunchDebit(ctx, operation, "sub2api_charge_unconfirmed")
		}
		confirmation := map[string]any{"code": charge.Code, "userId": charge.UserID, "chargeUsdMicros": charge.ChargeUSDMicros, "status": charge.Status}
		if !monthlyChargeConfirmationMatches(confirmation, operation.RedeemCode, userID, operation.TotalChargeUSDMicros) {
			return app.manualReviewWorkspaceLaunchDebit(ctx, operation, "sub2api_charge_confirmation_invalid")
		}
		operation.ChargeConfirmation, operation.ErrorCode = confirmation, ""
		if err := app.persistWorkspaceLaunch(ctx, operation); err != nil {
			return err
		}
	}
	history, historyErr := service.Sub2APIBalanceHistory(ctx, userID)
	row := map[string]any{"sub2apiRedeemCode": operation.RedeemCode, "chargeUsdMicros": operation.TotalChargeUSDMicros}
	if historyErr != nil || sub2APIReconciliationCode(row, userID, history) == "sub2api_charge_missing" {
		return app.retryWorkspaceLaunchDebit(ctx, operation, "sub2api_charge_history_unavailable", errors.Join(historyErr, clients.ErrSub2APIChargeUnknown))
	}
	if code := sub2APIReconciliationCode(row, userID, history); code != "" {
		return app.manualReviewWorkspaceLaunchDebit(ctx, operation, code)
	}
	postCharge, err := service.Sub2APIBalance(ctx, userID)
	if err != nil {
		return app.retryWorkspaceLaunchDebit(ctx, operation, "post_charge_balance_unavailable", err)
	}
	operation.PostChargeBalanceKnown, operation.PostChargeBalanceUSDMicros = true, postCharge.USDMicros
	if operation.PreChargeBalanceUSDMicros <= operation.TotalChargeUSDMicros || postCharge.USDMicros < 0 || postCharge.USDMicros != operation.PreChargeBalanceUSDMicros-operation.TotalChargeUSDMicros {
		return app.manualReviewWorkspaceLaunchDebit(ctx, operation, "post_charge_balance_invalid")
	}
	operation.Status, operation.Phase, operation.ErrorCode = "debited", "debited", ""
	releaseWorkspaceLaunchLease(operation)
	return app.persistWorkspaceLaunch(ctx, operation)
}

func (app *controlPlaneServer) waitWorkspaceLaunchFulfillment(ctx context.Context, operation *workspaceLaunchOperation) error {
	operation.Status, operation.ErrorCode = "waiting", ""
	releaseWorkspaceLaunchLease(operation)
	return app.persistWorkspaceLaunch(ctx, operation)
}

func (app *controlPlaneServer) retryWorkspaceLaunchFulfillment(ctx context.Context, operation *workspaceLaunchOperation, code string, cause error) error {
	if cause == nil {
		cause = errors.New(code)
	}
	operation.Status, operation.ErrorCode = "retryable", code
	releaseWorkspaceLaunchLease(operation)
	return errors.Join(cause, app.persistWorkspaceLaunch(ctx, operation))
}

func (app *controlPlaneServer) manualReviewWorkspaceLaunchFulfillment(ctx context.Context, operation *workspaceLaunchOperation, code string) error {
	operation.Status, operation.ErrorCode = "manual_review", code
	releaseWorkspaceLaunchLease(operation)
	return errors.Join(errors.New(code), app.persistWorkspaceLaunch(ctx, operation))
}

func (app *controlPlaneServer) refundWorkspaceLaunch(ctx context.Context, service *controlplane.Service, operation *workspaceLaunchOperation, reason string) error {
	unlockWallet := app.lockResource("sub2api-wallet", operation.AccountID)
	defer unlockWallet()
	userID, err := app.sub2APIUserID(ctx, operation.AccountID)
	if err != nil {
		return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_refund_account_unmapped")
	}
	if operation.RefundConfirmation != nil {
		return app.recordWorkspaceLaunchRefundReceipt(ctx, service, operation, userID)
	}
	recoverAttempt := operation.RefundAttempted
	if !operation.RefundAttempted {
		operation.Status, operation.Phase, operation.RefundAttempted, operation.RefundReason, operation.ErrorCode = "refund_pending", "refund_pending", true, reason, ""
		if err := app.persistWorkspaceLaunch(ctx, operation); err != nil {
			return err
		}
	}
	var refund clients.Sub2APIRefund
	if recoverAttempt {
		history, historyErr := service.Sub2APIBalanceHistory(ctx, userID)
		matches := make([]clients.Sub2APIBalanceHistoryEntry, 0, 1)
		for _, entry := range history {
			if entry.Code == operation.RefundCode {
				matches = append(matches, entry)
			}
		}
		if historyErr != nil || len(matches) == 0 {
			refund, err = service.RefundSub2API(ctx, clients.Sub2APIRefundInput{
				UserID: userID, Code: operation.RefundCode, RefundUSDMicros: operation.TotalChargeUSDMicros, Notes: "OPL Workspace launch refund " + operation.WorkspaceID,
			})
		} else {
			entry := matches[0]
			if len(matches) != 1 || entry.Type != "balance" || entry.Status != "used" || entry.UsedBy == nil || *entry.UsedBy != userID || entry.ValueUSDMicros != operation.TotalChargeUSDMicros {
				return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "sub2api_refund_mismatch")
			}
			refund = clients.Sub2APIRefund{Code: operation.RefundCode, UserID: userID, RefundUSDMicros: operation.TotalChargeUSDMicros, Status: "used"}
		}
	} else {
		refund, err = service.RefundSub2API(ctx, clients.Sub2APIRefundInput{
			UserID: userID, Code: operation.RefundCode, RefundUSDMicros: operation.TotalChargeUSDMicros, Notes: "OPL Workspace launch refund " + operation.WorkspaceID,
		})
	}
	if err != nil || refund.Code != operation.RefundCode || refund.UserID != userID || refund.RefundUSDMicros != operation.TotalChargeUSDMicros || refund.Status != "used" {
		return app.retryWorkspaceLaunchFulfillment(ctx, operation, "sub2api_refund_unconfirmed", errors.Join(err, clients.ErrSub2APIChargeUnknown))
	}
	operation.RefundConfirmation = map[string]any{"code": refund.Code, "userId": refund.UserID, "refundUsdMicros": refund.RefundUSDMicros, "status": refund.Status}
	if err := app.persistWorkspaceLaunch(ctx, operation); err != nil {
		return err
	}
	return app.recordWorkspaceLaunchRefundReceipt(ctx, service, operation, userID)
}

func (app *controlPlaneServer) recordWorkspaceLaunchRefundReceipt(ctx context.Context, service *controlplane.Service, operation *workspaceLaunchOperation, userID int64) error {
	components, _, _, err := workspaceLaunchComponents(*operation)
	if err != nil {
		return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_refund_price_invalid")
	}
	cost := map[string]any{
		"priceVersion": operation.PriceVersion, "currency": pricingCurrency, "billingUnit": pricingBillingUnit, "totalUsdMicros": operation.TotalChargeUSDMicros,
		"sub2apiUserId": userID, "sub2apiRedeemCode": operation.RedeemCode, "sub2apiRefundCode": operation.RefundCode,
		"refundUsdMicros": operation.TotalChargeUSDMicros, "periodStart": operation.PeriodStart, "paidThrough": operation.PaidThrough,
		"resourceType": "workspace", "resourceId": operation.WorkspaceID, "components": components,
	}
	receipt, err := service.RecordMonthlyReceipt(ctx, clients.ReceiptInput{
		Type: "billing.workspace_refunded.v1", Status: "completed", Surface: "control_plane", AccountID: operation.AccountID,
		WorkspaceID: operation.WorkspaceID, RequestID: operation.ID,
		Execution: map[string]any{
			"resourceType": "workspace", "resourceId": operation.WorkspaceID, "reason": operation.RefundReason,
			"computeAllocationId": operation.ComputeID, "storageId": operation.StorageID, "refundConfirmation": operation.RefundConfirmation,
		},
		Cost: cost, Owner: map[string]any{"accountId": operation.AccountID, "workspaceId": operation.WorkspaceID, "ownerUserId": operation.OwnerUserID},
	}, operation.ID+":refund-receipt")
	if err != nil {
		return app.retryWorkspaceLaunchFulfillment(ctx, operation, "ledger_refund_receipt_pending", err)
	}
	if receipt.ReceiptID == "" {
		return app.retryWorkspaceLaunchFulfillment(ctx, operation, "ledger_refund_receipt_invalid", errors.New("Ledger refund receipt ID missing"))
	}
	operation.RefundReceiptID, operation.ReceiptID = receipt.ReceiptID, receipt.ReceiptID
	operation.Status, operation.Phase, operation.ErrorCode = "refunded", "refunded", ""
	releaseWorkspaceLaunchLease(operation)
	return app.persistWorkspaceLaunch(ctx, operation)
}

func (app *controlPlaneServer) recordWorkspaceLaunchPurchaseReceipt(ctx context.Context, service *controlplane.Service, operation *workspaceLaunchOperation) error {
	workspace, ok := app.getWorkspace(operation.WorkspaceID)
	if !ok || !workspaceMatchesLaunch(workspace, *operation) || stringValue(workspace["runtimeId"]) != operation.RuntimeID {
		return app.retryWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_projection_unavailable", errors.New("Workspace projection unavailable"))
	}
	userID, err := app.sub2APIUserID(ctx, operation.AccountID)
	if err != nil {
		return app.retryWorkspaceLaunchFulfillment(ctx, operation, errMonthlyAccountUnmapped.Error(), err)
	}
	components, _, _, err := workspaceLaunchComponents(*operation)
	if err != nil {
		return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_receipt_price_invalid")
	}
	receipt, err := service.RecordMonthlyReceipt(ctx, clients.ReceiptInput{
		Type: "billing.workspace_purchased.v1", Status: "completed", Surface: "control_plane", AccountID: operation.AccountID,
		WorkspaceID: operation.WorkspaceID, RequestID: operation.ID,
		Execution: map[string]any{
			"resourceType": "workspace", "resourceId": operation.WorkspaceID, "computeAllocationId": operation.ComputeID,
			"storageId": operation.StorageID, "attachmentId": operation.AttachmentID, "workspaceApiKeyId": operation.WorkspaceAPIKeyID,
			"workspaceKeyFingerprint": operation.WorkspaceKeyFingerprint, "runtimeId": operation.RuntimeID, "runtimeServiceName": operation.RuntimeServiceName,
		},
		Cost: map[string]any{
			"priceVersion": operation.PriceVersion, "currency": pricingCurrency, "billingUnit": pricingBillingUnit, "totalUsdMicros": operation.TotalChargeUSDMicros,
			"sub2apiUserId": userID, "sub2apiRedeemCode": operation.RedeemCode, "postChargeBalanceUsdMicros": operation.PostChargeBalanceUSDMicros,
			"periodStart": operation.PeriodStart, "paidThrough": operation.PaidThrough, "resourceType": "workspace", "resourceId": operation.WorkspaceID,
			"components": components,
		},
		Owner: map[string]any{"accountId": operation.AccountID, "workspaceId": operation.WorkspaceID, "ownerUserId": operation.OwnerUserID},
	}, operation.ID+":purchase-receipt")
	if err != nil {
		return app.retryWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_receipt_retryable", err)
	}
	if receipt.ReceiptID == "" {
		return app.retryWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_receipt_invalid", errors.New("Ledger purchase receipt ID missing"))
	}
	workspace["purchaseReceiptId"] = receipt.ReceiptID
	if err := app.tables.SaveWorkspace(ctx, workspace); err != nil {
		return app.retryWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_receipt_projection_retryable", err)
	}
	operation.ReceiptID, operation.Status, operation.Phase, operation.ErrorCode = receipt.ReceiptID, "succeeded", "succeeded", ""
	releaseWorkspaceLaunchLease(operation)
	return app.persistWorkspaceLaunch(ctx, operation)
}

func (app *controlPlaneServer) workspaceLaunchAttachment(operation workspaceLaunchOperation) (map[string]any, bool) {
	for _, attachment := range app.listAttachments(operation.AccountID) {
		if stringValue(attachment["operationId"]) == operation.AttachmentOperationID && attachmentMatchesLaunch(attachment, operation) {
			return attachment, true
		}
	}
	return nil, false
}

func attachmentMatchesLaunch(attachment map[string]any, operation workspaceLaunchOperation) bool {
	return stringValue(attachment["workspaceId"]) == operation.WorkspaceID &&
		firstNonEmpty(stringValue(attachment["computeAllocationId"]), stringValue(attachment["computeId"])) == operation.ComputeID &&
		firstNonEmpty(stringValue(attachment["storageId"]), stringValue(attachment["volumeId"])) == operation.StorageID
}

func workspaceMatchesLaunch(workspace map[string]any, operation workspaceLaunchOperation) bool {
	return firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"])) == operation.AccountID &&
		firstNonEmpty(stringValue(workspace["ownerUserId"]), stringValue(workspace["ownerId"])) == operation.OwnerUserID &&
		stringValue(workspace["packageId"]) == operation.PackageID &&
		firstNonEmpty(stringValue(workspace["computeAllocationId"]), stringValue(workspace["currentComputeAllocationId"])) == operation.ComputeID &&
		stringValue(workspace["storageId"]) == operation.StorageID && int64(numberField(workspace, "workspaceApiKeyId", 0)) == operation.WorkspaceAPIKeyID &&
		firstNonEmpty(stringValue(workspace["attachmentId"]), stringValue(workspace["currentAttachmentId"])) == operation.AttachmentID
}

func workspaceProjectionMatchesLaunch(workspace domain.WorkspaceProjection, operation workspaceLaunchOperation) bool {
	return workspace.ID == operation.WorkspaceID && workspace.AccountID == operation.AccountID && workspace.OwnerID == operation.OwnerUserID &&
		workspace.PackageID == operation.PackageID && workspace.ComputeID == operation.ComputeID && workspace.VolumeID == operation.StorageID && workspace.AttachmentID == operation.AttachmentID && workspace.WorkspaceAPIKeyID == operation.WorkspaceAPIKeyID
}
