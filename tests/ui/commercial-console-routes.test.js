import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { adminMenuRoutes, consoleRoutes, ownerMenuRoutes } from "../../packages/console/ui/consoleRoutes.js";

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

test("commercial Console route contract covers current public, auth, owner, and admin surfaces", async () => {
  const contract = await readContract();
  const routes = allContractRoutes(contract);
  const byPath = new Map(routes.map((route) => [route.path, route]));

  for (const [path, status] of [
    ["/", "folded_into_parent"],
    ["/pricing", "folded_into_parent"],
    ["/docs", "folded_into_parent"],
    ["/status", "folded_into_parent"],
    ["/login", "implemented"],
    ["/console/overview", "implemented"],
    ["/console/workspaces", "implemented"],
    ["/console/workspaces/new", "implemented"],
    ["/console/workspaces/:id", "implemented"],
    ["/console/gateway", "implemented"],
    ["/console/billing", "implemented"],
    ["/console/account", "implemented"],
    ["/console/support", "implemented"],
    ["/console/support/new", "implemented"],
    ["/console/support/:id", "implemented"],
    ["/console/alerts", "implemented"],
    ["/admin/overview", "implemented"],
    ["/admin/users", "implemented"],
    ["/admin/billing", "implemented"],
    ["/admin/ledger", "implemented"],
    ["/admin/runtime", "implemented"],
    ["/admin/support", "implemented"]
  ]) {
    assert.equal(byPath.get(path)?.status, status, `${path} must have current commercial route status ${status}`);
  }

  for (const path of [
    "/register",
    "/invite/accept",
    "/email/verify",
    "/forgot-password",
    "/reset-password",
    "/admin/runtime/readiness",
    "/admin/ledger/events"
  ]) {
    assert.equal(byPath.get(path)?.status, "reserved", `${path} must be reserved until backed by implementation`);
  }
});

test("Lab Owner menu is commercial and excludes operator surfaces", () => {
  assert.deepEqual(ownerMenuRoutes.map((route) => route.label), [
    "Overview",
    "Workspaces",
    "Gateway",
    "Billing",
    "Account & Lab",
    "Support",
    "Alerts"
  ]);

  for (const route of ownerMenuRoutes) {
    assert.equal(route.area, "console");
    assert.equal(route.requiresAuth, true);
    assert.notEqual(route.requiresAdmin, true);
    assert.equal(route.adminMenu, undefined);
  }
});

test("Admin menu owns operator surfaces behind admin permission", () => {
  assert.deepEqual(adminMenuRoutes.map((route) => route.label), [
    "Admin Overview",
    "Users",
    "Governance",
    "All Workspaces",
    "Billing Ops",
    "Gateway Ops",
    "Fabric",
    "Ledger",
    "Runtime",
    "Support Ops",
    "Audit",
    "Settings"
  ]);

  for (const route of adminMenuRoutes) {
    assert.equal(route.area, "admin");
    assert.equal(route.requiresAuth, true);
    assert.equal(route.requiresAdmin, true);
  }
});

test("route table does not expose reserved routes in visible owner or admin menus", () => {
  const visiblePaths = new Set([...ownerMenuRoutes, ...adminMenuRoutes].map((route) => route.path));
  const reservedPaths = consoleRoutes
    .filter((route) => route.hiddenInMenu || route.featureGate)
    .map((route) => route.path);

  for (const path of reservedPaths) {
    assert.equal(visiblePaths.has(path), false, `${path} must stay out of visible menus`);
  }
});
