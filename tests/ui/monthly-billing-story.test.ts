import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);

async function source(path: string) {
  return readFile(new URL(path, root), "utf8");
}

test("Console reads the live Sub2API balance projection without a wallet fallback", async () => {
  const state = await source("apps/console-ui/src/store/console-state.ts");

  assert.match(state, /const balance = state\?\.balance/);
  assert.match(state, /\bbalance,/);
  assert.doesNotMatch(state, /state\?\.wallet|const wallet|\bwallet,/);
});

test("Console presents one monthly resource billing story", async () => {
  const paths = [
    "apps/console-ui/src/pages/HomePage.tsx",
    "apps/console-ui/src/pages/OverviewPage.tsx",
    "apps/console-ui/src/pages/account/AccountPage.tsx",
    "apps/console-ui/src/pages/admin/AdminOverviewPage.tsx",
    "apps/console-ui/src/pages/billing/BillingPage.tsx",
    "apps/console-ui/src/pages/gateway/GatewayPage.tsx",
    "apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx",
    "apps/console-ui/src/pages/shared/commercial-console.tsx",
    "apps/console-ui/src/pages/workspaces/WorkspaceDetailPage.tsx",
    "apps/console-ui/src/pages/workspaces/WorkspacesPage.tsx",
    "apps/console-ui/src/routes/opl-actions.ts",
    "apps/console-ui/src/routes/opl-routes.ts"
  ];
  const ui = (await Promise.all(paths.map(source))).join("\n");

  for (const signal of [
    "Sub2API",
    "USD",
    "1 USD = 7 CNY",
    "monthlyPriceCnyCents",
    "chargeUsdMicros",
    "paidThrough",
    "autoRenew",
    "manual_review",
    "step={10}"
  ]) {
    assert.match(ui, new RegExp(signal.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")), `missing monthly signal: ${signal}`);
  }

  assert.doesNotMatch(
    ui,
    /每小时|\/小时|手动续费|冻结余额|预冻结|冻结后|已冻结|释放冻结|holdAmountCents|walletAfterPreview|activeHourlyEstimate|hourlyPrice|hourlyEstimate|manualTopUp|settleResourceBilling|\/api\/billing\/(?:topups|resource-settlements)/,
    "active Console source still exposes the retired hourly/Hold path"
  );
});

test("Gateway remains an external portal and shows the same balance projection", async () => {
  const gateway = await source("apps/console-ui/src/pages/gateway/GatewayPage.tsx");

  assert.match(gateway, /gflabtoken\.cn/);
  assert.match(gateway, /state\.balance/);
  assert.match(gateway, /window\.open/);
  assert.doesNotMatch(gateway, /fetch\(|postJson|API Key.*CRUD|Ledger.*冻结/s);
});
