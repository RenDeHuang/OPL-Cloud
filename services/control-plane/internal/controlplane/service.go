package controlplane

import (
	"context"
	"fmt"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/domain"
)

type Service struct {
	ledger clients.LedgerClient
	fabric clients.FabricClient
}

type CreateWorkspaceInput struct {
	AccountID string `json:"accountId"`
	OwnerID   string `json:"ownerId"`
	Name      string `json:"name"`
	PackageID string `json:"packageId"`
}

type ManualTopUpInput struct {
	AccountID      string `json:"accountId"`
	AmountCents    int64  `json:"amountCents"`
	Currency       string `json:"currency"`
	OperatorUserID string `json:"operatorUserId"`
	Reason         string `json:"reason,omitempty"`
}

type ResourceSettlementInput struct {
	AccountID    string `json:"accountId"`
	WorkspaceID  string `json:"workspaceId"`
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId"`
	AmountCents  int64  `json:"amountCents"`
	Currency     string `json:"currency"`
}

type ReconciliationInput struct {
	Report map[string]any `json:"report"`
}

type ComputeAllocationInput struct {
	AccountID   string `json:"accountId"`
	WorkspaceID string `json:"workspaceId"`
	PackageID   string `json:"packageId"`
}

type StorageVolumeInput struct {
	AccountID   string `json:"accountId"`
	WorkspaceID string `json:"workspaceId"`
	SizeGB      int    `json:"sizeGb"`
}

type StorageAttachmentInput struct {
	WorkspaceID string `json:"workspaceId"`
	ComputeID   string `json:"computeId"`
	VolumeID    string `json:"volumeId"`
}

func NewService(ledger clients.LedgerClient, fabric clients.FabricClient) *Service {
	return &Service{ledger: ledger, fabric: fabric}
}

func (s *Service) ManualTopUp(ctx context.Context, input ManualTopUpInput, idempotencyKey string) (clients.ManualTopUpResult, error) {
	return s.ledger.ManualTopUp(ctx, clients.ManualTopUpInput{
		AccountID:      input.AccountID,
		AmountCents:    input.AmountCents,
		Currency:       input.Currency,
		OperatorUserID: input.OperatorUserID,
		Reason:         input.Reason,
	}, idempotencyKey)
}

func (s *Service) SettleResource(ctx context.Context, input ResourceSettlementInput, idempotencyKey string) (clients.ResourceSettlementResult, error) {
	return s.ledger.SettleResource(ctx, clients.ResourceSettlementInput{
		AccountID:    input.AccountID,
		WorkspaceID:  input.WorkspaceID,
		ResourceType: input.ResourceType,
		ResourceID:   input.ResourceID,
		AmountCents:  input.AmountCents,
		Currency:     input.Currency,
	}, idempotencyKey)
}

func (s *Service) RecordReconciliation(ctx context.Context, input ReconciliationInput, idempotencyKey string) (clients.ReconciliationResult, error) {
	return s.ledger.RecordReconciliation(ctx, clients.ReconciliationInput{Report: input.Report}, idempotencyKey)
}

func (s *Service) WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (clients.WorkspaceRuntime, error) {
	return s.fabric.WorkspaceRuntimeStatus(ctx, workspaceID)
}

func (s *Service) RuntimeReadiness(ctx context.Context) (map[string]any, error) {
	return s.fabric.Readiness(ctx)
}

func (s *Service) CreateComputeAllocation(ctx context.Context, input ComputeAllocationInput, idempotencyKey string) (clients.ComputeAllocation, error) {
	return s.fabric.CreateComputeAllocation(ctx, clients.ComputeAllocationInput{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID}, idempotencyKey)
}

func (s *Service) DestroyComputeAllocation(ctx context.Context, id string, idempotencyKey string) (clients.ComputeAllocation, error) {
	return s.fabric.DestroyComputeAllocation(ctx, id, idempotencyKey)
}

func (s *Service) CreateStorageVolume(ctx context.Context, input StorageVolumeInput, idempotencyKey string) (clients.StorageVolume, error) {
	return s.fabric.CreateStorageVolume(ctx, clients.StorageVolumeInput{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, SizeGB: input.SizeGB}, idempotencyKey)
}

func (s *Service) DestroyStorageVolume(ctx context.Context, id string, idempotencyKey string) (clients.StorageVolume, error) {
	return s.fabric.DestroyStorageVolume(ctx, id, idempotencyKey)
}

func (s *Service) CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput, idempotencyKey string) (clients.StorageAttachment, error) {
	return s.fabric.CreateStorageAttachment(ctx, clients.StorageAttachmentInput{WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID}, idempotencyKey)
}

func (s *Service) DetachStorageAttachment(ctx context.Context, id string, idempotencyKey string) (clients.StorageAttachment, error) {
	return s.fabric.DetachStorageAttachment(ctx, id, idempotencyKey)
}

func (s *Service) CreateWorkspace(ctx context.Context, input CreateWorkspaceInput, idempotencyKey string) (domain.WorkspaceProjection, error) {
	workspaceID := fmt.Sprintf("ws_%d", time.Now().UTC().UnixNano())
	hold, err := s.ledger.CreateHold(ctx, clients.HoldInput{AccountID: input.AccountID, WorkspaceID: workspaceID, AmountCents: 1000, Currency: "CNY"}, idempotencyKey+":hold")
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}
	compute, err := s.fabric.CreateComputeAllocation(ctx, clients.ComputeAllocationInput{AccountID: input.AccountID, WorkspaceID: workspaceID, PackageID: input.PackageID}, idempotencyKey+":compute")
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}
	volume, err := s.fabric.CreateStorageVolume(ctx, clients.StorageVolumeInput{AccountID: input.AccountID, WorkspaceID: workspaceID, SizeGB: 10}, idempotencyKey+":storage")
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}
	runtime, err := s.fabric.CreateWorkspaceRuntime(ctx, clients.WorkspaceRuntimeInput{WorkspaceID: workspaceID, ComputeID: compute.ID, VolumeID: volume.ID, ImageID: "one-person-lab-app"}, idempotencyKey+":runtime")
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}
	evidence, err := s.ledger.RecordEvidence(ctx, clients.EvidenceInput{WorkspaceID: workspaceID, ProviderRequestID: runtime.ID, RedactedURL: runtime.URL, TokenVersion: "v1"}, idempotencyKey+":evidence")
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}

	return domain.WorkspaceProjection{
		ID:                 workspaceID,
		AccountID:          input.AccountID,
		OwnerID:            input.OwnerID,
		Name:               input.Name,
		PackageID:          input.PackageID,
		URL:                runtime.URL,
		Status:             "running",
		HoldID:             hold.ID,
		ComputeID:          compute.ID,
		VolumeID:           volume.ID,
		RuntimeID:          runtime.ID,
		RuntimeServiceName: runtime.ServiceName,
		EvidenceID:         evidence.ID,
	}, nil
}
