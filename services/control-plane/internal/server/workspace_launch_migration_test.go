package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
)

type legacyMigrationRecordingStore struct {
	*workspaceLaunchUnitStore
	casCalls int
}

func (s *legacyMigrationRecordingStore) UpcastLegacyWorkspaceLaunch(context.Context, workspaceLaunchLegacyCAS) error {
	s.casCalls++
	return errWorkspaceLaunchCASConflict
}

type legacyMigrationSuccessStore struct {
	*workspaceLaunchUnitStore
}

func (s *legacyMigrationSuccessStore) UpcastLegacyWorkspaceLaunch(_ context.Context, update workspaceLaunchLegacyCAS) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row == nil || stringValue(s.row["id"]) != update.OperationID || stringValue(s.row["result"]) != update.ExpectedOperationResult {
		return errWorkspaceLaunchCASConflict
	}
	s.row = cloneMap(update.DesiredOperation)
	return nil
}

type legacyMigrationDivergentCASStore struct {
	*workspaceLaunchUnitStore
}

func (s *legacyMigrationDivergentCASStore) UpcastLegacyWorkspaceLaunch(_ context.Context, update workspaceLaunchLegacyCAS) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	operation, err := decodeWorkspaceLaunchReconcileOperation(update.DesiredOperation)
	if err != nil {
		return err
	}
	operation.Stage = "receipt"
	operation.raw["url"] = json.RawMessage(`"https://divergent.invalid"`)
	row, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		return err
	}
	s.row = row
	return errWorkspaceLaunchCASConflict
}

type legacyMigrationStageAdapter struct {
	workspaceLaunchUnitAdapter
	states    map[string]workspaceLaunchStageObservation
	mutations int
}

func (a *legacyMigrationStageAdapter) ReadStage(_ context.Context, operation workspaceLaunchReconcileOperation) (workspaceLaunchStageObservation, error) {
	observation, ok := a.states[operation.Stage]
	if !ok {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, errors.New("unexpected migration stage read")
	}
	return observation, nil
}

func (a *legacyMigrationStageAdapter) MutateStage(context.Context, workspaceLaunchReconcileOperation, string) error {
	a.mutations++
	return errors.New("migration must be GET-only")
}

func schema2ManualReviewRow(t *testing.T, _ bool) map[string]any {
	t.Helper()
	command := workspaceLaunchUnitCommand()
	legacy := map[string]any{
		"schemaVersion":              2,
		"phase":                      "storage_fulfilling",
		"requestHash":                command.RequestHash,
		"accountId":                  command.AccountID,
		"ownerUserId":                command.OwnerUserID,
		"sub2apiUserId":              command.Sub2APIUserID,
		"workspaceId":                command.WorkspaceID,
		"name":                       command.Name,
		"packageId":                  command.PackageID,
		"sizeGb":                     command.StorageGB,
		"autoRenew":                  command.AutoRenew,
		"priceVersion":               command.PriceVersion,
		"totalChargeUsdMicros":       command.TotalChargeUSDMicros,
		"workspaceImageDigest":       command.WorkspaceImageDigest,
		"workspaceKeyGroupId":        command.WorkspaceKeyGroupID,
		"workspaceApiKeyId":          int64(9),
		"workspaceKeyStatus":         workspaceKeyCodexGroupBound,
		"workspaceKeyFingerprint":    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"sub2apiRedeemCode":          "redeem-legacy",
		"chargeAttempted":            true,
		"chargeConfirmation":         map[string]any{"status": "used"},
		"preChargeBalanceUsdMicros":  command.PreChargeBalanceMicros,
		"postChargeBalanceUsdMicros": int64(47_420_000),
		"postChargeBalanceKnown":     true,
		"computeAllocationId":        "compute-legacy",
		"computeBindingRef":          "fabric-operation:compute-legacy",
		"storageId":                  "storage-legacy",
		"continuationAttemptBudgets": map[string]any{
			"storage":    map[string]any{"max": 1, "attempted": 0, "confirmed": 0, "unknown": 0},
			"attachment": map[string]any{"max": 1, "attempted": 0, "confirmed": 0, "unknown": 0},
			"secret":     map[string]any{"max": 1, "attempted": 0, "confirmed": 0, "unknown": 0},
			"runtime":    map[string]any{"max": 1, "attempted": 0, "confirmed": 0, "unknown": 0},
			"activation": map[string]any{"max": 1, "attempted": 0, "confirmed": 0, "unknown": 0},
			"receipt":    map[string]any{"max": 1, "attempted": 0, "confirmed": 0, "unknown": 0},
		},
	}
	body, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"id": command.OperationID, "operationId": command.OperationID,
		"accountId": command.AccountID, "workspaceId": command.WorkspaceID,
		"resourceId": command.WorkspaceID, "resourceKind": "workspace_launch",
		"action": workspaceLaunchAction, "status": "manual_review", "result": string(body),
		"createdAt": "2026-08-12T00:00:00Z",
	}
}

func legacyMigrationBinding(command workspaceLaunchReconcileCreate) *clients.LegacyWorkspaceLaunchBindingResult {
	return &clients.LegacyWorkspaceLaunchBindingResult{
		SchemaVersion: clients.WorkspaceLaunchFabricSchemaVersion, State: "ready", Reason: "legacy_partial_history",
		LaunchOperationID: command.OperationID, AccountID: command.AccountID, WorkspaceID: command.WorkspaceID,
		ProviderProfileRef: command.ProviderProfileRef, PreflightBindingRef: command.PreflightBindingRef,
		Resources: clients.WorkspaceLaunchResources{ComputeAllocationID: "compute-legacy", ComputeBindingRef: "fabric-operation:compute-legacy"},
		Stages: []clients.LegacyWorkspaceLaunchStageReadback{{
			Stage: "ensure_compute_allocation", State: "ready", OperationRef: "fabric-operation:compute-legacy",
			IdempotencyIdentity: "legacy-compute-idempotency", ResourceBindingRef: "fabric-operation:compute-legacy", AuthoritativeReadbackRef: "fabric-readback:compute-legacy",
		}, {Stage: "storage", State: "absent"}},
	}
}

func TestWorkspaceLaunchLegacyMigrationDoesNotInferKeyAttemptFromReadyReadback(t *testing.T) {
	row := schema2ManualReviewRow(t, false)
	store := &legacyMigrationSuccessStore{workspaceLaunchUnitStore: &workspaceLaunchUnitStore{row: row}}
	adapter := &legacyMigrationStageAdapter{states: map[string]workspaceLaunchStageObservation{
		"key": {State: workspaceLaunchStageReady, Facts: map[string]any{
			"workspaceApiKeyId": int64(9), "workspaceKeyGroupId": int64(7), "workspaceKeyStatus": workspaceKeyCodexGroupBound,
			"workspaceKeyFingerprint": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
		"debit": {State: workspaceLaunchStageReady, Facts: map[string]any{
			"chargeAttempted": true, "chargeConfirmation": map[string]any{"status": "used"},
			"preChargeBalanceUsdMicros": int64(100_000_000), "postChargeBalanceUsdMicros": int64(47_420_000), "postChargeBalanceKnown": true,
		}},
	}}
	migrated, err := NewWorkspaceLaunchReconciler(store, adapter).MigrateLegacy(context.Background(), row, workspaceLaunchUnitCommand(), legacyMigrationBinding(workspaceLaunchUnitCommand()))
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Attempts["key"] != (workspaceLaunchStageAttempt{Max: 1}) ||
		migrated.Attempts["debit"] != (workspaceLaunchStageAttempt{Attempted: 1, Confirmed: 1, Max: 1, Status: "confirmed", IdempotencyKey: "redeem-legacy"}) ||
		migrated.Attempts["ensure_compute_allocation"] != (workspaceLaunchStageAttempt{Attempted: 1, Confirmed: 1, Max: 1, Status: "confirmed", IdempotencyKey: "legacy-compute-idempotency"}) ||
		migrated.Stage != "storage" || adapter.mutations != 0 {
		t.Fatalf("migration inferred or lost history: cursor=%s attempts=%#v mutations=%d", workspaceLaunchReconcileResultSummary(migrated), migrated.Attempts, adapter.mutations)
	}
}

func TestWorkspaceLaunchLegacyMigrationDoesNotInferContinuationAttemptFromReadyReadback(t *testing.T) {
	row := schema2ManualReviewRow(t, false)
	original := stringValue(row["result"])
	store := &legacyMigrationRecordingStore{workspaceLaunchUnitStore: &workspaceLaunchUnitStore{row: row}}
	adapter := &legacyMigrationStageAdapter{states: map[string]workspaceLaunchStageObservation{
		"key": {State: workspaceLaunchStageReady, Facts: map[string]any{
			"workspaceApiKeyId": int64(9), "workspaceKeyGroupId": int64(7), "workspaceKeyStatus": workspaceKeyCodexGroupBound,
			"workspaceKeyFingerprint": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
		"debit": {State: workspaceLaunchStageReady, Facts: map[string]any{
			"chargeAttempted": true, "chargeConfirmation": map[string]any{"status": "used"},
			"preChargeBalanceUsdMicros": int64(100_000_000), "postChargeBalanceUsdMicros": int64(47_420_000), "postChargeBalanceKnown": true,
		}},
	}}
	binding := legacyMigrationBinding(workspaceLaunchUnitCommand())
	binding.Resources.StorageID, binding.Resources.StorageBindingRef = "storage-legacy", "fabric-operation:storage-legacy"
	binding.Stages[1] = clients.LegacyWorkspaceLaunchStageReadback{Stage: "storage", State: "ready", OperationRef: "fabric-operation:storage-legacy", IdempotencyIdentity: "legacy-storage-idempotency", ResourceBindingRef: "fabric-operation:storage-legacy", AuthoritativeReadbackRef: "fabric-readback:storage-legacy"}
	binding.Stages = append(binding.Stages, clients.LegacyWorkspaceLaunchStageReadback{Stage: "attachment", State: "absent"})
	_, err := NewWorkspaceLaunchReconciler(store, adapter).MigrateLegacy(context.Background(), row, workspaceLaunchUnitCommand(), binding)
	if !errors.Is(err, errWorkspaceLaunchLegacyMigrationBlocked) {
		t.Fatalf("migration error=%v, want %v", err, errWorkspaceLaunchLegacyMigrationBlocked)
	}
	current, found, readErr := store.GetRuntimeOperation(context.Background(), stringValue(row["id"]))
	if readErr != nil || !found || stringValue(current["result"]) != original || store.casCalls != 0 || adapter.mutations != 0 {
		t.Fatalf("ready continuation inferred history: found=%v cas=%d mutations=%d err=%v", found, store.casCalls, adapter.mutations, readErr)
	}
}

func TestWorkspaceLaunchLegacyMigrationRejectsDivergentCASLoserReadback(t *testing.T) {
	row := schema2ManualReviewRow(t, false)
	store := &legacyMigrationDivergentCASStore{workspaceLaunchUnitStore: &workspaceLaunchUnitStore{row: row}}
	adapter := &legacyMigrationStageAdapter{states: map[string]workspaceLaunchStageObservation{
		"key": {State: workspaceLaunchStageReady, Facts: map[string]any{
			"workspaceApiKeyId": int64(9), "workspaceKeyGroupId": int64(7), "workspaceKeyStatus": workspaceKeyCodexGroupBound,
			"workspaceKeyFingerprint": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
		"debit": {State: workspaceLaunchStageReady, Facts: map[string]any{
			"chargeAttempted": true, "chargeConfirmation": map[string]any{"status": "used"},
			"preChargeBalanceUsdMicros": int64(100_000_000), "postChargeBalanceUsdMicros": int64(47_420_000), "postChargeBalanceKnown": true,
		}},
	}}
	_, err := NewWorkspaceLaunchReconciler(store, adapter).MigrateLegacy(context.Background(), row, workspaceLaunchUnitCommand(), legacyMigrationBinding(workspaceLaunchUnitCommand()))
	if !errors.Is(err, errWorkspaceLaunchCASConflict) {
		t.Fatalf("divergent CAS loser readback error=%v, want %v", err, errWorkspaceLaunchCASConflict)
	}
}
