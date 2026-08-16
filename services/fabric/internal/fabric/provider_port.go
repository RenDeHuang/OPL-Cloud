package fabric

import "context"

// ProviderDescriptor keeps provider selection and portable package facts behind
// the adapter instead of leaking them into Fabric orchestration.
type ProviderDescriptor struct {
	Name                   string
	Catalog                Catalog
	Plans                  map[string]ComputePlan
	DefaultComputePoolIDs  map[string]string
	RequiresMonthlyPricing bool
}

type ComputePlan struct {
	ID           string
	Server       string
	CPU          int
	MemoryGB     int
	DiskGB       int
	InstanceType string
}

// plan remains an internal alias for existing validation helpers.
type plan = ComputePlan

type computeProvider interface {
	PrepareComputeAllocation(context.Context, ComputeAllocationInput) (ComputeAllocationPreparation, error)
	CreateComputeAllocation(context.Context, ComputeAllocationExecution) (ComputeAllocation, error)
	TagComputeMachine(context.Context, ProviderMachine, MachineOwnership) error
	SyncComputeAllocation(context.Context, ComputeAllocation) (ComputeAllocation, error)
	RenewComputeAllocation(context.Context, ComputeAllocation) (ComputeAllocation, error)
	DestroyComputeAllocation(context.Context, ComputeAllocation) (ComputeAllocation, error)
}

type storageProvider interface {
	CreateStorageVolume(context.Context, StorageVolumeInput) (StorageVolume, error)
	SyncStorageVolume(context.Context, StorageVolume) (StorageVolume, error)
	RenewStorageVolume(context.Context, StorageVolume) (StorageVolume, error)
	DestroyStorageVolume(context.Context, StorageVolume) (StorageVolume, error)
	CreateStorageSnapshot(context.Context, StorageSnapshotInput, StorageVolume) (StorageSnapshot, error)
	SyncStorageSnapshot(context.Context, StorageSnapshot) (StorageSnapshot, error)
	RestoreStorageSnapshot(context.Context, StorageRestoreInput, StorageSnapshot) (StorageVolume, error)
	DestroyStorageSnapshot(context.Context, StorageSnapshot) (StorageSnapshot, error)
}

type attachmentProvider interface {
	CreateStorageAttachment(context.Context, StorageAttachmentInput, ComputeAllocation, StorageVolume) (StorageAttachment, error)
	DetachStorageAttachment(context.Context, StorageAttachment) (StorageAttachment, error)
}

type secretProvider interface {
	UpsertGatewaySecret(context.Context, GatewaySecretInput) (GatewaySecret, error)
}

type runtimeProvider interface {
	CreateWorkspaceRuntime(context.Context, WorkspaceRuntimeInput, ComputeAllocation, StorageVolume) (WorkspaceRuntime, error)
	DestroyWorkspaceRuntime(context.Context, string) (WorkspaceRuntime, error)
	WorkspaceRuntimeStatus(context.Context, string) (WorkspaceRuntime, error)
}

// Provider is the retained live Fabric port. Snapshot/restore remains on the
// existing interface until its separate read-only admission is complete.
type Provider interface {
	computeProvider
	storageProvider
	attachmentProvider
	secretProvider
	runtimeProvider
	Descriptor() ProviderDescriptor
	ValidateComputeAllocation(ComputeAllocation, ComputeAllocationPreparation) error
	ValidateWorkspaceImageReference(string) bool
	MonthlyPreflight(context.Context, MonthlyPreflightInput) (MonthlyPreflight, error)
	Readiness(context.Context) (map[string]any, error)
}

type runtimeGatewaySecretProvider interface {
	BindWorkspaceRuntimeGatewaySecret(context.Context, WorkspaceRuntimeGatewaySecretInput) (WorkspaceRuntimeGatewaySecretBinding, error)
	WorkspaceRuntimeGatewaySecret(context.Context, string) (WorkspaceRuntimeGatewaySecretBinding, error)
}

type gatewaySecretReadbackProvider interface {
	ReadGatewaySecret(context.Context, GatewaySecretInput) (GatewaySecret, error)
}

type GatewaySecretReadbackInput struct {
	AccountID         string
	WorkspaceID       string
	WorkspaceAPIKeyID int64
	SecretRef         string
	Fingerprint       string
	KeyDigest         string
}

type providerFactsReader interface {
	ReadComputeProviderFacts(context.Context, ComputeAllocation) (ProviderResourceFacts, error)
	ReadStorageProviderFacts(context.Context, StorageVolume) (ProviderResourceFacts, error)
	ReadStorageAttachmentProviderFacts(context.Context, StorageAttachment, ComputeAllocation, StorageVolume) (ProviderResourceFacts, error)
	WorkspaceRuntimeProviderFacts(WorkspaceRuntime) ProviderResourceFacts
}

type storageAttachmentReadbackProvider interface {
	ReadStorageAttachment(context.Context, StorageAttachment, ComputeAllocation, StorageVolume) (StorageAttachment, error)
}

type storageVolumeStatusReader interface {
	ReadStorageVolumeStatus(context.Context, StorageVolume) (StorageVolume, error)
}

type runtimeHealthSummaryProvider interface {
	RuntimeHealthSummary(context.Context) (RuntimeHealthSummary, error)
}

func providerPlan(provider Provider, packageID string) (ComputePlan, bool) {
	plan, ok := provider.Descriptor().Plans[packageID]
	return plan, ok && plan.ID != "" && plan.InstanceType != ""
}
