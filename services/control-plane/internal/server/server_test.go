package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("OPL_TENCENT_ZONE", "ap-shanghai-2")
	_ = os.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "S5.MEDIUM4")
	_ = os.Setenv("OPL_PRO_COMPUTE_INSTANCE_TYPE", "SA5.2XLARGE16")
	os.Exit(m.Run())
}

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

func TestConsoleStaticEntryServesLoginAndHome(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
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

func TestConsoleStaticDelivery(t *testing.T) {
	dist := t.TempDir()
	asset := []byte(`console.log("hashed asset")`)
	index := []byte(`<!doctype html><html><body><div id="root"></div></body></html>`)
	icon := []byte("\x89PNG\r\n\x1a\nfixture")
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string][]byte{
		"assets/hash.js":   asset,
		"index.html":       index,
		"opl-app-icon.png": icon,
	} {
		if err := os.WriteFile(filepath.Join(dist, path), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("OPL_CONSOLE_DIST_DIR", dist)
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	request := func(method, path string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}

	t.Run("gzip hashed asset", func(t *testing.T) {
		rec := request(http.MethodGet, "/assets/hash.js", map[string]string{"Accept-Encoding": "gzip"})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding = %q, want gzip", got)
		}
		if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
			t.Fatalf("Vary = %q, want Accept-Encoding", got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "public,max-age=31536000,immutable" {
			t.Fatalf("Cache-Control = %q", got)
		}
		reader, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded, asset) {
			t.Fatalf("decoded asset = %q, want %q", decoded, asset)
		}
	})

	t.Run("index is revalidated", func(t *testing.T) {
		rec := request(http.MethodGet, "/login", nil)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), index) {
			t.Fatalf("index response = %d %q", rec.Code, rec.Body.Bytes())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("Cache-Control = %q, want no-cache", got)
		}
	})

	t.Run("app icon is PNG", func(t *testing.T) {
		rec := request(http.MethodGet, "/opl-app-icon.png", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); got != "image/png" {
			t.Fatalf("Content-Type = %q, want image/png", got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "public,max-age=86400" {
			t.Fatalf("Cache-Control = %q, want one-day public caching without immutable", got)
		}
		if !bytes.HasPrefix(rec.Body.Bytes(), []byte("\x89PNG\r\n\x1a\n")) {
			t.Fatalf("icon does not have PNG magic: %x", rec.Body.Bytes())
		}
	})

	t.Run("HEAD has no body", func(t *testing.T) {
		rec := request(http.MethodHead, "/assets/hash.js", map[string]string{"Accept-Encoding": "gzip"})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("HEAD body length = %d, want 0", rec.Body.Len())
		}
	})

	t.Run("Range is not dynamically compressed", func(t *testing.T) {
		rec := request(http.MethodGet, "/assets/hash.js", map[string]string{
			"Accept-Encoding": "gzip",
			"Range":           "bytes=0-6",
		})
		if rec.Code != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206: %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding = %q, want empty", got)
		}
		if !bytes.Equal(rec.Body.Bytes(), asset[:7]) {
			t.Fatalf("range body = %q, want %q", rec.Body.Bytes(), asset[:7])
		}
	})

	t.Run("gzip quality zero is respected", func(t *testing.T) {
		rec := request(http.MethodGet, "/assets/hash.js", map[string]string{"Accept-Encoding": "br, gzip;q=0"})
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "" {
			t.Fatalf("response = %d Content-Encoding %q", rec.Code, rec.Header().Get("Content-Encoding"))
		}
		if !bytes.Equal(rec.Body.Bytes(), asset) {
			t.Fatalf("asset body = %q, want %q", rec.Body.Bytes(), asset)
		}
	})

	t.Run("API is not compressed", func(t *testing.T) {
		rec := request(http.MethodGet, "/api/healthz", map[string]string{"Accept-Encoding": "gzip"})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding = %q, want empty", got)
		}
	})

	t.Run("path traversal is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/hash.js", nil)
		req.URL.Path = "/assets/../index.html"
		rec := httptest.NewRecorder()
		new(controlPlaneServer).consoleStatic(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestUncontractedAdminDiagnosticsAPIRouteDoesNotReturnFakeEvidence(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
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
	fabric := &fakeFabricClient{calls: &calls}
	server := NewServer(newTestService(fakeLedgerClient{}, fabric))
	session := tenantOwnerSessionForTest(t, server)
	var login map[string]any
	if err := json.NewDecoder(session.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}
	ownerID := stringValue(nested(login, "user", "id"))

	compute := createResourceWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	storage := createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","sizeGb":10,"computeAllocationId":"`+stringValue(compute["id"])+`"}`)
	createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-attachments", `{"workspaceId":"ws-alpha","computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`","mountPath":"/data"}`)

	body := bytes.NewBufferString(`{"accountId":"acct-alpha","ownerId":"usr-owner","workspaceName":"Alpha Lab","attachmentId":"attachment-from-fabric"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces", body)
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
	if workspace["accountId"] != "acct-alpha" || workspace["ownerId"] != ownerID || workspace["url"] != "https://workspace.medopl.cn/w/ws-from-fabric/" {
		t.Fatalf("workspace did not come from service boundary: %#v", workspace)
	}
	if workspace["computeAllocationId"] != compute["id"] || workspace["storageId"] != storage["id"] || workspace["attachmentId"] != "attachment-from-fabric" || workspace["receiptId"] != nil {
		t.Fatalf("workspace missing ledger/fabric evidence: %#v", workspace)
	}
	handler := server.(*controlPlaneHTTPHandler)
	stored, _ := handler.app.getWorkspace(stringValue(workspace["id"]))
	if stringValue(stored["receiptId"]) != "receipt-from-ledger" {
		t.Fatalf("Workspace receipt was not kept internally: %#v", stored)
	}
	if workspace["openable"] != true || workspace["accessState"] != "available" {
		t.Fatalf("ready workspace must be openable: %#v", workspace)
	}
	if workspace["autoRenew"] != false || workspace["authorizedBy"] != "" || workspace["authorizedAt"] != "" ||
		workspace["renewalStatus"] != "active" || workspace["priceVersion"] != pricingCatalogVersion ||
		workspace["computeUsdMicros"] != float64(50_000_000) || workspace["storageUsdMicros"] != float64(2_580_000) || workspace["totalUsdMicros"] != float64(52_580_000) {
		t.Fatalf("legacy owner create missing canonical renewal state: %#v", workspace)
	}
	if access := workspace["access"].(map[string]any); access["account"] != "admin" || access["password"] != nil || access["tokenStatus"] != nil || access["requiresLogin"] != nil {
		t.Fatalf("workspace creation must include credential metadata without plaintext: %#v", access)
	}
	if workspace["runtimePassword"] != nil {
		t.Fatalf("workspace response leaked internal runtimePassword field: %#v", workspace)
	}
	if slices.Contains(calls[5:], "fabric.compute") || slices.Contains(calls[5:], "fabric.storage") {
		t.Fatalf("workspace create must not allocate replacement resources: %#v", calls)
	}
	if len(calls) < 7 || !slices.Equal(calls[5:7], []string{"fabric.gateway-secret", "fabric.runtime"}) {
		t.Fatalf("workspace must write account Gateway Secret before runtime: %#v", calls)
	}
	if len(fabric.gatewaySecretInputs) != 1 || fabric.gatewaySecretInputs[0].AccountID != "acct-alpha" || fabric.gatewaySecretInputs[0].GatewayAPIKey != "workspace-key-secret" {
		t.Fatalf("workspace Gateway Secret input = %#v", fabric.gatewaySecretInputs)
	}
	if len(fabric.runtimeInputs) != 1 || fabric.runtimeInputs[0].GatewaySecretRef != "opl-gateway-acct-alpha" {
		t.Fatalf("workspace runtime input = %#v", fabric.runtimeInputs)
	}
	if strings.Contains(rec.Body.String(), "workspace-key-secret") {
		t.Fatalf("workspace response leaked Gateway key: %s", rec.Body.String())
	}
	management := requestWithSession(t, server, session, http.MethodGet, "/api/management/state", "")
	if strings.Contains(management.Body.String(), "runtime-password-alpha") || strings.Contains(management.Body.String(), "workspace-key-secret") {
		t.Fatalf("Control Plane persisted Workspace credential in state or audit: %s", management.Body.String())
	}
}

func TestWorkspaceCreateKeepsDraftIdentityAndReplaysWithoutHeader(t *testing.T) {
	calls, events := []string{}, &[]string{}
	fabric := &fakeFabricClient{calls: &calls}
	ledger := &monthlyLedger{events: events}
	server := NewServer(newTestService(ledger, fabric))
	session := tenantOwnerSessionForTest(t, server)

	compute := createResourceWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic","name":"verification-slot-01"}`)
	storage := createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","packageId":"basic","sizeGb":10,"name":"verification-slot-01","computeAllocationId":"`+stringValue(compute["id"])+`"}`)
	attachment := createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-attachments", `{"accountId":"acct-alpha","computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`"}`)
	draftID := resourceIDForMutation("ws", "acct-alpha", "primary")
	if compute["workspaceId"] != draftID || storage["workspaceId"] != draftID || attachment["workspaceId"] != draftID || attachment["packageId"] != "basic" {
		t.Fatalf("draft identity compute=%#v storage=%#v attachment=%#v want=%s", compute, storage, attachment, draftID)
	}

	postWorkspace := func(name string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/workspaces", bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceName":"`+name+`","attachmentId":"`+stringValue(attachment["id"])+`"}`))
		req.Header.Set("Content-Type", "application/json")
		addAuth(req, session)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	first := postWorkspace("verification-slot-01")
	second := postWorkspace("verification-slot-01")
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("first=%d %s second=%d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	var firstWorkspace, secondWorkspace map[string]any
	if err := json.NewDecoder(first.Body).Decode(&firstWorkspace); err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(second.Body).Decode(&secondWorkspace); err != nil {
		t.Fatal(err)
	}
	if firstWorkspace["id"] != draftID || secondWorkspace["id"] != draftID || firstWorkspace["packageId"] != "basic" || len(fabric.runtimeInputs) != 1 || fabric.runtimeInputs[0].WorkspaceID != draftID {
		t.Fatalf("first=%#v second=%#v runtimeInputs=%#v", firstWorkspace, secondWorkspace, fabric.runtimeInputs)
	}
	if len(ledger.receipts) != 3 || ledger.receipts[2].Type != "workspace.created" || ledger.receipts[2].WorkspaceID != draftID {
		t.Fatalf("receipts=%#v", ledger.receipts)
	}
	beforeCalls, beforeReceipts := len(calls), len(ledger.receipts)
	conflict := postWorkspace("different-name")
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), errPrimaryWorkspaceExists.Error()) || len(calls) != beforeCalls || len(ledger.receipts) != beforeReceipts {
		t.Fatalf("conflict=%d %s calls=%#v receipts=%#v", conflict.Code, conflict.Body.String(), calls, ledger.receipts)
	}
}

func TestWorkspaceCreateReplayCannotOverwriteCanonicalLifecycle(t *testing.T) {
	store := newMemoryTableStore()
	calls, events := []string{}, &[]string{}
	fabric := &fakeFabricClient{calls: &calls}
	ledger := &monthlyLedger{events: events}
	server, err := NewPersistentServer(newTestService(ledger, fabric), store)
	if err != nil {
		t.Fatal(err)
	}
	owner := tenantOwnerSessionForTest(t, server)
	attachment := createWorkspaceAttachmentForTest(t, server, owner, "workspace-create-canonical-replay")
	body := `{"workspaceName":"Canonical","attachmentId":"` + stringValue(attachment["id"]) + `"}`
	first := requestWithMutationKeyForTest(t, server, owner, http.MethodPost, "/api/workspaces", body, "workspace-create-canonical-replay")
	if first.Code != http.StatusCreated {
		t.Fatalf("first create=%d: %s", first.Code, first.Body.String())
	}
	rows, _ := store.ListWorkspaces(context.Background(), "acct-alpha")
	if len(rows) != 1 {
		t.Fatalf("created Workspaces=%#v", rows)
	}
	current := cloneMap(rows[0])
	paidThrough := current["paidThrough"]
	current["state"], current["status"], current["currentComputeAllocationId"], current["currentAttachmentId"], current["autoRenew"] = "suspended", "suspended", "", "", false
	if err := store.SaveWorkspace(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	beforeCalls, beforeReceipts := len(calls), len(ledger.receipts)
	replay := requestWithMutationKeyForTest(t, server, owner, http.MethodPost, "/api/workspaces", body, "workspace-create-canonical-replay")
	if replay.Code != http.StatusConflict || len(calls) != beforeCalls || len(ledger.receipts) != beforeReceipts {
		t.Fatalf("incompatible replay=%d body=%s calls=%d/%d receipts=%d/%d", replay.Code, replay.Body.String(), len(calls), beforeCalls, len(ledger.receipts), beforeReceipts)
	}
	rows, _ = store.ListWorkspaces(context.Background(), "acct-alpha")
	if len(rows) != 1 || rows[0]["state"] != "suspended" || stringValue(rows[0]["currentComputeAllocationId"]) != "" || rows[0]["autoRenew"] != false || rows[0]["paidThrough"] != paidThrough {
		t.Fatalf("replay overwrote canonical Workspace: %#v", rows)
	}
}

func TestCreateWorkspaceWaitsForFabricRuntimeReadiness(t *testing.T) {
	fabric := &fakeFabricClient{runtime: clients.WorkspaceRuntime{
		ID: "runtime-from-fabric", URL: "https://workspace.medopl.cn/w/ws-from-fabric/", Status: "unready", ServiceName: "opl-compute-from-fabric", Ready: false,
		Access: clients.WorkspaceRuntimeAccess{Username: "opl", Password: "runtime-password-alpha", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-compute-from-fabric-env"},
	}}
	server := NewServer(newTestService(fakeLedgerClient{}, fabric))
	session := tenantOwnerSessionForTest(t, server)
	compute := createResourceWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	storage := createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","sizeGb":10,"computeAllocationId":"`+stringValue(compute["id"])+`"}`)
	createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-attachments", `{"workspaceId":"ws-alpha","computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`","mountPath":"/data"}`)

	workspace := createResourceWithSession(t, server, session, http.MethodPost, "/api/workspaces", `{"accountId":"acct-alpha","ownerId":"usr-owner","workspaceName":"Alpha Lab","attachmentId":"attachment-from-fabric"}`)
	access := workspace["access"].(map[string]any)
	if workspace["state"] != "unready" || workspace["openable"] != false || workspace["accessState"] != "distributing" {
		t.Fatalf("unready Fabric runtime must stay closed: %#v", workspace)
	}
	if access["password"] != nil || access["tokenStatus"] != nil || access["requiresLogin"] != nil {
		t.Fatalf("workspace projection leaked password or fake URL auth fields: %#v", access)
	}
}

func TestWorkspaceCreateRetriesSameClaimAfterGatewayFailure(t *testing.T) {
	fabric := &fakeFabricClient{gatewaySecretErr: errors.New("gateway temporarily unavailable")}
	server := NewServer(newTestService(fakeLedgerClient{}, fabric))
	owner := tenantOwnerSessionForTest(t, server)
	attachment := createWorkspaceAttachmentForTest(t, server, owner, "workspace-create-retry")
	body := `{"workspaceName":"Retryable","attachmentId":"` + stringValue(attachment["id"]) + `"}`

	first := requestWithMutationKeyForTest(t, server, owner, http.MethodPost, "/api/workspaces", body, "workspace-create-retry")
	if first.Code != http.StatusBadGateway || len(fabric.gatewaySecretInputs) != 1 || len(fabric.runtimeInputs) != 0 {
		t.Fatalf("first create status=%d gateway=%d runtime=%d body=%s", first.Code, len(fabric.gatewaySecretInputs), len(fabric.runtimeInputs), first.Body.String())
	}

	fabric.gatewaySecretErr = nil
	retried := requestWithMutationKeyForTest(t, server, owner, http.MethodPost, "/api/workspaces", body, "workspace-create-retry")
	if retried.Code != http.StatusCreated || len(fabric.gatewaySecretInputs) != 2 || len(fabric.runtimeInputs) != 1 {
		t.Fatalf("retried create status=%d gateway=%d runtime=%d body=%s", retried.Code, len(fabric.gatewaySecretInputs), len(fabric.runtimeInputs), retried.Body.String())
	}

	changed := requestWithMutationKeyForTest(t, server, owner, http.MethodPost, "/api/workspaces", `{"workspaceName":"Changed","attachmentId":"`+stringValue(attachment["id"])+`"}`, "workspace-create-retry")
	if changed.Code != http.StatusConflict || !strings.Contains(changed.Body.String(), errPrimaryWorkspaceExists.Error()) || len(fabric.gatewaySecretInputs) != 2 || len(fabric.runtimeInputs) != 1 {
		t.Fatalf("changed replay status=%d gateway=%d runtime=%d body=%s", changed.Code, len(fabric.gatewaySecretInputs), len(fabric.runtimeInputs), changed.Body.String())
	}

	secondAttachment := createWorkspaceAttachmentForTest(t, server, owner, "workspace-create-second")
	second := requestWithMutationKeyForTest(t, server, owner, http.MethodPost, "/api/workspaces", `{"workspaceName":"Second","attachmentId":"`+stringValue(secondAttachment["id"])+`"}`, "workspace-create-second")
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), errPrimaryWorkspaceExists.Error()) || len(fabric.gatewaySecretInputs) != 2 || len(fabric.runtimeInputs) != 1 {
		t.Fatalf("second workspace status=%d gateway=%d runtime=%d body=%s", second.Code, len(fabric.gatewaySecretInputs), len(fabric.runtimeInputs), second.Body.String())
	}
}

func TestResumeWorkspaceRequiresOwningUserBeforeFabric(t *testing.T) {
	store := newMemoryTableStore()
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{
		"id": "workspace-alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha", "ownerUserId": "usr-owner",
		"state": "suspended", "status": "suspended", "storageId": "storage-alpha",
	}))
	fabric := &fakeFabricClient{}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, fabric), store)
	if err != nil {
		t.Fatal(err)
	}

	for _, role := range []string{"member", "admin"} {
		t.Run(role, func(t *testing.T) {
			session := tenantSessionForTest(t, server, role)
			response := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/workspaces/workspace-alpha/resume", `{"computeAllocationId":"compute-new","attachmentId":"attachment-new"}`, "resume-non-owner-"+role)
			if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "workspace_owner_required") || len(fabric.gatewaySecretInputs) != 0 || len(fabric.runtimeInputs) != 0 {
				t.Fatalf("non-owner resume status=%d gateway=%d runtime=%d body=%s", response.Code, len(fabric.gatewaySecretInputs), len(fabric.runtimeInputs), response.Body.String())
			}
		})
	}
}

func TestResumeWorkspaceValidatesRetainedResourcesBeforeFabric(t *testing.T) {
	calls := []string{}
	store := newMemoryTableStore()
	workspace := map[string]any{
		"id": "workspace-alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha", "ownerUserId": "",
		"name": "Alpha Lab", "packageId": "basic", "url": "https://workspace.medopl.cn/w/workspace-alpha/",
		"state": "running", "status": "running", "storageId": "storage-alpha",
		"currentComputeAllocationId": "", "currentAttachmentId": "", "runtimeId": "runtime-old",
		"runtime": map[string]any{"serviceName": "opl-compute-old"}, "runtimeServiceName": "opl-compute-old-root", "serviceName": "opl-compute-old-legacy",
	}
	mustStore(t, store.SaveWorkspace(context.Background(), workspace))
	mustStore(t, store.SaveStorage(context.Background(), map[string]any{"id": "storage-alpha", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "available", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z"}))
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{"id": "compute-replacement", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "running", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z"}))
	mustStore(t, store.SaveAttachment(context.Background(), map[string]any{"id": "attachment-replacement", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "computeAllocationId": "compute-replacement", "storageId": "storage-alpha", "status": "attached"}))
	mustStore(t, store.SaveProjectTaskSyncHead(context.Background(), map[string]any{"id": "project-alpha", "workspaceId": "workspace-alpha", "projectId": "project-alpha", "taskId": "task-alpha", "version": int64(7)}))
	fabric := &fakeFabricClient{calls: &calls}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, fabric), store)
	if err != nil {
		t.Fatalf("create resume server: %v", err)
	}
	admin := operatorSessionForTest(t, server)
	ownerUser := createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"owner-resume@lab.example","accountId":"acct-alpha","role":"owner","password":"CorrectHorseBatteryStaple!","sub2apiUserId":41}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/organizations/members", `{"organizationId":"org-alpha","userId":"`+stringValue(ownerUser["id"])+`","accountId":"acct-alpha","role":"owner"}`)
	workspace["ownerUserId"] = ownerUser["id"]
	mustStore(t, store.SaveWorkspace(context.Background(), workspace))
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"outside-resume@lab.example","accountId":"acct-beta","role":"member","password":"CorrectHorseBatteryStaple!","sub2apiUserId":42}`)
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
	if forbidden.Code != http.StatusForbidden || !strings.Contains(forbidden.Body.String(), "account_scope_forbidden") || len(calls) != before {
		t.Fatalf("cross-account resume = %d calls=%#v body=%s", forbidden.Code, calls[before:], forbidden.Body.String())
	}

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
	if got := calls[before:]; !slices.Equal(got, []string{"fabric.gateway-secret", "fabric.runtime"}) {
		t.Fatalf("resume Fabric calls = %#v", got)
	}
	if len(fabric.gatewaySecretInputs) != 1 || fabric.gatewaySecretInputs[0].AccountID != "acct-alpha" || fabric.gatewaySecretInputs[0].GatewayAPIKey != "workspace-key-secret" ||
		len(fabric.runtimeInputs) != 1 || fabric.runtimeInputs[0].GatewaySecretRef != "opl-gateway-acct-alpha" {
		t.Fatalf("resume Gateway Secret handoff = secret %#v runtime %#v", fabric.gatewaySecretInputs, fabric.runtimeInputs)
	}
	replayed := requestWithSession(t, server, owner, http.MethodPost, "/api/workspaces/workspace-alpha/resume", body)
	if replayed.Code != http.StatusOK || len(calls[before:]) != 2 {
		t.Fatalf("resume replay = %d calls=%#v body=%s", replayed.Code, calls[before:], replayed.Body.String())
	}
	var replayedResult map[string]any
	if err := json.NewDecoder(replayed.Body).Decode(&replayedResult); err != nil || !reflect.DeepEqual(replayedResult, result) {
		t.Fatalf("resume replay changed prior result: first=%#v replay=%#v err=%v", result, replayedResult, err)
	}
	changed := requestWithSession(t, server, owner, http.MethodPost, "/api/workspaces/workspace-alpha/resume", `{"computeAllocationId":"compute-other","attachmentId":"attachment-replacement"}`)
	if changed.Code != http.StatusConflict || !strings.Contains(changed.Body.String(), "idempotency_conflict") || len(calls[before:]) != 2 {
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
	mustStore(t, store.SaveUser(context.Background(), map[string]any{"id": "usr-admin", "email": "admin@medopl.cn", "accountId": "acct-alpha", "role": "owner", "status": "active", "passwordHash": hash}))
	mustStore(t, store.SaveAccount(context.Background(), map[string]any{"id": "acct-alpha", "status": "active", "sub2apiUserId": int64(41)}))
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{"id": "workspace-alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha", "ownerUserId": "usr-admin", "state": "suspended", "status": "suspended", "storageId": "storage-alpha", "url": "https://workspace.medopl.cn/w/workspace-alpha/"}))
	mustStore(t, store.SaveStorage(context.Background(), map[string]any{"id": "storage-alpha", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "available", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z"}))
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{"id": "compute-new", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "running", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z"}))
	mustStore(t, store.SaveAttachment(context.Background(), map[string]any{"id": "attachment-new", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "computeAllocationId": "compute-new", "storageId": "storage-alpha", "status": "attached"}))
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
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
	workspace := map[string]any{"id": "workspace-alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha", "state": "suspended", "status": "suspended", "storageId": "storage-alpha", "url": "https://workspace.medopl.cn/w/workspace-alpha/", "runtime": map[string]any{"serviceName": "old-nested"}, "runtimeServiceName": "old-root", "serviceName": "old-legacy", "access": map[string]any{"account": "opl", "username": "opl", "credentialStatus": "configured", "credentialVersion": "v1", "secretRef": "old-secret"}}
	mustStore(t, store.SaveWorkspace(context.Background(), workspace))
	mustStore(t, store.SaveStorage(context.Background(), map[string]any{"id": "storage-alpha", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "available", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z"}))
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{"id": "compute-new", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "running", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z"}))
	mustStore(t, store.SaveAttachment(context.Background(), map[string]any{"id": "attachment-new", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "computeAllocationId": "compute-new", "storageId": "storage-alpha", "status": "attached"}))
	runtime := clients.WorkspaceRuntime{ID: "runtime-new", WorkspaceID: "workspace-alpha", Status: "running", Ready: false}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{runtime: runtime}), store)
	if err != nil {
		t.Fatalf("create provisioning resume server: %v", err)
	}
	session := tenantOwnerSessionForTest(t, server)
	workspace["ownerUserId"] = sessionUserIDForTest(t, server, session)
	mustStore(t, store.SaveWorkspace(context.Background(), workspace))
	response := requestWithSession(t, server, session, http.MethodPost, "/api/workspaces/workspace-alpha/resume", `{"computeAllocationId":"compute-new","attachmentId":"attachment-new"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("provisioning resume status = %d: %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode provisioning resume: %v", err)
	}
	access := mapField(body, "access")
	if body["state"] != "unready" || body["openable"] != false || access["tokenStatus"] != nil || access["credentialStatus"] != "configured" || access["secretRef"] != nil {
		t.Fatalf("unready runtime became openable or cleared credentials: %#v", body)
	}
	stored, _ := store.ListWorkspaces(context.Background(), "")
	if stringValue(nested(stored[0], "runtime", "serviceName")) != "" || stringValue(stored[0]["runtimeServiceName"]) != "" || stringValue(stored[0]["serviceName"]) != "" || stringValue(nested(stored[0], "access", "secretRef")) != "old-secret" {
		t.Fatalf("provisioning resume kept stale service pointers: %#v", stored[0])
	}
}

func TestConcurrentWorkspaceResumeWaitsForResourceMutation(t *testing.T) {
	store := newMemoryTableStore()
	workspace := map[string]any{"id": "workspace-alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha", "state": "suspended", "status": "suspended", "storageId": "storage-alpha", "url": "https://workspace.medopl.cn/w/workspace-alpha/"}
	mustStore(t, store.SaveWorkspace(context.Background(), workspace))
	mustStore(t, store.SaveStorage(context.Background(), map[string]any{"id": "storage-alpha", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "available", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z"}))
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{"id": "compute-new", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "running", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z"}))
	mustStore(t, store.SaveAttachment(context.Background(), map[string]any{"id": "attachment-new", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "computeAllocationId": "compute-new", "storageId": "storage-alpha", "status": "attached"}))
	fabric := &blockingResumeFabricClient{fakeFabricClient: fakeFabricClient{}, entered: make(chan struct{}, 2), release: make(chan struct{})}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, fabric), store)
	if err != nil {
		t.Fatalf("create resume server: %v", err)
	}
	session := tenantOwnerSessionForTest(t, server)
	workspace["ownerUserId"] = sessionUserIDForTest(t, server, session)
	mustStore(t, store.SaveWorkspace(context.Background(), workspace))
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
		close(fabric.release)
		<-first
		t.Fatalf("second resume crossed resource lock with status %d: %s", response.Code, response.Body.String())
	case <-time.After(50 * time.Millisecond):
	}
	close(fabric.release)
	if response := <-first; response.Code != http.StatusOK {
		t.Fatalf("first resume status = %d: %s", response.Code, response.Body.String())
	}
	select {
	case response := <-second:
		if response.Code != http.StatusConflict {
			t.Fatalf("second resume status = %d: %s", response.Code, response.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("second resume did not resolve after resource unlock")
	}
}

func TestComputePoolsReadFabricCatalog(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &catalogFabricClient{}))
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
	server := NewServer(newTestService(fakeLedgerClient{}, &catalogFabricClient{}))
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

func TestPricingPackageAvailabilityFollowsFabricComputePools(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "console state", path: "/api/state?accountId=acct-alpha"},
		{name: "pricing catalog", path: "/api/pricing/catalog"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := NewServer(newTestService(fakeLedgerClient{}, &unavailableProCatalogFabricClient{}))
			response := requestWithSession(t, server, tenantAdminSessionForTest(t, server), http.MethodGet, tc.path, "")
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var body map[string]any
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			packages, _ := body["packages"].([]any)
			availability := map[string]bool{}
			for _, raw := range packages {
				pkg, _ := raw.(map[string]any)
				availability[stringValue(pkg["id"])] = pkg["available"] == true
			}
			if !availability["basic"] || availability["pro"] {
				t.Fatalf("package availability=%#v body=%#v", availability, body)
			}
		})
	}
}

func TestWorkspaceReceiptFailureKeepsSucceededRuntimeForReplay(t *testing.T) {
	ledger := &flakyWorkspaceReceiptLedger{}
	fabric := &fakeFabricClient{}
	sub2API := &testSub2APIClient{balance: 1_000_000_000_000, charges: map[string]int64{}}
	server := NewServer(controlplane.NewService(ledger, fabric, sub2API))
	session := tenantOwnerSessionForTest(t, server)
	compute := createResourceWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", `{"packageId":"basic"}`)
	storage := createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"sizeGb":10,"computeAllocationId":"`+stringValue(compute["id"])+`"}`)
	attachment := createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-attachments", `{"computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`"}`)
	body := `{"attachmentId":"` + stringValue(attachment["id"]) + `","workspaceName":"Receipt Replay"}`

	first := requestWithSession(t, server, session, http.MethodPost, "/api/workspaces", body)
	if first.Code != http.StatusBadGateway {
		t.Fatalf("first receipt outage status=%d body=%s", first.Code, first.Body.String())
	}
	state := requestWithSession(t, server, session, http.MethodGet, "/api/state?accountId=acct-alpha", "")
	if state.Code != http.StatusOK || !strings.Contains(state.Body.String(), stringValue(attachment["workspaceId"])) || !strings.Contains(state.Body.String(), "runtime-from-fabric") {
		t.Fatalf("receipt-pending workspace was not persisted: status=%d body=%s", state.Code, state.Body.String())
	}
	second := requestWithSession(t, server, session, http.MethodPost, "/api/workspaces", body)
	if second.Code != http.StatusCreated || strings.Contains(second.Body.String(), "receipt-from-ledger") {
		t.Fatalf("receipt replay status=%d body=%s", second.Code, second.Body.String())
	}
	if ledger.receiptCalls != 2 || len(sub2API.workspaceKeyUserIDs) != 3 || slices.ContainsFunc(sub2API.workspaceKeyUserIDs, func(id int64) bool { return id != 41 }) || len(fabric.gatewaySecretInputs) != 1 || len(fabric.runtimeInputs) != 1 {
		t.Fatalf("receipt replay repeated side effects: receipts=%d keys=%#v secrets=%#v runtimes=%#v", ledger.receiptCalls, sub2API.workspaceKeyUserIDs, fabric.gatewaySecretInputs, fabric.runtimeInputs)
	}
}

func TestWorkspaceCreatePreservesFabricConflictStatus(t *testing.T) {
	t.Setenv("OPL_TENCENT_ZONE", "ap-shanghai-2")
	t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "S5.MEDIUM4")
	fabric := &fakeFabricClient{}
	server := NewServer(newTestService(fakeLedgerClient{}, fabric))
	session := tenantOwnerSessionForTest(t, server)
	compute := createResourceWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", `{"packageId":"basic"}`)
	storage := createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"sizeGb":10,"computeAllocationId":"`+stringValue(compute["id"])+`"}`)
	attachment := createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-attachments", `{"computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`"}`)
	fabric.runtimeErr = &clients.FabricHTTPError{StatusCode: http.StatusConflict, Body: `{"error":"runtime_conflict"}`}

	response := requestWithSession(t, server, session, http.MethodPost, "/api/workspaces", `{"attachmentId":"`+stringValue(attachment["id"])+`"}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "upstream_conflict") {
		t.Fatalf("workspace conflict status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWorkspaceCreatePreservesGatewaySecretConflictStatus(t *testing.T) {
	fabric := &fakeFabricClient{gatewaySecretErr: &clients.FabricHTTPError{StatusCode: http.StatusConflict, Body: `{"error":"secret_conflict"}`}}
	server := NewServer(newTestService(fakeLedgerClient{}, fabric))
	session := tenantOwnerSessionForTest(t, server)
	compute := createResourceWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", `{"packageId":"basic"}`)
	storage := createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"sizeGb":10,"computeAllocationId":"`+stringValue(compute["id"])+`"}`)
	attachment := createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-attachments", `{"computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`"}`)

	response := requestWithSession(t, server, session, http.MethodPost, "/api/workspaces", `{"attachmentId":"`+stringValue(attachment["id"])+`"}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "upstream_conflict") {
		t.Fatalf("gateway Secret conflict status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWorkspaceCreateBindsAuthenticatedOwnerForGatewaySecretRotation(t *testing.T) {
	store := newMemoryTableStore()
	passwordHash, err := hashPassword("CorrectHorseBatteryStaple!")
	if err != nil {
		t.Fatal(err)
	}
	mustStore(t, store.SaveAccount(context.Background(), map[string]any{"id": "acct-alpha", "status": "active", "sub2apiUserId": int64(41)}))
	mustStore(t, store.SaveOrganization(context.Background(), map[string]any{"id": "org-alpha", "billingAccountId": "acct-alpha", "status": "active"}))
	for _, user := range []map[string]any{
		{"id": "usr-alpha-owner", "email": "alpha-owner@example.com", "accountId": "acct-alpha", "role": "owner", "status": "active", "passwordHash": passwordHash},
		{"id": "usr-alpha-member", "email": "alpha-member@example.com", "accountId": "acct-alpha", "role": "member", "status": "active", "passwordHash": passwordHash},
	} {
		mustStore(t, store.SaveUser(context.Background(), user))
		mustStore(t, store.SaveMembership(context.Background(), map[string]any{
			"id": "mem-" + stringValue(user["id"]), "organizationId": "org-alpha", "userId": user["id"],
			"accountId": "acct-alpha", "role": user["role"], "status": "active",
		}))
	}
	paidThrough := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	compute := monthlyActiveResource("compute", "compute-alpha", paidThrough)
	compute["accountId"], compute["workspaceId"], compute["ownerUserId"] = "acct-alpha", "workspace-alpha", "usr-alpha-owner"
	storage := monthlyActiveResource("storage", "storage-alpha", paidThrough)
	storage["accountId"], storage["workspaceId"], storage["ownerUserId"], storage["computeAllocationId"] = "acct-alpha", "workspace-alpha", "usr-alpha-owner", "compute-alpha"
	mustStore(t, store.SaveCompute(context.Background(), compute))
	mustStore(t, store.SaveStorage(context.Background(), storage))
	mustStore(t, store.SaveAttachment(context.Background(), map[string]any{
		"id": "attachment-alpha", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "packageId": "basic",
		"computeAllocationId": "compute-alpha", "storageId": "storage-alpha", "status": "attached",
	}))

	sub2API := &testSub2APIClient{balance: 1_000_000_000_000, charges: map[string]int64{}}
	fabric := &fakeFabricClient{}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, fabric, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	owner := loginForTest(t, server, "alpha-owner@example.com", "CorrectHorseBatteryStaple!")
	member := loginForTest(t, server, "alpha-member@example.com", "CorrectHorseBatteryStaple!")
	body := `{"workspaceName":"Alpha Workspace","attachmentId":"attachment-alpha"}`

	created := requestWithSession(t, server, owner, http.MethodPost, "/api/workspaces", body)
	if created.Code != http.StatusCreated {
		t.Fatalf("owner create status=%d body=%s", created.Code, created.Body.String())
	}
	rotated := requestWithSession(t, server, owner, http.MethodPost, "/api/workspaces/workspace-alpha/gateway-secret/rotate", `{"reason":"owner-request"}`)
	if rotated.Code != http.StatusOK {
		t.Fatalf("owner create then rotate status=%d body=%s", rotated.Code, rotated.Body.String())
	}
	workspaces, err := store.ListWorkspaces(context.Background(), "acct-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if workspace := findRecord(workspaces, "workspace-alpha"); stringValue(workspace["ownerUserId"]) != "usr-alpha-owner" {
		t.Fatalf("persisted workspace owner=%#v, want authenticated owner", workspace)
	}
	if response := requestWithSession(t, server, member, http.MethodPost, "/api/workspaces", body); response.Code != http.StatusForbidden {
		t.Fatalf("member create status=%d body=%s", response.Code, response.Body.String())
	}
	forged := `{"workspaceName":"Alpha Workspace","attachmentId":"attachment-alpha","ownerId":"usr-alpha-owner"}`
	if response := requestWithSession(t, server, member, http.MethodPost, "/api/workspaces", forged); response.Code != http.StatusForbidden {
		t.Fatalf("forged owner create status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWorkspaceCreateRejectsSecondPrimaryBeforeFabric(t *testing.T) {
	calls := []string{}
	fabric := &fakeFabricClient{calls: &calls}
	server := NewServer(newTestService(fakeLedgerClient{}, fabric))
	owner := tenantOwnerSessionForTest(t, server)

	firstAttachment := createWorkspaceAttachmentForTest(t, server, owner, "primary-first")
	first := requestWithMutationKeyForTest(t, server, owner, http.MethodPost, "/api/workspaces", `{"workspaceName":"Primary","attachmentId":"`+stringValue(firstAttachment["id"])+`"}`, "primary-first-workspace")
	if first.Code != http.StatusCreated {
		t.Fatalf("first Workspace status=%d body=%s", first.Code, first.Body.String())
	}
	secondAttachment := createWorkspaceAttachmentForTest(t, server, owner, "primary-second")
	beforeSecond := len(calls)
	second := requestWithMutationKeyForTest(t, server, owner, http.MethodPost, "/api/workspaces", `{"workspaceName":"Second","attachmentId":"`+stringValue(secondAttachment["id"])+`"}`, "primary-second-workspace")
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "primary_workspace_already_exists") {
		t.Fatalf("second Workspace status=%d body=%s", second.Code, second.Body.String())
	}
	if len(calls) != beforeSecond {
		t.Fatalf("second Workspace reached Fabric: before=%d calls=%#v", beforeSecond, calls[beforeSecond:])
	}
}

func TestWorkspaceCreateConcurrentClaimsReachFabricOnce(t *testing.T) {
	fabric := &countingWorkspaceFabricClient{}
	server := NewServer(newTestService(fakeLedgerClient{}, fabric))
	owner := tenantOwnerSessionForTest(t, server)
	attachments := []map[string]any{
		createWorkspaceAttachmentForTest(t, server, owner, "concurrent-first"),
		createWorkspaceAttachmentForTest(t, server, owner, "concurrent-second"),
	}
	requests := make([]*http.Request, 0, len(attachments))
	for index, attachment := range attachments {
		req := httptest.NewRequest(http.MethodPost, "/api/workspaces", bytes.NewBufferString(`{"workspaceName":"Concurrent","attachmentId":"`+stringValue(attachment["id"])+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", fmt.Sprintf("concurrent-workspace-%d", index))
		addAuth(req, owner)
		requests = append(requests, req)
	}

	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, len(requests))
	var workers sync.WaitGroup
	for _, req := range requests {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			responses <- rec
		}()
	}
	close(start)
	workers.Wait()
	close(responses)

	created, conflicted := 0, 0
	for response := range responses {
		switch response.Code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			if !strings.Contains(response.Body.String(), "primary_workspace_already_exists") {
				t.Fatalf("unexpected conflict body=%s", response.Body.String())
			}
			conflicted++
		default:
			t.Fatalf("concurrent Workspace status=%d body=%s", response.Code, response.Body.String())
		}
	}
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	if created != 1 || conflicted != 1 || fabric.gatewayWrites != 1 || fabric.runtimeCreates != 1 {
		t.Fatalf("concurrent results created=%d conflicted=%d gateway=%d runtime=%d", created, conflicted, fabric.gatewayWrites, fabric.runtimeCreates)
	}
}

func TestWorkspaceOwnerRotatesGatewaySecretIdempotentlyWithoutPersistingKey(t *testing.T) {
	store := newMemoryTableStore()
	passwordHash, err := hashPassword("CorrectHorseBatteryStaple!")
	if err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []struct {
		accountID, organizationID, userID, email string
		sub2APIUserID                            int64
	}{
		{accountID: "acct-alpha", organizationID: "org-alpha", userID: "usr-alpha", email: "alpha@example.com", sub2APIUserID: 41},
		{accountID: "acct-beta", organizationID: "org-beta", userID: "usr-beta", email: "beta@example.com", sub2APIUserID: 42},
	} {
		mustStore(t, store.SaveAccount(context.Background(), map[string]any{"id": tenant.accountID, "status": "active", "sub2apiUserId": tenant.sub2APIUserID}))
		mustStore(t, store.SaveOrganization(context.Background(), map[string]any{"id": tenant.organizationID, "billingAccountId": tenant.accountID, "status": "active"}))
		mustStore(t, store.SaveUser(context.Background(), map[string]any{"id": tenant.userID, "email": tenant.email, "accountId": tenant.accountID, "role": "owner", "status": "active", "passwordHash": passwordHash}))
		mustStore(t, store.SaveMembership(context.Background(), map[string]any{"id": "mem-" + tenant.userID, "organizationId": tenant.organizationID, "userId": tenant.userID, "accountId": tenant.accountID, "role": "owner", "status": "active"}))
	}
	mustStore(t, store.SaveUser(context.Background(), map[string]any{"id": "usr-alpha-member", "email": "alpha-member@example.com", "accountId": "acct-alpha", "role": "member", "status": "active", "passwordHash": passwordHash}))
	mustStore(t, store.SaveMembership(context.Background(), map[string]any{"id": "mem-usr-alpha-member", "organizationId": "org-alpha", "userId": "usr-alpha-member", "accountId": "acct-alpha", "role": "member", "status": "active"}))
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{"id": "compute-alpha", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "running", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z"}))
	mustStore(t, store.SaveStorage(context.Background(), map[string]any{"id": "storage-alpha", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "available", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z"}))
	mustStore(t, store.SaveAttachment(context.Background(), map[string]any{"id": "attachment-alpha", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "computeAllocationId": "compute-alpha", "storageId": "storage-alpha", "status": "attached"}))
	mustStore(t, store.SaveWorkspace(context.Background(), workspaceGatewayTestRow(map[string]any{
		"id": "workspace-alpha", "accountId": "acct-alpha", "ownerUserId": "usr-alpha", "state": "running", "currentComputeAllocationId": "compute-alpha", "storageId": "storage-alpha", "attachmentId": "attachment-alpha", "currentAttachmentId": "attachment-alpha",
		"runtime": map[string]any{"serviceName": "runtime-alpha", "status": "running", "ready": true},
	})))

	sub2API := &testSub2APIClient{workspaceKey: clients.Sub2APIWorkspaceKey{ID: 9, UserID: 41, Name: "opl-workspace", Key: "rotated-workspace-key", Status: "active"}}
	fabric := &fakeFabricClient{gatewaySecret: clients.GatewaySecretWriteResult{SecretRef: "opl-gateway-acct-alpha", Version: "v1", Fingerprint: "sha256:first"}}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, fabric, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	owner := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	requestRotate := func(session *httptest.ResponseRecorder, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/workspaces/workspace-alpha/gateway-secret/rotate", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		addAuth(req, session)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}

	first := requestRotate(owner, "rotate-once", `{"reason":"owner-request"}`)
	replay := requestRotate(owner, "rotate-once", `{"reason":"owner-request"}`)
	conflict := requestRotate(owner, "rotate-once", `{"reason":"different-request"}`)
	if first.Code != http.StatusOK || replay.Code != http.StatusOK || conflict.Code != http.StatusConflict {
		t.Fatalf("rotate statuses first=%d %s replay=%d %s conflict=%d %s", first.Code, first.Body.String(), replay.Code, replay.Body.String(), conflict.Code, conflict.Body.String())
	}
	if len(fabric.gatewaySecretInputs) != 1 || len(sub2API.workspaceKeyUserIDs) != 1 || sub2API.workspaceKeyUserIDs[0] != 41 {
		t.Fatalf("idempotent rotate inputs=%#v key users=%#v", fabric.gatewaySecretInputs, sub2API.workspaceKeyUserIDs)
	}
	if strings.Contains(first.Body.String(), "rotated-workspace-key") || !strings.Contains(first.Body.String(), "opl-gateway-acct-alpha") || !strings.Contains(first.Body.String(), "sha256:first") {
		t.Fatalf("rotate response leaked or omitted evidence: %s", first.Body.String())
	}

	sub2API.mu.Lock()
	sub2API.workspaceKey = clients.Sub2APIWorkspaceKey{ID: 10, UserID: 41, Name: "opl-workspace", Key: "rotated-workspace-key-v2", Status: "active"}
	sub2API.mu.Unlock()
	fabric.gatewaySecret = clients.GatewaySecretWriteResult{SecretRef: "opl-gateway-acct-alpha", Version: "v2", Fingerprint: "sha256:second"}
	second := requestRotate(owner, "rotate-twice", `{"reason":"key-changed"}`)
	if second.Code != http.StatusOK || len(fabric.gatewaySecretInputs) != 2 {
		t.Fatalf("second rotate status=%d body=%s inputs=%#v", second.Code, second.Body.String(), fabric.gatewaySecretInputs)
	}

	operations, err := store.ListRuntimeOperations(context.Background())
	if err != nil || len(operations) != 2 {
		t.Fatalf("secret operations=%#v err=%v", operations, err)
	}
	persisted := string(mustJSON(operations))
	for _, forbidden := range []string{"rotated-workspace-key", "rotated-workspace-key-v2", `"version"`, `"gatewayApiKey"`} {
		if strings.Contains(persisted, forbidden) {
			t.Fatalf("secret operation persisted %q: %s", forbidden, persisted)
		}
	}
	if !strings.Contains(persisted, "opl-gateway-acct-alpha") || !strings.Contains(persisted, "sha256:first") || !strings.Contains(persisted, "sha256:second") {
		t.Fatalf("secret operation omitted safe evidence: %s", persisted)
	}

	beta := loginForTest(t, server, "beta@example.com", "CorrectHorseBatteryStaple!")
	member := loginForTest(t, server, "alpha-member@example.com", "CorrectHorseBatteryStaple!")
	if response := requestRotate(beta, "rotate-foreign", `{}`); response.Code != http.StatusForbidden {
		t.Fatalf("foreign owner rotate status=%d body=%s", response.Code, response.Body.String())
	}
	if response := requestRotate(member, "rotate-member", `{}`); response.Code != http.StatusForbidden {
		t.Fatalf("member rotate status=%d body=%s", response.Code, response.Body.String())
	}
	workspaces, err := store.ListWorkspaces(context.Background(), "acct-alpha")
	if err != nil {
		t.Fatal(err)
	}
	workspace := findRecord(workspaces, "workspace-alpha")
	workspace["state"] = "suspended"
	mustStore(t, store.SaveWorkspace(context.Background(), workspace))
	if response := requestRotate(owner, "rotate-suspended", `{}`); response.Code != http.StatusConflict {
		t.Fatalf("suspended rotate status=%d body=%s", response.Code, response.Body.String())
	}
	if len(fabric.gatewaySecretInputs) != 2 {
		t.Fatalf("rejected rotate reached Fabric: %#v", fabric.gatewaySecretInputs)
	}
}

func TestStorageDestroyPreservesRetainedProviderStatus(t *testing.T) {
	fabric := &fakeFabricClient{storageDestroyStatus: "retained"}
	server := NewServer(newTestService(fakeLedgerClient{}, fabric))
	session := tenantAdminSessionForTest(t, server)
	compute := createResourceWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", `{"packageId":"basic"}`)
	storage := createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"sizeGb":10,"computeAllocationId":"`+stringValue(compute["id"])+`"}`)

	response := requestWithSession(t, server, session, http.MethodPost, "/api/storage-volumes/destroy", `{"storageId":"`+stringValue(storage["id"])+`","confirmDataLoss":true}`)
	var body map[string]any
	if response.Code != http.StatusOK || json.NewDecoder(response.Body).Decode(&body) != nil || body["status"] != "retained" || body["desiredStatus"] != "destroyed" || body["billingStatus"] != "stopped" {
		t.Fatalf("retained destroy status=%d body=%#v", response.Code, body)
	}
}

func TestProviderReconcilePreservesReleasedStorageStatus(t *testing.T) {
	app := newControlPlaneAppEmpty()
	row := map[string]any{"id": "storage-reconcile-released", "accountId": "acct-alpha", "status": "available", "desiredStatus": "destroyed", "billingStatus": "active"}
	mustStore(t, app.tables.SaveStorage(context.Background(), row))
	fabric := &fakeFabricClient{storageDestroyStatus: "released"}

	if err := app.reconcileMonthlyStorage(context.Background(), newTestService(fakeLedgerClient{}, fabric), row, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	stored, _ := app.getStorage("storage-reconcile-released")
	if stored["status"] != "released" || stored["desiredStatus"] != "destroyed" || stored["billingStatus"] != "stopped" {
		t.Fatalf("reconciled storage=%#v", stored)
	}
}

func TestProviderReconcileDoesNotOverwriteManualReview(t *testing.T) {
	for _, resourceType := range []string{"compute", "storage"} {
		t.Run(resourceType, func(t *testing.T) {
			app := newControlPlaneAppEmpty()
			row := map[string]any{
				"id": resourceType + "-manual-review", "accountId": "acct-alpha", "status": "running",
				"desiredStatus": "destroyed", "billingStatus": "manual_review", "manualReviewReason": "provider_unknown",
			}
			var err error
			service := newTestService(fakeLedgerClient{}, &fakeFabricClient{})
			if resourceType == "storage" {
				row["status"] = "available"
				mustStore(t, app.tables.SaveStorage(context.Background(), row))
				err = app.reconcileMonthlyStorage(context.Background(), service, row, time.Now().UTC())
				row, _ = app.getStorage(stringValue(row["id"]))
			} else {
				mustStore(t, app.tables.SaveCompute(context.Background(), row))
				err = app.reconcileMonthlyCompute(context.Background(), service, row, time.Now().UTC())
				row, _ = app.getCompute(stringValue(row["id"]))
			}
			if err != nil || row["billingStatus"] != "manual_review" || row["manualReviewReason"] != "provider_unknown" {
				t.Fatalf("reconciled manual review=%#v err=%v", row, err)
			}
		})
	}
}

func TestWorkspaceRuntimeStatusPassesFabricChecksWithoutCredentials(t *testing.T) {
	calls := []string{}
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{calls: &calls}))
	session := tenantOwnerSessionForTest(t, server)
	compute := createResourceWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	storage := createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","sizeGb":10,"computeAllocationId":"`+stringValue(compute["id"])+`"}`)
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
	if access["username"] != "opl" || access["credentialStatus"] != "configured" || access["password"] != nil || access["secretRef"] != nil {
		t.Fatalf("runtime status must return safe credential metadata only: %#v", body)
	}
	checks := body["checks"].([]any)
	if len(checks) != 2 || checks[0].(map[string]any)["name"] != "deployment_ready" || checks[1].(map[string]any)["name"] != "service_endpoints_ready" {
		t.Fatalf("runtime checks must pass through Fabric details: %#v", body["checks"])
	}
}

func TestWorkspaceRuntimeStatusPromotesProjectionWithoutPersistingPassword(t *testing.T) {
	store := newMemoryTableStore()
	fabric := &fakeFabricClient{runtimeStatus: clients.WorkspaceRuntime{
		ID: "runtime-from-fabric", WorkspaceID: "ws-alpha", URL: "https://workspace.medopl.cn/w/ws-alpha/", Status: "running", ServiceName: "opl-compute-from-fabric", Ready: true,
		Access: clients.WorkspaceRuntimeAccess{Username: "opl", Password: "runtime-password-alpha", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-compute-from-fabric-env"},
		Checks: []any{map[string]any{"name": "service_endpoints_ready", "ok": true}},
	}}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, fabric), store)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	session := tenantAdminSessionForTest(t, server)
	seedRuntimeAccessWorkspaceForTest(t, store, sessionUserIDForTest(t, server, session), map[string]any{
		"state": "unready", "status": "unready", "url": "https://workspace.medopl.cn/w/ws-alpha/",
		"runtime": map[string]any{"serviceName": "opl-compute-from-fabric", "status": "unready", "ready": false},
	})

	response := requestWithSession(t, server, session, http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"ws-alpha"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("runtime status = %d: %s", response.Code, response.Body.String())
	}
	var runtime map[string]any
	if err := json.NewDecoder(response.Body).Decode(&runtime); err != nil {
		t.Fatalf("decode runtime status: %v", err)
	}
	if runtime["ready"] != true || nested(runtime, "access", "credentialStatus") != "configured" || nested(runtime, "access", "password") != nil || nested(runtime, "access", "secretRef") != nil {
		t.Fatalf("runtime status must return ready state without credentials: %#v", runtime)
	}
	stored, err := store.ListWorkspaces(context.Background(), "acct-alpha")
	if err != nil || len(stored) != 1 {
		t.Fatalf("list workspaces: rows=%#v err=%v", stored, err)
	}
	projection := workspaceResponse(stored[0])
	if projection["state"] != "running" || projection["openable"] != true || nested(projection, "runtime", "ready") != true {
		t.Fatalf("ready runtime must promote persisted projection: %#v", projection)
	}
	if nested(stored[0], "access", "password") != nil {
		t.Fatalf("runtime password must not be persisted: %#v", stored[0])
	}
}

func TestWorkspaceRuntimeStatusDoesNotPromoteSuspendedProjection(t *testing.T) {
	calls := []string{}
	store := newMemoryTableStore()
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{
		"id": "ws-alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha", "state": "suspended", "status": "suspended",
		"computeAllocationId": "compute-alpha", "storageId": "storage-alpha", "attachmentId": "attachment-alpha",
	}))
	fabric := &fakeFabricClient{calls: &calls, runtimeStatus: clients.WorkspaceRuntime{ID: "runtime-from-fabric", WorkspaceID: "ws-alpha", Status: "running", Ready: true}}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, fabric), store)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	session := tenantAdminSessionForTest(t, server)

	response := requestWithSession(t, server, session, http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"ws-alpha"}`)
	stored, listErr := store.ListWorkspaces(context.Background(), "acct-alpha")
	if response.Code != http.StatusConflict || listErr != nil || len(stored) != 1 || stored[0]["state"] != "suspended" {
		t.Fatalf("suspended runtime status=%d rows=%#v err=%v", response.Code, stored, listErr)
	}
	if slices.Contains(calls, "fabric.runtime-status") {
		t.Fatalf("suspended Workspace must not read Fabric credentials: %#v", calls)
	}
}

func TestWorkspaceURLTokenRoutesDoNotExist(t *testing.T) {
	store := newMemoryTableStore()
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{"id": "ws-alpha", "accountId": "acct-alpha", "state": "running"}))
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	session := tenantAdminSessionForTest(t, server)
	for _, path := range []string{"/api/workspaces/reset-token", "/api/workspaces/delete-token"} {
		response := requestWithSession(t, server, session, http.MethodPost, path, `{"workspaceId":"ws-alpha"}`)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404: %s", path, response.Code, response.Body.String())
		}
	}
}

func TestWorkspaceRuntimeStatusDoesNotReadSecretForUnknownProjection(t *testing.T) {
	calls := []string{}
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{calls: &calls}))
	admin := operatorSessionForTest(t, server)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"outside-unknown@lab.example","accountId":"acct-beta","role":"member","password":"CorrectHorseBatteryStaple!","sub2apiUserId":41}`)
	outsider := loginForTest(t, server, "outside-unknown@lab.example", "CorrectHorseBatteryStaple!")

	before := len(calls)
	response := requestWithSession(t, server, outsider, http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"ws-unknown"}`)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "workspace_not_found") {
		t.Fatalf("unknown runtime status = %d: %s", response.Code, response.Body.String())
	}
	if len(calls) != before || strings.Contains(response.Body.String(), "runtime-password-alpha") {
		t.Fatalf("unknown projection reached Fabric or returned a password: calls=%#v body=%s", calls[before:], response.Body.String())
	}
}

func TestWorkspaceRuntimeStatusForbidsCrossAccountSecretRead(t *testing.T) {
	calls := []string{}
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{calls: &calls}))
	owner := tenantOwnerSessionForTest(t, server)
	createResourceWithSession(t, server, owner, http.MethodPost, "/api/users", `{"email":"outside@lab.example","accountId":"acct-beta","role":"member","password":"CorrectHorseBatteryStaple!","sub2apiUserId":42}`)
	outsider := loginForTest(t, server, "outside@lab.example", "CorrectHorseBatteryStaple!")

	compute := createResourceWithSession(t, server, owner, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	storage := createResourceWithSession(t, server, owner, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","sizeGb":10,"computeAllocationId":"`+stringValue(compute["id"])+`"}`)
	createResourceWithSession(t, server, owner, http.MethodPost, "/api/storage-attachments", `{"workspaceId":"ws-alpha","computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`","mountPath":"/data"}`)
	workspace := createResourceWithSession(t, server, owner, http.MethodPost, "/api/workspaces", `{"accountId":"acct-alpha","ownerId":"usr-owner","workspaceName":"Alpha Lab","attachmentId":"attachment-from-fabric"}`)

	before := len(calls)
	response := requestWithSession(t, server, outsider, http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"`+stringValue(workspace["id"])+`"}`)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "account_scope_forbidden") {
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
	if password := stringValue(nested(stored[0], "access", "password")); password != "" {
		t.Fatalf("memory store retained Workspace password: %q", password)
	}
	workspace := app.state("acct-alpha", nil)["workspaces"].([]any)[0].(map[string]any)
	if password := stringValue(nested(workspace, "access", "password")); password != "" {
		t.Fatalf("Workspace list exposed password: %q", password)
	}
}

func TestSessionFactSurvivesServerRestart(t *testing.T) {
	path := t.TempDir() + "/control-plane-state.sqlite"
	service := newTestService(fakeLedgerClient{}, &fakeFabricClient{})
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

func TestWorkspaceRuntimeStatePersistsAcrossRestart(t *testing.T) {
	setTestOperatorAccount(t, "acct-alpha")
	path := t.TempDir() + "/control-plane-state.sqlite"
	fabric := &fakeFabricClient{
		runtime:       clients.WorkspaceRuntime{ID: "runtime-from-fabric", URL: "https://workspace.medopl.cn/w/ws-from-fabric/", Status: "unready", ServiceName: "opl-compute-from-fabric", Ready: false},
		runtimeStatus: clients.WorkspaceRuntime{ID: "runtime-from-fabric", URL: "https://workspace.medopl.cn/w/ws-from-fabric/", Status: "running", ServiceName: "opl-compute-from-fabric", Ready: true},
	}
	service := newTestService(fakeLedgerClient{}, fabric)
	server, err := NewPersistentServer(service, NewTestEntStateStore(t, path))
	if err != nil {
		t.Fatalf("create persistent server: %v", err)
	}
	session := tenantOwnerSessionForTest(t, server)
	ensureCustomerMembershipForTest(t, server, session, "acct-alpha", "usr-admin")
	compute := createResourceWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	storage := createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","sizeGb":10,"computeAllocationId":"`+stringValue(compute["id"])+`"}`)
	createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-attachments", `{"workspaceId":"ws-alpha","computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`","mountPath":"/data"}`)
	workspace := createResourceWithSession(t, server, session, http.MethodPost, "/api/workspaces", `{"accountId":"acct-alpha","ownerId":"usr-owner","workspaceName":"Alpha Lab","attachmentId":"attachment-from-fabric"}`)
	status := requestWithSession(t, server, session, http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"`+stringValue(workspace["id"])+`"}`)
	if status.Code != http.StatusOK {
		t.Fatalf("runtime status = %d: %s", status.Code, status.Body.String())
	}

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
	if len(workspaces) != 1 || workspaces[0].(map[string]any)["state"] != "running" || workspaces[0].(map[string]any)["openable"] != true || nested(workspaces[0].(map[string]any), "access", "tokenStatus") != nil {
		t.Fatalf("workspace runtime state did not survive restart: %#v", workspaces)
	}
}

func TestBootstrapImportsAdminSeedAndDoesNotExposeLegacyOwner(t *testing.T) {
	t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"id":"usr-admin-production","email":"admin@medopl.cn","password":"StableAdminPass2026!","name":"Admin","role":"admin","accountId":"acct-admin","sub2apiUserId":41}]`)
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))

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
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := operatorSessionForTest(t, server)
	alphaUser := createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"alpha@lab.example","accountId":"acct-alpha","role":"member","password":"CorrectHorseBatteryStaple!","sub2apiUserId":41}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/organizations/members", `{"organizationId":"org-alpha","userId":"`+stringValue(alphaUser["id"])+`","accountId":"acct-alpha","role":"member"}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"beta@lab.example","accountId":"acct-beta","role":"member","password":"CorrectHorseBatteryStaple!","sub2apiUserId":42}`)
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

func TestBillingReconciliationAppendsAuditEvent(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := operatorSessionForTest(t, server)

	createResourceWithSession(t, server, admin, http.MethodPost, "/api/billing/reconciliation", `{"confirm":true}`)

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
		return event["action"] == "billing.reconciliation" && event["actorUserId"] != "" && event["result"] == "succeeded"
	}) {
		t.Fatalf("missing billing reconciliation audit event: %#v", events)
	}
}

func TestCreateUserRejectsDuplicateEmail(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := operatorSessionForTest(t, server)

	createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"pi@lab.example","accountId":"acct-alpha","role":"member","password":"CorrectHorseBatteryStaple!","sub2apiUserId":41}`)
	duplicate := requestWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"PI@lab.example","accountId":"acct-beta","role":"member","password":"CorrectHorseBatteryStaple!","sub2apiUserId":42}`)
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), "user_already_exists") {
		t.Fatalf("duplicate create status=%d body=%s, want 409 user_already_exists", duplicate.Code, duplicate.Body.String())
	}
}

type fakeLedgerClient struct{}

type testSub2APIClient struct {
	mu                  sync.Mutex
	balance             int64
	charges             map[string]int64
	workspaceKey        clients.Sub2APIWorkspaceKey
	workspaceKeyErr     error
	workspaceKeyUserIDs []int64
}

func (*testSub2APIClient) Version(context.Context) (string, error) { return "0.1.155", nil }

func (c *testSub2APIClient) Balance(_ context.Context, userID int64) (clients.Sub2APIBalance, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return clients.Sub2APIBalance{UserID: userID, USDMicros: c.balance, Status: "active"}, nil
}

func (c *testSub2APIClient) WorkspaceKey(_ context.Context, userID int64) (clients.Sub2APIWorkspaceKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.workspaceKeyUserIDs = append(c.workspaceKeyUserIDs, userID)
	if c.workspaceKeyErr != nil {
		return clients.Sub2APIWorkspaceKey{}, c.workspaceKeyErr
	}
	if c.workspaceKey.ID != 0 {
		return c.workspaceKey, nil
	}
	return clients.Sub2APIWorkspaceKey{ID: 9, UserID: userID, Name: "opl-workspace", Key: "workspace-key-secret", Status: "active"}, nil
}

func (c *testSub2APIClient) Charge(_ context.Context, input clients.Sub2APIChargeInput) (clients.Sub2APICharge, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous, ok := c.charges[input.Code]; ok {
		if previous != input.ChargeUSDMicros {
			return clients.Sub2APICharge{}, errors.New("redeem_code_conflict")
		}
		return clients.Sub2APICharge{Code: input.Code, UserID: input.UserID, ChargeUSDMicros: previous, Status: "used"}, nil
	}
	if input.ChargeUSDMicros <= 0 || input.ChargeUSDMicros > c.balance {
		return clients.Sub2APICharge{}, errors.New("insufficient_balance")
	}
	c.balance -= input.ChargeUSDMicros
	c.charges[input.Code] = input.ChargeUSDMicros
	return clients.Sub2APICharge{Code: input.Code, UserID: input.UserID, ChargeUSDMicros: input.ChargeUSDMicros, Status: "used"}, nil
}

func newTestService(ledger clients.LedgerClient, fabric clients.FabricClient) *controlplane.Service {
	return controlplane.NewService(ledger, fabric, &testSub2APIClient{balance: 1_000_000_000_000, charges: map[string]int64{}})
}

type failingResourceCreateFabricClient struct{ fakeFabricClient }

func (*failingResourceCreateFabricClient) CreateComputeAllocation(context.Context, clients.ComputeAllocationInput, string) (clients.ComputeAllocation, error) {
	return clients.ComputeAllocation{}, errors.New("compute create failed")
}

func (*failingResourceCreateFabricClient) CreateStorageVolume(context.Context, clients.StorageVolumeInput, string) (clients.StorageVolume, error) {
	return clients.StorageVolume{}, errors.New("storage create failed")
}

func (fakeLedgerClient) RecordReceipt(_ context.Context, input clients.ReceiptInput, _ string) (clients.Receipt, error) {
	return clients.Receipt{ReceiptInput: input, ReceiptID: "receipt-from-ledger", ContinuationID: "continuation-from-ledger"}, nil
}

func (fakeLedgerClient) Receipt(_ context.Context, receiptID string) (clients.Receipt, error) {
	return clients.Receipt{ReceiptInput: clients.ReceiptInput{Status: "completed", Execution: map[string]any{"jobStatus": "succeeded", "attempt": float64(1)}}, ReceiptID: receiptID, ContinuationID: "continuation-from-ledger"}, nil
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

func (fakeLedgerClient) RecordReconciliation(_ context.Context, input clients.ReconciliationInput, _ string) (clients.ReconciliationResult, error) {
	return clients.ReconciliationResult{ID: stringField(input.Report, "id", "reconciliation-from-ledger"), Status: "ok", Report: input.Report, BlockNewWorkspaces: false, Reason: "operator_reconciliation"}, nil
}

type fakeBlockingReconciliationLedgerClient struct {
	fakeLedgerClient
}

func (fakeBlockingReconciliationLedgerClient) RecordReconciliation(_ context.Context, input clients.ReconciliationInput, _ string) (clients.ReconciliationResult, error) {
	return clients.ReconciliationResult{ID: stringField(input.Report, "id", "reconciliation-from-ledger"), Status: "mismatch", Report: input.Report, BlockNewWorkspaces: true, Reason: "tencent_bill_reconciliation_failed"}, nil
}

type flakyWorkspaceReceiptLedger struct {
	fakeLedgerClient
	receiptCalls int
}

func (l *flakyWorkspaceReceiptLedger) RecordReceipt(ctx context.Context, input clients.ReceiptInput, key string) (clients.Receipt, error) {
	if input.Type != "workspace.created" {
		return l.fakeLedgerClient.RecordReceipt(ctx, input, key)
	}
	l.receiptCalls++
	if l.receiptCalls == 1 {
		return clients.Receipt{}, errors.New("ledger unavailable")
	}
	return l.fakeLedgerClient.RecordReceipt(ctx, input, key)
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

type unavailableProCatalogFabricClient struct{ fakeFabricClient }

func (unavailableProCatalogFabricClient) Catalog(_ context.Context) (clients.FabricCatalog, error) {
	return clients.FabricCatalog{WorkspacePackages: []clients.FabricWorkspacePackage{
		{ID: "basic", ComputeProfileID: "pool-basic", Available: true},
		{ID: "pro", ComputeProfileID: "pool-pro", Available: false},
	}}, nil
}

type fakeFabricClient struct {
	calls                *[]string
	runtime              clients.WorkspaceRuntime
	runtimeErr           error
	runtimeStatus        clients.WorkspaceRuntime
	gatewaySecret        clients.GatewaySecretWriteResult
	gatewaySecretErr     error
	storageDestroyStatus string
	gatewaySecretInputs  []clients.GatewaySecretWriteInput
	runtimeInputs        []clients.WorkspaceRuntimeInput
}

type countingWorkspaceFabricClient struct {
	fakeFabricClient
	mu             sync.Mutex
	gatewayWrites  int
	runtimeCreates int
}

func (f *countingWorkspaceFabricClient) WriteGatewaySecret(ctx context.Context, input clients.GatewaySecretWriteInput, key string) (clients.GatewaySecretWriteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gatewayWrites++
	return f.fakeFabricClient.WriteGatewaySecret(ctx, input, key)
}

func (f *countingWorkspaceFabricClient) CreateWorkspaceRuntime(ctx context.Context, input clients.WorkspaceRuntimeInput, key string) (clients.WorkspaceRuntime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runtimeCreates++
	return f.fakeFabricClient.CreateWorkspaceRuntime(ctx, input, key)
}

func (f *countingWorkspaceFabricClient) CreateStorageAttachment(_ context.Context, input clients.StorageAttachmentInput, _ string) (clients.StorageAttachment, error) {
	return clients.StorageAttachment{
		ID: "attachment-" + stableID(input.ComputeID, input.VolumeID)[:12], WorkspaceID: input.WorkspaceID,
		ComputeID: input.ComputeID, VolumeID: input.VolumeID, Status: "attached", Provider: "tencent-tke",
		ProviderAttachmentID: "deployment/runtime:pvc/storage:/data", ProviderRequestID: "attachment-request-from-fabric", MountPath: "/data",
	}, nil
}

type provisioningComputeFabricClient struct{ fakeFabricClient }

type pendingComputeFabricClient struct {
	provisioningComputeFabricClient
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

func (f *fakeFabricClient) MonthlyPreflight(_ context.Context, input clients.MonthlyPreflightInput) (clients.MonthlyPreflight, error) {
	f.record("fabric.monthly.preflight")
	requestIDs := map[string]string{"quota": "quota-request", "price": "price-request"}
	if input.ResourceType == "compute" {
		requestIDs = map[string]string{"nodePool": "node-pool-request", "subnets": "subnets-request", "availability": "availability-request"}
	}
	return clients.MonthlyPreflight{
		ResourceType: input.ResourceType, PackageID: input.PackageID, SizeGB: input.SizeGB, Zone: input.Zone,
		Available: true, ChargeType: "PREPAID", PeriodMonths: 1, RenewFlag: "NOTIFY_AND_MANUAL_RENEW",
		ProviderPriceCNY: 12.34, ProviderRequestIDs: requestIDs,
	}, nil
}

func (f *fakeFabricClient) CreateComputeAllocation(_ context.Context, input clients.ComputeAllocationInput, _ string) (clients.ComputeAllocation, error) {
	f.record("fabric.compute")
	instanceType := "S5.MEDIUM4"
	if input.PackageID == "pro" {
		instanceType = "SA5.2XLARGE16"
	}
	return clients.ComputeAllocation{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID, Status: "running", Provider: "tencent-tke", ProviderResourceID: "node/node-from-fabric", ProviderRequestID: "compute-request-from-fabric", InstanceID: "ins-from-fabric", NodeName: "node-from-fabric", ServiceName: "opl-compute-from-fabric", InstanceType: instanceType, Zone: "ap-shanghai-2", ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z", ProviderData: map[string]string{"zone": "ap-shanghai-2", "instanceType": instanceType}}, nil
}

func (f *provisioningComputeFabricClient) CreateComputeAllocation(_ context.Context, input clients.ComputeAllocationInput, _ string) (clients.ComputeAllocation, error) {
	f.record("fabric.compute")
	return clients.ComputeAllocation{ID: input.ID, AccountID: input.AccountID, PackageID: input.PackageID, Status: "provisioning", Provider: "tencent-tke"}, nil
}

func (f *provisioningComputeFabricClient) SyncComputeAllocation(_ context.Context, id string) (clients.ComputeAllocation, error) {
	f.record("fabric.compute-sync")
	return clients.ComputeAllocation{ID: id, Status: "running", Provider: "tencent-tke", MachineName: "machine-alpha", InstanceID: "ins-alpha", NodeName: "node-alpha"}, nil
}

func (f *pendingComputeFabricClient) SyncComputeAllocation(_ context.Context, id string) (clients.ComputeAllocation, error) {
	f.record("fabric.compute-sync")
	return clients.ComputeAllocation{ID: id, Status: "provisioning", Provider: "tencent-tke"}, nil
}

func (f *fakeFabricClient) GetComputeAllocation(_ context.Context, id string) (clients.ComputeAllocation, error) {
	f.record("fabric.compute-get")
	return clients.ComputeAllocation{ID: id, Status: "running", Provider: "tencent-tke", ProviderResourceID: "node/node-from-fabric", ProviderRequestID: "compute-request-from-fabric", InstanceID: "ins-from-fabric", NodeName: "node-from-fabric", ServiceName: "opl-compute-from-fabric"}, nil
}

func (f *fakeFabricClient) SyncComputeAllocation(_ context.Context, id string) (clients.ComputeAllocation, error) {
	f.record("fabric.compute-sync")
	return clients.ComputeAllocation{ID: id, Status: "external_deleted", Provider: "tencent-tke", ProviderRequestID: "compute-sync-from-fabric"}, nil
}

func (f *fakeFabricClient) DestroyComputeAllocation(_ context.Context, id string, _ string) (clients.ComputeAllocation, error) {
	f.record("fabric.compute-destroy")
	return clients.ComputeAllocation{ID: id, Status: "destroyed", Provider: "tencent-tke", ProviderRequestID: "compute-destroy-from-fabric"}, nil
}

func (f *fakeFabricClient) CreateStorageVolume(_ context.Context, input clients.StorageVolumeInput, _ string) (clients.StorageVolume, error) {
	f.record("fabric.storage")
	return clients.StorageVolume{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "available", Provider: "tencent-tke", ProviderResourceID: "disk-volume-from-fabric", ProviderRequestID: "storage-request-from-fabric", SizeGB: input.SizeGB, StorageClass: "cbs", CBSStatus: "UNATTACHED", DiskType: "CLOUD_PREMIUM", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z", Zone: input.Zone, ProviderData: map[string]string{"chargeType": "PREPAID"}}, nil
}

func (f *fakeFabricClient) SyncStorageVolume(_ context.Context, id string) (clients.StorageVolume, error) {
	f.record("fabric.storage-sync")
	return clients.StorageVolume{ID: id, Status: "external_deleted", Provider: "tencent-tke", ProviderRequestID: "storage-sync-from-fabric"}, nil
}

func (f *fakeFabricClient) DestroyStorageVolume(_ context.Context, id string, _ string) (clients.StorageVolume, error) {
	f.record("fabric.storage-destroy")
	return clients.StorageVolume{ID: id, Status: firstNonEmpty(f.storageDestroyStatus, "destroyed"), Provider: "tencent-tke", ProviderRequestID: "storage-destroy-from-fabric"}, nil
}

func (f *fakeFabricClient) CreateStorageAttachment(_ context.Context, input clients.StorageAttachmentInput, _ string) (clients.StorageAttachment, error) {
	f.record("fabric.attachment")
	return clients.StorageAttachment{ID: "attachment-from-fabric", WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Status: "attached", Provider: "tencent-tke", ProviderAttachmentID: "deployment/opl-compute-from-fabric:pvc/volume-from-fabric-data:/data", ProviderRequestID: "attachment-request-from-fabric", MountPath: "/data"}, nil
}

func (f *fakeFabricClient) DetachStorageAttachment(_ context.Context, id string, _ string) (clients.StorageAttachment, error) {
	f.record("fabric.attachment-detach")
	return clients.StorageAttachment{ID: id, Status: "detached", ProviderRequestID: "attachment-detach-from-fabric"}, nil
}

func (f *fakeFabricClient) WriteGatewaySecret(_ context.Context, input clients.GatewaySecretWriteInput, _ string) (clients.GatewaySecretWriteResult, error) {
	f.record("fabric.gateway-secret")
	f.gatewaySecretInputs = append(f.gatewaySecretInputs, input)
	if f.gatewaySecretErr != nil {
		return clients.GatewaySecretWriteResult{}, f.gatewaySecretErr
	}
	if f.gatewaySecret.SecretRef != "" {
		return f.gatewaySecret, nil
	}
	return clients.GatewaySecretWriteResult{SecretRef: "opl-gateway-acct-alpha", Version: "v1", Fingerprint: "sha256:redacted"}, nil
}

func (f *fakeFabricClient) CreateWorkspaceRuntime(_ context.Context, input clients.WorkspaceRuntimeInput, _ string) (clients.WorkspaceRuntime, error) {
	f.record("fabric.runtime")
	f.runtimeInputs = append(f.runtimeInputs, input)
	if f.runtimeErr != nil {
		return clients.WorkspaceRuntime{}, f.runtimeErr
	}
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
	if f.runtimeStatus.ID != "" {
		return f.runtimeStatus, nil
	}
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
	server := newExecutionTestServer(t, newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
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
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
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
			server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
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
			if tc.organization != nil {
				mustStore(t, store.SaveOrganization(context.Background(), tc.organization))
				mustStore(t, store.SaveMembership(context.Background(), map[string]any{"id": "mem-admin-alpha", "organizationId": "org-alpha", "userId": "usr-admin", "accountId": "acct-alpha", "role": "admin", "status": "active"}))
			}
			if tc.workspace != nil {
				mustStore(t, store.SaveWorkspace(context.Background(), tc.workspace))
			}
			server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
			if err != nil {
				t.Fatalf("create server: %v", err)
			}
			session := tenantAdminSessionForTest(t, server)
			if tc.organization == nil {
				users, _ := store.ListUsers(context.Background(), true)
				var sessionUser map[string]any
				for _, user := range users {
					if user["id"] != "usr-admin" && user["role"] == "admin" {
						sessionUser = user
					}
				}
				mustStore(t, store.SaveOrganization(context.Background(), map[string]any{"id": "org-session", "billingAccountId": "acct-alpha", "status": "active"}))
				mustStore(t, store.SaveMembership(context.Background(), map[string]any{"id": "mem-session", "organizationId": "org-session", "userId": stringValue(sessionUser["id"]), "accountId": "acct-alpha", "role": "admin", "status": "active"}))
				delete(store.organizations, "org-alpha")
				delete(store.memberships, "mem-admin-alpha")
			}

			rec := requestWithSession(t, server, session, http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`)
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
	return clients.Receipt{ReceiptInput: input, ReceiptID: receiptID, ContinuationID: continuationID}, nil
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
	server := newExecutionTestServer(t, newTestService(&executionCompletionLedgerClient{}, &completedExecutionFabricClient{}))
	admin := operatorSessionForTest(t, server)
	memberUser := createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"member@execution.example","accountId":"acct-alpha","role":"member","password":"CorrectHorseBatteryStaple!","sub2apiUserId":41}`)
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

	createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"outside-continuation@example.com","accountId":"acct-beta","role":"member","password":"CorrectHorseBatteryStaple!","sub2apiUserId":42}`)
	outsider := loginForTest(t, server, "outside-continuation@example.com", "CorrectHorseBatteryStaple!")
	forbidden := requestWithSession(t, server, outsider, http.MethodGet, "/api/execution-requests/"+requestID+"/continuation", "")
	if forbidden.Code != http.StatusForbidden || !strings.Contains(forbidden.Body.String(), "organization_membership_required") {
		t.Fatalf("outsider continuation status = %d, want organization_membership_required: %s", forbidden.Code, forbidden.Body.String())
	}
}

func TestProjectIdentityRequiresIdempotencyKey(t *testing.T) {
	server := newExecutionTestServer(t, newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
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
	server := newExecutionTestServer(t, newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
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
	server := newExecutionTestServer(t, newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := operatorSessionForTest(t, server)
	piUser := createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"pi@execution.example","accountId":"acct-alpha","role":"member","password":"CorrectHorseBatteryStaple!","sub2apiUserId":41}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/organizations/members", `{"organizationId":"org-alpha","userId":"`+stringValue(piUser["id"])+`","accountId":"acct-alpha","role":"member"}`)
	pi := loginForTest(t, server, "pi@execution.example", "CorrectHorseBatteryStaple!")
	rec := requestWithSession(t, server, pi, http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("member status = %d body=%s, want created", rec.Code, rec.Body.String())
	}

	createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"outsider@execution.example","accountId":"acct-beta","role":"member","password":"CorrectHorseBatteryStaple!","sub2apiUserId":42}`)
	outsider := loginForTest(t, server, "outsider@execution.example", "CorrectHorseBatteryStaple!")
	forbidden := requestWithSession(t, server, outsider, http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`)
	if forbidden.Code != http.StatusForbidden || !strings.Contains(forbidden.Body.String(), "organization_membership_required") {
		t.Fatalf("outsider status = %d body=%s, want organization_membership_required", forbidden.Code, forbidden.Body.String())
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
	return strings.HasPrefix(path, "/api/users") || strings.HasPrefix(path, "/api/organizations") || strings.HasPrefix(path, "/api/operator") || strings.HasPrefix(path, "/api/management") || strings.HasPrefix(path, "/api/billing/reconciliation")
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

func createWorkspaceAttachmentForTest(t *testing.T, server http.Handler, session *httptest.ResponseRecorder, key string) map[string]any {
	t.Helper()
	compute := createResourceWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/compute-allocations", `{"packageId":"basic"}`, key+"-compute")
	storage := createResourceWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/storage-volumes", `{"packageId":"basic","sizeGb":10,"computeAllocationId":"`+stringValue(compute["id"])+`"}`, key+"-storage")
	return createResourceWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/storage-attachments", `{"computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`"}`, key+"-attachment")
}

func createResourceWithMutationKeyForTest(t *testing.T, server http.Handler, session *httptest.ResponseRecorder, method, path, body, key string) map[string]any {
	t.Helper()
	rec := requestWithMutationKeyForTest(t, server, session, method, path, body, key)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("%s %s status = %d: %s", method, path, rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode %s %s: %v", method, path, err)
	}
	return payload
}

func requestWithMutationKeyForTest(t *testing.T, server http.Handler, session *httptest.ResponseRecorder, method, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	addAuth(req, session)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
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
	return tenantSessionForTest(t, server, "admin")
}

func tenantOwnerSessionForTest(t *testing.T, server http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	return tenantSessionForTest(t, server, "owner")
}

func tenantSessionForTest(t *testing.T, server http.Handler, role string) *httptest.ResponseRecorder {
	t.Helper()
	global := reservedOperatorSessionForTest(t, server)
	email := "tenant-" + role + "-" + newResourceID("test") + "@example.com"
	user := createResourceWithSession(t, server, global, http.MethodPost, "/api/users", `{"email":"`+email+`","accountId":"acct-alpha","role":"`+role+`","password":"CorrectHorseBatteryStaple!","sub2apiUserId":41}`)
	membership := requestWithSession(t, server, global, http.MethodPost, "/api/organizations/members", `{"organizationId":"org-alpha","userId":"`+stringValue(user["id"])+`","accountId":"acct-alpha","role":"`+role+`"}`)
	if membership.Code < 200 || membership.Code >= 300 {
		organization := createResourceWithSession(t, server, global, http.MethodPost, "/api/organizations", `{"name":"Test tenant","billingAccountId":"acct-alpha"}`)
		createResourceWithSession(t, server, global, http.MethodPost, "/api/organizations/members", `{"organizationId":"`+stringValue(organization["id"])+`","userId":"`+stringValue(user["id"])+`","accountId":"acct-alpha","role":"`+role+`"}`)
	}
	return loginForTest(t, server, email, "CorrectHorseBatteryStaple!")
}

func reservedOperatorSessionForTest(t *testing.T, server http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	handler, ok := server.(*controlPlaneHTTPHandler)
	if !ok {
		t.Fatalf("server type = %T, want *controlPlaneHTTPHandler", server)
	}
	users, err := handler.app.tables.ListUsers(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	var admin map[string]any
	for _, user := range users {
		if strings.EqualFold(stringValue(user["email"]), "admin@medopl.cn") {
			admin = user
			break
		}
	}
	if admin == nil {
		t.Fatal("admin@medopl.cn test user missing")
	}
	payload, sessionID, err := handler.app.createSession(admin)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	http.SetCookie(rec, sessionCookie(sessionID, 12*60*60))
	rec.Header().Set("x-opl-csrf-token", stringValue(payload["csrfToken"]))
	writeJSON(rec, http.StatusOK, payload)
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

func sessionUserIDForTest(t *testing.T, server http.Handler, loginRec *httptest.ResponseRecorder) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	addSessionCookies(req, loginRec)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("session lookup status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return stringValue(mapField(payload, "user")["id"])
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
	}))

	mustStore(t, app.saveComputeFact(map[string]any{
		"id": "compute-alpha", "accountId": "acct-alpha", "status": "destroyed", "billingStatus": "stopped",
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
		"id": "storage-alpha", "accountId": "acct-alpha", "status": "destroyed", "billingStatus": "stopped",
	}))
	workspace = storedWorkspace(t, app, "ws-alpha")
	if workspace["state"] != "data_deleted" || workspace["status"] != "unrecoverable" {
		t.Fatalf("storage destroy did not mark workspace unrecoverable: %#v", workspace)
	}
}

func TestDestroyComputeCleansLinkedWorkspaceRuntimeFirst(t *testing.T) {
	calls := []string{}
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{calls: &calls}))
	session := tenantOwnerSessionForTest(t, server)
	compute := createResourceWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic","name":"compute-alpha"}`)
	storage := createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","packageId":"basic","sizeGb":10,"name":"storage-alpha","computeAllocationId":"`+stringValue(compute["id"])+`"}`)
	attachment := createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-attachments", `{"accountId":"acct-alpha","computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`"}`)
	workspace := createResourceWithSession(t, server, session, http.MethodPost, "/api/workspaces", `{"accountId":"acct-alpha","attachmentId":"`+stringValue(attachment["id"])+`","workspaceName":"Workspace Alpha"}`)

	createResourceWithSession(t, server, session, http.MethodPost, "/api/compute-allocations/"+stringValue(compute["id"])+"/destroy", `{"confirm":true}`)
	runtimeDestroy := slices.Index(calls, "fabric.runtime-destroy")
	computeDestroy := slices.Index(calls, "fabric.compute-destroy")
	if runtimeDestroy < 0 || computeDestroy < 0 || runtimeDestroy > computeDestroy {
		t.Fatalf("destroy order = %#v, want runtime before compute", calls)
	}

	rec := requestWithSession(t, server, session, http.MethodGet, "/api/workspaces", "")
	var workspaces []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&workspaces); err != nil {
		t.Fatal(err)
	}
	row := recordByID(workspaces, stringValue(workspace["id"]))
	if row["state"] != "suspended" || stringValue(row["currentComputeAllocationId"]) != "" ||
		stringValue(row["computeAllocationId"]) != stringValue(compute["id"]) || row["autoRenew"] != false ||
		stringValue(row["runtimeId"]) != "" || stringValue(row["runtimeServiceName"]) != "" || stringValue(nested(row, "runtime", "serviceName")) != "" {
		t.Fatalf("destroyed runtime projection = %#v", row)
	}
}

func TestDetachStorageAttachmentPreservesOwnershipFacts(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := tenantAdminSessionForTest(t, server)

	compute := createResourceWithSession(t, server, admin, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic","name":"compute-alpha"}`)
	storage := createResourceWithSession(t, server, admin, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","packageId":"basic","sizeGb":10,"name":"storage-alpha","computeAllocationId":"`+stringValue(compute["id"])+`"}`)
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

func TestArchiveTerminalResourcesRemovesCurrentState(t *testing.T) {
	app := newControlPlaneApp()
	mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{"id": "compute-dead", "status": "destroyed"}))
	mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{"id": "storage-dead", "status": "destroyed"}))
	mustStore(t, app.tables.SaveAttachment(context.Background(), map[string]any{"id": "attach-dead", "status": "detached"}))
	mustStore(t, app.tables.SaveWorkspace(context.Background(), map[string]any{"id": "ws-dead", "state": "unrecoverable"}))

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
}

func TestArchiveStateEndpointReturnsBackendArchiveAndRetentionPolicy(t *testing.T) {
	path := t.TempDir() + "/control-plane-state.sqlite"
	store := NewTestEntStateStore(t, path)
	if err := store.SaveCompute(context.Background(), map[string]any{"id": "compute-dead", "accountId": "acct-alpha", "status": "destroyed"}); err != nil {
		t.Fatalf("seed terminal compute: %v", err)
	}
	service := newTestService(fakeLedgerClient{}, &fakeFabricClient{})
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

func TestOperatorSummaryIncludesWorkspaceResourceAnomalies(t *testing.T) {
	app := newControlPlaneApp()
	mustStore(t, app.tables.SaveWorkspace(context.Background(), map[string]any{
		"id":             "ws-missing-storage",
		"ownerAccountId": "acct-alpha",
		"storageId":      "missing-storage",
	}))

	operator := app.operatorSummary()
	anomalies := operator["resourceAnomalies"].([]any)
	if len(anomalies) != 1 || anomalies[0].(map[string]any)["status"] != "missing_storage" {
		t.Fatalf("operator resource anomalies should include Workspace fact issues: %#v", anomalies)
	}
}

func TestConsoleStateHydratesResourceListsFromFabricOperations(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fabricClientWithResourceOperations{}))

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

func TestRememberRuntimeOperationPreservesComputeBillingFacts(t *testing.T) {
	app := newControlPlaneAppEmpty()
	mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{
		"id": "compute-alpha", "accountId": "acct-alpha", "ownerUserId": "user-alpha", "name": "Alpha compute", "status": "provisioning",
		"billingStatus": "active", "pricingVersion": "pricing-v1", "chargeUsdMicros": int64(50_000_000),
		"periodStart": "2026-07-14T00:00:00Z", "paidThrough": "2026-08-14T00:00:00Z", "lastReceiptId": "receipt-compute",
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
	if compute["billingStatus"] != "active" || int64(numberField(compute, "chargeUsdMicros", 0)) != 50_000_000 || compute["paidThrough"] != "2026-08-14T00:00:00Z" || compute["lastReceiptId"] != "receipt-compute" || compute["pricingVersion"] != "pricing-v1" || compute["ownerUserId"] != "user-alpha" || compute["name"] != "Alpha compute" {
		t.Fatalf("Fabric operation erased Control Plane facts: %#v", compute)
	}
	if compute["nodeName"] != "node-from-fabric" || compute["status"] != "running" {
		t.Fatalf("Fabric provider facts were not applied: %#v", compute)
	}
}

func TestRememberRuntimeOperationPreservesStorageBillingFacts(t *testing.T) {
	app := newControlPlaneAppEmpty()
	mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{
		"id": "storage-alpha", "accountId": "acct-alpha", "ownerUserId": "user-alpha", "name": "Alpha storage", "status": "provisioning",
		"billingStatus": "active", "pricingVersion": "pricing-v1", "chargeUsdMicros": int64(2_571_429),
		"periodStart": "2026-07-14T00:00:00Z", "paidThrough": "2026-08-14T00:00:00Z", "lastReceiptId": "receipt-storage",
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
	if storage["billingStatus"] != "active" || int64(numberField(storage, "chargeUsdMicros", 0)) != 2_571_429 || storage["paidThrough"] != "2026-08-14T00:00:00Z" || storage["lastReceiptId"] != "receipt-storage" || storage["pricingVersion"] != "pricing-v1" || storage["ownerUserId"] != "user-alpha" || storage["name"] != "Alpha storage" {
		t.Fatalf("Fabric operation erased Control Plane facts: %#v", storage)
	}
	if storage["providerResourceId"] != "pvc/storage-alpha-data" || storage["status"] != "available" {
		t.Fatalf("Fabric provider facts were not applied: %#v", storage)
	}
}

func TestConsoleStateSkipsUnscopedHistoricFabricResourceProjection(t *testing.T) {
	setTestOperatorAccount(t, "acct-alpha")
	service := newTestService(fakeLedgerClient{}, &fabricClientWithUnscopedHistoricOperation{})
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

func TestReconciliationGuardBlocksNewResourceProvisioning(t *testing.T) {
	var calls []string
	server := NewServer(newTestService(fakeBlockingReconciliationLedgerClient{}, &fakeFabricClient{calls: &calls}))
	session := tenantAdminSessionForTest(t, server)

	createResourceWithSession(t, server, session, http.MethodPost, "/api/billing/reconciliation", `{"confirm":true}`)

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

func workspaceGatewayTestRow(row map[string]any) map[string]any {
	billing := canonicalWorkspaceRenewalRow(false)
	billing["id"], billing["accountId"], billing["ownerAccountId"], billing["ownerUserId"] = "ws-alpha", "acct-alpha", "acct-alpha", "usr-alpha"
	billing["currentComputeAllocationId"], billing["computeAllocationId"], billing["storageId"] = "compute-alpha", "compute-alpha", "storage-alpha"
	billing["periodStart"], billing["paidThrough"], billing["nextRenewalAt"], billing["billingAnchorDay"] = "2098-12-01T00:00:00Z", "2099-01-01T00:00:00Z", "2098-12-31T00:00:00Z", int64(1)
	for key, value := range row {
		billing[key] = value
	}
	return billing
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
	mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{"id": "compute-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "status": "running", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z"}))
	mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{"id": "storage-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "status": "available", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z"}))
	mustStore(t, app.tables.SaveAttachment(context.Background(), map[string]any{"id": "attachment-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "computeAllocationId": "compute-alpha", "storageId": "storage-alpha", "status": "attached"}))
	mustStore(t, app.tables.SaveWorkspace(context.Background(), workspaceGatewayTestRow(map[string]any{"id": "ws-alpha",
		"accountId":                  "acct-alpha",
		"state":                      "running",
		"currentComputeAllocationId": "compute-alpha",
		"storageId":                  "storage-alpha",
		"attachmentId":               "attachment-alpha",
		"currentAttachmentId":        "attachment-alpha",
		"runtime":                    map[string]any{"serviceName": strings.TrimPrefix(backend.URL, "http://")},
	})))
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

func TestWorkspaceGatewaySetsRoutingCookieForRootRuntimeApi(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_DOMAIN", "workspace.medopl.cn")
	var gotPaths []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		writeJSON(w, http.StatusOK, map[string]string{"proxied": r.URL.Path})
	}))
	defer backend.Close()
	app := newControlPlaneApp()
	mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{"id": "compute-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "status": "running", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z"}))
	mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{"id": "storage-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "status": "available", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z"}))
	mustStore(t, app.tables.SaveAttachment(context.Background(), map[string]any{"id": "attachment-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "computeAllocationId": "compute-alpha", "storageId": "storage-alpha", "status": "attached"}))
	mustStore(t, app.tables.SaveWorkspace(context.Background(), workspaceGatewayTestRow(map[string]any{"id": "ws-alpha",
		"accountId":                  "acct-alpha",
		"state":                      "running",
		"currentComputeAllocationId": "compute-alpha",
		"storageId":                  "storage-alpha",
		"attachmentId":               "attachment-alpha",
		"currentAttachmentId":        "attachment-alpha",
		"runtime":                    map[string]any{"serviceName": strings.TrimPrefix(backend.URL, "http://")},
	})))
	entryReq := httptest.NewRequest(http.MethodGet, "https://workspace.medopl.cn/w/ws-alpha/", nil)
	entryRec := httptest.NewRecorder()

	app.proxyWorkspace(entryRec, entryReq)

	if entryRec.Code != http.StatusOK {
		t.Fatalf("entry status = %d, want %d: %s", entryRec.Code, http.StatusOK, entryRec.Body.String())
	}
	cookies := entryRec.Result().Cookies()
	if !slices.ContainsFunc(cookies, func(cookie *http.Cookie) bool {
		return cookie.Name == "opl_ws_active" && cookie.Value == "ws-alpha"
	}) {
		t.Fatalf("entry response must set Workspace routing cookie, got %#v", cookies)
	}
	if slices.ContainsFunc(cookies, func(cookie *http.Cookie) bool {
		return strings.HasPrefix(cookie.Name, "opl_ws_") && cookie.Name != "opl_ws_active"
	}) {
		t.Fatalf("entry response must not set fake URL token cookies: %#v", cookies)
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
		t.Fatalf("proxied paths = %#v, want clean entry and root API paths", gotPaths)
	}
}

func TestWorkspaceStateRequiresActiveUnexpiredStorageEntitlement(t *testing.T) {
	for _, tc := range []struct {
		name          string
		storageStatus string
		billingStatus string
		paidThrough   string
		openable      bool
	}{
		{name: "active", storageStatus: "available", billingStatus: "active", paidThrough: "2099-01-01T00:00:00Z", openable: true},
		{name: "past due", storageStatus: "available", billingStatus: "past_due", paidThrough: "2099-01-01T00:00:00Z"},
		{name: "expired", storageStatus: "available", billingStatus: "active", paidThrough: "2000-01-01T00:00:00Z"},
		{name: "invalid deadline", storageStatus: "available", billingStatus: "active", paidThrough: "not-a-time"},
		{name: "retained", storageStatus: "retained", billingStatus: "active", paidThrough: "2099-01-01T00:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newControlPlaneApp()
			mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{
				"id": "compute-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "status": "running", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z",
			}))
			mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{
				"id": "storage-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "status": tc.storageStatus, "billingStatus": tc.billingStatus, "paidThrough": tc.paidThrough,
			}))
			mustStore(t, app.tables.SaveAttachment(context.Background(), map[string]any{
				"id": "attachment-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "computeAllocationId": "compute-alpha", "storageId": "storage-alpha", "status": "attached",
			}))
			mustStore(t, app.tables.SaveWorkspace(context.Background(), workspaceGatewayTestRow(map[string]any{
				"id": "ws-alpha", "accountId": "acct-alpha", "state": "running", "currentComputeAllocationId": "compute-alpha",
				"storageId": "storage-alpha", "attachmentId": "attachment-alpha", "currentAttachmentId": "attachment-alpha",
				"runtime": map[string]any{"serviceName": "runtime-alpha", "ready": true},
			})))
			workspace := app.workspaceStateRowsLocked("")[0].(map[string]any)
			if workspace["openable"] != tc.openable {
				t.Fatalf("workspace openable=%#v, want %v: %#v", workspace["openable"], tc.openable, workspace)
			}
			if !tc.openable && workspace["accessState"] != "disabled" {
				t.Fatalf("closed workspace accessState=%#v", workspace["accessState"])
			}
		})
	}
}

func TestWorkspaceGatewayBlocksExpiredStorageEntitlementBeforeProxy(t *testing.T) {
	proxied := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxied = true
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	app := newControlPlaneApp()
	mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{"id": "storage-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "status": "available", "billingStatus": "active", "paidThrough": "2000-01-01T00:00:00Z"}))
	mustStore(t, app.tables.SaveWorkspace(context.Background(), workspaceGatewayTestRow(map[string]any{
		"id": "ws-alpha", "accountId": "acct-alpha", "state": "running", "storageId": "storage-alpha", "runtime": map[string]any{"serviceName": strings.TrimPrefix(backend.URL, "http://"), "ready": true},
	})))
	req := httptest.NewRequest(http.MethodGet, "/w/ws-alpha/", nil)
	rec := httptest.NewRecorder()

	app.proxyWorkspace(rec, req)

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "workspace_storage_entitlement_inactive") || proxied {
		t.Fatalf("expired storage proxy status=%d body=%s proxied=%v", rec.Code, rec.Body.String(), proxied)
	}
}

func TestWorkspaceGatewayBlocksInactiveComputeEntitlementBeforeProxy(t *testing.T) {
	for _, tc := range []struct {
		name          string
		billingStatus string
		paidThrough   string
	}{
		{name: "past due", billingStatus: "past_due", paidThrough: "2099-01-01T00:00:00Z"},
		{name: "expired", billingStatus: "active", paidThrough: "2000-01-01T00:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxied := false
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				proxied = true
				w.WriteHeader(http.StatusOK)
			}))
			defer backend.Close()
			app := newControlPlaneApp()
			mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{
				"id": "compute-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "status": "running",
				"billingStatus": tc.billingStatus, "paidThrough": tc.paidThrough,
			}))
			mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{
				"id": "storage-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha", "status": "available",
				"billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z",
			}))
			mustStore(t, app.tables.SaveWorkspace(context.Background(), workspaceGatewayTestRow(map[string]any{
				"id": "ws-alpha", "accountId": "acct-alpha", "state": "running", "currentComputeAllocationId": "compute-alpha",
				"storageId": "storage-alpha", "runtime": map[string]any{"serviceName": strings.TrimPrefix(backend.URL, "http://"), "ready": true},
			})))
			req := httptest.NewRequest(http.MethodGet, "/w/ws-alpha/", nil)
			rec := httptest.NewRecorder()

			app.proxyWorkspace(rec, req)

			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "workspace_compute_entitlement_inactive") || proxied {
				t.Fatalf("inactive compute proxy status=%d body=%s proxied=%v", rec.Code, rec.Body.String(), proxied)
			}
		})
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
		{name: "unready", row: map[string]any{"state": "unready", "runtime": map[string]any{"serviceName": "runtime-alpha", "status": "running"}}, want: http.StatusConflict},
		{name: "running but not ready", row: map[string]any{"state": "running", "runtime": map[string]any{"serviceName": "runtime-alpha", "ready": false}}, want: http.StatusConflict},
		{name: "data deleted", row: map[string]any{"state": "data_deleted", "runtime": map[string]any{"serviceName": "runtime-alpha"}}, want: http.StatusGone},
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

func TestWorkspaceGatewayReturnsNotFoundWithoutRoutingCookieForUnknownWorkspace(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_DOMAIN", "workspace.medopl.cn")
	app := newControlPlaneApp()
	req := httptest.NewRequest(http.MethodGet, "https://workspace.medopl.cn/w/ws-unknown/", nil)
	rec := httptest.NewRecorder()

	app.proxyWorkspace(rec, req)

	if rec.Code != http.StatusNotFound || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("unknown workspace status=%d cookies=%#v", rec.Code, rec.Result().Cookies())
	}
}

func TestOverviewHTTP(t *testing.T) {
	server := NewServer(newTestService(nil, nil))
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

func TestOperatorLoginRouteDoesNotCreateAUserSession(t *testing.T) {
	server := NewServer(newTestService(nil, nil))
	req := httptest.NewRequest(http.MethodPost, "/api/auth/operator-login", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("operator login status = %d cookie=%q", rec.Code, rec.Header().Get("Set-Cookie"))
	}
}

func TestProtectedWriteRejectsOversizedJSONBody(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
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
	server := NewServer(newTestService(fakeLedgerClient{}, &failingFabricClient{}))
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
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))

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
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))

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

	ownerUser := createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"owner@lab.example","accountId":"acct-alpha","role":"member","password":"CorrectHorseBatteryStaple!","sub2apiUserId":41}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/organizations/members", `{"organizationId":"org-alpha","userId":"`+stringValue(ownerUser["id"])+`","accountId":"acct-alpha","role":"member"}`)
	owner := loginForTest(t, server, "owner@lab.example", "CorrectHorseBatteryStaple!")
	adminReq := httptest.NewRequest(http.MethodPost, "/api/billing/reconciliation", bytes.NewBufferString(`{"confirm":true,"report":{"id":"recon-nonadmin","status":"ok"}}`))
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

func TestPaidResourcePurchaseRequiresIdempotencyKeyBeforeSideEffects(t *testing.T) {
	calls := []string{}
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{calls: &calls}))
	session := tenantAdminSessionForTest(t, server)

	for _, tc := range []struct {
		path string
		body string
	}{
		{path: "/api/compute-allocations", body: `{"accountId":"acct-alpha","packageId":"basic"}`},
		{path: "/api/storage-volumes", body: `{"accountId":"acct-alpha","packageId":"basic","sizeGb":10}`},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(tc.body))
		req.Header.Set("Content-Type", "application/json")
		addAuth(req, session)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "missing Idempotency-Key") {
			t.Fatalf("%s status = %d body=%s, want missing Idempotency-Key", tc.path, rec.Code, rec.Body.String())
		}
	}
	if len(calls) != 0 {
		t.Fatalf("missing purchase key reached Fabric: %#v", calls)
	}
}

func TestResourceAutoRenewProductCommandUpdatesOnlyControlPlaneState(t *testing.T) {
	calls := []string{}
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{calls: &calls}))
	session := tenantAdminSessionForTest(t, server)
	compute := createResourceWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	resources := []map[string]any{
		compute,
		createResourceWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","packageId":"basic","sizeGb":10,"computeAllocationId":"`+stringValue(compute["id"])+`"}`),
	}
	before := len(calls)
	for _, resource := range resources {
		updated := createResourceWithSession(t, server, session, http.MethodPost, "/api/resources/"+stringValue(resource["id"])+"/auto-renew", `{"autoRenew":true}`)
		if updated["id"] != resource["id"] || updated["autoRenew"] != true {
			t.Fatalf("auto-renew response = %#v", updated)
		}
	}
	if len(calls) != before {
		t.Fatalf("auto-renew toggle touched Fabric: %#v", calls[before:])
	}

	invalid := requestWithSession(t, server, session, http.MethodPost, "/api/resources/"+stringValue(resources[0]["id"])+"/auto-renew", `{}`)
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "autoRenew_required") {
		t.Fatalf("invalid auto-renew status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}

func TestHighRiskMutationsRequireBackendConfirmation(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := operatorSessionForTest(t, server)
	tenant := tenantAdminSessionForTest(t, server)
	created := createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"member@lab.example","accountId":"acct-alpha","role":"member","password":"CorrectHorseBatteryStaple!","sub2apiUserId":41}`)

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/billing/reconciliation", `{"report":{"id":"recon-confirm","generatedAt":"2026-07-06T00:00:00Z"}}`},
		{http.MethodPost, "/api/users/delete", `{"userId":"` + stringValue(created["id"]) + `","reason":"left_lab"}`},
		{http.MethodPost, "/api/compute-allocations/compute-alpha/destroy", `{}`},
		{http.MethodPost, "/api/storage-volumes/destroy", `{"storageId":"storage-alpha"}`},
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
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := operatorSessionForTest(t, server)
	ownerUser := createResourceWithSession(t, server, admin, http.MethodPost, "/api/users", `{"email":"owner@lab.example","accountId":"acct-alpha","role":"admin","password":"CorrectHorseBatteryStaple!","sub2apiUserId":41}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/organizations/members", `{"organizationId":"org-alpha","userId":"`+stringValue(ownerUser["id"])+`","accountId":"acct-alpha","role":"admin"}`)

	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"email":"owner@lab.example","password":"CorrectHorseBatteryStaple!","sub2apiUserId":41}`))
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
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	createResource(t, server, http.MethodPost, "/api/users", `{"email":"owner@lab.example","accountId":"acct-alpha","role":"admin","password":"CorrectHorseBatteryStaple!","sub2apiUserId":41}`)

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
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
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
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	created := createResource(t, server, http.MethodPost, "/api/users", `{"email":"member@lab.example","accountId":"acct-alpha","role":"member","password":"CorrectHorseBatteryStaple!","sub2apiUserId":41}`)
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

func TestOwnerLifecycleDisablesRenewalAndRevokesSession(t *testing.T) {
	for index, action := range []string{"disable", "delete"} {
		t.Run(action, func(t *testing.T) {
			server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
			operator := operatorSessionForTest(t, server)
			accountID := "acct-owner-" + action
			owner := createResourceWithSession(t, server, operator, http.MethodPost, "/api/users", fmt.Sprintf(`{"email":"owner-%s@example.com","accountId":"%s","password":"CorrectHorseBatteryStaple!","sub2apiUserId":%d}`, action, accountID, 91+index))
			member := createResourceWithSession(t, server, operator, http.MethodPost, "/api/users", fmt.Sprintf(`{"email":"member-%s@example.com","accountId":"%s","role":"member","password":"CorrectHorseBatteryStaple!","sub2apiUserId":%d}`, action, accountID, 91+index))
			ownerSession := loginForTest(t, server, "owner-"+action+"@example.com", "CorrectHorseBatteryStaple!")
			app := server.(*controlPlaneHTTPHandler).app
			computeID, storageID := "compute-owner-"+action, "storage-owner-"+action
			mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{"id": computeID, "accountId": accountID, "autoRenew": true}))
			mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{"id": storageID, "accountId": accountID, "autoRenew": true}))
			mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{"id": "compute-other-" + action, "accountId": "acct-other", "autoRenew": true}))

			path := "/api/users/" + action
			memberBody := `{"userId":"` + stringValue(member["id"]) + `"}`
			ownerBody := `{"userId":"` + stringValue(owner["id"]) + `"}`
			if action == "delete" {
				memberBody = `{"userId":"` + stringValue(member["id"]) + `","confirm":true}`
				ownerBody = `{"userId":"` + stringValue(owner["id"]) + `","confirm":true}`
			}
			if response := requestWithSession(t, server, operator, http.MethodPost, path, memberBody); response.Code != http.StatusOK {
				t.Fatalf("member %s status=%d body=%s", action, response.Code, response.Body.String())
			}
			computes, _ := app.tables.ListComputes(context.Background(), accountID)
			if recordByID(computes, computeID)["autoRenew"] != true {
				t.Fatalf("member %s changed account renewal: %#v", action, computes)
			}
			if response := requestWithSession(t, server, operator, http.MethodPost, path, ownerBody); response.Code != http.StatusOK {
				t.Fatalf("owner %s status=%d body=%s", action, response.Code, response.Body.String())
			}
			computes, _ = app.tables.ListComputes(context.Background(), "")
			storages, _ := app.tables.ListStorages(context.Background(), accountID)
			if recordByID(computes, computeID)["autoRenew"] != false || recordByID(storages, storageID)["autoRenew"] != false {
				t.Fatalf("owner %s renewal still enabled: computes=%#v storages=%#v", action, computes, storages)
			}
			if recordByID(computes, "compute-other-"+action)["autoRenew"] != true {
				t.Fatalf("owner %s changed another account: %#v", action, computes)
			}
			assertSessionUnauthorized(t, server, ownerSession)
		})
	}
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
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	operator := operatorSessionForTest(t, server)
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
		addAuth(req, operator)
		rec := httptest.NewRecorder()

		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "last_active_admin") {
			t.Fatalf("%s status=%d body=%s, want last admin guard", tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestSupportTicketMappingRequiresExternalTicket(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
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
	service := newTestService(fakeLedgerClient{}, &fakeFabricClient{})
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
	t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"id":"usr-admin","email":"admin@medopl.cn","password":"StableAdminPass2026!","role":"admin","accountId":"`+accountID+`","sub2apiUserId":41}]`)
}

func ensureCustomerMembershipForTest(t *testing.T, server http.Handler, admin *httptest.ResponseRecorder, accountID, userID string) {
	t.Helper()
	organization := createResourceWithSession(t, server, admin, http.MethodPost, "/api/organizations", `{"name":"Test tenant","billingAccountId":"`+accountID+`"}`)
	createResourceWithSession(t, server, admin, http.MethodPost, "/api/organizations/members", `{"organizationId":"`+stringValue(organization["id"])+`","userId":"`+userID+`","accountId":"`+accountID+`","role":"admin"}`)
}

func TestActiveConsoleAPIRoutesReachControlPlane(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
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
		{http.MethodPost, "/api/users", `{"email":"pi@medopl.cn","accountId":"acct-lab","password":"secret","sub2apiUserId":41}`},
		{http.MethodPost, "/api/users/disable", `{"userId":"usr-owner"}`},
		{http.MethodPost, "/api/users/delete", `{"userId":"usr-owner"}`},
		{http.MethodPost, "/api/billing/reconciliation", `{"report":{"id":"recon-test","generatedAt":"2026-07-06T00:00:00Z"}}`},
		{http.MethodPost, "/api/compute-allocations", `{"packageId":"basic","name":"compute"}`},
		{http.MethodPost, "/api/compute-allocations/compute-alpha/sync", `{}`},
		{http.MethodPost, "/api/compute-allocations/compute-alpha/destroy", `{"confirm":true}`},
		{http.MethodPost, "/api/storage-volumes", `{"name":"data","sizeGb":10}`},
		{http.MethodPost, "/api/storage-volumes/storage-alpha/sync", `{}`},
		{http.MethodPost, "/api/storage-volumes/destroy", `{"storageId":"storage-alpha"}`},
		{http.MethodPost, "/api/storage-attachments", `{"computeAllocationId":"compute-alpha","storageId":"storage-alpha","mountPath":"/data"}`},
		{http.MethodPost, "/api/storage-attachments/detach", `{"attachmentId":"attach-alpha"}`},
		{http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"ws-alpha"}`},
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
