package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func TestWorkspaceLaunchOperationRoundTripsWithoutSecrets(t *testing.T) {
	input := workspaceLaunchOperation{
		ID: "launch-alpha", Status: "preparing", RequestHash: "hash", Phase: "compute",
		AccountID: "acct-alpha", OwnerUserID: "usr-alpha", WorkspaceID: "ws-alpha", Name: "Alpha", PackageID: "basic",
		StorageGB: 10, PricingVersion: pilotPriceVersion, TotalMonthlyPriceCNYCents: 36_800, TotalChargeUSDMicros: 52_580_000,
		ComputeID: "ca-alpha", ComputeBillingOperationID: "billing-compute-alpha",
		StorageID: "vol-alpha", StorageBillingOperationID: "billing-storage-alpha",
		AttachmentID: "attachment-alpha", AttachmentOperationID: "attach-operation-alpha", WorkspaceOperationID: "workspace-operation-alpha",
	}
	row := workspaceLaunchOperationRow(input)
	decoded, err := decodeWorkspaceLaunchOperation(row)
	if err != nil || decoded.RequestHash != input.RequestHash || decoded.ID != input.ID || decoded.Status != input.Status || decoded.PriceVersion != pilotPriceVersion || decoded.PricingVersion != "" || decoded.TotalMonthlyPriceCNYCents != 0 {
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
		StorageGB: 10, PricingVersion: pilotPriceVersion, TotalMonthlyPriceCNYCents: 36_800, TotalChargeUSDMicros: 52_580_000,
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
	if response["priceVersion"] != pilotPriceVersion || response["autoRenew"] != false || response["totalChargeUsdMicros"] != int64(52_580_000) {
		t.Fatalf("workspace launch pricing response = %#v", response)
	}
	for _, forbidden := range []string{"pricingVersion", "totalMonthlyPriceCnyCents"} {
		if _, ok := response[forbidden]; ok {
			t.Fatalf("workspace launch response exposed %s: %#v", forbidden, response)
		}
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
	promoteWorkspaceLaunchOwner(t, store, "usr-alpha")
	events := []string{}
	sub2API := &monthlySub2API{events: &events, balances: balances}
	fabric := &monthlyFabric{events: &events}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, fabric, &workspaceLaunchSub2API{monthlySub2API: sub2API}), store)
	if err != nil {
		t.Fatal(err)
	}
	return workspaceLaunchHTTPFixture{
		server: server, store: store, session: loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!"),
		events: &events, sub2API: sub2API, fabric: fabric,
	}
}

func promoteWorkspaceLaunchOwner(t *testing.T, store controlPlaneTableStore, userID string) {
	t.Helper()
	users, err := store.ListUsers(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	user := findRecord(users, userID)
	if user == nil {
		t.Fatalf("workspace launch owner %s not found", userID)
	}
	user["role"] = "owner"
	mustStore(t, store.SaveUser(context.Background(), user))
}

func (f workspaceLaunchHTTPFixture) launch(t *testing.T, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	return requestWithMutationKeyForTest(t, f.server, f.session, http.MethodPost, "/api/workspace-launches", body, key)
}

func TestWorkspaceLaunchRequiresCompleteBodyBeforeExternalCalls(t *testing.T) {
	for name, input := range map[string]struct{ body, errorCode string }{
		"name":             {body: `{"packageId":"basic","sizeGb":10,"autoRenew":false}`},
		"packageId":        {body: `{"name":"Alpha","sizeGb":10,"autoRenew":false}`},
		"sizeGb":           {body: `{"name":"Alpha","packageId":"basic","autoRenew":false}`},
		"autoRenew":        {body: `{"name":"Alpha","packageId":"basic","sizeGb":10}`, errorCode: "autoRenew_required"},
		"autoRenew string": {body: `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":"false"}`, errorCode: "autoRenew_required"},
		"autoRenew number": {body: `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":0}`, errorCode: "autoRenew_required"},
		"autoRenew null":   {body: `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":null}`, errorCode: "autoRenew_required"},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
			response := fixture.launch(t, input.body, "launch-alpha")
			operations, _ := fixture.store.ListRuntimeOperations(context.Background())
			if response.Code != http.StatusBadRequest || input.errorCode != "" && !strings.Contains(response.Body.String(), input.errorCode) || len(*fixture.events) != 0 || len(operations) != 0 {
				t.Fatalf("missing %s status=%d body=%s events=%#v operations=%#v", name, response.Code, response.Body.String(), *fixture.events, operations)
			}
		})
	}
}

func TestWorkspaceLaunchRejectsUnknownAndCrossPackageStorageBeforeExternalCalls(t *testing.T) {
	for _, body := range []string{
		`{"name":"Alpha","packageId":"basic","sizeGb":100,"autoRenew":false}`,
		`{"name":"Alpha","packageId":"pro","sizeGb":10,"autoRenew":false}`,
		`{"name":"Alpha","packageId":"enterprise","sizeGb":10,"autoRenew":false}`,
	} {
		fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
		response := fixture.launch(t, body, "launch-alpha")
		operations, _ := fixture.store.ListRuntimeOperations(context.Background())
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_pricing_input") || len(*fixture.events) != 0 || len(operations) != 0 {
			t.Fatalf("invalid package/storage status=%d body=%s events=%#v operations=%#v", response.Code, response.Body.String(), *fixture.events, operations)
		}
	}
}

func TestWorkspaceLaunchTotalPreflightRejectsInsufficientBalanceWithoutSideEffects(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 52_579_999)
	response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-alpha")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), errMonthlyInsufficientBalance.Error()) {
		t.Fatalf("insufficient launch status = %d, want 409: %s", response.Code, response.Body.String())
	}
	if want := []string{"fabric.monthly.preflight", "fabric.monthly.preflight", "sub2api.workspace_key", "sub2api.balance"}; !reflect.DeepEqual(*fixture.events, want) {
		t.Fatalf("preflight events = %#v, want %#v", *fixture.events, want)
	}
	operations, _ := fixture.store.ListRuntimeOperations(context.Background())
	computes, _ := fixture.store.ListComputes(context.Background(), "acct-alpha")
	storages, _ := fixture.store.ListStorages(context.Background(), "acct-alpha")
	if len(fixture.sub2API.charges) != 0 || len(fixture.sub2API.refunds) != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 || len(operations) != 0 || len(computes) != 0 || len(storages) != 0 {
		t.Fatalf("insufficient launch caused side effects: charges=%#v refunds=%#v compute=%#v storage=%#v operations=%#v", fixture.sub2API.charges, fixture.sub2API.refunds, fixture.fabric.computeIDs, fixture.fabric.storageIDs, operations)
	}
}

func TestWorkspaceLaunchGatewayKeyPreflightFailsBeforeBalanceAndSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "missing", err: clients.ErrSub2APIWorkspaceKeyMissing, wantStatus: http.StatusConflict, wantCode: "gateway_key_missing"},
		{name: "ambiguous", err: clients.ErrSub2APIWorkspaceKeyAmbiguous, wantStatus: http.StatusConflict, wantCode: "gateway_key_ambiguous"},
		{name: "unavailable", err: errors.New("Sub2API unavailable"), wantStatus: http.StatusBadGateway, wantCode: "upstream_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
			fixture.sub2API.workspaceKeyErr = tc.err
			response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-alpha")
			if response.Code != tc.wantStatus || !strings.Contains(response.Body.String(), tc.wantCode) {
				t.Fatalf("Gateway Key launch status = %d, want %d %s: %s", response.Code, tc.wantStatus, tc.wantCode, response.Body.String())
			}
			wantEvents := []string{"fabric.monthly.preflight", "fabric.monthly.preflight", "sub2api.workspace_key"}
			operations, _ := fixture.store.ListRuntimeOperations(context.Background())
			if !reflect.DeepEqual(*fixture.events, wantEvents) || len(operations) != 0 || len(fixture.sub2API.charges) != 0 || len(fixture.sub2API.refunds) != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
				t.Fatalf("Gateway Key failure caused side effects: events=%#v operations=%#v charges=%#v refunds=%#v compute=%#v storage=%#v", *fixture.events, operations, fixture.sub2API.charges, fixture.sub2API.refunds, fixture.fabric.computeIDs, fixture.fabric.storageIDs)
			}
		})
	}
}

func TestWorkspaceLaunchReplayAndFingerprintConflictAvoidExternalSideEffects(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	body := `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`
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
		`{"name":"Beta","packageId":"basic","sizeGb":10,"autoRenew":false}`,
		`{"name":"Alpha","packageId":"pro","sizeGb":100,"autoRenew":false}`,
		`{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":true}`,
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
			response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-alpha")
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), tt.code) {
				t.Fatalf("guarded launch status = %d, want 409 %s: %s", response.Code, tt.code, response.Body.String())
			}
			if len(*fixture.events) != 0 {
				t.Fatalf("guarded launch reached dependencies: %#v", *fixture.events)
			}
		})
	}
}

func TestWorkspaceLaunchRequiresOwnerBeforeExternalCalls(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	users, err := fixture.store.ListUsers(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	user := findRecord(users, "usr-alpha")
	user["role"] = "member"
	mustStore(t, fixture.store.SaveUser(context.Background(), user))

	response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-alpha")
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "workspace_owner_required") {
		t.Fatalf("member launch status = %d, want 403: %s", response.Code, response.Body.String())
	}
	operations, _ := fixture.store.ListRuntimeOperations(context.Background())
	if len(*fixture.events) != 0 || len(operations) != 0 {
		t.Fatalf("member launch reached dependencies: events=%#v operations=%#v", *fixture.events, operations)
	}
}

func TestWorkspaceLaunchListAndDetailAreTenantScoped(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	created := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":true}`, "launch-alpha")
	if created.Code != http.StatusAccepted {
		t.Fatalf("launch status = %d: %s", created.Code, created.Body.String())
	}
	var launch map[string]any
	if err := json.NewDecoder(created.Body).Decode(&launch); err != nil {
		t.Fatal(err)
	}
	if launch["autoRenew"] != true || launch["priceVersion"] != pilotPriceVersion || launch["totalChargeUsdMicros"] != float64(52_580_000) || strings.Contains(created.Body.String(), "pricingVersion") || strings.Contains(created.Body.String(), "totalMonthlyPriceCnyCents") {
		t.Fatalf("created launch projection = %#v", launch)
	}
	operationID := stringValue(launch["operationId"])

	alphaList := requestWithSession(t, fixture.server, fixture.session, http.MethodGet, "/api/workspace-launches", "")
	if alphaList.Code != http.StatusOK || !strings.Contains(alphaList.Body.String(), operationID) || !strings.Contains(alphaList.Body.String(), `"autoRenew":true`) || !strings.Contains(alphaList.Body.String(), `"priceVersion":"pilot-usd-2026-07-v1"`) || strings.Contains(alphaList.Body.String(), "usr-alpha") || strings.Contains(alphaList.Body.String(), "pricingVersion") {
		t.Fatalf("alpha launch list status=%d body=%s", alphaList.Code, alphaList.Body.String())
	}
	alphaDetail := requestWithSession(t, fixture.server, fixture.session, http.MethodGet, "/api/workspace-launches/"+operationID, "")
	if alphaDetail.Code != http.StatusOK || !strings.Contains(alphaDetail.Body.String(), operationID) || !strings.Contains(alphaDetail.Body.String(), `"autoRenew":true`) || !strings.Contains(alphaDetail.Body.String(), `"priceVersion":"pilot-usd-2026-07-v1"`) || strings.Contains(alphaDetail.Body.String(), "pricingVersion") {
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

func (s *workspaceLaunchSub2API) WorkspaceKey(ctx context.Context, userID int64) (clients.Sub2APIWorkspaceKey, error) {
	*s.events = append(*s.events, "sub2api.workspace_key")
	return s.monthlySub2API.WorkspaceKey(ctx, userID)
}

type workspaceLaunchWorkerFixture struct {
	app         *controlPlaneServer
	service     *controlplane.Service
	server      http.Handler
	operator    *httptest.ResponseRecorder
	store       *recordingWorkspaceLaunchStore
	events      *[]string
	sub2API     *workspaceLaunchSub2API
	fabric      *monthlyFabric
	ledger      *workspaceLaunchLedger
	operationID string
}

func newWorkspaceLaunchWorkerFixture(t *testing.T, balances []int64, chargeErrors []error, runtimeErr error, autoRenew ...bool) workspaceLaunchWorkerFixture {
	renew := len(autoRenew) != 0 && autoRenew[0]
	return newWorkspaceLaunchWorkerFixtureForPlan(t, balances, chargeErrors, runtimeErr, "basic", 10, renew)
}

func newWorkspaceLaunchWorkerFixtureForPlan(t *testing.T, balances []int64, chargeErrors []error, runtimeErr error, packageID string, storageGB int, autoRenew bool) workspaceLaunchWorkerFixture {
	t.Helper()
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	store := &recordingWorkspaceLaunchStore{memoryTableStore: newMemoryTableStore()}
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	promoteWorkspaceLaunchOwner(t, store, "usr-alpha")
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
	created := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/workspace-launches", fmt.Sprintf(`{"name":"Alpha","packageId":%q,"sizeGb":%d,"autoRenew":%t}`, packageID, storageGB, autoRenew), "launch-alpha")
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
		app: app, service: service, server: server, operator: reservedOperatorSessionForTest(t, server), store: store, events: &events, sub2API: sub2API, fabric: fabric, ledger: ledger,
		operationID: stringValue(response["operationId"]),
	}
}

func TestWorkspaceLaunchPersistsCanonicalRenewalIntent(t *testing.T) {
	for _, tc := range []struct {
		name, packageID                                    string
		storageGB                                          int
		autoRenew                                          bool
		computeUSDMicros, storageUSDMicros, totalUSDMicros int64
	}{
		{name: "basic enabled", packageID: "basic", storageGB: 10, autoRenew: true, computeUSDMicros: 50_000_000, storageUSDMicros: 2_580_000, totalUSDMicros: 52_580_000},
		{name: "basic disabled", packageID: "basic", storageGB: 10, computeUSDMicros: 50_000_000, storageUSDMicros: 2_580_000, totalUSDMicros: 52_580_000},
		{name: "pro enabled", packageID: "pro", storageGB: 100, autoRenew: true, computeUSDMicros: 214_280_000, storageUSDMicros: 25_800_000, totalUSDMicros: 240_080_000},
		{name: "pro disabled", packageID: "pro", storageGB: 100, computeUSDMicros: 214_280_000, storageUSDMicros: 25_800_000, totalUSDMicros: 240_080_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			postCompute, postTotal := int64(1_000_000_000)-tc.computeUSDMicros, int64(1_000_000_000)-tc.totalUSDMicros
			fixture := newWorkspaceLaunchWorkerFixtureForPlan(t, []int64{1_000_000_000, 1_000_000_000, postCompute, postCompute, postTotal}, nil, nil, tc.packageID, tc.storageGB, tc.autoRenew)
			if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
				t.Fatal(err)
			}
			workspaces, err := fixture.store.ListWorkspaces(context.Background(), "acct-alpha")
			if err != nil || len(workspaces) != 1 {
				t.Fatalf("Workspace projection=%#v err=%v", workspaces, err)
			}
			workspace := workspaces[0]
			operation := fixture.operation(t)
			if workspace["autoRenew"] != tc.autoRenew || workspace["packageId"] != tc.packageID || workspace["storageGb"] != int64(tc.storageGB) ||
				workspace["priceVersion"] != pricingCatalogVersion || workspace["currency"] != pricingCurrency || workspace["billingUnit"] != pricingBillingUnit ||
				workspace["computeUsdMicros"] != tc.computeUSDMicros || workspace["storageUsdMicros"] != tc.storageUSDMicros || workspace["totalUsdMicros"] != tc.totalUSDMicros ||
				workspace["renewalStatus"] != "active" || workspace["computeAllocationId"] != operation.ComputeID || workspace["storageId"] != operation.StorageID {
				t.Fatalf("canonical Workspace renewal state=%#v", workspace)
			}
			periodStart, startErr := time.Parse(time.RFC3339, stringValue(workspace["periodStart"]))
			paidThrough, paidErr := time.Parse(time.RFC3339, stringValue(workspace["paidThrough"]))
			nextRenewal, nextErr := time.Parse(time.RFC3339, stringValue(workspace["nextRenewalAt"]))
			if startErr != nil || paidErr != nil || nextErr != nil || !paidThrough.After(periodStart) || !nextRenewal.Equal(paidThrough.Add(-24*time.Hour)) || numberField(workspace, "billingAnchorDay", 0) < 1 {
				t.Fatalf("canonical Workspace period=%#v errors=%v/%v/%v", workspace, startErr, paidErr, nextErr)
			}
			if tc.autoRenew && (workspace["authorizedBy"] != "usr-alpha" || stringValue(workspace["authorizedAt"]) == "") || !tc.autoRenew && (workspace["authorizedBy"] != "" || workspace["authorizedAt"] != "") {
				t.Fatalf("canonical Workspace authorization=%#v", workspace)
			}
		})
	}
}

func TestWorkspaceLaunchOwnerRaceDisablesRenewalIntent(t *testing.T) {
	for _, status := range []string{"disabled", "deleted"} {
		t.Run(status, func(t *testing.T) {
			fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 950_000_000, 950_000_000, 947_420_000}, nil, nil, true)
			owner, err := fixture.app.findUserByID(context.Background(), "usr-alpha")
			if err != nil || owner == nil {
				t.Fatalf("find launch owner: owner=%#v err=%v", owner, err)
			}
			owner["status"] = status
			if err := fixture.store.ApplyUserLifecycle(context.Background(), owner); err != nil {
				t.Fatal(err)
			}
			if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
				t.Fatal(err)
			}
			workspaces, _ := fixture.store.ListWorkspaces(context.Background(), "acct-alpha")
			computes, _ := fixture.store.ListComputes(context.Background(), "acct-alpha")
			storages, _ := fixture.store.ListStorages(context.Background(), "acct-alpha")
			if len(workspaces) != 1 || workspaces[0]["autoRenew"] != false || workspaces[0]["authorizedBy"] != "" || workspaces[0]["authorizedAt"] != "" ||
				len(computes) != 1 || computes[0]["autoRenew"] != false || len(storages) != 1 || storages[0]["autoRenew"] != false {
				t.Fatalf("%s owner left renewal enabled: Workspace=%#v compute=%#v storage=%#v", status, workspaces, computes, storages)
			}
		})
	}
}

func TestWorkspaceLaunchRevalidatesOwnerAndChildrenAfterRuntimeApply(t *testing.T) {
	t.Run("owner disabled", func(t *testing.T) {
		fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 950_000_000, 950_000_000, 947_420_000}, nil, nil, true)
		fixture.fabric.afterRuntime = func() {
			owner, err := fixture.app.findUserByID(context.Background(), "usr-alpha")
			if err != nil || owner == nil {
				t.Fatalf("find owner during Runtime apply: owner=%#v err=%v", owner, err)
			}
			owner["status"] = "disabled"
			if err := fixture.store.ApplyUserLifecycle(context.Background(), owner); err != nil {
				t.Fatal(err)
			}
		}
		if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
			t.Fatal(err)
		}
		workspaces, _ := fixture.store.ListWorkspaces(context.Background(), "acct-alpha")
		if len(workspaces) != 1 || workspaces[0]["autoRenew"] != false {
			t.Fatalf("owner race persisted stale renewal intent: %#v", workspaces)
		}
	})

	t.Run("attachment detached", func(t *testing.T) {
		fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 950_000_000, 950_000_000, 947_420_000}, nil, nil, true)
		fixture.fabric.afterRuntime = func() {
			attachments, err := fixture.store.ListAttachments(context.Background(), "acct-alpha")
			if err != nil || len(attachments) != 1 {
				t.Fatalf("load attachment during Runtime apply: rows=%#v err=%v", attachments, err)
			}
			attachments[0]["status"] = "detached"
			if err := fixture.store.SaveAttachment(context.Background(), attachments[0]); err != nil {
				t.Fatal(err)
			}
		}
		if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
			t.Fatal("detached child was activated after Runtime apply")
		}
		operation := fixture.operation(t)
		workspaces, _ := fixture.store.ListWorkspaces(context.Background(), "acct-alpha")
		if operation.Status != "manual_review" || len(workspaces) != 0 {
			t.Fatalf("detached child activation: operation=%#v Workspaces=%#v", operation, workspaces)
		}
	})
}

func TestWorkspaceLaunchProviderDeadlineFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, phase string
		mutate      func(*monthlyFabric)
	}{
		{name: "compute missing", phase: "compute", mutate: func(fabric *monthlyFabric) {
			fabric.mutateCompute = func(value *clients.ComputeAllocation) { value.Deadline = "" }
		}},
		{name: "compute early", phase: "compute", mutate: func(fabric *monthlyFabric) {
			fabric.mutateCompute = func(value *clients.ComputeAllocation) { value.Deadline = "2000-01-01T00:00:00Z" }
		}},
		{name: "storage missing", phase: "storage", mutate: func(fabric *monthlyFabric) {
			fabric.mutateStorage = func(value *clients.StorageVolume) { value.Deadline = "" }
		}},
		{name: "storage early", phase: "storage", mutate: func(fabric *monthlyFabric) {
			fabric.mutateStorage = func(value *clients.StorageVolume) { value.Deadline = "2000-01-01T00:00:00Z" }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 950_000_000, 950_000_000, 947_420_000}, nil, nil, true)
			tc.mutate(fixture.fabric)
			if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
				t.Fatal("invalid provider deadline did not stop launch")
			}
			operation := fixture.operation(t)
			workspaces, _ := fixture.store.ListWorkspaces(context.Background(), "acct-alpha")
			if operation.Status != "manual_review" || operation.Phase != tc.phase || len(workspaces) != 0 || countStrings(*fixture.events, "fabric.gateway-secret") != 0 || countStrings(*fixture.events, "fabric.runtime") != 0 {
				t.Fatalf("deadline failure launch=%#v Workspaces=%#v events=%#v", operation, workspaces, *fixture.events)
			}
		})
	}
}

func TestWorkspaceLaunchBillingMismatchStopsBeforeWorkspaceRuntime(t *testing.T) {
	t.Run("parent total", func(t *testing.T) {
		fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 950_000_000, 950_000_000, 947_420_000}, nil, nil, true)
		operation := fixture.operation(t)
		operation.TotalChargeUSDMicros++
		mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
		if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
			t.Fatal("mismatched parent total did not stop launch")
		}
		operation = fixture.operation(t)
		if operation.Status != "manual_review" || operation.ErrorCode != "workspace_launch_billing_price_mismatch" || countStrings(*fixture.events, "fabric.runtime") != 0 {
			t.Fatalf("parent total mismatch launch=%#v events=%#v", operation, *fixture.events)
		}
	})

	t.Run("child component", func(t *testing.T) {
		fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 950_000_000, 950_000_000, 947_420_000}, nil, errors.New("runtime unavailable"), true)
		if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
			t.Fatal("runtime failure did not pause launch")
		}
		operation := fixture.operation(t)
		compute, _ := fixture.app.getCompute(operation.ComputeID)
		compute["chargeUsdMicros"] = int64(49_000_000)
		mustStore(t, fixture.store.SaveCompute(context.Background(), compute))
		fixture.fabric.runtimeErr = nil
		beforeRuntime := countStrings(*fixture.events, "fabric.runtime")
		if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
			t.Fatal("mismatched child component did not stop launch")
		}
		operation = fixture.operation(t)
		workspaces, _ := fixture.store.ListWorkspaces(context.Background(), "acct-alpha")
		if operation.Status != "manual_review" || operation.ErrorCode != "workspace_launch_billing_price_mismatch" || len(workspaces) != 0 || countStrings(*fixture.events, "fabric.runtime") != beforeRuntime {
			t.Fatalf("child mismatch launch=%#v Workspaces=%#v events=%#v", operation, workspaces, *fixture.events)
		}
	})

	t.Run("existing canonical state", func(t *testing.T) {
		fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 950_000_000, 950_000_000, 947_420_000}, nil, errors.New("runtime unavailable"), true)
		if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
			t.Fatal("runtime failure did not pause launch")
		}
		operation := fixture.operation(t)
		state, code := fixture.app.workspaceLaunchBillingState(context.Background(), operation)
		if code != "" {
			t.Fatalf("build expected state: %s", code)
		}
		state["autoRenew"], state["authorizedBy"], state["authorizedAt"] = false, "", ""
		workspace := map[string]any{
			"id": operation.WorkspaceID, "accountId": operation.AccountID, "ownerAccountId": operation.AccountID, "ownerUserId": operation.OwnerUserID,
			"name": operation.Name, "packageId": operation.PackageID, "currentComputeAllocationId": operation.ComputeID, "computeAllocationId": operation.ComputeID,
			"storageId": operation.StorageID, "currentAttachmentId": operation.AttachmentID, "attachmentId": operation.AttachmentID, "state": "preparing", "status": "preparing",
		}
		for key, value := range state {
			workspace[key] = value
		}
		mustStore(t, fixture.store.SaveWorkspace(context.Background(), workspace))
		fixture.fabric.runtimeErr = nil
		beforeRuntime := countStrings(*fixture.events, "fabric.runtime")
		if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
			t.Fatal("conflicting Workspace state did not stop launch")
		}
		operation = fixture.operation(t)
		workspaces, _ := fixture.store.ListWorkspaces(context.Background(), "acct-alpha")
		if operation.Status != "manual_review" || operation.ErrorCode != "workspace_launch_billing_state_mismatch" || len(workspaces) != 1 || workspaces[0]["autoRenew"] != false || countStrings(*fixture.events, "fabric.runtime") != beforeRuntime {
			t.Fatalf("existing conflict launch=%#v Workspaces=%#v events=%#v", operation, workspaces, *fixture.events)
		}
	})
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
		[]int64{1_000_000_000, 1_000_000_000, 950_000_000, 950_000_000, 947_420_000}, nil, nil,
	)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	wantEvents := []string{
		"fabric.monthly.preflight", "fabric.monthly.preflight", "sub2api.workspace_key", "sub2api.balance",
		"fabric.monthly.preflight", "sub2api.balance", "sub2api.workspace_key", "sub2api.charge", "sub2api.balance", "fabric.compute.prepare",
		"fabric.monthly.preflight", "sub2api.balance", "sub2api.workspace_key", "sub2api.charge", "sub2api.balance", "fabric.storage.prepare",
		"fabric.attachment", "sub2api.workspace_key", "fabric.gateway-secret", "fabric.runtime", "ledger.workspace.receipt",
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
		[]int64{1_000_000_000, 1_000_000_000, 950_000_000, 950_000_000, 947_420_000}, nil, errors.New("runtime unavailable"),
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

func TestWorkspaceLaunchWaitsForChildResourceMutation(t *testing.T) {
	for _, resourceType := range []string{"compute", "storage"} {
		t.Run(resourceType, func(t *testing.T) {
			fixture := newWorkspaceLaunchWorkerFixture(t,
				[]int64{1_000_000_000, 1_000_000_000, 950_000_000, 950_000_000, 947_420_000}, nil, nil,
			)
			operation := fixture.operation(t)
			resourceID := operation.ComputeID
			if resourceType == "storage" {
				resourceID = operation.StorageID
			}
			unlock := fixture.app.lockResource(resourceType, resourceID)
			done := make(chan error, 1)
			go func() { done <- fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service) }()
			select {
			case err := <-done:
				unlock()
				t.Fatalf("workspace launch crossed %s lock: %v", resourceType, err)
			case <-time.After(50 * time.Millisecond):
			}
			unlock()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatalf("workspace launch did not resume after %s unlock", resourceType)
			}
		})
	}
}

func TestWorkspaceLaunchResumesAfterBalanceTopUp(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 49_999_999}, nil, nil)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("insufficient child balance did not leave a retryable launch")
	}
	operation := fixture.operation(t)
	computes, _ := fixture.store.ListComputes(context.Background(), "acct-alpha")
	if operation.Status != "retryable" || operation.Phase != "compute" || len(computes) != 1 || computes[0]["billingStatus"] != "charge_pending" {
		t.Fatalf("insufficient child balance launch=%#v computes=%#v", operation, computes)
	}
	if len(fixture.sub2API.charges) != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("insufficient child balance caused side effects: charges=%#v compute=%#v storage=%#v", fixture.sub2API.charges, fixture.fabric.computeIDs, fixture.fabric.storageIDs)
	}

	fixture.sub2API.balances = []int64{1_000_000_000, 950_000_000, 950_000_000, 947_420_000}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if operation = fixture.operation(t); operation.Status != "succeeded" || operation.Phase != "complete" {
		t.Fatalf("topped-up workspace launch = %#v", operation)
	}
	if len(fixture.sub2API.charges) != 2 || len(fixture.fabric.computeIDs) != 1 || len(fixture.fabric.storageIDs) != 1 {
		t.Fatalf("topped-up launch side effects: charges=%#v compute=%#v storage=%#v", fixture.sub2API.charges, fixture.fabric.computeIDs, fixture.fabric.storageIDs)
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

func TestWorkspaceLaunchWorkerSkipsManualReviewOperations(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t,
		[]int64{1_000_000_000, 1_000_000_000}, []error{clients.ErrSub2APIChargeConflict}, nil,
	)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("charge conflict did not report the manual-review launch")
	}
	beforeEvents := append([]string(nil), (*fixture.events)...)
	beforeSaves := len(fixture.store.launchSaves)

	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatalf("manual-review launch was replayed: %v", err)
	}
	if !reflect.DeepEqual(*fixture.events, beforeEvents) || len(fixture.store.launchSaves) != beforeSaves {
		t.Fatalf("manual-review worker caused side effects: before=%#v after=%#v saves=%d->%d", beforeEvents, *fixture.events, beforeSaves, len(fixture.store.launchSaves))
	}
	if operation := fixture.operation(t); operation.Status != "manual_review" || operation.Phase != "compute" {
		t.Fatalf("manual-review launch changed: %#v", operation)
	}
}

func TestLegacyWorkspaceLaunchWithoutChildPriceSnapshotFailsClosed(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000}, nil, nil)
	operation := fixture.operation(t)
	operation.Status = "manual_review"
	operation.ErrorCode = "workspace_launch_compute_manual_review"
	operation.PriceVersion = "legacy-usd-v1"
	operation.TotalChargeUSDMicros = 41_234_567
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
	beforeEvents, beforeSaves := append([]string(nil), (*fixture.events)...), len(fixture.store.launchSaves)

	err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
	if err == nil || !strings.Contains(err.Error(), "workspace_launch_compute_price_snapshot_unavailable") {
		t.Fatalf("legacy launch without snapshot error=%v", err)
	}
	operation = fixture.operation(t)
	if operation.Status != "manual_review" || operation.Phase != "compute" || operation.ErrorCode != "workspace_launch_compute_price_snapshot_unavailable" {
		t.Fatalf("legacy launch without snapshot=%#v", operation)
	}
	if !reflect.DeepEqual(*fixture.events, beforeEvents) || len(fixture.store.launchSaves) != beforeSaves+1 || len(fixture.sub2API.charges) != 0 || len(fixture.fabric.computeIDs) != 0 {
		t.Fatalf("legacy launch without snapshot side effects: events=%#v saves=%d charges=%#v creates=%#v", *fixture.events, len(fixture.store.launchSaves), fixture.sub2API.charges, fixture.fabric.computeIDs)
	}

	beforeEvents, beforeSaves = append([]string(nil), (*fixture.events)...), len(fixture.store.launchSaves)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil || !reflect.DeepEqual(*fixture.events, beforeEvents) || len(fixture.store.launchSaves) != beforeSaves {
		t.Fatalf("legacy snapshot review was rescanned: err=%v events=%#v saves=%d", err, *fixture.events, len(fixture.store.launchSaves))
	}
}

func TestWorkspaceLaunchBillingReviewResolutionResumesActiveChildWithoutRepeatingPurchases(t *testing.T) {
	for _, resourceType := range []string{"compute", "storage"} {
		t.Run(resourceType, func(t *testing.T) {
			fixture := newWorkspaceLaunchWorkerFixture(t,
				[]int64{1_000_000_000, 1_000_000_000, 950_000_000, 950_000_000, 947_420_000}, nil, nil, true,
			)
			if resourceType == "compute" {
				fixture.fabric.mutateCompute = func(value *clients.ComputeAllocation) { value.ChargeType = "POSTPAID_BY_HOUR" }
			} else {
				fixture.fabric.mutateStorage = func(value *clients.StorageVolume) { value.ProviderData["chargeType"] = "POSTPAID_BY_HOUR" }
			}
			if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
				t.Fatal("invalid provider truth did not enter manual review")
			}
			operation := fixture.operation(t)
			if operation.Status != "manual_review" || operation.Phase != resourceType {
				t.Fatalf("manual-review launch = %#v", operation)
			}
			charges, computeCreates, storageCreates := len(fixture.sub2API.charges), len(fixture.fabric.computeIDs), len(fixture.fabric.storageIDs)
			fixture.setConfirmedReviewSync(operation, resourceType)
			resolved := fixture.resolveReview(t, operation, resourceType, billingReviewActivateCharged)
			if resolved.Code != http.StatusOK || !strings.Contains(resolved.Body.String(), `"billingStatus":"active"`) {
				t.Fatalf("active resolution status=%d body=%s", resolved.Code, resolved.Body.String())
			}
			if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
				t.Fatal(err)
			}
			operation = fixture.operation(t)
			if operation.Status != "succeeded" || operation.Phase != "complete" || operation.ErrorCode != "" {
				t.Fatalf("resumed launch = %#v", operation)
			}
			if len(fixture.sub2API.charges) != 2 || len(fixture.fabric.computeIDs) != 1 || len(fixture.fabric.storageIDs) != 1 || (resourceType == "compute" && (charges != 1 || computeCreates != 1 || storageCreates != 0)) || (resourceType == "storage" && (charges != 2 || computeCreates != 1 || storageCreates != 1)) {
				t.Fatalf("review resume repeated purchase: charges=%#v compute=%#v storage=%#v", fixture.sub2API.charges, fixture.fabric.computeIDs, fixture.fabric.storageIDs)
			}
			computes, _ := fixture.store.ListComputes(context.Background(), "acct-alpha")
			storages, _ := fixture.store.ListStorages(context.Background(), "acct-alpha")
			if len(computes) != 1 || len(storages) != 1 || computes[0]["autoRenew"] != true || storages[0]["autoRenew"] != true {
				t.Fatalf("autoRenew projection compute=%#v storage=%#v", computes, storages)
			}
		})
	}
}

func TestWorkspaceLaunchBillingReviewResolutionMakesRefundedChildTerminal(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 950_000_000}, nil, nil)
	fixture.fabric.mutateCompute = func(value *clients.ComputeAllocation) { value.ChargeType = "POSTPAID_BY_HOUR" }
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("invalid provider truth did not enter manual review")
	}
	operation := fixture.operation(t)
	fixture.fabric.computeSync = clients.ComputeAllocation{ID: operation.ComputeID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "external_deleted"}
	resolved := fixture.resolveReview(t, operation, "compute", billingReviewRefundCharged)
	if resolved.Code != http.StatusOK || !strings.Contains(resolved.Body.String(), `"billingStatus":"refunded"`) {
		t.Fatalf("refund resolution status=%d body=%s", resolved.Code, resolved.Body.String())
	}
	charges, creates, refunds := len(fixture.sub2API.charges), len(fixture.fabric.computeIDs), len(fixture.sub2API.refunds)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if operation = fixture.operation(t); operation.Status != "refunded" || operation.Phase != "compute" {
		t.Fatalf("refunded parent launch = %#v", operation)
	}
	if len(fixture.sub2API.charges) != charges || len(fixture.fabric.computeIDs) != creates || len(fixture.sub2API.refunds) != refunds || refunds != 1 {
		t.Fatalf("refunded launch repurchased: charges=%#v creates=%#v refunds=%#v", fixture.sub2API.charges, fixture.fabric.computeIDs, fixture.sub2API.refunds)
	}
	beforeEvents, beforeSaves := append([]string(nil), (*fixture.events)...), len(fixture.store.launchSaves)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil || !reflect.DeepEqual(*fixture.events, beforeEvents) || len(fixture.store.launchSaves) != beforeSaves {
		t.Fatalf("refunded terminal launch was rescanned: err=%v events=%#v saves=%d", err, *fixture.events, len(fixture.store.launchSaves))
	}
}

func TestWorkspaceLaunchFailedChildBecomesTerminalWithoutRepurchase(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000}, []error{clients.ErrSub2APIChargeConflict}, nil)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("charge conflict did not enter manual review")
	}
	operation := fixture.operation(t)
	compute, ok := fixture.app.getCompute(operation.ComputeID)
	if !ok {
		t.Fatal("manual-review compute missing")
	}
	compute["billingStatus"] = "failed"
	mustStore(t, fixture.store.SaveCompute(context.Background(), compute))
	charges, creates := len(fixture.sub2API.charges), len(fixture.fabric.computeIDs)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if operation = fixture.operation(t); operation.Status != "failed" || operation.Phase != "compute" {
		t.Fatalf("failed parent launch = %#v", operation)
	}
	if len(fixture.sub2API.charges) != charges || len(fixture.fabric.computeIDs) != creates {
		t.Fatalf("failed child was repurchased: charges=%#v creates=%#v", fixture.sub2API.charges, fixture.fabric.computeIDs)
	}
	beforeEvents, beforeSaves := append([]string(nil), (*fixture.events)...), len(fixture.store.launchSaves)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil || !reflect.DeepEqual(*fixture.events, beforeEvents) || len(fixture.store.launchSaves) != beforeSaves {
		t.Fatalf("failed terminal launch was rescanned: err=%v events=%#v saves=%d", err, *fixture.events, len(fixture.store.launchSaves))
	}
}

func TestWorkspaceLaunchNonChildManualReviewRemainsQuiescent(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000}, nil, nil)
	operation := fixture.operation(t)
	operation.Phase, operation.Status, operation.ErrorCode = "attachment", "manual_review", "workspace_launch_attachment_identity_mismatch"
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
	beforeEvents := append([]string(nil), (*fixture.events)...)
	beforeSaves := len(fixture.store.launchSaves)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if operation = fixture.operation(t); operation.Status != "manual_review" || operation.Phase != "attachment" || !reflect.DeepEqual(*fixture.events, beforeEvents) || len(fixture.store.launchSaves) != beforeSaves {
		t.Fatalf("non-child review changed: operation=%#v events=%#v saves=%d", operation, *fixture.events, len(fixture.store.launchSaves))
	}
}

func (f workspaceLaunchWorkerFixture) setConfirmedReviewSync(operation workspaceLaunchOperation, resourceType string) {
	if resourceType == "storage" {
		f.fabric.storageSync = clients.StorageVolume{
			ID: operation.StorageID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "available",
			Provider: "tencent-tke", ProviderResourceID: "disk-" + operation.StorageID, ProviderRequestID: "req-" + operation.StorageID,
			SizeGB: operation.StorageGB, CBSStatus: "UNATTACHED", DiskType: "CLOUD_PREMIUM", Zone: "ap-shanghai-2",
			RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z", ProviderData: map[string]string{"chargeType": "PREPAID"},
		}
		return
	}
	f.fabric.computeSync = clients.ComputeAllocation{
		ID: operation.ComputeID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, PackageID: operation.PackageID, Status: "running",
		Provider: "tencent-tke", ProviderResourceID: "ins-" + operation.ComputeID, ProviderRequestID: "req-" + operation.ComputeID,
		InstanceID: "ins-" + operation.ComputeID, CVMInstanceID: "ins-" + operation.ComputeID, InstanceType: "S5.MEDIUM4", Zone: "ap-shanghai-2",
		ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z", ProviderData: map[string]string{"chargeType": "PREPAID", "zone": "ap-shanghai-2", "instanceType": "S5.MEDIUM4"},
	}
}

func (f workspaceLaunchWorkerFixture) resolveReview(t *testing.T, operation workspaceLaunchOperation, resourceType, decision string) *httptest.ResponseRecorder {
	t.Helper()
	resourceID, billingOperationID := operation.ComputeID, operation.ComputeBillingOperationID
	if resourceType == "storage" {
		resourceID, billingOperationID = operation.StorageID, operation.StorageBillingOperationID
	}
	body := `{"accountId":"` + operation.AccountID + `","billingOperationId":"` + billingOperationID + `","decision":"` + decision + `","evidenceRef":"case-20260717-launch001"}`
	path := "/api/operator/billing-reviews/" + resourceType + "/" + resourceID + "/resolve"
	return requestWithMutationKeyForTest(t, f.server, f.operator, http.MethodPost, path, body, "review-workspace-launch-001")
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
