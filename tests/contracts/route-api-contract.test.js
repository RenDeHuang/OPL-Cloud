import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { apiRouteManifest } from "../../packages/console/api/routes/index.js";
import { consoleRoutes } from "../../packages/console/ui/consoleRoutes.js";

const contractPath = new URL("../../packages/contracts/opl-cloud-route-api-contract.json", import.meta.url);
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

test("OPL Cloud route/API contract is the long-term Console boundary map", async () => {
  const contract = await readJson(contractPath);

  assert.equal(contract.schemaVersion, 1);
  assert.equal(contract.owner, "OPL Console");
  assert.equal(contract.purpose, "Commercial route, permission, page, API client, server route, and service boundary map.");
  assert.deepEqual(contract.repositoryBoundaries, ["opl-console", "opl-fabric", "opl-ledger"]);
  assert.deepEqual(contract.statuses, ["implemented", "folded_into_parent", "reserved"]);
  assert.deepEqual(contract.routeKinds, ["static_content", "auth_flow", "read_model", "business_object", "policy_or_approval_object"]);
  assert.deepEqual(contract.contractLifecycles, ["long_term", "long_term_gap", "folded_parent", "dynamic_prune"]);
  assert.ok(contract.boundaryRules.includes("Console may call Fabric only through package boundary exports or published service APIs."));
  assert.ok(contract.boundaryRules.includes("Console may call Ledger only through package boundary exports or published service APIs."));
  assert.ok(contract.boundaryRules.includes("Reserved routes are product route space, not implemented business capability."));
  assert.ok(contract.boundaryRules.includes("Static content may remain API-free; dynamic control-plane objects must not claim implemented status without read/write/action evidence."));
});

test("every UI route is represented in the route/API contract with ownership and status", async () => {
  const contract = await readJson(contractPath);
  const contractRoutes = expectedRoutesFromContract(contract);
  const byPath = new Map(contractRoutes.map((route) => [route.path, route]));

  assert.equal(contractRoutes.length, consoleRoutes.length);
  for (const route of consoleRoutes) {
    const entry = byPath.get(route.path);
    assert.ok(entry, `missing contract entry for ${route.path}`);
    assert.equal(entry.area, route.area, `area mismatch for ${route.path}`);
    assert.ok(contract.statuses.includes(entry.status), `invalid status for ${route.path}`);
    assert.ok(["opl-console", "opl-fabric", "opl-ledger"].includes(entry.ownerRepo), `invalid ownerRepo for ${route.path}`);
    assert.ok(contract.routeKinds.includes(entry.routeKind), `invalid routeKind for ${route.path}`);
    assert.ok(contract.contractLifecycles.includes(entry.contractLifecycle), `invalid contractLifecycle for ${route.path}`);
    assert.ok(Array.isArray(entry.capabilities), `missing capabilities for ${route.path}`);
  }
});

test("implemented routes name a page, API client, server route, and service boundary", async () => {
  const contract = await readJson(contractPath);
  const routes = expectedRoutesFromContract(contract).filter((route) => route.status === "implemented");
  const routeTablePaths = new Set(consoleRoutes.map((route) => route.path));
  const serverRoutes = new Set(apiRouteManifest);

  assert.ok(routes.length > 0, "contract should mark real routes as implemented");
  for (const route of routes) {
    assert.ok(route.pageModule, `implemented route ${route.path} must name pageModule`);
    assert.ok(route.apiClient, `implemented route ${route.path} must name apiClient`);
    assert.ok(Array.isArray(route.apiRoutes), `implemented route ${route.path} must list apiRoutes`);
    assert.ok(route.apiRoutes.length > 0, `implemented route ${route.path} must list at least one apiRoute`);
    assert.ok(route.serviceBoundary, `implemented route ${route.path} must name serviceBoundary`);
    assert.equal(route.contractLifecycle, "long_term", `implemented route ${route.path} must be long_term`);
    assert.ok(route.capabilities.includes("read"), `implemented route ${route.path} must include read capability`);
    assert.ok(routeTablePaths.has(route.path), `route table missing ${route.path}`);
    await readFile(new URL(route.pageModule, repoRoot), "utf8");
    await readFile(new URL(route.apiClient, repoRoot), "utf8");
    for (const apiRoute of route.apiRoutes) {
      assert.ok(serverRoutes.has(apiRoute), `server route missing for ${route.path}: ${apiRoute}`);
    }
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

test("implemented dynamic business routes declare object kind and object capabilities", async () => {
  const contract = await readJson(contractPath);
  const routes = expectedRoutesFromContract(contract).filter((route) => (
    route.status === "implemented"
    && ["business_object", "policy_or_approval_object"].includes(route.routeKind)
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

test("unimplemented dynamic routes have explicit lifecycle and prune routes stay hidden", async () => {
  const contract = await readJson(contractPath);
  const routes = expectedRoutesFromContract(contract).filter((route) => (
    route.status !== "implemented"
    && ["business_object", "policy_or_approval_object"].includes(route.routeKind)
  ));

  assert.ok(routes.length > 0, "contract should classify unimplemented dynamic routes");
  for (const route of routes) {
    assert.ok(["long_term_gap", "folded_parent", "dynamic_prune"].includes(route.contractLifecycle), `${route.path} must explain lifecycle`);
    assert.ok(route.reason, `${route.path} must explain why it is not implemented`);
    if (route.contractLifecycle === "dynamic_prune") {
      assert.equal(route.status, "reserved", `${route.path} dynamic prune must stay reserved`);
      assert.match(route.reason, /prune/, `${route.path} dynamic prune reason must include prune`);
    }
  }
});

test("known route shells and future approval objects do not claim implementation", async () => {
  const contract = await readJson(contractPath);
  const routesByPath = new Map(expectedRoutesFromContract(contract).map((route) => [route.path, route]));

  for (const path of [
    "/register",
    "/invite/accept",
    "/console/approvals",
    "/console/resources/connectors",
    "/console/resources/agents",
    "/admin/fabric/connectors",
    "/admin/fabric/agents",
    "/admin/ledger/policies"
  ]) {
    const route = routesByPath.get(path);
    assert.ok(route, `missing ${path}`);
    assert.notEqual(route.status, "implemented", `${path} must not claim implemented status`);
    assert.ok(["long_term_gap", "folded_parent"].includes(route.contractLifecycle), `${path} must remain an explicit gap or folded view`);
  }
});
