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
	assert.equal(management.schemaVersion, 6);
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
