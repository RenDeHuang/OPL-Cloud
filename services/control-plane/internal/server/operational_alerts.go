package server

import (
	"log"
	"slices"
	"sort"
	"strings"
)

var operationalAlertCodes = [...]string{
	"manual_review", "past_due", "ledger_receipt_pending", "cleanup_failed",
	"insufficient", "renewal_receipt_pending", "refund_receipt_pending", "expiry_receipt_pending", "cleanup_pending",
}

func monthlyOperationalAlertCodes(row map[string]any) []string {
	codes := make([]string, 0, len(operationalAlertCodes))
	status, lastError := stringValue(row["billingStatus"]), stringValue(row["lastBillingError"])
	renewalStatus, renewalError := stringValue(row["renewalStatus"]), stringValue(row["renewalErrorCode"])
	if status == "manual_review" || renewalStatus == "manual_review" {
		codes = append(codes, "manual_review")
	}
	if status == "past_due" {
		codes = append(codes, "past_due")
	}
	if lastError == "ledger_receipt_pending" {
		codes = append(codes, "ledger_receipt_pending")
	}
	if strings.HasSuffix(lastError, "_cleanup_failed") || lastError == "fabric_expiry_destroy_failed" {
		codes = append(codes, "cleanup_failed")
	}
	if renewalStatus == "insufficient" {
		codes = append(codes, "insufficient")
	}
	if strings.HasPrefix(renewalError, "ledger_receipt_") {
		codes = append(codes, "renewal_receipt_pending")
	}
	if strings.HasPrefix(renewalError, "ledger_refund_receipt_") {
		codes = append(codes, "refund_receipt_pending")
	}
	if strings.HasPrefix(renewalError, "ledger_expiry_receipt_") {
		codes = append(codes, "expiry_receipt_pending")
	}
	if renewalError == "workspace_expiry_compute_cleanup_pending" {
		codes = append(codes, "cleanup_pending")
	}
	return codes
}

func workspaceRenewalOperationalRows(workspaces controlPlaneRecordSet, operations []map[string]any) controlPlaneRecordSet {
	rows := controlPlaneRecordSet{}
	for id, workspace := range workspaces {
		rows[id] = cloneMap(workspace)
	}
	paidThroughByWorkspace := map[string]string{}
	for _, row := range operations {
		if stringValue(row["action"]) != "workspace.renewal" {
			continue
		}
		operation, err := decodeWorkspaceRenewalOperation(row)
		if err != nil || rows[operation.WorkspaceID] == nil || operation.PaidThrough < paidThroughByWorkspace[operation.WorkspaceID] {
			continue
		}
		paidThroughByWorkspace[operation.WorkspaceID] = operation.PaidThrough
		rows[operation.WorkspaceID]["renewalStatus"] = operation.Status
		rows[operation.WorkspaceID]["renewalPhase"] = operation.Phase
		rows[operation.WorkspaceID]["renewalErrorCode"] = operation.ErrorCode
	}
	return rows
}

func operationalNotificationSummary(workspaces, computes, storages controlPlaneRecordSet) map[string]any {
	recent := make([]any, 0)
	errorCount, warningCount := 0, 0
	appendRows := func(resourceType string, rows controlPlaneRecordSet) {
		for _, row := range rows {
			for _, code := range monthlyOperationalAlertCodes(row) {
				severity := "error"
				if code == "past_due" || code == "ledger_receipt_pending" || code == "insufficient" || code == "renewal_receipt_pending" || code == "refund_receipt_pending" || code == "expiry_receipt_pending" {
					severity = "warning"
					warningCount++
				} else {
					errorCount++
				}
				recent = append(recent, map[string]any{
					"id":           "alert-" + stableID(code, resourceType, stringValue(row["id"]))[:12],
					"type":         code,
					"code":         code,
					"severity":     severity,
					"resourceType": resourceType,
					"resourceId":   stringValue(row["id"]),
					"accountId":    firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"])),
					"workspaceId":  stringValue(row["workspaceId"]),
				})
			}
		}
	}
	appendRows("workspace", workspaces)
	appendRows("compute", computes)
	appendRows("storage", storages)
	sort.Slice(recent, func(i, j int) bool {
		left, right := recent[i].(map[string]any), recent[j].(map[string]any)
		return stringValue(left["id"]) < stringValue(right["id"])
	})
	return map[string]any{"total": len(recent), "error": errorCount, "warning": warningCount, "recent": recent}
}

func (app *controlPlaneServer) observeMonthlyOperationalAlerts(resourceType string, row map[string]any) {
	id := stringValue(row["id"])
	if id == "" {
		return
	}
	activeCodes := monthlyOperationalAlertCodes(row)
	resourceRef := stableID(resourceType, id)[:12]
	for _, code := range operationalAlertCodes {
		key := resourceType + ":" + id + ":" + code
		if slices.Contains(activeCodes, code) {
			if _, loaded := app.operationalAlertStates.LoadOrStore(key, struct{}{}); !loaded {
				log.Printf("event=opl_operational_state code=%s state=active resource_type=%s resource_ref=%s", code, resourceType, resourceRef)
			}
			continue
		}
		if _, loaded := app.operationalAlertStates.LoadAndDelete(key); loaded {
			log.Printf("event=opl_operational_state code=%s state=recovered resource_type=%s resource_ref=%s", code, resourceType, resourceRef)
		}
	}
}
