package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	for _, forbidden := range []string{"/balance", "/usage", "/api-keys"} {
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
			writeSub2APISuccess(t, w, json.RawMessage(`{"id":41,"balance":12.345678}`))
		default:
			t.Errorf("unexpected Sub2API route %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}, []string{"0.1.151"}, time.Second)

	balance, err := client.Balance(context.Background(), 41)
	if err != nil {
		t.Fatalf("read balance: %v", err)
	}
	if balance.UserID != 41 || balance.USDMicros != 12_345_678 {
		t.Fatalf("balance = %#v", balance)
	}
	if loginCalls != 1 || refreshCalls != 1 || userCalls != 2 {
		t.Fatalf("calls login=%d refresh=%d user=%d", loginCalls, refreshCalls, userCalls)
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
