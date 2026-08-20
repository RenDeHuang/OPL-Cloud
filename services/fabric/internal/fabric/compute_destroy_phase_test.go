package fabric

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	computeDestroyReadbackPresent int32 = iota
	computeDestroyReadbackIdentityDrift
	computeDestroyReadbackUnknown
	computeDestroyReadbackAbsent
)

type phasedComputeDestroyProvider struct {
	testProvider
	destroyCalls  atomic.Int32
	readbackCalls atomic.Int32
	destroyResult ComputeAllocation
	destroyErr    error
	readback      ComputeAllocation
	readbackErr   error
}

func (p *phasedComputeDestroyProvider) DestroyComputeAllocation(_ context.Context, _ ComputeAllocation) (ComputeAllocation, error) {
	p.destroyCalls.Add(1)
	return cloneComputeAllocation(p.destroyResult), p.destroyErr
}

func (p *phasedComputeDestroyProvider) ReadComputeDestroyStatus(_ context.Context, _ ComputeAllocation) (ComputeAllocation, error) {
	p.readbackCalls.Add(1)
	return cloneComputeAllocation(p.readback), p.readbackErr
}

func (p *phasedComputeDestroyProvider) finalizeComputeDestroyAfterAbsence(_ context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	return cloneComputeAllocation(allocation), nil
}

func TestTencentComputeDispatchAuthorizationSurvivesRestartWithoutRedispatch(t *testing.T) {
	ctx := context.Background()
	for _, testCase := range []struct {
		name        string
		readback    func(ComputeAllocation) ComputeAllocation
		readbackErr error
		wantStatus  string
		wantError   string
	}{
		{
			name: "crash before send remains present",
			readback: func(resource ComputeAllocation) ComputeAllocation {
				resource.Status = "running"
				return resource
			},
			wantStatus: "failed", wantError: "compute_destroy_recovery_unconfirmed",
		},
		{
			name: "crash after send converges from absence",
			readback: func(resource ComputeAllocation) ComputeAllocation {
				return exactComputeDestroyAbsence(resource)
			},
			wantStatus: "succeeded",
		},
		{
			name: "unknown readback remains unconfirmed",
			readback: func(resource ComputeAllocation) ComputeAllocation {
				return resource
			},
			readbackErr: errors.New("Tencent compute readback unavailable"),
			wantStatus:  "failed", wantError: "compute_destroy_recovery_unconfirmed",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := NewMemoryOperationStore()
			resource := computeDestroyPhaseResource()
			resource.ID = "compute-" + stableSuffix(testCase.name)[:12]
			resource.ProviderResourceID = "ins-" + stableSuffix(testCase.name)[:12]
			resource.InstanceID = resource.ProviderResourceID
			resource.CVMInstanceID = resource.ProviderResourceID
			resource.MachineName = "machine-" + stableSuffix(testCase.name)[:12]
			resource.NodeName = "node-" + stableSuffix(testCase.name)[:12]
			resource.ProviderData["machineName"] = resource.MachineName
			resource.CostTags["opl_resource_id"] = resource.ID
			seedComputeDestroyPhaseResource(t, store, resource)

			dispatched := cloneComputeAllocation(resource)
			dispatched.Status = "destroying"
			dispatched.ProviderData[tencentComputeDestroyPhaseKey] = "dispatch_authorized_uncertain"
			appendStartedComputeDestroy(t, store, dispatched)

			provider := &phasedComputeDestroyProvider{
				destroyResult: exactComputeDestroyAbsence(resource),
				readback:      testCase.readback(dispatched),
				readbackErr:   testCase.readbackErr,
			}
			service := NewServiceWithOperationStore(provider, store)
			if _, err := service.DestroyComputeAllocation(ctx, resource.ID); err != nil {
				t.Fatal(err)
			}
			waitForComputeDestroyPhaseOperationCount(t, service, resource.ID, testCase.wantStatus, 1)
			if provider.destroyCalls.Load() != 0 || provider.readbackCalls.Load() != 1 {
				t.Fatalf("restart dispatches=%d readbacks=%d", provider.destroyCalls.Load(), provider.readbackCalls.Load())
			}
			latest, found, err := service.latestComputeDestroyOperation(ctx, resource.ID)
			if err != nil || !found || latest.Status != testCase.wantStatus || testCase.wantError != "" && !strings.Contains(latest.ErrorCode, testCase.wantError) {
				t.Fatalf("latest=%#v found=%v err=%v", latest, found, err)
			}
			current, ok := service.GetComputeAllocation(ctx, resource.ID)
			if !ok || testCase.wantStatus == "failed" && (current.Status != "destroying" || current.ProviderData[tencentComputeDestroyPhaseKey] != "dispatch_authorized_uncertain") ||
				testCase.wantStatus == "succeeded" && !validTencentComputeAbsenceEvidence(current) {
				t.Fatalf("current=%#v ok=%v", current, ok)
			}
		})
	}
}

func TestTencentComputeLegacyStartedWithoutPhaseUsesReadbackOnly(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	resource := computeDestroyPhaseResource()
	seedComputeDestroyPhaseResource(t, store, resource)
	started := cloneComputeAllocation(resource)
	started.Status = "destroying"
	delete(started.ProviderData, tencentComputeDestroyPhaseKey)
	appendStartedComputeDestroy(t, store, started)
	provider := &phasedComputeDestroyProvider{readback: started, destroyResult: exactComputeDestroyAbsence(resource)}
	service := NewServiceWithOperationStore(provider, store)

	if _, err := service.DestroyComputeAllocation(ctx, resource.ID); err != nil {
		t.Fatal(err)
	}
	waitForComputeDestroyPhaseOperationCount(t, service, resource.ID, "failed", 1)
	if provider.destroyCalls.Load() != 0 || provider.readbackCalls.Load() != 1 {
		t.Fatalf("legacy started dispatches=%d readbacks=%d", provider.destroyCalls.Load(), provider.readbackCalls.Load())
	}
}

func TestTencentComputeDestroyAttemptSurvivesRetryAndRestartWithoutSecondMutation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	provider := NewTencentProvider()
	var destroyCalls atomic.Int32
	var readbackCalls atomic.Int32
	var kubectlCalls atomic.Int32
	var cleanupFailures atomic.Int32
	var readbackState atomic.Int32
	var dispatchPersisted atomic.Bool
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		switch request.Action {
		case "destroy_compute_allocation":
			destroyCalls.Add(1)
			latest, found, latestErr := store.LatestResourceOperation(ctx, "compute_allocation", request.Allocation.ID)
			var persisted ComputeAllocation
			dispatchPersisted.Store(latestErr == nil && found && latest.Status == "started" && decodeOperationResource(latest, &persisted) &&
				persisted.ProviderData[tencentComputeDestroyPhaseKey] == tencentComputeDestroyPhaseDispatchAuthorized)
			present := true
			return provisionerResponse{
				OK: false, ErrorCode: "compute_machine_delete_unverified", Message: "DeleteClusterMachines response was lost while the machine remained visible", Retryable: true,
				NodePoolID: request.Pool.NodePoolID, InstanceID: request.Allocation.InstanceID, NodeName: request.Allocation.NodeName,
				MachinePresent: &present, CVMStatus: "RUNNING", TKEStatus: "RUNNING", MutationCount: 1,
				ProviderData: map[string]string{
					"machineType": "NativeCVM", "cvmApplicable": "true", "machinePresent": "true", "tkeStatus": "RUNNING", "cvmStatus": "RUNNING",
					"deleteMethod": "DeleteClusterMachines", "scaleDown": "true", "deleteMode": "terminate",
					"describeNodePoolRequestId": "req-delete-node-pool", "verifyMachineDeletedReqId": "req-delete-machine-present", "describeCvmRequestId": "req-delete-cvm-present",
				},
			}, nil
		case "read_compute_destroy_status":
			readbackCalls.Add(1)
			if request.Allocation.ID != "compute-durable-delete" || request.Allocation.MachineName != "machine-durable-delete" ||
				request.Allocation.InstanceID != "ins-durable-delete" || request.Allocation.NodeName != "node-durable-delete" || request.Allocation.PrivateIP != "10.0.0.18" {
				return provisionerResponse{}, errors.New("compute destroy recovery lost exact identity")
			}
			switch readbackState.Load() {
			case computeDestroyReadbackPresent:
				return computeDestroyPhasePresentReadback(request.Allocation.InstanceID), nil
			case computeDestroyReadbackIdentityDrift:
				response := computeDestroyPhasePresentReadback("ins-other")
				return response, nil
			case computeDestroyReadbackUnknown:
				return provisionerResponse{}, errors.New("Tencent compute readback unavailable")
			case computeDestroyReadbackAbsent:
				return provisionerResponse{
					OK: true, Status: "external_deleted", InstanceID: request.Allocation.InstanceID,
					CVMStatus: "NOT_FOUND", TKEStatus: "NOT_FOUND", ProviderRequestID: "req-sync-absent", MutationCount: 0,
					MachinePresent: func() *bool { value := false; return &value }(),
					ProviderData: map[string]string{
						"clusterId": "cls-alpha", "region": "ap-guangzhou", "nodePoolId": request.Pool.NodePoolID,
						"machineName": request.Allocation.MachineName, "nodeName": request.Allocation.NodeName, "privateIp": request.Allocation.PrivateIP,
						"machineType": "NativeCVM", "cvmApplicable": "true", "machinePresent": "false",
						"syncResult": "missing", "tkeStatus": "NOT_FOUND", "cvmStatus": "NOT_FOUND",
						"describeClusterMachinesReq": "req-sync-machine-absent", "describeCvmRequestId": "req-sync-cvm-absent",
					},
				}, nil
			default:
				return provisionerResponse{}, errors.New("unexpected compute destroy readback state")
			}
		default:
			return provisionerResponse{}, errors.New("unexpected provisioner action: " + request.Action)
		}
	}
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
		kubectlCalls.Add(1)
		if cleanupFailures.CompareAndSwap(1, 0) {
			return nil, errors.New("Kubernetes cleanup unavailable")
		}
		return nil, nil
	}
	resource := computeDestroyPhaseResource()
	seedComputeDestroyPhaseResource(t, store, resource)
	if _, _, err := store.ClaimMachine(ctx, MachineOwnership{
		ID: "owner-durable-delete", ResourceID: resource.ID, AccountID: resource.AccountID, WorkspaceID: resource.WorkspaceID, PackageID: resource.PackageID,
		NodePoolID: resource.NodePoolID, MachineID: resource.MachineName, InstanceID: resource.InstanceID, NodeName: resource.NodeName, Status: "active", ClaimedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	first := NewServiceWithOperationStore(provider, store)
	if _, err := first.DestroyComputeAllocation(ctx, resource.ID); err != nil {
		t.Fatal(err)
	}
	waitForComputeDestroyPhaseOperationCount(t, first, resource.ID, "failed", 1)
	failed, ok := first.GetComputeAllocation(ctx, resource.ID)
	if !ok || failed.ProviderData["computeDestroyPhase"] != "delete_attempted_uncertain" || destroyCalls.Load() != 1 || readbackCalls.Load() != 0 || !dispatchPersisted.Load() {
		t.Fatalf("first failed destroy=%#v ok=%v destroyCalls=%d readbackCalls=%d", failed, ok, destroyCalls.Load(), readbackCalls.Load())
	}

	restarted := NewServiceWithOperationStore(provider, store)
	restored, ok := restarted.GetComputeAllocation(ctx, resource.ID)
	if !ok || restored.ProviderData["computeDestroyPhase"] != "delete_attempted_uncertain" {
		t.Fatalf("restart lost destructive-attempt phase: restored=%#v ok=%v", restored, ok)
	}
	if _, err := restarted.DestroyComputeAllocation(ctx, resource.ID); err != nil {
		t.Fatal(err)
	}
	waitForComputeDestroyPhaseOperationCount(t, restarted, resource.ID, "failed", 2)
	if destroyCalls.Load() != 1 || readbackCalls.Load() != 1 {
		t.Fatalf("still-present recovery destroyCalls=%d readbackCalls=%d", destroyCalls.Load(), readbackCalls.Load())
	}

	readbackState.Store(computeDestroyReadbackIdentityDrift)
	if _, err := restarted.DestroyComputeAllocation(ctx, resource.ID); err != nil {
		t.Fatal(err)
	}
	waitForComputeDestroyPhaseOperationCount(t, restarted, resource.ID, "failed", 3)
	afterDrift, _ := restarted.GetComputeAllocation(ctx, resource.ID)
	if destroyCalls.Load() != 1 || readbackCalls.Load() != 2 || !sameComputeDestroyStableIdentity(resource, afterDrift) || afterDrift.ProviderData["computeDestroyPhase"] != "delete_attempted_uncertain" {
		t.Fatalf("identity drift changed authority: current=%#v destroyCalls=%d readbackCalls=%d", afterDrift, destroyCalls.Load(), readbackCalls.Load())
	}

	readbackState.Store(computeDestroyReadbackUnknown)
	if _, err := restarted.DestroyComputeAllocation(ctx, resource.ID); err != nil {
		t.Fatal(err)
	}
	waitForComputeDestroyPhaseOperationCount(t, restarted, resource.ID, "failed", 4)
	if destroyCalls.Load() != 1 || readbackCalls.Load() != 3 {
		t.Fatalf("unknown recovery destroyCalls=%d readbackCalls=%d", destroyCalls.Load(), readbackCalls.Load())
	}

	readbackState.Store(computeDestroyReadbackAbsent)
	cleanupFailures.Store(1)
	if _, err := restarted.DestroyComputeAllocation(ctx, resource.ID); err != nil {
		t.Fatal(err)
	}
	waitForComputeDestroyPhaseOperationCount(t, restarted, resource.ID, "failed", 5)
	confirmed, ok := restarted.GetComputeAllocation(ctx, resource.ID)
	ownership, ownershipErr := store.MachineOwnership(ctx, resource.ID)
	if !ok || confirmed.ProviderData["computeDestroyPhase"] != "cloud_absence_confirmed" || !validTencentComputeAbsenceEvidence(confirmed) ||
		destroyCalls.Load() != 1 || readbackCalls.Load() != 4 || kubectlCalls.Load() != 1 || ownershipErr != nil || ownership.Status != "active" {
		t.Fatalf("confirmed=%#v ok=%v destroyCalls=%d readbackCalls=%d kubectlCalls=%d ownership=%#v ownershipErr=%v", confirmed, ok, destroyCalls.Load(), readbackCalls.Load(), kubectlCalls.Load(), ownership, ownershipErr)
	}

	cleanupRestart := NewServiceWithOperationStore(provider, store)
	restoredConfirmed, ok := cleanupRestart.GetComputeAllocation(ctx, resource.ID)
	if !ok || restoredConfirmed.ProviderData["computeDestroyPhase"] != "cloud_absence_confirmed" || !validTencentComputeAbsenceEvidence(restoredConfirmed) {
		t.Fatalf("restart lost confirmed absence phase: restored=%#v ok=%v", restoredConfirmed, ok)
	}
	if _, err := cleanupRestart.DestroyComputeAllocation(ctx, resource.ID); err != nil {
		t.Fatal(err)
	}
	waitForComputeDestroyPhaseOperationCount(t, cleanupRestart, resource.ID, "succeeded", 1)
	final, ok := cleanupRestart.GetComputeAllocation(ctx, resource.ID)
	ownership, ownershipErr = store.MachineOwnership(ctx, resource.ID)
	if !ok || final.Status != "external_deleted" || !validTencentComputeAbsenceEvidence(final) || destroyCalls.Load() != 1 || readbackCalls.Load() != 4 || kubectlCalls.Load() != 2 ||
		ownershipErr != nil || ownership.Status != "released" || ownership.ReleasedAt == nil {
		t.Fatalf("final=%#v ok=%v destroyCalls=%d readbackCalls=%d kubectlCalls=%d ownership=%#v ownershipErr=%v", final, ok, destroyCalls.Load(), readbackCalls.Load(), kubectlCalls.Load(), ownership, ownershipErr)
	}
}

func TestTencentLegacyComputeDestroyRecoveryUsesMachineOnlyAbsence(t *testing.T) {
	ctx := context.Background()
	for _, machineType := range []string{"Native", "CXM"} {
		t.Run(machineType, func(t *testing.T) {
			store := NewMemoryOperationStore()
			resource := computeDestroyPhaseResource()
			resource.ID = "compute-legacy-" + strings.ToLower(machineType)
			resource.InstanceID = "legacy-instance-" + strings.ToLower(machineType)
			resource.CVMInstanceID = ""
			resource.CVMStatus = ""
			resource.ProviderResourceID = resource.InstanceID
			resource.ProviderData["machineType"] = machineType
			resource.ProviderData["cvmApplicable"] = "false"
			resource.CostTags["opl_resource_id"] = resource.ID
			seedComputeDestroyPhaseResource(t, store, resource)
			dispatched := cloneComputeAllocation(resource)
			dispatched.Status = "destroying"
			dispatched.ProviderData[tencentComputeDestroyPhaseKey] = tencentComputeDestroyPhaseDispatchAuthorized
			appendStartedComputeDestroy(t, store, dispatched)

			provider := NewTencentProvider()
			var readbackCalls atomic.Int32
			provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
				if request.Action != "read_compute_destroy_status" || request.Allocation.MachineType != machineType {
					return provisionerResponse{}, errors.New("unexpected provisioner request")
				}
				readbackCalls.Add(1)
				present := false
				return provisionerResponse{
					OK: true, Status: "external_deleted", InstanceID: request.Allocation.InstanceID, MachinePresent: &present,
					TKEStatus: "NOT_FOUND", ProviderRequestID: "req-legacy-machine-absent", MutationCount: 0,
					ProviderData: map[string]string{
						"clusterId": "cls-alpha", "region": "ap-guangzhou", "nodePoolId": request.Pool.NodePoolID,
						"machineName": request.Allocation.MachineName, "nodeName": request.Allocation.NodeName, "privateIp": request.Allocation.PrivateIP,
						"machineType": machineType, "cvmApplicable": "false", "machinePresent": "false", "tkeStatus": "NOT_FOUND",
						"syncResult": "missing", "describeClusterMachinesReq": "req-legacy-machine-absent",
					},
				}, nil
			}
			provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) { return nil, nil }
			service := NewServiceWithOperationStore(provider, store)
			if _, err := service.DestroyComputeAllocation(ctx, resource.ID); err != nil {
				t.Fatal(err)
			}
			waitForComputeDestroyPhaseOperationCount(t, service, resource.ID, "succeeded", 1)
			final, ok := service.GetComputeAllocation(ctx, resource.ID)
			_, hasCVMStatus := final.ProviderData["cvmStatus"]
			if !ok || readbackCalls.Load() != 1 || !validTencentComputeAbsenceEvidence(final) || final.CVMStatus != "" || hasCVMStatus {
				t.Fatalf("legacy final=%#v ok=%v readbacks=%d", final, ok, readbackCalls.Load())
			}
		})
	}
}

func TestTencentComputeConfirmedAbsenceSurvivesRuntimeCleanupRetryWithoutSecondMutation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	provider := NewTencentProvider()
	var destroyCalls atomic.Int32
	var kubectlCalls atomic.Int32
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "destroy_compute_allocation" {
			return provisionerResponse{}, errors.New("unexpected provisioner action: " + request.Action)
		}
		destroyCalls.Add(1)
		present := false
		return provisionerResponse{
			OK: true, Status: "external_deleted", NodePoolID: request.Pool.NodePoolID, InstanceID: request.Allocation.InstanceID,
			NodeName: request.Allocation.NodeName, MachinePresent: &present, CVMStatus: "NOT_FOUND", TKEStatus: "NOT_FOUND",
			ProviderRequestID: "req-delete-absent", MutationCount: 1,
			ProviderData: map[string]string{
				"machineType": "NativeCVM", "cvmApplicable": "true", "machinePresent": "false", "tkeStatus": "NOT_FOUND", "cvmStatus": "NOT_FOUND",
				"deleteMethod": "DeleteClusterMachines", "scaleDown": "true", "deleteMode": "terminate",
				"describeNodePoolRequestId": "req-delete-node-pool", "verifyMachineDeletedReqId": "req-delete-machine-absent", "describeCvmRequestId": "req-delete-cvm-absent",
			},
		}, nil
	}
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
		if kubectlCalls.Add(1) == 1 {
			return nil, errors.New("Kubernetes cleanup unavailable")
		}
		return nil, nil
	}
	resource := computeDestroyPhaseResource()
	resource.ID = "compute-confirmed-absence"
	resource.ProviderResourceID = "ins-confirmed-absence"
	resource.InstanceID = "ins-confirmed-absence"
	resource.CVMInstanceID = "ins-confirmed-absence"
	resource.MachineName = "machine-confirmed-absence"
	resource.NodeName = "node-confirmed-absence"
	resource.ServiceName = "opl-compute-confirmed-absence"
	resource.ProviderData["machineName"] = resource.MachineName
	resource.CostTags["opl_resource_id"] = resource.ID
	seedComputeDestroyPhaseResource(t, store, resource)

	first := NewServiceWithOperationStore(provider, store)
	if _, err := first.DestroyComputeAllocation(ctx, resource.ID); err != nil {
		t.Fatal(err)
	}
	waitForComputeDestroyPhaseOperationCount(t, first, resource.ID, "failed", 1)
	failed, ok := first.GetComputeAllocation(ctx, resource.ID)
	if !ok || failed.ProviderData["computeDestroyPhase"] != "cloud_absence_confirmed" || !validTencentComputeAbsenceEvidence(failed) || destroyCalls.Load() != 1 || kubectlCalls.Load() != 1 {
		t.Fatalf("failed=%#v ok=%v destroyCalls=%d kubectlCalls=%d", failed, ok, destroyCalls.Load(), kubectlCalls.Load())
	}

	restarted := NewServiceWithOperationStore(provider, store)
	if _, err := restarted.DestroyComputeAllocation(ctx, resource.ID); err != nil {
		t.Fatal(err)
	}
	waitForComputeDestroyPhaseOperationCount(t, restarted, resource.ID, "succeeded", 1)
	final, ok := restarted.GetComputeAllocation(ctx, resource.ID)
	if !ok || !validTencentComputeAbsenceEvidence(final) || destroyCalls.Load() != 1 || kubectlCalls.Load() != 2 {
		t.Fatalf("final=%#v ok=%v destroyCalls=%d kubectlCalls=%d", final, ok, destroyCalls.Load(), kubectlCalls.Load())
	}
}

func computeDestroyPhasePresentReadback(instanceID string) provisionerResponse {
	present := true
	return provisionerResponse{
		OK: true, Status: "present", InstanceID: instanceID, MachinePresent: &present, TKEStatus: "RUNNING", ProviderRequestID: "req-sync-present", MutationCount: 0,
		ProviderData: map[string]string{
			"clusterId": "cls-alpha", "region": "ap-guangzhou", "nodePoolId": "np-basic", "machineName": "machine-durable-delete",
			"nodeName": "node-durable-delete", "privateIp": "10.0.0.18", "machineType": "NativeCVM", "cvmApplicable": "true",
			"machinePresent": "true", "tkeStatus": "RUNNING", "describeClusterMachinesReq": "req-sync-machine-present", "syncResult": "found",
		},
	}
}

func computeDestroyPhaseResource() ComputeAllocation {
	return ComputeAllocation{
		ID: "compute-durable-delete", OperationID: "op-create-durable-delete", AccountID: "acct-durable-delete", WorkspaceID: "ws-durable-delete", PackageID: "basic",
		Status: "running", Provider: "tencent-tke", ProviderResourceID: "ins-durable-delete", ProviderRequestID: "req-create-durable-delete",
		PoolID: "pool-basic-2c4g", NodePoolID: "np-basic", InstanceID: "ins-durable-delete", CVMInstanceID: "ins-durable-delete",
		MachineName: "machine-durable-delete", NodeName: "node-durable-delete", PrivateIP: "10.0.0.18", InstanceType: "SA5.MEDIUM4", Zone: "ap-guangzhou-3",
		CVMStatus: "RUNNING", ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-09-20T00:00:00Z", ServiceName: "opl-compute-durable-delete",
		NodeSelector: map[string]any{"kubernetes.io/hostname": "node-durable-delete"},
		ProviderData: map[string]string{
			"clusterId": "cls-alpha", "region": "ap-guangzhou", "machineName": "machine-durable-delete", "instanceType": "SA5.MEDIUM4", "cpu": "2", "memoryGb": "4", "zone": "ap-guangzhou-3",
			"machineType": "NativeCVM", "cvmApplicable": "true",
		},
		CostTags: map[string]string{
			"opl_account_id": "acct-durable-delete", "opl_workspace_id": "ws-durable-delete", "opl_resource_id": "compute-durable-delete", "opl_operation_id": "op-create-durable-delete",
		},
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func exactComputeDestroyAbsence(resource ComputeAllocation) ComputeAllocation {
	resource = cloneComputeAllocation(resource)
	resource.Status = "external_deleted"
	resource.ProviderRequestID = "req-compute-destroy-absence"
	resource.CVMStatus = "NOT_FOUND"
	resource.ProviderData[tencentComputeDestroyPhaseKey] = tencentComputeDestroyPhaseAbsent
	resource.ProviderData["machineType"] = "NativeCVM"
	resource.ProviderData["cvmApplicable"] = "true"
	resource.ProviderData["machinePresent"] = "false"
	resource.ProviderData["tkeStatus"] = "NOT_FOUND"
	resource.ProviderData["cvmStatus"] = "NOT_FOUND"
	resource.ProviderData["describeClusterMachinesReq"] = "req-describe-machine-absent"
	resource.ProviderData["verifyMachineDeletedReqId"] = "req-describe-machine-absent"
	resource.ProviderData["describeCvmRequestId"] = "req-describe-cvm-absent"
	return resource
}

func appendStartedComputeDestroy(t *testing.T, store OperationStore, resource ComputeAllocation) {
	t.Helper()
	now := time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC)
	operation := newOperation("destroy_compute_allocation", "compute_allocation", resource.ID, resource.AccountID, resource.WorkspaceID, "", hashInput(map[string]string{"id": resource.ID}), now)
	operation.ID, operation.Status, operation.CreatedAt = "fop-started-"+stableSuffix(resource.ID)[:12], "started", now
	fillOperationResource(&operation, resource)
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
}

func seedComputeDestroyPhaseResource(t *testing.T, store OperationStore, resource ComputeAllocation) {
	t.Helper()
	now := resource.CreatedAt
	operation := newOperation("create_compute_allocation", "compute_allocation", resource.ID, resource.AccountID, resource.WorkspaceID, "seed-"+resource.ID, hashInput(resource), now)
	operation.ID, operation.Status, operation.CreatedAt, operation.FinishedAt = "fop-create-durable-delete", "succeeded", now, now
	fillOperationResource(&operation, resource)
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
}

func waitForComputeDestroyPhaseOperationCount(t *testing.T, service *Service, resourceID, status string, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		operations, err := service.ListOperations(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		matches := 0
		for _, operation := range operations {
			if operation.Action == "destroy_compute_allocation" && operation.ResourceKind == "compute_allocation" && operation.ResourceID == resourceID && operation.Status == status {
				matches++
			}
		}
		if matches >= count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("missing %d %s compute destroy operations for %s", count, status, resourceID)
}
