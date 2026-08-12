package controlplane

import (
	"context"
	"errors"
	"os"
	"strings"

	"opl-cloud/services/control-plane/internal/clients"
)

// ProviderAcceptanceLaunchZone preserves the legacy create-only configuration
// until the Instance caller cutover. ProviderFactsBatch remains readback authority.
func ProviderAcceptanceLaunchZone() string {
	return strings.TrimSpace(os.Getenv("OPL_TENCENT_ZONE"))
}

func (s *Service) Sub2APIBalance(ctx context.Context, userID int64) (clients.Sub2APIBalance, error) {
	if s.sub2API == nil {
		return clients.Sub2APIBalance{}, errors.New("sub2api_unavailable")
	}
	return s.sub2API.Balance(ctx, userID)
}

func (s *Service) ChargeSub2API(ctx context.Context, input clients.Sub2APIChargeInput) (clients.Sub2APICharge, error) {
	if s.sub2API == nil {
		return clients.Sub2APICharge{}, errors.New("sub2api_unavailable")
	}
	return s.sub2API.Charge(ctx, input)
}

func (s *Service) RefundSub2API(ctx context.Context, input clients.Sub2APIRefundInput) (clients.Sub2APIRefund, error) {
	client, ok := s.sub2API.(clients.Sub2APIRefundClient)
	if !ok {
		return clients.Sub2APIRefund{}, errors.New("sub2api_refund_unavailable")
	}
	return client.Refund(ctx, input)
}

func (s *Service) PreflightMonthlyResource(ctx context.Context, input clients.MonthlyPreflightInput) (clients.MonthlyPreflight, error) {
	client, ok := s.fabric.(clients.FabricMonthlyPreflightClient)
	if !ok {
		return clients.MonthlyPreflight{}, errors.New("fabric_monthly_preflight_unavailable")
	}
	return client.MonthlyPreflight(ctx, input)
}

func (s *Service) PrepareMonthlyCompute(ctx context.Context, input clients.ComputeAllocationInput, key string) (clients.ComputeAllocation, error) {
	return s.fabric.CreateComputeAllocation(ctx, input, key)
}

func (s *Service) RenewMonthlyCompute(ctx context.Context, accountID, workspaceID, id, key string) (clients.ProviderResourceMutation, error) {
	client, ok := s.fabric.(clients.FabricRenewalClient)
	if !ok {
		return clients.ProviderResourceMutation{}, errors.New("fabric_renewal_unavailable")
	}
	return client.RenewComputeAllocation(ctx, accountID, workspaceID, id, key)
}

func (s *Service) PrepareMonthlyStorage(ctx context.Context, input clients.StorageVolumeInput, key string) (clients.StorageVolume, error) {
	return s.fabric.CreateStorageVolume(ctx, input, key)
}

// PrepareProviderAcceptanceRuntime is a create-only compatibility bridge for the
// legacy operator route. Runtime readiness is read separately through ProviderFactsBatch.
func (s *Service) PrepareProviderAcceptanceRuntime(ctx context.Context, input CreateWorkspaceInput, key string) (clients.WorkspaceRuntime, error) {
	if input.WorkspaceID == "" || input.AccountID == "" || input.Sub2APIUserID <= 0 || input.ComputeID == "" || input.VolumeID == "" ||
		input.AttachmentID == "" || input.AttachmentOperationID == "" || input.RuntimeOperationID == "" || input.RuntimeOperationID != key+":runtime" {
		return clients.WorkspaceRuntime{}, errors.New("provider_acceptance_runtime_input_required")
	}
	secretRef, err := s.gatewaySecretRef(ctx, input.AccountID, input.WorkspaceID, input.Sub2APIUserID, key)
	if err != nil {
		return clients.WorkspaceRuntime{}, err
	}
	runtime, err := s.fabric.CreateWorkspaceRuntime(ctx, clients.WorkspaceRuntimeInput{
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID,
		AttachmentID: input.AttachmentID, AttachmentOperationID: input.AttachmentOperationID, RuntimeOperationID: input.RuntimeOperationID,
		ImageID: "one-person-lab-app", GatewaySecretRef: secretRef,
	}, input.RuntimeOperationID)
	if err != nil {
		return runtime, err
	}
	if runtime.ID == "" || runtime.WorkspaceID != input.WorkspaceID || runtime.OperationID != "" && runtime.OperationID != input.RuntimeOperationID {
		return clients.WorkspaceRuntime{}, ErrWorkspaceRuntimeIdentityMismatch
	}
	return runtime, nil
}

func (s *Service) RenewMonthlyStorage(ctx context.Context, accountID, workspaceID, id, key string) (clients.ProviderResourceMutation, error) {
	client, ok := s.fabric.(clients.FabricRenewalClient)
	if !ok {
		return clients.ProviderResourceMutation{}, errors.New("fabric_renewal_unavailable")
	}
	return client.RenewStorageVolume(ctx, accountID, workspaceID, id, key)
}

func (s *Service) RecordMonthlyReceipt(ctx context.Context, input clients.ReceiptInput, key string) (clients.Receipt, error) {
	return s.ledger.RecordReceipt(ctx, input, key)
}
