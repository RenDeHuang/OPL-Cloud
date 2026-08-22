package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func historicalWorkspaceLaunchMissingSpecDigest(t *testing.T) map[string]any {
	t.Helper()
	operation, err := newWorkspaceLaunchReconcileOperation(workspaceLaunchReconcileCreate{
		OperationID: "workspace-launch-repair", RequestHash: strings.Repeat("a", 64), AccountID: "acct-repair", OwnerUserID: "usr-repair",
		Sub2APIUserID: 41, WorkspaceKeyGroupID: 42, WorkspaceID: "ws-repair", Name: "Repair", PackageID: "basic", StorageGB: 10,
		PriceVersion: "price-v1", TotalChargeUSDMicros: 1000000, ProviderProfileRef: "tencent-tke",
		PreflightBindingRef: "fabric-provider-binding:repair", SpecDigest: strings.Repeat("b", 64),
		WorkspaceImageDigest: "registry.example/workspace@sha256:" + strings.Repeat("c", 64), PreChargeBalanceMicros: 2000000,
		CreatedAt: time.Date(2026, 8, 18, 1, 2, 3, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation.Version = 5
	operation.Stage, operation.Status = "debit", "manual_review"
	attempt := operation.Attempts["key"]
	attempt.Attempted, attempt.Confirmed, attempt.Status, attempt.IdempotencyKey = 1, 1, "confirmed", workspaceLaunchStageIdempotencyKey(operationWithStage(operation, "key"), 1)
	operation.Attempts["key"] = attempt
	operation.Observations["key"] = workspaceLaunchStageObservation{State: workspaceLaunchStageReady, Facts: workspaceLaunchReadyFacts("key")}
	row, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(stringValue(row["result"])), &raw) != nil {
		t.Fatal("decode row")
	}
	delete(raw, "specDigest")
	encoded, _ := json.Marshal(raw)
	row["result"] = string(encoded)
	return row
}

func TestWorkspaceLaunchCanonicalFactRepairCandidateRestoresStrictDecode(t *testing.T) {
	row := historicalWorkspaceLaunchMissingSpecDigest(t)
	if _, err := decodeWorkspaceLaunchReconcileOperation(row); workspaceLaunchDecodeFailureCategory(err) != "missing_canonical_facts" {
		t.Fatalf("failure=%v category=%s", err, workspaceLaunchDecodeFailureCategory(err))
	}
	classification, err := classifyWorkspaceLaunchCanonicalFactRepair(row)
	if err != nil {
		t.Fatal(err)
	}
	if classification.Version != 5 || classification.Stage != "debit" || classification.Status != "manual_review" || classification.PreflightBindingRef == "" {
		t.Fatalf("classification=%#v", classification)
	}
	digest := strings.Repeat("d", 64)
	preview, err := buildWorkspaceLaunchCanonicalFactRepairPreview(row, digest)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := decodeWorkspaceLaunchReconcileOperation(preview.DesiredOperation)
	if err != nil || operation.Version != 6 || operation.stringFact("specDigest") != digest || operation.Stage != "debit" || operation.Status != "manual_review" {
		t.Fatalf("operation=%#v err=%v", operation, err)
	}
	if len(preview.ChangedFields) != 2 || preview.ChangedFields[0] != "specDigest" || preview.ChangedFields[1] != "version" || !workspaceLaunchRepairDigestPattern.MatchString(preview.PreviewDigest) {
		t.Fatalf("preview=%#v", preview)
	}
}

func TestWorkspaceLaunchCanonicalFactRepairClassificationRejectsAnythingExceptKnownDefect(t *testing.T) {
	tests := map[string]func(map[string]json.RawMessage, map[string]any){
		"wrong schema":        func(raw map[string]json.RawMessage, _ map[string]any) { raw["schemaVersion"] = json.RawMessage(`2`) },
		"wrong stage":         func(raw map[string]json.RawMessage, _ map[string]any) { raw["stage"] = json.RawMessage(`"key"`) },
		"wrong status":        func(_ map[string]json.RawMessage, row map[string]any) { row["status"] = "pending" },
		"second missing fact": func(raw map[string]json.RawMessage, _ map[string]any) { delete(raw, "requestHash") },
		"legacy field":        func(raw map[string]json.RawMessage, _ map[string]any) { raw["phase"] = json.RawMessage(`"legacy"`) },
		"existing digest": func(raw map[string]json.RawMessage, _ map[string]any) {
			raw["specDigest"] = json.RawMessage(`"` + strings.Repeat("e", 64) + `"`)
		},
		"invalid version": func(raw map[string]json.RawMessage, _ map[string]any) { raw["version"] = json.RawMessage(`0`) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			row := historicalWorkspaceLaunchMissingSpecDigest(t)
			var raw map[string]json.RawMessage
			_ = json.Unmarshal([]byte(stringValue(row["result"])), &raw)
			mutate(raw, row)
			encoded, _ := json.Marshal(raw)
			row["result"] = string(encoded)
			if _, err := classifyWorkspaceLaunchCanonicalFactRepair(row); err == nil {
				t.Fatal("eligible")
			}
		})
	}
}

func TestWorkspaceLaunchCanonicalFactRepairPreviewDigestBindsCurrentResultAndSpecDigest(t *testing.T) {
	row := historicalWorkspaceLaunchMissingSpecDigest(t)
	first, err := buildWorkspaceLaunchCanonicalFactRepairPreview(row, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	second, _ := buildWorkspaceLaunchCanonicalFactRepairPreview(row, strings.Repeat("e", 64))
	if first.PreviewDigest == second.PreviewDigest {
		t.Fatal("digest ignores specDigest")
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal([]byte(stringValue(row["result"])), &raw)
	raw["version"] = json.RawMessage(`6`)
	encoded, _ := json.Marshal(raw)
	row["result"] = string(encoded)
	third, err := buildWorkspaceLaunchCanonicalFactRepairPreview(row, strings.Repeat("d", 64))
	if err != nil || first.PreviewDigest == third.PreviewDigest {
		t.Fatalf("digest ignores current result: first=%s third=%s err=%v", first.PreviewDigest, third.PreviewDigest, err)
	}
}
