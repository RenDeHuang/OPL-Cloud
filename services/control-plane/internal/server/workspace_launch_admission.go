package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	controlledBasicPilotEnabledEnv     = "OPL_CONTROLLED_BASIC_PILOT_ENABLED"
	controlledBasicPilotAccountsEnv    = "OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS"
	controlledBasicPilotMaxInFlightEnv = "OPL_CONTROLLED_BASIC_PILOT_MAX_IN_FLIGHT"
	controlledBasicPilotDefaultLimit   = 1
	productionAcceptanceBApprovalEnv   = "OPL_PRODUCTION_BASIC_ACCEPTANCE_B_APPROVAL_JSON"
	productionAcceptanceBCapability    = "x-opl-acceptance-b-capability"
	productionAcceptanceBApprovalID    = "x-opl-acceptance-b-approval-id"
	productionAcceptanceBConfirmation  = "RUN_ONE_INDEPENDENT_FRESH_BASIC_ORDER_FOR_ACCEPTANCE_B"
)

var productionAcceptanceBAllowedWrites = []string{
	"submit_one_workspace_launch", "debit_one_basic_month", "create_one_workspace_key", "ensure_one_compute_allocation",
	"ensure_one_storage", "ensure_one_attachment", "ensure_one_gateway_secret", "ensure_one_runtime",
	"activate_one_workspace", "record_one_purchase_receipt",
}

var productionAcceptanceBApprovalIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,199}$`)
var productionAcceptanceBReleaseSHAPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

var productionAcceptanceBForbiddenWrites = []string{
	"provision_account", "adjust_wallet", "submit_second_workspace_launch", "create_second_compute_allocation", "create_second_storage",
	"refund", "renew", "delete", "replace", "send_model_request",
}

type productionAcceptanceBApproval struct {
	SchemaVersion int    `json:"schemaVersion"`
	OperationMode string `json:"operationMode"`
	ApprovalID    string `json:"approvalId"`
	ExpiresAt     string `json:"expiresAt"`
	Confirmation  string `json:"confirmation"`
	Release       struct {
		MergedMainSHA        string `json:"mergedMainSha"`
		CloudImageDigest     string `json:"cloudImageDigest"`
		WorkspaceImageDigest string `json:"workspaceImageDigest"`
	} `json:"release"`
	Customer struct {
		Email     string `json:"email"`
		AccountID string `json:"accountId"`
	} `json:"customer"`
	Launch struct {
		IdempotencyKey string `json:"idempotencyKey"`
		OperationID    string `json:"operationId"`
		WorkspaceID    string `json:"workspaceId"`
		Name           string `json:"name"`
		PackageID      string `json:"packageId"`
		SizeGB         int    `json:"sizeGb"`
		AutoRenew      bool   `json:"autoRenew"`
	} `json:"launch"`
	AllowedWrites   []string `json:"allowedWrites"`
	ForbiddenWrites []string `json:"forbiddenWrites"`
}

type controlledBasicPilotAdmission struct {
	Enabled     bool
	Configured  bool
	AccountIDs  map[string]struct{}
	MaxInFlight int
}

func controlledBasicPilotAdmissionFromEnv() controlledBasicPilotAdmission {
	admission := controlledBasicPilotAdmission{
		AccountIDs:  map[string]struct{}{},
		MaxInFlight: controlledBasicPilotDefaultLimit,
	}
	valid := true
	switch strings.TrimSpace(os.Getenv(controlledBasicPilotEnabledEnv)) {
	case "", "0":
	case "1":
		admission.Enabled = true
	default:
		valid = false
	}
	for _, raw := range strings.Split(os.Getenv(controlledBasicPilotAccountsEnv), ",") {
		accountID := strings.TrimSpace(raw)
		if accountID == "" {
			continue
		}
		if !validAccountID(accountID) {
			valid = false
			continue
		}
		admission.AccountIDs[accountID] = struct{}{}
	}
	if raw := strings.TrimSpace(os.Getenv(controlledBasicPilotMaxInFlightEnv)); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			valid = false
		} else {
			admission.MaxInFlight = limit
		}
	}
	admission.Configured = valid && (!admission.Enabled || len(admission.AccountIDs) > 0)
	return admission
}

func (admission controlledBasicPilotAdmission) rejectNewLaunch(accountID, packageID string, autoRenew bool) string {
	if !admission.Configured {
		return "workspace_launch_admission_invalid"
	}
	if packageID != "basic" {
		return "workspace_launch_basic_only"
	}
	if autoRenew {
		return "autoRenew_unavailable"
	}
	if !admission.Enabled {
		return "workspace_launch_admission_disabled"
	}
	if _, ok := admission.AccountIDs[accountID]; !ok {
		return "workspace_launch_account_not_allowed"
	}
	return ""
}

func exactStringSlice(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for index := range expected {
		if actual[index] != expected[index] {
			return false
		}
	}
	return true
}

func parseProductionAcceptanceBApproval() (productionAcceptanceBApproval, bool) {
	raw := strings.TrimSpace(os.Getenv(productionAcceptanceBApprovalEnv))
	if raw == "" {
		return productionAcceptanceBApproval{}, false
	}
	var envelope map[string]any
	var approval productionAcceptanceBApproval
	if json.Unmarshal([]byte(raw), &envelope) != nil || !exactWorkspaceComputeClaimKeys(envelope, []string{
		"schemaVersion", "operationMode", "approvalId", "expiresAt", "confirmation", "release", "customer", "launch", "allowedWrites", "forbiddenWrites",
	}) || !exactNestedAcceptanceBApprovalKeys(envelope) || json.Unmarshal([]byte(raw), &approval) != nil {
		return productionAcceptanceBApproval{}, false
	}
	return approval, true
}

func exactNestedAcceptanceBApprovalKeys(envelope map[string]any) bool {
	wants := map[string][]string{
		"release":  {"mergedMainSha", "cloudImageDigest", "workspaceImageDigest"},
		"customer": {"email", "accountId"},
		"launch":   {"idempotencyKey", "operationId", "workspaceId", "name", "packageId", "sizeGb", "autoRenew"},
	}
	for field, want := range wants {
		value, ok := envelope[field].(map[string]any)
		if !ok || !exactWorkspaceComputeClaimKeys(value, want) {
			return false
		}
	}
	return true
}

func secureHeaderMatches(actual, expected string) bool {
	actualBytes, expectedBytes := []byte(actual), []byte(expected)
	return len(actualBytes) == len(expectedBytes) && len(expectedBytes) > 0 && subtle.ConstantTimeCompare(actualBytes, expectedBytes) == 1
}

func deployedImageDigest(value string) string {
	_, digest, ok := strings.Cut(strings.TrimSpace(value), "@")
	if !ok || !workspaceImageDigestPattern.MatchString(digest) {
		return ""
	}
	return digest
}

func productionAcceptanceBLaunchApproved(rHeader http.Header, approval productionAcceptanceBApproval, accountID, ownerEmail, name, packageID string, storageGB int, autoRenew bool, key string) bool {
	expiresAt, expiryErr := time.Parse(time.RFC3339, approval.ExpiresAt)
	canonicalOwnerEmail, ownerEmailErr := canonicalEmail(ownerEmail)
	canonicalApprovedEmail, approvedEmailErr := canonicalEmail(approval.Customer.Email)
	currentCloudDigest := deployedImageDigest(os.Getenv("OPL_CLOUD_IMAGE"))
	currentWorkspaceDigest := deployedImageDigest(os.Getenv("OPL_WORKSPACE_IMAGE"))
	operationID := workspaceLaunchOperationID(accountID, key)
	workspaceID := "ws-" + stableID("workspace-launch-v2", accountID, operationID)[:18]
	internalToken := strings.TrimSpace(os.Getenv("OPL_INTERNAL_SERVICE_TOKEN"))
	header := func(name string) string {
		values := rHeader.Values(name)
		if len(values) != 1 {
			return ""
		}
		return strings.TrimSpace(values[0])
	}
	return expiryErr == nil && time.Now().Before(expiresAt) && ownerEmailErr == nil && approvedEmailErr == nil &&
		approval.SchemaVersion == 1 && approval.OperationMode == "acceptance_b_fresh_order" && approval.Confirmation == productionAcceptanceBConfirmation &&
		productionAcceptanceBApprovalIDPattern.MatchString(approval.ApprovalID) && header(productionAcceptanceBApprovalID) == approval.ApprovalID &&
		secureHeaderMatches(header(productionAcceptanceBCapability), internalToken) &&
		approval.Release.MergedMainSHA == strings.TrimSpace(os.Getenv("OPL_RELEASE_SHA")) && productionAcceptanceBReleaseSHAPattern.MatchString(approval.Release.MergedMainSHA) &&
		approval.Release.CloudImageDigest == currentCloudDigest && approval.Release.WorkspaceImageDigest == currentWorkspaceDigest &&
		canonicalApprovedEmail == canonicalOwnerEmail && approval.Customer.Email == canonicalApprovedEmail && approval.Customer.AccountID == accountID &&
		approval.Launch.IdempotencyKey == key && key == strings.TrimSpace(key) && len(key) >= 8 && len(key) <= 200 && approval.Launch.OperationID == operationID && approval.Launch.WorkspaceID == workspaceID &&
		approval.Launch.Name == name && approval.Launch.PackageID == packageID && approval.Launch.SizeGB == storageGB && approval.Launch.AutoRenew == autoRenew &&
		packageID == "basic" && storageGB == 10 && !autoRenew &&
		exactStringSlice(approval.AllowedWrites, productionAcceptanceBAllowedWrites) && exactStringSlice(approval.ForbiddenWrites, productionAcceptanceBForbiddenWrites)
}

func controlledBasicPilotGlobalInFlightLimit() int {
	return controlledBasicPilotAdmissionFromEnv().MaxInFlight
}

func controlledBasicPilotMetrics(ctx context.Context, store controlPlaneTableStore) (map[string]any, error) {
	admission := controlledBasicPilotAdmissionFromEnv()
	rows, err := queryRuntimeOperations(ctx, store, runtimeOperationQuery{})
	if err != nil {
		return nil, err
	}
	stages, failures := map[string]int{}, map[string]int{}
	inFlight, manualReview := 0, 0
	for _, row := range rows {
		action := stringValue(row["action"])
		if !isWorkspaceLaunchAction(action) {
			continue
		}
		if action == "workspace.launch" {
			if !terminalWorkspaceLaunchStatus(stringValue(row["status"])) {
				inFlight++
				stages["legacy_operation"]++
			}
			continue
		}
		operation, decodeErr := decodeWorkspaceLaunchReconcileOperation(row)
		if decodeErr != nil {
			failures["operation_decode_failed"]++
			continue
		}
		if !terminalWorkspaceLaunchStatus(operation.Status) {
			inFlight++
			stages[safeControlledPilotMetricCode(operation.Stage)]++
			if operation.Status == "manual_review" {
				manualReview++
			}
		}
	}
	availableCapacity := admission.MaxInFlight - inFlight
	if availableCapacity < 0 {
		availableCapacity = 0
	}
	alerts := make([]any, 0, 3)
	if !admission.Configured {
		alerts = append(alerts, map[string]any{"code": "controlled_pilot_config_invalid", "severity": "error", "action": "disable_new_purchases"})
	}
	if len(failures) > 0 || manualReview > 0 {
		alerts = append(alerts, map[string]any{"code": "controlled_pilot_first_failure", "severity": "error", "action": "disable_new_purchases"})
	}
	if inFlight >= admission.MaxInFlight {
		alerts = append(alerts, map[string]any{"code": "controlled_pilot_capacity_exhausted", "severity": "warning", "action": "wait_for_authoritative_terminal_state"})
	}
	sort.Slice(alerts, func(i, j int) bool {
		return stringValue(alerts[i].(map[string]any)["code"]) < stringValue(alerts[j].(map[string]any)["code"])
	})
	return map[string]any{
		"enabled": admission.Enabled, "configured": admission.Configured, "packageId": "basic", "accountAllowlistCount": len(admission.AccountIDs),
		"maxInFlight": admission.MaxInFlight, "inFlight": inFlight, "availableCapacity": availableCapacity, "manualReview": manualReview,
		"stages": stages, "failures": failures, "disableRequired": len(failures) > 0 || manualReview > 0 || !admission.Configured,
		"alerts": alerts,
	}, nil
}

func safeControlledPilotMetricCode(value string) string {
	if value == "" {
		return "unknown"
	}
	for _, char := range value {
		if char != '_' && char != '-' && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return "unknown"
		}
	}
	return value
}
