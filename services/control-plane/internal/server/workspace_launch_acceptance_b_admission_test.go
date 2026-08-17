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

func productionAcceptanceBResumeHeaders() http.Header {
	header := productionAcceptanceBHeaders()
	header.Set(productionAcceptanceBApprovalID, "acceptance-b-resume-existing-approval")
	return header
}

func configureProductionAcceptanceBEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "acceptance-b-capability")
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_RELEASE_TREE", strings.Repeat("d", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	t.Setenv("OPL_WORKSPACE_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app@sha256:"+strings.Repeat("c", 64))
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_ID", "np-basic-acceptance")
	t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "SA5.MEDIUM4")
}

func canonicalProductionAcceptanceBResumeExistingApproval(operation workspaceLaunchReconcileOperation, authorization workspaceLaunchResumeAuthorization, authoritativeState string) map[string]any {
	attempt := operation.Attempts[authorization.AuthorizedStage]
	digests := workspaceLaunchAcceptanceBIdentityDigests(operation)
	return map[string]any{
		"schemaVersion": 1,
		"operationMode": "acceptance_b_resume_existing",
		"approvalId":    "acceptance-b-resume-existing-approval",
		"expiresAt":     "2099-08-16T00:00:00Z",
		"release": map[string]any{
			"canonicalCloudSha":        strings.Repeat("a", 40),
			"canonicalCloudTree":       strings.Repeat("d", 40),
			"deployedCloudImageDigest": "sha256:" + strings.Repeat("b", 64),
		},
		"authorization": map[string]any{
			"authorizationId": authorization.AuthorizationID, "operationId": operation.ID,
			"launchVersion": authorization.LaunchVersion, "authorizedStage": authorization.AuthorizedStage,
			"reasonSha256":   acceptanceBDigestParts(authorization.Reason),
			"mutationBudget": authorization.MutationBudget, "idempotentReplayBudget": authorization.IdempotentReplayBudget,
			"authoritativeReadBudget": authorization.AuthoritativeReadBudget,
		},
		"reconciliation": map[string]any{
			"operationStatus": operation.Status, "authoritativeStageState": authoritativeState,
			"attempt": map[string]any{
				"attempted": attempt.Attempted, "confirmed": attempt.Confirmed, "unknown": attempt.Unknown, "max": attempt.Max,
				"status": attempt.Status, "idempotencyKeySha256": acceptanceBDigestParts(attempt.IdempotencyKey),
			},
		},
		"identityDigests": map[string]any{
			"accountIdentitySha256": digests.AccountIdentitySHA256, "operationIdentitySha256": digests.OperationIdentitySHA256,
			"workspaceIdentitySha256": digests.WorkspaceIdentitySHA256, "keyIdentitySha256": digests.KeyIdentitySHA256,
			"debitIdentitySha256": digests.DebitIdentitySHA256, "quoteIdentitySha256": digests.QuoteIdentitySHA256,
			"providerIdentitySha256": digests.ProviderIdentitySHA256,
		},
	}
}

func parseProductionAcceptanceBResumeExistingApprovalFixture(t *testing.T, fixture map[string]any) (productionAcceptanceBResumeExistingApproval, bool) {
	t.Helper()
	encoded, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(productionAcceptanceBResumeExistingApprovalEnv, string(encoded))
	return parseProductionAcceptanceBResumeExistingApproval()
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

func TestProductionAcceptanceBResumeExistingApprovalBindsServerAuthority(t *testing.T) {
	configureProductionAcceptanceBEnvironment(t)
	operation, err := newWorkspaceLaunchReconcileOperation(workspaceLaunchUnitCommand())
	if err != nil {
		t.Fatal(err)
	}
	operation.Status, operation.Stage = "manual_review", "debit"
	attempt := operation.Attempts["debit"]
	attempt.Attempted, attempt.Status, attempt.IdempotencyKey = 1, "reserved", workspaceLaunchStageIdempotencyKey(operation, 1)
	operation.Attempts["debit"] = attempt
	authorization := workspaceLaunchResumeAuthorization{
		AuthorizationID: "acceptance-b-resume-existing", LaunchVersion: operation.Version, AuthorizedStage: "debit",
		AuthorizedBy: "reserved-admin", AuthorizedAt: "2026-08-16T00:00:00Z", Reason: "exact owner absence",
		MutationBudget: 0, IdempotentReplayBudget: 1, AuthoritativeReadBudget: workspaceLaunchAuthoritativeReadBudget,
	}
	fixture := canonicalProductionAcceptanceBResumeExistingApproval(operation, authorization, workspaceLaunchStageAbsent)
	approval, ok := parseProductionAcceptanceBResumeExistingApprovalFixture(t, fixture)
	if !ok {
		t.Fatal("canonical resume-existing approval did not parse")
	}
	binding, approved := productionAcceptanceBResumeExistingApproved(
		productionAcceptanceBResumeHeaders(), approval, authorization, operation,
		workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}, time.Now(),
	)
	if !approved || binding.ApprovalID != "acceptance-b-resume-existing-approval" || binding.CanonicalCloudTree != strings.Repeat("d", 40) ||
		binding.IdentityDigests != workspaceLaunchAcceptanceBIdentityDigests(operation) || binding.AuthoritativeState != workspaceLaunchStageAbsent {
		t.Fatalf("canonical resume-existing approval not bound: approved=%v binding=%#v", approved, binding)
	}

	for _, mutate := range []func(map[string]any){
		func(value map[string]any) {
			value["release"].(map[string]any)["canonicalCloudSha"] = strings.Repeat("e", 40)
		},
		func(value map[string]any) {
			value["authorization"].(map[string]any)["launchVersion"] = operation.Version + 1
		},
		func(value map[string]any) {
			value["authorization"].(map[string]any)["reasonSha256"] = strings.Repeat("f", 64)
		},
		func(value map[string]any) {
			value["reconciliation"].(map[string]any)["authoritativeStageState"] = workspaceLaunchStageReady
		},
	} {
		drifted := canonicalProductionAcceptanceBResumeExistingApproval(operation, authorization, workspaceLaunchStageAbsent)
		mutate(drifted)
		candidate, parsed := parseProductionAcceptanceBResumeExistingApprovalFixture(t, drifted)
		if !parsed {
			continue
		}
		if _, admitted := productionAcceptanceBResumeExistingApproved(
			productionAcceptanceBResumeHeaders(), candidate, authorization, operation,
			workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}, time.Now(),
		); admitted {
			t.Fatal("drifted resume-existing approval was admitted")
		}
	}
	for _, field := range []string{
		"accountIdentitySha256", "operationIdentitySha256", "workspaceIdentitySha256", "keyIdentitySha256",
		"debitIdentitySha256", "quoteIdentitySha256", "providerIdentitySha256",
	} {
		t.Run("identity drift/"+field, func(t *testing.T) {
			drifted := canonicalProductionAcceptanceBResumeExistingApproval(operation, authorization, workspaceLaunchStageAbsent)
			drifted["identityDigests"].(map[string]any)[field] = strings.Repeat("f", 64)
			candidate, parsed := parseProductionAcceptanceBResumeExistingApprovalFixture(t, drifted)
			if !parsed {
				t.Fatal("structurally valid identity drift did not parse")
			}
			if _, admitted := productionAcceptanceBResumeExistingApproved(
				productionAcceptanceBResumeHeaders(), candidate, authorization, operation,
				workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}, time.Now(),
			); admitted {
				t.Fatalf("%s drift was admitted", field)
			}
		})
	}
	if _, admitted := productionAcceptanceBResumeExistingApproved(
		productionAcceptanceBResumeHeaders(), approval, authorization, operation,
		workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, time.Now(),
	); admitted {
		t.Fatal("unknown owner observation was admitted")
	}
	driftedAuthorization := authorization
	driftedAuthorization.Reason = "different operator reason"
	if _, admitted := productionAcceptanceBResumeExistingApproved(
		productionAcceptanceBResumeHeaders(), approval, driftedAuthorization, operation,
		workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}, time.Now(),
	); admitted {
		t.Fatal("request reason drift was admitted")
	}
}

func TestProductionAcceptanceBResumeExistingRequestModeUsesDedicatedHeadersOnly(t *testing.T) {
	t.Setenv(productionAcceptanceBResumeExistingApprovalEnv, `{"configured":true}`)
	requested, valid := productionAcceptanceBResumeExistingRequestMode(http.Header{})
	if requested || !valid {
		t.Fatalf("server secret configuration selected dedicated mode: requested=%v valid=%v", requested, valid)
	}

	for name, configure := range map[string]func(http.Header){
		"approval only": func(header http.Header) {
			header.Set(productionAcceptanceBApprovalID, "approval-id")
		},
		"capability only": func(header http.Header) {
			header.Set(productionAcceptanceBCapability, "capability")
		},
		"duplicate approval": func(header http.Header) {
			header.Add(productionAcceptanceBApprovalID, "approval-id")
			header.Add(productionAcceptanceBApprovalID, "approval-id")
			header.Set(productionAcceptanceBCapability, "capability")
		},
		"duplicate capability": func(header http.Header) {
			header.Set(productionAcceptanceBApprovalID, "approval-id")
			header.Add(productionAcceptanceBCapability, "capability")
			header.Add(productionAcceptanceBCapability, "capability")
		},
	} {
		t.Run(name, func(t *testing.T) {
			header := http.Header{}
			configure(header)
			requested, valid := productionAcceptanceBResumeExistingRequestMode(header)
			if !requested || valid {
				t.Fatalf("invalid dedicated header shape: requested=%v valid=%v header=%#v", requested, valid, header)
			}
		})
	}
	requested, valid = productionAcceptanceBResumeExistingRequestMode(productionAcceptanceBResumeHeaders())
	if !requested || !valid {
		t.Fatalf("exact dedicated header pair rejected: requested=%v valid=%v", requested, valid)
	}
}

func TestParseProductionAcceptanceBResumeExistingApprovalRejectsDuplicateKeys(t *testing.T) {
	configureProductionAcceptanceBEnvironment(t)
	operation, err := newWorkspaceLaunchReconcileOperation(workspaceLaunchUnitCommand())
	if err != nil {
		t.Fatal(err)
	}
	operation.Status, operation.Stage = "manual_review", "debit"
	authorization := workspaceLaunchResumeAuthorization{
		AuthorizationID: "duplicate-key-authorization", LaunchVersion: operation.Version, AuthorizedStage: "debit",
		AuthorizedBy: "operator", AuthorizedAt: "2026-08-16T00:00:00Z", Reason: "exact owner absence",
		IdempotentReplayBudget: 1, AuthoritativeReadBudget: workspaceLaunchAuthoritativeReadBudget,
	}
	encoded, err := json.Marshal(canonicalProductionAcceptanceBResumeExistingApproval(operation, authorization, workspaceLaunchStageAbsent))
	if err != nil {
		t.Fatal(err)
	}
	replacements := map[string][2]string{
		"top level":        {`"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`},
		"release":          {`"canonicalCloudSha":"` + strings.Repeat("a", 40) + `"`, `"canonicalCloudSha":"` + strings.Repeat("a", 40) + `","canonicalCloudSha":"` + strings.Repeat("a", 40) + `"`},
		"authorization":    {`"authorizationId":"duplicate-key-authorization"`, `"authorizationId":"duplicate-key-authorization","authorizationId":"duplicate-key-authorization"`},
		"reconciliation":   {`"operationStatus":"manual_review"`, `"operationStatus":"manual_review","operationStatus":"manual_review"`},
		"attempt":          {`"attempted":0`, `"attempted":0,"attempted":0`},
		"identity digests": {`"accountIdentitySha256":"`, `"accountIdentitySha256":"`},
	}
	for name, replacement := range replacements {
		t.Run(name, func(t *testing.T) {
			raw := string(encoded)
			if name == "identity digests" {
				marker := `"accountIdentitySha256":"`
				start := strings.Index(raw, marker)
				if start < 0 {
					t.Fatal("identity digest marker missing")
				}
				valueStart := start + len(marker)
				valueEnd := strings.Index(raw[valueStart:], `"`)
				if valueEnd < 0 {
					t.Fatal("identity digest value incomplete")
				}
				field := raw[start : valueStart+valueEnd+1]
				raw = strings.Replace(raw, field, field+","+field, 1)
			} else {
				if !strings.Contains(raw, replacement[0]) {
					t.Fatalf("duplicate-key marker missing: %s", replacement[0])
				}
				raw = strings.Replace(raw, replacement[0], replacement[1], 1)
			}
			t.Setenv(productionAcceptanceBResumeExistingApprovalEnv, raw)
			if _, ok := parseProductionAcceptanceBResumeExistingApproval(); ok {
				t.Fatal("duplicate JSON key was accepted")
			}
		})
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
