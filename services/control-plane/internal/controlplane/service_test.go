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
		AccountID: "acct-alpha",
		OwnerID:   "usr-owner",
		Name:      "Alpha Lab",
		PackageID: "basic",
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
	wantCalls := []string{"ledger.hold", "fabric.compute", "fabric.storage", "fabric.runtime", "ledger.evidence"}
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
	return clients.HoldResult{ID: "hold-alpha", AccountID: input.AccountID, AmountCents: 1000}, nil
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

type fakeFabricClient struct {
	calls *[]string
}

func (f *fakeFabricClient) CreateComputeAllocation(ctx context.Context, input clients.ComputeAllocationInput, idempotencyKey string) (clients.ComputeAllocation, error) {
	*f.calls = append(*f.calls, "fabric.compute")
	return clients.ComputeAllocation{ID: "compute-alpha", ProviderRequestID: "compute-request-alpha"}, nil
}

func (f *fakeFabricClient) DestroyComputeAllocation(ctx context.Context, id string, idempotencyKey string) (clients.ComputeAllocation, error) {
	*f.calls = append(*f.calls, "fabric.compute-destroy")
	return clients.ComputeAllocation{ID: id, ProviderRequestID: "compute-destroy-request-alpha"}, nil
}

func (f *fakeFabricClient) CreateStorageVolume(ctx context.Context, input clients.StorageVolumeInput, idempotencyKey string) (clients.StorageVolume, error) {
	*f.calls = append(*f.calls, "fabric.storage")
	return clients.StorageVolume{ID: "volume-alpha", ProviderRequestID: "volume-request-alpha"}, nil
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
	return clients.WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: input.WorkspaceID, URL: "https://workspace.medopl.cn/w/" + input.WorkspaceID + "/", ServiceName: "opl-compute-alpha"}, nil
}

func (f *fakeFabricClient) WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (clients.WorkspaceRuntime, error) {
	*f.calls = append(*f.calls, "fabric.runtime-status")
	return clients.WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: workspaceID, URL: "https://workspace.medopl.cn/w/" + workspaceID + "/", Status: "running"}, nil
}

func (f *fakeFabricClient) Readiness(ctx context.Context) (map[string]any, error) {
	*f.calls = append(*f.calls, "fabric.readiness")
	return map[string]any{"provider": "tencent-tke", "ready": true}, nil
}
