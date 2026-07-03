import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import { join, relative } from "node:path";
import test from "node:test";

const repoRoot = new URL("../../", import.meta.url);

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

test("Console imports Fabric only through stable package boundary exports", async () => {
  const consoleFiles = await files("packages/console");
  for (const file of consoleFiles) {
    const src = await source(file);
    assert.doesNotMatch(
      src,
      /from\s+["'][^"']*fabric\/src\/(?!index\.js)/,
      `${file} must import Fabric through fabric/src/index.js`
    );
  }
});

test("Console imports Ledger only through stable package boundary exports", async () => {
  const consoleFiles = await files("packages/console");
  for (const file of consoleFiles) {
    const src = await source(file);
    assert.doesNotMatch(
      src,
      /from\s+["']\.\.\/\.\.\/ledger\/src\/(?!index\.js)/,
      `${file} must not import Ledger internals directly`
    );
  }
});

test("server routes and OPL Cloud facade are split by domain boundaries", async () => {
  for (const file of [
    "packages/console/api/routes/index.js",
    "packages/console/api/routes/auth-routes.js",
    "packages/console/api/routes/workspace-routes.js",
    "packages/console/api/routes/billing-routes.js",
    "packages/console/api/routes/admin-routes.js",
    "packages/console/api/routes/ledger-routes.js",
    "packages/console/api/routes/runtime-routes.js",
    "packages/console/src/services/workspace-lifecycle-service.js",
    "packages/console/src/services/billing-service.js",
    "packages/console/src/services/ledger-evidence-service.js",
    "packages/console/src/services/console-read-model-service.js",
    "packages/console/src/services/runtime-operation-service.js"
  ]) {
    assert.ok(await source(file), `${file} should exist`);
  }

  const facade = await source("packages/console/src/opl-cloud.js");
  assert.match(facade, /WorkspaceLifecycleService/);
  assert.match(facade, /BillingService/);
  assert.match(facade, /LedgerEvidenceService/);
  assert.match(facade, /ConsoleReadModelService/);
  assert.match(facade, /RuntimeOperationService/);
});
