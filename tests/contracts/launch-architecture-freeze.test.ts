import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);

async function text(path) {
  return readFile(new URL(path, root), "utf8");
}

async function json(path) {
  return JSON.parse(await text(path));
}

test("root agent instructions require the launch invariants", async () => {
  const [agents, gitignore] = await Promise.all([text("AGENTS.md"), text(".gitignore")]);

  assert.match(agents, /docs\/invariants\.md/);
  assert.match(agents, /Before changing billing, Fabric, Workspace, Gateway, Ledger, deployment, or E2E/i);
  assert.match(gitignore, /^\.codegraph\/$/m);
});

test("launch freeze fixes the V2 products, owner lanes, settlement, and verification slot", async () => {
  const freeze = await json("packages/contracts/opl-cloud-launch-freeze-contract.json");

  assert.equal(freeze.architectureAuthority.repository, "https://github.com/gaofeng21cn/one-person-lab-cloud");
  assert.equal(freeze.architectureAuthority.reviewedRevision, "c349a41d860e706ed43a4090b9e75abb0b130971");
  assert.deepEqual(Object.keys(freeze.productSurfaces), ["gateway", "workspace", "serve", "console", "fabric", "ledger"]);
  assert.deepEqual(freeze.productSurfaces.serve, { product: "OPL Serve", launchStatus: "planned_not_in_launch" });
  assert.match(freeze.machineBoundary, /Six product surfaces.*OPL Serve.*planned_not_in_launch/);
  assert.match(freeze.machineBoundary, /two guarded production verification slots/i);
  assert.deepEqual(Object.keys(freeze.ownerLanes), ["console", "fabric", "gateway", "ledger"]);
  assert.deepEqual(freeze.customerProducts.basic, {
    priceVersion: "pilot-usd-2026-07-v1",
    currency: "USD",
    compute: { cpu: 2, memoryGb: 4, usdMicros: 50000000 },
    storage: { sizeGb: 10, usdMicros: 2580000 },
    totalUsdMicros: 52580000,
    targetSaleable: true
  });
  assert.deepEqual(freeze.customerProducts.pro, {
    priceVersion: "pilot-usd-2026-07-v1",
    currency: "USD",
    compute: { cpu: 8, memoryGb: 16, usdMicros: 214280000 },
    storage: { sizeGb: 100, usdMicros: 25800000 },
    totalUsdMicros: 240080000,
    targetSaleable: true
  });
  assert.deepEqual(freeze.internalProviderCostEvidence, {
    currency: "CNY",
    computeMonthlyCnyCents: { basic: 35000, pro: 150000 },
    storageMonthlyCnyCents: { "10": 1800, "100": 18000 },
    customerChargeDerivation: "forbidden"
  });
  assert.equal(freeze.workspaceLaunch.priceVersion, "pilot-usd-2026-07-v1");
  assert.equal(freeze.workspaceLaunch.currency, "USD");
  assert.deepEqual(freeze.workspaceLaunch.requiredCreateFields, ["name", "packageId", "sizeGb", "autoRenew"]);
  assert.deepEqual(freeze.workspaceLaunch.validPackageStoragePairs, [
    { packageId: "basic", sizeGb: 10 },
    { packageId: "pro", sizeGb: 100 }
  ]);
  assert.deepEqual(freeze.workspaceLaunch.requestHashFields, ["accountId", "ownerUserId", "name", "packageId", "sizeGb", "autoRenew", "priceVersion"]);
  assert.equal(freeze.workspaceLaunch.autoRenew.submission, "required_boolean");
  assert.equal(freeze.workspaceLaunch.autoRenew.defaultProductIntent, false);
  assert.equal(freeze.workspaceLaunch.autoRenew.childProjection, "read_only_compute_and_storage_compatibility_until_workspace_canonical_state");
  assert.deepEqual(freeze.workspaceLaunch.customerResponsePricingFields, ["priceVersion", "currency", "totalChargeUsdMicros"]);
  assert.deepEqual(freeze.workspaceLaunch.manualReviewRecovery, {
    stillInReview: "visible_no_side_effects",
    resolvedActive: "resume_same_child_phase_receipt_only_safe",
    resolvedRefunded: "parent_refunded_terminal",
    resolvedFailed: "parent_failed_terminal",
    nonChildReview: "quiescent_operator_only"
  });

  assert.deepEqual(freeze.monthlySettlement.protocol, ["debit", "provision", "claim", "activate"]);
  assert.equal(freeze.monthlySettlement.confirmedNoResourceAfterDebit, "idempotent_refund");
  assert.equal(freeze.monthlySettlement.partialOrUnknownProviderResult, "manual_review_without_refund");
  assert.equal(freeze.providerProcurement.chargeType, "PREPAID");
  assert.equal(freeze.providerProcurement.periodMonths, 1);
  assert.equal(freeze.providerProcurement.renewFlag, "NOTIFY_AND_MANUAL_RENEW");
  assert.deepEqual(freeze.providerProcurement.forbiddenChargeTypes, ["POSTPAID_BY_HOUR"]);
  assert.equal(freeze.workspaceRuntime.sourceImage.digest, "sha256:9d867fe0fc9db48b6efa27371d77770e46fc8cd97d26ef85a81fbdac7e96ca76");
  assert.equal(freeze.workspaceRuntime.primaryWorkspacePerAccount, 1);
  assert.equal(freeze.workspaceRuntime.statusContainsPassword, false);
  assert.equal(freeze.workspaceRuntime.runtimeRequestIdentityBinding, "not_in_pilot_requires_sso");
  assert.deepEqual(freeze.workspaceRuntime.credentialCommands, {
    reveal: "POST /api/workspaces/{workspaceId}/runtime-credentials/reveal",
    rotate: "POST /api/workspaces/{workspaceId}/runtime-credentials/rotate",
    authorization: "workspace.ownerUserId_equals_session_user.id",
    cacheControl: "private, no-store",
    passwordPersistence: "none",
    rotationReceiptType: "workspace.access_token_reset"
  });
  assert.equal(freeze.gateway.sub2apiMutable, false);
  assert.equal(freeze.gateway.backend, "Sub2API");
  assert.equal(freeze.gateway.compatibilityGate, "required_capabilities");
  assert.equal(freeze.gateway.versionRole, "diagnostic_only");
  assert.equal("compatibleVersions" in freeze.gateway, false);
  assert.deepEqual(freeze.gateway.compatibilityEvidence, ["contract_tests", "read_only_production_probe"]);
  assert.equal(freeze.productSurfaces.gateway.backend, freeze.gateway.backend);
  assert.equal(freeze.gateway.keyName, "opl-workspace");
  assert.deepEqual(freeze.gateway.usageScope, ["user_id", "api_key_id"]);
  assert.equal(freeze.gateway.usageMoneySource, "actual_cost");
  assert.equal(freeze.gateway.usageMoneyRepresentation, "integer_usd_micros");
  assert.equal(freeze.gateway.rawAdminDTOForwarding, false);
  assert.equal(freeze.gateway.missingCapabilityBehavior, "dependent_surface_unavailable_never_zero");
  assert.equal(freeze.consoleFinancialProjection.mode, "read_only_projection");
  assert.deepEqual(freeze.consoleFinancialProjection.authorities, {
    balanceApiKeysAndRequestUsage: "Sub2API",
    resourceBillingHistory: "Ledger receipts",
    workspaceAndEntitlements: "Control Plane"
  });
  assert.deepEqual(freeze.consoleFinancialProjection.prohibitions, [
    "second_wallet",
    "usage_database",
    "billing_fact_table",
    "raw_admin_dto",
    "browser_to_sub2api",
    "prompt_or_response_content"
  ]);

  assert.deepEqual(freeze.verification.slots, [
    {
      id: "verification-slot-basic-01", packageId: "basic", computeInstanceType: "SA5.MEDIUM4",
      resources: { cpu: 2, memoryGb: 4, cbsGb: 10 }, customerProduct: false,
      chargeType: "PREPAID", periodMonths: 1, renewFlag: "NOTIFY_AND_MANUAL_RENEW", reuseForBillingPeriod: true, concurrency: 1
    },
    {
      id: "verification-slot-pro-01", packageId: "pro", computeInstanceType: "SA5.2XLARGE16",
      resources: { cpu: 8, memoryGb: 16, cbsGb: 100 }, customerProduct: false,
      chargeType: "PREPAID", periodMonths: 1, renewFlag: "NOTIFY_AND_MANUAL_RENEW", reuseForBillingPeriod: true, concurrency: 1
    }
  ]);
  assert.equal(freeze.verification.purchaseBudget, 2);
  assert.equal("purchaseBudgetRemaining" in freeze.verification, false);
  assert.deepEqual(freeze.verification.providerAcceptance, {
    operatorOnly: true,
    approvalEnvironment: "production-provider-acceptance",
    credentialEnv: "OPL_PROVIDER_ACCEPTANCE_TOKEN",
    credentialHeader: "x-opl-provider-acceptance-token",
    operatorSessionAccepted: false,
    genericOperatorTokenAccepted: false,
    operationCardinality: 2,
    operationCardinalityPerSlot: 1,
    fixedSlotOperationReplayable: true,
    slotExistenceSource: ["workspace", "compute", "storage"]
  });
  assert.equal(freeze.verification.perRunTencentPurchase, false);
  assert.equal(freeze.verification.monthlyBillingBackend, "fake");
  assert.equal(freeze.verification.gatewayRequest, "real_dedicated_test_key");
  assert.deepEqual(freeze.verification.providerResourcesDeletedPerRun, []);
  assert.equal(freeze.launchStages.length, 10);
  assert.equal("slides" in freeze, false);
  assert.equal(freeze.deliveryPhases.length, 6);
});

test("human launch contract pins the approved architecture authority revision", async () => {
  const [freeze, invariants] = await Promise.all([
    json("packages/contracts/opl-cloud-launch-freeze-contract.json"),
    text("docs/invariants.md")
  ]);

  assert.match(invariants, new RegExp(freeze.architectureAuthority.reviewedRevision));
});

test("public Workspace contract permits one primary Workspace only", async () => {
  const readme = await text("README.md");

  assert.match(readme, /one account owns exactly one\s+primary Workspace/i);
  assert.match(readme, /second Workspace.*409/i);
  assert.doesNotMatch(readme, /one account can create\s+multiple Workspaces/i);
});

test("every launch stage declares business, current state, deliverables, and evidence", async () => {
  const freeze = await json("packages/contracts/opl-cloud-launch-freeze-contract.json");
  const expected = [
    "offer_identity",
    "wallet_quote",
    "balance_debit",
    "prepaid_fulfillment",
    "claim_and_activate",
    "workspace_access",
    "gateway_usage",
    "renewal_expiry_recovery",
    "reusable_verification",
    "production_release"
  ];

  assert.deepEqual(freeze.launchStages.map((stage) => stage.id), expected);
  for (const stage of freeze.launchStages) {
    assert.ok(stage.business, `${stage.id} business`);
    assert.ok(Array.isArray(stage.owners) && stage.owners.length > 0, `${stage.id} owners`);
    assert.ok(stage.currentState, `${stage.id} currentState`);
    assert.ok(Array.isArray(stage.requiredDeliverables) && stage.requiredDeliverables.length > 0, `${stage.id} requiredDeliverables`);
    assert.ok(Array.isArray(stage.completionEvidence) && stage.completionEvidence.length > 0, `${stage.id} completionEvidence`);
  }
});

test("human invariants reject paid per-run resource verification", async () => {
  const invariants = await text("docs/invariants.md");

  for (const heading of ["Console", "Fabric", "Ledger", "Gateway", "Launch Stages", "Verification Slot"]) {
    assert.match(invariants, new RegExp(`## ${heading}`));
  }
  assert.match(invariants, /SA5\.MEDIUM4/);
  assert.match(invariants, /debit.*provision.*claim.*activate/is);
  assert.match(invariants, /confirmed.*no billable resource.*refund/is);
  assert.match(invariants, /POSTPAID_BY_HOUR.*forbidden/is);
  assert.doesNotMatch(invariants, /monthly settlement requires Sub2API-owned `reserve`/i);
  assert.match(invariants, /version is diagnostic metadata and never blocks/i);
  assert.match(invariants, /actual_cost.*integer USD micros/is);
  assert.match(invariants, /Raw Sub2API admin responses.*never reach Console/is);
  assert.doesNotMatch(invariants, /Cloud accepts the API-compatible v0\.1\./i);
  assert.doesNotMatch(invariants, /Production E2E requires explicit confirmation that it spends real balance/);
  assert.doesNotMatch(invariants, /Fabric prepares before charge/);
});

test("read-only fixed-slot verification replaces the legacy paid release gate", async () => {
  const deployment = await json("packages/contracts/opl-cloud-deployment-contract.json");
  const [architecture, decisions, project, readme, runbook, status] = await Promise.all([
    text("docs/architecture.md"),
    text("docs/decisions.md"),
    text("docs/project.md"),
    text("README.md"),
    text("docs/runtime/production-runbook.md"),
    text("docs/status.md")
  ]);

  assert.equal(deployment.productionVerificationWorkflow.launchStatus, "active");
  assert.equal(deployment.productionVerificationWorkflow.releaseGate, false);
  assert.equal(deployment.productionVerificationWorkflow.mode, "read_only_dual_fixed_slots");
  assert.equal(deployment.productionLiveQaJob.releaseGate, true);
  assert.equal(deployment.productionLiveQaJob.mode, "one_model_request_per_fixed_slot_no_provider_mutation");
  assert.doesNotMatch(JSON.stringify(deployment), /paid_confirmation|OPL_VERIFY_PAID_CONFIRMATION|OPL_VERIFY_MODEL_ACCESS_KEY/);
  assert.equal(deployment.workspaceImage.sourceDigest, "sha256:9d867fe0fc9db48b6efa27371d77770e46fc8cd97d26ef85a81fbdac7e96ca76");
  assert.equal(deployment.workspaceImage.productionReference, "repository@sha256");
  assert.match(runbook, /Do not run the legacy paid verifier/);
  assert.match(architecture, /debit.*before\s+Fabric.*activate/is);
  assert.doesNotMatch(architecture, /Fabric preparation happens before the external charge/);
  for (const document of [architecture, decisions, project, readme, status]) {
    assert.doesNotMatch(document, /single paid verifier|one paid production verifier|explicitly confirmed paid E2E/i);
  }
});
