package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/controlplane"
)

type failingSessionSaveStore struct {
	*memoryTableStore
}

func (store *failingSessionSaveStore) SaveSession(context.Context, map[string]any) error {
	return errors.New("session save failed")
}

type failingSessionAuthorityReadStore struct {
	*memoryTableStore
	failure string
}

type failOnSecondSessionReadStore struct {
	*memoryTableStore
	sessionReads int
}

type failOnSecondMembershipReadStore struct {
	*memoryTableStore
	membershipReads int
	failOnSecond    bool
}

func (store *failOnSecondSessionReadStore) GetSession(ctx context.Context, id string) (map[string]any, bool, error) {
	store.sessionReads++
	if store.sessionReads == 2 {
		return nil, false, errors.New("second session read failed")
	}
	return store.memoryTableStore.GetSession(ctx, id)
}

func (store *failOnSecondMembershipReadStore) GetMembershipByAccount(ctx context.Context, accountID string) (map[string]any, bool, error) {
	store.membershipReads++
	if store.failOnSecond && store.membershipReads == 2 {
		return nil, false, errors.New("second membership read failed")
	}
	return store.memoryTableStore.GetMembershipByAccount(ctx, accountID)
}

func (store *failingSessionAuthorityReadStore) GetSession(ctx context.Context, id string) (map[string]any, bool, error) {
	if store.failure == "session" {
		return nil, false, errors.New("session read failed")
	}
	return store.memoryTableStore.GetSession(ctx, id)
}

func (store *failingSessionAuthorityReadStore) GetUser(ctx context.Context, id string) (map[string]any, bool, error) {
	if store.failure == "user" {
		return nil, false, errors.New("user read failed")
	}
	return store.memoryTableStore.GetUser(ctx, id)
}

func (store *failingSessionAuthorityReadStore) GetAccount(ctx context.Context, id string) (map[string]any, bool, error) {
	if store.failure == "account" {
		return nil, false, errors.New("account read failed")
	}
	return store.memoryTableStore.GetAccount(ctx, id)
}

func (store *failingSessionAuthorityReadStore) GetOrganizationByAccount(ctx context.Context, accountID string) (map[string]any, bool, error) {
	if store.failure == "organization" {
		return nil, false, errors.New("organization read failed")
	}
	return store.memoryTableStore.GetOrganizationByAccount(ctx, accountID)
}

func (store *failingSessionAuthorityReadStore) GetMembershipByAccount(ctx context.Context, accountID string) (map[string]any, bool, error) {
	if store.failure == "membership" {
		return nil, false, errors.New("membership read failed")
	}
	return store.memoryTableStore.GetMembershipByAccount(ctx, accountID)
}

func TestAuthMeReportsAuthenticationUnavailableWithoutRevokingSessionOnAuthorityReadFailure(t *testing.T) {
	for _, failure := range []string{"session", "user", "account", "organization", "membership"} {
		t.Run(failure, func(t *testing.T) {
			store := &failingSessionAuthorityReadStore{memoryTableStore: newMemoryTableStore()}
			server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := createIdentityUser(server, map[string]any{
				"email": "authority-read-" + failure + "@example.com", "accountId": "acct-authority-read-" + failure,
				"password": "CorrectHorseBatteryStaple!",
			}); err != nil {
				t.Fatal(err)
			}
			login := loginForTest(t, server, "authority-read-"+failure+"@example.com", "CorrectHorseBatteryStaple!")

			store.failure = failure
			unavailable := requestWithSession(t, server, login, http.MethodGet, "/api/auth/me", "")
			if unavailable.Code != http.StatusServiceUnavailable || !strings.Contains(unavailable.Body.String(), "authentication_unavailable") {
				t.Fatalf("authority failure status=%d body=%s", unavailable.Code, unavailable.Body.String())
			}
			if cookie := unavailable.Header().Get("Set-Cookie"); cookie != "" {
				t.Fatalf("authority failure cleared Session cookie: %q", cookie)
			}

			store.failure = ""
			recovered := requestWithSession(t, server, login, http.MethodGet, "/api/auth/me", "")
			if recovered.Code != http.StatusOK {
				t.Fatalf("authority recovery status=%d body=%s", recovered.Code, recovered.Body.String())
			}
		})
	}
}

func TestAuthMeReusesProtectedAuthenticationPayload(t *testing.T) {
	store := &failOnSecondSessionReadStore{memoryTableStore: newMemoryTableStore()}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createIdentityUser(server, map[string]any{
		"email": "auth-me-one-shot@example.com", "accountId": "acct-auth-me-one-shot",
		"password": "CorrectHorseBatteryStaple!",
	}); err != nil {
		t.Fatal(err)
	}
	login := loginForTest(t, server, "auth-me-one-shot@example.com", "CorrectHorseBatteryStaple!")
	if len(login.Result().Cookies()) != 1 {
		t.Fatalf("login cookie count = %d", len(login.Result().Cookies()))
	}
	store.sessionReads = 0
	response := requestWithSession(t, server, login, http.MethodGet, "/api/auth/me", "")
	if response.Code != http.StatusOK {
		t.Fatalf("auth/me status=%d body=%s sessionReads=%d", response.Code, response.Body.String(), store.sessionReads)
	}
	if store.sessionReads != 1 {
		t.Fatalf("expected one authoritative Session read, got %d", store.sessionReads)
	}
}

func TestProtectedCustomerAuthenticationReadsMembershipOnce(t *testing.T) {
	store := &failOnSecondMembershipReadStore{memoryTableStore: newMemoryTableStore()}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createIdentityUser(server, map[string]any{
		"email": "membership-one-shot@example.com", "accountId": "acct-membership-one-shot",
		"password": "CorrectHorseBatteryStaple!",
	}); err != nil {
		t.Fatal(err)
	}
	login := loginForTest(t, server, "membership-one-shot@example.com", "CorrectHorseBatteryStaple!")
	store.membershipReads = 0
	store.failOnSecond = true
	response := requestWithSession(t, server, login, http.MethodGet, "/api/auth/me", "")
	if response.Code != http.StatusOK {
		t.Fatalf("auth/me status=%d body=%s membershipReads=%d", response.Code, response.Body.String(), store.membershipReads)
	}
	if store.membershipReads != 1 {
		t.Fatalf("expected one authoritative Membership read, got %d", store.membershipReads)
	}
}

func TestOperatorCustomerSurfaceReportsMembershipReadFailureAsAuthenticationUnavailable(t *testing.T) {
	store := &failingSessionAuthorityReadStore{memoryTableStore: newMemoryTableStore()}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatal(err)
	}
	login := reservedOperatorSessionForTest(t, server)
	store.failure = "membership"
	response := requestWithSession(t, server, login, http.MethodGet, "/api/auth/me", "")
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "authentication_unavailable") {
		t.Fatalf("auth/me status=%d body=%s", response.Code, response.Body.String())
	}
	if cookie := response.Header().Get("Set-Cookie"); cookie != "" {
		t.Fatalf("membership read failure cleared Session cookie")
	}
}

func TestSessionCredentialVaultUsesHashedKeysAndExpiresCredentials(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	vault := newSessionCredentialVault(func() time.Time { return now })
	credential := SessionDelegatedCredential{Bearer: "delegated-user-secret", ExpiresAt: now.Add(time.Hour)}
	if err := vault.Put("raw-session-id", credential); err == nil {
		t.Fatal("raw session ID accepted as a credential key")
	}
	key := sessionLookupKey("raw-session-id")
	if err := vault.Put(key, credential); err != nil {
		t.Fatal(err)
	}
	if got, ok := vault.Get(key); !ok || got != credential {
		t.Fatalf("Get() = %#v, %v", got, ok)
	}
	now = credential.ExpiresAt
	if _, ok := vault.Get(key); ok {
		t.Fatal("expired delegated credential remained available")
	}
}

func TestVaultMissRequiresLoginAndClearsSession(t *testing.T) {
	store := newMemoryTableStore()
	remote := newIdentityTestSub2API()
	service := controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, remote)
	server, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createIdentityUser(server, map[string]any{
		"email": "vault-miss@example.com", "accountId": "acct-vault-miss", "password": "CorrectHorseBatteryStaple!",
	}); err != nil {
		t.Fatal(err)
	}
	login := loginForTest(t, server, "vault-miss@example.com", "CorrectHorseBatteryStaple!")
	cookie := login.Result().Cookies()[0]

	restarted, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	restarted.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "reauthentication_required") || !strings.Contains(rec.Header().Get("Set-Cookie"), sessionCookieName+"=;") {
		t.Fatalf("vault miss status=%d cookie=%q body=%s", rec.Code, rec.Header().Get("Set-Cookie"), rec.Body.String())
	}
	sessions, err := store.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sessions[sessionLookupKey(cookie.Value)] != nil {
		t.Fatalf("vault miss left database session: %#v", sessions)
	}
}

func TestDelegatedCredentialNeverPersistsOrLeaks(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	if _, err := createIdentityUser(server, map[string]any{
		"email": "delegated@example.com", "accountId": "acct-delegated", "password": "CorrectHorseBatteryStaple!",
	}); err != nil {
		t.Fatal(err)
	}
	login := loginForTest(t, server, "delegated@example.com", "CorrectHorseBatteryStaple!")
	cookie := login.Result().Cookies()[0]
	app := server.(*controlPlaneHTTPHandler).app
	credential, ok := app.sessionCredentials.Get(sessionLookupKey(cookie.Value))
	if !ok || credential.Bearer != "test-user-delegated-token" {
		t.Fatalf("delegated credential = %#v, %v", credential, ok)
	}
	sessions, err := app.tables.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encodedSessions, err := json.Marshal(sessions)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{login.Body.String(), string(encodedSessions)} {
		if strings.Contains(value, credential.Bearer) || strings.Contains(value, "access_token") || strings.Contains(value, "refresh_token") {
			t.Fatalf("delegated credential leaked: %s", value)
		}
	}
}

func TestLogoutClearsCredential(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	login := operatorSessionForTest(t, server)
	cookie := login.Result().Cookies()[0]
	app := server.(*controlPlaneHTTPHandler).app
	key := sessionLookupKey(cookie.Value)
	if _, ok := app.sessionCredentials.Get(key); !ok {
		t.Fatal("login did not bind delegated credential")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	addAuth(req, login)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := app.sessionCredentials.Get(key); ok {
		t.Fatal("logout retained delegated credential")
	}
}

func TestSessionCredentialRollbackWhenVaultRejectsCredential(t *testing.T) {
	app := newControlPlaneApp()
	users, err := app.tables.ListUsers(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.createSession(findRecord(users, "usr-admin"), ""); err == nil {
		t.Fatal("createSession accepted an empty delegated credential")
	}
	sessions, err := app.tables.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("failed credential bind left database session: %#v", sessions)
	}
}

func TestSessionCredentialSaveFailureRollsBackVault(t *testing.T) {
	store := &failingSessionSaveStore{memoryTableStore: newMemoryTableStore()}
	app, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	users, err := app.tables.ListUsers(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.createSession(findRecord(users, "usr-admin"), "delegated-user-secret"); err == nil {
		t.Fatal("createSession succeeded when Session persistence failed")
	}
	app.sessionCredentials.mu.Lock()
	defer app.sessionCredentials.mu.Unlock()
	if len(app.sessionCredentials.credentials) != 0 {
		t.Fatal("Session persistence failure retained delegated credential")
	}
}

func TestAccountDisableRevokesSessionCredential(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	operator := operatorSessionForTest(t, server)
	const accountID = "acct-lifecycle-disable"
	if _, err := createIdentityUser(server, map[string]any{
		"email": "lifecycle-disable@example.com", "accountId": accountID, "password": "CorrectHorseBatteryStaple!",
	}); err != nil {
		t.Fatal(err)
	}
	login := loginForTest(t, server, "lifecycle-disable@example.com", "CorrectHorseBatteryStaple!")
	app := server.(*controlPlaneHTTPHandler).app
	key := sessionLookupKey(login.Result().Cookies()[0].Value)
	response := requestWithMutationKeyForTest(t, server, operator, http.MethodPost, "/api/operator/accounts/"+accountID+"/disable", `{"confirmationAccountId":"`+accountID+`","reason":"pilot_offboarding"}`, "disable-vault-account")
	if response.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", response.Code, response.Body.String())
	}
	if _, ok := app.sessionCredentials.Get(key); ok {
		t.Fatal("disable retained delegated credential")
	}
}
