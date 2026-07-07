package fabric

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCatalogExposesWorkspacePackages(t *testing.T) {
	service := NewService(testProvider{})
	catalog := service.Catalog(context.Background())

	if len(catalog.WorkspacePackages) == 0 {
		t.Fatalf("expected workspace packages")
	}
	if catalog.WorkspacePackages[0].ID != "basic" {
		t.Fatalf("first package = %q, want basic", catalog.WorkspacePackages[0].ID)
	}
}

func TestDryRunComputeAllocationRecordsProviderRequestIDWithoutLedgerTypes(t *testing.T) {
	service := NewService(testProvider{})
	allocation, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{
		AccountID:      "acct-alpha",
		WorkspaceID:    "ws-alpha",
		PackageID:      "basic",
		IdempotencyKey: "fabric-compute-once",
		DryRun:         true,
	})
	if err != nil {
		t.Fatalf("create allocation: %v", err)
	}
	if allocation.ProviderRequestID == "" {
		t.Fatalf("expected provider request id")
	}
	if strings.Contains(strings.ToLower(allocation.ProviderRequestID), "ledger") {
		t.Fatalf("provider request id must not reference ledger: %s", allocation.ProviderRequestID)
	}
}

type testProvider struct{}

func (testProvider) CreateComputeAllocation(_ context.Context, input ComputeAllocationInput) (ComputeAllocation, error) {
	now := time.Now().UTC()
	return ComputeAllocation{ID: "ca-test", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID, Status: "allocated", Provider: "tencent-tke", ProviderRequestID: providerRequestID("compute", input.IdempotencyKey), ServiceName: "opl-ca-test", CreatedAt: now}, nil
}

func (testProvider) DestroyComputeAllocation(_ context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	allocation.Status = "destroyed"
	return allocation, nil
}

func (testProvider) CreateStorageVolume(_ context.Context, input StorageVolumeInput) (StorageVolume, error) {
	return StorageVolume{ID: "vol-test", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", ProviderRequestID: providerRequestID("storage", input.IdempotencyKey)}, nil
}

func (testProvider) DestroyStorageVolume(_ context.Context, volume StorageVolume) (StorageVolume, error) {
	volume.Status = "destroyed"
	return volume, nil
}

func (testProvider) CreateStorageAttachment(_ context.Context, input StorageAttachmentInput, _ ComputeAllocation, _ StorageVolume) (StorageAttachment, error) {
	return StorageAttachment{ID: "att-test", WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Status: "attached", ProviderRequestID: providerRequestID("storage-attach", input.IdempotencyKey)}, nil
}

func (testProvider) DetachStorageAttachment(_ context.Context, attachment StorageAttachment) (StorageAttachment, error) {
	attachment.Status = "detached"
	return attachment, nil
}

func (testProvider) CreateWorkspaceRuntime(_ context.Context, input WorkspaceRuntimeInput, _ ComputeAllocation, _ StorageVolume) (WorkspaceRuntime, error) {
	return WorkspaceRuntime{ID: "rt-test", WorkspaceID: input.WorkspaceID, Status: "running", ServiceName: "opl-ca-test", ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey)}, nil
}

func (testProvider) WorkspaceRuntimeStatus(_ context.Context, workspaceID string) (WorkspaceRuntime, error) {
	return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "not_found"}, nil
}

func (testProvider) Readiness(_ context.Context) (map[string]any, error) {
	return map[string]any{"provider": "test", "ready": true}, nil
}
