package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"sync"
	"testing"
	"time"
)

func testWorkspaceLaunchBinding(stage, action, operationID string) WorkspaceLaunchStageBinding {
	return WorkspaceLaunchStageBinding{
		SchemaVersion: 1, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
		Stage: stage, Action: action, FabricOperationID: operationID, IdempotencyKey: "launch-alpha:" + stage,
		RequestHash: hashInput(map[string]string{"stage": stage}), ExpectedResourceBinding: "",
	}
}

func TestProviderMutationReplayEpochIsDurableConcurrentAndLeaseBound(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	startedAt := time.Date(2026, 8, 15, 4, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return startedAt }
	parent := testWorkspaceLaunchBinding("storage", "ensure_storage", "launch-alpha:storage")
	operation := newOperation("ensure_storage", "workspace_launch_stage", parent.FabricOperationID, parent.AccountID, parent.WorkspaceID, parent.IdempotencyKey, parent.RequestHash, startedAt)
	operation.ID, operation.OperationID, operation.Status = parent.FabricOperationID, parent.FabricOperationID, "started"
	if err := bindLaunchStageOperation(&operation, &parent); err != nil {
		t.Fatal(err)
	}
	ctx := service.providerMutationContext(context.Background(), operation)
	fresh, err := beginProviderMutation(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha")
	if err != nil || fresh == nil || !fresh.Fresh {
		t.Fatalf("fresh attempt=%#v err=%v", fresh, err)
	}

	attempts := make([]*providerMutationAttempt, 2)
	for index := range attempts {
		attempts[index], err = beginProviderMutation(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha")
		if err != nil || attempts[index] == nil || attempts[index].Fresh {
			t.Fatalf("reserved attempt %d=%#v err=%v", index, attempts[index], err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, len(attempts))
	for _, attempt := range attempts {
		wg.Add(1)
		go func(candidate *providerMutationAttempt) {
			defer wg.Done()
			claimed, claimErr := candidate.claimReplay(context.Background())
			if claimErr == nil && !claimed {
				claimErr = ErrRuntimeOperationNotCurrent
			}
			results <- claimErr
		}(attempt)
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for claimErr := range results {
		switch {
		case claimErr == nil:
			successes++
		case errors.Is(claimErr, ErrRuntimeOperationNotCurrent):
			conflicts++
		default:
			t.Fatalf("unexpected claim error: %v", claimErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("replay claims successes=%d conflicts=%d", successes, conflicts)
	}
	persisted, err := store.Get(context.Background(), fresh.operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	claimBody, err := json.Marshal(persisted.RedactedProviderPayload[providerMutationReplayEpochPayloadKey])
	if err != nil {
		t.Fatal(err)
	}
	var firstEpoch providerMutationReplayEpoch
	if json.Unmarshal(claimBody, &firstEpoch) != nil || firstEpoch.SchemaVersion != 1 || firstEpoch.ParentFabricOperationID != parent.FabricOperationID ||
		firstEpoch.ChildOperationID != fresh.operation.ID || firstEpoch.IdempotencyKey != fresh.operation.IdempotencyKey ||
		firstEpoch.State != "leased" || firstEpoch.LeaseGeneration != 1 || firstEpoch.ReplayID == "" {
		t.Fatalf("persisted replay epoch=%#v", firstEpoch)
	}

	service.now = func() time.Time { return startedAt.Add(providerMutationReplayLease + time.Second) }
	restarted, err := beginProviderMutation(service.providerMutationContext(context.Background(), operation), "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha")
	if err != nil || restarted == nil || restarted.Fresh {
		t.Fatalf("restart attempt=%#v err=%v", restarted, err)
	}
	claimed, err := restarted.claimReplay(context.Background())
	if err != nil || !claimed || !restarted.Replay || restarted.operation.IdempotencyKey != fresh.operation.IdempotencyKey {
		t.Fatalf("lease recovery claimed=%v replay=%v key=%q err=%v", claimed, restarted.Replay, restarted.operation.IdempotencyKey, err)
	}
	renewedBody, _ := json.Marshal(restarted.operation.RedactedProviderPayload[providerMutationReplayEpochPayloadKey])
	var renewed providerMutationReplayEpoch
	if json.Unmarshal(renewedBody, &renewed) != nil || renewed.ReplayID != firstEpoch.ReplayID ||
		renewed.ParentFabricOperationID != firstEpoch.ParentFabricOperationID || renewed.ChildOperationID != firstEpoch.ChildOperationID ||
		renewed.IdempotencyKey != firstEpoch.IdempotencyKey || renewed.State != "leased" || renewed.LeaseGeneration != 2 {
		t.Fatalf("lease renewal changed logical replay epoch: first=%#v renewed=%#v", firstEpoch, renewed)
	}
}

func TestProviderMutationReplayEpochConvergesTerminalAndCannotReclaim(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	now := time.Date(2026, 8, 15, 5, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	parent := testWorkspaceLaunchBinding("runtime", "ensure_runtime", "launch-alpha:runtime")
	operation := newOperation(parent.Action, "workspace_launch_stage", parent.FabricOperationID, parent.AccountID, parent.WorkspaceID, parent.IdempotencyKey, parent.RequestHash, now)
	operation.ID, operation.OperationID, operation.Status = parent.FabricOperationID, parent.FabricOperationID, "started"
	if err := bindLaunchStageOperation(&operation, &parent); err != nil {
		t.Fatal(err)
	}
	ctx := service.providerMutationContext(context.Background(), operation)
	fresh, err := beginProviderMutation(ctx, "provider_runtime_create", "workspace_runtime", "rt-alpha", "opl-runtime-alpha")
	if err != nil || fresh == nil || !fresh.Fresh {
		t.Fatalf("fresh attempt=%#v err=%v", fresh, err)
	}
	replay, err := beginProviderMutation(ctx, "provider_runtime_create", "workspace_runtime", "rt-alpha", "opl-runtime-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, claimErr := replay.claimReplay(ctx); claimErr != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, claimErr)
	}
	if err := replay.markReplayDispatch(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	runtime := WorkspaceRuntime{ID: "rt-alpha", OperationID: parent.FabricOperationID, WorkspaceID: parent.WorkspaceID, ProviderRequestID: "provider-runtime-alpha"}
	if err := replay.complete(ctx, runtime.ProviderRequestID, runtime, nil); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Get(ctx, fresh.operation.ID)
	binding, bindingOK := decodeProviderMutationBinding(persisted)
	epoch, epochOK := decodeProviderMutationReplayEpoch(persisted)
	if err != nil || persisted.Status != "succeeded" || persisted.ResourceID != "rt-alpha" || !bindingOK || binding.Parent != parent ||
		!epochOK || epoch.State != "succeeded" || epoch.DispatchStartedAt == "" || epoch.CompletedAt == "" {
		t.Fatalf("terminal child=%#v binding=%#v/%v epoch=%#v/%v err=%v", persisted, binding, bindingOK, epoch, epochOK, err)
	}
	restarted, err := beginProviderMutation(ctx, "provider_runtime_create", "workspace_runtime", "rt-alpha", "opl-runtime-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, claimErr := restarted.claimReplay(ctx); claimed || claimErr != nil {
		t.Fatalf("terminal replay reclaimed=%v err=%v", claimed, claimErr)
	}
}

func TestProviderMutationStalePreLeaseCompletionCannotOverwriteReplayEpoch(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	now := time.Date(2026, 8, 15, 5, 30, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	parent := testWorkspaceLaunchBinding("storage", "ensure_storage", "launch-alpha:storage")
	operation := newOperation(parent.Action, "workspace_launch_stage", parent.FabricOperationID, parent.AccountID, parent.WorkspaceID, parent.IdempotencyKey, parent.RequestHash, now)
	operation.ID, operation.OperationID, operation.Status = parent.FabricOperationID, parent.FabricOperationID, "started"
	if err := bindLaunchStageOperation(&operation, &parent); err != nil {
		t.Fatal(err)
	}
	ctx := service.providerMutationContext(context.Background(), operation)
	fresh, err := beginProviderMutation(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha")
	if err != nil || fresh == nil || !fresh.Fresh {
		t.Fatalf("fresh attempt=%#v err=%v", fresh, err)
	}
	stale, err := beginProviderMutation(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha")
	if err != nil {
		t.Fatal(err)
	}
	winner, err := beginProviderMutation(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, claimErr := winner.claimReplay(ctx); claimErr != nil || !claimed {
		t.Fatalf("winner claim=%v err=%v", claimed, claimErr)
	}
	if err := winner.markReplayDispatch(ctx); err != nil {
		t.Fatal(err)
	}
	volume := StorageVolume{ID: "vol-alpha", AccountID: parent.AccountID, WorkspaceID: parent.WorkspaceID, ProviderRequestID: "provider-storage-alpha"}
	if err := stale.complete(ctx, volume.ProviderRequestID, volume, nil); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("stale pre-lease completion err=%v, want %v", err, ErrRuntimeOperationNotCurrent)
	}
	persisted, err := store.Get(ctx, fresh.operation.ID)
	epoch, epochOK := decodeProviderMutationReplayEpoch(persisted)
	if err != nil || !epochOK || epoch.State != "awaiting_readback" || persisted.Status != "started" {
		t.Fatalf("stale completion changed owner operation=%#v epoch=%#v/%v err=%v", persisted, epoch, epochOK, err)
	}
	if err := winner.complete(ctx, volume.ProviderRequestID, volume, nil); err != nil {
		t.Fatal(err)
	}
	persisted, err = store.Get(ctx, fresh.operation.ID)
	epoch, epochOK = decodeProviderMutationReplayEpoch(persisted)
	if err != nil || persisted.Status != "succeeded" || persisted.ID != fresh.operation.ID || persisted.OperationID != fresh.operation.OperationID ||
		persisted.IdempotencyKey != fresh.operation.IdempotencyKey || !epochOK || epoch.State != "succeeded" ||
		epoch.ChildOperationID != fresh.operation.ID || epoch.IdempotencyKey != fresh.operation.IdempotencyKey {
		t.Fatalf("winner terminal operation=%#v epoch=%#v/%v err=%v", persisted, epoch, epochOK, err)
	}
}

func TestProviderMutationReplayEpochRejectsStaleLeaseGeneration(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	now := time.Date(2026, 8, 15, 6, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	parent := testWorkspaceLaunchBinding("storage", "ensure_storage", "launch-alpha:storage")
	operation := newOperation(parent.Action, "workspace_launch_stage", parent.FabricOperationID, parent.AccountID, parent.WorkspaceID, parent.IdempotencyKey, parent.RequestHash, now)
	operation.ID, operation.OperationID, operation.Status = parent.FabricOperationID, parent.FabricOperationID, "started"
	if err := bindLaunchStageOperation(&operation, &parent); err != nil {
		t.Fatal(err)
	}
	ctx := service.providerMutationContext(context.Background(), operation)
	if _, err := beginProviderMutation(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha"); err != nil {
		t.Fatal(err)
	}
	stale, _ := beginProviderMutation(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha")
	if claimed, err := stale.claimReplay(ctx); err != nil || !claimed {
		t.Fatalf("initial claim=%v err=%v", claimed, err)
	}
	now = now.Add(providerMutationReplayLease + time.Second)
	winner, _ := beginProviderMutation(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha")
	if claimed, err := winner.claimReplay(ctx); err != nil || !claimed {
		t.Fatalf("renewed claim=%v err=%v", claimed, err)
	}
	if err := stale.markReplayDispatch(ctx); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("stale generation dispatch err=%v", err)
	}
	if err := winner.markReplayDispatch(ctx); err != nil {
		t.Fatal(err)
	}
	volume := StorageVolume{ID: "vol-alpha", AccountID: parent.AccountID, WorkspaceID: parent.WorkspaceID, ProviderRequestID: "provider-storage-alpha"}
	if err := winner.complete(ctx, volume.ProviderRequestID, volume, nil); err != nil {
		t.Fatal(err)
	}
	if err := stale.complete(ctx, volume.ProviderRequestID, volume, nil); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("stale generation terminal err=%v", err)
	}
}

func TestProviderMutationReplayEpochDistinguishesBlockedFromAwaitingReadback(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		markDispatch  bool
		wantState     string
		wantRenewable bool
	}{
		{name: "pre-dispatch owner error", wantState: "blocked"},
		{name: "transport response loss", markDispatch: true, wantState: "awaiting_readback", wantRenewable: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := NewMemoryOperationStore()
			service := NewServiceWithOperationStore(testProvider{}, store)
			now := time.Date(2026, 8, 15, 7, 0, 0, 0, time.UTC)
			service.now = func() time.Time { return now }
			parent := testWorkspaceLaunchBinding("storage", "ensure_storage", "launch-alpha:storage")
			operation := newOperation(parent.Action, "workspace_launch_stage", parent.FabricOperationID, parent.AccountID, parent.WorkspaceID, parent.IdempotencyKey, parent.RequestHash, now)
			operation.ID, operation.OperationID, operation.Status = parent.FabricOperationID, parent.FabricOperationID, "started"
			if err := bindLaunchStageOperation(&operation, &parent); err != nil {
				t.Fatal(err)
			}
			ctx := service.providerMutationContext(context.Background(), operation)
			fresh, err := beginProviderMutation(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha")
			if err != nil {
				t.Fatal(err)
			}
			replay, _ := beginProviderMutation(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha")
			if claimed, claimErr := replay.claimReplay(ctx); claimErr != nil || !claimed {
				t.Fatalf("claim=%v err=%v", claimed, claimErr)
			}
			if testCase.markDispatch {
				if err := replay.markReplayDispatch(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if err := replay.complete(ctx, "", StorageVolume{ID: "vol-alpha"}, errors.New("injected response loss")); err != nil {
				t.Fatal(err)
			}
			persisted, _ := store.Get(ctx, fresh.operation.ID)
			epoch, ok := decodeProviderMutationReplayEpoch(persisted)
			if !ok || epoch.State != testCase.wantState || persisted.Status != "started" {
				t.Fatalf("persisted=%#v epoch=%#v/%v", persisted, epoch, ok)
			}
			now = now.Add(providerMutationReplayLease + time.Second)
			restarted, _ := beginProviderMutation(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha")
			claimed, claimErr := restarted.claimReplay(ctx)
			if claimed != testCase.wantRenewable || testCase.wantRenewable && claimErr != nil || !testCase.wantRenewable && !errors.Is(claimErr, ErrLaunchStageBindingConflict) {
				t.Fatalf("renewed=%v err=%v wantRenewable=%v", claimed, claimErr, testCase.wantRenewable)
			}
		})
	}
}

func TestProviderMutationReplayRenewalPreservesPriorDispatchEvidence(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	now := time.Date(2026, 8, 15, 7, 30, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	parent := testWorkspaceLaunchBinding("storage", "ensure_storage", "launch-alpha:storage")
	operation := newOperation(parent.Action, "workspace_launch_stage", parent.FabricOperationID, parent.AccountID, parent.WorkspaceID, parent.IdempotencyKey, parent.RequestHash, now)
	operation.ID, operation.OperationID, operation.Status = parent.FabricOperationID, parent.FabricOperationID, "started"
	if err := bindLaunchStageOperation(&operation, &parent); err != nil {
		t.Fatal(err)
	}
	ctx := service.providerMutationContext(context.Background(), operation)
	fresh, err := beginProviderMutation(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha")
	if err != nil || fresh == nil || !fresh.Fresh {
		t.Fatalf("fresh=%#v err=%v", fresh, err)
	}
	first, err := beginProviderMutation(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, claimErr := first.claimReplay(ctx); claimErr != nil || !claimed {
		t.Fatalf("first claim=%v err=%v", claimed, claimErr)
	}
	if err := first.markReplayDispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.complete(ctx, "", StorageVolume{ID: "vol-alpha"}, errors.New("transport response lost")); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Get(ctx, fresh.operation.ID)
	firstEpoch, epochOK := decodeProviderMutationReplayEpoch(persisted)
	if err != nil || !epochOK || firstEpoch.State != "awaiting_readback" || firstEpoch.DispatchStartedAt == "" {
		t.Fatalf("first epoch=%#v/%v operation=%#v err=%v", firstEpoch, epochOK, persisted, err)
	}

	now = now.Add(providerMutationReplayLease + time.Second)
	restarted, err := beginProviderMutation(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, claimErr := restarted.claimReplay(ctx); claimErr != nil || !claimed {
		t.Fatalf("renewed claim=%v err=%v", claimed, claimErr)
	}
	renewed, renewedOK := decodeProviderMutationReplayEpoch(restarted.operation)
	if !renewedOK || renewed.State != "leased" || renewed.LeaseGeneration != firstEpoch.LeaseGeneration+1 ||
		renewed.DispatchStartedAt != firstEpoch.DispatchStartedAt {
		t.Fatalf("renewal discarded dispatch evidence: first=%#v renewed=%#v/%v", firstEpoch, renewed, renewedOK)
	}
	if err := restarted.complete(ctx, "", StorageVolume{ID: "vol-alpha"}, errors.New("owner read unavailable")); err != nil {
		t.Fatal(err)
	}
	persisted, err = store.Get(ctx, fresh.operation.ID)
	finalEpoch, finalOK := decodeProviderMutationReplayEpoch(persisted)
	if err != nil || !finalOK || finalEpoch.State != "awaiting_readback" || finalEpoch.DispatchStartedAt != firstEpoch.DispatchStartedAt || finalEpoch.CompletedAt != "" {
		t.Fatalf("owner read error erased prior dispatch fact: epoch=%#v/%v operation=%#v err=%v", finalEpoch, finalOK, persisted, err)
	}
}

func TestProviderMutationReplayEpochRejectsParentBindingAndProviderStateDrift(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		drift func(FabricOperation) FabricOperation
	}{
		{
			name: "parent binding",
			drift: func(next FabricOperation) FabricOperation {
				binding, ok := decodeProviderMutationBinding(next)
				if !ok {
					t.Fatal("provider mutation binding missing")
				}
				binding.Parent.LaunchOperationID = "launch-drift"
				next.RedactedProviderPayload[providerMutationBindingPayloadKey] = persistedProviderMutationBinding{Binding: binding, Digest: hashInput(binding)}
				return next
			},
		},
		{
			name: "provider state",
			drift: func(next FabricOperation) FabricOperation {
				value := json.RawMessage(`{"allocation":"drift"}`)
				next.RedactedProviderPayload[providerMutationStatePayloadKey] = persistedProviderMutationState{Value: value, Digest: hashInput(value)}
				return next
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := NewMemoryOperationStore()
			service := NewServiceWithOperationStore(testProvider{}, store)
			now := time.Date(2026, 8, 15, 7, 45, 0, 0, time.UTC)
			service.now = func() time.Time { return now }
			parent := testWorkspaceLaunchBinding("storage", "ensure_storage", "launch-alpha:storage")
			operation := newOperation(parent.Action, "workspace_launch_stage", parent.FabricOperationID, parent.AccountID, parent.WorkspaceID, parent.IdempotencyKey, parent.RequestHash, now)
			operation.ID, operation.OperationID, operation.Status = parent.FabricOperationID, parent.FabricOperationID, "started"
			if err := bindLaunchStageOperation(&operation, &parent); err != nil {
				t.Fatal(err)
			}
			ctx := service.providerMutationContext(context.Background(), operation)
			state := map[string]string{"allocation": "original"}
			fresh, err := beginProviderMutationWithState(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha", state)
			if err != nil || fresh == nil || !fresh.Fresh {
				t.Fatalf("fresh=%#v err=%v", fresh, err)
			}
			replay, err := beginProviderMutationWithState(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha", state)
			if err != nil {
				t.Fatal(err)
			}
			if claimed, claimErr := replay.claimReplay(ctx); claimErr != nil || !claimed {
				t.Fatalf("claim=%v err=%v", claimed, claimErr)
			}
			expected := replay.operation
			next := expected
			next.RedactedProviderPayload = maps.Clone(expected.RedactedProviderPayload)
			epoch, ok := decodeProviderMutationReplayEpoch(expected)
			if !ok {
				t.Fatal("replay epoch missing")
			}
			epoch.LeaseGeneration++
			epoch.LeaseExpiresAt = now.Add(2 * providerMutationReplayLease).Format(time.RFC3339Nano)
			next.RedactedProviderPayload[providerMutationReplayEpochPayloadKey] = epoch
			next = testCase.drift(next)
			if err := store.SaveProviderMutationReplayEpoch(ctx, expected, next); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
				t.Fatalf("identity drift accepted: err=%v next=%#v", err, next)
			}
		})
	}
}

func TestProviderMutationReplayDispatchRequiresCurrentClaim(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	now := time.Date(2026, 8, 15, 7, 55, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	parent := testWorkspaceLaunchBinding("storage", "ensure_storage", "launch-alpha:storage")
	operation := newOperation(parent.Action, "workspace_launch_stage", parent.FabricOperationID, parent.AccountID, parent.WorkspaceID, parent.IdempotencyKey, parent.RequestHash, now)
	operation.ID, operation.OperationID, operation.Status = parent.FabricOperationID, parent.FabricOperationID, "started"
	if err := bindLaunchStageOperation(&operation, &parent); err != nil {
		t.Fatal(err)
	}
	ctx := service.providerMutationContext(context.Background(), operation)
	fresh, err := beginProviderMutation(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha")
	if err != nil || fresh == nil || !fresh.Fresh {
		t.Fatalf("fresh=%#v err=%v", fresh, err)
	}
	if err := fresh.markReplayDispatch(ctx); err != nil {
		t.Fatalf("fresh initial mutation was rejected: %v", err)
	}

	reserved, err := beginProviderMutation(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := reserved.markReplayDispatch(ctx); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("nonfresh dispatch without replay claim err=%v", err)
	}
	persisted, err := store.Get(ctx, fresh.operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := persisted.RedactedProviderPayload[providerMutationReplayEpochPayloadKey]; exists {
		t.Fatalf("unclaimed dispatch mutated replay epoch: %#v", persisted)
	}
	if claimed, claimErr := reserved.claimReplay(ctx); claimErr != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, claimErr)
	}
	if err := reserved.markReplayDispatch(ctx); err != nil {
		t.Fatalf("claimed replay dispatch failed: %v", err)
	}
}

func TestWorkspaceLaunchStageActionAcceptsOnlyCanonicalPairs(t *testing.T) {
	pairs := []struct {
		stage  string
		action string
	}{
		{stage: "ensure_compute_allocation", action: "ensure_compute_allocation"},
		{stage: "storage", action: "ensure_storage"},
		{stage: "attachment", action: "ensure_attachment"},
		{stage: "secret", action: "ensure_gateway_secret"},
		{stage: "runtime", action: "ensure_runtime"},
	}
	for _, stage := range pairs {
		for _, action := range pairs {
			binding := testWorkspaceLaunchBinding(stage.stage, action.action, "launch-alpha:"+stage.stage)
			if got, want := validWorkspaceLaunchStageBinding(binding), stage.stage == action.stage; got != want {
				t.Fatalf("pair %s/%s valid=%v want=%v", stage.stage, action.action, got, want)
			}
		}
	}
}

func TestProviderMutationBindingIsDurableBeforeWriteAndSurvivesCompletion(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	parent := testWorkspaceLaunchBinding("storage", "ensure_storage", "launch-alpha:storage")
	operation := newOperation("create_storage_volume", "storage_volume", "vol-alpha", parent.AccountID, parent.WorkspaceID, parent.IdempotencyKey, parent.RequestHash, time.Now().UTC())
	if err := bindLaunchStageOperation(&operation, &parent); err != nil {
		t.Fatal(err)
	}
	ctx := service.providerMutationContext(context.Background(), operation)
	attempt, err := beginProviderMutation(ctx, "provider_storage_create", "storage_volume", "vol-alpha", "volume/vol-alpha")
	if err != nil || attempt == nil || !attempt.Fresh {
		t.Fatalf("attempt=%#v err=%v", attempt, err)
	}
	beforeWrite, err := store.Get(context.Background(), attempt.operation.ID)
	childBinding, ok := decodeProviderMutationBinding(beforeWrite)
	if err != nil || beforeWrite.Status != "started" || !ok || childBinding.Parent != parent {
		t.Fatalf("before write operation=%#v binding=%#v/%v err=%v", beforeWrite, childBinding, ok, err)
	}
	volume := StorageVolume{ID: "vol-alpha", AccountID: parent.AccountID, WorkspaceID: parent.WorkspaceID, Status: "ready"}
	if err := attempt.complete(context.Background(), "provider-request-alpha", volume, nil); err != nil {
		t.Fatal(err)
	}
	afterWrite, err := store.Get(context.Background(), attempt.operation.ID)
	childBinding, ok = decodeProviderMutationBinding(afterWrite)
	if err != nil || afterWrite.Status != "succeeded" || !ok || childBinding.Parent != parent {
		t.Fatalf("after write operation=%#v binding=%#v/%v err=%v", afterWrite, childBinding, ok, err)
	}
}

func TestLaunchStageBindingRequiresExplicitCallerIdentity(t *testing.T) {
	operation := newOperation("create_storage_volume", "storage_volume", "vol-alpha", "acct-alpha", "ws-alpha", "ordinary-key", hashInput("request"), time.Now().UTC())
	if err := bindLaunchStageOperation(&operation, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := decodeLaunchStageBinding(operation); ok {
		t.Fatal("ordinary operation acquired a fabricated Launch binding")
	}
}
