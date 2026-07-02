import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const businessObjectContractPath = new URL("../../packages/contracts/opl-cloud-business-object-contract.json", import.meta.url);
const routeContractPath = new URL("../../packages/contracts/opl-cloud-route-api-contract.json", import.meta.url);

async function readJson(path) {
  return JSON.parse(await readFile(path, "utf8"));
}

function allRoutes(contract) {
  return [
    ...(contract.publicRoutes || []),
    ...(contract.authRoutes || []),
    ...(contract.consoleRoutes || []),
    ...(contract.adminRoutes || []),
    ...(contract.errorRoutes || [])
  ];
}

test("business object contract defines the long-term dynamic control-plane boundary", async () => {
  const contract = await readJson(businessObjectContractPath);

  assert.equal(contract.schemaVersion, 1);
  assert.equal(contract.owner, "OPL Console");
  assert.equal(contract.purpose, "Machine-readable requirements for dynamic control-plane route objects.");
  assert.deepEqual(contract.futureRepos, ["opl-console", "opl-fabric", "opl-ledger"]);
  assert.deepEqual(contract.routeKinds, ["business_object", "policy_or_approval_object"]);
  assert.ok(contract.principles.includes("Static content may remain API-free; dynamic control-plane objects need read/write/action evidence before implemented status."));
  assert.ok(contract.repoBoundaryRules.includes("Console owns UI, auth, route contracts, and read-model orchestration."));
  assert.ok(contract.repoBoundaryRules.includes("Fabric owns runtime, storage, connector, and agent resource execution boundaries."));
  assert.ok(contract.repoBoundaryRules.includes("Ledger owns evidence, audit, reconciliation, and review policy boundaries."));
});

test("route object kinds map to committed object specs and owner repos", async () => {
  const businessContract = await readJson(businessObjectContractPath);
  const routeContract = await readJson(routeContractPath);
  const objectSpecs = new Map(businessContract.objectKinds.map((object) => [object.kind, object]));

  for (const route of allRoutes(routeContract).filter((entry) => entry.objectKind)) {
    const objectSpec = objectSpecs.get(route.objectKind);
    assert.ok(objectSpec, `missing object spec for ${route.objectKind} on ${route.path}`);
    assert.equal(objectSpec.ownerRepo, route.ownerRepo, `${route.path} ownerRepo must match ${route.objectKind}`);
    assert.equal(objectSpec.routeKind, route.routeKind, `${route.path} routeKind must match ${route.objectKind}`);
  }
});

test("implemented dynamic objects satisfy capability requirements across their route cluster", async () => {
  const businessContract = await readJson(businessObjectContractPath);
  const routeContract = await readJson(routeContractPath);
  const objectSpecs = new Map(businessContract.objectKinds.map((object) => [object.kind, object]));
  const dynamicRoutes = allRoutes(routeContract).filter((route) => (
    route.status === "implemented"
    && businessContract.routeKinds.includes(route.routeKind)
  ));

  assert.ok(dynamicRoutes.length > 0, "there should be implemented dynamic routes");
  const implementedObjectKinds = new Set(dynamicRoutes.map((route) => route.objectKind));
  for (const objectKind of implementedObjectKinds) {
    const spec = objectSpecs.get(objectKind);
    assert.ok(spec, `${objectKind} must map to an object spec`);
    const routes = allRoutes(routeContract).filter((route) => route.objectKind === objectKind && route.contractLifecycle !== "dynamic_prune");
    const capabilities = new Set(routes.flatMap((route) => route.capabilities || []));
    for (const capability of spec.requiredCapabilitiesForImplemented) {
      assert.ok(capabilities.has(capability), `${objectKind} missing ${capability} capability`);
    }
    if (spec.evidenceRequired) {
      assert.ok(capabilities.has("audit") || capabilities.has("evidence"), `${objectKind} must include audit/evidence`);
    }
  }
});

test("long-term gaps are committed objects but do not claim implementation", async () => {
  const businessContract = await readJson(businessObjectContractPath);
  const routeContract = await readJson(routeContractPath);
  const objectSpecs = new Map(businessContract.objectKinds.map((object) => [object.kind, object]));
  const gapRoutes = allRoutes(routeContract).filter((route) => route.contractLifecycle === "long_term_gap");

  assert.ok(gapRoutes.length > 0, "there should be long-term product gaps");
  for (const route of gapRoutes) {
    assert.notEqual(route.status, "implemented", `${route.path} long-term gap must not be implemented`);
    if (businessContract.routeKinds.includes(route.routeKind)) {
      assert.ok(objectSpecs.has(route.objectKind), `${route.path} must map long-term gap to object spec`);
    }
  }
});

test("dynamic prune routes stay hidden and are not treated as committed product objects", async () => {
  const businessContract = await readJson(businessObjectContractPath);
  const routeContract = await readJson(routeContractPath);
  const pruneRoutes = allRoutes(routeContract).filter((route) => route.contractLifecycle === "dynamic_prune");

  assert.ok(pruneRoutes.length > 0, "contract should identify dynamic prune candidates");
  for (const route of pruneRoutes) {
    assert.equal(route.status, "placeholder_hidden", `${route.path} prune route must be hidden`);
    assert.match(route.reason, /prune/, `${route.path} prune reason must include prune`);
    if (businessContract.routeKinds.includes(route.routeKind)) {
      const objectSpec = businessContract.objectKinds.find((object) => object.kind === route.objectKind);
      assert.ok(objectSpec, `${route.path} must still map to an object spec while it exists`);
      assert.equal(objectSpec.lifecycle, "dynamic_prune", `${route.path} object spec must be dynamic_prune`);
    }
  }
});
