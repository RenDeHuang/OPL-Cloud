import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

async function runConsoleBrowserQa(options) {
  const harness = await import("../../tools/console-browser-qa.ts");
  return harness.runConsoleBrowserQa(options);
}

test("Console browser covers customer and operator truth states at desktop and mobile", { timeout: 120_000 }, async () => {
  const result = await runConsoleBrowserQa({ network: "fake-only" });

  assert.equal(result.ok, true);
  assert.equal(result.evidenceLevel, "code-complete");
  assert.equal(result.network, "fake-only");
  assert.deepEqual(result.viewports, ["desktop", "mobile"]);
  assert.deepEqual(result.roles, ["customer", "operator"]);
  assert.deepEqual(result.sourceStates, ["available", "empty", "unavailable", "error"]);
  assert.deepEqual(result.repeatedWrites, { gatewayKey: 1, walletAdjustment: 1 });
  assert.equal(result.workspaceSelection, true);
  assert.deepEqual(result.workspaceSecretReads, { "ws-1": 1, "ws-2": 1 });
  assert.equal(result.secretCleanup, true);
  assert.equal(result.externalRequests, 0);
});

test("Home Login Logo unchanged browser contract stays pinned", async () => {
  const app = await readFile("apps/console-ui/src/App.vue", "utf8");
  assert.match(app, /<h1>OPL Cloud<\/h1>/);
  assert.match(app, /邀请制 Workspace 与 API 服务。/);
  assert.match(app, /<span>Console 登录<\/span>/);
  assert.match(app, /src="\/opl-app-icon\.png" alt="OPL Cloud"/);
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
