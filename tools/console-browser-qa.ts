import { mkdir } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
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
const OPERATOR_PAGE_READS = new Set([
  "/api/operator/overview",
  "/api/operator/accounts",
  "/api/operator/workspaces",
  "/api/operator/reconciliation",
  "/api/operator/health",
  "/api/operator/announcements"
]);
const VIEWPORTS = Object.freeze({
  desktop: Object.freeze({ width: 1440, height: 1024 }),
  mobile: Object.freeze({ width: 390, height: 844 })
});
const CUSTOMER_ROUTES = Object.freeze([
  "/login",
  "/console/overview",
  "/console/workspaces",
  "/console/workspaces/new",
  "/console/workspaces/ws-1",
  "/console/workspaces/ws-2",
  "/console/api",
  "/console/api/usage",
  "/console/api/keys",
  "/console/billing",
  "/console/announcements"
]);

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

function billingReceipt() {
  return {
    receiptId: "receipt-fixture", type: "billing.workspace_purchased.v1", status: "succeeded",
    workspaceId: "ws-1", createdAt: NOW, resourceType: "workspace", resourceId: "ws-1",
    priceVersion: "pilot-usd-2026-07-v1", currency: "USD", periodStart: "2026-07-01T00:00:00Z",
    paidThrough: "2026-08-01T00:00:00Z", totalUsdMicros: 52_580_000
  };
}

function pendingWorkspaceLaunch() {
  return {
    operationId: "launch-fixture-pending", status: "preparing", phase: "runtime_starting",
    accountId: "acct-1", name: "Fixture pending Workspace", packageId: "basic", sizeGb: 10,
    autoRenew: false, priceVersion: "pilot-usd-2026-07-v1", currency: "USD",
    totalChargeUsdMicros: 52_580_000, createdAt: NOW, updatedAt: NOW
  };
}

function operatorAccount(accountId, status) {
  const disabled = status === "disabled";
  const userId = disabled ? "11" : "9";
  const email = disabled ? "stopped@example.com" : "pilot@example.com";
  return {
    accountId, consoleUserId: disabled ? "user-stopped" : "user-customer", role: "owner", sub2apiUserId: userId, email, status,
    gatewayIdentity: source({ userId, email, status }, "sub2api"),
    wallet: source({ userId, currency: "USD", usdMicros: disabled ? "0" : "50000000", status: "active" }, "sub2api"),
    keyCount: source(disabled ? 0 : 2, "sub2api"),
    usage: source({ todayActualCostUsdMicros: disabled ? 0 : 10_000, totalActualCostUsdMicros: disabled ? 0 : 25_000 }, "sub2api"),
    workspaceCount: source(disabled ? 0 : 1)
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
  if (method === "GET" && OPERATOR_PAGE_READS.has(path)) state.operatorPageReads.push(path);
  const emptyPage = { items: [], total: 0, page: 1, pageSize: 20 };

  if (path === "/api/auth/login" && method === "POST") {
    const input = request.postDataJSON();
    if (input.email !== "fixture@example.com" || input.password !== "fixture-password") {
      return fulfillJson(route, { error: "invalid_credentials" }, 401);
    }
    state.loginSubmissions += 1;
    return fulfillJson(route, {
      user: {
        id: "user-customer", accountId: "acct-1", email: input.email,
        role: "owner", status: "active"
      },
      isOperator: false,
      csrfToken: "csrf-fixture"
    }, 200, { "x-opl-csrf-token": "csrf-fixture" });
  }

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

  if (path === "/api/workspaces" && method === "GET") {
    const page = Number(url.searchParams.get("page"));
    const pageSize = Number(url.searchParams.get("pageSize"));
    const allWorkspaces = [workspace(), workspace("ws-2")];
    const start = (page - 1) * pageSize;
    state.workspacePageReads.push({ page, pageSize });
    return fulfillJson(route, source({ items: allWorkspaces.slice(start, start + pageSize), total: allWorkspaces.length, page, pageSize }));
  }
  if (path === "/api/workspace-launches" && method === "GET") return fulfillJson(route, state.launches);
  if (path === "/api/workspace-launches/launch-fixture-pending" && method === "GET") return fulfillJson(route, pendingWorkspaceLaunch());
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
      { id: "basic", name: "Basic", available: state.basicPlanAvailable, cpu: 2, memoryGb: 4, diskGb: 10, server: "2c4g", price: { priceVersion: "pilot-usd-2026-07-v1", currency: "USD", chargeUsdMicros: 52_580_000 } },
      { id: "pro", name: "Pro", available: true, cpu: 8, memoryGb: 16, diskGb: 100, server: "8c16g", price: { priceVersion: "pilot-usd-2026-07-v1", currency: "USD", chargeUsdMicros: 240_080_000 } }
    ]
  });
  if (path === "/api/pricing/preview" && method === "POST") {
    const previewRequest = route.request().postDataJSON();
    const packageId = previewRequest?.packageId === "pro" ? "pro" : "basic";
    return fulfillJson(route, {
      resourceType: "workspace", packageId, priceVersion: "pilot-usd-2026-07-v1", currency: "USD",
      displayCurrency: "USD", billingUnit: "month", totalChargeUsdMicros: packageId === "pro" ? 240_080_000 : 52_580_000
    });
  }
  if (path === "/api/billing/receipts") return fulfillJson(route, source({ receipts: [billingReceipt()], nextCursor: "", hasMore: false }, "ledger"));
  if (path === "/api/billing/receipts/receipt-fixture") return fulfillJson(route, source(billingReceipt(), "ledger"));
  if (path === "/api/announcements") return fulfillJson(route, source(emptyPage, "control-plane", "empty"));
  if (path === "/api/gateway/wallet") return fulfillJson(route, source({ userId: "9", currency: "USD", usdMicros: "500000000", status: "active" }, "sub2api"));
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

  const keyUsageMatch = path.match(/^\/api\/gateway\/keys\/(\d+)\/usage$/);
  if (keyUsageMatch && method === "GET") {
    const page = Number(url.searchParams.get("page"));
    const pageSize = Number(url.searchParams.get("pageSize"));
    const item = {
      apiKeyId: keyUsageMatch[1], requestId: "request-fixture", createdAt: NOW,
      model: "gpt-5-mini", inboundEndpoint: "/v1/responses",
      actualCostUsdMicros: 25_000
    };
    return fulfillJson(route, source({ items: page === 1 ? [item] : [], total: 1, page, pageSize, pages: 1 }, "sub2api"));
  }
  const keyUsageSummaryMatch = path.match(/^\/api\/gateway\/keys\/(\d+)\/usage-summary$/);
  if (keyUsageSummaryMatch && method === "GET") {
    return fulfillJson(route, source({
      totalRequests: 1, totalInputTokens: 120, totalOutputTokens: 36, totalTokens: 188,
      totalActualCostUsdMicros: 25_000
    }, "sub2api"));
  }

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
      accounts: source({ total: 1, active: 1, disabled: 0 }), wallet: source({ currency: "USD", usdMicros: "50000000" }, "sub2api"),
      keys: source({ total: 2 }, "sub2api"), usage: source({ todayActualCostUsdMicros: 10_000, totalActualCostUsdMicros: 25_000 }, "sub2api"),
      workspaces: source({ total: 1 }), resources: source({ total: 1 }, "fabric"), reconciliation: source({ total: 0 }),
      health: source({ controlPlane: ready, gateway: ready, fabric: ready, runtime: ready, ledger: ready })
    }));
  }
  if (path === "/api/operator/accounts") return fulfillJson(route, source({
    items: state.operatorAccounts, total: state.operatorAccounts.length, page: 1, pageSize: 20
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
  const operatorDisableMatch = path.match(/^\/api\/operator\/accounts\/(acct-\d+)\/disable$/);
  if (operatorDisableMatch && method === "POST") {
    const accountId = operatorDisableMatch[1];
    const operation = request.headers()["idempotency-key"] || "";
    const input = request.postDataJSON();
    if (!operation) return fulfillJson(route, { error: "idempotency_key_required" }, 400);
    if (input.confirmationAccountId !== accountId || input.reason !== "operator_requested") {
      return fulfillJson(route, { error: "invalid_disable_request" }, 400);
    }
    const account = state.operatorAccounts.find((item) => item.accountId === accountId);
    if (!account) return fulfillJson(route, { error: "account_not_found" }, 404);
    state.operatorDisableWrites.add(operation);
    account.status = "disabled";
    return fulfillJson(route, { operationId: `account-disable-${accountId}`, accountId, status: "succeeded" });
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
      amountUsd: "5", reason: "browser retry", beforeBalance: source({ currency: "USD", usdMicros: "50000000" }, "sub2api"),
      afterBalance: source({ currency: "USD", usdMicros: "55000000" }, "sub2api"), balanceHistoryRef: "balance-history-fixture", actor: "user-operator"
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
  const diagnostic = await page.evaluate(() => {
    const overflow = document.documentElement.scrollWidth - document.documentElement.clientWidth;
    const clippedWorkspaceRows = [...document.querySelectorAll(".workspace-list > a")]
      .map((element) => {
        const rect = element.getBoundingClientRect();
        return { left: Math.round(rect.left), right: Math.round(rect.right), width: Math.round(rect.width) };
      })
      .filter((item) => item.left < -1 || item.right > innerWidth + 1);
    const ancestors = [];
    for (let element = document.querySelector(".overview-workspace-table table"); element && ancestors.length < 8; element = element.parentElement) {
      const rect = element.getBoundingClientRect();
      const style = getComputedStyle(element);
      ancestors.push({ tag: element.tagName, className: element.className, left: Math.round(rect.left), right: Math.round(rect.right), width: Math.round(rect.width), clientWidth: element.clientWidth, scrollWidth: element.scrollWidth, overflowX: style.overflowX, minWidth: style.minWidth, gridTemplateColumns: style.gridTemplateColumns });
    }
    const offenders = [...document.querySelectorAll("body *")]
      .map((element) => {
        const rect = element.getBoundingClientRect();
        const style = getComputedStyle(element);
        return { tag: element.tagName, className: element.className, left: Math.round(rect.left), right: Math.round(rect.right), width: Math.round(rect.width), scrollWidth: element.scrollWidth, overflowX: style.overflowX, position: style.position };
      })
      .filter((item) => item.right > innerWidth + 1 || item.left < -1)
      .sort((left, right) => right.right - left.right)
      .slice(0, 8);
    return { overflow, path: location.pathname, width: innerWidth, scrollWidth: document.documentElement.scrollWidth, clippedWorkspaceRows, ancestors, offenders };
  });
  if (diagnostic.overflow > 1 || diagnostic.clippedWorkspaceRows.length) {
    throw new Error(`console_browser_viewport_overflow:${JSON.stringify(diagnostic)}`);
  }
}

async function captureFixtureScreenshot(page, state, screenshotDir, screen, viewportName) {
  if (!screenshotDir) return;
  await mkdir(screenshotDir, { recursive: true });
  const screenshotPath = join(screenshotDir, `fixture-${screen}-${viewportName}.png`);
  await page.screenshot({ path: screenshotPath });
  state.screenshots.push(screenshotPath);
}

function assertOperatorPageReads(state, start, expected) {
  const actual = state.operatorPageReads.slice(start);
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`console_browser_operator_route_fanout:${JSON.stringify({ expected, actual })}`);
  }
}

async function chooseKeyMoreAction(page, keyRow, keyName, action) {
  await keyRow.getByRole("button", { name: `${keyName} 更多操作`, exact: true }).click();
  await page.getByRole("menu", { name: `${keyName} 更多操作`, exact: true }).getByRole("menuitem", { name: action, exact: true }).click();
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

  await chooseKeyMoreAction(page, keyRow, "Browser retry key", "编辑");
  const editDialog = page.getByRole("dialog", { name: "编辑 API Key" });
  await editDialog.getByLabel("名称").fill("Browser edited key");
  await editDialog.getByRole("button", { name: "保存", exact: true }).click();
  await waitForText(page, "API Key 已更新");

  keyRow = page.getByRole("row").filter({ hasText: "Browser edited key" }).first();
  await keyRow.getByLabel("快捷换组").selectOption("202");
  await waitForText(page, "分组已更新");
  keyRow = page.getByRole("row").filter({ hasText: "Browser edited key" }).first();
  await chooseKeyMoreAction(page, keyRow, "Browser edited key", "停用");
  await waitForText(page, "API Key 已停用");
  keyRow = page.getByRole("row").filter({ hasText: "Browser edited key" }).first();
  await chooseKeyMoreAction(page, keyRow, "Browser edited key", "启用");
  await waitForText(page, "API Key 已启用");
  keyRow = page.getByRole("row").filter({ hasText: "Browser edited key" }).first();
  await chooseKeyMoreAction(page, keyRow, "Browser edited key", "重置配额");
  await waitForText(page, "配额用量已重置");
  keyRow = page.getByRole("row").filter({ hasText: "Browser edited key" }).first();
  await chooseKeyMoreAction(page, keyRow, "Browser edited key", "重置消费限额");
  await waitForText(page, "消费限额用量已重置");
  keyRow = page.getByRole("row").filter({ hasText: "Browser edited key" }).first();
  await chooseKeyMoreAction(page, keyRow, "Browser edited key", "删除");
  const deleteDialog = page.getByRole("dialog", { name: "删除 API Key" });
  await deleteDialog.getByRole("button", { name: "删除", exact: true }).click();
  await waitForText(page, "API Key 已删除");
  await waitForText(page, "暂无数据");

  if (state.keys.length !== 0 || state.emptyGatewayReadbacks < 1) throw new Error("console_browser_gateway_empty_readback_failed");
}

async function retryWalletAdjustment(page, state, screenshotDir, viewportName) {
  await page.getByRole("row").filter({ hasText: "pilot@example.com" }).getByRole("button", { name: "余额操作" }).click();
  const dialog = page.getByRole("dialog", { name: "账户余额操作" });
  await dialog.getByLabel("金额（USD）").fill("5");
  await dialog.getByLabel("原因").fill("browser retry");
  await assertNoViewportOverflow(page);
  await captureFixtureScreenshot(page, state, screenshotDir, "admin-balance-operation", viewportName);
  const submit = dialog.getByRole("button", { name: "确认操作" });
  await submit.click();
  await waitForText(page, "结果待确认");
  await submit.click();
  await waitForText(page, "余额操作已提交");
}

async function openWorkspaceFromList(page, workspaceName) {
  const workspaceList = page.locator(".workspace-list");
  await workspaceList.getByRole("listitem").filter({ hasText: workspaceName }).click();
  await page.waitForURL(new RegExp(`/console/workspaces/ws-[12]$`));
  await waitForText(page, "Workspace URL");
  await page.locator(".workspace-access-panel").getByText("opl", { exact: true }).waitFor({ state: "visible" });
}

async function assertUsageRecordFields(page, viewportName) {
  const expectedHeaders = ["时间", "模型", "端点", "实际金额", "请求编号"];
  if (viewportName === "desktop") {
    const table = page.locator(".request-table-desktop");
    await table.getByText("request-fixture", { exact: true }).waitFor({ state: "visible" });
    const headers = (await table.locator("th").allTextContents()).map((label) => label.trim());
    if (JSON.stringify(headers) !== JSON.stringify(expectedHeaders)) {
      throw new Error(`console_browser_request_fields:${JSON.stringify(headers)}`);
    }
  } else {
    const row = page.locator(".request-list-mobile").getByRole("listitem").filter({ hasText: "request-fixture" });
    await row.getByText("gpt-5-mini", { exact: true }).waitFor({ state: "visible" });
    await row.getByText("/v1/responses", { exact: true }).waitFor({ state: "visible" });
    await row.getByText("$0.03", { exact: true }).waitFor({ state: "visible" });
    if (await row.locator("small").count() !== 2 || await row.locator("code").count() !== 1) {
      throw new Error("console_browser_mobile_request_fields");
    }
  }
  for (const label of ["输入 Token", "输出 Token", "缓存写入 Token", "缓存读取 Token", "请求详情", "查看详情"]) {
    if (await page.getByText(label, { exact: true }).count()) {
      throw new Error(`console_browser_request_extra_field:${label}`);
    }
  }
}

export async function runConsoleBrowserQa({
  network,
  serverFactory = defaultServerFactory,
  browserFactory = defaultBrowserFactory,
  screenshotDir = ""
} = {}) {
  if (network !== "fake-only") throw new Error("console_browser_fake_only_required");

  const server = await serverFactory();
  let browser;
  const state = {
    role: "customer", sourceState: "available", keys: [], launches: [],
    basicPlanAvailable: true,
    operatorAccounts: [operatorAccount("acct-1", "active"), operatorAccount("acct-2", "disabled")],
    gatewayWrites: new Set(), walletWrites: new Set(), lostGatewayResponses: new Set(), lostWalletResponses: new Set(),
    operatorDisableWrites: new Set(),
    gatewayMutationWrites: new Set(), gatewayActions: [], revealCalls: new Map(), emptyGatewayReadbacks: 0,
    runtimeReads: new Map(), workspaceSecretReads: new Map(), workspacePageReads: [],
    customerRoutes: new Set(), loginSubmissions: 0,
    operatorPageReads: [], operatorAccountViewports: new Set(), unavailablePlanKeyboardViewports: new Set(),
    unexpectedApi: [], externalRequests: 0, pageErrors: [], consoleErrors: [], expectedNetworkConsoleErrors: [], dialogMessages: [], screenshots: []
  };
  try {
    browser = await browserFactory();
    for (const [name, viewport] of Object.entries(VIEWPORTS)) {
      state.basicPlanAvailable = name !== "mobile";
      const context = await browser.newContext({ viewport, permissions: ["clipboard-read", "clipboard-write"] });
      const page = await context.newPage();
      page.on("pageerror", (error) => state.pageErrors.push(error.message));
      page.on("console", (message) => {
        if (message.type() !== "error") return;
        const text = message.text();
        if (/^Failed to load resource: (?:net::ERR_FAILED|the server responded with a status of 503)/.test(text)) {
          state.expectedNetworkConsoleErrors.push(text);
        } else {
          state.consoleErrors.push(text);
        }
      });
      page.on("dialog", (dialog) => {
        state.dialogMessages.push(dialog.message());
        void dialog.accept();
      });
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

      state.role = "customer";
      state.sourceState = "available";
      await page.goto(`${server.origin}/`, { waitUntil: "networkidle" });
      await waitForText(page, "面向已开通用户的 Workspace 与 API 服务。");
      const logoLoaded = await page.getByAltText("OPL Cloud").evaluate((image) => image.complete && image.naturalWidth > 0);
      if (!logoLoaded) throw new Error("console_browser_logo_missing");
      await page.goto(`${server.origin}/login`, { waitUntil: "networkidle" });
      await waitForText(page, "Console 登录");
      state.customerRoutes.add("/login");
      await page.getByLabel("邮箱").fill("fixture@example.com");
      await page.getByLabel("密码").fill("fixture-password");
      await page.getByRole("button", { name: "登录", exact: true }).click();
      await page.waitForURL(/\/console\/overview$/);
      await waitForText(page, "Workspace 总数");
      state.customerRoutes.add("/console/overview");

      await assertNoViewportOverflow(page);
      await captureFixtureScreenshot(page, state, screenshotDir, "console-overview", name);
      await page.goto(`${server.origin}/console/workspaces?viewport=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "Pilot Workspace");
      await waitForText(page, "Second Workspace");
      state.customerRoutes.add("/console/workspaces");
      await assertNoViewportOverflow(page);
      await captureFixtureScreenshot(page, state, screenshotDir, "workspace-list", name);
      await page.getByRole("button", { name: "新建 Workspace", exact: true }).click();
      await page.waitForURL(/\/console\/workspaces\/new$/);
      await waitForText(page, "下一步：确认");
      await waitForText(page, name === "mobile" ? "价格暂不可用" : "$52.58/月");
      await waitForText(page, "$240.08/月");
      state.customerRoutes.add("/console/workspaces/new");
      await assertNoViewportOverflow(page);
      await captureFixtureScreenshot(page, state, screenshotDir, "workspace-new", name);
      const workspaceName = page.getByLabel("Workspace 名称");
      await workspaceName.fill("Fixture review Workspace");
      if (name === "mobile") {
        const basicPlan = page.getByRole("radio", { name: /Basic/ });
        const proPlan = page.getByRole("radio", { name: /Pro/ });
        if (!await basicPlan.isDisabled()) throw new Error("console_browser_unavailable_basic_enabled");
        await workspaceName.focus();
        await page.keyboard.press("Tab");
        if (!await proPlan.evaluate((element) => document.activeElement === element)) {
          throw new Error("console_browser_unavailable_plan_not_keyboard_focusable");
        }
        await proPlan.press("Space");
        if (await proPlan.getAttribute("aria-checked") !== "true") {
          throw new Error("console_browser_unavailable_plan_keyboard_selection_failed");
        }
        state.unavailablePlanKeyboardViewports.add(name);
      }
      await page.getByRole("button", { name: "下一步：确认", exact: true }).click();
      await page.getByRole("heading", { name: "确认开通信息", exact: true }).waitFor({ state: "visible" });
      await captureFixtureScreenshot(page, state, screenshotDir, "workspace-confirm", name);
      state.launches = [pendingWorkspaceLaunch()];
      await page.goto(`${server.origin}/console/workspaces/new?progress=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "runtime_starting");
      await assertNoViewportOverflow(page);
      await captureFixtureScreenshot(page, state, screenshotDir, "workspace-progress", name);
      state.launches = [];
      await page.goto(`${server.origin}/console/workspaces?after-progress=${name}`, { waitUntil: "networkidle" });
      await openWorkspaceFromList(page, "Pilot Workspace");
      state.customerRoutes.add("/console/workspaces/ws-1");
      await page.goto(`${server.origin}/console/workspaces/ws-1?direct=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "https://workspace.example.invalid/w/ws-1/");
      await assertNoViewportOverflow(page);
      await captureFixtureScreenshot(page, state, screenshotDir, "workspace-detail", name);
      if (name === "desktop") {
        const passwordRow = page.locator("dt", { hasText: "密码" }).locator("..");
        await passwordRow.getByRole("button", { name: "显示" }).click();
        await waitForText(page, WORKSPACE_PASSWORDS["ws-1"]);
        await passwordRow.getByRole("button", { name: "复制" }).click();
        const keyRow = page.locator("dt", { hasText: "Workspace Key" }).locator("..");
        await keyRow.getByRole("button", { name: "显示" }).click();
        await waitForText(page, WORKSPACE_KEYS["9"]);
        await keyRow.getByRole("button", { name: "复制" }).click();
      }

      await page.getByRole("button", { name: "Workspace 列表", exact: true }).click();
      await page.waitForURL(/\/console\/workspaces$/);
      await openWorkspaceFromList(page, "Second Workspace");
      state.customerRoutes.add("/console/workspaces/ws-2");
      await page.goto(`${server.origin}/console/workspaces/ws-2?direct=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "https://workspace.example.invalid/w/ws-2/");
      await waitForText(page, "PRO");
      await waitForText(page, "2026/08/15");
      if (await page.getByText(WORKSPACE_PASSWORDS["ws-1"], { exact: true }).count() || await page.getByText(WORKSPACE_KEYS["9"], { exact: true }).count()) {
        throw new Error("console_browser_workspace_navigation_secret_cleanup_failed");
      }
      if (name === "desktop") {
        const passwordRow = page.locator("dt", { hasText: "密码" }).locator("..");
        const keyRow = page.locator("dt", { hasText: "Workspace Key" }).locator("..");
        await passwordRow.getByRole("button", { name: "显示" }).click();
        await waitForText(page, WORKSPACE_PASSWORDS["ws-2"]);
        await keyRow.getByRole("button", { name: "显示" }).click();
        await waitForText(page, WORKSPACE_KEYS["19"]);
      }
      await assertNoViewportOverflow(page);

      await page.goto(`${server.origin}/console/api?viewport=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "余额记录");
      state.customerRoutes.add("/console/api");
      await assertNoViewportOverflow(page);

      state.keys = [gatewayKey()];
      await page.goto(`${server.origin}/console/api/usage?viewport=${name}`, { waitUntil: "networkidle" });
      await assertUsageRecordFields(page, name);
      state.customerRoutes.add("/console/api/usage");
      await assertNoViewportOverflow(page);
      await captureFixtureScreenshot(page, state, screenshotDir, "api-usage", name);

      state.keys = [gatewayKey()];
      await page.goto(`${server.origin}/console/api/keys?viewport=${name}`, { waitUntil: "networkidle" });
      await (name === "desktop" ? page.locator(".keys-table-wrap") : page.locator(".mobile-key-list"))
        .getByText("General fixture key", { exact: true }).waitFor({ state: "visible" });
      await assertNoViewportOverflow(page);
      if (name === "mobile") await page.locator(".mobile-key-card").scrollIntoViewIfNeeded();
      await captureFixtureScreenshot(page, state, screenshotDir, "api-keys", name);
      await page.getByRole("button", { name: "创建 Key" }).click();
      await page.getByRole("dialog", { name: "创建 API Key" }).waitFor({ state: "visible" });
      await assertNoViewportOverflow(page);
      await captureFixtureScreenshot(page, state, screenshotDir, "api-key-create", name);
      await page.getByRole("dialog", { name: "创建 API Key" }).getByRole("button", { name: "关闭" }).click();
      state.keys = [];
      await page.goto(`${server.origin}/console/api/keys?empty=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "暂无数据");
      state.customerRoutes.add("/console/api/keys");
      if (name === "desktop") {
        await page.goto(`${server.origin}/console/api/keys?write=1`, { waitUntil: "networkidle" });
        await exerciseGatewayKeyLifecycle(page, state);
      }

      await page.goto(`${server.origin}/console/billing?viewport=${name}`, { waitUntil: "networkidle" });
      await page.getByRole("heading", { name: "Workspace 条款", exact: true }).waitFor({ state: "visible" });
      state.customerRoutes.add("/console/billing");
      if (await page.getByText(WORKSPACE_PASSWORDS["ws-2"], { exact: true }).count() || await page.getByText(WORKSPACE_KEYS["19"], { exact: true }).count()) {
        throw new Error("console_browser_secret_cleanup_failed");
      }
      await page.getByRole("radio", { name: "账单收据", exact: true }).click();
      if (name === "desktop") {
        await page.locator(".billing-table-desktop").getByText("Workspace 开通", { exact: true }).waitFor({ state: "visible" });
        await page.getByRole("button", { name: "查看", exact: true }).click();
      } else {
        await page.locator(".billing-list-mobile").getByText("Workspace 开通", { exact: true }).waitFor({ state: "visible" });
        await page.locator(".billing-list-mobile").getByRole("listitem").click();
      }
      await page.getByRole("heading", { name: "收据详情", exact: true }).waitFor({ state: "visible" });
      await waitForText(page, "pilot-usd-2026-07-v1");
      await assertNoViewportOverflow(page);

      await page.goto(`${server.origin}/console/announcements?viewport=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "暂无公告");
      state.customerRoutes.add("/console/announcements");
      await assertNoViewportOverflow(page);

      for (const sourceState of ["empty", "unavailable", "error"]) {
        state.sourceState = sourceState;
        await page.goto(`${server.origin}/console/api/keys?state=${sourceState}&viewport=${name}`, { waitUntil: "networkidle" });
        await waitForText(page, sourceState === "empty" ? "暂无数据" : sourceState === "unavailable" ? "暂不可用" : "服务暂不可用");
      }

      state.role = "operator";
      state.sourceState = "available";
      let operatorReadStart = state.operatorPageReads.length;
      await page.goto(`${server.origin}/admin/overview?viewport=${name}`, { waitUntil: "networkidle" });
      await waitForText(page.locator(".main-column"), "运维概览");
      assertOperatorPageReads(state, operatorReadStart, ["/api/operator/overview", "/api/operator/announcements"]);
      await assertNoViewportOverflow(page);
      await captureFixtureScreenshot(page, state, screenshotDir, "admin-overview", name);
      operatorReadStart = state.operatorPageReads.length;
      await page.goto(`${server.origin}/admin/billing?viewport=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "暂无待复核项目");
      assertOperatorPageReads(state, operatorReadStart, ["/api/operator/reconciliation"]);
      operatorReadStart = state.operatorPageReads.length;
      await page.goto(`${server.origin}/admin/system?viewport=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "系统健康");
      assertOperatorPageReads(state, operatorReadStart, ["/api/operator/health"]);
      operatorReadStart = state.operatorPageReads.length;
      await page.goto(`${server.origin}/admin/announcements?viewport=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "暂无公告");
      assertOperatorPageReads(state, operatorReadStart, ["/api/operator/announcements"]);
      operatorReadStart = state.operatorPageReads.length;
      await page.goto(`${server.origin}/admin/resources?viewport=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "provider ID");
      await waitForText(page, "最近读回时间");
      assertOperatorPageReads(state, operatorReadStart, ["/api/operator/workspaces"]);
      if (name === "mobile") {
        state.operatorAccounts = [operatorAccount("acct-1", "active"), operatorAccount("acct-2", "disabled")];
      }
      operatorReadStart = state.operatorPageReads.length;
      await page.goto(`${server.origin}/admin/accounts?viewport=${name}`, { waitUntil: "networkidle" });
      assertOperatorPageReads(state, operatorReadStart, ["/api/operator/accounts"]);
      const activeAccountRow = page.getByRole("row").filter({ hasText: "pilot@example.com" });
      const disabledAccountRow = page.getByRole("row").filter({ hasText: "stopped@example.com" });
      await activeAccountRow.getByText("正常", { exact: true }).waitFor({ state: "visible" });
      await disabledAccountRow.getByText("已停用", { exact: true }).waitFor({ state: "visible" });
      if (await page.getByText("归档", { exact: false }).count() || await page.getByRole("radio", { name: "已归档", exact: true }).count()) {
        throw new Error("console_browser_archive_semantics_present");
      }
      await assertNoViewportOverflow(page);
      await captureFixtureScreenshot(page, state, screenshotDir, "admin-accounts", name);
      state.operatorAccountViewports.add(name);
      if (name === "desktop") {
        operatorReadStart = state.operatorPageReads.length;
        await activeAccountRow.getByRole("button", { name: "停用", exact: true }).click();
        await waitForText(page, "客户已停用");
        assertOperatorPageReads(state, operatorReadStart, ["/api/operator/accounts"]);
        const refreshedAccountRow = page.getByRole("row").filter({ hasText: "pilot@example.com" });
        await refreshedAccountRow.getByText("已停用", { exact: true }).waitFor({ state: "visible" });
        if (!state.dialogMessages.includes("确认停用该客户？账号会立即停用；历史账单、收据和审计记录会保留。")) {
          throw new Error(`console_browser_disable_confirmation_missing:${JSON.stringify(state.dialogMessages)}`);
        }
        await captureFixtureScreenshot(page, state, screenshotDir, "admin-account-disabled", name);
        operatorReadStart = state.operatorPageReads.length;
        await retryWalletAdjustment(page, state, screenshotDir, name);
        assertOperatorPageReads(state, operatorReadStart, ["/api/operator/accounts"]);
      } else {
        state.operatorAccounts[0] = operatorAccount("acct-1", "disabled");
      }
      for (const sourceState of ["empty", "unavailable", "error"]) {
        state.sourceState = sourceState;
        operatorReadStart = state.operatorPageReads.length;
        await page.goto(`${server.origin}/admin/resources?state=${sourceState}&viewport=${name}`, { waitUntil: "networkidle" });
        await waitForText(page, sourceState === "empty" ? "暂无 Workspace" : sourceState === "unavailable" ? "Workspace 暂不可用" : "服务暂不可用");
        assertOperatorPageReads(state, operatorReadStart, ["/api/operator/workspaces"]);
      }
      await assertNoViewportOverflow(page);
      await context.close();
    }

    if (state.unexpectedApi.length) throw new Error(`console_browser_unexpected_api:${state.unexpectedApi.join(",")}`);
    if (state.pageErrors.length) throw new Error(`console_browser_page_error:${state.pageErrors.join(",")}`);
    if (state.consoleErrors.length) throw new Error(`console_browser_console_error:${state.consoleErrors.join(",")}`);
    if (state.gatewayWrites.size !== 1 || state.walletWrites.size !== 1) throw new Error("console_browser_idempotency_failed");
    if (state.operatorDisableWrites.size !== 1) throw new Error(`console_browser_operator_disable_failed:${state.operatorDisableWrites.size}`);
    const expectedGatewayActions = ["edit", "group", "disable", "enable", "quota-reset", "rate-reset", "delete"];
    if (state.gatewayMutationWrites.size !== expectedGatewayActions.length || JSON.stringify(state.gatewayActions) !== JSON.stringify(expectedGatewayActions)) {
      throw new Error(`console_browser_gateway_lifecycle_failed:${JSON.stringify(state.gatewayActions)}`);
    }
    if (state.revealCalls.get("12") !== 1) throw new Error(`console_browser_created_key_reveal_failed:${state.revealCalls.get("12") || 0}`);
    if (state.revealCalls.get("9") !== 1 || state.revealCalls.get("19") !== 1) throw new Error(`console_browser_workspace_key_scope_failed:${JSON.stringify(Object.fromEntries(state.revealCalls))}`);
    if (state.workspaceSecretReads.get("ws-1") !== 1 || state.workspaceSecretReads.get("ws-2") !== 1) throw new Error(`console_browser_workspace_secret_scope_failed:${JSON.stringify(Object.fromEntries(state.workspaceSecretReads))}`);
    const missingCustomerRoutes = CUSTOMER_ROUTES.filter((route) => !state.customerRoutes.has(route));
    if (missingCustomerRoutes.length) throw new Error(`console_browser_customer_route_missing:${missingCustomerRoutes.join(",")}`);
    if (state.externalRequests !== 0) throw new Error(`console_browser_external_request:${state.externalRequests}`);
    return {
      ok: true,
      evidenceLevel: "code-complete",
      network: "fake-only",
      viewports: Object.keys(VIEWPORTS),
      roles: ["customer", "operator"],
      sourceStates: ["available", "empty", "unavailable", "error"],
      repeatedWrites: { gatewayKey: state.gatewayWrites.size, walletAdjustment: state.walletWrites.size },
      operatorAccountDisableWrites: state.operatorDisableWrites.size,
      operatorAccountStatuses: Object.fromEntries(state.operatorAccounts.map((account) => [account.accountId, account.status])),
      operatorAccountViewports: [...state.operatorAccountViewports],
      unavailablePlanKeyboardViewports: [...state.unavailablePlanKeyboardViewports],
      workspaceNavigation: state.runtimeReads.has("ws-1") && state.runtimeReads.has("ws-2"),
      workspacePagination: state.workspacePageReads.some(({ page, pageSize }) => page === 1 && pageSize === 10)
        && state.workspacePageReads.some(({ page, pageSize }) => page === 1 && pageSize === 1),
      directDetailRefresh: (state.runtimeReads.get("ws-1") || 0) > 1,
      requestRecordFields: ["time", "model", "endpoint", "actualAmount", "requestId"],
      billingViews: true,
      loginSubmissions: state.loginSubmissions,
      customerRoutes: CUSTOMER_ROUTES.filter((route) => state.customerRoutes.has(route)),
      screenshots: state.screenshots,
      workspaceSecretReads: Object.fromEntries(state.workspaceSecretReads),
      keyInteractions: state.gatewayActions,
      secretCleanup: true,
      operatorRouteLazyReads: true,
      externalRequests: state.externalRequests,
      consoleErrors: state.consoleErrors,
      expectedNetworkConsoleErrors: state.expectedNetworkConsoleErrors.length
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
  runConsoleBrowserQa({
    network: networkArg(process.argv.slice(2)),
    screenshotDir: process.env.OPL_CONSOLE_QA_SCREENSHOT_DIR || ""
  })
    .then((result) => process.stdout.write(`${JSON.stringify(result, null, 2)}\n`))
    .catch((error) => {
      process.stderr.write(`${JSON.stringify({ ok: false, error: error.message }, null, 2)}\n`);
      process.exitCode = 1;
    });
}
