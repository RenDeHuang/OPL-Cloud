package server

import (
	"context"
	"errors"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/domain"
)

func (a *controlPlaneWorkspaceLaunchStageAdapter) readWorkspaceLaunchActivation(ctx context.Context, operation workspaceLaunchReconcileOperation) (workspaceLaunchStageObservation, error) {
	workspace, found, err := a.app.tables.GetWorkspace(ctx, operation.stringFact("workspaceId"))
	if err != nil {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, err
	}
	if !found {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}, nil
	}
	if !workspaceLaunchProjectionMatches(operation, workspace) {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, nil
	}
	activatedAt := firstNonEmpty(stringValue(workspace["activatedAt"]), stringValue(workspace["updatedAt"]), stringValue(workspace["createdAt"]))
	if activatedAt == "" {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, nil
	}
	return workspaceLaunchStageObservation{State: workspaceLaunchStageReady, Facts: map[string]any{
		"activationOperationId": operation.ID + ":activation", "workspaceActivatedAt": activatedAt,
	}}, nil
}

func (a *controlPlaneWorkspaceLaunchStageAdapter) mutateWorkspaceLaunchActivation(ctx context.Context, operation workspaceLaunchReconcileOperation, idempotencyKey string) error {
	if idempotencyKey != operation.ID+":activation" {
		return errInvalidWorkspaceLaunchOperation
	}
	row, err := workspaceLaunchActivationRow(operation)
	if err != nil {
		return err
	}
	_, err = a.app.tables.ActivateWorkspaceLaunchProjection(ctx, row)
	return err
}

func workspaceLaunchActivationRow(operation workspaceLaunchReconcileOperation) (map[string]any, error) {
	quote, err := workspacePricingPreview(defaultPricingCatalog(), map[string]any{"packageId": operation.stringFact("packageId"), "sizeGb": operation.intFact("sizeGb")})
	if err != nil {
		return nil, err
	}
	computePrice, computeOK := requiredPositiveInteger(mapField(quote, "compute"), "chargeUsdMicros")
	storagePrice, storageOK := requiredPositiveInteger(mapField(quote, "storage"), "chargeUsdMicros")
	paidThrough, paidThroughErr := time.Parse(time.RFC3339, operation.stringFact("paidThrough"))
	if !computeOK || !storageOK || paidThroughErr != nil {
		return nil, errInvalidWorkspaceLaunchOperation
	}
	row := workspaceProjectionRow(domain.WorkspaceProjection{
		ID: operation.stringFact("workspaceId"), AccountID: operation.stringFact("accountId"), OwnerID: operation.stringFact("ownerUserId"),
		Name: operation.stringFact("name"), PackageID: operation.stringFact("packageId"), Provider: "fabric", URL: operation.stringFact("url"), Status: "running",
		ComputeID: operation.stringFact("computeAllocationId"), VolumeID: operation.stringFact("storageId"), AttachmentID: operation.stringFact("attachmentId"),
		RuntimeID: operation.stringFact("runtimeId"), RuntimeServiceName: operation.stringFact("runtimeServiceName"), WorkspaceAPIKeyID: operation.int64Fact("workspaceApiKeyId"),
		RuntimeReady: true, RuntimeUsername: operation.stringFact("runtimeUsername"), CredentialStatus: operation.stringFact("credentialStatus"),
		CredentialVersion: operation.stringFact("credentialVersion"), CredentialSecretRef: operation.stringFact("credentialSecretRef"),
	})
	for key, value := range map[string]any{
		"autoRenew": false, "authorizedBy": "", "authorizedAt": "", "priceVersion": operation.stringFact("priceVersion"), "currency": pricingCurrency,
		"billingUnit": pricingBillingUnit, "computeUsdMicros": computePrice, "storageUsdMicros": storagePrice, "totalUsdMicros": operation.int64Fact("totalChargeUsdMicros"),
		"periodStart": operation.stringFact("periodStart"), "paidThrough": operation.stringFact("paidThrough"), "nextRenewalAt": paidThrough.Add(-24 * time.Hour).Format(time.RFC3339Nano),
		"billingAnchorDay": operation.intFact("billingAnchorDay"), "renewalStatus": "active", "computeAllocationId": operation.stringFact("computeAllocationId"),
		"storageId": operation.stringFact("storageId"), "storageGb": operation.intFact("sizeGb"), "activatedAt": time.Now().UTC().Format(time.RFC3339Nano),
	} {
		row[key] = value
	}
	return row, nil
}

func workspaceLaunchProjectionMatches(operation workspaceLaunchReconcileOperation, workspace map[string]any) bool {
	return firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"])) == operation.stringFact("accountId") &&
		stringValue(workspace["ownerUserId"]) == operation.stringFact("ownerUserId") && stringValue(workspace["id"]) == operation.stringFact("workspaceId") &&
		firstNonEmpty(stringValue(workspace["currentComputeAllocationId"]), stringValue(workspace["computeAllocationId"])) == operation.stringFact("computeAllocationId") &&
		stringValue(workspace["storageId"]) == operation.stringFact("storageId") &&
		firstNonEmpty(stringValue(workspace["currentAttachmentId"]), stringValue(workspace["attachmentId"])) == operation.stringFact("attachmentId") &&
		stringValue(workspace["runtimeId"]) == operation.stringFact("runtimeId") &&
		stringValue(nested(workspace, "runtime", "serviceName")) == operation.stringFact("runtimeServiceName") &&
		firstNonEmpty(stringValue(workspace["state"]), stringValue(workspace["status"])) == "running"
}

func (a *controlPlaneWorkspaceLaunchStageAdapter) readWorkspaceLaunchReceipt(ctx context.Context, operation workspaceLaunchReconcileOperation) (workspaceLaunchStageObservation, error) {
	input, err := workspaceLaunchPurchaseReceiptInput(operation)
	if err != nil {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, err
	}
	receipt, found, err := workspaceLaunchPurchaseReceiptFromLedger(ctx, a, input)
	if err != nil {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, err
	}
	if !found {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}, nil
	}
	return workspaceLaunchStageObservation{State: workspaceLaunchStageReady, Facts: map[string]any{
		"receiptId": receipt.ReceiptID, "receiptOperationId": operation.ID + ":purchase-receipt",
	}}, nil
}

func (a *controlPlaneWorkspaceLaunchStageAdapter) mutateWorkspaceLaunchReceipt(ctx context.Context, operation workspaceLaunchReconcileOperation, idempotencyKey string) error {
	if idempotencyKey != operation.ID+":purchase-receipt" {
		return errInvalidWorkspaceLaunchOperation
	}
	input, err := workspaceLaunchPurchaseReceiptInput(operation)
	if err != nil {
		return err
	}
	_, err = a.service.RecordMonthlyReceipt(ctx, input, idempotencyKey)
	return err
}

func workspaceLaunchPurchaseReceiptInput(operation workspaceLaunchReconcileOperation) (clients.ReceiptInput, error) {
	quote, err := workspacePricingPreview(defaultPricingCatalog(), map[string]any{"packageId": operation.stringFact("packageId"), "sizeGb": operation.intFact("sizeGb")})
	if err != nil {
		return clients.ReceiptInput{}, err
	}
	computePrice, computeOK := requiredPositiveInteger(mapField(quote, "compute"), "chargeUsdMicros")
	storagePrice, storageOK := requiredPositiveInteger(mapField(quote, "storage"), "chargeUsdMicros")
	if !computeOK || !storageOK {
		return clients.ReceiptInput{}, errors.New("workspace_launch_pricing_snapshot_invalid")
	}
	return clients.ReceiptInput{
		Type: "billing.workspace_purchased.v1", Status: "completed", Surface: "control_plane", AccountID: operation.stringFact("accountId"),
		WorkspaceID: operation.stringFact("workspaceId"), RequestID: operation.ID,
		Execution: map[string]any{"resourceType": "workspace", "resourceId": operation.stringFact("workspaceId"), "computeAllocationId": operation.stringFact("computeAllocationId"),
			"storageId": operation.stringFact("storageId"), "attachmentId": operation.stringFact("attachmentId"), "workspaceApiKeyId": operation.int64Fact("workspaceApiKeyId"),
			"workspaceKeyFingerprint": operation.stringFact("workspaceKeyFingerprint"), "runtimeId": operation.stringFact("runtimeId"), "runtimeServiceName": operation.stringFact("runtimeServiceName")},
		Cost: map[string]any{"priceVersion": operation.stringFact("priceVersion"), "currency": pricingCurrency, "billingUnit": pricingBillingUnit,
			"totalUsdMicros": operation.int64Fact("totalChargeUsdMicros"), "sub2apiUserId": operation.int64Fact("sub2apiUserId"),
			"sub2apiRedeemCode": operation.stringFact("sub2apiRedeemCode"), "postChargeBalanceUsdMicros": operation.int64Fact("postChargeBalanceUsdMicros"),
			"periodStart": operation.stringFact("periodStart"), "paidThrough": operation.stringFact("paidThrough"), "resourceType": "workspace", "resourceId": operation.stringFact("workspaceId"),
			"components": map[string]any{"compute": map[string]any{"resourceType": "compute", "resourceId": operation.stringFact("computeAllocationId"), "chargeUsdMicros": computePrice},
				"storage": map[string]any{"resourceType": "storage", "resourceId": operation.stringFact("storageId"), "sizeGb": int64(operation.intFact("sizeGb")), "chargeUsdMicros": storagePrice}}},
		Owner: map[string]any{"accountId": operation.stringFact("accountId"), "workspaceId": operation.stringFact("workspaceId"), "ownerUserId": operation.stringFact("ownerUserId")},
	}, nil
}
