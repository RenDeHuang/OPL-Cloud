package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

var errWorkspaceLaunchCanonicalFactRepairNotEligible = errors.New("workspace_launch_canonical_fact_repair_not_eligible")

var workspaceLaunchRepairDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type workspaceLaunchCanonicalFactRepairClassification struct {
	Version              int
	Stage                string
	Status               string
	AccountID            string
	WorkspaceID          string
	OperationID          string
	RequestHash          string
	PackageID            string
	SizeGB               int
	ProviderProfileRef   string
	PreflightBindingRef  string
	WorkspaceImageDigest string
	PersistedResult      string
}

type workspaceLaunchCanonicalFactRepairPreview struct {
	Classification   workspaceLaunchCanonicalFactRepairClassification
	SpecDigest       string
	DesiredOperation map[string]any
	ChangedFields    []string
	PreviewDigest    string
}

func workspaceLaunchCanonicalFactRepairAuditID(operationID, key string) string {
	return "audit-" + stableID("workspace.launch.canonical_fact_repair", operationID, key)[:12]
}

func (app *controlPlaneServer) applyWorkspaceLaunchCanonicalFactRepair(ctx context.Context, service *controlplane.Service, operationID string, launchVersion int, previewDigest, key, reason string, audit map[string]any) (workspaceLaunchReconcileOperation, error) {
	preview, err := app.previewWorkspaceLaunchCanonicalFactRepair(ctx, service, operationID)
	if err != nil || preview.Classification.Version != launchVersion || preview.PreviewDigest != previewDigest || key == "" || reason == "" {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchCanonicalFactRepairNotEligible
	}
	audit["id"] = workspaceLaunchCanonicalFactRepairAuditID(operationID, key)
	audit["targetAccountId"] = preview.Classification.AccountID
	audit["createdAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	audit["before"] = map[string]any{"version": preview.Classification.Version, "stage": preview.Classification.Stage, "status": preview.Classification.Status}
	audit["after"] = map[string]any{
		"version": preview.Classification.Version + 1, "stage": preview.Classification.Stage, "status": preview.Classification.Status,
		"specDigestSha256": "sha256:" + preview.SpecDigest, "reason": reason, "previewDigest": preview.PreviewDigest,
	}
	if err := app.tables.ApplyWorkspaceLaunchCanonicalFactRepair(ctx, workspaceLaunchCanonicalFactRepairCAS{
		OperationID: operationID, ExpectedOperationResult: preview.Classification.PersistedResult,
		DesiredOperation: preview.DesiredOperation, AuditEvent: audit,
	}); err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	row, found, err := app.tables.GetRuntimeOperation(ctx, operationID)
	if err != nil || !found {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchCASConflict
	}
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil || operation.Version != launchVersion+1 || operation.stringFact("specDigest") != preview.SpecDigest {
		return workspaceLaunchReconcileOperation{}, errWorkspaceLaunchCASConflict
	}
	return operation, nil
}

func (app *controlPlaneServer) previewWorkspaceLaunchCanonicalFactRepair(ctx context.Context, service *controlplane.Service, operationID string) (workspaceLaunchCanonicalFactRepairPreview, error) {
	if app == nil || service == nil || operationID == "" {
		return workspaceLaunchCanonicalFactRepairPreview{}, errWorkspaceLaunchCanonicalFactRepairNotEligible
	}
	row, found, err := app.tables.GetRuntimeOperation(ctx, operationID)
	if err != nil || !found {
		return workspaceLaunchCanonicalFactRepairPreview{}, errWorkspaceLaunchCanonicalFactRepairNotEligible
	}
	classification, err := classifyWorkspaceLaunchCanonicalFactRepair(row)
	if err != nil || classification.OperationID != operationID {
		return workspaceLaunchCanonicalFactRepairPreview{}, errWorkspaceLaunchCanonicalFactRepairNotEligible
	}
	binding, err := service.ReadWorkspaceLaunchPreflight(ctx, clients.WorkspaceLaunchPreflightReadInput{ProviderBindingRef: classification.PreflightBindingRef})
	if err != nil || !workspaceLaunchCanonicalFactRepairBindingMatches(classification, binding) {
		return workspaceLaunchCanonicalFactRepairPreview{}, errWorkspaceLaunchCanonicalFactRepairNotEligible
	}
	return buildWorkspaceLaunchCanonicalFactRepairPreview(row, binding.SpecDigest)
}

func workspaceLaunchCanonicalFactRepairBindingMatches(classification workspaceLaunchCanonicalFactRepairClassification, binding clients.WorkspaceLaunchPreflightBinding) bool {
	return binding.SchemaVersion == clients.WorkspaceLaunchFabricSchemaVersion &&
		binding.LaunchOperationID == classification.OperationID && binding.AccountID == classification.AccountID && binding.WorkspaceID == classification.WorkspaceID &&
		binding.PackageID == classification.PackageID && binding.SizeGB == classification.SizeGB && binding.WorkspaceImageDigest == classification.WorkspaceImageDigest &&
		binding.RequestHash == classification.RequestHash && binding.ProviderProfileRef == classification.ProviderProfileRef &&
		binding.ProviderBindingRef == classification.PreflightBindingRef && workspaceProviderSpecDigestPattern.MatchString(binding.SpecDigest)
}

func workspaceLaunchCanonicalFactRepairPreviewResponse(preview workspaceLaunchCanonicalFactRepairPreview) map[string]any {
	identity := sha256.Sum256([]byte(preview.Classification.OperationID))
	binding := sha256.Sum256([]byte(preview.Classification.PreflightBindingRef))
	return map[string]any{
		"schemaVersion": 1, "eligible": true, "operationIdentityDigest": fmt.Sprintf("sha256:%x", identity[:]),
		"operationVersion": preview.Classification.Version, "proposedVersion": preview.Classification.Version + 1,
		"failureCategory": "missing_canonical_facts", "missingCanonicalKeys": []string{"specDigest"},
		"fabricBindingState": "confirmed", "bindingIdentityDigest": fmt.Sprintf("sha256:%x", binding[:]),
		"identityMatched": true, "changedFields": append([]string(nil), preview.ChangedFields...),
		"previewDigest": preview.PreviewDigest, "mutationBudget": 0,
	}
}

func workspaceLaunchCanonicalFactRepairApplyResponse(operation workspaceLaunchReconcileOperation, auditID string) map[string]any {
	identity := sha256.Sum256([]byte(operation.ID))
	audit := sha256.Sum256([]byte(auditID))
	return map[string]any{
		"schemaVersion": 1, "status": "repaired", "operationIdentityDigest": fmt.Sprintf("sha256:%x", identity[:]),
		"operationVersion": operation.Version, "stage": operation.Stage, "operationStatus": operation.Status,
		"changedFields": []string{"specDigest", "version"}, "strictDecoder": "passed",
		"auditIdentityDigest": fmt.Sprintf("sha256:%x", audit[:]), "operationMutationCount": 1, "auditMutationCount": 1,
		"providerMutationCount": 0, "debitMutationCount": 0, "workspaceMutationCount": 0, "receiptMutationCount": 0,
	}
}

func validateWorkspaceLaunchCanonicalFactRepairCAS(update workspaceLaunchCanonicalFactRepairCAS, current map[string]any) error {
	classification, err := classifyWorkspaceLaunchCanonicalFactRepair(current)
	if err != nil || update.OperationID != classification.OperationID || update.ExpectedOperationResult != classification.PersistedResult || stringValue(current["result"]) != update.ExpectedOperationResult {
		return errWorkspaceLaunchCASConflict
	}
	desired, err := decodeWorkspaceLaunchReconcileOperation(update.DesiredOperation)
	if err != nil || desired.ID != classification.OperationID || desired.Version != classification.Version+1 || desired.Stage != classification.Stage || desired.Status != classification.Status {
		return errWorkspaceLaunchCASConflict
	}
	var currentRaw, desiredRaw map[string]json.RawMessage
	if json.Unmarshal([]byte(classification.PersistedResult), &currentRaw) != nil || json.Unmarshal([]byte(stringValue(update.DesiredOperation["result"])), &desiredRaw) != nil {
		return errWorkspaceLaunchCASConflict
	}
	changed := workspaceLaunchCanonicalFactRepairChangedFields(currentRaw, desiredRaw)
	if len(changed) != 2 || changed[0] != "specDigest" || changed[1] != "version" || !workspaceProviderSpecDigestPattern.MatchString(desired.stringFact("specDigest")) {
		return errWorkspaceLaunchCASConflict
	}
	if !workspaceLaunchCanonicalFactRepairAuditValid(update, classification, desired.stringFact("specDigest")) {
		return errIdempotencyConflict
	}
	return nil
}

func workspaceLaunchCanonicalFactRepairAuditValid(update workspaceLaunchCanonicalFactRepairCAS, classification workspaceLaunchCanonicalFactRepairClassification, specDigest string) bool {
	audit := update.AuditEvent
	before, after := mapField(audit, "before"), mapField(audit, "after")
	return stringValue(audit["id"]) != "" && stringValue(audit["actorUserId"]) != "" && stringValue(audit["action"]) == "workspace.launch.canonical_fact_repair" &&
		stringValue(audit["resourceKind"]) == "workspace_launch" && stringValue(audit["resourceId"]) == classification.OperationID &&
		stringValue(audit["targetAccountId"]) == classification.AccountID && stringValue(audit["result"]) == "succeeded" && stringValue(audit["createdAt"]) != "" &&
		numberField(before, "version", 0) == float64(classification.Version) && stringValue(before["status"]) == classification.Status && stringValue(before["stage"]) == classification.Stage &&
		numberField(after, "version", 0) == float64(classification.Version+1) && stringValue(after["status"]) == classification.Status && stringValue(after["stage"]) == classification.Stage &&
		stringValue(after["specDigestSha256"]) == "sha256:"+specDigest
}

func workspaceLaunchCanonicalFactRepairAuditMatches(existing, desired map[string]any) bool {
	return string(mustJSON(existing)) == string(mustJSON(desired))
}

func classifyWorkspaceLaunchCanonicalFactRepair(row map[string]any) (workspaceLaunchCanonicalFactRepairClassification, error) {
	result := stringValue(row["result"])
	var raw map[string]json.RawMessage
	if result == "" || json.Unmarshal([]byte(result), &raw) != nil || raw == nil {
		return workspaceLaunchCanonicalFactRepairClassification{}, errWorkspaceLaunchCanonicalFactRepairNotEligible
	}
	_, decodeErr := decodeWorkspaceLaunchReconcileOperation(row)
	if workspaceLaunchDecodeFailureCategory(decodeErr) != "missing_canonical_facts" {
		return workspaceLaunchCanonicalFactRepairClassification{}, errWorkspaceLaunchCanonicalFactRepairNotEligible
	}
	if missing := workspaceLaunchMissingCanonicalKeys(raw); len(missing) != 1 || missing[0] != "specDigest" {
		return workspaceLaunchCanonicalFactRepairClassification{}, errWorkspaceLaunchCanonicalFactRepairNotEligible
	}
	for _, field := range workspaceLaunchReconcileForbiddenFields {
		if _, exists := raw[field]; exists {
			return workspaceLaunchCanonicalFactRepairClassification{}, errWorkspaceLaunchCanonicalFactRepairNotEligible
		}
	}
	var schemaVersion, version, sizeGB int
	var stage string
	if json.Unmarshal(raw["schemaVersion"], &schemaVersion) != nil || schemaVersion != workspaceLaunchReconcileSchemaVersion ||
		json.Unmarshal(raw["version"], &version) != nil || version <= 0 ||
		json.Unmarshal(raw["stage"], &stage) != nil || stage != "debit" ||
		json.Unmarshal(raw["sizeGb"], &sizeGB) != nil || sizeGB <= 0 || stringValue(row["status"]) != "manual_review" {
		return workspaceLaunchCanonicalFactRepairClassification{}, errWorkspaceLaunchCanonicalFactRepairNotEligible
	}
	classification := workspaceLaunchCanonicalFactRepairClassification{
		Version: version, Stage: stage, Status: stringValue(row["status"]), SizeGB: sizeGB, PersistedResult: result,
		OperationID: firstNonEmpty(stringValue(row["operationId"]), stringValue(row["id"])),
		AccountID:   rawStringFact(raw, "accountId"), WorkspaceID: rawStringFact(raw, "workspaceId"), RequestHash: rawStringFact(raw, "requestHash"),
		PackageID: rawStringFact(raw, "packageId"), ProviderProfileRef: rawStringFact(raw, "providerProfileRef"),
		PreflightBindingRef: rawStringFact(raw, "preflightBindingRef"), WorkspaceImageDigest: rawStringFact(raw, "workspaceImageDigest"),
	}
	if classification.OperationID == "" || classification.AccountID == "" || classification.WorkspaceID == "" || classification.RequestHash == "" ||
		classification.PackageID == "" || classification.ProviderProfileRef == "" || classification.PreflightBindingRef == "" || classification.WorkspaceImageDigest == "" ||
		stringValue(row["action"]) != workspaceLaunchAction || stringValue(row["accountId"]) != classification.AccountID || stringValue(row["workspaceId"]) != classification.WorkspaceID {
		return workspaceLaunchCanonicalFactRepairClassification{}, errWorkspaceLaunchCanonicalFactRepairNotEligible
	}
	return classification, nil
}

func rawStringFact(raw map[string]json.RawMessage, field string) string {
	var value string
	_ = json.Unmarshal(raw[field], &value)
	return value
}

func buildWorkspaceLaunchCanonicalFactRepairPreview(row map[string]any, specDigest string) (workspaceLaunchCanonicalFactRepairPreview, error) {
	classification, err := classifyWorkspaceLaunchCanonicalFactRepair(row)
	if err != nil || !workspaceProviderSpecDigestPattern.MatchString(specDigest) {
		return workspaceLaunchCanonicalFactRepairPreview{}, errWorkspaceLaunchCanonicalFactRepairNotEligible
	}
	var currentRaw map[string]json.RawMessage
	if json.Unmarshal([]byte(classification.PersistedResult), &currentRaw) != nil {
		return workspaceLaunchCanonicalFactRepairPreview{}, errWorkspaceLaunchCanonicalFactRepairNotEligible
	}
	desiredRaw := make(map[string]json.RawMessage, len(currentRaw)+1)
	for key, value := range currentRaw {
		desiredRaw[key] = append(json.RawMessage(nil), value...)
	}
	desiredRaw["specDigest"], _ = json.Marshal(specDigest)
	desiredRaw["version"], _ = json.Marshal(classification.Version + 1)
	encoded, err := json.Marshal(desiredRaw)
	if err != nil {
		return workspaceLaunchCanonicalFactRepairPreview{}, errWorkspaceLaunchCanonicalFactRepairNotEligible
	}
	desired := cloneMap(row)
	desired["result"] = string(encoded)
	operation, err := decodeWorkspaceLaunchReconcileOperation(desired)
	if err != nil || operation.Version != classification.Version+1 || operation.Stage != classification.Stage || operation.Status != classification.Status || operation.stringFact("specDigest") != specDigest {
		return workspaceLaunchCanonicalFactRepairPreview{}, errWorkspaceLaunchCanonicalFactRepairNotEligible
	}
	changed := workspaceLaunchCanonicalFactRepairChangedFields(currentRaw, desiredRaw)
	if len(changed) != 2 || changed[0] != "specDigest" || changed[1] != "version" {
		return workspaceLaunchCanonicalFactRepairPreview{}, errWorkspaceLaunchCanonicalFactRepairNotEligible
	}
	evidence, err := json.Marshal(struct {
		SchemaVersion   int      `json:"schemaVersion"`
		OperationResult string   `json:"operationResult"`
		ExpectedVersion int      `json:"expectedVersion"`
		SpecDigest      string   `json:"specDigest"`
		ChangedFields   []string `json:"changedFields"`
	}{1, classification.PersistedResult, classification.Version, specDigest, changed})
	if err != nil {
		return workspaceLaunchCanonicalFactRepairPreview{}, errWorkspaceLaunchCanonicalFactRepairNotEligible
	}
	sum := sha256.Sum256(evidence)
	return workspaceLaunchCanonicalFactRepairPreview{
		Classification: classification, SpecDigest: specDigest, DesiredOperation: desired, ChangedFields: changed,
		PreviewDigest: fmt.Sprintf("sha256:%x", sum[:]),
	}, nil
}

func workspaceLaunchCanonicalFactRepairChangedFields(current, desired map[string]json.RawMessage) []string {
	keys := make(map[string]struct{}, len(current)+len(desired))
	for key := range current {
		keys[key] = struct{}{}
	}
	for key := range desired {
		keys[key] = struct{}{}
	}
	changed := make([]string, 0, 2)
	for key := range keys {
		if string(current[key]) != string(desired[key]) {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)
	return changed
}
