package fabric

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type Provider interface {
	CreateComputeAllocation(ctx context.Context, input ComputeAllocationInput) (ComputeAllocation, error)
	DestroyComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error)
	CreateStorageVolume(ctx context.Context, input StorageVolumeInput) (StorageVolume, error)
	DestroyStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error)
	CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput, compute ComputeAllocation, volume StorageVolume) (StorageAttachment, error)
	DetachStorageAttachment(ctx context.Context, attachment StorageAttachment) (StorageAttachment, error)
	CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, compute ComputeAllocation, volume StorageVolume) (WorkspaceRuntime, error)
	WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (WorkspaceRuntime, error)
	Readiness(ctx context.Context) (map[string]any, error)
}

type Service struct {
	provider    Provider
	mu          sync.Mutex
	computes    map[string]ComputeAllocation
	volumes     map[string]StorageVolume
	attachments map[string]StorageAttachment
	runtimes    map[string]WorkspaceRuntime
}

func NewService(provider Provider) *Service {
	return &Service{provider: provider, computes: map[string]ComputeAllocation{}, volumes: map[string]StorageVolume{}, attachments: map[string]StorageAttachment{}, runtimes: map[string]WorkspaceRuntime{}}
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

	go s.finishComputeAllocation(input, id, allocation)
	return allocation, nil
}

func (s *Service) GetComputeAllocation(_ context.Context, allocationID string) (ComputeAllocation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	allocation, ok := s.computes[allocationID]
	return allocation, ok
}

func (s *Service) finishComputeAllocation(input ComputeAllocationInput, id string, pending ComputeAllocation) {
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
	s.mu.Lock()
	s.computes[id] = allocation
	s.mu.Unlock()
}

func (s *Service) DestroyComputeAllocation(ctx context.Context, allocationID string) (ComputeAllocation, error) {
	s.mu.Lock()
	existing := s.computes[allocationID]
	s.mu.Unlock()
	if existing.ID == "" {
		existing = ComputeAllocation{ID: allocationID}
	}
	allocation, err := s.provider.DestroyComputeAllocation(ctx, existing)
	if err != nil {
		return allocation, err
	}
	s.mu.Lock()
	s.computes[allocationID] = allocation
	s.mu.Unlock()
	return allocation, nil
}

func (s *Service) CreateStorageVolume(ctx context.Context, input StorageVolumeInput) (StorageVolume, error) {
	volume, err := s.provider.CreateStorageVolume(ctx, input)
	if err != nil {
		return volume, err
	}
	if input.ID != "" {
		volume.ID = input.ID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.volumes[volume.ID] = volume
	return volume, nil
}

func (s *Service) DestroyStorageVolume(ctx context.Context, volumeID string) (StorageVolume, error) {
	s.mu.Lock()
	existing := s.volumes[volumeID]
	s.mu.Unlock()
	if existing.ID == "" {
		existing = StorageVolume{ID: volumeID}
	}
	volume, err := s.provider.DestroyStorageVolume(ctx, existing)
	if err != nil {
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
	attachment, err := s.provider.CreateStorageAttachment(ctx, input, compute, volume)
	if err != nil {
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
		existing = StorageAttachment{ID: attachmentID}
	}
	attachment, err := s.provider.DetachStorageAttachment(ctx, existing)
	if err != nil {
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
	runtime, err := s.provider.CreateWorkspaceRuntime(ctx, input, compute, volume)
	if err != nil {
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
		return runtime, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.runtimes[workspaceID]; ok {
		return existing, nil
	}
	return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "not_found"}, nil
}

func (s *Service) Readiness(ctx context.Context) (map[string]any, error) {
	return s.provider.Readiness(ctx)
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
