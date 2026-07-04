import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { adminMenuRoutes, consoleRoutes, ownerMenuRoutes, routeTo, routesById } from "../../packages/console/ui/consoleRoutes.js";

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
    ["admin.support", "implemented"]
  ]) {
    assert.equal(byId.get(id)?.status, status, `${id} must have current commercial route status ${status}`);
  }

  assert.equal(routes.some((route) => route.status === "reserved"), false, "active UI route contract must not include reserved routes");
  assert.equal(consoleRoutes.some((route) => route.featureGate), false, "runtime route table must not include future feature gates");
});

test("Lab Owner menu is commercial and excludes operator surfaces", () => {
  assert.deepEqual(ownerMenuRoutes.map((route) => route.label), [
    "Overview",
    "Compute",
    "Storage",
    "Attachments",
    "Workspaces",
    "Gateway",
    "Billing",
    "Account & Lab",
    "Support",
    "Alerts"
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

test("Admin menu owns operator surfaces behind admin permission", () => {
  assert.deepEqual(adminMenuRoutes.map((route) => route.label), [
    "Admin Overview",
    "Users",
    "Billing Ops",
    "Ledger",
    "Runtime",
    "Support Ops"
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

test("current auth UI does not link to reserved account flows", async () => {
  const source = await readFile(new URL("../../packages/console/ui/pages/LoginPage.jsx", import.meta.url), "utf8");

  for (const path of [
    "/register",
    "/invite/accept",
    "/forgot-password",
    "/reset-password"
  ]) {
    assert.equal(source.includes(`href="${path}"`), false, `${path} must not be linked from current auth UI`);
  }
});
