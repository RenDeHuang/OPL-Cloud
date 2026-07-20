package server

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

var operatorProjectionTime = time.Date(2026, 7, 19, 4, 5, 6, 0, time.UTC)

type operatorProjectionSub2API struct {
	*testSub2APIClient
	users              []clients.Sub2APIUser
	userUsage          map[int64]clients.Sub2APIBatchUserUsage
	keyUsage           map[int64]clients.Sub2APIBatchKeyUsage
	adminUsersErr      error
	batchUsersErr      error
	batchKeysErr       error
	adminUsersCalls    int
	batchUsersCalls    int
	batchKeysCalls     int
	singleUserCalls    int
	requestedUserIDs   [][]int64
	requestedAPIKeyIDs [][]int64
}

func (c *operatorProjectionSub2API) AdminUsers(_ context.Context, query clients.Sub2APIUserPageQuery) (clients.Sub2APIUserPage, error) {
	c.adminUsersCalls++
	if c.adminUsersErr != nil {
		return clients.Sub2APIUserPage{}, c.adminUsersErr
	}
	return clients.Sub2APIUserPage{Items: c.users, Total: int64(len(c.users)), Page: query.Page, PageSize: query.PageSize, Pages: 1}, nil
}

func (c *operatorProjectionSub2API) BatchUsersUsage(_ context.Context, ids []int64) (map[int64]clients.Sub2APIBatchUserUsage, error) {
	c.batchUsersCalls++
	c.requestedUserIDs = append(c.requestedUserIDs, append([]int64(nil), ids...))
	if c.batchUsersErr != nil {
		return nil, c.batchUsersErr
	}
	result := make(map[int64]clients.Sub2APIBatchUserUsage, len(ids))
	for _, id := range ids {
		result[id] = c.userUsage[id]
	}
	return result, nil
}

func (c *operatorProjectionSub2API) BatchKeysUsage(_ context.Context, ids []int64) (map[int64]clients.Sub2APIBatchKeyUsage, error) {
	c.batchKeysCalls++
	c.requestedAPIKeyIDs = append(c.requestedAPIKeyIDs, append([]int64(nil), ids...))
	if c.batchKeysErr != nil {
		return nil, c.batchKeysErr
	}
	result := make(map[int64]clients.Sub2APIBatchKeyUsage, len(ids))
	for _, id := range ids {
		result[id] = c.keyUsage[id]
	}
	return result, nil
}

func (c *operatorProjectionSub2API) User(ctx context.Context, id int64) (clients.Sub2APIIdentity, error) {
	c.singleUserCalls++
	return c.testSub2APIClient.User(ctx, id)
}

type operatorProjectionLedger struct {
	fakeLedgerClient
	receipts map[string]clients.Receipt
	err      error
}

type operatorProjectionNoOperationsFabric struct{ fakeFabricClient }

func (*operatorProjectionNoOperationsFabric) ListOperations(context.Context) ([]clients.FabricOperation, error) {
	return []clients.FabricOperation{}, nil
}

func decodeOperatorEnvelope(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope map[string]any
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode operator envelope: %v: %s", err, response.Body.String())
	}
	if envelope["available"] != true {
		t.Fatalf("operator envelope = %#v", envelope)
	}
	if _, err := time.Parse(time.RFC3339Nano, stringValue(envelope["fetchedAt"])); err != nil {
		t.Fatalf("operator fetchedAt = %#v: %v", envelope["fetchedAt"], err)
	}
	return envelope
}

func (l *operatorProjectionLedger) Receipt(_ context.Context, id string) (clients.Receipt, error) {
	if l.err != nil {
		return clients.Receipt{}, l.err
	}
	receipt, ok := l.receipts[id]
	if !ok {
		return clients.Receipt{}, errors.New("receipt unavailable")
	}
	return receipt, nil
}

func newOperatorProjectionClient(users ...clients.Sub2APIUser) *operatorProjectionSub2API {
	return &operatorProjectionSub2API{
		testSub2APIClient: &testSub2APIClient{balance: 1_000_000_000, charges: map[string]int64{}},
		users:             users,
		userUsage:         map[int64]clients.Sub2APIBatchUserUsage{},
		keyUsage:          map[int64]clients.Sub2APIBatchKeyUsage{},
	}
}

func seedOperatorProjectionAccount(t *testing.T, store controlPlaneTableStore, accountID, userID, email string, remoteID int64) {
	t.Helper()
	organizationID := "org-" + accountID
	mustStore(t, store.CreateInvitedAccount(context.Background(),
		map[string]any{"id": accountID, "ownerUserId": userID, "sub2apiUserId": remoteID, "status": "active", "updatedAt": operatorProjectionTime.Add(-2 * time.Hour).Format(time.RFC3339)},
		map[string]any{"id": userID, "email": email, "accountId": accountID, "role": "owner", "status": "active"},
		map[string]any{"id": organizationID, "name": "Organization " + accountID, "billingAccountId": accountID, "status": "active"},
		map[string]any{"id": "mem-" + userID, "organizationId": organizationID, "userId": userID, "accountId": accountID, "role": "owner", "status": "active"},
	))
}

func operatorProjectionUser(id int64, email, status string, balance int64) clients.Sub2APIUser {
	return clients.Sub2APIUser{ID: id, Email: email, Status: status, BalanceUSDMicros: balance, CreatedAt: operatorProjectionTime.Add(-time.Hour), UpdatedAt: operatorProjectionTime}
}

func TestOperatorProjectionUsesBatchAPIs(t *testing.T) {
	store := newMemoryTableStore()
	seedOperatorProjectionAccount(t, store, "acct-alpha", "usr-alpha", "alpha@example.com", 41)
	seedOperatorProjectionAccount(t, store, "acct-beta", "usr-beta", "beta@example.com", 42)
	for _, workspace := range []map[string]any{
		{"id": "ws-alpha", "ownerAccountId": "acct-alpha", "ownerUserId": "usr-alpha", "accountId": "acct-alpha", "state": "active", "createdAt": operatorProjectionTime.Add(-time.Hour).Format(time.RFC3339), "updatedAt": operatorProjectionTime.Format(time.RFC3339), "workspaceApiKeyId": int64(7)},
		{"id": "ws-beta", "ownerAccountId": "acct-beta", "ownerUserId": "usr-beta", "accountId": "acct-beta", "state": "active", "createdAt": operatorProjectionTime.Add(-time.Hour).Format(time.RFC3339), "updatedAt": operatorProjectionTime.Format(time.RFC3339), "workspaceApiKeyId": int64(9)},
	} {
		mustStore(t, store.SaveWorkspace(context.Background(), workspace))
	}
	client := newOperatorProjectionClient(
		operatorProjectionUser(41, "alpha@example.com", "active", 10_000_000),
		operatorProjectionUser(42, "beta@example.com", "disabled", 20_000_000),
	)
	client.userUsage[41] = clients.Sub2APIBatchUserUsage{UserID: 41, TodayActualCostUSDMicros: 1, TotalActualCostUSDMicros: 100}
	client.userUsage[42] = clients.Sub2APIBatchUserUsage{UserID: 42, TodayActualCostUSDMicros: 2, TotalActualCostUSDMicros: 200}
	client.keyUsage[7] = clients.Sub2APIBatchKeyUsage{APIKeyID: 7, TotalActualCostUSDMicros: 70}
	client.keyUsage[9] = clients.Sub2APIBatchKeyUsage{APIKeyID: 9, TotalActualCostUSDMicros: 90}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, client), store)
	if err != nil {
		t.Fatal(err)
	}
	operator := reservedOperatorSessionForTest(t, server)

	accounts := requestWithSession(t, server, operator, http.MethodGet, "/api/operator/accounts?page=1&pageSize=20", "")
	if accounts.Code != http.StatusOK {
		t.Fatalf("operator accounts = %d: %s", accounts.Code, accounts.Body.String())
	}
	accountData := mapField(decodeOperatorEnvelope(t, accounts), "data")
	items := accountData["items"].([]any)
	if accountData["total"] != float64(2) || accountData["page"] != float64(1) || accountData["pageSize"] != float64(20) || len(items) != 2 {
		t.Fatalf("account page = %#v", accountData)
	}
	alpha := items[0].(map[string]any)
	if mapField(mapField(alpha, "wallet"), "data")["usdMicros"] != float64(10_000_000) || mapField(mapField(alpha, "usage"), "data")["totalActualCostUsdMicros"] != float64(100) {
		t.Fatalf("account projection = %#v", alpha)
	}
	if mapField(alpha, "wallet")["sourceUpdatedAt"] != operatorProjectionTime.Format(time.RFC3339Nano) {
		t.Fatalf("wallet source timestamp = %#v", mapField(alpha, "wallet"))
	}
	if _, exists := mapField(alpha, "workspaceCount")["sourceUpdatedAt"]; exists {
		t.Fatalf("workspace count borrowed unrelated source timestamp: %#v", mapField(alpha, "workspaceCount"))
	}

	workspaces := requestWithSession(t, server, operator, http.MethodGet, "/api/operator/workspaces?page=1&pageSize=20", "")
	if workspaces.Code != http.StatusOK {
		t.Fatalf("operator workspaces = %d: %s", workspaces.Code, workspaces.Body.String())
	}
	workspaceData := mapField(decodeOperatorEnvelope(t, workspaces), "data")
	workspaceItems := workspaceData["items"].([]any)
	keyUsage := mapField(workspaceItems[0].(map[string]any), "workspaceKeyUsage")
	if mapField(keyUsage, "data")["totalActualCostUsdMicros"] != float64(70) {
		t.Fatalf("workspace key usage = %#v", workspaceItems[0])
	}
	if client.adminUsersCalls != 1 || client.batchUsersCalls != 1 || client.batchKeysCalls != 1 || client.singleUserCalls != 0 {
		t.Fatalf("projection calls users=%d batchUsers=%d batchKeys=%d singleUsers=%d", client.adminUsersCalls, client.batchUsersCalls, client.batchKeysCalls, client.singleUserCalls)
	}
}

func TestOperatorOverviewRejectsMoneyAggregationOverflow(t *testing.T) {
	store := newMemoryTableStore()
	seedOperatorProjectionAccount(t, store, "acct-alpha", "usr-alpha", "alpha@example.com", 41)
	seedOperatorProjectionAccount(t, store, "acct-beta", "usr-beta", "beta@example.com", 42)
	client := newOperatorProjectionClient(
		operatorProjectionUser(41, "alpha@example.com", "active", math.MaxInt64),
		operatorProjectionUser(42, "beta@example.com", "active", 1),
	)
	client.userUsage[41] = clients.Sub2APIBatchUserUsage{UserID: 41, TodayActualCostUSDMicros: math.MaxInt64, TotalActualCostUSDMicros: math.MaxInt64}
	client.userUsage[42] = clients.Sub2APIBatchUserUsage{UserID: 42, TodayActualCostUSDMicros: 1, TotalActualCostUSDMicros: 1}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, client), store)
	if err != nil {
		t.Fatal(err)
	}
	response := requestWithSession(t, server, reservedOperatorSessionForTest(t, server), http.MethodGet, "/api/operator/overview", "")
	if response.Code != http.StatusOK {
		t.Fatalf("operator overview = %d: %s", response.Code, response.Body.String())
	}
	data := mapField(decodeOperatorEnvelope(t, response), "data")
	for _, field := range []string{"wallet", "usage"} {
		envelope := mapField(data, field)
		if envelope["status"] != "unavailable" || envelope["available"] != false {
			t.Fatalf("overflowed %s must be unavailable: %#v", field, envelope)
		}
		if _, exists := envelope["data"]; exists {
			t.Fatalf("overflowed %s exposed data: %#v", field, envelope)
		}
	}
}

func TestOperatorProjectionPartialFailure(t *testing.T) {
	store := newMemoryTableStore()
	seedOperatorProjectionAccount(t, store, "acct-alpha", "usr-alpha", "alpha@example.com", 41)
	client := newOperatorProjectionClient(operatorProjectionUser(41, "alpha@example.com", "active", 10_000_000))
	client.batchUsersErr = errors.New("batch usage unavailable")
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, client), store)
	if err != nil {
		t.Fatal(err)
	}
	response := requestWithSession(t, server, reservedOperatorSessionForTest(t, server), http.MethodGet, "/api/operator/accounts", "")
	if response.Code != http.StatusOK {
		t.Fatalf("partial projection = %d: %s", response.Code, response.Body.String())
	}
	item := mapField(decodeOperatorEnvelope(t, response), "data")["items"].([]any)[0].(map[string]any)
	if mapField(item, "wallet")["available"] != true {
		t.Fatalf("wallet should remain available: %#v", item)
	}
	usage := mapField(item, "usage")
	if usage["status"] != "unavailable" || usage["available"] != false {
		t.Fatalf("usage source = %#v", usage)
	}
	if _, exists := usage["data"]; exists {
		t.Fatalf("unavailable usage must not contain zero data: %#v", usage)
	}
}

func TestOperatorProjectionHasNoNPlusOne(t *testing.T) {
	store := newMemoryTableStore()
	client := newOperatorProjectionClient()
	for i := int64(1); i <= 5; i++ {
		accountID, userID := "acct-"+string(rune('a'+i-1)), "usr-"+string(rune('a'+i-1))
		email := userID + "@example.com"
		remoteID := 40 + i
		seedOperatorProjectionAccount(t, store, accountID, userID, email, remoteID)
		client.users = append(client.users, operatorProjectionUser(remoteID, email, "active", i*1_000_000))
		client.userUsage[remoteID] = clients.Sub2APIBatchUserUsage{UserID: remoteID}
	}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, client), store)
	if err != nil {
		t.Fatal(err)
	}
	response := requestWithSession(t, server, reservedOperatorSessionForTest(t, server), http.MethodGet, "/api/operator/accounts", "")
	if response.Code != http.StatusOK {
		t.Fatalf("five-account projection = %d: %s", response.Code, response.Body.String())
	}
	if client.adminUsersCalls != 1 || client.batchUsersCalls != 1 || client.singleUserCalls != 0 || len(client.requestedUserIDs) != 1 || len(client.requestedUserIDs[0]) != 5 {
		t.Fatalf("N+1 calls users=%d batch=%d single=%d ids=%#v", client.adminUsersCalls, client.batchUsersCalls, client.singleUserCalls, client.requestedUserIDs)
	}
}

func TestOperatorProjectionReadSurfaces(t *testing.T) {
	store := newMemoryTableStore()
	seedOperatorProjectionAccount(t, store, "acct-alpha", "usr-alpha", "alpha@example.com", 41)
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{
		"id": "ws-alpha", "ownerAccountId": "acct-alpha", "ownerUserId": "usr-alpha", "accountId": "acct-alpha", "state": "active",
		"createdAt": "2026-07-18T00:00:00Z", "updatedAt": "2026-07-19T00:00:00Z",
	}))
	client := newOperatorProjectionClient(operatorProjectionUser(41, "alpha@example.com", "active", 1_000_000))
	client.userUsage[41] = clients.Sub2APIBatchUserUsage{UserID: 41, TotalActualCostUSDMicros: 100}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, client), store)
	if err != nil {
		t.Fatal(err)
	}
	operator := reservedOperatorSessionForTest(t, server)
	for _, path := range []string{"/api/operator/overview", "/api/operator/reconciliation", "/api/operator/health"} {
		response := requestWithSession(t, server, operator, http.MethodGet, path, "")
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, response.Code, response.Body.String())
		}
		envelope := decodeOperatorEnvelope(t, response)
		if _, ok := envelope["data"].(map[string]any); !ok {
			t.Fatalf("GET %s data = %#v", path, envelope)
		}
	}
	invalid := requestWithSession(t, server, operator, http.MethodGet, "/api/operator/accounts?pageSize=51", "")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid operator pagination = %d: %s", invalid.Code, invalid.Body.String())
	}
}

func TestOperatorReconciliationProjectsLaunchRecoveryIdentity(t *testing.T) {
	store := newMemoryTableStore()
	operation := newWorkspaceLaunchOperation("acct-alpha", "usr-alpha", "Alpha", "basic", 10, false, pilotPriceVersion, 52_580_000, "review-launch")
	operation.WorkspaceAPIKeyID = 19
	operation.Status, operation.Phase, operation.ErrorCode = "manual_review", "storage_fulfilling", "fabric_storage_confirmed_absent_after_compute_created"
	mustStore(t, store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, newOperatorProjectionClient()), store)
	if err != nil {
		t.Fatal(err)
	}
	response := requestWithSession(t, server, reservedOperatorSessionForTest(t, server), http.MethodGet, "/api/operator/reconciliation", "")
	if response.Code != http.StatusOK {
		t.Fatalf("launch reconciliation status=%d body=%s", response.Code, response.Body.String())
	}
	items := mapField(decodeOperatorEnvelope(t, response), "data")["items"].([]any)
	item := items[0].(map[string]any)
	actions, ok := item["allowedActions"].([]any)
	if item["id"] != operation.ID || item["accountId"] != operation.AccountID || item["billingOperationId"] != operation.ID ||
		item["phase"] != operation.Phase || item["errorCode"] != operation.ErrorCode || !ok || len(actions) != 1 || actions[0] != "recover_workspace_launch" {
		t.Fatalf("launch reconciliation item=%#v", item)
	}
}

func TestOperatorResourceOwnerFields(t *testing.T) {
	store := newMemoryTableStore()
	seedOperatorProjectionAccount(t, store, "acct-alpha", "usr-alpha", "alpha@example.com", 41)
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{
		"id": "ws-alpha", "name": "Alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha", "ownerUserId": "usr-alpha", "state": "active",
		"packageId": "basic", "createdAt": "2026-07-18T00:00:00Z", "updatedAt": "2026-07-19T00:00:00Z", "receiptId": "receipt-workspace",
	}))
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{
		"id": "compute-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "packageId": "basic", "instanceType": "S5.MEDIUM4",
		"providerResourceId": "ins-alpha", "zone": "ap-shanghai-2", "providerStatus": "running", "status": "running", "deadline": "2026-08-18T00:00:00Z",
		"lastProviderSyncAt": "2026-07-19T03:00:00Z", "operationId": "op-compute", "lastReceiptId": "receipt-compute", "createdAt": "2026-07-18T00:00:00Z", "updatedAt": "2026-07-19T03:00:00Z",
	}))
	ledger := &operatorProjectionLedger{receipts: map[string]clients.Receipt{
		"receipt-workspace": {ReceiptInput: clients.ReceiptInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Type: "workspace.created", Status: "completed"}, ReceiptID: "receipt-workspace"},
		"receipt-compute":   {ReceiptInput: clients.ReceiptInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Type: "billing.resource_purchased.v1", Status: "completed", Cost: map[string]any{"resourceType": "compute", "resourceId": "compute-alpha"}}, ReceiptID: "receipt-compute"},
	}}
	client := newOperatorProjectionClient(operatorProjectionUser(41, "alpha@example.com", "active", 1_000_000))
	server, err := NewPersistentServer(controlplane.NewService(ledger, &fakeFabricClient{}, client), store)
	if err != nil {
		t.Fatal(err)
	}
	response := requestWithSession(t, server, reservedOperatorSessionForTest(t, server), http.MethodGet, "/api/operator/workspaces/ws-alpha", "")
	if response.Code != http.StatusOK {
		t.Fatalf("operator workspace detail = %d: %s", response.Code, response.Body.String())
	}
	data := mapField(decodeOperatorEnvelope(t, response), "data")
	resources := data["resources"].([]any)
	if len(resources) != 1 {
		t.Fatalf("resources = %#v", resources)
	}
	resource := resources[0].(map[string]any)
	want := map[string]any{
		"ownerAccount": "acct-alpha", "ownerUser": "usr-alpha", "workspace": "ws-alpha", "resourceType": "compute", "packageOrSpec": "S5.MEDIUM4",
		"providerId": "ins-alpha", "zone": "ap-shanghai-2", "status": "running", "expiresAt": "2026-08-18T00:00:00Z", "lastReadAt": "2026-07-19T03:00:00Z", "operationRef": "op-compute", "receiptRef": "receipt-compute",
	}
	for field, value := range want {
		envelope := mapField(resource, field)
		if envelope["available"] != true {
			t.Fatalf("%s unavailable: %#v", field, envelope)
		}
		got := envelope["data"]
		if object, ok := got.(map[string]any); ok {
			got = object["id"]
		}
		if got != value {
			t.Fatalf("%s = %#v, want %#v", field, got, value)
		}
	}
	created := mapField(resource, "createdAt")
	if created["status"] != "unavailable" || created["available"] != false {
		t.Fatalf("provider createdAt must stay unavailable: %#v", created)
	}
}

func TestOperatorResourceUnavailableFields(t *testing.T) {
	store := newMemoryTableStore()
	seedOperatorProjectionAccount(t, store, "acct-alpha", "usr-alpha", "alpha@example.com", 41)
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{
		"id": "ws-alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha", "ownerUserId": "usr-alpha", "state": "active", "createdAt": "2026-07-18T00:00:00Z", "updatedAt": "2026-07-19T00:00:00Z",
	}))
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{"id": "compute-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "status": "provisioning"}))
	client := newOperatorProjectionClient(operatorProjectionUser(41, "alpha@example.com", "active", 1_000_000))
	server, err := NewPersistentServer(controlplane.NewService(&operatorProjectionLedger{receipts: map[string]clients.Receipt{}}, &operatorProjectionNoOperationsFabric{}, client), store)
	if err != nil {
		t.Fatal(err)
	}
	response := requestWithSession(t, server, reservedOperatorSessionForTest(t, server), http.MethodGet, "/api/operator/workspaces/ws-alpha", "")
	if response.Code != http.StatusOK {
		t.Fatalf("operator workspace unavailable detail = %d: %s", response.Code, response.Body.String())
	}
	resource := mapField(decodeOperatorEnvelope(t, response), "data")["resources"].([]any)[0].(map[string]any)
	for _, field := range []string{"packageOrSpec", "providerId", "zone", "createdAt", "expiresAt", "lastReadAt", "operationRef", "receiptRef"} {
		envelope := mapField(resource, field)
		if envelope["status"] != "unavailable" || envelope["available"] != false {
			t.Fatalf("%s must be unavailable: %#v", field, envelope)
		}
		if _, exists := envelope["data"]; exists {
			t.Fatalf("%s unavailable data = %#v", field, envelope)
		}
	}
}

func TestOperatorAccountProvisionUsesCanonicalRouteAndTerminology(t *testing.T) {
	store := newMemoryTableStore()
	client := newOperatorProjectionClient()
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, client), store)
	if err != nil {
		t.Fatal(err)
	}
	operator := reservedOperatorSessionForTest(t, server)
	response := requestWithMutationKeyForTest(t, server, operator, http.MethodPost, "/api/operator/accounts", `{"email":"provision@example.com","password":"CorrectHorseBatteryStaple!","name":"Provision"}`, "provision-account-once")
	if response.Code != http.StatusCreated {
		t.Fatalf("canonical provision = %d: %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "succeeded" || !strings.HasPrefix(stringValue(body["operationId"]), "account-provision-") || !strings.HasPrefix(stringValue(body["accountId"]), "acct-") {
		t.Fatalf("account provision body = %#v", body)
	}
	events, err := store.ListAuditEvents(context.Background(), stringValue(body["accountId"]))
	if err != nil || len(events) != 1 || events[0]["action"] != "account.provision" {
		t.Fatalf("account provision audit=%#v err=%v", events, err)
	}
	legacy := requestWithMutationKeyForTest(t, server, operator, http.MethodPost, "/api/operator/accounts/invitations", `{"email":"legacy@example.com","password":"CorrectHorseBatteryStaple!"}`, "legacy-invite")
	if legacy.Code != http.StatusNotFound {
		t.Fatalf("legacy invitation route status=%d body=%s", legacy.Code, legacy.Body.String())
	}
}

func TestOperatorAccountDisable(t *testing.T) {
	store := newMemoryTableStore()
	seedOperatorProjectionAccount(t, store, "acct-alpha", "usr-alpha", "alpha@example.com", 41)
	client := newOperatorProjectionClient(operatorProjectionUser(41, "alpha@example.com", "active", 1_000_000))
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, client), store)
	if err != nil {
		t.Fatal(err)
	}
	response := requestWithMutationKeyForTest(t, server, reservedOperatorSessionForTest(t, server), http.MethodPost, "/api/operator/accounts/acct-alpha/disable", `{"confirmationAccountId":"acct-alpha","reason":"pilot_offboarding"}`, "disable-account-once")
	if response.Code != http.StatusOK {
		t.Fatalf("account disable = %d: %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["accountId"] != "acct-alpha" || body["status"] != "succeeded" || !strings.HasPrefix(stringValue(body["operationId"]), "account-disable-") {
		t.Fatalf("account disable body = %#v", body)
	}
	users, err := store.ListUsers(context.Background(), true)
	if err != nil || stringValue(findRecord(users, "usr-alpha")["status"]) != "disabled" {
		t.Fatalf("disabled user = %#v err=%v", users, err)
	}
}
