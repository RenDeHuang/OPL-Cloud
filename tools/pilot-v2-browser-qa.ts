import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const NOW = "2026-07-19T12:00:00Z";
const WORKSPACE_PASSWORD = "fixture-workspace-password";
const WORKSPACE_KEY = "sk-fixture-workspace-key";
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

function gatewayKey(id = "11", name = "General fixture key") {
  return {
    id, name, kind: id === "9" ? "workspace" : "general", status: "active",
    quotaUsdMicros: 10_000_000, quotaUsedUsdMicros: 250_000,
    usage5hUsdMicros: 0, usage1dUsdMicros: 10_000, usage7dUsdMicros: 25_000,
    expiresAt: "2026-08-18T12:00:00Z", lastUsedAt: NOW,
    manageable: id !== "9", deletable: id !== "9"
  };
}

function workspace() {
  return {
    id: "ws-1", ownerAccountId: "acct-1", ownerUserId: "user-customer", state: "running",
    createdAt: "2026-07-01T00:00:00Z", updatedAt: NOW, name: "Pilot Workspace",
    url: "https://workspace.example.invalid/w/ws-1/", packageId: "basic", storageGb: 10,
    autoRenew: false, priceVersion: "pilot-v2", currency: "USD", totalUsdMicros: 30_000_000,
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
  if (!address || typeof address === "string") throw new Error("pilot_v2_browser_server_address_missing");
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

  if (path === "/api/workspaces" && method === "GET") return fulfillJson(route, source({ items: [workspace()], total: 1 }));
  if (path === "/api/workspace-launches" && method === "GET") return fulfillJson(route, []);
  if (path === "/api/workspaces/ws-1/runtime-status") return fulfillJson(route, source({
    workspaceId: "ws-1", status: "running", ready: true, runtimeId: "runtime-1",
    url: workspace().url, serviceName: "runtime-ws-1", checks: [{ name: "ready_pod_uses_retained_pvc", ok: true }],
    access: { username: "opl", credentialStatus: "configured", credentialVersion: "1" }
  }, "fabric"));
  if (path === "/api/workspaces/ws-1/runtime-credentials/reveal" && method === "POST") return fulfillJson(route, {
    workspaceId: "ws-1",
    access: { account: "acct-1", username: "opl", password: WORKSPACE_PASSWORD, credentialStatus: "configured", credentialVersion: "1" }
  });
  if (path === "/api/pricing/catalog") return fulfillJson(route, {
    priceVersion: "pilot-v2", billingUnit: "month", displayCurrency: "USD", walletCurrency: "USD", currency: "USD",
    packages: [{ id: "basic", name: "Basic", available: true, cpu: 2, memoryGb: 4, diskGb: 10, server: "2c4g", price: { priceVersion: "pilot-v2", currency: "USD", chargeUsdMicros: 30_000_000 } }]
  });
  if (path === "/api/pricing/preview" && method === "POST") return fulfillJson(route, {
    resourceType: "workspace", packageId: "basic", priceVersion: "pilot-v2", currency: "USD",
    displayCurrency: "USD", billingUnit: "month", totalChargeUsdMicros: 30_000_000
  });
  if (path === "/api/billing/receipts") return fulfillJson(route, source({ receipts: [], nextCursor: "", hasMore: false }, "ledger", "empty"));
  if (path === "/api/announcements") return fulfillJson(route, source(emptyPage, "control-plane", "empty"));
  if (path === "/api/gateway/endpoint") return fulfillJson(route, source({ baseUrl: "https://api.example.invalid" }));
  if (path === "/api/gateway/wallet") return fulfillJson(route, source({ userId: "9", currency: "USD", usdMicros: 50_000_000, status: "active" }, "sub2api"));
  if (path === "/api/gateway/usage-summary") return fulfillJson(route, source({ totalRequests: 1, totalInputTokens: 10, totalOutputTokens: 2, totalTokens: 12, totalActualCostUsdMicros: 25_000 }, "sub2api"));
  if (path === "/api/gateway/balance-history") return fulfillJson(route, source({ items: [], total: 0 }, "sub2api", "empty"));

  if (path === "/api/gateway/keys" && method === "GET") {
    if (state.sourceState === "error") return fulfillJson(route, { error: "upstream_unavailable" }, 503);
    const keys = state.sourceState === "available" ? state.keys : [];
    return fulfillJson(route, sourceForState(state, { items: keys, total: keys.length, page: 1, pageSize: 20 }, "sub2api"));
  }
  if (path === "/api/gateway/keys" && method === "POST") {
    const operation = request.headers()["idempotency-key"] || "";
    if (!operation) return fulfillJson(route, { error: "idempotency_key_required" }, 400);
    state.gatewayWrites.add(operation);
    if (!state.keys.some((item) => item.id === "12")) state.keys.push(gatewayKey("12", "Browser retry key"));
    if (!state.lostGatewayResponses.has(operation)) {
      state.lostGatewayResponses.add(operation);
      return route.abort("failed");
    }
    return fulfillJson(route, source(gatewayKey("12", "Browser retry key"), "sub2api"));
  }
  const keyMatch = path.match(/^\/api\/gateway\/keys\/(\d+)$/);
  if (keyMatch && method === "GET") return fulfillJson(route, source(state.keys.find((item) => item.id === keyMatch[1]) || gatewayKey(keyMatch[1]), "sub2api"));
  const revealMatch = path.match(/^\/api\/gateway\/keys\/(\d+)\/reveal$/);
  if (revealMatch && method === "POST") return fulfillJson(route, source({
    id: revealMatch[1], name: revealMatch[1] === "9" ? "Workspace Key" : "General fixture key",
    status: "active", value: revealMatch[1] === "9" ? WORKSPACE_KEY : "sk-fixture-general-key"
  }, "sub2api"));

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
    throw new Error(`pilot_v2_browser_text_hidden:${text}:${JSON.stringify(diagnostic)}`, { cause: error });
  }
}

async function assertNoViewportOverflow(page) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 1) throw new Error(`pilot_v2_browser_viewport_overflow:${overflow}`);
}

async function retryGatewayKey(page) {
  await page.getByRole("button", { name: "创建 Key" }).click();
  const dialog = page.getByRole("dialog", { name: "api-key" });
  await dialog.getByLabel("名称").fill("Browser retry key");
  const submit = dialog.getByRole("button", { name: "创建", exact: true });
  await submit.click();
  await page.waitForFunction(() => [...document.querySelectorAll("button")].some((button) => button.textContent?.trim() === "创建" && !button.disabled));
  await submit.click();
  await waitForText(page, "API Key 已创建");
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

export async function runPilotV2BrowserQa({
  network,
  serverFactory = defaultServerFactory,
  browserFactory = defaultBrowserFactory
} = {}) {
  if (network !== "fake-only") throw new Error("pilot_v2_browser_fake_only_required");

  const server = await serverFactory();
  let browser;
  const state = {
    role: "customer", sourceState: "available", keys: [gatewayKey("9", "Workspace Key"), gatewayKey()],
    gatewayWrites: new Set(), walletWrites: new Set(), lostGatewayResponses: new Set(), lostWalletResponses: new Set(),
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
      await waitForText(page, "邀请制 Workspace 与 API 服务。");
      const logoLoaded = await page.getByAltText("OPL Cloud").evaluate((image) => image.complete && image.naturalWidth > 0);
      if (!logoLoaded) throw new Error("pilot_v2_browser_logo_missing");
      await page.goto(`${server.origin}/login`, { waitUntil: "networkidle" });
      await waitForText(page, "Console 登录");

      state.role = "customer";
      state.sourceState = "available";
      await page.goto(`${server.origin}/console/workspace?viewport=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "Workspace URL");
      await waitForText(page, "opl");
      if (name === "desktop") {
        const passwordRow = page.locator("dt", { hasText: "密码" }).locator("..");
        await passwordRow.getByRole("button", { name: "显示" }).click();
        await waitForText(page, WORKSPACE_PASSWORD);
        await passwordRow.getByRole("button", { name: "复制" }).click();
        const keyRow = page.locator("dt", { hasText: "Workspace Key" }).locator("..");
        await keyRow.getByRole("button", { name: "显示" }).click();
        await waitForText(page, WORKSPACE_KEY);
        await keyRow.getByRole("button", { name: "复制" }).click();
        await page.getByRole("link", { name: "账单", exact: true }).click();
        if (await page.getByText(WORKSPACE_PASSWORD, { exact: true }).count() || await page.getByText(WORKSPACE_KEY, { exact: true }).count()) {
          throw new Error("pilot_v2_browser_secret_cleanup_failed");
        }
        state.sourceState = "available";
        await page.goto(`${server.origin}/console/api/keys?write=1`, { waitUntil: "networkidle" });
        await retryGatewayKey(page);
      }

      for (const sourceState of ["empty", "unavailable", "error"]) {
        state.sourceState = sourceState;
        await page.goto(`${server.origin}/console/api/keys?state=${sourceState}&viewport=${name}`, { waitUntil: "networkidle" });
        await waitForText(page, sourceState === "empty" ? "暂无 API Key" : sourceState === "unavailable" ? "暂不可用" : "服务暂不可用");
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

    if (state.unexpectedApi.length) throw new Error(`pilot_v2_browser_unexpected_api:${state.unexpectedApi.join(",")}`);
    if (state.pageErrors.length) throw new Error(`pilot_v2_browser_page_error:${state.pageErrors.join(",")}`);
    if (state.gatewayWrites.size !== 1 || state.walletWrites.size !== 1) throw new Error("pilot_v2_browser_idempotency_failed");
    if (state.externalRequests !== 0) throw new Error(`pilot_v2_browser_external_request:${state.externalRequests}`);
    return {
      ok: true,
      evidenceLevel: "code-complete",
      network: "fake-only",
      viewports: Object.keys(VIEWPORTS),
      roles: ["customer", "operator"],
      sourceStates: ["available", "empty", "unavailable", "error"],
      repeatedWrites: { gatewayKey: state.gatewayWrites.size, walletAdjustment: state.walletWrites.size },
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
  runPilotV2BrowserQa({ network: networkArg(process.argv.slice(2)) })
    .then((result) => process.stdout.write(`${JSON.stringify(result, null, 2)}\n`))
    .catch((error) => {
      process.stderr.write(`${JSON.stringify({ ok: false, error: error.message }, null, 2)}\n`);
      process.exitCode = 1;
    });
}
