package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const providerMutationBindingPayloadKey = "providerMutationBinding"
const providerMutationStatePayloadKey = "providerMutationState"

type providerMutationJournalContextKey struct{}

type providerMutationJournal struct {
	operations      OperationStore
	parent          WorkspaceLaunchStageBinding
	parentOperation FabricOperation
	provider        string
	now             func() time.Time
}

type providerMutationBinding struct {
	SchemaVersion           int                         `json:"schemaVersion"`
	Parent                  WorkspaceLaunchStageBinding `json:"parent"`
	FabricOperationID       string                      `json:"fabricOperationId"`
	Action                  string                      `json:"action"`
	ResourceKind            string                      `json:"resourceKind"`
	ResourceID              string                      `json:"resourceId"`
	ExpectedResourceBinding string                      `json:"expectedResourceBinding"`
}

type providerMutationAttempt struct {
	journal   *providerMutationJournal
	operation FabricOperation
	Fresh     bool
}

type persistedProviderMutationBinding struct {
	Binding providerMutationBinding `json:"binding"`
	Digest  string                  `json:"digest"`
}

type persistedProviderMutationState struct {
	Value  json.RawMessage `json:"value"`
	Digest string          `json:"digest"`
}

func providerMutationJournalFromContext(ctx context.Context) *providerMutationJournal {
	journal, _ := ctx.Value(providerMutationJournalContextKey{}).(*providerMutationJournal)
	return journal
}

func (s *Service) providerMutationContext(ctx context.Context, operation FabricOperation) context.Context {
	binding, ok := decodeLaunchStageBinding(operation)
	if !ok {
		return ctx
	}
	return context.WithValue(ctx, providerMutationJournalContextKey{}, &providerMutationJournal{
		operations: s.operations, parent: binding, parentOperation: operation, provider: s.provider.Descriptor().Name, now: s.now,
	})
}

func providerMutationOperationID(parent WorkspaceLaunchStageBinding, action, resourceKind, resourceID, expectedBinding string) string {
	return parent.FabricOperationID + ":provider:" + stableSuffix(action, resourceKind, resourceID, expectedBinding)[:16]
}

func beginProviderMutation(ctx context.Context, action, resourceKind, resourceID, expectedBinding string) (*providerMutationAttempt, error) {
	return beginProviderMutationWithState(ctx, action, resourceKind, resourceID, expectedBinding, nil)
}

func beginProviderMutationWithState(ctx context.Context, action, resourceKind, resourceID, expectedBinding string, state any) (*providerMutationAttempt, error) {
	journal := providerMutationJournalFromContext(ctx)
	if journal == nil {
		return nil, nil
	}
	if action == "" || resourceKind == "" || resourceID == "" {
		return nil, fmt.Errorf("provider_mutation_binding_invalid")
	}
	binding := providerMutationBinding{
		SchemaVersion: 1, Parent: journal.parent, Action: action, ResourceKind: resourceKind,
		ResourceID: resourceID, ExpectedResourceBinding: expectedBinding,
	}
	binding.FabricOperationID = providerMutationOperationID(journal.parent, action, resourceKind, resourceID, expectedBinding)
	now := journal.now()
	operation := newOperation(action, resourceKind, resourceID, journal.parent.AccountID, journal.parent.WorkspaceID, binding.FabricOperationID, hashInput(binding), now)
	operation.ID, operation.OperationID = binding.FabricOperationID, binding.FabricOperationID
	operation.Provider, operation.Status, operation.CreatedAt = journal.provider, "started", now
	operation.RedactedProviderPayload = map[string]any{
		providerMutationBindingPayloadKey: persistedProviderMutationBinding{Binding: binding, Digest: hashInput(binding)},
	}
	if state != nil {
		body, err := json.Marshal(state)
		if err != nil {
			return nil, err
		}
		operation.RedactedProviderPayload[providerMutationStatePayloadKey] = persistedProviderMutationState{Value: body, Digest: hashInput(json.RawMessage(body))}
	}
	current, err := journal.operations.Get(ctx, operation.ID)
	if err == nil {
		persisted, ok := decodeProviderMutationBinding(current)
		if !ok || persisted != binding || !sameProviderMutationState(current, operation) {
			return nil, ErrLaunchStageBindingConflict
		}
		return &providerMutationAttempt{journal: journal, operation: current}, nil
	}
	if !errors.Is(err, ErrOperationNotFound) {
		return nil, err
	}
	if err := journal.operations.Append(ctx, operation); err != nil {
		concurrent, getErr := journal.operations.Get(ctx, operation.ID)
		if getErr != nil || concurrent.RequestHash != operation.RequestHash {
			return nil, err
		}
		return &providerMutationAttempt{journal: journal, operation: concurrent}, nil
	}
	return &providerMutationAttempt{journal: journal, operation: operation, Fresh: true}, nil
}

func sameProviderMutationState(current, expected FabricOperation) bool {
	currentState, currentOK := current.RedactedProviderPayload[providerMutationStatePayloadKey]
	expectedState, expectedOK := expected.RedactedProviderPayload[providerMutationStatePayloadKey]
	if currentOK != expectedOK {
		return false
	}
	if !currentOK {
		return true
	}
	return hashInput(currentState) == hashInput(expectedState)
}

func decodeProviderMutationBinding(operation FabricOperation) (providerMutationBinding, bool) {
	value, ok := operation.RedactedProviderPayload[providerMutationBindingPayloadKey]
	if !ok {
		return providerMutationBinding{}, false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return providerMutationBinding{}, false
	}
	var persisted persistedProviderMutationBinding
	if json.Unmarshal(body, &persisted) != nil || persisted.Binding.SchemaVersion != 1 ||
		persisted.Digest == "" || persisted.Digest != hashInput(persisted.Binding) {
		return providerMutationBinding{}, false
	}
	binding := persisted.Binding
	if operation.ID != binding.FabricOperationID || operation.OperationID != binding.FabricOperationID ||
		operation.Action != binding.Action || operation.ResourceKind != binding.ResourceKind || operation.ResourceID != binding.ResourceID ||
		operation.AccountID != binding.Parent.AccountID || operation.WorkspaceID != binding.Parent.WorkspaceID ||
		operation.IdempotencyKey != binding.FabricOperationID || operation.RequestHash != hashInput(binding) {
		return providerMutationBinding{}, false
	}
	return binding, true
}

func (a *providerMutationAttempt) resource(target any) bool {
	return a != nil && decodeOperationResource(a.operation, target)
}

func (a *providerMutationAttempt) state(target any) bool {
	if a == nil {
		return false
	}
	return decodeProviderMutationState(a.operation, target)
}

func decodeProviderMutationState(operation FabricOperation, target any) bool {
	value, ok := operation.RedactedProviderPayload[providerMutationStatePayloadKey]
	if !ok {
		return false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return false
	}
	var persisted persistedProviderMutationState
	if json.Unmarshal(body, &persisted) != nil || persisted.Digest == "" || persisted.Digest != hashInput(persisted.Value) {
		return false
	}
	return json.Unmarshal(persisted.Value, target) == nil
}

func (a *providerMutationAttempt) complete(ctx context.Context, providerRequestID string, resource any, mutationErr error) error {
	if a == nil || (!a.Fresh && a.operation.Status == "succeeded") {
		return nil
	}
	next := a.operation
	next.ProviderRequestID = providerRequestID
	next.FinishedAt = a.journal.now()
	fillOperationResource(&next, resource)
	if mutationErr == nil {
		next.Status = "succeeded"
	} else {
		next.Status, next.ErrorCode = "failed", errorCode(mutationErr)
	}
	if !a.Fresh && a.operation.Status == "failed" {
		if mutationErr != nil {
			return nil
		}
		converger, ok := a.journal.operations.(runtimeReadbackConverger)
		if !ok {
			return ErrRuntimeOperationNotCurrent
		}
		return converger.ConvergeRuntimeReadback(ctx, a.operation, next)
	}
	return a.journal.operations.SaveRuntime(ctx, next)
}
