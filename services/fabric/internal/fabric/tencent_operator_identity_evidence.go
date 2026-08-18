package fabric

import (
	"context"
	"encoding/hex"
	"strings"
)

type computeClaimStorageOperationDisposition string

const (
	computeClaimStorageOperationAbsent   computeClaimStorageOperationDisposition = "absent"
	computeClaimStorageOperationExact    computeClaimStorageOperationDisposition = "exact"
	computeClaimStorageOperationUnknown  computeClaimStorageOperationDisposition = "attempted_unknown"
	computeClaimStorageOperationConflict computeClaimStorageOperationDisposition = "conflict"
)

func computeClaimRecoveryStorageOperationDisposition(operation FabricOperation, found bool, input ComputeClaimRecoveryInput) computeClaimStorageOperationDisposition {
	if !found {
		return computeClaimStorageOperationAbsent
	}
	if operation.ResourceKind != "storage_volume" || operation.ResourceID != input.StorageVolumeID ||
		operation.IdempotencyKey != input.LaunchOperationID+":storage" || operation.AccountID != input.AccountID ||
		operation.WorkspaceID != input.WorkspaceID {
		return computeClaimStorageOperationConflict
	}
	switch operation.Status {
	case "started", "failed", "succeeded":
	default:
		return computeClaimStorageOperationConflict
	}
	if operation.ID == "" || operation.OperationID == "" || operation.RequestHash == "" {
		return computeClaimStorageOperationUnknown
	}
	var storage StorageVolume
	if !decodeOperationResource(operation, &storage) || storage.ID != input.StorageVolumeID ||
		storage.OperationID != input.LaunchOperationID+":storage" || storage.AccountID != input.AccountID || storage.WorkspaceID != input.WorkspaceID {
		return computeClaimStorageOperationUnknown
	}
	return computeClaimStorageOperationExact
}

func (s *Service) ComputeClaimRecoveryIdentityEvidence(ctx context.Context, input ComputeClaimRecoveryClaimInput) (*ComputeClaimIdentityEvidence, error) {
	if !validComputeClaimRecoveryClaimInput(input) {
		return nil, ErrInvalidComputeClaimRecovery
	}
	operation, _, _, _, _, err := s.computeClaimRecoveryLocalState(ctx, input.ComputeClaimRecoveryInput)
	if err != nil {
		return nil, err
	}
	return computeClaimIdentityEvidence(operation, input), nil
}

func validComputeClaimRecoveryInput(input ComputeClaimRecoveryInput) bool {
	values := []string{input.LaunchOperationID, input.AccountID, input.WorkspaceID, input.ComputeAllocationID, input.StorageVolumeID, input.PackageID, input.PoolID, input.NodePoolID}
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) {
			return false
		}
	}
	return true
}

func validComputeClaimRecoveryLocalIdentity(input ComputeClaimRecoveryInput, allocation ComputeAllocation, plan ComputeAllocationPreparation) bool {
	persistedPeriodMonths := strings.TrimSpace(allocation.ProviderData["periodMonths"])
	if allocation.ID != input.ComputeAllocationID || allocation.AccountID != input.AccountID || allocation.WorkspaceID != input.WorkspaceID ||
		allocation.PackageID != input.PackageID || allocation.Provider != "tencent-tke" || allocation.PoolID != input.PoolID || allocation.NodePoolID != input.NodePoolID ||
		allocation.PoolID != plan.PoolID || plan.PackageID != input.PackageID || plan.NodePoolID != input.NodePoolID || plan.PoolID == "" || plan.InstanceType == "" ||
		plan.BeforeMachineNames == nil || plan.BaselineReplicas < 0 || plan.TargetReplicas != plan.BaselineReplicas+1 ||
		int64(len(plan.BeforeMachineNames)) != plan.BaselineReplicas || allocation.MachineName == "" || allocation.InstanceType != plan.InstanceType ||
		!strings.HasPrefix(firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID), "ins-") || allocation.NodeName == "" || allocation.PrivateIP == "" ||
		allocation.Zone == "" || allocation.ChargeType != "PREPAID" || allocation.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || allocation.Deadline == "" ||
		allocation.ProviderData["instanceType"] != plan.InstanceType || allocation.ProviderData["zone"] != allocation.Zone ||
		allocation.ProviderData["chargeType"] != "PREPAID" || (persistedPeriodMonths != "" && persistedPeriodMonths != "1") ||
		allocation.ProviderData["renewFlag"] != "NOTIFY_AND_MANUAL_RENEW" || allocation.ProviderData["deadline"] != allocation.Deadline ||
		allocation.ProviderData["machineName"] != allocation.MachineName {
		return false
	}
	seen := map[string]bool{}
	for _, name := range plan.BeforeMachineNames {
		if name == "" || seen[name] || name == allocation.MachineName {
			return false
		}
		seen[name] = true
	}
	return true
}

func validComputeClaimRecoveryOwnership(allocation ComputeAllocation, ownership MachineOwnership) bool {
	return ownership.ResourceID == allocation.ID && ownership.AccountID == allocation.AccountID && ownership.WorkspaceID == allocation.WorkspaceID &&
		ownership.PackageID == allocation.PackageID && ownership.NodePoolID == allocation.NodePoolID && ownership.MachineID == allocation.MachineName &&
		ownership.InstanceID == firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID) && ownership.NodeName == allocation.NodeName &&
		ownership.ReleasedAt == nil && (ownership.Status == "quarantined" || ownership.Status == "active")
}

func validComputeClaimProviderIdentityFailure(value *ComputeClaimProviderIdentityFailure) bool {
	if value == nil || !validComputeClaimProviderIdentityPredicate(value.Predicate) || value.ExpectedDigest == value.ActualDigest {
		return false
	}
	for _, digest := range []string{value.ExpectedDigest, value.ActualDigest} {
		if len(digest) != 64 {
			return false
		}
		if _, err := hex.DecodeString(digest); err != nil || strings.ToLower(digest) != digest {
			return false
		}
	}
	return true
}

func validComputeClaimProviderIdentityPredicate(value string) bool {
	switch value {
	case "compute_claim.request_contract", "compute_claim.machine_selection", "compute_claim.node_pool_identity",
		"compute_claim.machine_identity", "compute_claim.tke_instance_identity", "compute_claim.network_identity",
		"compute_claim.cvm_identity", "compute_claim.cvm_billing", "compute_claim.cvm_ownership_shape",
		"compute_claim.cvm_ownership.instance_name", "compute_claim.cvm_ownership.opl_account_id",
		"compute_claim.cvm_ownership.opl_workspace_id", "compute_claim.cvm_ownership.opl_resource_id",
		"compute_claim.cvm_ownership.opl_operation_id", "compute_claim.provider_response_identity",
		"compute_claim.kubernetes_node_identity":
		return true
	default:
		return false
	}
}

func cloneComputeClaimProviderIdentityFailure(value *ComputeClaimProviderIdentityFailure) *ComputeClaimProviderIdentityFailure {
	if !validComputeClaimProviderIdentityFailure(value) {
		return nil
	}
	clone := *value
	return &clone
}

func safeComputeClaimRecoveryReason(value, fallback string) string {
	switch value {
	case "local_identity", "provider_describe", "iam_rbac", "multiple_candidate", "identity_mismatch", "node_ownership_conflict", "storage_already_started":
		return value
	default:
		return fallback
	}
}

func validComputeClaimFailureStage(value string) bool {
	switch value {
	case "", "cvm_pre_read", "cvm_conflict_check", "cvm_mutation_precondition", "cvm_rename_readback", "cvm_tag_readback", "cvm_final_readback",
		"cvm_provisioner_transport", "cvm_mutation_evidence", "node_pre_cvm_read", "node_pre_read", "node_conflict_check", "node_patch_build",
		"node_patch_readback", "node_final_readback", "claim_final_readback":
		return true
	default:
		return false
	}
}

func validComputeClaimProviderErrorClass(value string) bool {
	switch value {
	case "", "client_unavailable", "malformed_readback", "ownership_conflict", "readback_mismatch", "timeout", "iam_rbac", "provider_error",
		"transport_error", "evidence_incomplete":
		return true
	default:
		return false
	}
}

func validComputeClaimMutationEvidence(evidence ComputeClaimMutationEvidence, count, maximum int, domain string) bool {
	return validComputeClaimMutationEvidenceShape(evidence, count, maximum, domain) && evidence.Unknown == 0 &&
		evidence.Confirmed == evidence.Attempted && len(evidence.Missing) == 0
}

func validComputeClaimMissingField(domain, field string) bool {
	switch domain {
	case "cvm":
		switch field {
		case "instance", "instance_name", "opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id":
			return true
		}
	case "node":
		return field == "node_ownership"
	}
	return false
}

func validComputeClaimMutationEvidenceShape(evidence ComputeClaimMutationEvidence, count, maximum int, domain string) bool {
	if count < 0 || count > maximum || evidence.Attempted != count || evidence.Confirmed < 0 || evidence.Confirmed > evidence.Attempted ||
		evidence.Unknown < 0 || evidence.Unknown > evidence.Attempted || evidence.Confirmed+evidence.Unknown > evidence.Attempted {
		return false
	}
	seen := map[string]bool{}
	for _, field := range evidence.Missing {
		if !validComputeClaimMissingField(domain, field) || seen[field] {
			return false
		}
		seen[field] = true
	}
	return true
}

func validComputeClaimRecoveryClaimInput(input ComputeClaimRecoveryClaimInput) bool {
	if !validComputeClaimRecoveryInput(input.ComputeClaimRecoveryInput) {
		return false
	}
	for _, value := range []string{input.MachineName, input.NodeName, input.CVMInstanceID, input.PrivateIP, input.InstanceType, input.Zone, input.IdempotencyKey} {
		if value == "" || value != strings.TrimSpace(value) {
			return false
		}
	}
	_, driftAttempt := confirmedNodeDriftAttemptDigest(input)
	return strings.HasPrefix(input.CVMInstanceID, "ins-") &&
		(input.IdempotencyKey == input.LaunchOperationID+":compute" || driftAttempt)
}

func confirmedNodeDriftAttemptDigest(input ComputeClaimRecoveryClaimInput) (string, bool) {
	digest, ok := strings.CutPrefix(input.IdempotencyKey, input.LaunchOperationID+":compute:confirmed-node-drift:")
	return digest, ok && validComputeClaimRecoveryDigest(digest)
}
