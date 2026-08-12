package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type workspaceLaunchStageHashGoldenVector struct {
	Stage   string `json:"stage"`
	Payload struct {
		LaunchRequestHash string                   `json:"launchRequestHash"`
		Action            string                   `json:"action"`
		PackageID         string                   `json:"packageId"`
		SizeGB            int                      `json:"sizeGb"`
		ImageDigest       string                   `json:"imageDigest"`
		Resources         WorkspaceLaunchResources `json:"resources"`
	} `json:"payload"`
	SHA256 string `json:"sha256"`
}

func workspaceLaunchStageHashGoldenVectors(t *testing.T) []workspaceLaunchStageHashGoldenVector {
	t.Helper()
	raw, err := os.ReadFile("../../../../packages/contracts/opl-cloud-fabric-launch-binding-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		StageRequestHash struct {
			GoldenVectors []workspaceLaunchStageHashGoldenVector `json:"goldenVectors"`
		} `json:"stageRequestHash"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	if len(contract.StageRequestHash.GoldenVectors) != 5 {
		t.Fatalf("golden vectors=%d, want 5", len(contract.StageRequestHash.GoldenVectors))
	}
	return contract.StageRequestHash.GoldenVectors
}

type workspaceLaunchRecordingProvider struct {
	testProvider
	ensureCalls int
}

func (p *workspaceLaunchRecordingProvider) EnsureWorkspaceLaunchStage(_ context.Context, request WorkspaceLaunchProviderRequest) (WorkspaceLaunchProviderResult, error) {
	p.ensureCalls++
	return WorkspaceLaunchProviderResult{Resources: request.Input.Resources}, nil
}

func (p *workspaceLaunchRecordingProvider) ReadWorkspaceLaunchStage(_ context.Context, request WorkspaceLaunchProviderRequest) (WorkspaceLaunchProviderResult, error) {
	return WorkspaceLaunchProviderResult{Resources: request.Input.Resources}, nil
}

func workspaceLaunchStageFixture(t *testing.T) (*Service, *MemoryOperationStore, *workspaceLaunchRecordingProvider, WorkspaceLaunchPreflight, string, string) {
	t.Helper()
	store := NewMemoryOperationStore()
	provider := &workspaceLaunchRecordingProvider{}
	service := NewServiceWithOperationStore(provider, store)
	image := "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app@sha256:" + strings.Repeat("a", 64)
	launchHash := strings.Repeat("b", 64)
	preflight, err := service.PreflightWorkspaceLaunch(context.Background(), WorkspaceLaunchPreflightInput{
		SchemaVersion: 1, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: image, RequestHash: launchHash,
	})
	if err != nil || !preflight.Available || preflight.BindingRef == "" {
		t.Fatalf("preflight=%#v err=%v", preflight, err)
	}
	return service, store, provider, preflight, image, launchHash
}

func TestWorkspaceLaunchAdaptersImplementSameProviderNeutralPort(t *testing.T) {
	for name, provider := range map[string]Provider{
		"local-docker": NewLocalDockerProvider(),
		"tencent-tke":  NewTencentProvider(),
	} {
		if _, ok := provider.(workspaceLaunchProvider); !ok {
			t.Fatalf("provider %s does not implement workspaceLaunchProvider", name)
		}
	}
}

func TestReadLegacyWorkspaceLaunchBindingUsesScopedHistoryAndReadback(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := &legacyWorkspaceLaunchReadProvider{
		compute:    ComputeAllocation{ID: "compute-legacy", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", Status: "running", Provider: "local-docker", NodePoolID: "pool-basic"},
		storage:    StorageVolume{ID: "storage-legacy", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready", SizeGB: 10, Provider: "local-docker"},
		attachment: StorageAttachment{ID: "attachment-legacy", OperationID: "attachment-op", WorkspaceID: "ws-alpha", ComputeID: "compute-legacy", VolumeID: "storage-legacy", Status: "attached", Provider: "local-docker"},
		secret:     GatewaySecret{SecretRef: "secret-ws-alpha", Version: "v1", Fingerprint: "sha256:" + strings.Repeat("d", 64)},
		runtime:    WorkspaceRuntime{ID: "runtime-legacy", OperationID: "runtime-op", WorkspaceID: "ws-alpha", URL: "http://runtime.local", Status: "running", Ready: true, ServiceName: "runtime-ws-alpha", ImageID: "image-alpha", Access: RuntimeAccess{Username: "owner", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "secret-ws-alpha"}},
	}
	service := NewServiceWithOperationStore(provider, store)
	requestHash := strings.Repeat("9", 64)
	preflightRef := persistLegacyMigrationPreflight(t, service, WorkspaceLaunchPreflightInput{
		SchemaVersion: 1, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: "image-alpha", RequestHash: requestHash,
	})
	identities := legacyWorkspaceLaunchFixtureIdentities()
	legacyComputeBindingRef := ""
	for index, identity := range identities {
		resource := map[string]any{}
		switch identity.Stage {
		case "ensure_compute_allocation":
			resource["resource"] = provider.compute
			resource["allocationPlan"] = ComputeAllocationPreparation{PoolID: "pool-basic", PackageID: "basic", NodePoolID: "pool-basic", InstanceType: "basic", MaxReplicas: 10, BaselineReplicas: 0, TargetReplicas: 1}
		case "storage":
			resource["resource"] = provider.storage
		case "attachment":
			resource["resource"] = provider.attachment
		case "secret":
			resource["resource"] = provider.secret
		case "runtime":
			resource["resource"] = provider.runtime
		}
		operationID := "fabric-" + identity.Stage
		action, kind, historyResourceID := legacyWorkspaceLaunchStageStoreIdentity(identity.Stage, identity.ResourceRef, "ws-alpha")
		operation := newOperation(action, kind, historyResourceID, "acct-alpha", "ws-alpha", "persisted-"+identity.Stage+"-key", strings.Repeat(string(rune('a'+index)), 64), time.Now().UTC())
		operation.ID, operation.OperationID, operation.Provider, operation.Status = operationID, "logical-"+operationID, provider.Descriptor().Name, "succeeded"
		operation.RedactedProviderPayload = resource
		if err := store.Append(context.Background(), operation); err != nil {
			t.Fatal(err)
		}
		if identity.Stage == "ensure_compute_allocation" {
			legacyComputeBindingRef = operation.ID
		}
	}

	result, err := service.ReadLegacyWorkspaceLaunchBinding(context.Background(), LegacyWorkspaceLaunchBindingInput{
		SchemaVersion: 2, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", RequestHash: requestHash,
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: "image-alpha", Stages: identities,
		WorkspaceAPIKeyID: 11, WorkspaceKeyFingerprint: provider.secret.Fingerprint,
	})
	if err != nil || result.State != "ready" || result.Resources.ComputeAllocationID != "compute-legacy" || result.Resources.StorageID != "storage-legacy" ||
		result.Resources.AttachmentID != "attachment-legacy" || result.Resources.GatewaySecretRef != provider.secret.SecretRef || result.Resources.RuntimeID != "runtime-legacy" || result.Resources.RuntimeURL != provider.runtime.URL ||
		result.ProviderProfileRef != provider.Descriptor().Name || result.PreflightBindingRef != preflightRef || result.Resources.ComputeBindingRef != legacyComputeBindingRef {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if provider.reads != 5 || lenMustListOperations(t, store) != 6 {
		t.Fatalf("readback/mutation counts reads=%d operations=%d", provider.reads, lenMustListOperations(t, store))
	}

	provider.runtimeReadErr = errors.New("runtime readback unavailable")
	result, err = service.ReadLegacyWorkspaceLaunchBinding(context.Background(), LegacyWorkspaceLaunchBindingInput{
		SchemaVersion: 2, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", RequestHash: requestHash,
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: "image-alpha", Stages: identities,
		WorkspaceAPIKeyID: 11, WorkspaceKeyFingerprint: provider.secret.Fingerprint,
	})
	if err != nil || result.State != "unknown" || result.Reason != "legacy_runtime_readback_unavailable" {
		t.Fatalf("runtime readback failure result=%#v err=%v", result, err)
	}
	provider.runtimeReadErr = nil

	competing := identities[1]
	action, kind, resourceID := legacyWorkspaceLaunchStageStoreIdentity(competing.Stage, competing.ResourceRef, "ws-alpha")
	operation := newOperation(action, kind, resourceID, "acct-alpha", "ws-alpha", "persisted-competing-key", strings.Repeat("e", 64), time.Now().UTC())
	operation.ID, operation.OperationID, operation.Provider, operation.Status = "fabric-competing", "logical-fabric-competing", provider.Descriptor().Name, "succeeded"
	operation.RedactedProviderPayload = map[string]any{"resource": provider.storage}
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	result, err = service.ReadLegacyWorkspaceLaunchBinding(context.Background(), LegacyWorkspaceLaunchBindingInput{SchemaVersion: 2, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", RequestHash: requestHash, PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: "image-alpha", Stages: identities, WorkspaceAPIKeyID: 11, WorkspaceKeyFingerprint: provider.secret.Fingerprint})
	if err != nil || result.State != "conflict" || legacyStageState(result.Stages, "storage") != "conflict" {
		t.Fatalf("competing result=%#v err=%v", result, err)
	}
}

func TestReadLegacyWorkspaceLaunchBindingProvesExactUniqueLogicalOperationFromScopedOwnerHistory(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := &legacyWorkspaceLaunchReadProvider{
		compute: ComputeAllocation{ID: "compute-legacy", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", Status: "running", Provider: "local-docker", NodePoolID: "pool-basic"},
		storage: StorageVolume{ID: "storage-legacy", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready", SizeGB: 10, Provider: "local-docker"},
	}
	service := NewServiceWithOperationStore(provider, store)
	requestHash := strings.Repeat("9", 64)
	persistLegacyMigrationPreflight(t, service, WorkspaceLaunchPreflightInput{
		SchemaVersion: 1, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: "image-alpha", RequestHash: requestHash,
	})
	history := []struct {
		stage, action, kind, resourceID, operationID, key, hash string
		payload                                                 map[string]any
	}{
		{"ensure_compute_allocation", "create_compute_allocation", "compute_allocation", "compute-legacy", "fabric-history-compute", "persisted-compute-key", strings.Repeat("a", 64), map[string]any{
			"resource":       provider.compute,
			"allocationPlan": ComputeAllocationPreparation{PoolID: "pool-basic", PackageID: "basic", NodePoolID: "pool-basic", InstanceType: "basic", MaxReplicas: 10, BaselineReplicas: 0, TargetReplicas: 1},
		}},
		{"storage", "create_storage_volume", "storage_volume", "storage-legacy", "fabric-history-storage", "persisted-storage-key", strings.Repeat("b", 64), map[string]any{"resource": provider.storage}},
	}
	for _, item := range history {
		operation := newOperation(item.action, item.kind, item.resourceID, "acct-alpha", "ws-alpha", item.key, item.hash, time.Now().UTC())
		operation.ID, operation.OperationID, operation.Provider, operation.Status = item.operationID, "logical-"+item.operationID, provider.Descriptor().Name, "succeeded"
		operation.RedactedProviderPayload = item.payload
		if err := store.Append(context.Background(), operation); err != nil {
			t.Fatal(err)
		}
	}

	result, err := service.ReadLegacyWorkspaceLaunchBinding(context.Background(), LegacyWorkspaceLaunchBindingInput{
		SchemaVersion: 2, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", RequestHash: requestHash, PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: "image-alpha",
		Stages: []LegacyWorkspaceLaunchStageIdentity{
			{Stage: "ensure_compute_allocation", ResourceRef: "compute-legacy"},
			{Stage: "storage", ResourceRef: "storage-legacy"},
		},
	})
	if err != nil || result.State != "ready" || len(result.Stages) != 2 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	for _, stage := range result.Stages {
		if stage.OperationRef == "" || stage.ResourceBindingRef == "" || stage.AuthoritativeReadbackRef == "" {
			t.Fatalf("owner history did not prove an exact unique logical operation: %#v", stage)
		}
	}
	if provider.reads != 2 || lenMustListOperations(t, store) != 3 {
		t.Fatalf("readback/mutation counts reads=%d operations=%d", provider.reads, lenMustListOperations(t, store))
	}
}

func TestReadLegacyWorkspaceLaunchBindingTreatsMissingPersistedResourceHistoryAsUnknown(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := &legacyWorkspaceLaunchReadProvider{}
	service := NewServiceWithOperationStore(provider, store)
	requestHash := strings.Repeat("9", 64)
	persistLegacyMigrationPreflight(t, service, WorkspaceLaunchPreflightInput{
		SchemaVersion: 1, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: "image-alpha", RequestHash: requestHash,
	})
	result, err := service.ReadLegacyWorkspaceLaunchBinding(context.Background(), LegacyWorkspaceLaunchBindingInput{
		SchemaVersion: 2, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
		RequestHash: requestHash, PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: "image-alpha",
		Stages: []LegacyWorkspaceLaunchStageIdentity{{Stage: "ensure_compute_allocation", ResourceRef: "compute-persisted"}},
	})
	if err != nil || result.State != "unknown" || result.Reason != "legacy_operation_history_missing" || provider.reads != 0 {
		t.Fatalf("missing owner history result=%#v reads=%d err=%v", result, provider.reads, err)
	}
}

func TestReadLegacyWorkspaceLaunchBindingTreatsMissingExactPreflightHistoryAsUnknown(t *testing.T) {
	provider := &legacyWorkspaceLaunchReadProvider{}
	result, err := NewServiceWithOperationStore(provider, NewMemoryOperationStore()).ReadLegacyWorkspaceLaunchBinding(context.Background(), LegacyWorkspaceLaunchBindingInput{
		SchemaVersion: 2, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
		RequestHash: strings.Repeat("9", 64), PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: "image-alpha",
		Stages: []LegacyWorkspaceLaunchStageIdentity{{Stage: "ensure_compute_allocation", ResourceRef: "compute-persisted"}},
	})
	if err != nil || result.State != "unknown" || result.Reason != "legacy_preflight_history_missing" || provider.reads != 0 {
		t.Fatalf("missing exact preflight result=%#v reads=%d err=%v", result, provider.reads, err)
	}
}

func TestReadLegacyWorkspaceLaunchBindingRequiresFreshStorageReadback(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := &legacyWorkspaceLaunchMissingStorageReader{
		compute: ComputeAllocation{ID: "compute-legacy", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", Status: "running", Provider: "local-docker", NodePoolID: "pool-basic"},
	}
	service := NewServiceWithOperationStore(provider, store)
	requestHash := strings.Repeat("9", 64)
	persistLegacyMigrationPreflight(t, service, WorkspaceLaunchPreflightInput{
		SchemaVersion: 1, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: "image-alpha", RequestHash: requestHash,
	})
	resources := []struct {
		stage, resourceID string
		resource          any
	}{
		{"ensure_compute_allocation", "compute-legacy", map[string]any{
			"resource":       provider.compute,
			"allocationPlan": ComputeAllocationPreparation{PoolID: "pool-basic", PackageID: "basic", NodePoolID: "pool-basic", InstanceType: "basic", MaxReplicas: 10, TargetReplicas: 1},
		}},
		{"storage", "storage-legacy", StorageVolume{ID: "storage-legacy", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready", SizeGB: 10, Provider: "local-docker"}},
	}
	identities := make([]LegacyWorkspaceLaunchStageIdentity, 0, len(resources))
	for index, item := range resources {
		action, kind, historyResourceID := legacyWorkspaceLaunchStageStoreIdentity(item.stage, item.resourceID, "ws-alpha")
		operation := newOperation(action, kind, historyResourceID, "acct-alpha", "ws-alpha", "persisted-"+item.stage+"-key", strings.Repeat(string(rune('a'+index)), 64), time.Now().UTC())
		operation.ID, operation.OperationID, operation.Provider, operation.Status = "fabric-"+item.stage, "logical-"+item.stage, provider.Descriptor().Name, "succeeded"
		operation.RedactedProviderPayload = map[string]any{"resource": item.resource}
		if item.stage == "ensure_compute_allocation" {
			operation.RedactedProviderPayload = item.resource.(map[string]any)
		}
		if err := store.Append(context.Background(), operation); err != nil {
			t.Fatal(err)
		}
		identities = append(identities, LegacyWorkspaceLaunchStageIdentity{Stage: item.stage, ResourceRef: item.resourceID})
	}

	result, err := service.ReadLegacyWorkspaceLaunchBinding(context.Background(), LegacyWorkspaceLaunchBindingInput{
		SchemaVersion: 2, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", RequestHash: requestHash,
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: "image-alpha", Stages: identities,
	})
	if err != nil || result.State != "unknown" || result.Reason != "legacy_storage_readback_unavailable" || provider.reads != 1 {
		t.Fatalf("missing storage readback result=%#v reads=%d err=%v", result, provider.reads, err)
	}
}

func persistLegacyMigrationPreflight(t *testing.T, service *Service, input WorkspaceLaunchPreflightInput) string {
	t.Helper()
	admission := workspaceLaunchPreflightAdmission{SchemaVersion: 1, Input: input, ProviderProfileRef: service.provider.Descriptor().Name}
	admission.BindingRef = "fabric-preflight:" + hashInput(admission)
	if err := service.persistWorkspaceLaunchPreflight(context.Background(), admission); err != nil {
		t.Fatal(err)
	}
	return admission.BindingRef
}

func lenMustListOperations(t *testing.T, store OperationStore) int {
	t.Helper()
	operations, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(operations)
}

type legacyWorkspaceLaunchReadProvider struct {
	testProvider
	compute        ComputeAllocation
	storage        StorageVolume
	attachment     StorageAttachment
	secret         GatewaySecret
	runtime        WorkspaceRuntime
	runtimeReadErr error
	reads          int
}

type legacyWorkspaceLaunchMissingStorageReader struct {
	testProvider
	compute ComputeAllocation
	reads   int
}

func (p *legacyWorkspaceLaunchMissingStorageReader) DiscoverComputeAllocation(_ context.Context, _ ComputeAllocation, _ ComputeAllocationPreparation) (ComputeAllocation, error) {
	p.reads++
	return p.compute, nil
}

func (p *legacyWorkspaceLaunchReadProvider) DiscoverComputeAllocation(_ context.Context, allocation ComputeAllocation, _ ComputeAllocationPreparation) (ComputeAllocation, error) {
	p.reads++
	return p.compute, nil
}
func (p *legacyWorkspaceLaunchReadProvider) ReadStorageVolumeStatus(_ context.Context, _ StorageVolume) (StorageVolume, error) {
	p.reads++
	return p.storage, nil
}
func (p *legacyWorkspaceLaunchReadProvider) ReadStorageAttachment(_ context.Context, _ StorageAttachment, _ ComputeAllocation, _ StorageVolume) (StorageAttachment, error) {
	p.reads++
	return p.attachment, nil
}
func (p *legacyWorkspaceLaunchReadProvider) ReadGatewaySecretByDigest(_ context.Context, _ GatewaySecretReadbackInput) (GatewaySecret, error) {
	p.reads++
	return p.secret, nil
}
func (p *legacyWorkspaceLaunchReadProvider) WorkspaceRuntimeStatus(_ context.Context, _ string) (WorkspaceRuntime, error) {
	p.reads++
	return p.runtime, p.runtimeReadErr
}

func legacyWorkspaceLaunchFixtureIdentities() []LegacyWorkspaceLaunchStageIdentity {
	return []LegacyWorkspaceLaunchStageIdentity{
		{Stage: "ensure_compute_allocation", ResourceRef: "compute-legacy"},
		{Stage: "storage", ResourceRef: "storage-legacy"},
		{Stage: "attachment", ResourceRef: "attachment-legacy", PersistedOperationRef: "logical-fabric-attachment"},
		{Stage: "secret", ResourceRef: "secret-ws-alpha"},
		{Stage: "runtime", ResourceRef: "runtime-legacy", PersistedOperationRef: "logical-fabric-runtime"},
	}
}

func legacyStageState(stages []LegacyWorkspaceLaunchStageReadback, stage string) string {
	for _, item := range stages {
		if item.Stage == stage {
			return item.State
		}
	}
	return ""
}

func workspaceLaunchStageFixtureInput(preflight WorkspaceLaunchPreflight, image, launchHash, stage, action string, resources WorkspaceLaunchResources) WorkspaceLaunchStageInput {
	input := WorkspaceLaunchStageInput{
		Binding: WorkspaceLaunchStageBinding{
			SchemaVersion: 1, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
			Stage: stage, Action: action, FabricOperationID: "launch-alpha:" + stage, IdempotencyKey: "launch-alpha:" + stage,
		},
		ProviderProfileRef: "tencent-tke", PreflightBindingRef: preflight.BindingRef,
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: image, Resources: resources,
	}
	input.Binding.RequestHash = workspaceLaunchStageRequestHash(input, launchHash)
	return input
}

func TestWorkspaceLaunchPreflightIsDurableAndPointReadBeforeStageWrite(t *testing.T) {
	service, store, provider, preflight, image, launchHash := workspaceLaunchStageFixture(t)
	operation, err := store.Get(context.Background(), preflight.BindingRef)
	admission, ok := decodeWorkspaceLaunchPreflight(operation)
	if err != nil || !ok || operation.Status != "succeeded" || admission.BindingRef != preflight.BindingRef ||
		admission.Input.LaunchOperationID != "launch-alpha" || admission.Input.AccountID != "acct-alpha" ||
		admission.Input.WorkspaceID != "ws-alpha" || admission.Input.PackageID != "basic" || admission.Input.SizeGB != 10 ||
		admission.Input.WorkspaceImageDigest != image || admission.Input.RequestHash != launchHash || admission.ProviderProfileRef != "tencent-tke" {
		t.Fatalf("operation=%#v admission=%#v/%v err=%v", operation, admission, ok, err)
	}

	input := workspaceLaunchStageFixtureInput(preflight, image, launchHash, "storage", "ensure_storage", WorkspaceLaunchResources{})
	input.PreflightBindingRef = "fabric-preflight:" + strings.Repeat("0", 64)
	if _, err := service.EnsureWorkspaceLaunchStage(context.Background(), input); !errors.Is(err, ErrLaunchStageBindingNotFound) {
		t.Fatalf("forged preflight error=%v", err)
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].ID != preflight.BindingRef || provider.ensureCalls != 0 {
		t.Fatalf("forged preflight crossed stage write: operations=%#v providerCalls=%d err=%v", operations, provider.ensureCalls, err)
	}
}

func TestWorkspaceLaunchStageRejectsEveryPreflightIdentityDrift(t *testing.T) {
	service, _, provider, preflight, image, launchHash := workspaceLaunchStageFixture(t)
	tests := []struct {
		name   string
		mutate func(*WorkspaceLaunchStageInput)
	}{
		{name: "launch", mutate: func(input *WorkspaceLaunchStageInput) { input.Binding.LaunchOperationID = "launch-other" }},
		{name: "account", mutate: func(input *WorkspaceLaunchStageInput) { input.Binding.AccountID = "acct-other" }},
		{name: "workspace", mutate: func(input *WorkspaceLaunchStageInput) { input.Binding.WorkspaceID = "ws-other" }},
		{name: "provider", mutate: func(input *WorkspaceLaunchStageInput) { input.ProviderProfileRef = "local-docker" }},
		{name: "package", mutate: func(input *WorkspaceLaunchStageInput) { input.PackageID = "pro" }},
		{name: "size", mutate: func(input *WorkspaceLaunchStageInput) { input.SizeGB = 20 }},
		{name: "image", mutate: func(input *WorkspaceLaunchStageInput) {
			input.WorkspaceImageDigest = "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app@sha256:" + strings.Repeat("c", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := workspaceLaunchStageFixtureInput(preflight, image, launchHash, "storage", "ensure_storage", WorkspaceLaunchResources{})
			test.mutate(&input)
			input.Binding.RequestHash = workspaceLaunchStageRequestHash(input, launchHash)
			if _, err := service.EnsureWorkspaceLaunchStage(context.Background(), input); !errors.Is(err, ErrLaunchStageBindingConflict) {
				t.Fatalf("drift error=%v input=%#v", err, input)
			}
			if provider.ensureCalls != 0 {
				t.Fatalf("identity drift reached provider: calls=%d", provider.ensureCalls)
			}
		})
	}
}

func TestWorkspaceLaunchStageRequestHashMatchesOwnerGoldenVectors(t *testing.T) {
	for _, vector := range workspaceLaunchStageHashGoldenVectors(t) {
		t.Run(vector.Stage, func(t *testing.T) {
			input := WorkspaceLaunchStageInput{
				Binding:              WorkspaceLaunchStageBinding{Stage: vector.Stage, Action: vector.Payload.Action},
				PackageID:            vector.Payload.PackageID,
				SizeGB:               vector.Payload.SizeGB,
				WorkspaceImageDigest: vector.Payload.ImageDigest,
				Resources:            vector.Payload.Resources,
			}
			if got := workspaceLaunchStageRequestHash(input, vector.Payload.LaunchRequestHash); got != vector.SHA256 {
				t.Fatalf("workspaceLaunchStageRequestHash()=%s, owner golden=%s", got, vector.SHA256)
			}
		})
	}
}

func TestWorkspaceLaunchStageRejectsRequestHashAndResourceDrift(t *testing.T) {
	service, _, provider, preflight, image, launchHash := workspaceLaunchStageFixture(t)
	stages := []struct {
		stage     string
		action    string
		resources WorkspaceLaunchResources
	}{
		{stage: "ensure_compute_allocation", action: "ensure_compute_allocation"},
		{stage: "storage", action: "ensure_storage", resources: WorkspaceLaunchResources{ComputeAllocationID: "ca-alpha", ComputeBindingRef: "launch-alpha:ensure_compute_allocation"}},
		{stage: "attachment", action: "ensure_attachment", resources: WorkspaceLaunchResources{ComputeAllocationID: "ca-alpha", ComputeBindingRef: "launch-alpha:ensure_compute_allocation", StorageID: "vol-alpha", StorageBindingRef: "launch-alpha:storage"}},
		{stage: "secret", action: "ensure_gateway_secret", resources: WorkspaceLaunchResources{AttachmentID: "att-alpha", AttachmentBindingRef: "launch-alpha:attachment", GatewaySecretFingerprint: "sha256:" + strings.Repeat("d", 64)}},
		{stage: "runtime", action: "ensure_runtime", resources: WorkspaceLaunchResources{ComputeAllocationID: "ca-alpha", StorageID: "vol-alpha", AttachmentID: "att-alpha", GatewaySecretRef: "secret-alpha"}},
	}
	for _, stage := range stages {
		t.Run(stage.stage, func(t *testing.T) {
			input := workspaceLaunchStageFixtureInput(preflight, image, launchHash, stage.stage, stage.action, stage.resources)
			if err := service.validateWorkspaceLaunchStageInput(context.Background(), input); err != nil {
				t.Fatalf("canonical input rejected: %v", err)
			}
			driftedHash := input
			driftedHash.Binding.RequestHash = strings.Repeat("e", 64)
			if _, err := service.EnsureWorkspaceLaunchStage(context.Background(), driftedHash); !errors.Is(err, ErrLaunchStageBindingConflict) {
				t.Fatalf("request hash drift error=%v", err)
			}
			driftedResource := input
			driftedResource.Resources.RuntimeURL = "http://drift.invalid"
			if _, err := service.EnsureWorkspaceLaunchStage(context.Background(), driftedResource); !errors.Is(err, ErrLaunchStageBindingConflict) {
				t.Fatalf("resource drift error=%v", err)
			}
			if provider.ensureCalls != 0 {
				t.Fatalf("hash or resource drift reached provider: calls=%d", provider.ensureCalls)
			}
		})
	}
}

func TestWorkspaceLaunchExpectedBindingRequiresExactAuthoritativeRecordForFiveStages(t *testing.T) {
	service, store, _, preflight, image, launchHash := workspaceLaunchStageFixture(t)
	tests := []struct {
		stage, action string
		resources     func(string) WorkspaceLaunchResources
		drift         func(*WorkspaceLaunchResources)
	}{
		{stage: "ensure_compute_allocation", action: "ensure_compute_allocation", resources: func(ref string) WorkspaceLaunchResources {
			return WorkspaceLaunchResources{ComputeAllocationID: "ca-alpha", ComputeBindingRef: ref}
		}, drift: func(value *WorkspaceLaunchResources) { value.ComputeAllocationID = "ca-other" }},
		{stage: "storage", action: "ensure_storage", resources: func(ref string) WorkspaceLaunchResources {
			return WorkspaceLaunchResources{StorageID: "vol-alpha", StorageBindingRef: ref}
		}, drift: func(value *WorkspaceLaunchResources) { value.StorageID = "vol-other" }},
		{stage: "attachment", action: "ensure_attachment", resources: func(ref string) WorkspaceLaunchResources {
			return WorkspaceLaunchResources{AttachmentID: "att-alpha", AttachmentBindingRef: ref}
		}, drift: func(value *WorkspaceLaunchResources) { value.AttachmentID = "att-other" }},
		{stage: "secret", action: "ensure_gateway_secret", resources: func(ref string) WorkspaceLaunchResources {
			return WorkspaceLaunchResources{GatewaySecretRef: "secret-alpha", GatewaySecretVersion: "v1", GatewaySecretFingerprint: "sha256:" + strings.Repeat("d", 64), SecretBindingRef: ref}
		}, drift: func(value *WorkspaceLaunchResources) { value.GatewaySecretRef = "secret-other" }},
		{stage: "runtime", action: "ensure_runtime", resources: func(ref string) WorkspaceLaunchResources {
			return WorkspaceLaunchResources{RuntimeID: "rt-alpha", RuntimeServiceName: "runtime-alpha", RuntimeURL: "http://runtime.invalid", RuntimeBindingRef: ref}
		}, drift: func(value *WorkspaceLaunchResources) { value.RuntimeID = "rt-other" }},
	}
	for _, test := range tests {
		t.Run(test.stage, func(t *testing.T) {
			previousInput := workspaceLaunchStageFixtureInput(preflight, image, launchHash, test.stage, test.action, WorkspaceLaunchResources{})
			previousInput.Binding.FabricOperationID = "launch-alpha:" + test.stage + "-previous"
			previousInput.Binding.IdempotencyKey = previousInput.Binding.FabricOperationID
			previousInput.Binding.RequestHash = workspaceLaunchStageRequestHash(previousInput, launchHash)
			operation, record, err := newWorkspaceLaunchStageOperation(previousInput, "tencent-tke", time.Now)
			if err != nil {
				t.Fatal(err)
			}
			record.Resources = test.resources(previousInput.Binding.FabricOperationID)
			setWorkspaceLaunchStageRecord(&operation, record)
			operation.Status, operation.FinishedAt = "succeeded", time.Now().UTC()
			if err := store.Append(context.Background(), operation); err != nil {
				t.Fatal(err)
			}

			input := workspaceLaunchStageFixtureInput(preflight, image, launchHash, test.stage, test.action, record.Resources)
			input.Binding.FabricOperationID = "launch-alpha:" + test.stage + "-retry"
			input.Binding.IdempotencyKey = input.Binding.FabricOperationID
			input.Binding.ExpectedResourceBinding = previousInput.Binding.FabricOperationID
			input.Binding.RequestHash = workspaceLaunchStageRequestHash(input, launchHash)
			if err := service.validateWorkspaceLaunchStageInput(context.Background(), input); err != nil {
				t.Fatalf("exact expected binding rejected: %v", err)
			}

			driftedBinding := input
			driftedBinding.Binding.ExpectedResourceBinding = "launch-alpha:" + test.stage + "-other"
			if err := service.validateWorkspaceLaunchStageInput(context.Background(), driftedBinding); !errors.Is(err, ErrLaunchStageBindingConflict) {
				t.Fatalf("expected binding drift error=%v", err)
			}
			driftedResource := input
			test.drift(&driftedResource.Resources)
			driftedResource.Binding.RequestHash = workspaceLaunchStageRequestHash(driftedResource, launchHash)
			if err := service.validateWorkspaceLaunchStageInput(context.Background(), driftedResource); !errors.Is(err, ErrLaunchStageBindingConflict) {
				t.Fatalf("expected resource identity drift error=%v", err)
			}
		})
	}
}
