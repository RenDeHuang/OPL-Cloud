package controlplane

import (
	"context"
	"errors"
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
	AccountID    string `json:"accountId"`
	OwnerID      string `json:"ownerId"`
	Name         string `json:"name"`
	PackageID    string `json:"packageId"`
	AttachmentID string `json:"attachmentId"`
	ComputeID    string `json:"computeAllocationId"`
	VolumeID     string `json:"storageId"`
}

type ManualTopUpInput struct {
	AccountID      string `json:"accountId"`
	AmountCents    int64  `json:"amountCents"`
	Currency       string `json:"currency"`
	OperatorUserID string `json:"operatorUserId"`
	Reason         string `json:"reason,omitempty"`
}

type ResourceSettlementInput struct {
	AccountID               string         `json:"accountId"`
	WorkspaceID             string         `json:"workspaceId"`
	ResourceType            string         `json:"resourceType"`
	ResourceID              string         `json:"resourceId"`
	AmountCents             int64          `json:"amountCents"`
	Currency                string         `json:"currency"`
	PricingVersion          string         `json:"pricingVersion,omitempty"`
	PriceSnapshot           map[string]any `json:"priceSnapshot,omitempty"`
	UsagePeriodStart        string         `json:"usagePeriodStart,omitempty"`
	UsagePeriodEnd          string         `json:"usagePeriodEnd,omitempty"`
	Quantity                float64        `json:"quantity,omitempty"`
	Unit                    string         `json:"unit,omitempty"`
	ProviderCostEvidenceRef string         `json:"providerCostEvidenceRef,omitempty"`
}

type ReconciliationInput struct {
	Report map[string]any `json:"report"`
}

type ComputeAllocationInput struct {
	AccountID       string `json:"accountId"`
	WorkspaceID     string `json:"workspaceId"`
	PackageID       string `json:"packageId"`
	HoldAmountCents int64  `json:"holdAmountCents"`
}

type StorageVolumeInput struct {
	AccountID       string `json:"accountId"`
	WorkspaceID     string `json:"workspaceId"`
	SizeGB          int    `json:"sizeGb"`
	HoldAmountCents int64  `json:"holdAmountCents"`
}

type StorageAttachmentInput struct {
	WorkspaceID string `json:"workspaceId"`
	ComputeID   string `json:"computeId"`
	VolumeID    string `json:"volumeId"`
}

type DestroyResourceInput struct {
	ID              string `json:"id"`
	AccountID       string `json:"accountId"`
	WorkspaceID     string `json:"workspaceId"`
	HoldID          string `json:"holdId"`
	HoldAmountCents int64  `json:"holdAmountCents"`
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
		AccountID:               input.AccountID,
		WorkspaceID:             input.WorkspaceID,
		ResourceType:            input.ResourceType,
		ResourceID:              input.ResourceID,
		AmountCents:             input.AmountCents,
		Currency:                input.Currency,
		PricingVersion:          input.PricingVersion,
		PriceSnapshot:           input.PriceSnapshot,
		UsagePeriodStart:        input.UsagePeriodStart,
		UsagePeriodEnd:          input.UsagePeriodEnd,
		Quantity:                input.Quantity,
		Unit:                    input.Unit,
		ProviderCostEvidenceRef: input.ProviderCostEvidenceRef,
	}, idempotencyKey)
}

func (s *Service) RecordReconciliation(ctx context.Context, input ReconciliationInput, idempotencyKey string) (clients.ReconciliationResult, error) {
	return s.ledger.RecordReconciliation(ctx, clients.ReconciliationInput{Report: input.Report}, idempotencyKey)
}

func (s *Service) Wallet(ctx context.Context, accountID string) (clients.Wallet, error) {
	return s.ledger.Wallet(ctx, accountID)
}

func (s *Service) ListLedgerEntries(ctx context.Context, accountID string) ([]clients.LedgerEntry, error) {
	return s.ledger.ListLedgerEntries(ctx, accountID)
}

func (s *Service) ListWalletTransactions(ctx context.Context, accountID string) ([]clients.WalletTransaction, error) {
	return s.ledger.ListWalletTransactions(ctx, accountID)
}

func (s *Service) ListManualTopUps(ctx context.Context, accountID string) ([]clients.ManualTopUp, error) {
	return s.ledger.ListManualTopUps(ctx, accountID)
}

func (s *Service) ListResourceSettlements(ctx context.Context, accountID string) ([]clients.ResourceSettlementResult, error) {
	return s.ledger.ListResourceSettlements(ctx, accountID)
}

func (s *Service) WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (clients.WorkspaceRuntime, error) {
	return s.fabric.WorkspaceRuntimeStatus(ctx, workspaceID)
}

func (s *Service) RuntimeReadiness(ctx context.Context) (map[string]any, error) {
	return s.fabric.Readiness(ctx)
}

func (s *Service) FabricOperations(ctx context.Context) ([]clients.FabricOperation, error) {
	return s.fabric.ListOperations(ctx)
}

func (s *Service) FabricCatalog(ctx context.Context) (clients.FabricCatalog, error) {
	return s.fabric.Catalog(ctx)
}

func (s *Service) CreateComputeAllocation(ctx context.Context, input ComputeAllocationInput, idempotencyKey string) (clients.ComputeAllocation, error) {
	if input.HoldAmountCents <= 0 {
		return clients.ComputeAllocation{}, fmt.Errorf("compute_hold_amount_required")
	}
	resourceID := resourceID("ca")
	hold, err := s.ledger.CreateHold(ctx, clients.HoldInput{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: "compute", ResourceID: resourceID, AmountCents: input.HoldAmountCents, Currency: "CNY"}, idempotencyKey+":hold")
	if err != nil {
		return clients.ComputeAllocation{}, err
	}
	allocation, err := s.fabric.CreateComputeAllocation(ctx, clients.ComputeAllocationInput{ID: resourceID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID}, idempotencyKey)
	if err != nil {
		_, releaseErr := s.ledger.ReleaseHold(ctx, clients.HoldReleaseInput{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: "compute", ResourceID: resourceID, HoldID: hold.ID, AmountCents: hold.AmountCents, Currency: "CNY", Reason: "compute_create_failed"}, idempotencyKey+":hold-release")
		return clients.ComputeAllocation{}, errors.Join(err, releaseErr)
	}
	allocation.HoldID = hold.ID
	allocation.HoldAmountCents = hold.AmountCents
	allocation.Wallet = hold.Wallet
	return allocation, nil
}

func (s *Service) GetComputeAllocation(ctx context.Context, id string) (clients.ComputeAllocation, error) {
	return s.fabric.GetComputeAllocation(ctx, id)
}

func (s *Service) DestroyComputeAllocation(ctx context.Context, input DestroyResourceInput, idempotencyKey string) (clients.ComputeAllocation, error) {
	allocation, err := s.fabric.DestroyComputeAllocation(ctx, input.ID, idempotencyKey)
	if err != nil {
		return allocation, err
	}
	if input.HoldID != "" && input.HoldAmountCents > 0 {
		release, err := s.ledger.ReleaseHold(ctx, clients.HoldReleaseInput{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: "compute", ResourceID: input.ID, HoldID: input.HoldID, AmountCents: input.HoldAmountCents, Currency: "CNY", Reason: "destroy_compute"}, idempotencyKey+":hold-release")
		if err != nil {
			return allocation, err
		}
		allocation.HoldID = input.HoldID
		allocation.HoldAmountCents = input.HoldAmountCents
		allocation.HoldReleaseID = release.ID
		allocation.Wallet = release.Wallet
	}
	return allocation, nil
}

func (s *Service) CreateStorageVolume(ctx context.Context, input StorageVolumeInput, idempotencyKey string) (clients.StorageVolume, error) {
	if input.HoldAmountCents <= 0 {
		return clients.StorageVolume{}, fmt.Errorf("storage_hold_amount_required")
	}
	resourceID := resourceID("vol")
	hold, err := s.ledger.CreateHold(ctx, clients.HoldInput{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: "storage", ResourceID: resourceID, AmountCents: input.HoldAmountCents, Currency: "CNY"}, idempotencyKey+":hold")
	if err != nil {
		return clients.StorageVolume{}, err
	}
	volume, err := s.fabric.CreateStorageVolume(ctx, clients.StorageVolumeInput{ID: resourceID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, SizeGB: input.SizeGB}, idempotencyKey)
	if err != nil {
		_, releaseErr := s.ledger.ReleaseHold(ctx, clients.HoldReleaseInput{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: "storage", ResourceID: resourceID, HoldID: hold.ID, AmountCents: hold.AmountCents, Currency: "CNY", Reason: "storage_create_failed"}, idempotencyKey+":hold-release")
		return clients.StorageVolume{}, errors.Join(err, releaseErr)
	}
	volume.HoldID = hold.ID
	volume.HoldAmountCents = hold.AmountCents
	volume.Wallet = hold.Wallet
	return volume, nil
}

func (s *Service) DestroyStorageVolume(ctx context.Context, input DestroyResourceInput, idempotencyKey string) (clients.StorageVolume, error) {
	volume, err := s.fabric.DestroyStorageVolume(ctx, input.ID, idempotencyKey)
	if err != nil {
		return volume, err
	}
	if input.HoldID != "" && input.HoldAmountCents > 0 {
		release, err := s.ledger.ReleaseHold(ctx, clients.HoldReleaseInput{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: "storage", ResourceID: input.ID, HoldID: input.HoldID, AmountCents: input.HoldAmountCents, Currency: "CNY", Reason: "destroy_storage"}, idempotencyKey+":hold-release")
		if err != nil {
			return volume, err
		}
		volume.HoldID = input.HoldID
		volume.HoldAmountCents = input.HoldAmountCents
		volume.HoldReleaseID = release.ID
		volume.Wallet = release.Wallet
	}
	return volume, nil
}

func (s *Service) CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput, idempotencyKey string) (clients.StorageAttachment, error) {
	return s.fabric.CreateStorageAttachment(ctx, clients.StorageAttachmentInput{WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID}, idempotencyKey)
}

func (s *Service) DetachStorageAttachment(ctx context.Context, id string, idempotencyKey string) (clients.StorageAttachment, error) {
	return s.fabric.DetachStorageAttachment(ctx, id, idempotencyKey)
}

func (s *Service) CreateWorkspace(ctx context.Context, input CreateWorkspaceInput, idempotencyKey string) (domain.WorkspaceProjection, error) {
	if input.ComputeID == "" || input.VolumeID == "" || input.AttachmentID == "" {
		return domain.WorkspaceProjection{}, fmt.Errorf("attached_compute_storage_required")
	}
	workspaceID := fmt.Sprintf("ws_%d", time.Now().UTC().UnixNano())
	runtime, err := s.fabric.CreateWorkspaceRuntime(ctx, clients.WorkspaceRuntimeInput{WorkspaceID: workspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, ImageID: "one-person-lab-app"}, idempotencyKey+":runtime")
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}
	evidence, err := s.ledger.RecordEvidence(ctx, clients.EvidenceInput{WorkspaceID: workspaceID, ProviderRequestID: runtime.ID, RedactedURL: runtime.URL, TokenVersion: "v1"}, idempotencyKey+":evidence")
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}

	return domain.WorkspaceProjection{
		ID:                  workspaceID,
		AccountID:           input.AccountID,
		OwnerID:             input.OwnerID,
		Name:                input.Name,
		PackageID:           input.PackageID,
		Provider:            "tencent-tke",
		URL:                 runtime.URL,
		Status:              "running",
		ComputeID:           input.ComputeID,
		VolumeID:            input.VolumeID,
		AttachmentID:        input.AttachmentID,
		RuntimeID:           runtime.ID,
		RuntimeServiceName:  runtime.ServiceName,
		RuntimeUsername:     runtime.Access.Username,
		RuntimePassword:     runtime.Access.Password,
		CredentialStatus:    runtime.Access.CredentialStatus,
		CredentialVersion:   runtime.Access.CredentialVersion,
		CredentialSecretRef: runtime.Access.SecretRef,
		EvidenceID:          evidence.ID,
	}, nil
}

func resourceID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UTC().UnixNano())
}
