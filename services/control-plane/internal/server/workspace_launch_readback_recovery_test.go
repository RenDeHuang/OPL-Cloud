package server

import (
	"bytes"
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
	operations    []clients.FabricOperation
	operationsErr error
	ownership     clients.MachineOwnership
	ownershipErr  error
}

func (f *workspaceLaunchReadbackRecoveryFabric) ListOperations(_ context.Context) ([]clients.FabricOperation, error) {
	f.record("fabric.operations")
	return append([]clients.FabricOperation(nil), f.operations...), f.operationsErr
}

func (f *workspaceLaunchReadbackRecoveryFabric) MachineOwnership(_ context.Context, resourceID string) (clients.MachineOwnership, error) {
	f.record("fabric.machine-ownership")
	if f.ownership.ResourceID != resourceID {
		return clients.MachineOwnership{}, errors.New("machine ownership not found")
	}
	return f.ownership, f.ownershipErr
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
	fixture := newWorkspaceLaunchWorkerFixtureForPlan(t, []int64{1_000_000_000, 1_000_000_000, 1_000_000_000 - charge}, nil, nil, packageID, storageGB, false)
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
