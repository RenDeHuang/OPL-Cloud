package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSub2APIIdentityResolveReusesOneExactEmailAcrossFullPagination(t *testing.T) {
	requestedPages, customerLogins := []string{}, 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["email"] == "owner@example.com" {
				customerLogins++
				if body["password"] != "CorrectHorseBatteryStaple!" {
					t.Fatalf("customer login = %#v", body)
				}
				writeSub2APISuccess(t, w, map[string]any{
					"access_token": "customer-access", "refresh_token": "customer-refresh",
					"user": map[string]any{"id": 1001, "email": "owner@example.com", "status": "active"},
				})
				return
			}
			writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
		case "/api/v1/admin/users":
			query := r.URL.Query()
			if r.Method != http.MethodGet || query.Get("page_size") != "1000" || query.Get("search") != "owner@example.com" || query.Get("sort_by") != "id" || query.Get("sort_order") != "asc" {
				t.Fatalf("lookup request = %s %s", r.Method, r.URL.String())
			}
			requestedPages = append(requestedPages, query.Get("page"))
			page, _ := strconv.Atoi(query.Get("page"))
			if page == 1 {
				items := make([]any, 0, 1000)
				for id := 1; id <= 1000; id++ {
					items = append(items, map[string]any{"id": id, "email": fmt.Sprintf("other-%d@example.com", id), "status": "active", "notes": "private"})
				}
				writeSub2APISuccess(t, w, map[string]any{"items": items, "total": 1001, "page": 1, "page_size": 1000, "pages": 2})
				return
			}
			writeSub2APISuccess(t, w, map[string]any{"items": []any{map[string]any{"id": 1001, "email": " Owner@Example.com ", "status": "active", "balance": 99}}, "total": 1001, "page": 2, "page_size": 1000, "pages": 2})
		case "/api/v1/admin/users/1001":
			writeSub2APISuccess(t, w, map[string]any{"id": 1001, "email": "owner@example.com", "status": "active", "notes": "private"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}, time.Second)

	identity, err := client.ResolveOrCreateUser(context.Background(), " Owner@Example.com ", "CorrectHorseBatteryStaple!")
	if err != nil || identity != (Sub2APIIdentity{ID: 1001, Email: "owner@example.com", Status: "active"}) {
		t.Fatalf("identity=%#v err=%v", identity, err)
	}
	if strings.Join(requestedPages, ",") != "1,2" || customerLogins != 1 {
		t.Fatalf("requested pages = %#v customerLogins=%d", requestedPages, customerLogins)
	}
	encoded, _ := json.Marshal(identity)
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "balance") {
		t.Fatalf("identity leaked admin fields: %s", encoded)
	}
}

func TestSub2APIIdentityResolveRequiresPasswordForExistingUser(t *testing.T) {
	customerLogins, readbacks := 0, 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["email"] == "owner@example.com" {
				customerLogins++
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
		case "/api/v1/admin/users":
			writeSub2APISuccess(t, w, map[string]any{"items": []any{
				map[string]any{"id": 41, "email": "owner@example.com", "status": "active"},
			}, "total": 1, "page": 1, "page_size": 1000, "pages": 1})
		case "/api/v1/admin/users/41":
			readbacks++
			writeSub2APISuccess(t, w, map[string]any{"id": 41, "email": "owner@example.com", "status": "active"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}, time.Second)

	if _, err := client.ResolveOrCreateUser(context.Background(), "owner@example.com", "wrong password"); !errors.Is(err, ErrSub2APIInvalidCredentials) {
		t.Fatalf("error=%v want=%v", err, ErrSub2APIInvalidCredentials)
	}
	if customerLogins != 1 || readbacks != 0 {
		t.Fatalf("customerLogins=%d readbacks=%d", customerLogins, readbacks)
	}
}

func TestSub2APIIdentityLookupRejectsShortNonFinalPage(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
		case "/api/v1/admin/users":
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			items := make([]any, 0, 999)
			if page == 1 {
				for id := 1; id <= 999; id++ {
					items = append(items, map[string]any{"id": id, "email": fmt.Sprintf("other-%d@example.com", id), "status": "active"})
				}
			} else {
				items = append(items,
					map[string]any{"id": 1000, "email": "owner@example.com", "status": "active"},
					map[string]any{"id": 1001, "email": "other-1001@example.com", "status": "active"},
				)
			}
			writeSub2APISuccess(t, w, map[string]any{"items": items, "total": 1001, "page": page, "page_size": 1000, "pages": 2})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}, time.Second)

	if _, err := client.usersByEmail(context.Background(), "owner@example.com"); !errors.Is(err, ErrSub2APIIdentityConflict) {
		t.Fatalf("error=%v want=%v", err, ErrSub2APIIdentityConflict)
	}
}

func TestSub2APIIdentityResolveCreatesThenConvergesAfterLostResponse(t *testing.T) {
	lookups, creates, customerLogins := 0, 0, 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["email"] == "owner@example.com" {
				customerLogins++
				if body["password"] != "CorrectHorseBatteryStaple!" {
					t.Fatalf("customer login = %#v", body)
				}
				writeSub2APISuccess(t, w, map[string]any{
					"access_token": "customer-access", "refresh_token": "customer-refresh",
					"user": map[string]any{"id": 41, "email": "owner@example.com", "status": "active"},
				})
				return
			}
			writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
		case "/api/v1/admin/users":
			if r.Method == http.MethodPost {
				creates++
				if r.Header.Get("Idempotency-Key") != "" {
					t.Fatalf("user create sent unproven idempotency key %q", r.Header.Get("Idempotency-Key"))
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if len(body) != 3 || body["email"] != "owner@example.com" || body["password"] != "CorrectHorseBatteryStaple!" || body["role"] != "user" {
					t.Fatalf("create body = %#v", body)
				}
				http.Error(w, "response lost", http.StatusInternalServerError)
				return
			}
			lookups++
			items := []any{}
			if lookups > 1 {
				items = append(items, map[string]any{"id": 41, "email": "owner@example.com", "status": "active", "notes": "private"})
			}
			writeSub2APISuccess(t, w, map[string]any{"items": items, "total": len(items), "page": 1, "page_size": 1000, "pages": 1})
		case "/api/v1/admin/users/41":
			writeSub2APISuccess(t, w, map[string]any{"id": 41, "email": "owner@example.com", "status": "active"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}, time.Second)

	identity, err := client.ResolveOrCreateUser(context.Background(), "owner@example.com", "CorrectHorseBatteryStaple!")
	if err != nil || identity.ID != 41 || lookups != 2 || creates != 1 || customerLogins != 1 {
		t.Fatalf("identity=%#v lookups=%d creates=%d customerLogins=%d err=%v", identity, lookups, creates, customerLogins, err)
	}
}

func TestSub2APIIdentityResolveWaiterHonorsContextCancellation(t *testing.T) {
	leaderEntered := make(chan struct{})
	leaderRelease := make(chan struct{})
	var blockLeader sync.Once
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				http.Error(w, "invalid body", http.StatusBadRequest)
				return
			}
			if body["email"] == "admin@example.test" {
				blockLeader.Do(func() {
					close(leaderEntered)
					<-leaderRelease
				})
				writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
				return
			}
			writeSub2APISuccess(t, w, map[string]any{
				"access_token": "customer-access",
				"user":         map[string]any{"id": 41, "email": "owner@example.com", "status": "active"},
			})
		case "/api/v1/admin/users":
			writeSub2APISuccess(t, w, map[string]any{"items": []any{
				map[string]any{"id": 41, "email": "owner@example.com", "status": "active"},
			}, "total": 1, "page": 1, "page_size": 1000, "pages": 1})
		case "/api/v1/admin/users/41":
			writeSub2APISuccess(t, w, map[string]any{"id": 41, "email": "owner@example.com", "status": "active"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}, time.Second)

	leaderResult := make(chan error, 1)
	go func() {
		_, err := client.ResolveOrCreateUser(context.Background(), "owner@example.com", "CorrectHorseBatteryStaple!")
		leaderResult <- err
	}()
	<-leaderEntered

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterStarted := make(chan struct{})
	waiterResult := make(chan error, 1)
	go func() {
		close(waiterStarted)
		_, err := client.ResolveOrCreateUser(waiterCtx, "owner@example.com", "CorrectHorseBatteryStaple!")
		waiterResult <- err
	}()
	<-waiterStarted
	cancelWaiter()

	select {
	case err := <-waiterResult:
		if !errors.Is(err, context.Canceled) {
			close(leaderRelease)
			<-leaderResult
			t.Fatalf("waiter error=%v want=%v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		close(leaderRelease)
		<-leaderResult
		<-waiterResult
		t.Fatal("canceled waiter did not return before leader release")
	}
	close(leaderRelease)
	if err := <-leaderResult; err != nil {
		t.Fatalf("leader resolve: %v", err)
	}
}

func TestSub2APIIdentityConcurrentResolveCreatesUserOnce(t *testing.T) {
	var mu sync.Mutex
	created, creates := false, 0
	leaderEntered := make(chan struct{})
	leaderRelease := make(chan struct{})
	var blockLeader sync.Once
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				http.Error(w, "invalid body", http.StatusBadRequest)
				return
			}
			if body["email"] == "owner@example.com" {
				writeSub2APISuccess(t, w, map[string]any{
					"access_token": "customer-access",
					"user":         map[string]any{"id": 41, "email": "owner@example.com", "status": "active"},
				})
				return
			}
			writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
		case "/api/v1/admin/users":
			if r.Method == http.MethodPost {
				mu.Lock()
				creates++
				created = true
				mu.Unlock()
				writeSub2APISuccess(t, w, map[string]any{"id": 41})
				return
			}
			mu.Lock()
			wasCreated := created
			mu.Unlock()
			if !wasCreated {
				blockLeader.Do(func() {
					close(leaderEntered)
					<-leaderRelease
				})
			}
			items := []any{}
			if wasCreated {
				items = append(items, map[string]any{"id": 41, "email": "owner@example.com", "status": "active"})
			}
			writeSub2APISuccess(t, w, map[string]any{"items": items, "total": len(items), "page": 1, "page_size": 1000, "pages": 1})
		case "/api/v1/admin/users/41":
			writeSub2APISuccess(t, w, map[string]any{"id": 41, "email": "owner@example.com", "status": "active"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}, time.Second)

	errs := make(chan error, 2)
	go func() {
		_, err := client.ResolveOrCreateUser(context.Background(), "owner@example.com", "CorrectHorseBatteryStaple!")
		errs <- err
	}()
	<-leaderEntered
	waiterStarted := make(chan struct{})
	go func() {
		close(waiterStarted)
		_, err := client.ResolveOrCreateUser(context.Background(), "owner@example.com", "CorrectHorseBatteryStaple!")
		errs <- err
	}()
	<-waiterStarted
	close(leaderRelease)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("resolve user: %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if creates != 1 {
		t.Fatalf("remote creates = %d, want 1", creates)
	}
}

func TestSub2APIIdentityResolveFailsClosedForAmbiguousOrIncoherentLookup(t *testing.T) {
	for _, tc := range []struct {
		name string
		data map[string]any
	}{
		{name: "multiple exact matches", data: map[string]any{"items": []any{
			map[string]any{"id": 41, "email": "owner@example.com", "status": "active"},
			map[string]any{"id": 42, "email": " OWNER@example.com ", "status": "active"},
		}, "total": 2, "page": 1, "page_size": 1000, "pages": 1}},
		{name: "pagination drift", data: map[string]any{"items": []any{map[string]any{"id": 41, "email": "owner@example.com", "status": "active"}}, "total": 1, "page": 1, "page_size": 999, "pages": 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/login":
					writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
				case "/api/v1/admin/users":
					writeSub2APISuccess(t, w, tc.data)
				default:
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
				}
			}, time.Second)
			if _, err := client.ResolveOrCreateUser(context.Background(), "owner@example.com", "CorrectHorseBatteryStaple!"); err == nil {
				t.Fatal("ambiguous/incoherent lookup succeeded")
			}
		})
	}
}

func TestSub2APIIdentityReadbackRequiresExactActiveIdentity(t *testing.T) {
	for _, data := range []map[string]any{
		{"id": 0, "email": "owner@example.com", "status": "active"},
		{"id": 42, "email": "other@example.com", "status": "active"},
		{"id": 42, "email": "owner@example.com", "status": "disabled"},
	} {
		client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/auth/login" {
				writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
				return
			}
			writeSub2APISuccess(t, w, data)
		}, time.Second)
		if _, err := client.UserIdentity(context.Background(), 42, "owner@example.com"); err == nil {
			t.Fatalf("invalid readback accepted: %#v", data)
		}
	}
}

func TestSub2APIIdentityGenericReadbackAllowsDisabledAndRejectsUnknown(t *testing.T) {
	status := "disabled"
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
		case "/api/v1/admin/users/42":
			writeSub2APISuccess(t, w, map[string]any{"id": 42, "email": " Remote@Example.com ", "status": status, "balance": 999, "raw_admin": "drop"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}, time.Second)

	identityClient, ok := any(client).(interface {
		User(context.Context, int64) (Sub2APIIdentity, error)
	})
	if !ok {
		t.Fatal("Sub2API client does not expose generic user readback")
	}
	identity, err := identityClient.User(context.Background(), 42)
	if err != nil || identity != (Sub2APIIdentity{ID: 42, Email: "remote@example.com", Status: "disabled"}) {
		t.Fatalf("disabled identity = %#v, err=%v", identity, err)
	}
	status = "unknown"
	if _, err := identityClient.User(context.Background(), 42); err == nil {
		t.Fatal("unknown identity status was accepted")
	}
}

func TestSub2APIIdentityReadbackAcceptsAccessOnlyAdminToken(t *testing.T) {
	loginCalls, readbackCalls := 0, 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			loginCalls++
			writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access"})
		case "/api/v1/admin/users/91":
			readbackCalls++
			if r.Header.Get("Authorization") != "Bearer admin-access" {
				t.Fatalf("readback authorization = %q", r.Header.Get("Authorization"))
			}
			writeSub2APISuccess(t, w, map[string]any{"id": 91, "email": "admin@example.test", "status": "active"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}, time.Second)

	identity, err := client.UserIdentity(context.Background(), 91, "admin@example.test")
	if err != nil || identity != (Sub2APIIdentity{ID: 91, Email: "admin@example.test", Status: "active"}) || loginCalls != 1 || readbackCalls != 1 {
		t.Fatalf("identity=%#v logins=%d readbacks=%d err=%v", identity, loginCalls, readbackCalls, err)
	}
}

func TestAuthenticateUserReturnsDelegatedCredential(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/login" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 3 || body["email"] != "owner@example.com" || body["password"] != "new remote password" || body["turnstile_token"] != "" {
			t.Fatalf("login body = %#v", body)
		}
		writeSub2APISuccess(t, w, map[string]any{
			"access_token": "customer-access-secret", "refresh_token": "customer-refresh-secret",
			"user": map[string]any{"id": 41, "email": " Owner@Example.com ", "status": "active", "balance": 100},
		})
	}, time.Second)

	authentication, err := client.AuthenticateUser(context.Background(), " Owner@Example.com ", "new remote password")
	if err != nil {
		t.Fatal(err)
	}
	if authentication.Identity != (Sub2APIIdentity{ID: 41, Email: "owner@example.com", Status: "active"}) || authentication.AccessToken != "customer-access-secret" {
		t.Fatalf("authentication=%#v", authentication)
	}
	encoded, _ := json.Marshal(authentication)
	for _, secret := range []string{"customer-access-secret", "customer-refresh-secret", "balance"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("authentication leaked %q: %s", secret, encoded)
		}
	}
}

func TestSub2APIIdentityAuthenticateAcceptsAccessOnlyResponse(t *testing.T) {
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/login" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		writeSub2APISuccess(t, w, map[string]any{
			"access_token": "customer-access-secret",
			"user":         map[string]any{"id": 41, "email": "owner@example.com", "status": "active"},
		})
	}, time.Second)

	authentication, err := client.AuthenticateUser(context.Background(), "owner@example.com", "remote password")
	if err != nil || authentication.Identity != (Sub2APIIdentity{ID: 41, Email: "owner@example.com", Status: "active"}) || authentication.AccessToken != "customer-access-secret" {
		t.Fatalf("authentication=%#v err=%v", authentication, err)
	}
}

func TestSub2APIIdentityAuthenticateClassifiesFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		data   any
		want   error
	}{
		{name: "invalid credentials", status: http.StatusUnauthorized, want: ErrSub2APIInvalidCredentials},
		{name: "rate limited", status: http.StatusTooManyRequests, want: ErrSub2APIAuthRateLimited},
		{name: "upstream failure", status: http.StatusInternalServerError, want: ErrSub2APIAuthUnavailable},
		{name: "requires 2fa", status: http.StatusOK, data: map[string]any{"requires_2fa": true, "temp_token": "secret"}, want: ErrSub2APIAuthUnavailable},
		{name: "missing user", status: http.StatusOK, data: map[string]any{"access_token": "secret", "refresh_token": "secret"}, want: ErrSub2APIAuthUnavailable},
		{name: "identity inactive", status: http.StatusOK, data: map[string]any{"access_token": "secret", "refresh_token": "secret", "user": map[string]any{"id": 41, "email": "owner@example.com", "status": "disabled"}}, want: ErrSub2APIAuthUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.status != http.StatusOK {
					w.WriteHeader(tc.status)
					return
				}
				writeSub2APISuccess(t, w, tc.data)
			}, time.Second)
			_, err := client.AuthenticateUser(context.Background(), "owner@example.com", "password")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
		})
	}
}

func TestSub2APIAdminIdentityAuthenticatesThenReadsBackExactUser(t *testing.T) {
	loginCalls, readbackCalls := 0, 0
	client := newSub2APITestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			loginCalls++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["email"] != "admin@example.test" || body["password"] != "admin-secret" {
				t.Fatalf("admin login body = %#v", body)
			}
			if loginCalls == 1 {
				if body["turnstile_token"] != "" {
					t.Fatalf("admin identity login body = %#v", body)
				}
				writeSub2APISuccess(t, w, map[string]any{
					"access_token": "identity-access", "refresh_token": "identity-refresh",
					"user": map[string]any{"id": 91, "email": "admin@example.test", "status": "active", "balance": 100},
				})
				return
			}
			writeSub2APISuccess(t, w, map[string]any{"access_token": "admin-access", "refresh_token": "admin-refresh"})
		case "/api/v1/admin/users/91":
			readbackCalls++
			if r.Header.Get("Authorization") != "Bearer admin-access" {
				t.Fatalf("readback authorization = %q", r.Header.Get("Authorization"))
			}
			writeSub2APISuccess(t, w, map[string]any{"id": 91, "email": "admin@example.test", "status": "active", "notes": "private"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}, time.Second)
	capability, ok := any(client).(interface {
		AdminIdentity(context.Context) (Sub2APIIdentity, error)
	})
	if !ok {
		t.Fatal("Sub2API client is missing admin identity capability")
	}

	identity, err := capability.AdminIdentity(context.Background())
	if err != nil || identity != (Sub2APIIdentity{ID: 91, Email: "admin@example.test", Status: "active"}) || loginCalls != 2 || readbackCalls != 1 {
		t.Fatalf("identity=%#v logins=%d readbacks=%d err=%v", identity, loginCalls, readbackCalls, err)
	}
	encoded, _ := json.Marshal(identity)
	for _, secret := range []string{"identity-access", "identity-refresh", "admin-access", "admin-refresh", "private"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("admin identity leaked %q: %s", secret, encoded)
		}
	}
}
