import assert from "node:assert/strict";
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
  for (const truth of ["客户与计费账户", "开通用户", "余额操作", "计费复核", "provider ID", "Control Plane", "Gateway", "Ledger"]) {
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

  for (const className of ["operator-account-mobile-list", "operator-workspace-mobile-list", "operator-resource-mobile-list", "operator-health-mobile-list"]) {
    assert.match(admin, new RegExp(`className="${className}"`));
  }
  for (const className of ["operator-account-table", "operator-workspace-table", "operator-resource-detail-table", "operator-health-table"]) {
    assert.match(admin, new RegExp(`className="table-wrap ${className}"`));
  }
  assert.match(admin, /className="source-value"/);
  assert.match(admin, /className="account-source-summary"/);
  assert.match(admin, /className="operator-account-identity"/);
  assert.match(styles, /\.source-value\s*\{[\s\S]+display:\s*grid;/);
  assert.match(styles, /\.account-source-summary\s*\{[\s\S]+gap:/);
  assert.match(styles, /\.operator-account-identity\s*\{[\s\S]+display:\s*grid;/);
  assert.match(styles, /@media \(max-width:\s*820px\)[\s\S]+\.operator-account-table,[\s\S]+\.operator-workspace-table,[\s\S]+\.operator-health-table\s*\{[\s\S]+display:\s*none;/);
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
    /if \(!review\.allowedActions\.includes\("recover_workspace_launch"\) && !review\.allowedActions\.includes\("resolve_billing_review"\)\) return;/
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
