import assert from "node:assert/strict";
import test from "node:test";

import {
  adminMenuRoutes,
  consoleRoutes,
  findRoute,
  menuRoutesFor,
  ownerMenuRoutes,
  routeTo,
  routesById
} from "../../apps/console-ui/src/consoleRoutes.ts";

test("runtime registry contains only reachable Console routes", () => {
  for (const [id, path] of [
    ["public.home", "/"],
    ["auth.login", "/login"],
    ["console.overview", "/console/overview"],
    ["compute-pools.list", "/console/compute/pools"],
    ["compute-allocations.create", "/console/compute/new"],
    ["storage.create", "/console/storage/new"],
    ["attachment.create", "/console/attachments/new"],
    ["resources.relationships", "/console/resources/relationships"],
    ["workspace.create", "/console/workspaces/new"],
    ["gateway.external", "/console/gateway"],
    ["billing.overview", "/console/billing"],
    ["support.create", "/console/support/new"],
    ["alerts.list", "/console/alerts"],
    ["admin.overview", "/admin/overview"],
    ["admin.ledger", "/admin/ledger"],
    ["admin.cleanup", "/admin/cleanup"]
  ]) {
    assert.equal(routesById.get(id)?.path, path);
  }

  for (const path of [
    "/register",
    "/invite/accept",
    "/console/resources",
    "/console/approvals",
    "/console/receipts",
    "/admin/fabric",
    "/admin/governance",
    "/admin/runtime/kubernetes"
  ]) {
    assert.equal(consoleRoutes.some((route) => route.path === path), false, `${path} must not be reachable`);
  }
});

test("owner and admin menus expose the current product surfaces", () => {
  assert.deepEqual(ownerMenuRoutes.map((route) => route.label), [
    "概览",
    "工作区",
    "账号"
  ]);
  assert.deepEqual(adminMenuRoutes.map((route) => route.label), [
    "管理概览",
    "用户",
    "账单运营",
    "账本",
    "运行状态",
    "线上诊断",
    "E2E记录",
    "入口清理",
    "工单运营"
  ]);

  for (const route of ownerMenuRoutes) {
    assert.equal(route.role, "lab_owner");
    assert.equal(route.requiresAuth, true);
    assert.equal(route.requiresAdmin, false);
  }
  for (const route of adminMenuRoutes) {
    assert.equal(route.role, "admin");
    assert.equal(route.requiresAuth, true);
    assert.equal(route.requiresAdmin, true);
  }
});

test("route lookup, redirects, parameters, and role checks are executable", () => {
  assert.equal(findRoute("/console").redirect, "/console/overview");
  assert.equal(findRoute("/console/workspaces/ws_sample").id, "workspace.detail");
  assert.equal(findRoute("/missing").id, "error.notFound");
  assert.equal(routeTo("workspace.detail", { id: "ws sample" }), "/console/workspaces/ws%20sample");
  assert.equal(routeTo("support.detail", { id: "ticket_sample" }), "/console/support/ticket_sample");
  assert.throws(() => routeTo("workspace.detail"), /missing route param: id/);
  assert.throws(() => routeTo("admin.overview", {}, { role: "lab_owner" }), /not allowed/);
});

test("menu feature flags hide only the routes they own", () => {
  assert.deepEqual(
    menuRoutesFor("lab_owner", { features: { workspaces: false } }).map((route) => route.id),
    ["console.overview", "account.overview"]
  );
  assert.deepEqual(
    menuRoutesFor("admin", { features: { ledgerAdmin: false, runtimeAdmin: false, support: false } }).map((route) => route.id),
    ["admin.overview", "admin.users", "admin.billing"]
  );
});
