package fabric

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func seedOperatorTerminalizationHead(t *testing.T, store OperationStore, provider *normalLaunchComputeProvider) (ComputeAllocationInput, ComputeAllocation, FabricOperation) {
	return seedOperatorTerminalizationHeadWithBinding(t, store, provider, nil)
}

func seedOperatorTerminalizationHeadWithBinding(t *testing.T, store OperationStore, provider *normalLaunchComputeProvider, mutateBinding func(*computeClaimRecoveryBinding)) (ComputeAllocationInput, ComputeAllocation, FabricOperation) {
	t.Helper()
	input, allocation := seedNormalWorkspaceComputeClaimPending(t, store, provider, "operator-terminalization")
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("seed operations=%#v err=%v", operations, err)
	}
	current := operations[0]
	plan, present := decodeComputeAllocationPlan(current)
	if !present {
		t.Fatal("seed allocation plan missing")
	}
	binding, valid := automaticComputeClaimRecoveryBinding(current, allocation, plan)
	if !valid {
		t.Fatal("seed recovery binding invalid")
	}
	if mutateBinding != nil {
		mutateBinding(&binding)
	}
	reserved := current
	reserved.RedactedProviderPayload = withComputeClaimRecoveryBinding(reserved.RedactedProviderPayload, binding)
	reserved.RedactedProviderPayload = withComputeClaimRecoveryMutation(reserved.RedactedProviderPayload, reservedComputeClaimRecoveryMutation())
	if err := store.SaveComputeClaimRecovery(context.Background(), current, reserved); err != nil {
		t.Fatal(err)
	}
	manual := reserved
	manual.RedactedProviderPayload = withComputeClaimRecoveryMutation(manual.RedactedProviderPayload, observedComputeClaimRecoveryMutation(ComputeClaimRecoveryProof{
		Reason: "provider_describe", TencentMutationCount: 5, KubernetesMutationCount: 1,
		FailureStage: "claim_final_readback", ProviderErrorClass: "readback_mismatch",
		Evidence: &ComputeClaimEvidence{
			CVM:  ComputeClaimMutationEvidence{Attempted: 5, Unknown: 5, Missing: []string{"instance_name", "opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id"}},
			Node: ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"node_ownership"}},
		},
	}))
	if err := store.SaveComputeClaimRecovery(context.Background(), reserved, manual); err != nil {
		t.Fatal(err)
	}
	return input, allocation, manual
}

func TestOperatorTerminalizesExactHistoricalBindingWithoutProviderMutation(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := &normalLaunchComputeProvider{}
	input, allocation, pending := seedOperatorTerminalizationHeadWithBinding(t, store, provider, func(binding *computeClaimRecoveryBinding) {
		binding.IdempotencyKey = "recovery-exec-14deb7f41022c8a5ae9d"
	})
	service := NewServiceWithOperationStore(provider, store)

	readback, err := service.ReadComputePoolHeadTerminalization(context.Background(), input.NodePoolID)
	if err != nil || readback.Status != "candidate" || readback.HeadStatus != "claim_pending" || readback.AllocationStatus != "compute_claim_pending" ||
		readback.OwnershipStatus != "quarantined" || len(readback.ApprovalDigest) != 64 {
		t.Fatalf("readback=%#v err=%v", readback, err)
	}
	originalBinding := pending.RedactedProviderPayload["computeClaimRecovery"]
	originalLedger := pending.RedactedProviderPayload[computeClaimRecoveryMutationPayloadKey]
	originalOwnership, err := store.MachineOwnership(context.Background(), allocation.ID)
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.TerminalizeComputePoolHead(context.Background(), ComputePoolHeadTerminalizationInput{
		NodePoolID: input.NodePoolID, ApprovalID: "historical-head-terminalize-30970000001",
		ApprovalDigest: readback.ApprovalDigest, IdempotencyKey: "historical-head-terminalize-30970000001",
	})
	if err != nil || result.Status != "succeeded" || result.TerminalStatus != "terminal_unprovable" ||
		result.Sub2APIMutationCount != 0 || result.TencentMutationCount != 0 || result.KubernetesMutationCount != 0 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].Status != "failed" ||
		!reflect.DeepEqual(operations[0].RedactedProviderPayload["computeClaimRecovery"], originalBinding) ||
		!reflect.DeepEqual(operations[0].RedactedProviderPayload[computeClaimRecoveryMutationPayloadKey], originalLedger) {
		t.Fatalf("operations=%#v err=%v", operations, err)
	}
	ownership, err := store.MachineOwnership(context.Background(), allocation.ID)
	if err != nil || !reflect.DeepEqual(ownership, originalOwnership) {
		t.Fatalf("ownership=%#v original=%#v err=%v", ownership, originalOwnership, err)
	}
	prepare, create, proof, cvm, node := provider.automaticContinuationCounts()
	if prepare != 0 || create != 0 || proof != 0 || cvm != 0 || node != 0 {
		t.Fatalf("provider calls=%d/%d/%d/%d/%d", prepare, create, proof, cvm, node)
	}
}

func TestOperatorTerminalizationRejectsBindingForAnotherLaunch(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := &normalLaunchComputeProvider{}
	input, _, _ := seedOperatorTerminalizationHeadWithBinding(t, store, provider, func(binding *computeClaimRecoveryBinding) {
		binding.LaunchOperationID = "workspace-launch-another"
	})
	service := NewServiceWithOperationStore(provider, store)

	if _, err := service.ReadComputePoolHeadTerminalization(context.Background(), input.NodePoolID); !errors.Is(err, ErrComputePoolHeadTerminalizationUnavailable) {
		t.Fatalf("error=%v", err)
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].Status != "claim_pending" {
		t.Fatalf("operations=%#v err=%v", operations, err)
	}
	prepare, create, proof, cvm, node := provider.automaticContinuationCounts()
	if prepare != 0 || create != 0 || proof != 0 || cvm != 0 || node != 0 {
		t.Fatalf("provider calls=%d/%d/%d/%d/%d", prepare, create, proof, cvm, node)
	}
}

func TestOperatorTerminalizesOnlyExactManualRecoveryPoolHeadWithoutProviderMutation(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := &normalLaunchComputeProvider{}
	input, allocation, pending := seedOperatorTerminalizationHead(t, store, provider)
	service := NewServiceWithOperationStore(provider, store)

	readback, err := service.ReadComputePoolHeadTerminalization(context.Background(), input.NodePoolID)
	if err != nil || readback.Status != "candidate" || readback.HeadStatus != "claim_pending" ||
		readback.AllocationStatus != "compute_claim_pending" || readback.OwnershipStatus != "quarantined" ||
		len(readback.ApprovalDigest) != 64 || len(readback.BindingDigest) != 64 || len(readback.ManualRecoveryLedgerDigest) != 64 ||
		readback.Sub2APIMutationCount != 0 || readback.TencentMutationCount != 0 || readback.KubernetesMutationCount != 0 {
		t.Fatalf("readback=%#v err=%v", readback, err)
	}
	originalBinding := pending.RedactedProviderPayload["computeClaimRecovery"]
	originalLedger := pending.RedactedProviderPayload[computeClaimRecoveryMutationPayloadKey]
	originalOwnership, err := store.MachineOwnership(context.Background(), allocation.ID)
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.TerminalizeComputePoolHead(context.Background(), ComputePoolHeadTerminalizationInput{
		NodePoolID: input.NodePoolID, ApprovalID: "fresh-head-terminalize-30970000001",
		ApprovalDigest: readback.ApprovalDigest, IdempotencyKey: "fresh-head-terminalize-30970000001",
	})
	if err != nil || result.Status != "succeeded" || result.TerminalStatus != "terminal_unprovable" ||
		result.Sub2APIMutationCount != 0 || result.TencentMutationCount != 0 || result.KubernetesMutationCount != 0 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].Status != "failed" || operations[0].ErrorCode == "" {
		t.Fatalf("operations=%#v err=%v", operations, err)
	}
	var terminalAllocation ComputeAllocation
	terminal, present, valid := decodeComputeClaimTerminalEvidence(operations[0])
	if !decodeOperationResource(operations[0], &terminalAllocation) || terminalAllocation.Status != "quarantined" ||
		!present || !valid || terminal.Status != "terminal_unprovable" || terminal.OperatorApprovalID != "fresh-head-terminalize-30970000001" ||
		terminal.OperatorApprovalDigest != readback.ApprovalDigest || terminal.OperatorIdempotencyKey != "fresh-head-terminalize-30970000001" ||
		!reflect.DeepEqual(operations[0].RedactedProviderPayload["computeClaimRecovery"], originalBinding) ||
		!reflect.DeepEqual(operations[0].RedactedProviderPayload[computeClaimRecoveryMutationPayloadKey], originalLedger) {
		t.Fatalf("terminal operation=%#v allocation=%#v evidence=%#v present=%v valid=%v", operations[0], terminalAllocation, terminal, present, valid)
	}
	ownership, err := store.MachineOwnership(context.Background(), allocation.ID)
	if err != nil || !reflect.DeepEqual(ownership, originalOwnership) {
		t.Fatalf("ownership=%#v original=%#v err=%v", ownership, originalOwnership, err)
	}
	prepare, create, proof, cvm, node := provider.automaticContinuationCounts()
	if prepare != 0 || create != 0 || proof != 0 || cvm != 0 || node != 0 {
		t.Fatalf("provider calls=%d/%d/%d/%d/%d", prepare, create, proof, cvm, node)
	}

	replayed, replayErr := service.TerminalizeComputePoolHead(context.Background(), ComputePoolHeadTerminalizationInput{
		NodePoolID: input.NodePoolID, ApprovalID: "fresh-head-terminalize-30970000001",
		ApprovalDigest: readback.ApprovalDigest, IdempotencyKey: "fresh-head-terminalize-30970000001",
	})
	if replayErr != nil || replayed.Status != "succeeded" || replayed.Replayed != true {
		t.Fatalf("replayed=%#v err=%v", replayed, replayErr)
	}
}

func TestOperatorTerminalizationFailsClosedOnApprovalBindingAndCASDrift(t *testing.T) {
	for name, mutate := range map[string]func(*ComputePoolHeadTerminalizationInput){
		"approval id":     func(input *ComputePoolHeadTerminalizationInput) { input.ApprovalID = "invalid approval" },
		"approval digest": func(input *ComputePoolHeadTerminalizationInput) { input.ApprovalDigest = string(make([]byte, 64)) },
		"idempotency key": func(input *ComputePoolHeadTerminalizationInput) { input.IdempotencyKey = "different-key" },
	} {
		t.Run(name, func(t *testing.T) {
			store := NewMemoryOperationStore()
			provider := &normalLaunchComputeProvider{}
			input, _, _ := seedOperatorTerminalizationHead(t, store, provider)
			service := NewServiceWithOperationStore(provider, store)
			readback, err := service.ReadComputePoolHeadTerminalization(context.Background(), input.NodePoolID)
			if err != nil {
				t.Fatal(err)
			}
			request := ComputePoolHeadTerminalizationInput{
				NodePoolID: input.NodePoolID, ApprovalID: "fresh-head-terminalize-30970000002",
				ApprovalDigest: readback.ApprovalDigest, IdempotencyKey: "fresh-head-terminalize-30970000002",
			}
			mutate(&request)
			if _, err := service.TerminalizeComputePoolHead(context.Background(), request); !errors.Is(err, ErrInvalidComputePoolHeadTerminalization) && !errors.Is(err, ErrComputePoolHeadTerminalizationConflict) {
				t.Fatalf("error=%v", err)
			}
			operations, listErr := store.List(context.Background())
			if listErr != nil || len(operations) != 1 || operations[0].Status != "claim_pending" {
				t.Fatalf("operations=%#v err=%v", operations, listErr)
			}
			prepare, create, proof, cvm, node := provider.automaticContinuationCounts()
			if prepare != 0 || create != 0 || proof != 0 || cvm != 0 || node != 0 {
				t.Fatalf("provider calls=%d/%d/%d/%d/%d", prepare, create, proof, cvm, node)
			}
		})
	}
}

func TestOperatorTerminalizationCASAllowsOneWriterAndReleasesFreshHead(t *testing.T) {
	store := NewMemoryOperationStore()
	provider := &normalLaunchComputeProvider{}
	input, _, _ := seedOperatorTerminalizationHead(t, store, provider)
	fresh := FabricOperation{
		ID: "fop-fresh", OperationID: "op-fresh", Action: "create_compute_allocation", ResourceKind: "compute_allocation", ResourceID: "ca-fresh",
		IdempotencyKey: "workspace-launch-fresh:compute", RequestHash: "hash-fresh", Status: "started", ComputePoolKey: input.NodePoolID,
	}
	if _, claimed, err := store.ClaimComputePoolRuntime(context.Background(), fresh); err != nil || !claimed {
		t.Fatalf("fresh seed claimed=%v err=%v", claimed, err)
	}
	service := NewServiceWithOperationStore(provider, store)
	readback, err := service.ReadComputePoolHeadTerminalization(context.Background(), input.NodePoolID)
	if err != nil {
		t.Fatal(err)
	}
	request := ComputePoolHeadTerminalizationInput{
		NodePoolID: input.NodePoolID, ApprovalID: "fresh-head-terminalize-30970000003",
		ApprovalDigest: readback.ApprovalDigest, IdempotencyKey: "fresh-head-terminalize-30970000003",
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := service.TerminalizeComputePoolHead(context.Background(), request)
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("terminalization error=%v", err)
		}
	}
	head, claimed, err := store.TryClaimComputePoolHead(context.Background(), fresh.ID, input.NodePoolID, "fresh-lease", time.Now().UTC(), time.Now().UTC().Add(time.Minute))
	if err != nil || !claimed || head.ID != fresh.ID {
		t.Fatalf("fresh head=%#v claimed=%v err=%v", head, claimed, err)
	}
}

func TestOperatorTerminalizationResultFailsClosedOnDuplicateOrUnknownApprovalEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*MemoryOperationStore)
		want   error
	}{
		{
			name: "duplicate approval identity",
			mutate: func(store *MemoryOperationStore) {
				duplicate := store.operation[0]
				duplicate.ID = "fop-terminal-duplicate"
				store.operation = append(store.operation, duplicate)
			},
			want: ErrComputePoolHeadTerminalizationConflict,
		},
		{
			name: "unknown approval evidence",
			mutate: func(store *MemoryOperationStore) {
				evidence := store.operation[0].RedactedProviderPayload[computeClaimTerminalEvidencePayloadKey].(map[string]any)
				evidence["schemaVersion"] = 0
			},
			want: ErrComputePoolHeadTerminalizationUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryOperationStore()
			provider := &normalLaunchComputeProvider{}
			input, _, _ := seedOperatorTerminalizationHead(t, store, provider)
			service := NewServiceWithOperationStore(provider, store)
			candidate, err := service.ReadComputePoolHeadTerminalization(context.Background(), input.NodePoolID)
			if err != nil {
				t.Fatal(err)
			}
			request := ComputePoolHeadTerminalizationInput{
				NodePoolID: input.NodePoolID, ApprovalID: "result-head-terminalize-30970000005",
				ApprovalDigest: candidate.ApprovalDigest, IdempotencyKey: "result-head-terminalize-30970000005",
			}
			if _, err := service.TerminalizeComputePoolHead(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			store.mu.Lock()
			test.mutate(store)
			store.mu.Unlock()

			if _, err := service.ReadComputePoolHeadTerminalizationResult(context.Background(), request); !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			prepare, create, proof, cvm, node := provider.automaticContinuationCounts()
			if prepare != 0 || create != 0 || proof != 0 || cvm != 0 || node != 0 {
				t.Fatalf("provider calls=%d/%d/%d/%d/%d", prepare, create, proof, cvm, node)
			}
		})
	}
}
