package fabric

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"
)

// Tencent/Instance operator workflows retain this historical recovery surface.
// Typed Workspace Launch does not call these methods or consume this state.
func (s *Service) ReadComputePoolHead(ctx context.Context, nodePoolID string) (ComputePoolHeadReadback, error) {
	result := ComputePoolHeadReadback{SchemaVersion: 1, Status: "unknown", ContinuationState: "unknown", FailureStage: "compute_pool_head", ErrorCode: "fabric_compute_pool_head_unavailable"}
	if !validComputePoolNodePoolID(nodePoolID) {
		return result, ErrInvalidMonthlyPreflight
	}
	head, found, err := s.operations.ComputePoolHead(ctx, nodePoolID)
	if err != nil {
		return result, fmt.Errorf("%w: compute_pool_head_read_failed", ErrMonthlyPreflightUnavailable)
	}
	if !found {
		result.Status, result.ContinuationState, result.FailureStage, result.ErrorCode = "absent", "absent", "none", "none"
		return result, nil
	}
	result.Status = head.Status
	var allocation ComputeAllocation
	plan, hasPlan := decodeComputeAllocationPlan(head)
	if !decodeOperationResource(head, &allocation) || !hasPlan || head.ComputePoolKey != nodePoolID || allocation.NodePoolID != nodePoolID ||
		validateComputeAllocationPreparation(plan, allocation, packagePlan(allocation.PackageID)) != nil || validateTencentComputeAllocationIdentity(allocation, plan) != nil {
		return result, nil
	}
	if head.Status == "started" {
		result.ContinuationState, result.FailureStage, result.ErrorCode = "continuable", "none", "none"
		return result, nil
	}
	if head.Status != "claim_pending" {
		return result, nil
	}
	_, manualPresent, manualValid := decodeComputeClaimRecoveryMutation(head)
	if manualPresent {
		if manualValid {
			result.ContinuationState, result.ErrorCode = "blocked", "fabric_compute_pool_head_manual_recovery"
		}
		return result, nil
	}
	binding, bindingOK := automaticComputeClaimRecoveryBinding(head, allocation, plan)
	persisted, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(head)
	ownership, ownershipErr := s.operations.MachineOwnership(ctx, allocation.ID)
	requestHashRecovery := exactUnmarkedLegacyKubectlClientRejection(head, allocation, plan)
	historicalRecovery := ownershipErr == nil && exactHistoricalComputeClaimRecoveryWithoutLedger(head, allocation, plan, ownership)
	if bindingOK && (requestHashRecovery || historicalRecovery || !bindingPresent || bindingValid && persisted == binding) &&
		ownershipErr == nil && validComputeClaimRecoveryOwnership(allocation, ownership) {
		result.ContinuationState, result.FailureStage, result.ErrorCode = "continuable", "none", "none"
	}
	return result, nil
}

func exactHistoricalComputeClaimRecoveryWithoutLedger(operation FabricOperation, allocation ComputeAllocation, plan ComputeAllocationPreparation, ownership MachineOwnership) bool {
	input, inputOK := automaticComputeClaimRecoveryInput(operation, allocation, plan)
	if !inputOK || allocation.Status != "quarantined" || ownership.Status != "quarantined" ||
		!validComputeClaimRecoveryOwnership(allocation, ownership) {
		return false
	}
	persisted, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(operation)
	_, mutationPresent, _ := decodeComputeClaimRecoveryMutation(operation)
	_, reconciliationPresent, _ := decodeComputeClaimRecoveryReconciliation(operation)
	_, clientRejectionPresent, _ := decodeComputeClaimNodeClientRejectionRecovery(operation)
	return bindingPresent && bindingValid && persisted == historicalComputeClaimRecoveryBinding(input) &&
		!mutationPresent && !reconciliationPresent && !clientRejectionPresent
}

func exactUnmarkedLegacyKubectlClientRejection(operation FabricOperation, allocation ComputeAllocation, plan ComputeAllocationPreparation) bool {
	input, inputOK := automaticComputeClaimRecoveryInput(operation, allocation, plan)
	if !inputOK {
		return false
	}
	persisted, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(operation)
	if !bindingPresent || !bindingValid {
		return false
	}
	provenance, provenanceOK := isolatedRequestHashReconciliationProvenance(operation, input, persisted, bindingPresent, bindingValid)
	if !provenanceOK || provenance.SchemaVersion != 2 {
		return false
	}
	reconciliation, reconciliationPresent, reconciliationValid := decodeComputeClaimRecoveryReconciliation(operation)
	if !reconciliationPresent || !reconciliationValid || !exactLegacyKubectlClientRejectedReconciliation(reconciliation) ||
		!computeClaimRecoveryReconciliationMatches(reconciliation, operation, input, persisted, computeClaimRecoveryMutationLedger{}) {
		return false
	}
	_, clientRejectionPresent, clientRejectionValid := decodeComputeClaimNodeClientRejectionRecovery(operation)
	return !clientRejectionPresent && !clientRejectionValid
}

type computePoolHeadTerminalizationCandidate struct {
	operation  FabricOperation
	allocation ComputeAllocation
	plan       ComputeAllocationPreparation
	ownership  MachineOwnership
	binding    computeClaimRecoveryBinding
	ledger     computeClaimRecoveryMutationLedger
	readback   ComputePoolHeadTerminalizationReadback
}

func (s *Service) ReadComputePoolHeadTerminalization(ctx context.Context, nodePoolID string) (ComputePoolHeadTerminalizationReadback, error) {
	candidate, err := s.computePoolHeadTerminalizationCandidate(ctx, nodePoolID)
	if err != nil {
		return ComputePoolHeadTerminalizationReadback{SchemaVersion: 1, Status: "blocked"}, err
	}
	return candidate.readback, nil
}

func (s *Service) computePoolHeadTerminalizationCandidate(ctx context.Context, nodePoolID string) (computePoolHeadTerminalizationCandidate, error) {
	if !validComputePoolNodePoolID(nodePoolID) {
		return computePoolHeadTerminalizationCandidate{}, ErrInvalidComputePoolHeadTerminalization
	}
	operation, found, err := s.operations.ComputePoolHead(ctx, nodePoolID)
	if err != nil || !found || operation.Status != "claim_pending" || operation.ComputePoolKey != nodePoolID {
		return computePoolHeadTerminalizationCandidate{}, fmt.Errorf("%w: exact_claim_pending_head_required", ErrComputePoolHeadTerminalizationUnavailable)
	}
	var allocation ComputeAllocation
	plan, hasPlan := decodeComputeAllocationPlan(operation)
	if !decodeOperationResource(operation, &allocation) || !hasPlan || allocation.ID != operation.ResourceID || allocation.NodePoolID != nodePoolID ||
		(allocation.Status != "compute_claim_pending" && allocation.Status != "quarantined") ||
		validateComputeAllocationPreparation(plan, allocation, packagePlan(allocation.PackageID)) != nil || validateTencentComputeAllocationIdentity(allocation, plan) != nil {
		return computePoolHeadTerminalizationCandidate{}, fmt.Errorf("%w: allocation_identity_invalid", ErrComputePoolHeadTerminalizationUnavailable)
	}
	binding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(operation)
	ledger, ledgerPresent, ledgerValid := decodeComputeClaimRecoveryMutation(operation)
	ownership, ownershipErr := s.operations.MachineOwnership(ctx, allocation.ID)
	if !bindingPresent || !bindingValid || !validComputePoolHeadTerminalizationBinding(operation, binding) || !ledgerPresent || !ledgerValid ||
		ownershipErr != nil || ownership.Status != "quarantined" || !validComputeClaimRecoveryOwnership(allocation, ownership) {
		return computePoolHeadTerminalizationCandidate{}, fmt.Errorf("%w: terminalization_binding_invalid", ErrComputePoolHeadTerminalizationUnavailable)
	}
	bindingDigest := computeClaimIdentityDigest(binding.LaunchOperationID + "|" + binding.IdempotencyKey + "|" + binding.TargetHash + "|" + binding.RequestHash)
	_, _, ledgerDigest := computeClaimMutationLedgerEvidence(operation)
	approvalDigest := hashInput(struct {
		SchemaVersion  int
		OperationID    string
		ResourceID     string
		Status         string
		RequestHash    string
		IdempotencyKey string
		ComputePoolKey string
		Allocation     ComputeAllocation
		Plan           ComputeAllocationPreparation
		Ownership      MachineOwnership
		Binding        computeClaimRecoveryBinding
		Ledger         computeClaimRecoveryMutationLedger
	}{1, operation.OperationID, operation.ResourceID, operation.Status, operation.RequestHash, operation.IdempotencyKey, operation.ComputePoolKey, allocation, plan, ownership, binding, ledger})
	readback := ComputePoolHeadTerminalizationReadback{
		SchemaVersion: 1, Status: "candidate", HeadStatus: operation.Status, AllocationStatus: allocation.Status, OwnershipStatus: ownership.Status,
		ApprovalDigest: approvalDigest, BindingDigest: bindingDigest, ManualRecoveryLedgerDigest: ledgerDigest,
	}
	return computePoolHeadTerminalizationCandidate{operation: operation, allocation: allocation, plan: plan, ownership: ownership, binding: binding, ledger: ledger, readback: readback}, nil
}

func (s *Service) TerminalizeComputePoolHead(ctx context.Context, input ComputePoolHeadTerminalizationInput) (ComputePoolHeadTerminalizationReadback, error) {
	if !validComputePoolNodePoolID(input.NodePoolID) || !validComputePoolTerminalizationToken(input.ApprovalID) ||
		input.IdempotencyKey != input.ApprovalID || !validComputePoolTerminalizationToken(input.IdempotencyKey) || !validSHA256Hex(input.ApprovalDigest) {
		return ComputePoolHeadTerminalizationReadback{}, ErrInvalidComputePoolHeadTerminalization
	}
	if replay, found, err := s.computePoolHeadTerminalizationReplay(ctx, input); found || err != nil {
		return replay, err
	}
	candidate, err := s.computePoolHeadTerminalizationCandidate(ctx, input.NodePoolID)
	if err != nil {
		return ComputePoolHeadTerminalizationReadback{}, err
	}
	if subtle.ConstantTimeCompare([]byte(candidate.readback.ApprovalDigest), []byte(input.ApprovalDigest)) != 1 {
		return ComputePoolHeadTerminalizationReadback{}, ErrComputePoolHeadTerminalizationConflict
	}
	approval := input
	if err := terminalizeComputeClaimPendingWithApproval(ctx, s, candidate.operation, candidate.allocation, candidate.plan, "compute_claim_finalization", "operator_terminalized", nil, nil, &approval); err != nil {
		if replay, found, replayErr := s.computePoolHeadTerminalizationReplay(ctx, input); found || replayErr != nil {
			return replay, replayErr
		}
		return ComputePoolHeadTerminalizationReadback{}, err
	}
	result := candidate.readback
	result.Status, result.HeadStatus, result.TerminalStatus = "succeeded", "failed", "terminal_unprovable"
	return result, nil
}

func (s *Service) ReadComputePoolHeadTerminalizationResult(ctx context.Context, input ComputePoolHeadTerminalizationInput) (ComputePoolHeadTerminalizationReadback, error) {
	if !validComputePoolNodePoolID(input.NodePoolID) || !validComputePoolTerminalizationToken(input.ApprovalID) ||
		input.IdempotencyKey != input.ApprovalID || !validSHA256Hex(input.ApprovalDigest) {
		return ComputePoolHeadTerminalizationReadback{}, ErrInvalidComputePoolHeadTerminalization
	}
	if replay, found, err := s.computePoolHeadTerminalizationReplay(ctx, input); found || err != nil {
		return replay, err
	}
	candidate, err := s.computePoolHeadTerminalizationCandidate(ctx, input.NodePoolID)
	if err != nil {
		return ComputePoolHeadTerminalizationReadback{}, err
	}
	if subtle.ConstantTimeCompare([]byte(candidate.readback.ApprovalDigest), []byte(input.ApprovalDigest)) != 1 {
		return ComputePoolHeadTerminalizationReadback{}, ErrComputePoolHeadTerminalizationConflict
	}
	result := candidate.readback
	result.Status = "pending"
	return result, nil
}

func (s *Service) computePoolHeadTerminalizationReplay(ctx context.Context, input ComputePoolHeadTerminalizationInput) (ComputePoolHeadTerminalizationReadback, bool, error) {
	operation, found, err := s.operations.ComputeClaimTerminalOperation(ctx, input.ApprovalID, input.IdempotencyKey)
	if err != nil {
		if errors.Is(err, ErrOperationIdentityConflict) {
			return ComputePoolHeadTerminalizationReadback{}, true, ErrComputePoolHeadTerminalizationConflict
		}
		return ComputePoolHeadTerminalizationReadback{}, false, err
	}
	if !found {
		return ComputePoolHeadTerminalizationReadback{}, false, nil
	}
	evidence, present, valid := decodeComputeClaimTerminalEvidence(operation)
	if !present || !valid {
		return ComputePoolHeadTerminalizationReadback{}, true, fmt.Errorf("%w: terminalization_evidence_unknown", ErrComputePoolHeadTerminalizationUnavailable)
	}
	if evidence.OperatorApprovalID != input.ApprovalID || evidence.OperatorIdempotencyKey != input.IdempotencyKey || evidence.OperatorApprovalDigest != input.ApprovalDigest ||
		operation.ComputePoolKey != input.NodePoolID || operation.Status != "failed" {
		return ComputePoolHeadTerminalizationReadback{}, true, ErrComputePoolHeadTerminalizationConflict
	}
	return ComputePoolHeadTerminalizationReadback{
		SchemaVersion: 1, Status: "succeeded", HeadStatus: "failed", AllocationStatus: "quarantined", OwnershipStatus: "quarantined",
		TerminalStatus: "terminal_unprovable", ApprovalDigest: evidence.OperatorApprovalDigest, BindingDigest: evidence.BindingDigest,
		ManualRecoveryLedgerDigest: evidence.ManualRecoveryLedgerDigest, Replayed: true,
	}, true, nil
}

func validComputePoolNodePoolID(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 200 && !strings.ContainsAny(value, "\r\n\t ")
}

func validComputePoolTerminalizationToken(value string) bool {
	if len(value) < 16 || len(value) > 120 || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if character != '-' && character != '_' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func validComputePoolHeadTerminalizationBinding(operation FabricOperation, binding computeClaimRecoveryBinding) bool {
	launchOperationID, ok := strings.CutSuffix(strings.TrimSpace(operation.IdempotencyKey), ":compute")
	return ok && launchOperationID != "" && binding.LaunchOperationID == launchOperationID &&
		binding.IdempotencyKey != "" && binding.IdempotencyKey == strings.TrimSpace(binding.IdempotencyKey) && len(binding.IdempotencyKey) <= 200 &&
		validSHA256Hex(binding.TargetHash) && validSHA256Hex(binding.RequestHash)
}

func validSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func decodeComputeClaimTerminalEvidence(operation FabricOperation) (ComputeClaimTerminalEvidence, bool, bool) {
	value, ok := operation.RedactedProviderPayload[computeClaimTerminalEvidencePayloadKey]
	if !ok {
		return ComputeClaimTerminalEvidence{}, false, false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return ComputeClaimTerminalEvidence{}, true, false
	}
	var evidence ComputeClaimTerminalEvidence
	if json.Unmarshal(body, &evidence) != nil || !validComputeClaimTerminalEvidence(evidence) {
		return ComputeClaimTerminalEvidence{}, true, false
	}
	return evidence, true, true
}

func withComputeClaimTerminalEvidence(payload map[string]any, evidence ComputeClaimTerminalEvidence) map[string]any {
	result := maps.Clone(payload)
	if result == nil {
		result = map[string]any{}
	}
	result[computeClaimTerminalEvidencePayloadKey] = map[string]any{
		"schemaVersion": evidence.SchemaVersion, "stage": evidence.Stage, "status": evidence.Status,
		"errorCode": evidence.ErrorCode, "reason": evidence.Reason, "readbackStatus": evidence.ReadbackStatus,
		"attemptCount": evidence.AttemptCount, "attempted": evidence.Attempted, "confirmed": evidence.Confirmed,
		"unknown": evidence.Unknown, "max": evidence.Max, "startedAt": evidence.StartedAt, "finishedAt": evidence.FinishedAt,
		"fabricRecordId": evidence.FabricRecordID, "operationId": evidence.OperationID, "idempotencyKey": evidence.IdempotencyKey, "requestHash": evidence.RequestHash,
		"launchOperationId": evidence.LaunchOperationID, "accountId": evidence.AccountID, "workspaceId": evidence.WorkspaceID,
		"computeAllocationId": evidence.ComputeAllocationID, "storageVolumeId": evidence.StorageVolumeID, "packageId": evidence.PackageID,
		"poolId": evidence.PoolID, "nodePoolId": evidence.NodePoolID, "machineName": evidence.MachineName,
		"nodeName": evidence.NodeName, "cvmInstanceId": evidence.CVMInstanceID, "cvmOwnershipState": evidence.CVMOwnershipState,
		"nodeOwnershipState": evidence.NodeOwnershipState, "bindingDigest": evidence.BindingDigest,
		"operatorApprovalId": evidence.OperatorApprovalID, "operatorApprovalDigest": evidence.OperatorApprovalDigest,
		"operatorIdempotencyKey": evidence.OperatorIdempotencyKey, "manualRecoveryLedgerDigest": evidence.ManualRecoveryLedgerDigest,
		"evidence": evidence.Evidence, "stageBudgets": evidence.StageBudgets,
	}
	return result
}

func validComputeClaimTerminalEvidence(evidence ComputeClaimTerminalEvidence) bool {
	if evidence.SchemaVersion != 1 || evidence.Status != "terminal_unprovable" || evidence.Stage == "" || evidence.ErrorCode == "" ||
		evidence.ReadbackStatus == "" || evidence.FabricRecordID == "" || evidence.OperationID == "" || evidence.IdempotencyKey == "" || evidence.RequestHash == "" ||
		evidence.AccountID == "" || evidence.WorkspaceID == "" || evidence.ComputeAllocationID == "" || evidence.PackageID == "" || evidence.NodePoolID == "" ||
		evidence.StartedAt == "" || evidence.FinishedAt == "" || evidence.AttemptCount < 0 || evidence.Attempted < 0 || evidence.Confirmed < 0 || evidence.Unknown < 0 || evidence.Max < 0 ||
		!validComputeClaimTerminalStage(evidence.Stage) || !validComputeClaimTerminalReadback(evidence.ReadbackStatus) {
		return false
	}
	if safeComputeClaimTerminalToken(evidence.ErrorCode) != evidence.ErrorCode || evidence.Reason != "" && safeComputeClaimTerminalToken(evidence.Reason) != evidence.Reason {
		return false
	}
	operatorFields := []string{evidence.OperatorApprovalID, evidence.OperatorApprovalDigest, evidence.OperatorIdempotencyKey, evidence.ManualRecoveryLedgerDigest}
	operatorFieldCount := 0
	for _, field := range operatorFields {
		if field != "" {
			operatorFieldCount++
		}
	}
	if operatorFieldCount != 0 && (operatorFieldCount != len(operatorFields) || !validComputePoolTerminalizationToken(evidence.OperatorApprovalID) ||
		evidence.OperatorApprovalID != evidence.OperatorIdempotencyKey || !validSHA256Hex(evidence.OperatorApprovalDigest) || !validSHA256Hex(evidence.ManualRecoveryLedgerDigest)) {
		return false
	}
	started, startedErr := time.Parse(time.RFC3339Nano, evidence.StartedAt)
	finished, finishedErr := time.Parse(time.RFC3339Nano, evidence.FinishedAt)
	if startedErr != nil || finishedErr != nil || finished.Before(started) || evidence.Attempted > evidence.Max || evidence.Confirmed > evidence.Attempted || evidence.Unknown > evidence.Attempted || evidence.Confirmed+evidence.Unknown > evidence.Attempted || evidence.AttemptCount != evidence.Attempted {
		return false
	}
	if evidence.StageBudgets != nil {
		for stage, budget := range evidence.StageBudgets {
			if stage != "compute_claim_cvm" && stage != "compute_claim_node" || budget.Max != 1 || budget.Attempted != 1 || budget.Confirmed < 0 || budget.Confirmed > budget.Attempted || budget.Unknown < 0 || budget.Unknown > budget.Attempted || budget.Confirmed+budget.Unknown != budget.Attempted {
				return false
			}
		}
	}
	return true
}

func validComputeClaimTerminalStage(stage string) bool {
	switch stage {
	case "compute_claim_cvm", "compute_claim_node", "compute_claim_finalization":
		return true
	default:
		return false
	}
}

func validComputeClaimTerminalReadback(status string) bool {
	switch status {
	case "not_attempted", "unavailable", "mismatch", "unallocated", "target_owned", "ownership_unavailable", "operator_terminalized":
		return true
	default:
		return false
	}
}

func terminalComputeClaimBinding(operation FabricOperation, allocation ComputeAllocation, plan ComputeAllocationPreparation) computeClaimRecoveryBinding {
	launchOperationID, ok := strings.CutSuffix(strings.TrimSpace(operation.IdempotencyKey), ":compute")
	if !ok || launchOperationID == "" {
		launchOperationID = strings.TrimSpace(operation.IdempotencyKey)
	}
	target := struct {
		MachineName   string `json:"machineName"`
		NodeName      string `json:"nodeName"`
		CVMInstanceID string `json:"cvmInstanceId"`
		PrivateIP     string `json:"privateIp"`
		InstanceType  string `json:"instanceType"`
		Zone          string `json:"zone"`
	}{allocation.MachineName, allocation.NodeName, firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID), allocation.PrivateIP, firstNonEmpty(plan.InstanceType, allocation.InstanceType), allocation.Zone}
	return computeClaimRecoveryBinding{
		LaunchOperationID: launchOperationID, IdempotencyKey: operation.IdempotencyKey,
		TargetHash: hashInput(target), RequestHash: operation.RequestHash,
	}
}

func terminalComputeClaimEvidence(operation FabricOperation, allocation ComputeAllocation, plan ComputeAllocationPreparation, stage, readbackStatus string, cause error, cvmBudget, nodeBudget normalLaunchMutationBudget, now time.Time, binding computeClaimRecoveryBinding, proof *ComputeClaimProviderProof) ComputeClaimTerminalEvidence {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	startedAt := operation.StartedAt
	if startedAt.IsZero() {
		startedAt = now
	}
	code := safeComputeClaimTerminalToken(errorCode(cause))
	if code == "" || code == "provider_error" {
		code = "unprovable"
	}
	code = "compute_claim_terminal_" + strings.TrimPrefix(stage, "compute_claim_") + "_" + code
	if len(code) > 120 {
		code = "compute_claim_terminal_unprovable"
	}
	reason := safeComputeClaimTerminalToken(errorCode(cause))
	evidence := ComputeClaimTerminalEvidence{
		SchemaVersion: 1, Stage: stage, Status: "terminal_unprovable", ErrorCode: code, Reason: reason,
		ReadbackStatus: readbackStatus, Attempted: cvmBudget.Attempted + nodeBudget.Attempted,
		Confirmed: cvmBudget.Confirmed + nodeBudget.Confirmed, Unknown: cvmBudget.Unknown + nodeBudget.Unknown,
		Max: cvmBudget.Max + nodeBudget.Max, StartedAt: startedAt.UTC().Format(time.RFC3339Nano), FinishedAt: now.UTC().Format(time.RFC3339Nano),
		FabricRecordID: operation.ID, OperationID: operation.OperationID, IdempotencyKey: operation.IdempotencyKey, RequestHash: operation.RequestHash,
		AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, ComputeAllocationID: operation.ResourceID,
		PackageID: allocation.PackageID, NodePoolID: allocation.NodePoolID, MachineName: allocation.MachineName,
		NodeName: allocation.NodeName, CVMInstanceID: firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID),
		BindingDigest: computeClaimIdentityDigest(binding.LaunchOperationID + "|" + binding.IdempotencyKey + "|" + binding.TargetHash + "|" + binding.RequestHash),
		StageBudgets:  map[string]ComputeClaimStageBudget{},
	}
	if cvmBudget.Max > 0 {
		evidence.StageBudgets["compute_claim_cvm"] = ComputeClaimStageBudget{Attempted: cvmBudget.Attempted, Confirmed: cvmBudget.Confirmed, Unknown: cvmBudget.Unknown, Max: cvmBudget.Max}
	}
	if nodeBudget.Max > 0 {
		evidence.StageBudgets["compute_claim_node"] = ComputeClaimStageBudget{Attempted: nodeBudget.Attempted, Confirmed: nodeBudget.Confirmed, Unknown: nodeBudget.Unknown, Max: nodeBudget.Max}
	}
	evidence.AttemptCount = evidence.Attempted
	if binding.LaunchOperationID != "" {
		evidence.LaunchOperationID = binding.LaunchOperationID
	}
	if proof != nil {
		evidence.CVMOwnershipState, evidence.NodeOwnershipState = proof.CVMOwnershipState, proof.NodeOwnershipState
	}
	return evidence
}

func safeComputeClaimTerminalToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var builder strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_' {
			builder.WriteRune(char)
		} else {
			return ""
		}
	}
	return builder.String()
}
