import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);

async function text(path) {
  return readFile(new URL(path, root), "utf8");
}

async function json(path) {
  return JSON.parse(await text(path));
}

async function filesUnder(directory, include = () => true) {
  const files = [];
  for (const entry of await readdir(new URL(`${directory}/`, root), { withFileTypes: true })) {
    const path = `${directory}/${entry.name}`;
    if (entry.isDirectory()) files.push(...await filesUnder(path, include));
    else if (entry.isFile() && include(path)) files.push(path);
  }
  return files;
}

test("public entry and current contracts preserve the operator-provisioned paid Pilot boundary", async () => {
  const [readme, architecture, packages, invariants, status, runbook, tke, pricing] = await Promise.all([
    text("README.md"),
    text("docs/implementation-architecture.md"),
    text("packages/README.md"),
    text("docs/invariants.md"),
    text("docs/status.md"),
    text("docs/runtime/production-runbook.md"),
    text("docs/runtime/tke-production-deployment.md"),
    json("packages/contracts/opl-cloud-pricing-contract.json")
  ]);

  assert.match(readme, /assets\/branding\/opl-cloud-logo\.png/);
  assert.match(readme, /assets\/branding\/opl-cloud-overview-v2\.png/);
  assert.match(readme, /Purpose: `public_cloud_entry`/);
  assert.deepEqual(pricing.workspaceMonthly.basic, {
    packageId: "basic",
    sizeGb: 10,
    computeUsdMicros: 50000000,
    storageUsdMicros: 2580000,
    totalUsdMicros: 52580000
  });
  assert.deepEqual(pricing.workspaceMonthly.pro, {
    packageId: "pro",
    sizeGb: 100,
    computeUsdMicros: 214280000,
    storageUsdMicros: 25800000,
    totalUsdMicros: 240080000
  });
  assert.match(status, /administrator-provisioned accounts/i);
  assert.match(invariants, /one Console User.*one OPL Account.*one Sub2API User\/Wallet/is);
  assert.match(runbook, /normal Console\s+Basic canary.*separately once.*read-only.*never buy a second Workspace package/is);
  assert.match(tke, /separate Control Plane, Fabric, and Ledger Kubernetes Deployments/is);
  assert.match(status, /code-complete/i);
  assert.match(status, /production-proven=false/i);
  assert.doesNotMatch(architecture, /starts from a fresh database/i);
  assert.match(architecture, /legacy identity collisions.*fail closed/is);
  assert.doesNotMatch(runbook, /safe-update\.sh|\/home\/ubuntu\/sub2api/);

  for (const [name, document] of Object.entries({ readme, architecture, packages, invariants, status })) {
    assert.doesNotMatch(document, /\bCNY\b|1 USD\s*=|exchange rate/i, `${name} customer CNY`);
    assert.doesNotMatch(document, /verification-slot-01\b/, `${name} single slot`);
    assert.doesNotMatch(document, /\b2-5\b/, `${name} capped cohort`);
  }
  assert.doesNotMatch(runbook, /reuse `verification-slot-01`/i);
  assert.doesNotMatch(status, /current Pilot V2 implementation plan/i);
  assert.doesNotMatch(runbook, /current Pilot V2 implementation plan/i);
});

test("identity contracts expose operator-provisioned owners and keep Organization internal", async () => {
  const [management, boundary] = await Promise.all([
    json("packages/contracts/opl-cloud-management-contract.json"),
    json("packages/contracts/opl-cloud-service-boundary-contract.json")
  ]);

  assert.deepEqual(management.pilotCohort, {
    mode: "operator_provisioned",
    publicRegistration: false
  });
  assert.equal(management.customerIdentityGraph.cardinality, "exactly_one_console_user_account_sub2api_user_wallet");
  assert.equal(management.customerIdentityGraph.normalizedEmail, "lower_trim_console_email_equals_lower_trim_sub2api_email");
  assert.equal(management.customerIdentityGraph.customerAccess, "session_user_owns_account_and_remote_identity_is_active");
  assert.equal(management.identitySecurity.plaintextPasswordValidation, "non_empty_delegated_to_sub2api");
  assert.equal(management.identitySecurity.plaintextPasswordMinimumCharacters, undefined);
  assert.deepEqual(management.internalCompatibilityRecords, {
    organizationAndMembership: "one_to_one_storage_only",
    customerAuthorizationAuthority: false,
    browserProjection: false,
    sharedBehavior: false,
    mutationRoutes: "retired"
  });
  assert.equal(management.userLifecycle.ownerRenewalPolicy, "Disabling or deleting an owner turns off the Workspace autoRenew intent without deleting provider resources.");

  assert.equal(boundary.services.controlPlane.owns.includes("auth"), false);
  assert.equal(boundary.services.controlPlane.owns.includes("organizations"), false);
  assert.equal(boundary.services.controlPlane.owns.includes("memberships"), false);
  assert.ok(boundary.services.controlPlane.owns.includes("sessions"));
  assert.ok(boundary.services.controlPlane.owns.includes("accountMappings"));
  assert.ok(boundary.externalServices.gateway.owns.includes("customerIdentities"));
  assert.ok(boundary.externalServices.gateway.owns.includes("customerPasswords"));
});

test("current contracts expose only authoritative Pilot sources and controls", async () => {
  const [management, sourceTruth, product, boundary] = await Promise.all([
    json("packages/contracts/opl-cloud-management-contract.json"),
    json("packages/contracts/opl-cloud-console-source-truth-contract.json"),
    json("packages/contracts/opl-cloud-product-contract.json"),
    json("packages/contracts/opl-cloud-service-boundary-contract.json")
  ]);

  assert.deepEqual(management.pilotCohort, {
    mode: "operator_provisioned",
    publicRegistration: false
  });
  assert.equal(sourceTruth.sources.gateway.wallet.usdMicrosEncoding, "non_negative_int64_decimal_string");
  assert.equal(sourceTruth.sources.gateway.wallet.balanceProjection.meaning, "conservative_spendable_lower_bound");
  assert.equal(sourceTruth.sources.gateway.wallet.balanceProjection.exactRawBalanceCopy, false);
  assert.equal(sourceTruth.sources.gateway.balanceHistory.valueUsdMicrosEncoding, "signed_int64_decimal_string");
  assert.equal(sourceTruth.sources.gateway.keys.revealRoute, "POST /api/gateway/keys/{keyId}/reveal");
  assert.deepEqual(Object.keys(sourceTruth.sources.gateway), [
    "endpoint", "wallet", "groups", "keys", "usage", "usageStats", "accountUsageStats", "balanceHistory"
  ]);
  assert.equal(product.pilotBoundary.workspaceCardinality, "many_per_account");
  assert.equal(product.pilotBoundary.accountProvisioning, "operator_provisioned");
  assert.equal(product.pilotBoundary.publicRegistration, false);
  assert.equal(product.pilotBoundary.unpaidExpiry, "deny_access_zero_fabric_or_tencent_mutation_expire_by_provider");
  assert.equal(product.pilotBoundary.workspaceDataAuthority, "cbs");
  assert.deepEqual(product.pilotBoundary.unsupportedCustomerCapabilities, ["backup", "recovery", "sync", "transfer"]);
  assert.equal(product.pilotBoundary.autoRenewCustomerControl, "hidden_until_real_renewal_evidence");
  assert.equal(boundary.browserBoundary.onlyCalls, "control_plane_product_apis");
  assert.deepEqual(boundary.browserBoundary.forbidden, ["sub2api_management_direct", "sub2api_management_redirect", "sub2api_management_iframe", "html_scraping", "raw_admin_dto"]);
  assert.deepEqual(boundary.customerMutationBoundary, { payment: false, topUp: false, keyCreate: true, keyRevoke: true });
});

test("current truth hard-cuts invitation and stage vocabulary", async () => {
  const currentDocs = [
    "README.md",
    ...await filesUnder("docs", (path) => path.endsWith(".md")),
    "packages/README.md",
    "packages/contracts/README.md"
  ];
  const contractPaths = await filesUnder("packages/contracts", (path) => path.endsWith(".json"));
  const currentContracts = [];
  for (const path of contractPaths) {
    if ((await json(path)).state === "current") currentContracts.push(path);
  }
  const currentUI = [
    ...await filesUnder("apps/console-ui/src"),
    "tools/console-browser-qa.ts"
  ];
  const activeCode = await filesUnder(
    "services/control-plane",
    (path) => path.endsWith(".go") && !path.endsWith("_test.go") && !path.startsWith("services/control-plane/migrations/")
  );
  const retiredStageVocabulary = new RegExp("\\bS(?:7|9)\\b|\\bStage\\s?B\\b");
  const documents = await Promise.all([...currentDocs, ...currentContracts, ...currentUI, ...activeCode].map(async (path) => [path, await text(path)]));
  for (const [path, raw] of documents) {
    let content = raw;
    if (path === "services/control-plane/internal/server/ent_state_store.go") {
      content = content.replaceAll("202607170001_invited_account_identity", "").replaceAll("ApplyInvitedAccountIdentity", "");
    }
    if (path === "services/control-plane/internal/server/server.go") {
      content = content.replaceAll("/api/operator/accounts/invitations", "");
    }
    assert.doesNotMatch(content, /invite|invited|invitation|邀请制/i, path);
    assert.doesNotMatch(content, retiredStageVocabulary, path);
  }
});

test("Workspace owns renewal while retired machine contracts stay absent", async () => {
  const [billing, business, evidence] = await Promise.all([
    json("packages/contracts/opl-cloud-billing-ledger-contract.json"),
    json("packages/contracts/opl-cloud-business-object-contract.json"),
    json("packages/contracts/opl-cloud-evidence-ledger-contract.json")
  ]);

  assert.equal(billing.entitlementPolicy.customerRenewalAuthority, "workspace");
  assert.equal(billing.entitlementPolicy.resourceCompatibility.renewalIntentAuthority, false);
  assert.equal(billing.entitlementPolicy.resourceCompatibility.customerPricingAuthority, false);
  assert.equal(billing.ledgerEvidencePolicy.resourceReceiptSchemaStatus, "historical_read_only_compatibility");
  assert.equal(business.customerRenewalAuthority, "workspace");
  for (const kind of ["ComputeAllocation", "StorageVolume"]) {
    const object = business.objectKinds.find((entry) => entry.kind === kind);
    assert.equal(object.requiredBillingFields.includes("monthlyPriceCnyCents"), false);
    assert.equal(object.requiredBillingFields.includes("autoRenew"), false);
  }
  assert.equal(evidence.generalReceiptV1.pilotStatus, "not_exposed_in_operator_provisioned_pilot");
  assert.equal(evidence.monthlyBillingReceiptV1.status, "superseded_internal_compatibility");
  for (const type of [
    "workspace.compute_restarted",
    "workspace.compute_recreated",
    "billing.resource_purchased.v1",
    "billing.resource_renewed.v1",
    "billing.resource_expired.v1",
    "billing.resource_refunded.v1",
    "billing.charge_review_required.v1"
  ]) {
    assert.equal(evidence.receiptTypes.includes(type), false);
  }
  assert.deepEqual(evidence.historicalReadOnlyReceiptCompatibility, {
    types: [
      "workspace.compute_restarted",
      "workspace.compute_recreated",
      "billing.resource_purchased.v1",
      "billing.resource_renewed.v1",
      "billing.resource_expired.v1",
      "billing.resource_refunded.v1",
      "billing.charge_review_required.v1"
    ],
    existingReceiptsReadable: true,
    newWritesAllowed: false
  });
  assert.equal(evidence.receiptTypes.includes("workspace.storage_backup_created"), false);
  assert.equal(evidence.receiptTypes.includes("workspace.storage_restored"), false);
  const contracts = await filesUnder("packages/contracts", (path) => path.endsWith(".json"));
  assert.equal(contracts.includes("packages/contracts/opl-cloud-shared-execution-contract.json"), false);
  assert.equal(contracts.includes("packages/contracts/opl-cloud-package-boundary-contract.json"), false);
});

test("deployment contract keeps Acceptance outside ordinary deploy", async () => {
  const deployment = await json("packages/contracts/opl-cloud-deployment-contract.json");

  assert.equal(deployment.productionLiveQaJob, undefined);
  assert.equal(deployment.providerAcceptanceWorkflow.releaseGate, false);
  assert.equal(deployment.state, "migration");
  assert.equal(deployment.lifecycle.type, "migration_guard");
  assert.equal(deployment.deliveryEvidence, undefined);
});
