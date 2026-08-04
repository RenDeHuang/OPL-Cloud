package server

import (
	"context"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type acceptanceBReconcileSub2APIClient struct {
	*testSub2APIClient
	users      []clients.Sub2APIUser
	keyCount   int
	adminPages int
}

func (c *acceptanceBReconcileSub2APIClient) AdminUsers(_ context.Context, query clients.Sub2APIUserPageQuery) (clients.Sub2APIUserPage, error) {
	pages := c.adminPages
	if pages == 0 {
		pages = 1
	}
	items := c.users
	if query.Page > pages {
		return clients.Sub2APIUserPage{}, clients.ErrSub2APIIdentityConflict
	}
	if query.Page > 1 {
		items = nil
	}
	return clients.Sub2APIUserPage{Items: items, Total: int64(len(c.users)), Page: query.Page, PageSize: query.PageSize, Pages: pages}, nil
}

func (c *acceptanceBReconcileSub2APIClient) AdminUserKeyCount(context.Context, int64) (int, error) {
	return c.keyCount, nil
}

func TestAcceptanceBReconcileIdentityDigestsAreDeterministic(t *testing.T) {
	const email = "reconcile@example.com"
	accountID := "acct-" + stableID("account", email)[:18]
	if got := acceptanceBAccountOperationID(email); got != acceptanceBAccountOperationID(email) {
		t.Fatalf("account operation identity is not deterministic: %q", got)
	}
	if got := acceptanceBWalletOperationID(accountID, email); got != acceptanceBWalletOperationID(accountID, email) {
		t.Fatalf("wallet operation identity is not deterministic: %q", got)
	}
	for name, value := range map[string]string{
		"customer": acceptanceBAccountDigest(email),
		"account":  acceptanceBAccountIdentityDigest(email),
		"wallet":   acceptanceBWalletIdentityDigest(accountID, email),
	} {
		if len(value) != 64 {
			t.Fatalf("%s digest length=%d", name, len(value))
		}
	}
}

func TestAcceptanceBReconcileLocalGraphDistinguishesAbsentAndComplete(t *testing.T) {
	const email = "reconcile@example.com"
	accountID := "acct-" + stableID("account", email)[:18]
	userID := "usr-" + stableID("customer", email)[:18]
	store := newMemoryTableStore()
	app := &controlPlaneServer{tables: store}
	state, _, _, err := app.acceptanceBLocalGraph(context.Background(), accountID, userID, email)
	if err != nil || state != "absent" {
		t.Fatalf("absent local graph state=%q err=%v", state, err)
	}
	mustStore(t, store.CreateProvisionedAccount(context.Background(),
		map[string]any{"id": accountID, "ownerUserId": userID, "sub2apiUserId": int64(41), "status": "active"},
		map[string]any{"id": userID, "email": email, "accountId": accountID, "role": "owner", "status": "active"},
		map[string]any{"id": "org-reconcile", "name": "Reconcile", "billingAccountId": accountID, "status": "active"},
		map[string]any{"id": "mem-reconcile", "accountId": accountID, "organizationId": "org-reconcile", "userId": userID, "role": "owner", "status": "active"},
	))
	state, _, _, err = app.acceptanceBLocalGraph(context.Background(), accountID, userID, email)
	if err != nil || state != "complete" {
		t.Fatalf("complete local graph state=%q err=%v", state, err)
	}
}

func TestAcceptanceBReconcileRemoteIdentityUsesExactEmailAndFullPages(t *testing.T) {
	const email = "reconcile@example.com"
	client := &acceptanceBReconcileSub2APIClient{
		testSub2APIClient: &testSub2APIClient{charges: map[string]int64{}},
		users:             []clients.Sub2APIUser{{ID: 41, Email: email, BalanceUSDMicros: 60_000_000, Status: "active", CreatedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)}},
	}
	state, user, err := acceptanceBRemoteIdentity(context.Background(), controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, client), email)
	if err != nil || state != "active" || user == nil || user.ID != 41 {
		t.Fatalf("remote identity state=%q user=%#v err=%v", state, user, err)
	}
	client.users = nil
	state, user, err = acceptanceBRemoteIdentity(context.Background(), controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, client), email)
	if err != nil || state != "absent" || user != nil {
		t.Fatalf("remote absent state=%q user=%#v err=%v", state, user, err)
	}
}
