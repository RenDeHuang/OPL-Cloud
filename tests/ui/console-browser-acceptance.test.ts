import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

async function runConsoleBrowserQa(options) {
  const harness = await import("../../tools/console-browser-qa.ts");
  return harness.runConsoleBrowserQa(options);
}

test("Console browser covers customer and operator journeys at desktop and mobile", { timeout: 120_000 }, async (t) => {
  const configuredScreenshotDir = process.env.OPL_CONSOLE_QA_SCREENSHOT_DIR;
  const screenshotDir = configuredScreenshotDir || await mkdtemp(join(tmpdir(), "opl-console-fixture-"));
  if (!configuredScreenshotDir) t.after(() => rm(screenshotDir, { recursive: true, force: true }));
  const result = await runConsoleBrowserQa({ network: "fake-only", screenshotDir });

  assert.equal(result.ok, true);
  assert.equal(result.network, "fake-only");
  assert.deepEqual(result.viewports, ["desktop", "mobile"]);
  assert.deepEqual(result.roles, ["customer", "operator"]);
  assert.deepEqual(result.sourceStates, ["available", "empty", "unavailable", "error"]);
  assert.deepEqual(result.repeatedWrites, { gatewayKey: 1, walletAdjustment: 1 });
  assert.deepEqual(result.highRiskWrites, {
    workspaceLaunch: 1,
    operatorProvision: 1,
    announcementCreate: 1,
    announcementPublish: 1,
    announcementWithdraw: 1,
    supportMapping: 1
  });
  assert.equal(result.workspaceLaunchAuthoritativeReadback, true);
  assert.equal(result.operatorProvisionAuthoritativeReadback, true);
  assert.equal(result.announcementLifecycle, true);
  assert.equal(result.supportMappingReadback, true);
  assert.equal(result.workspaceNavigation, true);
  assert.equal(result.workspacePagination, true);
  assert.equal(result.directDetailRefresh, true);
  assert.equal(result.billingViews, true);
  assert.equal(result.secretCleanup, true);
  assert.equal(result.externalRequests, 0);
  assert.deepEqual(result.consoleErrors, []);

  const screens = [
    "console-overview",
    "workspace-list",
    "api-overview",
    "api-usage",
    "billing",
    "announcements",
    "admin-overview",
    "admin-accounts",
    "admin-balance-operation",
    "admin-reconciliation",
    "admin-resources",
    "admin-system"
  ];
  await Promise.all(screens.flatMap((screen) => ["desktop", "mobile"].map((viewport) => (
    stat(join(screenshotDir, `fixture-${screen}-${viewport}.png`))
  ))));
});

test("Console browser refuses non-fixture network access before startup", async () => {
  let started = 0;
  await assert.rejects(() => runConsoleBrowserQa({
    network: "production",
    serverFactory: async () => { started += 1; },
    browserFactory: async () => { started += 1; }
  }), /console_browser_fake_only_required/);
  assert.equal(started, 0);
});

test("Console browser CLI refuses non-fixture network access", async () => {
  const child = spawn(process.execPath, ["tools/console-browser-qa.ts", "--network=production"], {
    cwd: process.cwd(),
    stdio: ["ignore", "pipe", "pipe"]
  });
  let stdout = "";
  let stderr = "";
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");
  child.stdout.on("data", (chunk) => { stdout += chunk; });
  child.stderr.on("data", (chunk) => { stderr += chunk; });
  const [exitCode] = await once(child, "close");

  assert.equal(exitCode, 1);
  assert.equal(stdout, "");
  assert.match(stderr, /console_browser_fake_only_required/);
});
