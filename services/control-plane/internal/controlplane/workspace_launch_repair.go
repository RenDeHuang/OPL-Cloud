package controlplane

import (
	"context"
	"errors"

	"opl-cloud/services/control-plane/internal/clients"
)

// RepairWorkspaceRuntime delegates one paid-fulfillment Runtime replacement to
// Fabric without entering the destructive Workspace delete lane.
func (s *Service) RepairWorkspaceRuntime(ctx context.Context, input clients.WorkspaceRuntimeInput, idempotencyKey string) (clients.WorkspaceRuntime, error) {
	if input.AccountID == "" || input.WorkspaceID == "" || input.ComputeID == "" || input.VolumeID == "" ||
		input.AttachmentID == "" || input.AttachmentOperationID == "" || input.RuntimeOperationID == "" ||
		input.PreviousRuntimeOperationID == "" || input.ImageID == "" || input.GatewaySecretRef == "" || idempotencyKey == "" {
		return clients.WorkspaceRuntime{}, errors.New("workspace_runtime_repair_input_required")
	}
	client, ok := s.fabric.(clients.FabricWorkspaceRuntimeRepairClient)
	if !ok {
		return clients.WorkspaceRuntime{}, errors.New("workspace_runtime_repair_unavailable")
	}
	return client.RepairWorkspaceRuntime(ctx, input, idempotencyKey)
}
