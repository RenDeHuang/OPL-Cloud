package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func disposableResetLaunchRow(t *testing.T) map[string]any {
	t.Helper()
	operation, err := newWorkspaceLaunchReconcileOperation(workspaceLaunchReconcileCreate{
		OperationID: "workspace-launch-disposable", RequestHash: strings.Repeat("a", 64), AccountID: "acct-disposable", OwnerUserID: "usr-disposable",
		Sub2APIUserID: 51, WorkspaceKeyGroupID: 52, WorkspaceID: "ws-disposable", Name: "Disposable", PackageID: "basic", StorageGB: 10,
		PriceVersion: "price-v1", TotalChargeUSDMicros: 1000000, ProviderProfileRef: "tencent-tke",
		PreflightBindingRef: "fabric-provider-binding:disposable", SpecDigest: strings.Repeat("b", 64),
		WorkspaceImageDigest: "registry.example/workspace@sha256:" + strings.Repeat("c", 64), PreChargeBalanceMicros: 2000000,
		CreatedAt: time.Date(2026, 8, 18, 1, 2, 3, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation.Version = 6
	operation.Stage, operation.Status = "debit", "manual_review"
	attempt := operation.Attempts["key"]
	attempt.Attempted, attempt.Confirmed, attempt.Status, attempt.IdempotencyKey = 1, 1, "confirmed", workspaceLaunchStageIdempotencyKey(operationWithStage(operation, "key"), 1)
	operation.Attempts["key"] = attempt
	operation.Observations["key"] = workspaceLaunchStageObservation{State: workspaceLaunchStageReady, Facts: workspaceLaunchReadyFacts("key")}
	row, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func eligibleDisposableResetFacts() workspaceLaunchDisposableResetFacts {
	return workspaceLaunchDisposableResetFacts{
		DisposableAuthority: true,
		WorkspaceProjection: workspaceLaunchDisposableOwnerAbsent,
		CompetingOperations: workspaceLaunchDisposableOwnerAbsent,
		PreflightBinding:    workspaceLaunchDisposableOwnerConfirmed,
		FabricStages:        workspaceLaunchDisposableOwnerAbsent,
		ProviderResources:   workspaceLaunchDisposableOwnerAbsent,
		WorkspaceRuntime:    workspaceLaunchDisposableOwnerAbsent,
		WorkspaceKey:        workspaceLaunchDisposableOwnerConfirmed,
		Debit:               workspaceLaunchDisposableOwnerConfirmed,
		LedgerReceipts:      workspaceLaunchDisposableOwnerAbsent,
	}
}

func TestWorkspaceLaunchDisposableResetClassifiesOnlyExactAbandonedLaunch(t *testing.T) {
	row := disposableResetLaunchRow(t)
	classification, err := classifyWorkspaceLaunchDisposableReset(row, eligibleDisposableResetFacts())
	if err != nil {
		t.Fatal(err)
	}
	if classification.OperationID != "workspace-launch-disposable" || classification.Version != 6 || classification.Stage != "debit" || classification.Status != "manual_review" {
		t.Fatalf("classification=%#v", classification)
	}
	if len(classification.PlanSteps) != len(workspaceLaunchDisposableResetOrderedSteps) {
		t.Fatalf("steps=%v", classification.PlanSteps)
	}
	for i := range classification.PlanSteps {
		if classification.PlanSteps[i] != workspaceLaunchDisposableResetOrderedSteps[i] {
			t.Fatalf("steps=%v", classification.PlanSteps)
		}
	}
	if !workspaceLaunchDisposableResetDigestPattern.MatchString(classification.ResetPlanDigest) {
		t.Fatalf("digest=%q", classification.ResetPlanDigest)
	}
	preview := workspaceLaunchDisposableResetPreviewResponse(classification)
	encoded := string(mustJSON(preview))
	for _, secret := range []string{classification.OperationID, classification.AccountID, classification.WorkspaceID, classification.PreflightBindingRef} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("preview leaked %q: %s", secret, encoded)
		}
	}
}

func TestWorkspaceLaunchDisposableResetRejectsIneligibleClassification(t *testing.T) {
	tests := map[string]func(map[string]any, *workspaceLaunchDisposableResetFacts){
		"wrong action": func(row map[string]any, _ *workspaceLaunchDisposableResetFacts) { row["action"] = "workspace.launch" },
		"wrong stage": func(row map[string]any, _ *workspaceLaunchDisposableResetFacts) {
			mutateDisposableResetResult(t, row, "stage", "key")
		},
		"wrong status": func(row map[string]any, _ *workspaceLaunchDisposableResetFacts) { row["status"] = "pending" },
		"wrong schema": func(row map[string]any, _ *workspaceLaunchDisposableResetFacts) {
			mutateDisposableResetResult(t, row, "schemaVersion", 2)
		},
		"invalid version": func(row map[string]any, _ *workspaceLaunchDisposableResetFacts) {
			mutateDisposableResetResult(t, row, "version", 0)
		},
		"workspace exists": func(_ map[string]any, facts *workspaceLaunchDisposableResetFacts) {
			facts.WorkspaceProjection = workspaceLaunchDisposableOwnerConfirmed
		},
		"competing operation": func(_ map[string]any, facts *workspaceLaunchDisposableResetFacts) {
			facts.CompetingOperations = workspaceLaunchDisposableOwnerConfirmed
		},
		"invalid canonical identity": func(row map[string]any, _ *workspaceLaunchDisposableResetFacts) { row["accountId"] = "acct-other" },
		"authority absent":           func(_ map[string]any, facts *workspaceLaunchDisposableResetFacts) { facts.DisposableAuthority = false },
		"owner unknown": func(_ map[string]any, facts *workspaceLaunchDisposableResetFacts) {
			facts.Debit = workspaceLaunchDisposableOwnerUnknown
		},
		"owner conflict": func(_ map[string]any, facts *workspaceLaunchDisposableResetFacts) {
			facts.ProviderResources = workspaceLaunchDisposableOwnerConflict
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			row := disposableResetLaunchRow(t)
			facts := eligibleDisposableResetFacts()
			mutate(row, &facts)
			if _, err := classifyWorkspaceLaunchDisposableReset(row, facts); err == nil {
				t.Fatal("eligible")
			}
		})
	}
}

func TestWorkspaceLaunchDisposableResetPlanDigestIsStableAndIdentityBound(t *testing.T) {
	row := disposableResetLaunchRow(t)
	facts := eligibleDisposableResetFacts()
	first, err := classifyWorkspaceLaunchDisposableReset(row, facts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := classifyWorkspaceLaunchDisposableReset(row, facts)
	if err != nil || first.ResetPlanDigest != second.ResetPlanDigest {
		t.Fatalf("first=%q second=%q err=%v", first.ResetPlanDigest, second.ResetPlanDigest, err)
	}
	changed := disposableResetLaunchRow(t)
	mutateDisposableResetResult(t, changed, "preflightBindingRef", "fabric-provider-binding:changed")
	third, err := classifyWorkspaceLaunchDisposableReset(changed, facts)
	if err != nil {
		t.Fatal(err)
	}
	if first.ResetPlanDigest == third.ResetPlanDigest {
		t.Fatal("digest does not bind canonical identity")
	}
	facts.Debit = workspaceLaunchDisposableOwnerAbsent
	fourth, err := classifyWorkspaceLaunchDisposableReset(row, facts)
	if err != nil || first.ResetPlanDigest == fourth.ResetPlanDigest {
		t.Fatalf("digest does not bind owner facts: %v", err)
	}
}

func TestWorkspaceLaunchDisposableResetTerminalEvidenceStrictDecode(t *testing.T) {
	row := disposableResetLaunchRow(t)
	classification, err := classifyWorkspaceLaunchDisposableReset(row, eligibleDisposableResetFacts())
	if err != nil {
		t.Fatal(err)
	}
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	operation.Version++
	operation.Status = "failed"
	operation.DisposableReset = &workspaceLaunchDisposableResetEvidence{
		SchemaVersion: 1, LaunchVersion: classification.Version, ResetPlanDigest: classification.ResetPlanDigest,
		AuthorityDigest: "sha256:" + strings.Repeat("d", 64), LedgerReceiptDigest: "sha256:" + strings.Repeat("e", 64),
		CompletedAt: "2026-08-22T08:00:00Z", MutationScopeMatchedPlan: true,
	}
	terminal, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeWorkspaceLaunchReconcileOperation(terminal)
	if err != nil || decoded.Status != "failed" || decoded.Stage != "debit" || decoded.DisposableReset == nil {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
	reconciler := NewWorkspaceLaunchReconciler(&disposableResetReadStore{row: terminal}, disposableResetNoopAdapter{})
	reconciled, err := reconciler.Reconcile(t.Context(), operation.ID)
	if err != nil || reconciled.Status != "failed" || reconciled.Version != operation.Version {
		t.Fatalf("terminal reconcile mutated operation: %#v err=%v", reconciled, err)
	}

	tests := map[string]func(map[string]json.RawMessage, map[string]any){
		"missing evidence": func(raw map[string]json.RawMessage, _ map[string]any) { delete(raw, "disposableReset") },
		"invalid digest": func(raw map[string]json.RawMessage, _ map[string]any) {
			mutateDisposableResetEvidence(t, raw, "resetPlanDigest", "bad")
		},
		"scope mismatch": func(raw map[string]json.RawMessage, _ map[string]any) {
			mutateDisposableResetEvidence(t, raw, "mutationScopeMatchedPlan", false)
		},
		"wrong terminal stage":      func(raw map[string]json.RawMessage, _ map[string]any) { raw["stage"] = json.RawMessage(`"key"`) },
		"evidence on manual review": func(_ map[string]json.RawMessage, row map[string]any) { row["status"] = "manual_review" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := cloneMap(terminal)
			var raw map[string]json.RawMessage
			if json.Unmarshal([]byte(stringValue(candidate["result"])), &raw) != nil {
				t.Fatal("decode result")
			}
			mutate(raw, candidate)
			encoded, _ := json.Marshal(raw)
			candidate["result"] = string(encoded)
			if _, err := decodeWorkspaceLaunchReconcileOperation(candidate); err == nil {
				t.Fatal("decoded invalid terminal reset")
			}
		})
	}
}

type disposableResetReadStore struct {
	row map[string]any
}

func (store *disposableResetReadStore) GetRuntimeOperation(_ context.Context, _ string) (map[string]any, bool, error) {
	return cloneMap(store.row), true, nil
}

func (*disposableResetReadStore) ClaimWorkspaceLaunchReconcile(context.Context, workspaceLaunchReconcileClaim) error {
	return errors.New("unexpected claim")
}

func (*disposableResetReadStore) PersistWorkspaceLaunchReconcile(context.Context, workspaceLaunchReconcileCAS) error {
	return errors.New("unexpected persist")
}

type disposableResetNoopAdapter struct{}

func (disposableResetNoopAdapter) ReadStage(context.Context, workspaceLaunchReconcileOperation) (workspaceLaunchStageObservation, error) {
	return workspaceLaunchStageObservation{}, errors.New("unexpected read")
}

func (disposableResetNoopAdapter) CanMutateStage(workspaceLaunchReconcileOperation) bool {
	return false
}
func (disposableResetNoopAdapter) CanReplayStage(workspaceLaunchReconcileOperation) bool {
	return false
}
func (disposableResetNoopAdapter) MutateStage(context.Context, workspaceLaunchReconcileOperation, string) error {
	return errors.New("unexpected mutation")
}

func mutateDisposableResetResult(t *testing.T, row map[string]any, field string, value any) {
	t.Helper()
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(stringValue(row["result"])), &raw) != nil {
		t.Fatal("decode result")
	}
	raw[field] = mustJSON(value)
	encoded, _ := json.Marshal(raw)
	row["result"] = string(encoded)
}

func mutateDisposableResetEvidence(t *testing.T, raw map[string]json.RawMessage, field string, value any) {
	t.Helper()
	var evidence map[string]json.RawMessage
	if json.Unmarshal(raw["disposableReset"], &evidence) != nil {
		t.Fatal("decode evidence")
	}
	evidence[field] = mustJSON(value)
	raw["disposableReset"] = mustJSON(evidence)
}
