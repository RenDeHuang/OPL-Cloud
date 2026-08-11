package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
)

const launchStageBindingPayloadKey = "launchStageBinding"

var (
	ErrLaunchStageBindingNotFound = errors.New("launch_stage_binding_not_found")
	ErrLaunchStageBindingConflict = errors.New("launch_stage_binding_conflict")
)

type LaunchStageBinding struct {
	SchemaVersion       int               `json:"schemaVersion"`
	LaunchOperationID   string            `json:"launchOperationId"`
	AccountID           string            `json:"accountId"`
	WorkspaceID         string            `json:"workspaceId"`
	Stage               string            `json:"stage"`
	Action              string            `json:"action"`
	StageOperationID    string            `json:"stageOperationId"`
	IdempotencyKey      string            `json:"idempotencyKey"`
	RequestHash         string            `json:"requestHash"`
	ExpectedResourceIDs map[string]string `json:"expectedResourceIds"`
	Digest              string            `json:"digest"`
}

type LaunchStageBindingReadback struct {
	Available bool               `json:"available"`
	Status    string             `json:"status"`
	Binding   LaunchStageBinding `json:"binding"`
	Operation FabricOperation    `json:"operation"`
}

func launchOperationID(idempotencyKey, stage string) (string, bool) {
	suffixes := map[string][]string{
		"compute":    {":compute"},
		"storage":    {":storage"},
		"attachment": {":attachment"},
		"secret":     {":secret", ":gateway-secret"},
		"runtime":    {":runtime", ":workspace:runtime"},
	}
	key := strings.TrimSpace(idempotencyKey)
	for _, suffix := range suffixes[stage] {
		if launchID, ok := strings.CutSuffix(key, suffix); ok && launchID != "" {
			return launchID, true
		}
	}
	return "", false
}

func bindLaunchStageOperation(operation *FabricOperation, stage string, expectedResourceIDs map[string]string) {
	launchID, ok := launchOperationID(operation.IdempotencyKey, stage)
	if !ok {
		return
	}
	binding := LaunchStageBinding{
		SchemaVersion: 1, LaunchOperationID: launchID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
		Stage: stage, Action: operation.Action, StageOperationID: operation.OperationID,
		IdempotencyKey: operation.IdempotencyKey, RequestHash: operation.RequestHash,
		ExpectedResourceIDs: maps.Clone(expectedResourceIDs),
	}
	binding.Digest = hashInput(struct {
		SchemaVersion       int
		LaunchOperationID   string
		AccountID           string
		WorkspaceID         string
		Stage               string
		Action              string
		StageOperationID    string
		IdempotencyKey      string
		RequestHash         string
		ExpectedResourceIDs map[string]string
	}{binding.SchemaVersion, binding.LaunchOperationID, binding.AccountID, binding.WorkspaceID, binding.Stage, binding.Action, binding.StageOperationID, binding.IdempotencyKey, binding.RequestHash, binding.ExpectedResourceIDs})
	if operation.RedactedProviderPayload == nil {
		operation.RedactedProviderPayload = map[string]any{}
	}
	operation.RedactedProviderPayload[launchStageBindingPayloadKey] = binding
}

func decodeLaunchStageBinding(operation FabricOperation) (LaunchStageBinding, bool) {
	value, ok := operation.RedactedProviderPayload[launchStageBindingPayloadKey]
	if !ok {
		return LaunchStageBinding{}, false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return LaunchStageBinding{}, false
	}
	var binding LaunchStageBinding
	if json.Unmarshal(body, &binding) != nil || binding.SchemaVersion != 1 || binding.Digest == "" {
		return LaunchStageBinding{}, false
	}
	copy := binding
	copy.Digest = ""
	expected := hashInput(struct {
		SchemaVersion       int
		LaunchOperationID   string
		AccountID           string
		WorkspaceID         string
		Stage               string
		Action              string
		StageOperationID    string
		IdempotencyKey      string
		RequestHash         string
		ExpectedResourceIDs map[string]string
	}{copy.SchemaVersion, copy.LaunchOperationID, copy.AccountID, copy.WorkspaceID, copy.Stage, copy.Action, copy.StageOperationID, copy.IdempotencyKey, copy.RequestHash, copy.ExpectedResourceIDs})
	return binding, expected == binding.Digest
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

func (s *Service) LaunchStageBindingReadback(ctx context.Context, launchOperationID, stage string) (LaunchStageBindingReadback, error) {
	if strings.TrimSpace(launchOperationID) == "" || strings.TrimSpace(stage) == "" {
		return LaunchStageBindingReadback{}, ErrLaunchStageBindingNotFound
	}
	operations, err := s.operations.List(ctx)
	if err != nil {
		return LaunchStageBindingReadback{}, err
	}
	var selected FabricOperation
	var selectedBinding LaunchStageBinding
	for _, operation := range operations {
		binding, ok := decodeLaunchStageBinding(operation)
		if !ok || binding.LaunchOperationID != launchOperationID || binding.Stage != stage {
			continue
		}
		if binding.AccountID != operation.AccountID || binding.WorkspaceID != operation.WorkspaceID || binding.Action != operation.Action ||
			binding.StageOperationID != operation.OperationID || binding.IdempotencyKey != operation.IdempotencyKey || binding.RequestHash != operation.RequestHash {
			return LaunchStageBindingReadback{}, ErrLaunchStageBindingConflict
		}
		if selectedBinding.Digest != "" && selectedBinding.Digest != binding.Digest {
			return LaunchStageBindingReadback{}, ErrLaunchStageBindingConflict
		}
		selected, selectedBinding = operation, binding
	}
	if selectedBinding.Digest == "" {
		return LaunchStageBindingReadback{}, fmt.Errorf("%w: %s", ErrLaunchStageBindingNotFound, stage)
	}
	return LaunchStageBindingReadback{Available: selected.Status == "succeeded", Status: selected.Status, Binding: selectedBinding, Operation: selected}, nil
}
