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

test("all active OPL Cloud contracts declare lifecycle metadata and backlog files stay non-active", async () => {
  for (const file of await contractFiles()) {
    const contract = await readContract(file);

    assert.equal(Number.isInteger(contract.schemaVersion) && contract.schemaVersion >= 1, true, `${file} schemaVersion`);
    assert.ok(contract.owner, `${file} owner`);
    assert.ok(contract.purpose, `${file} purpose`);
    assert.ok(["current", "backlog"].includes(contract.state), `${file} state`);
    if (contract.state === "backlog") {
      assert.ok(contract.activeContract, `${file} activeContract`);
      assert.ok(Array.isArray(contract.rules), `${file} rules`);
      assert.ok(contract.rules.some((rule) => rule.includes("not current commercial commitments")), `${file} must not read as active truth`);
      continue;
    }
    assert.ok(contract.machineBoundary, `${file} machineBoundary`);
    assert.equal(contract.lifecycle?.type, "long_term_contract", `${file} lifecycle.type`);
    assert.ok(contract.lifecycle?.removalCondition, `${file} lifecycle.removalCondition`);
  }
});

test("current contracts do not preserve compatibility aliases as product truth", async () => {
  for (const file of await contractFiles()) {
    const contract = await readContract(file);
    walk(contract, (key, value) => {
      if (/compatibility.*Allowed/.test(key)) {
        assert.equal(value, false, `${file} ${key} must be false`);
      }
      assert.doesNotMatch(key, /^future.*Repos$/, `${file} must use repositoryBoundaries`);
    });
  }
});
