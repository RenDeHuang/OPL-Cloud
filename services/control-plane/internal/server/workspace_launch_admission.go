package server

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	controlledBasicPilotEnabledEnv     = "OPL_CONTROLLED_BASIC_PILOT_ENABLED"
	controlledBasicPilotAccountsEnv    = "OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS"
	controlledBasicPilotMaxInFlightEnv = "OPL_CONTROLLED_BASIC_PILOT_MAX_IN_FLIGHT"
	controlledBasicPilotDefaultLimit   = 1
)

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
	if !admission.Enabled {
		return "workspace_launch_admission_disabled"
	}
	if packageID != "basic" {
		return "workspace_launch_basic_only"
	}
	if autoRenew {
		return "autoRenew_unavailable"
	}
	if _, ok := admission.AccountIDs[accountID]; !ok {
		return "workspace_launch_account_not_allowed"
	}
	return ""
}

func controlledBasicPilotGlobalInFlightLimit() int {
	return controlledBasicPilotAdmissionFromEnv().MaxInFlight
}

func workspaceLaunchSubmissionMatches(operation workspaceLaunchOperation, accountID, ownerUserID, name, packageID string, storageGB int, autoRenew bool) bool {
	return operation.AccountID == accountID && operation.OwnerUserID == ownerUserID && operation.Name == name && operation.PackageID == packageID &&
		operation.StorageGB == storageGB && operation.AutoRenew == autoRenew
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
		operation, decodeErr := decodeWorkspaceLaunchOperation(row)
		if decodeErr != nil {
			failures["operation_decode_failed"]++
			continue
		}
		if !terminalWorkspaceLaunchStatus(operation.Status) {
			inFlight++
			stages[safeControlledPilotMetricCode(operation.Phase)]++
			if operation.Status == "manual_review" {
				manualReview++
			}
			if operation.ErrorCode != "" {
				failures[safeControlledPilotMetricCode(operation.ErrorCode)]++
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
