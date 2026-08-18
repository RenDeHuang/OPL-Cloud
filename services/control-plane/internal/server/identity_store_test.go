package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	controlplaneent "opl-cloud/services/control-plane/ent"
)

func strictProvisionedAccountRows() (map[string]any, map[string]any) {
	user := map[string]any{"id": "usr-provisioned", "email": "owner@provisioned.example", "accountId": "acct-provisioned", "role": "owner", "status": "active"}
	account := map[string]any{"id": "acct-provisioned", "ownerUserId": user["id"], "status": "active", "sub2apiUserId": int64(73), "workspacePurchaseEnabled": true}
	return account, user
}

func TestMemoryIdentityFactsAreReciprocalOneToOne(t *testing.T) {
	ctx := context.Background()
	store := newMemoryTableStore()
	account, user := strictProvisionedAccountRows()
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUser(ctx, user); err != nil {
		t.Fatal(err)
	}

	secondUser := cloneMap(user)
	secondUser["id"], secondUser["email"] = "usr-second", "second@provisioned.example"
	secondAccount := cloneMap(account)
	secondAccount["id"] = "acct-second"
	for name, attempt := range map[string]func() error{
		"second user for account": func() error { return store.SaveUser(ctx, secondUser) },
		"owner reused by account": func() error { return store.SaveAccount(ctx, secondAccount) },
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
		for _, role := range []string{"member", "admin"} {
			t.Run(storeType+" "+role, func(t *testing.T) {
				var store controlPlaneTableStore = NewTestEntStateStore(t, t.TempDir()+"/role.sqlite")
				if storeType == "memory" {
					store = newMemoryTableStore()
				}
				account, user := strictProvisionedAccountRows()
				if err := store.CreateProvisionedAccount(context.Background(), account, user); err != nil {
					t.Fatal(err)
				}
				row := cloneMap(user)
				row["role"] = role
				if err := store.SaveUser(context.Background(), row); !errors.Is(err, errInvalidRole) {
					t.Fatalf("%s role write error=%v, want=%v", role, err, errInvalidRole)
				}
			})
		}
	}
}

func TestMemoryProvisionedAccountReplayIsIdempotentAndConflictFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := newMemoryTableStore()
	account, user := strictProvisionedAccountRows()
	if err := store.CreateProvisionedAccount(ctx, account, user); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProvisionedAccount(ctx, account, user); err != nil {
		t.Fatalf("matching replay: %v", err)
	}
	conflicting := cloneMap(user)
	conflicting["id"], conflicting["email"] = "usr-other", "other@provisioned.example"
	if err := store.CreateProvisionedAccount(ctx, account, conflicting); err == nil {
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

func TestWorkspacePurchaseEligibilityAccountAndAuditCommitAtomically(t *testing.T) {
	for _, storeType := range []string{"memory", "sqlite"} {
		t.Run(storeType, func(t *testing.T) {
			var store controlPlaneTableStore = newMemoryTableStore()
			if storeType == "sqlite" {
				store = NewTestEntStateStore(t, t.TempDir()+"/eligibility.sqlite").(*postgresEntStateStore)
			}
			account, user := strictProvisionedAccountRows()
			if err := store.CreateProvisionedAccount(context.Background(), account, user); err != nil {
				t.Fatal(err)
			}
			audit := map[string]any{
				"id": "audit-eligibility", "actorUserId": "usr-admin", "actorAccountId": "acct-admin",
				"targetAccountId": "acct-provisioned", "action": "account.workspace_purchase.revoke",
				"resourceKind": "account", "resourceId": "acct-provisioned", "result": "succeeded",
				"after": map[string]any{"workspacePurchaseEnabled": false, "reason": "gateway_only_scope"},
			}
			mutation := workspacePurchaseEligibilityMutation{AccountID: "acct-provisioned", Enabled: false, AuditEvent: audit}
			updated, err := store.ApplyWorkspacePurchaseEligibility(context.Background(), mutation)
			if err != nil || workspacePurchaseEnabled(updated) {
				t.Fatalf("eligibility update = %#v err=%v", updated, err)
			}
			replayed, err := store.ApplyWorkspacePurchaseEligibility(context.Background(), mutation)
			if err != nil || workspacePurchaseEnabled(replayed) {
				t.Fatalf("eligibility replay = %#v err=%v", replayed, err)
			}
			audits, err := store.ListAuditEvents(context.Background(), "acct-provisioned")
			if err != nil || len(audits) != 1 || workspacePurchaseEnabled(mapField(audits[0], "before")) != true {
				t.Fatalf("eligibility audits = %#v err=%v", audits, err)
			}
			conflict := mutation
			conflict.AuditEvent = cloneMap(audit)
			conflict.AuditEvent["after"] = map[string]any{"workspacePurchaseEnabled": false, "reason": "different_reason"}
			if _, err := store.ApplyWorkspacePurchaseEligibility(context.Background(), conflict); !errors.Is(err, errIdempotencyConflict) {
				t.Fatalf("eligibility conflict err=%v", err)
			}
		})
	}
}

func TestEntProvisionedAccountMatchingReplaySucceeds(t *testing.T) {
	ctx := context.Background()
	store := NewTestEntStateStore(t, t.TempDir()+"/identity-replay.sqlite").(*postgresEntStateStore)
	account, user := strictProvisionedAccountRows()
	if err := store.CreateProvisionedAccount(ctx, account, user); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProvisionedAccount(ctx, account, user); err != nil {
		t.Fatalf("matching replay: %v", err)
	}
	users, _ := store.ListUsers(ctx, true)
	if len(users) != 1 {
		t.Fatalf("users=%#v", users)
	}
}

func TestPostgresProvisionedAccountMatchingReplaySucceeds(t *testing.T) {
	ctx := context.Background()
	store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
	account, user := strictProvisionedAccountRows()
	if err := store.CreateProvisionedAccount(ctx, account, user); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProvisionedAccount(ctx, account, user); err != nil {
		t.Fatalf("matching replay: %v", err)
	}
	accounts, _ := store.ListAccounts(ctx, "")
	users, _ := store.ListUsers(ctx, true)
	if len(accounts) != 1 || len(users) != 1 {
		t.Fatalf("accounts=%#v users=%#v", accounts, users)
	}
}

func TestPostgresIdentityDirectWritesRejectCrossAccountOwner(t *testing.T) {
	ctx := context.Background()
	store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
	accountA, userA := provisionedAccountRowsFor("acct-a", "usr-a", "a@example.com", 71)
	accountB, userB := provisionedAccountRowsFor("acct-b", "usr-b", "b@example.com", 72)
	if err := store.CreateProvisionedAccount(ctx, accountA, userA); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProvisionedAccount(ctx, accountB, userB); err != nil {
		t.Fatal(err)
	}

	crossAccountOwner := cloneMap(accountA)
	crossAccountOwner["ownerUserId"] = "usr-b"
	if err := store.SaveAccount(ctx, crossAccountOwner); err == nil {
		t.Fatal("cross-account owner write succeeded")
	}

	accounts, _ := store.ListAccounts(ctx, "acct-a")
	if findRecord(accounts, "acct-a")["ownerUserId"] != "usr-a" {
		t.Fatalf("rejected write changed identity graph: accounts=%#v", accounts)
	}
}

func TestEntSaveUserDoesNotPersistLocalPasswordHash(t *testing.T) {
	ctx := context.Background()
	store := NewTestEntStateStore(t, t.TempDir()+"/identity-password.sqlite").(*postgresEntStateStore)
	account, user := strictProvisionedAccountRows()
	if err := store.CreateProvisionedAccount(ctx, account, user); err != nil {
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

func TestIdentityStoresProvideEquivalentPointLookups(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store controlPlaneTableStore
	}{
		{name: "memory", store: newMemoryTableStore()},
		{name: "ent", store: NewTestEntStateStore(t, t.TempDir()+"/identity-point-lookups.sqlite")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			account, user := strictProvisionedAccountRows()
			if err := tc.store.CreateProvisionedAccount(ctx, account, user); err != nil {
				t.Fatal(err)
			}
			sessionID := sessionLookupKey("point-lookup-session")
			if err := tc.store.SaveSession(ctx, map[string]any{
				"id": sessionID, "userId": user["id"], "csrf": "csrf", "expiresAt": "2099-01-01T00:00:00Z",
			}); err != nil {
				t.Fatal(err)
			}

			foundUser, ok, err := tc.store.GetUserByEmail(ctx, "owner@provisioned.example", false)
			if err != nil || !ok || foundUser["id"] != user["id"] {
				t.Fatalf("user by email=%#v ok=%v err=%v", foundUser, ok, err)
			}
			foundSession, ok, err := tc.store.GetSession(ctx, sessionID)
			if err != nil || !ok || foundSession["userId"] != user["id"] {
				t.Fatalf("session by id=%#v ok=%v err=%v", foundSession, ok, err)
			}
			foundAccount, ok, err := tc.store.GetAccount(ctx, stringValue(account["id"]))
			if err != nil || !ok || foundAccount["ownerUserId"] != user["id"] {
				t.Fatalf("account by id=%#v ok=%v err=%v", foundAccount, ok, err)
			}
			sessions, err := tc.store.ListSessionsByUser(ctx, stringValue(user["id"]))
			if err != nil || len(sessions) != 1 || sessions[sessionID] == nil {
				t.Fatalf("sessions by user=%#v err=%v", sessions, err)
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
