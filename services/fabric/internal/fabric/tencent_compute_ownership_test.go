package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
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

	if tagCalls != 0 || patchCalls != 1 || truthCalls != 3 {
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

func TestTencentOwnershipReservedChildrenReplayAuthoritativeAbsenceOnce(t *testing.T) {
	for _, status := range []string{"started", "failed"} {
		t.Run(status, func(t *testing.T) {
			setProtectedResourceEnv(t)
			allocation, prepared, ownership := computeClaimProviderFixture()
			ownership.ProviderRequestID = "req-ownership-replay"
			provider := NewTencentProvider()
			provider.convergenceWait = func(context.Context, int) error { return nil }
			cvmOwned, nodeOwned := false, false
			tagCalls, truthCalls, patchCalls, getCalls := 0, 0, 0, 0
			provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
				switch request.Action {
				case "compute_claim_truth":
					truthCalls++
					response := tencentTargetOwnedProofResponse(allocation, prepared)
					if !cvmOwned {
						response.ProviderData["cvmOwnershipState"] = "recoverable"
					}
					return response, nil
				case "tag_compute_machine":
					tagCalls++
					if request.Allocation.ID != allocation.ID || request.Allocation.InstanceID != allocation.InstanceID ||
						request.Tags["opl_operation_id"] != ownership.ID {
						t.Fatalf("replay changed CVM identity: request=%#v", request)
					}
					cvmOwned = true
					return provisionerResponse{OK: true, Status: "tagged", MutationCount: 1, MutationEvidence: &ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}}, nil
				default:
					t.Fatalf("unexpected provisioner action %q", request.Action)
					return provisionerResponse{}, nil
				}
			}
			provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
				switch args[0] {
				case "get":
					getCalls++
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
				SchemaVersion: 1, LaunchOperationID: "launch-absent-replay-" + status, AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID,
				Stage: "ensure_compute_allocation", Action: "ensure_compute_allocation", FabricOperationID: "launch-absent-replay-" + status + ":compute",
				IdempotencyKey: "launch-absent-replay-" + status + ":compute", RequestHash: strings.Repeat("9", 64),
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
			if tagCalls != 1 || patchCalls != 1 {
				t.Fatalf("authoritative absence replay mutations tag=%d patch=%d truth=%d get=%d", tagCalls, patchCalls, truthCalls, getCalls)
			}
			for _, child := range []struct {
				action, binding string
			}{
				{action: "tencent_cvm_ownership_tag", binding: allocation.InstanceID},
				{action: "tencent_kubernetes_node_claim", binding: allocation.NodeName},
			} {
				childID := providerMutationOperationID(parent, child.action, "compute_binding", allocation.ID, child.binding)
				persisted, err := store.Get(context.Background(), childID)
				epoch, epochOK := decodeProviderMutationReplayEpoch(persisted)
				if err != nil || persisted.Status != "succeeded" || persisted.IdempotencyKey != childID || !epochOK || epoch.State != "succeeded" {
					t.Fatalf("child %s did not converge with original identity: operation=%#v epoch=%#v err=%v", child.action, persisted, epoch, err)
				}
			}
		})
	}
}

type tencentOwnershipReplayFixture struct {
	provider   *TencentProvider
	store      *MemoryOperationStore
	service    *Service
	ctx        context.Context
	parent     WorkspaceLaunchStageBinding
	allocation ComputeAllocation
	prepared   ComputeAllocationPreparation
	ownership  MachineOwnership
}

func newTencentOwnershipReplayFixture(t *testing.T, suffix string) tencentOwnershipReplayFixture {
	t.Helper()
	setProtectedResourceEnv(t)
	allocation, prepared, ownership := computeClaimProviderFixture()
	ownership.ProviderRequestID = "req-ownership-" + suffix
	provider := NewTencentProvider()
	provider.convergenceWait = func(context.Context, int) error { return nil }
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(provider, store)
	parent := WorkspaceLaunchStageBinding{
		SchemaVersion: 1, LaunchOperationID: "launch-ownership-" + suffix, AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID,
		Stage: "ensure_compute_allocation", Action: "ensure_compute_allocation", FabricOperationID: "launch-ownership-" + suffix + ":compute",
		IdempotencyKey: "launch-ownership-" + suffix + ":compute", RequestHash: strings.Repeat("8", 64),
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
	return tencentOwnershipReplayFixture{
		provider: provider, store: store, service: service, ctx: service.providerMutationContext(context.Background(), operation),
		parent: parent, allocation: allocation, prepared: prepared, ownership: ownership,
	}
}

func (f tencentOwnershipReplayFixture) reserveChild(t *testing.T, action, binding, status string) string {
	t.Helper()
	attempt, err := beginProviderMutation(f.ctx, action, "compute_binding", f.allocation.ID, binding)
	if err != nil || attempt == nil || !attempt.Fresh {
		t.Fatalf("reserve %s attempt=%#v err=%v", action, attempt, err)
	}
	if status == "failed" {
		if err := attempt.complete(f.ctx, f.ownership.ProviderRequestID, f.ownership, context.DeadlineExceeded); err != nil {
			t.Fatal(err)
		}
	}
	return providerMutationOperationID(f.parent, action, "compute_binding", f.allocation.ID, binding)
}

func (f tencentOwnershipReplayFixture) converge() error {
	return f.provider.TagComputeMachine(f.ctx, providerMachineFromComputeAllocation(f.allocation), f.ownership)
}

func TestTencentOwnershipReplaySecondReadReadyConvergesWithoutMutation(t *testing.T) {
	t.Run("CVM ready race", func(t *testing.T) {
		fixture := newTencentOwnershipReplayFixture(t, "cvm-ready-race")
		childID := fixture.reserveChild(t, "tencent_cvm_ownership_tag", fixture.allocation.InstanceID, "started")
		var truthCalls, tagCalls atomic.Int64
		fixture.provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
			switch request.Action {
			case "compute_claim_truth":
				call := truthCalls.Add(1)
				response := tencentTargetOwnedProofResponse(fixture.allocation, fixture.prepared)
				if call == 1 {
					response.ProviderData["cvmOwnershipState"] = "recoverable"
				}
				return response, nil
			case "tag_compute_machine":
				tagCalls.Add(1)
				return provisionerResponse{}, errors.New("ready race attempted CVM mutation")
			default:
				t.Fatalf("unexpected action %q", request.Action)
				return provisionerResponse{}, nil
			}
		}
		fixture.provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
			if args[0] != "get" {
				t.Fatalf("ready race attempted kubectl mutation: %#v", args)
			}
			return tencentOwnershipNodeReadback(fixture.allocation, fixture.ownership, true), nil
		}
		if err := fixture.converge(); err != nil {
			t.Fatal(err)
		}
		persisted, err := fixture.store.Get(context.Background(), childID)
		epoch, epochOK := decodeProviderMutationReplayEpoch(persisted)
		if err != nil || tagCalls.Load() != 0 || truthCalls.Load() != 2 || persisted.Status != "succeeded" || !epochOK || epoch.State != "succeeded" {
			t.Fatalf("ready race child=%#v epoch=%#v truth=%d tag=%d err=%v", persisted, epoch, truthCalls.Load(), tagCalls.Load(), err)
		}
	})

	t.Run("Node ready race", func(t *testing.T) {
		fixture := newTencentOwnershipReplayFixture(t, "node-ready-race")
		cvmID := fixture.reserveChild(t, "tencent_cvm_ownership_tag", fixture.allocation.InstanceID, "started")
		cvm, err := fixture.store.Get(context.Background(), cvmID)
		if err != nil {
			t.Fatal(err)
		}
		cvm.Status = "succeeded"
		cvm.FinishedAt = time.Now().UTC()
		if err := fixture.store.SaveRuntime(context.Background(), cvm); err != nil {
			t.Fatal(err)
		}
		childID := fixture.reserveChild(t, "tencent_kubernetes_node_claim", fixture.allocation.NodeName, "started")
		var nodeReads, patchCalls atomic.Int64
		fixture.provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
			if request.Action != "compute_claim_truth" {
				t.Fatalf("unexpected action %q", request.Action)
			}
			return tencentTargetOwnedProofResponse(fixture.allocation, fixture.prepared), nil
		}
		fixture.provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
			if args[0] == "patch" {
				patchCalls.Add(1)
				return nil, errors.New("ready race attempted node mutation")
			}
			owned := nodeReads.Add(1) >= 3
			return tencentOwnershipNodeReadback(fixture.allocation, fixture.ownership, owned), nil
		}
		if err := fixture.converge(); err != nil {
			t.Fatal(err)
		}
		persisted, err := fixture.store.Get(context.Background(), childID)
		epoch, epochOK := decodeProviderMutationReplayEpoch(persisted)
		if err != nil || patchCalls.Load() != 0 || nodeReads.Load() != 3 || persisted.Status != "succeeded" || !epochOK || epoch.State != "succeeded" {
			t.Fatalf("ready race child=%#v epoch=%#v reads=%d patch=%d err=%v", persisted, epoch, nodeReads.Load(), patchCalls.Load(), err)
		}
	})
}

func TestTencentOwnershipReplayUncertainReadFailsClosedWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		providerFn func(tencentOwnershipReplayFixture) (provisionerResponse, error)
	}{
		{name: "provider error", providerFn: func(tencentOwnershipReplayFixture) (provisionerResponse, error) {
			return provisionerResponse{}, context.DeadlineExceeded
		}},
		{name: "identity conflict", providerFn: func(f tencentOwnershipReplayFixture) (provisionerResponse, error) {
			response := tencentTargetOwnedProofResponse(f.allocation, f.prepared)
			response.ProviderData["machineName"] = "machine-conflict"
			return response, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTencentOwnershipReplayFixture(t, strings.ReplaceAll(tc.name, " ", "-"))
			fixture.reserveChild(t, "tencent_cvm_ownership_tag", fixture.allocation.InstanceID, "started")
			var tagCalls, patchCalls atomic.Int64
			fixture.provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
				if request.Action == "tag_compute_machine" {
					tagCalls.Add(1)
					return provisionerResponse{}, errors.New("uncertain read attempted mutation")
				}
				return tc.providerFn(fixture)
			}
			fixture.provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
				if args[0] == "patch" {
					patchCalls.Add(1)
				}
				return tencentOwnershipNodeReadback(fixture.allocation, fixture.ownership, false), nil
			}
			if err := fixture.converge(); err == nil {
				t.Fatal("uncertain authority unexpectedly converged")
			}
			if tagCalls.Load() != 0 || patchCalls.Load() != 0 {
				t.Fatalf("uncertain authority mutated tag=%d patch=%d", tagCalls.Load(), patchCalls.Load())
			}
		})
	}
}

func TestTencentOwnershipReplayResponseLossConvergesByGETOnly(t *testing.T) {
	fixture := newTencentOwnershipReplayFixture(t, "response-loss")
	childID := fixture.reserveChild(t, "tencent_cvm_ownership_tag", fixture.allocation.InstanceID, "started")
	var cvmOwned atomic.Bool
	var tagCalls atomic.Int64
	fixture.provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		switch request.Action {
		case "compute_claim_truth":
			response := tencentTargetOwnedProofResponse(fixture.allocation, fixture.prepared)
			if !cvmOwned.Load() {
				response.ProviderData["cvmOwnershipState"] = "recoverable"
			}
			return response, nil
		case "tag_compute_machine":
			tagCalls.Add(1)
			cvmOwned.Store(true)
			return provisionerResponse{}, context.DeadlineExceeded
		default:
			t.Fatalf("unexpected action %q", request.Action)
			return provisionerResponse{}, nil
		}
	}
	fixture.provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if args[0] != "get" {
			t.Fatalf("response-loss attempted node mutation: %#v", args)
		}
		return tencentOwnershipNodeReadback(fixture.allocation, fixture.ownership, true), nil
	}
	if err := fixture.converge(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first response-loss err=%v", err)
	}
	persisted, err := fixture.store.Get(context.Background(), childID)
	epoch, epochOK := decodeProviderMutationReplayEpoch(persisted)
	if err != nil || !epochOK || epoch.State != "awaiting_readback" || tagCalls.Load() != 1 {
		t.Fatalf("response-loss child=%#v epoch=%#v tag=%d err=%v", persisted, epoch, tagCalls.Load(), err)
	}
	if err := fixture.converge(); err != nil {
		t.Fatal(err)
	}
	persisted, err = fixture.store.Get(context.Background(), childID)
	epoch, epochOK = decodeProviderMutationReplayEpoch(persisted)
	if err != nil || tagCalls.Load() != 1 || persisted.Status != "succeeded" || !epochOK || epoch.State != "succeeded" {
		t.Fatalf("GET-only recovery child=%#v epoch=%#v tag=%d err=%v", persisted, epoch, tagCalls.Load(), err)
	}
}

func TestTencentOwnershipReplayConcurrentResumeHasOneWriter(t *testing.T) {
	fixture := newTencentOwnershipReplayFixture(t, "concurrent")
	childID := fixture.reserveChild(t, "tencent_cvm_ownership_tag", fixture.allocation.InstanceID, "started")
	var truthCalls, tagCalls atomic.Int64
	var nodeMutationCalls atomic.Int64
	var cvmOwned atomic.Bool
	firstReadEntered := make(chan struct{})
	secondReadEntered := make(chan struct{})
	mutationCompleted := make(chan struct{})
	staleCompletionReturned := make(chan struct{})
	fixture.provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		switch request.Action {
		case "compute_claim_truth":
			call := truthCalls.Add(1)
			switch call {
			case 1:
				close(firstReadEntered)
				<-secondReadEntered
			case 2:
				close(secondReadEntered)
				<-mutationCompleted
			}
			response := tencentTargetOwnedProofResponse(fixture.allocation, fixture.prepared)
			if !cvmOwned.Load() {
				response.ProviderData["cvmOwnershipState"] = "recoverable"
			}
			return response, nil
		case "tag_compute_machine":
			tagCalls.Add(1)
			cvmOwned.Store(true)
			close(mutationCompleted)
			<-staleCompletionReturned
			return provisionerResponse{OK: true, Status: "tagged", MutationCount: 1, MutationEvidence: &ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}}, nil
		default:
			t.Fatalf("unexpected action %q", request.Action)
			return provisionerResponse{}, nil
		}
	}
	fixture.provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if args[0] != "get" {
			nodeMutationCalls.Add(1)
			return nil, errors.New("concurrent replay attempted node mutation")
		}
		return tencentOwnershipNodeReadback(fixture.allocation, fixture.ownership, true), nil
	}
	firstResult, secondResult := make(chan error, 1), make(chan error, 1)
	go func() { firstResult <- fixture.converge() }()
	<-firstReadEntered
	go func() {
		secondResult <- fixture.converge()
		close(staleCompletionReturned)
	}()
	first, second := <-firstResult, <-secondResult
	if first != nil || !errors.Is(second, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("concurrent mutation writer=%v stale reader=%v", first, second)
	}
	if err := fixture.provider.readComputeMachineOwnership(context.Background(), fixture.allocation, fixture.prepared, fixture.ownership, true); err != nil {
		t.Fatalf("authoritative final ownership: %v", err)
	}
	if tagCalls.Load() != 1 || nodeMutationCalls.Load() != 0 {
		t.Fatalf("concurrent replay mutations tag=%d node=%d truth=%d", tagCalls.Load(), nodeMutationCalls.Load(), truthCalls.Load())
	}
	persisted, err := fixture.store.Get(context.Background(), childID)
	binding, bindingOK := decodeProviderMutationBinding(persisted)
	epoch, epochOK := decodeProviderMutationReplayEpoch(persisted)
	if err != nil || persisted.Status != "succeeded" || persisted.ID != childID || persisted.OperationID != childID || persisted.IdempotencyKey != childID ||
		!bindingOK || binding.Parent != fixture.parent || binding.FabricOperationID != childID || !epochOK || epoch.State != "succeeded" ||
		epoch.ParentFabricOperationID != fixture.parent.FabricOperationID || epoch.ChildOperationID != childID || epoch.IdempotencyKey != childID {
		t.Fatalf("concurrent final child=%#v binding=%#v/%v epoch=%#v/%v err=%v", persisted, binding, bindingOK, epoch, epochOK, err)
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
	admission := workspaceLaunchPreflightAdmission{SchemaVersion: 1, Input: preflightInput, ProviderProfileRef: "tencent-tke",
		CanonicalProviderPlan: json.RawMessage(`{"packageId":"basic","providerProfileRef":"tencent-tke","schemaVersion":1,"spec":{"compute":{"cpu":2,"memoryGb":4},"storage":{"sizeGb":10}}}`)}
	admission.SpecDigest = providerPlanDigest(admission.CanonicalProviderPlan)
	admission.ProviderBindingRef = workspaceLaunchPreflightBindingRef(admission)
	if err := service.persistWorkspaceLaunchPreflight(context.Background(), admission); err != nil {
		t.Fatal(err)
	}
	input := workspaceLaunchStageFixtureInput(
		WorkspaceLaunchPreflight{ProviderBindingRef: admission.ProviderBindingRef, SpecDigest: admission.SpecDigest}, image, launchHash,
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
	if prepareCalls != 1 || scaleCalls != 1 || tagCalls != 0 || patchCalls != 1 || truthCalls != 1 || readCalls != 2 {
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
	if prepareCalls != 1 || scaleCalls != 1 || tagCalls != 0 || patchCalls != 1 || truthCalls != 2 || readCalls != 3 {
		t.Fatalf("prepareCalls=%d scaleCalls=%d tagCalls=%d patchCalls=%d truthCalls=%d readCalls=%d", prepareCalls, scaleCalls, tagCalls, patchCalls, truthCalls, readCalls)
	}
	assertTencentOwnershipChildOperations(t, store, input.Binding, allocation)
}

type tencentWorkspaceLaunchComputeReadRecoveryFixture struct {
	service    *Service
	store      *MemoryOperationStore
	provider   *TencentProvider
	input      WorkspaceLaunchStageInput
	allocation ComputeAllocation
	prepared   ComputeAllocationPreparation
	computeID  string
}

func newTencentWorkspaceLaunchComputeReadRecoveryFixture(t *testing.T) tencentWorkspaceLaunchComputeReadRecoveryFixture {
	t.Helper()
	service, store, provider, preflight, image, launchHash := newTencentWorkspaceLaunchService(t)
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS", "20")
	provider.convergenceWait = func(context.Context, int) error { return nil }
	input := workspaceLaunchStageFixtureInput(preflight, image, launchHash, "ensure_compute_allocation", "ensure_compute_allocation", WorkspaceLaunchResources{})
	computeID := workspaceLaunchComputeID(input.Binding)
	allocation := ComputeAllocation{
		ID: computeID, OperationID: input.Binding.FabricOperationID, AccountID: input.Binding.AccountID, WorkspaceID: input.Binding.WorkspaceID,
		PackageID: "basic", Provider: "tencent-tke", ProviderResourceID: "ins-read-recovery", PoolID: "pool-basic-2c4g", NodePoolID: "np-basic",
		MachineName: "machine-read-recovery", InstanceID: "ins-read-recovery", CVMInstanceID: "ins-read-recovery", NodeName: "node-read-recovery",
		PrivateIP: "10.0.0.28", PublicIP: "203.0.113.28", InstanceType: "SA5.MEDIUM4", Zone: "ap-guangzhou-3",
		ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-09-12T00:00:00Z", Status: "ready",
	}
	prepared := ComputeAllocationPreparation{
		PoolID: allocation.PoolID, PackageID: allocation.PackageID, NodePoolID: allocation.NodePoolID, InstanceType: allocation.InstanceType,
		MaxReplicas: 20, BaselineReplicas: 1, TargetReplicas: 2, BeforeMachineNames: []string{"machine-before"},
	}
	operation, _, err := newWorkspaceLaunchStageOperation(input, "tencent-tke", func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	ctx := service.providerMutationContext(context.Background(), operation)
	child, err := beginProviderMutationWithState(ctx, "tencent_compute_allocation_create", "compute_allocation", computeID, prepared.NodePoolID, tencentComputeMutationState{Allocation: allocation, Plan: prepared})
	if err != nil || child == nil || !child.Fresh {
		t.Fatalf("compute child=%#v err=%v", child, err)
	}
	return tencentWorkspaceLaunchComputeReadRecoveryFixture{
		service: service, store: store, provider: provider, input: input,
		allocation: allocation, prepared: prepared, computeID: computeID,
	}
}

func TestTencentWorkspaceLaunchComputeReadIsGETOnlyBeforeSameOperationOwnershipRecovery(t *testing.T) {
	fixture := newTencentWorkspaceLaunchComputeReadRecoveryFixture(t)
	service, store, provider := fixture.service, fixture.store, fixture.provider
	input, allocation, prepared, computeID := fixture.input, fixture.allocation, fixture.prepared, fixture.computeID
	readCalls, truthCalls, tagCalls, patchCalls := 0, 0, 0, 0
	tagOwned, nodeOwned := false, false
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		switch request.Action {
		case "read_compute_allocation":
			readCalls++
			return tencentComputeAllocationResponse(allocation, "req-read-recovery"), nil
		case "compute_claim_truth":
			truthCalls++
			response := tencentTargetOwnedProofResponse(allocation, prepared)
			if !tagOwned {
				response.ProviderData["cvmOwnershipState"] = "recoverable"
			}
			return response, nil
		case "tag_compute_machine":
			tagCalls++
			tagOwned = true
			return provisionerResponse{OK: true, Status: "tagged", MutationEvidence: &ComputeClaimMutationEvidence{}}, nil
		default:
			t.Fatalf("unexpected provisioner action %q", request.Action)
			return provisionerResponse{}, nil
		}
	}
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		ownership := MachineOwnership{ResourceID: allocation.ID, AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID, PackageID: allocation.PackageID, NodePoolID: allocation.NodePoolID}
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

	readback, err := service.ReadWorkspaceLaunchStage(context.Background(), input)
	if err != nil || readback.State != "absent" || readCalls != 1 || truthCalls != 1 || tagCalls != 0 || patchCalls != 0 {
		t.Fatalf("GET-only readback=%#v err=%v read=%d truth=%d tag=%d patch=%d", readback, err, readCalls, truthCalls, tagCalls, patchCalls)
	}
	if _, err := store.MachineOwnership(context.Background(), computeID); !errors.Is(err, ErrMachineOwnershipNotFound) {
		t.Fatalf("GET-only read persisted ownership err=%v", err)
	}

	recovered, err := service.EnsureWorkspaceLaunchStage(context.Background(), input)
	if err != nil || recovered.State != "ready" || recovered.Resources.ComputeAllocationID != computeID || tagCalls != 1 || patchCalls != 1 {
		t.Fatalf("recovered=%#v err=%v read=%d truth=%d tag=%d patch=%d", recovered, err, readCalls, truthCalls, tagCalls, patchCalls)
	}
	ownership, err := store.MachineOwnership(context.Background(), computeID)
	if err != nil || ownership.Status != "active" || ownership.ResourceID != computeID || ownership.AccountID != input.Binding.AccountID || ownership.WorkspaceID != input.Binding.WorkspaceID {
		t.Fatalf("recovered ownership=%#v err=%v", ownership, err)
	}
	assertTencentOwnershipChildOperations(t, store, input.Binding, allocation)
}

func TestTencentWorkspaceLaunchComputeReadMissingOwnershipFailsClosedOnAuthoritativeConflictOrError(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	for _, tc := range []struct {
		name             string
		cvmState         string
		nodeOwned        bool
		mutateProof      func(*provisionerResponse)
		truthErr         error
		mutateNode       func([]byte) []byte
		wantSafeAbsent   bool
		wantTruthReads   int
		wantKubectlReads int
	}{
		{name: "recoverable cvm and unallocated node", cvmState: "recoverable", wantSafeAbsent: true, wantTruthReads: 1, wantKubectlReads: 1},
		{name: "target owned cvm and node", cvmState: "target_owned", nodeOwned: true, wantSafeAbsent: true, wantTruthReads: 1, wantKubectlReads: 1},
		{name: "cvm identity conflict", cvmState: "target_owned", mutateProof: func(response *provisionerResponse) {
			response.ProviderData["machineName"] = "machine-other"
		}, wantTruthReads: 1},
		{name: "node identity conflict", cvmState: "target_owned", mutateNode: func(raw []byte) []byte {
			var node map[string]any
			if json.Unmarshal(raw, &node) != nil {
				t.Fatal("decode node fixture")
			}
			metadata := node["metadata"].(map[string]any)
			metadata["name"] = "node-other"
			return mustJSON(node)
		}, wantTruthReads: 1, wantKubectlReads: 1},
		{name: "provider read error", cvmState: "target_owned", truthErr: providerErr, wantTruthReads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTencentWorkspaceLaunchComputeReadRecoveryFixture(t)
			readCalls, truthCalls, tagCalls, kubectlReads, patchCalls := 0, 0, 0, 0, 0
			fixture.provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
				switch request.Action {
				case "read_compute_allocation":
					readCalls++
					return tencentComputeAllocationResponse(fixture.allocation, "req-read-matrix"), nil
				case "compute_claim_truth":
					truthCalls++
					if tc.truthErr != nil {
						return provisionerResponse{}, tc.truthErr
					}
					response := tencentTargetOwnedProofResponse(fixture.allocation, fixture.prepared)
					response.ProviderData["cvmOwnershipState"] = tc.cvmState
					if tc.mutateProof != nil {
						tc.mutateProof(&response)
					}
					return response, nil
				case "tag_compute_machine":
					tagCalls++
					return provisionerResponse{}, errors.New("read attempted tag mutation")
				default:
					t.Fatalf("unexpected provisioner action %q", request.Action)
					return provisionerResponse{}, nil
				}
			}
			fixture.provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
				switch args[0] {
				case "get":
					kubectlReads++
					ownership, err := workspaceLaunchComputeOwnership(fixture.allocation)
					if err != nil {
						t.Fatal(err)
					}
					raw := tencentOwnershipNodeReadback(fixture.allocation, ownership, tc.nodeOwned)
					if tc.mutateNode != nil {
						raw = tc.mutateNode(raw)
					}
					return raw, nil
				case "patch":
					patchCalls++
					return nil, errors.New("read attempted node mutation")
				default:
					t.Fatalf("unexpected kubectl args=%#v", args)
					return nil, nil
				}
			}
			before, err := fixture.store.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			result, readErr := fixture.service.ReadWorkspaceLaunchStage(context.Background(), fixture.input)
			after, listErr := fixture.store.List(context.Background())
			if listErr != nil || string(mustJSON(after)) != string(mustJSON(before)) || readCalls != 1 || truthCalls != tc.wantTruthReads ||
				kubectlReads != tc.wantKubectlReads || tagCalls != 0 || patchCalls != 0 {
				t.Fatalf("read changed owner state or called mutation: result=%#v readErr=%v listErr=%v read=%d truth=%d kubectl=%d tag=%d patch=%d", result, readErr, listErr, readCalls, truthCalls, kubectlReads, tagCalls, patchCalls)
			}
			if tc.wantSafeAbsent {
				if readErr != nil || result.State != "absent" {
					t.Fatalf("safe owner state did not produce recoverable absence: result=%#v err=%v", result, readErr)
				}
			} else if readErr == nil {
				t.Fatalf("uncertain owner state did not fail closed: result=%#v", result)
			}
			if _, err := fixture.store.MachineOwnership(context.Background(), fixture.computeID); !errors.Is(err, ErrMachineOwnershipNotFound) {
				t.Fatalf("read persisted ownership err=%v", err)
			}
		})
	}
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
