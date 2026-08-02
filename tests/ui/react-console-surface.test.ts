import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile, readdir } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);
const source = (path: string) => readFile(new URL(path, root), "utf8").catch(() => "");

async function filesUnder(directory: string): Promise<string[]> {
  const entries = await readdir(new URL(`${directory}/`, root), { withFileTypes: true }).catch(() => []);
  const files: string[] = [];
  for (const entry of entries) {
    const path = `${directory}/${entry.name}`;
    if (entry.isDirectory()) files.push(...await filesUnder(path));
    if (entry.isFile()) files.push(path);
  }
  return files;
}

test("Console runtime is React and Vue is retired", async () => {
  const [packageSource, viteSource, entrySource, contractSource, consoleFiles] = await Promise.all([
    source("package.json"),
    source("vite.config.ts"),
    source("apps/console-ui/src/main.tsx"),
    source("packages/contracts/opl-cloud-console-ui-contract.json"),
    filesUnder("apps/console-ui/src")
  ]);
  const packageJson = JSON.parse(packageSource);
  const contract = JSON.parse(contractSource);

  assert.ok(packageJson.dependencies.react);
  assert.ok(packageJson.dependencies["react-dom"]);
  assert.ok(packageJson.dependencies["@openai/apps-sdk-ui"]);
  assert.ok(packageJson.dependencies["lucide-react"]);
  assert.equal(packageJson.dependencies.vue, undefined);
  assert.equal(packageJson.dependencies["@lucide/vue"], undefined);
  assert.equal(packageJson.dependencies["@vitejs/plugin-vue"], undefined);
  assert.equal(packageJson.devDependencies?.["vue-tsc"], undefined);
  assert.match(viteSource, /@vitejs\/plugin-react/);
  assert.doesNotMatch(viteSource, /plugin-vue|\bvue\(\)/);
  assert.match(entrySource, /createRoot/);
  assert.match(entrySource, /<App\s*\/>/);
  assert.equal(consoleFiles.some((path) => path.endsWith(".vue")), false);
  assert.equal(contract.framework, "react");
  assert.equal(contract.componentFoundation, "@openai/apps-sdk-ui");
});

test("React Console exposes the frozen customer and Admin surfaces", async () => {
  const [model, shell, customerPages, adminPages] = await Promise.all([
    source("apps/console-ui/src/console-model.ts"),
    source("apps/console-ui/src/layout/ConsoleShell.tsx"),
    source("apps/console-ui/src/pages/CustomerPages.tsx"),
    source("apps/console-ui/src/pages/AdminPages.tsx")
  ]);
  const joined = [model, shell, customerPages, adminPages].join("\n");
  for (const label of [
    "概览", "Workspace", "API 服务", "账单", "公告",
    "运维概览", "客户与计费账户", "计费复核", "资源状态", "系统状态"
  ]) assert.match(joined, new RegExp(label));
  for (const route of [
    "/console/overview", "/console/workspaces", "/console/api", "/console/billing", "/console/announcements",
    "/admin/overview", "/admin/accounts", "/admin/billing", "/admin/resources", "/admin/system"
  ]) assert.ok(joined.includes(route), route);
});

test("React Console uses the immutable Quiet Ledger visual contract", async () => {
  const [overview, shell, styles, contractSource, referenceImage] = await Promise.all([
    source("apps/console-ui/src/pages/CustomerPages.tsx"),
    source("apps/console-ui/src/layout/ConsoleShell.tsx"),
    source("apps/console-ui/src/styles.css"),
    source("packages/contracts/opl-cloud-console-ui-contract.json"),
    readFile(new URL("output/imagegen/opl-console-option-1-quiet-ledger-1440x1024.png", root))
  ]);
  const contract = JSON.parse(contractSource);
  const referenceHash = createHash("sha256").update(referenceImage).digest("hex");

  assert.match(shell, /data-visual-direction="quiet-ledger"/);
  assert.match(overview, /className="overview-summary"/);
  assert.doesNotMatch(overview, />\s*C-OV-01\s*</);
  assert.doesNotMatch(overview, /分别来自各自权威来源/);
  assert.match(styles, /--action:\s*#075b3b;/);
  assert.match(styles, /\/\* Quiet Ledger visual contract\. \*\/[\s\S]*?\.sidebar\s*\{[\s\S]*?width:\s*240px;/);
  assert.match(styles, /\.overview-primary-action\s*\{[\s\S]*?z-index:\s*25;/);
  assert.match(styles, /\.overview-grid\s*\{[\s\S]*?grid-template-columns:\s*minmax\(0,\s*1\.2fr\)\s*minmax\(420px,\s*1fr\);/);
  assert.match(styles, /\.overview-workspace-table table\s*\{[\s\S]*?font-size:\s*14px;/);
  assert.match(styles, /\.compact-announcement-list article\s*\{[\s\S]*?grid-template-columns:\s*minmax\(240px,\s*\.55fr\)\s*minmax\(0,\s*1fr\)\s*auto;/);
  assert.match(styles, /@media \(max-width:\s*820px\)[\s\S]*?\.topbar\s*\{[\s\S]*?padding:\s*0 16px 0 68px;/);
  assert.equal(contract.visualDirection.id, "quiet-ledger");
  assert.equal(contract.visualDirection.layout, "240px_light_rail_white_canvas_open_metrics_workspace_primary");
  assert.equal(contract.visualDirection.reference.path, "output/imagegen/opl-console-option-1-quiet-ledger-1440x1024.png");
  assert.equal(contract.visualDirection.reference.sha256, "9b75ba8b01cda552fcb7d44bc774797f22697f5fd4a0c067e885bc4895b738b5");
  assert.equal(referenceHash, contract.visualDirection.reference.sha256);
});

test("React Console freezes Sub2API-aligned account and usage presentation", async () => {
  const contract = JSON.parse(await source("packages/contracts/opl-cloud-console-ui-contract.json"));

  assert.deepEqual(contract.usageRecordPresentation, {
    desktopColumns: ["model_endpoint", "tokens", "actual_cost", "latency", "time", "request_id"],
    tokenFields: ["inputTokens", "outputTokens", "cacheReadTokens", "cacheCreationTokens"],
    latencyFields: ["firstTokenMs", "durationMs"],
    missingLatency: "dash_never_zero_or_derived"
  });
  assert.deepEqual(contract.operatorAccountPresentation, {
    desktopColumns: ["user", "account_mapping", "balance", "api_cost", "resources", "status", "actions"],
    nestedSourceDiagnostics: "account_detail_only",
    browserSearchOrSort: false,
    mobile: "compact_cards_same_fact_order"
  });
});

test("React Console freezes the selected Split Decision Workspace launch reference", async () => {
  const [styles, contractSource, referenceImage] = await Promise.all([
    source("apps/console-ui/src/styles.css"),
    source("packages/contracts/opl-cloud-console-ui-contract.json"),
    readFile(new URL("output/imagegen/opl-workspace-launch-option-1-split-decision-1440x1024.png", root))
  ]);
  const contract = JSON.parse(contractSource);
  const referenceHash = createHash("sha256").update(referenceImage).digest("hex");

  assert.equal(contract.workspaceLaunchVisual.id, "split-decision");
  assert.equal(contract.workspaceLaunchVisual.reference.path, "output/imagegen/opl-workspace-launch-option-1-split-decision-1440x1024.png");
  assert.equal(contract.workspaceLaunchVisual.reference.sha256, "897f657539d2ccd8e10df365e5108094bee3a68ffd1c349190f692501657b4b6");
  assert.equal(referenceHash, contract.workspaceLaunchVisual.reference.sha256);
  assert.deepEqual(contract.workspaceLaunchVisual.authoritativeReads, [
    "GET /api/pricing/catalog",
    "POST /api/pricing/preview",
    "GET /api/gateway/wallet",
    "POST /api/workspace-launches",
    "GET /api/workspace-launches/{operationId}"
  ]);
  assert.deepEqual(contract.workspaceLaunchVisual.fieldAudit.configure.map((entry: { api: string }) => entry.api), [
    "GET /api/pricing/catalog",
    "POST /api/pricing/preview",
    "GET /api/gateway/wallet",
    "POST /api/workspace-launches"
  ]);
  assert.deepEqual(contract.workspaceLaunchVisual.fieldAudit.operation[0].fields, [
    "operationId", "status", "phase", "name", "packageId", "priceVersion", "currency",
    "totalChargeUsdMicros", "autoRenew", "createdAt", "updatedAt", "errorCode"
  ]);
  assert.deepEqual(contract.workspaceLaunchVisual.submissionGuard, {
    inputs: ["wallet.data.usdMicros", "preview.totalChargeUsdMicros"],
    comparison: "strict_gt",
    authority: "client_feedback_only_server_rechecks"
  });
  assert.equal(contract.workspaceLaunchVisual.fieldAudit.presentationOnly[0].businessFact, false);
  assert.ok(contract.workspaceLaunchVisual.forbiddenPresentation.includes("inferred_per_phase_completion"));
  assert.match(styles, /\/\* Split Decision Workspace launch contract\. \*\/[\s\S]*?\.workspace-launch-layout\s*\{[\s\S]*?grid-template-columns:\s*minmax\(0,\s*1fr\)\s*320px;/);
});

test("Quiet Ledger surfaces show operational facts instead of implementation guidance", async () => {
  const [customer, admin, sourceState, styles] = await Promise.all([
    source("apps/console-ui/src/pages/CustomerPages.tsx"),
    source("apps/console-ui/src/pages/AdminPages.tsx"),
    source("apps/console-ui/src/components/source/SourceState.tsx"),
    source("apps/console-ui/src/styles.css")
  ]);

  assert.doesNotMatch(customer, /这里展示 API 服务钱包|不展示 Workspace Receipt|不提供充值按钮|当前 Workspace DTO 投影|只展示当前有效发布时间窗/);
  assert.match(customer, /<h2>API 端点<\/h2>/);
  assert.match(customer, /aria-label="复制 API 端点"/);
  assert.doesNotMatch(admin, /Control Plane 先稳定分页|不在浏览器扫描全部账户|只显示当前 projection|当前健康 DTO 未投影|available \/ empty \/ unavailable|服务端投影的待处理事项/);
  assert.doesNotMatch(sourceState, /权威来源已成功读取|无法确认权威事实|未使用 0 或空列表代替/);
  assert.match(admin, /account-source-summary__item/);
  assert.match(styles, /@media \(min-width:\s*821px\)[\s\S]*?\.operator-account-table\s*\{[\s\S]*?display:\s*block;[\s\S]*?\.operator-account-mobile-list\s*\{[\s\S]*?display:\s*none;/);
});

test("React shell exposes complete customer, API and Admin navigation", async () => {
  const shell = [
    await source("apps/console-ui/src/layout/ConsoleShell.tsx"),
    await source("apps/console-ui/src/console-model.ts")
  ].join("\n");
  for (const route of [
    "/console/overview", "/console/workspaces", "/console/api", "/console/billing", "/console/announcements",
    "/console/api/usage", "/console/api/keys",
    "/admin/overview", "/admin/accounts", "/admin/billing", "/admin/resources", "/admin/system"
  ]) assert.ok(shell.includes(route), route);
  for (const label of ["Account Settings", "Support", "退出登录"]) assert.match(shell, new RegExp(label));
  assert.match(shell, /Menu/);
  assert.match(shell, /RefreshCw/);
});

test("Support slide uses the existing external ticket mapping API", async () => {
  const [shell, controller, api, dto] = await Promise.all([
    source("apps/console-ui/src/layout/ConsoleShell.tsx"),
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("apps/console-ui/src/api/console-read-api.ts"),
    source("apps/console-ui/src/api/dtos.ts")
  ]);
  assert.match(dto, /interface SupportTicketMappingDTO/);
  assert.match(api, /getSupportTickets[\s\S]+\/api\/support\/tickets/);
  assert.match(api, /createSupportTicketMapping[\s\S]+\/api\/support\/tickets/);
  assert.match(controller, /supportTickets/);
  for (const label of ["外部工单映射", "外部工单号", "外部工单链接", "新增映射"]) {
    assert.match(shell, new RegExp(label));
  }
  assert.doesNotMatch(shell, />\s*创建工单\s*</);
});

test("React public pages cover login, forbidden, recovery and not found", async () => {
  const pages = await source("apps/console-ui/src/pages/PublicPages.tsx");
  for (const label of ["Console 登录", "无权访问", "正在恢复登录", "无法恢复登录", "页面不存在"]) {
    assert.match(pages, new RegExp(label));
  }
  assert.match(pages, /autocomplete|autoComplete/);
  assert.match(pages, /submitLogin/);
});

test("React customer pages expose every frozen customer slide", async () => {
  const pages = [
    await source("apps/console-ui/src/pages/CustomerPages.tsx"),
    await source("apps/console-ui/src/components/keys/KeysPanel.tsx")
  ].join("\n");
  for (const slide of ["C-OV-01", "C-WS-01", "C-WS-02", "C-WS-03", "C-WS-04", "C-WS-05", "C-API-01", "C-API-02", "C-API-03", "C-BIL-01", "C-BIL-02", "C-BIL-03", "C-ANN-01"]) {
    assert.ok(pages.includes(slide), slide);
  }
  for (const truth of ["生命周期状态", "Workspace 月度总额", "本月实际费用", "账单收据", "标记已读"]) {
    assert.match(pages, new RegExp(truth));
  }
  assert.doesNotMatch(pages, /localStorage|sessionStorage|公开注册/);
  assert.doesNotMatch(pages, /href=.*充值|onClick=.*充值|>在线充值</);
});

test("React Admin pages expose every frozen operator slide", async () => {
  const pages = await source("apps/console-ui/src/pages/AdminPages.tsx");
  for (const slide of ["A-OV-01", "A-OV-02", "A-ACC-01", "A-ACC-02", "A-ACC-03", "A-REC-01", "A-REC-02", "A-RES-01", "A-RES-02", "A-SYS-01"]) {
    assert.ok(pages.includes(slide), slide);
  }
  for (const truth of ["客户与计费账户", "开通用户", "余额操作", "账户已停用", "计费复核", "provider ID", "Control Plane", "Gateway", "Ledger"]) {
    assert.match(pages, new RegExp(truth));
  }
});

test("React Console preserves scan-first responsive presentation contracts", async () => {
  const [customer, admin, styles] = await Promise.all([
    source("apps/console-ui/src/pages/CustomerPages.tsx"),
    source("apps/console-ui/src/pages/AdminPages.tsx"),
    source("apps/console-ui/src/styles.css")
  ]);

  assert.match(styles, /\.source-empty\s*\{[\s\S]+place-items:\s*center;[\s\S]+text-align:\s*center;/);
  assert.match(customer, /<strong>\{workspaceLifecycleLabel\(workspace\.state\)\}<\/strong><small>生命周期状态<\/small>/);
  assert.match(customer, /<strong>\{formatDate\(workspace\.paidThrough\)\}<\/strong><small>权益截止<\/small>/);
  assert.doesNotMatch(customer, /<small>paidThrough<\/small>/);
  assert.match(customer, /className="usage-fact-stack usage-token-stack"/);
  assert.match(customer, /className="request-mobile-facts"/);
  assert.match(styles, /\.usage-fact-stack\s*\{[\s\S]+display:\s*grid;/);
  assert.match(styles, /\.request-mobile-facts\s*\{[\s\S]+grid-template-columns:/);

  for (const className of ["operator-account-mobile-list", "operator-workspace-mobile-list", "operator-resource-mobile-list", "operator-health-mobile-list"]) {
    assert.match(admin, new RegExp(`className="${className}"`));
  }
  for (const className of ["operator-account-table", "operator-workspace-table", "operator-resource-detail-table", "operator-health-table"]) {
    assert.match(admin, new RegExp(`className="table-wrap ${className}"`));
  }
  assert.match(admin, /className="source-value"/);
  assert.match(admin, /className="account-source-summary"/);
  assert.match(admin, /className="operator-account-identity"/);
  assert.match(admin, /className="account-mapping-stack"/);
  assert.match(admin, /className="account-resource-stack"/);
  assert.match(styles, /\.source-value\s*\{[\s\S]+display:\s*grid;/);
  assert.match(styles, /\.account-source-summary\s*\{[\s\S]+gap:/);
  assert.match(styles, /\.operator-account-identity\s*\{[\s\S]+display:\s*grid;/);
  assert.match(styles, /\.account-mapping-stack\s*\{[\s\S]+display:\s*grid;/);
  assert.match(styles, /\.account-resource-stack\s*\{[\s\S]+display:\s*grid;/);
  assert.match(styles, /@media \(max-width:\s*820px\)[\s\S]+\.operator-account-table,[\s\S]+\.operator-workspace-table,[\s\S]+\.operator-health-table\s*\{[\s\S]+display:\s*none;/);
});

test("usage filters opt into full-width Apps SDK controls", async () => {
  const [select, customer, styles] = await Promise.all([
    source("apps/console-ui/src/components/ui/Select.tsx"),
    source("apps/console-ui/src/pages/CustomerPages.tsx"),
    source("apps/console-ui/src/styles.css")
  ]);

  assert.match(select, /block\?: boolean;/);
  assert.match(select, /<AppsSelect[^>]*\bblock=\{block\}/);
  assert.match(customer, /<Select\s+block\s+label="API Key"/);
  assert.match(customer, /<SegmentedControl\s+ariaLabel="统计周期"\s+block/);
  assert.match(styles, /\/\* Apps SDK usage toolbar layout\. \*\/[\s\S]*?\.gateway-usage-toolbar\s*\{[\s\S]*?display:\s*grid;[\s\S]*?grid-template-columns:\s*minmax\(260px,\s*420px\)\s+minmax\(240px,\s*300px\);/);
  assert.match(styles, /\/\* Apps SDK usage toolbar layout\. \*\/[\s\S]*?\.gateway-usage-toolbar\s*\{[\s\S]*?margin:\s*16px 18px;/);
  assert.match(styles, /\.gateway-usage-toolbar \.console-field,[\s\S]*?\.gateway-usage-toolbar \.console-select,[\s\S]*?\.gateway-usage-toolbar > \[role="radiogroup"\]\s*\{[\s\S]*?width:\s*100%;[\s\S]*?min-width:\s*0;/);
  assert.match(styles, /@media \(max-width:\s*820px\)[\s\S]*?\.gateway-usage-toolbar\s*\{[\s\S]*?grid-template-columns:\s*minmax\(0,\s*1fr\);/);
});

test("Admin account actions preserve table-cell layout", async () => {
  const styles = await source("apps/console-ui/src/styles.css");

  assert.match(styles, /\/\* Admin account action-cell guard\. \*\/[\s\S]*?\.operator-account-table td\.table-actions\s*\{[\s\S]*?display:\s*table-cell;[\s\S]*?min-width:\s*0;/);
  assert.match(styles, /\.operator-account-table \.operator-card-actions\s*\{[\s\S]*?display:\s*grid;[\s\S]*?grid-template-columns:\s*repeat\(2,\s*max-content\);/);
});

test("account provisioning preserves the command readback", async () => {
  const [controller, admin] = await Promise.all([
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("apps/console-ui/src/pages/AdminPages.tsx")
  ]);
  assert.match(controller, /operatorProvisionOperation/);
  assert.match(controller, /const result = await provisionOperatorAccount/);
  for (const label of ["operation ID", "状态", "phase", "errorCode"]) assert.match(admin, new RegExp(label));
  assert.doesNotMatch(admin, /<dt>operation ID<\/dt><dd>暂不可用<\/dd>/);
});

test("React controller preserves source truth, idempotency and secret lifetime", async () => {
  const controller = await source("apps/console-ui/src/app/use-console-controller.ts");
  assert.match(controller, /secretLifetimeMs\s*=\s*60_000/);
  assert.match(controller, /clearSecrets/);
  assert.match(controller, /workspaceLaunchIdempotencyKey/);
  assert.match(controller, /requestGeneration/);
  assert.match(controller, /unavailableSource/);
  assert.match(
    controller,
    /if \(!session \|\| !review\.allowedActions\.includes\("diagnose_workspace_recovery_plan"\)\) return null;/
  );
  assert.doesNotMatch(controller, /localStorage|sessionStorage/);
});

test("React object-form handlers snapshot DOM values before queued state updates", async () => {
  const forms = [
    await source("apps/console-ui/src/layout/ConsoleShell.tsx"),
    await source("apps/console-ui/src/pages/AdminPages.tsx")
  ].join("\n");
  assert.doesNotMatch(forms, /setForm\(\([^)]*\)\s*=>[^\n]*event\.currentTarget\.(?:value|checked)/);
});

test("Modal focus lifecycle is stable across parent rerenders", async () => {
  const modal = await source("apps/console-ui/src/components/ui/Modal.tsx");
  assert.match(modal, /const onCloseRef = useRef\(onClose\)/);
  assert.match(modal, /onCloseRef\.current = onClose/);
  assert.match(modal, /onCloseRef\.current\(\)/);
  assert.match(modal, /\}, \[open\]\);/);
  assert.doesNotMatch(modal, /\}, \[onClose, open\]\);/);
});
