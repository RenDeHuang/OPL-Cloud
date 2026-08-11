package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

const launchStageBindingPayloadKey = "launchStageBinding"

var (
	ErrLaunchStageBindingInvalid  = errors.New("launch_stage_binding_invalid")
	ErrLaunchStageBindingNotFound = errors.New("launch_stage_binding_not_found")
	ErrLaunchStageBindingConflict = errors.New("launch_stage_binding_conflict")
)

// WorkspaceLaunchStageBinding mirrors the real Control Plane caller. It is
// provider-neutral and is persisted unchanged before a provider mutation.
type WorkspaceLaunchStageBinding struct {
	SchemaVersion           int    `json:"schemaVersion"`
	LaunchOperationID       string `json:"launchOperationId"`
	AccountID               string `json:"accountId"`
	WorkspaceID             string `json:"workspaceId"`
	Stage                   string `json:"stage"`
	Action                  string `json:"action"`
	FabricOperationID       string `json:"fabricOperationId"`
	IdempotencyKey          string `json:"idempotencyKey"`
	RequestHash             string `json:"requestHash"`
	ExpectedResourceBinding string `json:"expectedResourceBinding"`
}

type persistedLaunchStageBinding struct {
	Binding WorkspaceLaunchStageBinding `json:"binding"`
	Digest  string                      `json:"digest"`
}

type LaunchStageBindingReadback struct {
	Available bool                        `json:"available"`
	Status    string                      `json:"status"`
	Binding   WorkspaceLaunchStageBinding `json:"binding"`
	Operation FabricOperation             `json:"operation"`
}

func workspaceLaunchStageAction(stage string) (string, bool) {
	action, ok := map[string]string{
		"ensure_compute_allocation": "ensure_compute_allocation",
		"storage":                   "ensure_storage",
		"attachment":                "ensure_attachment",
		"secret":                    "ensure_gateway_secret",
		"runtime":                   "ensure_runtime",
	}[stage]
	return action, ok
}

func validWorkspaceLaunchStageBinding(binding WorkspaceLaunchStageBinding) bool {
	action, ok := workspaceLaunchStageAction(binding.Stage)
	if !ok || binding.SchemaVersion != 1 || binding.Action != action {
		return false
	}
	for _, value := range []string{
		binding.LaunchOperationID, binding.AccountID, binding.WorkspaceID, binding.Stage,
		binding.Action, binding.FabricOperationID, binding.IdempotencyKey, binding.RequestHash,
	} {
		if value == "" || value != strings.TrimSpace(value) {
			return false
		}
	}
	return binding.ExpectedResourceBinding == strings.TrimSpace(binding.ExpectedResourceBinding)
}

func bindLaunchStageOperation(operation *FabricOperation, binding *WorkspaceLaunchStageBinding) error {
	if binding == nil {
		return nil
	}
	if !validWorkspaceLaunchStageBinding(*binding) || operation.AccountID != binding.AccountID ||
		operation.WorkspaceID != binding.WorkspaceID || operation.IdempotencyKey != binding.IdempotencyKey {
		return ErrLaunchStageBindingInvalid
	}
	operation.ID = binding.FabricOperationID
	operation.RequestHash = binding.RequestHash
	if operation.RedactedProviderPayload == nil {
		operation.RedactedProviderPayload = map[string]any{}
	}
	operation.RedactedProviderPayload[launchStageBindingPayloadKey] = persistedLaunchStageBinding{
		Binding: *binding,
		Digest:  hashInput(*binding),
	}
	return nil
}

func decodeLaunchStageBinding(operation FabricOperation) (WorkspaceLaunchStageBinding, bool) {
	value, ok := operation.RedactedProviderPayload[launchStageBindingPayloadKey]
	if !ok {
		return WorkspaceLaunchStageBinding{}, false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return WorkspaceLaunchStageBinding{}, false
	}
	var persisted persistedLaunchStageBinding
	if json.Unmarshal(body, &persisted) != nil || !validWorkspaceLaunchStageBinding(persisted.Binding) ||
		persisted.Digest == "" || persisted.Digest != hashInput(persisted.Binding) {
		return WorkspaceLaunchStageBinding{}, false
	}
	binding := persisted.Binding
	if operation.ID != binding.FabricOperationID || operation.AccountID != binding.AccountID ||
		operation.WorkspaceID != binding.WorkspaceID || operation.IdempotencyKey != binding.IdempotencyKey ||
		operation.RequestHash != binding.RequestHash {
		return WorkspaceLaunchStageBinding{}, false
	}
	return binding, true
}

func preserveLaunchStageBinding(next, current map[string]any) map[string]any {
	if next == nil {
		next = map[string]any{}
	}
	if binding := current[launchStageBindingPayloadKey]; binding != nil {
		next[launchStageBindingPayloadKey] = binding
	}
	return next
}

func (s *Service) LaunchStageBindingReadback(ctx context.Context, expected WorkspaceLaunchStageBinding) (LaunchStageBindingReadback, error) {
	if !validWorkspaceLaunchStageBinding(expected) {
		return LaunchStageBindingReadback{}, ErrLaunchStageBindingInvalid
	}
	operation, err := s.operations.Get(ctx, expected.FabricOperationID)
	if errors.Is(err, ErrOperationNotFound) {
		return LaunchStageBindingReadback{}, ErrLaunchStageBindingNotFound
	}
	if err != nil {
		return LaunchStageBindingReadback{}, err
	}
	binding, ok := decodeLaunchStageBinding(operation)
	if !ok || binding != expected {
		return LaunchStageBindingReadback{}, ErrLaunchStageBindingConflict
	}
	return LaunchStageBindingReadback{
		Available: operation.Status == "succeeded", Status: operation.Status, Binding: binding, Operation: operation,
	}, nil
}
