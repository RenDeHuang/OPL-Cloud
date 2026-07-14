package server

import (
	"os"
	"strconv"
	"time"
)

type retentionPolicy struct {
	AdminAuditDays     int
	SupportDays        int
	ProductionE2EDays  int
	TerminalArchiveRun bool
}

func currentRetentionPolicy() retentionPolicy {
	return retentionPolicy{
		AdminAuditDays:     envInt("OPL_RETENTION_ADMIN_AUDIT_DAYS", 180),
		SupportDays:        envInt("OPL_RETENTION_SUPPORT_DAYS", 365),
		ProductionE2EDays:  envInt("OPL_RETENTION_PRODUCTION_E2E_DAYS", 90),
		TerminalArchiveRun: true,
	}
}

func (policy retentionPolicy) dto() map[string]any {
	return map[string]any{
		"terminalResources": map[string]any{"action": "archive", "currentStateOnly": true, "enabled": policy.TerminalArchiveRun},
		"adminAuditDays":    policy.AdminAuditDays,
		"supportDays":       policy.SupportDays,
		"productionE2EDays": policy.ProductionE2EDays,
		"billingLedger":     map[string]any{"action": "retain", "reason": "money_evidence_not_archived_by_control_plane"},
		"fabricOperations":  map[string]any{"action": "retain", "reason": "provider_evidence"},
	}
}

func (policy retentionPolicy) cutoff(days int) time.Time {
	if days <= 0 {
		return time.Time{}
	}
	return time.Now().UTC().AddDate(0, 0, -days)
}

func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func rowsAsAny(rows []controlPlaneRecord) []any {
	output := make([]any, 0, len(rows))
	for _, row := range rows {
		output = append(output, row)
	}
	return output
}

func productionE2ESummary(rows []controlPlaneRecord) map[string]any {
	recent := make([]any, 0, len(rows))
	passed := 0
	failed := 0
	for index := len(rows) - 1; index >= 0; index-- {
		row := rows[index]
		switch stringValue(row["status"]) {
		case "passed":
			passed++
		case "failed":
			failed++
		}
		if len(recent) < 10 {
			recent = append(recent, row)
		}
	}
	return map[string]any{"total": len(rows), "passed": passed, "failed": failed, "recent": recent}
}
