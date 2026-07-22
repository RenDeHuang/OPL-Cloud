import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const contract = (name) => new URL(`../../packages/contracts/${name}`, import.meta.url);

async function readJson(name) {
  return JSON.parse(await readFile(contract(name), "utf8"));
}

test("current contracts name Sub2API as the only spendable balance", async () => {
  const [billing, business, management, boundaries] = await Promise.all([
    readJson("opl-cloud-billing-ledger-contract.json"),
    readJson("opl-cloud-business-object-contract.json"),
    readJson("opl-cloud-management-contract.json"),
    readJson("opl-cloud-service-boundary-contract.json")
  ]);

  assert.equal(billing.balanceOwner, "sub2api");
  assert.equal(billing.billingUnit, "calendar_month");
  assert.equal(billing.walletPolicy, undefined);
  assert.equal(billing.prepaidHoldPolicy, undefined);
  assert.equal(billing.manualTopUpPolicy, undefined);
  assert.deepEqual(billing.moneyWriteApis, ["POST /api/v1/admin/redeem-codes/create-and-redeem"]);
  assert.deepEqual(billing.walletAdjustmentEvidence.redeemCode, {
    version: "v2",
    format: `"opl:" + stableID("sub2api-wallet-adjustment-v2", operationID)[:28]`,
    length: 32,
    pattern: "^opl:[0-9a-f]{28}$",
    legacyV1Length: 49,
    legacyV1Policy: "read_only_history_identity_never_payload_or_idempotency_key"
  });
  assert.deepEqual(management.walletAdjustments.manualReviewRecovery.identityReuse, ["original_operation_id", "stable_recovery_intent"]);
  assert.equal(management.walletAdjustments.manualReviewRecovery.unknownRecoveryResult, "manual_review_without_second_v2_write");

  const kinds = new Set(business.objectKinds.map((entry) => entry.kind));
  assert.equal(kinds.has("Wallet"), false);
  assert.equal(kinds.has("LedgerEntry"), false);
  assert.equal(kinds.has("Balance"), true);
  assert.match(business.principles.join("\n"), /Sub2API owns the only spendable balance/);

  assert.equal(management.entities.account.requiredFields.includes("sub2apiUserId"), true);
  assert.equal(management.entities.billingAccount, undefined);
  assert.equal(management.api.manualTopUp, undefined);
  assert.equal(boundaries.services.controlPlane.calls.sub2api, "http");
  for (const retired of ["wallets", "holds", "manualTopups", "ledgerEntries", "walletTransactions", "resourceSettlements"]) {
    assert.equal(boundaries.services.ledger.owns.includes(retired), false, `Ledger must not own ${retired}`);
  }
  assert.equal(boundaries.externalServices.gateway.calls, undefined);
  assert.equal(boundaries.externalServices.gateway.evidenceSink, undefined);
});

test("management contract hard-cuts customer identity to Sub2API and one atomic owner graph", async () => {
  const management = await readJson("opl-cloud-management-contract.json");

  assert.equal(management.schemaVersion, 16);
  assert.deepEqual(management.entities.account.requiredFields, ["id", "ownerUserId", "status", "sub2apiUserId", "createdAt", "updatedAt"]);
  assert.deepEqual(management.entities.user, {
    requiredFields: ["id", "email", "accountId", "role", "status", "createdAt", "updatedAt"],
    roles: ["owner"]
  });
  assert.deepEqual(management.entities.membership.requiredFields, ["id", "organizationId", "accountId", "userId", "role", "status", "createdAt", "updatedAt"]);
  assert.deepEqual(management.customerIdentityGraph, {
    cardinality: "exactly_one_console_user_account_sub2api_user_wallet",
    accountUser: "account.ownerUserId_equals_user.id_and_user.accountId_equals_account.id",
    sub2apiIdentity: "account.sub2apiUserId_equals_the_single_remote_user_id_and_wallet_owner",
    normalizedEmail: "lower_trim_console_email_equals_lower_trim_sub2api_email",
    customerRole: "owner_only",
    customerAccess: "session_user_owns_account_and_remote_identity_is_active",
    operatorException: "fixed_usr_admin_on_acct_admin_uses_admin_role_outside_customer_graph"
  });
  assert.deepEqual(management.pilotCohort, {
    mode: "invite_only",
    minimumCustomerAccounts: 2,
    maximumCustomerAccounts: 5,
    publicRegistration: false
  });
  assert.equal(management.internalCompatibilityRecords.sharedBehavior, false);
  assert.equal(management.internalCompatibilityRecords.customerAuthorizationAuthority, false);
  assert.deepEqual(management.identityProvisioning.atomicFacts, ["account", "user", "organization", "membership"]);
  assert.deepEqual(management.entities.membership.roles, ["owner"]);
  assert.equal(management.identityProvisioning.onlyMutation, "POST /api/operator/accounts");
  assert.equal(management.identityProvisioning.requestType, "ProvisionAccountRequest");
  assert.deepEqual(management.identityProvisioning.semantics, {
    command: "provision",
    operatorLanguage: "open",
    auditAction: "account.provision",
    operationIdPrefix: "account-provision"
  });
  assert.equal(management.identityProvisioning.callerSuppliedSub2apiUserId, "forbidden");
  assert.equal(management.identityProvisioning.partialFailure, "rollback_all_four_facts");
  assert.equal(management.identityProvisioning.matchingReplay, "return_existing_graph_without_duplicate_facts");
  assert.equal(management.identityProvisioning.mismatch, "fail_closed_without_mutation");

  assert.equal(management.identitySecurity.passwordAuthority, "sub2api");
  assert.equal(management.identitySecurity.provisionPasswordHandling, "forward_to_sub2api_only_never_persist");
  assert.equal(management.identitySecurity.invitePasswordHandling, undefined);
  assert.deepEqual(management.identitySecurity.localPasswordHash, {
    column: "control_plane_users.password_hash",
    requiredValue: "",
    enforcement: "database_check_constraint"
  });
  assert.equal(management.identitySecurity.sessionLookupKeyPrefix, "sub2api-sha256:");

  assert.deepEqual(management.api.unsupportedMutations, [
    "POST /api/organizations",
    "POST /api/organizations/members",
    "POST /api/organizations/members/{id}/revoke",
    "POST /api/users/{id}/reset-password"
  ]);
  for (const retired of ["createOrganization", "addOrganizationMember", "resetUserPassword"]) {
    assert.equal(management.api[retired], undefined);
  }
  assert.equal(management.workspaceOwnership.organizationWorkspace, undefined);
  assert.equal(management.api.managementState, "GET /api/management/state");
  assert.deepEqual(management.api.managementStateExcludedFields, ["organization", "organizations", "memberships"]);
  assert.equal(management.bootstrapLifecycle.legacyLocalUsersEnv, "retired_nonempty_value_fails_startup");
  assert.deepEqual(management.identityDelivery, {
    controlPlane: "canonical_provisioning_integrated_local_verified",
    deploymentCutover: "canonical_route_deployment_pending",
    authenticatedRuntimeEvidence: "pending"
  });
});

test("offer identity reports local integrated verification without claiming runtime evidence", async () => {
  const launch = await readJson("opl-cloud-launch-freeze-contract.json");
  const stage = launch.launchStages.find(({ id }) => id === "offer_identity");

  assert.equal(
    stage.currentState,
    "Canonical POST /api/operator/accounts provisioning and the strict one-to-one mapped-owner graph have integrated local evidence; deployment and authenticated production runtime evidence remain pending, while self-registration and SSO are outside the Pilot."
  );
  assert.doesNotMatch(stage.currentState, /CI-verified/);
  assert.doesNotMatch(stage.currentState, /operator password reset/);
});

test("pricing contract fixes exact integer monthly charges", async () => {
  const pricing = await readJson("opl-cloud-pricing-contract.json");

  assert.equal(pricing.priceVersion, "pilot-usd-2026-07-v1");
  assert.equal(pricing.catalogVersion, undefined);
  assert.equal(pricing.billingUnit, "calendar_month");
  assert.equal(pricing.currency, "USD");
  assert.equal(pricing.displayCurrency, "USD");
  assert.equal(pricing.walletCurrency, "USD");
  assert.equal(pricing.exchangeRateCnyPerUsd, undefined);
  assert.deepEqual(pricing.computeMonthly, {
    basic: { usdMicros: 50000000 },
    pro: { usdMicros: 214280000 }
  });
  assert.deepEqual(pricing.storagePer10GbMonthly, { usdMicros: 2580000 });
  assert.deepEqual(pricing.storageMonthly, {
    "10": { usdMicros: 2580000 },
    "100": { usdMicros: 25800000 }
  });
  assert.deepEqual(pricing.workspaceMonthly, {
    basic: { packageId: "basic", sizeGb: 10, computeUsdMicros: 50000000, storageUsdMicros: 2580000, totalUsdMicros: 52580000 },
    pro: { packageId: "pro", sizeGb: 100, computeUsdMicros: 214280000, storageUsdMicros: 25800000, totalUsdMicros: 240080000 }
  });
  assert.deepEqual(pricing.internalProviderCostEvidence, {
    currency: "CNY",
    computeMonthlyCnyCents: { basic: 35000, pro: 150000 },
    storageMonthlyCnyCents: { "10": 1800, "100": 18000 },
    customerChargeDerivation: "forbidden"
  });
  assert.deepEqual(pricing.storageSize, { minimumGb: 10, stepGb: 10 });
  assert.equal(pricing.computeHourly, undefined);
  assert.equal(pricing.storageGbMonth, undefined);
  assert.equal(pricing.env, undefined);
});

test("receipt contract exposes monthly product behavior only", async () => {
	const [billing, evidence] = await Promise.all([
		readJson("opl-cloud-billing-ledger-contract.json"),
		readJson("opl-cloud-evidence-ledger-contract.json")
	]);

  for (const type of [
    "billing.workspace_purchased.v1",
    "billing.resource_purchased.v1",
    "billing.resource_renewed.v1",
    "billing.resource_expired.v1",
    "billing.resource_refunded.v1",
    "billing.charge_review_required.v1",
    "billing.reconciliation.v1",
    "billing.workspace_renewed.v1",
    "billing.workspace_expired.v1",
    "billing.workspace_refunded.v1"
  ]) {
    assert.ok(evidence.receiptTypes.includes(type), `missing receipt type ${type}`);
  }
	assert.deepEqual(evidence.workspaceMonthlyBillingReceiptV1.exactComponents, {
		compute: ["resourceType", "resourceId", "chargeUsdMicros"],
		storage: ["resourceType", "resourceId", "sizeGb", "chargeUsdMicros"]
	});
	assert.equal(billing.ledgerEvidencePolicy.workspaceCostRules.outerWorkspaceIdentity, "cost.resourceId_equals_receipt.workspaceId");
	assert.ok(evidence.workspaceMonthlyBillingReceiptV1.rules.includes("cost.resourceId equals receipt workspaceId"));
	assert.deepEqual(billing.reconciliationPolicy.exceptions.resourceTypes, ["compute", "storage", "workspace"]);
	assert.deepEqual(billing.reconciliationPolicy.workspaceRenewalAuthority, {
		customerOperationCardinality: 1,
		balanceFact: "one_combined_sub2api_charge",
		providerFacts: ["compute_renewal", "storage_renewal"],
		receiptType: "billing.workspace_renewed.v1"
	});
	assert.deepEqual(evidence.reconciliationReportV1.exceptions.resourceTypes, ["compute", "storage", "workspace"]);
	assert.deepEqual(evidence.reconciliationReportV1.workspaceRenewalAuthority, billing.reconciliationPolicy.workspaceRenewalAuthority);
	const management = await readJson("opl-cloud-management-contract.json");
	assert.equal(management.schemaVersion, 16);
	assert.equal(
		management.operatorNotifications.source,
		"Derived from current Workspace renewal operations plus current compute and storage compatibility state; no alert table or second source of truth."
	);
	assert.deepEqual(management.operatorNotifications.activeCodes, [
		"manual_review",
		"past_due",
		"ledger_receipt_pending",
		"cleanup_failed",
		"insufficient",
		"renewal_retry_pending",
		"renewal_receipt_pending",
		"refund_receipt_pending",
		"expiry_receipt_pending",
		"cleanup_pending"
	]);
	assert.deepEqual(management.operatorNotifications.severity, {
		error: ["manual_review", "cleanup_failed", "cleanup_pending"],
		warning: ["past_due", "ledger_receipt_pending", "insufficient", "renewal_retry_pending", "renewal_receipt_pending", "refund_receipt_pending", "expiry_receipt_pending"]
	});
	assert.match(management.operatorNotifications.logPolicy, /hashed resource references/);
	assert.match(management.operatorNotifications.logPolicy, /account IDs, resource IDs, redeem codes, balances, credentials, and provider errors are excluded/);
});
