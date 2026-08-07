package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
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
	mu                 sync.Mutex
	operations         []clients.FabricOperation
	operationsErr      error
	ownership          clients.MachineOwnership
	ownershipErr       error
	stageProofCalls    int
	stageConvergeCalls int
}

func (f *workspaceLaunchReadbackRecoveryFabric) MonthlyProviderTruth(ctx context.Context, computeID, storageID string) (clients.MonthlyProviderTruth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.monthlyFabric.MonthlyProviderTruth(ctx, computeID, storageID)
}

func (f *workspaceLaunchReadbackRecoveryFabric) ListOperations(_ context.Context) ([]clients.FabricOperation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("fabric.operations")
	return append([]clients.FabricOperation(nil), f.operations...), f.operationsErr
}

func (f *workspaceLaunchReadbackRecoveryFabric) MachineOwnership(_ context.Context, resourceID string) (clients.MachineOwnership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("fabric.machine-ownership")
	if f.ownership.ResourceID != resourceID {
		return clients.MachineOwnership{}, errors.New("machine ownership not found")
	}
	return f.ownership, f.ownershipErr
}

func (f *workspaceLaunchReadbackRecoveryFabric) WorkspaceLaunchStageReadbackProof(_ context.Context, input clients.WorkspaceLaunchStageReadbackInput) (clients.WorkspaceLaunchStageReadbackProof, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("fabric.workspace-launch-stage-proof")
	f.stageProofCalls++
	for _, operation := range f.operations {
		if operation.ID == input.FabricRecordID && operation.OperationID == input.FabricOperationID && operation.IdempotencyKey == input.IdempotencyKey && operation.RequestHash == input.RequestHash {
			priorStatus := operation.Status
			operation.Status = "succeeded"
			return clients.WorkspaceLaunchStageReadbackProof{
				SchemaVersion: 1, Eligible: true, Reason: "none", Stage: input.Stage, PriorStatus: priorStatus,
				BindingDigest: workspaceComputeClaimStableSuffix("fabric-stage-binding", input.Stage, input.FabricRecordID), Operation: operation,
			}, nil
		}
	}
	return clients.WorkspaceLaunchStageReadbackProof{}, errors.New("workspace_launch_stage_readback_invalid")
}

func (f *workspaceLaunchReadbackRecoveryFabric) ConvergeWorkspaceLaunchStageReadback(_ context.Context, input clients.WorkspaceLaunchStageReadbackInput) (clients.WorkspaceLaunchStageReadbackProof, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("fabric.workspace-launch-stage-converge")
	f.stageConvergeCalls++
	for index := range f.operations {
		operation := f.operations[index]
		if operation.ID == input.FabricRecordID && operation.OperationID == input.FabricOperationID && operation.IdempotencyKey == input.IdempotencyKey && operation.RequestHash == input.RequestHash {
			binding := workspaceComputeClaimStableSuffix("fabric-stage-binding", input.Stage, input.FabricRecordID)
			if input.ExpectedBindingDigest == "" || input.ExpectedBindingDigest != binding {
				return clients.WorkspaceLaunchStageReadbackProof{}, errors.New("workspace_launch_stage_readback_invalid")
			}
			priorStatus, mutationCount := operation.Status, 0
			if operation.Status != "succeeded" {
				operation.Status = "succeeded"
				f.operations[index] = operation
				mutationCount = 1
			}
			return clients.WorkspaceLaunchStageReadbackProof{
				SchemaVersion: 1, Eligible: true, Reason: "none", Stage: input.Stage, PriorStatus: priorStatus,
				BindingDigest: binding, Operation: operation, FabricOperationMutationCount: mutationCount,
			}, nil
		}
	}
	return clients.WorkspaceLaunchStageReadbackProof{}, errors.New("workspace_launch_stage_readback_invalid")
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

func testWorkspaceLaunchReadbackApproval(t *testing.T, operation workspaceLaunchOperation, stage, key string, readback *workspaceLaunchReadbackRecoveryFabric) map[string]any {
	t.Helper()
	if readback.providerTruth == nil {
		t.Fatal("provider truth fixture unavailable")
	}
	target, err := workspaceLaunchReadbackRecoveryExpectedTarget(operation, *readback.providerTruth)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := workspaceLaunchReadbackRecoveryAuthorityForOperation(operation, stage, *readback.providerTruth, readback.ownership, readback.operations)
	if err != nil {
		t.Fatal(err)
	}
	if identity, specialized := workspaceLaunchReadbackStageOperationIdentity(authority.OperationIDs, stage); specialized {
		if !setWorkspaceLaunchReadbackStageBinding(&authority.OperationIDs, stage, workspaceComputeClaimStableSuffix("fabric-stage-binding", stage, identity.FabricRecordID)) {
			t.Fatal("failed to bind Fabric stage proof")
		}
	}
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
		"target":          structToMap(target),
		"resources":       structToMap(workspaceLaunchReadbackRecoveryExpectedResources(operation, *readback.providerTruth, authority)),
		"operationIds":    structToMap(authority.OperationIDs),
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

func refreshWorkspaceLaunchReadbackApprovalDigest(t *testing.T, approval map[string]any) {
	t.Helper()
	delete(approval, "approvalDigest")
	payload, err := json.Marshal(approval)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	approval["approvalDigest"] = hex.EncodeToString(digest[:])
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
	recorder := httptest.NewRecorder()
	handler := fixture.server.(*controlPlaneHTTPHandler)
	result, _, err := handler.app.recoverWorkspaceLaunchReviewWithReplay(context.Background(), handler.service, billingReviewResolutionInput{
		ResourceType: "workspace_launch", ResourceID: operation.ID, AccountID: operation.AccountID,
		BillingOperationID: operation.ID, EvidenceRef: "internal-state-machine-test", IdempotencyKey: key,
		Reviewer: sessionUserIDForTest(t, fixture.server, fixture.operator), ReadbackApproval: &typedApproval,
	})
	if err != nil {
		writeBillingReviewResolutionError(recorder, err)
		return recorder
	}
	writeJSON(recorder, http.StatusOK, result)
	return recorder
}

func persistedWorkspaceLaunchReadbackReplayFixture(t *testing.T, status string, expired bool) (workspaceLaunchWorkerFixture, map[string]any, string) {
	t.Helper()
	t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "workspace-launch-readback-capability")
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "secret", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	key := "recover-persisted-" + status
	approvalMap := testWorkspaceLaunchReadbackApproval(t, scenario.approvalOperation, "secret", key, scenario.readback)
	if expired {
		approvalMap["expiresAt"] = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
		refreshWorkspaceLaunchReadbackApprovalDigest(t, approvalMap)
	}
	var approval workspaceLaunchReadbackRecoveryApproval
	if jsonRoundTrip(approvalMap, &approval) != nil {
		t.Fatal("persisted replay approval is not JSON round-trippable")
	}
	recovered, proof, err := fixture.app.workspaceLaunchReadbackRecoveryProofForOperation(context.Background(), fixture.service, scenario.unknown)
	if err != nil {
		t.Fatal(err)
	}
	recovered.Status = status
	recovered.ReadbackRecoveryApproval = &approval
	recovered.ReadbackRecoveryProof = &proof
	recovered.ContinuationAttemptBudgets["secret"] = workspaceLaunchStageBudget{Attempted: 1, Confirmed: 1, Max: 1}
	if status == "succeeded" {
		recovered.Phase = "succeeded"
		recovered.ReceiptID = "receipt-persisted"
		recovered.URL = "https://workspace.medopl.cn/w/" + recovered.WorkspaceID + "/"
	}
	if err := fixture.store.memoryTableStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(recovered)); err != nil {
		t.Fatal(err)
	}
	scenario.readback.operationsErr = errors.New("fresh readback must not run during persisted replay")
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	return fixture, approvalMap, key
}

func requestWorkspaceLaunchReadbackProof(t *testing.T, fixture workspaceLaunchWorkerFixture) *httptest.ResponseRecorder {
	t.Helper()
	operation := fixture.operation(t)
	recorder := httptest.NewRecorder()
	handler := fixture.server.(*controlPlaneHTTPHandler)
	proof, err := handler.app.diagnoseWorkspaceLaunchReadbackRecovery(context.Background(), handler.service, operation.ID)
	if err != nil {
		writeError(recorder, http.StatusConflict, err.Error())
		return recorder
	}
	writeJSON(recorder, http.StatusOK, proof)
	return recorder
}

func fabricHTTPSerializedOperation(t *testing.T, operation clients.FabricOperation) clients.FabricOperation {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/fabric/operations" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(response).Encode([]clients.FabricOperation{operation}); err != nil {
			t.Fatal(err)
		}
	}))
	t.Cleanup(server.Close)
	operations, err := clients.NewFabricHTTPClient(server.URL, "test-fabric-token", server.Client()).ListOperations(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("Fabric operation HTTP round trip: operations=%#v err=%v", operations, err)
	}
	return operations[0]
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
	fixture := newExistingWorkspaceLaunchWorkerFixtureForPlan(t, []int64{1_000_000_000, 1_000_000_000, 1_000_000_000 - charge}, nil, nil, packageID, storageGB, false)
	launch := configureWorkspaceLaunchFulfillment(t, fixture)
	computeOperationID := "op-create-compute-" + stableID(launch.ID)[:10]
	storageOperationID := "op-create-storage-" + stableID(launch.ID)[:10]
	ownershipID := "owner-" + stableID(launch.ComputeID)[:12]
	fixture.fabric.computeSync.OperationID = launch.ID + ":compute"
	fixture.fabric.computeSync.PoolID = "pool-" + packageID
	fixture.fabric.computeSync.NodePoolID = launch.ComputeNodePoolID
	fixture.fabric.computeSync.MachineName = "machine-" + stableID(launch.ComputeID)[:10]
	fixture.fabric.computeSync.NodeName = "node-" + stableID(launch.ComputeID)[:10]
	fixture.fabric.computeSync.PrivateIP = "10.20.30.41"
	fixture.fabric.computeSync.CVMInstanceID = fixture.fabric.computeSync.InstanceID
	fixture.fabric.computeSync.CostTags = map[string]string{
		"opl_account_id": launch.AccountID, "opl_workspace_id": launch.WorkspaceID, "opl_resource_id": launch.ComputeID, "opl_operation_id": ownershipID,
	}
	fixture.fabric.storageSync.OperationID = launch.ID + ":storage"
	fixture.fabric.storageSync.CostTags = map[string]string{
		"opl_account_id": launch.AccountID, "opl_workspace_id": launch.WorkspaceID, "opl_resource_id": launch.StorageID, "opl_operation_id": storageOperationID,
	}
	attachmentID := "att_" + workspaceComputeClaimStableSuffix(launch.ID + ":attachment")[:18]
	fixture.fabric.attachment = clients.StorageAttachment{
		ID: attachmentID, OperationID: launch.ID + ":attachment", WorkspaceID: launch.WorkspaceID,
		ComputeID: launch.ComputeID, VolumeID: launch.StorageID, Status: "attached", Provider: "tencent-tke",
		ProviderAttachmentID: "pv/" + launch.StorageID + ":pvc/" + launch.StorageID + "-data",
		CostTags: map[string]string{
			"opl_account_id": launch.AccountID, "opl_workspace_id": launch.WorkspaceID, "opl_resource_id": attachmentID,
			"opl_operation_id": launch.ID + ":attachment",
		},
	}
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
	paidThrough, err := time.Parse(time.RFC3339, unknown.PaidThrough)
	if err != nil {
		t.Fatal(err)
	}
	fixture.fabric.computeSync.Deadline = paidThrough.Add(24 * time.Hour).Format(time.RFC3339)
	fixture.fabric.storageSync.Deadline = paidThrough.Add(48 * time.Hour).Format(time.RFC3339)
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
	readback := &workspaceLaunchReadbackRecoveryFabric{
		monthlyFabric: fixture.fabric,
		ownership: clients.MachineOwnership{
			ID: ownershipID, ResourceID: unknown.ComputeID, AccountID: unknown.AccountID, WorkspaceID: unknown.WorkspaceID,
			PackageID: unknown.PackageID, NodePoolID: fixture.fabric.computeSync.NodePoolID, MachineID: fixture.fabric.computeSync.MachineName,
			InstanceID: fixture.fabric.computeSync.CVMInstanceID, NodeName: fixture.fabric.computeSync.NodeName, Status: "active",
		},
		operations: []clients.FabricOperation{
			{
				ID: "fop-compute-readback", OperationID: computeOperationID, Action: "create_compute_allocation", ResourceKind: "compute_allocation",
				ResourceID: unknown.ComputeID, AccountID: unknown.AccountID, WorkspaceID: unknown.WorkspaceID, IdempotencyKey: unknown.ID + ":compute",
				RequestHash: stableID("compute", unknown.ID), Status: "succeeded", RedactedProviderPayload: map[string]any{"resource": fixture.fabric.computeSync},
			},
			{
				ID: "fop-storage-readback", OperationID: storageOperationID, Action: "create_storage_volume", ResourceKind: "storage_volume",
				ResourceID: unknown.StorageID, AccountID: unknown.AccountID, WorkspaceID: unknown.WorkspaceID, IdempotencyKey: unknown.ID + ":storage",
				RequestHash: stableID("storage", unknown.ID), Status: map[bool]string{true: "failed", false: "succeeded"}[stage == "storage"],
				RedactedProviderPayload: map[string]any{"resource": fixture.fabric.storageSync},
			},
		},
	}
	attachmentOperation := func() clients.FabricOperation {
		attachment := clients.StorageAttachment{
			ID: approvalOperation.AttachmentID, OperationID: unknown.AttachmentOperationID, WorkspaceID: unknown.WorkspaceID,
			ComputeID: unknown.ComputeID, VolumeID: unknown.StorageID, Status: "attached", Provider: "tencent-tke",
			ProviderAttachmentID: "pv/" + unknown.StorageID + ":pvc/" + unknown.StorageID + "-data",
			CostTags: map[string]string{
				"opl_account_id": unknown.AccountID, "opl_workspace_id": unknown.WorkspaceID, "opl_resource_id": approvalOperation.AttachmentID,
				"opl_operation_id": unknown.AttachmentOperationID,
			},
		}
		operation := clients.FabricOperation{
			ID:          "fop_attachment_claim_" + workspaceComputeClaimStableSuffix("create_storage_attachment", unknown.AttachmentOperationID)[:16],
			OperationID: "op_create_storage_attachment_" + workspaceComputeClaimStableSuffix(unknown.AttachmentOperationID, "storage_attachment", "create_storage_attachment")[:12], Action: "create_storage_attachment",
			ResourceKind: "storage_attachment", ResourceID: attachment.ID, AccountID: unknown.AccountID, WorkspaceID: unknown.WorkspaceID,
			IdempotencyKey: unknown.AttachmentOperationID, RequestHash: workspaceLaunchStorageAttachmentRequestHash(unknown), Status: "succeeded",
			RedactedProviderPayload: map[string]any{"resource": attachment},
		}
		return fabricHTTPSerializedOperation(t, operation)
	}
	secretOperation := func() clients.FabricOperation {
		return clients.FabricOperation{
			ID: "fop-gateway-readback", OperationID: "op-upsert-secret-" + stableID(unknown.ID)[:10], Action: "upsert_gateway_secret",
			ResourceKind: "gateway_secret", ResourceID: workspaceGatewaySecretReference(unknown.WorkspaceID), AccountID: unknown.AccountID,
			WorkspaceID: unknown.WorkspaceID, IdempotencyKey: unknown.WorkspaceOperationID + ":secret:gateway-secret",
			RequestHash: stableID("secret", unknown.ID), Status: "succeeded", RedactedProviderPayload: map[string]any{"resource": clients.GatewaySecretWriteResult{
				SecretRef: workspaceGatewaySecretReference(unknown.WorkspaceID), Version: "v1", Fingerprint: approvalOperation.WorkspaceKeyFingerprint,
			}},
		}
	}
	runtimeOperation := func() clients.FabricOperation {
		runtime := clients.WorkspaceRuntime{
			ID: approvalOperation.RuntimeID, OperationID: unknown.WorkspaceOperationID + ":runtime", WorkspaceID: unknown.WorkspaceID,
			URL: approvalOperation.URL, Status: "running", ServiceName: approvalOperation.RuntimeServiceName, Ready: true,
			CostTags: map[string]string{
				"opl_account_id": unknown.AccountID, "opl_workspace_id": unknown.WorkspaceID, "opl_resource_id": approvalOperation.RuntimeID,
				"opl_operation_id": unknown.WorkspaceOperationID + ":runtime",
			},
		}
		return clients.FabricOperation{
			ID: "fop-runtime-readback", OperationID: "op-create-runtime-" + stableID(unknown.ID)[:10], Action: "create_workspace_runtime",
			ResourceKind: "workspace_runtime", ResourceID: unknown.WorkspaceID, AccountID: unknown.AccountID, WorkspaceID: unknown.WorkspaceID,
			IdempotencyKey: unknown.WorkspaceOperationID + ":runtime", RequestHash: stableID("runtime", unknown.ID), Status: "succeeded",
			RedactedProviderPayload: map[string]any{"resource": runtime},
		}
	}
	switch stage {
	case "attachment":
		attachment := clients.StorageAttachment{
			ID: attachmentID, OperationID: unknown.AttachmentOperationID, WorkspaceID: unknown.WorkspaceID,
			ComputeID: unknown.ComputeID, VolumeID: unknown.StorageID, Status: "attached", Provider: "tencent-tke",
		}
		approvalOperation.AttachmentID = attachment.ID
	case "secret":
		fingerprint := "sha256:" + strings.Repeat("c", 64)
		approvalOperation.WorkspaceKeyFingerprint = fingerprint
	case "runtime":
		runtime := clients.WorkspaceRuntime{
			ID: "runtime-authoritative", OperationID: unknown.WorkspaceOperationID + ":runtime", WorkspaceID: unknown.WorkspaceID,
			URL: "https://workspace.medopl.cn/w/" + unknown.WorkspaceID + "/", Status: "running", ServiceName: "opl-compute-authoritative", Ready: true,
			Access: clients.WorkspaceRuntimeAccess{Username: "admin", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-compute-authoritative-env"},
		}
		approvalOperation.RuntimeID, approvalOperation.RuntimeReady = runtime.ID, runtime.Ready
		approvalOperation.RuntimeServiceName, approvalOperation.RuntimeUsername = runtime.ServiceName, runtime.Access.Username
		approvalOperation.CredentialStatus, approvalOperation.CredentialVersion = runtime.Access.CredentialStatus, runtime.Access.CredentialVersion
		approvalOperation.CredentialSecretRef, approvalOperation.URL = runtime.Access.SecretRef, runtime.URL
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
	if workspaceLaunchReadbackStageIndex(stage) >= workspaceLaunchReadbackStageIndex("attachment") {
		readback.operations = append(readback.operations, attachmentOperation())
	}
	if workspaceLaunchReadbackStageIndex(stage) >= workspaceLaunchReadbackStageIndex("secret") {
		readback.operations = append(readback.operations, secretOperation())
	}
	if workspaceLaunchReadbackStageIndex(stage) >= workspaceLaunchReadbackStageIndex("runtime") {
		readback.operations = append(readback.operations, runtimeOperation())
	}
	if stage == "attachment" || stage == "secret" || stage == "runtime" {
		for index := range readback.operations {
			if readback.operations[index].Action == map[string]string{
				"attachment": "create_storage_attachment", "secret": "upsert_gateway_secret", "runtime": "create_workspace_runtime",
			}[stage] {
				readback.operations[index].Status = "started"
			}
		}
	}
	return workspaceLaunchReadbackRecoveryScenario{
		fixture: fixture, unknown: unknown, approvalOperation: approvalOperation, readback: readback, beforeCurrentWrites: beforeCurrentWrites,
	}
}

func TestWorkspaceLaunchUnknownStageConvergesFromAuthoritativeReadbackAfterRestartWithoutSecondWrite(t *testing.T) {
	t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "workspace-launch-readback-capability")
	for _, packageID := range []string{"basic", "pro"} {
		for _, stage := range workspaceLaunchContinuationStages {
			t.Run(packageID+"/"+stage, func(t *testing.T) {
				scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, stage, packageID)
				fixture := scenario.fixture
				fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
				server, err := NewPersistentServer(fixture.service, fixture.store)
				if err != nil {
					t.Fatal(err)
				}
				fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
				key := "recover-readback-" + stableID(stage)[:8]
				approval := testWorkspaceLaunchReadbackApproval(t, scenario.approvalOperation, stage, key, scenario.readback)
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
}

func TestWorkspaceRecoveryManualReviewToSecretRequiresLiveCodexGroupReadback(t *testing.T) {
	t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "workspace-launch-readback-capability")
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))

	for _, test := range []struct {
		name       string
		keyGroup   *int64
		wantStatus string
		wantSecret bool
	}{
		{name: "live Codex group permits Secret", keyGroup: workspaceTestCodexGroupID(), wantStatus: "succeeded", wantSecret: true},
		{name: "missing live group blocks Secret", keyGroup: nil, wantStatus: "manual_review", wantSecret: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "attachment", "basic")
			fixture := scenario.fixture
			fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
			operation := fixture.operation(t)
			// A legacy row may retain only the configured marker. Recovery must
			// re-read the same Sub2API Key before crossing the Secret boundary.
			operation.WorkspaceKeyStatus = "configured"
			operation.WorkspaceKeyGroupID = *workspaceTestCodexGroupID()
			key := fixture.sub2API.keys[operation.WorkspaceAPIKeyID]
			key.GroupID = test.keyGroup
			fixture.sub2API.keys[operation.WorkspaceAPIKeyID] = key
			mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
			beforeSecretWrites := workspaceLaunchStageWriteCount(fixture, "secret")

			server, err := NewPersistentServer(fixture.service, fixture.store)
			if err != nil {
				t.Fatal(err)
			}
			fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)

			diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
			if diagnosed.Status != "diagnosed" {
				t.Fatalf("diagnosed plan=%#v", diagnosed)
			}
			validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
				"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
			}))
			if validated.Status != "validated" {
				t.Fatalf("validated plan=%#v", validated)
			}

			executed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/execute", map[string]any{
				"planId": validated.PlanID, "planDigest": validated.PlanDigest, "decision": "continue", "confirmation": "CONTINUE_RECOVERY_PLAN",
			}))
			current := fixture.operation(t)
			secretWrites := workspaceLaunchStageWriteCount(fixture, "secret")
			runtimeWrites := workspaceLaunchStageWriteCount(fixture, "runtime")
			if test.wantSecret {
				if executed.Status != "completed" || current.Status != test.wantStatus || current.Phase != "succeeded" ||
					secretWrites != beforeSecretWrites+1 || runtimeWrites == 0 || fixture.sub2API.updateCalls != 0 {
					t.Fatalf("live Codex Recovery did not cross Secret boundary: executed=%#v current=%#v secretWrites=%d baseline=%d runtimeWrites=%d keyUpdates=%d", executed, current, secretWrites, beforeSecretWrites, runtimeWrites, fixture.sub2API.updateCalls)
				}
				return
			}
			if executed.Status != "failed" || current.Status != test.wantStatus || current.Phase != "secret_writing" ||
				current.ErrorCode != errWorkspaceCodexGroupReadback.Error() || secretWrites != beforeSecretWrites || runtimeWrites != 0 || fixture.sub2API.updateCalls != 0 {
				t.Fatalf("missing Codex group crossed Secret/Runtime boundary: executed=%#v current=%#v secretWrites=%d baseline=%d runtimeWrites=%d keyUpdates=%d", executed, current, secretWrites, beforeSecretWrites, runtimeWrites, fixture.sub2API.updateCalls)
			}
		})
	}
}

func TestWorkspaceLaunchRecoveredStorageAuthorizesAttachmentRecoveryAfterRestart(t *testing.T) {
	t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "workspace-launch-readback-capability")
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "storage", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)

	fixture.fabric.attachmentErr = errors.New("attachment response lost after write")
	storageKey := "recover-storage-before-attachment"
	storageApproval := testWorkspaceLaunchReadbackApproval(t, scenario.approvalOperation, "storage", storageKey, scenario.readback)
	storageResponse := requestWorkspaceLaunchReadbackRecovery(t, fixture, storageApproval, storageKey)
	if storageResponse.Code != http.StatusOK {
		t.Fatalf("storage recovery status=%d body=%s", storageResponse.Code, storageResponse.Body.String())
	}
	attachmentUnknown := fixture.operation(t)
	if attachmentUnknown.Status != "manual_review" || attachmentUnknown.Phase != workspaceLaunchReadbackRecoveryPhase("attachment") ||
		attachmentUnknown.ContinuationAttemptBudgets["storage"] != (workspaceLaunchStageBudget{Attempted: 1, Confirmed: 1, Max: 1}) ||
		attachmentUnknown.ContinuationAttemptBudgets["attachment"] != (workspaceLaunchStageBudget{Attempted: 1, Unknown: 1, Max: 1}) ||
		attachmentUnknown.ReadbackRecoveryApproval == nil || attachmentUnknown.ReadbackRecoveryApproval.Stage != "storage" ||
		attachmentUnknown.ReadbackRecoveryProof == nil || attachmentUnknown.ReadbackRecoveryProof.Stage != "storage" {
		t.Fatalf("sequential recovery state=%#v", attachmentUnknown)
	}
	if scenario.readback.operations[1].Status != "failed" || len(fixture.fabric.storageIDs) != 1 || countStrings(*fixture.events, "fabric.attachment") != 1 {
		t.Fatalf("storage recovery changed Fabric operation or repeated a write: operations=%#v events=%#v", scenario.readback.operations, *fixture.events)
	}

	fixture.fabric.attachmentErr = nil
	fixture.fabric.gatewaySecretErr = errors.New("gateway Secret response lost after write")
	attachment := fixture.fabric.attachment
	scenario.readback.operations = append(scenario.readback.operations, fabricHTTPSerializedOperation(t, clients.FabricOperation{
		ID:          "fop_attachment_claim_" + workspaceComputeClaimStableSuffix("create_storage_attachment", attachmentUnknown.AttachmentOperationID)[:16],
		OperationID: workspaceLaunchStorageAttachmentFabricOperationID(attachmentUnknown), Action: "create_storage_attachment",
		ResourceKind: "storage_attachment", ResourceID: attachment.ID, AccountID: attachmentUnknown.AccountID, WorkspaceID: attachmentUnknown.WorkspaceID,
		IdempotencyKey: attachmentUnknown.AttachmentOperationID, RequestHash: workspaceLaunchStorageAttachmentRequestHash(attachmentUnknown), Status: "started",
		RedactedProviderPayload: map[string]any{"resource": attachment},
	}))

	restarted, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = restarted, reservedOperatorSessionForTest(t, restarted)
	for _, test := range []struct {
		name   string
		mutate func(*workspaceLaunchOperation)
	}{
		{name: "missing persisted storage recovery", mutate: func(operation *workspaceLaunchOperation) {
			operation.ReadbackRecoveryApproval, operation.ReadbackRecoveryProof = nil, nil
		}},
		{name: "storage budget not confirmed", mutate: func(operation *workspaceLaunchOperation) {
			operation.ContinuationAttemptBudgets["storage"] = workspaceLaunchStageBudget{Attempted: 1, Max: 1}
		}},
		{name: "storage operation identity drift", mutate: func(operation *workspaceLaunchOperation) {
			operation.ReadbackRecoveryApproval.OperationIDs.Storage.RequestHash = strings.Repeat("d", 64)
			operation.ReadbackRecoveryProof.OperationIDs.Storage.RequestHash = strings.Repeat("d", 64)
			operation.ReadbackRecoveryApproval.ApprovalDigest = workspaceLaunchReadbackRecoveryApprovalDigest(*operation.ReadbackRecoveryApproval)
		}},
		{name: "storage resource identity drift", mutate: func(operation *workspaceLaunchOperation) {
			operation.ReadbackRecoveryApproval.Resources.StorageProviderResourceID += "-other"
			operation.ReadbackRecoveryProof.Resources.StorageProviderResourceID += "-other"
			operation.ReadbackRecoveryApproval.ApprovalDigest = workspaceLaunchReadbackRecoveryApprovalDigest(*operation.ReadbackRecoveryApproval)
		}},
		{name: "storage approval binding drift", mutate: func(operation *workspaceLaunchOperation) {
			operation.ReadbackRecoveryApproval.ApprovalDigest = strings.Repeat("0", 64)
		}},
	} {
		t.Run("fails closed/"+test.name, func(t *testing.T) {
			mutated := attachmentUnknown
			mutated.ContinuationAttemptBudgets = make(map[string]workspaceLaunchStageBudget, len(attachmentUnknown.ContinuationAttemptBudgets))
			for stage, budget := range attachmentUnknown.ContinuationAttemptBudgets {
				mutated.ContinuationAttemptBudgets[stage] = budget
			}
			approval := *mutated.ReadbackRecoveryApproval
			proof := *mutated.ReadbackRecoveryProof
			mutated.ReadbackRecoveryApproval, mutated.ReadbackRecoveryProof = &approval, &proof
			test.mutate(&mutated)
			mustStore(t, fixture.store.memoryTableStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(mutated)))
			response := requestWorkspaceLaunchReadbackProof(t, fixture)
			if response.Code != http.StatusConflict {
				t.Fatalf("diagnosis status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	mustStore(t, fixture.store.memoryTableStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(attachmentUnknown)))
	scenario.readback.operations[1].Status = "started"
	if response := requestWorkspaceLaunchReadbackProof(t, fixture); response.Code != http.StatusOK {
		t.Fatalf("attachment diagnosis after recovered started storage status=%d body=%s", response.Code, response.Body.String())
	}
	scenario.readback.operations[1].Status = "failed"
	proofResponse := requestWorkspaceLaunchReadbackProof(t, fixture)
	if proofResponse.Code != http.StatusOK {
		t.Fatalf("attachment diagnosis after recovered storage status=%d body=%s", proofResponse.Code, proofResponse.Body.String())
	}

	attachmentKey := "recover-attachment-after-storage"
	attachmentApprovalOperation := attachmentUnknown
	attachmentApprovalOperation.AttachmentID = attachment.ID
	attachmentApproval := testWorkspaceLaunchReadbackApproval(t, attachmentApprovalOperation, "attachment", attachmentKey, scenario.readback)
	attachmentResponse := requestWorkspaceLaunchReadbackRecovery(t, fixture, attachmentApproval, attachmentKey)
	if attachmentResponse.Code != http.StatusOK {
		t.Fatalf("attachment recovery status=%d body=%s", attachmentResponse.Code, attachmentResponse.Body.String())
	}
	secretUnknown := fixture.operation(t)
	if secretUnknown.Status != "manual_review" || secretUnknown.Phase != workspaceLaunchReadbackRecoveryPhase("secret") ||
		secretUnknown.ContinuationAttemptBudgets["attachment"] != (workspaceLaunchStageBudget{Attempted: 1, Confirmed: 1, Max: 1}) ||
		secretUnknown.ContinuationAttemptBudgets["secret"] != (workspaceLaunchStageBudget{Attempted: 1, Unknown: 1, Max: 1}) ||
		secretUnknown.ReadbackRecoveryApproval == nil || secretUnknown.ReadbackRecoveryApproval.Stage != "attachment" ||
		secretUnknown.ReadbackRecoveryProof == nil || secretUnknown.ReadbackRecoveryProof.Stage != "attachment" ||
		len(fixture.fabric.gatewaySecretInputs) != 1 {
		t.Fatalf("attachment recovery did not preserve the next unknown stage: operation=%#v gatewayInputs=%#v", secretUnknown, fixture.fabric.gatewaySecretInputs)
	}
	fingerprint := fixture.fabric.gatewaySecretInputs[0].Fingerprint
	scenario.readback.operations = append(scenario.readback.operations, clients.FabricOperation{
		ID: "fop-gateway-readback", OperationID: "op-upsert-secret-" + stableID(secretUnknown.ID)[:10], Action: "upsert_gateway_secret",
		ResourceKind: "gateway_secret", ResourceID: workspaceGatewaySecretReference(secretUnknown.WorkspaceID), AccountID: secretUnknown.AccountID,
		WorkspaceID: secretUnknown.WorkspaceID, IdempotencyKey: secretUnknown.WorkspaceOperationID + ":secret:gateway-secret",
		RequestHash: stableID("secret", secretUnknown.ID), Status: "started", RedactedProviderPayload: map[string]any{"resource": clients.GatewaySecretWriteResult{
			SecretRef: workspaceGatewaySecretReference(secretUnknown.WorkspaceID), Version: "v1", Fingerprint: fingerprint,
		}},
	})
	fixture.fabric.gatewaySecretErr = nil
	restarted, err = NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = restarted, reservedOperatorSessionForTest(t, restarted)
	for _, test := range []struct {
		name   string
		mutate func(*workspaceLaunchOperation)
	}{
		{name: "latest recovered stage budget not confirmed", mutate: func(operation *workspaceLaunchOperation) {
			operation.ContinuationAttemptBudgets["attachment"] = workspaceLaunchStageBudget{Attempted: 1, Max: 1}
		}},
		{name: "latest recovery binding missing", mutate: func(operation *workspaceLaunchOperation) {
			operation.ReadbackRecoveryApproval.OperationIDs.Attachment.ReadbackBindingDigest = ""
			operation.ReadbackRecoveryProof.OperationIDs.Attachment.ReadbackBindingDigest = ""
			operation.ReadbackRecoveryApproval.ApprovalDigest = workspaceLaunchReadbackRecoveryApprovalDigest(*operation.ReadbackRecoveryApproval)
		}},
		{name: "latest recovery proof drift", mutate: func(operation *workspaceLaunchOperation) {
			operation.ReadbackRecoveryProof.OperationIDs.Storage.RequestHash = strings.Repeat("e", 64)
		}},
	} {
		t.Run("fails closed after later recovery/"+test.name, func(t *testing.T) {
			mutated := secretUnknown
			mutated.ContinuationAttemptBudgets = make(map[string]workspaceLaunchStageBudget, len(secretUnknown.ContinuationAttemptBudgets))
			for stage, budget := range secretUnknown.ContinuationAttemptBudgets {
				mutated.ContinuationAttemptBudgets[stage] = budget
			}
			approval := *mutated.ReadbackRecoveryApproval
			proof := *mutated.ReadbackRecoveryProof
			mutated.ReadbackRecoveryApproval, mutated.ReadbackRecoveryProof = &approval, &proof
			test.mutate(&mutated)
			mustStore(t, fixture.store.memoryTableStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(mutated)))
			response := requestWorkspaceLaunchReadbackProof(t, fixture)
			if response.Code != http.StatusConflict {
				t.Fatalf("diagnosis status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	mustStore(t, fixture.store.memoryTableStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(secretUnknown)))
	originalStorageProviderID := scenario.readback.providerTruth.Storage.ProviderResourceID
	scenario.readback.providerTruth.Storage.ProviderResourceID = originalStorageProviderID + "-other"
	if response := requestWorkspaceLaunchReadbackProof(t, fixture); response.Code != http.StatusConflict {
		t.Fatalf("provider identity drift diagnosis status=%d body=%s", response.Code, response.Body.String())
	}
	scenario.readback.providerTruth.Storage.ProviderResourceID = originalStorageProviderID
	proofResponse = requestWorkspaceLaunchReadbackProof(t, fixture)
	if proofResponse.Code != http.StatusOK {
		t.Fatalf("secret diagnosis after storage and attachment recovery status=%d body=%s", proofResponse.Code, proofResponse.Body.String())
	}

	secretKey := "recover-stage-" + stableID("storage", "attachment", "secret")[:12]
	secretApprovalOperation := secretUnknown
	secretApprovalOperation.WorkspaceKeyFingerprint = fingerprint
	secretApproval := testWorkspaceLaunchReadbackApproval(t, secretApprovalOperation, "secret", secretKey, scenario.readback)
	secretResponse := requestWorkspaceLaunchReadbackRecovery(t, fixture, secretApproval, secretKey)
	if secretResponse.Code != http.StatusOK {
		t.Fatalf("secret recovery status=%d body=%s", secretResponse.Code, secretResponse.Body.String())
	}
	recovered := fixture.operation(t)
	if recovered.Status != "succeeded" || recovered.Phase != "succeeded" || recovered.URL == "" || recovered.ReceiptID == "" ||
		recovered.ContinuationAttemptBudgets["secret"] != (workspaceLaunchStageBudget{Attempted: 1, Confirmed: 1, Max: 1}) ||
		len(fixture.fabric.storageIDs) != 1 || countStrings(*fixture.events, "fabric.attachment") != 1 ||
		countStrings(*fixture.events, "fabric.gateway-secret") != 1 || scenario.readback.operations[1].Status != "failed" {
		t.Fatalf("sequential recovery crossed bounds: operation=%#v operations=%#v events=%#v", recovered, scenario.readback.operations, *fixture.events)
	}
}

func TestWorkspaceLaunchAttachmentSecretRuntimeUseDedicatedFabricProofAndCAS(t *testing.T) {
	t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "workspace-launch-readback-capability")
	for _, packageID := range []string{"basic", "pro"} {
		for _, stage := range []string{"attachment", "secret", "runtime"} {
			t.Run(packageID+"/"+stage, func(t *testing.T) {
				scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, stage, packageID)
				fixture := scenario.fixture
				fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
				server, err := NewPersistentServer(fixture.service, fixture.store)
				if err != nil {
					t.Fatal(err)
				}
				fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
				proofResponse := requestWorkspaceLaunchReadbackProof(t, fixture)
				if proofResponse.Code != http.StatusOK || scenario.readback.stageProofCalls != 1 || scenario.readback.stageConvergeCalls != 0 {
					t.Fatalf("diagnosis status=%d proofCalls=%d convergeCalls=%d body=%s", proofResponse.Code, scenario.readback.stageProofCalls, scenario.readback.stageConvergeCalls, proofResponse.Body.String())
				}
				key := "recover-dedicated-" + stableID(packageID, stage)[:8]
				approval := testWorkspaceLaunchReadbackApproval(t, scenario.approvalOperation, stage, key, scenario.readback)
				response := requestWorkspaceLaunchReadbackRecovery(t, fixture, approval, key)
				if response.Code != http.StatusOK || scenario.readback.stageConvergeCalls != 1 {
					t.Fatalf("recovery status=%d proofCalls=%d convergeCalls=%d body=%s", response.Code, scenario.readback.stageProofCalls, scenario.readback.stageConvergeCalls, response.Body.String())
				}
				recovered := fixture.operation(t)
				if recovered.Status != "succeeded" || recovered.URL == "" || recovered.ReceiptID == "" {
					t.Fatalf("recovered launch=%#v", recovered)
				}
			})
		}
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
			expectedTarget, targetErr := workspaceLaunchReadbackRecoveryExpectedTarget(scenario.approvalOperation, *scenario.readback.providerTruth)
			expectedAuthority, authorityErr := workspaceLaunchReadbackRecoveryAuthorityForOperation(
				scenario.approvalOperation, stage, *scenario.readback.providerTruth, scenario.readback.ownership, scenario.readback.operations,
			)
			if targetErr != nil || authorityErr != nil {
				t.Fatalf("expected authority targetErr=%v authorityErr=%v", targetErr, authorityErr)
			}
			if identity, specialized := workspaceLaunchReadbackStageOperationIdentity(expectedAuthority.OperationIDs, stage); specialized {
				if !setWorkspaceLaunchReadbackStageBinding(&expectedAuthority.OperationIDs, stage, workspaceComputeClaimStableSuffix("fabric-stage-binding", stage, identity.FabricRecordID)) {
					t.Fatal("expected authority binding failed")
				}
			}
			expectedResources := workspaceLaunchReadbackRecoveryExpectedResources(scenario.approvalOperation, *scenario.readback.providerTruth, expectedAuthority)
			if proof.SchemaVersion != 1 || !proof.Eligible || proof.Reason != "none" || proof.Stage != stage ||
				proof.Customer != (workspaceLaunchReadbackRecoveryCustomer{Email: "alpha@example.com", AccountID: before.AccountID, OwnerUserID: before.OwnerUserID}) ||
				proof.Target != expectedTarget || proof.Resources != expectedResources || proof.OperationIDs != expectedAuthority.OperationIDs ||
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

func postgresSchemaSnapshot(t *testing.T, db *sql.DB, schema string) string {
	t.Helper()
	rows, err := db.Query(`SELECT table_name FROM information_schema.tables WHERE table_schema = $1 AND table_type = 'BASE TABLE' ORDER BY table_name`, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	tables := []string{}
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	snapshot := make(map[string]json.RawMessage, len(tables))
	quote := func(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
	for _, table := range tables {
		query := fmt.Sprintf(`SELECT COALESCE(jsonb_agg(to_jsonb(row_data) ORDER BY to_jsonb(row_data)::text), '[]'::jsonb)::text FROM %s.%s AS row_data`, quote(schema), quote(table))
		var encoded string
		if err := db.QueryRow(query).Scan(&encoded); err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		snapshot[table] = json.RawMessage(encoded)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestPostgresWorkspaceLaunchReadbackProofLeavesEntireSchemaUnchanged(t *testing.T) {
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "secret", "basic")
	admin := openControlPlaneTestPostgres(t)
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("control_plane_readback_snapshot_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })

	state, err := newTestPostgresEntStateStore(controlPlaneTestPostgresURL(t, "postgres", schema))
	if err != nil {
		t.Fatal(err)
	}
	store := state.(*postgresEntStateStore)
	t.Cleanup(func() { _ = store.client.Close() })
	seedTenantMember(t, store, scenario.unknown.AccountID, "org-alpha", scenario.unknown.OwnerUserID, "alpha@example.com")
	if err := store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(scenario.unknown)); err != nil {
		t.Fatal(err)
	}
	service := controlplane.NewService(scenario.fixture.ledger, scenario.readback, scenario.fixture.sub2API)
	server, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatal(err)
	}
	fixture := scenario.fixture
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	before := postgresSchemaSnapshot(t, admin, schema)
	response := requestWorkspaceLaunchReadbackProof(t, fixture)
	if response.Code != http.StatusOK {
		t.Fatalf("proof status=%d body=%s", response.Code, response.Body.String())
	}
	after := postgresSchemaSnapshot(t, admin, schema)
	if after != before {
		t.Fatalf("GET readback proof mutated PostgreSQL schema\nbefore=%s\nafter=%s", before, after)
	}
}

func TestPostgresWorkspaceLaunchPersistedReadbackReplaySurvivesReopenAndLeavesEntireSchemaUnchanged(t *testing.T) {
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "workspace-launch-readback-capability")
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "secret", "basic")
	key := "recover-postgres-persisted-replay"
	approvalMap := testWorkspaceLaunchReadbackApproval(t, scenario.approvalOperation, "secret", key, scenario.readback)
	approvalMap["expiresAt"] = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	refreshWorkspaceLaunchReadbackApprovalDigest(t, approvalMap)
	approval := parsedWorkspaceLaunchReadbackApproval(t, approvalMap, key)
	service := controlplane.NewService(scenario.fixture.ledger, scenario.readback, scenario.fixture.sub2API)
	recovered, proof, err := scenario.fixture.app.workspaceLaunchReadbackRecoveryProofForOperation(context.Background(), service, scenario.unknown)
	if err != nil {
		t.Fatal(err)
	}
	recovered.Status = "waiting"
	recovered.ReadbackRecoveryApproval = &approval
	recovered.ReadbackRecoveryProof = &proof
	recovered.ContinuationAttemptBudgets["secret"] = workspaceLaunchStageBudget{Attempted: 1, Confirmed: 1, Max: 1}

	admin := openControlPlaneTestPostgres(t)
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("control_plane_readback_replay_%d", time.Now().UnixNano())
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
	seedTenantMember(t, first, recovered.AccountID, "org-alpha", recovered.OwnerUserID, "alpha@example.com")
	if err := first.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(recovered)); err != nil {
		t.Fatal(err)
	}
	if err := first.client.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedState, err := newTestPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	reopened := reopenedState.(*postgresEntStateStore)
	t.Cleanup(func() { _ = reopened.client.Close() })
	server, err := NewPersistentServer(service, reopened)
	if err != nil {
		t.Fatal(err)
	}
	operator := reservedOperatorSessionForTest(t, server)
	scenario.readback.operationsErr = errors.New("fresh readback must not run during persisted replay")
	beforeEvents := append([]string(nil), (*scenario.fixture.events)...)
	before := postgresSchemaSnapshot(t, admin, schema)
	fixture := scenario.fixture
	fixture.server, fixture.service, fixture.operator = server, service, operator
	response := requestWorkspaceLaunchReadbackRecovery(t, fixture, approvalMap, key)
	if response.Code != http.StatusOK {
		t.Fatalf("persisted PostgreSQL replay status=%d body=%s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if json.Unmarshal(response.Body.Bytes(), &payload) != nil || stringValue(payload["status"]) != "waiting" || payload["readbackRecoveryProof"] == nil {
		t.Fatalf("persisted PostgreSQL replay response=%s", response.Body.String())
	}
	after := postgresSchemaSnapshot(t, admin, schema)
	if after != before || !equalStringSlices(*scenario.fixture.events, beforeEvents) {
		t.Fatalf("persisted PostgreSQL replay mutated state\nbefore=%s\nafter=%s\nevents=%#v/%#v", before, after, beforeEvents, *scenario.fixture.events)
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
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "secret", "basic")
	return scenario.fixture, scenario.unknown, scenario.approvalOperation, scenario.readback
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
		{name: "machine ownership drift", mutate: func(f *workspaceLaunchReadbackRecoveryFabric) { f.ownership.InstanceID = "ins-other" }},
		{name: "machine ownership read error", mutate: func(f *workspaceLaunchReadbackRecoveryFabric) {
			f.ownershipErr = errors.New("MachineOwnership unavailable")
		}},
		{name: "storage provider operation drift", mutate: func(f *workspaceLaunchReadbackRecoveryFabric) {
			f.providerTruth.Storage.CostTags["opl_operation_id"] = "op-storage-other"
		}},
		{name: "attachment missing", mutate: func(f *workspaceLaunchReadbackRecoveryFabric) {
			filtered := f.operations[:0]
			for _, operation := range f.operations {
				if operation.Action != "create_storage_attachment" {
					filtered = append(filtered, operation)
				}
			}
			f.operations = filtered
		}},
		{name: "attachment multiple", mutate: func(f *workspaceLaunchReadbackRecoveryFabric) {
			for _, operation := range f.operations {
				if operation.Action == "create_storage_attachment" {
					duplicate := operation
					duplicate.ID += "-duplicate"
					duplicate.OperationID += "-duplicate"
					f.operations = append(f.operations, duplicate)
					return
				}
			}
		}},
		{name: "attachment resource id drift", mutate: func(f *workspaceLaunchReadbackRecoveryFabric) {
			for index := range f.operations {
				if f.operations[index].Action == "create_storage_attachment" {
					wrongID := "att_" + strings.Repeat("f", 18)
					resource := f.operations[index].RedactedProviderPayload["resource"].(map[string]any)
					resource["id"] = wrongID
					resource["costTags"].(map[string]any)["opl_resource_id"] = wrongID
					f.operations[index].ResourceID = wrongID
					f.operations[index].RedactedProviderPayload["resource"] = resource
				}
			}
		}},
		{name: "attachment request hash drift", mutate: func(f *workspaceLaunchReadbackRecoveryFabric) {
			for index := range f.operations {
				if f.operations[index].Action == "create_storage_attachment" {
					f.operations[index].RequestHash = strings.Repeat("f", 64)
				}
			}
		}},
		{name: "attachment Fabric operation drift", mutate: func(f *workspaceLaunchReadbackRecoveryFabric) {
			for index := range f.operations {
				if f.operations[index].Action == "create_storage_attachment" {
					f.operations[index].OperationID = "op_create_storage_attachment_other"
				}
			}
		}},
		{name: "attachment provider identity drift", mutate: func(f *workspaceLaunchReadbackRecoveryFabric) {
			for index := range f.operations {
				if f.operations[index].Action == "create_storage_attachment" {
					resource := f.operations[index].RedactedProviderPayload["resource"].(map[string]any)
					resource["costTags"].(map[string]any)["opl_operation_id"] = "attachment-operation-other"
					f.operations[index].RedactedProviderPayload["resource"] = resource
				}
			}
		}},
		{name: "operation read error", mutate: func(f *workspaceLaunchReadbackRecoveryFabric) {
			f.operationsErr = errors.New("Fabric operation read unavailable")
		}},
		{name: "provider truth read error", mutate: func(f *workspaceLaunchReadbackRecoveryFabric) {
			f.providerTruthErr = errors.New("provider truth unavailable")
		}},
		{name: "compute deadline before paidThrough", mutate: func(f *workspaceLaunchReadbackRecoveryFabric) {
			f.providerTruth.Compute.Deadline = "2000-01-01T00:00:00Z"
		}},
		{name: "storage deadline before paidThrough", mutate: func(f *workspaceLaunchReadbackRecoveryFabric) {
			f.providerTruth.Storage.Deadline = "2000-01-01T00:00:00Z"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, unknown, approvalOperation, readback := newUnknownSecretReadbackFixture(t)
			key := "recover-negative-" + stableID(test.name)[:8]
			approval := testWorkspaceLaunchReadbackApproval(t, approvalOperation, "secret", key, readback)
			test.mutate(readback)
			fixture.service = controlplane.NewService(fixture.ledger, readback, fixture.sub2API)
			server, err := NewPersistentServer(fixture.service, fixture.store)
			if err != nil {
				t.Fatal(err)
			}
			fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
			beforeWrites := workspaceLaunchStageWriteCount(fixture, "secret")
			response := requestWorkspaceLaunchReadbackRecovery(t, fixture, approval, key)
			if response.Code != http.StatusConflict {
				t.Fatalf("fail-closed recovery status=%d body=%s", response.Code, response.Body.String())
			}
			current := fixture.operation(t)
			if current.PersistedResult != unknown.PersistedResult || current.Status != "manual_review" || current.Phase != unknown.Phase || current.ErrorCode != unknown.ErrorCode ||
				current.ContinuationAttemptBudgets["secret"] != (workspaceLaunchStageBudget{Attempted: 1, Unknown: 1, Max: 1}) ||
				workspaceLaunchStageWriteCount(fixture, "secret") != beforeWrites || countStrings(*fixture.events, "fabric.runtime") != 0 || fixture.store.activationCalls != 0 || len(fixture.ledger.receiptInputs) != 0 {
				t.Fatalf("fail-closed %s crossed recovery gate: operation=%#v events=%#v", test.name, current, *fixture.events)
			}
		})
	}
}

func TestWorkspaceLaunchReadbackRecoveryStageRejectsAmbiguousOrMalformedBudget(t *testing.T) {
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	for _, test := range []struct {
		name   string
		mutate func(*workspaceLaunchOperation)
	}{
		{name: "multiple unknown stages", mutate: func(operation *workspaceLaunchOperation) {
			operation.ContinuationAttemptBudgets["secret"] = workspaceLaunchStageBudget{Attempted: 1, Unknown: 1, Max: workspaceLaunchStageMax}
		}},
		{name: "malformed budget", mutate: func(operation *workspaceLaunchOperation) {
			operation.ContinuationAttemptBudgets["runtime"] = workspaceLaunchStageBudget{Attempted: 2, Unknown: 1, Max: workspaceLaunchStageMax}
		}},
		{name: "phase mismatch", mutate: func(operation *workspaceLaunchOperation) {
			operation.Phase = "secret_writing"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			operation := scenario.unknown
			test.mutate(&operation)
			if stage, ok := workspaceLaunchReadbackRecoveryStage(operation); ok {
				t.Fatalf("invalid recovery state accepted: stage=%q operation=%#v", stage, operation)
			}
		})
	}
}

func TestWorkspaceLaunchReadbackApprovalDriftStopsBeforeProviderOrDatabaseMutation(t *testing.T) {
	t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "workspace-launch-readback-capability")
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "customer", mutate: func(value map[string]any) { value["customer"].(map[string]any)["email"] = "other@example.com" }},
		{name: "launch target", mutate: func(value map[string]any) {
			value["target"].(map[string]any)["launchOperationId"] = "workspace-launch-other"
		}},
		{name: "compute resource", mutate: func(value map[string]any) { value["resources"].(map[string]any)["computeAllocationId"] = "ca_other" }},
		{name: "operation identity", mutate: func(value map[string]any) {
			value["operationIds"].(map[string]any)["compute"].(map[string]any)["idempotencyKey"] = "workspace-launch-other:compute"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "secret", "basic")
			fixture := scenario.fixture
			fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
			server, err := NewPersistentServer(fixture.service, fixture.store)
			if err != nil {
				t.Fatal(err)
			}
			fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
			key := "recover-approval-" + stableID(test.name)[:8]
			approval := testWorkspaceLaunchReadbackApproval(t, scenario.approvalOperation, "secret", key, scenario.readback)
			test.mutate(approval)
			refreshWorkspaceLaunchReadbackApprovalDigest(t, approval)
			before := fixture.operation(t)
			beforeEvents := append([]string(nil), (*fixture.events)...)
			response := requestWorkspaceLaunchReadbackRecovery(t, fixture, approval, key)
			current := fixture.operation(t)
			if response.Code != http.StatusConflict || current.PersistedResult != before.PersistedResult || !equalStringSlices(*fixture.events, beforeEvents) {
				t.Fatalf("approval drift crossed preflight: status=%d body=%s before=%#v current=%#v events=%#v", response.Code, response.Body.String(), before, current, *fixture.events)
			}
		})
	}
}

func TestWorkspaceLaunchTerminalReadbackRecoveryReplaysOnlyPersistedApprovalAndProof(t *testing.T) {
	t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "workspace-launch-readback-capability")
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "secret", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	key := "recover-terminal-readback"
	approval := testWorkspaceLaunchReadbackApproval(t, scenario.approvalOperation, "secret", key, scenario.readback)
	first := requestWorkspaceLaunchReadbackRecovery(t, fixture, approval, key)
	if first.Code != http.StatusOK {
		t.Fatalf("first recovery status=%d body=%s", first.Code, first.Body.String())
	}
	beforeWrites := append([]string(nil), (*fixture.events)...)
	replay := requestWorkspaceLaunchReadbackRecovery(t, fixture, approval, key)
	if replay.Code != http.StatusOK || replay.Body.String() != first.Body.String() {
		t.Fatalf("terminal replay status=%d first=%s replay=%s", replay.Code, first.Body.String(), replay.Body.String())
	}
	var payload map[string]any
	if json.Unmarshal(replay.Body.Bytes(), &payload) != nil || payload["readbackRecoveryProof"] == nil {
		t.Fatalf("terminal replay did not reconstruct persisted proof: %s", replay.Body.String())
	}
	if !equalStringSlices(*fixture.events, beforeWrites) {
		t.Fatalf("terminal replay repeated provider writes: before=%#v after=%#v", beforeWrites, *fixture.events)
	}

	otherKey := "recover-terminal-other"
	drifted := testWorkspaceLaunchReadbackApproval(t, scenario.approvalOperation, "secret", otherKey, scenario.readback)
	conflict := requestWorkspaceLaunchReadbackRecovery(t, fixture, drifted, otherKey)
	if conflict.Code != http.StatusConflict || !equalStringSlices(*fixture.events, beforeWrites) {
		t.Fatalf("different terminal approval status=%d body=%s events=%#v", conflict.Code, conflict.Body.String(), *fixture.events)
	}
}

func TestWorkspaceLaunchPersistedReadbackRecoveryReplayIsZeroWriteAfterExpiry(t *testing.T) {
	for _, status := range []string{"preparing", "waiting", "succeeded"} {
		t.Run(status, func(t *testing.T) {
			fixture, approval, key := persistedWorkspaceLaunchReadbackReplayFixture(t, status, true)
			beforeOperation := fixture.operation(t).PersistedResult
			beforeEvents := append([]string(nil), (*fixture.events)...)
			beforeAudits, err := fixture.store.ListAuditEvents(context.Background(), "acct-alpha")
			if err != nil {
				t.Fatal(err)
			}

			response := requestWorkspaceLaunchReadbackRecovery(t, fixture, approval, key)
			if response.Code != http.StatusOK {
				t.Fatalf("persisted %s replay status=%d body=%s", status, response.Code, response.Body.String())
			}
			var payload map[string]any
			if json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload["readbackRecoveryProof"] == nil || stringValue(payload["status"]) != status {
				t.Fatalf("persisted %s replay response=%s", status, response.Body.String())
			}
			afterAudits, err := fixture.store.ListAuditEvents(context.Background(), "acct-alpha")
			if err != nil {
				t.Fatal(err)
			}
			if fixture.operation(t).PersistedResult != beforeOperation || !equalStringSlices(*fixture.events, beforeEvents) || string(mustJSON(afterAudits)) != string(mustJSON(beforeAudits)) {
				t.Fatalf("persisted %s replay mutated state: events=%#v/%#v audits=%#v/%#v", status, beforeEvents, *fixture.events, beforeAudits, afterAudits)
			}
		})
	}
}

func TestWorkspaceLaunchPersistedReadbackRecoveryReplayRejectsIdentityDriftWithConflict(t *testing.T) {
	for _, test := range []struct {
		name      string
		mutate    func(map[string]any)
		headerKey func(string) string
	}{
		{name: "idempotency key", headerKey: func(string) string { return "recover-persisted-other" }},
		{name: "approval digest", mutate: func(approval map[string]any) { approval["approvalDigest"] = strings.Repeat("d", 64) }},
		{name: "target", mutate: func(approval map[string]any) {
			approval["target"].(map[string]any)["workspaceId"] = "ws-other"
			refreshWorkspaceLaunchReadbackApprovalDigest(t, approval)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, approval, key := persistedWorkspaceLaunchReadbackReplayFixture(t, "waiting", false)
			if test.mutate != nil {
				test.mutate(approval)
			}
			if test.headerKey != nil {
				key = test.headerKey(key)
			}
			beforeOperation := fixture.operation(t).PersistedResult
			beforeEvents := append([]string(nil), (*fixture.events)...)
			beforeAudits, _ := fixture.store.ListAuditEvents(context.Background(), "acct-alpha")
			response := requestWorkspaceLaunchReadbackRecovery(t, fixture, approval, key)
			afterAudits, _ := fixture.store.ListAuditEvents(context.Background(), "acct-alpha")
			if response.Code != http.StatusConflict || fixture.operation(t).PersistedResult != beforeOperation ||
				!equalStringSlices(*fixture.events, beforeEvents) || string(mustJSON(afterAudits)) != string(mustJSON(beforeAudits)) {
				t.Fatalf("%s drift status=%d body=%s events=%#v/%#v audits=%#v/%#v", test.name, response.Code, response.Body.String(), beforeEvents, *fixture.events, beforeAudits, afterAudits)
			}
		})
	}
}

func TestWorkspaceLaunchUnpersistedExpiredReadbackApprovalIsRejectedWithoutMutation(t *testing.T) {
	t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "workspace-launch-readback-capability")
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "secret", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	key := "recover-unpersisted-expired"
	approval := testWorkspaceLaunchReadbackApproval(t, scenario.approvalOperation, "secret", key, scenario.readback)
	approval["expiresAt"] = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	refreshWorkspaceLaunchReadbackApprovalDigest(t, approval)
	beforeOperation := fixture.operation(t).PersistedResult
	beforeEvents := append([]string(nil), (*fixture.events)...)
	beforeAudits, _ := fixture.store.ListAuditEvents(context.Background(), "acct-alpha")

	response := requestWorkspaceLaunchReadbackRecovery(t, fixture, approval, key)
	afterAudits, _ := fixture.store.ListAuditEvents(context.Background(), "acct-alpha")
	if response.Code != http.StatusConflict || fixture.operation(t).PersistedResult != beforeOperation ||
		!equalStringSlices(*fixture.events, beforeEvents) || string(mustJSON(afterAudits)) != string(mustJSON(beforeAudits)) {
		t.Fatalf("unpersisted expired approval status=%d body=%s events=%#v/%#v audits=%#v/%#v", response.Code, response.Body.String(), beforeEvents, *fixture.events, beforeAudits, afterAudits)
	}
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestWorkspaceLaunchResponseProjectsLatestReadbackApproval(t *testing.T) {
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "secret", "basic")
	operation := scenario.unknown
	operation.ComputeClaimApproval = &workspaceComputeClaimApprovalBinding{
		ApprovalID: "approval-old-compute", ApprovalDigest: strings.Repeat("a", 64), RecoveryKey: "recovery-old-compute",
		WorkspaceImageDigest: operation.WorkspaceImageDigest,
	}
	key := "recover-latest-readback"
	approval := parsedWorkspaceLaunchReadbackApproval(t, testWorkspaceLaunchReadbackApproval(t, scenario.approvalOperation, "secret", key, scenario.readback), key)
	operation.ReadbackRecoveryApproval = &approval
	response, err := workspaceLaunchResponse(workspaceLaunchOperationRow(operation))
	if err != nil {
		t.Fatal(err)
	}
	recovery := mapField(response, "recovery")
	if stringValue(recovery["approvalId"]) != approval.ApprovalID || stringValue(recovery["approvalDigest"]) != approval.ApprovalDigest ||
		stringValue(recovery["recoveryKey"]) != approval.RecoveryKey || stringValue(recovery["workspaceImageDigest"]) != approval.WorkspaceImageDigest {
		t.Fatalf("response projected stale compute approval: %#v", response)
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
	rawApproval := testWorkspaceLaunchReadbackApproval(t, approvalOperation, "secret", key, readback)
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
			computes, err := scenario.fixture.store.ListComputes(context.Background(), scenario.unknown.AccountID)
			if err != nil {
				t.Fatal(err)
			}
			for _, compute := range computes {
				if err := first.SaveCompute(context.Background(), compute); err != nil {
					t.Fatal(err)
				}
			}
			storages, err := scenario.fixture.store.ListStorages(context.Background(), scenario.unknown.AccountID)
			if err != nil {
				t.Fatal(err)
			}
			for _, storage := range storages {
				if err := first.SaveStorage(context.Background(), storage); err != nil {
					t.Fatal(err)
				}
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
			approval := parsedWorkspaceLaunchReadbackApproval(t, testWorkspaceLaunchReadbackApproval(t, scenario.approvalOperation, stage, key, scenario.readback), key)
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
