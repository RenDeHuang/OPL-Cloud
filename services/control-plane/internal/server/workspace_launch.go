package server

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"opl-cloud/services/control-plane/internal/controlplane"
	"opl-cloud/services/control-plane/internal/domain"
)

var (
	errInvalidWorkspaceLaunchOperation = errors.New("invalid_workspace_launch_operation")
	errWorkspaceLaunchInProgress       = errors.New("workspace_launch_in_progress")
)

type workspaceLaunchOperation struct {
	ID                        string `json:"-"`
	Status                    string `json:"-"`
	RequestHash               string `json:"requestHash"`
	Phase                     string `json:"phase"`
	AccountID                 string `json:"accountId"`
	OwnerUserID               string `json:"ownerUserId"`
	WorkspaceID               string `json:"workspaceId"`
	Name                      string `json:"name"`
	PackageID                 string `json:"packageId"`
	StorageGB                 int    `json:"sizeGb"`
	PricingVersion            string `json:"pricingVersion"`
	TotalMonthlyPriceCNYCents int64  `json:"totalMonthlyPriceCnyCents"`
	TotalChargeUSDMicros      int64  `json:"totalChargeUsdMicros"`
	ComputeID                 string `json:"computeAllocationId"`
	ComputeBillingOperationID string `json:"computeBillingOperationId"`
	StorageID                 string `json:"storageId"`
	StorageBillingOperationID string `json:"storageBillingOperationId"`
	AttachmentID              string `json:"attachmentId,omitempty"`
	AttachmentOperationID     string `json:"attachmentOperationId"`
	WorkspaceOperationID      string `json:"workspaceOperationId"`
	RuntimeServiceName        string `json:"runtimeServiceName,omitempty"`
	URL                       string `json:"url,omitempty"`
	ReceiptID                 string `json:"receiptId,omitempty"`
	ErrorCode                 string `json:"errorCode,omitempty"`
}

func encodeWorkspaceLaunchOperation(operation workspaceLaunchOperation) string {
	payload, _ := json.Marshal(operation)
	return string(payload)
}

func newWorkspaceLaunchOperation(accountID, ownerUserID, name, packageID string, storageGB int, pricingVersion string, totalMonthlyPriceCNYCents, totalChargeUSDMicros int64, key string) workspaceLaunchOperation {
	operationID := "workspace-launch-" + stableID(accountID, key)[:18]
	workspaceID := primaryWorkspaceID(accountID)
	return workspaceLaunchOperation{
		ID: operationID, Status: "preparing", Phase: "compute",
		RequestHash: stableID("workspace-launch-v1", accountID, ownerUserID, name, packageID, strconv.Itoa(storageGB), pricingVersion),
		AccountID:   accountID, OwnerUserID: ownerUserID, WorkspaceID: workspaceID, Name: name, PackageID: packageID,
		StorageGB: storageGB, PricingVersion: pricingVersion, TotalMonthlyPriceCNYCents: totalMonthlyPriceCNYCents, TotalChargeUSDMicros: totalChargeUSDMicros,
		ComputeID: resourceIDForMutation("ca", accountID, operationID+":compute"), ComputeBillingOperationID: "billing-" + stableID("compute", accountID, operationID)[:18],
		StorageID: resourceIDForMutation("vol", accountID, operationID+":storage"), StorageBillingOperationID: "billing-" + stableID("storage", accountID, operationID)[:18],
		AttachmentOperationID: operationID + ":attachment", WorkspaceOperationID: operationID + ":workspace",
	}
}

func decodeWorkspaceLaunchOperation(row map[string]any) (workspaceLaunchOperation, error) {
	var operation workspaceLaunchOperation
	if err := json.Unmarshal([]byte(stringValue(row["result"])), &operation); err != nil {
		return workspaceLaunchOperation{}, errInvalidWorkspaceLaunchOperation
	}
	operation.ID = firstNonEmpty(stringValue(row["operationId"]), stringValue(row["id"]))
	operation.Status = stringValue(row["status"])
	if operation.ID == "" || operation.Status == "" || operation.RequestHash == "" || operation.AccountID == "" || operation.WorkspaceID == "" {
		return workspaceLaunchOperation{}, errInvalidWorkspaceLaunchOperation
	}
	for field, want := range map[string]string{
		"accountId": operation.AccountID, "workspaceId": operation.WorkspaceID, "resourceId": operation.WorkspaceID,
		"resourceKind": "workspace_launch", "action": "workspace.launch",
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
		"resourceId": operation.WorkspaceID, "resourceKind": "workspace_launch", "action": "workspace.launch", "status": operation.Status,
		"result": encodeWorkspaceLaunchOperation(operation), "computeAllocationId": operation.ComputeID, "storageId": operation.StorageID,
		"attachmentId": operation.AttachmentID, "runtimeServiceName": operation.RuntimeServiceName,
	}
}

func workspaceLaunchResponse(row map[string]any) (map[string]any, error) {
	operation, err := decodeWorkspaceLaunchOperation(row)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"operationId": operation.ID, "status": operation.Status, "phase": operation.Phase,
		"accountId": operation.AccountID, "workspaceId": operation.WorkspaceID, "name": operation.Name,
		"packageId": operation.PackageID, "sizeGb": operation.StorageGB, "pricingVersion": operation.PricingVersion,
		"totalMonthlyPriceCnyCents": operation.TotalMonthlyPriceCNYCents, "totalChargeUsdMicros": operation.TotalChargeUSDMicros,
		"computeAllocationId": operation.ComputeID, "storageId": operation.StorageID, "attachmentId": operation.AttachmentID,
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
		if stringValue(row["action"]) != "workspace.launch" || stringValue(row["status"]) == "succeeded" {
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
	if err != nil || !ok || operation.Status == "succeeded" {
		return err
	}
	unlock := app.lockResource("workspace-launch", operation.AccountID)
	defer unlock()
	operation, ok, err = app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.Status == "succeeded" {
		return err
	}

	for range 6 {
		switch operation.Phase {
		case "compute":
			row, err := app.purchaseMonthlyResource(ctx, service, monthlyPurchaseInput{
				ResourceType: "compute", ResourceID: operation.ComputeID, BillingOperationID: operation.ComputeBillingOperationID,
				AccountID: operation.AccountID, OwnerUserID: operation.OwnerUserID, WorkspaceID: operation.WorkspaceID,
				Name: operation.Name, PackageID: operation.PackageID, Zone: monthlyComputeLaunchZone(), Environment: monthlyEnvironment(),
			})
			if err != nil {
				return app.failWorkspaceLaunchPurchase(ctx, operation, row, err)
			}
			if stringValue(row["billingStatus"]) != "active" {
				return app.waitWorkspaceLaunch(ctx, operation)
			}
			operation.Phase, operation.Status, operation.ErrorCode = "storage", "preparing", ""
			if err := app.saveWorkspaceLaunchOperation(ctx, operation); err != nil {
				return err
			}

		case "storage":
			compute, ok := app.getCompute(operation.ComputeID)
			if !ok {
				return app.retryWorkspaceLaunch(ctx, operation, "workspace_launch_compute_missing")
			}
			zone := firstNonEmpty(stringValue(compute["zone"]), providerDataValue(compute, "zone"))
			if zone == "" {
				return app.retryWorkspaceLaunch(ctx, operation, "workspace_launch_compute_zone_unavailable")
			}
			row, err := app.purchaseMonthlyResource(ctx, service, monthlyPurchaseInput{
				ResourceType: "storage", ResourceID: operation.StorageID, BillingOperationID: operation.StorageBillingOperationID,
				AccountID: operation.AccountID, OwnerUserID: operation.OwnerUserID, WorkspaceID: operation.WorkspaceID,
				Name: operation.Name, PackageID: operation.PackageID, SizeGB: operation.StorageGB, ComputeID: operation.ComputeID,
				Zone: zone, Environment: monthlyEnvironment(),
			})
			if err != nil {
				return app.failWorkspaceLaunchPurchase(ctx, operation, row, err)
			}
			if stringValue(row["billingStatus"]) != "active" {
				return app.waitWorkspaceLaunch(ctx, operation)
			}
			operation.Phase, operation.Status, operation.ErrorCode = "attachment", "preparing", ""
			if err := app.saveWorkspaceLaunchOperation(ctx, operation); err != nil {
				return err
			}

		case "attachment":
			if attachment, ok := app.workspaceLaunchAttachment(operation); ok {
				operation.AttachmentID = stringValue(attachment["id"])
			} else {
				created, err := service.CreateStorageAttachment(ctx, controlplane.StorageAttachmentInput{
					WorkspaceID: operation.WorkspaceID, ComputeID: operation.ComputeID, VolumeID: operation.StorageID,
				}, operation.AttachmentOperationID)
				if err != nil {
					return app.retryWorkspaceLaunch(ctx, operation, "workspace_launch_attachment_retryable")
				}
				if created.ID == "" || created.WorkspaceID != operation.WorkspaceID || created.ComputeID != operation.ComputeID || created.VolumeID != operation.StorageID {
					return app.manualReviewWorkspaceLaunch(ctx, operation, "workspace_launch_attachment_identity_mismatch")
				}
				body := attachmentResponse(structToMap(created), map[string]any{
					"computeAllocationId": operation.ComputeID, "storageId": operation.StorageID, "workspaceId": operation.WorkspaceID,
				})
				body["accountId"], body["packageId"], body["operationId"] = operation.AccountID, operation.PackageID, operation.AttachmentOperationID
				if err := app.saveAttachmentFact(body, body); err != nil {
					return app.retryWorkspaceLaunch(ctx, operation, "workspace_launch_attachment_persist_retryable")
				}
				operation.AttachmentID = created.ID
			}
			operation.Phase, operation.Status, operation.ErrorCode = "workspace", "preparing", ""
			if err := app.saveWorkspaceLaunchOperation(ctx, operation); err != nil {
				return err
			}

		case "workspace":
			if workspace, ok := app.getWorkspace(operation.WorkspaceID); ok {
				if !workspaceMatchesLaunch(workspace, operation) {
					return app.manualReviewWorkspaceLaunch(ctx, operation, "workspace_launch_projection_identity_mismatch")
				}
				if stringValue(workspace["runtimeId"]) != "" {
					operation.RuntimeServiceName = firstNonEmpty(stringValue(workspace["runtimeServiceName"]), stringValue(nested(workspace, "runtime", "serviceName")))
					operation.URL = stringValue(workspace["url"])
					operation.Phase, operation.Status, operation.ErrorCode = "receipt", "preparing", ""
					if err := app.saveWorkspaceLaunchOperation(ctx, operation); err != nil {
						return err
					}
					continue
				}
			}
			sub2APIUserID, err := app.sub2APIUserID(ctx, operation.AccountID)
			if err != nil {
				return app.retryWorkspaceLaunch(ctx, operation, "workspace_launch_account_mapping_unavailable")
			}
			workspace, err := service.PrepareWorkspace(ctx, controlplane.CreateWorkspaceInput{
				WorkspaceID: operation.WorkspaceID, AccountID: operation.AccountID, Sub2APIUserID: sub2APIUserID,
				OwnerID: operation.OwnerUserID, Name: operation.Name, PackageID: operation.PackageID,
				AttachmentID: operation.AttachmentID, ComputeID: operation.ComputeID, VolumeID: operation.StorageID,
			}, operation.WorkspaceOperationID)
			if err != nil {
				return app.retryWorkspaceLaunch(ctx, operation, "workspace_launch_runtime_retryable")
			}
			if !workspaceProjectionMatchesLaunch(workspace, operation) {
				return app.manualReviewWorkspaceLaunch(ctx, operation, "workspace_launch_runtime_identity_mismatch")
			}
			if err := app.saveWorkspaceProjection(workspace); err != nil {
				return app.retryWorkspaceLaunch(ctx, operation, "workspace_launch_projection_persist_retryable")
			}
			operation.RuntimeServiceName, operation.URL = workspace.RuntimeServiceName, workspace.URL
			operation.Phase, operation.Status, operation.ErrorCode = "receipt", "preparing", ""
			if err := app.saveWorkspaceLaunchOperation(ctx, operation); err != nil {
				return err
			}

		case "receipt":
			workspace, ok := app.getWorkspace(operation.WorkspaceID)
			if !ok || !workspaceMatchesLaunch(workspace, operation) {
				return app.retryWorkspaceLaunch(ctx, operation, "workspace_launch_projection_unavailable")
			}
			recorded, err := service.RecordWorkspaceCreatedReceipt(ctx, domain.WorkspaceProjection{
				ID: operation.WorkspaceID, AccountID: operation.AccountID, URL: stringValue(workspace["url"]), RuntimeID: stringValue(workspace["runtimeId"]),
			}, operation.WorkspaceOperationID)
			if err != nil {
				return app.retryWorkspaceLaunch(ctx, operation, "workspace_launch_receipt_retryable")
			}
			workspace["receiptId"] = recorded.ReceiptID
			if err := app.tables.SaveWorkspace(ctx, workspace); err != nil {
				return app.retryWorkspaceLaunch(ctx, operation, "workspace_launch_receipt_projection_retryable")
			}
			operation.ReceiptID, operation.Phase, operation.Status, operation.ErrorCode = recorded.ReceiptID, "complete", "succeeded", ""
			return app.saveWorkspaceLaunchOperation(ctx, operation)

		case "complete":
			return nil
		default:
			return app.manualReviewWorkspaceLaunch(ctx, operation, "workspace_launch_phase_invalid")
		}
	}
	return app.retryWorkspaceLaunch(ctx, operation, "workspace_launch_transition_limit")
}

func (app *controlPlaneServer) workspaceLaunchOperation(ctx context.Context, operationID string) (workspaceLaunchOperation, bool, error) {
	rows, err := app.tables.ListRuntimeOperations(ctx)
	if err != nil {
		return workspaceLaunchOperation{}, false, err
	}
	for _, row := range rows {
		if stringValue(row["id"]) != operationID || stringValue(row["action"]) != "workspace.launch" {
			continue
		}
		operation, err := decodeWorkspaceLaunchOperation(row)
		return operation, err == nil, err
	}
	return workspaceLaunchOperation{}, false, nil
}

func (app *controlPlaneServer) saveWorkspaceLaunchOperation(ctx context.Context, operation workspaceLaunchOperation) error {
	return app.tables.SaveRuntimeOperation(ctx, workspaceLaunchOperationRow(operation))
}

func (app *controlPlaneServer) waitWorkspaceLaunch(ctx context.Context, operation workspaceLaunchOperation) error {
	operation.Status, operation.ErrorCode = "waiting", ""
	return app.saveWorkspaceLaunchOperation(ctx, operation)
}

func (app *controlPlaneServer) retryWorkspaceLaunch(ctx context.Context, operation workspaceLaunchOperation, code string) error {
	operation.Status, operation.ErrorCode = "retryable", code
	if err := app.saveWorkspaceLaunchOperation(ctx, operation); err != nil {
		return err
	}
	return errors.New(code)
}

func (app *controlPlaneServer) manualReviewWorkspaceLaunch(ctx context.Context, operation workspaceLaunchOperation, code string) error {
	operation.Status, operation.ErrorCode = "manual_review", code
	if err := app.saveWorkspaceLaunchOperation(ctx, operation); err != nil {
		return err
	}
	return errors.New(code)
}

func (app *controlPlaneServer) failWorkspaceLaunchPurchase(ctx context.Context, operation workspaceLaunchOperation, row map[string]any, cause error) error {
	status := stringValue(row["billingStatus"])
	if status == "manual_review" || status == "failed" || errors.Is(cause, errMonthlyChargeNeedsReview) || errors.Is(cause, errMonthlyInsufficientBalance) || errors.Is(cause, errMonthlyPurchaseRefunded) || errors.Is(cause, errIdempotencyConflict) {
		return app.manualReviewWorkspaceLaunch(ctx, operation, "workspace_launch_"+operation.Phase+"_manual_review")
	}
	return app.retryWorkspaceLaunch(ctx, operation, "workspace_launch_"+operation.Phase+"_retryable")
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
		stringValue(workspace["storageId"]) == operation.StorageID &&
		firstNonEmpty(stringValue(workspace["attachmentId"]), stringValue(workspace["currentAttachmentId"])) == operation.AttachmentID
}

func workspaceProjectionMatchesLaunch(workspace domain.WorkspaceProjection, operation workspaceLaunchOperation) bool {
	return workspace.ID == operation.WorkspaceID && workspace.AccountID == operation.AccountID && workspace.OwnerID == operation.OwnerUserID &&
		workspace.PackageID == operation.PackageID && workspace.ComputeID == operation.ComputeID && workspace.VolumeID == operation.StorageID && workspace.AttachmentID == operation.AttachmentID
}
