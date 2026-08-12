package fabric

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func tencentTargetOwnedProofResponse(allocation ComputeAllocation, prepared ComputeAllocationPreparation) provisionerResponse {
	return provisionerResponse{
		OK: true, Status: "proven", PoolID: prepared.PoolID, NodePoolID: prepared.NodePoolID,
		InstanceID: allocation.InstanceID, NodeName: allocation.NodeName, PrivateIP: allocation.PrivateIP, InstanceType: prepared.InstanceType,
		ProviderData: map[string]string{
			"machineName": allocation.MachineName, "zone": allocation.Zone, "chargeType": "PREPAID", "periodMonths": "1",
			"renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": allocation.Deadline, "cvmOwnershipState": "target_owned",
		},
	}
}

func tencentOwnershipNodeReadback(allocation ComputeAllocation, ownership MachineOwnership, owned bool) []byte {
	labels := map[string]string{}
	if owned {
		labels = map[string]string{
			"medopl.cn/workload": "workspace", "oplcloud.cn/resource-id": ownership.ResourceID,
			"oplcloud.cn/account-id": ownership.AccountID, "oplcloud.cn/workspace-id": ownership.WorkspaceID,
		}
	}
	return mustJSON(map[string]any{
		"metadata": map[string]any{"name": allocation.NodeName, "resourceVersion": "7", "labels": labels},
		"spec":     map[string]any{"taints": []any{map[string]any{"key": "oplcloud.cn/package-id", "value": ownership.PackageID, "effect": "NoSchedule"}}},
		"status":   map[string]any{"addresses": []any{map[string]any{"type": "InternalIP", "address": allocation.PrivateIP}}},
	})
}

func assertTencentOwnershipChildOperations(t *testing.T, store OperationStore, parent WorkspaceLaunchStageBinding, allocation ComputeAllocation) {
	t.Helper()
	for _, expected := range []struct {
		action, binding string
	}{
		{action: "tencent_cvm_ownership_tag", binding: firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID)},
		{action: "tencent_kubernetes_node_claim", binding: allocation.NodeName},
	} {
		operationID := providerMutationOperationID(parent, expected.action, "compute_binding", allocation.ID, expected.binding)
		operation, err := store.Get(context.Background(), operationID)
		binding, ok := decodeProviderMutationBinding(operation)
		if err != nil || !ok || operation.Status != "succeeded" || binding.Parent != parent || binding.Action != expected.action ||
			binding.ResourceKind != "compute_binding" || binding.ResourceID != allocation.ID || binding.ExpectedResourceBinding != expected.binding ||
			binding.FabricOperationID != operationID {
			t.Fatalf("child %s operation=%#v binding=%#v/%v err=%v", expected.action, operation, binding, ok, err)
		}
	}
}

func TestTencentTagComputeMachineReplaysDeterministicOwnershipChildrenFromAuthoritativeReadback(t *testing.T) {
	setProtectedResourceEnv(t)
	allocation, prepared, ownership := computeClaimProviderFixture()
	ownership.ProviderRequestID = "req-ownership"
	provider := NewTencentProvider()
	provider.convergenceWait = func(context.Context, int) error { return nil }
	tagCalls, truthCalls, patchCalls := 0, 0, 0
	nodeOwned := false
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		switch request.Action {
		case "tag_compute_machine":
			tagCalls++
			return provisionerResponse{OK: true, Status: "tagged", MutationEvidence: &ComputeClaimMutationEvidence{}}, nil
		case "compute_claim_truth":
			truthCalls++
			if request.Allocation.ID != allocation.ID || request.Allocation.InstanceID != allocation.InstanceID ||
				request.Pool.ID != prepared.PoolID || request.Tags["opl_operation_id"] != ownership.ID {
				t.Fatalf("truth request=%#v", request)
			}
			return tencentTargetOwnedProofResponse(allocation, prepared), nil
		default:
			t.Fatalf("unexpected provisioner action %q", request.Action)
			return provisionerResponse{}, nil
		}
	}
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		switch args[0] {
		case "get":
			return tencentOwnershipNodeReadback(allocation, ownership, nodeOwned), nil
		case "patch":
			patchCalls++
			nodeOwned = true
			return nil, nil
		default:
			t.Fatalf("unexpected kubectl args=%#v", args)
			return nil, nil
		}
	}

	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(provider, store)
	parent := WorkspaceLaunchStageBinding{
		SchemaVersion: 1, LaunchOperationID: "launch-tag-alpha", AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID,
		Stage: "ensure_compute_allocation", Action: "ensure_compute_allocation", FabricOperationID: "launch-tag-alpha:compute",
		IdempotencyKey: "launch-tag-alpha:compute", RequestHash: strings.Repeat("a", 64),
	}
	operation := newOperation(parent.Action, "workspace_launch_stage", parent.FabricOperationID, parent.AccountID, parent.WorkspaceID, parent.IdempotencyKey, parent.RequestHash, time.Now().UTC())
	operation.ID, operation.OperationID, operation.Status = parent.FabricOperationID, parent.FabricOperationID, "started"
	operation.RedactedProviderPayload = computeAllocationOperationPayload(allocation, prepared)
	if err := bindLaunchStageOperation(&operation, &parent); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	ctx := service.providerMutationContext(context.Background(), operation)
	machine := providerMachineFromComputeAllocation(allocation)

	if err := provider.TagComputeMachine(ctx, machine, ownership); err != nil {
		t.Fatal(err)
	}
	if err := provider.TagComputeMachine(ctx, machine, ownership); err != nil {
		t.Fatal(err)
	}

	if tagCalls != 1 || patchCalls != 1 || truthCalls != 2 {
		t.Fatalf("tagCalls=%d patchCalls=%d truthCalls=%d", tagCalls, patchCalls, truthCalls)
	}
	assertTencentOwnershipChildOperations(t, store, parent, allocation)
}

func TestTencentWorkspaceLaunchComputeReplayReusesOwnershipCoreWithoutRepeatedMutation(t *testing.T) {
	setProtectedResourceEnv(t)
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS", "20")
	provider := NewTencentProvider()
	provider.convergenceWait = func(context.Context, int) error { return nil }
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(provider, store)

	image := "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app@sha256:" + strings.Repeat("c", 64)
	launchHash := strings.Repeat("d", 64)
	preflightInput := WorkspaceLaunchPreflightInput{
		SchemaVersion: 1, LaunchOperationID: "launch-workspace-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: image, RequestHash: launchHash,
	}
	admission := workspaceLaunchPreflightAdmission{SchemaVersion: 1, Input: preflightInput, ProviderProfileRef: "tencent-tke"}
	admission.BindingRef = "fabric-preflight:" + hashInput(admission)
	if err := service.persistWorkspaceLaunchPreflight(context.Background(), admission); err != nil {
		t.Fatal(err)
	}
	input := workspaceLaunchStageFixtureInput(
		WorkspaceLaunchPreflight{BindingRef: admission.BindingRef}, image, launchHash,
		"ensure_compute_allocation", "ensure_compute_allocation", WorkspaceLaunchResources{},
	)
	input.Binding.LaunchOperationID = preflightInput.LaunchOperationID
	input.Binding.FabricOperationID = preflightInput.LaunchOperationID + ":ensure_compute_allocation"
	input.Binding.IdempotencyKey = input.Binding.FabricOperationID
	input.Binding.RequestHash = workspaceLaunchStageRequestHash(input, launchHash)
	computeID := workspaceLaunchComputeID(input.Binding)
	allocation := ComputeAllocation{
		ID: computeID, AccountID: input.Binding.AccountID, WorkspaceID: input.Binding.WorkspaceID, PackageID: input.PackageID, Provider: "tencent-tke",
		ProviderResourceID: "ins-alpha", PoolID: "pool-basic-2c4g", NodePoolID: "np-basic", MachineName: "machine-alpha",
		InstanceID: "ins-alpha", CVMInstanceID: "ins-alpha", NodeName: "node-alpha", PrivateIP: "10.0.0.8", PublicIP: "203.0.113.8",
		InstanceType: "SA5.MEDIUM4", Zone: "ap-guangzhou-3", ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-28T00:00:00Z",
	}
	prepared := ComputeAllocationPreparation{
		PoolID: allocation.PoolID, PackageID: allocation.PackageID, NodePoolID: allocation.NodePoolID, InstanceType: allocation.InstanceType,
		MaxReplicas: 20, BaselineReplicas: 1, TargetReplicas: 2, BeforeMachineNames: []string{"machine-before"},
	}
	tagCalls, truthCalls, patchCalls, readCalls := 0, 0, 0, 0
	nodeOwned := false
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		switch request.Action {
		case "prepare_compute_allocation":
			return provisionerResponse{OK: true, ProviderRequestID: "req-prepare", CurrentReplicas: 1, TargetReplicas: 2, Machines: []provisionerMachine{{MachineID: "machine-before"}}}, nil
		case "create_compute_allocation":
			return tencentComputeAllocationResponse(allocation, "req-create"), nil
		case "tag_compute_machine":
			tagCalls++
			return provisionerResponse{OK: true, Status: "tagged", MutationEvidence: &ComputeClaimMutationEvidence{}}, nil
		case "read_compute_allocation":
			readCalls++
			if readCalls == 1 {
				return provisionerResponse{}, errors.New("readback temporarily unavailable")
			}
			return tencentComputeAllocationResponse(allocation, "req-read"), nil
		case "compute_claim_truth":
			truthCalls++
			return tencentTargetOwnedProofResponse(allocation, prepared), nil
		default:
			t.Fatalf("unexpected provisioner action %q", request.Action)
			return provisionerResponse{}, nil
		}
	}
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		ownership := MachineOwnership{
			ResourceID: allocation.ID, AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID,
			PackageID: allocation.PackageID, NodePoolID: allocation.NodePoolID,
		}
		switch args[0] {
		case "get":
			return tencentOwnershipNodeReadback(allocation, ownership, nodeOwned), nil
		case "patch":
			patchCalls++
			nodeOwned = true
			return nil, nil
		default:
			t.Fatalf("unexpected kubectl args=%#v", args)
			return nil, nil
		}
	}

	if _, err := service.EnsureWorkspaceLaunchStage(context.Background(), input); err == nil || err.Error() != "readback temporarily unavailable" {
		t.Fatalf("first ensure error=%v", err)
	}
	result, err := service.EnsureWorkspaceLaunchStage(context.Background(), input)
	if err != nil || result.State != "ready" || result.Resources.ComputeAllocationID != computeID || result.Resources.ComputeBindingRef != input.Binding.FabricOperationID {
		t.Fatalf("replayed result=%#v err=%v", result, err)
	}
	ownership, err := store.MachineOwnership(context.Background(), computeID)
	if err != nil || ownership.Status != "active" || ownership.InstanceID != allocation.InstanceID || ownership.NodeName != allocation.NodeName {
		t.Fatalf("ownership=%#v err=%v", ownership, err)
	}
	if tagCalls != 1 || patchCalls != 1 || truthCalls != 2 || readCalls != 3 {
		t.Fatalf("tagCalls=%d patchCalls=%d truthCalls=%d readCalls=%d", tagCalls, patchCalls, truthCalls, readCalls)
	}
	assertTencentOwnershipChildOperations(t, store, input.Binding, allocation)
}

func tencentComputeAllocationResponse(allocation ComputeAllocation, requestID string) provisionerResponse {
	return provisionerResponse{
		OK: true, Status: "running", ProviderRequestID: requestID, InstanceID: allocation.InstanceID,
		NodeName: allocation.NodeName, PrivateIP: allocation.PrivateIP, PublicIP: allocation.PublicIP,
		ProviderData: map[string]string{
			"machineName": allocation.MachineName, "zone": allocation.Zone, "chargeType": allocation.ChargeType,
			"renewFlag": allocation.RenewFlag, "deadline": allocation.Deadline,
		},
	}
}
