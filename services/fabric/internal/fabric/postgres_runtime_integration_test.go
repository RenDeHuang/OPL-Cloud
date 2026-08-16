package fabric

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/lib/pq"

	fabricent "opl-cloud/services/fabric/ent"
)

type runtimeMutationBarrier struct {
	mutation   string
	mutated    chan struct{}
	release    chan struct{}
	notifyOnce sync.Once
	releaseOne sync.Once
	armed      atomic.Bool
}

func newRuntimeMutationBarrier(mutation string) *runtimeMutationBarrier {
	return &runtimeMutationBarrier{mutation: mutation, mutated: make(chan struct{}), release: make(chan struct{})}
}

func (b *runtimeMutationBarrier) matchesMutation(query string) bool {
	query = strings.ToUpper(strings.TrimSpace(query))
	return strings.HasPrefix(query, b.mutation) && strings.Contains(query, "FABRIC_OPERATIONS")
}

func (b *runtimeMutationBarrier) notifyMutation() {
	b.armed.Store(true)
	b.notifyOnce.Do(func() { close(b.mutated) })
}

func (b *runtimeMutationBarrier) waitBeforeRead(ctx context.Context, query string) error {
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "SELECT") || !strings.Contains(strings.ToUpper(query), "FABRIC_OPERATIONS") || !b.armed.CompareAndSwap(true, false) {
		return nil
	}
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *runtimeMutationBarrier) releaseRead() {
	b.releaseOne.Do(func() { close(b.release) })
}

type runtimeMutationBarrierConnector struct {
	driver.Connector
	barrier *runtimeMutationBarrier
}

func (c *runtimeMutationBarrierConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &runtimeMutationBarrierConn{Conn: conn, barrier: c.barrier}, nil
}

type runtimeMutationBarrierConn struct {
	driver.Conn
	barrier *runtimeMutationBarrier
}

func (c *runtimeMutationBarrierConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.barrier.waitBeforeRead(ctx, query); err != nil {
		return nil, err
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil || !c.barrier.matchesMutation(query) {
		return rows, err
	}
	return &runtimeMutationBarrierRows{Rows: rows, notify: c.barrier.notifyMutation}, nil
}

func (c *runtimeMutationBarrierConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	if err == nil && c.barrier.matchesMutation(query) {
		c.barrier.notifyMutation()
	}
	return result, err
}

type runtimeMutationBarrierRows struct {
	driver.Rows
	notify func()
	once   sync.Once
}

func (r *runtimeMutationBarrierRows) Close() error {
	err := r.Rows.Close()
	r.once.Do(r.notify)
	return err
}

func newBarrierPostgresOperationStore(t *testing.T, databaseURL, mutation string) (*PostgresOperationStore, *runtimeMutationBarrier) {
	t.Helper()
	connector, err := pq.NewConnector(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	barrier := newRuntimeMutationBarrier(mutation)
	db := sql.OpenDB(&runtimeMutationBarrierConnector{Connector: connector, barrier: barrier})
	store := &PostgresOperationStore{db: db, client: fabricent.NewClient(fabricent.Driver(entsql.OpenDB(dialect.Postgres, db)))}
	t.Cleanup(func() {
		barrier.releaseRead()
		if err := store.client.Close(); err != nil {
			t.Errorf("close barrier operation store: %v", err)
		}
	})
	return store, barrier
}

func TestPostgresRuntimeMutationReturnsOwnFenceAtomically(t *testing.T) {
	for _, mutation := range []string{"INSERT", "UPDATE"} {
		t.Run(strings.ToLower(mutation), func(t *testing.T) {
			databaseURL := fabricTestDatabaseURL(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			currentStore, err := newTestPostgresOperationStore(databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			defer currentStore.client.Close()
			barrierStore, barrier := newBarrierPostgresOperationStore(t, databaseURL, mutation)

			startedAt := time.Date(2026, 7, 17, 0, 0, 0, 123456789, time.UTC)
			operation := newOperation("create_workspace_runtime", "workspace_runtime", "workspace-atomic-fence", "acct-alpha", "workspace-atomic-fence", "runtime-atomic-fence", "request-hash", startedAt)
			operation.ID = "fop-runtime-atomic-fence"
			operation.Status = "started"
			operation.CreatedAt = startedAt
			priorStartedAt := time.Time{}
			if mutation == "UPDATE" {
				seeded, claimed, err := currentStore.ClaimRuntime(ctx, operation)
				if err != nil || !claimed {
					t.Fatalf("seed runtime claim=%#v claimed=%v err=%v", seeded, claimed, err)
				}
				priorStartedAt = seeded.StartedAt
			}

			type claimResult struct {
				operation FabricOperation
				won       bool
				err       error
			}
			result := make(chan claimResult, 1)
			requestedStartedAt := startedAt
			go func() {
				if mutation == "INSERT" {
					stored, claimed, err := barrierStore.ClaimRuntime(ctx, operation)
					result <- claimResult{operation: stored, won: claimed, err: err}
					return
				}
				requestedStartedAt = priorStartedAt.Add(3*time.Minute + 789*time.Nanosecond)
				stored, won, err := barrierStore.ReclaimRuntime(ctx, operation.ID, priorStartedAt, requestedStartedAt)
				result <- claimResult{operation: stored, won: won, err: err}
			}()

			select {
			case <-barrier.mutated:
			case <-ctx.Done():
				t.Fatal("runtime mutation did not reach the readback boundary")
			}
			operations, err := currentStore.List(ctx)
			if err != nil || len(operations) != 1 {
				t.Fatalf("read mutation fence operations=%#v err=%v", operations, err)
			}
			canonicalStartedAt := operations[0].StartedAt
			if canonicalStartedAt.Equal(requestedStartedAt) {
				t.Fatal("test input must exercise PostgreSQL timestamp canonicalization")
			}
			successorStartedAt := canonicalStartedAt.Add(3*time.Minute + 987*time.Nanosecond)
			successor, won, err := currentStore.ReclaimRuntime(ctx, operation.ID, canonicalStartedAt, successorStartedAt)
			if err != nil || !won {
				t.Fatalf("successor reclaim=%#v won=%v err=%v", successor, won, err)
			}
			barrier.releaseRead()
			owner := <-result
			if owner.err != nil || !owner.won {
				t.Fatalf("mutation owner=%#v won=%v err=%v", owner.operation, owner.won, owner.err)
			}
			if !owner.operation.StartedAt.Equal(canonicalStartedAt) {
				t.Fatalf("mutation owner received successor fence: got=%s own=%s successor=%s", owner.operation.StartedAt, canonicalStartedAt, successor.StartedAt)
			}
			owner.operation.Status = "succeeded"
			owner.operation.FinishedAt = successor.StartedAt.Add(time.Second)
			if err := barrierStore.SaveRuntime(ctx, owner.operation); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
				t.Fatalf("superseded owner save error=%v, want ErrRuntimeOperationNotCurrent", err)
			}
			current, won, err := currentStore.ReclaimRuntime(ctx, operation.ID, canonicalStartedAt, successor.StartedAt.Add(time.Minute))
			if err != nil || won || !current.StartedAt.Equal(successor.StartedAt) {
				t.Fatalf("losing reclaim current=%#v won=%v err=%v", current, won, err)
			}
		})
	}
}

type stalePostgresRuntimeProvider struct {
	testProvider
	calls         atomic.Int32
	readbackCalls atomic.Int32
	readback      WorkspaceRuntime
}

func (p *stalePostgresRuntimeProvider) CreateWorkspaceRuntime(_ context.Context, input WorkspaceRuntimeInput, _ ComputeAllocation, _ StorageVolume) (WorkspaceRuntime, error) {
	p.calls.Add(1)
	p.readback = WorkspaceRuntime{
		ID: "rt_postgres-alpha", OperationID: input.RuntimeOperationID, WorkspaceID: input.WorkspaceID,
		Status: "running", Ready: true, ServiceName: "opl-compute-alpha", ImageID: input.ImageID,
		ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey),
		CostTags:          oplCostTags("acct-alpha", input.WorkspaceID, "rt_postgres-alpha", input.RuntimeOperationID),
	}
	return p.readback, nil
}

func (p *stalePostgresRuntimeProvider) WorkspaceRuntimeStatus(_ context.Context, _ string) (WorkspaceRuntime, error) {
	p.readbackCalls.Add(1)
	return p.readback, nil
}

func TestPostgresPersistedClaimPendingConcurrentReplayWaitsForControlPlaneDecision(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = firstStore.client.Close() })
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.client.Close() })
	provider := &normalLaunchComputeProvider{}
	input, allocation := seedNormalWorkspaceComputeClaimPending(t, firstStore, provider, "postgres-automatic-concurrent")
	first := NewServiceWithOperationStore(provider, firstStore)
	second := NewServiceWithOperationStore(provider, secondStore)

	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, service := range []*Service{first, second} {
		service := service
		go func() {
			<-start
			_, replayErr := service.CreateComputeAllocation(context.Background(), input)
			errs <- replayErr
		}()
	}
	close(start)
	for range 2 {
		if replayErr := <-errs; replayErr != nil {
			t.Fatal(replayErr)
		}
	}
	prepare, create, proof, cvmClaim, nodeClaim := provider.automaticContinuationCounts()
	if prepare != 0 || create != 0 || proof != 0 || cvmClaim != 0 || nodeClaim != 0 {
		t.Fatalf("PostgreSQL replay crossed Control Plane authorization: prepare=%d create=%d proof=%d cvmClaim=%d nodeClaim=%d", prepare, create, proof, cvmClaim, nodeClaim)
	}
	operations, operationsErr := secondStore.List(context.Background())
	if operationsErr != nil || len(operations) != 1 || operations[0].Status != "claim_pending" {
		t.Fatalf("PostgreSQL replay changed operation: operations=%#v err=%v", operations, operationsErr)
	}
	ownership, ownershipErr := secondStore.MachineOwnership(context.Background(), allocation.ID)
	if ownershipErr != nil || ownership.Status != "quarantined" {
		t.Fatalf("PostgreSQL ownership=%#v err=%v", ownership, ownershipErr)
	}
}

func TestPostgresStaleRuntimeClaimConvergesAcrossServiceInstances(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatalf("open first operation store: %v", err)
	}
	defer firstStore.client.Close()
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatalf("open second operation store: %v", err)
	}
	defer secondStore.client.Close()

	provider := &stalePostgresRuntimeProvider{}
	firstService := runtimeTestService(provider, &failFirstRuntimeSaveStore{OperationStore: firstStore})
	secondService := runtimeTestService(provider, secondStore)
	startedAt := time.Date(2026, 7, 17, 0, 0, 0, 123456000, time.UTC)
	firstService.now = func() time.Time { return startedAt }
	secondService.now = func() time.Time { return startedAt.Add(3 * time.Minute) }
	input := runtimeTestInput("postgres-runtime-stale")

	firstResult, firstErr := firstService.CreateWorkspaceRuntime(ctx, input)
	if firstErr == nil || firstErr.Error() != "injected runtime save failure" || firstResult.ID != provider.readback.ID || provider.calls.Load() != 1 {
		t.Fatalf("first runtime=%#v err=%v providerCalls=%d", firstResult, firstErr, provider.calls.Load())
	}
	operations, err := firstStore.List(ctx)
	if err != nil || len(operations) != 1 || operations[0].Status != "started" {
		t.Fatalf("persisted old claim=%#v err=%v", operations, err)
	}

	secondResult, secondErr := secondService.CreateWorkspaceRuntime(ctx, input)
	if secondErr != nil || secondResult.ID != provider.readback.ID || provider.calls.Load() != 1 || provider.readbackCalls.Load() != 1 {
		t.Fatalf("readback convergence runtime=%#v err=%v providerCalls=%d readbackCalls=%d", secondResult, secondErr, provider.calls.Load(), provider.readbackCalls.Load())
	}
	firstReplay, firstReplayErr := firstService.CreateWorkspaceRuntime(ctx, input)
	if firstReplayErr != nil || firstReplay.ID != secondResult.ID || provider.calls.Load() != 1 {
		t.Fatalf("final replay=%#v err=%v providerCalls=%d", firstReplay, firstReplayErr, provider.calls.Load())
	}
	operations, err = secondStore.List(ctx)
	if err != nil || len(operations) != 1 || operations[0].Status != "succeeded" {
		t.Fatalf("final operations=%#v err=%v", operations, err)
	}
}

func TestPostgresRuntimeClaimAcrossServiceInstances(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var firstStore, secondStore *PostgresOperationStore
	t.Cleanup(func() {
		if secondStore != nil {
			if err := secondStore.client.Close(); err != nil {
				t.Errorf("close second operation store: %v", err)
			}
		}
		if firstStore != nil {
			if err := firstStore.client.Close(); err != nil {
				t.Errorf("close first operation store: %v", err)
			}
		}
	})

	var err error
	firstStore, err = newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatalf("open first operation store: %v", err)
	}
	secondStore, err = newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatalf("open second operation store: %v", err)
	}

	provider := &blockingRuntimeProvider{entered: make(chan struct{}), release: make(chan struct{})}
	firstService := runtimeTestService(provider, firstStore)
	secondService := runtimeTestService(provider, secondStore)
	input := runtimeTestInput("postgres-runtime-shared")
	firstDone := make(chan error, 1)
	go func() {
		_, err := firstService.CreateWorkspaceRuntime(ctx, input)
		firstDone <- err
	}()
	select {
	case <-provider.entered:
	case <-ctx.Done():
		t.Fatal("first provider call did not start")
	}
	if _, err := secondService.CreateWorkspaceRuntime(ctx, input); err != ErrRuntimeOperationInProgress {
		t.Fatalf("concurrent replay error = %v, want %v", err, ErrRuntimeOperationInProgress)
	}
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	close(provider.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first runtime create: %v", err)
	}

	replayed, err := NewServiceWithOperationStore(provider, secondStore).CreateWorkspaceRuntime(ctx, input)
	if err != nil || replayed.ID != "runtime-alpha" || provider.calls.Load() != 1 {
		t.Fatalf("postgres restart replay = %#v err=%v providerCalls=%d", replayed, err, provider.calls.Load())
	}
}

func TestPostgresComputePoolHeadSerializesDifferentWorkspacesAcrossServiceInstances(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.client.Close()
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()

	provider := newSerializedPoolProvider("workspace-alpha")
	firstService := NewServiceWithOperationStore(provider, firstStore)
	secondService := NewServiceWithOperationStore(provider, secondStore)
	configureFastComputeAllocationPolling(firstService, 15*time.Millisecond)
	configureFastComputeAllocationPolling(secondService, 100*time.Millisecond)
	firstService.computeAllocationFinalizeTimeout = 2 * time.Second
	secondService.computeAllocationFinalizeTimeout = 2 * time.Second
	firstInput := ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "postgres-compute-alpha"}
	secondInput := ComputeAllocationInput{AccountID: "acct-beta", WorkspaceID: "workspace-beta", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "postgres-compute-beta"}

	first, err := firstService.CreateComputeAllocation(ctx, firstInput)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.firstHeadCall:
	case <-ctx.Done():
		t.Fatal("PostgreSQL head did not reach provider")
	}
	waitForComputeReconcileIdle(t, firstService, first.ID)
	second, err := secondService.CreateComputeAllocation(ctx, secondInput)
	if err != nil {
		t.Fatal(err)
	}
	waitForComputeReconcileIdle(t, secondService, second.ID)
	if calls := provider.workspaceCalls("workspace-beta"); calls != 0 {
		t.Fatalf("second PostgreSQL service bypassed persisted head: calls=%d", calls)
	}
	if calls := provider.workspacePrepareCalls("workspace-beta"); calls != 0 {
		t.Fatalf("second PostgreSQL service prepared before persisted head: calls=%d", calls)
	}

	provider.allowHeadCompletion()
	if _, err := secondService.CreateComputeAllocation(ctx, firstInput); err != nil {
		t.Fatal(err)
	}
	waitForPostgresComputeOperationSucceeded(t, ctx, secondService, secondStore, provider, first.ID, firstInput.WorkspaceID)
	if _, err := firstService.CreateComputeAllocation(ctx, secondInput); err != nil {
		t.Fatal(err)
	}
	waitForPostgresComputeOperationSucceeded(t, ctx, firstService, firstStore, provider, second.ID, secondInput.WorkspaceID)
	if targets, ambiguous := provider.allocationEvidence("np-basic"); !reflect.DeepEqual(targets, []int64{1, 2}) || ambiguous != 0 {
		t.Fatalf("PostgreSQL scale targets=%v ambiguous=%d", targets, ambiguous)
	}
}

func waitForPostgresComputeOperationSucceeded(t *testing.T, ctx context.Context, service *Service, store *PostgresOperationStore, provider *serializedPoolProvider, resourceID, workspaceID string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var latest FabricOperation
	for {
		operations, err := store.List(ctx)
		if err != nil {
			t.Fatalf("list PostgreSQL compute operations: %v", err)
		}
		for _, operation := range operations {
			if operation.Action != "create_compute_allocation" || operation.ResourceKind != "compute_allocation" || operation.ResourceID != resourceID {
				continue
			}
			latest = operation
			switch operation.Status {
			case "succeeded":
				if operation.OperationID == "" || operation.ProviderRequestID == "" || operation.RequestHash == "" || operation.StartedAt.IsZero() || operation.FinishedAt.IsZero() {
					t.Fatalf("PostgreSQL compute operation missing audit fields: %#v", operation)
				}
				return
			case "failed", "claim_pending":
				t.Fatalf("PostgreSQL compute operation reached %s: %#v", operation.Status, operation)
			}
		}
		select {
		case <-ctx.Done():
			service.mu.Lock()
			reconciling := service.reconciling[resourceID]
			service.mu.Unlock()
			targets, ambiguous := provider.allocationEvidence("np-basic")
			t.Fatalf("PostgreSQL compute operation did not succeed: resource=%s operation=%#v leaseOwner=%q leaseExpires=%v providerCalls=%d prepareCalls=%d targets=%v ambiguous=%d reconciling=%v context=%v",
				resourceID, latest, latest.ComputePoolLeaseOwner, latest.ComputePoolLeaseExpires,
				provider.workspaceCalls(workspaceID), provider.workspacePrepareCalls(workspaceID), targets, ambiguous, reconciling, ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestPostgresComputeClaimPendingKeepsFIFOHead(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.client.Close()
	createdAt := time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC)
	first := newOperation("create_compute_allocation", "compute_allocation", "compute-first", "acct-alpha", "workspace-first", "compute-first", "hash-first", createdAt)
	first.ID = "fop-compute-first"
	first.Status = "started"
	first.CreatedAt = createdAt
	first.ComputePoolKey = "np-basic"
	second := newOperation("create_compute_allocation", "compute_allocation", "compute-second", "acct-beta", "workspace-second", "compute-second", "hash-second", createdAt.Add(time.Second))
	second.ID = "fop-compute-second"
	second.Status = "started"
	second.CreatedAt = createdAt.Add(time.Second)
	second.ComputePoolKey = "np-basic"
	for _, operation := range []FabricOperation{first, second} {
		if _, claimed, err := store.ClaimComputePoolRuntime(ctx, operation); err != nil || !claimed {
			t.Fatalf("seed compute operation %s: claimed=%v err=%v", operation.ID, claimed, err)
		}
	}

	head, claimed, err := store.TryClaimComputePoolHead(ctx, first.ID, "np-basic", "lease-first", createdAt, createdAt.Add(time.Minute))
	if err != nil || !claimed {
		t.Fatalf("claim first head=%#v claimed=%v err=%v", head, claimed, err)
	}
	head.Status = "claim_pending"
	head.FinishedAt = time.Time{}
	if err := store.SaveRuntime(ctx, head); err != nil {
		t.Fatalf("persist claim_pending head: %v", err)
	}

	queued, claimed, err := store.TryClaimComputePoolHead(ctx, second.ID, "np-basic", "lease-second", createdAt.Add(time.Minute), createdAt.Add(2*time.Minute))
	if err != nil || claimed || queued.ID != first.ID || queued.Status != "claim_pending" {
		t.Fatalf("claim_pending head was bypassed: queued=%#v claimed=%v err=%v", queued, claimed, err)
	}
}

func TestPostgresComputePoolHeadTerminalizationCASReleasesFreshFIFOHead(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.client.Close()
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()
	provider := &normalLaunchComputeProvider{}
	input, _, _ := seedOperatorTerminalizationHeadWithBinding(t, firstStore, provider, func(binding *computeClaimRecoveryBinding) {
		binding.IdempotencyKey = "recovery-exec-14deb7f41022c8a5ae9d"
	})
	fresh := FabricOperation{
		ID: "fop-postgres-fresh", OperationID: "op-postgres-fresh", Action: "create_compute_allocation", ResourceKind: "compute_allocation", ResourceID: "ca-postgres-fresh",
		IdempotencyKey: "workspace-launch-postgres-fresh:compute", RequestHash: "hash-postgres-fresh", Status: "started", ComputePoolKey: input.NodePoolID,
	}
	if _, claimed, err := firstStore.ClaimComputePoolRuntime(ctx, fresh); err != nil || !claimed {
		t.Fatalf("fresh seed claimed=%v err=%v", claimed, err)
	}
	firstService := NewServiceWithOperationStore(provider, firstStore)
	secondService := NewServiceWithOperationStore(provider, secondStore)
	readback, err := firstService.ReadComputePoolHeadTerminalization(ctx, input.NodePoolID)
	if err != nil {
		t.Fatal(err)
	}
	request := ComputePoolHeadTerminalizationInput{
		NodePoolID: input.NodePoolID, ApprovalID: "fresh-head-terminalize-30970000004",
		ApprovalDigest: readback.ApprovalDigest, IdempotencyKey: "fresh-head-terminalize-30970000004",
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, service := range []*Service{firstService, secondService} {
		service := service
		go func() {
			<-start
			_, err := service.TerminalizeComputePoolHead(ctx, request)
			results <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("terminalization error=%v", err)
		}
	}
	replayed, err := secondService.ReadComputePoolHeadTerminalizationResult(ctx, request)
	if err != nil || replayed.Status != "succeeded" || !replayed.Replayed || replayed.ApprovalDigest != request.ApprovalDigest {
		t.Fatalf("terminalization replay=%#v err=%v", replayed, err)
	}
	prepare, create, proof, cvm, node := provider.automaticContinuationCounts()
	if prepare != 0 || create != 0 || proof != 0 || cvm != 0 || node != 0 {
		t.Fatalf("provider calls=%d/%d/%d/%d/%d", prepare, create, proof, cvm, node)
	}
	head, found, err := firstStore.ComputePoolHead(ctx, input.NodePoolID)
	if err != nil || !found || head.ID != fresh.ID || head.Status != "started" || head.ComputePoolLeaseOwner != "" {
		t.Fatalf("fresh read-only head=%#v found=%v err=%v", head, found, err)
	}
	claimedHead, claimed, err := secondStore.TryClaimComputePoolHead(ctx, fresh.ID, input.NodePoolID, "fresh-postgres-lease", time.Now().UTC(), time.Now().UTC().Add(time.Minute))
	if err != nil || !claimed || claimedHead.ID != fresh.ID {
		t.Fatalf("fresh claimed head=%#v claimed=%v err=%v", claimedHead, claimed, err)
	}
}

func TestPostgresComputePoolLeaseUsesDatabaseClockAcrossServiceInstances(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.client.Close()
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()

	operation := newOperation("create_compute_allocation", "compute_allocation", "compute-clock-skew", "acct-alpha", "workspace-alpha", "compute-clock-skew", "hash-clock-skew", time.Now().UTC())
	operation.ID = "fop_compute_claim_clock_skew"
	operation.Status = "started"
	operation.ComputePoolKey = "np-basic"
	stored, claimed, err := firstStore.ClaimComputePoolRuntime(ctx, operation)
	if err != nil || !claimed {
		t.Fatalf("seed compute operation: claimed=%v err=%v", claimed, err)
	}

	skewedNow := time.Now().UTC().Add(-time.Hour)
	if _, claimed, err := firstStore.TryClaimComputePoolHead(ctx, stored.ID, "np-basic", "lease-skewed", skewedNow, skewedNow.Add(time.Minute)); err != nil || !claimed {
		t.Fatalf("first lease: claimed=%v err=%v", claimed, err)
	}
	current, claimed, err := secondStore.TryClaimComputePoolHead(ctx, stored.ID, "np-basic", "lease-current", time.Now().UTC(), time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if claimed || current.ComputePoolLeaseOwner != "lease-skewed" {
		t.Fatalf("database lease was stolen because of process clock skew: claimed=%v current=%#v", claimed, current)
	}
}

func TestPostgresDestroyRuntimeFailedRetryBindsWorkspaceAcrossServiceInstances(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatalf("open first operation store: %v", err)
	}
	defer firstStore.client.Close()
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatalf("open second operation store: %v", err)
	}
	defer secondStore.client.Close()

	originalProvider := &failOnceDestroyProvider{}
	originalService := NewServiceWithOperationStore(originalProvider, firstStore)
	if _, err := originalService.DestroyWorkspaceRuntime(ctx, "workspace-alpha", "postgres-runtime-destroy-once"); err == nil {
		t.Fatal("first destroy succeeded, want transient failure")
	}
	before, err := firstStore.List(ctx)
	if err != nil || len(before) != 1 || before[0].Status != "failed" {
		t.Fatalf("failed operation = %#v err=%v", before, err)
	}

	otherProvider := &countingRuntimeProvider{}
	services := []*Service{
		NewServiceWithOperationStore(otherProvider, firstStore),
		NewServiceWithOperationStore(otherProvider, secondStore),
	}
	start := make(chan struct{})
	results := make(chan error, len(services))
	for _, service := range services {
		service := service
		go func() {
			<-start
			_, err := service.DestroyWorkspaceRuntime(ctx, "workspace-beta", "postgres-runtime-destroy-once")
			results <- err
		}()
	}
	close(start)
	for range services {
		if err := <-results; !errors.Is(err, ErrRuntimeIdempotencyConflict) {
			t.Fatalf("cross-workspace retry error = %v, want ErrRuntimeIdempotencyConflict", err)
		}
	}
	after, err := firstStore.List(ctx)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("cross-workspace retry changed operation: before=%#v after=%#v err=%v", before, after, err)
	}
	if otherProvider.destroyCalls.Load() != 0 {
		t.Fatalf("cross-workspace provider calls = %d, want 0", otherProvider.destroyCalls.Load())
	}

	runtime, err := originalService.DestroyWorkspaceRuntime(ctx, "workspace-alpha", "postgres-runtime-destroy-once")
	if err != nil || runtime.Status != "destroyed" || originalProvider.destroyCalls.Load() != 2 {
		t.Fatalf("original retry = %#v err=%v providerCalls=%d", runtime, err, originalProvider.destroyCalls.Load())
	}
}

func TestPostgresComputeClaimRecoveryOperationCASRejectsSkippedTransition(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	store, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.client.Close()

	operation := postgresComputeClaimOperation("skipped", "failed")
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	recovered := operation
	recovered.Status = "succeeded"
	recovered.FinishedAt = operation.CreatedAt.Add(time.Minute)
	if err := store.SaveComputeClaimRecovery(context.Background(), operation, recovered); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("failed -> succeeded error=%v, want ErrRuntimeOperationNotCurrent", err)
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].Status != "failed" {
		t.Fatalf("skipped transition changed operation: operations=%#v err=%v", operations, err)
	}
}

func TestPostgresComputeClaimRecoveryOperationCASAllowsOneTerminalClaimResult(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	store, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.client.Close()

	operation := postgresComputeClaimOperation("terminal", "claim_pending")
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	now := operation.CreatedAt.Add(time.Minute)
	terminal := operation
	terminal.Status, terminal.ErrorCode, terminal.FinishedAt = "failed", "compute_claim_terminal_node_unprovable", now
	terminal.RedactedProviderPayload = withComputeClaimTerminalEvidence(operation.RedactedProviderPayload, ComputeClaimTerminalEvidence{
		SchemaVersion: 1, Stage: "compute_claim_node", Status: "terminal_unprovable", ErrorCode: terminal.ErrorCode,
		ReadbackStatus: "unallocated", AttemptCount: 0, Attempted: 0, Confirmed: 0, Unknown: 0, Max: 0,
		StartedAt: operation.StartedAt.Format(time.RFC3339Nano), FinishedAt: now.Format(time.RFC3339Nano),
		FabricRecordID: operation.ID, OperationID: operation.OperationID, IdempotencyKey: operation.IdempotencyKey,
		RequestHash: operation.RequestHash, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
		ComputeAllocationID: operation.ResourceID, PackageID: "basic", NodePoolID: "np-postgres-basic",
	})
	if err := store.SaveComputeClaimRecovery(context.Background(), operation, terminal); err != nil {
		t.Fatalf("claim_pending -> terminal failed: %v", err)
	}
	if err := store.SaveComputeClaimRecovery(context.Background(), operation, terminal); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("stale terminal replay error=%v, want ErrRuntimeOperationNotCurrent", err)
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].Status != "failed" || operations[0].ErrorCode != terminal.ErrorCode {
		t.Fatalf("terminal operation=%#v err=%v", operations, err)
	}
}

func TestPostgresComputeClaimRecoveryOperationCASRejectsStaleAndRequestHashDrift(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	store, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.client.Close()

	operation := postgresComputeClaimOperation("identity", "failed")
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	pending := operation
	pending.Status = "claim_pending"
	pending.FinishedAt = time.Time{}
	pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, postgresComputeClaimRecoveryBinding("identity"))
	if err := store.SaveComputeClaimRecovery(context.Background(), operation, pending); err != nil {
		t.Fatalf("failed -> claim_pending: %v", err)
	}
	recovered := pending
	recovered.Status = "succeeded"
	recovered.FinishedAt = operation.CreatedAt.Add(time.Minute)
	if err := store.SaveComputeClaimRecovery(context.Background(), operation, recovered); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("stale failed owner error=%v, want ErrRuntimeOperationNotCurrent", err)
	}
	drifted := recovered
	drifted.RequestHash = "different-request-hash"
	if err := store.SaveComputeClaimRecovery(context.Background(), pending, drifted); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("request hash drift error=%v, want ErrRuntimeOperationNotCurrent", err)
	}
	if err := store.SaveComputeClaimRecovery(context.Background(), pending, recovered); err != nil {
		t.Fatalf("claim_pending -> succeeded: %v", err)
	}
	if err := store.SaveComputeClaimRecovery(context.Background(), pending, recovered); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("stale claim_pending owner error=%v, want ErrRuntimeOperationNotCurrent", err)
	}
}

func TestPostgresComputeClaimRecoveryOperationCASRejectsBindingDrift(t *testing.T) {
	drifts := map[string]func(*computeClaimRecoveryBinding){
		"launch": func(binding *computeClaimRecoveryBinding) { binding.LaunchOperationID = "launch-postgres-other" },
		"idempotency": func(binding *computeClaimRecoveryBinding) {
			binding.IdempotencyKey = "launch-postgres-binding:compute-other"
		},
		"target":  func(binding *computeClaimRecoveryBinding) { binding.TargetHash = "different-target-hash" },
		"request": func(binding *computeClaimRecoveryBinding) { binding.RequestHash = "different-request-hash" },
	}
	for name, drift := range drifts {
		t.Run(name, func(t *testing.T) {
			databaseURL := fabricTestDatabaseURL(t)
			store, err := newTestPostgresOperationStore(databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			defer store.client.Close()

			operation := postgresComputeClaimOperation("binding", "failed")
			if err := store.Append(context.Background(), operation); err != nil {
				t.Fatal(err)
			}
			pending := operation
			pending.Status, pending.FinishedAt = "claim_pending", time.Time{}
			binding := postgresComputeClaimRecoveryBinding("binding")
			pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, binding)
			if err := store.SaveComputeClaimRecovery(context.Background(), operation, pending); err != nil {
				t.Fatalf("failed -> claim_pending: %v", err)
			}
			drift(&binding)
			drifted := pending
			drifted.Status, drifted.FinishedAt = "succeeded", operation.CreatedAt.Add(time.Minute)
			drifted.RedactedProviderPayload = withComputeClaimRecoveryBinding(drifted.RedactedProviderPayload, binding)
			if err := store.SaveComputeClaimRecovery(context.Background(), pending, drifted); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
				t.Fatalf("binding drift error=%v, want ErrRuntimeOperationNotCurrent", err)
			}
			operations, err := store.List(context.Background())
			if err != nil || len(operations) != 1 {
				t.Fatalf("binding drift changed operation: operations=%#v err=%v", operations, err)
			}
			expectedPayloadJSON, expectedPayloadErr := operationPayloadJSON(pending)
			var expectedPayload map[string]any
			if expectedPayloadErr != nil || json.Unmarshal([]byte(expectedPayloadJSON), &expectedPayload) != nil ||
				operations[0].Status != "claim_pending" || !reflect.DeepEqual(operations[0].RedactedProviderPayload, expectedPayload) {
				t.Fatalf("binding drift changed operation: operations=%#v expectedPayload=%#v", operations, expectedPayload)
			}
		})
	}
}

func TestPostgresComputeClaimRecoveryOperationCASHasSingleConcurrentWinner(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.client.Close()
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()

	operation := postgresComputeClaimOperation("concurrent", "claim_pending")
	if err := firstStore.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	recovered := operation
	recovered.Status = "succeeded"
	recovered.FinishedAt = operation.CreatedAt.Add(time.Minute)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, store := range []*PostgresOperationStore{firstStore, secondStore} {
		store := store
		go func() {
			<-start
			results <- store.SaveComputeClaimRecovery(context.Background(), operation, recovered)
		}()
	}
	close(start)
	winners := 0
	for range 2 {
		err := <-results
		if err == nil {
			winners++
			continue
		}
		if !errors.Is(err, ErrRuntimeOperationNotCurrent) {
			t.Fatalf("concurrent CAS error=%v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent CAS winners=%d, want 1", winners)
	}
}

func TestPostgresComputeClaimRecoveryNodeReservationCASHasSingleWinnerAndKeepsOriginalBinding(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.client.Close()
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()

	operation := postgresComputeClaimOperation("node-reservation", "claim_pending")
	observed := observedComputeClaimRecoveryMutation(ComputeClaimRecoveryProof{
		Reason: "provider_describe", TencentMutationCount: 1, KubernetesMutationCount: 0,
		FailureStage: "cvm_tag_readback", ProviderErrorClass: "provider_error",
		Evidence: &ComputeClaimEvidence{CVM: ComputeClaimMutationEvidence{Attempted: 1, Missing: []string{"opl_account_id"}}},
	})
	operation.RedactedProviderPayload = withComputeClaimRecoveryMutation(operation.RedactedProviderPayload, observed)
	if err := firstStore.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	nodeReserved := operation
	nodeReserved.RedactedProviderPayload = withComputeClaimRecoveryMutation(nodeReserved.RedactedProviderPayload, nodeReservedComputeClaimRecoveryMutation(observed))

	for name, drift := range map[string]func(*computeClaimRecoveryBinding){
		"launch":      func(binding *computeClaimRecoveryBinding) { binding.LaunchOperationID = "launch-postgres-other" },
		"idempotency": func(binding *computeClaimRecoveryBinding) { binding.IdempotencyKey += "-other" },
		"target":      func(binding *computeClaimRecoveryBinding) { binding.TargetHash = "different-target-hash" },
		"request":     func(binding *computeClaimRecoveryBinding) { binding.RequestHash = "different-request-hash" },
	} {
		t.Run(name, func(t *testing.T) {
			binding, present, valid := decodeComputeClaimRecoveryBinding(nodeReserved)
			if !present || !valid {
				t.Fatalf("node reservation binding=%#v present=%v valid=%v", binding, present, valid)
			}
			drift(&binding)
			drifted := nodeReserved
			drifted.RedactedProviderPayload = withComputeClaimRecoveryBinding(drifted.RedactedProviderPayload, binding)
			if err := firstStore.SaveComputeClaimRecovery(context.Background(), operation, drifted); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
				t.Fatalf("node reservation binding drift error=%v, want ErrRuntimeOperationNotCurrent", err)
			}
		})
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for _, store := range []*PostgresOperationStore{firstStore, secondStore} {
		store := store
		go func() {
			<-start
			results <- store.SaveComputeClaimRecovery(context.Background(), operation, nodeReserved)
		}()
	}
	close(start)
	winners := 0
	for range 2 {
		err := <-results
		if err == nil {
			winners++
			continue
		}
		if !errors.Is(err, ErrRuntimeOperationNotCurrent) {
			t.Fatalf("node reservation concurrent CAS error=%v", err)
		}
	}
	stored, err := firstStore.List(context.Background())
	binding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(stored[0])
	ledger, ledgerPresent, ledgerValid := decodeComputeClaimRecoveryMutation(stored[0])
	if winners != 1 || err != nil || len(stored) != 1 || !bindingPresent || !bindingValid ||
		binding != postgresComputeClaimRecoveryBinding("node-reservation") || !ledgerPresent || !ledgerValid || ledger.State != "node_reserved" ||
		ledger.TencentMutationCount != 1 || ledger.KubernetesMutationCount != 1 || ledger.Evidence.CVM.Attempted != 1 ||
		ledger.Evidence.CVM.Confirmed != 1 || ledger.Evidence.Node.Attempted != 1 || ledger.Evidence.Node.Unknown != 1 {
		t.Fatalf("winners=%d stored=%#v err=%v binding=%#v present=%v valid=%v ledger=%#v ledgerPresent=%v ledgerValid=%v", winners, stored, err, binding, bindingPresent, bindingValid, ledger, ledgerPresent, ledgerValid)
	}
}

func TestPostgresComputeClaimRecoveryReconciliationProvenanceCASHasSingleWinner(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.client.Close()
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()

	operation := postgresComputeClaimOperation("request-hash-reconciliation", "claim_pending")
	originalBinding, present, valid := decodeComputeClaimRecoveryBinding(operation)
	if !present || !valid {
		t.Fatalf("binding=%#v present=%v valid=%v", originalBinding, present, valid)
	}
	originalBinding.RequestHash = strings.Repeat("7", 64)
	operation.RedactedProviderPayload = withComputeClaimRecoveryBinding(operation.RedactedProviderPayload, originalBinding)
	originalLedger := computeClaimRecoveryMutationLedger{
		State: "observed", Reason: "provider_describe", TencentMutationCount: 1, FailureStage: "cvm_tag_readback", ProviderErrorClass: "provider_error",
		Evidence: ComputeClaimEvidence{CVM: ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"opl_account_id"}}},
	}
	operation.RedactedProviderPayload = withComputeClaimRecoveryMutation(operation.RedactedProviderPayload, originalLedger)
	if err := firstStore.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	reconciliation := computeClaimRecoveryReconciliation{
		SchemaVersion: 1, Consumer: "claim_compute_recovery", Generation: "isolated_request_hash_v1", State: "verified",
		BindingDigest: strings.Repeat("a", 64), ExpectedRequestHashDigest: strings.Repeat("b", 64),
		PersistedRequestHashDigest: strings.Repeat("c", 64), MutationLedgerDigest: strings.Repeat("d", 64), AuthorityDigest: strings.Repeat("e", 64),
	}
	verified := operation
	verified.RedactedProviderPayload = withComputeClaimRecoveryReconciliation(verified.RedactedProviderPayload, reconciliation)

	drifted := verified
	driftedReconciliation := reconciliation
	driftedReconciliation.AuthorityDigest = strings.Repeat("f", 64)
	drifted.RedactedProviderPayload = withComputeClaimRecoveryReconciliation(drifted.RedactedProviderPayload, driftedReconciliation)
	if err := firstStore.SaveComputeClaimRecovery(context.Background(), verified, drifted); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("provenance authority drift error=%v, want ErrRuntimeOperationNotCurrent", err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for _, store := range []*PostgresOperationStore{firstStore, secondStore} {
		store := store
		go func() {
			<-start
			results <- store.SaveComputeClaimRecovery(context.Background(), operation, verified)
		}()
	}
	close(start)
	winners := 0
	for range 2 {
		err := <-results
		if err == nil {
			winners++
			continue
		}
		if !errors.Is(err, ErrRuntimeOperationNotCurrent) {
			t.Fatalf("provenance concurrent CAS error=%v", err)
		}
	}
	stored, err := firstStore.List(context.Background())
	binding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(stored[0])
	ledger, ledgerPresent, ledgerValid := decodeComputeClaimRecoveryMutation(stored[0])
	got, reconciliationPresent, reconciliationValid := decodeComputeClaimRecoveryReconciliation(stored[0])
	if winners != 1 || err != nil || len(stored) != 1 || !bindingPresent || !bindingValid || binding != originalBinding ||
		!ledgerPresent || !ledgerValid || !reflect.DeepEqual(ledger, originalLedger) || !reconciliationPresent || !reconciliationValid ||
		!reflect.DeepEqual(got, reconciliation) {
		t.Fatalf("winners=%d stored=%#v err=%v binding=%#v ledger=%#v reconciliation=%#v", winners, stored, err, binding, ledger, got)
	}
}

func postgresComputeClaimOperation(suffix, status string) FabricOperation {
	now := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	operation := newOperation(
		"create_compute_allocation", "compute_allocation", "ca-postgres-"+suffix,
		"acct-postgres-"+suffix, "workspace-postgres-"+suffix, "launch-postgres-"+suffix+":compute",
		"request-hash-"+suffix, now,
	)
	operation.ID = "fop-postgres-compute-claim-" + suffix
	operation.Status = status
	operation.CreatedAt = now
	if status == "failed" {
		operation.FinishedAt = now.Add(time.Second)
	}
	fillOperationResource(&operation, ComputeAllocation{
		ID: operation.ResourceID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
	})
	if status == "claim_pending" {
		operation.RedactedProviderPayload = withComputeClaimRecoveryBinding(operation.RedactedProviderPayload, postgresComputeClaimRecoveryBinding(suffix))
	}
	return operation
}

func postgresComputeClaimRecoveryBinding(suffix string) computeClaimRecoveryBinding {
	return computeClaimRecoveryBinding{
		LaunchOperationID: "launch-postgres-" + suffix,
		IdempotencyKey:    "launch-postgres-" + suffix + ":compute",
		TargetHash:        "target-hash-" + suffix,
		RequestHash:       "claim-request-hash-" + suffix,
	}
}

func TestPostgresOperationStoreReadsExactIdentitiesAndFailsClosedOnDuplicates(t *testing.T) {
	t.Run("action idempotency", func(t *testing.T) {
		store, err := newTestPostgresOperationStore(fabricTestDatabaseURL(t))
		if err != nil {
			t.Fatal(err)
		}
		defer store.client.Close()
		exact := postgresComputeClaimOperation("exact-read", "failed")
		alias := exact
		alias.ID, alias.OperationID, alias.IdempotencyKey = "fop-postgres-alias-read", "op-postgres-alias-read", "launch-postgres-alias:compute"
		for _, operation := range []FabricOperation{exact, alias} {
			if err := store.Append(context.Background(), operation); err != nil {
				t.Fatal(err)
			}
		}
		got, found, err := store.OperationByActionIdempotency(context.Background(), exact.Action, exact.IdempotencyKey)
		if err != nil || !found || got.ID != exact.ID {
			t.Fatalf("exact=%#v found=%v err=%v", got, found, err)
		}
		if missing, found, err := store.OperationByActionIdempotency(context.Background(), exact.Action, "launch-postgres-absent:compute"); err != nil || found || missing.ID != "" {
			t.Fatalf("missing=%#v found=%v err=%v", missing, found, err)
		}
		duplicate := exact
		duplicate.ID, duplicate.OperationID = "fop-postgres-duplicate-read", "op-postgres-duplicate-read"
		if err := store.Append(context.Background(), duplicate); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.OperationByActionIdempotency(context.Background(), exact.Action, exact.IdempotencyKey); !errors.Is(err, ErrOperationIdentityConflict) {
			t.Fatalf("duplicate error=%v", err)
		}
	})

	t.Run("operator approval", func(t *testing.T) {
		store, err := newTestPostgresOperationStore(fabricTestDatabaseURL(t))
		if err != nil {
			t.Fatal(err)
		}
		defer store.client.Close()
		approvalID := "postgres-approval-exact-30970000001"
		exact := postgresComputeClaimOperation("terminal-read", "failed")
		exact.RedactedProviderPayload = map[string]any{
			computeClaimTerminalEvidencePayloadKey: map[string]any{
				"operatorApprovalId": approvalID, "operatorIdempotencyKey": approvalID,
			},
		}
		if err := store.Append(context.Background(), exact); err != nil {
			t.Fatal(err)
		}
		got, found, err := store.ComputeClaimTerminalOperation(context.Background(), approvalID, approvalID)
		if err != nil || !found || got.ID != exact.ID {
			t.Fatalf("exact=%#v found=%v err=%v", got, found, err)
		}
		if missing, found, err := store.ComputeClaimTerminalOperation(context.Background(), "postgres-approval-absent-30970000001", "postgres-approval-absent-30970000001"); err != nil || found || missing.ID != "" {
			t.Fatalf("missing=%#v found=%v err=%v", missing, found, err)
		}
		duplicate := exact
		duplicate.ID, duplicate.OperationID = "fop-postgres-terminal-duplicate", "op-postgres-terminal-duplicate"
		if err := store.Append(context.Background(), duplicate); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.ComputeClaimTerminalOperation(context.Background(), approvalID, approvalID); !errors.Is(err, ErrOperationIdentityConflict) {
			t.Fatalf("duplicate error=%v", err)
		}
	})
}

func TestPostgresOperationStoreBoundsJobHistoryAndOperationPages(t *testing.T) {
	ctx := context.Background()
	store, err := newTestPostgresOperationStore(fabricTestDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.client.Close()

	now := time.Date(2026, 8, 14, 4, 0, 0, 0, time.UTC)
	service := NewServiceWithOperationStore(testProvider{}, store)
	service.now = func() time.Time { return now }
	created, err := service.CreateJob(ctx, JobInput{
		OrganizationID: "org-postgres", WorkspaceID: "workspace-postgres", ProjectID: "project-postgres",
		TaskID: "task-postgres", RequestID: "request-postgres", ApprovalID: "approval-postgres", IdempotencyKey: "job-postgres-bounded",
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := service.ClaimJob(ctx, created.JobID, JobClaimInput{RunnerID: "runner-postgres", IdempotencyKey: "claim-postgres-bounded"})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 50; index++ {
		now = now.Add(time.Second)
		service.now = func() time.Time { return now }
		if _, err := service.HeartbeatJob(ctx, created.JobID, JobHeartbeatInput{
			RunnerID: "runner-postgres", LeaseToken: claimed.LeaseToken, IdempotencyKey: fmt.Sprintf("heartbeat-postgres-%d", index),
		}); err != nil {
			t.Fatalf("heartbeat %d: %v", index, err)
		}
	}
	var jobOperationCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM fabric_operations WHERE resource_kind = 'job' AND resource_id = $1`, created.JobID).Scan(&jobOperationCount); err != nil {
		t.Fatal(err)
	}
	if jobOperationCount != 3 {
		t.Fatalf("job operation count=%d, want create + claim + one heartbeat", jobOperationCount)
	}
	latest, found, err := store.LatestResourceOperation(ctx, "job", created.JobID)
	var latestJob Job
	if err != nil || !found || latest.Action != "heartbeat_job" || !decodeOperationResource(latest, &latestJob) || latestJob.LeaseExpiresAt == nil || !latestJob.LeaseExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("latest=%#v job=%#v found=%v err=%v", latest, latestJob, found, err)
	}

	claim, found, err := store.OperationByResourceActionIdempotency(ctx, "job", created.JobID, "claim_job", "claim-postgres-bounded")
	if err != nil || !found || claim.Action != "claim_job" {
		t.Fatalf("claim=%#v found=%v err=%v", claim, found, err)
	}
	duplicateClaim := claim
	duplicateClaim.ID, duplicateClaim.OperationID = "fop-postgres-duplicate-job-claim", "op-postgres-duplicate-job-claim"
	duplicateClaim.CreatedAt = now.Add(time.Second)
	if err := store.Append(ctx, duplicateClaim); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.OperationByResourceActionIdempotency(ctx, "job", created.JobID, "claim_job", "claim-postgres-bounded"); !errors.Is(err, ErrOperationIdentityConflict) {
		t.Fatalf("duplicate resource identity error=%v", err)
	}

	for index := 0; index < 2; index++ {
		operation := newOperation(
			"create_workspace_runtime", "workspace_runtime", "workspace-runtime-postgres", "acct-postgres", "workspace-runtime-postgres",
			fmt.Sprintf("runtime-postgres-%d", index), fmt.Sprintf("runtime-hash-%d", index), now.Add(time.Duration(index+2)*time.Second),
		)
		operation.ID = fmt.Sprintf("fop-runtime-postgres-%d", index)
		operation.Status = "succeeded"
		operation.CreatedAt = operation.StartedAt
		fillOperationResource(&operation, WorkspaceRuntime{ID: fmt.Sprintf("runtime-postgres-%d", index), WorkspaceID: operation.WorkspaceID, OperationID: operation.IdempotencyKey})
		if err := store.Append(ctx, operation); err != nil {
			t.Fatal(err)
		}
	}
	candidates, err := store.WorkspaceRuntimeIdentityCandidates(ctx, "workspace-runtime-postgres")
	if err != nil || len(candidates) != 2 {
		t.Fatalf("runtime candidates=%#v err=%v", candidates, err)
	}

	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := store.ListPage(ctx, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, operation := range page.Operations {
			if seen[operation.ID] {
				t.Fatalf("operation %q appeared in multiple pages", operation.ID)
			}
			seen[operation.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			t.Fatalf("cursor did not advance: %q", cursor)
		}
		cursor = page.NextCursor
	}
	var totalOperationCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM fabric_operations`).Scan(&totalOperationCount); err != nil {
		t.Fatal(err)
	}
	if len(seen) != totalOperationCount {
		t.Fatalf("paged operations=%d total=%d", len(seen), totalOperationCount)
	}
}

func TestPostgresOperationStoreRunsEmbeddedMigrationsOnce(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	first, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.client.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var migrationCount int
	if err := db.QueryRow(`SELECT count(*) FROM opl_schema_migrations WHERE service = 'fabric'`).Scan(&migrationCount); err != nil {
		t.Fatalf("read Fabric migration journal: %v", err)
	}
	if migrationCount != 7 {
		t.Fatalf("Fabric migration count = %d, want 7", migrationCount)
	}
	if _, err := db.Exec(`DROP TABLE machine_ownerships`); err != nil {
		t.Fatal(err)
	}
	second, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.client.Close(); err != nil {
		t.Fatal(err)
	}
	var table sql.NullString
	if err := db.QueryRow(`SELECT to_regclass('machine_ownerships')`).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if table.Valid {
		t.Fatal("second Fabric startup repeated embedded DDL")
	}
}

func fabricTestDatabaseURL(t *testing.T) string {
	t.Helper()
	databaseURL := os.Getenv("FABRIC_TEST_DATABASE_URL")
	optional := false
	if databaseURL == "" {
		if os.Getenv("OPL_POSTGRES_TESTS") == "1" {
			databaseURL = "connect_timeout=10"
		} else {
			databaseURL = "host=/var/run/postgresql dbname=postgres sslmode=disable"
			optional = true
		}
	}
	admin, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.Ping(); err != nil {
		_ = admin.Close()
		if optional {
			t.Skipf("local PostgreSQL unavailable: %v", err)
		}
		t.Fatal(err)
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	schema := "fabric_test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	if parsed, err := url.Parse(databaseURL); err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema)
		query.Set("connect_timeout", "5")
		query.Set("statement_timeout", "10000")
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return databaseURL + " search_path=" + schema + " connect_timeout=5 statement_timeout=10000"
}
