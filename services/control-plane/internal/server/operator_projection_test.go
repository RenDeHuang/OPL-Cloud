package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	pages := (len(c.users) + query.PageSize - 1) / query.PageSize
	if pages == 0 {
		pages = 1
	}
	start := (query.Page - 1) * query.PageSize
	end := start + query.PageSize
	if start > len(c.users) {
		start = len(c.users)
	}
	if end > len(c.users) {
		end = len(c.users)
	}
	return clients.Sub2APIUserPage{Items: c.users[start:end], Total: int64(len(c.users)), Page: query.Page, PageSize: query.PageSize, Pages: pages}, nil
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

func operatorAccountItem(items []any, accountID string) map[string]any {
	for _, raw := range items {
		if item := raw.(map[string]any); item["accountId"] == accountID {
			return item
		}
	}
	return nil
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
	if accountData["total"] != float64(3) || accountData["page"] != float64(1) || accountData["pageSize"] != float64(20) || len(items) != 3 {
		t.Fatalf("account page = %#v", accountData)
	}
	alpha := operatorAccountItem(items, "acct-alpha")
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

func TestOperatorAccountsCollectsAllRemotePagesWithoutUsageNPlusOne(t *testing.T) {
	store := newMemoryTableStore()
	seedOperatorProjectionAccount(t, store, "acct-alpha", "usr-alpha", "alpha@example.com", 99)
	users := make([]clients.Sub2APIUser, 0, 51)
	for id := int64(1); id <= 50; id++ {
		users = append(users, operatorProjectionUser(id, fmt.Sprintf("unrelated-%d@example.com", id), "active", 0))
	}
	users = append(users, operatorProjectionUser(99, "alpha@example.com", "active", 10_000_000))
	client := newOperatorProjectionClient(users...)
	client.userUsage[99] = clients.Sub2APIBatchUserUsage{UserID: 99, TotalActualCostUSDMicros: 123}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, client), store)
	if err != nil {
		t.Fatal(err)
	}
	response := requestWithSession(t, server, reservedOperatorSessionForTest(t, server), http.MethodGet, "/api/operator/accounts", "")
	if response.Code != http.StatusOK {
		t.Fatalf("operator accounts status=%d body=%s", response.Code, response.Body.String())
	}
	items := mapField(decodeOperatorEnvelope(t, response), "data")["items"].([]any)
	alpha := operatorAccountItem(items, "acct-alpha")
	if len(items) != 2 || mapField(alpha, "wallet")["available"] != true || mapField(alpha, "usage")["available"] != true {
		t.Fatalf("mapped later-page customer=%#v", items)
	}
	if client.adminUsersCalls != 2 || client.batchUsersCalls != 1 || client.singleUserCalls != 0 {
		t.Fatalf("calls pages=%d batch=%d singles=%d", client.adminUsersCalls, client.batchUsersCalls, client.singleUserCalls)
	}
}

func TestOperatorAccountsKeepsIdentityWhenMappedWalletHasSubMicroBalance(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(data any) {
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "success", "data": data}); err != nil {
				t.Errorf("encode Sub2API response: %v", err)
			}
		}
		switch r.URL.Path {
		case "/api/v1/auth/login":
			write(map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
		case "/api/v1/admin/users":
			write(map[string]any{
				"items": []any{
					map[string]any{"id": 1, "email": "admin@medopl.cn", "balance": 0, "status": "active", "created_at": "2026-07-18T01:02:03Z", "updated_at": "2026-07-19T04:05:06Z"},
					map[string]any{"id": 41, "email": "alpha@example.com", "balance": 10, "status": "active", "created_at": "2026-07-18T01:02:03Z", "updated_at": "2026-07-19T04:05:06Z"},
					map[string]any{"id": 42, "email": "beta@example.com", "balance": json.RawMessage("0.00000001"), "status": "active", "created_at": "2026-07-18T01:02:03Z", "updated_at": "2026-07-19T04:05:06Z"},
					map[string]any{"id": 99, "email": "unrelated@example.com", "balance": json.RawMessage("0.00000002"), "status": "active", "created_at": "2026-07-18T01:02:03Z", "updated_at": "2026-07-19T04:05:06Z"},
				},
				"total": 4, "page": 1, "page_size": 50, "pages": 1,
			})
		case "/api/v1/admin/dashboard/users-usage":
			write(map[string]any{"stats": map[string]any{
				"1":  map[string]any{"user_id": 1, "today_actual_cost": 0, "total_actual_cost": 0, "by_platform": []any{}},
				"41": map[string]any{"user_id": 41, "today_actual_cost": 0, "total_actual_cost": 0, "by_platform": []any{}},
				"42": map[string]any{"user_id": 42, "today_actual_cost": 0, "total_actual_cost": 0, "by_platform": []any{}},
			}})
		default:
			t.Errorf("unexpected Sub2API route %s %s", r.Method, r.URL.String())
		}
	}))
	t.Cleanup(upstream.Close)
	client, err := clients.NewSub2APIHTTPClient(clients.Sub2APIConfig{
		BaseURL: upstream.URL, AdminEmail: "admin@medopl.cn", AdminPassword: "admin-secret", Timeout: time.Second,
	}, upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryTableStore()
	seedOperatorProjectionAccount(t, store, "acct-alpha", "usr-alpha", "alpha@example.com", 41)
	seedOperatorProjectionAccount(t, store, "acct-beta", "usr-beta", "beta@example.com", 42)
	app := &controlPlaneServer{tables: store}
	data, status, err := app.operatorAccountPage(context.Background(), controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, client), 1, 20)
	if err != nil {
		t.Fatalf("operator account projection: %v", err)
	}
	items := data["items"].([]any)
	admin, alpha, beta := operatorAccountItem(items, "acct-admin"), operatorAccountItem(items, "acct-alpha"), operatorAccountItem(items, "acct-beta")
	if status != "available" || len(items) != 3 || mapField(admin, "wallet")["available"] != true || mapField(alpha, "wallet")["available"] != true ||
		mapField(beta, "gatewayIdentity")["available"] != true || mapField(beta, "usage")["available"] != true {
		t.Fatalf("operator account sources status=%s items=%#v", status, items)
	}
	wallet := mapField(beta, "wallet")
	if wallet["status"] != "unavailable" || wallet["available"] != false {
		t.Fatalf("sub-micro wallet source = %#v", wallet)
	}
	if _, exists := wallet["data"]; exists {
		t.Fatalf("sub-micro wallet exposed fallback data = %#v", wallet)
	}
}

func TestOperatorAccountsFreshInstallIncludesReservedAdmin(t *testing.T) {
	client := newOperatorProjectionClient(operatorProjectionUser(99, "unrelated@example.com", "active", 0))
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, client), newMemoryTableStore())
	if err != nil {
		t.Fatal(err)
	}
	response := requestWithSession(t, server, reservedOperatorSessionForTest(t, server), http.MethodGet, "/api/operator/accounts", "")
	if response.Code != http.StatusOK {
		t.Fatalf("operator accounts status=%d body=%s", response.Code, response.Body.String())
	}
	envelope := decodeOperatorEnvelope(t, response)
	data := mapField(envelope, "data")
	items := data["items"].([]any)
	admin := operatorAccountItem(items, "acct-admin")
	if envelope["status"] != "available" || data["total"] != float64(1) || len(items) != 1 || admin["role"] != "admin" || mapField(admin, "wallet")["status"] != "unavailable" || client.adminUsersCalls != 1 || client.batchUsersCalls != 1 {
		t.Fatalf("fresh projection envelope=%#v calls=%d/%d", envelope, client.adminUsersCalls, client.batchUsersCalls)
	}
}

func TestOperatorAccountsIncludesReservedAdminOwner(t *testing.T) {
	client := newOperatorProjectionClient(operatorProjectionUser(1, "admin@medopl.cn", "active", 60_000_000))
	client.userUsage[1] = clients.Sub2APIBatchUserUsage{UserID: 1, TotalActualCostUSDMicros: 123}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, client), newMemoryTableStore())
	if err != nil {
		t.Fatal(err)
	}
	response := requestWithSession(t, server, reservedOperatorSessionForTest(t, server), http.MethodGet, "/api/operator/accounts", "")
	if response.Code != http.StatusOK {
		t.Fatalf("operator accounts status=%d body=%s", response.Code, response.Body.String())
	}
	data := mapField(decodeOperatorEnvelope(t, response), "data")
	items := data["items"].([]any)
	if data["total"] != float64(1) || len(items) != 1 || items[0].(map[string]any)["accountId"] != "acct-admin" || items[0].(map[string]any)["role"] != "admin" {
		t.Fatalf("reserved admin projection=%#v", data)
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
	item := operatorAccountItem(mapField(decodeOperatorEnvelope(t, response), "data")["items"].([]any), "acct-alpha")
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
	if client.adminUsersCalls != 1 || client.batchUsersCalls != 1 || client.singleUserCalls != 0 || len(client.requestedUserIDs) != 1 || len(client.requestedUserIDs[0]) != 6 {
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

func TestOperatorReconciliationOnlyProjectsBillingReviewFacts(t *testing.T) {
	store := newMemoryTableStore()
	for _, operation := range []map[string]any{
		{"id": "gateway-key", "operationId": "gateway-key", "action": "gateway.key.create", "status": "manual_review"},
		{"id": "account-provision", "operationId": "account-provision", "action": "account.provision", "status": "manual_review"},
		{"id": "runtime-resume", "operationId": "runtime-resume", "action": "runtime.resume", "status": "started"},
		{"id": "launch-started", "operationId": "launch-started", "action": workspaceLaunchAction, "status": "started"},
		{"id": "launch-succeeded", "operationId": "launch-succeeded", "action": workspaceLaunchAction, "status": "succeeded"},
	} {
		mustStore(t, store.SaveRuntimeOperation(context.Background(), operation))
	}
	mustStore(t, store.SaveBillingReconciliation(context.Background(), map[string]any{"id": "reconcile-ok", "status": "available"}))
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, newOperatorProjectionClient()), store)
	if err != nil {
		t.Fatal(err)
	}
	response := requestWithSession(t, server, reservedOperatorSessionForTest(t, server), http.MethodGet, "/api/operator/reconciliation", "")
	if response.Code != http.StatusOK {
		t.Fatalf("reconciliation status=%d body=%s", response.Code, response.Body.String())
	}
	envelope := decodeOperatorEnvelope(t, response)
	data := mapField(envelope, "data")
	if envelope["status"] != "empty" || data["total"] != float64(0) || len(data["items"].([]any)) != 0 {
		t.Fatalf("non-billing operations leaked into review queue: %#v", envelope)
	}
}

func TestOperatorReconciliationProjectsResourceRenewalAndMismatchReviews(t *testing.T) {
	store := newMemoryTableStore()
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{
		"id": "compute-review", "accountId": "acct-alpha", "billingOperationId": "compute-billing", "billingStatus": "manual_review", "lastBillingError": "compute_unknown",
	}))
	mustStore(t, store.SaveStorage(context.Background(), map[string]any{
		"id": "storage-review", "accountId": "acct-alpha", "billingOperationId": "storage-billing", "billingStatus": "manual_review", "lastBillingError": "storage_unknown",
	}))
	renewal := workspaceRenewalOperation{
		ID: "renewal-billing", Status: "manual_review", RequestHash: "renewal-hash", Phase: "provider_review",
		AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PaidThrough: "2026-08-19T00:00:00Z", ErrorCode: "provider_unknown",
	}
	mustStore(t, store.SaveRuntimeOperation(context.Background(), workspaceRenewalOperationRow(renewal)))
	mustStore(t, store.SaveBillingReconciliation(context.Background(), map[string]any{"id": "reconcile-mismatch", "status": "mismatch", "reason": "balance_mismatch"}))
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, newOperatorProjectionClient()), store)
	if err != nil {
		t.Fatal(err)
	}
	response := requestWithSession(t, server, reservedOperatorSessionForTest(t, server), http.MethodGet, "/api/operator/reconciliation", "")
	if response.Code != http.StatusOK {
		t.Fatalf("reconciliation status=%d body=%s", response.Code, response.Body.String())
	}
	data := mapField(decodeOperatorEnvelope(t, response), "data")
	items := data["items"].([]any)
	if data["total"] != float64(4) || len(items) != 4 {
		t.Fatalf("billing review items=%#v", data)
	}
	byID := map[string]map[string]any{}
	for _, raw := range items {
		item := raw.(map[string]any)
		byID[stringValue(item["id"])] = item
	}
	for id, resourceType := range map[string]string{"compute-review": "compute", "storage-review": "storage", "ws-alpha": "workspace"} {
		item := byID[id]
		actions, _ := item["allowedActions"].([]any)
		if item["resourceType"] != resourceType || item["status"] != "manual_review" || len(actions) != 1 || actions[0] != "resolve_billing_review" {
			t.Fatalf("%s review=%#v", id, item)
		}
	}
	if byID["ws-alpha"]["billingOperationId"] != renewal.ID || byID["ws-alpha"]["operationRef"] != renewal.ID {
		t.Fatalf("renewal identity=%#v", byID["ws-alpha"])
	}
	mismatchActions, _ := byID["reconcile-mismatch"]["allowedActions"].([]any)
	if byID["reconcile-mismatch"]["status"] != "mismatch" || len(mismatchActions) != 0 {
		t.Fatalf("mismatch review=%#v", byID["reconcile-mismatch"])
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
	admin := requestWithMutationKeyForTest(t, server, reservedOperatorSessionForTest(t, server), http.MethodPost, "/api/operator/accounts/acct-admin/disable", `{"confirmationAccountId":"acct-admin","reason":"pilot_offboarding"}`, "disable-admin-forbidden")
	if admin.Code != http.StatusBadRequest || !strings.Contains(admin.Body.String(), "last_active_admin") {
		t.Fatalf("admin disable status=%d body=%s", admin.Code, admin.Body.String())
	}
}
