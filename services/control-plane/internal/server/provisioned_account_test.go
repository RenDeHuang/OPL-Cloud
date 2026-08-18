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

func TestProvisionedAccountCreatesAccountAndOwnerUser(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	handler := server.(*controlPlaneHTTPHandler)
	user, err := handler.app.createUser(context.Background(), handler.service, map[string]any{
		"email": "owner@provisioned.example", "accountId": "acct-provisioned", "password": "CorrectHorseBatteryStaple!",
	})
	if err != nil {
		t.Fatal(err)
	}
	app := handler.app
	accounts, _ := app.tables.ListAccounts(context.Background(), "acct-provisioned")

	if account := findRecord(accounts, "acct-provisioned"); account == nil || int64(numberField(account, "sub2apiUserId", 0)) != 41 {
		t.Fatalf("provisioned account = %#v", account)
	}
	if user["accountId"] != "acct-provisioned" || user["role"] != "owner" {
		t.Fatalf("provisioned user = %#v", user)
	}
}

func TestProvisionedAccountDefaultsRoleOnlyWhenOmitted(t *testing.T) {
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
				"email": fmt.Sprintf("role-%d@provisioned.example", index), "accountId": accountID,
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

func TestProvisionedAccountsKeepDistinctAccountIDs(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	handler := server.(*controlPlaneHTTPHandler)
	for _, input := range []map[string]any{
		{"email": "prefixed@provisioned.example", "accountId": "acct-team", "password": "CorrectHorseBatteryStaple!"},
		{"email": "plain@provisioned.example", "accountId": "team", "password": "CorrectHorseBatteryStaple!"},
	} {
		if _, err := handler.app.createUser(context.Background(), handler.service, input); err != nil {
			t.Fatal(err)
		}
	}

	accounts, err := handler.app.tables.ListAccounts(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if findRecord(accounts, "acct-team") == nil || findRecord(accounts, "team") == nil {
		t.Fatalf("provisioned accounts missing: %#v", accounts)
	}
}

func TestMemoryProvisionedAccountRollsBackEveryValidationStage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seed    func(*testing.T, *memoryTableStore)
		mutate  func(map[string]any, map[string]any)
		wantErr error
	}{
		{
			name: "account mapping",
			seed: func(t *testing.T, store *memoryTableStore) {
				account, user := provisionedAccountRowsFor("acct-existing", "usr-existing", "existing@provisioned.example", 73)
				mustStore(t, store.CreateProvisionedAccount(context.Background(), account, user))
			},
			wantErr: errSub2APIAccountMappingConflict,
		},
		{
			name: "normalized user email",
			seed: func(t *testing.T, store *memoryTableStore) {
				account, user := provisionedAccountRowsFor("acct-existing", "usr-existing", "owner@provisioned.example", 74)
				mustStore(t, store.CreateProvisionedAccount(context.Background(), account, user))
			},
			mutate: func(_, user map[string]any) {
				user["email"] = " OWNER@PROVISIONED.EXAMPLE "
			},
			wantErr: errUserExists,
		},
		{
			name: "account owner relationship",
			mutate: func(account, _ map[string]any) {
				account["ownerUserId"] = "usr-other"
			},
			wantErr: errInvalidAccountID,
		},
		{
			name: "user account relationship",
			mutate: func(_, user map[string]any) {
				user["accountId"] = "acct-other"
			},
			wantErr: errInvalidEmail,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryTableStore()
			if tc.seed != nil {
				tc.seed(t, store)
			}
			account, user := provisionedAccountRows()
			if tc.mutate != nil {
				tc.mutate(account, user)
			}
			before := []controlPlaneRecordSet{
				cloneStateTable(store.accounts), cloneStateTable(store.users),
			}

			err := store.CreateProvisionedAccount(context.Background(), account, user)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("CreateProvisionedAccount error = %v, want %v", err, tc.wantErr)
			}
			after := []controlPlaneRecordSet{store.accounts, store.users}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("partial provisioned account rows remain: before=%#v after=%#v", before, after)
			}
		})
	}
}

func provisionedAccountRows() (map[string]any, map[string]any) {
	return provisionedAccountRowsFor("acct-provisioned", "usr-provisioned", "owner@provisioned.example", 73)
}

func provisionedAccountRowsFor(accountID, userID, email string, sub2APIUserID int64) (map[string]any, map[string]any) {
	account := map[string]any{"id": accountID, "ownerUserId": userID, "status": "active", "sub2apiUserId": sub2APIUserID}
	user := map[string]any{"id": userID, "email": email, "accountId": accountID, "role": "owner", "status": "active"}
	return account, user
}

func TestEntProvisionedAccountRollsBackOnUserInsertError(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/provisioned-rollback.sqlite"
	store := NewTestEntStateStore(t, path).(*postgresEntStateStore)
	db, err := sql.Open("sqlite3", path+"?_fk=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TRIGGER fail_provisioned_user BEFORE INSERT ON control_plane_users BEGIN SELECT RAISE(ABORT, 'user insert failed'); END`); err != nil {
		t.Fatal(err)
	}
	account, user := provisionedAccountRows()

	if err := store.CreateProvisionedAccount(ctx, account, user); err == nil {
		t.Fatal("CreateProvisionedAccount error = nil")
	}
	accounts, _ := store.ListAccounts(ctx, "acct-provisioned")
	users, _ := store.ListUsers(ctx, true)
	if len(accounts) != 0 || findRecord(users, "usr-provisioned") != nil {
		t.Fatalf("partial Ent provisioned survived rollback: accounts=%#v users=%#v", accounts, users)
	}
}

func TestPostgresProvisionedAccountConcurrentReplayOrConflict(t *testing.T) {
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
				CREATE FUNCTION delay_provisioned_account_insert() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN
					PERFORM pg_sleep(0.2);
					RETURN NEW;
				END
				$$;
				CREATE TRIGGER delay_provisioned_account_insert BEFORE INSERT ON control_plane_accounts
				FOR EACH ROW EXECUTE FUNCTION delay_provisioned_account_insert();
			`); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			firstUser := map[string]any{"id": "usr-new-provisioned-one", "email": "one@new-provisioned.example", "accountId": "acct-new-provisioned", "role": "owner", "status": "active"}
			firstAccount := map[string]any{"id": "acct-new-provisioned", "ownerUserId": firstUser["id"], "status": "active", "sub2apiUserId": int64(74)}
			secondAccount, secondUser := cloneMap(firstAccount), cloneMap(firstUser)
			if tc.conflicting {
				secondUser["id"], secondUser["email"] = "usr-new-provisioned-two", "two@new-provisioned.example"
				secondAccount["ownerUserId"] = secondUser["id"]
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for _, provision := range [][2]map[string]any{{firstAccount, firstUser}, {secondAccount, secondUser}} {
				go func(account, user map[string]any) {
					<-start
					results <- store.CreateProvisionedAccount(ctx, account, user)
				}(provision[0], provision[1])
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
					t.Fatalf("concurrent new account provision: %v", err)
				}
			}
			accounts, _ := store.ListAccounts(ctx, "acct-new-provisioned")
			users, _ := store.ListUsers(ctx, true)
			accountUsers := 0
			for _, user := range users {
				if user["accountId"] == "acct-new-provisioned" {
					accountUsers++
				}
			}
			if succeeded != tc.wantSucceeded || conflicted != tc.wantConflicted || len(accounts) != 1 || accountUsers != 1 {
				t.Fatalf("new account race succeeded=%d conflicted=%d accounts=%#v users=%#v", succeeded, conflicted, accounts, users)
			}
		})
	}
}

func TestEntUserLifecycleRollsBackAllFacts(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/user-lifecycle-rollback.sqlite"
	store := NewTestEntStateStore(t, path).(*postgresEntStateStore)
	account, user := provisionedAccountRowsFor("acct-lifecycle", "usr-lifecycle", "lifecycle@example.com", 113)
	mustStore(t, store.CreateProvisionedAccount(ctx, account, user))
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
