package fabric

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func seedTencentOperatorIdentityEvidence(t *testing.T) (*Service, *MemoryOperationStore, ComputeClaimRecoveryClaimInput) {
	t.Helper()
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	input := ComputeClaimRecoveryInput{
		LaunchOperationID: "workspace-launch-operator", AccountID: "acct-operator", WorkspaceID: "ws-operator",
		ComputeAllocationID: "ca_operator", StorageVolumeID: "vol_operator", PackageID: "basic",
		PoolID: "pool-basic-2c4g", NodePoolID: "np-workspace-basic",
	}
	plan := ComputeAllocationPreparation{
		PoolID: input.PoolID, PackageID: input.PackageID, NodePoolID: input.NodePoolID, InstanceType: "SA5.MEDIUM4",
		MaxReplicas: 20, BaselineReplicas: 1, TargetReplicas: 2, BeforeMachineNames: []string{"machine-before"},
	}
	allocation := ComputeAllocation{
		ID: input.ComputeAllocationID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID,
		Status: "quarantined", Provider: "tencent-tke", ProviderResourceID: "ins-operator", PoolID: input.PoolID, NodePoolID: input.NodePoolID,
		MachineName: "machine-operator", InstanceID: "ins-operator", CVMInstanceID: "ins-operator", NodeName: "10.0.0.18", PrivateIP: "10.0.0.18",
		InstanceType: plan.InstanceType, Zone: "ap-guangzhou-3", ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW",
		Deadline: "2026-09-12T00:00:00Z", ProviderData: map[string]string{
			"instanceType": plan.InstanceType, "zone": "ap-guangzhou-3", "chargeType": "PREPAID", "periodMonths": "1",
			"renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-09-12T00:00:00Z", "machineName": "machine-operator",
		},
		CreatedAt: now,
	}
	ownership := MachineOwnership{
		ID: "owner-operator", ResourceID: allocation.ID, AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID,
		PackageID: allocation.PackageID, NodePoolID: allocation.NodePoolID, MachineID: allocation.MachineName,
		InstanceID: allocation.InstanceID, NodeName: allocation.NodeName, Status: "quarantined", ClaimedAt: now,
	}
	computeInput := ComputeAllocationInput{
		ID: allocation.ID, AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID, PackageID: allocation.PackageID,
		NodePoolID: allocation.NodePoolID, IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	operation := newOperation(
		"create_compute_allocation", "compute_allocation", allocation.ID, allocation.AccountID, allocation.WorkspaceID,
		computeInput.IdempotencyKey, hashInput(computeInput), now,
	)
	operation.ID, operation.Status = "fop-operator", "claim_pending"
	operation.RedactedProviderPayload = computeAllocationOperationPayload(allocation, plan)
	store := NewMemoryOperationStore()
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimMachine(context.Background(), ownership); err != nil || !claimed {
		t.Fatalf("claim ownership: claimed=%v err=%v", claimed, err)
	}
	claim := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: allocation.MachineName, NodeName: allocation.NodeName,
		CVMInstanceID: allocation.InstanceID, PrivateIP: allocation.PrivateIP, InstanceType: allocation.InstanceType,
		Zone: allocation.Zone, IdempotencyKey: computeInput.IdempotencyKey,
	}
	return NewServiceWithOperationStore(testProvider{}, store), store, claim
}

func TestTencentOperatorIdentityEvidenceClassifiesHistoricalBindingWithoutMutation(t *testing.T) {
	service, store, input := seedTencentOperatorIdentityEvidence(t)
	store.mu.Lock()
	operation := store.operation[0]
	operation.RedactedProviderPayload = withComputeClaimRecoveryBinding(operation.RedactedProviderPayload, historicalComputeClaimRecoveryBinding(input))
	store.operation[0] = operation
	store.mu.Unlock()

	before, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := service.ComputeClaimRecoveryIdentityEvidence(context.Background(), input)
	after, listErr := store.List(context.Background())
	if err != nil || listErr != nil || evidence == nil || evidence.BindingClassification != "compute-claim" ||
		evidence.MutationLedger != "absent" || evidence.MutationLedgerOutcome != "confirmed_zero" || !reflect.DeepEqual(before, after) {
		t.Fatalf("evidence=%#v err=%v before=%#v after=%#v listErr=%v", evidence, err, before, after, listErr)
	}
}

func TestTencentOperatorIdentityEvidenceProjectsHistoricalLedgerWithoutMutation(t *testing.T) {
	service, store, input := seedTencentOperatorIdentityEvidence(t)
	ledger := computeClaimRecoveryMutationLedger{
		State: "observed", Reason: "none", TencentMutationCount: 1, KubernetesMutationCount: 1,
		Evidence: ComputeClaimEvidence{
			CVM:  ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
			Node: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
		},
	}
	store.mu.Lock()
	operation := store.operation[0]
	operation.RedactedProviderPayload = withComputeClaimRecoveryBinding(operation.RedactedProviderPayload, historicalComputeClaimRecoveryBinding(input))
	operation.RedactedProviderPayload = withComputeClaimRecoveryMutation(operation.RedactedProviderPayload, ledger)
	store.operation[0] = operation
	store.mu.Unlock()

	before, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := service.ComputeClaimRecoveryIdentityEvidence(context.Background(), input)
	after, listErr := store.List(context.Background())
	if err != nil || listErr != nil || evidence == nil || evidence.MutationLedger != "observed" ||
		evidence.MutationLedgerOutcome != "nonzero" || evidence.MutationEvidence == nil || !reflect.DeepEqual(*evidence.MutationEvidence, ledger.Evidence) ||
		!reflect.DeepEqual(before, after) {
		t.Fatalf("evidence=%#v err=%v before=%#v after=%#v listErr=%v", evidence, err, before, after, listErr)
	}
}
