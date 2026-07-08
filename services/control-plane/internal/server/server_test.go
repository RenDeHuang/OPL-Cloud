package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func TestCreateWorkspaceHTTPUsesMutationKeyWhenHeaderIsAbsent(t *testing.T) {
	calls := []string{}
	ledger := &fakeLedgerClientWithKeys{fakeLedgerClient{}, []string{}}
	server := NewServer(controlplane.NewService(ledger, &fakeFabricClient{calls: &calls}))

	createResource(t, server, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	createResource(t, server, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","sizeGb":10}`)
	createResource(t, server, http.MethodPost, "/api/storage-attachments", `{"workspaceId":"ws-alpha","computeAllocationId":"compute-from-fabric","storageId":"volume-from-fabric","mountPath":"/data"}`)

	body := bytes.NewBufferString(`{"accountId":"acct-alpha","ownerId":"usr-owner","workspaceName":"Alpha Lab","attachmentId":"attachment-from-fabric"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces", body)
	session := operatorSessionForTest(t, server)
	addSessionCookies(req, session)
	req.Header.Set("x-opl-csrf", session.Header().Get("x-opl-csrf-token"))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if len(ledger.keys) == 0 || ledger.keys[0] == "" {
		t.Fatalf("expected generated workspace idempotency key, got %#v", ledger.keys)
	}
}

func TestConsoleStaticEntryServesLoginAndHome(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	for _, path := range []string{"/", "/login", "/console/overview"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200: %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `<div id="root"></div>`) {
			t.Fatalf("%s did not serve Console HTML: %s", path, rec.Body.String())
		}
	}
}

func TestUncontractedAdminDiagnosticsAPIRouteDoesNotReturnFakeEvidence(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	req := httptest.NewRequest(http.MethodGet, "/api/admin/diagnostics", nil)
	addSessionCookies(req, operatorSessionForTest(t, server))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for uncontracted fake diagnostics route: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateWorkspaceHTTPUsesControlPlaneService(t *testing.T) {
	calls := []string{}
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{calls: &calls}))

	createResource(t, server, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	createResource(t, server, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","sizeGb":10}`)
	createResource(t, server, http.MethodPost, "/api/storage-attachments", `{"workspaceId":"ws-alpha","computeAllocationId":"compute-from-fabric","storageId":"volume-from-fabric","mountPath":"/data"}`)

	body := bytes.NewBufferString(`{"accountId":"acct-alpha","ownerId":"usr-owner","workspaceName":"Alpha Lab","attachmentId":"attachment-from-fabric"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces", body)
	session := operatorSessionForTest(t, server)
	addSessionCookies(req, session)
	req.Header.Set("x-opl-csrf", session.Header().Get("x-opl-csrf-token"))
	req.Header.Set("Idempotency-Key", "workspace-once")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var workspace map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&workspace); err != nil {
		t.Fatalf("decode workspace: %v", err)
	}
	if workspace["accountId"] != "acct-alpha" || workspace["ownerId"] != "usr-owner" || workspace["url"] != "https://workspace.medopl.cn/w/ws-from-fabric/" {
		t.Fatalf("workspace did not come from service boundary: %#v", workspace)
	}
	if workspace["computeAllocationId"] != "compute-from-fabric" || workspace["storageId"] != "volume-from-fabric" || workspace["attachmentId"] != "attachment-from-fabric" || workspace["evidenceId"] != "evidence-from-ledger" {
		t.Fatalf("workspace missing ledger/fabric evidence: %#v", workspace)
	}
	if access, ok := workspace["access"].(map[string]any); !ok || access["tokenStatus"] != "active" {
		t.Fatalf("workspace response must include active URL access state: %#v", workspace)
	}
	if access := workspace["access"].(map[string]any); access["account"] != "admin" || access["password"] != "runtime-password-alpha" {
		t.Fatalf("workspace response must include runtime login credentials from Fabric: %#v", access)
	}
	if slices.Contains(calls[3:], "fabric.compute") || slices.Contains(calls[3:], "fabric.storage") {
		t.Fatalf("workspace create must not allocate replacement resources: %#v", calls)
	}
}

func TestPricingPreviewMatchesResourceHoldAmount(t *testing.T) {
	ledger := &capturingHoldLedgerClient{}
	server := NewServer(controlplane.NewService(ledger, &fakeFabricClient{}))
	session := operatorSessionForTest(t, server)

	previewReq := httptest.NewRequest(http.MethodPost, "/api/pricing/preview", bytes.NewBufferString(`{"accountId":"acct-alpha","resourceType":"compute","packageId":"basic"}`))
	addSessionCookies(previewReq, session)
	previewReq.Header.Set("x-opl-csrf", session.Header().Get("x-opl-csrf-token"))
	previewRec := httptest.NewRecorder()
	server.ServeHTTP(previewRec, previewReq)
	if previewRec.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200: %s", previewRec.Code, previewRec.Body.String())
	}
	var preview map[string]any
	if err := json.NewDecoder(previewRec.Body).Decode(&preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	holdAmountCents := int64(numberField(preview, "holdAmountCents", 0))
	if holdAmountCents <= 0 || stringValue(preview["pricingVersion"]) == "" || preview["priceSnapshot"] == nil {
		t.Fatalf("preview must return pricingVersion, priceSnapshot and holdAmountCents: %#v", preview)
	}

	createReq := httptest.NewRequest(http.MethodPost, "/api/compute-allocations", bytes.NewBufferString(`{"accountId":"acct-alpha","packageId":"basic"}`))
	addSessionCookies(createReq, session)
	createReq.Header.Set("x-opl-csrf", session.Header().Get("x-opl-csrf-token"))
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want 202: %s", createRec.Code, createRec.Body.String())
	}
	if ledger.lastHold.AmountCents != holdAmountCents {
		t.Fatalf("create hold amount = %d, want preview %d", ledger.lastHold.AmountCents, holdAmountCents)
	}
}

func TestWorkspaceRuntimeStatusPassesFabricChecks(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/runtime-status", bytes.NewBufferString(`{"workspaceId":"ws-alpha"}`))
	req.Header.Set("Content-Type", "application/json")
	session := operatorSessionForTest(t, server)
	addSessionCookies(req, session)
	req.Header.Set("x-opl-csrf", session.Header().Get("x-opl-csrf-token"))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["ready"] != false {
		t.Fatalf("ready must come from Fabric runtime state: %#v", body)
	}
	checks := body["checks"].([]any)
	if len(checks) != 2 || checks[0].(map[string]any)["name"] != "deployment_ready" || checks[1].(map[string]any)["name"] != "service_endpoints_ready" {
		t.Fatalf("runtime checks must pass through Fabric details: %#v", body["checks"])
	}
}

func TestPersistentFactsSurviveServerRestart(t *testing.T) {
	path := t.TempDir() + "/control-plane-state.json"
	service := controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{})
	server, err := NewPersistentServer(service, NewFileFactStore(path))
	if err != nil {
		t.Fatalf("create persistent server: %v", err)
	}
	createResource(t, server, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)

	restarted, err := NewPersistentServer(service, NewFileFactStore(path))
	if err != nil {
		t.Fatalf("restart persistent server: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-alpha", nil)
	addSessionCookies(req, operatorSessionForTest(t, restarted))
	rec := httptest.NewRecorder()
	restarted.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("state status = %d: %s", rec.Code, rec.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	computes := state["computeAllocations"].([]any)
	if len(computes) != 1 || computes[0].(map[string]any)["id"] == "" {
		t.Fatalf("compute allocation did not survive restart: %#v", computes)
	}
	ledger := state["billingLedger"].([]any)
	if len(ledger) != 1 || ledger[0].(map[string]any)["type"] != "compute_hold" {
		t.Fatalf("compute hold ledger did not survive restart: %#v", ledger)
	}
	wallet := state["wallet"].(map[string]any)
	if wallet["frozenCents"].(float64) <= 0 {
		t.Fatalf("wallet frozen state did not survive restart: %#v", wallet)
	}
}

func TestWorkspaceTokenStatePersistsAcrossRestart(t *testing.T) {
	path := t.TempDir() + "/control-plane-state.json"
	service := controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{})
	server, err := NewPersistentServer(service, NewFileFactStore(path))
	if err != nil {
		t.Fatalf("create persistent server: %v", err)
	}
	createResource(t, server, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	createResource(t, server, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","sizeGb":10}`)
	createResource(t, server, http.MethodPost, "/api/storage-attachments", `{"workspaceId":"ws-alpha","computeAllocationId":"compute-from-fabric","storageId":"volume-from-fabric","mountPath":"/data"}`)
	workspace := createResource(t, server, http.MethodPost, "/api/workspaces", `{"accountId":"acct-alpha","ownerId":"usr-owner","workspaceName":"Alpha Lab","attachmentId":"attachment-from-fabric"}`)
	createResource(t, server, http.MethodPost, "/api/workspaces/delete-token", `{"workspaceId":"`+stringValue(workspace["id"])+`"}`)

	restarted, err := NewPersistentServer(service, NewFileFactStore(path))
	if err != nil {
		t.Fatalf("restart persistent server: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-alpha", nil)
	addSessionCookies(req, operatorSessionForTest(t, restarted))
	rec := httptest.NewRecorder()
	restarted.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("state status = %d: %s", rec.Code, rec.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	workspaces := state["workspaces"].([]any)
	if len(workspaces) != 1 || nested(workspaces[0].(map[string]any), "access", "tokenStatus") != "disabled" {
		t.Fatalf("workspace token state did not survive restart: %#v", workspaces)
	}
}

func TestBootstrapImportsAdminSeedAndDoesNotExposeLegacyOwner(t *testing.T) {
	t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"id":"usr-admin-production","email":"admin@medopl.cn","password":"StableAdminPass2026!","name":"Admin","role":"admin","accountId":"acct-admin"}]`)
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))

	loginRec := loginForTest(t, server, "admin@medopl.cn", "StableAdminPass2026!")
	if loginRec.Header().Get("x-opl-csrf-token") == "" {
		t.Fatalf("admin login did not return csrf token")
	}
	ownerRec := loginAttemptForTest(server, "owner@example.com", "StableAdminPass2026!", "203.0.113.50:1000")
	if ownerRec.Code != http.StatusUnauthorized {
		t.Fatalf("legacy owner@example.com login status = %d, want 401", ownerRec.Code)
	}
}

func TestLoginAcceptsLegacyScryptPasswordHash(t *testing.T) {
	app := newControlPlaneApp()
	app.users["usr-admin"]["passwordHash"] = "scrypt:00112233445566778899aabbccddeeff:4904ad313c8dcfe466e3babafef2471d2f5bcc7b0d4d893d5eb6c57666c8c5c1e9a26e8e1b9035f6625718daa983ae2798cbeb16b404e8418c901315147f642f"
	if _, _, err := app.login(map[string]any{"email": "admin@medopl.cn", "password": "legacy-secret"}); err != nil {
		t.Fatalf("legacy scrypt password did not verify: %v", err)
	}
}

func TestNonAdminRequestsCannotSelectAnotherAccount(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := operatorSessionForTest(t, server)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"alpha@lab.example","accountId":"acct-alpha","role":"pi","password":"CorrectHorseBatteryStaple!"}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"beta@lab.example","accountId":"acct-beta","role":"pi","password":"CorrectHorseBatteryStaple!"}`)
	alpha := loginForTest(t, server, "alpha@lab.example", "CorrectHorseBatteryStaple!")

	readOther := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-beta", nil)
	addSessionCookies(readOther, alpha)
	readOtherRec := httptest.NewRecorder()
	server.ServeHTTP(readOtherRec, readOther)
	if readOtherRec.Code != http.StatusForbidden {
		t.Fatalf("cross-account state status = %d, want 403: %s", readOtherRec.Code, readOtherRec.Body.String())
	}

	writeOther := requestWithSession(t, server, alpha, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-beta","packageId":"basic"}`)
	if writeOther.Code != http.StatusForbidden {
		t.Fatalf("cross-account compute create status = %d, want 403: %s", writeOther.Code, writeOther.Body.String())
	}

	mapOtherTicket := requestWithSession(t, server, alpha, http.MethodPost, "/api/support/tickets", `{"accountId":"acct-beta","externalTicketId":"ZAM-403","title":"wrong account"}`)
	if mapOtherTicket.Code != http.StatusForbidden {
		t.Fatalf("cross-account support mapping status = %d, want 403: %s", mapOtherTicket.Code, mapOtherTicket.Body.String())
	}

	writeOwn := requestWithSession(t, server, alpha, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	if writeOwn.Code != http.StatusAccepted {
		t.Fatalf("own-account compute create status = %d, want 202: %s", writeOwn.Code, writeOwn.Body.String())
	}

	mapOwnTicket := requestWithSession(t, server, alpha, http.MethodPost, "/api/support/tickets", `{"accountId":"acct-alpha","externalTicketId":"ZAM-200","title":"own account"}`)
	if mapOwnTicket.Code != http.StatusCreated {
		t.Fatalf("own-account support mapping status = %d, want 201: %s", mapOwnTicket.Code, mapOwnTicket.Body.String())
	}
}

func TestAdminMutationsAppendAuditEvents(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := operatorSessionForTest(t, server)

	createResourceWithSession(t, server, admin, http.MethodPost, "/api/billing/topups", `{"accountId":"acct-alpha","amount":100,"idempotencyKey":"audit-topup","confirm":true}`)

	req := httptest.NewRequest(http.MethodGet, "/api/management/state", nil)
	addSessionCookies(req, admin)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("management state status=%d body=%s", rec.Code, rec.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	events := state["auditEvents"].([]any)
	if !slices.ContainsFunc(events, func(item any) bool {
		event := item.(map[string]any)
		return event["action"] == "billing.topup" && event["targetAccountId"] == "acct-alpha" && event["actorUserId"] != "" && event["result"] == "succeeded"
	}) {
		t.Fatalf("missing billing topup audit event: %#v", events)
	}
}

func TestCreateUserRejectsDuplicateEmail(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := operatorSessionForTest(t, server)

	createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"pi@lab.example","accountId":"acct-alpha","role":"pi","password":"CorrectHorseBatteryStaple!"}`)
	duplicate := requestWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"PI@lab.example","accountId":"acct-beta","role":"pi","password":"CorrectHorseBatteryStaple!"}`)
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), "user_already_exists") {
		t.Fatalf("duplicate create status=%d body=%s, want 409 user_already_exists", duplicate.Code, duplicate.Body.String())
	}
}

type fakeLedgerClient struct{}

type fakeLedgerClientWithKeys struct {
	fakeLedgerClient
	keys []string
}

type capturingHoldLedgerClient struct {
	fakeLedgerClient
	lastHold clients.HoldInput
}

func (f *capturingHoldLedgerClient) CreateHold(ctx context.Context, input clients.HoldInput, idempotencyKey string) (clients.HoldResult, error) {
	f.lastHold = input
	return f.fakeLedgerClient.CreateHold(ctx, input, idempotencyKey)
}

func (f *fakeLedgerClientWithKeys) CreateHold(ctx context.Context, input clients.HoldInput, idempotencyKey string) (clients.HoldResult, error) {
	f.keys = append(f.keys, idempotencyKey)
	return f.fakeLedgerClient.CreateHold(ctx, input, idempotencyKey)
}

func (fakeLedgerClient) ManualTopUp(_ context.Context, input clients.ManualTopUpInput, _ string) (clients.ManualTopUpResult, error) {
	return clients.ManualTopUpResult{
		TopUp:             clients.ManualTopUp{ID: "topup-from-ledger", AccountID: input.AccountID, AmountCents: input.AmountCents, OperatorUserID: input.OperatorUserID},
		LedgerEntry:       clients.LedgerEntry{ID: "ledger-from-ledger", AccountID: input.AccountID, AmountCents: input.AmountCents},
		WalletTransaction: clients.WalletTransaction{ID: "wallet-tx-from-ledger", AccountID: input.AccountID, AmountCents: input.AmountCents},
		Wallet:            clients.Wallet{AccountID: input.AccountID, BalanceCents: input.AmountCents, AvailableCents: input.AmountCents, Currency: "CNY"},
	}, nil
}

func (fakeLedgerClient) CreateHold(_ context.Context, input clients.HoldInput, _ string) (clients.HoldResult, error) {
	return clients.HoldResult{ID: "hold-from-ledger", AccountID: input.AccountID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, AmountCents: input.AmountCents, Wallet: clients.Wallet{AccountID: input.AccountID, BalanceCents: 20000, FrozenCents: input.AmountCents, AvailableCents: 20000 - input.AmountCents, Currency: "CNY"}}, nil
}

func (fakeLedgerClient) ReleaseHold(_ context.Context, input clients.HoldReleaseInput, _ string) (clients.HoldReleaseResult, error) {
	return clients.HoldReleaseResult{ID: "release-from-ledger", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, HoldID: input.HoldID, AmountCents: input.AmountCents, Status: "released", Wallet: clients.Wallet{AccountID: input.AccountID, BalanceCents: 8800, FrozenCents: 0, AvailableCents: 8800, Currency: "CNY"}}, nil
}

func (fakeLedgerClient) RecordEvidence(_ context.Context, input clients.EvidenceInput, _ string) (clients.EvidenceReceipt, error) {
	return clients.EvidenceReceipt{ID: "evidence-from-ledger", WorkspaceID: input.WorkspaceID}, nil
}

func (fakeLedgerClient) SettleResource(_ context.Context, input clients.ResourceSettlementInput, _ string) (clients.ResourceSettlementResult, error) {
	return clients.ResourceSettlementResult{ID: "settlement-from-ledger", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, AmountCents: input.AmountCents, Status: "settled", LedgerEntryID: "ledger-settlement-from-ledger", WalletTransactionID: "wallet-settlement-from-ledger", PricingVersion: input.PricingVersion, PriceSnapshot: input.PriceSnapshot, UsagePeriodStart: input.UsagePeriodStart, UsagePeriodEnd: input.UsagePeriodEnd, Quantity: input.Quantity, Unit: input.Unit, ProviderCostEvidenceRef: input.ProviderCostEvidenceRef, Wallet: clients.Wallet{AccountID: input.AccountID, BalanceCents: 8800, AvailableCents: 8800, Currency: "CNY"}}, nil
}

type fakeLedgerClientWithoutSettlementIdentity struct {
	fakeLedgerClient
}

func (fakeLedgerClientWithoutSettlementIdentity) SettleResource(_ context.Context, _ clients.ResourceSettlementInput, _ string) (clients.ResourceSettlementResult, error) {
	return clients.ResourceSettlementResult{ID: "settlement-from-ledger", Status: "settled", LedgerEntryID: "ledger-settlement-from-ledger", WalletTransactionID: "wallet-settlement-from-ledger"}, nil
}

func (fakeLedgerClient) RecordReconciliation(_ context.Context, input clients.ReconciliationInput, _ string) (clients.ReconciliationResult, error) {
	return clients.ReconciliationResult{ID: stringField(input.Report, "id", "reconciliation-from-ledger"), Status: "ok", Report: input.Report, BlockNewWorkspaces: false, Reason: "operator_reconciliation"}, nil
}

func (fakeLedgerClient) Wallet(_ context.Context, accountID string) (clients.Wallet, error) {
	return clients.Wallet{AccountID: accountID, Currency: "CNY"}, nil
}

func (fakeLedgerClient) ListLedgerEntries(_ context.Context, _ string) ([]clients.LedgerEntry, error) {
	return nil, nil
}

func (fakeLedgerClient) ListWalletTransactions(_ context.Context, _ string) ([]clients.WalletTransaction, error) {
	return nil, nil
}

func (fakeLedgerClient) ListManualTopUps(_ context.Context, _ string) ([]clients.ManualTopUp, error) {
	return nil, nil
}

func (fakeLedgerClient) ListResourceSettlements(_ context.Context, _ string) ([]clients.ResourceSettlementResult, error) {
	return nil, nil
}

type fakeBlockingReconciliationLedgerClient struct {
	fakeLedgerClient
}

func (fakeBlockingReconciliationLedgerClient) RecordReconciliation(_ context.Context, input clients.ReconciliationInput, _ string) (clients.ReconciliationResult, error) {
	return clients.ReconciliationResult{ID: stringField(input.Report, "id", "reconciliation-from-ledger"), Status: "mismatch", Report: input.Report, BlockNewWorkspaces: true, Reason: "tencent_bill_reconciliation_failed"}, nil
}

type readBackedLedgerClient struct {
	fakeLedgerClient
}

func (readBackedLedgerClient) Wallet(_ context.Context, accountID string) (clients.Wallet, error) {
	return clients.Wallet{AccountID: accountID, BalanceCents: 9900, FrozenCents: 1200, AvailableCents: 8700, TotalSpentCents: 1200, Currency: "CNY"}, nil
}

func (readBackedLedgerClient) ListLedgerEntries(_ context.Context, _ string) ([]clients.LedgerEntry, error) {
	return []clients.LedgerEntry{{ID: "ledger-settlement-alpha", AccountID: "acct-alpha", AmountCents: 1200, Currency: "CNY", Direction: "debit", Source: "compute_settlement", Reason: "ws-alpha"}}, nil
}

func (readBackedLedgerClient) ListWalletTransactions(_ context.Context, _ string) ([]clients.WalletTransaction, error) {
	return []clients.WalletTransaction{{ID: "wallet-tx-alpha", AccountID: "acct-alpha", LedgerEntryID: "ledger-settlement-alpha", AmountCents: -1200, BalanceCents: 9900, FrozenCents: 1200, AvailableCents: 8700, TotalSpentCents: 1200, Currency: "CNY"}}, nil
}

func (readBackedLedgerClient) ListManualTopUps(_ context.Context, _ string) ([]clients.ManualTopUp, error) {
	return []clients.ManualTopUp{}, nil
}

func (readBackedLedgerClient) ListResourceSettlements(_ context.Context, _ string) ([]clients.ResourceSettlementResult, error) {
	return []clients.ResourceSettlementResult{{
		ID:                      "settlement-alpha",
		AccountID:               "acct-alpha",
		WorkspaceID:             "ws-alpha",
		ResourceType:            "compute",
		ResourceID:              "compute-alpha",
		AmountCents:             1200,
		Currency:                "CNY",
		Status:                  "settled",
		LedgerEntryID:           "ledger-settlement-alpha",
		WalletTransactionID:     "wallet-tx-alpha",
		PricingVersion:          "pricing-2026-07",
		PriceSnapshot:           map[string]any{"unitPriceCents": 1200},
		UsagePeriodStart:        "2026-07-08T00:00:00Z",
		UsagePeriodEnd:          "2026-07-08T01:00:00Z",
		Quantity:                1,
		Unit:                    "hour",
		ProviderCostEvidenceRef: "tencent-row-alpha",
	}}, nil
}

type failingFabricClient struct {
	fakeFabricClient
}

func (failingFabricClient) Readiness(_ context.Context) (map[string]any, error) {
	return nil, errors.New("provider secret leaked in raw error")
}

func (failingFabricClient) ListOperations(_ context.Context) ([]clients.FabricOperation, error) {
	return nil, errors.New("provider operation secret leaked in raw error")
}

type fakeFabricClient struct {
	calls *[]string
}

func (f *fakeFabricClient) record(call string) {
	if f != nil && f.calls != nil {
		*f.calls = append(*f.calls, call)
	}
}

func (f *fakeFabricClient) CreateComputeAllocation(_ context.Context, input clients.ComputeAllocationInput, _ string) (clients.ComputeAllocation, error) {
	f.record("fabric.compute")
	return clients.ComputeAllocation{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID, Status: "running", Provider: "tencent-tke", ProviderResourceID: "node/node-from-fabric", ProviderRequestID: "compute-request-from-fabric", InstanceID: "ins-from-fabric", NodeName: "node-from-fabric", BillingStatus: "active", ServiceName: "opl-compute-from-fabric"}, nil
}

func (f *fakeFabricClient) GetComputeAllocation(_ context.Context, id string) (clients.ComputeAllocation, error) {
	f.record("fabric.compute-get")
	return clients.ComputeAllocation{ID: id, Status: "running", Provider: "tencent-tke", ProviderResourceID: "node/node-from-fabric", ProviderRequestID: "compute-request-from-fabric", InstanceID: "ins-from-fabric", NodeName: "node-from-fabric", BillingStatus: "active", ServiceName: "opl-compute-from-fabric"}, nil
}

func (f *fakeFabricClient) DestroyComputeAllocation(_ context.Context, id string, _ string) (clients.ComputeAllocation, error) {
	f.record("fabric.compute-destroy")
	return clients.ComputeAllocation{ID: id, Status: "destroyed", Provider: "tencent-tke", ProviderRequestID: "compute-destroy-from-fabric", BillingStatus: "stopped"}, nil
}

func (f *fakeFabricClient) CreateStorageVolume(_ context.Context, input clients.StorageVolumeInput, _ string) (clients.StorageVolume, error) {
	f.record("fabric.storage")
	return clients.StorageVolume{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "available", Provider: "tencent-tke", ProviderResourceID: "pvc/volume-from-fabric-data", ProviderRequestID: "storage-request-from-fabric", SizeGB: input.SizeGB, StorageClass: "cbs", BillingStatus: "active"}, nil
}

func (f *fakeFabricClient) DestroyStorageVolume(_ context.Context, id string, _ string) (clients.StorageVolume, error) {
	f.record("fabric.storage-destroy")
	return clients.StorageVolume{ID: id, Status: "destroyed", Provider: "tencent-tke", ProviderRequestID: "storage-destroy-from-fabric", BillingStatus: "stopped"}, nil
}

func (f *fakeFabricClient) CreateStorageAttachment(_ context.Context, input clients.StorageAttachmentInput, _ string) (clients.StorageAttachment, error) {
	f.record("fabric.attachment")
	return clients.StorageAttachment{ID: "attachment-from-fabric", WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Status: "attached", Provider: "tencent-tke", ProviderAttachmentID: "deployment/opl-compute-from-fabric:pvc/volume-from-fabric-data:/data", ProviderRequestID: "attachment-request-from-fabric", MountPath: "/data"}, nil
}

func (f *fakeFabricClient) DetachStorageAttachment(_ context.Context, id string, _ string) (clients.StorageAttachment, error) {
	f.record("fabric.attachment-detach")
	return clients.StorageAttachment{ID: id, Status: "detached", ProviderRequestID: "attachment-detach-from-fabric"}, nil
}

func (f *fakeFabricClient) CreateWorkspaceRuntime(_ context.Context, input clients.WorkspaceRuntimeInput, _ string) (clients.WorkspaceRuntime, error) {
	f.record("fabric.runtime")
	return clients.WorkspaceRuntime{ID: "runtime-from-fabric", WorkspaceID: input.WorkspaceID, URL: "https://workspace.medopl.cn/w/ws-from-fabric/", ServiceName: "opl-compute-from-fabric", Access: clients.WorkspaceRuntimeAccess{Username: "admin", Password: "runtime-password-alpha"}}, nil
}

func (f *fakeFabricClient) WorkspaceRuntimeStatus(_ context.Context, workspaceID string) (clients.WorkspaceRuntime, error) {
	f.record("fabric.runtime-status")
	return clients.WorkspaceRuntime{
		ID:          "runtime-from-fabric",
		WorkspaceID: workspaceID,
		URL:         "https://workspace.medopl.cn/w/" + workspaceID + "/",
		Status:      "unready",
		ServiceName: "opl-compute-from-fabric",
		Ready:       false,
		Checks: []any{
			map[string]any{"name": "deployment_ready", "ok": true},
			map[string]any{"name": "service_endpoints_ready", "ok": false},
		},
	}, nil
}

func (f *fakeFabricClient) Readiness(_ context.Context) (map[string]any, error) {
	f.record("fabric.readiness")
	return map[string]any{"provider": "tencent-tke", "ready": true, "missingEnv": []string{}, "missingTools": []string{}}, nil
}

func (f *fakeFabricClient) ListOperations(_ context.Context) ([]clients.FabricOperation, error) {
	f.record("fabric.operations")
	return []clients.FabricOperation{{
		ID:                "fop-alpha",
		OperationID:       "op-create-compute-alpha",
		CallerService:     "control-plane",
		Action:            "create_compute_allocation",
		ResourceKind:      "compute_allocation",
		ResourceID:        "compute-alpha",
		AccountID:         "acct-alpha",
		WorkspaceID:       "ws-alpha",
		Provider:          "tencent-tke",
		ProviderRequestID: "compute-request-from-fabric",
		RequestHash:       "request-hash-alpha",
		Status:            "succeeded",
		StartedAt:         "2026-07-07T00:00:00Z",
		FinishedAt:        "2026-07-07T00:01:00Z",
		CreatedAt:         "2026-07-07T00:01:00Z",
	}}, nil
}

type fabricClientWithResourceOperations struct {
	fakeFabricClient
}

func (f *fabricClientWithResourceOperations) ListOperations(_ context.Context) ([]clients.FabricOperation, error) {
	f.record("fabric.operations")
	return []clients.FabricOperation{
		{
			ID:                "fop-compute-alpha",
			OperationID:       "op-create-compute-alpha",
			CallerService:     "control-plane",
			Action:            "create_compute_allocation",
			ResourceKind:      "compute_allocation",
			ResourceID:        "compute-alpha",
			AccountID:         "acct-alpha",
			WorkspaceID:       "ws-alpha",
			Provider:          "tencent-tke",
			ProviderRequestID: "compute-request-from-fabric",
			RequestHash:       "request-hash-alpha",
			RedactedProviderPayload: map[string]any{"resource": map[string]any{
				"id":                 "compute-alpha",
				"accountId":          "acct-alpha",
				"workspaceId":        "ws-alpha",
				"packageId":          "basic",
				"status":             "running",
				"provider":           "tencent-tke",
				"providerResourceId": "node/node-from-fabric",
				"providerRequestId":  "compute-request-from-fabric",
				"nodeName":           "node-from-fabric",
			}},
			Status:     "succeeded",
			StartedAt:  "2026-07-07T00:00:00Z",
			FinishedAt: "2026-07-07T00:01:00Z",
			CreatedAt:  "2026-07-07T00:01:00Z",
		},
		{
			ID:                "fop-storage-alpha",
			OperationID:       "op-create-storage-alpha",
			CallerService:     "control-plane",
			Action:            "create_storage_volume",
			ResourceKind:      "storage_volume",
			ResourceID:        "storage-alpha",
			AccountID:         "acct-alpha",
			WorkspaceID:       "ws-alpha",
			Provider:          "tencent-tke",
			ProviderRequestID: "storage-request-from-fabric",
			RequestHash:       "request-hash-storage-alpha",
			RedactedProviderPayload: map[string]any{"resource": map[string]any{
				"id":                 "storage-alpha",
				"accountId":          "acct-alpha",
				"workspaceId":        "ws-alpha",
				"status":             "ready",
				"provider":           "tencent-tke",
				"providerResourceId": "pvc/storage-alpha-data",
				"providerRequestId":  "storage-request-from-fabric",
				"sizeGb":             10,
			}},
			Status:     "succeeded",
			StartedAt:  "2026-07-07T00:00:00Z",
			FinishedAt: "2026-07-07T00:01:00Z",
			CreatedAt:  "2026-07-07T00:01:01Z",
		},
		{
			ID:                "fop-attachment-alpha",
			OperationID:       "op-attach-alpha",
			CallerService:     "control-plane",
			Action:            "create_storage_attachment",
			ResourceKind:      "storage_attachment",
			ResourceID:        "attachment-alpha",
			AccountID:         "acct-alpha",
			WorkspaceID:       "ws-alpha",
			Provider:          "tencent-tke",
			ProviderRequestID: "attachment-request-from-fabric",
			RequestHash:       "request-hash-attachment-alpha",
			RedactedProviderPayload: map[string]any{"resource": map[string]any{
				"id":                   "attachment-alpha",
				"workspaceId":          "ws-alpha",
				"computeId":            "compute-alpha",
				"volumeId":             "storage-alpha",
				"status":               "attached",
				"provider":             "tencent-tke",
				"providerAttachmentId": "deployment/compute-alpha:pvc/storage-alpha-data:/data",
				"providerRequestId":    "attachment-request-from-fabric",
			}},
			Status:     "succeeded",
			StartedAt:  "2026-07-07T00:00:00Z",
			FinishedAt: "2026-07-07T00:01:00Z",
			CreatedAt:  "2026-07-07T00:01:02Z",
		},
	}, nil
}

func createResource(t *testing.T, server http.Handler, method string, path string, body string) map[string]any {
	t.Helper()
	return createResourceWithSession(t, server, operatorSessionForTest(t, server), method, path, body)
}

func createResourceWithSession(t *testing.T, server http.Handler, loginRec *httptest.ResponseRecorder, method string, path string, body string) map[string]any {
	t.Helper()
	rec := requestWithSession(t, server, loginRec, method, path, body)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("%s %s status = %d: %s", method, path, rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode %s %s: %v", method, path, err)
	}
	return payload
}

func requestWithSession(t *testing.T, server http.Handler, loginRec *httptest.ResponseRecorder, method string, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "test-"+path)
	addAuth(req, loginRec)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func operatorSessionForTest(t *testing.T, server http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	t.Setenv("OPL_OPERATOR_SUMMARY_TOKEN", "operator-secret")
	req := httptest.NewRequest(http.MethodPost, "/api/auth/operator-login", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-opl-operator-token", "operator-secret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("operator login status = %d: %s", rec.Code, rec.Body.String())
	}
	return rec
}

func addSessionCookies(req *http.Request, loginRec *httptest.ResponseRecorder) {
	for _, cookie := range loginRec.Result().Cookies() {
		req.AddCookie(cookie)
	}
}

func addAuth(req *http.Request, loginRec *httptest.ResponseRecorder) {
	addSessionCookies(req, loginRec)
	req.Header.Set("x-opl-csrf", loginRec.Header().Get("x-opl-csrf-token"))
}

func TestCreateComputeAllocationUsesFabricService(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	req := httptest.NewRequest(http.MethodPost, "/api/compute-allocations", bytes.NewBufferString(`{"accountId":"acct-alpha","packageId":"basic","name":"Production Compute"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "compute-once")
	addAuth(req, operatorSessionForTest(t, server))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if stringValue(body["id"]) == "" || body["providerRequestId"] != "compute-request-from-fabric" || body["holdId"] != "hold-from-ledger" {
		t.Fatalf("compute allocation did not come from Fabric: %#v", body)
	}
	if body["provider"] != "tencent-tke" || body["billingStatus"] != "active" || body["nodeName"] != "node-from-fabric" || body["instanceId"] != "ins-from-fabric" {
		t.Fatalf("compute allocation missing route contract fields: %#v", body)
	}
	getReq := httptest.NewRequest(http.MethodGet, "/api/compute-allocations/"+stringValue(body["id"]), nil)
	addSessionCookies(getReq, operatorSessionForTest(t, server))
	getRec := httptest.NewRecorder()
	server.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d: %s", getRec.Code, http.StatusOK, getRec.Body.String())
	}
}

func TestManagementStateIncludesResourceLedgerEvidenceChain(t *testing.T) {
	app := newControlPlaneApp()
	app.mu.Lock()
	app.workspaces["ws-alpha"] = map[string]any{
		"id":                         "ws-alpha",
		"ownerAccountId":             "acct-alpha",
		"ownerUserId":                "usr-alpha",
		"currentComputeAllocationId": "compute-alpha",
		"currentAttachmentId":        "attach-alpha",
		"storageId":                  "storage-alpha",
	}
	app.computes["compute-alpha"] = map[string]any{
		"id":             "compute-alpha",
		"ownerAccountId": "acct-alpha",
		"ownerUserId":    "usr-alpha",
		"cvmInstanceId":  "ins-alpha",
		"nodeName":       "node-alpha",
	}
	app.storages["storage-alpha"] = map[string]any{
		"id":             "storage-alpha",
		"ownerAccountId": "acct-alpha",
	}
	ledger := app.addLedgerLocked("acct-alpha", "compute_debit", map[string]any{"workspaceId": "ws-alpha", "computeAllocationId": "compute-alpha"})
	app.addWalletTxLocked("acct-alpha", "compute_debit", map[string]any{"workspaceId": "ws-alpha", "computeAllocationId": "compute-alpha"})
	wallet := app.walletTx[len(app.walletTx)-1]
	app.mu.Unlock()

	state := app.managementState(true)
	rows := state["resourceLedgerEvidence"].([]any)
	if len(rows) != 1 {
		t.Fatalf("resourceLedgerEvidence rows = %d, want 1: %#v", len(rows), rows)
	}
	row := rows[0].(map[string]any)
	if row["ownerAccountId"] != "acct-alpha" || row["ownerUserId"] != "usr-alpha" || row["cvmInstanceId"] != "ins-alpha" || row["nodeName"] != "node-alpha" || row["storageId"] != "storage-alpha" {
		t.Fatalf("unexpected ownership evidence: %#v", row)
	}
	if !slices.Contains(row["workspaceIds"].([]string), "ws-alpha") {
		t.Fatalf("workspaceIds missing ws-alpha: %#v", row["workspaceIds"])
	}
	if !slices.Contains(row["ledgerEntryIds"].([]string), ledger["id"].(string)) {
		t.Fatalf("ledgerEntryIds missing ledger id: %#v", row["ledgerEntryIds"])
	}
	if !slices.Contains(row["walletTransactionIds"].([]string), wallet["id"].(string)) {
		t.Fatalf("walletTransactionIds missing wallet id: %#v", row["walletTransactionIds"])
	}
}

func TestConsoleStateIncludesResourceLedgerEvidenceChain(t *testing.T) {
	app := newControlPlaneApp()
	app.mu.Lock()
	app.workspaces["ws-replacement"] = map[string]any{
		"id":                         "ws-replacement",
		"ownerAccountId":             "acct-alpha",
		"currentComputeAllocationId": "compute-replacement",
		"currentAttachmentId":        "attach-replacement",
		"storageId":                  "storage-alpha",
	}
	app.computes["compute-replacement"] = map[string]any{"id": "compute-replacement", "ownerAccountId": "acct-alpha", "status": "running", "billingStatus": "active", "hourlyPrice": 1.25}
	app.storages["storage-alpha"] = map[string]any{"id": "storage-alpha", "ownerAccountId": "acct-alpha", "status": "available", "billingStatus": "active", "hourlyEstimate": 0.25}
	app.attachments["attach-replacement"] = map[string]any{"id": "attach-replacement", "ownerAccountId": "acct-alpha"}
	app.runtimeOps = []map[string]any{{
		"operationId":  "op-runtime-replacement",
		"resourceKind": "workspace_runtime",
		"resourceId":   "ws-replacement",
		"workspaceId":  "ws-replacement",
		"status":       "succeeded",
		"redactedProviderPayload": map[string]any{
			"costTags": map[string]any{
				"opl_account_id":   "acct-alpha",
				"opl_workspace_id": "ws-replacement",
				"opl_resource_id":  "ws-replacement",
				"opl_operation_id": "op-runtime-replacement",
			},
		},
	}}
	computeLedger := app.addLedgerLocked("acct-alpha", "compute_debit", map[string]any{"workspaceId": "ws-replacement", "computeAllocationId": "compute-replacement", "amountCents": -250})
	storageLedger := app.addLedgerLocked("acct-alpha", "storage_debit", map[string]any{"workspaceId": "ws-replacement", "storageId": "storage-alpha", "amountCents": -125})
	app.addWalletTxLocked("acct-alpha", "compute_debit", map[string]any{"workspaceId": "ws-replacement", "computeAllocationId": "compute-replacement", "amountCents": -250})
	app.addWalletTxLocked("acct-alpha", "storage_debit", map[string]any{"workspaceId": "ws-replacement", "storageId": "storage-alpha", "amountCents": -125})
	app.mu.Unlock()

	state := app.state("acct-alpha")
	rows := state["resourceLedgerEvidence"].([]any)
	if len(rows) != 1 {
		t.Fatalf("resourceLedgerEvidence rows = %d, want 1: %#v", len(rows), rows)
	}
	row := rows[0].(map[string]any)
	if row["operationId"] != "op-runtime-replacement" {
		t.Fatalf("row missing runtime operation id: %#v", row)
	}
	tags, _ := row["costTags"].(map[string]any)
	if tags["opl_account_id"] != "acct-alpha" || tags["opl_workspace_id"] != "ws-replacement" || tags["opl_operation_id"] != "op-runtime-replacement" {
		t.Fatalf("row missing provider cost tags: %#v", row)
	}
	ledgerIDs := row["ledgerEntryIds"].([]string)
	if !slices.Contains(ledgerIDs, computeLedger["id"].(string)) || !slices.Contains(ledgerIDs, storageLedger["id"].(string)) {
		t.Fatalf("row missing settlement ledger links: %#v", row)
	}
	if len(row["walletTransactionIds"].([]string)) != 2 {
		t.Fatalf("row missing settlement wallet links: %#v", row)
	}
	summary := state["billingSummary"].(map[string]any)
	if summary["activeHourlyEstimate"] != float64(1.5) || summary["recentResourceDebitTotal"] != float64(3.75) {
		t.Fatalf("state missing backend billing summary: %#v", summary)
	}
	workspace := state["workspaces"].([]any)[0].(map[string]any)
	billing := workspace["billing"].(map[string]any)
	if billing["activeHourlyEstimate"] != float64(1.5) || billing["currentChargeTotal"] != float64(3.75) {
		t.Fatalf("workspace missing backend billing facts: %#v", workspace)
	}
}

func TestResourceDestroyAndDetachUpdateWorkspaceState(t *testing.T) {
	app := newControlPlaneApp()
	app.workspaces["ws-alpha"] = map[string]any{
		"id":                         "ws-alpha",
		"ownerAccountId":             "acct-alpha",
		"state":                      "running",
		"status":                     "running",
		"currentComputeAllocationId": "compute-alpha",
		"computeAllocationId":        "compute-alpha",
		"storageId":                  "storage-alpha",
		"currentAttachmentId":        "attach-alpha",
		"attachmentId":               "attach-alpha",
		"runtime":                    map[string]any{"serviceName": "runtime-alpha"},
		"access":                     map[string]any{"tokenStatus": "active"},
	}

	app.rememberCompute(map[string]any{
		"id":              "compute-alpha",
		"accountId":       "acct-alpha",
		"status":          "destroyed",
		"holdId":          "hold-compute",
		"holdReleaseId":   "release-compute",
		"holdAmountCents": 7862,
		"wallet":          map[string]any{"accountId": "acct-alpha", "balanceCents": 20000, "frozenCents": 0, "availableCents": 20000, "currency": "CNY"},
	})
	workspace := app.workspaces["ws-alpha"]
	if workspace["state"] != "suspended" || workspace["currentComputeAllocationId"] != "" {
		t.Fatalf("compute destroy did not suspend and clear compute pointer: %#v", workspace)
	}

	app.rememberAttachment(map[string]any{"id": "attach-alpha", "status": "detached"}, map[string]any{})
	if workspace["currentAttachmentId"] != "" || workspace["attachmentId"] != "" {
		t.Fatalf("attachment detach did not clear workspace pointer: %#v", workspace)
	}

	app.rememberStorage(map[string]any{
		"id":              "storage-alpha",
		"accountId":       "acct-alpha",
		"status":          "destroyed",
		"holdId":          "hold-storage",
		"holdReleaseId":   "release-storage",
		"holdAmountCents": 101,
		"wallet":          map[string]any{"accountId": "acct-alpha", "balanceCents": 20000, "frozenCents": 0, "availableCents": 20000, "currency": "CNY"},
	})
	access, ok := workspace["access"].(map[string]any)
	if workspace["state"] != "data_deleted" || workspace["status"] != "unrecoverable" || !ok || access["tokenStatus"] != "disabled" {
		t.Fatalf("storage destroy did not mark workspace unrecoverable: %#v", workspace)
	}
	if len(app.ledger) != 2 || app.ledger[0]["type"] != "compute_hold_released" || app.ledger[1]["type"] != "storage_hold_released" {
		t.Fatalf("missing hold release ledger projection: %#v", app.ledger)
	}
}

func TestManagementStateUsesRealAccountsAndLedger(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))

	createResource(t, server, http.MethodPost, "/api/users", `{"email":"owner@lab.example","accountId":"acct-alpha","role":"pi","password":"CorrectHorseBatteryStaple!"}`)
	createResource(t, server, http.MethodPost, "/api/billing/topups", `{"accountId":"acct-alpha","amount":100,"idempotencyKey":"topup-alpha","confirm":true}`)
	createResource(t, server, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	createResource(t, server, http.MethodPost, "/api/billing/resource-settlements", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","resourceType":"compute","resourceId":"compute-alpha","amountCents":123,"confirm":true}`)

	stateReq := httptest.NewRequest(http.MethodGet, "/api/management/state", nil)
	addSessionCookies(stateReq, operatorSessionForTest(t, server))
	stateRec := httptest.NewRecorder()
	server.ServeHTTP(stateRec, stateReq)
	if stateRec.Code != http.StatusOK {
		t.Fatalf("management state status = %d: %s", stateRec.Code, stateRec.Body.String())
	}
	var management map[string]any
	if err := json.NewDecoder(stateRec.Body).Decode(&management); err != nil {
		t.Fatalf("decode management state: %v", err)
	}
	accounts := management["accounts"].([]any)
	if !slices.ContainsFunc(accounts, func(item any) bool {
		account := item.(map[string]any)
		return account["accountId"] == "acct-alpha" && account["email"] == "owner@lab.example" && number(account["totalSpent"]) > 0
	}) {
		t.Fatalf("management accounts missing real account totals: %#v", accounts)
	}
	if len(management["manualTopups"].([]any)) == 0 || len(management["billingLedger"].([]any)) == 0 || len(management["walletTransactions"].([]any)) == 0 {
		t.Fatalf("management state missing ledger-backed admin rows: %#v", management)
	}

	operatorReq := httptest.NewRequest(http.MethodGet, "/api/operator/summary", nil)
	addSessionCookies(operatorReq, operatorSessionForTest(t, server))
	operatorRec := httptest.NewRecorder()
	server.ServeHTTP(operatorRec, operatorReq)
	if operatorRec.Code != http.StatusOK {
		t.Fatalf("operator summary status = %d: %s", operatorRec.Code, operatorRec.Body.String())
	}
	var operator map[string]any
	if err := json.NewDecoder(operatorRec.Body).Decode(&operator); err != nil {
		t.Fatalf("decode operator summary: %v", err)
	}
	operatorAccounts := operator["accounts"].(map[string]any)
	if number(operatorAccounts["total"]) < 1 || number(operatorAccounts["totalSpent"]) <= 0 {
		t.Fatalf("operator accounts did not use real totals: %#v", operatorAccounts)
	}
}

func TestCleanupWorkspaceAccessDisablesInvalidActiveURL(t *testing.T) {
	app := newControlPlaneApp()
	app.workspaces["ws-alpha"] = map[string]any{
		"id":             "ws-alpha",
		"ownerAccountId": "acct-alpha",
		"storageId":      "missing-storage",
		"access":         map[string]any{"tokenStatus": "active"},
	}

	result, err := app.cleanupWorkspaceAccess(map[string]any{"reason": "test"})
	if err != nil {
		t.Fatalf("cleanup workspace access: %v", err)
	}
	if len(result["cleaned"].([]any)) != 1 || nested(app.workspaces["ws-alpha"], "access", "tokenStatus") != "disabled" {
		t.Fatalf("cleanup did not disable invalid URL: result=%#v workspace=%#v", result, app.workspaces["ws-alpha"])
	}
}

func TestResourceSettlementProjectsLedgerEvidenceIntoConsoleState(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))

	createResource(t, server, http.MethodPost, "/api/billing/resource-settlements", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","resourceType":"compute","resourceId":"compute-alpha","amountCents":123,"confirm":true}`)
	createResource(t, server, http.MethodPost, "/api/billing/resource-settlements", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","resourceType":"storage","resourceId":"storage-alpha","amountCents":45,"confirm":true}`)
	req := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-alpha", nil)
	addSessionCookies(req, operatorSessionForTest(t, server))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("state status = %d: %s", rec.Code, rec.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}

	ledger := state["billingLedger"].([]any)
	if !slices.ContainsFunc(ledger, func(row any) bool {
		entry := row.(map[string]any)
		return entry["type"] == "compute_debit" && entry["computeAllocationId"] == "compute-alpha" && entry["workspaceId"] == "ws-alpha"
	}) {
		t.Fatalf("missing compute debit ledger projection: %#v", ledger)
	}
	if !slices.ContainsFunc(ledger, func(row any) bool {
		entry := row.(map[string]any)
		return entry["type"] == "storage_debit" && entry["storageId"] == "storage-alpha" && entry["workspaceId"] == "ws-alpha"
	}) {
		t.Fatalf("missing storage debit ledger projection: %#v", ledger)
	}
	if _, ok := state["resourceUsageLogs"]; ok {
		t.Fatalf("state must not expose retired resource usage projection: %#v", state["resourceUsageLogs"])
	}
	walletTx := state["walletTransactions"].([]any)
	if len(walletTx) != 2 {
		t.Fatalf("wallet transaction rows = %d, want 2: %#v", len(walletTx), walletTx)
	}
	runtimeOps := state["runtimeOperations"].([]any)
	if !slices.ContainsFunc(runtimeOps, func(row any) bool {
		operation := row.(map[string]any)
		return operation["operationId"] == "op-create-compute-alpha" && operation["providerRequestId"] == "compute-request-from-fabric" && operation["requestHash"] == "request-hash-alpha"
	}) {
		t.Fatalf("state missing Fabric runtime operation facts: %#v", runtimeOps)
	}
	if _, ok := state["audit"]; ok {
		t.Fatalf("state must not expose empty audit facts when no audit source is synced: %#v", state["audit"])
	}
	if _, ok := state["evidenceLedger"]; ok {
		t.Fatalf("state must not expose empty evidence facts when no evidence source is synced: %#v", state["evidenceLedger"])
	}
}

func TestConsoleStateHydratesResourceListsFromFabricOperations(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fabricClientWithResourceOperations{}))

	req := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-alpha", nil)
	addSessionCookies(req, operatorSessionForTest(t, server))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("state status = %d: %s", rec.Code, rec.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	computes := state["computeAllocations"].([]any)
	if !slices.ContainsFunc(computes, func(row any) bool {
		compute := row.(map[string]any)
		return compute["id"] == "compute-alpha" && compute["status"] == "running" && compute["nodeName"] == "node-from-fabric"
	}) {
		t.Fatalf("state did not hydrate compute resource from Fabric operation: %#v", computes)
	}
	storageVolumes := state["storageVolumes"].([]any)
	if !slices.ContainsFunc(storageVolumes, func(row any) bool {
		storage := row.(map[string]any)
		return storage["id"] == "storage-alpha" && storage["status"] == "available" && storage["providerResourceId"] == "pvc/storage-alpha-data"
	}) {
		t.Fatalf("state did not hydrate storage resource from Fabric operation: %#v", storageVolumes)
	}
	attachments := state["storageAttachments"].([]any)
	if !slices.ContainsFunc(attachments, func(row any) bool {
		attachment := row.(map[string]any)
		return attachment["id"] == "attachment-alpha" &&
			attachment["ownerAccountId"] == "acct-alpha" &&
			attachment["computeAllocationId"] == "compute-alpha" &&
			attachment["storageId"] == "storage-alpha" &&
			attachment["status"] == "attached"
	}) {
		t.Fatalf("state did not hydrate attachment resource from Fabric operation: %#v", attachments)
	}
}

func TestResourceSettlementProjectionKeepsRequestIdentityWhenLedgerOmitsIt(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClientWithoutSettlementIdentity{}, &fakeFabricClient{}))

	createResource(t, server, http.MethodPost, "/api/billing/resource-settlements", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","resourceType":"compute","resourceId":"compute-alpha","amountCents":123,"confirm":true}`)
	req := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-alpha", nil)
	addSessionCookies(req, operatorSessionForTest(t, server))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("state status = %d: %s", rec.Code, rec.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}

	ledger := state["billingLedger"].([]any)
	if !slices.ContainsFunc(ledger, func(row any) bool {
		entry := row.(map[string]any)
		return entry["accountId"] == "acct-alpha" && entry["type"] == "compute_debit" && entry["computeAllocationId"] == "compute-alpha" && entry["workspaceId"] == "ws-alpha"
	}) {
		t.Fatalf("missing settlement request identity in ledger projection: %#v", ledger)
	}
	wallet := state["wallet"].(map[string]any)
	if wallet["accountId"] != "acct-alpha" {
		t.Fatalf("wallet lost settlement request account: %#v", wallet)
	}
}

func TestResourceSettlementPassesPriceAndEvidenceSnapshotToLedger(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	body := `{"accountId":"acct-alpha","workspaceId":"ws-alpha","resourceType":"compute","resourceId":"compute-alpha","amountCents":123,"currency":"CNY","pricingVersion":"opl-tencent-v1","priceSnapshot":{"unitPriceCents":123,"sku":"basic-cvm"},"usagePeriodStart":"2026-07-08T00:00:00Z","usagePeriodEnd":"2026-07-08T01:00:00Z","quantity":1,"unit":"hour","providerCostEvidenceRef":"fabric:op-alpha","confirm":true}`

	settlement := createResource(t, server, http.MethodPost, "/api/billing/resource-settlements", body)
	if settlement["pricingVersion"] != "opl-tencent-v1" || settlement["providerCostEvidenceRef"] != "fabric:op-alpha" {
		t.Fatalf("settlement response lost price/evidence fields: %#v", settlement)
	}
	priceSnapshot := settlement["priceSnapshot"].(map[string]any)
	if priceSnapshot["unitPriceCents"] != float64(123) {
		t.Fatalf("settlement response lost price facts: %#v", settlement)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-alpha", nil)
	addSessionCookies(req, operatorSessionForTest(t, server))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("state status = %d: %s", rec.Code, rec.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	ledger := state["billingLedger"].([]any)
	if !slices.ContainsFunc(ledger, func(row any) bool {
		entry := row.(map[string]any)
		facts, _ := entry["priceSnapshot"].(map[string]any)
		return entry["type"] == "compute_debit" && entry["pricingVersion"] == "opl-tencent-v1" && entry["providerCostEvidenceRef"] == "fabric:op-alpha" && facts["sku"] == "basic-cvm"
	}) {
		t.Fatalf("state ledger lost settlement evidence: %#v", ledger)
	}
}

func TestStateRefreshesLedgerFactsFromLedgerReads(t *testing.T) {
	server := NewServer(controlplane.NewService(readBackedLedgerClient{}, &fakeFabricClient{}))

	req := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-alpha", nil)
	addSessionCookies(req, operatorSessionForTest(t, server))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("state status = %d: %s", rec.Code, rec.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	wallet := state["wallet"].(map[string]any)
	if wallet["balanceCents"] != float64(9900) || wallet["totalSpentCents"] != float64(1200) {
		t.Fatalf("state wallet was not refreshed from Ledger: %#v", wallet)
	}
	ledger := state["billingLedger"].([]any)
	if !slices.ContainsFunc(ledger, func(row any) bool {
		entry := row.(map[string]any)
		priceSnapshot, _ := entry["priceSnapshot"].(map[string]any)
		return entry["type"] == "compute_debit" && entry["computeAllocationId"] == "compute-alpha" && priceSnapshot["unitPriceCents"] == float64(1200)
	}) {
		t.Fatalf("state missing Ledger settlement evidence: %#v", ledger)
	}
	transactions := state["walletTransactions"].([]any)
	if len(transactions) != 1 || transactions[0].(map[string]any)["availableCents"] != float64(8700) {
		t.Fatalf("state missing wallet after facts: %#v", transactions)
	}
	tx := transactions[0].(map[string]any)
	metadata, _ := tx["metadata"].(map[string]any)
	if tx["type"] != "compute_debit" || metadata["computeAllocationId"] != "compute-alpha" || metadata["settlementId"] != "settlement-alpha" {
		t.Fatalf("state wallet transaction missing settlement metadata: %#v", tx)
	}

	managementReq := httptest.NewRequest(http.MethodGet, "/api/management/state", nil)
	addSessionCookies(managementReq, operatorSessionForTest(t, server))
	managementRec := httptest.NewRecorder()
	server.ServeHTTP(managementRec, managementReq)
	if managementRec.Code != http.StatusOK {
		t.Fatalf("management state status = %d: %s", managementRec.Code, managementRec.Body.String())
	}
	var management map[string]any
	if err := json.NewDecoder(managementRec.Body).Decode(&management); err != nil {
		t.Fatalf("decode management state: %v", err)
	}
	if len(management["billingLedger"].([]any)) == 0 || len(management["walletTransactions"].([]any)) == 0 {
		t.Fatalf("management state did not expose Ledger read facts: %#v", management)
	}
}

func TestReconciliationGuardBlocksNewResourceProvisioning(t *testing.T) {
	var calls []string
	server := NewServer(controlplane.NewService(fakeBlockingReconciliationLedgerClient{}, &fakeFabricClient{calls: &calls}))
	session := operatorSessionForTest(t, server)

	createResourceWithSession(t, server, session, http.MethodPost, "/api/billing/reconciliation", `{"confirm":true,"report":{"id":"recon-mismatch","status":"mismatch"}}`)

	stateReq := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-alpha", nil)
	addAuth(stateReq, session)
	stateRec := httptest.NewRecorder()
	server.ServeHTTP(stateRec, stateReq)
	if stateRec.Code != http.StatusOK {
		t.Fatalf("state status = %d: %s", stateRec.Code, stateRec.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(stateRec.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	guard := state["billingReconciliation"].(map[string]any)["guard"].(map[string]any)
	if guard["blockNewWorkspaces"] != true || guard["reason"] != "tencent_bill_reconciliation_failed" {
		t.Fatalf("state missing blocking reconciliation guard: %#v", guard)
	}

	computeRec := requestWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	if computeRec.Code != http.StatusConflict {
		t.Fatalf("compute status = %d, want 409: %s", computeRec.Code, computeRec.Body.String())
	}
	storageRec := requestWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","sizeGb":10}`)
	if storageRec.Code != http.StatusConflict {
		t.Fatalf("storage status = %d, want 409: %s", storageRec.Code, storageRec.Body.String())
	}
	if slices.Contains(calls, "fabric.compute") || slices.Contains(calls, "fabric.storage") {
		t.Fatalf("reconciliation guard must block before Fabric mutation, calls=%#v", calls)
	}
}

func TestWorkspaceGatewayRoutesRootRuntimeApiByReferer(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_DOMAIN", "workspace.medopl.cn")
	var gotPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		writeJSON(w, http.StatusOK, map[string]string{"proxied": r.URL.Path})
	}))
	defer backend.Close()
	app := newControlPlaneApp()
	app.workspaces["ws-alpha"] = map[string]any{
		"runtime": map[string]any{"serviceName": strings.TrimPrefix(backend.URL, "http://")},
	}
	req := httptest.NewRequest(http.MethodPost, "https://workspace.medopl.cn/login", bytes.NewBufferString(`{"username":"admin"}`))
	req.Header.Set("Referer", "https://workspace.medopl.cn/w/ws-alpha/")
	rec := httptest.NewRecorder()

	app.proxyWorkspaceRoot(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gotPath != "/login" {
		t.Fatalf("proxied path = %q, want /login", gotPath)
	}
}

func TestWorkspaceGatewaySetsActiveCookieForRootRuntimeApi(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_DOMAIN", "workspace.medopl.cn")
	var gotPaths []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		writeJSON(w, http.StatusOK, map[string]string{"proxied": r.URL.Path})
	}))
	defer backend.Close()
	app := newControlPlaneApp()
	app.workspaces["ws-alpha"] = map[string]any{
		"runtime": map[string]any{"serviceName": strings.TrimPrefix(backend.URL, "http://")},
	}
	entryReq := httptest.NewRequest(http.MethodGet, "https://workspace.medopl.cn/w/ws-alpha/?token=share_alpha", nil)
	entryRec := httptest.NewRecorder()

	app.proxyWorkspace(entryRec, entryReq)

	if entryRec.Code != http.StatusFound {
		t.Fatalf("entry status = %d, want %d: %s", entryRec.Code, http.StatusFound, entryRec.Body.String())
	}
	if entryRec.Header().Get("Location") != "https://workspace.medopl.cn/w/ws-alpha/" {
		t.Fatalf("token entry must redirect to clean URL, got %q", entryRec.Header().Get("Location"))
	}
	cookies := entryRec.Result().Cookies()
	if !slices.ContainsFunc(cookies, func(cookie *http.Cookie) bool {
		return cookie.Name == "opl_ws_active" && cookie.Value == "ws-alpha"
	}) {
		t.Fatalf("entry response must set active workspace cookie, got %#v", cookies)
	}
	cleanReq := httptest.NewRequest(http.MethodGet, "https://workspace.medopl.cn/w/ws-alpha/", nil)
	for _, cookie := range cookies {
		cleanReq.AddCookie(cookie)
	}
	cleanRec := httptest.NewRecorder()
	app.proxyWorkspace(cleanRec, cleanReq)
	if cleanRec.Code != http.StatusOK {
		t.Fatalf("clean entry status = %d, want %d: %s", cleanRec.Code, http.StatusOK, cleanRec.Body.String())
	}
	apiReq := httptest.NewRequest(http.MethodGet, "https://workspace.medopl.cn/api/auth/user", nil)
	for _, cookie := range cookies {
		apiReq.AddCookie(cookie)
	}
	apiRec := httptest.NewRecorder()

	app.proxyWorkspaceRoot(apiRec, apiReq)

	if apiRec.Code != http.StatusOK {
		t.Fatalf("api status = %d, want %d: %s", apiRec.Code, http.StatusOK, apiRec.Body.String())
	}
	if !slices.Equal(gotPaths, []string{"/", "/api/auth/user"}) {
		t.Fatalf("proxied paths = %#v, want entry and root API paths", gotPaths)
	}
}

func TestWorkspaceGatewayBlocksInactiveWorkspace(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_DOMAIN", "workspace.medopl.cn")
	for _, tc := range []struct {
		name string
		row  map[string]any
		want int
	}{
		{name: "suspended", row: map[string]any{"state": "suspended", "runtime": map[string]any{"serviceName": "runtime-alpha"}}, want: http.StatusConflict},
		{name: "data deleted", row: map[string]any{"state": "data_deleted", "runtime": map[string]any{"serviceName": "runtime-alpha"}}, want: http.StatusGone},
		{name: "access disabled", row: map[string]any{"state": "running", "access": map[string]any{"tokenStatus": "disabled"}, "runtime": map[string]any{"serviceName": "runtime-alpha"}}, want: http.StatusGone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newControlPlaneApp()
			app.workspaces["ws-alpha"] = tc.row
			req := httptest.NewRequest(http.MethodGet, "https://workspace.medopl.cn/w/ws-alpha/", nil)
			rec := httptest.NewRecorder()

			app.proxyWorkspace(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestOverviewHTTP(t *testing.T) {
	server := NewServer(controlplane.NewService(nil, nil))
	req := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	addSessionCookies(req, operatorSessionForTest(t, server))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if body["service"] != "control-plane" {
		t.Fatalf("service = %v, want control-plane", body["service"])
	}
}

func TestOperatorLoginUsesConfiguredToken(t *testing.T) {
	t.Setenv("OPL_OPERATOR_SUMMARY_TOKEN", "operator-secret")
	server := NewServer(controlplane.NewService(nil, nil))
	req := httptest.NewRequest(http.MethodPost, "/api/auth/operator-login", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-opl-operator-token", "operator-secret")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Header().Get("x-opl-csrf-token") == "" {
		t.Fatalf("expected csrf response header")
	}
	if rec.Header().Get("Set-Cookie") == "" {
		t.Fatalf("expected session cookie")
	}
}

func TestOperatorLoginRejectsInvalidToken(t *testing.T) {
	t.Setenv("OPL_OPERATOR_SUMMARY_TOKEN", "operator-secret")
	server := NewServer(controlplane.NewService(nil, nil))
	req := httptest.NewRequest(http.MethodPost, "/api/auth/operator-login", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-opl-operator-token", "wrong")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestOperatorLoginRateLimitsInvalidToken(t *testing.T) {
	t.Setenv("OPL_OPERATOR_SUMMARY_TOKEN", "operator-secret")
	server := NewServer(controlplane.NewService(nil, nil))

	var rec *httptest.ResponseRecorder
	for range 6 {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/operator-login", bytes.NewBufferString(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-opl-operator-token", "wrong")
		req.RemoteAddr = "203.0.113.10:4242"
		rec = httptest.NewRecorder()
		server.ServeHTTP(rec, req)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("operator login status = %d, want 429: %s", rec.Code, rec.Body.String())
	}
}

func TestProtectedWriteRejectsOversizedJSONBody(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	session := operatorSessionForTest(t, server)
	body := `{"accountId":"acct-alpha","packageId":"` + strings.Repeat("x", int(maxJSONBodyBytes)+1) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/compute-allocations", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "oversized-body")
	addAuth(req, session)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", rec.Code, rec.Body.String())
	}
}

func TestUpstreamErrorsDoNotLeakProviderDetails(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &failingFabricClient{}))
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/readiness", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "upstream_unavailable") || strings.Contains(rec.Body.String(), "secret leaked") {
		t.Fatalf("upstream error leaked provider details: %s", rec.Body.String())
	}
}

func TestReadinessRoutesArePublicButAdminRoutesStayProtected(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))

	for _, path := range []string{"/api/runtime/readiness", "/api/production/readiness"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200: %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"ready":true`) {
			t.Fatalf("%s body missing readiness fact: %s", path, rec.Body.String())
		}
	}

	adminReq := httptest.NewRequest(http.MethodGet, "/api/management/state", nil)
	adminRec := httptest.NewRecorder()
	server.ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusUnauthorized {
		t.Fatalf("admin route without session status = %d, want 401: %s", adminRec.Code, adminRec.Body.String())
	}
}

func TestProtectedAPIRoutesRequireSessionCSRFAndAdminRole(t *testing.T) {
	t.Setenv("OPL_OPERATOR_SUMMARY_TOKEN", "operator-secret")
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))

	postWithoutSession := httptest.NewRequest(http.MethodPost, "/api/compute-allocations", bytes.NewBufferString(`{"accountId":"acct-alpha","packageId":"basic"}`))
	postWithoutSession.Header.Set("Content-Type", "application/json")
	postWithoutSession.Header.Set("Idempotency-Key", "compute-no-session")
	postWithoutSessionRec := httptest.NewRecorder()
	server.ServeHTTP(postWithoutSessionRec, postWithoutSession)
	if postWithoutSessionRec.Code != http.StatusUnauthorized {
		t.Fatalf("write without session status = %d, want 401: %s", postWithoutSessionRec.Code, postWithoutSessionRec.Body.String())
	}

	admin := operatorSessionForTest(t, server)
	postWithoutCSRF := httptest.NewRequest(http.MethodPost, "/api/compute-allocations", bytes.NewBufferString(`{"accountId":"acct-alpha","packageId":"basic"}`))
	postWithoutCSRF.Header.Set("Content-Type", "application/json")
	postWithoutCSRF.Header.Set("Idempotency-Key", "compute-no-csrf")
	addSessionCookies(postWithoutCSRF, admin)
	postWithoutCSRFRec := httptest.NewRecorder()
	server.ServeHTTP(postWithoutCSRFRec, postWithoutCSRF)
	if postWithoutCSRFRec.Code != http.StatusForbidden {
		t.Fatalf("write without csrf status = %d, want 403: %s", postWithoutCSRFRec.Code, postWithoutCSRFRec.Body.String())
	}

	createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"owner@lab.example","accountId":"acct-alpha","role":"pi","password":"CorrectHorseBatteryStaple!"}`)
	owner := loginForTest(t, server, "owner@lab.example", "CorrectHorseBatteryStaple!")
	adminReq := httptest.NewRequest(http.MethodPost, "/api/billing/topups", bytes.NewBufferString(`{"accountId":"acct-alpha","amount":100,"idempotencyKey":"topup-nonadmin"}`))
	adminReq.Header.Set("Content-Type", "application/json")
	adminReq.Header.Set("Idempotency-Key", "topup-nonadmin")
	addSessionCookies(adminReq, owner)
	adminReq.Header.Set("x-opl-csrf", owner.Header().Get("x-opl-csrf-token"))
	adminReqRec := httptest.NewRecorder()
	server.ServeHTTP(adminReqRec, adminReq)
	if adminReqRec.Code != http.StatusForbidden {
		t.Fatalf("admin route as owner status = %d, want 403: %s", adminReqRec.Code, adminReqRec.Body.String())
	}

	allowed := createResourceWithSession(t, server, admin, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	if allowed["providerRequestId"] != "compute-request-from-fabric" {
		t.Fatalf("admin csrf request did not reach protected route: %#v", allowed)
	}
}

func TestHighRiskMutationsRequireBackendConfirmation(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := operatorSessionForTest(t, server)
	created := createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"member@lab.example","accountId":"acct-alpha","role":"pi","password":"CorrectHorseBatteryStaple!"}`)

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/billing/topups", `{"accountId":"acct-alpha","amount":100,"idempotencyKey":"topup-confirm"}`},
		{http.MethodPost, "/api/billing/resource-settlements", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","resourceType":"compute","resourceId":"compute-alpha","amountCents":123}`},
		{http.MethodPost, "/api/billing/reconciliation", `{"report":{"id":"recon-confirm","generatedAt":"2026-07-06T00:00:00Z"}}`},
		{http.MethodPost, "/api/users/delete", `{"userId":"` + stringValue(created["id"]) + `","reason":"left_lab"}`},
		{http.MethodPost, "/api/compute-allocations/compute-alpha/destroy", `{}`},
		{http.MethodPost, "/api/storage-volumes/destroy", `{"storageId":"storage-alpha"}`},
		{http.MethodPost, "/api/operator/cleanup-workspace-access", `{"reason":"test"}`},
	} {
		rec := requestWithSession(t, server, admin, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "confirmation_required") {
			t.Fatalf("%s %s status=%d body=%s, want confirmation_required", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestLoginSessionMeAndLogoutUseStoredPasswordHash(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	createResource(t, server, http.MethodPost, "/api/users", `{"email":"owner@lab.example","accountId":"acct-alpha","role":"admin","password":"CorrectHorseBatteryStaple!"}`)

	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"email":"owner@lab.example","password":"CorrectHorseBatteryStaple!"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	server.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", loginRec.Code, loginRec.Body.String())
	}
	if loginRec.Header().Get("x-opl-csrf-token") == "" || len(loginRec.Result().Cookies()) == 0 {
		t.Fatalf("login must issue csrf and session cookie")
	}
	var loginPayload map[string]any
	if err := json.NewDecoder(loginRec.Body).Decode(&loginPayload); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	user := loginPayload["user"].(map[string]any)
	if user["passwordHash"] != nil || user["email"] != "owner@lab.example" {
		t.Fatalf("login leaked credentials or wrong user: %#v", user)
	}

	meReq := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	for _, cookie := range loginRec.Result().Cookies() {
		meReq.AddCookie(cookie)
	}
	meRec := httptest.NewRecorder()
	server.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("me status = %d: %s", meRec.Code, meRec.Body.String())
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/api/auth/logout", bytes.NewBufferString(`{}`))
	for _, cookie := range loginRec.Result().Cookies() {
		logoutReq.AddCookie(cookie)
	}
	logoutReq.Header.Set("x-opl-csrf", loginRec.Header().Get("x-opl-csrf-token"))
	logoutRec := httptest.NewRecorder()
	server.ServeHTTP(logoutRec, logoutReq)
	if logoutRec.Code != http.StatusOK {
		t.Fatalf("logout status = %d: %s", logoutRec.Code, logoutRec.Body.String())
	}
	afterLogoutReq := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	for _, cookie := range loginRec.Result().Cookies() {
		afterLogoutReq.AddCookie(cookie)
	}
	afterLogoutRec := httptest.NewRecorder()
	server.ServeHTTP(afterLogoutRec, afterLogoutReq)
	if afterLogoutRec.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout status = %d, want 401", afterLogoutRec.Code)
	}

	managementReq := httptest.NewRequest(http.MethodGet, "/api/management/state", nil)
	addSessionCookies(managementReq, operatorSessionForTest(t, server))
	managementRec := httptest.NewRecorder()
	server.ServeHTTP(managementRec, managementReq)
	var management map[string]any
	if err := json.NewDecoder(managementRec.Body).Decode(&management); err != nil {
		t.Fatalf("decode management: %v", err)
	}
	managementUser := management["users"].([]any)[0].(map[string]any)
	if managementUser["passwordHash"] != nil {
		t.Fatalf("management state leaked password hash: %#v", managementUser)
	}
}

func TestLoginRateLimitBlocksRepeatedFailuresAndResetsAfterSuccess(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	createResource(t, server, http.MethodPost, "/api/users", `{"email":"owner@lab.example","accountId":"acct-alpha","role":"admin","password":"CorrectHorseBatteryStaple!"}`)

	for i := 0; i < 2; i++ {
		rec := loginAttemptForTest(server, "owner@lab.example", "wrong", "203.0.113.10:1000")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("warmup failure %d status = %d, want 401: %s", i, rec.Code, rec.Body.String())
		}
	}
	if rec := loginAttemptForTest(server, "owner@lab.example", "CorrectHorseBatteryStaple!", "203.0.113.10:1000"); rec.Code != http.StatusOK {
		t.Fatalf("successful login did not reset limiter: status=%d body=%s", rec.Code, rec.Body.String())
	}
	for i := 0; i < 5; i++ {
		rec := loginAttemptForTest(server, "owner@lab.example", "wrong", "203.0.113.10:1000")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d status = %d, want 401 before limit: %s", i, rec.Code, rec.Body.String())
		}
	}
	blocked := loginAttemptForTest(server, "owner@lab.example", "wrong", "203.0.113.10:1000")
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked status = %d, want 429: %s", blocked.Code, blocked.Body.String())
	}
	otherIP := loginAttemptForTest(server, "owner@lab.example", "CorrectHorseBatteryStaple!", "203.0.113.11:1000")
	if otherIP.Code != http.StatusOK {
		t.Fatalf("rate limit must be scoped to email and IP: status=%d body=%s", otherIP.Code, otherIP.Body.String())
	}
}

func TestUserDeleteLifecycleReturnsNotFoundWithoutStub(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	req := httptest.NewRequest(http.MethodPost, "/api/users/delete", bytes.NewBufferString(`{"userId":"usr-missing","reason":"test","confirm":true}`))
	req.Header.Set("Content-Type", "application/json")
	addAuth(req, operatorSessionForTest(t, server))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	stateReq := httptest.NewRequest(http.MethodGet, "/api/management/state?includeDeleted=true", nil)
	addSessionCookies(stateReq, operatorSessionForTest(t, server))
	stateRec := httptest.NewRecorder()
	server.ServeHTTP(stateRec, stateReq)
	var state map[string]any
	if err := json.NewDecoder(stateRec.Body).Decode(&state); err != nil {
		t.Fatalf("decode management: %v", err)
	}
	for _, item := range state["users"].([]any) {
		if item.(map[string]any)["id"] == "usr-missing" {
			t.Fatalf("missing user was created as stub: %#v", state["users"])
		}
	}
}

func TestUserSoftDeleteRevokesSessionsAndHidesByDefault(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	created := createResource(t, server, http.MethodPost, "/api/users", `{"email":"member@lab.example","accountId":"acct-alpha","role":"pi","password":"CorrectHorseBatteryStaple!"}`)
	loginRec := loginForTest(t, server, "member@lab.example", "CorrectHorseBatteryStaple!")

	deleteReq := httptest.NewRequest(http.MethodPost, "/api/users/delete", bytes.NewBufferString(`{"userId":"`+stringValue(created["id"])+`","reason":"left_lab","confirm":true}`))
	deleteReq.Header.Set("Content-Type", "application/json")
	addAuth(deleteReq, operatorSessionForTest(t, server))
	deleteRec := httptest.NewRecorder()
	server.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d: %s", deleteRec.Code, deleteRec.Body.String())
	}
	var deleted map[string]any
	if err := json.NewDecoder(deleteRec.Body).Decode(&deleted); err != nil {
		t.Fatalf("decode deleted user: %v", err)
	}
	if deleted["status"] != "deleted" || deleted["deletedAt"] == nil || deleted["deletedBy"] != "usr-admin" || deleted["deleteReason"] != "left_lab" {
		t.Fatalf("delete metadata incomplete: %#v", deleted)
	}

	assertSessionUnauthorized(t, server, loginRec)
	assertUserAbsentFromManagement(t, server, "/api/management/state", stringValue(created["id"]))
	assertDeletedUserPresent(t, server, stringValue(created["id"]))
}

func loginForTest(t *testing.T, server http.Handler, email string, password string) *httptest.ResponseRecorder {
	t.Helper()
	loginReq := loginRequest(email, password, "")
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	server.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", loginRec.Code, loginRec.Body.String())
	}
	return loginRec
}

func loginAttemptForTest(server http.Handler, email string, password string, remoteAddr string) *httptest.ResponseRecorder {
	req := loginRequest(email, password, remoteAddr)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func loginRequest(email string, password string, remoteAddr string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"email":"`+email+`","password":"`+password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	return req
}

func assertSessionUnauthorized(t *testing.T, server http.Handler, loginRec *httptest.ResponseRecorder) {
	t.Helper()
	meReq := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	for _, cookie := range loginRec.Result().Cookies() {
		meReq.AddCookie(cookie)
	}
	meRec := httptest.NewRecorder()
	server.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusUnauthorized {
		t.Fatalf("deleted user session still works: status=%d body=%s", meRec.Code, meRec.Body.String())
	}
}

func assertUserAbsentFromManagement(t *testing.T, server http.Handler, path string, userID string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	addSessionCookies(req, operatorSessionForTest(t, server))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	var defaultState map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&defaultState); err != nil {
		t.Fatalf("decode default management: %v", err)
	}
	for _, item := range defaultState["users"].([]any) {
		if item.(map[string]any)["id"] == userID {
			t.Fatalf("deleted user visible without includeDeleted: %#v", defaultState["users"])
		}
	}
}

func assertDeletedUserPresent(t *testing.T, server http.Handler, userID string) {
	t.Helper()
	includeReq := httptest.NewRequest(http.MethodGet, "/api/management/state?includeDeleted=true", nil)
	addSessionCookies(includeReq, operatorSessionForTest(t, server))
	includeRec := httptest.NewRecorder()
	server.ServeHTTP(includeRec, includeReq)
	var includeState map[string]any
	if err := json.NewDecoder(includeRec.Body).Decode(&includeState); err != nil {
		t.Fatalf("decode include deleted management: %v", err)
	}
	if !slices.ContainsFunc(includeState["users"].([]any), func(item any) bool {
		user := item.(map[string]any)
		return user["id"] == userID && user["status"] == "deleted"
	}) {
		t.Fatalf("deleted user missing from includeDeleted state: %#v", includeState["users"])
	}
}

func TestUserLifecycleProtectsLastActiveAdmin(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	for _, tc := range []struct {
		path string
	}{
		{"/api/users/disable"},
		{"/api/users/delete"},
	} {
		body := `{"userId":"usr-admin","reason":"test"}`
		if tc.path == "/api/users/delete" {
			body = `{"userId":"usr-admin","reason":"test","confirm":true}`
		}
		req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		addAuth(req, operatorSessionForTest(t, server))
		rec := httptest.NewRecorder()

		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "last_active_admin") {
			t.Fatalf("%s status=%d body=%s, want last admin guard", tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestSupportTicketMappingRequiresExternalTicket(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	req := httptest.NewRequest(http.MethodPost, "/api/support/tickets", bytes.NewBufferString(`{"accountId":"acct-alpha","title":"Need help"}`))
	req.Header.Set("Content-Type", "application/json")
	addAuth(req, operatorSessionForTest(t, server))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "missing_external_ticket_id") {
		t.Fatalf("status=%d body=%s, want missing external ticket id", rec.Code, rec.Body.String())
	}
}

func TestSupportTicketMappingPersistsExternalContext(t *testing.T) {
	path := t.TempDir() + "/control-plane-state.json"
	service := controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{})
	server, err := NewPersistentServer(service, NewFileFactStore(path))
	if err != nil {
		t.Fatalf("create persistent server: %v", err)
	}
	body := `{"accountId":"acct-alpha","userId":"usr-alpha","workspaceId":"ws-alpha","externalSystem":"zammad","externalTicketId":"ZAM-42","externalUrl":"https://support.example/tickets/42","resourceIds":["compute-alpha"],"operationId":"op-alpha","title":"Workspace failed","description":"provider timeout"}`
	created := createResource(t, server, http.MethodPost, "/api/support/tickets", body)
	if !strings.HasPrefix(stringValue(created["id"]), "support-") || created["externalTicketId"] != "ZAM-42" || created["accountId"] != "acct-alpha" {
		t.Fatalf("support mapping did not keep external context: %#v", created)
	}

	restarted, err := NewPersistentServer(service, NewFileFactStore(path))
	if err != nil {
		t.Fatalf("restart persistent server: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/support/tickets?scope=all", nil)
	addSessionCookies(req, operatorSessionForTest(t, restarted))
	rec := httptest.NewRecorder()
	restarted.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var listed map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&listed); err != nil {
		t.Fatalf("decode support mappings: %v", err)
	}
	tickets := listed["tickets"].([]any)
	if len(tickets) != 1 || tickets[0].(map[string]any)["externalTicketId"] != "ZAM-42" {
		t.Fatalf("support mapping did not persist: %#v", tickets)
	}
}

func TestActiveConsoleAPIRoutesReachControlPlane(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/auth/me", ""},
		{http.MethodGet, "/api/healthz", ""},
		{http.MethodGet, "/api/state", ""},
		{http.MethodGet, "/api/management/state", ""},
		{http.MethodGet, "/api/operator/summary", ""},
		{http.MethodGet, "/api/runtime/readiness", ""},
		{http.MethodGet, "/api/production/readiness", ""},
		{http.MethodGet, "/api/compute-pools", ""},
		{http.MethodGet, "/api/compute-allocations", ""},
		{http.MethodGet, "/api/compute-allocations/compute-alpha", ""},
		{http.MethodGet, "/api/support/tickets", ""},
		{http.MethodGet, "/api/ledger/task-receipts", ""},
		{http.MethodPost, "/api/auth/logout", `{}`},
		{http.MethodPost, "/api/organizations", `{"name":"Lab","billingAccountId":"acct-lab"}`},
		{http.MethodPost, "/api/organizations/members", `{"organizationId":"org-lab","userId":"usr-owner","role":"member"}`},
		{http.MethodPost, "/api/users", `{"email":"pi@medopl.cn","accountId":"acct-lab","password":"secret"}`},
		{http.MethodPost, "/api/users/disable", `{"userId":"usr-owner"}`},
		{http.MethodPost, "/api/users/delete", `{"userId":"usr-owner"}`},
		{http.MethodPost, "/api/billing/topups", `{"accountId":"acct-lab","amount":100,"idempotencyKey":"topup-test"}`},
		{http.MethodPost, "/api/billing/resource-settlements", `{"accountId":"acct-lab","hours":1}`},
		{http.MethodPost, "/api/billing/reconciliation", `{"report":{"id":"recon-test","generatedAt":"2026-07-06T00:00:00Z"}}`},
		{http.MethodPost, "/api/compute-allocations", `{"packageId":"basic","name":"compute"}`},
		{http.MethodPost, "/api/compute-allocations/compute-alpha/destroy", `{"confirm":true}`},
		{http.MethodPost, "/api/storage-volumes", `{"name":"data","sizeGb":10}`},
		{http.MethodPost, "/api/storage-volumes/destroy", `{"storageId":"storage-alpha"}`},
		{http.MethodPost, "/api/storage-attachments", `{"computeAllocationId":"compute-alpha","storageId":"storage-alpha","mountPath":"/data"}`},
		{http.MethodPost, "/api/storage-attachments/detach", `{"attachmentId":"attach-alpha"}`},
		{http.MethodPost, "/api/workspaces/reset-token", `{"workspaceId":"ws-alpha"}`},
		{http.MethodPost, "/api/workspaces/delete-token", `{"workspaceId":"ws-alpha"}`},
		{http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"ws-alpha"}`},
		{http.MethodPost, "/api/operator/cleanup-workspace-access", `{"reason":"test"}`},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var body *bytes.Buffer
			if tc.body != "" {
				body = bytes.NewBufferString(tc.body)
			} else {
				body = bytes.NewBuffer(nil)
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "route-contract-test")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code == http.StatusMethodNotAllowed {
				t.Fatalf("status = %d for %s %s", rec.Code, tc.method, tc.path)
			}
			if rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("content-type = %q, want application/json", rec.Header().Get("Content-Type"))
			}
			var payload any
			if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
		})
	}
}
