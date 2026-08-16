package fabric

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type normalLaunchComputeProvider struct {
	testProvider
	mu                         sync.Mutex
	prepareCalls               int
	createCalls                int
	readbackCalls              int
	discoveryCalls             int
	proofCalls                 int
	cvmClaimCalls              int
	nodeClaimCalls             int
	legacyClaimCalls           int
	created                    ComputeAllocation
	createResultErr            error
	cvmClaimResponseLost       bool
	nodeClaimResponseLost      bool
	nodeClaimLeavesUnallocated bool
	failProofAfterNode         bool
	cvmOwned                   bool
	nodeOwned                  bool
	createGate                 *normalLaunchProviderWriteGate
	cvmClaimGate               *normalLaunchProviderWriteGate
	nodeClaimGate              *normalLaunchProviderWriteGate
}

func (p *normalLaunchComputeProvider) PrepareComputeAllocation(ctx context.Context, input ComputeAllocationInput) (ComputeAllocationPreparation, error) {
	p.mu.Lock()
	p.prepareCalls++
	p.mu.Unlock()
	return p.testProvider.PrepareComputeAllocation(ctx, input)
}

type normalLaunchProviderWriteGate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *normalLaunchProviderWriteGate) afterWrite() {
	if g == nil {
		return
	}
	g.once.Do(func() { close(g.entered) })
	<-g.release
}

func (p *normalLaunchComputeProvider) CreateComputeAllocation(_ context.Context, input ComputeAllocationExecution) (ComputeAllocation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.createCalls++
	p.created = readyComputeAllocationFor(input, "machine-normal-launch")
	gate := p.createGate
	p.mu.Unlock()
	gate.afterWrite()
	p.mu.Lock()
	// Simulate Tencent accepting the scale while the response is lost.
	return ComputeAllocation{Status: "provisioning", ProviderRequestID: "req-scale-response-lost"}, p.createResultErr
}

func (p *normalLaunchComputeProvider) ReadComputeAllocation(_ context.Context, _ ComputeAllocation) (ComputeAllocation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.readbackCalls++
	return ComputeAllocation{}, errors.New("identity-based sync cannot recover a lost scale response")
}

func (p *normalLaunchComputeProvider) DiscoverComputeAllocation(_ context.Context, allocation ComputeAllocation, plan ComputeAllocationPreparation) (ComputeAllocation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.discoveryCalls++
	if plan.NodePoolID == "" || plan.TargetReplicas != plan.BaselineReplicas+1 || len(plan.BeforeMachineNames) != int(plan.BaselineReplicas) {
		return allocation, errors.New("persisted allocation plan required")
	}
	return p.created, nil

}

func (p *normalLaunchComputeProvider) TagComputeMachine(_ context.Context, _ ProviderMachine, _ MachineOwnership) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.legacyClaimCalls++
	return errors.New("normal launch must use split claim stages")
}

func (p *normalLaunchComputeProvider) TagComputeMachineCVM(_ context.Context, _ ProviderMachine, _ MachineOwnership) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cvmClaimCalls++
	p.cvmOwned = true
	gate := p.cvmClaimGate
	p.mu.Unlock()
	gate.afterWrite()
	p.mu.Lock()
	if p.cvmClaimResponseLost {
		return errors.New("CVM ownership response lost")
	}
	return nil
}

func (p *normalLaunchComputeProvider) ClaimComputeNode(_ context.Context, _ ComputeAllocation, _ MachineOwnership) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nodeClaimCalls++
	if !p.nodeClaimLeavesUnallocated {
		p.nodeOwned = true
	}
	gate := p.nodeClaimGate
	p.mu.Unlock()
	gate.afterWrite()
	p.mu.Lock()
	if p.nodeClaimResponseLost {
		// Tencent's Node claim performs its bounded readback internally. A lost
		// response plus unavailable readback is therefore consumed in this call.
		p.failProofAfterNode = false
		return errors.New("Node patch response lost")
	}
	return nil
}

func (p *normalLaunchComputeProvider) ProveComputeClaimRecovery(_ context.Context, allocation ComputeAllocation, _ ComputeAllocationPreparation, _ MachineOwnership) (ComputeClaimProviderProof, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.proofCalls++
	if p.nodeOwned && p.failProofAfterNode {
		p.failProofAfterNode = false
		return ComputeClaimProviderProof{Reason: "provider_describe"}, errors.New("node readback unavailable")
	}
	proof := ComputeClaimProviderProof{
		Status: "proven", MachineName: allocation.MachineName, NodeName: allocation.NodeName,
		CVMInstanceID: firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID), PrivateIP: allocation.PrivateIP,
		InstanceType: allocation.InstanceType, Zone: allocation.Zone, ChargeType: allocation.ChargeType,
		PeriodMonths: 1, RenewFlag: allocation.RenewFlag, Deadline: allocation.Deadline,
		CVMOwnershipState: "recoverable", NodeOwnershipState: "unallocated",
	}
	if p.cvmOwned {
		proof.CVMOwnershipState = "target_owned"
	}
	if p.nodeOwned {
		proof.NodeOwnershipState = "target_owned"
	}
	return proof, nil
}

func (p *normalLaunchComputeProvider) automaticContinuationCounts() (prepare, create, proof, cvmClaim, nodeClaim int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.prepareCalls, p.createCalls, p.proofCalls, p.cvmClaimCalls, p.nodeClaimCalls
}

func seedNormalWorkspaceComputeClaimPending(t *testing.T, store OperationStore, provider *normalLaunchComputeProvider, suffix string) (ComputeAllocationInput, ComputeAllocation) {
	t.Helper()
	launchOperationID := "workspace-launch-" + suffix
	input := ComputeAllocationInput{
		AccountID: "acct-" + suffix, WorkspaceID: "workspace-" + suffix, PackageID: "basic",
		NodePoolID: "np-basic", IdempotencyKey: launchOperationID + ":compute",
	}
	input.ID = "ca_" + stableSuffix("create_compute_allocation", input.IdempotencyKey)[:18]
	plan := ComputeAllocationPreparation{
		PoolID: "pool-basic-2c4g", PackageID: input.PackageID, NodePoolID: input.NodePoolID, InstanceType: "SA5.MEDIUM4",
		MaxReplicas: 20, BaselineReplicas: 1, TargetReplicas: 2, BeforeMachineNames: []string{"machine-existing"},
	}
	seededAllocation := ComputeAllocation{
		ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID,
		NodePoolID: input.NodePoolID, Status: "compute_claim_pending", Provider: "tencent-tke", CreatedAt: time.Now().UTC(),
	}
	allocation := mergeComputeAllocation(readyComputeAllocationFor(ComputeAllocationExecution{
		Allocation: seededAllocation,
		Plan:       plan,
	}, "machine-"+suffix), seededAllocation, plan)
	allocation.Status = "compute_claim_pending"
	allocation.ProviderData["machineName"] = allocation.MachineName
	allocation.ProviderData["periodMonths"] = "1"
	operation := newOperation(
		"create_compute_allocation", "compute_allocation", allocation.ID, allocation.AccountID, allocation.WorkspaceID,
		input.IdempotencyKey, hashInput(input), allocation.CreatedAt,
	)
	operation.ID = "fop_compute_claim_" + stableSuffix("create_compute_allocation", input.IdempotencyKey)
	operation.Status = "claim_pending"
	operation.ErrorCode = "compute_claim_cvm_readback_mismatch"
	operation.ProviderRequestID = allocation.ProviderRequestID
	operation.CreatedAt = allocation.CreatedAt
	operation.ComputePoolKey = input.NodePoolID
	operation.RedactedProviderPayload = computeAllocationOperationPayload(allocation, plan)
	operation.RedactedProviderPayload = withNormalLaunchStageBudget(operation.RedactedProviderPayload, "compute_create", confirmedNormalLaunchMutationBudget())
	operation.RedactedProviderPayload = withNormalLaunchStageBudget(operation.RedactedProviderPayload, "compute_claim_cvm", reservedNormalLaunchMutationBudget())
	launchBinding := WorkspaceLaunchStageBinding{
		SchemaVersion: 1, LaunchOperationID: launchOperationID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID,
		Stage: "ensure_compute_allocation", Action: "ensure_compute_allocation", FabricOperationID: operation.ID,
		IdempotencyKey: input.IdempotencyKey, RequestHash: operation.RequestHash,
	}
	if err := bindLaunchStageOperation(&operation, &launchBinding); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	ownership := MachineOwnership{
		ID: "owner_" + stableSuffix(allocation.ID, allocation.MachineName)[:16], ResourceID: allocation.ID,
		AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID, PackageID: allocation.PackageID,
		NodePoolID: allocation.NodePoolID, MachineID: allocation.MachineName, InstanceID: allocation.InstanceID,
		NodeName: allocation.NodeName, Status: "quarantined", ProviderRequestID: allocation.ProviderRequestID, ClaimedAt: allocation.CreatedAt,
	}
	if _, created, err := store.ClaimMachine(context.Background(), ownership); err != nil || !created {
		t.Fatalf("seed machine ownership created=%v err=%v", created, err)
	}
	provider.cvmOwned = true
	return input, allocation
}

func TestNormalWorkspaceClaimPendingReplayWaitsForControlPlaneDecision(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := &normalLaunchComputeProvider{}
	input, allocation := seedNormalWorkspaceComputeClaimPending(t, store, provider, "control-plane-decision-required")
	service := NewServiceWithOperationStore(provider, store)

	replayed, err := service.CreateComputeAllocation(context.Background(), input)
	if err != nil || replayed.ID != allocation.ID {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	waitForComputeReconcileIdle(t, service, allocation.ID)
	prepare, create, proof, cvmClaim, nodeClaim := provider.automaticContinuationCounts()
	if prepare != 0 || create != 0 || proof != 0 || cvmClaim != 0 || nodeClaim != 0 {
		t.Fatalf("claim_pending replay crossed Control Plane authorization: prepare=%d create=%d proof=%d cvmClaim=%d nodeClaim=%d", prepare, create, proof, cvmClaim, nodeClaim)
	}
	operations, listErr := store.List(context.Background())
	if listErr != nil || len(operations) != 1 || operations[0].Status != "claim_pending" {
		t.Fatalf("claim_pending replay changed operation: operations=%#v err=%v", operations, listErr)
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
