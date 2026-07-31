package controlplane

import (
	"context"
	"errors"

	"opl-cloud/services/control-plane/internal/clients"
)

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

func (s *Service) MonthlyProviderTruth(ctx context.Context, computeID, storageID string) (clients.MonthlyProviderTruth, error) {
	client, ok := s.fabric.(clients.FabricMonthlyProviderTruthClient)
	if !ok {
		return clients.MonthlyProviderTruth{}, errors.New("fabric_monthly_provider_truth_unavailable")
	}
	return client.MonthlyProviderTruth(ctx, computeID, storageID)
}

func (s *Service) WorkspaceActivationTruth(ctx context.Context, input clients.WorkspaceActivationTruthInput) (clients.WorkspaceActivationTruth, error) {
	client, ok := s.fabric.(clients.FabricWorkspaceActivationTruthClient)
	if !ok {
		return clients.WorkspaceActivationTruth{Reason: "provider_unavailable", ErrorClass: "client_unavailable"}, errors.New("fabric_workspace_activation_truth_unavailable")
	}
	return client.WorkspaceActivationTruth(ctx, input)
}

func (s *Service) ComputeClaimRecoveryProof(ctx context.Context, input clients.ComputeClaimRecoveryInput) (clients.ComputeClaimRecoveryProof, error) {
	client, ok := s.fabric.(clients.FabricComputeClaimRecoveryClient)
	if !ok {
		return clients.ComputeClaimRecoveryProof{Reason: "provider_describe"}, errors.New("fabric_compute_claim_recovery_unavailable")
	}
	return client.ComputeClaimRecoveryProof(ctx, input)
}

func (s *Service) ClaimComputeRecovery(ctx context.Context, input clients.ComputeClaimRecoveryClaimInput, key string) (clients.ComputeClaimRecoveryProof, error) {
	client, ok := s.fabric.(clients.FabricComputeClaimRecoveryClient)
	if !ok {
		return clients.ComputeClaimRecoveryProof{Reason: "provider_describe"}, errors.New("fabric_compute_claim_recovery_unavailable")
	}
	return client.ClaimComputeRecovery(ctx, input, key)
}

func (s *Service) PrepareMonthlyCompute(ctx context.Context, input clients.ComputeAllocationInput, key string) (clients.ComputeAllocation, error) {
	return s.fabric.CreateComputeAllocation(ctx, input, key)
}

func (s *Service) ReadMonthlyCompute(ctx context.Context, id string) (clients.ComputeAllocation, error) {
	return s.fabric.GetComputeAllocation(ctx, id)
}

func (s *Service) SyncMonthlyCompute(ctx context.Context, id string) (clients.ComputeAllocation, error) {
	return s.fabric.SyncComputeAllocation(ctx, id)
}

func (s *Service) RenewMonthlyCompute(ctx context.Context, id, key string) (clients.ComputeAllocation, error) {
	client, ok := s.fabric.(clients.FabricRenewalClient)
	if !ok {
		return clients.ComputeAllocation{}, errors.New("fabric_renewal_unavailable")
	}
	return client.RenewComputeAllocation(ctx, id, key)
}

func (s *Service) CleanupMonthlyCompute(ctx context.Context, id, key string) (clients.ComputeAllocation, error) {
	return s.fabric.DestroyComputeAllocation(ctx, id, key)
}

func (s *Service) CleanupWorkspaceRuntime(ctx context.Context, workspaceID, key string) (clients.WorkspaceRuntime, error) {
	return s.fabric.DestroyWorkspaceRuntime(ctx, workspaceID, key)
}

func (s *Service) PrepareMonthlyStorage(ctx context.Context, input clients.StorageVolumeInput, key string) (clients.StorageVolume, error) {
	return s.fabric.CreateStorageVolume(ctx, input, key)
}

func (s *Service) ReadMonthlyStorage(ctx context.Context, id string) (clients.StorageVolume, error) {
	reader, ok := s.fabric.(clients.FabricStorageVolumeReader)
	if !ok {
		return clients.StorageVolume{}, errors.New("fabric_storage_volume_read_unavailable")
	}
	return reader.GetStorageVolume(ctx, id)
}

func (s *Service) SyncMonthlyStorage(ctx context.Context, id string) (clients.StorageVolume, error) {
	return s.fabric.SyncStorageVolume(ctx, id)
}

func (s *Service) RenewMonthlyStorage(ctx context.Context, id, key string) (clients.StorageVolume, error) {
	client, ok := s.fabric.(clients.FabricRenewalClient)
	if !ok {
		return clients.StorageVolume{}, errors.New("fabric_renewal_unavailable")
	}
	return client.RenewStorageVolume(ctx, id, key)
}

func (s *Service) CleanupMonthlyStorage(ctx context.Context, id, key string) (clients.StorageVolume, error) {
	return s.fabric.DestroyStorageVolume(ctx, id, key)
}

func (s *Service) RecordMonthlyReceipt(ctx context.Context, input clients.ReceiptInput, key string) (clients.Receipt, error) {
	return s.ledger.RecordReceipt(ctx, input, key)
}
