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

func TestPublicResponsesSetSecurityHeaders(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	want := map[string]string{
		"Content-Security-Policy":   "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; object-src 'none'; form-action 'self'",
		"Strict-Transport-Security": "max-age=63072000; includeSubDomains; preload",
		"X-Content-Type-Options":    "nosniff",
		"X-Frame-Options":           "DENY",
		"Referrer-Policy":           "no-referrer",
	}
	for _, test := range []struct {
		name, path string
		status     int
	}{
		{name: "success", path: "/api/healthz", status: http.StatusOK},
		{name: "error", path: "/api/auth/me", status: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, test.path, nil))
			if rec.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, test.status, rec.Body.String())
			}
			for name, value := range want {
				if got := rec.Header().Get(name); got != value {
					t.Fatalf("%s = %q, want %q", name, got, value)
				}
			}
		})
	}
}

func TestProductionOperatorRoutesDoNotRequireClientNetworkConfiguration(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	operator := reservedOperatorSessionForTest(t, server)
	t.Setenv("NODE_ENV", "production")
	t.Setenv("OPL_OPERATOR_CIDRS", "invalid")
	t.Setenv("OPL_TRUSTED_PROXY_CIDRS", "invalid")
	req := httptest.NewRequest(http.MethodGet, "/api/operator/announcements", nil)
	req.RemoteAddr = "198.51.100.9:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, unknown")
	addSessionCookies(req, operator)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("production operator status = %d, want 200 without a network gate: %s", rec.Code, rec.Body.String())
	}
}

func TestProviderAcceptanceTokenDoesNotDependOnOperatorNetworkConfiguration(t *testing.T) {
	t.Setenv("NODE_ENV", "production")
	t.Setenv("OPL_OPERATOR_CIDRS", "invalid")
	t.Setenv("OPL_TRUSTED_PROXY_CIDRS", "invalid")
	t.Setenv("OPL_PROVIDER_ACCEPTANCE_TOKEN", testProviderAcceptanceToken)
	called := false
	protected := newControlPlaneApp().providerAcceptanceProtected(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	acceptance := httptest.NewRequest(http.MethodPost, "/api/operator/provider-acceptance", bytes.NewBufferString(`{}`))
	acceptance.RemoteAddr = "198.51.100.9:1234"
	acceptance.Header.Set("x-opl-provider-acceptance-token", testProviderAcceptanceToken)
	acceptanceRec := httptest.NewRecorder()
	protected(acceptanceRec, acceptance)
	if acceptanceRec.Code != http.StatusNoContent || !called {
		t.Fatalf("Provider Acceptance token route = %d called=%v", acceptanceRec.Code, called)
	}
}

func newExecutionTestServer(t *testing.T, service *controlplane.Service) http.Handler {
	t.Helper()
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@execution.example")
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

	session := tenantOwnerSessionForTest(t, server)
	response := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/workspaces/workspace-alpha/resume", `{"computeAllocationId":"compute-new","attachmentId":"attachment-new"}`, "resume-owner-mismatch")
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "workspace_owner_required") || len(fabric.gatewaySecretInputs) != 0 || len(fabric.runtimeInputs) != 0 {
		t.Fatalf("owner mismatch resume status=%d gateway=%d runtime=%d body=%s", response.Code, len(fabric.gatewaySecretInputs), len(fabric.runtimeInputs), response.Body.String())
	}
}

func TestResumeWorkspaceValidatesRetainedResourcesBeforeFabric(t *testing.T) {
	calls := []string{}
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	seedTenantMember(t, store, "acct-beta", "org-beta", "usr-beta", "beta@example.com")
	workspace := map[string]any{
		"id": "workspace-alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha", "ownerUserId": "usr-alpha",
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
	owner := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	outsider := loginForTest(t, server, "beta@example.com", "CorrectHorseBatteryStaple!")
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
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{"id": "workspace-alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha", "ownerUserId": "usr-alpha", "state": "suspended", "status": "suspended", "storageId": "storage-alpha", "url": "https://workspace.medopl.cn/w/workspace-alpha/"}))
	mustStore(t, store.SaveStorage(context.Background(), map[string]any{"id": "storage-alpha", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "available", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z"}))
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{"id": "compute-new", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "running", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z"}))
	mustStore(t, store.SaveAttachment(context.Background(), map[string]any{"id": "attachment-new", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "computeAllocationId": "compute-new", "storageId": "storage-alpha", "status": "attached"}))
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatalf("create audit failure server: %v", err)
	}
	response := requestWithSession(t, server, loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!"), http.MethodPost, "/api/workspaces/workspace-alpha/resume", `{"computeAllocationId":"compute-new","attachmentId":"attachment-new"}`)
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
	store := newMemoryTableStore()
	calls := []string{}
	fabric := &fakeFabricClient{calls: &calls, runtimeStatus: clients.WorkspaceRuntime{
		ID: "runtime-from-fabric", URL: "https://workspace.medopl.cn/w/ws-from-fabric/", Status: "unready", ServiceName: "opl-compute-from-fabric", Ready: false,
		Access: clients.WorkspaceRuntimeAccess{Username: "opl", Password: "runtime-password-alpha", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-compute-from-fabric-env"},
		Checks: []any{
			map[string]any{"name": "deployment_ready", "ok": true},
			map[string]any{"name": "service_endpoints_ready", "ok": false},
		},
	}}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, fabric), store)
	if err != nil {
		t.Fatal(err)
	}
	session := tenantOwnerSessionForTest(t, server)
	seedRuntimeAccessWorkspaceForTest(t, store, sessionUserIDForTest(t, server, session), nil)
	fabric.runtimeStatus.WorkspaceID = "ws-alpha"
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/ws-alpha/runtime-status", nil)
	addSessionCookies(req, session)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	body = mapField(body, "data")
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

func TestWorkspaceRuntimeStatusDoesNotMutateProjection(t *testing.T) {
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
	before, err := store.ListWorkspaces(context.Background(), "acct-alpha")
	if err != nil {
		t.Fatal(err)
	}

	response := requestWithSession(t, server, session, http.MethodGet, "/api/workspaces/ws-alpha/runtime-status", "")
	if response.Code != http.StatusOK {
		t.Fatalf("runtime status = %d: %s", response.Code, response.Body.String())
	}
	var runtime map[string]any
	if err := json.NewDecoder(response.Body).Decode(&runtime); err != nil {
		t.Fatalf("decode runtime status: %v", err)
	}
	runtime = mapField(runtime, "data")
	if runtime["ready"] != true || nested(runtime, "access", "credentialStatus") != "configured" || nested(runtime, "access", "password") != nil || nested(runtime, "access", "secretRef") != nil {
		t.Fatalf("runtime status must return ready state without credentials: %#v", runtime)
	}
	stored, err := store.ListWorkspaces(context.Background(), "acct-alpha")
	if err != nil || len(stored) != 1 {
		t.Fatalf("list workspaces: rows=%#v err=%v", stored, err)
	}
	if !reflect.DeepEqual(before, stored) {
		t.Fatalf("runtime source mutated Workspace: before=%#v after=%#v", before, stored)
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

	response := requestWithSession(t, server, session, http.MethodGet, "/api/workspaces/ws-alpha/runtime-status", "")
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
	owner := tenantOwnerSessionForTest(t, server)

	before := len(calls)
	response := requestWithSession(t, server, owner, http.MethodGet, "/api/workspaces/ws-unknown/runtime-status", "")
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "workspace_not_found") {
		t.Fatalf("unknown runtime status = %d: %s", response.Code, response.Body.String())
	}
	if len(calls) != before || strings.Contains(response.Body.String(), "runtime-password-alpha") {
		t.Fatalf("unknown projection reached Fabric or returned a password: calls=%#v body=%s", calls[before:], response.Body.String())
	}
}

func TestWorkspaceRuntimeStatusForbidsCrossAccountSecretRead(t *testing.T) {
	store := newMemoryTableStore()
	calls := []string{}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{calls: &calls}), store)
	if err != nil {
		t.Fatal(err)
	}
	owner := tenantOwnerSessionForTest(t, server)
	handler := server.(*controlPlaneHTTPHandler)
	if _, err := handler.app.createUser(context.Background(), handler.service, map[string]any{
		"email": "beta@lab.example", "accountId": "acct-beta", "password": "CorrectHorseBatteryStaple!",
	}); err != nil {
		t.Fatal(err)
	}
	outsider := loginForTest(t, server, "beta@lab.example", "CorrectHorseBatteryStaple!")
	seedRuntimeAccessWorkspaceForTest(t, store, sessionUserIDForTest(t, server, owner), nil)

	before := len(calls)
	response := requestWithSession(t, server, outsider, http.MethodGet, "/api/workspaces/ws-alpha/runtime-status", "")
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "workspace_not_found") {
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

func TestSessionCredentialRequiresLoginAfterServerRestart(t *testing.T) {
	path := t.TempDir() + "/control-plane-state.sqlite"
	service := newTestService(fakeLedgerClient{}, &fakeFabricClient{})
	server, err := NewPersistentServer(service, NewTestEntStateStore(t, path))
	if err != nil {
		t.Fatalf("create persistent server: %v", err)
	}
	session := tenantAdminSessionForTest(t, server)

	restarted, err := NewPersistentServer(service, NewTestEntStateStore(t, path))
	if err != nil {
		t.Fatalf("restart persistent server: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	addSessionCookies(req, session)
	rec := httptest.NewRecorder()
	restarted.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "reauthentication_required") || !strings.Contains(rec.Header().Get("Set-Cookie"), sessionCookieName+"=;") {
		t.Fatalf("restart status=%d cookie=%q body=%s", rec.Code, rec.Header().Get("Set-Cookie"), rec.Body.String())
	}
}

func TestCustomerOwnerCannotSelectAnotherAccount(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	for _, input := range []map[string]any{
		{"email": "alpha@lab.example", "accountId": "acct-alpha", "password": "CorrectHorseBatteryStaple!"},
		{"email": "beta@lab.example", "accountId": "acct-beta", "password": "CorrectHorseBatteryStaple!"},
	} {
		if _, err := createIdentityUser(server, input); err != nil {
			t.Fatal(err)
		}
	}
	alpha := loginForTest(t, server, "alpha@lab.example", "CorrectHorseBatteryStaple!")

	readOther := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-beta", nil)
	addSessionCookies(readOther, alpha)
	readOtherRec := httptest.NewRecorder()
	server.ServeHTTP(readOtherRec, readOther)
	if readOtherRec.Code != http.StatusForbidden {
		t.Fatalf("cross-account state status = %d, want 403: %s", readOtherRec.Code, readOtherRec.Body.String())
	}

	retiredWrite := requestWithSession(t, server, alpha, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-beta","packageId":"basic"}`)
	if retiredWrite.Code != http.StatusNotFound {
		t.Fatalf("retired cross-account compute create status = %d, want 404: %s", retiredWrite.Code, retiredWrite.Body.String())
	}

	mapOtherTicket := requestWithSession(t, server, alpha, http.MethodPost, "/api/support/tickets", `{"accountId":"acct-beta","externalTicketId":"ZAM-403","title":"wrong account"}`)
	if mapOtherTicket.Code != http.StatusForbidden {
		t.Fatalf("cross-account support mapping status = %d, want 403: %s", mapOtherTicket.Code, mapOtherTicket.Body.String())
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

type fakeLedgerClient struct{}

type testSub2APIClient struct {
	mu                  sync.Mutex
	balance             int64
	charges             map[string]int64
	workspaceKey        clients.Sub2APIWorkspaceKey
	workspaceKeyErr     error
	workspaceKeyUserIDs []int64
	identities          map[string]clients.Sub2APIIdentity
	passwords           map[string]string
}

func (*testSub2APIClient) Version(context.Context) (string, error) { return "0.1.155", nil }

func (*testSub2APIClient) AdminIdentity(context.Context) (clients.Sub2APIIdentity, error) {
	return clients.Sub2APIIdentity{ID: 1, Email: "admin@medopl.cn", Status: "active"}, nil
}

func testSub2APIUserID(email string) int64 {
	email = normalizeEmail(email)
	if strings.HasPrefix(email, "beta") || strings.HasPrefix(email, "verification-slot-pro-") {
		return 42
	}
	return 41
}

func (c *testSub2APIClient) ResolveOrCreateUser(_ context.Context, email, password string) (clients.Sub2APIIdentity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	email = normalizeEmail(email)
	if c.identities == nil {
		c.identities = map[string]clients.Sub2APIIdentity{}
		c.passwords = map[string]string{}
	}
	if identity, ok := c.identities[email]; ok {
		return identity, nil
	}
	identity := clients.Sub2APIIdentity{ID: int64(41 + len(c.identities)), Email: email, Status: "active"}
	c.identities[email], c.passwords[email] = identity, password
	return identity, nil
}

func (c *testSub2APIClient) AuthenticateUser(_ context.Context, email, password string) (clients.Sub2APIUserAuthentication, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	email = normalizeEmail(email)
	identity, ok := c.identities[email]
	if !ok {
		if password == "CorrectHorseBatteryStaple!" {
			return clients.Sub2APIUserAuthentication{Identity: clients.Sub2APIIdentity{ID: testSub2APIUserID(email), Email: email, Status: "active"}, AccessToken: "test-user-delegated-token"}, nil
		}
		return clients.Sub2APIUserAuthentication{}, clients.ErrSub2APIInvalidCredentials
	}
	if c.passwords[email] != password {
		return clients.Sub2APIUserAuthentication{}, clients.ErrSub2APIInvalidCredentials
	}
	return clients.Sub2APIUserAuthentication{Identity: identity, AccessToken: "test-user-delegated-token"}, nil
}

func (c *testSub2APIClient) UserIdentity(_ context.Context, userID int64, email string) (clients.Sub2APIIdentity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	identity, ok := c.identities[normalizeEmail(email)]
	if !ok || identity.ID != userID {
		return clients.Sub2APIIdentity{}, clients.ErrSub2APIIdentityConflict
	}
	return identity, nil
}

func (c *testSub2APIClient) User(_ context.Context, userID int64) (clients.Sub2APIIdentity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, identity := range c.identities {
		if identity.ID == userID {
			return identity, nil
		}
	}
	if userID == 1 {
		return clients.Sub2APIIdentity{ID: 1, Email: "admin@medopl.cn", Status: "active"}, nil
	}
	if userID == 41 || userID == 42 {
		return clients.Sub2APIIdentity{ID: userID, Email: fmt.Sprintf("user-%d@example.com", userID), Status: "active"}, nil
	}
	return clients.Sub2APIIdentity{}, clients.ErrSub2APIIdentityConflict
}

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

type internalReadinessFabricClient struct {
	fakeFabricClient
}

func (internalReadinessFabricClient) Readiness(_ context.Context) (map[string]any, error) {
	return map[string]any{
		"provider": "tencent-tke", "ready": true, "cloudImagesReady": true, "workspaceImagesReady": true, "immutableImagesReady": true,
		"checks": []any{map[string]any{"detail": "internal secret"}}, "missingEnv": []string{"INTERNAL_SECRET"}, "internalCredential": "secret-value",
	}, nil
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
	runtimeResults       []clients.WorkspaceRuntime
	runtimeErr           error
	attachment           clients.StorageAttachment
	runtimeStatus        clients.WorkspaceRuntime
	runtimeStatusResults []clients.WorkspaceRuntime
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
	nodePoolID := ""
	if input.ResourceType == "compute" {
		requestIDs = map[string]string{"nodePool": "node-pool-request", "subnets": "subnets-request", "availability": "availability-request"}
		nodePoolID = "np-" + input.PackageID
	}
	return clients.MonthlyPreflight{
		ResourceType: input.ResourceType, PackageID: input.PackageID, NodePoolID: nodePoolID, SizeGB: input.SizeGB, Zone: input.Zone,
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
	if f.attachment.ID != "" {
		return f.attachment, nil
	}
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
	if len(f.runtimeResults) > 0 {
		result := f.runtimeResults[0]
		f.runtimeResults = f.runtimeResults[1:]
		return result, nil
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
	if len(f.runtimeStatusResults) > 0 {
		result := f.runtimeStatusResults[0]
		f.runtimeStatusResults = f.runtimeStatusResults[1:]
		return result, nil
	}
	if f.runtimeStatus.ID != "" {
		result := f.runtimeStatus
		if result.WorkspaceID == "" {
			result.WorkspaceID = workspaceID
		}
		return result, nil
	}
	if f.runtime.ID != "" {
		result := f.runtime
		if result.WorkspaceID == "" {
			result.WorkspaceID = workspaceID
		}
		return result, nil
	}
	if len(f.runtimeInputs) > 0 {
		input := f.runtimeInputs[len(f.runtimeInputs)-1]
		return clients.WorkspaceRuntime{ID: "runtime-from-fabric", WorkspaceID: input.WorkspaceID, URL: "https://workspace.medopl.cn/w/ws-from-fabric/", Status: "running", ServiceName: "opl-compute-from-fabric", Access: clients.WorkspaceRuntimeAccess{Username: "admin", Password: "runtime-password-alpha", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-compute-from-fabric-env"}, Ready: true}, nil
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
	return map[string]any{"provider": "tencent-tke", "ready": true, "cloudImagesReady": true, "workspaceImagesReady": true, "immutableImagesReady": true, "missingEnv": []string{}, "missingTools": []string{}}, nil
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
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha-project@example.com")
	seedTenantMember(t, store, "acct-beta", "org-beta", "usr-beta", "beta-project@example.com")
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{"id": "workspace-beta", "accountId": "acct-beta", "status": "running"}))
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	owner := loginForTest(t, server, "alpha-project@example.com", "CorrectHorseBatteryStaple!")

	forbidden := requestWithSession(t, server, owner, http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-beta"}`)
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

	betaOwner := loginForTest(t, server, "beta-project@example.com", "CorrectHorseBatteryStaple!")
	created := requestWithSession(t, server, betaOwner, http.MethodPost, "/api/projects", `{"organizationId":"org-beta","workspaceId":"workspace-beta"}`)
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
			seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-project-owner", "project-owner@example.com")
			mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{"id": "workspace-alpha", "accountId": "acct-alpha", "status": "running"}))
			server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
			if err != nil {
				t.Fatalf("create server: %v", err)
			}

			session := loginForTest(t, server, "project-owner@example.com", "CorrectHorseBatteryStaple!")
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
		name           string
		ownerOrgID     string
		workspace      map[string]any
		organizationID string
		status         int
		errorCode      string
	}{
		{name: "workspace", ownerOrgID: "org-alpha", organizationID: "org-alpha", status: http.StatusNotFound, errorCode: "workspace_not_found"},
		{name: "organization", ownerOrgID: "org-session", workspace: map[string]any{"id": "workspace-alpha", "accountId": "acct-alpha", "status": "running"}, organizationID: "org-alpha", status: http.StatusForbidden, errorCode: "organization_membership_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryTableStore()
			seedTenantMember(t, store, "acct-alpha", tc.ownerOrgID, "usr-project-owner", "project-owner@example.com")
			if tc.workspace != nil {
				mustStore(t, store.SaveWorkspace(context.Background(), tc.workspace))
			}
			server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
			if err != nil {
				t.Fatalf("create server: %v", err)
			}
			session := loginForTest(t, server, "project-owner@example.com", "CorrectHorseBatteryStaple!")
			rec := requestWithSession(t, server, session, http.MethodPost, "/api/projects", `{"organizationId":"`+tc.organizationID+`","workspaceId":"workspace-alpha"}`)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.errorCode) {
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

func TestOwnerSyncsExecutionAndReadsContinuation(t *testing.T) {
	server := newExecutionTestServer(t, newTestService(&executionCompletionLedgerClient{}, &completedExecutionFabricClient{}))
	owner := loginForTest(t, server, "alpha@execution.example", "CorrectHorseBatteryStaple!")

	project := createResourceWithSession(t, server, owner, http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`)
	projectID := stringValue(project["projectId"])
	task := createResourceWithSession(t, server, owner, http.MethodPost, "/api/projects/"+projectID+"/tasks", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`)
	request := createResourceWithSession(t, server, owner, http.MethodPost, "/api/execution-requests", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"`+projectID+`","taskId":"`+stringValue(task["taskId"])+`"}`)
	requestID := stringValue(request["requestId"])
	createResourceWithSession(t, server, owner, http.MethodPost, "/api/execution-requests/"+requestID+"/approve", `{}`)
	createResourceWithSession(t, server, owner, http.MethodPost, "/api/execution-requests/"+requestID+"/execute", `{}`)
	synced := createResourceWithSession(t, server, owner, http.MethodPost, "/api/execution-requests/"+requestID+"/sync", `{}`)
	if synced["status"] != "completed" || synced["receiptId"] != "receipt-final" || synced["continuationId"] != "continuation-final" {
		t.Fatalf("unexpected synced execution: %#v", synced)
	}

	continuationRec := requestWithSession(t, server, owner, http.MethodGet, "/api/execution-requests/"+requestID+"/continuation", "")
	if continuationRec.Code != http.StatusOK || !strings.Contains(continuationRec.Body.String(), "continuation-final") {
		t.Fatalf("continuation status = %d: %s", continuationRec.Code, continuationRec.Body.String())
	}

	seedTenantMember(t, server.(*controlPlaneHTTPHandler).app.tables, "acct-beta", "org-beta", "usr-beta", "beta-continuation@example.com")
	outsider := loginForTest(t, server, "beta-continuation@example.com", "CorrectHorseBatteryStaple!")
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

func TestExecutionRoutesAuthorizeStrictOwners(t *testing.T) {
	server := newExecutionTestServer(t, newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	owner := loginForTest(t, server, "alpha@execution.example", "CorrectHorseBatteryStaple!")
	rec := requestWithSession(t, server, owner, http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("owner status = %d body=%s, want created", rec.Code, rec.Body.String())
	}

	seedTenantMember(t, server.(*controlPlaneHTTPHandler).app.tables, "acct-beta", "org-beta", "usr-beta", "beta-execution@example.com")
	outsider := loginForTest(t, server, "beta-execution@example.com", "CorrectHorseBatteryStaple!")
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
	return strings.HasPrefix(path, "/api/operator") || strings.HasPrefix(path, "/api/management") || strings.HasPrefix(path, "/api/billing/reconciliation")
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
	return tenantOwnerSessionForTest(t, server)
}

func tenantOwnerSessionForTest(t *testing.T, server http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	handler := server.(*controlPlaneHTTPHandler)
	users, err := handler.app.tables.ListUsers(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		if user["accountId"] == "acct-alpha" && user["role"] == "owner" {
			return loginForTest(t, server, stringValue(user["email"]), "CorrectHorseBatteryStaple!")
		}
	}
	email := "tenant-owner-" + newResourceID("test") + "@example.com"
	if _, err := handler.app.createUser(context.Background(), handler.service, map[string]any{
		"email": email, "accountId": "acct-alpha", "password": "CorrectHorseBatteryStaple!",
	}); err != nil {
		t.Fatal(err)
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
	payload, sessionID, err := handler.app.createSession(admin, "test-operator-delegated-token")
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
	return stringValue(mapField(payload, "data")["consoleUserId"])
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
	service := newTestService(fakeLedgerClient{}, &fabricClientWithUnscopedHistoricOperation{})
	server, err := NewPersistentServer(service, NewTestEntStateStore(t, t.TempDir()+"/historic-fabric.sqlite"))
	if err != nil {
		t.Fatalf("create persistent server: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-alpha", nil)
	session := tenantAdminSessionForTest(t, server)
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

	launchRec := requestWithSession(t, server, session, http.MethodPost, "/api/workspace-launches", `{"name":"Blocked","packageId":"basic","sizeGb":10,"autoRenew":false}`)
	if launchRec.Code != http.StatusConflict {
		t.Fatalf("launch status = %d, want 409: %s", launchRec.Code, launchRec.Body.String())
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

func TestOverviewHTTPIsRetired(t *testing.T) {
	server := NewServer(newTestService(nil, nil))
	req := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	addSessionCookies(req, tenantAdminSessionForTest(t, server))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
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
	body := `{"name":"` + strings.Repeat("x", int(maxJSONBodyBytes)+1) + `","packageId":"basic","sizeGb":10,"autoRenew":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/workspace-launches", bytes.NewBufferString(body))
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

func TestProductionReadinessReturnsOnlyCustomerSafeImmutableImageFacts(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &internalReadinessFabricClient{}))
	req := httptest.NewRequest(http.MethodGet, "/api/production/readiness", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	var body map[string]any
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		t.Fatalf("production readiness = %d %s", rec.Code, rec.Body.String())
	}
	want := map[string]any{"provider": "tencent-tke", "ready": true, "cloudImagesReady": true, "workspaceImagesReady": true, "immutableImagesReady": true, "checks": []any{}}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("production readiness leaked internal facts: got %#v want %#v", body, want)
	}
}

func TestProtectedAPIRoutesRequireSessionCSRFAndAdminRole(t *testing.T) {
	t.Setenv("OPL_OPERATOR_SUMMARY_TOKEN", "operator-secret")
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))

	postWithoutSession := httptest.NewRequest(http.MethodPost, "/api/workspace-launches", bytes.NewBufferString(`{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`))
	postWithoutSession.Header.Set("Content-Type", "application/json")
	postWithoutSession.Header.Set("Idempotency-Key", "compute-no-session")
	postWithoutSessionRec := httptest.NewRecorder()
	server.ServeHTTP(postWithoutSessionRec, postWithoutSession)
	if postWithoutSessionRec.Code != http.StatusUnauthorized {
		t.Fatalf("write without session status = %d, want 401: %s", postWithoutSessionRec.Code, postWithoutSessionRec.Body.String())
	}

	admin := tenantAdminSessionForTest(t, server)
	postWithoutCSRF := httptest.NewRequest(http.MethodPost, "/api/workspace-launches", bytes.NewBufferString(`{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`))
	postWithoutCSRF.Header.Set("Content-Type", "application/json")
	postWithoutCSRF.Header.Set("Idempotency-Key", "compute-no-csrf")
	addSessionCookies(postWithoutCSRF, admin)
	postWithoutCSRFRec := httptest.NewRecorder()
	server.ServeHTTP(postWithoutCSRFRec, postWithoutCSRF)
	if postWithoutCSRFRec.Code != http.StatusForbidden {
		t.Fatalf("write without csrf status = %d, want 403: %s", postWithoutCSRFRec.Code, postWithoutCSRFRec.Body.String())
	}

	adminReq := httptest.NewRequest(http.MethodPost, "/api/billing/reconciliation", bytes.NewBufferString(`{"confirm":true,"report":{"id":"recon-nonadmin","status":"ok"}}`))
	adminReq.Header.Set("Content-Type", "application/json")
	adminReq.Header.Set("Idempotency-Key", "topup-nonadmin")
	addSessionCookies(adminReq, admin)
	adminReq.Header.Set("x-opl-csrf", admin.Header().Get("x-opl-csrf-token"))
	adminReqRec := httptest.NewRecorder()
	server.ServeHTTP(adminReqRec, adminReq)
	if adminReqRec.Code != http.StatusForbidden {
		t.Fatalf("admin route as owner status = %d, want 403: %s", adminReqRec.Code, adminReqRec.Body.String())
	}

	allowed := requestWithSession(t, server, admin, http.MethodPost, "/api/pricing/preview", `{"resourceType":"workspace","packageId":"basic","sizeGb":10}`)
	if allowed.Code != http.StatusOK {
		t.Fatalf("admin csrf request did not reach protected route: status=%d body=%s", allowed.Code, allowed.Body.String())
	}
}

func TestPerResourceAutoRenewRouteIsRemoved(t *testing.T) {
	calls := []string{}
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{calls: &calls}))
	session := tenantAdminSessionForTest(t, server)
	for _, resourceID := range []string{"compute-alpha", "storage-alpha"} {
		response := requestWithSession(t, server, session, http.MethodPost, "/api/resources/"+resourceID+"/auto-renew", `{"autoRenew":true}`)
		if response.Code != http.StatusNotFound {
			t.Fatalf("per-resource auto-renew status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if len(calls) != 0 {
		t.Fatalf("removed per-resource auto-renew touched Fabric: %#v", calls)
	}
}

func TestHighRiskMutationsRequireBackendConfirmation(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := operatorSessionForTest(t, server)

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/billing/reconciliation", `{"report":{"id":"recon-confirm","generatedAt":"2026-07-06T00:00:00Z"}}`},
		{http.MethodPost, "/api/operator/archive-terminal-resources", `{"reason":"test"}`},
	} {
		rec := requestWithSession(t, server, admin, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "confirmation_required") {
			t.Fatalf("%s %s status=%d body=%s, want confirmation_required", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestLoginSessionMeAndLogoutUseRemotePassword(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	if _, err := createIdentityUser(server, map[string]any{
		"email": "owner@lab.example", "accountId": "acct-alpha", "password": "CorrectHorseBatteryStaple!",
	}); err != nil {
		t.Fatal(err)
	}

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
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	if _, err := createIdentityUser(server, map[string]any{
		"email": "owner@lab.example", "accountId": "acct-alpha", "password": "CorrectHorseBatteryStaple!",
	}); err != nil {
		t.Fatal(err)
	}

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

func TestAccountDisableStopsRenewalAndRevokesOwnerSession(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	operator := operatorSessionForTest(t, server)
	const accountID = "acct-owner-disable"
	if _, err := createIdentityUser(server, map[string]any{
		"email": "owner-disable@example.com", "accountId": accountID, "password": "CorrectHorseBatteryStaple!",
	}); err != nil {
		t.Fatal(err)
	}
	ownerSession := loginForTest(t, server, "owner-disable@example.com", "CorrectHorseBatteryStaple!")
	app := server.(*controlPlaneHTTPHandler).app
	computeID, storageID := "compute-owner-disable", "storage-owner-disable"
	mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{"id": computeID, "accountId": accountID, "autoRenew": true}))
	mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{"id": storageID, "accountId": accountID, "autoRenew": true}))
	mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{"id": "compute-other-disable", "accountId": "acct-other", "autoRenew": true}))

	response := requestWithMutationKeyForTest(t, server, operator, http.MethodPost, "/api/operator/accounts/"+accountID+"/disable", `{"confirmationAccountId":"`+accountID+`","reason":"pilot_offboarding"}`, "disable-owner-account")
	if response.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", response.Code, response.Body.String())
	}
	computes, _ := app.tables.ListComputes(context.Background(), "")
	storages, _ := app.tables.ListStorages(context.Background(), accountID)
	if recordByID(computes, computeID)["autoRenew"] != false || recordByID(storages, storageID)["autoRenew"] != false {
		t.Fatalf("disable left renewal enabled: computes=%#v storages=%#v", computes, storages)
	}
	if recordByID(computes, "compute-other-disable")["autoRenew"] != true {
		t.Fatalf("disable changed another account: %#v", computes)
	}
	assertSessionUnauthorized(t, server, ownerSession)
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
	path := t.TempDir() + "/control-plane-state.sqlite"
	service := newTestService(fakeLedgerClient{}, &fakeFabricClient{})
	server, err := NewPersistentServer(service, NewTestEntStateStore(t, path))
	if err != nil {
		t.Fatalf("create persistent server: %v", err)
	}
	session := tenantAdminSessionForTest(t, server)
	body := `{"accountId":"acct-alpha","userId":"` + sessionUserIDForTest(t, server, session) + `","workspaceId":"ws-alpha","externalSystem":"zammad","externalTicketId":"ZAM-42","externalUrl":"https://support.example/tickets/42","resourceIds":["compute-alpha"],"operationId":"op-alpha","title":"Workspace failed","description":"provider timeout"}`
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
		{http.MethodGet, "/api/operator/overview", ""},
		{http.MethodGet, "/api/runtime/readiness", ""},
		{http.MethodGet, "/api/production/readiness", ""},
		{http.MethodGet, "/api/workspaces", ""},
		{http.MethodGet, "/api/workspaces/ws-alpha/runtime-status", ""},
		{http.MethodGet, "/api/support/tickets", ""},
		{http.MethodPost, "/api/auth/logout", `{}`},
		{http.MethodPost, "/api/billing/reconciliation", `{"report":{"id":"recon-test","generatedAt":"2026-07-06T00:00:00Z"}}`},
		{http.MethodPost, "/api/workspace-launches", `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`},
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

			if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
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
