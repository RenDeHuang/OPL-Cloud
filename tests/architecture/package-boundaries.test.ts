import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { access, readdir, readFile } from "node:fs/promises";
import { join, relative } from "node:path";
import test from "node:test";
import { promisify } from "node:util";

const repoRoot = new URL("../../", import.meta.url);
const contractPath = new URL("../../packages/contracts/opl-cloud-package-boundary-contract.json", import.meta.url);
const execFileAsync = promisify(execFile);

async function contract() {
  return JSON.parse(await readFile(contractPath, "utf8"));
}

async function files(dir, pattern = /\.(js|jsx|ts|tsx)$/) {
  const current = new URL(dir.endsWith("/") ? dir : `${dir}/`, repoRoot);
  let entries = [];
  try {
    entries = await readdir(current, { withFileTypes: true });
  } catch (error) {
    if (error?.code === "ENOENT") return [];
    throw error;
  }
  const out = [];
  for (const entry of entries) {
    const child = join(current.pathname, entry.name);
    if (entry.isDirectory()) {
      out.push(...await files(relative(repoRoot.pathname, child)));
    } else if (pattern.test(entry.name)) {
      out.push(relative(repoRoot.pathname, child));
    }
  }
  return out;
}

async function source(relativePath) {
  return readFile(new URL(relativePath, repoRoot), "utf8");
}

async function sourceIfExists(relativePath) {
  try {
    return await source(relativePath);
  } catch (error) {
    if (error?.code === "ENOENT") return null;
    throw error;
  }
}

async function assertFile(relativePath) {
  await access(new URL(relativePath, repoRoot));
}

async function gitFiles(args) {
  const { stdout } = await execFileAsync("git", ["ls-files", ...args], { cwd: repoRoot.pathname });
  return stdout.trim().split("\n").filter(Boolean);
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

test("packages/console does not keep business service implementations or compatibility facades", async () => {
  await assert.rejects(() => access(new URL("../../packages/console", import.meta.url)));
  await assert.rejects(() => access(new URL("../../packages/console/src/services", import.meta.url)));
});

test("packages contains contracts only, not Fabric or Ledger implementation packages", async () => {
  await assertFile("packages/contracts/opl-cloud-service-boundary-contract.json");
  await assert.rejects(() => access(new URL("../../packages/fabric", import.meta.url)));
  await assert.rejects(() => access(new URL("../../packages/ledger", import.meta.url)));
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
  assert.equal(boundary.services.fabric.persistence, "postgresql");
  assert.equal(boundary.services.fabric.cloudSdkOwner, true);
  assert.equal(boundary.services.controlPlane.calls.ledger, "http");
  assert.equal(boundary.services.controlPlane.calls.fabric, "http");
  assert.equal(boundary.services.controlPlane.calls.sub2api, "http");
  assert.ok(boundary.services.ledger.readApis.includes("receiptQuery"));
  for (const retired of ["wallets", "holds", "manualTopups", "ledgerEntries", "walletTransactions", "resourceSettlements"]) {
    assert.equal(boundary.services.ledger.owns.includes(retired), false, `Ledger must not own ${retired}`);
  }
  assert.equal(boundary.externalServices.gateway.calls, undefined);
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

test("Control Plane does not import Fabric or Ledger internals directly", async () => {
  const controlPlaneFiles = await gitFiles(["services/control-plane/**/*.go"]);
  assert.ok(controlPlaneFiles.length > 0, "services/control-plane must contain source files");

  for (const file of controlPlaneFiles) {
    const text = await sourceIfExists(file);
    if (text == null) continue;
    assert.doesNotMatch(
      text,
      /"opl-cloud\/services\/(?:fabric|ledger)\/internal\//,
      `${file} must use clients or contracts instead of service internals`
    );
  }
});

test("Control Plane current facts are not backed by retired JSON read model symbols", async () => {
  const controlPlaneFiles = await gitFiles(["services/control-plane/**/*.go"]);
  const forbidden = [
    "ReadModelStore",
    "readModelSnapshot",
    "control_plane_read_model",
    "NewJSONReadModelStore",
    "NewMemoryStateStore",
    "memoryStateStore",
    "OPL_CONTROL_PLANE_STATE_FILE",
    "runtimeApp",
    "read_model_persist_failed"
  ];

  for (const file of controlPlaneFiles) {
    const text = await sourceIfExists(file);
    if (text == null) continue;
    for (const marker of forbidden) {
      assert.doesNotMatch(text, forbiddenMarkerPattern(marker), `${file} must not keep retired read-model marker ${marker}`);
    }
  }
});

test("Control Plane HTTP stack is directoryized by repository service handler and routes", async () => {
  const requiredFiles = [
    "services/control-plane/internal/repository/user_repository.go",
    "services/control-plane/internal/repository/resource_repository.go",
    "services/control-plane/internal/repository/workspace_repository.go",
    "services/control-plane/internal/repository/archive_repository.go",
    "services/control-plane/internal/service/auth_service.go",
    "services/control-plane/internal/service/resource_service.go",
    "services/control-plane/internal/service/workspace_service.go",
    "services/control-plane/internal/service/billing_service.go",
    "services/control-plane/internal/service/archive_service.go",
    "services/control-plane/internal/handler/auth_handler.go",
    "services/control-plane/internal/handler/resource_handler.go",
    "services/control-plane/internal/handler/admin_handler.go",
    "services/control-plane/internal/server/routes/auth.go",
    "services/control-plane/internal/server/routes/resources.go"
  ];
  for (const file of requiredFiles) {
    await assertFile(file);
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
