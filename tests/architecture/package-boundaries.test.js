import assert from "node:assert/strict";
import { access, readdir, readFile } from "node:fs/promises";
import { join, relative } from "node:path";
import test from "node:test";

const repoRoot = new URL("../../", import.meta.url);
const contractPath = new URL("../../packages/contracts/opl-cloud-package-boundary-contract.json", import.meta.url);

async function contract() {
  return JSON.parse(await readFile(contractPath, "utf8"));
}

async function files(dir) {
  const current = new URL(dir, repoRoot);
  const entries = await readdir(current, { withFileTypes: true });
  const out = [];
  for (const entry of entries) {
    const child = join(current.pathname, entry.name);
    if (entry.isDirectory()) {
      out.push(...await files(relative(repoRoot.pathname, child)));
    } else if (/\.(js|jsx)$/.test(entry.name)) {
      out.push(relative(repoRoot.pathname, child));
    }
  }
  return out;
}

async function source(relativePath) {
  return readFile(new URL(relativePath, repoRoot), "utf8");
}

async function assertFile(relativePath) {
  await access(new URL(relativePath, repoRoot));
}

test("Console imports Fabric and Ledger only through package boundary exports", async () => {
  const spec = await contract();
  const consoleFiles = await files("packages/console");

  for (const rule of spec.consoleImportRules) {
    const pattern = new RegExp(rule.forbiddenImportPattern);
    for (const file of consoleFiles) {
      assert.doesNotMatch(
        await source(file),
        pattern,
        `${file} must import ${rule.package} through ${rule.allowedBoundary}`
      );
    }
  }
});

test("server routes and OPL Cloud facade follow the package boundary contract", async () => {
  const spec = await contract();
  for (const file of [...spec.apiRouteModules, ...spec.consoleServiceModules]) {
    await assertFile(file);
  }

  const facade = await source("packages/console/src/opl-cloud.js");
  for (const delegate of spec.facadeDelegates) {
    assert.match(facade, new RegExp(delegate));
  }
});
