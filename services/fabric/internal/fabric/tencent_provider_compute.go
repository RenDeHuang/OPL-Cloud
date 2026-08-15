package fabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strconv"
	"strings"

	"opl-cloud/services/fabric/internal/protectedresource"
)

func (p *TencentProvider) PrepareComputeAllocation(ctx context.Context, input ComputeAllocationInput) (ComputeAllocationPreparation, error) {
	packagePlan, err := configuredPackagePlan(input.PackageID)
	if err != nil {
		return ComputeAllocationPreparation{}, err
	}
	poolConfig, err := configuredPackageNodePool(input.PackageID)
	prepared := ComputeAllocationPreparation{PoolID: packagePlan.ID, PackageID: input.PackageID, NodePoolID: poolConfig.NodePoolID, InstanceType: packagePlan.InstanceType, MaxReplicas: poolConfig.MaxReplicas}
	if err != nil {
		return prepared, err
	}
	if strings.TrimSpace(input.NodePoolID) != prepared.NodePoolID {
		return prepared, protectedresource.ErrPackagePoolMismatch
	}
	response, err := p.provision(ctx, provisionerRequest{
		Action: "prepare_compute_allocation", DryRun: input.DryRun, PackageID: input.PackageID,
		Pool: provisionerPool{
			ID: packagePlan.ID, PackageID: input.PackageID, InstanceType: packagePlan.InstanceType,
			CPU: uint64(packagePlan.CPU), MemoryGB: uint64(packagePlan.MemoryGB), NodePoolID: prepared.NodePoolID, MaxReplicas: prepared.MaxReplicas,
		},
		Allocation: provisionerAllocation{ID: input.ID},
	})
	if err != nil {
		return prepared, err
	}
	if !response.OK {
		return prepared, provisionerError(response)
	}
	prepared.BaselineReplicas = response.CurrentReplicas
	prepared.TargetReplicas = response.TargetReplicas
	prepared.ProviderRequestID = response.ProviderRequestID
	prepared.BeforeMachineNames = make([]string, 0, len(response.Machines))
	for _, machine := range response.Machines {
		prepared.BeforeMachineNames = append(prepared.BeforeMachineNames, machine.MachineID)
	}
	return prepared, nil
}

type tencentComputeMutationState struct {
	Allocation ComputeAllocation            `json:"allocation"`
	Plan       ComputeAllocationPreparation `json:"plan"`
}

func (p *TencentProvider) CreateComputeAllocation(ctx context.Context, input ComputeAllocationExecution) (ComputeAllocation, error) {
	allocation, prepared := input.Allocation, input.Plan
	packagePlan := packagePlan(prepared.PackageID)
	var mutation *providerMutationAttempt
	var err error
	if !input.DryRun {
		mutation, err = beginProviderMutationWithState(ctx, "tencent_compute_allocation_create", "compute_allocation", allocation.ID, prepared.NodePoolID, tencentComputeMutationState{Allocation: allocation, Plan: prepared})
		if err != nil {
			return allocation, err
		}
		if mutation != nil && !mutation.Fresh {
			var persisted tencentComputeMutationState
			if !mutation.state(&persisted) || persisted.Allocation.ID != allocation.ID || !reflect.DeepEqual(persisted.Plan, prepared) {
				return allocation, ErrLaunchStageBindingConflict
			}
			allocation = persisted.Allocation
			_ = mutation.resource(&allocation)
			readback, readErr := p.DiscoverComputeAllocation(ctx, allocation, prepared)
			if readErr != nil {
				_ = mutation.complete(ctx, readback.ProviderRequestID, readback, readErr)
				return readback, readErr
			}
			if completeErr := mutation.complete(ctx, readback.ProviderRequestID, readback, nil); completeErr != nil {
				return readback, completeErr
			}
			return readback, nil
		}
	}
	response, err := p.provision(ctx, provisionerRequest{
		Action: "create_compute_allocation", DryRun: input.DryRun, AccountID: allocation.AccountID, PackageID: allocation.PackageID,
		Tags: oplCostTags(allocation.AccountID, allocation.WorkspaceID, allocation.ID, allocation.ProviderRequestID),
		Pool: provisionerPool{
			ID: prepared.PoolID, PackageID: prepared.PackageID, InstanceType: prepared.InstanceType, NodePoolID: prepared.NodePoolID,
			CPU: uint64(packagePlan.CPU), MemoryGB: uint64(packagePlan.MemoryGB),
			MaxReplicas: prepared.MaxReplicas, BaselineReplicas: prepared.BaselineReplicas, TargetReplicas: prepared.TargetReplicas, BeforeMachineNames: append([]string(nil), prepared.BeforeMachineNames...),
		},
		Allocation: provisionerAllocation{ID: allocation.ID},
	})
	if err != nil {
		_ = mutation.complete(ctx, allocation.ProviderRequestID, allocation, err)
		return allocation, err
	}
	allocation.ProviderRequestID = firstNonEmpty(response.ProviderRequestID, allocation.ProviderRequestID)
	allocation.PoolID = prepared.PoolID
	allocation.NodePoolID = prepared.NodePoolID
	allocation.InstanceType = prepared.InstanceType
	allocation.Status = firstNonEmpty(response.Status, allocation.Status)
	allocation.InstanceID = response.InstanceID
	allocation.CVMInstanceID = response.InstanceID
	allocation.MachineName = response.ProviderData["machineName"]
	allocation.NodeName = response.NodeName
	allocation.PrivateIP = response.PrivateIP
	allocation.PublicIP = response.PublicIP
	allocation.Zone = response.ProviderData["zone"]
	allocation.ChargeType = response.ProviderData["chargeType"]
	allocation.RenewFlag = response.ProviderData["renewFlag"]
	allocation.Deadline = response.ProviderData["deadline"]
	allocation.ProviderData = maps.Clone(response.ProviderData)
	allocation.ProviderResourceID = firstNonEmpty(response.InstanceID, allocation.ProviderResourceID)
	if !response.OK {
		if response.Retryable {
			_ = mutation.complete(ctx, allocation.ProviderRequestID, allocation, ErrComputeAllocationPending)
			return allocation, ErrComputeAllocationPending
		}
		err := provisionerError(response)
		_ = mutation.complete(ctx, allocation.ProviderRequestID, allocation, err)
		return allocation, err
	}
	if err := mutation.complete(ctx, allocation.ProviderRequestID, allocation, nil); err != nil {
		return allocation, err
	}
	return allocation, nil
}

func (p *TencentProvider) DiscoverComputeAllocation(ctx context.Context, allocation ComputeAllocation, prepared ComputeAllocationPreparation) (ComputeAllocation, error) {
	packagePlan := packagePlan(prepared.PackageID)
	response, err := p.provision(ctx, provisionerRequest{
		Action: "read_compute_allocation", AccountID: allocation.AccountID, PackageID: allocation.PackageID,
		Pool: provisionerPool{
			ID: prepared.PoolID, PackageID: prepared.PackageID, InstanceType: prepared.InstanceType, NodePoolID: prepared.NodePoolID,
			CPU: uint64(packagePlan.CPU), MemoryGB: uint64(packagePlan.MemoryGB), MaxReplicas: prepared.MaxReplicas,
			BaselineReplicas: prepared.BaselineReplicas, TargetReplicas: prepared.TargetReplicas,
			BeforeMachineNames: append([]string(nil), prepared.BeforeMachineNames...),
		},
		Allocation: provisionerAllocation{ID: allocation.ID},
	})
	if err != nil {
		return allocation, err
	}
	allocation.ProviderRequestID = firstNonEmpty(response.ProviderRequestID, allocation.ProviderRequestID)
	allocation.PoolID = prepared.PoolID
	allocation.NodePoolID = prepared.NodePoolID
	allocation.InstanceType = prepared.InstanceType
	allocation.Status = firstNonEmpty(response.Status, allocation.Status)
	allocation.InstanceID = response.InstanceID
	allocation.CVMInstanceID = response.InstanceID
	allocation.MachineName = response.ProviderData["machineName"]
	allocation.NodeName = response.NodeName
	allocation.PrivateIP = response.PrivateIP
	allocation.PublicIP = response.PublicIP
	allocation.Zone = response.ProviderData["zone"]
	allocation.ChargeType = response.ProviderData["chargeType"]
	allocation.RenewFlag = response.ProviderData["renewFlag"]
	allocation.Deadline = response.ProviderData["deadline"]
	allocation.ProviderData = maps.Clone(response.ProviderData)
	allocation.ProviderResourceID = firstNonEmpty(response.InstanceID, allocation.ProviderResourceID)
	if !response.OK {
		if response.Retryable {
			return allocation, ErrComputeAllocationPending
		}
		return allocation, provisionerError(response)
	}
	return allocation, nil
}

func (p *TencentProvider) ProveComputeClaimRecovery(ctx context.Context, allocation ComputeAllocation, prepared ComputeAllocationPreparation, ownership MachineOwnership) (ComputeClaimProviderProof, error) {
	proof := ComputeClaimProviderProof{Reason: "identity_mismatch"}
	plan := packagePlan(allocation.PackageID)
	instanceID := firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID)
	if allocation.ID == "" || allocation.AccountID == "" || allocation.WorkspaceID == "" || (allocation.PackageID != "basic" && allocation.PackageID != "pro") ||
		allocation.PoolID != prepared.PoolID || allocation.NodePoolID != prepared.NodePoolID || prepared.PackageID != allocation.PackageID ||
		prepared.InstanceType != plan.InstanceType || allocation.InstanceType != prepared.InstanceType || prepared.MaxReplicas <= 0 || prepared.BaselineReplicas < 0 ||
		prepared.TargetReplicas != prepared.BaselineReplicas+1 || int64(len(prepared.BeforeMachineNames)) != prepared.BaselineReplicas ||
		allocation.MachineName == "" || !strings.HasPrefix(instanceID, "ins-") || allocation.NodeName == "" || allocation.PrivateIP == "" || allocation.Zone == "" ||
		ownership.ResourceID != allocation.ID || ownership.AccountID != allocation.AccountID || ownership.WorkspaceID != allocation.WorkspaceID ||
		ownership.PackageID != allocation.PackageID || ownership.NodePoolID != allocation.NodePoolID || ownership.MachineID != allocation.MachineName ||
		ownership.InstanceID != instanceID || ownership.NodeName != allocation.NodeName || ownership.ID == "" {
		return proof, computeClaimProviderError(proof.Reason)
	}
	if err := protectedresource.FromEnv().Check(protectedresource.Target{
		PackageID: ownership.PackageID, NodePoolID: ownership.NodePoolID, MachineID: ownership.MachineID,
		NodeName: ownership.NodeName, CVMID: ownership.InstanceID,
	}); err != nil {
		return proof, computeClaimProviderError(proof.Reason)
	}
	response, err := p.provision(ctx, provisionerRequest{
		Action: "compute_claim_truth", AccountID: allocation.AccountID, PackageID: allocation.PackageID, Zone: allocation.Zone,
		Tags: oplCostTags(allocation.AccountID, allocation.WorkspaceID, allocation.ID, ownership.ID),
		Pool: provisionerPool{
			ID: prepared.PoolID, PackageID: prepared.PackageID, InstanceType: prepared.InstanceType, CPU: uint64(plan.CPU), MemoryGB: uint64(plan.MemoryGB),
			NodePoolID: prepared.NodePoolID, MaxReplicas: prepared.MaxReplicas, BaselineReplicas: prepared.BaselineReplicas,
			TargetReplicas: prepared.TargetReplicas, BeforeMachineNames: append([]string(nil), prepared.BeforeMachineNames...),
		},
		Allocation: provisionerAllocation{
			ID: allocation.ID, InstanceID: instanceID, MachineName: allocation.MachineName, NodeName: allocation.NodeName,
			PrivateIP: allocation.PrivateIP, PublicIP: allocation.PublicIP, Deadline: allocation.Deadline,
		},
	})
	if err != nil {
		proof.Reason = "provider_describe"
		return proof, computeClaimProviderError(proof.Reason)
	}
	if !response.OK {
		proof.Reason = safeComputeClaimRecoveryReason(response.ErrorCode, "provider_describe")
		proof.FailureStage = response.FailureStage
		proof.ProviderErrorClass = response.ProviderErrorClass
		proof.ProviderIdentityFailure = cloneComputeClaimProviderIdentityFailure(response.ProviderIdentityFailure)
		return proof, computeClaimProviderError(proof.Reason)
	}
	periodMonths, periodErr := strconv.Atoi(response.ProviderData["periodMonths"])
	proof = ComputeClaimProviderProof{
		Status: response.Status, MachineName: response.ProviderData["machineName"], NodeName: response.NodeName,
		CVMInstanceID: response.InstanceID, PrivateIP: response.PrivateIP, InstanceType: response.InstanceType,
		Zone: response.ProviderData["zone"], ChargeType: response.ProviderData["chargeType"], PeriodMonths: periodMonths,
		RenewFlag: response.ProviderData["renewFlag"], Deadline: response.ProviderData["deadline"],
		CVMOwnershipState: response.ProviderData["cvmOwnershipState"],
	}
	if periodErr != nil || proof.Status != "proven" || response.PoolID != prepared.PoolID || response.NodePoolID != prepared.NodePoolID ||
		proof.MachineName != allocation.MachineName || proof.NodeName != allocation.NodeName ||
		proof.CVMInstanceID != instanceID || proof.PrivateIP != allocation.PrivateIP || proof.InstanceType != prepared.InstanceType || proof.Zone != allocation.Zone ||
		proof.ChargeType != "PREPAID" || proof.PeriodMonths != 1 || proof.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || proof.Deadline != allocation.Deadline ||
		(proof.CVMOwnershipState != "recoverable" && proof.CVMOwnershipState != "target_owned") {
		proof.Reason = "identity_mismatch"
		proof.FailureStage, proof.ProviderErrorClass = "cvm_pre_read", "readback_mismatch"
		proof.ProviderIdentityFailure = newComputeClaimProviderIdentityFailure("compute_claim.provider_response_identity", map[string]any{
			"status": "proven", "machineName": allocation.MachineName, "nodeName": allocation.NodeName, "cvmInstanceId": instanceID,
			"privateIp": allocation.PrivateIP, "instanceType": prepared.InstanceType, "zone": allocation.Zone, "chargeType": "PREPAID",
			"periodMonths": 1, "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": allocation.Deadline,
		}, proof)
		return proof, computeClaimProviderError(proof.Reason)
	}
	nodeRaw, err := p.callKubectl(ctx, []string{"get", "node/" + allocation.NodeName, "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		proof.Reason = "provider_describe"
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "forbidden") || strings.Contains(message, "unauthorized") || strings.Contains(message, "permission") {
			proof.Reason = "iam_rbac"
		}
		return proof, computeClaimProviderError(proof.Reason)
	}
	nodeState, ok := computeClaimNodeOwnershipState(nodeRaw, allocation, ownership)
	if !ok {
		proof.Reason = "node_ownership_conflict"
		if nodeState == "identity_mismatch" {
			proof.Reason = "identity_mismatch"
			proof.FailureStage, proof.ProviderErrorClass = "node_pre_read", "readback_mismatch"
			proof.ProviderIdentityFailure = newComputeClaimProviderIdentityFailure("compute_claim.kubernetes_node_identity", map[string]any{
				"nodeName": allocation.NodeName, "privateIp": allocation.PrivateIP, "resourceId": allocation.ID,
				"accountId": allocation.AccountID, "workspaceId": allocation.WorkspaceID,
			}, json.RawMessage(nodeRaw))
		}
		return proof, computeClaimProviderError(proof.Reason)
	}
	proof.NodeOwnershipState = nodeState
	proof.Reason = ""
	return proof, nil
}

func newComputeClaimProviderIdentityFailure(predicate string, expected, actual any) *ComputeClaimProviderIdentityFailure {
	digest := func(value any) (string, bool) {
		raw, err := json.Marshal(value)
		if err != nil {
			return "", false
		}
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:]), true
	}
	expectedDigest, expectedOK := digest(expected)
	actualDigest, actualOK := digest(actual)
	if !expectedOK || !actualOK || expectedDigest == actualDigest {
		return nil
	}
	value := &ComputeClaimProviderIdentityFailure{
		Predicate: predicate, ExpectedDigest: expectedDigest, ActualDigest: actualDigest,
	}
	if !validComputeClaimProviderIdentityFailure(value) {
		return nil
	}
	return value
}

func cloneComputeClaimMutationEvidence(value ComputeClaimMutationEvidence) ComputeClaimMutationEvidence {
	value.Missing = append([]string(nil), value.Missing...)
	return value
}

func validConfirmedComputeClaimMutation(evidence *ComputeClaimMutationEvidence, count, maximum int) bool {
	return evidence != nil && count >= 0 && count <= maximum && evidence.Attempted == count && evidence.Attempted == evidence.Confirmed &&
		evidence.Unknown == 0 && len(evidence.Missing) == 0
}

type computeClaimNodeConvergenceError struct {
	Reason        string
	Stage         string
	ProviderClass string
}

func (p *TencentProvider) convergeComputeClaimNode(ctx context.Context, allocation ComputeAllocation, ownership MachineOwnership, target protectedresource.Target) (ComputeClaimMutationEvidence, *computeClaimNodeConvergenceError) {
	evidence := ComputeClaimMutationEvidence{}
	nodeRaw, err := p.callKubectl(ctx, []string{"get", "node/" + allocation.NodeName, "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		reason := computeClaimKubectlReason(err)
		return evidence, &computeClaimNodeConvergenceError{Reason: reason, Stage: "node_pre_read", ProviderClass: computeClaimKubectlErrorClass(err)}
	}
	nodeState, ok := computeClaimNodeOwnershipState(nodeRaw, allocation, ownership)
	if !ok {
		reason := "node_ownership_conflict"
		if nodeState == "identity_mismatch" {
			reason = "identity_mismatch"
		}
		return evidence, &computeClaimNodeConvergenceError{Reason: reason, Stage: "node_conflict_check", ProviderClass: "ownership_conflict"}
	}
	if nodeState == "target_owned" {
		return evidence, nil
	}
	patch, patchErr := computeClaimNodePatch(nodeRaw, allocation, ownership)
	if patchErr != nil {
		return evidence, &computeClaimNodeConvergenceError{Reason: "node_ownership_conflict", Stage: "node_patch_build", ProviderClass: "ownership_conflict"}
	}
	_, patchErr = p.callKubectl(ctx, []string{"patch", "node/" + allocation.NodeName, "--type=json", "--patch-file=/dev/stdin"}, patch, target)
	if !computeClaimKubectlClientRejectedBeforeAPI(patchErr) {
		evidence.Attempted = 1
	}
	readbackState, readbackOK, readbackErr, readbackClass := p.readNodeOwnershipAfterMutation(ctx, allocation, ownership)
	if readbackOK && readbackState == "target_owned" {
		evidence.Confirmed = evidence.Attempted
		return evidence, nil
	}
	evidence.Missing = []string{"node_ownership"}
	if readbackState == "identity_mismatch" {
		return evidence, &computeClaimNodeConvergenceError{Reason: "identity_mismatch", Stage: "node_final_readback", ProviderClass: "ownership_conflict"}
	}
	if readbackState == "node_ownership_conflict" {
		return evidence, &computeClaimNodeConvergenceError{Reason: "node_ownership_conflict", Stage: "node_final_readback", ProviderClass: "ownership_conflict"}
	}
	if readbackErr != nil {
		evidence.Unknown = 1
		reason := computeClaimKubectlReason(readbackErr)
		providerClass := readbackClass
		if patchErr != nil && reason == "provider_describe" {
			reason = computeClaimKubectlReason(patchErr)
			providerClass = computeClaimKubectlErrorClass(patchErr)
		}
		return evidence, &computeClaimNodeConvergenceError{Reason: reason, Stage: "node_patch_readback", ProviderClass: providerClass}
	}
	if patchErr != nil {
		return evidence, &computeClaimNodeConvergenceError{Reason: computeClaimKubectlReason(patchErr), Stage: "node_patch_readback", ProviderClass: computeClaimKubectlErrorClass(patchErr)}
	}
	return evidence, &computeClaimNodeConvergenceError{Reason: "node_ownership_conflict", Stage: "node_final_readback", ProviderClass: "readback_mismatch"}
}

// readNodeOwnershipAfterMutation performs bounded authoritative reads after a
// patch. It never retries the patch itself: a target-owned readback is the only
// success proof, while an explicit ownership conflict stops immediately.
func (p *TencentProvider) readNodeOwnershipAfterMutation(ctx context.Context, allocation ComputeAllocation, ownership MachineOwnership) (string, bool, error, string) {
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 && p.convergenceWait != nil {
			if err := p.convergenceWait(ctx, attempt); err != nil {
				return "unknown", false, err, computeClaimKubectlErrorClass(err)
			}
		}
		raw, err := p.callKubectl(ctx, []string{"get", "node/" + allocation.NodeName, "-o", "json"}, nil, protectedresource.Target{})
		if err != nil {
			lastErr = err
			continue
		}
		state, ok := computeClaimNodeOwnershipState(raw, allocation, ownership)
		if ok && state == "target_owned" {
			return state, true, nil, "readback_mismatch"
		}
		if !ok && (state == "identity_mismatch" || state == "node_ownership_conflict") {
			return state, false, nil, "ownership_conflict"
		}
	}
	if lastErr != nil {
		return "unknown", false, lastErr, computeClaimKubectlErrorClass(lastErr)
	}
	return "unallocated", true, nil, "readback_mismatch"
}

func computeClaimKubectlErrorClass(err error) string {
	if err == nil {
		return "readback_mismatch"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	if computeClaimKubectlReason(err) == "iam_rbac" {
		return "iam_rbac"
	}
	if computeClaimKubectlReason(err) == "node_ownership_conflict" {
		return "ownership_conflict"
	}
	return "provider_error"
}

func computeClaimKubectlClientRejectedBeforeAPI(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "must specify --patch or --patch-file containing the contents of the patch")
}

func computeClaimKubectlReason(err error) string {
	if err == nil {
		return "provider_describe"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "forbidden") || strings.Contains(message, "unauthorized") || strings.Contains(message, "permission") {
		return "iam_rbac"
	}
	if strings.Contains(message, "test failed") || strings.Contains(message, "conflict") || strings.Contains(message, "resourceversion") {
		return "node_ownership_conflict"
	}
	return "provider_describe"
}

func computeClaimNodePatch(raw []byte, allocation ComputeAllocation, ownership MachineOwnership) ([]byte, error) {
	var node struct {
		Metadata struct {
			Name            string            `json:"name"`
			ResourceVersion string            `json:"resourceVersion"`
			Labels          map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			Taints []struct {
				Key, Value, Effect string
			} `json:"taints"`
		} `json:"spec"`
	}
	if json.Unmarshal(raw, &node) != nil || node.Metadata.Name != allocation.NodeName || node.Metadata.ResourceVersion == "" {
		return nil, fmt.Errorf("node_identity_mismatch")
	}
	packageTaints := 0
	for _, taint := range node.Spec.Taints {
		if taint.Key == "oplcloud.cn/workspace-id" {
			return nil, fmt.Errorf("node_ownership_conflict")
		}
		if taint.Key == "oplcloud.cn/package-id" {
			packageTaints++
			if taint.Value != allocation.PackageID || taint.Effect != "NoSchedule" {
				return nil, fmt.Errorf("node_ownership_conflict")
			}
		}
	}
	if packageTaints != 1 {
		return nil, fmt.Errorf("node_ownership_conflict")
	}
	expected := []struct{ key, value string }{
		{key: "medopl.cn/workload", value: "workspace"},
		{key: "oplcloud.cn/resource-id", value: ownership.ResourceID},
		{key: "oplcloud.cn/account-id", value: ownership.AccountID},
		{key: "oplcloud.cn/workspace-id", value: ownership.WorkspaceID},
	}
	for _, label := range expected {
		if _, present := node.Metadata.Labels[label.key]; present {
			return nil, fmt.Errorf("node_ownership_conflict")
		}
	}
	patch := []map[string]any{{"op": "test", "path": "/metadata/resourceVersion", "value": node.Metadata.ResourceVersion}}
	if node.Metadata.Labels == nil {
		patch = append(patch, map[string]any{"op": "add", "path": "/metadata/labels", "value": map[string]string{}})
	}
	for _, label := range expected {
		patch = append(patch, map[string]any{"op": "add", "path": "/metadata/labels/" + strings.ReplaceAll(label.key, "/", "~1"), "value": label.value})
	}
	return json.Marshal(patch)
}

func computeClaimProviderError(reason string) error {
	return fmt.Errorf("compute_claim_recovery_%s", safeComputeClaimRecoveryReason(reason, "provider_describe"))
}

func computeClaimNodeOwnershipState(raw []byte, allocation ComputeAllocation, ownership MachineOwnership) (string, bool) {
	var node struct {
		Metadata struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			Taints []struct {
				Key, Value, Effect string
			} `json:"taints"`
		} `json:"spec"`
		Status struct {
			Addresses []struct {
				Type, Address string
			} `json:"addresses"`
		} `json:"status"`
	}
	if json.Unmarshal(raw, &node) != nil || node.Metadata.Name != allocation.NodeName {
		return "identity_mismatch", false
	}
	internalIPCount := 0
	for _, address := range node.Status.Addresses {
		if address.Type == "InternalIP" && address.Address == allocation.PrivateIP {
			internalIPCount++
		}
	}
	if internalIPCount != 1 {
		return "identity_mismatch", false
	}
	packageTaintCount := 0
	for _, taint := range node.Spec.Taints {
		if taint.Key == "oplcloud.cn/workspace-id" {
			return "node_ownership_conflict", false
		}
		if taint.Key == "oplcloud.cn/package-id" {
			packageTaintCount++
			if taint.Value != allocation.PackageID || taint.Effect != "NoSchedule" {
				return "node_ownership_conflict", false
			}
		}
	}
	if packageTaintCount != 1 || allocation.PackageID != ownership.PackageID {
		return "node_ownership_conflict", false
	}
	expected := map[string]string{
		"medopl.cn/workload": "workspace", "oplcloud.cn/resource-id": ownership.ResourceID,
		"oplcloud.cn/account-id": ownership.AccountID, "oplcloud.cn/workspace-id": ownership.WorkspaceID,
	}
	present := 0
	for key, value := range expected {
		actual, exists := node.Metadata.Labels[key]
		if !exists {
			continue
		}
		present++
		if actual != value {
			return "node_ownership_conflict", false
		}
	}
	if present == len(expected) {
		return "target_owned", true
	}
	if present == 0 {
		return "unallocated", true
	}
	return "node_ownership_conflict", false
}

func (p *TencentProvider) TagComputeMachineCVM(ctx context.Context, machine ProviderMachine, ownership MachineOwnership) error {
	if machine.InstanceID == "" || machine.NodeName == "" {
		return fmt.Errorf("compute_machine_identity_required")
	}
	if err := protectedresource.FromEnv().Check(protectedresource.Target{
		PackageID: ownership.PackageID, NodePoolID: ownership.NodePoolID,
		MachineID: machine.MachineID, NodeName: machine.NodeName, CVMID: machine.InstanceID,
	}); err != nil {
		return err
	}
	response, err := p.provision(ctx, provisionerRequest{
		Action:    "tag_compute_machine",
		PackageID: ownership.PackageID,
		Tags:      oplCostTags(ownership.AccountID, ownership.WorkspaceID, ownership.ResourceID, ownership.ID),
		Pool:      provisionerPool{NodePoolID: ownership.NodePoolID},
		Allocation: provisionerAllocation{
			ID: ownership.ResourceID, InstanceID: machine.InstanceID, MachineName: machine.MachineID, NodeName: machine.NodeName, PrivateIP: machine.PrivateIP,
		},
	})
	if err != nil {
		return err
	}
	if !response.OK || !validConfirmedComputeClaimMutation(response.MutationEvidence, response.MutationCount, 5) {
		return provisionerError(response)
	}
	return nil
}

func (p *TencentProvider) ClaimComputeNode(ctx context.Context, allocation ComputeAllocation, ownership MachineOwnership) error {
	target := protectedresource.Target{PackageID: ownership.PackageID, NodePoolID: ownership.NodePoolID, MachineID: allocation.MachineName, NodeName: allocation.NodeName, CVMID: firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID)}
	_, nodeErr := p.convergeComputeClaimNode(ctx, allocation, ownership, target)
	if nodeErr != nil {
		return fmt.Errorf("compute_machine_node_claim_%s", safeComputeClaimRecoveryReason(nodeErr.Reason, "provider_describe"))
	}
	return nil
}

func (p *TencentProvider) SyncComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	if allocation.ID == "" {
		return ComputeAllocation{}, fmt.Errorf("compute_allocation_id_required")
	}
	plan, err := configuredPackagePlan(firstNonEmpty(allocation.PackageID, "basic"))
	if err != nil {
		return allocation, err
	}
	response, err := p.provision(ctx, provisionerRequest{
		Action:    "sync_compute_allocation",
		AccountID: allocation.AccountID,
		PackageID: allocation.PackageID,
		Zone:      allocation.ProviderData["zone"],
		Tags:      allocation.CostTags,
		Pool: provisionerPool{
			ID: allocation.PoolID, PackageID: allocation.PackageID, NodePoolID: allocation.NodePoolID,
			InstanceType: plan.InstanceType, CPU: uint64(plan.CPU), MemoryGB: uint64(plan.MemoryGB),
		},
		Allocation: provisionerAllocation{
			ID:          allocation.ID,
			InstanceID:  firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID),
			MachineName: firstNonEmpty(allocation.MachineName, allocation.ProviderData["machineName"], allocation.NodeName),
			NodeName:    allocation.NodeName,
			PrivateIP:   allocation.PrivateIP,
			PublicIP:    allocation.PublicIP,
		},
	})
	if err != nil {
		return allocation, err
	}
	allocation.Status = firstNonEmpty(response.Status, allocation.Status)
	allocation.Provider = firstNonEmpty(allocation.Provider, "tencent-tke")
	allocation.ProviderRequestID = firstNonEmpty(response.ProviderRequestID, allocation.ProviderRequestID)
	allocation.NodePoolID = firstNonEmpty(response.NodePoolID, allocation.NodePoolID)
	allocation.InstanceID = firstNonEmpty(response.InstanceID, allocation.InstanceID)
	allocation.CVMInstanceID = firstNonEmpty(response.InstanceID, allocation.CVMInstanceID)
	allocation.NodeName = firstNonEmpty(response.NodeName, allocation.NodeName)
	allocation.PrivateIP = firstNonEmpty(response.PrivateIP, allocation.PrivateIP)
	allocation.PublicIP = firstNonEmpty(response.PublicIP, allocation.PublicIP)
	allocation.CVMStatus = firstNonEmpty(response.CVMStatus, allocation.CVMStatus)
	if allocation.ProviderData == nil {
		allocation.ProviderData = map[string]string{}
	}
	for key, value := range response.ProviderData {
		allocation.ProviderData[key] = value
	}
	allocation.ProviderData["instanceType"] = firstNonEmpty(response.InstanceType, allocation.ProviderData["instanceType"])
	allocation.ChargeType = firstNonEmpty(response.ProviderData["chargeType"], allocation.ChargeType)
	allocation.RenewFlag = firstNonEmpty(response.ProviderData["renewFlag"], allocation.RenewFlag)
	allocation.Deadline = firstNonEmpty(response.ProviderData["deadline"], allocation.Deadline)
	allocation.NodeSelector = tkeNodeSelector(allocation.ProviderData, allocation.NodeName)
	if !response.OK {
		return allocation, provisionerError(response)
	}
	if response.InstanceType != plan.InstanceType || response.ProviderData["instanceType"] != plan.InstanceType {
		return allocation, fmt.Errorf("compute_instance_type_mismatch")
	}
	if response.ProviderData["cpu"] != strconv.Itoa(plan.CPU) || response.ProviderData["memoryGb"] != strconv.Itoa(plan.MemoryGB) {
		return allocation, fmt.Errorf("compute_resource_shape_mismatch")
	}
	return allocation, nil
}

func (p *TencentProvider) ReadComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	return p.SyncComputeAllocation(ctx, allocation)
}

func (p *TencentProvider) ReadComputeProviderFacts(ctx context.Context, allocation ComputeAllocation) (ProviderResourceFacts, error) {
	readback, err := p.ReadComputeAllocation(ctx, allocation)
	if err != nil {
		return ProviderResourceFacts{}, err
	}
	return ProviderResourceFacts{
		PackageOrSpec: firstNonEmpty(readback.InstanceType, readback.ProviderData["instanceType"]),
		ProviderID:    firstNonEmpty(readback.ProviderResourceID, readback.InstanceID, readback.CVMInstanceID),
		Zone:          firstNonEmpty(readback.Zone, readback.ProviderData["zone"]),
		Status:        firstNonEmpty(readback.CVMStatus, readback.Status),
		ExpiresAt:     readback.Deadline,
	}, nil
}

func (p *TencentProvider) RenewComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	if !validComputeRenewalIdentity(allocation) {
		return ComputeAllocation{}, fmt.Errorf("compute_allocation_renew_identity_required")
	}
	expectedInstanceID := firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID)
	expectedInstanceType := allocation.ProviderData["instanceType"]
	expectedZone := allocation.ProviderData["zone"]
	expectedTags := allocation.CostTags
	response, err := p.provision(ctx, provisionerRequest{
		Action: "renew_compute_allocation", AccountID: allocation.AccountID, Zone: allocation.ProviderData["zone"], Tags: allocation.CostTags,
		Pool:       provisionerPool{InstanceType: allocation.ProviderData["instanceType"]},
		Allocation: provisionerAllocation{ID: allocation.ID, InstanceID: expectedInstanceID, PrivateIP: allocation.PrivateIP, Deadline: allocation.Deadline},
	})
	if err != nil {
		return ComputeAllocation{}, err
	}
	allocation.ProviderRequestID = firstNonEmpty(response.ProviderRequestID, allocation.ProviderRequestID)
	allocation.InstanceID = firstNonEmpty(response.InstanceID, allocation.InstanceID)
	allocation.CVMInstanceID = firstNonEmpty(response.InstanceID, allocation.CVMInstanceID)
	allocation.CVMStatus = response.CVMStatus
	if response.Status == "external_deleted" {
		allocation.Status = "external_deleted"
	}
	if allocation.ProviderData == nil {
		allocation.ProviderData = map[string]string{}
	}
	for key, value := range response.ProviderData {
		allocation.ProviderData[key] = value
	}
	allocation.ChargeType = firstNonEmpty(response.ProviderData["chargeType"], allocation.ChargeType)
	allocation.RenewFlag = firstNonEmpty(response.ProviderData["renewFlag"], allocation.RenewFlag)
	allocation.Deadline = firstNonEmpty(response.ProviderData["deadline"], allocation.Deadline)
	if !response.OK {
		return allocation, provisionerError(response)
	}
	if response.InstanceID != expectedInstanceID || response.ProviderData["instanceType"] != expectedInstanceType || response.ProviderData["zone"] != expectedZone {
		return allocation, fmt.Errorf("compute_renewal_readback_mismatch")
	}
	for _, key := range []string{"opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id"} {
		if response.ProviderData[key] != expectedTags[key] {
			return allocation, fmt.Errorf("compute_renewal_readback_mismatch")
		}
	}
	return allocation, nil
}

func (p *TencentProvider) DestroyComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	if allocation.ID == "" {
		return ComputeAllocation{}, fmt.Errorf("compute_allocation_id_required")
	}
	externallyDeleted := isExternallyDeletedComputeStatus(allocation.Status)
	if !externallyDeleted && firstNonEmpty(allocation.MachineName, allocation.ProviderData["machineName"]) == "" && allocation.NodeName == "" && firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID) == "" {
		allocation.Status = "destroyed"
		allocation.Provider = "tencent-tke"
		return allocation, nil
	}
	response := provisionerResponse{}
	if !externallyDeleted {
		var err error
		response, err = p.provision(ctx, provisionerRequest{
			Action:    "destroy_compute_allocation",
			AccountID: allocation.AccountID,
			PackageID: allocation.PackageID,
			Pool:      provisionerPool{ID: allocation.PoolID, NodePoolID: allocation.NodePoolID},
			Allocation: provisionerAllocation{
				ID:          allocation.ID,
				InstanceID:  firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID),
				MachineName: firstNonEmpty(allocation.MachineName, allocation.ProviderData["machineName"], allocation.NodeName),
				NodeName:    allocation.NodeName,
				PrivateIP:   allocation.PrivateIP,
			},
		})
		if err != nil {
			return ComputeAllocation{}, err
		}
		if !response.OK {
			return ComputeAllocation{}, provisionerError(response)
		}
	}
	serviceName := allocation.ServiceName
	if serviceName == "" && (externallyDeleted || allocation.Status == "running" || allocation.Status == "ready" || allocation.Status == "active" || allocation.Status == "destroying") {
		serviceName = k8sName(allocation.ID)
	}
	if serviceName != "" {
		if _, err := p.callKubectl(ctx, []string{"delete", "deployment/" + serviceName, "service/" + serviceName, "secret/" + serviceName + "-env", "--ignore-not-found=true", "--wait=true"}, nil, protectedresource.Target{PackageID: allocation.PackageID, NodePoolID: allocation.NodePoolID, MachineID: allocation.MachineName, NodeName: allocation.NodeName, CVMID: firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID)}); err != nil {
			return ComputeAllocation{}, err
		}
		allocation.ServiceName = serviceName
	}
	allocation.Status = "destroyed"
	allocation.ProviderRequestID = firstNonEmpty(response.ProviderRequestID, allocation.ProviderRequestID)
	if allocation.Provider == "" {
		allocation.Provider = "tencent-tke"
	}
	return allocation, nil
}
