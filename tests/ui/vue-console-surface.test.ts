import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);
const source = (path: string) => readFile(new URL(path, root), "utf8");

test("Console runtime is Vue without React or Ant Design", async () => {
  const [packageSource, viteSource, entrySource] = await Promise.all([
    source("package.json"), source("vite.config.ts"), source("apps/console-ui/src/main.ts")
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

test("customer views use granular V2 source projections and the one Workspace launch", async () => {
  const [app, readApi, workspaceApi] = await Promise.all([
    source("apps/console-ui/src/App.vue"),
    source("apps/console-ui/src/api/console-read-api.ts"),
    source("apps/console-ui/src/api/workspaces-api.ts")
  ]);
  const template = app.slice(app.indexOf("<template>"));
  for (const route of [
    "/api/gateway/endpoint", "/api/gateway/wallet", "/api/gateway/keys",
    "/api/gateway/keys/${encodeURIComponent(keyId)}/usage?",
    "/api/gateway/keys/${encodeURIComponent(keyId)}/usage-summary?",
    "/api/gateway/usage-summary?", "/api/gateway/balance-history",
    "/api/billing/receipts?", "/api/announcements"
  ]) assert.ok(readApi.includes(route), `${route} adapter is required`);
  assert.match(workspaceApi, /\/api\/workspace-launches/);
  assert.match(workspaceApi, /\/api\/workspaces\/\$\{encodeURIComponent\(workspaceId\)\}\/runtime-status/);
  assert.doesNotMatch(readApi, /\/api\/gateway\/summary|reveal=true/);
  assert.doesNotMatch(app, /\bgetGatewayUsage\(|\bgetGatewayUsageStats\(/);
  assert.doesNotMatch(app, /createComputeAllocation|createStorageVolume|attachStorage|buyCompute|buyStorage|mountStorage/);
  assert.doesNotMatch(template, /Sub2API|Gateway|Fabric|Ledger|Runtime|CVM|CBS|ComputeAllocation|StorageVolume|StorageAttachment|Mount/);
  for (const label of ["概览", "Workspace", "API 服务", "账单", "公告", "模型", "Token", "请求编号", "暂不可用"]) {
    assert.match(template, new RegExp(label));
  }
});

test("customer financial facts are direct server fields", async () => {
  const [app, model] = await Promise.all([
    source("apps/console-ui/src/App.vue"), source("apps/console-ui/src/console-model.ts")
  ]);
  assert.match(app, /workspace\.totalUsdMicros/);
  assert.match(app, /stats\.totalActualCostUsdMicros/);
  assert.doesNotMatch(app, /state\.value\?\.balance|fixedMonthlySpend|workspaceMonthlyPrice|renewalSummary/);
  assert.doesNotMatch(model, /fixedMonthlySpend|workspaceMonthlyPrice|renewalSummary|storageMonthlyPrice/);
  assert.doesNotMatch(app, /receipt\.status\s*\|\|\s*["']/);
});

test("administrator provisioning derives the billing account and omits remote identity input", async () => {
  const [app, readApi] = await Promise.all([
    source("apps/console-ui/src/App.vue"), source("apps/console-ui/src/api/console-read-api.ts")
  ]);
  const template = app.slice(app.indexOf("<template>"));
  assert.match(readApi, /postJson<unknown>\("\/api\/operator\/accounts"/);
  assert.doesNotMatch(readApi, /\/api\/operator\/accounts\/invitations/);
  assert.match(app, /provisionOperatorUser\(\)/);
  assert.match(app, /ProvisionAccountRequest/);
  assert.doesNotMatch(app, /adminUserForm\.sub2apiUserId|sub2apiUserId:\s*Number/);
  assert.doesNotMatch(template, /adminUserForm\.accountId/);
});

test("revealed secrets are cleared on navigation, refresh, and logout", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  assert.match(app, /function clearSecrets\(\)/);
  assert.match(app, /function isSensitiveRoute\(route: string\)/);
  assert.match(app, /isSensitiveRoute\(previous \|\| ""\)/);
  assert.match(app, /async function signOut\(\)[\s\S]*clearSecrets\(\)/);
  assert.match(app, /function refreshCurrentPage\(\) \{\s*clearSecrets\(\);/);
});

test("responsive tables and secret controls stay inside the mobile page", async () => {
  const styles = await source("apps/console-ui/src/styles.css");
  assert.match(styles, /\.panel,\s*\.spend-strip[^{]*\{[^}]*min-width:\s*0/);
  assert.match(styles, /\.table-wrap\s*\{[^}]*width:\s*100%/);
  assert.match(styles, /@media \(max-width: 820px\)[\s\S]*\.key-row\s*\{[^}]*grid-template-columns:\s*1fr/);
  assert.match(styles, /\.credential-actions\s*\{[^}]*flex-wrap:\s*wrap/);
  assert.match(styles, /\.workspace-details \.data-list a\s*\{[^}]*overflow-wrap:\s*anywhere/);
});
