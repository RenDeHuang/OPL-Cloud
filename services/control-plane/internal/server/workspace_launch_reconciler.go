package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const workspaceLaunchReconcileSchemaVersion = 3

const workspaceLaunchIdempotentReplayLease = 30 * time.Second
const workspaceLaunchAuthoritativeReadBudget = 3
const workspaceLaunchLegacyV3AuthoritativeReadBudget = 0

const (
	workspaceLaunchStageAbsent  = "absent"
	workspaceLaunchStagePending = "pending"
	workspaceLaunchStageReady   = "ready"
	workspaceLaunchStageUnknown = "unknown"
)

var workspaceLaunchReconcileStages = []string{
	"key",
	"debit",
	"ensure_compute_allocation",
	"storage",
	"attachment",
	"secret",
	"runtime",
	"activation",
	"receipt",
	"succeeded",
}

var workspaceLaunchReconcileForbiddenFields = []string{
	"phase",
	"currentDecision",
	"recoveryPlan",
	"recoveryExecution",
	"recoveryHistory",
	"readbackRecoveryApproval",
	"readbackRecoveryProof",
	"computeClaimApproval",
	"computeClaimProof",
	"computeClaimTerminalEvidence",
	"recoveryAcceptanceApprovalDigest",
	"operatorGrants",
	"currentGrantId",
	"proof",
	"artifact",
	"operatorGrant",
}

type workspaceLaunchCanonicalFactKind string

const (
	workspaceLaunchCanonicalFactString  workspaceLaunchCanonicalFactKind = "string"
	workspaceLaunchCanonicalFactInteger workspaceLaunchCanonicalFactKind = "integer"
	workspaceLaunchCanonicalFactBool    workspaceLaunchCanonicalFactKind = "bool"
	workspaceLaunchCanonicalFactObject  workspaceLaunchCanonicalFactKind = "object"
)

type workspaceLaunchCanonicalFactSpec struct {
	Kind     workspaceLaunchCanonicalFactKind
	Required bool
	Positive bool
	Exact    any
}

var workspaceLaunchStageCanonicalFacts = map[string]map[string]workspaceLaunchCanonicalFactSpec{
	"key": {
		"workspaceApiKeyId":       {Kind: workspaceLaunchCanonicalFactInteger, Required: true, Positive: true},
		"workspaceKeyGroupId":     {Kind: workspaceLaunchCanonicalFactInteger, Required: true, Positive: true},
		"workspaceKeyStatus":      {Kind: workspaceLaunchCanonicalFactString, Required: true},
		"workspaceKeyFingerprint": {Kind: workspaceLaunchCanonicalFactString, Required: true},
	},
	"debit": {
		"chargeAttempted":            {Kind: workspaceLaunchCanonicalFactBool, Required: true, Exact: true},
		"chargeConfirmation":         {Kind: workspaceLaunchCanonicalFactObject, Required: true},
		"preChargeBalanceUsdMicros":  {Kind: workspaceLaunchCanonicalFactInteger, Required: true},
		"postChargeBalanceUsdMicros": {Kind: workspaceLaunchCanonicalFactInteger, Required: true},
		"postChargeBalanceKnown":     {Kind: workspaceLaunchCanonicalFactBool, Required: true, Exact: true},
		"billingPeriodState":         {Kind: workspaceLaunchCanonicalFactString},
		"periodStart":                {Kind: workspaceLaunchCanonicalFactString},
		"paidThrough":                {Kind: workspaceLaunchCanonicalFactString},
		"billingAnchorDay":           {Kind: workspaceLaunchCanonicalFactInteger, Positive: true},
	},
	"ensure_compute_allocation": {
		"computeAllocationId": {Kind: workspaceLaunchCanonicalFactString, Required: true},
		"computeBindingRef":   {Kind: workspaceLaunchCanonicalFactString, Required: true},
	},
	"storage": {
		"storageId":         {Kind: workspaceLaunchCanonicalFactString, Required: true},
		"storageBindingRef": {Kind: workspaceLaunchCanonicalFactString, Required: true},
	},
	"attachment": {
		"attachmentId":         {Kind: workspaceLaunchCanonicalFactString, Required: true},
		"attachmentBindingRef": {Kind: workspaceLaunchCanonicalFactString, Required: true},
	},
	"secret": {
		"gatewaySecretRef":        {Kind: workspaceLaunchCanonicalFactString, Required: true},
		"gatewaySecretVersion":    {Kind: workspaceLaunchCanonicalFactString, Required: true},
		"secretBindingRef":        {Kind: workspaceLaunchCanonicalFactString, Required: true},
		"workspaceKeyStatus":      {Kind: workspaceLaunchCanonicalFactString, Required: true},
		"workspaceKeyFingerprint": {Kind: workspaceLaunchCanonicalFactString},
		"credentialStatus":        {Kind: workspaceLaunchCanonicalFactString},
		"credentialVersion":       {Kind: workspaceLaunchCanonicalFactString},
		"credentialSecretRef":     {Kind: workspaceLaunchCanonicalFactString},
	},
	"runtime": {
		"runtimeId":          {Kind: workspaceLaunchCanonicalFactString, Required: true},
		"runtimeReady":       {Kind: workspaceLaunchCanonicalFactBool, Required: true, Exact: true},
		"runtimeServiceName": {Kind: workspaceLaunchCanonicalFactString, Required: true},
		"runtimeBindingRef":  {Kind: workspaceLaunchCanonicalFactString, Required: true},
		"runtimeUsername":    {Kind: workspaceLaunchCanonicalFactString},
		"url":                {Kind: workspaceLaunchCanonicalFactString, Required: true},
	},
	"activation": {
		"activationOperationId": {Kind: workspaceLaunchCanonicalFactString, Required: true},
		"workspaceActivatedAt":  {Kind: workspaceLaunchCanonicalFactString, Required: true},
	},
	"receipt": {
		"receiptId":          {Kind: workspaceLaunchCanonicalFactString, Required: true},
		"receiptOperationId": {Kind: workspaceLaunchCanonicalFactString},
	},
}

var errWorkspaceLaunchGrantConflict = errors.New("workspace_launch_resume_authorization_conflict")

type workspaceLaunchStageAttempt struct {
	Attempted           int    `json:"attempted"`
	Confirmed           int    `json:"confirmed"`
	Unknown             int    `json:"unknown"`
	Max                 int    `json:"max"`
	Status              string `json:"status,omitempty"`
	IdempotencyKey      string `json:"idempotencyKey,omitempty"`
	PendingReadbacks    int    `json:"pendingReadbacks,omitempty"`
	MaxPendingReadbacks int    `json:"maxPendingReadbacks,omitempty"`
}

type workspaceLaunchStageObservation struct {
	State string         `json:"state"`
	Facts map[string]any `json:"facts,omitempty"`
}

type workspaceLaunchResumeAuthorization struct {
	AuthorizationID           string                                           `json:"authorizationId"`
	LaunchVersion             int                                              `json:"launchVersion"`
	AuthorizedStage           string                                           `json:"authorizedStage"`
	AuthorizedBy              string                                           `json:"authorizedBy"`
	AuthorizedAt              string                                           `json:"authorizedAt"`
	Reason                    string                                           `json:"reason"`
	MutationBudget            int                                              `json:"mutationBudget"`
	IdempotentReplayBudget    int                                              `json:"idempotentReplayBudget,omitempty"`
	AuthoritativeReadBudget   int                                              `json:"authoritativeReadBudget,omitempty"`
	ReadbacksAtAuthorization  int                                              `json:"readbacksAtAuthorization,omitempty"`
	AcceptanceBResumeExisting *workspaceLaunchAcceptanceBResumeExistingBinding `json:"acceptanceBResumeExisting,omitempty"`
}

type workspaceLaunchConsumedResumeAuthorization struct {
	Authorization workspaceLaunchResumeAuthorization `json:"authorization"`
	ConsumedAt    string                             `json:"consumedAt"`
}

type workspaceLaunchIdempotentReplayClaim struct {
	AuthorizationID string `json:"authorizationId"`
	Stage           string `json:"stage"`
	IdempotencyKey  string `json:"idempotencyKey"`
	Status          string `json:"status"`
	LeaseExpiresAt  string `json:"leaseExpiresAt,omitempty"`
	CompletedAt     string `json:"completedAt,omitempty"`
}

type workspaceLaunchReconcileOperation struct {
	ID                            string                                          `json:"-"`
	Status                        string                                          `json:"-"`
	CreatedAt                     string                                          `json:"-"`
	PersistedResult               string                                          `json:"-"`
	SchemaVersion                 int                                             `json:"schemaVersion"`
	Version                       int                                             `json:"version"`
	Stage                         string                                          `json:"stage"`
	Attempts                      map[string]workspaceLaunchStageAttempt          `json:"attempts"`
	Observations                  map[string]workspaceLaunchStageObservation      `json:"observations,omitempty"`
	ConsumedResumeAuthorizations  []workspaceLaunchConsumedResumeAuthorization    `json:"consumedResumeAuthorizations,omitempty"`
	ResumeAuthorization           *workspaceLaunchResumeAuthorization             `json:"resumeAuthorization,omitempty"`
	ResumeAuthorizationConsumedAt string                                          `json:"resumeAuthorizationConsumedAt,omitempty"`
	IdempotentReplayClaims        map[string]workspaceLaunchIdempotentReplayClaim `json:"idempotentReplayClaims,omitempty"`
	raw                           map[string]json.RawMessage
}

type workspaceLaunchReconcileCreate struct {
	OperationID             string
	RequestHash             string
	AccountID               string
	OwnerUserID             string
	Sub2APIUserID           int64
	WorkspaceKeyGroupID     int64
	WorkspaceID             string
	Name                    string
	PackageID               string
	StorageGB               int
	AutoRenew               bool
	PriceVersion            string
	TotalChargeUSDMicros    int64
	ProviderProfileRef      string
	PreflightBindingRef     string
	WorkspaceImageDigest    string
	PreChargeBalanceMicros  int64
	AcceptanceBCapacitySlot bool
	CreatedAt               time.Time
}

type workspaceLaunchReconcileClaim struct {
	AccountID               string
	DesiredOperation        map[string]any
	AcceptanceBCapacitySlot bool
}

type workspaceLaunchReconcileCAS struct {
	OperationID             string
	ExpectedOperationResult string
	DesiredOperation        map[string]any
}

type workspaceLaunchReconcileStore interface {
	GetRuntimeOperation(context.Context, string) (map[string]any, bool, error)
	ClaimWorkspaceLaunchReconcile(context.Context, workspaceLaunchReconcileClaim) error
	PersistWorkspaceLaunchReconcile(context.Context, workspaceLaunchReconcileCAS) error
}

type workspaceLaunchStageAdapter interface {
	ReadStage(context.Context, workspaceLaunchReconcileOperation) (workspaceLaunchStageObservation, error)
	CanMutateStage(workspaceLaunchReconcileOperation) bool
	CanReplayStage(workspaceLaunchReconcileOperation) bool
	MutateStage(context.Context, workspaceLaunchReconcileOperation, string) error
}

type WorkspaceLaunchReconciler struct {
	store   workspaceLaunchReconcileStore
	adapter workspaceLaunchStageAdapter
	now     func() time.Time
}

func NewWorkspaceLaunchReconciler(store workspaceLaunchReconcileStore, adapter workspaceLaunchStageAdapter) *WorkspaceLaunchReconciler {
	return &WorkspaceLaunchReconciler{store: store, adapter: adapter, now: time.Now}
}

func (r *WorkspaceLaunchReconciler) clockNow() time.Time {
	if r != nil && r.now != nil {
		return r.now().UTC()
	}
	return time.Now().UTC()
}

func (r *WorkspaceLaunchReconciler) Create(ctx context.Context, command workspaceLaunchReconcileCreate) (workspaceLaunchReconcileOperation, error) {
	operation, err := newWorkspaceLaunchReconcileOperation(command)
	if err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	row, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	if err := r.store.ClaimWorkspaceLaunchReconcile(ctx, workspaceLaunchReconcileClaim{
		AccountID: command.AccountID, DesiredOperation: row, AcceptanceBCapacitySlot: command.AcceptanceBCapacitySlot,
	}); err != nil {
		if !errors.Is(err, errWorkspaceLaunchCASConflict) {
			return workspaceLaunchReconcileOperation{}, err
		}
		current, found, readErr := r.store.GetRuntimeOperation(ctx, command.OperationID)
		if readErr != nil || !found {
			return workspaceLaunchReconcileOperation{}, err
		}
		existing, decodeErr := decodeWorkspaceLaunchReconcileOperation(current)
		if decodeErr != nil || !workspaceLaunchReconcileSubmissionMatches(existing, command) {
			return workspaceLaunchReconcileOperation{}, err
		}
		if existing.Status == "pending" {
			return r.Reconcile(ctx, command.OperationID)
		}
		return existing, nil
	}
	return r.Reconcile(ctx, command.OperationID)
}

func (r *WorkspaceLaunchReconciler) Reconcile(ctx context.Context, operationID string) (workspaceLaunchReconcileOperation, error) {
	if r == nil || r.store == nil || r.adapter == nil {
		return workspaceLaunchReconcileOperation{}, errors.New("WorkspaceLaunchReconciler dependencies are required")
	}
	row, found, err := r.store.GetRuntimeOperation(ctx, operationID)
	if err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	if !found {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchCASConflict
	}
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	if operation.Status == "manual_review" || operation.Status == "succeeded" {
		return operation, nil
	}
	attempt := operation.Attempts[operation.Stage]
	if attempt.Status == "reserved" && operation.Observations[operation.Stage].State == workspaceLaunchStagePending &&
		!workspaceLaunchContinuationReadAuthorized(operation, attempt) {
		operation.Status = "manual_review"
		return r.persist(ctx, operation)
	}

	observation, readErr := r.adapter.ReadStage(ctx, operation)
	if readErr != nil {
		observation = workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}
	}
	switch observation.State {
	case workspaceLaunchStageReady:
		observation, err = reduceWorkspaceLaunchStageObservation(&operation, observation)
		if err != nil {
			return workspaceLaunchReconcileOperation{}, err
		}
		attempt := operation.Attempts[operation.Stage]
		if attempt.Attempted > 0 {
			attempt.Confirmed, attempt.Unknown, attempt.Status = 1, 0, "confirmed"
			operation.Attempts[operation.Stage] = attempt
		}
		operation.Observations[operation.Stage] = observation
		operation.completeIdempotentReplay("succeeded", r.clockNow())
		operation.consumeResumeAuthorization(r.clockNow())
		operation.advance()
		return r.persist(ctx, operation)
	case workspaceLaunchStageUnknown:
		attempt := operation.Attempts[operation.Stage]
		if readErr != nil && attempt.Attempted == 0 && attempt.Status == "" {
			return operation, nil
		}
		if attempt.Status == "reserved" {
			attempt.Unknown = 1
			attempt.Status = "unknown"
			operation.Attempts[operation.Stage] = attempt
		}
		operation.Observations[operation.Stage] = workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}
		operation.completeIdempotentReplay("failed", r.clockNow())
		operation.consumeResumeAuthorization(r.clockNow())
		operation.Status = "manual_review"
		return r.persist(ctx, operation)
	case workspaceLaunchStagePending:
		attempt := operation.Attempts[operation.Stage]
		if attempt.Attempted != 1 || attempt.Status != "reserved" || attempt.Confirmed != 0 || attempt.Unknown != 0 ||
			!workspaceLaunchContinuationReadAuthorized(operation, attempt) {
			operation.Observations[operation.Stage] = workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}
			operation.completeIdempotentReplay("failed", r.clockNow())
			operation.consumeResumeAuthorization(r.clockNow())
			operation.Status = "manual_review"
			return r.persist(ctx, operation)
		}
		attempt.PendingReadbacks++
		operation.Attempts[operation.Stage] = attempt
		if attempt.PendingReadbacks >= attempt.MaxPendingReadbacks {
			attempt.Unknown, attempt.Status = 1, "unknown"
			operation.Attempts[operation.Stage] = attempt
			operation.Observations[operation.Stage] = workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}
			operation.completeIdempotentReplay("failed", r.clockNow())
			operation.consumeResumeAuthorization(r.clockNow())
			operation.Status = "manual_review"
			return r.persist(ctx, operation)
		}
		operation.Observations[operation.Stage] = workspaceLaunchStageObservation{State: workspaceLaunchStagePending}
		return r.persist(ctx, operation)
	case workspaceLaunchStageAbsent:
		if claim, exists := operation.IdempotentReplayClaims[operation.Stage]; exists && claim.Status == "waiting" {
			return r.parkClaimedReplay(ctx, operation, workspaceLaunchStageUnknown)
		}
	default:
		return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
	}

	attempt = operation.Attempts[operation.Stage]
	if attempt.Status == "reserved" || attempt.Attempted >= attempt.Max {
		if workspaceLaunchIdempotentReplayAuthorized(operation, attempt) {
			return r.replayReservedStage(ctx, operation, attempt)
		}
		operation.Observations[operation.Stage] = workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}
		operation.consumeResumeAuthorization(r.clockNow())
		operation.Status = "manual_review"
		return r.persist(ctx, operation)
	}
	if !r.adapter.CanMutateStage(operation) {
		return operation, nil
	}
	attempt.Attempted++
	attempt.Status = "reserved"
	attempt.IdempotencyKey = workspaceLaunchStageIdempotencyKey(operation, attempt.Attempted)
	operation.Attempts[operation.Stage] = attempt
	reserved, err := r.persist(ctx, operation)
	if err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	return r.mutateReservedStage(ctx, reserved)
}

func (r *WorkspaceLaunchReconciler) replayReservedStage(ctx context.Context, operation workspaceLaunchReconcileOperation, attempt workspaceLaunchStageAttempt) (workspaceLaunchReconcileOperation, error) {
	if !r.adapter.CanReplayStage(operation) {
		return operation, nil
	}
	claimed, dispatch, err := r.claimIdempotentReplay(ctx, operation, attempt)
	if err != nil || !dispatch {
		return claimed, err
	}
	observation, readErr := r.adapter.ReadStage(ctx, claimed)
	if readErr != nil || observation.State == workspaceLaunchStageUnknown {
		return r.parkClaimedReplay(ctx, claimed, workspaceLaunchStageUnknown)
	}
	switch observation.State {
	case workspaceLaunchStageReady:
		return r.convergeClaimedReplay(ctx, claimed, observation)
	case workspaceLaunchStagePending:
		return r.waitClaimedReplay(ctx, claimed)
	case workspaceLaunchStageAbsent:
		return r.mutateReservedStage(ctx, claimed)
	default:
		return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
	}
}

func (r *WorkspaceLaunchReconciler) claimIdempotentReplay(ctx context.Context, operation workspaceLaunchReconcileOperation, attempt workspaceLaunchStageAttempt) (workspaceLaunchReconcileOperation, bool, error) {
	now := r.clockNow()
	claim, exists := operation.IdempotentReplayClaims[operation.Stage]
	if exists {
		if claim.AuthorizationID != operation.ResumeAuthorization.AuthorizationID || claim.Stage != operation.Stage || claim.IdempotencyKey != attempt.IdempotencyKey || claim.Status != "claimed" {
			return workspaceLaunchReconcileOperation{}, false, errWorkspaceLaunchGrantConflict
		}
		lease, err := time.Parse(time.RFC3339Nano, claim.LeaseExpiresAt)
		if err != nil {
			return workspaceLaunchReconcileOperation{}, false, errInvalidWorkspaceLaunchOperation
		}
		if lease.After(now) {
			return operation, false, nil
		}
	} else {
		claim = workspaceLaunchIdempotentReplayClaim{
			AuthorizationID: operation.ResumeAuthorization.AuthorizationID,
			Stage:           operation.Stage, IdempotencyKey: attempt.IdempotencyKey, Status: "claimed",
		}
	}
	claim.LeaseExpiresAt = now.Add(workspaceLaunchIdempotentReplayLease).Format(time.RFC3339Nano)
	operation.IdempotentReplayClaims[operation.Stage] = claim
	claimed, err := r.persist(ctx, operation)
	return claimed, err == nil, err
}

func (r *WorkspaceLaunchReconciler) convergeClaimedReplay(ctx context.Context, operation workspaceLaunchReconcileOperation, observation workspaceLaunchStageObservation) (workspaceLaunchReconcileOperation, error) {
	reduced, err := reduceWorkspaceLaunchStageObservation(&operation, observation)
	if err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	attempt := operation.Attempts[operation.Stage]
	attempt.Confirmed, attempt.Unknown, attempt.Status = 1, 0, "confirmed"
	operation.Attempts[operation.Stage] = attempt
	operation.Observations[operation.Stage] = reduced
	operation.completeIdempotentReplay("succeeded", r.clockNow())
	operation.consumeResumeAuthorization(r.clockNow())
	operation.advance()
	return r.persist(ctx, operation)
}

func (r *WorkspaceLaunchReconciler) waitClaimedReplay(ctx context.Context, operation workspaceLaunchReconcileOperation) (workspaceLaunchReconcileOperation, error) {
	attempt := operation.Attempts[operation.Stage]
	attempt.PendingReadbacks++
	operation.Attempts[operation.Stage] = attempt
	operation.Observations[operation.Stage] = workspaceLaunchStageObservation{State: workspaceLaunchStagePending}
	operation.completeIdempotentReplay("waiting", r.clockNow())
	if attempt.PendingReadbacks >= attempt.MaxPendingReadbacks {
		attempt.Unknown, attempt.Status = 1, "unknown"
		operation.Attempts[operation.Stage] = attempt
		operation.Observations[operation.Stage] = workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}
		operation.consumeResumeAuthorization(r.clockNow())
		operation.Status = "manual_review"
	} else {
		operation.Status = "pending"
	}
	return r.persist(ctx, operation)
}

func (r *WorkspaceLaunchReconciler) parkClaimedReplay(ctx context.Context, operation workspaceLaunchReconcileOperation, state string) (workspaceLaunchReconcileOperation, error) {
	attempt := operation.Attempts[operation.Stage]
	attempt.Unknown, attempt.Status = 1, "unknown"
	operation.Attempts[operation.Stage] = attempt
	operation.Observations[operation.Stage] = workspaceLaunchStageObservation{State: state}
	operation.completeIdempotentReplay("failed", r.clockNow())
	operation.consumeResumeAuthorization(r.clockNow())
	operation.Status = "manual_review"
	return r.persist(ctx, operation)
}

func (r *WorkspaceLaunchReconciler) mutateReservedStage(ctx context.Context, reserved workspaceLaunchReconcileOperation) (workspaceLaunchReconcileOperation, error) {
	attempt := reserved.Attempts[reserved.Stage]
	mutationErr := r.adapter.MutateStage(ctx, reserved, attempt.IdempotencyKey)
	postRead, postReadErr := r.adapter.ReadStage(ctx, reserved)
	if postReadErr != nil {
		postRead = workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}
	}
	attempt = reserved.Attempts[reserved.Stage]
	if postRead.State == workspaceLaunchStageReady {
		reduced, err := reduceWorkspaceLaunchStageObservation(&reserved, postRead)
		if err != nil {
			return workspaceLaunchReconcileOperation{}, err
		}
		reserved.Observations[reserved.Stage] = reduced
		attempt.Confirmed, attempt.Unknown, attempt.Status = 1, 0, "confirmed"
		reserved.Attempts[reserved.Stage] = attempt
		reserved.completeIdempotentReplay("succeeded", r.clockNow())
		reserved.consumeResumeAuthorization(r.clockNow())
		reserved.advance()
		return r.persist(ctx, reserved)
	}
	if postRead.State == workspaceLaunchStagePending {
		if _, replay := reserved.IdempotentReplayClaims[reserved.Stage]; replay {
			return r.waitClaimedReplay(ctx, reserved)
		}
		reserved.Observations[reserved.Stage] = workspaceLaunchStageObservation{State: workspaceLaunchStagePending}
		reserved.Status = "manual_review"
		return r.persist(ctx, reserved)
	}
	reserved.Observations[reserved.Stage] = workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}
	attempt.Unknown = 1
	attempt.Status = "unknown"
	reserved.Attempts[reserved.Stage] = attempt
	reserved.completeIdempotentReplay("failed", r.clockNow())
	reserved.consumeResumeAuthorization(r.clockNow())
	reserved.Status = "manual_review"
	parked, persistErr := r.persist(ctx, reserved)
	if persistErr != nil {
		return workspaceLaunchReconcileOperation{}, persistErr
	}
	if mutationErr != nil {
		return parked, mutationErr
	}
	return parked, nil
}

func workspaceLaunchIdempotentReplayAuthorized(operation workspaceLaunchReconcileOperation, attempt workspaceLaunchStageAttempt) bool {
	authorization := operation.ResumeAuthorization
	return authorization != nil && operation.ResumeAuthorizationConsumedAt == "" && authorization.AuthorizedStage == operation.Stage &&
		authorization.MutationBudget == 0 && authorization.IdempotentReplayBudget == 1 &&
		authorization.ReadbacksAtAuthorization+authorization.AuthoritativeReadBudget == attempt.MaxPendingReadbacks &&
		attempt.Max == 1 && attempt.Attempted == attempt.Max && attempt.Confirmed == 0 && attempt.Unknown == 0 && attempt.Status == "reserved" &&
		attempt.IdempotencyKey == workspaceLaunchStageIdempotencyKey(operation, 1)
}

func workspaceLaunchContinuationReadAuthorized(operation workspaceLaunchReconcileOperation, attempt workspaceLaunchStageAttempt) bool {
	authorization := operation.ResumeAuthorization
	return authorization != nil && operation.ResumeAuthorizationConsumedAt == "" && authorization.AuthorizedStage == operation.Stage &&
		authorization.MutationBudget == 0 && authorization.AuthoritativeReadBudget > 0 &&
		authorization.ReadbacksAtAuthorization >= 0 && authorization.ReadbacksAtAuthorization <= attempt.PendingReadbacks &&
		authorization.ReadbacksAtAuthorization+authorization.AuthoritativeReadBudget == attempt.MaxPendingReadbacks &&
		attempt.PendingReadbacks < attempt.MaxPendingReadbacks
}

func workspaceLaunchStageIdempotencyKey(operation workspaceLaunchReconcileOperation, attempt int) string {
	switch operation.Stage {
	case "key":
		return operation.ID + ":workspace-key"
	case "debit":
		return operation.stringFact("sub2apiRedeemCode")
	case "ensure_compute_allocation":
		return operation.ID + ":ensure-compute-allocation"
	case "storage", "attachment", "secret", "runtime":
		return operation.ID + ":" + operation.Stage
	case "activation":
		return operation.ID + ":activation"
	case "receipt":
		return operation.ID + ":purchase-receipt"
	}
	return operation.ID + ":" + operation.Stage + ":attempt:" + strconv.Itoa(attempt)
}

func (r *WorkspaceLaunchReconciler) Resume(ctx context.Context, operationID string, authorization workspaceLaunchResumeAuthorization) (workspaceLaunchReconcileOperation, error) {
	if !validWorkspaceLaunchResumeAuthorization(authorization) {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchGrantConflict
	}
	row, found, err := r.store.GetRuntimeOperation(ctx, operationID)
	if err != nil || !found {
		if err == nil {
			err = errWorkspaceLaunchCASConflict
		}
		return workspaceLaunchReconcileOperation{}, err
	}
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	if existing, consumed, found := operation.resumeAuthorizationByID(authorization.AuthorizationID); found {
		if existing != authorization || workspaceLaunchResumeAuthorizationDigest(existing) != workspaceLaunchResumeAuthorizationDigest(authorization) {
			return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchGrantConflict
		}
		if consumed {
			return operation, nil
		}
		return r.Reconcile(ctx, operationID)
	}
	if operation.ResumeAuthorization != nil && operation.ResumeAuthorizationConsumedAt == "" {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchGrantConflict
	}
	attempt, ok := operation.Attempts[operation.Stage]
	if !ok {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchGrantConflict
	}
	remainingBudget := attempt.Max - attempt.Attempted
	if operation.Status != "manual_review" || operation.Stage == "succeeded" ||
		authorization.LaunchVersion != operation.Version || authorization.AuthorizedStage != operation.Stage {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchGrantConflict
	}
	if attempt.Status == "reserved" || attempt.Attempted >= attempt.Max {
		return r.authorizeExhaustedStage(ctx, operation, attempt, authorization)
	}
	if authorization.MutationBudget != remainingBudget || authorization.IdempotentReplayBudget != 0 {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchGrantConflict
	}
	operation.rotateResumeAuthorization(authorization)
	operation.Status = "pending"
	if _, err := r.persist(ctx, operation); err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	return r.Reconcile(ctx, operationID)
}

func workspaceLaunchReservedStageReplayEligible(operation workspaceLaunchReconcileOperation, attempt workspaceLaunchStageAttempt, authorization workspaceLaunchResumeAuthorization) bool {
	return authorization.AuthorizedStage == operation.Stage && authorization.MutationBudget == 0 && authorization.IdempotentReplayBudget == 1 &&
		authorization.AuthoritativeReadBudget > 0 && attempt.MaxPendingReadbacks == attempt.PendingReadbacks &&
		attempt.Max == 1 && attempt.Attempted == attempt.Max && attempt.Confirmed == 0 && attempt.Unknown == 0 && attempt.Status == "reserved" &&
		attempt.IdempotencyKey == workspaceLaunchStageIdempotencyKey(operation, 1) && !operation.idempotentReplayAuthorizationUsed(operation.Stage)
}

func workspaceLaunchReservedStageReadEligible(operation workspaceLaunchReconcileOperation, attempt workspaceLaunchStageAttempt, authorization workspaceLaunchResumeAuthorization) bool {
	return authorization.AuthorizedStage == operation.Stage && authorization.MutationBudget == 0 && authorization.IdempotentReplayBudget == 0 &&
		authorization.AuthoritativeReadBudget > 0 && operation.Observations[operation.Stage].State == workspaceLaunchStagePending &&
		attempt.Max == 1 && attempt.Attempted == attempt.Max && attempt.Confirmed == 0 && attempt.Unknown == 0 && attempt.Status == "reserved" &&
		attempt.IdempotencyKey == workspaceLaunchStageIdempotencyKey(operation, 1)
}

func (r *WorkspaceLaunchReconciler) authorizeExhaustedStage(ctx context.Context, operation workspaceLaunchReconcileOperation, attempt workspaceLaunchStageAttempt, authorization workspaceLaunchResumeAuthorization) (workspaceLaunchReconcileOperation, error) {
	if !workspaceLaunchReservedStageReplayEligible(operation, attempt, authorization) &&
		!workspaceLaunchReservedStageReadEligible(operation, attempt, authorization) {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchGrantConflict
	}
	observation, readErr := r.adapter.ReadStage(ctx, operation)
	if readErr != nil {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchGrantConflict
	}
	switch observation.State {
	case workspaceLaunchStageReady:
		reduced, err := reduceWorkspaceLaunchStageObservation(&operation, observation)
		if err != nil {
			return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchGrantConflict
		}
		operation.rotateResumeAuthorization(authorization)
		attempt.Confirmed, attempt.Unknown, attempt.Status = 1, 0, "confirmed"
		operation.Attempts[operation.Stage] = attempt
		operation.Observations[operation.Stage] = reduced
		operation.consumeResumeAuthorization(r.clockNow())
		operation.advance()
		return r.persist(ctx, operation)
	case workspaceLaunchStageAbsent:
		if authorization.IdempotentReplayBudget != 1 {
			return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchGrantConflict
		}
		authorization.ReadbacksAtAuthorization = attempt.PendingReadbacks
		attempt.MaxPendingReadbacks = attempt.PendingReadbacks + authorization.AuthoritativeReadBudget
		operation.Attempts[operation.Stage] = attempt
		operation.rotateResumeAuthorization(authorization)
		operation.Observations[operation.Stage] = workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}
		operation.Status = "pending"
		if _, err := r.persist(ctx, operation); err != nil {
			return workspaceLaunchReconcileOperation{}, err
		}
		return r.Reconcile(ctx, operation.ID)
	case workspaceLaunchStagePending:
		authorization.ReadbacksAtAuthorization = attempt.PendingReadbacks
		attempt.MaxPendingReadbacks = attempt.PendingReadbacks + authorization.AuthoritativeReadBudget
		operation.Attempts[operation.Stage] = attempt
		operation.rotateResumeAuthorization(authorization)
		operation.Observations[operation.Stage] = workspaceLaunchStageObservation{State: workspaceLaunchStagePending}
		operation.Status = "pending"
		if _, err := r.persist(ctx, operation); err != nil {
			return workspaceLaunchReconcileOperation{}, err
		}
		return r.Reconcile(ctx, operation.ID)
	default:
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchGrantConflict
	}
}

func (r *WorkspaceLaunchReconciler) persist(ctx context.Context, operation workspaceLaunchReconcileOperation) (workspaceLaunchReconcileOperation, error) {
	operation.Version++
	desired, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	if err := r.store.PersistWorkspaceLaunchReconcile(ctx, workspaceLaunchReconcileCAS{
		OperationID: operation.ID, ExpectedOperationResult: operation.PersistedResult, DesiredOperation: desired,
	}); err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	operation.PersistedResult = stringValue(desired["result"])
	return operation, nil
}

func newWorkspaceLaunchReconcileOperation(command workspaceLaunchReconcileCreate) (workspaceLaunchReconcileOperation, error) {
	if command.CreatedAt.IsZero() {
		command.CreatedAt = time.Now().UTC()
	}
	if strings.TrimSpace(command.OperationID) == "" || strings.TrimSpace(command.RequestHash) == "" || strings.TrimSpace(command.AccountID) == "" ||
		strings.TrimSpace(command.OwnerUserID) == "" || strings.TrimSpace(command.WorkspaceID) == "" || strings.TrimSpace(command.Name) == "" ||
		command.Sub2APIUserID <= 0 || command.WorkspaceKeyGroupID <= 0 || strings.TrimSpace(command.PackageID) == "" || command.StorageGB <= 0 ||
		strings.TrimSpace(command.PriceVersion) == "" || command.TotalChargeUSDMicros <= 0 || strings.TrimSpace(command.ProviderProfileRef) == "" ||
		strings.TrimSpace(command.PreflightBindingRef) == "" || strings.TrimSpace(command.WorkspaceImageDigest) == "" {
		return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
	}
	facts := map[string]any{
		"schemaVersion":             workspaceLaunchReconcileSchemaVersion,
		"version":                   1,
		"stage":                     "key",
		"requestHash":               command.RequestHash,
		"accountId":                 command.AccountID,
		"ownerUserId":               command.OwnerUserID,
		"sub2apiUserId":             command.Sub2APIUserID,
		"workspaceKeyGroupId":       command.WorkspaceKeyGroupID,
		"workspaceId":               command.WorkspaceID,
		"name":                      command.Name,
		"packageId":                 command.PackageID,
		"sizeGb":                    command.StorageGB,
		"autoRenew":                 command.AutoRenew,
		"priceVersion":              command.PriceVersion,
		"totalChargeUsdMicros":      command.TotalChargeUSDMicros,
		"providerProfileRef":        command.ProviderProfileRef,
		"preflightBindingRef":       command.PreflightBindingRef,
		"workspaceImageDigest":      command.WorkspaceImageDigest,
		"sub2apiRedeemCode":         monthlyRedeemCode(monthlyEnvironment(), command.OperationID),
		"preChargeBalanceUsdMicros": command.PreChargeBalanceMicros,
		"acceptanceBCapacitySlot":   command.AcceptanceBCapacitySlot,
	}
	attempts := make(map[string]workspaceLaunchStageAttempt, len(workspaceLaunchReconcileStages)-1)
	for _, stage := range workspaceLaunchReconcileStages[:len(workspaceLaunchReconcileStages)-1] {
		attempts[stage] = workspaceLaunchStageAttempt{Max: 1, MaxPendingReadbacks: workspaceLaunchLegacyV3AuthoritativeReadBudget}
	}
	facts["attempts"] = attempts
	facts["observations"] = map[string]workspaceLaunchStageObservation{}
	rawBytes, err := json.Marshal(facts)
	if err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	row := map[string]any{
		"id": command.OperationID, "operationId": command.OperationID, "accountId": command.AccountID,
		"workspaceId": command.WorkspaceID, "resourceId": command.WorkspaceID, "resourceKind": "workspace_launch",
		"action": workspaceLaunchAction, "status": "pending", "result": string(rawBytes),
		"createdAt": command.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	return decodeWorkspaceLaunchReconcileOperation(row)
}

func decodeWorkspaceLaunchReconcileOperation(row map[string]any) (workspaceLaunchReconcileOperation, error) {
	result := stringValue(row["result"])
	var raw map[string]json.RawMessage
	if result == "" || json.Unmarshal([]byte(result), &raw) != nil || raw == nil {
		return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
	}
	operation := workspaceLaunchReconcileOperation{
		ID:     firstNonEmpty(stringValue(row["operationId"]), stringValue(row["id"])),
		Status: stringValue(row["status"]), CreatedAt: stringValue(row["createdAt"]), PersistedResult: result, raw: raw,
		Attempts: map[string]workspaceLaunchStageAttempt{}, Observations: map[string]workspaceLaunchStageObservation{}, IdempotentReplayClaims: map[string]workspaceLaunchIdempotentReplayClaim{},
	}
	if json.Unmarshal(raw["schemaVersion"], &operation.SchemaVersion) != nil || operation.SchemaVersion != workspaceLaunchReconcileSchemaVersion ||
		json.Unmarshal(raw["version"], &operation.Version) != nil || operation.Version <= 0 ||
		json.Unmarshal(raw["stage"], &operation.Stage) != nil || !workspaceLaunchReconcileStageValid(operation.Stage) ||
		json.Unmarshal(raw["attempts"], &operation.Attempts) != nil || len(operation.Attempts) != len(workspaceLaunchReconcileStages)-1 {
		return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
	}
	for stage, attempt := range operation.Attempts {
		if attempt.MaxPendingReadbacks == 0 {
			attempt.MaxPendingReadbacks = workspaceLaunchLegacyV3AuthoritativeReadBudget
			operation.Attempts[stage] = attempt
		}
	}
	if value := raw["observations"]; len(value) > 0 && json.Unmarshal(value, &operation.Observations) != nil {
		return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
	}
	if value := raw["consumedResumeAuthorizations"]; len(value) > 0 && json.Unmarshal(value, &operation.ConsumedResumeAuthorizations) != nil {
		return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
	}
	for stage, observation := range operation.Observations {
		allowed := workspaceLaunchStageCanonicalFacts[stage]
		if allowed == nil || observation.State != workspaceLaunchStageReady && observation.State != workspaceLaunchStageAbsent && observation.State != workspaceLaunchStagePending && observation.State != workspaceLaunchStageUnknown ||
			observation.State != workspaceLaunchStageReady && len(observation.Facts) != 0 {
			return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
		}
		if observation.State == workspaceLaunchStageReady {
			if _, err := validateWorkspaceLaunchStageFacts(stage, observation.Facts, false); err != nil {
				return workspaceLaunchReconcileOperation{}, err
			}
		}
	}
	if value := raw["resumeAuthorization"]; len(value) > 0 && json.Unmarshal(value, &operation.ResumeAuthorization) != nil {
		return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
	}
	if value := raw["resumeAuthorizationConsumedAt"]; len(value) > 0 && json.Unmarshal(value, &operation.ResumeAuthorizationConsumedAt) != nil {
		return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
	}
	if value := raw["idempotentReplayClaims"]; len(value) > 0 && json.Unmarshal(value, &operation.IdempotentReplayClaims) != nil {
		return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
	}
	if operation.ResumeAuthorization != nil && !validWorkspaceLaunchResumeAuthorization(*operation.ResumeAuthorization) {
		return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
	}
	authorizationIDs := make(map[string]struct{}, len(operation.ConsumedResumeAuthorizations)+1)
	for _, consumed := range operation.ConsumedResumeAuthorizations {
		if !validWorkspaceLaunchResumeAuthorization(consumed.Authorization) || !validWorkspaceLaunchResumeAuthorizationConsumedAt(consumed.ConsumedAt) ||
			consumed.Authorization.LaunchVersion >= operation.Version {
			return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
		}
		if _, duplicate := authorizationIDs[consumed.Authorization.AuthorizationID]; duplicate {
			return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
		}
		authorizationIDs[consumed.Authorization.AuthorizationID] = struct{}{}
	}
	if operation.ResumeAuthorizationConsumedAt != "" {
		if !validWorkspaceLaunchResumeAuthorizationConsumedAt(operation.ResumeAuthorizationConsumedAt) || operation.ResumeAuthorization == nil {
			return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
		}
	}
	if operation.ResumeAuthorization != nil && operation.ResumeAuthorization.LaunchVersion >= operation.Version {
		return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
	}
	if operation.ResumeAuthorization != nil {
		if _, duplicate := authorizationIDs[operation.ResumeAuthorization.AuthorizationID]; duplicate {
			return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
		}
	}
	for _, field := range workspaceLaunchReconcileForbiddenFields {
		if _, exists := raw[field]; exists {
			return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
		}
	}
	for stage, claim := range operation.IdempotentReplayClaims {
		authorization, consumed, found := operation.resumeAuthorizationByID(claim.AuthorizationID)
		if stage != claim.Stage || !workspaceLaunchReconcileStageValid(stage) || stage == "succeeded" || strings.TrimSpace(claim.AuthorizationID) == "" ||
			claim.IdempotencyKey != workspaceLaunchStageIdempotencyKey(operationWithStage(operation, stage), 1) ||
			claim.Status != "claimed" && claim.Status != "waiting" && claim.Status != "succeeded" && claim.Status != "failed" ||
			!found || authorization.AuthorizedStage != stage || authorization.MutationBudget != 0 || authorization.IdempotentReplayBudget != 1 ||
			authorization.AuthoritativeReadBudget <= 0 || authorization.AuthoritativeReadBudget > workspaceLaunchAuthoritativeReadBudget {
			return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
		}
		if claim.Status == "claimed" {
			if consumed || operation.ResumeAuthorizationConsumedAt != "" || operation.ResumeAuthorization == nil || operation.ResumeAuthorization.AuthorizationID != claim.AuthorizationID ||
				claim.CompletedAt != "" {
				return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
			}
			if _, err := time.Parse(time.RFC3339Nano, claim.LeaseExpiresAt); err != nil {
				return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
			}
		} else if claim.Status == "waiting" {
			if consumed || operation.ResumeAuthorizationConsumedAt != "" || operation.ResumeAuthorization == nil || operation.ResumeAuthorization.AuthorizationID != claim.AuthorizationID ||
				claim.LeaseExpiresAt != "" || !validWorkspaceLaunchResumeAuthorizationConsumedAt(claim.CompletedAt) {
				return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
			}
		} else if !consumed || claim.LeaseExpiresAt != "" || !validWorkspaceLaunchResumeAuthorizationConsumedAt(claim.CompletedAt) {
			return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
		}
	}
	if operation.ID == "" || operation.stringFact("requestHash") == "" || operation.stringFact("accountId") == "" || operation.stringFact("ownerUserId") == "" ||
		operation.int64Fact("sub2apiUserId") <= 0 || operation.int64Fact("workspaceKeyGroupId") <= 0 ||
		operation.stringFact("workspaceId") == "" || operation.stringFact("name") == "" || operation.stringFact("packageId") == "" ||
		operation.stringFact("priceVersion") == "" || operation.intFact("sizeGb") <= 0 || operation.int64Fact("totalChargeUsdMicros") <= 0 ||
		operation.stringFact("providerProfileRef") == "" || operation.stringFact("preflightBindingRef") == "" ||
		operation.stringFact("workspaceImageDigest") == "" || operation.stringFact("sub2apiRedeemCode") == "" ||
		stringValue(row["action"]) != "" && stringValue(row["action"]) != workspaceLaunchAction ||
		stringValue(row["accountId"]) != "" && stringValue(row["accountId"]) != operation.stringFact("accountId") ||
		stringValue(row["workspaceId"]) != "" && stringValue(row["workspaceId"]) != operation.stringFact("workspaceId") {
		return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
	}
	for _, stage := range workspaceLaunchReconcileStages[:len(workspaceLaunchReconcileStages)-1] {
		attempt, exists := operation.Attempts[stage]
		if !exists || attempt.Max != 1 || attempt.Attempted < 0 || attempt.Attempted > 1 || attempt.Confirmed < 0 || attempt.Confirmed > attempt.Attempted ||
			attempt.Unknown < 0 || attempt.Unknown > attempt.Attempted || attempt.Confirmed+attempt.Unknown > attempt.Attempted ||
			attempt.MaxPendingReadbacks < workspaceLaunchLegacyV3AuthoritativeReadBudget || attempt.MaxPendingReadbacks > workspaceLaunchAuthoritativeReadBudget ||
			attempt.PendingReadbacks < 0 || attempt.PendingReadbacks > attempt.MaxPendingReadbacks ||
			attempt.Status != "" && attempt.Status != "reserved" && attempt.Status != "confirmed" && attempt.Status != "unknown" {
			return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
		}
	}
	if operation.Stage == "succeeded" {
		if operation.Status != "succeeded" {
			return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
		}
	} else if operation.Status != "pending" && operation.Status != "manual_review" {
		return workspaceLaunchReconcileOperation{}, errInvalidWorkspaceLaunchOperation
	}
	return operation, nil
}

func workspaceLaunchReconcileOperationRow(operation workspaceLaunchReconcileOperation) (map[string]any, error) {
	if operation.raw == nil {
		return nil, errInvalidWorkspaceLaunchOperation
	}
	raw := make(map[string]json.RawMessage, len(operation.raw)+8)
	for key, value := range operation.raw {
		raw[key] = append(json.RawMessage(nil), value...)
	}
	for _, field := range workspaceLaunchReconcileForbiddenFields {
		delete(raw, field)
	}
	for key, value := range map[string]any{
		"schemaVersion": operation.SchemaVersion, "version": operation.Version, "stage": operation.Stage, "attempts": operation.Attempts,
		"observations": operation.Observations, "consumedResumeAuthorizations": operation.ConsumedResumeAuthorizations, "resumeAuthorization": operation.ResumeAuthorization,
		"resumeAuthorizationConsumedAt": operation.ResumeAuthorizationConsumedAt, "idempotentReplayClaims": operation.IdempotentReplayClaims,
	} {
		if key == "consumedResumeAuthorizations" && len(operation.ConsumedResumeAuthorizations) == 0 ||
			key == "resumeAuthorization" && operation.ResumeAuthorization == nil || key == "resumeAuthorizationConsumedAt" && operation.ResumeAuthorizationConsumedAt == "" ||
			key == "idempotentReplayClaims" && len(operation.IdempotentReplayClaims) == 0 {
			delete(raw, key)
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		raw[key] = encoded
	}
	result, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id": operation.ID, "operationId": operation.ID,
		"accountId": operation.stringFact("accountId"), "workspaceId": operation.stringFact("workspaceId"),
		"resourceId": operation.stringFact("workspaceId"), "resourceKind": "workspace_launch",
		"action": workspaceLaunchAction, "status": operation.Status, "result": string(result),
		"computeAllocationId": operation.stringFact("computeAllocationId"), "storageId": operation.stringFact("storageId"),
		"createdAt": operation.CreatedAt,
	}, nil
}

func (operation *workspaceLaunchReconcileOperation) advance() {
	index := -1
	for i, stage := range workspaceLaunchReconcileStages {
		if stage == operation.Stage {
			index = i
			break
		}
	}
	if index < 0 || index == len(workspaceLaunchReconcileStages)-1 {
		operation.Stage, operation.Status = "succeeded", "succeeded"
		return
	}
	operation.Stage = workspaceLaunchReconcileStages[index+1]
	if operation.Stage == "succeeded" {
		operation.Status = "succeeded"
	} else {
		operation.Status = "pending"
	}
}

func (operation *workspaceLaunchReconcileOperation) consumeResumeAuthorization(now time.Time) {
	if operation.ResumeAuthorization != nil && operation.ResumeAuthorizationConsumedAt == "" {
		operation.ResumeAuthorizationConsumedAt = now.UTC().Format(time.RFC3339Nano)
	}
}

func (operation *workspaceLaunchReconcileOperation) rotateResumeAuthorization(authorization workspaceLaunchResumeAuthorization) {
	if operation.ResumeAuthorization != nil {
		operation.ConsumedResumeAuthorizations = append(operation.ConsumedResumeAuthorizations, workspaceLaunchConsumedResumeAuthorization{
			Authorization: *operation.ResumeAuthorization,
			ConsumedAt:    operation.ResumeAuthorizationConsumedAt,
		})
	}
	operation.ResumeAuthorization = &authorization
	operation.ResumeAuthorizationConsumedAt = ""
}

func (operation workspaceLaunchReconcileOperation) idempotentReplayAuthorizationUsed(stage string) bool {
	if _, exists := operation.IdempotentReplayClaims[stage]; exists {
		return true
	}
	if operation.ResumeAuthorization != nil && operation.ResumeAuthorization.AuthorizedStage == stage && operation.ResumeAuthorization.IdempotentReplayBudget == 1 {
		return true
	}
	for _, consumed := range operation.ConsumedResumeAuthorizations {
		if consumed.Authorization.AuthorizedStage == stage && consumed.Authorization.IdempotentReplayBudget == 1 {
			return true
		}
	}
	return false
}

func (operation *workspaceLaunchReconcileOperation) completeIdempotentReplay(status string, now time.Time) {
	claim, exists := operation.IdempotentReplayClaims[operation.Stage]
	if !exists || claim.Status != "claimed" && claim.Status != "waiting" {
		return
	}
	claim.Status, claim.LeaseExpiresAt, claim.CompletedAt = status, "", now.UTC().Format(time.RFC3339Nano)
	operation.IdempotentReplayClaims[operation.Stage] = claim
}

func operationWithStage(operation workspaceLaunchReconcileOperation, stage string) workspaceLaunchReconcileOperation {
	operation.Stage = stage
	return operation
}

func (operation workspaceLaunchReconcileOperation) resumeAuthorizationByID(authorizationID string) (workspaceLaunchResumeAuthorization, bool, bool) {
	if operation.ResumeAuthorization != nil && operation.ResumeAuthorization.AuthorizationID == authorizationID {
		return *operation.ResumeAuthorization, operation.ResumeAuthorizationConsumedAt != "", true
	}
	for _, consumed := range operation.ConsumedResumeAuthorizations {
		if consumed.Authorization.AuthorizationID == authorizationID {
			return consumed.Authorization, true, true
		}
	}
	return workspaceLaunchResumeAuthorization{}, false, false
}

func (operation workspaceLaunchReconcileOperation) stringFact(field string) string {
	var value string
	_ = json.Unmarshal(operation.raw[field], &value)
	return value
}

func (operation workspaceLaunchReconcileOperation) intFact(field string) int {
	var value int
	_ = json.Unmarshal(operation.raw[field], &value)
	return value
}

func (operation workspaceLaunchReconcileOperation) int64Fact(field string) int64 {
	var value int64
	_ = json.Unmarshal(operation.raw[field], &value)
	return value
}

func (operation workspaceLaunchReconcileOperation) boolFact(field string) bool {
	var value bool
	_ = json.Unmarshal(operation.raw[field], &value)
	return value
}

func workspaceLaunchReconcileStageValid(stage string) bool {
	for _, candidate := range workspaceLaunchReconcileStages {
		if stage == candidate {
			return true
		}
	}
	return false
}

func validWorkspaceLaunchResumeAuthorization(authorization workspaceLaunchResumeAuthorization) bool {
	if strings.TrimSpace(authorization.AuthorizationID) == "" || authorization.LaunchVersion <= 0 || !workspaceLaunchReconcileStageValid(authorization.AuthorizedStage) || authorization.AuthorizedStage == "succeeded" ||
		strings.TrimSpace(authorization.AuthorizedBy) == "" || strings.TrimSpace(authorization.Reason) == "" || authorization.MutationBudget < 0 || authorization.MutationBudget > 1 ||
		authorization.IdempotentReplayBudget < 0 || authorization.IdempotentReplayBudget > 1 || authorization.MutationBudget+authorization.IdempotentReplayBudget > 1 ||
		authorization.AuthoritativeReadBudget < 0 || authorization.AuthoritativeReadBudget > workspaceLaunchAuthoritativeReadBudget || authorization.ReadbacksAtAuthorization < 0 ||
		authorization.MutationBudget > 0 && (authorization.IdempotentReplayBudget != 0 || authorization.AuthoritativeReadBudget != 0) ||
		authorization.IdempotentReplayBudget > 0 && authorization.AuthoritativeReadBudget == 0 ||
		authorization.AuthorizationID != strings.TrimSpace(authorization.AuthorizationID) || authorization.AuthorizedBy != strings.TrimSpace(authorization.AuthorizedBy) || authorization.Reason != strings.TrimSpace(authorization.Reason) {
		return false
	}
	authorizedAt, err := time.Parse(time.RFC3339, authorization.AuthorizedAt)
	return err == nil && !authorizedAt.IsZero() && validWorkspaceLaunchAcceptanceBResumeExistingBinding(authorization.AcceptanceBResumeExisting)
}

func validWorkspaceLaunchAcceptanceBResumeExistingBinding(binding *workspaceLaunchAcceptanceBResumeExistingBinding) bool {
	if binding == nil {
		return true
	}
	return binding.SchemaVersion == 1 && productionAcceptanceBApprovalIDPattern.MatchString(binding.ApprovalID) &&
		workspaceImageDigestPattern.MatchString(binding.ApprovalSHA256) && productionAcceptanceBReleaseSHAPattern.MatchString(binding.CanonicalCloudSHA) &&
		productionAcceptanceBReleaseSHAPattern.MatchString(binding.CanonicalCloudTree) && workspaceImageDigestPattern.MatchString(binding.DeployedCloudImageDigest) &&
		(binding.AuthoritativeState == workspaceLaunchStageReady || binding.AuthoritativeState == workspaceLaunchStageAbsent || binding.AuthoritativeState == workspaceLaunchStagePending) &&
		validWorkspaceLaunchAcceptanceBIdentityDigests(binding.IdentityDigests)
}

func validWorkspaceLaunchResumeAuthorizationConsumedAt(value string) bool {
	consumedAt, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && !consumedAt.IsZero()
}

func workspaceLaunchReconcileSubmissionMatches(operation workspaceLaunchReconcileOperation, command workspaceLaunchReconcileCreate) bool {
	return operation.ID == command.OperationID && operation.stringFact("requestHash") == command.RequestHash &&
		operation.stringFact("accountId") == command.AccountID && operation.stringFact("ownerUserId") == command.OwnerUserID &&
		operation.int64Fact("sub2apiUserId") == command.Sub2APIUserID && operation.int64Fact("workspaceKeyGroupId") == command.WorkspaceKeyGroupID &&
		operation.stringFact("workspaceId") == command.WorkspaceID
}

func reduceWorkspaceLaunchStageObservation(operation *workspaceLaunchReconcileOperation, observation workspaceLaunchStageObservation) (workspaceLaunchStageObservation, error) {
	if operation == nil || observation.State != workspaceLaunchStageReady {
		return workspaceLaunchStageObservation{}, errInvalidWorkspaceLaunchOperation
	}
	encodedFacts, err := validateWorkspaceLaunchStageFacts(operation.Stage, observation.Facts, true)
	if err != nil {
		return workspaceLaunchStageObservation{}, err
	}
	reduced := workspaceLaunchStageObservation{State: workspaceLaunchStageReady, Facts: map[string]any{}}
	for field, encoded := range encodedFacts {
		operation.raw[field] = encoded
		reduced.Facts[field] = observation.Facts[field]
	}
	return reduced, nil
}

func validateWorkspaceLaunchStageFacts(stage string, facts map[string]any, ignoreUnknown bool) (map[string]json.RawMessage, error) {
	specs := workspaceLaunchStageCanonicalFacts[stage]
	if specs == nil {
		return nil, errInvalidWorkspaceLaunchOperation
	}
	encodedFacts := make(map[string]json.RawMessage, len(facts))
	for field, value := range facts {
		spec, ok := specs[field]
		if !ok {
			if ignoreUnknown {
				continue
			}
			return nil, errInvalidWorkspaceLaunchOperation
		}
		encoded, err := json.Marshal(value)
		if err != nil || !validWorkspaceLaunchCanonicalFact(encoded, spec) {
			return nil, errInvalidWorkspaceLaunchOperation
		}
		encodedFacts[field] = encoded
	}
	for field, spec := range specs {
		if spec.Required {
			if _, ok := encodedFacts[field]; !ok {
				return nil, errInvalidWorkspaceLaunchOperation
			}
		}
	}
	return encodedFacts, nil
}

func validWorkspaceLaunchCanonicalFact(encoded json.RawMessage, spec workspaceLaunchCanonicalFactSpec) bool {
	switch spec.Kind {
	case workspaceLaunchCanonicalFactString:
		var value string
		if json.Unmarshal(encoded, &value) != nil || strings.TrimSpace(value) == "" {
			return false
		}
		return spec.Exact == nil || value == spec.Exact
	case workspaceLaunchCanonicalFactInteger:
		value, err := strconv.ParseInt(string(encoded), 10, 64)
		if err != nil || spec.Positive && value <= 0 {
			return false
		}
		return spec.Exact == nil || value == spec.Exact
	case workspaceLaunchCanonicalFactBool:
		var value bool
		if json.Unmarshal(encoded, &value) != nil || string(encoded) != "true" && string(encoded) != "false" {
			return false
		}
		return spec.Exact == nil || value == spec.Exact
	case workspaceLaunchCanonicalFactObject:
		var value map[string]json.RawMessage
		return json.Unmarshal(encoded, &value) == nil && len(value) > 0
	default:
		return false
	}
}

func workspaceLaunchReconcileIdentityMatches(current, desired map[string]any) bool {
	next, nextErr := decodeWorkspaceLaunchReconcileOperation(desired)
	if nextErr != nil {
		return false
	}
	if existing, existingErr := decodeWorkspaceLaunchReconcileOperation(current); existingErr == nil {
		return existing.ID == next.ID && existing.stringFact("accountId") == next.stringFact("accountId") &&
			existing.stringFact("workspaceId") == next.stringFact("workspaceId") && existing.stringFact("ownerUserId") == next.stringFact("ownerUserId") &&
			existing.stringFact("requestHash") == next.stringFact("requestHash") && next.Version == existing.Version+1
	}
	return false
}

func workspaceLaunchReconcileAcceptanceSlot(row map[string]any) bool {
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	return err == nil && operation.boolFact("acceptanceBCapacitySlot")
}

func workspaceLaunchReconcileResultSummary(operation workspaceLaunchReconcileOperation) string {
	return fmt.Sprintf("%s/%s/%s", operation.ID, operation.Status, operation.Stage)
}
