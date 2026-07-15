package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func newSub2APITestClient(t *testing.T, handler http.HandlerFunc, versions []string, timeout time.Duration) *Sub2APIHTTPClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewSub2APIHTTPClient(Sub2APIConfig{
		BaseURL:           server.URL,
		AdminEmail:        "admin@example.test",
		AdminPassword:     "admin-secret",
		SupportedVersions: versions,
		Timeout:           timeout,
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
	}, []string{"0.1.151"}, time.Second)

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

func TestSub2APIClientSelectsOneActiveWorkspaceKeyAcrossPages(t *testing.T) {
	pages := []string{}
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/users/41/api-keys":
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer access" || r.URL.Query().Get("page_size") != "1000" {
				t.Fatalf("unexpected key request: %s %s auth=%q", r.Method, r.URL.String(), r.Header.Get("Authorization"))
			}
			page := r.URL.Query().Get("page")
			pages = append(pages, page)
			if page == "1" {
				writeSub2APISuccess(t, w, map[string]any{
					"items": []any{
						map[string]any{"id": 1, "user_id": 41, "name": "opl-workspace", "key": "inactive-key", "status": "disabled"},
						map[string]any{"id": 2, "user_id": 41, "name": "other", "key": "other-key", "status": "active"},
					},
					"total": 3, "page": 1, "page_size": 1000, "pages": 2,
				})
				return
			}
			writeSub2APISuccess(t, w, map[string]any{
				"items": []any{map[string]any{
					"id": 9, "user_id": 41, "name": "opl-workspace", "key": "workspace-key-secret", "status": "active",
					"quota": 100.000001, "quota_used": 12.345678, "usage_5h": 1.000001, "usage_1d": 2.000002, "usage_7d": 3.000003,
					"last_used_at": "2026-07-16T01:02:03Z",
				}},
				"total": 3, "page": 2, "page_size": 1000, "pages": 2,
			})
		default:
			t.Fatalf("unexpected Sub2API route %s %s", r.Method, r.URL.Path)
		}
	}, []string{"0.1.155"}, time.Second)

	key, err := client.WorkspaceKey(context.Background(), 41)
	if err != nil {
		t.Fatalf("workspace key: %v", err)
	}
	if key.ID != 9 || key.UserID != 41 || key.Name != "opl-workspace" || key.Key != "workspace-key-secret" || key.Status != "active" {
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

func TestSub2APIClientWorkspaceKeyCardinalityFailsClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		items []map[string]any
		want  error
	}{
		"missing": {items: []map[string]any{{"id": 1, "user_id": 41, "name": "other", "key": "other-key", "status": "active"}}, want: ErrSub2APIWorkspaceKeyMissing},
		"ambiguous": {items: []map[string]any{
			{"id": 1, "user_id": 41, "name": "opl-workspace", "key": "workspace-key-one", "status": "active"},
			{"id": 2, "user_id": 41, "name": "opl-workspace", "key": "workspace-key-two", "status": "active"},
		}, want: ErrSub2APIWorkspaceKeyAmbiguous},
	} {
		t.Run(name, func(t *testing.T) {
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/login":
					writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
				case "/api/v1/admin/users/41/api-keys":
					writeSub2APISuccess(t, w, map[string]any{"items": tc.items, "total": len(tc.items), "page": 1, "page_size": 1000, "pages": 1})
				default:
					t.Fatalf("unexpected route %s", r.URL.Path)
				}
			}, []string{"0.1.155"}, time.Second)
			if _, err := client.WorkspaceKey(context.Background(), 41); !errors.Is(err, tc.want) {
				t.Fatalf("workspace key error = %v, want %v", err, tc.want)
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
		case "/api/v1/admin/users/41/api-keys":
			writeSub2APISuccess(t, w, map[string]any{"items": []map[string]any{{"id": 1, "user_id": 42, "name": "opl-workspace", "key": secret, "status": "active"}}, "total": 1, "page": 1, "page_size": 1000, "pages": 1})
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}, []string{"0.1.155"}, time.Second)
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
			}, []string{"0.1.155"}, time.Second)
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

func TestSub2APIClientChargesWithExactNegativeMicrosAndReplays(t *testing.T) {
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
	}, []string{"0.1.151"}, time.Second)

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

func TestSub2APIClientRejectsUnsupportedVersionBeforeCharge(t *testing.T) {
	chargeCalls := 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/system/version":
			writeSub2APISuccess(t, w, map[string]any{"version": "0.1.152"})
		case "/api/v1/admin/redeem-codes/create-and-redeem":
			chargeCalls++
		default:
			http.NotFound(w, r)
		}
	}, []string{"0.1.151"}, time.Second)

	_, err := client.Charge(context.Background(), Sub2APIChargeInput{UserID: 41, Code: "opl:test", ChargeUSDMicros: 1})
	if !errors.Is(err, ErrSub2APIUnsupportedVersion) || chargeCalls != 0 {
		t.Fatalf("unsupported version error = %v, charge calls = %d", err, chargeCalls)
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
	}, []string{"0.1.151"}, time.Second)

	_, err := client.Charge(context.Background(), Sub2APIChargeInput{UserID: 41, Code: "opl:replay", ChargeUSDMicros: 40_000_000})
	if !errors.Is(err, ErrSub2APIChargeConflict) {
		t.Fatalf("same code with different value error = %v", err)
	}
}

func TestSub2APIClientBoundsBodiesAndTreatsChargeTimeoutAsUnknown(t *testing.T) {
	t.Run("response body limit", func(t *testing.T) {
		client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/auth/login":
				writeSub2APISuccess(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
			case "/api/v1/admin/users/41":
				_, _ = fmt.Fprintf(w, `{"code":0,"message":"success","data":{"id":41,"balance":1,"padding":"%s"}}`, strings.Repeat("x", maxSub2APIResponseBytes))
			default:
				http.NotFound(w, r)
			}
		}, []string{"0.1.151"}, time.Second)
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
		}, []string{"0.1.151"}, 20*time.Millisecond)

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
	}, []string{"0.1.151"}, time.Second)

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
