package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type workspaceRenewalAPIFixture struct {
	server    http.Handler
	app       *controlPlaneServer
	owner     *httptest.ResponseRecorder
	workspace map[string]any
}

func newWorkspaceRenewalAPIFixture(t *testing.T) workspaceRenewalAPIFixture {
	return newWorkspaceRenewalAPIFixtureWithStore(t, nil)
}

func newWorkspaceRenewalAPIFixtureWithStore(t *testing.T, store StateStore) workspaceRenewalAPIFixture {
	t.Helper()
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.(*controlPlaneHTTPHandler)
	owner := tenantOwnerSessionForTest(t, server)
	ownerID := sessionUserIDForTest(t, server, owner)
	workspace := canonicalWorkspaceRenewalRow(false)
	workspace["id"], workspace["accountId"], workspace["ownerAccountId"], workspace["ownerUserId"] = "workspace-renewal-api", "acct-alpha", "acct-alpha", ownerID
	workspace["state"], workspace["status"] = "running", "running"
	mustStore(t, handler.app.tables.SaveWorkspace(context.Background(), workspace))
	return workspaceRenewalAPIFixture{server: server, app: handler.app, owner: owner, workspace: workspace}
}

func (f workspaceRenewalAPIFixture) request(t *testing.T, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	return requestWithMutationKeyForTest(t, f.server, f.owner, http.MethodPost, "/api/workspaces/"+stringValue(f.workspace["id"])+"/auto-renew", body, key)
}

func decodeWorkspaceAutoRenewResponse(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("auto-renew status=%d body=%s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"autoRenew", "effectiveAfter", "nextRenewalAt", "paidThrough", "renewalStatus"}
	if len(body) != len(wantKeys) {
		t.Fatalf("auto-renew response=%#v, want exactly %v", body, wantKeys)
	}
	for _, key := range wantKeys {
		if _, ok := body[key]; !ok {
			t.Fatalf("auto-renew response missing %s: %#v", key, body)
		}
	}
	return body
}

func attachWorkspaceRenewalIntentAuditForTest(intent *workspaceRenewalIntentCAS, current map[string]any) {
	before := workspaceRenewalIntentState(current["autoRenew"] == true, stringValue(current["authorizedBy"]), stringValue(current["authorizedAt"]))
	after := workspaceRenewalIntentState(intent.WorkspacePatch.AutoRenew, intent.WorkspacePatch.AuthorizedBy, intent.WorkspacePatch.AuthorizedAt)
	event := map[string]any{
		"actorUserId": intent.OwnerUserID, "actorRole": "owner", "actorAccountId": intent.AccountID, "targetAccountId": intent.AccountID,
		"action": "workspace.auto_renew", "resourceKind": "workspace", "resourceId": intent.WorkspaceID,
		"ipAddress": "192.0.2.1", "userAgent": "workspace-renewal-test", "before": before, "after": after, "result": "succeeded",
	}
	intent.AuditEvent = bindWorkspaceAutoRenewAudit(intent.CommandOperation, event)
}

func enableWorkspaceAutoRenewForTest(t *testing.T, fixture workspaceRenewalAPIFixture) map[string]any {
	t.Helper()
	workspace, ok := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	if !ok {
		t.Fatal("workspace missing")
	}
	operations, err := fixture.app.tables.ListRuntimeOperations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ownerID := sessionUserIDForTest(t, fixture.server, fixture.owner)
	update, _, err := planWorkspaceRenewalIntent(workspace, map[string]any{"id": ownerID, "role": "owner"}, operations, true, "internal-test-enable", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	attachWorkspaceRenewalIntentAuditForTest(&update, workspace)
	mustStore(t, fixture.app.tables.ApplyWorkspaceRenewalIntent(context.Background(), update))
	workspace, _ = fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	return workspace
}

func TestWorkspaceAutoRenewRejectsEnableWithoutMutation(t *testing.T) {
	fixture := newWorkspaceRenewalAPIFixture(t)
	workspaceID := stringValue(fixture.workspace["id"])
	before, _ := fixture.app.getWorkspace(workspaceID)
	operationsBefore, _ := fixture.app.tables.ListRuntimeOperations(context.Background())
	auditsBefore, _ := fixture.app.tables.ListAuditEvents(context.Background(), stringValue(fixture.workspace["accountId"]))

	response := fixture.request(t, `{"autoRenew":true}`, "workspace-renewal-enable-forbidden")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "autoRenew_unavailable") {
		t.Fatalf("enable status=%d body=%s", response.Code, response.Body.String())
	}
	after, _ := fixture.app.getWorkspace(workspaceID)
	operationsAfter, _ := fixture.app.tables.ListRuntimeOperations(context.Background())
	auditsAfter, _ := fixture.app.tables.ListAuditEvents(context.Background(), stringValue(fixture.workspace["accountId"]))
	if string(mustJSON(before)) != string(mustJSON(after)) || len(operationsAfter) != len(operationsBefore) || len(auditsAfter) != len(auditsBefore) {
		t.Fatalf("rejected enable mutated state: before=%#v after=%#v operations=%d/%d audits=%d/%d", before, after, len(operationsBefore), len(operationsAfter), len(auditsBefore), len(auditsAfter))
	}
}

func TestWorkspaceAutoRenewAllowsDisable(t *testing.T) {
	fixture := newWorkspaceRenewalAPIFixture(t)
	body := decodeWorkspaceAutoRenewResponse(t, fixture.request(t, `{"autoRenew":false}`, "workspace-renewal-disable"))
	stored, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	if body["autoRenew"] != false || stored["autoRenew"] != false {
		t.Fatalf("disable response=%#v stored=%#v", body, stored)
	}
}

func TestWorkspaceAutoRenewAuditIsBoundToOriginalCommandAndRequest(t *testing.T) {
	for _, storeCase := range []struct {
		name string
		new  func(*testing.T) StateStore
	}{
		{name: "memory", new: func(*testing.T) StateStore { return nil }},
		{name: "postgres", new: func(t *testing.T) StateStore { return newPostgresWorkspaceRenewalStore(t).(StateStore) }},
	} {
		t.Run(storeCase.name, func(t *testing.T) {
			fixture := newWorkspaceRenewalAPIFixtureWithStore(t, storeCase.new(t))
			seeded := enableWorkspaceAutoRenewForTest(t, fixture)
			workspaceID := stringValue(fixture.workspace["id"])
			path := "/api/workspaces/" + workspaceID + "/auto-renew"
			request := func(remoteAddr, userAgent string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{"autoRenew":false}`))
				req.RemoteAddr = remoteAddr
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Idempotency-Key", "stable-request-audit")
				req.Header.Set("User-Agent", userAgent)
				addAuth(req, fixture.owner)
				rec := httptest.NewRecorder()
				fixture.server.ServeHTTP(rec, req)
				return rec
			}
			decodeWorkspaceAutoRenewResponse(t, request("198.51.100.23:4321", "opl-original-client/1.0"))

			commandID := workspaceAutoRenewCommandID(workspaceID, "stable-request-audit")
			operations, err := fixture.app.tables.ListRuntimeOperations(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			command := recordByID(operations, commandID)
			if command == nil {
				t.Fatalf("auto-renew command %s missing: %#v", commandID, operations)
			}
			audits, err := fixture.app.tables.ListAuditEvents(context.Background(), stringValue(fixture.workspace["accountId"]))
			if err != nil {
				t.Fatal(err)
			}
			auditID := workspaceAutoRenewAuditID(commandID)
			audit := recordByID(audits, auditID)
			if audit == nil {
				t.Fatalf("deterministic audit %s missing: %#v", auditID, audits)
			}
			ownerID := sessionUserIDForTest(t, fixture.server, fixture.owner)
			if audit["createdAt"] != command["createdAt"] || audit["actorUserId"] != ownerID || audit["actorRole"] != "owner" ||
				audit["actorAccountId"] != fixture.workspace["accountId"] || audit["ipAddress"] != "198.51.100.23" || audit["userAgent"] != "opl-original-client/1.0" {
				t.Fatalf("audit request identity=%#v command=%#v", audit, command)
			}
			wantBefore := map[string]any{"autoRenew": true, "authorizedBy": ownerID, "authorizedAt": seeded["authorizedAt"]}
			wantAfter := map[string]any{"autoRenew": false, "authorizedBy": "", "authorizedAt": ""}
			if string(mustJSON(mapField(audit, "before"))) != string(mustJSON(wantBefore)) || string(mustJSON(mapField(audit, "after"))) != string(mustJSON(wantAfter)) {
				t.Fatalf("audit intent snapshots before=%#v after=%#v", audit["before"], audit["after"])
			}

			originalAudit := string(mustJSON(audit))
			decodeWorkspaceAutoRenewResponse(t, request("203.0.113.45:9876", "opl-replay-client/2.0"))
			audits, err = fixture.app.tables.ListAuditEvents(context.Background(), stringValue(fixture.workspace["accountId"]))
			if err != nil {
				t.Fatal(err)
			}
			if got := recordByID(audits, auditID); got == nil || string(mustJSON(got)) != originalAudit {
				t.Fatalf("replay changed original audit: before=%s after=%#v", originalAudit, got)
			}
		})
	}
}

func TestWorkspaceAutoRenewRequiresExplicitBooleanOwnerCSRFAndMutationKey(t *testing.T) {
	fixture := newWorkspaceRenewalAPIFixture(t)
	path := "/api/workspaces/" + stringValue(fixture.workspace["id"]) + "/auto-renew"
	for _, body := range []string{`{}`, `{"autoRenew":null}`, `{"autoRenew":1}`, `{"autoRenew":"true"}`} {
		response := fixture.request(t, body, "workspace-renewal-invalid")
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "autoRenew_required") {
			t.Fatalf("body=%s status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
	missingKey := fixture.request(t, `{"autoRenew":true}`, "")
	if missingKey.Code != http.StatusBadRequest || !strings.Contains(missingKey.Body.String(), "missing Idempotency-Key") {
		t.Fatalf("missing key status=%d body=%s", missingKey.Code, missingKey.Body.String())
	}

	withoutCSRF := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{"autoRenew":true}`))
	withoutCSRF.Header.Set("Content-Type", "application/json")
	withoutCSRF.Header.Set("Idempotency-Key", "workspace-renewal-no-csrf")
	addSessionCookies(withoutCSRF, fixture.owner)
	withoutCSRFResponse := httptest.NewRecorder()
	fixture.server.ServeHTTP(withoutCSRFResponse, withoutCSRF)
	if withoutCSRFResponse.Code != http.StatusForbidden || !strings.Contains(withoutCSRFResponse.Body.String(), "csrf_token_invalid") {
		t.Fatalf("missing CSRF status=%d body=%s", withoutCSRFResponse.Code, withoutCSRFResponse.Body.String())
	}

}

func TestWorkspaceAutoRenewDeniesCrossTenantWorkspace(t *testing.T) {
	fixture := newWorkspaceRenewalAPIFixture(t)
	other := cloneMap(fixture.workspace)
	other["id"], other["accountId"] = "workspace-other-tenant", "acct-other"
	mustStore(t, fixture.app.tables.SaveWorkspace(context.Background(), other))
	response := requestWithMutationKeyForTest(t, fixture.server, fixture.owner, http.MethodPost, "/api/workspaces/workspace-other-tenant/auto-renew", `{"autoRenew":false}`, "workspace-renewal-cross-tenant")
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "account_scope_forbidden") {
		t.Fatalf("cross-tenant status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWorkspaceAutoRenewDisableBeforeAndAfterClaim(t *testing.T) {
	t.Run("before claim cancels current renewal", func(t *testing.T) {
		fixture := newWorkspaceRenewalAPIFixture(t)
		enableWorkspaceAutoRenewForTest(t, fixture)
		body := decodeWorkspaceAutoRenewResponse(t, fixture.request(t, `{"autoRenew":false}`, "workspace-renewal-disable-before-claim"))
		if body["autoRenew"] != false || body["renewalStatus"] != "cancelled" || body["effectiveAfter"] != body["paidThrough"] {
			t.Fatalf("before-claim disable=%#v", body)
		}
	})

	t.Run("after claim applies to following period", func(t *testing.T) {
		fixture := newWorkspaceRenewalAPIFixture(t)
		enableWorkspaceAutoRenewForTest(t, fixture)
		workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
		paidThrough, err := time.Parse(time.RFC3339, stringValue(workspace["paidThrough"]))
		if err != nil {
			t.Fatal(err)
		}
		operationID := "workspace-renewal-" + stableID(stringValue(workspace["id"]), paidThrough.Format(time.RFC3339))[:18]
		mustStore(t, fixture.app.tables.SaveRuntimeOperation(context.Background(), map[string]any{
			"id": operationID, "operationId": operationID, "accountId": workspace["accountId"], "workspaceId": workspace["id"],
			"resourceId": workspace["id"], "resourceKind": "workspace_renewal", "action": "workspace.renewal", "status": "claimed",
			"result": `{"requestHash":"test","phase":"claimed","paidThrough":"` + paidThrough.Format(time.RFC3339Nano) + `"}`,
		}))
		body := decodeWorkspaceAutoRenewResponse(t, fixture.request(t, `{"autoRenew":false}`, "workspace-renewal-disable-after-claim"))
		anchor := int(numberField(workspace, "billingAnchorDay", float64(paidThrough.Day())))
		wantEffective := nextBillingMonth(paidThrough, anchor).UTC().Format(time.RFC3339Nano)
		if body["autoRenew"] != false || body["renewalStatus"] != "claimed" || body["effectiveAfter"] != wantEffective {
			t.Fatalf("after-claim disable=%#v want effectiveAfter=%s", body, wantEffective)
		}
	})
}

type workspaceRenewalWorkerFixture struct {
	app            *controlPlaneServer
	service        *controlplane.Service
	sub2API        *monthlySub2API
	fabric         *monthlyFabric
	ledger         *monthlyLedger
	events         *[]string
	workspace      map[string]any
	compute        map[string]any
	storage        map[string]any
	paidThrough    time.Time
	renewedThrough time.Time
}

func newWorkspaceRenewalWorkerFixture(t *testing.T, balances []int64) workspaceRenewalWorkerFixture {
	t.Helper()
	app, service, sub2API, fabric, ledger, events := newMonthlyBillingTest(t, balances)
	paidThrough := time.Date(2026, 8, 31, 9, 30, 0, 0, time.UTC)
	renewedThrough := nextBillingMonth(paidThrough, paidThrough.Day())
	ownerID, workspaceID := "usr-monthly-owner", "workspace-monthly"
	computeID, storageID := "compute-workspace-monthly", "storage-workspace-monthly"
	compute := monthlyActiveResource("compute", computeID, paidThrough)
	storage := monthlyActiveResource("storage", storageID, paidThrough)
	for _, row := range []map[string]any{compute, storage} {
		row["workspaceId"], row["ownerUserId"], row["autoRenew"] = workspaceID, ownerID, false
		if !monthlyPriceSnapshotAvailable(row) {
			t.Fatalf("test child price snapshot invalid: %#v", row)
		}
	}
	storage["computeAllocationId"] = computeID
	mustStore(t, app.tables.SaveCompute(context.Background(), compute))
	mustStore(t, app.tables.SaveStorage(context.Background(), storage))
	billing, code := workspaceBillingStateFromChildren(compute, storage, workspaceBillingChildIdentity{
		AccountID: "acct-monthly", OwnerUserID: ownerID, WorkspaceID: workspaceID, PackageID: "basic",
		ComputeID: computeID, StorageID: storageID, StorageGB: 10,
	})
	if code != "" {
		t.Fatalf("test Workspace billing state: %s", code)
	}
	billing["autoRenew"], billing["authorizedBy"], billing["authorizedAt"] = true, ownerID, paidThrough.AddDate(0, -1, 0).Format(time.RFC3339Nano)
	workspace := mergeMaps(map[string]any{
		"id": workspaceID, "accountId": "acct-monthly", "ownerAccountId": "acct-monthly", "ownerUserId": ownerID,
		"state": "running", "status": "running", "currentComputeAllocationId": computeID, "storageId": storageID,
		"currentAttachmentId": "attachment-workspace-monthly", "attachmentId": "attachment-workspace-monthly",
	}, billing)
	mustStore(t, app.tables.SaveWorkspace(context.Background(), workspace))
	fabric.computeRenew = clients.ComputeAllocation{
		ID: computeID, AccountID: "acct-monthly", WorkspaceID: workspaceID, PackageID: "basic", Status: "running",
		Provider: "tencent-tke", ProviderResourceID: stringValue(compute["providerResourceId"]), ProviderRequestID: "renew-compute-workspace",
		InstanceID: stringValue(compute["providerResourceId"]), CVMInstanceID: stringValue(compute["providerResourceId"]), InstanceType: "S5.MEDIUM4",
		Zone: "ap-shanghai-2", ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: renewedThrough.Format(time.RFC3339),
		ProviderData: map[string]string{"chargeType": "PREPAID", "renewalResult": "renewed", "zone": "ap-shanghai-2", "instanceType": "S5.MEDIUM4", "deadline": renewedThrough.Format(time.RFC3339)},
	}
	fabric.storageRenew = clients.StorageVolume{
		ID: storageID, AccountID: "acct-monthly", WorkspaceID: workspaceID, Status: "available", Provider: "tencent-tke",
		ProviderResourceID: stringValue(storage["providerResourceId"]), ProviderRequestID: "renew-storage-workspace", SizeGB: 10,
		CBSStatus: "UNATTACHED", DiskType: "CLOUD_PREMIUM", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: renewedThrough.Format(time.RFC3339), Zone: "ap-shanghai-2",
		ProviderData: map[string]string{"chargeType": "PREPAID", "renewalResult": "renewed", "zone": "ap-shanghai-2", "deadline": renewedThrough.Format(time.RFC3339)},
	}
	fabric.computeSync, fabric.storageSync = fabric.computeRenew, fabric.storageRenew
	return workspaceRenewalWorkerFixture{
		app: app, service: service, sub2API: sub2API, fabric: fabric, ledger: ledger, events: events,
		workspace: workspace, compute: compute, storage: storage, paidThrough: paidThrough, renewedThrough: renewedThrough,
	}
}

func (f workspaceRenewalWorkerFixture) operation(t *testing.T) map[string]any {
	t.Helper()
	operations, err := f.app.tables.ListRuntimeOperations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range operations {
		if stringValue(operation["action"]) == "workspace.renewal" && stringValue(operation["workspaceId"]) == stringValue(f.workspace["id"]) {
			return operation
		}
	}
	t.Fatal("Workspace renewal operation not found")
	return nil
}

func workspaceRenewalReviewRequest(t *testing.T, server http.Handler, session *httptest.ResponseRecorder, operationID, key string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"accountId":"acct-monthly","billingOperationId":"` + operationID + `","decision":"activate_charged_resource","evidenceRef":"case-20260717-workspace"}`
	return requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/operator/billing-reviews/workspace/workspace-monthly/resolve", body, key)
}

func TestWorkspaceRenewalReviewResolutionRejectsOperationsWithoutPendingReview(t *testing.T) {
	for _, operationCase := range []struct {
		status    string
		phase     string
		receiptID string
	}{
		{status: "active", phase: "complete", receiptID: "receipt-existing"},
		{status: "verifying", phase: "receipt"},
	} {
		t.Run(operationCase.status, func(t *testing.T) {
			fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
			operation, err := newWorkspaceRenewalOperation(fixture.workspace, fixture.paidThrough.Add(-monthlyRenewalLead))
			if err != nil {
				t.Fatal(err)
			}
			operation.Status, operation.Phase, operation.ReceiptID = operationCase.status, operationCase.phase, operationCase.receiptID
			operation.EntitlementCommitted = operationCase.status == "verifying"
			operation.PreChargeBalanceUSDMicros, operation.PostChargeBalanceUSDMicros, operation.PostChargeBalanceKnown = 100_000_000, 47_420_000, true
			operation.ChargeConfirmation = map[string]any{
				"code": operation.RedeemCode, "userId": int64(41), "chargeUsdMicros": operation.TotalUSDMicros, "status": "used",
			}
			mustStore(t, fixture.app.tables.SaveRuntimeOperation(context.Background(), workspaceRenewalOperationRow(operation)))

			before := fixture.operation(t)
			beforeResult, beforeStatus := stringValue(before["result"]), stringValue(before["status"])
			beforeEvents, beforeReceipts := len(*fixture.events), len(fixture.ledger.receipts)
			result, resolveErr := fixture.app.resolveWorkspaceRenewalReview(context.Background(), fixture.service, billingReviewResolutionInput{
				ResourceType: "workspace", ResourceID: operation.WorkspaceID, AccountID: operation.AccountID, BillingOperationID: operation.ID,
				Decision: billingReviewActivateCharged, EvidenceRef: "case-without-pending-review", IdempotencyKey: "resolution-without-pending-review-" + operationCase.status, Reviewer: "operator-reviewer",
			})
			after := fixture.operation(t)
			if !errors.Is(resolveErr, errBillingReviewNotPending) || result != nil {
				t.Fatalf("resolution status=%s result=%#v err=%v, want %v", operationCase.status, result, resolveErr, errBillingReviewNotPending)
			}
			if stringValue(after["result"]) != beforeResult || stringValue(after["status"]) != beforeStatus {
				t.Fatalf("rejected resolution changed operation: before=%#v after=%#v", before, after)
			}
			if len(*fixture.events) != beforeEvents || len(fixture.ledger.receipts) != beforeReceipts {
				t.Fatalf("rejected resolution called Fabric/Ledger: events=%#v receipts=%#v", (*fixture.events)[beforeEvents:], fixture.ledger.receipts[beforeReceipts:])
			}
		})
	}
}

func TestWorkspaceRenewalReviewResolutionRequiresBothProviderFactsAndResumes(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	fixture.fabric.storageRenewErr = errors.New("provider response lost")
	fixture.fabric.storageSync = clients.StorageVolume{
		ID: stringValue(fixture.storage["id"]), AccountID: "acct-monthly", WorkspaceID: "workspace-monthly",
		Status: "external_deleted", CBSStatus: "NOT_FOUND",
	}
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(-monthlyRenewalLead)); err != nil {
		t.Fatal(err)
	}
	operationID := stringValue(fixture.operation(t)["id"])
	server, err := NewPersistentServer(fixture.service, fixture.app.tables)
	if err != nil {
		t.Fatal(err)
	}
	key := "workspace-review-provider-resume"
	first := workspaceRenewalReviewRequest(t, server, reservedOperatorSessionForTest(t, server), operationID, key)
	if first.Code != http.StatusConflict || !strings.Contains(first.Body.String(), `"error":"billing_review_provider_fact_unconfirmed"`) {
		t.Fatalf("partial resolution status=%d body=%s", first.Code, first.Body.String())
	}
	partial, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil {
		t.Fatal(err)
	}
	if partial.Status != "manual_review" || !strings.Contains(partial.PersistedResult, `"reviewResolutionPhase":"verify_storage"`) || len(fixture.sub2API.refunds) != 0 || len(fixture.ledger.receipts) != 0 {
		t.Fatalf("partial operation=%#v refunds=%#v receipts=%#v", partial, fixture.sub2API.refunds, fixture.ledger.receipts)
	}
	computeSyncs := strings.Count(strings.Join(*fixture.events, ","), "fabric.compute.sync")

	fixture.fabric.storageSync = fixture.fabric.storageRenew
	restarted, err := NewPersistentServer(fixture.service, fixture.app.tables)
	if err != nil {
		t.Fatal(err)
	}
	second := workspaceRenewalReviewRequest(t, restarted, reservedOperatorSessionForTest(t, restarted), operationID, key)
	if second.Code != http.StatusOK {
		t.Fatalf("resumed resolution status=%d body=%s", second.Code, second.Body.String())
	}
	workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	resolved, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != "active" || !strings.Contains(resolved.PersistedResult, `"reviewResolutionPhase":"completed"`) || workspace["paidThrough"] != fixture.renewedThrough.Format(time.RFC3339Nano) ||
		strings.Count(strings.Join(*fixture.events, ","), "fabric.compute.sync") != computeSyncs+1 || len(fixture.sub2API.refunds) != 0 || len(fixture.ledger.receipts) != 1 {
		t.Fatalf("resolved operation=%#v workspace=%#v events=%#v refunds=%#v receipts=%#v", resolved, workspace, *fixture.events, fixture.sub2API.refunds, fixture.ledger.receipts)
	}
	receipt := fixture.ledger.receipts[0]
	if receipt.InputRefs["evidenceRef"] != "case-20260717-workspace" || receipt.ReviewerChecks["decision"] != billingReviewActivateCharged ||
		receipt.ReviewerChecks["reviewer"] != resolved.ReviewResolutionReviewer || receipt.ReviewerChecks["evidenceRef"] != resolved.ReviewResolutionEvidenceRef ||
		receipt.ReviewerChecks["resolvedAt"] != resolved.ReviewResolutionResolvedAt {
		t.Fatalf("review evidence inputRefs=%#v reviewerChecks=%#v operation=%#v", receipt.InputRefs, receipt.ReviewerChecks, resolved)
	}
	beforeEvents, beforeReceipts := len(*fixture.events), len(fixture.ledger.receipts)
	replay := workspaceRenewalReviewRequest(t, restarted, reservedOperatorSessionForTest(t, restarted), operationID, key)
	if replay.Code != http.StatusOK || replay.Body.String() != second.Body.String() || len(*fixture.events) != beforeEvents || len(fixture.ledger.receipts) != beforeReceipts {
		t.Fatalf("resolution replay status=%d body=%s events=%#v receipts=%#v", replay.Code, replay.Body.String(), *fixture.events, fixture.ledger.receipts)
	}
}

func TestWorkspaceRenewalReviewResolutionReceiptFailureRetriesReceiptOnly(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	fixture.fabric.computeRenewErr = errors.New("provider response lost")
	fixture.fabric.computeSync = clients.ComputeAllocation{ID: stringValue(fixture.compute["id"]), AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "renewing"}
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(-monthlyRenewalLead)); err != nil {
		t.Fatal(err)
	}
	fixture.fabric.computeSync = fixture.fabric.computeRenew
	fixture.ledger.receiptErrors = []error{errors.New("ledger unavailable"), nil}
	operationID := stringValue(fixture.operation(t)["id"])
	server, err := NewPersistentServer(fixture.service, fixture.app.tables)
	if err != nil {
		t.Fatal(err)
	}
	key := "workspace-review-receipt-retry"
	first := workspaceRenewalReviewRequest(t, server, reservedOperatorSessionForTest(t, server), operationID, key)
	if first.Code != http.StatusBadGateway || !strings.Contains(first.Body.String(), `"error":"billing_review_receipt_pending"`) {
		t.Fatalf("receipt failure status=%d body=%s", first.Code, first.Body.String())
	}
	pending, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != "verifying" || pending.Phase != "receipt" || !strings.Contains(pending.PersistedResult, `"reviewResolutionPhase":"receipt"`) {
		t.Fatalf("receipt pending operation=%#v", pending)
	}
	before := append([]string(nil), (*fixture.events)...)
	restarted, err := NewPersistentServer(fixture.service, fixture.app.tables)
	if err != nil {
		t.Fatal(err)
	}
	second := workspaceRenewalReviewRequest(t, restarted, reservedOperatorSessionForTest(t, restarted), operationID, key)
	if second.Code != http.StatusOK {
		t.Fatalf("receipt retry status=%d body=%s", second.Code, second.Body.String())
	}
	if got := (*fixture.events)[len(before):]; len(got) != 1 || got[0] != "ledger.receipt" || len(fixture.ledger.receipts) != 2 || len(fixture.sub2API.refunds) != 0 {
		t.Fatalf("receipt retry events=%#v receipts=%#v refunds=%#v", got, fixture.ledger.receipts, fixture.sub2API.refunds)
	}
}

func TestWorkspaceRenewalUsesOneDebitStableProviderIDsAndOneReceipt(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	now := fixture.paidThrough.Add(-monthlyRenewalLead)
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); err != nil {
		t.Fatal(err)
	}
	if len(fixture.sub2API.charges) != 1 || fixture.sub2API.charges[0].ChargeUSDMicros != 52_580_000 {
		t.Fatalf("combined Workspace debit=%#v", fixture.sub2API.charges)
	}
	operation := fixture.operation(t)
	wantPrefix := stringValue(operation["id"])
	if len(fixture.fabric.computeRenewKeys) != 1 || fixture.fabric.computeRenewKeys[0] != wantPrefix+":compute" ||
		len(fixture.fabric.storageRenewKeys) != 1 || fixture.fabric.storageRenewKeys[0] != wantPrefix+":storage" {
		t.Fatalf("provider renewal keys compute=%#v storage=%#v operation=%#v", fixture.fabric.computeRenewKeys, fixture.fabric.storageRenewKeys, operation)
	}
	workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	if workspace["paidThrough"] != fixture.renewedThrough.Format(time.RFC3339Nano) || workspace["computeAllocationId"] != fixture.compute["id"] || workspace["storageId"] != fixture.storage["id"] {
		t.Fatalf("renewed Workspace=%#v", workspace)
	}
	compute, _ := fixture.app.getCompute(stringValue(fixture.compute["id"]))
	storage, _ := fixture.app.getStorage(stringValue(fixture.storage["id"]))
	if compute["providerResourceId"] != fixture.compute["providerResourceId"] || storage["providerResourceId"] != fixture.storage["providerResourceId"] ||
		compute["deadline"] != fixture.renewedThrough.Format(time.RFC3339) || storage["deadline"] != fixture.renewedThrough.Format(time.RFC3339) ||
		strings.Count(strings.Join(*fixture.events, ","), "fabric.compute.sync") != 2 || strings.Count(strings.Join(*fixture.events, ","), "fabric.storage.sync") != 2 {
		t.Fatalf("provider readback compute=%#v storage=%#v events=%#v", compute, storage, *fixture.events)
	}
	if len(fixture.ledger.receipts) != 1 || fixture.ledger.receipts[0].Type != "billing.workspace_renewed.v1" || fixture.ledger.receipts[0].Cost["totalUsdMicros"] != int64(52_580_000) {
		t.Fatalf("Workspace renewal receipts=%#v", fixture.ledger.receipts)
	}
	components := mapField(fixture.ledger.receipts[0].Cost, "components")
	if len(fixture.ledger.receipts[0].Cost) != 12 || mapField(components, "compute")["chargeUsdMicros"] != int64(50_000_000) || mapField(components, "storage")["chargeUsdMicros"] != int64(2_580_000) {
		t.Fatalf("Workspace renewal component snapshot=%#v", fixture.ledger.receipts[0].Cost)
	}
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); err != nil {
		t.Fatal(err)
	}
	if len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeRenewKeys) != 1 || len(fixture.fabric.storageRenewKeys) != 1 || len(fixture.ledger.receipts) != 1 {
		t.Fatalf("replayed completed renewal side effects charges=%d compute=%d storage=%d receipts=%d", len(fixture.sub2API.charges), len(fixture.fabric.computeRenewKeys), len(fixture.fabric.storageRenewKeys), len(fixture.ledger.receipts))
	}
}

func TestWorkspaceRenewalOriginalResourcesWithoutChildBilling(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	compute, _ := fixture.app.getCompute(stringValue(fixture.compute["id"]))
	storage, _ := fixture.app.getStorage(stringValue(fixture.storage["id"]))
	stripWorkspaceLaunchResourceBilling(compute)
	stripWorkspaceLaunchResourceBilling(storage)
	mustStore(t, fixture.app.tables.SaveCompute(context.Background(), compute))
	mustStore(t, fixture.app.tables.SaveStorage(context.Background(), storage))

	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(-monthlyRenewalLead)); err != nil {
		t.Fatal(err)
	}
	operation := fixture.operation(t)
	renewedCompute, _ := fixture.app.getCompute(stringValue(fixture.compute["id"]))
	renewedStorage, _ := fixture.app.getStorage(stringValue(fixture.storage["id"]))
	if operation["status"] != "active" || renewedCompute["providerResourceId"] != fixture.compute["providerResourceId"] || renewedStorage["providerResourceId"] != fixture.storage["providerResourceId"] {
		t.Fatalf("pure fulfillment renewal operation=%#v compute=%#v storage=%#v", operation, renewedCompute, renewedStorage)
	}
	for _, row := range []map[string]any{renewedCompute, renewedStorage} {
		for _, forbidden := range []string{"billingOperationId", "billingStatus", "sub2apiRedeemCode", "chargeUsdMicros", "priceVersion", "periodStart", "paidThrough"} {
			if _, ok := row[forbidden]; ok {
				t.Fatalf("renewal restored child billing field %s: %#v", forbidden, row)
			}
		}
	}
	if len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeRenewKeys) != 1 || len(fixture.fabric.storageRenewKeys) != 1 ||
		len(fixture.ledger.receipts) != 1 || fixture.ledger.receipts[0].Type != "billing.workspace_renewed.v1" {
		t.Fatalf("pure fulfillment renewal effects charges=%#v compute=%#v storage=%#v receipts=%#v", fixture.sub2API.charges, fixture.fabric.computeRenewKeys, fixture.fabric.storageRenewKeys, fixture.ledger.receipts)
	}
}

func TestWorkspaceRenewalConcurrentWorkersClaimOnce(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	second, err := newControlPlaneAppWithStore(fixture.app.tables)
	if err != nil {
		t.Fatal(err)
	}
	now := fixture.paidThrough.Add(-monthlyRenewalLead)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, app := range []*controlPlaneServer{fixture.app, second} {
		wg.Add(1)
		go func(app *controlPlaneServer) {
			defer wg.Done()
			errs <- app.runMonthlyBillingOnce(context.Background(), fixture.service, now)
		}(app)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeRenewKeys) != 1 || len(fixture.fabric.storageRenewKeys) != 1 || len(fixture.ledger.receipts) != 1 {
		t.Fatalf("concurrent effects charges=%d compute=%d storage=%d receipts=%d", len(fixture.sub2API.charges), len(fixture.fabric.computeRenewKeys), len(fixture.fabric.storageRenewKeys), len(fixture.ledger.receipts))
	}
}

type disableAutoRenewBeforeEntitlementStore struct {
	*memoryTableStore
	now      time.Time
	once     sync.Once
	disabled bool
	err      error
}

type mutateWorkspaceBeforeRecoveryPersistStore struct {
	StateStore
	once   sync.Once
	mutate func(context.Context) error
	err    error
}

func (s *mutateWorkspaceBeforeRecoveryPersistStore) PersistWorkspaceRenewal(ctx context.Context, update workspaceRenewalPersistCAS) error {
	if update.WorkspacePatch != nil && stringValue(update.WorkspacePatch["state"]) == "running" {
		s.once.Do(func() { s.err = s.mutate(ctx) })
		if s.err != nil {
			return s.err
		}
	}
	return s.StateStore.PersistWorkspaceRenewal(ctx, update)
}

func TestWorkspaceRenewalRecoveryRejectsConcurrentSafetyStateChanges(t *testing.T) {
	for _, storeCase := range []struct {
		name string
		new  func(*testing.T) StateStore
	}{
		{name: "memory", new: func(*testing.T) StateStore { return newMemoryTableStore() }},
		{name: "postgres", new: func(t *testing.T) StateStore {
			store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
			return store
		}},
	} {
		for _, mutationCase := range []struct {
			name   string
			mutate func(map[string]any)
			assert func(*testing.T, map[string]any)
		}{
			{
				name: "storage destroyed",
				mutate: func(workspace map[string]any) {
					workspace["state"], workspace["status"] = "data_deleted", "unrecoverable"
					workspace["currentComputeAllocationId"], workspace["currentAttachmentId"], workspace["attachmentId"] = "", "", ""
					workspace["autoRenew"], workspace["authorizedBy"], workspace["authorizedAt"] = false, "", ""
				},
				assert: func(t *testing.T, workspace map[string]any) {
					t.Helper()
					if workspace["state"] != "data_deleted" || workspace["status"] != "unrecoverable" || stringValue(workspace["currentComputeAllocationId"]) != "" || stringValue(workspace["currentAttachmentId"]) != "" {
						t.Fatalf("storage-destroyed Workspace was overwritten: %#v", workspace)
					}
				},
			},
			{
				name: "compute cleared",
				mutate: func(workspace map[string]any) {
					workspace["state"], workspace["status"], workspace["currentComputeAllocationId"] = "suspended", "suspended", ""
					workspace["autoRenew"], workspace["authorizedBy"], workspace["authorizedAt"] = false, "", ""
				},
				assert: func(t *testing.T, workspace map[string]any) {
					t.Helper()
					if workspace["state"] != "suspended" || workspace["status"] != "suspended" || stringValue(workspace["currentComputeAllocationId"]) != "" {
						t.Fatalf("compute-suspended Workspace was overwritten: %#v", workspace)
					}
				},
			},
		} {
			t.Run(storeCase.name+"/"+mutationCase.name, func(t *testing.T) {
				ctx := context.Background()
				store := storeCase.new(t)
				workspace := canonicalWorkspaceRenewalRow(true)
				workspace["state"], workspace["status"] = "suspended", "suspended"
				workspace["attachmentId"], workspace["currentAttachmentId"] = "attachment-renewal", "attachment-renewal"
				mustStore(t, store.SaveWorkspace(ctx, workspace))

				operation, err := newWorkspaceRenewalOperation(workspace, time.Date(2026, 8, 17, 1, 3, 0, 0, time.UTC))
				if err != nil {
					t.Fatal(err)
				}
				operation.Status, operation.Phase = "debit_pending", "debit"
				operation.ExpiryStatus, operation.ExpiryPhase, operation.ExpiryPaidThrough = "past_due", "financial", operation.PaidThrough
				operations, err := store.ListRuntimeOperations(ctx)
				if err != nil {
					t.Fatal(err)
				}
				claim := workspaceRenewalClaimCAS{
					WorkspaceID: operation.WorkspaceID, AccountID: operation.AccountID, ExpectedPaidThrough: operation.PaidThrough, ExpectedAutoRenew: true,
					ExpectedOperationsVersion: runtimeOperationsVersion(operations, operation.WorkspaceID), DesiredOperation: workspaceRenewalOperationRow(operation),
				}
				mustStore(t, store.ClaimWorkspaceRenewal(ctx, claim))
				operation.PersistedResult = stringValue(claim.DesiredOperation["result"])

				interleaved := &mutateWorkspaceBeforeRecoveryPersistStore{StateStore: store}
				interleaved.mutate = func(ctx context.Context) error {
					rows, err := store.ListWorkspaces(ctx, operation.AccountID)
					if err != nil {
						return err
					}
					concurrent := cloneMap(recordByID(rows, operation.WorkspaceID))
					mutationCase.mutate(concurrent)
					return store.SaveWorkspace(ctx, concurrent)
				}
				app, err := newControlPlaneAppWithStore(interleaved)
				if err != nil {
					t.Fatal(err)
				}
				if err := app.commitWorkspaceRenewalEntitlement(ctx, &operation); !errors.Is(err, errWorkspaceRenewalCASConflict) {
					t.Fatalf("commit error=%v, want %v", err, errWorkspaceRenewalCASConflict)
				}

				rows, err := store.ListWorkspaces(ctx, operation.AccountID)
				if err != nil {
					t.Fatal(err)
				}
				got := recordByID(rows, operation.WorkspaceID)
				mutationCase.assert(t, got)
				if stringValue(got["paidThrough"]) != operation.PaidThrough {
					t.Fatalf("rejected recovery advanced paidThrough: %#v", got)
				}
				operations, err = store.ListRuntimeOperations(ctx)
				if err != nil {
					t.Fatal(err)
				}
				persisted, err := decodeWorkspaceRenewalOperation(recordByID(operations, operation.ID))
				if err != nil || persisted.EntitlementCommitted || persisted.ExpiryStatus != "past_due" {
					t.Fatalf("rejected recovery changed operation=%#v err=%v", persisted, err)
				}
			})
		}
	}
}

func TestWorkspaceRenewalRecoveryRejectsPreexistingMissingAttachment(t *testing.T) {
	for _, storeCase := range []struct {
		name string
		new  func(*testing.T) StateStore
	}{
		{name: "memory", new: func(*testing.T) StateStore { return newMemoryTableStore() }},
		{name: "postgres", new: func(t *testing.T) StateStore {
			store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
			return store
		}},
	} {
		t.Run(storeCase.name, func(t *testing.T) {
			ctx := context.Background()
			store := storeCase.new(t)
			workspace := canonicalWorkspaceRenewalRow(true)
			workspace["state"], workspace["status"], workspace["currentAttachmentId"] = "suspended", "suspended", ""
			mustStore(t, store.SaveWorkspace(ctx, workspace))

			operation, err := newWorkspaceRenewalOperation(workspace, time.Date(2026, 8, 17, 1, 3, 0, 0, time.UTC))
			if err != nil {
				t.Fatal(err)
			}
			operation.Status, operation.Phase = "debit_pending", "debit"
			operation.ExpiryStatus, operation.ExpiryPhase, operation.ExpiryPaidThrough = "past_due", "financial", operation.PaidThrough
			operations, err := store.ListRuntimeOperations(ctx)
			if err != nil {
				t.Fatal(err)
			}
			claim := workspaceRenewalClaimCAS{
				WorkspaceID: operation.WorkspaceID, AccountID: operation.AccountID, ExpectedPaidThrough: operation.PaidThrough, ExpectedAutoRenew: true,
				ExpectedOperationsVersion: runtimeOperationsVersion(operations, operation.WorkspaceID), DesiredOperation: workspaceRenewalOperationRow(operation),
			}
			mustStore(t, store.ClaimWorkspaceRenewal(ctx, claim))
			operation.PersistedResult = stringValue(claim.DesiredOperation["result"])

			app, err := newControlPlaneAppWithStore(store)
			if err != nil {
				t.Fatal(err)
			}
			if err := app.commitWorkspaceRenewalEntitlement(ctx, &operation); err != nil {
				t.Fatal(err)
			}
			operations, err = store.ListRuntimeOperations(ctx)
			if err != nil {
				t.Fatal(err)
			}
			persisted, err := decodeWorkspaceRenewalOperation(recordByID(operations, operation.ID))
			if err != nil || persisted.Status != "manual_review" || persisted.ErrorCode != "workspace_renewal_recovery_state_mismatch" || persisted.EntitlementCommitted || persisted.ExpiryStatus != "past_due" {
				t.Fatalf("missing attachment recovery operation=%#v err=%v", persisted, err)
			}
			rows, err := store.ListWorkspaces(ctx, operation.AccountID)
			if err != nil {
				t.Fatal(err)
			}
			got := recordByID(rows, operation.WorkspaceID)
			if got["state"] != "suspended" || got["status"] != "suspended" || stringValue(got["currentAttachmentId"]) != "" || stringValue(got["paidThrough"]) != operation.PaidThrough {
				t.Fatalf("missing attachment Workspace was recovered: %#v", got)
			}
		})
	}
}

func (s *disableAutoRenewBeforeEntitlementStore) PersistWorkspaceRenewal(ctx context.Context, update workspaceRenewalPersistCAS) error {
	operation, err := decodeWorkspaceRenewalOperation(update.DesiredOperation)
	if err != nil {
		return err
	}
	if operation.EntitlementCommitted {
		s.once.Do(func() {
			workspaces, listErr := s.ListWorkspaces(ctx, operation.AccountID)
			operations, operationsErr := s.ListRuntimeOperations(ctx)
			users, usersErr := s.ListUsers(ctx, true)
			if listErr != nil || operationsErr != nil || usersErr != nil {
				s.err = errors.Join(listErr, operationsErr, usersErr)
				return
			}
			workspace := recordByID(workspaces, operation.WorkspaceID)
			user := recordByID(users, operation.OwnerUserID)
			intent, _, planErr := planWorkspaceRenewalIntent(workspace, user, operations, false, "disable-after-claim", s.now)
			if planErr != nil {
				s.err = planErr
				return
			}
			attachWorkspaceRenewalIntentAuditForTest(&intent, workspace)
			s.err = s.ApplyWorkspaceRenewalIntent(ctx, intent)
			s.disabled = s.err == nil
		})
		if s.err != nil {
			return s.err
		}
	}
	return s.memoryTableStore.PersistWorkspaceRenewal(ctx, update)
}

func TestWorkspaceRenewalDisableAfterClaimSurvivesEntitlementCommit(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	store := &disableAutoRenewBeforeEntitlementStore{
		memoryTableStore: fixture.app.tables.(*memoryTableStore),
		now:              fixture.paidThrough.Add(-time.Minute),
	}
	app, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	now := fixture.paidThrough.Add(-monthlyRenewalLead)
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); err != nil {
		t.Fatal(err)
	}
	workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	if !store.disabled || workspace["autoRenew"] != false || stringValue(workspace["authorizedBy"]) != "" || stringValue(workspace["authorizedAt"]) != "" || workspace["paidThrough"] != fixture.renewedThrough.Format(time.RFC3339Nano) {
		t.Fatalf("claim-time disable was overwritten: disabled=%v Workspace=%#v", store.disabled, workspace)
	}
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.renewedThrough.Add(-monthlyRenewalLead)); err != nil {
		t.Fatal(err)
	}
	operations, err := fixture.app.tables.ListRuntimeOperations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	renewals := 0
	for _, operation := range operations {
		if stringValue(operation["action"]) == "workspace.renewal" {
			renewals++
		}
	}
	if renewals != 1 || len(fixture.sub2API.charges) != 1 {
		t.Fatalf("disabled next-period intent renewed again: renewals=%d charges=%#v operations=%#v", renewals, fixture.sub2API.charges, operations)
	}
}

func TestWorkspaceRenewalInsufficientBalanceRetriesSamePeriodWithoutProviderCalls(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{40_000_000})
	now := fixture.paidThrough.Add(-monthlyRenewalLead)
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); err != nil {
		t.Fatal(err)
	}
	operation := fixture.operation(t)
	operationID := stringValue(operation["id"])
	if operation["status"] != "insufficient" || len(fixture.sub2API.charges) != 0 || len(fixture.fabric.computeRenewKeys) != 0 || len(fixture.fabric.storageRenewKeys) != 0 {
		t.Fatalf("insufficient operation=%#v charges=%#v compute=%#v storage=%#v", operation, fixture.sub2API.charges, fixture.fabric.computeRenewKeys, fixture.fabric.storageRenewKeys)
	}
	workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	if workspace["paidThrough"] != fixture.paidThrough.Format(time.RFC3339Nano) {
		t.Fatalf("insufficient balance changed entitlement: %#v", workspace)
	}
	fixture.sub2API.balances = []int64{100_000_000, 47_420_000}
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); err != nil {
		t.Fatal(err)
	}
	if recovered := fixture.operation(t); recovered["status"] != "active" || stringValue(recovered["id"]) != operationID || len(fixture.sub2API.charges) != 1 {
		t.Fatalf("same-period retry operation=%#v charges=%#v", recovered, fixture.sub2API.charges)
	}
}

func TestWorkspaceRenewalInvalidPreflightWithoutUpstreamErrorRemainsVisibleAndRetryable(t *testing.T) {
	validCompute := clients.MonthlyPreflight{
		ResourceType: "compute", PackageID: "basic", Zone: "ap-shanghai-2", Available: true,
		ChargeType: "PREPAID", PeriodMonths: 1, RenewFlag: "NOTIFY_AND_MANUAL_RENEW", ProviderPriceCNY: 12.34,
		ProviderRequestIDs: map[string]string{"nodePool": "node-pool-request", "subnets": "subnets-request", "availability": "availability-request"},
	}
	for _, tc := range []struct {
		name, phase, code string
		results           []clients.MonthlyPreflight
	}{
		{name: "compute", phase: "preflight_compute", code: "fabric_compute_preflight_failed", results: []clients.MonthlyPreflight{{}}},
		{name: "storage", phase: "preflight_storage", code: "fabric_storage_preflight_failed", results: []clients.MonthlyPreflight{validCompute, {}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
			fixture.fabric.preflightResults = tc.results
			err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(-monthlyRenewalLead))
			if err == nil {
				t.Fatal("invalid preflight without an upstream error returned nil")
			}
			operation, decodeErr := decodeWorkspaceRenewalOperation(fixture.operation(t))
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			notifications := fixture.app.operatorSummary()["notifications"].(map[string]any)
			if operation.Status != "claimed" || operation.Phase != tc.phase || operation.ErrorCode != tc.code || operation.LeaseToken != "" || operation.LeaseExpiresAt != "" ||
				len(fixture.sub2API.charges) != 0 || len(fixture.fabric.computeRenewKeys) != 0 || len(fixture.fabric.storageRenewKeys) != 0 ||
				notifications["total"] != 1 || notifications["recent"].([]any)[0].(map[string]any)["code"] != "renewal_retry_pending" {
				t.Fatalf("operation=%#v notifications=%#v charges=%#v compute=%#v storage=%#v err=%v", operation, notifications, fixture.sub2API.charges, fixture.fabric.computeRenewKeys, fixture.fabric.storageRenewKeys, err)
			}
		})
	}
}

func TestWorkspaceRenewalReceiptFailureRetriesOnlyReceipt(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	fixture.ledger.receiptErrors = []error{errors.New("ledger unavailable"), nil}
	now := fixture.paidThrough.Add(-monthlyRenewalLead)
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); err == nil {
		t.Fatal("receipt failure did not surface")
	}
	operation := fixture.operation(t)
	if operation["status"] != "verifying" || !strings.Contains(stringValue(operation["result"]), `"phase":"receipt"`) {
		t.Fatalf("receipt retry state=%#v", operation)
	}
	workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	if workspace["paidThrough"] != fixture.renewedThrough.Format(time.RFC3339Nano) {
		t.Fatalf("verified renewal entitlement was not committed: %#v", workspace)
	}
	beforeEvents := append([]string(nil), (*fixture.events)...)
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); err != nil {
		t.Fatal(err)
	}
	if len(fixture.ledger.receipts) != 2 || len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeRenewKeys) != 1 || len(fixture.fabric.storageRenewKeys) != 1 {
		t.Fatalf("receipt retry repeated effects receipts=%d charges=%d compute=%d storage=%d", len(fixture.ledger.receipts), len(fixture.sub2API.charges), len(fixture.fabric.computeRenewKeys), len(fixture.fabric.storageRenewKeys))
	}
	if got := (*fixture.events)[len(beforeEvents):]; len(got) != 1 || got[0] != "ledger.receipt" {
		t.Fatalf("receipt retry events=%#v", got)
	}
}

func TestWorkspaceRenewalUnknownProviderResultNeedsManualReviewWithoutRefund(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	fixture.fabric.computeRenewErr = errors.New("provider response lost")
	fixture.fabric.computeSync = clients.ComputeAllocation{ID: stringValue(fixture.compute["id"]), AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "renewing"}
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(-monthlyRenewalLead)); err != nil {
		t.Fatal(err)
	}
	operation := fixture.operation(t)
	workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	if operation["status"] != "manual_review" || len(fixture.sub2API.refunds) != 0 || workspace["paidThrough"] != fixture.paidThrough.Format(time.RFC3339Nano) {
		t.Fatalf("manual review operation=%#v refunds=%#v Workspace=%#v", operation, fixture.sub2API.refunds, workspace)
	}
}

func TestWorkspaceRenewalConfirmedProviderAbsenceRefundsCombinedDebit(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	fixture.fabric.computeRenewErr = errors.New("provider response lost")
	fixture.fabric.computeSync = clients.ComputeAllocation{ID: stringValue(fixture.compute["id"]), AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "external_deleted"}
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(-monthlyRenewalLead)); err != nil {
		t.Fatal(err)
	}
	operation := fixture.operation(t)
	workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	if operation["status"] != "refunded" || len(fixture.sub2API.refunds) != 1 || fixture.sub2API.refunds[0].RefundUSDMicros != 52_580_000 || workspace["paidThrough"] != fixture.paidThrough.Format(time.RFC3339Nano) {
		t.Fatalf("confirmed absence operation=%#v refunds=%#v Workspace=%#v", operation, fixture.sub2API.refunds, workspace)
	}
}

func TestWorkspaceRenewalRefundReceiptFailureRetriesOnlyReceipt(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	fixture.fabric.computeRenewErr = errors.New("provider response lost")
	fixture.fabric.computeSync = clients.ComputeAllocation{ID: stringValue(fixture.compute["id"]), AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "external_deleted"}
	fixture.ledger.receiptErrors = []error{errors.New("ledger unavailable"), nil}
	now := fixture.paidThrough.Add(-monthlyRenewalLead)
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); err == nil {
		t.Fatal("refund receipt failure did not surface")
	}
	pending, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != "refunded" || pending.Phase != "refund_receipt" || len(fixture.sub2API.refunds) != 1 || len(fixture.ledger.receipts) != 1 || fixture.ledger.receipts[0].Type != "billing.workspace_refunded.v1" {
		t.Fatalf("refund receipt pending operation=%#v refunds=%#v receipts=%#v", pending, fixture.sub2API.refunds, fixture.ledger.receipts)
	}
	beforeEvents := append([]string(nil), (*fixture.events)...)
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); err != nil {
		t.Fatal(err)
	}
	completed, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "refunded" || completed.Phase != "complete" || !strings.Contains(completed.PersistedResult, `"refundReceiptId":"`) || len(fixture.sub2API.refunds) != 1 ||
		len(fixture.fabric.computeRenewKeys) != 1 || len(fixture.ledger.receipts) != 2 || strings.Join((*fixture.events)[len(beforeEvents):], ",") != "ledger.receipt" {
		t.Fatalf("refund receipt retry operation=%#v events=%#v refunds=%#v receipts=%#v", completed, *fixture.events, fixture.sub2API.refunds, fixture.ledger.receipts)
	}
}

func TestWorkspaceRenewalRecoversRefundAfterConfirmationPersistFailure(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	fixture.fabric.computeRenewErr = errors.New("provider response lost")
	fixture.fabric.computeSync = clients.ComputeAllocation{ID: stringValue(fixture.compute["id"]), AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "external_deleted"}
	persistErr := errors.New("persist refund confirmation failed")
	store := &failingWorkspaceRenewalPersistStore{
		memoryTableStore: fixture.app.tables.(*memoryTableStore), err: persistErr,
		fail: func(operation workspaceRenewalOperation) bool { return operation.Status == "refunded" },
	}
	app, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	gateway := &workspaceAdjustmentHistorySub2API{monthlySub2API: fixture.sub2API}
	fixture.service = controlplane.NewService(fixture.ledger, fixture.fabric, gateway)
	now := fixture.paidThrough.Add(-monthlyRenewalLead)
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); !errors.Is(err, persistErr) {
		t.Fatalf("first run error=%v, want %v", err, persistErr)
	}
	if len(fixture.sub2API.refunds) != 1 || len(fixture.ledger.receipts) != 0 {
		t.Fatalf("failed refund persistence refunds=%#v receipts=%#v", fixture.sub2API.refunds, fixture.ledger.receipts)
	}
	restarted, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = restarted
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now.Add(workspaceRenewalLeaseDuration+time.Second)); err != nil {
		t.Fatal(err)
	}
	completed, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "refunded" || completed.Phase != "complete" || !strings.Contains(completed.PersistedResult, `"refundReceiptId":"`) || gateway.historyCalls != 1 ||
		len(fixture.sub2API.refunds) != 1 || len(fixture.ledger.receipts) != 1 || fixture.ledger.receipts[0].Type != "billing.workspace_refunded.v1" {
		t.Fatalf("refund recovery operation=%#v history=%d refunds=%#v receipts=%#v", completed, gateway.historyCalls, fixture.sub2API.refunds, fixture.ledger.receipts)
	}
}

func TestWorkspaceRenewalStorageAbsentAfterComputeRenewedNeedsManualReviewWithoutRefund(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	fixture.fabric.storageRenewErr = errors.New("provider response lost")
	fixture.fabric.storageSync = clients.StorageVolume{
		ID: stringValue(fixture.storage["id"]), AccountID: "acct-monthly", WorkspaceID: "workspace-monthly",
		Status: "external_deleted", CBSStatus: "NOT_FOUND",
	}
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(-monthlyRenewalLead)); err != nil {
		t.Fatal(err)
	}
	operation := fixture.operation(t)
	workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	if operation["status"] != "manual_review" || len(fixture.sub2API.refunds) != 0 || workspace["paidThrough"] != fixture.paidThrough.Format(time.RFC3339Nano) {
		t.Fatalf("partial provider success operation=%#v refunds=%#v Workspace=%#v", operation, fixture.sub2API.refunds, workspace)
	}
}

func TestWorkspaceRenewalExpiryStopsComputeAndNeverDeletesCBS(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, nil)
	workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	paidThrough := time.Now().UTC().Add(-time.Minute)
	workspace["periodStart"] = paidThrough.AddDate(0, -1, 0).Format(time.RFC3339Nano)
	workspace["paidThrough"] = paidThrough.Format(time.RFC3339Nano)
	workspace["nextRenewalAt"] = paidThrough.Add(-monthlyRenewalLead).Format(time.RFC3339Nano)
	workspace["billingAnchorDay"] = int64(paidThrough.Day())
	workspace["autoRenew"], workspace["authorizedBy"], workspace["authorizedAt"] = false, "", ""
	mustStore(t, fixture.app.tables.SaveWorkspace(context.Background(), workspace))
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	expired, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	if expired["renewalStatus"] != "expired_unpaid" || expired["state"] != "suspended" || stringValue(expired["storageId"]) != stringValue(fixture.storage["id"]) {
		t.Fatalf("expired Workspace=%#v", expired)
	}
	events := strings.Join(*fixture.events, ",")
	if !strings.Contains(events, "fabric.compute.cleanup") || strings.Contains(events, "fabric.storage.cleanup") || len(fixture.sub2API.charges) != 0 {
		t.Fatalf("expiry events=%#v charges=%#v", *fixture.events, fixture.sub2API.charges)
	}
	if len(fixture.ledger.receipts) != 1 || fixture.ledger.receipts[0].Type != "billing.workspace_expired.v1" || strings.Contains(string(mustJSON(fixture.ledger.receipts[0])), "password") {
		t.Fatalf("expiry receipts=%#v", fixture.ledger.receipts)
	}
}

func TestWorkspaceRenewalExpiryWaitsForDestroyReadbackBeforeReceipt(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, nil)
	workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	paidThrough := time.Now().UTC().Add(-time.Minute)
	workspace["periodStart"] = paidThrough.AddDate(0, -1, 0).Format(time.RFC3339Nano)
	workspace["paidThrough"] = paidThrough.Format(time.RFC3339Nano)
	workspace["nextRenewalAt"] = paidThrough.Add(-monthlyRenewalLead).Format(time.RFC3339Nano)
	workspace["billingAnchorDay"] = int64(paidThrough.Day())
	workspace["autoRenew"], workspace["authorizedBy"], workspace["authorizedAt"] = false, "", ""
	mustStore(t, fixture.app.tables.SaveWorkspace(context.Background(), workspace))
	fixture.fabric.computeCleanupStatus = "destroying"
	fixture.fabric.computeCleanupSync = clients.ComputeAllocation{ID: stringValue(fixture.compute["id"]), AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "destroyed"}

	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	events := strings.Join(*fixture.events, ",")
	operation, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil {
		t.Fatal(err)
	}
	compute, _ := fixture.app.getCompute(stringValue(fixture.compute["id"]))
	if operation.ExpiryPhase != "complete" || compute["status"] != "destroyed" || compute["billingStatus"] != "stopped" || len(fixture.ledger.receipts) != 1 ||
		strings.Count(events, "fabric.compute.cleanup") != 1 || strings.Count(events, "fabric.compute.sync") != 1 || strings.Index(events, "fabric.compute.sync") > strings.Index(events, "ledger.receipt") {
		t.Fatalf("expiry readback operation=%#v compute=%#v receipts=%#v events=%#v", operation, compute, fixture.ledger.receipts, *fixture.events)
	}
}

func TestWorkspaceRenewalExpiryRetriesUntilDestroyReadbackCompletes(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, nil)
	workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	paidThrough := time.Now().UTC().Add(-time.Minute)
	workspace["periodStart"] = paidThrough.AddDate(0, -1, 0).Format(time.RFC3339Nano)
	workspace["paidThrough"] = paidThrough.Format(time.RFC3339Nano)
	workspace["nextRenewalAt"] = paidThrough.Add(-monthlyRenewalLead).Format(time.RFC3339Nano)
	workspace["billingAnchorDay"] = int64(paidThrough.Day())
	workspace["autoRenew"], workspace["authorizedBy"], workspace["authorizedAt"] = false, "", ""
	mustStore(t, fixture.app.tables.SaveWorkspace(context.Background(), workspace))
	fixture.fabric.computeCleanupStatus = "destroying"
	fixture.fabric.computeCleanupSync = clients.ComputeAllocation{ID: stringValue(fixture.compute["id"]), AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "destroying"}
	now := time.Now().UTC()

	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); err == nil {
		t.Fatal("destroying readback was treated as completed expiry")
	}
	operation, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil {
		t.Fatal(err)
	}
	compute, _ := fixture.app.getCompute(stringValue(fixture.compute["id"]))
	notifications := fixture.app.operatorSummary()["notifications"].(map[string]any)
	hasCleanupAlert := false
	for _, item := range notifications["recent"].([]any) {
		hasCleanupAlert = hasCleanupAlert || stringValue(item.(map[string]any)["code"]) == "cleanup_pending"
	}
	if operation.ExpiryPhase != "compute" || operation.ExpiryErrorCode != "workspace_expiry_compute_cleanup_pending" || compute["status"] != "destroying" || compute["billingStatus"] == "stopped" ||
		len(fixture.ledger.receipts) != 0 || !hasCleanupAlert {
		t.Fatalf("pending destroy operation=%#v compute=%#v receipts=%#v notifications=%#v", operation, compute, fixture.ledger.receipts, notifications)
	}

	fixture.fabric.computeCleanupSync = clients.ComputeAllocation{ID: stringValue(fixture.compute["id"]), AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "destroyed"}
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	events := strings.Join(*fixture.events, ",")
	if operation, err = decodeWorkspaceRenewalOperation(fixture.operation(t)); err != nil || operation.ExpiryPhase != "complete" || len(fixture.ledger.receipts) != 1 ||
		strings.Count(events, "fabric.compute.cleanup") != 1 || strings.Count(events, "fabric.compute.sync") != 2 || strings.Contains(events, "fabric.storage.cleanup") {
		t.Fatalf("destroy retry operation=%#v receipts=%#v events=%#v err=%v", operation, fixture.ledger.receipts, *fixture.events, err)
	}
}

func TestWorkspaceRenewalExpiryReceiptFailureRetriesOnlyReceipt(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, nil)
	workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	paidThrough := time.Now().UTC().Add(-time.Minute)
	workspace["periodStart"] = paidThrough.AddDate(0, -1, 0).Format(time.RFC3339Nano)
	workspace["paidThrough"] = paidThrough.Format(time.RFC3339Nano)
	workspace["nextRenewalAt"] = paidThrough.Add(-monthlyRenewalLead).Format(time.RFC3339Nano)
	workspace["billingAnchorDay"] = int64(paidThrough.Day())
	workspace["autoRenew"], workspace["authorizedBy"], workspace["authorizedAt"] = false, "", ""
	mustStore(t, fixture.app.tables.SaveWorkspace(context.Background(), workspace))
	fixture.ledger.receiptErrors = []error{errors.New("ledger unavailable"), nil}
	now := time.Now().UTC()
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); err == nil {
		t.Fatal("expiry receipt failure did not surface")
	}
	operation, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil || operation.Status != "expired_unpaid" || operation.ExpiryPhase != "receipt" || operation.ExpiryErrorCode != "ledger_expiry_receipt_pending" {
		t.Fatalf("expiry receipt retry state=%#v err=%v", operation, err)
	}
	before := append([]string(nil), (*fixture.events)...)
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); err != nil {
		t.Fatal(err)
	}
	if len(fixture.ledger.receipts) != 2 || strings.Count(strings.Join(*fixture.events, ","), "fabric.compute.cleanup") != 1 || strings.Contains(strings.Join((*fixture.events)[len(before):], ","), "fabric.") {
		t.Fatalf("expiry receipt retry events=%#v receipts=%#v", *fixture.events, fixture.ledger.receipts)
	}
}

type workspaceRenewalPersistedSnapshot struct {
	operation map[string]any
	workspace map[string]any
	compute   map[string]any
	storage   map[string]any
}

type recordingWorkspaceRenewalStore struct {
	*memoryTableStore
	snapshots []workspaceRenewalPersistedSnapshot
}

type failingWorkspaceRenewalPersistStore struct {
	*memoryTableStore
	err    error
	fail   func(workspaceRenewalOperation) bool
	failed bool
}

func (s *failingWorkspaceRenewalPersistStore) PersistWorkspaceRenewal(ctx context.Context, update workspaceRenewalPersistCAS) error {
	operation, err := decodeWorkspaceRenewalOperation(update.DesiredOperation)
	if err != nil {
		return err
	}
	if !s.failed && s.fail(operation) {
		s.failed = true
		return s.err
	}
	return s.memoryTableStore.PersistWorkspaceRenewal(ctx, update)
}

type workspaceAdjustmentHistorySub2API struct {
	*monthlySub2API
	historyCalls      int
	omitRefundHistory bool
	historyErr        error
}

func (s *workspaceAdjustmentHistorySub2API) Usage(context.Context, clients.Sub2APIUsageQuery) (clients.Sub2APIUsagePage, error) {
	return clients.Sub2APIUsagePage{}, nil
}

func (s *workspaceAdjustmentHistorySub2API) UsageStats(context.Context, clients.Sub2APIUsageStatsQuery) (clients.Sub2APIUsageStats, error) {
	return clients.Sub2APIUsageStats{}, nil
}

func (s *workspaceAdjustmentHistorySub2API) BalanceHistory(context.Context, int64) ([]clients.Sub2APIBalanceHistoryEntry, error) {
	s.historyCalls++
	if s.historyErr != nil {
		return nil, s.historyErr
	}
	usedBy := int64(41)
	usedAt := time.Date(2026, 8, 30, 9, 30, 0, 0, time.UTC)
	entries := make([]clients.Sub2APIBalanceHistoryEntry, 0, len(s.charges)+len(s.refunds))
	for _, charge := range s.charges {
		entries = append(entries, clients.Sub2APIBalanceHistoryEntry{
			Code: charge.Code, Type: "balance", ValueUSDMicros: -charge.ChargeUSDMicros, Status: "used", UsedBy: &usedBy, UsedAt: &usedAt, CreatedAt: usedAt,
		})
	}
	for _, refund := range s.refunds {
		if s.omitRefundHistory {
			continue
		}
		entries = append(entries, clients.Sub2APIBalanceHistoryEntry{
			Code: refund.Code, Type: "balance", ValueUSDMicros: refund.RefundUSDMicros, Status: "used", UsedBy: &usedBy, UsedAt: &usedAt, CreatedAt: usedAt,
		})
	}
	return entries, nil
}

func TestWorkspaceRenewalRetriesStableRefundCodeWhenAttemptHistoryMissing(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	fixture.fabric.computeRenewErr = errors.New("provider response lost")
	fixture.fabric.computeSync = clients.ComputeAllocation{ID: stringValue(fixture.compute["id"]), AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "external_deleted"}
	fixture.sub2API.refundErrors = []error{clients.ErrSub2APIChargeUnknown, nil}
	gateway := &workspaceAdjustmentHistorySub2API{monthlySub2API: fixture.sub2API, omitRefundHistory: true}
	fixture.service = controlplane.NewService(fixture.ledger, fixture.fabric, gateway)
	now := fixture.paidThrough.Add(-monthlyRenewalLead)
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); !errors.Is(err, clients.ErrSub2APIChargeUnknown) {
		t.Fatalf("first refund attempt err=%v", err)
	}
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); err != nil {
		t.Fatal(err)
	}
	if len(fixture.sub2API.refunds) != 2 || fixture.sub2API.refunds[0].Code != fixture.sub2API.refunds[1].Code || gateway.historyCalls != 1 ||
		len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeRenewKeys) != 1 || len(fixture.ledger.receipts) != 1 {
		t.Fatalf("refund replay history=%d refunds=%#v charges=%#v compute=%#v receipts=%#v", gateway.historyCalls, fixture.sub2API.refunds, fixture.sub2API.charges, fixture.fabric.computeRenewKeys, fixture.ledger.receipts)
	}
}

func TestWorkspaceRenewalRefundPendingDefersExpiryCleanup(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	fixture.fabric.computeRenewErr = errors.New("provider response lost")
	fixture.fabric.computeSync = clients.ComputeAllocation{ID: stringValue(fixture.compute["id"]), AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "external_deleted"}
	fixture.sub2API.refundErrors = []error{clients.ErrSub2APIChargeUnknown, clients.ErrSub2APIChargeUnknown, clients.ErrSub2APIChargeUnknown}
	gateway := &workspaceAdjustmentHistorySub2API{monthlySub2API: fixture.sub2API, historyErr: errors.New("balance history unavailable")}
	fixture.service = controlplane.NewService(fixture.ledger, fixture.fabric, gateway)
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(-monthlyRenewalLead)); !errors.Is(err, clients.ErrSub2APIChargeUnknown) {
		t.Fatalf("initial refund err=%v", err)
	}
	for attempt := range 2 {
		if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(time.Duration(attempt)*time.Second)); !errors.Is(err, clients.ErrSub2APIChargeUnknown) {
			t.Fatalf("expired retry %d err=%v", attempt, err)
		}
	}
	workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	operation, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil {
		t.Fatal(err)
	}
	events := strings.Join(*fixture.events, ",")
	if workspace["state"] != "suspended" || workspace["status"] != "suspended" || workspace["currentComputeAllocationId"] != fixture.compute["id"] ||
		operation.Status != "refund_pending" || operation.Phase != "refund" || operation.ExpiryStatus != "past_due" || operation.ExpiryPhase != "financial" ||
		strings.Contains(events, "fabric.compute.cleanup") || strings.Contains(events, "fabric.storage.cleanup") {
		t.Fatalf("refund-pending expiry workspace=%#v operation=%#v events=%#v", workspace, operation, *fixture.events)
	}
	for _, receipt := range fixture.ledger.receipts {
		if receipt.Type == "billing.workspace_expired.v1" {
			t.Fatalf("refund pending wrote expiry receipt: %#v", receipt)
		}
	}
}

func TestWorkspaceRenewalRefundReceiptCannotBlockExpiry(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	fixture.fabric.computeRenewErr = errors.New("provider response lost")
	fixture.fabric.computeSync = clients.ComputeAllocation{ID: stringValue(fixture.compute["id"]), AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "external_deleted"}
	fixture.ledger.receiptErrors = []error{
		errors.New("ledger unavailable"), errors.New("ledger unavailable"), errors.New("ledger unavailable"), errors.New("ledger unavailable"), errors.New("ledger unavailable"),
	}
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(-monthlyRenewalLead)); err == nil {
		t.Fatal("initial refund receipt failure did not surface")
	}
	for attempt := range 2 {
		if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(time.Duration(attempt)*time.Second)); err == nil {
			t.Fatalf("expired retry %d did not surface Ledger failure", attempt)
		}
	}
	assertWorkspaceRenewalExpiredWhileEvidencePending(t, fixture, "refunded", "refund_receipt")
	if len(fixture.sub2API.refunds) != 1 {
		t.Fatalf("refund repeated after confirmation: %#v", fixture.sub2API.refunds)
	}
}

func TestWorkspaceRenewalRenewedReceiptCannotBlockNextPeriodExpiry(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	fixture.ledger.receiptErrors = []error{
		errors.New("ledger unavailable"), errors.New("ledger unavailable"), errors.New("ledger unavailable"), errors.New("ledger unavailable"), errors.New("ledger unavailable"),
	}
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(-monthlyRenewalLead)); err == nil {
		t.Fatal("initial renewal receipt failure did not surface")
	}
	for attempt := range 2 {
		if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.renewedThrough.Add(time.Duration(attempt)*time.Second)); err == nil {
			t.Fatalf("next-period expiry retry %d did not surface Ledger failure", attempt)
		}
	}
	assertWorkspaceRenewalExpiredWhileEvidencePending(t, fixture, "verifying", "receipt")
	operation, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil || !operation.EntitlementCommitted || len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeRenewKeys) != 1 || len(fixture.fabric.storageRenewKeys) != 1 {
		t.Fatalf("renewed receipt state operation=%#v charges=%#v compute=%#v storage=%#v err=%v", operation, fixture.sub2API.charges, fixture.fabric.computeRenewKeys, fixture.fabric.storageRenewKeys, err)
	}
}

func assertWorkspaceRenewalExpiredWhileEvidencePending(t *testing.T, fixture workspaceRenewalWorkerFixture, wantStatus, wantPhase string) {
	t.Helper()
	workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	operation, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil {
		t.Fatal(err)
	}
	events := strings.Join(*fixture.events, ",")
	if workspace["autoRenew"] != false || workspace["state"] != "suspended" || workspace["status"] != "suspended" || workspace["renewalStatus"] != "expired_unpaid" ||
		operation.Status != wantStatus || operation.Phase != wantPhase || operation.ExpiryStatus != "expired_unpaid" ||
		strings.Count(events, "fabric.compute.cleanup") != 1 || strings.Contains(events, "fabric.storage.cleanup") {
		t.Fatalf("expiry convergence workspace=%#v operation=%#v events=%#v", workspace, operation, *fixture.events)
	}
}

func (s *recordingWorkspaceRenewalStore) ClaimWorkspaceRenewal(ctx context.Context, claim workspaceRenewalClaimCAS) error {
	if err := s.memoryTableStore.ClaimWorkspaceRenewal(ctx, claim); err != nil {
		return err
	}
	s.capture(stringValue(claim.DesiredOperation["id"]), claim.WorkspaceID)
	return nil
}

func (s *recordingWorkspaceRenewalStore) PersistWorkspaceRenewal(ctx context.Context, update workspaceRenewalPersistCAS) error {
	if err := s.memoryTableStore.PersistWorkspaceRenewal(ctx, update); err != nil {
		return err
	}
	workspaceID := stringValue(update.DesiredOperation["workspaceId"])
	s.capture(update.OperationID, workspaceID)
	return nil
}

func (s *recordingWorkspaceRenewalStore) capture(operationID, workspaceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var operation map[string]any
	for _, row := range s.runtimeOps {
		if stringValue(row["id"]) == operationID {
			operation = cloneMap(row)
			break
		}
	}
	workspace := cloneMap(s.workspaces[workspaceID])
	s.snapshots = append(s.snapshots, workspaceRenewalPersistedSnapshot{
		operation: operation, workspace: workspace,
		compute: cloneMap(s.computes[stringValue(workspace["computeAllocationId"])]), storage: cloneMap(s.storages[stringValue(workspace["storageId"])]),
	})
}

func TestWorkspaceRenewalRestartsFromEveryPersistedPhase(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	store := &recordingWorkspaceRenewalStore{memoryTableStore: fixture.app.tables.(*memoryTableStore)}
	app, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	now := fixture.paidThrough.Add(-monthlyRenewalLead)
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); err != nil {
		t.Fatal(err)
	}
	if len(store.snapshots) < 10 {
		t.Fatalf("persisted phases=%d snapshots=%#v", len(store.snapshots), store.snapshots)
	}
	for index, snapshot := range store.snapshots {
		operation, err := decodeWorkspaceRenewalOperation(snapshot.operation)
		if err != nil {
			t.Fatal(err)
		}
		if terminalWorkspaceRenewal(operation) {
			continue
		}
		t.Run(fmt.Sprintf("%02d_%s_%s", index, operation.Status, operation.Phase), func(t *testing.T) {
			balances := []int64(nil)
			switch {
			case operation.ChargeConfirmation == nil && !operation.ChargeAttempted:
				balances = []int64{100_000_000, 47_420_000}
			case operation.ChargeConfirmation == nil || !operation.PostChargeBalanceKnown:
				balances = []int64{47_420_000}
			}
			replay := newWorkspaceRenewalWorkerFixture(t, balances)
			operation.LeaseToken, operation.LeaseExpiresAt = "", ""
			operationRow := workspaceRenewalOperationRow(operation)
			replayStore := replay.app.tables.(*memoryTableStore)
			replayStore.mu.Lock()
			replayStore.workspaces[operation.WorkspaceID] = cloneMap(snapshot.workspace)
			replayStore.computes[operation.ComputeID] = cloneMap(snapshot.compute)
			replayStore.storages[operation.StorageID] = cloneMap(snapshot.storage)
			replayStore.runtimeOps = []map[string]any{operationRow}
			replayStore.mu.Unlock()
			if err := replay.app.runMonthlyBillingOnce(context.Background(), replay.service, now); err != nil {
				t.Fatal(err)
			}
			completed := replay.operation(t)
			if completed["status"] != "active" {
				t.Fatalf("restart did not complete: %#v", completed)
			}
			if operation.ChargeConfirmation != nil && len(replay.sub2API.charges) != 0 {
				t.Fatalf("restart repeated debit: %#v", replay.sub2API.charges)
			}
			if operation.ComputeRenewal != nil && len(replay.fabric.computeRenewKeys) != 0 {
				t.Fatalf("restart repeated compute renewal: %#v", replay.fabric.computeRenewKeys)
			}
			if operation.StorageRenewal != nil && len(replay.fabric.storageRenewKeys) != 0 {
				t.Fatalf("restart repeated storage renewal: %#v", replay.fabric.storageRenewKeys)
			}
			if operation.EntitlementCommitted && (len(replay.sub2API.charges) != 0 || len(replay.fabric.computeRenewKeys) != 0 || len(replay.fabric.storageRenewKeys) != 0 || len(replay.ledger.receipts) != 1) {
				t.Fatalf("receipt phase repeated prior effects charges=%#v compute=%#v storage=%#v receipts=%#v", replay.sub2API.charges, replay.fabric.computeRenewKeys, replay.fabric.storageRenewKeys, replay.ledger.receipts)
			}
		})
	}
}

func TestWorkspaceRenewalRecoversLostDebitResponseFromBalanceHistoryBeforeBalancePreflight(t *testing.T) {
	gateway := newAuthoritativeReplaySub2API(t, authoritativeReplayConfig{
		chargeValue: "-52.580000", initialBalance: json.RawMessage("100"), adjustedBalance: json.RawMessage("47.42"), loseFirstResponse: true,
	})
	fixture := newWorkspaceRenewalWorkerFixture(t, nil)
	fixture.service = controlplane.NewService(fixture.ledger, fixture.fabric, gateway.client)
	now := fixture.paidThrough.Add(-monthlyRenewalLead)
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); !errors.Is(err, clients.ErrSub2APIChargeUnknown) {
		t.Fatalf("lost debit response error=%v", err)
	}
	fixture.fabric.preflightResults = []clients.MonthlyPreflight{{}, {}}
	pending := fixture.operation(t)
	if pending["status"] != "debit_pending" || !strings.Contains(stringValue(pending["result"]), `"errorCode":"sub2api_charge_unconfirmed"`) {
		t.Fatalf("lost debit state=%#v", pending)
	}
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); err != nil {
		t.Fatal(err)
	}
	if len(gateway.codes) != 1 || gateway.historyCalls != 1 || fixture.operation(t)["status"] != "active" || len(fixture.fabric.computeRenewKeys) != 1 || len(fixture.fabric.storageRenewKeys) != 1 || len(fixture.ledger.receipts) != 1 {
		t.Fatalf("lost-response recovery codes=%#v history=%d operation=%#v compute=%#v storage=%#v receipts=%#v", gateway.codes, gateway.historyCalls, fixture.operation(t), fixture.fabric.computeRenewKeys, fixture.fabric.storageRenewKeys, fixture.ledger.receipts)
	}
}

func TestWorkspaceRenewalRecoversSuccessfulDebitAfterConfirmationPersistFailure(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	persistErr := errors.New("persist charge confirmation failed")
	store := &failingWorkspaceRenewalPersistStore{
		memoryTableStore: fixture.app.tables.(*memoryTableStore), err: persistErr,
		fail: func(operation workspaceRenewalOperation) bool { return operation.ChargeConfirmation != nil },
	}
	app, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	gateway := &workspaceAdjustmentHistorySub2API{monthlySub2API: fixture.sub2API}
	fixture.service = controlplane.NewService(fixture.ledger, fixture.fabric, gateway)
	now := fixture.paidThrough.Add(-monthlyRenewalLead)
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); !errors.Is(err, persistErr) {
		t.Fatalf("first run error=%v, want %v", err, persistErr)
	}
	if len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeRenewKeys) != 0 || len(fixture.fabric.storageRenewKeys) != 0 {
		t.Fatalf("failed persistence effects charges=%#v compute=%#v storage=%#v", fixture.sub2API.charges, fixture.fabric.computeRenewKeys, fixture.fabric.storageRenewKeys)
	}
	restarted, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = restarted
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now.Add(workspaceRenewalLeaseDuration+time.Second)); err != nil {
		t.Fatal(err)
	}
	if operation := fixture.operation(t); operation["status"] != "active" || gateway.historyCalls != 1 || len(fixture.sub2API.charges) != 1 ||
		len(fixture.fabric.computeRenewKeys) != 1 || len(fixture.fabric.storageRenewKeys) != 1 || len(fixture.ledger.receipts) != 1 {
		t.Fatalf("crash recovery operation=%#v history=%d charges=%#v compute=%#v storage=%#v receipts=%#v", operation, gateway.historyCalls, fixture.sub2API.charges, fixture.fabric.computeRenewKeys, fixture.fabric.storageRenewKeys, fixture.ledger.receipts)
	}
}

func TestWorkspaceRenewalRetriesPostChargeBalanceAfterConfirmedDebit(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	balanceErr := errors.New("post-charge balance temporarily unavailable")
	fixture.sub2API.balanceErrors = []error{nil, balanceErr, nil}
	now := fixture.paidThrough.Add(-monthlyRenewalLead)

	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now); !errors.Is(err, balanceErr) {
		t.Fatalf("first run error=%v, want %v", err, balanceErr)
	}
	pending, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil {
		t.Fatal(err)
	}
	notifications := fixture.app.operatorSummary()["notifications"].(map[string]any)
	if pending.Status != "debit_pending" || pending.Phase != "debit" || pending.ErrorCode != "post_charge_balance_unavailable" || pending.ChargeConfirmation == nil ||
		pending.PostChargeBalanceKnown || len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeRenewKeys) != 0 || len(fixture.fabric.storageRenewKeys) != 0 ||
		notifications["total"] != 1 || notifications["recent"].([]any)[0].(map[string]any)["code"] != "renewal_retry_pending" {
		t.Fatalf("pending=%#v notifications=%#v charges=%#v compute=%#v storage=%#v", pending, notifications, fixture.sub2API.charges, fixture.fabric.computeRenewKeys, fixture.fabric.storageRenewKeys)
	}

	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	resolved, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil {
		t.Fatal(err)
	}
	events := strings.Join(*fixture.events, ",")
	if resolved.Status != "active" || !resolved.PostChargeBalanceKnown || resolved.PostChargeBalanceUSDMicros != 47_420_000 || len(fixture.sub2API.charges) != 1 ||
		strings.Count(events, "sub2api.balance") != 3 || strings.Count(events, "sub2api.charge") != 1 ||
		len(fixture.fabric.computeRenewKeys) != 1 || len(fixture.fabric.storageRenewKeys) != 1 || len(fixture.ledger.receipts) != 1 {
		t.Fatalf("resolved=%#v events=%#v charges=%#v compute=%#v storage=%#v receipts=%#v", resolved, *fixture.events, fixture.sub2API.charges, fixture.fabric.computeRenewKeys, fixture.fabric.storageRenewKeys, fixture.ledger.receipts)
	}
}

func TestWorkspaceRenewalInvalidPostChargeBalanceStillNeedsManualReview(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 60_000_000})
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(-monthlyRenewalLead)); err != nil {
		t.Fatal(err)
	}
	operation, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil {
		t.Fatal(err)
	}
	if operation.Status != "manual_review" || operation.ErrorCode != "post_charge_balance_invalid" || !operation.PostChargeBalanceKnown ||
		len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeRenewKeys) != 0 || len(fixture.fabric.storageRenewKeys) != 0 {
		t.Fatalf("operation=%#v charges=%#v compute=%#v storage=%#v", operation, fixture.sub2API.charges, fixture.fabric.computeRenewKeys, fixture.fabric.storageRenewKeys)
	}
}

func TestWorkspaceRenewalRejectsEqualBalanceBeforeCharge(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{52_580_000})
	err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(-monthlyRenewalLead))
	operation, decodeErr := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if err != nil || operation.Status != "insufficient" || operation.Phase != "debit" ||
		len(fixture.sub2API.charges) != 0 || len(fixture.fabric.computeRenewKeys) != 0 || len(fixture.fabric.storageRenewKeys) != 0 {
		t.Fatalf("equal renewal balance crossed debit gate: err=%v operation=%#v charges=%#v compute=%#v storage=%#v", err, operation, fixture.sub2API.charges, fixture.fabric.computeRenewKeys, fixture.fabric.storageRenewKeys)
	}
}

func TestWorkspaceRenewalPostChargeBalanceMustMatchExactDelta(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 40_000_000})
	err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(-monthlyRenewalLead))
	operation, decodeErr := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if err != nil || operation.Status != "manual_review" || operation.ErrorCode != "post_charge_balance_invalid" ||
		len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeRenewKeys) != 0 || len(fixture.fabric.storageRenewKeys) != 0 {
		t.Fatalf("inexact renewal post balance was accepted: err=%v operation=%#v charges=%#v compute=%#v storage=%#v", err, operation, fixture.sub2API.charges, fixture.fabric.computeRenewKeys, fixture.fabric.storageRenewKeys)
	}
}

func TestWorkspaceRenewalDebitedCrashCrossingExpiryCompletesFinancialSaga(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	persistErr := errors.New("persist provider transition failed")
	store := &failingWorkspaceRenewalPersistStore{
		memoryTableStore: fixture.app.tables.(*memoryTableStore), err: persistErr,
		fail: func(operation workspaceRenewalOperation) bool { return operation.Status == "provider_renewing" },
	}
	app, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(-monthlyRenewalLead)); !errors.Is(err, persistErr) {
		t.Fatalf("pre-expiry crash error=%v, want %v", err, persistErr)
	}
	if operation := fixture.operation(t); operation["status"] != "debited" || len(fixture.sub2API.charges) != 1 {
		t.Fatalf("pre-expiry financial state operation=%#v charges=%#v", operation, fixture.sub2API.charges)
	}

	restarted, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = restarted
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	operation, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil {
		t.Fatal(err)
	}
	receipts := map[string]int{}
	for _, receipt := range fixture.ledger.receipts {
		receipts[receipt.Type]++
	}
	workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	compute, _ := fixture.app.getCompute(stringValue(fixture.compute["id"]))
	events := strings.Join(*fixture.events, ",")
	if operation.Status != "active" || operation.ExpiryStatus != "" || operation.ExpiryPhase != "" ||
		workspace["renewalStatus"] != "active" || workspace["state"] != "running" || workspace["status"] != "running" || workspace["currentComputeAllocationId"] != fixture.compute["id"] ||
		compute["status"] != "running" || compute["billingStatus"] != "active" || receipts["billing.workspace_renewed.v1"] != 1 || receipts["billing.workspace_expired.v1"] != 0 || len(fixture.sub2API.refunds) != 0 ||
		len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeRenewKeys) != 1 || len(fixture.fabric.storageRenewKeys) != 1 ||
		strings.Contains(events, "fabric.compute.cleanup") || strings.Contains(events, "fabric.storage.cleanup") {
		t.Fatalf("cross-expiry recovery operation=%#v workspace=%#v compute=%#v receipts=%#v charges=%#v refunds=%#v events=%#v", operation, workspace, compute, receipts, fixture.sub2API.charges, fixture.sub2API.refunds, *fixture.events)
	}
}

func TestWorkspaceRenewalManualReviewCrossingExpiryRemainsResolvable(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	fixture.fabric.computeRenewErr = errors.New("provider response lost")
	fixture.fabric.computeSync = clients.ComputeAllocation{ID: stringValue(fixture.compute["id"]), AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "renewing"}
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(-monthlyRenewalLead)); err != nil {
		t.Fatal(err)
	}
	operationID := stringValue(fixture.operation(t)["id"])
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough); err != nil {
		t.Fatal(err)
	}
	expiredReview, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil {
		t.Fatal(err)
	}
	pendingWorkspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	pendingCompute, _ := fixture.app.getCompute(stringValue(fixture.compute["id"]))
	pendingNotifications := fixture.app.operatorSummary()["notifications"].(map[string]any)
	pendingCodes := map[string]bool{}
	for _, item := range pendingNotifications["recent"].([]any) {
		pendingCodes[stringValue(item.(map[string]any)["code"])] = true
	}
	if expiredReview.Status != "manual_review" || expiredReview.ExpiryStatus != "past_due" || expiredReview.ExpiryPhase != "financial" ||
		pendingWorkspace["state"] != "suspended" || pendingWorkspace["currentComputeAllocationId"] != fixture.compute["id"] || pendingCompute["status"] != "running" ||
		strings.Contains(strings.Join(*fixture.events, ","), "fabric.compute.cleanup") || len(fixture.ledger.receipts) != 0 ||
		pendingNotifications["total"] != 2 || !pendingCodes["manual_review"] || !pendingCodes["past_due"] {
		t.Fatalf("pending review operation=%#v workspace=%#v compute=%#v notifications=%#v receipts=%#v events=%#v", expiredReview, pendingWorkspace, pendingCompute, pendingNotifications, fixture.ledger.receipts, *fixture.events)
	}

	fixture.fabric.computeSync, fixture.fabric.storageSync = fixture.fabric.computeRenew, fixture.fabric.storageRenew
	server, err := NewPersistentServer(fixture.service, fixture.app.tables)
	if err != nil {
		t.Fatal(err)
	}
	response := workspaceRenewalReviewRequest(t, server, reservedOperatorSessionForTest(t, server), operationID, "workspace-review-after-expiry")
	if response.Code != http.StatusOK {
		t.Fatalf("post-expiry resolution status=%d body=%s", response.Code, response.Body.String())
	}
	resolved, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil {
		t.Fatal(err)
	}
	receipts := map[string]int{}
	for _, receipt := range fixture.ledger.receipts {
		receipts[receipt.Type]++
	}
	workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	compute, _ := fixture.app.getCompute(stringValue(fixture.compute["id"]))
	if resolved.Status != "active" || resolved.ReviewResolutionPhase != "completed" || resolved.ExpiryStatus != "" || resolved.ExpiryPhase != "" ||
		workspace["renewalStatus"] != "active" || workspace["state"] != "running" || workspace["status"] != "running" || workspace["currentComputeAllocationId"] != fixture.compute["id"] ||
		compute["status"] != "running" || receipts["billing.workspace_renewed.v1"] != 1 || receipts["billing.workspace_expired.v1"] != 0 ||
		strings.Contains(strings.Join(*fixture.events, ","), "fabric.compute.cleanup") || len(fixture.sub2API.refunds) != 0 {
		t.Fatalf("post-expiry resolution operation=%#v workspace=%#v compute=%#v receipts=%#v refunds=%#v events=%#v", resolved, workspace, compute, receipts, fixture.sub2API.refunds, *fixture.events)
	}
}

func TestWorkspaceRenewalManualReviewCrossingExpirySuspendsWithoutDestroy(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	fixture.fabric.computeRenewErr = errors.New("provider response lost")
	fixture.fabric.computeSync = clients.ComputeAllocation{ID: stringValue(fixture.compute["id"]), AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "renewing"}
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(-monthlyRenewalLead)); err != nil {
		t.Fatal(err)
	}
	if operation := fixture.operation(t); operation["status"] != "manual_review" {
		t.Fatalf("pre-expiry operation=%#v", operation)
	}
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough); err != nil {
		t.Fatal(err)
	}
	workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	operation, err := decodeWorkspaceRenewalOperation(fixture.operation(t))
	if err != nil {
		t.Fatal(err)
	}
	compute, _ := fixture.app.getCompute(stringValue(fixture.compute["id"]))
	events := strings.Join(*fixture.events, ",")
	if workspace["renewalStatus"] != "active" || workspace["state"] != "suspended" || workspace["autoRenew"] != true || workspace["currentComputeAllocationId"] != fixture.compute["id"] ||
		operation.Status != "manual_review" || operation.ExpiryStatus != "past_due" || operation.ExpiryPhase != "financial" || compute["status"] != "running" ||
		strings.Contains(events, "fabric.compute.cleanup") || strings.Contains(events, "fabric.storage.cleanup") {
		t.Fatalf("manual-review expiry workspace=%#v operation=%#v events=%#v", workspace, operation, *fixture.events)
	}
	for _, receipt := range fixture.ledger.receipts {
		if receipt.Type == "billing.workspace_expired.v1" {
			t.Fatalf("pending manual review wrote expiry receipt: %#v", receipt)
		}
	}
	notifications := fixture.app.operatorSummary()["notifications"].(map[string]any)
	codes := map[string]bool{}
	for _, item := range notifications["recent"].([]any) {
		codes[stringValue(item.(map[string]any)["code"])] = true
	}
	if notifications["total"] != 2 || !codes["manual_review"] || !codes["past_due"] {
		t.Fatalf("pending manual-review notifications=%#v", notifications)
	}
}

func TestWorkspaceRenewalRefundedPeriodExpiresAndStopsCompute(t *testing.T) {
	fixture := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	fixture.fabric.computeRenewErr = errors.New("provider response lost")
	fixture.fabric.computeSync = clients.ComputeAllocation{ID: stringValue(fixture.compute["id"]), AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "external_deleted"}
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough.Add(-monthlyRenewalLead)); err != nil {
		t.Fatal(err)
	}
	if operation := fixture.operation(t); operation["status"] != "refunded" || len(fixture.sub2API.refunds) != 1 {
		t.Fatalf("pre-expiry operation=%#v refunds=%#v", operation, fixture.sub2API.refunds)
	}
	if strings.Contains(strings.Join(*fixture.events, ","), "fabric.compute.cleanup") {
		t.Fatalf("compute cleaned before paid-through: events=%#v", *fixture.events)
	}
	if err := fixture.app.runMonthlyBillingOnce(context.Background(), fixture.service, fixture.paidThrough); err != nil {
		t.Fatal(err)
	}
	workspace, _ := fixture.app.getWorkspace(stringValue(fixture.workspace["id"]))
	operation := fixture.operation(t)
	events := strings.Join(*fixture.events, ",")
	receipts := map[string]int{}
	for _, receipt := range fixture.ledger.receipts {
		receipts[receipt.Type]++
	}
	refundAt, cleanupAt, expiryReceiptAt := strings.Index(events, "sub2api.refund"), strings.Index(events, "fabric.compute.cleanup"), strings.LastIndex(events, "ledger.receipt")
	if workspace["renewalStatus"] != "expired_unpaid" || workspace["state"] != "suspended" || workspace["autoRenew"] != false || stringValue(workspace["currentComputeAllocationId"]) != "" ||
		operation["status"] != "refunded" || !strings.Contains(stringValue(operation["result"]), `"expiryPhase":"complete"`) || !strings.Contains(stringValue(operation["result"]), `"priorStatus":"refunded"`) ||
		strings.Count(events, "fabric.compute.cleanup") != 1 || strings.Contains(events, "fabric.storage.cleanup") || len(fixture.sub2API.refunds) != 1 ||
		receipts["billing.workspace_refunded.v1"] != 1 || receipts["billing.workspace_expired.v1"] != 1 || refundAt < 0 || cleanupAt < refundAt || expiryReceiptAt < cleanupAt {
		t.Fatalf("refunded expiry workspace=%#v operation=%#v events=%#v refunds=%#v", workspace, operation, *fixture.events, fixture.sub2API.refunds)
	}
}
