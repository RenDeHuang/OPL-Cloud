import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);
const source = (path: string) => readFile(new URL(path, root), "utf8");

test("Console runtime is Vue without React or Ant Design", async () => {
  const [packageSource, viteSource, entrySource] = await Promise.all([
    source("package.json"),
    source("vite.config.ts"),
    source("apps/console-ui/src/main.ts")
  ]);
  const packageJson = JSON.parse(packageSource);

  assert.ok(packageJson.dependencies.vue);
  assert.ok(packageJson.dependencies["@lucide/vue"]);
  for (const dependency of ["react", "react-dom", "lucide-react", "antd", "@ant-design/pro-components", "@vitejs/plugin-react"]) {
    assert.equal(packageJson.dependencies[dependency], undefined, `${dependency} must be removed`);
  }
  assert.match(viteSource, /@vitejs\/plugin-vue/);
  assert.match(entrySource, /createApp\(App\)/);
});

test("customer views read only from the approved projections", async () => {
  const [appSource, readApiSource] = await Promise.all([
    source("apps/console-ui/src/App.vue"),
    source("apps/console-ui/src/api/console-read-api.ts")
  ]);

  assert.match(readApiSource, /\/api\/state/);
  assert.match(readApiSource, /\/api\/gateway\/summary/);
  assert.match(readApiSource, /\/api\/gateway\/usage\?/);
  assert.match(readApiSource, /\/api\/gateway\/usage\/stats\?/);
  assert.match(readApiSource, /\/api\/billing\/receipts/);
  assert.match(readApiSource, /\/api\/runtime\/readiness/);
  assert.match(readApiSource, /\/api\/production\/readiness/);
  for (const client of ["getConsoleState", "getGatewaySummary", "getGatewayUsage", "getGatewayUsageStats", "getBillingReceipts", "getPricingCatalog"]) {
    assert.match(appSource, new RegExp(client));
  }
  assert.doesNotMatch(appSource, /Sub2API 余额|gflabtoken\.cn|钱包扣款|月度权益|PREPAID|账号映射|最终结果以后端确认为准|登录身份、权限范围和余额/);
  assert.doesNotMatch(appSource, /逐请求|Token 明细|请求金额/);
  assert.doesNotMatch(appSource, /gatewayKey\.name \|\| "opl-workspace"/);
  assert.doesNotMatch(appSource, /gatewayHealthy[^}\n]*gatewayKey\.status/);
  for (const label of ["Usage", "API Keys", "输入 Token", "输出 Token", "实际金额", "请求 ID", "运维管理", "待处理事项", "系统状态"]) {
    assert.match(appSource, new RegExp(label));
  }
  assert.match(appSource, /previous === "\/console\/gateway\/keys" && next !== previous/);
  assert.match(appSource, /errors\.gateway && activeGatewayPage !== 'usage'/);
  assert.doesNotMatch(appSource, /折线图|趋势图|重新生成 Key|Prompt|Response 内容/);
});

test("resource purchase forms require customer names and use existing mutation clients", async () => {
  const appSource = await source("apps/console-ui/src/App.vue");

  for (const field of ["workspaceName", "computeName", "storageName"]) assert.match(appSource, new RegExp(field));
  for (const client of ["createComputeAllocation", "createStorageVolume", "attachStorage", "createWorkspace"]) {
    assert.match(appSource, new RegExp(client));
  }
});

test("admin creates a regular account owner rather than another Cloud administrator", async () => {
  const appSource = await source("apps/console-ui/src/App.vue");
  assert.match(appSource, /createUser\(\{[\s\S]*role:\s*"owner"/);
  assert.doesNotMatch(appSource, /role:\s*"pi"/);
});

test("wide resource tables cannot widen the mobile page", async () => {
  const styles = await source("apps/console-ui/src/styles.css");
  assert.match(styles, /\.panel,\s*\.spend-strip[^{]*\{[^}]*min-width:\s*0/);
  assert.match(styles, /\.table-wrap\s*\{[^}]*width:\s*100%/);
});

test("Gateway usage metrics use one grouped summary surface", async () => {
  const styles = await source("apps/console-ui/src/styles.css");
  assert.match(styles, /\.gateway-usage-metrics\s*\{[^}]*gap:\s*0[^}]*border:\s*1px solid/);
  assert.match(styles, /\.gateway-usage-metrics article\s*\{[^}]*border:\s*0[^}]*border-radius:\s*0/);
  assert.match(styles, /\.gateway-usage-toolbar\s*\{[^}]*justify-content:\s*flex-start/);
});

test("Gateway Key requests clear any previously revealed value before network access", async () => {
  const appSource = await source("apps/console-ui/src/App.vue");
  assert.match(appSource, /async function loadGateway\(\) \{\s*hideGatewayKey\(\);/);
  assert.match(appSource, /async function revealGatewayKey\(\) \{\s*hideGatewayKey\(\);/);
});

test("customer resource rows do not invent success or expose fallback internal ids", async () => {
  const appSource = await source("apps/console-ui/src/App.vue");
  assert.doesNotMatch(appSource, /class="dot good"/);
  assert.doesNotMatch(appSource, /item\.name\s*\|\|\s*item\.id/);
  assert.doesNotMatch(appSource, /latestOrder\.name\s*\|\|\s*latestOrder\.id/);
  assert.doesNotMatch(appSource, /\?\s*"已挂载"\s*:\s*item\.status/);
});
