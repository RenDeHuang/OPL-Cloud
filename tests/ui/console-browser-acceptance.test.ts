import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

async function runConsoleBrowserQa(options) {
  const harness = await import("../../tools/console-browser-qa.ts");
  return harness.runConsoleBrowserQa(options);
}

test("Console browser covers customer and operator truth states at desktop and mobile", { timeout: 120_000 }, async (t) => {
  const configuredScreenshotDir = process.env.OPL_CONSOLE_QA_SCREENSHOT_DIR;
  const screenshotDir = configuredScreenshotDir || await mkdtemp(join(tmpdir(), "opl-console-fixture-"));
  if (!configuredScreenshotDir) t.after(() => rm(screenshotDir, { recursive: true, force: true }));
  const result = await runConsoleBrowserQa({ network: "fake-only", screenshotDir });

  assert.equal(result.ok, true);
  assert.equal(result.evidenceLevel, "code-complete");
  assert.equal(result.network, "fake-only");
  assert.deepEqual(result.viewports, ["desktop", "mobile"]);
  assert.deepEqual(result.roles, ["customer", "operator"]);
  assert.deepEqual(result.sourceStates, ["available", "empty", "unavailable", "error"]);
  assert.deepEqual(result.repeatedWrites, { gatewayKey: 1, walletAdjustment: 1 });
  assert.equal(result.operatorAccountDisableWrites, 1);
  assert.deepEqual(result.operatorAccountStatuses, { "acct-1": "disabled", "acct-2": "disabled" });
  assert.deepEqual(result.operatorAccountViewports, ["desktop", "mobile"]);
  assert.deepEqual(result.unavailablePlanKeyboardViewports, ["mobile"]);
  assert.equal(result.workspaceNavigation, true);
  assert.equal(result.workspacePagination, true);
  assert.equal(result.directDetailRefresh, true);
  assert.deepEqual(result.requestRecordFields, ["time", "model", "endpoint", "actualAmount", "requestId"]);
  assert.equal(result.billingViews, true);
  assert.equal(result.loginSubmissions, 2);
  assert.deepEqual(result.customerRoutes, [
    "/login",
    "/console/overview",
    "/console/workspaces",
    "/console/workspaces/new",
    "/console/workspaces/ws-1",
    "/console/workspaces/ws-2",
    "/console/api",
    "/console/api/usage",
    "/console/api/keys",
    "/console/billing",
    "/console/announcements"
  ]);
  assert.deepEqual(result.workspaceSecretReads, { "ws-1": 1, "ws-2": 1 });
  assert.equal(result.secretCleanup, true);
  assert.equal(result.operatorRouteLazyReads, true);
  assert.equal(result.externalRequests, 0);
  assert.deepEqual(result.consoleErrors, []);
  await stat(join(screenshotDir, "fixture-console-overview-desktop.png"));
  await stat(join(screenshotDir, "fixture-console-overview-mobile.png"));
});

test("Home Login Logo unchanged browser contract stays pinned", async () => {
  const app = await readFile("apps/console-ui/src/App.vue", "utf8");
  assert.match(app, /<h1>OPL Cloud<\/h1>/);
  assert.match(app, /面向已开通用户的 Workspace 与 API 服务。/);
  assert.match(app, /<span>Console 登录<\/span>/);
  assert.match(app, /src="\/opl-app-icon\.png" alt="OPL Cloud"/);
});

test("operator provisioning delegates every non-empty password to Sub2API", async () => {
  const app = await readFile("apps/console-ui/src/App.vue", "utf8");
  assert.match(app, /v-model="adminUserForm\.password" type="password" required/);
  assert.doesNotMatch(app, /minlength="12"/);
});

test("Console browser rejects non-fake network before starting a server or browser", async () => {
  let started = 0;
  await assert.rejects(() => runConsoleBrowserQa({
    network: "production",
    serverFactory: async () => { started += 1; },
    browserFactory: async () => { started += 1; }
  }), /console_browser_fake_only_required/);
  assert.equal(started, 0);
});

test("Console browser final gate machine-checks Node and Go SKIP counts", async () => {
  const workflow = await readFile(".github/workflows/pull-request-ci.yml", "utf8");
  assert.match(workflow, /OPL_CAPACITY_TESTS:\s*["']1["']/);
  assert.match(workflow, /--test-reporter=tap/);
  assert.match(workflow, /Node SKIP result missing or nonzero/);
  assert.match(workflow, /go list -f ['"]\{\{if or \.TestGoFiles \.XTestGoFiles\}\}\{\{\.ImportPath\}\}\{\{end\}\}['"] \.\/\.\.\./);
  assert.doesNotMatch(workflow, /go test(?: -race)? \.\/\.\.\. -json/);
  assert.match(workflow, /go test[^\n]*-json/);
  assert.match(workflow, /Action === ["']skip["']/);
  assert.match(workflow, /Go SKIP/);
  assert.match(workflow, /console-browser-qa\.ts --network=fake-only/);
});
