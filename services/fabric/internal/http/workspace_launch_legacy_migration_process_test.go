package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	nethttp "net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/fabric/internal/fabric"
)

const legacyMigrationFabricHarnessEnv = "OPL_LEGACY_MIGRATION_FABRIC_HELPER"

type legacyMigrationProviderEvent struct {
	Stage    string `json:"stage"`
	Mutation bool   `json:"mutation"`
}

type legacyMigrationRecordingProvider struct {
	testProvider

	mu         sync.Mutex
	eventsPath string
	compute    fabric.ComputeAllocation
	storage    fabric.StorageVolume
	attachment fabric.StorageAttachment
	secret     fabric.GatewaySecret
	runtime    fabric.WorkspaceRuntime
}

func (p *legacyMigrationRecordingProvider) Descriptor() fabric.ProviderDescriptor {
	descriptor := p.testProvider.Descriptor()
	descriptor.Name = "local-docker"
	return descriptor
}

func (p *legacyMigrationRecordingProvider) ValidateWorkspaceImageReference(value string) bool {
	return value == "registry.example/workspace@sha256:"+strings.Repeat("b", 64)
}

func (p *legacyMigrationRecordingProvider) record(stage string, mutation bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	body, err := json.Marshal(legacyMigrationProviderEvent{Stage: stage, Mutation: mutation})
	if err != nil {
		panic(err)
	}
	file, err := os.OpenFile(p.eventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	defer file.Close()
	if _, err := file.Write(append(body, '\n')); err != nil {
		panic(err)
	}
}

func (p *legacyMigrationRecordingProvider) DiscoverComputeAllocation(context.Context, fabric.ComputeAllocation, fabric.ComputeAllocationPreparation) (fabric.ComputeAllocation, error) {
	p.record("compute_read", false)
	return p.compute, nil
}

func (p *legacyMigrationRecordingProvider) ReadStorageVolumeStatus(context.Context, fabric.StorageVolume) (fabric.StorageVolume, error) {
	p.record("storage_read", false)
	return p.storage, nil
}

func (p *legacyMigrationRecordingProvider) ReadStorageAttachment(context.Context, fabric.StorageAttachment, fabric.ComputeAllocation, fabric.StorageVolume) (fabric.StorageAttachment, error) {
	p.record("attachment_read", false)
	return p.attachment, nil
}

func (p *legacyMigrationRecordingProvider) ReadGatewaySecretByDigest(context.Context, fabric.GatewaySecretReadbackInput) (fabric.GatewaySecret, error) {
	p.record("secret_read", false)
	return p.secret, nil
}

func (p *legacyMigrationRecordingProvider) WorkspaceRuntimeStatus(context.Context, string) (fabric.WorkspaceRuntime, error) {
	p.record("runtime_read", false)
	return p.runtime, nil
}

func (p *legacyMigrationRecordingProvider) CreateComputeAllocation(ctx context.Context, input fabric.ComputeAllocationExecution) (fabric.ComputeAllocation, error) {
	p.record("compute_create", true)
	return p.testProvider.CreateComputeAllocation(ctx, input)
}

func (p *legacyMigrationRecordingProvider) CreateStorageVolume(ctx context.Context, input fabric.StorageVolumeInput) (fabric.StorageVolume, error) {
	p.record("storage_create", true)
	return p.testProvider.CreateStorageVolume(ctx, input)
}

func (p *legacyMigrationRecordingProvider) CreateStorageAttachment(ctx context.Context, input fabric.StorageAttachmentInput, compute fabric.ComputeAllocation, storage fabric.StorageVolume) (fabric.StorageAttachment, error) {
	p.record("attachment_create", true)
	return p.testProvider.CreateStorageAttachment(ctx, input, compute, storage)
}

func (p *legacyMigrationRecordingProvider) UpsertGatewaySecret(ctx context.Context, input fabric.GatewaySecretInput) (fabric.GatewaySecret, error) {
	p.record("secret_write", true)
	return p.testProvider.UpsertGatewaySecret(ctx, input)
}

func (p *legacyMigrationRecordingProvider) CreateWorkspaceRuntime(ctx context.Context, input fabric.WorkspaceRuntimeInput, compute fabric.ComputeAllocation, storage fabric.StorageVolume) (fabric.WorkspaceRuntime, error) {
	p.record("runtime_create", true)
	return p.testProvider.CreateWorkspaceRuntime(ctx, input, compute, storage)
}

func (p *legacyMigrationRecordingProvider) EnsureWorkspaceLaunchStage(context.Context, fabric.WorkspaceLaunchProviderRequest) (fabric.WorkspaceLaunchProviderResult, error) {
	p.record("workspace_launch_stage_ensure", true)
	return fabric.WorkspaceLaunchProviderResult{}, errors.New("legacy migration provider mutation is forbidden")
}

func TestWorkspaceLaunchLegacyMigrationFabricProcess(t *testing.T) {
	if os.Getenv(legacyMigrationFabricHarnessEnv) != "1" {
		return
	}
	addr := strings.TrimSpace(os.Getenv("OPL_LEGACY_MIGRATION_FABRIC_ADDR"))
	databaseURL := strings.TrimSpace(os.Getenv("OPL_LEGACY_MIGRATION_FABRIC_DATABASE_URL"))
	eventsPath := strings.TrimSpace(os.Getenv("OPL_LEGACY_MIGRATION_FABRIC_EVENTS"))
	runtimeURL := strings.TrimSpace(os.Getenv("OPL_LEGACY_MIGRATION_RUNTIME_URL"))
	token := strings.TrimSpace(os.Getenv("OPL_LEGACY_MIGRATION_FABRIC_TOKEN"))
	capabilityKey := strings.TrimSpace(os.Getenv("OPL_LEGACY_MIGRATION_FABRIC_CAPABILITY_KEY"))
	if addr == "" || databaseURL == "" || eventsPath == "" || runtimeURL == "" || token == "" || capabilityKey == "" {
		t.Fatal("legacy migration Fabric helper configuration incomplete")
	}
	store, err := fabric.NewPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatalf("open real Fabric PostgreSQL OperationStore: %v", err)
	}
	provider := legacyMigrationProviderFixture(eventsPath, runtimeURL)
	service := fabric.NewServiceWithOperationStore(provider, store)
	preflight, err := service.PreflightWorkspaceLaunch(context.Background(), fabric.WorkspaceLaunchPreflightInput{
		SchemaVersion: 1, LaunchOperationID: "launch-paid-schema2", AccountID: "acct-paid", WorkspaceID: "ws-paid",
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: "registry.example/workspace@sha256:" + strings.Repeat("b", 64),
		RequestHash: strings.Repeat("a", 64),
	})
	if err != nil || !preflight.Available || preflight.BindingRef == "" {
		t.Fatalf("seed exact historical preflight: %#v err=%v", preflight, err)
	}
	seedLegacyMigrationFabricHistory(t, store, provider)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	handler := NewServerWithAuth(service, ServerAuthConfig{ControlPlaneToken: token, CapabilityKey: capabilityKey})
	server := &nethttp.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() { _ = server.Close() })
	if err := server.Serve(listener); err != nil && !errors.Is(err, nethttp.ErrServerClosed) {
		t.Fatal(err)
	}
}

func legacyMigrationProviderFixture(eventsPath, runtimeURL string) *legacyMigrationRecordingProvider {
	secretFingerprint := "sha256:722c58cce111f93ae1012c716d360419081fd21ffdcc5abe8c42765f7063d75e"
	return &legacyMigrationRecordingProvider{
		eventsPath: eventsPath,
		compute: fabric.ComputeAllocation{
			ID: "compute-legacy-paid", AccountID: "acct-paid", WorkspaceID: "ws-paid", PackageID: "basic",
			Status: "running", Provider: "local-docker", PoolID: "pool-basic", NodePoolID: "pool-basic",
		},
		storage: fabric.StorageVolume{
			ID: "storage-legacy-paid", AccountID: "acct-paid", WorkspaceID: "ws-paid", Status: "ready",
			SizeGB: 10, Provider: "local-docker",
		},
		attachment: fabric.StorageAttachment{
			ID: "attachment-legacy-paid", OperationID: "attachment-operation-paid", WorkspaceID: "ws-paid",
			ComputeID: "compute-legacy-paid", VolumeID: "storage-legacy-paid", Status: "attached", Provider: "local-docker",
		},
		secret: fabric.GatewaySecret{SecretRef: "secret-ws-paid", Version: "v1", Fingerprint: secretFingerprint},
		runtime: fabric.WorkspaceRuntime{
			ID: "runtime-legacy-paid", OperationID: "workspace-operation-paid:runtime", WorkspaceID: "ws-paid",
			URL: runtimeURL, Status: "running", Ready: true, ServiceName: "runtime-ws-paid",
			ImageID: "registry.example/workspace@sha256:" + strings.Repeat("b", 64),
			Access:  fabric.RuntimeAccess{Username: "owner", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "secret-ws-paid"},
		},
	}
}

func seedLegacyMigrationFabricHistory(t *testing.T, store fabric.OperationStore, provider *legacyMigrationRecordingProvider) {
	t.Helper()
	type stageFixture struct {
		stage, action, kind, resourceID, logicalID, idempotencyKey string
		payload                                                    map[string]any
	}
	stages := []stageFixture{
		{stage: "compute", action: "create_compute_allocation", kind: "compute_allocation", resourceID: provider.compute.ID, logicalID: "logical-compute-paid", idempotencyKey: "persisted-compute-paid", payload: map[string]any{
			"resource":       provider.compute,
			"allocationPlan": fabric.ComputeAllocationPreparation{PoolID: "pool-basic", PackageID: "basic", NodePoolID: "pool-basic", InstanceType: "basic", MaxReplicas: 10, TargetReplicas: 1},
		}},
		{stage: "storage", action: "create_storage_volume", kind: "storage_volume", resourceID: provider.storage.ID, logicalID: "logical-storage-paid", idempotencyKey: "persisted-storage-paid", payload: map[string]any{"resource": provider.storage}},
		{stage: "attachment", action: "create_storage_attachment", kind: "storage_attachment", resourceID: provider.attachment.ID, logicalID: "logical-attachment-paid", idempotencyKey: provider.attachment.OperationID, payload: map[string]any{"resource": provider.attachment}},
		{stage: "secret", action: "upsert_gateway_secret", kind: "gateway_secret", resourceID: provider.secret.SecretRef, logicalID: "logical-secret-paid", idempotencyKey: "workspace-operation-paid:secret:gateway-secret", payload: map[string]any{"resource": provider.secret}},
		{stage: "runtime", action: "create_workspace_runtime", kind: "workspace_runtime", resourceID: provider.runtime.WorkspaceID, logicalID: "logical-runtime-paid", idempotencyKey: provider.runtime.OperationID, payload: map[string]any{"resource": provider.runtime}},
	}
	base := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	for index, stage := range stages {
		at := base.Add(time.Duration(index) * time.Second)
		operation := fabric.FabricOperation{
			ID: "fabric-record-" + stage.stage + "-paid", OperationID: stage.logicalID, CallerService: "control-plane",
			Action: stage.action, ResourceKind: stage.kind, ResourceID: stage.resourceID,
			AccountID: "acct-paid", WorkspaceID: "ws-paid", Provider: provider.Descriptor().Name,
			ProviderRequestID: "provider-readback-" + stage.stage, IdempotencyKey: stage.idempotencyKey,
			RequestHash: strings.Repeat(fmt.Sprintf("%x", index+10), 32), RedactedProviderPayload: stage.payload,
			Status: "succeeded", StartedAt: at, FinishedAt: at.Add(time.Millisecond), CreatedAt: at,
		}
		if err := store.Append(context.Background(), operation); err != nil {
			t.Fatalf("seed %s Fabric history: %v", stage.stage, err)
		}
	}
}
