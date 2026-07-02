import assert from "node:assert/strict";
import { access, readFile } from "node:fs/promises";
import test from "node:test";

const repoRoot = new URL("../../", import.meta.url);

function path(relativePath) {
  return new URL(relativePath, repoRoot);
}

async function source(relativePath) {
  return readFile(path(relativePath), "utf8");
}

async function assertFile(relativePath) {
  await access(path(relativePath));
}

function lineCount(text) {
  return text.trimEnd().split("\n").length;
}

test("console UI is split into api, store, shared, and page modules", async () => {
  for (const file of [
    "packages/console/ui/api/console-api.js",
    "packages/console/ui/store/console-state.js",
    "packages/console/ui/pages/shared/console-menu.jsx",
    "packages/console/ui/pages/shared/formatters.js",
    "packages/console/ui/pages/shared/page-widgets.jsx",
    "packages/console/ui/pages/workspaces/WorkspacesPage.jsx",
    "packages/console/ui/pages/workspaces/WorkspaceDetailPage.jsx",
    "packages/console/ui/pages/workspaces/CreateWorkspacePage.jsx",
    "packages/console/ui/pages/billing/BillingPage.jsx",
    "packages/console/ui/pages/admin/AdminOverviewPage.jsx",
    "packages/console/ui/pages/support/SupportPage.jsx"
  ]) {
    await assertFile(file);
  }

  const consolePage = await source("packages/console/ui/pages/ConsolePage.jsx");
  assert.ok(lineCount(consolePage) < 260, "ConsolePage should stay a route shell, not a page bundle");
  assert.doesNotMatch(consolePage, /async function api\(/);
  assert.doesNotMatch(consolePage, /function WorkspacesPage\(/);
  assert.doesNotMatch(consolePage, /function AdminOverviewPage\(/);
  assert.match(consolePage, /from "\.\.\/api\/console-api\.js"/);
  assert.match(consolePage, /from "\.\.\/store\/console-state\.js"/);
});

test("opl-cloud delegates pure billing and workspace helpers to service modules", async () => {
  for (const file of [
    "packages/console/src/services/core-utils.js",
    "packages/console/src/services/wallet-service.js",
    "packages/console/src/services/pricing-service.js",
    "packages/console/src/services/usage-billing-service.js",
    "packages/console/src/services/workspace-service.js"
  ]) {
    await assertFile(file);
  }

  const service = await source("packages/console/src/opl-cloud.js");
  assert.ok(lineCount(service) < 1800, "opl-cloud should keep orchestration methods, not every helper");
  assert.doesNotMatch(service, /function ensureUserWallet\(/);
  assert.doesNotMatch(service, /function packageHoldAmount\(/);
  assert.doesNotMatch(service, /function latestWorkspaceForAccount\(/);
  assert.match(service, /from "\.\/services\/wallet-service\.js"/);
  assert.match(service, /from "\.\/services\/pricing-service\.js"/);
  assert.match(service, /from "\.\/services\/workspace-service\.js"/);
});
