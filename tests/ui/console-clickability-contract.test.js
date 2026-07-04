import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { consoleActions } from "../../packages/console/ui/routes/opl-actions.js";
import { routeTo, routesById } from "../../packages/console/ui/consoleRoutes.js";

const repoRoot = new URL("../../", import.meta.url);

async function source(relativePath) {
  return readFile(new URL(relativePath, repoRoot), "utf8");
}

test("console actions resolve through route ids or explicit non-route action types", () => {
  assert.ok(consoleActions.length > 0, "action registry should declare current clickable objects");
  const actionIds = new Set();

  for (const action of consoleActions) {
    assert.ok(action.id, "action must have id");
    assert.equal(actionIds.has(action.id), false, `duplicate action id ${action.id}`);
    actionIds.add(action.id);
    assert.ok(action.objectKind, `${action.id} must declare objectKind`);
    assert.ok(action.role === "lab_owner" || action.role === "admin", `${action.id} must declare allowed role`);

    if (action.type === "route") {
      const route = routesById.get(action.routeId);
      assert.ok(route, `${action.id} points at missing route ${action.routeId}`);
      const params = Object.fromEntries([...route.path.matchAll(/:([^/]+)/g)].map(([, key]) => [key, `${key}-demo`]));
      assert.equal(routeTo(action.routeId, params, { role: action.role }), route.path.replace(/:([^/]+)/g, (_, key) => params[key]));
      assert.equal(action.path, undefined, `${action.id} must not hard-code path`);
    }

    if (action.type === "disabled") {
      assert.ok(action.disabledReason, `${action.id} disabled action must explain why`);
    }

    if (action.type === "api") {
      assert.ok(action.apiClient, `${action.id} api action must name apiClient`);
      assert.ok(action.apiName, `${action.id} api action must name apiName`);
    }
  }
});

test("workspace and support click targets are route/action registry backed", () => {
  const actionsById = new Map(consoleActions.map((action) => [action.id, action]));

  for (const id of [
    "compute-allocations.create",
    "compute-allocations.detail",
    "compute-allocations.destroy",
    "storage.create",
    "storage.detail",
    "storage.destroy",
    "attachment.create",
    "attachment.detail",
    "attachment.detach",
    "workspace.create",
    "workspace.detail",
    "workspace.openUrl",
    "workspace.resetUrl",
    "workspace.deleteUrl",
    "billing.wallet",
    "support.create",
    "support.detail",
    "admin.manualTopup",
    "admin.userCreate",
    "admin.userWallet.disabled"
  ]) {
    assert.ok(actionsById.has(id), `missing action ${id}`);
  }
});

test("page modules do not call raw server APIs directly", async () => {
  for (const page of [
    "packages/console/ui/pages/ConsolePage.jsx",
    "packages/console/ui/pages/OverviewPage.jsx",
    "packages/console/ui/pages/workspaces/WorkspacesPage.jsx",
    "packages/console/ui/pages/workspaces/WorkspaceDetailPage.jsx",
    "packages/console/ui/pages/workspaces/CreateWorkspacePage.jsx",
    "packages/console/ui/pages/resources/ResourceProvisioningPages.jsx",
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
