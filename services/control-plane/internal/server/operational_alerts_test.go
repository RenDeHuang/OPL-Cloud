package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strings"
	"testing"
)

func TestOperatorSummaryDerivesOperationalNotifications(t *testing.T) {
	app := newControlPlaneApp()
	for _, row := range []map[string]any{
		{"id": "compute-review", "accountId": "acct-review", "billingStatus": "manual_review"},
		{"id": "compute-receipt", "accountId": "acct-receipt", "billingStatus": "active", "lastBillingError": "ledger_receipt_pending"},
		{"id": "compute-past-due", "accountId": "acct-past-due", "billingStatus": "past_due"},
	} {
		mustStore(t, app.tables.SaveCompute(context.Background(), row))
	}
	for _, row := range []map[string]any{
		{"id": "storage-cleanup", "accountId": "acct-cleanup", "billingStatus": "failed", "lastBillingError": "fabric_prepare_cleanup_failed"},
		{"id": "storage-expiry", "accountId": "acct-expiry", "billingStatus": "past_due", "lastBillingError": "fabric_expiry_destroy_failed"},
	} {
		mustStore(t, app.tables.SaveStorage(context.Background(), row))
	}

	notifications := app.operatorSummary()["notifications"].(map[string]any)
	if notifications["total"] != 6 || notifications["error"] != 3 || notifications["warning"] != 3 {
		t.Fatalf("unexpected notification counts: %#v", notifications)
	}
	codes := map[string]int{}
	for _, item := range notifications["recent"].([]any) {
		notification := item.(map[string]any)
		codes[stringValue(notification["code"])]++
	}
	for code, want := range map[string]int{"manual_review": 1, "past_due": 2, "ledger_receipt_pending": 1, "cleanup_failed": 2} {
		if codes[code] != want {
			t.Fatalf("notification code %q count = %d, want %d: %#v", code, codes[code], want, notifications)
		}
	}
	payload, err := json.Marshal(notifications)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "sub2apiRedeemCode") || strings.Contains(string(payload), "lastBillingError") {
		t.Fatalf("operator notification leaked billing internals: %s", payload)
	}
}

func TestOperatorSummaryIncludesWorkspaceRenewalAlerts(t *testing.T) {
	tests := []struct {
		name, status, phase, errorCode, wantCode string
	}{
		{name: "manual review", status: "manual_review", phase: "verify_compute", errorCode: "fabric_compute_renewal_unconfirmed", wantCode: "manual_review"},
		{name: "insufficient", status: "insufficient", phase: "debit", errorCode: "monthly_balance_insufficient", wantCode: "insufficient"},
		{name: "renewal receipt", status: "verifying", phase: "receipt", errorCode: "ledger_receipt_pending", wantCode: "renewal_receipt_pending"},
		{name: "refund receipt", status: "refunded", phase: "refund_receipt", errorCode: "ledger_refund_receipt_pending", wantCode: "refund_receipt_pending"},
		{name: "expiry receipt", status: "expired_unpaid", phase: "expiry_receipt", errorCode: "ledger_expiry_receipt_pending", wantCode: "expiry_receipt_pending"},
		{name: "cleanup", status: "expired_unpaid", phase: "expire_compute", errorCode: "workspace_expiry_compute_cleanup_pending", wantCode: "cleanup_pending"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newWorkspaceRenewalWorkerFixture(t, nil)
			app, workspace := fixture.app, fixture.workspace
			operation := workspaceRenewalOperation{
				ID: "workspace-renewal-alert", Status: tc.status, CreatedAt: "2026-07-17T00:00:00Z", RequestHash: "alert-request",
				Phase: tc.phase, AccountID: firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"])),
				WorkspaceID: stringValue(workspace["id"]), PaidThrough: "2026-08-17T00:00:00Z", ErrorCode: tc.errorCode,
			}
			mustStore(t, app.tables.SaveRuntimeOperation(context.Background(), workspaceRenewalOperationRow(operation)))
			notifications := app.operatorSummary()["notifications"].(map[string]any)
			if notifications["total"] != 1 {
				t.Fatalf("notifications=%#v", notifications)
			}
			item := notifications["recent"].([]any)[0].(map[string]any)
			if item["resourceType"] != "workspace" || item["resourceId"] != workspace["id"] || item["code"] != tc.wantCode {
				t.Fatalf("notification=%#v wantCode=%s", item, tc.wantCode)
			}
		})
	}
}

func TestMonthlyOperationalLogsAreStableDeduplicatedAndRedacted(t *testing.T) {
	var output bytes.Buffer
	previousOutput, previousFlags, previousPrefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	app := newControlPlaneApp()
	row := map[string]any{
		"id":                "compute-sensitive-id",
		"accountId":         "acct-sensitive",
		"billingStatus":     "manual_review",
		"lastBillingError":  "provider-secret-detail",
		"sub2apiRedeemCode": "redeem-sensitive",
	}
	mustStore(t, app.saveMonthlyResource(context.Background(), "compute", row))
	mustStore(t, app.saveMonthlyResource(context.Background(), "compute", row))
	row["billingStatus"] = "active"
	delete(row, "lastBillingError")
	mustStore(t, app.saveMonthlyResource(context.Background(), "compute", row))

	logs := output.String()
	if strings.Count(logs, "event=opl_operational_state code=manual_review state=active") != 1 {
		t.Fatalf("active transition should be logged once: %q", logs)
	}
	if strings.Count(logs, "event=opl_operational_state code=manual_review state=recovered") != 1 {
		t.Fatalf("recovery transition should be logged once: %q", logs)
	}
	for _, secret := range []string{"compute-sensitive-id", "acct-sensitive", "provider-secret-detail", "redeem-sensitive"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("operational log leaked %q: %q", secret, logs)
		}
	}
}
