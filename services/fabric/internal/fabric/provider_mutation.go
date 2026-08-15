package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"
)

const providerMutationBindingPayloadKey = "providerMutationBinding"
const providerMutationStatePayloadKey = "providerMutationState"
const providerMutationReplayEpochPayloadKey = "providerMutationReplayEpoch"

const providerMutationReplayLease = 30 * time.Second

type providerMutationJournalContextKey struct{}

type providerMutationJournal struct {
	operations      OperationStore
	parent          WorkspaceLaunchStageBinding
	parentOperation FabricOperation
	provider        string
	now             func() time.Time
	readOnly        bool
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
	Replay    bool
}

type providerMutationReplayEpoch struct {
	SchemaVersion           int    `json:"schemaVersion"`
	ReplayID                string `json:"replayId"`
	ParentFabricOperationID string `json:"parentFabricOperationId"`
	ChildOperationID        string `json:"childOperationId"`
	IdempotencyKey          string `json:"idempotencyKey"`
	State                   string `json:"state"`
	LeaseGeneration         int    `json:"leaseGeneration"`
	LeaseExpiresAt          string `json:"leaseExpiresAt"`
	DispatchStartedAt       string `json:"dispatchStartedAt,omitempty"`
	CompletedAt             string `json:"completedAt,omitempty"`
}

type providerMutationReplayStore interface {
	SaveProviderMutationReplayEpoch(context.Context, FabricOperation, FabricOperation) error
	ConvergeProviderMutationReplay(context.Context, FabricOperation, FabricOperation) error
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
	return s.providerOperationContext(ctx, operation, false)
}

func (s *Service) providerReadContext(ctx context.Context, operation FabricOperation) context.Context {
	return s.providerOperationContext(ctx, operation, true)
}

func (s *Service) providerOperationContext(ctx context.Context, operation FabricOperation, readOnly bool) context.Context {
	binding, ok := decodeLaunchStageBinding(operation)
	if !ok {
		return ctx
	}
	return context.WithValue(ctx, providerMutationJournalContextKey{}, &providerMutationJournal{
		operations: s.operations, parent: binding, parentOperation: operation, provider: s.provider.Descriptor().Name, now: s.now, readOnly: readOnly,
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
	if journal.readOnly {
		return nil, fmt.Errorf("provider_mutation_forbidden_in_read")
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

func providerMutationReplayID(operation FabricOperation, binding providerMutationBinding) string {
	return "replay_" + stableSuffix(binding.Parent.FabricOperationID, operation.ID, operation.IdempotencyKey)[:24]
}

func decodeProviderMutationReplayEpoch(operation FabricOperation) (providerMutationReplayEpoch, bool) {
	value, ok := operation.RedactedProviderPayload[providerMutationReplayEpochPayloadKey]
	if !ok {
		return providerMutationReplayEpoch{}, false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return providerMutationReplayEpoch{}, false
	}
	var epoch providerMutationReplayEpoch
	binding, bindingOK := decodeProviderMutationBinding(operation)
	if json.Unmarshal(body, &epoch) != nil || !bindingOK || epoch.SchemaVersion != 1 || epoch.ReplayID != providerMutationReplayID(operation, binding) ||
		epoch.ParentFabricOperationID != binding.Parent.FabricOperationID || epoch.ChildOperationID != operation.ID ||
		epoch.IdempotencyKey != operation.IdempotencyKey || epoch.LeaseGeneration <= 0 {
		return providerMutationReplayEpoch{}, false
	}
	if _, err := time.Parse(time.RFC3339Nano, epoch.LeaseExpiresAt); err != nil {
		return providerMutationReplayEpoch{}, false
	}
	switch epoch.State {
	case "leased":
		if epoch.CompletedAt != "" {
			return providerMutationReplayEpoch{}, false
		}
		if epoch.DispatchStartedAt != "" {
			if _, err := time.Parse(time.RFC3339Nano, epoch.DispatchStartedAt); err != nil {
				return providerMutationReplayEpoch{}, false
			}
		}
	case "awaiting_readback":
		if epoch.DispatchStartedAt == "" || epoch.CompletedAt != "" {
			return providerMutationReplayEpoch{}, false
		}
		if _, err := time.Parse(time.RFC3339Nano, epoch.DispatchStartedAt); err != nil {
			return providerMutationReplayEpoch{}, false
		}
	case "succeeded":
		if epoch.CompletedAt == "" {
			return providerMutationReplayEpoch{}, false
		}
		if _, err := time.Parse(time.RFC3339Nano, epoch.CompletedAt); err != nil {
			return providerMutationReplayEpoch{}, false
		}
	case "blocked":
		if epoch.DispatchStartedAt != "" || epoch.CompletedAt == "" {
			return providerMutationReplayEpoch{}, false
		}
		if _, err := time.Parse(time.RFC3339Nano, epoch.CompletedAt); err != nil {
			return providerMutationReplayEpoch{}, false
		}
	default:
		return providerMutationReplayEpoch{}, false
	}
	return epoch, true
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
	if !a.Replay && mutationErr == nil {
		if epoch, ok := decodeProviderMutationReplayEpoch(a.operation); ok && epoch.State != "succeeded" {
			a.Replay = true
		}
	}
	next := a.operation
	next.ProviderRequestID = providerRequestID
	next.FinishedAt = a.journal.now()
	fillOperationResource(&next, resource)
	if a.Replay {
		epoch, ok := decodeProviderMutationReplayEpoch(a.operation)
		if !ok {
			return ErrLaunchStageBindingConflict
		}
		store, ok := a.journal.operations.(providerMutationReplayStore)
		if !ok {
			return ErrRuntimeOperationNotCurrent
		}
		if mutationErr != nil {
			if epoch.State == "leased" {
				if epoch.DispatchStartedAt == "" {
					epoch.State = "blocked"
					epoch.CompletedAt = a.journal.now().UTC().Format(time.RFC3339Nano)
				} else {
					epoch.State = "awaiting_readback"
				}
				next = a.operation
				next.RedactedProviderPayload = maps.Clone(a.operation.RedactedProviderPayload)
				next.RedactedProviderPayload[providerMutationReplayEpochPayloadKey] = epoch
				return store.SaveProviderMutationReplayEpoch(ctx, a.operation, next)
			}
			return nil
		}
		epoch.State = "succeeded"
		epoch.CompletedAt = a.journal.now().UTC().Format(time.RFC3339Nano)
		next.RedactedProviderPayload = maps.Clone(next.RedactedProviderPayload)
		next.RedactedProviderPayload[providerMutationReplayEpochPayloadKey] = epoch
		next.Status, next.ErrorCode = "succeeded", ""
		return store.ConvergeProviderMutationReplay(ctx, a.operation, next)
	}
	if mutationErr == nil {
		next.Status = "succeeded"
	} else {
		next.Status, next.ErrorCode = "failed", errorCode(mutationErr)
	}
	if !a.Fresh && a.operation.Status == "started" && mutationErr == nil {
		converger, ok := a.journal.operations.(runtimeReadbackConverger)
		if !ok {
			return ErrRuntimeOperationNotCurrent
		}
		return converger.ConvergeRuntimeReadback(ctx, a.operation, next)
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

func (a *providerMutationAttempt) claimReplay(ctx context.Context) (bool, error) {
	if a == nil || a.journal == nil || a.Fresh || a.operation.Status != "started" && a.operation.Status != "failed" {
		return false, nil
	}
	store, ok := a.journal.operations.(providerMutationReplayStore)
	if !ok {
		return false, ErrRuntimeOperationNotCurrent
	}
	now := a.journal.now().UTC()
	binding, validBinding := decodeProviderMutationBinding(a.operation)
	if !validBinding || binding.Parent.FabricOperationID == "" {
		return false, ErrLaunchStageBindingConflict
	}
	generation := 1
	dispatchStartedAt := ""
	if _, exists := a.operation.RedactedProviderPayload[providerMutationReplayEpochPayloadKey]; exists {
		epoch, valid := decodeProviderMutationReplayEpoch(a.operation)
		if !valid || epoch.State == "succeeded" || epoch.State == "blocked" {
			return false, ErrLaunchStageBindingConflict
		}
		lease, leaseErr := time.Parse(time.RFC3339Nano, epoch.LeaseExpiresAt)
		if leaseErr != nil {
			return false, ErrLaunchStageBindingConflict
		}
		if lease.After(now) {
			return false, nil
		}
		generation = epoch.LeaseGeneration + 1
		dispatchStartedAt = epoch.DispatchStartedAt
	}
	next := a.operation
	next.RedactedProviderPayload = maps.Clone(a.operation.RedactedProviderPayload)
	next.RedactedProviderPayload[providerMutationReplayEpochPayloadKey] = providerMutationReplayEpoch{
		SchemaVersion: 1, ReplayID: providerMutationReplayID(next, binding), ParentFabricOperationID: binding.Parent.FabricOperationID,
		ChildOperationID: next.ID, IdempotencyKey: next.IdempotencyKey, State: "leased", LeaseGeneration: generation,
		LeaseExpiresAt: now.Add(providerMutationReplayLease).Format(time.RFC3339Nano), DispatchStartedAt: dispatchStartedAt,
	}
	if err := store.SaveProviderMutationReplayEpoch(ctx, a.operation, next); err != nil {
		return false, err
	}
	a.operation, a.Replay = next, true
	return true, nil
}

func (a *providerMutationAttempt) markReplayDispatch(ctx context.Context) error {
	if a == nil || a.Fresh {
		return nil
	}
	if !a.Replay {
		return ErrRuntimeOperationNotCurrent
	}
	epoch, ok := decodeProviderMutationReplayEpoch(a.operation)
	if !ok || epoch.State != "leased" {
		return ErrLaunchStageBindingConflict
	}
	store, ok := a.journal.operations.(providerMutationReplayStore)
	if !ok {
		return ErrRuntimeOperationNotCurrent
	}
	next := a.operation
	next.RedactedProviderPayload = maps.Clone(a.operation.RedactedProviderPayload)
	epoch.State = "awaiting_readback"
	if epoch.DispatchStartedAt == "" {
		epoch.DispatchStartedAt = a.journal.now().UTC().Format(time.RFC3339Nano)
	}
	next.RedactedProviderPayload[providerMutationReplayEpochPayloadKey] = epoch
	if err := store.SaveProviderMutationReplayEpoch(ctx, a.operation, next); err != nil {
		return err
	}
	a.operation = next
	return nil
}
