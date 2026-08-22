package server

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
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

func classifyWorkspaceLaunchCanonicalFactRepair(row map[string]any) (workspaceLaunchCanonicalFactRepairClassification, error) {
	result := stringValue(row["result"])
	var raw map[string]json.RawMessage
	if result == "" || json.Unmarshal([]byte(result), &raw) != nil || raw == nil {
		return workspaceLaunchCanonicalFactRepairClassification{}, errWorkspaceLaunchCanonicalFactRepairNotEligible
	}
	if workspaceLaunchDecodeFailureCategory(func() error {
		_, err := decodeWorkspaceLaunchReconcileOperation(row)
		return err
	}()) != "missing_canonical_facts" {
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
