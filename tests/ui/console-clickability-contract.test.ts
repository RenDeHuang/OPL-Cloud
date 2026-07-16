import assert from "node:assert/strict";
import test from "node:test";

import { consoleActions } from "../../apps/console-ui/src/routes/opl-actions.ts";
import { routeTo, routesById } from "../../apps/console-ui/src/consoleRoutes.ts";

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
      const params = Object.fromEntries([...route.path.matchAll(/:([^/]+)/g)].map(([, key]) => [key, `${key}-sample`]));
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
    "workspace.gatewaySecret.rotate",
    "billing.overview",
    "support.create",
    "support.detail",
    "admin.userCreate"
  ]) {
    assert.ok(actionsById.has(id), `missing action ${id}`);
  }
  for (const id of ["workspace.resetUrl", "workspace.enableUrl", "workspace.deleteUrl"]) {
    assert.equal(actionsById.has(id), false, `retired fake URL action ${id} must not return`);
  }
});
