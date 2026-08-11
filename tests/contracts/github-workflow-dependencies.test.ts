import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
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

test("every external GitHub workflow dependency is pinned to one immutable SHA", async () => {
  const workflowFiles = (await readdir(repoFile(".github/workflows")))
    .filter((name) => name.endsWith(".yml") || name.endsWith(".yaml"));
  const observedRefs = new Set();

  for (const name of workflowFiles) {
    const workflow = parse(await readFile(repoFile(`.github/workflows/${name}`), "utf8"));
    for (const ref of collectUses(workflow)) {
      if (ref.startsWith("./")) continue;
      assert.match(ref, /^[^@]+@[0-9a-f]{40}$/, `${name} contains mutable or malformed uses ref ${ref}`);
      observedRefs.add(ref);
    }
  }

  assert.ok(observedRefs.size > 0);
});
