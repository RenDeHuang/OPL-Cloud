import assert from "node:assert/strict";
import { access, readFile } from "node:fs/promises";
import test from "node:test";

const contract = (name) => new URL(`../../packages/contracts/${name}`, import.meta.url);
const repoFile = (name) => new URL(`../../${name}`, import.meta.url);

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

test("current route and receipt contracts expose monthly product behavior only", async () => {
  const [routes, evidence] = await Promise.all([
    readJson("opl-cloud-route-api-contract.json"),
    readJson("opl-cloud-evidence-ledger-contract.json")
  ]);
  const activeRoutes = [
    ...(routes.publicRoutes || []),
    ...(routes.authRoutes || []),
    ...(routes.consoleRoutes || []),
    ...(routes.adminRoutes || [])
  ];
  const apiRoutes = new Set(activeRoutes.flatMap((entry) => entry.apiRoutes || []));

  assert.equal(apiRoutes.has("POST /api/billing/topups"), false);
  assert.equal(apiRoutes.has("POST /api/billing/resource-settlements"), false);
  assert.equal(apiRoutes.has("GET /api/billing/receipts/:id"), true);
  assert.equal(apiRoutes.has("POST /api/resources/:id/renew"), true);
  assert.equal(apiRoutes.has("POST /api/resources/:id/auto-renew"), true);

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

test("Control Plane and deployment keep only the monthly Sub2API billing path", async () => {
  for (const file of [
    "services/control-plane/internal/server/settlement_worker.go",
    "services/control-plane/internal/server/settlement_worker_test.go",
    "services/control-plane/ent/schema/wallet_projection.go",
    "services/control-plane/ent/schema/wallet_transaction_projection.go",
    "services/control-plane/ent/schema/manual_topup_projection.go",
    "services/control-plane/ent/schema/ledger_projection.go"
  ]) {
    await assert.rejects(() => access(repoFile(file)), `${file} must be deleted`);
  }

  for (const file of [
    ".github/workflows/diagnose-tke-production.yml",
    ".github/workflows/provision-manual-workspace.yml",
    "tools/production-execution-verifier.ts",
    "tools/provision-manual-workspace.ts"
  ]) {
    await assert.rejects(() => access(repoFile(file)), `${file} must be deleted`);
  }

  const runtime = await Promise.all([
    "services/control-plane/internal/clients/ledger.go",
    "services/control-plane/internal/controlplane/service.go",
    "services/control-plane/internal/server/routes_billing.go",
    "services/control-plane/internal/server/routes_resources.go",
    "services/control-plane/internal/server/routes_state.go",
    "services/control-plane/internal/server/server.go",
    "services/control-plane/internal/server/app_state.go",
    "services/control-plane/internal/server/retention_policy.go",
    "apps/console-ui/src/config/launch-config.ts"
  ].map((file) => readFile(repoFile(file), "utf8")));
  const retired = /ManualTopUp|manualTopup|CreateHold|ActivateHold|ReleaseHold|SettleResource|ResourceSettlement|resourceSettlements|activeHourlyForResource|syncLedgerFacts|MonthlyBillingEnabled|billing\/topups|resource-settlements|settlementWorker/i;
  for (const source of runtime) assert.doesNotMatch(source, retired);

  const deployment = await Promise.all([
    ".env.example",
    "deploy/tke/opl-cloud.k8s.json",
    "tools/render-tke-manifest.ts",
    "packages/contracts/opl-cloud-deployment-contract.json",
    ".github/workflows/verify-production-chain.yml",
    ".github/workflows/deploy-tke-production.yml"
  ].map((file) => readFile(repoFile(file), "utf8")));
  for (const source of deployment) {
    assert.doesNotMatch(source, /OPL_(?:BASIC|PRO)_COMPUTE_HOURLY_CNY|OPL_STORAGE_GB_MONTH_CNY|OPL_RESOURCE_BILLING_WORKER_ENABLED|billing\/topups|resource-settlements/i);
  }
});

test("production verifier requires an explicit paid run and proves stable Sub2API charges", async () => {
  const verifier = await readFile(repoFile("tools/production-verifier.ts"), "utf8");
  const workflow = await readFile(repoFile(".github/workflows/verify-production-chain.yml"), "utf8");

  assert.match(verifier, /OPL_VERIFY_PAID_CONFIRMATION/);
  assert.match(verifier, /sub2apiUserId/);
  assert.match(verifier, /sub2apiRedeemCode/);
  assert.match(verifier, /balance_delta_matches_exact_monthly_charges/);
  assert.match(workflow, /OPL_VERIFY_PAID_CONFIRMATION/);
});
