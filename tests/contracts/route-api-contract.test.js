import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { apiRouteManifest } from "../../packages/console/api/routes/index.js";
import { consoleRoutes, routeTo, routesById } from "../../packages/console/ui/consoleRoutes.js";

const contractPath = new URL("../../packages/contracts/opl-cloud-route-api-contract.json", import.meta.url);
const backlogPath = new URL("../../packages/contracts/opl-cloud-route-backlog.json", import.meta.url);
const repoRoot = new URL("../../", import.meta.url);

async function readJson(path) {
  return JSON.parse(await readFile(path, "utf8"));
}

function expectedRoutesFromContract(contract) {
  return [
    ...(contract.publicRoutes || []),
    ...(contract.authRoutes || []),
    ...(contract.consoleRoutes || []),
    ...(contract.adminRoutes || []),
    ...(contract.errorRoutes || [])
  ];
}

test("OPL Cloud route/API contract is the current commercial Console truth", async () => {
  const contract = await readJson(contractPath);

  assert.equal(contract.schemaVersion, 1);
  assert.equal(contract.owner, "OPL Console");
  assert.equal(contract.purpose, "Commercial route, permission, page, API client, server route, and service boundary map.");
  assert.deepEqual(contract.repositoryBoundaries, ["opl-console", "opl-fabric", "opl-ledger"]);
  assert.deepEqual(contract.statuses, ["implemented", "folded_into_parent", "external"]);
  assert.deepEqual(contract.routeKinds, ["static_content", "auth_flow", "read_model", "business_object", "external_integration"]);
  assert.deepEqual(contract.contractLifecycles, ["current", "folded_parent"]);
  assert.ok(contract.boundaryRules.includes("Console may call Fabric only through package boundary exports or published service APIs."));
  assert.ok(contract.boundaryRules.includes("Console may call Ledger only through package boundary exports or published service APIs."));
  assert.ok(contract.boundaryRules.includes("Active route contract contains only current commercial truth; future, reserved, and prune candidates live in route backlog."));
  assert.ok(contract.boundaryRules.includes("Every enabled route has a stable route id used by menus, actions, and routeTo()."));
});

test("every UI route is represented in the route/API contract with ownership and status", async () => {
  const contract = await readJson(contractPath);
  const contractRoutes = expectedRoutesFromContract(contract);
  const byPath = new Map(contractRoutes.map((route) => [route.path, route]));
  const byId = new Map(contractRoutes.map((route) => [route.id, route]));

  assert.equal(contractRoutes.length, consoleRoutes.length);
  for (const route of consoleRoutes) {
    const entry = byPath.get(route.path);
    assert.ok(entry, `missing contract entry for ${route.path}`);
    assert.equal(entry.id, route.id, `id mismatch for ${route.path}`);
    assert.equal(entry.area, route.area, `area mismatch for ${route.path}`);
    assert.ok(contract.statuses.includes(entry.status), `invalid status for ${route.path}`);
    assert.ok(["opl-console", "opl-fabric", "opl-ledger"].includes(entry.ownerRepo), `invalid ownerRepo for ${route.path}`);
    assert.ok(contract.routeKinds.includes(entry.routeKind), `invalid routeKind for ${route.path}`);
    assert.ok(contract.contractLifecycles.includes(entry.contractLifecycle), `invalid contractLifecycle for ${route.path}`);
    assert.ok(Array.isArray(entry.capabilities), `missing capabilities for ${route.path}`);
    assert.ok(byId.has(route.id), `missing contract route id ${route.id}`);
    assert.ok(routesById.has(route.id), `runtime route registry missing ${route.id}`);
  }
});

test("active route contract excludes future, reserved, and prune route shells", async () => {
  const contract = await readJson(contractPath);
  const backlog = await readJson(backlogPath);
  const activeRoutes = expectedRoutesFromContract(contract);
  const activePaths = new Set(activeRoutes.map((route) => route.path));
  const backlogPaths = new Set((backlog.routes || []).map((route) => route.path));

  assert.equal(activeRoutes.some((route) => route.status === "reserved"), false, "active contract must not include reserved routes");
  assert.equal(activeRoutes.some((route) => route.contractLifecycle === "long_term_gap"), false, "active contract must not include long-term gaps");
  assert.equal(activeRoutes.some((route) => route.contractLifecycle === "dynamic_prune"), false, "active contract must not include prune candidates");

  for (const path of [
    "/register",
    "/invite/accept",
    "/email/verify",
    "/forgot-password",
    "/reset-password",
    "/console/resources/connectors",
    "/console/approvals",
    "/admin/fabric/connectors",
    "/admin/ledger/policies",
    "/admin/runtime/kubernetes"
  ]) {
    assert.equal(activePaths.has(path), false, `${path} must be removed from active contract`);
    assert.equal(backlogPaths.has(path), true, `${path} must be tracked in route backlog`);
  }
});

test("implemented routes name a page, API client, server route, and service boundary", async () => {
  const contract = await readJson(contractPath);
  const routes = expectedRoutesFromContract(contract).filter((route) => route.status === "implemented");
  const routeTablePaths = new Set(consoleRoutes.map((route) => route.path));
  const serverRoutes = new Set(apiRouteManifest);

  assert.ok(routes.length > 0, "contract should mark real routes as implemented");
  for (const route of routes) {
    assert.ok(route.id, `implemented route ${route.path} must name stable route id`);
    assert.ok(route.pageModule, `implemented route ${route.path} must name pageModule`);
    if (route.routeKind !== "static_content" && route.routeKind !== "external_integration") {
      assert.ok(route.apiClient, `implemented route ${route.path} must name apiClient`);
      assert.ok(Array.isArray(route.apiRoutes), `implemented route ${route.path} must list apiRoutes`);
      assert.ok(route.apiRoutes.length > 0, `implemented route ${route.path} must list at least one apiRoute`);
      await readFile(new URL(route.apiClient, repoRoot), "utf8");
      for (const apiRoute of route.apiRoutes) {
        assert.ok(serverRoutes.has(apiRoute), `server route missing for ${route.path}: ${apiRoute}`);
      }
    }
    assert.ok(route.serviceBoundary, `implemented route ${route.path} must name serviceBoundary`);
    assert.equal(route.contractLifecycle, "current", `implemented route ${route.path} must be current`);
    assert.ok(route.capabilities.includes("read"), `implemented route ${route.path} must include read capability`);
    assert.ok(routeTablePaths.has(route.path), `route table missing ${route.path}`);
    await readFile(new URL(route.pageModule, repoRoot), "utf8");
  }
});

test("implemented read-model routes declare read capability and use GET APIs", async () => {
  const contract = await readJson(contractPath);
  const routes = expectedRoutesFromContract(contract).filter((route) => route.status === "implemented" && route.routeKind === "read_model");

  assert.ok(routes.length > 0, "contract should include implemented read-model routes");
  for (const route of routes) {
    assert.ok(route.capabilities.includes("read"), `${route.path} must declare read capability`);
    assert.ok(route.apiRoutes.some((apiRoute) => apiRoute.startsWith("GET ")), `${route.path} must include a read API`);
  }
});

test("implemented auth flows use auth APIs and declare auth/session capabilities", async () => {
  const contract = await readJson(contractPath);
  const routes = expectedRoutesFromContract(contract).filter((route) => route.status === "implemented" && route.routeKind === "auth_flow");

  assert.ok(routes.length > 0, "contract should include implemented auth flows");
  for (const route of routes) {
    assert.ok(route.capabilities.includes("authenticate") || route.capabilities.includes("session"), `${route.path} must declare auth capability`);
    assert.ok(route.apiRoutes.every((apiRoute) => apiRoute.includes(" /api/auth/")), `${route.path} must use auth API routes`);
  }
});

test("implemented commercial object routes declare object kind and object capabilities", async () => {
  const contract = await readJson(contractPath);
  const routes = expectedRoutesFromContract(contract).filter((route) => (
    route.status === "implemented"
    && ["business_object", "read_model"].includes(route.routeKind)
    && route.objectKind
  ));

  assert.ok(routes.length > 0, "contract should include implemented business routes");
  for (const route of routes) {
    assert.ok(route.objectKind, `${route.path} must declare objectKind`);
    assert.ok(route.capabilities.includes("read"), `${route.path} must declare read capability`);
    assert.ok(
      route.capabilities.some((capability) => ["list", "detail", "write", "action", "approve", "reject", "review", "evidence", "audit"].includes(capability)),
      `${route.path} must declare a business object capability`
    );
  }
});

test("route ids generate runtime paths through routeTo", async () => {
  const contract = await readJson(contractPath);
  const routes = expectedRoutesFromContract(contract);

  for (const route of routes) {
    const params = Object.fromEntries([...route.path.matchAll(/:([^/]+)/g)].map(([, key]) => [key, `${key}-demo`]));
    assert.equal(routeTo(route.id, params, { role: route.requiresAdmin ? "admin" : "lab_owner" }), route.path.replace(/:([^/]+)/g, (_, key) => params[key]));
  }

  assert.throws(() => routeTo("workspace.detail"), /missing route param: id/);
  assert.throws(() => routeTo("admin.users", {}, { role: "lab_owner" }), /not allowed/);
});
