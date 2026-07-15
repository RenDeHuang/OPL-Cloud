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

  assert.equal(pricing.catalogVersion, "2026-07-14-opl-monthly-v1");
  assert.equal(pricing.billingUnit, "calendar_month");
  assert.equal(pricing.displayCurrency, "CNY");
  assert.equal(pricing.walletCurrency, "USD");
  assert.equal(pricing.exchangeRateCnyPerUsd, 7);
  assert.deepEqual(pricing.computeMonthly, {
    basic: { cnyCents: 35000, usdMicros: 50000000 },
    pro: { cnyCents: 150000, usdMicros: 214285715 }
  });
  assert.deepEqual(pricing.storagePer10GbMonthly, { cnyCents: 1800, usdMicros: 2571429 });
  assert.deepEqual(pricing.storageSize, { minimumGb: 10, stepGb: 10 });
  assert.equal(pricing.computeHourly, undefined);
  assert.equal(pricing.storageGbMonth, undefined);
  assert.equal(pricing.env, undefined);
});

test("receipt contract exposes monthly product behavior only", async () => {
  const evidence = await readJson("opl-cloud-evidence-ledger-contract.json");

  for (const type of [
    "billing.resource_purchased.v1",
    "billing.resource_renewed.v1",
    "billing.resource_expired.v1",
    "billing.charge_review_required.v1",
    "billing.reconciliation.v1"
  ]) {
    assert.ok(evidence.receiptTypes.includes(type), `missing receipt type ${type}`);
  }
});
