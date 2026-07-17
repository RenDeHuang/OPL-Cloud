package fabric

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
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
			currentStore, err := NewPostgresOperationStore(databaseURL)
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
	firstStore, err := NewPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatalf("open first operation store: %v", err)
	}
	defer firstStore.client.Close()
	secondStore, err := NewPostgresOperationStore(databaseURL)
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
	firstStore, err = NewPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatalf("open first operation store: %v", err)
	}
	secondStore, err = NewPostgresOperationStore(databaseURL)
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

func TestPostgresDestroyRuntimeFailedRetryBindsWorkspaceAcrossServiceInstances(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	firstStore, err := NewPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatalf("open first operation store: %v", err)
	}
	defer firstStore.client.Close()
	secondStore, err := NewPostgresOperationStore(databaseURL)
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

func TestPostgresOperationStoreRunsEmbeddedMigrationsOnce(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	first, err := NewPostgresOperationStore(databaseURL)
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
	if migrationCount != 4 {
		t.Fatalf("Fabric migration count = %d, want 4", migrationCount)
	}
	if _, err := db.Exec(`DROP TABLE machine_ownerships`); err != nil {
		t.Fatal(err)
	}
	second, err := NewPostgresOperationStore(databaseURL)
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
