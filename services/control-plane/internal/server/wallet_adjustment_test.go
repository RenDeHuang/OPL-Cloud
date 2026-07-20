package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type walletAdjustmentSub2API struct {
	*testSub2APIClient
	history       []clients.Sub2APIBalanceHistoryEntry
	historyErr    error
	adjustmentErr error
	chargeCalls   int
	refundCalls   int
	balanceCalled chan struct{}
}

func (s *walletAdjustmentSub2API) Balance(ctx context.Context, userID int64) (clients.Sub2APIBalance, error) {
	if s.balanceCalled != nil {
		select {
		case s.balanceCalled <- struct{}{}:
		default:
		}
	}
	return s.testSub2APIClient.Balance(ctx, userID)
}

func (s *walletAdjustmentSub2API) Charge(_ context.Context, input clients.Sub2APIChargeInput) (clients.Sub2APICharge, error) {
	s.chargeCalls++
	if s.adjustmentErr != nil {
		return clients.Sub2APICharge{}, s.adjustmentErr
	}
	s.balance -= input.ChargeUSDMicros
	s.appendHistory(input.Code, -input.ChargeUSDMicros, input.UserID)
	return clients.Sub2APICharge{Code: input.Code, UserID: input.UserID, ChargeUSDMicros: input.ChargeUSDMicros, Status: "used"}, nil
}

func (s *walletAdjustmentSub2API) Refund(_ context.Context, input clients.Sub2APIRefundInput) (clients.Sub2APIRefund, error) {
	s.refundCalls++
	if s.adjustmentErr != nil {
		return clients.Sub2APIRefund{}, s.adjustmentErr
	}
	s.balance += input.RefundUSDMicros
	s.appendHistory(input.Code, input.RefundUSDMicros, input.UserID)
	return clients.Sub2APIRefund{Code: input.Code, UserID: input.UserID, RefundUSDMicros: input.RefundUSDMicros, Status: "used"}, nil
}

func (s *walletAdjustmentSub2API) appendHistory(code string, value, userID int64) {
	now := operatorProjectionTime
	s.history = append(s.history, clients.Sub2APIBalanceHistoryEntry{
		Code: code, Type: "balance", ValueUSDMicros: value, Status: "used", UsedBy: &userID, UsedAt: &now, CreatedAt: now,
	})
}

func (s *walletAdjustmentSub2API) Usage(context.Context, clients.Sub2APIUsageQuery) (clients.Sub2APIUsagePage, error) {
	return clients.Sub2APIUsagePage{}, nil
}

func (s *walletAdjustmentSub2API) UsageStats(context.Context, clients.Sub2APIUsageStatsQuery) (clients.Sub2APIUsageStats, error) {
	return clients.Sub2APIUsageStats{}, nil
}

func (s *walletAdjustmentSub2API) BalanceHistory(context.Context, int64) ([]clients.Sub2APIBalanceHistoryEntry, error) {
	if s.historyErr != nil {
		return nil, s.historyErr
	}
	return append([]clients.Sub2APIBalanceHistoryEntry(nil), s.history...), nil
}

type walletAdjustmentLedger struct {
	fakeLedgerClient
	receipts []clients.ReceiptInput
}

func (l *walletAdjustmentLedger) RecordReceipt(_ context.Context, input clients.ReceiptInput, _ string) (clients.Receipt, error) {
	l.receipts = append(l.receipts, input)
	return clients.Receipt{ReceiptInput: input, ReceiptID: "receipt-wallet-adjustment"}, nil
}

type walletAdjustmentFixture struct {
	server http.Handler
	store  *memoryTableStore
	remote *walletAdjustmentSub2API
	ledger *walletAdjustmentLedger
}

type walletAdjustmentAuditOnceStore struct {
	*memoryTableStore
	failAudit bool
}

func (s *walletAdjustmentAuditOnceStore) SaveAuditEvent(ctx context.Context, event map[string]any) error {
	if s.failAudit {
		s.failAudit = false
		return errors.New("audit temporarily unavailable")
	}
	return s.memoryTableStore.SaveAuditEvent(ctx, event)
}

func newWalletAdjustmentFixture(t *testing.T) walletAdjustmentFixture {
	t.Helper()
	store := newMemoryTableStore()
	seedOperatorProjectionAccount(t, store, "acct-alpha", "usr-alpha", "alpha@example.com", 41)
	remote := &walletAdjustmentSub2API{testSub2APIClient: &testSub2APIClient{
		balance: 100_000_000,
		charges: map[string]int64{},
		identities: map[string]clients.Sub2APIIdentity{
			"alpha@example.com": {ID: 41, Email: "alpha@example.com", Status: "active"},
		},
		passwords: map[string]string{"alpha@example.com": "CorrectHorseBatteryStaple!"},
	}}
	ledger := &walletAdjustmentLedger{}
	server, err := NewPersistentServer(controlplane.NewService(ledger, &fakeFabricClient{}, remote), store)
	if err != nil {
		t.Fatal(err)
	}
	return walletAdjustmentFixture{server: server, store: store, remote: remote, ledger: ledger}
}

func sendWalletAdjustmentRequest(t *testing.T, fixture walletAdjustmentFixture, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	return requestWithMutationKeyForTest(t, fixture.server, reservedOperatorSessionForTest(t, fixture.server), http.MethodPost,
		"/api/operator/accounts/acct-alpha/wallet-adjustments", body, key)
}

func decodeWalletAdjustmentResponse(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode wallet adjustment: %v: %s", err, response.Body.String())
	}
	return body
}

func TestWalletAdjustmentAuth(t *testing.T) {
	fixture := newWalletAdjustmentFixture(t)
	body := `{"kind":"debit","amountUsd":"1.00","reason":"manual correction","confirmationAccountId":"acct-alpha"}`

	anonymous := httptest.NewRecorder()
	fixture.server.ServeHTTP(anonymous, httptest.NewRequest(http.MethodPost, "/api/operator/accounts/acct-alpha/wallet-adjustments", strings.NewReader(body)))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d body=%s", anonymous.Code, anonymous.Body.String())
	}

	owner := tenantOwnerSessionForTest(t, fixture.server)
	forbidden := requestWithMutationKeyForTest(t, fixture.server, owner, http.MethodPost, "/api/operator/accounts/acct-alpha/wallet-adjustments", body, "wallet-auth-owner")
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("owner status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}
}

func TestWalletAdjustmentValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "unknown kind", body: `{"kind":"credit","amountUsd":"1.00","reason":"manual correction","confirmationAccountId":"acct-alpha"}`},
		{name: "fraction beyond micros", body: `{"kind":"debit","amountUsd":"1.0000001","reason":"manual correction","confirmationAccountId":"acct-alpha"}`},
		{name: "non positive", body: `{"kind":"debit","amountUsd":"0","reason":"manual correction","confirmationAccountId":"acct-alpha"}`},
		{name: "over maximum", body: `{"kind":"debit","amountUsd":"1000000.000001","reason":"manual correction","confirmationAccountId":"acct-alpha"}`},
		{name: "wrong confirmation", body: `{"kind":"debit","amountUsd":"1.00","reason":"manual correction","confirmationAccountId":"acct-beta"}`},
		{name: "refund missing link", body: `{"kind":"business_refund","amountUsd":"1.00","reason":"service recovery","confirmationAccountId":"acct-alpha"}`},
		{name: "unexpected field", body: `{"kind":"recharge","amountUsd":"1.00","reason":"manual correction","confirmationAccountId":"acct-alpha","sub2apiUserId":41}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newWalletAdjustmentFixture(t)
			response := sendWalletAdjustmentRequest(t, fixture, tc.body, "wallet-invalid-"+strings.ReplaceAll(tc.name, " ", "-"))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if fixture.remote.chargeCalls+fixture.remote.refundCalls != 0 {
				t.Fatalf("invalid request reached Sub2API: charge=%d refund=%d", fixture.remote.chargeCalls, fixture.remote.refundCalls)
			}
		})
	}
}

func TestWalletAdjustmentIdempotency(t *testing.T) {
	fixture := newWalletAdjustmentFixture(t)
	body := `{"kind":"debit","amountUsd":"1.250001","reason":"manual correction","confirmationAccountId":"acct-alpha"}`
	first := sendWalletAdjustmentRequest(t, fixture, body, "wallet-debit-once")
	if first.Code != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	firstBody := decodeWalletAdjustmentResponse(t, first)
	replay := sendWalletAdjustmentRequest(t, fixture, body, "wallet-debit-once")
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	if string(mustJSON(firstBody)) != string(mustJSON(decodeWalletAdjustmentResponse(t, replay))) || fixture.remote.chargeCalls != 1 || len(fixture.ledger.receipts) != 1 {
		t.Fatalf("replay changed result or side effects: first=%#v calls=%d receipts=%d", firstBody, fixture.remote.chargeCalls, len(fixture.ledger.receipts))
	}

	conflict := sendWalletAdjustmentRequest(t, fixture, `{"kind":"debit","amountUsd":"2.00","reason":"manual correction","confirmationAccountId":"acct-alpha"}`, "wallet-debit-once")
	if conflict.Code != http.StatusConflict || fixture.remote.chargeCalls != 1 {
		t.Fatalf("conflict status=%d calls=%d body=%s", conflict.Code, fixture.remote.chargeCalls, conflict.Body.String())
	}

	operationID := stringValue(firstBody["operationId"])
	read := requestWithSession(t, fixture.server, reservedOperatorSessionForTest(t, fixture.server), http.MethodGet, "/api/operator/wallet-adjustments/"+operationID, "")
	if read.Code != http.StatusOK || string(mustJSON(decodeWalletAdjustmentResponse(t, read))) != string(mustJSON(firstBody)) {
		t.Fatalf("readback status=%d body=%s", read.Code, read.Body.String())
	}
}

func TestWalletAdjustmentUsesAccountWalletLock(t *testing.T) {
	fixture := newWalletAdjustmentFixture(t)
	fixture.remote.balanceCalled = make(chan struct{}, 1)
	app := fixture.server.(*controlPlaneHTTPHandler).app
	unlock := app.lockResource("sub2api-wallet", "acct-alpha")
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response <- sendWalletAdjustmentRequest(t, fixture, `{"kind":"recharge","amountUsd":"1.00","reason":"pilot credit","confirmationAccountId":"acct-alpha"}`, "wallet-shared-lock")
	}()

	select {
	case <-fixture.remote.balanceCalled:
		unlock()
		t.Fatal("wallet adjustment reached Sub2API while the account wallet lock was held")
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	select {
	case result := <-response:
		if result.Code != http.StatusCreated {
			t.Fatalf("wallet adjustment status=%d body=%s", result.Code, result.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("wallet adjustment did not resume after the account wallet lock was released")
	}
}

func TestWalletAdjustmentInsufficientBalanceReplayNeverBecomesDelayedDebit(t *testing.T) {
	fixture := newWalletAdjustmentFixture(t)
	fixture.remote.balance = 500_000
	body := `{"kind":"debit","amountUsd":"1.00","reason":"manual correction","confirmationAccountId":"acct-alpha"}`

	first := sendWalletAdjustmentRequest(t, fixture, body, "wallet-insufficient")
	fixture.remote.balance = 100_000_000
	replay := sendWalletAdjustmentRequest(t, fixture, body, "wallet-insufficient")
	operations, _ := fixture.store.ListRuntimeOperations(context.Background())
	if first.Code != http.StatusConflict || replay.Code != http.StatusConflict || fixture.remote.chargeCalls != 0 || len(operations) != 1 || operations[0]["status"] != "failed" {
		t.Fatalf("first=%d replay=%d chargeCalls=%d operations=%#v", first.Code, replay.Code, fixture.remote.chargeCalls, operations)
	}
}

func TestWalletAdjustmentRefundLink(t *testing.T) {
	fixture := newWalletAdjustmentFixture(t)
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), map[string]any{
		"id": "workspace-launch-alpha", "operationId": "workspace-launch-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha",
		"resourceId": "ws-alpha", "resourceKind": "workspace_launch", "action": "workspace.launch", "status": "succeeded",
		"result": `{"totalChargeUsdMicros":5000000}`, "createdAt": operatorProjectionTime.Add(-time.Hour).Format(time.RFC3339Nano),
	}))

	for i, amount := range []string{"3.00", "2.00"} {
		body := `{"kind":"business_refund","amountUsd":"` + amount + `","reason":"service recovery","relatedOperationId":"workspace-launch-alpha","confirmationAccountId":"acct-alpha"}`
		response := sendWalletAdjustmentRequest(t, fixture, body, "wallet-refund-"+amount)
		if response.Code != http.StatusCreated {
			t.Fatalf("refund %d status=%d body=%s", i, response.Code, response.Body.String())
		}
	}
	over := sendWalletAdjustmentRequest(t, fixture, `{"kind":"business_refund","amountUsd":"0.000001","reason":"service recovery","relatedOperationId":"workspace-launch-alpha","confirmationAccountId":"acct-alpha"}`, "wallet-refund-over")
	if over.Code != http.StatusConflict || fixture.remote.refundCalls != 2 {
		t.Fatalf("over-refund status=%d calls=%d body=%s", over.Code, fixture.remote.refundCalls, over.Body.String())
	}
}

func TestWalletAdjustmentReadback(t *testing.T) {
	fixture := newWalletAdjustmentFixture(t)
	fixture.remote.adjustmentErr = clients.ErrSub2APIChargeUnknown
	fixture.remote.historyErr = errors.New("balance history unavailable")
	response := sendWalletAdjustmentRequest(t, fixture, `{"kind":"debit","amountUsd":"1.00","reason":"manual correction","confirmationAccountId":"acct-alpha"}`, "wallet-unknown")
	if response.Code != http.StatusAccepted {
		t.Fatalf("unknown status=%d body=%s", response.Code, response.Body.String())
	}
	body := decodeWalletAdjustmentResponse(t, response)
	if body["status"] != "manual_review" || body["phase"] != "authoritative_readback" || body["afterBalance"].(map[string]any)["available"] != false || len(fixture.ledger.receipts) != 0 {
		t.Fatalf("unknown result=%#v receipts=%d", body, len(fixture.ledger.receipts))
	}
	replay := sendWalletAdjustmentRequest(t, fixture, `{"kind":"debit","amountUsd":"1.00","reason":"manual correction","confirmationAccountId":"acct-alpha"}`, "wallet-unknown")
	if replay.Code != http.StatusAccepted || fixture.remote.chargeCalls != 1 {
		t.Fatalf("unknown replay status=%d chargeCalls=%d body=%s", replay.Code, fixture.remote.chargeCalls, replay.Body.String())
	}
}

func TestWalletAdjustmentManualReviewAuditRecoversWithoutReplayingAdjustment(t *testing.T) {
	store := &walletAdjustmentAuditOnceStore{memoryTableStore: newMemoryTableStore(), failAudit: true}
	seedOperatorProjectionAccount(t, store.memoryTableStore, "acct-alpha", "usr-alpha", "alpha@example.com", 41)
	remote := &walletAdjustmentSub2API{testSub2APIClient: &testSub2APIClient{
		balance: 100_000_000,
		charges: map[string]int64{},
		identities: map[string]clients.Sub2APIIdentity{
			"alpha@example.com": {ID: 41, Email: "alpha@example.com", Status: "active"},
		},
		passwords: map[string]string{"alpha@example.com": "CorrectHorseBatteryStaple!"},
	}, adjustmentErr: clients.ErrSub2APIChargeUnknown, historyErr: errors.New("balance history unavailable")}
	ledger := &walletAdjustmentLedger{}
	server, err := NewPersistentServer(controlplane.NewService(ledger, &fakeFabricClient{}, remote), store)
	if err != nil {
		t.Fatal(err)
	}
	fixture := walletAdjustmentFixture{server: server, store: store.memoryTableStore, remote: remote, ledger: ledger}
	body := `{"kind":"debit","amountUsd":"1.00","reason":"manual correction","confirmationAccountId":"acct-alpha"}`

	first := sendWalletAdjustmentRequest(t, fixture, body, "wallet-manual-review-audit")
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	replay := sendWalletAdjustmentRequest(t, fixture, body, "wallet-manual-review-audit")
	events, _ := store.ListAuditEvents(context.Background(), "acct-alpha")
	if replay.Code != http.StatusAccepted || remote.chargeCalls != 1 || len(events) != 1 || events[0]["result"] != "manual_review" {
		t.Fatalf("replay status=%d chargeCalls=%d events=%#v body=%s", replay.Code, remote.chargeCalls, events, replay.Body.String())
	}
}

func TestWalletAdjustmentAudit(t *testing.T) {
	fixture := newWalletAdjustmentFixture(t)
	response := sendWalletAdjustmentRequest(t, fixture, `{"kind":"recharge","amountUsd":"2.50","reason":"pilot credit","confirmationAccountId":"acct-alpha"}`, "wallet-recharge-audit")
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := decodeWalletAdjustmentResponse(t, response)
	before := body["beforeBalance"].(map[string]any)
	after := body["afterBalance"].(map[string]any)
	if nested(before, "data", "usdMicros") != float64(100_000_000) || nested(after, "data", "usdMicros") != float64(102_500_000) || body["balanceHistoryRef"] == "" {
		t.Fatalf("authoritative balances=%#v", body)
	}

	events, _ := fixture.store.ListAuditEvents(context.Background(), "acct-alpha")
	operations, _ := fixture.store.ListRuntimeOperations(context.Background())
	if len(events) != 1 || events[0]["action"] != "gateway.wallet_adjustment" || events[0]["result"] != "succeeded" || len(operations) != 1 || operations[0]["status"] != "succeeded" {
		t.Fatalf("audit=%#v operations=%#v", events, operations)
	}
	if len(fixture.ledger.receipts) != 1 {
		t.Fatalf("receipts=%#v", fixture.ledger.receipts)
	}
	receipt := fixture.ledger.receipts[0]
	if receipt.Type != "gateway.wallet_adjustment.v1" || receipt.AccountID != "acct-alpha" || receipt.Execution["operationId"] != body["operationId"] || receipt.Execution["amountUsdMicros"] != int64(2_500_000) || receipt.InputRefs["balanceHistoryRef"] != body["balanceHistoryRef"] || receipt.Actor["userId"] == "" {
		t.Fatalf("receipt=%#v", receipt)
	}
	serialized := string(mustJSON(map[string]any{"body": body, "audit": events, "operation": operations, "receipt": receipt}))
	for _, forbidden := range []string{"redeemCode", "adminToken", "rawSub2apiResponse"} {
		if strings.Contains(strings.ToLower(serialized), strings.ToLower(forbidden)) {
			t.Fatalf("forbidden %q persisted in %s", forbidden, serialized)
		}
	}
}
