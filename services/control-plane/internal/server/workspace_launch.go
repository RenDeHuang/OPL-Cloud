package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
	"opl-cloud/services/control-plane/internal/domain"
)

var (
	errInvalidWorkspaceLaunchOperation = errors.New("invalid_workspace_launch_operation")
	errWorkspaceLaunchInProgress       = errors.New("workspace_launch_in_progress")
	errWorkspaceLaunchCASConflict      = errors.New("workspace_launch_cas_conflict")
	errWorkspaceComputeClaimInvalid    = errors.New("workspace_compute_claim_invalid")
	errWorkspaceComputeClaimIdentity   = errors.New("workspace_compute_claim_identity_mismatch")
	errWorkspaceComputeClaimNotPending = errors.New("workspace_compute_claim_not_pending")
	errWorkspaceComputeClaimProof      = errors.New("workspace_compute_claim_proof_failed")
)

const (
	workspaceLaunchAction        = "workspace.launch.v2"
	workspaceLaunchSchemaVersion = 2
	workspaceLaunchStageMax      = 1
	workspaceImageRepository     = "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app"
)

var workspaceLaunchContinuationStages = []string{"storage", "attachment", "secret", "runtime", "activation", "receipt"}

var workspaceComputeClaimForbiddenWrites = []string{
	"create_launch", "debit", "recharge", "refund", "scale", "create_cvm", "create_second_cbs", "delete", "replace",
}

const workspaceLaunchReadbackRecoveryConfirmation = "RECOVER_UNKNOWN_WORKSPACE_LAUNCH_STAGE_FROM_AUTHORITATIVE_READBACK"

var workspaceLaunchReadbackRecoveryForbiddenWrites = []string{
	"create_launch", "debit", "recharge", "refund", "scale", "create_cvm", "create_second_cbs", "delete", "replace", "retry_unknown_stage_write",
}

func workspaceLaunchReadbackRecoveryAllowedWrites(stage string) []string {
	remaining := map[string][]string{
		"storage":    {"create_original_pv_pvc_attachment", "upsert_original_gateway_secret", "create_original_workspace_runtime", "activate_original_workspace", "record_original_purchase_receipt"},
		"attachment": {"upsert_original_gateway_secret", "create_original_workspace_runtime", "activate_original_workspace", "record_original_purchase_receipt"},
		"secret":     {"create_original_workspace_runtime", "activate_original_workspace", "record_original_purchase_receipt"},
		"runtime":    {"activate_original_workspace", "record_original_purchase_receipt"},
		"activation": {"record_original_purchase_receipt"},
		"receipt":    {},
	}
	writes, ok := remaining[stage]
	if !ok {
		return nil
	}
	return append([]string{"confirm_original_" + stage + "_from_authoritative_readback"}, writes...)
}

func workspaceLaunchReadbackRecoveryStageValid(stage string) bool {
	return workspaceLaunchReadbackRecoveryAllowedWrites(stage) != nil
}

func workspaceLaunchReadbackRecoveryPhase(stage string) string {
	return map[string]string{
		"storage": "storage_fulfilling", "attachment": "attaching", "secret": "secret_writing",
		"runtime": "runtime_starting", "activation": "activating", "receipt": "receipt_pending",
	}[stage]
}

func isWorkspaceLaunchAction(action string) bool {
	return action == workspaceLaunchAction || action == "workspace.launch"
}

type workspaceLaunchOperation struct {
	ID                         string                                   `json:"-"`
	Status                     string                                   `json:"-"`
	CreatedAt                  string                                   `json:"-"`
	PersistedResult            string                                   `json:"-"`
	SchemaVersion              int                                      `json:"schemaVersion"`
	RequestHash                string                                   `json:"requestHash"`
	Phase                      string                                   `json:"phase"`
	AccountID                  string                                   `json:"accountId"`
	OwnerUserID                string                                   `json:"ownerUserId"`
	WorkspaceID                string                                   `json:"workspaceId"`
	Name                       string                                   `json:"name"`
	PackageID                  string                                   `json:"packageId"`
	StorageGB                  int                                      `json:"sizeGb"`
	AutoRenew                  bool                                     `json:"autoRenew"`
	PriceVersion               string                                   `json:"priceVersion"`
	TotalChargeUSDMicros       int64                                    `json:"totalChargeUsdMicros"`
	PeriodStart                string                                   `json:"periodStart,omitempty"`
	PaidThrough                string                                   `json:"paidThrough,omitempty"`
	BillingAnchorDay           int                                      `json:"billingAnchorDay,omitempty"`
	BillingPeriodState         string                                   `json:"billingPeriodState,omitempty"`
	ComputeID                  string                                   `json:"computeAllocationId"`
	ComputePoolID              string                                   `json:"computePoolId,omitempty"`
	ComputeNodePoolID          string                                   `json:"computeNodePoolId"`
	ComputeMachineName         string                                   `json:"computeMachineName,omitempty"`
	ComputeNodeName            string                                   `json:"computeNodeName,omitempty"`
	ComputeCVMInstanceID       string                                   `json:"computeCvmInstanceId,omitempty"`
	ComputeInstanceType        string                                   `json:"computeInstanceType,omitempty"`
	ComputeZone                string                                   `json:"computeZone,omitempty"`
	ComputePrivateIP           string                                   `json:"computePrivateIp,omitempty"`
	ComputeChargeType          string                                   `json:"computeChargeType,omitempty"`
	ComputeRenewFlag           string                                   `json:"computeRenewFlag,omitempty"`
	ComputeDeadline            string                                   `json:"computeDeadline,omitempty"`
	ComputeClaimRequestHash    string                                   `json:"computeClaimRequestHash,omitempty"`
	ComputeClaimApprovalID     string                                   `json:"computeClaimApprovalId,omitempty"`
	ComputeClaimMergedMainSHA  string                                   `json:"computeClaimMergedMainSha,omitempty"`
	ComputeClaimCloudDigest    string                                   `json:"computeClaimCloudImageDigest,omitempty"`
	ComputeClaimApproval       *workspaceComputeClaimApprovalBinding    `json:"computeClaimApproval,omitempty"`
	ReadbackRecoveryApproval   *workspaceLaunchReadbackRecoveryApproval `json:"readbackRecoveryApproval,omitempty"`
	ReadbackRecoveryProof      *workspaceLaunchReadbackRecoveryProof    `json:"readbackRecoveryProof,omitempty"`
	WorkspaceImageDigest       string                                   `json:"workspaceImageDigest,omitempty"`
	ComputeClaimPrivateIP      string                                   `json:"computeClaimPrivateIp,omitempty"`
	ComputeClaimProof          *clients.ComputeClaimRecoveryProof       `json:"computeClaimProof,omitempty"`
	StorageID                  string                                   `json:"storageId"`
	AttachmentID               string                                   `json:"attachmentId,omitempty"`
	AttachmentOperationID      string                                   `json:"attachmentOperationId"`
	WorkspaceOperationID       string                                   `json:"workspaceOperationId"`
	WorkspaceAPIKeyID          int64                                    `json:"workspaceApiKeyId"`
	RedeemCode                 string                                   `json:"sub2apiRedeemCode"`
	RefundCode                 string                                   `json:"sub2apiRefundCode,omitempty"`
	ChargeAttempted            bool                                     `json:"chargeAttempted,omitempty"`
	ChargeConfirmation         map[string]any                           `json:"chargeConfirmation,omitempty"`
	PreChargeBalanceUSDMicros  int64                                    `json:"preChargeBalanceUsdMicros,omitempty"`
	PostChargeBalanceUSDMicros int64                                    `json:"postChargeBalanceUsdMicros,omitempty"`
	PostChargeBalanceKnown     bool                                     `json:"postChargeBalanceKnown,omitempty"`
	RefundAttempted            bool                                     `json:"refundAttempted,omitempty"`
	RefundConfirmation         map[string]any                           `json:"refundConfirmation,omitempty"`
	RefundReason               string                                   `json:"refundReason,omitempty"`
	RefundReceiptID            string                                   `json:"refundReceiptId,omitempty"`
	LeaseToken                 string                                   `json:"leaseToken,omitempty"`
	LeaseExpiresAt             string                                   `json:"leaseExpiresAt,omitempty"`
	GatewaySecretRef           string                                   `json:"gatewaySecretRef,omitempty"`
	WorkspaceKeyStatus         string                                   `json:"workspaceKeyStatus,omitempty"`
	WorkspaceKeyFingerprint    string                                   `json:"workspaceKeyFingerprint,omitempty"`
	RuntimeID                  string                                   `json:"runtimeId,omitempty"`
	RuntimeReady               bool                                     `json:"runtimeReady,omitempty"`
	RuntimeServiceName         string                                   `json:"runtimeServiceName,omitempty"`
	RuntimeUsername            string                                   `json:"runtimeUsername,omitempty"`
	CredentialStatus           string                                   `json:"credentialStatus,omitempty"`
	CredentialVersion          string                                   `json:"credentialVersion,omitempty"`
	CredentialSecretRef        string                                   `json:"credentialSecretRef,omitempty"`
	URL                        string                                   `json:"url,omitempty"`
	ReceiptID                  string                                   `json:"receiptId,omitempty"`
	ContinuationAttemptBudgets map[string]workspaceLaunchStageBudget    `json:"continuationAttemptBudgets"`
	ErrorCode                  string                                   `json:"errorCode,omitempty"`
}

type workspaceLaunchStageBudget struct {
	Attempted int `json:"attempted"`
	Confirmed int `json:"confirmed"`
	Unknown   int `json:"unknown"`
	Max       int `json:"max"`
}

type workspaceLaunchReadbackRecoveryCustomer struct {
	Email       string `json:"email"`
	AccountID   string `json:"accountId"`
	OwnerUserID string `json:"ownerUserId"`
}

type workspaceLaunchReadbackRecoveryTarget struct {
	LaunchOperationID    string `json:"launchOperationId"`
	AccountID            string `json:"accountId"`
	WorkspaceID          string `json:"workspaceId"`
	ComputeAllocationID  string `json:"computeAllocationId"`
	StorageID            string `json:"storageId"`
	PackageID            string `json:"packageId"`
	PoolID               string `json:"poolId"`
	NodePoolID           string `json:"nodePoolId"`
	MachineName          string `json:"machineName"`
	NodeName             string `json:"nodeName"`
	CVMInstanceID        string `json:"cvmInstanceId"`
	PrivateIP            string `json:"privateIp"`
	InstanceType         string `json:"instanceType"`
	Zone                 string `json:"zone"`
	ChargeType           string `json:"chargeType"`
	PeriodMonths         int    `json:"periodMonths"`
	RenewFlag            string `json:"renewFlag"`
	Deadline             string `json:"deadline"`
	StorageGB            int    `json:"storageGb"`
	AutoRenew            bool   `json:"autoRenew"`
	PriceVersion         string `json:"priceVersion"`
	TotalChargeUSDMicros int64  `json:"totalChargeUsdMicros"`
	PeriodStart          string `json:"periodStart"`
	PaidThrough          string `json:"paidThrough"`
	BillingAnchorDay     int    `json:"billingAnchorDay"`
}

type workspaceLaunchReadbackRecoveryResources struct {
	ComputeAllocationID       string `json:"computeAllocationId"`
	ComputeProviderResourceID string `json:"computeProviderResourceId"`
	StorageVolumeID           string `json:"storageVolumeId"`
	StorageProviderResourceID string `json:"storageProviderResourceId"`
	StorageZone               string `json:"storageZone"`
	StorageSizeGB             int    `json:"storageSizeGb"`
	StorageChargeType         string `json:"storageChargeType"`
	StorageRenewFlag          string `json:"storageRenewFlag"`
	StorageDeadline           string `json:"storageDeadline"`
	AttachmentID              string `json:"attachmentId"`
	AttachmentProviderID      string `json:"attachmentProviderId"`
	GatewaySecretRef          string `json:"gatewaySecretRef"`
	GatewaySecretFingerprint  string `json:"gatewaySecretFingerprint"`
	WorkspaceAPIKeyID         int64  `json:"workspaceApiKeyId"`
	RuntimeID                 string `json:"runtimeId"`
	RuntimeServiceName        string `json:"runtimeServiceName"`
	ReceiptID                 string `json:"receiptId"`
}

type workspaceLaunchReadbackRecoveryFabricOperationIdentity struct {
	IdempotencyKey        string `json:"idempotencyKey"`
	FabricRecordID        string `json:"fabricRecordId"`
	FabricOperationID     string `json:"fabricOperationId"`
	RequestHash           string `json:"requestHash"`
	ResourceOperationID   string `json:"resourceOperationId"`
	ProviderOperationID   string `json:"providerOperationId"`
	ReadbackBindingDigest string `json:"readbackBindingDigest"`
}

type workspaceLaunchReadbackRecoveryOperationIDs struct {
	LaunchOperationID     string                                                 `json:"launchOperationId"`
	LaunchRequestHash     string                                                 `json:"launchRequestHash"`
	MachineOwnershipID    string                                                 `json:"machineOwnershipId"`
	Compute               workspaceLaunchReadbackRecoveryFabricOperationIdentity `json:"compute"`
	Storage               workspaceLaunchReadbackRecoveryFabricOperationIdentity `json:"storage"`
	Attachment            workspaceLaunchReadbackRecoveryFabricOperationIdentity `json:"attachment"`
	Secret                workspaceLaunchReadbackRecoveryFabricOperationIdentity `json:"secret"`
	Runtime               workspaceLaunchReadbackRecoveryFabricOperationIdentity `json:"runtime"`
	ActivationOperationID string                                                 `json:"activationOperationId"`
	ReceiptOperationID    string                                                 `json:"receiptOperationId"`
}

type workspaceLaunchReadbackRecoveryApproval struct {
	SchemaVersion        int                                         `json:"schemaVersion"`
	ApprovalID           string                                      `json:"approvalId"`
	ApprovalDigest       string                                      `json:"approvalDigest"`
	ExpiresAt            string                                      `json:"expiresAt"`
	MergedMainSHA        string                                      `json:"mergedMainSha"`
	CloudImageDigest     string                                      `json:"cloudImageDigest"`
	WorkspaceImageDigest string                                      `json:"workspaceImageDigest"`
	Confirmation         string                                      `json:"confirmation"`
	IdempotencyKey       string                                      `json:"idempotencyKey"`
	RecoveryKey          string                                      `json:"recoveryKey"`
	Stage                string                                      `json:"stage"`
	Customer             workspaceLaunchReadbackRecoveryCustomer     `json:"customer"`
	Target               workspaceLaunchReadbackRecoveryTarget       `json:"target"`
	Resources            workspaceLaunchReadbackRecoveryResources    `json:"resources"`
	OperationIDs         workspaceLaunchReadbackRecoveryOperationIDs `json:"operationIds"`
	AttemptBudget        workspaceLaunchStageBudget                  `json:"attemptBudget"`
	AllowedWrites        []string                                    `json:"allowedWrites"`
	ForbiddenWrites      []string                                    `json:"forbiddenWrites"`
}

type workspaceLaunchReadbackRecoveryProof struct {
	SchemaVersion           int                                         `json:"schemaVersion"`
	Eligible                bool                                        `json:"eligible"`
	Reason                  string                                      `json:"reason"`
	Stage                   string                                      `json:"stage"`
	Customer                workspaceLaunchReadbackRecoveryCustomer     `json:"customer"`
	Target                  workspaceLaunchReadbackRecoveryTarget       `json:"target"`
	Resources               workspaceLaunchReadbackRecoveryResources    `json:"resources"`
	OperationIDs            workspaceLaunchReadbackRecoveryOperationIDs `json:"operationIds"`
	WorkspaceImageDigest    string                                      `json:"workspaceImageDigest"`
	AttemptBudget           workspaceLaunchStageBudget                  `json:"attemptBudget"`
	AllowedWrites           []string                                    `json:"allowedWrites"`
	ForbiddenWrites         []string                                    `json:"forbiddenWrites"`
	Sub2APIMutationCount    int                                         `json:"sub2apiMutationCount"`
	TencentMutationCount    int                                         `json:"tencentMutationCount"`
	KubernetesMutationCount int                                         `json:"kubernetesMutationCount"`
}

type workspaceComputeClaimApprovalCustomer struct {
	Email     string `json:"email"`
	AccountID string `json:"accountId"`
}

type workspaceComputeClaimApprovalTarget struct {
	LaunchOperationID   string `json:"launchOperationId"`
	AccountID           string `json:"accountId"`
	WorkspaceID         string `json:"workspaceId"`
	ComputeAllocationID string `json:"computeAllocationId"`
	StorageID           string `json:"storageId"`
	PackageID           string `json:"packageId"`
	PoolID              string `json:"poolId"`
	NodePoolID          string `json:"nodePoolId"`
	MachineName         string `json:"machineName"`
	NodeName            string `json:"nodeName"`
	CVMInstanceID       string `json:"cvmInstanceId"`
	PrivateIP           string `json:"privateIp"`
	InstanceType        string `json:"instanceType"`
	Zone                string `json:"zone"`
	ChargeType          string `json:"chargeType"`
	PeriodMonths        int    `json:"periodMonths"`
	RenewFlag           string `json:"renewFlag"`
	Deadline            string `json:"deadline"`
}

type workspaceComputeClaimApprovalResources struct {
	ComputeOperationID        string `json:"computeOperationId"`
	StorageOperationID        string `json:"storageOperationId"`
	StorageState              string `json:"storageState"`
	StorageProviderResourceID string `json:"storageProviderResourceId"`
	AttachmentID              string `json:"attachmentId"`
	AttachmentOperationID     string `json:"attachmentOperationId"`
	WorkspaceAPIKeyID         string `json:"workspaceApiKeyId"`
	GatewaySecretRef          string `json:"gatewaySecretRef"`
	SecretOperationID         string `json:"secretOperationId"`
	RuntimeID                 string `json:"runtimeId"`
	RuntimeOperationID        string `json:"runtimeOperationId"`
	ReceiptOperationID        string `json:"receiptOperationId"`
}

type workspaceComputeClaimProviderAttemptLimits struct {
	Sub2API    int `json:"sub2api"`
	Tencent    int `json:"tencent"`
	Kubernetes int `json:"kubernetes"`
}

type workspaceComputeClaimAttemptLimits struct {
	Claim      workspaceComputeClaimProviderAttemptLimits `json:"claim"`
	Storage    int                                        `json:"storage"`
	Attachment int                                        `json:"attachment"`
	Secret     int                                        `json:"secret"`
	Runtime    int                                        `json:"runtime"`
	Activation int                                        `json:"activation"`
	Receipt    int                                        `json:"receipt"`
}

type workspaceComputeClaimApprovalBinding struct {
	SchemaVersion        int                                    `json:"schemaVersion"`
	ApprovalID           string                                 `json:"approvalId"`
	ApprovalDigest       string                                 `json:"approvalDigest"`
	ExpiresAt            string                                 `json:"expiresAt"`
	MergedMainSHA        string                                 `json:"mergedMainSha"`
	CloudImageDigest     string                                 `json:"cloudImageDigest"`
	WorkspaceImageDigest string                                 `json:"workspaceImageDigest"`
	Confirmation         string                                 `json:"confirmation"`
	IdempotencyKey       string                                 `json:"idempotencyKey"`
	RecoveryKey          string                                 `json:"recoveryKey"`
	Customer             workspaceComputeClaimApprovalCustomer  `json:"customer"`
	Target               workspaceComputeClaimApprovalTarget    `json:"target"`
	Resources            workspaceComputeClaimApprovalResources `json:"resources"`
	AttemptLimits        workspaceComputeClaimAttemptLimits     `json:"attemptLimits"`
	AllowedWrites        []string                               `json:"allowedWrites"`
	ForbiddenWrites      []string                               `json:"forbiddenWrites"`
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

type workspaceComputeClaimRecoveryRequest struct {
	LaunchOperationID    string
	AccountID            string
	WorkspaceID          string
	ComputeID            string
	StorageID            string
	PackageID            string
	PoolID               string
	NodePoolID           string
	MachineName          string
	NodeName             string
	CVMInstanceID        string
	PrivateIP            string
	InstanceType         string
	Zone                 string
	MergedMainSHA        string
	CloudImageDigest     string
	ApprovalID           string
	ApprovalDigest       string
	ExpiresAt            string
	Confirmation         string
	WorkspaceImageDigest string
	CustomerEmail        string
	RecoveryKey          string
	Resources            workspaceComputeClaimApprovalResources
	AttemptLimits        workspaceComputeClaimAttemptLimits
	AllowedWrites        []string
	ForbiddenWrites      []string
}

func encodeWorkspaceLaunchOperation(operation workspaceLaunchOperation) string {
	payload, _ := json.Marshal(operation)
	return string(payload)
}

func newWorkspaceLaunchOperation(accountID, ownerUserID, name, packageID string, storageGB int, autoRenew bool, priceVersion string, totalChargeUSDMicros int64, key string) workspaceLaunchOperation {
	operationID := "workspace-launch-" + stableID(accountID, key)[:18]
	workspaceID := "ws-" + stableID("workspace-launch-v2", accountID, operationID)[:18]
	workspaceImageDigest := currentWorkspaceImageDigest()
	now := time.Now().UTC()
	return workspaceLaunchOperation{
		ID: operationID, Status: "debit_pending", CreatedAt: now.Format(time.RFC3339Nano), Phase: "debit_pending", SchemaVersion: workspaceLaunchSchemaVersion,
		RequestHash: stableID("workspace-launch-v2", accountID, ownerUserID, name, packageID, strconv.Itoa(storageGB), strconv.FormatBool(autoRenew), priceVersion, workspaceImageDigest),
		AccountID:   accountID, OwnerUserID: ownerUserID, WorkspaceID: workspaceID, Name: name, PackageID: packageID,
		StorageGB: storageGB, AutoRenew: autoRenew, PriceVersion: priceVersion, TotalChargeUSDMicros: totalChargeUSDMicros,
		WorkspaceImageDigest:  workspaceImageDigest,
		BillingPeriodState:    "pending",
		ComputeID:             resourceIDForMutation("ca", accountID, operationID+":compute"),
		StorageID:             resourceIDForMutation("vol", accountID, operationID+":storage"),
		AttachmentOperationID: operationID + ":attachment", WorkspaceOperationID: operationID + ":workspace",
		RedeemCode: monthlyRedeemCode(monthlyEnvironment(), operationID), RefundCode: monthlyRefundCode(monthlyEnvironment(), operationID),
		ContinuationAttemptBudgets: newWorkspaceLaunchContinuationAttemptBudgets(),
	}
}

func newWorkspaceLaunchContinuationAttemptBudgets() map[string]workspaceLaunchStageBudget {
	budgets := make(map[string]workspaceLaunchStageBudget, len(workspaceLaunchContinuationStages))
	for _, stage := range workspaceLaunchContinuationStages {
		budgets[stage] = workspaceLaunchStageBudget{Max: workspaceLaunchStageMax}
	}
	return budgets
}

func normalizeWorkspaceLaunchContinuationAttemptBudgets(operation *workspaceLaunchOperation) bool {
	if operation.ContinuationAttemptBudgets == nil {
		operation.ContinuationAttemptBudgets = newWorkspaceLaunchContinuationAttemptBudgets()
		for _, stage := range completedWorkspaceLaunchStages(operation.Phase) {
			operation.ContinuationAttemptBudgets[stage] = workspaceLaunchStageBudget{Attempted: 1, Confirmed: 1, Max: workspaceLaunchStageMax}
		}
		return true
	}
	if len(operation.ContinuationAttemptBudgets) != len(workspaceLaunchContinuationStages) {
		return false
	}
	allowed := make(map[string]bool, len(workspaceLaunchContinuationStages))
	for _, stage := range workspaceLaunchContinuationStages {
		allowed[stage] = true
	}
	for stage, budget := range operation.ContinuationAttemptBudgets {
		if !allowed[stage] || budget.Max != workspaceLaunchStageMax || budget.Attempted < 0 || budget.Confirmed < 0 || budget.Unknown < 0 ||
			budget.Attempted > budget.Max || budget.Confirmed > budget.Attempted || budget.Unknown > budget.Attempted || budget.Confirmed+budget.Unknown > budget.Attempted {
			return false
		}
	}
	return true
}

func completedWorkspaceLaunchStages(phase string) []string {
	switch phase {
	case "attaching":
		return []string{"storage"}
	case "secret_writing":
		return []string{"storage", "attachment"}
	case "runtime_starting":
		return []string{"storage", "attachment", "secret"}
	case "activating":
		return []string{"storage", "attachment", "secret", "runtime"}
	case "receipt_pending":
		return []string{"storage", "attachment", "secret", "runtime", "activation"}
	case "succeeded":
		return append([]string(nil), workspaceLaunchContinuationStages...)
	default:
		return nil
	}
}

func currentWorkspaceImageDigest() string {
	value := strings.TrimSpace(os.Getenv("OPL_WORKSPACE_IMAGE"))
	digest, ok := strings.CutPrefix(value, workspaceImageRepository+"@")
	if ok && computeClaimCloudDigestPattern.MatchString(digest) {
		return value
	}
	return ""
}

func validWorkspaceImageIdentity(value string) bool {
	if computeClaimCloudDigestPattern.MatchString(value) {
		return true
	}
	digest, ok := strings.CutPrefix(value, workspaceImageRepository+"@")
	return ok && computeClaimCloudDigestPattern.MatchString(digest)
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
	if operation.BillingPeriodState == "" {
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
		operation.BillingPeriodState = "frozen"
	} else if operation.BillingPeriodState == "pending" && (operation.PeriodStart != "" || operation.PaidThrough != "" || operation.BillingAnchorDay != 0) {
		return workspaceLaunchOperation{}, errInvalidWorkspaceLaunchOperation
	}
	if !normalizeWorkspaceLaunchContinuationAttemptBudgets(&operation) {
		return workspaceLaunchOperation{}, errInvalidWorkspaceLaunchOperation
	}
	keyPending := operation.Phase == "key_pending" && operation.WorkspaceAPIKeyID == 0
	computeClaimPending := operation.Phase == "compute_claim_pending"
	if operation.SchemaVersion != workspaceLaunchSchemaVersion || operation.ID == "" || operation.Status == "" || operation.RequestHash == "" || operation.AccountID == "" || operation.OwnerUserID == "" ||
		operation.WorkspaceID == "" || operation.PriceVersion == "" || operation.PackageID == "" || operation.StorageGB <= 0 || operation.TotalChargeUSDMicros <= 0 ||
		operation.WorkspaceAPIKeyID < 0 || operation.WorkspaceAPIKeyID == 0 && !keyPending || operation.RedeemCode == "" || computeClaimPending && !validWorkspaceLaunchComputeClaimIdentity(operation) {
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

func validWorkspaceLaunchComputeClaimIdentity(operation workspaceLaunchOperation) bool {
	deadline, err := time.Parse(time.RFC3339, operation.ComputeDeadline)
	return operation.ComputeID != "" && operation.StorageID != "" && operation.ComputePoolID != "" && operation.ComputeNodePoolID != "" &&
		operation.ComputeMachineName != "" && operation.ComputeNodeName != "" && operation.ComputeCVMInstanceID != "" && operation.ComputePrivateIP != "" &&
		operation.ComputeInstanceType != "" && operation.ComputeZone != "" && operation.ComputeChargeType == "PREPAID" &&
		operation.ComputeRenewFlag == "NOTIFY_AND_MANUAL_RENEW" && err == nil && !deadline.IsZero()
}

func validWorkspaceLaunchLegacyComputeClaimIdentity(operation workspaceLaunchOperation) bool {
	if validWorkspaceLaunchComputeClaimIdentity(operation) {
		return true
	}
	return operation.ComputeID != "" && operation.StorageID != "" && operation.ComputeNodePoolID != "" &&
		operation.ComputePoolID == "" && operation.ComputeMachineName == "" && operation.ComputeNodeName == "" &&
		operation.ComputeCVMInstanceID == "" && operation.ComputePrivateIP == "" && operation.ComputeInstanceType == "" &&
		operation.ComputeZone == "" && operation.ComputeChargeType == "" && operation.ComputeRenewFlag == "" && operation.ComputeDeadline == "" &&
		operation.ComputeClaimRequestHash == "" && operation.ComputeClaimApprovalID == "" && operation.ComputeClaimMergedMainSHA == "" &&
		operation.ComputeClaimCloudDigest == "" && operation.ComputeClaimPrivateIP == "" && operation.ComputeClaimProof == nil
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
	response := map[string]any{
		"operationId": operation.ID, "status": operation.Status, "phase": operation.Phase,
		"accountId": operation.AccountID, "workspaceId": operation.WorkspaceID, "name": operation.Name,
		"packageId": operation.PackageID, "sizeGb": operation.StorageGB, "autoRenew": operation.AutoRenew, "priceVersion": operation.PriceVersion,
		"currency": pricingCurrency, "totalChargeUsdMicros": operation.TotalChargeUSDMicros,
		"computeAllocationId": operation.ComputeID, "storageId": operation.StorageID, "attachmentId": operation.AttachmentID,
		"workspaceApiKeyId":  strconv.FormatInt(operation.WorkspaceAPIKeyID, 10),
		"workspaceKeyStatus": operation.WorkspaceKeyStatus, "workspaceKeyFingerprint": operation.WorkspaceKeyFingerprint,
		"runtimeServiceName": operation.RuntimeServiceName, "url": operation.URL, "receiptId": operation.ReceiptID,
		"continuationAttemptBudgets": operation.ContinuationAttemptBudgets,
		"errorCode":                  operation.ErrorCode, "createdAt": row["createdAt"], "updatedAt": row["updatedAt"],
	}
	if approval := operation.ReadbackRecoveryApproval; approval != nil {
		response["recovery"] = map[string]any{
			"approvalId": approval.ApprovalID, "approvalDigest": approval.ApprovalDigest,
			"recoveryKey": approval.RecoveryKey, "workspaceImageDigest": approval.WorkspaceImageDigest,
		}
	} else if approval := operation.ComputeClaimApproval; approval != nil {
		response["recovery"] = map[string]any{
			"approvalId": approval.ApprovalID, "approvalDigest": approval.ApprovalDigest,
			"recoveryKey": approval.RecoveryKey, "workspaceImageDigest": approval.WorkspaceImageDigest,
		}
	}
	return response, nil
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
	if operation.Phase == "key_pending" || operation.Phase == "compute_claim_pending" {
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
		if currentWorkspaceImageDigest() != operation.WorkspaceImageDigest || !validWorkspaceImageIdentity(operation.WorkspaceImageDigest) {
			return app.retryWorkspaceLaunchDebit(ctx, &operation, "workspace_image_digest_drift", errors.New("workspace_image_digest_drift"))
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
					storage, err := service.ReadMonthlyStorage(ctx, operation.StorageID)
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
			case "compute_claim_pending":
				operation.Status, operation.Phase, operation.ErrorCode = "compute_claim_pending", "compute_claim_pending", ""
				releaseWorkspaceLaunchLease(operation)
				return app.persistWorkspaceLaunch(ctx, operation)
			default:
				return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "fabric_"+resourceType+"_fulfillment_unconfirmed")
			}
		case "attaching":
			attachment, found := app.workspaceLaunchAttachment(*operation)
			budget := operation.ContinuationAttemptBudgets["attachment"]
			if budget.Confirmed == 0 {
				var created clients.StorageAttachment
				var err error
				if budget.Attempted == 0 {
					if err := app.reserveWorkspaceLaunchStageAttempt(ctx, operation, "attachment"); err != nil {
						return err
					}
					if !found {
						created, err = service.CreateStorageAttachment(ctx, controlplane.StorageAttachmentInput{
							WorkspaceID: operation.WorkspaceID, ComputeID: operation.ComputeID, VolumeID: operation.StorageID,
						}, operation.AttachmentOperationID)
					}
				} else if !found {
					created, err = app.workspaceLaunchAttachmentFromFabricOperation(ctx, service, *operation)
				}
				if err != nil {
					return app.unknownWorkspaceLaunchStageAttempt(ctx, operation, "attachment", err)
				}
				if !found {
					if created.OperationID == "" {
						created.OperationID = operation.AttachmentOperationID
					}
					if err := app.saveWorkspaceLaunchAttachment(created, *operation); err != nil {
						return app.unknownWorkspaceLaunchStageAttempt(ctx, operation, "attachment", err)
					}
					operation.AttachmentID = created.ID
				} else {
					operation.AttachmentID = stringValue(attachment["id"])
				}
				if err := app.confirmWorkspaceLaunchStageAttempt(ctx, operation, "attachment"); err != nil {
					return err
				}
			} else if found {
				operation.AttachmentID = stringValue(attachment["id"])
			} else {
				created, err := app.workspaceLaunchAttachmentFromFabricOperation(ctx, service, *operation)
				if err != nil || app.saveWorkspaceLaunchAttachment(created, *operation) != nil {
					return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_attachment_readback_invalid")
				}
				operation.AttachmentID = created.ID
			}
			operation.Status, operation.Phase, operation.ErrorCode = "preparing", "secret_writing", ""
			if err := app.persistWorkspaceLaunch(ctx, operation); err != nil {
				return err
			}
		case "secret_writing":
			budget := operation.ContinuationAttemptBudgets["secret"]
			if budget.Confirmed == 0 {
				var secret clients.GatewaySecretWriteResult
				var err error
				if budget.Attempted == 0 {
					userID, mappingErr := app.sub2APIUserID(ctx, operation.AccountID)
					if mappingErr != nil {
						return app.retryWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_account_mapping_unavailable", mappingErr)
					}
					if err := app.reserveWorkspaceLaunchStageAttempt(ctx, operation, "secret"); err != nil {
						return err
					}
					secret, err = service.SyncWorkspaceGatewaySecretByID(ctx, operation.AccountID, operation.WorkspaceID, userID, operation.WorkspaceAPIKeyID, workspaceReservedKeyName(operation.WorkspaceID), operation.WorkspaceOperationID+":secret")
				} else {
					secret, err = app.workspaceLaunchSecretFromFabricOperation(ctx, service, *operation)
				}
				if err != nil {
					if reconciled, readErr := app.workspaceLaunchSecretFromFabricOperation(ctx, service, *operation); readErr == nil {
						secret, err = reconciled, nil
					}
				}
				if err != nil || secret.SecretRef == "" || secret.Version == "" || secret.Fingerprint == "" {
					return app.unknownWorkspaceLaunchStageAttempt(ctx, operation, "secret", err)
				}
				operation.GatewaySecretRef, operation.WorkspaceKeyStatus, operation.WorkspaceKeyFingerprint = secret.SecretRef, "configured", secret.Fingerprint
				if err := app.confirmWorkspaceLaunchStageAttempt(ctx, operation, "secret"); err != nil {
					return err
				}
			}
			operation.Status, operation.Phase, operation.ErrorCode = "preparing", "runtime_starting", ""
			if err := app.persistWorkspaceLaunch(ctx, operation); err != nil {
				return err
			}
		case "runtime_starting":
			input := controlplane.CreateWorkspaceInput{
				WorkspaceID: operation.WorkspaceID, AccountID: operation.AccountID, WorkspaceAPIKeyID: operation.WorkspaceAPIKeyID, WorkspaceAPIKeyName: workspaceReservedKeyName(operation.WorkspaceID),
				OwnerID: operation.OwnerUserID, Name: operation.Name, PackageID: operation.PackageID, AttachmentID: operation.AttachmentID,
				AttachmentOperationID: operation.AttachmentOperationID, RuntimeOperationID: operation.WorkspaceOperationID + ":runtime",
				ComputeID: operation.ComputeID, VolumeID: operation.StorageID, GatewaySecretRef: operation.GatewaySecretRef,
				WorkspaceImageID: operation.WorkspaceImageDigest,
			}
			budget := operation.ContinuationAttemptBudgets["runtime"]
			var workspace domain.WorkspaceProjection
			var err error
			if budget.Confirmed == 0 {
				if budget.Attempted == 0 {
					userID, mappingErr := app.sub2APIUserID(ctx, operation.AccountID)
					if mappingErr != nil {
						return app.retryWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_account_mapping_unavailable", mappingErr)
					}
					input.Sub2APIUserID = userID
					if err := app.reserveWorkspaceLaunchStageAttempt(ctx, operation, "runtime"); err != nil {
						return err
					}
					workspace, err = service.PrepareWorkspace(ctx, input, operation.WorkspaceOperationID)
				} else {
					workspace, err = app.readWorkspaceLaunchRuntime(ctx, service, *operation)
				}
				if err != nil {
					if reconciled, readErr := app.readWorkspaceLaunchRuntime(ctx, service, *operation); readErr == nil {
						workspace, err = reconciled, nil
					}
				}
				if err != nil {
					return app.unknownWorkspaceLaunchStageAttempt(ctx, operation, "runtime", err)
				}
				if err := app.confirmWorkspaceLaunchStageAttempt(ctx, operation, "runtime"); err != nil {
					return err
				}
			} else {
				workspace, err = app.readWorkspaceLaunchRuntime(ctx, service, *operation)
				if err != nil {
					return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_runtime_readback_invalid")
				}
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
			if err := app.verifyWorkspaceLaunchActivationTruth(ctx, service, operation); err != nil {
				return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, err.Error())
			}
			billingState, reviewCode := app.workspaceLaunchBillingState(ctx, *operation)
			if reviewCode != "" {
				return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, reviewCode)
			}
			budget := operation.ContinuationAttemptBudgets["activation"]
			if existing, ok := app.getWorkspace(operation.WorkspaceID); ok {
				if !workspaceMatchesLaunch(existing, *operation) || !workspaceBillingStateMatchesLaunch(existing, billingState) || stringValue(existing["runtimeId"]) != operation.RuntimeID {
					return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_activation_identity_mismatch")
				}
				if budget.Confirmed == 0 {
					if budget.Attempted == 0 {
						if err := app.observeWorkspaceLaunchStageAttempt(ctx, operation, "activation"); err != nil {
							return err
						}
					} else if err := app.confirmWorkspaceLaunchStageAttempt(ctx, operation, "activation"); err != nil {
						return err
					}
				}
			} else {
				if budget.Confirmed > 0 || budget.Attempted > 0 {
					return app.unknownWorkspaceLaunchStageAttempt(ctx, operation, "activation", nil)
				}
				workspaceRow := workspaceProjectionRow(workspaceProjectionFromLaunch(*operation))
				for key, value := range billingState {
					workspaceRow[key] = value
				}
				if err := app.reserveWorkspaceLaunchStageAttempt(ctx, operation, "activation"); err != nil {
					return err
				}
				if _, err := app.tables.ActivateWorkspace(ctx, workspaceRow); errors.Is(err, errWorkspaceActivationConflict) {
					return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_activation_conflict")
				} else if err != nil {
					if existing, ok := app.getWorkspace(operation.WorkspaceID); !ok || !workspaceMatchesLaunch(existing, *operation) ||
						!workspaceBillingStateMatchesLaunch(existing, billingState) || stringValue(existing["runtimeId"]) != operation.RuntimeID {
						return app.unknownWorkspaceLaunchStageAttempt(ctx, operation, "activation", err)
					}
				}
				if err := app.confirmWorkspaceLaunchStageAttempt(ctx, operation, "activation"); err != nil {
					return err
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
		case "compute_claim_pending":
			return nil
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
	preparedThisRun := false
	if resourceType == "storage" {
		budget := operation.ContinuationAttemptBudgets[resourceType]
		if budget.Confirmed == 0 {
			preparedThisRun = true
			if budget.Attempted == 0 {
				if err := app.reserveWorkspaceLaunchStageAttempt(ctx, operation, resourceType); err != nil {
					return "", err
				}
				storageInput := clients.StorageVolumeInput{
					ID: operation.StorageID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, ComputeID: operation.ComputeID,
					Zone: stringValue(row["zone"]), SizeGB: operation.StorageGB,
				}
				if operation.ComputeClaimApproval != nil {
					storageInput.ExpectedRecoveryState = operation.ComputeClaimApproval.Resources.StorageState
					storageInput.ExpectedProviderResourceID = operation.ComputeClaimApproval.Resources.StorageProviderResourceID
				}
				prepared, prepareErr = service.PrepareMonthlyStorage(ctx, storageInput, operation.ID+":storage")
			} else {
				prepared, prepareErr = service.ReadMonthlyStorage(ctx, operation.StorageID)
			}
			if prepareErr != nil {
				if reconciled, readErr := service.ReadMonthlyStorage(ctx, operation.StorageID); readErr == nil {
					prepared, prepareErr = reconciled, nil
				}
			}
			preparedFacts := structToMap(prepared)
			if prepareErr != nil || !workspaceLaunchResourceIdentityMatches(resourceType, preparedFacts, *operation) || stringValue(preparedFacts["providerResourceId"]) == "" {
				return "", app.unknownWorkspaceLaunchStageAttempt(ctx, operation, resourceType, prepareErr)
			}
			if err := app.confirmWorkspaceLaunchStageAttempt(ctx, operation, resourceType); err != nil {
				return "", err
			}
		}
	} else {
		preparedThisRun = true
		prepared, prepareErr = service.PrepareMonthlyCompute(ctx, clients.ComputeAllocationInput{
			ID: operation.ComputeID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, PackageID: operation.PackageID, NodePoolID: operation.ComputeNodePoolID,
		}, operation.ID+":compute")
	}
	preparedFacts := structToMap(prepared)
	if preparedThisRun && prepareErr == nil && !workspaceLaunchResourceIdentityMatches(resourceType, preparedFacts, *operation) {
		return "unknown", nil
	}
	if resourceType == "compute" && prepareErr == nil {
		if allocation, ok := prepared.(clients.ComputeAllocation); ok && allocation.Status == "compute_claim_pending" {
			return app.persistWorkspaceLaunchComputeClaimIdentity(ctx, operation, allocation)
		}
	}

	var readback any
	var readErr error
	if resourceType == "storage" {
		readback, readErr = service.ReadMonthlyStorage(ctx, operation.StorageID)
	} else {
		readback, readErr = service.SyncMonthlyCompute(ctx, operation.ComputeID)
	}
	facts := structToMap(readback)
	if !workspaceLaunchResourceIdentityMatches(resourceType, facts, *operation) {
		return "unknown", nil
	}
	if resourceType == "compute" && readErr == nil {
		if allocation, ok := readback.(clients.ComputeAllocation); ok && allocation.Status == "compute_claim_pending" {
			return app.persistWorkspaceLaunchComputeClaimIdentity(ctx, operation, allocation)
		}
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

func (app *controlPlaneServer) persistWorkspaceLaunchComputeClaimIdentity(ctx context.Context, operation *workspaceLaunchOperation, allocation clients.ComputeAllocation) (string, error) {
	cvmID := firstNonEmpty(allocation.CVMInstanceID, allocation.InstanceID)
	if allocation.ID != operation.ComputeID || allocation.AccountID != operation.AccountID || allocation.WorkspaceID != operation.WorkspaceID ||
		allocation.PackageID != operation.PackageID || allocation.PoolID == "" || allocation.NodePoolID != operation.ComputeNodePoolID ||
		allocation.MachineName == "" || allocation.NodeName == "" || cvmID == "" || allocation.InstanceType == "" || allocation.Zone == "" {
		return "unknown", nil
	}
	row := mergeMaps(workspaceLaunchResourceRow(*operation, "compute"), structToMap(allocation))
	stripWorkspaceLaunchResourceBilling(row)
	if err := app.tables.SaveCompute(ctx, row); err != nil {
		return "", err
	}
	operation.ComputePoolID = allocation.PoolID
	operation.ComputeMachineName = allocation.MachineName
	operation.ComputeNodeName = allocation.NodeName
	operation.ComputeCVMInstanceID = cvmID
	operation.ComputeInstanceType = allocation.InstanceType
	operation.ComputeZone = allocation.Zone
	operation.ComputePrivateIP = allocation.PrivateIP
	operation.ComputeChargeType = allocation.ChargeType
	operation.ComputeRenewFlag = allocation.RenewFlag
	operation.ComputeDeadline = allocation.Deadline
	return "compute_claim_pending", nil
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
	if currentWorkspaceImageDigest() != operation.WorkspaceImageDigest || !validWorkspaceImageIdentity(operation.WorkspaceImageDigest) {
		return "workspace_image_digest_drift", errors.New("workspace_image_digest_drift")
	}
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

func (app *controlPlaneServer) verifyWorkspaceLaunchActivationTruth(ctx context.Context, service *controlplane.Service, operation *workspaceLaunchOperation) error {
	input := workspaceActivationTruthInputFromLaunch(*operation)
	if !validWorkspaceImageIdentity(input.WorkspaceImageDigest) {
		return errors.New("workspace_launch_activation_truth_identity_mismatch")
	}
	truth, err := service.WorkspaceActivationTruth(ctx, input)
	if err != nil || !workspaceActivationTruthMatchesLaunch(truth, input) {
		return errors.New(workspaceLaunchActivationTruthErrorCode(truth))
	}
	return nil
}

func workspaceActivationTruthInputFromLaunch(operation workspaceLaunchOperation) clients.WorkspaceActivationTruthInput {
	return clients.WorkspaceActivationTruthInput{
		LaunchOperationID: operation.ID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
		ComputeAllocationID: operation.ComputeID, ComputeOperationID: operation.ID + ":compute",
		StorageVolumeID: operation.StorageID, StorageOperationID: operation.ID + ":storage",
		AttachmentID: operation.AttachmentID, AttachmentOperationID: operation.AttachmentOperationID,
		RuntimeID: operation.RuntimeID, RuntimeOperationID: operation.WorkspaceOperationID + ":runtime",
		ServiceName: operation.RuntimeServiceName, WorkspaceImageDigest: operation.WorkspaceImageDigest,
		GatewaySecretRef: operation.GatewaySecretRef, WorkspaceAPIKeyID: operation.WorkspaceAPIKeyID,
		GatewaySecretFingerprint: operation.WorkspaceKeyFingerprint,
	}
}

func workspaceActivationTruthMatchesLaunch(truth clients.WorkspaceActivationTruth, input clients.WorkspaceActivationTruthInput) bool {
	return truth.SchemaVersion == 1 && truth.Ready && truth.Reason == "none" && truth.ErrorClass == "" &&
		truth.ComputeState == "ready" && truth.StorageState == "ready" && truth.Checks != nil &&
		truth.Sub2APIMutationCount == 0 && truth.TencentMutationCount == 0 && truth.KubernetesMutationCount == 0 &&
		truth.Compute.ID == input.ComputeAllocationID && truth.Compute.OperationID == input.ComputeOperationID &&
		truth.Storage.ID == input.StorageVolumeID && truth.Storage.OperationID == input.StorageOperationID &&
		truth.Attachment.ID == input.AttachmentID && truth.Attachment.OperationID == input.AttachmentOperationID &&
		truth.Runtime.ID == input.RuntimeID && truth.Runtime.OperationID == input.RuntimeOperationID && truth.Runtime.ServiceName == input.ServiceName
}

func workspaceLaunchActivationTruthErrorCode(truth clients.WorkspaceActivationTruth) string {
	switch truth.Reason {
	case "identity_mismatch", "multiple_candidate", "absent", "provider_unavailable":
		return "workspace_launch_activation_truth_" + truth.Reason
	default:
		return "workspace_launch_activation_truth_unavailable"
	}
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
	row, found, err := app.tables.GetRuntimeOperation(ctx, operationID)
	if err != nil {
		return workspaceLaunchOperation{}, false, err
	}
	if !found || stringValue(row["action"]) != workspaceLaunchAction {
		return workspaceLaunchOperation{}, false, nil
	}
	operation, err := decodeWorkspaceLaunchOperation(row)
	return operation, err == nil, err
}

func workspaceComputeClaimRecoveryInput(operation workspaceLaunchOperation, input workspaceComputeClaimRecoveryRequest) clients.ComputeClaimRecoveryInput {
	return clients.ComputeClaimRecoveryInput{
		LaunchOperationID: operation.ID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
		ComputeAllocationID: operation.ComputeID, StorageVolumeID: operation.StorageID, PackageID: operation.PackageID,
		PoolID: firstNonEmpty(operation.ComputePoolID, input.PoolID), NodePoolID: operation.ComputeNodePoolID,
	}
}

func workspaceComputeClaimRecoveryRequestMatches(operation workspaceLaunchOperation, input workspaceComputeClaimRecoveryRequest) bool {
	if input.LaunchOperationID != operation.ID || input.AccountID != operation.AccountID || input.WorkspaceID != operation.WorkspaceID ||
		input.ComputeID != operation.ComputeID || input.StorageID != operation.StorageID || input.PackageID != operation.PackageID ||
		input.NodePoolID != operation.ComputeNodePoolID {
		return false
	}
	if !validWorkspaceLaunchComputeClaimIdentity(operation) {
		return workspaceComputeClaimLegacyCandidate(operation) && validWorkspaceLaunchLegacyComputeClaimIdentity(operation)
	}
	return input.PoolID == operation.ComputePoolID && input.MachineName == operation.ComputeMachineName && input.NodeName == operation.ComputeNodeName &&
		input.CVMInstanceID == operation.ComputeCVMInstanceID && input.PrivateIP == operation.ComputePrivateIP && input.InstanceType == operation.ComputeInstanceType &&
		input.Zone == operation.ComputeZone
}

func workspaceComputeClaimRequestHash(input workspaceComputeClaimRecoveryRequest, idempotencyKey string) string {
	return stableID(
		input.LaunchOperationID, input.AccountID, input.WorkspaceID, input.ComputeID, input.StorageID, input.PackageID,
		input.PoolID, input.NodePoolID, input.MachineName, input.NodeName, input.CVMInstanceID, input.PrivateIP, input.InstanceType, input.Zone,
		input.MergedMainSHA, input.CloudImageDigest, input.WorkspaceImageDigest, input.ApprovalID, input.ApprovalDigest,
		input.ExpiresAt, input.CustomerEmail, input.RecoveryKey, input.Confirmation, idempotencyKey,
	)
}

func workspaceComputeClaimAttemptLimitsExact(limits workspaceComputeClaimAttemptLimits) bool {
	return limits.Claim == (workspaceComputeClaimProviderAttemptLimits{Sub2API: 0, Tencent: 5, Kubernetes: 1}) &&
		limits.Storage == 1 && limits.Attachment == 1 && limits.Secret == 1 && limits.Runtime == 1 && limits.Activation == 1 && limits.Receipt == 1
}

func equalWorkspaceComputeClaimStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func workspaceLaunchReadbackRecoveryApprovalDigest(approval workspaceLaunchReadbackRecoveryApproval) string {
	payload := map[string]any{
		"schemaVersion": approval.SchemaVersion, "approvalId": approval.ApprovalID, "expiresAt": approval.ExpiresAt,
		"mergedMainSha": approval.MergedMainSHA, "cloudImageDigest": approval.CloudImageDigest, "workspaceImageDigest": approval.WorkspaceImageDigest,
		"confirmation": approval.Confirmation, "idempotencyKey": approval.IdempotencyKey, "recoveryKey": approval.RecoveryKey, "stage": approval.Stage,
		"customer": structToMap(approval.Customer), "target": structToMap(approval.Target), "resources": structToMap(approval.Resources),
		"operationIds": structToMap(approval.OperationIDs), "attemptBudget": structToMap(approval.AttemptBudget),
		"allowedWrites": approval.AllowedWrites, "forbiddenWrites": approval.ForbiddenWrites,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func workspaceLaunchReadbackRecoveryApprovalMatches(got, want workspaceLaunchReadbackRecoveryApproval) bool {
	gotJSON, gotErr := json.Marshal(got)
	wantJSON, wantErr := json.Marshal(want)
	return gotErr == nil && wantErr == nil && bytes.Equal(gotJSON, wantJSON)
}

type workspaceLaunchReadbackRecoveryAuthority struct {
	OperationIDs workspaceLaunchReadbackRecoveryOperationIDs
	Attachment   clients.StorageAttachment
	Secret       clients.GatewaySecretWriteResult
	Runtime      clients.WorkspaceRuntime
}

type workspaceLaunchReadbackFabricOperationSpec struct {
	Stage, Action, ResourceKind, ResourceID, IdempotencyKey string
}

func workspaceLaunchReadbackFabricOperationSpecs(operation workspaceLaunchOperation) []workspaceLaunchReadbackFabricOperationSpec {
	return []workspaceLaunchReadbackFabricOperationSpec{
		{Stage: "compute", Action: "create_compute_allocation", ResourceKind: "compute_allocation", ResourceID: operation.ComputeID, IdempotencyKey: operation.ID + ":compute"},
		{Stage: "storage", Action: "create_storage_volume", ResourceKind: "storage_volume", ResourceID: operation.StorageID, IdempotencyKey: operation.ID + ":storage"},
		{Stage: "attachment", Action: "create_storage_attachment", ResourceKind: "storage_attachment", ResourceID: firstNonEmpty(operation.AttachmentID, workspaceLaunchStorageAttachmentID(operation)), IdempotencyKey: operation.AttachmentOperationID},
		{Stage: "secret", Action: "upsert_gateway_secret", ResourceKind: "gateway_secret", ResourceID: workspaceGatewaySecretReference(operation.WorkspaceID), IdempotencyKey: operation.WorkspaceOperationID + ":secret:gateway-secret"},
		{Stage: "runtime", Action: "create_workspace_runtime", ResourceKind: "workspace_runtime", ResourceID: operation.WorkspaceID, IdempotencyKey: operation.WorkspaceOperationID + ":runtime"},
	}
}

func workspaceLaunchStorageAttachmentID(operation workspaceLaunchOperation) string {
	return "att_" + workspaceComputeClaimStableSuffix(operation.AttachmentOperationID)[:18]
}

func workspaceLaunchStorageAttachmentFabricOperationID(operation workspaceLaunchOperation) string {
	return "op_create_storage_attachment_" + workspaceComputeClaimStableSuffix(
		operation.AttachmentOperationID, "storage_attachment", "create_storage_attachment",
	)[:12]
}

func workspaceLaunchStorageAttachmentRequestHash(operation workspaceLaunchOperation) string {
	payload, err := json.Marshal(clients.StorageAttachmentInput{
		WorkspaceID: operation.WorkspaceID,
		ComputeID:   operation.ComputeID,
		VolumeID:    operation.StorageID,
	})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func workspaceLaunchReadbackStageIndex(stage string) int {
	for index, candidate := range workspaceLaunchContinuationStages {
		if candidate == stage {
			return index
		}
	}
	if stage == "compute" {
		return -1
	}
	return len(workspaceLaunchContinuationStages) + 1
}

func workspaceLaunchReadbackFabricOperation(operations []clients.FabricOperation, operation workspaceLaunchOperation, spec workspaceLaunchReadbackFabricOperationSpec, required bool) (clients.FabricOperation, bool, error) {
	groups := map[string][]clients.FabricOperation{}
	for _, candidate := range operations {
		related := candidate.Action == spec.Action && (candidate.IdempotencyKey == spec.IdempotencyKey ||
			candidate.AccountID == operation.AccountID && candidate.WorkspaceID == operation.WorkspaceID && candidate.ResourceID == spec.ResourceID)
		if !related {
			continue
		}
		if candidate.ResourceKind != spec.ResourceKind || candidate.ResourceID != spec.ResourceID || candidate.AccountID != operation.AccountID ||
			candidate.WorkspaceID != operation.WorkspaceID || candidate.IdempotencyKey != spec.IdempotencyKey || candidate.ID == "" ||
			candidate.OperationID == "" || candidate.RequestHash == "" ||
			(candidate.Status != "started" && candidate.Status != "failed" && candidate.Status != "succeeded") {
			return clients.FabricOperation{}, false, errBillingReviewIdentity
		}
		groups[candidate.OperationID] = append(groups[candidate.OperationID], candidate)
	}
	if len(groups) == 0 {
		if required {
			return clients.FabricOperation{}, false, errBillingReviewProviderFact
		}
		return clients.FabricOperation{}, false, nil
	}
	if !required || len(groups) != 1 {
		return clients.FabricOperation{}, false, errBillingReviewIdentity
	}
	var history []clients.FabricOperation
	for _, candidates := range groups {
		history = candidates
	}
	requestHash := history[0].RequestHash
	bestRank, bestIndex := 0, -1
	for index, candidate := range history {
		if candidate.RequestHash != requestHash {
			return clients.FabricOperation{}, false, errBillingReviewIdentity
		}
		rank := map[string]int{"started": 1, "failed": 2, "succeeded": 3}[candidate.Status]
		if rank == bestRank {
			return clients.FabricOperation{}, false, errBillingReviewIdentity
		}
		if rank > bestRank {
			bestRank, bestIndex = rank, index
		}
	}
	return history[bestIndex], true, nil
}

func workspaceLaunchReadbackProviderOperationID(tags map[string]string, accountID, workspaceID, resourceID string) (string, bool) {
	operationID := strings.TrimSpace(tags["opl_operation_id"])
	return operationID, tags["opl_account_id"] == accountID && tags["opl_workspace_id"] == workspaceID && tags["opl_resource_id"] == resourceID && operationID != ""
}

func workspaceLaunchReadbackOperationIdentity(candidate clients.FabricOperation, resourceOperationID, providerOperationID string) workspaceLaunchReadbackRecoveryFabricOperationIdentity {
	return workspaceLaunchReadbackRecoveryFabricOperationIdentity{
		IdempotencyKey: candidate.IdempotencyKey, FabricRecordID: candidate.ID, FabricOperationID: candidate.OperationID,
		RequestHash: candidate.RequestHash, ResourceOperationID: resourceOperationID, ProviderOperationID: providerOperationID,
	}
}

func workspaceLaunchReadbackStageOperationIdentity(operationIDs workspaceLaunchReadbackRecoveryOperationIDs, stage string) (workspaceLaunchReadbackRecoveryFabricOperationIdentity, bool) {
	switch stage {
	case "attachment":
		return operationIDs.Attachment, true
	case "secret":
		return operationIDs.Secret, true
	case "runtime":
		return operationIDs.Runtime, true
	default:
		return workspaceLaunchReadbackRecoveryFabricOperationIdentity{}, false
	}
}

func setWorkspaceLaunchReadbackStageBinding(operationIDs *workspaceLaunchReadbackRecoveryOperationIDs, stage, digest string) bool {
	if operationIDs == nil || !computeClaimApprovalDigestPattern.MatchString(digest) {
		return false
	}
	switch stage {
	case "attachment":
		operationIDs.Attachment.ReadbackBindingDigest = digest
	case "secret":
		operationIDs.Secret.ReadbackBindingDigest = digest
	case "runtime":
		operationIDs.Runtime.ReadbackBindingDigest = digest
	default:
		return false
	}
	return true
}

func workspaceLaunchReadbackFabricSpec(operation workspaceLaunchOperation, stage string) (workspaceLaunchReadbackFabricOperationSpec, bool) {
	for _, spec := range workspaceLaunchReadbackFabricOperationSpecs(operation) {
		if spec.Stage == stage {
			return spec, true
		}
	}
	return workspaceLaunchReadbackFabricOperationSpec{}, false
}

func workspaceLaunchStageReadbackInput(operation workspaceLaunchOperation, stage string, identity workspaceLaunchReadbackRecoveryFabricOperationIdentity, expectedBindingDigest string) (clients.WorkspaceLaunchStageReadbackInput, error) {
	if _, ok := workspaceLaunchReadbackFabricSpec(operation, stage); !ok || identity.FabricRecordID == "" || identity.FabricOperationID == "" || identity.IdempotencyKey == "" || identity.RequestHash == "" {
		return clients.WorkspaceLaunchStageReadbackInput{}, errBillingReviewIdentity
	}
	runtimeOperationID := operation.WorkspaceOperationID + ":runtime"
	input := clients.WorkspaceLaunchStageReadbackInput{
		Stage: stage, FabricRecordID: identity.FabricRecordID, FabricOperationID: identity.FabricOperationID,
		AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, IdempotencyKey: identity.IdempotencyKey,
		RequestHash: identity.RequestHash, ComputeID: operation.ComputeID, StorageID: operation.StorageID,
		AttachmentID: firstNonEmpty(operation.AttachmentID, workspaceLaunchStorageAttachmentID(operation)), AttachmentOperationID: operation.AttachmentOperationID,
		RuntimeID:          firstNonEmpty(operation.RuntimeID, "rt_"+workspaceComputeClaimStableSuffix(operation.WorkspaceID, runtimeOperationID)[:18]),
		RuntimeOperationID: runtimeOperationID, ImageID: "one-person-lab-app",
		GatewaySecretRef:         firstNonEmpty(operation.GatewaySecretRef, workspaceGatewaySecretReference(operation.WorkspaceID)),
		GatewaySecretFingerprint: operation.WorkspaceKeyFingerprint, WorkspaceAPIKeyID: operation.WorkspaceAPIKeyID,
		ExpectedBindingDigest: expectedBindingDigest,
	}
	return input, nil
}

func workspaceLaunchStageReadbackOperation(operations []clients.FabricOperation, operation workspaceLaunchOperation, stage string) (clients.FabricOperation, error) {
	spec, ok := workspaceLaunchReadbackFabricSpec(operation, stage)
	if !ok {
		return clients.FabricOperation{}, errInvalidBillingReview
	}
	candidate, found, err := workspaceLaunchReadbackFabricOperation(operations, operation, spec, true)
	if err != nil || !found {
		return clients.FabricOperation{}, errors.Join(err, errBillingReviewProviderFact)
	}
	return candidate, nil
}

func workspaceLaunchOperationsWithStageProof(operations []clients.FabricOperation, candidate clients.FabricOperation, proof clients.WorkspaceLaunchStageReadbackProof) ([]clients.FabricOperation, error) {
	if proof.SchemaVersion != 1 || !proof.Eligible || proof.Reason != "none" || proof.Stage == "" || !computeClaimApprovalDigestPattern.MatchString(proof.BindingDigest) ||
		proof.Sub2APIMutationCount != 0 || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 || proof.FabricOperationMutationCount != 0 ||
		proof.Operation.ID != candidate.ID || proof.Operation.OperationID != candidate.OperationID || proof.Operation.Action != candidate.Action ||
		proof.Operation.ResourceKind != candidate.ResourceKind || proof.Operation.ResourceID != candidate.ResourceID || proof.Operation.AccountID != candidate.AccountID ||
		proof.Operation.WorkspaceID != candidate.WorkspaceID || proof.Operation.IdempotencyKey != candidate.IdempotencyKey || proof.Operation.RequestHash != candidate.RequestHash ||
		proof.Operation.Status != "succeeded" {
		return nil, errBillingReviewProviderFact
	}
	result := append([]clients.FabricOperation(nil), operations...)
	matches := 0
	for index := range result {
		if result[index].ID == candidate.ID {
			result[index] = proof.Operation
			matches++
		}
	}
	if matches != 1 {
		return nil, errBillingReviewProviderFact
	}
	return result, nil
}

func applyWorkspaceLaunchStageReadback(operation *workspaceLaunchOperation, stage string, authority workspaceLaunchReadbackRecoveryAuthority) error {
	if operation == nil {
		return errBillingReviewIdentity
	}
	switch stage {
	case "attachment":
		operation.AttachmentID = authority.Attachment.ID
	case "secret":
		operation.GatewaySecretRef, operation.WorkspaceKeyStatus = authority.Secret.SecretRef, "configured"
		operation.WorkspaceKeyFingerprint = authority.Secret.Fingerprint
	case "runtime":
		operation.RuntimeID, operation.RuntimeReady = authority.Runtime.ID, authority.Runtime.Ready
		operation.RuntimeServiceName, operation.RuntimeUsername = authority.Runtime.ServiceName, authority.Runtime.Access.Username
		operation.CredentialStatus, operation.CredentialVersion = authority.Runtime.Access.CredentialStatus, authority.Runtime.Access.CredentialVersion
		operation.CredentialSecretRef, operation.URL = authority.Runtime.Access.SecretRef, authority.Runtime.URL
	default:
		return errInvalidBillingReview
	}
	return nil
}

func workspaceLaunchStageConvergenceMatches(input clients.WorkspaceLaunchStageReadbackInput, proof clients.WorkspaceLaunchStageReadbackProof) bool {
	return proof.SchemaVersion == 1 && proof.Eligible && proof.Reason == "none" && proof.Stage == input.Stage &&
		proof.BindingDigest == input.ExpectedBindingDigest && proof.Sub2APIMutationCount == 0 && proof.TencentMutationCount == 0 && proof.KubernetesMutationCount == 0 &&
		(proof.FabricOperationMutationCount == 0 || proof.FabricOperationMutationCount == 1) &&
		proof.Operation.ID == input.FabricRecordID && proof.Operation.OperationID == input.FabricOperationID &&
		proof.Operation.IdempotencyKey == input.IdempotencyKey && proof.Operation.RequestHash == input.RequestHash && proof.Operation.Status == "succeeded"
}

func workspaceLaunchRecoveredStorageEvidenceMatches(operation workspaceLaunchOperation, currentStage string, truth clients.MonthlyProviderTruth, candidate clients.FabricOperation) bool {
	approval, proof := operation.ReadbackRecoveryApproval, operation.ReadbackRecoveryProof
	unknownBudget := workspaceLaunchStageBudget{Attempted: 1, Unknown: 1, Max: workspaceLaunchStageMax}
	confirmedBudget := workspaceLaunchStageBudget{Attempted: 1, Confirmed: 1, Max: workspaceLaunchStageMax}
	storageIndex, currentIndex := workspaceLaunchReadbackStageIndex("storage"), workspaceLaunchReadbackStageIndex(currentStage)
	recoveryStage, recoveryIndex := "", len(workspaceLaunchContinuationStages)+1
	expiresAtValue := ""
	if approval != nil {
		recoveryStage, expiresAtValue = approval.Stage, approval.ExpiresAt
		recoveryIndex = workspaceLaunchReadbackStageIndex(recoveryStage)
	}
	priorStagesConfirmed := workspaceLaunchReadbackRecoveryStageValid(currentStage) && workspaceLaunchReadbackRecoveryStageValid(recoveryStage) &&
		currentIndex > storageIndex && recoveryIndex >= storageIndex && recoveryIndex < currentIndex
	for index := storageIndex; priorStagesConfirmed && index < currentIndex; index++ {
		priorStagesConfirmed = operation.ContinuationAttemptBudgets[workspaceLaunchContinuationStages[index]] == confirmedBudget
	}
	expiresAt, expiresErr := time.Parse(time.RFC3339, expiresAtValue)
	if !priorStagesConfirmed || approval == nil || proof == nil || approval.SchemaVersion != 1 ||
		approval.ApprovalID == "" || approval.IdempotencyKey == "" || approval.RecoveryKey == "" || approval.Confirmation != workspaceLaunchReadbackRecoveryConfirmation ||
		approval.ApprovalDigest == "" || workspaceLaunchReadbackRecoveryApprovalDigest(*approval) != approval.ApprovalDigest || expiresErr != nil || expiresAt.IsZero() ||
		!computeClaimMergedSHAPattern.MatchString(approval.MergedMainSHA) || !computeClaimCloudDigestPattern.MatchString(approval.CloudImageDigest) ||
		approval.WorkspaceImageDigest != operation.WorkspaceImageDigest || approval.Customer.AccountID != operation.AccountID || approval.Customer.OwnerUserID != operation.OwnerUserID ||
		approval.AttemptBudget != unknownBudget || !workspaceLaunchReadbackRecoveryOperationPlanMatches(approval.OperationIDs, operation) ||
		approval.OperationIDs.Storage.ReadbackBindingDigest != "" || !workspaceLaunchReadbackRecoveryStageBindingMatches(approval.OperationIDs, recoveryStage) ||
		!equalWorkspaceComputeClaimStrings(approval.AllowedWrites, workspaceLaunchReadbackRecoveryAllowedWrites(recoveryStage)) ||
		!equalWorkspaceComputeClaimStrings(approval.ForbiddenWrites, workspaceLaunchReadbackRecoveryForbiddenWrites) ||
		proof.SchemaVersion != 1 || !proof.Eligible || proof.Reason != "none" || proof.Stage != recoveryStage || proof.Customer != approval.Customer ||
		proof.Target != approval.Target || proof.Resources != approval.Resources || proof.OperationIDs != approval.OperationIDs ||
		proof.WorkspaceImageDigest != approval.WorkspaceImageDigest || proof.AttemptBudget != approval.AttemptBudget ||
		!equalWorkspaceComputeClaimStrings(proof.AllowedWrites, approval.AllowedWrites) || !equalWorkspaceComputeClaimStrings(proof.ForbiddenWrites, approval.ForbiddenWrites) ||
		proof.Sub2APIMutationCount != 0 || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 {
		return false
	}

	providerOperationID, tagsOK := workspaceLaunchReadbackProviderOperationID(truth.Storage.CostTags, operation.AccountID, operation.WorkspaceID, operation.StorageID)
	storageIdentity := workspaceLaunchReadbackOperationIdentity(candidate, truth.Storage.OperationID, providerOperationID)
	expectedTarget, targetErr := workspaceLaunchReadbackRecoveryExpectedTarget(operation, truth)
	resources := approval.Resources
	return tagsOK && truth.Storage.OperationID == candidate.IdempotencyKey && approval.OperationIDs.Storage == storageIdentity && targetErr == nil && approval.Target == expectedTarget &&
		resources.ComputeAllocationID == operation.ComputeID && resources.ComputeProviderResourceID == truth.Compute.ProviderResourceID &&
		resources.StorageVolumeID == operation.StorageID && resources.StorageProviderResourceID == truth.Storage.ProviderResourceID &&
		resources.StorageZone == truth.Storage.Zone && resources.StorageSizeGB == truth.Storage.SizeGB && resources.StorageChargeType == truth.Storage.ProviderData["chargeType"] &&
		resources.StorageRenewFlag == truth.Storage.RenewFlag && resources.StorageDeadline == truth.Storage.Deadline &&
		resources.GatewaySecretRef == workspaceGatewaySecretReference(operation.WorkspaceID) && resources.WorkspaceAPIKeyID == operation.WorkspaceAPIKeyID
}

func workspaceLaunchReadbackRecoveryAuthorityForOperation(operation workspaceLaunchOperation, stage string, truth clients.MonthlyProviderTruth, ownership clients.MachineOwnership, operations []clients.FabricOperation) (workspaceLaunchReadbackRecoveryAuthority, error) {
	compute := truth.Compute
	instanceID := firstNonEmpty(compute.CVMInstanceID, compute.InstanceID)
	computeProviderOperationID, computeTagsOK := workspaceLaunchReadbackProviderOperationID(compute.CostTags, operation.AccountID, operation.WorkspaceID, operation.ComputeID)
	if ownership.ID == "" || ownership.ResourceID != operation.ComputeID || ownership.AccountID != operation.AccountID || ownership.WorkspaceID != operation.WorkspaceID ||
		ownership.PackageID != operation.PackageID || ownership.NodePoolID != compute.NodePoolID || ownership.MachineID != compute.MachineName ||
		ownership.InstanceID != instanceID || ownership.NodeName != compute.NodeName || ownership.Status != "active" || !computeTagsOK || computeProviderOperationID != ownership.ID {
		return workspaceLaunchReadbackRecoveryAuthority{}, errBillingReviewIdentity
	}

	authority := workspaceLaunchReadbackRecoveryAuthority{OperationIDs: workspaceLaunchReadbackRecoveryOperationIDs{
		LaunchOperationID: operation.ID, LaunchRequestHash: operation.RequestHash, MachineOwnershipID: ownership.ID,
		ActivationOperationID: operation.ID + ":activation", ReceiptOperationID: operation.ID + ":purchase-receipt",
	}}
	currentIndex := workspaceLaunchReadbackStageIndex(stage)
	for _, spec := range workspaceLaunchReadbackFabricOperationSpecs(operation) {
		required := spec.Stage == "compute" || workspaceLaunchReadbackStageIndex(spec.Stage) <= currentIndex
		candidate, found, err := workspaceLaunchReadbackFabricOperation(operations, operation, spec, required)
		if err != nil {
			return workspaceLaunchReadbackRecoveryAuthority{}, err
		}
		identity := workspaceLaunchReadbackRecoveryFabricOperationIdentity{IdempotencyKey: spec.IdempotencyKey}
		if found {
			if spec.Stage != stage && candidate.Status != "succeeded" &&
				(spec.Stage != "storage" || !workspaceLaunchRecoveredStorageEvidenceMatches(operation, stage, truth, candidate)) {
				return workspaceLaunchReadbackRecoveryAuthority{}, errBillingReviewProviderFact
			}
			switch spec.Stage {
			case "compute":
				identity = workspaceLaunchReadbackOperationIdentity(candidate, candidate.IdempotencyKey, computeProviderOperationID)
			case "storage":
				providerOperationID, ok := workspaceLaunchReadbackProviderOperationID(truth.Storage.CostTags, operation.AccountID, operation.WorkspaceID, operation.StorageID)
				if !ok || truth.Storage.OperationID != candidate.IdempotencyKey {
					return workspaceLaunchReadbackRecoveryAuthority{}, errBillingReviewIdentity
				}
				identity = workspaceLaunchReadbackOperationIdentity(candidate, truth.Storage.OperationID, providerOperationID)
			case "attachment":
				resource, ok := candidate.RedactedProviderPayload["resource"]
				if !ok || jsonRoundTrip(resource, &authority.Attachment) != nil || authority.Attachment.ID != workspaceLaunchStorageAttachmentID(operation) ||
					candidate.ResourceID != authority.Attachment.ID || candidate.OperationID != workspaceLaunchStorageAttachmentFabricOperationID(operation) ||
					candidate.RequestHash != workspaceLaunchStorageAttachmentRequestHash(operation) || authority.Attachment.OperationID != candidate.IdempotencyKey ||
					authority.Attachment.WorkspaceID != operation.WorkspaceID || authority.Attachment.ComputeID != operation.ComputeID || authority.Attachment.VolumeID != operation.StorageID ||
					authority.Attachment.Status != "attached" || operation.AttachmentID != "" && operation.AttachmentID != authority.Attachment.ID {
					return workspaceLaunchReadbackRecoveryAuthority{}, errBillingReviewIdentity
				}
				providerOperationID, ok := workspaceLaunchReadbackProviderOperationID(authority.Attachment.CostTags, operation.AccountID, operation.WorkspaceID, authority.Attachment.ID)
				if !ok {
					return workspaceLaunchReadbackRecoveryAuthority{}, errBillingReviewIdentity
				}
				identity = workspaceLaunchReadbackOperationIdentity(candidate, authority.Attachment.OperationID, providerOperationID)
			case "secret":
				resource, ok := candidate.RedactedProviderPayload["resource"]
				if !ok || jsonRoundTrip(resource, &authority.Secret) != nil || authority.Secret.SecretRef != spec.ResourceID || authority.Secret.Version == "" ||
					authority.Secret.Fingerprint == "" || operation.WorkspaceKeyFingerprint != "" && operation.WorkspaceKeyFingerprint != authority.Secret.Fingerprint {
					return workspaceLaunchReadbackRecoveryAuthority{}, errBillingReviewIdentity
				}
				identity = workspaceLaunchReadbackOperationIdentity(candidate, candidate.IdempotencyKey, "")
			case "runtime":
				resource, ok := candidate.RedactedProviderPayload["resource"]
				if !ok || jsonRoundTrip(resource, &authority.Runtime) != nil || authority.Runtime.ID == "" || authority.Runtime.OperationID != candidate.IdempotencyKey ||
					authority.Runtime.WorkspaceID != operation.WorkspaceID || authority.Runtime.ServiceName == "" || operation.RuntimeID != "" && operation.RuntimeID != authority.Runtime.ID {
					return workspaceLaunchReadbackRecoveryAuthority{}, errBillingReviewIdentity
				}
				providerOperationID, ok := workspaceLaunchReadbackProviderOperationID(authority.Runtime.CostTags, operation.AccountID, operation.WorkspaceID, authority.Runtime.ID)
				if !ok {
					return workspaceLaunchReadbackRecoveryAuthority{}, errBillingReviewIdentity
				}
				identity = workspaceLaunchReadbackOperationIdentity(candidate, authority.Runtime.OperationID, providerOperationID)
			}
		}
		switch spec.Stage {
		case "compute":
			authority.OperationIDs.Compute = identity
		case "storage":
			authority.OperationIDs.Storage = identity
		case "attachment":
			authority.OperationIDs.Attachment = identity
		case "secret":
			authority.OperationIDs.Secret = identity
		case "runtime":
			authority.OperationIDs.Runtime = identity
		}
	}
	if operation.RequestHash == "" {
		return workspaceLaunchReadbackRecoveryAuthority{}, errBillingReviewIdentity
	}
	return authority, nil
}

func workspaceLaunchReadbackRecoveryExpectedTarget(operation workspaceLaunchOperation, truth clients.MonthlyProviderTruth) (workspaceLaunchReadbackRecoveryTarget, error) {
	compute, storage := truth.Compute, truth.Storage
	instanceID := firstNonEmpty(compute.CVMInstanceID, compute.InstanceID)
	periodStart, startErr := time.Parse(time.RFC3339, operation.PeriodStart)
	paidThrough, paidErr := time.Parse(time.RFC3339, operation.PaidThrough)
	computeDeadline, computeDeadlineErr := time.Parse(time.RFC3339, compute.Deadline)
	storageDeadline, storageDeadlineErr := time.Parse(time.RFC3339, storage.Deadline)
	if compute.ID != operation.ComputeID || compute.AccountID != operation.AccountID || compute.WorkspaceID != operation.WorkspaceID || compute.PackageID != operation.PackageID ||
		compute.PoolID == "" || compute.NodePoolID != operation.ComputeNodePoolID || compute.MachineName == "" || compute.NodeName == "" || instanceID == "" || compute.PrivateIP == "" ||
		compute.InstanceType == "" || compute.Zone == "" || compute.ChargeType != "PREPAID" || compute.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" ||
		storage.ID != operation.StorageID || storage.AccountID != operation.AccountID || storage.WorkspaceID != operation.WorkspaceID || storage.SizeGB != operation.StorageGB ||
		storage.Zone != compute.Zone || storage.RenewFlag != compute.RenewFlag || storage.ProviderData["chargeType"] != compute.ChargeType ||
		operation.PriceVersion == "" || operation.TotalChargeUSDMicros <= 0 || operation.BillingAnchorDay < 1 || operation.BillingAnchorDay > 31 ||
		startErr != nil || paidErr != nil || !paidThrough.After(periodStart) || computeDeadlineErr != nil || storageDeadlineErr != nil ||
		computeDeadline.Before(paidThrough) || storageDeadline.Before(paidThrough) {
		return workspaceLaunchReadbackRecoveryTarget{}, errBillingReviewIdentity
	}
	return workspaceLaunchReadbackRecoveryTarget{
		LaunchOperationID: operation.ID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, ComputeAllocationID: operation.ComputeID,
		StorageID: operation.StorageID, PackageID: operation.PackageID, PoolID: compute.PoolID, NodePoolID: compute.NodePoolID,
		MachineName: compute.MachineName, NodeName: compute.NodeName, CVMInstanceID: instanceID, PrivateIP: compute.PrivateIP,
		InstanceType: compute.InstanceType, Zone: compute.Zone, ChargeType: compute.ChargeType, PeriodMonths: 1, RenewFlag: compute.RenewFlag,
		Deadline: compute.Deadline, StorageGB: operation.StorageGB, AutoRenew: operation.AutoRenew, PriceVersion: operation.PriceVersion,
		TotalChargeUSDMicros: operation.TotalChargeUSDMicros, PeriodStart: operation.PeriodStart, PaidThrough: operation.PaidThrough, BillingAnchorDay: operation.BillingAnchorDay,
	}, nil
}

func workspaceLaunchReadbackRecoveryExpectedResources(operation workspaceLaunchOperation, truth clients.MonthlyProviderTruth, authority workspaceLaunchReadbackRecoveryAuthority) workspaceLaunchReadbackRecoveryResources {
	chargeType := truth.Storage.ProviderData["chargeType"]
	return workspaceLaunchReadbackRecoveryResources{
		ComputeAllocationID: operation.ComputeID, ComputeProviderResourceID: truth.Compute.ProviderResourceID,
		StorageVolumeID: operation.StorageID, StorageProviderResourceID: truth.Storage.ProviderResourceID, StorageZone: truth.Storage.Zone,
		StorageSizeGB: truth.Storage.SizeGB, StorageChargeType: chargeType, StorageRenewFlag: truth.Storage.RenewFlag, StorageDeadline: truth.Storage.Deadline,
		AttachmentID: operation.AttachmentID, AttachmentProviderID: authority.Attachment.ProviderAttachmentID,
		GatewaySecretRef: workspaceGatewaySecretReference(operation.WorkspaceID), GatewaySecretFingerprint: operation.WorkspaceKeyFingerprint,
		WorkspaceAPIKeyID: operation.WorkspaceAPIKeyID, RuntimeID: operation.RuntimeID, RuntimeServiceName: operation.RuntimeServiceName, ReceiptID: operation.ReceiptID,
	}
}

func workspaceComputeClaimStableSuffix(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, ":")))
	return hex.EncodeToString(sum[:])
}

func workspaceComputeClaimExpectedResources(operation workspaceLaunchOperation, storageState, storageProviderResourceID string) workspaceComputeClaimApprovalResources {
	runtimeOperationID := operation.WorkspaceOperationID + ":runtime"
	return workspaceComputeClaimApprovalResources{
		ComputeOperationID: operation.ID + ":compute", StorageOperationID: operation.ID + ":storage",
		StorageState: storageState, StorageProviderResourceID: storageProviderResourceID,
		AttachmentID: "att_" + workspaceComputeClaimStableSuffix(operation.AttachmentOperationID)[:18], AttachmentOperationID: operation.AttachmentOperationID,
		WorkspaceAPIKeyID: strconv.FormatInt(operation.WorkspaceAPIKeyID, 10), GatewaySecretRef: workspaceGatewaySecretReference(operation.WorkspaceID),
		SecretOperationID: operation.WorkspaceOperationID + ":secret:gateway-secret",
		RuntimeID:         "rt_" + workspaceComputeClaimStableSuffix(operation.WorkspaceID, runtimeOperationID)[:18], RuntimeOperationID: runtimeOperationID,
		ReceiptOperationID: operation.ID + ":purchase-receipt",
	}
}

func workspaceComputeClaimStorageBindingValid(state, providerResourceID string) bool {
	switch state {
	case "storage_not_started":
		return providerResourceID == ""
	case "storage_existing_exact":
		return strings.HasPrefix(providerResourceID, "disk-")
	default:
		return false
	}
}

func workspaceComputeClaimAllowedWritesForStorage(state string) []string {
	storageWrite := "create_original_cbs"
	if state == "storage_existing_exact" {
		storageWrite = "reuse_original_cbs"
	}
	return []string{
		"claim_existing_cvm_node", storageWrite, "create_original_pv_pvc_attachment", "upsert_original_gateway_secret",
		"create_original_workspace_runtime", "activate_original_workspace", "record_original_purchase_receipt",
	}
}

func workspaceComputeClaimApprovalTargetFromOperation(operation workspaceLaunchOperation) workspaceComputeClaimApprovalTarget {
	return workspaceComputeClaimApprovalTarget{
		LaunchOperationID: operation.ID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
		ComputeAllocationID: operation.ComputeID, StorageID: operation.StorageID, PackageID: operation.PackageID,
		PoolID: operation.ComputePoolID, NodePoolID: operation.ComputeNodePoolID, MachineName: operation.ComputeMachineName,
		NodeName: operation.ComputeNodeName, CVMInstanceID: operation.ComputeCVMInstanceID, PrivateIP: operation.ComputePrivateIP,
		InstanceType: operation.ComputeInstanceType, Zone: operation.ComputeZone, ChargeType: operation.ComputeChargeType,
		PeriodMonths: 1, RenewFlag: operation.ComputeRenewFlag, Deadline: operation.ComputeDeadline,
	}
}

func workspaceComputeClaimApprovalDigest(binding workspaceComputeClaimApprovalBinding) string {
	approval := map[string]any{
		"schemaVersion": binding.SchemaVersion, "approvalId": binding.ApprovalID, "expiresAt": binding.ExpiresAt,
		"mergedMainSha": binding.MergedMainSHA, "cloudImageDigest": binding.CloudImageDigest, "workspaceImageDigest": binding.WorkspaceImageDigest,
		"confirmation": binding.Confirmation, "idempotencyKey": binding.IdempotencyKey, "recoveryKey": binding.RecoveryKey,
		"customer": structToMap(binding.Customer), "target": structToMap(binding.Target), "resources": structToMap(binding.Resources),
		"attemptLimits": structToMap(binding.AttemptLimits), "allowedWrites": binding.AllowedWrites, "forbiddenWrites": binding.ForbiddenWrites,
	}
	payload, err := json.Marshal(approval)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func workspaceComputeClaimApprovalBindingMatches(got, want workspaceComputeClaimApprovalBinding) bool {
	gotPayload, gotErr := json.Marshal(got)
	wantPayload, wantErr := json.Marshal(want)
	return gotErr == nil && wantErr == nil && bytes.Equal(gotPayload, wantPayload)
}

func workspaceComputeClaimWorkspaceImageDigestMatches(operation workspaceLaunchOperation, input workspaceComputeClaimRecoveryRequest) bool {
	if operation.WorkspaceImageDigest == input.WorkspaceImageDigest {
		return true
	}
	return operation.WorkspaceImageDigest == "" && workspaceComputeClaimCanonical(operation) &&
		operation.ComputeClaimApproval == nil && operation.ComputeClaimProof == nil
}

func (app *controlPlaneServer) workspaceComputeClaimApprovalBinding(ctx context.Context, operation workspaceLaunchOperation, input workspaceComputeClaimRecoveryRequest, key string, allowExpiredExactReplay bool) (workspaceComputeClaimApprovalBinding, error) {
	expiresAt, expiresErr := time.Parse(time.RFC3339, input.ExpiresAt)
	account, accountFound, accountErr := app.tables.GetAccount(ctx, operation.AccountID)
	owner, ownerFound, ownerErr := app.tables.GetUser(ctx, operation.OwnerUserID)
	resources := workspaceComputeClaimExpectedResources(operation, input.Resources.StorageState, input.Resources.StorageProviderResourceID)
	target := workspaceComputeClaimApprovalTargetFromOperation(operation)
	binding := workspaceComputeClaimApprovalBinding{
		SchemaVersion: 2, ApprovalID: input.ApprovalID, ApprovalDigest: input.ApprovalDigest, ExpiresAt: input.ExpiresAt,
		MergedMainSHA: input.MergedMainSHA, CloudImageDigest: input.CloudImageDigest, WorkspaceImageDigest: input.WorkspaceImageDigest,
		Confirmation: input.Confirmation, IdempotencyKey: key, RecoveryKey: input.RecoveryKey,
		Customer: workspaceComputeClaimApprovalCustomer{Email: input.CustomerEmail, AccountID: operation.AccountID}, Target: target,
		Resources: resources, AttemptLimits: input.AttemptLimits,
		AllowedWrites: append([]string(nil), input.AllowedWrites...), ForbiddenWrites: append([]string(nil), input.ForbiddenWrites...),
	}
	if expiresErr != nil || (!allowExpiredExactReplay && !expiresAt.After(time.Now().UTC())) || accountErr != nil || ownerErr != nil || !accountFound || !ownerFound ||
		!ownsActiveAccount(account, owner) || stringValue(owner["id"]) != operation.OwnerUserID || stringValue(owner["role"]) != "owner" ||
		normalizeEmail(stringValue(owner["email"])) != input.CustomerEmail || input.AccountID != operation.AccountID ||
		!workspaceComputeClaimWorkspaceImageDigestMatches(operation, input) || !workspaceComputeClaimStorageBindingValid(input.Resources.StorageState, input.Resources.StorageProviderResourceID) ||
		input.Resources != resources || !workspaceComputeClaimAttemptLimitsExact(input.AttemptLimits) ||
		!equalWorkspaceComputeClaimStrings(input.AllowedWrites, workspaceComputeClaimAllowedWritesForStorage(input.Resources.StorageState)) || !equalWorkspaceComputeClaimStrings(input.ForbiddenWrites, workspaceComputeClaimForbiddenWrites) ||
		input.PoolID != target.PoolID || input.MachineName != target.MachineName || input.NodeName != target.NodeName || input.CVMInstanceID != target.CVMInstanceID ||
		input.PrivateIP != target.PrivateIP || input.InstanceType != target.InstanceType || input.Zone != target.Zone ||
		workspaceComputeClaimApprovalDigest(binding) != input.ApprovalDigest {
		return workspaceComputeClaimApprovalBinding{}, errWorkspaceComputeClaimIdentity
	}
	return binding, nil
}

func (app *controlPlaneServer) bindWorkspaceComputeClaimApproval(ctx context.Context, operation *workspaceLaunchOperation, input workspaceComputeClaimRecoveryRequest, key string, persist bool) error {
	binding, err := app.workspaceComputeClaimApprovalBinding(ctx, *operation, input, key, operation.ComputeClaimApproval != nil)
	if err != nil {
		return err
	}
	if operation.ComputeClaimApproval != nil {
		if !workspaceComputeClaimApprovalBindingMatches(*operation.ComputeClaimApproval, binding) {
			return errWorkspaceComputeClaimIdentity
		}
		return nil
	}
	if !persist {
		return errWorkspaceComputeClaimIdentity
	}
	if operation.WorkspaceImageDigest == "" {
		operation.WorkspaceImageDigest = input.WorkspaceImageDigest
	}
	operation.ComputeClaimApproval = &binding
	return app.persistWorkspaceLaunch(ctx, operation)
}

func safeWorkspaceComputeClaimReason(reason string) bool {
	switch reason {
	case "none", "local_identity", "provider_describe", "iam_rbac", "multiple_candidate", "identity_mismatch", "node_ownership_conflict", "storage_already_started":
		return true
	default:
		return false
	}
}

func safeWorkspaceComputeClaimFailureStage(value string) bool {
	switch value {
	case "", "cvm_pre_read", "cvm_conflict_check", "cvm_mutation_precondition", "cvm_rename_readback", "cvm_tag_readback", "cvm_final_readback",
		"cvm_provisioner_transport", "cvm_mutation_evidence", "node_pre_cvm_read", "node_pre_read", "node_conflict_check", "node_patch_build",
		"node_patch_readback", "node_final_readback", "claim_final_readback":
		return true
	default:
		return false
	}
}

func safeWorkspaceComputeClaimProviderErrorClass(value string) bool {
	switch value {
	case "", "client_unavailable", "malformed_readback", "ownership_conflict", "readback_mismatch", "timeout", "iam_rbac", "provider_error",
		"transport_error", "evidence_incomplete":
		return true
	default:
		return false
	}
}

func workspaceComputeClaimMissingField(domain, field string) bool {
	switch domain {
	case "cvm":
		switch field {
		case "instance", "instance_name", "opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id":
			return true
		}
	case "node":
		return field == "node_ownership"
	}
	return false
}

func workspaceComputeClaimMutationEvidenceMatches(evidence clients.ComputeClaimMutationEvidence, count, maximum int, domain string, confirmed bool) bool {
	if count < 0 || count > maximum || evidence.Attempted != count || evidence.Confirmed < 0 || evidence.Confirmed > maximum ||
		evidence.Unknown < 0 || evidence.Unknown > maximum || evidence.Confirmed > evidence.Attempted ||
		evidence.Unknown > evidence.Attempted || evidence.Confirmed+evidence.Unknown > evidence.Attempted {
		return false
	}
	seen := map[string]bool{}
	for _, field := range evidence.Missing {
		if !workspaceComputeClaimMissingField(domain, field) || seen[field] {
			return false
		}
		seen[field] = true
	}
	if confirmed {
		return evidence.Confirmed == evidence.Attempted && evidence.Unknown == 0 && len(evidence.Missing) == 0
	}
	return evidence.Confirmed <= evidence.Attempted
}

func workspaceComputeClaimEvidenceMatches(proof clients.ComputeClaimRecoveryProof, confirmed bool) bool {
	return proof.Evidence != nil && safeWorkspaceComputeClaimFailureStage(proof.FailureStage) &&
		safeWorkspaceComputeClaimProviderErrorClass(proof.ProviderErrorClass) &&
		workspaceComputeClaimMutationEvidenceMatches(proof.Evidence.CVM, proof.TencentMutationCount, 5, "cvm", confirmed) &&
		workspaceComputeClaimMutationEvidenceMatches(proof.Evidence.Node, proof.KubernetesMutationCount, 1, "node", confirmed)
}

func workspaceComputeClaimProofBaseMatches(operation workspaceLaunchOperation, input workspaceComputeClaimRecoveryRequest, proof clients.ComputeClaimRecoveryProof) bool {
	return proof.SchemaVersion == 1 && proof.LaunchOperationID == operation.ID && proof.AccountID == operation.AccountID &&
		proof.WorkspaceID == operation.WorkspaceID && proof.ComputeAllocationID == operation.ComputeID && proof.StorageVolumeID == operation.StorageID &&
		proof.PackageID == operation.PackageID && proof.PoolID == input.PoolID && proof.NodePoolID == operation.ComputeNodePoolID &&
		proof.Sub2APIMutationCount == 0 && safeWorkspaceComputeClaimReason(proof.Reason) && workspaceComputeClaimEvidenceMatches(proof, false)
}

func workspaceComputeClaimProofEligible(operation workspaceLaunchOperation, input workspaceComputeClaimRecoveryRequest, proof clients.ComputeClaimRecoveryProof, claimed bool) bool {
	deadline, deadlineErr := time.Parse(time.RFC3339, proof.Deadline)
	storageMatchesApproval := input.Resources.StorageState == "" || proof.StorageState == input.Resources.StorageState && proof.StorageProviderResourceID == input.Resources.StorageProviderResourceID
	if !workspaceComputeClaimProofBaseMatches(operation, input, proof) || !proof.Eligible || proof.Reason != "none" ||
		!workspaceComputeClaimStorageBindingValid(proof.StorageState, proof.StorageProviderResourceID) || !storageMatchesApproval ||
		proof.MachineName != input.MachineName || proof.NodeName != input.NodeName || proof.CVMInstanceID != input.CVMInstanceID || proof.PrivateIP != input.PrivateIP ||
		proof.InstanceType != input.InstanceType || proof.Zone != input.Zone || proof.ChargeType != "PREPAID" || proof.PeriodMonths != 1 ||
		proof.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || deadlineErr != nil || deadline.IsZero() ||
		validWorkspaceLaunchComputeClaimIdentity(operation) && proof.Deadline != operation.ComputeDeadline ||
		!workspaceComputeClaimEvidenceMatches(proof, true) || proof.FailureStage != "" || proof.ProviderErrorClass != "" {
		return false
	}
	if claimed {
		return proof.NodeOwnershipState == "target_owned" && proof.CVMOwnershipState == "target_owned"
	}
	return (proof.NodeOwnershipState == "unallocated" || proof.NodeOwnershipState == "target_owned") &&
		(proof.CVMOwnershipState == "recoverable" || proof.CVMOwnershipState == "target_owned")
}

func workspaceComputeClaimSafeFailureForOperation(operation workspaceLaunchOperation, input workspaceComputeClaimRecoveryRequest, proof clients.ComputeClaimRecoveryProof) bool {
	if !workspaceComputeClaimProofBaseMatches(operation, input, proof) || proof.Eligible || proof.Reason == "none" || !safeWorkspaceComputeClaimReason(proof.Reason) {
		return false
	}
	return proof.FailureStage == "" && proof.ProviderErrorClass == "" || proof.FailureStage != "" && proof.ProviderErrorClass != ""
}

func persistWorkspaceComputeClaimIdentityFromProof(operation *workspaceLaunchOperation, proof clients.ComputeClaimRecoveryProof) bool {
	operation.ComputePoolID, operation.ComputeMachineName, operation.ComputeNodeName = proof.PoolID, proof.MachineName, proof.NodeName
	operation.ComputeCVMInstanceID, operation.ComputePrivateIP = proof.CVMInstanceID, proof.PrivateIP
	operation.ComputeInstanceType, operation.ComputeZone = proof.InstanceType, proof.Zone
	operation.ComputeChargeType, operation.ComputeRenewFlag, operation.ComputeDeadline = proof.ChargeType, proof.RenewFlag, proof.Deadline
	return validWorkspaceLaunchComputeClaimIdentity(*operation)
}

func workspaceComputeClaimCanonical(operation workspaceLaunchOperation) bool {
	return operation.Phase == "compute_claim_pending" && (operation.Status == "compute_claim_pending" || operation.Status == "manual_review")
}

func workspaceComputeClaimLegacyCandidate(operation workspaceLaunchOperation) bool {
	return operation.Status == "manual_review" && operation.Phase == "compute_fulfilling"
}

func (app *controlPlaneServer) loadWorkspaceComputeClaimOperation(ctx context.Context, operationID string, input workspaceComputeClaimRecoveryRequest, allowLegacy bool) (workspaceLaunchOperation, error) {
	operation, ok, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil {
		return workspaceLaunchOperation{}, err
	}
	if !ok {
		return workspaceLaunchOperation{}, errBillingReviewNotFound
	}
	if !workspaceComputeClaimRecoveryRequestMatches(operation, input) {
		return workspaceLaunchOperation{}, errWorkspaceComputeClaimIdentity
	}
	if !workspaceComputeClaimCanonical(operation) && (!allowLegacy || !workspaceComputeClaimLegacyCandidate(operation)) {
		return workspaceLaunchOperation{}, errWorkspaceComputeClaimNotPending
	}
	userID, err := app.sub2APIUserID(ctx, operation.AccountID)
	validIdentity := validWorkspaceLaunchComputeClaimIdentity(operation)
	if workspaceComputeClaimLegacyCandidate(operation) {
		validIdentity = validWorkspaceLaunchLegacyComputeClaimIdentity(operation)
	}
	if err != nil || !validIdentity || !workspaceLaunchChargeConfirmed(operation, userID) {
		return workspaceLaunchOperation{}, errWorkspaceComputeClaimIdentity
	}
	return operation, nil
}

func (app *controlPlaneServer) diagnoseWorkspaceComputeClaim(ctx context.Context, service *controlplane.Service, input workspaceComputeClaimRecoveryRequest) (clients.ComputeClaimRecoveryProof, error) {
	operation, ok, err := app.workspaceLaunchOperation(ctx, input.LaunchOperationID)
	if err != nil || !ok {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return clients.ComputeClaimRecoveryProof{}, err
	}
	unlock := app.lockResource("workspace-launch", operation.AccountID)
	defer unlock()
	operation, err = app.loadWorkspaceComputeClaimOperation(ctx, input.LaunchOperationID, input, true)
	if err != nil {
		return clients.ComputeClaimRecoveryProof{}, err
	}
	proof, proofErr := service.ComputeClaimRecoveryProof(ctx, workspaceComputeClaimRecoveryInput(operation, input))
	if !workspaceComputeClaimProofBaseMatches(operation, input, proof) || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 {
		return clients.ComputeClaimRecoveryProof{}, errWorkspaceComputeClaimProof
	}
	if proofErr != nil || !workspaceComputeClaimProofEligible(operation, input, proof, false) {
		return proof, errWorkspaceComputeClaimProof
	}
	return proof, nil
}

func (app *controlPlaneServer) claimWorkspaceCompute(ctx context.Context, service *controlplane.Service, input workspaceComputeClaimRecoveryRequest, key string) (clients.ComputeClaimRecoveryProof, error) {
	operation, ok, err := app.workspaceLaunchOperation(ctx, input.LaunchOperationID)
	if err != nil || !ok {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return clients.ComputeClaimRecoveryProof{}, err
	}
	unlock := app.lockResource("workspace-launch", operation.AccountID)
	defer unlock()
	operation, ok, err = app.workspaceLaunchOperation(ctx, input.LaunchOperationID)
	if err != nil || !ok {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return clients.ComputeClaimRecoveryProof{}, err
	}
	requestHash := workspaceComputeClaimRequestHash(input, key)
	if operation.ComputeClaimProof != nil {
		if app.bindWorkspaceComputeClaimApproval(ctx, &operation, input, key, false) != nil || operation.ComputeClaimRequestHash != requestHash || operation.ComputeClaimApprovalID != input.ApprovalID ||
			operation.ComputeClaimMergedMainSHA != input.MergedMainSHA || operation.ComputeClaimCloudDigest != input.CloudImageDigest ||
			operation.ComputeClaimPrivateIP != input.PrivateIP || operation.ComputeClaimProof.PrivateIP != input.PrivateIP ||
			!workspaceComputeClaimRecoveryRequestMatches(operation, input) || !workspaceComputeClaimProofEligible(operation, input, *operation.ComputeClaimProof, true) {
			return clients.ComputeClaimRecoveryProof{}, errWorkspaceComputeClaimIdentity
		}
		if operation.Phase == "compute_claim_pending" {
			if err := app.continueWorkspaceComputeClaimReadback(ctx, service, &operation); err != nil {
				return *operation.ComputeClaimProof, err
			}
		}
		return *operation.ComputeClaimProof, nil
	}
	operation, err = app.loadWorkspaceComputeClaimOperation(ctx, input.LaunchOperationID, input, true)
	if err != nil {
		return clients.ComputeClaimRecoveryProof{}, err
	}
	legacyCandidate := workspaceComputeClaimLegacyCandidate(operation)
	if !legacyCandidate {
		if err := app.bindWorkspaceComputeClaimApproval(ctx, &operation, input, key, true); err != nil {
			return clients.ComputeClaimRecoveryProof{}, err
		}
	}

	proof, proofErr := service.ComputeClaimRecoveryProof(ctx, workspaceComputeClaimRecoveryInput(operation, input))
	if !workspaceComputeClaimProofBaseMatches(operation, input, proof) || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 {
		proof = workspaceComputeClaimFailureProof(operation, "local_identity")
		proofErr = errWorkspaceComputeClaimProof
	}
	if proofErr != nil || !workspaceComputeClaimProofEligible(operation, input, proof, false) {
		if !workspaceComputeClaimSafeFailureForOperation(operation, input, proof) {
			proof = workspaceComputeClaimFailureProof(operation, "provider_describe")
		}
		operation.Status, operation.ErrorCode = "manual_review", "workspace_compute_claim_"+proof.Reason
		releaseWorkspaceLaunchLease(&operation)
		if err := app.persistWorkspaceLaunch(ctx, &operation); err != nil {
			return clients.ComputeClaimRecoveryProof{}, err
		}
		return proof, errWorkspaceComputeClaimProof
	}
	if legacyCandidate {
		if !persistWorkspaceComputeClaimIdentityFromProof(&operation, proof) {
			return clients.ComputeClaimRecoveryProof{}, errWorkspaceComputeClaimIdentity
		}
		operation.Status, operation.Phase, operation.ErrorCode = "compute_claim_pending", "compute_claim_pending", ""
		releaseWorkspaceLaunchLease(&operation)
		if err := app.persistWorkspaceLaunch(ctx, &operation); err != nil {
			return clients.ComputeClaimRecoveryProof{}, err
		}
		operation, err = app.loadWorkspaceComputeClaimOperation(ctx, input.LaunchOperationID, input, false)
		if err != nil || operation.Status != "compute_claim_pending" || operation.Phase != "compute_claim_pending" {
			if err == nil {
				err = errWorkspaceComputeClaimNotPending
			}
			return clients.ComputeClaimRecoveryProof{}, err
		}
	}
	if legacyCandidate {
		if err := app.bindWorkspaceComputeClaimApproval(ctx, &operation, input, key, true); err != nil {
			return clients.ComputeClaimRecoveryProof{}, err
		}
	}

	claimed, claimErr := service.ClaimComputeRecovery(ctx, clients.ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: workspaceComputeClaimRecoveryInput(operation, input), MachineName: operation.ComputeMachineName,
		NodeName: operation.ComputeNodeName, CVMInstanceID: operation.ComputeCVMInstanceID, PrivateIP: operation.ComputePrivateIP,
		InstanceType: operation.ComputeInstanceType, Zone: operation.ComputeZone,
	}, operation.ID+":compute")
	if claimErr != nil || !workspaceComputeClaimProofEligible(operation, input, claimed, true) {
		if !workspaceComputeClaimSafeFailureForOperation(operation, input, claimed) {
			claimed = workspaceComputeClaimFailureProof(operation, "identity_mismatch")
		}
		operation.Status, operation.ErrorCode = "manual_review", "workspace_compute_claim_"+claimed.Reason
		releaseWorkspaceLaunchLease(&operation)
		if err := app.persistWorkspaceLaunch(ctx, &operation); err != nil {
			return clients.ComputeClaimRecoveryProof{}, err
		}
		return claimed, errWorkspaceComputeClaimProof
	}

	operation.Status, operation.Phase, operation.ErrorCode = "compute_claim_pending", "compute_claim_pending", ""
	operation.ComputeClaimRequestHash, operation.ComputeClaimApprovalID = requestHash, input.ApprovalID
	operation.ComputeClaimMergedMainSHA, operation.ComputeClaimCloudDigest = input.MergedMainSHA, input.CloudImageDigest
	operation.ComputeClaimPrivateIP = input.PrivateIP
	operation.ComputeClaimProof = &claimed
	releaseWorkspaceLaunchLease(&operation)
	if err := app.persistWorkspaceLaunch(ctx, &operation); err != nil {
		return clients.ComputeClaimRecoveryProof{}, err
	}
	if err := app.continueWorkspaceComputeClaimReadback(ctx, service, &operation); err != nil {
		return claimed, err
	}
	return claimed, nil
}

func (app *controlPlaneServer) continueWorkspaceComputeClaimReadback(ctx context.Context, service *controlplane.Service, operation *workspaceLaunchOperation) error {
	readback, err := service.ReadMonthlyCompute(ctx, operation.ComputeID)
	if err != nil {
		return app.blockWorkspaceComputeClaimReadback(ctx, operation, "workspace_compute_claim_readback_unavailable", err)
	}
	if !workspaceComputeClaimReadbackMatches(*operation, readback) {
		return app.blockWorkspaceComputeClaimReadback(ctx, operation, "workspace_compute_claim_readback_identity_mismatch", errWorkspaceComputeClaimIdentity)
	}
	existing, ok := app.getCompute(operation.ComputeID)
	if !ok || !workspaceLaunchResourceIdentityMatches("compute", existing, *operation) || stringValue(existing["operationId"]) != operation.ID+":compute" {
		return app.blockWorkspaceComputeClaimReadback(ctx, operation, "workspace_compute_claim_readback_identity_mismatch", errWorkspaceComputeClaimIdentity)
	}
	if err := app.tables.SaveCompute(ctx, mergeMaps(existing, structToMap(readback))); err != nil {
		return app.blockWorkspaceComputeClaimReadback(ctx, operation, "workspace_compute_claim_readback_persist_failed", err)
	}
	operation.Status, operation.Phase, operation.ErrorCode = "preparing", "storage_fulfilling", ""
	releaseWorkspaceLaunchLease(operation)
	return app.persistWorkspaceLaunch(ctx, operation)
}

func workspaceComputeClaimReadbackMatches(operation workspaceLaunchOperation, readback clients.ComputeAllocation) bool {
	proof := operation.ComputeClaimProof
	cvmID := firstNonEmpty(readback.CVMInstanceID, readback.InstanceID)
	deadline, deadlineErr := time.Parse(time.RFC3339, readback.Deadline)
	providerData := readback.ProviderData
	costTags := readback.CostTags
	return proof != nil && (readback.Status == "ready" || readback.Status == "running") && readback.Provider == "tencent-tke" &&
		readback.ID == operation.ComputeID && readback.AccountID == operation.AccountID && readback.WorkspaceID == operation.WorkspaceID && readback.PackageID == operation.PackageID &&
		(readback.OperationID == "" || readback.OperationID == operation.ID+":compute") && readback.ProviderResourceID == operation.ComputeCVMInstanceID && readback.ProviderRequestID != "" &&
		readback.PoolID == operation.ComputePoolID && readback.NodePoolID == operation.ComputeNodePoolID && readback.MachineName == operation.ComputeMachineName &&
		readback.NodeName == operation.ComputeNodeName && cvmID == operation.ComputeCVMInstanceID &&
		(readback.InstanceID == "" || readback.InstanceID == operation.ComputeCVMInstanceID) && (readback.CVMInstanceID == "" || readback.CVMInstanceID == operation.ComputeCVMInstanceID) &&
		readback.PrivateIP == operation.ComputePrivateIP && readback.InstanceType == operation.ComputeInstanceType && readback.Zone == operation.ComputeZone &&
		readback.ChargeType == "PREPAID" && readback.ChargeType == operation.ComputeChargeType && readback.RenewFlag == "NOTIFY_AND_MANUAL_RENEW" &&
		readback.RenewFlag == operation.ComputeRenewFlag && readback.Deadline == operation.ComputeDeadline && deadlineErr == nil && !deadline.IsZero() &&
		providerData["instanceType"] == operation.ComputeInstanceType && providerData["zone"] == operation.ComputeZone && providerData["chargeType"] == "PREPAID" &&
		providerData["renewFlag"] == "NOTIFY_AND_MANUAL_RENEW" && providerData["deadline"] == operation.ComputeDeadline && providerData["machineName"] == operation.ComputeMachineName &&
		costTags["opl_account_id"] == operation.AccountID && costTags["opl_workspace_id"] == operation.WorkspaceID && costTags["opl_resource_id"] == operation.ComputeID && costTags["opl_operation_id"] != "" &&
		proof.ComputeAllocationID == readback.ID && proof.AccountID == readback.AccountID && proof.WorkspaceID == readback.WorkspaceID && proof.PackageID == readback.PackageID &&
		proof.PoolID == readback.PoolID && proof.NodePoolID == readback.NodePoolID && proof.MachineName == readback.MachineName && proof.NodeName == readback.NodeName &&
		proof.CVMInstanceID == cvmID && proof.PrivateIP == readback.PrivateIP && proof.InstanceType == readback.InstanceType && proof.Zone == readback.Zone &&
		proof.ChargeType == readback.ChargeType && proof.PeriodMonths == 1 && proof.RenewFlag == readback.RenewFlag && proof.Deadline == readback.Deadline &&
		monthlyPurchaseReadbackConfirmed("compute", workspaceLaunchProviderExpectation(operation, "compute"), structToMap(readback))
}

func (app *controlPlaneServer) blockWorkspaceComputeClaimReadback(ctx context.Context, operation *workspaceLaunchOperation, code string, cause error) error {
	operation.Status, operation.Phase, operation.ErrorCode = "manual_review", "compute_claim_pending", code
	releaseWorkspaceLaunchLease(operation)
	if cause == nil {
		cause = errors.New(code)
	}
	return errors.Join(cause, app.persistWorkspaceLaunch(ctx, operation))
}

func workspaceComputeClaimFailureProof(operation workspaceLaunchOperation, reason string) clients.ComputeClaimRecoveryProof {
	return clients.ComputeClaimRecoveryProof{
		SchemaVersion: 1, Reason: reason, StorageState: "unknown", LaunchOperationID: operation.ID, AccountID: operation.AccountID,
		WorkspaceID: operation.WorkspaceID, ComputeAllocationID: operation.ComputeID, StorageVolumeID: operation.StorageID,
		PackageID: operation.PackageID, PoolID: operation.ComputePoolID, NodePoolID: operation.ComputeNodePoolID,
		Evidence: &clients.ComputeClaimEvidence{},
	}
}

func workspaceLaunchReadbackUnknownStage(operation workspaceLaunchOperation) (string, bool) {
	stage := ""
	for _, candidate := range workspaceLaunchContinuationStages {
		if operation.ContinuationAttemptBudgets[candidate].Unknown == 0 {
			continue
		}
		if stage != "" {
			return "", true
		}
		stage = candidate
	}
	return stage, stage != ""
}

func (app *controlPlaneServer) workspaceLaunchReadbackRecoveryCustomer(ctx context.Context, operation workspaceLaunchOperation) (workspaceLaunchReadbackRecoveryCustomer, error) {
	account, accountFound, accountErr := app.tables.GetAccount(ctx, operation.AccountID)
	owner, ownerFound, ownerErr := app.tables.GetUser(ctx, operation.OwnerUserID)
	if accountErr != nil || ownerErr != nil || !accountFound || !ownerFound || !ownsActiveAccount(account, owner) ||
		stringValue(owner["id"]) != operation.OwnerUserID || stringValue(owner["role"]) != "owner" || normalizeEmail(stringValue(owner["email"])) == "" {
		return workspaceLaunchReadbackRecoveryCustomer{}, errBillingReviewIdentity
	}
	if workspace, ok := app.getWorkspace(operation.WorkspaceID); ok && !workspaceMatchesLaunch(workspace, operation) {
		return workspaceLaunchReadbackRecoveryCustomer{}, errBillingReviewIdentity
	}
	return workspaceLaunchReadbackRecoveryCustomer{
		Email: normalizeEmail(stringValue(owner["email"])), AccountID: operation.AccountID, OwnerUserID: operation.OwnerUserID,
	}, nil
}

func (app *controlPlaneServer) workspaceLaunchReadbackRecoveryProviderTruth(ctx context.Context, service *controlplane.Service, operation workspaceLaunchOperation) (clients.MonthlyProviderTruth, error) {
	truth, err := service.MonthlyProviderTruth(ctx, operation.ComputeID, operation.StorageID)
	if err != nil {
		return clients.MonthlyProviderTruth{}, errBillingReviewProviderFact
	}
	computeState := workspaceLaunchRecoveryResourceStateReadOnly(operation, "compute", truth.ComputeState, structToMap(truth.Compute))
	storageState := workspaceLaunchRecoveryResourceStateReadOnly(operation, "storage", truth.StorageState, structToMap(truth.Storage))
	if computeState != "ready" || storageState != "ready" {
		return clients.MonthlyProviderTruth{}, errBillingReviewProviderFact
	}
	return truth, nil
}

func (app *controlPlaneServer) readWorkspaceLaunchUnknownStage(ctx context.Context, service *controlplane.Service, operation *workspaceLaunchOperation, stage string, operations []clients.FabricOperation) (*clients.StorageAttachment, error) {
	switch stage {
	case "storage":
		return nil, nil
	case "attachment":
		attachment, err := workspaceLaunchAttachmentFromFabricOperations(operations, *operation)
		if err != nil {
			return nil, err
		}
		operation.AttachmentID = attachment.ID
		return &attachment, nil
	case "secret":
		secret, err := workspaceLaunchSecretFromFabricOperations(operations, *operation)
		if err != nil {
			return nil, err
		}
		operation.GatewaySecretRef, operation.WorkspaceKeyStatus, operation.WorkspaceKeyFingerprint = secret.SecretRef, "configured", secret.Fingerprint
		return nil, nil
	case "runtime":
		workspace, err := app.readWorkspaceLaunchRuntime(ctx, service, *operation)
		if err != nil || !workspaceProjectionMatchesLaunch(workspace, *operation) || !workspaceRuntimeAttemptMatches(workspace, *operation) {
			return nil, controlplane.ErrWorkspaceRuntimeReadbackInvalid
		}
		operation.RuntimeID, operation.RuntimeReady = workspace.RuntimeID, workspace.RuntimeReady
		operation.RuntimeServiceName, operation.RuntimeUsername = workspace.RuntimeServiceName, workspace.RuntimeUsername
		operation.CredentialStatus, operation.CredentialVersion, operation.CredentialSecretRef = workspace.CredentialStatus, workspace.CredentialVersion, workspace.CredentialSecretRef
		operation.URL = workspace.URL
		return nil, nil
	case "activation":
		if err := app.verifyWorkspaceLaunchActivationTruth(ctx, service, operation); err != nil {
			return nil, err
		}
		workspace, ok := app.getWorkspace(operation.WorkspaceID)
		billingState, reviewCode := app.workspaceLaunchBillingState(ctx, *operation)
		if !ok || reviewCode != "" || !workspaceMatchesLaunch(workspace, *operation) || !workspaceBillingStateMatchesLaunch(workspace, billingState) || stringValue(workspace["runtimeId"]) != operation.RuntimeID {
			return nil, errors.New("workspace_launch_activation_readback_invalid")
		}
		return nil, nil
	case "receipt":
		input, err := app.workspaceLaunchPurchaseReceiptReadbackInput(ctx, *operation)
		if err != nil {
			return nil, err
		}
		receipt, found, err := workspaceLaunchPurchaseReceiptFromLedger(ctx, service, input)
		if err != nil || !found || receipt.ReceiptID == "" {
			return nil, errors.Join(err, errors.New("workspace_launch_receipt_readback_invalid"))
		}
		operation.ReceiptID = receipt.ReceiptID
		return nil, nil
	default:
		return nil, errInvalidBillingReview
	}
}

func (app *controlPlaneServer) workspaceLaunchReadbackRecoveryProofForOperation(ctx context.Context, service *controlplane.Service, operation workspaceLaunchOperation, expectedBindings ...string) (workspaceLaunchOperation, workspaceLaunchReadbackRecoveryProof, error) {
	stage, hasUnknown := workspaceLaunchReadbackUnknownStage(operation)
	budget := operation.ContinuationAttemptBudgets[stage]
	if !hasUnknown || stage == "" || operation.Status != "manual_review" || operation.Phase != workspaceLaunchReadbackRecoveryPhase(stage) ||
		budget != (workspaceLaunchStageBudget{Attempted: 1, Unknown: 1, Max: workspaceLaunchStageMax}) ||
		!validWorkspaceImageIdentity(operation.WorkspaceImageDigest) {
		return operation, workspaceLaunchReadbackRecoveryProof{}, errInvalidBillingReview
	}
	customer, err := app.workspaceLaunchReadbackRecoveryCustomer(ctx, operation)
	if err != nil {
		return operation, workspaceLaunchReadbackRecoveryProof{}, err
	}
	userID, err := app.sub2APIUserID(ctx, operation.AccountID)
	if err != nil || !workspaceLaunchChargeConfirmed(operation, userID) {
		return operation, workspaceLaunchReadbackRecoveryProof{}, errBillingReviewChargeFact
	}
	truth, err := app.workspaceLaunchReadbackRecoveryProviderTruth(ctx, service, operation)
	if err != nil {
		return operation, workspaceLaunchReadbackRecoveryProof{}, err
	}
	operations, err := service.FabricOperations(ctx)
	if err != nil {
		return operation, workspaceLaunchReadbackRecoveryProof{}, errBillingReviewProviderFact
	}
	ownership, err := service.MachineOwnership(ctx, operation.ComputeID)
	if err != nil {
		return operation, workspaceLaunchReadbackRecoveryProof{}, errBillingReviewProviderFact
	}
	expectedBinding := ""
	if len(expectedBindings) > 0 {
		expectedBinding = expectedBindings[0]
	}
	specializedStage := stage == "attachment" || stage == "secret" || stage == "runtime"
	stageBinding := ""
	if specializedStage {
		if stage == "attachment" {
			operation.AttachmentID = firstNonEmpty(operation.AttachmentID, workspaceLaunchStorageAttachmentID(operation))
		}
		candidate, candidateErr := workspaceLaunchStageReadbackOperation(operations, operation, stage)
		if candidateErr != nil || candidate.Status == "succeeded" && expectedBinding == "" ||
			(candidate.Status != "started" && candidate.Status != "failed" && candidate.Status != "succeeded") {
			return operation, workspaceLaunchReadbackRecoveryProof{}, errBillingReviewProviderFact
		}
		identity := workspaceLaunchReadbackOperationIdentity(candidate, "", "")
		input, inputErr := workspaceLaunchStageReadbackInput(operation, stage, identity, expectedBinding)
		if inputErr != nil {
			return operation, workspaceLaunchReadbackRecoveryProof{}, inputErr
		}
		stageProof, proofErr := service.WorkspaceLaunchStageReadbackProof(ctx, input)
		if proofErr != nil || stageProof.Stage != stage || stageProof.PriorStatus != candidate.Status ||
			expectedBinding != "" && stageProof.BindingDigest != expectedBinding {
			return operation, workspaceLaunchReadbackRecoveryProof{}, errBillingReviewProviderFact
		}
		operations, err = workspaceLaunchOperationsWithStageProof(operations, candidate, stageProof)
		if err != nil {
			return operation, workspaceLaunchReadbackRecoveryProof{}, err
		}
		stageBinding = stageProof.BindingDigest
	} else if _, err := app.readWorkspaceLaunchUnknownStage(ctx, service, &operation, stage, operations); err != nil {
		return operation, workspaceLaunchReadbackRecoveryProof{}, errBillingReviewProviderFact
	}
	authority, err := workspaceLaunchReadbackRecoveryAuthorityForOperation(operation, stage, truth, ownership, operations)
	if err != nil {
		return operation, workspaceLaunchReadbackRecoveryProof{}, err
	}
	if specializedStage {
		if !setWorkspaceLaunchReadbackStageBinding(&authority.OperationIDs, stage, stageBinding) || applyWorkspaceLaunchStageReadback(&operation, stage, authority) != nil {
			return operation, workspaceLaunchReadbackRecoveryProof{}, errBillingReviewIdentity
		}
	}
	target, err := workspaceLaunchReadbackRecoveryExpectedTarget(operation, truth)
	if err != nil {
		return operation, workspaceLaunchReadbackRecoveryProof{}, err
	}
	proof := workspaceLaunchReadbackRecoveryProof{
		SchemaVersion: 1, Eligible: true, Reason: "none", Stage: stage, Customer: customer,
		Target: target, Resources: workspaceLaunchReadbackRecoveryExpectedResources(operation, truth, authority),
		OperationIDs: authority.OperationIDs, WorkspaceImageDigest: operation.WorkspaceImageDigest,
		AttemptBudget: budget, AllowedWrites: workspaceLaunchReadbackRecoveryAllowedWrites(stage),
		ForbiddenWrites: append([]string(nil), workspaceLaunchReadbackRecoveryForbiddenWrites...),
	}
	return operation, proof, nil
}

func (app *controlPlaneServer) diagnoseWorkspaceLaunchReadbackRecovery(ctx context.Context, service *controlplane.Service, operationID string) (workspaceLaunchReadbackRecoveryProof, error) {
	operation, ok, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return workspaceLaunchReadbackRecoveryProof{}, err
	}
	unlock := app.lockResource("workspace-launch", operation.AccountID)
	defer unlock()
	operation, ok, err = app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return workspaceLaunchReadbackRecoveryProof{}, err
	}
	_, proof, err := app.workspaceLaunchReadbackRecoveryProofForOperation(ctx, service, operation)
	return proof, err
}

func (app *controlPlaneServer) convergeWorkspaceLaunchUnknownStage(ctx context.Context, service *controlplane.Service, operation *workspaceLaunchOperation, approval workspaceLaunchReadbackRecoveryApproval) (bool, error) {
	stageIdentity, specializedStage := workspaceLaunchReadbackStageOperationIdentity(approval.OperationIDs, approval.Stage)
	expectedBinding := ""
	if specializedStage {
		expectedBinding = stageIdentity.ReadbackBindingDigest
	}
	recovered, proof, err := app.workspaceLaunchReadbackRecoveryProofForOperation(ctx, service, *operation, expectedBinding)
	if err != nil {
		return false, err
	}
	expected := approval
	expected.Customer, expected.Target, expected.Resources = proof.Customer, proof.Target, proof.Resources
	expected.OperationIDs, expected.WorkspaceImageDigest, expected.Stage = proof.OperationIDs, proof.WorkspaceImageDigest, proof.Stage
	expected.AttemptBudget, expected.AllowedWrites, expected.ForbiddenWrites = proof.AttemptBudget, proof.AllowedWrites, proof.ForbiddenWrites
	if !workspaceLaunchReadbackRecoveryApprovalMatches(approval, expected) || workspaceLaunchReadbackRecoveryApprovalDigest(expected) != approval.ApprovalDigest {
		return false, errBillingReviewIdentity
	}
	if specializedStage {
		input, inputErr := workspaceLaunchStageReadbackInput(recovered, approval.Stage, stageIdentity, expectedBinding)
		if inputErr != nil {
			return false, inputErr
		}
		converged, convergeErr := service.ConvergeWorkspaceLaunchStageReadback(ctx, input)
		if convergeErr != nil || !workspaceLaunchStageConvergenceMatches(input, converged) {
			return false, errBillingReviewProviderFact
		}
	}
	*operation = recovered
	budget := operation.ContinuationAttemptBudgets[approval.Stage]
	budget.Confirmed, budget.Unknown = 1, 0
	operation.ContinuationAttemptBudgets[approval.Stage] = budget
	operation.ReadbackRecoveryApproval = &approval
	operation.ReadbackRecoveryProof = &proof
	operation.Status, operation.ErrorCode = "preparing", ""
	releaseWorkspaceLaunchLease(operation)
	if err := app.persistWorkspaceLaunch(ctx, operation); errors.Is(err, errWorkspaceLaunchCASConflict) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

func (app *controlPlaneServer) recoverWorkspaceLaunchReviewWithReplay(ctx context.Context, service *controlplane.Service, input billingReviewResolutionInput) (map[string]any, bool, error) {
	operation, ok, err := app.workspaceLaunchOperation(ctx, input.ResourceID)
	if err != nil {
		return nil, false, err
	}
	if ok && operation.AccountID == input.AccountID && operation.ReadbackRecoveryApproval != nil && operation.ReadbackRecoveryProof != nil &&
		(operation.Status == "preparing" || operation.Status == "waiting" || terminalWorkspaceLaunchStatus(operation.Status)) {
		if input.ReadbackApproval == nil || input.IdempotencyKey != operation.ReadbackRecoveryApproval.IdempotencyKey ||
			!workspaceLaunchReadbackRecoveryApprovalMatches(*input.ReadbackApproval, *operation.ReadbackRecoveryApproval) {
			return nil, true, errBillingReviewIdentity
		}
		result, responseErr := workspaceLaunchRecoveryResponse(operation)
		return result, true, responseErr
	}
	result, err := app.recoverWorkspaceLaunchReview(ctx, service, input)
	return result, false, err
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
		if input.ReadbackApproval == nil && operation.ReadbackRecoveryApproval == nil && operation.ReadbackRecoveryProof == nil {
			return workspaceLaunchRecoveryResponse(operation)
		}
		if input.ReadbackApproval == nil || operation.ReadbackRecoveryApproval == nil || operation.ReadbackRecoveryProof == nil ||
			!workspaceLaunchReadbackRecoveryApprovalMatches(*input.ReadbackApproval, *operation.ReadbackRecoveryApproval) {
			return nil, errBillingReviewIdentity
		}
		return workspaceLaunchRecoveryResponse(operation)
	}
	if operation.Status != "manual_review" {
		return nil, errBillingReviewNotPending
	}

	unlockAccount := app.lockResource("account", operation.AccountID)
	defer unlockAccount()
	if stage, hasUnknown := workspaceLaunchReadbackUnknownStage(operation); hasUnknown {
		if stage == "" || input.ReadbackApproval == nil || input.ReadbackApproval.Stage != stage {
			return nil, errInvalidBillingReview
		}
		if err := app.validateWorkspaceLaunchReadbackRecoveryApproval(ctx, operation, *input.ReadbackApproval, input.IdempotencyKey); err != nil {
			return nil, err
		}
		won, convergeErr := app.convergeWorkspaceLaunchUnknownStage(ctx, service, &operation, *input.ReadbackApproval)
		if convergeErr != nil {
			return nil, convergeErr
		}
		if won {
			_ = app.fulfillWorkspaceLaunch(ctx, service, &operation)
		}
		current, ok, err := app.workspaceLaunchOperation(ctx, operation.ID)
		if err != nil || !ok {
			if err == nil {
				err = errBillingReviewNotFound
			}
			return nil, err
		}
		if current.ReadbackRecoveryApproval == nil || current.ReadbackRecoveryProof == nil ||
			!workspaceLaunchReadbackRecoveryApprovalMatches(*input.ReadbackApproval, *current.ReadbackRecoveryApproval) {
			return nil, errBillingReviewIdentity
		}
		return workspaceLaunchRecoveryResponse(current)
	}
	if input.ReadbackApproval != nil {
		return nil, errInvalidBillingReview
	}
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
	return operation.ChargeAttempted && (!operation.PostChargeBalanceKnown || operation.PostChargeBalanceUSDMicros >= 0) &&
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
	state = workspaceLaunchRecoveryResourceStateReadOnly(operation, resourceType, state, facts)
	if state == "unknown" {
		return state, nil
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

func workspaceLaunchRecoveryResourceStateReadOnly(operation workspaceLaunchOperation, resourceType, state string, facts map[string]any) string {
	if (state != "ready" && state != "absent") || !workspaceLaunchResourceIdentityMatches(resourceType, facts, operation) ||
		(state == "ready" && !monthlyPurchaseReadbackConfirmed(resourceType, workspaceLaunchProviderExpectation(operation, resourceType), facts)) ||
		(state == "absent" && !monthlyResourceConfirmedAbsent(resourceType, facts)) {
		return "unknown"
	}
	return state
}

func workspaceLaunchReadbackRecoveryOperationPlanMatches(operationIDs workspaceLaunchReadbackRecoveryOperationIDs, operation workspaceLaunchOperation) bool {
	return operationIDs.LaunchOperationID == operation.ID && operationIDs.LaunchRequestHash == operation.RequestHash && operationIDs.MachineOwnershipID != "" &&
		operationIDs.Compute.IdempotencyKey == operation.ID+":compute" && operationIDs.Storage.IdempotencyKey == operation.ID+":storage" &&
		operationIDs.Attachment.IdempotencyKey == operation.AttachmentOperationID &&
		operationIDs.Secret.IdempotencyKey == operation.WorkspaceOperationID+":secret:gateway-secret" &&
		operationIDs.Runtime.IdempotencyKey == operation.WorkspaceOperationID+":runtime" &&
		operationIDs.ActivationOperationID == operation.ID+":activation" && operationIDs.ReceiptOperationID == operation.ID+":purchase-receipt"
}

func workspaceLaunchReadbackRecoveryStageBindingMatches(operationIDs workspaceLaunchReadbackRecoveryOperationIDs, stage string) bool {
	identity, specialized := workspaceLaunchReadbackStageOperationIdentity(operationIDs, stage)
	return !specialized || computeClaimApprovalDigestPattern.MatchString(identity.ReadbackBindingDigest)
}

func (app *controlPlaneServer) validateWorkspaceLaunchReadbackRecoveryApproval(ctx context.Context, operation workspaceLaunchOperation, approval workspaceLaunchReadbackRecoveryApproval, key string) error {
	expiresAt, expiresErr := time.Parse(time.RFC3339, approval.ExpiresAt)
	customer, customerErr := app.workspaceLaunchReadbackRecoveryCustomer(ctx, operation)
	if approval.SchemaVersion != 1 || approval.IdempotencyKey != key || approval.Confirmation != workspaceLaunchReadbackRecoveryConfirmation ||
		approval.ApprovalDigest == "" || workspaceLaunchReadbackRecoveryApprovalDigest(approval) != approval.ApprovalDigest || expiresErr != nil || !expiresAt.After(time.Now().UTC()) ||
		!computeClaimMergedSHAPattern.MatchString(approval.MergedMainSHA) || !computeClaimCloudDigestPattern.MatchString(approval.CloudImageDigest) ||
		approval.WorkspaceImageDigest != operation.WorkspaceImageDigest || approval.Stage == "" || customerErr != nil || approval.Customer != customer ||
		approval.Target.LaunchOperationID != operation.ID || approval.Target.AccountID != operation.AccountID || approval.Target.WorkspaceID != operation.WorkspaceID ||
		approval.Target.ComputeAllocationID != operation.ComputeID || approval.Target.StorageID != operation.StorageID || approval.Target.PackageID != operation.PackageID ||
		approval.Target.NodePoolID != operation.ComputeNodePoolID || approval.Target.StorageGB != operation.StorageGB || approval.Target.AutoRenew != operation.AutoRenew ||
		approval.Target.PriceVersion != operation.PriceVersion || approval.Target.TotalChargeUSDMicros != operation.TotalChargeUSDMicros ||
		approval.Target.PeriodStart != operation.PeriodStart || approval.Target.PaidThrough != operation.PaidThrough || approval.Target.BillingAnchorDay != operation.BillingAnchorDay ||
		approval.Resources.ComputeAllocationID != operation.ComputeID ||
		approval.Resources.StorageVolumeID != operation.StorageID || approval.Resources.GatewaySecretRef != workspaceGatewaySecretReference(operation.WorkspaceID) ||
		approval.Resources.StorageSizeGB != operation.StorageGB || approval.Resources.WorkspaceAPIKeyID != operation.WorkspaceAPIKeyID ||
		!workspaceLaunchReadbackRecoveryOperationPlanMatches(approval.OperationIDs, operation) || !workspaceLaunchReadbackRecoveryStageBindingMatches(approval.OperationIDs, approval.Stage) ||
		approval.AttemptBudget != operation.ContinuationAttemptBudgets[approval.Stage] ||
		!equalWorkspaceComputeClaimStrings(approval.AllowedWrites, workspaceLaunchReadbackRecoveryAllowedWrites(approval.Stage)) ||
		!equalWorkspaceComputeClaimStrings(approval.ForbiddenWrites, workspaceLaunchReadbackRecoveryForbiddenWrites) {
		return errBillingReviewIdentity
	}
	return nil
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
	if operation.ReadbackRecoveryProof != nil {
		result["readbackRecoveryProof"] = operation.ReadbackRecoveryProof
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

func (app *controlPlaneServer) reserveWorkspaceLaunchStageAttempt(ctx context.Context, operation *workspaceLaunchOperation, stage string) error {
	budget, ok := operation.ContinuationAttemptBudgets[stage]
	if !ok || budget.Max <= 0 {
		return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_"+stage+"_budget_invalid")
	}
	if budget.Unknown > 0 || budget.Attempted >= budget.Max {
		return app.unknownWorkspaceLaunchStageAttempt(ctx, operation, stage, nil)
	}
	budget.Attempted++
	operation.ContinuationAttemptBudgets[stage] = budget
	return app.persistWorkspaceLaunch(ctx, operation)
}

func (app *controlPlaneServer) confirmWorkspaceLaunchStageAttempt(ctx context.Context, operation *workspaceLaunchOperation, stage string) error {
	budget, ok := operation.ContinuationAttemptBudgets[stage]
	if !ok || budget.Unknown != 0 || budget.Attempted <= budget.Confirmed || budget.Confirmed >= budget.Max {
		return app.unknownWorkspaceLaunchStageAttempt(ctx, operation, stage, nil)
	}
	budget.Confirmed++
	operation.ContinuationAttemptBudgets[stage] = budget
	return app.persistWorkspaceLaunch(ctx, operation)
}

func (app *controlPlaneServer) observeWorkspaceLaunchStageAttempt(ctx context.Context, operation *workspaceLaunchOperation, stage string) error {
	budget, ok := operation.ContinuationAttemptBudgets[stage]
	if !ok || budget.Max != workspaceLaunchStageMax || budget.Attempted != 0 || budget.Confirmed != 0 || budget.Unknown != 0 {
		return app.unknownWorkspaceLaunchStageAttempt(ctx, operation, stage, nil)
	}
	budget.Attempted, budget.Confirmed = 1, 1
	operation.ContinuationAttemptBudgets[stage] = budget
	return app.persistWorkspaceLaunch(ctx, operation)
}

func (app *controlPlaneServer) unknownWorkspaceLaunchStageAttempt(ctx context.Context, operation *workspaceLaunchOperation, stage string, cause error) error {
	budget, ok := operation.ContinuationAttemptBudgets[stage]
	if ok && budget.Attempted > budget.Confirmed {
		budget.Unknown = budget.Attempted - budget.Confirmed
		operation.ContinuationAttemptBudgets[stage] = budget
	}
	code := "workspace_launch_" + stage + "_attempt_unknown"
	if cause == nil {
		cause = errors.New(code)
	}
	return errors.Join(cause, app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, code))
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
	key, err := service.WorkspaceKeyByIDForConvergence(ctx, userID, operation.WorkspaceAPIKeyID, workspaceReservedKeyName(operation.WorkspaceID))
	if err != nil || key.ID != operation.WorkspaceAPIKeyID || key.UserID != userID || key.Name != workspaceReservedKeyName(operation.WorkspaceID) || key.Status != "active" {
		return app.retryWorkspaceLaunchDebit(ctx, operation, "gateway_key_unavailable", err)
	}
	if operation.ChargeConfirmation == nil {
		var charge clients.Sub2APICharge
		if operation.ChargeAttempted || operation.Status == "unknown" {
			history, historyErr := service.FinancialBalanceHistoryByCodes(ctx, userID, []string{operation.RedeemCode})
			if historyErr != nil {
				return app.retryWorkspaceLaunchDebit(ctx, operation, "sub2api_charge_history_unavailable", historyErr)
			}
			row := map[string]any{"sub2apiRedeemCode": operation.RedeemCode, "chargeUsdMicros": operation.TotalChargeUSDMicros}
			switch code := sub2APIReconciliationCode(row, userID, history); {
			case code == "sub2api_charge_missing":
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
	history, historyErr := service.FinancialBalanceHistoryByCodes(ctx, userID, []string{operation.RedeemCode})
	row := map[string]any{"sub2apiRedeemCode": operation.RedeemCode, "chargeUsdMicros": operation.TotalChargeUSDMicros}
	if historyErr != nil || sub2APIReconciliationCode(row, userID, history) == "sub2api_charge_missing" {
		return app.retryWorkspaceLaunchDebit(ctx, operation, "sub2api_charge_history_unavailable", errors.Join(historyErr, clients.ErrSub2APIChargeUnknown))
	}
	if code := sub2APIReconciliationCode(row, userID, history); code != "" {
		return app.manualReviewWorkspaceLaunchDebit(ctx, operation, code)
	}
	if operation.BillingPeriodState == "pending" {
		chargeHistory := history[operation.RedeemCode]
		if chargeHistory.UsedAt == nil || chargeHistory.UsedAt.IsZero() {
			return app.manualReviewWorkspaceLaunchDebit(ctx, operation, "workspace_launch_billing_period_invalid")
		}
		periodStart := chargeHistory.UsedAt.UTC()
		operation.PeriodStart = periodStart.Format(time.RFC3339Nano)
		operation.BillingAnchorDay = periodStart.Day()
		operation.PaidThrough = nextBillingMonth(periodStart, operation.BillingAnchorDay).Format(time.RFC3339Nano)
		operation.BillingPeriodState = "frozen"
		if err := app.persistWorkspaceLaunch(ctx, operation); err != nil {
			return err
		}
	} else if operation.BillingPeriodState != "frozen" {
		return app.manualReviewWorkspaceLaunchDebit(ctx, operation, "workspace_launch_billing_period_invalid")
	}
	postCharge, err := service.Sub2APIBalance(ctx, userID)
	if err == nil {
		operation.PostChargeBalanceKnown, operation.PostChargeBalanceUSDMicros = true, postCharge.USDMicros
		if postCharge.USDMicros < 0 {
			return app.manualReviewWorkspaceLaunchDebit(ctx, operation, "post_charge_balance_invalid")
		}
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
		history, historyErr := service.FinancialBalanceHistoryByCodes(ctx, userID, []string{operation.RefundCode})
		if historyErr != nil {
			return app.retryWorkspaceLaunchFulfillment(ctx, operation, "sub2api_refund_history_unavailable", historyErr)
		}
		entry, found := history[operation.RefundCode]
		if !found {
			refund, err = service.RefundSub2API(ctx, clients.Sub2APIRefundInput{
				UserID: userID, Code: operation.RefundCode, RefundUSDMicros: operation.TotalChargeUSDMicros, Notes: "OPL Workspace launch refund " + operation.WorkspaceID,
			})
		} else {
			if entry.Type != "balance" || entry.Status != "used" || entry.UsedBy == nil || *entry.UsedBy != userID || entry.ValueUSDMicros != operation.TotalChargeUSDMicros {
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

func workspaceLaunchPurchaseReceiptInput(operation workspaceLaunchOperation, userID int64, components map[string]any) clients.ReceiptInput {
	return clients.ReceiptInput{
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
	}
}

func (app *controlPlaneServer) workspaceLaunchPurchaseReceiptReadbackInput(ctx context.Context, operation workspaceLaunchOperation) (clients.ReceiptInput, error) {
	workspace, ok := app.getWorkspace(operation.WorkspaceID)
	if !ok || !workspaceMatchesLaunch(workspace, operation) || stringValue(workspace["runtimeId"]) != operation.RuntimeID {
		return clients.ReceiptInput{}, errors.New("workspace_launch_projection_unavailable")
	}
	userID, err := app.sub2APIUserID(ctx, operation.AccountID)
	if err != nil {
		return clients.ReceiptInput{}, err
	}
	components, _, _, err := workspaceLaunchComponents(operation)
	if err != nil {
		return clients.ReceiptInput{}, err
	}
	return workspaceLaunchPurchaseReceiptInput(operation, userID, components), nil
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
	input := workspaceLaunchPurchaseReceiptInput(*operation, userID, components)
	budget := operation.ContinuationAttemptBudgets["receipt"]
	receipt, found, err := workspaceLaunchPurchaseReceiptFromLedger(ctx, service, input)
	if err != nil {
		return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_receipt_readback_invalid")
	}
	if found {
		if budget.Confirmed == 0 {
			if budget.Attempted == 0 {
				if err := app.observeWorkspaceLaunchStageAttempt(ctx, operation, "receipt"); err != nil {
					return err
				}
			} else if err := app.confirmWorkspaceLaunchStageAttempt(ctx, operation, "receipt"); err != nil {
				return err
			}
		}
	} else {
		if budget.Confirmed > 0 || budget.Attempted > 0 {
			return app.unknownWorkspaceLaunchStageAttempt(ctx, operation, "receipt", nil)
		}
		if err := app.reserveWorkspaceLaunchStageAttempt(ctx, operation, "receipt"); err != nil {
			return err
		}
		receipt, err = service.RecordMonthlyReceipt(ctx, input, operation.ID+":purchase-receipt")
		if err != nil || receipt.ReceiptID == "" || !workspaceLaunchReceiptInputMatches(receipt.ReceiptInput, input) {
			if reconciled, reconciledFound, readErr := workspaceLaunchPurchaseReceiptFromLedger(ctx, service, input); readErr == nil && reconciledFound {
				receipt, err = reconciled, nil
			}
		}
		if err != nil || receipt.ReceiptID == "" || !workspaceLaunchReceiptInputMatches(receipt.ReceiptInput, input) {
			return app.unknownWorkspaceLaunchStageAttempt(ctx, operation, "receipt", err)
		}
		if err := app.confirmWorkspaceLaunchStageAttempt(ctx, operation, "receipt"); err != nil {
			return err
		}
	}
	if receipt.ReceiptID == "" || !workspaceLaunchReceiptInputMatches(receipt.ReceiptInput, input) {
		return app.manualReviewWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_receipt_readback_invalid")
	}
	workspace["purchaseReceiptId"] = receipt.ReceiptID
	if err := app.tables.SaveWorkspace(ctx, workspace); err != nil {
		return app.retryWorkspaceLaunchFulfillment(ctx, operation, "workspace_launch_receipt_projection_retryable", err)
	}
	operation.ReceiptID, operation.Status, operation.Phase, operation.ErrorCode = receipt.ReceiptID, "succeeded", "succeeded", ""
	releaseWorkspaceLaunchLease(operation)
	return app.persistWorkspaceLaunch(ctx, operation)
}

func workspaceLaunchPurchaseReceiptFromLedger(ctx context.Context, service *controlplane.Service, input clients.ReceiptInput) (clients.Receipt, bool, error) {
	receipts, err := reconciliationLedgerReceipts(ctx, service, input.AccountID)
	if err != nil {
		return clients.Receipt{}, false, err
	}
	matches := make([]clients.Receipt, 0, 1)
	for _, receipt := range receipts {
		if receipt.RequestID != input.RequestID {
			continue
		}
		if !workspaceLaunchReceiptInputMatches(receipt.ReceiptInput, input) || receipt.ReceiptID == "" {
			return clients.Receipt{}, false, errors.New("workspace_launch_receipt_identity_mismatch")
		}
		matches = append(matches, receipt)
	}
	if len(matches) > 1 {
		return clients.Receipt{}, false, errors.New("workspace_launch_receipt_multiple_candidate")
	}
	if len(matches) == 0 {
		return clients.Receipt{}, false, nil
	}
	return matches[0], true, nil
}

func workspaceLaunchReceiptInputMatches(actual, expected clients.ReceiptInput) bool {
	actualJSON, actualErr := json.Marshal(actual)
	expectedJSON, expectedErr := json.Marshal(expected)
	return actualErr == nil && expectedErr == nil && bytes.Equal(actualJSON, expectedJSON)
}

func (app *controlPlaneServer) readWorkspaceLaunchRuntime(ctx context.Context, service *controlplane.Service, operation workspaceLaunchOperation) (domain.WorkspaceProjection, error) {
	runtime, err := service.WorkspaceRuntimeStatus(ctx, operation.WorkspaceID)
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}
	if runtime.WorkspaceID != operation.WorkspaceID || runtime.OperationID != operation.WorkspaceOperationID+":runtime" || runtime.ID == "" || runtime.ServiceName == "" || runtime.URL == "" ||
		runtime.Access.Username == "" || runtime.Access.CredentialStatus != "configured" || runtime.Access.CredentialVersion == "" || runtime.Access.SecretRef == "" ||
		(runtime.Status != "running" && runtime.Status != "unready") || runtime.Ready && runtime.Status != "running" {
		return domain.WorkspaceProjection{}, controlplane.ErrWorkspaceRuntimeReadbackInvalid
	}
	return domain.WorkspaceProjection{
		ID: operation.WorkspaceID, AccountID: operation.AccountID, OwnerID: operation.OwnerUserID, Name: operation.Name, PackageID: operation.PackageID,
		Provider: "tencent-tke", URL: runtime.URL, Status: runtime.Status, ComputeID: operation.ComputeID,
		VolumeID: operation.StorageID, AttachmentID: operation.AttachmentID, RuntimeID: runtime.ID, RuntimeServiceName: runtime.ServiceName,
		WorkspaceAPIKeyID: operation.WorkspaceAPIKeyID, RuntimeReady: runtime.Ready, RuntimeUsername: runtime.Access.Username,
		CredentialStatus: runtime.Access.CredentialStatus, CredentialVersion: runtime.Access.CredentialVersion, CredentialSecretRef: runtime.Access.SecretRef,
	}, nil
}

func (app *controlPlaneServer) workspaceLaunchAttachment(operation workspaceLaunchOperation) (map[string]any, bool) {
	for _, attachment := range app.listAttachments(operation.AccountID) {
		if stringValue(attachment["operationId"]) == operation.AttachmentOperationID && attachmentMatchesLaunch(attachment, operation) {
			return attachment, true
		}
	}
	return nil, false
}

// workspaceLaunchAttachmentFromFabricOperation reconciles an interrupted
// attachment write from Fabric's durable, redacted operation record. It never
// retries the provider call.
func (app *controlPlaneServer) workspaceLaunchAttachmentFromFabricOperation(ctx context.Context, service *controlplane.Service, operation workspaceLaunchOperation) (clients.StorageAttachment, error) {
	operations, err := service.FabricOperations(ctx)
	if err != nil {
		return clients.StorageAttachment{}, err
	}
	return workspaceLaunchAttachmentFromFabricOperations(operations, operation)
}

func workspaceLaunchAttachmentFromFabricOperations(operations []clients.FabricOperation, operation workspaceLaunchOperation) (clients.StorageAttachment, error) {
	var matches []clients.StorageAttachment
	expectedAttachmentID := workspaceLaunchStorageAttachmentID(operation)
	expectedFabricOperationID := workspaceLaunchStorageAttachmentFabricOperationID(operation)
	expectedRequestHash := workspaceLaunchStorageAttachmentRequestHash(operation)
	for _, candidate := range operations {
		if candidate.Action != "create_storage_attachment" ||
			(candidate.IdempotencyKey != operation.AttachmentOperationID && candidate.ResourceID != expectedAttachmentID) {
			continue
		}
		if candidate.ResourceKind != "storage_attachment" || candidate.Status != "succeeded" || candidate.ResourceID != expectedAttachmentID ||
			candidate.OperationID != expectedFabricOperationID || candidate.IdempotencyKey != operation.AttachmentOperationID || candidate.RequestHash != expectedRequestHash ||
			candidate.AccountID != operation.AccountID || candidate.WorkspaceID != operation.WorkspaceID {
			return clients.StorageAttachment{}, errors.New("workspace_launch_attachment_readback_invalid")
		}
		var attachment clients.StorageAttachment
		resource, ok := candidate.RedactedProviderPayload["resource"]
		if !ok || jsonRoundTrip(resource, &attachment) != nil || attachment.ID != expectedAttachmentID {
			return clients.StorageAttachment{}, errors.New("workspace_launch_attachment_readback_invalid")
		}
		if attachment.OperationID == "" {
			attachment.OperationID = operation.AttachmentOperationID
		}
		if attachment.OperationID != operation.AttachmentOperationID || attachment.WorkspaceID != operation.WorkspaceID ||
			attachment.ComputeID != operation.ComputeID || attachment.VolumeID != operation.StorageID || attachment.Status != "attached" ||
			operation.AttachmentID != "" && operation.AttachmentID != attachment.ID {
			return clients.StorageAttachment{}, errors.New("workspace_launch_attachment_readback_invalid")
		}
		providerOperationID, tagsOK := workspaceLaunchReadbackProviderOperationID(
			attachment.CostTags, operation.AccountID, operation.WorkspaceID, attachment.ID,
		)
		if !tagsOK || providerOperationID != operation.AttachmentOperationID {
			return clients.StorageAttachment{}, errors.New("workspace_launch_attachment_readback_invalid")
		}
		matches = append(matches, attachment)
	}
	if len(matches) != 1 {
		return clients.StorageAttachment{}, errors.New("workspace_launch_attachment_readback_invalid")
	}
	return matches[0], nil
}

func (app *controlPlaneServer) saveWorkspaceLaunchAttachment(attachment clients.StorageAttachment, operation workspaceLaunchOperation) error {
	if attachment.ID == "" || attachment.OperationID != operation.AttachmentOperationID || attachment.WorkspaceID != operation.WorkspaceID ||
		attachment.ComputeID != operation.ComputeID || attachment.VolumeID != operation.StorageID || attachment.Status != "attached" {
		return errors.New("workspace_launch_attachment_identity_mismatch")
	}
	row := structToMap(attachment)
	row["accountId"], row["ownerAccountId"], row["ownerUserId"] = operation.AccountID, operation.AccountID, operation.OwnerUserID
	row["operationId"] = attachment.OperationID
	row["computeAllocationId"], row["storageId"] = attachment.ComputeID, attachment.VolumeID
	row["mountPath"] = "/data"
	return app.tables.SaveAttachment(context.Background(), row)
}

// workspaceLaunchSecretFromFabricOperation reconciles a completed Gateway
// Secret write from its durable Fabric operation. Secret material is never
// present in the operation payload; only the reference and fingerprint are
// accepted.
func (app *controlPlaneServer) workspaceLaunchSecretFromFabricOperation(ctx context.Context, service *controlplane.Service, operation workspaceLaunchOperation) (clients.GatewaySecretWriteResult, error) {
	operations, err := service.FabricOperations(ctx)
	if err != nil {
		return clients.GatewaySecretWriteResult{}, err
	}
	return workspaceLaunchSecretFromFabricOperations(operations, operation)
}

func workspaceLaunchSecretFromFabricOperations(operations []clients.FabricOperation, operation workspaceLaunchOperation) (clients.GatewaySecretWriteResult, error) {
	wantKey := operation.WorkspaceOperationID + ":secret:gateway-secret"
	var matches []clients.GatewaySecretWriteResult
	for _, candidate := range operations {
		if candidate.Action != "upsert_gateway_secret" || candidate.ResourceKind != "gateway_secret" || candidate.Status != "succeeded" ||
			candidate.IdempotencyKey != wantKey || candidate.AccountID != operation.AccountID || candidate.WorkspaceID != operation.WorkspaceID {
			continue
		}
		var secret clients.GatewaySecretWriteResult
		resource, ok := candidate.RedactedProviderPayload["resource"]
		if !ok || jsonRoundTrip(resource, &secret) != nil || secret.SecretRef == "" || secret.Version == "" || secret.Fingerprint == "" {
			continue
		}
		if secret.SecretRef != workspaceGatewaySecretReference(operation.WorkspaceID) || secret.Fingerprint != operation.WorkspaceKeyFingerprint && operation.WorkspaceKeyFingerprint != "" {
			continue
		}
		matches = append(matches, secret)
	}
	if len(matches) != 1 {
		return clients.GatewaySecretWriteResult{}, errors.New("workspace_launch_secret_readback_invalid")
	}
	return matches[0], nil
}

func jsonRoundTrip(value any, target any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func workspaceGatewaySecretReference(workspaceID string) string {
	return "opl-gateway-" + workspaceComputeClaimStableSuffix(workspaceID)[:16]
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
