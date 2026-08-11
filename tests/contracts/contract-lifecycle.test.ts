import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import test from "node:test";

const contractsDir = new URL("../../packages/contracts/", import.meta.url);

async function contractFiles() {
  return (await readdir(contractsDir))
    .filter((file) => file.endsWith(".json"))
    .sort();
}

async function readContract(file) {
  return JSON.parse(await readFile(new URL(file, contractsDir), "utf8"));
}

function walk(value, visit) {
  if (!value || typeof value !== "object") return;
  if (Array.isArray(value)) {
    for (const item of value) walk(item, visit);
    return;
  }
  for (const [key, child] of Object.entries(value)) {
    visit(key, child);
    walk(child, visit);
  }
}

test("all OPL Cloud contracts declare lifecycle metadata", async () => {
  for (const file of await contractFiles()) {
    const contract = await readContract(file);

    assert.equal(Number.isInteger(contract.schemaVersion) && contract.schemaVersion >= 1, true, `${file} schemaVersion`);
    assert.ok(contract.owner, `${file} owner`);
    assert.ok(contract.purpose, `${file} purpose`);
    assert.ok(["current", "migration", "superseded"].includes(contract.state), `${file} state`);
    assert.ok(contract.machineBoundary, `${file} machineBoundary`);
    if (contract.state === "current") assert.equal(contract.lifecycle?.type, "long_term_contract", `${file} lifecycle.type`);
    if (contract.state === "migration") assert.equal(contract.lifecycle?.type, "migration_guard", `${file} lifecycle.type`);
    if (contract.state === "superseded") assert.equal(contract.lifecycle?.type, "historical_contract", `${file} lifecycle.type`);
    if (contract.state === "superseded") assert.ok(contract.lifecycle?.supersededBy, `${file} lifecycle.supersededBy`);
    assert.ok(contract.lifecycle?.removalCondition, `${file} lifecycle.removalCondition`);
  }
});

test("Console presentation choices are not machine contracts", async () => {
  const files = await contractFiles();
  assert.equal(files.includes("opl-cloud-console-ui-contract.json"), false);

  const boundary = await readContract("opl-cloud-service-boundary-contract.json");
  assert.deepEqual(Object.keys(boundary.services.consoleUi).sort(), ["calls", "path", "persistence"]);
});

test("machine contracts do not own presentation taste or mutable delivery status", async () => {
  const forbiddenKeys = new Set([
    "componentLibrary",
    "currentBranchScope",
    "currentImplementation",
    "currentState",
    "deliveryEvidence",
    "deploymentEvidence",
    "designModel",
    "homeLoginLogo",
    "imageHash",
    "implementation",
    "implementationState",
    "launchStatus",
    "pageCount",
    "productionEvidence",
    "publicEndpointDisplay",
    "realEnvironmentEvidence",
    "releaseEvidence",
    "slideCount",
    "visualDirection"
  ]);

  for (const file of await contractFiles()) {
    const contract = await readContract(file);
    walk(contract, (key) => {
      assert.equal(forbiddenKeys.has(key), false, `${file} must not own ${key}`);
    });
  }
});

test("current contracts do not preserve compatibility aliases as product truth", async () => {
  for (const file of await contractFiles()) {
    const contract = await readContract(file);
    if (contract.state !== "current") continue;
    walk(contract, (key, value) => {
      if (/compatibility.*Allowed/.test(key)) {
        assert.equal(value, false, `${file} ${key} must be false`);
      }
      assert.doesNotMatch(key, /^future.*Repos$/, `${file} must use repositoryBoundaries`);
    });
  }
});
