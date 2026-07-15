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

func (s *Service) PrepareMonthlyCompute(ctx context.Context, input clients.ComputeAllocationInput, key string) (clients.ComputeAllocation, error) {
	return s.fabric.CreateComputeAllocation(ctx, input, key)
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
