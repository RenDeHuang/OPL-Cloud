package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

var errWorkspaceLaunchDisposableResetNotEligible = errors.New("workspace_launch_disposable_reset_not_eligible")
var workspaceLaunchDisposableResetDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

const workspaceLaunchDisposableResetOperationEnv = "OPL_WORKSPACE_LAUNCH_DISPOSABLE_RESET_OPERATION_ID"

type workspaceLaunchDisposableOwnerState string

const (
	workspaceLaunchDisposableOwnerAbsent    workspaceLaunchDisposableOwnerState = "absent"
	workspaceLaunchDisposableOwnerConfirmed workspaceLaunchDisposableOwnerState = "confirmed"
	workspaceLaunchDisposableOwnerUnknown   workspaceLaunchDisposableOwnerState = "unknown"
	workspaceLaunchDisposableOwnerConflict  workspaceLaunchDisposableOwnerState = "conflict"
)

type workspaceLaunchDisposableResetFacts struct {
	DisposableAuthority bool
	WorkspaceProjection workspaceLaunchDisposableOwnerState
	CompetingOperations workspaceLaunchDisposableOwnerState
	PreflightBinding    workspaceLaunchDisposableOwnerState
	FabricStages        workspaceLaunchDisposableOwnerState
	ProviderResources   workspaceLaunchDisposableOwnerState
	WorkspaceRuntime    workspaceLaunchDisposableOwnerState
	WorkspaceKey        workspaceLaunchDisposableOwnerState
	Debit               workspaceLaunchDisposableOwnerState
	LedgerReceipts      workspaceLaunchDisposableOwnerState
}

type workspaceLaunchDisposableOwnerObservation struct {
	State           workspaceLaunchDisposableOwnerState `json:"state"`
	Count           int                                 `json:"count"`
	AmountUSDMicros int64                               `json:"amountUsdMicros"`
	IdentityDigests []string                            `json:"identityDigests"`
}

type workspaceLaunchDisposableResetInventory struct {
	Facts        workspaceLaunchDisposableResetFacts
	Observations map[string]workspaceLaunchDisposableOwnerObservation
	Blockers     []string
}

type workspaceLaunchDisposableResetClassification struct {
	OperationID         string
	AccountID           string
	WorkspaceID         string
	PreflightBindingRef string
	Version             int
	Stage               string
	Status              string
	Facts               workspaceLaunchDisposableResetFacts
	Observations        map[string]workspaceLaunchDisposableOwnerObservation
	PlanSteps           []string
	ResetPlanDigest     string
}

type workspaceLaunchDisposableResetPreview struct {
	SchemaVersion           int                                                  `json:"schemaVersion"`
	Eligible                bool                                                 `json:"eligible"`
	OperationIdentityDigest string                                               `json:"operationIdentityDigest"`
	AccountIdentityDigest   string                                               `json:"accountIdentityDigest"`
	WorkspaceIdentityDigest string                                               `json:"workspaceIdentityDigest"`
	OperationVersion        int                                                  `json:"operationVersion"`
	Stage                   string                                               `json:"stage"`
	Status                  string                                               `json:"status"`
	OwnerStates             map[string]workspaceLaunchDisposableOwnerState       `json:"ownerStates"`
	OwnerObservations       map[string]workspaceLaunchDisposableOwnerObservation `json:"ownerObservations"`
	Blockers                []string                                             `json:"blockers"`
	PlanSteps               []string                                             `json:"planSteps"`
	ResetPlanDigest         string                                               `json:"resetPlanDigest"`
	MutationBudget          int                                                  `json:"mutationBudget"`
}

func classifyWorkspaceLaunchDisposableReset(row map[string]any, facts workspaceLaunchDisposableResetFacts) (workspaceLaunchDisposableResetClassification, error) {
	return classifyWorkspaceLaunchDisposableResetInventory(row, workspaceLaunchDisposableResetInventory{Facts: facts})
}

func classifyWorkspaceLaunchDisposableResetInventory(row map[string]any, inventory workspaceLaunchDisposableResetInventory) (workspaceLaunchDisposableResetClassification, error) {
	facts := inventory.Facts
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil || operation.SchemaVersion != workspaceLaunchReconcileSchemaVersion || operation.Version <= 0 || operation.Stage != "debit" || operation.Status != "manual_review" ||
		stringValue(row["action"]) != workspaceLaunchAction || !facts.DisposableAuthority ||
		facts.WorkspaceProjection != workspaceLaunchDisposableOwnerAbsent || facts.CompetingOperations != workspaceLaunchDisposableOwnerAbsent ||
		facts.PreflightBinding != workspaceLaunchDisposableOwnerConfirmed || facts.FabricStages != workspaceLaunchDisposableOwnerAbsent ||
		facts.ProviderResources != workspaceLaunchDisposableOwnerAbsent || facts.WorkspaceRuntime != workspaceLaunchDisposableOwnerAbsent ||
		!workspaceLaunchDisposableResetFactsDeterminate(facts) || len(inventory.Blockers) != 0 {
		return workspaceLaunchDisposableResetClassification{}, errWorkspaceLaunchDisposableResetNotEligible
	}
	classification := workspaceLaunchDisposableResetClassification{
		OperationID: operation.ID, AccountID: operation.stringFact("accountId"), WorkspaceID: operation.stringFact("workspaceId"),
		PreflightBindingRef: operation.stringFact("preflightBindingRef"), Version: operation.Version, Stage: operation.Stage, Status: operation.Status,
		Facts: facts, Observations: cloneWorkspaceLaunchDisposableObservations(inventory.Observations),
		PlanSteps: workspaceLaunchDisposableMinimalPlan(facts),
	}
	if classification.OperationID == "" || classification.AccountID == "" || classification.WorkspaceID == "" || classification.PreflightBindingRef == "" {
		return workspaceLaunchDisposableResetClassification{}, errWorkspaceLaunchDisposableResetNotEligible
	}
	classification.ResetPlanDigest, err = workspaceLaunchDisposableResetPlanDigest(classification)
	if err != nil {
		return workspaceLaunchDisposableResetClassification{}, errWorkspaceLaunchDisposableResetNotEligible
	}
	return classification, nil
}

func workspaceLaunchDisposableResetFactsDeterminate(facts workspaceLaunchDisposableResetFacts) bool {
	states := []workspaceLaunchDisposableOwnerState{
		facts.WorkspaceProjection, facts.CompetingOperations, facts.PreflightBinding, facts.FabricStages, facts.ProviderResources,
		facts.WorkspaceRuntime, facts.WorkspaceKey, facts.Debit, facts.LedgerReceipts,
	}
	for _, state := range states {
		if state != workspaceLaunchDisposableOwnerAbsent && state != workspaceLaunchDisposableOwnerConfirmed {
			return false
		}
	}
	return true
}

func workspaceLaunchDisposableResetPlanDigest(classification workspaceLaunchDisposableResetClassification) (string, error) {
	payload := struct {
		SchemaVersion       int                                                  `json:"schemaVersion"`
		DisposableAuthority bool                                                 `json:"disposableAuthority"`
		OperationID         string                                               `json:"operationId"`
		AccountID           string                                               `json:"accountId"`
		WorkspaceID         string                                               `json:"workspaceId"`
		PreflightBindingRef string                                               `json:"preflightBindingRef"`
		OperationVersion    int                                                  `json:"operationVersion"`
		Stage               string                                               `json:"stage"`
		Status              string                                               `json:"status"`
		OwnerStates         map[string]workspaceLaunchDisposableOwnerState       `json:"ownerStates"`
		OwnerObservations   map[string]workspaceLaunchDisposableOwnerObservation `json:"ownerObservations"`
		PlanSteps           []string                                             `json:"planSteps"`
	}{
		SchemaVersion: 1, DisposableAuthority: classification.Facts.DisposableAuthority,
		OperationID: classification.OperationID, AccountID: classification.AccountID, WorkspaceID: classification.WorkspaceID,
		PreflightBindingRef: classification.PreflightBindingRef, OperationVersion: classification.Version, Stage: classification.Stage, Status: classification.Status,
		OwnerStates: workspaceLaunchDisposableResetOwnerStates(classification.Facts), OwnerObservations: classification.Observations, PlanSteps: classification.PlanSteps,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("sha256:%x", digest[:]), nil
}

func workspaceLaunchDisposableResetOwnerStates(facts workspaceLaunchDisposableResetFacts) map[string]workspaceLaunchDisposableOwnerState {
	return map[string]workspaceLaunchDisposableOwnerState{
		"workspaceProjection": facts.WorkspaceProjection,
		"competingOperations": facts.CompetingOperations,
		"preflightBinding":    facts.PreflightBinding,
		"fabricStages":        facts.FabricStages,
		"providerResources":   facts.ProviderResources,
		"workspaceRuntime":    facts.WorkspaceRuntime,
		"workspaceKey":        facts.WorkspaceKey,
		"debit":               facts.Debit,
		"ledgerReceipts":      facts.LedgerReceipts,
	}
}

func workspaceLaunchDisposableResetPreviewResponse(classification workspaceLaunchDisposableResetClassification) workspaceLaunchDisposableResetPreview {
	return workspaceLaunchDisposableResetPreview{
		SchemaVersion: 1, Eligible: true,
		OperationIdentityDigest: workspaceLaunchDisposableResetIdentityDigest("operation", classification.OperationID),
		AccountIdentityDigest:   workspaceLaunchDisposableResetIdentityDigest("account", classification.AccountID),
		WorkspaceIdentityDigest: workspaceLaunchDisposableResetIdentityDigest("workspace", classification.WorkspaceID),
		OperationVersion:        classification.Version, Stage: classification.Stage, Status: classification.Status,
		OwnerStates: workspaceLaunchDisposableResetOwnerStates(classification.Facts), OwnerObservations: cloneWorkspaceLaunchDisposableObservations(classification.Observations),
		PlanSteps: append([]string(nil), classification.PlanSteps...), Blockers: []string{},
		ResetPlanDigest: classification.ResetPlanDigest, MutationBudget: 0,
	}
}

func workspaceLaunchDisposableMinimalPlan(facts workspaceLaunchDisposableResetFacts) []string {
	steps := make([]string, 0, 4)
	if facts.WorkspaceKey == workspaceLaunchDisposableOwnerConfirmed {
		steps = append(steps, "workspace_key")
	}
	if facts.Debit == workspaceLaunchDisposableOwnerConfirmed {
		steps = append(steps, "debit_compensation")
	}
	steps = append(steps, "ledger_evidence", "launch_terminalization")
	return steps
}

func cloneWorkspaceLaunchDisposableObservations(input map[string]workspaceLaunchDisposableOwnerObservation) map[string]workspaceLaunchDisposableOwnerObservation {
	if input == nil {
		return nil
	}
	output := make(map[string]workspaceLaunchDisposableOwnerObservation, len(input))
	for key, value := range input {
		value.IdentityDigests = append([]string(nil), value.IdentityDigests...)
		sort.Strings(value.IdentityDigests)
		output[key] = value
	}
	return output
}

func workspaceLaunchDisposableObservation(state workspaceLaunchDisposableOwnerState, amount int64, identities ...string) workspaceLaunchDisposableOwnerObservation {
	digests := []string{}
	for _, identity := range identities {
		if strings.TrimSpace(identity) != "" {
			digests = append(digests, workspaceLaunchDisposableResetIdentityDigest("owner-fact", identity))
		}
	}
	sort.Strings(digests)
	return workspaceLaunchDisposableOwnerObservation{State: state, Count: len(digests), AmountUSDMicros: amount, IdentityDigests: digests}
}

func workspaceLaunchDisposableResetAuthority(operationID string) bool {
	configured := strings.TrimSpace(os.Getenv(workspaceLaunchDisposableResetOperationEnv))
	return configured != "" && configured == operationID
}

func (app *controlPlaneServer) previewWorkspaceLaunchDisposableReset(ctx context.Context, service *controlplane.Service, operationID string) (workspaceLaunchDisposableResetPreview, error) {
	if app == nil || service == nil || strings.TrimSpace(operationID) == "" {
		return workspaceLaunchDisposableResetPreview{}, errWorkspaceLaunchDisposableResetNotEligible
	}
	row, found, err := app.tables.GetRuntimeOperation(ctx, operationID)
	if err != nil || !found {
		return workspaceLaunchDisposableResetPreview{}, errWorkspaceLaunchDisposableResetNotEligible
	}
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil || operation.ID != operationID || operation.Stage != "debit" || operation.Status != "manual_review" {
		return workspaceLaunchDisposableResetPreview{}, errWorkspaceLaunchDisposableResetNotEligible
	}
	inventory := app.workspaceLaunchDisposableResetInventory(ctx, service, operation)
	classification, classifyErr := classifyWorkspaceLaunchDisposableResetInventory(row, inventory)
	if classifyErr == nil {
		return workspaceLaunchDisposableResetPreviewResponse(classification), nil
	}
	preview := workspaceLaunchDisposableResetPreview{
		SchemaVersion: 1, Eligible: false,
		OperationIdentityDigest: workspaceLaunchDisposableResetIdentityDigest("operation", operation.ID),
		AccountIdentityDigest:   workspaceLaunchDisposableResetIdentityDigest("account", operation.stringFact("accountId")),
		WorkspaceIdentityDigest: workspaceLaunchDisposableResetIdentityDigest("workspace", operation.stringFact("workspaceId")),
		OperationVersion:        operation.Version, Stage: operation.Stage, Status: operation.Status,
		OwnerStates: workspaceLaunchDisposableResetOwnerStates(inventory.Facts), OwnerObservations: cloneWorkspaceLaunchDisposableObservations(inventory.Observations),
		Blockers: append([]string(nil), inventory.Blockers...), MutationBudget: 0,
	}
	return preview, errWorkspaceLaunchDisposableResetNotEligible
}

type workspaceLaunchDisposableInventoryResult struct {
	observations map[string]workspaceLaunchDisposableOwnerObservation
	blockers     []string
}

func (app *controlPlaneServer) workspaceLaunchDisposableResetInventory(ctx context.Context, service *controlplane.Service, operation workspaceLaunchReconcileOperation) workspaceLaunchDisposableResetInventory {
	results := make(chan workspaceLaunchDisposableInventoryResult, 5)
	var wait sync.WaitGroup
	run := func(read func() workspaceLaunchDisposableInventoryResult) {
		wait.Add(1)
		go func() { defer wait.Done(); results <- read() }()
	}
	run(func() workspaceLaunchDisposableInventoryResult {
		return app.readWorkspaceLaunchDisposableControlPlane(ctx, operation)
	})
	run(func() workspaceLaunchDisposableInventoryResult {
		return readWorkspaceLaunchDisposableFabric(ctx, service, operation)
	})
	run(func() workspaceLaunchDisposableInventoryResult {
		return readWorkspaceLaunchDisposableKey(ctx, service, operation)
	})
	run(func() workspaceLaunchDisposableInventoryResult {
		return readWorkspaceLaunchDisposableDebit(ctx, service, operation)
	})
	run(func() workspaceLaunchDisposableInventoryResult {
		return readWorkspaceLaunchDisposableLedger(ctx, service, operation)
	})
	wait.Wait()
	close(results)
	inventory := workspaceLaunchDisposableResetInventory{Observations: map[string]workspaceLaunchDisposableOwnerObservation{}}
	inventory.Facts.DisposableAuthority = workspaceLaunchDisposableResetAuthority(operation.ID)
	if !inventory.Facts.DisposableAuthority {
		inventory.Blockers = append(inventory.Blockers, "disposable_authority_not_configured")
	}
	for result := range results {
		for name, observation := range result.observations {
			inventory.Observations[name] = observation
		}
		inventory.Blockers = append(inventory.Blockers, result.blockers...)
	}
	inventory.Facts.WorkspaceProjection = inventory.Observations["workspaceProjection"].State
	inventory.Facts.CompetingOperations = inventory.Observations["competingOperations"].State
	inventory.Facts.PreflightBinding = inventory.Observations["preflightBinding"].State
	inventory.Facts.FabricStages = inventory.Observations["fabricStages"].State
	inventory.Facts.ProviderResources = inventory.Observations["providerResources"].State
	inventory.Facts.WorkspaceRuntime = inventory.Observations["workspaceRuntime"].State
	inventory.Facts.WorkspaceKey = inventory.Observations["workspaceKey"].State
	inventory.Facts.Debit = inventory.Observations["debit"].State
	inventory.Facts.LedgerReceipts = inventory.Observations["ledgerReceipts"].State
	sort.Strings(inventory.Blockers)
	return inventory
}

func disposableInventoryResult(name string, observation workspaceLaunchDisposableOwnerObservation, blockers ...string) workspaceLaunchDisposableInventoryResult {
	return workspaceLaunchDisposableInventoryResult{observations: map[string]workspaceLaunchDisposableOwnerObservation{name: observation}, blockers: blockers}
}

func (app *controlPlaneServer) readWorkspaceLaunchDisposableControlPlane(ctx context.Context, operation workspaceLaunchReconcileOperation) workspaceLaunchDisposableInventoryResult {
	result := workspaceLaunchDisposableInventoryResult{observations: map[string]workspaceLaunchDisposableOwnerObservation{}}
	workspaceID, accountID := operation.stringFact("workspaceId"), operation.stringFact("accountId")
	_, found, err := app.tables.GetWorkspace(ctx, workspaceID)
	if err != nil {
		result.observations["workspaceProjection"] = workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerUnknown, 0)
		result.blockers = append(result.blockers, "workspace_projection_unavailable")
	} else if found {
		result.observations["workspaceProjection"] = workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerConfirmed, 0, workspaceID)
		result.blockers = append(result.blockers, "workspace_projection_present")
	} else {
		result.observations["workspaceProjection"] = workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerAbsent, 0)
	}
	rows, err := queryRuntimeOperations(ctx, app.tables, runtimeOperationQuery{AccountID: accountID})
	if err != nil {
		result.observations["competingOperations"] = workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerUnknown, 0)
		result.blockers = append(result.blockers, "competing_operations_unavailable")
		return result
	}
	identities := make([]string, 0)
	for _, row := range rows {
		id := firstNonEmpty(stringValue(row["operationId"]), stringValue(row["id"]))
		if id == operation.ID || stringValue(row["workspaceId"]) != workspaceID || terminalWorkspaceLaunchStatus(stringValue(row["status"])) {
			continue
		}
		identities = append(identities, id)
	}
	state := workspaceLaunchDisposableOwnerAbsent
	if len(identities) > 0 {
		state = workspaceLaunchDisposableOwnerConfirmed
		result.blockers = append(result.blockers, "competing_operation_present")
	}
	result.observations["competingOperations"] = workspaceLaunchDisposableObservation(state, 0, identities...)
	return result
}

func readWorkspaceLaunchDisposableFabric(ctx context.Context, service *controlplane.Service, operation workspaceLaunchReconcileOperation) workspaceLaunchDisposableInventoryResult {
	result := workspaceLaunchDisposableInventoryResult{observations: map[string]workspaceLaunchDisposableOwnerObservation{}}
	binding, err := service.ReadWorkspaceLaunchPreflight(ctx, clients.WorkspaceLaunchPreflightReadInput{ProviderBindingRef: operation.stringFact("preflightBindingRef")})
	classification := workspaceLaunchCanonicalFactRepairClassification{
		OperationID: operation.ID, AccountID: operation.stringFact("accountId"), WorkspaceID: operation.stringFact("workspaceId"),
		RequestHash: operation.stringFact("requestHash"), PackageID: operation.stringFact("packageId"), SizeGB: operation.intFact("sizeGb"),
		ProviderProfileRef: operation.stringFact("providerProfileRef"), PreflightBindingRef: operation.stringFact("preflightBindingRef"),
		WorkspaceImageDigest: operation.stringFact("workspaceImageDigest"),
	}
	if err != nil {
		result.observations["preflightBinding"] = workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerUnknown, 0)
		result.blockers = append(result.blockers, "preflight_binding_unavailable")
	} else if !workspaceLaunchCanonicalFactRepairBindingMatches(classification, binding) || binding.SpecDigest != operation.stringFact("specDigest") {
		result.observations["preflightBinding"] = workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerConflict, 0, binding.ProviderBindingRef)
		result.blockers = append(result.blockers, "preflight_binding_conflict")
	} else {
		result.observations["preflightBinding"] = workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerConfirmed, 0, binding.ProviderBindingRef)
	}
	stageIdentities, resourceIdentities := make([]string, 0), make([]string, 0)
	stageState := workspaceLaunchDisposableOwnerAbsent
	for _, stage := range []string{"ensure_compute_allocation", "storage", "attachment", "secret", "runtime"} {
		staged := operationWithStage(operation, stage)
		input, inputErr := (&controlPlaneWorkspaceLaunchStageAdapter{app: &controlPlaneServer{}, service: service}).workspaceLaunchFabricStageInput(ctx, staged, false)
		if inputErr != nil {
			stageState = workspaceLaunchDisposableOwnerUnknown
			result.blockers = append(result.blockers, "fabric_stage_input_invalid")
			break
		}
		readback, readErr := service.ReadWorkspaceLaunchStage(ctx, input)
		if readErr != nil {
			stageState = workspaceLaunchDisposableOwnerUnknown
			result.blockers = append(result.blockers, "fabric_stage_unavailable")
			break
		}
		if readback.SchemaVersion == clients.WorkspaceLaunchFabricSchemaVersion && readback.Binding == input.Binding &&
			(readback.State == workspaceLaunchStageReady || readback.State == workspaceLaunchStagePending) {
			stageState = workspaceLaunchDisposableOwnerConflict
			stageIdentities = append(stageIdentities, stage)
			resourceIdentities = append(resourceIdentities, workspaceLaunchDisposableResourceIdentities(readback.Resources)...)
			continue
		}
		observation, observationErr := workspaceLaunchFabricObservation(staged, input, readback)
		if observationErr != nil || observation.State == workspaceLaunchStageUnknown {
			stageState = workspaceLaunchDisposableOwnerUnknown
			result.blockers = append(result.blockers, "fabric_stage_unknown")
			break
		}
		if observation.State != workspaceLaunchStageAbsent {
			stageState = workspaceLaunchDisposableOwnerConflict
			stageIdentities = append(stageIdentities, stage)
			for key, value := range observation.Facts {
				if strings.HasSuffix(key, "Id") || strings.HasSuffix(key, "Ref") {
					resourceIdentities = append(resourceIdentities, fmt.Sprint(value))
				}
			}
		}
	}
	if stageState == workspaceLaunchDisposableOwnerConflict {
		result.blockers = append(result.blockers, "fabric_residual_present")
	}
	result.observations["fabricStages"] = workspaceLaunchDisposableObservation(stageState, 0, stageIdentities...)
	// Fabric stage records are not provider inventory. Until Fabric exposes its
	// owner-authoritative aggregate observation, provider absence is unknown.
	result.observations["providerResources"] = workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerUnknown, 0, resourceIdentities...)
	result.blockers = append(result.blockers, "provider_resources_observation_unavailable")
	runtime, runtimeErr := service.ObserveWorkspaceDeleteRuntime(ctx, operation.stringFact("workspaceId"))
	secret, secretErr := service.ObserveWorkspaceDeleteRuntimeGatewaySecret(ctx, operation.stringFact("workspaceId"))
	runtimeState, runtimeIdentities := workspaceLaunchDisposableOwnerAbsent, []string{}
	if runtimeErr != nil || secretErr != nil {
		runtimeState = workspaceLaunchDisposableOwnerUnknown
		result.blockers = append(result.blockers, "workspace_runtime_observation_unavailable")
	} else {
		for _, state := range []string{runtime.State, secret.State} {
			if state == clients.WorkspaceOwnerObservationConflict || state == clients.WorkspaceOwnerObservationError {
				runtimeState = workspaceLaunchDisposableOwnerConflict
			}
			if state == clients.WorkspaceOwnerObservationReady || state == clients.WorkspaceOwnerObservationPending {
				runtimeState = workspaceLaunchDisposableOwnerConflict
			}
		}
		if runtime.Runtime != nil {
			runtimeIdentities = append(runtimeIdentities, runtime.Runtime.ID)
		}
		if secret.Binding != nil {
			runtimeIdentities = append(runtimeIdentities, secret.Binding.SecretRef)
		}
		if runtimeState == workspaceLaunchDisposableOwnerConflict {
			result.blockers = append(result.blockers, "workspace_runtime_residual_present")
		}
	}
	result.observations["workspaceRuntime"] = workspaceLaunchDisposableObservation(runtimeState, 0, runtimeIdentities...)
	return result
}

func workspaceLaunchDisposableResourceIdentities(resources clients.WorkspaceLaunchResources) []string {
	identities := []string{}
	for _, identity := range []string{
		resources.ComputeAllocationID, resources.ComputeBindingRef, resources.StorageID, resources.StorageBindingRef,
		resources.AttachmentID, resources.AttachmentBindingRef, resources.GatewaySecretRef, resources.SecretBindingRef,
		resources.RuntimeID, resources.RuntimeBindingRef,
	} {
		if strings.TrimSpace(identity) != "" {
			identities = append(identities, identity)
		}
	}
	return identities
}

func readWorkspaceLaunchDisposableKey(ctx context.Context, service *controlplane.Service, operation workspaceLaunchReconcileOperation) workspaceLaunchDisposableInventoryResult {
	keys, err := service.WorkspaceKeysForConvergence(ctx, operation.int64Fact("sub2apiUserId"), workspaceReservedKeyName(operation.stringFact("workspaceId")))
	if err != nil {
		return disposableInventoryResult("workspaceKey", workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerUnknown, 0), "workspace_key_unavailable")
	}
	keys = workspaceKeysNamed(keys, workspaceReservedKeyName(operation.stringFact("workspaceId")))
	if len(keys) == 0 {
		return disposableInventoryResult("workspaceKey", workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerAbsent, 0))
	}
	identities := make([]string, 0, len(keys))
	for _, key := range keys {
		identities = append(identities, fmt.Sprintf("%d", key.ID))
	}
	if len(keys) != 1 || keys[0].ID != operation.int64Fact("workspaceApiKeyId") || keys[0].UserID != operation.int64Fact("sub2apiUserId") || keys[0].GroupID == nil || *keys[0].GroupID != operation.int64Fact("workspaceKeyGroupId") || workspaceLaunchCredentialFingerprint(keys[0].Key) != operation.stringFact("workspaceKeyFingerprint") {
		return disposableInventoryResult("workspaceKey", workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerConflict, 0, identities...), "workspace_key_conflict")
	}
	return disposableInventoryResult("workspaceKey", workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerConfirmed, 0, identities...))
}

func readWorkspaceLaunchDisposableDebit(ctx context.Context, service *controlplane.Service, operation workspaceLaunchReconcileOperation) workspaceLaunchDisposableInventoryResult {
	if operation.raw["resourceBillingEnabled"] != nil && !operation.boolFact("resourceBillingEnabled") {
		return disposableInventoryResult("debit", workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerAbsent, 0))
	}
	code, amount, userID := operation.stringFact("sub2apiRedeemCode"), operation.int64Fact("totalChargeUsdMicros"), operation.int64Fact("sub2apiUserId")
	history, err := service.FinancialBalanceHistoryByCodes(ctx, userID, []string{code})
	if err != nil {
		return disposableInventoryResult("debit", workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerUnknown, amount), "debit_history_unavailable")
	}
	entry, found := history[code]
	if !found {
		return disposableInventoryResult("debit", workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerAbsent, 0))
	}
	if reason := sub2APIReconciliationCode(map[string]any{"sub2apiRedeemCode": code, "chargeUsdMicros": amount}, userID, history); reason != "" || entry.UsedAt == nil || entry.UsedAt.IsZero() {
		return disposableInventoryResult("debit", workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerConflict, amount, code), "debit_history_conflict")
	}
	return disposableInventoryResult("debit", workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerConfirmed, amount, code))
}

func readWorkspaceLaunchDisposableLedger(ctx context.Context, service *controlplane.Service, operation workspaceLaunchReconcileOperation) workspaceLaunchDisposableInventoryResult {
	receipts, err := reconciliationLedgerReceipts(ctx, service, operation.stringFact("accountId"))
	if err != nil {
		return disposableInventoryResult("ledgerReceipts", workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerUnknown, 0), "ledger_receipts_unavailable")
	}
	identities := make([]string, 0)
	for _, receipt := range receipts {
		if receipt.WorkspaceID != operation.stringFact("workspaceId") {
			continue
		}
		if receipt.RequestID != operation.ID && stringValue(receipt.Execution["operationId"]) != operation.ID {
			continue
		}
		identities = append(identities, receipt.ReceiptID)
	}
	if len(identities) > 1 {
		return disposableInventoryResult("ledgerReceipts", workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerConflict, 0, identities...), "ledger_receipt_duplicate")
	}
	if len(identities) == 1 {
		return disposableInventoryResult("ledgerReceipts", workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerConfirmed, 0, identities...))
	}
	return disposableInventoryResult("ledgerReceipts", workspaceLaunchDisposableObservation(workspaceLaunchDisposableOwnerAbsent, 0))
}

func workspaceLaunchDisposableResetIdentityDigest(kind, identity string) string {
	digest := sha256.Sum256([]byte(kind + "\x00" + identity))
	return fmt.Sprintf("sha256:%x", digest[:])
}
