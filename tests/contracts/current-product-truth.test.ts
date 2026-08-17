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

test("pricing contract preserves the Basic and Pro monthly package facts", async () => {
  const pricing = await json("packages/contracts/opl-cloud-pricing-contract.json");

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
    organizationAndMembership: "historical_read_only_table_custody",
    legacyCustody: "preserve_existing_rows_and_ids_for_migration_validation_only",
    runtimeRead: false,
    runtimeWrite: false,
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
