import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { consoleRoutes } from "../../packages/console/ui/consoleRoutes.js";

const contractPath = new URL("../../packages/contracts/opl-cloud-route-api-contract.json", import.meta.url);
const repoRoot = new URL("../../", import.meta.url);

async function readJson(path) {
  return JSON.parse(await readFile(path, "utf8"));
}

async function source(relativePath) {
  return readFile(new URL(relativePath, repoRoot), "utf8");
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

function serverRoutePattern(route) {
  const [method, pathname] = route.split(" ");
  const escapedPath = pathname.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`["']${method}\\s+${escapedPath}["']|method:\\s*["']${method}["'][\\s\\S]{0,120}path:\\s*["']${escapedPath}["']`);
}

test("OPL Cloud route/API contract is the long-term Console boundary map", async () => {
  const contract = await readJson(contractPath);

  assert.equal(contract.schemaVersion, 1);
  assert.equal(contract.owner, "OPL Console");
  assert.equal(contract.purpose, "Route, page, API client, server route, service, and future repo ownership boundary.");
  assert.deepEqual(contract.futureRepos, ["opl-console", "opl-fabric", "opl-ledger"]);
  assert.deepEqual(contract.statuses, ["implemented", "folded_into_parent", "placeholder_hidden"]);
  assert.ok(contract.boundaryRules.includes("Console may call Fabric only through package boundary exports or future service APIs."));
  assert.ok(contract.boundaryRules.includes("Console may call Ledger only through package boundary exports or future service APIs."));
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
  }
});

test("implemented routes name a page, API client, server route, and service boundary", async () => {
  const contract = await readJson(contractPath);
  const routes = expectedRoutesFromContract(contract).filter((route) => route.status === "implemented");
  const serverRoutes = await source("packages/console/api/routes/index.js");
  const consoleRoutesSource = await source("packages/console/ui/consoleRoutes.js");

  assert.ok(routes.length > 0, "contract should mark real routes as implemented");
  for (const route of routes) {
    assert.ok(route.pageModule, `implemented route ${route.path} must name pageModule`);
    assert.ok(route.apiClient, `implemented route ${route.path} must name apiClient`);
    assert.ok(Array.isArray(route.apiRoutes), `implemented route ${route.path} must list apiRoutes`);
    assert.ok(route.apiRoutes.length > 0, `implemented route ${route.path} must list at least one apiRoute`);
    assert.ok(route.serviceBoundary, `implemented route ${route.path} must name serviceBoundary`);
    assert.match(consoleRoutesSource, new RegExp(`path:\\s*["']${route.path.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}["']`));
    await source(route.pageModule);
    await source(route.apiClient);
    for (const apiRoute of route.apiRoutes) {
      assert.match(serverRoutes, serverRoutePattern(apiRoute), `server route missing for ${route.path}: ${apiRoute}`);
    }
  }
});
