package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type acceptanceBReconcileSub2APIClient struct {
	*testSub2APIClient
	users             []clients.Sub2APIUser
	keyCount          int
	adminPages        int
	workspaceKeys     []clients.Sub2APIWorkspaceKey
	workspaceKeysErr  error
	history           map[string]clients.Sub2APIBalanceHistoryEntry
	historyErr        error
	historyLookupCode []string
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

func (c *acceptanceBReconcileSub2APIClient) WorkspaceKeysForConvergence(_ context.Context, _ int64, _ string) ([]clients.Sub2APIWorkspaceKey, error) {
	return c.workspaceKeys, c.workspaceKeysErr
}

func (c *acceptanceBReconcileSub2APIClient) FinancialBalanceHistoryByCodes(_ context.Context, _ int64, codes []string) (map[string]clients.Sub2APIBalanceHistoryEntry, error) {
	c.historyLookupCode = append([]string(nil), codes...)
	if c.historyErr != nil {
		return nil, c.historyErr
	}
	matches := map[string]clients.Sub2APIBalanceHistoryEntry{}
	for _, code := range codes {
		if entry, ok := c.history[code]; ok {
			matches[code] = entry
		}
	}
	return matches, nil
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

type acceptanceBReceiptListLedger struct {
	fakeLedgerClient
	query    clients.ReceiptQuery
	receipts []clients.Receipt
	err      error
}

func (l *acceptanceBReceiptListLedger) ListReceipts(_ context.Context, query clients.ReceiptQuery) (clients.ReceiptPage, error) {
	l.query = query
	if l.err != nil {
		return clients.ReceiptPage{}, l.err
	}
	receipts := append([]clients.Receipt(nil), l.receipts...)
	if query.TypePrefix != "" {
		filtered := receipts[:0]
		for _, receipt := range receipts {
			if strings.HasPrefix(receipt.Type, query.TypePrefix) {
				filtered = append(filtered, receipt)
			}
		}
		receipts = filtered
	}
	return clients.ReceiptPage{Receipts: receipts}, nil
}

func TestAcceptanceBBillingReceiptCountFiltersWalletAdjustmentReceipts(t *testing.T) {
	ledger := &acceptanceBReceiptListLedger{receipts: []clients.Receipt{
		{ReceiptInput: clients.ReceiptInput{Type: "gateway.wallet_adjustment.v1"}},
		{ReceiptInput: clients.ReceiptInput{Type: "billing.workspace_purchased.v1"}},
	}}
	service := controlplane.NewService(ledger, &fakeFabricClient{}, &acceptanceBReconcileSub2APIClient{})

	count, err := acceptanceBBillingReceiptCount(context.Background(), service, "acct-reconcile")
	if err != nil {
		t.Fatalf("billing receipt count: %v", err)
	}
	if count != 1 {
		t.Fatalf("billing receipt count=%d, want 1", count)
	}
	if ledger.query.TypePrefix != "billing." {
		t.Fatalf("receipt query type prefix=%q, want billing.", ledger.query.TypePrefix)
	}
}

type acceptanceBAccountReconcileFixture struct {
	t          *testing.T
	email      string
	accountID  string
	userID     string
	approval   productionAcceptanceBApproval
	app        *controlPlaneServer
	store      *memoryTableStore
	sub2API    *acceptanceBReconcileSub2APIClient
	ledger     *acceptanceBReceiptListLedger
	service    *controlplane.Service
	redeemCode string
}

func newAcceptanceBAccountReconcileFixture(t *testing.T) *acceptanceBAccountReconcileFixture {
	t.Helper()
	configureProductionAcceptanceBEnvironment(t)
	t.Setenv("NODE_ENV", "production")
	email := "reconcile@example.com"
	accountID := "acct-" + stableID("account", email)[:18]
	userID := "usr-" + stableID("customer", email)[:18]
	approvalFixture := canonicalProductionAcceptanceBApproval(t)
	approvalFixture["customer"] = map[string]any{"email": email, "accountId": accountID}
	launch := approvalFixture["launch"].(map[string]any)
	launch["idempotencyKey"] = "acceptance-b-reconcile-basic"
	launch["operationId"] = workspaceLaunchOperationID(accountID, launch["idempotencyKey"].(string))
	launch["workspaceId"] = "ws-" + stableID("workspace-launch-v2", accountID, launch["operationId"].(string))[:18]
	approval, ok := parseProductionAcceptanceBApprovalFixture(t, approvalFixture)
	if !ok {
		t.Fatal("Acceptance B reconcile approval did not parse")
	}
	store := newMemoryTableStore()
	mustStore(t, store.CreateProvisionedAccount(context.Background(),
		map[string]any{"id": accountID, "ownerUserId": userID, "sub2apiUserId": int64(41), "status": "active"},
		map[string]any{"id": userID, "email": email, "accountId": accountID, "role": "owner", "status": "active"},
		map[string]any{"id": "org-reconcile", "name": "Reconcile", "billingAccountId": accountID, "status": "active"},
		map[string]any{"id": "mem-reconcile", "accountId": accountID, "organizationId": "org-reconcile", "userId": userID, "role": "owner", "status": "active"},
	))
	sub2API := &acceptanceBReconcileSub2APIClient{
		testSub2APIClient: &testSub2APIClient{balance: 27_635_986, charges: map[string]int64{}},
		users:             []clients.Sub2APIUser{{ID: 41, Email: email, BalanceUSDMicros: 27_635_986, Status: "active"}},
		keyCount:          4,
		history: map[string]clients.Sub2APIBalanceHistoryEntry{
			"unrelated-general-debit": {Code: "unrelated-general-debit", Type: "balance", ValueUSDMicros: -32_364_014, Status: "used"},
		},
	}
	ledger := &acceptanceBReceiptListLedger{}
	return &acceptanceBAccountReconcileFixture{
		t: t, email: email, accountID: accountID, userID: userID, approval: approval,
		app: &controlPlaneServer{tables: store}, store: store, sub2API: sub2API, ledger: ledger,
		service:    controlplane.NewService(ledger, &fakeFabricClient{}, sub2API),
		redeemCode: monthlyRedeemCode(monthlyEnvironment(), approval.Launch.OperationID),
	}
}

func (f *acceptanceBAccountReconcileFixture) reconcile() (acceptanceBAccountReconcileData, error) {
	return f.app.reconcileAcceptanceBAccount(context.Background(), f.service, f.email)
}

func TestAcceptanceBAccountReconcilePreparedUsesOnlyApprovedWorkspaceFootprint(t *testing.T) {
	fixture := newAcceptanceBAccountReconcileFixture(t)
	result, err := fixture.reconcile()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Status != "prepared" || result.ApprovalState != "bound" || result.WorkspaceDebitState != "absent" ||
		result.WorkspaceLaunchState != "absent" || result.WorkspaceCount != 0 || result.KeyCount != 0 || result.ReceiptCount != 0 {
		t.Fatalf("unexpected prepared reconcile result: %#v", result)
	}
	if len(fixture.sub2API.historyLookupCode) != 1 || fixture.sub2API.historyLookupCode[0] != fixture.redeemCode {
		t.Fatalf("history lookup codes=%#v, want only approved code", fixture.sub2API.historyLookupCode)
	}
	encoded, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, secret := range []string{fixture.email, fixture.accountID, fixture.approval.Launch.OperationID, fixture.approval.Launch.WorkspaceID, fixture.redeemCode} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("redacted DTO contains protected identity %q: %s", secret, encoded)
		}
	}
}

func TestAcceptanceBAccountReconcileApprovedFootprintNeverPrepared(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*acceptanceBAccountReconcileFixture)
	}{
		{name: "approved debit present", mutate: func(f *acceptanceBAccountReconcileFixture) {
			usedBy, usedAt := int64(41), time.Now().UTC()
			f.sub2API.history[f.redeemCode] = clients.Sub2APIBalanceHistoryEntry{Code: f.redeemCode, Type: "balance", ValueUSDMicros: -52_580_000, Status: "used", UsedBy: &usedBy, UsedAt: &usedAt}
		}},
		{name: "approved debit lookup unknown", mutate: func(f *acceptanceBAccountReconcileFixture) { f.sub2API.historyErr = errors.New("history unavailable") }},
		{name: "approval mismatch", mutate: func(f *acceptanceBAccountReconcileFixture) { f.t.Setenv("OPL_RELEASE_SHA", strings.Repeat("d", 40)) }},
		{name: "approved launch present", mutate: func(f *acceptanceBAccountReconcileFixture) {
			mustStore(f.t, f.store.SaveRuntimeOperation(context.Background(), map[string]any{"id": f.approval.Launch.OperationID, "operationId": f.approval.Launch.OperationID, "accountId": f.accountID, "workspaceId": f.approval.Launch.WorkspaceID, "action": workspaceLaunchAction, "status": "running"}))
		}},
		{name: "approved workspace present", mutate: func(f *acceptanceBAccountReconcileFixture) {
			mustStore(f.t, f.store.SaveWorkspace(context.Background(), map[string]any{"id": f.approval.Launch.WorkspaceID, "ownerAccountId": f.accountID, "ownerUserId": f.userID, "state": "active"}))
		}},
		{name: "approved workspace key present", mutate: func(f *acceptanceBAccountReconcileFixture) {
			f.sub2API.workspaceKeys = []clients.Sub2APIWorkspaceKey{{ID: 9, UserID: 41, Name: workspaceReservedKeyName(f.approval.Launch.WorkspaceID), Status: "active"}}
		}},
		{name: "approved receipt present", mutate: func(f *acceptanceBAccountReconcileFixture) {
			f.ledger.receipts = []clients.Receipt{{ReceiptInput: clients.ReceiptInput{Type: "billing.workspace_purchased.v1", AccountID: f.accountID, WorkspaceID: f.approval.Launch.WorkspaceID}}}
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newAcceptanceBAccountReconcileFixture(t)
			testCase.mutate(fixture)
			result, _ := fixture.reconcile()
			if result.Status == "prepared" {
				t.Fatalf("unsafe footprint was prepared: %#v", result)
			}
		})
	}
}
