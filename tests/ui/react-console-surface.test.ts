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
  const [packageSource, viteSource, entrySource, consoleFiles] = await Promise.all([
    source("package.json"),
    source("vite.config.ts"),
    source("apps/console-ui/src/main.tsx"),
    filesUnder("apps/console-ui/src")
  ]);
  const packageJson = JSON.parse(packageSource);

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
});

test("Console surfaces show operational facts instead of implementation guidance", async () => {
  const [customer, admin, sourceState] = await Promise.all([
    source("apps/console-ui/src/pages/CustomerPages.tsx"),
    source("apps/console-ui/src/pages/AdminPages.tsx"),
    source("apps/console-ui/src/components/source/SourceState.tsx")
  ]);

  assert.doesNotMatch(customer, /这里展示 API 服务钱包|不展示 Workspace Receipt|不提供充值按钮|当前 Workspace DTO 投影|只展示当前有效发布时间窗/);
  assert.match(customer, /<h2>API 端点<\/h2>/);
  assert.match(customer, /aria-label="复制 API 端点"/);
  assert.doesNotMatch(admin, /Control Plane 先稳定分页|不在浏览器扫描全部账户|只显示当前 projection|当前健康 DTO 未投影|available \/ empty \/ unavailable|服务端投影的待处理事项/);
  assert.doesNotMatch(sourceState, /权威来源已成功读取|无法确认权威事实|未使用 0 或空列表代替/);
  assert.match(admin, /account-source-summary__item/);
});

test("React shell keeps core customer and Admin tasks navigable", async () => {
  const shell = [
    await source("apps/console-ui/src/layout/ConsoleShell.tsx"),
    await source("apps/console-ui/src/console-model.ts")
  ].join("\n");
  for (const route of [
    "/console/workspaces", "/console/api", "/console/billing", "/admin/accounts"
  ]) assert.ok(shell.includes(route), route);
  for (const label of ["Account Settings", "Support", "退出登录"]) assert.match(shell, new RegExp(label));
  assert.match(shell, /Menu/);
  assert.match(shell, /RefreshCw/);
});

test("Support panel uses the existing external ticket mapping API", async () => {
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
  for (const label of ["登录 OPL Cloud", "无权访问", "正在恢复登录", "无法恢复登录", "页面不存在"]) {
    assert.match(pages, new RegExp(label));
  }
  assert.match(pages, /aria-labelledby="home-heading"/);
  assert.match(pages, /aria-label="产品能力"/);
  assert.match(pages, /autocomplete|autoComplete/);
  assert.match(pages, /submitLogin/);
});

test("React customer pages expose real tasks without fabricated capabilities", async () => {
  const pages = [
    await source("apps/console-ui/src/pages/CustomerPages.tsx"),
    await source("apps/console-ui/src/components/keys/KeysPanel.tsx")
  ].join("\n");
  for (const truth of ["生命周期状态", "Workspace 月度总额", "本月实际费用", "账单收据", "标记已读"]) {
    assert.match(pages, new RegExp(truth));
  }
  assert.doesNotMatch(pages, /localStorage|sessionStorage|公开注册/);
  assert.doesNotMatch(pages, /href=.*充值|onClick=.*充值|>在线充值</);
});

test("React Admin pages expose operator tasks and service truth", async () => {
  const pages = await source("apps/console-ui/src/pages/AdminPages.tsx");
  for (const truth of ["客户与计费账户", "开通用户", "余额操作", "账户已停用", "计费复核", "provider ID", "Control Plane", "Gateway", "Ledger"]) {
    assert.match(pages, new RegExp(truth));
  }
});

test("usage filters expose accessible Apps SDK controls", async () => {
  const [select, customer] = await Promise.all([
    source("apps/console-ui/src/components/ui/Select.tsx"),
    source("apps/console-ui/src/pages/CustomerPages.tsx")
  ]);

  assert.match(select, /block\?: boolean;/);
  assert.match(select, /<AppsSelect[^>]*\bblock=\{block\}/);
  assert.match(customer, /<Select\s+block\s+label="API Key"/);
  assert.match(customer, /<SegmentedControl\s+ariaLabel="统计周期"\s+block/);
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
  assert.doesNotMatch(controller, /recovery-plan/);
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
