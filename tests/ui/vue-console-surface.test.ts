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
  assert.doesNotMatch(template, /Sub2API|Gateway|Fabric|CVM|CBS|ComputeAllocation|StorageVolume|StorageAttachment|Mount/);
  for (const label of ["概览", "Workspace", "API 服务", "账单", "公告", "模型", "实际金额", "请求编号", "暂不可用"]) {
    assert.match(template, new RegExp(label));
  }
});

test("Overview reads its independent customer summary sources without Runtime detail", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const overviewStart = app.indexOf("<section v-if=\"isOverviewRoute\" class=\"overview-page\">");
  const overviewEnd = app.indexOf("workspaceRoute === 'list'", overviewStart);
  const overview = app.slice(overviewStart, overviewEnd);

  assert.match(overview, /class=\"metric-row overview-metrics\"/);
  assert.match(overview, /Workspace 总数/);
  assert.match(overview, /可用余额/);
  assert.match(overview, /本月 API 费用/);
  assert.doesNotMatch(overview, /Ledger/);
  assert.match(overview, /最近账单/);
  assert.match(overview, /class=\"panel overview-receipts\"/);
  assert.match(overview, /class=\"panel overview-announcements\"/);
  assert.doesNotMatch(overview, /runtime|workspaceCanOpen|selectedWorkspaceId|Workspace 月费|Token 构成|固定月支出|交易记录数量/);
});

test("Overview empty states keep the account band and one clear next action", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const overviewStart = app.indexOf('<section v-if="isOverviewRoute" class="overview-page">');
  const overviewEnd = app.indexOf("workspaceRoute === 'list'", overviewStart);
  const overview = app.slice(overviewStart, overviewEnd);

  assert.match(overview, /class="account-band overview-band"/);
  assert.match(overview, /class="overview-empty-state"/);
  assert.match(overview, /overviewPrimaryAction\.action === 'create'/);
  assert.match(overview, /navigate\('\/console\/workspaces\/new'\)/);
  assert.match(overview, /receiptStatusLabel\(receipt\.status\)/);
  assert.doesNotMatch(overview, /\{\{\s*receipt\.status\s*\}\}/);
});

test("Customer status copy is localized instead of exposing service enum values", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const template = app.slice(app.indexOf("<template>"));

  assert.match(app, /function workspaceStateLabel\(state: string\)/);
  assert.match(app, /function receiptStatusLabel\(status: string\)/);
  assert.match(template, /workspaceStateLabel\(item\.state\)/);
  assert.match(template, /receiptStatusLabel\(receiptDetail\.status\)/);
  assert.doesNotMatch(template, /\{\{\s*item\.state\s*\}\}/);
  assert.doesNotMatch(template, /\{\{\s*receiptDetail\.status\s*\}\}/);
});

test("Admin overview source status is localized instead of exposing transport enums", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const overviewStart = app.indexOf("path === '/admin' || path === '/admin/overview'");
  const overviewEnd = app.indexOf("path.startsWith('/admin/accounts')", overviewStart);
  const overview = app.slice(overviewStart, overviewEnd);

  assert.match(app, /function sourceStatusLabel\(status\?: string\)/);
  assert.match(app, /available: "正常"/);
  assert.match(app, /empty: "暂无数据"/);
  assert.match(overview, /sourceStatusLabel\(operatorOverview\?\.accounts\?\.status/);
  assert.match(overview, /sourceStatusLabel\(operatorOverview\?\.resources\?\.status/);
  assert.match(overview, /sourceStatusLabel\(operatorOverview\?\.health\?\.status/);
  assert.doesNotMatch(overview, /operatorOverview\?\.(accounts|resources|health)\?\.status \|\|/);
});

test("Admin overview uses operational copy instead of a promotional slogan", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const overviewStart = app.indexOf("path === '/admin' || path === '/admin/overview'");
  const overviewEnd = app.indexOf("path.startsWith('/admin/accounts')", overviewStart);
  const overview = app.slice(overviewStart, overviewEnd);

  assert.match(overview, /<h2>运营概览<\/h2>/);
  assert.match(overview, /客户、计费与系统状态/);
  assert.doesNotMatch(overview, /一处掌握|保持在同一 Console/);
});

test("API Key filters collapse into a native mobile disclosure", async () => {
  const [keys, styles] = await Promise.all([
    source("apps/console-ui/src/components/keys/KeysPanel.vue"),
    source("apps/console-ui/src/styles.css")
  ]);

  assert.match(keys, /details class="key-filter-disclosure"/);
  assert.match(keys, /summary>筛选与显示/);
  assert.match(keys, /class="key-filters key-filter-fields"/);
  assert.match(styles, /\.key-filter-disclosure/);
  assert.match(styles, /@media \(max-width: 820px\)[\s\S]+\.key-filter-disclosure/);
});

test("Console controls provide pressed feedback and respect reduced motion", async () => {
  const styles = await source("apps/console-ui/src/styles.css");
  assert.match(styles, /\.ui-button:not\(:disabled\):active/);
  assert.match(styles, /\.workspace-list > a:active/);
  assert.match(styles, /@media \(prefers-reduced-motion: reduce\)[\s\S]+\.ui-button/);
});

test("Workspace detail keeps Runtime facts legible and Billing stays terms-and-receipts only", async () => {
  const [app, styles, uiStyles] = await Promise.all([
    source("apps/console-ui/src/App.vue"),
    source("apps/console-ui/src/styles.css"),
    source("apps/console-ui/src/components/ui/components.css")
  ]);
  const template = app.slice(app.indexOf("<template>"));
  const detailStart = template.indexOf("<section v-else-if=\"workspaceRoute === 'detail'\"");
  const detailEnd = template.indexOf("<section v-else-if=\"apiRoute\"", detailStart);
  const detail = template.slice(detailStart, detailEnd);
  const billingStart = app.indexOf("<section v-else-if=\"path === '/console/billing'\" class=\"billing-page\">");
  const billingEnd = app.indexOf("<nav v-if=\"!isAdminRoute\" class=\"mobile-bottom-nav\"", billingStart);
  const billing = app.slice(billingStart, billingEnd);

  assert.match(detail, /Workspace URL/);
  assert.match(detail, /mountCheck\.ok/);
  assert.match(detail, /runtime\.ready/);
  assert.match(detail, /runtime\?\.access\?\.username/);
  assert.match(uiStyles, /\.ui-button\s*\{[^}]*min-height:\s*var\(--control-size-md\)/);
  assert.match(styles, /\.workspace-access-panel \.data-list a\s*\{[^}]*overflow-wrap:\s*anywhere/);
  assert.match(billing, /Workspace 条款/);
  assert.match(billing, /账单收据/);
  assert.doesNotMatch(billing, /Ledger/);
  assert.doesNotMatch(billing, /可用余额|当前 Workspace 月费|AI 用量|余额记录|loadWallet|loadAccountUsage|loadHistory|固定月支出/);
});

test("fake browser serves the strict requested balance-history page envelope", async () => {
  const browserQA = await source("tools/console-browser-qa.ts");
  assert.match(browserQA, /path === "\/api\/gateway\/balance-history"[\s\S]+url\.searchParams\.get\("page"\)[\s\S]+url\.searchParams\.get\("pageSize"\)/);
  assert.match(browserQA, /items: \[\], total: 0, page, pageSize, pages: 1/);
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

test("Workspace list and detail routes use bounded authoritative reads", async () => {
  const [app, model, workspaceApi, dto] = await Promise.all([
    source("apps/console-ui/src/App.vue"),
    source("apps/console-ui/src/console-model.ts"),
    source("apps/console-ui/src/api/workspaces-api.ts"),
    source("apps/console-ui/src/api/dtos.ts")
  ]);

  assert.match(model, /path:\s*"\/console\/workspaces"/);
  assert.match(model, /function workspacePage\(/);
  assert.match(model, /function workspaceIdFromPath\(/);
  assert.match(dto, /interface WorkspaceListData[\s\S]+page: number;[\s\S]+pageSize: number;/);
  assert.match(workspaceApi, /getWorkspaces\(page = 1, pageSize = 20/);
  assert.match(workspaceApi, /new URLSearchParams\(\{ page: String\(page\), pageSize: String\(pageSize\) \}\)/);
  assert.match(workspaceApi, /findWorkspaceInPages\([\s\S]+while \(true\)[\s\S]+getWorkspaces\(page, pageSize\)/);
  assert.match(workspaceApi, /inspected === total[\s\S]+status: "empty"[\s\S]+data: null/);
  assert.doesNotMatch(workspaceApi, /getJson<unknown>\(`\/api\/workspaces\/\$\{encodeURIComponent\(workspaceId\)\}`\)/);
  assert.doesNotMatch(workspaceApi, /getAllWorkspaces/);

  assert.match(app, /workspaceRoute === 'list'/);
  assert.match(app, /workspaceRoute === 'new'/);
  assert.match(app, /workspaceRoute === 'detail'/);
  assert.match(app, /findWorkspaceInPages\(workspaceId\)/);
  assert.match(app, /getWorkspaceRuntimeStatus\(workspaceId\)/);
  assert.match(app, /workspacePageNumber/);
  assert.match(app, /workspaceSource\.data\.total/);
  assert.doesNotMatch(app, /workspaceSource\.value\.data\.items\.find\([^\n]+selectedWorkspaceId/);
});

test("Overview and Billing keep their requested product boundaries", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const overviewStart = app.indexOf('class="overview-page"');
  const overviewEnd = app.indexOf("workspaceRoute === 'list'", overviewStart);
  const overview = app.slice(overviewStart, overviewEnd);
  const billingStart = app.indexOf('class="billing-page"');
  const billingEnd = app.indexOf('<div v-if="modal"', billingStart);
  const billing = app.slice(billingStart, billingEnd);

  for (const label of ["Workspace 总数", "可用余额", "本月 API 费用", "最近账单", "公告"]) {
    assert.match(overview, new RegExp(label));
  }
  assert.doesNotMatch(overview, /Workspace 月费|计费周期|常用入口|查看 API 服务|Token/);
  assert.match(billing, /Workspace 条款/);
  assert.match(billing, /账单收据/);
  assert.doesNotMatch(billing, /Ledger/);
  assert.doesNotMatch(billing, /余额记录|固定月支出|AI 用量|Token/);
});

test("request records expose only time, model, endpoint, actual amount, and request ID", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const usageStart = app.indexOf('activeApiPage === \'usage\'');
  const usageEnd = app.indexOf("<KeysPanel", usageStart);
  const usageView = app.slice(usageStart, usageEnd);
  assert.match(usageView, /<th>时间<\/th><th>模型<\/th><th>端点<\/th><th>实际金额<\/th><th>请求编号<\/th>/);
  assert.match(usageView, /formatDate\(item\.createdAt, true\)/);
  assert.match(usageView, /item\.model/);
  assert.match(usageView, /item\.inboundEndpoint/);
  assert.match(usageView, /formatUsdMicros\(item\.actualCostUsdMicros\)/);
  assert.match(usageView, /item\.requestId/);
  assert.doesNotMatch(usageView, /输入 Token|输出 Token|缓存 Token|request-detail|请求详情|查看详情/);
});

test("unknown Console routes render an explicit not-found state", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  assert.match(app, /const isNotFound = computed/);
  assert.match(app, /v-else-if="isNotFound"/);
  assert.match(app, /页面不存在/);
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
  assert.match(app, /const workspacePlanOptions = computed\([\s\S]+disabled: !plan\.available/);
  assert.match(app, /const selectedPlan = computed\([\s\S]+plan\.available/);
  assert.match(app, /客户与计费账户/);
  assert.match(app, /account\.role === ['"]admin['"][\s\S]*管理员/);
  assert.match(app, /account\.status === ['"]active['"] && account\.accountId !== ['"]acct-admin['"]/);
});

test("approved Lab Ledger visual contract stays on the real Vue Console", async () => {
  const [app, styles, keysPanel] = await Promise.all([
    source("apps/console-ui/src/App.vue"),
    source("apps/console-ui/src/styles.css"),
    source("apps/console-ui/src/components/keys/KeysPanel.vue")
  ]);
  const template = app.slice(app.indexOf("<template>"));
  const overviewStart = app.indexOf('<section v-if="isOverviewRoute" class="overview-page">');
  const overviewEnd = app.indexOf("workspaceRoute === 'list'", overviewStart);
  const overview = app.slice(overviewStart, overviewEnd);
  const launchStart = app.indexOf("workspaceRoute === 'new'");
  const launchEnd = app.indexOf("workspaceRoute === 'detail'", launchStart);
  const launch = app.slice(launchStart, launchEnd);
  const usageStart = app.indexOf("activeApiPage === 'usage'");
  const usageEnd = app.indexOf("<KeysPanel", usageStart);
  const usage = app.slice(usageStart, usageEnd);

  assert.match(template, /class="operator-nav"[\s\S]+>Admin</);
  assert.match(template, /v-if="isOperator"[\s\S]+v-for="item in adminMenu"/);
  assert.match(template, /class="account-online-dot"/);
  assert.match(template, /客户管理员/);
  assert.match(app, /mobileNavigationItems/);
  assert.doesNotMatch(template, /切换到运维中心|Sub2API/);

  assert.match(overview, /class="account-band overview-band"/);
  assert.match(overview, /可用余额[\s\S]+本月 API 费用[\s\S]+Workspace 总数/);
  assert.doesNotMatch(overview, /Ledger/);
  assert.match(overview, /class="panel overview-workspaces"[\s\S]+Workspace 状态/);
  assert.match(overview, /<thead>[\s\S]+Workspace[\s\S]+套餐[\s\S]+状态[\s\S]+已付至/);
  assert.match(overview, /workspaceSource\?\.status === 'empty'[\s\S]+暂无 Workspace/);

  assert.match(app, /launchStep\.value = "confirm"/);
  assert.match(launch, /launchStep === 'configure'[\s\S]+class="workspace-launch-confirm"/);
  assert.match(launch, /Workspace 名称[\s\S]+套餐[\s\S]+存储[\s\S]+权威报价[\s\S]+续费设置/);
  assert.match(launch, /launchOperation\.phase/);
  assert.doesNotMatch(launch, /百分比|ETA|预计|phaseHistory/);

  assert.match(usage, /总 Token[\s\S]+keyStats\.totalTokens/);
  assert.match(keysPanel, /5h \/ 1d \/ 7d 消费限额/);
  assert.match(keysPanel, /groupOptionLabel\(group\)/);
  assert.doesNotMatch(keysPanel, /RPM|QPS|Sub2API/);

  assert.match(styles, /--navy:\s*#111c33/);
  assert.match(styles, /\.account-band\s*\{[\s\S]+background:\s*var\(--navy\)/);
  assert.match(styles, /\.operator-nav[\s\S]+\.active[\s\S]+var\(--green\)/);
});

test("mobile overview metrics stay integrated with the navy account band", async () => {
  const styles = await source("apps/console-ui/src/styles.css");
  const mobileStyles = styles.slice(styles.lastIndexOf("@media (max-width: 820px)"));
  assert.match(mobileStyles, /\.account-band \.overview-metrics\s*\{[^}]*background:\s*transparent[^}]*border:\s*0[^}]*overflow:\s*visible/);
});

test("mobile overview keeps Workspace identity, status, and detail action visible", async () => {
  const styles = await source("apps/console-ui/src/styles.css");
  const mobileStyles = styles.slice(styles.lastIndexOf("@media (max-width: 820px)"));
  assert.match(mobileStyles, /\.overview-workspace-table table\s*\{[^}]*min-width:\s*0[^}]*table-layout:\s*fixed/);
  assert.match(mobileStyles, /\.overview-workspace-table th:nth-child\(2\),[\s\S]+\.overview-workspace-table td:nth-child\(4\)\s*\{[^}]*display:\s*none/);
});

test("populated overview panels size to authoritative rows instead of fixed filler space", async () => {
  const styles = await source("apps/console-ui/src/styles.css");
  assert.doesNotMatch(styles, /\.overview-workspaces,\s*\.overview-receipts\s*\{[^}]*min-height:\s*286px/);
});

test("fixture screenshots capture the requested viewport rather than a full-page composite", async () => {
  const browserQa = await source("tools/console-browser-qa.ts");
  const captureStart = browserQa.indexOf("async function captureFixtureScreenshot");
  const captureEnd = browserQa.indexOf("function assertOperatorPageReads", captureStart);
  const capture = browserQa.slice(captureStart, captureEnd);
  assert.match(capture, /page\.screenshot\(\{ path: screenshotPath \}\)/);
  assert.doesNotMatch(capture, /fullPage/);
});

test("mobile Key inventory screenshot scrolls the authoritative card into view", async () => {
  const browserQa = await source("tools/console-browser-qa.ts");
  const keysStart = browserQa.indexOf('await page.goto(`${server.origin}/console/api/keys?viewport=${name}`');
  const keysEnd = browserQa.indexOf('await page.getByRole("button", { name: "创建 Key" })', keysStart);
  const keysCapture = browserQa.slice(keysStart, keysEnd);

  assert.match(keysCapture, /if \(name === "mobile"\)[\s\S]+page\.locator\("\.mobile-key-card"\)\.scrollIntoViewIfNeeded\(\)/);
  assert.match(keysCapture, /captureFixtureScreenshot\(page, state, screenshotDir, "api-keys", name\)/);
});

test("fixture browser QA uses the acceptance desktop and mobile viewport dimensions", async () => {
  const browserQa = await source("tools/console-browser-qa.ts");
  assert.match(browserQa, /desktop:\s*Object\.freeze\(\{ width:\s*1440, height:\s*1024 \}\)/);
  assert.match(browserQa, /mobile:\s*Object\.freeze\(\{ width:\s*390, height:\s*844 \}\)/);
});

test("Key inventory keeps row group selectors short while retaining authoritative metadata", async () => {
  const panel = await source("apps/console-ui/src/components/keys/KeysPanel.vue");
  const inventoryStart = panel.indexOf('<div class="keys-table-wrap">');
  const inventoryEnd = panel.indexOf('<div v-if="dialog" class="keys-modal-backdrop"', inventoryStart);
  const inventory = panel.slice(inventoryStart, inventoryEnd);
  assert.match(inventory, /class="key-group-cell"[\s\S]+\{\{ group\.name \}\}[\s\S]+class="key-group-meta"[\s\S]+groupMeta\(key\.groupId\)/);
  assert.match(inventory, /class="mobile-key-card__detail--wide mobile-key-card__group"[\s\S]+\{\{ group\.name \}\}[\s\S]+class="key-group-meta"[\s\S]+groupMeta\(key\.groupId\)/);
  assert.doesNotMatch(inventory, /<option[^>]*>\{\{ groupOptionLabel\(group\) \}\}<\/option>/);
  assert.match(panel, /function groupMeta\(groupId: string \| null\)/);
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
  assert.match(panel, /<UiCopyButton :value="revealed\.value" @copied="notice = 'Key 已复制'"/);
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

test("customer overview prioritizes the next real action without duplicate English labels", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const overviewStart = app.indexOf('<section v-if="isOverviewRoute" class="overview-page">');
  const overviewEnd = app.indexOf("workspaceRoute === 'list'", overviewStart);
  const overview = app.slice(overviewStart, overviewEnd);

  assert.match(app, /const overviewPrimaryAction = computed/);
  assert.match(app, /workspaceRows\.value\.find\(\(item\) => item\.state === "running"\)/);
  assert.match(overview, /overviewPrimaryAction\.label/);
  assert.match(overview, /overviewPrimaryAction\.action === ['"]open['"]/);
  assert.match(overview, /overviewPrimaryAction\.action === ['"]create['"]/);
  assert.match(overview, /overviewPrimaryAction\.action === ['"]retry['"]/);
  assert.doesNotMatch(overview, />OPL Cloud<|>Workspaces<|>Announcements</);
  assert.doesNotMatch(overview, /余额、API 费用与 Workspace 状态/);
});

test("Workspace launch surfaces authoritative balance sufficiency before confirmation", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const launchStart = app.indexOf("workspaceRoute === 'new'");
  const launchEnd = app.indexOf("workspaceRoute === 'detail'", launchStart);
  const launch = app.slice(launchStart, launchEnd);

  assert.match(app, /const workspaceLaunchBalanceSufficient = computed/);
  assert.match(app, /BigInt\(wallet\.value\.usdMicros\)/);
  assert.match(app, /BigInt\(selectedPlanPrice\.value\)/);
  assert.match(launch, /本次预付/);
  assert.match(launch, /余额不足，请联系管理员充值/);
  assert.match(launch, /workspaceLaunchBalanceSufficient === false/);
  assert.match(launch, /:disabled="[^\"]*workspaceLaunchBalanceSufficient !== true/);
});

test("Workspace launch presents translated status and phase while preserving raw readback", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const launchStart = app.indexOf("workspaceRoute === 'new'");
  const launchEnd = app.indexOf("workspaceRoute === 'detail'", launchStart);
  const launch = app.slice(launchStart, launchEnd);

  assert.match(app, /function workspaceLaunchStatusLabel\(status: string\)/);
  assert.match(app, /function workspaceLaunchPhaseLabel\(phase: string\)/);
  assert.match(app, /runtime_starting: "正在启动服务"/);
  assert.match(launch, /workspaceLaunchStatusLabel\(launchOperation\.status\)/);
  assert.match(launch, /workspaceLaunchPhaseLabel\(launchOperation\.phase\)/);
  assert.match(launch, /launchOperation\.status/);
  assert.match(launch, /launchOperation\.phase/);
});

test("Workspace detail prioritizes access and credentials before commercial terms", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const detailStart = app.indexOf("workspaceRoute === 'detail'");
  const detailEnd = app.indexOf('v-else-if="apiRoute"', detailStart);
  const detail = app.slice(detailStart, detailEnd);
  const accessIndex = detail.indexOf("访问与凭据");
  const termsIndex = detail.indexOf("套餐与条款");

  assert.ok(accessIndex > -1, "Workspace detail must retain access and credentials");
  assert.ok(termsIndex > accessIndex, "Workspace commercial terms must follow access and credentials");
});

test("customer API surfaces use progressive disclosure and quota visuals without invented analytics", async () => {
  const [app, keysPanel] = await Promise.all([
    source("apps/console-ui/src/App.vue"),
    source("apps/console-ui/src/components/keys/KeysPanel.vue")
  ]);
  const usageStart = app.indexOf("activeApiPage === 'usage'");
  const usageEnd = app.indexOf("<KeysPanel v-else", usageStart);
  const usage = app.slice(usageStart, usageEnd);

  assert.match(usage, /class="usage-summary-strip"/);
  assert.doesNotMatch(usage, /class="metric-row usage-summary-metrics"/);
  assert.match(keysPanel, /<details class="key-advanced-settings"/);
  assert.match(keysPanel, /高级限制/);
  assert.match(keysPanel, /class="key-quota-progress"/);
  assert.match(keysPanel, /quotaUsedUsdMicros/);
  assert.match(keysPanel, /:items="keySecondaryMenuItems\(key\)"/);
  assert.doesNotMatch(app + keysPanel, /模型分布|端点分布|Token 趋势|平均耗时|推理强度|计费模式/);
});

test("final Console tokens use FengGao Lab blue and semantic green", async () => {
  const [styles, tokens] = await Promise.all([
    source("apps/console-ui/src/styles.css"),
    source("apps/console-ui/src/components/ui/tokens.css")
  ]);

  assert.match(styles, /--cobalt:\s*#0969da/);
  assert.match(styles, /--green:\s*#1f883d/);
  assert.match(tokens, /--color-background-primary-solid:\s*#0969da/);
  assert.match(tokens, /--color-background-success-solid:\s*#1f883d/);
});

test("Workspace empty, unavailable, and launch recovery states stay distinct", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  assert.match(app, /暂无 Workspace/);
  assert.match(app, /Workspace 暂不可用/);
  assert.match(app, /访问状态暂不可用/);
  assert.match(app, /status === "waiting" \|\| status === "retryable"/);
  assert.match(app, /Workspace 继续处理中/);
  assert.match(app, /status === "refunded"[\s\S]+Workspace 开通未完成，已退款/);
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
  assert.match(styles, /\.credential-actions\s*\{[^}]*flex-wrap:\s*wrap/);
  assert.match(styles, /\.workspace-access-panel \.data-list a\s*\{[^}]*overflow-wrap:\s*anywhere/);
});
