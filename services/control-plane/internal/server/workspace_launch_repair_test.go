package server

import (
	"context"
	"reflect"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const workspaceLaunchRepairImage = "registry.example/opl/workspace@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

type workspaceLaunchRepairFabric struct {
	fakeFabricClient
	repairs         int
	inputs          []clients.WorkspaceRuntimeInput
	idempotencyKeys []string
}

func (f *workspaceLaunchRepairFabric) RepairWorkspaceRuntime(_ context.Context, input clients.WorkspaceRuntimeInput, idempotencyKey string) (clients.WorkspaceRuntime, error) {
	f.repairs++
	f.inputs = append(f.inputs, input)
	f.idempotencyKeys = append(f.idempotencyKeys, idempotencyKey)
	return clients.WorkspaceRuntime{
		ID: "runtime-repaired", OperationID: input.RuntimeOperationID, WorkspaceID: input.WorkspaceID,
		URL: "https://workspace.example/repaired", Status: "running", ServiceName: "runtime-repaired",
		ImageID: input.ImageID, Ready: true,
		Access: clients.WorkspaceRuntimeAccess{
			Username: "opl", CredentialStatus: "configured", CredentialVersion: "v2", SecretRef: "runtime-repaired-env",
		},
	}, nil
}

type workspaceLaunchRepairLedger struct {
	fakeLedgerClient
	records  int
	receipts map[string]clients.Receipt
}

func (l *workspaceLaunchRepairLedger) RecordReceipt(_ context.Context, input clients.ReceiptInput, key string) (clients.Receipt, error) {
	l.records++
	if receipt, ok := l.receipts[key]; ok {
		return receipt, nil
	}
	receipt := clients.Receipt{ReceiptInput: input, ReceiptID: "receipt-runtime-repair"}
	l.receipts[key] = receipt
	return receipt, nil
}

func (l *workspaceLaunchRepairLedger) ListReceipts(_ context.Context, query clients.ReceiptQuery) (clients.ReceiptPage, error) {
	page := clients.ReceiptPage{}
	for _, receipt := range l.receipts {
		if receipt.AccountID == query.AccountID {
			page.Receipts = append(page.Receipts, receipt)
		}
	}
	return page, nil
}

func workspaceLaunchRuntimeRepairFixture(t *testing.T) workspaceLaunchReconcileOperation {
	t.Helper()
	operation, err := newWorkspaceLaunchReconcileOperation(workspaceLaunchUnitCommand())
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"key", "debit", "ensure_compute_allocation", "storage", "attachment", "secret"} {
		operation.Stage = stage
		facts := workspaceLaunchReadyFacts(stage)
		if stage == "debit" {
			facts["periodStart"] = "2026-08-12T00:00:00Z"
			facts["paidThrough"] = "2026-09-12T00:00:00Z"
			facts["billingAnchorDay"] = int64(12)
		}
		observation, reduceErr := reduceWorkspaceLaunchStageObservation(&operation, workspaceLaunchStageObservation{
			State: workspaceLaunchStageReady, Facts: facts,
		})
		if reduceErr != nil {
			t.Fatalf("prepare %s facts: %v", stage, reduceErr)
		}
		attempt := operation.Attempts[stage]
		attempt.Attempted, attempt.Confirmed, attempt.Unknown, attempt.Status = 1, 1, 0, "confirmed"
		attempt.IdempotencyKey = workspaceLaunchStageIdempotencyKey(operation, 1)
		operation.Attempts[stage], operation.Observations[stage] = attempt, observation
	}
	operation.Stage, operation.Status = "runtime", "manual_review"
	runtimeAttempt := operation.Attempts["runtime"]
	runtimeAttempt.Attempted, runtimeAttempt.Confirmed, runtimeAttempt.Unknown, runtimeAttempt.Status = 1, 0, 1, "unknown"
	runtimeAttempt.IdempotencyKey = workspaceLaunchStageIdempotencyKey(operation, 1)
	operation.Attempts["runtime"] = runtimeAttempt
	operation.Observations["runtime"] = workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}
	return operation
}

func cloneWorkspaceLaunchRuntimeRepairFixture(t *testing.T, operation workspaceLaunchReconcileOperation) workspaceLaunchReconcileOperation {
	t.Helper()
	row, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	return cloned
}

func TestWorkspaceLaunchRuntimeRepairEligibility(t *testing.T) {
	base := workspaceLaunchRuntimeRepairFixture(t)
	if !workspaceLaunchRuntimeRepairEligible(base) {
		t.Fatal("canonical paid Runtime failure is not eligible for repair")
	}

	for _, stage := range []string{"key", "debit", "ensure_compute_allocation", "storage", "attachment", "secret"} {
		t.Run(stage+" must be confirmed", func(t *testing.T) {
			operation := cloneWorkspaceLaunchRuntimeRepairFixture(t, base)
			attempt := operation.Attempts[stage]
			attempt.Confirmed, attempt.Status = 0, "unknown"
			operation.Attempts[stage] = attempt
			if workspaceLaunchRuntimeRepairEligible(operation) {
				t.Fatalf("repair accepted unconfirmed %s", stage)
			}
		})
		t.Run(stage+" must be ready", func(t *testing.T) {
			operation := cloneWorkspaceLaunchRuntimeRepairFixture(t, base)
			operation.Observations[stage] = workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}
			if workspaceLaunchRuntimeRepairEligible(operation) {
				t.Fatalf("repair accepted non-ready %s", stage)
			}
		})
	}

	tests := []struct {
		name   string
		mutate func(*workspaceLaunchReconcileOperation)
	}{
		{name: "wrong launch status", mutate: func(operation *workspaceLaunchReconcileOperation) { operation.Status = "pending" }},
		{name: "wrong launch stage", mutate: func(operation *workspaceLaunchReconcileOperation) { operation.Stage = "secret" }},
		{name: "runtime not unknown", mutate: func(operation *workspaceLaunchReconcileOperation) {
			attempt := operation.Attempts["runtime"]
			attempt.Confirmed, attempt.Unknown, attempt.Status = 1, 0, "confirmed"
			operation.Attempts["runtime"] = attempt
		}},
		{name: "runtime observation not unknown", mutate: func(operation *workspaceLaunchReconcileOperation) {
			operation.Observations["runtime"] = workspaceLaunchStageObservation{State: workspaceLaunchStageReady, Facts: workspaceLaunchReadyFacts("runtime")}
		}},
		{name: "activation already attempted", mutate: func(operation *workspaceLaunchReconcileOperation) {
			attempt := operation.Attempts["activation"]
			attempt.Attempted, attempt.Status = 1, "reserved"
			operation.Attempts["activation"] = attempt
		}},
		{name: "receipt already observed", mutate: func(operation *workspaceLaunchReconcileOperation) {
			operation.Observations["receipt"] = workspaceLaunchStageObservation{State: workspaceLaunchStageReady, Facts: workspaceLaunchReadyFacts("receipt")}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation := cloneWorkspaceLaunchRuntimeRepairFixture(t, base)
			test.mutate(&operation)
			if workspaceLaunchRuntimeRepairEligible(operation) {
				t.Fatal("repair accepted an ineligible launch")
			}
		})
	}
}

func TestWorkspaceLaunchRuntimeRepairConvergesAndReplaysExactlyOnce(t *testing.T) {
	ctx := context.Background()
	operation := workspaceLaunchRuntimeRepairFixture(t)
	launchVersion := operation.Version
	row, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	store := &workspaceLaunchActivationCountingStore{memoryTableStore: newMemoryTableStore()}
	store.users["usr-unit"] = map[string]any{"id": "usr-unit", "accountId": "acct-unit", "role": "owner", "status": "active"}
	store.runtimeOps = []map[string]any{row}
	fabricCalls := []string{}
	fabric := &workspaceLaunchRepairFabric{fakeFabricClient: fakeFabricClient{calls: &fabricCalls}}
	ledger := &workspaceLaunchRepairLedger{receipts: map[string]clients.Receipt{}}
	service := controlplane.NewService(ledger, fabric)
	app := &controlPlaneServer{tables: store}

	got, err := app.repairWorkspaceLaunchRuntime(ctx, service, operation.ID, launchVersion, "repair-auth-unit", "usr-unit", "replace incompatible runtime image", workspaceLaunchRepairImage)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "succeeded" || got.Stage != "succeeded" {
		t.Fatalf("repair terminal state = %s/%s, want succeeded/succeeded", got.Status, got.Stage)
	}
	wantInput := clients.WorkspaceRuntimeInput{
		AccountID: "acct-unit", WorkspaceID: "ws-unit", ComputeID: "ca-unit", VolumeID: "vol-unit",
		AttachmentID: "att-unit", AttachmentOperationID: "workspace-launch-unit:attachment",
		RuntimeOperationID:         "workspace-launch-unit:runtime-repair:repair-auth-unit:create",
		PreviousRuntimeOperationID: "workspace-launch-unit:runtime", ImageID: workspaceLaunchRepairImage, GatewaySecretRef: "secret-unit",
	}
	if fabric.repairs != 1 || len(fabric.inputs) != 1 || !reflect.DeepEqual(fabric.inputs[0], wantInput) ||
		len(fabric.idempotencyKeys) != 1 || fabric.idempotencyKeys[0] != "workspace-launch-unit:runtime-repair:repair-auth-unit" {
		t.Fatalf("repair mutation calls=%d inputs=%#v keys=%#v", fabric.repairs, fabric.inputs, fabric.idempotencyKeys)
	}
	if store.activationMutations != 1 || ledger.records != 1 || len(ledger.receipts) != 1 {
		t.Fatalf("downstream mutations activation=%d receipt calls=%d receipts=%d", store.activationMutations, ledger.records, len(ledger.receipts))
	}
	if len(fabricCalls) != 0 {
		t.Fatalf("repair repeated an earlier Fabric stage: %v", fabricCalls)
	}
	for _, stage := range []string{"key", "debit", "ensure_compute_allocation", "storage", "attachment", "secret", "runtime", "activation", "receipt"} {
		attempt := got.Attempts[stage]
		if attempt.Attempted != 1 || attempt.Confirmed != 1 || attempt.Unknown != 0 || attempt.Status != "confirmed" {
			t.Fatalf("%s attempt after repair = %#v", stage, attempt)
		}
	}

	replayed, err := app.repairWorkspaceLaunchRuntime(ctx, service, operation.ID, launchVersion, "repair-auth-unit", "usr-unit", "replace incompatible runtime image", workspaceLaunchRepairImage)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Status != got.Status || replayed.Stage != got.Stage || replayed.PersistedResult != got.PersistedResult {
		t.Fatalf("exact replay changed terminal result: first=%s replay=%s", workspaceLaunchReconcileResultSummary(got), workspaceLaunchReconcileResultSummary(replayed))
	}
	if fabric.repairs != 1 || store.activationMutations != 1 || ledger.records != 1 || len(ledger.receipts) != 1 || len(fabricCalls) != 0 {
		t.Fatalf("exact replay mutated state: repairs=%d activation=%d receipt calls=%d receipts=%d earlier fabric=%v",
			fabric.repairs, store.activationMutations, ledger.records, len(ledger.receipts), fabricCalls)
	}
	if got.RuntimeRepair == nil || got.RuntimeRepair.AuthorizedBy != "usr-unit" {
		t.Fatalf("repair audit actor missing: %#v", got.RuntimeRepair)
	}
	if _, err := time.Parse(time.RFC3339Nano, got.RuntimeRepair.AuthorizedAt); err != nil {
		t.Fatalf("repair audit timestamp invalid: %#v err=%v", got.RuntimeRepair, err)
	}
}
