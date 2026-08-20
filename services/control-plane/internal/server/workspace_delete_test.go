package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
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
	computeTerminal     <-chan struct{}
	destroyed           bool
	runtimeResponseLost bool
	observeState        string
	secretObserveState  string
	observeErr          error
	observeRuntimeID    string
	observeKeyID        int64
	observeSecretRef    string
	observeFingerprint  string
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
		secretRef := f.observeSecretRef
		if secretRef == "" {
			secretRef = "opl-gateway-ws-alpha"
		}
		fingerprint := f.observeFingerprint
		if fingerprint == "" {
			fingerprint = "sha256:" + strings.Repeat("a", 64)
		}
		observation.Binding = &clients.WorkspaceRuntimeGatewaySecretBinding{
			WorkspaceID: workspaceID, WorkspaceAPIKeyID: keyID, SecretRef: secretRef, Fingerprint: fingerprint, Bound: state == clients.WorkspaceOwnerObservationReady,
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
	if f.computeTerminal != nil {
		select {
		case <-f.computeTerminal:
			status = "destroyed"
		default:
			status = "destroying"
		}
	} else if len(f.computeReads) > 0 {
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
	keyDeleteKeys   []string
	history         map[string]clients.Sub2APIBalanceHistoryEntry
	historyReads    [][]string
	refunds         []clients.Sub2APIRefundInput
	keyReadErr      error
	keyDeleteErr    error
	keyResponseLost bool
	keyName         string
	keyUserID       int64
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
	return s.DeleteUserKeyIdempotent(context.Background(), clients.SessionDelegatedCredential{}, userID, keyID, "")
}

func (s *workspaceDeleteSub2API) DeleteUserKeyIdempotent(_ context.Context, _ clients.SessionDelegatedCredential, userID, keyID int64, idempotencyKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keyDeletes++
	s.keyDeleteKeys = append(s.keyDeleteKeys, idempotencyKey)
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
	usedBy := input.UserID
	now := time.Now().UTC()
	s.history[input.Code] = clients.Sub2APIBalanceHistoryEntry{
		Code: input.Code, Type: "balance", ValueUSDMicros: input.RefundUSDMicros, Status: "used", UsedBy: &usedBy, UsedAt: &now,
	}
	return clients.Sub2APIRefund{Code: input.Code, UserID: input.UserID, RefundUSDMicros: input.RefundUSDMicros, Status: "used"}, nil
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
	l.events.add("ledger:deletion-receipt")
	if l.failures > 0 {
		l.failures--
		return clients.Receipt{}, errors.New("ledger receipt unavailable")
	}
	return clients.Receipt{ReceiptInput: input, ReceiptID: "receipt-delete-alpha"}, nil
}

type workspaceDeleteNoopDeleteStore struct {
	controlPlaneTableStore
}

type workspaceDeletePersistThenFailStore struct {
	controlPlaneTableStore
	failKeyReservation bool
}

type workspaceDeleteEventStore struct {
	controlPlaneTableStore
	events *workspaceDeleteEvents
}

type workspaceDeleteAuditFailStore struct {
	controlPlaneTableStore
	failAudit bool
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

func (s *workspaceDeleteAuditFailStore) SaveAuditEvent(ctx context.Context, event map[string]any) error {
	if s.failAudit {
		return errors.New("injected audit failure")
	}
	return s.controlPlaneTableStore.SaveAuditEvent(ctx, event)
}

func (s *workspaceDeletePersistThenFailStore) ApplyWorkspaceDelete(ctx context.Context, mutation workspaceDeleteStoreMutation) error {
	current, _, _ := s.controlPlaneTableStore.GetRuntimeOperation(ctx, stringValue(mutation.DesiredOperation["id"]))
	var before, after workspaceDeleteOperation
	_ = json.Unmarshal([]byte(stringValue(current["result"])), &before)
	_ = json.Unmarshal([]byte(stringValue(mutation.DesiredOperation["result"])), &after)
	if err := s.controlPlaneTableStore.ApplyWorkspaceDelete(ctx, mutation); err != nil {
		return err
	}
	if s.failKeyReservation && !before.KeyDeleteAttempted && after.KeyDeleteAttempted {
		s.failKeyReservation = false
		return errors.New("injected crash after key reservation")
	}
	return nil
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
	if err := fixture.store.SaveCompute(context.Background(), map[string]any{
		"id": "compute-alpha", "accountId": "acct-alpha", "ownerUserId": fixture.workspace["ownerUserId"], "workspaceId": "ws-alpha", "status": "running",
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.SaveStorage(context.Background(), map[string]any{
		"id": "storage-alpha", "accountId": "acct-alpha", "ownerUserId": fixture.workspace["ownerUserId"], "workspaceId": "ws-alpha", "status": "available",
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.SaveAttachment(context.Background(), map[string]any{
		"id": "attachment-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "computeAllocationId": "compute-alpha", "storageId": "storage-alpha", "volumeId": "storage-alpha", "status": "attached",
	}); err != nil {
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
	var terminal map[string]any
	if json.Unmarshal(response.Body.Bytes(), &terminal) != nil || terminal["status"] != "deleted" || terminal["accountId"] != "acct-alpha" ||
		terminal["launchOperationId"] != "workspace-launch-alpha" || terminal["operationId"] != workspaceDeleteOperationID("ws-alpha") ||
		terminal["workspaceId"] != "ws-alpha" || terminal["runtimeId"] != "runtime-alpha" ||
		int64(numberField(terminal, "sub2apiUserId", 0)) != 41 || int64(numberField(terminal, "workspaceApiKeyId", 0)) != 19 ||
		terminal["launchReceiptId"] != "receipt-purchase-alpha" || terminal["deletionReceiptId"] != "receipt-delete-alpha" ||
		terminal["runtimeStatus"] != "absent" || terminal["secretStatus"] != "absent" || terminal["keyStatus"] != "absent" {
		t.Fatalf("delete terminal response=%#v", terminal)
	}
	if sub2API.keyExists || sub2API.keyDeletes != 1 || len(sub2API.historyReads) != 0 || len(sub2API.refunds) != 0 {
		t.Fatalf("Sub2API completion keyExists=%v keyReads=%d keyDeletes=%d refunds=%#v", sub2API.keyExists, sub2API.keyReads, sub2API.keyDeletes, sub2API.refunds)
	}
	if len(ledger.receipts) != 1 || len(ledger.keys) != 1 {
		t.Fatalf("Ledger writes receipts=%#v keys=%#v", ledger.receipts, ledger.keys)
	}
	computes, computeErr := fixture.store.ListComputes(context.Background(), "acct-alpha")
	storages, storageErr := fixture.store.ListStorages(context.Background(), "acct-alpha")
	attachments, attachmentErr := fixture.store.ListAttachments(context.Background(), "acct-alpha")
	if computeErr != nil || storageErr != nil || attachmentErr != nil || len(computes) != 0 || len(storages) != 0 || len(attachments) != 0 {
		t.Fatalf("Delete left resource projections computes=%#v storages=%#v attachments=%#v errors=%v/%v/%v", computes, storages, attachments, computeErr, storageErr, attachmentErr)
	}
	receipt := ledger.receipts[0]
	if receipt.Type != "workspace.deleted.v1" || receipt.Status != "completed" || receipt.Surface != "control_plane" || receipt.AccountID != "acct-alpha" || receipt.WorkspaceID != "ws-alpha" ||
		receipt.RequestID != workspaceDeleteOperationID("ws-alpha") || len(receipt.Cost) != 0 || receipt.SupersedesReceiptID != "" ||
		stringValue(receipt.Execution["runtimeId"]) != "runtime-alpha" ||
		stringValue(receipt.Execution["computeAllocationId"]) != "compute-alpha" || stringValue(receipt.Execution["storageId"]) != "storage-alpha" ||
		stringValue(receipt.Execution["attachmentId"]) != "attachment-alpha" || int64(numberField(receipt.Execution, "workspaceApiKeyId", 0)) != 19 ||
		stringValue(receipt.InputRefs["launchReceiptId"]) != "receipt-purchase-alpha" || len(receipt.InputRefs) != 1 ||
		stringValue(receipt.Owner["accountId"]) != "acct-alpha" || stringValue(receipt.Owner["workspaceId"]) != "ws-alpha" {
		t.Fatalf("deletion receipt=%#v", receipt)
	}
	if ledger.keys[0] != workspaceDeleteOperationID("ws-alpha")+":deletion-receipt" {
		t.Fatalf("deletion receipt key=%#v", ledger.keys)
	}
	row, found, err := fixture.store.GetRuntimeOperation(context.Background(), workspaceDeleteOperationID("ws-alpha"))
	if err != nil || !found {
		t.Fatalf("delete operation found=%v err=%v", found, err)
	}
	var result map[string]any
	if json.Unmarshal([]byte(stringValue(row["result"])), &result) != nil || result["accountId"] != "acct-alpha" ||
		result["operationId"] != workspaceDeleteOperationID("ws-alpha") || result["workspaceId"] != "ws-alpha" || result["runtimeId"] != "runtime-alpha" ||
		int64(numberField(result, "workspaceApiKeyId", 0)) != 19 || result["launchReceiptId"] != "receipt-purchase-alpha" ||
		result["deletionReceiptId"] != "receipt-delete-alpha" {
		t.Fatalf("durable identity result=%#v", result)
	}
	wantEvents := []string{
		"ledger:purchase-get",
		"fabric:runtime-read", "fabric:secret-read", "fabric:runtime", "fabric:runtime-read", "fabric:secret-read",
		"fabric:attachment", "fabric:storage", "fabric:compute", "fabric:compute-read",
		"sub2api:key-get", "sub2api:key-get", "sub2api:key-delete", "sub2api:key-get",
		"control-plane:workspace-absent", "ledger:deletion-receipt",
	}
	if got := events.snapshot(); strings.Join(got, "\n") != strings.Join(wantEvents, "\n") {
		t.Fatalf("owner completion events=%#v want=%#v", got, wantEvents)
	}
}

func TestWorkspaceDeleteUsesCurrentGatewayIdentityAfterCompletedRotationLineage(t *testing.T) {
	fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
	const (
		firstKeyID   = int64(29)
		currentKeyID = int64(39)
	)
	first := workspaceKeyRotationOperation{
		RequestHash: "rotation-first-request", Phase: "succeeded", OldKeyID: 19, NewKeyID: firstKeyID, ReplacementGroupID: 7,
		ReplacementName: "opl-workspace-replacement-first", RetiredName: "opl-workspace-retired-first",
		BudgetSnapshotCaptured: true, BudgetCapturedAt: "2026-08-16T00:00:00Z",
		SecretRef: "opl-gateway-ws-alpha", Fingerprint: "sha256:" + strings.Repeat("b", 64), ReceiptID: "receipt-rotation-first", CompletedAt: "2026-08-16T00:00:00Z",
		AuditEvent: workspaceDeleteRotationAudit("rotation-first", "acct-alpha", "ws-alpha"),
	}
	second := workspaceKeyRotationOperation{
		RequestHash: "rotation-second-request", Phase: "succeeded", OldKeyID: firstKeyID, NewKeyID: currentKeyID, ReplacementGroupID: 7,
		ReplacementName: "opl-workspace-replacement-second", RetiredName: "opl-workspace-retired-second",
		BudgetSnapshotCaptured: true, BudgetCapturedAt: "2026-08-17T00:00:00Z",
		SecretRef: "opl-gateway-ws-alpha", Fingerprint: "sha256:" + strings.Repeat("c", 64), ReceiptID: "receipt-rotation-second", CompletedAt: "2026-08-17T00:00:00Z",
		AuditEvent: workspaceDeleteRotationAudit("rotation-second", "acct-alpha", "ws-alpha"),
	}
	if err := fixture.store.SaveRuntimeOperation(context.Background(), workspaceKeyRotationRow("rotation-first", "acct-alpha", "ws-alpha", "succeeded", first)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.CompareAndSwapWorkspaceAPIKey(context.Background(), "ws-alpha", 19, firstKeyID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.SaveRuntimeOperation(context.Background(), workspaceKeyRotationRow("rotation-second", "acct-alpha", "ws-alpha", "succeeded", second)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.CompareAndSwapWorkspaceAPIKey(context.Background(), "ws-alpha", firstKeyID, currentKeyID); err != nil {
		t.Fatal(err)
	}
	sub2API.mu.Lock()
	sub2API.keyID = currentKeyID
	sub2API.mu.Unlock()
	fixture.fabric.mu.Lock()
	fixture.fabric.observeKeyID = currentKeyID
	fixture.fabric.observeSecretRef = second.SecretRef
	fixture.fabric.observeFingerprint = second.Fingerprint
	fixture.fabric.mu.Unlock()

	response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-after-rotations")
	if response.Code != http.StatusOK {
		row, _, _ := fixture.store.GetRuntimeOperation(context.Background(), workspaceDeleteOperationID("ws-alpha"))
		t.Fatalf("Delete after rotations status=%d body=%s operation=%#v", response.Code, response.Body.String(), row)
	}
	if sub2API.keyDeletes != 1 || sub2API.keyID != currentKeyID || sub2API.keyExists {
		t.Fatalf("Delete did not remove only current Key: id=%d deletes=%d exists=%v", sub2API.keyID, sub2API.keyDeletes, sub2API.keyExists)
	}
	if len(ledger.receipts) != 1 || int64(numberField(ledger.receipts[0].Execution, "workspaceApiKeyId", 0)) != currentKeyID ||
		stringValue(ledger.receipts[0].Execution["workspaceKeyFingerprint"]) != second.Fingerprint || stringValue(ledger.receipts[0].Execution["gatewaySecretRef"]) != second.SecretRef {
		t.Fatalf("Deletion Receipt did not bind current gateway identity: %#v", ledger.receipts)
	}
}

func TestWorkspaceDeleteStopsBeforeMutationWhileKeyRotationIsActive(t *testing.T) {
	fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
	rotation := workspaceKeyRotationOperation{
		RequestHash: "rotation-active-request", Phase: "replacement_check", OldKeyID: 19,
		ReplacementName: "opl-workspace-replacement-active", RetiredName: "opl-workspace-retired-active",
		AuditEvent: workspaceDeleteRotationAudit("rotation-active", "acct-alpha", "ws-alpha"),
	}
	if err := fixture.store.ClaimWorkspaceKeyRotation(context.Background(), workspaceKeyRotationRow("rotation-active", "acct-alpha", "ws-alpha", "started", rotation)); err != nil {
		t.Fatal(err)
	}
	response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-during-rotation")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), errWorkspaceKeyRotationInProgress.Error()) {
		t.Fatalf("Delete during Rotation status=%d body=%s", response.Code, response.Body.String())
	}
	if calls := fixture.fabric.recordedCalls(); len(calls) != 0 || sub2API.keyDeletes != 0 || len(ledger.receipts) != 0 {
		t.Fatalf("Delete crossed mutation boundary calls=%#v keyDeletes=%d receipts=%#v", calls, sub2API.keyDeletes, ledger.receipts)
	}
}

func TestWorkspaceDeleteAcceptsExactCurrentAndHistoricalLaunchReceiptsWithoutMoneyCalls(t *testing.T) {
	t.Run("historical charged", func(t *testing.T) {
		fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
		ledger.purchase.ReceiptInput = workspaceLaunchHistoricalChargedReceiptInput(ledger.purchase.ReceiptInput)
		response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-historical-charged")
		if response.Code != http.StatusOK || len(sub2API.historyReads) != 0 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 1 {
			t.Fatalf("historical charged status=%d history=%#v refunds=%d receipts=%d body=%s", response.Code, sub2API.historyReads, len(sub2API.refunds), len(ledger.receipts), response.Body.String())
		}
	})

	for _, legacy := range []bool{false, true} {
		name := "current zero-cost"
		if legacy {
			name = "legacy zero-cost"
		}
		t.Run(name, func(t *testing.T) {
			fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
			configureWorkspaceDeleteZeroCostLaunch(t, fixture, ledger, legacy)
			response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-zero-cost")
			if response.Code != http.StatusOK || len(sub2API.historyReads) != 0 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 1 || ledger.receipts[0].Type != "workspace.deleted.v1" {
				t.Fatalf("zero-cost legacy=%v status=%d history=%#v refunds=%d receipts=%#v body=%s", legacy, response.Code, sub2API.historyReads, len(sub2API.refunds), ledger.receipts, response.Body.String())
			}
		})
	}
}

func configureWorkspaceDeleteZeroCostLaunch(t *testing.T, fixture workspaceDeleteFixture, ledger *workspaceDeleteLedger, legacy bool) {
	t.Helper()
	row, found, err := fixture.store.GetRuntimeOperation(context.Background(), "workspace-launch-alpha")
	if err != nil || !found {
		t.Fatalf("launch found=%v err=%v", found, err)
	}
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{
		"resourceBillingEnabled": false, "chargeAttempted": false, "chargeConfirmation": map[string]any{"status": "not_charged"},
		"preChargeBalanceUsdMicros": int64(0), "postChargeBalanceUsdMicros": int64(0), "postChargeBalanceKnown": true, "billingPeriodState": "not_billed",
		"totalChargeUsdMicros": int64(0),
	} {
		operation.raw[key], _ = json.Marshal(value)
	}
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
	if err := fixture.store.DeleteWorkspace(context.Background(), "ws-alpha"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	input, err := workspaceLaunchPurchaseReceiptInput(operation)
	if err != nil {
		t.Fatal(err)
	}
	if legacy {
		input = workspaceLaunchLegacyCreatedReceiptInput(operation)
	}
	ledger.purchase = clients.Receipt{ReceiptInput: input, ReceiptID: "receipt-purchase-alpha"}
}

func TestWorkspaceDeleteHistoricalV1IsReadOnly(t *testing.T) {
	if workspaceDeleteOperationID("ws-alpha") == workspaceDeleteLegacyOperationID("ws-alpha") {
		t.Fatal("v2 and historical v1 operation IDs must differ")
	}

	t.Run("completed replay with Workspace absent", func(t *testing.T) {
		fixture, sub2API, ledger, events := newWorkspaceDeleteEventFixture(t)
		legacy := workspaceDeleteLegacyFixtureRow(fixture, ledger.purchase.ReceiptInput, "complete", "succeeded")
		if err := fixture.store.SaveRuntimeOperation(context.Background(), legacy); err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.DeleteWorkspace(context.Background(), "ws-alpha"); err != nil {
			t.Fatal(err)
		}
		response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-v1-complete-replay")
		var payload map[string]any
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload["operationId"] != workspaceDeleteLegacyOperationID("ws-alpha") || payload["historical"] != true ||
			len(events.snapshot()) != 0 || len(fixture.fabric.recordedCalls()) != 0 || sub2API.keyDeletes != 0 || len(sub2API.historyReads) != 0 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 0 {
			t.Fatalf("historical terminal status=%d payload=%#v events=%#v calls=%#v keyDeletes=%d history=%#v refunds=%d receipts=%d", response.Code, payload, events.snapshot(), fixture.fabric.recordedCalls(), sub2API.keyDeletes, sub2API.historyReads, len(sub2API.refunds), len(ledger.receipts))
		}
	})

	t.Run("active operation conflicts before mutation", func(t *testing.T) {
		fixture, sub2API, ledger, events := newWorkspaceDeleteEventFixture(t)
		legacy := workspaceDeleteLegacyFixtureRow(fixture, ledger.purchase.ReceiptInput, "storage_destroyed", "running")
		if err := fixture.store.SaveRuntimeOperation(context.Background(), legacy); err != nil {
			t.Fatal(err)
		}
		response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-v1-active-conflict")
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), errWorkspaceDeleteHistoricalConflict.Error()) ||
			len(events.snapshot()) != 0 || len(fixture.fabric.recordedCalls()) != 0 || sub2API.keyDeletes != 0 || len(sub2API.historyReads) != 0 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 0 {
			t.Fatalf("historical active status=%d body=%s events=%#v calls=%#v keyDeletes=%d history=%#v refunds=%d receipts=%d", response.Code, response.Body.String(), events.snapshot(), fixture.fabric.recordedCalls(), sub2API.keyDeletes, sub2API.historyReads, len(sub2API.refunds), len(ledger.receipts))
		}
	})

	for _, testCase := range []struct {
		name   string
		mutate func(*workspaceDeleteLegacyOperation)
	}{
		{name: "request hash drift", mutate: func(operation *workspaceDeleteLegacyOperation) { operation.RequestHash = "drift" }},
		{name: "resource identity drift", mutate: func(operation *workspaceDeleteLegacyOperation) {
			operation.RuntimeID = "runtime-other"
			operation.RequestHash = workspaceDeleteLegacyRequestHash(*operation)
		}},
		{name: "terminal evidence missing", mutate: func(operation *workspaceDeleteLegacyOperation) { operation.RefundReceiptID = "" }},
		{name: "v2-only compute terminal", mutate: func(operation *workspaceDeleteLegacyOperation) { operation.ComputeStatus = "absent" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture, sub2API, ledger, events := newWorkspaceDeleteEventFixture(t)
			legacy := workspaceDeleteLegacyFixtureRow(fixture, ledger.purchase.ReceiptInput, "complete", "succeeded")
			var operation workspaceDeleteLegacyOperation
			if json.Unmarshal([]byte(stringValue(legacy["result"])), &operation) != nil {
				t.Fatal("legacy fixture decode failed")
			}
			testCase.mutate(&operation)
			legacy["result"] = string(mustJSON(operation))
			if err := fixture.store.SaveRuntimeOperation(context.Background(), legacy); err != nil {
				t.Fatal(err)
			}
			if err := fixture.store.DeleteWorkspace(context.Background(), "ws-alpha"); err != nil {
				t.Fatal(err)
			}
			response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-v1-corrupt-"+testCase.name)
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), errWorkspaceDeleteHistoricalConflict.Error()) ||
				len(events.snapshot()) != 0 || len(fixture.fabric.recordedCalls()) != 0 || sub2API.keyDeletes != 0 || len(sub2API.historyReads) != 0 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 0 {
				t.Fatalf("corrupt historical status=%d body=%s events=%#v calls=%#v", response.Code, response.Body.String(), events.snapshot(), fixture.fabric.recordedCalls())
			}
		})
	}
}

func workspaceDeleteLegacyFixtureRow(fixture workspaceDeleteFixture, purchaseReceipt clients.ReceiptInput, phase, status string) map[string]any {
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	operationID := workspaceDeleteLegacyOperationID("ws-alpha")
	operation := workspaceDeleteLegacyOperation{
		OperationID: operationID, AccountID: "acct-alpha", OwnerUserID: stringValue(fixture.workspace["ownerUserId"]), Sub2APIUserID: 41, WorkspaceID: "ws-alpha",
		LaunchOperationID: "workspace-launch-alpha", RuntimeID: "runtime-alpha", ComputeID: "compute-alpha", StorageID: "storage-alpha", AttachmentID: "attachment-alpha",
		WorkspaceAPIKeyID: 19, GatewaySecretRef: "opl-gateway-ws-alpha", GatewayFingerprint: "sha256:" + strings.Repeat("a", 64),
		DebitCode: "opl:workspace-purchase-alpha", PurchaseReceiptID: "receipt-purchase-alpha", PurchaseReceipt: purchaseReceipt,
		RefundCode: monthlyRefundCode(monthlyEnvironment(), operationID), TotalUSDMicros: 52_580_000, Phase: phase, Status: status, CreatedAt: createdAt,
	}
	if phase == "complete" && status == "succeeded" {
		operation.RuntimeStatus, operation.SecretStatus, operation.AttachmentStatus = "absent", "absent", "detached"
		operation.StorageStatus, operation.ComputeStatus, operation.KeyStatus = "destroyed", "destroyed", "absent"
		operation.RefundReceiptID = "receipt-refund-alpha"
		operation.RefundConfirmation = map[string]any{"code": operation.RefundCode, "userId": int64(41), "refundUsdMicros": int64(52_580_000), "status": "used"}
	}
	operation.RequestHash = workspaceDeleteLegacyRequestHash(operation)
	result, _ := json.Marshal(operation)
	return map[string]any{
		"id": operation.OperationID, "operationId": operation.OperationID, "accountId": operation.AccountID, "workspaceId": operation.WorkspaceID,
		"resourceId": operation.WorkspaceID, "resourceKind": "workspace", "action": workspaceDeleteLegacyAction, "status": status, "result": string(result),
		"computeAllocationId": operation.ComputeID, "storageId": operation.StorageID, "attachmentId": operation.AttachmentID, "runtimeId": operation.RuntimeID,
		"createdAt": createdAt,
	}
}

func TestWorkspaceDeleteComputePendingKeepsSameOperationAndOneMutation(t *testing.T) {
	fabric := &workspaceDeleteFabric{computeReads: []string{"destroying", "destroying", "destroying", "destroying", "destroying", "destroying", "destroying", "destroying"}}
	fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixtureWith(t, newMemoryTableStore(), fabric)
	const mutationKey = "delete-compute-pending"

	first := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, mutationKey)
	if first.Code != http.StatusAccepted {
		t.Fatalf("pending delete status=%d body=%s", first.Code, first.Body.String())
	}
	var pending map[string]any
	if json.Unmarshal(first.Body.Bytes(), &pending) != nil || pending["status"] != "pending" || pending["phase"] != "storage_absent" ||
		pending["ownerStage"] != "compute" || int64(numberField(pending, "computeReadbacks", 0)) != 1 ||
		int64(numberField(pending, "maxComputeReadbacks", 0)) != workspaceDeleteComputeReadbackBudget {
		t.Fatalf("pending delete response=%#v", pending)
	}
	row, found, err := fixture.store.GetRuntimeOperation(context.Background(), workspaceDeleteOperationID("ws-alpha"))
	operation, decodeErr := decodeWorkspaceDeleteOperation(row)
	if err != nil || !found || decodeErr != nil || operation.Status != "running" || operation.Phase != "storage_absent" ||
		operation.ComputeStatus != "destroying" || operation.ComputeReadbacks != 1 || operation.MaxComputeReadbacks != workspaceDeleteComputeReadbackBudget || operation.LastErrorCode != "" {
		t.Fatalf("pending delete operation=%#v found=%v err=%v decode=%v", operation, found, err, decodeErr)
	}
	fabric.mu.Lock()
	fabric.computeReads = []string{"destroyed"}
	fabric.mu.Unlock()
	expireWorkspaceDeleteComputeReadback(t, fixture.store)

	second := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, mutationKey)
	if second.Code != http.StatusOK {
		t.Fatalf("continued delete status=%d body=%s", second.Code, second.Body.String())
	}
	computeMutations, computeReads := 0, 0
	for _, call := range fabric.recordedCalls() {
		if strings.HasPrefix(call, "compute:") {
			computeMutations++
		}
		if strings.HasPrefix(call, "compute-read:") {
			computeReads++
		}
	}
	if computeMutations != 1 || computeReads != 2 || sub2API.keyDeletes != 1 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 1 {
		t.Fatalf("continued delete mutations=%d reads=%d keyDeletes=%d refunds=%d receipts=%d calls=%#v", computeMutations, computeReads, sub2API.keyDeletes, len(sub2API.refunds), len(ledger.receipts), fabric.recordedCalls())
	}
}

func TestWorkspaceDeleteComputePendingWaitsForServerScheduleWithoutConsumingRead(t *testing.T) {
	workerTerminal := make(chan struct{})
	fabric := &workspaceDeleteFabric{computeTerminal: workerTerminal}
	fixture, _, _ := newWorkspaceDeleteCompletionFixtureWith(t, newMemoryTableStore(), fabric)
	const mutationKey = "delete-compute-scheduled-read"

	first := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, mutationKey)
	if first.Code != http.StatusAccepted || first.Header().Get("Retry-After") != "1" {
		t.Fatalf("initial pending status=%d retry-after=%q body=%s", first.Code, first.Header().Get("Retry-After"), first.Body.String())
	}
	second := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, mutationKey)
	if second.Code != http.StatusAccepted {
		t.Fatalf("early continuation status=%d body=%s", second.Code, second.Body.String())
	}
	var pending map[string]any
	if json.Unmarshal(second.Body.Bytes(), &pending) != nil || int64(numberField(pending, "computeReadbacks", 0)) != 1 {
		t.Fatalf("early continuation consumed read slot: %#v", pending)
	}
	computeMutations, computeReads := 0, 0
	for _, call := range fabric.recordedCalls() {
		if strings.HasPrefix(call, "compute:") {
			computeMutations++
		}
		if strings.HasPrefix(call, "compute-read:") {
			computeReads++
		}
	}
	if computeMutations != 1 || computeReads != 1 {
		t.Fatalf("early continuation mutations=%d reads=%d calls=%#v", computeMutations, computeReads, fabric.recordedCalls())
	}

	close(workerTerminal)
	expireWorkspaceDeleteComputeReadback(t, fixture.store)
	terminal := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, mutationKey)
	if terminal.Code != http.StatusOK {
		t.Fatalf("scheduled terminal status=%d body=%s", terminal.Code, terminal.Body.String())
	}
	computeMutations, computeReads = 0, 0
	for _, call := range fabric.recordedCalls() {
		if strings.HasPrefix(call, "compute:") {
			computeMutations++
		}
		if strings.HasPrefix(call, "compute-read:") {
			computeReads++
		}
	}
	if computeMutations != 1 || computeReads != 2 {
		t.Fatalf("terminal mutations=%d reads=%d calls=%#v", computeMutations, computeReads, fabric.recordedCalls())
	}
}

func TestWorkspaceDeleteComputePendingBudgetAndFailureMatrix(t *testing.T) {
	t.Run("permanent pending exhausts exact read budget", func(t *testing.T) {
		reads := make([]string, workspaceDeleteComputeReadbackBudget)
		for index := range reads {
			reads[index] = "destroying"
		}
		fabric := &workspaceDeleteFabric{computeReads: reads}
		fixture, _, _ := newWorkspaceDeleteCompletionFixtureWith(t, newMemoryTableStore(), fabric)
		for readback := 1; readback <= workspaceDeleteComputeReadbackBudget; readback++ {
			if readback > 1 {
				expireWorkspaceDeleteComputeReadback(t, fixture.store)
			}
			response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-compute-permanent-pending")
			if readback < workspaceDeleteComputeReadbackBudget && response.Code != http.StatusAccepted {
				t.Fatalf("pending readback %d status=%d body=%s", readback, response.Code, response.Body.String())
			}
			if readback == workspaceDeleteComputeReadbackBudget && response.Code != http.StatusBadGateway {
				t.Fatalf("exhausted readback status=%d body=%s", response.Code, response.Body.String())
			}
		}
		row, found, err := fixture.store.GetRuntimeOperation(context.Background(), workspaceDeleteOperationID("ws-alpha"))
		operation, decodeErr := decodeWorkspaceDeleteOperation(row)
		if err != nil || !found || decodeErr != nil || operation.Status != "manual_review" || operation.Phase != "storage_absent" ||
			operation.LastErrorCode != "fabric_compute_absence_unconfirmed" || operation.ComputeReadbacks != workspaceDeleteComputeReadbackBudget {
			t.Fatalf("exhausted operation=%#v found=%v err=%v decode=%v", operation, found, err, decodeErr)
		}
		computeMutations, computeReads := 0, 0
		for _, call := range fabric.recordedCalls() {
			if strings.HasPrefix(call, "compute:") {
				computeMutations++
			}
			if strings.HasPrefix(call, "compute-read:") {
				computeReads++
			}
		}
		if computeMutations != 1 || computeReads != workspaceDeleteComputeReadbackBudget {
			t.Fatalf("permanent pending mutations=%d reads=%d calls=%#v", computeMutations, computeReads, fabric.recordedCalls())
		}
	})

	for _, test := range []struct {
		name      string
		configure func(*workspaceDeleteFabric)
	}{
		{name: "unknown", configure: func(fabric *workspaceDeleteFabric) { fabric.unknownStage = "compute-read" }},
		{name: "identity conflict", configure: func(fabric *workspaceDeleteFabric) { fabric.mismatchStage = "compute-read" }},
		{name: "owner error", configure: func(fabric *workspaceDeleteFabric) { fabric.failStage, fabric.failures = "compute-read", 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fabric := &workspaceDeleteFabric{computeReads: []string{"destroying"}}
			fixture, _, _ := newWorkspaceDeleteCompletionFixtureWith(t, newMemoryTableStore(), fabric)
			first := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-compute-fail-closed")
			if first.Code != http.StatusAccepted {
				t.Fatalf("initial pending status=%d body=%s", first.Code, first.Body.String())
			}
			test.configure(fabric)
			expireWorkspaceDeleteComputeReadback(t, fixture.store)
			second := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-compute-fail-closed")
			if second.Code != http.StatusBadGateway {
				t.Fatalf("fail-closed status=%d body=%s", second.Code, second.Body.String())
			}
			row, found, err := fixture.store.GetRuntimeOperation(context.Background(), workspaceDeleteOperationID("ws-alpha"))
			operation, decodeErr := decodeWorkspaceDeleteOperation(row)
			if err != nil || !found || decodeErr != nil || operation.Status != "manual_review" || operation.LastErrorCode != "fabric_compute_absence_unconfirmed" {
				t.Fatalf("fail-closed operation=%#v found=%v err=%v decode=%v", operation, found, err, decodeErr)
			}
			computeMutations := 0
			for _, call := range fabric.recordedCalls() {
				if strings.HasPrefix(call, "compute:") {
					computeMutations++
				}
			}
			if computeMutations != 1 {
				t.Fatalf("fail-closed compute mutations=%d calls=%#v", computeMutations, fabric.recordedCalls())
			}
		})
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

	t.Run("financial history is not consulted", func(t *testing.T) {
		fixture, sub2API, ledger, events := newWorkspaceDeleteEventFixture(t)
		entry := sub2API.history["opl:workspace-purchase-alpha"]
		entry.ValueUSDMicros++
		sub2API.history[entry.Code] = entry
		response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-debit-conflict")
		if response.Code != http.StatusOK || len(sub2API.historyReads) != 0 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 1 ||
			!slices.Contains(events.snapshot(), "ledger:deletion-receipt") {
			t.Fatalf("delete status=%d events=%#v history=%#v refunds=%d receipts=%d", response.Code, events.snapshot(), sub2API.historyReads, len(sub2API.refunds), len(ledger.receipts))
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

func TestWorkspaceDeleteKeyOwnerConflictsStopSubsequentMutation(t *testing.T) {
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

}

func TestWorkspaceDeleteResponseLossAndReceiptOnlyRecovery(t *testing.T) {
	t.Run("runtime response loss", func(t *testing.T) {
		fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
		fixture.fabric.runtimeResponseLost = true
		response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-runtime-response-loss")
		if response.Code != http.StatusOK || sub2API.keyDeletes != 1 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 1 {
			t.Fatalf("status=%d body=%s keyDeletes=%d refunds=%d receipts=%d", response.Code, response.Body.String(), sub2API.keyDeletes, len(sub2API.refunds), len(ledger.receipts))
		}
	})

	t.Run("key response loss", func(t *testing.T) {
		fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
		sub2API.keyResponseLost = true
		first := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-key-response-loss")
		if first.Code != http.StatusOK || sub2API.keyDeletes != 1 || sub2API.keyExists == true || len(sub2API.refunds) != 0 || len(ledger.receipts) != 1 {
			t.Fatalf("first status=%d keyExists=%v deletes=%d refunds=%d", first.Code, sub2API.keyExists, sub2API.keyDeletes, len(sub2API.refunds))
		}
		second := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-key-response-loss-replay")
		if second.Code != http.StatusOK || sub2API.keyDeletes != 1 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 1 {
			t.Fatalf("replay status=%d body=%s deletes=%d refunds=%d receipts=%d", second.Code, second.Body.String(), sub2API.keyDeletes, len(sub2API.refunds), len(ledger.receipts))
		}
	})

	t.Run("receipt failure", func(t *testing.T) {
		fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
		ledger.failures = 1
		first := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-receipt-failure")
		fabricCalls := len(fixture.fabric.recordedCalls())
		if first.Code != http.StatusBadGateway || len(sub2API.refunds) != 0 || len(ledger.receipts) != 1 {
			t.Fatalf("first status=%d refunds=%d receipts=%d", first.Code, len(sub2API.refunds), len(ledger.receipts))
		}
		row, found, err := fixture.store.GetRuntimeOperation(context.Background(), workspaceDeleteOperationID("ws-alpha"))
		operation, decodeErr := decodeWorkspaceDeleteOperation(row)
		if err != nil || !found || decodeErr != nil || operation.Phase != "workspace_absent" || operation.Status != "running" ||
			ledger.keys[0] != operation.OperationID+":deletion-receipt" {
			t.Fatalf("receipt-only recovery point operation=%#v found=%v err=%v decode=%v keys=%#v", operation, found, err, decodeErr, ledger.keys)
		}
		if _, found, err := fixture.store.GetWorkspace(context.Background(), "ws-alpha"); err != nil || found {
			t.Fatalf("Workspace must already be absent before receipt retry found=%v err=%v", found, err)
		}
		second := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-receipt-failure-replay")
		if second.Code != http.StatusOK || len(sub2API.refunds) != 0 || sub2API.keyDeletes != 1 || len(ledger.receipts) != 2 || len(fixture.fabric.recordedCalls()) != fabricCalls {
			t.Fatalf("replay status=%d refunds=%d keyDeletes=%d receipts=%d Fabric calls=%d/%d", second.Code, len(sub2API.refunds), sub2API.keyDeletes, len(ledger.receipts), len(fixture.fabric.recordedCalls()), fabricCalls)
		}
		if ledger.keys[0] != ledger.keys[1] || ledger.keys[1] != workspaceDeleteOperationID("ws-alpha")+":deletion-receipt" {
			t.Fatalf("receipt retry changed idempotency key: %#v", ledger.keys)
		}
	})
}

func TestWorkspaceDeleteCrashBeforeOwnerSendUsesOneAuthorizedExactReplay(t *testing.T) {
	t.Run("Key DELETE reservation", func(t *testing.T) {
		base := newMemoryTableStore()
		store := &workspaceDeletePersistThenFailStore{controlPlaneTableStore: base, failKeyReservation: true}
		fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixtureWith(t, store, &workspaceDeleteFabric{})
		first := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-key-crash")
		if first.Code != http.StatusInternalServerError || sub2API.keyDeletes != 0 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 0 {
			t.Fatalf("first status=%d keyDeletes=%d refunds=%d receipts=%d", first.Code, sub2API.keyDeletes, len(sub2API.refunds), len(ledger.receipts))
		}
		second := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "authorize-key-crash-replay")
		if second.Code != http.StatusOK || sub2API.keyDeletes != 1 || len(sub2API.keyDeleteKeys) != 1 ||
			sub2API.keyDeleteKeys[0] != workspaceDeleteOperationID("ws-alpha")+":key" || len(sub2API.refunds) != 0 || len(ledger.receipts) != 1 {
			t.Fatalf("replay status=%d body=%s deletes=%d keys=%#v refunds=%d receipts=%d", second.Code, second.Body.String(), sub2API.keyDeletes, sub2API.keyDeleteKeys, len(sub2API.refunds), len(ledger.receipts))
		}
	})
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
	if sub2API.keyDeletes != 1 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 1 {
		t.Fatalf("concurrent keyDeletes=%d refunds=%d receipts=%d", sub2API.keyDeletes, len(sub2API.refunds), len(ledger.receipts))
	}
}

func TestWorkspaceDeleteCompletedOperationRejectsRecreatedWorkspaceWithoutMutation(t *testing.T) {
	fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
	first := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-before-recreate")
	if first.Code != http.StatusOK {
		t.Fatalf("initial delete status=%d body=%s", first.Code, first.Body.String())
	}
	operationID := workspaceDeleteOperationID("ws-alpha")
	terminalRow, found, err := fixture.store.GetRuntimeOperation(context.Background(), operationID)
	if err != nil || !found {
		t.Fatalf("terminal operation found=%v err=%v", found, err)
	}
	terminalResult := stringValue(terminalRow["result"])
	if err := fixture.store.SaveWorkspace(context.Background(), cloneMap(fixture.workspace)); err != nil {
		t.Fatal(err)
	}
	fabricCalls, keyDeletes, refunds, receipts := len(fixture.fabric.recordedCalls()), sub2API.keyDeletes, len(sub2API.refunds), len(ledger.receipts)
	for attempt := 0; attempt < 2; attempt++ {
		response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-recreated-"+strconv.Itoa(attempt))
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), errWorkspaceDeleteTerminalConflict.Error()) {
			t.Fatalf("recreated Workspace attempt=%d status=%d body=%s", attempt, response.Code, response.Body.String())
		}
	}
	if len(fixture.fabric.recordedCalls()) != fabricCalls || sub2API.keyDeletes != keyDeletes || len(sub2API.refunds) != refunds || len(ledger.receipts) != receipts {
		t.Fatalf("terminal conflict repeated mutation Fabric=%d/%d key=%d/%d refunds=%d/%d receipts=%d/%d", len(fixture.fabric.recordedCalls()), fabricCalls, sub2API.keyDeletes, keyDeletes, len(sub2API.refunds), refunds, len(ledger.receipts), receipts)
	}
	afterRow, found, err := fixture.store.GetRuntimeOperation(context.Background(), operationID)
	operation, decodeErr := decodeWorkspaceDeleteOperation(afterRow)
	if err != nil || !found || decodeErr != nil || operation.Phase != "complete" || operation.Status != "succeeded" || stringValue(afterRow["result"]) != terminalResult {
		t.Fatalf("terminal operation changed operation=%#v found=%v err=%v decode=%v", operation, found, err, decodeErr)
	}
	audits, err := fixture.store.ListAuditEvents(context.Background(), "acct-alpha")
	if err != nil {
		t.Fatal(err)
	}
	auditID := "audit-" + stableID("workspace.delete.terminal_conflict", operationID, errWorkspaceDeleteTerminalConflict.Error())[:12]
	matching := make([]map[string]any, 0, 1)
	for _, audit := range audits {
		if stringValue(audit["id"]) == auditID {
			matching = append(matching, audit)
		}
	}
	if len(matching) != 1 || stringValue(matching[0]["action"]) != "workspace.delete.terminal_conflict" || stringValue(matching[0]["targetAccountId"]) != "acct-alpha" ||
		stringValue(matching[0]["resourceKind"]) != "workspace" || stringValue(matching[0]["resourceId"]) != "ws-alpha" || stringValue(matching[0]["result"]) != "conflict" ||
		stringValue(mapField(matching[0], "after")["operationId"]) != operationID || stringValue(mapField(matching[0], "after")["error"]) != errWorkspaceDeleteTerminalConflict.Error() {
		t.Fatalf("terminal conflict audits=%#v", audits)
	}
}

func TestWorkspaceDeleteCompletedOperationRejectsInvalidTerminalEvidenceWithoutMutation(t *testing.T) {
	fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixture(t)
	first := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-before-terminal-evidence-drift")
	if first.Code != http.StatusOK {
		t.Fatalf("initial delete status=%d body=%s", first.Code, first.Body.String())
	}
	operationID := workspaceDeleteOperationID("ws-alpha")
	row, found, err := fixture.store.GetRuntimeOperation(context.Background(), operationID)
	if err != nil || !found {
		t.Fatalf("terminal operation found=%v err=%v", found, err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(stringValue(row["result"])), &result); err != nil {
		t.Fatal(err)
	}
	delete(result, "deletionReceiptId")
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	row["result"] = string(encoded)
	if err := fixture.store.SaveRuntimeOperation(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	terminalResult := string(encoded)
	fabricCalls, keyDeletes, refunds, receipts := len(fixture.fabric.recordedCalls()), sub2API.keyDeletes, len(sub2API.refunds), len(ledger.receipts)
	response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-terminal-evidence-drift")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), errWorkspaceDeleteTerminalConflict.Error()) ||
		len(fixture.fabric.recordedCalls()) != fabricCalls || sub2API.keyDeletes != keyDeletes || len(sub2API.refunds) != refunds || len(ledger.receipts) != receipts {
		t.Fatalf("terminal evidence conflict status=%d body=%s Fabric=%d/%d key=%d/%d refunds=%d/%d receipts=%d/%d", response.Code, response.Body.String(), len(fixture.fabric.recordedCalls()), fabricCalls, sub2API.keyDeletes, keyDeletes, len(sub2API.refunds), refunds, len(ledger.receipts), receipts)
	}
	afterRow, found, err := fixture.store.GetRuntimeOperation(context.Background(), operationID)
	if err != nil || !found || stringValue(afterRow["result"]) != terminalResult || stringValue(afterRow["status"]) != "succeeded" {
		t.Fatalf("terminal evidence conflict changed operation=%#v found=%v err=%v", afterRow, found, err)
	}
	audits, err := fixture.store.ListAuditEvents(context.Background(), "acct-alpha")
	auditID := "audit-" + stableID("workspace.delete.terminal_conflict", operationID, errWorkspaceDeleteTerminalConflict.Error())[:12]
	if err != nil || !slices.ContainsFunc(audits, func(audit map[string]any) bool { return stringValue(audit["id"]) == auditID }) {
		t.Fatalf("terminal evidence conflict audit=%#v err=%v", audits, err)
	}
}

func TestWorkspaceDeleteTerminalConflictAuditFailureFailsClosed(t *testing.T) {
	store := &workspaceDeleteAuditFailStore{controlPlaneTableStore: newMemoryTableStore()}
	fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixtureWith(t, store, &workspaceDeleteFabric{})
	first := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-before-audit-failure")
	if first.Code != http.StatusOK {
		t.Fatalf("initial delete status=%d body=%s", first.Code, first.Body.String())
	}
	operationID := workspaceDeleteOperationID("ws-alpha")
	terminalRow, found, err := fixture.store.GetRuntimeOperation(context.Background(), operationID)
	if err != nil || !found {
		t.Fatalf("terminal operation found=%v err=%v", found, err)
	}
	if err := fixture.store.SaveWorkspace(context.Background(), cloneMap(fixture.workspace)); err != nil {
		t.Fatal(err)
	}
	store.failAudit = true
	fabricCalls, keyDeletes, refunds, receipts := len(fixture.fabric.recordedCalls()), sub2API.keyDeletes, len(sub2API.refunds), len(ledger.receipts)
	response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-audit-failure")
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "state_persist_failed") ||
		len(fixture.fabric.recordedCalls()) != fabricCalls || sub2API.keyDeletes != keyDeletes || len(sub2API.refunds) != refunds || len(ledger.receipts) != receipts {
		t.Fatalf("audit failure status=%d body=%s Fabric=%d/%d key=%d/%d refunds=%d/%d receipts=%d/%d", response.Code, response.Body.String(), len(fixture.fabric.recordedCalls()), fabricCalls, sub2API.keyDeletes, keyDeletes, len(sub2API.refunds), refunds, len(ledger.receipts), receipts)
	}
	afterRow, found, err := fixture.store.GetRuntimeOperation(context.Background(), operationID)
	if err != nil || !found || stringValue(afterRow["result"]) != stringValue(terminalRow["result"]) || stringValue(afterRow["status"]) != "succeeded" {
		t.Fatalf("audit failure changed terminal operation=%#v found=%v err=%v", afterRow, found, err)
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
		decoded.AttachmentStatus != "absent" || decoded.StorageStatus != "absent" || decoded.ComputeStatus != "absent" || decoded.DeletionReceiptID != "receipt-delete-alpha" {
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
	if err != nil || !found || decodeErr != nil || operation.Status != "manual_review" || operation.Phase != "attachment_absent" {
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
	if len(calls) != 10 || calls[6] != calls[7] || !strings.HasPrefix(calls[6], "storage:") ||
		!strings.HasPrefix(calls[8], "compute:") || !strings.HasPrefix(calls[9], "compute-read:") {
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

func TestWorkspaceDeleteDoesNotRecordReceiptWhileWorkspaceStillExists(t *testing.T) {
	fixture, _, ledger := newWorkspaceDeleteCompletionFixture(t)
	operation := workspaceDeleteStoreOperationForWorkspace(fixture.workspace, time.Now().UTC().Format(time.RFC3339Nano))
	operation.Phase = "workspace_absent"
	operation.RuntimeStatus, operation.SecretStatus, operation.AttachmentStatus = "absent", "absent", "absent"
	operation.StorageStatus, operation.ComputeStatus, operation.KeyStatus = "absent", "absent", "absent"
	operation.ComputeReadbacks, operation.MaxComputeReadbacks = 1, workspaceDeleteComputeReadbackBudget
	if err := fixture.store.SaveRuntimeOperation(context.Background(), workspaceDeleteOperationRow(operation)); err != nil {
		t.Fatal(err)
	}
	response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, "delete-workspace-presence-drift")
	if response.Code != http.StatusBadGateway || len(ledger.receipts) != 0 {
		t.Fatalf("presence drift status=%d body=%s receipts=%#v", response.Code, response.Body.String(), ledger.receipts)
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

func TestWorkspaceDeleteRenewalMutualExclusionMemoryAndSQLite(t *testing.T) {
	for _, storeCase := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "sqlite", new: func(t *testing.T) controlPlaneTableStore {
			return NewTestEntStateStore(t, t.TempDir()+"/workspace-delete-renewal.sqlite")
		}},
	} {
		t.Run(storeCase.name, func(t *testing.T) {
			exerciseWorkspaceDeleteRenewalMutualExclusion(t, storeCase.new(t))
		})
	}
}

func TestWorkspaceDeleteRotationMutualExclusionMemoryAndSQLite(t *testing.T) {
	for _, storeCase := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "sqlite", new: func(t *testing.T) controlPlaneTableStore {
			return NewTestEntStateStore(t, t.TempDir()+"/workspace-delete-rotation.sqlite")
		}},
	} {
		t.Run(storeCase.name, func(t *testing.T) {
			exerciseWorkspaceDeleteRotationMutualExclusion(t, storeCase.new(t))
		})
	}
}

func TestPostgresWorkspaceDeleteStoreLifecycle(t *testing.T) {
	exerciseWorkspaceDeleteStoreLifecycle(t, newPostgresWorkspaceRenewalStore(t))
}

func TestPostgresWorkspaceDeleteRenewalMutualExclusion(t *testing.T) {
	exerciseWorkspaceDeleteRenewalMutualExclusion(t, newPostgresWorkspaceRenewalStore(t))
}

func TestPostgresWorkspaceDeleteRotationMutualExclusion(t *testing.T) {
	exerciseWorkspaceDeleteRotationMutualExclusion(t, newPostgresWorkspaceRenewalStore(t))
}

func TestPostgresWorkspaceDeleteRotationConcurrentClaim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store := newPostgresWorkspaceRenewalStore(t)
	workspace := currentWorkspaceRenewalAPIRow()
	workspaceID := "workspace-delete-rotation-concurrent-claim"
	workspace["id"], workspace["accountId"], workspace["ownerAccountId"], workspace["ownerUserId"] = workspaceID, "acct-delete", "acct-delete", "usr-delete"
	workspace["workspaceApiKeyId"] = int64(19)
	if err := store.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	deleteOperation := workspaceDeleteStoreOperationForWorkspace(workspace, time.Now().UTC().Format(time.RFC3339Nano))
	rotation := workspaceKeyRotationOperation{
		RequestHash: "rotation-concurrent-request", Phase: "replacement_check", OldKeyID: 19,
		ReplacementName: "opl-workspace-replacement-concurrent", RetiredName: "opl-workspace-retired-concurrent",
		AuditEvent: workspaceDeleteRotationAudit("rotation-concurrent", "acct-delete", workspaceID),
	}
	rotationRow := workspaceKeyRotationRow("rotation-concurrent", "acct-delete", workspaceID, "started", rotation)
	type claimResult struct {
		kind string
		err  error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		results <- claimResult{kind: "delete", err: store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{Create: true, DesiredOperation: workspaceDeleteOperationRow(deleteOperation)})}
	}()
	go func() {
		defer wg.Done()
		<-start
		results <- claimResult{kind: "rotation", err: store.ClaimWorkspaceKeyRotation(ctx, rotationRow)}
	}()
	close(start)
	wg.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for result := range results {
		if result.err == nil {
			succeeded++
		} else if errors.Is(result.err, errWorkspaceDeleteCASConflict) || errors.Is(result.err, errWorkspaceKeyRotationInProgress) {
			conflicted++
		} else {
			t.Fatalf("unexpected %s claim error: %v", result.kind, result.err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent Delete/Rotation claims succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

func TestPostgresWorkspaceDeleteRenewalConcurrentClaim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	var blocker *sql.Tx
	var wg sync.WaitGroup
	defer func() {
		cancel()
		if blocker != nil {
			_ = blocker.Rollback()
		}
		wg.Wait()
	}()
	store, db := newPostgresWorkspaceRenewalStoreWithDB(t)
	workspace := currentWorkspaceRenewalAPIRow()
	workspaceID := "workspace-delete-renewal-concurrent-claim"
	workspace["id"], workspace["accountId"], workspace["ownerAccountId"], workspace["ownerUserId"] = workspaceID, "acct-delete", "acct-delete", "usr-delete"
	workspace["autoRenew"], workspace["authorizedBy"], workspace["authorizedAt"] = true, "usr-delete", time.Now().UTC().Format(time.RFC3339Nano)
	if err := store.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	operations, err := store.ListRuntimeOperations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	renewal, err := newWorkspaceRenewalOperation(workspace, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	renewalClaim := workspaceRenewalClaimCAS{
		WorkspaceID: workspaceID, AccountID: "acct-delete", ExpectedPaidThrough: stringValue(workspace["paidThrough"]), ExpectedAutoRenew: true,
		ExpectedOperationsVersion: runtimeOperationsVersion(operations, workspaceID), DesiredOperation: workspaceRenewalOperationRow(renewal),
	}
	deleteOperation := workspaceDeleteStoreOperationForWorkspace(workspace, time.Now().UTC().Format(time.RFC3339Nano))
	blocker, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var blockerPID int
	if err := blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	var lockedID string
	if err := blocker.QueryRowContext(ctx, `SELECT id FROM control_plane_workspaces WHERE id = $1 FOR UPDATE`, workspaceID).Scan(&lockedID); err != nil {
		t.Fatal(err)
	}
	if lockedID != workspaceID {
		t.Fatalf("blocker locked Workspace=%q", lockedID)
	}
	type claimResult struct {
		kind string
		err  error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		results <- claimResult{kind: "delete", err: store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{Create: true, DesiredOperation: workspaceDeleteOperationRow(deleteOperation)})}
	}()
	go func() {
		defer wg.Done()
		<-start
		results <- claimResult{kind: "renewal", err: store.ClaimWorkspaceRenewal(ctx, renewalClaim)}
	}()
	close(start)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiters, lockWaiters int
		if err := db.QueryRowContext(ctx, `
			WITH RECURSIVE blocker_waiters(pid, wait_event_type) AS (
				SELECT activity.pid, activity.wait_event_type
				FROM pg_stat_activity AS activity
				WHERE $1 = ANY(pg_blocking_pids(activity.pid))
				UNION
				SELECT activity.pid, activity.wait_event_type
				FROM pg_stat_activity AS activity
				JOIN blocker_waiters AS parent
				  ON parent.pid = ANY(pg_blocking_pids(activity.pid))
			)
			SELECT count(*), count(*) FILTER (WHERE wait_event_type = 'Lock')
			FROM blocker_waiters
		`, blockerPID).Scan(&waiters, &lockWaiters); err != nil {
			t.Fatal(err)
		}
		if waiters == 2 && lockWaiters == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("claims did not both enter blocker PID %d lock wait chain: waiters=%d lockWaiters=%d err=%v", blockerPID, waiters, lockWaiters, ctx.Err())
		case <-ticker.C:
		}
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	claimErrors := make(map[string]error, 2)
	for range 2 {
		select {
		case result := <-results:
			claimErrors[result.kind] = result.err
		case <-ctx.Done():
			t.Fatalf("concurrent claims did not converge: %v", ctx.Err())
		}
	}
	deleteErr, renewalErr := claimErrors["delete"], claimErrors["renewal"]
	if (deleteErr == nil) == (renewalErr == nil) {
		t.Fatalf("concurrent claims delete=%v renewal=%v", deleteErr, renewalErr)
	}
	if deleteErr == nil {
		if !errors.Is(renewalErr, errWorkspaceRenewalCASConflict) {
			t.Fatalf("Delete winner returned Renewal error=%v", renewalErr)
		}
	} else if !errors.Is(deleteErr, errWorkspaceDeleteCASConflict) || renewalErr != nil {
		t.Fatalf("Renewal winner returned Delete error=%v renewal=%v", deleteErr, renewalErr)
	}
	operations, err = store.ListRuntimeOperations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lifecycleOperations := 0
	for _, operation := range operations {
		if stringValue(operation["workspaceId"]) == workspaceID && (stringValue(operation["action"]) == workspaceDeleteAction || stringValue(operation["action"]) == "workspace.renewal") {
			lifecycleOperations++
		}
	}
	if lifecycleOperations != 1 {
		t.Fatalf("concurrent lifecycle operations=%d rows=%#v", lifecycleOperations, operations)
	}
}

func exerciseWorkspaceDeleteRenewalMutualExclusion(t *testing.T, store controlPlaneTableStore) {
	t.Helper()
	ctx := context.Background()
	for _, renewalStatus := range []string{"claimed", "insufficient", "debit_pending", "debited", "provider_renewing", "verifying", "refund_pending", "manual_review"} {
		workspace := currentWorkspaceRenewalAPIRow()
		workspaceID := "workspace-delete-blocked-by-renewal-" + renewalStatus
		workspace["id"], workspace["accountId"], workspace["ownerAccountId"], workspace["ownerUserId"] = workspaceID, "acct-delete", "acct-delete", "usr-delete"
		workspace["autoRenew"], workspace["authorizedBy"], workspace["authorizedAt"] = true, "usr-delete", time.Now().UTC().Format(time.RFC3339Nano)
		if err := store.SaveWorkspace(ctx, workspace); err != nil {
			t.Fatal(err)
		}
		renewal, err := newWorkspaceRenewalOperation(workspace, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		renewal.Status = renewalStatus
		if err := store.SaveRuntimeOperation(ctx, workspaceRenewalOperationRow(renewal)); err != nil {
			t.Fatal(err)
		}
		operation := workspaceDeleteStoreOperationForWorkspace(workspace, time.Now().UTC().Format(time.RFC3339Nano))
		if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{Create: true, DesiredOperation: workspaceDeleteOperationRow(operation)}); !errors.Is(err, errWorkspaceDeleteCASConflict) {
			t.Fatalf("renewal %s did not block Delete claim: %v", renewalStatus, err)
		}
	}
	for _, renewalStatus := range []string{"active", "cancelled", "refunded", "expired_unpaid"} {
		workspace := currentWorkspaceRenewalAPIRow()
		workspaceID := "workspace-delete-after-terminal-renewal-" + renewalStatus
		workspace["id"], workspace["accountId"], workspace["ownerAccountId"], workspace["ownerUserId"] = workspaceID, "acct-delete", "acct-delete", "usr-delete"
		workspace["autoRenew"], workspace["authorizedBy"], workspace["authorizedAt"] = true, "usr-delete", time.Now().UTC().Format(time.RFC3339Nano)
		if err := store.SaveWorkspace(ctx, workspace); err != nil {
			t.Fatal(err)
		}
		renewal, err := newWorkspaceRenewalOperation(workspace, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		renewal.Status = renewalStatus
		if renewalStatus == "refunded" || renewalStatus == "expired_unpaid" {
			renewal.Phase = "complete"
		}
		if err := store.SaveRuntimeOperation(ctx, workspaceRenewalOperationRow(renewal)); err != nil {
			t.Fatal(err)
		}
		operation := workspaceDeleteStoreOperationForWorkspace(workspace, time.Now().UTC().Format(time.RFC3339Nano))
		if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{Create: true, DesiredOperation: workspaceDeleteOperationRow(operation)}); err != nil {
			t.Fatalf("terminal renewal %s blocked Delete claim: %v", renewalStatus, err)
		}
	}
	invalidWorkspace := currentWorkspaceRenewalAPIRow()
	invalidWorkspaceID := "workspace-delete-blocked-by-invalid-renewal"
	invalidWorkspace["id"], invalidWorkspace["accountId"], invalidWorkspace["ownerAccountId"], invalidWorkspace["ownerUserId"] = invalidWorkspaceID, "acct-delete", "acct-delete", "usr-delete"
	invalidWorkspace["autoRenew"], invalidWorkspace["authorizedBy"], invalidWorkspace["authorizedAt"] = true, "usr-delete", time.Now().UTC().Format(time.RFC3339Nano)
	if err := store.SaveWorkspace(ctx, invalidWorkspace); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRuntimeOperation(ctx, map[string]any{
		"id": "invalid-renewal-blocking-delete", "operationId": "invalid-renewal-blocking-delete", "accountId": "acct-delete", "workspaceId": invalidWorkspaceID,
		"resourceId": invalidWorkspaceID, "resourceKind": "workspace_renewal", "action": "workspace.renewal", "status": "active", "result": `{}`,
		"createdAt": time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
	invalidDelete := workspaceDeleteStoreOperationForWorkspace(invalidWorkspace, time.Now().UTC().Format(time.RFC3339Nano))
	if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{Create: true, DesiredOperation: workspaceDeleteOperationRow(invalidDelete)}); !errors.Is(err, errWorkspaceDeleteCASConflict) {
		t.Fatalf("invalid Renewal did not fail closed before Delete claim: %v", err)
	}

	workspace := currentWorkspaceRenewalAPIRow()
	workspaceID := "workspace-renewal-blocked-by-delete"
	workspace["id"], workspace["accountId"], workspace["ownerAccountId"], workspace["ownerUserId"] = workspaceID, "acct-delete", "acct-delete", "usr-delete"
	workspace["autoRenew"], workspace["authorizedBy"], workspace["authorizedAt"] = true, "usr-delete", time.Now().UTC().Format(time.RFC3339Nano)
	if err := store.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	operation := workspaceDeleteStoreOperationForWorkspace(workspace, time.Now().UTC().Format(time.RFC3339Nano))
	if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{Create: true, DesiredOperation: workspaceDeleteOperationRow(operation)}); err != nil {
		t.Fatal(err)
	}
	operations, err := store.ListRuntimeOperations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	renewalID := "renewal-blocked-by-delete"
	claim := workspaceRenewalClaimCAS{
		WorkspaceID: workspaceID, AccountID: "acct-delete", ExpectedPaidThrough: stringValue(workspace["paidThrough"]), ExpectedAutoRenew: true,
		ExpectedOperationsVersion: runtimeOperationsVersion(operations, workspaceID),
		DesiredOperation: map[string]any{
			"id": renewalID, "operationId": renewalID, "accountId": "acct-delete", "workspaceId": workspaceID, "resourceId": workspaceID,
			"resourceKind": "workspace_renewal", "action": "workspace.renewal", "status": "claimed", "result": `{}`, "createdAt": time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
	if err := store.ClaimWorkspaceRenewal(ctx, claim); !errors.Is(err, errWorkspaceRenewalCASConflict) {
		t.Fatalf("active Delete did not block Renewal claim: %v", err)
	}
}

func exerciseWorkspaceDeleteRotationMutualExclusion(t *testing.T, store controlPlaneTableStore) {
	t.Helper()
	ctx := context.Background()
	for _, phase := range []string{"replacement_check", "replacement_create", "secret_write", "runtime_bind", "runtime_readback", "workspace_commit", "retire_old", "promote_new", "delete_old", "receipt"} {
		workspace := currentWorkspaceRenewalAPIRow()
		workspaceID := "workspace-delete-blocked-by-rotation-" + phase
		workspace["id"], workspace["accountId"], workspace["ownerAccountId"], workspace["ownerUserId"] = workspaceID, "acct-delete", "acct-delete", "usr-delete"
		workspace["workspaceApiKeyId"] = int64(19)
		if err := store.SaveWorkspace(ctx, workspace); err != nil {
			t.Fatal(err)
		}
		rotation := workspaceKeyRotationOperation{
			RequestHash: "rotation-request-" + phase, Phase: "replacement_check", OldKeyID: 19,
			ReplacementName: "opl-workspace-replacement-" + phase, RetiredName: "opl-workspace-retired-" + phase,
			AuditEvent: workspaceDeleteRotationAudit("rotation-"+phase, "acct-delete", workspaceID),
		}
		row := workspaceKeyRotationRow("rotation-"+phase, "acct-delete", workspaceID, "started", rotation)
		if err := store.ClaimWorkspaceKeyRotation(ctx, row); err != nil {
			t.Fatal(err)
		}
		rotation.Phase = phase
		if err := store.SaveRuntimeOperation(ctx, workspaceKeyRotationRow("rotation-"+phase, "acct-delete", workspaceID, "started", rotation)); err != nil {
			t.Fatal(err)
		}
		operation := workspaceDeleteStoreOperationForWorkspace(workspace, time.Now().UTC().Format(time.RFC3339Nano))
		if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{Create: true, DesiredOperation: workspaceDeleteOperationRow(operation)}); !errors.Is(err, errWorkspaceDeleteCASConflict) {
			t.Fatalf("Rotation phase %s did not block Delete claim: %v", phase, err)
		}
	}

	workspace := currentWorkspaceRenewalAPIRow()
	workspaceID := "workspace-rotation-blocked-by-delete"
	workspace["id"], workspace["accountId"], workspace["ownerAccountId"], workspace["ownerUserId"] = workspaceID, "acct-delete", "acct-delete", "usr-delete"
	workspace["workspaceApiKeyId"] = int64(19)
	if err := store.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	operation := workspaceDeleteStoreOperationForWorkspace(workspace, time.Now().UTC().Format(time.RFC3339Nano))
	if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{Create: true, DesiredOperation: workspaceDeleteOperationRow(operation)}); err != nil {
		t.Fatal(err)
	}
	rotation := workspaceKeyRotationOperation{
		RequestHash: "rotation-blocked-request", Phase: "replacement_check", OldKeyID: 19,
		ReplacementName: "opl-workspace-replacement-blocked", RetiredName: "opl-workspace-retired-blocked",
		AuditEvent: workspaceDeleteRotationAudit("rotation-blocked-by-delete", "acct-delete", workspaceID),
	}
	if err := store.ClaimWorkspaceKeyRotation(ctx, workspaceKeyRotationRow("rotation-blocked-by-delete", "acct-delete", workspaceID, "started", rotation)); !errors.Is(err, errWorkspaceKeyRotationInProgress) {
		t.Fatalf("active Delete did not block Rotation claim: %v", err)
	}
}

func workspaceDeleteRotationAudit(operationID, accountID, workspaceID string) map[string]any {
	return map[string]any{
		"id": "audit-" + stableID(operationID, "workspace.gateway_key.rotate")[:12], "action": "workspace.gateway_key.rotate",
		"resourceKind": "workspace_gateway_key", "resourceId": workspaceID, "actorAccountId": accountID, "targetAccountId": accountID,
		"actorUserId": "usr-delete", "createdAt": "2026-08-16T00:00:00Z",
	}
}

func TestWorkspaceRenewalWorkerSkipsActiveDeleteV2(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	operation := workspaceDeleteStoreOperationForWorkspace(fixture.workspace, time.Now().UTC().Format(time.RFC3339Nano))
	if err := fixture.app.tables.ApplyWorkspaceDelete(context.Background(), workspaceDeleteStoreMutation{Create: true, DesiredOperation: workspaceDeleteOperationRow(operation)}); err != nil {
		t.Fatal(err)
	}
	beforeEvents := len(*fixture.events)
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(-monthlyRenewalLead)); err != nil {
		t.Fatal(err)
	}
	operations, err := fixture.app.tables.ListRuntimeOperations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range operations {
		if stringValue(row["workspaceId"]) == stringValue(fixture.workspace["id"]) && stringValue(row["action"]) == "workspace.renewal" {
			t.Fatalf("worker claimed Renewal during active Delete: %#v", row)
		}
	}
	if len(*fixture.events) != beforeEvents || len(fixture.sub2API.charges) != 0 || len(fixture.fabric.computeRenewKeys) != 0 || len(fixture.fabric.storageRenewKeys) != 0 {
		t.Fatalf("worker mutated during active Delete events=%#v charges=%#v compute=%#v storage=%#v", *fixture.events, fixture.sub2API.charges, fixture.fabric.computeRenewKeys, fixture.fabric.storageRenewKeys)
	}
}

func TestPostgresWorkspaceDeleteComputePendingSurvivesRestart(t *testing.T) {
	admin := openControlPlaneTestPostgres(t)
	schema := "control_plane_workspace_delete_pending_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	databaseURL := controlPlaneTestPostgresURL(t, "postgres", schema)
	firstState, err := newTestPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	first := firstState.(*postgresEntStateStore)
	fabric := &workspaceDeleteFabric{computeReads: []string{"destroying"}}
	fixture, sub2API, ledger := newWorkspaceDeleteCompletionFixtureWith(t, first, fabric)
	const mutationKey = "delete-compute-postgres-restart"
	response := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, mutationKey)
	if response.Code != http.StatusAccepted {
		t.Fatalf("pending delete status=%d body=%s", response.Code, response.Body.String())
	}
	if err := first.client.Close(); err != nil {
		t.Fatal(err)
	}

	restartedState, err := newTestPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	restarted := restartedState.(*postgresEntStateStore)
	t.Cleanup(func() { _ = restarted.client.Close() })
	row, found, err := restarted.GetRuntimeOperation(context.Background(), workspaceDeleteOperationID("ws-alpha"))
	operation, decodeErr := decodeWorkspaceDeleteOperation(row)
	if err != nil || !found || decodeErr != nil || operation.Phase != "storage_absent" || operation.Status != "running" ||
		operation.ComputeStatus != "destroying" || operation.ComputeReadbacks != 1 || operation.MaxComputeReadbacks != workspaceDeleteComputeReadbackBudget {
		t.Fatalf("restarted pending operation=%#v found=%v err=%v decode=%v", operation, found, err, decodeErr)
	}
	fabric.mu.Lock()
	fabric.computeReads = []string{"destroyed"}
	fabric.mu.Unlock()
	expireWorkspaceDeleteComputeReadback(t, restarted)
	service := controlplane.NewService(ledger, fabric, sub2API)
	restartedServer, err := NewPersistentServer(service, restarted)
	if err != nil {
		t.Fatal(err)
	}
	restartedSession := tenantOwnerSessionForTest(t, restartedServer)
	terminal := requestWithMutationKeyForTest(t, restartedServer, restartedSession, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, mutationKey)
	if terminal.Code != http.StatusOK {
		t.Fatalf("restarted delete status=%d body=%s", terminal.Code, terminal.Body.String())
	}
	computeMutations, computeReads := 0, 0
	for _, call := range fabric.recordedCalls() {
		if strings.HasPrefix(call, "compute:") {
			computeMutations++
		}
		if strings.HasPrefix(call, "compute-read:") {
			computeReads++
		}
	}
	if computeMutations != 1 || computeReads != 2 {
		t.Fatalf("restarted compute mutations=%d reads=%d calls=%#v", computeMutations, computeReads, fabric.recordedCalls())
	}
}

func expireWorkspaceDeleteComputeReadback(t *testing.T, store controlPlaneTableStore) {
	t.Helper()
	row, found, err := store.GetRuntimeOperation(context.Background(), workspaceDeleteOperationID("ws-alpha"))
	if err != nil || !found {
		t.Fatalf("pending operation found=%v err=%v", found, err)
	}
	operation, err := decodeWorkspaceDeleteOperation(row)
	if err != nil {
		t.Fatalf("pending operation decode: %v", err)
	}
	currentResult := stringValue(row["result"])
	operation.ComputeReadbackNotBefore = time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
	if err := store.ApplyWorkspaceDelete(context.Background(), workspaceDeleteStoreMutation{
		ExpectedResult: currentResult, DesiredOperation: workspaceDeleteOperationRow(operation),
	}); err != nil {
		t.Fatalf("expire compute readback schedule: %v", err)
	}
}

func exerciseWorkspaceDeleteStoreLifecycle(t *testing.T, store controlPlaneTableStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	workspace := map[string]any{
		"id": "ws-delete-store", "accountId": "acct-delete", "ownerAccountId": "acct-delete", "ownerUserId": "usr-delete",
		"currentComputeAllocationId": "compute-delete", "storageId": "storage-delete", "currentAttachmentId": "attachment-delete",
		"runtimeId": "runtime-delete", "workspaceApiKeyId": int64(19),
	}
	if err := store.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCompute(ctx, map[string]any{
		"id": "compute-delete", "accountId": "acct-delete", "ownerUserId": "usr-delete", "workspaceId": "ws-delete-store", "status": "destroyed",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStorage(ctx, map[string]any{
		"id": "storage-delete", "accountId": "acct-delete", "ownerUserId": "usr-delete", "workspaceId": "ws-delete-store", "status": "destroyed",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAttachment(ctx, map[string]any{
		"id": "attachment-delete", "accountId": "acct-delete", "workspaceId": "ws-delete-store", "computeAllocationId": "compute-delete", "storageId": "storage-delete", "volumeId": "storage-delete", "status": "detached",
	}); err != nil {
		t.Fatal(err)
	}
	claimed := workspaceDeleteStoreOperation(now)
	if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{Create: true, DesiredOperation: workspaceDeleteOperationRow(claimed)}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{Create: true, DesiredOperation: workspaceDeleteOperationRow(claimed)}); !errors.Is(err, errWorkspaceDeleteCASConflict) {
		t.Fatalf("duplicate claim err=%v", err)
	}
	invalid := claimed
	invalid.Phase = "workspace_absent"
	expected := stringValue(workspaceDeleteOperationRow(claimed)["result"])
	if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{DeleteWorkspace: true, ExpectedResult: expected, DesiredOperation: workspaceDeleteOperationRow(invalid)}); !errors.Is(err, errWorkspaceDeleteCASConflict) {
		t.Fatalf("phase jump without cumulative evidence err=%v", err)
	}
	advance := func(current, next workspaceDeleteOperation, deleteWorkspace, requireAbsent bool) workspaceDeleteOperation {
		t.Helper()
		if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{
			DeleteWorkspace: deleteWorkspace, RequireWorkspaceAbsent: requireAbsent,
			ExpectedResult: stringValue(workspaceDeleteOperationRow(current)["result"]), DesiredOperation: workspaceDeleteOperationRow(next),
		}); err != nil {
			t.Fatal(err)
		}
		return next
	}
	runtimeAbsent := claimed
	runtimeAbsent.Phase, runtimeAbsent.RuntimeStatus, runtimeAbsent.SecretStatus = "runtime_secret_absent", "absent", "absent"
	current := advance(claimed, runtimeAbsent, false, false)
	attachmentAbsent := current
	attachmentAbsent.Phase, attachmentAbsent.AttachmentStatus = "attachment_absent", "absent"
	current = advance(current, attachmentAbsent, false, false)
	storageAbsent := current
	storageAbsent.Phase, storageAbsent.StorageStatus = "storage_absent", "absent"
	current = advance(current, storageAbsent, false, false)
	impossibleComputeSchedule := current
	impossibleComputeSchedule.ComputeStatus = "destroying"
	if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{
		ExpectedResult: stringValue(workspaceDeleteOperationRow(current)["result"]), DesiredOperation: workspaceDeleteOperationRow(impossibleComputeSchedule),
	}); !errors.Is(err, errWorkspaceDeleteCASConflict) {
		t.Fatalf("destroying compute without readback schedule err=%v", err)
	}
	computeAbsent := current
	computeAbsent.Phase, computeAbsent.ComputeStatus = "compute_absent", "absent"
	computeAbsent.ComputeReadbacks, computeAbsent.MaxComputeReadbacks = 1, workspaceDeleteComputeReadbackBudget
	current = advance(current, computeAbsent, false, false)
	keyAbsent := current
	keyAbsent.Phase, keyAbsent.KeyStatus = "key_absent", "absent"
	current = advance(current, keyAbsent, false, false)
	if err := store.SaveCompute(ctx, map[string]any{
		"id": "compute-delete", "accountId": "acct-delete", "ownerUserId": "usr-delete", "workspaceId": "ws-other", "status": "destroyed",
	}); err != nil {
		t.Fatal(err)
	}
	deleted := current
	deleted.Phase = "workspace_absent"
	if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{
		DeleteWorkspace: true, ExpectedResult: stringValue(workspaceDeleteOperationRow(current)["result"]), DesiredOperation: workspaceDeleteOperationRow(deleted),
	}); !errors.Is(err, errWorkspaceDeleteCASConflict) {
		t.Fatalf("mismatched resource projection did not block atomic delete: %v", err)
	}
	if _, found, err := store.GetWorkspace(ctx, claimed.WorkspaceID); err != nil || !found {
		t.Fatalf("failed projection delete removed Workspace found=%v err=%v", found, err)
	}
	if err := store.SaveCompute(ctx, map[string]any{
		"id": "compute-delete", "accountId": "acct-delete", "ownerUserId": "usr-delete", "workspaceId": "ws-delete-store", "status": "destroyed",
	}); err != nil {
		t.Fatal(err)
	}
	current = advance(current, deleted, true, false)
	if _, found, err := store.GetWorkspace(ctx, claimed.WorkspaceID); err != nil || found {
		t.Fatalf("Workspace after atomic delete found=%v err=%v", found, err)
	}
	if _, found, err := store.GetCompute(ctx, claimed.ComputeID); err != nil || found {
		t.Fatalf("Compute projection after atomic delete found=%v err=%v", found, err)
	}
	if _, found, err := store.GetStorage(ctx, claimed.StorageID); err != nil || found {
		t.Fatalf("Storage projection after atomic delete found=%v err=%v", found, err)
	}
	if _, found, err := store.GetAttachment(ctx, claimed.AttachmentID); err != nil || found {
		t.Fatalf("Attachment projection after atomic delete found=%v err=%v", found, err)
	}
	receiptRecorded := current
	receiptRecorded.Phase, receiptRecorded.DeletionReceiptID = "deletion_receipt_recorded", "receipt-delete-store"
	current = advance(current, receiptRecorded, false, true)
	complete := current
	complete.Phase, complete.Status = "complete", "succeeded"
	advance(current, complete, false, true)
	row, found, err := store.GetRuntimeOperation(ctx, complete.OperationID)
	if err != nil || !found || stringValue(row["status"]) != "succeeded" {
		t.Fatalf("terminal operation=%#v found=%v err=%v", row, found, err)
	}
	if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{RequireWorkspaceAbsent: true, ExpectedResult: "stale", DesiredOperation: workspaceDeleteOperationRow(complete)}); !errors.Is(err, errWorkspaceDeleteCASConflict) {
		t.Fatalf("stale terminal update err=%v", err)
	}
	if err := store.ApplyWorkspaceDelete(ctx, workspaceDeleteStoreMutation{
		RequireWorkspaceAbsent: true, ExpectedResult: stringValue(workspaceDeleteOperationRow(complete)["result"]), DesiredOperation: workspaceDeleteOperationRow(complete),
	}); !errors.Is(err, errWorkspaceDeleteCASConflict) {
		t.Fatalf("terminal operation accepted a write: %v", err)
	}
}

func workspaceDeleteStoreOperation(now string) workspaceDeleteOperation {
	return workspaceDeleteStoreOperationForWorkspace(map[string]any{"id": "ws-delete-store", "accountId": "acct-delete", "ownerUserId": "usr-delete"}, now)
}

func workspaceDeleteStoreOperationForWorkspace(workspace map[string]any, now string) workspaceDeleteOperation {
	workspaceID := stringValue(workspace["id"])
	workspaceAPIKeyID := int64(numberField(workspace, "workspaceApiKeyId", 19))
	operation := workspaceDeleteOperation{
		SchemaVersion: 2, OperationID: workspaceDeleteOperationID(workspaceID), AccountID: stringValue(workspace["accountId"]), OwnerUserID: stringValue(workspace["ownerUserId"]), Sub2APIUserID: 41,
		WorkspaceID: workspaceID, ResourceType: "workspace", ResourceID: workspaceID, LaunchOperationID: "workspace-launch-" + workspaceID, LaunchReceiptID: "receipt-launch-" + workspaceID,
		RuntimeID: "runtime-delete", RuntimeServiceName: "runtime-delete",
		ComputeID: "compute-delete", StorageID: "storage-delete", AttachmentID: "attachment-delete", WorkspaceAPIKeyID: workspaceAPIKeyID,
		GatewaySecretRef: "opl-gateway-ws-delete-store", GatewayFingerprint: "sha256:" + strings.Repeat("a", 64),
		Phase: "claimed", Status: "running", CreatedAt: now,
	}
	operation.RequestHash = workspaceDeleteRequestHash(operation)
	return operation
}
