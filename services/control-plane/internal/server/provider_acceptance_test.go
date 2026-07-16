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
	testProviderAcceptanceAccount = "acct-verification-slot-01"
	testProviderAcceptanceKey     = "provider-acceptance:verification-slot-01"
	testProviderConfirmation      = "I_UNDERSTAND_THIS_BUYS_ONE_PREPAID_CVM_AND_CBS"
)

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
	f.compute = clients.ComputeAllocation{
		ID: id, AccountID: testProviderAcceptanceAccount, WorkspaceID: primaryWorkspaceID(testProviderAcceptanceAccount), PackageID: "basic",
		Status: "running", Provider: "tencent-tke", ProviderResourceID: "node/slot-01", ProviderRequestID: "req-compute-slot",
		NodePoolID: "np-verification-slot-01", InstanceID: "ins-verification-slot-01", CVMInstanceID: "ins-verification-slot-01", NodeName: "node-verification-slot-01",
		InstanceType: "SA5.MEDIUM4", Zone: "ap-shanghai-2", ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z",
		ProviderData: map[string]string{"instanceType": "SA5.MEDIUM4", "zone": "ap-shanghai-2"},
		CostTags:     providerAcceptanceTestTags(testProviderAcceptanceAccount, primaryWorkspaceID(testProviderAcceptanceAccount), id, "op-compute-slot"),
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
	if input.AccountID != testProviderAcceptanceAccount || input.GatewayAPIKey != "workspace-key-secret" {
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
		"id": "usr-verification-slot-01", "email": "verification-slot-01@fenggaolab.org", "accountId": testProviderAcceptanceAccount, "role": "owner", "status": "active",
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
	payload, _ := json.Marshal(map[string]string{"accountId": accountID, "slotId": "verification-slot-01", "confirmation": confirmation})
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
		if operation["id"] == providerAcceptanceOperationID {
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
		if operation["id"] == providerAcceptanceOperationID && (operation["status"] != "succeeded" || !strings.Contains(stringValue(operation["result"]), `"status":"reused"`)) {
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
	if len(workspaces) != 1 || workspaces[0]["verificationSlotId"] != "verification-slot-01" || workspaces[0]["customerProduct"] != false {
		t.Fatalf("stored verification Workspace = %#v", workspaces)
	}
	if len(computes) != 1 || computes[0]["instanceType"] != "SA5.MEDIUM4" || computes[0]["chargeType"] != "PREPAID" || numberField(computes[0], "periodMonths", 0) != 1 {
		t.Fatalf("stored verification compute = %#v", computes)
	}
	if len(storages) != 1 || storages[0]["providerResourceId"] != "disk-verification-slot-01" || storages[0]["chargeType"] != "PREPAID" || numberField(storages[0], "periodMonths", 0) != 1 {
		t.Fatalf("stored verification storage = %#v", storages)
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
		if operation["id"] == "provider-acceptance-verification-slot-01" && operation["status"] == "manual_review" {
			found = true
		}
	}
	if !found {
		t.Fatalf("manual review operation not persisted: %#v", operations)
	}
}
