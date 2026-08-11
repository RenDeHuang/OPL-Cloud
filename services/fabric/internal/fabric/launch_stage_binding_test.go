package fabric

import (
	"context"
	"errors"
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

func TestLaunchStageBindingReadbackUsesExactOperationIdentity(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	binding := testWorkspaceLaunchBinding("storage", "ensure_storage", "launch-alpha:storage")
	operation := newOperation("create_storage_volume", "storage_volume", "vol-alpha", binding.AccountID, binding.WorkspaceID, binding.IdempotencyKey, binding.RequestHash, time.Now().UTC())
	operation.Status, operation.CreatedAt = "started", time.Now().UTC()
	if err := bindLaunchStageOperation(&operation, &binding); err != nil {
		t.Fatal(err)
	}
	fillOperationResource(&operation, StorageVolume{ID: "vol-alpha", AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID, Status: "pending"})
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}

	started, err := service.LaunchStageBindingReadback(context.Background(), binding)
	if err != nil || started.Available || started.Status != "started" || started.Operation.ID != binding.FabricOperationID {
		t.Fatalf("started readback = %#v, %v", started, err)
	}

	operation.Status, operation.FinishedAt = "succeeded", time.Now().UTC()
	fillOperationResource(&operation, StorageVolume{ID: "vol-alpha", AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID, Status: "ready"})
	if err := store.SaveRuntime(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	finished, err := service.LaunchStageBindingReadback(context.Background(), binding)
	if err != nil || !finished.Available || finished.Status != "succeeded" || finished.Binding != binding {
		t.Fatalf("finished readback = %#v, %v", finished, err)
	}
}

func TestLaunchStageBindingReadbackRejectsAnyCallerFieldDrift(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	binding := testWorkspaceLaunchBinding("storage", "ensure_storage", "launch-alpha:storage")
	operation := newOperation("create_storage_volume", "storage_volume", "vol-alpha", binding.AccountID, binding.WorkspaceID, binding.IdempotencyKey, binding.RequestHash, time.Now().UTC())
	operation.Status = "started"
	if err := bindLaunchStageOperation(&operation, &binding); err != nil {
		t.Fatal(err)
	}
	_ = store.Append(context.Background(), operation)

	tests := []struct {
		name   string
		mutate func(*WorkspaceLaunchStageBinding)
	}{
		{name: "launch operation", mutate: func(value *WorkspaceLaunchStageBinding) { value.LaunchOperationID += "-other" }},
		{name: "account", mutate: func(value *WorkspaceLaunchStageBinding) { value.AccountID += "-other" }},
		{name: "workspace", mutate: func(value *WorkspaceLaunchStageBinding) { value.WorkspaceID += "-other" }},
		{name: "fabric operation", mutate: func(value *WorkspaceLaunchStageBinding) { value.FabricOperationID += "-other" }},
		{name: "idempotency key", mutate: func(value *WorkspaceLaunchStageBinding) { value.IdempotencyKey += "-other" }},
		{name: "request hash", mutate: func(value *WorkspaceLaunchStageBinding) { value.RequestHash += "-other" }},
		{name: "expected resource binding", mutate: func(value *WorkspaceLaunchStageBinding) { value.ExpectedResourceBinding = "fabric-operation:other" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			drifted := binding
			test.mutate(&drifted)
			_, err := service.LaunchStageBindingReadback(context.Background(), drifted)
			if !errors.Is(err, ErrLaunchStageBindingConflict) && !errors.Is(err, ErrLaunchStageBindingNotFound) {
				t.Fatalf("conflict error = %v", err)
			}
		})
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

func TestLaunchStageBindingReadbackDoesNotListOperations(t *testing.T) {
	store := NewMemoryOperationStore()
	binding := testWorkspaceLaunchBinding("storage", "ensure_storage", "launch-alpha:storage")
	operation := newOperation("create_storage_volume", "storage_volume", "vol-alpha", binding.AccountID, binding.WorkspaceID, binding.IdempotencyKey, binding.RequestHash, time.Now().UTC())
	operation.Status = "started"
	if err := bindLaunchStageOperation(&operation, &binding); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithOperationStore(testProvider{}, failingListOperationStore{OperationStore: store})
	readback, err := service.LaunchStageBindingReadback(context.Background(), binding)
	if err != nil || readback.Operation.ID != binding.FabricOperationID {
		t.Fatalf("readback=%#v err=%v", readback, err)
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
