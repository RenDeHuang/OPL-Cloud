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
  assert.equal(freeze.architectureAuthority.reviewedRevision, "fdeb0e4df3e4905fb1c3551337b9dfda65bb2119");
  assert.deepEqual(Object.keys(freeze.productSurfaces), ["gateway", "workspace", "console", "fabric", "ledger"]);
  assert.deepEqual(Object.keys(freeze.ownerLanes), ["console", "fabric", "gateway", "ledger"]);
  assert.deepEqual(freeze.customerProducts.basic, {
    compute: { cpu: 2, memoryGb: 4, cnyCents: 35000, usdMicros: 50000000 },
    storage: { sizeGb: 10, cnyCents: 1800, usdMicros: 2571429 },
    targetSaleable: true
  });
  assert.deepEqual(freeze.customerProducts.pro, {
    compute: { cpu: 8, memoryGb: 16, cnyCents: 150000, usdMicros: 214285715 },
    storage: { sizeGb: 100, cnyCents: 18000, usdMicros: 25714286 },
    targetSaleable: true
  });

  assert.deepEqual(freeze.monthlySettlement.protocol, ["debit", "provision", "claim", "activate"]);
  assert.equal(freeze.monthlySettlement.confirmedNoResourceAfterDebit, "idempotent_refund");
  assert.equal(freeze.monthlySettlement.partialOrUnknownProviderResult, "manual_review_without_refund");
  assert.equal(freeze.providerProcurement.chargeType, "PREPAID");
  assert.equal(freeze.providerProcurement.periodMonths, 1);
  assert.equal(freeze.providerProcurement.renewFlag, "NOTIFY_AND_MANUAL_RENEW");
  assert.deepEqual(freeze.providerProcurement.forbiddenChargeTypes, ["POSTPAID_BY_HOUR"]);
  assert.equal(freeze.workspaceRuntime.sourceImage.digest, "sha256:9d867fe0fc9db48b6efa27371d77770e46fc8cd97d26ef85a81fbdac7e96ca76");
  assert.equal(freeze.gateway.sub2apiMutable, false);
  assert.equal(freeze.gateway.backend, "Sub2API v0.1.156");
  assert.deepEqual(freeze.gateway.compatibleVersions, ["0.1.155", "0.1.156"]);
  assert.equal(freeze.productSurfaces.gateway.backend, freeze.gateway.backend);
  assert.equal(freeze.gateway.keyName, "opl-workspace");
  assert.equal(freeze.gateway.adminUsageEndpointAllowed, false);

  assert.equal(freeze.verification.slot.computeInstanceType, "SA5.MEDIUM4");
  assert.equal(freeze.verification.slot.resources.memoryGb, 4);
  assert.equal(freeze.verification.slot.customerProduct, false);
  assert.equal(freeze.verification.slot.reuseForBillingPeriod, true);
  assert.equal(freeze.verification.purchaseBudget, 1);
  assert.equal(freeze.verification.purchaseBudgetRemaining, 1);
  assert.equal(freeze.verification.perRunTencentPurchase, false);
  assert.equal(freeze.verification.monthlyBillingBackend, "fake");
  assert.equal(freeze.verification.gatewayRequest, "real_dedicated_test_key");
  assert.deepEqual(freeze.verification.providerResourcesDeletedPerRun, []);
  assert.equal(freeze.launchStages.length, 10);
  assert.equal("slides" in freeze, false);
  assert.equal(freeze.deliveryPhases.length, 6);
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
  assert.equal(deployment.productionVerificationWorkflow.mode, "read_only_fixed_slot");
  assert.equal(deployment.productionLiveQaJob.releaseGate, true);
  assert.equal(deployment.productionLiveQaJob.mode, "one_model_request_no_provider_mutation");
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
