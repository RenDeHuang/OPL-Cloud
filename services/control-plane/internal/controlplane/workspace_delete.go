package controlplane

import (
	"context"
	"errors"

	"opl-cloud/services/control-plane/internal/clients"
)

func (s *Service) workspaceDeleteFabric() (clients.FabricWorkspaceDeleteClient, error) {
	client, ok := s.fabric.(clients.FabricWorkspaceDeleteClient)
	if !ok {
		return nil, errors.New("fabric_workspace_delete_unavailable")
	}
	return client, nil
}

func (s *Service) DestroyWorkspaceRuntime(ctx context.Context, workspaceID, idempotencyKey string) (clients.WorkspaceRuntime, error) {
	client, err := s.workspaceDeleteFabric()
	if err != nil {
		return clients.WorkspaceRuntime{}, err
	}
	return client.DestroyWorkspaceRuntime(ctx, workspaceID, idempotencyKey)
}

func (s *Service) DetachWorkspaceStorage(ctx context.Context, attachmentID, idempotencyKey string) (clients.StorageAttachment, error) {
	client, err := s.workspaceDeleteFabric()
	if err != nil {
		return clients.StorageAttachment{}, err
	}
	return client.DetachStorageAttachment(ctx, attachmentID, idempotencyKey)
}

func (s *Service) DestroyWorkspaceStorage(ctx context.Context, storageID, idempotencyKey string) (clients.StorageVolume, error) {
	client, err := s.workspaceDeleteFabric()
	if err != nil {
		return clients.StorageVolume{}, err
	}
	return client.DestroyStorageVolume(ctx, storageID, idempotencyKey)
}

func (s *Service) DestroyWorkspaceCompute(ctx context.Context, computeID, idempotencyKey string) (clients.ComputeAllocation, error) {
	client, err := s.workspaceDeleteFabric()
	if err != nil {
		return clients.ComputeAllocation{}, err
	}
	return client.DestroyComputeAllocation(ctx, computeID, idempotencyKey)
}

func (s *Service) WorkspaceDeleteComputeStatus(ctx context.Context, computeID string) (clients.ComputeAllocation, error) {
	client, err := s.workspaceDeleteFabric()
	if err != nil {
		return clients.ComputeAllocation{}, err
	}
	return client.ReadComputeAllocation(ctx, computeID)
}
