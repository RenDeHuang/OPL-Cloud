import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);

async function source(path) {
  return readFile(new URL(path, root), "utf8");
}

test("commercial pages keep the maintained Ant Design surface", async () => {
  for (const page of [
    "apps/console-ui/src/pages/billing/BillingPage.tsx",
    "apps/console-ui/src/pages/gateway/GatewayPage.tsx",
    "apps/console-ui/src/pages/workspaces/CreateWorkspacePage.tsx",
    "apps/console-ui/src/pages/workspaces/WorkspaceDetailPage.tsx"
  ]) {
    const pageSource = await source(page);
    assert.match(pageSource, /shared\/commercial-console\.tsx/);
    assert.doesNotMatch(pageSource, /StatisticCard|StepsForm/);
  }
});

test("Gateway loads on page entry and reveals or copies only on explicit action", async () => {
  const [gatewaySource, apiSource] = await Promise.all([
    source("apps/console-ui/src/pages/gateway/GatewayPage.tsx"),
    source("apps/console-ui/src/api/console-read-api.ts")
  ]);

  assert.match(apiSource, /getGatewaySummary/);
  assert.match(apiSource, /\/api\/gateway\/summary/);
  assert.match(apiSource, /reveal=true/);
  assert.match(gatewaySource, /React\.useEffect/);
  assert.match(gatewaySource, /maskedValue/);
  assert.match(gatewaySource, /navigator\.clipboard\.writeText/);
  assert.doesNotMatch(gatewaySource, /localStorage|sessionStorage/);
  assert.doesNotMatch(gatewaySource, /职责边界|Control Plane|Fabric|Ledger/);
});

test("launch guide and sensitive values remain usable on narrow screens", async () => {
  const styles = await source("apps/console-ui/src/styles.css");

  assert.match(styles, /\.launchGuide/);
  assert.match(styles, /\.gatewaySecretValue/);
  assert.match(styles, /@media \(max-width: 860px\)[\s\S]*\.launchGuide/);
  assert.match(styles, /overflow-wrap:\s*anywhere/);
});
