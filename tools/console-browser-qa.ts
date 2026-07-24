import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const NOW = "2026-07-19T12:00:00Z";
const WORKSPACE_PASSWORDS = Object.freeze({
  "ws-1": "fixture-workspace-password",
  "ws-2": "fixture-second-workspace-password"
});
const WORKSPACE_KEYS = Object.freeze({
  "9": "sk-fixture-workspace-key",
  "19": "sk-fixture-second-workspace-key"
});
const GENERAL_KEY = "sk-fixture-general-key";
const VIEWPORTS = Object.freeze({
  desktop: Object.freeze({ width: 1440, height: 900 }),
  mobile: Object.freeze({ width: 390, height: 844 })
});

function source(data, name = "control-plane", status = "available") {
  return { source: name, status, available: true, fetchedAt: NOW, data };
}

function unavailable(name) {
  return { source: name, status: "unavailable", available: false, fetchedAt: NOW };
}

function gatewayKey(id = "11", name = "General fixture key", input = {}) {
  return {
    id, name, kind: Object.hasOwn(WORKSPACE_KEYS, id) ? "workspace" : "general", status: "active",
    groupId: input.groupId || "101", ipWhitelist: input.ipWhitelist || [], ipBlacklist: input.ipBlacklist || [],
    quotaUsdMicros: input.quotaUsdMicros ?? 10_000_000, quotaUsedUsdMicros: 250_000,
    rateLimit5hUsdMicros: input.rateLimit5hUsdMicros || 0, rateLimit1dUsdMicros: input.rateLimit1dUsdMicros || 0,
    rateLimit7dUsdMicros: input.rateLimit7dUsdMicros || 0,
    usage5hUsdMicros: 0, usage1dUsdMicros: 10_000, usage7dUsdMicros: 25_000, currentConcurrency: 0,
    expiresAt: "2026-08-18T12:00:00Z", lastUsedAt: NOW, lastUsedIp: "127.0.0.1", createdAt: NOW, updatedAt: NOW,
    manageable: !Object.hasOwn(WORKSPACE_KEYS, id), deletable: !Object.hasOwn(WORKSPACE_KEYS, id)
  };
}

function workspace(id = "ws-1") {
  if (id === "ws-2") {
    return {
      id, ownerAccountId: "acct-1", ownerUserId: "user-customer", state: "running",
      createdAt: "2026-07-15T00:00:00Z", updatedAt: NOW, name: "Second Workspace",
      url: "https://workspace.example.invalid/w/ws-2/", packageId: "pro", storageGb: 100,
      autoRenew: false, priceVersion: "pilot-usd-2026-07-v1", currency: "USD", totalUsdMicros: 240_080_000,
      periodStart: "2026-07-15T00:00:00Z", paidThrough: "2026-08-15T00:00:00Z",
      renewalStatus: "manual", workspaceApiKeyId: "19"
    };
  }
  return {
    id: "ws-1", ownerAccountId: "acct-1", ownerUserId: "user-customer", state: "running",
    createdAt: "2026-07-01T00:00:00Z", updatedAt: NOW, name: "Pilot Workspace",
    url: "https://workspace.example.invalid/w/ws-1/", packageId: "basic", storageGb: 10,
    autoRenew: false, priceVersion: "pilot-usd-2026-07-v1", currency: "USD", totalUsdMicros: 52_580_000,
    periodStart: "2026-07-01T00:00:00Z", paidThrough: "2026-08-01T00:00:00Z",
    renewalStatus: "manual", workspaceApiKeyId: "9"
  };
}

function operatorWorkspace() {
  const ownerAccount = source({ id: "acct-1" });
  const ownerUser = source({ id: "user-customer", email: "pilot@example.com" });
  const workspaceSource = source(workspace());
  const resource = {
    ownerAccount, ownerUser, workspace: source({ id: "ws-1", name: "Pilot Workspace" }),
    resourceType: source("compute", "fabric"), packageOrSpec: source("SA5.MEDIUM4", "fabric"),
    providerId: source("ins-fixture", "fabric"), zone: source("ap-guangzhou-6", "fabric"),
    status: source("RUNNING", "fabric"), createdAt: source("2026-07-01T00:00:00Z", "fabric"),
    expiresAt: source("2026-08-01T00:00:00Z", "fabric"), lastReadAt: source(NOW, "fabric"),
    operationRef: source("workspace-launch:fixture"), receiptRef: source("receipt-fixture", "ledger")
  };
  return {
    workspace: workspaceSource, ownerAccount, ownerUser, resources: [resource],
    receipt: source({ receiptId: "receipt-fixture" }, "ledger"),
    workspaceKeyUsage: source({ keyId: "9", todayActualCostUsdMicros: 10_000, totalActualCostUsdMicros: 25_000 }, "sub2api")
  };
}

function sourceForState(state, data, name) {
  if (state.sourceState === "error") return null;
  if (state.sourceState === "unavailable") return unavailable(name);
  if (state.sourceState === "empty") return source(data, name, "empty");
  return source(data, name);
}

async function defaultServerFactory() {
  const { createServer } = await import("vite");
  const server = await createServer({
    root: ROOT,
    configFile: resolve(ROOT, "vite.config.ts"),
    logLevel: "silent",
    server: { host: "127.0.0.1", port: 0, strictPort: true }
  });
  await server.listen();
  const address = server.httpServer?.address();
  if (!address || typeof address === "string") throw new Error("console_browser_server_address_missing");
  return { origin: `http://127.0.0.1:${address.port}`, close: () => server.close() };
}

async function defaultBrowserFactory() {
  const { chromium } = await import("playwright");
  return chromium.launch({ headless: true });
}

async function fulfillJson(route, payload, status = 200, headers = {}) {
  await route.fulfill({
    status,
    contentType: "application/json",
    headers,
    body: JSON.stringify(payload)
  });
}

async function apiFixture(route, state) {
  const request = route.request();
  const url = new URL(request.url());
  const path = url.pathname;
  const method = request.method();
  const emptyPage = { items: [], total: 0, page: 1, pageSize: 20 };

  if (path === "/api/auth/me") {
    const operator = state.role === "operator";
    return fulfillJson(route, source({
      consoleUserId: operator ? "user-operator" : "user-customer",
      accountId: operator ? "acct-operator" : "acct-1",
      role: operator ? "admin" : "owner",
      sub2apiUserId: operator ? "10" : "9",
      email: operator ? "operator@example.com" : "pilot@example.com",
      status: "active"
    }, "control-plane"), 200, { "x-opl-csrf-token": "csrf-fixture" });
  }

  if (path === "/api/workspaces" && method === "GET") return fulfillJson(route, source({ items: [workspace(), workspace("ws-2")], total: 2 }));
  if (path === "/api/workspace-launches" && method === "GET") return fulfillJson(route, []);
  const runtimeMatch = path.match(/^\/api\/workspaces\/(ws-[12])\/runtime-status$/);
  if (runtimeMatch) {
    const workspaceId = runtimeMatch[1];
    state.runtimeReads.set(workspaceId, (state.runtimeReads.get(workspaceId) || 0) + 1);
    return fulfillJson(route, source({
    workspaceId, status: "running", ready: true, runtimeId: `runtime-${workspaceId}`,
    url: workspace(workspaceId).url, serviceName: `runtime-${workspaceId}`, checks: [{ name: "ready_pod_uses_retained_pvc", ok: true }],
    access: { username: "opl", credentialStatus: "configured", credentialVersion: "1" }
    }, "fabric"));
  }
  const credentialMatch = path.match(/^\/api\/workspaces\/(ws-[12])\/runtime-credentials\/reveal$/);
  if (credentialMatch && method === "POST") {
    const workspaceId = credentialMatch[1];
    state.workspaceSecretReads.set(workspaceId, (state.workspaceSecretReads.get(workspaceId) || 0) + 1);
    return fulfillJson(route, {
      workspaceId,
      access: { account: "acct-1", username: "opl", password: WORKSPACE_PASSWORDS[workspaceId], credentialStatus: "configured", credentialVersion: "1" }
    });
  }
  if (path === "/api/pricing/catalog") return fulfillJson(route, {
    priceVersion: "pilot-usd-2026-07-v1", billingUnit: "month", displayCurrency: "USD", walletCurrency: "USD", currency: "USD",
    packages: [
      { id: "basic", name: "Basic", available: true, cpu: 2, memoryGb: 4, diskGb: 10, server: "2c4g", price: { priceVersion: "pilot-usd-2026-07-v1", currency: "USD", chargeUsdMicros: 52_580_000 } },
      { id: "pro", name: "Pro", available: true, cpu: 8, memoryGb: 16, diskGb: 100, server: "8c16g", price: { priceVersion: "pilot-usd-2026-07-v1", currency: "USD", chargeUsdMicros: 240_080_000 } }
    ]
  });
  if (path === "/api/pricing/preview" && method === "POST") return fulfillJson(route, {
    resourceType: "workspace", packageId: "basic", priceVersion: "pilot-usd-2026-07-v1", currency: "USD",
    displayCurrency: "USD", billingUnit: "month", totalChargeUsdMicros: 52_580_000
  });
  if (path === "/api/billing/receipts") return fulfillJson(route, source({ receipts: [], nextCursor: "", hasMore: false }, "ledger", "empty"));
  if (path === "/api/announcements") return fulfillJson(route, source(emptyPage, "control-plane", "empty"));
  if (path === "/api/gateway/wallet") return fulfillJson(route, source({ userId: "9", currency: "USD", usdMicros: 50_000_000, status: "active" }, "sub2api"));
  if (path === "/api/gateway/usage-summary") return fulfillJson(route, source({ totalRequests: 1, totalInputTokens: 10, totalOutputTokens: 2, totalTokens: 12, totalActualCostUsdMicros: 25_000 }, "sub2api"));
  if (path === "/api/gateway/balance-history") {
    const page = Number(url.searchParams.get("page"));
    const pageSize = Number(url.searchParams.get("pageSize"));
    return fulfillJson(route, source({ items: [], total: 0, page, pageSize, pages: 1 }, "sub2api", "empty"));
  }
  if (path === "/api/gateway/endpoint") return fulfillJson(route, source({ baseUrl: "https://gflabtoken.cn/v1" }, "sub2api"));
  if (path === "/api/gateway/groups") return fulfillJson(route, source({
    items: [
      { id: "101", name: "default", description: "", platform: "openai", rateMultiplier: 1, subscriptionType: "standard", status: "active" },
      { id: "202", name: "priority", description: "", platform: "anthropic", rateMultiplier: 1, subscriptionType: "standard", status: "active" }
    ],
    total: 2
  }, "sub2api"));

  if (path === "/api/gateway/keys" && method === "GET") {
    if (state.sourceState === "error") return fulfillJson(route, { error: "upstream_unavailable" }, 503);
    const keys = state.sourceState === "available" ? state.keys : [];
    if (state.sourceState === "available" && keys.length === 0) state.emptyGatewayReadbacks += 1;
    const data = { items: keys, total: keys.length, page: 1, pageSize: 20, pages: keys.length ? 1 : 0 };
    return fulfillJson(route, state.sourceState === "available" && keys.length === 0
      ? source(data, "sub2api", "empty")
      : sourceForState(state, data, "sub2api"));
  }
  if (path === "/api/gateway/keys" && method === "POST") {
    const operation = request.headers()["idempotency-key"] || "";
    if (!operation) return fulfillJson(route, { error: "idempotency_key_required" }, 400);
    const input = request.postDataJSON();
    state.gatewayWrites.add(operation);
    if (!state.keys.some((item) => item.id === "12")) state.keys.push(gatewayKey("12", input.name, input));
    if (!state.lostGatewayResponses.has(operation)) {
      state.lostGatewayResponses.add(operation);
      return route.abort("failed");
    }
    return fulfillJson(route, source(state.keys.find((item) => item.id === "12"), "sub2api"));
  }
  const keyMatch = path.match(/^\/api\/gateway\/keys\/(\d+)$/);
  if (keyMatch && method === "GET") {
    const key = state.keys.find((item) => item.id === keyMatch[1]);
    return key ? fulfillJson(route, source(key, "sub2api")) : fulfillJson(route, { error: "gateway_key_not_found" }, 404);
  }
  if (keyMatch && method === "PATCH") {
    const operation = request.headers()["idempotency-key"] || "";
    const key = state.keys.find((item) => item.id === keyMatch[1]);
    if (!operation) return fulfillJson(route, { error: "idempotency_key_required" }, 400);
    if (!key) return fulfillJson(route, { error: "gateway_key_not_found" }, 404);
    const input = request.postDataJSON();
    for (const field of ["name", "groupId", "ipWhitelist", "ipBlacklist", "quotaUsdMicros", "rateLimit5hUsdMicros", "rateLimit1dUsdMicros", "rateLimit7dUsdMicros", "expiresAt"]) {
      if (input[field] !== undefined) key[field] = input[field] || (field === "expiresAt" ? null : input[field]);
    }
    if (input.enabled !== undefined) key.status = input.enabled ? "active" : "disabled";
    if (input.resetQuota) key.quotaUsedUsdMicros = 0;
    if (input.resetRateLimitUsage) key.usage5hUsdMicros = key.usage1dUsdMicros = key.usage7dUsdMicros = 0;
    key.updatedAt = NOW;
    state.gatewayMutationWrites.add(operation);
    state.gatewayActions.push(input.resetQuota ? "quota-reset" : input.resetRateLimitUsage ? "rate-reset" : input.enabled === false ? "disable" : input.enabled === true ? "enable" : input.groupId && !input.name ? "group" : "edit");
    return fulfillJson(route, source(key, "sub2api"));
  }
  if (keyMatch && method === "DELETE") {
    const operation = request.headers()["idempotency-key"] || "";
    if (!operation) return fulfillJson(route, { error: "idempotency_key_required" }, 400);
    const index = state.keys.findIndex((item) => item.id === keyMatch[1]);
    if (index < 0) return fulfillJson(route, { error: "gateway_key_not_found" }, 404);
    state.keys.splice(index, 1);
    state.gatewayMutationWrites.add(operation);
    state.gatewayActions.push("delete");
    return fulfillJson(route, source({ status: "deleted" }, "sub2api"));
  }
  const revealMatch = path.match(/^\/api\/gateway\/keys\/(\d+)\/reveal$/);
  if (revealMatch && method === "POST") {
    const key = Object.hasOwn(WORKSPACE_KEYS, revealMatch[1])
      ? gatewayKey(revealMatch[1], "Workspace Key")
      : state.keys.find((item) => item.id === revealMatch[1]);
    if (!key) return fulfillJson(route, { error: "gateway_key_not_found" }, 404);
    state.revealCalls.set(key.id, (state.revealCalls.get(key.id) || 0) + 1);
    return fulfillJson(route, source({
      id: key.id, name: key.name, status: key.status, value: WORKSPACE_KEYS[key.id] || GENERAL_KEY
    }, "sub2api"), 200, { "cache-control": "private, no-store" });
  }

  if (path === "/api/operator/overview") {
    const ready = source({ ready: true }, "control-plane");
    return fulfillJson(route, source({
      accounts: source({ total: 1, active: 1, disabled: 0 }), wallet: source({ currency: "USD", usdMicros: 50_000_000 }, "sub2api"),
      keys: source({ total: 2 }, "sub2api"), usage: source({ todayActualCostUsdMicros: 10_000, totalActualCostUsdMicros: 25_000 }, "sub2api"),
      workspaces: source({ total: 1 }), resources: source({ total: 1 }, "fabric"), reconciliation: source({ total: 0 }),
      health: source({ controlPlane: ready, gateway: ready, fabric: ready, runtime: ready, ledger: ready })
    }));
  }
  if (path === "/api/operator/accounts") return fulfillJson(route, source({
    items: [{
      accountId: "acct-1", consoleUserId: "user-customer", role: "owner", sub2apiUserId: "9", email: "pilot@example.com", status: "active",
      gatewayIdentity: source({ userId: "9", email: "pilot@example.com", status: "active" }, "sub2api"),
      wallet: source({ userId: "9", currency: "USD", usdMicros: 50_000_000, status: "active" }, "sub2api"),
      keyCount: source(2, "sub2api"), usage: source({ todayActualCostUsdMicros: 10_000, totalActualCostUsdMicros: 25_000 }, "sub2api"), workspaceCount: source(1)
    }], total: 1, page: 1, pageSize: 20
  }));
  if (path === "/api/operator/workspaces") {
    if (state.sourceState === "error") return fulfillJson(route, { error: "upstream_unavailable" }, 503);
    const items = state.sourceState === "available" ? [operatorWorkspace()] : [];
    return fulfillJson(route, sourceForState(state, { items, total: items.length, page: 1, pageSize: 20 }, "control-plane+fabric+sub2api"));
  }
  if (path === "/api/operator/reconciliation") return fulfillJson(route, source(emptyPage, "control-plane", "empty"));
  if (path === "/api/operator/announcements") return fulfillJson(route, source(emptyPage, "control-plane", "empty"));
  if (path === "/api/operator/health") {
    const ready = source({ ready: true }, "control-plane");
    return fulfillJson(route, source({ controlPlane: ready, gateway: ready, fabric: ready, runtime: ready, ledger: ready }));
  }
  if (/^\/api\/operator\/accounts\/acct-1\/wallet-adjustments$/.test(path) && method === "POST") {
    const operation = request.headers()["idempotency-key"] || "";
    if (!operation) return fulfillJson(route, { error: "idempotency_key_required" }, 400);
    state.walletWrites.add(operation);
    if (!state.lostWalletResponses.has(operation)) {
      state.lostWalletResponses.add(operation);
      return route.abort("failed");
    }
    return fulfillJson(route, {
      operationId: "wallet-adjustment-fixture", accountId: "acct-1", status: "succeeded", kind: "recharge",
      amountUsd: "5", reason: "browser retry", beforeBalance: source({ currency: "USD", usdMicros: 50_000_000 }, "sub2api"),
      afterBalance: source({ currency: "USD", usdMicros: 55_000_000 }, "sub2api"), balanceHistoryRef: "balance-history-fixture", actor: "user-operator"
    });
  }

  state.unexpectedApi.push(`${method} ${path}`);
  return fulfillJson(route, { error: "unexpected_browser_fixture_request" }, 500);
}

async function waitForText(page, text) {
  const locator = page.getByText(text, { exact: false }).first();
  try {
    await locator.waitFor({ state: "visible", timeout: 15_000 });
  } catch (error) {
    const diagnostic = await locator.evaluate((element) => {
      const ancestors = [];
      for (let current = element; current && ancestors.length < 12; current = current.parentElement) {
        const style = getComputedStyle(current);
        ancestors.push({ tag: current.tagName, className: current.className, display: style.display, visibility: style.visibility, opacity: style.opacity, width: current.clientWidth, height: current.clientHeight });
      }
      return {
        viewport: { innerWidth, innerHeight, bodyWidth: document.body.clientWidth, rootWidth: document.documentElement.clientWidth },
        ancestors, body: document.body.innerText.slice(0, 1000), path: location.pathname
      };
    }).catch(() => ({ missing: true }));
    throw new Error(`console_browser_text_hidden:${text}:${JSON.stringify(diagnostic)}`, { cause: error });
  }
}

async function assertNoViewportOverflow(page) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 1) throw new Error(`console_browser_viewport_overflow:${overflow}`);
}

async function exerciseGatewayKeyLifecycle(page, state) {
  await page.getByRole("button", { name: "创建 Key" }).click();
  const dialog = page.getByRole("dialog", { name: "创建 API Key" });
  await dialog.getByLabel("名称").fill("Browser retry key");
  const submit = dialog.getByRole("button", { name: "创建", exact: true });
  await submit.click();
  await page.waitForFunction(() => [...document.querySelectorAll("button")].some((button) => button.textContent?.trim() === "创建" && !button.disabled));
  await submit.click();
  await waitForText(page, "API Key 已创建");
  await waitForText(page, GENERAL_KEY);

  const secretRow = page.locator("tr.secret-row");
  await secretRow.getByRole("button", { name: "复制", exact: true }).click();
  if (await page.evaluate(() => navigator.clipboard.readText()) !== GENERAL_KEY) throw new Error("console_browser_created_key_copy_failed");

  let keyRow = page.getByRole("row").filter({ hasText: "Browser retry key" }).first();
  await keyRow.getByRole("button", { name: "使用说明", exact: true }).click();
  const useDialog = page.getByRole("dialog", { name: "使用说明" });
  await waitForText(useDialog, "openai");
  await waitForText(useDialog, "https://gflabtoken.cn/v1");
  await waitForText(useDialog, GENERAL_KEY);
  await useDialog.getByRole("button", { name: "复制配置", exact: true }).click();
  const copiedConfiguration = await page.evaluate(() => navigator.clipboard.readText());
  for (const value of ["https://gflabtoken.cn/v1", GENERAL_KEY, "openai"]) {
    if (!copiedConfiguration.includes(value)) throw new Error(`console_browser_key_configuration_missing:${value}`);
  }
  await useDialog.getByRole("button", { name: "关闭", exact: true }).last().click();

  await keyRow.getByRole("button", { name: "编辑", exact: true }).click();
  const editDialog = page.getByRole("dialog", { name: "编辑 API Key" });
  await editDialog.getByLabel("名称").fill("Browser edited key");
  await editDialog.getByRole("button", { name: "保存", exact: true }).click();
  await waitForText(page, "API Key 已更新");

  keyRow = page.getByRole("row").filter({ hasText: "Browser edited key" }).first();
  await keyRow.getByLabel("快捷换组").selectOption("202");
  await waitForText(page, "分组已更新");
  keyRow = page.getByRole("row").filter({ hasText: "Browser edited key" }).first();
  await keyRow.getByRole("button", { name: "停用", exact: true }).click();
  await waitForText(page, "API Key 已停用");
  await keyRow.getByRole("button", { name: "启用", exact: true }).click();
  await waitForText(page, "API Key 已启用");
  await keyRow.getByRole("button", { name: "重置配额", exact: true }).click();
  await waitForText(page, "配额用量已重置");
  await keyRow.getByRole("button", { name: "重置限速", exact: true }).click();
  await waitForText(page, "限速用量已重置");
  await keyRow.getByRole("button", { name: "删除", exact: true }).click();
  const deleteDialog = page.getByRole("dialog", { name: "删除 API Key" });
  await deleteDialog.getByRole("button", { name: "删除", exact: true }).click();
  await waitForText(page, "API Key 已删除");
  await waitForText(page, "暂无数据");

  if (state.keys.length !== 0 || state.emptyGatewayReadbacks < 1) throw new Error("console_browser_gateway_empty_readback_failed");
}

async function retryWalletAdjustment(page) {
  await page.getByRole("button", { name: "调整钱包" }).last().click();
  const dialog = page.getByRole("dialog", { name: "wallet-adjustment" });
  await dialog.getByLabel("金额（USD）").fill("5");
  await dialog.getByLabel("原因").fill("browser retry");
  const submit = dialog.getByRole("button", { name: "确认调整" });
  await submit.click();
  await waitForText(page, "结果待确认");
  await submit.click();
  await waitForText(page, "钱包调整已提交");
}

export async function runConsoleBrowserQa({
  network,
  serverFactory = defaultServerFactory,
  browserFactory = defaultBrowserFactory
} = {}) {
  if (network !== "fake-only") throw new Error("console_browser_fake_only_required");

  const server = await serverFactory();
  let browser;
  const state = {
    role: "customer", sourceState: "available", keys: [],
    gatewayWrites: new Set(), walletWrites: new Set(), lostGatewayResponses: new Set(), lostWalletResponses: new Set(),
    gatewayMutationWrites: new Set(), gatewayActions: [], revealCalls: new Map(), emptyGatewayReadbacks: 0,
    runtimeReads: new Map(), workspaceSecretReads: new Map(),
    unexpectedApi: [], externalRequests: 0, pageErrors: []
  };
  try {
    browser = await browserFactory();
    for (const [name, viewport] of Object.entries(VIEWPORTS)) {
      const context = await browser.newContext({ viewport, permissions: ["clipboard-read", "clipboard-write"] });
      const page = await context.newPage();
      page.on("pageerror", (error) => state.pageErrors.push(error.message));
      page.on("dialog", (dialog) => { void dialog.accept(); });
      await page.route("**/*", async (route) => {
        const url = new URL(route.request().url());
        const local = url.hostname === "127.0.0.1" && url.port === new URL(server.origin).port;
        if (!local) {
          state.externalRequests += 1;
          return route.abort("blockedbyclient");
        }
        if (url.pathname.startsWith("/api/")) return apiFixture(route, state);
        return route.continue();
      });

      await page.goto(`${server.origin}/`, { waitUntil: "networkidle" });
      await waitForText(page, "面向已开通用户的 Workspace 与 API 服务。");
      const logoLoaded = await page.getByAltText("OPL Cloud").evaluate((image) => image.complete && image.naturalWidth > 0);
      if (!logoLoaded) throw new Error("console_browser_logo_missing");
      await page.goto(`${server.origin}/login`, { waitUntil: "networkidle" });
      await waitForText(page, "Console 登录");

      state.role = "customer";
      state.sourceState = "available";
      await page.goto(`${server.origin}/console/workspace?viewport=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "Workspace URL");
      await waitForText(page, "opl");
      const workspaceSelect = page.getByLabel("选择 Workspace");
      if (await workspaceSelect.locator("option").count() !== 2) throw new Error("console_browser_workspace_list_missing");
      if (await workspaceSelect.inputValue() !== "ws-1") throw new Error("console_browser_default_workspace_selection_failed");
      await waitForText(page, "https://workspace.example.invalid/w/ws-1/");
      if (name === "desktop") {
        const passwordRow = page.locator("dt", { hasText: "密码" }).locator("..");
        await passwordRow.getByRole("button", { name: "显示" }).click();
        await waitForText(page, WORKSPACE_PASSWORDS["ws-1"]);
        await passwordRow.getByRole("button", { name: "复制" }).click();
        const keyRow = page.locator("dt", { hasText: "Workspace Key" }).locator("..");
        await keyRow.getByRole("button", { name: "显示" }).click();
        await waitForText(page, WORKSPACE_KEYS["9"]);
        await keyRow.getByRole("button", { name: "复制" }).click();
        await workspaceSelect.selectOption("ws-2");
        if (await workspaceSelect.inputValue() !== "ws-2") throw new Error("console_browser_workspace_selection_failed");
        await waitForText(page, "https://workspace.example.invalid/w/ws-2/");
        await waitForText(page, "PRO");
        await waitForText(page, "2026/08/15");
        if (await page.getByText(WORKSPACE_PASSWORDS["ws-1"], { exact: true }).count() || await page.getByText(WORKSPACE_KEYS["9"], { exact: true }).count()) {
          throw new Error("console_browser_workspace_switch_secret_cleanup_failed");
        }
        await passwordRow.getByRole("button", { name: "显示" }).click();
        await waitForText(page, WORKSPACE_PASSWORDS["ws-2"]);
        await keyRow.getByRole("button", { name: "显示" }).click();
        await waitForText(page, WORKSPACE_KEYS["19"]);
        await page.getByRole("link", { name: "账单", exact: true }).click();
        if (await page.getByText(WORKSPACE_PASSWORDS["ws-2"], { exact: true }).count() || await page.getByText(WORKSPACE_KEYS["19"], { exact: true }).count()) {
          throw new Error("console_browser_secret_cleanup_failed");
        }
        state.sourceState = "available";
        await page.goto(`${server.origin}/console/api/keys?write=1`, { waitUntil: "networkidle" });
        await exerciseGatewayKeyLifecycle(page, state);
      }

      for (const sourceState of ["empty", "unavailable", "error"]) {
        state.sourceState = sourceState;
        await page.goto(`${server.origin}/console/api/keys?state=${sourceState}&viewport=${name}`, { waitUntil: "networkidle" });
        await waitForText(page, sourceState === "empty" ? "暂无数据" : sourceState === "unavailable" ? "暂不可用" : "服务暂不可用");
      }

      state.role = "operator";
      state.sourceState = "available";
      await page.goto(`${server.origin}/admin/resources?viewport=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "provider ID");
      await waitForText(page, "最近读回时间");
      if (name === "desktop") {
        await page.goto(`${server.origin}/admin/accounts?write=1`, { waitUntil: "networkidle" });
        await retryWalletAdjustment(page);
      }
      for (const sourceState of ["empty", "unavailable", "error"]) {
        state.sourceState = sourceState;
        await page.goto(`${server.origin}/admin/resources?state=${sourceState}&viewport=${name}`, { waitUntil: "networkidle" });
        await waitForText(page, sourceState === "empty" ? "暂无 Workspace" : sourceState === "unavailable" ? "Workspace 暂不可用" : "服务暂不可用");
      }
      await assertNoViewportOverflow(page);
      await context.close();
    }

    if (state.unexpectedApi.length) throw new Error(`console_browser_unexpected_api:${state.unexpectedApi.join(",")}`);
    if (state.pageErrors.length) throw new Error(`console_browser_page_error:${state.pageErrors.join(",")}`);
    if (state.gatewayWrites.size !== 1 || state.walletWrites.size !== 1) throw new Error("console_browser_idempotency_failed");
    const expectedGatewayActions = ["edit", "group", "disable", "enable", "quota-reset", "rate-reset", "delete"];
    if (state.gatewayMutationWrites.size !== expectedGatewayActions.length || JSON.stringify(state.gatewayActions) !== JSON.stringify(expectedGatewayActions)) {
      throw new Error(`console_browser_gateway_lifecycle_failed:${JSON.stringify(state.gatewayActions)}`);
    }
    if (state.revealCalls.get("12") !== 1) throw new Error(`console_browser_created_key_reveal_failed:${state.revealCalls.get("12") || 0}`);
    if (state.revealCalls.get("9") !== 1 || state.revealCalls.get("19") !== 1) throw new Error(`console_browser_workspace_key_scope_failed:${JSON.stringify(Object.fromEntries(state.revealCalls))}`);
    if (state.workspaceSecretReads.get("ws-1") !== 1 || state.workspaceSecretReads.get("ws-2") !== 1) throw new Error(`console_browser_workspace_secret_scope_failed:${JSON.stringify(Object.fromEntries(state.workspaceSecretReads))}`);
    if (state.externalRequests !== 0) throw new Error(`console_browser_external_request:${state.externalRequests}`);
    return {
      ok: true,
      evidenceLevel: "code-complete",
      network: "fake-only",
      viewports: Object.keys(VIEWPORTS),
      roles: ["customer", "operator"],
      sourceStates: ["available", "empty", "unavailable", "error"],
      repeatedWrites: { gatewayKey: state.gatewayWrites.size, walletAdjustment: state.walletWrites.size },
      workspaceSelection: state.runtimeReads.has("ws-1") && state.runtimeReads.has("ws-2"),
      workspaceSecretReads: Object.fromEntries(state.workspaceSecretReads),
      keyInteractions: state.gatewayActions,
      secretCleanup: true,
      externalRequests: state.externalRequests
    };
  } finally {
    if (browser) await browser.close();
    await server.close();
  }
}

function networkArg(argv) {
  if (argv.length !== 1 || !argv[0].startsWith("--network=")) return "";
  return argv[0].slice("--network=".length);
}

if (import.meta.url === pathToFileURL(process.argv[1] || "").href) {
  runConsoleBrowserQa({ network: networkArg(process.argv.slice(2)) })
    .then((result) => process.stdout.write(`${JSON.stringify(result, null, 2)}\n`))
    .catch((error) => {
      process.stderr.write(`${JSON.stringify({ ok: false, error: error.message }, null, 2)}\n`);
      process.exitCode = 1;
    });
}
