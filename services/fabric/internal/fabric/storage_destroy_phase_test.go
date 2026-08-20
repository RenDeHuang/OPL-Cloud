package fabric

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type phasedStorageDestroyProvider struct {
	testProvider
	destroyCalls  atomic.Int32
	readbackCalls atomic.Int32
	destroyResult StorageVolume
	destroyErr    error
	readback      StorageVolume
	readbackErr   error
	destroyHook   func(StorageVolume)
}

func (p *phasedStorageDestroyProvider) DestroyStorageVolume(_ context.Context, volume StorageVolume) (StorageVolume, error) {
	p.destroyCalls.Add(1)
	if p.destroyHook != nil {
		p.destroyHook(cloneStorageVolume(volume))
	}
	return cloneStorageVolume(p.destroyResult), p.destroyErr
}

func (p *phasedStorageDestroyProvider) ReadStorageVolumeStatus(_ context.Context, _ StorageVolume) (StorageVolume, error) {
	p.readbackCalls.Add(1)
	return cloneStorageVolume(p.readback), p.readbackErr
}

func storageDestroyPhaseResult(volume StorageVolume, phase, mutationCount, status, cbsStatus string) StorageVolume {
	volume = cloneStorageVolume(volume)
	volume.Status = status
	volume.CBSStatus = cbsStatus
	volume.ProviderRequestID = "req-storage-destroy-phase"
	if volume.ProviderData == nil {
		volume.ProviderData = map[string]string{}
	}
	volume.ProviderData["storageDestroyPhase"] = phase
	volume.ProviderData["storageDestroyMutationCount"] = mutationCount
	return volume
}

func exactStorageDestroyAbsence(volume StorageVolume) StorageVolume {
	volume = storageDestroyPhaseResult(volume, "absence_confirmed", "0", "external_deleted", "NOT_FOUND")
	volume.ProviderData["storageVolumeId"] = volume.ProviderResourceID
	volume.ProviderData["cbsStatus"] = "NOT_FOUND"
	volume.ProviderData["status"] = "external_deleted"
	volume.ProviderData["describeCbsRequestId"] = "req-describe-cbs-absent"
	return volume
}

func appendFailedStorageDestroy(t *testing.T, store OperationStore, volume StorageVolume) {
	t.Helper()
	now := time.Date(2026, 1, 20, 8, 0, 0, 0, time.UTC)
	operation := newOperation("destroy_storage_volume", "storage_volume", volume.ID, volume.AccountID, volume.WorkspaceID, "", hashInput(map[string]string{"id": volume.ID}), now)
	operation.ID, operation.Status, operation.CreatedAt, operation.FinishedAt = "fop_destroy_storage_failed_"+stableSuffix(volume.ID)[:12], "failed", now, now
	fillOperationResource(&operation, volume)
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
}

func appendStartedStorageDestroy(t *testing.T, store OperationStore, volume StorageVolume) {
	t.Helper()
	now := time.Date(2026, 1, 20, 7, 0, 0, 0, time.UTC)
	operation := newOperation("destroy_storage_volume", "storage_volume", volume.ID, volume.AccountID, volume.WorkspaceID, "", hashInput(map[string]string{"id": volume.ID}), now)
	operation.ID, operation.Status, operation.CreatedAt = "fop_destroy_storage_started_"+stableSuffix(volume.ID)[:12], "started", now
	fillOperationResource(&operation, volume)
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
}

func appendSucceededStorageCreate(t *testing.T, store OperationStore, volume StorageVolume) {
	t.Helper()
	now := time.Date(2026, 1, 19, 8, 0, 0, 0, time.UTC)
	operation := newOperation("create_storage_volume", "storage_volume", volume.ID, volume.AccountID, volume.WorkspaceID, volume.OperationID, hashInput(volume), now)
	operation.ID, operation.Status, operation.CreatedAt, operation.FinishedAt = "fop_create_storage_succeeded_"+stableSuffix(volume.ID)[:12], "succeeded", now, now
	fillOperationResource(&operation, volume)
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
}

func TestDestroyStorageVolumeDoesNotRedispatchFailedTerminateNotAttemptedPhase(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	resource := storageDestroyTestVolume("storage-destroy-safe-redispatch")
	appendSucceededStorageCreate(t, store, resource)
	failed := storageDestroyPhaseResult(resource, "terminate_not_attempted", "0", "ready", "ATTACHED")
	appendFailedStorageDestroy(t, store, failed)

	present := storageDestroyPhaseResult(resource, "terminate_not_attempted", "0", "ready", "UNATTACHED")
	provider := &phasedStorageDestroyProvider{readback: present, destroyResult: exactStorageDestroyAbsence(resource)}
	service := NewServiceWithOperationStore(provider, store)

	result, err := service.DestroyStorageVolume(ctx, resource.ID)
	if err == nil || !strings.Contains(err.Error(), "storage_destroy_recovery_unconfirmed") || result.ID != resource.ID || provider.readbackCalls.Load() != 1 || provider.destroyCalls.Load() != 0 {
		t.Fatalf("at-most-once result=%#v err=%v readbacks=%d destroys=%d", result, err, provider.readbackCalls.Load(), provider.destroyCalls.Load())
	}
	provider.readback = exactStorageDestroyAbsence(resource)
	result, err = service.DestroyStorageVolume(ctx, resource.ID)
	if err != nil || result.Status != "external_deleted" || provider.readbackCalls.Load() != 2 || provider.destroyCalls.Load() != 0 {
		t.Fatalf("absence convergence result=%#v err=%v readbacks=%d destroys=%d", result, err, provider.readbackCalls.Load(), provider.destroyCalls.Load())
	}
}

func TestStorageDispatchAuthorizationSurvivesRestartWithoutRedispatch(t *testing.T) {
	ctx := context.Background()
	for _, testCase := range []struct {
		name        string
		readback    func(StorageVolume) StorageVolume
		readbackErr error
		wantSuccess bool
	}{
		{
			name: "crash before send remains present",
			readback: func(volume StorageVolume) StorageVolume {
				return storageDestroyPhaseResult(volume, "dispatch_authorized_uncertain", "0", "ready", "UNATTACHED")
			},
		},
		{
			name:        "crash after send converges from absence",
			readback:    func(volume StorageVolume) StorageVolume { return exactStorageDestroyAbsence(volume) },
			wantSuccess: true,
		},
		{
			name: "unknown readback remains unconfirmed",
			readback: func(volume StorageVolume) StorageVolume {
				return storageDestroyPhaseResult(volume, "dispatch_authorized_uncertain", "0", "destroying", "UNKNOWN")
			},
			readbackErr: errors.New("Tencent CBS readback unavailable"),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := NewMemoryOperationStore()
			resource := storageDestroyTestVolume("storage-" + stableSuffix(testCase.name)[:12])
			appendSucceededStorageCreate(t, store, resource)
			dispatched := storageDestroyPhaseResult(resource, "dispatch_authorized_uncertain", "0", "destroying", resource.CBSStatus)
			appendStartedStorageDestroy(t, store, dispatched)
			provider := &phasedStorageDestroyProvider{
				destroyResult: exactStorageDestroyAbsence(resource),
				readback:      testCase.readback(dispatched),
				readbackErr:   testCase.readbackErr,
			}
			service := NewServiceWithOperationStore(provider, store)

			result, err := service.DestroyStorageVolume(ctx, resource.ID)
			if provider.destroyCalls.Load() != 0 || provider.readbackCalls.Load() != 1 {
				t.Fatalf("restart dispatches=%d readbacks=%d", provider.destroyCalls.Load(), provider.readbackCalls.Load())
			}
			if testCase.wantSuccess {
				if err != nil || !storageDestroyReadbackConfirmsAbsence(result) {
					t.Fatalf("absence result=%#v err=%v", result, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "storage_destroy_recovery_unconfirmed") || result.ID != resource.ID {
				t.Fatalf("unconfirmed result=%#v err=%v", result, err)
			}
		})
	}
}

func TestStorageDispatchAuthorizationIsPersistedBeforeProviderCall(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	resource := storageDestroyTestVolume("storage-dispatch-order")
	provider := &phasedStorageDestroyProvider{destroyResult: exactStorageDestroyAbsence(resource)}
	provider.destroyHook = func(request StorageVolume) {
		latest, found, err := store.LatestResourceOperation(ctx, "storage_volume", resource.ID)
		var persisted StorageVolume
		if err != nil || !found || latest.Status != "started" || !decodeOperationResource(latest, &persisted) ||
			persisted.ProviderData["storageDestroyPhase"] != storageDestroyPhaseDispatchAuthorized ||
			request.ProviderData["storageDestroyPhase"] != storageDestroyPhaseDispatchAuthorized {
			t.Fatalf("provider called before durable dispatch authority: latest=%#v request=%#v found=%v err=%v", latest, request, found, err)
		}
	}
	service := NewServiceWithOperationStore(provider, store)
	service.volumes[resource.ID] = cloneStorageVolume(resource)

	result, err := service.DestroyStorageVolume(ctx, resource.ID)
	if err != nil || !storageDestroyReadbackConfirmsAbsence(result) || provider.destroyCalls.Load() != 1 {
		t.Fatalf("destroy result=%#v err=%v calls=%d", result, err, provider.destroyCalls.Load())
	}
}

func TestDestroyStorageVolumeNeverRedispatchesFailedTerminateAttemptedPhase(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	resource := storageDestroyTestVolume("storage-destroy-attempted-readback")
	appendSucceededStorageCreate(t, store, resource)
	failed := storageDestroyPhaseResult(resource, "terminate_attempted", "1", "ready", "UNATTACHED")
	appendFailedStorageDestroy(t, store, failed)

	provider := &phasedStorageDestroyProvider{readback: failed, destroyResult: exactStorageDestroyAbsence(resource)}
	service := NewServiceWithOperationStore(provider, store)

	result, err := service.DestroyStorageVolume(ctx, resource.ID)
	if err == nil || !strings.Contains(err.Error(), "storage_destroy_recovery_unconfirmed") || result.ID != resource.ID || provider.readbackCalls.Load() != 1 || provider.destroyCalls.Load() != 0 {
		t.Fatalf("attempted phase result=%#v err=%v readbacks=%d destroys=%d", result, err, provider.readbackCalls.Load(), provider.destroyCalls.Load())
	}

	provider.readback = exactStorageDestroyAbsence(resource)
	result, err = service.DestroyStorageVolume(ctx, resource.ID)
	if err != nil || result.Status != "external_deleted" || provider.readbackCalls.Load() != 2 || provider.destroyCalls.Load() != 0 {
		t.Fatalf("absence convergence result=%#v err=%v readbacks=%d destroys=%d", result, err, provider.readbackCalls.Load(), provider.destroyCalls.Load())
	}
}

func TestTencentStorageDestroyAbsenceRequiresExactEvidence(t *testing.T) {
	resource := storageDestroyTestVolume("storage-destroy-exact-absence")
	valid := exactStorageDestroyAbsence(resource)
	if !storageDestroyReadbackConfirmsAbsence(valid) {
		t.Fatalf("exact absence rejected: %#v", valid)
	}

	for _, testCase := range []struct {
		name   string
		mutate func(*StorageVolume)
	}{
		{name: "missing describe request", mutate: func(volume *StorageVolume) { delete(volume.ProviderData, "describeCbsRequestId") }},
		{name: "wrong storage volume", mutate: func(volume *StorageVolume) { volume.ProviderData["storageVolumeId"] = "disk-other" }},
		{name: "wrong cbs status", mutate: func(volume *StorageVolume) { volume.ProviderData["cbsStatus"] = "UNATTACHED" }},
		{name: "missing status", mutate: func(volume *StorageVolume) { delete(volume.ProviderData, "status") }},
		{name: "wrong status", mutate: func(volume *StorageVolume) { volume.ProviderData["status"] = "retained" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := cloneStorageVolume(valid)
			testCase.mutate(&candidate)
			if storageDestroyReadbackConfirmsAbsence(candidate) {
				t.Fatalf("invalid absence accepted: %#v", candidate)
			}
		})
	}
}

func TestDestroyStorageVolumeDoesNotRedispatchUnknownFailedPhase(t *testing.T) {
	ctx := context.Background()
	for _, failed := range []StorageVolume{
		storageDestroyPhaseResult(storageDestroyTestVolume("storage-destroy-missing-phase"), "", "0", "ready", "ATTACHED"),
		storageDestroyPhaseResult(storageDestroyTestVolume("storage-destroy-wrong-count"), "terminate_not_attempted", "1", "ready", "ATTACHED"),
	} {
		t.Run(failed.ID, func(t *testing.T) {
			if failed.ProviderData["storageDestroyPhase"] == "" {
				delete(failed.ProviderData, "storageDestroyPhase")
			}
			store := NewMemoryOperationStore()
			appendSucceededStorageCreate(t, store, storageDestroyTestVolume(failed.ID))
			appendFailedStorageDestroy(t, store, failed)
			provider := &phasedStorageDestroyProvider{readback: failed, readbackErr: errors.New("readback unavailable"), destroyResult: exactStorageDestroyAbsence(failed)}
			service := NewServiceWithOperationStore(provider, store)

			if _, err := service.DestroyStorageVolume(ctx, failed.ID); err == nil || provider.destroyCalls.Load() != 0 {
				t.Fatalf("untrusted failed phase err=%v destroys=%d", err, provider.destroyCalls.Load())
			}
		})
	}
}
