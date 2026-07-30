package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const testWorkspaceLaunchReadbackRecoveryConfirmation = "RECOVER_UNKNOWN_WORKSPACE_LAUNCH_STAGE_FROM_AUTHORITATIVE_READBACK"

var testWorkspaceLaunchReadbackRecoveryForbiddenWrites = []string{
	"create_launch", "debit", "recharge", "refund", "scale", "create_cvm", "create_second_cbs", "delete", "replace", "retry_unknown_stage_write",
}

type workspaceLaunchReadbackRecoveryFabric struct {
	*monthlyFabric
	operations    []clients.FabricOperation
	operationsErr error
}

func (f *workspaceLaunchReadbackRecoveryFabric) ListOperations(_ context.Context) ([]clients.FabricOperation, error) {
	f.record("fabric.operations")
	return append([]clients.FabricOperation(nil), f.operations...), f.operationsErr
}

func testWorkspaceLaunchReadbackAllowedWrites(stage string) []string {
	remaining := map[string][]string{
		"storage":    {"create_original_pv_pvc_attachment", "upsert_original_gateway_secret", "create_original_workspace_runtime", "activate_original_workspace", "record_original_purchase_receipt"},
		"attachment": {"upsert_original_gateway_secret", "create_original_workspace_runtime", "activate_original_workspace", "record_original_purchase_receipt"},
		"secret":     {"create_original_workspace_runtime", "activate_original_workspace", "record_original_purchase_receipt"},
		"runtime":    {"activate_original_workspace", "record_original_purchase_receipt"},
		"activation": {"record_original_purchase_receipt"},
		"receipt":    {},
	}
	return append([]string{"confirm_original_" + stage + "_from_authoritative_readback"}, remaining[stage]...)
}

func testWorkspaceLaunchReadbackApproval(t *testing.T, operation workspaceLaunchOperation, stage, key string, compute, storage map[string]any) map[string]any {
	t.Helper()
	approval := map[string]any{
		"schemaVersion":        1,
		"approvalId":           "approval-readback-" + stableID(stage)[:8],
		"expiresAt":            time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		"mergedMainSha":        strings.Repeat("a", 40),
		"cloudImageDigest":     "sha256:" + strings.Repeat("b", 64),
		"workspaceImageDigest": operation.WorkspaceImageDigest,
		"confirmation":         testWorkspaceLaunchReadbackRecoveryConfirmation,
		"idempotencyKey":       key,
		"recoveryKey":          "recovery-readback-" + stableID("recovery", stage)[:8],
		"stage":                stage,
		"customer": map[string]any{
			"email": "alpha@example.com", "accountId": operation.AccountID, "ownerUserId": operation.OwnerUserID,
		},
		"target": map[string]any{
			"launchOperationId": operation.ID, "workspaceId": operation.WorkspaceID, "packageId": operation.PackageID,
		},
		"resources": map[string]any{
			"computeAllocationId":       operation.ComputeID,
			"computeProviderResourceId": stringValue(compute["providerResourceId"]),
			"storageVolumeId":           operation.StorageID,
			"storageProviderResourceId": stringValue(storage["providerResourceId"]),
			"attachmentId":              operation.AttachmentID,
			"gatewaySecretRef":          workspaceGatewaySecretReference(operation.WorkspaceID),
			"gatewaySecretFingerprint":  operation.WorkspaceKeyFingerprint,
			"runtimeId":                 operation.RuntimeID,
			"receiptId":                 operation.ReceiptID,
		},
		"operationIds": map[string]any{
			"compute": operation.ID + ":compute", "storage": operation.ID + ":storage",
			"attachment": operation.AttachmentOperationID, "secret": operation.WorkspaceOperationID + ":secret:gateway-secret",
			"runtime": operation.WorkspaceOperationID + ":runtime", "activation": operation.ID + ":activation",
			"receipt": operation.ID + ":purchase-receipt",
		},
		"attemptBudget":   map[string]any{"attempted": 1, "confirmed": 0, "unknown": 1, "max": 1},
		"allowedWrites":   testWorkspaceLaunchReadbackAllowedWrites(stage),
		"forbiddenWrites": append([]string(nil), testWorkspaceLaunchReadbackRecoveryForbiddenWrites...),
	}
	payload, err := json.Marshal(approval)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	approval["approvalDigest"] = hex.EncodeToString(digest[:])
	return approval
}

func requestWorkspaceLaunchReadbackRecovery(t *testing.T, fixture workspaceLaunchWorkerFixture, approval map[string]any, key string) *httptest.ResponseRecorder {
	t.Helper()
	operation := fixture.operation(t)
	approvalJSON, err := json.Marshal(approval)
	if err != nil {
		t.Fatal(err)
	}
	var decodedApproval map[string]any
	var typedApproval workspaceLaunchReadbackRecoveryApproval
	if json.Unmarshal(approvalJSON, &decodedApproval) != nil || jsonRoundTrip(decodedApproval, &typedApproval) != nil {
		t.Fatal("readback approval fixture is not JSON round-trippable")
	}
	if _, ok := workspaceLaunchReadbackRecoveryApprovalFromMap(decodedApproval, key); !ok {
		t.Fatalf("readback approval fixture rejected: digest=%s computed=%s approval=%s", typedApproval.ApprovalDigest, workspaceLaunchReadbackRecoveryApprovalDigest(typedApproval), approvalJSON)
	}
	body, err := json.Marshal(map[string]any{
		"accountId": operation.AccountID, "billingOperationId": operation.ID, "evidenceRef": "case-20260731-readback", "approval": approval,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/operator/workspace-launches/"+operation.ID+"/recover", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("x-opl-compute-claim-capability", "workspace-launch-readback-capability")
	addAuth(req, fixture.operator)
	recorder := httptest.NewRecorder()
	fixture.server.ServeHTTP(recorder, req)
	return recorder
}

func requestWorkspaceLaunchReadbackProof(t *testing.T, fixture workspaceLaunchWorkerFixture) *httptest.ResponseRecorder {
	t.Helper()
	operation := fixture.operation(t)
	req := httptest.NewRequest(http.MethodGet, "/api/operator/workspace-launches/"+operation.ID+"/readback-recovery-proof", nil)
	addAuth(req, fixture.operator)
	recorder := httptest.NewRecorder()
	fixture.server.ServeHTTP(recorder, req)
	return recorder
}

type workspaceLaunchReadbackRecoveryScenario struct {
	fixture             workspaceLaunchWorkerFixture
	unknown             workspaceLaunchOperation
	approvalOperation   workspaceLaunchOperation
	readback            *workspaceLaunchReadbackRecoveryFabric
	beforeCurrentWrites int
}

func newWorkspaceLaunchReadbackRecoveryScenario(t *testing.T, stage, packageID string) workspaceLaunchReadbackRecoveryScenario {
	t.Helper()
	storageGB, charge := 10, int64(52_580_000)
	if packageID == "pro" {
		storageGB, charge = 100, 240_080_000
	}
	fixture := newWorkspaceLaunchWorkerFixtureForPlan(t, []int64{1_000_000_000, 1_000_000_000, 1_000_000_000 - charge}, nil, nil, packageID, storageGB, false)
	configureWorkspaceLaunchFulfillment(t, fixture)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	switch stage {
	case "storage":
		fixture.fabric.storageCreateErr = errors.New("storage response lost after write")
		fixture.fabric.storageSyncErr = errors.New("storage readback unavailable")
	case "attachment":
		fixture.fabric.attachmentErr = errors.New("attachment response lost after write")
	case "secret":
		fixture.fabric.gatewaySecretErr = errors.New("gateway Secret response lost after write")
	case "runtime":
		fixture.fabric.runtimeErr = errors.New("runtime response lost after write")
		fixture.fabric.runtimeStatusErr = errors.New("runtime readback unavailable")
	case "activation":
		fixture.store.activationErr = errors.New("activation response lost after write")
	case "receipt":
		fixture.ledger.receiptErrors = []error{errors.New("receipt response lost after write")}
	default:
		t.Fatalf("unsupported stage %q", stage)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatalf("lost %s response did not enter manual review", stage)
	}
	unknown := fixture.operation(t)
	if unknown.Status != "manual_review" || unknown.Phase != workspaceLaunchReadbackRecoveryPhase(stage) ||
		unknown.ContinuationAttemptBudgets[stage] != (workspaceLaunchStageBudget{Attempted: 1, Unknown: 1, Max: 1}) {
		t.Fatalf("unknown %s state=%#v budget=%#v", stage, unknown, unknown.ContinuationAttemptBudgets[stage])
	}
	beforeCurrentWrites := workspaceLaunchStageWriteCount(fixture, stage)
	fixture.fabric.storageCreateErr, fixture.fabric.storageSyncErr = nil, nil
	fixture.fabric.attachmentErr, fixture.fabric.gatewaySecretErr = nil, nil
	fixture.fabric.runtimeErr, fixture.fabric.runtimeStatusErr = nil, nil
	fixture.store.activationErr = nil
	fixture.ledger.receiptErrors = nil
	fixture.fabric.providerTruth = &clients.MonthlyProviderTruth{
		ComputeState: "ready", StorageState: "ready", Compute: fixture.fabric.computeSync, Storage: fixture.fabric.storageSync,
	}
	approvalOperation := unknown
	readback := &workspaceLaunchReadbackRecoveryFabric{monthlyFabric: fixture.fabric}
	switch stage {
	case "attachment":
		attachment := clients.StorageAttachment{
			ID: "attachment-authoritative", OperationID: unknown.AttachmentOperationID, WorkspaceID: unknown.WorkspaceID,
			ComputeID: unknown.ComputeID, VolumeID: unknown.StorageID, Status: "attached", Provider: "tencent-tke",
		}
		approvalOperation.AttachmentID = attachment.ID
		readback.operations = []clients.FabricOperation{{
			ID: "fop-attachment-readback", Action: "create_storage_attachment", ResourceKind: "storage_attachment", ResourceID: attachment.ID,
			AccountID: unknown.AccountID, WorkspaceID: unknown.WorkspaceID, IdempotencyKey: unknown.AttachmentOperationID, Status: "succeeded",
			RedactedProviderPayload: map[string]any{"resource": attachment},
		}}
	case "secret":
		fingerprint := "sha256:" + strings.Repeat("c", 64)
		approvalOperation.WorkspaceKeyFingerprint = fingerprint
		readback.operations = []clients.FabricOperation{{
			ID: "fop-gateway-readback", Action: "upsert_gateway_secret", ResourceKind: "gateway_secret", ResourceID: workspaceGatewaySecretReference(unknown.WorkspaceID),
			AccountID: unknown.AccountID, WorkspaceID: unknown.WorkspaceID, IdempotencyKey: unknown.WorkspaceOperationID + ":secret:gateway-secret", Status: "succeeded",
			RedactedProviderPayload: map[string]any{"resource": clients.GatewaySecretWriteResult{
				SecretRef: workspaceGatewaySecretReference(unknown.WorkspaceID), Version: "v1", Fingerprint: fingerprint,
			}},
		}}
	case "runtime":
		runtime := clients.WorkspaceRuntime{
			ID: "runtime-authoritative", OperationID: unknown.WorkspaceOperationID + ":runtime", WorkspaceID: unknown.WorkspaceID,
			URL: "https://workspace.medopl.cn/w/" + unknown.WorkspaceID + "/", Status: "running", ServiceName: "opl-compute-authoritative", Ready: true,
			Access: clients.WorkspaceRuntimeAccess{Username: "admin", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-compute-authoritative-env"},
		}
		approvalOperation.RuntimeID = runtime.ID
		fixture.fabric.runtimeStatus = runtime
	case "activation":
		billingState, reviewCode := fixture.app.workspaceLaunchBillingState(context.Background(), unknown)
		if reviewCode != "" {
			t.Fatalf("activation readback billing state=%s", reviewCode)
		}
		workspaceRow := workspaceProjectionRow(workspaceProjectionFromLaunch(unknown))
		for key, value := range billingState {
			workspaceRow[key] = value
		}
		if _, err := fixture.store.memoryTableStore.ActivateWorkspace(context.Background(), workspaceRow); err != nil {
			t.Fatal(err)
		}
	case "receipt":
		input, err := fixture.app.workspaceLaunchPurchaseReceiptReadbackInput(context.Background(), unknown)
		if err != nil {
			t.Fatal(err)
		}
		receipt := clients.Receipt{ReceiptInput: input, ReceiptID: "receipt-authoritative"}
		fixture.ledger.receipts[unknown.ID+":purchase-receipt"] = receipt
		approvalOperation.ReceiptID = receipt.ReceiptID
	}
	return workspaceLaunchReadbackRecoveryScenario{
		fixture: fixture, unknown: unknown, approvalOperation: approvalOperation, readback: readback, beforeCurrentWrites: beforeCurrentWrites,
	}
}

func TestWorkspaceLaunchUnknownStageConvergesFromAuthoritativeReadbackAfterRestartWithoutSecondWrite(t *testing.T) {
	t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "workspace-launch-readback-capability")
	for _, stage := range workspaceLaunchContinuationStages {
		t.Run(stage, func(t *testing.T) {
			scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, stage, "basic")
			fixture := scenario.fixture
			fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
			server, err := NewPersistentServer(fixture.service, fixture.store)
			if err != nil {
				t.Fatal(err)
			}
			fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
			key := "recover-readback-" + stableID(stage)[:8]
			approval := testWorkspaceLaunchReadbackApproval(t, scenario.approvalOperation, stage, key, structToMap(fixture.fabric.computeSync), structToMap(fixture.fabric.storageSync))
			response := requestWorkspaceLaunchReadbackRecovery(t, fixture, approval, key)
			if response.Code != http.StatusOK {
				t.Fatalf("%s readback recovery status=%d body=%s", stage, response.Code, response.Body.String())
			}
			recovered := fixture.operation(t)
			if recovered.Status != "succeeded" || recovered.Phase != "succeeded" || recovered.URL == "" || recovered.ReceiptID == "" ||
				recovered.ContinuationAttemptBudgets[stage] != (workspaceLaunchStageBudget{Attempted: 1, Confirmed: 1, Max: 1}) ||
				recovered.ReadbackRecoveryApproval == nil || recovered.ReadbackRecoveryApproval.Stage != stage {
				t.Fatalf("recovered %s launch=%#v", stage, recovered)
			}
			if workspaceLaunchStageWriteCount(fixture, stage) != scenario.beforeCurrentWrites || len(fixture.sub2API.charges) != 1 || len(fixture.sub2API.refunds) != 0 ||
				len(fixture.fabric.computeIDs) != 1 || len(fixture.fabric.storageIDs) != 1 || countStrings(*fixture.events, "fabric.attachment") != 1 ||
				countStrings(*fixture.events, "fabric.gateway-secret") != 1 || countStrings(*fixture.events, "fabric.runtime") != 1 || fixture.store.activationCalls != 1 || len(fixture.ledger.receiptInputs) != 1 {
				t.Fatalf("%s recovery repeated or crossed writes: events=%#v compute=%d storage=%d activation=%d receipts=%d charges=%d refunds=%d", stage, *fixture.events, len(fixture.fabric.computeIDs), len(fixture.fabric.storageIDs), fixture.store.activationCalls, len(fixture.ledger.receiptInputs), len(fixture.sub2API.charges), len(fixture.sub2API.refunds))
			}
		})
	}
}

func TestWorkspaceLaunchReadbackProofIsAuthoritativeAndReadOnly(t *testing.T) {
	for _, stage := range workspaceLaunchContinuationStages {
		t.Run(stage, func(t *testing.T) {
			scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, stage, "basic")
			fixture := scenario.fixture
			fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
			server, err := NewPersistentServer(fixture.service, fixture.store)
			if err != nil {
				t.Fatal(err)
			}
			fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
			before := fixture.operation(t)
			beforeWrites := workspaceLaunchStageWriteCount(fixture, stage)

			response := requestWorkspaceLaunchReadbackProof(t, fixture)
			if response.Code != http.StatusOK {
				t.Fatalf("%s proof status=%d body=%s", stage, response.Code, response.Body.String())
			}
			var proof workspaceLaunchReadbackRecoveryProof
			if err := json.Unmarshal(response.Body.Bytes(), &proof); err != nil {
				t.Fatal(err)
			}
			expectedResources := workspaceLaunchReadbackRecoveryExpectedResources(
				scenario.approvalOperation,
				structToMap(fixture.fabric.computeSync),
				structToMap(fixture.fabric.storageSync),
			)
			if proof.SchemaVersion != 1 || !proof.Eligible || proof.Reason != "none" || proof.Stage != stage ||
				proof.Customer != (workspaceLaunchReadbackRecoveryCustomer{Email: "alpha@example.com", AccountID: before.AccountID, OwnerUserID: before.OwnerUserID}) ||
				proof.Target != (workspaceLaunchReadbackRecoveryTarget{LaunchOperationID: before.ID, WorkspaceID: before.WorkspaceID, PackageID: before.PackageID}) ||
				proof.Resources != expectedResources || proof.OperationIDs != workspaceLaunchReadbackRecoveryExpectedOperationIDs(before) ||
				proof.WorkspaceImageDigest != before.WorkspaceImageDigest || proof.AttemptBudget != (workspaceLaunchStageBudget{Attempted: 1, Unknown: 1, Max: 1}) ||
				!equalWorkspaceComputeClaimStrings(proof.AllowedWrites, workspaceLaunchReadbackRecoveryAllowedWrites(stage)) ||
				!equalWorkspaceComputeClaimStrings(proof.ForbiddenWrites, workspaceLaunchReadbackRecoveryForbiddenWrites) ||
				proof.Sub2APIMutationCount != 0 || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 {
				t.Fatalf("%s proof=%#v", stage, proof)
			}
			after := fixture.operation(t)
			if after.PersistedResult != before.PersistedResult || workspaceLaunchStageWriteCount(fixture, stage) != beforeWrites {
				t.Fatalf("%s proof mutated launch: before=%#v after=%#v events=%#v", stage, before, after, *fixture.events)
			}
		})
	}
}

func workspaceLaunchStageWriteCount(fixture workspaceLaunchWorkerFixture, stage string) int {
	switch stage {
	case "storage":
		return len(fixture.fabric.storageIDs)
	case "attachment":
		return countStrings(*fixture.events, "fabric.attachment")
	case "secret":
		return countStrings(*fixture.events, "fabric.gateway-secret")
	case "runtime":
		return countStrings(*fixture.events, "fabric.runtime")
	case "activation":
		return fixture.store.activationCalls
	case "receipt":
		return len(fixture.ledger.receiptInputs)
	default:
		return -1
	}
}

func newUnknownSecretReadbackFixture(t *testing.T) (workspaceLaunchWorkerFixture, workspaceLaunchOperation, workspaceLaunchOperation, *workspaceLaunchReadbackRecoveryFabric) {
	t.Helper()
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	configureWorkspaceLaunchFulfillment(t, fixture)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	fixture.fabric.gatewaySecretErr = errors.New("gateway Secret response lost after write")
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("lost Secret response did not enter manual review")
	}
	fixture.fabric.gatewaySecretErr = nil
	fixture.fabric.providerTruth = &clients.MonthlyProviderTruth{
		ComputeState: "ready", StorageState: "ready", Compute: fixture.fabric.computeSync, Storage: fixture.fabric.storageSync,
	}
	unknown := fixture.operation(t)
	approvalOperation := unknown
	fingerprint := "sha256:" + strings.Repeat("c", 64)
	approvalOperation.WorkspaceKeyFingerprint = fingerprint
	readback := &workspaceLaunchReadbackRecoveryFabric{
		monthlyFabric: fixture.fabric,
		operations: []clients.FabricOperation{{
			ID: "fop-gateway-readback", Action: "upsert_gateway_secret", ResourceKind: "gateway_secret", ResourceID: workspaceGatewaySecretReference(unknown.WorkspaceID),
			AccountID: unknown.AccountID, WorkspaceID: unknown.WorkspaceID, IdempotencyKey: unknown.WorkspaceOperationID + ":secret:gateway-secret", Status: "succeeded",
			RedactedProviderPayload: map[string]any{"resource": clients.GatewaySecretWriteResult{
				SecretRef: workspaceGatewaySecretReference(unknown.WorkspaceID), Version: "v1", Fingerprint: fingerprint,
			}},
		}},
	}
	return fixture, unknown, approvalOperation, readback
}

func TestWorkspaceLaunchReadbackRecoveryFailsClosedWithoutUniqueExactAuthority(t *testing.T) {
	t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "workspace-launch-readback-capability")
	for _, test := range []struct {
		name   string
		mutate func(*workspaceLaunchReadbackRecoveryFabric)
	}{
		{name: "missing", mutate: func(f *workspaceLaunchReadbackRecoveryFabric) { f.operations = nil }},
		{name: "multiple", mutate: func(f *workspaceLaunchReadbackRecoveryFabric) { f.operations = append(f.operations, f.operations[0]) }},
		{name: "identity drift", mutate: func(f *workspaceLaunchReadbackRecoveryFabric) { f.operations[0].WorkspaceID = "ws-other" }},
		{name: "operation read error", mutate: func(f *workspaceLaunchReadbackRecoveryFabric) {
			f.operationsErr = errors.New("Fabric operation read unavailable")
		}},
		{name: "provider truth read error", mutate: func(f *workspaceLaunchReadbackRecoveryFabric) {
			f.providerTruthErr = errors.New("provider truth unavailable")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, unknown, approvalOperation, readback := newUnknownSecretReadbackFixture(t)
			test.mutate(readback)
			fixture.service = controlplane.NewService(fixture.ledger, readback, fixture.sub2API)
			server, err := NewPersistentServer(fixture.service, fixture.store)
			if err != nil {
				t.Fatal(err)
			}
			fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
			key := "recover-negative-" + stableID(test.name)[:8]
			approval := testWorkspaceLaunchReadbackApproval(t, approvalOperation, "secret", key, structToMap(fixture.fabric.computeSync), structToMap(fixture.fabric.storageSync))
			beforeWrites := workspaceLaunchStageWriteCount(fixture, "secret")
			response := requestWorkspaceLaunchReadbackRecovery(t, fixture, approval, key)
			if response.Code != http.StatusOK {
				t.Fatalf("fail-closed recovery status=%d body=%s", response.Code, response.Body.String())
			}
			current := fixture.operation(t)
			if current.Status != "manual_review" || current.Phase != unknown.Phase || current.ErrorCode != "workspace_launch_secret_readback_unconfirmed" ||
				current.ContinuationAttemptBudgets["secret"] != (workspaceLaunchStageBudget{Attempted: 1, Unknown: 1, Max: 1}) ||
				workspaceLaunchStageWriteCount(fixture, "secret") != beforeWrites || countStrings(*fixture.events, "fabric.runtime") != 0 || fixture.store.activationCalls != 0 || len(fixture.ledger.receiptInputs) != 0 {
				t.Fatalf("fail-closed %s crossed recovery gate: operation=%#v events=%#v", test.name, current, *fixture.events)
			}
		})
	}
}

type workspaceLaunchReadbackCASBarrierStore struct {
	*recordingWorkspaceLaunchStore
	mu       sync.Mutex
	arrivals int
	release  chan struct{}
}

func (s *workspaceLaunchReadbackCASBarrierStore) PersistWorkspaceLaunch(ctx context.Context, update workspaceLaunchPersistCAS) error {
	desired, err := decodeWorkspaceLaunchOperation(update.DesiredOperation)
	if err == nil && desired.ReadbackRecoveryApproval != nil && desired.Phase == workspaceLaunchReadbackRecoveryPhase(desired.ReadbackRecoveryApproval.Stage) &&
		desired.ContinuationAttemptBudgets[desired.ReadbackRecoveryApproval.Stage] == (workspaceLaunchStageBudget{Attempted: 1, Confirmed: 1, Max: 1}) {
		s.mu.Lock()
		s.arrivals++
		if s.arrivals == 2 {
			close(s.release)
		}
		s.mu.Unlock()
		<-s.release
	}
	return s.recordingWorkspaceLaunchStore.PersistWorkspaceLaunch(ctx, update)
}

func TestWorkspaceLaunchConcurrentReadbackRecoveryHasOneCASWinner(t *testing.T) {
	fixture, _, approvalOperation, readback := newUnknownSecretReadbackFixture(t)
	service := controlplane.NewService(fixture.ledger, readback, fixture.sub2API)
	store := &workspaceLaunchReadbackCASBarrierStore{recordingWorkspaceLaunchStore: fixture.store, release: make(chan struct{})}
	first, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	key := "recover-concurrent-readback"
	rawApproval := testWorkspaceLaunchReadbackApproval(t, approvalOperation, "secret", key, structToMap(fixture.fabric.computeSync), structToMap(fixture.fabric.storageSync))
	encoded, err := json.Marshal(rawApproval)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if json.Unmarshal(encoded, &decoded) != nil {
		t.Fatal("approval decode failed")
	}
	approval, ok := workspaceLaunchReadbackRecoveryApprovalFromMap(decoded, key)
	if !ok {
		t.Fatal("approval fixture rejected")
	}
	input := billingReviewResolutionInput{
		ResourceType: "workspace_launch", ResourceID: approvalOperation.ID, AccountID: approvalOperation.AccountID,
		BillingOperationID: approvalOperation.ID, EvidenceRef: "case-20260731-readback", IdempotencyKey: key, Reviewer: "usr-admin", ReadbackApproval: &approval,
	}
	errorsByCaller := make(chan error, 2)
	for _, app := range []*controlPlaneServer{first, second} {
		go func(app *controlPlaneServer) {
			_, err := app.recoverWorkspaceLaunchReview(context.Background(), service, input)
			errorsByCaller <- err
		}(app)
	}
	for range 2 {
		if err := <-errorsByCaller; err != nil {
			t.Fatal(err)
		}
	}
	store.mu.Lock()
	arrivals := store.arrivals
	store.mu.Unlock()
	recovered := fixture.operation(t)
	if arrivals != 2 || recovered.Status != "succeeded" || recovered.URL == "" || recovered.ReceiptID == "" ||
		workspaceLaunchStageWriteCount(fixture, "secret") != 1 || countStrings(*fixture.events, "fabric.runtime") != 1 || fixture.store.activationCalls != 1 || len(fixture.ledger.receiptInputs) != 1 {
		t.Fatalf("concurrent recovery arrivals=%d operation=%#v events=%#v", arrivals, recovered, *fixture.events)
	}
}

type workspaceLaunchReadbackPostgresStore struct {
	*postgresEntStateStore
	activationCalls int
}

func (s *workspaceLaunchReadbackPostgresStore) ActivateWorkspace(ctx context.Context, row map[string]any) (map[string]any, error) {
	s.activationCalls++
	return s.postgresEntStateStore.ActivateWorkspace(ctx, row)
}

func parsedWorkspaceLaunchReadbackApproval(t *testing.T, raw map[string]any, key string) workspaceLaunchReadbackRecoveryApproval {
	t.Helper()
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	approval, ok := workspaceLaunchReadbackRecoveryApprovalFromMap(decoded, key)
	if !ok {
		t.Fatal("readback approval fixture rejected")
	}
	return approval
}

func TestPostgresWorkspaceLaunchUnknownStagesConvergeAfterStoreReopen(t *testing.T) {
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	admin := openControlPlaneTestPostgres(t)
	t.Cleanup(func() { _ = admin.Close() })

	for _, stage := range workspaceLaunchContinuationStages {
		t.Run(stage, func(t *testing.T) {
			scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, stage, "basic")
			schema := fmt.Sprintf("control_plane_readback_%s_%d", stage, time.Now().UnixNano())
			if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })
			databaseURL := controlPlaneTestPostgresURL(t, "postgres", schema)
			state, err := newTestPostgresEntStateStore(databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			first := state.(*postgresEntStateStore)
			seedTenantMember(t, first, scenario.unknown.AccountID, "org-alpha", scenario.unknown.OwnerUserID, "alpha@example.com")
			if err := first.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(scenario.unknown)); err != nil {
				t.Fatal(err)
			}
			workspaces, err := scenario.fixture.store.ListWorkspaces(context.Background(), scenario.unknown.AccountID)
			if err != nil {
				t.Fatal(err)
			}
			for _, workspace := range workspaces {
				if err := first.SaveWorkspace(context.Background(), workspace); err != nil {
					t.Fatal(err)
				}
			}
			attachments, err := scenario.fixture.store.ListAttachments(context.Background(), scenario.unknown.AccountID)
			if err != nil {
				t.Fatal(err)
			}
			for _, attachment := range attachments {
				if err := first.SaveAttachment(context.Background(), attachment); err != nil {
					t.Fatal(err)
				}
			}
			if err := first.client.Close(); err != nil {
				t.Fatal(err)
			}

			reopenedState, err := newTestPostgresEntStateStore(databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			reopened := &workspaceLaunchReadbackPostgresStore{postgresEntStateStore: reopenedState.(*postgresEntStateStore)}
			t.Cleanup(func() { _ = reopened.client.Close() })
			app, err := newControlPlaneAppWithStore(reopened)
			if err != nil {
				t.Fatal(err)
			}
			service := controlplane.NewService(scenario.fixture.ledger, scenario.readback, scenario.fixture.sub2API)
			key := "recover-postgres-" + stableID(stage)[:8]
			approval := parsedWorkspaceLaunchReadbackApproval(t, testWorkspaceLaunchReadbackApproval(
				t, scenario.approvalOperation, stage, key, structToMap(scenario.fixture.fabric.computeSync), structToMap(scenario.fixture.fabric.storageSync),
			), key)
			input := billingReviewResolutionInput{
				ResourceType: "workspace_launch", ResourceID: scenario.unknown.ID, AccountID: scenario.unknown.AccountID,
				BillingOperationID: scenario.unknown.ID, EvidenceRef: "case-20260731-readback", IdempotencyKey: key, Reviewer: "usr-admin", ReadbackApproval: &approval,
			}
			if _, err := app.recoverWorkspaceLaunchReview(context.Background(), service, input); err != nil {
				t.Fatal(err)
			}
			row, found, err := reopened.GetRuntimeOperation(context.Background(), scenario.unknown.ID)
			if err != nil || !found {
				t.Fatalf("reopened launch found=%t err=%v", found, err)
			}
			recovered, err := decodeWorkspaceLaunchOperation(row)
			if err != nil {
				t.Fatal(err)
			}
			expectedActivationWrites := 1
			if stage == "activation" || stage == "receipt" {
				expectedActivationWrites = 0
			}
			if recovered.Status != "succeeded" || recovered.Phase != "succeeded" || recovered.URL == "" || recovered.ReceiptID == "" ||
				recovered.ContinuationAttemptBudgets[stage] != (workspaceLaunchStageBudget{Attempted: 1, Confirmed: 1, Max: 1}) ||
				workspaceLaunchStageWriteCount(scenario.fixture, stage) != scenario.beforeCurrentWrites || reopened.activationCalls != expectedActivationWrites ||
				len(scenario.fixture.fabric.storageIDs) != 1 || countStrings(*scenario.fixture.events, "fabric.attachment") != 1 ||
				countStrings(*scenario.fixture.events, "fabric.gateway-secret") != 1 || countStrings(*scenario.fixture.events, "fabric.runtime") != 1 ||
				len(scenario.fixture.ledger.receiptInputs) != 1 || len(scenario.fixture.sub2API.charges) != 1 || len(scenario.fixture.sub2API.refunds) != 0 {
				t.Fatalf("PostgreSQL %s recovery crossed bounds: operation=%#v events=%#v activation=%d", stage, recovered, *scenario.fixture.events, reopened.activationCalls)
			}
		})
	}
}
