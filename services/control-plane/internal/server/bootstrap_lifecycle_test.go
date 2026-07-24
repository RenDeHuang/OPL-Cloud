package server

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type bootstrapIdentitySub2API struct {
	testSub2APIClient
	identity clients.Sub2APIIdentity
	err      error
	calls    int
}

func (c *bootstrapIdentitySub2API) AdminIdentity(context.Context) (clients.Sub2APIIdentity, error) {
	c.calls++
	return c.identity, c.err
}

func freshIdentityMemoryStore() *memoryTableStore {
	store := newMemoryTableStore()
	store.accounts = controlPlaneRecordSet{}
	store.users = controlPlaneRecordSet{}
	store.organizations = controlPlaneRecordSet{}
	store.memberships = controlPlaneRecordSet{}
	return store
}

func TestFreshPersistentServerBootstrapsRemoteOperatorIdentityAtomically(t *testing.T) {
	store := freshIdentityMemoryStore()
	remote := &bootstrapIdentitySub2API{
		testSub2APIClient: testSub2APIClient{charges: map[string]int64{}},
		identity:          clients.Sub2APIIdentity{ID: 91, Email: "admin@medopl.cn", Status: "active"},
	}

	_, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, remote), store)
	if err != nil {
		t.Fatal(err)
	}
	accounts, _ := store.ListAccounts(context.Background(), "")
	users, _ := store.ListUsers(context.Background(), true)
	organizations, _ := store.ListOrganizations(context.Background())
	memberships, _ := store.ListMemberships(context.Background())
	if remote.calls != 1 || len(accounts) != 1 || len(users) != 1 || len(organizations) != 1 || len(memberships) != 1 {
		t.Fatalf("calls=%d accounts=%#v users=%#v organizations=%#v memberships=%#v", remote.calls, accounts, users, organizations, memberships)
	}
	if accounts[0]["id"] != "acct-admin" || accounts[0]["ownerUserId"] != "usr-admin" || accounts[0]["sub2apiUserId"] != int64(91) ||
		users[0]["id"] != "usr-admin" || users[0]["email"] != "admin@medopl.cn" || users[0]["role"] != "admin" || users[0]["passwordHash"] != nil ||
		organizations[0]["id"] != "org-admin" || memberships[0]["id"] != "mem-admin" || memberships[0]["userId"] != "usr-admin" {
		t.Fatalf("accounts=%#v users=%#v organizations=%#v memberships=%#v", accounts, users, organizations, memberships)
	}
}

func TestFreshPostgresPersistentServerBootstrapsRemoteOperatorIdentityAtomically(t *testing.T) {
	store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
	remote := &bootstrapIdentitySub2API{
		testSub2APIClient: testSub2APIClient{charges: map[string]int64{}},
		identity:          clients.Sub2APIIdentity{ID: 91, Email: "admin@medopl.cn", Status: "active"},
	}

	if _, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, remote), store); err != nil {
		t.Fatal(err)
	}
	accounts, _ := store.ListAccounts(context.Background(), "")
	users, _ := store.ListUsers(context.Background(), true)
	organizations, _ := store.ListOrganizations(context.Background())
	memberships, _ := store.ListMemberships(context.Background())
	if remote.calls != 1 || len(accounts) != 1 || len(users) != 1 || len(organizations) != 1 || len(memberships) != 1 {
		t.Fatalf("calls=%d accounts=%#v users=%#v organizations=%#v memberships=%#v", remote.calls, accounts, users, organizations, memberships)
	}
	if accounts[0]["id"] != "acct-admin" || accounts[0]["ownerUserId"] != "usr-admin" || accounts[0]["sub2apiUserId"] != int64(91) ||
		users[0]["id"] != "usr-admin" || users[0]["email"] != "admin@medopl.cn" || users[0]["role"] != "admin" || users[0]["passwordHash"] != nil ||
		organizations[0]["id"] != "org-admin" || organizations[0]["billingAccountId"] != "acct-admin" ||
		memberships[0]["id"] != "mem-admin" || memberships[0]["accountId"] != "acct-admin" || memberships[0]["organizationId"] != "org-admin" || memberships[0]["userId"] != "usr-admin" || memberships[0]["role"] != "owner" {
		t.Fatalf("accounts=%#v users=%#v organizations=%#v memberships=%#v", accounts, users, organizations, memberships)
	}
}

func TestFreshPersistentServerFailsClosedWithoutRemoteOperatorIdentity(t *testing.T) {
	for _, remoteErr := range []error{clients.ErrSub2APIAuthUnavailable, clients.ErrSub2APIIdentityConflict} {
		t.Run(remoteErr.Error(), func(t *testing.T) {
			store := freshIdentityMemoryStore()
			remote := &bootstrapIdentitySub2API{testSub2APIClient: testSub2APIClient{charges: map[string]int64{}}, err: remoteErr}

			_, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, remote), store)
			if !errors.Is(err, remoteErr) {
				t.Fatalf("error=%v want=%v", err, remoteErr)
			}
			if len(store.accounts) != 0 || len(store.users) != 0 || len(store.organizations) != 0 || len(store.memberships) != 0 {
				t.Fatalf("failed bootstrap mutated facts: accounts=%#v users=%#v organizations=%#v memberships=%#v", store.accounts, store.users, store.organizations, store.memberships)
			}
		})
	}
}

func TestPersistentServerBootstrapsOperatorAlongsideExistingCustomer(t *testing.T) {
	store := freshIdentityMemoryStore()
	account, user, organization, membership := strictProvisionedAccountRows()
	if err := store.CreateProvisionedAccount(context.Background(), account, user, organization, membership); err != nil {
		t.Fatal(err)
	}
	remote := &bootstrapIdentitySub2API{
		testSub2APIClient: testSub2APIClient{charges: map[string]int64{}},
		identity:          clients.Sub2APIIdentity{ID: 91, Email: "admin@medopl.cn", Status: "active"},
	}

	_, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, remote), store)
	if err != nil {
		t.Fatal(err)
	}
	users, _ := store.ListUsers(context.Background(), true)
	if remote.calls != 1 || findRecord(users, "usr-admin") == nil {
		t.Fatalf("calls=%d users=%#v", remote.calls, users)
	}
}

func TestPersistentServerRejectsPartialOperatorFactsWithoutMutation(t *testing.T) {
	store := freshIdentityMemoryStore()
	store.accounts["acct-admin"] = map[string]any{"id": "acct-admin", "ownerUserId": "usr-admin", "sub2apiUserId": int64(91), "status": "active"}
	beforeAccounts := cloneStateTable(store.accounts)
	remote := &bootstrapIdentitySub2API{
		testSub2APIClient: testSub2APIClient{charges: map[string]int64{}},
		identity:          clients.Sub2APIIdentity{ID: 91, Email: "admin@medopl.cn", Status: "active"},
	}

	_, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, remote), store)
	if !errors.Is(err, errBootstrapUserIdentityConflict) {
		t.Fatalf("error=%v want=%v", err, errBootstrapUserIdentityConflict)
	}
	if remote.calls != 0 || !reflect.DeepEqual(store.accounts, beforeAccounts) || len(store.users) != 0 || len(store.organizations) != 0 || len(store.memberships) != 0 {
		t.Fatalf("calls=%d accounts=%#v users=%#v organizations=%#v memberships=%#v", remote.calls, store.accounts, store.users, store.organizations, store.memberships)
	}
}

func TestPersistentServerValidatesCompleteLegacyOperatorGraphWithoutWriting(t *testing.T) {
	store := freshIdentityMemoryStore()
	account := map[string]any{"id": "acct-admin", "ownerUserId": "usr-admin", "sub2apiUserId": int64(91), "status": "active"}
	user := map[string]any{"id": "usr-admin", "email": "admin@medopl.cn", "accountId": "acct-admin", "role": "admin", "status": "active"}
	organization := map[string]any{"id": "org-legacy-admin", "name": "Legacy operator", "billingAccountId": "acct-admin", "status": "active"}
	membership := map[string]any{"id": "mem-legacy-admin", "accountId": "acct-admin", "organizationId": "org-legacy-admin", "userId": "usr-admin", "role": "owner", "status": "active"}
	if err := store.CreateProvisionedAccount(context.Background(), account, user, organization, membership); err != nil {
		t.Fatal(err)
	}
	beforeAccounts, beforeUsers := cloneStateTable(store.accounts), cloneStateTable(store.users)
	beforeOrganizations, beforeMemberships := cloneStateTable(store.organizations), cloneStateTable(store.memberships)
	remote := &bootstrapIdentitySub2API{
		testSub2APIClient: testSub2APIClient{charges: map[string]int64{}},
		identity:          clients.Sub2APIIdentity{ID: 91, Email: "admin@medopl.cn", Status: "active"},
	}

	if _, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, remote), store); err != nil {
		t.Fatal(err)
	}
	if remote.calls != 1 || !reflect.DeepEqual(store.accounts, beforeAccounts) || !reflect.DeepEqual(store.users, beforeUsers) ||
		!reflect.DeepEqual(store.organizations, beforeOrganizations) || !reflect.DeepEqual(store.memberships, beforeMemberships) {
		t.Fatalf("calls=%d accounts=%#v users=%#v organizations=%#v memberships=%#v", remote.calls, store.accounts, store.users, store.organizations, store.memberships)
	}
}

func TestPersistentServerRejectsMalformedLocalOperatorEmailWithoutMutation(t *testing.T) {
	store := freshIdentityMemoryStore()
	account := map[string]any{"id": "acct-admin", "ownerUserId": "usr-admin", "sub2apiUserId": int64(91), "status": "active"}
	user := map[string]any{"id": "usr-admin", "email": "admin@medopl.cn", "accountId": "acct-admin", "role": "admin", "status": "active"}
	organization := map[string]any{"id": "org-admin", "name": "OPL Cloud", "billingAccountId": "acct-admin", "status": "active"}
	membership := map[string]any{"id": "mem-admin", "accountId": "acct-admin", "organizationId": "org-admin", "userId": "usr-admin", "role": "owner", "status": "active"}
	if err := store.CreateProvisionedAccount(context.Background(), account, user, organization, membership); err != nil {
		t.Fatal(err)
	}
	store.users["usr-admin"]["email"] = " Admin@medopl.cn "
	beforeAccounts, beforeUsers := cloneStateTable(store.accounts), cloneStateTable(store.users)
	beforeOrganizations, beforeMemberships := cloneStateTable(store.organizations), cloneStateTable(store.memberships)
	remote := &bootstrapIdentitySub2API{
		testSub2APIClient: testSub2APIClient{charges: map[string]int64{}},
		identity:          clients.Sub2APIIdentity{ID: 91, Email: "admin@medopl.cn", Status: "active"},
	}

	_, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, remote), store)
	if !errors.Is(err, errBootstrapUserIdentityConflict) {
		t.Fatalf("error=%v want=%v", err, errBootstrapUserIdentityConflict)
	}
	if remote.calls != 0 || !reflect.DeepEqual(store.accounts, beforeAccounts) || !reflect.DeepEqual(store.users, beforeUsers) ||
		!reflect.DeepEqual(store.organizations, beforeOrganizations) || !reflect.DeepEqual(store.memberships, beforeMemberships) {
		t.Fatalf("calls=%d accounts=%#v users=%#v organizations=%#v memberships=%#v", remote.calls, store.accounts, store.users, store.organizations, store.memberships)
	}
}

func TestPersistentServerRejectsRemoteOperatorMappingMismatchWithoutWriting(t *testing.T) {
	store := freshIdentityMemoryStore()
	account := map[string]any{"id": "acct-admin", "ownerUserId": "usr-admin", "sub2apiUserId": int64(91), "status": "active"}
	user := map[string]any{"id": "usr-admin", "email": "admin@medopl.cn", "accountId": "acct-admin", "role": "admin", "status": "active"}
	organization := map[string]any{"id": "org-admin", "name": "OPL Cloud", "billingAccountId": "acct-admin", "status": "active"}
	membership := map[string]any{"id": "mem-admin", "accountId": "acct-admin", "organizationId": "org-admin", "userId": "usr-admin", "role": "owner", "status": "active"}
	if err := store.CreateProvisionedAccount(context.Background(), account, user, organization, membership); err != nil {
		t.Fatal(err)
	}
	beforeAccounts, beforeUsers := cloneStateTable(store.accounts), cloneStateTable(store.users)
	remote := &bootstrapIdentitySub2API{
		testSub2APIClient: testSub2APIClient{charges: map[string]int64{}},
		identity:          clients.Sub2APIIdentity{ID: 92, Email: "admin@medopl.cn", Status: "active"},
	}

	_, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, remote), store)
	if !errors.Is(err, clients.ErrSub2APIIdentityConflict) {
		t.Fatalf("error=%v want=%v", err, clients.ErrSub2APIIdentityConflict)
	}
	if remote.calls != 1 || !reflect.DeepEqual(store.accounts, beforeAccounts) || !reflect.DeepEqual(store.users, beforeUsers) {
		t.Fatalf("calls=%d accounts=%#v users=%#v", remote.calls, store.accounts, store.users)
	}
}

func TestLegacyBootstrapSeedFailsClosedWithoutMutatingIdentityFacts(t *testing.T) {
	store := newMemoryTableStore()
	beforeAccounts, beforeUsers := cloneStateTable(store.accounts), cloneStateTable(store.users)
	t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"id":"usr-legacy","email":"legacy@example.com","password":"CorrectHorseBatteryStaple!","role":"owner","accountId":"acct-legacy","sub2apiUserId":114}]`)

	_, err := newControlPlaneAppWithStore(store)
	if err == nil || !strings.Contains(err.Error(), "OPL_CONSOLE_USERS_JSON is retired") {
		t.Fatalf("legacy bootstrap error = %v", err)
	}
	if !reflect.DeepEqual(store.accounts, beforeAccounts) || !reflect.DeepEqual(store.users, beforeUsers) {
		t.Fatalf("retired bootstrap mutated facts: accounts=%#v users=%#v", store.accounts, store.users)
	}
}

func TestWhitespaceLegacyBootstrapSeedFailsClosed(t *testing.T) {
	t.Setenv("OPL_CONSOLE_USERS_JSON", " \t\n")
	if _, err := bootstrapUsersFromEnv(); err == nil || !strings.Contains(err.Error(), "OPL_CONSOLE_USERS_JSON is retired") {
		t.Fatalf("legacy bootstrap error = %v", err)
	}
}
