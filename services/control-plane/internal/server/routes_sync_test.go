package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"opl-cloud/services/control-plane/internal/controlplane"
)

func syncRequest(t *testing.T, server http.Handler, session *httptest.ResponseRecorder, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	addAuth(req, session)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func decodeSyncPayload(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode sync payload: %v", err)
	}
	return payload
}

func TestWorkspaceSyncAcceptsMutationAndPullsByCursor(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := operatorSessionForTest(t, server)
	project := createResourceWithSession(t, server, admin, http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha","localAliasId":"local-project-alpha"}`)
	projectID := stringValue(project["projectId"])

	created := syncRequest(t, server, admin, http.MethodPost, "/api/workspaces/workspace-alpha/sync/mutations", "sync-mutation-once", `{
		"operationId":"operation-alpha",
		"organizationId":"org-alpha",
		"entityKind":"project",
		"projectId":"`+projectID+`",
		"clientId":"client-alpha",
		"baseVersion":1,
		"operation":"replace",
		"payload":{"title":"Cloud title"},
		"contentDigest":"sha256:alpha",
		"occurredAt":"2026-07-11T00:00:00Z"
	}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create sync mutation status = %d: %s", created.Code, created.Body.String())
	}
	event := decodeSyncPayload(t, created)
	if event["status"] != "accepted" || event["projectId"] != projectID || event["serverVersion"] != float64(2) || numberField(event, "cursor", 0) <= 0 {
		t.Fatalf("unexpected accepted event: %#v", event)
	}

	pulled := syncRequest(t, server, admin, http.MethodGet, "/api/workspaces/workspace-alpha/sync/changes?after=0&limit=10", "", "")
	if pulled.Code != http.StatusOK {
		t.Fatalf("pull sync changes status = %d: %s", pulled.Code, pulled.Body.String())
	}
	page := decodeSyncPayload(t, pulled)
	changes, ok := page["changes"].([]any)
	if !ok || len(changes) != 1 || changes[0].(map[string]any)["id"] != event["id"] || page["nextCursor"] != event["cursor"] {
		t.Fatalf("unexpected sync page: %#v", page)
	}
}

func TestWorkspaceSyncPersistsStaleReplaceConflictWithoutOverwrite(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := operatorSessionForTest(t, server)
	project := createResourceWithSession(t, server, admin, http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`)
	projectID := stringValue(project["projectId"])
	first := syncRequest(t, server, admin, http.MethodPost, "/api/workspaces/workspace-alpha/sync/mutations", "sync-first", `{
		"operationId":"operation-first","organizationId":"org-alpha","entityKind":"project","projectId":"`+projectID+`","clientId":"client-alpha","baseVersion":1,"operation":"replace","payload":{"title":"Cloud title"},"occurredAt":"2026-07-11T00:00:00Z"
	}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first mutation status = %d: %s", first.Code, first.Body.String())
	}
	firstEvent := decodeSyncPayload(t, first)

	conflictRec := syncRequest(t, server, admin, http.MethodPost, "/api/workspaces/workspace-alpha/sync/mutations", "sync-conflict", `{
		"operationId":"operation-conflict","organizationId":"org-alpha","entityKind":"project","projectId":"`+projectID+`","clientId":"client-beta","baseVersion":1,"operation":"replace","payload":{"title":"Offline title"},"occurredAt":"2026-07-11T00:01:00Z"
	}`)
	if conflictRec.Code != http.StatusConflict {
		t.Fatalf("stale replace status = %d: %s", conflictRec.Code, conflictRec.Body.String())
	}
	conflict := decodeSyncPayload(t, conflictRec)
	payload, _ := conflict["payload"].(map[string]any)
	current, _ := payload["current"].(map[string]any)
	incoming, _ := payload["incoming"].(map[string]any)
	if conflict["status"] != "conflict" || stringValue(conflict["conflictId"]) == "" || current["title"] != "Cloud title" || incoming["title"] != "Offline title" || conflict["serverVersion"] != float64(2) {
		t.Fatalf("unexpected durable conflict: %#v", conflict)
	}

	pulled := syncRequest(t, server, admin, http.MethodGet, "/api/workspaces/workspace-alpha/sync/changes?after="+strconv.FormatInt(int64(numberField(firstEvent, "cursor", 0)), 10)+"&limit=10", "", "")
	page := decodeSyncPayload(t, pulled)
	changes := page["changes"].([]any)
	if pulled.Code != http.StatusOK || len(changes) != 1 || changes[0].(map[string]any)["status"] != "conflict" {
		t.Fatalf("conflict was not appended to sync log: status=%d page=%#v", pulled.Code, page)
	}
}

func TestWorkspaceSyncResolvesConflictByAppendingEvent(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := operatorSessionForTest(t, server)
	project := createResourceWithSession(t, server, admin, http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`)
	projectID := stringValue(project["projectId"])
	first := syncRequest(t, server, admin, http.MethodPost, "/api/workspaces/workspace-alpha/sync/mutations", "resolve-first", `{
		"operationId":"resolve-operation-first","organizationId":"org-alpha","entityKind":"project","projectId":"`+projectID+`","clientId":"client-alpha","baseVersion":1,"operation":"replace","payload":{"title":"Cloud title"},"occurredAt":"2026-07-11T00:00:00Z"
	}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first mutation status = %d: %s", first.Code, first.Body.String())
	}
	conflictRec := syncRequest(t, server, admin, http.MethodPost, "/api/workspaces/workspace-alpha/sync/mutations", "resolve-conflict", `{
		"operationId":"resolve-operation-conflict","organizationId":"org-alpha","entityKind":"project","projectId":"`+projectID+`","clientId":"client-beta","baseVersion":1,"operation":"replace","payload":{"title":"Offline title"},"occurredAt":"2026-07-11T00:01:00Z"
	}`)
	conflict := decodeSyncPayload(t, conflictRec)
	conflictID := stringValue(conflict["conflictId"])

	resolvedRec := syncRequest(t, server, admin, http.MethodPost, "/api/workspaces/workspace-alpha/sync/conflicts/"+conflictID+"/resolve", "resolve-once", `{
		"organizationId":"org-alpha","operationId":"resolve-operation-final","clientId":"client-beta","baseVersion":2,"resolution":"accept_incoming","occurredAt":"2026-07-11T00:02:00Z"
	}`)
	if resolvedRec.Code != http.StatusCreated {
		t.Fatalf("resolve conflict status = %d: %s", resolvedRec.Code, resolvedRec.Body.String())
	}
	resolved := decodeSyncPayload(t, resolvedRec)
	resolvedPayload, _ := resolved["payload"].(map[string]any)
	if resolved["status"] != "resolved" || resolved["conflictId"] != conflictID || resolved["serverVersion"] != float64(3) || resolvedPayload["title"] != "Offline title" {
		t.Fatalf("unexpected resolved event: %#v", resolved)
	}

	duplicate := syncRequest(t, server, admin, http.MethodPost, "/api/workspaces/workspace-alpha/sync/conflicts/"+conflictID+"/resolve", "resolve-twice", `{
		"organizationId":"org-alpha","operationId":"resolve-operation-twice","clientId":"client-beta","baseVersion":3,"resolution":"accept_current","occurredAt":"2026-07-11T00:03:00Z"
	}`)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate resolution status = %d, want 409: %s", duplicate.Code, duplicate.Body.String())
	}
}

func TestWorkspaceSyncReplaysSameMutationAndRejectsChangedFingerprint(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := operatorSessionForTest(t, server)
	project := createResourceWithSession(t, server, admin, http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`)
	projectID := stringValue(project["projectId"])
	body := `{"operationId":"idempotent-operation","organizationId":"org-alpha","entityKind":"project","projectId":"` + projectID + `","clientId":"client-alpha","baseVersion":1,"operation":"replace","payload":{"title":"Stable"},"occurredAt":"2026-07-11T00:00:00Z"}`
	created := syncRequest(t, server, admin, http.MethodPost, "/api/workspaces/workspace-alpha/sync/mutations", "idempotent-key", body)
	replayed := syncRequest(t, server, admin, http.MethodPost, "/api/workspaces/workspace-alpha/sync/mutations", "idempotent-key", body)
	if created.Code != http.StatusCreated || replayed.Code != http.StatusOK || decodeSyncPayload(t, created)["id"] != decodeSyncPayload(t, replayed)["id"] {
		t.Fatalf("same mutation did not replay: created=%d replayed=%d", created.Code, replayed.Code)
	}

	changed := syncRequest(t, server, admin, http.MethodPost, "/api/workspaces/workspace-alpha/sync/mutations", "idempotent-key", `{"operationId":"idempotent-operation","organizationId":"org-alpha","entityKind":"project","projectId":"`+projectID+`","clientId":"client-alpha","baseVersion":1,"operation":"replace","payload":{"title":"Changed"},"occurredAt":"2026-07-11T00:00:00Z"}`)
	if changed.Code != http.StatusConflict || !strings.Contains(changed.Body.String(), "idempotency_conflict") {
		t.Fatalf("changed replay status = %d: %s", changed.Code, changed.Body.String())
	}
}
