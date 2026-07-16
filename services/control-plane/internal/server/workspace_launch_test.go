package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func TestWorkspaceLaunchOperationRoundTripsWithoutSecrets(t *testing.T) {
	input := workspaceLaunchOperation{
		ID: "launch-alpha", Status: "preparing", RequestHash: "hash", Phase: "compute",
		AccountID: "acct-alpha", OwnerUserID: "usr-alpha", WorkspaceID: "ws-alpha", Name: "Alpha", PackageID: "basic",
		StorageGB: 10, PricingVersion: "2026-07-16", TotalMonthlyPriceCNYCents: 36_800, TotalChargeUSDMicros: 52_571_429,
		ComputeID: "ca-alpha", ComputeBillingOperationID: "billing-compute-alpha",
		StorageID: "vol-alpha", StorageBillingOperationID: "billing-storage-alpha",
		AttachmentID: "attachment-alpha", AttachmentOperationID: "attach-operation-alpha", WorkspaceOperationID: "workspace-operation-alpha",
	}
	row := workspaceLaunchOperationRow(input)
	decoded, err := decodeWorkspaceLaunchOperation(row)
	if err != nil || decoded.RequestHash != input.RequestHash || decoded.ID != input.ID || decoded.Status != input.Status {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
	if row["action"] != "workspace.launch" || row["resourceKind"] != "workspace_launch" || row["computeAllocationId"] != input.ComputeID || row["storageId"] != input.StorageID {
		t.Fatalf("workspace launch row = %#v", row)
	}
	encoded := stringValue(row["result"])
	for _, forbidden := range []string{"password", "apiKey", "redeemCode", "rawProvider"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("encoded launch contains %q: %s", forbidden, encoded)
		}
	}
}

func TestWorkspaceLaunchResponseAllowsOnlyCustomerSafeFields(t *testing.T) {
	operation := workspaceLaunchOperation{
		ID: "launch-alpha", Status: "retryable", RequestHash: "hash", Phase: "runtime",
		AccountID: "acct-alpha", OwnerUserID: "usr-private", WorkspaceID: "ws-alpha", Name: "Alpha", PackageID: "basic",
		StorageGB: 10, PricingVersion: "2026-07-16", TotalMonthlyPriceCNYCents: 36_800, TotalChargeUSDMicros: 52_571_429,
		ComputeID: "ca-alpha", ComputeBillingOperationID: "billing-compute-private",
		StorageID: "vol-alpha", StorageBillingOperationID: "billing-storage-private",
		AttachmentID: "attachment-alpha", AttachmentOperationID: "attachment-operation-private", WorkspaceOperationID: "workspace-operation-private",
		ErrorCode: "upstream_unavailable",
	}
	row := workspaceLaunchOperationRow(operation)
	var persisted map[string]any
	if err := json.Unmarshal([]byte(stringValue(row["result"])), &persisted); err != nil {
		t.Fatal(err)
	}
	persisted["dependencyError"] = "private upstream detail"
	persisted["password"] = "private-password"
	encoded, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	row["result"] = string(encoded)
	row["internalDependencyError"] = "private row detail"

	response, err := workspaceLaunchResponse(row)
	if err != nil {
		t.Fatal(err)
	}
	if response["operationId"] != operation.ID || response["status"] != operation.Status || response["phase"] != operation.Phase || response["errorCode"] != operation.ErrorCode {
		t.Fatalf("workspace launch response = %#v", response)
	}
	responseJSON, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"usr-private", "billing-compute-private", "billing-storage-private", "attachment-operation-private", "workspace-operation-private", "private upstream detail", "private-password", "private row detail"} {
		if strings.Contains(string(responseJSON), forbidden) {
			t.Fatalf("workspace launch response leaked %q: %s", forbidden, responseJSON)
		}
	}
}

type workspaceLaunchHTTPFixture struct {
	server  http.Handler
	store   *memoryTableStore
	session *httptest.ResponseRecorder
	events  *[]string
	sub2API *monthlySub2API
	fabric  *monthlyFabric
}

func newWorkspaceLaunchHTTPFixture(t *testing.T, balances ...int64) workspaceLaunchHTTPFixture {
	t.Helper()
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	events := []string{}
	sub2API := &monthlySub2API{events: &events, balances: balances}
	fabric := &monthlyFabric{events: &events}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, fabric, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	return workspaceLaunchHTTPFixture{
		server: server, store: store, session: loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!"),
		events: &events, sub2API: sub2API, fabric: fabric,
	}
}

func (f workspaceLaunchHTTPFixture) launch(t *testing.T, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	return requestWithMutationKeyForTest(t, f.server, f.session, http.MethodPost, "/api/workspace-launches", body, key)
}

func TestWorkspaceLaunchTotalPreflightRejectsInsufficientBalanceWithoutSideEffects(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 52_571_428)
	response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10}`, "launch-alpha")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), errMonthlyInsufficientBalance.Error()) {
		t.Fatalf("insufficient launch status = %d, want 409: %s", response.Code, response.Body.String())
	}
	if want := []string{"fabric.monthly.preflight", "fabric.monthly.preflight", "sub2api.balance"}; !reflect.DeepEqual(*fixture.events, want) {
		t.Fatalf("preflight events = %#v, want %#v", *fixture.events, want)
	}
	operations, _ := fixture.store.ListRuntimeOperations(context.Background())
	computes, _ := fixture.store.ListComputes(context.Background(), "acct-alpha")
	storages, _ := fixture.store.ListStorages(context.Background(), "acct-alpha")
	if len(fixture.sub2API.charges) != 0 || len(fixture.sub2API.refunds) != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 || len(operations) != 0 || len(computes) != 0 || len(storages) != 0 {
		t.Fatalf("insufficient launch caused side effects: charges=%#v refunds=%#v compute=%#v storage=%#v operations=%#v", fixture.sub2API.charges, fixture.sub2API.refunds, fixture.fabric.computeIDs, fixture.fabric.storageIDs, operations)
	}
}

func TestWorkspaceLaunchReplayAndFingerprintConflictAvoidExternalSideEffects(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	body := `{"name":"Alpha","packageId":"basic","sizeGb":10}`
	first := fixture.launch(t, body, "launch-alpha")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first launch status = %d, want 202: %s", first.Code, first.Body.String())
	}
	var original map[string]any
	if err := json.NewDecoder(first.Body).Decode(&original); err != nil {
		t.Fatal(err)
	}
	eventCount := len(*fixture.events)
	replay := fixture.launch(t, body, "launch-alpha")
	var replayed map[string]any
	if err := json.NewDecoder(replay.Body).Decode(&replayed); err != nil {
		t.Fatal(err)
	}
	if replay.Code != http.StatusAccepted || replayed["operationId"] != original["operationId"] || len(*fixture.events) != eventCount {
		t.Fatalf("launch replay = status %d body %#v events %#v", replay.Code, replayed, *fixture.events)
	}
	for _, changed := range []string{
		`{"name":"Beta","packageId":"basic","sizeGb":10}`,
		`{"name":"Alpha","packageId":"pro","sizeGb":10}`,
		`{"name":"Alpha","packageId":"basic","sizeGb":20}`,
	} {
		conflict := fixture.launch(t, changed, "launch-alpha")
		if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), errIdempotencyConflict.Error()) {
			t.Fatalf("changed launch status = %d, want 409: %s", conflict.Code, conflict.Body.String())
		}
	}
	operations, _ := fixture.store.ListRuntimeOperations(context.Background())
	if len(*fixture.events) != eventCount || len(operations) != 1 || len(fixture.sub2API.charges) != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("launch replay caused side effects: events=%#v operations=%#v", *fixture.events, operations)
	}
}

func TestWorkspaceLaunchPreflightGuardsRunBeforeExternalCalls(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, workspaceLaunchHTTPFixture)
		code  string
	}{
		{
			name: "reconciliation guard",
			setup: func(t *testing.T, fixture workspaceLaunchHTTPFixture) {
				mustStore(t, fixture.store.SaveBillingReconciliation(context.Background(), map[string]any{"id": "global", "guard": map[string]any{"blockNewWorkspaces": true}}))
			},
			code: "billing_reconciliation_blocked",
		},
		{
			name: "existing primary Workspace",
			setup: func(t *testing.T, fixture workspaceLaunchHTTPFixture) {
				mustStore(t, fixture.store.SaveWorkspace(context.Background(), map[string]any{"id": primaryWorkspaceID("acct-alpha"), "accountId": "acct-alpha", "status": "running"}))
			},
			code: errPrimaryWorkspaceExists.Error(),
		},
		{
			name: "different active launch",
			setup: func(t *testing.T, fixture workspaceLaunchHTTPFixture) {
				mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(workspaceLaunchOperation{
					ID: "launch-other", Status: "preparing", RequestHash: "other", Phase: "compute", AccountID: "acct-alpha", OwnerUserID: "usr-alpha",
					WorkspaceID: primaryWorkspaceID("acct-alpha"), PackageID: "basic", StorageGB: 10,
				})))
			},
			code: "workspace_launch_in_progress",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newWorkspaceLaunchHTTPFixture(t)
			tt.setup(t, fixture)
			response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10}`, "launch-alpha")
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), tt.code) {
				t.Fatalf("guarded launch status = %d, want 409 %s: %s", response.Code, tt.code, response.Body.String())
			}
			if len(*fixture.events) != 0 {
				t.Fatalf("guarded launch reached dependencies: %#v", *fixture.events)
			}
		})
	}
}

func TestWorkspaceLaunchListAndDetailAreTenantScoped(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	created := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10}`, "launch-alpha")
	if created.Code != http.StatusAccepted {
		t.Fatalf("launch status = %d: %s", created.Code, created.Body.String())
	}
	var launch map[string]any
	if err := json.NewDecoder(created.Body).Decode(&launch); err != nil {
		t.Fatal(err)
	}
	operationID := stringValue(launch["operationId"])

	alphaList := requestWithSession(t, fixture.server, fixture.session, http.MethodGet, "/api/workspace-launches", "")
	if alphaList.Code != http.StatusOK || !strings.Contains(alphaList.Body.String(), operationID) || strings.Contains(alphaList.Body.String(), "usr-alpha") {
		t.Fatalf("alpha launch list status=%d body=%s", alphaList.Code, alphaList.Body.String())
	}
	alphaDetail := requestWithSession(t, fixture.server, fixture.session, http.MethodGet, "/api/workspace-launches/"+operationID, "")
	if alphaDetail.Code != http.StatusOK || !strings.Contains(alphaDetail.Body.String(), operationID) {
		t.Fatalf("alpha launch detail status=%d body=%s", alphaDetail.Code, alphaDetail.Body.String())
	}

	seedTenantMember(t, fixture.store, "acct-beta", "org-beta", "usr-beta", "beta@example.com")
	betaSession := loginForTest(t, fixture.server, "beta@example.com", "CorrectHorseBatteryStaple!")
	betaList := requestWithSession(t, fixture.server, betaSession, http.MethodGet, "/api/workspace-launches", "")
	if betaList.Code != http.StatusOK || strings.TrimSpace(betaList.Body.String()) != "[]" {
		t.Fatalf("beta launch list status=%d body=%s", betaList.Code, betaList.Body.String())
	}
	for _, id := range []string{operationID, "launch-missing"} {
		response := requestWithSession(t, fixture.server, betaSession, http.MethodGet, "/api/workspace-launches/"+id, "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("beta launch detail %s status=%d, want 404: %s", id, response.Code, response.Body.String())
		}
	}
}

type recordingWorkspaceLaunchStore struct {
	*memoryTableStore
	launchSaves []workspaceLaunchOperation
}

func (s *recordingWorkspaceLaunchStore) SaveRuntimeOperation(ctx context.Context, row map[string]any) error {
	if err := s.memoryTableStore.SaveRuntimeOperation(ctx, row); err != nil {
		return err
	}
	if stringValue(row["action"]) == "workspace.launch" {
		operation, err := decodeWorkspaceLaunchOperation(row)
		if err == nil {
			s.launchSaves = append(s.launchSaves, operation)
		}
	}
	return nil
}

type workspaceLaunchLedger struct {
	fakeLedgerClient
	events                *[]string
	receipts              map[string]clients.Receipt
	workspaceReceiptCalls int
}

func (l *workspaceLaunchLedger) RecordReceipt(_ context.Context, input clients.ReceiptInput, key string) (clients.Receipt, error) {
	if input.Type == "workspace.created" {
		*l.events = append(*l.events, "ledger.workspace.receipt")
		l.workspaceReceiptCalls++
	}
	if receipt, ok := l.receipts[key]; ok {
		return receipt, nil
	}
	receipt := clients.Receipt{ReceiptInput: input, ReceiptID: "receipt-" + stableID(key)[:12]}
	l.receipts[key] = receipt
	return receipt, nil
}

type workspaceLaunchSub2API struct{ *monthlySub2API }

func (s *workspaceLaunchSub2API) WorkspaceKey(_ context.Context, userID int64) (clients.Sub2APIWorkspaceKey, error) {
	return clients.Sub2APIWorkspaceKey{ID: 9, UserID: userID, Name: "opl-workspace", Key: "workspace-key-secret", Status: "active"}, nil
}

type workspaceLaunchWorkerFixture struct {
	app         *controlPlaneServer
	service     *controlplane.Service
	store       *recordingWorkspaceLaunchStore
	events      *[]string
	sub2API     *workspaceLaunchSub2API
	fabric      *monthlyFabric
	ledger      *workspaceLaunchLedger
	operationID string
}

func newWorkspaceLaunchWorkerFixture(t *testing.T, balances []int64, chargeErrors []error, runtimeErr error) workspaceLaunchWorkerFixture {
	t.Helper()
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	store := &recordingWorkspaceLaunchStore{memoryTableStore: newMemoryTableStore()}
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	events := []string{}
	sub2API := &workspaceLaunchSub2API{monthlySub2API: &monthlySub2API{events: &events, balances: balances, chargeErrors: chargeErrors}}
	fabric := &monthlyFabric{fakeFabricClient: fakeFabricClient{calls: &events, runtimeErr: runtimeErr}, events: &events}
	ledger := &workspaceLaunchLedger{events: &events, receipts: map[string]clients.Receipt{}}
	service := controlplane.NewService(ledger, fabric, sub2API)
	server, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatal(err)
	}
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	created := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/workspace-launches", `{"name":"Alpha","packageId":"basic","sizeGb":10}`, "launch-alpha")
	if created.Code != http.StatusAccepted {
		t.Fatalf("launch status = %d: %s", created.Code, created.Body.String())
	}
	var response map[string]any
	if err := json.NewDecoder(created.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	app, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	return workspaceLaunchWorkerFixture{
		app: app, service: service, store: store, events: &events, sub2API: sub2API, fabric: fabric, ledger: ledger,
		operationID: stringValue(response["operationId"]),
	}
}

func (f workspaceLaunchWorkerFixture) operation(t *testing.T) workspaceLaunchOperation {
	t.Helper()
	rows, err := f.store.ListRuntimeOperations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if stringValue(row["id"]) == f.operationID {
			operation, err := decodeWorkspaceLaunchOperation(row)
			if err != nil {
				t.Fatal(err)
			}
			return operation
		}
	}
	t.Fatalf("workspace launch %s not found", f.operationID)
	return workspaceLaunchOperation{}
}

func TestWorkspaceLaunchOrderPersistsEveryPhase(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t,
		[]int64{1_000_000_000, 1_000_000_000, 950_000_000, 950_000_000, 947_428_571}, nil, nil,
	)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	wantEvents := []string{
		"fabric.monthly.preflight", "fabric.monthly.preflight", "sub2api.balance",
		"fabric.monthly.preflight", "sub2api.balance", "sub2api.charge", "sub2api.balance", "fabric.compute.prepare",
		"fabric.monthly.preflight", "sub2api.balance", "sub2api.charge", "sub2api.balance", "fabric.storage.prepare",
		"fabric.attachment", "fabric.gateway-secret", "fabric.runtime", "ledger.workspace.receipt",
	}
	if !reflect.DeepEqual(*fixture.events, wantEvents) {
		t.Fatalf("workspace launch events = %#v, want %#v", *fixture.events, wantEvents)
	}
	wantSaves := []string{"preparing/compute", "preparing/storage", "preparing/attachment", "preparing/workspace", "preparing/receipt", "succeeded/complete"}
	gotSaves := make([]string, 0, len(fixture.store.launchSaves))
	for _, operation := range fixture.store.launchSaves {
		gotSaves = append(gotSaves, operation.Status+"/"+operation.Phase)
	}
	if !reflect.DeepEqual(gotSaves, wantSaves) {
		t.Fatalf("workspace launch saves = %#v, want %#v", gotSaves, wantSaves)
	}
	operation := fixture.operation(t)
	if operation.Status != "succeeded" || operation.Phase != "complete" || operation.AttachmentID == "" || operation.URL == "" || operation.ReceiptID == "" {
		t.Fatalf("completed workspace launch = %#v", operation)
	}
	workspaces, _ := fixture.store.ListWorkspaces(context.Background(), "acct-alpha")
	if len(workspaces) != 1 || stringValue(workspaces[0]["receiptId"]) == "" || strings.Contains(string(mustJSON(workspaces[0])), "runtime-password-alpha") {
		t.Fatalf("workspace projection = %#v", workspaces)
	}

	before := append([]string(nil), (*fixture.events)...)
	restarted, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*fixture.events, before) {
		t.Fatalf("completed launch replayed side effects: before=%#v after=%#v", before, *fixture.events)
	}
}

func TestWorkspaceLaunchRestartResumesWithoutRepeatingCompletedPurchases(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t,
		[]int64{1_000_000_000, 1_000_000_000, 950_000_000, 950_000_000, 947_428_571}, nil, errors.New("runtime unavailable"),
	)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("runtime failure did not leave a retryable launch")
	}
	failed := fixture.operation(t)
	if failed.Status != "retryable" || failed.Phase != "workspace" {
		t.Fatalf("failed workspace launch = %#v", failed)
	}
	charges, computeCreates, storageCreates := len(fixture.sub2API.charges), len(fixture.fabric.computeIDs), len(fixture.fabric.storageIDs)
	attachments := countStrings(*fixture.events, "fabric.attachment")
	fixture.fabric.runtimeErr = nil
	restarted, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if len(fixture.sub2API.charges) != charges || len(fixture.fabric.computeIDs) != computeCreates || len(fixture.fabric.storageIDs) != storageCreates || countStrings(*fixture.events, "fabric.attachment") != attachments {
		t.Fatalf("restart repeated completed work: events=%#v charges=%#v compute=%#v storage=%#v", *fixture.events, fixture.sub2API.charges, fixture.fabric.computeIDs, fixture.fabric.storageIDs)
	}
	if operation := fixture.operation(t); operation.Status != "succeeded" || operation.Phase != "complete" || fixture.ledger.workspaceReceiptCalls != 1 {
		t.Fatalf("resumed workspace launch = %#v receipt calls=%d", operation, fixture.ledger.workspaceReceiptCalls)
	}
}

func TestWorkspaceLaunchManualReviewStopsBeforeProviderMutation(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t,
		[]int64{1_000_000_000, 1_000_000_000}, []error{clients.ErrSub2APIChargeConflict}, nil,
	)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("charge conflict did not report the manual-review launch")
	}
	operation := fixture.operation(t)
	if operation.Status != "manual_review" || operation.Phase != "compute" {
		t.Fatalf("manual-review workspace launch = %#v", operation)
	}
	if len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 || countStrings(*fixture.events, "fabric.attachment") != 0 {
		t.Fatalf("manual-review launch continued: events=%#v", *fixture.events)
	}
}

func countStrings(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}
