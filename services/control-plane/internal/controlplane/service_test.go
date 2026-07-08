package controlplane

import (
	"context"
	"reflect"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
)

func TestCreateWorkspaceOrchestratesLedgerAndFabric(t *testing.T) {
	calls := []string{}
	ledger := &fakeLedgerClient{calls: &calls}
	fabric := &fakeFabricClient{calls: &calls}
	service := NewService(ledger, fabric)

	workspace, err := service.CreateWorkspace(context.Background(), CreateWorkspaceInput{
		AccountID:    "acct-alpha",
		OwnerID:      "usr-owner",
		Name:         "Alpha Lab",
		PackageID:    "basic",
		AttachmentID: "attachment-alpha",
		ComputeID:    "compute-alpha",
		VolumeID:     "volume-alpha",
	}, "workspace-once")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	if workspace.ID == "" {
		t.Fatalf("expected workspace id")
	}
	if workspace.URL == "" {
		t.Fatalf("expected runtime url")
	}
	if workspace.ComputeID != "compute-alpha" || workspace.VolumeID != "volume-alpha" || workspace.AttachmentID != "attachment-alpha" {
		t.Fatalf("workspace must bind existing resources: %#v", workspace)
	}
	if workspace.RuntimeUsername != "admin" || workspace.RuntimePassword != "runtime-password-alpha" {
		t.Fatalf("workspace must carry runtime credentials from Fabric: %#v", workspace)
	}
	wantCalls := []string{"fabric.runtime", "ledger.evidence"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestCreateComputeAllocationHoldsBeforeFabric(t *testing.T) {
	calls := []string{}
	service := NewService(&fakeLedgerClient{calls: &calls}, &fakeFabricClient{calls: &calls})

	compute, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{AccountID: "acct-alpha", PackageID: "basic", HoldAmountCents: 7862}, "compute-once")
	if err != nil {
		t.Fatalf("create compute: %v", err)
	}

	if compute.HoldID != "hold-alpha" || compute.HoldAmountCents != 7862 || compute.ID == "" {
		t.Fatalf("compute missing hold linkage: %#v", compute)
	}
	wantCalls := []string{"ledger.hold", "fabric.compute"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestCreateStorageVolumeHoldsBeforeFabric(t *testing.T) {
	calls := []string{}
	service := NewService(&fakeLedgerClient{calls: &calls}, &fakeFabricClient{calls: &calls})

	volume, err := service.CreateStorageVolume(context.Background(), StorageVolumeInput{AccountID: "acct-alpha", SizeGB: 10, HoldAmountCents: 101}, "storage-once")
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}

	if volume.HoldID != "hold-alpha" || volume.HoldAmountCents != 101 || volume.ID == "" {
		t.Fatalf("storage missing hold linkage: %#v", volume)
	}
	wantCalls := []string{"ledger.hold", "fabric.storage"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestDestroyComputeAllocationReleasesHoldAfterFabricDestroy(t *testing.T) {
	calls := []string{}
	service := NewService(&fakeLedgerClient{calls: &calls}, &fakeFabricClient{calls: &calls})

	compute, err := service.DestroyComputeAllocation(context.Background(), DestroyResourceInput{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", HoldID: "hold-alpha", HoldAmountCents: 7862}, "destroy-compute")
	if err != nil {
		t.Fatalf("destroy compute: %v", err)
	}
	if compute.HoldReleaseID != "release-alpha" || compute.Wallet.AccountID != "acct-alpha" {
		t.Fatalf("compute missing release linkage: %#v", compute)
	}
	wantCalls := []string{"fabric.compute-destroy", "ledger.release"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestDestroyStorageVolumeReleasesHoldAfterFabricDestroy(t *testing.T) {
	calls := []string{}
	service := NewService(&fakeLedgerClient{calls: &calls}, &fakeFabricClient{calls: &calls})

	volume, err := service.DestroyStorageVolume(context.Background(), DestroyResourceInput{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", HoldID: "hold-alpha", HoldAmountCents: 101}, "destroy-storage")
	if err != nil {
		t.Fatalf("destroy storage: %v", err)
	}
	if volume.HoldReleaseID != "release-alpha" || volume.Wallet.AccountID != "acct-alpha" {
		t.Fatalf("storage missing release linkage: %#v", volume)
	}
	wantCalls := []string{"fabric.storage-destroy", "ledger.release"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

type fakeLedgerClient struct {
	calls *[]string
}

func (f *fakeLedgerClient) ManualTopUp(ctx context.Context, input clients.ManualTopUpInput, idempotencyKey string) (clients.ManualTopUpResult, error) {
	*f.calls = append(*f.calls, "ledger.topup")
	return clients.ManualTopUpResult{Wallet: clients.Wallet{AccountID: input.AccountID, BalanceCents: input.AmountCents, AvailableCents: input.AmountCents}}, nil
}

func (f *fakeLedgerClient) CreateHold(ctx context.Context, input clients.HoldInput, idempotencyKey string) (clients.HoldResult, error) {
	*f.calls = append(*f.calls, "ledger.hold")
	return clients.HoldResult{ID: "hold-alpha", AccountID: input.AccountID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, AmountCents: input.AmountCents, Wallet: clients.Wallet{AccountID: input.AccountID, BalanceCents: 20000, FrozenCents: input.AmountCents, AvailableCents: 20000 - input.AmountCents}}, nil
}

func (f *fakeLedgerClient) ReleaseHold(ctx context.Context, input clients.HoldReleaseInput, idempotencyKey string) (clients.HoldReleaseResult, error) {
	*f.calls = append(*f.calls, "ledger.release")
	return clients.HoldReleaseResult{ID: "release-alpha", AccountID: input.AccountID, AmountCents: input.AmountCents, Status: "released", Wallet: clients.Wallet{AccountID: input.AccountID, BalanceCents: 20000, AvailableCents: 20000}}, nil
}

func (f *fakeLedgerClient) RecordEvidence(ctx context.Context, input clients.EvidenceInput, idempotencyKey string) (clients.EvidenceReceipt, error) {
	*f.calls = append(*f.calls, "ledger.evidence")
	return clients.EvidenceReceipt{ID: "ev-alpha", WorkspaceID: input.WorkspaceID}, nil
}

func (f *fakeLedgerClient) SettleResource(ctx context.Context, input clients.ResourceSettlementInput, idempotencyKey string) (clients.ResourceSettlementResult, error) {
	*f.calls = append(*f.calls, "ledger.settlement")
	return clients.ResourceSettlementResult{ID: "settle-alpha", AccountID: input.AccountID, AmountCents: input.AmountCents, Status: "settled"}, nil
}

func (f *fakeLedgerClient) RecordReconciliation(ctx context.Context, input clients.ReconciliationInput, idempotencyKey string) (clients.ReconciliationResult, error) {
	*f.calls = append(*f.calls, "ledger.reconciliation")
	return clients.ReconciliationResult{ID: "recon-alpha", Status: "ok", Report: input.Report}, nil
}

func (f *fakeLedgerClient) Wallet(ctx context.Context, accountID string) (clients.Wallet, error) {
	*f.calls = append(*f.calls, "ledger.wallet")
	return clients.Wallet{AccountID: accountID, Currency: "CNY"}, nil
}

func (f *fakeLedgerClient) ListLedgerEntries(ctx context.Context, accountID string) ([]clients.LedgerEntry, error) {
	*f.calls = append(*f.calls, "ledger.entries")
	return nil, nil
}

func (f *fakeLedgerClient) ListWalletTransactions(ctx context.Context, accountID string) ([]clients.WalletTransaction, error) {
	*f.calls = append(*f.calls, "ledger.wallet-tx")
	return nil, nil
}

func (f *fakeLedgerClient) ListManualTopUps(ctx context.Context, accountID string) ([]clients.ManualTopUp, error) {
	*f.calls = append(*f.calls, "ledger.topups")
	return nil, nil
}

func (f *fakeLedgerClient) ListResourceSettlements(ctx context.Context, accountID string) ([]clients.ResourceSettlementResult, error) {
	*f.calls = append(*f.calls, "ledger.settlements")
	return nil, nil
}

type fakeFabricClient struct {
	calls *[]string
}

func (f *fakeFabricClient) CreateComputeAllocation(ctx context.Context, input clients.ComputeAllocationInput, idempotencyKey string) (clients.ComputeAllocation, error) {
	*f.calls = append(*f.calls, "fabric.compute")
	return clients.ComputeAllocation{ID: input.ID, AccountID: input.AccountID, PackageID: input.PackageID, ProviderRequestID: "compute-request-alpha"}, nil
}

func (f *fakeFabricClient) GetComputeAllocation(ctx context.Context, id string) (clients.ComputeAllocation, error) {
	*f.calls = append(*f.calls, "fabric.compute-get")
	return clients.ComputeAllocation{ID: id, ProviderRequestID: "compute-request-alpha"}, nil
}

func (f *fakeFabricClient) DestroyComputeAllocation(ctx context.Context, id string, idempotencyKey string) (clients.ComputeAllocation, error) {
	*f.calls = append(*f.calls, "fabric.compute-destroy")
	return clients.ComputeAllocation{ID: id, ProviderRequestID: "compute-destroy-request-alpha"}, nil
}

func (f *fakeFabricClient) CreateStorageVolume(ctx context.Context, input clients.StorageVolumeInput, idempotencyKey string) (clients.StorageVolume, error) {
	*f.calls = append(*f.calls, "fabric.storage")
	return clients.StorageVolume{ID: input.ID, AccountID: input.AccountID, SizeGB: input.SizeGB, ProviderRequestID: "volume-request-alpha"}, nil
}

func (f *fakeFabricClient) DestroyStorageVolume(ctx context.Context, id string, idempotencyKey string) (clients.StorageVolume, error) {
	*f.calls = append(*f.calls, "fabric.storage-destroy")
	return clients.StorageVolume{ID: id, ProviderRequestID: "volume-destroy-request-alpha"}, nil
}

func (f *fakeFabricClient) CreateStorageAttachment(ctx context.Context, input clients.StorageAttachmentInput, idempotencyKey string) (clients.StorageAttachment, error) {
	*f.calls = append(*f.calls, "fabric.attachment")
	return clients.StorageAttachment{ID: "attachment-alpha", WorkspaceID: input.WorkspaceID, VolumeID: input.VolumeID, Status: "attached", ProviderRequestID: "attachment-request-alpha"}, nil
}

func (f *fakeFabricClient) DetachStorageAttachment(ctx context.Context, id string, idempotencyKey string) (clients.StorageAttachment, error) {
	*f.calls = append(*f.calls, "fabric.attachment-detach")
	return clients.StorageAttachment{ID: id, Status: "detached", ProviderRequestID: "attachment-detach-request-alpha"}, nil
}

func (f *fakeFabricClient) CreateWorkspaceRuntime(ctx context.Context, input clients.WorkspaceRuntimeInput, idempotencyKey string) (clients.WorkspaceRuntime, error) {
	*f.calls = append(*f.calls, "fabric.runtime")
	return clients.WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: input.WorkspaceID, URL: "https://workspace.medopl.cn/w/" + input.WorkspaceID + "/", ServiceName: "opl-compute-alpha", Access: clients.WorkspaceRuntimeAccess{Username: "admin", Password: "runtime-password-alpha", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-compute-alpha-env"}}, nil
}

func (f *fakeFabricClient) WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (clients.WorkspaceRuntime, error) {
	*f.calls = append(*f.calls, "fabric.runtime-status")
	return clients.WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: workspaceID, URL: "https://workspace.medopl.cn/w/" + workspaceID + "/", Status: "running"}, nil
}

func (f *fakeFabricClient) Readiness(ctx context.Context) (map[string]any, error) {
	*f.calls = append(*f.calls, "fabric.readiness")
	return map[string]any{"provider": "tencent-tke", "ready": true}, nil
}

func (f *fakeFabricClient) ListOperations(ctx context.Context) ([]clients.FabricOperation, error) {
	*f.calls = append(*f.calls, "fabric.operations")
	return []clients.FabricOperation{{ID: "fop-alpha", OperationID: "op-alpha", Action: "create_compute_allocation", ResourceKind: "compute_allocation", ResourceID: "compute-alpha", ProviderRequestID: "compute-request-alpha", RequestHash: "hash-alpha", Status: "succeeded"}}, nil
}
