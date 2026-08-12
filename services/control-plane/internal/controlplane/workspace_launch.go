package controlplane

import (
	"context"
	"errors"

	"opl-cloud/services/control-plane/internal/clients"
)

func (s *Service) PreflightWorkspaceLaunch(ctx context.Context, input clients.WorkspaceLaunchPreflightInput) (clients.WorkspaceLaunchPreflight, error) {
	client, ok := s.fabric.(clients.FabricWorkspaceLaunchClient)
	if !ok {
		return clients.WorkspaceLaunchPreflight{}, errors.New("fabric_workspace_launch_unavailable")
	}
	return client.PreflightWorkspaceLaunch(ctx, input)
}

func (s *Service) ReadWorkspaceLaunchStage(ctx context.Context, input clients.WorkspaceLaunchStageInput) (clients.WorkspaceLaunchStageResult, error) {
	client, ok := s.fabric.(clients.FabricWorkspaceLaunchClient)
	if !ok {
		return clients.WorkspaceLaunchStageResult{}, errors.New("fabric_workspace_launch_unavailable")
	}
	return client.ReadWorkspaceLaunchStage(ctx, input)
}

func (s *Service) EnsureWorkspaceLaunchStage(ctx context.Context, input clients.WorkspaceLaunchStageInput) (clients.WorkspaceLaunchStageResult, error) {
	client, ok := s.fabric.(clients.FabricWorkspaceLaunchClient)
	if !ok {
		return clients.WorkspaceLaunchStageResult{}, errors.New("fabric_workspace_launch_unavailable")
	}
	return client.EnsureWorkspaceLaunchStage(ctx, input)
}

func (s *Service) ReadLegacyWorkspaceLaunchBinding(ctx context.Context, input clients.LegacyWorkspaceLaunchBindingInput) (clients.LegacyWorkspaceLaunchBindingResult, error) {
	client, ok := s.fabric.(clients.FabricLegacyWorkspaceLaunchClient)
	if !ok {
		return clients.LegacyWorkspaceLaunchBindingResult{}, errors.New("fabric_legacy_workspace_launch_unavailable")
	}
	return client.ReadLegacyWorkspaceLaunchBinding(ctx, input)
}
