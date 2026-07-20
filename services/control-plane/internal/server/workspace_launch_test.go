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
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func TestWorkspaceLaunchOperationRoundTripsWithoutSecrets(t *testing.T) {
	input := workspaceLaunchOperation{
		ID: "launch-alpha", Status: "debit_pending", SchemaVersion: workspaceLaunchSchemaVersion, RequestHash: "hash", Phase: "debit_pending",
		AccountID: "acct-alpha", OwnerUserID: "usr-alpha", WorkspaceID: "ws-alpha", Name: "Alpha", PackageID: "basic",
		StorageGB: 10, PriceVersion: pilotPriceVersion, TotalChargeUSDMicros: 52_580_000,
		ComputeID: "ca-alpha", StorageID: "vol-alpha",
		AttachmentID: "attachment-alpha", AttachmentOperationID: "attach-operation-alpha", WorkspaceOperationID: "workspace-operation-alpha",
		WorkspaceAPIKeyID: 19, RedeemCode: "opl:launch-alpha",
	}
	row := workspaceLaunchOperationRow(input)
	decoded, err := decodeWorkspaceLaunchOperation(row)
	if err != nil || decoded.RequestHash != input.RequestHash || decoded.ID != input.ID || decoded.Status != input.Status || decoded.PriceVersion != pilotPriceVersion {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
	if row["action"] != workspaceLaunchAction || row["resourceKind"] != "workspace_launch" || row["computeAllocationId"] != input.ComputeID || row["storageId"] != input.StorageID {
		t.Fatalf("workspace launch row = %#v", row)
	}
	encoded := stringValue(row["result"])
	for _, forbidden := range []string{"password", "apiKey", "rawProvider"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("encoded launch contains %q: %s", forbidden, encoded)
		}
	}
}

func TestNoLegacyWorkspaceBillingConsumer(t *testing.T) {
	operation := newWorkspaceLaunchOperation("acct-alpha", "usr-alpha", "Alpha", "basic", 10, false, pilotPriceVersion, 52_580_000, "launch-v2")
	encoded := encodeWorkspaceLaunchOperation(operation)
	for _, field := range []string{"pricingVersion", "totalMonthlyPriceCnyCents", "computeBillingOperationId", "storageBillingOperationId"} {
		if strings.Contains(encoded, `"`+field+`"`) {
			t.Fatalf("current Workspace launch persisted legacy field %s: %s", field, encoded)
		}
	}
}

func TestWorkspaceLaunchResponseAllowsOnlyCustomerSafeFields(t *testing.T) {
	operation := workspaceLaunchOperation{
		ID: "launch-alpha", Status: "unknown", SchemaVersion: workspaceLaunchSchemaVersion, RequestHash: "hash", Phase: "debit_pending",
		AccountID: "acct-alpha", OwnerUserID: "usr-private", WorkspaceID: "ws-alpha", Name: "Alpha", PackageID: "basic",
		StorageGB: 10, PriceVersion: pilotPriceVersion, TotalChargeUSDMicros: 52_580_000,
		ComputeID: "ca-alpha", StorageID: "vol-alpha",
		AttachmentID: "attachment-alpha", AttachmentOperationID: "attachment-operation-private", WorkspaceOperationID: "workspace-operation-private",
		WorkspaceAPIKeyID: 19, RedeemCode: "opl:launch-alpha",
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
	if response["workspaceApiKeyId"] != "19" {
		t.Fatalf("workspace launch Key ID must be a decimal string: %#v", response)
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
	for _, forbidden := range []string{"usr-private", "attachment-operation-private", "workspace-operation-private", "private upstream detail", "private-password", "private row detail"} {
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
	sub2API *workspaceLaunchSub2API
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
	launchSub2API := &workspaceLaunchSub2API{monthlySub2API: sub2API, keys: map[int64]clients.Sub2APIWorkspaceKey{
		9: {ID: 9, UserID: 41, Name: "opl-workspace", Key: "workspace-key-secret", Status: "active"},
	}}
	fabric := &monthlyFabric{events: &events}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, fabric, launchSub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	return workspaceLaunchHTTPFixture{
		server: server, store: store, session: loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!"),
		events: &events, sub2API: launchSub2API, fabric: fabric,
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

func TestWorkspaceLaunchRejectsAutoRenewBeforeExternalCalls(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":true}`, "launch-auto-renew")
	operations, _ := fixture.store.ListRuntimeOperations(context.Background())
	workspaces, _ := fixture.store.ListWorkspaces(context.Background(), "acct-alpha")
	computes, _ := fixture.store.ListComputes(context.Background(), "acct-alpha")
	storages, _ := fixture.store.ListStorages(context.Background(), "acct-alpha")
	attachments, _ := fixture.store.ListAttachments(context.Background(), "acct-alpha")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "autoRenew_unavailable") {
		t.Fatalf("auto-renew launch status=%d body=%s", response.Code, response.Body.String())
	}
	if len(*fixture.events) != 0 || len(operations) != 0 || len(workspaces) != 0 || len(computes) != 0 || len(storages) != 0 || len(attachments) != 0 || len(fixture.sub2API.charges) != 0 || len(fixture.sub2API.refunds) != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("auto-renew launch caused side effects: events=%#v operations=%#v workspaces=%#v computes=%#v storages=%#v attachments=%#v charges=%#v refunds=%#v", *fixture.events, operations, workspaces, computes, storages, attachments, fixture.sub2API.charges, fixture.sub2API.refunds)
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

func TestWorkspaceLaunchRejectsClientPrice(t *testing.T) {
	for name, field := range map[string]string{
		"price version": `"priceVersion":"client-price"`,
		"total":         `"totalChargeUsdMicros":1`,
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
			body := `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false,` + field + `}`
			response := fixture.launch(t, body, "launch-client-price")
			operations, _ := fixture.store.ListRuntimeOperations(context.Background())
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "client_pricing_forbidden") || len(*fixture.events) != 0 || len(operations) != 0 {
				t.Fatalf("client price status=%d body=%s events=%#v operations=%#v", response.Code, response.Body.String(), *fixture.events, operations)
			}
		})
	}
}

func TestWorkspaceLaunchTotalPreflightRejectsInsufficientBalanceWithoutSideEffects(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 52_579_999)
	response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-alpha")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), errMonthlyInsufficientBalance.Error()) {
		t.Fatalf("insufficient launch status = %d, want 409: %s", response.Code, response.Body.String())
	}
	if want := []string{"fabric.monthly.preflight", "fabric.monthly.preflight", "sub2api.user_keys", "sub2api.balance"}; !reflect.DeepEqual(*fixture.events, want) {
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
			fixture.sub2API.userKeysErr = tc.err
			response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-alpha")
			if response.Code != tc.wantStatus || !strings.Contains(response.Body.String(), tc.wantCode) {
				t.Fatalf("Gateway Key launch status = %d, want %d %s: %s", response.Code, tc.wantStatus, tc.wantCode, response.Body.String())
			}
			wantEvents := []string{"fabric.monthly.preflight", "fabric.monthly.preflight", "sub2api.user_keys"}
			operations, _ := fixture.store.ListRuntimeOperations(context.Background())
			if !reflect.DeepEqual(*fixture.events, wantEvents) || len(operations) != 0 || len(fixture.sub2API.charges) != 0 || len(fixture.sub2API.refunds) != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
				t.Fatalf("Gateway Key failure caused side effects: events=%#v operations=%#v charges=%#v refunds=%#v compute=%#v storage=%#v", *fixture.events, operations, fixture.sub2API.charges, fixture.sub2API.refunds, fixture.fabric.computeIDs, fixture.fabric.storageIDs)
			}
		})
	}
}

func TestWorkspaceKeyConvergenceCreatesBeforeBalanceAndPersistsID(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	client := fixture.sub2API
	client.keys = map[int64]clients.Sub2APIWorkspaceKey{}

	response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-converge")
	if response.Code != http.StatusAccepted {
		t.Fatalf("launch status=%d body=%s", response.Code, response.Body.String())
	}
	operations, err := fixture.store.ListRuntimeOperations(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("launch operations=%#v err=%v", operations, err)
	}
	operation, err := decodeWorkspaceLaunchOperation(operations[0])
	if err != nil || operation.WorkspaceAPIKeyID != 19 || client.createCalls != 1 {
		t.Fatalf("converged operation=%#v creates=%d err=%v", operation, client.createCalls, err)
	}
	if got := *fixture.events; !reflect.DeepEqual(got, []string{
		"fabric.monthly.preflight", "fabric.monthly.preflight", "sub2api.user_keys", "sub2api.create_workspace_key", "sub2api.user_keys", "sub2api.balance",
	}) {
		t.Fatalf("convergence order=%#v", got)
	}
	if strings.Contains(string(mustJSON(operations)), "created-workspace-key-secret") {
		t.Fatalf("launch operation persisted raw Key: %#v", operations)
	}
}

func TestWorkspaceKeyAmbiguityStopsBeforeBalanceAndCharge(t *testing.T) {
	for _, keys := range []map[int64]clients.Sub2APIWorkspaceKey{
		{9: {ID: 9, UserID: 41, Name: "opl-workspace", Status: "disabled"}},
		{10: {ID: 10, UserID: 41, Name: "opl-workspace-replacement-conflict", Status: "active"}},
	} {
		fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
		client := fixture.sub2API
		client.keys = keys
		response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-ambiguous")
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "gateway_key_ambiguous") {
			t.Fatalf("ambiguous launch=%d body=%s", response.Code, response.Body.String())
		}
		operations, _ := fixture.store.ListRuntimeOperations(context.Background())
		if countStrings(*fixture.events, "sub2api.balance") != 0 || len(client.charges) != 0 || len(operations) != 0 {
			t.Fatalf("ambiguous Key crossed billing gate: events=%#v charges=%#v operations=%#v", *fixture.events, client.charges, operations)
		}
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
	if findRecord(users, "usr-alpha") == nil {
		t.Fatal("owner missing")
	}
	fixture.store.accounts["acct-alpha"]["ownerUserId"] = "usr-other"

	response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-alpha")
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "reauthentication_required") {
		t.Fatalf("owner mismatch launch status = %d, want 401: %s", response.Code, response.Body.String())
	}
	operations, _ := fixture.store.ListRuntimeOperations(context.Background())
	if len(*fixture.events) != 0 || len(operations) != 0 {
		t.Fatalf("owner mismatch launch reached dependencies: events=%#v operations=%#v", *fixture.events, operations)
	}
}

func TestWorkspaceLaunchOwnerLifecycleFencesInitialKeyAndClaim(t *testing.T) {
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	store := &recordingWorkspaceLaunchStore{
		memoryTableStore: newMemoryTableStore(), lifecycleStarted: make(chan struct{}), releaseLifecycle: make(chan struct{}),
		workspaceLaunchClaimed: make(chan struct{}),
	}
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	promoteWorkspaceLaunchOwner(t, store, "usr-alpha")
	events := []string{}
	sub2API := &workspaceLaunchSub2API{monthlySub2API: &monthlySub2API{events: &events, balances: []int64{1_000_000_000}}, keys: map[int64]clients.Sub2APIWorkspaceKey{}}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &monthlyFabric{events: &events}, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	app := server.(*controlPlaneHTTPHandler).app

	disableResult := make(chan error, 1)
	go func() {
		_, err := app.disableUser(map[string]any{"userId": "usr-alpha", "reason": "pilot_offboarding"})
		disableResult <- err
	}()
	select {
	case <-store.lifecycleStarted:
	case <-time.After(time.Second):
		t.Fatal("account disable did not enter the lifecycle transaction")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/workspace-launches", strings.NewReader(`{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "launch-lifecycle-fence")
	addAuth(req, session)
	response := httptest.NewRecorder()
	launchDone := make(chan struct{})
	go func() {
		server.ServeHTTP(response, req)
		close(launchDone)
	}()
	claimCrossedLifecycleFence := false
	select {
	case <-store.workspaceLaunchClaimed:
		claimCrossedLifecycleFence = true
	case <-time.After(100 * time.Millisecond):
	}
	close(store.releaseLifecycle)
	if err := <-disableResult; err != nil {
		t.Fatal(err)
	}
	select {
	case <-launchDone:
	case <-time.After(time.Second):
		t.Fatal("Workspace launch did not leave the lifecycle fence")
	}
	operations, err := store.ListRuntimeOperations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if claimCrossedLifecycleFence || response.Code != http.StatusUnauthorized || sub2API.createCalls != 0 || len(operations) != 0 {
		t.Fatalf("launch crossed owner lifecycle fence: crossed=%t status=%d body=%s creates=%d operations=%#v", claimCrossedLifecycleFence, response.Code, response.Body.String(), sub2API.createCalls, operations)
	}
}

func TestWorkspaceLaunchListAndDetailAreTenantScoped(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	created := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-alpha")
	if created.Code != http.StatusAccepted {
		t.Fatalf("launch status = %d: %s", created.Code, created.Body.String())
	}
	var launch map[string]any
	if err := json.NewDecoder(created.Body).Decode(&launch); err != nil {
		t.Fatal(err)
	}
	if launch["autoRenew"] != false || launch["priceVersion"] != pilotPriceVersion || launch["totalChargeUsdMicros"] != float64(52_580_000) || strings.Contains(created.Body.String(), "pricingVersion") || strings.Contains(created.Body.String(), "totalMonthlyPriceCnyCents") {
		t.Fatalf("created launch projection = %#v", launch)
	}
	operationID := stringValue(launch["operationId"])

	alphaList := requestWithSession(t, fixture.server, fixture.session, http.MethodGet, "/api/workspace-launches", "")
	if alphaList.Code != http.StatusOK || !strings.Contains(alphaList.Body.String(), operationID) || !strings.Contains(alphaList.Body.String(), `"autoRenew":false`) || !strings.Contains(alphaList.Body.String(), `"priceVersion":"pilot-usd-2026-07-v1"`) || strings.Contains(alphaList.Body.String(), "usr-alpha") || strings.Contains(alphaList.Body.String(), "pricingVersion") {
		t.Fatalf("alpha launch list status=%d body=%s", alphaList.Code, alphaList.Body.String())
	}
	alphaDetail := requestWithSession(t, fixture.server, fixture.session, http.MethodGet, "/api/workspace-launches/"+operationID, "")
	if alphaDetail.Code != http.StatusOK || !strings.Contains(alphaDetail.Body.String(), operationID) || !strings.Contains(alphaDetail.Body.String(), `"autoRenew":false`) || !strings.Contains(alphaDetail.Body.String(), `"priceVersion":"pilot-usd-2026-07-v1"`) || strings.Contains(alphaDetail.Body.String(), "pricingVersion") {
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
	lifecycleStarted           chan struct{}
	releaseLifecycle           chan struct{}
	lifecycleSignal            sync.Once
	workspaceLaunchClaimed     chan struct{}
	workspaceLaunchClaimSignal sync.Once
}

func (s *recordingWorkspaceLaunchStore) ApplyUserLifecycle(ctx context.Context, user map[string]any) error {
	if s.lifecycleStarted != nil {
		s.lifecycleSignal.Do(func() {
			close(s.lifecycleStarted)
			<-s.releaseLifecycle
		})
	}
	return s.memoryTableStore.ApplyUserLifecycle(ctx, user)
}

func (s *recordingWorkspaceLaunchStore) ClaimWorkspaceLaunch(ctx context.Context, claim workspaceLaunchClaimCAS) error {
	if s.workspaceLaunchClaimed != nil {
		s.workspaceLaunchClaimSignal.Do(func() { close(s.workspaceLaunchClaimed) })
	}
	return s.memoryTableStore.ClaimWorkspaceLaunch(ctx, claim)
}

type workspaceLaunchLedger struct {
	fakeLedgerClient
	events                *[]string
	receipts              map[string]clients.Receipt
	receiptInputs         []clients.ReceiptInput
	receiptErrors         []error
	workspaceReceiptCalls int
}

func (l *workspaceLaunchLedger) RecordReceipt(_ context.Context, input clients.ReceiptInput, key string) (clients.Receipt, error) {
	l.receiptInputs = append(l.receiptInputs, input)
	if input.Type == "workspace.created" {
		*l.events = append(*l.events, "ledger.workspace.receipt")
		l.workspaceReceiptCalls++
	}
	if len(l.receiptErrors) > 0 {
		err := l.receiptErrors[0]
		l.receiptErrors = l.receiptErrors[1:]
		if err != nil {
			return clients.Receipt{}, err
		}
	}
	if receipt, ok := l.receipts[key]; ok {
		return receipt, nil
	}
	receipt := clients.Receipt{ReceiptInput: input, ReceiptID: "receipt-" + stableID(key)[:12]}
	l.receipts[key] = receipt
	return receipt, nil
}

type workspaceLaunchSub2API struct {
	*monthlySub2API
	keys        map[int64]clients.Sub2APIWorkspaceKey
	createCalls int
	userKeysErr error
}

type durableWorkspaceLaunchSub2API struct {
	*workspaceLaunchSub2API
	mu                sync.Mutex
	balance           int64
	chargeCalls       []clients.Sub2APIChargeInput
	appliedCharges    map[string]clients.Sub2APIChargeInput
	loseNextResponses int
}

func (s *durableWorkspaceLaunchSub2API) Balance(_ context.Context, userID int64) (clients.Sub2APIBalance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.events = append(*s.events, "sub2api.balance")
	return clients.Sub2APIBalance{UserID: userID, USDMicros: s.balance, Status: "active"}, nil
}

func (s *durableWorkspaceLaunchSub2API) Charge(_ context.Context, input clients.Sub2APIChargeInput) (clients.Sub2APICharge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.events = append(*s.events, "sub2api.charge")
	s.chargeCalls = append(s.chargeCalls, input)
	if existing, ok := s.appliedCharges[input.Code]; ok {
		if existing.UserID != input.UserID || existing.ChargeUSDMicros != input.ChargeUSDMicros {
			return clients.Sub2APICharge{}, clients.ErrSub2APIChargeConflict
		}
		return clients.Sub2APICharge{Code: input.Code, UserID: input.UserID, ChargeUSDMicros: input.ChargeUSDMicros, Status: "used"}, nil
	}
	if input.ChargeUSDMicros <= 0 || input.ChargeUSDMicros > s.balance {
		return clients.Sub2APICharge{}, errMonthlyInsufficientBalance
	}
	s.balance -= input.ChargeUSDMicros
	s.appliedCharges[input.Code] = input
	if s.loseNextResponses > 0 {
		s.loseNextResponses--
		return clients.Sub2APICharge{}, clients.ErrSub2APIChargeUnknown
	}
	return clients.Sub2APICharge{Code: input.Code, UserID: input.UserID, ChargeUSDMicros: input.ChargeUSDMicros, Status: "used"}, nil
}

func (s *durableWorkspaceLaunchSub2API) Usage(context.Context, clients.Sub2APIUsageQuery) (clients.Sub2APIUsagePage, error) {
	return clients.Sub2APIUsagePage{}, nil
}

func (s *durableWorkspaceLaunchSub2API) UsageStats(context.Context, clients.Sub2APIUsageStatsQuery) (clients.Sub2APIUsageStats, error) {
	return clients.Sub2APIUsageStats{}, nil
}

func (s *durableWorkspaceLaunchSub2API) BalanceHistory(_ context.Context, userID int64) ([]clients.Sub2APIBalanceHistoryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	usedAt := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	rows := make([]clients.Sub2APIBalanceHistoryEntry, 0, len(s.appliedCharges))
	for _, input := range s.appliedCharges {
		usedBy := userID
		rows = append(rows, clients.Sub2APIBalanceHistoryEntry{
			Code: input.Code, Type: "balance", ValueUSDMicros: -input.ChargeUSDMicros, Status: "used",
			UsedBy: &usedBy, UsedAt: &usedAt, CreatedAt: usedAt,
		})
	}
	return rows, nil
}

type workspaceLaunchRouteBarrierStore struct {
	*memoryTableStore
	mu      sync.Mutex
	armed   bool
	waiting int
	release chan struct{}
}

func (s *workspaceLaunchRouteBarrierStore) ListRuntimeOperations(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.memoryTableStore.ListRuntimeOperations(ctx)
	s.mu.Lock()
	if !s.armed {
		s.mu.Unlock()
		return rows, err
	}
	s.waiting++
	if s.waiting == 2 {
		close(s.release)
	}
	release := s.release
	s.mu.Unlock()
	<-release
	return rows, err
}

func (s *workspaceLaunchSub2API) WorkspaceKey(ctx context.Context, userID int64) (clients.Sub2APIWorkspaceKey, error) {
	*s.events = append(*s.events, "sub2api.workspace_key")
	return s.monthlySub2API.WorkspaceKey(ctx, userID)
}

func (s *workspaceLaunchSub2API) Keys(_ context.Context, userID int64) ([]clients.Sub2APIWorkspaceKey, error) {
	keys := make([]clients.Sub2APIWorkspaceKey, 0, len(s.keys))
	for _, key := range s.keys {
		if key.UserID == userID {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (s *workspaceLaunchSub2API) UserKeys(_ context.Context, credential clients.SessionDelegatedCredential, userID int64) ([]clients.Sub2APIWorkspaceKey, error) {
	*s.events = append(*s.events, "sub2api.user_keys")
	if credential.Bearer != "test-user-delegated-token" {
		return nil, errors.New("wrong delegated credential")
	}
	if s.userKeysErr != nil {
		return nil, s.userKeysErr
	}
	keys := make([]clients.Sub2APIWorkspaceKey, 0, len(s.keys))
	for _, key := range s.keys {
		if key.UserID == userID {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (s *workspaceLaunchSub2API) UserKey(_ context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64) (clients.Sub2APIWorkspaceKey, error) {
	if credential.Bearer != "test-user-delegated-token" {
		return clients.Sub2APIWorkspaceKey{}, errors.New("wrong delegated credential")
	}
	key, ok := s.keys[keyID]
	if !ok || key.UserID != userID {
		return clients.Sub2APIWorkspaceKey{}, clients.ErrSub2APIKeyNotFound
	}
	return key, nil
}

func (s *workspaceLaunchSub2API) CreateUserKey(_ context.Context, credential clients.SessionDelegatedCredential, userID int64, input clients.Sub2APICreateKeyInput, idempotencyKey string) (clients.Sub2APIWorkspaceKey, error) {
	*s.events = append(*s.events, "sub2api.create_workspace_key")
	if credential.Bearer != "test-user-delegated-token" || idempotencyKey == "" || input.Name != "opl-workspace" || input.ExpiresInDays != nil {
		return clients.Sub2APIWorkspaceKey{}, errors.New("invalid Workspace Key create")
	}
	s.createCalls++
	key := clients.Sub2APIWorkspaceKey{ID: 19, UserID: userID, Name: input.Name, Key: "created-workspace-key-secret", Status: "active"}
	if s.keys == nil {
		s.keys = map[int64]clients.Sub2APIWorkspaceKey{}
	}
	s.keys[key.ID] = key
	return key, nil
}

func (s *workspaceLaunchSub2API) UpdateUserKey(context.Context, clients.SessionDelegatedCredential, int64, int64, clients.Sub2APIUpdateKeyInput) (clients.Sub2APIWorkspaceKey, error) {
	return clients.Sub2APIWorkspaceKey{}, errors.New("unexpected Workspace Key update")
}

func (s *workspaceLaunchSub2API) DeleteUserKey(context.Context, clients.SessionDelegatedCredential, int64, int64) error {
	return errors.New("unexpected Workspace Key delete")
}

func (s *workspaceLaunchSub2API) Usage(context.Context, clients.Sub2APIUsageQuery) (clients.Sub2APIUsagePage, error) {
	return clients.Sub2APIUsagePage{}, nil
}

func (s *workspaceLaunchSub2API) UsageStats(context.Context, clients.Sub2APIUsageStatsQuery) (clients.Sub2APIUsageStats, error) {
	return clients.Sub2APIUsageStats{}, nil
}

func (s *workspaceLaunchSub2API) BalanceHistory(_ context.Context, userID int64) ([]clients.Sub2APIBalanceHistoryEntry, error) {
	usedAt := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	rows := make([]clients.Sub2APIBalanceHistoryEntry, 0, len(s.charges))
	for _, charge := range s.charges {
		usedBy := userID
		rows = append(rows, clients.Sub2APIBalanceHistoryEntry{
			Code: charge.Code, Type: "balance", ValueUSDMicros: -charge.ChargeUSDMicros, Status: "used",
			UsedBy: &usedBy, UsedAt: &usedAt, CreatedAt: usedAt,
		})
	}
	return rows, nil
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
	created := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/workspace-launches", fmt.Sprintf(`{"name":"Alpha","packageId":%q,"sizeGb":%d,"autoRenew":false}`, packageID, storageGB), "launch-alpha")
	if created.Code != http.StatusAccepted {
		t.Fatalf("launch status = %d: %s", created.Code, created.Body.String())
	}
	var response map[string]any
	if err := json.NewDecoder(created.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	operationID := stringValue(response["operationId"])
	if autoRenew {
		operations, err := store.ListRuntimeOperations(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		operation, err := decodeWorkspaceLaunchOperation(recordByID(operations, operationID))
		if err != nil {
			t.Fatal(err)
		}
		operation.AutoRenew = true
		operation.RequestHash = newWorkspaceLaunchOperation(operation.AccountID, operation.OwnerUserID, operation.Name, operation.PackageID, operation.StorageGB, true, operation.PriceVersion, operation.TotalChargeUSDMicros, "launch-alpha").RequestHash
		mustStore(t, store.memoryTableStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
	}
	app, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	return workspaceLaunchWorkerFixture{
		app: app, service: service, server: server, operator: reservedOperatorSessionForTest(t, server), store: store, events: &events, sub2API: sub2API, fabric: fabric, ledger: ledger,
		operationID: operationID,
	}
}

func TestWorkspaceLaunchSingleTotalDebit(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	rows, err := fixture.store.ListRuntimeOperations(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("launch rows=%#v err=%v", rows, err)
	}
	var persisted map[string]any
	if err := json.Unmarshal([]byte(stringValue(rows[0]["result"])), &persisted); err != nil {
		t.Fatal(err)
	}
	operation := fixture.operation(t)
	if stringValue(rows[0]["action"]) != "workspace.launch.v2" || persisted["schemaVersion"] != float64(2) || operation.Status != "debited" || operation.Phase != "debited" {
		t.Fatalf("debited launch row=%#v operation=%#v", rows[0], operation)
	}
	if len(fixture.sub2API.charges) != 1 || fixture.sub2API.charges[0].ChargeUSDMicros != 52_580_000 {
		t.Fatalf("Workspace debit calls=%#v", fixture.sub2API.charges)
	}
	if len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 || countStrings(*fixture.events, "fabric.attachment") != 0 || countStrings(*fixture.events, "fabric.runtime") != 0 {
		t.Fatalf("S7 crossed fulfillment gate: events=%#v", *fixture.events)
	}
}

func TestWorkspaceLaunchRejectsEqualBalanceBeforeCharge(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{100_000_000, 52_580_000, 0}, nil, nil)
	err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
	operation := fixture.operation(t)
	if !errors.Is(err, errMonthlyInsufficientBalance) || operation.Status != "insufficient" || operation.Phase != "debit_pending" ||
		len(fixture.sub2API.charges) != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("equal balance crossed debit gate: err=%v operation=%#v charges=%#v compute=%#v storage=%#v", err, operation, fixture.sub2API.charges, fixture.fabric.computeIDs, fixture.fabric.storageIDs)
	}
}

func TestWorkspaceLaunchPostChargeBalanceMustMatchExactDelta(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{100_000_000, 100_000_000, 40_000_000}, nil, nil)
	err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
	operation := fixture.operation(t)
	if err == nil || operation.Status != "manual_review" || operation.ErrorCode != "post_charge_balance_invalid" ||
		len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("inexact post balance was accepted: err=%v operation=%#v charges=%#v compute=%#v storage=%#v", err, operation, fixture.sub2API.charges, fixture.fabric.computeIDs, fixture.fabric.storageIDs)
	}
}

func TestWorkspaceLaunchWorkerRechecksProviderPreflightBeforeFirstCharge(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{100_000_000, 100_000_000, 47_420_000}, nil, nil)
	fixture.fabric.preflightResults = []clients.MonthlyPreflight{{}, {}}
	err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
	operation := fixture.operation(t)
	if err == nil || operation.Status != "unknown" || operation.Phase != "debit_pending" || operation.ErrorCode != "fabric_compute_preflight_failed" ||
		len(fixture.sub2API.charges) != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("worker skipped preflight gate: err=%v operation=%#v charges=%#v compute=%#v storage=%#v", err, operation, fixture.sub2API.charges, fixture.fabric.computeIDs, fixture.fabric.storageIDs)
	}
}

func TestWorkspaceLaunchActivationReadsProviderTruthAgain(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	configureWorkspaceLaunchFulfillment(t, fixture)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if countStrings(*fixture.events, "fabric.compute.sync") < 2 || countStrings(*fixture.events, "fabric.storage.sync") < 2 {
		t.Fatalf("activation did not perform authoritative provider readback: events=%#v", *fixture.events)
	}
}

func configureWorkspaceLaunchFulfillment(t *testing.T, fixture workspaceLaunchWorkerFixture) workspaceLaunchOperation {
	t.Helper()
	operation := fixture.operation(t)
	fixture.fabric.computeSync = clients.ComputeAllocation{
		ID: operation.ComputeID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, PackageID: operation.PackageID,
		Status: "running", Provider: "tencent-tke", ProviderResourceID: "ins-" + operation.ComputeID, ProviderRequestID: "req-" + operation.ComputeID,
		InstanceID: "ins-" + operation.ComputeID, InstanceType: "S5.MEDIUM4", Zone: "ap-shanghai-2", ChargeType: "PREPAID",
		RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z", ProviderData: map[string]string{"zone": "ap-shanghai-2", "instanceType": "S5.MEDIUM4"},
	}
	fixture.fabric.storageSync = clients.StorageVolume{
		ID: operation.StorageID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "available",
		Provider: "tencent-tke", ProviderResourceID: "disk-" + operation.StorageID, ProviderRequestID: "req-" + operation.StorageID,
		SizeGB: operation.StorageGB, CBSStatus: "UNATTACHED", DiskType: "CLOUD_PREMIUM", RenewFlag: "NOTIFY_AND_MANUAL_RENEW",
		Deadline: "2099-01-01T00:00:00Z", Zone: "ap-shanghai-2", ProviderData: map[string]string{"chargeType": "PREPAID"},
	}
	return operation
}

func TestWorkspaceLaunchFulfillmentOnly(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	configureWorkspaceLaunchFulfillment(t, fixture)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}

	operation := fixture.operation(t)
	if operation.Status != "succeeded" || operation.Phase != "succeeded" || operation.AttachmentID == "" || operation.RuntimeServiceName == "" || operation.URL == "" {
		t.Fatalf("fulfilled launch=%#v", operation)
	}
	if len(fixture.sub2API.charges) != 1 || fixture.sub2API.charges[0].ChargeUSDMicros != 52_580_000 || len(fixture.sub2API.refunds) != 0 {
		t.Fatalf("Workspace billing calls: charges=%#v refunds=%#v", fixture.sub2API.charges, fixture.sub2API.refunds)
	}
	if len(fixture.fabric.computeIDs) != 1 || len(fixture.fabric.storageIDs) != 1 || countStrings(*fixture.events, "fabric.compute.sync") != 2 || countStrings(*fixture.events, "fabric.storage.sync") != 2 ||
		countStrings(*fixture.events, "fabric.attachment") != 1 || countStrings(*fixture.events, "fabric.gateway-secret") != 1 || countStrings(*fixture.events, "fabric.runtime") != 1 {
		t.Fatalf("fulfillment events=%#v", *fixture.events)
	}
	computes, _ := fixture.store.ListComputes(context.Background(), operation.AccountID)
	storages, _ := fixture.store.ListStorages(context.Background(), operation.AccountID)
	if len(computes) != 1 || len(storages) != 1 {
		t.Fatalf("fulfilled resources: computes=%#v storages=%#v", computes, storages)
	}
	for _, row := range []map[string]any{computes[0], storages[0]} {
		for _, forbidden := range []string{"billingOperationId", "sub2apiRedeemCode", "chargeUsdMicros", "priceVersion"} {
			if _, ok := row[forbidden]; ok {
				t.Fatalf("resource retained customer billing field %s: %#v", forbidden, row)
			}
		}
	}
}

func TestWorkspaceLaunchSingleReceipt(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	expected := configureWorkspaceLaunchFulfillment(t, fixture)
	for range 2 {
		if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
			t.Fatal(err)
		}
	}
	if len(fixture.ledger.receiptInputs) != 1 {
		t.Fatalf("launch receipts=%#v", fixture.ledger.receiptInputs)
	}
	receipt := fixture.ledger.receiptInputs[0]
	if receipt.Type != "billing.workspace_purchased.v1" || receipt.AccountID != expected.AccountID || receipt.WorkspaceID != expected.WorkspaceID || receipt.RequestID != expected.ID {
		t.Fatalf("Workspace purchase receipt=%#v", receipt)
	}
	if receipt.Cost["priceVersion"] != pilotPriceVersion || receipt.Cost["currency"] != "USD" || receipt.Cost["billingUnit"] != "calendar_month" ||
		receipt.Cost["totalUsdMicros"] != int64(52_580_000) || receipt.Cost["sub2apiRedeemCode"] != expected.RedeemCode ||
		stringValue(receipt.Cost["periodStart"]) == "" || stringValue(receipt.Cost["paidThrough"]) == "" {
		t.Fatalf("Workspace purchase cost=%#v", receipt.Cost)
	}
	components := mapField(receipt.Cost, "components")
	if numberField(mapField(components, "compute"), "chargeUsdMicros", 0) != 50_000_000 || numberField(mapField(components, "storage"), "chargeUsdMicros", 0) != 2_580_000 {
		t.Fatalf("Workspace purchase components=%#v", components)
	}
	if receipt.Execution["computeAllocationId"] != expected.ComputeID || receipt.Execution["storageId"] != expected.StorageID ||
		stringValue(receipt.Execution["attachmentId"]) == "" || stringValue(receipt.Execution["runtimeId"]) == "" {
		t.Fatalf("Workspace purchase fulfillment=%#v", receipt.Execution)
	}
}

func TestWorkspaceLaunchCreateResponseLossConvergesFromReadback(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	configureWorkspaceLaunchFulfillment(t, fixture)
	fixture.fabric.createErr = errors.New("Fabric create response lost")
	for range 2 {
		if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
			t.Fatal(err)
		}
	}
	operation := fixture.operation(t)
	if operation.Status != "succeeded" || operation.Phase != "succeeded" || len(fixture.sub2API.refunds) != 0 || len(fixture.ledger.receiptInputs) != 1 {
		t.Fatalf("response-loss launch=%#v refunds=%#v receipts=%#v", operation, fixture.sub2API.refunds, fixture.ledger.receiptInputs)
	}
}

func TestWorkspaceLaunchRuntimeReadinessWaits(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	operation := configureWorkspaceLaunchFulfillment(t, fixture)
	runtime := clients.WorkspaceRuntime{
		ID: "runtime-from-fabric", WorkspaceID: operation.WorkspaceID, URL: "https://workspace.medopl.cn/w/" + operation.WorkspaceID + "/",
		Status: "starting", ServiceName: "opl-compute-from-fabric",
		Access: clients.WorkspaceRuntimeAccess{Username: "admin", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-compute-from-fabric-env"},
	}
	ready := runtime
	ready.Status, ready.Ready = "running", true
	fixture.fabric.runtime = runtime
	fixture.fabric.runtimeStatusResults = []clients.WorkspaceRuntime{runtime, ready}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	waiting := fixture.operation(t)
	workspaces, _ := fixture.store.ListWorkspaces(context.Background(), operation.AccountID)
	if waiting.Status != "waiting" || waiting.Phase != "runtime_starting" || len(workspaces) != 0 || len(fixture.ledger.receiptInputs) != 0 {
		t.Fatalf("unready runtime launch=%#v workspaces=%#v receipts=%#v", waiting, workspaces, fixture.ledger.receiptInputs)
	}
	beforeEvents := append([]string(nil), (*fixture.events)...)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	completed := fixture.operation(t)
	if completed.Status != "succeeded" || completed.Phase != "succeeded" || len(fixture.fabric.runtimeInputs) != 2 || countStrings(*fixture.events, "fabric.runtime-status") != 2 || len(fixture.ledger.receiptInputs) != 1 {
		t.Fatalf("ready runtime launch=%#v runtime calls=%#v receipts=%#v", completed, fixture.fabric.runtimeInputs, fixture.ledger.receiptInputs)
	}
	for _, event := range []string{"fabric.compute.prepare", "fabric.storage.prepare", "fabric.attachment", "fabric.gateway-secret"} {
		if countStrings(*fixture.events, event) != countStrings(beforeEvents, event) {
			t.Fatalf("runtime readiness retry repeated %s: before=%#v after=%#v", event, beforeEvents, *fixture.events)
		}
	}
	for _, event := range []string{"fabric.compute.sync", "fabric.storage.sync"} {
		if countStrings(*fixture.events, event) != countStrings(beforeEvents, event)+1 {
			t.Fatalf("activation did not read %s once: before=%#v after=%#v", event, beforeEvents, *fixture.events)
		}
	}
}

func TestWorkspaceLaunchActivationRejectsProviderZoneDrift(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	operation := configureWorkspaceLaunchFulfillment(t, fixture)
	runtime := clients.WorkspaceRuntime{
		ID: "runtime-from-fabric", WorkspaceID: operation.WorkspaceID, URL: "https://workspace.medopl.cn/w/" + operation.WorkspaceID + "/",
		Status: "starting", ServiceName: "opl-compute-from-fabric",
		Access: clients.WorkspaceRuntimeAccess{Username: "admin", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-compute-from-fabric-env"},
	}
	ready := runtime
	ready.Status, ready.Ready = "running", true
	fixture.fabric.runtime = runtime
	fixture.fabric.runtimeStatusResults = []clients.WorkspaceRuntime{runtime, ready}
	for range 2 {
		if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
			t.Fatal(err)
		}
	}
	fixture.fabric.computeSync.Zone = "ap-shanghai-3"
	fixture.fabric.computeSync.ProviderData["zone"] = "ap-shanghai-3"
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("provider Zone drift was accepted before activation")
	}
	current := fixture.operation(t)
	workspaces, _ := fixture.store.ListWorkspaces(context.Background(), operation.AccountID)
	if current.Status != "manual_review" || current.ErrorCode != "workspace_launch_provider_readback_invalid" || len(workspaces) != 0 || len(fixture.ledger.receiptInputs) != 0 {
		t.Fatalf("Zone drift activation=%#v workspaces=%#v receipts=%#v", current, workspaces, fixture.ledger.receiptInputs)
	}
}

func TestWorkspaceLaunchRuntimeReadbackDoesNotBackfillAuthority(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	operation := configureWorkspaceLaunchFulfillment(t, fixture)
	fixture.fabric.runtime = clients.WorkspaceRuntime{
		ID: "runtime-from-create", WorkspaceID: operation.WorkspaceID, URL: "https://workspace.medopl.cn/w/" + operation.WorkspaceID + "/",
		Status: "running", ServiceName: "opl-compute-from-create", Ready: true,
		Access: clients.WorkspaceRuntimeAccess{Username: "admin", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "runtime-secret-from-create"},
	}
	fixture.fabric.runtimeStatusResults = []clients.WorkspaceRuntime{{WorkspaceID: operation.WorkspaceID, Status: "running", Ready: true}}
	for range 2 {
		_ = fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
	}

	current := fixture.operation(t)
	workspaces, _ := fixture.store.ListWorkspaces(context.Background(), operation.AccountID)
	if current.Status != "manual_review" || current.ErrorCode != "workspace_launch_runtime_readback_invalid" || len(workspaces) != 0 || len(fixture.ledger.receiptInputs) != 0 {
		t.Fatalf("partial Runtime readback launch=%#v workspaces=%#v receipts=%#v", current, workspaces, fixture.ledger.receiptInputs)
	}
}

func TestWorkspaceLaunchAttachmentAllowsProviderDTOWithoutMountPath(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	operation := configureWorkspaceLaunchFulfillment(t, fixture)
	fixture.fabric.attachment = clients.StorageAttachment{
		ID: "attachment-from-tencent", WorkspaceID: operation.WorkspaceID, ComputeID: operation.ComputeID, VolumeID: operation.StorageID,
		Status: "attached", Provider: "tencent-tke", ProviderAttachmentID: "deployment/runtime:pvc/storage", ProviderRequestID: "request-from-tencent",
	}
	for range 2 {
		if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
			t.Fatal(err)
		}
	}
	completed := fixture.operation(t)
	if completed.Status != "succeeded" || completed.AttachmentID != "attachment-from-tencent" {
		t.Fatalf("Tencent attachment launch=%#v", completed)
	}
	attachments, _ := fixture.store.ListAttachments(context.Background(), operation.AccountID)
	if len(attachments) != 1 || stringValue(attachments[0]["providerAttachmentId"]) == "" || stringValue(attachments[0]["mountPath"]) != "/data" {
		t.Fatalf("attachment projection=%#v", attachments)
	}
}

func TestWorkspaceLaunchRefundWhenNoResources(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	operation := fixture.operation(t)
	fixture.fabric.createErr = errors.New("compute create response lost")
	fixture.fabric.computeSync = clients.ComputeAllocation{
		ID: operation.ComputeID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "external_deleted",
	}
	fixture.fabric.storageSyncErr = &clients.FabricHTTPError{StatusCode: http.StatusInternalServerError, Body: `{"error":"storage_volume_not_found"}`}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	refunded := fixture.operation(t)
	if refunded.Status != "refunded" || refunded.Phase != "refunded" || len(fixture.sub2API.refunds) != 1 || fixture.sub2API.refunds[0].RefundUSDMicros != 52_580_000 {
		t.Fatalf("refunded launch=%#v refunds=%#v", refunded, fixture.sub2API.refunds)
	}
	if len(fixture.fabric.storageIDs) != 0 || countStrings(*fixture.events, "fabric.storage.sync") != 1 || countStrings(*fixture.events, "fabric.attachment") != 0 || countStrings(*fixture.events, "fabric.runtime") != 0 {
		t.Fatalf("absent compute crossed fulfillment: events=%#v", *fixture.events)
	}
	if len(fixture.ledger.receiptInputs) != 1 || fixture.ledger.receiptInputs[0].Type != "billing.workspace_refunded.v1" {
		t.Fatalf("refund receipts=%#v", fixture.ledger.receiptInputs)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil || len(fixture.sub2API.refunds) != 1 || len(fixture.ledger.receiptInputs) != 1 {
		t.Fatalf("refund replay err=%v refunds=%#v receipts=%#v", err, fixture.sub2API.refunds, fixture.ledger.receiptInputs)
	}
}

func TestWorkspaceLaunchComputeAbsentRequiresAuthoritativeStorageAbsenceBeforeRefund(t *testing.T) {
	for _, tc := range []struct {
		name, wantCode string
		err            error
	}{
		{name: "present", wantCode: "fabric_storage_presence_blocks_refund"},
		{name: "readback unavailable", wantCode: "fabric_storage_readback_unconfirmed_blocks_refund", err: errors.New("Fabric storage readback unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
			operation := fixture.operation(t)
			fixture.fabric.createErr = errors.New("compute create response lost")
			fixture.fabric.computeSync = clients.ComputeAllocation{
				ID: operation.ComputeID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "external_deleted",
			}
			fixture.fabric.storageSyncErr = tc.err
			if tc.name == "present" {
				fixture.fabric.storageSync = clients.StorageVolume{
					ID: operation.StorageID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "available",
					Provider: "tencent-tke", ProviderResourceID: "disk-" + operation.StorageID, CBSStatus: "UNATTACHED",
				}
			}
			for range 2 {
				_ = fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
			}

			current := fixture.operation(t)
			if current.Status != "manual_review" || current.ErrorCode != tc.wantCode || len(fixture.sub2API.refunds) != 0 || countStrings(*fixture.events, "fabric.storage.sync") != 1 {
				t.Fatalf("storage recovery=%#v refunds=%#v events=%#v", current, fixture.sub2API.refunds, *fixture.events)
			}
		})
	}
}

func TestWorkspaceLaunchPartialResourceManualReview(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	operation := configureWorkspaceLaunchFulfillment(t, fixture)
	fixture.fabric.storageSync = clients.StorageVolume{
		ID: operation.StorageID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "external_deleted", CBSStatus: "NOT_FOUND",
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	_ = fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
	partial := fixture.operation(t)
	if partial.Status != "manual_review" || partial.Phase != "storage_fulfilling" || len(fixture.sub2API.refunds) != 0 {
		t.Fatalf("partial launch=%#v refunds=%#v", partial, fixture.sub2API.refunds)
	}
	workspaces, _ := fixture.store.ListWorkspaces(context.Background(), operation.AccountID)
	if len(workspaces) != 0 || countStrings(*fixture.events, "fabric.attachment") != 0 || countStrings(*fixture.events, "fabric.runtime") != 0 || len(fixture.ledger.receiptInputs) != 0 {
		t.Fatalf("partial launch crossed activation: workspaces=%#v events=%#v receipts=%#v", workspaces, *fixture.events, fixture.ledger.receiptInputs)
	}
}

func TestWorkspaceLaunchReceiptRetry(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	configureWorkspaceLaunchFulfillment(t, fixture)
	fixture.ledger.receiptErrors = []error{errors.New("Ledger unavailable"), nil}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("first Ledger failure was not returned")
	}
	pending := fixture.operation(t)
	workspaces, _ := fixture.store.ListWorkspaces(context.Background(), pending.AccountID)
	if pending.Phase != "receipt_pending" || pending.Status != "retryable" || len(workspaces) != 1 || stringValue(workspaces[0]["runtimeId"]) == "" || len(fixture.ledger.receiptInputs) != 1 {
		t.Fatalf("receipt pending launch=%#v workspaces=%#v receipts=%#v", pending, workspaces, fixture.ledger.receiptInputs)
	}
	beforeEvents := append([]string(nil), (*fixture.events)...)
	beforeCharges := len(fixture.sub2API.charges)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	completed := fixture.operation(t)
	if completed.Status != "succeeded" || completed.Phase != "succeeded" || len(fixture.ledger.receiptInputs) != 2 || len(fixture.sub2API.charges) != beforeCharges {
		t.Fatalf("receipt retry launch=%#v charges=%#v receipts=%#v", completed, fixture.sub2API.charges, fixture.ledger.receiptInputs)
	}
	for _, event := range []string{"fabric.compute.prepare", "fabric.compute.sync", "fabric.storage.prepare", "fabric.storage.sync", "fabric.attachment", "fabric.gateway-secret", "fabric.runtime"} {
		if countStrings(*fixture.events, event) != countStrings(beforeEvents, event) {
			t.Fatalf("receipt retry repeated %s: before=%#v after=%#v", event, beforeEvents, *fixture.events)
		}
	}
}

func TestWorkspaceLaunchNoFabricBeforeDebit(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000}, []error{clients.ErrSub2APIChargeUnknown}, nil)
	err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
	operation := fixture.operation(t)
	if !errors.Is(err, clients.ErrSub2APIChargeUnknown) || operation.Phase != "debit_pending" || operation.ErrorCode != "sub2api_charge_unconfirmed" {
		t.Fatalf("unknown debit err=%v operation=%#v", err, operation)
	}
	if len(fixture.sub2API.charges) != 1 || fixture.sub2API.charges[0].ChargeUSDMicros != 52_580_000 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 || countStrings(*fixture.events, "fabric.attachment") != 0 || countStrings(*fixture.events, "fabric.runtime") != 0 {
		t.Fatalf("unconfirmed debit crossed fulfillment gate: events=%#v charges=%#v", *fixture.events, fixture.sub2API.charges)
	}
}

func TestWorkspaceLaunchRestartRecoversLostDebitResponse(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000}, nil, nil)
	gateway := &durableWorkspaceLaunchSub2API{
		workspaceLaunchSub2API: fixture.sub2API, balance: 1_000_000_000,
		appliedCharges: map[string]clients.Sub2APIChargeInput{}, loseNextResponses: 1,
	}
	service := controlplane.NewService(fixture.ledger, fixture.fabric, gateway)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), service); !errors.Is(err, clients.ErrSub2APIChargeUnknown) {
		t.Fatalf("lost response error=%v", err)
	}
	fixture.fabric.preflightResults = []clients.MonthlyPreflight{{}, {}}
	restarted, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.runWorkspaceLaunchesOnce(context.Background(), service); err != nil {
		t.Fatal(err)
	}
	operation := fixture.operation(t)
	if operation.Status != "debited" || operation.Phase != "debited" || len(gateway.chargeCalls) != 1 || gateway.chargeCalls[0].ChargeUSDMicros != 52_580_000 {
		t.Fatalf("restart operation=%#v calls=%#v", operation, gateway.chargeCalls)
	}
	if len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("restart crossed fulfillment gate: events=%#v", *fixture.events)
	}
}

func TestWorkspaceLaunchConcurrentWorkers(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000}, nil, nil)
	configureWorkspaceLaunchFulfillment(t, fixture)
	gateway := &durableWorkspaceLaunchSub2API{
		workspaceLaunchSub2API: fixture.sub2API, balance: 1_000_000_000,
		appliedCharges: map[string]clients.Sub2APIChargeInput{},
	}
	service := controlplane.NewService(fixture.ledger, fixture.fabric, gateway)
	second, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, app := range []*controlPlaneServer{fixture.app, second} {
		go func(app *controlPlaneServer) {
			<-start
			results <- app.runWorkspaceLaunchesOnce(context.Background(), service)
		}(app)
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	operation := fixture.operation(t)
	settled := operation.Status == "debited" && operation.Phase == "debited" || operation.Status == "succeeded" && operation.Phase == "succeeded"
	if !settled || len(gateway.chargeCalls) != 1 || gateway.chargeCalls[0].ChargeUSDMicros != 52_580_000 {
		t.Fatalf("concurrent operation=%#v calls=%#v", operation, gateway.chargeCalls)
	}
}

func TestWorkspaceLaunchCAS(t *testing.T) {
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	store := &workspaceLaunchRouteBarrierStore{memoryTableStore: newMemoryTableStore(), release: make(chan struct{})}
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	promoteWorkspaceLaunchOwner(t, store, "usr-alpha")

	newServer := func() (http.Handler, *httptest.ResponseRecorder) {
		events := []string{}
		gateway := &workspaceLaunchSub2API{
			monthlySub2API: &monthlySub2API{events: &events, balances: []int64{1_000_000_000}},
			keys:           map[int64]clients.Sub2APIWorkspaceKey{9: {ID: 9, UserID: 41, Name: "opl-workspace", Status: "active"}},
		}
		server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &monthlyFabric{events: &events}, gateway), store)
		if err != nil {
			t.Fatal(err)
		}
		return server, loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	}
	firstServer, firstSession := newServer()
	secondServer, secondSession := newServer()
	store.mu.Lock()
	store.armed = true
	store.mu.Unlock()

	results := make(chan *httptest.ResponseRecorder, 2)
	for index, pair := range []struct {
		server  http.Handler
		session *httptest.ResponseRecorder
	}{{firstServer, firstSession}, {secondServer, secondSession}} {
		go func(index int, pair struct {
			server  http.Handler
			session *httptest.ResponseRecorder
		}) {
			results <- requestWithMutationKeyForTest(t, pair.server, pair.session, http.MethodPost, "/api/workspace-launches", `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, fmt.Sprintf("launch-cas-%d", index))
		}(index, pair)
	}
	accepted, conflicted := 0, 0
	for range 2 {
		response := <-results
		switch response.Code {
		case http.StatusAccepted:
			accepted++
		case http.StatusConflict:
			conflicted++
		default:
			t.Fatalf("CAS response status=%d body=%s", response.Code, response.Body.String())
		}
	}
	rows, err := store.memoryTableStore.ListRuntimeOperations(context.Background())
	if err != nil || accepted != 1 || conflicted != 1 || len(rows) != 1 || stringValue(rows[0]["action"]) != "workspace.launch.v2" {
		t.Fatalf("CAS accepted=%d conflicted=%d rows=%#v err=%v", accepted, conflicted, rows, err)
	}
}

func (f workspaceLaunchWorkerFixture) operation(t *testing.T) workspaceLaunchOperation {
	t.Helper()
	rows, err := f.store.ListRuntimeOperations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	row := findRecord(rows, f.operationID)
	operation, err := decodeWorkspaceLaunchOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	return operation
}

func TestWorkspaceLaunchRevalidatesOwnerBeforeDebit(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, workspaceLaunchWorkerFixture)
	}{
		{name: "disabled", mutate: func(t *testing.T, fixture workspaceLaunchWorkerFixture) {
			owner, err := fixture.app.findUserByID(context.Background(), "usr-alpha")
			if err != nil || owner == nil {
				t.Fatalf("find launch owner: owner=%#v err=%v", owner, err)
			}
			owner["status"] = "disabled"
			if err := fixture.store.ApplyUserLifecycle(context.Background(), owner); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "reciprocal mismatch", mutate: func(_ *testing.T, fixture workspaceLaunchWorkerFixture) {
			fixture.store.mu.Lock()
			fixture.store.accounts["acct-alpha"]["ownerUserId"] = "usr-other"
			fixture.store.mu.Unlock()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000}, nil, nil)
			test.mutate(t, fixture)
			beforeEvents := append([]string(nil), (*fixture.events)...)
			if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
				t.Fatal("invalid launch owner did not stop the worker")
			}
			operation := fixture.operation(t)
			if operation.Status != "manual_review" || operation.ErrorCode != "workspace_launch_owner_identity_mismatch" || len(fixture.sub2API.charges) != 0 ||
				len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 || !reflect.DeepEqual(*fixture.events, beforeEvents) {
				t.Fatalf("invalid owner operation=%#v events=%#v before=%#v", operation, *fixture.events, beforeEvents)
			}
		})
	}
}

func TestWorkspaceLaunchOwnerLifecycleFencesClaimBeforeDebit(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000}, nil, nil)
	fixture.store.lifecycleStarted = make(chan struct{})
	fixture.store.releaseLifecycle = make(chan struct{})
	fixture.store.workspaceLaunchClaimed = make(chan struct{})

	disableResult := make(chan error, 1)
	go func() {
		_, err := fixture.app.disableUser(map[string]any{"userId": "usr-alpha", "reason": "pilot_offboarding"})
		disableResult <- err
	}()
	select {
	case <-fixture.store.lifecycleStarted:
	case <-time.After(time.Second):
		t.Fatal("account disable did not enter the lifecycle transaction")
	}

	workerResult := make(chan error, 1)
	go func() { workerResult <- fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service) }()
	claimCrossedLifecycleFence := false
	select {
	case <-fixture.store.workspaceLaunchClaimed:
		claimCrossedLifecycleFence = true
	case <-time.After(100 * time.Millisecond):
	}
	close(fixture.store.releaseLifecycle)
	if err := <-disableResult; err != nil {
		t.Fatal(err)
	}
	if err := <-workerResult; err == nil {
		t.Fatal("disabled launch owner did not stop the worker")
	}
	if claimCrossedLifecycleFence {
		t.Fatal("Workspace launch claim crossed an in-progress owner lifecycle change")
	}
	operation := fixture.operation(t)
	if operation.Status != "manual_review" || operation.ErrorCode != "workspace_launch_owner_identity_mismatch" || len(fixture.sub2API.charges) != 0 {
		t.Fatalf("disabled owner operation=%#v charges=%#v", operation, fixture.sub2API.charges)
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
