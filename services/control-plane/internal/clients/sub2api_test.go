package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newSub2APITestClient(t *testing.T, handler http.HandlerFunc, timeout time.Duration) *Sub2APIHTTPClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewSub2APIHTTPClient(Sub2APIConfig{
		BaseURL:       server.URL,
		AdminEmail:    "admin@example.test",
		AdminPassword: "admin-secret",
		Timeout:       timeout,
	}, server.Client())
	if err != nil {
		t.Fatalf("new Sub2API client: %v", err)
	}
	return client
}

func writeSub2APISuccess(t *testing.T, w http.ResponseWriter, data any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "success", "data": data}); err != nil {
		t.Errorf("encode Sub2API fixture response: %v", err)
	}
}

func TestSub2APIAdminUsersPagination(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
		case "/api/v1/admin/users":
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer admin-access" {
				t.Fatalf("admin users request = %s auth=%q", r.Method, r.Header.Get("Authorization"))
			}
			query := r.URL.Query()
			if query.Get("page") != "2" || query.Get("page_size") != "2" || query.Get("search") != "pilot@example.com" || query.Get("sort_by") != "id" || query.Get("sort_order") != "asc" {
				t.Fatalf("admin users query = %q", r.URL.RawQuery)
			}
			writeSub2APISuccess(t, w, map[string]any{
				"items": []any{map[string]any{
					"id": 42, "email": "Pilot@Example.com", "balance": 12.345678, "status": "active",
					"created_at": "2026-07-18T01:02:03Z", "updated_at": "2026-07-19T04:05:06Z",
				}},
				"total": 3, "page": 2, "page_size": 2, "pages": 2,
			})
		default:
			t.Fatalf("unexpected route %s %s", r.Method, r.URL.String())
		}
	}, time.Second)

	page, err := client.AdminUsers(context.Background(), Sub2APIUserPageQuery{
		Page: 2, PageSize: 2, Search: "pilot@example.com", SortBy: "id", SortOrder: "asc",
	})
	if err != nil {
		t.Fatalf("admin users: %v", err)
	}
	if page.Total != 3 || page.Page != 2 || page.PageSize != 2 || page.Pages != 2 || len(page.Items) != 1 {
		t.Fatalf("admin users page = %#v", page)
	}
	user := page.Items[0]
	if user.ID != 42 || user.Email != "pilot@example.com" || user.Status != "active" || user.BalanceUSDMicros != 12_345_678 || user.CreatedAt.Format(time.RFC3339) != "2026-07-18T01:02:03Z" || user.UpdatedAt.Format(time.RFC3339) != "2026-07-19T04:05:06Z" {
		t.Fatalf("admin user = %#v", user)
	}
}

func TestSub2APIBatchUsersUsage(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
		case "/api/v1/admin/dashboard/users-usage":
			var input struct {
				UserIDs []int64 `json:"user_ids"`
			}
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&input) != nil || !slices.Equal(input.UserIDs, []int64{41, 42}) {
				t.Fatalf("batch users request = %s %#v", r.Method, input)
			}
			writeSub2APISuccess(t, w, map[string]any{"stats": map[string]any{
				"41": map[string]any{"user_id": 41, "today_actual_cost": 0.000001, "total_actual_cost": 1.25},
				"42": map[string]any{"user_id": 42, "today_actual_cost": 0, "total_actual_cost": 2.5},
			}})
		default:
			t.Fatalf("unexpected route %s %s", r.Method, r.URL.String())
		}
	}, time.Second)

	stats, err := client.BatchUsersUsage(context.Background(), []int64{42, 41, 42})
	if err != nil || len(stats) != 2 || stats[41].TodayActualCostUSDMicros != 1 || stats[41].TotalActualCostUSDMicros != 1_250_000 || stats[42].TotalActualCostUSDMicros != 2_500_000 {
		t.Fatalf("batch users usage = %#v err=%v", stats, err)
	}
}

func TestSub2APIBatchKeysUsage(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
		case "/api/v1/admin/dashboard/api-keys-usage":
			var input struct {
				APIKeyIDs []int64 `json:"api_key_ids"`
			}
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&input) != nil || !slices.Equal(input.APIKeyIDs, []int64{7, 9}) {
				t.Fatalf("batch keys request = %s %#v", r.Method, input)
			}
			writeSub2APISuccess(t, w, map[string]any{"stats": map[string]any{
				"7": map[string]any{"api_key_id": 7, "today_actual_cost": 0.125, "total_actual_cost": 4.5},
				"9": map[string]any{"api_key_id": 9, "today_actual_cost": 0, "total_actual_cost": 0},
			}})
		default:
			t.Fatalf("unexpected route %s %s", r.Method, r.URL.String())
		}
	}, time.Second)

	stats, err := client.BatchKeysUsage(context.Background(), []int64{9, 7, 9})
	if err != nil || len(stats) != 2 || stats[7].TodayActualCostUSDMicros != 125_000 || stats[7].TotalActualCostUSDMicros != 4_500_000 || stats[9].TotalActualCostUSDMicros != 0 {
		t.Fatalf("batch keys usage = %#v err=%v", stats, err)
	}
}

func userKeyFixture(id int64, status string) map[string]any {
	return map[string]any{
		"id": id, "user_id": 41, "key": "sk-user-secret", "name": "general-key", "status": status,
		"quota": 12.345678, "quota_used": 1.25, "usage_5h": 0.1, "usage_1d": 0.2, "usage_7d": 0.3,
		"last_used_at": "2026-07-18T01:02:03Z", "expires_at": "2026-08-18T01:02:03Z",
	}
}

func TestUserKeyCreateIdempotent(t *testing.T) {
	calls := 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/keys" || r.Header.Get("Authorization") != "Bearer delegated-user-token" || r.Header.Get("Idempotency-Key") != "key-create-once" {
			t.Fatalf("unexpected delegated create: %s %s auth=%q idempotency=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key"))
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if len(input) != 2 || input["name"] != "general-key" || input["quota"] != 12.345678 {
			t.Fatalf("create input = %#v", input)
		}
		writeSub2APISuccess(t, w, userKeyFixture(17, "active"))
	}, time.Second)

	key, err := client.CreateUserKey(context.Background(), SessionDelegatedCredential{Bearer: "delegated-user-token"}, 41, Sub2APICreateKeyInput{
		Name: "general-key", QuotaUSDMicros: 12_345_678,
	}, "key-create-once")
	if err != nil || key.ID != 17 || key.UserID != 41 || key.Key != "sk-user-secret" || key.Status != "active" {
		t.Fatalf("created key = %#v err=%v", key, err)
	}
	if calls != 1 {
		t.Fatalf("create calls = %d, want 1", calls)
	}
}

func TestUserKeyCreateExpiresInDays(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if len(input) != 3 || input["expires_in_days"] != float64(30) {
			t.Fatalf("create expiry input = %#v", input)
		}
		if _, exists := input["expires_at"]; exists {
			t.Fatalf("create must not simulate exact expiry: %#v", input)
		}
		writeSub2APISuccess(t, w, userKeyFixture(17, "active"))
	}, time.Second)

	days := 30
	key, err := client.CreateUserKey(context.Background(), SessionDelegatedCredential{Bearer: "delegated-user-token"}, 41, Sub2APICreateKeyInput{
		Name: "general-key", QuotaUSDMicros: 12_345_678, ExpiresInDays: &days,
	}, "key-create-expiry")
	if err != nil || key.ExpiresAt == nil || key.ExpiresAt.Format(time.RFC3339) != "2026-08-18T01:02:03Z" {
		t.Fatalf("created expiry = %#v err=%v", key.ExpiresAt, err)
	}
}

func TestUserKeyUpdate(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/keys/17" || r.Header.Get("Authorization") != "Bearer delegated-user-token" {
			t.Fatalf("unexpected delegated update: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if len(input) != 3 || input["name"] != "renamed" || input["quota"] != 2.5 || input["status"] != "inactive" {
			t.Fatalf("update input = %#v", input)
		}
		fixture := userKeyFixture(17, "inactive")
		fixture["name"], fixture["quota"] = "renamed", 2.5
		writeSub2APISuccess(t, w, fixture)
	}, time.Second)

	name, quota, enabled := "renamed", int64(2_500_000), false
	key, err := client.UpdateUserKey(context.Background(), SessionDelegatedCredential{Bearer: "delegated-user-token"}, 41, 17, Sub2APIUpdateKeyInput{
		Name: &name, QuotaUSDMicros: &quota, Enabled: &enabled,
	})
	if err != nil || key.Name != name || key.QuotaUSDMicros != quota || key.Status != "disabled" {
		t.Fatalf("updated key = %#v err=%v", key, err)
	}
}

func TestUserKeyDelete(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/keys/17" || r.Header.Get("Authorization") != "Bearer delegated-user-token" {
			t.Fatalf("unexpected delegated delete: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusNoContent)
	}, time.Second)
	if err := client.DeleteUserKey(context.Background(), SessionDelegatedCredential{Bearer: "delegated-user-token"}, 41, 17); err != nil {
		t.Fatalf("delete key: %v", err)
	}
}

func TestUserKeyUsage(t *testing.T) {
	requests := 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
		case "/api/v1/keys/17":
			if r.Header.Get("Authorization") != "Bearer delegated-user-token" {
				t.Fatalf("key read used wrong authorization: %q", r.Header.Get("Authorization"))
			}
			writeSub2APISuccess(t, w, userKeyFixture(17, "active"))
		case "/api/v1/admin/usage/stats":
			if r.URL.Query().Get("user_id") != "41" || r.URL.Query().Has("api_key_id") || r.URL.Query().Get("period") != "month" {
				t.Fatalf("account usage query = %q", r.URL.RawQuery)
			}
			writeSub2APISuccess(t, w, map[string]any{"total_requests": 2, "total_input_tokens": 3, "total_output_tokens": 4, "total_tokens": 7, "total_actual_cost": 0.000005})
		default:
			t.Fatalf("unexpected route %s %s", r.Method, r.URL.String())
		}
	}, time.Second)

	key, err := client.UserKey(context.Background(), SessionDelegatedCredential{Bearer: "delegated-user-token"}, 41, 17)
	if err != nil || key.ID != 17 || key.UserID != 41 {
		t.Fatalf("owned key = %#v err=%v", key, err)
	}
	stats, err := client.UsageStats(context.Background(), Sub2APIUsageStatsQuery{UserID: 41, Period: "month"})
	if err != nil || stats.TotalRequests != 2 || stats.TotalActualCostUSDMicros != 5 {
		t.Fatalf("account stats = %#v err=%v", stats, err)
	}
	if requests != 3 { // Account stats authenticates once with the admin credential.
		t.Fatalf("requests = %d, want key read + admin login + stats", requests)
	}
}

func rejectForbiddenSub2APIRoute(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
	t.Helper()
	for _, forbidden := range []string{"/balance", "/usage"} {
		if strings.Contains(r.URL.Path, forbidden) {
			t.Errorf("client called forbidden Sub2API route %s", r.URL.Path)
			http.Error(w, "forbidden fixture route", http.StatusTeapot)
			return true
		}
	}
	return false
}

func TestSub2APIClientLogsInRefreshesOnceAndParsesDecimalBalance(t *testing.T) {
	loginCalls, refreshCalls, userCalls := 0, 0, 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if rejectForbiddenSub2APIRoute(t, w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/v1/auth/login":
			loginCalls++
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access-one", "refresh_token": "refresh-one"})
		case "/api/v1/auth/refresh":
			refreshCalls++
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access-two", "refresh_token": "refresh-two"})
		case "/api/v1/admin/system/version":
			writeSub2APISuccess(t, w, map[string]any{"version": "0.1.151"})
		case "/api/v1/admin/users/41":
			userCalls++
			if r.Header.Get("Authorization") == "Bearer access-one" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Header.Get("Authorization") != "Bearer access-two" {
				t.Errorf("unexpected authorization header %q", r.Header.Get("Authorization"))
			}
			writeSub2APISuccess(t, w, json.RawMessage(`{"id":41,"balance":12.345678,"status":"active"}`))
		default:
			t.Errorf("unexpected Sub2API route %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}, time.Second)

	balance, err := client.Balance(context.Background(), 41)
	if err != nil {
		t.Fatalf("read balance: %v", err)
	}
	if balance.UserID != 41 || balance.USDMicros != 12_345_678 || balance.Status != "active" {
		t.Fatalf("balance = %#v", balance)
	}
	if loginCalls != 1 || refreshCalls != 1 || userCalls != 2 {
		t.Fatalf("calls login=%d refresh=%d user=%d", loginCalls, refreshCalls, userCalls)
	}
}

func TestSub2APIClientReloginsOnceWhenAccessOnlyTokenExpires(t *testing.T) {
	loginCalls, refreshCalls, userCalls := 0, 0, 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			loginCalls++
			writeSub2APISuccess(t, w, map[string]any{"access_token": fmt.Sprintf("access-%d", loginCalls)})
		case "/api/v1/auth/refresh":
			refreshCalls++
			w.WriteHeader(http.StatusInternalServerError)
		case "/api/v1/admin/users/41":
			userCalls++
			switch r.Header.Get("Authorization") {
			case "Bearer access-1":
				w.WriteHeader(http.StatusUnauthorized)
			case "Bearer access-2":
				writeSub2APISuccess(t, w, json.RawMessage(`{"id":41,"balance":12.345678,"status":"active"}`))
			default:
				t.Fatalf("unexpected authorization header %q", r.Header.Get("Authorization"))
			}
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}, time.Second)

	balance, err := client.Balance(context.Background(), 41)
	if err != nil || balance != (Sub2APIBalance{UserID: 41, USDMicros: 12_345_678, Status: "active"}) {
		t.Fatalf("balance=%#v err=%v", balance, err)
	}
	if loginCalls != 2 || refreshCalls != 0 || userCalls != 2 {
		t.Fatalf("calls login=%d refresh=%d user=%d", loginCalls, refreshCalls, userCalls)
	}
}

func TestSub2APIClientBalanceAcceptsDisabledAndRejectsUnknownStatus(t *testing.T) {
	status := "disabled"
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/users/41":
			writeSub2APISuccess(t, w, map[string]any{"id": 41, "balance": 0, "status": status})
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}, time.Second)

	balance, err := client.Balance(context.Background(), 41)
	if err != nil || balance.Status != "disabled" || balance.USDMicros != 0 {
		t.Fatalf("disabled zero balance = %#v, err=%v", balance, err)
	}
	status = "unknown"
	if _, err := client.Balance(context.Background(), 41); err == nil {
		t.Fatal("unknown user status was accepted")
	}
}

func TestSub2APIClientListsStrictMappedUserKeys(t *testing.T) {
	lastUsedAt := "2026-07-18T01:02:03Z"
	keyStatus := "active"
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/users/41/api-keys":
			writeSub2APISuccess(t, w, map[string]any{
				"items": []any{
					map[string]any{"id": 8, "user_id": 41, "name": "retired", "key": "", "status": "disabled", "quota": 0, "quota_used": 0, "usage_5h": 0, "usage_1d": 0, "usage_7d": 0},
					map[string]any{"id": 9, "user_id": 41, "name": "opl-workspace", "key": "workspace-secret", "status": keyStatus, "quota": 10.000001, "quota_used": 2.000002, "usage_5h": 1, "usage_1d": 2, "usage_7d": 3, "last_used_at": lastUsedAt},
				},
				"total": 2, "page": 1, "page_size": 1000, "pages": 1,
			})
		default:
			t.Fatalf("unexpected Sub2API route %s %s", r.Method, r.URL.Path)
		}
	}, time.Second)

	keyClient, ok := any(client).(interface {
		Keys(context.Context, int64) ([]Sub2APIWorkspaceKey, error)
	})
	if !ok {
		t.Fatal("Sub2API client does not expose strict key listing")
	}
	keys, err := keyClient.Keys(context.Background(), 41)
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	if len(keys) != 2 || keys[0].ID != 8 || keys[0].Status != "disabled" || keys[1].ID != 9 || keys[1].UserID != 41 {
		t.Fatalf("keys = %#v", keys)
	}
	if keys[1].QuotaUSDMicros != 10_000_001 || keys[1].QuotaUsedUSDMicros != 2_000_002 || keys[1].LastUsedAt == nil || keys[1].LastUsedAt.Format(time.RFC3339) != lastUsedAt {
		t.Fatalf("strict key fields = %#v", keys[1])
	}
	keyStatus = "unknown"
	if _, err := keyClient.Keys(context.Background(), 41); err == nil {
		t.Fatal("unknown key status was accepted")
	}
}

func TestSub2APIClientWorkspaceKeyRequiresSelectedSecret(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/users/41/api-keys":
			writeSub2APISuccess(t, w, map[string]any{"items": []any{map[string]any{
				"id": 9, "user_id": 41, "name": "opl-workspace", "key": "", "status": "active",
				"quota": 0, "quota_used": 0, "usage_5h": 0, "usage_1d": 0, "usage_7d": 0,
			}}, "total": 1, "page": 1, "page_size": 1000, "pages": 1})
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}, time.Second)
	if _, err := client.WorkspaceKey(context.Background(), 41); err == nil {
		t.Fatal("active Workspace key without secret was accepted")
	}
}

func TestSub2APIClientSelectsOneActiveWorkspaceKeyAcrossPages(t *testing.T) {
	pages := []string{}
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/system/version":
			writeSub2APISuccess(t, w, map[string]any{"version": "0.1.155"})
		case "/api/v1/admin/users/41/api-keys":
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer access" || r.URL.Query().Get("page_size") != "1000" {
				t.Fatalf("unexpected key request: %s %s auth=%q", r.Method, r.URL.String(), r.Header.Get("Authorization"))
			}
			page := r.URL.Query().Get("page")
			pages = append(pages, page)
			if page == "1" {
				items := make([]any, 0, 1000)
				for id := int64(1); id <= 1000; id++ {
					items = append(items, map[string]any{"id": id, "user_id": 41, "name": "other", "key": "other-key", "status": "active", "quota": 0, "quota_used": 0, "usage_5h": 0, "usage_1d": 0, "usage_7d": 0})
				}
				writeSub2APISuccess(t, w, map[string]any{
					"items": items, "total": 1001, "page": 1, "page_size": 1000, "pages": 2,
				})
				return
			}
			writeSub2APISuccess(t, w, map[string]any{
				"items": []any{map[string]any{
					"id": 1001, "user_id": 41, "name": "opl-workspace", "key": "workspace-key-secret", "status": "active",
					"quota": 100.000001, "quota_used": 12.345678, "usage_5h": 1.000001, "usage_1d": 2.000002, "usage_7d": 3.000003,
					"last_used_at": "2026-07-16T01:02:03Z",
				}},
				"total": 1001, "page": 2, "page_size": 1000, "pages": 2,
			})
		default:
			t.Fatalf("unexpected Sub2API route %s %s", r.Method, r.URL.Path)
		}
	}, time.Second)

	key, err := client.WorkspaceKey(context.Background(), 41)
	if err != nil {
		t.Fatalf("workspace key: %v", err)
	}
	if key.ID != 1001 || key.UserID != 41 || key.Name != "opl-workspace" || key.Key != "workspace-key-secret" || key.Status != "active" {
		t.Fatalf("workspace key identity = %#v", key)
	}
	if key.QuotaUSDMicros != 100_000_001 || key.QuotaUsedUSDMicros != 12_345_678 || key.Usage5hUSDMicros != 1_000_001 || key.Usage1dUSDMicros != 2_000_002 || key.Usage7dUSDMicros != 3_000_003 {
		t.Fatalf("workspace key usage = %#v", key)
	}
	if key.LastUsedAt == nil || key.LastUsedAt.Format(time.RFC3339) != "2026-07-16T01:02:03Z" {
		t.Fatalf("workspace key last used = %#v", key.LastUsedAt)
	}
	if !slices.Equal(pages, []string{"1", "2"}) {
		t.Fatalf("key pages = %#v", pages)
	}
}

func TestSub2APIClientWorkspaceKeyRejectsIncoherentFullPagination(t *testing.T) {
	items := func(start, count int64) []any {
		result := make([]any, 0, count)
		for id := start; id < start+count; id++ {
			result = append(result, map[string]any{"id": id, "user_id": 41, "name": "other", "key": "other-key", "status": "active", "quota": 0, "quota_used": 0, "usage_5h": 0, "usage_1d": 0, "usage_7d": 0})
		}
		return result
	}
	active := func(id int64) map[string]any {
		return map[string]any{
			"id": id, "user_id": 41, "name": "opl-workspace", "key": "workspace-key-secret", "status": "active",
			"quota": 0, "quota_used": 0, "usage_5h": 0, "usage_1d": 0, "usage_7d": 0,
		}
	}
	page := func(items []any, total, number, pageSize, pages int) map[string]any {
		return map[string]any{"items": items, "total": total, "page": number, "page_size": pageSize, "pages": pages}
	}

	fullFirstPage := items(1, 1000)
	tooManyItems := append(items(1, 1000), active(1001))
	for _, tc := range []struct {
		name  string
		pages []map[string]any
	}{
		{name: "first page size differs from request", pages: []map[string]any{page([]any{active(1)}, 1, 1, 999, 1)}},
		{name: "pages differ from total ceiling", pages: []map[string]any{page([]any{active(1)}, 1, 1, 1000, 2), page(nil, 1, 2, 1000, 2)}},
		{name: "total drifts between pages", pages: []map[string]any{page(fullFirstPage, 1001, 1, 1000, 2), page([]any{active(1001)}, 1002, 2, 1000, 2)}},
		{name: "page size drifts between pages", pages: []map[string]any{page(fullFirstPage, 1001, 1, 1000, 2), page([]any{active(1001)}, 1001, 2, 999, 2)}},
		{name: "items exceed page size", pages: []map[string]any{page(tooManyItems, 1001, 1, 1000, 2), page(nil, 1001, 2, 1000, 2)}},
		{name: "empty page before total", pages: []map[string]any{page(nil, 1001, 1, 1000, 2), page([]any{active(1001)}, 1001, 2, 1000, 2)}},
		{name: "duplicate key id", pages: []map[string]any{page(fullFirstPage, 1001, 1, 1000, 2), page([]any{active(1)}, 1001, 2, 1000, 2)}},
		{name: "non-positive key id", pages: []map[string]any{page([]any{map[string]any{"id": 0, "user_id": 41, "name": "other", "key": "other-key", "status": "active"}}, 1, 1, 1000, 1)}},
		{name: "collected count differs from total", pages: []map[string]any{page([]any{active(1)}, 2, 1, 1000, 1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/login":
					writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
				case "/api/v1/admin/users/41/api-keys":
					requested, err := strconv.Atoi(r.URL.Query().Get("page"))
					if err != nil || requested < 1 || requested > len(tc.pages) {
						t.Fatalf("unexpected key page %q", r.URL.Query().Get("page"))
					}
					writeSub2APISuccess(t, w, tc.pages[requested-1])
				default:
					t.Fatalf("unexpected route %s", r.URL.Path)
				}
			}, time.Second)
			if _, err := client.WorkspaceKey(context.Background(), 41); err == nil || !strings.Contains(err.Error(), "pagination") {
				t.Fatalf("incoherent pagination error = %v", err)
			}
		})
	}
}

func TestSub2APIClientWorkspaceKeyRequiresUsageFieldsAndAcceptsZero(t *testing.T) {
	base := map[string]any{
		"id": int64(9), "user_id": int64(41), "name": "opl-workspace", "key": "workspace-key-secret", "status": "active",
		"quota": 0, "quota_used": 0, "usage_5h": 0, "usage_1d": 0, "usage_7d": 0,
	}
	newClient := func(t *testing.T, item map[string]any) *Sub2APIHTTPClient {
		t.Helper()
		return newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/auth/login":
				writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
			case "/api/v1/admin/system/version":
				writeSub2APISuccess(t, w, map[string]any{"version": "0.1.155"})
			case "/api/v1/admin/users/41/api-keys":
				writeSub2APISuccess(t, w, map[string]any{"items": []any{item}, "total": 1, "page": 1, "page_size": 1000, "pages": 1})
			default:
				t.Fatalf("unexpected route %s", r.URL.Path)
			}
		}, time.Second)
	}

	for _, field := range []string{"quota", "quota_used", "usage_5h", "usage_1d", "usage_7d"} {
		for _, mode := range []string{"missing", "null"} {
			t.Run(field+" "+mode, func(t *testing.T) {
				item := maps.Clone(base)
				if mode == "missing" {
					delete(item, field)
				} else {
					item[field] = nil
				}
				if _, err := newClient(t, item).WorkspaceKey(context.Background(), 41); err == nil || !strings.Contains(err.Error(), "invalid sub2api workspace key usage") {
					t.Fatalf("%s %s error = %v", field, mode, err)
				}
			})
		}
	}

	key, err := newClient(t, maps.Clone(base)).WorkspaceKey(context.Background(), 41)
	if err != nil || key.QuotaUSDMicros != 0 || key.QuotaUsedUSDMicros != 0 || key.Usage5hUSDMicros != 0 || key.Usage1dUSDMicros != 0 || key.Usage7dUSDMicros != 0 {
		t.Fatalf("zero usage key=%#v err=%v", key, err)
	}
}

func TestSub2APIClientWorkspaceKeyCardinalityFailsClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		items []map[string]any
		want  error
	}{
		"empty":   {want: ErrSub2APIWorkspaceKeyMissing},
		"missing": {items: []map[string]any{{"id": 1, "user_id": 41, "name": "other", "key": "other-key", "status": "active", "quota": 0, "quota_used": 0, "usage_5h": 0, "usage_1d": 0, "usage_7d": 0}}, want: ErrSub2APIWorkspaceKeyMissing},
		"ambiguous": {items: []map[string]any{
			{"id": 1, "user_id": 41, "name": "opl-workspace", "key": "workspace-key-one", "status": "active", "quota": 0, "quota_used": 0, "usage_5h": 0, "usage_1d": 0, "usage_7d": 0},
			{"id": 2, "user_id": 41, "name": "opl-workspace", "key": "workspace-key-two", "status": "active", "quota": 0, "quota_used": 0, "usage_5h": 0, "usage_1d": 0, "usage_7d": 0},
		}, want: ErrSub2APIWorkspaceKeyAmbiguous},
	} {
		t.Run(name, func(t *testing.T) {
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/login":
					writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
				case "/api/v1/admin/system/version":
					writeSub2APISuccess(t, w, map[string]any{"version": "0.1.155"})
				case "/api/v1/admin/users/41/api-keys":
					pages := 1
					if len(tc.items) == 0 {
						pages = 0
					}
					writeSub2APISuccess(t, w, map[string]any{"items": tc.items, "total": len(tc.items), "page": 1, "page_size": 1000, "pages": pages})
				default:
					t.Fatalf("unexpected route %s", r.URL.Path)
				}
			}, time.Second)
			if _, err := client.WorkspaceKey(context.Background(), 41); !errors.Is(err, tc.want) {
				t.Fatalf("workspace key error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestSub2APIClientWorkspaceKeyRejectsExcessivePaginationWithoutContinuing(t *testing.T) {
	for name, pagination := range map[string]map[string]any{
		"pages": {"total": 1, "page": 1, "page_size": 1000, "pages": 11},
		"total": {"total": 10_001, "page": 1, "page_size": 1000, "pages": 1},
	} {
		t.Run(name, func(t *testing.T) {
			keyRequests := 0
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/login":
					writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
				case "/api/v1/admin/system/version":
					writeSub2APISuccess(t, w, map[string]any{"version": "0.1.155"})
				case "/api/v1/admin/users/41/api-keys":
					keyRequests++
					pagination["items"] = []any{}
					writeSub2APISuccess(t, w, pagination)
				default:
					t.Fatalf("unexpected route %s", r.URL.Path)
				}
			}, time.Second)
			if _, err := client.WorkspaceKey(context.Background(), 41); err == nil || !strings.Contains(err.Error(), "pagination") {
				t.Fatalf("workspace key pagination error = %v", err)
			}
			if keyRequests != 1 {
				t.Fatalf("key requests = %d, want 1", keyRequests)
			}
		})
	}
}

func TestSub2APIClientWorkspaceKeyRejectsCrossUserAndDoesNotLeakKey(t *testing.T) {
	const secret = "workspace-key-secret"
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/system/version":
			writeSub2APISuccess(t, w, map[string]any{"version": "0.1.155"})
		case "/api/v1/admin/users/41/api-keys":
			writeSub2APISuccess(t, w, map[string]any{"items": []map[string]any{{"id": 1, "user_id": 42, "name": "opl-workspace", "key": secret, "status": "active"}}, "total": 1, "page": 1, "page_size": 1000, "pages": 1})
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}, time.Second)
	if _, err := client.WorkspaceKey(context.Background(), 41); err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("cross-user workspace key error = %v", err)
	}
}

func TestSub2APIClientWorkspaceKeyBoundsAndRedactsUpstreamResponses(t *testing.T) {
	const secret = "workspace-key-secret"
	for name, handler := range map[string]http.HandlerFunc{
		"too large": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, `{"code":0,"data":{"items":[],"padding":"%s"}}`, strings.Repeat("x", maxSub2APIResponseBytes))
		},
		"upstream error": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, secret, http.StatusInternalServerError)
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/auth/login" {
					writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
					return
				}
				if r.URL.Path == "/api/v1/admin/system/version" {
					writeSub2APISuccess(t, w, map[string]any{"version": "0.1.155"})
					return
				}
				handler(w, r)
			}, time.Second)
			_, err := client.WorkspaceKey(context.Background(), 41)
			if err == nil || strings.Contains(err.Error(), secret) {
				t.Fatalf("workspace key error = %v", err)
			}
			if name == "too large" && !errors.Is(err, ErrSub2APIResponseTooLarge) {
				t.Fatalf("workspace key error = %v, want response too large", err)
			}
		})
	}
}

func TestSub2APIAdjustmentExactAmount(t *testing.T) {
	chargeCalls := 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if rejectForbiddenSub2APIRoute(t, w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/system/version":
			writeSub2APISuccess(t, w, map[string]any{"version": "0.1.151"})
		case "/api/v1/admin/redeem-codes/create-and-redeem":
			chargeCalls++
			if r.Header.Get("Idempotency-Key") != "opl:production:op-41:charge:v1" {
				t.Errorf("idempotency key = %q", r.Header.Get("Idempotency-Key"))
			}
			var body map[string]any
			decoder := json.NewDecoder(r.Body)
			decoder.UseNumber()
			if err := decoder.Decode(&body); err != nil {
				t.Errorf("decode charge request: %v", err)
			}
			if body["code"] != "opl:production:op-41:charge:v1" || body["type"] != "balance" || body["user_id"] != json.Number("41") || body["value"] != json.Number("-50.000000") {
				t.Errorf("charge request = %#v", body)
			}
			writeSub2APISuccess(t, w, json.RawMessage(`{"redeem_code":{"code":"opl:production:op-41:charge:v1","type":"balance","value":-50.000000,"status":"used","used_by":41}}`))
		default:
			t.Errorf("unexpected Sub2API route %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}, time.Second)

	input := Sub2APIChargeInput{UserID: 41, Code: "opl:production:op-41:charge:v1", ChargeUSDMicros: 50_000_000}
	for i := 0; i < 2; i++ {
		charge, err := client.Charge(context.Background(), input)
		if err != nil {
			t.Fatalf("charge attempt %d: %v", i+1, err)
		}
		if charge.Code != input.Code || charge.UserID != 41 || charge.ChargeUSDMicros != 50_000_000 {
			t.Fatalf("charge = %#v", charge)
		}
	}
	if chargeCalls != 2 {
		t.Fatalf("charge calls = %d, want 2", chargeCalls)
	}
}

func TestSub2APIClientRefundsWithExactPositiveMicrosAndReplays(t *testing.T) {
	refundCalls := 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/system/version":
			writeSub2APISuccess(t, w, map[string]any{"version": "0.1.155"})
		case "/api/v1/admin/redeem-codes/create-and-redeem":
			refundCalls++
			if r.Header.Get("Idempotency-Key") != "opl:production:op-41:refund:v1" {
				t.Errorf("idempotency key = %q", r.Header.Get("Idempotency-Key"))
			}
			var body map[string]any
			decoder := json.NewDecoder(r.Body)
			decoder.UseNumber()
			if err := decoder.Decode(&body); err != nil {
				t.Errorf("decode refund request: %v", err)
			}
			if body["code"] != "opl:production:op-41:refund:v1" || body["type"] != "balance" || body["user_id"] != json.Number("41") || body["value"] != json.Number("50.000000") {
				t.Errorf("refund request = %#v", body)
			}
			writeSub2APISuccess(t, w, json.RawMessage(`{"redeem_code":{"code":"opl:production:op-41:refund:v1","type":"balance","value":50.000000,"status":"used","used_by":41}}`))
		default:
			t.Errorf("unexpected Sub2API route %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}, time.Second)

	input := Sub2APIRefundInput{UserID: 41, Code: "opl:production:op-41:refund:v1", RefundUSDMicros: 50_000_000}
	for i := 0; i < 2; i++ {
		refund, err := client.Refund(context.Background(), input)
		if err != nil {
			t.Fatalf("refund attempt %d: %v", i+1, err)
		}
		if refund.Code != input.Code || refund.UserID != 41 || refund.RefundUSDMicros != 50_000_000 {
			t.Fatalf("refund = %#v", refund)
		}
	}
	if refundCalls != 2 {
		t.Fatalf("refund calls = %d, want 2", refundCalls)
	}
}

func TestSub2APIClientVersionIsDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name          string
		version       string
		versionStatus int
	}{
		{name: "deployed version", version: "0.1.153"},
		{name: "future version", version: "99.0.0"},
		{name: "diagnostic unavailable", versionStatus: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			versionCalls := 0
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/login":
					writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
				case "/api/v1/admin/system/version":
					versionCalls++
					if tc.versionStatus != 0 {
						http.Error(w, "unavailable", tc.versionStatus)
						return
					}
					writeSub2APISuccess(t, w, map[string]any{"version": tc.version})
				case "/api/v1/admin/users/41":
					writeSub2APISuccess(t, w, map[string]any{"id": 41, "balance": 12.345678, "status": "active"})
				default:
					http.NotFound(w, r)
				}
			}, time.Second)

			version, versionErr := client.Version(context.Background())
			if tc.versionStatus == 0 && (versionErr != nil || version != tc.version) {
				t.Fatalf("version = %q, err = %v", version, versionErr)
			}
			if tc.versionStatus != 0 && versionErr == nil {
				t.Fatal("version diagnostic should report its own failure")
			}

			balance, err := client.Balance(context.Background(), 41)
			if err != nil || balance.UserID != 41 || balance.USDMicros != 12_345_678 {
				t.Fatalf("balance = %#v, err = %v", balance, err)
			}
			if versionCalls != 1 {
				t.Fatalf("version calls = %d, want only the explicit diagnostic call", versionCalls)
			}
		})
	}
}

func TestSub2APIClientCapabilitiesDoNotRequestVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Sub2APIHTTPClient) error
	}{
		{name: "balance", call: func(client *Sub2APIHTTPClient) error {
			balance, err := client.Balance(context.Background(), 41)
			if err == nil && (balance.UserID != 41 || balance.USDMicros != 12_345_678) {
				return fmt.Errorf("unexpected balance: %#v", balance)
			}
			return err
		}},
		{name: "workspace key", call: func(client *Sub2APIHTTPClient) error {
			key, err := client.WorkspaceKey(context.Background(), 41)
			if err == nil && (key.ID != 9 || key.UserID != 41 || key.Key != "workspace-key-secret") {
				return fmt.Errorf("unexpected workspace key: %#v", key)
			}
			return err
		}},
		{name: "charge", call: func(client *Sub2APIHTTPClient) error {
			charge, err := client.Charge(context.Background(), Sub2APIChargeInput{UserID: 41, Code: "opl:capability:charge", ChargeUSDMicros: 1})
			if err == nil && (charge.Code != "opl:capability:charge" || charge.Status != "used") {
				return fmt.Errorf("unexpected charge: %#v", charge)
			}
			return err
		}},
		{name: "refund", call: func(client *Sub2APIHTTPClient) error {
			refund, err := client.Refund(context.Background(), Sub2APIRefundInput{UserID: 41, Code: "opl:capability:refund", RefundUSDMicros: 1})
			if err == nil && (refund.Code != "opl:capability:refund" || refund.Status != "used") {
				return fmt.Errorf("unexpected refund: %#v", refund)
			}
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			versionCalls := 0
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/login":
					writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
				case "/api/v1/admin/system/version":
					versionCalls++
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
				case "/api/v1/admin/users/41":
					writeSub2APISuccess(t, w, map[string]any{"id": 41, "balance": 12.345678, "status": "active"})
				case "/api/v1/admin/users/41/api-keys":
					writeSub2APISuccess(t, w, map[string]any{
						"items": []any{map[string]any{
							"id": 9, "user_id": 41, "name": "opl-workspace", "key": "workspace-key-secret", "status": "active",
							"quota": 0, "quota_used": 0, "usage_5h": 0, "usage_1d": 0, "usage_7d": 0,
						}},
						"total": 1, "page": 1, "page_size": 1000, "pages": 1,
					})
				case "/api/v1/admin/redeem-codes/create-and-redeem":
					var input struct {
						Code   string      `json:"code"`
						Type   string      `json:"type"`
						Value  json.Number `json:"value"`
						UserID int64       `json:"user_id"`
					}
					decoder := json.NewDecoder(r.Body)
					decoder.UseNumber()
					if err := decoder.Decode(&input); err != nil {
						t.Fatalf("decode balance adjustment: %v", err)
					}
					writeSub2APISuccess(t, w, map[string]any{"redeem_code": map[string]any{
						"code": input.Code, "type": input.Type, "value": input.Value, "status": "used", "used_by": input.UserID,
					}})
				default:
					http.NotFound(w, r)
				}
			}, time.Second)

			if err := tc.call(client); err != nil {
				t.Fatalf("capability call: %v", err)
			}
			if versionCalls != 0 {
				t.Fatalf("version calls = %d, want 0", versionCalls)
			}
		})
	}
}

func TestSub2APIClientDetectsSameCodeDifferentValue(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/system/version":
			writeSub2APISuccess(t, w, map[string]any{"version": "0.1.151"})
		case "/api/v1/admin/redeem-codes/create-and-redeem":
			writeSub2APISuccess(t, w, json.RawMessage(`{"redeem_code":{"code":"opl:replay","type":"balance","value":-50.000000,"status":"used","used_by":41}}`))
		default:
			http.NotFound(w, r)
		}
	}, time.Second)

	_, err := client.Charge(context.Background(), Sub2APIChargeInput{UserID: 41, Code: "opl:replay", ChargeUSDMicros: 40_000_000})
	if !errors.Is(err, ErrSub2APIChargeConflict) {
		t.Fatalf("same code with different value error = %v", err)
	}
}

func TestSub2APIAdjustmentReplay(t *testing.T) {
	historyEntry := func(code string, valueUSDMicros int64) map[string]any {
		return map[string]any{
			"code": code, "type": "balance", "value": usdMicrosJSON(valueUSDMicros), "status": "used", "used_by": 41,
			"used_at": "2026-07-16T00:01:00Z", "created_at": "2026-07-16T00:00:00Z",
		}
	}
	for _, adjustment := range []struct {
		name   string
		code   string
		signed int64
		call   func(*Sub2APIHTTPClient) (string, error)
	}{
		{name: "charge", code: "opl:replay:charge", signed: -50_000_000, call: func(client *Sub2APIHTTPClient) (string, error) {
			result, err := client.Charge(context.Background(), Sub2APIChargeInput{UserID: 41, Code: "opl:replay:charge", ChargeUSDMicros: 50_000_000})
			return result.Status, err
		}},
		{name: "refund", code: "opl:replay:refund", signed: 50_000_000, call: func(client *Sub2APIHTTPClient) (string, error) {
			result, err := client.Refund(context.Background(), Sub2APIRefundInput{UserID: 41, Code: "opl:replay:refund", RefundUSDMicros: 50_000_000})
			return result.Status, err
		}},
	} {
		for _, scenario := range []struct {
			name          string
			items         func() []any
			total         int
			historyStatus int
			wantErr       error
		}{
			{name: "exact", items: func() []any { return []any{historyEntry(adjustment.code, adjustment.signed)} }, total: 1},
			{name: "different amount", items: func() []any { return []any{historyEntry(adjustment.code, adjustment.signed+1)} }, total: 1, wantErr: ErrSub2APIChargeConflict},
			{name: "missing", items: func() []any { return []any{historyEntry("opl:other", adjustment.signed)} }, total: 1, wantErr: ErrSub2APIChargeUnknown},
			{name: "duplicate", items: func() []any {
				return []any{historyEntry(adjustment.code, adjustment.signed), historyEntry(adjustment.code, adjustment.signed)}
			}, total: 2, wantErr: ErrSub2APIChargeConflict},
			{name: "history unavailable", historyStatus: http.StatusServiceUnavailable, wantErr: ErrSub2APIChargeUnknown},
			{name: "invalid history pagination", items: func() []any { return []any{historyEntry(adjustment.code, adjustment.signed)} }, total: 2, wantErr: ErrSub2APIChargeUnknown},
		} {
			t.Run(adjustment.name+" "+scenario.name, func(t *testing.T) {
				postCalls, historyCalls := 0, 0
				client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/v1/auth/login":
						writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
					case "/api/v1/admin/redeem-codes/create-and-redeem":
						postCalls++
						http.Error(w, "conflict", http.StatusConflict)
					case "/api/v1/admin/users/41/balance-history":
						historyCalls++
						if r.URL.Query().Get("type") != "balance" || r.URL.Query().Get("page") != "1" || r.URL.Query().Get("page_size") != "1000" {
							t.Fatalf("history query = %s", r.URL.RawQuery)
						}
						if scenario.historyStatus != 0 {
							http.Error(w, "unavailable", scenario.historyStatus)
							return
						}
						writeSub2APISuccess(t, w, map[string]any{"items": scenario.items(), "total": scenario.total, "page": 1, "page_size": 1000, "pages": 1})
					default:
						t.Fatalf("unexpected route %s", r.URL.Path)
					}
				}, time.Second)

				status, err := adjustment.call(client)
				if scenario.wantErr == nil {
					if err != nil || status != "used" {
						t.Fatalf("confirmed replay status=%q err=%v", status, err)
					}
				} else if !errors.Is(err, scenario.wantErr) {
					t.Fatalf("replay error=%v, want %v", err, scenario.wantErr)
				}
				if postCalls != 1 || historyCalls != 1 {
					t.Fatalf("replay calls post=%d history=%d", postCalls, historyCalls)
				}
			})
		}
	}
}

func TestSub2APIAdjustmentUnknown(t *testing.T) {
	t.Run("response body limit", func(t *testing.T) {
		client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/auth/login":
				writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
			case "/api/v1/admin/system/version":
				writeSub2APISuccess(t, w, map[string]any{"version": "0.1.151"})
			case "/api/v1/admin/users/41":
				_, _ = fmt.Fprintf(w, `{"code":0,"message":"success","data":{"id":41,"balance":1,"padding":"%s"}}`, strings.Repeat("x", maxSub2APIResponseBytes))
			default:
				http.NotFound(w, r)
			}
		}, time.Second)
		if _, err := client.Balance(context.Background(), 41); !errors.Is(err, ErrSub2APIResponseTooLarge) {
			t.Fatalf("oversized response error = %v", err)
		}
	})

	t.Run("charge timeout", func(t *testing.T) {
		client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/auth/login":
				writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
			case "/api/v1/admin/system/version":
				writeSub2APISuccess(t, w, map[string]any{"version": "0.1.151"})
			case "/api/v1/admin/redeem-codes/create-and-redeem":
				time.Sleep(100 * time.Millisecond)
				writeSub2APISuccess(t, w, map[string]any{})
			default:
				http.NotFound(w, r)
			}
		}, 20*time.Millisecond)

		_, err := client.Charge(context.Background(), Sub2APIChargeInput{UserID: 41, Code: "opl:timeout", ChargeUSDMicros: 1_000_000})
		if !errors.Is(err, ErrSub2APIChargeUnknown) {
			t.Fatalf("timeout error = %v", err)
		}
	})
}

func TestSub2APIClientErrorsDoNotLeakSecrets(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"admin-secret access-token response-secret admin@example.test"}`))
	}, time.Second)

	_, err := client.Balance(context.Background(), 41)
	if err == nil {
		t.Fatal("login failure should return an error")
	}
	for _, secret := range []string{"admin-secret", "access-token", "response-secret", "admin@example.test"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
}

func TestSub2APIUsageListIsScopedAndDropsAdminFields(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/usage":
			query := r.URL.Query()
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer access" || query.Get("user_id") != "41" || query.Get("api_key_id") != "9" || query.Get("page") != "1" || query.Get("page_size") != "50" || query.Get("sort_by") != "created_at" || query.Get("sort_order") != "desc" {
				t.Fatalf("usage request = %s %s auth=%q", r.Method, r.URL.String(), r.Header.Get("Authorization"))
			}
			writeSub2APISuccess(t, w, json.RawMessage(`{"items":[{"user_id":41,"api_key_id":9,"request_id":"req-1","created_at":"2026-07-16T00:00:00Z","model":"gpt-5","inbound_endpoint":"/v1/responses","request_type":"sync","input_tokens":10,"output_tokens":20,"cache_creation_tokens":0,"cache_read_tokens":5,"actual_cost":0.001234,"user":{"email":"private@example.test"},"api_key":{"key":"key-secret"},"ip_address":"198.51.100.1","user_agent":"secret-agent","prompt":"prompt-secret","response":"response-secret"}],"total":1,"page":1,"page_size":50,"pages":1}`))
		default:
			t.Fatalf("unexpected Sub2API route %s %s", r.Method, r.URL.Path)
		}
	}, time.Second)

	page, err := client.Usage(context.Background(), Sub2APIUsageQuery{UserID: 41, APIKeyID: 9, Page: 1, PageSize: 50})
	if err != nil || len(page.Items) != 1 || page.Total != 1 || page.Page != 1 || page.PageSize != 50 || page.Pages != 1 {
		t.Fatalf("usage page = %#v err=%v", page, err)
	}
	row := page.Items[0]
	if row.UserID != 41 || row.APIKeyID != 9 || row.RequestID != "req-1" || row.Model != "gpt-5" || row.InboundEndpoint != "/v1/responses" || row.RequestType != "sync" || row.InputTokens != 10 || row.OutputTokens != 20 || row.CacheCreationTokens != 0 || row.CacheReadTokens != 5 || row.ActualCostUSDMicros != 1234 || row.CreatedAt.Format(time.RFC3339) != "2026-07-16T00:00:00Z" {
		t.Fatalf("usage row = %#v", row)
	}
	encoded, _ := json.Marshal(row)
	for _, forbidden := range []string{"private@example.test", "key-secret", "198.51.100.1", "secret-agent", "prompt-secret", "response-secret"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("usage row leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestSub2APIUsageListRejectsCrossIdentity(t *testing.T) {
	for name, identity := range map[string]string{
		"user": `"user_id":42,"api_key_id":9`,
		"key":  `"user_id":41,"api_key_id":10`,
	} {
		t.Run(name, func(t *testing.T) {
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/login":
					writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
				case "/api/v1/admin/usage":
					writeSub2APISuccess(t, w, json.RawMessage(`{"items":[{`+identity+`,"request_id":"req-1","created_at":"2026-07-16T00:00:00Z","model":"gpt-5","inbound_endpoint":"/v1/responses","request_type":"sync","input_tokens":1,"output_tokens":2,"cache_creation_tokens":0,"cache_read_tokens":0,"actual_cost":0.000001}],"total":1,"page":1,"page_size":50,"pages":1}`))
				default:
					t.Fatalf("unexpected route %s", r.URL.Path)
				}
			}, time.Second)
			if _, err := client.Usage(context.Background(), Sub2APIUsageQuery{UserID: 41, APIKeyID: 9, Page: 1, PageSize: 50}); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
				t.Fatalf("cross-%s usage error = %v", name, err)
			}
		})
	}
}

func TestSub2APIUsageListRequiresCoherentPagination(t *testing.T) {
	rows := func(count int) []map[string]any {
		items := make([]map[string]any, count)
		for index := range items {
			items[index] = map[string]any{
				"user_id": 41, "api_key_id": 9, "request_id": fmt.Sprintf("req-%d", index), "created_at": "2026-07-16T00:00:00Z",
				"model": "gpt-5", "inbound_endpoint": "/v1/responses", "request_type": "sync",
				"input_tokens": 0, "output_tokens": 0, "cache_creation_tokens": 0, "cache_read_tokens": 0, "actual_cost": 0,
			}
		}
		return items
	}
	for _, tc := range []struct {
		name               string
		page, total, pages int
		items              []map[string]any
		wantErr            bool
	}{
		{name: "reported total without items", page: 1, total: 1, pages: 1, items: rows(0), wantErr: true},
		{name: "wrong total pages", page: 1, total: 51, pages: 1, items: rows(50), wantErr: true},
		{name: "short non-final page", page: 1, total: 51, pages: 2, items: rows(1), wantErr: true},
		{name: "short final page", page: 2, total: 51, pages: 2, items: rows(0), wantErr: true},
		{name: "empty", page: 1, total: 0, pages: 0, items: rows(0)},
		{name: "full non-final page", page: 1, total: 51, pages: 2, items: rows(50)},
		{name: "final remainder", page: 2, total: 51, pages: 2, items: rows(1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/login":
					writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
				case "/api/v1/admin/usage":
					writeSub2APISuccess(t, w, map[string]any{"items": tc.items, "total": tc.total, "page": tc.page, "page_size": 50, "pages": tc.pages})
				default:
					t.Fatalf("unexpected route %s", r.URL.Path)
				}
			}, time.Second)
			page, err := client.Usage(context.Background(), Sub2APIUsageQuery{UserID: 41, APIKeyID: 9, Page: tc.page, PageSize: 50})
			if tc.wantErr && (err == nil || !strings.Contains(err.Error(), "invalid sub2api usage pagination")) {
				t.Fatalf("pagination error = %v page=%#v", err, page)
			}
			if !tc.wantErr && (err != nil || len(page.Items) != len(tc.items) || page.Total != int64(tc.total) || page.Pages != tc.pages) {
				t.Fatalf("usage page = %#v err=%v", page, err)
			}
		})
	}
}

func TestSub2APIUsageStatsConvertsExactActualCost(t *testing.T) {
	for raw, want := range map[string]int64{"0": 0, "0.000001": 1, "12.345678": 12_345_678} {
		t.Run(raw, func(t *testing.T) {
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/login":
					writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
				case "/api/v1/admin/usage/stats":
					query := r.URL.Query()
					if query.Get("user_id") != "41" || query.Get("api_key_id") != "9" || query.Get("period") != "month" {
						t.Fatalf("stats query = %s", r.URL.RawQuery)
					}
					writeSub2APISuccess(t, w, json.RawMessage(fmt.Sprintf(`{"total_requests":3,"total_input_tokens":10,"total_output_tokens":20,"total_tokens":35,"total_actual_cost":%s,"user":{"email":"private@example.test"},"endpoints":[{"endpoint":"private"}]}`, raw)))
				default:
					t.Fatalf("unexpected route %s", r.URL.Path)
				}
			}, time.Second)

			stats, err := client.UsageStats(context.Background(), Sub2APIUsageStatsQuery{UserID: 41, APIKeyID: 9, Period: "month"})
			if err != nil || stats.TotalRequests != 3 || stats.TotalInputTokens != 10 || stats.TotalOutputTokens != 20 || stats.TotalTokens != 35 || stats.TotalActualCostUSDMicros != want {
				t.Fatalf("usage stats = %#v err=%v", stats, err)
			}
			encoded, _ := json.Marshal(stats)
			if strings.Contains(string(encoded), "private@example.test") || strings.Contains(string(encoded), "endpoints") {
				t.Fatalf("stats leaked admin fields: %s", encoded)
			}
		})
	}
}

func TestSub2APIUsageStatsRejectsInvalidFacts(t *testing.T) {
	for name, data := range map[string]string{
		"negative tokens":   `{"total_requests":1,"total_input_tokens":-1,"total_output_tokens":0,"total_tokens":0,"total_actual_cost":0}`,
		"missing cost":      `{"total_requests":1,"total_input_tokens":1,"total_output_tokens":0,"total_tokens":1}`,
		"fractional micros": `{"total_requests":1,"total_input_tokens":1,"total_output_tokens":0,"total_tokens":1,"total_actual_cost":0.0000001}`,
		"overflow":          `{"total_requests":1,"total_input_tokens":1,"total_output_tokens":0,"total_tokens":1,"total_actual_cost":9223372036854.775808}`,
	} {
		t.Run(name, func(t *testing.T) {
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/auth/login" {
					writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
					return
				}
				if r.URL.Path != "/api/v1/admin/usage/stats" {
					t.Fatalf("unexpected route %s", r.URL.Path)
				}
				writeSub2APISuccess(t, w, json.RawMessage(data))
			}, time.Second)
			if _, err := client.UsageStats(context.Background(), Sub2APIUsageStatsQuery{UserID: 41, APIKeyID: 9, Period: "month"}); err == nil {
				t.Fatalf("invalid stats accepted: %s", data)
			}
		})
	}
}

func TestSub2APIBalanceHistoryIsBoundedAndDropsNotes(t *testing.T) {
	requestedPages := []string{}
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/users/41/balance-history":
			query := r.URL.Query()
			if query.Get("page_size") != "1000" || query.Get("type") != "balance" {
				t.Fatalf("history query = %s", r.URL.RawQuery)
			}
			requestedPages = append(requestedPages, query.Get("page"))
			if query.Get("page") == "1" {
				items := make([]any, 0, 1000)
				items = append(items, map[string]any{"code": "opl:charge", "type": "balance", "value": -50.000000, "status": "used", "used_by": 41, "used_at": "2026-07-16T00:01:00Z", "created_at": "2026-07-16T00:00:00Z", "notes": "balance-secret"})
				for i := 2; i <= 1000; i++ {
					items = append(items, map[string]any{"code": fmt.Sprintf("opl:filler:%d", i), "type": "balance", "value": -0.000001, "status": "used", "used_by": 41, "used_at": "2026-07-16T00:01:00Z", "created_at": "2026-07-16T00:00:00Z", "notes": "balance-secret"})
				}
				writeSub2APISuccess(t, w, map[string]any{"items": items, "total": 1001, "page": 1, "page_size": 1000, "pages": 2, "total_recharged": 50})
				return
			}
			writeSub2APISuccess(t, w, json.RawMessage(`{"items":[{"code":"opl:refund","type":"balance","value":50.000000,"status":"used","used_by":41,"used_at":"2026-07-16T00:03:00Z","created_at":"2026-07-16T00:02:00Z","notes":"balance-secret"}],"total":1001,"page":2,"page_size":1000,"pages":2,"total_recharged":50}`))
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}, time.Second)

	entries, err := client.BalanceHistory(context.Background(), 41)
	if err != nil || len(entries) != 1001 || !slices.Equal(requestedPages, []string{"1", "2"}) {
		t.Fatalf("balance history = %#v pages=%#v err=%v", entries, requestedPages, err)
	}
	if entries[0].Code != "opl:charge" || entries[0].ValueUSDMicros != -50_000_000 || entries[0].UsedBy == nil || *entries[0].UsedBy != 41 || entries[1000].Code != "opl:refund" || entries[1000].ValueUSDMicros != 50_000_000 {
		t.Fatalf("balance entries = %#v", entries)
	}
	encoded, _ := json.Marshal(entries)
	if strings.Contains(string(encoded), "balance-secret") || strings.Contains(string(encoded), "notes") {
		t.Fatalf("balance history leaked notes: %s", encoded)
	}
}

func TestSub2APIBalanceHistoryAcceptsEmptyCoherentListing(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
			return
		}
		writeSub2APISuccess(t, w, map[string]any{"items": []any{}, "total": 0, "page": 1, "page_size": 1000, "pages": 0})
	}, time.Second)
	entries, err := client.BalanceHistory(context.Background(), 41)
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty balance history=%#v err=%v", entries, err)
	}
}

func TestSub2APIBalanceHistoryRejectsIncoherentFullPagination(t *testing.T) {
	entry := func(code string) map[string]any {
		return map[string]any{
			"code": code, "type": "balance", "value": -0.000001, "status": "used", "used_by": 41,
			"used_at": "2026-07-16T00:01:00Z", "created_at": "2026-07-16T00:00:00Z",
		}
	}
	entries := func(start, count int) []any {
		result := make([]any, 0, count)
		for i := start; i < start+count; i++ {
			result = append(result, entry(fmt.Sprintf("opl:history:%d", i)))
		}
		return result
	}
	page := func(items []any, total, number, pageSize, pages int) map[string]any {
		return map[string]any{"items": items, "total": total, "page": number, "page_size": pageSize, "pages": pages}
	}
	fullFirstPage := entries(1, 1000)
	tooManyItems := append(entries(1, 1000), entry("opl:history:1001"))
	for _, tc := range []struct {
		name  string
		pages []map[string]any
	}{
		{name: "first page size differs from request", pages: []map[string]any{page([]any{entry("opl:history:1")}, 1, 1, 999, 1)}},
		{name: "pages differ from total ceiling", pages: []map[string]any{page([]any{entry("opl:history:1")}, 1, 1, 1000, 2), page(nil, 1, 2, 1000, 2)}},
		{name: "total drifts between pages", pages: []map[string]any{page(fullFirstPage, 1001, 1, 1000, 2), page([]any{entry("opl:history:1001")}, 1002, 2, 1000, 2)}},
		{name: "page size drifts between pages", pages: []map[string]any{page(fullFirstPage, 1001, 1, 1000, 2), page([]any{entry("opl:history:1001")}, 1001, 2, 999, 2)}},
		{name: "items exceed page size", pages: []map[string]any{page(tooManyItems, 1001, 1, 1000, 2), page(nil, 1001, 2, 1000, 2)}},
		{name: "empty page before total", pages: []map[string]any{page(nil, 1001, 1, 1000, 2), page([]any{entry("opl:history:1001")}, 1001, 2, 1000, 2)}},
		{name: "collected count differs from total", pages: []map[string]any{page([]any{entry("opl:history:1")}, 2, 1, 1000, 1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/auth/login" {
					writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
					return
				}
				requested, err := strconv.Atoi(r.URL.Query().Get("page"))
				if r.URL.Path != "/api/v1/admin/users/41/balance-history" || err != nil || requested < 1 || requested > len(tc.pages) {
					t.Fatalf("unexpected history request %s page=%q", r.URL.Path, r.URL.Query().Get("page"))
				}
				writeSub2APISuccess(t, w, tc.pages[requested-1])
			}, time.Second)
			if _, err := client.BalanceHistory(context.Background(), 41); err == nil || !strings.Contains(err.Error(), "pagination") {
				t.Fatalf("incoherent history pagination error = %v", err)
			}
		})
	}
}

func TestSub2APIBalanceHistoryRejectsUntrustedIdentityAndPagination(t *testing.T) {
	for name, data := range map[string]string{
		"used by another user": `{"items":[{"code":"opl:charge","type":"balance","value":-1,"status":"used","used_by":42,"used_at":"2026-07-16T00:01:00Z","created_at":"2026-07-16T00:00:00Z"}],"total":1,"page":1,"page_size":1000,"pages":1}`,
		"too many pages":       `{"items":[],"total":10001,"page":1,"page_size":1000,"pages":11}`,
	} {
		t.Run(name, func(t *testing.T) {
			requests := 0
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/auth/login" {
					writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
					return
				}
				requests++
				writeSub2APISuccess(t, w, json.RawMessage(data))
			}, time.Second)
			if _, err := client.BalanceHistory(context.Background(), 41); err == nil {
				t.Fatalf("untrusted history accepted: %s", data)
			}
			if requests != 1 {
				t.Fatalf("history requests = %d", requests)
			}
		})
	}
}
