package server

import (
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
)

func validComputeClaimProofForEvaluator(t *testing.T) (workspaceLaunchOperation, workspaceComputeClaimRecoveryRequest, clients.ComputeClaimRecoveryProof) {
	t.Helper()
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	input := workspaceComputeClaimRequestFromOperation(operation)
	proof := computeClaimRecoveryProofForLaunchStorage(operation, "unallocated", "storage_not_started", "")
	// Keep the fixture's store alive until the caller has copied the operation;
	// the evaluator itself is pure and does not use the service.
	_ = fixture
	return operation, input, proof
}

func productionComputeClaimEvaluationFixture() (workspaceLaunchOperation, workspaceComputeClaimRecoveryRequest, clients.ComputeClaimRecoveryProof) {
	operation := workspaceLaunchOperation{
		ID: "workspace-launch-f4338141c25d0882b0", AccountID: "acct-54658088f52b242ed8", WorkspaceID: "ws-30e2861bbdf9805492",
		Status: "manual_review", Phase: "storage_fulfilling", PackageID: "basic",
		ComputeID: "ca_2968bc5edba23a5c36", StorageID: "vol_57f5d2a477b616e8c9",
		ComputePoolID: "pool-basic-2c4g", ComputeNodePoolID: "np-33sy1qqa", ComputeMachineName: "np-33sy1qqa-whp8b",
		ComputeNodeName: "10.66.0.191", ComputeCVMInstanceID: "ins-rjkoixhs", ComputePrivateIP: "10.66.0.191",
		ComputeInstanceType: "SA5.MEDIUM4", ComputeZone: "ap-shanghai-2", ComputeChargeType: "PREPAID",
		ComputeRenewFlag: "NOTIFY_AND_MANUAL_RENEW", ComputeDeadline: "2099-01-01T00:00:00Z",
		ContinuationAttemptBudgets: map[string]workspaceLaunchStageBudget{
			"storage": {Attempted: 1, Unknown: 1, Max: 1},
		},
		ComputeClaimApproval: &workspaceComputeClaimApprovalBinding{
			Resources: workspaceComputeClaimApprovalResources{StorageState: "storage_attempt_unknown"},
		},
	}
	input := workspaceComputeClaimRequestFromOperation(operation)
	proof := clients.ComputeClaimRecoveryProof{
		SchemaVersion: 1, Eligible: true, Reason: "none", StorageState: "storage_attempt_unknown",
		LaunchOperationID: operation.ID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
		ComputeAllocationID: operation.ComputeID, StorageVolumeID: operation.StorageID, PackageID: operation.PackageID,
		PoolID: operation.ComputePoolID, NodePoolID: operation.ComputeNodePoolID, MachineName: operation.ComputeMachineName,
		NodeName: operation.ComputeNodeName, CVMInstanceID: operation.ComputeCVMInstanceID, PrivateIP: operation.ComputePrivateIP,
		InstanceType: operation.ComputeInstanceType, Zone: operation.ComputeZone, ChargeType: "PREPAID", PeriodMonths: 1,
		RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: operation.ComputeDeadline,
		NodeOwnershipState: "unallocated", CVMOwnershipState: "target_owned", Evidence: &clients.ComputeClaimEvidence{},
	}
	return operation, input, proof
}

func TestEvaluateWorkspaceComputeClaimProofAcceptsCompleteEvidence(t *testing.T) {
	operation, input, proof := validComputeClaimProofForEvaluator(t)
	evaluation := evaluateWorkspaceComputeClaimProof(operation, input, proof, false)
	if !evaluation.Eligible || !evaluation.BaseMatches || evaluation.FirstFalsePredicate != "provider.nodeOwnership" ||
		evaluation.Expected != "target_owned" || evaluation.Actual != "unallocated" || evaluation.Authority != "provider.nodeOwnership" {
		t.Fatalf("complete proof was rejected: %#v", evaluation)
	}
}

func TestEvaluateWorkspaceComputeClaimProofReportsEveryFailureCondition(t *testing.T) {
	proofOnly := func(fn func(*clients.ComputeClaimRecoveryProof)) func(*workspaceLaunchOperation, *workspaceComputeClaimRecoveryRequest, *clients.ComputeClaimRecoveryProof) {
		return func(_ *workspaceLaunchOperation, _ *workspaceComputeClaimRecoveryRequest, proof *clients.ComputeClaimRecoveryProof) {
			fn(proof)
		}
	}
	inputOnly := func(fn func(*workspaceComputeClaimRecoveryRequest)) func(*workspaceLaunchOperation, *workspaceComputeClaimRecoveryRequest, *clients.ComputeClaimRecoveryProof) {
		return func(_ *workspaceLaunchOperation, input *workspaceComputeClaimRecoveryRequest, _ *clients.ComputeClaimRecoveryProof) {
			fn(input)
		}
	}
	tests := []struct {
		name      string
		claimed   bool
		mutate    func(*workspaceLaunchOperation, *workspaceComputeClaimRecoveryRequest, *clients.ComputeClaimRecoveryProof)
		predicate string
	}{
		{"schema", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.SchemaVersion = 2 }), "provider.proofSchemaVersion"},
		{"launch operation", false, func(operation *workspaceLaunchOperation, _ *workspaceComputeClaimRecoveryRequest, proof *clients.ComputeClaimRecoveryProof) {
			proof.LaunchOperationID = operation.ID + "-other"
		}, "provider.launchOperationId"},
		{"account", false, func(operation *workspaceLaunchOperation, _ *workspaceComputeClaimRecoveryRequest, proof *clients.ComputeClaimRecoveryProof) {
			proof.AccountID = operation.AccountID + "-other"
		}, "provider.accountId"},
		{"workspace", false, func(operation *workspaceLaunchOperation, _ *workspaceComputeClaimRecoveryRequest, proof *clients.ComputeClaimRecoveryProof) {
			proof.WorkspaceID = operation.WorkspaceID + "-other"
		}, "provider.workspaceId"},
		{"compute allocation", false, func(operation *workspaceLaunchOperation, _ *workspaceComputeClaimRecoveryRequest, proof *clients.ComputeClaimRecoveryProof) {
			proof.ComputeAllocationID = operation.ComputeID + "-other"
		}, "provider.computeAllocationId"},
		{"storage volume", false, func(operation *workspaceLaunchOperation, _ *workspaceComputeClaimRecoveryRequest, proof *clients.ComputeClaimRecoveryProof) {
			proof.StorageVolumeID = operation.StorageID + "-other"
		}, "provider.storageVolumeId"},
		{"package", false, func(operation *workspaceLaunchOperation, _ *workspaceComputeClaimRecoveryRequest, proof *clients.ComputeClaimRecoveryProof) {
			proof.PackageID = operation.PackageID + "-other"
		}, "provider.packageId"},
		{"pool", false, func(_ *workspaceLaunchOperation, input *workspaceComputeClaimRecoveryRequest, proof *clients.ComputeClaimRecoveryProof) {
			proof.PoolID = input.PoolID + "-other"
		}, "provider.poolId"},
		{"node pool", false, func(operation *workspaceLaunchOperation, _ *workspaceComputeClaimRecoveryRequest, proof *clients.ComputeClaimRecoveryProof) {
			proof.NodePoolID = operation.ComputeNodePoolID + "-other"
		}, "provider.nodePoolId"},
		{"sub2api mutation", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.Sub2APIMutationCount = 1 }), "provider.sub2apiMutationCount"},
		{"reason allowlist", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.Reason = "unexpected" }), "provider.proofReasonAllowlisted"},
		{"evidence absent", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.Evidence = nil }), "provider.mutationEvidence"},
		{"failure stage allowlist", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.FailureStage = "unexpected" }), "provider.failureStageAllowlisted"},
		{"provider error class allowlist", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.ProviderErrorClass = "unexpected" }), "provider.errorClassAllowlisted"},
		{"cvm mutation bounds", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.Evidence.CVM.Attempted = 1 }), "provider.cvmMutationEvidence"},
		{"node mutation bounds", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.Evidence.Node.Attempted = 1 }), "provider.nodeMutationEvidence"},
		{"eligible flag", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.Eligible = false }), "provider.proofEligible"},
		{"reason", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.Reason = "storage_already_started" }), "provider.proofReason"},
		{"storage binding", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.StorageState = "invalid" }), "provider.storageBinding"},
		{"storage approval binding", false, inputOnly(func(input *workspaceComputeClaimRecoveryRequest) {
			input.Resources.StorageState, input.Resources.StorageProviderResourceID = "storage_existing_exact", "disk-approved"
		}), "provider.storageApprovalBinding"},
		{"machine", false, func(_ *workspaceLaunchOperation, input *workspaceComputeClaimRecoveryRequest, proof *clients.ComputeClaimRecoveryProof) {
			proof.MachineName = input.MachineName + "-other"
		}, "provider.machineIdentity"},
		{"node", false, func(_ *workspaceLaunchOperation, input *workspaceComputeClaimRecoveryRequest, proof *clients.ComputeClaimRecoveryProof) {
			proof.NodeName = input.NodeName + "-other"
		}, "provider.nodeIdentity"},
		{"cvm", false, func(_ *workspaceLaunchOperation, input *workspaceComputeClaimRecoveryRequest, proof *clients.ComputeClaimRecoveryProof) {
			proof.CVMInstanceID = input.CVMInstanceID + "-other"
		}, "provider.cvmIdentity"},
		{"private ip", false, func(_ *workspaceLaunchOperation, input *workspaceComputeClaimRecoveryRequest, proof *clients.ComputeClaimRecoveryProof) {
			proof.PrivateIP = input.PrivateIP + "-other"
		}, "provider.privateIpIdentity"},
		{"instance type", false, func(_ *workspaceLaunchOperation, input *workspaceComputeClaimRecoveryRequest, proof *clients.ComputeClaimRecoveryProof) {
			proof.InstanceType = input.InstanceType + "-other"
		}, "provider.instanceType"},
		{"zone", false, func(_ *workspaceLaunchOperation, input *workspaceComputeClaimRecoveryRequest, proof *clients.ComputeClaimRecoveryProof) {
			proof.Zone = input.Zone + "-other"
		}, "provider.zone"},
		{"charge type", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.ChargeType = "POSTPAID" }), "provider.chargeType"},
		{"period", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.PeriodMonths = 2 }), "provider.periodMonths"},
		{"renew flag", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.RenewFlag = "AUTO_RENEW" }), "provider.renewFlag"},
		{"deadline parse", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.Deadline = "not-a-deadline" }), "provider.deadlineParse"},
		{"deadline equality", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.Deadline = "2099-01-02T00:00:00Z" }), "provider.deadlineEquality"},
		{"confirmed mutation evidence", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) {
			proof.TencentMutationCount = 1
			proof.Evidence.CVM = clients.ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1}
		}), "provider.confirmedMutationEvidence"},
		{"failure stage", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.FailureStage = "cvm_pre_read" }), "provider.failureStage"},
		{"provider error class", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.ProviderErrorClass = "provider_error" }), "provider.errorClass"},
		{"candidate node ownership", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.NodeOwnershipState = "conflict" }), "provider.nodeOwnership"},
		{"candidate cvm ownership", false, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.CVMOwnershipState = "unknown" }), "provider.cvmOwnership"},
		{"claimed node ownership", true, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) { proof.NodeOwnershipState = "unallocated" }), "provider.nodeOwnership"},
		{"claimed cvm ownership", true, proofOnly(func(proof *clients.ComputeClaimRecoveryProof) {
			proof.NodeOwnershipState, proof.CVMOwnershipState = "target_owned", "recoverable"
		}), "provider.cvmOwnership"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			operation, input, proof := validComputeClaimProofForEvaluator(t)
			tc.mutate(&operation, &input, &proof)
			evaluation := evaluateWorkspaceComputeClaimProof(operation, input, proof, tc.claimed)
			if evaluation.Eligible || evaluation.FirstFalsePredicate != tc.predicate {
				t.Fatalf("evaluation=%#v want predicate=%s", evaluation, tc.predicate)
			}
			decision := currentDecisionForComputeClaimEvaluation(operation, nil, evaluation)
			if decision.AllowedMutation != "none" || AuthorizeStageMutation(decision, "node_only_continuation") {
				t.Fatalf("failed evaluator authorized mutation: evaluation=%#v decision=%#v", evaluation, decision)
			}
		})
	}
}

func TestWorkspaceComputeClaimTraceProjectsEvaluatorWithoutReimplementingIt(t *testing.T) {
	operation, input, proof := validComputeClaimProofForEvaluator(t)
	evaluation := evaluateWorkspaceComputeClaimProof(operation, input, proof, false)
	projection := workspaceComputeClaimTraceProofEligibility(evaluation)
	if projection["eligible"] != evaluation.Eligible || projection["firstFalsePredicate"] != evaluation.FirstFalsePredicate ||
		projection["expected"] != evaluation.Expected || projection["actual"] != evaluation.Actual || projection["authority"] != evaluation.Authority {
		t.Fatalf("trace projection=%#v evaluation=%#v", projection, evaluation)
	}
}

func TestComputeClaimEvaluationContractFeedsCurrentDecisionAndMutationBudget(t *testing.T) {
	operation, input, proof := productionComputeClaimEvaluationFixture()
	evaluation := evaluateWorkspaceComputeClaimProof(operation, input, proof, false)
	if !evaluation.Eligible {
		t.Fatalf("safe continuation proof rejected: %#v", evaluation)
	}
	decision := currentDecisionForComputeClaimEvaluation(operation, nil, evaluation)
	if decision.CurrentStage != "compute_claim" || decision.StageState != "pending" ||
		decision.FirstFalsePredicate != "provider.nodeOwnership" || decision.Expected != "target_owned" ||
		decision.Actual != "unallocated" || decision.Authority != "provider.nodeOwnership" ||
		decision.NextAction != nextActionNodeOnlyContinuation || !AuthorizeStageMutation(decision, "node_only_continuation") {
		t.Fatalf("continuation decision=%#v", decision)
	}
	if decision.MutationState != "pending" || decision.AllowedMutation != "node_only_continuation" {
		t.Fatalf("unexpected mutation authorization=%#v", decision)
	}
	projection := workspaceComputeClaimTraceProofEligibility(evaluation)
	if projection["firstFalsePredicate"] != decision.FirstFalsePredicate || projection["expected"] != decision.Expected ||
		projection["actual"] != decision.Actual || projection["authority"] != decision.Authority {
		t.Fatalf("trace diverged from evaluator decision: projection=%#v decision=%#v", projection, decision)
	}
	evidence := recoverableCVMOnlyIdentityEvidence()
	plan, err := newWorkspaceComputeClaimRecoveryPlan(operation, input, proof, evaluation, &evidence, workspaceRecoveryReleaseBinding{
		MainSHA:              "0123456789012345678901234567890123456789",
		CloudImageDigest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		WorkspaceImageDigest: operation.WorkspaceImageDigest,
	})
	if err != nil || plan.DecisionBinding.MutationBudget != (workspaceRecoveryMutationCounts{Kubernetes: 1}) {
		t.Fatalf("node-only write set escaped budget: plan=%#v err=%v", plan, err)
	}
	if plan.DecisionBinding.CurrentStage != decision.CurrentStage || plan.DecisionBinding.EvidenceDigest != decision.EvidenceDigest ||
		plan.DecisionBinding.DecisionVersion != decision.DecisionVersion || plan.DecisionBinding.AllowedMutation != decision.AllowedMutation {
		t.Fatalf("Recovery diverged from canonical evaluation: plan=%#v decision=%#v", plan.DecisionBinding, decision)
	}

	proof.StorageState, proof.StorageProviderResourceID = "storage_existing_exact", "disk-approved"
	input.Resources.StorageState, input.Resources.StorageProviderResourceID = "storage_not_started", ""
	evaluation = evaluateWorkspaceComputeClaimProof(operation, input, proof, false)
	decision = currentDecisionForComputeClaimEvaluation(operation, nil, evaluation)
	if evaluation.Eligible || decision.CurrentStage != "compute_claim" || AuthorizeStageMutation(decision, "node_only_continuation") {
		t.Fatalf("failed evaluator authorized mutation: evaluation=%#v decision=%#v", evaluation, decision)
	}
	if decision.FirstFalsePredicate != evaluation.FirstFalsePredicate || decision.Expected != evaluation.Expected ||
		decision.Actual != evaluation.Actual || decision.Authority != evaluation.Authority {
		t.Fatalf("decision diverged from evaluator: evaluation=%#v decision=%#v", evaluation, decision)
	}
	projection = workspaceComputeClaimTraceProofEligibility(evaluation)
	if projection["firstFalsePredicate"] != decision.FirstFalsePredicate || projection["expected"] != decision.Expected ||
		projection["actual"] != decision.Actual || projection["authority"] != decision.Authority {
		t.Fatalf("trace diverged from evaluator decision: projection=%#v decision=%#v", projection, decision)
	}
}

func TestCurrentDecisionConsumesPersistedEvaluationInsteadOfRawProof(t *testing.T) {
	operation, input, proof := validComputeClaimProofForEvaluator(t)
	proof.NodeOwnershipState, proof.CVMOwnershipState = "unallocated", "target_owned"
	evaluation := evaluateWorkspaceComputeClaimProof(operation, input, proof, false)
	if !evaluation.Eligible {
		t.Fatalf("continuation proof rejected: %#v", evaluation)
	}

	// A caller may retain or enrich the raw provider response after evaluation;
	// the persisted decision must remain the decision for that exact evaluation.
	proof.NodeOwnershipState = "target_owned"
	decision := currentDecisionForComputeClaimEvaluation(operation, nil, evaluation)
	if decision.CurrentStage != "compute_claim" || decision.NextAction != nextActionNodeOnlyContinuation ||
		decision.FirstFalsePredicate != "provider.nodeOwnership" || decision.Expected != "target_owned" ||
		decision.Actual != "unallocated" || decision.Authority != "provider.nodeOwnership" ||
		!AuthorizeStageMutation(decision, "node_only_continuation") {
		t.Fatalf("CurrentDecision reinterpreted raw proof: decision=%#v evaluation=%#v", decision, evaluation)
	}
}
