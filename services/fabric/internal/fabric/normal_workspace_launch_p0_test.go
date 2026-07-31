package fabric

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type normalLaunchComputeProvider struct {
	testProvider
	mu              sync.Mutex
	createCalls     int
	readbackCalls   int
	claimCalls      int
	created         ComputeAllocation
	createResultErr error
}

func (p *normalLaunchComputeProvider) CreateComputeAllocation(_ context.Context, input ComputeAllocationExecution) (ComputeAllocation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.createCalls++
	p.created = readyComputeAllocationFor(input, "machine-normal-launch")
	// Simulate Tencent accepting the scale while the response is lost.
	return ComputeAllocation{Status: "provisioning", ProviderRequestID: "req-scale-response-lost"}, p.createResultErr
}

func (p *normalLaunchComputeProvider) ReadComputeAllocation(_ context.Context, _ ComputeAllocation) (ComputeAllocation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.readbackCalls++
	return p.created, nil
}

func (p *normalLaunchComputeProvider) TagComputeMachine(_ context.Context, _ ProviderMachine, _ MachineOwnership) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.claimCalls++
	return nil
}

func (p *normalLaunchComputeProvider) counts() (int, int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.createCalls, p.readbackCalls, p.claimCalls
}

func TestNormalWorkspaceComputeCreateResponseLossUsesReadbackAfterRestart(t *testing.T) {
	for _, packageID := range []string{"basic", "pro"} {
		t.Run(packageID, func(t *testing.T) {
			store := NewMemoryOperationStore()
			provider := &normalLaunchComputeProvider{createResultErr: ErrComputeAllocationPending}
			input := ComputeAllocationInput{
				AccountID: "acct-" + packageID, WorkspaceID: "workspace-" + packageID, PackageID: packageID,
				NodePoolID: "np-" + packageID, IdempotencyKey: "workspace-launch-" + packageID + ":compute",
			}
			first := NewServiceWithOperationStore(provider, store)
			configureFastComputeAllocationPolling(first, time.Millisecond)
			allocation, err := first.CreateComputeAllocation(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				createCalls, _, _ := provider.counts()
				if createCalls == 1 {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if createCalls, _, _ := provider.counts(); createCalls != 1 {
				t.Fatalf("initial provider create calls=%d, want 1", createCalls)
			}
			waitForComputeReconcileIdle(t, first, allocation.ID)

			restarted := NewServiceWithOperationStore(provider, store)
			configureFastComputeAllocationPolling(restarted, 100*time.Millisecond)
			if _, err := restarted.CreateComputeAllocation(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			waitForOperation(t, restarted, "create_compute_allocation", "compute_allocation", allocation.ID, "succeeded")
			createCalls, readbackCalls, claimCalls := provider.counts()
			if createCalls != 1 || readbackCalls == 0 || claimCalls != 1 {
				t.Fatalf("provider calls create=%d readback=%d claim=%d, want 1/>0/1", createCalls, readbackCalls, claimCalls)
			}
			assertNormalLaunchStageBudget(t, store, "create_compute_allocation", "compute_create", 1, 1, 0)
			assertNormalLaunchStageBudget(t, store, "create_compute_allocation", "compute_claim", 1, 1, 0)
		})
	}
}

type normalLaunchStorageProvider struct {
	testProvider
	mu                   sync.Mutex
	cbsCreateCalls       int
	cbsReadbackCalls     int
	bindingApplyCalls    int
	bindingReadbackCalls int
	created              StorageVolume
	bindingApplied       bool
	failBindingResponse  bool
}

func (p *normalLaunchStorageProvider) CreateCBSVolume(_ context.Context, input StorageVolumeInput) (StorageVolume, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cbsCreateCalls++
	p.created = normalLaunchStorageVolume(input)
	return p.created, nil
}

func (p *normalLaunchStorageProvider) ReadCBSVolume(_ context.Context, input StorageVolumeInput, persisted StorageVolume) (StorageVolume, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cbsReadbackCalls++
	if p.created.ID == "" {
		return StorageVolume{}, errors.New("cbs absent")
	}
	return p.created, nil
}

func (p *normalLaunchStorageProvider) ApplyStaticStorageBinding(_ context.Context, volume StorageVolume) (StorageVolume, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bindingApplyCalls++
	p.bindingApplied = true
	volume.Status = "ready"
	if p.failBindingResponse {
		return volume, errors.New("binding response lost")
	}
	return volume, nil
}

func (p *normalLaunchStorageProvider) ReadStaticStorageBinding(_ context.Context, volume StorageVolume) (StorageVolume, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bindingReadbackCalls++
	if !p.bindingApplied {
		return volume, errors.New("static binding absent")
	}
	volume.Status = "ready"
	return volume, nil
}

func (p *normalLaunchStorageProvider) storageCounts() (int, int, int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cbsCreateCalls, p.cbsReadbackCalls, p.bindingApplyCalls, p.bindingReadbackCalls
}

func normalLaunchStorageVolume(input StorageVolumeInput) StorageVolume {
	name := k8sName(input.ID)
	return StorageVolume{
		ID: input.ID, OperationID: input.IdempotencyKey, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID,
		Status: "provider_ready", Provider: "tencent-tke", ProviderResourceID: "disk-" + stableSuffix(input.ID)[:12],
		ProviderRequestID: "req-cbs-create", SizeGB: input.SizeGB, DiskType: "CLOUD_BSSD", Zone: input.Zone,
		CBSStatus: "UNATTACHED", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z",
		ProviderData: map[string]string{"pvName": name + "-pv", "pvcName": name + "-data", "diskChargeType": "PREPAID"},
		CostTags:     oplCostTags(input.AccountID, input.WorkspaceID, input.ID, input.OperationID), CreatedAt: time.Now().UTC(),
	}
}

func TestNormalWorkspaceStoragePersistsCBSAndStaticBindingStagesAcrossRestart(t *testing.T) {
	for _, test := range []struct {
		packageID string
		sizeGB    int
	}{
		{packageID: "basic", sizeGB: 10},
		{packageID: "pro", sizeGB: 100},
	} {
		t.Run(test.packageID, func(t *testing.T) {
			store := NewMemoryOperationStore()
			provider := &normalLaunchStorageProvider{failBindingResponse: true}
			computeID := "compute-" + test.packageID
			input := StorageVolumeInput{
				ID: "storage-" + test.packageID, AccountID: "acct-" + test.packageID, WorkspaceID: "workspace-" + test.packageID,
				ComputeID: computeID, Zone: "ap-guangzhou-3", SizeGB: test.sizeGB,
				IdempotencyKey: "workspace-launch-" + test.packageID + ":storage",
			}
			first := NewServiceWithOperationStore(provider, store)
			first.computes[computeID] = ComputeAllocation{
				ID: computeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: test.packageID,
				Status: "running", ProviderData: map[string]string{"zone": input.Zone},
			}
			if _, err := first.CreateStorageVolume(context.Background(), input); err == nil {
				t.Fatal("lost static-binding response must leave the request unresolved")
			}
			provider.failBindingResponse = false

			restarted := NewServiceWithOperationStore(provider, store)
			restarted.computes[computeID] = first.computes[computeID]
			volume, err := restarted.CreateStorageVolume(context.Background(), input)
			if err != nil || volume.Status != "ready" || !stringsHasDiskPrefix(volume.ProviderResourceID) {
				t.Fatalf("restarted storage=%#v err=%v", volume, err)
			}
			cbsCreate, cbsReadback, bindingApply, bindingReadback := provider.storageCounts()
			if cbsCreate != 1 || cbsReadback != 0 || bindingApply != 1 || bindingReadback != 1 {
				t.Fatalf("provider calls cbsCreate=%d cbsReadback=%d bindingApply=%d bindingReadback=%d", cbsCreate, cbsReadback, bindingApply, bindingReadback)
			}
			assertNormalLaunchStageOperation(t, store, "cbs_create", input, volume, "succeeded")
			assertNormalLaunchStageOperation(t, store, "static_binding_apply", input, volume, "succeeded")
		})
	}
}

type noDeletePendingStorageProvider struct {
	testProvider
	deleteCalls int
}

func (p *noDeletePendingStorageProvider) SyncStorageVolume(_ context.Context, volume StorageVolume) (StorageVolume, error) {
	volume.Status = "pending"
	return volume, nil
}

func (p *noDeletePendingStorageProvider) DestroyStorageVolume(_ context.Context, volume StorageVolume) (StorageVolume, error) {
	p.deleteCalls++
	volume.Status = "retained"
	return volume, nil
}

func TestActivePaidPendingStorageNeverDeletesOrReplacesStaticBinding(t *testing.T) {
	provider := &noDeletePendingStorageProvider{}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	service.now = func() time.Time { return time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC) }
	volume := StorageVolume{
		ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", Status: "pending", Provider: "tencent-tke",
		ProviderResourceID: "disk-fixture-alpha", ProviderRequestID: "req-cbs-alpha", SizeGB: 10, Zone: "ap-guangzhou-3",
		ProviderData: map[string]string{"pvName": "storage-alpha-pv", "pvcName": "storage-alpha-data"}, CreatedAt: service.now().Add(-time.Hour),
	}
	service.volumes[volume.ID] = volume

	got, err := service.SyncStorageVolume(context.Background(), volume.ID)
	if err != nil || got.Status != "pending" || got.ProviderResourceID != volume.ProviderResourceID || provider.deleteCalls != 0 {
		t.Fatalf("sync=%#v err=%v deleteCalls=%d", got, err, provider.deleteCalls)
	}
}

func assertNormalLaunchStageBudget(t *testing.T, store OperationStore, action, stage string, attempted, confirmed, unknown int) {
	t.Helper()
	operations, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range operations {
		if operation.Action != action {
			continue
		}
		budgets, _ := operation.RedactedProviderPayload["normalLaunchMutationBudget"].(map[string]any)
		budget, _ := budgets[stage].(map[string]any)
		if fmt.Sprint(budget["attempted"]) == fmt.Sprint(attempted) && fmt.Sprint(budget["confirmed"]) == fmt.Sprint(confirmed) &&
			fmt.Sprint(budget["unknown"]) == fmt.Sprint(unknown) && fmt.Sprint(budget["max"]) == "1" {
			return
		}
	}
	t.Fatalf("missing %s budget attempted=%d confirmed=%d unknown=%d in %#v", stage, attempted, confirmed, unknown, operations)
}

func assertNormalLaunchStageOperation(t *testing.T, store OperationStore, action string, input StorageVolumeInput, volume StorageVolume, status string) {
	t.Helper()
	operations, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range operations {
		if operation.Action != action || operation.Status != status || operation.ResourceID != input.ID || operation.AccountID != input.AccountID ||
			operation.WorkspaceID != input.WorkspaceID || operation.RequestHash == "" {
			continue
		}
		var stored StorageVolume
		if decodeOperationResource(operation, &stored) && stored.ProviderResourceID == volume.ProviderResourceID && stored.SizeGB == input.SizeGB && stored.Zone == input.Zone {
			return
		}
	}
	t.Fatalf("missing %s %s operation for %#v in %#v", action, status, input, operations)
}

func stringsHasDiskPrefix(value string) bool {
	return len(value) > len("disk-") && value[:len("disk-")] == "disk-"
}
