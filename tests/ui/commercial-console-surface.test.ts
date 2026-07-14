import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);

async function source(path: string) {
  return readFile(new URL(path, root), "utf8");
}

test("shared Console layer exposes the components used by product pages", async () => {
  const shared = await source("apps/console-ui/src/pages/shared/commercial-console.tsx");
  for (const name of [
    "ConsoleSurface",
    "MetricStrip",
    "InsightPanel",
    "StatusPill",
    "ResourceSplit",
    "ActionGroup",
    "ObjectTable",
    "OperationConfirmButton",
    "OperationResultPanel",
    "OperationTimeline",
    "FailureRecoveryPanel",
    "BalanceChargePanel",
    "ResourceRelationshipGraph",
    "ProductionE2EPanel"
  ]) {
    assert.match(shared, new RegExp(`export function ${name}\\b`));
  }
});

test("paid resource forms call typed clients and show backend monthly facts", async () => {
  const page = await source("apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx");
  const api = await source("apps/console-ui/src/api/resources-api.ts");

  for (const call of ["createComputeAllocation", "createStorageVolume", "reactivateStorageVolume", "setResourceAutoRenew", "destroyComputeAllocation", "destroyStorageVolume", "attachStorage", "detachStorage"]) {
    assert.match(page, new RegExp(`${call}\\(`));
    assert.match(api, new RegExp(`export (?:function|const) ${call}\\b`));
  }
  for (const fact of ["monthlyPriceCnyCents", "chargeUsdMicros", "paidThrough", "autoRenew", "BalanceChargePanel", "OperationResultPanel"]) {
    assert.match(page, new RegExp(fact));
  }
  assert.match(page, /step=\{10\}/);
  assert.match(page, /<Switch/);
  assert.match(page, /OperationConfirmButton/);
  assert.doesNotMatch(page, /disabled: true|providerRequestId|ownerAccountId/);
});

test("Workspace access remains scoped, recoverable, and duplicate-safe", async () => {
  const create = await source("apps/console-ui/src/pages/workspaces/CreateWorkspacePage.tsx");
  const detail = await source("apps/console-ui/src/pages/workspaces/WorkspaceDetailPage.tsx");
  const store = await source("apps/console-ui/src/store/console-state.ts");

  assert.match(create, /attachmentId/);
  assert.match(create, /OperationResultPanel/);
  assert.doesNotMatch(create, /if \(created\) navigate/);
  assert.match(detail, /showPassword/);
  assert.match(detail, /重置 URL/);
  assert.match(detail, /停用访问/);
  assert.match(detail, /启用访问/);
  assert.match(detail, /actionKey: `workspace-reset-\$\{selected\.id\}`/);
  assert.match(detail, /actionKey: `workspace-delete-\$\{selected\.id\}`/);
  assert.doesNotMatch(detail, /ownerAccountId|providerRequestId|CVM ID/);
  assert.match(store, /pendingActionKeys\.current\.has\(actionKey\)/);
});

test("Console state and mutations refresh account-scoped server truth", async () => {
  const page = await source("apps/console-ui/src/pages/ConsolePage.tsx");
  const store = await source("apps/console-ui/src/store/console-state.ts");
  const api = await source("apps/console-ui/src/api/console-read-api.ts");
  const tickets = await source("apps/console-ui/src/pages/support/useTickets.ts");

  assert.match(page, /accountId: session\.user\?\.accountId/);
  assert.match(store, /getConsoleState\(accountId\)/);
  assert.match(store, /tickets\.refresh\(\)/);
  assert.match(api, /getManagementState\(organizationId = "", includeDeleted = false\)/);
  assert.match(tickets, /await refresh\(\)/);
  assert.doesNotMatch(tickets, /setTickets\(\(current\)/);
});

test("Admin surfaces expose Sub2API mapping, monthly review, and Receipt evidence", async () => {
  const admin = await source("apps/console-ui/src/pages/admin/AdminOverviewPage.tsx");
  const routes = await source("apps/console-ui/src/routes/opl-routes.ts");

  for (const signal of ["sub2apiUserId", "manual_review", "lastBillingError", "billingReceipts", "EvidenceReceipt", "chargeUsdMicros"]) {
    assert.match(`${admin}\n${routes}`, new RegExp(signal));
  }
  for (const retired of ["manualTopUp", "settleResourceBilling", "billing/topups", "resource-settlements", "walletTransactions"]) {
    assert.doesNotMatch(`${admin}\n${routes}`, new RegExp(retired.replace("/", "\\/")));
  }
  assert.match(admin, /createUser\(/);
  assert.match(admin, /Form\.Item name="sub2apiUserId"/);
  assert.match(admin, /disableUser\(/);
  assert.match(admin, /deleteUser\(/);
});

test("Admin diagnostics stay read-only and expose provider ownership evidence", async () => {
  const admin = await source("apps/console-ui/src/pages/admin/AdminOverviewPage.tsx");
  const diagnostics = admin.slice(admin.indexOf("export function AdminDiagnosticsPage"), admin.indexOf("export function AdminCleanupPage"));

  for (const signal of ["resourceLedgerEvidence", "ownerAccountId", "cvmInstanceId", "nodeName", "storageProviderId", "operationId", "costTags", "receiptIds"]) {
    assert.match(admin, new RegExp(signal));
  }
  assert.doesNotMatch(diagnostics, /OPL_CODEX_API_KEY|access\?\.token|password/i);
});

test("public and authenticated chrome consistently present OPL Console", async () => {
  const home = await source("apps/console-ui/src/pages/HomePage.tsx");
  const login = await source("apps/console-ui/src/pages/LoginPage.tsx");
  const shell = await source("apps/console-ui/src/pages/ConsolePage.tsx");

  assert.match(home, /publicConsole/);
  assert.match(home, /Sub2API USD/);
  assert.match(shell, /title="OPL Console"/);
  for (const page of [home, login, shell]) assert.match(page, /OplAppLogo/);
  assert.doesNotMatch(`${home}\n${shell}`, /Loading OPL Cloud|> OPL Cloud</);
});
