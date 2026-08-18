package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type workspaceLaunchMonthlyPreflightLedger struct {
	fakeLedgerClient
	mu       sync.Mutex
	receipts map[string]clients.Receipt
}

func (l *workspaceLaunchMonthlyPreflightLedger) RecordReceipt(_ context.Context, input clients.ReceiptInput, key string) (clients.Receipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if receipt, ok := l.receipts[key]; ok {
		return receipt, nil
	}
	receipt := clients.Receipt{ReceiptInput: input, ReceiptID: "receipt-" + stableID(key)[:16]}
	l.receipts[key] = receipt
	return receipt, nil
}

func (l *workspaceLaunchMonthlyPreflightLedger) ListReceipts(_ context.Context, query clients.ReceiptQuery) (clients.ReceiptPage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	page := clients.ReceiptPage{}
	for _, receipt := range l.receipts {
		if receipt.AccountID == query.AccountID {
			page.Receipts = append(page.Receipts, receipt)
		}
	}
	return page, nil
}

type workspaceLaunchMonthlyPreflightFabric struct {
	*gatewayAccountingFabric
	events             *[]string
	failureMode        string
	runtimePending     bool
	runtimeReady       bool
	runtimeEnsureCalls int
	runtimeReadCalls   int
	runtimeReadyResult clients.WorkspaceLaunchStageResult
}

func (f *workspaceLaunchMonthlyPreflightFabric) PreflightWorkspaceLaunch(_ context.Context, input clients.WorkspaceLaunchPreflightInput) (clients.WorkspaceLaunchPreflight, error) {
	*f.events = append(*f.events, "fabric.workspace.preflight")
	return clients.WorkspaceLaunchPreflight{
		SchemaVersion:      clients.WorkspaceLaunchFabricSchemaVersion,
		Available:          true,
		Reason:             "none",
		LaunchOperationID:  input.LaunchOperationID,
		RequestHash:        input.RequestHash,
		ProviderProfileRef: "provider-profile",
		BindingRef:         "workspace-binding",
		SpecDigest:         strings.Repeat("a", 64),
	}, nil
}

func (f *workspaceLaunchMonthlyPreflightFabric) MonthlyPreflight(_ context.Context, input clients.MonthlyPreflightInput) (clients.MonthlyPreflight, error) {
	*f.events = append(*f.events, "fabric.monthly."+input.ResourceType)
	if f.failureMode == input.ResourceType+"_error" {
		return clients.MonthlyPreflight{}, errors.New("monthly preflight unavailable")
	}
	result := clients.MonthlyPreflight{
		ResourceType: input.ResourceType,
		PackageID:    input.PackageID,
		SizeGB:       input.SizeGB,
		Zone:         input.Zone,
		Available:    f.failureMode != input.ResourceType+"_unavailable",
	}
	if f.failureMode == input.ResourceType+"_invalid" {
		result.PackageID = "drifted"
	}
	return result, nil
}

func (f *workspaceLaunchMonthlyPreflightFabric) EnsureWorkspaceLaunchStage(ctx context.Context, input clients.WorkspaceLaunchStageInput) (clients.WorkspaceLaunchStageResult, error) {
	*f.events = append(*f.events, "fabric.stage."+input.Binding.Stage)
	result, err := f.gatewayAccountingFabric.EnsureWorkspaceLaunchStage(ctx, input)
	if err != nil || input.Binding.Stage != "runtime" || !f.runtimePending {
		return result, err
	}
	f.runtimeEnsureCalls++
	f.runtimeReadyResult = result
	result.State, result.Reason = workspaceLaunchStagePending, "provider_provisioning"
	return result, nil
}

func (f *workspaceLaunchMonthlyPreflightFabric) ReadWorkspaceLaunchStage(ctx context.Context, input clients.WorkspaceLaunchStageInput) (clients.WorkspaceLaunchStageResult, error) {
	if input.Binding.Stage != "runtime" || !f.runtimePending {
		return f.gatewayAccountingFabric.ReadWorkspaceLaunchStage(ctx, input)
	}
	f.runtimeReadCalls++
	if f.runtimeEnsureCalls == 0 {
		*f.events = append(*f.events, "fabric.read.runtime.absent")
		return clients.WorkspaceLaunchStageResult{
			SchemaVersion: clients.WorkspaceLaunchFabricSchemaVersion, State: workspaceLaunchStageAbsent, Reason: "no_stage_record",
			Binding: input.Binding, Resources: input.Resources,
		}, nil
	}
	if f.runtimeReady {
		*f.events = append(*f.events, "fabric.read.runtime.ready")
		return f.runtimeReadyResult, nil
	}
	*f.events = append(*f.events, "fabric.read.runtime.pending")
	result := f.runtimeReadyResult
	result.State, result.Reason = workspaceLaunchStagePending, "provider_provisioning"
	return result, nil
}

type workspaceLaunchMonthlyPreflightSub2API struct {
	*testSub2APIClient
	events *[]string
	keys   []clients.Sub2APIWorkspaceKey
}

func (c *workspaceLaunchMonthlyPreflightSub2API) UserGroups(_ context.Context, credential clients.SessionDelegatedCredential, userID int64) ([]clients.Sub2APIGroup, error) {
	if credential.Bearer != "test-user-delegated-token" || userID != 41 {
		return nil, errors.New("wrong delegated credential")
	}
	return []clients.Sub2APIGroup{{ID: 7, Name: "Codex", Status: "active"}}, nil
}

func (c *workspaceLaunchMonthlyPreflightSub2API) WorkspaceKeysForConvergence(_ context.Context, userID int64, name string) ([]clients.Sub2APIWorkspaceKey, error) {
	result := make([]clients.Sub2APIWorkspaceKey, 0, len(c.keys))
	for _, key := range c.keys {
		if key.UserID == userID && key.Name == name {
			result = append(result, key)
		}
	}
	return result, nil
}

func (c *workspaceLaunchMonthlyPreflightSub2API) WorkspaceUserKeysForConvergence(_ context.Context, credential clients.SessionDelegatedCredential, userID int64, name string) ([]clients.Sub2APIWorkspaceKey, error) {
	if credential.Bearer != "test-user-delegated-token" || userID != 41 {
		return nil, errors.New("wrong delegated credential")
	}
	return c.WorkspaceKeysForConvergence(context.Background(), userID, name)
}

func (c *workspaceLaunchMonthlyPreflightSub2API) CreateUserKey(_ context.Context, _ clients.SessionDelegatedCredential, userID int64, input clients.Sub2APICreateKeyInput, _ string) (clients.Sub2APIWorkspaceKey, error) {
	*c.events = append(*c.events, "sub2api.workspace-key")
	groupID := input.GroupID
	key := clients.Sub2APIWorkspaceKey{ID: 19, UserID: userID, Name: input.Name, Key: "workspace-key-secret", GroupID: &groupID, Status: "active"}
	c.keys = append(c.keys, key)
	return key, nil
}

func (*workspaceLaunchMonthlyPreflightSub2API) UpdateUserKey(context.Context, clients.SessionDelegatedCredential, int64, int64, clients.Sub2APIUpdateKeyInput) (clients.Sub2APIWorkspaceKey, error) {
	return clients.Sub2APIWorkspaceKey{}, errors.New("unexpected key update")
}

func (*workspaceLaunchMonthlyPreflightSub2API) DeleteUserKey(context.Context, clients.SessionDelegatedCredential, int64, int64) error {
	return errors.New("unexpected key delete")
}

func (c *workspaceLaunchMonthlyPreflightSub2API) Charge(ctx context.Context, input clients.Sub2APIChargeInput) (clients.Sub2APICharge, error) {
	*c.events = append(*c.events, "sub2api.charge")
	return c.testSub2APIClient.Charge(ctx, input)
}

func (c *workspaceLaunchMonthlyPreflightSub2API) FinancialBalanceHistoryByCodes(_ context.Context, _ int64, codes []string) (map[string]clients.Sub2APIBalanceHistoryEntry, error) {
	c.testSub2APIClient.mu.Lock()
	defer c.testSub2APIClient.mu.Unlock()
	history := map[string]clients.Sub2APIBalanceHistoryEntry{}
	usedAt := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	userID := int64(41)
	for _, code := range codes {
		charge, ok := c.testSub2APIClient.charges[code]
		if ok {
			history[code] = clients.Sub2APIBalanceHistoryEntry{
				Code: code, Type: "balance", ValueUSDMicros: -charge, Status: "used", UsedBy: &userID, UsedAt: &usedAt, CreatedAt: usedAt,
			}
		}
	}
	return history, nil
}

func newWorkspaceLaunchMonthlyPreflightFixture(t *testing.T, failureMode string) (http.Handler, *memoryTableStore, *workspaceLaunchMonthlyPreflightSub2API, *workspaceLaunchMonthlyPreflightFabric, *[]string) {
	t.Helper()
	// These cases prove the platform billing sequence, independent of the shell
	// environment used to run the package.
	t.Setenv("OPL_DEPLOYMENT_MODE", string(deploymentPlatformOwned))
	events := []string{}
	client := &workspaceLaunchMonthlyPreflightSub2API{
		testSub2APIClient: &testSub2APIClient{
			balance: 100_000_000, charges: map[string]int64{},
			identities: map[string]clients.Sub2APIIdentity{"alpha@example.com": {ID: 41, Email: "alpha@example.com", Status: "active"}},
			passwords:  map[string]string{"alpha@example.com": "CorrectHorseBatteryStaple!"},
		},
		events: &events,
	}
	fabric := &workspaceLaunchMonthlyPreflightFabric{
		gatewayAccountingFabric: newGatewayAccountingFabric(),
		events:                  &events,
		failureMode:             failureMode,
	}
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	ledger := &workspaceLaunchMonthlyPreflightLedger{receipts: map[string]clients.Receipt{}}
	server, err := NewPersistentServer(controlplane.NewService(ledger, fabric, client), store)
	if err != nil {
		t.Fatal(err)
	}
	return server, store, client, fabric, &events
}

func continueWorkspaceLaunchKeyForMonthlyPreflightTest(t *testing.T, server http.Handler, session *httptest.ResponseRecorder, key string) (workspaceLaunchReconcileOperation, error) {
	t.Helper()
	handler, ok := server.(*controlPlaneHTTPHandler)
	if !ok {
		t.Fatal("unexpected control-plane test handler")
	}
	var credential clients.SessionDelegatedCredential
	for _, cookie := range session.Result().Cookies() {
		if value, found := handler.app.sessionCredentials.Get(sessionLookupKey(cookie.Value)); found {
			credential = value
			break
		}
	}
	if credential.Bearer == "" {
		t.Fatal("missing delegated session credential")
	}
	operationID := workspaceLaunchOperationID("acct-alpha", key)
	reconciler := handler.app.workspaceLaunchReconciler(handler.service, credential, 41)
	return reconciler.Reconcile(context.Background(), operationID)
}

func TestWorkspaceLaunchMissingPurchaseEligibilityFailsClosedBeforeMutation(t *testing.T) {
	t.Setenv(controlledBasicPilotEnabledEnv, "1")
	t.Setenv("OPL_WORKSPACE_LAUNCH_WORKER_ENABLED", "0")
	server, store, client, _, events := newWorkspaceLaunchMonthlyPreflightFixture(t, "")
	account, found, err := store.GetAccount(context.Background(), "acct-alpha")
	if err != nil || !found {
		t.Fatalf("seed account readback = %#v found=%v err=%v", account, found, err)
	}
	delete(account, "workspacePurchaseEnabled")
	if err := store.SaveAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	response := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/workspace-launches", `{"name":"Missing eligibility","packageId":"basic","autoRenew":false}`, "missing-eligibility")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "workspace_purchase_not_enabled") {
		t.Fatalf("missing eligibility response = %d: %s", response.Code, response.Body.String())
	}
	if len(client.charges) != 0 || len(*events) != 0 {
		t.Fatalf("missing eligibility crossed mutation boundary: charges=%#v events=%#v", client.charges, *events)
	}
}

func TestWorkspaceLaunchRetainsInstancePilotAllowlistUntilMigrationReadback(t *testing.T) {
	t.Setenv(controlledBasicPilotEnabledEnv, "1")
	t.Setenv(controlledBasicPilotAccountsEnv, "")
	t.Setenv("OPL_WORKSPACE_LAUNCH_WORKER_ENABLED", "0")
	server, _, client, _, events := newWorkspaceLaunchMonthlyPreflightFixture(t, "")
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	response := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/workspace-launches", `{"name":"Pilot allowlist pending","packageId":"basic","autoRenew":false}`, "pilot-allowlist-pending")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "workspace_launch_admission_invalid") {
		t.Fatalf("pilot allowlist transition response = %d: %s", response.Code, response.Body.String())
	}
	if len(client.charges) != 0 || len(*events) != 0 {
		t.Fatalf("pilot allowlist transition crossed mutation boundary: charges=%#v events=%#v", client.charges, *events)
	}
}

func TestWorkspaceLaunchMonthlyPreflightFailureBlocksDebitAndFabricMutation(t *testing.T) {
	t.Setenv(controlledBasicPilotEnabledEnv, "1")
	t.Setenv(controlledBasicPilotAccountsEnv, "acct-alpha")
	t.Setenv("OPL_TENCENT_ZONE", "ap-guangzhou-1")
	t.Setenv("OPL_WORKSPACE_LAUNCH_WORKER_ENABLED", "0")

	for _, failureMode := range []string{
		"compute_error", "compute_unavailable", "compute_invalid",
		"storage_error", "storage_unavailable", "storage_invalid",
	} {
		t.Run(failureMode, func(t *testing.T) {
			server, store, client, _, events := newWorkspaceLaunchMonthlyPreflightFixture(t, failureMode)
			session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")

			response := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/workspace-launches",
				`{"name":"Monthly preflight failure","packageId":"basic","autoRenew":false}`,
				"monthly-preflight-"+failureMode)
			if response.Code != http.StatusAccepted {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			continued, runErr := continueWorkspaceLaunchKeyForMonthlyPreflightTest(t, server, session, "monthly-preflight-"+failureMode)
			operations, err := store.ListRuntimeOperations(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(operations) != 1 || len(client.charges) != 0 {
				t.Fatalf("monthly %s failure crossed debit: operations=%#v charges=%#v events=%#v", failureMode, operations, client.charges, *events)
			}
			operation, decodeErr := decodeWorkspaceLaunchReconcileOperation(operations[0])
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			attempt := operation.Attempts["debit"]
			if continued.ID != operation.ID || runErr == nil || !errors.Is(runErr, errWorkspaceLaunchMutationNotDispatched) ||
				operation.Status != "manual_review" || operation.Stage != "debit" || attempt.Status != "unknown" || attempt.Unknown != 1 ||
				operation.Observations["debit"].State != workspaceLaunchStageUnknown || len(operation.FreshContinuationAuthorizations) != 0 {
				t.Fatalf("monthly %s failure did not park pre-dispatch: operation=%#v attempt=%#v err=%v", failureMode, operation, attempt, runErr)
			}
			for _, event := range *events {
				if strings.HasPrefix(event, "fabric.stage.") || event == "sub2api.charge" {
					t.Fatalf("monthly %s failure crossed mutation boundary: events=%#v", failureMode, *events)
				}
			}
		})
	}
}

func TestWorkspaceLaunchMissingProviderZoneParksReservedDebitWithoutAuthorityWrite(t *testing.T) {
	t.Setenv(controlledBasicPilotEnabledEnv, "1")
	t.Setenv(controlledBasicPilotAccountsEnv, "acct-alpha")
	t.Setenv("OPL_TENCENT_ZONE", "")
	t.Setenv("OPL_WORKSPACE_LAUNCH_WORKER_ENABLED", "0")

	server, store, client, _, events := newWorkspaceLaunchMonthlyPreflightFixture(t, "")
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	response := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/workspace-launches",
		`{"name":"Missing local provider zone","packageId":"basic","autoRenew":false}`,
		"missing-local-provider-zone")
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	_, runErr := continueWorkspaceLaunchKeyForMonthlyPreflightTest(t, server, session, "missing-local-provider-zone")

	if runErr == nil || !errors.Is(runErr, errWorkspaceLaunchMonthlyPreflightInvalid) {
		t.Fatalf("run launch error=%v, want %v", runErr, errWorkspaceLaunchMonthlyPreflightInvalid)
	}
	rows, err := store.ListRuntimeOperations(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("read launch operations=%#v err=%v", rows, err)
	}
	operation, err := decodeWorkspaceLaunchReconcileOperation(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	attempt := operation.Attempts["debit"]
	if operation.Status != "manual_review" || operation.Stage != "debit" ||
		attempt.Attempted != 1 || attempt.Confirmed != 0 || attempt.Unknown != 1 || attempt.Max != 1 || attempt.Status != "unknown" ||
		operation.Observations["debit"].State != workspaceLaunchStageUnknown {
		t.Fatalf("unexpected debit failure transition: operation=%#v attempt=%#v", operation, attempt)
	}
	if len(client.charges) != 0 {
		t.Fatalf("missing provider zone wrote debit authority: charges=%#v events=%#v", client.charges, *events)
	}
}

func TestWorkspaceLaunchMonthlyPreflightRunsBeforeDebitAndProviderStages(t *testing.T) {
	t.Setenv(controlledBasicPilotEnabledEnv, "1")
	t.Setenv(controlledBasicPilotAccountsEnv, "acct-alpha")
	t.Setenv("OPL_TENCENT_ZONE", "ap-guangzhou-1")
	t.Setenv("OPL_WORKSPACE_LAUNCH_WORKER_ENABLED", "0")

	server, store, client, _, events := newWorkspaceLaunchMonthlyPreflightFixture(t, "")
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	response := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/workspace-launches",
		`{"name":"Monthly preflight success","packageId":"basic","autoRenew":false}`,
		"monthly-preflight-success")
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(client.charges) != 0 {
		t.Fatalf("launch route debited before worker: charges=%#v events=%#v", client.charges, *events)
	}
	if _, err := continueWorkspaceLaunchKeyForMonthlyPreflightTest(t, server, session, "monthly-preflight-success"); err != nil {
		t.Fatalf("continue launch: %v", err)
	}

	handler := server.(*controlPlaneHTTPHandler)
	for range 20 {
		if err := handler.app.runWorkspaceLaunchesOnce(context.Background(), handler.service); err != nil {
			t.Fatalf("run launch: %v", err)
		}
		if len(*events) >= 10 {
			break
		}
	}
	operations, err := store.ListRuntimeOperations(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("read launch operations=%#v err=%v", operations, err)
	}
	key, monthlyCompute, monthlyStorage, debit, firstFabricStage := -1, -1, -1, -1, -1
	for index, event := range *events {
		switch event {
		case "sub2api.workspace-key":
			key = index
		case "fabric.monthly.compute":
			monthlyCompute = index
		case "fabric.monthly.storage":
			monthlyStorage = index
		case "sub2api.charge":
			debit = index
		}
		if firstFabricStage < 0 && strings.HasPrefix(event, "fabric.stage.") {
			firstFabricStage = index
		}
	}
	if key < 0 || monthlyCompute < 0 || monthlyStorage < 0 || debit < 0 || firstFabricStage < 0 ||
		key >= monthlyCompute || monthlyCompute >= monthlyStorage || monthlyStorage >= debit || debit >= firstFabricStage {
		t.Fatalf("want key < compute < storage < debit < first Fabric stage: events=%#v", *events)
	}
}

func TestWorkspaceLaunchNormalPostWorkerContinuesFreshRuntimePendingReadOnly(t *testing.T) {
	t.Setenv(controlledBasicPilotEnabledEnv, "1")
	t.Setenv(controlledBasicPilotAccountsEnv, "acct-alpha")
	t.Setenv("OPL_TENCENT_ZONE", "ap-guangzhou-1")
	t.Setenv("OPL_WORKSPACE_LAUNCH_WORKER_ENABLED", "0")

	server, store, _, fabric, events := newWorkspaceLaunchMonthlyPreflightFixture(t, "")
	fabric.runtimePending = true
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	response := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/workspace-launches",
		`{"name":"Fresh runtime pending","packageId":"basic","autoRenew":false}`,
		"fresh-runtime-pending")
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := continueWorkspaceLaunchKeyForMonthlyPreflightTest(t, server, session, "fresh-runtime-pending"); err != nil {
		t.Fatalf("continue launch: %v", err)
	}
	handler := server.(*controlPlaneHTTPHandler)
	var pending workspaceLaunchReconcileOperation
	for range len(workspaceLaunchReconcileStages) {
		if err := handler.app.runWorkspaceLaunchesOnce(context.Background(), handler.service); err != nil {
			t.Fatalf("run launch to runtime pending: %v", err)
		}
		rows, err := store.ListRuntimeOperations(context.Background())
		if err != nil || len(rows) != 1 {
			t.Fatalf("read launch operations=%#v err=%v", rows, err)
		}
		pending, err = decodeWorkspaceLaunchReconcileOperation(rows[0])
		if err != nil {
			t.Fatal(err)
		}
		if pending.Stage == "runtime" && pending.Status == "pending" && len(pending.FreshContinuationAuthorizations) == 1 {
			break
		}
	}
	attempt := pending.Attempts["runtime"]
	authorization := pending.FreshContinuationAuthorizations["runtime"]
	if pending.Stage != "runtime" || pending.Status != "pending" || attempt.Attempted != 1 || attempt.Confirmed != 0 || attempt.Unknown != 0 ||
		attempt.PendingReadbacks != 1 || attempt.MaxPendingReadbacks != 3 || authorization.Status != "active" || pending.ResumeAuthorization != nil ||
		fabric.runtimeEnsureCalls != 1 || fabric.runtimeReadCalls != 2 {
		t.Fatalf("normal POST did not park fresh runtime pending read-only: operation=%s authorization=%#v ensure=%d reads=%d events=%#v",
			workspaceLaunchReconcileResultSummary(pending), authorization, fabric.runtimeEnsureCalls, fabric.runtimeReadCalls, *events)
	}

	fabric.runtimeReady = true
	if err := handler.app.runWorkspaceLaunchesOnce(context.Background(), handler.service); err != nil {
		t.Fatalf("continue runtime owner read: %v", err)
	}
	rows, err := store.ListRuntimeOperations(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("read continued launch operations=%#v err=%v", rows, err)
	}
	continued, err := decodeWorkspaceLaunchReconcileOperation(rows[0])
	if err != nil || continued.Stage != "activation" || continued.Status != "pending" || continued.Attempts["runtime"].Confirmed != 1 ||
		continued.Attempts["runtime"].PendingReadbacks != 2 || continued.FreshContinuationAuthorizations["runtime"].Status != "consumed" ||
		fabric.runtimeEnsureCalls != 1 || fabric.runtimeReadCalls != 3 {
		t.Fatalf("runtime continuation did not converge read-only: operation=%s ensure=%d reads=%d err=%v events=%#v",
			workspaceLaunchReconcileResultSummary(continued), fabric.runtimeEnsureCalls, fabric.runtimeReadCalls, err, *events)
	}
	for range 2 {
		if err := handler.app.runWorkspaceLaunchesOnce(context.Background(), handler.service); err != nil {
			t.Fatalf("complete launch after runtime ready: %v", err)
		}
	}
	rows, err = store.ListRuntimeOperations(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("read terminal launch operations=%#v err=%v", rows, err)
	}
	terminal, err := decodeWorkspaceLaunchReconcileOperation(rows[0])
	if err != nil || terminal.Status != "succeeded" || terminal.Stage != "succeeded" || terminal.ID != pending.ID ||
		terminal.stringFact("workspaceId") != pending.stringFact("workspaceId") || fabric.runtimeEnsureCalls != 1 {
		t.Fatalf("fresh runtime continuation did not reach same-operation terminal: operation=%s ensure=%d err=%v", workspaceLaunchReconcileResultSummary(terminal), fabric.runtimeEnsureCalls, err)
	}
	wantOrder := []string{"fabric.read.runtime.absent", "fabric.stage.runtime", "fabric.read.runtime.pending", "fabric.read.runtime.ready"}
	position := 0
	for _, event := range *events {
		if position < len(wantOrder) && event == wantOrder[position] {
			position++
		}
	}
	if position != len(wantOrder) {
		t.Fatalf("runtime owner read/mutate order mismatch: want=%#v events=%#v", wantOrder, *events)
	}
}
