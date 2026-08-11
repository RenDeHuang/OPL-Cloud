package fabric

import (
	"context"
	"encoding/hex"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"time"
)

type computeClaimRecoveryProvider interface {
	ProveComputeClaimRecovery(context.Context, ComputeAllocation, ComputeAllocationPreparation, MachineOwnership) (ComputeClaimProviderProof, error)
}

type computeClaimRecoveryClaimProvider interface {
	ClaimComputeRecovery(context.Context, ComputeAllocation, ComputeAllocationPreparation, MachineOwnership) (ComputeClaimProviderClaim, error)
}

type computeClaimRecoveryNodeOnlyProvider interface {
	ClaimComputeRecoveryNodeOnly(context.Context, ComputeAllocation, ComputeAllocationPreparation, MachineOwnership) (ComputeClaimProviderClaim, error)
}

type storageRecoveryDiscoveryProvider interface {
	DiscoverStorageRecovery(context.Context, StorageVolumeInput) (StorageRecoveryDiscovery, error)
}

func (s *Service) ComputeProviderTruth(ctx context.Context, input ComputeClaimRecoveryInput) (ComputeProviderTruth, error) {
	input.AllowExistingStorageOperation = true
	s.mu.Lock()
	compute := cloneComputeAllocation(s.computes[strings.TrimSpace(input.ComputeAllocationID)])
	s.mu.Unlock()
	if compute.ID != strings.TrimSpace(input.ComputeAllocationID) {
		_, persisted, _, _, _, persistedErr := s.computeClaimRecoveryLocalState(ctx, input)
		if persistedErr == nil {
			compute = cloneComputeAllocation(persisted)
		}
	}
	truth := ComputeProviderTruth{
		SchemaVersion: 1, State: "unknown", ComputeState: "unknown", StorageState: "unknown", Compute: compute,
	}
	proof, err := s.ComputeClaimRecoveryProof(ctx, input)
	truth.Reason, truth.FailureStage, truth.ProviderErrorClass = proof.Reason, proof.FailureStage, proof.ProviderErrorClass
	truth.NodeOwnershipState, truth.CVMOwnershipState, truth.Proof = proof.NodeOwnershipState, proof.CVMOwnershipState, &proof
	truth.StorageState = normalizedComputeStorageState(proof.StorageState)
	truth.ProviderRequestID = compute.ProviderRequestID
	if err != nil {
		return truth, err
	}
	if !proof.Eligible || proof.Reason != "none" {
		return truth, fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, firstNonEmpty(proof.Reason, "provider_describe"))
	}
	// The proof already binds the persisted Fabric operation, MachineOwnership,
	// CVM, and Node. The process projection is optional output enrichment and
	// must not become a second authorization gate after those authorities agree.
	truth.Compute.ID, truth.Compute.AccountID, truth.Compute.WorkspaceID = proof.ComputeAllocationID, proof.AccountID, proof.WorkspaceID
	truth.Compute.PackageID, truth.Compute.Provider = proof.PackageID, "tencent-tke"
	truth.Compute.PoolID, truth.Compute.NodePoolID = proof.PoolID, proof.NodePoolID
	truth.State, truth.ComputeState = "ready", "ready"
	truth.Compute.Status = "ready"
	truth.Compute.MachineName, truth.Compute.NodeName = proof.MachineName, proof.NodeName
	truth.Compute.CVMInstanceID, truth.Compute.InstanceID = proof.CVMInstanceID, proof.CVMInstanceID
	truth.Compute.PrivateIP, truth.Compute.InstanceType, truth.Compute.Zone = proof.PrivateIP, proof.InstanceType, proof.Zone
	truth.Compute.ChargeType, truth.Compute.RenewFlag, truth.Compute.Deadline = proof.ChargeType, proof.RenewFlag, proof.Deadline
	truth.Compute.ProviderResourceID = proof.CVMInstanceID
	truth.Compute.ProviderRequestID = firstNonEmpty(truth.ProviderRequestID, compute.ProviderRequestID)
	return truth, nil
}

func normalizedComputeStorageState(value string) string {
	switch value {
	case "storage_not_started", "absent":
		return "absent"
	case "storage_existing_exact", "ready":
		return "ready"
	default:
		return "unknown"
	}
}

func (s *Service) ComputeClaimRecoveryProof(ctx context.Context, input ComputeClaimRecoveryInput) (ComputeClaimRecoveryProof, error) {
	proof := newComputeClaimRecoveryProof(input)
	if !validComputeClaimRecoveryInput(input) {
		proof.Reason = "local_identity"
		return proof, ErrInvalidComputeClaimRecovery
	}
	operations, err := s.operations.List(ctx)
	if err != nil {
		proof.Reason = "local_identity"
		return proof, fmt.Errorf("%w: local_identity", ErrComputeClaimRecoveryUnavailable)
	}
	computeOperations := make([]FabricOperation, 0, 1)
	for _, operation := range operations {
		if operation.Action == "create_compute_allocation" && (operation.ResourceID == input.ComputeAllocationID ||
			operation.IdempotencyKey == input.LaunchOperationID+":compute" || operation.AccountID == input.AccountID && operation.WorkspaceID == input.WorkspaceID) {
			computeOperations = append(computeOperations, operation)
		}
	}
	if len(computeOperations) != 1 {
		proof.Reason = "local_identity"
		return proof, fmt.Errorf("%w: local_identity", ErrComputeClaimRecoveryUnavailable)
	}
	operation := computeOperations[0]
	var allocation ComputeAllocation
	plan, hasPlan := decodeComputeAllocationPlan(operation)
	if operation.AccountID != input.AccountID || operation.WorkspaceID != input.WorkspaceID || operation.IdempotencyKey != input.LaunchOperationID+":compute" ||
		(operation.Status != "failed" && operation.Status != "claim_pending" && operation.Status != "succeeded") || !decodeOperationResource(operation, &allocation) || !hasPlan ||
		!validComputeClaimRecoveryLocalIdentity(input, allocation, plan) {
		proof.Reason = "local_identity"
		return proof, fmt.Errorf("%w: local_identity", ErrComputeClaimRecoveryUnavailable)
	}
	ownership, err := s.operations.MachineOwnership(ctx, input.ComputeAllocationID)
	if err != nil || !validComputeClaimRecoveryOwnership(allocation, ownership) {
		proof.Reason = "local_identity"
		return proof, fmt.Errorf("%w: local_identity", ErrComputeClaimRecoveryUnavailable)
	}
	provider, ok := s.provider.(computeClaimRecoveryProvider)
	if !ok {
		proof.Reason = "provider_describe"
		return proof, fmt.Errorf("%w: provider_describe", ErrComputeClaimRecoveryUnavailable)
	}
	providerProof, err := provider.ProveComputeClaimRecovery(ctx, allocation, plan, ownership)
	if err != nil {
		proof.Reason = safeComputeClaimRecoveryReason(providerProof.Reason, "provider_describe")
		if validComputeClaimProviderFailureEvidence(providerProof) {
			proof.FailureStage = providerProof.FailureStage
			proof.ProviderErrorClass = providerProof.ProviderErrorClass
			proof.ProviderIdentityFailure = cloneComputeClaimProviderIdentityFailure(providerProof.ProviderIdentityFailure)
		}
		return proof, fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, proof.Reason)
	}
	if !validComputeClaimProviderProof(providerProof, allocation, plan) {
		proof.Reason = safeComputeClaimRecoveryReason(providerProof.Reason, "identity_mismatch")
		return proof, fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, proof.Reason)
	}
	// Compute ownership is authoritative for the first incomplete stage. Read
	// CVM and Node before applying any later storage-operation disposition so an
	// attempted or conflicting storage record cannot hide Node ownership truth.
	applyComputeClaimRecoveryProviderProof(&proof, providerProof)
	if confirmedNodeDrift(operation, ownership, input, proof) {
		proof.RecoveryClassification = "confirmed_node_drift"
	}
	storageDisposition := computeClaimRecoveryStorageOperationDisposition(operations, input)
	if storageDisposition == computeClaimStorageOperationUnknown && !input.AllowExistingStorageOperation {
		proof.Reason = "storage_already_started"
		return proof, fmt.Errorf("%w: storage_already_started", ErrComputeClaimRecoveryUnavailable)
	}
	if storageDisposition == computeClaimStorageOperationConflict && !input.AllowExistingStorageOperation {
		proof.Reason = "identity_mismatch"
		return proof, fmt.Errorf("%w: identity_mismatch", ErrComputeClaimRecoveryUnavailable)
	}
	if input.AllowExistingStorageOperation && storageDisposition != computeClaimStorageOperationAbsent {
		// Storage is a later stage. Once the compute provider has proved the
		// original CVM and Node identity, an attempted or conflicting storage
		// record remains unknown and cannot block the safe Node-only continuation.
		// The storage worker must reconcile it before any CBS mutation.
		proof.Eligible, proof.Reason, proof.StorageState, proof.StorageProviderResourceID = true, "none", "storage_attempt_unknown", ""
		return proof, nil
	}
	storageProvider, ok := s.provider.(storageRecoveryDiscoveryProvider)
	if !ok {
		if !input.AllowExistingStorageOperation {
			proof.Reason = "provider_describe"
			return proof, fmt.Errorf("%w: provider_describe", ErrComputeClaimRecoveryUnavailable)
		}
		// A missing Storage readback capability is an unknown downstream stage;
		// it must not erase an already authoritative Compute proof.
		proof.Eligible, proof.Reason, proof.StorageState, proof.StorageProviderResourceID = true, "none", "storage_attempt_unknown", ""
		return proof, nil
	}
	storageInput := StorageVolumeInput{
		ID: input.StorageVolumeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeAllocationID,
		Zone: allocation.Zone, SizeGB: packagePlan(input.PackageID).DiskGB, IdempotencyKey: input.LaunchOperationID + ":storage",
	}
	storageOperation := newOperation(
		"create_storage_volume", "storage_volume", storageInput.ID, storageInput.AccountID, storageInput.WorkspaceID,
		storageInput.IdempotencyKey, hashInput(storageInput), s.now(),
	)
	storageInput.OperationID = storageOperation.OperationID
	storageDiscovery, err := storageProvider.DiscoverStorageRecovery(ctx, storageInput)
	if err != nil {
		if input.AllowExistingStorageOperation && storageDiscovery.MutationCount == 0 {
			// Storage readback is independent of the already-proved Compute
			// ownership. Preserve the unknown stage and let the launch worker
			// reconcile it before any storage mutation.
			proof.Eligible, proof.Reason, proof.StorageState, proof.StorageProviderResourceID = true, "none", "storage_attempt_unknown", ""
			return proof, nil
		}
		proof.Reason = safeComputeClaimRecoveryReason(storageDiscovery.Reason, "provider_describe")
		return proof, fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, proof.Reason)
	}
	if !validStorageRecoveryDiscovery(storageDiscovery) {
		if input.AllowExistingStorageOperation && storageDiscovery.MutationCount == 0 {
			proof.Eligible, proof.Reason, proof.StorageState, proof.StorageProviderResourceID = true, "none", "storage_attempt_unknown", ""
			return proof, nil
		}
		proof.Reason = safeComputeClaimRecoveryReason(storageDiscovery.Reason, "identity_mismatch")
		return proof, fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, proof.Reason)
	}
	proof.Eligible, proof.Reason, proof.StorageState = true, "none", storageDiscovery.State
	proof.StorageProviderResourceID = storageDiscovery.ProviderResourceID
	return proof, nil
}

func confirmedNodeDrift(operation FabricOperation, ownership MachineOwnership, input ComputeClaimRecoveryInput, proof ComputeClaimRecoveryProof) bool {
	persistedBinding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(operation)
	_, mutationPresent, _ := decodeComputeClaimRecoveryMutation(operation)
	claimInput := ComputeClaimRecoveryClaimInput{ComputeClaimRecoveryInput: input}
	claimInput.MachineName, claimInput.NodeName, claimInput.CVMInstanceID = proof.MachineName, proof.NodeName, proof.CVMInstanceID
	claimInput.PrivateIP, claimInput.InstanceType, claimInput.Zone = proof.PrivateIP, proof.InstanceType, proof.Zone
	claimInput.IdempotencyKey = input.LaunchOperationID + ":compute"
	return !mutationPresent && bindingPresent && bindingValid && validConfirmedNodeDriftAuthority(operation, ownership, claimInput, persistedBinding) &&
		proof.NodeOwnershipState == "unallocated" &&
		(proof.CVMOwnershipState == "recoverable" || proof.CVMOwnershipState == "target_owned")
}

func applyComputeClaimRecoveryProviderProof(proof *ComputeClaimRecoveryProof, providerProof ComputeClaimProviderProof) {
	proof.MachineName, proof.NodeName, proof.CVMInstanceID = providerProof.MachineName, providerProof.NodeName, providerProof.CVMInstanceID
	proof.PrivateIP, proof.InstanceType, proof.Zone = providerProof.PrivateIP, providerProof.InstanceType, providerProof.Zone
	proof.ChargeType, proof.PeriodMonths, proof.RenewFlag, proof.Deadline = providerProof.ChargeType, providerProof.PeriodMonths, providerProof.RenewFlag, providerProof.Deadline
	proof.NodeOwnershipState, proof.CVMOwnershipState = providerProof.NodeOwnershipState, providerProof.CVMOwnershipState
}

type computeClaimStorageOperationDisposition string

const (
	computeClaimStorageOperationAbsent   computeClaimStorageOperationDisposition = "absent"
	computeClaimStorageOperationExact    computeClaimStorageOperationDisposition = "exact"
	computeClaimStorageOperationUnknown  computeClaimStorageOperationDisposition = "attempted_unknown"
	computeClaimStorageOperationConflict computeClaimStorageOperationDisposition = "conflict"
)

func computeClaimRecoveryStorageOperationDisposition(operations []FabricOperation, input ComputeClaimRecoveryInput) computeClaimStorageOperationDisposition {
	matches := make([]FabricOperation, 0, 1)
	for _, operation := range operations {
		if operation.Action == "create_storage_volume" &&
			(operation.ResourceID == input.StorageVolumeID || operation.IdempotencyKey == input.LaunchOperationID+":storage" ||
				operation.AccountID == input.AccountID && operation.WorkspaceID == input.WorkspaceID) {
			matches = append(matches, operation)
		}
	}
	if len(matches) == 0 {
		return computeClaimStorageOperationAbsent
	}
	if len(matches) != 1 {
		return computeClaimStorageOperationConflict
	}
	operation := matches[0]
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

func validStorageRecoveryDiscovery(discovery StorageRecoveryDiscovery) bool {
	if discovery.MutationCount != 0 || strings.TrimSpace(discovery.ProviderRequestID) == "" {
		return false
	}
	switch discovery.State {
	case "storage_not_started":
		return discovery.ProviderResourceID == "" && discovery.Reason == ""
	case "storage_existing_exact":
		return strings.HasPrefix(discovery.ProviderResourceID, "disk-") && discovery.Reason == ""
	default:
		return false
	}
}

func newComputeClaimRecoveryProof(input ComputeClaimRecoveryInput) ComputeClaimRecoveryProof {
	return ComputeClaimRecoveryProof{
		SchemaVersion: 1, Reason: "local_identity", StorageState: "unknown", LaunchOperationID: input.LaunchOperationID,
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ComputeAllocationID: input.ComputeAllocationID,
		StorageVolumeID: input.StorageVolumeID, PackageID: input.PackageID, NodePoolID: input.NodePoolID,
		PoolID: input.PoolID, Evidence: &ComputeClaimEvidence{},
	}
}

func validComputeClaimRecoveryInput(input ComputeClaimRecoveryInput) bool {
	values := []string{input.LaunchOperationID, input.AccountID, input.WorkspaceID, input.ComputeAllocationID, input.StorageVolumeID, input.PackageID, input.PoolID, input.NodePoolID}
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) {
			return false
		}
	}
	return input.PackageID == "basic" || input.PackageID == "pro"
}

func validComputeClaimRecoveryLocalIdentity(input ComputeClaimRecoveryInput, allocation ComputeAllocation, plan ComputeAllocationPreparation) bool {
	persistedPeriodMonths := strings.TrimSpace(allocation.ProviderData["periodMonths"])
	if allocation.ID != input.ComputeAllocationID || allocation.AccountID != input.AccountID || allocation.WorkspaceID != input.WorkspaceID ||
		allocation.PackageID != input.PackageID || allocation.Provider != "tencent-tke" || allocation.PoolID != input.PoolID || allocation.NodePoolID != input.NodePoolID ||
		allocation.PoolID != plan.PoolID || plan.PackageID != input.PackageID || plan.NodePoolID != input.NodePoolID || plan.PoolID != packagePlan(input.PackageID).ID ||
		plan.InstanceType != packagePlan(input.PackageID).InstanceType || plan.BeforeMachineNames == nil || plan.BaselineReplicas < 0 || plan.TargetReplicas != plan.BaselineReplicas+1 ||
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

func validComputeClaimProviderProof(proof ComputeClaimProviderProof, allocation ComputeAllocation, plan ComputeAllocationPreparation) bool {
	deadline, deadlineErr := time.Parse(time.RFC3339, proof.Deadline)
	return proof.Status == "proven" && (proof.NodeOwnershipState == "unallocated" || proof.NodeOwnershipState == "target_owned") &&
		(proof.CVMOwnershipState == "recoverable" || proof.CVMOwnershipState == "target_owned") &&
		proof.MachineName == allocation.MachineName && proof.NodeName == allocation.NodeName && proof.CVMInstanceID == firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID) &&
		proof.PrivateIP == allocation.PrivateIP && proof.InstanceType == plan.InstanceType && proof.Zone == allocation.Zone && proof.ChargeType == "PREPAID" &&
		proof.PeriodMonths == 1 && proof.RenewFlag == "NOTIFY_AND_MANUAL_RENEW" && proof.Deadline == allocation.Deadline && deadlineErr == nil && !deadline.IsZero() &&
		proof.FailureStage == "" && proof.ProviderErrorClass == "" && proof.ProviderIdentityFailure == nil
}

func validComputeClaimProviderFailureEvidence(proof ComputeClaimProviderProof) bool {
	return proof.Reason == "identity_mismatch" && proof.FailureStage != "" && validComputeClaimFailureStage(proof.FailureStage) &&
		proof.ProviderErrorClass != "" && validComputeClaimProviderErrorClass(proof.ProviderErrorClass) &&
		validComputeClaimProviderIdentityFailure(proof.ProviderIdentityFailure)
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

func validComputeClaimEvidence(claim ComputeClaimProviderClaim) bool {
	return claim.Evidence != nil && validComputeClaimFailureStage(claim.FailureStage) && validComputeClaimProviderErrorClass(claim.ProviderErrorClass) &&
		validComputeClaimMutationEvidence(claim.Evidence.CVM, claim.TencentMutationCount, 5, "cvm") &&
		validComputeClaimMutationEvidence(claim.Evidence.Node, claim.KubernetesMutationCount, 1, "node") &&
		claim.FailureStage == "" && claim.ProviderErrorClass == ""
}

func (s *Service) ClaimComputeRecovery(ctx context.Context, input ComputeClaimRecoveryClaimInput) (ComputeClaimRecoveryProof, error) {
	result := newComputeClaimRecoveryProof(input.ComputeClaimRecoveryInput)
	if !validComputeClaimRecoveryClaimInput(input) {
		result.Reason = "local_identity"
		return result, ErrInvalidComputeClaimRecovery
	}
	driftAttemptDigest, driftAttempt := confirmedNodeDriftAttemptDigest(input)
	err := s.operations.WithPoolLock(ctx, workspaceLaunchResourceLockKey(input.LaunchOperationID), func(lockCtx context.Context) error {
		operation, allocation, plan, ownership, localReason, err := s.computeClaimRecoveryLocalState(lockCtx, input.ComputeClaimRecoveryInput)
		if err != nil {
			result.Eligible, result.Reason = false, safeComputeClaimRecoveryReason(localReason, "local_identity")
			return err
		}
		if driftAttempt {
			result, err = s.claimApprovedConfirmedNodeDrift(lockCtx, input, operation, allocation, plan, ownership, driftAttemptDigest)
			return err
		}
		binding := newComputeClaimRecoveryBinding(input)
		persistedBinding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(operation)
		mutationLedger, mutationPresent, mutationValid := decodeComputeClaimRecoveryMutation(operation)
		reconciliation, reconciliationPresent, reconciliationValid := decodeComputeClaimRecoveryReconciliation(operation)
		clientRejection, clientRejectionPresent, clientRejectionValid := decodeComputeClaimNodeClientRejectionRecovery(operation)
		historicalBinding := bindingPresent && bindingValid && persistedBinding != binding &&
			persistedBinding == historicalComputeClaimRecoveryBinding(input)
		reconciliationProvenance, requestHashBinding := isolatedRequestHashReconciliationProvenance(operation, input, persistedBinding, bindingPresent, bindingValid)
		reconciliationCandidate := requestHashBinding && allocation.Status == "quarantined" && ownership.Status == "quarantined" &&
			(operation.Status == "claim_pending" && reconciliationProvenance.SchemaVersion == 1 ||
				operation.Status == "failed" && reconciliationProvenance.SchemaVersion == 2)
		reconciliationReplay := requestHashBinding && reconciliationPresent && reconciliationValid &&
			(reconciliation.SchemaVersion == 1 && mutationPresent && mutationValid || reconciliation.SchemaVersion == 2 && !mutationPresent) &&
			computeClaimRecoveryReconciliationMatches(reconciliation, operation, input, persistedBinding, mutationLedger)
		requestHashReconciliation := reconciliationCandidate || reconciliationReplay
		historicalWithoutLedger := historicalBinding && !mutationPresent
		historicalNodeContinuation := historicalBinding && mutationPresent && mutationValid && confirmedCVMOnlyObservedComputeClaimRecoveryMutation(mutationLedger)
		historicalReservedReplay := historicalBinding && mutationPresent && mutationValid && validNodeReservedComputeClaimRecoveryMutation(mutationLedger)
		historicalCompletedReplay := historicalBinding && mutationPresent && mutationValid && successfulNodeClaimRecoveryMutation(mutationLedger) && ownership.Status == "active"
		if bindingPresent && (!bindingValid || persistedBinding != binding && !historicalWithoutLedger && !historicalNodeContinuation && !historicalReservedReplay && !historicalCompletedReplay &&
			!requestHashReconciliation) {
			result.Eligible, result.Reason = false, "identity_mismatch"
			return ErrComputeClaimRecoveryIdempotencyConflict
		}
		if mutationPresent && (!mutationValid || !bindingPresent) || reconciliationPresent && (!reconciliationValid || !reconciliationReplay) ||
			clientRejectionPresent && (!clientRejectionValid || !reconciliationPresent) {
			result.Eligible, result.Reason = false, "identity_mismatch"
			return ErrComputeClaimRecoveryIdempotencyConflict
		}
		proof, proofErr := s.ComputeClaimRecoveryProof(lockCtx, input.ComputeClaimRecoveryInput)
		result = proof
		if proofErr != nil {
			if mutationPresent && result.Reason != "local_identity" && result.Reason != "storage_already_started" {
				readbackReason := result.Reason
				applyComputeClaimRecoveryReplayFailure(&result, mutationLedger, readbackReason)
				return fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, result.Reason)
			}
			return proofErr
		}
		if input.MachineName != proof.MachineName || input.NodeName != proof.NodeName || input.CVMInstanceID != proof.CVMInstanceID || input.PrivateIP != proof.PrivateIP ||
			input.InstanceType != proof.InstanceType || input.Zone != proof.Zone || input.PoolID != proof.PoolID {
			result.Eligible, result.Reason = false, "identity_mismatch"
			return fmt.Errorf("%w: identity_mismatch", ErrComputeClaimRecoveryUnavailable)
		}
		reconciledCVMOwnership := requestHashReconciliation &&
			(proof.CVMOwnershipState == "recoverable" || proof.CVMOwnershipState == "target_owned")
		if requestHashReconciliation {
			if !reconciledCVMOwnership || (proof.NodeOwnershipState != "unallocated" && proof.NodeOwnershipState != "target_owned") {
				result.Eligible, result.Reason = false, "identity_mismatch"
				return fmt.Errorf("%w: identity_mismatch", ErrComputeClaimRecoveryUnavailable)
			}
			expectedReconciliation := newComputeClaimRecoveryReconciliation(operation, allocation, plan, ownership, input, proof, persistedBinding, mutationLedger)
			if reconciliationPresent {
				if reconciliation.AuthorityDigest != expectedReconciliation.AuthorityDigest {
					result.Eligible, result.Reason = false, "identity_mismatch"
					return ErrComputeClaimRecoveryIdempotencyConflict
				}
			} else {
				verified := operation
				if verified.Status == "failed" {
					verified.Status, verified.ErrorCode, verified.FinishedAt = "claim_pending", "", time.Time{}
				}
				verified.RedactedProviderPayload = withComputeClaimRecoveryReconciliation(verified.RedactedProviderPayload, expectedReconciliation)
				if err := s.operations.SaveComputeClaimRecovery(lockCtx, operation, verified); err != nil {
					result.Eligible, result.Reason = false, "local_identity"
					return err
				}
				operation, reconciliation, reconciliationPresent = verified, expectedReconciliation, true
			}
		}
		if !bindingPresent {
			if operation.Status == "succeeded" {
				result.Eligible, result.Reason = false, "identity_mismatch"
				return ErrComputeClaimRecoveryIdempotencyConflict
			}
			pending := operation
			pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
			pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(operation.RedactedProviderPayload, binding)
			if err := s.operations.SaveComputeClaimRecovery(lockCtx, operation, pending); err != nil {
				result.Eligible, result.Reason = false, "local_identity"
				return err
			}
			operation = pending
			bindingPresent, bindingValid = true, true
		}
		activeHistoricalNodeContinuation := historicalWithoutLedger && input.NodeOnlyContinuation && operation.Status == "succeeded" &&
			ownership.Status == "active" && (proof.CVMOwnershipState == "recoverable" || proof.CVMOwnershipState == "target_owned") &&
			proof.NodeOwnershipState == "unallocated"
		reserveHistoricalNodeClaim := historicalWithoutLedger && (ownership.Status != "active" &&
			proof.CVMOwnershipState == "target_owned" && proof.NodeOwnershipState == "unallocated" || activeHistoricalNodeContinuation)
		if historicalWithoutLedger && !reserveHistoricalNodeClaim {
			result.Eligible, result.Reason = false, "identity_mismatch"
			return ErrComputeClaimRecoveryIdempotencyConflict
		}
		if mutationPresent && mutationLedger.State == "reserved" {
			applyComputeClaimRecoveryMutation(&result, mutationLedger)
			return fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, result.Reason)
		}
		resumeObservedNodeClaim := mutationPresent &&
			(recoverableObservedComputeClaimRecoveryMutation(mutationLedger) || historicalNodeContinuation) &&
			proof.CVMOwnershipState == "target_owned" && proof.NodeOwnershipState == "unallocated"
		legacyClientRejectedNodeCall := requestHashReconciliation && reconciliationPresent && !clientRejectionPresent &&
			exactLegacyKubectlClientRejectedReconciliation(reconciliation) && reconciledCVMOwnership && proof.NodeOwnershipState == "unallocated"
		reconciledNodeContinuation := requestHashReconciliation && reconciliationPresent &&
			(reconciliation.State == "verified" || legacyClientRejectedNodeCall) &&
			ownership.Status != "active" && reconciledCVMOwnership && proof.NodeOwnershipState == "unallocated"
		requestedNodeContinuation := input.NodeOnlyContinuation && !mutationPresent &&
			(operation.Status == "claim_pending" || operation.Status == "succeeded" && ownership.Status == "active" &&
				bindingPresent && bindingValid && persistedBinding == binding) &&
			(proof.CVMOwnershipState == "recoverable" || proof.CVMOwnershipState == "target_owned") && proof.NodeOwnershipState == "unallocated"
		activeNodeContinuation := ownership.Status == "active" && !mutationPresent && bindingPresent && bindingValid && persistedBinding == binding &&
			proof.NodeOwnershipState == "unallocated" && (requestedNodeContinuation || proof.CVMOwnershipState == "target_owned")
		activeNodeContinuation = activeNodeContinuation || activeHistoricalNodeContinuation
		completedNodeOnlyReadback := ownership.Status == "active" && mutationPresent && mutationValid &&
			successfulNodeClaimRecoveryMutation(mutationLedger) && mutationLedger.TencentMutationCount == 0 &&
			reflect.DeepEqual(mutationLedger.Evidence.CVM, ComputeClaimMutationEvidence{}) &&
			(proof.CVMOwnershipState == "recoverable" || proof.CVMOwnershipState == "target_owned") && proof.NodeOwnershipState == "target_owned"
		createBudget, createPresent, createValid := normalLaunchStageBudget(operation.RedactedProviderPayload, "compute_create")
		cvmBudget, cvmPresent, cvmValid := normalLaunchStageBudget(operation.RedactedProviderPayload, "compute_claim_cvm")
		_, nodePresent, nodeValid := normalLaunchStageBudget(operation.RedactedProviderPayload, "compute_claim_node")
		currentNodeContinuation := operation.Status == "claim_pending" && !mutationPresent &&
			proof.CVMOwnershipState == "target_owned" && proof.NodeOwnershipState == "unallocated" &&
			createPresent && createValid && createBudget == confirmedNormalLaunchMutationBudget() &&
			cvmPresent && cvmValid && cvmBudget == confirmedNormalLaunchMutationBudget() && !nodePresent && nodeValid
		nodeOnlyContinuation := requestedNodeContinuation || resumeObservedNodeClaim || reserveHistoricalNodeClaim || activeNodeContinuation || reconciledNodeContinuation || currentNodeContinuation
		resumeReservedNodeReadback := mutationPresent && validNodeReservedComputeClaimRecoveryMutation(mutationLedger) &&
			proof.CVMOwnershipState == "target_owned" && proof.NodeOwnershipState == "target_owned"
		reconciledNodeReadback := requestHashReconciliation && reconciliationPresent &&
			(reconciliation.State == "verified" || reconciliation.State == "node_reserved" || reconciliation.State == "observed") &&
			reconciledCVMOwnership && proof.NodeOwnershipState == "target_owned"
		reservedNodeOutcomeUnknown := mutationPresent && validNodeReservedComputeClaimRecoveryMutation(mutationLedger) &&
			proof.CVMOwnershipState == "target_owned" && proof.NodeOwnershipState == "unallocated"
		if reservedNodeOutcomeUnknown {
			applyComputeClaimRecoveryMutation(&result, mutationLedger)
			return fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, result.Reason)
		}
		if mutationPresent && mutationLedger.State == "node_reserved" && !resumeReservedNodeReadback {
			result.Eligible, result.Reason = false, "identity_mismatch"
			return ErrComputeClaimRecoveryIdempotencyConflict
		}
		if requestHashReconciliation && reconciliationPresent && !legacyClientRejectedNodeCall &&
			(reconciliation.State == "node_reserved" || reconciliation.State == "observed") && !reconciledNodeReadback {
			result.Eligible, result.Reason = false, "provider_describe"
			result.FailureStage, result.ProviderErrorClass = reconciliation.FailureStage, reconciliation.ProviderErrorClass
			result.Evidence = &ComputeClaimEvidence{CVM: cloneComputeClaimMutationEvidence(mutationLedger.Evidence.CVM), Node: cloneComputeClaimMutationEvidence(reconciliation.Node)}
			return fmt.Errorf("%w: provider_describe", ErrComputeClaimRecoveryUnavailable)
		}
		if ownership.Status == "active" && !activeNodeContinuation && !completedNodeOnlyReadback {
			if proof.CVMOwnershipState != "target_owned" && !reconciledCVMOwnership || proof.NodeOwnershipState != "target_owned" {
				result.Eligible, result.Reason = false, "identity_mismatch"
				return fmt.Errorf("%w: identity_mismatch", ErrComputeClaimRecoveryUnavailable)
			}
		} else if !completedNodeOnlyReadback && !reconciledNodeReadback &&
			(activeNodeContinuation || proof.CVMOwnershipState != "target_owned" || proof.NodeOwnershipState != "target_owned") {
			provider, providerOK := s.provider.(computeClaimRecoveryClaimProvider)
			nodeOnlyProvider, nodeOnlyProviderOK := s.provider.(computeClaimRecoveryNodeOnlyProvider)
			if nodeOnlyContinuation && !nodeOnlyProviderOK || !nodeOnlyContinuation && !providerOK {
				result.Eligible, result.Reason = false, "provider_describe"
				return fmt.Errorf("%w: provider_describe", ErrComputeClaimRecoveryUnavailable)
			}
			if mutationPresent && !resumeObservedNodeClaim && !reconciledNodeContinuation {
				applyComputeClaimRecoveryReplayFailure(&result, mutationLedger, proof.Reason)
				return fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, result.Reason)
			}
			if nodeOnlyContinuation {
				if reconciledNodeContinuation {
					var rejectedCall computeClaimNodeClientRejectionRecovery
					if legacyClientRejectedNodeCall {
						rejectedCall = newComputeClaimNodeClientRejectionRecovery(reconciliation)
						clientRejection, clientRejectionPresent = rejectedCall, true
					}
					reconciliation.State, reconciliation.FailureStage, reconciliation.ProviderErrorClass = "node_reserved", "node_patch_readback", "transport_error"
					reconciliation.Node = ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"node_ownership"}}
					reserved := operation
					reserved.RedactedProviderPayload = withComputeClaimRecoveryReconciliation(reserved.RedactedProviderPayload, reconciliation)
					if legacyClientRejectedNodeCall {
						reserved.RedactedProviderPayload = withComputeClaimNodeClientRejectionRecovery(reserved.RedactedProviderPayload, rejectedCall)
					}
					if err := s.operations.SaveComputeClaimRecovery(lockCtx, operation, reserved); err != nil {
						result.Eligible, result.Reason = false, "local_identity"
						return err
					}
					operation = reserved
				} else if reserveHistoricalNodeClaim || activeNodeContinuation || currentNodeContinuation {
					mutationLedger = legacyNodeReservedComputeClaimRecoveryMutation()
				} else {
					mutationLedger = nodeReservedComputeClaimRecoveryMutation(mutationLedger)
				}
				if !reconciledNodeContinuation {
					reserved := operation
					reserved.RedactedProviderPayload = withComputeClaimRecoveryMutation(reserved.RedactedProviderPayload, mutationLedger)
					if err := s.operations.SaveComputeClaimRecovery(lockCtx, operation, reserved); err != nil {
						result.Eligible, result.Reason = false, "local_identity"
						return err
					}
					operation = reserved
				}
			}
			if !nodeOnlyContinuation {
				mutationLedger = reservedComputeClaimRecoveryMutation()
				reserved := operation
				reserved.RedactedProviderPayload = withComputeClaimRecoveryMutation(operation.RedactedProviderPayload, mutationLedger)
				if err := s.operations.SaveComputeClaimRecovery(lockCtx, operation, reserved); err != nil {
					result.Eligible, result.Reason = false, "local_identity"
					return err
				}
				operation = reserved
			}
			if !reconciledNodeContinuation {
				mutationPresent = true
			}
			var claimed ComputeClaimProviderClaim
			var claimErr error
			if nodeOnlyContinuation {
				claimed, claimErr = nodeOnlyProvider.ClaimComputeRecoveryNodeOnly(lockCtx, allocation, plan, ownership)
			} else {
				claimed, claimErr = provider.ClaimComputeRecovery(lockCtx, allocation, plan, ownership)
			}
			result.TencentMutationCount = max(0, claimed.TencentMutationCount)
			result.KubernetesMutationCount = max(0, claimed.KubernetesMutationCount)
			result.FailureStage = claimed.FailureStage
			result.ProviderErrorClass = claimed.ProviderErrorClass
			if claimed.Evidence != nil {
				result.Evidence = &ComputeClaimEvidence{
					CVM:  cloneComputeClaimMutationEvidence(claimed.Evidence.CVM),
					Node: cloneComputeClaimMutationEvidence(claimed.Evidence.Node),
				}
			}
			claimedCVMOwnership := claimed.Proof.CVMOwnershipState == "target_owned" ||
				nodeOnlyContinuation && claimed.Proof.CVMOwnershipState == "recoverable"
			claimSucceeded := claimErr == nil && validComputeClaimProviderProof(claimed.Proof, allocation, plan) &&
				claimedCVMOwnership && claimed.Proof.NodeOwnershipState == "target_owned" && validComputeClaimEvidence(claimed)
			if nodeOnlyContinuation {
				claimSucceeded = claimSucceeded && claimed.TencentMutationCount == 0 &&
					reflect.DeepEqual(claimed.Evidence.CVM, ComputeClaimMutationEvidence{})
			}
			if !claimSucceeded {
				result.Eligible = false
				result.Reason = safeComputeClaimRecoveryReason(claimed.Proof.Reason, "identity_mismatch")
				if claimErr != nil && claimed.Proof.Reason == "" {
					result.Reason = "provider_describe"
				}
			} else {
				result.MachineName, result.NodeName, result.CVMInstanceID = claimed.Proof.MachineName, claimed.Proof.NodeName, claimed.Proof.CVMInstanceID
				result.PrivateIP, result.InstanceType, result.Zone = claimed.Proof.PrivateIP, claimed.Proof.InstanceType, claimed.Proof.Zone
				result.ChargeType, result.PeriodMonths, result.RenewFlag, result.Deadline = claimed.Proof.ChargeType, claimed.Proof.PeriodMonths, claimed.Proof.RenewFlag, claimed.Proof.Deadline
				result.NodeOwnershipState, result.CVMOwnershipState = claimed.Proof.NodeOwnershipState, claimed.Proof.CVMOwnershipState
				result.Eligible, result.Reason = true, "none"
			}
			if reconciledNodeContinuation {
				reconciliation.State = "observed"
				reconciliation.FailureStage, reconciliation.ProviderErrorClass = result.FailureStage, result.ProviderErrorClass
				if result.Evidence != nil && validComputeClaimMutationEvidenceShape(result.Evidence.Node, result.KubernetesMutationCount, 1, "node") {
					reconciliation.Node = cloneComputeClaimMutationEvidence(result.Evidence.Node)
				}
				if claimSucceeded {
					reconciliation.State, reconciliation.FailureStage, reconciliation.ProviderErrorClass = "succeeded", "", ""
				}
			} else if nodeOnlyContinuation {
				mutationLedger = observedNodeClaimRecoveryMutation(mutationLedger, result)
			} else {
				mutationLedger = observedComputeClaimRecoveryMutation(result)
			}
			observed := operation
			if reconciledNodeContinuation {
				observed.RedactedProviderPayload = withComputeClaimRecoveryReconciliation(operation.RedactedProviderPayload, reconciliation)
			} else {
				observed.RedactedProviderPayload = withComputeClaimRecoveryMutation(operation.RedactedProviderPayload, mutationLedger)
			}
			if err := s.operations.SaveComputeClaimRecovery(lockCtx, operation, observed); err != nil {
				result.Eligible, result.Reason = false, "local_identity"
				return err
			}
			operation = observed
			if !claimSucceeded {
				if !reconciledNodeContinuation {
					applyComputeClaimRecoveryMutation(&result, mutationLedger)
				}
				return fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, result.Reason)
			}
		}
		if mutationPresent && mutationLedger.State == "node_reserved" && proof.NodeOwnershipState == "target_owned" {
			mutationLedger = observedNodeClaimReadbackMutation(mutationLedger)
		}
		if requestHashReconciliation && reconciliationPresent && reconciliation.State != "succeeded" && proof.NodeOwnershipState == "target_owned" {
			reconciliation.State, reconciliation.FailureStage, reconciliation.ProviderErrorClass = "succeeded", "", ""
			if reconciliation.Node.Attempted == 1 {
				reconciliation.Node = ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}
			} else {
				reconciliation.Node = ComputeClaimMutationEvidence{}
			}
		}
		if requestHashReconciliation && reconciliationPresent && reconciliation.State == "succeeded" && proof.NodeOwnershipState == "target_owned" {
			result.NodeOwnershipState, result.CVMOwnershipState, result.Eligible, result.Reason = "target_owned", "target_owned", true, "none"
		}
		allocation.Status = "ready"
		allocation.CostTags = oplCostTags(allocation.AccountID, allocation.WorkspaceID, allocation.ID, ownership.ID)
		allocation.NodeSelector = tkeNodeSelector(allocation.ProviderData, allocation.NodeName)
		ownership.Status, ownership.ReleasedAt = "active", nil
		if err := s.operations.ActivateComputeClaimRecoveryOwnership(lockCtx, ownership); err != nil {
			result.Eligible, result.Reason = false, "local_identity"
			return err
		}
		if operation.Status != "succeeded" {
			recovered := operation
			recovered.Status, recovered.ErrorCode, recovered.FinishedAt = "succeeded", "", s.now()
			finalBinding := binding
			if historicalBinding || requestHashReconciliation {
				finalBinding = persistedBinding
			}
			recovered.RedactedProviderPayload = preserveNormalLaunchMutationBudget(computeAllocationOperationPayload(allocation, plan), operation.RedactedProviderPayload)
			recovered.RedactedProviderPayload = withComputeClaimRecoveryBinding(recovered.RedactedProviderPayload, finalBinding)
			if terminal, present, valid := decodeComputeClaimTerminalEvidence(operation); present && valid {
				recovered.RedactedProviderPayload = withComputeClaimTerminalEvidence(recovered.RedactedProviderPayload, terminal)
			}
			if mutationPresent {
				recovered.RedactedProviderPayload = withComputeClaimRecoveryMutation(recovered.RedactedProviderPayload, mutationLedger)
			}
			if requestHashReconciliation && reconciliationPresent {
				recovered.RedactedProviderPayload = withComputeClaimRecoveryReconciliation(recovered.RedactedProviderPayload, reconciliation)
			}
			if clientRejectionPresent {
				recovered.RedactedProviderPayload = withComputeClaimNodeClientRejectionRecovery(recovered.RedactedProviderPayload, clientRejection)
			}
			if err := s.operations.SaveComputeClaimRecovery(lockCtx, operation, recovered); err != nil {
				result.Eligible, result.Reason = false, "local_identity"
				return err
			}
		}
		s.mu.Lock()
		s.computes[allocation.ID] = allocation
		s.mu.Unlock()
		return nil
	})
	evidenceInput := input
	if driftAttempt {
		evidenceInput.IdempotencyKey = input.LaunchOperationID + ":compute"
	}
	if evidence, evidenceErr := s.ComputeClaimRecoveryIdentityEvidence(ctx, evidenceInput); evidenceErr == nil {
		result.IdentityEvidence = evidence
	}
	return result, err
}

func (s *Service) claimApprovedConfirmedNodeDrift(
	ctx context.Context,
	input ComputeClaimRecoveryClaimInput,
	operation FabricOperation,
	allocation ComputeAllocation,
	plan ComputeAllocationPreparation,
	ownership MachineOwnership,
	attemptDigest string,
) (ComputeClaimRecoveryProof, error) {
	result := newComputeClaimRecoveryProof(input.ComputeClaimRecoveryInput)
	canonicalInput := input
	canonicalInput.IdempotencyKey = input.LaunchOperationID + ":compute"
	persistedBinding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(operation)
	ledger, ledgerPresent, ledgerValid := decodeComputeClaimRecoveryMutation(operation)
	if !bindingPresent || !bindingValid || !validConfirmedNodeDriftAuthority(operation, ownership, canonicalInput, persistedBinding) ||
		ledgerPresent && (!ledgerValid || ledger.Generation != confirmedNodeDriftGeneration || ledger.AttemptDigest != attemptDigest) {
		result.Reason = "identity_mismatch"
		return result, ErrComputeClaimRecoveryIdempotencyConflict
	}

	proof, proofErr := s.ComputeClaimRecoveryProof(ctx, input.ComputeClaimRecoveryInput)
	result = proof
	if proofErr != nil {
		if ledgerPresent {
			applyComputeClaimRecoveryMutation(&result, ledger)
		}
		return result, proofErr
	}
	if input.MachineName != proof.MachineName || input.NodeName != proof.NodeName || input.CVMInstanceID != proof.CVMInstanceID ||
		input.PrivateIP != proof.PrivateIP || input.InstanceType != proof.InstanceType || input.Zone != proof.Zone || input.PoolID != proof.PoolID ||
		(proof.CVMOwnershipState != "recoverable" && proof.CVMOwnershipState != "target_owned") {
		result.Eligible, result.Reason = false, "identity_mismatch"
		return result, fmt.Errorf("%w: identity_mismatch", ErrComputeClaimRecoveryUnavailable)
	}
	if proof.NodeOwnershipState == "target_owned" {
		if ledgerPresent && ledger.State == "node_reserved" {
			observedLedger := ledger
			observedLedger.State, observedLedger.Reason = "observed", "confirmed_node_drift"
			observedLedger.FailureStage, observedLedger.ProviderErrorClass = "", ""
			observedLedger.Evidence.Node = ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}
			observed := operation
			observed.RedactedProviderPayload = withComputeClaimRecoveryMutation(observed.RedactedProviderPayload, observedLedger)
			if err := s.operations.SaveComputeClaimRecovery(ctx, operation, observed); err != nil {
				result.Eligible, result.Reason = false, "local_identity"
				return result, err
			}
		}
		result.Eligible, result.Reason = true, "none"
		result.TencentMutationCount, result.KubernetesMutationCount, result.Evidence = 0, 0, nil
		return result, nil
	}
	if proof.NodeOwnershipState != "unallocated" {
		result.Eligible, result.Reason = false, "identity_mismatch"
		return result, fmt.Errorf("%w: identity_mismatch", ErrComputeClaimRecoveryUnavailable)
	}
	if ledgerPresent {
		applyComputeClaimRecoveryMutation(&result, ledger)
		if ledger.State == "observed" {
			result.Reason, result.FailureStage, result.ProviderErrorClass = "identity_mismatch", "claim_final_readback", "readback_mismatch"
		}
		return result, fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, result.Reason)
	}
	if proof.RecoveryClassification != "confirmed_node_drift" {
		result.Eligible, result.Reason = false, "identity_mismatch"
		return result, ErrComputeClaimRecoveryIdempotencyConflict
	}
	nodeOnlyProvider, ok := s.provider.(computeClaimRecoveryNodeOnlyProvider)
	if !ok {
		result.Eligible, result.Reason = false, "provider_describe"
		return result, fmt.Errorf("%w: provider_describe", ErrComputeClaimRecoveryUnavailable)
	}

	ledger = reservedConfirmedNodeDriftMutation(attemptDigest)
	reserved := operation
	reserved.RedactedProviderPayload = withComputeClaimRecoveryMutation(reserved.RedactedProviderPayload, ledger)
	if err := s.operations.SaveComputeClaimRecovery(ctx, operation, reserved); err != nil {
		result.Eligible, result.Reason = false, "local_identity"
		return result, err
	}
	operation = reserved

	claimed, claimErr := nodeOnlyProvider.ClaimComputeRecoveryNodeOnly(ctx, allocation, plan, ownership)
	result.TencentMutationCount = max(0, claimed.TencentMutationCount)
	result.KubernetesMutationCount = max(0, claimed.KubernetesMutationCount)
	result.FailureStage, result.ProviderErrorClass = claimed.FailureStage, claimed.ProviderErrorClass
	if claimed.Evidence != nil {
		result.Evidence = &ComputeClaimEvidence{
			CVM: cloneComputeClaimMutationEvidence(claimed.Evidence.CVM), Node: cloneComputeClaimMutationEvidence(claimed.Evidence.Node),
		}
	}
	claimSucceeded := claimErr == nil && validComputeClaimProviderProof(claimed.Proof, allocation, plan) &&
		(claimed.Proof.CVMOwnershipState == "recoverable" || claimed.Proof.CVMOwnershipState == "target_owned") &&
		claimed.Proof.NodeOwnershipState == "target_owned" && claimed.TencentMutationCount == 0 && claimed.KubernetesMutationCount == 1 &&
		claimed.Evidence != nil && reflect.DeepEqual(claimed.Evidence.CVM, ComputeClaimMutationEvidence{}) &&
		reflect.DeepEqual(claimed.Evidence.Node, ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}) && validComputeClaimEvidence(claimed)
	if claimSucceeded {
		applyComputeClaimRecoveryProviderProof(&result, claimed.Proof)
		result.Eligible, result.Reason = true, "none"
	} else {
		result.Eligible = false
		result.Reason = safeComputeClaimRecoveryReason(claimed.Proof.Reason, "identity_mismatch")
		if claimErr != nil && claimed.Proof.Reason == "" {
			result.Reason = "provider_describe"
		}
	}
	recordableOutcome := result.Evidence != nil && result.TencentMutationCount == 0 && result.KubernetesMutationCount >= 0 && result.KubernetesMutationCount <= 1 &&
		reflect.DeepEqual(result.Evidence.CVM, ComputeClaimMutationEvidence{}) &&
		validComputeClaimMutationEvidenceShape(result.Evidence.Node, result.KubernetesMutationCount, 1, "node")
	if recordableOutcome {
		observedLedger := observedConfirmedNodeDriftMutation(ledger, result)
		observed := operation
		observed.RedactedProviderPayload = withComputeClaimRecoveryMutation(observed.RedactedProviderPayload, observedLedger)
		if err := s.operations.SaveComputeClaimRecovery(ctx, operation, observed); err != nil {
			result.Eligible, result.Reason = false, "local_identity"
			return result, err
		}
	}
	if !claimSucceeded {
		return result, fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, result.Reason)
	}
	return result, nil
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

func validConfirmedNodeDriftAuthority(
	operation FabricOperation,
	ownership MachineOwnership,
	input ComputeClaimRecoveryClaimInput,
	persistedBinding computeClaimRecoveryBinding,
) bool {
	if operation.Status != "succeeded" || ownership.Status != "active" {
		return false
	}
	reconciliation, present, valid := decodeComputeClaimRecoveryReconciliation(operation)
	if !present || !valid || reconciliation.SchemaVersion != 2 || reconciliation.Consumer != "claim_compute_recovery" ||
		reconciliation.Generation != "normal_launch_terminal_evidence_v1" || reconciliation.State != "succeeded" ||
		!reflect.DeepEqual(reconciliation.Node, ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}) {
		return false
	}
	withoutAttempt := operation
	withoutAttempt.RedactedProviderPayload = maps.Clone(operation.RedactedProviderPayload)
	delete(withoutAttempt.RedactedProviderPayload, computeClaimRecoveryMutationPayloadKey)
	return computeClaimRecoveryReconciliationMatches(
		reconciliation, withoutAttempt, input, persistedBinding, computeClaimRecoveryMutationLedger{},
	)
}
