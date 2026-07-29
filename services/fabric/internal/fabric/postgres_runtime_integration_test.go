package fabric

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	calls        atomic.Int32
	firstEntered chan struct{}
	releaseFirst chan struct{}
}

func (p *stalePostgresRuntimeProvider) CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, _ ComputeAllocation, _ StorageVolume) (WorkspaceRuntime, error) {
	if p.calls.Add(1) == 1 {
		close(p.firstEntered)
		select {
		case <-p.releaseFirst:
		case <-ctx.Done():
			return WorkspaceRuntime{}, ctx.Err()
		}
	}
	return WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: input.WorkspaceID, Status: "running", Ready: true, ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey)}, nil
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

	provider := &stalePostgresRuntimeProvider{firstEntered: make(chan struct{}), releaseFirst: make(chan struct{})}
	firstService := runtimeTestService(provider, firstStore)
	secondService := runtimeTestService(provider, secondStore)
	startedAt := time.Date(2026, 7, 17, 0, 0, 0, 123456000, time.UTC)
	var clock atomic.Int64
	clock.Store(startedAt.UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	firstService.now = now
	secondService.now = now
	input := runtimeTestInput("postgres-runtime-stale")

	oldOwnerDone := make(chan error, 1)
	go func() {
		_, err := firstService.CreateWorkspaceRuntime(ctx, input)
		oldOwnerDone <- err
	}()
	select {
	case <-provider.firstEntered:
	case <-ctx.Done():
		t.Fatal("old owner provider call did not start")
	}
	operations, err := firstStore.List(ctx)
	if err != nil || len(operations) != 1 || operations[0].Status != "started" {
		t.Fatalf("persisted old claim=%#v err=%v", operations, err)
	}
	clock.Store(operations[0].StartedAt.Add(3 * time.Minute).UnixNano())

	type callResult struct {
		runtime WorkspaceRuntime
		err     error
	}
	start := make(chan struct{})
	results := make(chan callResult, 2)
	for _, service := range []*Service{firstService, secondService} {
		service := service
		go func() {
			<-start
			runtime, err := service.CreateWorkspaceRuntime(ctx, input)
			results <- callResult{runtime: runtime, err: err}
		}()
	}
	close(start)
	firstResult, secondResult := <-results, <-results
	for _, result := range []callResult{firstResult, secondResult} {
		if result.err != nil && !errors.Is(result.err, ErrRuntimeOperationInProgress) {
			t.Fatalf("stale caller result=%#v err=%v", result.runtime, result.err)
		}
	}
	if provider.calls.Load() != 2 {
		t.Fatalf("provider calls after stale race=%d, want old owner plus one reclaim", provider.calls.Load())
	}

	close(provider.releaseFirst)
	if err := <-oldOwnerDone; !errors.Is(err, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("old owner completion error=%v, want ErrRuntimeOperationNotCurrent", err)
	}
	firstReplay, firstErr := firstService.CreateWorkspaceRuntime(ctx, input)
	secondReplay, secondErr := secondService.CreateWorkspaceRuntime(ctx, input)
	if firstErr != nil || secondErr != nil || firstReplay.ID != "runtime-alpha" || secondReplay.ID != firstReplay.ID || secondReplay.Status != firstReplay.Status || provider.calls.Load() != 2 {
		t.Fatalf("final replays first=%#v err=%v second=%#v err=%v providerCalls=%d", firstReplay, firstErr, secondReplay, secondErr, provider.calls.Load())
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
	waitForOperation(t, secondService, "create_compute_allocation", "compute_allocation", first.ID, "succeeded")
	if _, err := firstService.CreateComputeAllocation(ctx, secondInput); err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, firstService, "create_compute_allocation", "compute_allocation", second.ID, "succeeded")
	if targets, ambiguous := provider.allocationEvidence("np-basic"); !reflect.DeepEqual(targets, []int64{1, 2}) || ambiguous != 0 {
		t.Fatalf("PostgreSQL scale targets=%v ambiguous=%d", targets, ambiguous)
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
			binding.IdempotencyKey = "launch-postgres-binding:compute-claim-other"
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

func TestPostgresComputeClaimRecoveryOwnershipCASActivatesOnlySameTarget(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	store, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.client.Close()

	ownership := postgresComputeClaimOwnership("identity")
	stored, created, err := store.ClaimMachine(context.Background(), ownership)
	if err != nil || !created {
		t.Fatalf("seed ownership=%#v created=%v err=%v", stored, created, err)
	}
	target := stored
	target.Status = "active"
	target.ReleasedAt = nil
	if err := store.ActivateComputeClaimRecoveryOwnership(context.Background(), target); err != nil {
		t.Fatalf("quarantined -> active: %v", err)
	}
	active, err := store.MachineOwnership(context.Background(), target.ResourceID)
	if err != nil || active.Status != "active" || !sameComputeClaimRecoveryOwnership(active, target) {
		t.Fatalf("active ownership=%#v err=%v", active, err)
	}
	if err := store.ActivateComputeClaimRecoveryOwnership(context.Background(), target); err != nil {
		t.Fatalf("active replay: %v", err)
	}

	drifts := map[string]func(*MachineOwnership){
		"account":   func(value *MachineOwnership) { value.AccountID = "acct-other" },
		"workspace": func(value *MachineOwnership) { value.WorkspaceID = "workspace-other" },
		"package":   func(value *MachineOwnership) { value.PackageID = "pro" },
		"pool":      func(value *MachineOwnership) { value.NodePoolID = "np-other" },
		"machine":   func(value *MachineOwnership) { value.MachineID = "machine-other" },
		"node":      func(value *MachineOwnership) { value.NodeName = "node-other" },
		"cvm":       func(value *MachineOwnership) { value.InstanceID = "ins-other" },
	}
	for name, drift := range drifts {
		t.Run(name, func(t *testing.T) {
			conflict := target
			drift(&conflict)
			if err := store.ActivateComputeClaimRecoveryOwnership(context.Background(), conflict); !errors.Is(err, ErrMachineOwnershipConflict) {
				t.Fatalf("identity drift error=%v, want ErrMachineOwnershipConflict", err)
			}
			current, err := store.MachineOwnership(context.Background(), target.ResourceID)
			if err != nil || !reflect.DeepEqual(current, active) {
				t.Fatalf("identity drift changed ownership: current=%#v active=%#v err=%v", current, active, err)
			}
		})
	}
}

func TestPostgresComputeClaimRecoveryOwnershipCASRejectsNonRecoverableStatus(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	store, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.client.Close()

	ownership := postgresComputeClaimOwnership("status")
	ownership.Status = "claimed"
	stored, created, err := store.ClaimMachine(context.Background(), ownership)
	if err != nil || !created {
		t.Fatalf("seed ownership=%#v created=%v err=%v", stored, created, err)
	}
	target := stored
	target.Status = "active"
	if err := store.ActivateComputeClaimRecoveryOwnership(context.Background(), target); !errors.Is(err, ErrMachineOwnershipConflict) {
		t.Fatalf("claimed -> active error=%v, want ErrMachineOwnershipConflict", err)
	}
	current, err := store.MachineOwnership(context.Background(), target.ResourceID)
	if err != nil || current.Status != "claimed" {
		t.Fatalf("non-recoverable status changed ownership: current=%#v err=%v", current, err)
	}
}

type postgresComputeClaimRecoveryProvider struct {
	fakeComputeClaimRecoveryProvider
	storageCreates atomic.Int32
}

func (p *postgresComputeClaimRecoveryProvider) CreateStorageVolume(_ context.Context, input StorageVolumeInput) (StorageVolume, error) {
	p.storageCreates.Add(1)
	return StorageVolume{
		ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", Provider: "tencent-tke",
		ProviderResourceID: "disk-postgres-fixture", ProviderRequestID: providerRequestID("storage", input.IdempotencyKey),
		Zone: input.Zone, SizeGB: input.SizeGB,
	}, nil
}

func TestPostgresComputeClaimRecoveryRestartAfterActiveOwnershipSkipsProviderMutation(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.client.Close()
	provider, input, claimInput := seedPostgresActiveComputeClaimRecovery(t, firstStore, "pro")
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()

	restarted := NewServiceWithOperationStore(provider, secondStore)
	result, err := restarted.ClaimComputeRecovery(context.Background(), claimInput)
	operations, operationsErr := secondStore.List(context.Background())
	if err != nil || operationsErr != nil || !result.Eligible || result.TencentMutationCount != 0 || result.KubernetesMutationCount != 0 ||
		provider.proofCalls != 1 || provider.claimCalls != 0 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCreates.Load() != 0 {
		t.Fatalf("restart result=%#v err=%v operationsErr=%v provider=%#v storageCreates=%d", result, err, operationsErr, provider, provider.storageCreates.Load())
	}
	assertRecoveredComputeOperation(t, operations, input, "succeeded")
	ownership, err := secondStore.MachineOwnership(context.Background(), input.ComputeAllocationID)
	if err != nil || ownership.Status != "active" {
		t.Fatalf("restart ownership=%#v err=%v", ownership, err)
	}
}

func TestPostgresComputeClaimRecoveryRestartAfterTargetOwnedReadbackConvergesLocalStateOnly(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	provider, input, claimInput := seedPostgresComputeClaimRecovery(t, firstStore, "basic", false)
	if err := firstStore.client.Close(); err != nil {
		t.Fatal(err)
	}
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()

	result, err := NewServiceWithOperationStore(provider, secondStore).ClaimComputeRecovery(context.Background(), claimInput)
	operations, operationsErr := secondStore.List(context.Background())
	ownership, ownershipErr := secondStore.MachineOwnership(context.Background(), input.ComputeAllocationID)
	if err != nil || operationsErr != nil || ownershipErr != nil || !result.Eligible || ownership.Status != "active" ||
		result.TencentMutationCount != 0 || result.KubernetesMutationCount != 0 || provider.proofCalls != 1 || provider.claimCalls != 0 ||
		provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCreates.Load() != 0 {
		t.Fatalf("restart result=%#v err=%v operationsErr=%v ownership=%#v ownershipErr=%v provider=%#v storageCreates=%d", result, err, operationsErr, ownership, ownershipErr, provider, provider.storageCreates.Load())
	}
	assertRecoveredComputeOperation(t, operations, input, "succeeded")
}

func TestPostgresComputeClaimRecoveryFailureReplayDoesNotMutateAcrossServiceInstances(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	provider, _, claimInput := seedPostgresComputeClaimRecovery(t, firstStore, "basic", false)
	provider.proof.NodeOwnershipState = "unallocated"
	provider.proof.CVMOwnershipState = "recoverable"
	provider.claim.Proof = provider.proof
	provider.claim.Proof.Reason = "provider_describe"
	provider.claim.TencentMutationCount = 1
	provider.claim.KubernetesMutationCount = 0
	provider.claim.FailureStage = "cvm_tag_readback"
	provider.claim.ProviderErrorClass = "timeout"
	provider.claim.Evidence = &ComputeClaimEvidence{
		CVM:  ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"opl_workspace_id"}},
		Node: ComputeClaimMutationEvidence{},
	}
	provider.claimErr = errors.New("provider readback timed out")

	first, firstErr := NewServiceWithOperationStore(provider, firstStore).ClaimComputeRecovery(context.Background(), claimInput)
	if closeErr := firstStore.client.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()
	provider.proofErr = errors.New("provider proof unavailable")
	second, secondErr := NewServiceWithOperationStore(provider, secondStore).ClaimComputeRecovery(context.Background(), claimInput)

	if firstErr == nil || secondErr == nil || first.Eligible || second.Eligible || provider.claimCalls != 1 || provider.proofCalls != 2 {
		t.Fatalf("first=%#v firstErr=%v second=%#v secondErr=%v provider=%#v", first, firstErr, second, secondErr, provider)
	}
	if second.Reason != first.Reason || second.TencentMutationCount != first.TencentMutationCount ||
		second.KubernetesMutationCount != first.KubernetesMutationCount || second.FailureStage != first.FailureStage ||
		second.ProviderErrorClass != first.ProviderErrorClass || !reflect.DeepEqual(second.Evidence, first.Evidence) {
		t.Fatalf("PostgreSQL replay changed persisted mutation proof: first=%#v second=%#v", first, second)
	}
}

func TestPostgresComputeClaimRecoveryObservedSuccessReadbackRegressionFailsClosedAcrossRestart(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	provider, input, claimInput := seedPostgresComputeClaimRecovery(t, firstStore, "basic", false)
	provider.proof.NodeOwnershipState = "unallocated"
	provider.proof.CVMOwnershipState = "recoverable"
	activationStore := &failOnceComputeClaimActivationStore{OperationStore: firstStore, fail: true}

	first, firstErr := NewServiceWithOperationStore(provider, activationStore).ClaimComputeRecovery(context.Background(), claimInput)
	if closeErr := firstStore.client.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()
	second, secondErr := NewServiceWithOperationStore(provider, secondStore).ClaimComputeRecovery(context.Background(), claimInput)
	ownership, ownershipErr := secondStore.MachineOwnership(context.Background(), input.ComputeAllocationID)

	if firstErr == nil || secondErr == nil || first.TencentMutationCount != 1 || first.KubernetesMutationCount != 1 ||
		second.Eligible || second.Reason != "identity_mismatch" || second.FailureStage != "claim_final_readback" ||
		second.ProviderErrorClass != "readback_mismatch" || second.TencentMutationCount != 1 || second.KubernetesMutationCount != 1 ||
		ownershipErr != nil || ownership.Status != "quarantined" || provider.proofCalls != 2 || provider.claimCalls != 1 {
		t.Fatalf("first=%#v firstErr=%v second=%#v secondErr=%v ownership=%#v ownershipErr=%v provider=%#v", first, firstErr, second, secondErr, ownership, ownershipErr, provider)
	}
}

func TestPostgresComputeClaimRecoveryStorageIdentityCreatesCBSOnceAcrossRestart(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.client.Close()
	provider, input, claimInput := seedPostgresActiveComputeClaimRecovery(t, firstStore, "basic")
	firstService := NewServiceWithOperationStore(provider, firstStore)
	if result, err := firstService.ClaimComputeRecovery(context.Background(), claimInput); err != nil || !result.Eligible {
		t.Fatalf("claim recovery result=%#v err=%v", result, err)
	}
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()
	secondService := NewServiceWithOperationStore(provider, secondStore)
	storageInput := StorageVolumeInput{
		ID: input.StorageVolumeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID,
		ComputeID: input.ComputeAllocationID, Zone: claimInput.Zone, SizeGB: 10,
		IdempotencyKey: input.LaunchOperationID + ":storage",
	}

	start := make(chan struct{})
	type storageResult struct {
		volume StorageVolume
		err    error
	}
	results := make(chan storageResult, 2)
	for _, service := range []*Service{firstService, secondService} {
		service := service
		go func() {
			<-start
			volume, err := service.CreateStorageVolume(context.Background(), storageInput)
			results <- storageResult{volume: volume, err: err}
		}()
	}
	close(start)
	for range 2 {
		result := <-results
		if result.err != nil || result.volume.ID != input.StorageVolumeID || result.volume.ProviderResourceID != "disk-postgres-fixture" {
			t.Fatalf("storage result=%#v err=%v", result.volume, result.err)
		}
	}
	thirdStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer thirdStore.client.Close()
	replayed, err := NewServiceWithOperationStore(provider, thirdStore).CreateStorageVolume(context.Background(), storageInput)
	if err != nil || replayed.ID != input.StorageVolumeID || provider.storageCreates.Load() != 1 {
		t.Fatalf("storage restart replay=%#v err=%v providerCreates=%d", replayed, err, provider.storageCreates.Load())
	}
	operations, err := thirdStore.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	started, succeeded := 0, 0
	for _, operation := range operations {
		if operation.Action != "create_storage_volume" {
			continue
		}
		if operation.ResourceID != input.StorageVolumeID || operation.IdempotencyKey != input.LaunchOperationID+":storage" {
			t.Fatalf("unexpected storage identity: %#v", operation)
		}
		switch operation.Status {
		case "started":
			started++
		case "succeeded":
			succeeded++
		}
	}
	if started != 1 || succeeded != 1 {
		t.Fatalf("storage transitions started=%d succeeded=%d operations=%#v", started, succeeded, operations)
	}
}

func seedPostgresActiveComputeClaimRecovery(t *testing.T, store *PostgresOperationStore, packageID string) (*postgresComputeClaimRecoveryProvider, ComputeClaimRecoveryInput, ComputeClaimRecoveryClaimInput) {
	return seedPostgresComputeClaimRecovery(t, store, packageID, true)
}

func seedPostgresComputeClaimRecovery(t *testing.T, store *PostgresOperationStore, packageID string, activateOwnership bool) (*postgresComputeClaimRecoveryProvider, ComputeClaimRecoveryInput, ComputeClaimRecoveryClaimInput) {
	t.Helper()
	_, memoryStore, fixtureProvider, input := seedComputeClaimRecovery(t, packageID)
	operations, err := memoryStore.List(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("fixture operations=%#v err=%v", operations, err)
	}
	operation := operations[0]
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	provider := &postgresComputeClaimRecoveryProvider{fakeComputeClaimRecoveryProvider: *fixtureProvider}
	provider.proof.NodeOwnershipState = "target_owned"
	provider.proof.CVMOwnershipState = "target_owned"
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: provider.proof.MachineName, NodeName: provider.proof.NodeName,
		CVMInstanceID: provider.proof.CVMInstanceID, PrivateIP: provider.proof.PrivateIP,
		InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute-claim",
	}
	pending := operation
	pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
	pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, newComputeClaimRecoveryBinding(claimInput))
	if err := store.SaveComputeClaimRecovery(context.Background(), operation, pending); err != nil {
		t.Fatal(err)
	}
	ownership, err := memoryStore.MachineOwnership(context.Background(), input.ComputeAllocationID)
	if err != nil {
		t.Fatal(err)
	}
	stored, created, err := store.ClaimMachine(context.Background(), ownership)
	if err != nil || !created {
		t.Fatalf("seed PostgreSQL ownership=%#v created=%v err=%v", stored, created, err)
	}
	if activateOwnership {
		stored.Status, stored.ReleasedAt = "active", nil
		if err := store.ActivateComputeClaimRecoveryOwnership(context.Background(), stored); err != nil {
			t.Fatal(err)
		}
	}
	return provider, input, claimInput
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
		IdempotencyKey:    "launch-postgres-" + suffix + ":compute-claim",
		TargetHash:        "target-hash-" + suffix,
		RequestHash:       "claim-request-hash-" + suffix,
	}
}

func postgresComputeClaimOwnership(suffix string) MachineOwnership {
	return MachineOwnership{
		ID: "owner-postgres-" + suffix, ResourceID: "ca-postgres-" + suffix,
		AccountID: "acct-postgres-" + suffix, WorkspaceID: "workspace-postgres-" + suffix,
		PackageID: "basic", NodePoolID: "np-postgres-basic", MachineID: "machine-postgres-" + suffix,
		InstanceID: "ins-postgres-" + suffix, NodeName: "node-postgres-" + suffix, Status: "quarantined",
		ProviderRequestID: "redacted-provider-reference", ClaimedAt: time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC),
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
	if migrationCount != 6 {
		t.Fatalf("Fabric migration count = %d, want 6", migrationCount)
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
