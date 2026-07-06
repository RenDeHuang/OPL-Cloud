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
    } else if (/\.(js|jsx|ts|tsx)$/.test(entry.name)) {
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

function forbiddenMarkerPattern(marker) {
  if (marker === "pg") return /(^|[^A-Za-z0-9_])pg([^A-Za-z0-9_]|$)/i;
  const escaped = marker.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(escaped, "i");
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

test("legacy Node package boundary no longer requires API routes or facade delegates", async () => {
  const spec = await contract();
  assert.deepEqual(spec.apiRouteModules, []);
  assert.deepEqual(spec.consoleServiceModules, []);
  assert.deepEqual(spec.facadeDelegates, []);
});

test("target service boundaries assign persistence, cloud SDKs, and UI responsibilities", async () => {
  const boundary = JSON.parse(
    await readFile(
      new URL("../../packages/contracts/opl-cloud-service-boundary-contract.json", import.meta.url),
      "utf8"
    )
  );

  assert.equal(boundary.state, "current");
  assert.equal(boundary.services.consoleUi.persistence, "none");
  assert.equal(boundary.services.ledger.persistence, "postgresql");
  assert.equal(boundary.services.fabric.cloudSdkOwner, true);
  assert.equal(boundary.services.controlPlane.calls.ledger, "http");
  assert.equal(boundary.services.controlPlane.calls.fabric, "http");
  assert.equal(boundary.migration.compatibilityLayer, "forbidden");
  assert.ok(boundary.forbiddenRuntimeMarkers.consoleUi.includes("pg"));
  assert.ok(boundary.forbiddenRuntimeMarkers.controlPlane.includes("tencentcloud"));
  assert.ok(boundary.secretPolicy.forbiddenEvidenceMarkers.includes("token"));
});

test("Console UI is a browser-only app with no persistence or cloud SDK markers", async () => {
  const boundary = JSON.parse(
    await readFile(
      new URL("../../packages/contracts/opl-cloud-service-boundary-contract.json", import.meta.url),
      "utf8"
    )
  );
  const uiFiles = await files("apps/console-ui/src");
  assert.ok(uiFiles.length > 0, "apps/console-ui/src must contain UI source files");

  for (const file of uiFiles) {
    const text = await source(file);
    for (const marker of boundary.forbiddenRuntimeMarkers.consoleUi) {
      assert.doesNotMatch(text, forbiddenMarkerPattern(marker), `${file} must not contain ${marker}`);
    }
  }
});

test("Console UI source is TypeScript-only with no ts-nocheck escape hatches", async () => {
  const uiFiles = await files("apps/console-ui/src");
  assert.ok(uiFiles.length > 0, "apps/console-ui/src must contain UI source files");

  for (const file of uiFiles) {
    assert.doesNotMatch(file, /\.(js|jsx)$/, `${file} must be migrated to TS/TSX`);
    assert.doesNotMatch(await source(file), /@ts-nocheck|migrated JS/, `${file} must not keep migration type bypasses`);
  }
});

test("retired Node Console API and store are not kept as a compatibility layer", async () => {
  const packageJson = JSON.parse(await readFile(new URL("../../package.json", import.meta.url), "utf8"));

  await assert.rejects(() => access(new URL("../../packages/console/api/server.ts", import.meta.url)));
  await assert.rejects(() => access(new URL("../../packages/console/src/store.ts", import.meta.url)));
  assert.doesNotMatch(packageJson.scripts.start, /node\s+packages\/console\/api\/server\.js/);
});
