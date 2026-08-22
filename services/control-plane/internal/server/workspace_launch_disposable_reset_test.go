package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
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
	wantSteps := []string{"workspace_key", "debit_compensation", "ledger_evidence", "launch_terminalization"}
	if len(classification.PlanSteps) != len(wantSteps) {
		t.Fatalf("steps=%v", classification.PlanSteps)
	}
	for i := range classification.PlanSteps {
		if classification.PlanSteps[i] != wantSteps[i] {
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

type disposableResetFabric struct {
	fakeFabricClient
	binding clients.WorkspaceLaunchPreflightBinding
	stages  map[string]clients.WorkspaceLaunchStageResult
	reads   int
	writes  int
}

func (f *disposableResetFabric) ReadWorkspaceLaunchPreflight(_ context.Context, input clients.WorkspaceLaunchPreflightReadInput) (clients.WorkspaceLaunchPreflightBinding, error) {
	f.reads++
	if input.ProviderBindingRef != f.binding.ProviderBindingRef {
		return clients.WorkspaceLaunchPreflightBinding{}, errors.New("binding mismatch")
	}
	return f.binding, nil
}

func (f *disposableResetFabric) PreflightWorkspaceLaunch(context.Context, clients.WorkspaceLaunchPreflightInput) (clients.WorkspaceLaunchPreflight, error) {
	f.writes++
	return clients.WorkspaceLaunchPreflight{}, errors.New("unexpected mutation")
}

func (f *disposableResetFabric) ReadWorkspaceLaunchStage(_ context.Context, input clients.WorkspaceLaunchStageInput) (clients.WorkspaceLaunchStageResult, error) {
	f.reads++
	result, ok := f.stages[input.Binding.Stage]
	if !ok {
		return clients.WorkspaceLaunchStageResult{SchemaVersion: 1, State: workspaceLaunchStageAbsent, Reason: "no_stage_record", Binding: input.Binding, Resources: input.Resources}, nil
	}
	result.Binding = input.Binding
	return result, nil
}

func (f *disposableResetFabric) EnsureWorkspaceLaunchStage(context.Context, clients.WorkspaceLaunchStageInput) (clients.WorkspaceLaunchStageResult, error) {
	f.writes++
	return clients.WorkspaceLaunchStageResult{}, errors.New("unexpected mutation")
}

func (f *disposableResetFabric) ObserveWorkspaceRuntime(_ context.Context, workspaceID string) (clients.WorkspaceRuntimeObservation, error) {
	f.reads++
	return clients.WorkspaceRuntimeObservation{SchemaVersion: 1, State: clients.WorkspaceOwnerObservationAbsent, WorkspaceID: workspaceID}, nil
}

func (f *disposableResetFabric) ObserveWorkspaceRuntimeGatewaySecret(_ context.Context, workspaceID string) (clients.WorkspaceRuntimeGatewaySecretObservation, error) {
	f.reads++
	return clients.WorkspaceRuntimeGatewaySecretObservation{SchemaVersion: 1, State: clients.WorkspaceOwnerObservationAbsent, WorkspaceID: workspaceID}, nil
}

type disposableResetSub2API struct {
	testSub2APIClient
	keys    []clients.Sub2APIWorkspaceKey
	history map[string]clients.Sub2APIBalanceHistoryEntry
}

func (s *disposableResetSub2API) WorkspaceKeysForConvergence(context.Context, int64, string) ([]clients.Sub2APIWorkspaceKey, error) {
	return append([]clients.Sub2APIWorkspaceKey(nil), s.keys...), nil
}

func (s *disposableResetSub2API) FinancialBalanceHistoryByCodes(_ context.Context, _ int64, _ []string) (map[string]clients.Sub2APIBalanceHistoryEntry, error) {
	result := map[string]clients.Sub2APIBalanceHistoryEntry{}
	for key, value := range s.history {
		result[key] = value
	}
	return result, nil
}

type disposableResetLedger struct {
	fakeLedgerClient
	receipts []clients.Receipt
	writes   int
}

func (l *disposableResetLedger) ListReceipts(context.Context, clients.ReceiptQuery) (clients.ReceiptPage, error) {
	return clients.ReceiptPage{Receipts: append([]clients.Receipt(nil), l.receipts...)}, nil
}
func (l *disposableResetLedger) RecordReceipt(context.Context, clients.ReceiptInput, string) (clients.Receipt, error) {
	l.writes++
	return clients.Receipt{}, errors.New("unexpected mutation")
}

func disposableResetPreviewFixture(t *testing.T) (http.Handler, *disposableResetFabric, *disposableResetLedger, workspaceLaunchReconcileOperation) {
	t.Helper()
	store := newMemoryTableStore()
	row := disposableResetLaunchRow(t)
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	mustStore(t, store.SaveRuntimeOperation(context.Background(), row))
	binding := clients.WorkspaceLaunchPreflightBinding{
		SchemaVersion: 1, LaunchOperationID: operation.ID, AccountID: operation.stringFact("accountId"), WorkspaceID: operation.stringFact("workspaceId"),
		PackageID: operation.stringFact("packageId"), SizeGB: operation.intFact("sizeGb"), WorkspaceImageDigest: operation.stringFact("workspaceImageDigest"),
		RequestHash: operation.stringFact("requestHash"), ProviderProfileRef: operation.stringFact("providerProfileRef"),
		ProviderBindingRef: operation.stringFact("preflightBindingRef"), SpecDigest: operation.stringFact("specDigest"),
	}
	fabric := &disposableResetFabric{binding: binding, stages: map[string]clients.WorkspaceLaunchStageResult{}}
	groupID := operation.int64Fact("workspaceKeyGroupId")
	sub2 := &disposableResetSub2API{keys: []clients.Sub2APIWorkspaceKey{{
		ID: operation.int64Fact("workspaceApiKeyId"), UserID: operation.int64Fact("sub2apiUserId"), Name: workspaceReservedKeyName(operation.stringFact("workspaceId")),
		Key: "test-key", GroupID: &groupID, Status: "active",
	}}, history: map[string]clients.Sub2APIBalanceHistoryEntry{}}
	operation.raw["workspaceKeyFingerprint"], _ = json.Marshal(workspaceLaunchCredentialFingerprint("test-key"))
	updated, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	mustStore(t, store.SaveRuntimeOperation(context.Background(), updated))
	ledger := &disposableResetLedger{}
	server, err := NewPersistentServer(controlplane.NewService(ledger, fabric, sub2), store)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(workspaceLaunchDisposableResetOperationEnv, operation.ID)
	return server, fabric, ledger, operation
}

func TestWorkspaceLaunchDisposableResetPreviewRouteInventoriesOwnersWithoutMutation(t *testing.T) {
	server, fabric, ledger, operation := disposableResetPreviewFixture(t)
	operator := reservedOperatorSessionForTest(t, server)
	req := httptest.NewRequest(http.MethodGet, "/api/operator/workspace-launches/"+operation.ID+"/disposable-reset-preview", nil)
	addAuth(req, operator)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || fabric.writes != 0 || ledger.writes != 0 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d fabricWrites=%d ledgerWrites=%d cache=%q body=%s", rec.Code, fabric.writes, ledger.writes, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
	var preview workspaceLaunchDisposableResetPreview
	if json.Unmarshal(rec.Body.Bytes(), &preview) != nil || preview.Eligible || preview.MutationBudget != 0 || len(preview.PlanSteps) != 0 || preview.ResetPlanDigest != "" || preview.OwnerStates["providerResources"] != workspaceLaunchDisposableOwnerUnknown || !slices.Contains(preview.Blockers, "provider_resources_observation_unavailable") {
		t.Fatalf("preview=%#v body=%s", preview, rec.Body.String())
	}
	for _, raw := range []string{operation.ID, operation.stringFact("accountId"), operation.stringFact("workspaceId"), operation.stringFact("preflightBindingRef"), "test-key"} {
		if strings.Contains(rec.Body.String(), raw) {
			t.Fatalf("preview leaked %q: %s", raw, rec.Body.String())
		}
	}
	beforeDigest := string(mustJSON(preview.OwnerObservations))
	secondReq := httptest.NewRequest(http.MethodGet, "/api/operator/workspace-launches/"+operation.ID+"/disposable-reset-preview", nil)
	addAuth(secondReq, operator)
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	var second workspaceLaunchDisposableResetPreview
	if secondRec.Code != http.StatusOK || json.Unmarshal(secondRec.Body.Bytes(), &second) != nil || beforeDigest != string(mustJSON(second.OwnerObservations)) || strings.Join(preview.Blockers, "\x00") != strings.Join(second.Blockers, "\x00") {
		t.Fatalf("preview is not deterministic: first=%#v second=%#v", preview, second)
	}
}

func TestWorkspaceLaunchDisposableResetPreviewFailsClosedOnFabricResidual(t *testing.T) {
	server, fabric, _, operation := disposableResetPreviewFixture(t)
	fabric.stages["storage"] = clients.WorkspaceLaunchStageResult{SchemaVersion: 1, State: workspaceLaunchStageReady, Reason: "none", Resources: clients.WorkspaceLaunchResources{StorageID: "vol-residual", StorageBindingRef: operation.ID + ":storage"}}
	operator := reservedOperatorSessionForTest(t, server)
	req := httptest.NewRequest(http.MethodGet, "/api/operator/workspace-launches/"+operation.ID+"/disposable-reset-preview", nil)
	addAuth(req, operator)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "fabric_residual_present") || strings.Contains(rec.Body.String(), "vol-residual") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWorkspaceLaunchDisposableResetPreviewRequiresOperatorAndExactAuthority(t *testing.T) {
	server, _, _, operation := disposableResetPreviewFixture(t)
	path := "/api/operator/workspace-launches/" + operation.ID + "/disposable-reset-preview"
	unauth := httptest.NewRecorder()
	server.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, path, nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", unauth.Code, unauth.Body.String())
	}
	t.Setenv(workspaceLaunchDisposableResetOperationEnv, "different-operation")
	operator := reservedOperatorSessionForTest(t, server)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	addAuth(req, operator)
	denied := httptest.NewRecorder()
	server.ServeHTTP(denied, req)
	if denied.Code != http.StatusOK || !strings.Contains(denied.Body.String(), "disposable_authority_not_configured") {
		t.Fatalf("status=%d body=%s", denied.Code, denied.Body.String())
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
	inventory := workspaceLaunchDisposableResetInventory{Facts: facts, Observations: map[string]workspaceLaunchDisposableOwnerObservation{
		"debit": workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerConfirmed, 1_000_000, "debit-one"),
	}}
	first, err := classifyWorkspaceLaunchDisposableResetInventory(row, inventory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := classifyWorkspaceLaunchDisposableResetInventory(row, inventory)
	if err != nil || first.ResetPlanDigest != second.ResetPlanDigest {
		t.Fatalf("first=%q second=%q err=%v", first.ResetPlanDigest, second.ResetPlanDigest, err)
	}
	changed := disposableResetLaunchRow(t)
	mutateDisposableResetResult(t, changed, "preflightBindingRef", "fabric-provider-binding:changed")
	third, err := classifyWorkspaceLaunchDisposableResetInventory(changed, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if first.ResetPlanDigest == third.ResetPlanDigest {
		t.Fatal("digest does not bind canonical identity")
	}
	inventory.Facts.Debit = workspaceLaunchDisposableOwnerAbsent
	inventory.Observations["debit"] = workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerAbsent, 0)
	fourth, err := classifyWorkspaceLaunchDisposableResetInventory(row, inventory)
	if err != nil || first.ResetPlanDigest == fourth.ResetPlanDigest {
		t.Fatalf("digest does not bind owner facts: %v", err)
	}
	inventory.Facts.Debit = workspaceLaunchDisposableOwnerConfirmed
	inventory.Observations["debit"] = workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerConfirmed, 2_000_000, "debit-one")
	fifth, err := classifyWorkspaceLaunchDisposableResetInventory(row, inventory)
	if err != nil || first.ResetPlanDigest == fifth.ResetPlanDigest {
		t.Fatalf("digest does not bind owner amount: %v", err)
	}
	inventory.Observations["debit"] = workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerConfirmed, 1_000_000, "debit-two")
	sixth, err := classifyWorkspaceLaunchDisposableResetInventory(row, inventory)
	if err != nil || first.ResetPlanDigest == sixth.ResetPlanDigest {
		t.Fatalf("digest does not bind owner identity: %v", err)
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
	reconciled, err := reconciler.Reconcile(context.Background(), operation.ID)
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

func TestWorkspaceLaunchDisposableResetTerminalEvidencePreservesFailedContinuation(t *testing.T) {
	store, adapter, seeded := workspaceLaunchFreshTypedPendingForTest(t, "debit")
	adapter.readResultsByStage["debit"] = []workspaceLaunchUnitReadResult{{observation: workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}}}
	operation, err := NewWorkspaceLaunchReconciler(store, adapter).Reconcile(context.Background(), seeded.ID)
	if err != nil || operation.Status != "manual_review" || operation.FreshContinuationAuthorizations["debit"].Status != "failed" {
		t.Fatalf("operation=%#v err=%v", operation, err)
	}
	classification, err := classifyWorkspaceLaunchDisposableReset(store.row, eligibleDisposableResetFacts())
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
	if err != nil || decoded.FreshContinuationAuthorizations["debit"].Status != "failed" {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
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
