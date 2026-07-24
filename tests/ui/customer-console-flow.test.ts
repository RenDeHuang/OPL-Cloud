import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import * as readApi from "../../apps/console-ui/src/api/console-read-api.ts";
import * as workspaceApi from "../../apps/console-ui/src/api/workspaces-api.ts";

const root = new URL("../../", import.meta.url);
const source = (path: string) => readFile(new URL(path, root), "utf8");

test("Home Login Logo unchanged", async () => {
  const app = await source("apps/console-ui/src/App.vue");

  assert.match(app, /<nav class="public-nav"><a href="\/" class="brand" @click\.prevent="navigate\('\/'\)"><img src="\/opl-app-icon\.png" alt="" \/><strong>OPL Cloud<\/strong><\/a><button class="button secondary" type="button" @click="navigate\('\/login'\)">登录<\/button><\/nav>/);
  assert.match(app, /<p class="kicker">One Person Lab<\/p><h1>OPL Cloud<\/h1><p>邀请制 Workspace 与 API 服务。<\/p>/);
  assert.match(app, /<section class="login-panel"><div class="login-brand"><img src="\/opl-app-icon\.png" alt="" \/><div><strong>OPL Cloud<\/strong><span>Console 登录<\/span><\/div><\/div><form @submit\.prevent="submitLogin">/);
  assert.match(app, /<button class="back-button" type="button" @click="navigate\('\/'\)">返回<\/button>/);
});

test("Workspace access answers URL username password and corresponding Workspace Key", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const workspaceView = app.slice(app.indexOf("path.startsWith('/console/workspace')"), app.indexOf("<section v-else-if=\"apiRoute\""));

  for (const label of ["Workspace URL", "用户名", "密码", "Workspace Key"]) {
    assert.match(workspaceView, new RegExp(label));
  }
  assert.match(app, /workspace\.value\?\.workspaceApiKeyId/);
  assert.match(app, /revealGatewayKey\(workspaceKeyId\.value, session\.value\?\.csrfToken \|\| ""\)/);
  assert.match(workspaceView, /@click="copyWorkspacePassword"/);
  assert.match(workspaceView, /@click="copyWorkspaceKey"/);
  assert.doesNotMatch(app, /keys\.value\.find\(\(item\) => item\.name === "opl-workspace"\)/);
});

test("Workspace access selects one of many independent Workspace subscriptions", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const workspaceView = app.slice(app.indexOf("path.startsWith('/console/workspace')"), app.indexOf("<section v-else-if=\"apiRoute\""));

  assert.match(app, /const selectedWorkspaceId = ref\(""\)/);
  assert.match(app, /workspaceSource\.value\.data\.items\.find\(\(item\) => item\.id === selectedWorkspaceId\.value\)/);
  assert.match(app, /function selectWorkspace\(workspaceId: string\)/);
  assert.match(app, /selectWorkspace[\s\S]+clearSecrets\(\)[\s\S]+workspaceStatusSource\.value = null[\s\S]+runtimeRotationIntent = null/);
  assert.match(workspaceView, /aria-label="选择 Workspace"/);
  assert.match(workspaceView, /v-for="item in workspaceSource\.data\.items"/);
  assert.match(workspaceView, /@change="changeWorkspaceSelection"/);
  assert.match(app, /if \(next === "workspace"\) launchForm\.name = ""/);
  assert.match(workspaceView, /新建 Workspace/);
  assert.doesNotMatch(app, /items\.length !== 1/);
  assert.doesNotMatch(app, /账号存在多个 Workspace，暂不可用/);
});

test("Workspace and Overview render only server-owned package runtime and billing facts", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const workspaceView = app.slice(app.indexOf("path.startsWith('/console/workspace')"), app.indexOf("<section v-else-if=\"apiRoute\""));
  const overviewStart = app.indexOf("<section v-if=\"path === '/console' || path === '/console/overview'\" class=\"overview-layout\"");
  const overviewEnd = app.indexOf("<section v-else-if=\"path.startsWith('/console/workspace')\"", overviewStart);
  const overview = app.slice(overviewStart, overviewEnd);

  assert.match(app, /const workspacePlan = computed\(\(\) => catalog\.value\?\.packages\.find\(\(plan\) => plan\.id === workspace\.value\?\.packageId\) \|\| null\)/);
  assert.match(app, /const mountCheck = computed\(\(\) => runtime\.value\?\.checks\.find\(\(check\) => check\.name === "ready_pod_uses_retained_pvc"\) \|\| null\)/);
  assert.match(workspaceView, /<dt>创建时间<\/dt><dd>\{\{ formatDate\(workspace\.createdAt, true\) \}\}<\/dd>/);
  assert.match(workspaceView, /<dt>续费状态<\/dt><dd>\{\{ workspace\.renewalStatus \|\| "暂不可用" \}\}<\/dd>/);
  assert.match(workspaceView, /<dt>套餐规格<\/dt>[\s\S]+workspacePlan\.cpu[\s\S]+workspacePlan\.memoryGb/);
  assert.match(workspaceView, /<dt>挂载状态<\/dt>[\s\S]+mountCheck\.ok/);
  assert.match(workspaceView, /<dt>服务健康<\/dt>[\s\S]+runtime\.ready/);

  assert.match(overview, /Workspace 月费[\s\S]+typeof workspace\?\.totalUsdMicros === "number"[\s\S]+formatUsdMicros\(workspace\.totalUsdMicros\)/);
  assert.match(overview, /计费周期[\s\S]+workspace\?\.periodStart[\s\S]+workspace\?\.paidThrough[\s\S]+formatDate\(workspace\.periodStart\)[\s\S]+formatDate\(workspace\.paidThrough\)/);
  assert.doesNotMatch(overview, /previews|selectedPlanPrice|totalChargeUsdMicros/);
});

test("Customer Console general Key path supports create read reveal toggle and delete", async () => {
  for (const name of ["getGatewayKey", "createGatewayKey", "updateGatewayKey", "deleteGatewayKey", "revealGatewayKey"] as const) {
    assert.equal(typeof readApi[name], "function", `${name} adapter is required`);
  }

  const app = await source("apps/console-ui/src/components/keys/KeysPanel.vue");
  for (const call of ["getGatewayKey", "createGatewayKey", "updateGatewayKey", "deleteGatewayKey"]) {
    assert.match(app, new RegExp(`${call}\\(`));
  }
  assert.match(app, /expiresInDays/);
  assert.match(app, /key\.expiresAt/);
  assert.match(app, /revealGatewayKey\(key\.id,/);
  assert.match(app, /enabled:\s*key\.status !== "active"/);
  assert.doesNotMatch(app, /createGatewayKey\([\s\S]{0,500}updateGatewayKey\(/);
});

test("API Key kind and Workspace receipt types use customer-facing labels", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const keysView = await source("apps/console-ui/src/components/keys/KeysPanel.vue");
  const labelStart = app.indexOf("function receiptLabel(type: string)");
  const labelEnd = app.indexOf("\n}", labelStart) + 2;
  const receiptLabel = new Function(`${app.slice(labelStart, labelEnd).replace("type: string", "type")}\nreturn receiptLabel;`) as () => (type: string) => string;

  assert.equal(receiptLabel()("billing.workspace_purchased.v1"), "Workspace 开通");
  assert.equal(receiptLabel()("billing.workspace_expired.v1"), "Workspace 到期");
  assert.equal(receiptLabel()("billing.internal_future.v9"), "账单记录");
  assert.match(keysView, /<th>名称<\/th>/);
  assert.match(keysView, /key\.kind === "workspace" \? "系统 Key" : "普通 Key"/);
  assert.match(keysView, /revealed\?\.id === key\.id[\s\S]+:colspan="columnCount"/);
});

test("Customer Console API projects the configured endpoint and uses V2 usage owners", async () => {
  const [app, keysPanel] = await Promise.all([
    source("apps/console-ui/src/App.vue"),
    source("apps/console-ui/src/components/keys/KeysPanel.vue")
  ]);

  for (const call of [
    "getGatewayKeyUsage", "getGatewayKeyUsageSummary", "getGatewayAccountUsageSummary"
  ]) assert.match(app, new RegExp(`${call}\\(`));
  assert.doesNotMatch(app, /\bgetGatewayUsage\(/);
  assert.doesNotMatch(app, /\bgetGatewayUsageStats\(/);
  assert.equal(typeof readApi.getGatewayEndpoint, "function");
  assert.equal(typeof readApi.getGatewayGroups, "function");
  assert.match(keysPanel, /getGatewayEndpoint\(/);
  assert.match(keysPanel, /getGatewayGroups\(/);
  assert.match(keysPanel, /API Endpoint/);
  assert.doesNotMatch(keysPanel, /OPL_SUB2API_BASE_URL|gflabtoken\.cn|<iframe|window\.__ENV|import\.meta\.env|window\.open\(/);
});

test("every wallet summary has independent loading error unavailable and retry states", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const overviewStart = app.indexOf("<section v-if=\"path === '/console' || path === '/console/overview'\" class=\"overview-layout\"");
  const overviewEnd = app.indexOf("<section v-else-if=\"path.startsWith('/console/workspace')\"", overviewStart);
  const apiStart = app.indexOf("<div v-if=\"activeApiPage === 'overview'\" class=\"api-overview\"");
  const apiEnd = app.indexOf("<section v-else-if=\"activeApiPage === 'usage'\"", apiStart);
  const billingStart = app.indexOf("<section v-else class=\"billing-page\"");
  const billingEnd = app.indexOf("</template>", billingStart);

  for (const [name, view] of [
    ["Overview", app.slice(overviewStart, overviewEnd)],
    ["API overview", app.slice(apiStart, apiEnd)],
    ["Billing", app.slice(billingStart, billingEnd)]
  ]) {
    assert.match(view, /loading\.wallet[\s\S]+正在读取余额/, `${name} wallet loading`);
    assert.match(view, /errors\.wallet[\s\S]+@click="loadWallet"/, `${name} wallet error retry`);
    assert.match(view, /walletSource\?\.status === 'unavailable'[\s\S]+@click="loadWallet"/, `${name} wallet unavailable retry`);
  }
});

test("API overview and Billing expose every balance history state", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const apiStart = app.indexOf("<div v-if=\"activeApiPage === 'overview'\" class=\"api-overview\"");
  const apiEnd = app.indexOf("<section v-else-if=\"activeApiPage === 'usage'\"", apiStart);
  const billingStart = app.indexOf("<section v-else class=\"billing-page\"");
  const billingEnd = app.indexOf("</template>", billingStart);

  for (const [name, view] of [
    ["API overview", app.slice(apiStart, apiEnd)],
    ["Billing", app.slice(billingStart, billingEnd)]
  ]) {
    assert.match(view, /<h2>余额记录<\/h2>/, `${name} balance history`);
    assert.match(view, /loading\.history[\s\S]+正在读取余额记录/, `${name} history loading`);
    assert.match(view, /errors\.history[\s\S]+@click="loadHistory"/, `${name} history error retry`);
    assert.match(view, /balanceHistorySource\?\.status === 'unavailable'[\s\S]+@click="loadHistory"/, `${name} history unavailable retry`);
    assert.match(view, /balanceHistorySource\?\.status === 'empty'[\s\S]+暂无余额记录/, `${name} history empty`);
    assert.match(view, /v-else class="table-wrap"[\s\S]+v-for="item in history"/, `${name} history table`);
  }
});

test("Billing receipt rows open a customer-safe detail view", async () => {
  assert.equal(typeof readApi.getBillingReceipt, "function");
  const app = await source("apps/console-ui/src/App.vue");
  const billingStart = app.indexOf("<section v-else class=\"billing-page\"");
  const billingEnd = app.indexOf("</template>", billingStart);
  const billing = app.slice(billingStart, billingEnd);
  const detailStart = billing.indexOf("<section v-if=\"selectedReceiptId\" class=\"panel receipt-detail\"");
  const detailEnd = billing.indexOf("</section>", detailStart) + "</section>".length;
  const detail = billing.slice(detailStart, detailEnd);

  assert.match(billing, /@click="loadReceiptDetail\(receipt\.receiptId\)"/);
  assert.match(detail, /<h2>交易详情<\/h2>/);
  assert.match(detail, /loading\.receiptDetail[\s\S]+正在读取交易详情/);
  assert.match(detail, /errors\.receiptDetail[\s\S]+loadReceiptDetail\(selectedReceiptId\)/);
  assert.match(detail, /receiptDetailSource\?\.status === 'unavailable'[\s\S]+loadReceiptDetail\(selectedReceiptId\)/);
  assert.match(detail, /@click="clearReceiptDetail"/);
  assert.match(detail, /receiptLabel\(receiptDetail\.type\)/);
  for (const field of ["status", "createdAt", "workspaceId", "priceVersion", "periodStart", "paidThrough"]) {
    assert.match(detail, new RegExp(`receiptDetail\\.${field}`));
  }
  assert.match(detail, /receiptDetail\.(?:refundUsdMicros|chargeUsdMicros|totalUsdMicros)/);
  assert.doesNotMatch(detail, /\{\{\s*receiptDetail\.type\s*\}\}/);
  assert.doesNotMatch(detail, /chargeReference|components|fulfillment|resourceType|resourceId|sourceUpdatedAt|fetchedAt|source-note/);
});

test("Customer Console announcements keep actionable states without exposing envelope metadata", async () => {
  assert.equal(typeof readApi.getAnnouncements, "function");
  assert.equal(typeof readApi.markAnnouncementRead, "function");

  const app = await source("apps/console-ui/src/App.vue");
  const announcementsStart = app.indexOf("<section v-else-if=\"path.startsWith('/console/announcements')\"");
  const announcementsEnd = app.indexOf("<section v-else class=\"billing-page\">", announcementsStart);
  const announcementsView = app.slice(announcementsStart, announcementsEnd);
  assert.match(app, /getAnnouncements\(/);
  assert.match(app, /markAnnouncementRead\([^,]+,[^,]+,[^)]+\)/);
  for (const field of ["source", "status", "available", "fetchedAt", "sourceUpdatedAt"]) {
    assert.doesNotMatch(announcementsView, new RegExp(`announcementsSource\\?\\.${field}`));
  }
  assert.doesNotMatch(announcementsView, /source-note/);
  assert.match(announcementsView, /loading\.announcements[\s\S]+正在读取公告/);
  assert.match(announcementsView, /errors\.announcements[\s\S]+@click="loadAnnouncements"/);
  assert.match(announcementsView, /announcementsUnavailable[\s\S]+@click="loadAnnouncements"/);
  assert.match(announcementsView, /announcementsEmpty[\s\S]+暂无公告/);
  assert.match(announcementsView, /v-else class="announcement-list"[\s\S]+announcement\.title[\s\S]+announcement\.body/);
  assert.match(announcementsView, /formatDate\(announcement\.publishedAt \|\| announcement\.startsAt, true\)/);
  assert.match(announcementsView, /announcement\.read[\s\S]+@click="readAnnouncement\(announcement\.id\)"/);
});

test("authenticated Overview shows actionable announcements in every source state", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const overviewStart = app.indexOf("<section v-if=\"path === '/console' || path === '/console/overview'\" class=\"overview-layout\"");
  const overviewEnd = app.indexOf("<section v-else-if=\"path.startsWith('/console/workspace')\"", overviewStart);
  const overview = app.slice(overviewStart, overviewEnd);

  assert.notEqual(overviewStart, -1);
  assert.notEqual(overviewEnd, -1);
  assert.match(overview, /class="panel overview-announcements"[\s\S]+<h2>公告<\/h2>/);
  assert.match(overview, /loading\.announcements[\s\S]+正在读取公告/);
  assert.match(overview, /errors\.announcements[\s\S]+@click="loadAnnouncements"/);
  assert.match(overview, /announcementsUnavailable[\s\S]+@click="loadAnnouncements"/);
  assert.match(overview, /announcementsEmpty[\s\S]+暂无公告/);
  assert.match(overview, /v-else class="announcement-list"[\s\S]+announcement\.title[\s\S]+announcement\.body/);
  assert.match(overview, /formatDate\(announcement\.publishedAt \|\| announcement\.startsAt, true\)/);
  assert.match(overview, /announcement\.read[\s\S]+@click="readAnnouncement\(announcement\.id\)"/);
  assert.doesNotMatch(overview, /announcementsSource\?\.(?:source|status|available|fetchedAt|sourceUpdatedAt)/);
});

test("Customer Console secrets stay in component memory and expire", async () => {
  const [app, authApi, readApiSource, workspaceApiSource] = await Promise.all([
    source("apps/console-ui/src/App.vue"),
    source("apps/console-ui/src/api/auth-api.ts"),
    source("apps/console-ui/src/api/console-read-api.ts"),
    source("apps/console-ui/src/api/workspaces-api.ts")
  ]);
  const browserCode = [app, authApi, readApiSource, workspaceApiSource].join("\n");

  assert.doesNotMatch(browserCode, /localStorage|sessionStorage|indexedDB|IndexedDB/);
  assert.match(app, /const secretLifetimeMs = 60_000/);
  assert.match(app, /function armSecretTimeout\(\)/);
  assert.match(app, /window\.setTimeout\(clearSecrets, secretLifetimeMs\)/);
  assert.match(app, /function clearSecrets\(\)[\s\S]*window\.clearTimeout\(secretTimer\)/);
  assert.match(app, /onBeforeUnmount\(\(\) => \{\s*clearSecrets\(\)/);
});

test("Customer Console does not render the paused Runtime file facts", async () => {
  assert.equal("getWorkspaceFiles" in workspaceApi, false);
  assert.equal("getWorkspaceFilesystemUsage" in workspaceApi, false);

  const [app, workspaceApiSource, dtos] = await Promise.all([
    source("apps/console-ui/src/App.vue"),
    source("apps/console-ui/src/api/workspaces-api.ts"),
    source("apps/console-ui/src/api/dtos.ts")
  ]);
  assert.doesNotMatch(app, /文件与目录|实际空间用量|filesSource|filesystemSource|WorkspaceFilePageDTO|WorkspaceFilesystemUsageDTO/);
  assert.doesNotMatch(app, /getWorkspaceFiles|getWorkspaceFilesystemUsage|loadWorkspaceFiles|loadWorkspaceFilesystemUsage|loading\.files|loading\.filesystem|errors\.files|errors\.filesystem/);
  assert.doesNotMatch(workspaceApiSource, /\/files\?|\/filesystem-usage/);
  assert.match(dtos, /export interface WorkspaceFilePageDTO/);
  assert.match(dtos, /export interface WorkspaceFilesystemUsageDTO/);
});

test("Customer Console source blocks fail independently and remain retryable", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  for (const [key, sourceName, retry] of [
    ["runtime", "workspaceStatusSource", "loadWorkspaceStatus"],
    ["keys", "keySource", "loadKeys"],
    ["usage", "usageSource", "loadUsage"],
    ["accountStats", "accountUsageSource", "loadAccountUsage"],
    ["receipts", "receiptsSource", "loadReceipts"],
    ["announcements", "announcementsSource", "loadAnnouncements"]
  ]) {
    assert.match(app, new RegExp(`loading\\.${key}`));
    assert.match(app, new RegExp(`errors\\.${key}`));
    if (key === "announcements") assert.match(app, /announcementsUnavailable/);
    else assert.match(app, new RegExp(`${sourceName}\\?\\.status === 'unavailable'`));
    assert.match(app, new RegExp(`@click="${retry}"`));
  }
});

test("Customer Console auto renewal stays disabled with an owner reason", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const workspaceView = app.slice(app.indexOf("path.startsWith('/console/workspace')"), app.indexOf("<section v-else-if=\"apiRoute\""));

  assert.match(workspaceView, /自动续费/);
  assert.match(workspaceView, /disabled[^>]*aria-describedby="auto-renew-reason"/);
  assert.match(workspaceView, /id="auto-renew-reason"[^>]*>真实续费验证完成前不可启用/);
  assert.doesNotMatch(app, /updateWorkspaceRenewal\([^)]*autoRenew:\s*true/);
});
