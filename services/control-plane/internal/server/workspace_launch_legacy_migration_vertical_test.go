package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const (
	legacyMigrationInternalToken = "legacy-migration-internal-token"
	legacyMigrationCapabilityKey = "legacy-migration-capability-key"
)

type legacyMigrationVerticalStore struct {
	*postgresEntStateStore

	mu                     sync.Mutex
	legacyResult           string
	upcastCalls            int
	upcastResult           string
	readSchema3AfterUpcast bool
	persistBeforeReadback  bool
}

func (s *legacyMigrationVerticalStore) arm(legacyResult string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.legacyResult = legacyResult
}

func (s *legacyMigrationVerticalStore) UpcastLegacyWorkspaceLaunch(ctx context.Context, update workspaceLaunchLegacyCAS) error {
	s.mu.Lock()
	s.upcastCalls++
	s.mu.Unlock()
	if err := s.postgresEntStateStore.UpcastLegacyWorkspaceLaunch(ctx, update); err != nil {
		return err
	}
	s.mu.Lock()
	s.upcastResult = stringValue(update.DesiredOperation["result"])
	s.mu.Unlock()
	return nil
}

func (s *legacyMigrationVerticalStore) GetRuntimeOperation(ctx context.Context, operationID string) (map[string]any, bool, error) {
	row, found, err := s.postgresEntStateStore.GetRuntimeOperation(ctx, operationID)
	if err != nil || !found {
		return row, found, err
	}
	s.mu.Lock()
	if s.upcastCalls == 1 && s.upcastResult != "" && stringValue(row["result"]) == s.upcastResult {
		s.readSchema3AfterUpcast = true
	}
	s.mu.Unlock()
	return row, found, nil
}

func (s *legacyMigrationVerticalStore) PersistWorkspaceLaunchReconcile(ctx context.Context, update workspaceLaunchReconcileCAS) error {
	s.mu.Lock()
	if s.upcastCalls == 1 && s.upcastResult != "" && !s.readSchema3AfterUpcast && update.ExpectedOperationResult == s.upcastResult {
		s.persistBeforeReadback = true
	}
	s.mu.Unlock()
	return s.postgresEntStateStore.PersistWorkspaceLaunchReconcile(ctx, update)
}

func (s *legacyMigrationVerticalStore) evidence() (int, string, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upcastCalls, s.upcastResult, s.readSchema3AfterUpcast, s.persistBeforeReadback
}

type legacyMigrationSub2API struct {
	*testSub2APIClient

	mu              sync.Mutex
	keyReads        int
	historyReads    int
	balanceReads    int
	keyMutations    int
	walletMutations int
}

type legacyMigrationFabricClient struct {
	clients.FabricClient
	legacy clients.FabricLegacyWorkspaceLaunchClient

	mu     sync.Mutex
	result clients.LegacyWorkspaceLaunchBindingResult
	err    error
}

func (c *legacyMigrationFabricClient) ReadLegacyWorkspaceLaunchBinding(ctx context.Context, input clients.LegacyWorkspaceLaunchBindingInput) (clients.LegacyWorkspaceLaunchBindingResult, error) {
	result, err := c.legacy.ReadLegacyWorkspaceLaunchBinding(ctx, input)
	c.mu.Lock()
	c.result, c.err = result, err
	c.mu.Unlock()
	return result, err
}

func (c *legacyMigrationFabricClient) evidence() (clients.LegacyWorkspaceLaunchBindingResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.result, c.err
}

func (s *legacyMigrationSub2API) WorkspaceKeysForConvergence(_ context.Context, userID int64, name string) ([]clients.Sub2APIWorkspaceKey, error) {
	s.mu.Lock()
	s.keyReads++
	s.mu.Unlock()
	if userID != 41 || name != workspaceReservedKeyName("ws-paid") {
		return nil, clients.ErrSub2APIWorkspaceKeyMissing
	}
	groupID := int64(7)
	return []clients.Sub2APIWorkspaceKey{{ID: 9, UserID: 41, Name: name, Key: "legacy-workspace-key", GroupID: &groupID, Status: "active"}}, nil
}

func (s *legacyMigrationSub2API) FinancialBalanceHistoryByCodes(_ context.Context, userID int64, codes []string) (map[string]clients.Sub2APIBalanceHistoryEntry, error) {
	s.mu.Lock()
	s.historyReads++
	s.mu.Unlock()
	if userID != 41 || len(codes) != 1 || codes[0] != "redeem-legacy-paid" {
		return nil, errors.New("unexpected Sub2API history identity")
	}
	usedBy := int64(41)
	usedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	return map[string]clients.Sub2APIBalanceHistoryEntry{codes[0]: {
		Code: codes[0], Type: "balance", ValueUSDMicros: -52_580_000, Status: "used", UsedBy: &usedBy, UsedAt: &usedAt, CreatedAt: usedAt,
	}}, nil
}

func (s *legacyMigrationSub2API) Balance(_ context.Context, userID int64) (clients.Sub2APIBalance, error) {
	s.mu.Lock()
	s.balanceReads++
	s.mu.Unlock()
	return clients.Sub2APIBalance{UserID: userID, USDMicros: 947_420_000, Status: "active"}, nil
}

func (s *legacyMigrationSub2API) Charge(context.Context, clients.Sub2APIChargeInput) (clients.Sub2APICharge, error) {
	s.mu.Lock()
	s.walletMutations++
	s.mu.Unlock()
	return clients.Sub2APICharge{}, errors.New("legacy migration wallet mutation is forbidden")
}

func (s *legacyMigrationSub2API) Refund(context.Context, clients.Sub2APIRefundInput) (clients.Sub2APIRefund, error) {
	s.mu.Lock()
	s.walletMutations++
	s.mu.Unlock()
	return clients.Sub2APIRefund{}, errors.New("legacy migration wallet mutation is forbidden")
}

func (s *legacyMigrationSub2API) CreateUserKey(context.Context, clients.SessionDelegatedCredential, int64, clients.Sub2APICreateKeyInput, string) (clients.Sub2APIWorkspaceKey, error) {
	s.mu.Lock()
	s.keyMutations++
	s.mu.Unlock()
	return clients.Sub2APIWorkspaceKey{}, errors.New("legacy migration key mutation is forbidden")
}

func (s *legacyMigrationSub2API) UpdateUserKey(context.Context, clients.SessionDelegatedCredential, int64, int64, clients.Sub2APIUpdateKeyInput) (clients.Sub2APIWorkspaceKey, error) {
	s.mu.Lock()
	s.keyMutations++
	s.mu.Unlock()
	return clients.Sub2APIWorkspaceKey{}, errors.New("legacy migration key mutation is forbidden")
}

func (s *legacyMigrationSub2API) DeleteUserKey(context.Context, clients.SessionDelegatedCredential, int64, int64) error {
	s.mu.Lock()
	s.keyMutations++
	s.mu.Unlock()
	return errors.New("legacy migration key mutation is forbidden")
}

func (s *legacyMigrationSub2API) counts() (int, int, int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keyReads, s.historyReads, s.balanceReads, s.keyMutations, s.walletMutations
}

func TestWorkspaceLaunchPaidSchema2ResumeMigratesThroughRealServicesAndSucceeds(t *testing.T) {
	for key, value := range map[string]string{
		"OPL_MONTHLY_BILLING_WORKER_ENABLED":    "false",
		"OPL_PROVIDER_RECONCILE_WORKER_ENABLED": "false",
		"OPL_ARCHIVE_RETENTION_WORKER_ENABLED":  "false",
		"OPL_WORKSPACE_LAUNCH_WORKER_ENABLED":   "false",
	} {
		t.Setenv(key, value)
	}
	runtimeServer := httptestServerOK(t)
	cpDatabaseURL, fabricDatabaseURL, ledgerDatabaseURL := legacyMigrationPostgresURLs(t)
	fabricProcess := startLegacyMigrationFabricProcess(t, fabricDatabaseURL, runtimeServer.URL)
	fabricStoreBefore := legacyMigrationFabricStoreDigest(t, fabricDatabaseURL)
	ledgerProcess := startLegacyMigrationLedgerProcess(t, ledgerDatabaseURL)

	state, err := newTestPostgresEntStateStore(cpDatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	postgresStore := state.(*postgresEntStateStore)
	store := &legacyMigrationVerticalStore{postgresEntStateStore: postgresStore}
	t.Cleanup(func() { _ = postgresStore.client.Close() })
	seedTenantMember(t, store, "acct-paid", "org-paid", "usr-paid", "paid@example.test")

	ledger := clients.NewLedgerHTTPClient(ledgerProcess.baseURL, legacyMigrationInternalToken, &http.Client{Timeout: 5 * time.Second})
	ledgerList, ok := ledger.(clients.LedgerReceiptListClient)
	if !ok {
		t.Fatal("real Ledger HTTP client does not expose receipt listing")
	}
	realFabricClient := clients.NewFabricHTTPClientWithCapability(fabricProcess.baseURL, legacyMigrationInternalToken, legacyMigrationCapabilityKey, &http.Client{Timeout: 10 * time.Second})
	fabricClient := &legacyMigrationFabricClient{FabricClient: realFabricClient, legacy: realFabricClient.(clients.FabricLegacyWorkspaceLaunchClient)}
	sub2API := &legacyMigrationSub2API{testSub2APIClient: &testSub2APIClient{balance: 947_420_000, charges: map[string]int64{}}}
	service := controlplane.NewService(ledger, fabricClient, sub2API)
	handler, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatal(err)
	}
	legacyResult := legacyMigrationPaidSchema2Result(t)
	legacyRow := map[string]any{
		"id": "launch-paid-schema2", "operationId": "launch-paid-schema2", "accountId": "acct-paid", "workspaceId": "ws-paid",
		"resourceId": "ws-paid", "resourceKind": "workspace_launch", "action": workspaceLaunchAction,
		"status": "manual_review", "result": legacyResult, "createdAt": "2026-08-01T00:00:00Z",
	}
	if err := store.SaveRuntimeOperation(context.Background(), legacyRow); err != nil {
		t.Fatal(err)
	}
	store.arm(legacyResult)

	operator := reservedOperatorSessionForTest(t, handler)
	resumeBody := `{"launchVersion":1,"authorizedStage":"activation","reason":"owner_authoritative_legacy_history_verified","mutationBudget":1}`
	resume := requestWithMutationKeyForTest(t, handler, operator, http.MethodPost, "/api/operator/workspace-launches/launch-paid-schema2/resume", resumeBody, "legacy-migration-resume-paid-01")
	if resume.Code != http.StatusOK {
		binding, bindingErr := fabricClient.evidence()
		t.Fatalf("Operator Resume status=%d body=%s Fabric binding state=%s reason=%s err=%v stages=%#v", resume.Code, resume.Body.String(), binding.State, binding.Reason, bindingErr, binding.Stages)
	}
	var afterResume map[string]any
	if err := json.NewDecoder(resume.Body).Decode(&afterResume); err != nil {
		t.Fatal(err)
	}
	if afterResume["schemaVersion"] != float64(3) || stringValue(afterResume["operationId"]) != "launch-paid-schema2" ||
		stringValue(afterResume["stage"]) != "receipt" || stringValue(afterResume["status"]) != "pending" {
		t.Fatalf("Resume did not enter the schema3 reconciler at receipt: %#v", afterResume)
	}
	if err := handler.(*controlPlaneHTTPHandler).app.runWorkspaceLaunchesOnce(context.Background(), service); err != nil {
		t.Fatalf("same WorkspaceLaunchReconciler did not finish receipt: %v", err)
	}

	finalRow, found, err := store.GetRuntimeOperation(context.Background(), "launch-paid-schema2")
	if err != nil || !found {
		t.Fatalf("final Launch readback found=%t err=%v", found, err)
	}
	finalOperation, err := decodeWorkspaceLaunchReconcileOperation(finalRow)
	if err != nil || finalOperation.Status != "succeeded" || finalOperation.Stage != "succeeded" || finalOperation.stringFact("url") != runtimeServer.URL || finalOperation.stringFact("receiptId") == "" {
		t.Fatalf("legacy Launch terminal operation=%s url=%q receipt=%q err=%v", workspaceLaunchReconcileResultSummary(finalOperation), finalOperation.stringFact("url"), finalOperation.stringFact("receiptId"), err)
	}
	response, err := http.Get(finalOperation.stringFact("url"))
	if err != nil {
		t.Fatalf("GET migrated Workspace URL: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET migrated Workspace URL status=%d, want 200", response.StatusCode)
	}
	receipts, err := ledgerList.ListReceipts(context.Background(), clients.ReceiptQuery{AccountID: "acct-paid", TypePrefix: "billing.workspace_purchased.v1"})
	if err != nil || len(receipts.Receipts) != 1 || receipts.Receipts[0].ReceiptID != finalOperation.stringFact("receiptId") || receipts.Receipts[0].RequestID != finalOperation.ID {
		t.Fatalf("real Ledger Receipt readback=%#v err=%v", receipts, err)
	}

	upcastCalls, upcastResult, readAfterUpcast, persistedBeforeReadback := store.evidence()
	if upcastCalls != 1 || upcastResult == "" || !readAfterUpcast || persistedBeforeReadback {
		t.Fatalf("schema2 CAS/readback calls=%d readAfter=%t persistBeforeRead=%t result=%q", upcastCalls, readAfterUpcast, persistedBeforeReadback, upcastResult)
	}
	assertLegacyMigrationPreservedFacts(t, legacyResult, upcastResult)
	keyReads, historyReads, balanceReads, keyMutations, walletMutations := sub2API.counts()
	if keyReads != 1 || historyReads != 1 || balanceReads != 1 || keyMutations != 0 || walletMutations != 0 {
		t.Fatalf("Sub2API boundary reads=%d/%d/%d keyMutations=%d walletMutations=%d", keyReads, historyReads, balanceReads, keyMutations, walletMutations)
	}
	events := readLegacyMigrationProviderEvents(t, fabricProcess.eventsPath)
	if len(events) != 5 {
		t.Fatalf("Fabric authoritative provider reads=%d, want 5: %#v", len(events), events)
	}
	for _, event := range events {
		if event.Mutation {
			t.Fatalf("migration crossed Fabric/provider mutation boundary: %#v", events)
		}
	}
	if fabricStoreAfter := legacyMigrationFabricStoreDigest(t, fabricDatabaseURL); fabricStoreAfter != fabricStoreBefore {
		t.Fatalf("Fabric migration point-read mutated OperationStore: before=%s after=%s", fabricStoreBefore, fabricStoreAfter)
	}
	if finalOperation.ResumeAuthorization == nil || finalOperation.ResumeAuthorization.AuthorizationID != "legacy-migration-resume-paid-01" || finalOperation.ResumeAuthorizationConsumedAt == "" || len(finalOperation.ConsumedResumeAuthorizations) != 0 {
		t.Fatalf("Resume authorization is not one immutable consumed grant: %#v consumed=%#v", finalOperation.ResumeAuthorization, finalOperation.ConsumedResumeAuthorizations)
	}

	replay := requestWithMutationKeyForTest(t, handler, operator, http.MethodPost, "/api/operator/workspace-launches/launch-paid-schema2/resume", resumeBody, "legacy-migration-resume-paid-01")
	if replay.Code != http.StatusOK {
		t.Fatalf("exact Resume replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	drift := requestWithMutationKeyForTest(t, handler, operator, http.MethodPost, "/api/operator/workspace-launches/launch-paid-schema2/resume",
		`{"launchVersion":1,"authorizedStage":"receipt","reason":"drifted_authorization","mutationBudget":0}`, "legacy-migration-resume-paid-01")
	if drift.Code != http.StatusConflict {
		t.Fatalf("drifted immutable Resume status=%d body=%s", drift.Code, drift.Body.String())
	}
}

func legacyMigrationFabricStoreDigest(t *testing.T, databaseURL string) string {
	t.Helper()
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	var digest string
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(md5(string_agg(row_to_json(f)::text, '' ORDER BY id)), md5('')) FROM fabric_operations f`).Scan(&count, &digest); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%d:%s", count, digest)
}

func legacyMigrationPaidSchema2Result(t *testing.T) string {
	t.Helper()
	result := map[string]any{
		"schemaVersion": 2, "phase": "runtime_ready", "requestHash": strings.Repeat("a", 64),
		"accountId": "acct-paid", "ownerUserId": "usr-paid", "sub2apiUserId": int64(41), "workspaceId": "ws-paid",
		"name": "Paid Legacy", "packageId": "basic", "sizeGb": 10, "autoRenew": false,
		"priceVersion": pricingCatalogVersion, "totalChargeUsdMicros": int64(52_580_000),
		"workspaceImageDigest": "registry.example/workspace@sha256:" + strings.Repeat("b", 64),
		"workspaceKeyGroupId":  int64(7), "workspaceApiKeyId": int64(9), "workspaceKeyStatus": workspaceKeyCodexGroupBound,
		"workspaceKeyFingerprint": "sha256:722c58cce111f93ae1012c716d360419081fd21ffdcc5abe8c42765f7063d75e",
		"sub2apiRedeemCode":       "redeem-legacy-paid", "chargeAttempted": true,
		"chargeConfirmation":        map[string]any{"code": "redeem-legacy-paid", "userId": int64(41), "chargeUsdMicros": int64(52_580_000), "status": "used"},
		"preChargeBalanceUsdMicros": int64(1_000_000_000), "postChargeBalanceUsdMicros": int64(947_420_000), "postChargeBalanceKnown": true,
		"billingPeriodState": "frozen", "periodStart": "2026-08-01T00:00:00Z", "paidThrough": "2026-09-01T00:00:00Z", "billingAnchorDay": 1,
		"computeAllocationId": "compute-legacy-paid", "computeBindingRef": "fabric-record-compute-paid",
		"storageId": "storage-legacy-paid", "storageBindingRef": "fabric-record-storage-paid",
		"attachmentId": "attachment-legacy-paid", "attachmentOperationId": "attachment-operation-paid", "attachmentBindingRef": "fabric-record-attachment-paid",
		"workspaceOperationId": "workspace-operation-paid", "gatewaySecretRef": "secret-ws-paid", "gatewaySecretVersion": "v1", "secretBindingRef": "fabric-record-secret-paid",
		"runtimeId": "runtime-legacy-paid", "runtimeReady": true, "runtimeServiceName": "runtime-ws-paid", "runtimeBindingRef": "fabric-record-runtime-paid",
		"runtimeUsername": "owner", "credentialStatus": "configured", "credentialVersion": "v1", "credentialSecretRef": "secret-ws-paid",
		"url": "runtime-url-replaced-by-authoritative-readback", "acceptanceBCapacitySlot": true,
		"continuationAttemptBudgets": map[string]any{
			"storage":    map[string]any{"max": 1, "attempted": 1, "confirmed": 1, "unknown": 0},
			"attachment": map[string]any{"max": 1, "attempted": 1, "confirmed": 1, "unknown": 0},
			"secret":     map[string]any{"max": 1, "attempted": 1, "confirmed": 1, "unknown": 0},
			"runtime":    map[string]any{"max": 1, "attempted": 1, "confirmed": 1, "unknown": 0},
			"activation": map[string]any{"max": 1, "attempted": 0, "confirmed": 0, "unknown": 0},
			"receipt":    map[string]any{"max": 1, "attempted": 0, "confirmed": 0, "unknown": 0},
		},
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func assertLegacyMigrationPreservedFacts(t *testing.T, legacyResult, upcastResult string) {
	t.Helper()
	var legacy, upcast map[string]json.RawMessage
	if json.Unmarshal([]byte(legacyResult), &legacy) != nil || json.Unmarshal([]byte(upcastResult), &upcast) != nil {
		t.Fatal("legacy/upcast result is invalid JSON")
	}
	for _, field := range []string{
		"requestHash", "accountId", "ownerUserId", "sub2apiUserId", "workspaceId", "name", "packageId", "sizeGb", "autoRenew",
		"priceVersion", "totalChargeUsdMicros", "workspaceImageDigest", "workspaceKeyGroupId", "workspaceApiKeyId", "workspaceKeyFingerprint",
		"sub2apiRedeemCode", "preChargeBalanceUsdMicros", "postChargeBalanceUsdMicros", "postChargeBalanceKnown", "periodStart", "paidThrough", "billingAnchorDay",
		"computeAllocationId", "computeBindingRef", "storageId", "storageBindingRef", "attachmentId", "attachmentOperationId", "attachmentBindingRef",
		"workspaceOperationId", "gatewaySecretRef", "gatewaySecretVersion", "secretBindingRef", "runtimeId", "runtimeServiceName", "runtimeBindingRef",
	} {
		if !bytes.Equal(legacy[field], upcast[field]) {
			t.Fatalf("CAS upcast changed %s: expected=%s actual=%s", field, legacy[field], upcast[field])
		}
	}
	var budgets map[string]workspaceLaunchStageAttempt
	var attempts map[string]workspaceLaunchStageAttempt
	if json.Unmarshal(legacy["continuationAttemptBudgets"], &budgets) != nil || json.Unmarshal(upcast["attempts"], &attempts) != nil {
		t.Fatal("CAS upcast omitted attempt budgets")
	}
	for _, stage := range workspaceLaunchLegacyBudgetStages {
		want, got := budgets[stage], attempts[stage]
		if got.Attempted != want.Attempted || got.Confirmed != want.Confirmed || got.Unknown != want.Unknown || got.Max != want.Max {
			t.Fatalf("CAS upcast changed %s budget: expected=%#v actual=%#v", stage, want, got)
		}
	}
	if attempts["key"].Attempted != 0 || attempts["key"].Confirmed != 0 || attempts["debit"].Confirmed != 1 || attempts["ensure_compute_allocation"].Confirmed != 1 {
		t.Fatalf("migration inferred or lost historical attempts: %#v", attempts)
	}
}

type legacyMigrationChild struct {
	baseURL string
	command *exec.Cmd
	output  *bytes.Buffer
	done    chan struct{}
	mu      sync.Mutex
	err     error
}

func (p *legacyMigrationChild) startWait() {
	go func() {
		err := p.command.Wait()
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		close(p.done)
	}()
}

func (p *legacyMigrationChild) waitErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *legacyMigrationChild) cleanup(t *testing.T, label string) {
	t.Helper()
	select {
	case <-p.done:
		return
	default:
	}
	if p.command.Process != nil {
		_ = p.command.Process.Kill()
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Errorf("%s child did not exit after kill: %s", label, p.output.String())
	}
}

type legacyMigrationFabricProcess struct {
	*legacyMigrationChild
	eventsPath string
}

func startLegacyMigrationFabricProcess(t *testing.T, databaseURL, runtimeURL string) legacyMigrationFabricProcess {
	t.Helper()
	repoRoot := legacyMigrationRepoRoot(t)
	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "fabric-http.test")
	build := exec.Command("go", "test", "-c", "-o", binaryPath, "./internal/http")
	build.Dir = filepath.Join(repoRoot, "services", "fabric")
	build.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real Fabric helper: %v\n%s", err, output)
	}
	addr := legacyMigrationFreeAddress(t)
	eventsPath := filepath.Join(tempDir, "provider-events.jsonl")
	command := exec.Command(binaryPath, "-test.run=^TestWorkspaceLaunchLegacyMigrationFabricProcess$", "-test.v")
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	output := &bytes.Buffer{}
	command.Stdout, command.Stderr = output, output
	command.Env = append(os.Environ(),
		"PGSSLMODE=disable", "OPL_LEGACY_MIGRATION_FABRIC_HELPER=1",
		"OPL_LEGACY_MIGRATION_FABRIC_ADDR="+addr,
		"OPL_LEGACY_MIGRATION_FABRIC_DATABASE_URL="+legacyMigrationPrivatePostgresProxy(t, databaseURL),
		"OPL_LEGACY_MIGRATION_FABRIC_EVENTS="+eventsPath,
		"OPL_LEGACY_MIGRATION_RUNTIME_URL="+runtimeURL,
		"OPL_LEGACY_MIGRATION_FABRIC_TOKEN="+legacyMigrationInternalToken,
		"OPL_LEGACY_MIGRATION_FABRIC_CAPABILITY_KEY="+legacyMigrationCapabilityKey,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	child := &legacyMigrationChild{baseURL: "http://" + addr, command: command, output: output, done: make(chan struct{})}
	child.startWait()
	t.Cleanup(func() { child.cleanup(t, "Fabric") })
	waitLegacyMigrationHTTP(t, child, "/healthz", "Fabric")
	return legacyMigrationFabricProcess{legacyMigrationChild: child, eventsPath: eventsPath}
}

type legacyMigrationLedgerProcess struct{ *legacyMigrationChild }

func startLegacyMigrationLedgerProcess(t *testing.T, databaseURL string) legacyMigrationLedgerProcess {
	t.Helper()
	repoRoot := legacyMigrationRepoRoot(t)
	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "ledger")
	build := exec.Command("go", "build", "-o", binaryPath, "./cmd/ledger")
	build.Dir = filepath.Join(repoRoot, "services", "ledger")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real Ledger: %v\n%s", err, output)
	}
	addr := legacyMigrationFreeAddress(t)
	command := exec.Command(binaryPath)
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	output := &bytes.Buffer{}
	command.Stdout, command.Stderr = output, output
	command.Env = append(os.Environ(), "NODE_ENV=test", "PGSSLMODE=disable", "LEDGER_ADDR="+addr,
		"DATABASE_URL="+legacyMigrationPrivatePostgresProxy(t, databaseURL), "OPL_INTERNAL_SERVICE_TOKEN="+legacyMigrationInternalToken)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	child := &legacyMigrationChild{baseURL: "http://" + addr, command: command, output: output, done: make(chan struct{})}
	child.startWait()
	t.Cleanup(func() { child.cleanup(t, "Ledger") })
	waitLegacyMigrationHTTP(t, child, "/readyz", "Ledger")
	return legacyMigrationLedgerProcess{legacyMigrationChild: child}
}

func waitLegacyMigrationHTTP(t *testing.T, child *legacyMigrationChild, path, label string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(child.baseURL + path)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case <-child.done:
			t.Fatalf("%s child exited before readiness: %v\n%s", label, child.waitErr(), child.output.String())
		case <-time.After(25 * time.Millisecond):
		}
	}
	t.Fatalf("%s child readiness timeout: %s", label, child.output.String())
}

func legacyMigrationPostgresURLs(t *testing.T) (string, string, string) {
	t.Helper()
	admin := openControlPlaneTestPostgres(t)
	suffix := time.Now().UTC().UnixNano()
	databases := []string{
		fmt.Sprintf("legacy_migration_cp_%d", suffix), fmt.Sprintf("legacy_migration_fabric_%d", suffix), fmt.Sprintf("legacy_migration_ledger_%d", suffix),
	}
	for _, database := range databases {
		if _, err := admin.Exec(`CREATE DATABASE "` + database + `"`); err != nil {
			_ = admin.Close()
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, database := range databases {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = admin.ExecContext(ctx, `DROP DATABASE "`+database+`" WITH (FORCE)`)
			cancel()
		}
		_ = admin.Close()
	})
	return controlPlaneTestPostgresURL(t, databases[0], ""), controlPlaneTestPostgresURL(t, databases[1], ""), controlPlaneTestPostgresURL(t, databases[2], "")
}

func legacyMigrationPrivatePostgresProxy(t *testing.T, databaseURL string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal("legacy migration test requires a PostgreSQL DSN")
	}
	target := parsed.Host
	if parsed.Scheme == "" {
		target = net.JoinHostPort(strings.TrimSpace(os.Getenv("PGHOST")), strings.TrimSpace(os.Getenv("PGPORT")))
	} else if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" || parsed.Hostname() == "" {
		t.Fatal("legacy migration test requires a PostgreSQL DSN")
	} else if ip := net.ParseIP(parsed.Hostname()); ip != nil && ip.IsPrivate() && !ip.IsLoopback() {
		return databaseURL
	}
	if host, port, splitErr := net.SplitHostPort(target); splitErr != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		t.Fatal("legacy migration test requires an explicit PostgreSQL host and port")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(legacyMigrationPrivateIPv4(t).String(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			client, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go legacyMigrationProxyConnection(client, target)
		}
	}()
	if parsed.Scheme == "" {
		host, port, _ := net.SplitHostPort(listener.Addr().String())
		return databaseURL + " host=" + host + " port=" + port + " sslmode=disable"
	}
	parsed.Host = listener.Addr().String()
	return parsed.String()
}

func legacyMigrationProxyConnection(client net.Conn, target string) {
	upstream, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		_ = client.Close()
		return
	}
	closeBoth := func() { _ = client.Close(); _ = upstream.Close() }
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); closeBoth(); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); closeBoth(); done <- struct{}{} }()
	<-done
	<-done
}

func legacyMigrationPrivateIPv4(t *testing.T) net.IP {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		ip, _, parseErr := net.ParseCIDR(address.String())
		if parseErr == nil && ip.To4() != nil && ip.IsPrivate() && !ip.IsLoopback() {
			return ip.To4()
		}
	}
	t.Fatal("no RFC1918 IPv4 address available for PostgreSQL proxy")
	return nil
}

func legacyMigrationFreeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func legacyMigrationRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "packages", "contracts", "opl-cloud-fabric-launch-binding-contract.json")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root not found")
		}
		dir = parent
	}
}

func httptestServerOK(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("workspace-ready"))
	}))
	t.Cleanup(server.Close)
	return server
}

func readLegacyMigrationProviderEvents(t *testing.T, path string) []struct {
	Stage    string `json:"stage"`
	Mutation bool   `json:"mutation"`
} {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	events := make([]struct {
		Stage    string `json:"stage"`
		Mutation bool   `json:"mutation"`
	}, 0)
	for _, line := range bytes.Split(bytes.TrimSpace(body), []byte{'\n'}) {
		var event struct {
			Stage    string `json:"stage"`
			Mutation bool   `json:"mutation"`
		}
		if len(line) == 0 {
			continue
		}
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("decode provider event: %v", err)
		}
		events = append(events, event)
	}
	return events
}

var _ StateStore = (*legacyMigrationVerticalStore)(nil)
var _ clients.Sub2APIClient = (*legacyMigrationSub2API)(nil)
var _ clients.Sub2APIWorkspaceKeyConvergenceClient = (*legacyMigrationSub2API)(nil)
var _ clients.Sub2APIFinancialBalanceHistoryLookupClient = (*legacyMigrationSub2API)(nil)
