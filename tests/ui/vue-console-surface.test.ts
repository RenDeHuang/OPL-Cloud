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
    "/api/gateway/wallet", "/api/gateway/keys", "/api/gateway/groups", "/api/gateway/endpoint",
    "/api/gateway/keys/${encodeURIComponent(keyId)}/usage?",
    "/api/gateway/keys/${encodeURIComponent(keyId)}/usage-summary?",
    "/api/gateway/usage-summary?", "/api/gateway/balance-history",
    "/api/billing/receipts?", "/api/announcements"
  ]) assert.ok(readApi.includes(route), `${route} adapter is required`);
  assert.match(readApi, /GatewayEndpointDTO/);
  assert.match(readApi, /GatewayGroupPageDTO/);
  assert.match(workspaceApi, /\/api\/workspace-launches/);
  assert.match(workspaceApi, /\/api\/workspaces\/\$\{encodeURIComponent\(workspaceId\)\}\/runtime-status/);
  assert.doesNotMatch(readApi, /\/api\/gateway\/summary|reveal=true/);
  assert.doesNotMatch(app, /\bgetGatewayUsage\(|\bgetGatewayUsageStats\(/);
  assert.doesNotMatch(app, /createComputeAllocation|createStorageVolume|attachStorage|buyCompute|buyStorage|mountStorage/);
  assert.doesNotMatch(template, /Sub2API|Gateway|Fabric|Ledger|CVM|CBS|ComputeAllocation|StorageVolume|StorageAttachment|Mount/);
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

test("administrator keeps customer routes and adds operator navigation", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  assert.doesNotMatch(app, /isOperator\.value && !isAdminRoute\.value/);
  assert.match(app, /defaultAuthenticatedRoute\(next\.isOperator\)/);
  assert.doesNotMatch(app, /v-if="!isOperator"[\s\S]+v-for="item in customerMenu"/);
  assert.match(app, /v-for="item in customerMenu"[\s\S]+v-if="isOperator"[\s\S]+v-for="item in adminMenu"/);
  assert.match(app, /filter\(\(plan\) => plan\.id === "basic" \|\| plan\.id === "pro"\)/);
  assert.match(app, /type="radio"[^>]+:disabled="!plan\.available"/);
  assert.match(app, /plans\.value\.filter\(\(plan\) => plan\.available\)/);
  assert.match(app, /客户与计费账户/);
  assert.match(app, /account\.role === ['"]admin['"][\s\S]*管理员/);
  assert.match(app, /account\.status === ['"]active['"] && account\.accountId !== ['"]acct-admin['"]/);
});

test("created Key readback is revealed, copied, and timed out in memory", async () => {
  const panel = await source("apps/console-ui/src/components/keys/KeysPanel.vue");
  const created = panel.slice(panel.indexOf("async function submitKey"), panel.indexOf("async function mutateKey"));
  assert.match(created, /createGatewayKey/);
  assert.match(created, /getGatewayKey\(created\.data\.id\)/);
  assert.match(created, /await revealGatewayKey\(created\.data\.id, props\.csrfToken\)/);
  assert.match(created, /revealed\.value = secret\.data/);
  assert.match(created, /armSecretTimer\(\)/);
  assert.match(panel, /secretTimer = window\.setTimeout\(clearSecret, 60_000\)/);
  assert.match(panel, /revealed\.value[\s\S]+copyText\(revealed\.value, ['"]Key 已复制['"]\)/);
});

test("Key usage instructions use the revealed value, endpoint, and authoritative group platform", async () => {
  const panel = await source("apps/console-ui/src/components/keys/KeysPanel.vue");
  assert.match(panel, /useConfiguration/);
  assert.match(panel, /groupPlatform/);
  assert.match(panel, /endpoint\.value/);
  assert.match(panel, /revealed\.value\.value/);
  assert.match(panel, /复制配置/);
  assert.doesNotMatch(panel, /Bearer &lt;API_KEY&gt;/);
});

test("App delegates ordinary Key creation to KeysPanel and keeps only the Workspace reveal path", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  for (const deadPath of [
    "createGatewayKey", "CreateGatewayKeyRequest", "keyForm", "gatewayKeyCreateIntent",
    "gatewayKeyToggleIntents", "gatewayKeyDeleteIntents", "sameGatewayKeyCreateRequest", "submitKey"
  ]) assert.doesNotMatch(app, new RegExp(`\\b${deadPath}\\b`), deadPath);
  assert.doesNotMatch(app, /["']api-key["']/);
  assert.match(app, /await revealGatewayKey\(workspaceKeyId\.value, session\.value\?\.csrfToken \|\| ["']["']\)/);
  assert.match(app, /function copyWorkspaceKey\(\)/);
});

test("Workspace empty, unavailable, and launch recovery states stay distinct", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  assert.match(app, /暂无 Workspace/);
  assert.match(app, /暂无 Runtime 数据/);
  assert.match(app, /status === "waiting" \|\| status === "retryable"/);
  assert.match(app, /Workspace 继续处理中/);
  assert.match(app, /launchOperation\.value\?\.status === "refunded"/);
  assert.match(app, /Workspace 正在人工复核/);
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
