package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
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
	if workspace.RuntimeUsername != "admin" || workspace.CredentialSecretRef != "opl-compute-alpha-env" {
		t.Fatalf("workspace creation must carry credential metadata only: %#v", workspace)
	}
	encoded, _ := json.Marshal(workspace)
	if strings.Contains(string(encoded), "runtime-password-alpha") || strings.Contains(string(encoded), "runtimePassword") {
		t.Fatalf("workspace creation leaked runtime password: %s", encoded)
	}
	wantCalls := []string{"fabric.runtime", "ledger.receipt"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
	if len(ledger.receipts) != 1 || len(ledger.receipts[0].Continuation) != 0 {
		t.Fatalf("workspace lifecycle receipt must not claim an execution continuation: %#v", ledger.receipts)
	}
}

func TestComputeActivationWaitsForRunningProviderEvidence(t *testing.T) {
	calls := []string{}
	ledger := &fakeLedgerClient{calls: &calls}
	fabric := &runningComputeFabricClient{fakeFabricClient: fakeFabricClient{calls: &calls}}
	service := NewService(ledger, fabric)
	created, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic", HoldAmountCents: 16900, ActivationAmountCents: 100}, "create-compute")
	if err != nil {
		t.Fatal(err)
	}
	if created.BillingStatus == "active" || !reflect.DeepEqual(calls, []string{"ledger.hold", "fabric.compute"}) {
		t.Fatalf("create activated before machine evidence: created=%#v calls=%#v", created, calls)
	}
	synced, err := service.SyncComputeAllocation(context.Background(), DestroyResourceInput{ID: "compute-alpha", AccountID: "acct-alpha", HoldID: created.HoldID, HoldAmountCents: created.HoldAmountCents}, "sync-compute")
	if err != nil {
		t.Fatal(err)
	}
	if synced.BillingStatus != "active" || synced.LedgerEntryID == "" || !reflect.DeepEqual(calls, []string{"ledger.hold", "fabric.compute", "fabric.compute-sync", "ledger.activate"}) {
		t.Fatalf("sync did not activate: synced=%#v calls=%#v", synced, calls)
	}
}

func TestComputeActivationRequiresClaimedMachineIdentity(t *testing.T) {
	calls := []string{}
	service := NewService(&fakeLedgerClient{calls: &calls}, &incompleteComputeFabricClient{fakeFabricClient: fakeFabricClient{calls: &calls}})

	synced, err := service.SyncComputeAllocation(context.Background(), DestroyResourceInput{ID: "compute-alpha", AccountID: "acct-alpha", HoldID: "hold-alpha"}, "sync-compute")
	if err != nil {
		t.Fatal(err)
	}
	if synced.BillingStatus == "active" || !reflect.DeepEqual(calls, []string{"fabric.compute-sync"}) {
		t.Fatalf("compute without machine identity activated: synced=%#v calls=%#v", synced, calls)
	}
}

func TestComputeActivationUsesResourceIdempotencyKey(t *testing.T) {
	calls := []string{}
	ledger := &fakeLedgerClient{calls: &calls}
	service := NewService(ledger, &runningComputeFabricClient{fakeFabricClient: fakeFabricClient{calls: &calls}})
	input := DestroyResourceInput{ID: "compute-alpha", AccountID: "acct-alpha", HoldID: "hold-alpha"}

	if _, err := service.SyncComputeAllocation(context.Background(), input, "readiness"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SyncComputeAllocation(context.Background(), input, "provider-reconcile"); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(ledger.activationKeys, []string{"compute:compute-alpha:hold-activation", "compute:compute-alpha:hold-activation"}) {
		t.Fatalf("activation keys = %#v", ledger.activationKeys)
	}
}

func TestStorageActivationRequiresBoundPVCIdentity(t *testing.T) {
	calls := []string{}
	service := NewService(&fakeLedgerClient{calls: &calls}, &readyStorageWithoutIdentityFabricClient{fakeFabricClient: fakeFabricClient{calls: &calls}})
	volume, err := service.SyncStorageVolume(context.Background(), DestroyResourceInput{ID: "storage-alpha", AccountID: "acct-alpha", HoldID: "hold-alpha"}, "sync-storage")
	if err != nil {
		t.Fatal(err)
	}
	if volume.BillingStatus == "active" || !reflect.DeepEqual(calls, []string{"fabric.storage-sync"}) {
		t.Fatalf("storage without PVC identity activated: volume=%#v calls=%#v", volume, calls)
	}
}

func TestCreateWorkspaceCompensatesReceiptFailure(t *testing.T) {
	calls := []string{}
	ledger := &failingReceiptLedger{fakeLedgerClient: fakeLedgerClient{calls: &calls}, err: errReceiptWrite}
	fabric := &compensatingFabricClient{fakeFabricClient: fakeFabricClient{calls: &calls}}
	service := NewService(ledger, fabric)

	workspace, err := service.CreateWorkspace(context.Background(), CreateWorkspaceInput{
		AccountID: "acct-alpha", OwnerID: "user-alpha", Name: "Alpha", PackageID: "basic",
		AttachmentID: "attachment-alpha", ComputeID: "compute-alpha", VolumeID: "volume-alpha",
	}, "workspace-once")
	if !errors.Is(err, errReceiptWrite) || workspace.ID != "" || fabric.destroyedWorkspaceID == "" {
		t.Fatalf("receipt failure was not compensated: workspace=%#v err=%v fabric=%#v", workspace, err, fabric)
	}
	if calls[len(calls)-1] != "fabric.runtime-destroy" {
		t.Fatalf("calls = %#v, want runtime destroy last", calls)
	}
}

func TestCreateWorkspaceJoinsReceiptAndCleanupFailures(t *testing.T) {
	calls := []string{}
	ledger := &failingReceiptLedger{fakeLedgerClient: fakeLedgerClient{calls: &calls}, err: errReceiptWrite}
	fabric := &compensatingFabricClient{fakeFabricClient: fakeFabricClient{calls: &calls}, destroyErr: errRuntimeCleanup}
	service := NewService(ledger, fabric)

	_, err := service.CreateWorkspace(context.Background(), CreateWorkspaceInput{
		AccountID: "acct-alpha", OwnerID: "user-alpha", Name: "Alpha", PackageID: "basic",
		AttachmentID: "attachment-alpha", ComputeID: "compute-alpha", VolumeID: "volume-alpha",
	}, "workspace-once")
	if !errors.Is(err, errReceiptWrite) || !errors.Is(err, errRuntimeCleanup) {
		t.Fatalf("joined error = %v", err)
	}
}

func TestCreateWorkspaceCompensatesAfterRequestCancellation(t *testing.T) {
	calls := []string{}
	ctx, cancel := context.WithCancel(context.Background())
	ledger := &failingReceiptLedger{fakeLedgerClient: fakeLedgerClient{calls: &calls}, err: errReceiptWrite, beforeReturn: cancel}
	fabric := &compensatingFabricClient{fakeFabricClient: fakeFabricClient{calls: &calls}}
	service := NewService(ledger, fabric)

	_, err := service.CreateWorkspace(ctx, CreateWorkspaceInput{
		AccountID: "acct-alpha", OwnerID: "user-alpha", Name: "Alpha", PackageID: "basic",
		AttachmentID: "attachment-alpha", ComputeID: "compute-alpha", VolumeID: "volume-alpha",
	}, "workspace-once")
	if !errors.Is(err, errReceiptWrite) || fabric.destroyCtxErr != nil || !fabric.destroyHasDeadline {
		t.Fatalf("canceled receipt cleanup: err=%v cleanupCtxErr=%v hasDeadline=%t", err, fabric.destroyCtxErr, fabric.destroyHasDeadline)
	}
}

var (
	errReceiptWrite   = errors.New("receipt write failed")
	errRuntimeCleanup = errors.New("runtime cleanup failed")
)

type failingReceiptLedger struct {
	fakeLedgerClient
	err          error
	beforeReturn func()
}

func (l *failingReceiptLedger) RecordReceipt(context.Context, clients.ReceiptInput, string) (clients.Receipt, error) {
	*l.calls = append(*l.calls, "ledger.receipt")
	if l.beforeReturn != nil {
		l.beforeReturn()
	}
	return clients.Receipt{}, l.err
}

type compensatingFabricClient struct {
	fakeFabricClient
	destroyedWorkspaceID string
	destroyErr           error
	destroyCtxErr        error
	destroyHasDeadline   bool
}

func (f *compensatingFabricClient) DestroyWorkspaceRuntime(ctx context.Context, workspaceID, _ string) (clients.WorkspaceRuntime, error) {
	*f.calls = append(*f.calls, "fabric.runtime-destroy")
	f.destroyedWorkspaceID = workspaceID
	f.destroyCtxErr = ctx.Err()
	_, f.destroyHasDeadline = ctx.Deadline()
	return clients.WorkspaceRuntime{WorkspaceID: workspaceID, Status: "destroyed"}, f.destroyErr
}

func TestResumeWorkspaceReusesIdentityStorageAndRecordsReceipt(t *testing.T) {
	calls := []string{}
	ledger := &fakeLedgerClient{calls: &calls}
	service := NewService(ledger, &fakeFabricClient{calls: &calls})

	workspace, err := service.ResumeWorkspace(context.Background(), ResumeWorkspaceInput{
		WorkspaceID:  "workspace-alpha",
		AccountID:    "acct-alpha",
		OwnerID:      "usr-owner",
		Name:         "Alpha Lab",
		PackageID:    "basic",
		URL:          "https://workspace.medopl.cn/w/workspace-alpha/",
		AttachmentID: "attachment-replacement",
		ComputeID:    "compute-replacement",
		VolumeID:     "volume-alpha",
	}, "resume-once")
	if err != nil {
		t.Fatalf("resume workspace: %v", err)
	}

	if workspace.ID != "workspace-alpha" || workspace.URL != "https://workspace.medopl.cn/w/workspace-alpha/" || workspace.VolumeID != "volume-alpha" {
		t.Fatalf("resume changed stable workspace identity or storage: %#v", workspace)
	}
	if workspace.ComputeID != "compute-replacement" || workspace.AttachmentID != "attachment-replacement" || workspace.Status != "running" {
		t.Fatalf("resume did not project replacement runtime resources: %#v", workspace)
	}
	if len(ledger.receipts) != 1 || ledger.receipts[0].Type != "workspace.compute_restarted" || ledger.receipts[0].WorkspaceID != "workspace-alpha" || ledger.receipts[0].JobID != "runtime-alpha" || len(ledger.receipts[0].Continuation) != 0 {
		t.Fatalf("unexpected resume receipt: %#v", ledger.receipts)
	}
	wantCalls := []string{"fabric.runtime", "ledger.receipt"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestResumeWorkspaceProjectsUnreadyFabricRuntime(t *testing.T) {
	calls := []string{}
	service := NewService(&fakeLedgerClient{calls: &calls}, &fakeFabricClient{calls: &calls, runtime: clients.WorkspaceRuntime{ID: "runtime-new", WorkspaceID: "workspace-alpha", Status: "provisioning", ServiceName: "opl-compute-new", Ready: false}})

	workspace, err := service.ResumeWorkspace(context.Background(), ResumeWorkspaceInput{WorkspaceID: "workspace-alpha", AccountID: "acct-alpha", AttachmentID: "attachment-new", ComputeID: "compute-new", VolumeID: "volume-alpha"}, "resume-provisioning")
	if err != nil {
		t.Fatalf("resume provisioning workspace: %v", err)
	}
	if workspace.Status != "provisioning" || workspace.RuntimeReady || workspace.CredentialStatus != "" || workspace.RuntimeUsername != "" {
		t.Fatalf("resume advertised an unready runtime as openable: %#v", workspace)
	}
}

func TestExecuteApprovedRequestCreatesJobAndReceipt(t *testing.T) {
	calls := []string{}
	service := NewService(&fakeLedgerClient{calls: &calls}, &fakeFabricClient{calls: &calls})

	result, err := service.Execute(context.Background(), ExecuteInput{
		OrganizationID: "org-alpha",
		WorkspaceID:    "workspace-alpha",
		ProjectID:      "project-alpha",
		TaskID:         "task-alpha",
		RequestID:      "request-alpha",
		ApprovalID:     "approval-alpha",
		EnvironmentRef: "environment-alpha",
	}, "execute-once")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.RequestID != "request-alpha" || result.ApprovalID != "approval-alpha" || result.JobID != "job-alpha" || result.ReceiptID != "receipt-alpha" || result.ContinuationID != "continuation-alpha" {
		t.Fatalf("unexpected execution result: %#v", result)
	}
	wantCalls := []string{"fabric.job", "ledger.receipt"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestSyncExecutionFinalizesAcceptedEvidence(t *testing.T) {
	calls := []string{}
	ledger := &fakeLedgerClient{calls: &calls, artifacts: map[string]clients.Artifact{"artifact-alpha": {ArtifactID: "artifact-alpha", JobID: "job-alpha", Digest: "sha256:alpha"}}, reviews: map[string]clients.Review{"review-alpha": {ReviewID: "review-alpha", JobID: "job-alpha", Decision: "accepted", InputArtifactDigests: []string{"sha256:alpha"}}}}
	fabric := &fakeFabricClient{calls: &calls, job: clients.Job{JobID: "job-alpha", Status: "succeeded", Attempt: 1, ArtifactIDs: []string{"artifact-alpha"}, ReviewIDs: []string{"review-alpha"}}}
	result, err := NewService(ledger, fabric).SyncExecution(context.Background(), ExecutionSyncInput{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", RequestID: "request-alpha", ApprovalID: "approval-alpha", JobID: "job-alpha", ReceiptID: "receipt-running", EnvironmentRef: "environment-alpha"})
	if err != nil {
		t.Fatalf("sync execution: %v", err)
	}
	if result.Status != "completed" || result.ReceiptID != "receipt-alpha" || result.ContinuationID != "continuation-alpha" || len(result.ArtifactIDs) != 1 || len(result.ReviewIDs) != 1 {
		t.Fatalf("unexpected sync result: %#v", result)
	}
	final := ledger.receipts[len(ledger.receipts)-1]
	if final.Status != "completed" || final.SupersedesReceiptID != "receipt-running" || len(final.Continuation) == 0 {
		t.Fatalf("unexpected final receipt: %#v", final)
	}
	receiptCount := len(ledger.receipts)
	replayed, err := NewService(ledger, fabric).SyncExecution(context.Background(), ExecutionSyncInput{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", RequestID: "request-alpha", ApprovalID: "approval-alpha", JobID: "job-alpha", ReceiptID: result.ReceiptID, ContinuationID: result.ContinuationID, Status: result.Status, EnvironmentRef: "environment-alpha"})
	if err != nil || replayed.ReceiptID != result.ReceiptID || replayed.ContinuationID != result.ContinuationID || len(ledger.receipts) != receiptCount {
		t.Fatalf("idempotent sync = %#v receipts=%d err=%v", replayed, len(ledger.receipts), err)
	}
	fabric.job.Attempt = 2
	if _, err := NewService(ledger, fabric).SyncExecution(context.Background(), ExecutionSyncInput{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", RequestID: "request-alpha", ApprovalID: "approval-alpha", JobID: "job-alpha", ReceiptID: result.ReceiptID, ContinuationID: result.ContinuationID, Status: result.Status, EnvironmentRef: "environment-alpha"}); err != nil || len(ledger.receipts) != receiptCount+1 {
		t.Fatalf("new attempt receipts=%d err=%v", len(ledger.receipts), err)
	}
}

func TestSyncExecutionBlocksRejectedReview(t *testing.T) {
	calls := []string{}
	ledger := &fakeLedgerClient{calls: &calls, artifacts: map[string]clients.Artifact{"artifact-alpha": {ArtifactID: "artifact-alpha", JobID: "job-alpha", Digest: "sha256:alpha"}}, reviews: map[string]clients.Review{"review-alpha": {ReviewID: "review-alpha", JobID: "job-alpha", Decision: "rejected", InputArtifactDigests: []string{"sha256:alpha"}}}}
	fabric := &fakeFabricClient{calls: &calls, job: clients.Job{JobID: "job-alpha", Status: "succeeded", ArtifactIDs: []string{"artifact-alpha"}, ReviewIDs: []string{"review-alpha"}}}
	result, err := NewService(ledger, fabric).SyncExecution(context.Background(), ExecutionSyncInput{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", RequestID: "request-alpha", ApprovalID: "approval-alpha", JobID: "job-alpha", ReceiptID: "receipt-running"})
	if err != nil || result.Status != "review_blocked" || result.ContinuationID != "" {
		t.Fatalf("unexpected blocked result: %#v, %v", result, err)
	}
	final := ledger.receipts[len(ledger.receipts)-1]
	if final.Status != "review_blocked" || len(final.Continuation) != 0 {
		t.Fatalf("unexpected blocked receipt: %#v", final)
	}
}

func TestSyncExecutionProjectsTerminalAndRunningJobs(t *testing.T) {
	for _, status := range []string{"failed", "timed_out", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			calls := []string{}
			ledger := &fakeLedgerClient{calls: &calls}
			fabric := &fakeFabricClient{calls: &calls, job: clients.Job{JobID: "job-alpha", Status: status, Attempt: 2, ErrorCode: "runner_failed"}}
			result, err := NewService(ledger, fabric).SyncExecution(context.Background(), ExecutionSyncInput{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", RequestID: "request-alpha", ApprovalID: "approval-alpha", JobID: "job-alpha", ReceiptID: "receipt-running"})
			if err != nil || result.Status != status || len(ledger.receipts) != 1 || ledger.receipts[0].Status != status {
				t.Fatalf("result = %#v receipts=%#v err=%v", result, ledger.receipts, err)
			}
		})
	}

	calls := []string{}
	ledger := &fakeLedgerClient{calls: &calls}
	fabric := &fakeFabricClient{calls: &calls, job: clients.Job{JobID: "job-alpha", Status: "running", Attempt: 1}}
	result, err := NewService(ledger, fabric).SyncExecution(context.Background(), ExecutionSyncInput{RequestID: "request-alpha", JobID: "job-alpha", ReceiptID: "receipt-running"})
	if err != nil || result.Status != "running" || len(ledger.receipts) != 0 {
		t.Fatalf("running result = %#v receipts=%#v err=%v", result, ledger.receipts, err)
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

func TestCreateComputeFailureReturnsReleasedHoldFacts(t *testing.T) {
	calls := []string{}
	service := NewService(&fakeLedgerClient{calls: &calls}, &failingComputeCreateFabricClient{fakeFabricClient: fakeFabricClient{calls: &calls}})
	compute, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{ID: "compute-alpha", AccountID: "acct-alpha", HoldAmountCents: 200, ActivationAmountCents: 100}, "compute-failed")
	if err == nil || compute.Status != "failed" || compute.HoldID != "hold-alpha" || compute.HoldReleaseID == "" || compute.LedgerEntryID != "ledger-release" || compute.WalletTransactionID != "wallet-release" || compute.BillingStatus != "stopped" {
		t.Fatalf("failed compute = %#v err=%v", compute, err)
	}
}

func TestCreateComputeFailureKeepsBillingStoppingWhenHoldReleaseFails(t *testing.T) {
	calls := []string{}
	ledger := &failingReleaseLedgerClient{fakeLedgerClient: fakeLedgerClient{calls: &calls}}
	service := NewService(ledger, &failingComputeCreateFabricClient{fakeFabricClient: fakeFabricClient{calls: &calls}})
	compute, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{ID: "compute-alpha", AccountID: "acct-alpha", HoldAmountCents: 200, ActivationAmountCents: 100}, "compute-failed")
	if err == nil || compute.Status != "failed" || compute.HoldID != "hold-alpha" || compute.HoldReleaseID != "" || compute.BillingStatus != "stopping" {
		t.Fatalf("failed compute with unreleased hold = %#v err=%v", compute, err)
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

func TestCreateStorageFailureReturnsReleasedHoldFacts(t *testing.T) {
	calls := []string{}
	service := NewService(&fakeLedgerClient{calls: &calls}, &failingStorageCreateFabricClient{fakeFabricClient: fakeFabricClient{calls: &calls}})
	volume, err := service.CreateStorageVolume(context.Background(), StorageVolumeInput{ID: "storage-alpha", AccountID: "acct-alpha", SizeGB: 10, HoldAmountCents: 200, ActivationAmountCents: 100}, "storage-failed")
	if err == nil || volume.Status != "failed" || volume.HoldID != "hold-alpha" || volume.HoldReleaseID == "" || volume.LedgerEntryID != "ledger-release" || volume.WalletTransactionID != "wallet-release" || volume.BillingStatus != "stopped" {
		t.Fatalf("failed storage = %#v err=%v", volume, err)
	}
}

func TestCreateStorageActivationFailureCleansProviderBeforeReleasingHold(t *testing.T) {
	calls := []string{}
	service := NewService(&failingActivationLedgerClient{fakeLedgerClient: fakeLedgerClient{calls: &calls}}, &readyStorageFabricClient{fakeFabricClient: fakeFabricClient{calls: &calls}})
	volume, err := service.CreateStorageVolume(context.Background(), StorageVolumeInput{ID: "storage-alpha", AccountID: "acct-alpha", SizeGB: 10, HoldAmountCents: 200, ActivationAmountCents: 100}, "storage-activation-failed")
	if err == nil || volume.Status != "failed" || volume.BillingStatus != "stopped" || volume.HoldReleaseID != "release-alpha" || volume.LedgerEntryID != "ledger-release" || volume.WalletTransactionID != "wallet-release" {
		t.Fatalf("storage activation compensation = %#v err=%v", volume, err)
	}
	wantCalls := []string{"ledger.hold", "fabric.storage", "ledger.activate", "fabric.storage-destroy", "ledger.release"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestCreateStorageActivationFailureKeepsHoldWhenProviderCleanupFails(t *testing.T) {
	calls := []string{}
	service := NewService(&failingActivationLedgerClient{fakeLedgerClient: fakeLedgerClient{calls: &calls}}, &failingStorageDestroyFabricClient{readyStorageFabricClient: readyStorageFabricClient{fakeFabricClient: fakeFabricClient{calls: &calls}}})
	volume, err := service.CreateStorageVolume(context.Background(), StorageVolumeInput{ID: "storage-alpha", AccountID: "acct-alpha", SizeGB: 10, HoldAmountCents: 200, ActivationAmountCents: 100}, "storage-cleanup-failed")
	if err == nil || volume.BillingStatus != "stopping" || volume.HoldReleaseID != "" {
		t.Fatalf("storage with failed cleanup = %#v err=%v", volume, err)
	}
	wantCalls := []string{"ledger.hold", "fabric.storage", "ledger.activate", "fabric.storage-destroy"}
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
	if compute.HoldReleaseID != "release-alpha" || compute.LedgerEntryID != "ledger-release" || compute.WalletTransactionID != "wallet-release" || compute.Wallet.AccountID != "acct-alpha" {
		t.Fatalf("compute missing release linkage: %#v", compute)
	}
	wantCalls := []string{"fabric.compute-destroy", "ledger.release"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

type destroyingComputeFabricClient struct{ fakeFabricClient }

func (f *destroyingComputeFabricClient) DestroyComputeAllocation(_ context.Context, id string, _ string) (clients.ComputeAllocation, error) {
	*f.calls = append(*f.calls, "fabric.compute-destroy")
	return clients.ComputeAllocation{ID: id, Status: "destroying", ProviderRequestID: "compute-destroy-request-alpha"}, nil
}

func TestDestroyingDoesNotReleaseComputeHold(t *testing.T) {
	calls := []string{}
	service := NewService(&fakeLedgerClient{calls: &calls}, &destroyingComputeFabricClient{fakeFabricClient{calls: &calls}})

	compute, err := service.DestroyComputeAllocation(context.Background(), DestroyResourceInput{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", HoldID: "hold-alpha", HoldAmountCents: 7862}, "destroy-compute")
	if err != nil || compute.Status != "destroying" || compute.BillingStatus != "stopping" || compute.HoldReleaseID != "" {
		t.Fatalf("destroying compute = %#v err=%v", compute, err)
	}
	wantCalls := []string{"fabric.compute-destroy"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestDestroyedComputeWithoutHoldStopsBilling(t *testing.T) {
	calls := []string{}
	service := NewService(&fakeLedgerClient{calls: &calls}, &fakeFabricClient{calls: &calls})

	compute, err := service.DestroyComputeAllocation(context.Background(), DestroyResourceInput{ID: "compute-alpha", AccountID: "acct-alpha"}, "destroy-compute")
	if err != nil || compute.Status != "destroyed" || compute.BillingStatus != "stopped" {
		t.Fatalf("destroyed compute = %#v err=%v", compute, err)
	}
	wantCalls := []string{"fabric.compute-destroy"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestSyncComputeAllocationReleasesHoldWhenProviderDeleted(t *testing.T) {
	calls := []string{}
	service := NewService(&fakeLedgerClient{calls: &calls}, &fakeFabricClient{calls: &calls})

	compute, err := service.SyncComputeAllocation(context.Background(), DestroyResourceInput{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", HoldID: "hold-alpha", HoldAmountCents: 7862}, "sync-compute")
	if err != nil {
		t.Fatalf("sync compute: %v", err)
	}
	if compute.Status != "external_deleted" || compute.BillingStatus != "stopped" || compute.HoldReleaseID != "release-alpha" || compute.LedgerEntryID != "ledger-release" || compute.WalletTransactionID != "wallet-release" {
		t.Fatalf("compute must stop billing on provider deletion: %#v", compute)
	}
	wantCalls := []string{"fabric.compute-sync", "ledger.release"}
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
	if volume.HoldReleaseID != "release-alpha" || volume.LedgerEntryID != "ledger-release" || volume.WalletTransactionID != "wallet-release" || volume.Wallet.AccountID != "acct-alpha" {
		t.Fatalf("storage missing release linkage: %#v", volume)
	}
	wantCalls := []string{"fabric.storage-destroy", "ledger.release"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestSyncStorageVolumeReleasesHoldWhenProviderDeleted(t *testing.T) {
	calls := []string{}
	service := NewService(&fakeLedgerClient{calls: &calls}, &fakeFabricClient{calls: &calls})

	volume, err := service.SyncStorageVolume(context.Background(), DestroyResourceInput{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", HoldID: "hold-alpha", HoldAmountCents: 101}, "sync-storage")
	if err != nil {
		t.Fatalf("sync storage: %v", err)
	}
	if volume.Status != "external_deleted" || volume.BillingStatus != "stopped" || volume.HoldReleaseID != "release-alpha" || volume.LedgerEntryID != "ledger-release" || volume.WalletTransactionID != "wallet-release" {
		t.Fatalf("storage must stop billing on provider deletion: %#v", volume)
	}
	wantCalls := []string{"fabric.storage-sync", "ledger.release"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

type fakeLedgerClient struct {
	calls          *[]string
	activationKeys []string
	artifacts      map[string]clients.Artifact
	reviews        map[string]clients.Review
	receipts       []clients.ReceiptInput
}

func (f *fakeLedgerClient) ManualTopUp(ctx context.Context, input clients.ManualTopUpInput, idempotencyKey string) (clients.ManualTopUpResult, error) {
	*f.calls = append(*f.calls, "ledger.topup")
	return clients.ManualTopUpResult{Wallet: clients.Wallet{AccountID: input.AccountID, BalanceCents: input.AmountCents, AvailableCents: input.AmountCents}}, nil
}

func (f *fakeLedgerClient) CreateHold(ctx context.Context, input clients.HoldInput, idempotencyKey string) (clients.HoldResult, error) {
	*f.calls = append(*f.calls, "ledger.hold")
	return clients.HoldResult{ID: "hold-alpha", AccountID: input.AccountID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, AmountCents: input.AmountCents, ActivationAmountCents: input.ActivationAmountCents, RemainingCents: input.AmountCents, Wallet: clients.Wallet{AccountID: input.AccountID, BalanceCents: 20000, FrozenCents: input.AmountCents, AvailableCents: 20000 - input.AmountCents}}, nil
}

func (f *fakeLedgerClient) ActivateHold(_ context.Context, input clients.HoldActivationInput, key string) (clients.HoldActivationResult, error) {
	*f.calls = append(*f.calls, "ledger.activate")
	f.activationKeys = append(f.activationKeys, key)
	return clients.HoldActivationResult{HoldResult: clients.HoldResult{ID: input.HoldID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, RemainingCents: 16800, Status: "active", Wallet: clients.Wallet{AccountID: input.AccountID, BalanceCents: 19900, FrozenCents: 16800, AvailableCents: 3100}}, ActivationLedgerEntryID: "ledger-activation", ActivationWalletTransactionID: "wallet-activation"}, nil
}

func (f *fakeLedgerClient) ReleaseHold(ctx context.Context, input clients.HoldReleaseInput, idempotencyKey string) (clients.HoldReleaseResult, error) {
	*f.calls = append(*f.calls, "ledger.release")
	return clients.HoldReleaseResult{ID: "release-alpha", AccountID: input.AccountID, AmountCents: 16800, Status: "released", LedgerEntryID: "ledger-release", WalletTransactionID: "wallet-release", Wallet: clients.Wallet{AccountID: input.AccountID, BalanceCents: 20000, AvailableCents: 20000}}, nil
}

func (f *fakeLedgerClient) RecordReceipt(ctx context.Context, input clients.ReceiptInput, idempotencyKey string) (clients.Receipt, error) {
	*f.calls = append(*f.calls, "ledger.receipt")
	f.receipts = append(f.receipts, input)
	return clients.Receipt{ReceiptID: "receipt-alpha", WorkspaceID: input.WorkspaceID, ProjectID: input.ProjectID, TaskID: input.TaskID, RequestID: input.RequestID, ApprovalID: input.ApprovalID, JobID: input.JobID, ContinuationID: "continuation-alpha"}, nil
}

func (f *fakeLedgerClient) Receipt(_ context.Context, receiptID string) (clients.Receipt, error) {
	*f.calls = append(*f.calls, "ledger.receipt-get")
	if len(f.receipts) == 0 {
		return clients.Receipt{ReceiptID: receiptID}, nil
	}
	input := f.receipts[len(f.receipts)-1]
	return clients.Receipt{ReceiptID: receiptID, Status: input.Status, Execution: input.Execution, ContinuationID: "continuation-alpha"}, nil
}

func (f *fakeLedgerClient) Review(_ context.Context, reviewID string) (clients.Review, error) {
	*f.calls = append(*f.calls, "ledger.review")
	return f.reviews[reviewID], nil
}

func (f *fakeLedgerClient) Artifact(_ context.Context, artifactID string) (clients.Artifact, error) {
	*f.calls = append(*f.calls, "ledger.artifact")
	return f.artifacts[artifactID], nil
}

func (f *fakeLedgerClient) Continuation(_ context.Context, receiptID string) (map[string]any, error) {
	*f.calls = append(*f.calls, "ledger.continuation")
	return map[string]any{"receiptId": receiptID, "continuationId": "continuation-alpha"}, nil
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
	calls   *[]string
	job     clients.Job
	runtime clients.WorkspaceRuntime
}

type runningComputeFabricClient struct{ fakeFabricClient }

type incompleteComputeFabricClient struct{ fakeFabricClient }

type readyStorageWithoutIdentityFabricClient struct{ fakeFabricClient }

type failingComputeCreateFabricClient struct{ fakeFabricClient }

type failingStorageCreateFabricClient struct{ fakeFabricClient }

type readyStorageFabricClient struct{ fakeFabricClient }

type failingStorageDestroyFabricClient struct{ readyStorageFabricClient }

type failingReleaseLedgerClient struct{ fakeLedgerClient }

type failingActivationLedgerClient struct{ fakeLedgerClient }

func (f *failingReleaseLedgerClient) ReleaseHold(_ context.Context, _ clients.HoldReleaseInput, _ string) (clients.HoldReleaseResult, error) {
	*f.calls = append(*f.calls, "ledger.release")
	return clients.HoldReleaseResult{}, errors.New("ledger release failed")
}

func (f *failingActivationLedgerClient) ActivateHold(_ context.Context, _ clients.HoldActivationInput, _ string) (clients.HoldActivationResult, error) {
	*f.calls = append(*f.calls, "ledger.activate")
	return clients.HoldActivationResult{}, errors.New("ledger activation failed")
}

func (f *runningComputeFabricClient) SyncComputeAllocation(_ context.Context, id string) (clients.ComputeAllocation, error) {
	*f.calls = append(*f.calls, "fabric.compute-sync")
	return clients.ComputeAllocation{ID: id, Status: "running", ProviderRequestID: "pool-request-alpha", MachineName: "machine-alpha", InstanceID: "ins-alpha", NodeName: "node-alpha"}, nil
}

func (f *incompleteComputeFabricClient) SyncComputeAllocation(_ context.Context, id string) (clients.ComputeAllocation, error) {
	*f.calls = append(*f.calls, "fabric.compute-sync")
	return clients.ComputeAllocation{ID: id, Status: "running", ProviderRequestID: "pool-request-alpha"}, nil
}

func (f *readyStorageWithoutIdentityFabricClient) SyncStorageVolume(_ context.Context, id string) (clients.StorageVolume, error) {
	*f.calls = append(*f.calls, "fabric.storage-sync")
	return clients.StorageVolume{ID: id, Status: "ready", ProviderRequestID: "local-request-only"}, nil
}

func (f *failingComputeCreateFabricClient) CreateComputeAllocation(context.Context, clients.ComputeAllocationInput, string) (clients.ComputeAllocation, error) {
	*f.calls = append(*f.calls, "fabric.compute")
	return clients.ComputeAllocation{}, errors.New("tencent create failed")
}

func (f *failingStorageCreateFabricClient) CreateStorageVolume(context.Context, clients.StorageVolumeInput, string) (clients.StorageVolume, error) {
	*f.calls = append(*f.calls, "fabric.storage")
	return clients.StorageVolume{}, errors.New("tencent storage create failed")
}

func (f *readyStorageFabricClient) CreateStorageVolume(_ context.Context, input clients.StorageVolumeInput, _ string) (clients.StorageVolume, error) {
	*f.calls = append(*f.calls, "fabric.storage")
	return clients.StorageVolume{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", ProviderResourceID: "pvc/storage-alpha"}, nil
}

func (f *failingStorageDestroyFabricClient) DestroyStorageVolume(_ context.Context, _ string, _ string) (clients.StorageVolume, error) {
	*f.calls = append(*f.calls, "fabric.storage-destroy")
	return clients.StorageVolume{}, errors.New("provider cleanup failed")
}

func (f *fakeFabricClient) Catalog(ctx context.Context) (clients.FabricCatalog, error) {
	*f.calls = append(*f.calls, "fabric.catalog")
	return clients.FabricCatalog{}, nil
}

func (f *fakeFabricClient) CreateComputeAllocation(ctx context.Context, input clients.ComputeAllocationInput, idempotencyKey string) (clients.ComputeAllocation, error) {
	*f.calls = append(*f.calls, "fabric.compute")
	return clients.ComputeAllocation{ID: input.ID, AccountID: input.AccountID, PackageID: input.PackageID, ProviderRequestID: "compute-request-alpha"}, nil
}

func (f *fakeFabricClient) GetComputeAllocation(ctx context.Context, id string) (clients.ComputeAllocation, error) {
	*f.calls = append(*f.calls, "fabric.compute-get")
	return clients.ComputeAllocation{ID: id, ProviderRequestID: "compute-request-alpha"}, nil
}

func (f *fakeFabricClient) SyncComputeAllocation(ctx context.Context, id string) (clients.ComputeAllocation, error) {
	*f.calls = append(*f.calls, "fabric.compute-sync")
	return clients.ComputeAllocation{ID: id, Status: "external_deleted", ProviderRequestID: "sync-request-alpha"}, nil
}

func (f *fakeFabricClient) DestroyComputeAllocation(ctx context.Context, id string, idempotencyKey string) (clients.ComputeAllocation, error) {
	*f.calls = append(*f.calls, "fabric.compute-destroy")
	return clients.ComputeAllocation{ID: id, Status: "destroyed", ProviderRequestID: "compute-destroy-request-alpha"}, nil
}

func (f *fakeFabricClient) CreateStorageVolume(ctx context.Context, input clients.StorageVolumeInput, idempotencyKey string) (clients.StorageVolume, error) {
	*f.calls = append(*f.calls, "fabric.storage")
	return clients.StorageVolume{ID: input.ID, AccountID: input.AccountID, SizeGB: input.SizeGB, ProviderRequestID: "volume-request-alpha"}, nil
}

func (f *fakeFabricClient) SyncStorageVolume(ctx context.Context, id string) (clients.StorageVolume, error) {
	*f.calls = append(*f.calls, "fabric.storage-sync")
	return clients.StorageVolume{ID: id, Status: "external_deleted", ProviderRequestID: "sync-storage-request-alpha"}, nil
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
	if f.runtime.ID != "" {
		return f.runtime, nil
	}
	return clients.WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: input.WorkspaceID, URL: "https://workspace.medopl.cn/w/" + input.WorkspaceID + "/", Status: "running", ServiceName: "opl-compute-alpha", Access: clients.WorkspaceRuntimeAccess{Username: "admin", Password: "runtime-password-alpha", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-compute-alpha-env"}, Ready: true}, nil
}

func (f *fakeFabricClient) DestroyWorkspaceRuntime(_ context.Context, workspaceID, _ string) (clients.WorkspaceRuntime, error) {
	*f.calls = append(*f.calls, "fabric.runtime-destroy")
	return clients.WorkspaceRuntime{WorkspaceID: workspaceID, Status: "destroyed"}, nil
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

func (f *fakeFabricClient) CreateJob(ctx context.Context, input clients.JobInput, idempotencyKey string) (clients.Job, error) {
	*f.calls = append(*f.calls, "fabric.job")
	return clients.Job{JobID: "job-alpha", OrganizationID: input.OrganizationID, WorkspaceID: input.WorkspaceID, ProjectID: input.ProjectID, TaskID: input.TaskID, RequestID: input.RequestID, ApprovalID: input.ApprovalID, EnvironmentRef: input.EnvironmentRef, Status: "queued"}, nil
}

func (f *fakeFabricClient) GetJob(ctx context.Context, jobID string) (clients.Job, error) {
	*f.calls = append(*f.calls, "fabric.job-get")
	if f.job.JobID != "" {
		return f.job, nil
	}
	return clients.Job{JobID: jobID, Status: "queued"}, nil
}

func (f *fakeFabricClient) CancelJob(ctx context.Context, jobID string, idempotencyKey string) (clients.Job, error) {
	*f.calls = append(*f.calls, "fabric.job-cancel")
	return clients.Job{JobID: jobID, Status: "cancelled"}, nil
}
