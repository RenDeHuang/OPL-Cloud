package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
)

type workspaceDeleteFabric struct {
	fakeFabricClient
	mu            sync.Mutex
	calls         []string
	failStage     string
	failures      int
	mismatchStage string
	unknownStage  string
	storageStatus string
	computeReads  []string
}

func (f *workspaceDeleteFabric) call(stage, id, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, stage+":"+id+":"+key)
	if f.failStage == stage && f.failures > 0 {
		f.failures--
		return errors.New("fabric owner unavailable")
	}
	return nil
}

func (f *workspaceDeleteFabric) DestroyWorkspaceRuntime(_ context.Context, workspaceID, key string) (clients.WorkspaceRuntime, error) {
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
	return clients.WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: workspaceID, Status: status}, nil
}

func (f *workspaceDeleteFabric) DetachStorageAttachment(_ context.Context, attachmentID, key string) (clients.StorageAttachment, error) {
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

func (f *workspaceDeleteFabric) DestroyStorageVolume(_ context.Context, storageID, key string) (clients.StorageVolume, error) {
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

func (f *workspaceDeleteFabric) DestroyComputeAllocation(_ context.Context, computeID, key string) (clients.ComputeAllocation, error) {
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

type workspaceDeleteNoopDeleteStore struct {
	controlPlaneTableStore
}

func (s workspaceDeleteNoopDeleteStore) ApplyWorkspaceDelete(ctx context.Context, mutation workspaceDeleteStoreMutation) error {
	mutation.DeleteWorkspace = false
	return s.controlPlaneTableStore.ApplyWorkspaceDelete(ctx, mutation)
}

func newWorkspaceDeleteFixture(t *testing.T, store controlPlaneTableStore, fabric *workspaceDeleteFabric) workspaceDeleteFixture {
	t.Helper()
	service := newTestService(fakeLedgerClient{}, fabric)
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
		"runtime:ws-alpha:" + operationID + ":runtime",
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
	if err != nil || decoded.Phase != "complete" || decoded.RuntimeStatus != "destroyed" || decoded.AttachmentStatus != "detached" || decoded.StorageStatus != "destroyed" || decoded.ComputeStatus != "destroyed" {
		t.Fatalf("decoded operation=%#v err=%v", decoded, err)
	}

	replayed := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, key)
	if replayed.Code != http.StatusOK || len(fabric.recordedCalls()) != len(wantCalls) {
		t.Fatalf("replay status=%d body=%s calls=%#v", replayed.Code, replayed.Body.String(), fabric.recordedCalls())
	}
	conflict := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, key+":other")
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), errIdempotencyConflict.Error()) {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestWorkspaceDeletePartialFabricResultStaysManualReviewAndResumes(t *testing.T) {
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
	beforeConflict := len(fabric.recordedCalls())
	conflict := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, key+":other")
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), errIdempotencyConflict.Error()) || len(fabric.recordedCalls()) != beforeConflict {
		t.Fatalf("conflicting retry status=%d body=%s calls=%#v", conflict.Code, conflict.Body.String(), fabric.recordedCalls())
	}

	second := requestWithMutationKeyForTest(t, fixture.server, fixture.session, http.MethodDelete, "/api/workspaces/ws-alpha", `{}`, key)
	if second.Code != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", second.Code, second.Body.String())
	}
	calls := fabric.recordedCalls()
	if len(calls) != 6 || !strings.HasPrefix(calls[2], "storage:") || !strings.HasPrefix(calls[3], "storage:") || calls[2] != calls[3] ||
		!strings.HasPrefix(calls[4], "compute:") || !strings.HasPrefix(calls[5], "compute-read:") {
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
		if len(calls) != 2 || !strings.HasPrefix(calls[0], "runtime:") || !strings.HasPrefix(calls[1], "attachment:") {
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
	claimed := workspaceDeleteOperation{
		OperationID: workspaceDeleteOperationID("ws-delete-store"), RequestHash: "request-delete", AccountID: "acct-delete", OwnerUserID: "usr-delete",
		WorkspaceID: "ws-delete-store", ComputeID: "compute-delete", StorageID: "storage-delete", AttachmentID: "attachment-delete",
		Phase: "claimed", Status: "running", CreatedAt: now,
	}
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
