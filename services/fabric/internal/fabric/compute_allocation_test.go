package fabric

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

type allocationScopedProvider struct {
	testProvider
	mu             sync.Mutex
	prepareCalls   int
	executeCalls   int
	preparation    ComputeAllocationPreparation
	executeResults []ComputeAllocation
	executeErrors  []error
	executions     []ComputeAllocationExecution
}

type claimInterruptedAllocationProvider struct {
	allocationScopedProvider
	failAt       string
	tagCalls     int
	syncCalls    int
	storageCalls int
}

func (p *claimInterruptedAllocationProvider) TagComputeMachine(_ context.Context, _ ProviderMachine, _ MachineOwnership) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tagCalls++
	if p.failAt == "tag" {
		return errors.New("transient claim tag failure")
	}
	return nil
}

func (p *claimInterruptedAllocationProvider) SyncComputeAllocation(_ context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.syncCalls++
	if p.failAt == "sync" {
		return allocation, errors.New("transient strict sync failure")
	}
	return allocation, nil
}

func (p *claimInterruptedAllocationProvider) CreateStorageVolume(_ context.Context, _ StorageVolumeInput) (StorageVolume, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.storageCalls++
	return StorageVolume{}, errors.New("storage must not start during compute claim recovery")
}

type serializedPoolProvider struct {
	testProvider
	mu                sync.Mutex
	replicas          map[string]int64
	machines          map[string][]string
	prepareCalls      map[string]int
	executeCalls      map[string]int
	scaleTargets      map[string][]int64
	headWorkspace     string
	headMayComplete   bool
	firstHeadCall     chan struct{}
	firstHeadCallOnce sync.Once
	ambiguousResults  int
}

func newSerializedPoolProvider(headWorkspace string) *serializedPoolProvider {
	return &serializedPoolProvider{
		replicas:      map[string]int64{},
		machines:      map[string][]string{},
		prepareCalls:  map[string]int{},
		executeCalls:  map[string]int{},
		scaleTargets:  map[string][]int64{},
		headWorkspace: headWorkspace,
		firstHeadCall: make(chan struct{}),
	}
}

func (p *serializedPoolProvider) PrepareComputeAllocation(_ context.Context, input ComputeAllocationInput) (ComputeAllocationPreparation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prepareCalls[input.WorkspaceID]++
	current := p.replicas[input.NodePoolID]
	plan := packagePlan(input.PackageID)
	return ComputeAllocationPreparation{
		PoolID: plan.ID, PackageID: input.PackageID, NodePoolID: input.NodePoolID, InstanceType: plan.InstanceType,
		MaxReplicas: 20, BaselineReplicas: current, TargetReplicas: current + 1, BeforeMachineNames: append([]string(nil), p.machines[input.NodePoolID]...),
	}, nil
}

func (p *serializedPoolProvider) CreateComputeAllocation(_ context.Context, input ComputeAllocationExecution) (ComputeAllocation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	workspaceID := input.Allocation.WorkspaceID
	p.executeCalls[workspaceID]++
	current := p.replicas[input.Plan.NodePoolID]
	if current < input.Plan.BaselineReplicas || current > input.Plan.TargetReplicas {
		p.ambiguousResults++
		return input.Allocation, fmt.Errorf("compute_allocation_replica_state_ambiguous")
	}
	if current == input.Plan.BaselineReplicas {
		p.replicas[input.Plan.NodePoolID] = input.Plan.TargetReplicas
		p.scaleTargets[input.Plan.NodePoolID] = append(p.scaleTargets[input.Plan.NodePoolID], input.Plan.TargetReplicas)
		p.machines[input.Plan.NodePoolID] = append(p.machines[input.Plan.NodePoolID], fmt.Sprintf("machine-%s-%d", input.Plan.NodePoolID, input.Plan.TargetReplicas))
	}
	if workspaceID == p.headWorkspace && !p.headMayComplete {
		p.firstHeadCallOnce.Do(func() { close(p.firstHeadCall) })
		return mergeComputeAllocation(ComputeAllocation{Status: "provisioning"}, input.Allocation, input.Plan), ErrComputeAllocationPending
	}
	machineName := fmt.Sprintf("machine-%s-%d", input.Plan.NodePoolID, input.Plan.TargetReplicas)
	return readyComputeAllocationFor(input, machineName), nil
}

func (p *serializedPoolProvider) allowHeadCompletion() {
	p.mu.Lock()
	p.headMayComplete = true
	p.mu.Unlock()
}

func (p *serializedPoolProvider) workspaceCalls(workspaceID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.executeCalls[workspaceID]
}

func (p *serializedPoolProvider) workspacePrepareCalls(workspaceID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.prepareCalls[workspaceID]
}

func (p *serializedPoolProvider) allocationEvidence(nodePoolID string) ([]int64, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int64(nil), p.scaleTargets[nodePoolID]...), p.ambiguousResults
}

func readyComputeAllocationFor(input ComputeAllocationExecution, machineName string) ComputeAllocation {
	instanceID := "ins-" + stableSuffix(input.Allocation.ID, machineName)[:12]
	nodeName := fmt.Sprintf("10.0.%d.%d", len(input.Plan.NodePoolID)%250, input.Plan.TargetReplicas+10)
	return ComputeAllocation{
		Status: "running", Provider: "tencent-tke", ProviderRequestID: "req-" + stableSuffix(input.Allocation.ID)[:12], ProviderResourceID: "machine/" + machineName,
		PoolID: input.Plan.PoolID, NodePoolID: input.Plan.NodePoolID, MachineName: machineName, InstanceID: instanceID, CVMInstanceID: instanceID,
		NodeName: nodeName, PrivateIP: nodeName, InstanceType: input.Plan.InstanceType, Zone: "na-siliconvalley-1", ChargeType: "PREPAID",
		RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-25T00:00:00Z",
		ProviderData: map[string]string{"instanceType": input.Plan.InstanceType, "zone": "na-siliconvalley-1", "chargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-25T00:00:00Z"},
	}
}

func configureFastComputeAllocationPolling(service *Service, window time.Duration) {
	service.computeAllocationPollInterval = time.Millisecond
	service.computeAllocationPollWindow = window
	service.computeAllocationAttemptTimeout = 100 * time.Millisecond
	service.computeAllocationFinalizeTimeout = 100 * time.Millisecond
}

func waitForComputeReconcileIdle(t *testing.T, service *Service, allocationID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		service.mu.Lock()
		busy := service.reconciling[allocationID]
		service.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("compute allocation %s did not leave the local reconciler", allocationID)
}

func waitForStartedComputeOperation(t *testing.T, store OperationStore, resourceID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		operations, err := store.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, operation := range operations {
			if operation.Action == "create_compute_allocation" && operation.ResourceID == resourceID && operation.Status == "started" {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("missing started compute operation %s", resourceID)
}

func TestComputeAllocationSerializesDifferentWorkspacesInOneNodePool(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := newSerializedPoolProvider("workspace-alpha")
	service := NewServiceWithOperationStore(provider, store)
	configureFastComputeAllocationPolling(service, 500*time.Millisecond)
	firstInput := ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "compute-alpha"}
	secondInput := ComputeAllocationInput{AccountID: "acct-beta", WorkspaceID: "workspace-beta", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "compute-beta"}

	first, err := service.CreateComputeAllocation(context.Background(), firstInput)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.firstHeadCall:
	case <-time.After(time.Second):
		t.Fatal("head operation did not reach provider")
	}
	second, err := service.CreateComputeAllocation(context.Background(), secondInput)
	if err != nil {
		t.Fatal(err)
	}
	waitForComputeReconcileIdle(t, service, second.ID)
	waitForStartedComputeOperation(t, store, second.ID)
	if calls := provider.workspaceCalls("workspace-beta"); calls != 0 {
		t.Fatalf("queued workspace reached provider %d times while head was pending", calls)
	}
	if calls := provider.workspacePrepareCalls("workspace-beta"); calls != 0 {
		t.Fatalf("queued workspace prepared provider allocation %d times while head was pending", calls)
	}

	provider.allowHeadCompletion()
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", first.ID, "succeeded")
	if _, err := service.CreateComputeAllocation(context.Background(), secondInput); err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", second.ID, "succeeded")
	firstAllocation, firstOK := service.GetComputeAllocation(context.Background(), first.ID)
	secondAllocation, secondOK := service.GetComputeAllocation(context.Background(), second.ID)
	if !firstOK || !secondOK || firstAllocation.MachineName == secondAllocation.MachineName {
		t.Fatalf("same-pool allocations are not unique: first=%#v second=%#v", firstAllocation, secondAllocation)
	}
	if targets, ambiguous := provider.allocationEvidence("np-basic"); !reflect.DeepEqual(targets, []int64{1, 2}) || ambiguous != 0 {
		t.Fatalf("same-pool scale targets=%v ambiguous=%d", targets, ambiguous)
	}
}

func TestComputeAllocationRestartKeepsPersistedPoolHeadAheadOfLaterWorkspace(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := newSerializedPoolProvider("workspace-alpha")
	firstService := NewServiceWithOperationStore(provider, store)
	configureFastComputeAllocationPolling(firstService, 15*time.Millisecond)
	firstInput := ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "compute-restart-alpha"}
	secondInput := ComputeAllocationInput{AccountID: "acct-beta", WorkspaceID: "workspace-beta", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "compute-restart-beta"}

	first, err := firstService.CreateComputeAllocation(context.Background(), firstInput)
	if err != nil {
		t.Fatal(err)
	}
	<-provider.firstHeadCall
	waitForComputeReconcileIdle(t, firstService, first.ID)
	second, err := firstService.CreateComputeAllocation(context.Background(), secondInput)
	if err != nil {
		t.Fatal(err)
	}
	waitForComputeReconcileIdle(t, firstService, second.ID)

	restarted := NewServiceWithOperationStore(provider, store)
	configureFastComputeAllocationPolling(restarted, 100*time.Millisecond)
	if _, err := restarted.CreateComputeAllocation(context.Background(), secondInput); err != nil {
		t.Fatal(err)
	}
	waitForComputeReconcileIdle(t, restarted, second.ID)
	if calls := provider.workspaceCalls("workspace-beta"); calls != 0 {
		t.Fatalf("restart allowed queued workspace to bypass head: calls=%d", calls)
	}
	if calls := provider.workspacePrepareCalls("workspace-beta"); calls != 0 {
		t.Fatalf("restart allowed queued workspace to prepare before head: calls=%d", calls)
	}
	provider.allowHeadCompletion()
	if _, err := restarted.CreateComputeAllocation(context.Background(), firstInput); err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, restarted, "create_compute_allocation", "compute_allocation", first.ID, "succeeded")
}

type parallelPoolProvider struct {
	testProvider
	entered chan string
	release chan struct{}
}

func (p *parallelPoolProvider) PrepareComputeAllocation(_ context.Context, input ComputeAllocationInput) (ComputeAllocationPreparation, error) {
	plan := packagePlan(input.PackageID)
	return ComputeAllocationPreparation{PoolID: plan.ID, PackageID: input.PackageID, NodePoolID: input.NodePoolID, InstanceType: plan.InstanceType, MaxReplicas: 20, TargetReplicas: 1}, nil
}

func (p *parallelPoolProvider) CreateComputeAllocation(ctx context.Context, input ComputeAllocationExecution) (ComputeAllocation, error) {
	p.entered <- input.Plan.NodePoolID
	select {
	case <-p.release:
		return readyComputeAllocationFor(input, "machine-"+input.Plan.NodePoolID+"-1"), nil
	case <-ctx.Done():
		return input.Allocation, ctx.Err()
	}
}

func TestComputeAllocationAllowsDifferentNodePoolsInParallel(t *testing.T) {
	provider := &parallelPoolProvider{entered: make(chan string, 2), release: make(chan struct{})}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	inputs := []ComputeAllocationInput{
		{AccountID: "acct-alpha", WorkspaceID: "workspace-basic", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "parallel-basic"},
		{AccountID: "acct-beta", WorkspaceID: "workspace-pro", PackageID: "pro", NodePoolID: "np-pro", IdempotencyKey: "parallel-pro"},
	}
	allocations := make([]ComputeAllocation, 0, len(inputs))
	for _, input := range inputs {
		allocation, err := service.CreateComputeAllocation(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		allocations = append(allocations, allocation)
	}
	seen := map[string]bool{}
	for range inputs {
		select {
		case pool := <-provider.entered:
			seen[pool] = true
		case <-time.After(time.Second):
			t.Fatalf("different pools did not enter provider concurrently: %v", seen)
		}
	}
	close(provider.release)
	if !seen["np-basic"] || !seen["np-pro"] {
		t.Fatalf("parallel pools=%v", seen)
	}
	for _, allocation := range allocations {
		waitForOperation(t, service, "create_compute_allocation", "compute_allocation", allocation.ID, "succeeded")
	}
}

func (p *allocationScopedProvider) PrepareComputeAllocation(_ context.Context, _ ComputeAllocationInput) (ComputeAllocationPreparation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prepareCalls++
	return p.preparation, nil
}

func (p *allocationScopedProvider) CreateComputeAllocation(_ context.Context, input ComputeAllocationExecution) (ComputeAllocation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.executions = append(p.executions, input)
	index := p.executeCalls
	p.executeCalls++
	var result ComputeAllocation
	if index < len(p.executeResults) {
		result = p.executeResults[index]
	}
	var err error
	if index < len(p.executeErrors) {
		err = p.executeErrors[index]
	}
	return result, err
}

func TestComputeAllocationPersistsAbsoluteTargetBeforeProviderMutationAndResumesIt(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := &allocationScopedProvider{
		preparation: ComputeAllocationPreparation{
			PoolID: "pool-basic-2c4g", PackageID: "basic", NodePoolID: "np-basic", InstanceType: "SA5.MEDIUM4",
			MaxReplicas: 20, BaselineReplicas: 1, TargetReplicas: 2, BeforeMachineNames: []string{"machine-existing"},
		},
		executeResults: []ComputeAllocation{
			{Status: "provisioning", ProviderRequestID: "req-scale-unknown"},
			readyComputeAllocation("machine-new", "ins-new", "10.0.0.12"),
		},
		executeErrors: []error{ErrComputeAllocationPending, nil},
	}
	input := ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "compute-restart-target"}

	firstService := NewServiceWithOperationStore(provider, store)
	configureFastComputeAllocationPolling(firstService, time.Millisecond)
	first, err := firstService.CreateComputeAllocation(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	waitForComputeProviderCalls(t, provider, 1)

	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("operations=%#v err=%v", operations, err)
	}
	plan, ok := decodeComputeAllocationPlan(operations[0])
	if !ok || plan.MaxReplicas != 20 || plan.BaselineReplicas != 1 || plan.TargetReplicas != 2 || len(plan.BeforeMachineNames) != 1 || plan.BeforeMachineNames[0] != "machine-existing" {
		t.Fatalf("persisted plan=%#v ok=%v", plan, ok)
	}
	waitForComputeReconcileIdle(t, firstService, first.ID)

	restarted := NewServiceWithOperationStore(provider, store)
	configureFastComputeAllocationPolling(restarted, 100*time.Millisecond)
	replayed, err := restarted.CreateComputeAllocation(context.Background(), input)
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	waitForOperation(t, restarted, "create_compute_allocation", "compute_allocation", first.ID, "succeeded")
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.prepareCalls != 1 || provider.executeCalls != 2 {
		t.Fatalf("prepareCalls=%d executeCalls=%d", provider.prepareCalls, provider.executeCalls)
	}
	for _, execution := range provider.executions {
		if execution.Plan.MaxReplicas != 20 || execution.Plan.BaselineReplicas != 1 || execution.Plan.TargetReplicas != 2 || execution.Plan.NodePoolID != "np-basic" {
			t.Fatalf("execution recomputed target: %#v", execution)
		}
	}
}

func TestComputeAllocationClaimInterruptionPersistsClaimOnlyStateAndNeverRecreates(t *testing.T) {
	for _, failAt := range []string{"tag", "sync"} {
		t.Run(failAt, func(t *testing.T) {
			provider := &claimInterruptedAllocationProvider{
				allocationScopedProvider: allocationScopedProvider{
					preparation: ComputeAllocationPreparation{
						PoolID: "pool-basic-2c4g", PackageID: "basic", NodePoolID: "np-basic", InstanceType: "SA5.MEDIUM4",
						MaxReplicas: 20, BaselineReplicas: 1, TargetReplicas: 2, BeforeMachineNames: []string{"machine-existing"},
					},
					executeResults: []ComputeAllocation{readyComputeAllocation("machine-new", "ins-new", "10.0.0.12")},
				},
				failAt: failAt,
			}
			store := NewMemoryOperationStore()
			service := NewServiceWithOperationStore(provider, store)
			configureFastComputeAllocationPolling(service, 100*time.Millisecond)
			input := ComputeAllocationInput{
				AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "claim-interrupted-" + failAt,
			}

			created, err := service.CreateComputeAllocation(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			waitForComputeClaimPending(t, service, created.ID)
			current, ok := service.GetComputeAllocation(context.Background(), created.ID)
			ownership, ownershipErr := store.MachineOwnership(context.Background(), created.ID)
			if !ok || ownershipErr != nil || current.Status != "compute_claim_pending" || current.ProviderData["recoveryAction"] != "compute_claim_recovery" || ownership.Status != "quarantined" {
				t.Fatalf("current=%#v ok=%v ownership=%#v ownershipErr=%v", current, ok, ownership, ownershipErr)
			}

			provider.mu.Lock()
			prepareCalls, executeCalls, tagCalls, syncCalls, storageCalls := provider.prepareCalls, provider.executeCalls, provider.tagCalls, provider.syncCalls, provider.storageCalls
			provider.mu.Unlock()
			if prepareCalls != 1 || executeCalls != 1 || tagCalls != 1 || syncCalls != map[string]int{"tag": 0, "sync": 1}[failAt] || storageCalls != 0 {
				t.Fatalf("before replay calls prepare=%d execute=%d tag=%d sync=%d storage=%d", prepareCalls, executeCalls, tagCalls, syncCalls, storageCalls)
			}

			replayed, err := service.CreateComputeAllocation(context.Background(), input)
			if err != nil || replayed.ID != created.ID || replayed.Status != "compute_claim_pending" {
				t.Fatalf("replayed=%#v err=%v", replayed, err)
			}
			waitForComputeReconcileIdle(t, service, created.ID)
			provider.mu.Lock()
			defer provider.mu.Unlock()
			if provider.prepareCalls != 1 || provider.executeCalls != 1 || provider.tagCalls != 1 || provider.syncCalls != syncCalls || provider.storageCalls != 0 {
				t.Fatalf("replay performed provider mutation: prepare=%d execute=%d tag=%d sync=%d storage=%d", provider.prepareCalls, provider.executeCalls, provider.tagCalls, provider.syncCalls, provider.storageCalls)
			}
		})
	}
}

func waitForComputeClaimPending(t *testing.T, service *Service, resourceID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		operations, err := service.ListOperations(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, operation := range operations {
			if operation.Action == "create_compute_allocation" && operation.ResourceID == resourceID && operation.Status == "claim_pending" {
				if operation.ErrorCode == "" || !operation.FinishedAt.IsZero() {
					t.Fatalf("claim pending operation audit fields=%#v", operation)
				}
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("missing claim_pending compute operation %s", resourceID)
}

func TestComputeAllocationNeverClaimsMachinePresentBeforeScale(t *testing.T) {
	provider := &allocationScopedProvider{
		preparation: ComputeAllocationPreparation{
			PoolID: "pool-basic-2c4g", PackageID: "basic", NodePoolID: "np-basic", InstanceType: "SA5.MEDIUM4",
			MaxReplicas: 20, BaselineReplicas: 1, TargetReplicas: 2, BeforeMachineNames: []string{"machine-existing"},
		},
		executeResults: []ComputeAllocation{readyComputeAllocation("machine-existing", "ins-existing", "10.0.0.11")},
	}
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(provider, store)
	created, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{
		AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "compute-old-machine",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", created.ID, "failed")
	current, ok := service.GetComputeAllocation(context.Background(), created.ID)
	if !ok || current.Status != "quarantined" || current.ProviderData["recoveryAction"] != "manual_review" {
		t.Fatalf("current=%#v ok=%v", current, ok)
	}
	if _, ownershipErr := store.MachineOwnership(context.Background(), created.ID); !errors.Is(ownershipErr, ErrMachineOwnershipNotFound) {
		t.Fatalf("old machine was claimed: %v", ownershipErr)
	}
}

func readyComputeAllocation(machineID, instanceID, nodeName string) ComputeAllocation {
	return ComputeAllocation{
		Status: "running", Provider: "tencent-tke", ProviderRequestID: "req-scale", ProviderResourceID: "machine/" + machineID,
		PoolID: "pool-basic-2c4g", NodePoolID: "np-basic", MachineName: machineID, InstanceID: instanceID, CVMInstanceID: instanceID,
		NodeName: nodeName, PrivateIP: nodeName, InstanceType: "SA5.MEDIUM4", Zone: "na-siliconvalley-1", ChargeType: "PREPAID",
		RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-25T00:00:00Z",
		ProviderData: map[string]string{"instanceType": "SA5.MEDIUM4", "zone": "na-siliconvalley-1", "chargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-25T00:00:00Z"},
	}
}

func waitForComputeProviderCalls(t *testing.T, provider *allocationScopedProvider, expected int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		provider.mu.Lock()
		calls := provider.executeCalls
		provider.mu.Unlock()
		if calls >= expected {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("provider execute calls did not reach %d", expected)
}
