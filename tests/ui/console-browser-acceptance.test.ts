import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, readFile, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { parse } from "yaml";

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
  assert.equal(result.operatorAccountDisableWrites, 1);
  assert.deepEqual(result.operatorAccountStatuses, { "acct-1": "disabled", "acct-2": "disabled" });
  assert.deepEqual(result.operatorAccountViewports, ["desktop", "mobile"]);
  assert.deepEqual(result.unavailablePlanKeyboardViewports, ["mobile"]);
  assert.equal(result.workspaceNavigation, true);
  assert.equal(result.workspacePagination, true);
  assert.equal(result.directDetailRefresh, true);
  assert.deepEqual(result.requestRecordFields, ["modelEndpoint", "tokens", "actualCost", "latency", "time", "requestId"]);
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
  const primaryScreens = [
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
  await Promise.all(primaryScreens.flatMap((screen) => ["desktop", "mobile"].map((viewport) => (
    stat(join(screenshotDir, `fixture-${screen}-${viewport}.png`))
  ))));
});

test("public entry states the product identity and current access boundary", async () => {
  const pages = await readFile("apps/console-ui/src/pages/PublicPages.tsx", "utf8");
  assert.match(pages, /OPL Cloud/);
  assert.match(pages, /让你的 One Person Lab 在云端继续工作/);
  assert.match(pages, /登录 OPL Cloud/);
  assert.match(pages, /账户由管理员开通/);
  assert.match(pages, /alt="OPL Cloud" src="\/opl-app-icon\.png"/);
  assert.doesNotMatch(pages, /权威控制面|浏览器端业务推导|余额守卫|已冻结的 Console 展示合同/);
});

test("operator provisioning delegates every non-empty password to Sub2API", async () => {
  const pages = await readFile("apps/console-ui/src/pages/AdminPages.tsx", "utf8");
  assert.match(pages, /label="初始密码"[\s\S]+required type="password"/);
  assert.doesNotMatch(pages, /minLength=\{?12\}?/);
});

test("Console browser confirms the target Account ID before a wallet adjustment", async () => {
  const browserQa = await readFile("tools/console-browser-qa.ts", "utf8");
  assert.match(browserQa, /getByLabel\("再次确认 Account ID"\)\.pressSequentially\("acct-1"\)/);
  assert.match(browserQa, /getByLabel\("金额（USD）"\)\.pressSequentially\("5"\)/);
  assert.match(browserQa, /getByLabel\("业务原因"\)\.pressSequentially\("browser retry"\)/);
});

test("Console browser uses the visible account surface for each viewport", async () => {
  const browserQa = await readFile("tools/console-browser-qa.ts", "utf8");
  assert.match(browserQa, /operator-account-mobile-card/);
  assert.match(browserQa, /operator-account-table tbody tr:visible/);
  assert.match(browserQa, /name === "desktop"/);
  assert.match(browserQa, /querySelectorAll\("\.table-wrap"\)[\s\S]+scrollLeft = 0/);
});

test("Console browser text waits ignore hidden duplicate navigation labels", async () => {
  const browserQa = await readFile("tools/console-browser-qa.ts", "utf8");
  assert.match(browserQa, /getByText\(text, \{ exact: false \}\)\.filter\(\{ visible: true \}\)\.first\(\)/);
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

test("Console browser CLI rejects a non-fake network before running Browser QA", async () => {
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

test("browser qualification runs fake-only Browser QA once through the Node acceptance test", async () => {
  const [workflow, acceptance] = await Promise.all([
    readFile(".github/workflows/qualification.yml", "utf8"),
    readFile("tests/ui/console-browser-acceptance.test.ts", "utf8")
  ]);
  const pullRequestCI = parse(workflow);
  const nodeSteps = pullRequestCI.jobs.node_console.steps;
  const nodeTestSteps = nodeSteps.filter((step) => step.name === "Test Node");
  const browserCliSteps = nodeSteps.filter((step) => /console-browser-qa\.ts/.test(step.run || ""));

  assert.equal(nodeTestSteps.length, 1);
  assert.match(nodeTestSteps[0].run, /node --test --test-reporter=tap "tests\/\*\*\/\*\.test\.ts"/);
  assert.equal(browserCliSteps.length, 0);
  assert.equal(nodeSteps.filter((step) => step.name === "Typecheck").length, 0);
  assert.equal(nodeSteps.filter((step) => step.name === "Build").length, 1);
  assert.equal(nodeSteps.filter((step) => step.name === "Lint").length, 1);
  assert.match(acceptance, /runConsoleBrowserQa\(\{ network: "fake-only", screenshotDir \}\)/);
  assert.match(acceptance, /assert\.equal\(result\.network, "fake-only"\)/);
  assert.match(acceptance, /console_browser_fake_only_required/);
});

test("Cloud qualification final gate machine-checks Node and Go SKIP counts", async () => {
  const workflow = await readFile(".github/workflows/qualification.yml", "utf8");
  const parsed = parse(workflow);
  const jobs = parsed.jobs;
  assert.match(workflow, /OPL_CAPACITY_TESTS:\s*["']1["']/);
  assert.match(workflow, /--test-reporter=tap/);
  assert.match(workflow, /Node SKIP result missing or nonzero/);
  assert.match(workflow, /go list -f ['"]\{\{if or \.TestGoFiles \.XTestGoFiles\}\}\{\{\.ImportPath\}\}\{\{end\}\}['"] \.\/\.\.\./);
  assert.doesNotMatch(workflow, /go test(?: -race)? \.\/\.\.\. -json/);
  assert.match(workflow, /go test[^\n]*-json/);
  assert.match(workflow, /Action === ["']skip["']/);
  assert.match(workflow, /Go SKIP/);
  assert.doesNotMatch(workflow, /console-browser-qa\.ts --network=fake-only/);
  assert.deepEqual(Object.keys(jobs).sort(), ["control_plane", "fabric", "node_console", "postgres_ledger", "validate"]);
  assert.deepEqual(jobs.validate.needs, ["node_console", "postgres_ledger", "control_plane", "fabric"]);
  assert.equal(jobs.validate.if, "${{ always() }}");

  const nodeSetups = [jobs.node_console, jobs.postgres_ledger, jobs.control_plane, jobs.fabric]
    .map((job) => job.steps.find((step) => step.name === "Set up Node 24"));
  assert.deepEqual(nodeSetups.map((step) => step.with["node-version"]), ["24", "24", "24", "24"]);
  const goSetups = [jobs.postgres_ledger, jobs.control_plane, jobs.fabric]
    .map((job) => job.steps.find((step) => step.name === "Set up Go"));
  assert.deepEqual(goSetups.map((step) => step.with["go-version"]), ["1.22.x", "1.22.x", "1.25.x"]);

  const goTestSteps = [
    jobs.postgres_ledger.steps.find((step) => step.name === "Test PostgreSQL migrations"),
    jobs.postgres_ledger.steps.find((step) => step.name === "Test Ledger"),
    jobs.control_plane.steps.find((step) => step.name === "Test Control Plane"),
    jobs.fabric.steps.find((step) => step.name === "Test Fabric")
  ];
  assert.deepEqual(goTestSteps.map((step) => ({
    workingDirectory: step["working-directory"],
    args: step.env.GO_TEST_ARGS
  })), [
    { workingDirectory: "services/internal/postgresmigrate", args: "-race -count=1" },
    { workingDirectory: "services/ledger", args: "-count=1" },
    { workingDirectory: "services/control-plane", args: "-timeout=15m -count=1" },
    { workingDirectory: "services/fabric", args: "-count=1" }
  ]);
  for (const step of goTestSteps) {
    assert.match(step.run, /read -r -a go_test_args <<< "\$GO_TEST_ARGS"/);
    assert.match(step.run, /go test "\$\{go_test_args\[@\]\}" -json "\$\{test_packages\[@\]\}"/);
    assert.match(step.run, /Action === "skip"/);
  }
  assert.deepEqual(
    [jobs.postgres_ledger, jobs.control_plane, jobs.fabric].map((job) => job.services),
    [jobs.postgres_ledger.services, jobs.postgres_ledger.services, jobs.postgres_ledger.services]
  );
  assert.equal(jobs.postgres_ledger.services.postgres.image, "postgres:16");
  assert.equal(jobs.postgres_ledger.services.postgres.env.POSTGRES_HOST_AUTH_METHOD, "trust");
});
