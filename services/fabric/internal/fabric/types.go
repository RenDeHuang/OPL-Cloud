package fabric

import (
	"errors"
	"time"
)

var ErrJobNotFound = errors.New("job_not_found")
var ErrJobIdempotencyConflict = errors.New("job_idempotency_conflict")
var ErrInvalidJobInput = errors.New("invalid_job_input")
var ErrJobStateConflict = errors.New("job_state_conflict")
var ErrJobLeaseMismatch = errors.New("job_lease_mismatch")
var ErrMachineOwnershipConflict = errors.New("machine_ownership_conflict")
var ErrMachineOwnershipNotFound = errors.New("machine_ownership_not_found")
var ErrUnsupportedComputePackage = errors.New("unsupported_compute_package")
var ErrInvalidStorageSize = errors.New("invalid_storage_size")
var ErrInvalidMonthlyPreflight = errors.New("invalid_monthly_preflight")
var ErrMonthlyPreflightUnavailable = errors.New("monthly_preflight_unavailable")
var ErrInvalidMonthlyProviderTruth = errors.New("invalid_monthly_provider_truth")
var ErrMonthlyProviderTruthUnavailable = errors.New("monthly_provider_truth_unavailable")
var ErrInvalidComputeClaimRecovery = errors.New("invalid_compute_claim_recovery")
var ErrComputeClaimRecoveryUnavailable = errors.New("compute_claim_recovery_unavailable")
var ErrComputeClaimRecoveryIdempotencyConflict = errors.New("compute_claim_recovery_idempotency_conflict")
var ErrInvalidWorkspaceActivationTruth = errors.New("invalid_workspace_activation_truth")
var ErrWorkspaceActivationTruthUnavailable = errors.New("workspace_activation_truth_unavailable")
var ErrRuntimeHealthSummaryUnavailable = errors.New("runtime_health_summary_unavailable")
var ErrComputeIdempotencyConflict = errors.New("compute_idempotency_conflict")
var ErrComputeOperationFailed = errors.New("compute_operation_failed")
var ErrComputeAllocationPending = errors.New("compute_allocation_pending")
var ErrRuntimeIdempotencyConflict = errors.New("runtime_idempotency_conflict")
var ErrRuntimeOperationInProgress = errors.New("runtime_operation_in_progress")
var ErrRuntimeOperationFailed = errors.New("runtime_operation_failed")
var ErrRuntimeOperationNotCurrent = errors.New("runtime_operation_not_current")
var ErrStorageAttachmentIdempotencyConflict = errors.New("storage_attachment_idempotency_conflict")
var ErrStorageAttachmentOperationInProgress = errors.New("storage_attachment_operation_in_progress")
var ErrStorageAttachmentOperationFailed = errors.New("storage_attachment_operation_failed")
var ErrGatewaySecretIdempotencyConflict = errors.New("gateway_secret_idempotency_conflict")

type Catalog struct {
	SchemaVersion     int                `json:"schemaVersion"`
	Owner             string             `json:"owner"`
	WorkspacePackages []WorkspacePackage `json:"workspacePackages"`
	StorageClasses    []StorageClass     `json:"storageClasses"`
	IngressDomains    []IngressDomain    `json:"ingressDomains"`
}

type MonthlyPreflightInput struct {
	ResourceType string `json:"resourceType"`
	PackageID    string `json:"packageId"`
	SizeGB       int    `json:"sizeGb,omitempty"`
	Zone         string `json:"zone"`
}

type MonthlyPreflight struct {
	ResourceType       string            `json:"resourceType"`
	PackageID          string            `json:"packageId"`
	NodePoolID         string            `json:"nodePoolId,omitempty"`
	SizeGB             int               `json:"sizeGb,omitempty"`
	Zone               string            `json:"zone"`
	Available          bool              `json:"available"`
	ChargeType         string            `json:"chargeType"`
	PeriodMonths       int               `json:"periodMonths"`
	RenewFlag          string            `json:"renewFlag"`
	ProviderPriceCNY   float64           `json:"providerPriceCny"`
	ProviderRequestIDs map[string]string `json:"providerRequestIds"`
}

type MonthlyPreflightReportInput struct {
	Zone string `json:"zone"`
}

type MonthlyPreflightStage struct {
	Stage      string         `json:"stage"`
	Status     string         `json:"status"`
	ErrorCode  string         `json:"errorCode,omitempty"`
	BlockedBy  []string       `json:"blockedBy"`
	DurationMS int64          `json:"durationMs"`
	SafeFacts  map[string]any `json:"safeFacts"`
}

type MonthlyPreflightReport struct {
	SchemaVersion           int                             `json:"schemaVersion"`
	Status                  string                          `json:"status"`
	Zone                    string                          `json:"zone"`
	Items                   []MonthlyPreflightStage         `json:"items"`
	Packages                []MonthlyPreflightPackageReport `json:"packages"`
	Sub2APIMutationCount    int                             `json:"sub2apiMutationCount"`
	TencentMutationCount    int                             `json:"tencentMutationCount"`
	KubernetesMutationCount int                             `json:"kubernetesMutationCount"`
}

type MonthlyPreflightPackageReport struct {
	PackageID string                  `json:"packageId"`
	SizeGB    int                     `json:"sizeGb"`
	Status    string                  `json:"status"`
	Items     []MonthlyPreflightStage `json:"items"`
}

type MonthlyProviderTruth struct {
	ComputeState      string            `json:"computeState"`
	StorageState      string            `json:"storageState"`
	Compute           ComputeAllocation `json:"compute"`
	Storage           StorageVolume     `json:"storage"`
	ProviderRequestID string            `json:"providerRequestId,omitempty"`
	ErrorCode         string            `json:"errorCode,omitempty"`
}

type ComputeClaimRecoveryInput struct {
	LaunchOperationID   string `json:"launchOperationId"`
	AccountID           string `json:"accountId"`
	WorkspaceID         string `json:"workspaceId"`
	ComputeAllocationID string `json:"computeAllocationId"`
	StorageVolumeID     string `json:"storageVolumeId"`
	PackageID           string `json:"packageId"`
	PoolID              string `json:"poolId"`
	NodePoolID          string `json:"nodePoolId"`
}

type ComputeClaimRecoveryClaimInput struct {
	ComputeClaimRecoveryInput
	MachineName    string `json:"machineName"`
	NodeName       string `json:"nodeName"`
	CVMInstanceID  string `json:"cvmInstanceId"`
	PrivateIP      string `json:"privateIp"`
	InstanceType   string `json:"instanceType"`
	Zone           string `json:"zone"`
	IdempotencyKey string `json:"-"`
}

type ComputeClaimProviderProof struct {
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	NodeOwnershipState string `json:"nodeOwnershipState"`
	CVMOwnershipState  string `json:"cvmOwnershipState"`
	MachineName        string `json:"machineName"`
	NodeName           string `json:"nodeName"`
	CVMInstanceID      string `json:"cvmInstanceId"`
	PrivateIP          string `json:"privateIp"`
	InstanceType       string `json:"instanceType"`
	Zone               string `json:"zone"`
	ChargeType         string `json:"chargeType"`
	PeriodMonths       int    `json:"periodMonths"`
	RenewFlag          string `json:"renewFlag"`
	Deadline           string `json:"deadline"`
}

type ComputeClaimProviderClaim struct {
	Proof                   ComputeClaimProviderProof `json:"proof"`
	TencentMutationCount    int                       `json:"tencentMutationCount"`
	KubernetesMutationCount int                       `json:"kubernetesMutationCount"`
	FailureStage            string                    `json:"failureStage,omitempty"`
	ProviderErrorClass      string                    `json:"providerErrorClass,omitempty"`
	Evidence                *ComputeClaimEvidence     `json:"evidence,omitempty"`
}

type StorageRecoveryDiscovery struct {
	State              string `json:"state"`
	ProviderResourceID string `json:"providerResourceId,omitempty"`
	ProviderRequestID  string `json:"providerRequestId,omitempty"`
	Reason             string `json:"reason,omitempty"`
	MutationCount      int    `json:"mutationCount"`
}

// ComputeClaimMutationEvidence separates calls attempted from mutations that
// were confirmed by an authoritative readback. Unknown is deliberately kept
// distinct from zero so callers cannot mistake an unavailable read for proof.
type ComputeClaimMutationEvidence struct {
	Attempted int      `json:"attempted"`
	Confirmed int      `json:"confirmed"`
	Unknown   int      `json:"unknown"`
	Missing   []string `json:"missing,omitempty"`
}

type ComputeClaimEvidence struct {
	CVM  ComputeClaimMutationEvidence `json:"cvm"`
	Node ComputeClaimMutationEvidence `json:"node"`
}

type ComputeClaimIdentityCheck struct {
	Field          string `json:"field"`
	Matches        bool   `json:"matches"`
	Expected       string `json:"expected,omitempty"`
	Actual         string `json:"actual,omitempty"`
	ExpectedDigest string `json:"expectedDigest,omitempty"`
	ActualDigest   string `json:"actualDigest,omitempty"`
}

type ComputeClaimIdentityEvidence struct {
	Checks                []ComputeClaimIdentityCheck `json:"checks"`
	MutationLedger        string                      `json:"mutationLedger"`
	MutationLedgerOutcome string                      `json:"mutationLedgerOutcome"`
	MutationLedgerDigest  string                      `json:"mutationLedgerDigest"`
}

type ComputeClaimRecoveryProof struct {
	SchemaVersion             int                           `json:"schemaVersion"`
	Eligible                  bool                          `json:"eligible"`
	Reason                    string                        `json:"reason"`
	StorageState              string                        `json:"storageState"`
	StorageProviderResourceID string                        `json:"storageProviderResourceId,omitempty"`
	LaunchOperationID         string                        `json:"launchOperationId"`
	AccountID                 string                        `json:"accountId"`
	WorkspaceID               string                        `json:"workspaceId"`
	ComputeAllocationID       string                        `json:"computeAllocationId"`
	StorageVolumeID           string                        `json:"storageVolumeId"`
	PackageID                 string                        `json:"packageId"`
	PoolID                    string                        `json:"poolId"`
	NodePoolID                string                        `json:"nodePoolId"`
	MachineName               string                        `json:"machineName,omitempty"`
	NodeName                  string                        `json:"nodeName,omitempty"`
	CVMInstanceID             string                        `json:"cvmInstanceId,omitempty"`
	PrivateIP                 string                        `json:"privateIp,omitempty"`
	InstanceType              string                        `json:"instanceType,omitempty"`
	Zone                      string                        `json:"zone,omitempty"`
	ChargeType                string                        `json:"chargeType,omitempty"`
	PeriodMonths              int                           `json:"periodMonths,omitempty"`
	RenewFlag                 string                        `json:"renewFlag,omitempty"`
	Deadline                  string                        `json:"deadline,omitempty"`
	NodeOwnershipState        string                        `json:"nodeOwnershipState,omitempty"`
	CVMOwnershipState         string                        `json:"cvmOwnershipState,omitempty"`
	Sub2APIMutationCount      int                           `json:"sub2apiMutationCount"`
	TencentMutationCount      int                           `json:"tencentMutationCount"`
	KubernetesMutationCount   int                           `json:"kubernetesMutationCount"`
	FailureStage              string                        `json:"failureStage,omitempty"`
	ProviderErrorClass        string                        `json:"providerErrorClass,omitempty"`
	Evidence                  *ComputeClaimEvidence         `json:"evidence,omitempty"`
	IdentityEvidence          *ComputeClaimIdentityEvidence `json:"identityEvidence,omitempty"`
}

type WorkspacePackage struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ComputeProfileID string `json:"computeProfileId"`
	CPU              int    `json:"cpu"`
	MemoryGB         int    `json:"memoryGb"`
	DiskGB           int    `json:"diskGb"`
	Provider         string `json:"provider"`
	Available        bool   `json:"available"`
}

type StorageClass struct {
	ID               string `json:"id"`
	StorageClassName string `json:"storageClassName"`
	Provider         string `json:"provider"`
	Available        bool   `json:"available"`
}

type IngressDomain struct {
	ID          string `json:"id"`
	Host        string `json:"host"`
	PathPattern string `json:"pathPattern"`
	Available   bool   `json:"available"`
}

type ComputeAllocationInput struct {
	ID             string `json:"id,omitempty"`
	AccountID      string `json:"accountId"`
	WorkspaceID    string `json:"workspaceId"`
	PackageID      string `json:"packageId"`
	NodePoolID     string `json:"nodePoolId,omitempty"`
	IdempotencyKey string `json:"-"`
	OperationID    string `json:"-"`
	DryRun         bool   `json:"dryRun,omitempty"`
}

type ComputeAllocation struct {
	ID                 string            `json:"id"`
	OperationID        string            `json:"operationId,omitempty"`
	AccountID          string            `json:"accountId"`
	WorkspaceID        string            `json:"workspaceId"`
	PackageID          string            `json:"packageId"`
	Status             string            `json:"status"`
	Provider           string            `json:"provider"`
	ProviderResourceID string            `json:"providerResourceId,omitempty"`
	ProviderRequestID  string            `json:"providerRequestId"`
	PoolID             string            `json:"poolId,omitempty"`
	NodePoolID         string            `json:"nodePoolId,omitempty"`
	InstanceID         string            `json:"instanceId,omitempty"`
	CVMInstanceID      string            `json:"cvmInstanceId,omitempty"`
	NodeName           string            `json:"nodeName,omitempty"`
	MachineName        string            `json:"machineName,omitempty"`
	PrivateIP          string            `json:"privateIp,omitempty"`
	PublicIP           string            `json:"publicIp,omitempty"`
	InstanceType       string            `json:"instanceType,omitempty"`
	Zone               string            `json:"zone,omitempty"`
	CVMStatus          string            `json:"cvmStatus,omitempty"`
	ChargeType         string            `json:"chargeType,omitempty"`
	RenewFlag          string            `json:"renewFlag,omitempty"`
	Deadline           string            `json:"deadline,omitempty"`
	ServiceName        string            `json:"serviceName,omitempty"`
	NodeSelector       map[string]any    `json:"nodeSelector,omitempty"`
	ProviderData       map[string]string `json:"providerData,omitempty"`
	CostTags           map[string]string `json:"costTags,omitempty"`
	CreatedAt          time.Time         `json:"createdAt"`
}

type MachineOwnership struct {
	ID                string     `json:"id"`
	ResourceID        string     `json:"resourceId"`
	AccountID         string     `json:"accountId"`
	WorkspaceID       string     `json:"workspaceId,omitempty"`
	PackageID         string     `json:"packageId"`
	NodePoolID        string     `json:"nodePoolId"`
	MachineID         string     `json:"machineId"`
	InstanceID        string     `json:"instanceId,omitempty"`
	NodeName          string     `json:"nodeName,omitempty"`
	Status            string     `json:"status"`
	ProviderRequestID string     `json:"providerRequestId,omitempty"`
	ClaimedAt         time.Time  `json:"claimedAt"`
	ReleasedAt        *time.Time `json:"releasedAt,omitempty"`
}

type ProviderMachine struct {
	MachineID    string `json:"machineId"`
	InstanceID   string `json:"instanceId,omitempty"`
	NodeName     string `json:"nodeName,omitempty"`
	PrivateIP    string `json:"privateIp,omitempty"`
	PublicIP     string `json:"publicIp,omitempty"`
	InstanceType string `json:"instanceType,omitempty"`
	Zone         string `json:"zone,omitempty"`
	ChargeType   string `json:"chargeType,omitempty"`
	RenewFlag    string `json:"renewFlag,omitempty"`
	Deadline     string `json:"deadline,omitempty"`
	Ready        bool   `json:"ready"`
}

type ComputeAllocationPreparation struct {
	PoolID             string   `json:"poolId"`
	PackageID          string   `json:"packageId"`
	NodePoolID         string   `json:"nodePoolId"`
	InstanceType       string   `json:"instanceType"`
	MaxReplicas        int64    `json:"maxReplicas"`
	BaselineReplicas   int64    `json:"baselineReplicas"`
	TargetReplicas     int64    `json:"targetReplicas"`
	BeforeMachineNames []string `json:"beforeMachineNames"`
	ProviderRequestID  string   `json:"providerRequestId,omitempty"`
}

type ComputeAllocationExecution struct {
	Allocation ComputeAllocation            `json:"allocation"`
	Plan       ComputeAllocationPreparation `json:"plan"`
	DryRun     bool                         `json:"dryRun,omitempty"`
}

type StorageVolumeInput struct {
	ID                         string `json:"id,omitempty"`
	AccountID                  string `json:"accountId"`
	WorkspaceID                string `json:"workspaceId"`
	ComputeID                  string `json:"computeId"`
	Zone                       string `json:"zone"`
	SizeGB                     int    `json:"sizeGb"`
	ExpectedRecoveryState      string `json:"expectedRecoveryState,omitempty"`
	ExpectedProviderResourceID string `json:"expectedProviderResourceId,omitempty"`
	IdempotencyKey             string `json:"-"`
	OperationID                string `json:"-"`
	AllowExistingExactReplay   bool   `json:"-"`
}

type StorageVolume struct {
	ID                 string            `json:"id"`
	OperationID        string            `json:"operationId,omitempty"`
	AccountID          string            `json:"accountId,omitempty"`
	WorkspaceID        string            `json:"workspaceId"`
	Status             string            `json:"status"`
	Provider           string            `json:"provider,omitempty"`
	ProviderResourceID string            `json:"providerResourceId,omitempty"`
	ProviderRequestID  string            `json:"providerRequestId"`
	SizeGB             int               `json:"sizeGb,omitempty"`
	StorageClass       string            `json:"storageClass,omitempty"`
	CBSStatus          string            `json:"cbsStatus,omitempty"`
	DiskType           string            `json:"diskType,omitempty"`
	RenewFlag          string            `json:"renewFlag,omitempty"`
	Deadline           string            `json:"deadline,omitempty"`
	Zone               string            `json:"zone,omitempty"`
	ProviderData       map[string]string `json:"providerData,omitempty"`
	CostTags           map[string]string `json:"costTags,omitempty"`
	CreatedAt          time.Time         `json:"createdAt"`
}

type StorageSnapshotInput struct {
	AccountID      string `json:"accountId"`
	WorkspaceID    string `json:"workspaceId"`
	VolumeID       string `json:"volumeId"`
	IdempotencyKey string `json:"-"`
	OperationID    string `json:"-"`
}

type StorageRestoreInput struct {
	SnapshotID     string `json:"snapshotId"`
	AccountID      string `json:"accountId"`
	WorkspaceID    string `json:"workspaceId"`
	TargetVolumeID string `json:"targetVolumeId"`
	IdempotencyKey string `json:"-"`
	OperationID    string `json:"-"`
}

type StorageSnapshot struct {
	ID                  string    `json:"id"`
	AccountID           string    `json:"accountId"`
	WorkspaceID         string    `json:"workspaceId"`
	VolumeID            string    `json:"volumeId"`
	Status              string    `json:"status"`
	Provider            string    `json:"provider"`
	ProviderSnapshotRef string    `json:"providerSnapshotRef"`
	ProviderRequestID   string    `json:"providerRequestId"`
	SnapshotClass       string    `json:"snapshotClass,omitempty"`
	SizeGB              int       `json:"sizeGb"`
	CreatedAt           time.Time `json:"createdAt"`
}

type StorageAttachmentInput struct {
	WorkspaceID    string `json:"workspaceId"`
	ComputeID      string `json:"computeId"`
	VolumeID       string `json:"volumeId"`
	IdempotencyKey string `json:"-"`
	OperationID    string `json:"-"`
}

type StorageAttachment struct {
	ID                   string            `json:"id"`
	OperationID          string            `json:"operationId"`
	WorkspaceID          string            `json:"workspaceId"`
	ComputeID            string            `json:"computeId,omitempty"`
	VolumeID             string            `json:"volumeId"`
	Status               string            `json:"status"`
	Provider             string            `json:"provider,omitempty"`
	ProviderAttachmentID string            `json:"providerAttachmentId,omitempty"`
	ProviderRequestID    string            `json:"providerRequestId"`
	CostTags             map[string]string `json:"costTags,omitempty"`
	CreatedAt            time.Time         `json:"createdAt"`
}

type WorkspaceRuntimeInput struct {
	WorkspaceID           string `json:"workspaceId"`
	ComputeID             string `json:"computeId"`
	VolumeID              string `json:"volumeId"`
	AttachmentID          string `json:"attachmentId"`
	AttachmentOperationID string `json:"attachmentOperationId"`
	RuntimeOperationID    string `json:"runtimeOperationId"`
	ImageID               string `json:"imageId"`
	GatewaySecretRef      string `json:"gatewaySecretRef"`
	IdempotencyKey        string `json:"-"`
	OperationID           string `json:"-"`
}

type WorkspaceRuntime struct {
	ID                string            `json:"id"`
	OperationID       string            `json:"operationId,omitempty"`
	WorkspaceID       string            `json:"workspaceId"`
	URL               string            `json:"url"`
	Status            string            `json:"status"`
	ServiceName       string            `json:"serviceName,omitempty"`
	ImageID           string            `json:"imageId,omitempty"`
	ProviderRequestID string            `json:"providerRequestId"`
	Access            RuntimeAccess     `json:"access,omitempty"`
	Ready             bool              `json:"ready,omitempty"`
	Checks            []Check           `json:"checks,omitempty"`
	CostTags          map[string]string `json:"costTags,omitempty"`
	CreatedAt         time.Time         `json:"createdAt"`
}

type WorkspaceActivationTruthInput struct {
	LaunchOperationID        string `json:"launchOperationId"`
	AccountID                string `json:"accountId"`
	WorkspaceID              string `json:"workspaceId"`
	ComputeAllocationID      string `json:"computeAllocationId"`
	ComputeOperationID       string `json:"computeOperationId"`
	StorageVolumeID          string `json:"storageVolumeId"`
	StorageOperationID       string `json:"storageOperationId"`
	AttachmentID             string `json:"attachmentId"`
	AttachmentOperationID    string `json:"attachmentOperationId"`
	RuntimeID                string `json:"runtimeId"`
	RuntimeOperationID       string `json:"runtimeOperationId"`
	ServiceName              string `json:"serviceName"`
	WorkspaceImageDigest     string `json:"workspaceImageDigest"`
	GatewaySecretRef         string `json:"gatewaySecretRef"`
	WorkspaceAPIKeyID        int64  `json:"workspaceApiKeyId"`
	GatewaySecretFingerprint string `json:"gatewaySecretFingerprint"`
}

type WorkspaceActivationRuntimeTruth struct {
	ID                   string   `json:"id"`
	OperationID          string   `json:"operationId"`
	ServiceName          string   `json:"serviceName"`
	DeploymentName       string   `json:"deploymentName"`
	RuntimeSecretRef     string   `json:"runtimeSecretRef"`
	GatewaySecretRef     string   `json:"gatewaySecretRef"`
	PVName               string   `json:"pvName"`
	PVCName              string   `json:"pvcName"`
	VolumeAttachmentName string   `json:"volumeAttachmentName"`
	PodName              string   `json:"podName"`
	PodIP                string   `json:"podIp"`
	NodeName             string   `json:"nodeName"`
	ImageID              string   `json:"imageId"`
	EndpointIPs          []string `json:"endpointIps"`
}

type WorkspaceActivationTruth struct {
	SchemaVersion           int                             `json:"schemaVersion"`
	Ready                   bool                            `json:"ready"`
	Reason                  string                          `json:"reason"`
	ErrorClass              string                          `json:"errorClass,omitempty"`
	ComputeState            string                          `json:"computeState"`
	StorageState            string                          `json:"storageState"`
	Compute                 ComputeAllocation               `json:"compute"`
	Storage                 StorageVolume                   `json:"storage"`
	Attachment              StorageAttachment               `json:"attachment"`
	Runtime                 WorkspaceActivationRuntimeTruth `json:"runtime"`
	Checks                  []Check                         `json:"checks"`
	Sub2APIMutationCount    int                             `json:"sub2apiMutationCount"`
	TencentMutationCount    int                             `json:"tencentMutationCount"`
	KubernetesMutationCount int                             `json:"kubernetesMutationCount"`
}

type RuntimeAccess struct {
	Username          string    `json:"username,omitempty"`
	Password          string    `json:"password,omitempty"`
	CredentialStatus  string    `json:"credentialStatus,omitempty"`
	CredentialVersion string    `json:"credentialVersion,omitempty"`
	SecretRef         string    `json:"secretRef,omitempty"`
	UpdatedAt         time.Time `json:"updatedAt,omitempty"`
}

type GatewaySecretInput struct {
	AccountID         string `json:"accountId"`
	WorkspaceID       string `json:"workspaceId"`
	WorkspaceAPIKeyID int64  `json:"workspaceApiKeyId"`
	Fingerprint       string `json:"fingerprint"`
	GatewayAPIKey     string `json:"gatewayApiKey"`
	IdempotencyKey    string `json:"-"`
}

type GatewaySecret struct {
	SecretRef   string `json:"secretRef"`
	Version     string `json:"version"`
	Fingerprint string `json:"fingerprint"`
}

type WorkspaceRuntimeGatewaySecretInput struct {
	WorkspaceID       string `json:"workspaceId"`
	WorkspaceAPIKeyID int64  `json:"workspaceApiKeyId"`
	SecretRef         string `json:"secretRef"`
	Fingerprint       string `json:"fingerprint"`
	IdempotencyKey    string `json:"-"`
}

type WorkspaceRuntimeGatewaySecretBinding struct {
	WorkspaceID       string `json:"workspaceId"`
	WorkspaceAPIKeyID int64  `json:"workspaceApiKeyId"`
	SecretRef         string `json:"secretRef"`
	Fingerprint       string `json:"fingerprint"`
	Bound             bool   `json:"bound"`
}

type ProviderFactInput struct {
	AccountID    string `json:"accountId"`
	WorkspaceID  string `json:"workspaceId"`
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId"`
}

type ProviderFactsBatchInput struct {
	Items []ProviderFactInput `json:"items"`
}

type ProviderResourceFacts struct {
	PackageOrSpec string `json:"packageOrSpec,omitempty"`
	ProviderID    string `json:"providerId,omitempty"`
	Zone          string `json:"zone,omitempty"`
	Status        string `json:"status,omitempty"`
	CreatedAt     string `json:"createdAt,omitempty"`
	ExpiresAt     string `json:"expiresAt,omitempty"`
	LastReadAt    string `json:"lastReadAt,omitempty"`
}

type ProviderFact struct {
	AccountID    string                `json:"accountId"`
	WorkspaceID  string                `json:"workspaceId"`
	ResourceType string                `json:"resourceType"`
	ResourceID   string                `json:"resourceId"`
	Available    bool                  `json:"available"`
	Facts        ProviderResourceFacts `json:"facts,omitempty"`
	ErrorCode    string                `json:"errorCode,omitempty"`
}

type ProviderFactsBatch struct {
	Items []ProviderFact `json:"items"`
}

type RuntimeHealthSummary struct {
	Total   int `json:"total"`
	Ready   int `json:"ready"`
	Unready int `json:"unready"`
}

type Check struct {
	Name    string         `json:"name"`
	OK      bool           `json:"ok"`
	Details map[string]any `json:"details,omitempty"`
}

type JobInput struct {
	OrganizationID string `json:"organizationId"`
	WorkspaceID    string `json:"workspaceId"`
	ProjectID      string `json:"projectId"`
	TaskID         string `json:"taskId"`
	RequestID      string `json:"requestId"`
	ApprovalID     string `json:"approvalId"`
	EnvironmentRef string `json:"environmentRef,omitempty"`
	IdempotencyKey string `json:"-"`
}

type JobClaimInput struct {
	RunnerID       string `json:"runnerId"`
	IdempotencyKey string `json:"-"`
}

type JobHeartbeatInput struct {
	RunnerID       string `json:"runnerId"`
	LeaseToken     string `json:"leaseToken"`
	IdempotencyKey string `json:"-"`
}

type JobCompleteInput struct {
	RunnerID       string   `json:"runnerId"`
	LeaseToken     string   `json:"leaseToken"`
	ArtifactIDs    []string `json:"artifactIds"`
	ReviewIDs      []string `json:"reviewIds"`
	IdempotencyKey string   `json:"-"`
}

type JobFailInput struct {
	RunnerID       string `json:"runnerId"`
	LeaseToken     string `json:"leaseToken"`
	ErrorCode      string `json:"errorCode"`
	IdempotencyKey string `json:"-"`
}

type Job struct {
	JobID          string     `json:"jobId"`
	OrganizationID string     `json:"organizationId"`
	WorkspaceID    string     `json:"workspaceId"`
	ProjectID      string     `json:"projectId"`
	TaskID         string     `json:"taskId"`
	RequestID      string     `json:"requestId"`
	ApprovalID     string     `json:"approvalId"`
	EnvironmentRef string     `json:"environmentRef,omitempty"`
	Status         string     `json:"status"`
	Attempt        int        `json:"attempt"`
	LeaseOwner     string     `json:"leaseOwner,omitempty"`
	LeaseExpiresAt *time.Time `json:"leaseExpiresAt,omitempty"`
	LeaseToken     string     `json:"leaseToken,omitempty"`
	ArtifactIDs    []string   `json:"artifactIds,omitempty"`
	ReviewIDs      []string   `json:"reviewIds,omitempty"`
	ErrorCode      string     `json:"errorCode,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	Replayed       bool       `json:"replayed,omitempty"`
	leaseTokenHash string
}

type FabricOperation struct {
	ID                      string         `json:"id"`
	OperationID             string         `json:"operationId"`
	CallerService           string         `json:"callerService"`
	Action                  string         `json:"action"`
	ResourceKind            string         `json:"resourceKind"`
	ResourceID              string         `json:"resourceId"`
	AccountID               string         `json:"accountId,omitempty"`
	WorkspaceID             string         `json:"workspaceId,omitempty"`
	Provider                string         `json:"provider,omitempty"`
	ProviderRequestID       string         `json:"providerRequestId,omitempty"`
	IdempotencyKey          string         `json:"idempotencyKey,omitempty"`
	RequestHash             string         `json:"requestHash,omitempty"`
	RedactedProviderPayload map[string]any `json:"redactedProviderPayload,omitempty"`
	Status                  string         `json:"status"`
	ErrorCode               string         `json:"errorCode,omitempty"`
	Retryable               bool           `json:"retryable,omitempty"`
	ComputePoolKey          string         `json:"-"`
	ComputePoolLeaseOwner   string         `json:"-"`
	ComputePoolLeaseExpires *time.Time     `json:"-"`
	StartedAt               time.Time      `json:"startedAt"`
	FinishedAt              time.Time      `json:"finishedAt,omitempty"`
	CreatedAt               time.Time      `json:"createdAt"`
}
