package fabric

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type workspaceRuntimeDeleteObservationProviderForTest struct {
	testProvider
	observation WorkspaceRuntimeDeleteObservation
	err         error
}

func (p workspaceRuntimeDeleteObservationProviderForTest) ObserveWorkspaceRuntimeDelete(_ context.Context, _ string) (WorkspaceRuntimeDeleteObservation, error) {
	return p.observation, p.err
}

func TestObserveWorkspaceRuntimeDeleteUsesTypedProviderObservation(t *testing.T) {
	want := WorkspaceRuntimeDeleteObservation{
		SchemaVersion: WorkspaceRuntimeDeleteObservationSchemaVersion,
		State:         WorkspaceRuntimeDeleteObservationPresent,
		WorkspaceID:   "workspace-alpha",
		Residuals: []WorkspaceRuntimeDeleteResidual{
			{Kind: "NetworkPolicy", Name: "runtime-alpha"},
		},
	}
	service := NewService(workspaceRuntimeDeleteObservationProviderForTest{observation: want})
	got := service.ObserveWorkspaceRuntimeDelete(context.Background(), "workspace-alpha")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("observation=%#v want=%#v", got, want)
	}
}

func TestObserveWorkspaceRuntimeDeleteFailsClosedOnProviderErrorOrInvalidObservation(t *testing.T) {
	providerError := NewService(workspaceRuntimeDeleteObservationProviderForTest{err: errors.New("provider unavailable")})
	if got := providerError.ObserveWorkspaceRuntimeDelete(context.Background(), "workspace-alpha"); got.State != WorkspaceOwnerObservationError || len(got.Residuals) != 0 {
		t.Fatalf("provider error observation=%#v", got)
	}
	invalid := NewService(workspaceRuntimeDeleteObservationProviderForTest{observation: WorkspaceRuntimeDeleteObservation{
		SchemaVersion: WorkspaceRuntimeDeleteObservationSchemaVersion,
		State:         WorkspaceRuntimeDeleteObservationPresent,
		WorkspaceID:   "workspace-alpha",
	}})
	if got := invalid.ObserveWorkspaceRuntimeDelete(context.Background(), "workspace-alpha"); got.State != WorkspaceOwnerObservationError || len(got.Residuals) != 0 {
		t.Fatalf("invalid observation=%#v", got)
	}
}

func TestWorkspaceRuntimeDeleteResidualsFromItemsDetectsPartialKubernetesResidue(t *testing.T) {
	items := []any{
		map[string]any{"kind": "NetworkPolicy", "metadata": map[string]any{"name": "runtime-alpha", "labels": map[string]any{"oplcloud.cn/workspace-id": "workspace-alpha"}}},
	}
	residuals, err := workspaceRuntimeDeleteResidualsFromItems(items, "workspace-alpha")
	if err != nil || !reflect.DeepEqual(residuals, []WorkspaceRuntimeDeleteResidual{{Kind: "NetworkPolicy", Name: "runtime-alpha"}}) {
		t.Fatalf("residuals=%#v err=%v", residuals, err)
	}
}

func TestWorkspaceRuntimeDeleteResidualsFromItemsRejectsAmbiguousOwnership(t *testing.T) {
	items := []any{
		map[string]any{"kind": "Service", "metadata": map[string]any{"name": "runtime-a", "labels": map[string]any{"oplcloud.cn/workspace-id": "workspace-alpha"}}},
		map[string]any{"kind": "Service", "metadata": map[string]any{"name": "runtime-b", "labels": map[string]any{"oplcloud.cn/workspace-id": "workspace-alpha"}}},
	}
	if _, err := workspaceRuntimeDeleteResidualsFromItems(items, "workspace-alpha"); !errors.Is(err, ErrLaunchStageBindingConflict) {
		t.Fatalf("err=%v", err)
	}
}
