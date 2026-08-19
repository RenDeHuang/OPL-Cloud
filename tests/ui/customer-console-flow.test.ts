import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { afterEach, test } from "node:test";

import * as readApi from "../../apps/console-ui/src/api/console-read-api.ts";
import * as workspaceApi from "../../apps/console-ui/src/api/workspaces-api.ts";

const root = new URL("../../", import.meta.url);
const source = (path: string) => readFile(new URL(path, root), "utf8");
const originalFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = originalFetch;
});

test("public entry and Login preserve the OPL Cloud identity", async () => {
  const pages = await source("apps/console-ui/src/pages/PublicPages.tsx");
  assert.match(pages, /src="\/opl-app-icon\.png"/);
  assert.match(pages, /alt="OPL Cloud"/);
  assert.match(pages, /让你的 One Person Lab 在云端继续工作/);
  assert.match(pages, /在线 Workspace/);
  assert.match(pages, /AI API/);
  assert.match(pages, /余额与账单/);
  assert.match(pages, /登录 OPL Cloud/);
  assert.match(pages, /账户由管理员开通/);
  assert.doesNotMatch(pages, /权威控制面|浏览器端业务推导|余额守卫|正式回执|medopl/i);
  assert.match(pages, /autoComplete="email"/);
  assert.match(pages, /autoComplete="current-password"/);
});

test("Workspace access exposes the exact URL, username, password, and Workspace Key", async () => {
  const [pages, controller] = await Promise.all([
    source("apps/console-ui/src/pages/CustomerPages.tsx"),
    source("apps/console-ui/src/app/use-console-controller.ts")
  ]);
  for (const label of ["Workspace URL", "用户名", "密码", "Workspace Key"]) assert.match(pages, new RegExp(label));
  assert.match(controller, /workspace\.workspaceApiKeyId/);
  assert.match(controller, /revealGatewayKey\(workspace\.workspaceApiKeyId, session\.csrfToken\)/);
  assert.match(pages, /Workspace 密码已复制/);
  assert.match(pages, /Workspace Key 已复制/);
  assert.doesNotMatch(`${pages}\n${controller}`, /name === ["']opl-workspace["']/);
});

test("Workspace list, launch, and detail remain independent routes", async () => {
  const [pages, controller, model] = await Promise.all([
    source("apps/console-ui/src/pages/CustomerPages.tsx"),
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("apps/console-ui/src/console-model.ts")
  ]);
  assert.match(pages, /WorkspaceListPage/);
  assert.match(pages, /WorkspaceLaunchPage/);
  assert.match(pages, /WorkspaceDetailPage/);
  assert.match(pages, /changeWorkspacePage/);
  assert.match(pages, /\/console\/workspaces\/new/);
  assert.match(controller, /findWorkspaceInPages\(workspaceId\)/);
  assert.match(controller, /getWorkspaceRuntimeStatus\(workspaceId\)/);
  assert.match(model, /workspaceIdFromPath/);
  assert.doesNotMatch(`${pages}\n${controller}`, /selectedWorkspaceId|账号存在多个 Workspace，暂不可用/);
});

test("Workspace launch uses the selected split-decision flow with API-backed facts", async () => {
  const pages = await source("apps/console-ui/src/pages/CustomerPages.tsx");
  const launch = pages.slice(pages.indexOf("function PlanOption"), pages.indexOf("function SecretRow"));

  assert.match(launch, /function WorkspaceLaunchSteps/);
  assert.match(launch, /function WorkspaceOrderSummary/);
  assert.match(launch, /<RadioGroup<PlanId>/);
  assert.match(launch, /<RadioGroup\.Item/);
  assert.match(launch, /className="workspace-launch-layout"/);
  assert.match(launch, /className="workspace-launch-config"/);
  assert.match(launch, /className="workspace-order-summary"/);
  assert.match(launch, /<Field[\s\S]*label="Workspace 名称"/);
  for (const label of ["配置", "核对", "开通状态", "订单摘要", "价格明细", "可用余额", "按自然月计费", "自动续费关闭"]) {
    assert.match(launch, new RegExp(label));
  }
  for (const fact of ["preview.compute", "preview.storage", "preview.totalChargeUsdMicros", "wallet.usdMicros", "preview.billingUnit"]) {
	assert.match(launch, new RegExp(fact.replaceAll(".", "\\.")));
  }
  assert.doesNotMatch(launch, /plan\.(?:cpu|memoryGb|diskGb|server)/);
  assert.doesNotMatch(launch, /分别来自各自权威来源|浏览器不会自行计算|服务端权威总价/);
});

test("Workspace launch renders only provider-available plans", async () => {
  const pages = await source("apps/console-ui/src/pages/CustomerPages.tsx");
  const launch = pages.slice(pages.indexOf("function WorkspaceLaunchPage"), pages.indexOf("function WorkspaceLaunchConfirm"));

  assert.match(launch, /catalog\.packages\.filter\(\(plan\) => plan\.available && \(plan\.id === "basic" \|\| plan\.id === "pro"\)\)/);
  assert.doesNotMatch(launch, /disabled=\{!plan\.available\}/);
});

test("Workspace launch status shows only the authoritative current phase", async () => {
  const pages = await source("apps/console-ui/src/pages/CustomerPages.tsx");
  const operation = pages.slice(pages.indexOf("function LaunchOperation"), pages.indexOf("function SecretRow"));

  assert.match(operation, /当前处理阶段/);
  assert.match(operation, /operation\.phase/);
  assert.match(operation, /operation\.status/);
  assert.doesNotMatch(operation, /phaseIndex|workspace-progress|className=.*complete|当前阶段.*已完成|已完成.*等待/);
});

test("Workspace adapter exposes paging and exact lookup without an eager all-pages aggregate", () => {
  assert.equal(typeof workspaceApi.getWorkspaces, "function");
  assert.equal(typeof workspaceApi.findWorkspaceInPages, "function");
  assert.equal("getAllWorkspaces" in workspaceApi, false);
});

test("Workspace and Overview render server-owned lifecycle, runtime, and billing facts", async () => {
  const pages = await source("apps/console-ui/src/pages/CustomerPages.tsx");
  for (const value of [
    "生命周期状态", "运行状态", "创建时间", "续费状态", "Workspace 月度总价",
    "持久存储", "挂载检查", "服务健康", "本月实际费用", "当前账户总数"
  ]) assert.match(pages, new RegExp(value));
  assert.match(pages, /detail\.totalUsdMicros/);
  assert.match(pages, /detail\.storageGb/);
  assert.match(pages, /runtimeData\.checks/);
  assert.match(pages, /<dt>CPU \/ 内存规格<\/dt><dd>-<\/dd>/);
  assert.doesNotMatch(pages, /Workspace DTO|套餐 ID 推导/);
  assert.doesNotMatch(pages, /workspace\.packageId === ["']basic["'].*(?:cpu|memory)/s);
  assert.doesNotMatch(pages, /\|\| "opl"/);
});

test("Workspace detail owns the scoped model budget controls", async () => {
  const pages = await source("apps/console-ui/src/pages/CustomerPages.tsx");
  const detail = pages.slice(pages.indexOf("const workspaceBudgetLimitFields"), pages.indexOf("function WorkspaceDetailPage"));
  for (const label of [
    "模型预算", "总额度（micros）", "5 小时限额（micros）", "1 天限额（micros）", "7 天限额（micros）",
    "启用 Workspace Key", "保存预算", "重置总额度用量", "重置滚动窗口用量", "总额度已用（micros）",
    "5 小时已用（micros）", "1 天已用（micros）", "7 天已用（micros）", "更新时间"
  ]) assert.match(detail, new RegExp(label));
  assert.match(detail, /controller\.sources\.workspaceBudget/);
  assert.match(detail, /controller\.updateWorkspaceBudget\(input\)/);
  assert.match(detail, /controller\.updateWorkspaceBudget\(\{ resetQuota: true \}\)/);
  assert.match(detail, /controller\.updateWorkspaceBudget\(\{ resetRateLimitUsage: true \}\)/);
  assert.match(detail, /Number\.isSafeInteger/);
  assert.match(detail, /value\.trim\(\) === ""/);
  assert.doesNotMatch(detail, /controller\.(?:updateGatewayKey|deleteGatewayKey|revealGatewayKey)|groupId|重新绑定|修改名称/);
});

test("Workspace unavailable states offer retry instead of creation", async () => {
  const pages = await source("apps/console-ui/src/pages/CustomerPages.tsx");
  const overview = pages.slice(pages.indexOf("function OverviewPage"), pages.indexOf("function WorkspaceSummaryRow"));
  const list = pages.slice(pages.indexOf("function WorkspaceListPage"), pages.indexOf("function WorkspaceLaunchPage"));
  assert.match(overview, /workspacesUnavailable/);
  assert.match(overview, /workspacesUnavailable \? "重试读取 Workspace"/);
  assert.match(overview, /workspacesUnavailable \? void controller\.refreshCurrentPage\(\)/);
  assert.match(list, /workspacesUnavailable/);
  assert.match(list, /workspacesUnavailable \? void controller\.refreshCurrentPage\(\)/);
  assert.match(list, /workspacesUnavailable \? "重试读取"/);
});

test("general API Key path supports create, readback, reveal, update, and delete", async () => {
  for (const name of ["getGatewayKey", "createGatewayKey", "updateGatewayKey", "deleteGatewayKey", "revealGatewayKey"] as const) {
    assert.equal(typeof readApi[name], "function", `${name} adapter is required`);
  }
  const panel = await source("apps/console-ui/src/components/keys/KeysPanel.tsx");
  for (const call of ["getGatewayKey", "createGatewayKey", "updateGatewayKey", "deleteGatewayKey", "revealGatewayKey"]) {
    assert.match(panel, new RegExp(`${call}\\(`));
  }
  assert.match(panel, /expiresInDays/);
  assert.match(panel, /key\.expiresAt/);
  assert.match(panel, /enabled: key\.status !== "active"/);
  assert.match(panel, /isProtectedWorkspaceKey/);
});

test("API Key and Workspace receipt types use customer-facing labels", async () => {
  const [pages, panel] = await Promise.all([
    source("apps/console-ui/src/pages/CustomerPages.tsx"),
    source("apps/console-ui/src/components/keys/KeysPanel.tsx")
  ]);
  assert.match(pages, /billing\.workspace_purchased\.v1[\s\S]+Workspace 开通/);
  assert.match(pages, /billing\.workspace_expired\.v1[\s\S]+Workspace 到期/);
  assert.match(pages, /return type \? "账单记录" : "暂不可用"/);
  assert.match(panel, /isProtectedWorkspaceKey\(key\) \? "Workspace 系统 Key" : "普通 Key"/);
  assert.match(panel, /mobile-key-list/);
});

test("Console source states expose reason codes and keep empty distinct from unavailable", async () => {
  const [dto, controller, sourceState] = await Promise.all([
    source("apps/console-ui/src/api/dtos.ts"),
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("apps/console-ui/src/components/source/SourceState.tsx")
  ]);

  assert.match(dto, /export type GatewayUsagePeriod = "today" \| "week" \| "month";/);
  assert.match(dto, /reasonCode: string;/);
  assert.match(dto, /typeof dto\.reasonCode !== "string"/);
  assert.match(controller, /replace\(\/\[\^a-z0-9\]\+\/g, "_"\)/);
  assert.match(controller, /getGatewayKeyUsage\(keyId, page, 20, period\)/);
  assert.match(sourceState, /source\.reasonCode/);
  assert.doesNotMatch(sourceState, /description="请稍后重试。"/);
  assert.match(sourceState, /source\.status === "empty"/);
  assert.ok(sourceState.indexOf('source?.status === "unavailable"') < sourceState.indexOf("if (error)"));
});

test("Customer usage controls expose only canonical periods", async () => {
  const pages = await source("apps/console-ui/src/pages/CustomerPages.tsx");
  const usagePage = pages.slice(pages.indexOf("function UsagePage"), pages.indexOf("function ApiPage"));

  assert.match(usagePage, /value: "today", label: "今日"/);
  assert.match(usagePage, /value: "week", label: "本周"/);
  assert.match(usagePage, /value: "month", label: "本月"/);
  assert.doesNotMatch(usagePage, /value: "day"/);
});

test("Customer and Key list empty states trust the source envelope", async () => {
  const [pages, panel] = await Promise.all([
    source("apps/console-ui/src/pages/CustomerPages.tsx"),
    source("apps/console-ui/src/components/keys/KeysPanel.tsx")
  ]);

  assert.doesNotMatch(`${pages}\n${panel}`, /empty=\{[^}]*\.length === 0[^}]*\}/);
  assert.match(pages, /empty=\{controller\.sources\.usageKeys\.value\?\.status === "empty"\}/);
  assert.match(pages, /empty=\{controller\.sources\.usage\.value\?\.status === "empty"\}/);
  assert.match(panel, /empty=\{source\?\.status === "empty"\}/);
});

test("API Key protection trusts backend kind instead of a name prefix", async () => {
  const panel = await source("apps/console-ui/src/components/keys/KeysPanel.tsx");
  assert.match(panel, /function isProtectedWorkspaceKey\(key: GatewayKeySummaryDTO\) \{\s*return key\.kind === "workspace";\s*\}/);
  assert.doesNotMatch(panel, /reservedWorkspaceKeyPrefix|name\.trim\(\)\.toLowerCase\(\)\.startsWith/);
});

test("API Key sources keep network failures independent with stable reason codes", async () => {
  const panel = await source("apps/console-ui/src/components/keys/KeysPanel.tsx");
  assert.match(panel, /import \{ unavailableSource \} from "\.\.\/\.\.\/app\/use-console-controller\.ts";/);
  assert.match(panel, /setSource\(unavailableSource\("sub2api"\)\)/);
  assert.match(panel, /groupResult\.status === "fulfilled" \? groupResult\.value : unavailableSource\("sub2api"\)/);
  assert.match(panel, /endpointResult\.status === "fulfilled" \? endpointResult\.value : unavailableSource\("sub2api"\)/);
  assert.match(panel, /groupsSource\.reasonCode/);
  assert.match(panel, /endpointSource\.reasonCode/);
});

test("Billing, Receipt, and Workspace facts do not label missing optional fields as unavailable", async () => {
  const pages = await source("apps/console-ui/src/pages/CustomerPages.tsx");
  const workspaceDetail = pages.slice(pages.indexOf("function WorkspaceDetailPage"), pages.indexOf("function ApiOverview"));
  const billing = pages.slice(pages.indexOf("function BillingPage"), pages.indexOf("function AnnouncementRows"));

  assert.match(billing, /source=\{controller\.sources\.workspaces\.value\}[\s\S]+empty=\{controller\.sources\.workspaces\.value\?\.status === "empty"\}/);
  assert.match(billing, /source=\{controller\.sources\.receipts\.value\}[\s\S]+empty=\{controller\.sources\.receipts\.value\?\.status === "empty"\}/);
  assert.doesNotMatch(`${workspaceDetail}\n${billing}`, /\|\| "暂不可用"|: "暂不可用"/);
});

test("API Key surface uses the configured endpoint without browser environment fallbacks", async () => {
  const [pages, panel] = await Promise.all([
    source("apps/console-ui/src/pages/CustomerPages.tsx"),
    source("apps/console-ui/src/components/keys/KeysPanel.tsx")
  ]);
  assert.equal(typeof readApi.getGatewayEndpoint, "function");
  assert.equal(typeof readApi.getGatewayGroups, "function");
  assert.match(panel, /getGatewayEndpoint\(\)/);
  assert.match(panel, /getGatewayGroups\(\)/);
  assert.match(panel, /API Endpoint/);
  assert.match(pages, /getGatewayKeyUsage|UsagePage/);
  assert.doesNotMatch(`${pages}\n${panel}`, /OPL_SUB2API_BASE_URL|gflabtoken\.cn|<iframe|window\.__ENV|import\.meta\.env/);
});

test("Overview and API overview own wallet facts while Billing stays focused on terms and receipts", async () => {
  const pages = await source("apps/console-ui/src/pages/CustomerPages.tsx");
  const overview = pages.slice(pages.indexOf("function OverviewPage"), pages.indexOf("function WorkspaceSummaryRow"));
  const api = pages.slice(pages.indexOf("function ApiOverview"), pages.indexOf("function RequestRows"));
  const billing = pages.slice(pages.indexOf("function BillingPage"), pages.indexOf("function AnnouncementRows"));
  assert.match(overview, /sources\.wallet/);
  assert.match(api, /sources\.wallet/);
  assert.match(api, /余额历史/);
  assert.match(billing, /Workspace 条款/);
  assert.match(billing, /账单收据/);
  assert.doesNotMatch(billing, /sources\.wallet|sources\.accountUsage|余额历史/);
});

test("balance history uses the explicit paged DTO", async () => {
  const [dto, api] = await Promise.all([
    source("apps/console-ui/src/api/dtos.ts"),
    source("apps/console-ui/src/api/console-read-api.ts")
  ]);
  assert.match(dto, /export interface GatewayBalanceHistoryPageDTO \{[\s\S]+page: number;[\s\S]+pageSize: number;[\s\S]+pages: number;/);
  assert.doesNotMatch(dto, /interface BalanceHistoryData|type GatewayBalanceHistoryPageDTO = BalanceHistoryData/);
  assert.match(api, /getGatewayBalanceHistory\(page = 1, pageSize = 20/);
});

test("Billing receipt rows open a customer-safe detail with authoritative components", async () => {
  assert.equal(typeof readApi.getBillingReceipt, "function");
  const pages = await source("apps/console-ui/src/pages/CustomerPages.tsx");
  const detail = pages.slice(pages.indexOf("function ReceiptDetail"), pages.indexOf("function AnnouncementRows"));
  for (const label of ["收据详情", "Receipt ID", "Workspace ID", "计算组成金额", "存储组成金额", "扣款引用"]) {
    assert.match(detail, new RegExp(label));
  }
  for (const field of ["receiptId", "status", "createdAt", "workspaceId", "priceVersion", "periodStart", "paidThrough", "components", "chargeReference"]) {
    assert.match(detail, new RegExp(field));
  }
  assert.doesNotMatch(detail, /履约资源引用|fulfillment|computeAllocationId|storageId|attachmentId|workspaceApiKeyId|runtimeId|providerId|rawResponse|secretRef/);
});

test("announcements preserve available, empty, unavailable, retry, and mark-read states", async () => {
  assert.equal(typeof readApi.getAnnouncements, "function");
  assert.equal(typeof readApi.markAnnouncementRead, "function");
  const [pages, controller] = await Promise.all([
    source("apps/console-ui/src/pages/CustomerPages.tsx"),
    source("apps/console-ui/src/app/use-console-controller.ts")
  ]);
  assert.match(pages, /暂无公告/);
  assert.match(pages, /标记已读/);
  assert.match(pages, /refreshCurrentPage/);
  assert.match(controller, /markAnnouncementRead\(announcementId, session\.csrfToken/);
  assert.match(controller, /unavailableSource\("control-plane"\)/);
});

test("customer secrets stay in memory and expire after sixty seconds", async () => {
  const sources = await Promise.all([
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("apps/console-ui/src/components/keys/KeysPanel.tsx"),
    source("apps/console-ui/src/api/auth-api.ts"),
    source("apps/console-ui/src/api/console-read-api.ts"),
    source("apps/console-ui/src/api/workspaces-api.ts")
  ]);
  const browserCode = sources.join("\n");
  assert.doesNotMatch(browserCode, /localStorage|sessionStorage|indexedDB|IndexedDB/);
  assert.match(browserCode, /secretLifetimeMs\s*=\s*60_000/);
  assert.match(browserCode, /window\.setTimeout\(clearSecrets?, secretLifetimeMs\)|window\.setTimeout\(clearSecret, secretLifetimeMs\)/);
  assert.match(browserCode, /secretRequestGeneration\.current \+= 1/);
});

test("Customer Console does not render paused Runtime file facts", async () => {
  assert.equal("getWorkspaceFiles" in workspaceApi, false);
  assert.equal("getWorkspaceFilesystemUsage" in workspaceApi, false);
  const [pages, workspaceSource] = await Promise.all([
    source("apps/console-ui/src/pages/CustomerPages.tsx"),
    source("apps/console-ui/src/api/workspaces-api.ts")
  ]);
  assert.doesNotMatch(pages, /文件与目录|实际空间用量|WorkspaceFilePageDTO|WorkspaceFilesystemUsageDTO/);
  assert.doesNotMatch(workspaceSource, /\/files\?|\/filesystem-usage/);
});

test("Customer source blocks remain independently retryable", async () => {
  const [pages, controller, sourceState] = await Promise.all([
    source("apps/console-ui/src/pages/CustomerPages.tsx"),
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("apps/console-ui/src/components/source/SourceState.tsx")
  ]);
  for (const key of ["runtime", "usage", "usageSummary", "accountUsage", "receipts", "announcements"]) {
    assert.match(controller, new RegExp(`(?:beginSource|failSource)\\(\"${key}\"`));
  }
  assert.match(sourceState, /onRetry/);
  assert.match(sourceState, /source\?\.status === "unavailable"/);
  assert.match(pages, /refreshCurrentPage/);
  const usagePage = pages.slice(pages.indexOf("function UsagePage"), pages.indexOf("function ApiPage"));
  assert.match(usagePage, /sources\.usageSummary/);
  assert.match(usagePage, /sources\.usage/);
  assert.ok((usagePage.match(/<SourceState/g) || []).length >= 3);
});

test("Customer usage records expose authoritative Token, cost, latency, and time facts", async () => {
  const [pages, dtos] = await Promise.all([
    source("apps/console-ui/src/pages/CustomerPages.tsx"),
    source("apps/console-ui/src/api/dtos.ts")
  ]);
  const requestRows = pages.slice(pages.indexOf("function RequestRows"), pages.indexOf("function UsagePage"));

  assert.match(dtos, /durationMs:\s*number \| null;/);
  assert.match(dtos, /firstTokenMs:\s*number \| null;/);
  assert.match(requestRows, /<th>模型 \/ 端点<\/th><th>Token<\/th><th>费用<\/th><th>延迟<\/th><th>时间<\/th><th>请求 ID<\/th>/);
  for (const label of ["输入", "输出", "缓存读取", "缓存写入", "首字", "总耗时"]) {
    assert.match(pages, new RegExp(label));
  }
  assert.match(pages, /item\.cacheReadTokens > 0/);
  assert.match(pages, /item\.cacheCreationTokens > 0/);
  assert.match(requestRows, /<dt>Token<\/dt>/);
  assert.match(requestRows, /<dt>费用<\/dt>/);
  assert.match(requestRows, /<dt>延迟<\/dt>/);
  assert.match(requestRows, /<dt>时间<\/dt>/);
  assert.match(pages, /function formatLatency\(value: number \| null\)/);
  assert.match(pages, /value === null \? "-" : `\$\{formatCount\(value\)\} ms`/);
  assert.doesNotMatch(requestRows, /firstTokenMs \|\||durationMs \|\|/);
});

test("Customer Console renders automatic-renewal state without inventing an enable path", async () => {
  const pages = await source("apps/console-ui/src/pages/CustomerPages.tsx");
  assert.match(pages, /自动续费/);
  assert.match(pages, /detail\.autoRenew === true \? "开启" : detail\.autoRenew === false \? "关闭" : "-"/);
  assert.doesNotMatch(pages, /自动续费启用路径未开放|updateWorkspaceRenewal|setAutoRenew|onChange=.*autoRenew/);
});
