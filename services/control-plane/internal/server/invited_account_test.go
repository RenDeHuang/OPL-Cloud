package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestInvitedAccountCreatesAccountUserOrganizationAndOwnerMembership(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	handler := server.(*controlPlaneHTTPHandler)
	user, err := handler.app.createUser(context.Background(), handler.service, map[string]any{
		"email": "owner@invite.example", "accountId": "acct-invite", "password": "CorrectHorseBatteryStaple!",
	})
	if err != nil {
		t.Fatal(err)
	}
	app := handler.app
	accounts, _ := app.tables.ListAccounts(context.Background(), "acct-invite")
	organizations, _ := app.tables.ListOrganizations(context.Background())
	memberships, _ := app.tables.ListMemberships(context.Background())
	organizationID := "org-" + stableID("account", "acct-invite")[:18]
	organization := findRecord(organizations, organizationID)

	if account := findRecord(accounts, "acct-invite"); account == nil || int64(numberField(account, "sub2apiUserId", 0)) != 41 {
		t.Fatalf("invited account = %#v", account)
	}
	if user["accountId"] != "acct-invite" || user["role"] != "owner" {
		t.Fatalf("invited user = %#v", user)
	}
	if organization == nil || organization["billingAccountId"] != "acct-invite" {
		t.Fatalf("invited organization = %#v", organization)
	}
	membership := findRecord(memberships, "mem-"+stableID(organizationID, stringValue(user["id"]))[:18])
	if membership == nil || membership["accountId"] != "acct-invite" || membership["userId"] != user["id"] || membership["role"] != "owner" || membership["status"] != "active" {
		t.Fatalf("invited membership = %#v", membership)
	}
}

func TestInvitedAccountDefaultsRoleOnlyWhenOmitted(t *testing.T) {
	for index, test := range []struct {
		name string
		role any
	}{
		{name: "null", role: nil},
		{name: "number", role: 7},
		{name: "blank", role: "   "},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
			handler := server.(*controlPlaneHTTPHandler)
			accountID := fmt.Sprintf("acct-role-%d", index)
			_, err := handler.app.createUser(context.Background(), handler.service, map[string]any{
				"email": fmt.Sprintf("role-%d@invite.example", index), "accountId": accountID,
				"password": "CorrectHorseBatteryStaple!", "role": test.role,
			})

			if !errors.Is(err, errInvalidRole) {
				t.Fatalf("error=%v, want invalid role", err)
			}
			accounts, _ := handler.app.tables.ListAccounts(context.Background(), accountID)
			if len(accounts) != 0 {
				t.Fatalf("invalid role created account: %#v", accounts)
			}
		})
	}
}

func TestInvitedAccountsUseDistinctDefaultOrganizations(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	handler := server.(*controlPlaneHTTPHandler)
	for _, input := range []map[string]any{
		{"email": "prefixed@invite.example", "accountId": "acct-team", "password": "CorrectHorseBatteryStaple!"},
		{"email": "plain@invite.example", "accountId": "team", "password": "CorrectHorseBatteryStaple!"},
	} {
		if _, err := handler.app.createUser(context.Background(), handler.service, input); err != nil {
			t.Fatal(err)
		}
	}

	organizations, err := handler.app.tables.ListOrganizations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var prefixed, plain map[string]any
	for _, organization := range organizations {
		switch organization["billingAccountId"] {
		case "acct-team":
			prefixed = organization
		case "team":
			plain = organization
		}
	}
	if prefixed == nil || plain == nil || prefixed["id"] == plain["id"] {
		t.Fatalf("default Organizations collided: prefixed=%#v plain=%#v all=%#v", prefixed, plain, organizations)
	}
}

func TestMemoryInvitedAccountRollsBackEveryValidationStage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seed    func(*testing.T, *memoryTableStore)
		mutate  func(map[string]any, map[string]any, map[string]any, map[string]any)
		wantErr error
	}{
		{
			name: "account mapping",
			seed: func(t *testing.T, store *memoryTableStore) {
				account, user, organization, membership := invitedAccountRowsFor("acct-existing", "usr-existing", "org-existing", "existing@invite.example", 73)
				mustStore(t, store.CreateInvitedAccount(context.Background(), account, user, organization, membership))
			},
			wantErr: errSub2APIAccountMappingConflict,
		},
		{
			name: "normalized user email",
			seed: func(t *testing.T, store *memoryTableStore) {
				account, user, organization, membership := invitedAccountRowsFor("acct-existing", "usr-existing", "org-existing", "owner@invite.example", 74)
				mustStore(t, store.CreateInvitedAccount(context.Background(), account, user, organization, membership))
			},
			mutate: func(_ map[string]any, user, _, _ map[string]any) {
				user["email"] = " OWNER@INVITE.EXAMPLE "
			},
			wantErr: errUserExists,
		},
		{
			name: "organization billing account",
			seed: func(t *testing.T, store *memoryTableStore) {
				account, user, organization, membership := invitedAccountRowsFor("acct-existing", "usr-existing", "org-invite", "existing@invite.example", 74)
				mustStore(t, store.CreateInvitedAccount(context.Background(), account, user, organization, membership))
			},
			wantErr: errMembershipAccountMismatch,
		},
		{
			name: "membership relationship",
			mutate: func(_, _, _, membership map[string]any) {
				membership["userId"] = "usr-other"
			},
			wantErr: errMembershipUserNotFound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryTableStore()
			if tc.seed != nil {
				tc.seed(t, store)
			}
			account, user, organization, membership := invitedAccountRows()
			if tc.mutate != nil {
				tc.mutate(account, user, organization, membership)
			}
			before := []controlPlaneRecordSet{
				cloneStateTable(store.accounts), cloneStateTable(store.users),
				cloneStateTable(store.organizations), cloneStateTable(store.memberships),
			}

			err := store.CreateInvitedAccount(context.Background(), account, user, organization, membership)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("CreateInvitedAccount error = %v, want %v", err, tc.wantErr)
			}
			after := []controlPlaneRecordSet{store.accounts, store.users, store.organizations, store.memberships}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("partial invited account rows remain: before=%#v after=%#v", before, after)
			}
		})
	}
}

func invitedAccountRows() (map[string]any, map[string]any, map[string]any, map[string]any) {
	return invitedAccountRowsFor("acct-invite", "usr-invite", "org-invite", "owner@invite.example", 73)
}

func invitedAccountRowsFor(accountID, userID, organizationID, email string, sub2APIUserID int64) (map[string]any, map[string]any, map[string]any, map[string]any) {
	account := map[string]any{"id": accountID, "ownerUserId": userID, "status": "active", "sub2apiUserId": sub2APIUserID}
	user := map[string]any{"id": userID, "email": email, "accountId": accountID, "role": "owner", "status": "active"}
	organization := map[string]any{"id": organizationID, "name": "Organization " + accountID, "billingAccountId": accountID, "status": "active"}
	membership := map[string]any{"id": "mem-" + stableID(organizationID, userID)[:12], "accountId": accountID, "organizationId": organizationID, "userId": userID, "role": "owner", "status": "active"}
	return account, user, organization, membership
}

func TestEntInvitedAccountRollsBackOnMembershipInsertError(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/invited-rollback.sqlite"
	store := NewTestEntStateStore(t, path).(*postgresEntStateStore)
	db, err := sql.Open("sqlite3", path+"?_fk=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TRIGGER fail_invited_membership BEFORE INSERT ON control_plane_memberships BEGIN SELECT RAISE(ABORT, 'membership insert failed'); END`); err != nil {
		t.Fatal(err)
	}
	account, user, organization, membership := invitedAccountRows()

	if err := store.CreateInvitedAccount(ctx, account, user, organization, membership); err == nil {
		t.Fatal("CreateInvitedAccount error = nil")
	}
	accounts, _ := store.ListAccounts(ctx, "acct-invite")
	organizations, _ := store.ListOrganizations(ctx)
	users, _ := store.ListUsers(ctx, true)
	memberships, _ := store.ListMemberships(ctx)
	if len(accounts) != 0 || findRecord(organizations, "org-invite") != nil || findRecord(users, "usr-invite") != nil || findRecord(memberships, stringValue(membership["id"])) != nil {
		t.Fatalf("partial Ent invite survived rollback: accounts=%#v organizations=%#v users=%#v memberships=%#v", accounts, organizations, users, memberships)
	}
}

func TestPostgresInvitedAccountConcurrentReplayOrConflict(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		conflicting                   bool
		wantSucceeded, wantConflicted int
	}{
		{name: "matching replay", wantSucceeded: 2},
		{name: "different owner", conflicting: true, wantSucceeded: 1, wantConflicted: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, db := newPostgresWorkspaceRenewalStoreWithDB(t)
			if _, err := db.Exec(`
				CREATE FUNCTION delay_invited_account_insert() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN
					PERFORM pg_sleep(0.2);
					RETURN NEW;
				END
				$$;
				CREATE TRIGGER delay_invited_account_insert BEFORE INSERT ON control_plane_accounts
				FOR EACH ROW EXECUTE FUNCTION delay_invited_account_insert();
			`); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			organization := map[string]any{"id": "org-new-invite", "name": "Organization acct-new-invite", "billingAccountId": "acct-new-invite", "status": "active"}
			firstUser := map[string]any{"id": "usr-new-invite-one", "email": "one@new-invite.example", "accountId": "acct-new-invite", "role": "owner", "status": "active"}
			firstAccount := map[string]any{"id": "acct-new-invite", "ownerUserId": firstUser["id"], "status": "active", "sub2apiUserId": int64(74)}
			firstMembership := map[string]any{"id": "mem-new-invite-one", "accountId": "acct-new-invite", "organizationId": "org-new-invite", "userId": firstUser["id"], "role": "owner", "status": "active"}
			secondAccount, secondUser, secondMembership := cloneMap(firstAccount), cloneMap(firstUser), cloneMap(firstMembership)
			if tc.conflicting {
				secondUser["id"], secondUser["email"] = "usr-new-invite-two", "two@new-invite.example"
				secondAccount["ownerUserId"] = secondUser["id"]
				secondMembership["id"], secondMembership["userId"] = "mem-new-invite-two", secondUser["id"]
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for _, invite := range [][3]map[string]any{{firstAccount, firstUser, firstMembership}, {secondAccount, secondUser, secondMembership}} {
				go func(account, user, membership map[string]any) {
					<-start
					results <- store.CreateInvitedAccount(ctx, account, user, organization, membership)
				}(invite[0], invite[1], invite[2])
			}
			close(start)
			succeeded, conflicted := 0, 0
			for range 2 {
				err := <-results
				if err == nil {
					succeeded++
				} else if errors.Is(err, errSub2APIAccountMappingConflict) {
					conflicted++
				} else {
					t.Fatalf("concurrent new account invite: %v", err)
				}
			}
			accounts, _ := store.ListAccounts(ctx, "acct-new-invite")
			organizations, _ := store.ListOrganizations(ctx)
			users, _ := store.ListUsers(ctx, true)
			memberships, _ := store.ListMemberships(ctx)
			accountUsers, accountMemberships := 0, 0
			for _, user := range users {
				if user["accountId"] == "acct-new-invite" {
					accountUsers++
				}
			}
			for _, membership := range memberships {
				if membership["accountId"] == "acct-new-invite" {
					accountMemberships++
				}
			}
			if succeeded != tc.wantSucceeded || conflicted != tc.wantConflicted || len(accounts) != 1 || findRecord(organizations, "org-new-invite") == nil || accountUsers != 1 || accountMemberships != 1 {
				t.Fatalf("new account race succeeded=%d conflicted=%d accounts=%#v organizations=%#v users=%#v memberships=%#v", succeeded, conflicted, accounts, organizations, users, memberships)
			}
		})
	}
}

func TestEntUserLifecycleRollsBackAllFacts(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/user-lifecycle-rollback.sqlite"
	store := NewTestEntStateStore(t, path).(*postgresEntStateStore)
	account, user, organization, membership := invitedAccountRowsFor("acct-lifecycle", "usr-lifecycle", "org-lifecycle", "lifecycle@example.com", 113)
	mustStore(t, store.CreateInvitedAccount(ctx, account, user, organization, membership))
	sessionID := sessionLookupKey("session-lifecycle")
	mustStore(t, store.SaveSession(ctx, map[string]any{"id": sessionID, "userId": "usr-lifecycle", "csrf": "csrf", "expiresAt": "2099-01-01T00:00:00Z"}))
	mustStore(t, store.SaveCompute(ctx, map[string]any{"id": "compute-lifecycle", "accountId": "acct-lifecycle", "autoRenew": true}))
	mustStore(t, store.SaveStorage(ctx, map[string]any{"id": "storage-lifecycle", "accountId": "acct-lifecycle", "autoRenew": true}))
	db, err := sql.Open("sqlite3", path+"?_fk=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TRIGGER fail_lifecycle_storage BEFORE UPDATE ON control_plane_storage_volumes BEGIN SELECT RAISE(ABORT, 'storage update failed'); END`); err != nil {
		t.Fatal(err)
	}
	user["status"] = "disabled"

	if err := store.ApplyUserLifecycle(ctx, user); err == nil {
		t.Fatal("ApplyUserLifecycle error = nil")
	}
	users, _ := store.ListUsers(ctx, true)
	sessions, _ := store.ListSessions(ctx)
	computes, _ := store.ListComputes(ctx, "acct-lifecycle")
	storages, _ := store.ListStorages(ctx, "acct-lifecycle")
	if findRecord(users, "usr-lifecycle")["status"] != "active" || sessions[sessionID] == nil || findRecord(computes, "compute-lifecycle")["autoRenew"] != true || findRecord(storages, "storage-lifecycle")["autoRenew"] != true {
		t.Fatalf("partial lifecycle survived rollback: users=%#v sessions=%#v computes=%#v storages=%#v", users, sessions, computes, storages)
	}
}
