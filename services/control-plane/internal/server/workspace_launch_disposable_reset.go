package server

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

var errWorkspaceLaunchDisposableResetNotEligible = errors.New("workspace_launch_disposable_reset_not_eligible")
var workspaceLaunchDisposableResetDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type workspaceLaunchDisposableOwnerState string

const (
	workspaceLaunchDisposableOwnerAbsent    workspaceLaunchDisposableOwnerState = "absent"
	workspaceLaunchDisposableOwnerConfirmed workspaceLaunchDisposableOwnerState = "confirmed"
	workspaceLaunchDisposableOwnerUnknown   workspaceLaunchDisposableOwnerState = "unknown"
	workspaceLaunchDisposableOwnerConflict  workspaceLaunchDisposableOwnerState = "conflict"
)

var workspaceLaunchDisposableResetOrderedSteps = []string{
	"workspace_runtime",
	"storage_attachment",
	"storage",
	"compute",
	"provider_absence",
	"workspace_key",
	"debit_compensation",
	"ledger_evidence",
	"launch_terminalization",
	"operator_audit",
	"final_readback",
}

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

type workspaceLaunchDisposableResetClassification struct {
	OperationID         string
	AccountID           string
	WorkspaceID         string
	PreflightBindingRef string
	Version             int
	Stage               string
	Status              string
	Facts               workspaceLaunchDisposableResetFacts
	PlanSteps           []string
	ResetPlanDigest     string
}

type workspaceLaunchDisposableResetPreview struct {
	SchemaVersion           int                                            `json:"schemaVersion"`
	Eligible                bool                                           `json:"eligible"`
	OperationIdentityDigest string                                         `json:"operationIdentityDigest"`
	AccountIdentityDigest   string                                         `json:"accountIdentityDigest"`
	WorkspaceIdentityDigest string                                         `json:"workspaceIdentityDigest"`
	OperationVersion        int                                            `json:"operationVersion"`
	Stage                   string                                         `json:"stage"`
	Status                  string                                         `json:"status"`
	OwnerStates             map[string]workspaceLaunchDisposableOwnerState `json:"ownerStates"`
	PlanSteps               []string                                       `json:"planSteps"`
	ResetPlanDigest         string                                         `json:"resetPlanDigest"`
	MutationBudget          int                                            `json:"mutationBudget"`
}

func classifyWorkspaceLaunchDisposableReset(row map[string]any, facts workspaceLaunchDisposableResetFacts) (workspaceLaunchDisposableResetClassification, error) {
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil || operation.SchemaVersion != workspaceLaunchReconcileSchemaVersion || operation.Version <= 0 || operation.Stage != "debit" || operation.Status != "manual_review" ||
		stringValue(row["action"]) != workspaceLaunchAction || !facts.DisposableAuthority ||
		facts.WorkspaceProjection != workspaceLaunchDisposableOwnerAbsent || facts.CompetingOperations != workspaceLaunchDisposableOwnerAbsent ||
		facts.PreflightBinding != workspaceLaunchDisposableOwnerConfirmed || !workspaceLaunchDisposableResetFactsDeterminate(facts) {
		return workspaceLaunchDisposableResetClassification{}, errWorkspaceLaunchDisposableResetNotEligible
	}
	classification := workspaceLaunchDisposableResetClassification{
		OperationID: operation.ID, AccountID: operation.stringFact("accountId"), WorkspaceID: operation.stringFact("workspaceId"),
		PreflightBindingRef: operation.stringFact("preflightBindingRef"), Version: operation.Version, Stage: operation.Stage, Status: operation.Status,
		Facts: facts, PlanSteps: append([]string(nil), workspaceLaunchDisposableResetOrderedSteps...),
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
		SchemaVersion       int                                            `json:"schemaVersion"`
		DisposableAuthority bool                                           `json:"disposableAuthority"`
		OperationID         string                                         `json:"operationId"`
		AccountID           string                                         `json:"accountId"`
		WorkspaceID         string                                         `json:"workspaceId"`
		PreflightBindingRef string                                         `json:"preflightBindingRef"`
		OperationVersion    int                                            `json:"operationVersion"`
		Stage               string                                         `json:"stage"`
		Status              string                                         `json:"status"`
		OwnerStates         map[string]workspaceLaunchDisposableOwnerState `json:"ownerStates"`
		PlanSteps           []string                                       `json:"planSteps"`
	}{
		SchemaVersion: 1, DisposableAuthority: classification.Facts.DisposableAuthority,
		OperationID: classification.OperationID, AccountID: classification.AccountID, WorkspaceID: classification.WorkspaceID,
		PreflightBindingRef: classification.PreflightBindingRef, OperationVersion: classification.Version, Stage: classification.Stage, Status: classification.Status,
		OwnerStates: workspaceLaunchDisposableResetOwnerStates(classification.Facts), PlanSteps: classification.PlanSteps,
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
		OwnerStates: workspaceLaunchDisposableResetOwnerStates(classification.Facts), PlanSteps: append([]string(nil), classification.PlanSteps...),
		ResetPlanDigest: classification.ResetPlanDigest, MutationBudget: 0,
	}
}

func workspaceLaunchDisposableResetIdentityDigest(kind, identity string) string {
	digest := sha256.Sum256([]byte(kind + "\x00" + identity))
	return fmt.Sprintf("sha256:%x", digest[:])
}
