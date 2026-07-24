package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type identityTestSub2API struct {
	testSub2APIClient
	identityMu     sync.Mutex
	identities     map[string]clients.Sub2APIIdentity
	passwords      map[string]string
	resolveCalls   int
	remoteCreates  int
	authCalls      int
	authErr        error
	authOverride   *clients.Sub2APIIdentity
	resolveEntered chan struct{}
	resolveRelease <-chan struct{}
}

func newIdentityTestSub2API() *identityTestSub2API {
	return &identityTestSub2API{
		testSub2APIClient: testSub2APIClient{balance: 1_000_000_000_000, charges: map[string]int64{}},
		identities:        map[string]clients.Sub2APIIdentity{},
		passwords:         map[string]string{},
	}
}

func (c *identityTestSub2API) ResolveOrCreateUser(_ context.Context, email, password string) (clients.Sub2APIIdentity, error) {
	if c.resolveEntered != nil {
		c.resolveEntered <- struct{}{}
	}
	if c.resolveRelease != nil {
		<-c.resolveRelease
	}
	c.identityMu.Lock()
	defer c.identityMu.Unlock()
	c.resolveCalls++
	email = normalizeEmail(email)
	if identity, ok := c.identities[email]; ok {
		return identity, nil
	}
	identity := clients.Sub2APIIdentity{ID: int64(70 + len(c.identities) + 1), Email: email, Status: "active"}
	c.identities[email], c.passwords[email] = identity, password
	c.remoteCreates++
	return identity, nil
}

func (c *identityTestSub2API) AuthenticateUser(_ context.Context, email, password string) (clients.Sub2APIUserAuthentication, error) {
	c.identityMu.Lock()
	defer c.identityMu.Unlock()
	c.authCalls++
	if c.authErr != nil {
		return clients.Sub2APIUserAuthentication{}, c.authErr
	}
	if c.authOverride != nil {
		return clients.Sub2APIUserAuthentication{Identity: *c.authOverride, AccessToken: "test-user-delegated-token"}, nil
	}
	email = normalizeEmail(email)
	identity, ok := c.identities[email]
	if !ok || c.passwords[email] != password {
		return clients.Sub2APIUserAuthentication{}, clients.ErrSub2APIInvalidCredentials
	}
	return clients.Sub2APIUserAuthentication{Identity: identity, AccessToken: "test-user-delegated-token"}, nil
}

func (c *identityTestSub2API) UserIdentity(_ context.Context, id int64, email string) (clients.Sub2APIIdentity, error) {
	c.identityMu.Lock()
	defer c.identityMu.Unlock()
	identity, ok := c.identities[normalizeEmail(email)]
	if !ok || identity.ID != id {
		return clients.Sub2APIIdentity{}, clients.ErrSub2APIIdentityConflict
	}
	return identity, nil
}

func newIdentityTestServer(t *testing.T, remote *identityTestSub2API, store StateStore) http.Handler {
	t.Helper()
	service := controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, remote)
	if store == nil {
		return NewServer(service)
	}
	server, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func createIdentityUser(server http.Handler, input map[string]any) (map[string]any, error) {
	handler := server.(*controlPlaneHTTPHandler)
	return handler.app.createUser(context.Background(), handler.service, input)
}

func TestCreateUserRejectsCallerSuppliedSub2APIIdentityAndNonOwnerRole(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{{"null", nil}, {"zero", 0}, {"positive", 73}} {
		t.Run(tc.name, func(t *testing.T) {
			remote := newIdentityTestSub2API()
			server := newIdentityTestServer(t, remote, nil)
			_, err := createIdentityUser(server, map[string]any{
				"email": "owner@example.com", "accountId": "acct-owner", "password": "CorrectHorseBatteryStaple!", "sub2apiUserId": tc.value,
			})
			if !errors.Is(err, errCallerSuppliedSub2APIUserID) || remote.resolveCalls != 0 {
				t.Fatalf("error=%v resolveCalls=%d", err, remote.resolveCalls)
			}
		})
	}
	for _, tc := range []struct {
		name  string
		value any
	}{{"null", nil}, {"number", 73}, {"empty", ""}, {"blank", " "}, {"admin", "admin"}, {"member", "member"}} {
		t.Run("role_"+tc.name, func(t *testing.T) {
			remote := newIdentityTestSub2API()
			server := newIdentityTestServer(t, remote, nil)
			_, err := createIdentityUser(server, map[string]any{
				"email": "owner@example.com", "accountId": "acct-owner", "password": "CorrectHorseBatteryStaple!", "role": tc.value,
			})
			if !errors.Is(err, errInvalidRole) || remote.resolveCalls != 0 {
				t.Fatalf("error=%v resolveCalls=%d", err, remote.resolveCalls)
			}
		})
	}
}

func TestCreateUserUsesRemoteIdentityAndDeterministicOneToOneFacts(t *testing.T) {
	remote := newIdentityTestSub2API()
	server := newIdentityTestServer(t, remote, nil)
	input := map[string]any{"email": " Owner@Example.com ", "accountId": "acct-owner", "password": "CorrectHorseBatteryStaple!"}
	firstUser, firstErr := createIdentityUser(server, input)
	secondUser, secondErr := createIdentityUser(server, input)
	if firstErr != nil || secondErr != nil {
		t.Fatalf("first=%v second=%v", firstErr, secondErr)
	}
	if firstUser["id"] == "" || firstUser["id"] != secondUser["id"] || firstUser["email"] != "owner@example.com" || firstUser["role"] != "owner" {
		t.Fatalf("first=%#v second=%#v", firstUser, secondUser)
	}
	app := server.(*controlPlaneHTTPHandler).app
	accounts, _ := app.tables.ListAccounts(context.Background(), "acct-owner")
	users, _ := app.tables.ListUsers(context.Background(), true)
	organizations, _ := app.tables.ListOrganizations(context.Background())
	memberships, _ := app.tables.ListMemberships(context.Background())
	account := findRecord(accounts, "acct-owner")
	user := findRecord(users, stringValue(firstUser["id"]))
	ownerOrganizations, ownerMemberships := 0, 0
	for _, organization := range organizations {
		if organization["billingAccountId"] == "acct-owner" {
			ownerOrganizations++
		}
	}
	for _, membership := range memberships {
		if membership["accountId"] == "acct-owner" {
			ownerMemberships++
		}
	}
	if account == nil || account["ownerUserId"] != firstUser["id"] || int64(numberField(account, "sub2apiUserId", 0)) != 71 || user == nil || user["passwordHash"] != nil || ownerOrganizations != 1 || ownerMemberships != 1 {
		t.Fatalf("account=%#v user=%#v organizations=%#v memberships=%#v", account, user, organizations, memberships)
	}
	if remote.resolveCalls != 2 || remote.remoteCreates != 1 {
		t.Fatalf("resolveCalls=%d remoteCreates=%d", remote.resolveCalls, remote.remoteCreates)
	}
}

func TestCreateUserRejectsKnownLocalIdentityConflictBeforeRemoteCreate(t *testing.T) {
	store := newMemoryTableStore()
	accountID, email := "acct-existing", "existing@example.com"
	userID := "usr-" + stableID("customer", email)[:18]
	organizationID := "org-" + stableID("account", accountID)[:18]
	if err := store.CreateProvisionedAccount(context.Background(),
		map[string]any{"id": accountID, "ownerUserId": userID, "status": "active", "sub2apiUserId": int64(71)},
		map[string]any{"id": userID, "email": email, "accountId": accountID, "role": "owner", "status": "active"},
		map[string]any{"id": organizationID, "name": "Organization " + accountID, "billingAccountId": accountID, "status": "active"},
		map[string]any{"id": "mem-" + stableID(organizationID, userID)[:18], "accountId": accountID, "organizationId": organizationID, "userId": userID, "role": "owner", "status": "active"},
	); err != nil {
		t.Fatal(err)
	}
	remote := newIdentityTestSub2API()
	server := newIdentityTestServer(t, remote, store)
	_, err := createIdentityUser(server, map[string]any{
		"email": "new@example.com", "accountId": "acct-existing", "password": "CorrectHorseBatteryStaple!",
	})
	if !errors.Is(err, errAccountIdentityConflict) || remote.remoteCreates != 0 {
		t.Fatalf("error=%v remoteCreates=%d", err, remote.remoteCreates)
	}
}

type failOnceProvisionStore struct {
	*memoryTableStore
	mu     sync.Mutex
	failed bool
}

func (s *failOnceProvisionStore) CreateProvisionedAccount(ctx context.Context, account, user, organization, membership map[string]any) error {
	s.mu.Lock()
	if !s.failed {
		s.failed = true
		s.mu.Unlock()
		return errors.New("injected local transaction failure")
	}
	s.mu.Unlock()
	return s.memoryTableStore.CreateProvisionedAccount(ctx, account, user, organization, membership)
}

func TestCreateUserLocalFailureRetryDoesNotDuplicateRemoteOrLocalIdentity(t *testing.T) {
	remote := newIdentityTestSub2API()
	store := &failOnceProvisionStore{memoryTableStore: newMemoryTableStore()}
	server := newIdentityTestServer(t, remote, store)
	input := map[string]any{"email": "retry@example.com", "accountId": "acct-retry", "password": "CorrectHorseBatteryStaple!"}
	_, failed := createIdentityUser(server, input)
	_, succeeded := createIdentityUser(server, input)
	if failed == nil || succeeded != nil {
		t.Fatalf("failed=%v succeeded=%v", failed, succeeded)
	}
	users, _ := store.ListUsers(context.Background(), true)
	count := 0
	for _, user := range users {
		if user["email"] == "retry@example.com" {
			count++
		}
	}
	if remote.remoteCreates != 1 || count != 1 {
		t.Fatalf("remoteCreates=%d localUsers=%#v", remote.remoteCreates, users)
	}
}

func TestCreateUserConcurrentRetryConvergesToOneRemoteAndLocalIdentity(t *testing.T) {
	remote := newIdentityTestSub2API()
	server := newIdentityTestServer(t, remote, nil)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := createIdentityUser(server, map[string]any{
				"email": "race@example.com", "accountId": "acct-race", "password": "CorrectHorseBatteryStaple!",
			})
			results <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent create error=%v", err)
		}
	}
	users, _ := server.(*controlPlaneHTTPHandler).app.tables.ListUsers(context.Background(), true)
	count := 0
	for _, user := range users {
		if user["email"] == "race@example.com" {
			count++
		}
	}
	if remote.remoteCreates != 1 || count != 1 {
		t.Fatalf("remoteCreates=%d users=%#v", remote.remoteCreates, users)
	}
}

func TestCreateUserConcurrentDifferentEmailsForOneAccountCreatesOneRemoteIdentity(t *testing.T) {
	remote := newIdentityTestSub2API()
	remote.resolveEntered = make(chan struct{}, 2)
	release := make(chan struct{})
	remote.resolveRelease = release
	server := newIdentityTestServer(t, remote, nil)
	app := server.(*controlPlaneHTTPHandler).app
	const accountID = "acct-concurrent-owner"
	results := make(chan error, 2)
	go func() {
		_, err := createIdentityUser(server, map[string]any{
			"email": "first@example.com", "accountId": accountID, "password": "CorrectHorseBatteryStaple!",
		})
		results <- err
	}()
	<-remote.resolveEntered
	go func() {
		_, err := createIdentityUser(server, map[string]any{
			"email": "second@example.com", "accountId": accountID, "password": "CorrectHorseBatteryStaple!",
		})
		results <- err
	}()
	if _, accountLockExists := app.resourceLocks.Load("account:" + accountID); !accountLockExists {
		<-remote.resolveEntered
	}
	close(release)

	created, conflicts := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			created++
		case errors.Is(err, errAccountIdentityConflict), errors.Is(err, errUserExists):
			conflicts++
		default:
			t.Fatalf("concurrent create error=%v", err)
		}
	}
	accounts, _ := app.tables.ListAccounts(context.Background(), accountID)
	users, _ := app.tables.ListUsers(context.Background(), true)
	organizations, _ := app.tables.ListOrganizations(context.Background())
	memberships, _ := app.tables.ListMemberships(context.Background())
	accountUsers, accountOrganizations, accountMemberships := 0, 0, 0
	for _, user := range users {
		if user["accountId"] == accountID {
			accountUsers++
		}
	}
	for _, organization := range organizations {
		if organization["billingAccountId"] == accountID {
			accountOrganizations++
		}
	}
	for _, membership := range memberships {
		if membership["accountId"] == accountID {
			accountMemberships++
		}
	}
	if remote.remoteCreates != 1 || created != 1 || conflicts != 1 || len(accounts) != 1 || accountUsers != 1 || accountOrganizations != 1 || accountMemberships != 1 {
		t.Fatalf("remoteCreates=%d created=%d conflicts=%d accounts=%#v users=%#v organizations=%#v memberships=%#v", remote.remoteCreates, created, conflicts, accounts, users, organizations, memberships)
	}
}

func TestCreateUserAccountLockWaiterHonorsContextCancellation(t *testing.T) {
	remote := newIdentityTestSub2API()
	remote.resolveEntered = make(chan struct{}, 1)
	release := make(chan struct{})
	remote.resolveRelease = release
	service := controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, remote)
	app := NewServer(service).(*controlPlaneHTTPHandler).app
	const accountID = "acct-canceled-provision"
	leaderResult := make(chan error, 1)
	go func() {
		_, err := app.createUser(context.Background(), service, map[string]any{
			"email": "leader@example.com", "accountId": accountID, "password": "CorrectHorseBatteryStaple!",
		})
		leaderResult <- err
	}()
	<-remote.resolveEntered

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterStarted := make(chan struct{})
	waiterResult := make(chan error, 1)
	go func() {
		close(waiterStarted)
		_, err := app.createUser(waiterCtx, service, map[string]any{
			"email": "waiter@example.com", "accountId": accountID, "password": "CorrectHorseBatteryStaple!",
		})
		waiterResult <- err
	}()
	<-waiterStarted
	cancelWaiter()

	var releaseOnce sync.Once
	releaseLeader := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseLeader()
	select {
	case err := <-waiterResult:
		if !errors.Is(err, context.Canceled) {
			releaseLeader()
			<-leaderResult
			t.Fatalf("waiter error=%v want=%v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		releaseLeader()
		<-leaderResult
		<-waiterResult
		t.Fatal("canceled account-lock waiter did not return before leader release")
	}
	releaseLeader()
	if err := <-leaderResult; err != nil {
		t.Fatalf("leader create: %v", err)
	}
	if remote.remoteCreates != 1 {
		t.Fatalf("remoteCreates=%d want=1", remote.remoteCreates)
	}
}

func TestLoginUsesRemotePasswordAndStrictMappedIdentity(t *testing.T) {
	remote := newIdentityTestSub2API()
	server := newIdentityTestServer(t, remote, nil)
	if _, err := createIdentityUser(server, map[string]any{
		"email": "login@example.com", "accountId": "acct-login", "password": "InitialRemotePassword!",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	remote.identityMu.Lock()
	remote.passwords["login@example.com"] = "ChangedRemotePassword!"
	remote.identityMu.Unlock()
	if old := loginAttemptForTest(server, "login@example.com", "InitialRemotePassword!", "192.0.2.10:1234"); old.Code != http.StatusUnauthorized {
		t.Fatalf("old password status=%d body=%s", old.Code, old.Body.String())
	}
	if current := loginAttemptForTest(server, "login@example.com", "ChangedRemotePassword!", "192.0.2.10:1234"); current.Code != http.StatusOK {
		t.Fatalf("changed password status=%d body=%s", current.Code, current.Body.String())
	}

	mismatch := clients.Sub2APIIdentity{ID: 999, Email: "login@example.com", Status: "active"}
	remote.identityMu.Lock()
	remote.authOverride = &mismatch
	remote.identityMu.Unlock()
	before := server.(*controlPlaneHTTPHandler).app.loginRateLimits["login@example.com|192.0.2.11"].Count
	response := loginAttemptForTest(server, "login@example.com", "ChangedRemotePassword!", "192.0.2.11:1234")
	after := server.(*controlPlaneHTTPHandler).app.loginRateLimits["login@example.com|192.0.2.11"].Count
	if response.Code != http.StatusServiceUnavailable || before != after {
		t.Fatalf("identity mismatch status=%d body=%s failures=%d->%d", response.Code, response.Body.String(), before, after)
	}
}

func TestLoginUpstreamUnavailableIsNotCredentialFailureAndStoresNoSecrets(t *testing.T) {
	remote := newIdentityTestSub2API()
	server := newIdentityTestServer(t, remote, nil)
	if _, err := createIdentityUser(server, map[string]any{
		"email": "secretless@example.com", "accountId": "acct-secretless", "password": "RemotePasswordOnly!",
	}); err != nil {
		t.Fatal(err)
	}
	remote.identityMu.Lock()
	remote.authErr = clients.ErrSub2APIAuthUnavailable
	remote.identityMu.Unlock()
	response := loginAttemptForTest(server, "secretless@example.com", "RemotePasswordOnly!", "192.0.2.12:1234")
	app := server.(*controlPlaneHTTPHandler).app
	if response.Code != http.StatusServiceUnavailable || app.loginRateLimits["secretless@example.com|192.0.2.12"].Count != 0 {
		t.Fatalf("status=%d body=%s failures=%#v", response.Code, response.Body.String(), app.loginRateLimits)
	}
	users, _ := app.tables.ListUsers(context.Background(), true)
	sessions, _ := app.tables.ListSessions(context.Background())
	encoded, _ := json.Marshal([]any{users, sessions})
	for _, secret := range []string{"RemotePasswordOnly!", "passwordHash", "access_token", "refresh_token"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("local facts leaked %q: %s", secret, encoded)
		}
	}
}

func TestSharedIdentityMutationRoutesAreRemoved(t *testing.T) {
	server := newIdentityTestServer(t, newIdentityTestSub2API(), nil)
	operator := operatorSessionForTest(t, server)
	for _, path := range []string{
		"/api/organizations", "/api/organizations/members", "/api/organizations/members/mem-any/revoke", "/api/users/usr-any/reset-password",
	} {
		response := requestWithSession(t, server, operator, http.MethodPost, path, `{}`)
		if response.Code != http.StatusNotFound {
			t.Fatalf("POST %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestManagementStateDoesNotExposeSharedOrganizationFacts(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	response := requestWithSession(t, server, operatorSessionForTest(t, server), http.MethodGet, "/api/management/state", "")
	if response.Code != http.StatusOK {
		t.Fatalf("management state status=%d body=%s", response.Code, response.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"organization", "organizations", "memberships"} {
		if _, exists := state[key]; exists {
			t.Fatalf("management state exposed retired %s: %#v", key, state[key])
		}
	}
}

func TestManagementStateAccountDTOExcludesUserIdentityFields(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	response := requestWithSession(t, server, operatorSessionForTest(t, server), http.MethodGet, "/api/management/state", "")
	if response.Code != http.StatusOK {
		t.Fatalf("management state status=%d body=%s", response.Code, response.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	accounts, _ := state["accounts"].([]any)
	if len(accounts) == 0 {
		t.Fatal("management state has no accounts")
	}
	for _, item := range accounts {
		account := item.(map[string]any)
		for _, field := range []string{"email", "userId"} {
			if _, exists := account[field]; exists {
				t.Fatalf("account DTO exposed %s: %#v", field, account)
			}
		}
	}
}
