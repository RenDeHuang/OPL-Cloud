package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type canonicalFactRepairFabric struct {
	fakeFabricClient
	binding clients.WorkspaceLaunchPreflightBinding
	err     error
	reads   int
}

func (f *canonicalFactRepairFabric) ReadWorkspaceLaunchPreflight(_ context.Context, input clients.WorkspaceLaunchPreflightReadInput) (clients.WorkspaceLaunchPreflightBinding, error) {
	f.reads++
	if f.err != nil {
		return clients.WorkspaceLaunchPreflightBinding{}, f.err
	}
	if input.ProviderBindingRef != f.binding.ProviderBindingRef {
		return clients.WorkspaceLaunchPreflightBinding{}, errors.New("binding mismatch")
	}
	return f.binding, nil
}

func canonicalFactRepairAudit(preview workspaceLaunchCanonicalFactRepairPreview, key string) map[string]any {
	return map[string]any{
		"id": "audit-" + key, "actorUserId": "usr-operator", "actorRole": "admin", "actorAccountId": "acct-operator",
		"targetAccountId": preview.Classification.AccountID, "action": "workspace.launch.canonical_fact_repair", "resourceKind": "workspace_launch",
		"resourceId": preview.Classification.OperationID, "ipAddress": "", "userAgent": "test", "result": "succeeded", "createdAt": "2026-08-22T05:00:00Z",
		"before": map[string]any{"version": preview.Classification.Version, "stage": preview.Classification.Stage, "status": preview.Classification.Status},
		"after":  map[string]any{"version": preview.Classification.Version + 1, "stage": preview.Classification.Stage, "status": preview.Classification.Status, "specDigestSha256": "sha256:" + preview.SpecDigest},
	}
}

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

func TestMemoryWorkspaceLaunchCanonicalFactRepairIsCASAuditedAndIdempotent(t *testing.T) {
	store := newMemoryTableStore()
	row := historicalWorkspaceLaunchMissingSpecDigest(t)
	mustStore(t, store.SaveRuntimeOperation(context.Background(), row))
	preview, err := buildWorkspaceLaunchCanonicalFactRepairPreview(row, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	update := workspaceLaunchCanonicalFactRepairCAS{
		OperationID: preview.Classification.OperationID, ExpectedOperationResult: preview.Classification.PersistedResult,
		DesiredOperation: preview.DesiredOperation, AuditEvent: canonicalFactRepairAudit(preview, "repair-once"),
	}
	if err := store.ApplyWorkspaceLaunchCanonicalFactRepair(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyWorkspaceLaunchCanonicalFactRepair(context.Background(), update); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	stored, found, err := store.GetRuntimeOperation(context.Background(), preview.Classification.OperationID)
	operation, decodeErr := decodeWorkspaceLaunchReconcileOperation(stored)
	audits, auditErr := store.ListAuditEvents(context.Background(), preview.Classification.AccountID)
	if err != nil || !found || decodeErr != nil || operation.Version != 6 || operation.stringFact("specDigest") != strings.Repeat("d", 64) || auditErr != nil || len(audits) != 1 {
		t.Fatalf("operation=%#v found=%v audits=%#v errors=%v/%v/%v", operation, found, audits, err, decodeErr, auditErr)
	}
}

func TestMemoryWorkspaceLaunchCanonicalFactRepairRejectsCASAndAuditDrift(t *testing.T) {
	store := newMemoryTableStore()
	row := historicalWorkspaceLaunchMissingSpecDigest(t)
	mustStore(t, store.SaveRuntimeOperation(context.Background(), row))
	preview, _ := buildWorkspaceLaunchCanonicalFactRepairPreview(row, strings.Repeat("d", 64))
	update := workspaceLaunchCanonicalFactRepairCAS{
		OperationID: preview.Classification.OperationID, ExpectedOperationResult: preview.Classification.PersistedResult,
		DesiredOperation: preview.DesiredOperation, AuditEvent: canonicalFactRepairAudit(preview, "repair-drift"),
	}
	drifted := update
	drifted.ExpectedOperationResult += " "
	if err := store.ApplyWorkspaceLaunchCanonicalFactRepair(context.Background(), drifted); !errors.Is(err, errWorkspaceLaunchCASConflict) {
		t.Fatalf("CAS error=%v", err)
	}
	if err := store.ApplyWorkspaceLaunchCanonicalFactRepair(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	conflict := update
	conflict.AuditEvent = cloneMap(update.AuditEvent)
	conflict.AuditEvent["userAgent"] = "different"
	if err := store.ApplyWorkspaceLaunchCanonicalFactRepair(context.Background(), conflict); !errors.Is(err, errIdempotencyConflict) {
		t.Fatalf("audit conflict=%v", err)
	}
}

func TestWorkspaceLaunchCanonicalFactRepairPreviewRouteReturnsOnlyRedactedEvidence(t *testing.T) {
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-repair", "org-repair", "usr-repair", "repair@example.com")
	row := historicalWorkspaceLaunchMissingSpecDigest(t)
	mustStore(t, store.SaveRuntimeOperation(context.Background(), row))
	classification, err := classifyWorkspaceLaunchCanonicalFactRepair(row)
	if err != nil {
		t.Fatal(err)
	}
	fabric := &canonicalFactRepairFabric{binding: clients.WorkspaceLaunchPreflightBinding{
		SchemaVersion: 1, LaunchOperationID: classification.OperationID, AccountID: classification.AccountID, WorkspaceID: classification.WorkspaceID,
		PackageID: classification.PackageID, SizeGB: classification.SizeGB, WorkspaceImageDigest: classification.WorkspaceImageDigest,
		RequestHash: classification.RequestHash, ProviderProfileRef: classification.ProviderProfileRef,
		ProviderBindingRef: classification.PreflightBindingRef, SpecDigest: strings.Repeat("d", 64),
	}}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, fabric, &testSub2APIClient{balance: 1000000, charges: map[string]int64{}}), store)
	if err != nil {
		t.Fatal(err)
	}
	operator := reservedOperatorSessionForTest(t, server)
	req := httptest.NewRequest(http.MethodGet, "/api/operator/workspace-launches/"+classification.OperationID+"/canonical-facts-repair-preview", nil)
	addAuth(req, operator)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || fabric.reads != 1 {
		t.Fatalf("status=%d reads=%d body=%s", rec.Code, fabric.reads, rec.Body.String())
	}
	serialized := rec.Body.String()
	for _, secret := range []string{classification.OperationID, classification.AccountID, classification.WorkspaceID, classification.PreflightBindingRef, strings.Repeat("d", 64)} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("response leaked %q: %s", secret, serialized)
		}
	}
}

func TestWorkspaceLaunchCanonicalFactRepairPreviewRejectsFabricIdentityDrift(t *testing.T) {
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-repair", "org-repair", "usr-repair", "repair@example.com")
	row := historicalWorkspaceLaunchMissingSpecDigest(t)
	mustStore(t, store.SaveRuntimeOperation(context.Background(), row))
	classification, _ := classifyWorkspaceLaunchCanonicalFactRepair(row)
	fabric := &canonicalFactRepairFabric{binding: clients.WorkspaceLaunchPreflightBinding{
		SchemaVersion: 1, LaunchOperationID: classification.OperationID, AccountID: "acct-other", WorkspaceID: classification.WorkspaceID,
		PackageID: classification.PackageID, SizeGB: classification.SizeGB, WorkspaceImageDigest: classification.WorkspaceImageDigest,
		RequestHash: classification.RequestHash, ProviderProfileRef: classification.ProviderProfileRef,
		ProviderBindingRef: classification.PreflightBindingRef, SpecDigest: strings.Repeat("d", 64),
	}}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, fabric, &testSub2APIClient{balance: 1000000, charges: map[string]int64{}}), store)
	if err != nil {
		t.Fatal(err)
	}
	operator := reservedOperatorSessionForTest(t, server)
	req := httptest.NewRequest(http.MethodGet, "/api/operator/workspace-launches/"+classification.OperationID+"/canonical-facts-repair-preview", nil)
	addAuth(req, operator)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWorkspaceLaunchCanonicalFactRepairApplyRouteRepairsAndReplays(t *testing.T) {
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-repair", "org-repair", "usr-repair", "repair@example.com")
	row := historicalWorkspaceLaunchMissingSpecDigest(t)
	mustStore(t, store.SaveRuntimeOperation(context.Background(), row))
	classification, _ := classifyWorkspaceLaunchCanonicalFactRepair(row)
	fabric := &canonicalFactRepairFabric{binding: clients.WorkspaceLaunchPreflightBinding{
		SchemaVersion: 1, LaunchOperationID: classification.OperationID, AccountID: classification.AccountID, WorkspaceID: classification.WorkspaceID,
		PackageID: classification.PackageID, SizeGB: classification.SizeGB, WorkspaceImageDigest: classification.WorkspaceImageDigest,
		RequestHash: classification.RequestHash, ProviderProfileRef: classification.ProviderProfileRef,
		ProviderBindingRef: classification.PreflightBindingRef, SpecDigest: strings.Repeat("d", 64),
	}}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, fabric, &testSub2APIClient{balance: 1000000, charges: map[string]int64{}}), store)
	if err != nil {
		t.Fatal(err)
	}
	operator := reservedOperatorSessionForTest(t, server)
	previewReq := httptest.NewRequest(http.MethodGet, "/api/operator/workspace-launches/"+classification.OperationID+"/canonical-facts-repair-preview", nil)
	addAuth(previewReq, operator)
	previewRec := httptest.NewRecorder()
	server.ServeHTTP(previewRec, previewReq)
	var previewBody map[string]any
	_ = json.Unmarshal(previewRec.Body.Bytes(), &previewBody)
	body := `{"launchVersion":5,"previewDigest":"` + stringValue(previewBody["previewDigest"]) + `","reason":"historical schema 3 operation predates required specDigest"}`
	path := "/api/operator/workspace-launches/" + classification.OperationID + "/canonical-facts-repair"
	first := requestWithMutationKeyForTest(t, server, operator, http.MethodPost, path, body, "canonical-repair-once")
	replay := requestWithMutationKeyForTest(t, server, operator, http.MethodPost, path, body, "canonical-repair-once")
	if first.Code != http.StatusOK || replay.Code != http.StatusOK || first.Body.String() != replay.Body.String() {
		t.Fatalf("first=%d %s replay=%d %s", first.Code, first.Body.String(), replay.Code, replay.Body.String())
	}
	stored, found, readErr := store.GetRuntimeOperation(context.Background(), classification.OperationID)
	operation, decodeErr := decodeWorkspaceLaunchReconcileOperation(stored)
	audits, auditErr := store.ListAuditEvents(context.Background(), classification.AccountID)
	if !found || readErr != nil || decodeErr != nil || operation.Version != 6 || operation.Status != "manual_review" || operation.Stage != "debit" || len(audits) != 1 || auditErr != nil {
		t.Fatalf("operation=%#v audits=%#v errors=%v/%v/%v", operation, audits, readErr, decodeErr, auditErr)
	}
}
