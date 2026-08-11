import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import test from "node:test";
import { parse } from "yaml";

const repoFile = (path) => new URL(`../../${path}`, import.meta.url);

function collectUses(value, refs = []) {
  if (Array.isArray(value)) {
    for (const item of value) collectUses(item, refs);
    return refs;
  }
  if (!value || typeof value !== "object") return refs;
  for (const [key, item] of Object.entries(value)) {
    if (key === "uses" && typeof item === "string") refs.push(item);
    else collectUses(item, refs);
  }
  return refs;
}

test("every external GitHub workflow dependency is registered and pinned to one immutable SHA", async () => {
  const contract = JSON.parse(await readFile(repoFile("packages/contracts/opl-cloud-deployment-contract.json"), "utf8"));
  const dependencies = contract.immutableGithubDependencies;
  const registeredRefs = new Set(Object.values(dependencies).map((dependency) => dependency.ref));
  const observedRefs = new Set();
  const workflowFiles = (await readdir(repoFile(".github/workflows")))
    .filter((name) => name.endsWith(".yml") || name.endsWith(".yaml"));

  for (const name of workflowFiles) {
    const workflow = parse(await readFile(repoFile(`.github/workflows/${name}`), "utf8"));
    for (const ref of collectUses(workflow)) {
      if (ref.startsWith("./")) continue;
      assert.match(ref, /^[^@]+@[0-9a-f]{40}$/, `${name} contains mutable or malformed uses ref ${ref}`);
      assert.ok(registeredRefs.has(ref), `${name} contains unregistered external dependency ${ref}`);
      observedRefs.add(ref);
    }
  }

  assert.deepEqual([...observedRefs].sort(), [...registeredRefs].sort());
  for (const [name, dependency] of Object.entries(dependencies)) {
    assert.ok(["action", "reusable_workflow"].includes(dependency.kind), `${name} dependency kind`);
    assert.match(dependency.version, /^v?[0-9a-f][0-9A-Za-z.-]*$/, `${name} dependency version`);
    assert.ok(dependency.ref.startsWith(`${name}@`), `${name} dependency owner`);
  }
});
