package fabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
)

func computeClaimIdentityDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func computeClaimIdentityEvidence(operation FabricOperation, input ComputeClaimRecoveryClaimInput) *ComputeClaimIdentityEvidence {
	want := newComputeClaimRecoveryBinding(input)
	historical := historicalComputeClaimRecoveryBinding(input)
	expectedOperation := expectedComputeClaimRecoveryOperation(input.ComputeClaimRecoveryInput)
	got, present, valid := decodeComputeClaimRecoveryBinding(operation)
	bindingClassification, bindingDigest := classifyComputeClaimRecoveryBinding(operation, input, got, present, valid)
	ledgerState, ledgerOutcome, ledgerDigest := computeClaimMutationLedgerEvidence(operation)
	checks := []ComputeClaimIdentityCheck{
		{Field: "fabric.operationId", Matches: operation.OperationID == expectedOperation.OperationID, Expected: expectedOperation.OperationID, Actual: operation.OperationID},
		{Field: "fabric.operationIdempotencyKey", Matches: operation.IdempotencyKey == input.LaunchOperationID+":compute", Expected: input.LaunchOperationID + ":compute", Actual: operation.IdempotencyKey},
		{Field: "fabric.operationRequestHash", Matches: operation.RequestHash == expectedOperation.RequestHash, ExpectedDigest: computeClaimIdentityDigest(expectedOperation.RequestHash), ActualDigest: computeClaimIdentityDigest(operation.RequestHash)},
		{Field: "binding.present", Matches: present, Expected: "present", Actual: map[bool]string{true: "present", false: "absent"}[present]},
		{Field: "binding.valid", Matches: valid, Expected: "valid", Actual: map[bool]string{true: "valid", false: "invalid"}[valid]},
	}
	if present && valid {
		bindingKind := bindingClassification
		expected := want
		switch bindingClassification {
		case "current":
		case "compute-claim":
			expected = historical
		}
		compatible := bindingClassification == "current" || bindingClassification == "compute-claim" || bindingClassification == "request-hash-reconciliation"
		checks = append(checks,
			ComputeClaimIdentityCheck{Field: "binding.compatibility", Matches: compatible, Expected: "current_compute_claim_or_request_hash_reconciliation", Actual: bindingKind},
			ComputeClaimIdentityCheck{Field: "binding.launchOperationId", Matches: got.LaunchOperationID == expected.LaunchOperationID, Expected: expected.LaunchOperationID, Actual: got.LaunchOperationID},
			ComputeClaimIdentityCheck{Field: "binding.idempotencyKey", Matches: got.IdempotencyKey == expected.IdempotencyKey, Expected: expected.IdempotencyKey, Actual: got.IdempotencyKey},
			ComputeClaimIdentityCheck{Field: "binding.targetHash", Matches: got.TargetHash == expected.TargetHash, ExpectedDigest: computeClaimIdentityDigest(expected.TargetHash), ActualDigest: computeClaimIdentityDigest(got.TargetHash)},
			ComputeClaimIdentityCheck{Field: "binding.requestHash", Matches: got.RequestHash == expected.RequestHash, ExpectedDigest: computeClaimIdentityDigest(expected.RequestHash), ActualDigest: computeClaimIdentityDigest(got.RequestHash)},
		)
	}
	result := &ComputeClaimIdentityEvidence{
		Checks: checks, BindingClassification: bindingClassification, BindingDigest: bindingDigest,
		MutationLedger: ledgerState, MutationLedgerOutcome: ledgerOutcome, MutationLedgerDigest: ledgerDigest,
	}
	if ledger, ledgerPresent, ledgerValid := decodeComputeClaimRecoveryMutation(operation); ledgerPresent && ledgerValid {
		result.MutationEvidence = &ComputeClaimEvidence{
			CVM:  cloneComputeClaimMutationEvidence(ledger.Evidence.CVM),
			Node: cloneComputeClaimMutationEvidence(ledger.Evidence.Node),
		}
		result.FailureStage = ledger.FailureStage
		result.ProviderErrorClass = ledger.ProviderErrorClass
	}
	if reconciliation, present, valid := decodeComputeClaimRecoveryReconciliation(operation); present && valid {
		result.Reconciliation = &ComputeClaimReconciliationEvidence{
			SchemaVersion: reconciliation.SchemaVersion, Consumer: reconciliation.Consumer, Generation: reconciliation.Generation,
			ProvenanceSource: reconciliation.ProvenanceSource, ProvenanceDigest: reconciliation.ProvenanceDigest, State: reconciliation.State,
			ExpectedRequestHashDigest:  reconciliation.ExpectedRequestHashDigest,
			PersistedRequestHashDigest: reconciliation.PersistedRequestHashDigest,
			FailureStage:               reconciliation.FailureStage, ProviderErrorClass: reconciliation.ProviderErrorClass,
			Node: cloneComputeClaimMutationEvidence(reconciliation.Node),
		}
	}
	return result
}

func classifyComputeClaimRecoveryBinding(operation FabricOperation, input ComputeClaimRecoveryClaimInput, got computeClaimRecoveryBinding, present, valid bool) (string, string) {
	value, rawPresent := operation.RedactedProviderPayload["computeClaimRecovery"]
	if !rawPresent {
		return "other", computeClaimIdentityDigest("absent")
	}
	body, err := json.Marshal(value)
	if err != nil {
		return "other", computeClaimIdentityDigest("invalid")
	}
	digest := computeClaimIdentityDigest(string(body))
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || len(fields) != 4 {
		return "other", digest
	}
	for _, field := range []string{"launchOperationId", "idempotencyKey", "targetHash", "requestHash"} {
		if _, ok := fields[field]; !ok {
			return "other", digest
		}
	}
	if !present || !valid {
		return "other", digest
	}
	if got == newComputeClaimRecoveryBinding(input) {
		return "current", digest
	}
	if got == historicalComputeClaimRecoveryBinding(input) {
		return "compute-claim", digest
	}
	if isolatedRequestHashReconciliationBinding(operation, input, got, present, valid) {
		return "request-hash-reconciliation", digest
	}
	if knownLegacyComputeClaimRecoveryIdempotencyKey(got.IdempotencyKey) {
		legacy := input
		legacy.IdempotencyKey = got.IdempotencyKey
		if got == newComputeClaimRecoveryBinding(legacy) {
			return "known-legacy", digest
		}
	}
	return "other", digest
}

func knownLegacyComputeClaimRecoveryIdempotencyKey(value string) bool {
	const prefix = "recovery-exec-"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+20 {
		return false
	}
	for _, character := range value[len(prefix):] {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func computeClaimMutationLedgerEvidence(operation FabricOperation) (string, string, string) {
	value, present := operation.RedactedProviderPayload[computeClaimRecoveryMutationPayloadKey]
	if !present {
		return "absent", "confirmed_zero", computeClaimIdentityDigest("absent")
	}
	body, err := json.Marshal(value)
	if err != nil {
		return "invalid", "unknown", computeClaimIdentityDigest("invalid")
	}
	digest := computeClaimIdentityDigest(string(body))
	ledger, _, valid := decodeComputeClaimRecoveryMutation(operation)
	if !valid {
		return "invalid", "unknown", digest
	}
	if ledger.State != "observed" {
		return ledger.State, "unknown", digest
	}
	complete := validComputeClaimMutationEvidence(ledger.Evidence.CVM, ledger.TencentMutationCount, 5, "cvm") &&
		validComputeClaimMutationEvidence(ledger.Evidence.Node, ledger.KubernetesMutationCount, 1, "node")
	if !complete {
		return ledger.State, "unknown", digest
	}
	if ledger.TencentMutationCount == 0 && ledger.KubernetesMutationCount == 0 {
		return ledger.State, "confirmed_zero", digest
	}
	return ledger.State, "nonzero", digest
}

func decodeComputeClaimRecoveryBinding(operation FabricOperation) (computeClaimRecoveryBinding, bool, bool) {
	value, ok := operation.RedactedProviderPayload["computeClaimRecovery"]
	if !ok {
		return computeClaimRecoveryBinding{}, false, false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return computeClaimRecoveryBinding{}, true, false
	}
	var binding computeClaimRecoveryBinding
	if json.Unmarshal(body, &binding) != nil || binding.LaunchOperationID == "" || binding.IdempotencyKey == "" || binding.TargetHash == "" || binding.RequestHash == "" {
		return computeClaimRecoveryBinding{}, true, false
	}
	return binding, true, true
}

func withComputeClaimRecoveryBinding(payload map[string]any, binding computeClaimRecoveryBinding) map[string]any {
	result := maps.Clone(payload)
	if result == nil {
		result = map[string]any{}
	}
	result["computeClaimRecovery"] = map[string]any{
		"launchOperationId": binding.LaunchOperationID,
		"idempotencyKey":    binding.IdempotencyKey,
		"targetHash":        binding.TargetHash,
		"requestHash":       binding.RequestHash,
	}
	return result
}

func (s *Service) computeClaimRecoveryLocalState(ctx context.Context, input ComputeClaimRecoveryInput) (FabricOperation, ComputeAllocation, ComputeAllocationPreparation, MachineOwnership, string, error) {
	operation, found, err := s.operations.OperationByActionIdempotency(ctx, "create_compute_allocation", input.LaunchOperationID+":compute")
	if err != nil {
		if errors.Is(err, ErrOperationIdentityConflict) {
			return FabricOperation{}, ComputeAllocation{}, ComputeAllocationPreparation{}, MachineOwnership{}, "local_identity",
				fmt.Errorf("%w: local_identity", ErrComputeClaimRecoveryUnavailable)
		}
		return FabricOperation{}, ComputeAllocation{}, ComputeAllocationPreparation{}, MachineOwnership{}, "local_identity", err
	}
	storageOperation, storageFound, err := s.operations.OperationByActionIdempotency(ctx, "create_storage_volume", input.LaunchOperationID+":storage")
	storageDisposition := computeClaimRecoveryStorageOperationDisposition(storageOperation, storageFound, input)
	if errors.Is(err, ErrOperationIdentityConflict) {
		storageDisposition = computeClaimStorageOperationConflict
	} else if err != nil {
		return FabricOperation{}, ComputeAllocation{}, ComputeAllocationPreparation{}, MachineOwnership{}, "local_identity", err
	}
	if storageDisposition == computeClaimStorageOperationUnknown && !input.AllowExistingStorageOperation {
		return FabricOperation{}, ComputeAllocation{}, ComputeAllocationPreparation{}, MachineOwnership{}, "storage_already_started",
			fmt.Errorf("%w: storage_already_started", ErrComputeClaimRecoveryUnavailable)
	}
	if storageDisposition == computeClaimStorageOperationConflict && !input.AllowExistingStorageOperation {
		return FabricOperation{}, ComputeAllocation{}, ComputeAllocationPreparation{}, MachineOwnership{}, "identity_mismatch",
			fmt.Errorf("%w: identity_mismatch", ErrComputeClaimRecoveryUnavailable)
	}
	if !found || operation.ResourceID != input.ComputeAllocationID || operation.AccountID != input.AccountID || operation.WorkspaceID != input.WorkspaceID ||
		operation.IdempotencyKey != input.LaunchOperationID+":compute" ||
		(operation.Status != "failed" && operation.Status != "claim_pending" && operation.Status != "succeeded") {
		return FabricOperation{}, ComputeAllocation{}, ComputeAllocationPreparation{}, MachineOwnership{}, "local_identity",
			fmt.Errorf("%w: local_identity", ErrComputeClaimRecoveryUnavailable)
	}
	var allocation ComputeAllocation
	plan, hasPlan := decodeComputeAllocationPlan(operation)
	if !decodeOperationResource(operation, &allocation) || !hasPlan || !validComputeClaimRecoveryLocalIdentity(input, allocation, plan) {
		return FabricOperation{}, ComputeAllocation{}, ComputeAllocationPreparation{}, MachineOwnership{}, "local_identity",
			fmt.Errorf("%w: local_identity", ErrComputeClaimRecoveryUnavailable)
	}
	ownership, err := s.operations.MachineOwnership(ctx, input.ComputeAllocationID)
	if err != nil || !validComputeClaimRecoveryOwnership(allocation, ownership) {
		return FabricOperation{}, ComputeAllocation{}, ComputeAllocationPreparation{}, MachineOwnership{}, "local_identity",
			fmt.Errorf("%w: local_identity", ErrComputeClaimRecoveryUnavailable)
	}
	return operation, allocation, plan, ownership, "", nil
}

func (s *Service) MachineOwnership(ctx context.Context, resourceID string) (MachineOwnership, error) {
	return s.operations.MachineOwnership(ctx, strings.TrimSpace(resourceID))
}
