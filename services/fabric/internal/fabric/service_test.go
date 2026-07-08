package fabric

import (
	"context"
	"fmt"
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

func TestComputeAllocationReturnsProvisioningBeforeProviderCompletes(t *testing.T) {
	provider := &blockingProvider{done: make(chan struct{})}
	service := NewService(provider)

	allocation, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{
		AccountID:      "acct-alpha",
		WorkspaceID:    "ws-alpha",
		PackageID:      "basic",
		IdempotencyKey: "compute-once",
	})
	if err != nil {
		t.Fatalf("create allocation: %v", err)
	}
	if allocation.Status != "provisioning" || allocation.ID == "" {
		t.Fatalf("initial allocation = %#v, want provisioning with id", allocation)
	}
	current, ok := service.GetComputeAllocation(context.Background(), allocation.ID)
	if !ok || current.Status != "provisioning" {
		t.Fatalf("stored allocation = %#v ok=%v, want provisioning", current, ok)
	}

	close(provider.done)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current, ok = service.GetComputeAllocation(context.Background(), allocation.ID)
		if ok && current.Status == "running" {
			if current.ID != allocation.ID || current.NodeName != "node-alpha" {
				t.Fatalf("completed allocation lost identity: %#v", current)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("allocation did not become running: %#v", current)
}

func TestResourceMutationsAppendFabricOperationFacts(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	ctx := context.Background()

	compute, err := service.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "ops-compute"})
	if err != nil {
		t.Fatalf("create compute: %v", err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")

	volume, err := service.CreateStorageVolume(ctx, StorageVolumeInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", SizeGB: 10, IdempotencyKey: "ops-storage"})
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	attachment, err := service.CreateStorageAttachment(ctx, StorageAttachmentInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, IdempotencyKey: "ops-attach"})
	if err != nil {
		t.Fatalf("attach storage: %v", err)
	}
	runtime, err := service.CreateWorkspaceRuntime(ctx, WorkspaceRuntimeInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, ImageID: "one-person-lab-app", IdempotencyKey: "ops-runtime"})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if _, err := service.DetachStorageAttachment(ctx, attachment.ID); err != nil {
		t.Fatalf("detach storage: %v", err)
	}
	if _, err := service.DestroyStorageVolume(ctx, volume.ID); err != nil {
		t.Fatalf("destroy storage: %v", err)
	}
	if _, err := service.DestroyComputeAllocation(ctx, compute.ID); err != nil {
		t.Fatalf("destroy compute: %v", err)
	}

	operations, err := service.ListOperations(ctx)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	for _, expected := range []struct {
		action       string
		resourceKind string
		resourceID   string
		status       string
	}{
		{"create_storage_volume", "storage_volume", volume.ID, "succeeded"},
		{"create_storage_attachment", "storage_attachment", attachment.ID, "succeeded"},
		{"create_workspace_runtime", "workspace_runtime", runtime.WorkspaceID, "succeeded"},
		{"detach_storage_attachment", "storage_attachment", attachment.ID, "succeeded"},
		{"destroy_storage_volume", "storage_volume", volume.ID, "succeeded"},
		{"destroy_compute_allocation", "compute_allocation", compute.ID, "succeeded"},
	} {
		assertOperationFact(t, operations, expected.action, expected.resourceKind, expected.resourceID, expected.status)
	}
}

func TestWorkspaceRuntimeAccessIsBusinessStateNotOperationPayload(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	ctx := context.Background()

	compute, err := service.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "access-compute"})
	if err != nil {
		t.Fatalf("create compute: %v", err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")
	volume, err := service.CreateStorageVolume(ctx, StorageVolumeInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", SizeGB: 10, IdempotencyKey: "access-storage"})
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	runtime, err := service.CreateWorkspaceRuntime(ctx, WorkspaceRuntimeInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, ImageID: "one-person-lab-app", IdempotencyKey: "access-runtime"})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if runtime.Access.Username != "admin" || runtime.Access.Password != "runtime-password-alpha" {
		t.Fatalf("runtime access not returned from business state: %#v", runtime.Access)
	}

	operations, err := service.ListOperations(ctx)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	for _, operation := range operations {
		payload := operation.RedactedProviderPayload
		if strings.Contains(strings.ToLower(fmt.Sprint(payload)), "runtime-password-alpha") {
			t.Fatalf("fabric operation leaked workspace password: %#v", operation)
		}
	}
}

func TestFabricRejectsIllegalResourceMutationsWithOperationFacts(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	ctx := context.Background()

	if _, err := service.DestroyComputeAllocation(ctx, "missing-compute"); err == nil {
		t.Fatalf("destroy missing compute must fail")
	}
	if _, err := service.CreateStorageAttachment(ctx, StorageAttachmentInput{WorkspaceID: "ws-alpha", ComputeID: "missing-compute", VolumeID: "missing-volume", IdempotencyKey: "reject-missing-attach"}); err == nil {
		t.Fatalf("attach missing compute/storage must fail")
	}

	compute, err := service.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "reject-compute"})
	if err != nil {
		t.Fatalf("create compute: %v", err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")
	volume, err := service.CreateStorageVolume(ctx, StorageVolumeInput{AccountID: "acct-beta", WorkspaceID: "ws-beta", SizeGB: 10, IdempotencyKey: "reject-storage"})
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	if _, err := service.CreateStorageAttachment(ctx, StorageAttachmentInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, IdempotencyKey: "reject-cross-account-attach"}); err == nil {
		t.Fatalf("attach cross-account compute/storage must fail")
	}

	operations, err := service.ListOperations(ctx)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	assertOperationFact(t, operations, "destroy_compute_allocation", "compute_allocation", "missing-compute", "rejected")
	assertOperationFact(t, operations, "create_storage_attachment", "storage_attachment", "reject-missing-attach", "rejected")
	assertOperationFact(t, operations, "create_storage_attachment", "storage_attachment", "reject-cross-account-attach", "rejected")
}

func TestServiceReplaysResourceStateFromOperationStore(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	original := NewServiceWithOperationStore(testProvider{}, store)

	compute, err := original.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "replay-compute"})
	if err != nil {
		t.Fatalf("create compute: %v", err)
	}
	waitForOperation(t, original, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")
	volume, err := original.CreateStorageVolume(ctx, StorageVolumeInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", SizeGB: 10, IdempotencyKey: "replay-storage"})
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}

	replayed := NewServiceWithOperationStore(testProvider{}, store)
	current, ok := replayed.GetComputeAllocation(ctx, compute.ID)
	if !ok || current.Status == "" || current.AccountID != "acct-alpha" {
		t.Fatalf("replayed compute = %#v ok=%v", current, ok)
	}
	attachment, err := replayed.CreateStorageAttachment(ctx, StorageAttachmentInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, IdempotencyKey: "replay-attach"})
	if err != nil {
		t.Fatalf("attach after replay: %v", err)
	}
	runtime, err := replayed.CreateWorkspaceRuntime(ctx, WorkspaceRuntimeInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, ImageID: "one-person-lab-app", IdempotencyKey: "replay-runtime"})
	if err != nil {
		t.Fatalf("runtime after replay: %v", err)
	}

	replayedAgain := NewServiceWithOperationStore(testProvider{}, store)
	if detached, err := replayedAgain.DetachStorageAttachment(ctx, attachment.ID); err != nil || detached.Status != "detached" {
		t.Fatalf("detach replayed attachment = %#v err=%v", detached, err)
	}
	status, err := replayedAgain.WorkspaceRuntimeStatus(ctx, runtime.WorkspaceID)
	if err != nil || status.Status != "running" {
		t.Fatalf("replayed runtime status = %#v err=%v", status, err)
	}
}

func waitForOperation(t *testing.T, service *Service, action string, resourceKind string, resourceID string, status string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		operations, err := service.ListOperations(context.Background())
		if err != nil {
			t.Fatalf("list operations: %v", err)
		}
		for _, operation := range operations {
			if operation.Action == action && operation.ResourceKind == resourceKind && operation.ResourceID == resourceID && operation.Status == status {
				if operation.OperationID == "" || operation.ProviderRequestID == "" || operation.RequestHash == "" || operation.StartedAt.IsZero() || operation.FinishedAt.IsZero() {
					t.Fatalf("operation missing audit fields: %#v", operation)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("missing operation %s/%s/%s/%s", action, resourceKind, resourceID, status)
}

func assertOperationFact(t *testing.T, operations []FabricOperation, action string, resourceKind string, resourceID string, status string) {
	t.Helper()
	for _, operation := range operations {
		if operation.Action != action || operation.ResourceKind != resourceKind || operation.ResourceID != resourceID || operation.Status != status {
			continue
		}
		if operation.OperationID == "" || operation.ProviderRequestID == "" || operation.RequestHash == "" || operation.StartedAt.IsZero() || operation.FinishedAt.IsZero() {
			t.Fatalf("operation missing audit fields: %#v", operation)
		}
		return
	}
	t.Fatalf("missing operation %s/%s/%s/%s in %#v", action, resourceKind, resourceID, status, operations)
}

type blockingProvider struct {
	testProvider
	done chan struct{}
}

func (p *blockingProvider) CreateComputeAllocation(ctx context.Context, input ComputeAllocationInput) (ComputeAllocation, error) {
	<-p.done
	return ComputeAllocation{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID, Status: "running", Provider: "tencent-tke", ProviderRequestID: providerRequestID("compute", input.IdempotencyKey), NodeName: "node-alpha", CreatedAt: time.Now().UTC()}, nil
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
	return WorkspaceRuntime{ID: "rt-test", WorkspaceID: input.WorkspaceID, Status: "running", ServiceName: "opl-ca-test", ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey), Access: RuntimeAccess{Username: "admin", Password: "runtime-password-alpha", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-ca-test-env"}}, nil
}

func (testProvider) WorkspaceRuntimeStatus(_ context.Context, workspaceID string) (WorkspaceRuntime, error) {
	return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "not_found"}, nil
}

func (testProvider) Readiness(_ context.Context) (map[string]any, error) {
	return map[string]any{"provider": "test", "ready": true}, nil
}
