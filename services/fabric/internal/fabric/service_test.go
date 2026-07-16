package fabric

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestContentTransferResumesAndPublishesOnlyVerifiedBytes(t *testing.T) {
	const chunkSize = 4 << 20
	body := bytes.Repeat([]byte("x"), 2*chunkSize+17)
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	provider := &contentTestProvider{}
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(provider, store)
	ctx := context.Background()

	transfer, err := service.CreateTransfer(ctx, TransferInput{
		OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha",
		Path: "inputs/paper.txt", Digest: digest, Size: int64(len(body)),
		IdempotencyKey: "transfer-once",
	})
	if err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	if _, err := service.PutTransferChunk(ctx, transfer.TransferID, 1, body[chunkSize:2*chunkSize], fmt.Sprintf("%x", sha256.Sum256(body[chunkSize:2*chunkSize]))); err != nil {
		t.Fatalf("put middle chunk: %v", err)
	}

	restarted := NewServiceWithOperationStore(provider, store)
	resumed, err := restarted.Transfer(ctx, transfer.TransferID)
	if err != nil || len(resumed.ReceivedChunks) != 1 || resumed.ReceivedChunks[0] != 1 {
		t.Fatalf("resumed transfer = %#v err=%v", resumed, err)
	}
	changed := bytes.Repeat([]byte("y"), chunkSize)
	if _, err := restarted.PutTransferChunk(ctx, transfer.TransferID, 1, changed, fmt.Sprintf("%x", sha256.Sum256(changed))); !errors.Is(err, ErrTransferChunkConflict) {
		t.Fatalf("changed replay error = %v, want chunk conflict", err)
	}
	if _, err := restarted.CompleteTransfer(ctx, transfer.TransferID); !errors.Is(err, ErrTransferIncomplete) {
		t.Fatalf("incomplete error = %v", err)
	}
	for index, chunk := range [][]byte{body[:chunkSize], body[2*chunkSize:]} {
		actualIndex := index
		if index == 1 {
			actualIndex = 2
		}
		if _, err := restarted.PutTransferChunk(ctx, transfer.TransferID, actualIndex, chunk, fmt.Sprintf("%x", sha256.Sum256(chunk))); err != nil {
			t.Fatalf("put chunk %d: %v", actualIndex, err)
		}
	}
	completed, err := restarted.CompleteTransfer(ctx, transfer.TransferID)
	if err != nil || completed.Status != "completed" {
		t.Fatalf("complete transfer = %#v err=%v", completed, err)
	}
	if string(provider.published) != string(body) || provider.path != "inputs/paper.txt" {
		t.Fatalf("published path=%q body=%q", provider.path, provider.published)
	}
	downloaded, err := restarted.Content(ctx, "workspace-alpha", digest)
	if err != nil || string(downloaded.Body) != string(body) {
		t.Fatalf("downloaded = %#v err=%v", downloaded, err)
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

func TestSyncStorageVolumeCleansUpTimedOutPVCBeforeFailing(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		deleteErr  error
		wantStatus string
	}{
		{name: "released", wantStatus: "released"},
		{name: "delete unconfirmed", deleteErr: errors.New("pvc delete failed"), wantStatus: "quarantined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &pendingStorageProvider{deleteErr: tc.deleteErr}
			service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
			service.now = func() time.Time { return now }
			service.volumes["storage-alpha"] = StorageVolume{ID: "storage-alpha", AccountID: "acct-alpha", Status: "pending", ProviderResourceID: "pvc/storage-alpha-data", CreatedAt: now.Add(-11 * time.Minute)}

			volume, err := service.SyncStorageVolume(context.Background(), "storage-alpha")
			if err != nil || volume.Status != tc.wantStatus || provider.deleteCalls != 1 {
				t.Fatalf("timed out storage = %#v err=%v deleteCalls=%d", volume, err, provider.deleteCalls)
			}
		})
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

func TestCatalogExposesConfiguredWorkspacePackagesIndependently(t *testing.T) {
	for _, tc := range []struct {
		name           string
		basicPool      string
		proPool        string
		basicAvailable bool
		proAvailable   bool
	}{
		{name: "neither configured"},
		{name: "basic only", basicPool: "np-basic", basicAvailable: true},
		{name: "pro only", proPool: "np-pro", proAvailable: true},
		{name: "both configured", basicPool: "np-basic", proPool: "np-pro", basicAvailable: true, proAvailable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_ID", tc.basicPool)
			t.Setenv("OPL_PRO_COMPUTE_NODE_POOL_ID", tc.proPool)
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
			if basic.ID != "basic" || basic.CPU != 2 || basic.MemoryGB != 4 || basic.DiskGB != 10 || basic.Available != tc.basicAvailable ||
				pro.ID != "pro" || pro.CPU != 8 || pro.MemoryGB != 16 || pro.DiskGB != 100 || pro.Available != tc.proAvailable {
				t.Fatalf("unexpected commercial catalog: %#v", catalog.WorkspacePackages)
			}
		})
	}
}

type resourceBoundaryProvider struct {
	testProvider
	computeCalls  int
	storageCalls  int
	storageInputs []StorageVolumeInput
}

func (p *resourceBoundaryProvider) ReconcileComputePool(ctx context.Context, input ComputePoolDemand) (ComputePoolState, error) {
	p.computeCalls++
	return p.testProvider.ReconcileComputePool(ctx, input)
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

type countingBlockedPoolProvider struct {
	testProvider
	calls   atomic.Int32
	entered chan ComputePoolDemand
	release chan struct{}
}

func (p *countingBlockedPoolProvider) ReconcileComputePool(ctx context.Context, input ComputePoolDemand) (ComputePoolState, error) {
	p.calls.Add(1)
	p.entered <- input
	select {
	case <-p.release:
		return testProvider{}.ReconcileComputePool(ctx, input)
	case <-ctx.Done():
		return ComputePoolState{}, ctx.Err()
	}
}

func TestCreateComputeAllocationReplaysStartedClaimWithoutIncreasingDemand(t *testing.T) {
	provider := &countingBlockedPoolProvider{entered: make(chan ComputePoolDemand, 2), release: make(chan struct{})}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	input := ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "compute-replay"}
	t.Cleanup(func() { close(provider.release) })

	first, err := service.CreateComputeAllocation(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	demand := <-provider.entered
	replayed, err := service.CreateComputeAllocation(context.Background(), input)
	if err != nil || replayed.ID != first.ID || replayed.Status != first.Status || demand.DesiredReplicas != 1 || provider.calls.Load() != 1 {
		t.Fatalf("first=%#v replayed=%#v err=%v demand=%#v calls=%d", first, replayed, err, demand, provider.calls.Load())
	}
	operations, err := service.ListOperations(context.Background())
	if err != nil || len(operations) != 1 || operations[0].Status != "started" {
		t.Fatalf("operations=%#v err=%v", operations, err)
	}
}

func TestCreateComputeAllocationRejectsSameKeyWithDifferentRequest(t *testing.T) {
	provider := &countingBlockedPoolProvider{entered: make(chan ComputePoolDemand, 2), release: make(chan struct{})}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	t.Cleanup(func() { close(provider.release) })

	first, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "compute-conflict"})
	if err != nil {
		t.Fatal(err)
	}
	<-provider.entered
	replayed, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-other", PackageID: "basic", IdempotencyKey: "compute-conflict"})
	if err == nil || err.Error() != "compute_idempotency_conflict" || replayed.ID != "" || provider.calls.Load() != 1 {
		t.Fatalf("first=%#v replayed=%#v err=%v calls=%d", first, replayed, err, provider.calls.Load())
	}
}

func TestCreateComputeAllocationConcurrentSameKeyClaimsOnce(t *testing.T) {
	provider := &countingBlockedPoolProvider{entered: make(chan ComputePoolDemand, 20), release: make(chan struct{})}
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(provider, store)
	input := ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "compute-concurrent"}
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

type failedPoolProvider struct{ testProvider }

func (failedPoolProvider) ReconcileComputePool(_ context.Context, input ComputePoolDemand) (ComputePoolState, error) {
	return ComputePoolState{PoolID: input.PoolID, DesiredReplicas: input.DesiredReplicas, CurrentReplicas: 0}, nil
}

func TestCreateComputeAllocationReplaysSucceededAndFailedResults(t *testing.T) {
	t.Run("succeeded", func(t *testing.T) {
		service := NewServiceWithOperationStore(testProvider{}, NewMemoryOperationStore())
		input := ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "compute-succeeded"}
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
		previousAttempts := poolReconcileAttempts
		poolReconcileAttempts = 1
		t.Cleanup(func() { poolReconcileAttempts = previousAttempts })
		service := NewServiceWithOperationStore(failedPoolProvider{}, NewMemoryOperationStore())
		input := ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "compute-failed"}
		first, err := service.CreateComputeAllocation(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		waitForOperation(t, service, "create_compute_allocation", "compute_allocation", first.ID, "failed")
		replayed, err := service.CreateComputeAllocation(context.Background(), input)
		if err == nil || err.Error() != "compute_operation_failed" || replayed.ID != first.ID || replayed.Status != "failed" {
			t.Fatalf("first=%#v replayed=%#v err=%v", first, replayed, err)
		}
	})
}

func TestResourceMutationsAppendFabricOperationFacts(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	ctx := context.Background()

	compute, err := service.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "ops-compute"})
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
	runtime, err := service.CreateWorkspaceRuntime(ctx, WorkspaceRuntimeInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, ImageID: "one-person-lab-app", GatewaySecretRef: gatewaySecretName("acct-alpha"), IdempotencyKey: "ops-runtime"})
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

func TestStorageSnapshotRestorePersistsAndKeepsSourceVolume(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	compute, err := service.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "source-compute"})
	if err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")
	volume, err := service.CreateStorageVolume(ctx, StorageVolumeInput{ID: "vol-source", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: compute.ID, Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "source-volume"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.CreateStorageSnapshot(ctx, StorageSnapshotInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", VolumeID: volume.ID, IdempotencyKey: "snapshot-once"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != "ready" || snapshot.VolumeID != volume.ID {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	restarted := NewServiceWithOperationStore(testProvider{}, store)
	persisted, ok := restarted.GetStorageSnapshot(ctx, snapshot.ID)
	if !ok || persisted.ProviderSnapshotRef == "" {
		t.Fatalf("persisted snapshot = %#v, ok=%v", persisted, ok)
	}
	replayed, err := restarted.CreateStorageSnapshot(ctx, StorageSnapshotInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", VolumeID: volume.ID, IdempotencyKey: "snapshot-once"})
	if err != nil || replayed.ID != snapshot.ID {
		t.Fatalf("replayed snapshot = %#v, err=%v", replayed, err)
	}
	restored, err := restarted.RestoreStorageSnapshot(ctx, StorageRestoreInput{SnapshotID: snapshot.ID, AccountID: "acct-alpha", WorkspaceID: "ws-restored", TargetVolumeID: "vol-restored", IdempotencyKey: "restore-once"})
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID != "vol-restored" || restored.WorkspaceID != "ws-restored" || restored.SizeGB != 10 {
		t.Fatalf("restored volume = %#v", restored)
	}
	if source, ok := restarted.GetStorageVolume(ctx, volume.ID); !ok || source.Status != "ready" {
		t.Fatalf("source volume changed: %#v, ok=%v", source, ok)
	}
	destroyed, err := restarted.DestroyStorageSnapshot(ctx, snapshot.ID)
	if err != nil || destroyed.Status != "destroyed" {
		t.Fatalf("destroyed snapshot = %#v, err=%v", destroyed, err)
	}
}

func TestWorkspaceRuntimeCreationDoesNotReturnCredential(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	ctx := context.Background()

	compute, err := service.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "access-compute"})
	if err != nil {
		t.Fatalf("create compute: %v", err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")
	volume, err := service.CreateStorageVolume(ctx, StorageVolumeInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: compute.ID, Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "access-storage"})
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	runtime, err := service.CreateWorkspaceRuntime(ctx, WorkspaceRuntimeInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, ImageID: "one-person-lab-app", GatewaySecretRef: gatewaySecretName("acct-alpha"), IdempotencyKey: "access-runtime"})
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

func TestWorkspaceRuntimeRequiresOwnedGatewaySecretReference(t *testing.T) {
	provider := &countingRuntimeProvider{}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	service.computes["compute-alpha"] = ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "running"}
	service.volumes["storage-alpha"] = StorageVolume{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready"}
	for _, ref := range []string{"", gatewaySecretName("acct-other")} {
		_, err := service.CreateWorkspaceRuntime(context.Background(), WorkspaceRuntimeInput{
			WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha", ImageID: "one-person-lab-app",
			GatewaySecretRef: ref, IdempotencyKey: "runtime-ref-" + stableSuffix(ref)[:8],
		})
		if err == nil {
			t.Fatalf("runtime must reject Gateway Secret ref %q", ref)
		}
	}
	if provider.calls.Load() != 0 {
		t.Fatalf("invalid Gateway Secret refs reached provider %d times", provider.calls.Load())
	}
	valid, err := service.CreateWorkspaceRuntime(context.Background(), WorkspaceRuntimeInput{
		WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha", ImageID: "one-person-lab-app",
		GatewaySecretRef: gatewaySecretName("acct-alpha"), IdempotencyKey: "runtime-ref-valid",
	})
	if err != nil || !valid.Ready || provider.calls.Load() != 1 {
		t.Fatalf("valid runtime=%#v err=%v calls=%d", valid, err, provider.calls.Load())
	}
}

type countingGatewayProvider struct {
	testProvider
	calls atomic.Int32
}

func (p *countingGatewayProvider) UpsertGatewaySecret(ctx context.Context, input GatewaySecretInput) (GatewaySecret, error) {
	p.calls.Add(1)
	return p.testProvider.UpsertGatewaySecret(ctx, input)
}

func TestGatewaySecretWriteReplaysOneRedactedOperation(t *testing.T) {
	provider := &countingGatewayProvider{}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	input := GatewaySecretInput{AccountID: "acct-alpha", GatewayAPIKey: "raw-gateway-key", IdempotencyKey: "gateway-once"}
	secret, err := service.UpsertGatewaySecret(context.Background(), input)
	if err != nil || secret.SecretRef != gatewaySecretName("acct-alpha") || secret.Version == "" || secret.Fingerprint == "" {
		t.Fatalf("gateway secret=%#v err=%v", secret, err)
	}
	replayed, err := service.UpsertGatewaySecret(context.Background(), input)
	if err != nil || replayed != secret || provider.calls.Load() != 1 {
		t.Fatalf("gateway replay=%#v err=%v calls=%d", replayed, err, provider.calls.Load())
	}
	input.GatewayAPIKey = "rotated-gateway-key"
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

	compute, err := service.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "reject-compute"})
	if err != nil {
		t.Fatalf("create compute: %v", err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")
	otherCompute, err := service.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-beta", WorkspaceID: "ws-beta", PackageID: "basic", IdempotencyKey: "reject-compute-beta"})
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
	assertOperationFact(t, operations, "create_storage_attachment", "storage_attachment", "reject-missing-attach", "rejected")
	assertOperationFact(t, operations, "create_storage_attachment", "storage_attachment", "reject-cross-account-attach", "rejected")
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
			tc.configure(&compute, &volume)
			if err := validateAttachmentInput(StorageAttachmentInput{WorkspaceID: "ws-alpha"}, compute, volume); err == nil || errorCode(err) != "resource_workspace_mismatch" {
				t.Fatalf("attachment workspace isolation error=%v", err)
			}
			if err := validateRuntimeInput(WorkspaceRuntimeInput{WorkspaceID: "ws-alpha", GatewaySecretRef: gatewaySecretName("acct-alpha")}, compute, volume); err == nil || errorCode(err) != "resource_workspace_mismatch" {
				t.Fatalf("runtime workspace isolation error=%v", err)
			}
		})
	}
}

func TestServiceReplaysResourceStateFromOperationStore(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	original := NewServiceWithOperationStore(testProvider{}, store)

	compute, err := original.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "replay-compute"})
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
	runtime, err := replayed.CreateWorkspaceRuntime(ctx, WorkspaceRuntimeInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, ImageID: "one-person-lab-app", GatewaySecretRef: gatewaySecretName("acct-alpha"), IdempotencyKey: "replay-runtime"})
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

type countingAttachmentProvider struct {
	testProvider
	calls atomic.Int32
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

func TestCreateStorageAttachmentDoesNotReapplyPersistedIncompleteOperation(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   string
	}{
		{status: "started", want: "storage_attachment_operation_in_progress"},
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
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	service.computes["compute-alpha"] = ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", Status: "running", ServiceName: "opl-compute-alpha"}
	service.volumes["storage-alpha"] = StorageVolume{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", Status: "ready", ProviderResourceID: "pvc/storage-alpha"}
	service.volumes["storage-other"] = StorageVolume{ID: "storage-other", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", Status: "ready", ProviderResourceID: "pvc/storage-other"}
	input := WorkspaceRuntimeInput{WorkspaceID: "workspace-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha", ImageID: "one-person-lab-app", GatewaySecretRef: gatewaySecretName("acct-alpha"), IdempotencyKey: "runtime-once"}
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

func TestCreateWorkspaceRuntimeDoesNotReapplyPersistedIncompleteOperation(t *testing.T) {
	for _, status := range []string{"started", "failed"} {
		t.Run(status, func(t *testing.T) {
			provider := &countingRuntimeProvider{}
			store := NewMemoryOperationStore()
			input := runtimeTestInput("runtime-" + status)
			now := time.Now().UTC()
			operation := newOperation("create_workspace_runtime", "workspace_runtime", input.WorkspaceID, "acct-alpha", input.WorkspaceID, input.IdempotencyKey, hashInput(input), now)
			operation.ID = "persisted-" + status
			operation.Status = status
			operation.CreatedAt = now
			if status == "failed" {
				operation.FinishedAt = now
				operation.ErrorCode = "provider_error"
			}
			if err := store.Append(context.Background(), operation); err != nil {
				t.Fatalf("seed operation: %v", err)
			}

			service := runtimeTestService(provider, store)
			_, err := service.CreateWorkspaceRuntime(context.Background(), input)
			want := "runtime_operation_in_progress"
			if status == "failed" {
				want = "runtime_operation_failed"
			}
			if err == nil || err.Error() != want {
				t.Fatalf("persisted %s error = %v, want %s", status, err, want)
			}
			if calls := provider.calls.Load(); calls != 0 {
				t.Fatalf("provider calls = %d, want 0", calls)
			}
		})
	}
}

func runtimeTestService(provider Provider, store OperationStore) *Service {
	service := NewServiceWithOperationStore(provider, store)
	service.computes["compute-alpha"] = ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", Status: "running", ServiceName: "opl-compute-alpha"}
	service.volumes["storage-alpha"] = StorageVolume{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", Status: "ready", ProviderResourceID: "pvc/storage-alpha"}
	return service
}

func runtimeTestInput(key string) WorkspaceRuntimeInput {
	return WorkspaceRuntimeInput{WorkspaceID: "workspace-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha", ImageID: "one-person-lab-app", GatewaySecretRef: gatewaySecretName("acct-alpha"), IdempotencyKey: key}
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

func (p *blockingProvider) ReconcileComputePool(ctx context.Context, input ComputePoolDemand) (ComputePoolState, error) {
	<-p.done
	return testProvider{}.ReconcileComputePool(ctx, input)
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
	compute, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "async-destroy-create"})
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

type countingRuntimeProvider struct {
	testProvider
	calls        atomic.Int32
	destroyCalls atomic.Int32
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
	return WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: input.WorkspaceID, Status: "running", Ready: true, ServiceName: "opl-compute-alpha", ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey)}, nil
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
		return WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: input.WorkspaceID, Status: "running", Ready: true, ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey)}, nil
	case <-ctx.Done():
		return WorkspaceRuntime{}, ctx.Err()
	}
}

type contentTestProvider struct {
	testProvider
	path      string
	published []byte
}

func (p *contentTestProvider) PublishWorkspaceContent(_ context.Context, _ string, targetPath string, body []byte) error {
	p.path = targetPath
	p.published = append([]byte(nil), body...)
	return nil
}

func (testProvider) ReconcileComputePool(_ context.Context, input ComputePoolDemand) (ComputePoolState, error) {
	machines := make([]ProviderMachine, 0, input.DesiredReplicas)
	for index := int64(0); index < input.DesiredReplicas; index++ {
		id := fmt.Sprintf("%s-%03d", input.PoolID, index+1)
		machines = append(machines, ProviderMachine{MachineID: id, InstanceID: "ins-" + id, NodeName: id, InstanceType: input.InstanceType, Zone: "ap-guangzhou-3", ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-16T00:00:00Z", Ready: true})
	}
	return ComputePoolState{PoolID: input.PoolID, NodePoolID: "np-" + input.PoolID, DesiredReplicas: input.DesiredReplicas, CurrentReplicas: input.DesiredReplicas, ProviderRequestID: "pool-test", Machines: machines}, nil
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

func (testProvider) DeleteComputeMachine(_ context.Context, _ ProviderMachine, _ MachineOwnership) error {
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

func (testProvider) CreateStorageSnapshot(_ context.Context, input StorageSnapshotInput, volume StorageVolume) (StorageSnapshot, error) {
	return StorageSnapshot{ID: "snap-test", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, VolumeID: volume.ID, Status: "ready", Provider: "test", ProviderSnapshotRef: "volumesnapshot/snap-test", ProviderRequestID: "snapshot-request", SizeGB: volume.SizeGB, CreatedAt: time.Now().UTC()}, nil
}

func (testProvider) SyncStorageSnapshot(_ context.Context, snapshot StorageSnapshot) (StorageSnapshot, error) {
	return snapshot, nil
}

func (testProvider) RestoreStorageSnapshot(_ context.Context, input StorageRestoreInput, snapshot StorageSnapshot) (StorageVolume, error) {
	return StorageVolume{ID: input.TargetVolumeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", Provider: "test", ProviderResourceID: "pvc/" + input.TargetVolumeID, ProviderRequestID: "restore-request", SizeGB: snapshot.SizeGB, CreatedAt: time.Now().UTC()}, nil
}

func (testProvider) DestroyStorageSnapshot(_ context.Context, snapshot StorageSnapshot) (StorageSnapshot, error) {
	snapshot.Status = "destroyed"
	return snapshot, nil
}

func (testProvider) CreateStorageAttachment(_ context.Context, input StorageAttachmentInput, _ ComputeAllocation, _ StorageVolume) (StorageAttachment, error) {
	return StorageAttachment{ID: "att-test", WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Status: "attached", ProviderRequestID: providerRequestID("storage-attach", input.IdempotencyKey)}, nil
}

func (testProvider) DetachStorageAttachment(_ context.Context, attachment StorageAttachment) (StorageAttachment, error) {
	attachment.Status = "detached"
	return attachment, nil
}

func (testProvider) CreateWorkspaceRuntime(_ context.Context, input WorkspaceRuntimeInput, _ ComputeAllocation, _ StorageVolume) (WorkspaceRuntime, error) {
	return WorkspaceRuntime{ID: "rt-test", WorkspaceID: input.WorkspaceID, Status: "running", ServiceName: "opl-ca-test", ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey), Access: RuntimeAccess{Username: "admin", Password: "runtime-password-alpha", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-ca-test-env"}}, nil
}

func (testProvider) DestroyWorkspaceRuntime(_ context.Context, workspaceID string) (WorkspaceRuntime, error) {
	return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "destroyed"}, nil
}

func (testProvider) WorkspaceRuntimeStatus(_ context.Context, workspaceID string) (WorkspaceRuntime, error) {
	return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "not_found"}, nil
}

func (testProvider) UpsertGatewaySecret(_ context.Context, input GatewaySecretInput) (GatewaySecret, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(input.GatewayAPIKey)))
	return GatewaySecret{SecretRef: gatewaySecretName(input.AccountID), Version: digest[:16], Fingerprint: "sha256:" + digest}, nil
}

func (testProvider) Readiness(_ context.Context) (map[string]any, error) {
	return map[string]any{"provider": "test", "ready": true}, nil
}
