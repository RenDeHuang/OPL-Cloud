package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type workspaceDeleteFabric struct {
	fakeFabricClient
	mu                  sync.Mutex
	calls               []string
	failStage           string
	failures            int
	mismatchStage       string
	unknownStage        string
	storageStatus       string
	computeReads        []string
	destroyed           bool
	runtimeResponseLost bool
	observeState        string
	secretObserveState  string
	observeErr          error
	observeRuntimeID    string
	observeKeyID        int64
	events              *workspaceDeleteEvents
}

type workspaceDeleteEvents struct {
	mu    sync.Mutex
	items []string
}

func (e *workspaceDeleteEvents) add(value string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.items = append(e.items, value)
}

func (e *workspaceDeleteEvents) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.items...)
}

func (f *workspaceDeleteFabric) call(stage, id, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, stage+":"+id+":"+key)
	f.events.add("fabric:" + stage)
	if f.failStage == stage && f.failures > 0 {
		f.failures--
		return errors.New("fabric owner unavailable")
	}
	return nil
}

func (f *workspaceDeleteFabric) DestroyWorkspaceRuntime(_ context.Context, _, workspaceID, key string) (clients.WorkspaceRuntime, error) {
	if err := f.call("runtime", workspaceID, key); err != nil {
		return clients.WorkspaceRuntime{}, err
	}
	if f.mismatchStage == "runtime" {
		workspaceID = "ws-other"
	}
	status := "destroyed"
	if f.unknownStage == "runtime" {
		status = "unknown"
	}
	f.mu.Lock()
	f.destroyed = status == "destroyed"
	f.mu.Unlock()
	if f.runtimeResponseLost {
		return clients.WorkspaceRuntime{}, errors.New("runtime destroy response lost")
	}
	return clients.WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: workspaceID, Status: status}, nil
}

func (f *workspaceDeleteFabric) ObserveWorkspaceRuntime(_ context.Context, workspaceID string) (clients.WorkspaceRuntimeObservation, error) {
	if err := f.call("runtime-read", workspaceID, ""); err != nil {
		return clients.WorkspaceRuntimeObservation{}, err
	}
	f.mu.Lock()
	destroyed, state, observeErr := f.destroyed, f.observeState, f.observeErr
	f.mu.Unlock()
	if observeErr != nil {
		return clients.WorkspaceRuntimeObservation{}, observeErr
	}
	if state == "" {
		state = clients.WorkspaceOwnerObservationReady
		if destroyed {
			state = clients.WorkspaceOwnerObservationAbsent
		}
	}
	observation := clients.WorkspaceRuntimeObservation{SchemaVersion: clients.WorkspaceOwnerObservationSchemaVersion, State: state, WorkspaceID: workspaceID}
	if state == clients.WorkspaceOwnerObservationReady || state == clients.WorkspaceOwnerObservationPending {
		runtimeID := f.observeRuntimeID
		if runtimeID == "" {
			runtimeID = "runtime-alpha"
		}
		observation.Runtime = &clients.WorkspaceRuntime{ID: runtimeID, WorkspaceID: workspaceID, Status: "running", Ready: state == clients.WorkspaceOwnerObservationReady}
	}
	return observation, nil
}

func (f *workspaceDeleteFabric) ObserveWorkspaceRuntimeGatewaySecret(_ context.Context, workspaceID string) (clients.WorkspaceRuntimeGatewaySecretObservation, error) {
	if err := f.call("secret-read", workspaceID, ""); err != nil {
		return clients.WorkspaceRuntimeGatewaySecretObservation{}, err
	}
	f.mu.Lock()
	destroyed, state, observeErr := f.destroyed, f.secretObserveState, f.observeErr
	f.mu.Unlock()
	if observeErr != nil {
		return clients.WorkspaceRuntimeGatewaySecretObservation{}, observeErr
	}
	if state == "" {
		state = f.observeState
		if state == "" {
			state = clients.WorkspaceOwnerObservationReady
		}
		if destroyed && f.secretObserveState == "" {
			state = clients.WorkspaceOwnerObservationAbsent
		}
	}
	observation := clients.WorkspaceRuntimeGatewaySecretObservation{SchemaVersion: clients.WorkspaceOwnerObservationSchemaVersion, State: state, WorkspaceID: workspaceID}
	if state == clients.WorkspaceOwnerObservationReady || state == clients.WorkspaceOwnerObservationPending {
		keyID := f.observeKeyID
		if keyID == 0 {
			keyID = 19
		}
		observation.Binding = &clients.WorkspaceRuntimeGatewaySecretBinding{
			WorkspaceID: workspaceID, WorkspaceAPIKeyID: keyID, SecretRef: "opl-gateway-ws-alpha", Fingerprint: "sha256:" + strings.Repeat("a", 64), Bound: state == clients.WorkspaceOwnerObservationReady,
		}
	}
	return observation, nil
}

func (f *workspaceDeleteFabric) DetachStorageAttachment(_ context.Context, _, _, attachmentID, key string) (clients.StorageAttachment, error) {
	if err := f.call("attachment", attachmentID, key); err != nil {
		return clients.StorageAttachment{}, err
	}
	workspaceID := "ws-alpha"
	if f.mismatchStage == "attachment" {
		workspaceID = "ws-other"
	}
	status := "detached"
	if f.unknownStage == "attachment" {
		status = "unknown"
	}
	return clients.StorageAttachment{ID: attachmentID, WorkspaceID: workspaceID, ComputeID: "compute-alpha", VolumeID: "storage-alpha", Status: status}, nil
}

func (f *workspaceDeleteFabric) DestroyStorageVolume(_ context.Context, _, _, storageID, key string) (clients.StorageVolume, error) {
	if err := f.call("storage", storageID, key); err != nil {
		return clients.StorageVolume{}, err
	}
	workspaceID := "ws-alpha"
	if f.mismatchStage == "storage" {
		workspaceID = "ws-other"
	}
	status := "destroyed"
	if f.storageStatus != "" {
		status = f.storageStatus
	}
	if f.unknownStage == "storage" {
		status = "unknown"
	}
	return clients.StorageVolume{ID: storageID, WorkspaceID: workspaceID, Status: status}, nil
}

func (f *workspaceDeleteFabric) DestroyComputeAllocation(_ context.Context, _, _, computeID, key string) (clients.ComputeAllocation, error) {
	if err := f.call("compute", computeID, key); err != nil {
		return clients.ComputeAllocation{}, err
	}
	workspaceID := "ws-alpha"
	if f.mismatchStage == "compute" {
		workspaceID = "ws-other"
	}
	status := "destroying"
	if f.unknownStage == "compute" {
		status = "unknown"
	}
	return clients.ComputeAllocation{ID: computeID, WorkspaceID: workspaceID, Status: status}, nil
}

func (f *workspaceDeleteFabric) ReadComputeAllocation(_ context.Context, computeID string) (clients.ComputeAllocation, error) {
	if err := f.call("compute-read", computeID, ""); err != nil {
		return clients.ComputeAllocation{}, err
	}
	f.mu.Lock()
	status := "destroyed"
	if len(f.computeReads) > 0 {
		status = f.computeReads[0]
		f.computeReads = f.computeReads[1:]
	}
	if f.unknownStage == "compute-read" {
		status = "unknown"
	}
	f.mu.Unlock()
	workspaceID := "ws-alpha"
	if f.mismatchStage == "compute-read" {
		workspaceID = "ws-other"
	}
	return clients.ComputeAllocation{ID: computeID, WorkspaceID: workspaceID, Status: status}, nil
}

func (f *workspaceDeleteFabric) recordedCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type workspaceDeleteFixture struct {
	server    http.Handler
	store     controlPlaneTableStore
	session   *httptest.ResponseRecorder
	fabric    *workspaceDeleteFabric
	workspace map[string]any
}

type workspaceDeleteSub2API struct {
	*testSub2APIClient
	mu              sync.Mutex
	userID          int64
	keyID           int64
	keyExists       bool
	keyReads        int
	keyDeletes      int
	history         map[string]clients.Sub2APIBalanceHistoryEntry
	historyReads    [][]string
	refunds         []clients.Sub2APIRefundInput
	keyReadErr      error
	keyDeleteErr    error
	keyResponseLost bool
	keyName         string
	keyUserID       int64
	historyReadErr  error
	refundErr       error
	responseLost    bool
	events          *workspaceDeleteEvents
}

func (s *workspaceDeleteSub2API) UserKey(_ context.Context, _ clients.SessionDelegatedCredential, userID, keyID int64) (clients.Sub2APIWorkspaceKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keyReads++
	s.events.add("sub2api:key-get")
	if s.keyReadErr != nil {
		return clients.Sub2APIWorkspaceKey{}, s.keyReadErr
	}
	if userID != s.userID || keyID != s.keyID || !s.keyExists {
		return clients.Sub2APIWorkspaceKey{}, clients.ErrSub2APIKeyNotFound
	}
	returnedUserID := userID
	if s.keyUserID != 0 {
		returnedUserID = s.keyUserID
	}
	name := s.keyName
	if name == "" {
		name = workspaceReservedKeyName("ws-alpha")
	}
	return clients.Sub2APIWorkspaceKey{ID: keyID, UserID: returnedUserID, Name: name, Status: "active"}, nil
}

func (s *workspaceDeleteSub2API) DeleteUserKey(_ context.Context, _ clients.SessionDelegatedCredential, userID, keyID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keyDeletes++
	s.events.add("sub2api:key-delete")
	if s.keyDeleteErr != nil {
		return s.keyDeleteErr
	}
	if userID != s.userID || keyID != s.keyID || !s.keyExists {
		return clients.ErrSub2APIKeyNotFound
	}
	s.keyExists = false
	if s.keyResponseLost {
		return errors.New("key delete response lost")
	}
	return nil
}

func (s *workspaceDeleteSub2API) CreateUserKey(context.Context, clients.SessionDelegatedCredential, int64, clients.Sub2APICreateKeyInput, string) (clients.Sub2APIWorkspaceKey, error) {
	return clients.Sub2APIWorkspaceKey{}, errors.New("unexpected key create")
}

func (s *workspaceDeleteSub2API) UpdateUserKey(context.Context, clients.SessionDelegatedCredential, int64, int64, clients.Sub2APIUpdateKeyInput) (clients.Sub2APIWorkspaceKey, error) {
	return clients.Sub2APIWorkspaceKey{}, errors.New("unexpected key update")
}

func (s *workspaceDeleteSub2API) FinancialBalanceHistoryByCodes(_ context.Context, userID int64, codes []string) (map[string]clients.Sub2APIBalanceHistoryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historyReads = append(s.historyReads, append([]string(nil), codes...))
	if len(codes) == 1 && codes[0] == "opl:workspace-purchase-alpha" {
		s.events.add("sub2api:debit-get")
	} else {
		s.events.add("sub2api:refund-get")
	}
	if s.historyReadErr != nil {
		return nil, s.historyReadErr
	}
	result := make(map[string]clients.Sub2APIBalanceHistoryEntry, len(codes))
	for _, code := range codes {
		if entry, ok := s.history[code]; ok {
			result[code] = entry
		}
	}
	return result, nil
}

func (s *workspaceDeleteSub2API) Refund(_ context.Context, input clients.Sub2APIRefundInput) (clients.Sub2APIRefund, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refunds = append(s.refunds, input)
	s.events.add("sub2api:refund-post")
	if s.refundErr != nil {
		return clients.Sub2APIRefund{}, s.refundErr
	}
	usedBy := input.UserID
	now := time.Now().UTC()
	s.history[input.Code] = clients.Sub2APIBalanceHistoryEntry{
		Code: input.Code, Type: "balance", ValueUSDMicros: input.RefundUSDMicros, Status: "used", UsedBy: &usedBy, UsedAt: &now,
	}
	if s.responseLost {
		return clients.Sub2APIRefund{}, errors.New("refund response lost")
	}
	return clients.Sub2APIRefund{Code: input.Code, UserID: input.UserID, RefundUSDMicros: input.RefundUSDMicros, Status: "used"}, nil
}

func assertWorkspaceDeleteExactHistoryReads(t *testing.T, sub2API *workspaceDeleteSub2API) {
	t.Helper()
	sub2API.mu.Lock()
	reads := make([][]string, len(sub2API.historyReads))
	for index := range sub2API.historyReads {
		reads[index] = append([]string(nil), sub2API.historyReads[index]...)
	}
	sub2API.mu.Unlock()
	refundCode := monthlyRefundCode(monthlyEnvironment(), workspaceDeleteOperationID("ws-alpha"))
	if len(reads) < 2 || len(reads[0]) != 1 || reads[0][0] != "opl:workspace-purchase-alpha" {
		t.Fatalf("debit history reads=%#v", reads)
	}
	for _, codes := range reads[1:] {
		if len(codes) != 1 || codes[0] != refundCode {
			t.Fatalf("non-exact refund history reads=%#v", reads)
		}
	}
}

type workspaceDeleteLedger struct {
	fakeLedgerClient
	mu       sync.Mutex
	purchase clients.Receipt
	reads    int
	receipts []clients.ReceiptInput
	keys     []string
	failures int
	events   *workspaceDeleteEvents
}

func (l *workspaceDeleteLedger) Receipt(_ context.Context, receiptID string) (clients.Receipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reads++
	l.events.add("ledger:purchase-get")
	if receiptID != l.purchase.ReceiptID {
		return clients.Receipt{}, errors.New("receipt not found")
	}
	return l.purchase, nil
}

func (l *workspaceDeleteLedger) ReceiptForAccount(_ context.Context, accountID, workspaceID, receiptID string) (clients.Receipt, error) {
	receipt, err := l.Receipt(context.Background(), receiptID)
	if err != nil || receipt.AccountID != accountID || receipt.WorkspaceID != workspaceID {
		return clients.Receipt{}, errors.New("receipt scope mismatch")
	}
	return receipt, nil
}

func (l *workspaceDeleteLedger) RecordReceipt(_ context.Context, input clients.ReceiptInput, key string) (clients.Receipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.receipts = append(l.receipts, input)
	l.keys = append(l.keys, key)
	l.events.add("ledger:refund-receipt")
	if l.failures > 0 {
		l.failures--
		return clients.Receipt{}, errors.New("ledger receipt unavailable")
	}
	return clients.Receipt{ReceiptInput: input, ReceiptID: "receipt-refund-alpha"}, nil
}

type workspaceDeleteNoopDeleteStore struct {
	controlPlaneTableStore
}

type workspaceDeleteEventStore struct {
	controlPlaneTableStore
	events *workspaceDeleteEvents
}

func (s workspaceDeleteEventStore) ApplyWorkspaceDelete(ctx context.Context, mutation workspaceDeleteStoreMutation) error {
	err := s.controlPlaneTableStore.ApplyWorkspaceDelete(ctx, mutation)
	if err == nil && mutation.DeleteWorkspace {
		s.events.add("control-plane:workspace-absent")
	}
	return err
}

func (s workspaceDeleteNoopDeleteStore) ApplyWorkspaceDelete(ctx context.Context, mutation workspaceDeleteStoreMutation) error {
	mutation.DeleteWorkspace = false
	return s.controlPlaneTableStore.ApplyWorkspaceDelete(ctx, mutation)
}

func newWorkspaceDeleteFixture(t *testing.T, store controlPlaneTableStore, fabric *workspaceDeleteFabric) workspaceDeleteFixture {
	t.Helper()
	fixture, _, _ := newWorkspaceDeleteCompletionFixtureWith(t, store, fabric)
	return fixture
}

func newWorkspaceDeleteFixtureWithService(t *testing.T, store controlPlaneTableStore, fabric *workspaceDeleteFabric, service *controlplane.Service) workspaceDeleteFixture {
	t.Helper()
	server, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatal(err)
	}
	session := tenantOwnerSessionForTest(t, server)
	handler := server.(*controlPlaneHTTPHandler)
	users, err := handler.app.tables.ListUsers(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	var owner map[string]any
	for _, user := range users {
		if stringValue(user["accountId"]) == "acct-alpha" {
			owner = user
			break
		}
	}
	if owner == nil {
		t.Fatal("tenant owner missing")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	workspace := map[string]any{
		"id": "ws-alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha", "ownerUserId": owner["id"],
		"state": "running", "status": "running", "currentComputeAllocationId": "compute-alpha", "storageId": "storage-alpha",
		"currentAttachmentId": "attachment-alpha", "runtimeId": "runtime-alpha", "createdAt": now, "updatedAt": now,
	}
	if err := handler.app.tables.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	return workspaceDeleteFixture{server: server, store: handler.app.tables, session: session, fabric: fabric, workspace: workspace}
}

func newWorkspaceDeleteCompletionFixture(t *testing.T) (workspaceDeleteFixture, *workspaceDeleteSub2API, *workspaceDeleteLedger) {
	t.Helper()
	return newWorkspaceDeleteCompletionFixtureWith(t, newMemoryTableStore(), &workspaceDeleteFabric{})
}

func newWorkspaceDeleteEventFixture(t *testing.T) (workspaceDeleteFixture, *workspaceDeleteSub2API, *workspaceDeleteLedger, *workspaceDeleteEvents) {
	t.Helper()
	events := &workspaceDeleteEvents{}
	fabric := &workspaceDeleteFabric{events: events}
	store := workspaceDeleteEventStore{controlPlaneTableStore: newMemoryTableStore(), events: events}
	fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixtureWith(t, store, fabric)
	sub2API.events, ledger.events = events, events
	return fixture, sub2API, ledger, events
}

func newWorkspaceDeleteCompletionFixtureWith(t *testing.T, store controlPlaneTableStore, fabric *workspaceDeleteFabric) (workspaceDeleteFixture, *workspaceDeleteSub2API, *workspaceDeleteLedger) {
	t.Helper()
	sub2API := &workspaceDeleteSub2API{
		testSub2APIClient: &testSub2APIClient{balance: 1_000_000_000_000, charges: map[string]int64{}},
		userID:            41, keyID: 19, keyExists: true, history: map[string]clients.Sub2APIBalanceHistoryEntry{},
	}
	ledger := &workspaceDeleteLedger{}
	service := controlplane.NewService(ledger, fabric, sub2API)
	fixture := newWorkspaceDeleteFixtureWithService(t, store, fabric, service)
	command := workspaceLaunchUnitCommand()
	command.OperationID, command.AccountID, command.OwnerUserID, command.Sub2APIUserID = "workspace-launch-alpha", "acct-alpha", stringValue(fixture.workspace["ownerUserId"]), 41
	command.WorkspaceID, command.Name = "ws-alpha", "Alpha"
	operation, err := newWorkspaceLaunchReconcileOperation(command)
	if err != nil {
		t.Fatal(err)
	}
	readyFacts := map[string]any{
		"sub2apiRedeemCode": "opl:workspace-purchase-alpha",
		"workspaceApiKeyId": int64(19), "workspaceKeyStatus": workspaceKeyCodexGroupBound, "workspaceKeyFingerprint": "sha256:" + strings.Repeat("a", 64),
		"chargeAttempted": true, "chargeConfirmation": map[string]any{"code": "opl:workspace-purchase-alpha", "userId": int64(41), "chargeUsdMicros": int64(52_580_000), "status": "used"},
		"postChargeBalanceUsdMicros": int64(947_420_000), "postChargeBalanceKnown": true, "billingPeriodState": "frozen",
		"periodStart": "2026-08-15T00:00:00Z", "paidThrough": "2026-09-15T00:00:00Z", "billingAnchorDay": 15,
		"computeAllocationId": "compute-alpha", "computeBindingRef": "workspace-launch-alpha:compute", "storageId": "storage-alpha", "storageBindingRef": "workspace-launch-alpha:storage",
		"attachmentId": "attachment-alpha", "attachmentBindingRef": "workspace-launch-alpha:attachment",
		"gatewaySecretRef": "opl-gateway-ws-alpha", "gatewaySecretVersion": "v1", "secretBindingRef": "workspace-launch-alpha:secret",
		"runtimeId": "runtime-alpha", "runtimeReady": true, "runtimeServiceName": "runtime-alpha", "runtimeBindingRef": "workspace-launch-alpha:runtime",
		"url": "https://workspace.example/alpha", "runtimeUsername": "opl", "credentialStatus": "configured", "credentialVersion": "v1", "credentialSecretRef": "runtime-secret-alpha",
		"activationOperationId": "workspace-launch-alpha:activation", "workspaceActivatedAt": "2026-08-15T00:01:00Z",
		"receiptId": "receipt-purchase-alpha", "receiptOperationId": "workspace-launch-alpha:purchase-receipt",
	}
	for key, value := range readyFacts {
		operation.raw[key], _ = json.Marshal(value)
	}
	operation.Stage, operation.Status = "succeeded", "succeeded"
	launchRow, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.SaveRuntimeOperation(context.Background(), launchRow); err != nil {
		t.Fatal(err)
	}
	workspace, err := workspaceLaunchActivationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	workspace["purchaseReceiptId"] = "receipt-purchase-alpha"
	workspace["sub2apiUserId"], workspace["sub2apiRedeemCode"] = int64(41), "opl:workspace-purchase-alpha"
	purchaseInput, err := workspaceLaunchPurchaseReceiptInput(operation)
	if err != nil {
		t.Fatal(err)
	}
	ledger.purchase = clients.Receipt{ReceiptInput: purchaseInput, ReceiptID: "receipt-purchase-alpha"}
	usedBy := int64(41)
	usedAt := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	sub2API.history["opl:workspace-purchase-alpha"] = clients.Sub2APIBalanceHistoryEntry{
		Code: "opl:workspace-purchase-alpha", Type: "balance", ValueUSDMicros: -52_580_000, Status: "used", UsedBy: &usedBy, UsedAt: &usedAt,
	}
	if err := fixture.store.DeleteWorkspace(context.Background(), "ws-alpha"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	fixture.workspace = workspace
	return fixture, sub2API, ledger
}

func TestWorkspaceDeleteCompletesExactOwnerChain(t *testing.T) {
	fixture, sub2API, ledger, events := newWorkspaceDeleteEventFixture(t)
	response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-complete-owner-chain")
	if response.Code != http.StatusOK {
		row, _, _ := fixture.store.GetRuntimeOperation(context.Background(), workspaceDeleteOperationID("ws-alpha"))
		t.Fatalf("delete status=%d body=%s operation=%#v", response.Code, response.Body.String(), row)
	}
	if sub2API.keyExists || sub2API.keyDeletes != 1 || len(sub2API.refunds) != 1 {
		t.Fatalf("Sub2API completion keyExists=%v keyReads=%d keyDeletes=%d refunds=%#v", sub2API.keyExists, sub2API.keyReads, sub2API.keyDeletes, sub2API.refunds)
	}
	refund := sub2API.refunds[0]
	if refund.UserID != 41 || refund.RefundUSDMicros != 52_580_000 || refund.Code == "" || refund.Code == "opl:workspace-purchase-alpha" {
		t.Fatalf("refund identity=%#v", refund)
	}
	if len(ledger.receipts) != 1 || len(ledger.keys) != 1 {
		t.Fatalf("Ledger writes receipts=%#v keys=%#v", ledger.receipts, ledger.keys)
	}
	receipt := ledger.receipts[0]
	if receipt.Type != "billing.workspace_refunded.v1" || receipt.AccountID != "acct-alpha" || receipt.WorkspaceID != "ws-alpha" ||
		receipt.RequestID != workspaceDeleteOperationID("ws-alpha") || stringValue(receipt.Cost["sub2apiRedeemCode"]) != "opl:workspace-purchase-alpha" ||
		stringValue(receipt.Cost["sub2apiRefundCode"]) != refund.Code || int64(numberField(receipt.Cost, "refundUsdMicros", 0)) != 52_580_000 ||
		receipt.SupersedesReceiptID != "receipt-purchase-alpha" || stringValue(receipt.Execution["runtimeId"]) != "runtime-alpha" ||
		stringValue(receipt.Execution["computeAllocationId"]) != "compute-alpha" || stringValue(receipt.Execution["storageId"]) != "storage-alpha" ||
		stringValue(receipt.Execution["attachmentId"]) != "attachment-alpha" || int64(numberField(receipt.Execution, "workspaceApiKeyId", 0)) != 19 ||
		stringValue(receipt.Execution["debitCode"]) != "opl:workspace-purchase-alpha" || stringValue(receipt.Execution["purchaseReceiptId"]) != "receipt-purchase-alpha" ||
		stringValue(receipt.Owner["accountId"]) != "acct-alpha" || stringValue(receipt.Owner["workspaceId"]) != "ws-alpha" {
		t.Fatalf("refund receipt=%#v", receipt)
	}
	row, found, err := fixture.store.GetRuntimeOperation(context.Background(), workspaceDeleteOperationID("ws-alpha"))
	if err != nil || !found {
		t.Fatalf("delete operation found=%v err=%v", found, err)
	}
	var result map[string]any
	if json.Unmarshal([]byte(stringValue(row["result"])), &result) != nil || result["accountId"] != "acct-alpha" ||
		result["operationId"] != workspaceDeleteOperationID("ws-alpha") || result["workspaceId"] != "ws-alpha" || result["runtimeId"] != "runtime-alpha" ||
		int64(numberField(result, "workspaceApiKeyId", 0)) != 19 || result["debitCode"] != "opl:workspace-purchase-alpha" ||
		result["purchaseReceiptId"] != "receipt-purchase-alpha" || result["refundReceiptId"] != "receipt-refund-alpha" {
		t.Fatalf("durable identity result=%#v", result)
	}
	wantEvents := []string{
		"ledger:purchase-get", "sub2api:debit-get",
		"fabric:runtime-read", "fabric:secret-read", "fabric:runtime", "fabric:runtime-read", "fabric:secret-read",
		"fabric:attachment", "fabric:storage", "fabric:compute", "fabric:compute-read",
		"sub2api:key-get", "sub2api:key-delete", "sub2api:key-get",
		"fabric:runtime-read", "fabric:secret-read", "sub2api:key-get",
		"sub2api:refund-get", "sub2api:refund-get", "sub2api:refund-post", "sub2api:refund-get",
		"ledger:refund-receipt", "control-plane:workspace-absent",
	}
	if got := events.snapshot(); strings.Join(got, "\n") != strings.Join(wantEvents, "\n") {
		t.Fatalf("owner completion events=%#v want=%#v", got, wantEvents)
	}
}

func TestWorkspaceDeleteClaimAuthoritiesFailClosedBeforeFabric(t *testing.T) {
	t.Run("purchase receipt conflict", func(t *testing.T) {
		fixture, sub2API, ledger, events := newWorkspaceDeleteEventFixture(t)
		ledger.purchase.Cost = cloneMap(ledger.purchase.Cost)
		ledger.purchase.Cost["sub2apiRedeemCode"] = "opl:debit-other"
		response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-purchase-conflict")
		if response.Code != http.StatusBadGateway || strings.Join(events.snapshot(), "\n") != "ledger:purchase-get" ||
			sub2API.keyDeletes != 0 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 0 || len(fixture.fabric.recordedCalls()) != 0 {
			t.Fatalf("purchase conflict status=%d events=%#v calls=%#v keyDeletes=%d refunds=%d receipts=%d", response.Code, events.snapshot(), fixture.fabric.recordedCalls(), sub2API.keyDeletes, len(sub2API.refunds), len(ledger.receipts))
		}
	})

	t.Run("debit conflict", func(t *testing.T) {
		fixture, sub2API, ledger, events := newWorkspaceDeleteEventFixture(t)
		entry := sub2API.history["opl:workspace-purchase-alpha"]
		entry.ValueUSDMicros++
		sub2API.history[entry.Code] = entry
		response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-debit-conflict")
		want := []string{"ledger:purchase-get", "sub2api:debit-get"}
		if response.Code != http.StatusBadGateway || strings.Join(events.snapshot(), "\n") != strings.Join(want, "\n") ||
			sub2API.keyDeletes != 0 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 0 || len(fixture.fabric.recordedCalls()) != 0 {
			t.Fatalf("debit conflict status=%d events=%#v calls=%#v keyDeletes=%d refunds=%d receipts=%d", response.Code, events.snapshot(), fixture.fabric.recordedCalls(), sub2API.keyDeletes, len(sub2API.refunds), len(ledger.receipts))
		}
	})
}

func TestWorkspaceDeleteFabricObservationStatesFailClosed(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*workspaceDeleteFabric)
	}{
		{name: "pending", configure: func(f *workspaceDeleteFabric) { f.observeState = clients.WorkspaceOwnerObservationPending }},
		{name: "conflict", configure: func(f *workspaceDeleteFabric) { f.observeState = clients.WorkspaceOwnerObservationConflict }},
		{name: "owner error", configure: func(f *workspaceDeleteFabric) { f.observeState = clients.WorkspaceOwnerObservationError }},
		{name: "secret pending", configure: func(f *workspaceDeleteFabric) { f.secretObserveState = clients.WorkspaceOwnerObservationPending }},
		{name: "secret conflict", configure: func(f *workspaceDeleteFabric) { f.secretObserveState = clients.WorkspaceOwnerObservationConflict }},
		{name: "secret error", configure: func(f *workspaceDeleteFabric) { f.secretObserveState = clients.WorkspaceOwnerObservationError }},
		{name: "transport error", configure: func(f *workspaceDeleteFabric) { f.observeErr = errors.New("Fabric observation unavailable") }},
		{name: "runtime identity", configure: func(f *workspaceDeleteFabric) { f.observeRuntimeID = "runtime-other" }},
		{name: "secret identity", configure: func(f *workspaceDeleteFabric) { f.observeKeyID = 20 }},
		{name: "split absence", configure: func(f *workspaceDeleteFabric) {
			f.observeState, f.secretObserveState = clients.WorkspaceOwnerObservationAbsent, clients.WorkspaceOwnerObservationReady
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture, sub2API, ledger, events := newWorkspaceDeleteEventFixture(t)
			tc.configure(fixture.fabric)
			response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-observation-"+strings.ReplaceAll(tc.name, " ", "-"))
			for _, event := range events.snapshot() {
				if event == "fabric:runtime" || event == "fabric:attachment" || event == "fabric:storage" || event == "fabric:compute" || event == "sub2api:key-delete" || event == "sub2api:refund-post" || event == "ledger:refund-receipt" {
					t.Fatalf("state %s performed mutation event=%s all=%#v", tc.name, event, events.snapshot())
				}
			}
			if response.Code != http.StatusBadGateway || sub2API.keyDeletes != 0 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 0 {
				t.Fatalf("state %s status=%d body=%s keyDeletes=%d refunds=%d receipts=%d", tc.name, response.Code, response.Body.String(), sub2API.keyDeletes, len(sub2API.refunds), len(ledger.receipts))
			}
		})
	}
}

func TestWorkspaceDeleteKeyAndRefundOwnerConflictsStopSubsequentMutation(t *testing.T) {
	t.Run("key identity conflict", func(t *testing.T) {
		fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
		sub2API.keyName = "customer-key"
		response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-key-conflict")
		if response.Code != http.StatusBadGateway || sub2API.keyDeletes != 0 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 0 {
			t.Fatalf("status=%d keyDeletes=%d refunds=%d receipts=%d", response.Code, sub2API.keyDeletes, len(sub2API.refunds), len(ledger.receipts))
		}
	})

	t.Run("key read error", func(t *testing.T) {
		fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
		sub2API.keyReadErr = errors.New("Key owner unavailable")
		response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-key-error")
		if response.Code != http.StatusBadGateway || sub2API.keyDeletes != 0 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 0 {
			t.Fatalf("status=%d keyDeletes=%d refunds=%d receipts=%d", response.Code, sub2API.keyDeletes, len(sub2API.refunds), len(ledger.receipts))
		}
	})

	t.Run("refund history conflict", func(t *testing.T) {
		fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
		code := monthlyRefundCode(monthlyEnvironment(), workspaceDeleteOperationID("ws-alpha"))
		usedBy := int64(42)
		usedAt := time.Now().UTC()
		sub2API.history[code] = clients.Sub2APIBalanceHistoryEntry{Code: code, Type: "balance", ValueUSDMicros: 52_580_000, Status: "used", UsedBy: &usedBy, UsedAt: &usedAt}
		response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-refund-conflict")
		if response.Code != http.StatusBadGateway || len(sub2API.refunds) != 0 || len(ledger.receipts) != 0 {
			t.Fatalf("status=%d refunds=%d receipts=%d", response.Code, len(sub2API.refunds), len(ledger.receipts))
		}
	})
}

func TestWorkspaceDeleteResponseLossAndReceiptOnlyRecovery(t *testing.T) {
	t.Run("runtime response loss", func(t *testing.T) {
		fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
		fixture.fabric.runtimeResponseLost = true
		response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-runtime-response-loss")
		if response.Code != http.StatusOK || sub2API.keyDeletes != 1 || len(sub2API.refunds) != 1 || len(ledger.receipts) != 1 {
			t.Fatalf("status=%d body=%s keyDeletes=%d refunds=%d receipts=%d", response.Code, response.Body.String(), sub2API.keyDeletes, len(sub2API.refunds), len(ledger.receipts))
		}
	})

	t.Run("key response loss", func(t *testing.T) {
		fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
		sub2API.keyResponseLost = true
		first := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-key-response-loss")
		if first.Code != http.StatusOK || sub2API.keyDeletes != 1 || sub2API.keyExists == true || len(sub2API.refunds) != 1 || len(ledger.receipts) != 1 {
			t.Fatalf("first status=%d keyExists=%v deletes=%d refunds=%d", first.Code, sub2API.keyExists, sub2API.keyDeletes, len(sub2API.refunds))
		}
		assertWorkspaceDeleteExactHistoryReads(t, sub2API)
		second := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-key-response-loss-replay")
		if second.Code != http.StatusOK || sub2API.keyDeletes != 1 || len(sub2API.refunds) != 1 || len(ledger.receipts) != 1 {
			t.Fatalf("replay status=%d body=%s deletes=%d refunds=%d receipts=%d", second.Code, second.Body.String(), sub2API.keyDeletes, len(sub2API.refunds), len(ledger.receipts))
		}
	})

	t.Run("refund response loss", func(t *testing.T) {
		fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
		sub2API.responseLost = true
		response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-refund-response-loss")
		if response.Code != http.StatusOK || len(sub2API.refunds) != 1 || len(ledger.receipts) != 1 {
			t.Fatalf("status=%d body=%s refunds=%d receipts=%d historyReads=%#v", response.Code, response.Body.String(), len(sub2API.refunds), len(ledger.receipts), sub2API.historyReads)
		}
		assertWorkspaceDeleteExactHistoryReads(t, sub2API)
		replayed := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-refund-response-loss-replay")
		if replayed.Code != http.StatusOK || len(sub2API.refunds) != 1 || len(ledger.receipts) != 1 {
			t.Fatalf("replay status=%d refunds=%d receipts=%d", replayed.Code, len(sub2API.refunds), len(ledger.receipts))
		}
	})

	t.Run("receipt failure", func(t *testing.T) {
		fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
		ledger.failures = 1
		first := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-receipt-failure")
		fabricCalls := len(fixture.fabric.recordedCalls())
		if first.Code != http.StatusBadGateway || len(sub2API.refunds) != 1 || len(ledger.receipts) != 1 {
			t.Fatalf("first status=%d refunds=%d receipts=%d", first.Code, len(sub2API.refunds), len(ledger.receipts))
		}
		second := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-receipt-failure-replay")
		if second.Code != http.StatusOK || len(sub2API.refunds) != 1 || sub2API.keyDeletes != 1 || len(ledger.receipts) != 2 || len(fixture.fabric.recordedCalls()) != fabricCalls {
			t.Fatalf("replay status=%d refunds=%d keyDeletes=%d receipts=%d Fabric calls=%d/%d", second.Code, len(sub2API.refunds), sub2API.keyDeletes, len(ledger.receipts), len(fixture.fabric.recordedCalls()), fabricCalls)
		}
	})
}

func TestWorkspaceDeleteRefundErrorIsGETOnlyOnReplay(t *testing.T) {
	fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
	sub2API.refundErr = errors.New("refund transport unavailable")
	first := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-refund-error")
	second := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-refund-error-replay")
	if first.Code != http.StatusBadGateway || second.Code != http.StatusBadGateway || len(sub2API.refunds) != 1 || len(ledger.receipts) != 0 {
		t.Fatalf("statuses=%d/%d refunds=%d receipts=%d", first.Code, second.Code, len(sub2API.refunds), len(ledger.receipts))
	}
	assertWorkspaceDeleteExactHistoryReads(t, sub2API)
}

func TestWorkspaceDeleteConcurrentReplayUsesOneMutationChain(t *testing.T) {
	fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
	cookies := fixture.session.Result().Cookies()
	csrf := fixture.session.Header().Get("x-opl-csrf-token")
	responses := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodDelete, "/api/workspaces/ws-alpha", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "delete-concurrent-"+strconv.Itoa(index))
			req.Header.Set("x-opl-csrf", csrf)
			for _, cookie := range cookies {
				req.AddCookie(cookie)
			}
			recorder := httptest.NewRecorder()
			fixture.server.ServeHTTP(recorder, req)
			responses <- recorder
		}(index)
	}
	wg.Wait()
	close(responses)
	for response := range responses {
		if response.Code != http.StatusOK {
			t.Fatalf("concurrent status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if sub2API.keyDeletes != 1 || len(sub2API.refunds) != 1 || len(ledger.receipts) != 1 {
		t.Fatalf("concurrent keyDeletes=%d refunds=%d receipts=%d", sub2API.keyDeletes, len(sub2API.refunds), len(ledger.receipts))
	}
}

func TestWorkspaceDeletePersistedIdentityDriftFailsClosed(t *testing.T) {
	fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
	ledger.failures = 1
	first := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-identity-drift")
	if first.Code != http.StatusBadGateway {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	row, found, err := fixture.store.GetRuntimeOperation(context.Background(), workspaceDeleteOperationID("ws-alpha"))
	if err != nil || !found {
		t.Fatalf("operation found=%v err=%v", found, err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(stringValue(row["result"])), &result); err != nil {
		t.Fatal(err)
	}
	result["runtimeId"] = "runtime-drifted"
	encoded, _ := json.Marshal(result)
	row["result"] = string(encoded)
	if err := fixture.store.SaveRuntimeOperation(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	fabricCalls, keyDeletes, refunds, receipts := len(fixture.fabric.recordedCalls()), sub2API.keyDeletes, len(sub2API.refunds), len(ledger.receipts)
	second := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-identity-drift-replay")
	if second.Code != http.StatusInternalServerError || len(fixture.fabric.recordedCalls()) != fabricCalls || sub2API.keyDeletes != keyDeletes || len(sub2API.refunds) != refunds || len(ledger.receipts) != receipts {
		t.Fatalf("drift status=%d body=%s Fabric=%d/%d key=%d/%d refunds=%d/%d receipts=%d/%d", second.Code, second.Body.String(), len(fixture.fabric.recordedCalls()), fabricCalls, sub2API.keyDeletes, keyDeletes, len(sub2API.refunds), refunds, len(ledger.receipts), receipts)
	}
}

func TestWorkspaceDeleteOwnerCommandIsOrderedDurableAndIdempotent(t *testing.T) {
	fabric := &workspaceDeleteFabric{}
	fixture := newWorkspaceDeleteFixture(t, newMemoryTableStore(), fabric)
	key := "workspace-delete:ws-alpha:once"

	response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, key)
	if response.Code != http.StatusOK {
		row, _, _ := fixture.store.GetRuntimeOperation(context.Background(), workspaceDeleteOperationID("ws-alpha"))
		t.Fatalf("delete status=%d body=%s calls=%#v operation=%#v", response.Code, response.Body.String(), fabric.recordedCalls(), row)
	}
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	operationID := workspaceDeleteOperationID("ws-alpha")
	if payload["workspaceId"] != "ws-alpha" || payload["operationId"] != operationID || payload["status"] != "deleted" {
		t.Fatalf("delete payload=%#v", payload)
	}
	wantCalls := []string{
		"runtime-read:ws-alpha:",
		"secret-read:ws-alpha:",
		"runtime:ws-alpha:" + operationID + ":runtime",
		"runtime-read:ws-alpha:",
		"secret-read:ws-alpha:",
		"attachment:attachment-alpha:" + operationID + ":attachment",
		"storage:storage-alpha:" + operationID + ":storage",
		"compute:compute-alpha:" + operationID + ":compute",
		"compute-read:compute-alpha:",
		"runtime-read:ws-alpha:",
		"secret-read:ws-alpha:",
	}
	if strings.Join(fabric.recordedCalls(), "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("Fabric calls=%#v want=%#v", fabric.recordedCalls(), wantCalls)
	}

	list := requestWithSession(t, fixture.server, fixture.session, http.MethodGet, "/api/workspaces?page=1&pageSize=20", "")
	if list.Code != http.StatusOK {
		t.Fatalf("Workspace readback status=%d body=%s", list.Code, list.Body.String())
	}
	var envelope map[string]any
	if err := json.NewDecoder(list.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	data := mapField(envelope, "data")
	items, _ := data["items"].([]any)
	if envelope["status"] != "empty" || len(items) != 0 || data["total"] != float64(0) {
		t.Fatalf("Workspace readback=%#v", envelope)
	}
	operation, found, err := fixture.store.GetRuntimeOperation(context.Background(), operationID)
	if err != nil || !found || stringValue(operation["status"]) != "succeeded" {
		t.Fatalf("delete operation=%#v found=%v err=%v", operation, found, err)
	}
	decoded, err := decodeWorkspaceDeleteOperation(operation)
	if err != nil || decoded.Phase != "complete" || decoded.RuntimeStatus != "absent" || decoded.SecretStatus != "absent" || decoded.KeyStatus != "absent" ||
		decoded.AttachmentStatus != "detached" || decoded.StorageStatus != "destroyed" || decoded.ComputeStatus != "destroyed" ||
		decoded.RefundReceiptID == "" || !workspaceDeleteRefundConfirmationMatches(decoded) {
		t.Fatalf("decoded operation=%#v err=%v", decoded, err)
	}

	replayed := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, key+":new-session")
	if replayed.Code != http.StatusOK || len(fabric.recordedCalls()) != len(wantCalls) {
		t.Fatalf("replay status=%d body=%s calls=%#v", replayed.Code, replayed.Body.String(), fabric.recordedCalls())
	}
}

func TestWorkspaceDeletePartialFabricResultResumesWithNewTransportKey(t *testing.T) {
	fabric := &workspaceDeleteFabric{failStage: "storage", failures: 1}
	fixture := newWorkspaceDeleteFixture(t, newMemoryTableStore(), fabric)
	key := "workspace-delete:ws-alpha:resume"

	first := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, key)
	if first.Code != http.StatusBadGateway || !strings.Contains(first.Body.String(), `"status":"manual_review"`) {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	if _, found, err := fixture.store.GetWorkspace(context.Background(), "ws-alpha"); err != nil || !found {
		t.Fatalf("partial cleanup removed Workspace found=%v err=%v", found, err)
	}
	row, found, err := fixture.store.GetRuntimeOperation(context.Background(), workspaceDeleteOperationID("ws-alpha"))
	operation, decodeErr := decodeWorkspaceDeleteOperation(row)
	if err != nil || !found || decodeErr != nil || operation.Status != "manual_review" || operation.Phase != "attachment_detached" {
		t.Fatalf("partial operation=%#v found=%v err=%v decode=%v", operation, found, err, decodeErr)
	}
	second := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, key+":new-session")
	if second.Code != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", second.Code, second.Body.String())
	}
	var payload map[string]any
	if err := json.NewDecoder(second.Body).Decode(&payload); err != nil || payload["operationId"] != operation.OperationID || payload["status"] != "deleted" {
		t.Fatalf("resumed operation payload=%#v err=%v", payload, err)
	}
	calls := fabric.recordedCalls()
	if len(calls) != 12 || calls[6] != calls[7] || !strings.HasPrefix(calls[6], "storage:") ||
		!strings.HasPrefix(calls[8], "compute:") || !strings.HasPrefix(calls[9], "compute-read:") ||
		!strings.HasPrefix(calls[10], "runtime-read:") || !strings.HasPrefix(calls[11], "secret-read:") {
		t.Fatalf("resume calls=%#v", calls)
	}
}

func TestWorkspaceDeleteRejectsNonOwnerAndMismatchedFabricReadback(t *testing.T) {
	t.Run("cross-account", func(t *testing.T) {
		fabric := &workspaceDeleteFabric{}
		fixture := newWorkspaceDeleteFixture(t, newMemoryTableStore(), fabric)
		handler := fixture.server.(*controlPlaneHTTPHandler)
		_, err := handler.app.createUser(context.Background(), handler.service, map[string]any{
			"email": "beta@example.com", "accountId": "acct-beta", "password": "CorrectHorseBatteryStaple!",
		})
		if err != nil {
			t.Fatal(err)
		}
		other := loginForTest(t, fixture.server, "beta@example.com", "CorrectHorseBatteryStaple!")
		response := requestWithMutationKeyForTest(t, fixture.server, other, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-other")
		if response.Code != http.StatusForbidden || len(fabric.recordedCalls()) != 0 {
			t.Fatalf("non-owner status=%d body=%s calls=%#v", response.Code, response.Body.String(), fabric.recordedCalls())
		}
	})

	t.Run("workspace-owner-mismatch", func(t *testing.T) {
		fabric := &workspaceDeleteFabric{}
		fixture := newWorkspaceDeleteFixture(t, newMemoryTableStore(), fabric)
		workspace := cloneMap(fixture.workspace)
		workspace["ownerUserId"] = "usr-other-owner"
		if err := fixture.store.DeleteWorkspace(context.Background(), "ws-alpha"); err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.SaveWorkspace(context.Background(), workspace); err != nil {
			t.Fatal(err)
		}
		response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-owner-mismatch")
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "workspace_owner_required") || len(fabric.recordedCalls()) != 0 {
			t.Fatalf("owner mismatch status=%d body=%s calls=%#v", response.Code, response.Body.String(), fabric.recordedCalls())
		}
	})

	t.Run("mismatched owner readback", func(t *testing.T) {
		fabric := &workspaceDeleteFabric{mismatchStage: "attachment"}
		fixture := newWorkspaceDeleteFixture(t, newMemoryTableStore(), fabric)
		response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-mismatch")
		if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"status":"manual_review"`) {
			t.Fatalf("mismatch status=%d body=%s", response.Code, response.Body.String())
		}
		if _, found, err := fixture.store.GetWorkspace(context.Background(), "ws-alpha"); err != nil || !found {
			t.Fatalf("mismatch removed Workspace found=%v err=%v", found, err)
		}
		calls := fabric.recordedCalls()
		if len(calls) != 6 || !strings.HasPrefix(calls[0], "runtime-read:") || !strings.HasPrefix(calls[1], "secret-read:") ||
			!strings.HasPrefix(calls[2], "runtime:") || !strings.HasPrefix(calls[5], "attachment:") {
			t.Fatalf("mismatch calls=%#v", calls)
		}
	})
}

func TestWorkspaceDeleteUnknownFabricResultKeepsProjectionForManualReview(t *testing.T) {
	for _, stage := range []string{"runtime", "attachment", "storage", "compute", "compute-read"} {
		t.Run(stage, func(t *testing.T) {
			fabric := &workspaceDeleteFabric{unknownStage: stage}
			fixture := newWorkspaceDeleteFixture(t, newMemoryTableStore(), fabric)
			response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-unknown-"+stage)
			if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"status":"manual_review"`) {
				t.Fatalf("unknown %s status=%d body=%s", stage, response.Code, response.Body.String())
			}
			if _, found, err := fixture.store.GetWorkspace(context.Background(), "ws-alpha"); err != nil || !found {
				t.Fatalf("unknown %s removed Workspace found=%v err=%v", stage, found, err)
			}
			row, found, err := fixture.store.GetRuntimeOperation(context.Background(), workspaceDeleteOperationID("ws-alpha"))
			operation, decodeErr := decodeWorkspaceDeleteOperation(row)
			if err != nil || !found || decodeErr != nil || operation.Status != "manual_review" {
				t.Fatalf("unknown %s operation=%#v found=%v err=%v decode=%v", stage, operation, found, err, decodeErr)
			}
		})
	}
}

func TestWorkspaceDeleteRetainedStorageStaysManualReview(t *testing.T) {
	for _, status := range []string{"retained", "released"} {
		t.Run(status, func(t *testing.T) {
			fabric := &workspaceDeleteFabric{storageStatus: status}
			fixture := newWorkspaceDeleteFixture(t, newMemoryTableStore(), fabric)
			response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-storage-"+status)
			if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"status":"manual_review"`) {
				t.Fatalf("storage %s status=%d body=%s", status, response.Code, response.Body.String())
			}
			if _, found, err := fixture.store.GetWorkspace(context.Background(), "ws-alpha"); err != nil || !found {
				t.Fatalf("storage %s removed Workspace found=%v err=%v", status, found, err)
			}
		})
	}
}

func TestWorkspaceDeleteDoesNotReturnSuccessBeforeAuthoritativeAbsence(t *testing.T) {
	base := newMemoryTableStore()
	store := workspaceDeleteNoopDeleteStore{controlPlaneTableStore: base}
	fixture := newWorkspaceDeleteFixture(t, store, &workspaceDeleteFabric{})
	response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-without-absence")
	if response.Code == http.StatusOK {
		t.Fatalf("delete reported success before Workspace absence: %s", response.Body.String())
	}
	if _, found, err := fixture.store.GetWorkspace(context.Background(), "ws-alpha"); err != nil || !found {
		t.Fatalf("absence guard fixture lost Workspace found=%v err=%v", found, err)
	}
}

func TestWorkspaceDeleteStoreLifecycleMemoryAndSQLite(t *testing.T) {
	for _, storeCase := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "sqlite", new: func(t *testing.T) controlPlaneTableStore {
			return NewTestEntStateStore(t, t.TempDir()+"/workspace-delete.sqlite")
		}},
	} {
		t.Run(storeCase.name, func(t *testing.T) {
			exerciseWorkspaceDeleteStoreLifecycle(t, storeCase.new(t))
		})
	}
}

func TestPostgresWorkspaceDeleteStoreLifecycle(t *testing.T) {
	exerciseWorkspaceDeleteStoreLifecycle(t, newPostgresWorkspaceRenewalStore(t))
}

func exerciseWorkspaceDeleteStoreLifecycle(t *testing.T, store controlPlaneTableStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	workspace := map[string]any{
		"id": "ws-delete-store", "accountId": "acct-delete", "ownerAccountId": "acct-delete", "ownerUserId": "usr-delete",
		"currentComputeAllocationId": "compute-delete", "storageId": "storage-delete", "currentAttachmentId": "attachment-delete",
	}
	if err := store.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	claimed := workspaceDeleteStoreOperation(now)
	if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{Create: true, DesiredOperation: workspaceDeleteOperationRow(claimed)}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{Create: true, DesiredOperation: workspaceDeleteOperationRow(claimed)}); !errors.Is(err, errWorkspaceDeleteCASConflict) {
		t.Fatalf("duplicate claim err=%v", err)
	}
	complete := claimed
	complete.Phase, complete.Status = "complete", "succeeded"
	expected := stringValue(workspaceDeleteOperationRow(claimed)["result"])
	if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{RequireWorkspaceAbsent: true, ExpectedResult: expected, DesiredOperation: workspaceDeleteOperationRow(complete)}); !errors.Is(err, errWorkspaceDeleteCASConflict) {
		t.Fatalf("terminal update with Workspace present err=%v", err)
	}
	deleted := claimed
	deleted.Phase = "workspace_deleted"
	expected = stringValue(workspaceDeleteOperationRow(claimed)["result"])
	if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{DeleteWorkspace: true, ExpectedResult: expected, DesiredOperation: workspaceDeleteOperationRow(deleted)}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetWorkspace(ctx, claimed.WorkspaceID); err != nil || found {
		t.Fatalf("Workspace after atomic delete found=%v err=%v", found, err)
	}
	complete = deleted
	complete.Phase, complete.Status = "complete", "succeeded"
	expected = stringValue(workspaceDeleteOperationRow(deleted)["result"])
	if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{RequireWorkspaceAbsent: true, ExpectedResult: expected, DesiredOperation: workspaceDeleteOperationRow(complete)}); err != nil {
		t.Fatal(err)
	}
	row, found, err := store.GetRuntimeOperation(ctx, complete.OperationID)
	if err != nil || !found || stringValue(row["status"]) != "succeeded" {
		t.Fatalf("terminal operation=%#v found=%v err=%v", row, found, err)
	}
	if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{RequireWorkspaceAbsent: true, ExpectedResult: "stale", DesiredOperation: workspaceDeleteOperationRow(complete)}); !errors.Is(err, errWorkspaceDeleteCASConflict) {
		t.Fatalf("stale terminal update err=%v", err)
	}
}

func workspaceDeleteStoreOperation(now string) workspaceDeleteOperation {
	operation := workspaceDeleteOperation{
		OperationID: workspaceDeleteOperationID("ws-delete-store"), AccountID: "acct-delete", OwnerUserID: "usr-delete", Sub2APIUserID: 41,
		WorkspaceID: "ws-delete-store", LaunchOperationID: "workspace-launch-delete-store", RuntimeID: "runtime-delete",
		ComputeID: "compute-delete", StorageID: "storage-delete", AttachmentID: "attachment-delete", WorkspaceAPIKeyID: 19,
		GatewaySecretRef: "opl-gateway-ws-delete-store", GatewayFingerprint: "sha256:" + strings.Repeat("a", 64), DebitCode: "opl:debit-delete-store",
		PurchaseReceiptID: "receipt-purchase-delete-store", RefundCode: "opl:refund-delete-store", TotalUSDMicros: 52_580_000,
		Phase: "claimed", Status: "running", CreatedAt: now,
	}
	operation.PurchaseReceipt = clients.ReceiptInput{
		Type: "billing.workspace_purchased.v1", Status: "completed", AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, RequestID: operation.LaunchOperationID,
		Execution: map[string]any{"runtimeId": operation.RuntimeID, "computeAllocationId": operation.ComputeID, "storageId": operation.StorageID,
			"attachmentId": operation.AttachmentID, "workspaceApiKeyId": operation.WorkspaceAPIKeyID},
		Cost: map[string]any{"sub2apiUserId": operation.Sub2APIUserID, "sub2apiRedeemCode": operation.DebitCode, "totalUsdMicros": operation.TotalUSDMicros,
			"resourceId": operation.WorkspaceID},
	}
	operation.RequestHash = workspaceDeleteRequestHash(operation)
	return operation
}
