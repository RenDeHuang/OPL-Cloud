package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const (
	testProviderAcceptanceAccount = "acct-verification-slot-basic-01"
	testProviderAcceptanceKey     = "provider-acceptance:verification-slot-basic-01"
	testProviderConfirmation      = "I_UNDERSTAND_THIS_BUYS_ONE_PREPAID_CVM_AND_CBS"
)

func TestProviderAcceptanceFreezesDualSlotIdentitiesAndBudget(t *testing.T) {
	if providerAcceptanceLifetimePurchaseBudget != 2 {
		t.Fatalf("lifetime purchase budget = %d, want 2", providerAcceptanceLifetimePurchaseBudget)
	}
	want := map[string]providerAcceptanceSlot{
		"verification-slot-basic-01": {
			ID: "verification-slot-basic-01", AccountID: "acct-verification-slot-basic-01",
			Key: "provider-acceptance:verification-slot-basic-01", PackageID: "basic",
			InstanceType: "SA5.MEDIUM4", StorageGB: 10,
		},
		"verification-slot-pro-01": {
			ID: "verification-slot-pro-01", AccountID: "acct-verification-slot-pro-01",
			Key: "provider-acceptance:verification-slot-pro-01", PackageID: "pro",
			InstanceType: "SA5.2XLARGE16", StorageGB: 100,
		},
	}
	if len(providerAcceptanceSlots) != len(want) {
		t.Fatalf("fixed slots = %d, want %d", len(providerAcceptanceSlots), len(want))
	}
	for id, expected := range want {
		actual, ok := providerAcceptanceSlots[id]
		if !ok {
			t.Fatalf("fixed slot %q missing", id)
		}
		if actual.ID != expected.ID || actual.AccountID != expected.AccountID || actual.Key != expected.Key ||
			actual.PackageID != expected.PackageID || actual.InstanceType != expected.InstanceType || actual.StorageGB != expected.StorageGB {
			t.Fatalf("fixed slot %q = %#v, want %#v", id, actual, expected)
		}
	}
}

type providerAcceptanceFabric struct {
	fakeFabricClient
	mu                  sync.Mutex
	compute             clients.ComputeAllocation
	storage             clients.StorageVolume
	computeCreates      int
	computeSyncs        int
	storageCreates      int
	storageSyncs        int
	attachmentCreates   int
	secretWrites        int
	runtimeCreates      int
	mutationKeys        []string
	preflightCalls      int
	failStorageCreation bool
}

func (f *providerAcceptanceFabric) MonthlyPreflight(_ context.Context, input clients.MonthlyPreflightInput) (clients.MonthlyPreflight, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preflightCalls++
	return clients.MonthlyPreflight{
		ResourceType: input.ResourceType, PackageID: input.PackageID, SizeGB: input.SizeGB, Zone: input.Zone,
		Available: true, ChargeType: "PREPAID", PeriodMonths: 1, RenewFlag: "NOTIFY_AND_MANUAL_RENEW",
		ProviderPriceCNY: 8.8, ProviderRequestIDs: map[string]string{"nodePool": "req-pool", "subnets": "req-subnets", "availability": "req-availability", "quota": "req-quota", "price": "req-price"},
	}, nil
}

func (f *providerAcceptanceFabric) CreateComputeAllocation(_ context.Context, input clients.ComputeAllocationInput, key string) (clients.ComputeAllocation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.computeCreates++
	f.mutationKeys = append(f.mutationKeys, key)
	f.compute = clients.ComputeAllocation{
		ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID,
		Status: "provisioning", Provider: "tencent-tke", ProviderRequestID: "req-compute-slot",
	}
	return f.compute, nil
}

func (f *providerAcceptanceFabric) SyncComputeAllocation(_ context.Context, id string) (clients.ComputeAllocation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.computeSyncs++
	accountID, workspaceID, packageID := f.compute.AccountID, f.compute.WorkspaceID, f.compute.PackageID
	instanceType := map[string]string{"basic": "SA5.MEDIUM4", "pro": "SA5.2XLARGE16"}[packageID]
	f.compute = clients.ComputeAllocation{
		ID: id, AccountID: accountID, WorkspaceID: workspaceID, PackageID: packageID,
		Status: "running", Provider: "tencent-tke", ProviderResourceID: "node/slot-01", ProviderRequestID: "req-compute-slot",
		NodePoolID: "np-verification-slot-01", InstanceID: "ins-verification-slot-01", CVMInstanceID: "ins-verification-slot-01", NodeName: "node-verification-slot-01",
		InstanceType: instanceType, Zone: "ap-shanghai-2", ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z",
		ProviderData: map[string]string{"instanceType": instanceType, "zone": "ap-shanghai-2"},
		CostTags:     providerAcceptanceTestTags(accountID, workspaceID, id, "op-compute-slot"),
	}
	return f.compute, nil
}

func (f *providerAcceptanceFabric) CreateStorageVolume(_ context.Context, input clients.StorageVolumeInput, key string) (clients.StorageVolume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.storageCreates++
	f.mutationKeys = append(f.mutationKeys, key)
	f.storage = clients.StorageVolume{
		ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "pending", Provider: "tencent-tke",
		ProviderResourceID: "disk-verification-slot-01", ProviderRequestID: "req-storage-slot", SizeGB: input.SizeGB,
		RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z", Zone: input.Zone,
		ProviderData: map[string]string{"chargeType": "PREPAID", "pvName": "pv-verification-slot-01", "pvcName": "pvc-verification-slot-01", "zone": input.Zone},
		CostTags:     providerAcceptanceTestTags(input.AccountID, input.WorkspaceID, input.ID, "op-storage-slot"),
	}
	if f.failStorageCreation {
		return f.storage, errors.New("provider result unknown")
	}
	return f.storage, nil
}

func (f *providerAcceptanceFabric) SyncStorageVolume(_ context.Context, id string) (clients.StorageVolume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.storageSyncs++
	f.storage.Status = "ready"
	f.storage.ID = id
	f.storage.CBSStatus = "UNATTACHED"
	return f.storage, nil
}

func (f *providerAcceptanceFabric) CreateStorageAttachment(_ context.Context, input clients.StorageAttachmentInput, key string) (clients.StorageAttachment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attachmentCreates++
	f.mutationKeys = append(f.mutationKeys, key)
	return clients.StorageAttachment{
		ID: "att-verification-slot-01", WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID,
		Status: "attached", Provider: "tencent-tke", ProviderAttachmentID: "pv/pv-verification-slot-01:pvc/pvc-verification-slot-01", ProviderRequestID: "req-attachment-slot", MountPath: "/data",
	}, nil
}

func (f *providerAcceptanceFabric) WriteGatewaySecret(_ context.Context, input clients.GatewaySecretWriteInput, key string) (clients.GatewaySecretWriteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secretWrites++
	f.mutationKeys = append(f.mutationKeys, key)
	if input.AccountID == "" || input.GatewayAPIKey != "workspace-key-secret" {
		return clients.GatewaySecretWriteResult{}, errors.New("unexpected gateway secret input")
	}
	return clients.GatewaySecretWriteResult{SecretRef: "opl-gateway-verification-slot-01", Version: "v1", Fingerprint: "sha256:slot-fingerprint"}, nil
}

func (f *providerAcceptanceFabric) CreateWorkspaceRuntime(_ context.Context, input clients.WorkspaceRuntimeInput, key string) (clients.WorkspaceRuntime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runtimeCreates++
	f.mutationKeys = append(f.mutationKeys, key)
	return clients.WorkspaceRuntime{
		ID: "rt-verification-slot-01", WorkspaceID: input.WorkspaceID, URL: "https://workspace.medopl.cn/w/" + input.WorkspaceID + "/",
		Status: "running", ServiceName: "opl-verification-slot-01", Ready: true,
		Access: clients.WorkspaceRuntimeAccess{Username: "opl", Password: "must-not-leak", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "runtime-secret-ref"},
	}, nil
}

func providerAcceptanceTestTags(accountID, workspaceID, resourceID, operationID string) map[string]string {
	return map[string]string{
		"opl_account_id": accountID, "opl_workspace_id": workspaceID, "opl_resource_id": resourceID, "opl_operation_id": operationID,
	}
}

func newProviderAcceptanceTestServer(t *testing.T, fabric *providerAcceptanceFabric) (http.Handler, *memoryTableStore) {
	t.Helper()
	t.Setenv("OPL_TENCENT_ZONE", "ap-shanghai-2")
	t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "SA5.MEDIUM4")
	store := newMemoryTableStore()
	mustStore(t, store.SaveAccount(context.Background(), map[string]any{"id": testProviderAcceptanceAccount, "status": "active", "sub2apiUserId": int64(41)}))
	mustStore(t, store.SaveUser(context.Background(), map[string]any{
		"id": "usr-verification-slot-basic-01", "email": "verification-slot-basic-01@fenggaolab.org", "accountId": testProviderAcceptanceAccount, "role": "owner", "status": "active",
	}))
	service := controlplane.NewService(fakeLedgerClient{}, fabric, &testSub2APIClient{balance: 1_000_000, charges: map[string]int64{}})
	server, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatalf("create Provider Acceptance server: %v", err)
	}
	return server, store
}

func newProviderAcceptanceTestServerForSlot(t *testing.T, fabric *providerAcceptanceFabric, slot providerAcceptanceSlot) (http.Handler, *memoryTableStore) {
	t.Helper()
	t.Setenv("OPL_TENCENT_ZONE", "ap-shanghai-2")
	t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "SA5.MEDIUM4")
	t.Setenv("OPL_PRO_COMPUTE_INSTANCE_TYPE", "SA5.2XLARGE16")
	store := newMemoryTableStore()
	mustStore(t, store.SaveAccount(context.Background(), map[string]any{"id": slot.AccountID, "status": "active", "sub2apiUserId": int64(41)}))
	mustStore(t, store.SaveUser(context.Background(), map[string]any{
		"id": "usr-" + slot.ID, "email": slot.OwnerEmail, "accountId": slot.AccountID, "role": "owner", "status": "active",
	}))
	service := controlplane.NewService(fakeLedgerClient{}, fabric, &testSub2APIClient{balance: 1_000_000, charges: map[string]int64{}})
	server, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatalf("create Provider Acceptance server: %v", err)
	}
	return server, store
}

func providerAcceptanceRequest(server http.Handler, session *httptest.ResponseRecorder, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/operator/provider-acceptance", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	addAuth(req, session)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func providerAcceptanceBody(accountID, confirmation string) string {
	payload, _ := json.Marshal(map[string]any{
		"accountId": accountID, "slotId": "verification-slot-basic-01", "confirmation": confirmation,
		"environmentApproved": true, "purchaseBudget": 1, "maxApprovedProviderCost": 20,
	})
	return string(payload)
}

func providerAcceptanceBodyForSlot(slot providerAcceptanceSlot, approved bool, purchaseBudget int, maxApprovedProviderCost float64) string {
	payload, _ := json.Marshal(map[string]any{
		"accountId": slot.AccountID, "slotId": slot.ID, "confirmation": testProviderConfirmation,
		"environmentApproved": approved, "purchaseBudget": purchaseBudget, "maxApprovedProviderCost": maxApprovedProviderCost,
	})
	return string(payload)
}

func providerAcceptancePayload(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode Provider Acceptance response: %v: %s", err, rec.Body.String())
	}
	return payload
}

func TestProviderAcceptanceServiceTokenDoesNotCreateAUserSession(t *testing.T) {
	t.Setenv("OPL_OPERATOR_SUMMARY_TOKEN", "operator-secret")
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	body := `{"accountId":"acct-verification-slot-basic-01","confirmation":"I_UNDERSTAND_THIS_BUYS_ONE_PREPAID_CVM_AND_CBS","slotId":"verification-slot-basic-01","environmentApproved":true,"purchaseBudget":1,"maxApprovedProviderCost":20}`

	valid := httptest.NewRequest(http.MethodPost, "/api/operator/provider-acceptance", bytes.NewBufferString(body))
	valid.Header.Set("Content-Type", "application/json")
	valid.Header.Set("Idempotency-Key", testProviderAcceptanceKey)
	valid.Header.Set("x-opl-operator-token", "operator-secret")
	validRec := httptest.NewRecorder()
	server.ServeHTTP(validRec, valid)
	if validRec.Code == http.StatusUnauthorized || validRec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("service token status=%d cookie=%q body=%s", validRec.Code, validRec.Header().Get("Set-Cookie"), validRec.Body.String())
	}

	invalid := httptest.NewRequest(http.MethodPost, "/api/operator/provider-acceptance", bytes.NewBufferString(body))
	invalid.Header.Set("Content-Type", "application/json")
	invalid.Header.Set("Idempotency-Key", testProviderAcceptanceKey)
	invalid.Header.Set("x-opl-operator-token", "wrong")
	invalidRec := httptest.NewRecorder()
	server.ServeHTTP(invalidRec, invalid)
	if invalidRec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid service token status=%d body=%s", invalidRec.Code, invalidRec.Body.String())
	}
}

func TestProviderAcceptanceSelectsFixedBasicAndProAuthorities(t *testing.T) {
	for _, id := range []string{"verification-slot-basic-01", "verification-slot-pro-01"} {
		t.Run(id, func(t *testing.T) {
			slot := providerAcceptanceSlots[id]
			fabric := &providerAcceptanceFabric{}
			server, _ := newProviderAcceptanceTestServerForSlot(t, fabric, slot)
			operator := operatorSessionForTest(t, server)
			rec := providerAcceptanceRequest(server, operator, providerAcceptanceBodyForSlot(slot, true, 1, 20), slot.Key)
			if rec.Code != http.StatusOK {
				t.Fatalf("fixed %s authority = %d: %s", slot.PackageID, rec.Code, rec.Body.String())
			}
			payload := providerAcceptancePayload(t, rec)
			if payload["status"] != "in_progress" {
				t.Fatalf("fixed %s status = %#v", slot.PackageID, payload)
			}
			fabric.mu.Lock()
			defer fabric.mu.Unlock()
			if fabric.computeCreates != 1 || len(fabric.mutationKeys) != 1 || fabric.mutationKeys[0] != slot.Key+":compute" {
				t.Fatalf("fixed %s mutation = creates:%d keys:%v", slot.PackageID, fabric.computeCreates, fabric.mutationKeys)
			}
		})
	}
}

func TestProviderAcceptanceRequiresApprovalBudgetAndQuoteCapBeforeMutation(t *testing.T) {
	slot := providerAcceptanceSlots["verification-slot-basic-01"]
	for _, test := range []struct {
		name       string
		approved   bool
		budget     int
		maxCost    float64
		wantError  string
		wantChecks int
	}{
		{name: "environment approval", approved: false, budget: 1, maxCost: 20, wantError: "provider_acceptance_environment_approval_required"},
		{name: "slot budget", approved: true, budget: 2, maxCost: 20, wantError: "provider_acceptance_purchase_budget_invalid"},
		{name: "quote cap", approved: true, budget: 1, maxCost: 17.5, wantError: "provider_acceptance_provider_cost_exceeds_approval", wantChecks: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			fabric := &providerAcceptanceFabric{}
			server, _ := newProviderAcceptanceTestServerForSlot(t, fabric, slot)
			operator := operatorSessionForTest(t, server)
			rec := providerAcceptanceRequest(server, operator, providerAcceptanceBodyForSlot(slot, test.approved, test.budget, test.maxCost), slot.Key)
			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), test.wantError) {
				t.Fatalf("guard = %d %s, want %s", rec.Code, rec.Body.String(), test.wantError)
			}
			fabric.mu.Lock()
			defer fabric.mu.Unlock()
			if fabric.preflightCalls != test.wantChecks || fabric.computeCreates != 0 || fabric.storageCreates != 0 {
				t.Fatalf("guard reached provider: preflight=%d compute=%d storage=%d", fabric.preflightCalls, fabric.computeCreates, fabric.storageCreates)
			}
		})
	}
}

func TestProviderAcceptanceCreatesOneFixedSlotAndReusesIt(t *testing.T) {
	fabric := &providerAcceptanceFabric{}
	server, store := newProviderAcceptanceTestServer(t, fabric)
	operator := operatorSessionForTest(t, server)
	_ = operator.Result() // Freeze httptest's lazy response before concurrent cookie reads.
	body := providerAcceptanceBody(testProviderAcceptanceAccount, testProviderConfirmation)

	responses := make(chan *httptest.ResponseRecorder, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			responses <- providerAcceptanceRequest(server, operator, body, testProviderAcceptanceKey)
		}()
	}
	wait.Wait()
	close(responses)
	for rec := range responses {
		payload := providerAcceptancePayload(t, rec)
		if rec.Code != http.StatusOK || payload["status"] != "in_progress" {
			t.Fatalf("concurrent Provider Acceptance = %d %#v, want in_progress", rec.Code, payload)
		}
	}

	ready := providerAcceptanceRequest(server, operator, body, testProviderAcceptanceKey)
	readyPayload := providerAcceptancePayload(t, ready)
	if ready.Code != http.StatusOK || readyPayload["status"] != "ready" {
		t.Fatalf("ready Provider Acceptance = %d %#v", ready.Code, readyPayload)
	}
	if leaked := ready.Body.String(); strings.Contains(leaked, "workspace-key-secret") || strings.Contains(leaked, "must-not-leak") || strings.Contains(strings.ToLower(leaked), "secretref") {
		t.Fatalf("Provider Acceptance response leaked a secret: %s", leaked)
	}

	replayed := providerAcceptanceRequest(server, operator, body, testProviderAcceptanceKey)
	replayedPayload := providerAcceptancePayload(t, replayed)
	if replayed.Code != http.StatusOK || replayedPayload["status"] != "reused" {
		t.Fatalf("replayed Provider Acceptance = %d %#v", replayed.Code, replayedPayload)
	}

	operations, _ := store.ListRuntimeOperations(context.Background())
	var started map[string]any
	for _, operation := range operations {
		if operation["id"] == providerAcceptanceSlots["verification-slot-basic-01"].OperationID {
			started = operation
			break
		}
	}
	if started == nil {
		t.Fatalf("Provider Acceptance operation missing: %#v", operations)
	}
	started["status"], started["result"] = "started", "{}"
	delete(started, "errorCode")
	mustStore(t, store.SaveRuntimeOperation(context.Background(), started))
	recovered := providerAcceptanceRequest(server, operator, body, testProviderAcceptanceKey)
	if payload := providerAcceptancePayload(t, recovered); recovered.Code != http.StatusOK || payload["status"] != "reused" {
		t.Fatalf("recovered Provider Acceptance = %d %#v", recovered.Code, payload)
	}
	operations, _ = store.ListRuntimeOperations(context.Background())
	for _, operation := range operations {
		if operation["id"] == providerAcceptanceSlots["verification-slot-basic-01"].OperationID && (operation["status"] != "succeeded" || !strings.Contains(stringValue(operation["result"]), `"status":"reused"`)) {
			t.Fatalf("recovered operation = %#v, want succeeded reused result", operation)
		}
	}

	fabric.mu.Lock()
	counts := []int{fabric.computeCreates, fabric.computeSyncs, fabric.storageCreates, fabric.storageSyncs, fabric.attachmentCreates, fabric.secretWrites, fabric.runtimeCreates}
	keys := append([]string(nil), fabric.mutationKeys...)
	fabric.mu.Unlock()
	if want := []int{1, 1, 1, 1, 1, 1, 1}; len(counts) != len(want) {
		t.Fatal("unreachable count shape")
	} else {
		for index := range want {
			if counts[index] != want[index] {
				t.Fatalf("provider mutation counts = %v, want %v", counts, want)
			}
		}
	}
	for _, key := range keys {
		if !strings.HasPrefix(key, testProviderAcceptanceKey+":") {
			t.Fatalf("provider mutation key %q does not reuse fixed operation", key)
		}
	}

	workspaces, _ := store.ListWorkspaces(context.Background(), testProviderAcceptanceAccount)
	computes, _ := store.ListComputes(context.Background(), testProviderAcceptanceAccount)
	storages, _ := store.ListStorages(context.Background(), testProviderAcceptanceAccount)
	if len(workspaces) != 1 || workspaces[0]["verificationSlotId"] != "verification-slot-basic-01" || workspaces[0]["customerProduct"] != false {
		t.Fatalf("stored verification Workspace = %#v", workspaces)
	}
	if len(computes) != 1 || computes[0]["instanceType"] != "SA5.MEDIUM4" || computes[0]["chargeType"] != "PREPAID" || numberField(computes[0], "periodMonths", 0) != 1 {
		t.Fatalf("stored verification compute = %#v", computes)
	}
	if len(storages) != 1 || storages[0]["providerResourceId"] != "disk-verification-slot-01" || storages[0]["chargeType"] != "PREPAID" || numberField(storages[0], "periodMonths", 0) != 1 {
		t.Fatalf("stored verification storage = %#v", storages)
	}
}

func TestProviderAcceptanceCreatesAndReusesCompleteProSlot(t *testing.T) {
	slot := providerAcceptanceSlots["verification-slot-pro-01"]
	fabric := &providerAcceptanceFabric{}
	server, store := newProviderAcceptanceTestServerForSlot(t, fabric, slot)
	operator := operatorSessionForTest(t, server)
	body := providerAcceptanceBodyForSlot(slot, true, 1, 20)

	for attempt := 0; attempt < 2; attempt++ {
		rec := providerAcceptanceRequest(server, operator, body, slot.Key)
		if payload := providerAcceptancePayload(t, rec); rec.Code != http.StatusOK || payload["status"] != "in_progress" {
			t.Fatalf("Pro attempt %d = %d %#v", attempt+1, rec.Code, payload)
		}
	}
	ready := providerAcceptanceRequest(server, operator, body, slot.Key)
	if payload := providerAcceptancePayload(t, ready); ready.Code != http.StatusOK || payload["status"] != "ready" || mapField(payload, "slot")["id"] != slot.ID {
		t.Fatalf("Pro ready = %d %#v", ready.Code, payload)
	}
	fabric.mu.Lock()
	providerMutations := len(fabric.mutationKeys)
	fabric.mu.Unlock()
	store.mu.Lock()
	store.runtimeOps = nil
	store.mu.Unlock()
	reused := providerAcceptanceRequest(server, operator, providerAcceptanceBodyForSlot(slot, false, 0, 0), slot.Key)
	if payload := providerAcceptancePayload(t, reused); reused.Code != http.StatusOK || payload["status"] != "reused" {
		t.Fatalf("one-candidate Pro adoption = %d %#v", reused.Code, payload)
	}
	fabric.mu.Lock()
	if len(fabric.mutationKeys) != providerMutations {
		t.Fatalf("one-candidate adoption mutated provider: before=%d after=%d", providerMutations, len(fabric.mutationKeys))
	}
	fabric.mu.Unlock()

	workspaces, _ := store.ListWorkspaces(context.Background(), slot.AccountID)
	computes, _ := store.ListComputes(context.Background(), slot.AccountID)
	storages, _ := store.ListStorages(context.Background(), slot.AccountID)
	if len(workspaces) != 1 || workspaces[0]["packageId"] != "pro" || workspaces[0]["verificationSlotId"] != slot.ID || workspaces[0]["customerProduct"] != false {
		t.Fatalf("stored Pro Workspace = %#v", workspaces)
	}
	if len(computes) != 1 || computes[0]["packageId"] != "pro" || computes[0]["instanceType"] != slot.InstanceType {
		t.Fatalf("stored Pro compute = %#v", computes)
	}
	if len(storages) != 1 || numberField(storages[0], "sizeGb", 0) != 100 {
		t.Fatalf("stored Pro storage = %#v", storages)
	}
}

func TestProviderAcceptanceMultipleInventoryCandidatesStopBeforePreflight(t *testing.T) {
	slot := providerAcceptanceSlots["verification-slot-basic-01"]
	fabric := &providerAcceptanceFabric{}
	server, store := newProviderAcceptanceTestServerForSlot(t, fabric, slot)
	operator := operatorSessionForTest(t, server)
	for _, id := range []string{"candidate-a", "candidate-b"} {
		mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{
			"id": id, "accountId": slot.AccountID, "ownerAccountId": slot.AccountID,
			"verificationSlotId": slot.ID, "customerProduct": false, "status": "running",
		}))
	}
	rec := providerAcceptanceRequest(server, operator, providerAcceptanceBodyForSlot(slot, true, 1, 20), slot.Key)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errPrimaryWorkspaceExists.Error()) {
		t.Fatalf("multiple candidates = %d: %s", rec.Code, rec.Body.String())
	}
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	if fabric.preflightCalls != 0 || fabric.computeCreates != 0 || fabric.storageCreates != 0 {
		t.Fatalf("ambiguous inventory reached provider: preflight=%d compute=%d storage=%d", fabric.preflightCalls, fabric.computeCreates, fabric.storageCreates)
	}
}

func TestProviderAcceptanceAmbiguousComputeOrStorageInventoryStopsBeforePreflight(t *testing.T) {
	slot := providerAcceptanceSlots["verification-slot-basic-01"]
	for _, test := range []struct {
		name     string
		resource string
		ids      []string
	}{
		{name: "unexpected compute", resource: "compute", ids: []string{"compute-other"}},
		{name: "multiple computes", resource: "compute", ids: []string{providerAcceptanceComputeID(slot), "compute-other"}},
		{name: "unexpected storage", resource: "storage", ids: []string{"storage-other"}},
		{name: "multiple storages", resource: "storage", ids: []string{providerAcceptanceStorageID(slot), "storage-other"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fabric := &providerAcceptanceFabric{}
			server, store := newProviderAcceptanceTestServerForSlot(t, fabric, slot)
			operator := operatorSessionForTest(t, server)
			for _, id := range test.ids {
				row := map[string]any{"id": id, "accountId": slot.AccountID, "workspaceId": primaryWorkspaceID(slot.AccountID), "verificationSlotId": slot.ID, "customerProduct": false}
				if test.resource == "compute" {
					mustStore(t, store.SaveCompute(context.Background(), row))
				} else {
					mustStore(t, store.SaveStorage(context.Background(), row))
				}
			}
			rec := providerAcceptanceRequest(server, operator, providerAcceptanceBodyForSlot(slot, true, 1, 20), slot.Key)
			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "provider_acceptance_inventory_ambiguous") {
				t.Fatalf("ambiguous %s inventory = %d: %s", test.resource, rec.Code, rec.Body.String())
			}
			fabric.mu.Lock()
			defer fabric.mu.Unlock()
			if fabric.preflightCalls != 0 || fabric.computeCreates != 0 || fabric.storageCreates != 0 {
				t.Fatalf("ambiguous inventory reached provider: preflight=%d compute=%d storage=%d", fabric.preflightCalls, fabric.computeCreates, fabric.storageCreates)
			}
		})
	}
}

func TestProviderAcceptanceGuardsFixedAuthorityAndPrimaryWorkspace(t *testing.T) {
	fabric := &providerAcceptanceFabric{}
	server, store := newProviderAcceptanceTestServer(t, fabric)
	operator := operatorSessionForTest(t, server)

	for _, test := range []struct {
		name string
		body string
		key  string
	}{
		{name: "confirmation", body: providerAcceptanceBody(testProviderAcceptanceAccount, "yes"), key: testProviderAcceptanceKey},
		{name: "account", body: providerAcceptanceBody("acct-other", testProviderConfirmation), key: testProviderAcceptanceKey},
		{name: "idempotency", body: providerAcceptanceBody(testProviderAcceptanceAccount, testProviderConfirmation), key: "another-key"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := providerAcceptanceRequest(server, operator, test.body, test.key)
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusConflict {
				t.Fatalf("guard status = %d: %s", rec.Code, rec.Body.String())
			}
		})
	}

	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{
		"id": primaryWorkspaceID(testProviderAcceptanceAccount), "accountId": testProviderAcceptanceAccount, "ownerAccountId": testProviderAcceptanceAccount, "customerProduct": true, "status": "running",
	}))
	rec := providerAcceptanceRequest(server, operator, providerAcceptanceBody(testProviderAcceptanceAccount, testProviderConfirmation), testProviderAcceptanceKey)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errPrimaryWorkspaceExists.Error()) {
		t.Fatalf("existing primary status = %d: %s", rec.Code, rec.Body.String())
	}
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	if fabric.preflightCalls != 0 || fabric.computeCreates != 0 || fabric.storageCreates != 0 {
		t.Fatalf("guarded request reached provider: preflight=%d compute=%d storage=%d", fabric.preflightCalls, fabric.computeCreates, fabric.storageCreates)
	}
}

func TestProviderAcceptancePersistsManualReviewWithoutRetryingUnknownStorage(t *testing.T) {
	fabric := &providerAcceptanceFabric{failStorageCreation: true}
	server, store := newProviderAcceptanceTestServer(t, fabric)
	operator := operatorSessionForTest(t, server)
	body := providerAcceptanceBody(testProviderAcceptanceAccount, testProviderConfirmation)

	first := providerAcceptanceRequest(server, operator, body, testProviderAcceptanceKey)
	if payload := providerAcceptancePayload(t, first); first.Code != http.StatusOK || payload["status"] != "in_progress" {
		t.Fatalf("first status = %d %#v", first.Code, payload)
	}
	failed := providerAcceptanceRequest(server, operator, body, testProviderAcceptanceKey)
	if payload := providerAcceptancePayload(t, failed); failed.Code != http.StatusOK || payload["status"] != "manual_review" {
		t.Fatalf("manual review status = %d %#v", failed.Code, payload)
	}
	replayed := providerAcceptanceRequest(server, operator, body, testProviderAcceptanceKey)
	if payload := providerAcceptancePayload(t, replayed); replayed.Code != http.StatusOK || payload["status"] != "manual_review" {
		t.Fatalf("manual review replay = %d %#v", replayed.Code, payload)
	}

	fabric.mu.Lock()
	storageCreates := fabric.storageCreates
	fabric.mu.Unlock()
	if storageCreates != 1 {
		t.Fatalf("unknown storage result retried %d times", storageCreates)
	}
	operations, _ := store.ListRuntimeOperations(context.Background())
	found := false
	for _, operation := range operations {
		if operation["id"] == "provider-acceptance-verification-slot-basic-01" && operation["status"] == "manual_review" {
			found = true
		}
	}
	if !found {
		t.Fatalf("manual review operation not persisted: %#v", operations)
	}
}
