package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const (
	capacityAccountCount  = 1000
	capacityConsoleUsers  = 100
	capacityCommands      = 20
	capacityRequestBudget = 5 * time.Second
)

type capacityLedger struct {
	fakeLedgerClient
	mu       sync.Mutex
	receipts map[string]string
}

func (l *capacityLedger) RecordReceipt(_ context.Context, _ clients.ReceiptInput, key string) (clients.Receipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if id := l.receipts[key]; id != "" {
		return clients.Receipt{ReceiptID: id}, nil
	}
	id := "receipt-" + stableID(key)[:18]
	l.receipts[key] = id
	return clients.Receipt{ReceiptID: id}, nil
}

func (l *capacityLedger) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.receipts)
}

type capacityFabric struct {
	fakeFabricClient
	mu      sync.Mutex
	creates map[string]string
}

func (f *capacityFabric) CreateComputeAllocation(_ context.Context, input clients.ComputeAllocationInput, key string) (clients.ComputeAllocation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id := f.creates[key]; id != "" && id != input.ID {
		return clients.ComputeAllocation{}, fmt.Errorf("fabric idempotency conflict: %s", key)
	}
	f.creates[key] = input.ID
	return clients.ComputeAllocation{
		ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID,
		Status: "running", Provider: "capacity-fake", ProviderResourceID: "ins-" + input.ID, ProviderRequestID: "req-" + input.ID,
	}, nil
}

func (f *capacityFabric) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.creates)
}

type capacitySub2API struct {
	*testSub2APIClient
	keyMu       sync.Mutex
	keys        map[int64]clients.Sub2APIWorkspaceKey
	keyByIntent map[string]int64
	nextKeyID   int64
}

func (c *capacitySub2API) UserKeys(ctx context.Context, credential clients.SessionDelegatedCredential, userID int64) ([]clients.Sub2APIWorkspaceKey, error) {
	if credential.Bearer != "test-user-delegated-token" {
		return nil, errors.New("wrong delegated credential")
	}
	c.keyMu.Lock()
	defer c.keyMu.Unlock()
	keys := make([]clients.Sub2APIWorkspaceKey, 0, len(c.keys)+1)
	legacy, err := c.WorkspaceKey(ctx, userID)
	if err == nil {
		keys = append(keys, legacy)
	}
	for _, key := range c.keys {
		if key.UserID == userID {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (c *capacitySub2API) UserKey(ctx context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64) (clients.Sub2APIWorkspaceKey, error) {
	keys, err := c.UserKeys(ctx, credential, userID)
	if err != nil {
		return clients.Sub2APIWorkspaceKey{}, err
	}
	for _, key := range keys {
		if key.ID == keyID {
			return key, nil
		}
	}
	return clients.Sub2APIWorkspaceKey{}, clients.ErrSub2APIKeyNotFound
}

func (c *capacitySub2API) CreateUserKey(_ context.Context, credential clients.SessionDelegatedCredential, userID int64, input clients.Sub2APICreateKeyInput, idempotencyKey string) (clients.Sub2APIWorkspaceKey, error) {
	if credential.Bearer != "test-user-delegated-token" || idempotencyKey == "" || !strings.HasPrefix(input.Name, "opl-workspace-") {
		return clients.Sub2APIWorkspaceKey{}, errors.New("invalid Workspace Key create")
	}
	c.keyMu.Lock()
	defer c.keyMu.Unlock()
	if keyID := c.keyByIntent[idempotencyKey]; keyID > 0 {
		return c.keys[keyID], nil
	}
	c.nextKeyID++
	key := clients.Sub2APIWorkspaceKey{ID: c.nextKeyID, UserID: userID, Name: input.Name, Key: "capacity-workspace-key-secret", Status: "active"}
	c.keys[key.ID], c.keyByIntent[idempotencyKey] = key, key.ID
	return key, nil
}

func (*capacitySub2API) UpdateUserKey(context.Context, clients.SessionDelegatedCredential, int64, int64, clients.Sub2APIUpdateKeyInput) (clients.Sub2APIWorkspaceKey, error) {
	return clients.Sub2APIWorkspaceKey{}, errors.New("unexpected Workspace Key update")
}

func (*capacitySub2API) DeleteUserKey(context.Context, clients.SessionDelegatedCredential, int64, int64) error {
	return errors.New("unexpected Workspace Key delete")
}

func (c *capacitySub2API) Keys(ctx context.Context, userID int64) ([]clients.Sub2APIWorkspaceKey, error) {
	return c.UserKeys(ctx, clients.SessionDelegatedCredential{Bearer: "test-user-delegated-token"}, userID)
}

func newCapacitySub2API() *capacitySub2API {
	return &capacitySub2API{
		testSub2APIClient: &testSub2APIClient{balance: 100_000_000_000_000, charges: map[string]int64{}},
		keys:              map[int64]clients.Sub2APIWorkspaceKey{},
		keyByIntent:       map[string]int64{},
		nextKeyID:         18,
	}
}

type capacityCall struct {
	duration time.Duration
	status   int
	body     string
	err      error
}

type capacityProcessSample struct {
	cpu       time.Duration
	heapBytes uint64
	sysBytes  uint64
}

type capacityConnectionSample struct {
	max int
	err error
}

func TestSinglePodCapacity(t *testing.T) {
	if os.Getenv("OPL_CAPACITY_TESTS") != "1" {
		t.Skip("set OPL_CAPACITY_TESTS=1 to run the isolated single-Pod load test")
	}
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	admin := openControlPlaneTestPostgres(t)
	schema := fmt.Sprintf("control_plane_capacity_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	applicationName := "opl_capacity_" + stableID(schema)[:12]
	databaseURL := controlPlaneTestPostgresURL(t, "postgres", schema) + " application_name=" + applicationName
	stateStore, err := newTestPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	store := stateStore.(*postgresEntStateStore)
	t.Cleanup(func() { _ = store.client.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	seedStarted := time.Now()
	for index := 0; index < capacityAccountCount; index++ {
		accountID := fmt.Sprintf("acct-capacity-%04d", index)
		sub2APIUserID := int64(10_000 + index)
		if index == 0 {
			accountID = "acct-alpha"
			sub2APIUserID = 41
		}
		userID := fmt.Sprintf("usr-capacity-%04d", index)
		organizationID := fmt.Sprintf("org-capacity-%04d", index)
		account, user, organization, membership := provisionedAccountRowsFor(accountID, userID, organizationID, fmt.Sprintf("capacity-%04d@example.com", index), sub2APIUserID)
		if err := store.CreateProvisionedAccount(ctx, account, user, organization, membership); err != nil {
			t.Fatalf("seed account %d: %v", index, err)
		}
		row := monthlyActiveResource("compute", fmt.Sprintf("compute-capacity-%04d", index), now.Add(12*time.Hour))
		row["accountId"], row["workspaceId"] = accountID, fmt.Sprintf("workspace-capacity-%04d", index)
		if err := store.SaveCompute(ctx, row); err != nil {
			t.Fatalf("seed compute %d: %v", index, err)
		}
	}
	seedDuration := time.Since(seedStarted)

	sub2API := newCapacitySub2API()
	fabric := &capacityFabric{creates: map[string]string{}}
	ledger := &capacityLedger{receipts: map[string]string{}}
	service := controlplane.NewService(ledger, fabric, sub2API)
	handler, err := NewPersistentServer(service, stateStore)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	transport := &http.Transport{MaxIdleConns: 200, MaxIdleConnsPerHost: 200, MaxConnsPerHost: 200}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	defer transport.CloseIdleConnections()
	session := tenantAdminSessionForTest(t, handler)
	cookies, csrf := session.Result().Cookies(), session.Header().Get("x-opl-csrf-token")

	runtime.GC()
	processBefore := capacityProcessUsage(t)
	connectionContext, stopConnectionMonitor := context.WithCancel(ctx)
	connectionSamples := capacityMonitorConnections(connectionContext, admin, applicationName)

	consoleStarted := time.Now()
	consoleCalls := capacityRunConcurrent(ctx, capacityConsoleUsers, func(ctx context.Context, _ int) capacityCall {
		return capacityHTTPCall(ctx, client, httpServer.URL, http.MethodGet, "/api/state", "", "", cookies, csrf)
	})
	consoleDuration := time.Since(consoleStarted)
	capacityRequireStatus(t, "console", consoleCalls, http.StatusOK)

	commandBody := `{"packageId":"basic","name":"Capacity Workspace","sizeGb":10,"autoRenew":false}`
	commandStarted := time.Now()
	commandCalls := capacityRunConcurrent(ctx, capacityCommands, func(ctx context.Context, _ int) capacityCall {
		return capacityHTTPCall(ctx, client, httpServer.URL, http.MethodPost, "/api/workspace-launches", commandBody, "capacity-workspace-launch", cookies, csrf)
	})
	commandDuration := time.Since(commandStarted)
	capacityRequireStatus(t, "Workspace launch", commandCalls, http.StatusAccepted)
	replayStarted := time.Now()
	replayCalls := capacityRunConcurrent(ctx, capacityCommands, func(ctx context.Context, _ int) capacityCall {
		return capacityHTTPCall(ctx, client, httpServer.URL, http.MethodPost, "/api/workspace-launches", commandBody, "capacity-workspace-launch", cookies, csrf)
	})
	replayDuration := time.Since(replayStarted)
	capacityRequireStatus(t, "Workspace launch replay", replayCalls, http.StatusAccepted)
	operations, err := store.ListRuntimeOperations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	launches := make([]map[string]any, 0, 1)
	for _, operation := range operations {
		if operation["action"] == workspaceLaunchAction {
			launches = append(launches, operation)
		}
	}
	if len(launches) != 1 {
		t.Fatalf("durable Workspace launches = %#v", launches)
	}
	if got := fabric.count(); got != 0 {
		t.Fatalf("Workspace submission created Fabric resources: %d", got)
	}
	if got := capacitySub2APIChargeCount(sub2API); got != 0 {
		t.Fatalf("Workspace submission charged before the worker: %d", got)
	}
	if got := ledger.count(); got != 0 {
		t.Fatalf("Workspace submission recorded receipts before the worker: %d", got)
	}

	scanApp, err := newControlPlaneAppWithStore(stateStore)
	if err != nil {
		t.Fatal(err)
	}
	billingStarted := time.Now()
	if err := scanApp.runMonthlyBillingOnce(ctx, service, now); err != nil {
		t.Fatal(err)
	}
	billingDuration := time.Since(billingStarted)
	if err := scanApp.runMonthlyBillingOnce(ctx, service, now); err != nil {
		t.Fatal(err)
	}
	stopConnectionMonitor()
	connectionSample := <-connectionSamples
	if connectionSample.err != nil {
		t.Fatal(connectionSample.err)
	}
	if got := capacitySub2APIChargeCount(sub2API); got != 0 {
		t.Fatalf("Workspace scheduler charged historical resource rows: %d", got)
	}
	if got := ledger.count(); got != 0 {
		t.Fatalf("Workspace scheduler wrote resource receipts: %d", got)
	}
	if got := fabric.count(); got != 0 {
		t.Fatalf("renewal created Fabric resources: prepares=%d", got)
	}
	var rows, uniqueResources, uniqueOperations int
	if err := admin.QueryRowContext(ctx, `SELECT count(*), count(DISTINCT id), count(DISTINCT billing_operation_id) FROM `+schema+`.control_plane_compute_allocations`).Scan(&rows, &uniqueResources, &uniqueOperations); err != nil {
		t.Fatal(err)
	}
	if rows != capacityAccountCount || uniqueResources != rows || uniqueOperations != rows {
		t.Fatalf("historical resource rows changed: rows=%d resources=%d operations=%d", rows, uniqueResources, uniqueOperations)
	}

	processAfter := capacityProcessUsage(t)
	consoleP50, consoleP95, consoleErrors := capacityCallMetrics(consoleCalls)
	commandP50, commandP95, commandErrors := capacityCallMetrics(commandCalls)
	replayP50, replayP95, replayErrors := capacityCallMetrics(replayCalls)
	t.Logf("single_pod_capacity accounts=%d historical_resources=%d seed=%s console_requests=%d console_p50=%s console_p95=%s console_error_rate=%.4f console_total=%s commands=%d command_p50=%s command_p95=%s command_error_rate=%.4f command_total=%s replay_p50=%s replay_p95=%s replay_error_rate=%.4f replay_total=%s workspace_billing_total=%s cpu=%s heap_before_mb=%.1f heap_after_mb=%.1f go_sys_mb=%.1f db_connections=%d",
		capacityAccountCount, rows, seedDuration, capacityConsoleUsers, consoleP50, consoleP95, float64(consoleErrors)/capacityConsoleUsers, consoleDuration,
		capacityCommands, commandP50, commandP95, float64(commandErrors)/capacityCommands, commandDuration, replayP50, replayP95, float64(replayErrors)/capacityCommands, replayDuration,
		billingDuration, processAfter.cpu-processBefore.cpu, float64(processBefore.heapBytes)/(1<<20), float64(processAfter.heapBytes)/(1<<20), float64(processAfter.sysBytes)/(1<<20), connectionSample.max)
	if consoleP95 > capacityRequestBudget || commandP95 > capacityRequestBudget || replayP95 > capacityRequestBudget {
		t.Fatalf("request budget exceeded: console_p95=%s command_p95=%s replay_p95=%s budget=%s", consoleP95, commandP95, replayP95, capacityRequestBudget)
	}
	if connectionSample.max > controlPlaneMaxOpenDBConnections {
		t.Fatalf("database connection budget exceeded: connections=%d budget=%d", connectionSample.max, controlPlaneMaxOpenDBConnections)
	}
}

func capacityRunConcurrent(ctx context.Context, count int, call func(context.Context, int) capacityCall) []capacityCall {
	results := make([]capacityCall, count)
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(count)
	for index := range count {
		go func() {
			defer wait.Done()
			<-start
			results[index] = call(ctx, index)
		}()
	}
	close(start)
	wait.Wait()
	return results
}

func capacityHTTPCall(ctx context.Context, client *http.Client, baseURL, method, path, body, key string, cookies []*http.Cookie, csrf string) capacityCall {
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, strings.NewReader(body))
	if err != nil {
		return capacityCall{duration: time.Since(started), err: err}
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-opl-csrf", csrf)
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return capacityCall{duration: time.Since(started), err: err}
	}
	defer resp.Body.Close()
	payload, readErr := io.ReadAll(resp.Body)
	return capacityCall{duration: time.Since(started), status: resp.StatusCode, body: string(payload), err: readErr}
}

func capacityRequireStatus(t *testing.T, name string, calls []capacityCall, want int) {
	t.Helper()
	for index, call := range calls {
		if call.err != nil || call.status != want {
			t.Fatalf("%s %d status=%d err=%v body=%.200q", name, index, call.status, call.err, call.body)
		}
	}
}

func capacityCallMetrics(calls []capacityCall) (time.Duration, time.Duration, int) {
	durations := make([]time.Duration, 0, len(calls))
	errors := 0
	for _, call := range calls {
		durations = append(durations, call.duration)
		if call.err != nil || call.status < 200 || call.status >= 300 {
			errors++
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	percentile := func(numerator int) time.Duration {
		index := (len(durations)*numerator + 99) / 100
		return durations[max(0, index-1)]
	}
	return percentile(50), percentile(95), errors
}

func capacitySub2APIChargeCount(client *capacitySub2API) int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return len(client.charges)
}

func capacityMonitorConnections(ctx context.Context, db *sql.DB, applicationName string) <-chan capacityConnectionSample {
	result := make(chan capacityConnectionSample, 1)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		maxConnections := 0
		for {
			select {
			case <-ctx.Done():
				result <- capacityConnectionSample{max: maxConnections}
				return
			case <-ticker.C:
				var connections int
				if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name = $1`, applicationName).Scan(&connections); err != nil {
					if ctx.Err() != nil {
						result <- capacityConnectionSample{max: maxConnections}
					} else {
						result <- capacityConnectionSample{max: maxConnections, err: err}
					}
					return
				}
				maxConnections = max(maxConnections, connections)
			}
		}
	}()
	return result
}

func capacityProcessUsage(t *testing.T) capacityProcessSample {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return capacityProcessSample{
		cpu:       time.Duration(usage.Utime.Sec+usage.Stime.Sec)*time.Second + time.Duration(usage.Utime.Usec+usage.Stime.Usec)*time.Microsecond,
		heapBytes: memory.HeapAlloc,
		sysBytes:  memory.Sys,
	}
}
