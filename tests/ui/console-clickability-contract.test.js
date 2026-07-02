import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const repoRoot = new URL("../../", import.meta.url);

async function source(relativePath) {
  return readFile(new URL(relativePath, repoRoot), "utf8");
}

test("console menus use declared route groups and navigate through route paths", async () => {
  const routes = await source("packages/console/ui/consoleRoutes.js");
  const menu = await source("packages/console/ui/pages/shared/console-menu.jsx");
  const consolePage = await source("packages/console/ui/pages/ConsolePage.jsx");

  assert.match(routes, /export const ownerMenuRoutes/);
  assert.match(routes, /export const adminMenuRoutes/);
  assert.match(menu, /ownerMenuRoutes/);
  assert.match(menu, /adminMenuRoutes/);
  assert.match(consolePage, /navigate\(item\.path \|\| "\/console\/overview"\)/);
});

test("workspace list routes users to workspace detail", async () => {
  const page = await source("packages/console/ui/pages/workspaces/WorkspacesPage.jsx");

  assert.match(page, /navigate\(`\/console\/workspaces\/\$\{row\.id\}`\)/);
});

test("workspace detail exposes runtime, storage, backup, and URL actions through the Workspace API client", async () => {
  const detailPage = await source("packages/console/ui/pages/workspaces/WorkspaceDetailPage.jsx");
  const apiClient = await source("packages/console/ui/api/workspaces-api.js");

  assert.match(detailPage, /from "\.\.\/\.\.\/api\/workspaces-api\.js"/);
  for (const apiName of [
    "stopWorkspaceServer",
    "restartWorkspaceServer",
    "destroyWorkspaceServer",
    "destroyWorkspaceDisk",
    "createStorageBackup",
    "resetWorkspaceToken",
    "deleteWorkspaceToken"
  ]) {
    assert.match(detailPage, new RegExp(`\\b${apiName}\\b`), `Workspace detail should call ${apiName}`);
    assert.match(apiClient, new RegExp(`export function ${apiName}\\(`), `Workspace API should export ${apiName}`);
  }
});

test("page modules do not call raw server APIs directly", async () => {
  for (const page of [
    "packages/console/ui/pages/ConsolePage.jsx",
    "packages/console/ui/pages/OverviewPage.jsx",
    "packages/console/ui/pages/workspaces/WorkspacesPage.jsx",
    "packages/console/ui/pages/workspaces/WorkspaceDetailPage.jsx",
    "packages/console/ui/pages/workspaces/CreateWorkspacePage.jsx",
    "packages/console/ui/pages/billing/BillingPage.jsx",
    "packages/console/ui/pages/gateway/GatewayPage.jsx",
    "packages/console/ui/pages/account/AccountPage.jsx",
    "packages/console/ui/pages/catalog/FabricPages.jsx",
    "packages/console/ui/pages/support/SupportPage.jsx",
    "packages/console/ui/pages/admin/AdminOverviewPage.jsx"
  ]) {
    const pageSource = await source(page);
    assert.doesNotMatch(pageSource, /fetch\(["']\/api\//, `${page} should not fetch raw APIs`);
    assert.doesNotMatch(pageSource, /postJson\(["']\/api\//, `${page} should not call generic API helper directly`);
    assert.doesNotMatch(pageSource, /getJson\(["']\/api\//, `${page} should not call generic API helper directly`);
  }
});
