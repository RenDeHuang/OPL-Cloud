package fabric

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

type recordingPoolProvider struct {
	testProvider
	mu         sync.Mutex
	maxDesired int64
}

type failingPoolProvider struct {
	testProvider
	syncCalls int
}

type tagFailurePoolProvider struct {
	testProvider
	deleteCalls int
	deleteErr   error
}

func (*tagFailurePoolProvider) TagComputeMachine(context.Context, ProviderMachine, MachineOwnership) error {
	return fmt.Errorf("node label failed")
}

func (p *tagFailurePoolProvider) DeleteComputeMachine(context.Context, ProviderMachine) error {
	p.deleteCalls++
	return p.deleteErr
}

func TestPoolAllocatorDoesNotReleaseHoldStateWhileMachineIsQuarantined(t *testing.T) {
	provider := &tagFailurePoolProvider{deleteErr: fmt.Errorf("tencent delete failed")}
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(provider, store)
	resource := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic", Status: "provisioning"}
	operation := newOperation("create_compute_allocation", "compute_allocation", resource.ID, resource.AccountID, "", "request-alpha", hashInput(resource), time.Now().UTC())
	if err := service.recordOperation(context.Background(), operation, "started", resource, nil); err != nil {
		t.Fatal(err)
	}
	service.computes[resource.ID] = resource
	oldAttempts, oldDelay := poolReconcileAttempts, poolReconcileDelay
	poolReconcileAttempts, poolReconcileDelay = 1, 0
	t.Cleanup(func() { poolReconcileAttempts, poolReconcileDelay = oldAttempts, oldDelay })

	service.reconcileComputePool("basic", false)
	got, _ := service.GetComputeAllocation(context.Background(), resource.ID)
	if got.Status != "quarantined" || got.MachineName == "" || got.InstanceID == "" || got.NodePoolID == "" {
		t.Fatalf("resource with undeleted machine = %#v", got)
	}
}

func TestPoolAllocatorDeletesPartiallyTaggedMachineBeforeReleasingClaim(t *testing.T) {
	provider := &tagFailurePoolProvider{}
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(provider, store)
	resource := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic", Status: "provisioning"}
	operation := newOperation("create_compute_allocation", "compute_allocation", resource.ID, resource.AccountID, "", "request-alpha", hashInput(resource), time.Now().UTC())
	if err := service.recordOperation(context.Background(), operation, "started", resource, nil); err != nil {
		t.Fatal(err)
	}
	service.computes[resource.ID] = resource

	_, _, _ = service.reconcileComputePoolOnce(context.Background(), "basic", false)
	ownership, err := store.MachineOwnership(context.Background(), resource.ID)
	if err != nil || ownership.Status != "released" || provider.deleteCalls != 1 {
		t.Fatalf("partial claim cleanup ownership=%#v err=%v deletes=%d", ownership, err, provider.deleteCalls)
	}
}

func (p *failingPoolProvider) ReconcileComputePool(context.Context, ComputePoolDemand) (ComputePoolState, error) {
	return ComputePoolState{}, fmt.Errorf("tencent pool unavailable")
}

func (p *failingPoolProvider) SyncComputeAllocation(_ context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	p.syncCalls++
	return allocation, nil
}

func TestPoolAllocatorExhaustionPersistsFailedResourceForHoldRelease(t *testing.T) {
	provider := &failingPoolProvider{}
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(provider, store)
	resource := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic", Status: "provisioning", ProviderRequestID: "local-request"}
	operation := newOperation("create_compute_allocation", "compute_allocation", resource.ID, resource.AccountID, "", "request-alpha", hashInput(resource), time.Now().UTC())
	if err := service.recordOperation(context.Background(), operation, "started", resource, nil); err != nil {
		t.Fatal(err)
	}
	service.computes[resource.ID] = resource
	oldAttempts, oldDelay := poolReconcileAttempts, poolReconcileDelay
	poolReconcileAttempts, poolReconcileDelay = 1, 0
	t.Cleanup(func() { poolReconcileAttempts, poolReconcileDelay = oldAttempts, oldDelay })

	service.reconcileComputePool("basic", false)
	failed, ok := service.GetComputeAllocation(context.Background(), resource.ID)
	if !ok || failed.Status != "failed" {
		t.Fatalf("failed resource = %#v ok=%v", failed, ok)
	}
	synced, err := service.SyncComputeAllocation(context.Background(), resource.ID)
	if err != nil || synced.Status != "failed" || provider.syncCalls != 0 {
		t.Fatalf("sync failed resource = %#v err=%v provider calls=%d", synced, err, provider.syncCalls)
	}
}

func (p *recordingPoolProvider) ReconcileComputePool(ctx context.Context, input ComputePoolDemand) (ComputePoolState, error) {
	p.mu.Lock()
	if input.DesiredReplicas > p.maxDesired {
		p.maxDesired = input.DesiredReplicas
	}
	p.mu.Unlock()
	return p.testProvider.ReconcileComputePool(ctx, input)
}

func TestPoolAllocatorAssignsDifferentMachinesToConcurrentResources(t *testing.T) {
	provider := &recordingPoolProvider{}
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(provider, store)
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{ID: fmt.Sprintf("resource-%03d", i), AccountID: fmt.Sprintf("acct-%03d", i), PackageID: "basic", IdempotencyKey: fmt.Sprintf("request-%03d", i)})
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		ownerships, err := store.ListMachineOwnerships(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(ownerships) == 100 {
			seen := map[string]bool{}
			for _, ownership := range ownerships {
				if ownership.Status != "active" || seen[ownership.MachineID] {
					t.Fatalf("invalid ownership: %#v", ownership)
				}
				seen[ownership.MachineID] = true
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ownership count = %d, want 100", len(ownerships))
		}
		time.Sleep(10 * time.Millisecond)
	}
	provider.mu.Lock()
	maxDesired := provider.maxDesired
	provider.mu.Unlock()
	if maxDesired != 100 {
		t.Fatalf("max desired replicas = %d, want 100", maxDesired)
	}
}
