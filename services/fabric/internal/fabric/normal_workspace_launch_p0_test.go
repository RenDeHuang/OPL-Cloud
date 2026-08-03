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

func newNormalLaunchProviderWriteGate() *normalLaunchProviderWriteGate {
	return &normalLaunchProviderWriteGate{entered: make(chan struct{}), release: make(chan struct{})}
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

func (p *normalLaunchComputeProvider) counts() (create, readback, discovery, cvmClaim, nodeClaim, legacyClaim int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.createCalls, p.readbackCalls, p.discoveryCalls, p.cvmClaimCalls, p.nodeClaimCalls, p.legacyClaimCalls
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

func TestNormalWorkspacePersistedClaimPendingReplayContinuesTargetOwnedCVMWithoutRetagging(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := &normalLaunchComputeProvider{}
	input, allocation := seedNormalWorkspaceComputeClaimPending(t, store, provider, "automatic-continuation")
	service := NewServiceWithOperationStore(provider, store)
	configureFastComputeAllocationPolling(service, 100*time.Millisecond)

	replayed, err := service.CreateComputeAllocation(context.Background(), input)
	if err != nil || replayed.ID != allocation.ID {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", allocation.ID, "succeeded")
	prepare, create, proof, cvmClaim, nodeClaim := provider.automaticContinuationCounts()
	if prepare != 0 || create != 0 || proof != 1 || cvmClaim != 0 || nodeClaim != 1 {
		t.Fatalf("automatic continuation calls prepare=%d create=%d proof=%d cvmClaim=%d nodeClaim=%d, want 0/0/1/0/1", prepare, create, proof, cvmClaim, nodeClaim)
	}
	operations, operationErr := store.List(context.Background())
	var persisted ComputeAllocation
	if operationErr != nil || len(operations) != 1 || operations[0].Status != "succeeded" || !decodeOperationResource(operations[0], &persisted) || persisted.Status != "running" {
		t.Fatalf("automatic continuation operations=%#v persisted=%#v err=%v", operations, persisted, operationErr)
	}
	for _, stage := range []string{"compute_claim_cvm", "compute_claim_node"} {
		budget, present, valid := normalLaunchStageBudget(operations[0].RedactedProviderPayload, stage)
		if !present || !valid || budget != confirmedNormalLaunchMutationBudget() {
			t.Fatalf("automatic continuation stage %s budget=%#v present=%v valid=%v", stage, budget, present, valid)
		}
	}
	ownership, ownershipErr := store.MachineOwnership(context.Background(), allocation.ID)
	if ownershipErr != nil || ownership.Status != "active" {
		t.Fatalf("ownership=%#v err=%v", ownership, ownershipErr)
	}
}

func TestNormalWorkspacePersistedClaimPendingReplayConvergesTargetOwnedNodeWithoutPatch(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := &normalLaunchComputeProvider{nodeOwned: true}
	input, allocation := seedNormalWorkspaceComputeClaimPending(t, store, provider, "automatic-target-owned")
	service := NewServiceWithOperationStore(provider, store)

	if _, err := service.CreateComputeAllocation(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", allocation.ID, "succeeded")
	prepare, create, proof, cvmClaim, nodeClaim := provider.automaticContinuationCounts()
	if prepare != 0 || create != 0 || proof != 1 || cvmClaim != 0 || nodeClaim != 0 {
		t.Fatalf("target-owned continuation calls prepare=%d create=%d proof=%d cvmClaim=%d nodeClaim=%d, want 0/0/1/0/0", prepare, create, proof, cvmClaim, nodeClaim)
	}
	ownership, ownershipErr := store.MachineOwnership(context.Background(), allocation.ID)
	if ownershipErr != nil || ownership.Status != "active" {
		t.Fatalf("target-owned ownership=%#v err=%v", ownership, ownershipErr)
	}
}

func TestNormalWorkspacePersistedClaimPendingReplayAfterOwnershipActivationConvergesByReadbackOnly(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := &normalLaunchComputeProvider{nodeOwned: true}
	input, allocation := seedNormalWorkspaceComputeClaimPending(t, store, provider, "automatic-active-ownership-crash")
	ownership, err := store.MachineOwnership(context.Background(), allocation.ID)
	if err != nil {
		t.Fatal(err)
	}
	ownership.Status, ownership.ReleasedAt = "active", nil
	if err := store.ActivateComputeClaimRecoveryOwnership(context.Background(), ownership); err != nil {
		t.Fatal(err)
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].Status != "claim_pending" {
		t.Fatalf("crash-window operations=%#v err=%v", operations, err)
	}
	operationID := operations[0].ID

	service := NewServiceWithOperationStore(provider, store)
	if _, err := service.CreateComputeAllocation(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", allocation.ID, "succeeded")
	prepare, create, proof, cvmClaim, nodeClaim := provider.automaticContinuationCounts()
	if prepare != 0 || create != 0 || proof != 1 || cvmClaim != 0 || nodeClaim != 0 {
		t.Fatalf("active ownership continuation calls prepare=%d create=%d proof=%d cvmClaim=%d nodeClaim=%d, want 0/0/1/0/0", prepare, create, proof, cvmClaim, nodeClaim)
	}
	operations, err = store.List(context.Background())
	var persisted ComputeAllocation
	if err != nil || len(operations) != 1 || operations[0].ID != operationID || operations[0].Status != "succeeded" ||
		!decodeOperationResource(operations[0], &persisted) || persisted.Status != "running" {
		t.Fatalf("active ownership operations=%#v persisted=%#v err=%v", operations, persisted, err)
	}
	ownership, err = store.MachineOwnership(context.Background(), allocation.ID)
	if err != nil || ownership.Status != "active" {
		t.Fatalf("active ownership=%#v err=%v", ownership, err)
	}
}

func TestNormalWorkspacePersistedClaimPendingConcurrentReplayHasOneNodePatchWinner(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := &normalLaunchComputeProvider{nodeClaimGate: newNormalLaunchProviderWriteGate()}
	input, allocation := seedNormalWorkspaceComputeClaimPending(t, store, provider, "automatic-concurrent")
	first := NewServiceWithOperationStore(provider, store)
	second := NewServiceWithOperationStore(provider, store)

	if _, err := first.CreateComputeAllocation(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.nodeClaimGate.entered:
	case <-time.After(time.Second):
		t.Fatal("continuation winner did not reach Node patch")
	}
	if _, err := second.CreateComputeAllocation(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, second, "create_compute_allocation", "compute_allocation", allocation.ID, "succeeded")
	close(provider.nodeClaimGate.release)
	waitForComputeReconcileIdle(t, first, allocation.ID)
	prepare, create, proof, cvmClaim, nodeClaim := provider.automaticContinuationCounts()
	if prepare != 0 || create != 0 || proof < 2 || cvmClaim != 0 || nodeClaim != 1 {
		t.Fatalf("concurrent continuation calls prepare=%d create=%d proof=%d cvmClaim=%d nodeClaim=%d, want 0/0/>=2/0/1", prepare, create, proof, cvmClaim, nodeClaim)
	}
}

func TestNormalWorkspacePersistedClaimPendingRestartAfterLostNodeResponseUsesReadbackOnly(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := &normalLaunchComputeProvider{nodeClaimResponseLost: true, failProofAfterNode: true}
	input, allocation := seedNormalWorkspaceComputeClaimPending(t, store, provider, "automatic-lost-node-response")
	first := NewServiceWithOperationStore(provider, store)

	if _, err := first.CreateComputeAllocation(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	waitForComputeReconcileIdle(t, first, allocation.ID)
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].Status != "claim_pending" {
		t.Fatalf("after lost response operations=%#v err=%v", operations, err)
	}
	nodeBudget, present, valid := normalLaunchStageBudget(operations[0].RedactedProviderPayload, "compute_claim_node")
	if !present || !valid || nodeBudget != reservedNormalLaunchMutationBudget() {
		t.Fatalf("persisted Node reservation=%#v present=%v valid=%v", nodeBudget, present, valid)
	}

	provider.nodeClaimResponseLost = false
	restarted := NewServiceWithOperationStore(provider, store)
	if _, err := restarted.CreateComputeAllocation(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, restarted, "create_compute_allocation", "compute_allocation", allocation.ID, "succeeded")
	prepare, create, proof, cvmClaim, nodeClaim := provider.automaticContinuationCounts()
	if prepare != 0 || create != 0 || proof != 2 || cvmClaim != 0 || nodeClaim != 1 {
		t.Fatalf("restart continuation calls prepare=%d create=%d proof=%d cvmClaim=%d nodeClaim=%d, want 0/0/2/0/1", prepare, create, proof, cvmClaim, nodeClaim)
	}
}

func TestNormalWorkspacePersistedClaimPendingRejectsPersistedIdentityDriftBeforeProviderRead(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		drift func(*ComputeAllocation, *ComputeAllocationPreparation)
	}{
		{name: "allocation", drift: func(allocation *ComputeAllocation, _ *ComputeAllocationPreparation) {
			allocation.InstanceType = "S6.MEDIUM4"
		}},
		{name: "plan", drift: func(_ *ComputeAllocation, plan *ComputeAllocationPreparation) {
			plan.TargetReplicas++
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := NewMemoryOperationStore()
			provider := &normalLaunchComputeProvider{}
			input, allocation := seedNormalWorkspaceComputeClaimPending(t, store, provider, "automatic-drift-"+testCase.name)
			operations, err := store.List(context.Background())
			if err != nil || len(operations) != 1 {
				t.Fatalf("seeded operations=%#v err=%v", operations, err)
			}
			operation := operations[0]
			plan, hasPlan := decodeComputeAllocationPlan(operation)
			if !hasPlan {
				t.Fatal("seeded operation is missing allocation plan")
			}
			testCase.drift(&allocation, &plan)
			binding, bindingOK := automaticComputeClaimRecoveryBinding(operation, allocation, plan)
			if !bindingOK {
				t.Fatal("drift fixture must retain a syntactically valid recovery binding")
			}
			drifted := operation
			drifted.RedactedProviderPayload = preserveNormalLaunchMutationBudget(computeAllocationOperationPayload(allocation, plan), operation.RedactedProviderPayload)
			drifted.RedactedProviderPayload = withComputeClaimRecoveryBinding(drifted.RedactedProviderPayload, binding)
			if err := store.SaveComputeClaimRecovery(context.Background(), operation, drifted); err != nil {
				t.Fatal(err)
			}

			service := NewServiceWithOperationStore(provider, store)
			if _, err := service.CreateComputeAllocation(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			waitForComputeReconcileIdle(t, service, allocation.ID)
			prepare, create, proof, cvmClaim, nodeClaim := provider.automaticContinuationCounts()
			if prepare != 0 || create != 0 || proof != 0 || cvmClaim != 0 || nodeClaim != 0 {
				t.Fatalf("identity drift calls prepare=%d create=%d proof=%d cvmClaim=%d nodeClaim=%d, want all zero", prepare, create, proof, cvmClaim, nodeClaim)
			}
			operations, err = store.List(context.Background())
			if err != nil || len(operations) != 1 || operations[0].Status != "claim_pending" {
				t.Fatalf("identity drift operations=%#v err=%v", operations, err)
			}
		})
	}
}

func TestNormalWorkspacePersistedClaimPendingRejectsManualRecoveryLedgerBeforeProviderRead(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := &normalLaunchComputeProvider{}
	input, allocation := seedNormalWorkspaceComputeClaimPending(t, store, provider, "automatic-manual-recovery-ledger")
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("seeded operations=%#v err=%v", operations, err)
	}
	operation := operations[0]
	plan, hasPlan := decodeComputeAllocationPlan(operation)
	if !hasPlan {
		t.Fatal("seeded operation is missing allocation plan")
	}
	binding, bindingOK := automaticComputeClaimRecoveryBinding(operation, allocation, plan)
	if !bindingOK {
		t.Fatal("seeded operation is missing automatic recovery binding")
	}
	manual := operation
	manual.RedactedProviderPayload = withComputeClaimRecoveryBinding(manual.RedactedProviderPayload, binding)
	manual.RedactedProviderPayload = withComputeClaimRecoveryMutation(manual.RedactedProviderPayload, reservedComputeClaimRecoveryMutation())
	if err := store.SaveComputeClaimRecovery(context.Background(), operation, manual); err != nil {
		t.Fatal(err)
	}

	service := NewServiceWithOperationStore(provider, store)
	if _, err := service.CreateComputeAllocation(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	waitForComputeReconcileIdle(t, service, allocation.ID)
	prepare, create, proof, cvmClaim, nodeClaim := provider.automaticContinuationCounts()
	if prepare != 0 || create != 0 || proof != 0 || cvmClaim != 0 || nodeClaim != 0 {
		t.Fatalf("manual recovery ledger calls prepare=%d create=%d proof=%d cvmClaim=%d nodeClaim=%d, want all zero", prepare, create, proof, cvmClaim, nodeClaim)
	}
	operations, err = store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].Status != "claim_pending" {
		t.Fatalf("manual recovery ledger operations=%#v err=%v", operations, err)
	}
}

func TestNormalWorkspacePersistedClaimPendingTerminalizesUnprovableNodeWithoutReplay(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := &normalLaunchComputeProvider{nodeClaimResponseLost: true, nodeClaimLeavesUnallocated: true}
	input, allocation := seedNormalWorkspaceComputeClaimPending(t, store, provider, "automatic-terminal-node")
	service := NewServiceWithOperationStore(provider, store)

	if _, err := service.CreateComputeAllocation(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	waitForComputeReconcileIdle(t, service, allocation.ID)
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].Status != "claim_pending" {
		t.Fatalf("terminal operations=%#v err=%v", operations, err)
	}
	// The first replay only records the Node reservation.  The second replay
	// is the bounded readback that produces the terminal result.
	if _, err := service.CreateComputeAllocation(context.Background(), input); err != nil {
		t.Fatalf("reservation replay err=%v", err)
	}
	waitForComputeReconcileIdle(t, service, allocation.ID)
	operations, err = store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].Status != "failed" || operations[0].FinishedAt.IsZero() {
		t.Fatalf("terminal operations=%#v err=%v", operations, err)
	}
	terminal, present, valid := decodeComputeClaimTerminalEvidence(operations[0])
	if !present || !valid || terminal.Stage != "compute_claim_node" || terminal.Status != "terminal_unprovable" || terminal.AttemptCount == 0 || terminal.ErrorCode == "" {
		t.Fatalf("terminal evidence=%#v present=%v valid=%v", terminal, present, valid)
	}
	var persisted ComputeAllocation
	if !decodeOperationResource(operations[0], &persisted) || persisted.Status != "quarantined" || persisted.ClaimTerminalEvidence == nil {
		t.Fatalf("terminal allocation=%#v", persisted)
	}
	ownership, err := store.MachineOwnership(context.Background(), allocation.ID)
	if err != nil || ownership.Status != "quarantined" {
		t.Fatalf("terminal ownership=%#v err=%v", ownership, err)
	}
	_, _, proofCalls, _, nodeClaims := provider.automaticContinuationCounts()
	if proofCalls != 2 || nodeClaims != 1 {
		t.Fatalf("initial terminal provider calls proof=%d node=%d", proofCalls, nodeClaims)
	}
	if _, err := service.CreateComputeAllocation(context.Background(), input); !errors.Is(err, ErrComputeOperationFailed) {
		t.Fatalf("terminal replay err=%v, want %v", err, ErrComputeOperationFailed)
	}
	waitForComputeReconcileIdle(t, service, allocation.ID)
	_, _, proofCalls, _, nodeClaims = provider.automaticContinuationCounts()
	if proofCalls != 2 || nodeClaims != 1 {
		t.Fatalf("terminal replay repeated provider calls proof=%d node=%d", proofCalls, nodeClaims)
	}
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
				createCalls, _, _, _, _, _ := provider.counts()
				if createCalls == 1 {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if createCalls, _, _, _, _, _ := provider.counts(); createCalls != 1 {
				t.Fatalf("initial provider create calls=%d, want 1", createCalls)
			}
			waitForComputeReconcileIdle(t, first, allocation.ID)

			restarted := NewServiceWithOperationStore(provider, store)
			configureFastComputeAllocationPolling(restarted, 100*time.Millisecond)
			if _, err := restarted.CreateComputeAllocation(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			waitForOperation(t, restarted, "create_compute_allocation", "compute_allocation", allocation.ID, "succeeded")
			createCalls, readbackCalls, discoveryCalls, cvmClaimCalls, nodeClaimCalls, legacyClaimCalls := provider.counts()
			if createCalls != 1 || readbackCalls != 0 || discoveryCalls == 0 || cvmClaimCalls != 1 || nodeClaimCalls != 1 || legacyClaimCalls != 0 {
				t.Fatalf("provider calls create=%d readback=%d discovery=%d cvmClaim=%d nodeClaim=%d legacyClaim=%d, want 1/0/>0/1/1/0", createCalls, readbackCalls, discoveryCalls, cvmClaimCalls, nodeClaimCalls, legacyClaimCalls)
			}
			assertNormalLaunchStageBudget(t, store, "create_compute_allocation", "compute_create", 1, 1, 0)
			assertNormalLaunchStageBudget(t, store, "create_compute_allocation", "compute_claim_cvm", 1, 1, 0)
			assertNormalLaunchStageBudget(t, store, "create_compute_allocation", "compute_claim_node", 1, 1, 0)
		})
	}
}

func TestNormalWorkspaceClaimStagesConvergeLostResponsesWithoutRepeatingWrites(t *testing.T) {
	for _, lostStage := range []string{"cvm", "node"} {
		t.Run(lostStage, func(t *testing.T) {
			store := NewMemoryOperationStore()
			provider := &normalLaunchComputeProvider{createResultErr: ErrComputeAllocationPending}
			provider.cvmClaimResponseLost = lostStage == "cvm"
			provider.nodeClaimResponseLost = lostStage == "node"
			input := ComputeAllocationInput{
				AccountID: "acct-" + lostStage, WorkspaceID: "workspace-" + lostStage, PackageID: "basic",
				NodePoolID: "np-basic", IdempotencyKey: "workspace-launch-" + lostStage + ":compute",
			}
			first := NewServiceWithOperationStore(provider, store)
			configureFastComputeAllocationPolling(first, time.Millisecond)
			allocation, err := first.CreateComputeAllocation(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			waitForComputeReconcileIdle(t, first, allocation.ID)

			provider.cvmClaimResponseLost = false
			provider.nodeClaimResponseLost = false
			restarted := NewServiceWithOperationStore(provider, store)
			configureFastComputeAllocationPolling(restarted, time.Millisecond)
			if _, err := restarted.CreateComputeAllocation(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			waitForOperation(t, restarted, "create_compute_allocation", "compute_allocation", allocation.ID, "succeeded")

			_, _, _, cvmCalls, nodeCalls, legacyCalls := provider.counts()
			if cvmCalls != 1 || nodeCalls != 1 || legacyCalls != 0 {
				t.Fatalf("claim writes cvm=%d node=%d legacy=%d, want 1/1/0", cvmCalls, nodeCalls, legacyCalls)
			}
			assertNormalLaunchStageBudget(t, store, "create_compute_allocation", "compute_claim_cvm", 1, 1, 0)
			assertNormalLaunchStageBudget(t, store, "create_compute_allocation", "compute_claim_node", 1, 1, 0)
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
	cbsCreateGate        *normalLaunchProviderWriteGate
}

func (p *normalLaunchStorageProvider) CreateCBSVolume(_ context.Context, input StorageVolumeInput) (StorageVolume, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cbsCreateCalls++
	p.created = normalLaunchStorageVolume(input)
	gate := p.cbsCreateGate
	p.mu.Unlock()
	gate.afterWrite()
	p.mu.Lock()
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
