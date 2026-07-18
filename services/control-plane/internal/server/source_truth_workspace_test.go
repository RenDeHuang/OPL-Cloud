package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
)

type failingWorkspaceListStore struct {
	controlPlaneTableStore
	accountIDs []string
}

func (s *failingWorkspaceListStore) ListWorkspaces(_ context.Context, accountID string) ([]map[string]any, error) {
	s.accountIDs = append(s.accountIDs, accountID)
	return nil, errors.New("workspace store unavailable")
}

type scopedWorkspaceListStore struct {
	controlPlaneTableStore
	accountIDs []string
}

func (s *scopedWorkspaceListStore) ListWorkspaces(ctx context.Context, accountID string) ([]map[string]any, error) {
	s.accountIDs = append(s.accountIDs, accountID)
	return s.controlPlaneTableStore.ListWorkspaces(ctx, accountID)
}

type sourceTruthRuntimeFabric struct {
	fakeFabricClient
	statusErr error
}

func (f *sourceTruthRuntimeFabric) WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (clients.WorkspaceRuntime, error) {
	if f.statusErr != nil {
		f.record("fabric.runtime-status")
		return clients.WorkspaceRuntime{}, f.statusErr
	}
	if f.runtimeStatus.Status != "" {
		f.record("fabric.runtime-status")
		return f.runtimeStatus, nil
	}
	return f.fakeFabricClient.WorkspaceRuntimeStatus(ctx, workspaceID)
}

func TestWorkspaceListIsStrictControlPlaneSource(t *testing.T) {
	store := newMemoryTableStore()
	client := &testSub2APIClient{charges: map[string]int64{}}
	server, session := newGatewayOwnerTestServer(t, client, store)
	mustStore(t, store.SaveWorkspace(context.Background(), workspaceGatewayTestRow(map[string]any{
		"id": "ws-source", "accountId": "acct-gateway", "ownerAccountId": "acct-gateway", "ownerUserId": "usr-gateway-owner",
		"createdAt": "2026-07-18T00:00:00Z", "updatedAt": "2026-07-18T01:00:00Z",
		"name": "Source Workspace", "url": "https://workspace.medopl.cn/w/ws-source/", "state": "provisioning", "status": "must-not-project",
		"storageId": "storage-source", "computeAllocationId": "compute-source", "currentComputeAllocationId": "compute-source", "attachmentId": "attachment-source", "currentAttachmentId": "attachment-source", "runtimeId": "runtime-source",
		"compute": map[string]any{"raw": true}, "storage": map[string]any{"raw": true}, "attachment": map[string]any{"raw": true},
		"runtime": map[string]any{"provider": "fabric-raw", "checks": []any{map[string]any{"raw": true}}}, "access": map[string]any{"secretRef": "secret"},
	})))

	response := requestWithSession(t, server, session, http.MethodGet, "/api/workspaces?accountId=acct-other", "")
	if response.Code != http.StatusOK {
		t.Fatalf("workspace list = %d: %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("workspace Cache-Control = %q", got)
	}
	var envelope map[string]any
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	data := mapField(envelope, "data")
	items, _ := data["items"].([]any)
	if envelope["source"] != "control-plane" || envelope["status"] != "available" || len(items) != 1 || data["total"] != float64(1) {
		t.Fatalf("workspace envelope = %#v", envelope)
	}
	workspace := items[0].(map[string]any)
	allowed := map[string]bool{
		"id": true, "ownerAccountId": true, "ownerUserId": true, "state": true, "createdAt": true, "updatedAt": true,
		"name": true, "url": true, "storageId": true, "currentComputeAllocationId": true, "currentAttachmentId": true, "runtimeId": true,
		"packageId": true, "storageGb": true, "autoRenew": true, "priceVersion": true, "currency": true, "totalUsdMicros": true,
		"periodStart": true, "paidThrough": true, "renewalStatus": true,
	}
	if workspace["id"] != "ws-source" || workspace["state"] != "provisioning" || len(workspace) != len(allowed) {
		t.Fatalf("workspace projection = %#v", workspace)
	}
	for key := range workspace {
		if !allowed[key] {
			t.Fatalf("unsafe workspace field %q in %#v", key, workspace)
		}
	}

	emptyStore := newMemoryTableStore()
	emptyServer, emptySession := newGatewayOwnerTestServer(t, client, emptyStore)
	empty := requestWithSession(t, emptyServer, emptySession, http.MethodGet, "/api/workspaces", "")
	var emptyEnvelope map[string]any
	_ = json.NewDecoder(empty.Body).Decode(&emptyEnvelope)
	if empty.Code != http.StatusOK || emptyEnvelope["status"] != "empty" || emptyEnvelope["available"] != true {
		t.Fatalf("empty workspaces = %d: %#v", empty.Code, emptyEnvelope)
	}
}

func TestWorkspaceListStoreFailureIsUnavailable(t *testing.T) {
	base := newMemoryTableStore()
	seedTenantMember(t, base, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	store := &failingWorkspaceListStore{controlPlaneTableStore: base}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatal(err)
	}
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	response := requestWithSession(t, server, session, http.MethodGet, "/api/workspaces", "")
	assertUnavailableWorkspaceEnvelope(t, response, http.StatusInternalServerError, "control-plane")
	if len(store.accountIDs) != 1 || store.accountIDs[0] != "acct-alpha" {
		t.Fatalf("workspace list account scope = %#v", store.accountIDs)
	}
}

func TestRuntimeStatusIsStrictReadOnlyFabricSource(t *testing.T) {
	base := newMemoryTableStore()
	store := &scopedWorkspaceListStore{controlPlaneTableStore: base}
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	calls := []string{}
	fabric := &sourceTruthRuntimeFabric{fakeFabricClient: fakeFabricClient{calls: &calls, runtimeStatus: clients.WorkspaceRuntime{
		ID: "runtime-alpha", WorkspaceID: "ws-alpha", URL: "https://workspace.medopl.cn/w/ws-alpha/", ServiceName: "opl-compute-alpha",
		Status: "unready", Ready: false, Checks: []any{map[string]any{"name": "deployment_ready", "ok": false, "details": map[string]any{"providerSecret": "must-not-leak"}}},
		Access: clients.WorkspaceRuntimeAccess{Username: "opl", Password: "must-not-leak", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "must-not-leak"},
	}}}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, fabric), store)
	if err != nil {
		t.Fatal(err)
	}
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	seedRuntimeAccessWorkspaceForTest(t, store, "usr-alpha", nil)
	before, _ := store.ListWorkspaces(context.Background(), "acct-alpha")

	response := requestWithSession(t, server, session, http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"ws-alpha"}`)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "must-not-leak") || strings.Contains(response.Body.String(), "provider") {
		t.Fatalf("runtime source = %d: %s", response.Code, response.Body.String())
	}
	var envelope map[string]any
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	data := mapField(envelope, "data")
	checks, ok := data["checks"].([]any)
	if envelope["source"] != "fabric" || envelope["status"] != "available" || !ok || len(checks) != 1 || len(checks[0].(map[string]any)) != 2 || data["status"] != "unready" || data["ready"] != false {
		t.Fatalf("runtime envelope = %#v", envelope)
	}
	access := mapField(data, "access")
	if len(data) != 8 || len(access) != 3 || access["username"] != "opl" || access["credentialStatus"] != "configured" || access["credentialVersion"] != "v1" {
		t.Fatalf("runtime data = %#v", data)
	}
	fabric.runtimeStatus.Checks = []any{}
	emptyChecks := requestWithSession(t, server, session, http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"ws-alpha"}`)
	var emptyEnvelope map[string]any
	_ = json.NewDecoder(emptyChecks.Body).Decode(&emptyEnvelope)
	if emptyChecks.Code != http.StatusOK || len(mapField(emptyEnvelope, "data")["checks"].([]any)) != 0 {
		t.Fatalf("empty runtime checks = %d: %#v", emptyChecks.Code, emptyEnvelope)
	}
	after, _ := store.ListWorkspaces(context.Background(), "acct-alpha")
	if !reflect.DeepEqual(before, after) || len(calls) != 2 {
		t.Fatalf("runtime source caused side effects: before=%#v after=%#v calls=%#v", before, after, calls)
	}

	fabric.runtimeStatus = clients.WorkspaceRuntime{WorkspaceID: "ws-alpha", Status: "not_found", Ready: false, Checks: []any{map[string]any{"name": "workspace_resources_found", "ok": false}}}
	notFound := requestWithSession(t, server, session, http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"ws-alpha"}`)
	if notFound.Code != http.StatusOK {
		t.Fatalf("not_found runtime = %d: %s", notFound.Code, notFound.Body.String())
	}
	var notFoundEnvelope map[string]any
	_ = json.NewDecoder(notFound.Body).Decode(&notFoundEnvelope)
	notFoundData := mapField(notFoundEnvelope, "data")
	if notFoundData["status"] != "not_found" || notFoundData["ready"] != false || notFoundData["runtimeId"] != nil || notFoundData["url"] != nil || notFoundData["serviceName"] != nil {
		t.Fatalf("not_found runtime data = %#v", notFoundData)
	}
	fabric.runtimeStatus = clients.WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: "ws-alpha", URL: "https://workspace.medopl.cn/w/ws-alpha/", ServiceName: "opl-compute-alpha", Status: "unready", Ready: false, Checks: []any{}}
	fabric.runtimeStatus.Status = "mystery"
	unknown := requestWithSession(t, server, session, http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"ws-alpha"}`)
	assertUnavailableWorkspaceEnvelope(t, unknown, http.StatusBadGateway, "fabric")
	fabric.runtimeStatus.Status = "unready"
	fabric.runtimeStatus.WorkspaceID = "ws-other"
	mismatch := requestWithSession(t, server, session, http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"ws-alpha"}`)
	assertUnavailableWorkspaceEnvelope(t, mismatch, http.StatusBadGateway, "fabric")
	fabric.runtimeStatus.WorkspaceID = "ws-alpha"
	fabric.runtimeStatus.Status = ""
	missing := requestWithSession(t, server, session, http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"ws-alpha"}`)
	assertUnavailableWorkspaceEnvelope(t, missing, http.StatusBadGateway, "fabric")
	fabric.runtimeStatus.Status = "unready"
	fabric.statusErr = errors.New("Fabric unavailable")
	unavailable := requestWithSession(t, server, session, http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"ws-alpha"}`)
	assertUnavailableWorkspaceEnvelope(t, unavailable, http.StatusBadGateway, "fabric")
	for _, accountID := range store.accountIDs {
		if accountID != "acct-alpha" {
			t.Fatalf("runtime workspace account scope = %#v", store.accountIDs)
		}
	}
}

func assertUnavailableWorkspaceEnvelope(t *testing.T, response *httptest.ResponseRecorder, wantStatus int, source string) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("unavailable status = %d, want %d: %s", response.Code, wantStatus, response.Body.String())
	}
	var envelope map[string]any
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope) != 4 || envelope["source"] != source || envelope["status"] != "unavailable" || envelope["available"] != false || envelope["data"] != nil {
		t.Fatalf("unavailable workspace envelope = %#v", envelope)
	}
	if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("unavailable Cache-Control = %q", got)
	}
}
