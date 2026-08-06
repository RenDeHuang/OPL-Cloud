package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeComputeClaimRecoveryProvider struct {
	testProvider
	proof               ComputeClaimProviderProof
	proofErr            error
	claim               ComputeClaimProviderClaim
	claimErr            error
	storageDiscovery    StorageRecoveryDiscovery
	storageDiscoveryErr error
	proofCalls          int
	claimCalls          int
	nodeOnlyClaimCalls  int
	storageDiscoveries  []StorageVolumeInput
	claimHook           func()
	tagCalls            int
	scaleCalls          int
	storageCalls        int
}

type failOnceComputeClaimActivationStore struct {
	OperationStore
	fail bool
}

type failAfterComputeClaimNodeReservationStore struct {
	OperationStore
	failed bool
}

type failBeforeComputeClaimObservedSaveStore struct {
	OperationStore
	failed bool
}

type failAfterComputeClaimReconciliationCASStore struct {
	OperationStore
	failed bool
}

type failBeforeReconciledComputeClaimObservedSaveStore struct {
	OperationStore
	failed bool
}

func (s *failAfterComputeClaimReconciliationCASStore) SaveComputeClaimRecovery(ctx context.Context, expected, next FabricOperation) error {
	if err := s.OperationStore.SaveComputeClaimRecovery(ctx, expected, next); err != nil {
		return err
	}
	_, currentPresent, _ := decodeComputeClaimRecoveryReconciliation(expected)
	nextValue, nextPresent, nextValid := decodeComputeClaimRecoveryReconciliation(next)
	if !s.failed && !currentPresent && nextPresent && nextValid && nextValue.State == "verified" {
		s.failed = true
		return errors.New("simulated response loss after reconciliation CAS")
	}
	return nil
}

func (s *failBeforeReconciledComputeClaimObservedSaveStore) SaveComputeClaimRecovery(ctx context.Context, expected, next FabricOperation) error {
	currentValue, currentPresent, currentValid := decodeComputeClaimRecoveryReconciliation(expected)
	nextValue, nextPresent, nextValid := decodeComputeClaimRecoveryReconciliation(next)
	if !s.failed && currentPresent && currentValid && nextPresent && nextValid &&
		currentValue.State == "node_reserved" && (nextValue.State == "observed" || nextValue.State == "succeeded") {
		s.failed = true
		return errors.New("simulated response loss after reconciled Node patch")
	}
	return s.OperationStore.SaveComputeClaimRecovery(ctx, expected, next)
}

func (s *failAfterComputeClaimNodeReservationStore) SaveComputeClaimRecovery(ctx context.Context, expected, next FabricOperation) error {
	if err := s.OperationStore.SaveComputeClaimRecovery(ctx, expected, next); err != nil {
		return err
	}
	ledger, present, valid := decodeComputeClaimRecoveryMutation(next)
	if !s.failed && present && valid && ledger.State == "node_reserved" {
		s.failed = true
		return errors.New("simulated interruption after compute claim node reservation")
	}
	return nil
}

func (s *failBeforeComputeClaimObservedSaveStore) SaveComputeClaimRecovery(ctx context.Context, expected, next FabricOperation) error {
	currentLedger, currentPresent, currentValid := decodeComputeClaimRecoveryMutation(expected)
	nextLedger, nextPresent, nextValid := decodeComputeClaimRecoveryMutation(next)
	if !s.failed && currentPresent && currentValid && nextPresent && nextValid &&
		currentLedger.State == "node_reserved" && nextLedger.State == "observed" {
		s.failed = true
		return errors.New("simulated interruption before compute claim observed save")
	}
	return s.OperationStore.SaveComputeClaimRecovery(ctx, expected, next)
}

func (s *failOnceComputeClaimActivationStore) ActivateComputeClaimRecoveryOwnership(ctx context.Context, ownership MachineOwnership) error {
	if s.fail {
		s.fail = false
		return errors.New("activation unavailable")
	}
	return s.OperationStore.ActivateComputeClaimRecoveryOwnership(ctx, ownership)
}

func (p *fakeComputeClaimRecoveryProvider) ProveComputeClaimRecovery(_ context.Context, allocation ComputeAllocation, plan ComputeAllocationPreparation, ownership MachineOwnership) (ComputeClaimProviderProof, error) {
	p.proofCalls++
	return p.proof, p.proofErr
}

func (p *fakeComputeClaimRecoveryProvider) ClaimComputeRecovery(_ context.Context, allocation ComputeAllocation, plan ComputeAllocationPreparation, ownership MachineOwnership) (ComputeClaimProviderClaim, error) {
	p.claimCalls++
	if p.claimHook != nil {
		p.claimHook()
	}
	return p.claim, p.claimErr
}

func (p *fakeComputeClaimRecoveryProvider) ClaimComputeRecoveryNodeOnly(_ context.Context, allocation ComputeAllocation, plan ComputeAllocationPreparation, ownership MachineOwnership) (ComputeClaimProviderClaim, error) {
	p.nodeOnlyClaimCalls++
	if p.claimHook != nil {
		p.claimHook()
	}
	return p.claim, p.claimErr
}

func (p *fakeComputeClaimRecoveryProvider) DiscoverStorageRecovery(_ context.Context, input StorageVolumeInput) (StorageRecoveryDiscovery, error) {
	p.storageDiscoveries = append(p.storageDiscoveries, input)
	return p.storageDiscovery, p.storageDiscoveryErr
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
	provider.storageDiscovery = StorageRecoveryDiscovery{State: "storage_not_started", ProviderRequestID: "req-storage-discovery"}
	provider.claim = ComputeClaimProviderClaim{Proof: provider.proof}
	provider.claim.Proof.NodeOwnershipState = "target_owned"
	provider.claim.Proof.CVMOwnershipState = "target_owned"
	provider.claim.TencentMutationCount = 1
	provider.claim.KubernetesMutationCount = 1
	provider.claim.Evidence = &ComputeClaimEvidence{
		CVM:  ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
		Node: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
	}
	service := NewServiceWithOperationStore(provider, store)
	service.computes[allocation.ID] = allocation
	return service, store, provider, input
}

func seedRequestHashReconciliationCandidate(t *testing.T, store *MemoryOperationStore, provider *fakeComputeClaimRecoveryProvider, input ComputeClaimRecoveryInput) ComputeClaimRecoveryClaimInput {
	t.Helper()
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	binding := newComputeClaimRecoveryBinding(claimInput)
	binding.RequestHash = strings.Repeat("7", 64)
	ledger := computeClaimRecoveryMutationLedger{
		State: "observed", Reason: "provider_describe", TencentMutationCount: 1, FailureStage: "cvm_tag_readback", ProviderErrorClass: "provider_error",
		Evidence: ComputeClaimEvidence{CVM: ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"opl_account_id"}}},
	}
	store.mu.Lock()
	pending := store.operation[0]
	pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
	pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, binding)
	pending.RedactedProviderPayload = withComputeClaimRecoveryMutation(pending.RedactedProviderPayload, ledger)
	store.operation[0] = pending
	store.mu.Unlock()
	return claimInput
}

func seedNormalLaunchTerminalRequestHashReconciliationCandidate(t *testing.T, store *MemoryOperationStore, provider *fakeComputeClaimRecoveryProvider, input ComputeClaimRecoveryInput) ComputeClaimRecoveryClaimInput {
	t.Helper()
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	claimInput.StorageVolumeID = "vol_" + stableID("vol", input.AccountID, input.LaunchOperationID+":storage")[:18]
	binding := newComputeClaimRecoveryBinding(claimInput)
	binding.RequestHash = strings.Repeat("7", 64)

	store.mu.Lock()
	pending := store.operation[0]
	var allocation ComputeAllocation
	plan, planPresent := decodeComputeAllocationPlan(pending)
	if !decodeOperationResource(pending, &allocation) || !planPresent {
		store.mu.Unlock()
		t.Fatal("seed compute identity missing")
	}
	pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
	pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, binding)
	pending.RedactedProviderPayload = withNormalLaunchStageBudget(pending.RedactedProviderPayload, "compute_create", confirmedNormalLaunchMutationBudget())
	pending.RedactedProviderPayload = withNormalLaunchStageBudget(pending.RedactedProviderPayload, "compute_claim_cvm", reservedNormalLaunchMutationBudget())
	store.operation[0] = pending
	store.mu.Unlock()

	service := NewServiceWithOperationStore(provider, store)
	cause := errors.New("compute_claim_cvm_readback_mismatch")
	proof := provider.proof
	if err := terminalizeComputeClaimPending(context.Background(), service, pending, allocation, plan, "compute_claim_cvm", "mismatch", cause, &proof); !errors.Is(err, cause) {
		t.Fatalf("terminalize normal launch: %v", err)
	}
	configureNormalLaunchTerminalNodeOnlySuccess(provider)
	return claimInput
}

func configureNormalLaunchTerminalNodeOnlySuccess(provider *fakeComputeClaimRecoveryProvider) {
	provider.proof.CVMOwnershipState = "recoverable"
	provider.proof.NodeOwnershipState = "unallocated"
	provider.claim = ComputeClaimProviderClaim{
		Proof: ComputeClaimProviderProof{
			Status: "proven", Reason: "none", MachineName: provider.proof.MachineName, NodeName: provider.proof.NodeName,
			CVMInstanceID: provider.proof.CVMInstanceID, PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType,
			Zone: provider.proof.Zone, ChargeType: provider.proof.ChargeType, PeriodMonths: provider.proof.PeriodMonths,
			RenewFlag: provider.proof.RenewFlag, Deadline: provider.proof.Deadline, CVMOwnershipState: "recoverable", NodeOwnershipState: "target_owned",
		},
		KubernetesMutationCount: 1,
		Evidence:                &ComputeClaimEvidence{Node: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}},
	}
	provider.claimHook = func() { provider.proof.NodeOwnershipState = "target_owned" }
}

func configureRequestHashReconciliationNodeSuccess(provider *fakeComputeClaimRecoveryProvider) {
	provider.proof.CVMOwnershipState = "recoverable"
	provider.proof.NodeOwnershipState = "unallocated"
	provider.claim = ComputeClaimProviderClaim{
		Proof: ComputeClaimProviderProof{
			Status: "proven", Reason: "none", MachineName: provider.proof.MachineName, NodeName: provider.proof.NodeName,
			CVMInstanceID: provider.proof.CVMInstanceID, PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType,
			Zone: provider.proof.Zone, ChargeType: provider.proof.ChargeType, PeriodMonths: provider.proof.PeriodMonths,
			RenewFlag: provider.proof.RenewFlag, Deadline: provider.proof.Deadline, CVMOwnershipState: "recoverable", NodeOwnershipState: "target_owned",
		},
		KubernetesMutationCount: 1,
		Evidence:                &ComputeClaimEvidence{Node: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}},
	}
	provider.claimHook = func() { provider.proof.NodeOwnershipState = "target_owned" }
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

func TestComputeClaimRecoveryProofPreservesOnlyValidatedProviderIdentityFailure(t *testing.T) {
	service, _, provider, input := seedComputeClaimRecovery(t, "basic")
	provider.proof = ComputeClaimProviderProof{
		Reason: "identity_mismatch", FailureStage: "cvm_pre_read", ProviderErrorClass: "readback_mismatch",
		ProviderIdentityFailure: &ComputeClaimProviderIdentityFailure{
			Predicate:      "compute_claim.cvm_ownership.opl_account_id",
			ExpectedDigest: strings.Repeat("a", 64), ActualDigest: strings.Repeat("b", 64),
		},
	}
	provider.proofErr = errors.New("raw provider identity must not escape")

	proof, err := service.ComputeClaimRecoveryProof(context.Background(), input)

	if err == nil || proof.Eligible || proof.Reason != "identity_mismatch" || proof.FailureStage != "cvm_pre_read" ||
		proof.ProviderErrorClass != "readback_mismatch" || proof.ProviderIdentityFailure == nil ||
		proof.ProviderIdentityFailure.Predicate != "compute_claim.cvm_ownership.opl_account_id" ||
		proof.ProviderIdentityFailure.ExpectedDigest != strings.Repeat("a", 64) ||
		proof.ProviderIdentityFailure.ActualDigest != strings.Repeat("b", 64) ||
		proof.Sub2APIMutationCount != 0 || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 || provider.proofCalls != 1 {
		t.Fatalf("proof=%#v err=%v", proof, err)
	}
}

func TestComputeClaimRecoveryProofRejectsUnallowlistedProviderIdentityFailure(t *testing.T) {
	service, _, provider, input := seedComputeClaimRecovery(t, "basic")
	provider.proof = ComputeClaimProviderProof{
		Reason: "identity_mismatch", FailureStage: "cvm_pre_read", ProviderErrorClass: "readback_mismatch",
		ProviderIdentityFailure: &ComputeClaimProviderIdentityFailure{
			Predicate: "raw.provider.account", ExpectedDigest: strings.Repeat("a", 64), ActualDigest: strings.Repeat("b", 64),
		},
	}
	provider.proofErr = errors.New("raw provider identity must not escape")

	proof, err := service.ComputeClaimRecoveryProof(context.Background(), input)

	if err == nil || proof.Eligible || proof.Reason != "identity_mismatch" || proof.FailureStage != "" ||
		proof.ProviderErrorClass != "" || proof.ProviderIdentityFailure != nil || proof.Sub2APIMutationCount != 0 ||
		proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 || provider.proofCalls != 1 {
		t.Fatalf("proof=%#v err=%v", proof, err)
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
			if provider.proofCalls != 1 || len(provider.storageDiscoveries) != 1 || provider.claimCalls != 0 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
				t.Fatalf("provider calls=%#v", provider)
			}
			storage := provider.storageDiscoveries[0]
			wantSize := 10
			if packageID == "pro" {
				wantSize = 100
			}
			wantOperationID := newOperation("create_storage_volume", "storage_volume", input.StorageVolumeID, input.AccountID, input.WorkspaceID, input.LaunchOperationID+":storage", "", time.Time{}).OperationID
			if storage.ID != input.StorageVolumeID || storage.AccountID != input.AccountID || storage.WorkspaceID != input.WorkspaceID ||
				storage.ComputeID != input.ComputeAllocationID || storage.Zone != proof.Zone || storage.SizeGB != wantSize || storage.IdempotencyKey != input.LaunchOperationID+":storage" ||
				storage.OperationID != wantOperationID {
				t.Fatalf("storage discovery input=%#v wantOperationID=%q", storage, wantOperationID)
			}
		})
	}
}

func TestComputeClaimRecoveryProofBindsOneExactExistingCBS(t *testing.T) {
	service, _, provider, input := seedComputeClaimRecovery(t, "basic")
	provider.storageDiscovery = StorageRecoveryDiscovery{
		State: "storage_existing_exact", ProviderResourceID: "disk-existing-fixture", ProviderRequestID: "req-existing-cbs",
	}

	proof, err := service.ComputeClaimRecoveryProof(context.Background(), input)

	if err != nil || !proof.Eligible || proof.Reason != "none" || proof.StorageState != "storage_existing_exact" ||
		proof.StorageProviderResourceID != "disk-existing-fixture" || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 ||
		provider.proofCalls != 1 || len(provider.storageDiscoveries) != 1 || provider.storageCalls != 0 {
		t.Fatalf("proof=%#v err=%v provider=%#v", proof, err, provider)
	}
}

func TestComputeClaimRecoveryProofFailsClosedOnUnknownCBSDiscovery(t *testing.T) {
	for _, test := range []struct {
		name      string
		discovery StorageRecoveryDiscovery
		err       error
		want      string
	}{
		{name: "multiple", discovery: StorageRecoveryDiscovery{State: "unknown", Reason: "multiple_candidate"}, err: errors.New("multiple candidate"), want: "multiple_candidate"},
		{name: "identity drift", discovery: StorageRecoveryDiscovery{State: "unknown", Reason: "identity_mismatch"}, err: errors.New("identity mismatch"), want: "identity_mismatch"},
		{name: "describe error", discovery: StorageRecoveryDiscovery{State: "unknown", Reason: "provider_describe"}, err: errors.New("describe unavailable"), want: "provider_describe"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, provider, input := seedComputeClaimRecovery(t, "basic")
			provider.storageDiscovery, provider.storageDiscoveryErr = test.discovery, test.err

			proof, err := service.ComputeClaimRecoveryProof(context.Background(), input)

			if err == nil || proof.Eligible || proof.StorageState != "unknown" || proof.StorageProviderResourceID != "" || proof.Reason != test.want ||
				proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 || provider.proofCalls != 1 || len(provider.storageDiscoveries) != 1 || provider.storageCalls != 0 {
				t.Fatalf("proof=%#v err=%v provider=%#v", proof, err, provider)
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
		IdempotencyKey: input.LaunchOperationID + ":compute",
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
		IdempotencyKey: input.LaunchOperationID + ":compute",
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

func TestClaimComputeRecoveryReconcilesExactActiveOwnershipWithUnallocatedNodeOnce(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
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
	provider.proof.CVMOwnershipState = "target_owned"
	provider.proof.NodeOwnershipState = "unallocated"
	provider.claim.TencentMutationCount = 0
	provider.claim.KubernetesMutationCount = 1
	provider.claim.Evidence = &ComputeClaimEvidence{Node: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}}
	provider.claimHook = func() { provider.proof.NodeOwnershipState = "target_owned" }

	result, err := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	if err != nil || !result.Eligible || result.TencentMutationCount != 0 || result.KubernetesMutationCount != 1 || provider.claimCalls != 1 {
		t.Fatalf("active drift result=%#v err=%v provider=%#v", result, err, provider)
	}
	stored, listErr := store.List(context.Background())
	ledger, present, valid := decodeComputeClaimRecoveryMutation(stored[0])
	if listErr != nil || len(stored) != 1 || stored[0].Status != "succeeded" || !present || !valid || ledger.State != "observed" ||
		ledger.TencentMutationCount != 0 || ledger.KubernetesMutationCount != 1 {
		t.Fatalf("active drift stored=%#v listErr=%v ledger=%#v present=%v valid=%v", stored, listErr, ledger, present, valid)
	}

	provider.proof.NodeOwnershipState = "target_owned"
	replayed, replayErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	if replayErr != nil || replayed.TencentMutationCount != 0 || replayed.KubernetesMutationCount != 0 || provider.claimCalls != 1 {
		t.Fatalf("active drift replay=%#v err=%v provider=%#v", replayed, replayErr, provider)
	}
}

func TestClaimComputeRecoveryContinuesHistoricalCVMOnlyObservedLedgerWithOneNodePatch(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	canonicalInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	legacyInput := canonicalInput
	legacyInput.IdempotencyKey = input.LaunchOperationID + ":compute-claim"
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("seed operations=%#v err=%v", operations, err)
	}
	pending := operations[0]
	pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
	pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, newComputeClaimRecoveryBinding(legacyInput))
	pending.RedactedProviderPayload = withComputeClaimRecoveryMutation(pending.RedactedProviderPayload, computeClaimRecoveryMutationLedger{
		State: "observed", Reason: "none", TencentMutationCount: 1, KubernetesMutationCount: 0,
		Evidence: ComputeClaimEvidence{CVM: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}},
	})
	store.mu.Lock()
	store.operation[0] = pending
	store.mu.Unlock()
	provider.proof.CVMOwnershipState = "target_owned"
	provider.proof.NodeOwnershipState = "unallocated"
	provider.claim = ComputeClaimProviderClaim{
		Proof: ComputeClaimProviderProof{
			Status: "proven", Reason: "none", MachineName: provider.proof.MachineName, NodeName: provider.proof.NodeName,
			CVMInstanceID: provider.proof.CVMInstanceID, PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType,
			Zone: provider.proof.Zone, ChargeType: provider.proof.ChargeType, PeriodMonths: provider.proof.PeriodMonths,
			RenewFlag: provider.proof.RenewFlag, Deadline: provider.proof.Deadline, CVMOwnershipState: "target_owned", NodeOwnershipState: "target_owned",
		},
		KubernetesMutationCount: 1,
		Evidence:                &ComputeClaimEvidence{Node: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}},
	}

	result, claimErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), canonicalInput)
	stored, listErr := store.List(context.Background())
	ownership, ownershipErr := store.MachineOwnership(context.Background(), input.ComputeAllocationID)
	ledger, ledgerPresent, ledgerValid := decodeComputeClaimRecoveryMutation(stored[0])
	binding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(stored[0])
	if claimErr != nil || !result.Eligible || result.TencentMutationCount != 0 || result.KubernetesMutationCount != 1 ||
		provider.proofCalls != 1 || provider.claimCalls != 1 || ownershipErr != nil || ownership.Status != "active" || listErr != nil ||
		len(stored) != 1 || stored[0].Status != "succeeded" || !ledgerPresent || !ledgerValid || ledger.State != "observed" ||
		ledger.TencentMutationCount != 1 || ledger.KubernetesMutationCount != 1 || ledger.Evidence.CVM.Confirmed != 1 ||
		ledger.Evidence.Node.Confirmed != 1 || !bindingPresent || !bindingValid || binding != newComputeClaimRecoveryBinding(legacyInput) {
		t.Fatalf("result=%#v claimErr=%v stored=%#v listErr=%v ownership=%#v ownershipErr=%v ledger=%#v binding=%#v provider=%#v", result, claimErr, stored, listErr, ownership, ownershipErr, ledger, binding, provider)
	}
}

func TestClaimComputeRecoveryContinuesHistoricalBindingWithoutLedgerWithOneNodePatch(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	canonicalInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	legacyInput := canonicalInput
	legacyInput.IdempotencyKey = input.LaunchOperationID + ":compute-claim"
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("seed operations=%#v err=%v", operations, err)
	}
	pending := operations[0]
	pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
	pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, newComputeClaimRecoveryBinding(legacyInput))
	store.mu.Lock()
	store.operation[0] = pending
	store.mu.Unlock()
	provider.proof.CVMOwnershipState = "target_owned"
	provider.proof.NodeOwnershipState = "unallocated"
	provider.claim = ComputeClaimProviderClaim{
		Proof: ComputeClaimProviderProof{
			Status: "proven", Reason: "none", MachineName: provider.proof.MachineName, NodeName: provider.proof.NodeName,
			CVMInstanceID: provider.proof.CVMInstanceID, PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType,
			Zone: provider.proof.Zone, ChargeType: provider.proof.ChargeType, PeriodMonths: provider.proof.PeriodMonths,
			RenewFlag: provider.proof.RenewFlag, Deadline: provider.proof.Deadline, CVMOwnershipState: "target_owned", NodeOwnershipState: "target_owned",
		},
		KubernetesMutationCount: 1,
		Evidence:                &ComputeClaimEvidence{Node: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}},
	}

	result, claimErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), canonicalInput)
	stored, listErr := store.List(context.Background())
	ownership, ownershipErr := store.MachineOwnership(context.Background(), input.ComputeAllocationID)
	ledger, ledgerPresent, ledgerValid := decodeComputeClaimRecoveryMutation(stored[0])
	binding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(stored[0])
	if claimErr != nil || !result.Eligible || result.TencentMutationCount != 0 || result.KubernetesMutationCount != 1 ||
		provider.proofCalls != 1 || provider.claimCalls != 1 || ownershipErr != nil || ownership.Status != "active" || listErr != nil ||
		len(stored) != 1 || stored[0].Status != "succeeded" || !ledgerPresent || !ledgerValid || ledger.State != "observed" ||
		ledger.TencentMutationCount != 0 || ledger.KubernetesMutationCount != 1 || !reflect.DeepEqual(ledger.Evidence.CVM, ComputeClaimMutationEvidence{}) ||
		ledger.Evidence.Node.Confirmed != 1 || !bindingPresent || !bindingValid || binding != newComputeClaimRecoveryBinding(legacyInput) {
		t.Fatalf("result=%#v claimErr=%v stored=%#v listErr=%v ownership=%#v ownershipErr=%v ledger=%#v binding=%#v provider=%#v", result, claimErr, stored, listErr, ownership, ownershipErr, ledger, binding, provider)
	}
}

func TestClaimComputeRecoveryReconcilesOnlyIsolatedRequestHashAndPreservesUnknownLedger(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	persistedBinding := newComputeClaimRecoveryBinding(claimInput)
	persistedBinding.RequestHash = strings.Repeat("7", 64)
	originalLedger := computeClaimRecoveryMutationLedger{
		State: "observed", Reason: "provider_describe", TencentMutationCount: 1, FailureStage: "cvm_tag_readback", ProviderErrorClass: "provider_error",
		Evidence: ComputeClaimEvidence{CVM: ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"opl_account_id"}}},
	}
	store.mu.Lock()
	pending := store.operation[0]
	pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
	pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, persistedBinding)
	pending.RedactedProviderPayload = withComputeClaimRecoveryMutation(pending.RedactedProviderPayload, originalLedger)
	originalBindingJSON, bindingJSONErr := json.Marshal(pending.RedactedProviderPayload["computeClaimRecovery"])
	originalLedgerJSON, ledgerJSONErr := json.Marshal(pending.RedactedProviderPayload[computeClaimRecoveryMutationPayloadKey])
	if bindingJSONErr != nil || ledgerJSONErr != nil {
		store.mu.Unlock()
		t.Fatalf("seed payload: binding=%v ledger=%v", bindingJSONErr, ledgerJSONErr)
	}
	store.operation[0] = pending
	store.mu.Unlock()

	evidence, evidenceErr := NewServiceWithOperationStore(provider, store).ComputeClaimRecoveryIdentityEvidence(context.Background(), claimInput)
	if evidenceErr != nil || evidence.BindingClassification != "request-hash-reconciliation" || evidence.MutationLedgerOutcome != "unknown" ||
		len(evidence.Checks) != 10 || evidence.Checks[8].Field != "binding.targetHash" || !evidence.Checks[8].Matches ||
		evidence.Checks[9].Field != "binding.requestHash" || evidence.Checks[9].Matches ||
		evidence.Checks[9].ExpectedDigest == evidence.Checks[9].ActualDigest {
		t.Fatalf("identity evidence=%#v err=%v", evidence, evidenceErr)
	}

	configureRequestHashReconciliationNodeSuccess(provider)

	result, claimErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	stored, listErr := store.List(context.Background())
	binding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(stored[0])
	ledger, ledgerPresent, ledgerValid := decodeComputeClaimRecoveryMutation(stored[0])
	reconciliation, reconciliationPresent, reconciliationValid := decodeComputeClaimRecoveryReconciliation(stored[0])
	storedBindingJSON, storedBindingJSONErr := json.Marshal(stored[0].RedactedProviderPayload["computeClaimRecovery"])
	storedLedgerJSON, storedLedgerJSONErr := json.Marshal(stored[0].RedactedProviderPayload[computeClaimRecoveryMutationPayloadKey])
	if claimErr != nil || !result.Eligible || result.TencentMutationCount != 0 || result.KubernetesMutationCount != 1 ||
		provider.claimCalls != 0 || provider.nodeOnlyClaimCalls != 1 || listErr != nil || len(stored) != 1 || stored[0].Status != "succeeded" ||
		!bindingPresent || !bindingValid || binding != persistedBinding || !ledgerPresent || !ledgerValid || !reflect.DeepEqual(ledger, originalLedger) ||
		!reconciliationPresent || !reconciliationValid || reconciliation.State != "succeeded" ||
		!reflect.DeepEqual(reconciliation.Node, ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}) ||
		storedBindingJSONErr != nil || storedLedgerJSONErr != nil || !reflect.DeepEqual(storedBindingJSON, originalBindingJSON) || !reflect.DeepEqual(storedLedgerJSON, originalLedgerJSON) {
		t.Fatalf("result=%#v err=%v stored=%#v listErr=%v binding=%#v ledger=%#v reconciliation=%#v provider=%#v", result, claimErr, stored, listErr, binding, ledger, reconciliation, provider)
	}

	replayed, replayErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	replayedStored, replayedListErr := store.List(context.Background())
	if replayedListErr != nil || len(replayedStored) != 1 {
		t.Fatalf("replay list=%#v err=%v", replayedStored, replayedListErr)
	}
	replayedBindingJSON, replayedBindingJSONErr := json.Marshal(replayedStored[0].RedactedProviderPayload["computeClaimRecovery"])
	replayedLedgerJSON, replayedLedgerJSONErr := json.Marshal(replayedStored[0].RedactedProviderPayload[computeClaimRecoveryMutationPayloadKey])
	if replayErr != nil || !replayed.Eligible || replayed.TencentMutationCount != 0 || replayed.KubernetesMutationCount != 0 ||
		provider.claimCalls != 0 || provider.nodeOnlyClaimCalls != 1 ||
		replayedBindingJSONErr != nil || replayedLedgerJSONErr != nil ||
		!reflect.DeepEqual(replayedBindingJSON, originalBindingJSON) || !reflect.DeepEqual(replayedLedgerJSON, originalLedgerJSON) {
		t.Fatalf("replay=%#v err=%v provider=%#v", replayed, replayErr, provider)
	}
}

func TestClaimComputeRecoveryReconcilesFailedNormalLaunchTerminalEvidenceAndPreservesUnknown(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := seedNormalLaunchTerminalRequestHashReconciliationCandidate(t, store, provider, input)
	operations, listErr := store.List(context.Background())
	if listErr != nil || len(operations) != 1 {
		t.Fatalf("seed operations=%#v err=%v", operations, listErr)
	}
	original := operations[0]
	originalBinding, bindingErr := json.Marshal(original.RedactedProviderPayload["computeClaimRecovery"])
	originalBudgets, budgetsErr := json.Marshal(original.RedactedProviderPayload["normalLaunchMutationBudget"])
	originalTerminal, terminalErr := json.Marshal(original.RedactedProviderPayload[computeClaimTerminalEvidencePayloadKey])
	if bindingErr != nil || budgetsErr != nil || terminalErr != nil {
		t.Fatalf("seed evidence: binding=%v budgets=%v terminal=%v", bindingErr, budgetsErr, terminalErr)
	}
	if _, present, _ := decodeComputeClaimRecoveryMutation(original); present {
		t.Fatal("normal launch fixture unexpectedly contains a manual recovery ledger")
	}
	persistedBinding, persistedBindingPresent, persistedBindingValid := decodeComputeClaimRecoveryBinding(original)
	if digest, ok := normalLaunchTerminalRequestHashReconciliationEvidence(original, claimInput, persistedBinding); !ok || digest == "" || !persistedBindingPresent || !persistedBindingValid {
		terminal, terminalPresent, terminalValid := decodeComputeClaimTerminalEvidence(original)
		createBudget, createPresent, createValid := normalLaunchStageBudget(original.RedactedProviderPayload, "compute_create")
		cvmBudget, cvmPresent, cvmValid := normalLaunchStageBudget(original.RedactedProviderPayload, "compute_claim_cvm")
		_, nodePresent, nodeValid := normalLaunchStageBudget(original.RedactedProviderPayload, "compute_claim_node")
		t.Fatalf("normal launch provenance rejected: binding=%#v/%v/%v terminal=%#v/%v/%v create=%#v/%v/%v cvm=%#v/%v/%v node=%v/%v",
			persistedBinding, persistedBindingPresent, persistedBindingValid, terminal, terminalPresent, terminalValid,
			createBudget, createPresent, createValid, cvmBudget, cvmPresent, cvmValid, nodePresent, nodeValid)
	}

	evidence, evidenceErr := NewServiceWithOperationStore(provider, store).ComputeClaimRecoveryIdentityEvidence(context.Background(), claimInput)
	if evidenceErr != nil || evidence.BindingClassification != "request-hash-reconciliation" || evidence.MutationLedger != "absent" ||
		evidence.MutationLedgerOutcome != "confirmed_zero" || evidence.MutationLedgerDigest != computeClaimIdentityDigest("absent") ||
		evidence.MutationEvidence != nil || evidence.FailureStage != "" || evidence.ProviderErrorClass != "" || len(evidence.Checks) != 10 ||
		!evidence.Checks[8].Matches || evidence.Checks[9].Matches || !validComputeClaimRecoveryDigest(evidence.Checks[9].ExpectedDigest) ||
		!validComputeClaimRecoveryDigest(evidence.Checks[9].ActualDigest) || evidence.Checks[9].ExpectedDigest == evidence.Checks[9].ActualDigest {
		t.Fatalf("identity evidence=%#v err=%v", evidence, evidenceErr)
	}

	result, claimErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	stored, storedErr := store.List(context.Background())
	ownership, ownershipErr := store.MachineOwnership(context.Background(), input.ComputeAllocationID)
	if storedErr != nil || len(stored) != 1 {
		t.Fatalf("stored=%#v err=%v", stored, storedErr)
	}
	recovered := stored[0]
	binding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(recovered)
	reconciliation, reconciliationPresent, reconciliationValid := decodeComputeClaimRecoveryReconciliation(recovered)
	terminal, terminalPresent, terminalValid := decodeComputeClaimTerminalEvidence(recovered)
	createBudget, createPresent, createValid := normalLaunchStageBudget(recovered.RedactedProviderPayload, "compute_create")
	cvmBudget, cvmPresent, cvmValid := normalLaunchStageBudget(recovered.RedactedProviderPayload, "compute_claim_cvm")
	_, nodePresent, nodeValid := normalLaunchStageBudget(recovered.RedactedProviderPayload, "compute_claim_node")
	storedBinding, storedBindingErr := json.Marshal(recovered.RedactedProviderPayload["computeClaimRecovery"])
	storedBudgets, storedBudgetsErr := json.Marshal(recovered.RedactedProviderPayload["normalLaunchMutationBudget"])
	storedTerminal, storedTerminalErr := json.Marshal(recovered.RedactedProviderPayload[computeClaimTerminalEvidencePayloadKey])
	_, manualLedgerPresent, _ := decodeComputeClaimRecoveryMutation(recovered)
	if claimErr != nil || !result.Eligible || result.TencentMutationCount != 0 || result.KubernetesMutationCount != 1 ||
		provider.claimCalls != 0 || provider.nodeOnlyClaimCalls != 1 || ownershipErr != nil || ownership.Status != "active" || recovered.Status != "succeeded" ||
		!bindingPresent || !bindingValid || binding.RequestHash != strings.Repeat("7", 64) || manualLedgerPresent ||
		!reconciliationPresent || !reconciliationValid || reconciliation.SchemaVersion != 2 || reconciliation.Generation != "normal_launch_terminal_evidence_v1" || reconciliation.State != "succeeded" ||
		!terminalPresent || !terminalValid || terminal.Status != "terminal_unprovable" || !createPresent || !createValid || createBudget != confirmedNormalLaunchMutationBudget() ||
		!cvmPresent || !cvmValid || cvmBudget != reservedNormalLaunchMutationBudget() || nodePresent || !nodeValid ||
		storedBindingErr != nil || storedBudgetsErr != nil || storedTerminalErr != nil || !reflect.DeepEqual(storedBinding, originalBinding) ||
		!reflect.DeepEqual(storedBudgets, originalBudgets) || !reflect.DeepEqual(storedTerminal, originalTerminal) {
		t.Fatalf("result=%#v err=%v operation=%#v binding=%#v reconciliation=%#v terminal=%#v budgets=%#v/%#v nodePresent=%v ownership=%#v provider=%#v",
			result, claimErr, recovered, binding, reconciliation, terminal, createBudget, cvmBudget, nodePresent, ownership, provider)
	}
}

func TestClaimComputeRecoveryNormalLaunchTerminalReconciliationCASResponseLossReplaysOnce(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := seedNormalLaunchTerminalRequestHashReconciliationCandidate(t, store, provider, input)

	first, firstErr := NewServiceWithOperationStore(provider, &failAfterComputeClaimReconciliationCASStore{OperationStore: store}).ClaimComputeRecovery(context.Background(), claimInput)
	operations, listErr := store.List(context.Background())
	reconciliation, present, valid := decodeComputeClaimRecoveryReconciliation(operations[0])
	binding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(operations[0])
	provenance, provenanceValid := isolatedRequestHashReconciliationProvenance(operations[0], claimInput, binding, bindingPresent, bindingValid)
	matches := computeClaimRecoveryReconciliationMatches(reconciliation, operations[0], claimInput, binding, computeClaimRecoveryMutationLedger{})
	if firstErr == nil || first.Eligible || listErr != nil || len(operations) != 1 || operations[0].Status != "claim_pending" ||
		!present || !valid || reconciliation.SchemaVersion != 2 || reconciliation.State != "verified" || provider.claimCalls != 0 || provider.nodeOnlyClaimCalls != 0 ||
		first.TencentMutationCount != 0 || first.KubernetesMutationCount != 0 || !provenanceValid || provenance.SchemaVersion != 2 || !matches {
		t.Fatalf("first=%#v err=%v operations=%#v binding=%#v provenance=%#v/%v reconciliation=%#v matches=%v provider=%#v",
			first, firstErr, operations, binding, provenance, provenanceValid, reconciliation, matches, provider)
	}

	replayed, replayErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	if replayErr != nil || !replayed.Eligible || replayed.TencentMutationCount != 0 || replayed.KubernetesMutationCount != 1 ||
		provider.claimCalls != 0 || provider.nodeOnlyClaimCalls != 1 {
		t.Fatalf("replay=%#v err=%v provider=%#v", replayed, replayErr, provider)
	}
}

func TestClaimComputeRecoveryNormalLaunchTerminalVerifiedTargetReadbackSkipsProviderMutation(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := seedNormalLaunchTerminalRequestHashReconciliationCandidate(t, store, provider, input)

	first, firstErr := NewServiceWithOperationStore(provider, &failAfterComputeClaimReconciliationCASStore{OperationStore: store}).ClaimComputeRecovery(context.Background(), claimInput)
	if firstErr == nil || first.Eligible || provider.claimCalls != 0 || provider.nodeOnlyClaimCalls != 0 {
		t.Fatalf("first=%#v err=%v provider=%#v", first, firstErr, provider)
	}
	provider.proof.NodeOwnershipState = "target_owned"
	provider.claimHook = nil

	replayed, replayErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	operations, listErr := store.List(context.Background())
	reconciliation, present, valid := decodeComputeClaimRecoveryReconciliation(operations[0])
	if replayErr != nil || !replayed.Eligible || replayed.CVMOwnershipState != "target_owned" || replayed.NodeOwnershipState != "target_owned" ||
		replayed.TencentMutationCount != 0 || replayed.KubernetesMutationCount != 0 || provider.claimCalls != 0 || provider.nodeOnlyClaimCalls != 0 ||
		listErr != nil || len(operations) != 1 || operations[0].Status != "succeeded" || !present || !valid || reconciliation.State != "succeeded" {
		t.Fatalf("replay=%#v err=%v operations=%#v listErr=%v reconciliation=%#v provider=%#v", replayed, replayErr, operations, listErr, reconciliation, provider)
	}
}

func TestClaimComputeRecoveryNormalLaunchTerminalNodePatchResponseLossConvergesByReadback(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := seedNormalLaunchTerminalRequestHashReconciliationCandidate(t, store, provider, input)

	first, firstErr := NewServiceWithOperationStore(provider, &failBeforeReconciledComputeClaimObservedSaveStore{OperationStore: store}).ClaimComputeRecovery(context.Background(), claimInput)
	operations, listErr := store.List(context.Background())
	reconciliation, present, valid := decodeComputeClaimRecoveryReconciliation(operations[0])
	if firstErr == nil || first.Eligible || listErr != nil || len(operations) != 1 || !present || !valid || reconciliation.SchemaVersion != 2 ||
		reconciliation.State != "node_reserved" || provider.claimCalls != 0 || provider.nodeOnlyClaimCalls != 1 || first.TencentMutationCount != 0 || first.KubernetesMutationCount != 1 {
		t.Fatalf("first=%#v err=%v operations=%#v reconciliation=%#v provider=%#v", first, firstErr, operations, reconciliation, provider)
	}

	replayed, replayErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	if replayErr != nil || !replayed.Eligible || replayed.CVMOwnershipState != "target_owned" || replayed.NodeOwnershipState != "target_owned" ||
		replayed.TencentMutationCount != 0 || replayed.KubernetesMutationCount != 0 ||
		provider.claimCalls != 0 || provider.nodeOnlyClaimCalls != 1 {
		t.Fatalf("replay=%#v err=%v provider=%#v", replayed, replayErr, provider)
	}
}

func TestClaimComputeRecoveryNormalLaunchTerminalConcurrentReplayHasOneNodePatch(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := seedNormalLaunchTerminalRequestHashReconciliationCandidate(t, store, provider, input)

	type outcome struct {
		proof ComputeClaimRecoveryProof
		err   error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			proof, err := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
			results <- outcome{proof: proof, err: err}
		}()
	}
	close(start)
	for range 2 {
		result := <-results
		if result.err != nil || !result.proof.Eligible || result.proof.TencentMutationCount != 0 || result.proof.KubernetesMutationCount > 1 {
			t.Fatalf("outcome=%#v", result)
		}
	}
	if provider.claimCalls != 0 || provider.nodeOnlyClaimCalls != 1 {
		t.Fatalf("provider=%#v", provider)
	}
}

func TestClaimComputeRecoveryNormalLaunchTerminalReconciliationFailsClosedBeforeMutation(t *testing.T) {
	tests := map[string]func(*FabricOperation, *ComputeAllocation, *ComputeAllocationPreparation, *MachineOwnership, *fakeComputeClaimRecoveryProvider){
		"binding target": func(operation *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, _ *fakeComputeClaimRecoveryProvider) {
			binding, _, _ := decodeComputeClaimRecoveryBinding(*operation)
			binding.TargetHash = strings.Repeat("8", 64)
			operation.RedactedProviderPayload = withComputeClaimRecoveryBinding(operation.RedactedProviderPayload, binding)
		},
		"allocation plan": func(operation *FabricOperation, _ *ComputeAllocation, plan *ComputeAllocationPreparation, _ *MachineOwnership, _ *fakeComputeClaimRecoveryProvider) {
			plan.TargetReplicas++
			operation.RedactedProviderPayload["allocationPlan"] = *plan
		},
		"compute allocation": func(operation *FabricOperation, allocation *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, _ *fakeComputeClaimRecoveryProvider) {
			allocation.CVMInstanceID = "ins-other"
			allocation.InstanceID = "ins-other"
			operation.RedactedProviderPayload["resource"] = *allocation
		},
		"machine ownership": func(_ *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, ownership *MachineOwnership, _ *fakeComputeClaimRecoveryProvider) {
			ownership.InstanceID = "ins-other"
		},
		"terminal launch": func(operation *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, _ *fakeComputeClaimRecoveryProvider) {
			terminal, _, _ := decodeComputeClaimTerminalEvidence(*operation)
			terminal.LaunchOperationID = "workspace-launch-other"
			operation.RedactedProviderPayload = withComputeClaimTerminalEvidence(operation.RedactedProviderPayload, terminal)
		},
		"terminal binding": func(operation *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, _ *fakeComputeClaimRecoveryProvider) {
			terminal, _, _ := decodeComputeClaimTerminalEvidence(*operation)
			terminal.BindingDigest = strings.Repeat("9", 64)
			operation.RedactedProviderPayload = withComputeClaimTerminalEvidence(operation.RedactedProviderPayload, terminal)
		},
		"compute create budget": func(operation *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, _ *fakeComputeClaimRecoveryProvider) {
			operation.RedactedProviderPayload = withNormalLaunchStageBudget(operation.RedactedProviderPayload, "compute_create", reservedNormalLaunchMutationBudget())
		},
		"cvm budget": func(operation *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, _ *fakeComputeClaimRecoveryProvider) {
			operation.RedactedProviderPayload = withNormalLaunchStageBudget(operation.RedactedProviderPayload, "compute_claim_cvm", confirmedNormalLaunchMutationBudget())
		},
		"node budget": func(operation *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, _ *fakeComputeClaimRecoveryProvider) {
			operation.RedactedProviderPayload = withNormalLaunchStageBudget(operation.RedactedProviderPayload, "compute_claim_node", reservedNormalLaunchMutationBudget())
		},
		"manual ledger": func(operation *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, _ *fakeComputeClaimRecoveryProvider) {
			operation.RedactedProviderPayload = withComputeClaimRecoveryMutation(operation.RedactedProviderPayload, reservedComputeClaimRecoveryMutation())
		},
		"provider cvm unknown": func(_ *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, provider *fakeComputeClaimRecoveryProvider) {
			provider.proof.CVMOwnershipState = "unknown"
		},
		"provider machine": func(_ *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, provider *fakeComputeClaimRecoveryProvider) {
			provider.proof.MachineName = "machine-other"
		},
		"provider unknown": func(_ *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, provider *fakeComputeClaimRecoveryProvider) {
			provider.proofErr = errors.New("provider readback unavailable")
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			_, store, provider, input := seedComputeClaimRecovery(t, "basic")
			claimInput := seedNormalLaunchTerminalRequestHashReconciliationCandidate(t, store, provider, input)
			store.mu.Lock()
			operation := store.operation[0]
			var allocation ComputeAllocation
			plan, planPresent := decodeComputeAllocationPlan(operation)
			if !decodeOperationResource(operation, &allocation) || !planPresent {
				store.mu.Unlock()
				t.Fatal("seed compute identity missing")
			}
			ownership := store.machineOwnerships[input.ComputeAllocationID]
			mutate(&operation, &allocation, &plan, &ownership, provider)
			store.operation[0] = operation
			store.machineOwnerships[input.ComputeAllocationID] = ownership
			beforePayload, payloadErr := operationPayloadJSON(operation)
			store.mu.Unlock()
			if payloadErr != nil {
				t.Fatal(payloadErr)
			}

			result, claimErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
			stored, listErr := store.List(context.Background())
			afterPayload, afterPayloadErr := operationPayloadJSON(stored[0])
			_, reconciliationPresent, _ := decodeComputeClaimRecoveryReconciliation(stored[0])
			if claimErr == nil || result.Eligible || listErr != nil || len(stored) != 1 || afterPayloadErr != nil || beforePayload != afterPayload ||
				reconciliationPresent || provider.claimCalls != 0 || provider.nodeOnlyClaimCalls != 0 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
				t.Fatalf("result=%#v err=%v stored=%#v listErr=%v payloadChanged=%v provider=%#v", result, claimErr, stored, listErr, beforePayload != afterPayload, provider)
			}
		})
	}
}

func TestClaimComputeRecoveryRequestHashReconciliationCASResponseLossReplaysOnce(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := seedRequestHashReconciliationCandidate(t, store, provider, input)
	configureRequestHashReconciliationNodeSuccess(provider)

	first, firstErr := NewServiceWithOperationStore(provider, &failAfterComputeClaimReconciliationCASStore{OperationStore: store}).ClaimComputeRecovery(context.Background(), claimInput)
	operations, listErr := store.List(context.Background())
	reconciliation, present, valid := decodeComputeClaimRecoveryReconciliation(operations[0])
	if firstErr == nil || first.Eligible || listErr != nil || len(operations) != 1 || !present || !valid || reconciliation.State != "verified" ||
		provider.claimCalls != 0 || first.TencentMutationCount != 0 || first.KubernetesMutationCount != 0 {
		t.Fatalf("first=%#v err=%v operations=%#v reconciliation=%#v provider=%#v", first, firstErr, operations, reconciliation, provider)
	}

	replayed, replayErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	if replayErr != nil || !replayed.Eligible || replayed.TencentMutationCount != 0 || replayed.KubernetesMutationCount != 1 ||
		provider.claimCalls != 0 || provider.nodeOnlyClaimCalls != 1 {
		t.Fatalf("replay=%#v err=%v provider=%#v", replayed, replayErr, provider)
	}
}

func TestClaimComputeRecoveryRequestHashReconciliationNodePatchResponseLossConvergesByReadback(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := seedRequestHashReconciliationCandidate(t, store, provider, input)
	configureRequestHashReconciliationNodeSuccess(provider)

	first, firstErr := NewServiceWithOperationStore(provider, &failBeforeReconciledComputeClaimObservedSaveStore{OperationStore: store}).ClaimComputeRecovery(context.Background(), claimInput)
	operations, listErr := store.List(context.Background())
	reconciliation, present, valid := decodeComputeClaimRecoveryReconciliation(operations[0])
	if firstErr == nil || first.Eligible || listErr != nil || len(operations) != 1 || !present || !valid || reconciliation.State != "node_reserved" ||
		provider.claimCalls != 0 || provider.nodeOnlyClaimCalls != 1 || first.TencentMutationCount != 0 || first.KubernetesMutationCount != 1 {
		t.Fatalf("first=%#v err=%v operations=%#v reconciliation=%#v provider=%#v", first, firstErr, operations, reconciliation, provider)
	}

	replayed, replayErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	if replayErr != nil || !replayed.Eligible || replayed.TencentMutationCount != 0 || replayed.KubernetesMutationCount != 0 ||
		provider.claimCalls != 0 || provider.nodeOnlyClaimCalls != 1 {
		t.Fatalf("replay=%#v err=%v provider=%#v", replayed, replayErr, provider)
	}
}

func TestClaimComputeRecoveryRequestHashReconciliationConcurrentReplayHasOneNodePatch(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := seedRequestHashReconciliationCandidate(t, store, provider, input)
	configureRequestHashReconciliationNodeSuccess(provider)

	type outcome struct {
		proof ComputeClaimRecoveryProof
		err   error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			proof, err := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
			results <- outcome{proof: proof, err: err}
		}()
	}
	close(start)
	for range 2 {
		result := <-results
		if result.err != nil || !result.proof.Eligible || result.proof.TencentMutationCount != 0 || result.proof.KubernetesMutationCount > 1 {
			t.Fatalf("outcome=%#v", result)
		}
	}
	if provider.claimCalls != 0 || provider.nodeOnlyClaimCalls != 1 {
		t.Fatalf("provider=%#v", provider)
	}
}

func TestClaimComputeRecoveryRequestHashReconciliationRejectsReleasedOwnershipBeforeProofOrCAS(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := seedRequestHashReconciliationCandidate(t, store, provider, input)

	store.mu.Lock()
	ownership := store.machineOwnerships[input.ComputeAllocationID]
	releasedAt := time.Now().UTC()
	ownership.ReleasedAt = &releasedAt
	store.machineOwnerships[input.ComputeAllocationID] = ownership
	before := store.operation[0]
	beforePayload, beforePayloadErr := operationPayloadJSON(before)
	store.mu.Unlock()
	if beforePayloadErr != nil {
		t.Fatal(beforePayloadErr)
	}

	result, claimErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	operations, listErr := store.List(context.Background())
	afterPayload, afterPayloadErr := operationPayloadJSON(operations[0])
	_, reconciliationPresent, _ := decodeComputeClaimRecoveryReconciliation(operations[0])
	if claimErr == nil || result.Eligible || result.Reason != "local_identity" || listErr != nil || len(operations) != 1 ||
		afterPayloadErr != nil || beforePayload != afterPayload || reconciliationPresent || provider.proofCalls != 0 || provider.claimCalls != 0 ||
		provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("result=%#v err=%v operations=%#v listErr=%v payloadChanged=%v provider=%#v", result, claimErr, operations, listErr, beforePayload != afterPayload, provider)
	}
}

func TestClaimComputeRecoveryRequestHashReconciliationFailsClosedBeforeMutation(t *testing.T) {
	type driftCase struct {
		mutate                func(*FabricOperation, *ComputeAllocation, *ComputeAllocationPreparation, *MachineOwnership, *computeClaimRecoveryBinding, *computeClaimRecoveryMutationLedger, *fakeComputeClaimRecoveryProvider)
		extraCompute, storage bool
	}
	tests := map[string]driftCase{
		"operation request hash": {mutate: func(operation *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, _ *computeClaimRecoveryBinding, _ *computeClaimRecoveryMutationLedger, _ *fakeComputeClaimRecoveryProvider) {
			operation.RequestHash = strings.Repeat("6", 64)
		}},
		"binding launch": {mutate: func(_ *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, binding *computeClaimRecoveryBinding, _ *computeClaimRecoveryMutationLedger, _ *fakeComputeClaimRecoveryProvider) {
			binding.LaunchOperationID = "workspace-launch-other"
		}},
		"binding idempotency": {mutate: func(_ *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, binding *computeClaimRecoveryBinding, _ *computeClaimRecoveryMutationLedger, _ *fakeComputeClaimRecoveryProvider) {
			binding.IdempotencyKey = "workspace-launch-other:compute"
		}},
		"target drift": {mutate: func(_ *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, binding *computeClaimRecoveryBinding, _ *computeClaimRecoveryMutationLedger, _ *fakeComputeClaimRecoveryProvider) {
			binding.TargetHash = strings.Repeat("8", 64)
		}},
		"allocation status": {mutate: func(_ *FabricOperation, allocation *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, _ *computeClaimRecoveryBinding, _ *computeClaimRecoveryMutationLedger, _ *fakeComputeClaimRecoveryProvider) {
			allocation.Status = "ready"
		}},
		"allocation plan": {mutate: func(_ *FabricOperation, _ *ComputeAllocation, plan *ComputeAllocationPreparation, _ *MachineOwnership, _ *computeClaimRecoveryBinding, _ *computeClaimRecoveryMutationLedger, _ *fakeComputeClaimRecoveryProvider) {
			plan.TargetReplicas++
		}},
		"ownership status": {mutate: func(_ *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, ownership *MachineOwnership, _ *computeClaimRecoveryBinding, _ *computeClaimRecoveryMutationLedger, _ *fakeComputeClaimRecoveryProvider) {
			ownership.Status = "active"
		}},
		"ledger missing field": {mutate: func(_ *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, _ *computeClaimRecoveryBinding, ledger *computeClaimRecoveryMutationLedger, _ *fakeComputeClaimRecoveryProvider) {
			ledger.Evidence.CVM.Missing = []string{"opl_account_id", "opl_workspace_id"}
		}},
		"ledger provider error class": {mutate: func(_ *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, _ *computeClaimRecoveryBinding, ledger *computeClaimRecoveryMutationLedger, _ *fakeComputeClaimRecoveryProvider) {
			ledger.ProviderErrorClass = "readback_mismatch"
		}},
		"provider CVM unknown": {mutate: func(_ *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, _ *computeClaimRecoveryBinding, _ *computeClaimRecoveryMutationLedger, provider *fakeComputeClaimRecoveryProvider) {
			provider.proof.CVMOwnershipState = "unknown"
		}},
		"provider node drift": {mutate: func(_ *FabricOperation, _ *ComputeAllocation, _ *ComputeAllocationPreparation, _ *MachineOwnership, _ *computeClaimRecoveryBinding, _ *computeClaimRecoveryMutationLedger, provider *fakeComputeClaimRecoveryProvider) {
			provider.proof.NodeName = "node-other"
		}},
		"multiple compute candidates": {extraCompute: true},
		"storage already started":     {storage: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, store, provider, input := seedComputeClaimRecovery(t, "basic")
			claimInput := ComputeClaimRecoveryClaimInput{
				ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
				PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
				IdempotencyKey: input.LaunchOperationID + ":compute",
			}
			binding := newComputeClaimRecoveryBinding(claimInput)
			binding.RequestHash = strings.Repeat("7", 64)
			ledger := computeClaimRecoveryMutationLedger{
				State: "observed", Reason: "provider_describe", TencentMutationCount: 1, FailureStage: "cvm_tag_readback", ProviderErrorClass: "provider_error",
				Evidence: ComputeClaimEvidence{CVM: ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"opl_account_id"}}},
			}
			store.mu.Lock()
			pending := store.operation[0]
			var allocation ComputeAllocation
			plan, planPresent := decodeComputeAllocationPlan(pending)
			if !decodeOperationResource(pending, &allocation) || !planPresent {
				store.mu.Unlock()
				t.Fatal("seed compute identity missing")
			}
			ownership := store.machineOwnerships[input.ComputeAllocationID]
			if test.mutate != nil {
				test.mutate(&pending, &allocation, &plan, &ownership, &binding, &ledger, provider)
			}
			pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
			pending.RedactedProviderPayload = computeAllocationOperationPayload(allocation, plan)
			pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, binding)
			pending.RedactedProviderPayload = withComputeClaimRecoveryMutation(pending.RedactedProviderPayload, ledger)
			store.operation[0] = pending
			store.machineOwnerships[input.ComputeAllocationID] = ownership
			store.mu.Unlock()
			if test.extraCompute {
				extra := newOperation("create_compute_allocation", "compute_allocation", "ca_other", input.AccountID, input.WorkspaceID, input.LaunchOperationID+":compute-other", strings.Repeat("9", 64), time.Now())
				extra.ID, extra.Status, extra.RedactedProviderPayload = "fop-other", "started", map[string]any{}
				if err := store.Append(context.Background(), extra); err != nil {
					t.Fatal(err)
				}
			}
			if test.storage {
				storage := newOperation("create_storage_volume", "storage_volume", input.StorageVolumeID, input.AccountID, input.WorkspaceID, input.LaunchOperationID+":storage", strings.Repeat("9", 64), time.Now())
				storage.ID, storage.Status, storage.RedactedProviderPayload = "fop-storage", "started", map[string]any{}
				if err := store.Append(context.Background(), storage); err != nil {
					t.Fatal(err)
				}
			}
			originalPayload, payloadErr := operationPayloadJSON(pending)
			if payloadErr != nil {
				t.Fatal(payloadErr)
			}

			result, claimErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
			stored, listErr := store.List(context.Background())
			var original FabricOperation
			for _, operation := range stored {
				if operation.ID == pending.ID {
					original = operation
				}
			}
			storedPayload, storedPayloadErr := operationPayloadJSON(original)
			_, reconciliationPresent, _ := decodeComputeClaimRecoveryReconciliation(original)
			if claimErr == nil || result.Eligible || listErr != nil || storedPayloadErr != nil || storedPayload != originalPayload || reconciliationPresent || provider.claimCalls != 0 || provider.nodeOnlyClaimCalls != 0 ||
				provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
				t.Fatalf("result=%#v err=%v stored=%#v listErr=%v payloadChanged=%v provider=%#v", result, claimErr, stored, listErr, storedPayload != originalPayload, provider)
			}
		})
	}
}

func TestClaimComputeRecoveryRejectsHistoricalBindingUnlessLedgerIsCVMOnlyAndIdentityExact(t *testing.T) {
	tests := map[string]func(*ComputeClaimRecoveryClaimInput, *computeClaimRecoveryMutationLedger){
		"node already attempted": func(_ *ComputeClaimRecoveryClaimInput, ledger *computeClaimRecoveryMutationLedger) {
			ledger.KubernetesMutationCount = 1
			ledger.Evidence.Node = ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}
		},
		"target drift": func(legacyInput *ComputeClaimRecoveryClaimInput, _ *computeClaimRecoveryMutationLedger) {
			legacyInput.PrivateIP = "10.0.0.99"
		},
	}
	for name, drift := range tests {
		t.Run(name, func(t *testing.T) {
			_, store, provider, input := seedComputeClaimRecovery(t, "basic")
			canonicalInput := ComputeClaimRecoveryClaimInput{
				ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
				PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
				IdempotencyKey: input.LaunchOperationID + ":compute",
			}
			legacyInput := canonicalInput
			legacyInput.IdempotencyKey = input.LaunchOperationID + ":compute-claim"
			ledger := computeClaimRecoveryMutationLedger{
				State: "observed", Reason: "none", TencentMutationCount: 1, KubernetesMutationCount: 0,
				Evidence: ComputeClaimEvidence{CVM: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}},
			}
			drift(&legacyInput, &ledger)
			operations, err := store.List(context.Background())
			if err != nil || len(operations) != 1 {
				t.Fatalf("seed operations=%#v err=%v", operations, err)
			}
			pending := operations[0]
			pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
			pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, newComputeClaimRecoveryBinding(legacyInput))
			pending.RedactedProviderPayload = withComputeClaimRecoveryMutation(pending.RedactedProviderPayload, ledger)
			store.mu.Lock()
			store.operation[0] = pending
			store.mu.Unlock()
			provider.proof.CVMOwnershipState = "target_owned"
			provider.proof.NodeOwnershipState = "unallocated"

			result, claimErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), canonicalInput)
			if !errors.Is(claimErr, ErrComputeClaimRecoveryIdempotencyConflict) || result.Eligible || result.Reason != "identity_mismatch" ||
				provider.proofCalls != 0 || provider.claimCalls != 0 {
				t.Fatalf("result=%#v claimErr=%v provider=%#v", result, claimErr, provider)
			}
		})
	}
}

func TestClaimComputeRecoveryRejectsHistoricalBindingWithoutLedgerUnlessFreshProofIsNodeOnly(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	canonicalInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	legacyInput := canonicalInput
	legacyInput.IdempotencyKey = input.LaunchOperationID + ":compute-claim"
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("seed operations=%#v err=%v", operations, err)
	}
	pending := operations[0]
	pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
	pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, newComputeClaimRecoveryBinding(legacyInput))
	store.mu.Lock()
	store.operation[0] = pending
	store.mu.Unlock()
	provider.proof.CVMOwnershipState = "recoverable"
	provider.proof.NodeOwnershipState = "unallocated"

	result, claimErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), canonicalInput)
	if !errors.Is(claimErr, ErrComputeClaimRecoveryIdempotencyConflict) || result.Eligible || result.Reason != "identity_mismatch" ||
		provider.proofCalls != 1 || provider.claimCalls != 0 {
		t.Fatalf("result=%#v claimErr=%v provider=%#v", result, claimErr, provider)
	}
}

func TestClaimComputeRecoveryNodeReservedTargetOwnedReadbackDoesNotRepeatNodeClaim(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	provider.claim.Proof.Reason = "provider_describe"
	provider.claim.Proof.NodeOwnershipState = "unallocated"
	provider.claim.Proof.CVMOwnershipState = "recoverable"
	provider.claim.TencentMutationCount = 1
	provider.claim.KubernetesMutationCount = 0
	provider.claim.FailureStage = "cvm_tag_readback"
	provider.claim.ProviderErrorClass = "provider_error"
	provider.claim.Evidence = &ComputeClaimEvidence{CVM: ComputeClaimMutationEvidence{Attempted: 1, Missing: []string{"opl_account_id"}}}
	provider.claimErr = errors.New("provider tag readback failed")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	if _, err := service.ClaimComputeRecovery(context.Background(), claimInput); err == nil {
		t.Fatal("first claim unexpectedly succeeded")
	}

	provider.proof.CVMOwnershipState = "target_owned"
	provider.proof.NodeOwnershipState = "unallocated"
	provider.claim = ComputeClaimProviderClaim{
		Proof: ComputeClaimProviderProof{
			Status: "proven", Reason: "none", MachineName: provider.proof.MachineName, NodeName: provider.proof.NodeName,
			CVMInstanceID: provider.proof.CVMInstanceID, PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType,
			Zone: provider.proof.Zone, ChargeType: provider.proof.ChargeType, PeriodMonths: provider.proof.PeriodMonths,
			RenewFlag: provider.proof.RenewFlag, Deadline: provider.proof.Deadline, CVMOwnershipState: "target_owned", NodeOwnershipState: "target_owned",
		},
		KubernetesMutationCount: 1,
		Evidence:                &ComputeClaimEvidence{Node: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}},
	}
	provider.claimErr = nil
	interruptingStore := &failBeforeComputeClaimObservedSaveStore{OperationStore: store}

	interrupted, interruptedErr := NewServiceWithOperationStore(provider, interruptingStore).ClaimComputeRecovery(context.Background(), claimInput)
	if interruptedErr == nil || interrupted.Eligible || provider.claimCalls != 2 {
		t.Fatalf("interrupted=%#v interruptedErr=%v provider=%#v", interrupted, interruptedErr, provider)
	}
	provider.proof.NodeOwnershipState = "target_owned"
	recovered, recoveredErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	ownership, ownershipErr := store.MachineOwnership(context.Background(), input.ComputeAllocationID)
	operations, operationsErr := store.List(context.Background())
	ledger, ledgerPresent, ledgerValid := decodeComputeClaimRecoveryMutation(operations[0])
	if recoveredErr != nil || !recovered.Eligible || recovered.TencentMutationCount != 0 || recovered.KubernetesMutationCount != 0 ||
		ownershipErr != nil || ownership.Status != "active" || operationsErr != nil || provider.claimCalls != 2 || !ledgerPresent || !ledgerValid ||
		ledger.State != "observed" || ledger.Reason != "none" || ledger.TencentMutationCount != 1 || ledger.KubernetesMutationCount != 1 ||
		ledger.Evidence.CVM.Attempted != 1 || ledger.Evidence.CVM.Confirmed != 1 || ledger.Evidence.Node.Attempted != 1 || ledger.Evidence.Node.Confirmed != 1 {
		t.Fatalf("recovered=%#v recoveredErr=%v ownership=%#v ownershipErr=%v operationsErr=%v ledger=%#v present=%v valid=%v provider=%#v", recovered, recoveredErr, ownership, ownershipErr, operationsErr, ledger, ledgerPresent, ledgerValid, provider)
	}
	replayed, replayErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	if replayErr != nil || !replayed.Eligible || replayed.TencentMutationCount != 0 || replayed.KubernetesMutationCount != 0 || provider.claimCalls != 2 {
		t.Fatalf("same-binding success replay=%#v err=%v provider=%#v", replayed, replayErr, provider)
	}
}

func TestClaimComputeRecoveryNodeReservedRejectsIdentityTargetAndRequestDrift(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	provider.claim.Proof.Reason = "provider_describe"
	provider.claim.Proof.NodeOwnershipState = "unallocated"
	provider.claim.Proof.CVMOwnershipState = "recoverable"
	provider.claim.TencentMutationCount = 1
	provider.claim.KubernetesMutationCount = 0
	provider.claim.FailureStage = "cvm_tag_readback"
	provider.claim.ProviderErrorClass = "provider_error"
	provider.claim.Evidence = &ComputeClaimEvidence{CVM: ComputeClaimMutationEvidence{Attempted: 1, Missing: []string{"opl_account_id"}}}
	provider.claimErr = errors.New("provider tag readback failed")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	if _, err := service.ClaimComputeRecovery(context.Background(), claimInput); err == nil {
		t.Fatal("first claim unexpectedly succeeded")
	}
	provider.proof.CVMOwnershipState = "target_owned"
	provider.claim = ComputeClaimProviderClaim{
		Proof: ComputeClaimProviderProof{
			Status: "proven", Reason: "none", MachineName: provider.proof.MachineName, NodeName: provider.proof.NodeName,
			CVMInstanceID: provider.proof.CVMInstanceID, PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType,
			Zone: provider.proof.Zone, ChargeType: provider.proof.ChargeType, PeriodMonths: provider.proof.PeriodMonths,
			RenewFlag: provider.proof.RenewFlag, Deadline: provider.proof.Deadline, CVMOwnershipState: "target_owned", NodeOwnershipState: "target_owned",
		},
		KubernetesMutationCount: 1,
		Evidence:                &ComputeClaimEvidence{Node: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}},
	}
	provider.claimErr = nil
	if _, err := NewServiceWithOperationStore(provider, &failAfterComputeClaimNodeReservationStore{OperationStore: store}).ClaimComputeRecovery(context.Background(), claimInput); err == nil {
		t.Fatal("node reservation interruption unexpectedly succeeded")
	}
	claimCalls := provider.claimCalls

	tests := map[string]func(ComputeClaimRecoveryClaimInput) ComputeClaimRecoveryClaimInput{
		"identity": func(value ComputeClaimRecoveryClaimInput) ComputeClaimRecoveryClaimInput {
			value.AccountID = "acct-other"
			return value
		},
		"target": func(value ComputeClaimRecoveryClaimInput) ComputeClaimRecoveryClaimInput {
			value.PrivateIP = "10.0.0.99"
			return value
		},
		"request": func(value ComputeClaimRecoveryClaimInput) ComputeClaimRecoveryClaimInput {
			value.StorageVolumeID = "vol_other"
			return value
		},
	}
	for name, drift := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), drift(claimInput))
			if err == nil || result.Eligible || provider.claimCalls != claimCalls {
				t.Fatalf("result=%#v err=%v provider=%#v", result, err, provider)
			}
		})
	}
	operations, err := store.List(context.Background())
	ledger, present, valid := decodeComputeClaimRecoveryMutation(operations[0])
	if err != nil || len(operations) != 1 || !present || !valid || ledger.State != "node_reserved" || provider.claimCalls != claimCalls {
		t.Fatalf("operations=%#v err=%v ledger=%#v present=%v valid=%v provider=%#v", operations, err, ledger, present, valid, provider)
	}
}

func TestClaimComputeRecoveryRejectsDifferentFabricBinding(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":recovery-key",
	}

	result, err := service.ClaimComputeRecovery(context.Background(), claimInput)
	operations, operationsErr := store.List(context.Background())
	_, bindingPresent, _ := decodeComputeClaimRecoveryBinding(operations[0])
	_, ledgerPresent, _ := decodeComputeClaimRecoveryMutation(operations[0])
	if !errors.Is(err, ErrInvalidComputeClaimRecovery) || result.Reason != "local_identity" || operationsErr != nil ||
		bindingPresent || ledgerPresent || provider.proofCalls != 0 || provider.claimCalls != 0 {
		t.Fatalf("result=%#v err=%v operations=%#v operationsErr=%v bindingPresent=%v ledgerPresent=%v provider=%#v", result, err, operations, operationsErr, bindingPresent, ledgerPresent, provider)
	}
}

func TestClaimComputeRecoverySameBindingNonRecoverableFailureReplaysOnce(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	provider.claim.Proof.Reason = "provider_describe"
	provider.claim.Proof.NodeOwnershipState = "unallocated"
	provider.claim.Proof.CVMOwnershipState = "recoverable"
	provider.claim.TencentMutationCount = 1
	provider.claim.FailureStage = "cvm_tag_readback"
	provider.claim.ProviderErrorClass = "provider_error"
	provider.claim.Evidence = &ComputeClaimEvidence{CVM: ComputeClaimMutationEvidence{Attempted: 1, Missing: []string{"opl_account_id"}}}
	provider.claimErr = errors.New("provider tag readback failed")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	if _, err := service.ClaimComputeRecovery(context.Background(), claimInput); err == nil {
		t.Fatal("first claim unexpectedly succeeded")
	}
	provider.proof.CVMOwnershipState = "target_owned"
	provider.proof.NodeOwnershipState = "unallocated"
	result, err := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	if err == nil || result.Eligible || provider.claimCalls != 1 {
		t.Fatalf("result=%#v err=%v provider=%#v", result, err, provider)
	}
}

func TestClaimComputeRecoveryRestartAfterTargetOwnedReadbackConvergesLocalStateOnly(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
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
	provider.proof.NodeOwnershipState = "target_owned"
	replayed, replayErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	if replayErr != nil || !replayed.Eligible || replayed.TencentMutationCount != 0 || replayed.KubernetesMutationCount != 0 || provider.claimCalls != 0 {
		t.Fatalf("replayed=%#v replayErr=%v provider=%#v", replayed, replayErr, provider)
	}
}

func TestClaimComputeRecoveryRejectsMalformedPersistedBindingWithoutMutationOrOverwrite(t *testing.T) {
	tests := map[string]any{
		"not_an_object": "malformed",
		"missing_request_hash": map[string]any{
			"launchOperationId": "workspace-launch-fixture",
			"idempotencyKey":    "workspace-launch-fixture:compute",
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
				IdempotencyKey: input.LaunchOperationID + ":compute",
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

func TestComputeClaimRecoveryIdentityEvidenceClassifiesPersistedBindingWithoutMutation(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("seed operations=%#v err=%v", operations, err)
	}
	pending := operations[0]
	pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
	pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, historicalComputeClaimRecoveryBinding(claimInput))
	if err := store.SaveComputeClaimRecovery(context.Background(), operations[0], pending); err != nil {
		t.Fatal(err)
	}

	evidence, err := service.ComputeClaimRecoveryIdentityEvidence(context.Background(), claimInput)
	stored, listErr := store.List(context.Background())
	if err != nil || listErr != nil || evidence == nil || evidence.BindingClassification != "compute-claim" || len(evidence.BindingDigest) != 64 ||
		evidence.MutationLedger != "absent" || evidence.MutationEvidence != nil || len(evidence.Checks) != 10 ||
		len(stored) != 1 || !reflect.DeepEqual(stored[0], pending) || provider.proofCalls != 0 || provider.claimCalls != 0 ||
		provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("evidence=%#v err=%v stored=%#v listErr=%v provider=%#v", evidence, err, stored, listErr, provider)
	}
	checks := map[string]ComputeClaimIdentityCheck{}
	for _, check := range evidence.Checks {
		checks[check.Field] = check
	}
	if !checks["binding.compatibility"].Matches || checks["binding.compatibility"].Actual != "compute-claim" ||
		!checks["fabric.operationId"].Matches || !checks["fabric.operationRequestHash"].Matches ||
		!checks["binding.idempotencyKey"].Matches || !checks["binding.requestHash"].Matches ||
		checks["binding.targetHash"].ExpectedDigest == "" || checks["binding.targetHash"].ExpectedDigest != checks["binding.targetHash"].ActualDigest {
		t.Fatalf("unexpected identity checks: %#v", checks)
	}
	serialized, marshalErr := json.Marshal(evidence)
	if marshalErr != nil || strings.Contains(string(serialized), historicalComputeClaimRecoveryBinding(claimInput).RequestHash) {
		t.Fatalf("identity evidence leaked raw hash: %s err=%v", serialized, marshalErr)
	}
}

func TestComputeClaimRecoveryIdentityEvidenceClassifiesBindingGenerationsWithoutMutation(t *testing.T) {
	tests := map[string]struct {
		binding func(ComputeClaimRecoveryClaimInput) computeClaimRecoveryBinding
		want    string
	}{
		"current": {
			binding: newComputeClaimRecoveryBinding,
			want:    "current",
		},
		"compute claim": {
			binding: historicalComputeClaimRecoveryBinding,
			want:    "compute-claim",
		},
		"known legacy": {
			binding: func(input ComputeClaimRecoveryClaimInput) computeClaimRecoveryBinding {
				input.IdempotencyKey = "recovery-exec-0123456789abcdefabcd"
				return newComputeClaimRecoveryBinding(input)
			},
			want: "known-legacy",
		},
		"arbitrary legacy-shaped request": {
			binding: func(input ComputeClaimRecoveryClaimInput) computeClaimRecoveryBinding {
				input.IdempotencyKey = "recovery-exec-not-authoritative"
				return newComputeClaimRecoveryBinding(input)
			},
			want: "other",
		},
		"other": {
			binding: func(input ComputeClaimRecoveryClaimInput) computeClaimRecoveryBinding {
				binding := newComputeClaimRecoveryBinding(input)
				binding.TargetHash = "drifted-target-hash"
				return binding
			},
			want: "other",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			service, store, provider, input := seedComputeClaimRecovery(t, "basic")
			claimInput := ComputeClaimRecoveryClaimInput{
				ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
				PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
				IdempotencyKey: input.LaunchOperationID + ":compute",
			}
			store.mu.Lock()
			pending := store.operation[0]
			pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
			pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, test.binding(claimInput))
			store.operation[0] = pending
			store.mu.Unlock()

			evidence, err := service.ComputeClaimRecoveryIdentityEvidence(context.Background(), claimInput)
			stored, listErr := store.List(context.Background())
			if err != nil || listErr != nil || evidence == nil || evidence.BindingClassification != test.want || len(evidence.BindingDigest) != 64 ||
				len(stored) != 1 || !reflect.DeepEqual(stored[0], pending) || provider.proofCalls != 0 || provider.claimCalls != 0 ||
				provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
				t.Fatalf("evidence=%#v err=%v stored=%#v listErr=%v provider=%#v", evidence, err, stored, listErr, provider)
			}
			if test.want == "known-legacy" {
				for _, check := range evidence.Checks {
					if check.Field == "binding.compatibility" && check.Matches {
						t.Fatalf("known legacy binding became mutation authority: %#v", evidence.Checks)
					}
				}
			}
		})
	}
}

func TestComputeClaimRecoveryIdentityEvidenceClassifiesObservedConfirmedZeroLedgerWithoutMutation(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	store.mu.Lock()
	if len(store.operation) != 1 {
		store.mu.Unlock()
		t.Fatalf("operations=%#v", store.operation)
	}
	pending := store.operation[0]
	pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
	pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, historicalComputeClaimRecoveryBinding(claimInput))
	pending.RedactedProviderPayload = withComputeClaimRecoveryMutation(pending.RedactedProviderPayload, computeClaimRecoveryMutationLedger{
		State: "observed", Reason: "identity_mismatch", Evidence: ComputeClaimEvidence{},
	})
	store.operation[0] = pending
	store.mu.Unlock()

	evidence, err := service.ComputeClaimRecoveryIdentityEvidence(context.Background(), claimInput)
	stored, listErr := store.List(context.Background())
	if err != nil || listErr != nil || evidence == nil || evidence.MutationLedger != "observed" ||
		evidence.MutationLedgerOutcome != "confirmed_zero" || len(evidence.MutationLedgerDigest) != 64 ||
		len(stored) != 1 || !reflect.DeepEqual(stored[0], pending) || provider.proofCalls != 0 || provider.claimCalls != 0 ||
		provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("evidence=%#v err=%v stored=%#v listErr=%v provider=%#v", evidence, err, stored, listErr, provider)
	}
}

func TestComputeClaimRecoveryIdentityEvidenceClassifiesObservedNonzeroLedgerWithoutMutation(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	store.mu.Lock()
	if len(store.operation) != 1 {
		store.mu.Unlock()
		t.Fatalf("operations=%#v", store.operation)
	}
	pending := store.operation[0]
	pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
	pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, historicalComputeClaimRecoveryBinding(claimInput))
	pending.RedactedProviderPayload = withComputeClaimRecoveryMutation(pending.RedactedProviderPayload, computeClaimRecoveryMutationLedger{
		State: "observed", Reason: "none", TencentMutationCount: 1, KubernetesMutationCount: 1,
		Evidence: ComputeClaimEvidence{
			CVM:  ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
			Node: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
		},
	})
	store.operation[0] = pending
	store.mu.Unlock()

	evidence, err := service.ComputeClaimRecoveryIdentityEvidence(context.Background(), claimInput)
	stored, listErr := store.List(context.Background())
	if err != nil || listErr != nil || evidence == nil || evidence.MutationLedger != "observed" ||
		evidence.MutationLedgerOutcome != "nonzero" || len(evidence.MutationLedgerDigest) != 64 ||
		evidence.MutationEvidence == nil || !reflect.DeepEqual(*evidence.MutationEvidence, ComputeClaimEvidence{
		CVM: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}, Node: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
	}) || evidence.FailureStage != "" || evidence.ProviderErrorClass != "" ||
		len(stored) != 1 || !reflect.DeepEqual(stored[0], pending) || provider.proofCalls != 0 || provider.claimCalls != 0 ||
		provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("evidence=%#v err=%v stored=%#v listErr=%v provider=%#v", evidence, err, stored, listErr, provider)
	}
}

func TestComputeClaimRecoveryIdentityEvidenceProjectsExactUnconfirmedLedgerWithoutMutation(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	wantEvidence := ComputeClaimEvidence{
		CVM: ComputeClaimMutationEvidence{
			Attempted: 1, Missing: []string{"opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id"},
		},
		Node: ComputeClaimMutationEvidence{},
	}
	store.mu.Lock()
	pending := store.operation[0]
	pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
	pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, historicalComputeClaimRecoveryBinding(claimInput))
	pending.RedactedProviderPayload = withComputeClaimRecoveryMutation(pending.RedactedProviderPayload, computeClaimRecoveryMutationLedger{
		State: "observed", Reason: "provider_describe", TencentMutationCount: 1, KubernetesMutationCount: 0,
		FailureStage: "cvm_tag_readback", ProviderErrorClass: "readback_mismatch", Evidence: wantEvidence,
	})
	store.operation[0] = pending
	store.mu.Unlock()

	evidence, err := service.ComputeClaimRecoveryIdentityEvidence(context.Background(), claimInput)
	stored, listErr := store.List(context.Background())
	if err != nil || listErr != nil || evidence == nil || evidence.BindingClassification != "compute-claim" ||
		evidence.MutationLedger != "observed" || evidence.MutationLedgerOutcome != "unknown" ||
		evidence.MutationEvidence == nil || !reflect.DeepEqual(*evidence.MutationEvidence, wantEvidence) ||
		evidence.FailureStage != "cvm_tag_readback" || evidence.ProviderErrorClass != "readback_mismatch" ||
		len(stored) != 1 || !reflect.DeepEqual(stored[0], pending) || provider.proofCalls != 0 || provider.claimCalls != 0 ||
		provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("evidence=%#v err=%v stored=%#v listErr=%v provider=%#v", evidence, err, stored, listErr, provider)
	}
}

func TestComputeClaimRecoveryIdentityEvidenceKeepsUnconfirmedLedgersUnknown(t *testing.T) {
	tests := map[string]struct {
		payload map[string]any
		state   string
	}{
		"reserved": {
			payload: withComputeClaimRecoveryMutation(nil, reservedComputeClaimRecoveryMutation()),
			state:   "reserved",
		},
		"invalid": {
			payload: map[string]any{computeClaimRecoveryMutationPayloadKey: "invalid"},
			state:   "invalid",
		},
		"incomplete": {
			payload: withComputeClaimRecoveryMutation(nil, computeClaimRecoveryMutationLedger{
				State: "observed", Reason: "identity_mismatch", TencentMutationCount: 1,
				Evidence: ComputeClaimEvidence{
					CVM: ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"instance_name"}},
				},
			}),
			state: "observed",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			operation := FabricOperation{RedactedProviderPayload: test.payload}
			state, outcome, digest := computeClaimMutationLedgerEvidence(operation)
			if state != test.state || outcome != "unknown" || len(digest) != 64 {
				t.Fatalf("state=%q outcome=%q digest=%q", state, outcome, digest)
			}
		})
	}
}

func TestComputeClaimRecoveryIdentityEvidenceDetectsOriginalOperationRequestHashDriftWithoutMutation(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	store.mu.Lock()
	if len(store.operation) != 1 {
		store.mu.Unlock()
		t.Fatalf("operations=%#v", store.operation)
	}
	pending := store.operation[0]
	pending.OperationID = "op_create_compute_allocation_drifted"
	pending.RequestHash = "drifted-original-compute-request"
	pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, historicalComputeClaimRecoveryBinding(claimInput))
	store.operation[0] = pending
	store.mu.Unlock()

	evidence, err := service.ComputeClaimRecoveryIdentityEvidence(context.Background(), claimInput)
	stored, listErr := store.List(context.Background())
	if err != nil || listErr != nil || evidence == nil || len(stored) != 1 || !reflect.DeepEqual(stored[0], pending) ||
		provider.proofCalls != 0 || provider.claimCalls != 0 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("evidence=%#v err=%v stored=%#v listErr=%v provider=%#v", evidence, err, stored, listErr, provider)
	}
	checks := map[string]ComputeClaimIdentityCheck{}
	for _, check := range evidence.Checks {
		checks[check.Field] = check
	}
	operationID := checks["fabric.operationId"]
	requestHash := checks["fabric.operationRequestHash"]
	if operationID.Matches || operationID.Expected == "" || operationID.Actual != pending.OperationID ||
		requestHash.Matches || requestHash.ExpectedDigest == "" || requestHash.ActualDigest == "" || requestHash.ExpectedDigest == requestHash.ActualDigest {
		t.Fatalf("operation identity drift was not classified: operation=%#v requestHash=%#v", operationID, requestHash)
	}
	serialized, marshalErr := json.Marshal(evidence)
	if marshalErr != nil || strings.Contains(string(serialized), pending.RequestHash) {
		t.Fatalf("identity evidence leaked raw request hash: %s err=%v", serialized, marshalErr)
	}
}

func TestMemoryComputeClaimRecoveryCASRejectsPersistedBindingDrift(t *testing.T) {
	drifts := map[string]func(*computeClaimRecoveryBinding){
		"launch": func(binding *computeClaimRecoveryBinding) { binding.LaunchOperationID = "workspace-launch-other" },
		"idempotency": func(binding *computeClaimRecoveryBinding) {
			binding.IdempotencyKey = "workspace-launch-fixture:compute-other"
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
				IdempotencyKey: input.LaunchOperationID + ":compute",
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

func TestMemoryComputeClaimRecoveryCASRejectsReservedLedgerCompletion(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
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
	reserved := pending
	reserved.RedactedProviderPayload = withComputeClaimRecoveryMutation(reserved.RedactedProviderPayload, reservedComputeClaimRecoveryMutation())
	if err := store.SaveComputeClaimRecovery(context.Background(), pending, reserved); err != nil {
		t.Fatal(err)
	}
	completed := reserved
	completed.Status, completed.FinishedAt = "succeeded", time.Now().UTC()

	if err := store.SaveComputeClaimRecovery(context.Background(), reserved, completed); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("reserved completion error=%v, want ErrRuntimeOperationNotCurrent", err)
	}
	stored, err := store.List(context.Background())
	if err != nil || len(stored) != 1 || stored[0].Status != "claim_pending" || !reflect.DeepEqual(stored[0].RedactedProviderPayload, reserved.RedactedProviderPayload) {
		t.Fatalf("reserved completion changed operation: stored=%#v err=%v", stored, err)
	}
}

func TestMemoryComputeClaimRecoveryNodeReservationCASHasSingleWinnerAndKeepsOriginalBinding(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("seed operations=%#v err=%v", operations, err)
	}
	pending := operations[0]
	pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
	pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, newComputeClaimRecoveryBinding(claimInput))
	observed := observedComputeClaimRecoveryMutation(ComputeClaimRecoveryProof{
		Reason: "provider_describe", TencentMutationCount: 1, KubernetesMutationCount: 0,
		FailureStage: "cvm_tag_readback", ProviderErrorClass: "provider_error",
		Evidence: &ComputeClaimEvidence{CVM: ComputeClaimMutationEvidence{Attempted: 1, Missing: []string{"opl_account_id"}}},
	})
	pending.RedactedProviderPayload = withComputeClaimRecoveryMutation(pending.RedactedProviderPayload, observed)
	store.mu.Lock()
	store.operation[0] = pending
	store.mu.Unlock()
	storedObserved, err := store.List(context.Background())
	if err != nil || len(storedObserved) != 1 {
		t.Fatal(err)
	}
	observedOperation := storedObserved[0]
	nodeReserved := observedOperation
	nodeReserved.RedactedProviderPayload = withComputeClaimRecoveryMutation(nodeReserved.RedactedProviderPayload, nodeReservedComputeClaimRecoveryMutation(observed))

	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- store.SaveComputeClaimRecovery(context.Background(), observedOperation, nodeReserved)
		}()
	}
	close(start)
	winners := 0
	for range 2 {
		err := <-results
		if err == nil {
			winners++
			continue
		}
		if !errors.Is(err, ErrRuntimeOperationNotCurrent) {
			t.Fatalf("node reservation CAS error=%v", err)
		}
	}
	stored, err := store.List(context.Background())
	binding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(stored[0])
	ledger, ledgerPresent, ledgerValid := decodeComputeClaimRecoveryMutation(stored[0])
	if winners != 1 || err != nil || len(stored) != 1 || !bindingPresent || !bindingValid || binding != newComputeClaimRecoveryBinding(claimInput) ||
		!ledgerPresent || !ledgerValid || ledger.State != "node_reserved" {
		t.Fatalf("winners=%d stored=%#v err=%v binding=%#v present=%v valid=%v ledger=%#v ledgerPresent=%v ledgerValid=%v", winners, stored, err, binding, bindingPresent, bindingValid, ledger, ledgerPresent, ledgerValid)
	}
}

func TestClaimComputeRecoveryRequiresStrictTargetOwnedReadback(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "pro")
	provider.claim.Proof.NodeOwnershipState = "unallocated"
	provider.claimErr = errors.New("strict readback failed")

	result, err := service.ClaimComputeRecovery(context.Background(), ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
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
		IdempotencyKey: input.LaunchOperationID + ":compute",
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
	provider.claim.Evidence = &ComputeClaimEvidence{
		CVM:  ComputeClaimMutationEvidence{Attempted: 4, Confirmed: 4},
		Node: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
	}
	provider.claimErr = errors.New("node readback forbidden")

	result, err := service.ClaimComputeRecovery(context.Background(), ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	})
	ownership, _ := store.MachineOwnership(context.Background(), input.ComputeAllocationID)
	operations, _ := store.List(context.Background())
	if err == nil || result.Eligible || result.Reason != "iam_rbac" || result.TencentMutationCount != 4 || result.KubernetesMutationCount != 1 ||
		ownership.Status != "quarantined" || provider.claimCalls != 1 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("result=%#v err=%v ownership=%#v provider=%#v", result, err, ownership, provider)
	}
	assertRecoveredComputeOperation(t, operations, input, "claim_pending")
}

func TestClaimComputeRecoveryFailureReplayDoesNotSpendExternalMutationBudgetAgain(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	provider.claim.Proof.Reason = "provider_describe"
	provider.claim.Proof.NodeOwnershipState = "unallocated"
	provider.claim.Proof.CVMOwnershipState = "recoverable"
	provider.claim.TencentMutationCount = 1
	provider.claim.KubernetesMutationCount = 0
	provider.claim.FailureStage = "cvm_tag_readback"
	provider.claim.ProviderErrorClass = "timeout"
	provider.claim.Evidence = &ComputeClaimEvidence{
		CVM:  ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"opl_workspace_id"}},
		Node: ComputeClaimMutationEvidence{},
	}
	provider.claimErr = errors.New("provider readback timed out")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}

	first, firstErr := service.ClaimComputeRecovery(context.Background(), claimInput)
	second, secondErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)

	if firstErr == nil || secondErr == nil || first.Eligible || second.Eligible || provider.claimCalls != 1 || provider.proofCalls != 2 {
		t.Fatalf("first=%#v firstErr=%v second=%#v secondErr=%v provider=%#v", first, firstErr, second, secondErr, provider)
	}
	if second.Reason != first.Reason || second.TencentMutationCount != first.TencentMutationCount ||
		second.KubernetesMutationCount != first.KubernetesMutationCount || second.FailureStage != first.FailureStage ||
		second.ProviderErrorClass != first.ProviderErrorClass || !reflect.DeepEqual(second.Evidence, first.Evidence) {
		t.Fatalf("replay changed persisted mutation proof: first=%#v second=%#v", first, second)
	}
}

func TestClaimComputeRecoveryResumesNodeOnlyAfterObservedCVMTagReadbackRepair(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	provider.claim.Proof.Reason = "provider_describe"
	provider.claim.Proof.NodeOwnershipState = "unallocated"
	provider.claim.Proof.CVMOwnershipState = "recoverable"
	provider.claim.TencentMutationCount = 1
	provider.claim.KubernetesMutationCount = 0
	provider.claim.FailureStage = "cvm_tag_readback"
	provider.claim.ProviderErrorClass = "provider_error"
	provider.claim.Evidence = &ComputeClaimEvidence{
		CVM: ComputeClaimMutationEvidence{Attempted: 1, Missing: []string{"opl_account_id"}},
	}
	provider.claimErr = errors.New("provider tag readback failed")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}

	first, firstErr := service.ClaimComputeRecovery(context.Background(), claimInput)
	if firstErr == nil || first.Eligible || provider.claimCalls != 1 {
		t.Fatalf("first=%#v firstErr=%v provider=%#v", first, firstErr, provider)
	}
	claimInput.IdempotencyKey = input.LaunchOperationID + ":compute"
	provider.proof.CVMOwnershipState = "target_owned"
	provider.proof.NodeOwnershipState = "unallocated"
	provider.claim = ComputeClaimProviderClaim{
		Proof: ComputeClaimProviderProof{
			Status: "proven", Reason: "none", MachineName: provider.proof.MachineName, NodeName: provider.proof.NodeName,
			CVMInstanceID: provider.proof.CVMInstanceID, PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType,
			Zone: provider.proof.Zone, ChargeType: provider.proof.ChargeType, PeriodMonths: provider.proof.PeriodMonths,
			RenewFlag: provider.proof.RenewFlag, Deadline: provider.proof.Deadline, CVMOwnershipState: "target_owned", NodeOwnershipState: "target_owned",
		},
		KubernetesMutationCount: 1,
		Evidence:                &ComputeClaimEvidence{Node: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}},
	}
	provider.claimErr = nil

	recovered, recoverErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	ownership, ownershipErr := store.MachineOwnership(context.Background(), input.ComputeAllocationID)
	operations, operationsErr := store.List(context.Background())
	recoveredBinding, recoveredBindingPresent, recoveredBindingValid := decodeComputeClaimRecoveryBinding(operations[0])
	if recoverErr != nil || ownershipErr != nil || operationsErr != nil || !recovered.Eligible || recovered.TencentMutationCount != 0 ||
		recovered.KubernetesMutationCount != 1 || ownership.Status != "active" || provider.claimCalls != 2 ||
		!recoveredBindingPresent || !recoveredBindingValid || recoveredBinding.IdempotencyKey != input.LaunchOperationID+":compute" {
		t.Fatalf("recovered=%#v recoverErr=%v ownership=%#v ownershipErr=%v operationsErr=%v provider=%#v", recovered, recoverErr, ownership, ownershipErr, operationsErr, provider)
	}
	assertRecoveredComputeOperation(t, operations, input, "succeeded")
}

func TestClaimComputeRecoveryRestartsAfterNodeReservationBeforeNodeClaim(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	provider.claim.Proof.Reason = "provider_describe"
	provider.claim.Proof.NodeOwnershipState = "unallocated"
	provider.claim.Proof.CVMOwnershipState = "recoverable"
	provider.claim.TencentMutationCount = 1
	provider.claim.KubernetesMutationCount = 0
	provider.claim.FailureStage = "cvm_tag_readback"
	provider.claim.ProviderErrorClass = "provider_error"
	provider.claim.Evidence = &ComputeClaimEvidence{
		CVM: ComputeClaimMutationEvidence{Attempted: 1, Missing: []string{"opl_account_id"}},
	}
	provider.claimErr = errors.New("provider tag readback failed")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	if _, err := service.ClaimComputeRecovery(context.Background(), claimInput); err == nil {
		t.Fatal("first claim unexpectedly succeeded")
	}

	claimInput.IdempotencyKey = input.LaunchOperationID + ":compute"
	provider.proof.CVMOwnershipState = "target_owned"
	provider.proof.NodeOwnershipState = "unallocated"
	provider.claim = ComputeClaimProviderClaim{
		Proof: ComputeClaimProviderProof{
			Status: "proven", Reason: "none", MachineName: provider.proof.MachineName, NodeName: provider.proof.NodeName,
			CVMInstanceID: provider.proof.CVMInstanceID, PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType,
			Zone: provider.proof.Zone, ChargeType: provider.proof.ChargeType, PeriodMonths: provider.proof.PeriodMonths,
			RenewFlag: provider.proof.RenewFlag, Deadline: provider.proof.Deadline, CVMOwnershipState: "target_owned", NodeOwnershipState: "target_owned",
		},
		KubernetesMutationCount: 1,
		Evidence:                &ComputeClaimEvidence{Node: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}},
	}
	provider.claimErr = nil
	interruptingStore := &failAfterComputeClaimNodeReservationStore{OperationStore: store}

	interrupted, interruptedErr := NewServiceWithOperationStore(provider, interruptingStore).ClaimComputeRecovery(context.Background(), claimInput)
	interruptedOperations, listErr := store.List(context.Background())
	if interruptedErr == nil || interrupted.Eligible || listErr != nil || provider.claimCalls != 1 || len(interruptedOperations) != 1 {
		t.Fatalf("interrupted=%#v err=%v operations=%#v listErr=%v provider=%#v", interrupted, interruptedErr, interruptedOperations, listErr, provider)
	}
	reservedLedger, reservedPresent, reservedValid := decodeComputeClaimRecoveryMutation(interruptedOperations[0])
	reservedBinding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(interruptedOperations[0])
	if !reservedPresent || !reservedValid || reservedLedger.State != "node_reserved" || reservedLedger.TencentMutationCount != 1 ||
		reservedLedger.KubernetesMutationCount != 1 || reservedLedger.Evidence.CVM.Attempted != 1 || reservedLedger.Evidence.CVM.Confirmed != 1 ||
		reservedLedger.Evidence.CVM.Unknown != 0 || len(reservedLedger.Evidence.CVM.Missing) != 0 || reservedLedger.Evidence.Node.Attempted != 1 ||
		reservedLedger.Evidence.Node.Confirmed != 0 || reservedLedger.Evidence.Node.Unknown != 1 || !reflect.DeepEqual(reservedLedger.Evidence.Node.Missing, []string{"node_ownership"}) ||
		!bindingPresent || !bindingValid || reservedBinding.IdempotencyKey != input.LaunchOperationID+":compute" {
		t.Fatalf("node-only reservation changed the original binding: ledger=%#v binding=%#v present=%v valid=%v", reservedLedger, reservedBinding, bindingPresent, bindingValid)
	}

	recovered, recoverErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	ownership, ownershipErr := store.MachineOwnership(context.Background(), input.ComputeAllocationID)
	operations, operationsErr := store.List(context.Background())
	finalLedger, finalPresent, finalValid := decodeComputeClaimRecoveryMutation(operations[0])
	if recoverErr == nil || recovered.Eligible || recovered.Reason != "provider_describe" || recovered.TencentMutationCount != 1 ||
		recovered.KubernetesMutationCount != 1 || ownershipErr != nil || ownership.Status != "quarantined" || operationsErr != nil || provider.claimCalls != 1 ||
		!finalPresent || !finalValid || finalLedger.State != "node_reserved" || finalLedger.Reason != "provider_describe" ||
		finalLedger.TencentMutationCount != 1 || finalLedger.KubernetesMutationCount != 1 || finalLedger.Evidence.CVM.Attempted != 1 ||
		finalLedger.Evidence.CVM.Confirmed != 1 || finalLedger.Evidence.Node.Attempted != 1 || finalLedger.Evidence.Node.Confirmed != 0 ||
		finalLedger.Evidence.Node.Unknown != 1 || !reflect.DeepEqual(finalLedger.Evidence.Node.Missing, []string{"node_ownership"}) {
		t.Fatalf("restart after node reservation=%#v recoverErr=%v ownership=%#v ownershipErr=%v operationsErr=%v ledger=%#v present=%v valid=%v provider=%#v", recovered, recoverErr, ownership, ownershipErr, operationsErr, finalLedger, finalPresent, finalValid, provider)
	}
	assertRecoveredComputeOperation(t, operations, input, "claim_pending")
}

func TestClaimComputeRecoveryDoesNotResumeObservedUnknownCVMOutcome(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	provider.claim.Proof.Reason = "provider_describe"
	provider.claim.Proof.NodeOwnershipState = "unallocated"
	provider.claim.Proof.CVMOwnershipState = "recoverable"
	provider.claim.TencentMutationCount = 1
	provider.claim.FailureStage = "cvm_tag_readback"
	provider.claim.ProviderErrorClass = "timeout"
	provider.claim.Evidence = &ComputeClaimEvidence{
		CVM: ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"opl_account_id"}},
	}
	provider.claimErr = errors.New("provider tag readback timed out")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}

	if _, err := service.ClaimComputeRecovery(context.Background(), claimInput); err == nil {
		t.Fatal("first claim unexpectedly succeeded")
	}
	claimInput.IdempotencyKey = input.LaunchOperationID + ":compute"
	provider.proof.CVMOwnershipState = "target_owned"
	provider.proof.NodeOwnershipState = "unallocated"
	second, secondErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	if secondErr == nil || second.Eligible || second.Reason != "provider_describe" || provider.claimCalls != 1 ||
		second.Evidence == nil || second.Evidence.CVM.Unknown == 0 || second.KubernetesMutationCount != 1 {
		t.Fatalf("second=%#v secondErr=%v provider=%#v", second, secondErr, provider)
	}
}

func TestClaimComputeRecoveryRejectsUnallowlistedMutationEvidenceBeforeResponseAndReplay(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	const marker = "ghp_secret"
	provider.claim.Proof.Reason = "provider_describe"
	provider.claim.TencentMutationCount = 1
	provider.claim.KubernetesMutationCount = 0
	provider.claim.FailureStage = "cvm_final_readback"
	provider.claim.ProviderErrorClass = "readback_mismatch"
	provider.claim.Evidence = &ComputeClaimEvidence{
		CVM:  ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{marker}},
		Node: ComputeClaimMutationEvidence{},
	}
	provider.claimErr = errors.New("provider readback failed")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}

	first, firstErr := service.ClaimComputeRecovery(context.Background(), claimInput)
	second, secondErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	operations, listErr := store.List(context.Background())
	encoded, marshalErr := json.Marshal(struct {
		First      ComputeClaimRecoveryProof
		Second     ComputeClaimRecoveryProof
		Operations []FabricOperation
	}{first, second, operations})

	if firstErr == nil || secondErr == nil || listErr != nil || marshalErr != nil || provider.claimCalls != 1 || provider.proofCalls != 2 ||
		strings.Contains(string(encoded), marker) {
		t.Fatalf("first=%#v firstErr=%v second=%#v secondErr=%v operations=%#v listErr=%v encoded=%s marshalErr=%v provider=%#v", first, firstErr, second, secondErr, operations, listErr, encoded, marshalErr, provider)
	}
}

func TestClaimComputeRecoveryObservedReplayReturnsPersistedEvidenceWhenProofIsUnavailable(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	provider.claim.Proof.Reason = "provider_describe"
	provider.claim.Proof.NodeOwnershipState = "unallocated"
	provider.claim.Proof.CVMOwnershipState = "recoverable"
	provider.claim.TencentMutationCount = 1
	provider.claim.KubernetesMutationCount = 0
	provider.claim.FailureStage = "cvm_tag_readback"
	provider.claim.ProviderErrorClass = "timeout"
	provider.claim.Evidence = &ComputeClaimEvidence{
		CVM:  ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"opl_workspace_id"}},
		Node: ComputeClaimMutationEvidence{},
	}
	provider.claimErr = errors.New("provider readback timed out")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}

	first, firstErr := service.ClaimComputeRecovery(context.Background(), claimInput)
	provider.proofErr = errors.New("provider proof unavailable")
	second, secondErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)

	if firstErr == nil || secondErr == nil || provider.claimCalls != 1 || provider.proofCalls != 2 ||
		second.Reason != first.Reason || second.TencentMutationCount != first.TencentMutationCount ||
		second.KubernetesMutationCount != first.KubernetesMutationCount || second.FailureStage != first.FailureStage ||
		second.ProviderErrorClass != first.ProviderErrorClass || !reflect.DeepEqual(second.Evidence, first.Evidence) {
		t.Fatalf("first=%#v firstErr=%v second=%#v secondErr=%v provider=%#v", first, firstErr, second, secondErr, provider)
	}
}

func TestClaimComputeRecoveryReservedReplayReturnsPersistedUnknownEvidenceWhenProofIsUnavailable(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
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
	reserved := pending
	reserved.RedactedProviderPayload = withComputeClaimRecoveryMutation(reserved.RedactedProviderPayload, reservedComputeClaimRecoveryMutation())
	if err := store.SaveComputeClaimRecovery(context.Background(), pending, reserved); err != nil {
		t.Fatal(err)
	}
	provider.proofErr = errors.New("provider proof unavailable")

	result, claimErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)

	if claimErr == nil || result.Eligible || result.Reason != "provider_describe" || result.TencentMutationCount != 5 ||
		result.KubernetesMutationCount != 1 || result.Evidence == nil || result.Evidence.CVM.Unknown != 5 || result.Evidence.Node.Unknown != 1 ||
		provider.proofCalls != 1 || provider.claimCalls != 0 {
		t.Fatalf("result=%#v err=%v provider=%#v", result, claimErr, provider)
	}
}

func TestClaimComputeRecoveryObservedSuccessFailsClosedWhenActivationFailedAndReadbackRegresses(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	activationStore := &failOnceComputeClaimActivationStore{OperationStore: store, fail: true}
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}

	first, firstErr := NewServiceWithOperationStore(provider, activationStore).ClaimComputeRecovery(context.Background(), claimInput)
	second, secondErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	ownership, ownershipErr := store.MachineOwnership(context.Background(), input.ComputeAllocationID)

	if firstErr == nil || secondErr == nil || first.TencentMutationCount != 1 || first.KubernetesMutationCount != 1 ||
		second.Eligible || second.Reason != "identity_mismatch" || second.FailureStage != "claim_final_readback" ||
		second.ProviderErrorClass != "readback_mismatch" || second.TencentMutationCount != 1 || second.KubernetesMutationCount != 1 ||
		second.Evidence == nil || second.Evidence.CVM.Confirmed != 1 || second.Evidence.Node.Confirmed != 1 ||
		ownershipErr != nil || ownership.Status != "quarantined" || provider.proofCalls != 2 || provider.claimCalls != 1 {
		t.Fatalf("first=%#v firstErr=%v second=%#v secondErr=%v ownership=%#v ownershipErr=%v provider=%#v", first, firstErr, second, secondErr, ownership, ownershipErr, provider)
	}
}

func TestClaimComputeRecoverySanitizesProviderFailureBeforeFirstResponseAndReplay(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	const marker = "provider_private_error_payload"
	provider.claim.Proof.Reason = marker
	provider.claim.Proof.NodeOwnershipState = "unallocated"
	provider.claim.Proof.CVMOwnershipState = "recoverable"
	provider.claim.TencentMutationCount = 1
	provider.claim.KubernetesMutationCount = 0
	provider.claim.FailureStage = marker
	provider.claim.ProviderErrorClass = marker
	provider.claim.Evidence = &ComputeClaimEvidence{
		CVM:  ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"opl_workspace_id"}},
		Node: ComputeClaimMutationEvidence{},
	}
	provider.claimErr = errors.New(marker)
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}

	first, firstErr := service.ClaimComputeRecovery(context.Background(), claimInput)
	second, secondErr := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	encoded, marshalErr := json.Marshal([]ComputeClaimRecoveryProof{first, second})

	if firstErr == nil || secondErr == nil || marshalErr != nil || provider.claimCalls != 1 || provider.proofCalls != 2 ||
		strings.Contains(string(encoded), marker) || !reflect.DeepEqual(first, second) {
		t.Fatalf("first=%#v firstErr=%v second=%#v secondErr=%v encoded=%s marshalErr=%v provider=%#v", first, firstErr, second, secondErr, encoded, marshalErr, provider)
	}
}

func TestClaimComputeRecoveryPersistsMutationReservationBeforeProviderCall(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "pro")
	provider.claimHook = func() {
		operations, err := store.List(context.Background())
		if err != nil || len(operations) != 1 {
			t.Fatalf("provider-call operation=%#v err=%v", operations, err)
		}
		reservation, ok := operations[0].RedactedProviderPayload["computeClaimRecoveryMutation"].(map[string]any)
		if !ok || reservation["state"] != "reserved" {
			t.Fatalf("provider called before persisted mutation reservation: payload=%#v", operations[0].RedactedProviderPayload)
		}
	}
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}

	if _, err := service.ClaimComputeRecovery(context.Background(), claimInput); err != nil {
		t.Fatal(err)
	}
}

func TestClaimComputeRecoveryReservedOutcomeStaysUnknownAfterTargetOwnedReadback(t *testing.T) {
	_, store, provider, input := seedComputeClaimRecovery(t, "basic")
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
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
	reserved := pending
	reserved.RedactedProviderPayload = withComputeClaimRecoveryMutation(reserved.RedactedProviderPayload, reservedComputeClaimRecoveryMutation())
	if err := store.SaveComputeClaimRecovery(context.Background(), pending, reserved); err != nil {
		t.Fatal(err)
	}
	provider.proof.NodeOwnershipState = "target_owned"
	provider.proof.CVMOwnershipState = "target_owned"

	result, err := NewServiceWithOperationStore(provider, store).ClaimComputeRecovery(context.Background(), claimInput)
	ownership, ownershipErr := store.MachineOwnership(context.Background(), input.ComputeAllocationID)
	stored, listErr := store.List(context.Background())

	if err == nil || result.Eligible || result.Reason != "provider_describe" || result.TencentMutationCount != 5 ||
		result.KubernetesMutationCount != 1 || result.Evidence == nil || result.Evidence.CVM.Unknown != 5 || result.Evidence.Node.Unknown != 1 ||
		ownershipErr != nil || ownership.Status != "quarantined" || listErr != nil || len(stored) != 1 || stored[0].Status != "claim_pending" ||
		provider.proofCalls != 1 || provider.claimCalls != 0 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
		t.Fatalf("result=%#v err=%v ownership=%#v ownershipErr=%v stored=%#v listErr=%v provider=%#v", result, err, ownership, ownershipErr, stored, listErr, provider)
	}
}

func TestClaimComputeRecoveryFailsClosedWhenMutationReadbackIsUnknown(t *testing.T) {
	service, store, provider, input := seedComputeClaimRecovery(t, "basic")
	provider.claim.Evidence = &ComputeClaimEvidence{
		CVM:  ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"opl_workspace_id"}},
		Node: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
	}
	provider.claim.FailureStage = "cvm_tag_readback"
	provider.claim.ProviderErrorClass = "timeout"

	result, err := service.ClaimComputeRecovery(context.Background(), ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: "machine-after", NodeName: "10.0.0.18", CVMInstanceID: "ins-fixture",
		PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	})
	ownership, _ := store.MachineOwnership(context.Background(), input.ComputeAllocationID)
	operations, _ := store.List(context.Background())
	if err == nil || result.Eligible || ownership.Status != "quarantined" || result.FailureStage != "cvm_tag_readback" ||
		result.ProviderErrorClass != "timeout" || result.Evidence == nil || result.Evidence.CVM.Unknown != 1 || len(result.Evidence.CVM.Missing) != 1 ||
		provider.claimCalls != 1 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCalls != 0 {
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
		IdempotencyKey: input.LaunchOperationID + ":compute",
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
