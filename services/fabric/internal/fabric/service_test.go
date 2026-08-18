package fabric

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type readinessRecordingProvider struct {
	testProvider
	calls        atomic.Int32
	entered      chan int32
	releaseFirst chan struct{}
	probe        func(context.Context, int32) (map[string]any, error)
}

func (p *readinessRecordingProvider) Readiness(ctx context.Context) (map[string]any, error) {
	call := p.calls.Add(1)
	if p.entered != nil {
		p.entered <- call
	}
	if call == 1 && p.releaseFirst != nil {
		select {
		case <-p.releaseFirst:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if p.probe != nil {
		return p.probe(ctx, call)
	}
	return map[string]any{"provider": "test", "ready": true, "generation": call}, nil
}

func TestReadinessCachesSuccessfulResultAndSingleflightsRefresh(t *testing.T) {
	const callers = 8
	provider := &readinessRecordingProvider{
		entered:      make(chan int32, callers),
		releaseFirst: make(chan struct{}),
	}
	service := NewService(provider)
	now := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	start := make(chan struct{})
	results := make(chan map[string]any, callers)
	errors := make(chan error, callers)
	var ready sync.WaitGroup
	var finished sync.WaitGroup
	ready.Add(callers)
	finished.Add(callers)
	for range callers {
		go func() {
			defer finished.Done()
			ready.Done()
			<-start
			result, err := service.Readiness(context.Background())
			results <- result
			errors <- err
		}()
	}
	ready.Wait()
	close(start)
	<-provider.entered
	select {
	case call := <-provider.entered:
		close(provider.releaseFirst)
		finished.Wait()
		t.Fatalf("concurrent readiness started provider call %d before the first refresh completed", call)
	case <-time.After(100 * time.Millisecond):
		close(provider.releaseFirst)
	}
	finished.Wait()

	for range callers {
		if err := <-errors; err != nil {
			t.Fatalf("concurrent readiness error = %v", err)
		}
		if result := <-results; result["generation"] != int32(1) || result["ready"] != true {
			t.Fatalf("concurrent readiness result = %#v", result)
		}
	}
	if result, err := service.Readiness(context.Background()); err != nil || result["generation"] != int32(1) || provider.calls.Load() != 1 {
		t.Fatalf("cached readiness = %#v, err=%v, provider calls=%d", result, err, provider.calls.Load())
	}

	now = now.Add(time.Minute)
	result, err := service.Readiness(context.Background())
	if err != nil || result["generation"] != int32(2) || provider.calls.Load() != 2 {
		t.Fatalf("expired readiness = %#v, err=%v, provider calls=%d", result, err, provider.calls.Load())
	}
}

func TestReadinessDoesNotCacheErrors(t *testing.T) {
	provider := &readinessRecordingProvider{
		probe: func(_ context.Context, call int32) (map[string]any, error) {
			if call == 1 {
				return nil, errors.New("provider readiness failed")
			}
			return map[string]any{"provider": "test", "ready": true}, nil
		},
	}
	service := NewService(provider)

	if result, err := service.Readiness(context.Background()); err == nil || err.Error() != "provider readiness failed" || result != nil {
		t.Fatalf("failed readiness = %#v, err=%v", result, err)
	}
	result, err := service.Readiness(context.Background())
	if err != nil || result["ready"] != true || provider.calls.Load() != 2 {
		t.Fatalf("retried readiness = %#v, err=%v, provider calls=%d", result, err, provider.calls.Load())
	}
	if _, err := service.Readiness(context.Background()); err != nil || provider.calls.Load() != 2 {
		t.Fatalf("successful retry was not cached: err=%v provider calls=%d", err, provider.calls.Load())
	}
}

func TestReadinessBoundsProviderCallWithTimeout(t *testing.T) {
	provider := &readinessRecordingProvider{
		probe: func(ctx context.Context, _ int32) (map[string]any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	service := NewService(provider)
	service.readinessTimeout = 20 * time.Millisecond

	started := time.Now()
	result, err := service.Readiness(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || result != nil {
		t.Fatalf("timed readiness = %#v, err=%v", result, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("provider timeout took %s", elapsed)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls.Load())
	}
}

func TestMonthlyPreflightIsReadOnlyAndDoesNotRecordOperation(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	input := MonthlyPreflightInput{ResourceType: "storage", PackageID: "basic", SizeGB: 10, Zone: "na-siliconvalley-1"}

	result, err := service.MonthlyPreflight(context.Background(), input)
	operations, listErr := service.ListOperations(context.Background())
	if err != nil || listErr != nil || len(operations) != 0 {
		t.Fatalf("preflight=%#v err=%v operations=%#v listErr=%v", result, err, operations, listErr)
	}
	if result.ResourceType != input.ResourceType || result.PackageID != input.PackageID || result.SizeGB != input.SizeGB || result.Zone != input.Zone || !result.Available || result.ProviderPriceCNY <= 0 || len(result.ProviderRequestIDs) == 0 {
		t.Fatalf("preflight identity or evidence mismatch: %#v", result)
	}
}

type recordingMonthlyTruthProvider struct {
	testProvider
	called  int
	compute ComputeAllocation
	storage StorageVolume
	result  MonthlyProviderTruth
	err     error
}

func (p *recordingMonthlyTruthProvider) MonthlyProviderTruth(_ context.Context, compute ComputeAllocation, storage StorageVolume) (MonthlyProviderTruth, error) {
	p.called++
	p.compute, p.storage = compute, storage
	return p.result, p.err
}

func monthlyTruthResources() (ComputeAllocation, StorageVolume) {
	compute := ComputeAllocation{
		ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", Status: "running",
		Provider: "tencent-tke", ProviderResourceID: "machine/node-basic-1", ProviderRequestID: "req-compute",
		NodePoolID: "np-basic", InstanceID: "ins-basic-1", CVMInstanceID: "ins-basic-1", MachineName: "node-basic-1",
		PrivateIP: "10.0.0.11", InstanceType: "SA5.MEDIUM4", Zone: "ap-guangzhou-3", ChargeType: "PREPAID",
		RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-16T00:00:00Z",
		ProviderData: map[string]string{"instanceType": "SA5.MEDIUM4", "zone": "ap-guangzhou-3", "chargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16T00:00:00Z"},
		CostTags:     oplCostTags("acct-alpha", "ws-alpha", "compute-alpha", "owner-compute-alpha"),
	}
	storage := StorageVolume{
		ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready", Provider: "tencent-tke",
		ProviderResourceID: "disk-storage-alpha", ProviderRequestID: "req-storage", SizeGB: 10, CBSStatus: "ATTACHED",
		DiskType: "CLOUD_BSSD", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-16T00:00:00Z", Zone: "ap-guangzhou-3",
		ProviderData: map[string]string{"chargeType": "PREPAID", "diskChargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16T00:00:00Z", "zone": "ap-guangzhou-3"},
		CostTags:     oplCostTags("acct-alpha", "ws-alpha", "storage-alpha", "owner-storage-alpha"),
	}
	return compute, storage
}

func monthlyTruthResult(computeState, storageState string) MonthlyProviderTruth {
	compute, storage := monthlyTruthResources()
	compute.ProviderRequestID, storage.ProviderRequestID = "req-provider-truth", "req-provider-truth"
	if computeState == "absent" {
		compute.Status, compute.CVMStatus = "external_deleted", "NOT_FOUND"
	}
	if storageState == "absent" {
		storage.Status, storage.CBSStatus = "external_deleted", "NOT_FOUND"
	}
	return MonthlyProviderTruth{
		ComputeState: computeState, StorageState: storageState, Compute: compute, Storage: storage,
		ProviderRequestID: "req-provider-truth",
	}
}

func TestMonthlyProviderTruthReturnsIndependentReadOnlyStates(t *testing.T) {
	for _, tc := range []struct {
		name, computeState, storageState string
	}{
		{name: "compute ready storage absent", computeState: "ready", storageState: "absent"},
		{name: "both absent", computeState: "absent", storageState: "absent"},
		{name: "compute absent storage ready", computeState: "absent", storageState: "ready"},
		{name: "both ready", computeState: "ready", storageState: "ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compute, storage := monthlyTruthResources()
			provider := &recordingMonthlyTruthProvider{result: monthlyTruthResult(tc.computeState, tc.storageState)}
			store := NewMemoryOperationStore()
			service := NewServiceWithOperationStore(provider, store)
			service.computes[compute.ID], service.volumes[storage.ID] = compute, storage

			result, err := service.MonthlyProviderTruth(context.Background(), compute.ID, storage.ID)
			operations, listErr := service.ListOperations(context.Background())

			if err != nil || listErr != nil || result.ComputeState != tc.computeState || result.StorageState != tc.storageState || provider.called != 1 {
				t.Fatalf("truth=%#v err=%v calls=%d listErr=%v", result, err, provider.called, listErr)
			}
			if result.Compute.ID != compute.ID || result.Storage.ID != storage.ID || provider.compute.ID != compute.ID || provider.storage.ID != storage.ID || len(operations) != 0 {
				t.Fatalf("truth changed identity or recorded a mutation: result=%#v provider=%#v/%#v operations=%#v", result, provider.compute, provider.storage, operations)
			}
		})
	}
}

func TestMonthlyProviderTruthRejectsMissingOrMismatchedLocalIdentityBeforeProvider(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Service, ComputeAllocation, StorageVolume)
	}{
		{name: "compute missing", mutate: func(service *Service, _ ComputeAllocation, storage StorageVolume) {
			service.volumes[storage.ID] = storage
		}},
		{name: "storage missing", mutate: func(service *Service, compute ComputeAllocation, _ StorageVolume) {
			service.computes[compute.ID] = compute
		}},
		{name: "account mismatch", mutate: func(service *Service, compute ComputeAllocation, storage StorageVolume) {
			storage.AccountID = "acct-other"
			service.computes[compute.ID], service.volumes[storage.ID] = compute, storage
		}},
		{name: "workspace mismatch", mutate: func(service *Service, compute ComputeAllocation, storage StorageVolume) {
			storage.WorkspaceID = "ws-other"
			service.computes[compute.ID], service.volumes[storage.ID] = compute, storage
		}},
		{name: "zone mismatch", mutate: func(service *Service, compute ComputeAllocation, storage StorageVolume) {
			storage.Zone = "ap-guangzhou-4"
			service.computes[compute.ID], service.volumes[storage.ID] = compute, storage
		}},
		{name: "compute ownership mismatch", mutate: func(service *Service, compute ComputeAllocation, storage StorageVolume) {
			compute.CostTags["opl_resource_id"] = "compute-other"
			service.computes[compute.ID], service.volumes[storage.ID] = compute, storage
		}},
		{name: "storage provider identity missing", mutate: func(service *Service, compute ComputeAllocation, storage StorageVolume) {
			storage.ProviderResourceID = ""
			service.computes[compute.ID], service.volumes[storage.ID] = compute, storage
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compute, storage := monthlyTruthResources()
			provider := &recordingMonthlyTruthProvider{result: monthlyTruthResult("ready", "ready")}
			store := NewMemoryOperationStore()
			service := NewServiceWithOperationStore(provider, store)
			tc.mutate(service, compute, storage)

			result, err := service.MonthlyProviderTruth(context.Background(), compute.ID, storage.ID)
			operations, listErr := service.ListOperations(context.Background())

			if err == nil || result.ComputeState != "unknown" || result.StorageState != "unknown" || provider.called != 0 || listErr != nil || len(operations) != 0 {
				t.Fatalf("unsafe local identity truth=%#v err=%v calls=%d operations=%#v listErr=%v", result, err, provider.called, operations, listErr)
			}
		})
	}
}

func TestMonthlyProviderTruthRejectsMismatchedProviderIdentity(t *testing.T) {
	compute, storage := monthlyTruthResources()
	invalid := monthlyTruthResult("ready", "ready")
	invalid.Compute.ID = "compute-other"
	provider := &recordingMonthlyTruthProvider{result: invalid}
	service := NewService(provider)
	service.computes[compute.ID], service.volumes[storage.ID] = compute, storage

	result, err := service.MonthlyProviderTruth(context.Background(), compute.ID, storage.ID)

	if err == nil || result.ComputeState != "unknown" || result.StorageState != "unknown" || provider.called != 1 {
		t.Fatalf("mismatched provider identity truth=%#v err=%v calls=%d", result, err, provider.called)
	}
}

type pendingStorageProvider struct {
	testProvider
	deleteErr   error
	deleteCalls int
}

type countingComputeSyncProvider struct {
	testProvider
	syncCalls int
	lastSync  ComputeAllocation
}

type externalDeletedComputeSyncProvider struct{ testProvider }

func (externalDeletedComputeSyncProvider) SyncComputeAllocation(_ context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	allocation.Status = "external_deleted"
	return allocation, nil
}

type externalDeletedComputeDestroyProvider struct {
	testProvider
	destroyed chan ComputeAllocation
}

func (p *externalDeletedComputeDestroyProvider) DestroyComputeAllocation(_ context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	p.destroyed <- allocation
	allocation.Status = "destroyed"
	allocation.Provider = "tencent-tke"
	allocation.ProviderRequestID = "cleanup-alpha"
	return allocation, nil
}

func (p *countingComputeSyncProvider) SyncComputeAllocation(_ context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	p.syncCalls++
	p.lastSync = allocation
	allocation.Status = "running"
	return allocation, nil
}

func TestSyncComputeAllocationHydratesSucceededMachineIdentity(t *testing.T) {
	provider := &countingComputeSyncProvider{}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	pending := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic", Status: "provisioning"}
	ready := pending
	ready.Status = "running"
	ready.MachineName = "machine-alpha"
	ready.InstanceID = "ins-alpha"
	ready.NodeName = "node-alpha"
	operation := newOperation("create_compute_allocation", "compute_allocation", pending.ID, pending.AccountID, "", "request-alpha", hashInput(pending), time.Now().UTC())
	if err := service.recordOperation(context.Background(), operation, "succeeded", ready, nil); err != nil {
		t.Fatal(err)
	}
	service.computes[pending.ID] = pending

	allocation, err := service.SyncComputeAllocation(context.Background(), pending.ID)

	if err != nil || allocation.Status != "running" || provider.syncCalls != 1 || provider.lastSync.MachineName != ready.MachineName || provider.lastSync.InstanceID != ready.InstanceID || provider.lastSync.NodeName != ready.NodeName {
		t.Fatalf("hydrated allocation=%#v err=%v provider=%#v", allocation, err, provider)
	}
}

func TestSyncComputeAllocationWaitsForMachineIdentity(t *testing.T) {
	provider := &countingComputeSyncProvider{}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	resource := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic", Status: "provisioning"}
	operation := newOperation("create_compute_allocation", "compute_allocation", resource.ID, resource.AccountID, "", "request-alpha", hashInput(resource), time.Now().UTC())
	if err := service.recordOperation(context.Background(), operation, "started", resource, nil); err != nil {
		t.Fatal(err)
	}
	service.computes[resource.ID] = resource

	allocation, err := service.SyncComputeAllocation(context.Background(), "compute-alpha")

	if err != nil || allocation.Status != "provisioning" || provider.syncCalls != 0 {
		t.Fatalf("pending allocation=%#v err=%v provider sync calls=%d", allocation, err, provider.syncCalls)
	}
}

func TestSyncComputeAllocationReleasesExternallyDeletedMachineOwnership(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(externalDeletedComputeSyncProvider{}, store)
	resource := ComputeAllocation{
		ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic", Status: "running",
		MachineName: "machine-alpha", InstanceID: "ins-alpha", NodeName: "node-alpha",
	}
	service.computes[resource.ID] = resource
	if _, _, err := store.ClaimMachine(context.Background(), MachineOwnership{
		ID: "owner-alpha", ResourceID: resource.ID, AccountID: resource.AccountID, PackageID: resource.PackageID,
		NodePoolID: "np-basic", MachineID: resource.MachineName, InstanceID: resource.InstanceID,
		NodeName: resource.NodeName, Status: "active", ClaimedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	allocation, err := service.SyncComputeAllocation(context.Background(), resource.ID)
	ownership, ownershipErr := store.MachineOwnership(context.Background(), resource.ID)

	if err != nil || allocation.Status != "external_deleted" || ownershipErr != nil || ownership.Status != "released" || ownership.ReleasedAt == nil {
		t.Fatalf("allocation=%#v err=%v ownership=%#v ownershipErr=%v", allocation, err, ownership, ownershipErr)
	}
}

func TestDestroyComputeAllocationPreservesExternalDeletionForProviderCleanup(t *testing.T) {
	provider := &externalDeletedComputeDestroyProvider{destroyed: make(chan ComputeAllocation, 1)}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	resource := ComputeAllocation{
		ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic", Status: "external_deleted",
		MachineName: "machine-alpha", InstanceID: "ins-alpha", NodeName: "node-alpha",
	}
	service.computes[resource.ID] = resource

	started, err := service.DestroyComputeAllocation(context.Background(), resource.ID)
	providerInput := <-provider.destroyed
	waitForOperation(t, service, "destroy_compute_allocation", "compute_allocation", resource.ID, "succeeded")

	if err != nil || started.Status != "external_deleted" || providerInput.Status != "external_deleted" {
		t.Fatalf("started=%#v err=%v providerInput=%#v", started, err, providerInput)
	}
}

func (*pendingStorageProvider) SyncStorageVolume(_ context.Context, volume StorageVolume) (StorageVolume, error) {
	volume.Status = "pending"
	return volume, nil
}

func (p *pendingStorageProvider) DestroyStorageVolume(_ context.Context, volume StorageVolume) (StorageVolume, error) {
	p.deleteCalls++
	if p.deleteErr != nil {
		return volume, p.deleteErr
	}
	volume.Status = "released"
	return volume, nil
}

func TestSyncStorageVolumePreservesTimedOutPendingStorage(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	provider := &pendingStorageProvider{deleteErr: errors.New("delete must not be called")}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	service.now = func() time.Time { return now }
	service.volumes["storage-alpha"] = StorageVolume{ID: "storage-alpha", AccountID: "acct-alpha", Status: "pending", ProviderResourceID: "pvc/storage-alpha-data", CreatedAt: now.Add(-11 * time.Minute)}

	volume, err := service.SyncStorageVolume(context.Background(), "storage-alpha")
	if err != nil || volume.Status != "pending" || provider.deleteCalls != 0 {
		t.Fatalf("timed out storage = %#v err=%v deleteCalls=%d", volume, err, provider.deleteCalls)
	}
}

func TestJobLifecycleUsesDurableOperationStore(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	ctx := context.Background()
	input := JobInput{
		OrganizationID: "org-alpha",
		WorkspaceID:    "workspace-alpha",
		ProjectID:      "project-alpha",
		TaskID:         "task-alpha",
		RequestID:      "request-alpha",
		ApprovalID:     "approval-alpha",
		EnvironmentRef: "environment-alpha",
		IdempotencyKey: "job-once",
	}

	created, err := service.CreateJob(ctx, input)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if created.JobID == "" || created.Status != "queued" || created.RequestID != "request-alpha" {
		t.Fatalf("unexpected created job: %#v", created)
	}
	replayed, err := service.CreateJob(ctx, input)
	if err != nil {
		t.Fatalf("replay job: %v", err)
	}
	if !replayed.Replayed || replayed.JobID != created.JobID {
		t.Fatalf("unexpected replayed job: %#v", replayed)
	}

	restarted := NewServiceWithOperationStore(testProvider{}, store)
	queried, err := restarted.Job(ctx, created.JobID)
	if err != nil || queried.Status != "queued" {
		t.Fatalf("query durable job: %#v, %v", queried, err)
	}
	cancelled, err := restarted.CancelJob(ctx, created.JobID, "cancel-once")
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel job: %#v, %v", cancelled, err)
	}
	queried, err = restarted.Job(ctx, created.JobID)
	if err != nil || queried.Status != "cancelled" {
		t.Fatalf("query cancelled job: %#v, %v", queried, err)
	}

	input.EnvironmentRef = "environment-beta"
	if _, err := restarted.CreateJob(ctx, input); !errors.Is(err, ErrJobIdempotencyConflict) {
		t.Fatalf("idempotency conflict = %v, want ErrJobIdempotencyConflict", err)
	}
}

func TestRunnerCompletesJobAcrossServiceRestart(t *testing.T) {
	store := NewMemoryOperationStore()
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	service := NewServiceWithOperationStore(testProvider{}, store)
	service.now = func() time.Time { return now }
	created, err := service.CreateJob(context.Background(), JobInput{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", RequestID: "request-alpha", ApprovalID: "approval-alpha", IdempotencyKey: "runner-job"})
	if err != nil || created.Attempt != 1 {
		t.Fatalf("create job: %#v, %v", created, err)
	}
	claimed, err := service.ClaimJob(context.Background(), created.JobID, JobClaimInput{RunnerID: "runner-alpha", IdempotencyKey: "claim-once"})
	if err != nil || claimed.Status != "running" || claimed.LeaseToken == "" || claimed.LeaseOwner != "runner-alpha" || claimed.LeaseExpiresAt == nil {
		t.Fatalf("claim job: %#v, %v", claimed, err)
	}

	restarted := NewServiceWithOperationStore(testProvider{}, store)
	restarted.now = func() time.Time { return now.Add(10 * time.Second) }
	heartbeat, err := restarted.HeartbeatJob(context.Background(), created.JobID, JobHeartbeatInput{RunnerID: "runner-alpha", LeaseToken: claimed.LeaseToken, IdempotencyKey: "heartbeat-once"})
	if err != nil || heartbeat.Status != "running" || !heartbeat.LeaseExpiresAt.After(*claimed.LeaseExpiresAt) {
		t.Fatalf("heartbeat job: %#v, %v", heartbeat, err)
	}
	completed, err := restarted.CompleteJob(context.Background(), created.JobID, JobCompleteInput{RunnerID: "runner-alpha", LeaseToken: claimed.LeaseToken, ArtifactIDs: []string{"artifact-alpha"}, ReviewIDs: []string{"review-alpha"}, IdempotencyKey: "complete-once"})
	if err != nil || completed.Status != "succeeded" || len(completed.ArtifactIDs) != 1 || len(completed.ReviewIDs) != 1 {
		t.Fatalf("complete job: %#v, %v", completed, err)
	}
	loaded, err := NewServiceWithOperationStore(testProvider{}, store).Job(context.Background(), created.JobID)
	if err != nil || loaded.Status != "succeeded" {
		t.Fatalf("load completed job: %#v, %v", loaded, err)
	}
	operations, _ := store.List(context.Background())
	payload, _ := json.Marshal(operations)
	if strings.Contains(string(payload), claimed.LeaseToken) {
		t.Fatalf("operation log leaked lease token")
	}
	operationIDs := map[string]bool{}
	for _, operation := range operations {
		if operationIDs[operation.ID] {
			t.Fatalf("duplicate operation id %q", operation.ID)
		}
		operationIDs[operation.ID] = true
	}
}

func TestRunnerHeartbeatsUseBoundedPointQueriesAndPersistence(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	service := NewServiceWithOperationStore(testProvider{}, store)
	service.now = func() time.Time { return now }
	created, err := service.CreateJob(ctx, JobInput{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", RequestID: "request-alpha", ApprovalID: "approval-alpha", IdempotencyKey: "bounded-heartbeat-job"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := service.ClaimJob(ctx, created.JobID, JobClaimInput{RunnerID: "runner-alpha", IdempotencyKey: "bounded-heartbeat-claim"})
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewServiceWithOperationStore(testProvider{}, rejectFullOperationListStore{OperationStore: store})
	for index := 0; index < 50; index++ {
		now = now.Add(time.Second)
		restarted.now = func() time.Time { return now }
		heartbeat, heartbeatErr := restarted.HeartbeatJob(ctx, created.JobID, JobHeartbeatInput{
			RunnerID: "runner-alpha", LeaseToken: claimed.LeaseToken, IdempotencyKey: fmt.Sprintf("heartbeat-%d", index),
		})
		if heartbeatErr != nil || heartbeat.Status != "running" {
			t.Fatalf("heartbeat %d=%#v err=%v", index, heartbeat, heartbeatErr)
		}
	}
	operations, err := store.List(ctx)
	if err != nil || len(operations) != 3 {
		t.Fatalf("operations=%#v err=%v, want create + claim + one bounded heartbeat", operations, err)
	}
	loadedService := NewServiceWithOperationStore(testProvider{}, rejectFullOperationListStore{OperationStore: store})
	loadedService.now = func() time.Time { return now }
	loaded, err := loadedService.Job(ctx, created.JobID)
	if err != nil || loaded.Status != "running" || loaded.LeaseExpiresAt == nil || !loaded.LeaseExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("loaded heartbeat state=%#v err=%v", loaded, err)
	}
}

func TestRunnerLeaseMismatchAndEvidenceValidation(t *testing.T) {
	service := NewService(testProvider{})
	created, err := service.CreateJob(context.Background(), JobInput{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", RequestID: "request-alpha", ApprovalID: "approval-alpha", IdempotencyKey: "lease-job"})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	claimed, err := service.ClaimJob(context.Background(), created.JobID, JobClaimInput{RunnerID: "runner-alpha", IdempotencyKey: "lease-claim"})
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if _, err := service.HeartbeatJob(context.Background(), created.JobID, JobHeartbeatInput{RunnerID: "runner-beta", LeaseToken: claimed.LeaseToken, IdempotencyKey: "wrong-owner"}); !errors.Is(err, ErrJobLeaseMismatch) {
		t.Fatalf("owner mismatch = %v, want ErrJobLeaseMismatch", err)
	}
	if _, err := service.CompleteJob(context.Background(), created.JobID, JobCompleteInput{RunnerID: "runner-alpha", LeaseToken: "wrong-token", ArtifactIDs: []string{"artifact-alpha"}, ReviewIDs: []string{"review-alpha"}, IdempotencyKey: "wrong-token"}); !errors.Is(err, ErrJobLeaseMismatch) {
		t.Fatalf("token mismatch = %v, want ErrJobLeaseMismatch", err)
	}
	if _, err := service.CompleteJob(context.Background(), created.JobID, JobCompleteInput{RunnerID: "runner-alpha", LeaseToken: claimed.LeaseToken, IdempotencyKey: "missing-evidence"}); !errors.Is(err, ErrInvalidJobInput) {
		t.Fatalf("missing evidence = %v, want ErrInvalidJobInput", err)
	}
}

func TestExpiredJobCanRetryAndFail(t *testing.T) {
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	service := NewService(testProvider{})
	service.now = func() time.Time { return now }
	created, _ := service.CreateJob(context.Background(), JobInput{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", RequestID: "request-alpha", ApprovalID: "approval-alpha", IdempotencyKey: "retry-job"})
	claimed, err := service.ClaimJob(context.Background(), created.JobID, JobClaimInput{RunnerID: "runner-alpha", IdempotencyKey: "retry-claim"})
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}
	service.now = func() time.Time { return now.Add(31 * time.Second) }
	timedOut, err := service.Job(context.Background(), created.JobID)
	if err != nil || timedOut.Status != "timed_out" {
		t.Fatalf("timeout job: %#v, %v", timedOut, err)
	}
	retried, err := service.RetryJob(context.Background(), created.JobID, "retry-once")
	if err != nil || retried.Status != "queued" || retried.Attempt != 2 || retried.LeaseOwner != "" {
		t.Fatalf("retry job: %#v, %v", retried, err)
	}
	claimed, err = service.ClaimJob(context.Background(), created.JobID, JobClaimInput{RunnerID: "runner-alpha", IdempotencyKey: "retry-claim-2"})
	if err != nil {
		t.Fatalf("claim retry: %v", err)
	}
	failed, err := service.FailJob(context.Background(), created.JobID, JobFailInput{RunnerID: "runner-alpha", LeaseToken: claimed.LeaseToken, ErrorCode: "runner_failed", IdempotencyKey: "fail-once"})
	if err != nil || failed.Status != "failed" || failed.ErrorCode != "runner_failed" {
		t.Fatalf("fail job: %#v, %v", failed, err)
	}
}

func TestProductionCatalogKeepsBasicAndProAvailable(t *testing.T) {
	t.Setenv("NODE_ENV", "production")
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_ID", "")
	t.Setenv("OPL_PRO_COMPUTE_NODE_POOL_ID", "np-pro-must-not-enable-production")
	provider := NewTencentProvider()
	provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
		t.Fatal("catalog availability must not call Tencent provisioner")
		return provisionerResponse{}, nil
	}
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
		t.Fatal("catalog availability must not call Kubernetes")
		return nil, nil
	}

	catalog := NewService(provider).Catalog(context.Background())
	if len(catalog.WorkspacePackages) != 2 {
		t.Fatalf("workspace packages = %#v, want Basic and Pro", catalog.WorkspacePackages)
	}
	basic, pro := catalog.WorkspacePackages[0], catalog.WorkspacePackages[1]
	if basic.ID != "basic" || basic.CPU != 2 || basic.MemoryGB != 4 || basic.DiskGB != 10 || !basic.Available ||
		pro.ID != "pro" || pro.CPU != 8 || pro.MemoryGB != 16 || pro.DiskGB != 100 || !pro.Available {
		t.Fatalf("unexpected production catalog: %#v", catalog.WorkspacePackages)
	}
}

func TestCreateComputeAllocationPersistsExplicitNodePoolID(t *testing.T) {
	var input ComputeAllocationInput
	if err := json.Unmarshal([]byte(`{"id":"compute-alpha","accountId":"acct-alpha","workspaceId":"ws-alpha","packageId":"basic","nodePoolId":"np-basic"}`), &input); err != nil {
		t.Fatal(err)
	}
	input.IdempotencyKey = "compute-with-explicit-pool"
	allocation, err := NewServiceWithOperationStore(testProvider{}, NewMemoryOperationStore()).CreateComputeAllocation(context.Background(), input)
	if err != nil || allocation.NodePoolID != "np-basic" {
		t.Fatalf("compute allocation = %#v, err=%v", allocation, err)
	}
}

func TestCreateComputeAllocationRequiresExactNodePoolID(t *testing.T) {
	service := NewService(testProvider{})

	allocation, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{
		AccountID:      "acct-alpha",
		WorkspaceID:    "ws-alpha",
		PackageID:      "basic",
		IdempotencyKey: "compute-node-pool-required",
	})
	if err == nil || err.Error() != "compute_node_pool_id_required" || allocation.ID != "" {
		t.Fatalf("allocation=%#v err=%v, want exact NodePool rejection", allocation, err)
	}
	operations, listErr := service.ListOperations(context.Background())
	if listErr != nil || len(operations) != 0 {
		t.Fatalf("rejected request reached operation/provider path: operations=%#v err=%v", operations, listErr)
	}
}

type resourceBoundaryProvider struct {
	testProvider
	computeCalls  int
	storageCalls  int
	storageInputs []StorageVolumeInput
}

func (p *resourceBoundaryProvider) CreateComputeAllocation(ctx context.Context, input ComputeAllocationExecution) (ComputeAllocation, error) {
	p.computeCalls++
	return p.testProvider.CreateComputeAllocation(ctx, input)
}

func (p *resourceBoundaryProvider) CreateStorageVolume(ctx context.Context, input StorageVolumeInput) (StorageVolume, error) {
	p.storageCalls++
	p.storageInputs = append(p.storageInputs, input)
	return p.testProvider.CreateStorageVolume(ctx, input)
}

func TestResourceBoundariesRejectUnknownPackagesAndInvalidStorageBeforeProvider(t *testing.T) {
	provider := &resourceBoundaryProvider{}
	service := NewService(provider)
	for _, packageID := range []string{"", "enterprise"} {
		if _, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: packageID, IdempotencyKey: "invalid-package-" + packageID}); !errors.Is(err, ErrUnsupportedComputePackage) {
			t.Fatalf("package %q err=%v, want %v", packageID, err, ErrUnsupportedComputePackage)
		}
	}
	for _, sizeGB := range []int{0, 9, 15} {
		if _, err := service.CreateStorageVolume(context.Background(), StorageVolumeInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", SizeGB: sizeGB, IdempotencyKey: fmt.Sprintf("invalid-storage-%d", sizeGB)}); !errors.Is(err, ErrInvalidStorageSize) {
			t.Fatalf("storage %dGB err=%v, want %v", sizeGB, err, ErrInvalidStorageSize)
		}
	}
	time.Sleep(20 * time.Millisecond)
	operations, err := service.ListOperations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if provider.computeCalls != 0 || provider.storageCalls != 0 || len(operations) != 0 {
		t.Fatalf("invalid inputs mutated provider/state: compute=%d storage=%d operations=%#v", provider.computeCalls, provider.storageCalls, operations)
	}
}

func TestStorageCreationRequiresMatchingClaimedComputeZoneBeforeProvider(t *testing.T) {
	provider := &resourceBoundaryProvider{}
	service := NewService(provider)
	service.computes["compute-alpha"] = ComputeAllocation{
		ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "running", ProviderData: map[string]string{"zone": "ap-guangzhou-3"},
	}
	valid := StorageVolumeInput{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "storage-zone"}
	for _, tc := range []struct {
		name   string
		mutate func(*StorageVolumeInput)
	}{
		{name: "compute missing", mutate: func(input *StorageVolumeInput) { input.ComputeID = "compute-missing" }},
		{name: "account mismatch", mutate: func(input *StorageVolumeInput) { input.AccountID = "acct-other" }},
		{name: "workspace mismatch", mutate: func(input *StorageVolumeInput) { input.WorkspaceID = "ws-other" }},
		{name: "zone missing", mutate: func(input *StorageVolumeInput) { input.Zone = "" }},
		{name: "zone mismatch", mutate: func(input *StorageVolumeInput) { input.Zone = "ap-guangzhou-4" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			input.IdempotencyKey += "-" + tc.name
			tc.mutate(&input)
			if _, err := service.CreateStorageVolume(context.Background(), input); err == nil {
				t.Fatalf("invalid compute Zone binding must fail: %#v", input)
			}
		})
	}
	if provider.storageCalls != 0 {
		t.Fatalf("invalid Zone bindings reached provider %d times", provider.storageCalls)
	}
	if _, err := service.CreateStorageVolume(context.Background(), valid); err != nil || provider.storageCalls != 1 {
		t.Fatalf("valid Zone binding err=%v calls=%d", err, provider.storageCalls)
	}
}

func TestStorageCreationWithoutIDReplaysStableIdentity(t *testing.T) {
	provider := &resourceBoundaryProvider{}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	service.computes["compute-alpha"] = ComputeAllocation{
		ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "running", ProviderData: map[string]string{"zone": "ap-guangzhou-3"},
	}
	input := StorageVolumeInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "storage-without-id"}

	first, err := service.CreateStorageVolume(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateStorageVolume(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || !strings.HasPrefix(first.ID, "vol_") || second.ID != first.ID || provider.storageCalls != 1 || len(provider.storageInputs) != 1 || provider.storageInputs[0].ID != first.ID {
		t.Fatalf("unstable storage replay: first=%#v second=%#v calls=%d inputs=%#v", first, second, provider.storageCalls, provider.storageInputs)
	}
}

func TestStorageRecoveryExpectationUsesStableFabricOperationIdentity(t *testing.T) {
	provider := &resourceBoundaryProvider{}
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(provider, store)
	service.computes["compute-alpha"] = ComputeAllocation{
		ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready",
		ProviderData: map[string]string{"zone": "ap-guangzhou-3"},
	}
	input := StorageVolumeInput{
		ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", Zone: "ap-guangzhou-3", SizeGB: 10,
		IdempotencyKey: "launch-alpha:storage", ExpectedRecoveryState: "storage_existing_exact", ExpectedProviderResourceID: "disk-existing-alpha",
	}

	first, err := service.CreateStorageVolume(context.Background(), input)
	second, replayErr := service.CreateStorageVolume(context.Background(), input)

	if err != nil || replayErr != nil || provider.storageCalls != 1 || len(provider.storageInputs) != 1 ||
		provider.storageInputs[0].ExpectedRecoveryState != input.ExpectedRecoveryState || provider.storageInputs[0].ExpectedProviderResourceID != input.ExpectedProviderResourceID ||
		!strings.HasPrefix(provider.storageInputs[0].OperationID, "op_create_storage_volume_") || first.OperationID != input.IdempotencyKey || second.OperationID != input.IdempotencyKey {
		t.Fatalf("first=%#v second=%#v err=%v replayErr=%v inputs=%#v calls=%d", first, second, err, replayErr, provider.storageInputs, provider.storageCalls)
	}
	for _, invalid := range []StorageVolumeInput{
		{ID: "storage-invalid-absent", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "invalid-absent", ExpectedRecoveryState: "storage_not_started", ExpectedProviderResourceID: "disk-unexpected"},
		{ID: "storage-invalid-existing", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "invalid-existing", ExpectedRecoveryState: "storage_existing_exact"},
		{ID: "storage-invalid-unknown", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "invalid-unknown", ExpectedRecoveryState: "storage_attempt_unknown"},
	} {
		if _, err := service.CreateStorageVolume(context.Background(), invalid); err == nil || err.Error() != "storage_recovery_expectation_invalid" {
			t.Fatalf("invalid recovery expectation=%#v err=%v", invalid, err)
		}
	}
	if provider.storageCalls != 1 {
		t.Fatalf("invalid recovery expectation reached provider: calls=%d", provider.storageCalls)
	}
}

func TestStorageCreateReplayFlagComesOnlyFromPersistedFabricAttempt(t *testing.T) {
	provider := &resourceBoundaryProvider{}
	store := NewMemoryOperationStore()
	input := StorageVolumeInput{
		ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", Zone: "ap-guangzhou-3", SizeGB: 10,
		IdempotencyKey: "launch-alpha:storage", ExpectedRecoveryState: "storage_not_started",
	}
	operation := newOperation("create_storage_volume", "storage_volume", input.ID, input.AccountID, input.WorkspaceID, input.IdempotencyKey, hashInput(input), time.Now().UTC())
	if err := (&Service{operations: store, now: func() time.Time { return time.Now().UTC() }}).recordOperation(
		context.Background(), operation, "started",
		StorageVolume{ID: input.ID, OperationID: input.IdempotencyKey, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Provider: "tencent-tke"}, nil,
	); err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithOperationStore(provider, store)
	service.computes["compute-alpha"] = ComputeAllocation{
		ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready", ProviderData: map[string]string{"zone": "ap-guangzhou-3"},
	}

	if _, err := service.CreateStorageVolume(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if provider.storageCalls != 1 || len(provider.storageInputs) != 1 || !provider.storageInputs[0].AllowExistingExactReplay ||
		provider.storageInputs[0].OperationID != operation.OperationID {
		t.Fatalf("persisted attempt did not bind replay: inputs=%#v calls=%d operation=%#v", provider.storageInputs, provider.storageCalls, operation)
	}
}

type partialStorageProvider struct{ testProvider }

func (*partialStorageProvider) CreateStorageVolume(_ context.Context, input StorageVolumeInput) (StorageVolume, error) {
	return StorageVolume{
		ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "pending", Provider: "tencent-tke",
		ProviderResourceID: "disk-storage-alpha", ProviderRequestID: "req-create-cbs", CBSStatus: "UNATTACHED", DiskType: "CLOUD_BSSD",
		RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-16 00:00:00", Zone: input.Zone, ProviderData: map[string]string{"diskChargeType": "PREPAID"},
	}, errors.New("cluster unavailable")
}

func TestStorageCreateFailureRecordsPartialCBSIdentity(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(&partialStorageProvider{}, store)
	service.computes["compute-alpha"] = ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "running", ProviderData: map[string]string{"zone": "ap-guangzhou-3"}}
	volume, err := service.CreateStorageVolume(context.Background(), StorageVolumeInput{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "partial-storage"})
	if err == nil || volume.ProviderResourceID != "disk-storage-alpha" {
		t.Fatalf("partial volume=%#v err=%v", volume, err)
	}
	operations, listErr := service.ListOperations(context.Background())
	if listErr != nil {
		t.Fatal(listErr)
	}
	found := false
	for _, operation := range operations {
		if operation.Action == "create_storage_volume" && operation.Status == "failed" && strings.Contains(fmt.Sprint(operation.RedactedProviderPayload), "disk-storage-alpha") {
			found = true
		}
	}
	if !found {
		t.Fatalf("failed operation lost partial CBS identity: %#v", operations)
	}
}

func TestStorageCreateFailureKeepsPartialCBSIdentityForSameProcessSync(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(&partialStorageProvider{}, store)
	service.computes["compute-alpha"] = ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "running", ProviderData: map[string]string{"zone": "ap-guangzhou-3"}}

	created, err := service.CreateStorageVolume(context.Background(), StorageVolumeInput{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "partial-storage"})
	stored, ok := service.GetStorageVolume(context.Background(), created.ID)
	if err == nil || !ok || stored.Status != "quarantined" || stored.ProviderResourceID != "disk-storage-alpha" {
		t.Fatalf("partial storage was not recoverable in process: created=%#v stored=%#v ok=%v err=%v", created, stored, ok, err)
	}
	recovered, err := service.SyncStorageVolume(context.Background(), created.ID)
	if err != nil || recovered.Status != "ready" || recovered.ProviderResourceID != "disk-storage-alpha" {
		t.Fatalf("same-process storage recovery=%#v err=%v", recovered, err)
	}
}

func TestServiceReplaysPartialCBSIdentityFromFailedCreate(t *testing.T) {
	store := NewMemoryOperationStore()
	original := NewServiceWithOperationStore(&partialStorageProvider{}, store)
	original.computes["compute-alpha"] = ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "running", ProviderData: map[string]string{"zone": "ap-guangzhou-3"}}
	created, err := original.CreateStorageVolume(context.Background(), StorageVolumeInput{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "partial-storage"})
	if err == nil || created.ProviderResourceID != "disk-storage-alpha" {
		t.Fatalf("partial create=%#v err=%v", created, err)
	}

	replayed := NewServiceWithOperationStore(&partialStorageProvider{}, store)
	stored, ok := replayed.GetStorageVolume(context.Background(), created.ID)
	if !ok || stored.Status != "quarantined" || stored.ProviderResourceID != "disk-storage-alpha" || stored.AccountID != "acct-alpha" || stored.WorkspaceID != "ws-alpha" {
		t.Fatalf("replayed partial storage=%#v ok=%v", stored, ok)
	}
	recovered, err := replayed.SyncStorageVolume(context.Background(), created.ID)
	if err != nil || recovered.Status != "ready" || recovered.ProviderResourceID != "disk-storage-alpha" {
		t.Fatalf("restarted storage recovery=%#v err=%v", recovered, err)
	}
}

type failingStorageSyncProvider struct{ testProvider }

func (*failingStorageSyncProvider) SyncStorageVolume(context.Context, StorageVolume) (StorageVolume, error) {
	return StorageVolume{}, errors.New("provider readback unavailable")
}

func TestStorageSyncFailurePreservesKnownIdentity(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(&failingStorageSyncProvider{}, store)
	existing := StorageVolume{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Provider: "tencent-tke", ProviderResourceID: "disk-storage-alpha", ProviderRequestID: "req-create-cbs", Status: "pending"}
	service.volumes[existing.ID] = existing

	volume, err := service.SyncStorageVolume(context.Background(), existing.ID)
	if err == nil || volume.ID != existing.ID || volume.ProviderResourceID != existing.ProviderResourceID || volume.ProviderRequestID != existing.ProviderRequestID {
		t.Fatalf("sync failure lost known volume: volume=%#v err=%v", volume, err)
	}
	operations, listErr := service.ListOperations(context.Background())
	if listErr != nil {
		t.Fatal(listErr)
	}
	found := false
	for _, operation := range operations {
		if operation.Action == "sync_storage_volume" && operation.Status == "failed" && strings.Contains(fmt.Sprint(operation.RedactedProviderPayload), existing.ProviderResourceID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("failed sync operation lost known volume: %#v", operations)
	}
}

func TestDryRunComputeAllocationRecordsProviderRequestIDWithoutLedgerTypes(t *testing.T) {
	service := NewService(testProvider{})
	allocation, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{
		AccountID:      "acct-alpha",
		WorkspaceID:    "ws-alpha",
		PackageID:      "basic",
		NodePoolID:     "np-basic",
		IdempotencyKey: "fabric-compute-once",
		DryRun:         true,
	})
	if err != nil {
		t.Fatalf("create allocation: %v", err)
	}
	if allocation.ProviderRequestID == "" {
		t.Fatalf("expected provider request id")
	}
	if strings.Contains(strings.ToLower(allocation.ProviderRequestID), "ledger") {
		t.Fatalf("provider request id must not reference ledger: %s", allocation.ProviderRequestID)
	}
}

func TestComputeAllocationReturnsProvisioningBeforeProviderCompletes(t *testing.T) {
	provider := &blockingProvider{done: make(chan struct{})}
	service := NewService(provider)

	allocation, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{
		AccountID:      "acct-alpha",
		WorkspaceID:    "ws-alpha",
		PackageID:      "basic",
		NodePoolID:     "np-basic",
		IdempotencyKey: "compute-once",
	})
	if err != nil {
		t.Fatalf("create allocation: %v", err)
	}
	if allocation.Status != "provisioning" || allocation.ID == "" {
		t.Fatalf("initial allocation = %#v, want provisioning with id", allocation)
	}
	current, ok := service.GetComputeAllocation(context.Background(), allocation.ID)
	if !ok || current.Status != "provisioning" {
		t.Fatalf("stored allocation = %#v ok=%v, want provisioning", current, ok)
	}

	close(provider.done)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current, ok = service.GetComputeAllocation(context.Background(), allocation.ID)
		if ok && current.Status == "running" {
			if current.ID != allocation.ID || current.NodeName == "" || current.MachineName == "" || current.InstanceID == "" {
				t.Fatalf("completed allocation lost identity: %#v", current)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("allocation did not become running: %#v", current)
}

type countingBlockedAllocationProvider struct {
	testProvider
	calls   atomic.Int32
	entered chan ComputeAllocationExecution
	release chan struct{}
}

func (p *countingBlockedAllocationProvider) CreateComputeAllocation(ctx context.Context, input ComputeAllocationExecution) (ComputeAllocation, error) {
	p.calls.Add(1)
	p.entered <- input
	select {
	case <-p.release:
		return testProvider{}.CreateComputeAllocation(ctx, input)
	case <-ctx.Done():
		return ComputeAllocation{}, ctx.Err()
	}
}

func TestCreateComputeAllocationReplaysStartedClaimWithoutIncreasingDemand(t *testing.T) {
	provider := &countingBlockedAllocationProvider{entered: make(chan ComputeAllocationExecution, 2), release: make(chan struct{})}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	input := ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "compute-replay"}
	t.Cleanup(func() { close(provider.release) })

	first, err := service.CreateComputeAllocation(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	execution := <-provider.entered
	replayed, err := service.CreateComputeAllocation(context.Background(), input)
	if err != nil || replayed.ID != first.ID || replayed.Status != first.Status || execution.Plan.TargetReplicas != 1 || provider.calls.Load() != 1 {
		t.Fatalf("first=%#v replayed=%#v err=%v execution=%#v calls=%d", first, replayed, err, execution, provider.calls.Load())
	}
	operations, err := service.ListOperations(context.Background())
	if err != nil || len(operations) != 1 || operations[0].Status != "started" {
		t.Fatalf("operations=%#v err=%v", operations, err)
	}
}

func TestServiceResumesStartedComputeClaimAfterRestart(t *testing.T) {
	store := NewMemoryOperationStore()
	input := ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "compute-restart"}
	now := time.Now().UTC()
	allocation := ComputeAllocation{
		ID: "ca_" + stableSuffix("create_compute_allocation", input.IdempotencyKey)[:18], AccountID: input.AccountID, WorkspaceID: input.WorkspaceID,
		PackageID: input.PackageID, NodePoolID: input.NodePoolID, Status: "provisioning", Provider: "tencent-tke", ProviderRequestID: providerRequestID("compute", input.IdempotencyKey), CreatedAt: now,
	}
	operation := newOperation("create_compute_allocation", "compute_allocation", allocation.ID, input.AccountID, input.WorkspaceID, input.IdempotencyKey, hashInput(input), now)
	operation.ID = "fop_compute_claim_" + stableSuffix("create_compute_allocation", input.IdempotencyKey)
	operation.Status = "started"
	operation.CreatedAt = now
	fillOperationResource(&operation, allocation)
	if _, claimed, err := store.ClaimRuntime(context.Background(), operation); err != nil || !claimed {
		t.Fatalf("seed started compute claim: claimed=%v err=%v", claimed, err)
	}
	release := make(chan struct{})
	close(release)
	provider := &countingBlockedAllocationProvider{entered: make(chan ComputeAllocationExecution, 2), release: release}

	restarted := NewServiceWithOperationStore(provider, store)
	if replayed, err := restarted.CreateComputeAllocation(context.Background(), input); err != nil || replayed.ID != allocation.ID {
		t.Fatalf("replay started compute claim: allocation=%#v err=%v", replayed, err)
	}
	waitForOperation(t, restarted, "create_compute_allocation", "compute_allocation", allocation.ID, "succeeded")
	current, ok := restarted.GetComputeAllocation(context.Background(), allocation.ID)
	if !ok || current.Status != "running" || provider.calls.Load() != 1 {
		t.Fatalf("restarted compute=%#v ok=%v providerCalls=%d", current, ok, provider.calls.Load())
	}
}

func TestCreateComputeAllocationRejectsSameKeyWithDifferentRequest(t *testing.T) {
	provider := &countingBlockedAllocationProvider{entered: make(chan ComputeAllocationExecution, 2), release: make(chan struct{})}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	t.Cleanup(func() { close(provider.release) })

	first, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "compute-conflict"})
	if err != nil {
		t.Fatal(err)
	}
	<-provider.entered
	replayed, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-other", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "compute-conflict"})
	if err == nil || err.Error() != "compute_idempotency_conflict" || replayed.ID != "" || provider.calls.Load() != 1 {
		t.Fatalf("first=%#v replayed=%#v err=%v calls=%d", first, replayed, err, provider.calls.Load())
	}
}

func TestCreateComputeAllocationConcurrentSameKeyClaimsOnce(t *testing.T) {
	provider := &countingBlockedAllocationProvider{entered: make(chan ComputeAllocationExecution, 20), release: make(chan struct{})}
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(provider, store)
	input := ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "compute-concurrent"}
	t.Cleanup(func() { close(provider.release) })

	const callers = 16
	results := make(chan ComputeAllocation, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.CreateComputeAllocation(context.Background(), input)
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]bool{}
	for result := range results {
		ids[result.ID] = true
	}
	operations, err := store.List(context.Background())
	if err != nil || len(ids) != 1 || len(operations) != 1 {
		t.Fatalf("ids=%#v operations=%#v err=%v", ids, operations, err)
	}
}

type failedAllocationProvider struct{ testProvider }

func (failedAllocationProvider) CreateComputeAllocation(_ context.Context, input ComputeAllocationExecution) (ComputeAllocation, error) {
	return input.Allocation, fmt.Errorf("compute_provider_rejected")
}

func TestCreateComputeAllocationReplaysSucceededAndFailedResults(t *testing.T) {
	t.Run("succeeded", func(t *testing.T) {
		service := NewServiceWithOperationStore(testProvider{}, NewMemoryOperationStore())
		input := ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "compute-succeeded"}
		first, err := service.CreateComputeAllocation(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		waitForOperation(t, service, "create_compute_allocation", "compute_allocation", first.ID, "succeeded")
		replayed, err := service.CreateComputeAllocation(context.Background(), input)
		if err != nil || replayed.ID != first.ID || replayed.Status != "running" {
			t.Fatalf("first=%#v replayed=%#v err=%v", first, replayed, err)
		}
	})

	t.Run("failed", func(t *testing.T) {
		service := NewServiceWithOperationStore(failedAllocationProvider{}, NewMemoryOperationStore())
		input := ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "compute-failed"}
		first, err := service.CreateComputeAllocation(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		waitForOperation(t, service, "create_compute_allocation", "compute_allocation", first.ID, "failed")
		replayed, err := service.CreateComputeAllocation(context.Background(), input)
		if err == nil || err.Error() != "compute_operation_failed" || replayed.ID != first.ID || replayed.Status != "quarantined" {
			t.Fatalf("first=%#v replayed=%#v err=%v", first, replayed, err)
		}
	})
}

func TestResourceMutationsAppendFabricOperationFacts(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	ctx := context.Background()

	compute, err := service.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "ops-compute"})
	if err != nil {
		t.Fatalf("create compute: %v", err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")

	volume, err := service.CreateStorageVolume(ctx, StorageVolumeInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: compute.ID, Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "ops-storage"})
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	attachment, err := service.CreateStorageAttachment(ctx, StorageAttachmentInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, IdempotencyKey: "ops-attach"})
	if err != nil {
		t.Fatalf("attach storage: %v", err)
	}
	runtime, err := service.CreateWorkspaceRuntime(ctx, WorkspaceRuntimeInput{
		WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID,
		AttachmentID: attachment.ID, AttachmentOperationID: attachment.OperationID,
		RuntimeOperationID: "ops-runtime", ImageID: testWorkspaceRuntimeImageID(),
		GatewaySecretRef: gatewaySecretName("ws-alpha"), IdempotencyKey: "ops-runtime",
	})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if _, err := service.DetachStorageAttachment(ctx, attachment.ID); err != nil {
		t.Fatalf("detach storage: %v", err)
	}
	if _, err := service.DestroyStorageVolume(ctx, volume.ID); err != nil {
		t.Fatalf("destroy storage: %v", err)
	}
	if _, err := service.DestroyComputeAllocation(ctx, compute.ID); err != nil {
		t.Fatalf("destroy compute: %v", err)
	}
	waitForOperation(t, service, "destroy_compute_allocation", "compute_allocation", compute.ID, "succeeded")

	operations, err := service.ListOperations(ctx)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	for _, expected := range []struct {
		action       string
		resourceKind string
		resourceID   string
		status       string
	}{
		{"create_storage_volume", "storage_volume", volume.ID, "succeeded"},
		{"create_storage_attachment", "storage_attachment", attachment.ID, "succeeded"},
		{"create_workspace_runtime", "workspace_runtime", runtime.WorkspaceID, "succeeded"},
		{"detach_storage_attachment", "storage_attachment", attachment.ID, "succeeded"},
		{"destroy_storage_volume", "storage_volume", volume.ID, "succeeded"},
		{"destroy_compute_allocation", "compute_allocation", compute.ID, "succeeded"},
	} {
		assertOperationFact(t, operations, expected.action, expected.resourceKind, expected.resourceID, expected.status)
	}
}

type providerAttachmentIdentityProvider struct{ testProvider }

func (providerAttachmentIdentityProvider) CreateStorageAttachment(_ context.Context, input StorageAttachmentInput, _ ComputeAllocation, _ StorageVolume) (StorageAttachment, error) {
	id := "att_" + stableSuffix(input.OperationID)[:18]
	return StorageAttachment{
		ID: id, OperationID: input.OperationID, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID,
		Status: "attached", Provider: "tencent-tke", ProviderAttachmentID: "pv/volume-alpha:pvc/volume-alpha-data",
		ProviderRequestID: providerRequestID("storage-attach", input.IdempotencyKey),
		CostTags: map[string]string{
			"opl_account_id": "acct-alpha", "opl_workspace_id": input.WorkspaceID, "opl_resource_id": id, "opl_operation_id": input.OperationID,
		},
	}, nil
}

func TestCreateStorageAttachmentOperationPersistsProviderResourceIdentity(t *testing.T) {
	service := NewServiceWithOperationStore(providerAttachmentIdentityProvider{}, NewMemoryOperationStore())
	ctx := context.Background()
	compute, err := service.CreateComputeAllocation(ctx, ComputeAllocationInput{
		AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "workspace-launch-alpha:compute",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")
	volume, err := service.CreateStorageVolume(ctx, StorageVolumeInput{
		AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: compute.ID, Zone: "ap-guangzhou-3", SizeGB: 10,
		IdempotencyKey: "workspace-launch-alpha:storage",
	})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := service.CreateStorageAttachment(ctx, StorageAttachmentInput{
		WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, IdempotencyKey: "workspace-launch-alpha:attachment",
	})
	if err != nil {
		t.Fatal(err)
	}
	operations, err := service.ListOperations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var stored FabricOperation
	for _, operation := range operations {
		if operation.Action == "create_storage_attachment" {
			stored = operation
		}
	}
	var storedAttachment StorageAttachment
	expectedOperationID := "op_create_storage_attachment_" + stableSuffix(
		"workspace-launch-alpha:attachment", "storage_attachment", "create_storage_attachment",
	)[:12]
	expectedRequestHash := hashInput(StorageAttachmentInput{
		WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID,
	})
	if stored.ID == "" || stored.Status != "succeeded" || stored.ResourceID != attachment.ID || !strings.HasPrefix(stored.ResourceID, "att_") ||
		stored.IdempotencyKey != "workspace-launch-alpha:attachment" || stored.OperationID != expectedOperationID || stored.RequestHash != expectedRequestHash ||
		!decodeOperationResource(stored, &storedAttachment) || storedAttachment.ID != attachment.ID || storedAttachment.OperationID != stored.IdempotencyKey {
		t.Fatalf("attachment=%#v operation=%#v storedAttachment=%#v", attachment, stored, storedAttachment)
	}
}

func TestWorkspaceRuntimeCreationDoesNotReturnCredential(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	ctx := context.Background()

	compute, err := service.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "access-compute"})
	if err != nil {
		t.Fatalf("create compute: %v", err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")
	volume, err := service.CreateStorageVolume(ctx, StorageVolumeInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: compute.ID, Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "access-storage"})
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	attachment, err := service.CreateStorageAttachment(ctx, StorageAttachmentInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, IdempotencyKey: "access-attachment"})
	if err != nil {
		t.Fatalf("create attachment: %v", err)
	}
	runtime, err := service.CreateWorkspaceRuntime(ctx, WorkspaceRuntimeInput{
		WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, AttachmentID: attachment.ID,
		AttachmentOperationID: "access-attachment", RuntimeOperationID: "access-runtime",
		ImageID: testWorkspaceRuntimeImageID(), GatewaySecretRef: gatewaySecretName("ws-alpha"), IdempotencyKey: "access-runtime",
	})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if runtime.Access.Password != "" || runtime.Access.CredentialStatus != "configured" || runtime.Access.SecretRef != "opl-ca-test-env" {
		t.Fatalf("runtime creation must return credential metadata only: %#v", runtime.Access)
	}

	operations, err := service.ListOperations(ctx)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	for _, operation := range operations {
		payload := operation.RedactedProviderPayload
		if strings.Contains(strings.ToLower(fmt.Sprint(payload)), "runtime-password-alpha") {
			t.Fatalf("fabric operation leaked workspace password: %#v", operation)
		}
	}
}

func TestWorkspaceRuntimeStatusBackfillsCreatedRuntimeIdentity(t *testing.T) {
	provider := liveRuntimeWithoutIDProvider{runtimeIDs: map[string]string{"runtime-status-identity": "runtime-stable"}}
	service := runtimeTestService(provider, NewMemoryOperationStore())
	created, err := service.CreateWorkspaceRuntime(context.Background(), runtimeTestInput("runtime-status-identity"))
	if err != nil || created.ID != "runtime-stable" {
		t.Fatalf("created runtime=%#v err=%v", created, err)
	}
	service.operations = rejectFullOperationListStore{OperationStore: service.operations}

	live, err := service.WorkspaceRuntimeStatus(context.Background(), "workspace-alpha")
	if err != nil {
		t.Fatalf("runtime status: %v", err)
	}
	if live.ID != created.ID || live.Status != "running" || !live.Ready || live.URL != "https://workspace.medopl.cn/w/workspace-alpha/" || live.ServiceName != "runtime-live" || !reflect.DeepEqual(live.Checks, []Check{{Name: "deployment_ready", OK: true}}) {
		t.Fatalf("live runtime=%#v created=%#v", live, created)
	}
	if live.Access.Password != "" || live.Access.Username != "opl" {
		t.Fatalf("runtime status leaked credentials: %#v", live.Access)
	}
	credentials, err := service.WorkspaceRuntimeCredentials(context.Background(), "acct-alpha", "workspace-alpha")
	if err != nil || credentials.Access.Password != "runtime-password-alpha" || credentials.ID != created.ID {
		t.Fatalf("runtime credentials=%#v err=%v", credentials, err)
	}
	other, err := service.WorkspaceRuntimeCredentials(context.Background(), "acct-other", "workspace-alpha")
	if err == nil || other.Access.Password != "" {
		t.Fatalf("cross-account runtime credentials=%#v err=%v", other, err)
	}
}

func TestWorkspaceRuntimeCredentialsRejectsUnownedRuntimeState(t *testing.T) {
	service := runtimeTestService(liveRuntimeWithoutIDProvider{status: "provisioning"}, NewMemoryOperationStore())
	status, err := service.WorkspaceRuntimeStatus(context.Background(), "workspace-alpha")
	if err != nil || status.Status != "provisioning" || status.Access.Password != "" {
		t.Fatalf("runtime status=%#v err=%v", status, err)
	}
	credentials, err := service.WorkspaceRuntimeCredentials(context.Background(), "acct-alpha", "workspace-alpha")
	if err == nil || credentials.Access.Password != "" {
		t.Fatalf("unowned runtime credentials=%#v err=%v", credentials, err)
	}
}

func TestProviderFactsBatchBackfillsRuntimeIdentity(t *testing.T) {
	provider := liveRuntimeWithoutIDProvider{runtimeIDs: map[string]string{"runtime-provider-facts": "runtime-stable"}}
	service := runtimeTestService(provider, NewMemoryOperationStore())
	created, err := service.CreateWorkspaceRuntime(context.Background(), runtimeTestInput("runtime-provider-facts"))
	if err != nil {
		t.Fatal(err)
	}

	batch, err := service.ProviderFactsBatch(context.Background(), ProviderFactsBatchInput{Items: []ProviderFactInput{{
		AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", ResourceType: "runtime", ResourceID: created.ID,
	}}})
	if err != nil || len(batch.Items) != 1 || !batch.Items[0].Available || batch.Items[0].ResourceID != created.ID || batch.Items[0].Facts.ProviderID != "runtime-live" {
		t.Fatalf("runtime provider facts=%#v err=%v", batch, err)
	}
}

type boundedProviderFactsRuntimeProvider struct {
	testProvider
	mu        sync.Mutex
	active    int
	maxActive int
	deadlines []time.Time
	started   chan struct{}
	release   <-chan struct{}
}

func (p *boundedProviderFactsRuntimeProvider) WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (WorkspaceRuntime, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return WorkspaceRuntime{}, errors.New("provider facts batch deadline missing")
	}
	p.mu.Lock()
	p.active++
	if p.active > p.maxActive {
		p.maxActive = p.active
	}
	p.deadlines = append(p.deadlines, deadline)
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.active--
		p.mu.Unlock()
	}()
	p.started <- struct{}{}
	select {
	case <-p.release:
		return WorkspaceRuntime{ID: "rt-" + workspaceID, WorkspaceID: workspaceID, Status: "running", Ready: true}, nil
	case <-ctx.Done():
		return WorkspaceRuntime{}, ctx.Err()
	}
}

func TestProviderFactsBatchBoundsWorkersAndSharesOneDeadline(t *testing.T) {
	release := make(chan struct{})
	provider := &boundedProviderFactsRuntimeProvider{started: make(chan struct{}, 50), release: release}
	service := NewService(provider)
	items := make([]ProviderFactInput, 0, 50)
	for index := 1; index <= 50; index++ {
		workspaceID := fmt.Sprintf("workspace-%02d", index)
		items = append(items, ProviderFactInput{AccountID: "acct-alpha", WorkspaceID: workspaceID, ResourceType: "runtime", ResourceID: "rt-" + workspaceID})
	}
	result := make(chan ProviderFactsBatch, 1)
	errs := make(chan error, 1)
	go func() {
		batch, err := service.ProviderFactsBatch(context.Background(), ProviderFactsBatchInput{Items: items})
		result <- batch
		errs <- err
	}()
	for index := 0; index < 8; index++ {
		select {
		case <-provider.started:
		case <-time.After(time.Second):
			close(release)
			<-result
			<-errs
			t.Fatal("provider facts batch did not start the fixed worker pool")
		}
	}
	select {
	case <-provider.started:
		close(release)
		<-result
		<-errs
		t.Fatal("provider facts batch exceeded eight active readers")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	batch, err := <-result, <-errs
	if err != nil || len(batch.Items) != 50 {
		t.Fatalf("provider facts batch=%#v err=%v", batch, err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.maxActive != 8 || len(provider.deadlines) != 50 {
		t.Fatalf("provider facts concurrency=%d deadlines=%d", provider.maxActive, len(provider.deadlines))
	}
	for _, deadline := range provider.deadlines[1:] {
		if !deadline.Equal(provider.deadlines[0]) {
			t.Fatalf("provider facts did not use one batch deadline: first=%s next=%s", provider.deadlines[0], deadline)
		}
	}
}

func TestWorkspaceRuntimeStatusRejectsMultipleCreatedIdentityCandidates(t *testing.T) {
	provider := liveRuntimeWithoutIDProvider{runtimeIDs: map[string]string{"runtime-status-old": "runtime-old", "runtime-status-new": "runtime-new"}}
	store := NewMemoryOperationStore()
	service := runtimeTestService(provider, store)
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	if _, err := service.CreateWorkspaceRuntime(context.Background(), runtimeTestInput("runtime-status-old")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := service.CreateWorkspaceRuntime(context.Background(), runtimeTestInput("runtime-status-new")); err != nil {
		t.Fatal(err)
	}
	store.operation[0], store.operation[1] = store.operation[1], store.operation[0]

	live, err := service.WorkspaceRuntimeStatus(context.Background(), "workspace-alpha")
	if err == nil || live.ID != "" {
		t.Fatalf("multiple Runtime candidates were accepted: live=%#v err=%v", live, err)
	}
}

func TestWorkspaceRuntimeStatusFailsClosedWithoutCreatedIdentityEvidence(t *testing.T) {
	malformed := NewMemoryOperationStore()
	malformedOperation := newOperation(
		"create_workspace_runtime", "workspace_runtime", "workspace-alpha", "acct-alpha", "workspace-alpha",
		"runtime-malformed", "runtime-malformed-hash", time.Date(2026, 8, 14, 5, 0, 0, 0, time.UTC),
	)
	malformedOperation.ID = "fop-runtime-malformed"
	malformedOperation.Status = "succeeded"
	malformedOperation.CreatedAt = malformedOperation.StartedAt
	malformedOperation.RedactedProviderPayload = map[string]any{"resource": "malformed"}
	if err := malformed.Append(context.Background(), malformedOperation); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		store OperationStore
	}{
		{name: "missing", store: NewMemoryOperationStore()},
		{name: "malformed", store: malformed},
		{name: "store unavailable", store: failingRuntimeIdentityCandidatesStore{OperationStore: NewMemoryOperationStore()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := runtimeTestService(liveRuntimeWithoutIDProvider{}, tc.store)
			if runtime, err := service.WorkspaceRuntimeStatus(context.Background(), "workspace-alpha"); err == nil {
				t.Fatalf("runtime=%#v err=nil", runtime)
			}
		})
	}
}

func TestWorkspaceRuntimeRequiresOwnedGatewaySecretReference(t *testing.T) {
	provider := &countingRuntimeProvider{}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	service.computes["compute-alpha"] = ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "running"}
	service.volumes["storage-alpha"] = StorageVolume{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready"}
	service.attachments["attachment-alpha"] = StorageAttachment{ID: "attachment-alpha", OperationID: "workspace-launch-alpha:attachment", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha", Status: "attached"}
	for _, ref := range []string{"", gatewaySecretName("ws-other")} {
		key := "runtime-ref-" + stableSuffix(ref)[:8]
		_, err := service.CreateWorkspaceRuntime(context.Background(), WorkspaceRuntimeInput{
			WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha", AttachmentID: "attachment-alpha",
			AttachmentOperationID: "workspace-launch-alpha:attachment", RuntimeOperationID: key, ImageID: testWorkspaceRuntimeImageID(),
			GatewaySecretRef: ref, IdempotencyKey: key,
		})
		if err == nil {
			t.Fatalf("runtime must reject Gateway Secret ref %q", ref)
		}
	}
	if provider.calls.Load() != 0 {
		t.Fatalf("invalid Gateway Secret refs reached provider %d times", provider.calls.Load())
	}
	valid, err := service.CreateWorkspaceRuntime(context.Background(), WorkspaceRuntimeInput{
		WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha", AttachmentID: "attachment-alpha",
		AttachmentOperationID: "workspace-launch-alpha:attachment", RuntimeOperationID: "runtime-ref-valid", ImageID: testWorkspaceRuntimeImageID(),
		GatewaySecretRef: gatewaySecretName("ws-alpha"), IdempotencyKey: "runtime-ref-valid",
	})
	if err != nil || !valid.Ready || provider.calls.Load() != 1 {
		t.Fatalf("valid runtime=%#v err=%v calls=%d", valid, err, provider.calls.Load())
	}
}

type countingGatewayProvider struct {
	testProvider
	calls atomic.Int32
}

type failFirstRuntimeSaveStore struct {
	OperationStore
	failed atomic.Bool
}

func (s *failFirstRuntimeSaveStore) SaveRuntime(ctx context.Context, operation FabricOperation) error {
	if s.failed.CompareAndSwap(false, true) {
		return errors.New("injected runtime save failure")
	}
	return s.OperationStore.SaveRuntime(ctx, operation)
}

func (s *failFirstRuntimeSaveStore) ConvergeRuntimeReadback(ctx context.Context, expected, next FabricOperation) error {
	converger, ok := s.OperationStore.(runtimeReadbackConverger)
	if !ok {
		return ErrRuntimeOperationNotCurrent
	}
	return converger.ConvergeRuntimeReadback(ctx, expected, next)
}

func (p *countingGatewayProvider) UpsertGatewaySecret(ctx context.Context, input GatewaySecretInput) (GatewaySecret, error) {
	p.calls.Add(1)
	return p.testProvider.UpsertGatewaySecret(ctx, input)
}

type convergingGatewayProvider struct {
	countingGatewayProvider
	readback      GatewaySecret
	readbackErr   error
	writeErr      error
	readbackCalls atomic.Int32
}

func (p *convergingGatewayProvider) UpsertGatewaySecret(ctx context.Context, input GatewaySecretInput) (GatewaySecret, error) {
	p.calls.Add(1)
	secret, err := p.testProvider.UpsertGatewaySecret(ctx, input)
	if err != nil {
		return secret, err
	}
	return secret, p.writeErr
}

func (p *convergingGatewayProvider) ReadGatewaySecret(_ context.Context, _ GatewaySecretInput) (GatewaySecret, error) {
	p.readbackCalls.Add(1)
	if p.readbackErr != nil {
		return GatewaySecret{}, p.readbackErr
	}
	return p.readback, nil
}

func TestGatewaySecretWriteReplaysOneRedactedOperation(t *testing.T) {
	provider := &countingGatewayProvider{}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	input := GatewaySecretInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", WorkspaceAPIKeyID: 19, Fingerprint: "sha256:12982dcaf26b60cde5b6b68b01556e591badb2768ac9b71525619cb4ebc646f0", GatewayAPIKey: "raw-gateway-key", IdempotencyKey: "gateway-once"}
	secret, err := service.UpsertGatewaySecret(context.Background(), input)
	if err != nil || secret.SecretRef != gatewaySecretName("ws-alpha") || secret.Version == "" || secret.Fingerprint == "" {
		t.Fatalf("gateway secret=%#v err=%v", secret, err)
	}
	replayed, err := service.UpsertGatewaySecret(context.Background(), input)
	if err != nil || replayed != secret || provider.calls.Load() != 1 {
		t.Fatalf("gateway replay=%#v err=%v calls=%d", replayed, err, provider.calls.Load())
	}
	input.GatewayAPIKey = "rotated-gateway-key"
	input.Fingerprint = "sha256:46b91e2f7bc95555effd550e0dd92346b5a4548d9f644a18b11602c5f1c07c68"
	if _, err := service.UpsertGatewaySecret(context.Background(), input); err == nil || err.Error() != "gateway_secret_idempotency_conflict" {
		t.Fatalf("changed Gateway key replay error=%v", err)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("conflicting replay reached provider %d times", provider.calls.Load())
	}
	operations, err := service.ListOperations(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("Gateway operations=%#v err=%v", operations, err)
	}
	operation := operations[0]
	serialized := fmt.Sprint(operation)
	if operation.Action != "upsert_gateway_secret" || operation.Status != "succeeded" || operation.RequestHash == "" ||
		operation.RedactedProviderPayload["keyDigest"] == "" || strings.Contains(serialized, "raw-gateway-key") || strings.Contains(serialized, "rotated-gateway-key") {
		t.Fatalf("Gateway operation is not safely replayable: %#v", operation)
	}
	var recorded GatewaySecret
	if !decodeOperationResource(operation, &recorded) || recorded != secret {
		t.Fatalf("recorded Gateway secret=%#v operation=%#v", recorded, operation)
	}
}

func TestUpsertGatewaySecretReclaimsStaleOperationAfterSaveFailure(t *testing.T) {
	input := GatewaySecretInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", WorkspaceAPIKeyID: 19, Fingerprint: "sha256:12982dcaf26b60cde5b6b68b01556e591badb2768ac9b71525619cb4ebc646f0", GatewayAPIKey: "raw-gateway-key", IdempotencyKey: "gateway-stale"}
	readback, err := (testProvider{}).UpsertGatewaySecret(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	provider := &convergingGatewayProvider{readback: readback}
	store := &failFirstRuntimeSaveStore{OperationStore: NewMemoryOperationStore()}
	startedAt := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)

	first := NewServiceWithOperationStore(provider, store)
	first.now = func() time.Time { return startedAt }
	if _, err := first.UpsertGatewaySecret(context.Background(), input); err == nil || err.Error() != "injected runtime save failure" {
		t.Fatalf("first Gateway Secret save error=%v", err)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("first Gateway Secret provider calls=%d, want 1", provider.calls.Load())
	}

	fresh := NewServiceWithOperationStore(provider, store)
	fresh.now = func() time.Time { return startedAt.Add(time.Minute) }
	if _, err := fresh.UpsertGatewaySecret(context.Background(), input); err == nil || err.Error() != "gateway_secret_operation_started" {
		t.Fatalf("fresh Gateway Secret replay error=%v", err)
	}
	changed := input
	changed.GatewayAPIKey = "rotated-gateway-key"
	changed.Fingerprint = "sha256:46b91e2f7bc95555effd550e0dd92346b5a4548d9f644a18b11602c5f1c07c68"
	stale := NewServiceWithOperationStore(provider, store)
	stale.now = func() time.Time { return startedAt.Add(3 * time.Minute) }
	if _, err := stale.UpsertGatewaySecret(context.Background(), changed); !errors.Is(err, ErrGatewaySecretIdempotencyConflict) {
		t.Fatalf("changed stale Gateway Secret replay error=%v", err)
	}

	secret, err := stale.UpsertGatewaySecret(context.Background(), input)
	if err != nil || secret != readback || provider.calls.Load() != 1 || provider.readbackCalls.Load() != 1 {
		t.Fatalf("stale Gateway Secret=%#v err=%v providerCalls=%d", secret, err, provider.calls.Load())
	}
	replayed, err := NewServiceWithOperationStore(provider, store).UpsertGatewaySecret(context.Background(), input)
	if err != nil || replayed != secret || provider.calls.Load() != 1 {
		t.Fatalf("converged Gateway Secret replay=%#v err=%v providerCalls=%d", replayed, err, provider.calls.Load())
	}
	operations, err := store.List(context.Background())
	serialized := fmt.Sprint(operations)
	if err != nil || len(operations) != 1 || operations[0].Status != "succeeded" || operations[0].RedactedProviderPayload["keyDigest"] == "" ||
		strings.Contains(serialized, input.GatewayAPIKey) || strings.Contains(serialized, changed.GatewayAPIKey) {
		t.Fatalf("converged Gateway Secret operation=%#v err=%v", operations, err)
	}
}

type renewingProvider struct {
	testProvider
	calls atomic.Int32
}

func (p *renewingProvider) RenewStorageVolume(_ context.Context, volume StorageVolume) (StorageVolume, error) {
	p.calls.Add(1)
	volume.Deadline = "2026-09-16T00:00:00Z"
	volume.RenewFlag = "NOTIFY_AND_MANUAL_RENEW"
	volume.ProviderData = map[string]string{"diskChargeType": "PREPAID"}
	volume.ProviderRequestID = "req-renew-cbs"
	return volume, nil
}

type retainedStorageProvider struct {
	testProvider
	syncCalls  int
	renewCalls int
}

func (p *retainedStorageProvider) SyncStorageVolume(_ context.Context, volume StorageVolume) (StorageVolume, error) {
	p.syncCalls++
	volume.Status = "ready"
	return volume, nil
}

func (p *retainedStorageProvider) RenewStorageVolume(_ context.Context, volume StorageVolume) (StorageVolume, error) {
	p.renewCalls++
	volume.Deadline = "2026-09-16T00:00:00Z"
	volume.RenewFlag = "NOTIFY_AND_MANUAL_RENEW"
	volume.ProviderData = map[string]string{"diskChargeType": "PREPAID"}
	return volume, nil
}

func TestRetainedStorageRequiresSuccessfulRenewBeforeSyncReactivation(t *testing.T) {
	provider := &retainedStorageProvider{}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	service.computes["compute-alpha"] = ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "running"}
	service.volumes["storage-alpha"] = StorageVolume{
		ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "retained",
		ProviderResourceID: "disk-storage-alpha", Deadline: "2026-08-16T00:00:00Z",
	}

	retained, err := service.SyncStorageVolume(context.Background(), "storage-alpha")
	if err != nil || retained.Status != "retained" || provider.syncCalls != 0 {
		t.Fatalf("ordinary sync reactivated retained storage: volume=%#v err=%v syncCalls=%d", retained, err, provider.syncCalls)
	}
	if _, err := service.CreateStorageAttachment(context.Background(), StorageAttachmentInput{WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha", IdempotencyKey: "attach-before-renew"}); err == nil || errorCode(err) != "resource_status_invalid" {
		t.Fatalf("retained storage attachment error=%v", err)
	}

	renewed, err := service.RenewStorageVolume(context.Background(), "storage-alpha", "renew-retained")
	if err != nil || renewed.Status != "pending" || provider.renewCalls != 1 {
		t.Fatalf("renewed retained storage=%#v err=%v renewCalls=%d", renewed, err, provider.renewCalls)
	}
	recovered, err := service.SyncStorageVolume(context.Background(), "storage-alpha")
	if err != nil || recovered.Status != "ready" || provider.syncCalls != 1 {
		t.Fatalf("post-renew storage recovery=%#v err=%v syncCalls=%d", recovered, err, provider.syncCalls)
	}
	attached, err := service.CreateStorageAttachment(context.Background(), StorageAttachmentInput{WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha", IdempotencyKey: "attach-after-renew"})
	if err != nil || attached.Status != "attached" {
		t.Fatalf("post-renew attachment=%#v err=%v", attached, err)
	}
}

func TestRenewStorageVolumeReplaysWithoutSecondProviderMutation(t *testing.T) {
	provider := &renewingProvider{}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	service.volumes["storage-alpha"] = StorageVolume{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready", ProviderResourceID: "disk-storage-alpha", Deadline: "2026-08-16T00:00:00Z"}

	first, err := service.RenewStorageVolume(context.Background(), "storage-alpha", "renew-storage-once")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.RenewStorageVolume(context.Background(), "storage-alpha", "renew-storage-once")
	if err != nil || first.Deadline != "2026-09-16T00:00:00Z" || second.Deadline != first.Deadline || provider.calls.Load() != 1 {
		t.Fatalf("first=%#v second=%#v err=%v calls=%d", first, second, err, provider.calls.Load())
	}
}

type blockingStorageRenewProvider struct {
	testProvider
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (p *blockingStorageRenewProvider) RenewStorageVolume(_ context.Context, volume StorageVolume) (StorageVolume, error) {
	if p.calls.Add(1) == 1 {
		close(p.entered)
	}
	<-p.release
	volume.Deadline = "2026-09-16T00:00:00Z"
	volume.RenewFlag = "NOTIFY_AND_MANUAL_RENEW"
	volume.ProviderData = map[string]string{"diskChargeType": "PREPAID"}
	return volume, nil
}

func TestRenewStorageVolumeSerializesConcurrentSameKey(t *testing.T) {
	provider := &blockingStorageRenewProvider{entered: make(chan struct{}), release: make(chan struct{})}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	service.volumes["storage-alpha"] = StorageVolume{ID: "storage-alpha", Status: "ready", ProviderResourceID: "disk-storage-alpha", Deadline: "2026-08-16T00:00:00Z"}
	results := make(chan StorageVolume, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			volume, err := service.RenewStorageVolume(context.Background(), "storage-alpha", "renew-storage-concurrent")
			results <- volume
			errs <- err
		}()
	}
	<-provider.entered
	time.Sleep(20 * time.Millisecond)
	close(provider.release)
	for range 2 {
		volume, err := <-results, <-errs
		if err != nil || volume.Deadline != "2026-09-16T00:00:00Z" {
			t.Fatalf("renewed volume=%#v err=%v", volume, err)
		}
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("concurrent same-key storage renewal called provider %d times", provider.calls.Load())
	}
}

type blockingComputeRenewProvider struct {
	testProvider
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (p *blockingComputeRenewProvider) RenewComputeAllocation(_ context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	if p.calls.Add(1) == 1 {
		close(p.entered)
	}
	<-p.release
	allocation.Deadline = "2026-09-16T00:00:00Z"
	allocation.RenewFlag = "NOTIFY_AND_MANUAL_RENEW"
	allocation.ChargeType = "PREPAID"
	allocation.ProviderRequestID = "req-renew-cvm"
	return allocation, nil
}

func TestRenewComputeAllocationSerializesConcurrentSameKey(t *testing.T) {
	provider := &blockingComputeRenewProvider{entered: make(chan struct{}), release: make(chan struct{})}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	service.computes["compute-alpha"] = ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "running", InstanceID: "ins-basic-1", Deadline: "2026-08-16T00:00:00Z", ProviderData: map[string]string{"instanceType": "SA5.MEDIUM4", "zone": "ap-guangzhou-3"}, CostTags: oplCostTags("acct-alpha", "ws-alpha", "compute-alpha", "owner-alpha")}

	results := make(chan ComputeAllocation, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			allocation, err := service.RenewComputeAllocation(context.Background(), "compute-alpha", "renew-compute-once")
			results <- allocation
			errs <- err
		}()
	}
	<-provider.entered
	close(provider.release)
	for range 2 {
		allocation, err := <-results, <-errs
		if err != nil || allocation.Deadline != "2026-09-16T00:00:00Z" {
			t.Fatalf("renewed allocation=%#v err=%v", allocation, err)
		}
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("concurrent same-key renewal called provider %d times", provider.calls.Load())
	}
}

type renewalResultProvider struct {
	testProvider
	compute func(ComputeAllocation) ComputeAllocation
	storage func(StorageVolume) StorageVolume
}

func (p *renewalResultProvider) RenewComputeAllocation(_ context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	return p.compute(allocation), nil
}

func (p *renewalResultProvider) RenewStorageVolume(_ context.Context, volume StorageVolume) (StorageVolume, error) {
	return p.storage(volume), nil
}

func TestRenewComputeAllocationRejectsMalformedProviderSuccess(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*ComputeAllocation)
	}{
		{name: "resource id", configure: func(result *ComputeAllocation) { result.ID = "compute-other" }},
		{name: "instance id", configure: func(result *ComputeAllocation) { result.InstanceID = "ins-other" }},
		{name: "instance type", configure: func(result *ComputeAllocation) { result.ProviderData["instanceType"] = "SA5.2XLARGE16" }},
		{name: "zone", configure: func(result *ComputeAllocation) { result.ProviderData["zone"] = "ap-guangzhou-4" }},
		{name: "account tag", configure: func(result *ComputeAllocation) { result.CostTags["opl_account_id"] = "acct-other" }},
		{name: "workspace tag", configure: func(result *ComputeAllocation) { result.CostTags["opl_workspace_id"] = "ws-other" }},
		{name: "resource tag", configure: func(result *ComputeAllocation) { result.CostTags["opl_resource_id"] = "compute-other" }},
		{name: "operation tag", configure: func(result *ComputeAllocation) { result.CostTags["opl_operation_id"] = "owner-other" }},
		{name: "postpaid", configure: func(result *ComputeAllocation) { result.ChargeType = "POSTPAID_BY_HOUR" }},
		{name: "auto renew", configure: func(result *ComputeAllocation) { result.RenewFlag = "NOTIFY_AND_AUTO_RENEW" }},
		{name: "deadline without timezone", configure: func(result *ComputeAllocation) { result.Deadline = "2026-09-16 00:00:00" }},
		{name: "deadline did not grow", configure: func(result *ComputeAllocation) { result.Deadline = "2026-08-16T00:00:00Z" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			existing := ComputeAllocation{
				ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "running", InstanceID: "ins-basic-1", CVMInstanceID: "ins-basic-1",
				ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-16T00:00:00Z",
				ProviderData: map[string]string{"instanceType": "SA5.MEDIUM4", "zone": "ap-guangzhou-3"},
				CostTags:     oplCostTags("acct-alpha", "ws-alpha", "compute-alpha", "owner-alpha"),
			}
			provider := &renewalResultProvider{compute: func(result ComputeAllocation) ComputeAllocation {
				result.Deadline = "2026-09-16T00:00:00Z"
				tc.configure(&result)
				return result
			}}
			service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
			service.computes[existing.ID] = existing
			returned, err := service.RenewComputeAllocation(context.Background(), existing.ID, "renew-compute-invalid-"+tc.name)
			if err == nil || errorCode(err) != "compute_renewal_readback_mismatch" {
				t.Fatalf("malformed compute renewal returned=%#v err=%v", returned, err)
			}
			current, ok := service.GetComputeAllocation(context.Background(), existing.ID)
			if !ok || current.InstanceID != existing.InstanceID || current.CVMInstanceID != existing.CVMInstanceID || current.Deadline != existing.Deadline {
				t.Fatalf("malformed renewal overwrote compute identity: %#v", current)
			}
		})
	}
}

func TestRenewComputeAllocationRejectsMissingProviderIdentityBeforeMutation(t *testing.T) {
	for _, missing := range []string{"instanceType", "zone", "tags"} {
		t.Run(missing, func(t *testing.T) {
			provider := &renewalResultProvider{compute: func(allocation ComputeAllocation) ComputeAllocation { return allocation }}
			service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
			existing := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "running", InstanceID: "ins-basic-1", Deadline: "2026-08-16T00:00:00Z", ProviderData: map[string]string{"instanceType": "SA5.MEDIUM4", "zone": "ap-guangzhou-3"}, CostTags: oplCostTags("acct-alpha", "ws-alpha", "compute-alpha", "owner-alpha")}
			switch missing {
			case "instanceType":
				delete(existing.ProviderData, "instanceType")
			case "zone":
				delete(existing.ProviderData, "zone")
			case "tags":
				existing.CostTags = nil
			}
			service.computes[existing.ID] = existing
			if _, err := service.RenewComputeAllocation(context.Background(), existing.ID, "renew-missing-"+missing); err == nil || errorCode(err) != "compute_allocation_renew_identity_required" {
				t.Fatalf("missing %s error=%v", missing, err)
			}
		})
	}
}

func TestRenewStorageVolumeRejectsMalformedProviderSuccess(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*StorageVolume)
	}{
		{name: "resource id", configure: func(result *StorageVolume) { result.ID = "storage-other" }},
		{name: "disk id", configure: func(result *StorageVolume) { result.ProviderResourceID = "disk-other" }},
		{name: "postpaid", configure: func(result *StorageVolume) { result.ProviderData["diskChargeType"] = "POSTPAID_BY_HOUR" }},
		{name: "auto renew", configure: func(result *StorageVolume) { result.RenewFlag = "NOTIFY_AND_AUTO_RENEW" }},
		{name: "deadline without timezone", configure: func(result *StorageVolume) { result.Deadline = "2026-09-16 00:00:00" }},
		{name: "deadline did not grow", configure: func(result *StorageVolume) { result.Deadline = "2026-08-16T00:00:00Z" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			existing := StorageVolume{
				ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready", ProviderResourceID: "disk-storage-alpha",
				RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-16T00:00:00Z", ProviderData: map[string]string{"diskChargeType": "PREPAID"},
			}
			provider := &renewalResultProvider{storage: func(result StorageVolume) StorageVolume {
				result.Deadline = "2026-09-16T00:00:00Z"
				result.ProviderData = map[string]string{"diskChargeType": "PREPAID"}
				tc.configure(&result)
				return result
			}}
			service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
			service.volumes[existing.ID] = existing
			returned, err := service.RenewStorageVolume(context.Background(), existing.ID, "renew-storage-invalid-"+tc.name)
			if err == nil || errorCode(err) != "storage_renewal_readback_mismatch" {
				t.Fatalf("malformed storage renewal returned=%#v err=%v", returned, err)
			}
			current, ok := service.GetStorageVolume(context.Background(), existing.ID)
			if !ok || current.ProviderResourceID != existing.ProviderResourceID || current.Deadline != existing.Deadline {
				t.Fatalf("malformed renewal overwrote storage identity: %#v", current)
			}
		})
	}
}

func TestFabricRejectsIllegalResourceMutationsWithOperationFacts(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	ctx := context.Background()

	if _, err := service.DestroyComputeAllocation(ctx, "missing-compute"); err == nil {
		t.Fatalf("destroy missing compute must fail")
	}
	if _, err := service.CreateStorageAttachment(ctx, StorageAttachmentInput{WorkspaceID: "ws-alpha", ComputeID: "missing-compute", VolumeID: "missing-volume", IdempotencyKey: "reject-missing-attach"}); err == nil {
		t.Fatalf("attach missing compute/storage must fail")
	}

	compute, err := service.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "reject-compute"})
	if err != nil {
		t.Fatalf("create compute: %v", err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")
	otherCompute, err := service.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-beta", WorkspaceID: "ws-beta", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "reject-compute-beta"})
	if err != nil {
		t.Fatalf("create other compute: %v", err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", otherCompute.ID, "succeeded")
	volume, err := service.CreateStorageVolume(ctx, StorageVolumeInput{AccountID: "acct-beta", WorkspaceID: "ws-beta", ComputeID: otherCompute.ID, Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "reject-storage"})
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	if _, err := service.CreateStorageAttachment(ctx, StorageAttachmentInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, IdempotencyKey: "reject-cross-account-attach"}); err == nil {
		t.Fatalf("attach cross-account compute/storage must fail")
	}

	operations, err := service.ListOperations(ctx)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	assertOperationFact(t, operations, "destroy_compute_allocation", "compute_allocation", "missing-compute", "rejected")
	assertOperationFact(t, operations, "create_storage_attachment", "storage_attachment", "att_"+stableSuffix("reject-missing-attach")[:18], "rejected")
	assertOperationFact(t, operations, "create_storage_attachment", "storage_attachment", "att_"+stableSuffix("reject-cross-account-attach")[:18], "rejected")
}

func TestStorageAttachmentRequiresReadyComputeAndVolume(t *testing.T) {
	input := StorageAttachmentInput{WorkspaceID: "ws-alpha"}
	readyCompute := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "running"}
	readyVolume := StorageVolume{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready"}
	for _, status := range []string{"provisioning", "pending", "provider_ready", "quarantined"} {
		compute := readyCompute
		compute.Status = status
		if err := validateAttachmentInput(input, compute, readyVolume); err == nil || errorCode(err) != "resource_status_invalid" {
			t.Fatalf("compute status %q err=%v, want resource_status_invalid", status, err)
		}
	}
	for _, status := range []string{"pending", "provider_ready", "quarantined", "retained", "released"} {
		volume := StorageVolume{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: status}
		if err := validateAttachmentInput(input, readyCompute, volume); err == nil || errorCode(err) != "resource_status_invalid" {
			t.Fatalf("storage status %q err=%v, want resource_status_invalid", status, err)
		}
	}
	if err := validateAttachmentInput(input, readyCompute, readyVolume); err != nil {
		t.Fatalf("ready resources rejected: %v", err)
	}
}

func TestAttachmentAndRuntimeRequireExactWorkspaceOwnership(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*ComputeAllocation, *StorageVolume)
	}{
		{name: "compute", configure: func(compute *ComputeAllocation, _ *StorageVolume) { compute.WorkspaceID = "ws-beta" }},
		{name: "volume", configure: func(_ *ComputeAllocation, volume *StorageVolume) { volume.WorkspaceID = "ws-beta" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compute := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "running"}
			volume := StorageVolume{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready"}
			attachment := StorageAttachment{ID: "attachment-alpha", OperationID: "workspace-launch-alpha:attachment", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha", Status: "attached"}
			tc.configure(&compute, &volume)
			if err := validateAttachmentInput(StorageAttachmentInput{WorkspaceID: "ws-alpha"}, compute, volume); err == nil || errorCode(err) != "resource_workspace_mismatch" {
				t.Fatalf("attachment workspace isolation error=%v", err)
			}
			if err := validateRuntimeInput(WorkspaceRuntimeInput{
				WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha", AttachmentID: "attachment-alpha",
				AttachmentOperationID: "workspace-launch-alpha:attachment", RuntimeOperationID: "workspace-launch-alpha:workspace:runtime",
				IdempotencyKey: "workspace-launch-alpha:workspace:runtime", GatewaySecretRef: gatewaySecretName("ws-alpha"),
			}, compute, volume, attachment, false, testProvider{}.ValidateWorkspaceImageReference); err == nil || errorCode(err) != "resource_workspace_mismatch" {
				t.Fatalf("runtime workspace isolation error=%v", err)
			}
		})
	}
}

func TestWorkspaceRuntimeRequiresExactPersistedAttachmentIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*WorkspaceRuntimeInput)
	}{
		{name: "missing attachment", mutate: func(input *WorkspaceRuntimeInput) { input.AttachmentID = "att-missing" }},
		{name: "attachment operation drift", mutate: func(input *WorkspaceRuntimeInput) { input.AttachmentOperationID = "workspace-launch-other:attachment" }},
		{name: "runtime operation drift", mutate: func(input *WorkspaceRuntimeInput) {
			input.RuntimeOperationID = "workspace-launch-other:workspace:runtime"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &countingRuntimeProvider{}
			service := runtimeTestService(provider, NewMemoryOperationStore())
			input := runtimeTestInput("workspace-launch-alpha:workspace:runtime")
			tc.mutate(&input)

			if _, err := service.CreateWorkspaceRuntime(context.Background(), input); err == nil {
				t.Fatal("drifted attachment identity reached runtime provider")
			}
			if calls := provider.calls.Load(); calls != 0 {
				t.Fatalf("provider calls=%d, want 0", calls)
			}
		})
	}
}

func TestWorkspaceRuntimeRejectsNonImmutableImageBeforeProvider(t *testing.T) {
	provider := &countingRuntimeProvider{}
	service := runtimeTestService(provider, NewMemoryOperationStore())
	input := runtimeTestInput("workspace-launch-alpha:workspace:runtime")
	input.ImageID = "one-person-lab-app:latest"

	if _, err := service.CreateWorkspaceRuntime(context.Background(), input); err == nil || errorCode(err) != "workspace_image_identity_invalid" {
		t.Fatalf("invalid Workspace image error=%v", err)
	}
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("invalid Workspace image reached provider %d times", calls)
	}
}

func TestWorkspaceRuntimeRejectsProviderResultWithDifferentImage(t *testing.T) {
	input := runtimeTestInput("workspace-launch-alpha:workspace:runtime")
	provider := &convergingRuntimeProvider{readback: WorkspaceRuntime{
		ID: "rt_wrong-image", OperationID: input.RuntimeOperationID, WorkspaceID: input.WorkspaceID,
		Status: "running", Ready: true, ServiceName: "opl-compute-alpha",
		ImageID:           "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app@sha256:" + strings.Repeat("b", 64),
		ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey),
	}}

	if _, err := runtimeTestService(provider, NewMemoryOperationStore()).CreateWorkspaceRuntime(context.Background(), input); err == nil || errorCode(err) != "workspace_runtime_image_mismatch" {
		t.Fatalf("different Workspace image result error=%v", err)
	}
}

func TestWorkspaceRuntimePersistsStableAttachmentAndRuntimeOperationIdentity(t *testing.T) {
	provider := &countingRuntimeProvider{}
	service := runtimeTestService(provider, NewMemoryOperationStore())
	input := runtimeTestInput("workspace-launch-alpha:workspace:runtime")

	runtime, err := service.CreateWorkspaceRuntime(context.Background(), input)

	if err != nil {
		t.Fatal(err)
	}
	if provider.input.AttachmentID != input.AttachmentID || provider.input.AttachmentOperationID != input.AttachmentOperationID ||
		provider.input.RuntimeOperationID != input.RuntimeOperationID {
		t.Fatalf("provider input=%#v", provider.input)
	}
	if runtime.OperationID != input.RuntimeOperationID {
		t.Fatalf("runtime operationId=%q want %q", runtime.OperationID, input.RuntimeOperationID)
	}
	attachment := service.attachments[input.AttachmentID]
	if attachment.OperationID != input.AttachmentOperationID {
		t.Fatalf("attachment operationId=%q want %q", attachment.OperationID, input.AttachmentOperationID)
	}
}

func TestWorkspaceRuntimeObservationClassifiesTypedOwnerReadback(t *testing.T) {
	workspaceID := "workspace-alpha"
	runtime := WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: workspaceID, Status: "running", Ready: true}
	for _, testCase := range []struct {
		name      string
		result    WorkspaceRuntime
		err       error
		wantState string
		wantBody  bool
	}{
		{name: "ready", result: runtime, wantState: WorkspaceOwnerObservationReady, wantBody: true},
		{name: "running pending readiness", result: WorkspaceRuntime{ID: runtime.ID, WorkspaceID: workspaceID, Status: "running"}, wantState: WorkspaceOwnerObservationPending, wantBody: true},
		{name: "pending", result: WorkspaceRuntime{ID: runtime.ID, WorkspaceID: workspaceID, Status: "destroying"}, wantState: WorkspaceOwnerObservationPending, wantBody: true},
		{name: "pending status with ready drift", result: WorkspaceRuntime{ID: runtime.ID, WorkspaceID: workspaceID, Status: "destroying", Ready: true}, wantState: WorkspaceOwnerObservationError},
		{name: "absent", err: ErrWorkspaceLaunchResourceAbsent, wantState: WorkspaceOwnerObservationAbsent},
		{name: "conflict sentinel", err: ErrLaunchStageBindingConflict, wantState: WorkspaceOwnerObservationConflict},
		{name: "identity conflict", result: WorkspaceRuntime{ID: runtime.ID, WorkspaceID: "workspace-other", Status: "running", Ready: true}, wantState: WorkspaceOwnerObservationConflict},
		{name: "unknown status", result: WorkspaceRuntime{ID: runtime.ID, WorkspaceID: workspaceID, Status: "destroyed"}, wantState: WorkspaceOwnerObservationError},
		{name: "provider error", err: errors.New("provider unavailable"), wantState: WorkspaceOwnerObservationError},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			observation := workspaceRuntimeOwnerObservation(workspaceID, testCase.result, testCase.err)
			if observation.SchemaVersion != WorkspaceOwnerObservationSchemaVersion || observation.WorkspaceID != workspaceID || observation.State != testCase.wantState {
				t.Fatalf("observation=%#v", observation)
			}
			if (observation.Runtime != nil) != testCase.wantBody {
				t.Fatalf("runtime body=%#v wantBody=%v", observation.Runtime, testCase.wantBody)
			}
			if observation.Runtime != nil && observation.Runtime.Access.Password != "" {
				t.Fatal("runtime observation leaked password")
			}
		})
	}
}

func TestWorkspaceRuntimeGatewaySecretObservationClassifiesTypedOwnerReadback(t *testing.T) {
	workspaceID := "workspace-alpha"
	binding := WorkspaceRuntimeGatewaySecretBinding{
		WorkspaceID: workspaceID, WorkspaceAPIKeyID: 19, SecretRef: gatewaySecretName(workspaceID), Fingerprint: "sha256:alpha", Bound: true,
	}
	for _, testCase := range []struct {
		name      string
		result    WorkspaceRuntimeGatewaySecretBinding
		err       error
		wantState string
		wantBody  bool
	}{
		{name: "ready", result: binding, wantState: WorkspaceOwnerObservationReady, wantBody: true},
		{name: "pending", result: WorkspaceRuntimeGatewaySecretBinding{WorkspaceID: workspaceID, WorkspaceAPIKeyID: 19, SecretRef: gatewaySecretName(workspaceID), Fingerprint: "sha256:alpha"}, wantState: WorkspaceOwnerObservationPending, wantBody: true},
		{name: "absent", err: ErrWorkspaceLaunchResourceAbsent, wantState: WorkspaceOwnerObservationAbsent},
		{name: "conflict sentinel", err: ErrLaunchStageBindingConflict, wantState: WorkspaceOwnerObservationConflict},
		{name: "identity conflict", result: WorkspaceRuntimeGatewaySecretBinding{WorkspaceID: "workspace-other", WorkspaceAPIKeyID: 19, SecretRef: gatewaySecretName(workspaceID), Fingerprint: "sha256:alpha", Bound: true}, wantState: WorkspaceOwnerObservationConflict},
		{name: "provider error", err: errors.New("provider unavailable"), wantState: WorkspaceOwnerObservationError},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			observation := workspaceRuntimeGatewaySecretOwnerObservation(workspaceID, testCase.result, testCase.err)
			if observation.SchemaVersion != WorkspaceOwnerObservationSchemaVersion || observation.WorkspaceID != workspaceID || observation.State != testCase.wantState {
				t.Fatalf("observation=%#v", observation)
			}
			if (observation.Binding != nil) != testCase.wantBody {
				t.Fatalf("binding=%#v wantBody=%v", observation.Binding, testCase.wantBody)
			}
		})
	}
}

func TestWorkspaceRuntimeCredentialUpdateKeepsSingleCreateIdentity(t *testing.T) {
	provider := &countingRuntimeProvider{}
	service := runtimeTestService(provider, NewMemoryOperationStore())
	input := runtimeTestInput("workspace-launch-alpha:workspace:runtime")

	created, err := service.CreateWorkspaceRuntime(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	update := input
	update.IdempotencyKey = "runtime-credential-rotate:workspace-alpha:rotate-alpha:runtime"
	updated, err := service.CreateWorkspaceRuntime(context.Background(), update)
	if err != nil {
		t.Fatalf("credential update: %v", err)
	}
	if created.ID != updated.ID || updated.OperationID != input.RuntimeOperationID {
		t.Fatalf("runtime identity changed: created=%#v updated=%#v", created, updated)
	}
	if _, err := service.CreateWorkspaceRuntime(context.Background(), update); err != nil {
		t.Fatalf("credential update replay: %v", err)
	}
	if calls := provider.calls.Load(); calls != 2 {
		t.Fatalf("provider calls=%d, want initial create plus one update", calls)
	}

	operations, err := service.ListOperations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	createCount, updateCount := 0, 0
	for _, operation := range operations {
		if operation.ResourceKind != "workspace_runtime" || operation.WorkspaceID != input.WorkspaceID || operation.Status != "succeeded" {
			continue
		}
		switch operation.Action {
		case "create_workspace_runtime":
			createCount++
		case "update_workspace_runtime":
			updateCount++
		}
	}
	if createCount != 1 || updateCount != 1 {
		t.Fatalf("runtime operation cardinality create=%d update=%d operations=%#v", createCount, updateCount, operations)
	}
}

func TestServiceReplaysResourceStateFromOperationStore(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	original := NewServiceWithOperationStore(testProvider{}, store)

	compute, err := original.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "replay-compute"})
	if err != nil {
		t.Fatalf("create compute: %v", err)
	}
	waitForOperation(t, original, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")
	volume, err := original.CreateStorageVolume(ctx, StorageVolumeInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: compute.ID, Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "replay-storage"})
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}

	replayed := NewServiceWithOperationStore(testProvider{}, store)
	current, ok := replayed.GetComputeAllocation(ctx, compute.ID)
	if !ok || current.Status == "" || current.AccountID != "acct-alpha" {
		t.Fatalf("replayed compute = %#v ok=%v", current, ok)
	}
	attachment, err := replayed.CreateStorageAttachment(ctx, StorageAttachmentInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, IdempotencyKey: "replay-attach"})
	if err != nil {
		t.Fatalf("attach after replay: %v", err)
	}
	runtime, err := replayed.CreateWorkspaceRuntime(ctx, WorkspaceRuntimeInput{
		WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID,
		AttachmentID: attachment.ID, AttachmentOperationID: attachment.OperationID,
		RuntimeOperationID: "replay-runtime", ImageID: testWorkspaceRuntimeImageID(),
		GatewaySecretRef: gatewaySecretName("ws-alpha"), IdempotencyKey: "replay-runtime",
	})
	if err != nil {
		t.Fatalf("runtime after replay: %v", err)
	}

	replayedAgain := NewServiceWithOperationStore(testProvider{}, store)
	if detached, err := replayedAgain.DetachStorageAttachment(ctx, attachment.ID); err != nil || detached.Status != "detached" {
		t.Fatalf("detach replayed attachment = %#v err=%v", detached, err)
	}
	status, err := replayedAgain.WorkspaceRuntimeStatus(ctx, runtime.WorkspaceID)
	if err != nil || status.Status != "not_found" || status.Access.Password != "" {
		t.Fatalf("runtime status must come from provider/Secret, not replayed facts: %#v err=%v", status, err)
	}
}

type canonicalAttachmentReplayFixture struct {
	operations []FabricOperation
	binding    WorkspaceLaunchStageBinding
	attachment StorageAttachment
}

type countingCanonicalDetachProvider struct {
	testProvider
	detachCalls atomic.Int32
}

func (p *countingCanonicalDetachProvider) DetachStorageAttachment(_ context.Context, attachment StorageAttachment) (StorageAttachment, error) {
	p.detachCalls.Add(1)
	attachment.Status = "detached"
	return attachment, nil
}

func canonicalAttachmentReplayFixtureFor(t *testing.T, providerProfile, suffix string) canonicalAttachmentReplayFixture {
	t.Helper()
	parentBinding := testWorkspaceLaunchBinding("attachment", "ensure_attachment", "launch-canonical-attachment:attachment")
	parentBinding.AccountID, parentBinding.WorkspaceID = "acct-"+suffix, "ws-"+suffix
	parentBinding.LaunchOperationID = "launch-" + suffix
	parentBinding.FabricOperationID = parentBinding.LaunchOperationID + ":attachment"
	parentBinding.IdempotencyKey = parentBinding.LaunchOperationID + ":attachment"
	parentBinding.RequestHash = hashInput(map[string]string{"launch": parentBinding.LaunchOperationID, "stage": parentBinding.Stage})
	attachment := StorageAttachment{
		ID: workspaceLaunchAttachmentID(parentBinding), OperationID: parentBinding.IdempotencyKey, WorkspaceID: parentBinding.WorkspaceID,
		ComputeID: "ca_" + suffix, VolumeID: "vol_" + suffix, Status: "attached", Provider: providerProfile,
		ProviderAttachmentID: "provider/attachment-" + suffix, ProviderRequestID: "provider-attachment-" + suffix,
	}
	now := time.Date(2026, 8, 16, 6, 0, 0, 0, time.UTC)
	parent := newOperation(parentBinding.Action, "workspace_launch_stage", parentBinding.FabricOperationID, parentBinding.AccountID, parentBinding.WorkspaceID, parentBinding.IdempotencyKey, parentBinding.RequestHash, now)
	parent.ID, parent.OperationID, parent.Provider, parent.Status = parentBinding.FabricOperationID, parentBinding.FabricOperationID, providerProfile, "succeeded"
	parent.CreatedAt, parent.FinishedAt = now, now
	if err := bindLaunchStageOperation(&parent, &parentBinding); err != nil {
		t.Fatal(err)
	}
	state, err := encodeLocalDockerWorkspaceLaunchState(localDockerWorkspaceLaunchState{Attachment: &attachment})
	if err != nil {
		t.Fatal(err)
	}
	setWorkspaceLaunchStageRecord(&parent, workspaceLaunchStageRecord{
		SchemaVersion: workspaceLaunchStageRecordSchemaVersion, ProviderProfileRef: providerProfile, ProviderBindingRef: "preflight-" + suffix, SpecDigest: strings.Repeat("a", 64),
		RequestResources: WorkspaceLaunchResources{ComputeAllocationID: attachment.ComputeID, ComputeBindingRef: parentBinding.LaunchOperationID + ":compute", StorageID: attachment.VolumeID, StorageBindingRef: parentBinding.LaunchOperationID + ":storage"},
		Resources:        WorkspaceLaunchResources{ComputeAllocationID: attachment.ComputeID, ComputeBindingRef: parentBinding.LaunchOperationID + ":compute", StorageID: attachment.VolumeID, StorageBindingRef: parentBinding.LaunchOperationID + ":storage", AttachmentID: attachment.ID, AttachmentBindingRef: parentBinding.FabricOperationID},
		ProviderState:    state,
	})
	compute := ComputeAllocation{ID: attachment.ComputeID, AccountID: parentBinding.AccountID, WorkspaceID: parentBinding.WorkspaceID, Status: "running"}
	computeOperation := newOperation("create_compute_allocation", "compute_allocation", compute.ID, compute.AccountID, compute.WorkspaceID, "canonical-attachment-compute-"+suffix, "compute-request-"+suffix, now.Add(time.Second))
	computeOperation.ID, computeOperation.Status = "fop_compute_"+stableSuffix(suffix)[:16], "succeeded"
	computeOperation.CreatedAt, computeOperation.FinishedAt = now.Add(time.Second), now.Add(time.Second)
	fillOperationResource(&computeOperation, compute)
	volume := StorageVolume{ID: attachment.VolumeID, AccountID: parentBinding.AccountID, WorkspaceID: parentBinding.WorkspaceID, Status: "ready"}
	volumeOperation := newOperation("create_storage_volume", "storage_volume", volume.ID, volume.AccountID, volume.WorkspaceID, "canonical-attachment-storage-"+suffix, "storage-request-"+suffix, now.Add(2*time.Second))
	volumeOperation.ID, volumeOperation.Status = "fop_storage_"+stableSuffix(suffix)[:16], "succeeded"
	volumeOperation.CreatedAt, volumeOperation.FinishedAt = now.Add(2*time.Second), now.Add(2*time.Second)
	fillOperationResource(&volumeOperation, volume)
	return canonicalAttachmentReplayFixture{operations: []FabricOperation{parent, computeOperation, volumeOperation}, binding: parentBinding, attachment: attachment}
}

func TestServiceReplaysCanonicalLaunchAttachmentFromParentAfterRestart(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	provider := &countingCanonicalDetachProvider{}
	fixture := canonicalAttachmentReplayFixtureFor(t, provider.Descriptor().Name, "canonical-attachment")
	for _, operation := range fixture.operations {
		if err := store.Append(ctx, operation); err != nil {
			t.Fatal(err)
		}
	}

	restarted := NewServiceWithOperationStore(provider, store)
	facts, err := restarted.ProviderFactsBatch(ctx, ProviderFactsBatchInput{Items: []ProviderFactInput{{
		AccountID: fixture.binding.AccountID, WorkspaceID: fixture.binding.WorkspaceID, ResourceType: "attachment", ResourceID: fixture.attachment.ID,
	}}})
	if err != nil || len(facts.Items) != 1 || !facts.Items[0].Available {
		t.Fatalf("canonical parent attachment facts=%#v err=%v", facts, err)
	}
	if detached, err := restarted.DetachStorageAttachment(ctx, fixture.attachment.ID); err != nil || detached.Status != "detached" {
		t.Fatalf("canonical parent attachment detach=%#v err=%v", detached, err)
	}
	if provider.detachCalls.Load() != 1 {
		t.Fatalf("canonical attachment provider detach calls=%d", provider.detachCalls.Load())
	}
	replayed := NewServiceWithOperationStore(provider, store)
	if detached, err := replayed.DetachStorageAttachment(ctx, fixture.attachment.ID); err != nil || detached.Status != "detached" || provider.detachCalls.Load() != 1 {
		t.Fatalf("replayed canonical detach=%#v err=%v providerCalls=%d", detached, err, provider.detachCalls.Load())
	}
}

func TestCanonicalLaunchAttachmentReplayFailsClosedOnConflictOrUnknownDetach(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *canonicalAttachmentReplayFixture)
	}{
		{name: "provider state identity drift", mutate: func(t *testing.T, fixture *canonicalAttachmentReplayFixture) {
			record, ok := decodeWorkspaceLaunchStageRecord(fixture.operations[0])
			if !ok {
				t.Fatal("decode canonical attachment parent")
			}
			var state localDockerWorkspaceLaunchState
			if json.Unmarshal(record.ProviderState, &state) != nil || state.Attachment == nil {
				t.Fatal("decode canonical attachment state")
			}
			state.Attachment.WorkspaceID += "-drift"
			var err error
			record.ProviderState, err = encodeLocalDockerWorkspaceLaunchState(state)
			if err != nil {
				t.Fatal(err)
			}
			setWorkspaceLaunchStageRecord(&fixture.operations[0], record)
		}},
		{name: "duplicate canonical parent", mutate: func(_ *testing.T, fixture *canonicalAttachmentReplayFixture) {
			fixture.operations = append(fixture.operations, fixture.operations[0])
		}},
		{name: "unknown prior detach", mutate: func(_ *testing.T, fixture *canonicalAttachmentReplayFixture) {
			operation := newOperation("detach_storage_attachment", "storage_attachment", fixture.attachment.ID, "", fixture.attachment.WorkspaceID, "", hashInput(map[string]string{"id": fixture.attachment.ID}), time.Now().UTC())
			operation.Status = "failed"
			fillOperationResource(&operation, fixture.attachment)
			fixture.operations = append(fixture.operations, operation)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := NewMemoryOperationStore()
			provider := &countingCanonicalDetachProvider{}
			fixture := canonicalAttachmentReplayFixtureFor(t, provider.Descriptor().Name, "canonical-conflict-"+stableSuffix(test.name)[:8])
			test.mutate(t, &fixture)
			for _, operation := range fixture.operations {
				if err := store.Append(ctx, operation); err != nil {
					t.Fatal(err)
				}
			}
			restarted := NewServiceWithOperationStore(provider, store)
			facts, err := restarted.ProviderFactsBatch(ctx, ProviderFactsBatchInput{Items: []ProviderFactInput{{
				AccountID: fixture.binding.AccountID, WorkspaceID: fixture.binding.WorkspaceID, ResourceType: "attachment", ResourceID: fixture.attachment.ID,
			}}})
			if err != nil || len(facts.Items) != 1 || facts.Items[0].Available || facts.Items[0].ErrorCode != "provider_fact_identity_mismatch" {
				t.Fatalf("conflicted attachment facts=%#v err=%v", facts, err)
			}
			if _, err := restarted.DetachStorageAttachment(ctx, fixture.attachment.ID); err == nil || err.Error() != "storage_attachment_not_found" || provider.detachCalls.Load() != 0 {
				t.Fatalf("conflicted attachment detach err=%v providerCalls=%d", err, provider.detachCalls.Load())
			}
		})
	}
}

type countingAttachmentProvider struct {
	testProvider
	calls atomic.Int32
}

type convergingAttachmentProvider struct {
	countingAttachmentProvider
	readback      StorageAttachment
	readbackErr   error
	writeErr      error
	readbackCalls atomic.Int32
}

func (p *convergingAttachmentProvider) CreateStorageAttachment(_ context.Context, input StorageAttachmentInput, _ ComputeAllocation, _ StorageVolume) (StorageAttachment, error) {
	p.calls.Add(1)
	result := p.readback
	if result.ID == "" {
		result = StorageAttachment{ID: "att_authoritative-alpha", OperationID: input.IdempotencyKey, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Status: "attached", Provider: "tencent-tke", ProviderAttachmentID: "pv/workspace-alpha-pv:pvc/workspace-alpha-data", ProviderRequestID: providerRequestID("storage-attach", input.IdempotencyKey)}
	}
	return result, p.writeErr
}

func (p *convergingAttachmentProvider) ReadStorageAttachment(_ context.Context, _ StorageAttachment, _ ComputeAllocation, _ StorageVolume) (StorageAttachment, error) {
	p.readbackCalls.Add(1)
	if p.readbackErr != nil {
		return StorageAttachment{}, p.readbackErr
	}
	return p.readback, nil
}

type blockingAttachmentProvider struct {
	testProvider
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (p *countingAttachmentProvider) CreateStorageAttachment(_ context.Context, input StorageAttachmentInput, _ ComputeAllocation, _ StorageVolume) (StorageAttachment, error) {
	p.calls.Add(1)
	return StorageAttachment{ID: "attachment-alpha", WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Status: "attached", ProviderRequestID: providerRequestID("storage-attach", input.IdempotencyKey)}, nil
}

func (p *blockingAttachmentProvider) CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput, _ ComputeAllocation, _ StorageVolume) (StorageAttachment, error) {
	if p.calls.Add(1) == 1 {
		close(p.entered)
	}
	select {
	case <-p.release:
		return StorageAttachment{ID: "attachment-alpha", WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Status: "attached", ProviderRequestID: providerRequestID("storage-attach", input.IdempotencyKey)}, nil
	case <-ctx.Done():
		return StorageAttachment{}, ctx.Err()
	}
}

func TestCreateStorageAttachmentReplaysIdempotentlyAcrossRestart(t *testing.T) {
	provider := &countingAttachmentProvider{}
	store := NewMemoryOperationStore()
	service := attachmentTestService(provider, store)
	input := attachmentTestInput("attachment-once")

	first, firstErr := service.CreateStorageAttachment(context.Background(), input)
	replayed, replayErr := service.CreateStorageAttachment(context.Background(), input)
	restarted := attachmentTestService(provider, store)
	restartedResult, restartErr := restarted.CreateStorageAttachment(context.Background(), input)
	changed := input
	changed.VolumeID = "storage-other"
	_, conflictErr := restarted.CreateStorageAttachment(context.Background(), changed)

	if firstErr != nil || replayErr != nil || restartErr != nil || first.ID != "attachment-alpha" || replayed.ID != first.ID || restartedResult.ID != first.ID || provider.calls.Load() != 1 {
		t.Fatalf("attachment replay first=%#v firstErr=%v replayed=%#v replayErr=%v restarted=%#v restartErr=%v calls=%d", first, firstErr, replayed, replayErr, restartedResult, restartErr, provider.calls.Load())
	}
	if conflictErr == nil || conflictErr.Error() != "storage_attachment_idempotency_conflict" || provider.calls.Load() != 1 {
		t.Fatalf("changed attachment replay error=%v calls=%d", conflictErr, provider.calls.Load())
	}
}

func TestCreateStorageAttachmentClaimsAcrossServiceInstances(t *testing.T) {
	provider := &blockingAttachmentProvider{entered: make(chan struct{}), release: make(chan struct{})}
	store := NewMemoryOperationStore()
	firstService := attachmentTestService(provider, store)
	secondService := attachmentTestService(provider, store)
	input := attachmentTestInput("attachment-shared")

	firstDone := make(chan error, 1)
	go func() {
		_, err := firstService.CreateStorageAttachment(context.Background(), input)
		firstDone <- err
	}()
	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		t.Fatal("first attachment provider call did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, secondErr := secondService.CreateStorageAttachment(ctx, input)
	callsBeforeRelease := provider.calls.Load()
	close(provider.release)
	firstErr := <-firstDone
	if firstErr != nil {
		t.Fatalf("first attachment create: %v", firstErr)
	}
	if secondErr == nil || secondErr.Error() != "storage_attachment_operation_in_progress" || callsBeforeRelease != 1 {
		t.Fatalf("concurrent attachment error=%v providerCalls=%d", secondErr, callsBeforeRelease)
	}
}

func TestCreateStorageAttachmentReclaimsStaleOperationAfterSaveFailure(t *testing.T) {
	input := attachmentTestInput("attachment-stale")
	readback := StorageAttachment{ID: "att_authoritative-alpha", OperationID: input.IdempotencyKey, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Status: "attached", Provider: "tencent-tke", ProviderAttachmentID: "pv/workspace-alpha-pv:pvc/workspace-alpha-data", ProviderRequestID: providerRequestID("storage-attach", input.IdempotencyKey)}
	provider := &convergingAttachmentProvider{readback: readback}
	store := &failFirstRuntimeSaveStore{OperationStore: NewMemoryOperationStore()}
	startedAt := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)

	first := attachmentTestService(provider, store)
	first.now = func() time.Time { return startedAt }
	firstResult, err := first.CreateStorageAttachment(context.Background(), input)
	if err == nil || err.Error() != "injected runtime save failure" || firstResult.ID != readback.ID || provider.calls.Load() != 1 {
		t.Fatalf("first attachment=%#v err=%v providerCalls=%d", firstResult, err, provider.calls.Load())
	}
	fresh := attachmentTestService(provider, store)
	fresh.now = func() time.Time { return startedAt.Add(time.Minute) }
	if _, err := fresh.CreateStorageAttachment(context.Background(), input); !errors.Is(err, ErrStorageAttachmentOperationInProgress) {
		t.Fatalf("fresh attachment replay error=%v", err)
	}

	stale := attachmentTestService(provider, store)
	stale.now = func() time.Time { return startedAt.Add(3 * time.Minute) }
	changed := input
	changed.VolumeID = "storage-other"
	if _, err := stale.CreateStorageAttachment(context.Background(), changed); !errors.Is(err, ErrStorageAttachmentIdempotencyConflict) {
		t.Fatalf("changed stale attachment replay error=%v", err)
	}
	recovered, err := stale.CreateStorageAttachment(context.Background(), input)
	if err != nil || recovered.ID != readback.ID || recovered.OperationID != input.IdempotencyKey || provider.calls.Load() != 1 || provider.readbackCalls.Load() != 1 {
		t.Fatalf("stale attachment=%#v err=%v providerCalls=%d", recovered, err, provider.calls.Load())
	}
	replayed, err := attachmentTestService(provider, store).CreateStorageAttachment(context.Background(), input)
	if err != nil || replayed.ID != recovered.ID || provider.calls.Load() != 1 {
		t.Fatalf("converged attachment replay=%#v err=%v providerCalls=%d", replayed, err, provider.calls.Load())
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].Status != "succeeded" {
		t.Fatalf("converged attachment operations=%#v err=%v", operations, err)
	}
}

func TestCreateStorageAttachmentDoesNotReapplyUnsafePersistedOperation(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   string
	}{
		{status: "failed", want: "storage_attachment_operation_failed"},
		{status: "succeeded", want: "storage_attachment_operation_failed"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			provider := &countingAttachmentProvider{}
			store := NewMemoryOperationStore()
			input := attachmentTestInput("attachment-" + tc.status)
			now := time.Now().UTC()
			operation := newOperation("create_storage_attachment", "storage_attachment", input.IdempotencyKey, "acct-alpha", input.WorkspaceID, input.IdempotencyKey, hashInput(input), now)
			operation.ID = "persisted-attachment-" + tc.status
			operation.Status = tc.status
			operation.CreatedAt = now
			if tc.status != "started" {
				operation.FinishedAt = now
			}
			if tc.status == "failed" {
				operation.ErrorCode = "provider_error"
			}
			if err := store.Append(context.Background(), operation); err != nil {
				t.Fatalf("seed attachment operation: %v", err)
			}

			service := attachmentTestService(provider, store)
			_, err := service.CreateStorageAttachment(context.Background(), input)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("persisted attachment %s error=%v want=%s", tc.status, err, tc.want)
			}
			if provider.calls.Load() != 0 {
				t.Fatalf("persisted attachment %s providerCalls=%d", tc.status, provider.calls.Load())
			}
		})
	}
}

func TestCreateWorkspaceRuntimeReplaysIdempotentlyBeforeProvider(t *testing.T) {
	provider := &countingRuntimeProvider{}
	service := runtimeTestService(provider, NewMemoryOperationStore())
	service.volumes["storage-other"] = StorageVolume{ID: "storage-other", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", Status: "ready", ProviderResourceID: "pvc/storage-other"}
	input := runtimeTestInput("runtime-once")
	first, err := service.CreateWorkspaceRuntime(context.Background(), input)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	replayed, err := service.CreateWorkspaceRuntime(context.Background(), input)
	if err != nil || replayed.ID != first.ID || provider.calls.Load() != 1 {
		t.Fatalf("runtime replay = %#v err=%v providerCalls=%d", replayed, err, provider.calls.Load())
	}
	changed := input
	changed.VolumeID = "storage-other"
	if _, err := service.CreateWorkspaceRuntime(context.Background(), changed); !errors.Is(err, ErrRuntimeIdempotencyConflict) {
		t.Fatalf("changed replay error = %v, want ErrRuntimeIdempotencyConflict", err)
	}
}

func TestRepairWorkspaceRuntimeRejectsMissingPredecessorBeforeProvider(t *testing.T) {
	provider := &countingRuntimeRepairProvider{}
	store := NewMemoryOperationStore()
	service := runtimeTestService(provider, store)
	input := runtimeTestInput("replacement-runtime-operation")
	input.PreviousRuntimeOperationID = "missing-runtime-operation"
	input.IdempotencyKey = "runtime-repair-once"

	if _, err := service.RepairWorkspaceRuntime(context.Background(), input); !errors.Is(err, ErrLaunchStageBindingConflict) {
		t.Fatalf("repair without predecessor error=%v, want ErrLaunchStageBindingConflict", err)
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 0 || provider.repairCalls.Load() != 0 {
		t.Fatalf("repair without predecessor operations=%#v listErr=%v providerCalls=%d", operations, err, provider.repairCalls.Load())
	}
}

func TestRepairWorkspaceRuntimeReplaysAndPublishesSingleCanonicalReplacement(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	predecessor := WorkspaceRuntime{
		ID: "rt_runtime-alpha", OperationID: "original-runtime-operation", WorkspaceID: "workspace-alpha",
		ServiceName: "opl-compute-alpha", ImageID: testWorkspaceRuntimeImageID(), Status: "running", Ready: true,
	}
	operation := newOperation(
		"create_workspace_runtime", "workspace_runtime", predecessor.WorkspaceID, "acct-alpha", predecessor.WorkspaceID,
		predecessor.OperationID, hashInput(predecessor), time.Now().UTC(),
	)
	operation.ID, operation.Status, operation.FinishedAt = "fop_original-runtime-operation", "succeeded", operation.StartedAt
	fillOperationResource(&operation, predecessor)
	if err := store.Append(ctx, operation); err != nil {
		t.Fatalf("seed predecessor: %v", err)
	}

	replacementImage := workspaceImageRepository + "@sha256:" + strings.Repeat("b", 64)
	provider := &countingRuntimeRepairProvider{result: predecessor}
	service := runtimeTestService(provider, store)
	input := runtimeTestInput("replacement-runtime-operation")
	input.PreviousRuntimeOperationID = predecessor.OperationID
	input.IdempotencyKey = "runtime-repair-once"
	input.ImageID = replacementImage

	repaired, err := service.RepairWorkspaceRuntime(ctx, input)
	if err != nil {
		t.Fatalf("repair runtime: %v", err)
	}
	if repaired.ID != predecessor.ID || repaired.ServiceName != predecessor.ServiceName ||
		repaired.OperationID != input.RuntimeOperationID || repaired.ImageID != replacementImage {
		t.Fatalf("replacement runtime identity=%#v predecessor=%#v", repaired, predecessor)
	}
	replayed, err := service.RepairWorkspaceRuntime(ctx, input)
	if err != nil || !reflect.DeepEqual(replayed, repaired) || provider.repairCalls.Load() != 1 {
		t.Fatalf("repair replay=%#v err=%v providerCalls=%d", replayed, err, provider.repairCalls.Load())
	}

	otherAuthorization := input
	otherAuthorization.IdempotencyKey = "runtime-repair-other-authorization"
	if _, err := service.RepairWorkspaceRuntime(ctx, otherAuthorization); !errors.Is(err, ErrRuntimeIdempotencyConflict) {
		t.Fatalf("different repair authorization error=%v, want ErrRuntimeIdempotencyConflict", err)
	}
	if provider.repairCalls.Load() != 1 {
		t.Fatalf("different repair authorization providerCalls=%d, want 1", provider.repairCalls.Load())
	}

	candidates, err := store.WorkspaceRuntimeIdentityCandidates(ctx, predecessor.WorkspaceID)
	if err != nil || len(candidates) != 1 || candidates[0].Action != "repair_workspace_runtime" {
		t.Fatalf("replacement runtime candidates=%#v err=%v", candidates, err)
	}
	var candidate WorkspaceRuntime
	if !decodeOperationResource(candidates[0], &candidate) || !reflect.DeepEqual(candidate, repaired) {
		t.Fatalf("canonical replacement runtime=%#v repaired=%#v", candidate, repaired)
	}
}

func TestDestroyWorkspaceRuntimeReplaysIdempotentlyBeforeProvider(t *testing.T) {
	provider := &countingRuntimeProvider{}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())

	first, err := service.DestroyWorkspaceRuntime(context.Background(), "workspace-alpha", "runtime-destroy-once")
	if err != nil {
		t.Fatalf("destroy runtime: %v", err)
	}
	replayed, err := service.DestroyWorkspaceRuntime(context.Background(), "workspace-alpha", "runtime-destroy-once")
	if err != nil || replayed.Status != "destroyed" || first.WorkspaceID != "workspace-alpha" || provider.destroyCalls.Load() != 1 {
		t.Fatalf("destroy replay = %#v err=%v providerCalls=%d", replayed, err, provider.destroyCalls.Load())
	}
}

func TestDestroyWorkspaceRuntimeRetriesFailedProviderOperation(t *testing.T) {
	provider := &failOnceDestroyProvider{}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())

	if _, err := service.DestroyWorkspaceRuntime(context.Background(), "workspace-alpha", "runtime-destroy-once"); err == nil {
		t.Fatal("first destroy succeeded, want transient failure")
	}
	runtime, err := service.DestroyWorkspaceRuntime(context.Background(), "workspace-alpha", "runtime-destroy-once")
	if err != nil || runtime.Status != "destroyed" || provider.destroyCalls.Load() != 2 {
		t.Fatalf("retry destroy = %#v err=%v providerCalls=%d", runtime, err, provider.destroyCalls.Load())
	}
	if _, err := service.DestroyWorkspaceRuntime(context.Background(), "workspace-alpha", "runtime-destroy-once"); err != nil || provider.destroyCalls.Load() != 2 {
		t.Fatalf("successful replay err=%v providerCalls=%d", err, provider.destroyCalls.Load())
	}
}

func TestDestroyWorkspaceRuntimeRejectsFailedRetryForDifferentWorkspace(t *testing.T) {
	store := NewMemoryOperationStore()
	originalProvider := &failOnceDestroyProvider{}
	originalService := NewServiceWithOperationStore(originalProvider, store)
	if _, err := originalService.DestroyWorkspaceRuntime(context.Background(), "workspace-alpha", "runtime-destroy-once"); err == nil {
		t.Fatal("first destroy succeeded, want transient failure")
	}
	before, err := store.List(context.Background())
	if err != nil || len(before) != 1 || before[0].Status != "failed" {
		t.Fatalf("failed operation = %#v err=%v", before, err)
	}

	otherProvider := &countingRuntimeProvider{}
	otherService := NewServiceWithOperationStore(otherProvider, store)
	if _, err := otherService.DestroyWorkspaceRuntime(context.Background(), "workspace-beta", "runtime-destroy-once"); !errors.Is(err, ErrRuntimeIdempotencyConflict) {
		t.Fatalf("cross-workspace retry error = %v, want ErrRuntimeIdempotencyConflict", err)
	}
	after, err := store.List(context.Background())
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("cross-workspace retry changed operation: before=%#v after=%#v err=%v", before, after, err)
	}
	if otherProvider.destroyCalls.Load() != 0 {
		t.Fatalf("cross-workspace provider calls = %d, want 0", otherProvider.destroyCalls.Load())
	}

	runtime, err := originalService.DestroyWorkspaceRuntime(context.Background(), "workspace-alpha", "runtime-destroy-once")
	if err != nil || runtime.Status != "destroyed" || originalProvider.destroyCalls.Load() != 2 {
		t.Fatalf("original retry = %#v err=%v providerCalls=%d", runtime, err, originalProvider.destroyCalls.Load())
	}
}

func TestCreateWorkspaceRuntimeClaimsAcrossServiceInstances(t *testing.T) {
	provider := &blockingRuntimeProvider{entered: make(chan struct{}), release: make(chan struct{})}
	store := NewMemoryOperationStore()
	firstService := runtimeTestService(provider, store)
	secondService := runtimeTestService(provider, store)
	input := runtimeTestInput("runtime-shared")

	firstDone := make(chan error, 1)
	go func() {
		_, err := firstService.CreateWorkspaceRuntime(context.Background(), input)
		firstDone <- err
	}()
	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		t.Fatal("first provider call did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := secondService.CreateWorkspaceRuntime(ctx, input); err == nil || err.Error() != "runtime_operation_in_progress" {
		t.Fatalf("concurrent replay error = %v, want runtime_operation_in_progress", err)
	}
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	close(provider.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first runtime create: %v", err)
	}

	restarted := NewServiceWithOperationStore(provider, store)
	replayed, err := restarted.CreateWorkspaceRuntime(context.Background(), input)
	if err != nil || replayed.ID != "runtime-alpha" || provider.calls.Load() != 1 {
		t.Fatalf("restart replay = %#v err=%v providerCalls=%d", replayed, err, provider.calls.Load())
	}
	changed := input
	changed.ImageID = "changed-image"
	if _, err := restarted.CreateWorkspaceRuntime(context.Background(), changed); !errors.Is(err, ErrRuntimeIdempotencyConflict) {
		t.Fatalf("changed restart replay error = %v, want ErrRuntimeIdempotencyConflict", err)
	}
}

func TestCreateWorkspaceRuntimeReclaimsStaleOperationAfterSaveFailure(t *testing.T) {
	input := runtimeTestInput("runtime-stale")
	readback := WorkspaceRuntime{ID: "rt_authoritative-alpha", OperationID: input.RuntimeOperationID, WorkspaceID: input.WorkspaceID, Status: "running", Ready: true, ServiceName: "opl-compute-alpha", ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey)}
	provider := &convergingRuntimeProvider{readback: readback}
	store := &failFirstRuntimeSaveStore{OperationStore: NewMemoryOperationStore()}
	startedAt := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)

	first := runtimeTestService(provider, store)
	first.now = func() time.Time { return startedAt }
	firstResult, err := first.CreateWorkspaceRuntime(context.Background(), input)
	if err == nil || err.Error() != "injected runtime save failure" || firstResult.ID != readback.ID || provider.calls.Load() != 1 {
		t.Fatalf("first runtime=%#v err=%v providerCalls=%d", firstResult, err, provider.calls.Load())
	}
	fresh := runtimeTestService(provider, store)
	fresh.now = func() time.Time { return startedAt.Add(time.Minute) }
	if _, err := fresh.CreateWorkspaceRuntime(context.Background(), input); !errors.Is(err, ErrRuntimeOperationInProgress) {
		t.Fatalf("fresh runtime replay error=%v", err)
	}

	stale := runtimeTestService(provider, store)
	stale.now = func() time.Time { return startedAt.Add(3 * time.Minute) }
	changed := input
	changed.ImageID = "changed-image"
	if _, err := stale.CreateWorkspaceRuntime(context.Background(), changed); !errors.Is(err, ErrRuntimeIdempotencyConflict) {
		t.Fatalf("changed stale runtime replay error=%v", err)
	}
	recovered, err := stale.CreateWorkspaceRuntime(context.Background(), input)
	if err != nil || recovered.ID != readback.ID || recovered.OperationID != input.RuntimeOperationID || provider.calls.Load() != 1 || provider.readbackCalls.Load() != 1 {
		t.Fatalf("stale runtime=%#v err=%v providerCalls=%d", recovered, err, provider.calls.Load())
	}
	replayed, err := NewServiceWithOperationStore(provider, store).CreateWorkspaceRuntime(context.Background(), input)
	if err != nil || replayed.ID != recovered.ID || provider.calls.Load() != 1 {
		t.Fatalf("converged runtime replay=%#v err=%v providerCalls=%d", replayed, err, provider.calls.Load())
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].Status != "succeeded" {
		t.Fatalf("converged runtime operations=%#v err=%v", operations, err)
	}
}

func TestRuntimeStageFailedOperationsConvergeByReadbackWithoutSecondWrite(t *testing.T) {
	t.Run("attachment", func(t *testing.T) {
		input := attachmentTestInput("attachment-failed-readback")
		readback := StorageAttachment{ID: "att_failed-readback", OperationID: input.IdempotencyKey, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Status: "attached", Provider: "tencent-tke", ProviderAttachmentID: "pv/workspace-alpha-pv:pvc/workspace-alpha-data", ProviderRequestID: providerRequestID("storage-attach", input.IdempotencyKey)}
		provider := &convergingAttachmentProvider{readback: readback, writeErr: errors.New("provider response lost")}
		store := NewMemoryOperationStore()
		if result, err := attachmentTestService(provider, store).CreateStorageAttachment(context.Background(), input); err == nil || result.ID != readback.ID {
			t.Fatalf("first attachment=%#v err=%v", result, err)
		}
		result, err := attachmentTestService(provider, store).CreateStorageAttachment(context.Background(), input)
		if err != nil || result.ID != readback.ID || result.OperationID != readback.OperationID || result.ProviderAttachmentID != readback.ProviderAttachmentID || provider.calls.Load() != 1 || provider.readbackCalls.Load() != 1 {
			t.Fatalf("attachment readback=%#v err=%v writes=%d reads=%d", result, err, provider.calls.Load(), provider.readbackCalls.Load())
		}
	})

	t.Run("gateway secret", func(t *testing.T) {
		input := GatewaySecretInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", WorkspaceAPIKeyID: 19, Fingerprint: "sha256:12982dcaf26b60cde5b6b68b01556e591badb2768ac9b71525619cb4ebc646f0", GatewayAPIKey: "raw-gateway-key", IdempotencyKey: "gateway-failed-readback"}
		readback, err := (testProvider{}).UpsertGatewaySecret(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		provider := &convergingGatewayProvider{readback: readback, writeErr: errors.New("provider response lost")}
		store := NewMemoryOperationStore()
		if result, err := NewServiceWithOperationStore(provider, store).UpsertGatewaySecret(context.Background(), input); err == nil || result != readback {
			t.Fatalf("first Gateway Secret=%#v err=%v", result, err)
		}
		result, err := NewServiceWithOperationStore(provider, store).UpsertGatewaySecret(context.Background(), input)
		if err != nil || result != readback || provider.calls.Load() != 1 || provider.readbackCalls.Load() != 1 {
			t.Fatalf("Gateway Secret readback=%#v err=%v writes=%d reads=%d", result, err, provider.calls.Load(), provider.readbackCalls.Load())
		}
	})

	t.Run("runtime", func(t *testing.T) {
		input := runtimeTestInput("runtime-failed-readback")
		readback := WorkspaceRuntime{ID: "rt_failed-readback", OperationID: input.RuntimeOperationID, WorkspaceID: input.WorkspaceID, Status: "running", Ready: true, ServiceName: "opl-compute-alpha", ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey)}
		provider := &convergingRuntimeProvider{readback: readback, writeErr: errors.New("provider response lost")}
		store := NewMemoryOperationStore()
		if result, err := runtimeTestService(provider, store).CreateWorkspaceRuntime(context.Background(), input); err == nil || result.ID != readback.ID {
			t.Fatalf("first runtime=%#v err=%v", result, err)
		}
		result, err := runtimeTestService(provider, store).CreateWorkspaceRuntime(context.Background(), input)
		if err != nil || result.ID != readback.ID || result.OperationID != input.RuntimeOperationID || provider.calls.Load() != 1 || provider.readbackCalls.Load() != 1 {
			t.Fatalf("runtime readback=%#v err=%v writes=%d reads=%d", result, err, provider.calls.Load(), provider.readbackCalls.Load())
		}
	})
}

func TestRuntimeStageReadbackFailureNeverRepeatsProviderWrite(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result WorkspaceRuntime
		err    error
	}{
		{name: "absent"},
		{name: "identity drift", result: WorkspaceRuntime{ID: "rt_other", OperationID: "runtime-other", WorkspaceID: "workspace-alpha", Status: "running", ServiceName: "opl-compute-alpha"}},
		{name: "multiple candidates", err: errors.New("workspace_runtime_status_ownership_conflict")},
		{name: "read error", err: errors.New("workspace_runtime_status_iam_rbac")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := runtimeTestInput("runtime-fail-closed-" + strings.ReplaceAll(tc.name, " ", "-"))
			initial := WorkspaceRuntime{ID: "rt_initial", OperationID: input.RuntimeOperationID, WorkspaceID: input.WorkspaceID, Status: "running", Ready: true, ServiceName: "opl-compute-alpha", ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey)}
			provider := &convergingRuntimeProvider{readback: initial, writeErr: errors.New("provider response lost")}
			store := NewMemoryOperationStore()
			if _, err := runtimeTestService(provider, store).CreateWorkspaceRuntime(context.Background(), input); err == nil {
				t.Fatal("first runtime unexpectedly succeeded")
			}
			provider.readback, provider.readbackErr = tc.result, tc.err
			if _, err := runtimeTestService(provider, store).CreateWorkspaceRuntime(context.Background(), input); !errors.Is(err, ErrRuntimeOperationFailed) {
				t.Fatalf("readback error=%v", err)
			}
			operations, err := store.List(context.Background())
			if err != nil || len(operations) != 1 || operations[0].Status != "failed" || provider.calls.Load() != 1 || provider.readbackCalls.Load() != 1 {
				t.Fatalf("operations=%#v err=%v writes=%d reads=%d", operations, err, provider.calls.Load(), provider.readbackCalls.Load())
			}
		})
	}
}

func TestRuntimeStageReadbackRejectsDifferentWorkspaceImage(t *testing.T) {
	input := runtimeTestInput("runtime-image-drift")
	readback := WorkspaceRuntime{
		ID: "rt_image-drift", OperationID: input.RuntimeOperationID, WorkspaceID: input.WorkspaceID,
		Status: "running", Ready: true, ServiceName: "opl-compute-alpha",
		ImageID:           workspaceImageRepository + "@sha256:" + strings.Repeat("b", 64),
		ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey),
	}
	provider := &convergingRuntimeProvider{readback: readback, writeErr: errors.New("provider response lost")}
	store := NewMemoryOperationStore()
	if _, err := runtimeTestService(provider, store).CreateWorkspaceRuntime(context.Background(), input); err == nil {
		t.Fatal("first runtime unexpectedly succeeded")
	}
	if _, err := runtimeTestService(provider, store).CreateWorkspaceRuntime(context.Background(), input); !errors.Is(err, ErrRuntimeOperationFailed) {
		t.Fatalf("different Workspace image readback error=%v", err)
	}
	if provider.calls.Load() != 1 || provider.readbackCalls.Load() != 1 {
		t.Fatalf("writes=%d reads=%d, want 1/1", provider.calls.Load(), provider.readbackCalls.Load())
	}
}

func TestCreateWorkspaceRuntimeDoesNotReapplyFailedOperation(t *testing.T) {
	provider := &countingRuntimeProvider{}
	store := NewMemoryOperationStore()
	input := runtimeTestInput("runtime-failed")
	now := time.Now().UTC()
	operation := newOperation("create_workspace_runtime", "workspace_runtime", input.WorkspaceID, "acct-alpha", input.WorkspaceID, input.IdempotencyKey, hashInput(input), now)
	operation.ID = "persisted-failed"
	operation.Status = "failed"
	operation.CreatedAt = now
	operation.FinishedAt = now
	operation.ErrorCode = "provider_error"
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatalf("seed operation: %v", err)
	}

	service := runtimeTestService(provider, store)
	if _, err := service.CreateWorkspaceRuntime(context.Background(), input); !errors.Is(err, ErrRuntimeOperationFailed) {
		t.Fatalf("persisted failed runtime error=%v", err)
	}
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("provider calls=%d, want 0", calls)
	}
}

func runtimeTestService(provider Provider, store OperationStore) *Service {
	service := NewServiceWithOperationStore(provider, store)
	service.computes["compute-alpha"] = ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", Status: "running", ServiceName: "opl-compute-alpha"}
	service.volumes["storage-alpha"] = StorageVolume{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", Status: "ready", ProviderResourceID: "pvc/storage-alpha"}
	service.attachments["attachment-alpha"] = StorageAttachment{
		ID: "attachment-alpha", OperationID: "workspace-launch-alpha:attachment", WorkspaceID: "workspace-alpha",
		ComputeID: "compute-alpha", VolumeID: "storage-alpha", Status: "attached",
	}
	return service
}

func runtimeTestInput(key string) WorkspaceRuntimeInput {
	return WorkspaceRuntimeInput{
		WorkspaceID: "workspace-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha",
		AttachmentID: "attachment-alpha", AttachmentOperationID: "workspace-launch-alpha:attachment", RuntimeOperationID: key,
		ImageID: testWorkspaceRuntimeImageID(), GatewaySecretRef: gatewaySecretName("workspace-alpha"), IdempotencyKey: key,
	}
}

func testWorkspaceRuntimeImageID() string {
	return workspaceImageRepository + "@sha256:" + strings.Repeat("a", 64)
}

func attachmentTestService(provider Provider, store OperationStore) *Service {
	service := NewServiceWithOperationStore(provider, store)
	service.computes["compute-alpha"] = ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", Status: "running"}
	service.volumes["storage-alpha"] = StorageVolume{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", Status: "ready"}
	service.volumes["storage-other"] = StorageVolume{ID: "storage-other", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", Status: "ready"}
	return service
}

func attachmentTestInput(key string) StorageAttachmentInput {
	return StorageAttachmentInput{WorkspaceID: "workspace-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha", IdempotencyKey: key}
}

func waitForOperation(t *testing.T, service *Service, action string, resourceKind string, resourceID string, status string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		operations, err := service.ListOperations(context.Background())
		if err != nil {
			t.Fatalf("list operations: %v", err)
		}
		for _, operation := range operations {
			if operation.Action == action && operation.ResourceKind == resourceKind && operation.ResourceID == resourceID && operation.Status == status {
				if operation.OperationID == "" || operation.ProviderRequestID == "" || operation.RequestHash == "" || operation.StartedAt.IsZero() || operation.FinishedAt.IsZero() {
					t.Fatalf("operation missing audit fields: %#v", operation)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("missing operation %s/%s/%s/%s", action, resourceKind, resourceID, status)
}

func assertOperationFact(t *testing.T, operations []FabricOperation, action string, resourceKind string, resourceID string, status string) {
	t.Helper()
	for _, operation := range operations {
		if operation.Action != action || operation.ResourceKind != resourceKind || operation.ResourceID != resourceID || operation.Status != status {
			continue
		}
		if operation.OperationID == "" || operation.ProviderRequestID == "" || operation.RequestHash == "" || operation.StartedAt.IsZero() || operation.FinishedAt.IsZero() {
			t.Fatalf("operation missing audit fields: %#v", operation)
		}
		return
	}
	t.Fatalf("missing operation %s/%s/%s/%s in %#v", action, resourceKind, resourceID, status, operations)
}

type blockingProvider struct {
	testProvider
	done chan struct{}
}

func (p *blockingProvider) CreateComputeAllocation(ctx context.Context, input ComputeAllocationExecution) (ComputeAllocation, error) {
	<-p.done
	return testProvider{}.CreateComputeAllocation(ctx, input)
}

type blockingComputeDestroyProvider struct {
	testProvider
	destroyCalls atomic.Int32
	entered      chan struct{}
	release      chan struct{}
}

func (p *blockingComputeDestroyProvider) DestroyComputeAllocation(_ context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	if p.destroyCalls.Add(1) == 1 {
		close(p.entered)
	}
	<-p.release
	allocation.Status = "destroyed"
	return allocation, nil
}

func TestComputeAsyncDestroyReturnsBeforeProviderCleanupAndReplays(t *testing.T) {
	provider := &blockingComputeDestroyProvider{entered: make(chan struct{}), release: make(chan struct{})}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	compute, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "async-destroy-create"})
	if err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")
	t.Cleanup(func() {
		select {
		case <-provider.release:
		default:
			close(provider.release)
		}
	})

	result := make(chan ComputeAllocation, 1)
	errs := make(chan error, 1)
	go func() {
		allocation, destroyErr := service.DestroyComputeAllocation(context.Background(), compute.ID)
		result <- allocation
		errs <- destroyErr
	}()

	select {
	case allocation := <-result:
		if err := <-errs; err != nil || allocation.Status != "destroying" {
			t.Fatalf("destroy response = %#v err=%v", allocation, err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("destroy blocked on provider cleanup")
	}
	<-provider.entered
	replayed, err := service.DestroyComputeAllocation(context.Background(), compute.ID)
	if err != nil || replayed.Status != "destroying" || provider.destroyCalls.Load() != 1 {
		t.Fatalf("destroy replay = %#v err=%v calls=%d", replayed, err, provider.destroyCalls.Load())
	}
	close(provider.release)
	waitForOperation(t, service, "destroy_compute_allocation", "compute_allocation", compute.ID, "succeeded")
	finished, err := service.DestroyComputeAllocation(context.Background(), compute.ID)
	if err != nil || finished.Status != "destroyed" || provider.destroyCalls.Load() != 1 {
		t.Fatalf("finished destroy replay = %#v err=%v calls=%d", finished, err, provider.destroyCalls.Load())
	}
}

type testProvider struct{}

func (testProvider) Descriptor() ProviderDescriptor {
	return NewTencentProvider().Descriptor()
}

func (testProvider) ResolveWorkspacePlan(_ context.Context, input WorkspaceLaunchPlanInput) (json.RawMessage, error) {
	plan, ok := providerPlan(testProvider{}, input.PackageID)
	if !ok {
		return nil, ErrProviderPlanUnavailable
	}
	return json.Marshal(map[string]any{
		"compute": plan,
		"storage": map[string]any{"sizeGb": input.SizeGB},
	})
}

func (testProvider) ValidateComputeAllocation(allocation ComputeAllocation, prepared ComputeAllocationPreparation) error {
	return NewTencentProvider().ValidateComputeAllocation(allocation, prepared)
}

func (testProvider) ValidateWorkspaceImageReference(value string) bool {
	return validWorkspaceRuntimeImageIdentity(value)
}

func (testProvider) ReadComputeProviderFacts(_ context.Context, allocation ComputeAllocation) (ProviderResourceFacts, error) {
	return ProviderResourceFacts{
		PackageOrSpec: allocation.PackageID,
		ProviderID:    allocation.ProviderResourceID,
		Zone:          allocation.Zone,
		Status:        allocation.Status,
		ExpiresAt:     allocation.Deadline,
	}, nil
}

func (testProvider) ReadStorageProviderFacts(_ context.Context, volume StorageVolume) (ProviderResourceFacts, error) {
	return ProviderResourceFacts{PackageOrSpec: volume.StorageClass, ProviderID: volume.ProviderResourceID, Zone: volume.Zone, Status: volume.Status, ExpiresAt: volume.Deadline}, nil
}

func (testProvider) ReadStorageAttachmentProviderFacts(_ context.Context, attachment StorageAttachment, _ ComputeAllocation, _ StorageVolume) (ProviderResourceFacts, error) {
	return ProviderResourceFacts{PackageOrSpec: "/data", ProviderID: attachment.ProviderAttachmentID, Status: attachment.Status}, nil
}

func (testProvider) WorkspaceRuntimeProviderFacts(runtime WorkspaceRuntime) ProviderResourceFacts {
	return ProviderResourceFacts{ProviderID: runtime.ServiceName, Status: runtime.Status}
}

type liveRuntimeWithoutIDProvider struct {
	testProvider
	runtimeIDs map[string]string
	status     string
}

type failingListOperationStore struct{ OperationStore }

func (failingListOperationStore) List(context.Context) ([]FabricOperation, error) {
	return nil, errors.New("operation store unavailable")
}

type rejectFullOperationListStore struct{ OperationStore }

func (rejectFullOperationListStore) List(context.Context) ([]FabricOperation, error) {
	return nil, errors.New("full operation list must not be used")
}

type failingRuntimeIdentityCandidatesStore struct{ OperationStore }

func (failingRuntimeIdentityCandidatesStore) WorkspaceRuntimeIdentityCandidates(context.Context, string) ([]FabricOperation, error) {
	return nil, errors.New("runtime identity candidates unavailable")
}

func (p liveRuntimeWithoutIDProvider) CreateWorkspaceRuntime(_ context.Context, input WorkspaceRuntimeInput, _ ComputeAllocation, _ StorageVolume) (WorkspaceRuntime, error) {
	return WorkspaceRuntime{ID: p.runtimeIDs[input.IdempotencyKey], WorkspaceID: input.WorkspaceID, URL: "https://stale.invalid", Status: "unready", ServiceName: "runtime-created", ImageID: input.ImageID, Checks: []Check{{Name: "deployment_ready", OK: false}}}, nil
}

func (p liveRuntimeWithoutIDProvider) WorkspaceRuntimeStatus(_ context.Context, workspaceID string) (WorkspaceRuntime, error) {
	status := p.status
	if status == "" {
		status = "running"
	}
	return WorkspaceRuntime{WorkspaceID: workspaceID, URL: "https://workspace.medopl.cn/w/workspace-alpha/", Status: status, ServiceName: "runtime-live", Ready: status == "running", Access: RuntimeAccess{Username: "opl", Password: "runtime-password-alpha", CredentialStatus: "configured", CredentialVersion: "v1"}, Checks: []Check{{Name: "deployment_ready", OK: status == "running"}}}, nil
}

type countingRuntimeProvider struct {
	testProvider
	calls        atomic.Int32
	destroyCalls atomic.Int32
	input        WorkspaceRuntimeInput
}

type countingRuntimeRepairProvider struct {
	countingRuntimeProvider
	repairCalls atomic.Int32
	result      WorkspaceRuntime
}

func (p *countingRuntimeRepairProvider) RepairWorkspaceRuntime(_ context.Context, input WorkspaceRuntimeInput, _ ComputeAllocation, _ StorageVolume) (WorkspaceRuntime, error) {
	p.repairCalls.Add(1)
	result := p.result
	result.OperationID = input.RuntimeOperationID
	result.ImageID = input.ImageID
	return result, nil
}

type convergingRuntimeProvider struct {
	countingRuntimeProvider
	readback      WorkspaceRuntime
	readbackErr   error
	writeErr      error
	imageID       string
	readbackCalls atomic.Int32
}

func (p *convergingRuntimeProvider) CreateWorkspaceRuntime(_ context.Context, input WorkspaceRuntimeInput, _ ComputeAllocation, _ StorageVolume) (WorkspaceRuntime, error) {
	p.calls.Add(1)
	p.imageID = input.ImageID
	result := p.readback
	if result.ID == "" {
		result = WorkspaceRuntime{ID: "rt_authoritative-alpha", OperationID: input.RuntimeOperationID, WorkspaceID: input.WorkspaceID, Status: "running", Ready: true, ServiceName: "opl-compute-alpha", ImageID: input.ImageID, ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey)}
	} else if result.ImageID == "" {
		result.ImageID = input.ImageID
	}
	return result, p.writeErr
}

func (p *convergingRuntimeProvider) WorkspaceRuntimeStatus(_ context.Context, _ string) (WorkspaceRuntime, error) {
	p.readbackCalls.Add(1)
	if p.readbackErr != nil {
		return WorkspaceRuntime{}, p.readbackErr
	}
	result := p.readback
	if result.ImageID == "" {
		result.ImageID = p.imageID
	}
	return result, nil
}

type failOnceDestroyProvider struct {
	testProvider
	destroyCalls atomic.Int32
}

func (p *failOnceDestroyProvider) DestroyWorkspaceRuntime(_ context.Context, workspaceID string) (WorkspaceRuntime, error) {
	if p.destroyCalls.Add(1) == 1 {
		return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "destroying"}, errors.New("cluster unavailable")
	}
	return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "destroyed"}, nil
}

func (p *countingRuntimeProvider) CreateWorkspaceRuntime(_ context.Context, input WorkspaceRuntimeInput, _ ComputeAllocation, _ StorageVolume) (WorkspaceRuntime, error) {
	p.calls.Add(1)
	p.input = input
	return WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: input.WorkspaceID, Status: "running", Ready: true, ServiceName: "opl-compute-alpha", ImageID: input.ImageID, ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey)}, nil
}

func (p *countingRuntimeProvider) DestroyWorkspaceRuntime(_ context.Context, workspaceID string) (WorkspaceRuntime, error) {
	p.destroyCalls.Add(1)
	return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "destroyed"}, nil
}

type blockingRuntimeProvider struct {
	testProvider
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (p *blockingRuntimeProvider) CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, _ ComputeAllocation, _ StorageVolume) (WorkspaceRuntime, error) {
	if p.calls.Add(1) == 1 {
		close(p.entered)
	}
	select {
	case <-p.release:
		return WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: input.WorkspaceID, Status: "running", Ready: true, ImageID: input.ImageID, ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey)}, nil
	case <-ctx.Done():
		return WorkspaceRuntime{}, ctx.Err()
	}
}

func (testProvider) PrepareComputeAllocation(_ context.Context, input ComputeAllocationInput) (ComputeAllocationPreparation, error) {
	plan := packagePlan(input.PackageID)
	return ComputeAllocationPreparation{
		PoolID: plan.ID, PackageID: input.PackageID, NodePoolID: input.NodePoolID, InstanceType: plan.InstanceType,
		MaxReplicas: 10, BaselineReplicas: 0, TargetReplicas: 1, BeforeMachineNames: []string{},
	}, nil
}

func (testProvider) CreateComputeAllocation(_ context.Context, input ComputeAllocationExecution) (ComputeAllocation, error) {
	machine := firstNonEmpty(input.Allocation.ID, "machine-test")
	return ComputeAllocation{
		ID: input.Allocation.ID, AccountID: input.Allocation.AccountID, WorkspaceID: input.Allocation.WorkspaceID, PackageID: input.Allocation.PackageID,
		Status: "running", Provider: "tencent-tke", ProviderResourceID: "machine/" + machine, ProviderRequestID: "compute-test",
		PoolID: input.Plan.PoolID, NodePoolID: input.Plan.NodePoolID, MachineName: machine, InstanceID: "ins-" + machine, CVMInstanceID: "ins-" + machine,
		NodeName: "10.0.0.11", PrivateIP: "10.0.0.11", InstanceType: input.Plan.InstanceType, Zone: "ap-guangzhou-3", ChargeType: "PREPAID",
		RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-16T00:00:00Z", ProviderData: map[string]string{"instanceType": input.Plan.InstanceType, "zone": "ap-guangzhou-3", "chargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16T00:00:00Z"},
	}, nil
}

func (testProvider) MonthlyPreflight(_ context.Context, input MonthlyPreflightInput) (MonthlyPreflight, error) {
	return MonthlyPreflight{
		ResourceType: input.ResourceType, PackageID: input.PackageID, SizeGB: input.SizeGB, Zone: input.Zone,
		Available: true, ChargeType: "PREPAID", PeriodMonths: 1, RenewFlag: "NOTIFY_AND_MANUAL_RENEW", ProviderPriceCNY: 7.5,
		ProviderRequestIDs: map[string]string{"nodePool": "req-pool", "subnets": "req-subnets", "availability": "req-availability", "quota": "req-quota", "price": "req-price"},
	}, nil
}

func (testProvider) TagComputeMachine(_ context.Context, _ ProviderMachine, _ MachineOwnership) error {
	return nil
}

func (testProvider) SyncComputeAllocation(_ context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	allocation.Status = "running"
	return allocation, nil
}

func (testProvider) RenewComputeAllocation(_ context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	allocation.Deadline = "2026-09-16T00:00:00Z"
	allocation.RenewFlag = "NOTIFY_AND_MANUAL_RENEW"
	allocation.ChargeType = "PREPAID"
	if allocation.ProviderData == nil {
		allocation.ProviderData = map[string]string{}
	}
	allocation.ProviderData["deadline"] = allocation.Deadline
	allocation.ProviderData["renewFlag"] = allocation.RenewFlag
	allocation.ProviderData["renewalResult"] = "renewed"
	return allocation, nil
}

func (testProvider) DestroyComputeAllocation(_ context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	allocation.Status = "destroyed"
	return allocation, nil
}

func (testProvider) CreateStorageVolume(_ context.Context, input StorageVolumeInput) (StorageVolume, error) {
	return StorageVolume{ID: "vol-test", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", ProviderRequestID: providerRequestID("storage", input.IdempotencyKey), SizeGB: input.SizeGB}, nil
}

func (testProvider) SyncStorageVolume(_ context.Context, volume StorageVolume) (StorageVolume, error) {
	volume.Status = "ready"
	return volume, nil
}

func (testProvider) RenewStorageVolume(_ context.Context, volume StorageVolume) (StorageVolume, error) {
	volume.Deadline = "2026-09-16T00:00:00Z"
	volume.RenewFlag = "NOTIFY_AND_MANUAL_RENEW"
	volume.ProviderData = map[string]string{"diskChargeType": "PREPAID"}
	return volume, nil
}

func (testProvider) DestroyStorageVolume(_ context.Context, volume StorageVolume) (StorageVolume, error) {
	volume.Status = "destroyed"
	return volume, nil
}

func (testProvider) CreateStorageAttachment(_ context.Context, input StorageAttachmentInput, _ ComputeAllocation, _ StorageVolume) (StorageAttachment, error) {
	return StorageAttachment{ID: "att-test", WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Status: "attached", ProviderRequestID: providerRequestID("storage-attach", input.IdempotencyKey)}, nil
}

func (testProvider) DetachStorageAttachment(_ context.Context, attachment StorageAttachment) (StorageAttachment, error) {
	attachment.Status = "detached"
	return attachment, nil
}

func (testProvider) CreateWorkspaceRuntime(_ context.Context, input WorkspaceRuntimeInput, _ ComputeAllocation, _ StorageVolume) (WorkspaceRuntime, error) {
	return WorkspaceRuntime{ID: "rt-test", WorkspaceID: input.WorkspaceID, Status: "running", ServiceName: "opl-ca-test", ImageID: input.ImageID, ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey), Access: RuntimeAccess{Username: "admin", Password: "runtime-password-alpha", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-ca-test-env"}}, nil
}

func (testProvider) DestroyWorkspaceRuntime(_ context.Context, workspaceID string) (WorkspaceRuntime, error) {
	return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "destroyed"}, nil
}

func (testProvider) WorkspaceRuntimeStatus(_ context.Context, workspaceID string) (WorkspaceRuntime, error) {
	return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "not_found"}, nil
}

func (testProvider) UpsertGatewaySecret(_ context.Context, input GatewaySecretInput) (GatewaySecret, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(input.GatewayAPIKey)))
	return GatewaySecret{SecretRef: gatewaySecretName(input.WorkspaceID), Version: digest[:16], Fingerprint: "sha256:" + digest}, nil
}

func (testProvider) Readiness(_ context.Context) (map[string]any, error) {
	return map[string]any{"provider": "test", "ready": true}, nil
}
