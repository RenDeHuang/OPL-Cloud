package fabric

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type Provider interface {
	CreateComputeAllocation(ctx context.Context, input ComputeAllocationInput) (ComputeAllocation, error)
}

type Service struct {
	provider Provider
	mu       sync.Mutex
	runtimes map[string]WorkspaceRuntime
}

func NewService(provider Provider) *Service {
	return &Service{provider: provider, runtimes: map[string]WorkspaceRuntime{}}
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
	return s.provider.CreateComputeAllocation(ctx, input)
}

func (s *Service) DestroyComputeAllocation(_ context.Context, allocationID string) (ComputeAllocation, error) {
	now := time.Now().UTC()
	return ComputeAllocation{ID: allocationID, Status: "destroy_requested", Provider: "tencent-tke", ProviderRequestID: providerRequestID("compute-destroy", allocationID), CreatedAt: now}, nil
}

func (s *Service) CreateStorageVolume(_ context.Context, input StorageVolumeInput) (StorageVolume, error) {
	now := time.Now().UTC()
	id := fabricID("vol", input.WorkspaceID, now)
	return StorageVolume{ID: id, WorkspaceID: input.WorkspaceID, Status: "ready", ProviderRequestID: providerRequestID("storage", input.IdempotencyKey), CreatedAt: now}, nil
}

func (s *Service) CreateStorageAttachment(_ context.Context, input StorageAttachmentInput) (StorageAttachment, error) {
	now := time.Now().UTC()
	id := fabricID("att", input.WorkspaceID, now)
	return StorageAttachment{ID: id, WorkspaceID: input.WorkspaceID, VolumeID: input.VolumeID, Status: "attached", ProviderRequestID: providerRequestID("storage-attach", input.IdempotencyKey), CreatedAt: now}, nil
}

func (s *Service) CreateWorkspaceRuntime(_ context.Context, input WorkspaceRuntimeInput) (WorkspaceRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	runtime := WorkspaceRuntime{
		ID:                fabricID("rt", input.WorkspaceID, now),
		WorkspaceID:       input.WorkspaceID,
		URL:               fmt.Sprintf("https://workspace.medopl.cn/w/%s/", input.WorkspaceID),
		Status:            "running",
		ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey),
		CreatedAt:         now,
	}
	s.runtimes[input.WorkspaceID] = runtime
	return runtime, nil
}

func (s *Service) WorkspaceRuntimeStatus(_ context.Context, workspaceID string) (WorkspaceRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if runtime, ok := s.runtimes[workspaceID]; ok {
		return runtime, nil
	}
	return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "not_found"}, nil
}

type DryRunProvider struct{}

func NewDryRunProvider() DryRunProvider {
	return DryRunProvider{}
}

func (DryRunProvider) CreateComputeAllocation(_ context.Context, input ComputeAllocationInput) (ComputeAllocation, error) {
	now := time.Now().UTC()
	return ComputeAllocation{
		ID:                fabricID("ca", input.WorkspaceID, now),
		AccountID:         input.AccountID,
		WorkspaceID:       input.WorkspaceID,
		PackageID:         input.PackageID,
		Status:            "allocated",
		Provider:          "tencent-tke",
		ProviderRequestID: providerRequestID("compute", input.IdempotencyKey),
		CreatedAt:         now,
	}, nil
}

func fabricID(prefix string, owner string, now time.Time) string {
	return fmt.Sprintf("%s_%s_%d", prefix, owner, now.UnixNano())
}

func providerRequestID(prefix string, key string) string {
	if key == "" {
		key = "dry-run"
	}
	return fmt.Sprintf("%s_%s", prefix, key)
}
