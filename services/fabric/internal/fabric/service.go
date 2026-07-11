package fabric

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Provider interface {
	CreateComputeAllocation(ctx context.Context, input ComputeAllocationInput) (ComputeAllocation, error)
	SyncComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error)
	DestroyComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error)
	CreateStorageVolume(ctx context.Context, input StorageVolumeInput) (StorageVolume, error)
	SyncStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error)
	DestroyStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error)
	CreateStorageSnapshot(ctx context.Context, input StorageSnapshotInput, volume StorageVolume) (StorageSnapshot, error)
	SyncStorageSnapshot(ctx context.Context, snapshot StorageSnapshot) (StorageSnapshot, error)
	RestoreStorageSnapshot(ctx context.Context, input StorageRestoreInput, snapshot StorageSnapshot) (StorageVolume, error)
	DestroyStorageSnapshot(ctx context.Context, snapshot StorageSnapshot) (StorageSnapshot, error)
	CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput, compute ComputeAllocation, volume StorageVolume) (StorageAttachment, error)
	DetachStorageAttachment(ctx context.Context, attachment StorageAttachment) (StorageAttachment, error)
	CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, compute ComputeAllocation, volume StorageVolume) (WorkspaceRuntime, error)
	WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (WorkspaceRuntime, error)
	Readiness(ctx context.Context) (map[string]any, error)
}

type Service struct {
	provider    Provider
	mu          sync.Mutex
	jobMu       sync.Mutex
	computes    map[string]ComputeAllocation
	volumes     map[string]StorageVolume
	snapshots   map[string]StorageSnapshot
	attachments map[string]StorageAttachment
	runtimes    map[string]WorkspaceRuntime
	operations  OperationStore
	access      RuntimeAccessStore
	transfers   TransferStore
	now         func() time.Time
}

func NewService(provider Provider) *Service {
	return NewServiceWithOperationStore(provider, NewMemoryOperationStore())
}

func NewServiceWithOperationStore(provider Provider, operations OperationStore) *Service {
	if operations == nil {
		operations = NewMemoryOperationStore()
	}
	computes, volumes, snapshots, attachments, runtimes := replayResourceState(context.Background(), operations)
	accessStore, _ := operations.(RuntimeAccessStore)
	transferStore, _ := operations.(TransferStore)
	if transferStore == nil {
		transferStore = newMemoryTransferStore()
	}
	return &Service{provider: provider, computes: computes, volumes: volumes, snapshots: snapshots, attachments: attachments, runtimes: runtimes, operations: operations, access: accessStore, transfers: transferStore, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Catalog(_ context.Context) Catalog {
	return Catalog{
		SchemaVersion: 1,
		Owner:         "OPL Fabric",
		WorkspacePackages: []WorkspacePackage{
			{ID: "basic", Name: "Basic Workspace", ComputeProfileID: "cpu-basic", CPU: 2, MemoryGB: 4, DiskGB: 10, Provider: "tencent-tke", Available: true},
			{ID: "pro", Name: "Pro Workspace", ComputeProfileID: "cpu-pro", CPU: 8, MemoryGB: 16, DiskGB: 100, Provider: "tencent-tke", Available: true},
		},
		StorageClasses: []StorageClass{{ID: "workspace-cbs", StorageClassName: "cbs", Provider: "tencent-tke", Available: true}},
		IngressDomains: []IngressDomain{{ID: "workspace", Host: "workspace.medopl.cn", PathPattern: "/w/<workspaceId>/", Available: true}},
	}
}

func (s *Service) CreateComputeAllocation(ctx context.Context, input ComputeAllocationInput) (ComputeAllocation, error) {
	now := time.Now().UTC()
	id := firstNonEmpty(input.ID, fabricID("ca", firstNonEmpty(input.WorkspaceID, input.AccountID, "compute"), now))
	input.ID = id
	operation := newOperation("create_compute_allocation", "compute_allocation", id, input.AccountID, input.WorkspaceID, input.IdempotencyKey, hashInput(input), now)
	input.OperationID = operation.OperationID
	if err := s.recordOperation(ctx, operation, "started", ComputeAllocation{ID: id, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: firstNonEmpty(input.PackageID, "basic"), Status: "provisioning", Provider: "tencent-tke", ProviderRequestID: providerRequestID("compute", input.IdempotencyKey), CreatedAt: now}, nil); err != nil {
		return ComputeAllocation{}, err
	}
	allocation := ComputeAllocation{
		ID:                id,
		AccountID:         input.AccountID,
		WorkspaceID:       input.WorkspaceID,
		PackageID:         firstNonEmpty(input.PackageID, "basic"),
		Status:            "provisioning",
		Provider:          "tencent-tke",
		ProviderRequestID: providerRequestID("compute", input.IdempotencyKey),
		CreatedAt:         now,
	}
	s.mu.Lock()
	s.computes[allocation.ID] = allocation
	s.mu.Unlock()

	go s.finishComputeAllocation(input, id, allocation, operation)
	return allocation, nil
}

func (s *Service) GetComputeAllocation(_ context.Context, allocationID string) (ComputeAllocation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	allocation, ok := s.computes[allocationID]
	return allocation, ok
}

func (s *Service) SyncComputeAllocation(ctx context.Context, allocationID string) (ComputeAllocation, error) {
	s.mu.Lock()
	existing := s.computes[allocationID]
	s.mu.Unlock()
	if existing.ID == "" {
		operation := newOperation("sync_compute_allocation", "compute_allocation", allocationID, "", "", "", hashInput(map[string]string{"id": allocationID}), time.Now().UTC())
		operation.ProviderRequestID = providerRequestID("sync-compute", allocationID)
		err := fmt.Errorf("compute_allocation_not_found")
		_ = s.recordOperation(ctx, operation, "rejected", ComputeAllocation{ID: allocationID}, err)
		return ComputeAllocation{}, err
	}
	operation := newOperation("sync_compute_allocation", "compute_allocation", allocationID, existing.AccountID, existing.WorkspaceID, "", hashInput(existing), time.Now().UTC())
	if err := s.recordOperation(ctx, operation, "started", existing, nil); err != nil {
		return ComputeAllocation{}, err
	}
	allocation, err := s.provider.SyncComputeAllocation(ctx, existing)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", allocation, err)
		return allocation, err
	}
	if allocation.ID == "" {
		allocation.ID = existing.ID
	}
	if allocation.AccountID == "" {
		allocation.AccountID = existing.AccountID
	}
	if allocation.WorkspaceID == "" {
		allocation.WorkspaceID = existing.WorkspaceID
	}
	if allocation.PackageID == "" {
		allocation.PackageID = existing.PackageID
	}
	if allocation.Provider == "" {
		allocation.Provider = firstNonEmpty(existing.Provider, "tencent-tke")
	}
	if err := s.recordOperation(ctx, operation, "succeeded", allocation, nil); err != nil {
		return allocation, err
	}
	s.mu.Lock()
	s.computes[allocationID] = allocation
	s.mu.Unlock()
	return allocation, nil
}

func (s *Service) finishComputeAllocation(input ComputeAllocationInput, id string, pending ComputeAllocation, operation FabricOperation) {
	allocation, err := s.provider.CreateComputeAllocation(context.Background(), input)
	if err != nil {
		allocation = pending
		allocation.Status = "failed"
		allocation.ProviderRequestID = firstNonEmpty(allocation.ProviderRequestID, providerRequestID("compute", input.IdempotencyKey))
	} else {
		allocation.ID = id
		if allocation.Status == "" {
			allocation.Status = "running"
		}
		if allocation.Provider == "" {
			allocation.Provider = "tencent-tke"
		}
		if allocation.ProviderRequestID == "" {
			allocation.ProviderRequestID = providerRequestID("compute", input.IdempotencyKey)
		}
	}
	_ = s.recordOperation(context.Background(), operation, operationStatus(err), allocation, err)
	s.mu.Lock()
	s.computes[id] = allocation
	s.mu.Unlock()
}

func (s *Service) DestroyComputeAllocation(ctx context.Context, allocationID string) (ComputeAllocation, error) {
	s.mu.Lock()
	existing := s.computes[allocationID]
	s.mu.Unlock()
	if existing.ID == "" {
		operation := newOperation("destroy_compute_allocation", "compute_allocation", allocationID, "", "", "", hashInput(map[string]string{"id": allocationID}), time.Now().UTC())
		operation.ProviderRequestID = providerRequestID("destroy-compute", allocationID)
		err := fmt.Errorf("compute_allocation_not_found")
		_ = s.recordOperation(ctx, operation, "rejected", ComputeAllocation{ID: allocationID}, err)
		return ComputeAllocation{}, err
	}
	operation := newOperation("destroy_compute_allocation", "compute_allocation", allocationID, existing.AccountID, existing.WorkspaceID, "", hashInput(map[string]string{"id": allocationID}), time.Now().UTC())
	if err := s.recordOperation(ctx, operation, "started", existing, nil); err != nil {
		return ComputeAllocation{}, err
	}
	allocation, err := s.provider.DestroyComputeAllocation(ctx, existing)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", allocation, err)
		return allocation, err
	}
	if err := s.recordOperation(ctx, operation, "succeeded", allocation, nil); err != nil {
		return allocation, err
	}
	s.mu.Lock()
	s.computes[allocationID] = allocation
	s.mu.Unlock()
	return allocation, nil
}

func (s *Service) CreateStorageVolume(ctx context.Context, input StorageVolumeInput) (StorageVolume, error) {
	operation := newOperation("create_storage_volume", "storage_volume", firstNonEmpty(input.ID, "pending"), input.AccountID, input.WorkspaceID, input.IdempotencyKey, hashInput(input), time.Now().UTC())
	input.OperationID = operation.OperationID
	if err := s.recordOperation(ctx, operation, "started", StorageVolume{ID: operation.ResourceID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Provider: "tencent-tke", ProviderRequestID: providerRequestID("storage", input.IdempotencyKey)}, nil); err != nil {
		return StorageVolume{}, err
	}
	volume, err := s.provider.CreateStorageVolume(ctx, input)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", volume, err)
		return volume, err
	}
	if input.ID != "" {
		volume.ID = input.ID
	}
	operation.ResourceID = volume.ID
	if err := s.recordOperation(ctx, operation, "succeeded", volume, nil); err != nil {
		return volume, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.volumes[volume.ID] = volume
	return volume, nil
}

func (s *Service) GetStorageVolume(_ context.Context, volumeID string) (StorageVolume, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	volume, ok := s.volumes[volumeID]
	return volume, ok
}

func (s *Service) CreateStorageSnapshot(ctx context.Context, input StorageSnapshotInput) (StorageSnapshot, error) {
	if input.AccountID == "" || input.WorkspaceID == "" || input.VolumeID == "" || input.IdempotencyKey == "" {
		return StorageSnapshot{}, fmt.Errorf("storage_snapshot_input_required")
	}
	requestHash := hashInput(input)
	operations, err := s.operations.List(ctx)
	if err != nil {
		return StorageSnapshot{}, err
	}
	for _, operation := range operations {
		if operation.Action != "create_storage_snapshot" || operation.IdempotencyKey != input.IdempotencyKey {
			continue
		}
		if operation.RequestHash != requestHash {
			return StorageSnapshot{}, fmt.Errorf("storage_snapshot_idempotency_conflict")
		}
		var replayed StorageSnapshot
		if operation.Status == "succeeded" && decodeOperationResource(operation, &replayed) {
			return replayed, nil
		}
	}
	s.mu.Lock()
	volume := s.volumes[input.VolumeID]
	s.mu.Unlock()
	if volume.ID == "" || volume.Status != "ready" {
		return StorageSnapshot{}, fmt.Errorf("storage_volume_not_ready")
	}
	now := s.now()
	id := "snap-" + stableSuffix(input.WorkspaceID, input.VolumeID, input.IdempotencyKey)[:16]
	operation := newOperation("create_storage_snapshot", "storage_snapshot", id, input.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	input.OperationID = operation.OperationID
	if err := s.recordOperation(ctx, operation, "started", StorageSnapshot{ID: id, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, VolumeID: input.VolumeID, Status: "creating", Provider: "tencent-tke", ProviderRequestID: providerRequestID("snapshot", input.IdempotencyKey), CreatedAt: now}, nil); err != nil {
		return StorageSnapshot{}, err
	}
	snapshot, err := s.provider.CreateStorageSnapshot(ctx, input, volume)
	if snapshot.ID == "" {
		snapshot.ID = id
	}
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", snapshot, err)
		return snapshot, err
	}
	operation.ResourceID = snapshot.ID
	if err := s.recordOperation(ctx, operation, "succeeded", snapshot, nil); err != nil {
		return snapshot, err
	}
	s.mu.Lock()
	s.snapshots[snapshot.ID] = snapshot
	s.mu.Unlock()
	return snapshot, nil
}

func (s *Service) GetStorageSnapshot(_ context.Context, snapshotID string) (StorageSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.snapshots[snapshotID]
	return snapshot, ok
}

func (s *Service) SyncStorageSnapshot(ctx context.Context, snapshotID string) (StorageSnapshot, error) {
	s.mu.Lock()
	snapshot := s.snapshots[snapshotID]
	s.mu.Unlock()
	if snapshot.ID == "" {
		return StorageSnapshot{}, fmt.Errorf("storage_snapshot_not_found")
	}
	operation := newOperation("sync_storage_snapshot", "storage_snapshot", snapshotID, snapshot.AccountID, snapshot.WorkspaceID, "", hashInput(map[string]string{"id": snapshotID}), s.now())
	if err := s.recordOperation(ctx, operation, "started", snapshot, nil); err != nil {
		return StorageSnapshot{}, err
	}
	synced, err := s.provider.SyncStorageSnapshot(ctx, snapshot)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", synced, err)
		return synced, err
	}
	if err := s.recordOperation(ctx, operation, "succeeded", synced, nil); err != nil {
		return synced, err
	}
	s.mu.Lock()
	s.snapshots[snapshotID] = synced
	s.mu.Unlock()
	return synced, nil
}

func (s *Service) RestoreStorageSnapshot(ctx context.Context, input StorageRestoreInput) (StorageVolume, error) {
	if input.SnapshotID == "" || input.AccountID == "" || input.WorkspaceID == "" || input.TargetVolumeID == "" || input.IdempotencyKey == "" {
		return StorageVolume{}, fmt.Errorf("storage_restore_input_required")
	}
	s.mu.Lock()
	snapshot := s.snapshots[input.SnapshotID]
	s.mu.Unlock()
	if snapshot.ID == "" || snapshot.Status != "ready" {
		return StorageVolume{}, fmt.Errorf("storage_snapshot_not_ready")
	}
	operation := newOperation("restore_storage_snapshot", "storage_volume", input.TargetVolumeID, input.AccountID, input.WorkspaceID, input.IdempotencyKey, hashInput(input), s.now())
	input.OperationID = operation.OperationID
	if err := s.recordOperation(ctx, operation, "started", StorageVolume{ID: input.TargetVolumeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "restoring", Provider: snapshot.Provider, ProviderRequestID: providerRequestID("restore", input.IdempotencyKey)}, nil); err != nil {
		return StorageVolume{}, err
	}
	volume, err := s.provider.RestoreStorageSnapshot(ctx, input, snapshot)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", volume, err)
		return volume, err
	}
	if err := s.recordOperation(ctx, operation, "succeeded", volume, nil); err != nil {
		return volume, err
	}
	s.mu.Lock()
	s.volumes[volume.ID] = volume
	s.mu.Unlock()
	return volume, nil
}

func (s *Service) DestroyStorageSnapshot(ctx context.Context, snapshotID string) (StorageSnapshot, error) {
	s.mu.Lock()
	snapshot := s.snapshots[snapshotID]
	s.mu.Unlock()
	if snapshot.ID == "" {
		return StorageSnapshot{}, fmt.Errorf("storage_snapshot_not_found")
	}
	operation := newOperation("destroy_storage_snapshot", "storage_snapshot", snapshotID, snapshot.AccountID, snapshot.WorkspaceID, "", hashInput(map[string]string{"id": snapshotID}), s.now())
	if err := s.recordOperation(ctx, operation, "started", snapshot, nil); err != nil {
		return StorageSnapshot{}, err
	}
	destroyed, err := s.provider.DestroyStorageSnapshot(ctx, snapshot)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", destroyed, err)
		return destroyed, err
	}
	if err := s.recordOperation(ctx, operation, "succeeded", destroyed, nil); err != nil {
		return destroyed, err
	}
	s.mu.Lock()
	s.snapshots[snapshotID] = destroyed
	s.mu.Unlock()
	return destroyed, nil
}

func (s *Service) DestroyStorageVolume(ctx context.Context, volumeID string) (StorageVolume, error) {
	s.mu.Lock()
	existing := s.volumes[volumeID]
	s.mu.Unlock()
	if existing.ID == "" {
		operation := newOperation("destroy_storage_volume", "storage_volume", volumeID, "", "", "", hashInput(map[string]string{"id": volumeID}), time.Now().UTC())
		operation.ProviderRequestID = providerRequestID("destroy-storage", volumeID)
		err := fmt.Errorf("storage_volume_not_found")
		_ = s.recordOperation(ctx, operation, "rejected", StorageVolume{ID: volumeID}, err)
		return StorageVolume{}, err
	}
	operation := newOperation("destroy_storage_volume", "storage_volume", volumeID, existing.AccountID, existing.WorkspaceID, "", hashInput(map[string]string{"id": volumeID}), time.Now().UTC())
	if err := s.recordOperation(ctx, operation, "started", existing, nil); err != nil {
		return StorageVolume{}, err
	}
	volume, err := s.provider.DestroyStorageVolume(ctx, existing)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", volume, err)
		return volume, err
	}
	if err := s.recordOperation(ctx, operation, "succeeded", volume, nil); err != nil {
		return volume, err
	}
	s.mu.Lock()
	s.volumes[volumeID] = volume
	s.mu.Unlock()
	return volume, nil
}

func (s *Service) SyncStorageVolume(ctx context.Context, volumeID string) (StorageVolume, error) {
	s.mu.Lock()
	existing := s.volumes[volumeID]
	s.mu.Unlock()
	if existing.ID == "" {
		operation := newOperation("sync_storage_volume", "storage_volume", volumeID, "", "", "", hashInput(map[string]string{"id": volumeID}), time.Now().UTC())
		operation.ProviderRequestID = providerRequestID("sync-storage", volumeID)
		err := fmt.Errorf("storage_volume_not_found")
		_ = s.recordOperation(ctx, operation, "rejected", StorageVolume{ID: volumeID}, err)
		return StorageVolume{}, err
	}
	operation := newOperation("sync_storage_volume", "storage_volume", volumeID, existing.AccountID, existing.WorkspaceID, "", hashInput(existing), time.Now().UTC())
	if err := s.recordOperation(ctx, operation, "started", existing, nil); err != nil {
		return StorageVolume{}, err
	}
	volume, err := s.provider.SyncStorageVolume(ctx, existing)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", volume, err)
		return volume, err
	}
	if volume.ID == "" {
		volume.ID = existing.ID
	}
	if volume.AccountID == "" {
		volume.AccountID = existing.AccountID
	}
	if volume.WorkspaceID == "" {
		volume.WorkspaceID = existing.WorkspaceID
	}
	if volume.Provider == "" {
		volume.Provider = firstNonEmpty(existing.Provider, "tencent-tke")
	}
	if err := s.recordOperation(ctx, operation, "succeeded", volume, nil); err != nil {
		return volume, err
	}
	s.mu.Lock()
	s.volumes[volumeID] = volume
	s.mu.Unlock()
	return volume, nil
}

func (s *Service) CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput) (StorageAttachment, error) {
	s.mu.Lock()
	compute := s.computes[input.ComputeID]
	volume := s.volumes[input.VolumeID]
	s.mu.Unlock()
	operation := newOperation("create_storage_attachment", "storage_attachment", firstNonEmpty(input.IdempotencyKey, input.WorkspaceID, input.ComputeID, input.VolumeID, "pending"), compute.AccountID, input.WorkspaceID, input.IdempotencyKey, hashInput(input), time.Now().UTC())
	input.OperationID = operation.OperationID
	if err := validateAttachmentInput(input, compute, volume); err != nil {
		operation.ProviderRequestID = providerRequestID("storage-attach", input.IdempotencyKey)
		_ = s.recordOperation(ctx, operation, "rejected", StorageAttachment{ID: operation.ResourceID, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, ProviderRequestID: operation.ProviderRequestID}, err)
		return StorageAttachment{}, err
	}
	if err := s.recordOperation(ctx, operation, "started", StorageAttachment{ID: operation.ResourceID, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Provider: "tencent-tke", ProviderRequestID: providerRequestID("storage-attach", input.IdempotencyKey)}, nil); err != nil {
		return StorageAttachment{}, err
	}
	attachment, err := s.provider.CreateStorageAttachment(ctx, input, compute, volume)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", attachment, err)
		return attachment, err
	}
	operation.ResourceID = attachment.ID
	if err := s.recordOperation(ctx, operation, "succeeded", attachment, nil); err != nil {
		return attachment, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attachments[attachment.ID] = attachment
	return attachment, nil
}

func (s *Service) DetachStorageAttachment(ctx context.Context, attachmentID string) (StorageAttachment, error) {
	s.mu.Lock()
	existing := s.attachments[attachmentID]
	s.mu.Unlock()
	if existing.ID == "" {
		operation := newOperation("detach_storage_attachment", "storage_attachment", attachmentID, "", "", "", hashInput(map[string]string{"id": attachmentID}), time.Now().UTC())
		operation.ProviderRequestID = providerRequestID("detach-attachment", attachmentID)
		err := fmt.Errorf("storage_attachment_not_found")
		_ = s.recordOperation(ctx, operation, "rejected", StorageAttachment{ID: attachmentID}, err)
		return StorageAttachment{}, err
	}
	operation := newOperation("detach_storage_attachment", "storage_attachment", attachmentID, "", existing.WorkspaceID, "", hashInput(map[string]string{"id": attachmentID}), time.Now().UTC())
	if err := s.recordOperation(ctx, operation, "started", existing, nil); err != nil {
		return StorageAttachment{}, err
	}
	attachment, err := s.provider.DetachStorageAttachment(ctx, existing)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", attachment, err)
		return attachment, err
	}
	if err := s.recordOperation(ctx, operation, "succeeded", attachment, nil); err != nil {
		return attachment, err
	}
	s.mu.Lock()
	s.attachments[attachmentID] = attachment
	s.mu.Unlock()
	return attachment, nil
}

func (s *Service) CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput) (WorkspaceRuntime, error) {
	s.mu.Lock()
	compute := s.computes[input.ComputeID]
	volume := s.volumes[input.VolumeID]
	s.mu.Unlock()
	operation := newOperation("create_workspace_runtime", "workspace_runtime", input.WorkspaceID, compute.AccountID, input.WorkspaceID, input.IdempotencyKey, hashInput(input), time.Now().UTC())
	input.OperationID = operation.OperationID
	if err := validateRuntimeInput(input, compute, volume); err != nil {
		operation.ProviderRequestID = providerRequestID("runtime", input.IdempotencyKey)
		_ = s.recordOperation(ctx, operation, "rejected", WorkspaceRuntime{WorkspaceID: input.WorkspaceID, ProviderRequestID: operation.ProviderRequestID}, err)
		return WorkspaceRuntime{}, err
	}
	if err := s.recordOperation(ctx, operation, "started", WorkspaceRuntime{WorkspaceID: input.WorkspaceID, ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey)}, nil); err != nil {
		return WorkspaceRuntime{}, err
	}
	runtime, err := s.provider.CreateWorkspaceRuntime(ctx, input, compute, volume)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", runtime, err)
		return runtime, err
	}
	if err := s.saveRuntimeAccess(ctx, &runtime); err != nil {
		return runtime, err
	}
	if err := s.recordOperation(ctx, operation, "succeeded", runtime, nil); err != nil {
		return runtime, err
	}
	s.mu.Lock()
	s.runtimes[input.WorkspaceID] = runtime
	s.mu.Unlock()
	return runtime, nil
}

func (s *Service) WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (WorkspaceRuntime, error) {
	runtime, err := s.provider.WorkspaceRuntimeStatus(ctx, workspaceID)
	if err != nil {
		return runtime, err
	}
	if runtime.Status != "not_found" {
		_ = s.attachRuntimeAccess(ctx, &runtime)
		return runtime, nil
	}
	s.mu.Lock()
	if existing, ok := s.runtimes[workspaceID]; ok {
		s.mu.Unlock()
		_ = s.attachRuntimeAccess(ctx, &existing)
		return existing, nil
	}
	s.mu.Unlock()
	return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "not_found"}, nil
}

func (s *Service) Readiness(ctx context.Context) (map[string]any, error) {
	return s.provider.Readiness(ctx)
}

func (s *Service) ListOperations(ctx context.Context) ([]FabricOperation, error) {
	return s.operations.List(ctx)
}

func (s *Service) CreateJob(ctx context.Context, input JobInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if input.OrganizationID == "" || input.WorkspaceID == "" || input.ProjectID == "" || input.TaskID == "" || input.RequestID == "" || input.ApprovalID == "" || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(input)
	operations, err := s.operations.List(ctx)
	if err != nil {
		return Job{}, err
	}
	// ponytail: linear scan is enough for the initial job volume; add an indexed store query when measured throughput requires it.
	for _, operation := range operations {
		if operation.ResourceKind != "job" || operation.Action != "create_job" || operation.IdempotencyKey != input.IdempotencyKey {
			continue
		}
		if operation.RequestHash != requestHash {
			return Job{}, ErrJobIdempotencyConflict
		}
		var job Job
		if decodeOperationResource(operation, &job) {
			job.Replayed = true
			return job, nil
		}
	}
	now := s.now()
	job := Job{
		JobID:          "job-" + stableSuffix(input.IdempotencyKey, input.RequestID, input.TaskID)[:16],
		OrganizationID: input.OrganizationID,
		WorkspaceID:    input.WorkspaceID,
		ProjectID:      input.ProjectID,
		TaskID:         input.TaskID,
		RequestID:      input.RequestID,
		ApprovalID:     input.ApprovalID,
		EnvironmentRef: input.EnvironmentRef,
		Status:         "queued",
		Attempt:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	operation := newOperation("create_job", "job", job.JobID, "", input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	operation.ProviderRequestID = job.JobID
	if err := s.recordOperation(ctx, operation, job.Status, job, nil); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) Job(ctx context.Context, jobID string) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	return s.jobLocked(ctx, jobID, true)
}

func (s *Service) jobLocked(ctx context.Context, jobID string, expire bool) (Job, error) {
	operations, err := s.operations.List(ctx)
	if err != nil {
		return Job{}, err
	}
	var job Job
	leaseTokenHash := ""
	found := false
	for _, operation := range operations {
		if operation.ResourceKind == "job" && operation.ResourceID == jobID && decodeOperationResource(operation, &job) {
			found = true
			leaseTokenHash, _ = operation.RedactedProviderPayload["leaseTokenHash"].(string)
		}
	}
	if !found {
		return Job{}, ErrJobNotFound
	}
	job.leaseTokenHash = leaseTokenHash
	if expire && job.Status == "running" && job.LeaseExpiresAt != nil && !s.now().Before(*job.LeaseExpiresAt) {
		job.Status = "timed_out"
		job.ErrorCode = "lease_expired"
		job.UpdatedAt = s.now()
		if err := s.appendJobTransition(ctx, "timeout_job", "timeout-"+job.JobID+fmt.Sprintf("-%d", job.Attempt), hashInput(map[string]any{"jobId": job.JobID, "attempt": job.Attempt}), job, "runner"); err != nil {
			return Job{}, err
		}
	}
	return job, nil
}

func (s *Service) CancelJob(ctx context.Context, jobID string, idempotencyKey string) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if idempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID})
	if replayed, ok, err := s.replayedJobTransition(ctx, "cancel_job", jobID, idempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.jobLocked(ctx, jobID, true)
	if err != nil {
		return Job{}, err
	}
	if job.Status == "cancelled" {
		job.Replayed = true
		return job, nil
	}
	if job.Status != "queued" && job.Status != "running" {
		return Job{}, ErrJobStateConflict
	}
	now := s.now()
	job.Status = "cancelled"
	job.UpdatedAt = now
	if err := s.appendJobTransition(ctx, "cancel_job", idempotencyKey, requestHash, job, "control-plane"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) ClaimJob(ctx context.Context, jobID string, input JobClaimInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || input.RunnerID == "" || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID, "runnerId": input.RunnerID})
	if replayed, ok, err := s.replayedJobTransition(ctx, "claim_job", jobID, input.IdempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.jobLocked(ctx, jobID, true)
	if err != nil {
		return Job{}, err
	}
	if job.Status != "queued" {
		return Job{}, ErrJobStateConflict
	}
	now := s.now()
	token, err := newLeaseToken()
	if err != nil {
		return Job{}, err
	}
	expiresAt := now.Add(30 * time.Second)
	job.Status = "running"
	job.LeaseOwner = input.RunnerID
	job.LeaseExpiresAt = &expiresAt
	job.LeaseToken = token
	job.leaseTokenHash = stableSuffix(token)
	job.UpdatedAt = now
	if err := s.appendJobTransition(ctx, "claim_job", input.IdempotencyKey, requestHash, job, "runner"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) HeartbeatJob(ctx context.Context, jobID string, input JobHeartbeatInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || input.RunnerID == "" || input.LeaseToken == "" || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID, "runnerId": input.RunnerID, "leaseTokenHash": stableSuffix(input.LeaseToken)})
	if replayed, ok, err := s.replayedJobTransition(ctx, "heartbeat_job", jobID, input.IdempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.activeLeasedJob(ctx, jobID, input.RunnerID, input.LeaseToken)
	if err != nil {
		return Job{}, err
	}
	now := s.now()
	expiresAt := now.Add(30 * time.Second)
	job.LeaseExpiresAt = &expiresAt
	job.UpdatedAt = now
	if err := s.appendJobTransition(ctx, "heartbeat_job", input.IdempotencyKey, requestHash, job, "runner"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) CompleteJob(ctx context.Context, jobID string, input JobCompleteInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || input.RunnerID == "" || input.LeaseToken == "" || len(input.ArtifactIDs) == 0 || len(input.ReviewIDs) == 0 || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(struct {
		JobID, RunnerID, LeaseTokenHash string
		ArtifactIDs, ReviewIDs          []string
	}{jobID, input.RunnerID, stableSuffix(input.LeaseToken), input.ArtifactIDs, input.ReviewIDs})
	if replayed, ok, err := s.replayedJobTransition(ctx, "complete_job", jobID, input.IdempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.activeLeasedJob(ctx, jobID, input.RunnerID, input.LeaseToken)
	if err != nil {
		return Job{}, err
	}
	job.Status = "succeeded"
	job.ArtifactIDs = append([]string(nil), input.ArtifactIDs...)
	job.ReviewIDs = append([]string(nil), input.ReviewIDs...)
	job.ErrorCode = ""
	job.UpdatedAt = s.now()
	if err := s.appendJobTransition(ctx, "complete_job", input.IdempotencyKey, requestHash, job, "runner"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) FailJob(ctx context.Context, jobID string, input JobFailInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || input.RunnerID == "" || input.LeaseToken == "" || input.ErrorCode == "" || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID, "runnerId": input.RunnerID, "leaseTokenHash": stableSuffix(input.LeaseToken), "errorCode": input.ErrorCode})
	if replayed, ok, err := s.replayedJobTransition(ctx, "fail_job", jobID, input.IdempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.activeLeasedJob(ctx, jobID, input.RunnerID, input.LeaseToken)
	if err != nil {
		return Job{}, err
	}
	job.Status = "failed"
	job.ErrorCode = input.ErrorCode
	job.UpdatedAt = s.now()
	if err := s.appendJobTransition(ctx, "fail_job", input.IdempotencyKey, requestHash, job, "runner"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) RetryJob(ctx context.Context, jobID, idempotencyKey string) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || idempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID})
	if replayed, ok, err := s.replayedJobTransition(ctx, "retry_job", jobID, idempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.jobLocked(ctx, jobID, true)
	if err != nil {
		return Job{}, err
	}
	if job.Status != "failed" && job.Status != "timed_out" {
		return Job{}, ErrJobStateConflict
	}
	job.Status = "queued"
	job.Attempt++
	job.LeaseOwner = ""
	job.LeaseExpiresAt = nil
	job.LeaseToken = ""
	job.leaseTokenHash = ""
	job.ArtifactIDs = nil
	job.ReviewIDs = nil
	job.ErrorCode = ""
	job.UpdatedAt = s.now()
	if err := s.appendJobTransition(ctx, "retry_job", idempotencyKey, requestHash, job, "control-plane"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) activeLeasedJob(ctx context.Context, jobID, runnerID, leaseToken string) (Job, error) {
	job, err := s.jobLocked(ctx, jobID, true)
	if err != nil {
		return Job{}, err
	}
	if job.Status != "running" {
		return Job{}, ErrJobStateConflict
	}
	if job.LeaseOwner != runnerID || subtle.ConstantTimeCompare([]byte(job.leaseTokenHash), []byte(stableSuffix(leaseToken))) != 1 {
		return Job{}, ErrJobLeaseMismatch
	}
	return job, nil
}

func (s *Service) replayedJobTransition(ctx context.Context, action, jobID, idempotencyKey, requestHash string) (Job, bool, error) {
	operations, err := s.operations.List(ctx)
	if err != nil {
		return Job{}, false, err
	}
	for _, operation := range operations {
		if operation.ResourceKind != "job" || operation.ResourceID != jobID || operation.Action != action || operation.IdempotencyKey != idempotencyKey {
			continue
		}
		if operation.RequestHash != requestHash {
			return Job{}, false, ErrJobIdempotencyConflict
		}
		var job Job
		if decodeOperationResource(operation, &job) {
			job.Replayed = true
			return job, true, nil
		}
	}
	return Job{}, false, nil
}

func (s *Service) appendJobTransition(ctx context.Context, action, idempotencyKey, requestHash string, job Job, caller string) error {
	operation := newOperation(action, "job", job.JobID, "", job.WorkspaceID, idempotencyKey, requestHash, s.now())
	operation.ProviderRequestID = job.JobID
	operation.CallerService = caller
	return s.recordOperation(ctx, operation, job.Status, job, nil)
}

func newLeaseToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "lease-" + hex.EncodeToString(data), nil
}

func (s *Service) saveRuntimeAccess(ctx context.Context, runtime *WorkspaceRuntime) error {
	if runtime.Access.CredentialStatus == "" && runtime.Access.Password != "" {
		runtime.Access.CredentialStatus = "configured"
	}
	if runtime.Access.CredentialVersion == "" && runtime.Access.Password != "" {
		runtime.Access.CredentialVersion = "v1"
	}
	if runtime.Access.UpdatedAt.IsZero() && runtime.Access.Password != "" {
		runtime.Access.UpdatedAt = time.Now().UTC()
	}
	if s.access == nil {
		return nil
	}
	return s.access.SaveRuntimeAccess(ctx, *runtime)
}

func (s *Service) attachRuntimeAccess(ctx context.Context, runtime *WorkspaceRuntime) error {
	if runtime.Access.Password != "" || s.access == nil {
		return nil
	}
	access, ok, err := s.access.RuntimeAccess(ctx, runtime.WorkspaceID)
	if err != nil || !ok {
		return err
	}
	runtime.Access = access
	return nil
}

func replayResourceState(ctx context.Context, operations OperationStore) (map[string]ComputeAllocation, map[string]StorageVolume, map[string]StorageSnapshot, map[string]StorageAttachment, map[string]WorkspaceRuntime) {
	computes := map[string]ComputeAllocation{}
	volumes := map[string]StorageVolume{}
	snapshots := map[string]StorageSnapshot{}
	attachments := map[string]StorageAttachment{}
	runtimes := map[string]WorkspaceRuntime{}
	records, err := operations.List(ctx)
	if err != nil {
		return computes, volumes, snapshots, attachments, runtimes
	}
	for _, operation := range records {
		switch operation.ResourceKind {
		case "compute_allocation":
			var resource ComputeAllocation
			if !decodeOperationResource(operation, &resource) {
				continue
			}
			if operation.Status == "started" && operation.Action != "create_compute_allocation" {
				continue
			}
			if operation.Status == "failed" && !strings.HasPrefix(operation.Action, "create_") {
				continue
			}
			computes[resource.ID] = resource
		case "storage_volume":
			var resource StorageVolume
			if operation.Status != "succeeded" || !decodeOperationResource(operation, &resource) {
				continue
			}
			volumes[resource.ID] = resource
		case "storage_snapshot":
			var resource StorageSnapshot
			if operation.Status != "succeeded" || !decodeOperationResource(operation, &resource) {
				continue
			}
			snapshots[resource.ID] = resource
		case "storage_attachment":
			var resource StorageAttachment
			if operation.Status != "succeeded" || !decodeOperationResource(operation, &resource) {
				continue
			}
			attachments[resource.ID] = resource
		case "workspace_runtime":
			var resource WorkspaceRuntime
			if operation.Status != "succeeded" || !decodeOperationResource(operation, &resource) {
				continue
			}
			runtimes[resource.WorkspaceID] = resource
		}
	}
	return computes, volumes, snapshots, attachments, runtimes
}

func decodeOperationResource(operation FabricOperation, target any) bool {
	resource, ok := operation.RedactedProviderPayload["resource"]
	if !ok {
		return false
	}
	data, err := json.Marshal(resource)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, target) == nil
}

func newOperation(action string, resourceKind string, resourceID string, accountID string, workspaceID string, idempotencyKey string, requestHash string, now time.Time) FabricOperation {
	operationID := "op_" + action + "_" + stableSuffix(firstNonEmpty(idempotencyKey, resourceID, accountID, workspaceID, fmt.Sprintf("%d", now.UnixNano())), resourceKind, action)[:12]
	return FabricOperation{
		OperationID:    operationID,
		CallerService:  "control-plane",
		Action:         action,
		ResourceKind:   resourceKind,
		ResourceID:     resourceID,
		AccountID:      accountID,
		WorkspaceID:    workspaceID,
		IdempotencyKey: idempotencyKey,
		RequestHash:    requestHash,
		StartedAt:      now,
	}
}

func (s *Service) recordOperation(ctx context.Context, base FabricOperation, status string, resource any, operationErr error) error {
	now := s.now()
	operation := base
	operation.ID = fabricID("fop", firstNonEmpty(base.OperationID, base.ResourceID)+"_"+status, now)
	operation.Status = status
	operation.CreatedAt = now
	if status != "started" {
		operation.FinishedAt = now
	}
	if operationErr != nil {
		operation.ErrorCode = errorCode(operationErr)
	}
	fillOperationResource(&operation, resource)
	return s.operations.Append(ctx, operation)
}

func fillOperationResource(operation *FabricOperation, resource any) {
	switch value := resource.(type) {
	case ComputeAllocation:
		operation.ResourceID = firstNonEmpty(value.ID, operation.ResourceID)
		operation.AccountID = firstNonEmpty(value.AccountID, operation.AccountID)
		operation.WorkspaceID = firstNonEmpty(value.WorkspaceID, operation.WorkspaceID)
		operation.Provider = firstNonEmpty(value.Provider, operation.Provider)
		operation.ProviderRequestID = firstNonEmpty(value.ProviderRequestID, operation.ProviderRequestID)
		operation.RedactedProviderPayload = map[string]any{"resource": value, "providerResourceId": value.ProviderResourceID, "nodeName": value.NodeName, "instanceId": firstNonEmpty(value.CVMInstanceID, value.InstanceID), "costTags": value.CostTags}
	case StorageVolume:
		operation.ResourceID = firstNonEmpty(value.ID, operation.ResourceID)
		operation.AccountID = firstNonEmpty(value.AccountID, operation.AccountID)
		operation.WorkspaceID = firstNonEmpty(value.WorkspaceID, operation.WorkspaceID)
		operation.Provider = firstNonEmpty(value.Provider, operation.Provider)
		operation.ProviderRequestID = firstNonEmpty(value.ProviderRequestID, operation.ProviderRequestID)
		operation.RedactedProviderPayload = map[string]any{"resource": value, "providerResourceId": value.ProviderResourceID, "storageClass": value.StorageClass, "sizeGb": value.SizeGB, "costTags": value.CostTags}
	case StorageSnapshot:
		operation.ResourceID = firstNonEmpty(value.ID, operation.ResourceID)
		operation.AccountID = firstNonEmpty(value.AccountID, operation.AccountID)
		operation.WorkspaceID = firstNonEmpty(value.WorkspaceID, operation.WorkspaceID)
		operation.Provider = firstNonEmpty(value.Provider, operation.Provider)
		operation.ProviderRequestID = firstNonEmpty(value.ProviderRequestID, operation.ProviderRequestID)
		operation.RedactedProviderPayload = map[string]any{"resource": value, "providerSnapshotRef": value.ProviderSnapshotRef, "volumeId": value.VolumeID, "snapshotClass": value.SnapshotClass}
	case StorageAttachment:
		operation.ResourceID = firstNonEmpty(value.ID, operation.ResourceID)
		operation.WorkspaceID = firstNonEmpty(value.WorkspaceID, operation.WorkspaceID)
		operation.Provider = firstNonEmpty(value.Provider, operation.Provider)
		operation.ProviderRequestID = firstNonEmpty(value.ProviderRequestID, operation.ProviderRequestID)
		operation.RedactedProviderPayload = map[string]any{"resource": value, "providerAttachmentId": value.ProviderAttachmentID, "computeId": value.ComputeID, "volumeId": value.VolumeID, "costTags": value.CostTags}
	case WorkspaceRuntime:
		redacted := value
		if redacted.Access.Password != "" {
			redacted.Access.Password = ""
			redacted.Access.CredentialStatus = firstNonEmpty(redacted.Access.CredentialStatus, "configured")
		}
		operation.ResourceID = firstNonEmpty(value.WorkspaceID, operation.ResourceID)
		operation.WorkspaceID = firstNonEmpty(value.WorkspaceID, operation.WorkspaceID)
		operation.ProviderRequestID = firstNonEmpty(value.ProviderRequestID, operation.ProviderRequestID)
		operation.RedactedProviderPayload = map[string]any{"resource": redacted, "serviceName": value.ServiceName, "ready": value.Ready, "credentialConfigured": value.Access.Password != "", "credentialVersion": value.Access.CredentialVersion, "secretRef": value.Access.SecretRef, "costTags": value.CostTags}
	case Job:
		redacted := value
		redacted.LeaseToken = ""
		redacted.leaseTokenHash = ""
		operation.ResourceID = value.JobID
		operation.WorkspaceID = value.WorkspaceID
		operation.ProviderRequestID = firstNonEmpty(operation.ProviderRequestID, value.JobID)
		operation.RedactedProviderPayload = map[string]any{"resource": redacted, "leaseTokenHash": value.leaseTokenHash}
	}
}

func operationStatus(err error) string {
	if err != nil {
		return "failed"
	}
	return "succeeded"
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return "provider_error"
	}
	return strings.Fields(text)[0]
}

func validateAttachmentInput(input StorageAttachmentInput, compute ComputeAllocation, volume StorageVolume) error {
	if compute.ID == "" {
		return fmt.Errorf("compute_allocation_not_found")
	}
	if volume.ID == "" {
		return fmt.Errorf("storage_volume_not_found")
	}
	if compute.AccountID == "" || volume.AccountID == "" || compute.AccountID != volume.AccountID {
		return fmt.Errorf("resource_account_mismatch")
	}
	if isTerminalResourceStatus(compute.Status) || isTerminalResourceStatus(volume.Status) {
		return fmt.Errorf("resource_status_invalid")
	}
	return nil
}

func validateRuntimeInput(input WorkspaceRuntimeInput, compute ComputeAllocation, volume StorageVolume) error {
	if compute.ID == "" {
		return fmt.Errorf("compute_allocation_not_found")
	}
	if volume.ID == "" {
		return fmt.Errorf("storage_volume_not_found")
	}
	if compute.AccountID == "" || volume.AccountID == "" || compute.AccountID != volume.AccountID {
		return fmt.Errorf("resource_account_mismatch")
	}
	if isTerminalResourceStatus(compute.Status) || isTerminalResourceStatus(volume.Status) {
		return fmt.Errorf("resource_status_invalid")
	}
	return nil
}

func isTerminalResourceStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "destroyed", "deleted", "failed", "detached", "unrecoverable":
		return true
	default:
		return false
	}
}

func hashInput(input any) string {
	data, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func stableSuffix(values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, ":")))
	return hex.EncodeToString(sum[:])
}

func fabricID(prefix string, owner string, now time.Time) string {
	return fmt.Sprintf("%s_%s_%d", prefix, owner, now.UnixNano())
}

func providerRequestID(prefix string, key string) string {
	if key == "" {
		key = "no-idempotency-key"
	}
	return fmt.Sprintf("%s_%s", prefix, key)
}
