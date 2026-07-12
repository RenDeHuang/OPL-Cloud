package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func mustStore(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("store setup failed: %v", err)
	}
}

func newExecutionTestServer(t *testing.T, service *controlplane.Service) http.Handler {
	t.Helper()
	store := newMemoryTableStore()
	mustStore(t, store.SaveOrganization(context.Background(), map[string]any{"id": "org-alpha", "billingAccountId": "acct-alpha", "status": "active"}))
	mustStore(t, store.SaveMembership(context.Background(), map[string]any{"id": "mem-admin-alpha", "organizationId": "org-alpha", "userId": "usr-admin", "accountId": "acct-alpha", "role": "admin", "status": "active"}))
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{"id": "workspace-alpha", "accountId": "acct-alpha", "status": "running"}))
	server, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatalf("create execution test server: %v", err)
	}
	return server
}

type failingProjectIdentityStore struct {
	*memoryTableStore
	workspaceErr    error
	organizationErr error
}

func (s *failingProjectIdentityStore) ListWorkspaces(ctx context.Context, accountID string) ([]map[string]any, error) {
	if s.workspaceErr != nil {
		return nil, s.workspaceErr
	}
	return s.memoryTableStore.ListWorkspaces(ctx, accountID)
}

func (s *failingProjectIdentityStore) ListOrganizations(ctx context.Context) ([]map[string]any, error) {
	if s.organizationErr != nil {
		return nil, s.organizationErr
	}
	return s.memoryTableStore.ListOrganizations(ctx)
}

func storedWorkspace(t *testing.T, app *controlPlaneServer, id string) map[string]any {
	t.Helper()
	workspace, ok := app.getWorkspace(id)
	if !ok {
		t.Fatalf("workspace %s not found", id)
	}
	return workspace
}

func storedAttachment(t *testing.T, app *controlPlaneServer, id string) map[string]any {
	t.Helper()
	attachment, ok := app.getAttachment(id)
	if !ok {
		t.Fatalf("attachment %s not found", id)
	}
	return attachment
}

func TestCreateWorkspaceHTTPUsesMutationKeyWhenHeaderIsAbsent(t *testing.T) {
	calls := []string{}
	ledger := &fakeLedgerClientWithKeys{fakeLedgerClient{}, []string{}}
	server := NewServer(controlplane.NewService(ledger, &fakeFabricClient{calls: &calls}))

	compute := createResource(t, server, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	storage := createResource(t, server, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","sizeGb":10}`)
	createResource(t, server, http.MethodPost, "/api/storage-attachments", `{"workspaceId":"ws-alpha","computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`","mountPath":"/data"}`)

	body := bytes.NewBufferString(`{"accountId":"acct-alpha","ownerId":"usr-owner","workspaceName":"Alpha Lab","attachmentId":"attachment-from-fabric"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces", body)
	session := tenantAdminSessionForTest(t, server)
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

	compute := createResource(t, server, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	storage := createResource(t, server, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","sizeGb":10}`)
	createResource(t, server, http.MethodPost, "/api/storage-attachments", `{"workspaceId":"ws-alpha","computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`","mountPath":"/data"}`)

	body := bytes.NewBufferString(`{"accountId":"acct-alpha","ownerId":"usr-owner","workspaceName":"Alpha Lab","attachmentId":"attachment-from-fabric"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces", body)
	session := tenantAdminSessionForTest(t, server)
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
	if workspace["computeAllocationId"] != compute["id"] || workspace["storageId"] != storage["id"] || workspace["attachmentId"] != "attachment-from-fabric" || workspace["receiptId"] != "receipt-from-ledger" {
		t.Fatalf("workspace missing ledger/fabric evidence: %#v", workspace)
	}
	if access, ok := workspace["access"].(map[string]any); !ok || access["tokenStatus"] != "active" {
		t.Fatalf("workspace response must include active URL access state: %#v", workspace)
	}
	if access := workspace["access"].(map[string]any); access["account"] != "admin" || access["password"] != nil {
		t.Fatalf("workspace creation must include credential metadata without plaintext: %#v", access)
	}
	if workspace["runtimePassword"] != nil {
		t.Fatalf("workspace response leaked internal runtimePassword field: %#v", workspace)
	}
	if slices.Contains(calls[3:], "fabric.compute") || slices.Contains(calls[3:], "fabric.storage") {
		t.Fatalf("workspace create must not allocate replacement resources: %#v", calls)
	}
	management := requestWithSession(t, server, session, http.MethodGet, "/api/management/state", "")
	if strings.Contains(management.Body.String(), "runtime-password-alpha") {
		t.Fatalf("Control Plane persisted Workspace password in state or audit: %s", management.Body.String())
	}
}

func TestResumeWorkspaceValidatesRetainedResourcesBeforeFabric(t *testing.T) {
	calls := []string{}
	store := newMemoryTableStore()
	workspace := map[string]any{
		"id": "workspace-alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha", "ownerUserId": "usr-owner",
		"name": "Alpha Lab", "packageId": "basic", "url": "https://workspace.medopl.cn/w/workspace-alpha/",
		"state": "suspended", "status": "suspended", "storageId": "storage-alpha",
		"currentComputeAllocationId": "", "currentAttachmentId": "", "runtimeId": "runtime-old",
		"runtime": map[string]any{"serviceName": "opl-compute-old"}, "runtimeServiceName": "opl-compute-old-root", "serviceName": "opl-compute-old-legacy", "access": map[string]any{"tokenStatus": "suspended"},
	}
	mustStore(t, store.SaveWorkspace(context.Background(), workspace))
	mustStore(t, store.SaveStorage(context.Background(), map[string]any{"id": "storage-alpha", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "available"}))
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{"id": "compute-replacement", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "running"}))
	mustStore(t, store.SaveAttachment(context.Background(), map[string]any{"id": "attachment-replacement", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "computeAllocationId": "compute-replacement", "storageId": "storage-alpha", "status": "attached"}))
	mustStore(t, store.SaveProjectTaskSyncHead(context.Background(), map[string]any{"id": "project-alpha", "workspaceId": "workspace-alpha", "projectId": "project-alpha", "taskId": "task-alpha", "version": int64(7)}))
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{calls: &calls}), store)
	if err != nil {
		t.Fatalf("create resume server: %v", err)
	}
	admin := operatorSessionForTest(t, server)
	ownerUser := createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"owner-resume@lab.example","accountId":"acct-alpha","role":"member","password":"CorrectHorseBatteryStaple!"}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/organizations/members", `{"organizationId":"org-alpha","userId":"`+stringValue(ownerUser["id"])+`","accountId":"acct-alpha","role":"member"}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"outside-resume@lab.example","accountId":"acct-beta","role":"member","password":"CorrectHorseBatteryStaple!"}`)
	owner := loginForTest(t, server, "owner-resume@lab.example", "CorrectHorseBatteryStaple!")
	outsider := loginForTest(t, server, "outside-resume@lab.example", "CorrectHorseBatteryStaple!")
	body := `{"computeAllocationId":"compute-replacement","attachmentId":"attachment-replacement"}`
	missingKeyReq := httptest.NewRequest(http.MethodPost, "/api/workspaces/workspace-alpha/resume", bytes.NewBufferString(body))
	missingKeyReq.Header.Set("Content-Type", "application/json")
	addAuth(missingKeyReq, owner)
	missingKey := httptest.NewRecorder()
	server.ServeHTTP(missingKey, missingKeyReq)
	if missingKey.Code != http.StatusBadRequest || !strings.Contains(missingKey.Body.String(), "missing Idempotency-Key") || len(calls) != 0 {
		t.Fatalf("missing-key resume = %d calls=%#v: %s", missingKey.Code, calls, missingKey.Body.String())
	}

	before := len(calls)
	forbidden := requestWithSession(t, server, outsider, http.MethodPost, "/api/workspaces/workspace-alpha/resume", body)
	if forbidden.Code != http.StatusUnauthorized || len(calls) != before {
		t.Fatalf("cross-account resume = %d calls=%#v body=%s", forbidden.Code, calls[before:], forbidden.Body.String())
	}

	workspace["state"], workspace["status"] = "running", "running"
	mustStore(t, store.SaveWorkspace(context.Background(), workspace))
	wrongState := requestWithSession(t, server, owner, http.MethodPost, "/api/workspaces/workspace-alpha/resume", body)
	if wrongState.Code != http.StatusConflict || len(calls) != before {
		t.Fatalf("running resume = %d calls=%#v body=%s", wrongState.Code, calls[before:], wrongState.Body.String())
	}

	workspace["state"], workspace["status"] = "suspended", "suspended"
	mustStore(t, store.SaveWorkspace(context.Background(), workspace))
	computes, _ := store.ListComputes(context.Background(), "")
	computes[0]["accountId"] = "acct-beta"
	mustStore(t, store.SaveCompute(context.Background(), computes[0]))
	wrongResourceAccount := requestWithSession(t, server, owner, http.MethodPost, "/api/workspaces/workspace-alpha/resume", body)
	if wrongResourceAccount.Code != http.StatusConflict || len(calls) != before {
		t.Fatalf("wrong-account resource resume = %d calls=%#v body=%s", wrongResourceAccount.Code, calls[before:], wrongResourceAccount.Body.String())
	}
	computes[0]["accountId"] = "acct-alpha"
	mustStore(t, store.SaveCompute(context.Background(), computes[0]))
	attachment, _ := store.ListAttachments(context.Background(), "")
	attachment[0]["storageId"] = "storage-other"
	mustStore(t, store.SaveAttachment(context.Background(), attachment[0]))
	wrongStorage := requestWithSession(t, server, owner, http.MethodPost, "/api/workspaces/workspace-alpha/resume", body)
	if wrongStorage.Code != http.StatusConflict || len(calls) != before {
		t.Fatalf("wrong-storage resume = %d calls=%#v body=%s", wrongStorage.Code, calls[before:], wrongStorage.Body.String())
	}

	attachment[0]["storageId"] = "storage-alpha"
	mustStore(t, store.SaveAttachment(context.Background(), attachment[0]))
	resumed := requestWithSession(t, server, owner, http.MethodPost, "/api/workspaces/workspace-alpha/resume", body)
	if resumed.Code != http.StatusOK {
		t.Fatalf("resume status = %d: %s", resumed.Code, resumed.Body.String())
	}
	var result map[string]any
	if err := json.NewDecoder(resumed.Body).Decode(&result); err != nil {
		t.Fatalf("decode resume: %v", err)
	}
	if result["id"] != "workspace-alpha" || result["url"] != "https://workspace.medopl.cn/w/workspace-alpha/" || result["storageId"] != "storage-alpha" || result["currentComputeAllocationId"] != "compute-replacement" || result["currentAttachmentId"] != "attachment-replacement" {
		t.Fatalf("resume changed stable identity or missed replacement resources: %#v", result)
	}
	if got := calls[before:]; !slices.Equal(got, []string{"fabric.runtime"}) {
		t.Fatalf("resume Fabric calls = %#v", got)
	}
	replayed := requestWithSession(t, server, owner, http.MethodPost, "/api/workspaces/workspace-alpha/resume", body)
	if replayed.Code != http.StatusOK || len(calls[before:]) != 1 {
		t.Fatalf("resume replay = %d calls=%#v body=%s", replayed.Code, calls[before:], replayed.Body.String())
	}
	var replayedResult map[string]any
	if err := json.NewDecoder(replayed.Body).Decode(&replayedResult); err != nil || !reflect.DeepEqual(replayedResult, result) {
		t.Fatalf("resume replay changed prior result: first=%#v replay=%#v err=%v", result, replayedResult, err)
	}
	changed := requestWithSession(t, server, owner, http.MethodPost, "/api/workspaces/workspace-alpha/resume", `{"computeAllocationId":"compute-other","attachmentId":"attachment-replacement"}`)
	if changed.Code != http.StatusConflict || !strings.Contains(changed.Body.String(), "idempotency_conflict") || len(calls[before:]) != 1 {
		t.Fatalf("changed resume replay = %d calls=%#v body=%s", changed.Code, calls[before:], changed.Body.String())
	}
	stored, _ := store.ListWorkspaces(context.Background(), "")
	if nested(stored[0], "runtime", "serviceName") != "opl-compute-from-fabric" || stored[0]["runtimeServiceName"] != "opl-compute-from-fabric" || stored[0]["serviceName"] != "opl-compute-from-fabric" {
		t.Fatalf("resume kept stale runtime service pointers: %#v", stored[0])
	}
	heads, err := store.ListProjectTaskSyncHeads(context.Background())
	if err != nil || len(heads) != 1 || numberField(heads[0], "version", 0) != 7 {
		t.Fatalf("resume changed project/task sync heads: %#v err=%v", heads, err)
	}
}

type failingResumeCommitStore struct{ *memoryTableStore }

func (s *failingResumeCommitStore) CommitWorkspaceResume(context.Context, map[string]any, map[string]any, map[string]any) error {
	return errors.New("audit write failed")
}

func (s *failingResumeCommitStore) SaveAuditEvent(context.Context, map[string]any) error {
	return errors.New("audit write failed")
}

func TestResumeWorkspaceAuditFailureDoesNotPersistRunningProjection(t *testing.T) {
	store := &failingResumeCommitStore{memoryTableStore: newMemoryTableStore()}
	hash, err := hashPassword("CorrectHorseBatteryStaple!")
	if err != nil {
		t.Fatal(err)
	}
	mustStore(t, store.SaveUser(context.Background(), map[string]any{"id": "usr-admin", "email": "admin@medopl.cn", "accountId": "acct-alpha", "role": "admin", "status": "active", "passwordHash": hash}))
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{"id": "workspace-alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha", "state": "suspended", "status": "suspended", "storageId": "storage-alpha", "url": "https://workspace.medopl.cn/w/workspace-alpha/", "access": map[string]any{"tokenStatus": "suspended"}}))
	mustStore(t, store.SaveStorage(context.Background(), map[string]any{"id": "storage-alpha", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "available"}))
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{"id": "compute-new", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "running"}))
	mustStore(t, store.SaveAttachment(context.Background(), map[string]any{"id": "attachment-new", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "computeAllocationId": "compute-new", "storageId": "storage-alpha", "status": "attached"}))
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatalf("create audit failure server: %v", err)
	}
	response := requestWithSession(t, server, loginForTest(t, server, "admin@medopl.cn", "CorrectHorseBatteryStaple!"), http.MethodPost, "/api/workspaces/workspace-alpha/resume", `{"computeAllocationId":"compute-new","attachmentId":"attachment-new"}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("audit failure status = %d: %s", response.Code, response.Body.String())
	}
	workspace, _ := store.ListWorkspaces(context.Background(), "")
	if workspace[0]["state"] != "suspended" || workspace[0]["status"] != "suspended" {
		t.Fatalf("audit failure left partial running projection: %#v", workspace[0])
	}
	operations, _ := store.ListRuntimeOperations(context.Background())
	if len(operations) != 1 || operations[0]["status"] != "retryable" {
		t.Fatalf("audit failure must leave deterministic retryable operation: %#v", operations)
	}
}

func TestResumeWorkspaceKeepsUnreadyRuntimeClosedAndCredentialsIntact(t *testing.T) {
	store := newMemoryTableStore()
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{"id": "workspace-alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha", "state": "suspended", "status": "suspended", "storageId": "storage-alpha", "url": "https://workspace.medopl.cn/w/workspace-alpha/", "runtime": map[string]any{"serviceName": "old-nested"}, "runtimeServiceName": "old-root", "serviceName": "old-legacy", "access": map[string]any{"tokenStatus": "suspended", "account": "opl", "username": "opl", "credentialStatus": "configured", "credentialVersion": "v1", "secretRef": "old-secret"}}))
	mustStore(t, store.SaveStorage(context.Background(), map[string]any{"id": "storage-alpha", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "available"}))
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{"id": "compute-new", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "running"}))
	mustStore(t, store.SaveAttachment(context.Background(), map[string]any{"id": "attachment-new", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "computeAllocationId": "compute-new", "storageId": "storage-alpha", "status": "attached"}))
	runtime := clients.WorkspaceRuntime{ID: "runtime-new", WorkspaceID: "workspace-alpha", Status: "provisioning", Ready: false}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{runtime: runtime}), store)
	if err != nil {
		t.Fatalf("create provisioning resume server: %v", err)
	}
	response := requestWithSession(t, server, tenantAdminSessionForTest(t, server), http.MethodPost, "/api/workspaces/workspace-alpha/resume", `{"computeAllocationId":"compute-new","attachmentId":"attachment-new"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("provisioning resume status = %d: %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode provisioning resume: %v", err)
	}
	access := mapField(body, "access")
	if body["state"] != "provisioning" || body["openable"] != false || access["tokenStatus"] != "suspended" || access["credentialStatus"] != "configured" || access["secretRef"] != "old-secret" {
		t.Fatalf("unready runtime became openable or cleared credentials: %#v", body)
	}
	stored, _ := store.ListWorkspaces(context.Background(), "")
	if stringValue(nested(stored[0], "runtime", "serviceName")) != "" || stringValue(stored[0]["runtimeServiceName"]) != "" || stringValue(stored[0]["serviceName"]) != "" {
		t.Fatalf("provisioning resume kept stale service pointers: %#v", stored[0])
	}
}

func TestConcurrentWorkspaceResumeClaimsBeforeFabricSideEffects(t *testing.T) {
	store := newMemoryTableStore()
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{"id": "workspace-alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha", "state": "suspended", "status": "suspended", "storageId": "storage-alpha", "url": "https://workspace.medopl.cn/w/workspace-alpha/", "access": map[string]any{"tokenStatus": "suspended"}}))
	mustStore(t, store.SaveStorage(context.Background(), map[string]any{"id": "storage-alpha", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "available"}))
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{"id": "compute-new", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "running"}))
	mustStore(t, store.SaveAttachment(context.Background(), map[string]any{"id": "attachment-new", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "computeAllocationId": "compute-new", "storageId": "storage-alpha", "status": "attached"}))
	fabric := &blockingResumeFabricClient{fakeFabricClient: fakeFabricClient{}, entered: make(chan struct{}, 2), release: make(chan struct{})}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, fabric), store)
	if err != nil {
		t.Fatalf("create resume server: %v", err)
	}
	session := tenantAdminSessionForTest(t, server)
	resume := func(key string) <-chan *httptest.ResponseRecorder {
		done := make(chan *httptest.ResponseRecorder, 1)
		req := httptest.NewRequest(http.MethodPost, "/api/workspaces/workspace-alpha/resume", bytes.NewBufferString(`{"computeAllocationId":"compute-new","attachmentId":"attachment-new"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		addAuth(req, session)
		go func() {
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			done <- rec
		}()
		return done
	}
	first := resume("resume-first")
	select {
	case <-fabric.entered:
	case <-time.After(time.Second):
		t.Fatal("first resume did not reach Fabric")
	}
	second := resume("resume-second")
	select {
	case <-fabric.entered:
		close(fabric.release)
		<-first
		<-second
		t.Fatal("concurrent resume reached Fabric twice")
	case response := <-second:
		if response.Code != http.StatusConflict {
			close(fabric.release)
			<-first
			t.Fatalf("second resume status = %d: %s", response.Code, response.Body.String())
		}
	case <-time.After(time.Second):
		close(fabric.release)
		<-first
		t.Fatal("second resume did not resolve deterministically")
	}
	close(fabric.release)
	if response := <-first; response.Code != http.StatusOK {
		t.Fatalf("first resume status = %d: %s", response.Code, response.Body.String())
	}
}

func TestPricingPreviewMatchesResourceHoldAmount(t *testing.T) {
	ledger := &capturingHoldLedgerClient{}
	server := NewServer(controlplane.NewService(ledger, &fakeFabricClient{}))
	session := tenantAdminSessionForTest(t, server)

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

func TestPricingCatalogMatchesContractDefaults(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "packages", "contracts", "opl-cloud-pricing-contract.json"))
	if err != nil {
		t.Fatalf("read pricing contract: %v", err)
	}
	var contract map[string]any
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("decode pricing contract: %v", err)
	}
	computeHourly, _ := contract["computeHourly"].(map[string]any)
	assertPlanPrice := func(packageID string) {
		t.Helper()
		plan := packageByID(packageID)
		price, _ := plan["price"].(map[string]any)
		if price["computeHourly"] != computeHourly[packageID] {
			t.Fatalf("%s compute price = %v, want contract %v", packageID, price["computeHourly"], computeHourly[packageID])
		}
		if price["storageGbMonth"] != contract["storageGbMonth"] {
			t.Fatalf("%s storage price = %v, want contract %v", packageID, price["storageGbMonth"], contract["storageGbMonth"])
		}
	}
	if pricingCatalogVersion != contract["catalogVersion"] || pricingCurrency != contract["currency"] {
		t.Fatalf("pricing catalog identity drifted from contract")
	}
	assertPlanPrice("basic")
	assertPlanPrice("pro")
}

func TestBillingSummaryReadsLedgerWallet(t *testing.T) {
	server := NewServer(controlplane.NewService(walletSummaryLedgerClient{}, &fakeFabricClient{}))
	session := tenantAdminSessionForTest(t, server)
	req := httptest.NewRequest(http.MethodGet, "/api/billing/summary?accountId=acct-alpha", nil)
	addAuth(req, session)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("billing summary status = %d: %s", rec.Code, rec.Body.String())
	}
	var summary map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summary["accountId"] != "acct-alpha" || summary["balanceCents"] != float64(12345) || summary["availableCents"] != float64(12000) || summary["frozenCents"] != float64(345) {
		t.Fatalf("billing summary must come from Ledger wallet: %#v", summary)
	}
}

func TestComputePoolsReadFabricCatalog(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &catalogFabricClient{}))
	session := operatorSessionForTest(t, server)
	req := httptest.NewRequest(http.MethodGet, "/api/compute-pools", nil)
	addAuth(req, session)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("compute pools status = %d: %s", rec.Code, rec.Body.String())
	}
	var pools []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&pools); err != nil {
		t.Fatalf("decode pools: %v", err)
	}
	if len(pools) != 1 || pools[0]["id"] != "pool-ultra" || pools[0]["packageId"] != "ultra" || pools[0]["provider"] != "fabric-test" {
		t.Fatalf("compute pools must come from Fabric catalog: %#v", pools)
	}
}

func TestConsoleStateComputePoolsReadFabricCatalog(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &catalogFabricClient{}))
	session := tenantAdminSessionForTest(t, server)
	req := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-alpha", nil)
	addAuth(req, session)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("state status = %d: %s", rec.Code, rec.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	pools := state["computePools"].([]any)
	first := pools[0].(map[string]any)
	if len(pools) != 1 || first["id"] != "pool-ultra" || first["packageId"] != "ultra" || first["provider"] != "fabric-test" {
		t.Fatalf("state compute pools must come from Fabric catalog: %#v", pools)
	}
}

func TestWorkspaceRuntimeStatusPassesFabricChecks(t *testing.T) {
	calls := []string{}
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{calls: &calls}))
	session := tenantAdminSessionForTest(t, server)
	compute := createResourceWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	storage := createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","sizeGb":10}`)
	createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-attachments", `{"workspaceId":"ws-alpha","computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`","mountPath":"/data"}`)
	workspace := createResourceWithSession(t, server, session, http.MethodPost, "/api/workspaces", `{"accountId":"acct-alpha","ownerId":"usr-owner","workspaceName":"Alpha Lab","attachmentId":"attachment-from-fabric"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/runtime-status", bytes.NewBufferString(`{"workspaceId":"`+stringValue(workspace["id"])+`"}`))
	req.Header.Set("Content-Type", "application/json")
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
	access := body["access"].(map[string]any)
	if access["password"] != "runtime-password-alpha" || access["secretRef"] != "opl-compute-from-fabric-env" {
		t.Fatalf("runtime status must return transient Fabric credentials: %#v", body)
	}
	checks := body["checks"].([]any)
	if len(checks) != 2 || checks[0].(map[string]any)["name"] != "deployment_ready" || checks[1].(map[string]any)["name"] != "service_endpoints_ready" {
		t.Fatalf("runtime checks must pass through Fabric details: %#v", body["checks"])
	}
}

func TestWorkspaceRuntimeStatusDoesNotReadSecretForUnknownProjection(t *testing.T) {
	calls := []string{}
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{calls: &calls}))
	admin := operatorSessionForTest(t, server)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"outside-unknown@lab.example","accountId":"acct-beta","role":"member","password":"CorrectHorseBatteryStaple!"}`)
	outsider := loginForTest(t, server, "outside-unknown@lab.example", "CorrectHorseBatteryStaple!")

	before := len(calls)
	response := requestWithSession(t, server, outsider, http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"ws-unknown"}`)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "not_authenticated") {
		t.Fatalf("unknown runtime status = %d: %s", response.Code, response.Body.String())
	}
	if len(calls) != before || strings.Contains(response.Body.String(), "runtime-password-alpha") {
		t.Fatalf("unknown projection reached Fabric or returned a password: calls=%#v body=%s", calls[before:], response.Body.String())
	}
}

func TestWorkspaceRuntimeStatusForbidsCrossAccountSecretRead(t *testing.T) {
	calls := []string{}
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{calls: &calls}))
	admin := tenantAdminSessionForTest(t, server)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"outside@lab.example","accountId":"acct-beta","role":"member","password":"CorrectHorseBatteryStaple!"}`)
	outsider := loginForTest(t, server, "outside@lab.example", "CorrectHorseBatteryStaple!")

	compute := createResourceWithSession(t, server, admin, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	storage := createResourceWithSession(t, server, admin, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","sizeGb":10}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/storage-attachments", `{"workspaceId":"ws-alpha","computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`","mountPath":"/data"}`)
	workspace := createResourceWithSession(t, server, admin, http.MethodPost, "/api/workspaces", `{"accountId":"acct-alpha","ownerId":"usr-owner","workspaceName":"Alpha Lab","attachmentId":"attachment-from-fabric"}`)

	before := len(calls)
	response := requestWithSession(t, server, outsider, http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"`+stringValue(workspace["id"])+`"}`)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "not_authenticated") {
		t.Fatalf("cross-account runtime status = %d: %s", response.Code, response.Body.String())
	}
	if len(calls) != before {
		t.Fatalf("cross-account status reached Fabric Secret lookup: %#v", calls[before:])
	}
}

func TestWorkspaceListNeverExposesPersistedPassword(t *testing.T) {
	app := newControlPlaneApp()
	mustStore(t, app.tables.SaveWorkspace(context.Background(), map[string]any{
		"id": "ws-alpha", "accountId": "acct-alpha", "state": "running",
		"access": map[string]any{"username": "opl", "password": "must-not-leak", "secretRef": "opl-compute-alpha-env"},
	}))
	stored, err := app.tables.ListWorkspaces(context.Background(), "acct-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if password := stringValue(valueOrNil(stored[0], "access", "password")); password != "" {
		t.Fatalf("memory store retained Workspace password: %q", password)
	}
	workspace := app.state("acct-alpha", nil)["workspaces"].([]any)[0].(map[string]any)
	if password := stringValue(valueOrNil(workspace, "access", "password")); password != "" {
		t.Fatalf("Workspace list exposed password: %q", password)
	}
}

func TestPersistentFactsSurviveServerRestart(t *testing.T) {
	setTestOperatorAccount(t, "acct-alpha")
	path := t.TempDir() + "/control-plane-state.sqlite"
	service := controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{})
	server, err := NewPersistentServer(service, NewTestEntStateStore(t, path))
	if err != nil {
		t.Fatalf("create persistent server: %v", err)
	}
	session := tenantAdminSessionForTest(t, server)
	ensureCustomerMembershipForTest(t, server, session, "acct-alpha", "usr-admin")
	createResourceWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)

	restarted, err := NewPersistentServer(service, NewTestEntStateStore(t, path))
	if err != nil {
		t.Fatalf("restart persistent server: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-alpha", nil)
	addSessionCookies(req, tenantAdminSessionForTest(t, restarted))
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

func TestSessionFactSurvivesServerRestart(t *testing.T) {
	path := t.TempDir() + "/control-plane-state.sqlite"
	service := controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{})
	server, err := NewPersistentServer(service, NewTestEntStateStore(t, path))
	if err != nil {
		t.Fatalf("create persistent server: %v", err)
	}
	session := tenantAdminSessionForTest(t, server)
	ensureCustomerMembershipForTest(t, server, session, "acct-admin", "usr-admin")

	restarted, err := NewPersistentServer(service, NewTestEntStateStore(t, path))
	if err != nil {
		t.Fatalf("restart persistent server: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	addSessionCookies(req, session)
	rec := httptest.NewRecorder()
	restarted.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("session did not survive restart: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWorkspaceTokenStatePersistsAcrossRestart(t *testing.T) {
	setTestOperatorAccount(t, "acct-alpha")
	path := t.TempDir() + "/control-plane-state.sqlite"
	service := controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{})
	server, err := NewPersistentServer(service, NewTestEntStateStore(t, path))
	if err != nil {
		t.Fatalf("create persistent server: %v", err)
	}
	session := tenantAdminSessionForTest(t, server)
	ensureCustomerMembershipForTest(t, server, session, "acct-alpha", "usr-admin")
	compute := createResourceWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	storage := createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","sizeGb":10}`)
	createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-attachments", `{"workspaceId":"ws-alpha","computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`","mountPath":"/data"}`)
	workspace := createResourceWithSession(t, server, session, http.MethodPost, "/api/workspaces", `{"accountId":"acct-alpha","ownerId":"usr-owner","workspaceName":"Alpha Lab","attachmentId":"attachment-from-fabric"}`)
	createResourceWithSession(t, server, session, http.MethodPost, "/api/workspaces/delete-token", `{"workspaceId":"`+stringValue(workspace["id"])+`"}`)

	restarted, err := NewPersistentServer(service, NewTestEntStateStore(t, path))
	if err != nil {
		t.Fatalf("restart persistent server: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-alpha", nil)
	addSessionCookies(req, tenantAdminSessionForTest(t, restarted))
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
	mustStore(t, app.tables.SaveUser(context.Background(), map[string]any{"id": "usr-admin", "email": "admin@medopl.cn", "accountId": "acct-admin", "role": "admin", "status": "active", "passwordHash": "scrypt:00112233445566778899aabbccddeeff:4904ad313c8dcfe466e3babafef2471d2f5bcc7b0d4d893d5eb6c57666c8c5c1e9a26e8e1b9035f6625718daa983ae2798cbeb16b404e8418c901315147f642f"}))
	if _, _, err := app.login(map[string]any{"email": "admin@medopl.cn", "password": "legacy-secret"}); err != nil {
		t.Fatalf("legacy scrypt password did not verify: %v", err)
	}
}

func TestNonAdminRequestsCannotSelectAnotherAccount(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := operatorSessionForTest(t, server)
	alphaUser := createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"alpha@lab.example","accountId":"acct-alpha","role":"member","password":"CorrectHorseBatteryStaple!"}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/organizations/members", `{"organizationId":"org-alpha","userId":"`+stringValue(alphaUser["id"])+`","accountId":"acct-alpha","role":"member"}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"beta@lab.example","accountId":"acct-beta","role":"member","password":"CorrectHorseBatteryStaple!"}`)
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

	createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"pi@lab.example","accountId":"acct-alpha","role":"member","password":"CorrectHorseBatteryStaple!"}`)
	duplicate := requestWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"PI@lab.example","accountId":"acct-beta","role":"member","password":"CorrectHorseBatteryStaple!"}`)
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
	return clients.HoldResult{ID: "hold-" + input.ResourceType + "-" + input.ResourceID, AccountID: input.AccountID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, AmountCents: input.AmountCents, Wallet: clients.Wallet{AccountID: input.AccountID, BalanceCents: 20000, FrozenCents: input.AmountCents, AvailableCents: 20000 - input.AmountCents, Currency: "CNY"}}, nil
}

func (fakeLedgerClient) ReleaseHold(_ context.Context, input clients.HoldReleaseInput, _ string) (clients.HoldReleaseResult, error) {
	return clients.HoldReleaseResult{ID: "release-" + input.ResourceType + "-" + input.ResourceID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, HoldID: input.HoldID, AmountCents: input.AmountCents, Status: "released", Wallet: clients.Wallet{AccountID: input.AccountID, BalanceCents: 8800, FrozenCents: 0, AvailableCents: 8800, Currency: "CNY"}}, nil
}

func (fakeLedgerClient) RecordReceipt(_ context.Context, input clients.ReceiptInput, _ string) (clients.Receipt, error) {
	return clients.Receipt{ReceiptID: "receipt-from-ledger", WorkspaceID: input.WorkspaceID, ProjectID: input.ProjectID, TaskID: input.TaskID, RequestID: input.RequestID, ApprovalID: input.ApprovalID, JobID: input.JobID, ContinuationID: "continuation-from-ledger"}, nil
}

func (fakeLedgerClient) Receipt(_ context.Context, receiptID string) (clients.Receipt, error) {
	return clients.Receipt{ReceiptID: receiptID, Status: "completed", Execution: map[string]any{"jobStatus": "succeeded", "attempt": float64(1)}, ContinuationID: "continuation-from-ledger"}, nil
}

func (fakeLedgerClient) Artifact(_ context.Context, artifactID string) (clients.Artifact, error) {
	return clients.Artifact{ArtifactID: artifactID, JobID: "job-from-fabric", Digest: "sha256:artifact-alpha"}, nil
}

func (fakeLedgerClient) Review(_ context.Context, reviewID string) (clients.Review, error) {
	return clients.Review{ReviewID: reviewID, JobID: "job-from-fabric", Decision: "accepted", InputArtifactDigests: []string{"sha256:artifact-alpha"}}, nil
}

func (fakeLedgerClient) Continuation(_ context.Context, receiptID string) (map[string]any, error) {
	return map[string]any{"receiptId": receiptID, "continuationId": "continuation-from-ledger"}, nil
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

type walletSummaryLedgerClient struct {
	fakeLedgerClient
}

func (walletSummaryLedgerClient) Wallet(_ context.Context, accountID string) (clients.Wallet, error) {
	return clients.Wallet{AccountID: accountID, BalanceCents: 12345, FrozenCents: 345, AvailableCents: 12000, TotalSpentCents: 6789, Currency: "CNY"}, nil
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

type catalogFabricClient struct {
	fakeFabricClient
}

func (catalogFabricClient) Catalog(_ context.Context) (clients.FabricCatalog, error) {
	return clients.FabricCatalog{WorkspacePackages: []clients.FabricWorkspacePackage{{
		ID:               "ultra",
		Name:             "Ultra Workspace",
		ComputeProfileID: "pool-ultra",
		CPU:              16,
		MemoryGB:         32,
		DiskGB:           200,
		Provider:         "fabric-test",
		Available:        true,
	}}}, nil
}

type fakeFabricClient struct {
	calls   *[]string
	runtime clients.WorkspaceRuntime
}

type blockingResumeFabricClient struct {
	fakeFabricClient
	entered chan struct{}
	release chan struct{}
}

func (f *blockingResumeFabricClient) CreateWorkspaceRuntime(ctx context.Context, input clients.WorkspaceRuntimeInput, key string) (clients.WorkspaceRuntime, error) {
	f.entered <- struct{}{}
	select {
	case <-f.release:
		return f.fakeFabricClient.CreateWorkspaceRuntime(ctx, input, key)
	case <-ctx.Done():
		return clients.WorkspaceRuntime{}, ctx.Err()
	}
}

func (f *fakeFabricClient) record(call string) {
	if f != nil && f.calls != nil {
		*f.calls = append(*f.calls, call)
	}
}

func (f *fakeFabricClient) Catalog(_ context.Context) (clients.FabricCatalog, error) {
	f.record("fabric.catalog")
	return clients.FabricCatalog{WorkspacePackages: []clients.FabricWorkspacePackage{
		{ID: "basic", Name: "Basic Workspace", ComputeProfileID: "pool-basic", CPU: 2, MemoryGB: 4, DiskGB: 10, Provider: "tencent-tke", Available: true},
		{ID: "pro", Name: "Pro Workspace", ComputeProfileID: "pool-pro", CPU: 8, MemoryGB: 16, DiskGB: 100, Provider: "tencent-tke", Available: true},
	}}, nil
}

func (f *fakeFabricClient) CreateComputeAllocation(_ context.Context, input clients.ComputeAllocationInput, _ string) (clients.ComputeAllocation, error) {
	f.record("fabric.compute")
	return clients.ComputeAllocation{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID, Status: "running", Provider: "tencent-tke", ProviderResourceID: "node/node-from-fabric", ProviderRequestID: "compute-request-from-fabric", InstanceID: "ins-from-fabric", NodeName: "node-from-fabric", BillingStatus: "active", ServiceName: "opl-compute-from-fabric"}, nil
}

func (f *fakeFabricClient) GetComputeAllocation(_ context.Context, id string) (clients.ComputeAllocation, error) {
	f.record("fabric.compute-get")
	return clients.ComputeAllocation{ID: id, Status: "running", Provider: "tencent-tke", ProviderResourceID: "node/node-from-fabric", ProviderRequestID: "compute-request-from-fabric", InstanceID: "ins-from-fabric", NodeName: "node-from-fabric", BillingStatus: "active", ServiceName: "opl-compute-from-fabric"}, nil
}

func (f *fakeFabricClient) SyncComputeAllocation(_ context.Context, id string) (clients.ComputeAllocation, error) {
	f.record("fabric.compute-sync")
	return clients.ComputeAllocation{ID: id, Status: "external_deleted", Provider: "tencent-tke", ProviderRequestID: "compute-sync-from-fabric", BillingStatus: "stopped"}, nil
}

func (f *fakeFabricClient) DestroyComputeAllocation(_ context.Context, id string, _ string) (clients.ComputeAllocation, error) {
	f.record("fabric.compute-destroy")
	return clients.ComputeAllocation{ID: id, Status: "destroyed", Provider: "tencent-tke", ProviderRequestID: "compute-destroy-from-fabric", BillingStatus: "stopped"}, nil
}

func (f *fakeFabricClient) CreateStorageVolume(_ context.Context, input clients.StorageVolumeInput, _ string) (clients.StorageVolume, error) {
	f.record("fabric.storage")
	return clients.StorageVolume{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "available", Provider: "tencent-tke", ProviderResourceID: "pvc/volume-from-fabric-data", ProviderRequestID: "storage-request-from-fabric", SizeGB: input.SizeGB, StorageClass: "cbs", BillingStatus: "active"}, nil
}

func (f *fakeFabricClient) SyncStorageVolume(_ context.Context, id string) (clients.StorageVolume, error) {
	f.record("fabric.storage-sync")
	return clients.StorageVolume{ID: id, Status: "external_deleted", Provider: "tencent-tke", ProviderRequestID: "storage-sync-from-fabric", BillingStatus: "stopped"}, nil
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
	if f.runtime.ID != "" {
		return f.runtime, nil
	}
	return clients.WorkspaceRuntime{ID: "runtime-from-fabric", WorkspaceID: input.WorkspaceID, URL: "https://workspace.medopl.cn/w/ws-from-fabric/", Status: "running", ServiceName: "opl-compute-from-fabric", Access: clients.WorkspaceRuntimeAccess{Username: "admin", Password: "runtime-password-alpha", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-compute-from-fabric-env"}, Ready: true}, nil
}

func (f *fakeFabricClient) DestroyWorkspaceRuntime(_ context.Context, workspaceID, _ string) (clients.WorkspaceRuntime, error) {
	f.record("fabric.runtime-destroy")
	return clients.WorkspaceRuntime{WorkspaceID: workspaceID, Status: "destroyed"}, nil
}

func (f *fakeFabricClient) WorkspaceRuntimeStatus(_ context.Context, workspaceID string) (clients.WorkspaceRuntime, error) {
	f.record("fabric.runtime-status")
	return clients.WorkspaceRuntime{
		ID:          "runtime-from-fabric",
		WorkspaceID: workspaceID,
		URL:         "https://workspace.medopl.cn/w/" + workspaceID + "/",
		Status:      "unready",
		ServiceName: "opl-compute-from-fabric",
		Access:      clients.WorkspaceRuntimeAccess{Username: "opl", Password: "runtime-password-alpha", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-compute-from-fabric-env"},
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

func (f *fakeFabricClient) CreateJob(_ context.Context, input clients.JobInput, _ string) (clients.Job, error) {
	f.record("fabric.job")
	return clients.Job{JobID: "job-from-fabric", OrganizationID: input.OrganizationID, WorkspaceID: input.WorkspaceID, ProjectID: input.ProjectID, TaskID: input.TaskID, RequestID: input.RequestID, ApprovalID: input.ApprovalID, EnvironmentRef: input.EnvironmentRef, Status: "queued"}, nil
}

func (f *fakeFabricClient) GetJob(_ context.Context, jobID string) (clients.Job, error) {
	f.record("fabric.job-get")
	return clients.Job{JobID: jobID, Status: "queued"}, nil
}

func (f *fakeFabricClient) CancelJob(_ context.Context, jobID string, _ string) (clients.Job, error) {
	f.record("fabric.job-cancel")
	return clients.Job{JobID: jobID, Status: "cancelled"}, nil
}

func TestExecutionRoutesPersistCanonicalFlow(t *testing.T) {
	server := newExecutionTestServer(t, controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := tenantAdminSessionForTest(t, server)

	project := createResourceWithSession(t, server, admin, http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha","localAliasId":"local-project-alpha"}`)
	projectID := stringValue(project["projectId"])
	if !strings.HasPrefix(projectID, "project-") {
		t.Fatalf("unexpected project identity: %#v", project)
	}
	task := createResourceWithSession(t, server, admin, http.MethodPost, "/api/projects/"+projectID+"/tasks", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha","localAliasId":"local-task-alpha"}`)
	taskID := stringValue(task["taskId"])
	if !strings.HasPrefix(taskID, "task-") {
		t.Fatalf("unexpected task identity: %#v", task)
	}

	request := createResourceWithSession(t, server, admin, http.MethodPost, "/api/execution-requests", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"`+projectID+`","taskId":"`+taskID+`","environmentRef":"environment-alpha"}`)
	requestID := stringValue(request["requestId"])
	approved := createResourceWithSession(t, server, admin, http.MethodPost, "/api/execution-requests/"+requestID+"/approve", `{}`)
	if approved["approvalStatus"] != "approved" || stringValue(approved["approvalId"]) == "" {
		t.Fatalf("unexpected approval: %#v", approved)
	}
	executed := createResourceWithSession(t, server, admin, http.MethodPost, "/api/execution-requests/"+requestID+"/execute", `{}`)
	if executed["jobId"] != "job-from-fabric" || executed["receiptId"] != "receipt-from-ledger" || executed["continuationId"] != "continuation-from-ledger" {
		t.Fatalf("unexpected execution: %#v", executed)
	}

	loaded := createResourceWithSession(t, server, admin, http.MethodGet, "/api/execution-requests/"+requestID, ``)
	if loaded["status"] != "queued" || loaded["jobId"] != "job-from-fabric" || loaded["receiptId"] != "receipt-from-ledger" {
		t.Fatalf("unexpected persisted request: %#v", loaded)
	}
}

func TestProjectCreationRequiresWorkspaceOrganizationOwnership(t *testing.T) {
	store := newMemoryTableStore()
	mustStore(t, store.SaveAccount(context.Background(), map[string]any{"id": "acct-beta", "status": "active"}))
	mustStore(t, store.SaveOrganization(context.Background(), map[string]any{"id": "org-alpha", "billingAccountId": "acct-alpha", "status": "active"}))
	mustStore(t, store.SaveOrganization(context.Background(), map[string]any{"id": "org-beta", "billingAccountId": "acct-beta", "status": "active"}))
	mustStore(t, store.SaveMembership(context.Background(), map[string]any{"id": "mem-admin-alpha", "organizationId": "org-alpha", "userId": "usr-admin", "accountId": "acct-alpha", "role": "admin", "status": "active"}))
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{"id": "workspace-beta", "accountId": "acct-beta", "status": "running"}))
	passwordHash, err := hashPassword("CorrectHorseBatteryStaple!")
	if err != nil {
		t.Fatal(err)
	}
	mustStore(t, store.SaveUser(context.Background(), map[string]any{"id": "usr-beta", "email": "beta-admin@example.com", "accountId": "acct-beta", "role": "admin", "status": "active", "passwordHash": passwordHash}))
	mustStore(t, store.SaveMembership(context.Background(), map[string]any{"id": "mem-admin-beta", "organizationId": "org-beta", "userId": "usr-beta", "accountId": "acct-beta", "role": "admin", "status": "active"}))
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	admin := tenantAdminSessionForTest(t, server)

	forbidden := requestWithSession(t, server, admin, http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-beta"}`)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("cross-organization status = %d, want %d: %s", forbidden.Code, http.StatusForbidden, forbidden.Body.String())
	}
	heads, err := store.ListProjectTaskSyncHeads(context.Background())
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(heads) != 0 {
		t.Fatalf("cross-organization request persisted projects: %#v", heads)
	}

	betaAdmin := loginForTest(t, server, "beta-admin@example.com", "CorrectHorseBatteryStaple!")
	created := requestWithSession(t, server, betaAdmin, http.MethodPost, "/api/projects", `{"organizationId":"org-beta","workspaceId":"workspace-beta"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("same-organization status = %d, want %d: %s", created.Code, http.StatusCreated, created.Body.String())
	}
}

func TestProjectCreationReportsIdentityStoreFailures(t *testing.T) {
	for _, tc := range []struct {
		name            string
		workspaceErr    error
		organizationErr error
	}{
		{name: "workspace read", workspaceErr: errors.New("workspace store unavailable")},
		{name: "organization read", organizationErr: errors.New("organization store unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &failingProjectIdentityStore{memoryTableStore: newMemoryTableStore()}
			hash, err := hashPassword("CorrectHorseBatteryStaple!")
			if err != nil {
				t.Fatal(err)
			}
			mustStore(t, store.SaveUser(context.Background(), map[string]any{"id": "usr-project-admin", "email": "project-admin@example.com", "accountId": "acct-alpha", "role": "admin", "status": "active", "passwordHash": hash}))
			mustStore(t, store.SaveOrganization(context.Background(), map[string]any{"id": "org-alpha", "billingAccountId": "acct-alpha", "status": "active"}))
			mustStore(t, store.SaveMembership(context.Background(), map[string]any{"id": "mem-project-admin", "organizationId": "org-alpha", "userId": "usr-project-admin", "accountId": "acct-alpha", "role": "admin", "status": "active"}))
			mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{"id": "workspace-alpha", "accountId": "acct-alpha", "status": "running"}))
			server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}), store)
			if err != nil {
				t.Fatalf("create server: %v", err)
			}

			session := loginForTest(t, server, "project-admin@example.com", "CorrectHorseBatteryStaple!")
			store.workspaceErr, store.organizationErr = tc.workspaceErr, tc.organizationErr
			rec := requestWithSession(t, server, session, http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`)
			wantStatus, wantError := http.StatusInternalServerError, "state_read_failed"
			if tc.organizationErr != nil {
				wantStatus, wantError = http.StatusUnauthorized, "not_authenticated"
			}
			if rec.Code != wantStatus || !strings.Contains(rec.Body.String(), wantError) {
				t.Fatalf("status = %d body=%s, want state_read_failed", rec.Code, rec.Body.String())
			}
			heads, err := store.ListProjectTaskSyncHeads(context.Background())
			if err != nil {
				t.Fatalf("list projects: %v", err)
			}
			if len(heads) != 0 {
				t.Fatalf("failed identity read persisted projects: %#v", heads)
			}
		})
	}
}

func TestProjectCreationReportsMissingIdentity(t *testing.T) {
	for _, tc := range []struct {
		name         string
		organization map[string]any
		workspace    map[string]any
		errorCode    string
	}{
		{name: "workspace", organization: map[string]any{"id": "org-alpha", "billingAccountId": "acct-alpha", "status": "active"}, errorCode: "workspace_not_found"},
		{name: "organization", workspace: map[string]any{"id": "workspace-alpha", "accountId": "acct-alpha", "status": "running"}, errorCode: "organization_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryTableStore()
			if tc.organization == nil {
				delete(store.organizations, "org-alpha")
				delete(store.memberships, "mem-admin-alpha")
			}
			if tc.organization != nil {
				mustStore(t, store.SaveOrganization(context.Background(), tc.organization))
				mustStore(t, store.SaveMembership(context.Background(), map[string]any{"id": "mem-admin-alpha", "organizationId": "org-alpha", "userId": "usr-admin", "accountId": "acct-alpha", "role": "admin", "status": "active"}))
			}
			if tc.workspace != nil {
				mustStore(t, store.SaveWorkspace(context.Background(), tc.workspace))
			}
			server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}), store)
			if err != nil {
				t.Fatalf("create server: %v", err)
			}

			rec := requestWithSession(t, server, tenantAdminSessionForTest(t, server), http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`)
			wantStatus := http.StatusNotFound
			if tc.organization == nil {
				wantStatus, tc.errorCode = http.StatusForbidden, "organization_membership_required"
			}
			if rec.Code != wantStatus || !strings.Contains(rec.Body.String(), tc.errorCode) {
				t.Fatalf("status = %d body=%s, want %s", rec.Code, rec.Body.String(), tc.errorCode)
			}
			heads, err := store.ListProjectTaskSyncHeads(context.Background())
			if err != nil {
				t.Fatalf("list projects: %v", err)
			}
			if len(heads) != 0 {
				t.Fatalf("missing identity persisted projects: %#v", heads)
			}
		})
	}
}

type executionCompletionLedgerClient struct {
	fakeLedgerClient
}

func (*executionCompletionLedgerClient) RecordReceipt(_ context.Context, input clients.ReceiptInput, _ string) (clients.Receipt, error) {
	receiptID := "receipt-running"
	continuationID := ""
	if input.Status != "running" {
		receiptID = "receipt-final"
	}
	if input.Status == "completed" {
		continuationID = "continuation-final"
	}
	return clients.Receipt{ReceiptID: receiptID, Status: input.Status, WorkspaceID: input.WorkspaceID, ProjectID: input.ProjectID, TaskID: input.TaskID, RequestID: input.RequestID, ApprovalID: input.ApprovalID, JobID: input.JobID, ContinuationID: continuationID}, nil
}

func (*executionCompletionLedgerClient) Continuation(_ context.Context, receiptID string) (map[string]any, error) {
	return map[string]any{"receiptId": receiptID, "continuationId": "continuation-final", "artifactIds": []any{"artifact-alpha"}}, nil
}

type completedExecutionFabricClient struct {
	fakeFabricClient
}

func (f *completedExecutionFabricClient) GetJob(_ context.Context, jobID string) (clients.Job, error) {
	f.record("fabric.job-get")
	return clients.Job{JobID: jobID, Status: "succeeded", Attempt: 1, ArtifactIDs: []string{"artifact-alpha"}, ReviewIDs: []string{"review-alpha"}}, nil
}

func TestOrganizationMemberSyncsExecutionAndReadsContinuation(t *testing.T) {
	server := newExecutionTestServer(t, controlplane.NewService(&executionCompletionLedgerClient{}, &completedExecutionFabricClient{}))
	admin := operatorSessionForTest(t, server)
	memberUser := createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"member@execution.example","accountId":"acct-alpha","role":"member","password":"CorrectHorseBatteryStaple!"}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/organizations/members", `{"organizationId":"org-alpha","userId":"`+stringValue(memberUser["id"])+`","accountId":"acct-alpha","role":"member"}`)
	member := loginForTest(t, server, "member@execution.example", "CorrectHorseBatteryStaple!")

	project := createResourceWithSession(t, server, member, http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`)
	projectID := stringValue(project["projectId"])
	task := createResourceWithSession(t, server, member, http.MethodPost, "/api/projects/"+projectID+"/tasks", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`)
	request := createResourceWithSession(t, server, member, http.MethodPost, "/api/execution-requests", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"`+projectID+`","taskId":"`+stringValue(task["taskId"])+`"}`)
	requestID := stringValue(request["requestId"])
	createResourceWithSession(t, server, member, http.MethodPost, "/api/execution-requests/"+requestID+"/approve", `{}`)
	createResourceWithSession(t, server, member, http.MethodPost, "/api/execution-requests/"+requestID+"/execute", `{}`)
	synced := createResourceWithSession(t, server, member, http.MethodPost, "/api/execution-requests/"+requestID+"/sync", `{}`)
	if synced["status"] != "completed" || synced["receiptId"] != "receipt-final" || synced["continuationId"] != "continuation-final" {
		t.Fatalf("unexpected synced execution: %#v", synced)
	}

	continuationRec := requestWithSession(t, server, member, http.MethodGet, "/api/execution-requests/"+requestID+"/continuation", "")
	if continuationRec.Code != http.StatusOK || !strings.Contains(continuationRec.Body.String(), "continuation-final") {
		t.Fatalf("continuation status = %d: %s", continuationRec.Code, continuationRec.Body.String())
	}

	createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"outside-continuation@example.com","accountId":"acct-beta","role":"member","password":"CorrectHorseBatteryStaple!"}`)
	outsider := loginForTest(t, server, "outside-continuation@example.com", "CorrectHorseBatteryStaple!")
	forbidden := requestWithSession(t, server, outsider, http.MethodGet, "/api/execution-requests/"+requestID+"/continuation", "")
	if forbidden.Code != http.StatusUnauthorized {
		t.Fatalf("outsider continuation status = %d, want %d: %s", forbidden.Code, http.StatusUnauthorized, forbidden.Body.String())
	}
}

func TestProjectIdentityRequiresIdempotencyKey(t *testing.T) {
	server := newExecutionTestServer(t, controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := tenantAdminSessionForTest(t, server)
	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewBufferString(`{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`))
	req.Header.Set("Content-Type", "application/json")
	addAuth(req, admin)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "missing Idempotency-Key") {
		t.Fatalf("status = %d body=%s, want missing Idempotency-Key", rec.Code, rec.Body.String())
	}
}

func TestExecutionRequestSameKeyDifferentPayloadConflicts(t *testing.T) {
	server := newExecutionTestServer(t, controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := tenantAdminSessionForTest(t, server)
	project := createResourceWithSession(t, server, admin, http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`)
	projectID := stringValue(project["projectId"])
	task := createResourceWithSession(t, server, admin, http.MethodPost, "/api/projects/"+projectID+"/tasks", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`)
	taskID := stringValue(task["taskId"])
	path := "/api/execution-requests"
	body := `{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"` + projectID + `","taskId":"` + taskID + `","environmentRef":"environment-alpha"}`
	first := requestWithSession(t, server, admin, http.MethodPost, path, body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d: %s", first.Code, first.Body.String())
	}
	second := requestWithSession(t, server, admin, http.MethodPost, path, strings.Replace(body, "environment-alpha", "environment-beta", 1))
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "idempotency_conflict") {
		t.Fatalf("second status = %d body=%s, want idempotency conflict", second.Code, second.Body.String())
	}
}

func TestExecutionRoutesAuthorizeActiveOrganizationMembers(t *testing.T) {
	server := newExecutionTestServer(t, controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := operatorSessionForTest(t, server)
	piUser := createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"pi@execution.example","accountId":"acct-alpha","role":"member","password":"CorrectHorseBatteryStaple!"}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/organizations/members", `{"organizationId":"org-alpha","userId":"`+stringValue(piUser["id"])+`","accountId":"acct-alpha","role":"member"}`)
	pi := loginForTest(t, server, "pi@execution.example", "CorrectHorseBatteryStaple!")
	rec := requestWithSession(t, server, pi, http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("member status = %d body=%s, want created", rec.Code, rec.Body.String())
	}

	createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"outsider@execution.example","accountId":"acct-beta","role":"member","password":"CorrectHorseBatteryStaple!"}`)
	outsider := loginForTest(t, server, "outsider@execution.example", "CorrectHorseBatteryStaple!")
	forbidden := requestWithSession(t, server, outsider, http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`)
	if forbidden.Code != http.StatusUnauthorized || !strings.Contains(forbidden.Body.String(), "not_authenticated") {
		t.Fatalf("outsider status = %d body=%s, want not_authenticated", forbidden.Code, forbidden.Body.String())
	}
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

type fabricClientWithUnscopedHistoricOperation struct {
	fakeFabricClient
}

func (f *fabricClientWithUnscopedHistoricOperation) ListOperations(_ context.Context) ([]clients.FabricOperation, error) {
	return []clients.FabricOperation{{
		ID:           "fop-historic-compute",
		OperationID:  "op-historic-compute",
		Action:       "create_compute_allocation",
		ResourceKind: "compute_allocation",
		ResourceID:   "compute-historic",
		RedactedProviderPayload: map[string]any{"resource": map[string]any{
			"id":     "compute-historic",
			"status": "running",
		}},
		Status: "succeeded",
	}}, nil
}

func createResource(t *testing.T, server http.Handler, method string, path string, body string) map[string]any {
	t.Helper()
	session := operatorSessionForTest(t, server)
	if !explicitOperatorTestPath(path) {
		session = tenantAdminSessionForTest(t, server)
	}
	return createResourceWithSession(t, server, session, method, path, body)
}

func explicitOperatorTestPath(path string) bool {
	return strings.HasPrefix(path, "/api/users") || strings.HasPrefix(path, "/api/organizations") || strings.HasPrefix(path, "/api/operator") || strings.HasPrefix(path, "/api/management") || strings.HasPrefix(path, "/api/billing/topups") || strings.HasPrefix(path, "/api/billing/resource-settlements") || strings.HasPrefix(path, "/api/billing/reconciliation")
}

func createResourceWithSession(t *testing.T, server http.Handler, loginRec *httptest.ResponseRecorder, method string, path string, body string) map[string]any {
	t.Helper()
	if explicitOperatorTestPath(path) {
		loginRec = reservedOperatorSessionForTest(t, server)
	}
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
	return reservedOperatorSessionForTest(t, server)
}

func tenantAdminSessionForTest(t *testing.T, server http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	global := reservedOperatorSessionForTest(t, server)
	email := "tenant-admin-" + newResourceID("test") + "@example.com"
	user := createResourceWithSession(t, server, global, http.MethodPost, "/api/users", `{"email":"`+email+`","accountId":"acct-alpha","role":"admin","password":"CorrectHorseBatteryStaple!"}`)
	membership := requestWithSession(t, server, global, http.MethodPost, "/api/organizations/members", `{"organizationId":"org-alpha","userId":"`+stringValue(user["id"])+`","accountId":"acct-alpha","role":"admin"}`)
	if membership.Code < 200 || membership.Code >= 300 {
		organization := createResourceWithSession(t, server, global, http.MethodPost, "/api/organizations", `{"name":"Test tenant","billingAccountId":"acct-alpha"}`)
		createResourceWithSession(t, server, global, http.MethodPost, "/api/organizations/members", `{"organizationId":"`+stringValue(organization["id"])+`","userId":"`+stringValue(user["id"])+`","accountId":"acct-alpha","role":"admin"}`)
	}
	return loginForTest(t, server, email, "CorrectHorseBatteryStaple!")
}

func reservedOperatorSessionForTest(t *testing.T, server http.Handler) *httptest.ResponseRecorder {
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
	addAuth(req, tenantAdminSessionForTest(t, server))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if stringValue(body["id"]) == "" || body["providerRequestId"] != "compute-request-from-fabric" || body["holdId"] != "hold-compute-"+stringValue(body["id"]) {
		t.Fatalf("compute allocation did not come from Fabric: %#v", body)
	}
	if body["provider"] != "tencent-tke" || body["billingStatus"] != "active" || body["nodeName"] != "node-from-fabric" || body["instanceId"] != "ins-from-fabric" {
		t.Fatalf("compute allocation missing route contract fields: %#v", body)
	}
	getReq := httptest.NewRequest(http.MethodGet, "/api/compute-allocations/"+stringValue(body["id"]), nil)
	addSessionCookies(getReq, tenantAdminSessionForTest(t, server))
	getRec := httptest.NewRecorder()
	server.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d: %s", getRec.Code, http.StatusOK, getRec.Body.String())
	}
}

func TestStorageResponseActivatesBillingWhenProviderIsAvailable(t *testing.T) {
	body := storageResponse(map[string]any{"id": "storage-alpha", "status": "ready", "billingStatus": "pending"})
	if body["status"] != "available" || body["billingStatus"] != "active" {
		t.Fatalf("storage response should activate available provider resource, got %#v", body)
	}
}

func TestTerminalResourceStatusStopsBilling(t *testing.T) {
	body := computeResponse(map[string]any{"id": "compute-alpha", "status": "destroyed", "billingStatus": "active"})
	if body["billingStatus"] != "stopped" {
		t.Fatalf("terminal resource should stop billing, got %#v", body)
	}
}

func TestSyncComputeAllocationExternalDeleteStopsBillingAndSuspendsWorkspace(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := tenantAdminSessionForTest(t, server)
	compute := createResourceWithSession(t, server, admin, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","packageId":"basic"}`)
	storage := createResourceWithSession(t, server, admin, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","sizeGb":10}`)
	attachment := createResourceWithSession(t, server, admin, http.MethodPost, "/api/storage-attachments", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`"}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/workspaces", `{"accountId":"acct-alpha","ownerId":"usr-alpha","workspaceId":"ws-alpha","attachmentId":"`+stringValue(attachment["id"])+`","name":"Alpha"}`)

	syncRec := requestWithSession(t, server, admin, http.MethodPost, "/api/compute-allocations/"+stringValue(compute["id"])+"/sync", `{}`)
	if syncRec.Code != http.StatusOK {
		t.Fatalf("sync status = %d, want %d: %s", syncRec.Code, http.StatusOK, syncRec.Body.String())
	}
	var synced map[string]any
	if err := json.NewDecoder(syncRec.Body).Decode(&synced); err != nil {
		t.Fatalf("decode sync: %v", err)
	}
	if synced["status"] != "external_deleted" || synced["billingStatus"] != "stopped" || synced["holdReleaseId"] != "release-compute-"+stringValue(compute["id"]) {
		t.Fatalf("sync must return stopped backend facts: %#v", synced)
	}

	stateRec := requestWithSession(t, server, admin, http.MethodGet, "/api/state?accountId=acct-alpha", ``)
	if stateRec.Code != http.StatusOK {
		t.Fatalf("state status = %d, want %d: %s", stateRec.Code, http.StatusOK, stateRec.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(stateRec.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	workspace := state["workspaces"].([]any)[0].(map[string]any)
	if workspace["state"] != "suspended" || workspace["currentComputeAllocationId"] != "" {
		t.Fatalf("workspace must be suspended after provider deletion: %#v", workspace)
	}
}

func TestSyncStorageVolumeExternalDeleteStopsBillingAndDeletesWorkspaceData(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := tenantAdminSessionForTest(t, server)
	compute := createResourceWithSession(t, server, admin, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","packageId":"basic"}`)
	storage := createResourceWithSession(t, server, admin, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","sizeGb":10}`)
	attachment := createResourceWithSession(t, server, admin, http.MethodPost, "/api/storage-attachments", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`"}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/workspaces", `{"accountId":"acct-alpha","ownerId":"usr-alpha","workspaceId":"ws-alpha","attachmentId":"`+stringValue(attachment["id"])+`","name":"Alpha"}`)

	syncRec := requestWithSession(t, server, admin, http.MethodPost, "/api/storage-volumes/"+stringValue(storage["id"])+"/sync", `{}`)
	if syncRec.Code != http.StatusOK {
		t.Fatalf("sync status = %d, want %d: %s", syncRec.Code, http.StatusOK, syncRec.Body.String())
	}
	var synced map[string]any
	if err := json.NewDecoder(syncRec.Body).Decode(&synced); err != nil {
		t.Fatalf("decode sync: %v", err)
	}
	if synced["status"] != "external_deleted" || synced["billingStatus"] != "stopped" || synced["holdReleaseId"] != "release-storage-"+stringValue(storage["id"]) {
		t.Fatalf("sync must return stopped backend facts: %#v", synced)
	}

	stateRec := requestWithSession(t, server, admin, http.MethodGet, "/api/state?accountId=acct-alpha", ``)
	if stateRec.Code != http.StatusOK {
		t.Fatalf("state status = %d, want %d: %s", stateRec.Code, http.StatusOK, stateRec.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(stateRec.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	workspace := state["workspaces"].([]any)[0].(map[string]any)
	if workspace["state"] != "data_deleted" || workspace["status"] != "unrecoverable" {
		t.Fatalf("workspace data must be marked deleted after provider storage deletion: %#v", workspace)
	}
}

func TestManagementStateIncludesResourceLedgerEvidenceChain(t *testing.T) {
	app := newControlPlaneApp()
	mustStore(t, app.tables.SaveWorkspace(context.Background(), map[string]any{
		"id":                         "ws-alpha",
		"ownerAccountId":             "acct-alpha",
		"ownerUserId":                "usr-alpha",
		"currentComputeAllocationId": "compute-alpha",
		"currentAttachmentId":        "attach-alpha",
		"storageId":                  "storage-alpha",
	}))
	mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{
		"id":             "compute-alpha",
		"ownerAccountId": "acct-alpha",
		"ownerUserId":    "usr-alpha",
		"cvmInstanceId":  "ins-alpha",
		"nodeName":       "node-alpha",
	}))
	mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{
		"id":             "storage-alpha",
		"ownerAccountId": "acct-alpha",
	}))
	ledger := app.addLedgerLocked("acct-alpha", "compute_debit", map[string]any{"workspaceId": "ws-alpha", "computeAllocationId": "compute-alpha"})
	app.addWalletTxLocked("acct-alpha", "compute_debit", map[string]any{"workspaceId": "ws-alpha", "computeAllocationId": "compute-alpha"})
	wallets, err := app.tables.ListWalletTransactions(context.Background(), "acct-alpha")
	mustStore(t, err)
	wallet := wallets[len(wallets)-1]

	state := app.managementState(true, nil)
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
	mustStore(t, app.tables.SaveWorkspace(context.Background(), map[string]any{
		"id":                         "ws-replacement",
		"ownerAccountId":             "acct-alpha",
		"currentComputeAllocationId": "compute-replacement",
		"currentAttachmentId":        "attach-replacement",
		"storageId":                  "storage-alpha",
	}))
	mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{"id": "compute-replacement", "ownerAccountId": "acct-alpha", "status": "running", "billingStatus": "active", "hourlyPrice": 1.25}))
	mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{"id": "storage-alpha", "ownerAccountId": "acct-alpha", "status": "available", "billingStatus": "active", "hourlyEstimate": 0.25}))
	mustStore(t, app.tables.SaveAttachment(context.Background(), map[string]any{"id": "attach-replacement", "ownerAccountId": "acct-alpha"}))
	mustStore(t, app.tables.SaveRuntimeOperation(context.Background(), map[string]any{
		"id":           "fabric-op-runtime-replacement",
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
	}))
	app.mu.Lock()
	computeLedger := app.addLedgerLocked("acct-alpha", "compute_debit", map[string]any{"workspaceId": "ws-replacement", "computeAllocationId": "compute-replacement", "amountCents": -250})
	storageLedger := app.addLedgerLocked("acct-alpha", "storage_debit", map[string]any{"workspaceId": "ws-replacement", "storageId": "storage-alpha", "amountCents": -125})
	app.addWalletTxLocked("acct-alpha", "compute_debit", map[string]any{"workspaceId": "ws-replacement", "computeAllocationId": "compute-replacement", "amountCents": -250})
	app.addWalletTxLocked("acct-alpha", "storage_debit", map[string]any{"workspaceId": "ws-replacement", "storageId": "storage-alpha", "amountCents": -125})
	app.mu.Unlock()

	state := app.state("acct-alpha", nil)
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

func TestResourceLedgerEvidenceDerivesProviderCostTags(t *testing.T) {
	app := newControlPlaneApp()
	mustStore(t, app.tables.SaveWorkspace(context.Background(), map[string]any{
		"id":                         "ws-alpha",
		"ownerAccountId":             "acct-alpha",
		"currentComputeAllocationId": "compute-alpha",
		"currentAttachmentId":        "attach-alpha",
		"storageId":                  "storage-alpha",
	}))
	mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{"id": "compute-alpha", "ownerAccountId": "acct-alpha"}))
	mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{"id": "storage-alpha", "ownerAccountId": "acct-alpha"}))
	mustStore(t, app.tables.SaveAttachment(context.Background(), map[string]any{"id": "attach-alpha", "ownerAccountId": "acct-alpha"}))
	mustStore(t, app.tables.SaveRuntimeOperation(context.Background(), map[string]any{
		"id":           "fabric-op-runtime-alpha",
		"operationId":  "op-runtime-alpha",
		"resourceKind": "workspace_runtime",
		"resourceId":   "ws-alpha",
		"workspaceId":  "ws-alpha",
		"status":       "succeeded",
	}))

	row := app.state("acct-alpha", nil)["resourceLedgerEvidence"].([]any)[0].(map[string]any)
	tags, _ := row["costTags"].(map[string]any)
	if tags["opl_account_id"] != "acct-alpha" || tags["opl_workspace_id"] != "ws-alpha" || tags["opl_resource_id"] != "ws-alpha" || tags["opl_operation_id"] != "op-runtime-alpha" {
		t.Fatalf("row missing derived provider cost tags: %#v", row)
	}
}

func TestResourceDestroyAndDetachUpdateWorkspaceState(t *testing.T) {
	app := newControlPlaneApp()
	mustStore(t, app.tables.SaveWorkspace(context.Background(), map[string]any{
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
	}))

	mustStore(t, app.saveComputeFact(map[string]any{
		"id":              "compute-alpha",
		"accountId":       "acct-alpha",
		"status":          "destroyed",
		"holdId":          "hold-compute",
		"holdReleaseId":   "release-compute",
		"holdAmountCents": 7862,
		"wallet":          map[string]any{"accountId": "acct-alpha", "balanceCents": 20000, "frozenCents": 0, "availableCents": 20000, "currency": "CNY"},
	}))
	workspace := storedWorkspace(t, app, "ws-alpha")
	if workspace["state"] != "suspended" || workspace["currentComputeAllocationId"] != "" {
		t.Fatalf("compute destroy did not suspend and clear compute pointer: %#v", workspace)
	}

	mustStore(t, app.saveAttachmentFact(map[string]any{"id": "attach-alpha", "status": "detached"}, map[string]any{}))
	workspace = storedWorkspace(t, app, "ws-alpha")
	if workspace["currentAttachmentId"] != "" || workspace["attachmentId"] != "" {
		t.Fatalf("attachment detach did not clear workspace pointer: %#v", workspace)
	}

	mustStore(t, app.saveStorageFact(map[string]any{
		"id":              "storage-alpha",
		"accountId":       "acct-alpha",
		"status":          "destroyed",
		"holdId":          "hold-storage",
		"holdReleaseId":   "release-storage",
		"holdAmountCents": 101,
		"wallet":          map[string]any{"accountId": "acct-alpha", "balanceCents": 20000, "frozenCents": 0, "availableCents": 20000, "currency": "CNY"},
	}))
	workspace = storedWorkspace(t, app, "ws-alpha")
	access, ok := workspace["access"].(map[string]any)
	if workspace["state"] != "data_deleted" || workspace["status"] != "unrecoverable" || !ok || access["tokenStatus"] != "disabled" {
		t.Fatalf("storage destroy did not mark workspace unrecoverable: %#v", workspace)
	}
	ledger, err := app.tables.ListLedger(context.Background(), "acct-alpha")
	mustStore(t, err)
	if len(ledger) != 2 || ledger[0]["type"] != "compute_hold_released" || ledger[1]["type"] != "storage_hold_released" {
		t.Fatalf("missing hold release ledger projection: %#v", ledger)
	}
}

func TestDetachStorageAttachmentPreservesOwnershipFacts(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := tenantAdminSessionForTest(t, server)

	compute := createResourceWithSession(t, server, admin, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic","name":"compute-alpha"}`)
	storage := createResourceWithSession(t, server, admin, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","packageId":"basic","name":"storage-alpha"}`)
	attachment := createResourceWithSession(t, server, admin, http.MethodPost, "/api/storage-attachments", `{"accountId":"acct-alpha","computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`","mountPath":"/data"}`)

	detached := createResourceWithSession(t, server, admin, http.MethodPost, "/api/storage-attachments/detach", `{"attachmentId":"`+stringValue(attachment["id"])+`"}`)
	if detached["status"] != "detached" || detached["accountId"] != "acct-alpha" || detached["computeAllocationId"] != compute["id"] || detached["storageId"] != storage["id"] {
		t.Fatalf("detach should preserve ownership facts, got %#v", detached)
	}
}

func TestRememberAttachmentDerivesAccountFromLinkedResources(t *testing.T) {
	app := newControlPlaneApp()
	mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{"id": "compute-alpha", "accountId": "acct-alpha"}))
	mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{"id": "storage-alpha", "accountId": "acct-alpha"}))
	if err := app.saveAttachmentFact(map[string]any{
		"id":                  "attach-alpha",
		"computeAllocationId": "compute-alpha",
		"storageId":           "storage-alpha",
		"status":              "attached",
	}, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if got := stringValue(storedAttachment(t, app, "attach-alpha")["accountId"]); got != "acct-alpha" {
		t.Fatalf("attachment accountId = %q, want acct-alpha", got)
	}
}

func TestPersistDerivesAttachmentAccountFromExistingFacts(t *testing.T) {
	app := newControlPlaneApp()
	app.store = NewTestEntStateStore(t, t.TempDir()+"/attachment-account.sqlite")
	app.tables = app.store.(controlPlaneTableStore)
	mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{"id": "compute-alpha", "accountId": "acct-alpha"}))
	mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{"id": "storage-alpha", "accountId": "acct-alpha"}))
	mustStore(t, app.saveAttachmentFact(map[string]any{
		"id":                  "attach-alpha",
		"computeAllocationId": "compute-alpha",
		"storageId":           "storage-alpha",
		"status":              "attached",
	}, map[string]any{}))
	attachments, err := app.tables.ListAttachments(context.Background(), "")
	mustStore(t, err)
	if got := stringValue(attachments[0]["accountId"]); got != "acct-alpha" {
		t.Fatalf("persisted attachment accountId = %q, want acct-alpha", got)
	}
}

func TestManagementStateUsesRealAccountsAndLedger(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))

	createResource(t, server, http.MethodPost, "/api/users", `{"email":"owner@lab.example","accountId":"acct-alpha","role":"member","password":"CorrectHorseBatteryStaple!"}`)
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
		return account["accountId"] == "acct-alpha" && number(account["totalSpent"]) > 0
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

func TestOperatorAccountTotalsIgnoreDeletedUserWalletResiduals(t *testing.T) {
	app := newControlPlaneApp()
	mustStore(t, app.tables.SaveUser(context.Background(), map[string]any{"id": "usr-active", "accountId": "acct-active", "role": "member", "status": "active", "email": "active@example.test"}))
	mustStore(t, app.tables.SaveUser(context.Background(), map[string]any{"id": "usr-deleted", "accountId": "acct-deleted", "role": "member", "status": "deleted", "email": "deleted@example.test"}))
	mustStore(t, app.tables.SaveWallet(context.Background(), map[string]any{"id": "acct-active", "accountId": "acct-active", "balance": 10.0, "frozen": 2.0, "totalSpent": 3.0}))
	mustStore(t, app.tables.SaveWallet(context.Background(), map[string]any{"id": "acct-deleted", "accountId": "acct-deleted", "balance": 99.0, "frozen": 88.0, "totalSpent": 77.0}))
	mustStore(t, app.tables.SaveWallet(context.Background(), map[string]any{"id": "acct-wallet-only", "accountId": "acct-wallet-only", "balance": 50.0, "frozen": 40.0, "totalSpent": 30.0}))
	summary := app.operatorSummary()

	accounts := summary["accounts"].(map[string]any)
	if accounts["total"] != 2 {
		t.Fatalf("operator account count should ignore deleted/userless wallet residuals: %#v", accounts)
	}
	if number(accounts["frozen"]) != 2 {
		t.Fatalf("operator frozen total should use active backend account wallets only: %#v", accounts)
	}
	if number(accounts["totalSpent"]) != 3 {
		t.Fatalf("operator spent total should use wallet totalSpent without double-counting ledger rows: %#v", accounts)
	}
}

func TestCleanupWorkspaceAccessDisablesInvalidActiveURL(t *testing.T) {
	app := newControlPlaneApp()
	mustStore(t, app.tables.SaveWorkspace(context.Background(), map[string]any{
		"id":             "ws-alpha",
		"ownerAccountId": "acct-alpha",
		"storageId":      "missing-storage",
		"access":         map[string]any{"tokenStatus": "active"},
	}))

	result, err := app.cleanupWorkspaceAccess(map[string]any{"reason": "test"})
	if err != nil {
		t.Fatalf("cleanup workspace access: %v", err)
	}
	workspace := storedWorkspace(t, app, "ws-alpha")
	if len(result["cleaned"].([]any)) != 1 || nested(workspace, "access", "tokenStatus") != "disabled" {
		t.Fatalf("cleanup did not disable invalid URL: result=%#v workspace=%#v", result, workspace)
	}
}

func TestArchiveTerminalResourcesRemovesCurrentStateWithoutLedger(t *testing.T) {
	app := newControlPlaneApp()
	mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{"id": "compute-dead", "status": "destroyed"}))
	mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{"id": "storage-dead", "status": "destroyed"}))
	mustStore(t, app.tables.SaveAttachment(context.Background(), map[string]any{"id": "attach-dead", "status": "detached"}))
	mustStore(t, app.tables.SaveWorkspace(context.Background(), map[string]any{"id": "ws-dead", "state": "unrecoverable"}))
	mustStore(t, app.tables.SaveLedgerEntry(context.Background(), map[string]any{"id": "ledger-kept"}))

	result, err := app.archiveTerminalResources(context.Background(), map[string]any{"reason": "test"})
	if err != nil {
		t.Fatalf("archive terminal resources: %v", err)
	}
	if result["currentStateRemoved"] != 4 {
		t.Fatalf("archive removed count = %#v, want 4", result)
	}
	if len(app.listComputes("")) != 0 || len(app.listStorages("")) != 0 || len(app.listAttachments("")) != 0 || len(app.listWorkspaces("")) != 0 {
		t.Fatalf("terminal resources still in current state")
	}
	ledger, err := app.tables.ListLedger(context.Background(), "")
	mustStore(t, err)
	if len(ledger) != 1 {
		t.Fatalf("archive must not remove ledger facts: %#v", ledger)
	}
}

func TestArchiveStateEndpointReturnsBackendArchiveAndRetentionPolicy(t *testing.T) {
	path := t.TempDir() + "/control-plane-state.sqlite"
	store := NewTestEntStateStore(t, path)
	if err := store.SaveCompute(context.Background(), map[string]any{"id": "compute-dead", "accountId": "acct-alpha", "status": "destroyed"}); err != nil {
		t.Fatalf("seed terminal compute: %v", err)
	}
	service := controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{})
	server, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	admin := operatorSessionForTest(t, server)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/operator/archive-terminal-resources", `{"confirm":true,"reason":"test_archive_query"}`)

	rec := requestWithSession(t, server, admin, http.MethodGet, "/api/operator/archive", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("archive state status = %d: %s", rec.Code, rec.Body.String())
	}
	var archive map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&archive); err != nil {
		t.Fatalf("decode archive state: %v", err)
	}
	if len(archive["resources"].([]any)) == 0 || archive["retentionPolicy"].(map[string]any)["adminAuditDays"] == nil {
		t.Fatalf("archive state must come from backend archive facts and policy: %#v", archive)
	}
}

func TestManagementStateIncludesBackendCleanupAndAnomalySummary(t *testing.T) {
	app := newControlPlaneApp()
	mustStore(t, app.tables.SaveWorkspace(context.Background(), map[string]any{
		"id":             "ws-missing-storage",
		"ownerAccountId": "acct-alpha",
		"storageId":      "missing-storage",
		"access":         map[string]any{"tokenStatus": "active"},
	}))

	management := app.managementState(false, nil)
	cleanup := management["workspaceAccessCleanup"].(map[string]any)
	if cleanup["cleanupCandidateCount"] != 1 {
		t.Fatalf("cleanup summary should come from backend facts: %#v", cleanup)
	}
	operator := app.operatorSummary()
	anomalies := operator["resourceAnomalies"].([]any)
	if len(anomalies) != 1 || anomalies[0].(map[string]any)["status"] != "missing_storage" {
		t.Fatalf("operator resource anomalies should include backend cleanup issues: %#v", anomalies)
	}
}

func TestResourceSettlementProjectsLedgerEvidenceIntoConsoleState(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))

	createResource(t, server, http.MethodPost, "/api/billing/resource-settlements", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","resourceType":"compute","resourceId":"compute-alpha","amountCents":123,"confirm":true}`)
	createResource(t, server, http.MethodPost, "/api/billing/resource-settlements", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","resourceType":"storage","resourceId":"storage-alpha","amountCents":45,"confirm":true}`)
	req := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-alpha", nil)
	addSessionCookies(req, tenantAdminSessionForTest(t, server))
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
	addSessionCookies(req, tenantAdminSessionForTest(t, server))
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
		return compute["id"] == "compute-alpha" && compute["accountId"] == "acct-alpha" && compute["workspaceId"] == "ws-alpha" && compute["status"] == "running" && compute["nodeName"] == "node-from-fabric"
	}) {
		t.Fatalf("state did not hydrate compute resource from Fabric operation: %#v", computes)
	}
	storageVolumes := state["storageVolumes"].([]any)
	if !slices.ContainsFunc(storageVolumes, func(row any) bool {
		storage := row.(map[string]any)
		return storage["id"] == "storage-alpha" && storage["accountId"] == "acct-alpha" && storage["workspaceId"] == "ws-alpha" && storage["status"] == "available" && storage["providerResourceId"] == "pvc/storage-alpha-data"
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

func TestRememberRuntimeOperationPreservesComputeCommercialFacts(t *testing.T) {
	app := newControlPlaneAppEmpty()
	mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{
		"id": "compute-alpha", "accountId": "acct-alpha", "ownerUserId": "user-alpha", "name": "Alpha compute",
		"status": "provisioning", "holdId": "hold-compute", "holdAmountCents": int64(7862),
		"pricingVersion": "pricing-v1", "priceSnapshot": map[string]any{"packageId": "basic", "unitPriceCents": int64(47)},
	}))

	err := app.rememberRuntimeOperations([]clients.FabricOperation{{
		ID: "fabric-compute", OperationID: "operation-compute", ResourceKind: "compute_allocation", ResourceID: "compute-alpha",
		AccountID: "acct-alpha", Status: "succeeded", RedactedProviderPayload: map[string]any{"resource": map[string]any{
			"id": "compute-alpha", "accountId": "acct-alpha", "status": "running", "nodeName": "node-from-fabric",
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	compute, _ := app.getCompute("compute-alpha")
	if compute["holdId"] != "hold-compute" || int64(numberField(compute, "holdAmountCents", 0)) != 7862 || compute["pricingVersion"] != "pricing-v1" || compute["ownerUserId"] != "user-alpha" || compute["name"] != "Alpha compute" {
		t.Fatalf("Fabric operation erased Control Plane facts: %#v", compute)
	}
	if compute["nodeName"] != "node-from-fabric" || compute["status"] != "running" {
		t.Fatalf("Fabric provider facts were not applied: %#v", compute)
	}
}

func TestRememberRuntimeOperationPreservesStorageCommercialFacts(t *testing.T) {
	app := newControlPlaneAppEmpty()
	mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{
		"id": "storage-alpha", "accountId": "acct-alpha", "ownerUserId": "user-alpha", "name": "Alpha storage",
		"status": "provisioning", "holdId": "hold-storage", "holdAmountCents": int64(101),
		"pricingVersion": "pricing-v1", "priceSnapshot": map[string]any{"resourceType": "storage", "unitPriceCents": int64(1)},
	}))

	err := app.rememberRuntimeOperations([]clients.FabricOperation{{
		ID: "fabric-storage", OperationID: "operation-storage", ResourceKind: "storage_volume", ResourceID: "storage-alpha",
		AccountID: "acct-alpha", Status: "succeeded", RedactedProviderPayload: map[string]any{"resource": map[string]any{
			"id": "storage-alpha", "accountId": "acct-alpha", "status": "ready", "providerResourceId": "pvc/storage-alpha-data",
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	storage, _ := app.getStorage("storage-alpha")
	if storage["holdId"] != "hold-storage" || int64(numberField(storage, "holdAmountCents", 0)) != 101 || storage["pricingVersion"] != "pricing-v1" || storage["ownerUserId"] != "user-alpha" || storage["name"] != "Alpha storage" {
		t.Fatalf("Fabric operation erased Control Plane facts: %#v", storage)
	}
	if storage["providerResourceId"] != "pvc/storage-alpha-data" || storage["status"] != "available" {
		t.Fatalf("Fabric provider facts were not applied: %#v", storage)
	}
}

func TestConsoleStateSkipsUnscopedHistoricFabricResourceProjection(t *testing.T) {
	setTestOperatorAccount(t, "acct-alpha")
	service := controlplane.NewService(fakeLedgerClient{}, &fabricClientWithUnscopedHistoricOperation{})
	server, err := NewPersistentServer(service, NewTestEntStateStore(t, t.TempDir()+"/historic-fabric.sqlite"))
	if err != nil {
		t.Fatalf("create persistent server: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-alpha", nil)
	session := tenantAdminSessionForTest(t, server)
	ensureCustomerMembershipForTest(t, server, session, "acct-alpha", "usr-admin")
	addSessionCookies(req, session)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("state status = %d: %s", rec.Code, rec.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	for _, row := range state["computeAllocations"].([]any) {
		if row.(map[string]any)["id"] == "compute-historic" {
			t.Fatalf("unscoped historic resource must not become a compute projection: %#v", state["computeAllocations"])
		}
	}
}

func TestResourceSettlementProjectionKeepsRequestIdentityWhenLedgerOmitsIt(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClientWithoutSettlementIdentity{}, &fakeFabricClient{}))

	createResource(t, server, http.MethodPost, "/api/billing/resource-settlements", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","resourceType":"compute","resourceId":"compute-alpha","amountCents":123,"confirm":true}`)
	req := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-alpha", nil)
	addSessionCookies(req, tenantAdminSessionForTest(t, server))
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
	addSessionCookies(req, tenantAdminSessionForTest(t, server))
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
	addSessionCookies(req, tenantAdminSessionForTest(t, server))
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

func TestApplyLedgerFactsSerializesProjectionWrites(t *testing.T) {
	store := &concurrencyDetectingTableStore{memoryTableStore: newMemoryTableStore()}
	app := newControlPlaneAppEmpty()
	app.tables = store
	ledger := readBackedLedgerClient{}
	wallet, _ := ledger.Wallet(context.Background(), "acct-alpha")
	entries, _ := ledger.ListLedgerEntries(context.Background(), "acct-alpha")
	transactions, _ := ledger.ListWalletTransactions(context.Background(), "acct-alpha")
	settlements, _ := ledger.ListResourceSettlements(context.Background(), "acct-alpha")

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := app.applyLedgerFacts("acct-alpha", wallet, entries, transactions, nil, settlements); err != nil {
				t.Errorf("apply ledger facts: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := atomic.LoadInt32(&store.concurrentLedgerWrites); got != 0 {
		t.Fatalf("ledger projection writes were concurrent: %d", got)
	}
}

type concurrencyDetectingTableStore struct {
	*memoryTableStore
	activeLedgerWrites     int32
	concurrentLedgerWrites int32
}

func (s *concurrencyDetectingTableStore) SaveLedgerEntry(ctx context.Context, row map[string]any) error {
	if active := atomic.AddInt32(&s.activeLedgerWrites, 1); active > 1 {
		atomic.AddInt32(&s.concurrentLedgerWrites, 1)
	}
	time.Sleep(time.Millisecond)
	err := s.memoryTableStore.SaveLedgerEntry(ctx, row)
	atomic.AddInt32(&s.activeLedgerWrites, -1)
	return err
}

func TestReconciliationGuardBlocksNewResourceProvisioning(t *testing.T) {
	var calls []string
	server := NewServer(controlplane.NewService(fakeBlockingReconciliationLedgerClient{}, &fakeFabricClient{calls: &calls}))
	session := tenantAdminSessionForTest(t, server)

	createResourceWithSession(t, server, session, http.MethodPost, "/api/billing/reconciliation", `{"confirm":true,"report":{"id":"recon-mismatch","status":"mismatch"}}`)

	stateReq := httptest.NewRequest(http.MethodGet, "/api/management/state", nil)
	addAuth(stateReq, operatorSessionForTest(t, server))
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
	mustStore(t, app.tables.SaveWorkspace(context.Background(), map[string]any{"id": "ws-alpha",
		"runtime": map[string]any{"serviceName": strings.TrimPrefix(backend.URL, "http://")},
	}))
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
	mustStore(t, app.tables.SaveWorkspace(context.Background(), map[string]any{"id": "ws-alpha",
		"runtime": map[string]any{"serviceName": strings.TrimPrefix(backend.URL, "http://")},
	}))
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
			tc.row["id"] = "ws-alpha"
			mustStore(t, app.tables.SaveWorkspace(context.Background(), tc.row))
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
	addSessionCookies(req, tenantAdminSessionForTest(t, server))
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
	session := tenantAdminSessionForTest(t, server)
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

	admin := tenantAdminSessionForTest(t, server)
	postWithoutCSRF := httptest.NewRequest(http.MethodPost, "/api/compute-allocations", bytes.NewBufferString(`{"accountId":"acct-alpha","packageId":"basic"}`))
	postWithoutCSRF.Header.Set("Content-Type", "application/json")
	postWithoutCSRF.Header.Set("Idempotency-Key", "compute-no-csrf")
	addSessionCookies(postWithoutCSRF, admin)
	postWithoutCSRFRec := httptest.NewRecorder()
	server.ServeHTTP(postWithoutCSRFRec, postWithoutCSRF)
	if postWithoutCSRFRec.Code != http.StatusForbidden {
		t.Fatalf("write without csrf status = %d, want 403: %s", postWithoutCSRFRec.Code, postWithoutCSRFRec.Body.String())
	}

	ownerUser := createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"owner@lab.example","accountId":"acct-alpha","role":"member","password":"CorrectHorseBatteryStaple!"}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/organizations/members", `{"organizationId":"org-alpha","userId":"`+stringValue(ownerUser["id"])+`","accountId":"acct-alpha","role":"member"}`)
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
	tenant := tenantAdminSessionForTest(t, server)
	created := createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"member@lab.example","accountId":"acct-alpha","role":"member","password":"CorrectHorseBatteryStaple!"}`)

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
		{http.MethodPost, "/api/operator/archive-terminal-resources", `{"reason":"test"}`},
	} {
		session := admin
		if !explicitOperatorTestPath(tc.path) {
			session = tenant
		}
		rec := requestWithSession(t, server, session, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "confirmation_required") {
			t.Fatalf("%s %s status=%d body=%s, want confirmation_required", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestLoginSessionMeAndLogoutUseStoredPasswordHash(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := operatorSessionForTest(t, server)
	ownerUser := createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"owner@lab.example","accountId":"acct-alpha","role":"admin","password":"CorrectHorseBatteryStaple!"}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/organizations/members", `{"organizationId":"org-alpha","userId":"`+stringValue(ownerUser["id"])+`","accountId":"acct-alpha","role":"admin"}`)

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
	created := createResource(t, server, http.MethodPost, "/api/users", `{"email":"member@lab.example","accountId":"acct-alpha","role":"member","password":"CorrectHorseBatteryStaple!"}`)
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
	if deleted["status"] != "deleted" || deleted["deletedAt"] == nil || deleted["deletedBy"] != "usr-operator" || deleted["deleteReason"] != "left_lab" {
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

func TestUserLifecycleProtectsLastActiveOperator(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	operator := operatorSessionForTest(t, server)
	for _, tc := range []struct {
		path string
	}{
		{"/api/users/disable"},
		{"/api/users/delete"},
	} {
		body := `{"userId":"usr-operator","reason":"test"}`
		if tc.path == "/api/users/delete" {
			body = `{"userId":"usr-operator","reason":"test","confirm":true}`
		}
		req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		addAuth(req, operator)
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
	addAuth(req, tenantAdminSessionForTest(t, server))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "missing_external_ticket_id") {
		t.Fatalf("status=%d body=%s, want missing external ticket id", rec.Code, rec.Body.String())
	}
}

func TestSupportTicketMappingPersistsExternalContext(t *testing.T) {
	setTestOperatorAccount(t, "acct-alpha")
	path := t.TempDir() + "/control-plane-state.sqlite"
	service := controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{})
	server, err := NewPersistentServer(service, NewTestEntStateStore(t, path))
	if err != nil {
		t.Fatalf("create persistent server: %v", err)
	}
	body := `{"accountId":"acct-alpha","userId":"usr-alpha","workspaceId":"ws-alpha","externalSystem":"zammad","externalTicketId":"ZAM-42","externalUrl":"https://support.example/tickets/42","resourceIds":["compute-alpha"],"operationId":"op-alpha","title":"Workspace failed","description":"provider timeout"}`
	session := tenantAdminSessionForTest(t, server)
	ensureCustomerMembershipForTest(t, server, session, "acct-alpha", "usr-admin")
	created := createResourceWithSession(t, server, session, http.MethodPost, "/api/support/tickets", body)
	if !strings.HasPrefix(stringValue(created["id"]), "support-") || created["externalTicketId"] != "ZAM-42" || created["accountId"] != "acct-alpha" {
		t.Fatalf("support mapping did not keep external context: %#v", created)
	}

	restarted, err := NewPersistentServer(service, NewTestEntStateStore(t, path))
	if err != nil {
		t.Fatalf("restart persistent server: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/support/tickets?scope=all", nil)
	addSessionCookies(req, tenantAdminSessionForTest(t, restarted))
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

func setTestOperatorAccount(t *testing.T, accountID string) {
	t.Helper()
	t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"id":"usr-admin","email":"admin@medopl.cn","password":"StableAdminPass2026!","role":"admin","accountId":"`+accountID+`"}]`)
}

func ensureCustomerMembershipForTest(t *testing.T, server http.Handler, admin *httptest.ResponseRecorder, accountID, userID string) {
	t.Helper()
	organization := createResourceWithSession(t, server, admin, http.MethodPost, "/api/organizations", `{"name":"Test tenant","billingAccountId":"`+accountID+`"}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/organizations/members", `{"organizationId":"`+stringValue(organization["id"])+`","userId":"`+userID+`","accountId":"`+accountID+`","role":"admin"}`)
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
		{http.MethodPost, "/api/compute-allocations/compute-alpha/sync", `{}`},
		{http.MethodPost, "/api/compute-allocations/compute-alpha/destroy", `{"confirm":true}`},
		{http.MethodPost, "/api/storage-volumes", `{"name":"data","sizeGb":10}`},
		{http.MethodPost, "/api/storage-volumes/storage-alpha/sync", `{}`},
		{http.MethodPost, "/api/storage-volumes/destroy", `{"storageId":"storage-alpha"}`},
		{http.MethodPost, "/api/storage-attachments", `{"computeAllocationId":"compute-alpha","storageId":"storage-alpha","mountPath":"/data"}`},
		{http.MethodPost, "/api/storage-attachments/detach", `{"attachmentId":"attach-alpha"}`},
		{http.MethodPost, "/api/workspaces/reset-token", `{"workspaceId":"ws-alpha"}`},
		{http.MethodPost, "/api/workspaces/delete-token", `{"workspaceId":"ws-alpha"}`},
		{http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"ws-alpha"}`},
		{http.MethodPost, "/api/operator/cleanup-workspace-access", `{"reason":"test"}`},
		{http.MethodPost, "/api/operator/archive-terminal-resources", `{"reason":"test"}`},
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
