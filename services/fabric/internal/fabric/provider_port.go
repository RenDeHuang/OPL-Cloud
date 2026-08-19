package fabric

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
)

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
	ID           string `json:"id"`
	Server       string `json:"server"`
	CPU          int    `json:"cpu"`
	MemoryGB     int    `json:"memoryGb"`
	DiskGB       int    `json:"diskGb"`
	InstanceType string `json:"instanceType"`
}

// WorkspaceLaunchPlanInput is the only plan lookup input Fabric derives from
// the product launch request. Provider adapters own the returned JSON shape.
type WorkspaceLaunchPlanInput struct {
	PackageID string
	SizeGB    int
}

type providerPlanResolver interface {
	ResolveWorkspacePlan(context.Context, WorkspaceLaunchPlanInput) (json.RawMessage, error)
}

var ErrProviderPlanUnavailable = errors.New("provider_plan_unavailable")
var ErrProviderPlanInvalid = errors.New("provider_plan_invalid")

func canonicalProviderPlan(raw json.RawMessage) (json.RawMessage, string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, "", ErrProviderPlanInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil || value == nil {
		return nil, "", ErrProviderPlanInvalid
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, "", ErrProviderPlanInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, "", ErrProviderPlanInvalid
	}
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) == 0 {
		return nil, "", ErrProviderPlanInvalid
	}
	sum := sha256.Sum256(canonical)
	return json.RawMessage(canonical), hex.EncodeToString(sum[:]), nil
}

func canonicalProviderPlanEnvelope(providerProfileRef, packageID string, spec json.RawMessage) (json.RawMessage, string, error) {
	canonicalSpec, _, err := canonicalProviderPlan(spec)
	if err != nil || providerProfileRef == "" || packageID == "" {
		return nil, "", ErrProviderPlanInvalid
	}
	envelope, err := json.Marshal(struct {
		PackageID          string          `json:"packageId"`
		ProviderProfileRef string          `json:"providerProfileRef"`
		SchemaVersion      int             `json:"schemaVersion"`
		Spec               json.RawMessage `json:"spec"`
	}{packageID, providerProfileRef, 1, canonicalSpec})
	if err != nil {
		return nil, "", ErrProviderPlanInvalid
	}
	return canonicalProviderPlan(envelope)
}

func providerPlanDigest(raw json.RawMessage) string {
	_, digest, err := canonicalProviderPlan(raw)
	if err != nil {
		return ""
	}
	return digest
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

type runtimeRepairProvider interface {
	RepairWorkspaceRuntime(context.Context, WorkspaceRuntimeInput, ComputeAllocation, StorageVolume) (WorkspaceRuntime, error)
}

// Provider is the retained live Fabric port.
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
	providerPlanResolver
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
