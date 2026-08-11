import { mkdir } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const NOW = "2026-07-19T12:00:00Z";
export const CONSOLE_DEMO_CREDENTIALS = Object.freeze({
  customer: Object.freeze({ email: "fixture@example.com", password: "fixture-password" }),
  admin: Object.freeze({ email: "operator@example.com", password: "operator-password" })
});
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
  "/api/operator/workspaces/ws-1",
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

function fixtureIdentity(role = "customer") {
  const operator = role === "operator";
  return {
    authenticated: true,
    role,
    accountId: operator ? "acct-operator" : "acct-1",
    consoleUserId: operator ? "user-operator" : "user-customer",
    sub2apiUserId: operator ? "10" : "9",
    email: operator ? CONSOLE_DEMO_CREDENTIALS.admin.email : CONSOLE_DEMO_CREDENTIALS.customer.email
  };
}

export function createConsoleFixtureSession() {
  return {
    authenticated: false,
    role: "customer",
    accountId: "",
    consoleUserId: "",
    sub2apiUserId: "",
    email: ""
  };
}

function authenticateFixtureSession(session, role) {
  Object.assign(session, fixtureIdentity(role));
}

function clearFixtureSession(session) {
  Object.assign(session, createConsoleFixtureSession());
}

function source(data, name = "control-plane", status = "available") {
  return { source: name, status, available: true, fetchedAt: NOW, data };
}

function unavailable(name) {
  const normalizedName = name.trim().toLowerCase().replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "") || "unknown";
  return { source: name, status: "unavailable", available: false, fetchedAt: NOW, reasonCode: `${normalizedName}_unavailable` };
}

function gatewayKey(id = "11", name = "General fixture key", input = {}, identity = {}) {
  const kind = identity.kind || (Object.hasOwn(WORKSPACE_KEYS, id) ? "workspace" : "general");
  return {
    id, name, kind, status: "active",
    groupId: input.groupId || "101", ipWhitelist: input.ipWhitelist || [], ipBlacklist: input.ipBlacklist || [],
    quotaUsdMicros: input.quotaUsdMicros ?? 10_000_000, quotaUsedUsdMicros: 250_000,
    rateLimit5hUsdMicros: input.rateLimit5hUsdMicros || 0, rateLimit1dUsdMicros: input.rateLimit1dUsdMicros || 0,
    rateLimit7dUsdMicros: input.rateLimit7dUsdMicros || 0,
    usage5hUsdMicros: 0, usage1dUsdMicros: 10_000, usage7dUsdMicros: 25_000, currentConcurrency: 0,
    expiresAt: "2026-08-18T12:00:00Z", lastUsedAt: NOW, lastUsedIp: "127.0.0.1", createdAt: NOW, updatedAt: NOW,
    manageable: kind === "general", deletable: kind === "general",
    ownerAccountId: identity.ownerAccountId || "acct-1",
    secret: identity.secret || WORKSPACE_KEYS[id] || GENERAL_KEY
  };
}

function gatewayKeyView(key) {
  return Object.fromEntries(Object.entries(key).filter(([field]) => !["ownerAccountId", "secret"].includes(field)));
}

function allGatewayKeys(state) {
  return [...state.keys, ...state.workspaceKeys];
}

function findGatewayKey(state, keyId, accountId) {
  return allGatewayKeys(state).find((key) => key.id === keyId && key.ownerAccountId === accountId);
}

function allocateGatewayKeyId(state) {
  while (allGatewayKeys(state).some((key) => key.id === String(state.nextKeyId))) state.nextKeyId += 1;
  const keyId = String(state.nextKeyId);
  state.nextKeyId += 1;
  return keyId;
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

function recoveryPlan(executionId = "") {
  return {
    planId: "recovery-plan-fixture", planDigest: "sha256:recovery-plan-fixture", status: executionId ? "succeeded" : "ready",
    operationId: "launch-recovery-fixture", stages: [{ stage: "diagnose", status: "succeeded", phase: "manual_review" }, { stage: "continue", status: executionId ? "succeeded" : "ready" }],
    mismatches: [{ field: "workspace_image_digest", expectedDigest: "sha256:expected", actualDigest: "sha256:actual" }],
    ...(executionId ? { executionId, runId: "run-recovery-fixture", url: "https://workspace.example.invalid/w/recovered/", receiptId: "receipt-recovery-fixture" } : {})
  };
}

function operatorAccount(accountId, status, overrides = {}) {
  const disabled = status === "disabled";
  const userId = overrides.sub2apiUserId || (disabled ? "11" : "9");
  const email = overrides.email || (disabled ? "stopped@example.com" : "pilot@example.com");
  return {
    accountId, consoleUserId: overrides.consoleUserId || (disabled ? "user-stopped" : "user-customer"), role: "owner", sub2apiUserId: userId, email, status,
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

function demoAnnouncement(id = "announcement-demo-1") {
  return {
    id,
    title: "OPL Cloud 演示环境已就绪",
    body: "这是 localhost 内存数据，用于查看公告、已读状态和 Console 信息层级。",
    status: "published",
    startsAt: NOW,
    publishedAt: NOW,
    createdAt: NOW,
    updatedAt: NOW,
    read: false
  };
}

export function createConsoleFixtureState({ faultInjection = true, seedDemoData = false } = {}) {
  return {
    ...createConsoleFixtureSession(),
    sourceState: "available",
    keys: seedDemoData ? [gatewayKey()] : [],
    workspaceKeys: [
      gatewayKey("9", "Workspace Key", {}, { kind: "workspace", ownerAccountId: "acct-1", secret: WORKSPACE_KEYS["9"] }),
      gatewayKey("19", "Workspace Key", {}, { kind: "workspace", ownerAccountId: "acct-1", secret: WORKSPACE_KEYS["19"] })
    ],
    nextKeyId: 12,
    gatewayWriteResults: new Map(),
    workspaceLaunchWriteResults: new Map(),
    operatorProvisionWriteResults: new Map(),
    announcementCreateWriteResults: new Map(),
    announcementPublishWriteResults: new Map(),
    announcementWithdrawWriteResults: new Map(),
    supportWriteResults: new Map(),
    launches: [],
    workspaces: [workspace(), workspace("ws-2")],
    workspacePasswords: new Map(Object.entries(WORKSPACE_PASSWORDS)),
    workspaceCredentialVersions: new Map(Object.keys(WORKSPACE_PASSWORDS).map((workspaceId) => [workspaceId, 1])),
    announcements: seedDemoData ? [demoAnnouncement()] : [],
    supportTickets: [],
    basicPlanAvailable: true,
    faultInjection,
    operatorAccounts: [operatorAccount("acct-1", "active"), operatorAccount("acct-2", "disabled")],
    gatewayWrites: new Set(), walletWrites: new Set(), lostGatewayResponses: new Set(), lostWalletResponses: new Set(),
    workspaceLaunchWrites: new Set(), operatorProvisionWrites: new Set(), announcementCreateWrites: new Set(),
    announcementPublishWrites: new Set(), announcementWithdrawWrites: new Set(), supportWrites: new Set(),
    workspaceLaunchAttempts: new Map(), operatorProvisionAttempts: new Map(), announcementCreateAttempts: new Map(),
    announcementPublishAttempts: new Map(), announcementWithdrawAttempts: new Map(), supportAttempts: new Map(),
    lostWorkspaceLaunchResponses: new Set(), lostOperatorProvisionResponses: new Set(), lostAnnouncementCreateResponses: new Set(),
    lostAnnouncementPublishResponses: new Set(), lostAnnouncementWithdrawResponses: new Set(), lostSupportResponses: new Set(),
    workspaceLaunchReadbacks: new Set(), operatorProvisionReadbacks: new Set(), announcementReadbackStatuses: new Map(), supportReadbacks: new Set(),
    operatorDisableWrites: new Set(),
    recoveryPlanDiagnoses: 0, recoveryPlanValidations: 0, recoveryPlanExecutions: new Map(), recoveryPlanExecuteAttempts: new Map(),
    gatewayMutationWrites: new Set(), gatewayActions: [], revealCalls: new Map(), emptyGatewayReadbacks: 0,
    runtimeReads: new Map(), workspaceSecretReads: new Map(), workspacePageReads: [],
    customerRoutes: new Set(), loginSubmissions: 0,
    operatorPageReads: [], operatorAccountViewports: new Set(), unavailablePlanKeyboardViewports: new Set(),
    unexpectedApi: [], externalRequests: 0, pageErrors: [], consoleErrors: [], expectedNetworkConsoleErrors: [], expectedConsole404s: new Set(), dialogMessages: [], screenshots: []
  };
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

function recordWriteAttempt(writes, attempts, identity) {
  writes.add(identity);
  attempts.set(identity, (attempts.get(identity) || 0) + 1);
}

export async function apiFixture(route, state, session = state) {
  const request = route.request();
  const url = new URL(request.url());
  const path = url.pathname;
  const method = request.method();
  if (method === "GET" && OPERATOR_PAGE_READS.has(path)) state.operatorPageReads.push(path);
  const emptyPage = { items: [], total: 0, page: 1, pageSize: 20 };

  if (path === "/api/auth/login" && method === "POST") {
    const input = request.postDataJSON();
    const role = input.email === CONSOLE_DEMO_CREDENTIALS.customer.email && input.password === CONSOLE_DEMO_CREDENTIALS.customer.password
      ? "customer"
      : input.email === CONSOLE_DEMO_CREDENTIALS.admin.email && input.password === CONSOLE_DEMO_CREDENTIALS.admin.password
        ? "operator"
        : "";
    if (!role) {
      return fulfillJson(route, { error: "invalid_credentials" }, 401);
    }
    authenticateFixtureSession(session, role);
    state.loginSubmissions += 1;
    const operator = role === "operator";
    return fulfillJson(route, {
      user: {
        id: operator ? "user-operator" : "user-customer",
        accountId: operator ? "acct-operator" : "acct-1",
        email: input.email,
        role: operator ? "admin" : "owner",
        status: "active"
      },
      isOperator: operator,
      csrfToken: "csrf-fixture"
    }, 200, { "x-opl-csrf-token": "csrf-fixture" });
  }

  if (path === "/api/auth/me") {
    if (!session.authenticated) return fulfillJson(route, { error: "not_authenticated" }, 401);
    const operator = session.role === "operator";
    return fulfillJson(route, source({
      consoleUserId: session.consoleUserId,
      accountId: session.accountId,
      role: operator ? "admin" : "owner",
      sub2apiUserId: session.sub2apiUserId,
      email: session.email,
      status: "active"
    }, "control-plane"), 200, { "x-opl-csrf-token": "csrf-fixture" });
  }

  if (path === "/api/auth/logout" && method === "POST") {
    clearFixtureSession(session);
    return fulfillJson(route, { ok: true });
  }

  if (!session.authenticated) return fulfillJson(route, { error: "not_authenticated" }, 401);
  if (path.startsWith("/api/operator/") && session.role !== "operator") {
    return fulfillJson(route, { error: "operator_required" }, 403);
  }

  if (path === "/api/workspaces" && method === "GET") {
    const page = Number(url.searchParams.get("page"));
    const pageSize = Number(url.searchParams.get("pageSize"));
    const allWorkspaces = state.workspaces.filter((item) => item.ownerAccountId === session.accountId);
    const start = (page - 1) * pageSize;
    const items = allWorkspaces.slice(start, start + pageSize);
    state.workspacePageReads.push({ page, pageSize });
    for (const operation of state.workspaceLaunchWriteResults.values()) {
      if (items.some((item) => item.id === operation.workspaceId)) state.workspaceLaunchReadbacks.add(operation.workspaceId);
    }
    return fulfillJson(route, source({ items, total: allWorkspaces.length, page, pageSize }));
  }
  if (path === "/api/workspace-launches" && method === "GET") {
    return fulfillJson(route, state.launches.filter((item) => item.accountId === session.accountId));
  }
  if (path === "/api/workspace-launches" && method === "POST") {
    const idempotencyKey = request.headers()["idempotency-key"] || "";
    if (!idempotencyKey) return fulfillJson(route, { error: "idempotency_key_required" }, 400);
    const input = request.postDataJSON();
    const writeIdentity = `${session.accountId}:${idempotencyKey}`;
    recordWriteAttempt(state.workspaceLaunchWrites, state.workspaceLaunchAttempts, writeIdentity);
    let operation = state.workspaceLaunchWriteResults.get(writeIdentity);
    if (!operation) {
      const workspaceId = `ws-demo-${state.workspaces.length + 1}`;
      const plan = input.packageId === "pro" ? "pro" : "basic";
      const workspaceKeyId = allocateGatewayKeyId(state);
      state.workspaceKeys.push(gatewayKey(workspaceKeyId, "Workspace Key", {}, {
        kind: "workspace",
        ownerAccountId: session.accountId,
        secret: `sk-fixture-workspace-${workspaceId}-${workspaceKeyId}`
      }));
      const created = {
        id: workspaceId, ownerAccountId: session.accountId, ownerUserId: session.consoleUserId, state: "running",
        createdAt: NOW, updatedAt: NOW, name: input.name,
        url: `https://workspace.example.invalid/w/${workspaceId}/`, packageId: plan,
        storageGb: plan === "pro" ? 100 : 10, autoRenew: false,
        priceVersion: "pilot-usd-2026-07-v1", currency: "USD",
        totalUsdMicros: plan === "pro" ? 240_080_000 : 52_580_000,
        periodStart: NOW, paidThrough: "2026-08-19T12:00:00Z", renewalStatus: "manual",
        workspaceApiKeyId: workspaceKeyId
      };
      state.workspaces.push(created);
      state.workspacePasswords.set(workspaceId, `fixture-${workspaceId}-workspace-password-v1`);
      state.workspaceCredentialVersions.set(workspaceId, 1);
      operation = {
        operationId: `launch-${workspaceId}`, status: "succeeded", phase: "completed",
        accountId: session.accountId, workspaceId, name: created.name, packageId: plan,
        sizeGb: created.storageGb, autoRenew: false, priceVersion: created.priceVersion,
        currency: "USD", totalChargeUsdMicros: created.totalUsdMicros,
        url: created.url, receiptId: `receipt-${workspaceId}`, createdAt: NOW, updatedAt: NOW
      };
      state.workspaceLaunchWriteResults.set(writeIdentity, operation);
      state.launches = [
        ...state.launches.filter((item) => item.accountId !== session.accountId || item.operationId !== operation.operationId),
        operation
      ];
    }
    if (state.faultInjection && !state.lostWorkspaceLaunchResponses.has(writeIdentity)) {
      state.lostWorkspaceLaunchResponses.add(writeIdentity);
      return route.abort("failed");
    }
    return fulfillJson(route, operation);
  }
  const launchMatch = path.match(/^\/api\/workspace-launches\/([^/]+)$/);
  if (launchMatch && method === "GET") {
    const launch = state.launches.find((item) => item.operationId === launchMatch[1] && item.accountId === session.accountId);
    return launch ? fulfillJson(route, launch) : fulfillJson(route, { error: "workspace_launch_not_found" }, 404);
  }
  const runtimeMatch = path.match(/^\/api\/workspaces\/([^/]+)\/runtime-status$/);
  if (runtimeMatch) {
    const workspaceId = runtimeMatch[1];
    const currentWorkspace = state.workspaces.find((item) => item.id === workspaceId && item.ownerAccountId === session.accountId);
    if (!currentWorkspace) return fulfillJson(route, { error: "workspace_not_found" }, 404);
    state.runtimeReads.set(workspaceId, (state.runtimeReads.get(workspaceId) || 0) + 1);
    return fulfillJson(route, source({
    workspaceId, status: "running", ready: true, runtimeId: `runtime-${workspaceId}`,
    url: currentWorkspace.url, serviceName: `runtime-${workspaceId}`, checks: [{ name: "ready_pod_uses_retained_pvc", ok: true }],
    access: {
      username: "opl", credentialStatus: "configured",
      credentialVersion: String(state.workspaceCredentialVersions.get(workspaceId) || 1)
    }
    }, "fabric"));
  }
  const credentialMatch = path.match(/^\/api\/workspaces\/([^/]+)\/runtime-credentials\/reveal$/);
  if (credentialMatch && method === "POST") {
    const workspaceId = credentialMatch[1];
    if (!state.workspaces.some((item) => item.id === workspaceId && item.ownerAccountId === session.accountId)) return fulfillJson(route, { error: "workspace_not_found" }, 404);
    const password = state.workspacePasswords.get(workspaceId);
    if (!password) return fulfillJson(route, { error: "runtime_credentials_unavailable" }, 503);
    state.workspaceSecretReads.set(workspaceId, (state.workspaceSecretReads.get(workspaceId) || 0) + 1);
    return fulfillJson(route, {
      workspaceId,
      access: {
        account: session.accountId, username: "opl", password, credentialStatus: "configured",
        credentialVersion: String(state.workspaceCredentialVersions.get(workspaceId) || 1)
      }
    });
  }
  const credentialRotationMatch = path.match(/^\/api\/workspaces\/([^/]+)\/runtime-credentials\/rotate$/);
  if (credentialRotationMatch && method === "POST") {
    const workspaceId = credentialRotationMatch[1];
    if (!state.workspaces.some((item) => item.id === workspaceId && item.ownerAccountId === session.accountId)) return fulfillJson(route, { error: "workspace_not_found" }, 404);
    const credentialVersion = (state.workspaceCredentialVersions.get(workspaceId) || 1) + 1;
    const password = `fixture-${workspaceId}-workspace-password-v${credentialVersion}`;
    state.workspacePasswords.set(workspaceId, password);
    state.workspaceCredentialVersions.set(workspaceId, credentialVersion);
    return fulfillJson(route, {
      workspaceId,
      access: {
        account: session.accountId, username: "opl", password, credentialStatus: "configured",
        credentialVersion: String(credentialVersion)
      },
      receiptId: `receipt-rotation-${workspaceId}`
    });
  }
  if (path === "/api/pricing/catalog") return fulfillJson(route, {
    priceVersion: "pilot-usd-2026-07-v1", billingUnit: "calendar_month", displayCurrency: "USD", walletCurrency: "USD", currency: "USD",
    packages: [
      { id: "basic", name: "Basic", available: state.basicPlanAvailable, cpu: 2, memoryGb: 4, diskGb: 10, server: "2c4g", price: { priceVersion: "pilot-usd-2026-07-v1", currency: "USD", chargeUsdMicros: 52_580_000 } },
      { id: "pro", name: "Pro", available: true, cpu: 8, memoryGb: 16, diskGb: 100, server: "8c16g", price: { priceVersion: "pilot-usd-2026-07-v1", currency: "USD", chargeUsdMicros: 240_080_000 } }
    ]
  });
  if (path === "/api/pricing/preview" && method === "POST") {
    const previewRequest = route.request().postDataJSON();
    const packageId = previewRequest?.packageId === "pro" ? "pro" : "basic";
    const computeChargeUsdMicros = packageId === "pro" ? 214_280_000 : 50_000_000;
    const storageChargeUsdMicros = packageId === "pro" ? 25_800_000 : 2_580_000;
    const sizeGb = packageId === "pro" ? 100 : 10;
    return fulfillJson(route, {
      resourceType: "workspace", packageId, priceVersion: "pilot-usd-2026-07-v1", currency: "USD",
      displayCurrency: "USD", billingUnit: "calendar_month",
      compute: {
        resourceType: "compute", packageId, priceVersion: "pilot-usd-2026-07-v1", currency: "USD",
        displayCurrency: "USD", billingUnit: "calendar_month", chargeUsdMicros: computeChargeUsdMicros
      },
      storage: {
        resourceType: "storage", packageId, priceVersion: "pilot-usd-2026-07-v1", currency: "USD",
        displayCurrency: "USD", billingUnit: "calendar_month", chargeUsdMicros: storageChargeUsdMicros, sizeGb
      },
      totalChargeUsdMicros: computeChargeUsdMicros + storageChargeUsdMicros
    });
  }
  if (path === "/api/billing/receipts") {
    const receipts = state.workspaces.some((item) => item.id === "ws-1" && item.ownerAccountId === session.accountId) ? [billingReceipt()] : [];
    return fulfillJson(route, source({ receipts, nextCursor: "", hasMore: false }, "ledger", receipts.length ? "available" : "empty"));
  }
  if (path === "/api/billing/receipts/receipt-fixture") {
    return state.workspaces.some((item) => item.id === "ws-1" && item.ownerAccountId === session.accountId)
      ? fulfillJson(route, source(billingReceipt(), "ledger"))
      : fulfillJson(route, { error: "receipt_not_found" }, 404);
  }
  if (path === "/api/support/tickets" && method === "GET") {
    const tickets = state.supportTickets.filter((item) => item.accountId === session.accountId);
    for (const ticketId of state.supportWriteResults.values()) {
      if (tickets.some((ticket) => ticket.id === ticketId)) state.supportReadbacks.add(ticketId);
    }
    return fulfillJson(route, { tickets });
  }
  if (path === "/api/support/tickets" && method === "POST") {
    const idempotencyKey = request.headers()["idempotency-key"] || "";
    if (!idempotencyKey) return fulfillJson(route, { error: "idempotency_key_required" }, 400);
    const input = request.postDataJSON();
    const writeIdentity = `${session.accountId}:${idempotencyKey}`;
    recordWriteAttempt(state.supportWrites, state.supportAttempts, writeIdentity);
    let ticket = state.supportTickets.find((item) => item.id === state.supportWriteResults.get(writeIdentity));
    if (!ticket) {
      ticket = {
        id: `support-${state.supportTickets.length + 1}`, externalSystem: input.externalSystem || "support",
        externalTicketId: input.externalTicketId, externalUrl: input.externalUrl || "", accountId: session.accountId,
        ...(input.workspaceId ? { workspaceId: input.workspaceId } : {}), resourceIds: input.resourceIds || [],
        ...(input.operationId ? { operationId: input.operationId } : {}), title: input.title,
        category: "support", priority: "normal", status: "open", createdAt: NOW, updatedAt: NOW,
        messages: input.description ? [{ author: "demo", text: input.description, createdAt: NOW }] : []
      };
      state.supportTickets.push(ticket);
      state.supportWriteResults.set(writeIdentity, ticket.id);
    }
    if (state.faultInjection && !state.lostSupportResponses.has(writeIdentity)) {
      state.lostSupportResponses.add(writeIdentity);
      return route.abort("failed");
    }
    return fulfillJson(route, ticket);
  }
  if (path === "/api/announcements" && method === "GET") {
    const data = { items: state.announcements, total: state.announcements.length, page: 1, pageSize: 20 };
    return fulfillJson(route, source(data, "control-plane", state.announcements.length ? "available" : "empty"));
  }
  const announcementReadMatch = path.match(/^\/api\/announcements\/([^/]+)\/read$/);
  if (announcementReadMatch && method === "POST") {
    const announcement = state.announcements.find((item) => item.id === announcementReadMatch[1]);
    if (!announcement) return fulfillJson(route, { error: "announcement_not_found" }, 404);
    announcement.read = true;
    return fulfillJson(route, { announcementId: announcement.id, readAt: NOW });
  }
  if (path === "/api/gateway/wallet") return fulfillJson(route, source({ userId: session.sub2apiUserId, currency: "USD", usdMicros: "500000000", status: "active" }, "sub2api"));
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
    if (!findGatewayKey(state, keyUsageMatch[1], session.accountId)) return fulfillJson(route, { error: "gateway_key_not_found" }, 404);
    const page = Number(url.searchParams.get("page"));
    const pageSize = Number(url.searchParams.get("pageSize"));
    const items = [{
      apiKeyId: keyUsageMatch[1], requestId: "request-fixture", createdAt: NOW,
      model: "gpt-5-mini", inboundEndpoint: "/v1/responses", requestType: "sync",
      inputTokens: 120, outputTokens: 36, cacheCreationTokens: 24, cacheReadTokens: 8,
      actualCostUsdMicros: 25_000, durationMs: 860, firstTokenMs: 140
    }, {
      apiKeyId: keyUsageMatch[1], requestId: "request-null-latency", createdAt: "2026-07-29T22:00:00Z",
      model: "gpt-5-mini", inboundEndpoint: "/v1/responses", requestType: "sync",
      inputTokens: 48, outputTokens: 12, cacheCreationTokens: 0, cacheReadTokens: 0,
      actualCostUsdMicros: 9_000, durationMs: null, firstTokenMs: null
    }];
    return fulfillJson(route, source({ items: page === 1 ? items : [], total: 2, page, pageSize, pages: 1 }, "sub2api"));
  }
  const keyUsageSummaryMatch = path.match(/^\/api\/gateway\/keys\/(\d+)\/usage-summary$/);
  if (keyUsageSummaryMatch && method === "GET") {
    if (!findGatewayKey(state, keyUsageSummaryMatch[1], session.accountId)) return fulfillJson(route, { error: "gateway_key_not_found" }, 404);
    return fulfillJson(route, source({
      totalRequests: 2, totalInputTokens: 168, totalOutputTokens: 48, totalTokens: 248,
      totalActualCostUsdMicros: 34_000
    }, "sub2api"));
  }

  if (path === "/api/gateway/keys" && method === "GET") {
    if (state.sourceState === "error") return fulfillJson(route, { error: "upstream_unavailable" }, 503);
    const keys = state.sourceState === "available"
      ? state.keys.filter((item) => item.ownerAccountId === session.accountId).map(gatewayKeyView)
      : [];
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
    const writeIdentity = `${session.accountId}:${operation}`;
    let key = state.keys.find((item) => item.id === state.gatewayWriteResults.get(writeIdentity) && item.ownerAccountId === session.accountId);
    if (!key) {
      const keyId = allocateGatewayKeyId(state);
      const secret = state.keys.some((item) => item.secret === GENERAL_KEY) ? `${GENERAL_KEY}-${keyId}` : GENERAL_KEY;
      key = gatewayKey(keyId, input.name, input, { ownerAccountId: session.accountId, secret });
      state.keys.push(key);
      state.gatewayWriteResults.set(writeIdentity, keyId);
    }
    if (state.faultInjection && !state.lostGatewayResponses.has(operation)) {
      state.lostGatewayResponses.add(operation);
      return route.abort("failed");
    }
    return fulfillJson(route, source(gatewayKeyView(key), "sub2api"));
  }
  const keyMatch = path.match(/^\/api\/gateway\/keys\/(\d+)$/);
  if (keyMatch && method === "GET") {
    const key = state.keys.find((item) => item.id === keyMatch[1] && item.ownerAccountId === session.accountId);
    if (key) return fulfillJson(route, source(gatewayKeyView(key), "sub2api"));
    if (keyMatch[1] === "12" && state.gatewayActions.at(-1) === "delete") {
      state.expectedConsole404s.add(path);
    }
    return fulfillJson(route, { error: "gateway_key_not_found" }, 404);
  }
  if (keyMatch && method === "PATCH") {
    const operation = request.headers()["idempotency-key"] || "";
    const key = state.keys.find((item) => item.id === keyMatch[1] && item.ownerAccountId === session.accountId);
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
    return fulfillJson(route, source(gatewayKeyView(key), "sub2api"));
  }
  if (keyMatch && method === "DELETE") {
    const operation = request.headers()["idempotency-key"] || "";
    if (!operation) return fulfillJson(route, { error: "idempotency_key_required" }, 400);
    const index = state.keys.findIndex((item) => item.id === keyMatch[1] && item.ownerAccountId === session.accountId);
    if (index < 0) return fulfillJson(route, { error: "gateway_key_not_found" }, 404);
    state.keys.splice(index, 1);
    state.gatewayMutationWrites.add(operation);
    state.gatewayActions.push("delete");
    return fulfillJson(route, source({ status: "deleted" }, "sub2api"));
  }
  const revealMatch = path.match(/^\/api\/gateway\/keys\/(\d+)\/reveal$/);
  if (revealMatch && method === "POST") {
    const key = findGatewayKey(state, revealMatch[1], session.accountId);
    if (!key) return fulfillJson(route, { error: "gateway_key_not_found" }, 404);
    state.revealCalls.set(key.id, (state.revealCalls.get(key.id) || 0) + 1);
    return fulfillJson(route, source({
      id: key.id, name: key.name, status: key.status, value: key.secret
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
  if (path === "/api/operator/accounts" && method === "GET") {
    const page = Number(url.searchParams.get("page") || 1);
    const pageSize = Number(url.searchParams.get("pageSize") || 20);
    const start = (page - 1) * pageSize;
    const items = state.operatorAccounts.slice(start, start + pageSize);
    for (const operation of state.operatorProvisionWriteResults.values()) {
      if (items.some((account) => account.accountId === operation.accountId)) state.operatorProvisionReadbacks.add(operation.accountId);
    }
    return fulfillJson(route, source({
      items, total: state.operatorAccounts.length, page, pageSize
    }));
  }
  if (path === "/api/operator/accounts" && method === "POST") {
    const idempotencyKey = request.headers()["idempotency-key"] || "";
    if (!idempotencyKey) return fulfillJson(route, { error: "idempotency_key_required" }, 400);
    const input = request.postDataJSON();
    const writeIdentity = `${session.accountId}:${idempotencyKey}`;
    recordWriteAttempt(state.operatorProvisionWrites, state.operatorProvisionAttempts, writeIdentity);
    let operation = state.operatorProvisionWriteResults.get(writeIdentity);
    if (!operation) {
      const accountId = `acct-${state.operatorAccounts.length + 1}`;
      const userId = String(20 + state.operatorAccounts.length);
      const account = operatorAccount(accountId, "active", {
        email: String(input.email || "").trim().toLowerCase(),
        consoleUserId: `user-${state.operatorAccounts.length + 1}`,
        sub2apiUserId: userId
      });
      state.operatorAccounts.push(account);
      operation = { operationId: `account-provision-${accountId}`, accountId, status: "succeeded", phase: "completed", createdAt: NOW, updatedAt: NOW };
      state.operatorProvisionWriteResults.set(writeIdentity, operation);
    }
    if (state.faultInjection && !state.lostOperatorProvisionResponses.has(writeIdentity)) {
      state.lostOperatorProvisionResponses.add(writeIdentity);
      return route.abort("failed");
    }
    return fulfillJson(route, operation);
  }
  if (path === "/api/operator/workspaces") {
    if (state.sourceState === "error") return fulfillJson(route, { error: "upstream_unavailable" }, 503);
    const items = state.sourceState === "available" ? [operatorWorkspace()] : [];
    return fulfillJson(route, sourceForState(state, { items, total: items.length, page: 1, pageSize: 20 }, "control-plane+fabric+sub2api"));
  }
  if (path === "/api/operator/workspaces/ws-1") return fulfillJson(route, source(operatorWorkspace(), "control-plane+fabric+ledger+sub2api"));
  if (path === "/api/operator/reconciliation") return fulfillJson(route, source({ items: [{
    id: "review-recovery-fixture", resourceType: "workspace", status: "manual_review", accountId: "acct-operator",
    billingOperationId: "launch-recovery-fixture", phase: "manual_review", errorCode: "provider_unknown",
    progressionOwner: "control_plane_recovery_plan", allowedActions: ["diagnose_workspace_recovery_plan"], operationRef: "launch-recovery-fixture", receiptRef: "receipt-recovery-fixture"
  }], total: 1, page: 1, pageSize: 20 }, "control-plane"));
  const recoveryPlanMatch = path.match(/^\/api\/operator\/workspace-launches\/([^/]+)\/recovery-plan(?:\/(diagnose|validate|execute))?$/);
  if (recoveryPlanMatch && recoveryPlanMatch[1] === "launch-recovery-fixture") {
    const action = recoveryPlanMatch[2] || "read";
    if (action === "read" && method === "GET") return fulfillJson(route, recoveryPlan([...state.recoveryPlanExecutions.values()][0] || ""));
    const input = request.postDataJSON();
    if (action === "diagnose" && method === "POST") {
      if (JSON.stringify(input) !== JSON.stringify({ accountId: "acct-operator" })) return fulfillJson(route, { error: "recovery_plan_diagnose_shape_invalid" }, 400);
      state.recoveryPlanDiagnoses += 1;
      return fulfillJson(route, recoveryPlan());
    }
    if (action === "validate" && method === "POST") {
      if (JSON.stringify(input) !== JSON.stringify({ planId: "recovery-plan-fixture", planDigest: "sha256:recovery-plan-fixture" })) return fulfillJson(route, { error: "recovery_plan_validate_shape_invalid" }, 400);
      state.recoveryPlanValidations += 1;
      return fulfillJson(route, recoveryPlan());
    }
    if (action === "execute" && method === "POST") {
      if (JSON.stringify(input) !== JSON.stringify({ planId: "recovery-plan-fixture", planDigest: "sha256:recovery-plan-fixture", decision: "continue", confirmation: "CONTINUE_RECOVERY_PLAN" })) return fulfillJson(route, { error: "recovery_plan_execute_shape_invalid" }, 400);
      const identity = request.headers()["idempotency-key"] || "";
      if (!identity) return fulfillJson(route, { error: "idempotency_key_required" }, 400);
      state.recoveryPlanExecuteAttempts.set(identity, (state.recoveryPlanExecuteAttempts.get(identity) || 0) + 1);
      if (!state.recoveryPlanExecutions.has(identity)) state.recoveryPlanExecutions.set(identity, "execution-recovery-fixture");
      return fulfillJson(route, recoveryPlan(state.recoveryPlanExecutions.get(identity)));
    }
  }
  if (path === "/api/operator/announcements" && method === "GET") {
    const data = { items: state.announcements, total: state.announcements.length, page: 1, pageSize: 20 };
    for (const announcementId of state.announcementCreateWriteResults.values()) {
      const announcement = state.announcements.find((item) => item.id === announcementId);
      if (!announcement) continue;
      const statuses = state.announcementReadbackStatuses.get(announcementId) || new Set();
      statuses.add(announcement.status);
      state.announcementReadbackStatuses.set(announcementId, statuses);
    }
    return fulfillJson(route, source(data, "control-plane", state.announcements.length ? "available" : "empty"));
  }
  if (path === "/api/operator/announcements" && method === "POST") {
    const idempotencyKey = request.headers()["idempotency-key"] || "";
    if (!idempotencyKey) return fulfillJson(route, { error: "idempotency_key_required" }, 400);
    const input = request.postDataJSON();
    const writeIdentity = `${session.accountId}:${idempotencyKey}`;
    recordWriteAttempt(state.announcementCreateWrites, state.announcementCreateAttempts, writeIdentity);
    let announcement = state.announcements.find((item) => item.id === state.announcementCreateWriteResults.get(writeIdentity));
    if (!announcement) {
      announcement = {
        id: `announcement-${state.announcements.length + 1}`, title: input.title, body: input.body,
        status: "draft", ...(input.startsAt ? { startsAt: input.startsAt } : {}), ...(input.endsAt ? { endsAt: input.endsAt } : {}),
        createdAt: NOW, updatedAt: NOW, read: false
      };
      state.announcements.push(announcement);
      state.announcementCreateWriteResults.set(writeIdentity, announcement.id);
    }
    if (state.faultInjection && !state.lostAnnouncementCreateResponses.has(writeIdentity)) {
      state.lostAnnouncementCreateResponses.add(writeIdentity);
      return route.abort("failed");
    }
    return fulfillJson(route, announcement);
  }
  const announcementPublishMatch = path.match(/^\/api\/operator\/announcements\/([^/]+)\/publish$/);
  if (announcementPublishMatch && method === "POST") {
    const idempotencyKey = request.headers()["idempotency-key"] || "";
    if (!idempotencyKey) return fulfillJson(route, { error: "idempotency_key_required" }, 400);
    const input = request.postDataJSON();
    const announcement = state.announcements.find((item) => item.id === announcementPublishMatch[1]);
    if (!announcement) return fulfillJson(route, { error: "announcement_not_found" }, 404);
    const writeIdentity = `${session.accountId}:${idempotencyKey}`;
    recordWriteAttempt(state.announcementPublishWrites, state.announcementPublishAttempts, writeIdentity);
    let result = state.announcementPublishWriteResults.get(writeIdentity);
    if (!result) {
      announcement.status = "published";
      announcement.startsAt = input.startsAt || NOW;
      announcement.endsAt = input.endsAt || announcement.endsAt;
      announcement.publishedAt = NOW;
      announcement.updatedAt = NOW;
      result = { ...announcement };
      state.announcementPublishWriteResults.set(writeIdentity, result);
    }
    if (state.faultInjection && !state.lostAnnouncementPublishResponses.has(writeIdentity)) {
      state.lostAnnouncementPublishResponses.add(writeIdentity);
      return route.abort("failed");
    }
    return fulfillJson(route, result);
  }
  const announcementWithdrawMatch = path.match(/^\/api\/operator\/announcements\/([^/]+)\/withdraw$/);
  if (announcementWithdrawMatch && method === "POST") {
    const idempotencyKey = request.headers()["idempotency-key"] || "";
    if (!idempotencyKey) return fulfillJson(route, { error: "idempotency_key_required" }, 400);
    const announcement = state.announcements.find((item) => item.id === announcementWithdrawMatch[1]);
    if (!announcement) return fulfillJson(route, { error: "announcement_not_found" }, 404);
    const writeIdentity = `${session.accountId}:${idempotencyKey}`;
    recordWriteAttempt(state.announcementWithdrawWrites, state.announcementWithdrawAttempts, writeIdentity);
    let result = state.announcementWithdrawWriteResults.get(writeIdentity);
    if (!result) {
      announcement.status = "withdrawn";
      announcement.updatedAt = NOW;
      result = { ...announcement };
      state.announcementWithdrawWriteResults.set(writeIdentity, result);
    }
    if (state.faultInjection && !state.lostAnnouncementWithdrawResponses.has(writeIdentity)) {
      state.lostAnnouncementWithdrawResponses.add(writeIdentity);
      return route.abort("failed");
    }
    return fulfillJson(route, result);
  }
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
    if (state.faultInjection && !state.lostWalletResponses.has(operation)) {
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

async function installFixturePage(page, state, serverOrigin) {
  const fixturePort = new URL(serverOrigin).port;
  page.on("pageerror", (error) => state.pageErrors.push(error.stack || error.message));
  page.on("console", (message) => {
    if (message.type() !== "error") return;
    const text = message.text();
    const location = message.location().url;
    const diagnosticText = location ? `${text} @ ${location}` : text;
    const locationPath = location ? new URL(location).pathname : "";
    const expected404 = /^Failed to load resource: the server responded with a status of 404/.test(text)
      && state.expectedConsole404s.delete(locationPath);
    if (/^Failed to load resource: (?:net::ERR_FAILED|the server responded with a status of 503)/.test(text) || expected404) {
      state.expectedNetworkConsoleErrors.push(diagnosticText);
    } else {
      state.consoleErrors.push(diagnosticText);
    }
  });
  page.on("dialog", (dialog) => {
    state.dialogMessages.push(dialog.message());
    void dialog.accept();
  });
  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    const local = url.hostname === "127.0.0.1" && url.port === fixturePort;
    if (!local) {
      state.externalRequests += 1;
      return route.abort("blockedbyclient");
    }
    if (url.pathname.startsWith("/api/")) return apiFixture(route, state);
    return route.continue();
  });
}

async function waitForText(page, text) {
  const locator = page.getByText(text, { exact: false }).filter({ visible: true }).first();
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

async function assertWorkspaceLaunchLayout(page, viewportName) {
  const layout = await page.locator(".workspace-launch-layout").evaluate((element) => {
    const summary = element.querySelector(".workspace-order-summary");
    const style = getComputedStyle(element);
    return {
      columns: style.gridTemplateColumns.split(" ").filter(Boolean).length,
      summaryPosition: summary ? getComputedStyle(summary).position : "missing"
    };
  });
  const expectedColumns = viewportName === "desktop" ? 2 : 1;
  const expectedSummaryPosition = viewportName === "desktop" ? "sticky" : "static";
  if (layout.columns !== expectedColumns || layout.summaryPosition !== expectedSummaryPosition) {
    throw new Error(`console_browser_workspace_launch_layout:${JSON.stringify({ viewportName, expectedColumns, expectedSummaryPosition, ...layout })}`);
  }
}

async function assertWorkspacePlanRadios(page) {
  const radios = await page.locator(".workspace-plan-option [role='radio']").evaluateAll((elements) => elements.map((element) => {
    const rect = element.getBoundingClientRect();
    const style = getComputedStyle(element);
    return { width: Math.round(rect.width), height: Math.round(rect.height), borderRadius: Math.round(Number.parseFloat(style.borderRadius)), opacity: style.opacity, visibility: style.visibility };
  }));
  if (radios.length !== 2 || radios.some((radio) => radio.width < 16 || radio.height < 16 || radio.borderRadius < 8 || radio.opacity === "0" || radio.visibility !== "visible")) {
    throw new Error(`console_browser_workspace_plan_radio_missing:${JSON.stringify(radios)}`);
  }
}

async function assertWorkspaceLaunchConfirmation(page, viewportName) {
  await page.waitForFunction(() => window.scrollY === 0);
  const diagnostic = await page.locator(".launch-confirm-check").evaluate((element) => {
    const checkbox = element.querySelector(".console-checkbox");
    const control = checkbox?.querySelector(":scope > div");
    const label = control?.querySelector("label");
    const elementRect = element.getBoundingClientRect();
    const controlRect = control?.getBoundingClientRect();
    const labelRect = label?.getBoundingClientRect();
    return {
      containerWidth: Math.round(elementRect.width),
      controlWidth: Math.round(controlRect?.width || 0),
      labelHeight: Math.round(labelRect?.height || 0),
      labelWidth: Math.round(labelRect?.width || 0)
    };
  });
  const minimumLabelWidth = viewportName === "desktop" ? 300 : 220;
  if (diagnostic.controlWidth < diagnostic.containerWidth - 34 || diagnostic.labelWidth < minimumLabelWidth || diagnostic.labelHeight > 44) {
    throw new Error(`console_browser_workspace_confirmation_layout:${JSON.stringify({ viewportName, minimumLabelWidth, ...diagnostic })}`);
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

  const secretStatus = page.locator(".keys-secret");
  await secretStatus.getByRole("button", { name: "复制", exact: true }).click();
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
  await keyRow.locator(".console-select").getByRole("button").click();
  await page.getByRole("option", { name: "priority", exact: true }).click();
  await waitForText(page, "分组已更新");
  keyRow = page.getByRole("row").filter({ hasText: "Browser edited key" }).first();
  await keyRow.getByRole("button", { name: "停用", exact: true }).click();
  await waitForText(page, "API Key 已停用");
  keyRow = page.getByRole("row").filter({ hasText: "Browser edited key" }).first();
  await keyRow.getByRole("button", { name: "启用", exact: true }).click();
  await waitForText(page, "API Key 已启用");
  keyRow = page.getByRole("row").filter({ hasText: "Browser edited key" }).first();
  await keyRow.getByRole("button", { name: "重置配额用量", exact: true }).click();
  await waitForText(page, "配额用量已重置");
  keyRow = page.getByRole("row").filter({ hasText: "Browser edited key" }).first();
  await keyRow.getByRole("button", { name: "重置消费限额用量", exact: true }).click();
  await waitForText(page, "消费限额用量已重置");
  keyRow = page.getByRole("row").filter({ hasText: "Browser edited key" }).first();
  await keyRow.getByRole("button", { name: "删除", exact: true }).click();
  const deleteDialog = page.getByRole("dialog", { name: "删除 API Key" });
  await deleteDialog.getByRole("button", { name: "删除", exact: true }).click();
  await waitForText(page, "API Key 已删除");
  await waitForText(page, "暂无数据");

  if (state.keys.length !== 0 || state.emptyGatewayReadbacks < 1) throw new Error("console_browser_gateway_empty_readback_failed");
}

async function exerciseWalletAdjustment(page, state, screenshotDir, viewportName, { submit = false } = {}) {
  const accountSurface = (viewportName === "desktop"
    ? page.locator(".operator-account-table tbody tr:visible")
    : page.locator(".operator-account-mobile-card:visible")).filter({ hasText: "pilot@example.com" });
  await accountSurface.getByRole("button", { name: "余额操作" }).click();
  const dialog = page.getByRole("dialog", { name: "余额操作" });
  await dialog.getByLabel("再次确认 Account ID").pressSequentially("acct-1");
  await dialog.getByLabel("金额（USD）").pressSequentially("5");
  await dialog.getByLabel("业务原因").pressSequentially("browser retry");
  await assertNoViewportOverflow(page);
  const footerDiagnostic = await dialog.locator(".console-modal__footer").evaluate((footer) => {
    const footerRect = footer.getBoundingClientRect();
    const actions = [...footer.children].map((element) => {
      const rect = element.getBoundingClientRect();
      return { left: rect.left, right: rect.right, width: rect.width };
    });
    return { left: footerRect.left, right: footerRect.right, width: footerRect.width, actions };
  });
  if (footerDiagnostic.actions.some((action) => action.left < footerDiagnostic.left - 1 || action.right > footerDiagnostic.right + 1)) {
    throw new Error(`console_browser_modal_footer_overflow:${JSON.stringify(footerDiagnostic)}`);
  }
  await captureFixtureScreenshot(page, state, screenshotDir, "admin-balance-operation", viewportName);
  if (!submit) {
    await dialog.getByRole("button", { name: "关闭", exact: true }).last().click();
    return;
  }
  const submitButton = dialog.getByRole("button", { name: "确认操作" });
  await submitButton.click();
  await waitForText(page, "结果待确认");
  await submitButton.click();
  await waitForText(page, "余额操作已提交");
}

async function openWorkspaceFromList(page, workspaceName) {
  const workspaceList = page.locator(".workspace-list");
  await workspaceList.locator(".workspace-list-row").filter({ hasText: workspaceName }).click();
  await page.waitForURL(new RegExp(`/console/workspaces/ws-[12]$`));
  await waitForText(page, "Workspace URL");
  await page.locator(".workspace-access-panel").getByText("opl", { exact: true }).waitFor({ state: "visible" });
}

async function assertUsageRecordFields(page, viewportName) {
  const expectedHeaders = ["模型 / 端点", "Token", "费用", "延迟", "时间", "请求 ID"];
  const surface = viewportName === "desktop"
    ? page.locator(".request-table-desktop")
    : page.locator(".request-list-mobile");
  if (viewportName === "desktop") {
    await surface.getByText("request-fixture", { exact: true }).waitFor({ state: "visible" });
    const headers = (await surface.locator("th").allTextContents()).map((label) => label.trim());
    if (JSON.stringify(headers) !== JSON.stringify(expectedHeaders)) {
      throw new Error(`console_browser_request_fields:${JSON.stringify(headers)}`);
    }
  } else {
    const row = surface.getByRole("listitem").filter({ hasText: "request-fixture" });
    const labels = (await row.locator("dt").allTextContents()).map((label) => label.trim());
    if (JSON.stringify(labels) !== JSON.stringify(["Token", "费用", "延迟", "时间"])) {
      throw new Error(`console_browser_mobile_request_fields:${JSON.stringify(labels)}`);
    }
  }

  const completeRow = viewportName === "desktop"
    ? surface.locator("tbody tr").filter({ hasText: "request-fixture" })
    : surface.getByRole("listitem").filter({ hasText: "request-fixture" });
  for (const label of ["输入", "输出", "缓存读取", "缓存写入", "首字", "总耗时"]) {
    await completeRow.getByText(label, { exact: true }).waitFor({ state: "visible" });
  }
  for (const value of ["120", "36", "8", "24", "140 ms", "860 ms", "$0.03"]) {
    await completeRow.getByText(value, { exact: true }).waitFor({ state: "visible" });
  }
  if (await completeRow.locator("time").count() !== 1 || await completeRow.getByRole("button", { name: "复制请求 ID" }).count() !== 1) {
    throw new Error("console_browser_request_time_or_copy_missing");
  }

  const nullLatencyRow = viewportName === "desktop"
    ? surface.locator("tbody tr").filter({ hasText: "request-null-latency" })
    : surface.getByRole("listitem").filter({ hasText: "request-null-latency" });
  await nullLatencyRow.waitFor({ state: "visible" });
  const nullLatencyValues = (await nullLatencyRow.locator(".usage-latency-stack strong").allTextContents()).map((value) => value.trim());
  if (JSON.stringify(nullLatencyValues) !== JSON.stringify(["-", "-"]) || await nullLatencyRow.getByText("0 ms", { exact: true }).count()) {
    throw new Error(`console_browser_null_latency:${JSON.stringify(nullLatencyValues)}`);
  }
  for (const label of ["缓存读取", "缓存写入"]) {
    if (await nullLatencyRow.getByText(label, { exact: true }).count()) {
      throw new Error(`console_browser_zero_cache_visible:${label}`);
    }
  }
}

async function exerciseHighRiskWriteFlows(browser, serverOrigin) {
  const state = createConsoleFixtureState();
  const context = await browser.newContext({ viewport: VIEWPORTS.desktop, permissions: ["clipboard-read", "clipboard-write"] });
  const page = await context.newPage();
  await installFixturePage(page, state, serverOrigin);
  const unknownWriteMessage = "结果待确认，请刷新操作状态，不要重复提交";

  try {
    authenticateFixtureSession(state, "customer");
    await page.goto(`${serverOrigin}/console/workspaces/new`, { waitUntil: "networkidle" });
    await waitForText(page, "核对开通信息");
    await waitForText(page, "$52.58");
    await page.getByLabel("Workspace 名称").fill("Browser retry Workspace");
    await page.getByRole("button", { name: "核对开通信息", exact: true }).click();
    await page.getByRole("heading", { name: "确认开通信息", exact: true }).waitFor({ state: "visible" });
    await page.getByRole("checkbox", { name: "我确认一次性预付 Workspace 月度总额并开通", exact: true }).click();
    const launchButton = page.getByRole("button", { name: "确认预付并开通", exact: true });
    await launchButton.click();
    await waitForText(page, "请求失败，请重试");
    await launchButton.click();
    await page.waitForURL(/\/console\/workspaces\/ws-demo-\d+$/);
    const workspaceOperation = [...state.workspaceLaunchWriteResults.values()][0];
    if (!workspaceOperation?.workspaceId) throw new Error("console_browser_workspace_launch_result_missing");
    await waitForText(page, "Browser retry Workspace");
    await waitForText(page, `https://workspace.example.invalid/w/${workspaceOperation.workspaceId}/`);

    authenticateFixtureSession(state, "operator");
    await page.goto(`${serverOrigin}/admin/accounts`, { waitUntil: "networkidle" });
    await waitForText(page, "客户与计费账户");
    await page.getByRole("button", { name: "开通用户", exact: true }).first().click();
    const provisionDialog = page.getByRole("dialog", { name: "开通用户" });
    await provisionDialog.getByLabel("登录邮箱").fill("browser-provision@example.com");
    await provisionDialog.getByLabel("初始密码").fill("browser-provision-password");
    await provisionDialog.getByLabel("姓名").fill("Browser Provision");
    const provisionButton = provisionDialog.getByRole("button", { name: "开通用户", exact: true });
    await provisionButton.click();
    await waitForText(page, unknownWriteMessage);
    await provisionButton.click();
    await waitForText(provisionDialog, "账户映射已完成权威读回");
    await waitForText(provisionDialog, "browser-provision@example.com");

    await page.goto(`${serverOrigin}/admin/announcements`, { waitUntil: "networkidle" });
    await waitForText(page, "公告管理");
    await page.getByRole("button", { name: "新建草稿", exact: true }).click();
    const announcementDialog = page.getByRole("dialog", { name: "新建公告草稿" });
    await announcementDialog.getByLabel("标题").fill("Browser lifecycle announcement");
    await announcementDialog.getByLabel("正文").fill("Create, publish, and withdraw through the operator UI.");
    const saveAnnouncementButton = announcementDialog.getByRole("button", { name: "保存草稿", exact: true });
    await saveAnnouncementButton.click();
    await waitForText(page, unknownWriteMessage);
    await saveAnnouncementButton.click();
    const announcement = page.locator(".announcement-item").filter({ hasText: "Browser lifecycle announcement" });
    await announcement.getByText("草稿", { exact: true }).waitFor({ state: "visible" });
    const publishButton = announcement.getByRole("button", { name: "发布", exact: true });
    await publishButton.click();
    await waitForText(page, unknownWriteMessage);
    await publishButton.click();
    await announcement.getByText("已发布", { exact: true }).waitFor({ state: "visible" });
    const withdrawButton = announcement.getByRole("button", { name: "撤下", exact: true });
    await withdrawButton.click();
    await waitForText(page, unknownWriteMessage);
    await withdrawButton.click();
    await announcement.getByText("已撤下", { exact: true }).waitFor({ state: "visible" });

    await page.getByRole("button", { name: "Support", exact: true }).click();
    const supportSlide = page.getByRole("complementary", { name: "Support" });
    await waitForText(supportSlide, "暂无外部工单映射");
    await supportSlide.getByRole("button", { name: "新增映射", exact: true }).click();
    await supportSlide.getByLabel("外部工单号").fill("SUP-2026-001");
    await supportSlide.getByLabel("标题").fill("Browser support mapping");
    const saveSupportButton = supportSlide.getByRole("button", { name: "保存外部映射", exact: true });
    await saveSupportButton.click();
    await waitForText(page, unknownWriteMessage);
    await saveSupportButton.click();
    await waitForText(supportSlide, "Browser support mapping");
    await waitForText(supportSlide, "SUP-2026-001");
  } finally {
    await context.close();
  }

  return state;
}

function highRiskWriteEvidence(state) {
  const writeContracts = [
    ["workspace_launch", state.workspaceLaunchWrites, state.workspaceLaunchAttempts, state.workspaceLaunchWriteResults, state.lostWorkspaceLaunchResponses],
    ["operator_provision", state.operatorProvisionWrites, state.operatorProvisionAttempts, state.operatorProvisionWriteResults, state.lostOperatorProvisionResponses],
    ["announcement_create", state.announcementCreateWrites, state.announcementCreateAttempts, state.announcementCreateWriteResults, state.lostAnnouncementCreateResponses],
    ["announcement_publish", state.announcementPublishWrites, state.announcementPublishAttempts, state.announcementPublishWriteResults, state.lostAnnouncementPublishResponses],
    ["announcement_withdraw", state.announcementWithdrawWrites, state.announcementWithdrawAttempts, state.announcementWithdrawWriteResults, state.lostAnnouncementWithdrawResponses],
    ["support_mapping", state.supportWrites, state.supportAttempts, state.supportWriteResults, state.lostSupportResponses]
  ];
  for (const [name, writes, attempts, results, lostResponses] of writeContracts) {
    const identity = [...writes][0] || "";
    const idempotencyKey = identity.slice(identity.indexOf(":") + 1);
    if (writes.size !== 1 || attempts.size !== 1 || [...attempts.values()][0] !== 2 || results.size !== 1 || lostResponses.size !== 1 || !idempotencyKey) {
      throw new Error(`console_browser_high_risk_idempotency_failed:${name}`);
    }
  }
  if (state.unexpectedApi.length) throw new Error(`console_browser_high_risk_unexpected_api:${state.unexpectedApi.join(",")}`);
  if (state.pageErrors.length) throw new Error(`console_browser_high_risk_page_error:${state.pageErrors.join(",")}`);
  if (state.consoleErrors.length) throw new Error(`console_browser_high_risk_console_error:${state.consoleErrors.join(",")}`);
  if (state.externalRequests !== 0) throw new Error(`console_browser_high_risk_external_request:${state.externalRequests}`);

  const workspaceOperation = [...state.workspaceLaunchWriteResults.values()][0];
  const operatorOperation = [...state.operatorProvisionWriteResults.values()][0];
  const announcementId = [...state.announcementCreateWriteResults.values()][0];
  const supportTicketId = [...state.supportWriteResults.values()][0];
  const announcementStatuses = state.announcementReadbackStatuses.get(announcementId) || new Set();
  const workspaceLaunchAuthoritativeReadback = Boolean(workspaceOperation?.workspaceId
    && state.workspaceLaunchReadbacks.has(workspaceOperation.workspaceId)
    && state.runtimeReads.has(workspaceOperation.workspaceId));
  const operatorProvisionAuthoritativeReadback = Boolean(operatorOperation?.accountId
    && state.operatorProvisionReadbacks.has(operatorOperation.accountId));
  const announcementLifecycle = ["draft", "published", "withdrawn"].every((status) => announcementStatuses.has(status))
    && state.announcements.find((item) => item.id === announcementId)?.status === "withdrawn";
  const supportMappingReadback = Boolean(supportTicketId && state.supportReadbacks.has(supportTicketId));
  if (!workspaceLaunchAuthoritativeReadback || !operatorProvisionAuthoritativeReadback || !announcementLifecycle || !supportMappingReadback) {
    throw new Error("console_browser_high_risk_authoritative_readback_failed");
  }

  return {
    highRiskWrites: {
      workspaceLaunch: state.workspaceLaunchWrites.size,
      operatorProvision: state.operatorProvisionWrites.size,
      announcementCreate: state.announcementCreateWrites.size,
      announcementPublish: state.announcementPublishWrites.size,
      announcementWithdraw: state.announcementWithdrawWrites.size,
      supportMapping: state.supportWrites.size
    },
    workspaceLaunchAuthoritativeReadback,
    operatorProvisionAuthoritativeReadback,
    announcementLifecycle,
    supportMappingReadback
  };
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
  const state = createConsoleFixtureState();
  try {
    browser = await browserFactory();
    for (const [name, viewport] of Object.entries(VIEWPORTS)) {
      state.basicPlanAvailable = name !== "mobile";
      const context = await browser.newContext({ viewport, permissions: ["clipboard-read", "clipboard-write"] });
      const page = await context.newPage();
      await installFixturePage(page, state, server.origin);

      state.role = "customer";
      authenticateFixtureSession(state, "customer");
      state.sourceState = "available";
      await page.goto(`${server.origin}/`, { waitUntil: "networkidle" });
      await waitForText(page, "让你的 One Person Lab 在云端继续工作");
      await waitForText(page, "账户由管理员开通");
      const logoLoaded = await page.locator(".public-nav").getByAltText("OPL Cloud").evaluate((image) => image.complete && image.naturalWidth > 0);
      if (!logoLoaded) throw new Error("console_browser_logo_missing");
      await page.goto(`${server.origin}/login`, { waitUntil: "networkidle" });
      await waitForText(page, "登录 OPL Cloud");
      state.customerRoutes.add("/login");
      await page.getByLabel("邮箱").fill(CONSOLE_DEMO_CREDENTIALS.customer.email);
      await page.getByLabel("密码").fill(CONSOLE_DEMO_CREDENTIALS.customer.password);
      await page.getByRole("button", { name: "登录", exact: true }).click();
      await page.waitForURL(/\/console\/overview$/);
      await waitForText(page, "当前账户总数");
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
      await waitForText(page, "核对开通信息");
      await waitForText(page, name === "mobile" ? "暂不可用" : "$52.58");
      await waitForText(page, "$240.08");
      await waitForText(page, "按自然月计费");
      state.customerRoutes.add("/console/workspaces/new");
      await assertWorkspaceLaunchLayout(page, name);
      await assertWorkspacePlanRadios(page);
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
        if (!await proPlan.isChecked()) {
          throw new Error("console_browser_unavailable_plan_keyboard_selection_failed");
        }
        state.unavailablePlanKeyboardViewports.add(name);
      }
      await captureFixtureScreenshot(page, state, screenshotDir, "workspace-new-ready", name);
      await page.getByRole("button", { name: "核对开通信息", exact: true }).click();
      await page.getByRole("heading", { name: "确认开通信息", exact: true }).waitFor({ state: "visible" });
      await assertWorkspaceLaunchLayout(page, name);
      await assertWorkspaceLaunchConfirmation(page, name);
      const launchConfirmation = page.getByRole("checkbox", { name: "我确认一次性预付 Workspace 月度总额并开通", exact: true });
      await launchConfirmation.click();
      if (await launchConfirmation.getAttribute("data-state") !== "checked") throw new Error("console_browser_workspace_confirmation_not_checked");
      if (!await page.getByRole("button", { name: "确认预付并开通", exact: true }).isEnabled()) throw new Error("console_browser_workspace_confirmation_submit_disabled");
      await page.waitForTimeout(250);
      await captureFixtureScreenshot(page, state, screenshotDir, "workspace-confirm", name);
      state.launches = [pendingWorkspaceLaunch()];
      await page.goto(`${server.origin}/console/workspaces/new?progress=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "当前处理阶段");
      await waitForText(page, "runtime_starting");
      if (await page.locator(".workspace-progress").count()) throw new Error("console_browser_inferred_workspace_progress_present");
      await assertWorkspaceLaunchLayout(page, name);
      await assertNoViewportOverflow(page);
      await captureFixtureScreenshot(page, state, screenshotDir, "workspace-operation", name);
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
      await waitForText(page, "余额历史");
      state.customerRoutes.add("/console/api");
      await assertNoViewportOverflow(page);
      await captureFixtureScreenshot(page, state, screenshotDir, "api-overview", name);

      state.keys = [gatewayKey()];
      await page.goto(`${server.origin}/console/api/usage?viewport=${name}`, { waitUntil: "networkidle" });
      await assertUsageRecordFields(page, name);
      state.customerRoutes.add("/console/api/usage");
      await assertNoViewportOverflow(page);
      await captureFixtureScreenshot(page, state, screenshotDir, "api-usage", name);

      state.keys = [gatewayKey()];
      await page.goto(`${server.origin}/console/api/keys?viewport=${name}`, { waitUntil: "networkidle" });
      const keySurface = name === "desktop" ? page.locator(".keys-table-wrap") : page.locator(".mobile-key-list");
      try {
        await keySurface.getByText("General fixture key", { exact: true }).waitFor({ state: "visible" });
      } catch (error) {
        const diagnostic = await page.evaluate(() => ({
          url: window.location.href,
          bodyText: document.body.textContent?.replace(/\s+/g, " ").trim().slice(0, 1200) || "",
          rootHtml: document.querySelector("#root")?.innerHTML.slice(0, 1200) || "",
          panelText: document.querySelector(".keys-panel")?.textContent?.replace(/\s+/g, " ").trim() || "",
          sourceLoading: document.querySelectorAll(".keys-panel .source-loading").length,
          sourceEmpty: document.querySelectorAll(".keys-panel .source-empty").length,
          alerts: [...document.querySelectorAll(".keys-panel [role='alert']")].map((element) => element.textContent?.replace(/\s+/g, " ").trim()),
          desktopDisplay: getComputedStyle(document.querySelector(".keys-table-wrap") || document.body).display,
          mobileDisplay: getComputedStyle(document.querySelector(".mobile-key-list") || document.body).display
        }));
        throw new Error(`console_browser_key_surface_missing:${JSON.stringify({ fixtureKeys: state.keys.map((key) => key.id), pageErrors: state.pageErrors, consoleErrors: state.consoleErrors, diagnostic })}`, { cause: error });
      }
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
      await captureFixtureScreenshot(page, state, screenshotDir, "billing", name);

      await page.goto(`${server.origin}/console/announcements?viewport=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "暂无公告");
      state.customerRoutes.add("/console/announcements");
      await assertNoViewportOverflow(page);
      await captureFixtureScreenshot(page, state, screenshotDir, "announcements", name);

      for (const sourceState of ["empty", "unavailable", "error"]) {
        state.sourceState = sourceState;
        await page.goto(`${server.origin}/console/api/keys?state=${sourceState}&viewport=${name}`, { waitUntil: "networkidle" });
        await waitForText(page, sourceState === "empty" ? "暂无数据" : "API Keys 暂不可用");
        if (sourceState !== "empty") await waitForText(page, "原因代码：sub2api_unavailable");
      }

      state.role = "operator";
      authenticateFixtureSession(state, "operator");
      state.sourceState = "available";
      let operatorReadStart = state.operatorPageReads.length;
      await page.goto(`${server.origin}/admin/overview?viewport=${name}`, { waitUntil: "networkidle" });
      await waitForText(page.locator(".main-column"), "运维概览");
      assertOperatorPageReads(state, operatorReadStart, ["/api/operator/overview", "/api/operator/announcements"]);
      await assertNoViewportOverflow(page);
      await captureFixtureScreenshot(page, state, screenshotDir, "admin-overview", name);
      operatorReadStart = state.operatorPageReads.length;
      await page.goto(`${server.origin}/admin/billing?viewport=${name}`, { waitUntil: "networkidle" });
      await page.getByRole("button", { name: "查看恢复计划", exact: true }).click();
      await page.getByRole("dialog", { name: "复核详情与 Recovery Plan" }).getByRole("button", { name: "诊断恢复计划", exact: true }).click();
      await waitForText(page, "recovery-plan-fixture");
      await page.getByRole("button", { name: "查看已持久化计划", exact: true }).click();
      await page.getByRole("button", { name: "校验计划", exact: true }).click();
      await page.getByRole("checkbox", { name: "确认继续此 Recovery Plan" }).click();
      await page.getByRole("button", { name: "确认继续", exact: true }).click();
      await waitForText(page, "execution-recovery-fixture");
      await page.getByRole("button", { name: "确认继续", exact: true }).click();
      assertOperatorPageReads(state, operatorReadStart, ["/api/operator/reconciliation"]);
      await assertNoViewportOverflow(page);
      await captureFixtureScreenshot(page, state, screenshotDir, "admin-reconciliation", name);
      operatorReadStart = state.operatorPageReads.length;
      await page.goto(`${server.origin}/admin/system?viewport=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "系统状态");
      await waitForText(page, "服务健康");
      assertOperatorPageReads(state, operatorReadStart, ["/api/operator/health"]);
      await assertNoViewportOverflow(page);
      await captureFixtureScreenshot(page, state, screenshotDir, "admin-system", name);
      operatorReadStart = state.operatorPageReads.length;
      await page.goto(`${server.origin}/admin/announcements?viewport=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "暂无公告");
      assertOperatorPageReads(state, operatorReadStart, ["/api/operator/announcements"]);
      operatorReadStart = state.operatorPageReads.length;
      await page.goto(`${server.origin}/admin/resources?viewport=${name}`, { waitUntil: "networkidle" });
      await waitForText(page, "Workspace 资源列表");
      assertOperatorPageReads(state, operatorReadStart, ["/api/operator/workspaces"]);
      operatorReadStart = state.operatorPageReads.length;
      await page.getByRole("button", { name: "查看资源", exact: true }).first().click();
      await waitForText(page, "provider ID");
      await waitForText(page, "最近 provider 读回");
      assertOperatorPageReads(state, operatorReadStart, ["/api/operator/workspaces/ws-1"]);
      await page.evaluate(() => {
        window.scrollTo({ top: 0 });
        document.querySelectorAll(".table-wrap").forEach((element) => {
          element.scrollLeft = 0;
        });
      });
      await captureFixtureScreenshot(page, state, screenshotDir, "admin-resources", name);
      if (name === "mobile") {
        state.operatorAccounts = [operatorAccount("acct-1", "active"), operatorAccount("acct-2", "disabled")];
      }
      operatorReadStart = state.operatorPageReads.length;
      await page.goto(`${server.origin}/admin/accounts?viewport=${name}`, { waitUntil: "networkidle" });
      assertOperatorPageReads(state, operatorReadStart, ["/api/operator/accounts"]);
      const accountSurface = name === "desktop"
        ? page.locator(".operator-account-table tbody tr:visible")
        : page.locator(".operator-account-mobile-card:visible");
      const activeAccountRow = accountSurface.filter({ hasText: "pilot@example.com" });
      const disabledAccountRow = accountSurface.filter({ hasText: "stopped@example.com" });
      if (name === "desktop") {
        const accountHeaders = (await page.locator(".operator-account-table th").allTextContents()).map((label) => label.trim());
        const expectedAccountHeaders = ["用户", "账户映射", "余额", "API 费用", "资源", "状态", "操作"];
        if (JSON.stringify(accountHeaders) !== JSON.stringify(expectedAccountHeaders)) {
          throw new Error(`console_browser_account_fields:${JSON.stringify(accountHeaders)}`);
        }
      }
      await activeAccountRow.getByText("正常", { exact: true }).last().waitFor({ state: "visible" });
      await disabledAccountRow.getByText("已停用", { exact: true }).last().waitFor({ state: "visible" });
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
        const refreshedAccountRow = page.locator(".operator-account-table tbody tr:visible").filter({ hasText: "pilot@example.com" });
        await refreshedAccountRow.getByText("已停用", { exact: true }).last().waitFor({ state: "visible" });
        if (!state.dialogMessages.includes("确认停用该客户？账号会立即停用；历史账单、收据和审计记录会保留。")) {
          throw new Error(`console_browser_disable_confirmation_missing:${JSON.stringify(state.dialogMessages)}`);
        }
        await captureFixtureScreenshot(page, state, screenshotDir, "admin-account-disabled", name);
        operatorReadStart = state.operatorPageReads.length;
        await exerciseWalletAdjustment(page, state, screenshotDir, name, { submit: true });
        assertOperatorPageReads(state, operatorReadStart, ["/api/operator/accounts"]);
      } else {
        await exerciseWalletAdjustment(page, state, screenshotDir, name);
        state.operatorAccounts[0] = operatorAccount("acct-1", "disabled");
      }
      for (const sourceState of ["empty", "unavailable", "error"]) {
        state.sourceState = sourceState;
        operatorReadStart = state.operatorPageReads.length;
        await page.goto(`${server.origin}/admin/resources?state=${sourceState}&viewport=${name}`, { waitUntil: "networkidle" });
        await waitForText(page, sourceState === "empty" ? "暂无 Workspace" : "Workspace 资源暂不可用");
        if (sourceState !== "empty") await waitForText(page, "原因代码：control_plane_fabric_sub2api_unavailable");
        assertOperatorPageReads(state, operatorReadStart, ["/api/operator/workspaces"]);
      }
      await assertNoViewportOverflow(page);
      await context.close();
    }

    const highRiskEvidence = highRiskWriteEvidence(await exerciseHighRiskWriteFlows(browser, server.origin));

    if (state.unexpectedApi.length) throw new Error(`console_browser_unexpected_api:${state.unexpectedApi.join(",")}`);
    if (state.pageErrors.length) throw new Error(`console_browser_page_error:${state.pageErrors.join(",")}`);
    if (state.consoleErrors.length) throw new Error(`console_browser_console_error:${state.consoleErrors.join(",")}`);
    if (state.gatewayWrites.size !== 1 || state.walletWrites.size !== 1) throw new Error("console_browser_idempotency_failed");
    if (state.operatorDisableWrites.size !== 1) throw new Error(`console_browser_operator_disable_failed:${state.operatorDisableWrites.size}`);
    if (state.recoveryPlanDiagnoses !== 2 || state.recoveryPlanValidations !== 2 || state.recoveryPlanExecutions.size !== 1 || [...state.recoveryPlanExecuteAttempts.values()].some((attempts) => attempts !== 4)) {
      throw new Error(`console_browser_recovery_plan_failed:${JSON.stringify({ diagnoses: state.recoveryPlanDiagnoses, validations: state.recoveryPlanValidations, executions: state.recoveryPlanExecutions.size, attempts: [...state.recoveryPlanExecuteAttempts.values()] })}`);
    }
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
      recoveryPlan: { diagnoses: state.recoveryPlanDiagnoses, validations: state.recoveryPlanValidations, executions: state.recoveryPlanExecutions.size },
      ...highRiskEvidence,
      operatorAccountDisableWrites: state.operatorDisableWrites.size,
      operatorAccountStatuses: Object.fromEntries(state.operatorAccounts.map((account) => [account.accountId, account.status])),
      operatorAccountViewports: [...state.operatorAccountViewports],
      unavailablePlanKeyboardViewports: [...state.unavailablePlanKeyboardViewports],
      workspaceNavigation: state.runtimeReads.has("ws-1") && state.runtimeReads.has("ws-2"),
      workspacePagination: state.workspacePageReads.some(({ page, pageSize }) => page === 1 && pageSize === 10)
        && state.workspacePageReads.some(({ page, pageSize }) => page === 1 && pageSize === 1),
      directDetailRefresh: (state.runtimeReads.get("ws-1") || 0) > 1,
      requestRecordFields: ["modelEndpoint", "tokens", "actualCost", "latency", "time", "requestId"],
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
