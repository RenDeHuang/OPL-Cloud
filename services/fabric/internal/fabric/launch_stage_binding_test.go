package fabric

import (
	"context"
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
