package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/ent/productione2erecord"
	"opl-cloud/services/control-plane/internal/clients"
)

func recoveredWorkspaceE2EAttemptFixture() productionE2EAttemptClaim {
	return productionE2EAttemptClaim{
		ID:          "production-e2e-recovered-fixture",
		AccountID:   "acct-recovered-e2e",
		WorkspaceID: "ws-recovered-e2e",
		URL:         "https://workspace.medopl.cn/w/ws-recovered-e2e/",
		Binding:     `{"approvalDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expectedModel":"claude-sonnet-4-20250514","modelRequestKey":"recovered-workspace-model-fixture","recoveryKey":"compute-claim-recovery-fixture"}`,
	}
}

func TestRecoveredWorkspaceE2EAttemptReservationIsSingleUse(t *testing.T) {
	for _, test := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "postgres", new: newPostgresWorkspaceRenewalStore},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := test.new(t)
			claim := recoveredWorkspaceE2EAttemptFixture()
			if _, err := store.ReserveProductionE2EAttempt(context.Background(), claim); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReserveProductionE2EAttempt(context.Background(), claim); !errors.Is(err, errProductionE2EAttemptAlreadyExists) {
				t.Fatalf("second reserve error=%v, want %v", err, errProductionE2EAttemptAlreadyExists)
			}
			record, found, err := store.GetProductionE2EAttempt(context.Background(), claim.ID)
			if err != nil || !found || stringValue(record["status"]) != "attempted" || stringValue(record["result"]) != claim.Binding ||
				stringValue(record["reason"]) != recoveredWorkspaceE2EAttemptReason {
				t.Fatalf("attempt record found=%t err=%v record=%#v", found, err, record)
			}
		})
	}
}

func TestPostgresRecoveredWorkspaceE2EAttemptSurvivesStoreReopenAndRetention(t *testing.T) {
	admin := openControlPlaneTestPostgres(t)
	schema := fmt.Sprintf("control_plane_recovered_workspace_e2e_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	databaseURL := controlPlaneTestPostgresURL(t, "postgres", schema)

	stateStore, err := newTestPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	first := stateStore.(*postgresEntStateStore)
	claim := recoveredWorkspaceE2EAttemptFixture()
	if _, err := first.ReserveProductionE2EAttempt(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -365)
	if err := first.client.ProductionE2ERecord.UpdateOneID(claim.ID).SetCreatedAt(old).SetUpdatedAt(old).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.client.ProductionE2ERecord.Create().
		SetID("production-e2e-expiring-fixture").
		SetAccountID(claim.AccountID).
		SetWorkspaceID(claim.WorkspaceID).
		SetStatus("failed").
		SetResult("{}").
		SetReason("legacy_verification").
		SetURL(claim.URL).
		SetCreatedAt(old).
		SetUpdatedAt(old).
		Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := first.ApplyRetention(context.Background(), retentionPolicy{ProductionE2EDays: 90}); err != nil {
		t.Fatal(err)
	}
	if exists, err := first.client.ProductionE2ERecord.Query().Where(productione2erecord.IDEQ(claim.ID)).Exist(context.Background()); err != nil || !exists {
		t.Fatalf("recovered marker retained=%t err=%v", exists, err)
	}
	if exists, err := first.client.ProductionE2ERecord.Query().Where(productione2erecord.IDEQ("production-e2e-expiring-fixture")).Exist(context.Background()); err != nil || exists {
		t.Fatalf("legacy marker retained=%t err=%v", exists, err)
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
	if _, err := restarted.ReserveProductionE2EAttempt(context.Background(), claim); !errors.Is(err, errProductionE2EAttemptAlreadyExists) {
		t.Fatalf("reserve after reopen error=%v, want %v", err, errProductionE2EAttemptAlreadyExists)
	}
}

func TestRecoveredWorkspaceE2EAttemptCompletionRequiresSameBinding(t *testing.T) {
	for _, test := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "postgres", new: newPostgresWorkspaceRenewalStore},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := test.new(t)
			claim := recoveredWorkspaceE2EAttemptFixture()
			if _, err := store.ReserveProductionE2EAttempt(context.Background(), claim); err != nil {
				t.Fatal(err)
			}
			completed, err := store.CompleteProductionE2EAttempt(context.Background(), claim.ID, claim.Binding)
			if err != nil || stringValue(completed["status"]) != "passed" || stringValue(completed["result"]) != claim.Binding {
				t.Fatalf("complete err=%v record=%#v", err, completed)
			}
			if replay, err := store.CompleteProductionE2EAttempt(context.Background(), claim.ID, claim.Binding); err != nil || stringValue(replay["status"]) != "passed" {
				t.Fatalf("completion replay err=%v record=%#v", err, replay)
			}
			if _, err := store.CompleteProductionE2EAttempt(context.Background(), claim.ID, `{"approvalDigest":"drift"}`); !errors.Is(err, errProductionE2EAttemptBindingMismatch) {
				t.Fatalf("binding drift error=%v, want %v", err, errProductionE2EAttemptBindingMismatch)
			}
			if _, err := store.CompleteProductionE2EAttempt(context.Background(), "production-e2e-missing", claim.Binding); !errors.Is(err, errProductionE2EAttemptNotFound) {
				t.Fatalf("missing completion error=%v, want %v", err, errProductionE2EAttemptNotFound)
			}
		})
	}
}

type recoveredWorkspaceE2EHTTPFixture struct {
	server    http.Handler
	store     *memoryTableStore
	session   *httptest.ResponseRecorder
	fabric    *monthlyFabric
	operation workspaceLaunchOperation
	request   recoveredWorkspaceE2EAttemptRequest
}

func newRecoveredWorkspaceE2EHTTPFixture(t *testing.T) recoveredWorkspaceE2EHTTPFixture {
	t.Helper()
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_WORKSPACE_LAUNCH_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	events := []string{}
	fabric := &monthlyFabric{fakeFabricClient: fakeFabricClient{calls: &events}, events: &events}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, fabric), store)
	if err != nil {
		t.Fatal(err)
	}
	operation := newWorkspaceLaunchOperation("acct-alpha", "usr-alpha", "Recovered", "basic", 10, false, pilotPriceVersion, 52_580_000, "recovered-e2e")
	operation.ID, operation.WorkspaceID = "workspace-launch-recovered-e2e", "ws-recovered-e2e"
	operation.Status, operation.Phase = "succeeded", "succeeded"
	operation.ComputeID, operation.StorageID = "compute-recovered-e2e", "storage-recovered-e2e"
	operation.AttachmentOperationID, operation.WorkspaceOperationID = operation.ID+":attachment", operation.ID+":workspace"
	operation.ComputePoolID, operation.ComputeNodePoolID = "pool-basic", "np-basic"
	operation.ComputeMachineName, operation.ComputeNodeName = "machine-recovered-e2e", "node-recovered-e2e"
	operation.ComputeCVMInstanceID, operation.ComputePrivateIP = "ins-recovered-e2e", "10.20.30.40"
	operation.ComputeInstanceType, operation.ComputeZone = "S5.MEDIUM4", "ap-shanghai-2"
	operation.ComputeChargeType, operation.ComputeRenewFlag, operation.ComputeDeadline = "PREPAID", "NOTIFY_AND_MANUAL_RENEW", "2099-08-28T00:00:00Z"
	operation.WorkspaceImageDigest = "sha256:" + strings.Repeat("c", 64)
	operation.WorkspaceAPIKeyID, operation.WorkspaceKeyFingerprint = 42, "sha256:"+strings.Repeat("d", 64)
	expectedResources := workspaceComputeClaimExpectedResources(operation, "storage_not_started", "")
	operation.AttachmentID, operation.GatewaySecretRef = expectedResources.AttachmentID, expectedResources.GatewaySecretRef
	operation.RuntimeID, operation.RuntimeServiceName = expectedResources.RuntimeID, "workspace-service-recovered-e2e"
	operation.RuntimeReady = true
	operation.URL = "https://workspace.medopl.cn/w/" + operation.WorkspaceID + "/"
	operation.ReceiptID = "receipt-recovered-e2e"
	planDigest := strings.Repeat("f", 64)
	operation.RecoveryPlan = &workspaceRecoveryPlan{
		PlanID: "recovery-plan-" + planDigest[:20], PlanDigest: planDigest, Status: "completed",
		OperationID: operation.ID, URL: operation.URL, ReceiptID: operation.ReceiptID,
	}
	operation.RecoveryExecution = &workspaceRecoveryExecution{
		ExecutionID: "recovery-exec-plan-authority", RunIdentity: "control-plane-run-plan-authority",
		PlanID: operation.RecoveryPlan.PlanID, PlanDigest: planDigest, ApprovalDigest: strings.Repeat("a", 64),
		Decision: "continue", Status: "completed", StartedAt: "2026-08-28T00:00:00Z", CompletedAt: "2026-08-28T00:01:00Z",
	}
	mustStore(t, store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{
		"id": operation.ComputeID, "accountId": operation.AccountID, "workspaceId": operation.WorkspaceID, "status": "running",
	}))
	mustStore(t, store.SaveStorage(context.Background(), map[string]any{
		"id": operation.StorageID, "accountId": operation.AccountID, "workspaceId": operation.WorkspaceID, "status": "available",
	}))
	mustStore(t, store.SaveAttachment(context.Background(), map[string]any{
		"id": operation.AttachmentID, "accountId": operation.AccountID, "workspaceId": operation.WorkspaceID,
		"computeAllocationId": operation.ComputeID, "storageId": operation.StorageID, "status": "attached",
	}))
	mustStore(t, store.SaveWorkspace(context.Background(), workspaceGatewayTestRow(map[string]any{
		"id": operation.WorkspaceID, "accountId": operation.AccountID, "ownerAccountId": operation.AccountID, "ownerUserId": operation.OwnerUserID,
		"name": operation.Name, "state": "running", "status": "running",
		"computeAllocationId": operation.ComputeID, "currentComputeAllocationId": operation.ComputeID, "storageId": operation.StorageID,
		"attachmentId": operation.AttachmentID, "currentAttachmentId": operation.AttachmentID,
		"runtimeId": operation.RuntimeID, "runtimeServiceName": operation.RuntimeServiceName, "url": operation.URL,
		"workspaceApiKeyId": operation.WorkspaceAPIKeyID, "purchaseReceiptId": operation.ReceiptID,
	})))
	request := recoveredWorkspaceE2EAttemptRequest{
		SchemaVersion: 2, ApprovalID: "approval-recovered-e2e", LaunchOperationID: operation.ID,
		PlanID: operation.RecoveryPlan.PlanID, PlanDigest: planDigest, Decision: "continue",
		Confirmation: recoveredWorkspaceE2EConfirmation, ExpectedModel: "claude-sonnet-4-20250514",
		ModelRequestKey: "recovered-workspace-model-e2e",
	}
	return recoveredWorkspaceE2EHTTPFixture{
		server: server, store: store, session: loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!"), fabric: fabric,
		operation: operation, request: request,
	}
}

func (f recoveredWorkspaceE2EHTTPFixture) post(t *testing.T, suffix string, request recoveredWorkspaceE2EAttemptRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return requestWithSession(t, f.server, f.session, http.MethodPost, "/api/workspaces/"+f.operation.WorkspaceID+suffix, string(body))
}

func TestRecoveredWorkspaceE2EAttemptUsesPersistedRecoveryPlanAuthority(t *testing.T) {
	fixture := newRecoveredWorkspaceE2EHTTPFixture(t)
	row, found, err := fixture.store.GetRuntimeOperation(context.Background(), fixture.operation.ID)
	if err != nil || !found {
		t.Fatalf("launch found=%t err=%v", found, err)
	}
	operation, err := decodeWorkspaceLaunchOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	planDigest := strings.Repeat("f", 64)
	operation.ComputeClaimApproval = nil
	operation.RecoveryPlan = &workspaceRecoveryPlan{
		PlanID: "recovery-plan-" + planDigest[:20], PlanDigest: planDigest, Status: "completed",
		OperationID: operation.ID, URL: operation.URL, ReceiptID: operation.ReceiptID,
	}
	operation.RecoveryExecution = &workspaceRecoveryExecution{
		ExecutionID: "recovery-exec-plan-authority", RunIdentity: "control-plane-run-plan-authority",
		PlanID: operation.RecoveryPlan.PlanID, PlanDigest: planDigest, ApprovalDigest: strings.Repeat("a", 64),
		Decision: "continue", Status: "completed", StartedAt: "2026-08-28T00:00:00Z", CompletedAt: "2026-08-28T00:01:00Z",
	}
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))

	body, err := json.Marshal(map[string]any{
		"schemaVersion":     2,
		"approvalId":        "approval-recovered-e2e",
		"launchOperationId": operation.ID,
		"planId":            operation.RecoveryPlan.PlanID,
		"planDigest":        operation.RecoveryPlan.PlanDigest,
		"decision":          "continue",
		"confirmation":      recoveredWorkspaceE2EConfirmation,
		"expectedModel":     "claude-sonnet-4-20250514",
		"modelRequestKey":   "recovered-workspace-model-e2e",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := requestWithSession(t, fixture.server, fixture.session, http.MethodPost,
		"/api/workspaces/"+operation.WorkspaceID+"/recovered-e2e-attempt", string(body))
	if response.Code != http.StatusCreated {
		t.Fatalf("reserve status=%d body=%s", response.Code, response.Body.String())
	}

	record, found, err := fixture.store.GetProductionE2EAttempt(context.Background(), recoveredWorkspaceE2EAttemptID(operation))
	if err != nil || !found {
		t.Fatalf("marker found=%t err=%v", found, err)
	}
	for _, forbidden := range []string{"resources", "cloudImageDigest", "workspaceImageDigest", "computeAllocationId", "storageId"} {
		if strings.Contains(stringValue(record["result"]), forbidden) {
			t.Fatalf("marker binding contains caller-supplied resource field %q: %s", forbidden, stringValue(record["result"]))
		}
	}
}

func TestRecoveredWorkspaceE2EAttemptBindsPersistedPlanDigest(t *testing.T) {
	t.Run("exact plan reference is persisted and completed with the same binding", func(t *testing.T) {
		fixture := newRecoveredWorkspaceE2EHTTPFixture(t)
		reserve := fixture.post(t, "/recovered-e2e-attempt", fixture.request)
		if reserve.Code != http.StatusCreated {
			t.Fatalf("reserve status=%d body=%s", reserve.Code, reserve.Body.String())
		}
		var reserved map[string]any
		if err := json.NewDecoder(reserve.Body).Decode(&reserved); err != nil {
			t.Fatal(err)
		}
		record, found, err := fixture.store.GetProductionE2EAttempt(context.Background(), stringValue(reserved["attemptId"]))
		if err != nil || !found {
			t.Fatalf("marker found=%t err=%v", found, err)
		}
		var binding map[string]any
		if err := json.Unmarshal([]byte(stringValue(record["result"])), &binding); err != nil {
			t.Fatal(err)
		}
		boundRequest, ok := binding["request"].(map[string]any)
		if !ok || boundRequest["planId"] != fixture.request.PlanID || boundRequest["planDigest"] != fixture.request.PlanDigest ||
			binding["requestDigest"] != reserved["approvalDigest"] {
			t.Fatalf("marker binding=%#v reserve=%#v", binding, reserved)
		}
		if reserved["executionId"] != fixture.operation.RecoveryExecution.ExecutionID || reserved["runId"] != fixture.operation.RecoveryExecution.RunIdentity {
			t.Fatalf("reserve does not expose persisted execution identity: %#v", reserved)
		}

		complete := fixture.post(t, "/recovered-e2e-attempt/complete", fixture.request)
		if complete.Code != http.StatusOK || !strings.Contains(complete.Body.String(), `"status":"passed"`) {
			t.Fatalf("complete status=%d body=%s", complete.Code, complete.Body.String())
		}
	})

	t.Run("legacy caller-supplied resource approval fails closed", func(t *testing.T) {
		fixture := newRecoveredWorkspaceE2EHTTPFixture(t)
		body := `{"approval":{"schemaVersion":1,"resources":{"computeAllocationId":"caller-value"}},"approvalDigest":"` + strings.Repeat("a", 64) + `"}`
		response := requestWithSession(t, fixture.server, fixture.session, http.MethodPost,
			"/api/workspaces/"+fixture.operation.WorkspaceID+"/recovered-e2e-attempt", body)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "recovered_workspace_e2e_approval_invalid") {
			t.Fatalf("legacy approval status=%d body=%s", response.Code, response.Body.String())
		}
		assertNoRecoveredWorkspaceE2EMarker(t, fixture.store)
	})

	t.Run("completion rejects plan digest drift", func(t *testing.T) {
		fixture := newRecoveredWorkspaceE2EHTTPFixture(t)
		reserve := fixture.post(t, "/recovered-e2e-attempt", fixture.request)
		if reserve.Code != http.StatusCreated {
			t.Fatalf("reserve status=%d body=%s", reserve.Code, reserve.Body.String())
		}
		drifted := fixture.request
		drifted.PlanDigest = strings.Repeat("e", 64)
		complete := fixture.post(t, "/recovered-e2e-attempt/complete", drifted)
		if complete.Code != http.StatusConflict || !strings.Contains(complete.Body.String(), "model_result_unknown") {
			t.Fatalf("drifted completion status=%d body=%s", complete.Code, complete.Body.String())
		}
	})
}

func TestRecoveredWorkspaceE2EAttemptAPIIsOwnerScopedSingleUse(t *testing.T) {
	fixture := newRecoveredWorkspaceE2EHTTPFixture(t)
	reserve := fixture.post(t, "/recovered-e2e-attempt", fixture.request)
	if reserve.Code != http.StatusCreated {
		t.Fatalf("reserve status=%d body=%s", reserve.Code, reserve.Body.String())
	}
	var reserved map[string]any
	if err := json.NewDecoder(reserve.Body).Decode(&reserved); err != nil {
		t.Fatal(err)
	}
	attemptID := stringValue(reserved["attemptId"])
	record, found, err := fixture.store.GetProductionE2EAttempt(context.Background(), attemptID)
	if err != nil || !found || stringValue(record["status"]) != "attempted" || stringValue(record["accountId"]) != fixture.operation.AccountID ||
		stringValue(record["workspaceId"]) != fixture.operation.WorkspaceID || stringValue(record["reason"]) != recoveredWorkspaceE2EAttemptReason {
		t.Fatalf("reserved marker found=%t err=%v record=%#v", found, err, record)
	}
	second := fixture.post(t, "/recovered-e2e-attempt", fixture.request)
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "model_result_unknown") {
		t.Fatalf("second reserve status=%d body=%s", second.Code, second.Body.String())
	}
	complete := fixture.post(t, "/recovered-e2e-attempt/complete", fixture.request)
	if complete.Code != http.StatusOK || !strings.Contains(complete.Body.String(), `"status":"passed"`) {
		t.Fatalf("complete status=%d body=%s", complete.Code, complete.Body.String())
	}
	workspace, _, _ := fixture.store.GetWorkspace(context.Background(), fixture.operation.WorkspaceID)
	launch, _, _ := fixture.store.GetRuntimeOperation(context.Background(), fixture.operation.ID)
	if stringValue(workspace["state"]) != "running" || stringValue(launch["status"]) != "succeeded" {
		t.Fatalf("E2E marker changed resource state workspace=%#v launch=%#v", workspace, launch)
	}
}

func TestRecoveredWorkspaceE2EAttemptCompletionUsesReservedBindingAfterResourceDrift(t *testing.T) {
	fixture := newRecoveredWorkspaceE2EHTTPFixture(t)
	reserve := fixture.post(t, "/recovered-e2e-attempt", fixture.request)
	if reserve.Code != http.StatusCreated {
		t.Fatalf("reserve status=%d body=%s", reserve.Code, reserve.Body.String())
	}
	row, found, err := fixture.store.GetRuntimeOperation(context.Background(), fixture.operation.ID)
	if err != nil || !found {
		t.Fatalf("launch found=%t err=%v", found, err)
	}
	operation, err := decodeWorkspaceLaunchOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	operation.Status, operation.Phase = "manual_review", "receipt_pending"
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))

	complete := fixture.post(t, "/recovered-e2e-attempt/complete", fixture.request)
	if complete.Code != http.StatusOK || !strings.Contains(complete.Body.String(), `"status":"passed"`) {
		t.Fatalf("complete status=%d body=%s", complete.Code, complete.Body.String())
	}
}

func TestRecoveredWorkspaceE2EAttemptAPIRejectsBeforeMarkerWrite(t *testing.T) {
	t.Run("resource closure incomplete", func(t *testing.T) {
		fixture := newRecoveredWorkspaceE2EHTTPFixture(t)
		row, _, _ := fixture.store.GetRuntimeOperation(context.Background(), fixture.operation.ID)
		operation, err := decodeWorkspaceLaunchOperation(row)
		if err != nil {
			t.Fatal(err)
		}
		operation.Status, operation.Phase = "waiting", "receipt_pending"
		mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
		response := fixture.post(t, "/recovered-e2e-attempt", fixture.request)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "recovered_workspace_e2e_resource_closure_required") {
			t.Fatalf("incomplete closure status=%d body=%s", response.Code, response.Body.String())
		}
		assertNoRecoveredWorkspaceE2EMarker(t, fixture.store)
	})

	t.Run("approval binding drift", func(t *testing.T) {
		fixture := newRecoveredWorkspaceE2EHTTPFixture(t)
		request := fixture.request
		request.PlanDigest = strings.Repeat("e", 64)
		response := fixture.post(t, "/recovered-e2e-attempt", request)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "recovered_workspace_e2e_binding_mismatch") {
			t.Fatalf("binding drift status=%d body=%s", response.Code, response.Body.String())
		}
		assertNoRecoveredWorkspaceE2EMarker(t, fixture.store)
	})

	t.Run("fresh Fabric truth unavailable", func(t *testing.T) {
		fixture := newRecoveredWorkspaceE2EHTTPFixture(t)
		fixture.fabric.activationTruth = &clients.WorkspaceActivationTruth{
			SchemaVersion: 1, Ready: false, Reason: "provider_unavailable", ErrorClass: "timeout",
			ComputeState: "unknown", StorageState: "unknown", Checks: []any{},
		}
		fixture.fabric.activationTruthErr = errors.New("workspace activation truth unavailable")
		response := fixture.post(t, "/recovered-e2e-attempt", fixture.request)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "recovered_workspace_e2e_resource_closure_required") ||
			len(fixture.fabric.activationTruthInputs) != 1 {
			t.Fatalf("stale truth status=%d body=%s truthInputs=%#v", response.Code, response.Body.String(), fixture.fabric.activationTruthInputs)
		}
		assertNoRecoveredWorkspaceE2EMarker(t, fixture.store)
	})

	t.Run("other account", func(t *testing.T) {
		fixture := newRecoveredWorkspaceE2EHTTPFixture(t)
		seedTenantMember(t, fixture.store, "acct-beta", "org-beta", "usr-beta", "beta@example.com")
		fixture.session = loginForTest(t, fixture.server, "beta@example.com", "CorrectHorseBatteryStaple!")
		response := fixture.post(t, "/recovered-e2e-attempt", fixture.request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("cross-account status=%d body=%s", response.Code, response.Body.String())
		}
		assertNoRecoveredWorkspaceE2EMarker(t, fixture.store)
	})
}

func assertNoRecoveredWorkspaceE2EMarker(t *testing.T, store *memoryTableStore) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.productionE2E) != 0 {
		t.Fatalf("unexpected recovered E2E marker=%#v", store.productionE2E)
	}
}
