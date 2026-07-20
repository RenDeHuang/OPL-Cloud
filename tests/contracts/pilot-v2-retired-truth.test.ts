import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);
const source = (path: string) => readFile(new URL(path, root), "utf8");

test("current Console adapters do not consume retired V2 routes or fallbacks", async () => {
  const [readApi, workspaceApi, app] = await Promise.all([
    source("apps/console-ui/src/api/console-read-api.ts"),
    source("apps/console-ui/src/api/workspaces-api.ts"),
    source("apps/console-ui/src/App.vue")
  ]);
  assert.doesNotMatch(readApi, /getGatewayUsage|getGatewayUsageStats|getOperatorSummary|createUser/);
  assert.doesNotMatch(readApi, /api\/gateway\/keys\/opl-workspace\/reveal/);
  assert.doesNotMatch(readApi, /OPL_SUB2API_BASE_URL|gflabtoken\.cn/);
  assert.match(readApi, /getGatewayKeyUsage/);
  assert.doesNotMatch(workspaceApi, /postJson<unknown>\("\/api\/workspaces\/runtime-status"/);
  assert.match(workspaceApi, /getWorkspaceRuntimeStatus[\s\S]*getJson<unknown>[\s\S]*runtime-status/);
  assert.doesNotMatch(app, /getGatewayUsage\(|getGatewayUsageStats\(|getOperatorSummary\(|createUser\(/);
});

test("current source has no legacy route strings outside explicit retired tests", async () => {
  const files = [
    "apps/console-ui/src/api/console-read-api.ts",
    "apps/console-ui/src/api/workspaces-api.ts",
    "apps/console-ui/src/App.vue",
    "services/control-plane/internal/server/routes_gateway.go",
    "services/control-plane/internal/server/routes_workspace.go",
    "services/control-plane/internal/server/routes_state.go",
    "services/control-plane/internal/server/routes_admin.go"
  ];
  const contents = await Promise.all(files.map(source));
  const joined = contents.join("\n");
  for (const pattern of [
    /GET \/api\/gateway\/usage"/,
    /GET \/api\/gateway\/usage\/stats"/,
    /opl-workspace\/reveal/,
    /POST \/api\/workspaces\/runtime-status"/,
    /gateway-secret\/rotate/,
    /GET \/api\/operator\/summary"/,
    /POST \/api\/users(?:"|\/)/
  ]) assert.doesNotMatch(joined, pattern);
});

test("current Workspace launch has no legacy child-billing executor or DTO fields", async () => {
  const launch = await source("services/control-plane/internal/server/workspace_launch.go");
  assert.doesNotMatch(launch, /runLegacyWorkspaceLaunch|ComputeBillingOperationID|StorageBillingOperationID|TotalMonthlyPriceCNYCents|\bPricingVersion\b/);
});
