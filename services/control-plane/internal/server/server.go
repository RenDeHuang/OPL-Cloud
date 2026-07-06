package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/controlplane"
)

func NewServer(service *controlplane.Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"token": "control-plane-local-token"})
	})
	mux.HandleFunc("GET /api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, sessionPayload())
	})
	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"id": "usr-local", "role": "owner"})
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, consoleState())
	})
	mux.HandleFunc("GET /api/management/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, managementState())
	})
	mux.HandleFunc("GET /api/operator/summary", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, operatorSummary())
	})
	mux.HandleFunc("GET /api/runtime/readiness", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ready": true, "missingEnv": []any{}, "missingTools": []any{}, "failedChecks": []any{}})
	})
	mux.HandleFunc("GET /api/production/readiness", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ready": true, "missingEnv": []any{}, "missingTools": []any{}, "failedChecks": []any{}, "checks": []any{}})
	})
	mux.HandleFunc("GET /api/overview", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"service": "control-plane", "workspaces": 0})
	})
	mux.HandleFunc("GET /api/workspaces", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, []any{})
	})
	mux.HandleFunc("POST /api/workspaces/reset-token", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		writeJSON(w, http.StatusOK, map[string]any{"id": stringField(input, "workspaceId", "ws-local"), "tokenStatus": "active"})
	})
	mux.HandleFunc("POST /api/workspaces/delete-token", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		writeJSON(w, http.StatusOK, map[string]any{"id": stringField(input, "workspaceId", "ws-local"), "tokenStatus": "deleted"})
	})
	mux.HandleFunc("POST /api/workspaces/runtime-status", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		writeJSON(w, http.StatusOK, map[string]any{"id": stringField(input, "workspaceId", "ws-local"), "status": "ready"})
	})
	mux.HandleFunc("POST /api/workspaces", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		var input controlplane.CreateWorkspaceInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		workspace, err := service.CreateWorkspace(r.Context(), input, idempotencyKey)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, workspace)
	})
	mux.HandleFunc("GET /api/billing/summary", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"currency": "CNY", "balanceCents": 0})
	})
	mux.HandleFunc("POST /api/billing/topups", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		amount := numberField(input, "amount", 0)
		writeJSON(w, http.StatusCreated, map[string]any{
			"id":                  "manual-topup-local",
			"idempotencyKey":      stringField(input, "idempotencyKey", ""),
			"targetAccountId":     stringField(input, "accountId", "acct-local"),
			"amount":              amount,
			"balanceBefore":       0,
			"balanceAfter":        amount,
			"operatorUserId":      stringField(input, "operatorUserId", ""),
			"operatorAccountId":   stringField(input, "operatorAccountId", ""),
			"ledgerEntryId":       "ledger-local-topup",
			"walletTransactionId": "wallet-local-topup",
			"status":              "completed",
		})
	})
	mux.HandleFunc("POST /api/billing/resource-settlements", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}, "account": map[string]any{"id": stringField(input, "accountId", "acct-local")}})
	})
	mux.HandleFunc("POST /api/billing/reconciliation", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		report, _ := input["report"].(map[string]any)
		if report == nil {
			report = map[string]any{}
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": stringField(report, "id", "reconciliation-local"), "guard": map[string]any{"status": "ok", "blockNewWorkspaces": false, "reason": "operator_reconciliation"}})
	})
	mux.HandleFunc("GET /api/compute-pools", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, consoleState()["computePools"])
	})
	mux.HandleFunc("GET /api/compute-allocations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, consoleState()["computeAllocations"])
	})
	mux.HandleFunc("POST /api/compute-allocations", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		writeJSON(w, http.StatusAccepted, resourceResult("compute", stringField(input, "name", "compute-local")))
	})
	mux.HandleFunc("GET /api/compute-allocations/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"id": strings.TrimSpace(r.PathValue("id")), "status": "running", "billingStatus": "active"})
	})
	mux.HandleFunc("POST /api/compute-allocations/{id}/destroy", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"id": strings.TrimSpace(r.PathValue("id")), "status": "destroyed", "billingStatus": "stopped"})
	})
	mux.HandleFunc("POST /api/storage-volumes", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		writeJSON(w, http.StatusAccepted, resourceResult("storage", stringField(input, "name", "storage-local")))
	})
	mux.HandleFunc("POST /api/storage-volumes/destroy", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		writeJSON(w, http.StatusOK, map[string]any{"id": stringField(input, "storageId", "storage-local"), "status": "destroyed", "billingStatus": "stopped"})
	})
	mux.HandleFunc("POST /api/storage-attachments", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		writeJSON(w, http.StatusAccepted, map[string]any{"id": "attach-local", "computeAllocationId": input["computeAllocationId"], "storageId": input["storageId"], "status": "attached", "mountPath": stringField(input, "mountPath", "/data")})
	})
	mux.HandleFunc("POST /api/storage-attachments/detach", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		writeJSON(w, http.StatusOK, map[string]any{"id": stringField(input, "attachmentId", "attach-local"), "status": "detached"})
	})
	mux.HandleFunc("GET /api/support/tickets", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"tickets": []any{}})
	})
	mux.HandleFunc("POST /api/support/tickets", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		writeJSON(w, http.StatusCreated, map[string]any{"id": "ticket-local", "title": stringField(input, "title", "Support"), "status": "open"})
	})
	mux.HandleFunc("GET /api/ledger/task-receipts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"receipts": []any{}})
	})
	mux.HandleFunc("POST /api/organizations", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		name := stringField(input, "name", "Organization")
		writeJSON(w, http.StatusCreated, map[string]any{"id": "org-local", "name": name, "billingAccountId": stringField(input, "billingAccountId", "acct-local"), "status": "active"})
	})
	mux.HandleFunc("POST /api/organizations/members", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		writeJSON(w, http.StatusCreated, map[string]any{"id": "membership-local", "organizationId": stringField(input, "organizationId", "org-local"), "userId": stringField(input, "userId", "usr-local"), "role": stringField(input, "role", "member"), "status": "active"})
	})
	mux.HandleFunc("POST /api/users", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		writeJSON(w, http.StatusCreated, map[string]any{"id": "usr-local", "email": stringField(input, "email", "owner@example.com"), "accountId": stringField(input, "accountId", "acct-local"), "status": "active"})
	})
	mux.HandleFunc("POST /api/users/disable", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		writeJSON(w, http.StatusOK, map[string]any{"id": stringField(input, "userId", "usr-local"), "status": "disabled"})
	})
	mux.HandleFunc("POST /api/users/delete", func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		writeJSON(w, http.StatusOK, map[string]any{"id": stringField(input, "userId", "usr-local"), "status": "deleted"})
	})
	mux.HandleFunc("POST /api/operator/cleanup-workspace-access", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"cleaned": []any{}, "skipped": []any{}})
	})
	mux.HandleFunc("GET /api/admin/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"service": "control-plane", "status": "ok"})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func decodeJSON(r *http.Request) map[string]any {
	var input map[string]any
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		return map[string]any{}
	}
	return input
}

func stringField(input map[string]any, key string, fallback string) string {
	if value, ok := input[key].(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func numberField(input map[string]any, key string, fallback float64) float64 {
	switch value := input[key].(type) {
	case float64:
		return value
	case int:
		return float64(value)
	default:
		return fallback
	}
}

func sessionPayload() map[string]any {
	return map[string]any{
		"user":      map[string]any{"id": "usr-local", "email": "owner@example.com", "accountId": "acct-local", "role": "admin", "status": "active"},
		"csrfToken": "csrf-local",
	}
}

func consoleState() map[string]any {
	return map[string]any{
		"product":               map[string]any{"name": "OPL Cloud", "console": "OPL Console", "workspace": "OPL Workspace"},
		"billingPolicy":         map[string]any{"holdDays": 7, "priceBasis": "OPL price list"},
		"packages":              []any{map[string]any{"id": "basic", "name": "Basic", "available": true, "cpu": 2, "memoryGb": 4, "diskGb": 10, "server": "standard", "price": map[string]any{"computeHourly": 1, "storageGbMonth": 0.2}}},
		"computePools":          []any{map[string]any{"id": "pool-basic", "name": "Basic", "available": true}},
		"wallet":                map[string]any{"accountId": "acct-local", "balance": 0, "frozen": 0, "available": 0, "totalRecharged": 0},
		"account":               map[string]any{"id": "acct-local", "balance": 0, "frozen": 0, "available": 0, "totalRecharged": 0},
		"user":                  sessionPayload()["user"],
		"workspaces":            []any{},
		"computeAllocations":    []any{},
		"storageVolumes":        []any{},
		"storageAttachments":    []any{},
		"billingLedger":         []any{},
		"resourceUsageLogs":     []any{},
		"walletTransactions":    []any{},
		"manualTopups":          []any{},
		"billingReconciliation": map[string]any{"guard": map[string]any{"status": "not_required", "blockNewWorkspaces": false, "reason": "billing_reconciliation_not_required"}},
		"evidenceLedger":        []any{},
		"audit":                 []any{},
		"notifications":         []any{},
		"runtimeOperations":     []any{},
		"generatedAt":           time.Now().UTC().Format(time.RFC3339),
	}
}

func managementState() map[string]any {
	return map[string]any{
		"organization":           nil,
		"organizations":          []any{},
		"users":                  []any{map[string]any{"id": "usr-local", "email": "owner@example.com", "accountId": "acct-local", "role": "admin", "status": "active"}},
		"memberships":            []any{},
		"accounts":               []any{map[string]any{"id": "acct-local", "accountId": "acct-local", "email": "owner@example.com", "balance": 0, "frozen": 0, "totalRecharged": 0}},
		"packages":               consoleState()["packages"],
		"computePools":           consoleState()["computePools"],
		"workspaces":             []any{},
		"computeAllocations":     []any{},
		"storageVolumes":         []any{},
		"storageAttachments":     []any{},
		"resourceLedgerEvidence": []any{},
		"walletTransactions":     []any{},
		"manualTopups":           []any{},
	}
}

func operatorSummary() map[string]any {
	return map[string]any{
		"product":                "OPL Console",
		"generatedAt":            time.Now().UTC().Format(time.RFC3339),
		"accountScope":           "all",
		"accounts":               map[string]any{"total": 1, "frozen": 0, "balance": 0, "totalSpent": 0},
		"workspaces":             map[string]any{"total": 0, "running": 0, "urlActive": 0, "destroyed": 0, "needsAttention": 0},
		"computeAllocations":     map[string]any{"total": 0, "running": 0, "failed": 0},
		"notifications":          map[string]any{"total": 0, "error": 0, "warning": 0, "recent": []any{}},
		"runtimeOperations":      map[string]any{"total": 0, "failed": 0, "recentFailed": []any{}},
		"failedOperations":       []any{},
		"resourceAnomalies":      []any{},
		"resourceLedgerEvidence": map[string]any{"total": 0, "recent": []any{}},
		"productionE2E":          map[string]any{},
		"billingReconciliation":  map[string]any{"reports": 0, "guard": map[string]any{"status": "not_required", "blockNewWorkspaces": false, "reason": "billing_reconciliation_not_required"}},
	}
}

func resourceResult(resourceType string, name string) map[string]any {
	id := resourceType + "-local"
	return map[string]any{"id": id, "name": name, "status": "provisioning", "billingStatus": "active", "operationId": "op-" + id, "providerRequestId": "req-" + id}
}
