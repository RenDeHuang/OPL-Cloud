package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	controlplaneent "opl-cloud/services/control-plane/ent"
)

func strictProvisionedAccountRows() (map[string]any, map[string]any, map[string]any, map[string]any) {
	user := map[string]any{"id": "usr-provisioned", "email": "owner@provisioned.example", "accountId": "acct-provisioned", "role": "owner", "status": "active"}
	account := map[string]any{"id": "acct-provisioned", "ownerUserId": user["id"], "status": "active", "sub2apiUserId": int64(73)}
	organization := map[string]any{"id": "org-provisioned", "name": "Organization acct-provisioned", "billingAccountId": "acct-provisioned", "status": "active"}
	membership := map[string]any{"id": "mem-provisioned", "accountId": "acct-provisioned", "organizationId": "org-provisioned", "userId": user["id"], "role": "owner", "status": "active"}
	return account, user, organization, membership
}

func TestMemoryIdentityFactsAreReciprocalOneToOne(t *testing.T) {
	ctx := context.Background()
	store := newMemoryTableStore()
	account, user, organization, membership := strictProvisionedAccountRows()
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveOrganization(ctx, organization); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMembership(ctx, membership); err != nil {
		t.Fatal(err)
	}

	secondUser := cloneMap(user)
	secondUser["id"], secondUser["email"] = "usr-second", "second@provisioned.example"
	secondAccount := cloneMap(account)
	secondAccount["id"] = "acct-second"
	secondOrganization := cloneMap(organization)
	secondOrganization["id"] = "org-second"
	secondMembership := cloneMap(membership)
	secondMembership["id"] = "mem-second"
	for name, attempt := range map[string]func() error{
		"second user for account":         func() error { return store.SaveUser(ctx, secondUser) },
		"owner reused by account":         func() error { return store.SaveAccount(ctx, secondAccount) },
		"second organization for account": func() error { return store.SaveOrganization(ctx, secondOrganization) },
		"second membership for account":   func() error { return store.SaveMembership(ctx, secondMembership) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := attempt(); err == nil {
				t.Fatal("conflicting 1:1 fact succeeded")
			}
		})
	}
}

func TestIdentityStoresRejectNonOwnerRole(t *testing.T) {
	for _, storeType := range []string{"memory", "ent"} {
		for _, fact := range []string{"user", "membership"} {
			for _, role := range []string{"member", "admin"} {
				t.Run(storeType+" "+fact+" "+role, func(t *testing.T) {
					var store controlPlaneTableStore = NewTestEntStateStore(t, t.TempDir()+"/role.sqlite")
					if storeType == "memory" {
						store = newMemoryTableStore()
					}
					account, user, organization, membership := strictProvisionedAccountRows()
					if err := store.CreateProvisionedAccount(context.Background(), account, user, organization, membership); err != nil {
						t.Fatal(err)
					}
					row, save := cloneMap(membership), store.SaveMembership
					if fact == "user" {
						row, save = cloneMap(user), store.SaveUser
					}
					row["role"] = role
					if err := save(context.Background(), row); !errors.Is(err, errInvalidRole) {
						t.Fatalf("%s role write error=%v, want=%v", role, err, errInvalidRole)
					}
				})
			}
		}
	}
}

func TestMemoryProvisionedAccountReplayIsIdempotentAndConflictFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := newMemoryTableStore()
	account, user, organization, membership := strictProvisionedAccountRows()
	if err := store.CreateProvisionedAccount(ctx, account, user, organization, membership); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProvisionedAccount(ctx, account, user, organization, membership); err != nil {
		t.Fatalf("matching replay: %v", err)
	}
	conflicting := cloneMap(user)
	conflicting["id"], conflicting["email"] = "usr-other", "other@provisioned.example"
	if err := store.CreateProvisionedAccount(ctx, account, conflicting, organization, membership); err == nil {
		t.Fatal("second account user succeeded")
	}
	users, _ := store.ListUsers(ctx, true)
	count := 0
	for _, row := range users {
		if row["accountId"] == "acct-provisioned" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("account users = %#v", users)
	}
}

func TestEntProvisionedAccountMatchingReplaySucceeds(t *testing.T) {
	ctx := context.Background()
	store := NewTestEntStateStore(t, t.TempDir()+"/identity-replay.sqlite").(*postgresEntStateStore)
	account, user, organization, membership := strictProvisionedAccountRows()
	if err := store.CreateProvisionedAccount(ctx, account, user, organization, membership); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProvisionedAccount(ctx, account, user, organization, membership); err != nil {
		t.Fatalf("matching replay: %v", err)
	}
	users, _ := store.ListUsers(ctx, true)
	memberships, _ := store.ListMemberships(ctx)
	if len(users) != 1 || len(memberships) != 1 {
		t.Fatalf("users=%#v memberships=%#v", users, memberships)
	}
}

func TestPostgresProvisionedAccountMatchingReplaySucceeds(t *testing.T) {
	ctx := context.Background()
	store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
	account, user, organization, membership := strictProvisionedAccountRows()
	if err := store.CreateProvisionedAccount(ctx, account, user, organization, membership); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProvisionedAccount(ctx, account, user, organization, membership); err != nil {
		t.Fatalf("matching replay: %v", err)
	}
	accounts, _ := store.ListAccounts(ctx, "")
	users, _ := store.ListUsers(ctx, true)
	organizations, _ := store.ListOrganizations(ctx)
	memberships, _ := store.ListMemberships(ctx)
	if len(accounts) != 1 || len(users) != 1 || len(organizations) != 1 || len(memberships) != 1 {
		t.Fatalf("accounts=%#v users=%#v organizations=%#v memberships=%#v", accounts, users, organizations, memberships)
	}
}

func TestPostgresIdentityDirectWritesRejectCrossAccountOwnerAndMembership(t *testing.T) {
	ctx := context.Background()
	store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
	accountA, userA, organizationA, membershipA := provisionedAccountRowsFor("acct-a", "usr-a", "org-a", "a@example.com", 71)
	accountB, userB, organizationB, membershipB := provisionedAccountRowsFor("acct-b", "usr-b", "org-b", "b@example.com", 72)
	if err := store.CreateProvisionedAccount(ctx, accountA, userA, organizationA, membershipA); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProvisionedAccount(ctx, accountB, userB, organizationB, membershipB); err != nil {
		t.Fatal(err)
	}

	crossAccountOwner := cloneMap(accountA)
	crossAccountOwner["ownerUserId"] = "usr-b"
	if err := store.SaveAccount(ctx, crossAccountOwner); err == nil {
		t.Fatal("cross-account owner write succeeded")
	}
	crossAccountMembership := cloneMap(membershipA)
	crossAccountMembership["userId"] = "usr-b"
	if err := store.SaveMembership(ctx, crossAccountMembership); err == nil {
		t.Fatal("cross-account membership write succeeded")
	}

	accounts, _ := store.ListAccounts(ctx, "acct-a")
	memberships, _ := store.ListMemberships(ctx)
	if findRecord(accounts, "acct-a")["ownerUserId"] != "usr-a" || findRecord(memberships, stringValue(membershipA["id"]))["userId"] != "usr-a" {
		t.Fatalf("rejected writes changed identity graph: accounts=%#v memberships=%#v", accounts, memberships)
	}
}

func TestEntSaveUserDoesNotPersistLocalPasswordHash(t *testing.T) {
	ctx := context.Background()
	store := NewTestEntStateStore(t, t.TempDir()+"/identity-password.sqlite").(*postgresEntStateStore)
	account, user, organization, membership := strictProvisionedAccountRows()
	if err := store.CreateProvisionedAccount(ctx, account, user, organization, membership); err != nil {
		t.Fatal(err)
	}
	user["passwordHash"] = "local-password-secret"
	if err := store.SaveUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	users, err := store.ListUsers(ctx, true)
	if err != nil || len(users) != 1 {
		t.Fatalf("users=%#v err=%v", users, err)
	}
	if _, persisted := users[0]["passwordHash"]; persisted {
		t.Fatalf("local password hash persisted: %#v", users[0])
	}
}

func TestSessionStoresRejectPreRemoteAuthorityLookupKeys(t *testing.T) {
	if key := sessionLookupKey("raw-cookie-token"); !strings.HasPrefix(key, "sub2api-sha256:") {
		t.Fatalf("session lookup key = %q", key)
	}
	for _, tc := range []struct {
		name  string
		store controlPlaneTableStore
	}{
		{name: "memory", store: newMemoryTableStore()},
		{name: "ent", store: NewTestEntStateStore(t, t.TempDir()+"/session-key.sqlite")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := map[string]any{"id": "sha256:old-local-session", "userId": "usr-admin", "csrf": "csrf", "expiresAt": "2099-01-01T00:00:00Z"}
			if err := tc.store.SaveSession(context.Background(), row); err == nil {
				t.Fatal("old session lookup key was accepted")
			}
			row["id"] = sessionLookupKey("raw-cookie-token")
			if err := tc.store.SaveSession(context.Background(), row); err != nil {
				t.Fatalf("current session lookup key: %v", err)
			}
		})
	}
}

func TestEntIdentitySchemaEnforcesOneToOneFields(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, *controlplaneent.Client) error
	}{
		{name: "account owner", run: func(ctx context.Context, client *controlplaneent.Client) error {
			_, _ = client.Account.Create().SetID("acct-one").SetOwnerUserID("usr-one").SetSub2apiUserID(71).Save(ctx)
			_, err := client.Account.Create().SetID("acct-two").SetOwnerUserID("usr-one").SetSub2apiUserID(72).Save(ctx)
			return err
		}},
		{name: "account remote identity", run: func(ctx context.Context, client *controlplaneent.Client) error {
			_, _ = client.Account.Create().SetID("acct-one").SetOwnerUserID("usr-one").SetSub2apiUserID(71).Save(ctx)
			_, err := client.Account.Create().SetID("acct-two").SetOwnerUserID("usr-two").SetSub2apiUserID(71).Save(ctx)
			return err
		}},
		{name: "user account", run: func(ctx context.Context, client *controlplaneent.Client) error {
			_, _ = client.User.Create().SetID("usr-one").SetAccountID("acct-one").SetEmail("one@example.com").Save(ctx)
			_, err := client.User.Create().SetID("usr-two").SetAccountID("acct-one").SetEmail("two@example.com").Save(ctx)
			return err
		}},
		{name: "organization account", run: func(ctx context.Context, client *controlplaneent.Client) error {
			_, _ = client.Organization.Create().SetID("org-one").SetBillingAccountID("acct-one").Save(ctx)
			_, err := client.Organization.Create().SetID("org-two").SetBillingAccountID("acct-one").Save(ctx)
			return err
		}},
		{name: "membership account", run: func(ctx context.Context, client *controlplaneent.Client) error {
			_, _ = client.Membership.Create().SetID("mem-one").SetAccountID("acct-one").SetUserID("usr-one").SetOrganizationID("org-one").Save(ctx)
			_, err := client.Membership.Create().SetID("mem-two").SetAccountID("acct-one").SetUserID("usr-two").SetOrganizationID("org-two").Save(ctx)
			return err
		}},
		{name: "membership user", run: func(ctx context.Context, client *controlplaneent.Client) error {
			_, _ = client.Membership.Create().SetID("mem-one").SetAccountID("acct-one").SetUserID("usr-one").SetOrganizationID("org-one").Save(ctx)
			_, err := client.Membership.Create().SetID("mem-two").SetAccountID("acct-two").SetUserID("usr-one").SetOrganizationID("org-two").Save(ctx)
			return err
		}},
		{name: "membership organization", run: func(ctx context.Context, client *controlplaneent.Client) error {
			_, _ = client.Membership.Create().SetID("mem-one").SetAccountID("acct-one").SetUserID("usr-one").SetOrganizationID("org-one").Save(ctx)
			_, err := client.Membership.Create().SetID("mem-two").SetAccountID("acct-two").SetUserID("usr-two").SetOrganizationID("org-one").Save(ctx)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewTestEntStateStore(t, t.TempDir()+"/identity-schema.sqlite").(*postgresEntStateStore)
			if err := test.run(context.Background(), store.client); err == nil {
				t.Fatal("duplicate one-to-one identity field succeeded")
			}
		})
	}
}
