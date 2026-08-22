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

func (s *Service) DestroyWorkspaceRuntime(ctx context.Context, accountID, workspaceID, idempotencyKey string) (clients.WorkspaceRuntime, error) {
	client, err := s.workspaceDeleteFabric()
	if err != nil {
		return clients.WorkspaceRuntime{}, err
	}
	return client.DestroyWorkspaceRuntime(ctx, accountID, workspaceID, idempotencyKey)
}

func (s *Service) DetachWorkspaceStorage(ctx context.Context, accountID, workspaceID, attachmentID, idempotencyKey string) (clients.StorageAttachment, error) {
	client, err := s.workspaceDeleteFabric()
	if err != nil {
		return clients.StorageAttachment{}, err
	}
	return client.DetachStorageAttachment(ctx, accountID, workspaceID, attachmentID, idempotencyKey)
}

func (s *Service) DestroyWorkspaceStorage(ctx context.Context, accountID, workspaceID, storageID, idempotencyKey string) (clients.StorageVolume, error) {
	client, err := s.workspaceDeleteFabric()
	if err != nil {
		return clients.StorageVolume{}, err
	}
	return client.DestroyStorageVolume(ctx, accountID, workspaceID, storageID, idempotencyKey)
}

func (s *Service) DestroyWorkspaceCompute(ctx context.Context, accountID, workspaceID, computeID, idempotencyKey string) (clients.ComputeAllocation, error) {
	client, err := s.workspaceDeleteFabric()
	if err != nil {
		return clients.ComputeAllocation{}, err
	}
	return client.DestroyComputeAllocation(ctx, accountID, workspaceID, computeID, idempotencyKey)
}

func (s *Service) WorkspaceDeleteComputeStatus(ctx context.Context, computeID string) (clients.ComputeAllocation, error) {
	client, err := s.workspaceDeleteFabric()
	if err != nil {
		return clients.ComputeAllocation{}, err
	}
	return client.ReadComputeAllocation(ctx, computeID)
}

func (s *Service) workspaceDeleteObservationFabric() (clients.FabricWorkspaceDeleteObservationClient, error) {
	client, ok := s.fabric.(clients.FabricWorkspaceDeleteObservationClient)
	if !ok {
		return nil, errors.New("fabric_workspace_delete_observation_unavailable")
	}
	return client, nil
}

func (s *Service) ObserveWorkspaceDeleteRuntime(ctx context.Context, workspaceID string) (clients.WorkspaceRuntimeObservation, error) {
	client, err := s.workspaceDeleteObservationFabric()
	if err != nil {
		return clients.WorkspaceRuntimeObservation{}, err
	}
	return client.ObserveWorkspaceRuntime(ctx, workspaceID)
}

func (s *Service) ObserveWorkspaceDeleteRuntimeGatewaySecret(ctx context.Context, workspaceID string) (clients.WorkspaceRuntimeGatewaySecretObservation, error) {
	client, err := s.workspaceDeleteObservationFabric()
	if err != nil {
		return clients.WorkspaceRuntimeGatewaySecretObservation{}, err
	}
	return client.ObserveWorkspaceRuntimeGatewaySecret(ctx, workspaceID)
}

func (s *Service) ObserveWorkspaceDeleteRuntimeResiduals(ctx context.Context, workspaceID string) (clients.WorkspaceRuntimeDeleteObservation, error) {
	client, err := s.workspaceDeleteObservationFabric()
	if err != nil {
		return clients.WorkspaceRuntimeDeleteObservation{}, err
	}
	return client.ObserveWorkspaceRuntimeDelete(ctx, workspaceID)
}
