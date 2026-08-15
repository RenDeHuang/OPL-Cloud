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
	mu                     sync.Mutex
	readyStages            map[string]bool
	unknownStages          map[string]bool
	stageObservations      map[string]workspaceLaunchStageObservation
	readErrors             map[string]error
	reads                  int
	mutations              int
	mutationsByStage       map[string]int
	mutationOperationID    string
	mutationWorkspaceID    string
	mutationRedeemCode     string
	mutationIdempotencyKey string
	mutationUserID         int64
	mutationAmount         int64
	mutationBlocked        bool
	replayableStages       map[string]bool
	readResultsByStage     map[string][]workspaceLaunchUnitReadResult
	mutationErrors         map[string]error
	panicBeforeMutations   map[string]int
	barrier                chan struct{}
}

type workspaceLaunchUnitReadResult struct {
	observation workspaceLaunchStageObservation
	err         error
}

func (a *workspaceLaunchUnitAdapter) ReadStage(_ context.Context, operation workspaceLaunchReconcileOperation) (workspaceLaunchStageObservation, error) {
	a.mu.Lock()
	a.reads++
	if results := a.readResultsByStage[operation.Stage]; len(results) > 0 {
		result := results[0]
		a.readResultsByStage[operation.Stage] = results[1:]
		a.mu.Unlock()
		return result.observation, result.err
	}
	if err := a.readErrors[operation.Stage]; err != nil {
		a.mu.Unlock()
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, err
	}
	if observation, ok := a.stageObservations[operation.Stage]; ok {
		a.mu.Unlock()
		return observation, nil
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

func (a *workspaceLaunchUnitAdapter) CanReplayStage(operation workspaceLaunchReconcileOperation) bool {
	return a.replayableStages[operation.Stage] && !a.mutationBlocked
}

func (a *workspaceLaunchUnitAdapter) MutateStage(_ context.Context, operation workspaceLaunchReconcileOperation, idempotencyKey string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.panicBeforeMutations[operation.Stage] > 0 {
		a.panicBeforeMutations[operation.Stage]--
		panic("simulated process crash before transport send")
	}
	a.mutations++
	a.mutationOperationID = operation.ID
	a.mutationWorkspaceID = operation.stringFact("workspaceId")
	a.mutationRedeemCode = operation.stringFact("sub2apiRedeemCode")
	a.mutationIdempotencyKey = idempotencyKey
	a.mutationUserID = operation.int64Fact("sub2apiUserId")
	a.mutationAmount = operation.int64Fact("totalChargeUsdMicros")
	if a.readyStages == nil {
		a.readyStages = map[string]bool{}
	}
	if a.mutationsByStage == nil {
		a.mutationsByStage = map[string]int{}
	}
	a.mutationsByStage[operation.Stage]++
	if err := a.mutationErrors[operation.Stage]; err != nil {
		return err
	}
	a.readyStages[operation.Stage] = true
	return nil
}

func workspaceLaunchReadyFacts(stage string) map[string]any {
	switch stage {
	case "key":
		return map[string]any{"workspaceApiKeyId": int64(9), "workspaceKeyGroupId": int64(7), "workspaceKeyStatus": workspaceKeyCodexGroupBound, "workspaceKeyFingerprint": "sha256:" + strings.Repeat("a", 64)}
	case "debit":
		return map[string]any{"chargeAttempted": true, "chargeConfirmation": map[string]any{"status": "used"}, "preChargeBalanceUsdMicros": int64(100), "postChargeBalanceUsdMicros": int64(50), "postChargeBalanceKnown": true}
	case "ensure_compute_allocation":
		return map[string]any{"computeAllocationId": "ca-unit", "computeBindingRef": "workspace-launch-unit:ensure_compute_allocation"}
	case "storage":
		return map[string]any{"storageId": "vol-unit", "storageBindingRef": "workspace-launch-unit:storage"}
	case "attachment":
		return map[string]any{"attachmentId": "att-unit", "attachmentBindingRef": "workspace-launch-unit:attachment"}
	case "secret":
		return map[string]any{"gatewaySecretRef": "secret-unit", "gatewaySecretVersion": "v1", "secretBindingRef": "workspace-launch-unit:secret", "workspaceKeyStatus": "configured"}
	case "runtime":
		return map[string]any{"runtimeId": "rt-unit", "runtimeReady": true, "runtimeServiceName": "runtime-unit", "runtimeBindingRef": "workspace-launch-unit:runtime", "url": "https://workspace.example/unit"}
	case "activation":
		return map[string]any{"activationOperationId": "workspace-launch-unit:activation", "workspaceActivatedAt": "2026-08-15T00:00:00Z"}
	case "receipt":
		return map[string]any{"receiptId": "receipt-unit", "receiptOperationId": "workspace-launch-unit:purchase-receipt"}
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

func workspaceLaunchReservedStageManualReviewRow(t *testing.T, stage string) map[string]any {
	t.Helper()
	operation, err := newWorkspaceLaunchReconcileOperation(workspaceLaunchUnitCommand())
	if err != nil {
		t.Fatal(err)
	}
	operation.Version = 5
	operation.Stage = stage
	operation.Status = "manual_review"
	attempt := operation.Attempts[stage]
	attempt.Attempted = 1
	attempt.Status = "reserved"
	attempt.IdempotencyKey = workspaceLaunchStageIdempotencyKey(operation, 1)
	operation.Attempts[stage] = attempt
	operation.Observations[stage] = workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}
	row, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func workspaceLaunchReservedStageAuthorization(t *testing.T, row map[string]any, authorizationID string) workspaceLaunchResumeAuthorization {
	t.Helper()
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	return workspaceLaunchResumeAuthorization{
		AuthorizationID: authorizationID, LaunchVersion: operation.Version, AuthorizedStage: operation.Stage, AuthorizedBy: "usr-admin",
		AuthorizedAt: "2026-08-15T01:00:00Z", Reason: "exact reserved stage absent by authoritative readback",
		MutationBudget: 0, IdempotentReplayBudget: 1, AuthoritativeReadBudget: workspaceLaunchAuthoritativeReadBudget,
	}
}

func TestWorkspaceLaunchReservedStageReplayMatrix(t *testing.T) {
	for _, stage := range workspaceLaunchReconcileStages[:len(workspaceLaunchReconcileStages)-1] {
		t.Run(stage+"/absent replays one logical claim", func(t *testing.T) {
			row := workspaceLaunchReservedStageManualReviewRow(t, stage)
			authorization := workspaceLaunchReservedStageAuthorization(t, row, "resume-"+stage)
			store := &workspaceLaunchUnitStore{row: row}
			adapter := &workspaceLaunchUnitAdapter{replayableStages: map[string]bool{stage: true}}
			got, err := NewWorkspaceLaunchReconciler(store, adapter).Resume(context.Background(), workspaceLaunchUnitCommand().OperationID, authorization)
			if err != nil {
				t.Fatal(err)
			}
			attempt := got.Attempts[stage]
			if attempt.Attempted != 1 || attempt.Max != 1 || attempt.Confirmed != 1 || attempt.Unknown != 0 || attempt.Status != "confirmed" ||
				adapter.mutationsByStage[stage] != 1 || adapter.mutationIdempotencyKey != attempt.IdempotencyKey ||
				got.IdempotentReplayClaims[stage].AuthorizationID != authorization.AuthorizationID {
				t.Fatalf("stage replay changed budget or identity: operation=%s attempt=%#v claims=%#v mutations=%#v err=%v", workspaceLaunchReconcileResultSummary(got), attempt, got.IdempotentReplayClaims, adapter.mutationsByStage, err)
			}
		})

		t.Run(stage+"/ready converges read only", func(t *testing.T) {
			row := workspaceLaunchReservedStageManualReviewRow(t, stage)
			authorization := workspaceLaunchReservedStageAuthorization(t, row, "resume-ready-"+stage)
			adapter := &workspaceLaunchUnitAdapter{readyStages: map[string]bool{stage: true}, replayableStages: map[string]bool{stage: true}}
			got, err := NewWorkspaceLaunchReconciler(&workspaceLaunchUnitStore{row: row}, adapter).Resume(context.Background(), workspaceLaunchUnitCommand().OperationID, authorization)
			if err != nil || got.Attempts[stage].Confirmed != 1 || adapter.mutations != 0 {
				t.Fatalf("ready stage did not converge read-only: operation=%s mutations=%d err=%v", workspaceLaunchReconcileResultSummary(got), adapter.mutations, err)
			}
		})
	}
}

func TestWorkspaceLaunchReservedStageReplayRefusesUncertainAuthority(t *testing.T) {
	invalidReady := workspaceLaunchReadyFacts("debit")
	invalidReady["chargeAttempted"] = false
	cases := []struct {
		name    string
		adapter *workspaceLaunchUnitAdapter
	}{
		{name: "unknown", adapter: &workspaceLaunchUnitAdapter{unknownStages: map[string]bool{"debit": true}}},
		{name: "read error", adapter: &workspaceLaunchUnitAdapter{readErrors: map[string]error{"debit": errors.New("read failed")}}},
		{name: "conflicting ready facts", adapter: &workspaceLaunchUnitAdapter{stageObservations: map[string]workspaceLaunchStageObservation{"debit": {State: workspaceLaunchStageReady, Facts: invalidReady}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := workspaceLaunchReservedStageManualReviewRow(t, "debit")
			persistedBefore := stringValue(row["result"])
			store := &workspaceLaunchUnitStore{row: row}
			tc.adapter.replayableStages = map[string]bool{"debit": true}
			authorization := workspaceLaunchReservedStageAuthorization(t, row, "resume-debit-"+strings.ReplaceAll(tc.name, " ", "-"))
			_, err := NewWorkspaceLaunchReconciler(store, tc.adapter).Resume(context.Background(), workspaceLaunchUnitCommand().OperationID, authorization)
			if !errors.Is(err, errWorkspaceLaunchGrantConflict) || tc.adapter.reads != 1 || tc.adapter.mutations != 0 || stringValue(store.row["result"]) != persistedBefore {
				t.Fatalf("uncertain authority changed debit: reads=%d mutations=%d err=%v", tc.adapter.reads, tc.adapter.mutations, err)
			}
		})
	}
}

func TestWorkspaceLaunchReservedStageReplayRefusesStateAndAuthorizationDrift(t *testing.T) {
	cases := []struct {
		name       string
		mutateOp   func(*workspaceLaunchReconcileOperation)
		mutateAuth func(*workspaceLaunchResumeAuthorization)
	}{
		{name: "status", mutateOp: func(operation *workspaceLaunchReconcileOperation) { operation.Status = "pending" }},
		{name: "attempt", mutateOp: func(operation *workspaceLaunchReconcileOperation) {
			attempt := operation.Attempts["debit"]
			attempt.Unknown, attempt.Status = 1, "unknown"
			operation.Attempts["debit"] = attempt
		}},
		{name: "version", mutateAuth: func(authorization *workspaceLaunchResumeAuthorization) { authorization.LaunchVersion++ }},
		{name: "stage", mutateAuth: func(authorization *workspaceLaunchResumeAuthorization) { authorization.AuthorizedStage = "key" }},
		{name: "budget", mutateAuth: func(authorization *workspaceLaunchResumeAuthorization) { authorization.MutationBudget = 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := workspaceLaunchReservedStageManualReviewRow(t, "debit")
			operation, err := decodeWorkspaceLaunchReconcileOperation(row)
			if err != nil {
				t.Fatal(err)
			}
			if tc.mutateOp != nil {
				tc.mutateOp(&operation)
				row, err = workspaceLaunchReconcileOperationRow(operation)
				if err != nil {
					t.Fatal(err)
				}
			}
			authorization := workspaceLaunchReservedStageAuthorization(t, row, "resume-debit-drift-"+tc.name)
			if tc.mutateAuth != nil {
				tc.mutateAuth(&authorization)
			}
			store, adapter := &workspaceLaunchUnitStore{row: row}, &workspaceLaunchUnitAdapter{replayableStages: map[string]bool{"debit": true}}
			persistedBefore := stringValue(row["result"])
			_, err = NewWorkspaceLaunchReconciler(store, adapter).Resume(context.Background(), operation.ID, authorization)
			if !errors.Is(err, errWorkspaceLaunchGrantConflict) || adapter.mutations != 0 || stringValue(store.row["result"]) != persistedBefore {
				t.Fatalf("drift changed debit: reads=%d mutations=%d err=%v", adapter.reads, adapter.mutations, err)
			}
		})
	}
}

func TestWorkspaceLaunchReservedStageReplayCannotBeAuthorizedTwice(t *testing.T) {
	row := workspaceLaunchReservedStageManualReviewRow(t, "debit")
	store, adapter := &workspaceLaunchUnitStore{row: row}, &workspaceLaunchUnitAdapter{replayableStages: map[string]bool{"debit": true}}
	reconciler := NewWorkspaceLaunchReconciler(store, adapter)
	firstAuthorization := workspaceLaunchReservedStageAuthorization(t, row, "resume-debit-first")
	first, err := reconciler.Resume(context.Background(), workspaceLaunchUnitCommand().OperationID, firstAuthorization)
	if err != nil || adapter.mutations != 1 {
		t.Fatalf("first recovery failed: operation=%s mutations=%d err=%v", workspaceLaunchReconcileResultSummary(first), adapter.mutations, err)
	}

	first.Stage = "debit"
	first.Status = "manual_review"
	attempt := first.Attempts["debit"]
	attempt.Attempted, attempt.Confirmed, attempt.Unknown, attempt.Status = 1, 0, 0, "reserved"
	first.Attempts["debit"] = attempt
	first.Observations["debit"] = workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}
	store.row, err = workspaceLaunchReconcileOperationRow(first)
	if err != nil {
		t.Fatal(err)
	}
	adapter.readyStages["debit"] = false
	secondAuthorization := workspaceLaunchReservedStageAuthorization(t, store.row, "resume-debit-second")
	persistedBefore := stringValue(store.row["result"])
	if _, err = reconciler.Resume(context.Background(), first.ID, secondAuthorization); !errors.Is(err, errWorkspaceLaunchGrantConflict) || adapter.mutations != 1 || stringValue(store.row["result"]) != persistedBefore {
		t.Fatalf("second recovery authorization changed debit: mutations=%d err=%v", adapter.mutations, err)
	}
}

func TestWorkspaceLaunchReservedStageReplayCASAllowsOneWriter(t *testing.T) {
	row := workspaceLaunchReservedStageManualReviewRow(t, "debit")
	authorization := workspaceLaunchReservedStageAuthorization(t, row, "resume-debit-concurrent")
	store := &workspaceLaunchUnitStore{row: row}
	adapter := &workspaceLaunchUnitAdapter{barrier: make(chan struct{}), replayableStages: map[string]bool{"debit": true}}
	reconciler := NewWorkspaceLaunchReconciler(store, adapter)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := reconciler.Resume(context.Background(), workspaceLaunchUnitCommand().OperationID, authorization)
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
			t.Fatalf("unexpected concurrent recovery error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 || adapter.mutationsByStage["debit"] != 1 {
		t.Fatalf("successes=%d conflicts=%d debit mutations=%d", successes, conflicts, adapter.mutationsByStage["debit"])
	}
}

func TestWorkspaceLaunchReservedStageReplaySurvivesCrashBeforeTransportSend(t *testing.T) {
	for _, stage := range workspaceLaunchReconcileStages[:len(workspaceLaunchReconcileStages)-1] {
		t.Run(stage, func(t *testing.T) {
			row := workspaceLaunchReservedStageManualReviewRow(t, stage)
			store := &workspaceLaunchUnitStore{row: row}
			adapter := &workspaceLaunchUnitAdapter{
				replayableStages:     map[string]bool{stage: true},
				panicBeforeMutations: map[string]int{stage: 1},
			}
			startedAt := time.Date(2026, 8, 15, 2, 0, 0, 0, time.UTC)
			reconciler := NewWorkspaceLaunchReconciler(store, adapter)
			reconciler.now = func() time.Time { return startedAt }
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("expected simulated process crash")
					}
				}()
				_, _ = reconciler.Resume(context.Background(), workspaceLaunchUnitCommand().OperationID, workspaceLaunchReservedStageAuthorization(t, row, "resume-crash-"+stage))
			}()

			claimed, err := decodeWorkspaceLaunchReconcileOperation(store.row)
			if err != nil || claimed.Status != "pending" || claimed.Stage != stage || claimed.IdempotentReplayClaims[stage].Status != "claimed" ||
				claimed.ResumeAuthorization == nil || claimed.ResumeAuthorizationConsumedAt != "" || adapter.mutations != 0 {
				t.Fatalf("crash cut not durable: operation=%s claim=%#v mutations=%d err=%v", workspaceLaunchReconcileResultSummary(claimed), claimed.IdempotentReplayClaims[stage], adapter.mutations, err)
			}

			restarted := NewWorkspaceLaunchReconciler(store, adapter)
			restarted.now = func() time.Time { return startedAt.Add(workspaceLaunchIdempotentReplayLease + time.Second) }
			got, err := restarted.Reconcile(context.Background(), claimed.ID)
			if err != nil || got.Attempts[stage].Attempted != 1 || got.Attempts[stage].Max != 1 || got.Attempts[stage].Confirmed != 1 ||
				got.IdempotentReplayClaims[stage].Status != "succeeded" || adapter.mutationsByStage[stage] != 1 || adapter.mutationIdempotencyKey != claimed.Attempts[stage].IdempotencyKey {
				t.Fatalf("restart did not recover exact replay: operation=%s claim=%#v mutations=%#v err=%v", workspaceLaunchReconcileResultSummary(got), got.IdempotentReplayClaims[stage], adapter.mutationsByStage, err)
			}
		})
	}
}

func TestWorkspaceLaunchReservedStageReplayPostReadMatrix(t *testing.T) {
	mutationErr := errors.New("transport response lost")
	cases := []struct {
		name              string
		mutationErr       error
		postRead          workspaceLaunchUnitReadResult
		wantStatus        string
		wantStage         string
		wantClaim         string
		wantUnknown       int
		wantReturnedError bool
	}{
		{name: "success ready", postRead: workspaceLaunchUnitReadResult{observation: workspaceLaunchStageObservation{State: workspaceLaunchStageReady, Facts: workspaceLaunchReadyFacts("debit")}}, wantStatus: "pending", wantStage: "ensure_compute_allocation", wantClaim: "succeeded"},
		{name: "error ready", mutationErr: mutationErr, postRead: workspaceLaunchUnitReadResult{observation: workspaceLaunchStageObservation{State: workspaceLaunchStageReady, Facts: workspaceLaunchReadyFacts("debit")}}, wantStatus: "pending", wantStage: "ensure_compute_allocation", wantClaim: "succeeded"},
		{name: "success pending", postRead: workspaceLaunchUnitReadResult{observation: workspaceLaunchStageObservation{State: workspaceLaunchStagePending}}, wantStatus: "pending", wantStage: "debit", wantClaim: "waiting"},
		{name: "error pending", mutationErr: mutationErr, postRead: workspaceLaunchUnitReadResult{observation: workspaceLaunchStageObservation{State: workspaceLaunchStagePending}}, wantStatus: "pending", wantStage: "debit", wantClaim: "waiting"},
		{name: "success absent", postRead: workspaceLaunchUnitReadResult{observation: workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}}, wantStatus: "manual_review", wantStage: "debit", wantClaim: "failed", wantUnknown: 1},
		{name: "error absent", mutationErr: mutationErr, postRead: workspaceLaunchUnitReadResult{observation: workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}}, wantStatus: "manual_review", wantStage: "debit", wantClaim: "failed", wantUnknown: 1, wantReturnedError: true},
		{name: "success unknown", postRead: workspaceLaunchUnitReadResult{observation: workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}}, wantStatus: "manual_review", wantStage: "debit", wantClaim: "failed", wantUnknown: 1},
		{name: "error read error", mutationErr: mutationErr, postRead: workspaceLaunchUnitReadResult{observation: workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, err: errors.New("owner read failed")}, wantStatus: "manual_review", wantStage: "debit", wantClaim: "failed", wantUnknown: 1, wantReturnedError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := workspaceLaunchReservedStageManualReviewRow(t, "debit")
			absent := workspaceLaunchUnitReadResult{observation: workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}}
			adapter := &workspaceLaunchUnitAdapter{
				replayableStages: map[string]bool{"debit": true}, mutationErrors: map[string]error{"debit": tc.mutationErr},
				readResultsByStage: map[string][]workspaceLaunchUnitReadResult{"debit": {absent, absent, absent, tc.postRead}},
			}
			got, err := NewWorkspaceLaunchReconciler(&workspaceLaunchUnitStore{row: row}, adapter).Resume(context.Background(), workspaceLaunchUnitCommand().OperationID, workspaceLaunchReservedStageAuthorization(t, row, "resume-post-read-"+strings.ReplaceAll(tc.name, " ", "-")))
			if (err != nil) != tc.wantReturnedError || got.Status != tc.wantStatus || got.Stage != tc.wantStage || got.IdempotentReplayClaims["debit"].Status != tc.wantClaim ||
				got.Attempts["debit"].Attempted != 1 || got.Attempts["debit"].Max != 1 || got.Attempts["debit"].Unknown != tc.wantUnknown || adapter.mutationsByStage["debit"] != 1 {
				t.Fatalf("post-read transition mismatch: operation=%s attempt=%#v claim=%#v mutations=%#v err=%v", workspaceLaunchReconcileResultSummary(got), got.Attempts["debit"], got.IdempotentReplayClaims["debit"], adapter.mutationsByStage, err)
			}
		})
	}
}

func TestWorkspaceLaunchPendingReadbackIsBoundedAndCanConvergeReadOnly(t *testing.T) {
	for _, tc := range []struct {
		name       string
		followups  []workspaceLaunchUnitReadResult
		wantStatus string
		wantStage  string
	}{
		{name: "pending then ready", followups: []workspaceLaunchUnitReadResult{{observation: workspaceLaunchStageObservation{State: workspaceLaunchStagePending}}, {observation: workspaceLaunchStageObservation{State: workspaceLaunchStageReady, Facts: workspaceLaunchReadyFacts("debit")}}}, wantStatus: "pending", wantStage: "ensure_compute_allocation"},
		{name: "permanent pending exhausts", followups: []workspaceLaunchUnitReadResult{{observation: workspaceLaunchStageObservation{State: workspaceLaunchStagePending}}, {observation: workspaceLaunchStageObservation{State: workspaceLaunchStagePending}}}, wantStatus: "manual_review", wantStage: "debit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := workspaceLaunchReservedStageManualReviewRow(t, "debit")
			absent := workspaceLaunchUnitReadResult{observation: workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}}
			pending := workspaceLaunchUnitReadResult{observation: workspaceLaunchStageObservation{State: workspaceLaunchStagePending}}
			adapter := &workspaceLaunchUnitAdapter{
				replayableStages:   map[string]bool{"debit": true},
				readResultsByStage: map[string][]workspaceLaunchUnitReadResult{"debit": append([]workspaceLaunchUnitReadResult{absent, absent, absent, pending}, tc.followups...)},
			}
			store := &workspaceLaunchUnitStore{row: row}
			reconciler := NewWorkspaceLaunchReconciler(store, adapter)
			got, err := reconciler.Resume(context.Background(), workspaceLaunchUnitCommand().OperationID, workspaceLaunchReservedStageAuthorization(t, row, "resume-pending-"+strings.ReplaceAll(tc.name, " ", "-")))
			for err == nil && got.Status == "pending" && got.Stage == "debit" && got.Attempts["debit"].PendingReadbacks < got.Attempts["debit"].MaxPendingReadbacks {
				got, err = reconciler.Reconcile(context.Background(), got.ID)
			}
			if err != nil || got.Status != tc.wantStatus || got.Stage != tc.wantStage || adapter.mutationsByStage["debit"] != 1 ||
				got.Attempts["debit"].PendingReadbacks > got.Attempts["debit"].MaxPendingReadbacks {
				t.Fatalf("bounded pending mismatch: operation=%s attempt=%#v mutations=%#v err=%v", workspaceLaunchReconcileResultSummary(got), got.Attempts["debit"], adapter.mutationsByStage, err)
			}
			if tc.wantStatus == "manual_review" {
				attempt := got.Attempts["debit"]
				if attempt.Unknown != 1 || attempt.Status != "unknown" || got.Observations["debit"].State != workspaceLaunchStageUnknown ||
					got.ResumeAuthorizationConsumedAt == "" || got.Observations["debit"].State == workspaceLaunchStageAbsent {
					t.Fatalf("exhaustion inferred absence or left authorization active: operation=%s attempt=%#v", workspaceLaunchReconcileResultSummary(got), attempt)
				}
			}
		})
	}
}

func TestWorkspaceLaunchPendingReadbackRequiresPersistedOperatorAuthorization(t *testing.T) {
	row := workspaceLaunchReservedStageManualReviewRow(t, "debit")
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	operation.Status = "pending"
	operation.Observations["debit"] = workspaceLaunchStageObservation{State: workspaceLaunchStagePending}
	row, err = workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	store := &workspaceLaunchUnitStore{row: row}
	adapter := &workspaceLaunchUnitAdapter{stageObservations: map[string]workspaceLaunchStageObservation{
		"debit": {State: workspaceLaunchStageReady, Facts: workspaceLaunchReadyFacts("debit")},
	}}

	got, err := NewWorkspaceLaunchReconciler(store, adapter).Reconcile(context.Background(), operation.ID)
	if err != nil || got.Status != "manual_review" || got.Stage != "debit" || adapter.reads != 0 || adapter.mutations != 0 {
		t.Fatalf("unauthorized pending continuation read owner state: operation=%s reads=%d mutations=%d err=%v", workspaceLaunchReconcileResultSummary(got), adapter.reads, adapter.mutations, err)
	}
}

func TestWorkspaceLaunchLegacyV3MissingReadBudgetDefaultsToSafeStop(t *testing.T) {
	row := workspaceLaunchReservedStageManualReviewRow(t, "debit")
	var result map[string]any
	if err := json.Unmarshal([]byte(stringValue(row["result"])), &result); err != nil {
		t.Fatal(err)
	}
	attempts := mapField(result, "attempts")
	debit := mapField(attempts, "debit")
	delete(debit, "pendingReadbacks")
	delete(debit, "maxPendingReadbacks")
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	row["result"] = string(encoded)

	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	if got := operation.Attempts["debit"]; got.PendingReadbacks != 0 || got.MaxPendingReadbacks != workspaceLaunchLegacyV3AuthoritativeReadBudget {
		t.Fatalf("legacy compatibility invented owner facts or reads: attempt=%#v", got)
	}
	operation.Status = "pending"
	operation.Observations["debit"] = workspaceLaunchStageObservation{State: workspaceLaunchStagePending}
	row, err = workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	store := &workspaceLaunchUnitStore{row: row}
	adapter := &workspaceLaunchUnitAdapter{readyStages: map[string]bool{"debit": true}, replayableStages: map[string]bool{"debit": true}}
	got, err := NewWorkspaceLaunchReconciler(store, adapter).Reconcile(context.Background(), operation.ID)
	if err != nil || got.Status != "manual_review" || got.Stage != "debit" || adapter.reads != 0 || adapter.mutations != 0 {
		t.Fatalf("legacy zero budget performed owner work: operation=%s reads=%d mutations=%d err=%v", workspaceLaunchReconcileResultSummary(got), adapter.reads, adapter.mutations, err)
	}
}

func TestWorkspaceLaunchRecoveryAtEveryStageContinuesOriginalOperationToSucceeded(t *testing.T) {
	for _, failedStage := range workspaceLaunchReconcileStages[:len(workspaceLaunchReconcileStages)-1] {
		t.Run(failedStage, func(t *testing.T) {
			row := workspaceLaunchReservedStageManualReviewRow(t, failedStage)
			store := &workspaceLaunchUnitStore{row: row}
			adapter := &workspaceLaunchUnitAdapter{replayableStages: map[string]bool{failedStage: true}}
			reconciler := NewWorkspaceLaunchReconciler(store, adapter)
			got, err := reconciler.Resume(context.Background(), workspaceLaunchUnitCommand().OperationID, workspaceLaunchReservedStageAuthorization(t, row, "resume-terminal-"+failedStage))
			for err == nil && got.Status == "pending" {
				got, err = reconciler.Reconcile(context.Background(), got.ID)
			}
			if err != nil || got.Status != "succeeded" || got.Stage != "succeeded" || got.ID != workspaceLaunchUnitCommand().OperationID || got.stringFact("workspaceId") != workspaceLaunchUnitCommand().WorkspaceID {
				t.Fatalf("recovery did not reach terminal: operation=%s mutations=%#v err=%v", workspaceLaunchReconcileResultSummary(got), adapter.mutationsByStage, err)
			}
			for _, stage := range workspaceLaunchReconcileStages[:len(workspaceLaunchReconcileStages)-1] {
				if adapter.mutationsByStage[stage] > 1 {
					t.Fatalf("stage %s mutated %d times after %s recovery", stage, adapter.mutationsByStage[stage], failedStage)
				}
			}
		})
	}
}

func TestWorkspaceLaunchReceiptOnlyReplayReachesTerminalWithoutRepeatingPriorStages(t *testing.T) {
	operation, err := newWorkspaceLaunchReconcileOperation(workspaceLaunchUnitCommand())
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range workspaceLaunchReconcileStages[:len(workspaceLaunchReconcileStages)-2] {
		operation.Stage = stage
		observation, reduceErr := reduceWorkspaceLaunchStageObservation(&operation, workspaceLaunchStageObservation{State: workspaceLaunchStageReady, Facts: workspaceLaunchReadyFacts(stage)})
		if reduceErr != nil {
			t.Fatalf("seed %s: %v", stage, reduceErr)
		}
		attempt := operation.Attempts[stage]
		attempt.Attempted, attempt.Confirmed, attempt.Status = 1, 1, "confirmed"
		attempt.IdempotencyKey = workspaceLaunchStageIdempotencyKey(operation, 1)
		operation.Attempts[stage] = attempt
		operation.Observations[stage] = observation
	}
	operation.Version, operation.Stage, operation.Status = 17, "receipt", "manual_review"
	receiptAttempt := operation.Attempts["receipt"]
	receiptAttempt.Attempted, receiptAttempt.Status = 1, "reserved"
	receiptAttempt.IdempotencyKey = workspaceLaunchStageIdempotencyKey(operation, 1)
	operation.Attempts["receipt"] = receiptAttempt
	operation.Observations["receipt"] = workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}
	row, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	store := &workspaceLaunchUnitStore{row: row}
	adapter := &workspaceLaunchUnitAdapter{replayableStages: map[string]bool{"receipt": true}}
	reconciler := NewWorkspaceLaunchReconciler(store, adapter)
	authorization := workspaceLaunchReservedStageAuthorization(t, row, "resume-receipt-only")
	got, err := reconciler.Resume(context.Background(), operation.ID, authorization)
	if err != nil || got.Status != "succeeded" || got.Stage != "succeeded" || got.stringFact("workspaceId") != operation.stringFact("workspaceId") ||
		got.stringFact("sub2apiRedeemCode") != operation.stringFact("sub2apiRedeemCode") || got.int64Fact("totalChargeUsdMicros") != operation.int64Fact("totalChargeUsdMicros") ||
		got.stringFact("receiptId") != "receipt-unit" || got.stringFact("receiptOperationId") != operation.ID+":purchase-receipt" || adapter.mutationsByStage["receipt"] != 1 {
		t.Fatalf("receipt-only recovery mismatch: operation=%s mutations=%#v err=%v", workspaceLaunchReconcileResultSummary(got), adapter.mutationsByStage, err)
	}
	for _, stage := range workspaceLaunchReconcileStages[:len(workspaceLaunchReconcileStages)-2] {
		if adapter.mutationsByStage[stage] != 0 {
			t.Fatalf("receipt recovery repeated %s mutation", stage)
		}
	}
	readsBefore, mutationsBefore, persistedBefore := adapter.reads, adapter.mutations, stringValue(store.row["result"])
	replayed, err := reconciler.Resume(context.Background(), operation.ID, authorization)
	if err != nil || replayed.Status != "succeeded" || adapter.reads != readsBefore || adapter.mutations != mutationsBefore || stringValue(store.row["result"]) != persistedBefore {
		t.Fatalf("exact receipt authorization replay caused work: operation=%s reads=%d/%d mutations=%d/%d err=%v", workspaceLaunchReconcileResultSummary(replayed), adapter.reads, readsBefore, adapter.mutations, mutationsBefore, err)
	}
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

func TestWorkspaceLaunchProjectionMatchesCanonicalCurrentResourceFields(t *testing.T) {
	operation, err := newWorkspaceLaunchReconcileOperation(workspaceLaunchUnitCommand())
	if err != nil {
		t.Fatal(err)
	}
	for field, value := range map[string]string{
		"computeAllocationId": "compute-fabric", "storageId": "storage-fabric", "attachmentId": "attachment-fabric",
		"runtimeId": "runtime-fabric", "runtimeServiceName": "runtime-service", "url": "https://workspace.example",
		"runtimeUsername": "opl", "credentialStatus": "configured", "credentialVersion": "v1", "credentialSecretRef": "secret-ref",
		"periodStart": "2026-08-15T00:00:00Z", "paidThrough": "2026-09-15T00:00:00Z",
	} {
		operation.raw[field], _ = json.Marshal(value)
	}
	operation.raw["workspaceApiKeyId"], _ = json.Marshal(int64(9))
	operation.raw["billingAnchorDay"], _ = json.Marshal(15)
	workspace := map[string]any{
		"id": operation.stringFact("workspaceId"), "accountId": operation.stringFact("accountId"), "ownerUserId": operation.stringFact("ownerUserId"),
		"name": operation.stringFact("name"), "packageId": operation.stringFact("packageId"), "provider": "fabric", "url": "https://workspace.example",
		"currentComputeAllocationId": "compute-fabric", "storageId": "storage-fabric", "currentAttachmentId": "attachment-fabric",
		"runtimeId": "runtime-fabric", "runtime": map[string]any{"serviceName": "runtime-service", "status": "running", "ready": true}, "state": "running",
		"workspaceApiKeyId": int64(9), "access": map[string]any{"username": "opl", "credentialStatus": "configured", "credentialVersion": "v1", "secretRef": "secret-ref"},
		"priceVersion": operation.stringFact("priceVersion"), "totalUsdMicros": operation.int64Fact("totalChargeUsdMicros"), "storageGb": operation.intFact("sizeGb"),
		"periodStart": "2026-08-15T00:00:00Z", "paidThrough": "2026-09-15T00:00:00Z", "billingAnchorDay": 15,
	}
	if !workspaceLaunchProjectionMatches(operation, workspace) {
		t.Fatalf("canonical PostgreSQL Workspace projection did not match: %#v", workspace)
	}
	for _, drift := range []struct {
		name  string
		apply func(map[string]any)
	}{
		{name: "attachment", apply: func(row map[string]any) { row["currentAttachmentId"] = "attachment-other" }},
		{name: "url", apply: func(row map[string]any) { row["url"] = "https://other.example" }},
		{name: "runtime", apply: func(row map[string]any) { row["runtime"].(map[string]any)["serviceName"] = "runtime-other" }},
		{name: "key", apply: func(row map[string]any) { row["workspaceApiKeyId"] = int64(10) }},
		{name: "credential", apply: func(row map[string]any) { row["access"].(map[string]any)["secretRef"] = "secret-other" }},
		{name: "amount", apply: func(row map[string]any) { row["totalUsdMicros"] = operation.int64Fact("totalChargeUsdMicros") + 1 }},
	} {
		drifted := cloneMap(workspace)
		drifted["runtime"] = cloneMap(mapField(workspace, "runtime"))
		drifted["access"] = cloneMap(mapField(workspace, "access"))
		drift.apply(drifted)
		if workspaceLaunchProjectionMatches(operation, drifted) {
			t.Fatalf("%s drift matched: %#v", drift.name, drifted)
		}
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
