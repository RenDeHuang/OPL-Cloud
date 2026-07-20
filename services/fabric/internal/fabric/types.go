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
var ErrComputeIdempotencyConflict = errors.New("compute_idempotency_conflict")
var ErrComputeOperationFailed = errors.New("compute_operation_failed")
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
	SizeGB             int               `json:"sizeGb,omitempty"`
	Zone               string            `json:"zone"`
	Available          bool              `json:"available"`
	ChargeType         string            `json:"chargeType"`
	PeriodMonths       int               `json:"periodMonths"`
	RenewFlag          string            `json:"renewFlag"`
	ProviderPriceCNY   float64           `json:"providerPriceCny"`
	ProviderRequestIDs map[string]string `json:"providerRequestIds"`
}

type MonthlyProviderTruth struct {
	ComputeState      string            `json:"computeState"`
	StorageState      string            `json:"storageState"`
	Compute           ComputeAllocation `json:"compute"`
	Storage           StorageVolume     `json:"storage"`
	ProviderRequestID string            `json:"providerRequestId,omitempty"`
	ErrorCode         string            `json:"errorCode,omitempty"`
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
	IdempotencyKey string `json:"-"`
	OperationID    string `json:"-"`
	DryRun         bool   `json:"dryRun,omitempty"`
}

type ComputeAllocation struct {
	ID                 string            `json:"id"`
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

type ComputePoolDemand struct {
	PoolID          string `json:"poolId"`
	PackageID       string `json:"packageId"`
	NodePoolID      string `json:"nodePoolId,omitempty"`
	InstanceType    string `json:"instanceType"`
	DesiredReplicas int64  `json:"desiredReplicas"`
	DryRun          bool   `json:"dryRun,omitempty"`
}

type ComputePoolState struct {
	PoolID            string            `json:"poolId"`
	NodePoolID        string            `json:"nodePoolId"`
	DesiredReplicas   int64             `json:"desiredReplicas"`
	CurrentReplicas   int64             `json:"currentReplicas"`
	ProviderRequestID string            `json:"providerRequestId,omitempty"`
	ProviderData      map[string]string `json:"providerData,omitempty"`
	Machines          []ProviderMachine `json:"machines"`
}

type StorageVolumeInput struct {
	ID             string `json:"id,omitempty"`
	AccountID      string `json:"accountId"`
	WorkspaceID    string `json:"workspaceId"`
	ComputeID      string `json:"computeId"`
	Zone           string `json:"zone"`
	SizeGB         int    `json:"sizeGb"`
	IdempotencyKey string `json:"-"`
	OperationID    string `json:"-"`
}

type StorageVolume struct {
	ID                 string            `json:"id"`
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
	WorkspaceID      string `json:"workspaceId"`
	ComputeID        string `json:"computeId"`
	VolumeID         string `json:"volumeId"`
	ImageID          string `json:"imageId"`
	GatewaySecretRef string `json:"gatewaySecretRef"`
	IdempotencyKey   string `json:"-"`
	OperationID      string `json:"-"`
}

type WorkspaceRuntime struct {
	ID                string            `json:"id"`
	WorkspaceID       string            `json:"workspaceId"`
	URL               string            `json:"url"`
	Status            string            `json:"status"`
	ServiceName       string            `json:"serviceName,omitempty"`
	ProviderRequestID string            `json:"providerRequestId"`
	Access            RuntimeAccess     `json:"access,omitempty"`
	Ready             bool              `json:"ready,omitempty"`
	Checks            []Check           `json:"checks,omitempty"`
	CostTags          map[string]string `json:"costTags,omitempty"`
	CreatedAt         time.Time         `json:"createdAt"`
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
	AccountID      string `json:"accountId"`
	GatewayAPIKey  string `json:"gatewayApiKey"`
	IdempotencyKey string `json:"-"`
}

type GatewaySecret struct {
	SecretRef   string `json:"secretRef"`
	Version     string `json:"version"`
	Fingerprint string `json:"fingerprint"`
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
	StartedAt               time.Time      `json:"startedAt"`
	FinishedAt              time.Time      `json:"finishedAt,omitempty"`
	CreatedAt               time.Time      `json:"createdAt"`
}
