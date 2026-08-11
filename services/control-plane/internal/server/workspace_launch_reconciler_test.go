package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/domain"
)

type workspaceLaunchUnitStore struct {
	mu  sync.Mutex
	row map[string]any
}

func (s *workspaceLaunchUnitStore) GetRuntimeOperation(_ context.Context, id string) (map[string]any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row == nil || stringValue(s.row["id"]) != id {
		return nil, false, nil
	}
	return cloneMap(s.row), true, nil
}

func (s *workspaceLaunchUnitStore) ClaimWorkspaceLaunchReconcile(_ context.Context, claim workspaceLaunchReconcileClaim) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row != nil {
		return errWorkspaceLaunchCASConflict
	}
	s.row = cloneMap(claim.DesiredOperation)
	return nil
}

func (s *workspaceLaunchUnitStore) PersistWorkspaceLaunchReconcile(_ context.Context, update workspaceLaunchReconcileCAS) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row == nil || stringValue(s.row["result"]) != update.ExpectedOperationResult {
		return errWorkspaceLaunchCASConflict
	}
	s.row = cloneMap(update.DesiredOperation)
	return nil
}

type workspaceLaunchUnitAdapter struct {
	mu               sync.Mutex
	readyStages      map[string]bool
	unknownStages    map[string]bool
	readErrors       map[string]error
	reads            int
	mutations        int
	mutationsByStage map[string]int
	mutationBlocked  bool
	barrier          chan struct{}
}

func (a *workspaceLaunchUnitAdapter) ReadStage(_ context.Context, operation workspaceLaunchReconcileOperation) (workspaceLaunchStageObservation, error) {
	a.mu.Lock()
	a.reads++
	if err := a.readErrors[operation.Stage]; err != nil {
		a.mu.Unlock()
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, err
	}
	if a.unknownStages[operation.Stage] {
		a.mu.Unlock()
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, nil
	}
	if a.readyStages[operation.Stage] {
		a.mu.Unlock()
		return workspaceLaunchStageObservation{State: workspaceLaunchStageReady, Facts: workspaceLaunchReadyFacts(operation.Stage)}, nil
	}
	if a.barrier != nil && a.reads == 2 {
		close(a.barrier)
	}
	barrier := a.barrier
	a.mu.Unlock()
	if barrier != nil {
		<-barrier
	}
	return workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}, nil
}

func (a *workspaceLaunchUnitAdapter) CanMutateStage(workspaceLaunchReconcileOperation) bool {
	return !a.mutationBlocked
}

func (a *workspaceLaunchUnitAdapter) MutateStage(_ context.Context, operation workspaceLaunchReconcileOperation, _ string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mutations++
	if a.readyStages == nil {
		a.readyStages = map[string]bool{}
	}
	if a.mutationsByStage == nil {
		a.mutationsByStage = map[string]int{}
	}
	a.readyStages[operation.Stage] = true
	a.mutationsByStage[operation.Stage]++
	return nil
}

func workspaceLaunchReadyFacts(stage string) map[string]any {
	switch stage {
	case "key":
		return map[string]any{"workspaceApiKeyId": int64(9), "workspaceKeyGroupId": int64(7), "workspaceKeyStatus": workspaceKeyCodexGroupBound, "workspaceKeyFingerprint": "sha256:" + strings.Repeat("a", 64)}
	case "debit":
		return map[string]any{"chargeAttempted": true, "chargeConfirmation": map[string]any{"status": "used"}, "preChargeBalanceUsdMicros": int64(100), "postChargeBalanceUsdMicros": int64(50), "postChargeBalanceKnown": true}
	default:
		return nil
	}
}

func workspaceLaunchUnitCommand() workspaceLaunchReconcileCreate {
	return workspaceLaunchReconcileCreate{
		OperationID: "workspace-launch-unit", RequestHash: strings.Repeat("a", 64), AccountID: "acct-unit", OwnerUserID: "usr-unit",
		Sub2APIUserID: 11, WorkspaceKeyGroupID: 7, WorkspaceID: "ws-unit", Name: "Unit", PackageID: "basic", StorageGB: 10,
		PriceVersion: pricingCatalogVersion, TotalChargeUSDMicros: 52_580_000, ProviderProfileRef: "profile-unit", PreflightBindingRef: "binding-unit",
		WorkspaceImageDigest: "repo.example/workspace@sha256:" + strings.Repeat("b", 64), PreChargeBalanceMicros: 100_000_000,
		CreatedAt: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC),
	}
}

func workspaceLaunchManualReviewRow(t *testing.T) map[string]any {
	t.Helper()
	operation, err := newWorkspaceLaunchReconcileOperation(workspaceLaunchUnitCommand())
	if err != nil {
		t.Fatal(err)
	}
	operation.Status = "manual_review"
	row, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestWorkspaceLaunchCreateAndResumeUseReconcile(t *testing.T) {
	createStore, createAdapter := &workspaceLaunchUnitStore{}, &workspaceLaunchUnitAdapter{}
	created, err := NewWorkspaceLaunchReconciler(createStore, createAdapter).Create(context.Background(), workspaceLaunchUnitCommand())
	if err != nil || created.Stage != "debit" || createAdapter.mutations != 1 {
		t.Fatalf("create operation=%s mutations=%d err=%v", workspaceLaunchReconcileResultSummary(created), createAdapter.mutations, err)
	}

	resumeStore, resumeAdapter := &workspaceLaunchUnitStore{row: workspaceLaunchManualReviewRow(t)}, &workspaceLaunchUnitAdapter{}
	resumed, err := NewWorkspaceLaunchReconciler(resumeStore, resumeAdapter).Resume(context.Background(), workspaceLaunchUnitCommand().OperationID, workspaceLaunchResumeAuthorization{
		AuthorizationID: "resume-unit", LaunchVersion: 1, AuthorizedStage: "key", AuthorizedBy: "usr-admin",
		AuthorizedAt: "2026-08-12T00:01:00Z", Reason: "authoritative readback approved", MutationBudget: 1,
	})
	if err != nil || resumed.Stage != "debit" || resumeAdapter.mutations != 1 || resumed.ResumeAuthorizationConsumedAt == "" {
		t.Fatalf("resume operation=%s mutations=%d err=%v", workspaceLaunchReconcileResultSummary(resumed), resumeAdapter.mutations, err)
	}
}

func TestWorkspaceLaunchCASAllowsOneMutationReservation(t *testing.T) {
	store := &workspaceLaunchUnitStore{row: workspaceLaunchManualReviewRow(t)}
	operation, err := decodeWorkspaceLaunchReconcileOperation(store.row)
	if err != nil {
		t.Fatal(err)
	}
	operation.Status = "pending"
	store.row, _ = workspaceLaunchReconcileOperationRow(operation)
	adapter := &workspaceLaunchUnitAdapter{barrier: make(chan struct{})}
	reconciler := NewWorkspaceLaunchReconciler(store, adapter)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := reconciler.Reconcile(context.Background(), operation.ID)
			results <- err
		}()
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, errWorkspaceLaunchCASConflict):
			conflicts++
		default:
			t.Fatalf("unexpected reconcile error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 || adapter.mutations != 1 {
		t.Fatalf("successes=%d conflicts=%d mutations=%d", successes, conflicts, adapter.mutations)
	}
}

func TestWorkspaceLaunchPreAttemptReadFailureRemainsPending(t *testing.T) {
	row := workspaceLaunchManualReviewRow(t)
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	operation.Status = "pending"
	row, err = workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	store := &workspaceLaunchUnitStore{row: row}
	adapter := &workspaceLaunchUnitAdapter{readErrors: map[string]error{"key": errors.New("transient read failure")}}
	persistedBefore := stringValue(row["result"])

	got, err := NewWorkspaceLaunchReconciler(store, adapter).Reconcile(context.Background(), operation.ID)
	if err != nil || got.Status != "pending" || got.Stage != "key" || got.Attempts["key"].Attempted != 0 ||
		got.ResumeAuthorization != nil || adapter.mutations != 0 || stringValue(store.row["result"]) != persistedBefore {
		t.Fatalf("pre-attempt read failure changed launch: operation=%s mutations=%d err=%v", workspaceLaunchReconcileResultSummary(got), adapter.mutations, err)
	}
}

func TestWorkspaceLaunchAuthorizedMutationWaitsForCapableCaller(t *testing.T) {
	store := &workspaceLaunchUnitStore{row: workspaceLaunchManualReviewRow(t)}
	adapter := &workspaceLaunchUnitAdapter{mutationBlocked: true}
	reconciler := NewWorkspaceLaunchReconciler(store, adapter)
	authorization := workspaceLaunchResumeAuthorization{
		AuthorizationID: "resume-caller-credential", LaunchVersion: 1, AuthorizedStage: "key", AuthorizedBy: "usr-admin",
		AuthorizedAt: "2026-08-12T00:01:00Z", Reason: "bounded retry", MutationBudget: 1,
	}

	waiting, err := reconciler.Resume(context.Background(), workspaceLaunchUnitCommand().OperationID, authorization)
	if err != nil || waiting.Status != "pending" || waiting.Stage != "key" || waiting.Attempts["key"].Attempted != 0 ||
		waiting.ResumeAuthorization == nil || *waiting.ResumeAuthorization != authorization || waiting.ResumeAuthorizationConsumedAt != "" || adapter.mutations != 0 {
		t.Fatalf("blocked caller consumed authorization: operation=%s mutations=%d err=%v", workspaceLaunchReconcileResultSummary(waiting), adapter.mutations, err)
	}

	adapter.mutationBlocked = false
	continued, err := reconciler.Reconcile(context.Background(), waiting.ID)
	if err != nil || continued.Status != "pending" || continued.Stage != "debit" || continued.Attempts["key"].Attempted != 1 ||
		continued.ResumeAuthorization == nil || *continued.ResumeAuthorization != authorization || continued.ResumeAuthorizationConsumedAt == "" || adapter.mutations != 1 {
		t.Fatalf("capable caller did not continue launch: operation=%s mutations=%d err=%v", workspaceLaunchReconcileResultSummary(continued), adapter.mutations, err)
	}
}

func TestWorkspaceLaunchResumeAuthorizationIsImmutable(t *testing.T) {
	store, adapter := &workspaceLaunchUnitStore{row: workspaceLaunchManualReviewRow(t)}, &workspaceLaunchUnitAdapter{}
	reconciler := NewWorkspaceLaunchReconciler(store, adapter)
	authorization := workspaceLaunchResumeAuthorization{
		AuthorizationID: "resume-unit", LaunchVersion: 1, AuthorizedStage: "key", AuthorizedBy: "usr-admin",
		AuthorizedAt: "2026-08-12T00:01:00Z", Reason: "bounded retry", MutationBudget: 1,
	}
	first, err := reconciler.Resume(context.Background(), workspaceLaunchUnitCommand().OperationID, authorization)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reconciler.Resume(context.Background(), workspaceLaunchUnitCommand().OperationID, authorization)
	if err != nil || second.ID != first.ID || second.stringFact("workspaceId") != first.stringFact("workspaceId") || second.Attempts["key"].Max != 1 {
		t.Fatalf("idempotent resume changed launch: first=%s second=%s err=%v", workspaceLaunchReconcileResultSummary(first), workspaceLaunchReconcileResultSummary(second), err)
	}
	drifted := authorization
	drifted.Reason = "different reason"
	if _, err := reconciler.Resume(context.Background(), workspaceLaunchUnitCommand().OperationID, drifted); !errors.Is(err, errWorkspaceLaunchGrantConflict) {
		t.Fatalf("drifted authorization error=%v", err)
	}
}

func TestWorkspaceLaunchResumeAuthorizationsRotateAcrossStages(t *testing.T) {
	store := &workspaceLaunchUnitStore{row: workspaceLaunchManualReviewRow(t)}
	adapter := &workspaceLaunchUnitAdapter{unknownStages: map[string]bool{}}
	reconciler := NewWorkspaceLaunchReconciler(store, adapter)
	stageA := workspaceLaunchResumeAuthorization{
		AuthorizationID: "resume-stage-a", LaunchVersion: 1, AuthorizedStage: "key", AuthorizedBy: "usr-admin-a",
		AuthorizedAt: "2026-08-12T00:01:00Z", Reason: "stage A reviewed", MutationBudget: 1,
	}
	afterA, err := reconciler.Resume(context.Background(), workspaceLaunchUnitCommand().OperationID, stageA)
	if err != nil || afterA.Stage != "debit" || afterA.ResumeAuthorization == nil || *afterA.ResumeAuthorization != stageA || afterA.ResumeAuthorizationConsumedAt == "" {
		t.Fatalf("stage A resume=%s err=%v", workspaceLaunchReconcileResultSummary(afterA), err)
	}

	adapter.unknownStages["debit"] = true
	reviewed, err := reconciler.Reconcile(context.Background(), afterA.ID)
	if err != nil || reviewed.Status != "manual_review" || reviewed.Stage != "debit" || reviewed.Attempts["debit"].Attempted != 0 {
		t.Fatalf("stage B review=%s err=%v", workspaceLaunchReconcileResultSummary(reviewed), err)
	}
	stageB := workspaceLaunchResumeAuthorization{
		AuthorizationID: "resume-stage-b", LaunchVersion: reviewed.Version, AuthorizedStage: reviewed.Stage, AuthorizedBy: "usr-admin-b",
		AuthorizedAt: "2026-08-12T00:02:00Z", Reason: "stage B reviewed", MutationBudget: 1,
	}
	adapter.unknownStages["debit"] = false
	afterB, err := reconciler.Resume(context.Background(), reviewed.ID, stageB)
	if err != nil || afterB.Stage != "ensure_compute_allocation" || afterB.ResumeAuthorization == nil || *afterB.ResumeAuthorization != stageB || afterB.ResumeAuthorizationConsumedAt == "" ||
		len(afterB.ConsumedResumeAuthorizations) != 1 || afterB.ConsumedResumeAuthorizations[0].Authorization != stageA || afterB.ConsumedResumeAuthorizations[0].ConsumedAt == "" ||
		adapter.mutationsByStage["key"] != 1 || adapter.mutationsByStage["debit"] != 1 {
		t.Fatalf("stage B resume=%s history=%#v mutations=%#v err=%v", workspaceLaunchReconcileResultSummary(afterB), afterB.ConsumedResumeAuthorizations, adapter.mutationsByStage, err)
	}

	persistedBefore := stringValue(store.row["result"])
	readsBefore, mutationsBefore := adapter.reads, adapter.mutations
	for name, authorization := range map[string]workspaceLaunchResumeAuthorization{"stage A": stageA, "stage B": stageB} {
		got, retryErr := reconciler.Resume(context.Background(), afterB.ID, authorization)
		if retryErr != nil || got.ResumeAuthorization == nil || *got.ResumeAuthorization != stageB || len(got.ConsumedResumeAuthorizations) != 1 {
			t.Fatalf("%s exact retry changed authorization: operation=%s err=%v", name, workspaceLaunchReconcileResultSummary(got), retryErr)
		}
	}
	if adapter.reads != readsBefore || adapter.mutations != mutationsBefore || stringValue(store.row["result"]) != persistedBefore {
		t.Fatalf("exact retry caused work: reads=%d/%d mutations=%d/%d", adapter.reads, readsBefore, adapter.mutations, mutationsBefore)
	}

	drifts := []struct {
		name   string
		mutate func(*workspaceLaunchResumeAuthorization)
	}{
		{name: "authorization ID", mutate: func(value *workspaceLaunchResumeAuthorization) { value.AuthorizationID += "-changed" }},
		{name: "launch version", mutate: func(value *workspaceLaunchResumeAuthorization) { value.LaunchVersion++ }},
		{name: "stage", mutate: func(value *workspaceLaunchResumeAuthorization) { value.AuthorizedStage = "storage" }},
		{name: "reviewer", mutate: func(value *workspaceLaunchResumeAuthorization) { value.AuthorizedBy += "-changed" }},
		{name: "time", mutate: func(value *workspaceLaunchResumeAuthorization) { value.AuthorizedAt = "2026-08-12T00:03:00Z" }},
		{name: "reason", mutate: func(value *workspaceLaunchResumeAuthorization) { value.Reason += " changed" }},
		{name: "budget", mutate: func(value *workspaceLaunchResumeAuthorization) { value.MutationBudget = 0 }},
	}
	for stageName, authorization := range map[string]workspaceLaunchResumeAuthorization{"stage A": stageA, "stage B": stageB} {
		for _, drift := range drifts {
			drifted := authorization
			drift.mutate(&drifted)
			if _, driftErr := reconciler.Resume(context.Background(), afterB.ID, drifted); !errors.Is(driftErr, errWorkspaceLaunchGrantConflict) {
				t.Fatalf("%s %s drift error=%v", stageName, drift.name, driftErr)
			}
		}
	}
	if adapter.reads != readsBefore || adapter.mutations != mutationsBefore || stringValue(store.row["result"]) != persistedBefore {
		t.Fatalf("authorization drift changed launch state")
	}
}

func TestWorkspaceLaunchResumeWrapperReusesAuthorizedAtOnExactRetry(t *testing.T) {
	row := workspaceLaunchManualReviewRow(t)
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	authorization := workspaceLaunchResumeAuthorization{
		AuthorizationID: "resume-route-unit", LaunchVersion: 1, AuthorizedStage: "key", AuthorizedBy: "usr-admin",
		AuthorizedAt: "2026-08-12T00:01:00Z", Reason: "bounded retry", MutationBudget: 1,
	}
	current := workspaceLaunchResumeAuthorization{
		AuthorizationID: "resume-route-current", LaunchVersion: 3, AuthorizedStage: "debit", AuthorizedBy: "usr-admin-current",
		AuthorizedAt: "2026-08-12T00:02:00Z", Reason: "current bounded retry", MutationBudget: 1,
	}
	operation.Version = 4
	operation.Stage = "ensure_compute_allocation"
	operation.Status = "pending"
	operation.ConsumedResumeAuthorizations = []workspaceLaunchConsumedResumeAuthorization{{Authorization: authorization, ConsumedAt: "2026-08-12T00:01:30Z"}}
	operation.ResumeAuthorization = &current
	operation.ResumeAuthorizationConsumedAt = "2026-08-12T00:02:00Z"
	row, err = workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryTableStore()
	store.runtimeOps = []map[string]any{row}
	app := &controlPlaneServer{tables: store}
	retry := authorization
	retry.AuthorizedAt = ""
	persistedBefore := stringValue(row["result"])
	got, err := app.resumeWorkspaceLaunch(context.Background(), nil, operation.ID, retry)
	if err != nil || got.ResumeAuthorization == nil || *got.ResumeAuthorization != current || len(got.ConsumedResumeAuthorizations) != 1 ||
		got.ConsumedResumeAuthorizations[0].Authorization != authorization || stringValue(store.runtimeOps[0]["result"]) != persistedBefore {
		t.Fatalf("exact retry changed authorization: operation=%s err=%v", workspaceLaunchReconcileResultSummary(got), err)
	}
}

func TestWorkspaceLaunchLedgerEvidenceCannotAuthorizeContinuation(t *testing.T) {
	row := workspaceLaunchManualReviewRow(t)
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	operation.raw["receiptId"] = json.RawMessage(`"receipt-evidence"`)
	operation.raw["continuationRef"] = json.RawMessage(`"ledger-continuation"`)
	row, err = workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	store, adapter := &workspaceLaunchUnitStore{row: row}, &workspaceLaunchUnitAdapter{}
	got, err := NewWorkspaceLaunchReconciler(store, adapter).Reconcile(context.Background(), operation.ID)
	if err != nil || got.Status != "manual_review" || got.ResumeAuthorization != nil || adapter.reads != 0 || adapter.mutations != 0 {
		t.Fatalf("ledger evidence continued launch: operation=%s reads=%d mutations=%d err=%v", workspaceLaunchReconcileResultSummary(got), adapter.reads, adapter.mutations, err)
	}
}

func TestWorkspaceLaunchFabricBindingDriftBecomesUnknown(t *testing.T) {
	operation, err := newWorkspaceLaunchReconcileOperation(workspaceLaunchUnitCommand())
	if err != nil {
		t.Fatal(err)
	}
	operation.Stage = "storage"
	input := clients.WorkspaceLaunchStageInput{Binding: clients.WorkspaceLaunchStageBinding{SchemaVersion: 1, LaunchOperationID: operation.ID, Stage: "storage"}}
	result := clients.WorkspaceLaunchStageResult{SchemaVersion: 1, State: workspaceLaunchStageReady, Binding: input.Binding, Resources: clients.WorkspaceLaunchResources{StorageID: "storage-unit", StorageBindingRef: "binding-a"}}
	result.Binding.RequestHash = "drifted"
	observation, err := workspaceLaunchFabricObservation(operation, input, result)
	if err != nil || observation.State != workspaceLaunchStageUnknown {
		t.Fatalf("binding drift observation=%#v err=%v", observation, err)
	}
}

func TestWorkspaceLaunchFiveFabricStageCallersUseCanonicalHashPayload(t *testing.T) {
	operation, err := newWorkspaceLaunchReconcileOperation(workspaceLaunchUnitCommand())
	if err != nil {
		t.Fatal(err)
	}
	operation.raw["computeAllocationId"] = json.RawMessage(`"compute-unit"`)
	operation.raw["computeBindingRef"] = json.RawMessage(`"binding-compute"`)
	operation.raw["storageId"] = json.RawMessage(`"storage-unit"`)
	operation.raw["storageBindingRef"] = json.RawMessage(`"binding-storage"`)
	operation.raw["attachmentId"] = json.RawMessage(`"attachment-unit"`)
	operation.raw["attachmentBindingRef"] = json.RawMessage(`"binding-attachment"`)
	operation.raw["gatewaySecretRef"] = json.RawMessage(`"secret-unit"`)
	operation.raw["gatewaySecretVersion"] = json.RawMessage(`"version-unit"`)
	operation.raw["secretBindingRef"] = json.RawMessage(`"binding-secret"`)
	operation.raw["runtimeId"] = json.RawMessage(`"runtime-unit"`)
	operation.raw["runtimeBindingRef"] = json.RawMessage(`"binding-runtime"`)

	stages := []struct {
		stage, action, expectedBinding string
	}{
		{"ensure_compute_allocation", "ensure_compute_allocation", "binding-compute"},
		{"storage", "ensure_storage", "binding-storage"},
		{"attachment", "ensure_attachment", "binding-attachment"},
		{"secret", "ensure_gateway_secret", "binding-secret"},
		{"runtime", "ensure_runtime", "binding-runtime"},
	}
	for _, stage := range stages {
		t.Run(stage.stage, func(t *testing.T) {
			current := operation
			current.Stage = stage.stage
			input, err := (&controlPlaneWorkspaceLaunchStageAdapter{}).workspaceLaunchFabricStageInput(context.Background(), current, false)
			if err != nil {
				t.Fatal(err)
			}
			launchRequestHash := current.stringFact("requestHash")
			if input.ProviderProfileRef != current.stringFact("providerProfileRef") || input.PreflightBindingRef != current.stringFact("preflightBindingRef") ||
				input.Binding.FabricOperationID != current.ID+":"+stage.stage || input.Binding.LaunchOperationID != current.ID ||
				input.Binding.AccountID != current.stringFact("accountId") || input.Binding.WorkspaceID != current.stringFact("workspaceId") ||
				input.Binding.Stage != stage.stage || input.Binding.Action != stage.action || input.Binding.IdempotencyKey != workspaceLaunchStageIdempotencyKey(current, 1) ||
				input.Binding.RequestHash != workspaceLaunchFabricRequestHash(input, launchRequestHash) || input.Binding.ExpectedResourceBinding != stage.expectedBinding {
				t.Fatalf("incomplete explicit Fabric stage input=%#v", input)
			}

			if workspaceLaunchFabricRequestHash(input, strings.Repeat("f", 64)) == input.Binding.RequestHash {
				t.Fatal("launch request is not bound by stage request hash")
			}
			includedMutations := map[string]func(*clients.WorkspaceLaunchStageInput){
				"action":    func(changed *clients.WorkspaceLaunchStageInput) { changed.Binding.Action += "-changed" },
				"package":   func(changed *clients.WorkspaceLaunchStageInput) { changed.PackageID += "-changed" },
				"size":      func(changed *clients.WorkspaceLaunchStageInput) { changed.SizeGB += 10 },
				"image":     func(changed *clients.WorkspaceLaunchStageInput) { changed.WorkspaceImageDigest += "-changed" },
				"resources": func(changed *clients.WorkspaceLaunchStageInput) { changed.Resources.RuntimeURL += "-changed" },
			}
			for name, mutate := range includedMutations {
				changed := input
				mutate(&changed)
				if workspaceLaunchFabricRequestHash(changed, launchRequestHash) == input.Binding.RequestHash {
					t.Fatalf("%s is not bound by stage request hash", name)
				}
			}

			excluded := input
			excluded.ProviderProfileRef += "-changed"
			excluded.PreflightBindingRef += "-changed"
			excluded.GatewayCredential = &clients.WorkspaceLaunchGatewayCredential{KeyID: 9, Value: "credential-value"}
			excluded.Binding.SchemaVersion++
			excluded.Binding.LaunchOperationID += "-changed"
			excluded.Binding.AccountID += "-changed"
			excluded.Binding.WorkspaceID += "-changed"
			excluded.Binding.Stage += "-changed"
			excluded.Binding.FabricOperationID += "-changed"
			excluded.Binding.IdempotencyKey += "-changed"
			excluded.Binding.RequestHash += "-changed"
			excluded.Binding.ExpectedResourceBinding += "-changed"
			if workspaceLaunchFabricRequestHash(excluded, launchRequestHash) != input.Binding.RequestHash {
				t.Fatal("stage request hash included independently validated identity or transient credential")
			}
		})
	}
}

func TestWorkspaceLaunchFabricRequestHashMatchesContractGoldenVectors(t *testing.T) {
	contractPath := filepath.Join("..", "..", "..", "..", "packages", "contracts", "opl-cloud-fabric-launch-binding-contract.json")
	contractJSON, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		StageRequestHash struct {
			GoldenVectors []struct {
				Stage   string `json:"stage"`
				Payload struct {
					LaunchRequestHash string                           `json:"launchRequestHash"`
					Action            string                           `json:"action"`
					PackageID         string                           `json:"packageId"`
					SizeGB            int                              `json:"sizeGb"`
					ImageDigest       string                           `json:"imageDigest"`
					Resources         clients.WorkspaceLaunchResources `json:"resources"`
				} `json:"payload"`
				SHA256 string `json:"sha256"`
			} `json:"goldenVectors"`
		} `json:"stageRequestHash"`
	}
	if err := json.Unmarshal(contractJSON, &contract); err != nil {
		t.Fatal(err)
	}
	expectedStages := map[string]bool{
		"ensure_compute_allocation": false,
		"storage":                   false,
		"attachment":                false,
		"secret":                    false,
		"runtime":                   false,
	}
	if len(contract.StageRequestHash.GoldenVectors) != len(expectedStages) {
		t.Fatalf("golden vector count=%d", len(contract.StageRequestHash.GoldenVectors))
	}
	for _, vector := range contract.StageRequestHash.GoldenVectors {
		seen, ok := expectedStages[vector.Stage]
		if !ok || seen {
			t.Fatalf("unexpected or duplicate golden vector stage=%q", vector.Stage)
		}
		expectedStages[vector.Stage] = true
		input := clients.WorkspaceLaunchStageInput{
			Binding:   clients.WorkspaceLaunchStageBinding{Stage: vector.Stage, Action: vector.Payload.Action},
			PackageID: vector.Payload.PackageID, SizeGB: vector.Payload.SizeGB,
			WorkspaceImageDigest: vector.Payload.ImageDigest, Resources: vector.Payload.Resources,
		}
		if vector.Stage == "secret" {
			input.GatewayCredential = &clients.WorkspaceLaunchGatewayCredential{KeyID: 9, Value: "transient-secret"}
		}
		if got := workspaceLaunchFabricRequestHash(input, vector.Payload.LaunchRequestHash); got != vector.SHA256 {
			t.Fatalf("stage=%s hash=%s want=%s", vector.Stage, got, vector.SHA256)
		}
	}
}

func TestWorkspaceLaunchFabricReadyWithoutRequiredFactsBecomesUnknown(t *testing.T) {
	operation, err := newWorkspaceLaunchReconcileOperation(workspaceLaunchUnitCommand())
	if err != nil {
		t.Fatal(err)
	}
	operation.Stage = "storage"
	input := clients.WorkspaceLaunchStageInput{Binding: clients.WorkspaceLaunchStageBinding{SchemaVersion: 1, LaunchOperationID: operation.ID, Stage: "storage"}}
	result := clients.WorkspaceLaunchStageResult{
		SchemaVersion: 1,
		State:         workspaceLaunchStageReady,
		Binding:       input.Binding,
		Resources:     clients.WorkspaceLaunchResources{StorageID: "storage-unit"},
	}
	observation, err := workspaceLaunchFabricObservation(operation, input, result)
	if err != nil || observation.State != workspaceLaunchStageUnknown {
		t.Fatalf("incomplete ready observation=%#v err=%v", observation, err)
	}
}

func TestWorkspaceLaunchActivationWritesProjectionWithoutFabricRows(t *testing.T) {
	store := newMemoryTableStore()
	store.users["usr-unit"] = map[string]any{"id": "usr-unit", "accountId": "acct-unit", "role": "owner", "status": "active"}
	quote, err := workspacePricingPreview(defaultPricingCatalog(), map[string]any{"packageId": "basic", "sizeGb": 10})
	if err != nil {
		t.Fatal(err)
	}
	computePrice, _ := requiredPositiveInteger(mapField(quote, "compute"), "chargeUsdMicros")
	storagePrice, _ := requiredPositiveInteger(mapField(quote, "storage"), "chargeUsdMicros")
	totalPrice, _ := requiredPositiveInteger(quote, "totalChargeUsdMicros")
	row := workspaceProjectionBillingRow(domain.WorkspaceProjection{
		ID: "ws-unit", AccountID: "acct-unit", OwnerID: "usr-unit", Name: "Unit", PackageID: "basic", Provider: "fabric",
		Status: "running", ComputeID: "compute-fabric", VolumeID: "storage-fabric", AttachmentID: "attachment-fabric",
		RuntimeID: "runtime-fabric", RuntimeServiceName: "runtime-service", RuntimeReady: true, URL: "https://workspace.example",
	}, map[string]any{
		"autoRenew": false, "authorizedBy": "", "authorizedAt": "", "packageId": "basic", "storageGb": 10,
		"priceVersion": pricingCatalogVersion, "currency": pricingCurrency, "billingUnit": pricingBillingUnit,
		"computeUsdMicros": computePrice, "storageUsdMicros": storagePrice, "totalUsdMicros": totalPrice,
		"periodStart": "2026-08-12T00:00:00Z", "paidThrough": "2026-09-12T00:00:00Z", "nextRenewalAt": "2026-09-11T00:00:00Z",
		"billingAnchorDay": 12, "renewalStatus": "active", "computeAllocationId": "compute-fabric", "storageId": "storage-fabric",
	})
	row["activatedAt"] = "2026-08-12T00:01:00Z"
	activated, err := store.ActivateWorkspaceLaunchProjection(context.Background(), row)
	if err != nil {
		t.Fatal(err)
	}
	if stringValue(activated["id"]) != "ws-unit" || activated["customerProduct"] != true || len(store.computes) != 0 || len(store.storages) != 0 || len(store.attachments) != 0 {
		t.Fatalf("activation copied Fabric truth: workspace=%#v computes=%d storages=%d attachments=%d", activated, len(store.computes), len(store.storages), len(store.attachments))
	}
	drifted := cloneMap(row)
	drifted["ownerAccountId"] = "acct-other"
	if _, err := store.ActivateWorkspaceLaunchProjection(context.Background(), drifted); !errors.Is(err, errWorkspaceActivationConflict) {
		t.Fatalf("owner drift error=%v", err)
	}
}

func TestCurrentWorkspaceImageDigestRequiresRepository(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	t.Setenv("OPL_WORKSPACE_IMAGE", "@"+digest)
	if got := currentWorkspaceImageDigest(); got != "" {
		t.Fatalf("empty repository image=%q", got)
	}
	t.Setenv("OPL_WORKSPACE_IMAGE", "registry.example/workspace@"+digest)
	if got := currentWorkspaceImageDigest(); got != "registry.example/workspace@"+digest {
		t.Fatalf("valid image=%q", got)
	}
}

func TestWorkspaceLaunchResumeAuthorizationDigestBindsImmutableAuthorization(t *testing.T) {
	authorization := workspaceLaunchResumeAuthorization{
		AuthorizationID: "resume-unit", LaunchVersion: 1, AuthorizedStage: "runtime", AuthorizedBy: "usr-admin",
		AuthorizedAt: "2026-08-12T00:01:00Z", Reason: "bounded retry", MutationBudget: 1,
	}
	first := workspaceLaunchResumeAuthorizationDigest(authorization)
	if !strings.HasPrefix(first, "sha256:") || first != workspaceLaunchResumeAuthorizationDigest(authorization) {
		t.Fatalf("unstable authorization digest=%q", first)
	}
	authorization.Reason = "different authorization"
	if second := workspaceLaunchResumeAuthorizationDigest(authorization); second == first {
		t.Fatalf("authorization drift retained digest=%q", second)
	}
}

func TestWorkspaceLaunchActiveSourceHasNoProviderReducer(t *testing.T) {
	files := []string{"workspace_launch.go", "workspace_launch_reconciler.go", "workspace_launch_service.go", "workspace_launch_fabric_stages.go", "workspace_launch_activation.go", "routes_workspace_launch.go"}
	for _, name := range files {
		source, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(source))
		for _, forbidden := range []string{"tencent", "nodepool", "machine", "cvm", "providerdata", "costtags", "fabricoperations", "listoperations"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("%s contains provider-owned token %q", name, forbidden)
			}
		}
	}
}
