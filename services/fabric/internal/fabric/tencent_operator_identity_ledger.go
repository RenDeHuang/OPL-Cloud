package fabric

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"reflect"
	"time"
)

type computeClaimRecoveryBinding struct {
	LaunchOperationID string `json:"launchOperationId"`
	IdempotencyKey    string `json:"idempotencyKey"`
	TargetHash        string `json:"targetHash"`
	RequestHash       string `json:"requestHash"`
}

const (
	computeClaimRecoveryMutationPayloadKey       = "computeClaimRecoveryMutation"
	computeClaimRecoveryReconciliationPayloadKey = "computeClaimRecoveryReconciliation"
	computeClaimTerminalEvidencePayloadKey       = "computeClaimTerminalEvidence"
	computeClaimNodeClientRejectionPayloadKey    = "computeClaimNodeClientRejectionRecovery"
)

type computeClaimRecoveryReconciliation struct {
	SchemaVersion              int                          `json:"schemaVersion"`
	Consumer                   string                       `json:"consumer"`
	Generation                 string                       `json:"generation"`
	ProvenanceSource           string                       `json:"provenanceSource,omitempty"`
	ProvenanceDigest           string                       `json:"provenanceDigest,omitempty"`
	State                      string                       `json:"state"`
	BindingDigest              string                       `json:"bindingDigest"`
	ExpectedRequestHashDigest  string                       `json:"expectedRequestHashDigest"`
	PersistedRequestHashDigest string                       `json:"persistedRequestHashDigest"`
	MutationLedgerDigest       string                       `json:"mutationLedgerDigest"`
	AuthorityDigest            string                       `json:"authorityDigest"`
	FailureStage               string                       `json:"failureStage,omitempty"`
	ProviderErrorClass         string                       `json:"providerErrorClass,omitempty"`
	Node                       ComputeClaimMutationEvidence `json:"node"`
}

type computeClaimNodeClientRejectionRecovery struct {
	SchemaVersion              int    `json:"schemaVersion"`
	Classification             string `json:"classification"`
	Invocation                 string `json:"invocation"`
	RecordedCalls              int    `json:"recordedCalls"`
	APIAcceptedMutations       int    `json:"apiAcceptedMutations"`
	SourceReconciliationDigest string `json:"sourceReconciliationDigest"`
}

type computeClaimRecoveryReconciliationProvenance struct {
	SchemaVersion int
	Generation    string
	Source        string
	Digest        string
}

type computeClaimRecoveryMutationLedger struct {
	Generation              string               `json:"generation,omitempty"`
	AttemptDigest           string               `json:"attemptDigest,omitempty"`
	State                   string               `json:"state"`
	Reason                  string               `json:"reason"`
	TencentMutationCount    int                  `json:"tencentMutationCount"`
	KubernetesMutationCount int                  `json:"kubernetesMutationCount"`
	FailureStage            string               `json:"failureStage,omitempty"`
	ProviderErrorClass      string               `json:"providerErrorClass,omitempty"`
	Evidence                ComputeClaimEvidence `json:"evidence"`
}

const confirmedNodeDriftGeneration = "normal_launch_confirmed_node_drift_v1"

func reservedConfirmedNodeDriftMutation(attemptDigest string) computeClaimRecoveryMutationLedger {
	return computeClaimRecoveryMutationLedger{
		Generation: confirmedNodeDriftGeneration, AttemptDigest: attemptDigest,
		State: "node_reserved", Reason: "confirmed_node_drift", TencentMutationCount: 0, KubernetesMutationCount: 1,
		FailureStage: "node_patch_readback", ProviderErrorClass: "transport_error",
		Evidence: ComputeClaimEvidence{Node: ComputeClaimMutationEvidence{
			Attempted: 1, Unknown: 1, Missing: []string{"node_ownership"},
		}},
	}
}

func reservedComputeClaimRecoveryMutation() computeClaimRecoveryMutationLedger {
	return computeClaimRecoveryMutationLedger{
		State: "reserved", Reason: "provider_describe", TencentMutationCount: 5, KubernetesMutationCount: 1,
		FailureStage: "cvm_provisioner_transport", ProviderErrorClass: "transport_error",
		Evidence: ComputeClaimEvidence{
			CVM: ComputeClaimMutationEvidence{
				Attempted: 5, Unknown: 5,
				Missing: []string{"instance_name", "opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id"},
			},
			Node: ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"node_ownership"}},
		},
	}
}

func nodeReservedComputeClaimRecoveryMutation(observed computeClaimRecoveryMutationLedger) computeClaimRecoveryMutationLedger {
	return computeClaimRecoveryMutationLedger{
		State: "node_reserved", Reason: "provider_describe", TencentMutationCount: observed.TencentMutationCount, KubernetesMutationCount: 1,
		FailureStage: "node_patch_readback", ProviderErrorClass: "transport_error",
		Evidence: ComputeClaimEvidence{
			CVM: ComputeClaimMutationEvidence{
				Attempted: observed.Evidence.CVM.Attempted, Confirmed: observed.Evidence.CVM.Attempted,
			},
			Node: ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"node_ownership"}},
		},
	}
}

func legacyNodeReservedComputeClaimRecoveryMutation() computeClaimRecoveryMutationLedger {
	return nodeReservedComputeClaimRecoveryMutation(computeClaimRecoveryMutationLedger{
		State: "observed", Reason: "none", Evidence: ComputeClaimEvidence{},
	})
}

func validNodeReservedComputeClaimRecoveryMutation(ledger computeClaimRecoveryMutationLedger) bool {
	return ledger.Generation == "" && ledger.AttemptDigest == "" && ledger.State == "node_reserved" && ledger.Reason == "provider_describe" && ledger.FailureStage == "node_patch_readback" &&
		ledger.ProviderErrorClass == "transport_error" && ledger.TencentMutationCount >= 0 && ledger.TencentMutationCount <= 5 &&
		ledger.KubernetesMutationCount == 1 && ledger.Evidence.CVM.Attempted == ledger.TencentMutationCount &&
		ledger.Evidence.CVM.Confirmed == ledger.TencentMutationCount && ledger.Evidence.CVM.Unknown == 0 && len(ledger.Evidence.CVM.Missing) == 0 &&
		reflect.DeepEqual(ledger.Evidence.Node, ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"node_ownership"}})
}

func observedComputeClaimRecoveryMutation(result ComputeClaimRecoveryProof) computeClaimRecoveryMutationLedger {
	ledger := reservedComputeClaimRecoveryMutation()
	ledger.State = "observed"
	ledger.Reason = safeComputeClaimRecoveryReason(result.Reason, "identity_mismatch")
	if result.Reason == "none" {
		ledger.Reason = "none"
	}
	if validComputeClaimFailureStage(result.FailureStage) {
		ledger.FailureStage = result.FailureStage
	}
	if validComputeClaimProviderErrorClass(result.ProviderErrorClass) {
		ledger.ProviderErrorClass = result.ProviderErrorClass
	}
	if result.Evidence != nil &&
		validComputeClaimMutationEvidenceShape(result.Evidence.CVM, result.TencentMutationCount, 5, "cvm") &&
		validComputeClaimMutationEvidenceShape(result.Evidence.Node, result.KubernetesMutationCount, 1, "node") {
		ledger.TencentMutationCount = result.TencentMutationCount
		ledger.KubernetesMutationCount = result.KubernetesMutationCount
		ledger.Evidence = ComputeClaimEvidence{
			CVM:  cloneComputeClaimMutationEvidence(result.Evidence.CVM),
			Node: cloneComputeClaimMutationEvidence(result.Evidence.Node),
		}
	}
	if ledger.Reason == "none" {
		ledger.FailureStage, ledger.ProviderErrorClass = "", ""
	}
	return ledger
}

func validComputeClaimRecoveryMutationLedger(ledger computeClaimRecoveryMutationLedger) bool {
	if ledger.Generation != "" || ledger.AttemptDigest != "" {
		return validConfirmedNodeDriftMutationLedger(ledger)
	}
	if ledger.State != "reserved" && ledger.State != "node_reserved" && ledger.State != "observed" {
		return false
	}
	if ledger.Reason != "none" && safeComputeClaimRecoveryReason(ledger.Reason, "") != ledger.Reason {
		return false
	}
	valid := validComputeClaimFailureStage(ledger.FailureStage) && validComputeClaimProviderErrorClass(ledger.ProviderErrorClass) &&
		validComputeClaimMutationEvidenceShape(ledger.Evidence.CVM, ledger.TencentMutationCount, 5, "cvm") &&
		validComputeClaimMutationEvidenceShape(ledger.Evidence.Node, ledger.KubernetesMutationCount, 1, "node")
	if !valid {
		return false
	}
	if ledger.State == "reserved" {
		return reflect.DeepEqual(ledger, reservedComputeClaimRecoveryMutation())
	}
	if ledger.State == "node_reserved" {
		return validNodeReservedComputeClaimRecoveryMutation(ledger)
	}
	if ledger.Reason == "none" {
		return ledger.FailureStage == "" && ledger.ProviderErrorClass == "" &&
			validComputeClaimMutationEvidence(ledger.Evidence.CVM, ledger.TencentMutationCount, 5, "cvm") &&
			validComputeClaimMutationEvidence(ledger.Evidence.Node, ledger.KubernetesMutationCount, 1, "node")
	}
	return true
}

func validConfirmedNodeDriftMutationLedger(ledger computeClaimRecoveryMutationLedger) bool {
	if ledger.Generation != confirmedNodeDriftGeneration || !validComputeClaimRecoveryDigest(ledger.AttemptDigest) ||
		ledger.TencentMutationCount != 0 || ledger.KubernetesMutationCount < 0 || ledger.KubernetesMutationCount > 1 ||
		!reflect.DeepEqual(ledger.Evidence.CVM, ComputeClaimMutationEvidence{}) ||
		!validComputeClaimMutationEvidenceShape(ledger.Evidence.Node, ledger.KubernetesMutationCount, 1, "node") {
		return false
	}
	if ledger.State == "node_reserved" {
		return reflect.DeepEqual(ledger, reservedConfirmedNodeDriftMutation(ledger.AttemptDigest))
	}
	if ledger.State != "observed" {
		return false
	}
	if ledger.Reason == "confirmed_node_drift" {
		return ledger.FailureStage == "" && ledger.ProviderErrorClass == "" && ledger.KubernetesMutationCount == 1 &&
			reflect.DeepEqual(ledger.Evidence.Node, ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1})
	}
	return ledger.Reason != "none" && safeComputeClaimRecoveryReason(ledger.Reason, "") == ledger.Reason &&
		validComputeClaimFailureStage(ledger.FailureStage) && validComputeClaimProviderErrorClass(ledger.ProviderErrorClass)
}

func recoverableObservedComputeClaimRecoveryMutation(ledger computeClaimRecoveryMutationLedger) bool {
	if ledger.Generation != "" || ledger.AttemptDigest != "" || ledger.State != "observed" || ledger.Reason != "provider_describe" || ledger.FailureStage != "cvm_tag_readback" ||
		ledger.TencentMutationCount < 1 || ledger.TencentMutationCount > 5 || ledger.KubernetesMutationCount != 0 ||
		ledger.Evidence.CVM.Attempted != ledger.TencentMutationCount || ledger.Evidence.CVM.Confirmed != 0 || ledger.Evidence.CVM.Unknown != 0 ||
		!reflect.DeepEqual(ledger.Evidence.Node, ComputeClaimMutationEvidence{}) || len(ledger.Evidence.CVM.Missing) == 0 {
		return false
	}
	for _, field := range ledger.Evidence.CVM.Missing {
		switch field {
		case "opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id":
		default:
			return false
		}
	}
	return true
}

func confirmedCVMOnlyObservedComputeClaimRecoveryMutation(ledger computeClaimRecoveryMutationLedger) bool {
	return ledger.Generation == "" && ledger.AttemptDigest == "" && ledger.State == "observed" && ledger.Reason == "none" && ledger.FailureStage == "" && ledger.ProviderErrorClass == "" &&
		ledger.TencentMutationCount >= 1 && ledger.TencentMutationCount <= 5 && ledger.KubernetesMutationCount == 0 &&
		validComputeClaimMutationEvidence(ledger.Evidence.CVM, ledger.TencentMutationCount, 5, "cvm") &&
		reflect.DeepEqual(ledger.Evidence.Node, ComputeClaimMutationEvidence{})
}

func decodeComputeClaimRecoveryMutation(operation FabricOperation) (computeClaimRecoveryMutationLedger, bool, bool) {
	value, ok := operation.RedactedProviderPayload[computeClaimRecoveryMutationPayloadKey]
	if !ok {
		return computeClaimRecoveryMutationLedger{}, false, false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return computeClaimRecoveryMutationLedger{}, true, false
	}
	var ledger computeClaimRecoveryMutationLedger
	if json.Unmarshal(body, &ledger) != nil || !validComputeClaimRecoveryMutationLedger(ledger) {
		return computeClaimRecoveryMutationLedger{}, true, false
	}
	return ledger, true, true
}

func withComputeClaimRecoveryMutation(payload map[string]any, ledger computeClaimRecoveryMutationLedger) map[string]any {
	result := maps.Clone(payload)
	if result == nil {
		result = map[string]any{}
	}
	value := map[string]any{
		"state": ledger.State, "reason": ledger.Reason,
		"tencentMutationCount": ledger.TencentMutationCount, "kubernetesMutationCount": ledger.KubernetesMutationCount,
		"failureStage": ledger.FailureStage, "providerErrorClass": ledger.ProviderErrorClass,
		"evidence": map[string]any{
			"cvm": map[string]any{
				"attempted": ledger.Evidence.CVM.Attempted, "confirmed": ledger.Evidence.CVM.Confirmed,
				"unknown": ledger.Evidence.CVM.Unknown, "missing": append([]string(nil), ledger.Evidence.CVM.Missing...),
			},
			"node": map[string]any{
				"attempted": ledger.Evidence.Node.Attempted, "confirmed": ledger.Evidence.Node.Confirmed,
				"unknown": ledger.Evidence.Node.Unknown, "missing": append([]string(nil), ledger.Evidence.Node.Missing...),
			},
		},
	}
	if ledger.Generation != "" {
		value["generation"] = ledger.Generation
	}
	if ledger.AttemptDigest != "" {
		value["attemptDigest"] = ledger.AttemptDigest
	}
	result[computeClaimRecoveryMutationPayloadKey] = value
	return result
}

func validComputeClaimRecoveryMutationTransition(current, next FabricOperation) bool {
	currentLedger, currentPresent, currentValid := decodeComputeClaimRecoveryMutation(current)
	nextLedger, nextPresent, nextValid := decodeComputeClaimRecoveryMutation(next)
	if currentPresent && !currentValid || nextPresent && !nextValid {
		return false
	}
	if currentPresent && currentLedger.Generation == confirmedNodeDriftGeneration ||
		nextPresent && nextLedger.Generation == confirmedNodeDriftGeneration {
		if !nextPresent || nextLedger.Generation != confirmedNodeDriftGeneration {
			return false
		}
		if !currentPresent {
			return nextLedger.State == "node_reserved" && validConfirmedNodeDriftReservationTransition(current, next, nextLedger)
		}
		if currentLedger.Generation != confirmedNodeDriftGeneration || currentLedger.AttemptDigest != nextLedger.AttemptDigest {
			return false
		}
		return reflect.DeepEqual(currentLedger, nextLedger) || currentLedger.State == "node_reserved" && nextLedger.State == "observed"
	}
	if !currentPresent {
		return !nextPresent || nextLedger.State == "reserved" || validLegacyNodeReservationTransition(current, next, nextLedger)
	}
	if !nextPresent {
		return false
	}
	if (nextLedger.State == "reserved" || nextLedger.State == "node_reserved") && next.Status == "succeeded" {
		return false
	}
	switch currentLedger.State {
	case "reserved":
		return nextLedger.State == "observed" || reflect.DeepEqual(currentLedger, nextLedger)
	case "observed":
		return reflect.DeepEqual(currentLedger, nextLedger) ||
			(recoverableObservedComputeClaimRecoveryMutation(currentLedger) || confirmedCVMOnlyObservedComputeClaimRecoveryMutation(currentLedger)) &&
				reflect.DeepEqual(nextLedger, nodeReservedComputeClaimRecoveryMutation(currentLedger))
	case "node_reserved":
		return nextLedger.State == "observed" || reflect.DeepEqual(currentLedger, nextLedger)
	default:
		return false
	}
}

func validConfirmedNodeDriftReservationTransition(current, next FabricOperation, nextLedger computeClaimRecoveryMutationLedger) bool {
	if current.Status != "succeeded" || next.Status != "succeeded" || !validConfirmedNodeDriftMutationLedger(nextLedger) || nextLedger.State != "node_reserved" {
		return false
	}
	currentBinding, currentBindingPresent, currentBindingValid := decodeComputeClaimRecoveryBinding(current)
	nextBinding, nextBindingPresent, nextBindingValid := decodeComputeClaimRecoveryBinding(next)
	currentReconciliation, currentReconciliationPresent, currentReconciliationValid := decodeComputeClaimRecoveryReconciliation(current)
	nextReconciliation, nextReconciliationPresent, nextReconciliationValid := decodeComputeClaimRecoveryReconciliation(next)
	return currentBindingPresent && currentBindingValid && nextBindingPresent && nextBindingValid && currentBinding == nextBinding &&
		currentReconciliationPresent && currentReconciliationValid && nextReconciliationPresent && nextReconciliationValid &&
		reflect.DeepEqual(currentReconciliation, nextReconciliation) && currentReconciliation.SchemaVersion == 2 &&
		currentReconciliation.Generation == "normal_launch_terminal_evidence_v1" && currentReconciliation.State == "succeeded" &&
		reflect.DeepEqual(currentReconciliation.Node, ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1})
}

func validLegacyNodeReservationTransition(current, next FabricOperation, nextLedger computeClaimRecoveryMutationLedger) bool {
	currentBinding, currentPresent, currentValid := decodeComputeClaimRecoveryBinding(current)
	nextBinding, nextPresent, nextValid := decodeComputeClaimRecoveryBinding(next)
	return currentPresent && currentValid && nextPresent && nextValid && currentBinding == nextBinding &&
		(currentBinding.IdempotencyKey == currentBinding.LaunchOperationID+":compute-claim" ||
			currentBinding.IdempotencyKey == currentBinding.LaunchOperationID+":compute") &&
		current.IdempotencyKey == currentBinding.LaunchOperationID+":compute" &&
		reflect.DeepEqual(nextLedger, legacyNodeReservedComputeClaimRecoveryMutation())
}

func newComputeClaimRecoveryBinding(input ComputeClaimRecoveryClaimInput) computeClaimRecoveryBinding {
	target := struct {
		MachineName   string `json:"machineName"`
		NodeName      string `json:"nodeName"`
		CVMInstanceID string `json:"cvmInstanceId"`
		PrivateIP     string `json:"privateIp"`
		InstanceType  string `json:"instanceType"`
		Zone          string `json:"zone"`
	}{input.MachineName, input.NodeName, input.CVMInstanceID, input.PrivateIP, input.InstanceType, input.Zone}
	bindingInput := input
	bindingInput.AllowExistingStorageOperation = false
	// The execution selector is an authorization command, not part of the
	// stable resource binding. Replays must retain the original operation
	// identity while the service independently enforces the Node-only write set.
	bindingInput.NodeOnlyContinuation = false
	return computeClaimRecoveryBinding{
		LaunchOperationID: input.LaunchOperationID,
		IdempotencyKey:    input.IdempotencyKey,
		TargetHash:        hashInput(target),
		RequestHash:       hashInput(bindingInput),
	}
}

func historicalComputeClaimRecoveryBinding(input ComputeClaimRecoveryClaimInput) computeClaimRecoveryBinding {
	legacy := input
	legacy.IdempotencyKey = input.LaunchOperationID + ":compute-claim"
	return newComputeClaimRecoveryBinding(legacy)
}

func expectedComputeClaimRecoveryOperation(input ComputeClaimRecoveryInput) FabricOperation {
	expectedInput := ComputeAllocationInput{
		ID: input.ComputeAllocationID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID,
		PackageID: input.PackageID, NodePoolID: input.NodePoolID, IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	expected := newOperation(
		"create_compute_allocation", "compute_allocation", expectedInput.ID, expectedInput.AccountID,
		expectedInput.WorkspaceID, expectedInput.IdempotencyKey, hashInput(expectedInput), time.Time{},
	)
	return expected
}

func canonicalComputeClaimRecoveryOperation(operation FabricOperation, input ComputeClaimRecoveryInput) bool {
	expected := expectedComputeClaimRecoveryOperation(input)
	return operation.OperationID == expected.OperationID && operation.CallerService == expected.CallerService &&
		operation.Action == expected.Action && operation.ResourceKind == expected.ResourceKind && operation.ResourceID == expected.ResourceID &&
		operation.AccountID == expected.AccountID && operation.WorkspaceID == expected.WorkspaceID &&
		operation.IdempotencyKey == expected.IdempotencyKey && operation.RequestHash == expected.RequestHash
}

func computeClaimRecoveryBindingDigest(binding computeClaimRecoveryBinding) string {
	body, err := json.Marshal(binding)
	if err != nil {
		return ""
	}
	return computeClaimIdentityDigest(string(body))
}

func computeClaimRecoveryMutationDigest(ledger computeClaimRecoveryMutationLedger) string {
	body, err := json.Marshal(ledger)
	if err != nil {
		return ""
	}
	return computeClaimIdentityDigest(string(body))
}

func validComputeClaimRecoveryDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func isolatedRequestHashReconciliationLedger(ledger computeClaimRecoveryMutationLedger) bool {
	if ledger.State != "observed" || ledger.Reason != "provider_describe" || ledger.FailureStage != "cvm_tag_readback" ||
		ledger.ProviderErrorClass != "provider_error" ||
		ledger.TencentMutationCount != 1 || ledger.KubernetesMutationCount != 0 ||
		!reflect.DeepEqual(ledger.Evidence.Node, ComputeClaimMutationEvidence{}) ||
		ledger.Evidence.CVM.Attempted != 1 || ledger.Evidence.CVM.Confirmed != 0 || ledger.Evidence.CVM.Unknown != 1 ||
		!reflect.DeepEqual(ledger.Evidence.CVM.Missing, []string{"opl_account_id"}) {
		return false
	}
	return true
}

func normalLaunchTerminalRequestHashReconciliationEvidence(
	operation FabricOperation,
	input ComputeClaimRecoveryClaimInput,
	binding computeClaimRecoveryBinding,
) (string, bool) {
	if _, present, _ := decodeComputeClaimRecoveryMutation(operation); present {
		return "", false
	}
	createBudget, createPresent, createValid := normalLaunchStageBudget(operation.RedactedProviderPayload, "compute_create")
	cvmBudget, cvmPresent, cvmValid := normalLaunchStageBudget(operation.RedactedProviderPayload, "compute_claim_cvm")
	_, nodePresent, nodeValid := normalLaunchStageBudget(operation.RedactedProviderPayload, "compute_claim_node")
	terminal, terminalPresent, terminalValid := decodeComputeClaimTerminalEvidence(operation)
	wantBindingDigest := computeClaimIdentityDigest(binding.LaunchOperationID + "|" + binding.IdempotencyKey + "|" + binding.TargetHash + "|" + binding.RequestHash)
	if !createPresent || !createValid || createBudget != confirmedNormalLaunchMutationBudget() ||
		!cvmPresent || !cvmValid || cvmBudget != reservedNormalLaunchMutationBudget() || nodePresent || !nodeValid ||
		!terminalPresent || !terminalValid || terminal.Stage != "compute_claim_cvm" || terminal.Status != "terminal_unprovable" ||
		terminal.AttemptCount != 1 || terminal.Attempted != 1 || terminal.Confirmed != 0 || terminal.Unknown != 1 || terminal.Max != 1 ||
		terminal.FabricRecordID != operation.ID || terminal.OperationID != operation.OperationID || terminal.IdempotencyKey != operation.IdempotencyKey ||
		terminal.RequestHash != operation.RequestHash || terminal.LaunchOperationID != input.LaunchOperationID || terminal.AccountID != input.AccountID ||
		terminal.WorkspaceID != input.WorkspaceID || terminal.ComputeAllocationID != input.ComputeAllocationID || terminal.StorageVolumeID != input.StorageVolumeID ||
		terminal.PackageID != input.PackageID || terminal.PoolID != input.PoolID || terminal.NodePoolID != input.NodePoolID ||
		terminal.MachineName != input.MachineName || terminal.NodeName != input.NodeName || terminal.CVMInstanceID != input.CVMInstanceID ||
		terminal.BindingDigest != wantBindingDigest || terminal.OperatorApprovalID != "" || terminal.OperatorApprovalDigest != "" ||
		terminal.OperatorIdempotencyKey != "" || terminal.ManualRecoveryLedgerDigest != "" || terminal.Evidence != nil || len(terminal.StageBudgets) != 1 ||
		terminal.StageBudgets["compute_claim_cvm"] != (ComputeClaimStageBudget{Attempted: 1, Confirmed: 0, Unknown: 1, Max: 1}) {
		return "", false
	}
	return hashInput(struct {
		ComputeCreate normalLaunchMutationBudget
		ComputeClaim  normalLaunchMutationBudget
		Terminal      ComputeClaimTerminalEvidence
	}{createBudget, cvmBudget, terminal}), true
}

func isolatedRequestHashReconciliationProvenance(
	operation FabricOperation,
	input ComputeClaimRecoveryClaimInput,
	persisted computeClaimRecoveryBinding,
	present, valid bool,
) (computeClaimRecoveryReconciliationProvenance, bool) {
	if !present || !valid || !canonicalComputeClaimRecoveryOperation(operation, input.ComputeClaimRecoveryInput) {
		return computeClaimRecoveryReconciliationProvenance{}, false
	}
	want := newComputeClaimRecoveryBinding(input)
	if persisted.LaunchOperationID != want.LaunchOperationID || persisted.IdempotencyKey != want.IdempotencyKey ||
		persisted.TargetHash != want.TargetHash || persisted.RequestHash == want.RequestHash {
		return computeClaimRecoveryReconciliationProvenance{}, false
	}
	ledger, ledgerPresent, ledgerValid := decodeComputeClaimRecoveryMutation(operation)
	if ledgerPresent {
		if !ledgerValid || !isolatedRequestHashReconciliationLedger(ledger) {
			return computeClaimRecoveryReconciliationProvenance{}, false
		}
		return computeClaimRecoveryReconciliationProvenance{
			SchemaVersion: 1, Generation: "isolated_request_hash_v1", Source: "manual_recovery_ledger", Digest: computeClaimRecoveryMutationDigest(ledger),
		}, true
	}
	digest, ok := normalLaunchTerminalRequestHashReconciliationEvidence(operation, input, persisted)
	if !ok {
		return computeClaimRecoveryReconciliationProvenance{}, false
	}
	return computeClaimRecoveryReconciliationProvenance{
		SchemaVersion: 2, Generation: "normal_launch_terminal_evidence_v1", Source: "normal_launch_terminal_evidence", Digest: digest,
	}, true
}

func isolatedRequestHashReconciliationBinding(operation FabricOperation, input ComputeClaimRecoveryClaimInput, persisted computeClaimRecoveryBinding, present, valid bool) bool {
	_, ok := isolatedRequestHashReconciliationProvenance(operation, input, persisted, present, valid)
	return ok
}

func validComputeClaimRecoveryReconciliation(value computeClaimRecoveryReconciliation) bool {
	if value.Consumer != "claim_compute_recovery" || !validComputeClaimRecoveryDigest(value.BindingDigest) || !validComputeClaimRecoveryDigest(value.ExpectedRequestHashDigest) ||
		!validComputeClaimRecoveryDigest(value.PersistedRequestHashDigest) || value.ExpectedRequestHashDigest == value.PersistedRequestHashDigest ||
		!validComputeClaimRecoveryDigest(value.AuthorityDigest) {
		return false
	}
	switch value.SchemaVersion {
	case 1:
		if value.Generation != "isolated_request_hash_v1" || value.ProvenanceSource != "" || value.ProvenanceDigest != "" ||
			!validComputeClaimRecoveryDigest(value.MutationLedgerDigest) {
			return false
		}
	case 2:
		if value.Generation != "normal_launch_terminal_evidence_v1" || value.ProvenanceSource != "normal_launch_terminal_evidence" ||
			!validComputeClaimRecoveryDigest(value.ProvenanceDigest) || value.MutationLedgerDigest != "" {
			return false
		}
	default:
		return false
	}
	switch value.State {
	case "verified":
		return value.FailureStage == "" && value.ProviderErrorClass == "" && reflect.DeepEqual(value.Node, ComputeClaimMutationEvidence{})
	case "node_reserved":
		return value.FailureStage == "node_patch_readback" && value.ProviderErrorClass == "transport_error" &&
			reflect.DeepEqual(value.Node, ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"node_ownership"}})
	case "observed":
		return validComputeClaimFailureStage(value.FailureStage) && validComputeClaimProviderErrorClass(value.ProviderErrorClass) &&
			validComputeClaimMutationEvidenceShape(value.Node, value.Node.Attempted, 1, "node")
	case "succeeded":
		return value.FailureStage == "" && value.ProviderErrorClass == "" &&
			(reflect.DeepEqual(value.Node, ComputeClaimMutationEvidence{}) || reflect.DeepEqual(value.Node, ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}))
	default:
		return false
	}
}

func exactLegacyKubectlClientRejectedReconciliation(value computeClaimRecoveryReconciliation) bool {
	return value.SchemaVersion == 2 && value.Generation == "normal_launch_terminal_evidence_v1" &&
		value.ProvenanceSource == "normal_launch_terminal_evidence" && value.State == "observed" &&
		value.FailureStage == "node_patch_readback" && value.ProviderErrorClass == "provider_error" &&
		reflect.DeepEqual(value.Node, ComputeClaimMutationEvidence{Attempted: 1, Missing: []string{"node_ownership"}})
}

func newComputeClaimNodeClientRejectionRecovery(source computeClaimRecoveryReconciliation) computeClaimNodeClientRejectionRecovery {
	return computeClaimNodeClientRejectionRecovery{
		SchemaVersion: 1, Classification: "kubectl_client_validation_rejected", Invocation: "patch_json_filename_stdin_v1",
		RecordedCalls: 1, APIAcceptedMutations: 0, SourceReconciliationDigest: hashInput(source),
	}
}

func validComputeClaimNodeClientRejectionRecovery(value computeClaimNodeClientRejectionRecovery) bool {
	return value.SchemaVersion == 1 && value.Classification == "kubectl_client_validation_rejected" &&
		value.Invocation == "patch_json_filename_stdin_v1" && value.RecordedCalls == 1 && value.APIAcceptedMutations == 0 &&
		validComputeClaimRecoveryDigest(value.SourceReconciliationDigest)
}

func decodeComputeClaimNodeClientRejectionRecovery(operation FabricOperation) (computeClaimNodeClientRejectionRecovery, bool, bool) {
	value, present := operation.RedactedProviderPayload[computeClaimNodeClientRejectionPayloadKey]
	if !present {
		return computeClaimNodeClientRejectionRecovery{}, false, false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return computeClaimNodeClientRejectionRecovery{}, true, false
	}
	var recovery computeClaimNodeClientRejectionRecovery
	if json.Unmarshal(body, &recovery) != nil || !validComputeClaimNodeClientRejectionRecovery(recovery) {
		return computeClaimNodeClientRejectionRecovery{}, true, false
	}
	return recovery, true, true
}

func computeClaimRecoveryReconciliationMatches(
	value computeClaimRecoveryReconciliation,
	operation FabricOperation,
	input ComputeClaimRecoveryClaimInput,
	binding computeClaimRecoveryBinding,
	ledger computeClaimRecoveryMutationLedger,
) bool {
	want := newComputeClaimRecoveryBinding(input)
	if value.BindingDigest != computeClaimRecoveryBindingDigest(binding) {
		return false
	}
	if value.ExpectedRequestHashDigest != computeClaimIdentityDigest(want.RequestHash) ||
		value.PersistedRequestHashDigest != computeClaimIdentityDigest(binding.RequestHash) ||
		!canonicalComputeClaimRecoveryOperation(operation, input.ComputeClaimRecoveryInput) {
		return false
	}
	if value.SchemaVersion == 1 {
		return value.MutationLedgerDigest == computeClaimRecoveryMutationDigest(ledger)
	}
	provenance, ok := isolatedRequestHashReconciliationProvenance(operation, input, binding, true, true)
	return ok && provenance.SchemaVersion == 2 && value.ProvenanceSource == provenance.Source && value.ProvenanceDigest == provenance.Digest
}

func decodeComputeClaimRecoveryReconciliation(operation FabricOperation) (computeClaimRecoveryReconciliation, bool, bool) {
	value, present := operation.RedactedProviderPayload[computeClaimRecoveryReconciliationPayloadKey]
	if !present {
		return computeClaimRecoveryReconciliation{}, false, false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return computeClaimRecoveryReconciliation{}, true, false
	}
	var reconciliation computeClaimRecoveryReconciliation
	if json.Unmarshal(body, &reconciliation) != nil || !validComputeClaimRecoveryReconciliation(reconciliation) {
		return computeClaimRecoveryReconciliation{}, true, false
	}
	return reconciliation, true, true
}

func withComputeClaimRecoveryReconciliation(payload map[string]any, value computeClaimRecoveryReconciliation) map[string]any {
	result := maps.Clone(payload)
	if result == nil {
		result = map[string]any{}
	}
	result[computeClaimRecoveryReconciliationPayloadKey] = map[string]any{
		"schemaVersion": value.SchemaVersion, "consumer": value.Consumer, "generation": value.Generation, "state": value.State,
		"provenanceSource": value.ProvenanceSource, "provenanceDigest": value.ProvenanceDigest,
		"bindingDigest": value.BindingDigest, "expectedRequestHashDigest": value.ExpectedRequestHashDigest,
		"persistedRequestHashDigest": value.PersistedRequestHashDigest, "mutationLedgerDigest": value.MutationLedgerDigest,
		"authorityDigest": value.AuthorityDigest, "failureStage": value.FailureStage, "providerErrorClass": value.ProviderErrorClass,
		"node": map[string]any{"attempted": value.Node.Attempted, "confirmed": value.Node.Confirmed, "unknown": value.Node.Unknown, "missing": append([]string(nil), value.Node.Missing...)},
	}
	return result
}

func validComputeClaimRecoveryReconciliationTransition(current, next FabricOperation) bool {
	currentValue, currentPresent, currentValid := decodeComputeClaimRecoveryReconciliation(current)
	nextValue, nextPresent, nextValid := decodeComputeClaimRecoveryReconciliation(next)
	currentRejection, currentRejectionPresent, currentRejectionValid := decodeComputeClaimNodeClientRejectionRecovery(current)
	nextRejection, nextRejectionPresent, nextRejectionValid := decodeComputeClaimNodeClientRejectionRecovery(next)
	if currentPresent && !currentValid || nextPresent && !nextValid {
		return false
	}
	if currentRejectionPresent && !currentRejectionValid || nextRejectionPresent && !nextRejectionValid {
		return false
	}
	if !currentPresent {
		return !currentRejectionPresent && !nextRejectionPresent && (!nextPresent || nextValue.State == "verified")
	}
	legacyClientRejectionReservation := !currentRejectionPresent && nextRejectionPresent &&
		exactLegacyKubectlClientRejectedReconciliation(currentValue) && nextValue.State == "node_reserved" &&
		nextRejection == newComputeClaimNodeClientRejectionRecovery(currentValue)
	if currentRejectionPresent && (!nextRejectionPresent || currentRejection != nextRejection) ||
		!currentRejectionPresent && nextRejectionPresent && !legacyClientRejectionReservation {
		return false
	}
	if !nextPresent || currentValue.SchemaVersion != nextValue.SchemaVersion || currentValue.Consumer != nextValue.Consumer ||
		currentValue.Generation != nextValue.Generation || currentValue.ProvenanceSource != nextValue.ProvenanceSource || currentValue.ProvenanceDigest != nextValue.ProvenanceDigest ||
		currentValue.BindingDigest != nextValue.BindingDigest ||
		currentValue.ExpectedRequestHashDigest != nextValue.ExpectedRequestHashDigest ||
		currentValue.PersistedRequestHashDigest != nextValue.PersistedRequestHashDigest ||
		currentValue.MutationLedgerDigest != nextValue.MutationLedgerDigest || currentValue.AuthorityDigest != nextValue.AuthorityDigest {
		return false
	}
	switch currentValue.State {
	case "verified":
		return nextValue.State == "verified" || nextValue.State == "node_reserved" || nextValue.State == "succeeded"
	case "node_reserved":
		return nextValue.State == "node_reserved" || nextValue.State == "observed" || nextValue.State == "succeeded"
	case "observed":
		return nextValue.State == "observed" || nextValue.State == "succeeded" || legacyClientRejectionReservation
	case "succeeded":
		return nextValue.State == "succeeded" && reflect.DeepEqual(currentValue, nextValue)
	default:
		return false
	}
}
