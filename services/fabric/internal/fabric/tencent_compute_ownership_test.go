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

func TestTencentComputeChildPackageBindingWouldChangePersistedOperationIdentity(t *testing.T) {
	parent := WorkspaceLaunchStageBinding{FabricOperationID: "launch-identity:compute"}
	computeID := "compute-alpha"
	nodePoolOperationID := providerMutationOperationID(parent, "tencent_compute_allocation_create", "compute_allocation", computeID, "np-basic")
	packageOperationID := providerMutationOperationID(parent, "tencent_compute_allocation_create", "compute_allocation", computeID, "basic")
	if nodePoolOperationID != "launch-identity:compute:provider:cf80fb1d988388d1" ||
		packageOperationID != "launch-identity:compute:provider:44f8aca4ae1f1e7f" || nodePoolOperationID == packageOperationID {
		t.Fatalf("NodePool operation ID=%q Package operation ID=%q", nodePoolOperationID, packageOperationID)
	}
}

func TestTencentTagComputeMachineReplaysDeterministicOwnershipChildrenFromAuthoritativeReadback(t *testing.T) {
	setProtectedResourceEnv(t)
	allocation, prepared, ownership := computeClaimProviderFixture()
	ownership.ProviderRequestID = "req-ownership"
	provider := NewTencentProvider()
	provider.convergenceWait = func(context.Context, int) error { return nil }
	store := NewMemoryOperationStore()
	parent := WorkspaceLaunchStageBinding{
		SchemaVersion: 1, LaunchOperationID: "launch-tag-alpha", AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID,
		Stage: "ensure_compute_allocation", Action: "ensure_compute_allocation", FabricOperationID: "launch-tag-alpha:compute",
		IdempotencyKey: "launch-tag-alpha:compute", RequestHash: strings.Repeat("a", 64),
	}
	tagCalls, truthCalls, patchCalls := 0, 0, 0
	nodeOwned := false
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		switch request.Action {
		case "tag_compute_machine":
			tagCalls++
			operationID := providerMutationOperationID(parent, "tencent_cvm_ownership_tag", "compute_binding", allocation.ID, allocation.InstanceID)
			operation, err := store.Get(context.Background(), operationID)
			if err != nil || operation.Status != "started" {
				t.Fatalf("CVM ownership child was not persisted before Tag: operation=%#v err=%v", operation, err)
			}
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
			operationID := providerMutationOperationID(parent, "tencent_kubernetes_node_claim", "compute_binding", allocation.ID, allocation.NodeName)
			operation, err := store.Get(context.Background(), operationID)
			if err != nil || operation.Status != "started" {
				t.Fatalf("Node ownership child was not persisted before Patch: operation=%#v err=%v", operation, err)
			}
			nodeOwned = true
			return nil, nil
		default:
			t.Fatalf("unexpected kubectl args=%#v", args)
			return nil, nil
		}
	}

	service := NewServiceWithOperationStore(provider, store)
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

func TestTencentOwnershipReservedOrUnknownChildrenReplayWithGETOnly(t *testing.T) {
	for _, status := range []string{"started", "failed"} {
		t.Run(status, func(t *testing.T) {
			setProtectedResourceEnv(t)
			allocation, prepared, ownership := computeClaimProviderFixture()
			ownership.ProviderRequestID = "req-ownership"
			provider := NewTencentProvider()
			provider.convergenceWait = func(context.Context, int) error { return nil }
			tagCalls, truthCalls, patchCalls, getCalls := 0, 0, 0, 0
			provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
				switch request.Action {
				case "tag_compute_machine":
					tagCalls++
					return provisionerResponse{}, errors.New("unexpected repeated Tag")
				case "compute_claim_truth":
					truthCalls++
					return tencentTargetOwnedProofResponse(allocation, prepared), nil
				default:
					t.Fatalf("unexpected provisioner action %q", request.Action)
					return provisionerResponse{}, nil
				}
			}
			provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
				switch args[0] {
				case "get":
					getCalls++
					return tencentOwnershipNodeReadback(allocation, ownership, true), nil
				case "patch":
					patchCalls++
					return nil, errors.New("unexpected repeated Node Patch")
				default:
					t.Fatalf("unexpected kubectl args=%#v", args)
					return nil, nil
				}
			}

			store := NewMemoryOperationStore()
			service := NewServiceWithOperationStore(provider, store)
			parent := WorkspaceLaunchStageBinding{
				SchemaVersion: 1, LaunchOperationID: "launch-get-only-" + status, AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID,
				Stage: "ensure_compute_allocation", Action: "ensure_compute_allocation", FabricOperationID: "launch-get-only-" + status + ":compute",
				IdempotencyKey: "launch-get-only-" + status + ":compute", RequestHash: strings.Repeat("f", 64),
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
			for _, child := range []struct {
				action, binding string
			}{
				{action: "tencent_cvm_ownership_tag", binding: allocation.InstanceID},
				{action: "tencent_kubernetes_node_claim", binding: allocation.NodeName},
			} {
				attempt, err := beginProviderMutation(ctx, child.action, "compute_binding", allocation.ID, child.binding)
				if err != nil || attempt == nil || !attempt.Fresh {
					t.Fatalf("persist %s child attempt=%#v err=%v", child.action, attempt, err)
				}
				if status == "failed" {
					if err := attempt.complete(ctx, ownership.ProviderRequestID, ownership, context.DeadlineExceeded); err != nil {
						t.Fatal(err)
					}
				}
			}

			if err := provider.TagComputeMachine(ctx, providerMachineFromComputeAllocation(allocation), ownership); err != nil {
				t.Fatal(err)
			}
			if tagCalls != 0 || patchCalls != 0 || truthCalls != 2 || getCalls != 2 {
				t.Fatalf("tagCalls=%d patchCalls=%d truthCalls=%d getCalls=%d", tagCalls, patchCalls, truthCalls, getCalls)
			}
			assertTencentOwnershipChildOperations(t, store, parent, allocation)
		})
	}
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
	prepareCalls, scaleCalls, tagCalls, truthCalls, patchCalls, readCalls := 0, 0, 0, 0, 0, 0
	nodeOwned := false
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		switch request.Action {
		case "prepare_compute_allocation":
			prepareCalls++
			return provisionerResponse{OK: true, ProviderRequestID: "req-prepare", CurrentReplicas: 1, TargetReplicas: 2, Machines: []provisionerMachine{{MachineID: "machine-before"}}}, nil
		case "create_compute_allocation":
			scaleCalls++
			operationID := providerMutationOperationID(input.Binding, "tencent_compute_allocation_create", "compute_allocation", computeID, prepared.NodePoolID)
			operation, err := store.Get(context.Background(), operationID)
			var state tencentComputeMutationState
			binding, ok := decodeProviderMutationBinding(operation)
			if err != nil || operation.Status != "started" || !ok || binding.Parent != input.Binding ||
				binding.ExpectedResourceBinding != prepared.NodePoolID || !decodeProviderMutationState(operation, &state) ||
				state.Allocation.PackageID != input.PackageID || state.Plan.PackageID != input.PackageID || state.Plan.NodePoolID != prepared.NodePoolID {
				t.Fatalf("Scale child was not persisted with compatible NodePool identity and exact Package/NodePool state: operation=%#v binding=%#v state=%#v err=%v", operation, binding, state, err)
			}
			packageOperationID := providerMutationOperationID(input.Binding, "tencent_compute_allocation_create", "compute_allocation", computeID, input.PackageID)
			if packageOperationID == operationID {
				t.Fatalf("PackageID unexpectedly preserved the persisted operation ID %q", operationID)
			}
			if _, err := store.Get(context.Background(), packageOperationID); !errors.Is(err, ErrOperationNotFound) {
				t.Fatalf("unexpected PackageID child identity %q err=%v", packageOperationID, err)
			}
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
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_ID", "np-basic-rotated")
	if _, err := service.EnsureWorkspaceLaunchStage(context.Background(), input); !errors.Is(err, ErrLaunchStageBindingConflict) {
		t.Fatalf("NodePool configuration drift err=%v", err)
	}
	if prepareCalls != 1 || scaleCalls != 1 || tagCalls != 1 || patchCalls != 1 || truthCalls != 0 || readCalls != 1 {
		t.Fatalf("configuration drift repeated API call: prepareCalls=%d scaleCalls=%d tagCalls=%d patchCalls=%d truthCalls=%d readCalls=%d", prepareCalls, scaleCalls, tagCalls, patchCalls, truthCalls, readCalls)
	}
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_ID", prepared.NodePoolID)
	result, err := service.EnsureWorkspaceLaunchStage(context.Background(), input)
	if err != nil || result.State != "ready" || result.Resources.ComputeAllocationID != computeID || result.Resources.ComputeBindingRef != input.Binding.FabricOperationID {
		t.Fatalf("replayed result=%#v err=%v", result, err)
	}
	ownership, err := store.MachineOwnership(context.Background(), computeID)
	if err != nil || ownership.Status != "active" || ownership.InstanceID != allocation.InstanceID || ownership.NodeName != allocation.NodeName {
		t.Fatalf("ownership=%#v err=%v", ownership, err)
	}
	if prepareCalls != 2 || scaleCalls != 1 || tagCalls != 1 || patchCalls != 1 || truthCalls != 2 || readCalls != 3 {
		t.Fatalf("prepareCalls=%d scaleCalls=%d tagCalls=%d patchCalls=%d truthCalls=%d readCalls=%d", prepareCalls, scaleCalls, tagCalls, patchCalls, truthCalls, readCalls)
	}
	assertTencentOwnershipChildOperations(t, store, input.Binding, allocation)
}

func TestTencentWorkspaceLaunchComputeStateUsesPersistedNodePoolAfterConfigurationDrift(t *testing.T) {
	setProtectedResourceEnv(t)
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS", "20")
	allocation, prepared, ownership := computeClaimProviderFixture()
	parent := WorkspaceLaunchStageBinding{
		SchemaVersion: 1, LaunchOperationID: "launch-readback-alpha", AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID,
		Stage: "ensure_compute_allocation", Action: "ensure_compute_allocation", FabricOperationID: "launch-readback-alpha:compute",
		IdempotencyKey: "launch-readback-alpha:compute", RequestHash: strings.Repeat("e", 64),
	}
	allocation.ID = workspaceLaunchComputeID(parent)
	allocation.OperationID = parent.FabricOperationID
	ownership.ResourceID = allocation.ID

	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(NewTencentProvider(), store)
	outer := newOperation(parent.Action, "workspace_launch_stage", parent.FabricOperationID, parent.AccountID, parent.WorkspaceID, parent.IdempotencyKey, parent.RequestHash, time.Now().UTC())
	outer.ID, outer.OperationID, outer.Status = parent.FabricOperationID, parent.FabricOperationID, "started"
	if err := bindLaunchStageOperation(&outer, &parent); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), outer); err != nil {
		t.Fatal(err)
	}
	ctx := service.providerMutationContext(context.Background(), outer)
	attempt, err := beginProviderMutationWithState(ctx, "tencent_compute_allocation_create", "compute_allocation", allocation.ID, prepared.NodePoolID, tencentComputeMutationState{Allocation: allocation, Plan: prepared})
	if err != nil || attempt == nil || !attempt.Fresh {
		t.Fatalf("persist compute child attempt=%#v err=%v", attempt, err)
	}
	if err := attempt.complete(ctx, allocation.ProviderRequestID, allocation, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ClaimMachine(context.Background(), ownership); err != nil {
		t.Fatal(err)
	}
	for _, configuredNodePool := range []string{"np-basic-rotated", ""} {
		t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_ID", configuredNodePool)
		state, err := service.provider.(*TencentProvider).tencentWorkspaceLaunchComputeStateFromMutation(ctx, parent, "basic")
		if err != nil || state.Compute == nil || state.ComputePlan == nil || state.Ownership == nil ||
			state.Compute.ID != allocation.ID || state.ComputePlan.NodePoolID != prepared.NodePoolID || state.Ownership.ResourceID != allocation.ID {
			t.Fatalf("configuredNodePool=%q state=%#v err=%v", configuredNodePool, state, err)
		}
	}
}

func TestTencentWorkspaceLaunchComputeStateRejectsPersistedPackageOrNodePoolDrift(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		drift func(*ComputeAllocation, *ComputeAllocationPreparation, *MachineOwnership)
	}{
		{name: "allocation package", drift: func(allocation *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership) {
			allocation.PackageID = "pro"
		}},
		{name: "allocation node pool", drift: func(allocation *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership) {
			allocation.NodePoolID = "np-pro"
		}},
		{name: "plan package", drift: func(_ *ComputeAllocation, plan *ComputeAllocationPreparation, _ *MachineOwnership) {
			plan.PackageID = "pro"
		}},
		{name: "plan node pool", drift: func(_ *ComputeAllocation, plan *ComputeAllocationPreparation, _ *MachineOwnership) {
			plan.NodePoolID = "np-pro"
		}},
		{name: "ownership package", drift: func(_ *ComputeAllocation, _ *ComputeAllocationPreparation, ownership *MachineOwnership) {
			ownership.PackageID = "pro"
		}},
		{name: "ownership node pool", drift: func(_ *ComputeAllocation, _ *ComputeAllocationPreparation, ownership *MachineOwnership) {
			ownership.NodePoolID = "np-pro"
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			setProtectedResourceEnv(t)
			allocation, prepared, ownership := computeClaimProviderFixture()
			parent := WorkspaceLaunchStageBinding{
				SchemaVersion: 1, LaunchOperationID: "launch-readback-drift", AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID,
				Stage: "ensure_compute_allocation", Action: "ensure_compute_allocation", FabricOperationID: "launch-readback-drift:compute",
				IdempotencyKey: "launch-readback-drift:compute", RequestHash: strings.Repeat("d", 64),
			}
			allocation.ID = workspaceLaunchComputeID(parent)
			allocation.OperationID = parent.FabricOperationID
			ownership.ResourceID = allocation.ID
			testCase.drift(&allocation, &prepared, &ownership)

			store := NewMemoryOperationStore()
			service := NewServiceWithOperationStore(NewTencentProvider(), store)
			outer := newOperation(parent.Action, "workspace_launch_stage", parent.FabricOperationID, parent.AccountID, parent.WorkspaceID, parent.IdempotencyKey, parent.RequestHash, time.Now().UTC())
			outer.ID, outer.OperationID, outer.Status = parent.FabricOperationID, parent.FabricOperationID, "started"
			if err := bindLaunchStageOperation(&outer, &parent); err != nil {
				t.Fatal(err)
			}
			if err := store.Append(context.Background(), outer); err != nil {
				t.Fatal(err)
			}
			ctx := service.providerMutationContext(context.Background(), outer)
			attempt, err := beginProviderMutationWithState(ctx, "tencent_compute_allocation_create", "compute_allocation", allocation.ID, "np-basic", tencentComputeMutationState{Allocation: allocation, Plan: prepared})
			if err != nil || attempt == nil || !attempt.Fresh {
				t.Fatalf("persist compute child attempt=%#v err=%v", attempt, err)
			}
			if err := attempt.complete(ctx, allocation.ProviderRequestID, allocation, nil); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.ClaimMachine(context.Background(), ownership); err != nil {
				t.Fatal(err)
			}

			if _, err := service.provider.(*TencentProvider).tencentWorkspaceLaunchComputeStateFromMutation(ctx, parent, "basic"); !errors.Is(err, ErrLaunchStageBindingConflict) {
				t.Fatalf("persisted Package/NodePool identity drift err=%v", err)
			}
		})
	}
}

func TestTencentOwnershipTargetOwnedRejectsAuthoritativePoolOrNodePoolDrift(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		drift func(*provisionerResponse)
	}{
		{name: "pool", drift: func(response *provisionerResponse) { response.PoolID = "pool-pro-8c16g" }},
		{name: "node pool", drift: func(response *provisionerResponse) { response.NodePoolID = "np-pro" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			setProtectedResourceEnv(t)
			allocation, prepared, ownership := computeClaimProviderFixture()
			provider := NewTencentProvider()
			provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
				if request.Action != "compute_claim_truth" {
					t.Fatalf("unexpected provisioner action %q", request.Action)
				}
				response := tencentTargetOwnedProofResponse(allocation, prepared)
				testCase.drift(&response)
				return response, nil
			}
			provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
				if len(args) == 0 || args[0] != "get" {
					t.Fatalf("unexpected kubectl args=%#v", args)
				}
				return tencentOwnershipNodeReadback(allocation, ownership, true), nil
			}

			if err := provider.readComputeMachineOwnership(context.Background(), allocation, prepared, ownership, true); err == nil {
				t.Fatalf("target_owned accepted authoritative %s drift", testCase.name)
			}
		})
	}
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
