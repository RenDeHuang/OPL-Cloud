package fabric

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"testing"
	"time"
)

type fakeComputeClaimRecoveryProvider struct {
	testProvider
	proof        ComputeClaimProviderProof
	proofErr     error
	claim        ComputeClaimProviderClaim
	claimErr     error
	proofCalls   int
	claimCalls   int
	tagCalls     int
	scaleCalls   int
	storageCalls int
}

func (p *fakeComputeClaimRecoveryProvider) ProveComputeClaimRecovery(_ context.Context, allocation ComputeAllocation, plan ComputeAllocationPreparation, ownership MachineOwnership) (ComputeClaimProviderProof, error) {
	p.proofCalls++
	return p.proof, p.proofErr
}

func (p *fakeComputeClaimRecoveryProvider) ClaimComputeRecovery(_ context.Context, allocation ComputeAllocation, plan ComputeAllocationPreparation, ownership MachineOwnership) (ComputeClaimProviderClaim, error) {
	p.claimCalls++
	return p.claim, p.claimErr
}

func (p *fakeComputeClaimRecoveryProvider) TagComputeMachine(_ context.Context, _ ProviderMachine, _ MachineOwnership) error {
	p.tagCalls++
	return nil
}

func (p *fakeComputeClaimRecoveryProvider) CreateStorageVolume(_ context.Context, input StorageVolumeInput) (StorageVolume, error) {
	p.storageCalls++
	return StorageVolume{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", Provider: "tencent-tke", ProviderResourceID: "disk-fixture", ProviderRequestID: "storage-fixture", Zone: input.Zone, SizeGB: input.SizeGB}, nil
}

func seedComputeClaimRecovery(t *testing.T, packageID string) (*Service, *MemoryOperationStore, *fakeComputeClaimRecoveryProvider, ComputeClaimRecoveryInput) {
	return seedComputeClaimRecoveryWithPeriod(t, packageID, "1")
}

func seedComputeClaimRecoveryWithPeriod(t *testing.T, packageID, periodMonths string) (*Service, *MemoryOperationStore, *fakeComputeClaimRecoveryProvider, ComputeClaimRecoveryInput) {
	t.Helper()
	instanceType := "SA5.MEDIUM4"
	poolID := "pool-basic-2c4g"
	nodePoolID := "np-workspace-basic"
	if packageID == "pro" {
		instanceType = "SA5.2XLARGE16"
		poolID = "pool-pro-8c16g"
		nodePoolID = "np-workspace-pro"
	}
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	input := ComputeClaimRecoveryInput{
		LaunchOperationID:   "workspace-launch-fixture",
		AccountID:           "acct-fixture",
		WorkspaceID:         "ws-fixture",
		ComputeAllocationID: "ca_fixture",
		StorageVolumeID:     "vol_fixture",
		PackageID:           packageID,
		PoolID:              poolID,
		NodePoolID:          nodePoolID,
	}
	plan := ComputeAllocationPreparation{
		PoolID: poolID, PackageID: packageID, NodePoolID: nodePoolID, InstanceType: instanceType,
		MaxReplicas: 20, BaselineReplicas: 1, TargetReplicas: 2, BeforeMachineNames: []string{"machine-before"},
	}
	providerData := map[string]string{"instanceType": instanceType, "zone": "ap-guangzhou-3", "chargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-28T00:00:00Z", "machineName": "machine-after"}
	if periodMonths != "" {
		providerData["periodMonths"] = periodMonths
	}
	allocation := ComputeAllocation{
		ID: input.ComputeAllocationID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: packageID,
		Status: "quarantined", Provider: "tencent-tke", ProviderResourceID: "ins-fixture", PoolID: poolID, NodePoolID: nodePoolID,
		MachineName: "machine-after", InstanceID: "ins-fixture", CVMInstanceID: "ins-fixture", NodeName: "10.0.0.18", PrivateIP: "10.0.0.18",
		InstanceType: instanceType, Zone: "ap-guangzhou-3", ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-28T00:00:00Z",
		ProviderData: providerData,
		CreatedAt:    now,
	}
	ownership := MachineOwnership{
		ID: "owner-fixture", ResourceID: allocation.ID, AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID,
		PackageID: packageID, NodePoolID: nodePoolID, MachineID: allocation.MachineName, InstanceID: allocation.InstanceID,
		NodeName: allocation.NodeName, Status: "quarantined", ClaimedAt: now,
	}
	operation := newOperation("create_compute_allocation", "compute_allocation", allocation.ID, allocation.AccountID, allocation.WorkspaceID, input.LaunchOperationID+":compute", hashInput(ComputeAllocationInput{
		ID: allocation.ID, AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID, PackageID: packageID, NodePoolID: nodePoolID, IdempotencyKey: input.LaunchOperationID + ":compute",
	}), now)
	operation.ID = "fop-compute-fixture"
	operation.Status = "failed"
	operation.CreatedAt = now
	operation.FinishedAt = now.Add(time.Minute)
	operation.RedactedProviderPayload = computeAllocationOperationPayload(allocation, plan)

	store := NewMemoryOperationStore()
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimMachine(context.Background(), ownership); err != nil || !claimed {
		t.Fatalf("seed ownership: claimed=%v err=%v", claimed, err)
	}
	provider := &fakeComputeClaimRecoveryProvider{proof: ComputeClaimProviderProof{
		Status: "proven", NodeOwnershipState: "unallocated", CVMOwnershipState: "recoverable", MachineName: allocation.MachineName, NodeName: allocation.NodeName,
		CVMInstanceID: allocation.InstanceID, PrivateIP: allocation.PrivateIP, InstanceType: instanceType, Zone: allocation.Zone,
		ChargeType: "PREPAID", PeriodMonths: 1, RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: allocation.Deadline,
	}}
	provider.claim = ComputeClaimProviderClaim{Proof: provider.proof}
	provider.claim.Proof.NodeOwnershipState = "target_owned"
	provider.claim.Proof.CVMOwnershipState = "target_owned"
	provider.claim.TencentMutationCount = 1
	provider.claim.KubernetesMutationCount = 1
	service := NewServiceWithOperationStore(provider, store)
	service.computes[allocation.ID] = allocation
	return service, store, provider, input
}

func TestComputeClaimRecoveryProofAllowsHistoricalMissingPeriodWhenProviderProvesOneMonth(t *testing.T) {
	service, _, provider, input := seedComputeClaimRecoveryWithPeriod(t, "basic", "")

	proof, err := service.ComputeClaimRecoveryProof(context.Background(), input)

	if err != nil || !proof.Eligible || proof.Reason != "none" || proof.PeriodMonths != 1 || provider.proofCalls != 1 {
		t.Fatalf("proof=%#v err=%v providerProofCalls=%d", proof, err, provider.proofCalls)
	}
	if proof.Sub2APIMutationCount != 0 || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 {
		t.Fatalf("historical proof mutation counts=%#v", proof)
	}
}

func TestComputeClaimRecoveryProofRejectsHistoricalMissingPeriodWhenProviderDoesNotProveOneMonth(t *testing.T) {
	service, _, provider, input := seedComputeClaimRecoveryWithPeriod(t, "basic", "")
	provider.proof.PeriodMonths = 2

	proof, err := service.ComputeClaimRecoveryProof(context.Background(), input)

	if err == nil || proof.Eligible || proof.Reason != "identity_mismatch" || provider.proofCalls != 1 {
		t.Fatalf("proof=%#v err=%v providerProofCalls=%d", proof, err, provider.proofCalls)
	}
	if proof.Sub2APIMutationCount != 0 || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 {
		t.Fatalf("non-monthly provider proof mutation counts=%#v", proof)
	}
}

func TestComputeClaimRecoveryProofRejectsPersistedNonMonthlyPeriodBeforeProvider(t *testing.T) {
	service, _, provider, input := seedComputeClaimRecoveryWithPeriod(t, "basic", "2")

	proof, err := service.ComputeClaimRecoveryProof(context.Background(), input)

	if err == nil || proof.Eligible || proof.Reason != "local_identity" || provider.proofCalls != 0 {
		t.Fatalf("proof=%#v err=%v providerProofCalls=%d", proof, err, provider.proofCalls)
	}
	if proof.Sub2APIMutationCount != 0 || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 {
		t.Fatalf("persisted non-monthly mutation counts=%#v", proof)
	}
}

func TestComputeClaimRecoveryProofSupportsBasicAndProWithoutMutation(t *testing.T) {
	for _, packageID := range []string{"basic", "pro"} {
		t.Run(packageID, func(t *testing.T) {
			service, _, provider, input := seedComputeClaimRecovery(t, packageID)
			proof, err := service.ComputeClaimRecoveryProof(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if !proof.Eligible || proof.Reason != "none" || proof.StorageState != "storage_not_started" || proof.PackageID != packageID ||
				proof.LaunchOperationID != input.LaunchOperationID || proof.ComputeAllocationID != input.ComputeAllocationID ||
				proof.MachineName != "machine-after" || proof.CVMInstanceID != "ins-fixture" || proof.NodeName != "10.0.0.18" ||
				proof.NodeOwnershipState != "unallocated" || proof.CVMOwnershipState != "recoverable" ||
				proof.Sub2APIMutationCount != 0 || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 {
				t.Fatalf("proof=%#v", proof)
			}
			if provider.proofCalls != 1 || provider.claimCalls != 0 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
				t.Fatalf("provider calls=%#v", provider)
			}
		})
	}
}

func TestComputeClaimRecoveryProofRejectsStorageOperationBeforeProviderRead(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	now := time.Now().UTC()
	storage := newOperation("create_storage_volume", "storage_volume", input.StorageVolumeID, input.AccountID, input.WorkspaceID, input.LaunchOperationID+":storage", "hash", now)
	storage.ID, storage.Status, storage.CreatedAt = "fop-storage-fixture", "started", now
	fillOperationResource(&storage, StorageVolume{ID: input.StorageVolumeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID})
	if err := store.Append(context.Background(), storage); err != nil {
		t.Fatal(err)
	}

	proof, err := service.ComputeClaimRecoveryProof(context.Background(), input)
	if err == nil || proof.Eligible || proof.Reason != "storage_already_started" || provider.proofCalls != 0 || provider.claimCalls != 0 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("proof=%#v err=%v provider=%#v", proof, err, provider)
	}
}

func TestComputeClaimRecoveryProofRejectsConflictingStorageOperationIdentityBeforeProviderRead(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	now := time.Now().UTC()
	storage := newOperation("create_storage_volume", "storage_volume", "vol_conflicting", "acct-conflicting", "ws-conflicting", input.LaunchOperationID+":storage", "hash", now)
	storage.ID, storage.Status, storage.CreatedAt = "fop-storage-conflicting", "started", now
	fillOperationResource(&storage, StorageVolume{ID: "vol_conflicting", AccountID: "acct-conflicting", WorkspaceID: "ws-conflicting"})
	if err := store.Append(context.Background(), storage); err != nil {
		t.Fatal(err)
	}

	proof, err := service.ComputeClaimRecoveryProof(context.Background(), input)
	if err == nil || proof.Eligible || proof.Reason != "storage_already_started" || provider.proofCalls != 0 || provider.claimCalls != 0 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("proof=%#v err=%v provider=%#v", proof, err, provider)
	}
}

func TestComputeClaimRecoveryProofRejectsWorkspaceStorageOperationWithDifferentIdentityBeforeProviderRead(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	now := time.Now().UTC()
	storage := newOperation("create_storage_volume", "storage_volume", "vol_other", input.AccountID, input.WorkspaceID, "other-storage-key", "hash", now)
	storage.ID, storage.Status, storage.CreatedAt = "fop-storage-other", "started", now
	fillOperationResource(&storage, StorageVolume{ID: "vol_other", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID})
	if err := store.Append(context.Background(), storage); err != nil {
		t.Fatal(err)
	}

	proof, err := service.ComputeClaimRecoveryProof(context.Background(), input)
	if err == nil || proof.Eligible || proof.Reason != "storage_already_started" || provider.proofCalls != 0 || provider.claimCalls != 0 {
		t.Fatalf("proof=%#v err=%v provider=%#v", proof, err, provider)
	}
}

func TestComputeClaimRecoveryProofRejectsConflictingLaunchComputeOperationBeforeProviderRead(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	now := time.Now().UTC()
	conflict := newOperation("create_compute_allocation", "compute_allocation", "ca-other", input.AccountID, input.WorkspaceID, input.LaunchOperationID+":compute", "other-hash", now)
	conflict.ID, conflict.Status, conflict.CreatedAt = "fop-compute-other", "failed", now
	fillOperationResource(&conflict, ComputeAllocation{ID: "ca-other", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID})
	if err := store.Append(context.Background(), conflict); err != nil {
		t.Fatal(err)
	}

	proof, err := service.ComputeClaimRecoveryProof(context.Background(), input)
	if err == nil || proof.Eligible || proof.Reason != "local_identity" || provider.proofCalls != 0 || provider.claimCalls != 0 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("proof=%#v err=%v provider=%#v", proof, err, provider)
	}
}

func TestComputeClaimRecoveryProofRejectsWorkspaceComputeOperationWithDifferentIdentityBeforeProviderRead(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	now := time.Now().UTC()
	conflict := newOperation("create_compute_allocation", "compute_allocation", "ca-other", input.AccountID, input.WorkspaceID, "other-compute-key", "other-hash", now)
	conflict.ID, conflict.Status, conflict.CreatedAt = "fop-compute-other-workspace", "failed", now
	fillOperationResource(&conflict, ComputeAllocation{ID: "ca-other", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID})
	if err := store.Append(context.Background(), conflict); err != nil {
		t.Fatal(err)
	}

	proof, err := service.ComputeClaimRecoveryProof(context.Background(), input)
	if err == nil || proof.Eligible || proof.Reason != "local_identity" || provider.proofCalls != 0 || provider.claimCalls != 0 {
		t.Fatalf("proof=%#v err=%v provider=%#v", proof, err, provider)
	}
}

func TestComputeClaimRecoveryProofRejectsNonMonthlyPersistedBillingBeforeProviderRead(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "pro")
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("operations=%#v err=%v", operations, err)
	}
	var allocation ComputeAllocation
	plan, ok := decodeComputeAllocationPlan(operations[0])
	if !ok || !decodeOperationResource(operations[0], &allocation) {
		t.Fatalf("operation payload=%#v", operations[0])
	}
	allocation.ProviderData["periodMonths"] = "2"
	operations[0].RedactedProviderPayload = computeAllocationOperationPayload(allocation, plan)
	store.mu.Lock()
	store.operation[0] = operations[0]
	store.mu.Unlock()

	proof, err := service.ComputeClaimRecoveryProof(context.Background(), input)
	if err == nil || proof.Eligible || proof.Reason != "local_identity" || provider.proofCalls != 0 || provider.claimCalls != 0 {
		t.Fatalf("proof=%#v err=%v provider=%#v", proof, err, provider)
	}
}

func TestClaimComputeRecoveryConvergesOriginalOperationAndReplaysIdempotently(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute-claim",
	}

	result, err := service.ClaimComputeRecovery(context.Background(), claimInput)
	if err != nil {
		t.Fatal(err)
	}
	ownership, ownershipErr := store.MachineOwnership(context.Background(), input.ComputeAllocationID)
	operations, operationsErr := store.List(context.Background())
	if ownershipErr != nil || operationsErr != nil || ownership.Status != "active" || !result.Eligible || result.StorageState != "storage_not_started" ||
		result.NodeOwnershipState != "target_owned" || result.CVMOwnershipState != "target_owned" || result.TencentMutationCount != 1 || result.KubernetesMutationCount != 1 || provider.claimCalls != 1 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("result=%#v err=%v ownership=%#v provider=%#v", result, err, ownership, provider)
	}
	assertRecoveredComputeOperation(t, operations, input, "succeeded")

	provider.proof.NodeOwnershipState = "target_owned"
	provider.proof.CVMOwnershipState = "target_owned"
	replayed, err := service.ClaimComputeRecovery(context.Background(), claimInput)
	if err != nil || replayed.TencentMutationCount != 0 || replayed.KubernetesMutationCount != 0 || provider.proofCalls != 2 || provider.claimCalls != 1 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("replay=%#v err=%v provider=%#v", replayed, err, provider)
	}
}

func TestClaimComputeRecoveryRestartAfterActiveOwnershipSkipsProviderMutation(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "pro")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute-claim",
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("seed operations=%#v err=%v", operations, err)
	}
	pending := operations[0]
	pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
	pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, newComputeClaimRecoveryBinding(claimInput))
	if err := store.SaveComputeClaimRecovery(context.Background(), operations[0], pending); err != nil {
		t.Fatal(err)
	}
	ownership, err := store.MachineOwnership(context.Background(), input.ComputeAllocationID)
	if err != nil {
		t.Fatal(err)
	}
	ownership.Status = "active"
	if err := store.ActivateComputeClaimRecoveryOwnership(context.Background(), ownership); err != nil {
		t.Fatal(err)
	}
	provider.proof.NodeOwnershipState = "target_owned"
	provider.proof.CVMOwnershipState = "target_owned"
	restarted := NewServiceWithOperationStore(provider, store)

	result, err := restarted.ClaimComputeRecovery(context.Background(), claimInput)
	operations, operationsErr := store.List(context.Background())
	if err != nil || operationsErr != nil || !result.Eligible || result.TencentMutationCount != 0 || result.KubernetesMutationCount != 0 ||
		provider.proofCalls != 1 || provider.claimCalls != 0 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("restart result=%#v err=%v operationsErr=%v provider=%#v", result, err, operationsErr, provider)
	}
	assertRecoveredComputeOperation(t, operations, input, "succeeded")
}

func TestClaimComputeRecoveryRestartAfterTargetOwnedReadbackConvergesLocalStateOnly(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute-claim",
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("seed operations=%#v err=%v", operations, err)
	}
	pending := operations[0]
	pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
	pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, newComputeClaimRecoveryBinding(claimInput))
	if err := store.SaveComputeClaimRecovery(context.Background(), operations[0], pending); err != nil {
		t.Fatal(err)
	}
	provider.proof.NodeOwnershipState = "target_owned"
	provider.proof.CVMOwnershipState = "target_owned"
	restarted := NewServiceWithOperationStore(provider, store)

	result, err := restarted.ClaimComputeRecovery(context.Background(), claimInput)
	ownership, ownershipErr := store.MachineOwnership(context.Background(), input.ComputeAllocationID)
	operations, operationsErr := store.List(context.Background())
	if err != nil || ownershipErr != nil || operationsErr != nil || !result.Eligible || ownership.Status != "active" ||
		result.TencentMutationCount != 0 || result.KubernetesMutationCount != 0 || provider.proofCalls != 1 || provider.claimCalls != 0 ||
		provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("restart result=%#v err=%v ownership=%#v ownershipErr=%v operationsErr=%v provider=%#v", result, err, ownership, ownershipErr, operationsErr, provider)
	}
	assertRecoveredComputeOperation(t, operations, input, "succeeded")
}

func TestClaimComputeRecoveryRejectsMalformedPersistedBindingWithoutMutationOrOverwrite(t *testing.T) {
	tests := map[string]any{
		"not_an_object": "malformed",
		"missing_request_hash": map[string]any{
			"launchOperationId": "workspace-launch-fixture",
			"idempotencyKey":    "workspace-launch-fixture:compute-claim",
			"targetHash":        "persisted-target-hash",
		},
	}
	for name, malformedBinding := range tests {
		t.Run(name, func(t *testing.T) {
			service, store, provider, input := seedComputeClaimRecovery(t, "pro")
			operations, err := store.List(context.Background())
			if err != nil || len(operations) != 1 {
				t.Fatalf("seed operations=%#v err=%v", operations, err)
			}
			pending := operations[0]
			pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
			pending.RedactedProviderPayload = maps.Clone(pending.RedactedProviderPayload)
			pending.RedactedProviderPayload["computeClaimRecovery"] = malformedBinding
			store.mu.Lock()
			store.operation[0] = pending
			store.mu.Unlock()

			result, err := service.ClaimComputeRecovery(context.Background(), ComputeClaimRecoveryClaimInput{
				ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
				PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
				IdempotencyKey: input.LaunchOperationID + ":compute-claim",
			})
			ownership, ownershipErr := store.MachineOwnership(context.Background(), input.ComputeAllocationID)
			stored, listErr := store.List(context.Background())
			if !errors.Is(err, ErrComputeClaimRecoveryIdempotencyConflict) || result.Eligible || result.Reason != "identity_mismatch" ||
				ownershipErr != nil || ownership.Status != "quarantined" || listErr != nil || len(stored) != 1 ||
				!reflect.DeepEqual(stored[0].RedactedProviderPayload["computeClaimRecovery"], malformedBinding) ||
				provider.claimCalls != 0 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
				t.Fatalf("result=%#v err=%v ownership=%#v ownershipErr=%v stored=%#v listErr=%v provider=%#v", result, err, ownership, ownershipErr, stored, listErr, provider)
			}
		})
	}
}

func TestMemoryComputeClaimRecoveryCASRejectsPersistedBindingDrift(t *testing.T) {
	drifts := map[string]func(*computeClaimRecoveryBinding){
		"launch": func(binding *computeClaimRecoveryBinding) { binding.LaunchOperationID = "workspace-launch-other" },
		"idempotency": func(binding *computeClaimRecoveryBinding) {
			binding.IdempotencyKey = "workspace-launch-fixture:compute-claim-other"
		},
		"target":  func(binding *computeClaimRecoveryBinding) { binding.TargetHash = "different-target-hash" },
		"request": func(binding *computeClaimRecoveryBinding) { binding.RequestHash = "different-request-hash" },
	}
	for name, drift := range drifts {
		t.Run(name, func(t *testing.T) {
			_, store, provider, input := seedComputeClaimRecovery(t, "basic")
			claimInput := ComputeClaimRecoveryClaimInput{
				ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
				PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
				IdempotencyKey: input.LaunchOperationID + ":compute-claim",
			}
			operations, err := store.List(context.Background())
			if err != nil || len(operations) != 1 {
				t.Fatalf("seed operations=%#v err=%v", operations, err)
			}
			pending := operations[0]
			pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
			binding := newComputeClaimRecoveryBinding(claimInput)
			pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, binding)
			if err := store.SaveComputeClaimRecovery(context.Background(), operations[0], pending); err != nil {
				t.Fatal(err)
			}
			drift(&binding)
			drifted := pending
			drifted.Status, drifted.FinishedAt = "succeeded", time.Now().UTC()
			drifted.RedactedProviderPayload = withComputeClaimRecoveryBinding(drifted.RedactedProviderPayload, binding)

			if err := store.SaveComputeClaimRecovery(context.Background(), pending, drifted); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
				t.Fatalf("binding drift error=%v, want ErrRuntimeOperationNotCurrent", err)
			}
			stored, err := store.List(context.Background())
			if err != nil || len(stored) != 1 || stored[0].Status != "claim_pending" || !reflect.DeepEqual(stored[0].RedactedProviderPayload, pending.RedactedProviderPayload) {
				t.Fatalf("binding drift changed operation: stored=%#v err=%v", stored, err)
			}
		})
	}
}

func TestClaimComputeRecoveryRequiresStrictTargetOwnedReadback(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "pro")
	provider.claim.Proof.NodeOwnershipState = "unallocated"
	provider.claimErr = errors.New("strict readback failed")

	result, err := service.ClaimComputeRecovery(context.Background(), ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute-claim",
	})
	ownership, _ := store.MachineOwnership(context.Background(), input.ComputeAllocationID)
	operations, _ := store.List(context.Background())
	if err == nil || result.Eligible || ownership.Status != "quarantined" || provider.claimCalls != 1 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("result=%#v err=%v ownership=%#v provider=%#v", result, err, ownership, provider)
	}
	assertRecoveredComputeOperation(t, operations, input, "claim_pending")
}

func TestClaimComputeRecoveryRejectsPrivateIPTargetDriftBeforeMutation(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	result, err := service.ClaimComputeRecovery(context.Background(), ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: "10.0.0.99", InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute-claim",
	})
	ownership, _ := store.MachineOwnership(context.Background(), input.ComputeAllocationID)
	operations, _ := store.List(context.Background())
	if err == nil || result.Eligible || result.Reason != "identity_mismatch" || ownership.Status != "quarantined" ||
		provider.proofCalls != 1 || provider.claimCalls != 0 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("result=%#v err=%v ownership=%#v provider=%#v", result, err, ownership, provider)
	}
	assertRecoveredComputeOperation(t, operations, input, "failed")
}

func TestClaimComputeRecoveryFailurePreservesAttemptedMutationCounts(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	provider.claim.Proof.Reason = "iam_rbac"
	provider.claim.Proof.NodeOwnershipState = "unallocated"
	provider.claim.TencentMutationCount = 4
	provider.claim.KubernetesMutationCount = 1
	provider.claimErr = errors.New("node readback forbidden")

	result, err := service.ClaimComputeRecovery(context.Background(), ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute-claim",
	})
	ownership, _ := store.MachineOwnership(context.Background(), input.ComputeAllocationID)
	operations, _ := store.List(context.Background())
	if err == nil || result.Eligible || result.Reason != "iam_rbac" || result.TencentMutationCount != 4 || result.KubernetesMutationCount != 1 ||
		ownership.Status != "quarantined" || provider.claimCalls != 1 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("result=%#v err=%v ownership=%#v provider=%#v", result, err, ownership, provider)
	}
	assertRecoveredComputeOperation(t, operations, input, "claim_pending")
}

func TestClaimComputeRecoveryRejectsStartedStorageBeforeMutation(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	now := time.Now().UTC()
	storage := newOperation("create_storage_volume", "storage_volume", input.StorageVolumeID, input.AccountID, input.WorkspaceID, input.LaunchOperationID+":storage", "hash", now)
	storage.ID, storage.Status, storage.CreatedAt = "fop-storage-fixture", "started", now
	fillOperationResource(&storage, StorageVolume{ID: input.StorageVolumeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID})
	if err := store.Append(context.Background(), storage); err != nil {
		t.Fatal(err)
	}

	result, err := service.ClaimComputeRecovery(context.Background(), ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute-claim",
	})
	if err == nil || result.Reason != "storage_already_started" || provider.claimCalls != 0 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("result=%#v err=%v provider=%#v", result, err, provider)
	}
}

func TestSyncComputeAllocationDoesNotClaimQuarantinedOwnership(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")

	result, err := service.SyncComputeAllocation(context.Background(), input.ComputeAllocationID)
	ownership, _ := store.MachineOwnership(context.Background(), input.ComputeAllocationID)
	if err != nil || result.Status != "compute_claim_pending" || ownership.Status != "quarantined" || provider.tagCalls != 0 || provider.claimCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("result=%#v err=%v ownership=%#v provider=%#v", result, err, ownership, provider)
	}
}

func assertRecoveredComputeOperation(t *testing.T, operations []FabricOperation, input ComputeClaimRecoveryInput, status string) {
	t.Helper()
	matches := 0
	for _, operation := range operations {
		if operation.Action == "create_compute_allocation" && operation.ResourceID == input.ComputeAllocationID && operation.IdempotencyKey == input.LaunchOperationID+":compute" {
			matches++
			if operation.Status != status {
				t.Fatalf("compute operation status=%q want=%q operation=%#v", operation.Status, status, operation)
			}
		}
	}
	if matches != 1 {
		t.Fatalf("compute operation matches=%d operations=%#v", matches, operations)
	}
}
