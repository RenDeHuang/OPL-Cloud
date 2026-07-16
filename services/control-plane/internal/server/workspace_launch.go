package server

import (
	"encoding/json"
	"errors"
	"strconv"
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
