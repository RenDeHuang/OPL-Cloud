package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func canonicalProductionAcceptanceBApproval(t *testing.T) map[string]any {
	t.Helper()
	key := "acceptance-b-fresh-basic"
	operationID := workspaceLaunchOperationID("acct-alpha", key)
	return map[string]any{
		"schemaVersion": 1,
		"operationMode": "acceptance_b_fresh_order",
		"approvalId":    "acceptance-b-approval",
		"expiresAt":     "2099-08-05T00:00:00Z",
		"confirmation":  productionAcceptanceBConfirmation,
		"release": map[string]any{
			"mergedMainSha":        strings.Repeat("a", 40),
			"cloudImageDigest":     "sha256:" + strings.Repeat("b", 64),
			"workspaceImageDigest": "sha256:" + strings.Repeat("c", 64),
		},
		"customer": map[string]any{
			"email":     "alpha@example.com",
			"accountId": "acct-alpha",
		},
		"launch": map[string]any{
			"idempotencyKey": key,
			"operationId":    operationID,
			"workspaceId":    "ws-" + stableID("workspace-launch-v2", "acct-alpha", operationID)[:18],
			"name":           "Acceptance B Basic Workspace",
			"packageId":      "basic",
			"sizeGb":         10,
			"autoRenew":      false,
		},
		"expected": map[string]any{
			"nodePoolId":           "np-basic-acceptance",
			"resolvedInstanceType": "SA5.MEDIUM4",
		},
		"allowedWrites": []string{
			"submit_one_workspace_launch",
			"debit_one_basic_month",
			"create_one_workspace_key",
			"create_one_cvm",
			"claim_one_cvm_ownership",
			"claim_one_node",
			"create_one_cbs",
			"create_one_attachment",
			"upsert_one_gateway_secret",
			"create_one_runtime",
			"activate_one_workspace",
			"record_one_purchase_receipt",
		},
		"forbiddenWrites": []string{
			"provision_account",
			"adjust_wallet",
			"submit_second_workspace_launch",
			"create_second_cvm",
			"create_second_cbs",
			"refund",
			"renew",
			"delete",
			"replace",
			"send_model_request",
		},
	}
}

func parseProductionAcceptanceBApprovalFixture(t *testing.T, fixture map[string]any) (productionAcceptanceBApproval, bool) {
	t.Helper()
	encoded, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(productionAcceptanceBApprovalEnv, string(encoded))
	return parseProductionAcceptanceBApproval()
}

func productionAcceptanceBHeaders() http.Header {
	header := http.Header{}
	header.Set(productionAcceptanceBApprovalID, "acceptance-b-approval")
	header.Set(productionAcceptanceBCapability, "acceptance-b-capability")
	return header
}

func configureProductionAcceptanceBEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "acceptance-b-capability")
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	t.Setenv("OPL_WORKSPACE_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app@sha256:"+strings.Repeat("c", 64))
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_ID", "np-basic-acceptance")
	t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "SA5.MEDIUM4")
}

func productionAcceptanceBFixtureApproved(header http.Header, approval productionAcceptanceBApproval) bool {
	return productionAcceptanceBLaunchApproved(
		header,
		approval,
		"acct-alpha",
		"alpha@example.com",
		"Acceptance B Basic Workspace",
		"basic",
		10,
		false,
		"acceptance-b-fresh-basic",
	)
}

func TestProductionAcceptanceBAdmissionMatchesCanonicalProductApproval(t *testing.T) {
	configureProductionAcceptanceBEnvironment(t)

	approval, ok := parseProductionAcceptanceBApprovalFixture(t, canonicalProductionAcceptanceBApproval(t))
	if !ok {
		t.Fatal("canonical product approval did not pass exact Control Plane parsing")
	}
	if !productionAcceptanceBFixtureApproved(productionAcceptanceBHeaders(), approval) {
		t.Fatal("canonical product approval was not admitted")
	}
}

func TestProductionAcceptanceBApprovalMatchesDeployedTargetWithoutRequestHeaders(t *testing.T) {
	configureProductionAcceptanceBEnvironment(t)
	approval, ok := parseProductionAcceptanceBApprovalFixture(t, canonicalProductionAcceptanceBApproval(t))
	if !ok {
		t.Fatal("canonical product approval did not parse")
	}
	if !productionAcceptanceBDeploymentApproved(approval, "acct-alpha", "alpha@example.com", time.Now()) {
		t.Fatal("canonical approval was not bound to the deployed target")
	}

	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "release SHA", mutate: func(value map[string]any) {
			value["release"].(map[string]any)["mergedMainSha"] = strings.Repeat("d", 40)
		}},
		{name: "cloud image", mutate: func(value map[string]any) {
			value["release"].(map[string]any)["cloudImageDigest"] = "sha256:" + strings.Repeat("d", 64)
		}},
		{name: "workspace image", mutate: func(value map[string]any) {
			value["release"].(map[string]any)["workspaceImageDigest"] = "sha256:" + strings.Repeat("d", 64)
		}},
		{name: "customer", mutate: func(value map[string]any) { value["customer"].(map[string]any)["accountId"] = "acct-other" }},
		{name: "operation", mutate: func(value map[string]any) { value["launch"].(map[string]any)["operationId"] = "workspace-launch-other" }},
		{name: "workspace", mutate: func(value map[string]any) { value["launch"].(map[string]any)["workspaceId"] = "ws-other" }},
		{name: "expired", mutate: func(value map[string]any) { value["expiresAt"] = "2000-01-01T00:00:00Z" }},
		{name: "allowed writes", mutate: func(value map[string]any) { value["allowedWrites"].([]string)[0] = "submit_two_workspace_launches" }},
		{name: "forbidden writes", mutate: func(value map[string]any) { value["forbiddenWrites"].([]string)[0] = "adjust_wallet_twice" }},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := canonicalProductionAcceptanceBApproval(t)
			testCase.mutate(fixture)
			candidate, parsed := parseProductionAcceptanceBApprovalFixture(t, fixture)
			if !parsed {
				t.Fatal("structurally valid approval drift did not parse")
			}
			if productionAcceptanceBDeploymentApproved(candidate, "acct-alpha", "alpha@example.com", time.Now()) {
				t.Fatal("drifted approval was bound to the deployed target")
			}
		})
	}
}

func TestProductionAcceptanceBAdmissionRejectsApprovalDrift(t *testing.T) {
	configureProductionAcceptanceBEnvironment(t)

	parseMutations := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "extra top-level key", mutate: func(fixture map[string]any) { fixture["unexpected"] = true }},
		{name: "missing expected", mutate: func(fixture map[string]any) { delete(fixture, "expected") }},
		{name: "extra expected key", mutate: func(fixture map[string]any) { fixture["expected"].(map[string]any)["unexpected"] = true }},
	}
	for _, testCase := range parseMutations {
		t.Run("parse/"+testCase.name, func(t *testing.T) {
			fixture := canonicalProductionAcceptanceBApproval(t)
			testCase.mutate(fixture)
			if _, ok := parseProductionAcceptanceBApprovalFixture(t, fixture); ok {
				t.Fatal("drifted approval passed exact parsing")
			}
		})
	}

	approvalMutations := []struct {
		name   string
		mutate func(*testing.T, map[string]any, http.Header)
	}{
		{name: "expired", mutate: func(_ *testing.T, fixture map[string]any, _ http.Header) {
			fixture["expiresAt"] = "2000-01-01T00:00:00Z"
		}},
		{name: "release SHA", mutate: func(_ *testing.T, fixture map[string]any, _ http.Header) {
			fixture["release"].(map[string]any)["mergedMainSha"] = strings.Repeat("d", 40)
		}},
		{name: "cloud image", mutate: func(_ *testing.T, fixture map[string]any, _ http.Header) {
			fixture["release"].(map[string]any)["cloudImageDigest"] = "sha256:" + strings.Repeat("d", 64)
		}},
		{name: "workspace image", mutate: func(_ *testing.T, fixture map[string]any, _ http.Header) {
			fixture["release"].(map[string]any)["workspaceImageDigest"] = "sha256:" + strings.Repeat("d", 64)
		}},
		{name: "customer", mutate: func(_ *testing.T, fixture map[string]any, _ http.Header) {
			fixture["customer"].(map[string]any)["accountId"] = "acct-other"
		}},
		{name: "launch operation identity", mutate: func(_ *testing.T, fixture map[string]any, _ http.Header) {
			fixture["launch"].(map[string]any)["operationId"] = "workspace-launch-other"
		}},
		{name: "node pool", mutate: func(_ *testing.T, fixture map[string]any, _ http.Header) {
			fixture["expected"].(map[string]any)["nodePoolId"] = "np-other"
		}},
		{name: "instance type", mutate: func(_ *testing.T, fixture map[string]any, _ http.Header) {
			fixture["expected"].(map[string]any)["resolvedInstanceType"] = "SA5.LARGE8"
		}},
		{name: "approval header", mutate: func(_ *testing.T, _ map[string]any, header http.Header) {
			header.Set(productionAcceptanceBApprovalID, "acceptance-b-other")
		}},
		{name: "duplicate approval header", mutate: func(_ *testing.T, _ map[string]any, header http.Header) {
			header.Add(productionAcceptanceBApprovalID, "acceptance-b-approval")
		}},
		{name: "capability", mutate: func(_ *testing.T, _ map[string]any, header http.Header) {
			header.Set(productionAcceptanceBCapability, "wrong-capability")
		}},
		{name: "package", mutate: func(_ *testing.T, fixture map[string]any, _ http.Header) {
			fixture["launch"].(map[string]any)["packageId"] = "pro"
		}},
		{name: "size", mutate: func(_ *testing.T, fixture map[string]any, _ http.Header) {
			fixture["launch"].(map[string]any)["sizeGb"] = 20
		}},
		{name: "renewal", mutate: func(_ *testing.T, fixture map[string]any, _ http.Header) {
			fixture["launch"].(map[string]any)["autoRenew"] = true
		}},
		{name: "allowed writes", mutate: func(_ *testing.T, fixture map[string]any, _ http.Header) {
			fixture["allowedWrites"].([]string)[3] = "ensure_one_compute_allocation"
		}},
		{name: "forbidden writes", mutate: func(_ *testing.T, fixture map[string]any, _ http.Header) {
			fixture["forbiddenWrites"].([]string)[3] = "create_second_compute_allocation"
		}},
	}
	for _, testCase := range approvalMutations {
		t.Run("approval/"+testCase.name, func(t *testing.T) {
			fixture := canonicalProductionAcceptanceBApproval(t)
			header := productionAcceptanceBHeaders()
			testCase.mutate(t, fixture, header)
			approval, ok := parseProductionAcceptanceBApprovalFixture(t, fixture)
			if !ok {
				t.Fatal("structurally valid drift fixture did not parse")
			}
			if productionAcceptanceBFixtureApproved(header, approval) {
				t.Fatal("drifted approval was admitted")
			}
		})
	}
}
