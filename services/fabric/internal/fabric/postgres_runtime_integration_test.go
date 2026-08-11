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
	calls         atomic.Int32
	readbackCalls atomic.Int32
	readback      WorkspaceRuntime
}

func TestPostgresWorkspaceComputeBindingPersistsBeforeProviderWriteAcrossProcessRestart(t *testing.T) {
	for _, packageID := range []string{"basic", "pro"} {
		t.Run(packageID, func(t *testing.T) {
			databaseURL := fabricTestDatabaseURL(t)
			firstStore, err := newTestPostgresOperationStore(databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			gate := newNormalLaunchProviderWriteGate()
			provider := &journaledNormalLaunchComputeProvider{normalLaunchComputeProvider: &normalLaunchComputeProvider{createResultErr: ErrComputeAllocationPending, createGate: gate}}
			launchID := "workspace-launch-" + packageID + "-binding"
			input := ComputeAllocationInput{
				AccountID: "acct-" + packageID, WorkspaceID: "workspace-" + packageID,
				PackageID: packageID, NodePoolID: "np-" + packageID, IdempotencyKey: launchID + ":compute",
			}
			input.LaunchBinding = normalWorkspaceComputeBinding(input, launchID)
			first := NewServiceWithOperationStore(provider, firstStore)
			configureFastComputeAllocationPolling(first, 250*time.Millisecond)
			allocation, err := first.CreateComputeAllocation(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-gate.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("provider write was not reached")
			}
			beforeWrite, getErr := firstStore.Get(context.Background(), input.LaunchBinding.FabricOperationID)
			persisted, bindingOK := decodeLaunchStageBinding(beforeWrite)
			if getErr != nil || beforeWrite.Status != "started" || !bindingOK || persisted != *input.LaunchBinding {
				t.Fatalf("persist-before-write operation=%#v binding=%#v/%v err=%v", beforeWrite, persisted, bindingOK, getErr)
			}
			if err := firstStore.client.Close(); err != nil {
				t.Fatal(err)
			}
			close(gate.release)
			waitForComputeReconcileIdle(t, first, allocation.ID)

			reopenedStore, err := newTestPostgresOperationStore(databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopenedStore.client.Close() })
			afterRestart, getErr := reopenedStore.Get(context.Background(), input.LaunchBinding.FabricOperationID)
			persisted, bindingOK = decodeLaunchStageBinding(afterRestart)
			if getErr != nil || !bindingOK || persisted != *input.LaunchBinding {
				t.Fatalf("restart operation=%#v binding=%#v/%v err=%v", afterRestart, persisted, bindingOK, getErr)
			}
			if createCalls, _, _, _, _, _ := provider.counts(); createCalls != 1 {
				t.Fatalf("provider create calls=%d, want 1", createCalls)
			}
		})
	}
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

func waitForPostgresComputeLeaseExpiry(t *testing.T, store *PostgresOperationStore, resourceID string) {
	t.Helper()
	var remainingMilliseconds int64
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT COALESCE(CEIL(EXTRACT(EPOCH FROM GREATEST(
			compute_pool_lease_expires_at - clock_timestamp(), interval '0 seconds'
		)) * 1000), 0)::bigint
		FROM fabric_operations
		WHERE action = 'create_compute_allocation' AND resource_id = $1`, resourceID).Scan(&remainingMilliseconds); err != nil {
		t.Fatal(err)
	}
	if remainingMilliseconds > 0 {
		timer := time.NewTimer(time.Duration(remainingMilliseconds+10) * time.Millisecond)
		defer timer.Stop()
		<-timer.C
	}
}

func TestPostgresNormalWorkspaceCBSResponseLossConvergesAcrossProcessRestart(t *testing.T) {
	for _, test := range []struct {
		packageID string
		sizeGB    int
	}{
		{packageID: "basic", sizeGB: 10},
		{packageID: "pro", sizeGB: 100},
	} {
		t.Run(test.packageID, func(t *testing.T) {
			databaseURL := fabricTestDatabaseURL(t)
			firstStore, err := newTestPostgresOperationStore(databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			gate := newNormalLaunchProviderWriteGate()
			provider := &normalLaunchStorageProvider{cbsCreateGate: gate}
			first := NewServiceWithOperationStore(provider, firstStore)
			computeInput := ComputeAllocationInput{
				AccountID: "acct-" + test.packageID, WorkspaceID: "workspace-" + test.packageID,
				PackageID: test.packageID, NodePoolID: "np-" + test.packageID,
				IdempotencyKey: "workspace-launch-" + test.packageID + ":compute",
			}
			compute, err := first.CreateComputeAllocation(context.Background(), computeInput)
			if err != nil {
				t.Fatal(err)
			}
			waitForOperation(t, first, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")
			input := StorageVolumeInput{
				ID: "storage-" + test.packageID, AccountID: computeInput.AccountID, WorkspaceID: computeInput.WorkspaceID,
				ComputeID: compute.ID, Zone: "ap-guangzhou-3", SizeGB: test.sizeGB,
				IdempotencyKey: "workspace-launch-" + test.packageID + ":storage",
			}
			input.LaunchBinding = normalWorkspaceStorageBinding(input, "workspace-launch-"+test.packageID)
			type storageResult struct {
				volume StorageVolume
				err    error
			}
			result := make(chan storageResult, 1)
			go func() {
				volume, createErr := first.CreateStorageVolume(context.Background(), input)
				result <- storageResult{volume: volume, err: createErr}
			}()
			select {
			case <-gate.entered:
			case <-time.After(time.Second):
				t.Fatal("CreateDisks-equivalent provider write was not reached")
			}
			childID := providerMutationOperationID(*input.LaunchBinding, "cbs_create", "storage_volume", input.ID, input.ExpectedProviderResourceID)
			child, childErr := firstStore.Get(context.Background(), childID)
			if childErr != nil || child.Status != "started" {
				t.Fatalf("persist-before-write child=%#v err=%v", child, childErr)
			}
			operations, err := firstStore.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range operations {
				if operation.Action != "cbs_create" {
					continue
				}
				var persisted StorageVolume
				if !decodeOperationResource(operation, &persisted) || persisted.ID != input.ID || persisted.ProviderResourceID != "" {
					t.Fatalf("pre-restart CBS identity=%#v operation=%#v", persisted, operation)
				}
			}
			if err := firstStore.client.Close(); err != nil {
				t.Fatal(err)
			}
			close(gate.release)
			if firstResult := <-result; firstResult.err == nil {
				t.Fatalf("closed store unexpectedly persisted CBS result=%#v", firstResult.volume)
			}

			provider.mu.Lock()
			provider.cbsCreateGate = nil
			provider.mu.Unlock()
			reopenedStore, err := newTestPostgresOperationStore(databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopenedStore.client.Close() })
			restarted := NewServiceWithOperationStore(provider, reopenedStore)
			volume, err := restarted.CreateStorageVolume(context.Background(), input)
			if err != nil || volume.Status != "ready" || !strings.HasPrefix(volume.ProviderResourceID, "disk-") {
				t.Fatalf("CBS replay=%#v err=%v", volume, err)
			}
			cbsCreate, cbsReadback, bindingApply, bindingReadback := provider.storageCounts()
			if cbsCreate != 1 || cbsReadback != 1 || bindingApply != 1 || bindingReadback != 0 {
				t.Fatalf("provider calls CreateCBS=%d ReadCBS=%d ApplyBinding=%d ReadBinding=%d", cbsCreate, cbsReadback, bindingApply, bindingReadback)
			}
			assertNormalLaunchStageOperation(t, reopenedStore, "cbs_create", input, volume, "succeeded")
			assertNormalLaunchStageOperation(t, reopenedStore, "static_binding_apply", input, volume, "succeeded")
		})
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
