package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type transferFabricClient struct {
	*fakeFabricClient
	transfer clients.ContentTransfer
	body     []byte
}

type recoveryFabricClient struct{ *fakeFabricClient }

func (f *recoveryFabricClient) CreateStorageSnapshot(_ context.Context, input clients.StorageSnapshotInput, _ string) (clients.StorageSnapshot, error) {
	return clients.StorageSnapshot{ID: "snap-alpha", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, VolumeID: input.VolumeID, Status: "ready", ProviderRequestID: "private-provider-request", SizeGB: 10, CreatedAt: "2026-07-11T00:00:00Z"}, nil
}
func (f *recoveryFabricClient) GetStorageSnapshot(_ context.Context, id string) (clients.StorageSnapshot, error) {
	return clients.StorageSnapshot{ID: id, AccountID: "acct-alpha", WorkspaceID: "ws-alpha", VolumeID: "vol-test", Status: "ready", SizeGB: 10}, nil
}
func (f *recoveryFabricClient) SyncStorageSnapshot(ctx context.Context, id string) (clients.StorageSnapshot, error) {
	return f.GetStorageSnapshot(ctx, id)
}
func (f *recoveryFabricClient) RestoreStorageSnapshot(_ context.Context, _ string, input clients.StorageRestoreInput, _ string) (clients.StorageVolume, error) {
	return clients.StorageVolume{ID: input.TargetVolumeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", SizeGB: 10}, nil
}
func (f *recoveryFabricClient) DestroyStorageSnapshot(_ context.Context, id, _ string) (clients.StorageSnapshot, error) {
	return clients.StorageSnapshot{ID: id, Status: "destroyed"}, nil
}

func TestWorkspaceBackupRestoreCloneAndExportKeepBackendTruth(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &recoveryFabricClient{fakeFabricClient: &fakeFabricClient{}}))
	admin := tenantAdminSessionForTest(t, server)
	compute := createResourceWithSession(t, server, admin, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","packageId":"basic"}`)
	storage := createResourceWithSession(t, server, admin, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","sizeGb":10}`)
	attachment := createResourceWithSession(t, server, admin, http.MethodPost, "/api/storage-attachments", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`"}`)
	workspace := createResourceWithSession(t, server, admin, http.MethodPost, "/api/workspaces", `{"accountId":"acct-alpha","ownerId":"usr-alpha","attachmentId":"`+stringValue(attachment["id"])+`","name":"Alpha"}`)
	workspaceID := stringValue(workspace["id"])

	created := syncRequest(t, server, admin, http.MethodPost, "/api/workspaces/"+workspaceID+"/backups", "backup-once", `{"syncCursor":7,"projectVersions":{"project-alpha":3},"artifactIds":["artifact-alpha"],"receiptIds":["receipt-alpha"],"continuationIds":["continuation-alpha"]}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create backup=%d %s", created.Code, created.Body.String())
	}
	backup := decodeSyncPayload(t, created)
	backupID := stringValue(backup["backupId"])
	if backupID == "" || stringValue(backup["providerRequestId"]) != "" {
		t.Fatalf("backup projection leaked provider facts: %#v", backup)
	}

	exported := syncRequest(t, server, admin, http.MethodGet, "/api/workspace-backups/"+backupID+"/export", "", "")
	if exported.Code != http.StatusOK || strings.Contains(exported.Body.String(), "private-provider-request") || strings.Contains(exported.Body.String(), "providerSnapshot") {
		t.Fatalf("export=%d %s", exported.Code, exported.Body.String())
	}
	restored := syncRequest(t, server, admin, http.MethodPost, "/api/workspace-backups/"+backupID+"/restore", "restore-once", `{"targetStorageId":"vol-restored"}`)
	if restored.Code != http.StatusAccepted || !strings.Contains(restored.Body.String(), "vol-restored") {
		t.Fatalf("restore=%d %s", restored.Code, restored.Body.String())
	}
	stateRec := syncRequest(t, server, admin, http.MethodGet, "/api/state?accountId=acct-alpha", "", "")
	var state map[string]any
	if stateRec.Code != http.StatusOK || json.NewDecoder(stateRec.Body).Decode(&state) != nil {
		t.Fatalf("state=%d %s", stateRec.Code, stateRec.Body.String())
	}
	foundRestored := false
	for _, value := range state["storageVolumes"].([]any) {
		foundRestored = foundRestored || stringValue(value.(map[string]any)["id"]) == "vol-restored"
	}
	if !foundRestored {
		t.Fatalf("restored storage projection missing: %#v", state["storageVolumes"])
	}
	cloned := syncRequest(t, server, admin, http.MethodPost, "/api/workspace-backups/"+backupID+"/clone", "clone-once", `{"name":"Alpha Clone"}`)
	if cloned.Code != http.StatusCreated {
		t.Fatalf("clone=%d %s", cloned.Code, cloned.Body.String())
	}
	clone := decodeSyncPayload(t, cloned)
	if stringValue(clone["workspaceId"]) == workspaceID || stringValue(clone["storageId"]) == stringValue(storage["id"]) {
		t.Fatalf("clone reused source identity: %#v", clone)
	}
	destroyed := syncRequest(t, server, admin, http.MethodPost, "/api/workspace-backups/"+backupID+"/destroy", "destroy-backup-once", "{}")
	if destroyed.Code != http.StatusAccepted || !strings.Contains(destroyed.Body.String(), "destroyed") {
		t.Fatalf("destroy backup=%d %s", destroyed.Code, destroyed.Body.String())
	}
	listed := syncRequest(t, server, admin, http.MethodGet, "/api/workspaces/"+workspaceID+"/backups", "", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"status":"destroyed"`) {
		t.Fatalf("backup state not persisted=%d %s", listed.Code, listed.Body.String())
	}
}

func (f *transferFabricClient) CreateTransfer(_ context.Context, input clients.ContentTransferInput, _ string) (clients.ContentTransfer, error) {
	f.transfer = clients.ContentTransfer{TransferID: "transfer-alpha", OrganizationID: input.OrganizationID, WorkspaceID: input.WorkspaceID, ProjectID: input.ProjectID, Path: input.Path, Digest: input.Digest, Size: input.Size, ChunkSize: 4 << 20, ChunkCount: 1, Status: "uploading"}
	return f.transfer, nil
}
func (f *transferFabricClient) Transfer(_ context.Context, _ string) (clients.ContentTransfer, error) {
	return f.transfer, nil
}
func (f *transferFabricClient) PutTransferChunk(_ context.Context, _ string, index int, body []byte, _ string) (clients.ContentTransfer, error) {
	f.body = append([]byte(nil), body...)
	f.transfer.ReceivedChunks = []int{index}
	return f.transfer, nil
}
func (f *transferFabricClient) CompleteTransfer(_ context.Context, _ string) (clients.ContentTransfer, error) {
	f.transfer.Status = "completed"
	return f.transfer, nil
}
func (f *transferFabricClient) Content(_ context.Context, _ string, digest string) (clients.FabricContent, error) {
	return clients.FabricContent{Digest: digest, WorkspaceID: f.transfer.WorkspaceID, Path: f.transfer.Path, Body: f.body}, nil
}

func TestWorkspaceContentTransferIsAuthorizedAndStreamedThroughFabric(t *testing.T) {
	fabricClient := &transferFabricClient{fakeFabricClient: &fakeFabricClient{}}
	server := newExecutionTestServer(t, controlplane.NewService(fakeLedgerClient{}, fabricClient))
	admin := tenantAdminSessionForTest(t, server)
	project := createResourceWithSession(t, server, admin, http.MethodPost, "/api/projects", `{"organizationId":"org-alpha","workspaceId":"workspace-alpha"}`)
	projectID := stringValue(project["projectId"])
	unknown := syncRequest(t, server, admin, http.MethodPost, "/api/workspaces/workspace-alpha/transfers", "transfer-unknown", `{"organizationId":"org-alpha","projectId":"project-missing","path":"inputs/a.txt","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":7}`)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown project=%d %s", unknown.Code, unknown.Body.String())
	}
	created := syncRequest(t, server, admin, http.MethodPost, "/api/workspaces/workspace-alpha/transfers", "transfer-once", `{"organizationId":"org-alpha","projectId":"`+projectID+`","path":"inputs/a.txt","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":7}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}
	chunk := httptest.NewRequest(http.MethodPut, "/api/workspaces/workspace-alpha/transfers/transfer-alpha/chunks/0", bytes.NewBufferString("content"))
	chunk.Header.Set("Content-Type", "application/octet-stream")
	chunk.Header.Set("X-Chunk-SHA256", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	addAuth(chunk, admin)
	chunkRec := httptest.NewRecorder()
	server.ServeHTTP(chunkRec, chunk)
	if chunkRec.Code != http.StatusOK {
		t.Fatalf("chunk=%d %s", chunkRec.Code, chunkRec.Body.String())
	}
	completed := syncRequest(t, server, admin, http.MethodPost, "/api/workspaces/workspace-alpha/transfers/transfer-alpha/complete", "", "")
	if completed.Code != http.StatusOK {
		t.Fatalf("complete=%d %s", completed.Code, completed.Body.String())
	}
	downloaded := syncRequest(t, server, admin, http.MethodGet, "/api/workspaces/workspace-alpha/contents/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", "")
	body, _ := io.ReadAll(downloaded.Body)
	if downloaded.Code != http.StatusOK || string(body) != "content" {
		t.Fatalf("download=%d %q", downloaded.Code, body)
	}
}

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
	server := newExecutionTestServer(t, controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := tenantAdminSessionForTest(t, server)
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
	server := newExecutionTestServer(t, controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := tenantAdminSessionForTest(t, server)
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
	server := newExecutionTestServer(t, controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := tenantAdminSessionForTest(t, server)
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
	server := newExecutionTestServer(t, controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	admin := tenantAdminSessionForTest(t, server)
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
