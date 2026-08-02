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

func ptr[T any](value T) *T { return &value }

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

func TestSub2APIAdminUserUsesExactIDAndAuthoritativeFacts(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
		case "/api/v1/admin/users/42":
			if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer admin-access" {
				t.Fatalf("admin user request = %s %s auth=%q", r.Method, r.URL.String(), r.Header.Get("Authorization"))
			}
			writeSub2APISuccess(t, w, map[string]any{
				"id": 42, "email": " Pilot@Example.com ", "balance": 12.345678, "status": "active",
				"created_at": "2026-07-18T09:02:03+08:00", "updated_at": "2026-07-19T12:05:06+08:00",
			})
		default:
			t.Fatalf("unexpected route %s %s", r.Method, r.URL.String())
		}
	}, time.Second)

	user, err := client.AdminUser(context.Background(), 42)
	if err != nil {
		t.Fatalf("admin user: %v", err)
	}
	if user.ID != 42 || user.Email != "pilot@example.com" || user.Status != "active" || user.BalanceUSDMicros != 12_345_678 || user.BalanceUnavailable {
		t.Fatalf("admin user facts = %#v", user)
	}
	if user.CreatedAt.Location() != time.UTC || user.CreatedAt.Format(time.RFC3339Nano) != "2026-07-18T01:02:03Z" ||
		user.UpdatedAt.Location() != time.UTC || user.UpdatedAt.Format(time.RFC3339Nano) != "2026-07-19T04:05:06Z" {
		t.Fatalf("admin user authoritative times = %s / %s", user.CreatedAt.Format(time.RFC3339Nano), user.UpdatedAt.Format(time.RFC3339Nano))
	}
}

func TestSub2APIAdminUserRejectsInvalidIdentityFacts(t *testing.T) {
	for _, tc := range []struct {
		name string
		data map[string]any
	}{
		{name: "id mismatch", data: map[string]any{"id": 41, "email": "pilot@example.com", "balance": 1, "status": "active", "created_at": "2026-07-18T01:02:03Z", "updated_at": "2026-07-19T04:05:06Z"}},
		{name: "empty normalized email", data: map[string]any{"id": 42, "email": "  ", "balance": 1, "status": "active", "created_at": "2026-07-18T01:02:03Z", "updated_at": "2026-07-19T04:05:06Z"}},
		{name: "invalid status", data: map[string]any{"id": 42, "email": "pilot@example.com", "balance": 1, "status": "pending", "created_at": "2026-07-18T01:02:03Z", "updated_at": "2026-07-19T04:05:06Z"}},
		{name: "missing created at", data: map[string]any{"id": 42, "email": "pilot@example.com", "balance": 1, "status": "active", "updated_at": "2026-07-19T04:05:06Z"}},
		{name: "missing updated at", data: map[string]any{"id": 42, "email": "pilot@example.com", "balance": 1, "status": "active", "created_at": "2026-07-18T01:02:03Z"}},
		{name: "updated before created", data: map[string]any{"id": 42, "email": "pilot@example.com", "balance": 1, "status": "active", "created_at": "2026-07-19T04:05:06Z", "updated_at": "2026-07-18T01:02:03Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/login":
					writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
				case "/api/v1/admin/users/42":
					writeSub2APISuccess(t, w, tc.data)
				default:
					t.Fatalf("unexpected route %s", r.URL.Path)
				}
			}, time.Second)
			if _, err := client.AdminUser(context.Background(), 42); !errors.Is(err, ErrSub2APIIdentityConflict) {
				t.Fatalf("admin user error = %v", err)
			}
		})
	}
}

func TestSub2APIAdminUserKeepsIdentityWhenBalanceIsSubMicro(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
		case "/api/v1/admin/users/42":
			writeSub2APISuccess(t, w, map[string]any{
				"id": 42, "email": "pilot@example.com", "balance": json.RawMessage("0.00000001"), "status": "disabled",
				"created_at": "2026-07-18T01:02:03Z", "updated_at": "2026-07-19T04:05:06Z",
			})
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}, time.Second)

	user, err := client.AdminUser(context.Background(), 42)
	if err != nil || user.ID != 42 || user.Email != "pilot@example.com" || user.Status != "disabled" || user.BalanceUSDMicros != 0 || user.BalanceUnavailable {
		t.Fatalf("sub-micro admin user = %#v err=%v", user, err)
	}
}

func TestSub2APIAdminUsersFloorSubMicroBalance(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
		case "/api/v1/admin/users":
			writeSub2APISuccess(t, w, map[string]any{
				"items": []any{map[string]any{
					"id": 42, "email": "pilot@example.com", "balance": json.RawMessage("0.00000001"), "status": "active",
					"created_at": "2026-07-18T01:02:03Z", "updated_at": "2026-07-19T04:05:06Z",
				}},
				"total": 1, "page": 1, "page_size": 1, "pages": 1,
			})
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}, time.Second)

	page, err := client.AdminUsers(context.Background(), Sub2APIUserPageQuery{Page: 1, PageSize: 1, SortBy: "id", SortOrder: "asc"})
	if err != nil || len(page.Items) != 1 || page.Items[0].BalanceUSDMicros != 0 || page.Items[0].BalanceUnavailable {
		t.Fatalf("sub-micro admin users page = %#v err=%v", page, err)
	}
}

func TestSub2APIEmptyListingsRequireV0162Pagination(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		call func(*Sub2APIHTTPClient) error
	}{
		{
			name: "delegated user keys",
			path: "/api/v1/keys",
			call: func(client *Sub2APIHTTPClient) error {
				_, err := client.WorkspaceUserKeysForConvergence(context.Background(), SessionDelegatedCredential{Bearer: "delegated", ExpiresAt: time.Now().Add(time.Hour)}, 41, "opl-workspace-test")
				return err
			},
		},
		{
			name: "usage",
			path: "/api/v1/admin/usage",
			call: func(client *Sub2APIHTTPClient) error {
				_, err := client.Usage(context.Background(), Sub2APIUsageQuery{UserID: 41, APIKeyID: 9, Page: 1, PageSize: 50})
				return err
			},
		},
		{
			name: "balance history",
			path: "/api/v1/admin/users/41/balance-history",
			call: func(client *Sub2APIHTTPClient) error {
				_, err := client.FinancialBalanceHistoryByCodes(context.Background(), 41, []string{"opl:target"})
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, pagination := range []struct {
				pages   int
				wantErr bool
			}{{pages: 1}, {pages: 0, wantErr: true}} {
				t.Run(fmt.Sprintf("pages=%d", pagination.pages), func(t *testing.T) {
					client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
						switch r.URL.Path {
						case "/api/v1/auth/login":
							writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
						case tc.path:
							pageSize := 100
							if tc.path == "/api/v1/admin/usage" {
								pageSize = 50
							} else if tc.path == "/api/v1/admin/users/41/balance-history" {
								pageSize = 100
							}
							writeSub2APISuccess(t, w, map[string]any{"items": []any{}, "total": 0, "page": 1, "page_size": pageSize, "pages": pagination.pages})
						default:
							t.Fatalf("unexpected route %s", r.URL.Path)
						}
					}, time.Second)
					err := tc.call(client)
					if pagination.wantErr && (err == nil || !strings.Contains(err.Error(), "pagination")) {
						t.Fatalf("pages=%d error=%v, want pagination error", pagination.pages, err)
					}
					if !pagination.wantErr && err != nil {
						t.Fatalf("pages=%d error=%v", pagination.pages, err)
					}
				})
			}
		})
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

func TestSub2APIBatchUsersUsagePreservesValidRequestedItems(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
		case "/api/v1/admin/dashboard/users-usage":
			writeSub2APISuccess(t, w, map[string]any{"stats": map[string]any{
				"41": map[string]any{"user_id": 41, "today_actual_cost": json.RawMessage("0.00000001"), "total_actual_cost": 1.25, "by_platform": []any{}},
				"42": map[string]any{"user_id": 42, "today_actual_cost": "malformed", "total_actual_cost": 2.5, "by_platform": []any{}},
			}})
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}, time.Second)

	stats, err := client.BatchUsersUsage(context.Background(), []int64{41, 42, 43})
	if err != nil || len(stats) != 1 || stats[41].TodayActualCostUSDMicros != 0 || stats[41].TotalActualCostUSDMicros != 1_250_000 {
		t.Fatalf("partially valid batch user usage = %#v err=%v", stats, err)
	}
	if _, exists := stats[42]; exists {
		t.Fatalf("malformed requested user usage was retained: %#v", stats)
	}
	if _, exists := stats[43]; exists {
		t.Fatalf("missing requested user usage was fabricated: %#v", stats)
	}
}

func TestSub2APIBatchKeysUsagePreservesValidRequestedItems(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
		case "/api/v1/admin/dashboard/api-keys-usage":
			writeSub2APISuccess(t, w, map[string]any{"stats": map[string]any{
				"7": map[string]any{"api_key_id": 7, "today_actual_cost": json.RawMessage("0.00000001"), "total_actual_cost": 4.5},
				"9": map[string]any{"api_key_id": 9, "today_actual_cost": 0, "total_actual_cost": "malformed"},
			}})
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}, time.Second)

	stats, err := client.BatchKeysUsage(context.Background(), []int64{7, 8, 9})
	if err != nil || len(stats) != 1 || stats[7].TodayActualCostUSDMicros != 0 || stats[7].TotalActualCostUSDMicros != 4_500_000 {
		t.Fatalf("partially valid batch key usage = %#v err=%v", stats, err)
	}
	if _, exists := stats[8]; exists {
		t.Fatalf("missing requested key usage was fabricated: %#v", stats)
	}
	if _, exists := stats[9]; exists {
		t.Fatalf("malformed requested key usage was retained: %#v", stats)
	}
}

func TestSub2APIBatchUsageRejectsUnrequestedItems(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		data map[string]any
		call func(*Sub2APIHTTPClient) error
	}{
		{
			name: "user", path: "/api/v1/admin/dashboard/users-usage",
			data: map[string]any{"stats": map[string]any{"99": map[string]any{"user_id": 99, "today_actual_cost": 0, "total_actual_cost": 0}}},
			call: func(client *Sub2APIHTTPClient) error {
				_, err := client.BatchUsersUsage(context.Background(), []int64{41})
				return err
			},
		},
		{
			name: "key", path: "/api/v1/admin/dashboard/api-keys-usage",
			data: map[string]any{"stats": map[string]any{"99": map[string]any{"api_key_id": 99, "today_actual_cost": 0, "total_actual_cost": 0}}},
			call: func(client *Sub2APIHTTPClient) error {
				_, err := client.BatchKeysUsage(context.Background(), []int64{7})
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/login":
					writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
				case tc.path:
					writeSub2APISuccess(t, w, tc.data)
				default:
					t.Fatalf("unexpected route %s", r.URL.Path)
				}
			}, time.Second)
			if err := tc.call(client); err == nil || !strings.Contains(err.Error(), "unexpected sub2api batch") {
				t.Fatalf("unrequested %s usage error = %v", tc.name, err)
			}
		})
	}
}

func userKeyFixture(id int64, status string) map[string]any {
	return map[string]any{
		"id": id, "user_id": 41, "key": "sk-user-secret", "name": "general-key", "status": status,
		"quota": 12.345678, "quota_used": 1.25, "usage_5h": 0.1, "usage_1d": 0.2, "usage_7d": 0.3,
		"group_id": 7, "ip_whitelist": []string{"203.0.113.10"}, "ip_blacklist": []string{"198.51.100.0/24"},
		"rate_limit_5h": 5.0, "rate_limit_1d": 10.0, "rate_limit_7d": 20.0, "current_concurrency": 2,
		"last_used_at": "2026-07-18T01:02:03Z", "last_used_ip": "203.0.113.10", "expires_at": "2026-08-18T01:02:03Z",
		"created_at": "2026-07-17T01:02:03Z", "updated_at": "2026-07-18T02:03:04Z",
	}
}

func TestSub2APIUserKeyPageAndGroupsParity(t *testing.T) {
	requests := 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer delegated-user-token" {
			t.Fatalf("delegated authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/v1/keys":
			query := r.URL.Query()
			if query.Get("page") != "2" || query.Get("page_size") != "20" || query.Get("search") != "pilot" || query.Get("status") != "inactive" || query.Get("group_id") != "7" || query.Get("sort_by") != "last_used_at" || query.Get("sort_order") != "asc" {
				t.Fatalf("key query = %q", r.URL.RawQuery)
			}
			writeSub2APISuccess(t, w, map[string]any{"items": []any{userKeyFixture(17, "inactive")}, "total": 21, "page": 2, "page_size": 20, "pages": 2})
		case "/api/v1/groups/available":
			writeSub2APISuccess(t, w, []any{map[string]any{
				"id": 7, "name": "Basic", "description": "Default group", "platform": "openai", "rate_multiplier": 1.25,
				"subscription_type": "standard", "status": "active", "internal_secret": "must-not-project",
			}})
		default:
			t.Fatalf("unexpected delegated route: %s %s", r.Method, r.URL.String())
		}
	}, time.Second)
	credential := SessionDelegatedCredential{Bearer: "delegated-user-token", ExpiresAt: time.Now().Add(time.Hour)}
	page, err := client.UserKeyPage(context.Background(), credential, 41, Sub2APIKeyPageQuery{
		Page: 2, PageSize: 20, Search: "pilot", Status: "disabled", GroupID: ptr(int64(7)), SortBy: "lastUsedAt", SortOrder: "asc",
	})
	if err != nil || page.Total != 21 || page.Pages != 2 || len(page.Items) != 1 {
		t.Fatalf("key page = %#v err=%v", page, err)
	}
	key := page.Items[0]
	if key.Key != "sk-user-secret" || key.GroupID == nil || *key.GroupID != 7 || key.Status != "disabled" || key.CurrentConcurrency != 2 || key.RateLimit7dUSDMicros != 20_000_000 || key.CreatedAt.IsZero() || key.UpdatedAt.IsZero() {
		t.Fatalf("key parity fields = %#v", key)
	}
	groups, err := client.UserGroups(context.Background(), credential, 41)
	if err != nil || len(groups) != 1 || groups[0].ID != 7 || groups[0].Name != "Basic" || groups[0].RateMultiplier != 1.25 {
		t.Fatalf("groups = %#v err=%v", groups, err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
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

func TestUserKeyParityCreateAndUpdatePayloads(t *testing.T) {
	requests := 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		fixture := userKeyFixture(17, "active")
		switch r.Method {
		case http.MethodPost:
			for field, want := range map[string]any{
				"name": "parity-key", "group_id": float64(7), "quota": 1.25, "expires_in_days": float64(30),
				"rate_limit_5h": 5.0, "rate_limit_1d": 10.0, "rate_limit_7d": 20.0,
			} {
				if input[field] != want {
					t.Fatalf("create %s = %#v, want %#v; payload=%#v", field, input[field], want, input)
				}
			}
		case http.MethodPut:
			for field, want := range map[string]any{
				"name": "edited", "group_id": float64(8), "status": "inactive", "quota": 2.5,
				"expires_at": "2026-09-18T01:02:03Z", "rate_limit_5h": 0.0, "rate_limit_1d": 12.0,
				"rate_limit_7d": 24.0, "reset_quota": true, "reset_rate_limit_usage": true,
			} {
				if input[field] != want {
					t.Fatalf("update %s = %#v, want %#v; payload=%#v", field, input[field], want, input)
				}
			}
			fixture["name"], fixture["group_id"], fixture["status"], fixture["quota"] = "edited", 8, "inactive", 2.5
			fixture["rate_limit_5h"], fixture["rate_limit_1d"], fixture["rate_limit_7d"] = 0, 12, 24
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
		writeSub2APISuccess(t, w, fixture)
	}, time.Second)
	days := 30
	created, err := client.CreateUserKey(context.Background(), SessionDelegatedCredential{Bearer: "delegated-user-token"}, 41, Sub2APICreateKeyInput{
		Name: "parity-key", GroupID: 7, IPWhitelist: []string{"203.0.113.10"}, IPBlacklist: []string{"198.51.100.0/24"},
		QuotaUSDMicros: 1_250_000, ExpiresInDays: &days, RateLimit5hUSDMicros: 5_000_000,
		RateLimit1dUSDMicros: 10_000_000, RateLimit7dUSDMicros: 20_000_000,
	}, "create-parity")
	if err != nil || created.ID != 17 {
		t.Fatalf("create parity = %#v err=%v", created, err)
	}
	name, groupID, quota, enabled := "edited", int64(8), int64(2_500_000), false
	expiresAt, rate5h, rate1d, rate7d, reset := "2026-09-18T01:02:03Z", int64(0), int64(12_000_000), int64(24_000_000), true
	updated, err := client.UpdateUserKey(context.Background(), SessionDelegatedCredential{Bearer: "delegated-user-token"}, 41, 17, Sub2APIUpdateKeyInput{
		Name: &name, GroupID: &groupID, Enabled: &enabled, IPWhitelist: ptr([]string{}), IPBlacklist: ptr([]string{"192.0.2.0/24"}),
		QuotaUSDMicros: &quota, ExpiresAt: &expiresAt, RateLimit5hUSDMicros: &rate5h, RateLimit1dUSDMicros: &rate1d,
		RateLimit7dUSDMicros: &rate7d, ResetQuota: &reset, ResetRateLimitUsage: &reset,
	})
	if err != nil || updated.Name != name || updated.GroupID == nil || *updated.GroupID != groupID || updated.Status != "disabled" {
		t.Fatalf("update parity = %#v err=%v", updated, err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
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
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(location)
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
			query := r.URL.Query()
			if query.Get("user_id") != "41" || query.Has("api_key_id") || query.Has("period") ||
				query.Get("start_date") != time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, location).Format("2006-01-02") ||
				query.Get("end_date") != now.Format("2006-01-02") || query.Get("timezone") != "Asia/Shanghai" {
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

func TestSub2APIClientBalanceFloorsLiveDecimalToSpendableMicros(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/users/41":
			writeSub2APISuccess(t, w, json.RawMessage(`{"id":41,"balance":60.00000001,"status":"active"}`))
		default:
			t.Fatalf("unexpected Sub2API route %s %s", r.Method, r.URL.Path)
		}
	}, time.Second)

	balance, err := client.Balance(context.Background(), 41)
	if err != nil || balance != (Sub2APIBalance{UserID: 41, USDMicros: 60_000_000, Status: "active"}) {
		t.Fatalf("balance = %#v, err=%v", balance, err)
	}
}

func TestFloorUSDDecimalToSpendableMicros(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  int64
	}{
		{name: "sub-micro becomes zero", value: "0.00000001", want: 0},
		{name: "fractional micro is floored", value: "60.00000001", want: 60_000_000},
		{name: "largest representable floor", value: "9223372036854.7758079", want: 9_223_372_036_854_775_807},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := floorUSDDecimalToSpendableMicros(json.Number(tc.value))
			if err != nil || got != tc.want {
				t.Fatalf("floor spendable micros = %d, err=%v, want %d", got, err, tc.want)
			}
		})
	}
}

func TestFloorUSDDecimalToSpendableMicrosPreservesIntegerDebitDelta(t *testing.T) {
	pre, err := floorUSDDecimalToSpendableMicros(json.Number("60.00000001"))
	if err != nil {
		t.Fatalf("pre-balance projection: %v", err)
	}
	post, err := floorUSDDecimalToSpendableMicros(json.Number("7.42000001"))
	if err != nil {
		t.Fatalf("post-balance projection: %v", err)
	}
	if delta := pre - post; delta != 52_580_000 {
		t.Fatalf("projected debit delta = %d, want 52580000", delta)
	}
}

func TestFloorUSDDecimalToSpendableMicrosFailsClosed(t *testing.T) {
	for _, value := range []string{
		"-0.00000001",
		"9223372036854.775808",
		"not-a-number",
		"NaN",
		"Inf",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := floorUSDDecimalToSpendableMicros(json.Number(value)); err == nil {
				t.Fatalf("invalid live balance %q was accepted", value)
			}
		})
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

func TestSub2APIClientReadsStrictMappedWorkspaceKeyByFilteredName(t *testing.T) {
	lastUsedAt := "2026-07-18T01:02:03Z"
	keyStatus := "active"
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/usage/search-api-keys":
			writeSub2APISuccess(t, w, []any{map[string]any{"id": 9, "user_id": 41, "name": "opl-workspace"}})
		case "/api/v1/admin/users/41/api-keys":
			writeSub2APISuccess(t, w, map[string]any{
				"items": []any{map[string]any{"id": 9, "user_id": 41, "name": "opl-workspace", "key": "workspace-secret", "status": keyStatus, "quota": 10.000001, "quota_used": 2.000002, "usage_5h": 1, "usage_1d": 2, "usage_7d": 3, "last_used_at": lastUsedAt}},
				"total": 1, "page": 1, "page_size": 1, "pages": 1,
			})
		default:
			t.Fatalf("unexpected Sub2API route %s %s", r.Method, r.URL.Path)
		}
	}, time.Second)

	keyClient, ok := any(client).(interface {
		WorkspaceKeysForConvergence(context.Context, int64, string) ([]Sub2APIWorkspaceKey, error)
	})
	if !ok {
		t.Fatal("Sub2API client does not expose strict key listing")
	}
	keys, err := keyClient.WorkspaceKeysForConvergence(context.Background(), 41, "opl-workspace")
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	if len(keys) != 1 || keys[0].ID != 9 || keys[0].UserID != 41 {
		t.Fatalf("keys = %#v", keys)
	}
	if keys[0].QuotaUSDMicros != 10_000_001 || keys[0].QuotaUsedUSDMicros != 2_000_002 || keys[0].LastUsedAt == nil || keys[0].LastUsedAt.Format(time.RFC3339) != lastUsedAt {
		t.Fatalf("strict key fields = %#v", keys[0])
	}
	keyStatus = "unknown"
	if _, err := keyClient.WorkspaceKeysForConvergence(context.Background(), 41, "opl-workspace"); err == nil {
		t.Fatal("unknown key status was accepted")
	}
}

func TestSub2APIWorkspaceKeyConvergenceFiltersByNameAndDoesNotDownloadAllKeys(t *testing.T) {
	const targetName = "opl-workspace-0123456789ab"
	listCalls := 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/usage/search-api-keys":
			if r.URL.Query().Get("user_id") != "41" || r.URL.Query().Get("q") != targetName {
				t.Fatalf("workspace key search query = %s", r.URL.RawQuery)
			}
			writeSub2APISuccess(t, w, []any{
				map[string]any{"id": 777, "user_id": 41, "name": targetName},
				map[string]any{"id": 778, "user_id": 41, "name": targetName + "-fuzzy"},
			})
		case "/api/v1/admin/users/41/api-keys":
			listCalls++
			query := r.URL.Query()
			page, err := strconv.Atoi(query.Get("page"))
			if err != nil || page < 1 || page > 1001 || query.Get("page_size") != "1" || query.Get("sort_by") != "id" || query.Get("sort_order") != "asc" {
				t.Fatalf("workspace key lookup query = %s", r.URL.RawQuery)
			}
			writeSub2APISuccess(t, w, map[string]any{
				"items": []any{map[string]any{
					"id": page, "user_id": 41, "name": map[bool]string{true: targetName, false: "other"}[page == 777],
					"key": map[bool]string{true: "workspace-secret", false: "other-secret"}[page == 777], "status": "active",
					"quota": 0, "quota_used": 0, "usage_5h": 0, "usage_1d": 0, "usage_7d": 0,
				}},
				"total": 1001, "page": page, "page_size": 1, "pages": 1001,
			})
		default:
			t.Fatalf("unexpected Sub2API route %s %s", r.Method, r.URL.String())
		}
	}, time.Second)

	keys, err := client.WorkspaceKeysForConvergence(context.Background(), 41, targetName)
	if err != nil || len(keys) != 1 || keys[0].ID != 777 || keys[0].Name != targetName || keys[0].Key != "workspace-secret" {
		t.Fatalf("workspace key convergence = %#v err=%v", keys, err)
	}
	if listCalls > 12 {
		t.Fatalf("workspace key lookup downloaded too many rows: %d", listCalls)
	}
}

func TestSub2APIClientWorkspaceKeyRequiresSelectedSecret(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/usage/search-api-keys":
			writeSub2APISuccess(t, w, []any{map[string]any{"id": 9, "user_id": 41, "name": "opl-workspace"}})
		case "/api/v1/admin/users/41/api-keys":
			writeSub2APISuccess(t, w, map[string]any{"items": []any{map[string]any{
				"id": 9, "user_id": 41, "name": "opl-workspace", "key": "", "status": "active",
				"quota": 0, "quota_used": 0, "usage_5h": 0, "usage_1d": 0, "usage_7d": 0,
			}}, "total": 1, "page": 1, "page_size": 1, "pages": 1})
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}, time.Second)
	if _, err := client.WorkspaceKey(context.Background(), 41); err == nil {
		t.Fatal("active Workspace key without secret was accepted")
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
			case "/api/v1/admin/usage/search-api-keys":
				writeSub2APISuccess(t, w, []any{map[string]any{"id": 9, "user_id": 41, "name": "opl-workspace"}})
			case "/api/v1/admin/users/41/api-keys":
				writeSub2APISuccess(t, w, map[string]any{"items": []any{item}, "total": 1, "page": 1, "page_size": 1, "pages": 1})
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
				case "/api/v1/admin/usage/search-api-keys":
					refs := make([]map[string]any, 0, len(tc.items))
					for _, item := range tc.items {
						refs = append(refs, map[string]any{"id": item["id"], "user_id": item["user_id"], "name": item["name"]})
					}
					writeSub2APISuccess(t, w, refs)
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

func TestSub2APIClientWorkspaceKeyRejectsPaginationIdentityDrift(t *testing.T) {
	keyRequests := 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/usage/search-api-keys":
			writeSub2APISuccess(t, w, []any{map[string]any{"id": 3, "user_id": 41, "name": "opl-workspace"}})
		case "/api/v1/admin/users/41/api-keys":
			keyRequests++
			page, err := strconv.Atoi(r.URL.Query().Get("page"))
			if err != nil {
				t.Fatalf("invalid page query: %v", err)
			}
			total := 4
			if page > 1 {
				total = 5
			}
			writeSub2APISuccess(t, w, map[string]any{
				"items": []any{map[string]any{
					"id": page, "user_id": 41, "name": "other", "key": "other-key", "status": "active",
					"quota": 0, "quota_used": 0, "usage_5h": 0, "usage_1d": 0, "usage_7d": 0,
				}},
				"total": total, "page": page, "page_size": 1, "pages": total,
			})
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}, time.Second)
	if _, err := client.WorkspaceKey(context.Background(), 41); err == nil || !strings.Contains(err.Error(), "pagination") {
		t.Fatalf("workspace key pagination error = %v", err)
	}
	if keyRequests != 2 {
		t.Fatalf("key requests = %d, want 2", keyRequests)
	}
}

func TestSub2APIClientWorkspaceKeyRejectsCrossUserAndDoesNotLeakKey(t *testing.T) {
	const secret = "workspace-key-secret"
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/usage/search-api-keys":
			writeSub2APISuccess(t, w, []any{map[string]any{"id": 1, "user_id": 41, "name": "opl-workspace"}})
		case "/api/v1/admin/users/41/api-keys":
			writeSub2APISuccess(t, w, map[string]any{"items": []map[string]any{{"id": 1, "user_id": 42, "name": "opl-workspace", "key": secret, "status": "active"}}, "total": 1, "page": 1, "page_size": 1, "pages": 1})
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

func TestSub2APIAdjustmentRejectsOverlengthCodeBeforeHTTP(t *testing.T) {
	legacyCode := "opl:wallet-adjustment:" + strings.Repeat("a", 24) + ":v1"
	if len(legacyCode) != 49 {
		t.Fatalf("legacy code length = %d, want 49", len(legacyCode))
	}
	for _, tc := range []struct {
		name string
		call func(*Sub2APIHTTPClient) error
	}{
		{name: "charge", call: func(client *Sub2APIHTTPClient) error {
			_, err := client.Charge(context.Background(), Sub2APIChargeInput{UserID: 41, Code: legacyCode, ChargeUSDMicros: 1})
			return err
		}},
		{name: "refund", call: func(client *Sub2APIHTTPClient) error {
			_, err := client.Refund(context.Background(), Sub2APIRefundInput{UserID: 41, Code: legacyCode, RefundUSDMicros: 1})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			httpCalls := 0
			client := newSub2APITestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				httpCalls++
				http.Error(w, "must not be called", http.StatusInternalServerError)
			}, time.Second)
			if err := tc.call(client); err == nil {
				t.Fatal("overlength redeem code should be rejected")
			}
			if httpCalls != 0 {
				t.Fatalf("overlength redeem code made %d HTTP requests", httpCalls)
			}
		})
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
				case "/api/v1/admin/usage/search-api-keys":
					writeSub2APISuccess(t, w, []any{map[string]any{"id": 9, "user_id": 41, "name": "opl-workspace"}})
				case "/api/v1/admin/users/41/api-keys":
					writeSub2APISuccess(t, w, map[string]any{
						"items": []any{map[string]any{
							"id": 9, "user_id": 41, "name": "opl-workspace", "key": "workspace-key-secret", "status": "active",
							"quota": 0, "quota_used": 0, "usage_5h": 0, "usage_1d": 0, "usage_7d": 0,
						}},
						"total": 1, "page": 1, "page_size": 1, "pages": 1,
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
						if r.URL.Query().Get("type") != "balance" || r.URL.Query().Get("page") != "1" || r.URL.Query().Get("page_size") != "100" {
							t.Fatalf("history query = %s", r.URL.RawQuery)
						}
						if scenario.historyStatus != 0 {
							http.Error(w, "unavailable", scenario.historyStatus)
							return
						}
						writeSub2APISuccess(t, w, map[string]any{"items": scenario.items(), "total": scenario.total, "page": 1, "page_size": 100, "pages": 1})
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
	t.Run("conflict diagnostics survive missing replay evidence", func(t *testing.T) {
		client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/auth/login":
				writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
			case "/api/v1/admin/redeem-codes/create-and-redeem":
				w.Header().Set("X-Request-ID", "req-upstream-409")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"code":"redeem_conflict","message":"response-secret"}`))
			case "/api/v1/admin/users/41/balance-history":
				writeSub2APISuccess(t, w, map[string]any{"items": []any{}, "total": 0, "page": 1, "pages": 1})
			default:
				http.NotFound(w, r)
			}
		}, time.Second)

		_, err := client.Charge(context.Background(), Sub2APIChargeInput{UserID: 41, Code: "opl:conflict-diagnostic", ChargeUSDMicros: 1_000_000})
		if !errors.Is(err, ErrSub2APIChargeUnknown) {
			t.Fatalf("conflict diagnostic error = %v", err)
		}
		details, ok := Sub2APIFailure(err)
		if !ok || details.HTTPStatus != http.StatusConflict || details.ErrorCode != "redeem_conflict" || details.RequestID != "req-upstream-409" {
			t.Fatalf("conflict diagnostic details = %#v, ok=%t", details, ok)
		}
		if strings.Contains(err.Error(), "response-secret") {
			t.Fatalf("conflict diagnostic leaked response body: %v", err)
		}
	})

	t.Run("http diagnostics", func(t *testing.T) {
		client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/auth/login":
				writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
			case "/api/v1/admin/redeem-codes/create-and-redeem":
				w.Header().Set("X-Request-ID", "req-upstream-503")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"code":"gateway_busy","message":"response-secret","request_id":"body-request-id"}`))
			default:
				http.NotFound(w, r)
			}
		}, time.Second)

		_, err := client.Charge(context.Background(), Sub2APIChargeInput{UserID: 41, Code: "opl:http-diagnostic", ChargeUSDMicros: 1_000_000})
		if !errors.Is(err, ErrSub2APIChargeUnknown) {
			t.Fatalf("HTTP diagnostic error = %v", err)
		}
		details, ok := Sub2APIFailure(err)
		if !ok || details.HTTPStatus != http.StatusServiceUnavailable || details.ErrorCode != "gateway_busy" || details.RequestID != "req-upstream-503" {
			t.Fatalf("HTTP diagnostic details = %#v, ok=%t", details, ok)
		}
		if strings.Contains(err.Error(), "response-secret") || strings.Contains(err.Error(), "body-request-id") {
			t.Fatalf("HTTP diagnostic leaked response body: %v", err)
		}
	})

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
		details, ok := Sub2APIFailure(err)
		if !ok || details.HTTPStatus != 0 || details.ErrorCode != "request_timeout" || details.RequestID != "" {
			t.Fatalf("timeout diagnostic details = %#v, ok=%t", details, ok)
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
			writeSub2APISuccess(t, w, json.RawMessage(`{"items":[{"user_id":41,"api_key_id":9,"request_id":"req-1","created_at":"2026-07-16T00:00:00Z","model":"gpt-5","inbound_endpoint":"/v1/responses","request_type":"sync","input_tokens":10,"output_tokens":20,"cache_creation_tokens":0,"cache_read_tokens":5,"actual_cost":0.001234,"duration_ms":987,"first_token_ms":123,"user":{"email":"private@example.test"},"api_key":{"key":"key-secret"},"ip_address":"198.51.100.1","user_agent":"secret-agent","prompt":"prompt-secret","response":"response-secret"}],"total":1,"page":1,"page_size":50,"pages":1}`))
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
	if row.DurationMS == nil || *row.DurationMS != 987 || row.FirstTokenMS == nil || *row.FirstTokenMS != 123 {
		t.Fatalf("usage latency = duration:%v first-token:%v", row.DurationMS, row.FirstTokenMS)
	}
	encoded, _ := json.Marshal(row)
	for _, forbidden := range []string{"private@example.test", "key-secret", "198.51.100.1", "secret-agent", "prompt-secret", "response-secret"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("usage row leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestSub2APIUsageListAppliesCalendarMonthDateRange(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(location)
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/usage":
			query := r.URL.Query()
			if query.Get("start_date") != time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, location).Format("2006-01-02") ||
				query.Get("end_date") != now.Format("2006-01-02") || query.Get("timezone") != "Asia/Shanghai" {
				t.Fatalf("usage period query = %q", r.URL.RawQuery)
			}
			writeSub2APISuccess(t, w, map[string]any{"items": []any{}, "total": 0, "page": 1, "page_size": 50, "pages": 1})
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}, time.Second)

	if _, err := client.Usage(context.Background(), Sub2APIUsageQuery{UserID: 41, APIKeyID: 9, Page: 1, PageSize: 50, Period: "month"}); err != nil {
		t.Fatalf("usage month range: %v", err)
	}
}

func TestSub2APIUsageDateRangeUsesShanghaiCalendarBoundaries(t *testing.T) {
	now := time.Date(2026, time.August, 2, 16, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		period    string
		wantStart string
		wantEnd   string
		wantOK    bool
	}{
		{period: "today", wantStart: "2026-08-03", wantEnd: "2026-08-03", wantOK: true},
		{period: "week", wantStart: "2026-08-03", wantEnd: "2026-08-03", wantOK: true},
		{period: "month", wantStart: "2026-08-01", wantEnd: "2026-08-03", wantOK: true},
		{period: "quarter"},
	} {
		start, end, ok := sub2APIUsageDateRange(tc.period, now)
		if start != tc.wantStart || end != tc.wantEnd || ok != tc.wantOK {
			t.Fatalf("period %q range = %q..%q ok=%t, want %q..%q ok=%t", tc.period, start, end, ok, tc.wantStart, tc.wantEnd, tc.wantOK)
		}
	}
}

func TestSub2APIUsageListFloorsSubMicroActualCost(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/usage":
			writeSub2APISuccess(t, w, json.RawMessage(`{"items":[{"user_id":41,"api_key_id":9,"request_id":"req-sub-micro","created_at":"2026-07-16T00:00:00Z","model":"gpt-5","request_type":"sync","input_tokens":1,"output_tokens":1,"cache_creation_tokens":0,"cache_read_tokens":0,"actual_cost":0.00000001}],"total":1,"page":1,"page_size":50,"pages":1}`))
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}, time.Second)

	page, err := client.Usage(context.Background(), Sub2APIUsageQuery{UserID: 41, APIKeyID: 9, Page: 1, PageSize: 50})
	if err != nil || len(page.Items) != 1 || page.Items[0].ActualCostUSDMicros != 0 {
		t.Fatalf("sub-micro usage actual cost = %#v err=%v", page, err)
	}
}

func TestSub2APIUsageListValidatesNullableLatency(t *testing.T) {
	for _, tc := range []struct {
		name, latency string
		wantErr       bool
	}{
		{name: "missing", latency: ""},
		{name: "null", latency: `"duration_ms":null,"first_token_ms":null,`},
		{name: "negative duration", latency: `"duration_ms":-1,"first_token_ms":1,`, wantErr: true},
		{name: "negative first token", latency: `"duration_ms":1,"first_token_ms":-1,`, wantErr: true},
		{name: "fractional duration", latency: `"duration_ms":1.5,"first_token_ms":1,`, wantErr: true},
		{name: "fractional first token", latency: `"duration_ms":1,"first_token_ms":1.5,`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/login":
					writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
				case "/api/v1/admin/usage":
					row := fmt.Sprintf(`{"user_id":41,"api_key_id":9,"request_id":"req-1","created_at":"2026-07-16T00:00:00Z","model":"gpt-5","inbound_endpoint":"/v1/responses","request_type":"sync",%s"input_tokens":1,"output_tokens":2,"cache_creation_tokens":0,"cache_read_tokens":0,"actual_cost":0.000001}`, tc.latency)
					writeSub2APISuccess(t, w, json.RawMessage(fmt.Sprintf(`{"items":[%s],"total":1,"page":1,"page_size":50,"pages":1}`, row)))
				default:
					t.Fatalf("unexpected route %s", r.URL.Path)
				}
			}, time.Second)

			page, err := client.Usage(context.Background(), Sub2APIUsageQuery{UserID: 41, APIKeyID: 9, Page: 1, PageSize: 50})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("usage latency accepted: %#v", page)
				}
				return
			}
			if err != nil || len(page.Items) != 1 || page.Items[0].DurationMS != nil || page.Items[0].FirstTokenMS != nil {
				t.Fatalf("nullable usage latency = %#v err=%v", page, err)
			}
		})
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
		{name: "empty", page: 1, total: 0, pages: 1, items: rows(0)},
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

func TestSub2APIUsageStatsConvertsActualCostToConservativeMicros(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(location)
	for raw, want := range map[string]int64{"0": 0, "0.0000001": 0, "0.000001": 1, "12.345678": 12_345_678} {
		t.Run(raw, func(t *testing.T) {
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/login":
					writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
				case "/api/v1/admin/usage/stats":
					query := r.URL.Query()
					if query.Get("user_id") != "41" || query.Get("api_key_id") != "9" || query.Has("period") ||
						query.Get("start_date") != time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, location).Format("2006-01-02") ||
						query.Get("end_date") != now.Format("2006-01-02") || query.Get("timezone") != "Asia/Shanghai" {
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
		"negative tokens": `{"total_requests":1,"total_input_tokens":-1,"total_output_tokens":0,"total_tokens":0,"total_actual_cost":0}`,
		"missing cost":    `{"total_requests":1,"total_input_tokens":1,"total_output_tokens":0,"total_tokens":1}`,
		"negative cost":   `{"total_requests":1,"total_input_tokens":1,"total_output_tokens":0,"total_tokens":1,"total_actual_cost":-0.0000001}`,
		"overflow":        `{"total_requests":1,"total_input_tokens":1,"total_output_tokens":0,"total_tokens":1,"total_actual_cost":9223372036854.775808}`,
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

func TestSub2APIFinancialBalanceHistoryByCodesStopsAfterTargetPage(t *testing.T) {
	requestedPages := []string{}
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/users/41/balance-history":
			query := r.URL.Query()
			if query.Get("page_size") != "100" || query.Get("type") != "balance" {
				t.Fatalf("history query = %s", r.URL.RawQuery)
			}
			requestedPages = append(requestedPages, query.Get("page"))
			page, err := strconv.Atoi(query.Get("page"))
			if err != nil || page < 1 || page > 3 {
				t.Fatalf("history page = %q", query.Get("page"))
			}
			items := make([]any, 0, 100)
			for index := 0; index < 100; index++ {
				code := fmt.Sprintf("opl:filler:%d", (page-1)*100+index)
				if page == 2 && index == 42 {
					code = "opl:target"
				}
				items = append(items, map[string]any{
					"code": code, "type": "balance", "value": -52.58, "status": "used", "used_by": 41,
					"used_at": "2026-07-16T00:01:00Z", "created_at": "2026-07-16T00:00:00Z", "notes": "must-not-leak",
				})
			}
			writeSub2APISuccess(t, w, map[string]any{"items": items, "total": 300, "page": page, "page_size": 100, "pages": 3})
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}, time.Second)

	matches, err := client.FinancialBalanceHistoryByCodes(context.Background(), 41, []string{"opl:target"})
	if err != nil || !slices.Equal(requestedPages, []string{"1", "2"}) || len(matches) != 1 {
		t.Fatalf("financial history matches=%#v pages=%#v err=%v", matches, requestedPages, err)
	}
	entry := matches["opl:target"]
	if entry.Code != "opl:target" || entry.ValueUSDMicros != -52_580_000 || entry.UsedBy == nil || *entry.UsedBy != 41 {
		t.Fatalf("financial history entry = %#v", entry)
	}
	encoded, _ := json.Marshal(matches)
	if strings.Contains(string(encoded), "must-not-leak") || strings.Contains(string(encoded), "notes") {
		t.Fatalf("financial history leaked upstream fields: %s", encoded)
	}
}

func TestSub2APIFinancialBalanceHistoryByCodesFindsTargetBeyondTenThousandRows(t *testing.T) {
	requests := 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/users/41/balance-history":
			page, err := strconv.Atoi(r.URL.Query().Get("page"))
			if err != nil || page < 1 || page > 101 || r.URL.Query().Get("page_size") != "100" || r.URL.Query().Get("type") != "balance" {
				t.Fatalf("history query = %s", r.URL.RawQuery)
			}
			requests++
			count := 100
			if page == 101 {
				count = 1
			}
			items := make([]any, 0, count)
			for index := 0; index < count; index++ {
				code := fmt.Sprintf("opl:financial:%d", (page-1)*100+index+1)
				if page == 101 {
					code = "opl:financial:last"
				}
				items = append(items, map[string]any{
					"code": code, "type": "balance", "value": -0.000001, "status": "used", "used_by": 41,
					"used_at": "2026-07-16T00:01:00Z", "created_at": "2026-07-16T00:00:00Z",
				})
			}
			writeSub2APISuccess(t, w, map[string]any{"items": items, "total": 10001, "page": page, "page_size": 100, "pages": 101})
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}, 5*time.Second)

	matches, err := client.FinancialBalanceHistoryByCodes(context.Background(), 41, []string{"opl:financial:last"})
	if err != nil || requests != 101 || len(matches) != 1 || matches["opl:financial:last"].Code != "opl:financial:last" {
		t.Fatalf("financial history requests=%d matches=%#v err=%v", requests, matches, err)
	}
}

func TestSub2APIFinancialBalanceHistoryByCodesAcceptsAuthoritativeMissingTarget(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
			return
		}
		writeSub2APISuccess(t, w, map[string]any{"items": []any{}, "total": 0, "page": 1, "page_size": 100, "pages": 1})
	}, time.Second)
	matches, err := client.FinancialBalanceHistoryByCodes(context.Background(), 41, []string{"opl:missing"})
	if err != nil || len(matches) != 0 {
		t.Fatalf("missing balance history=%#v err=%v", matches, err)
	}
}

func TestSub2APIFinancialBalanceHistoryByCodesRejectsUntrustedIdentityAndPagination(t *testing.T) {
	for name, data := range map[string]string{
		"used by another user": `{"items":[{"code":"opl:target","type":"balance","value":-1,"status":"used","used_by":42,"used_at":"2026-07-16T00:01:00Z","created_at":"2026-07-16T00:00:00Z"}],"total":1,"page":1,"page_size":100,"pages":1}`,
		"wrong page count":     `{"items":[],"total":0,"page":1,"page_size":100,"pages":2}`,
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
			if _, err := client.FinancialBalanceHistoryByCodes(context.Background(), 41, []string{"opl:target"}); err == nil {
				t.Fatalf("untrusted history accepted: %s", data)
			}
			if requests != 1 {
				t.Fatalf("history requests = %d", requests)
			}
		})
	}
}

func TestSub2APIFinancialBalanceHistoryByCodesUsesOneAbsoluteDeadline(t *testing.T) {
	requests := 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
			return
		}
		requests++
		<-r.Context().Done()
	}, 25*time.Millisecond)
	if _, err := client.FinancialBalanceHistoryByCodes(context.Background(), 41, []string{"opl:target"}); err == nil {
		t.Fatal("financial history deadline was ignored")
	}
	if requests != 1 {
		t.Fatalf("financial history requests = %d", requests)
	}
}

func TestSub2APIBalanceHistoryPageReadsOnlyRequestedPage(t *testing.T) {
	requests := 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/users/41/balance-history":
			requests++
			if query := r.URL.Query(); query.Get("page") != "3" || query.Get("page_size") != "20" || query.Get("type") != "balance" {
				t.Fatalf("history query = %s", r.URL.RawQuery)
			}
			writeSub2APISuccess(t, w, map[string]any{
				"items": []any{map[string]any{"code": "opl:page-three", "type": "balance", "value": -1.25, "status": "used", "used_by": 41, "used_at": "2026-07-16T00:01:00Z", "created_at": "2026-07-16T00:00:00Z"}},
				"total": 41, "page": 3, "page_size": 20, "pages": 3,
			})
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}, time.Second)

	page, err := client.BalanceHistoryPage(context.Background(), 41, Sub2APIBalanceHistoryPageQuery{Page: 3, PageSize: 20})
	if err != nil || requests != 1 || page.Total != 41 || page.Page != 3 || page.PageSize != 20 || page.Pages != 3 || len(page.Items) != 1 || page.Items[0].Code != "opl:page-three" || page.Items[0].ValueUSDMicros != -1_250_000 {
		t.Fatalf("history page = %#v requests=%d err=%v", page, requests, err)
	}
}

func TestSub2APIBalanceHistoryPageAllowsDisplayPaginationAcrossTenThousandRows(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
			return
		}
		if query := r.URL.Query(); r.URL.Path != "/api/v1/admin/users/41/balance-history" || query.Get("page") != "1" || query.Get("page_size") != "20" {
			t.Fatalf("unexpected history request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		items := make([]any, 20)
		for index := range items {
			items[index] = map[string]any{"code": fmt.Sprintf("opl:display:%d", index), "type": "balance", "value": -0.000001, "status": "used", "used_by": 41, "used_at": "2026-07-16T00:01:00Z", "created_at": "2026-07-16T00:00:00Z"}
		}
		writeSub2APISuccess(t, w, map[string]any{"items": items, "total": 10001, "page": 1, "page_size": 20, "pages": 501})
	}, time.Second)

	page, err := client.BalanceHistoryPage(context.Background(), 41, Sub2APIBalanceHistoryPageQuery{Page: 1, PageSize: 20})
	if err != nil || page.Total != 10001 || page.Pages != 501 || len(page.Items) != 20 {
		t.Fatalf("large display page = %#v err=%v", page, err)
	}
}

func TestSub2APIAdminUserKeyCountReadsOnlyPaginationTotal(t *testing.T) {
	requests := 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/users/41/api-keys":
			requests++
			if query := r.URL.Query(); query.Get("page") != "1" || query.Get("page_size") != "1" {
				t.Fatalf("key count query = %s", r.URL.RawQuery)
			}
			writeSub2APISuccess(t, w, map[string]any{
				"items": []any{userKeyFixture(7, "active")},
				"total": 1001, "page": 1, "page_size": 1, "pages": 1001,
			})
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}, time.Second)

	count, err := client.AdminUserKeyCount(context.Background(), 41)
	if err != nil || count != 1001 || requests != 1 {
		t.Fatalf("key count = %d requests=%d err=%v", count, requests, err)
	}
}

func TestSub2APIAdminUserKeyCountRejectsInvalidPaginationItem(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/users/41/api-keys":
			writeSub2APISuccess(t, w, map[string]any{
				"items": []any{1}, "total": 1001, "page": 1, "page_size": 1, "pages": 1001,
			})
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}, time.Second)

	if _, err := client.AdminUserKeyCount(context.Background(), 41); err == nil {
		t.Fatal("invalid key-count pagination item was accepted")
	}
}
