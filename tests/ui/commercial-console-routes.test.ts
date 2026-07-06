import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { adminMenuRoutes, consoleRoutes, ownerMenuRoutes, routeTo, routesById } from "../../apps/console-ui/src/consoleRoutes.ts";

const contractPath = new URL("../../packages/contracts/opl-cloud-route-api-contract.json", import.meta.url);

async function readContract() {
  return JSON.parse(await readFile(contractPath, "utf8"));
}

function allContractRoutes(contract) {
  return [
    ...(contract.publicRoutes || []),
    ...(contract.authRoutes || []),
    ...(contract.consoleRoutes || []),
    ...(contract.adminRoutes || []),
    ...(contract.errorRoutes || [])
  ];
}

test("commercial Console route contract covers current public, auth, owner, and admin surfaces only", async () => {
  const contract = await readContract();
  const routes = allContractRoutes(contract);
  const byId = new Map(routes.map((route) => [route.id, route]));

  for (const [id, status] of [
    ["public.home", "implemented"],
    ["public.pricing", "folded_into_parent"],
    ["public.docs", "folded_into_parent"],
    ["public.status", "folded_into_parent"],
    ["auth.login", "implemented"],
    ["console.overview", "implemented"],
    ["compute-pools.list", "implemented"],
    ["compute-allocations.list", "implemented"],
    ["compute-allocations.create", "implemented"],
    ["compute-allocations.detail", "implemented"],
    ["storage.list", "implemented"],
    ["storage.create", "implemented"],
    ["storage.detail", "implemented"],
    ["attachment.list", "implemented"],
    ["attachment.create", "implemented"],
    ["attachment.detail", "implemented"],
    ["resources.relationships", "implemented"],
    ["workspace.list", "implemented"],
    ["workspace.create", "implemented"],
    ["workspace.detail", "implemented"],
    ["gateway.external", "external"],
    ["billing.overview", "implemented"],
    ["billing.wallet", "folded_into_parent"],
    ["account.overview", "implemented"],
    ["support.list", "implemented"],
    ["support.create", "implemented"],
    ["support.detail", "implemented"],
    ["alerts.list", "implemented"],
    ["admin.overview", "implemented"],
    ["admin.users", "implemented"],
    ["admin.billing", "implemented"],
    ["admin.ledger", "implemented"],
    ["admin.runtime", "implemented"],
    ["admin.diagnostics", "implemented"],
    ["admin.e2e", "implemented"],
    ["admin.cleanup", "implemented"],
    ["admin.support", "implemented"]
  ]) {
    assert.equal(byId.get(id)?.status, status, `${id} must have current commercial route status ${status}`);
  }

  assert.equal(routes.some((route) => route.status === "reserved"), false, "active UI route contract must not include reserved routes");
  assert.equal(consoleRoutes.some((route) => route.featureGate), false, "runtime route table must not include future feature gates");
});

test("Lab Owner menu is commercial and excludes operator surfaces", () => {
  assert.deepEqual(ownerMenuRoutes.map((route) => route.label), [
    "概览",
    "工作区",
    "配置"
  ]);

  for (const route of ownerMenuRoutes) {
    assert.equal(route.area, "console");
    assert.equal(route.role, "lab_owner");
    assert.equal(route.requiresAuth, true);
    assert.notEqual(route.requiresAdmin, true);
    assert.equal(route.adminMenu, undefined);
    assert.ok(routesById.has(route.id), `${route.id} must exist in route registry`);
  }
});

test("runtime owner menu mirrors the active route contract", async () => {
  const contract = await readContract();
  const contractOwnerMenu = allContractRoutes(contract)
    .filter((route) => route.area === "console" && route.role === "lab_owner" && route.menu === true)
    .map((route) => route.id);
  const runtimeOwnerMenu = ownerMenuRoutes.map((route) => route.id);

  assert.deepEqual(runtimeOwnerMenu, contractOwnerMenu);
  assert.deepEqual(ownerMenuRoutes.map((route) => route.label), [
    "概览",
    "工作区",
    "配置"
  ]);

  for (const id of [
    "compute-allocations.list",
    "storage.list",
    "attachment.list",
    "resources.relationships",
    "gateway.external",
    "billing.overview",
    "support.list",
    "alerts.list"
  ]) {
    assert.equal(routesById.get(id)?.hiddenInMenu, true, `${id} must stay second-level in runtime routes`);
    assert.equal(
      allContractRoutes(contract).find((route) => route.id === id)?.hiddenInMenu,
      true,
      `${id} must stay second-level in route contract`
    );
  }
});

test("Admin menu owns operator surfaces behind admin permission", () => {
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

  for (const route of adminMenuRoutes) {
    assert.equal(route.area, "admin");
    assert.equal(route.role, "admin");
    assert.equal(route.requiresAuth, true);
    assert.equal(route.requiresAdmin, true);
    assert.ok(routesById.has(route.id), `${route.id} must exist in route registry`);
  }
});

test("route table and routeTo do not expose reserved routes in visible owner or admin menus", () => {
  const visiblePaths = new Set([...ownerMenuRoutes, ...adminMenuRoutes].map((route) => route.path));
  const reservedPaths = [
    "/register",
    "/invite/accept",
    "/email/verify",
    "/forgot-password",
    "/reset-password",
    "/console/resources",
    "/console/approvals",
    "/admin/fabric",
    "/admin/governance",
    "/admin/runtime/kubernetes"
  ];

  for (const path of reservedPaths) {
    assert.equal(visiblePaths.has(path), false, `${path} must stay out of visible menus`);
    assert.equal(consoleRoutes.some((route) => route.path === path), false, `${path} must stay out of runtime routes`);
  }

  assert.equal(routeTo("workspace.detail", { id: "ws_demo" }), "/console/workspaces/ws_demo");
  assert.equal(routeTo("compute-allocations.detail", { id: "compute_demo" }), "/console/compute/compute_demo");
  assert.equal(routeTo("storage.detail", { id: "storage_demo" }), "/console/storage/storage_demo");
  assert.equal(routeTo("attachment.detail", { id: "attachment_demo" }), "/console/attachments/attachment_demo");
  assert.equal(routeTo("support.detail", { id: "ticket_demo" }), "/console/support/ticket_demo");
});

test("runtime route registry mirrors contract resource operation protocols", async () => {
  const contract = await readContract();
  const contractById = new Map(allContractRoutes(contract).map((route) => [route.id, route]));

  for (const id of [
    "compute-allocations.create",
    "compute-allocations.detail",
    "storage.create",
    "storage.detail",
    "attachment.create",
    "attachment.detail",
    "workspace.create",
    "workspace.detail"
  ]) {
    const contractRoute = contractById.get(id);
    const runtimeRoute = routesById.get(id);
    assert.deepEqual(runtimeRoute?.operationProtocol, contractRoute?.operationProtocol, `${id} runtime operation protocol must mirror active contract`);
  }

  assert.deepEqual(routesById.get("storage.detail")?.dynamicFields, contractById.get("storage.detail")?.dynamicFields, "storage runtime dynamic fields must mirror active contract");
});

test("current auth UI does not link to reserved account flows", async () => {
  const source = await readFile(new URL("../../apps/console-ui/src/pages/LoginPage.tsx", import.meta.url), "utf8");

  for (const path of [
    "/register",
    "/invite/accept",
    "/forgot-password",
    "/reset-password"
  ]) {
    assert.equal(source.includes(`href="${path}"`), false, `${path} must not be linked from current auth UI`);
  }
});
