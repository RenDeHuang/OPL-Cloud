import { pathToFileURL } from "node:url";

import {
  FIXED_VERIFICATION_SLOT_ID,
  assertPublicHttpsUrl,
  dedicatedWorkspaceKey,
  login,
  mutationApprovalFromJson,
  requestJson,
  sourceEnvelope,
  verificationOwnerFromSeed,
  verifyProductionChain,
  walletFact,
  writeVerificationManifest
} from "./production-verifier.ts";

export const LIVE_QA_CONFIRMATION = "I_UNDERSTAND_THIS_SENDS_ONE_REAL_MODEL_REQUEST";

const DEFAULT_USAGE_ATTEMPTS = 24;
const DEFAULT_USAGE_RETRY_DELAY_MS = 5_000;
const DEFAULT_BROWSER_TIMEOUT_MS = 45_000;
const DEFAULT_MODEL_TIMEOUT_MS = 180_000;
const DEFAULT_REQUEST_TIMEOUT_MS = 30_000;
const MAX_USAGE_ITEMS = 10_000;
const MAX_USAGE_PAGES = 100;
const READ_ONLY_VIEWPORTS = Object.freeze({
  desktop: Object.freeze({ width: 1440, height: 900 }),
  mobile: Object.freeze({ width: 390, height: 844 })
});

function sleep(ms) {
  return ms > 0 ? new Promise((resolve) => setTimeout(resolve, ms)) : Promise.resolve();
}

function socketPath(url) {
  try {
    return new URL(url).pathname === "/ws";
  } catch {
    return false;
  }
}

function readOnlyRequestSignal(signal, timeoutMs) {
  if (!Number.isInteger(timeoutMs) || timeoutMs < 1 || timeoutMs > 300_000) throw new Error("verification_request_timeout_invalid");
  const timeout = AbortSignal.timeout(timeoutMs);
  return signal ? AbortSignal.any([signal, timeout]) : timeout;
}

async function verifyRetiredRoutes({ origin, fetchImpl, signal, requestTimeoutMs }) {
  const paths = ["/api/projects", "/api/execution-requests", "/api/workspaces/retired/resume"];
  for (const path of paths) {
    const response = await fetchImpl(`${origin}${path}`, {
      method: "GET",
      signal: readOnlyRequestSignal(signal, requestTimeoutMs)
    });
    if (response.status !== 404 || !response.headers.get("content-security-policy")) {
      throw new Error(`retired_route_404_required:${path}`);
    }
  }
  return paths;
}

async function verifyConsoleViewports({ origin, browserFactory }) {
  const createBrowser = browserFactory || (async () => {
    const { chromium } = await import("playwright");
    return chromium.launch({ headless: true });
  });
  const browser = await createBrowser();
  const checked = [];
  try {
    for (const [name, viewport] of Object.entries(READ_ONLY_VIEWPORTS)) {
      const context = await browser.newContext({ viewport });
      try {
        const page = await context.newPage();
        const response = await page.goto(origin, { waitUntil: "domcontentloaded", timeout: DEFAULT_BROWSER_TIMEOUT_MS });
        if (!response?.ok() || !(await page.locator("body").innerText()).trim()) throw new Error(`production_console_${name}_invalid`);
        checked.push(name);
      } finally {
        await context.close();
      }
    }
  } finally {
    await browser.close();
  }
  return checked;
}

export async function verifyProductionReadOnlyRollout(options = {}) {
  const {
    origin,
    authUsersJson,
    accountId = "",
    requestTimeoutMs = DEFAULT_REQUEST_TIMEOUT_MS,
    fetchImpl = globalThis.fetch,
    browserFactory,
    signal
  } = options;
  const owner = verificationOwnerFromSeed(authUsersJson, accountId);
  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  const requestOptions = { fetchImpl, origin: normalizedOrigin, signal, timeoutMs: requestTimeoutMs };

  const health = (await requestJson({ ...requestOptions, path: "/api/healthz" })).payload;
  if (health?.status !== "ok" || Object.keys(health).length !== 1) throw new Error("production_health_invalid");
  const readiness = (await requestJson({ ...requestOptions, path: "/api/production/readiness" })).payload;
  if (readiness?.ready !== true || readiness?.cloudImagesReady !== true || readiness?.workspaceImagesReady !== true || readiness?.immutableImagesReady !== true) {
    throw new Error("production_readiness_invalid");
  }

  const auth = await login({ ...requestOptions, email: owner.email, password: owner.password });
  if (auth.user?.accountId !== owner.accountId) throw new Error("production_read_only_login_failed");
  const endpoint = sourceEnvelope(await requestJson({ ...requestOptions, auth, path: "/api/gateway/endpoint" }), "sub2api").data;
  if (endpoint?.baseUrl !== "https://gflabtoken.cn/v1") throw new Error("production_gateway_endpoint_invalid");
  walletFact(sourceEnvelope(await requestJson({ ...requestOptions, auth, path: "/api/gateway/wallet" }), "sub2api"), owner.sub2apiUserId);

  const workspaces = sourceEnvelope(await requestJson({ ...requestOptions, auth, path: "/api/workspaces?page=1&pageSize=20" }), "control-plane", true);
  if (!Number.isSafeInteger(workspaces.data?.total) || workspaces.data.total < 0 || workspaces.data?.page !== 1 || workspaces.data?.pageSize !== 20 || !Array.isArray(workspaces.data?.items)) {
    throw new Error("production_workspace_source_invalid");
  }
  const receipts = sourceEnvelope(await requestJson({ ...requestOptions, auth, path: "/api/billing/receipts?limit=20" }), "ledger", true);
  if (!Array.isArray(receipts.data?.receipts) || typeof receipts.data?.hasMore !== "boolean") throw new Error("production_ledger_source_invalid");

  let fabricSource = "not_applicable_no_workspace";
  const workspace = workspaces.data.items[0];
  if (workspace?.id) {
    const runtime = sourceEnvelope(await requestJson({
      ...requestOptions,
      auth,
      path: `/api/workspaces/${encodeURIComponent(workspace.id)}/runtime-status`
    }), "fabric").data;
    if (runtime?.workspaceId && runtime.workspaceId !== workspace.id) throw new Error("production_fabric_source_invalid");
    fabricSource = "available";
  }

  const retiredRoutes = await verifyRetiredRoutes({ origin: normalizedOrigin, fetchImpl, signal, requestTimeoutMs });
  const viewports = await verifyConsoleViewports({ origin: normalizedOrigin, browserFactory });
  return {
    ok: true,
    mode: "read-only",
    evidenceLevel: "read-only",
    writesPerformed: 0,
    accountId: owner.accountId,
    checks: {
      health: "ok",
      readiness: "ready",
      sources: ["sub2api", "control-plane", "ledger", fabricSource],
      retiredRoutes,
      viewports
    },
    viewports
  };
}

async function waitFor(check, timeoutMs, error) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (check()) return;
    await sleep(Math.min(100, Math.max(0, deadline - Date.now())));
  }
  throw new Error(error);
}

function resourceIds(result) {
  const ids = {
    cvmInstanceId: result?.slot?.computeProviderResourceId,
    cbsDiskId: result?.slot?.storageProviderResourceId,
    nodePoolId: result?.slot?.nodePoolId,
    persistentVolumeId: result?.slot?.persistentVolumeId
  };
  if (!/^ins-/.test(ids.cvmInstanceId || "") || !/^disk-/.test(ids.cbsDiskId || "") || !/^np-/.test(ids.nodePoolId || "") || !ids.persistentVolumeId) {
    throw new Error("production_live_qa_resource_ids_required");
  }
  return ids;
}

async function gatewayUsageSnapshot(requestOptions, auth, keyId) {
  const items = [];
  const ids = new Set();
  let expected;
  for (let page = 1; !expected || page <= expected.pages; page += 1) {
    const envelope = sourceEnvelope(await requestJson({ ...requestOptions, auth, path: `/api/gateway/keys/${encodeURIComponent(keyId)}/usage?page=${page}&pageSize=100` }), "sub2api", true);
    const payload = envelope.data;
    // ponytail: the dedicated QA key is capped at 10k rows; add a server-side snapshot endpoint if that ceiling is ever reached.
    if ((Number.isSafeInteger(payload?.total) && payload.total > MAX_USAGE_ITEMS) || (Number.isSafeInteger(payload?.pages) && payload.pages > MAX_USAGE_PAGES)) {
      throw new Error("gateway_usage_snapshot_limit_exceeded");
    }
    if (!Number.isSafeInteger(payload?.total) || payload.total < 0 || payload?.page !== page || payload?.pageSize !== 100 || !Number.isSafeInteger(payload?.pages) || payload.pages < 0 || !Array.isArray(payload?.items)) {
      throw new Error("gateway_usage_snapshot_invalid");
    }
    if (envelope.status !== (payload.total === 0 ? "empty" : "available")) throw new Error("gateway_usage_snapshot_invalid");
    if (!expected) {
      expected = { total: payload.total, pages: payload.pages };
      if (payload.pages !== (payload.total === 0 ? 0 : Math.ceil(payload.total / 100))) throw new Error("gateway_usage_snapshot_invalid");
    } else if (payload.total !== expected.total || payload.pages !== expected.pages) {
      throw new Error("gateway_usage_snapshot_changed");
    }
    for (const item of payload.items) {
      const requestId = String(item?.requestId || "").trim();
      if (!requestId || ids.has(requestId)) throw new Error("gateway_usage_snapshot_invalid");
      ids.add(requestId);
      items.push(item);
    }
    if (expected.pages === 0) break;
  }
  if (items.length !== expected.total) throw new Error("gateway_usage_snapshot_invalid");
  return { total: expected.total, ids, items };
}

async function gatewayUsageStats(requestOptions, auth, keyId) {
  const stats = sourceEnvelope(await requestJson({ ...requestOptions, auth, path: `/api/gateway/keys/${encodeURIComponent(keyId)}/usage-summary?period=month` }), "sub2api").data;
  for (const key of ["totalRequests", "totalInputTokens", "totalOutputTokens", "totalTokens", "totalActualCostUsdMicros"]) {
    if (!Number.isSafeInteger(stats?.[key]) || stats[key] < 0) throw new Error("gateway_usage_stats_invalid");
  }
  return stats;
}

function exactUsageRecord(before, after, expectedModel, expectedKeyId) {
  if (after.total === before.total) {
    if (after.ids.size !== before.ids.size || [...before.ids].some((id) => !after.ids.has(id))) throw new Error("gateway_request_cardinality_mismatch");
    return null;
  }
  if (after.total !== before.total + 1 || [...before.ids].some((id) => !after.ids.has(id))) throw new Error("gateway_request_cardinality_mismatch");
  const added = [...after.ids].filter((id) => !before.ids.has(id));
  if (added.length !== 1) throw new Error("gateway_request_cardinality_mismatch");
  const record = after.items.find((item) => item.requestId === added[0]);
  const tokenFields = ["inputTokens", "outputTokens", "cacheCreationTokens", "cacheReadTokens"];
  if (record?.apiKeyId !== expectedKeyId || record?.model !== expectedModel || record?.requestType !== "sync" || record?.inboundEndpoint !== "/v1/responses" ||
    !tokenFields.every((key) => Number.isSafeInteger(record[key]) && record[key] >= 0) || record.inputTokens + record.outputTokens < 1 ||
    !Number.isSafeInteger(record.actualCostUsdMicros) || record.actualCostUsdMicros <= 0) {
    throw new Error("gateway_request_usage_invalid");
  }
  return record;
}

function statsDelta(before, after) {
  return {
    totalRequests: after.totalRequests - before.totalRequests,
    totalInputTokens: after.totalInputTokens - before.totalInputTokens,
    totalOutputTokens: after.totalOutputTokens - before.totalOutputTokens,
    totalTokens: after.totalTokens - before.totalTokens,
    totalActualCostUsdMicros: after.totalActualCostUsdMicros - before.totalActualCostUsdMicros
  };
}

function statsMatchRequest(before, after, request) {
  const delta = statsDelta(before, after);
  return delta.totalRequests === 1 && delta.totalInputTokens === request.inputTokens && delta.totalOutputTokens === request.outputTokens &&
    delta.totalTokens === request.inputTokens + request.outputTokens + request.cacheCreationTokens + request.cacheReadTokens &&
    delta.totalActualCostUsdMicros === request.actualCostUsdMicros;
}

export async function verifyWorkspaceBrowserQa({
  url,
  username,
  password,
  runId,
  browserTimeoutMs = DEFAULT_BROWSER_TIMEOUT_MS,
  modelTimeoutMs = DEFAULT_MODEL_TIMEOUT_MS,
  browserFactory
}) {
  const parsed = assertPublicHttpsUrl(url, "public_workspace_url_required", { hostname: "workspace.medopl.cn" });
  if (!username || !password) throw new Error("workspace_login_credentials_required");
  const createBrowser = browserFactory || (async () => {
    const { chromium } = await import("playwright");
    return chromium.launch({ headless: true });
  });
  const startedAt = Date.now();
  const browser = await createBrowser();
  const context = await browser.newContext();
  try {
    const page = await context.newPage();
    const entry = await page.goto(parsed.toString(), { waitUntil: "domcontentloaded", timeout: browserTimeoutMs });
    if (!entry?.ok()) throw new Error(`workspace_entry_failed:${entry?.status() || 0}`);

    const requestHeaders = { referer: parsed.toString() };
    const loginResponse = await context.request.post(`${parsed.origin}/login`, {
      headers: requestHeaders,
      data: { username, password, remember: true }
    });
    const loginPayload = await loginResponse.json();
    if (!loginResponse.ok() || loginPayload?.success !== true || !loginPayload?.user) throw new Error("workspace_password_login_failed");
    const authUser = await page.evaluate(async (timeoutMs) => {
      const response = await fetch("/api/auth/user", { credentials: "include", signal: AbortSignal.timeout(timeoutMs) });
      return { status: response.status, payload: await response.json() };
    }, browserTimeoutMs);
    if (authUser?.status !== 200 || authUser?.payload?.success !== true || !authUser?.payload?.user) throw new Error("workspace_auth_user_failed");

    let opened = false;
    let framesSent = 0;
    let framesReceived = 0;
    const websocketRequestIds = new Set();
    page.on("websocket", (socket) => {
      if (!socketPath(socket.url())) return;
      opened = true;
      socket.on("framesent", () => { framesSent += 1; });
      socket.on("framereceived", () => { framesReceived += 1; });
    });
    const cdp = await context.newCDPSession(page);
    await cdp.send("Network.enable");
    let websocketStatus = 0;
    cdp.on("Network.webSocketCreated", ({ requestId, url: socketUrl }) => {
      if (socketPath(socketUrl)) websocketRequestIds.add(requestId);
    });
    cdp.on("Network.webSocketHandshakeResponseReceived", ({ requestId, response }) => {
      if (websocketRequestIds.has(requestId) || socketPath(response?.url)) websocketStatus = response?.status || 0;
    });

    await page.reload({ waitUntil: "domcontentloaded", timeout: browserTimeoutMs });
    await waitFor(
      () => opened && websocketStatus === 101 && framesSent > 0 && framesReceived > 0,
      browserTimeoutMs,
      "workspace_websocket_frames_required"
    );

    const token = `OPL_QA_${String(runId).replace(/[^A-Za-z0-9]/g, "_").toUpperCase()}`;
    const input = page.locator("[data-testid='guid-input']");
    await input.waitFor({ state: "visible", timeout: browserTimeoutMs });
    await input.fill(`Reply with exactly ${token} and nothing else.`);
    await page.locator("[data-testid='guid-send-btn']").click();
    await page.waitForURL(/(?:#\/|\/)conversation\//, { timeout: modelTimeoutMs });
    const response = page
      .locator("[data-testid='message-text-left'] [data-testid='message-text-content']")
      .filter({ hasText: token })
      .last();
    try {
      await response.waitFor({ state: "visible", timeout: modelTimeoutMs });
      if (String(await response.textContent() || "").trim() !== token) throw new Error("workspace_model_response_required");
    } catch {
      throw new Error("workspace_model_response_required");
    }

    return {
      login: true,
      authUser: true,
      websocket: { opened, status: websocketStatus, framesSent, framesReceived },
      modelResponse: true,
      durationMs: Date.now() - startedAt
    };
  } finally {
    await context.close();
    await browser.close();
  }
}

export async function verifyProductionLiveQa(options = {}) {
  const {
    origin,
    authUsersJson,
    accountId = "",
    runId = new Date().toISOString().replace(/[-:.]/g, ""),
    confirmation,
    slotId = FIXED_VERIFICATION_SLOT_ID,
    slotDescriptor,
    workspaceUrlAttempts = 3,
    retryDelayMs = 10_000,
    usageAttempts = DEFAULT_USAGE_ATTEMPTS,
    usageRetryDelayMs = DEFAULT_USAGE_RETRY_DELAY_MS,
    browserTimeoutMs = DEFAULT_BROWSER_TIMEOUT_MS,
    modelTimeoutMs = DEFAULT_MODEL_TIMEOUT_MS,
    expectedModel = "",
    requestTimeoutMs = DEFAULT_REQUEST_TIMEOUT_MS,
    manifestPath = "",
    mutationApprovalJson = "",
    mutationApprovalId = "",
    browserFactory,
    fetchImpl = globalThis.fetch,
    signal,
    now = new Date()
  } = options;
  if (confirmation !== LIVE_QA_CONFIRMATION) throw new Error("production_live_qa_confirmation_required");
  if (!Number.isInteger(usageAttempts) || usageAttempts < 1 || !Number.isFinite(usageRetryDelayMs) || usageRetryDelayMs < 0 || !Number.isFinite(browserTimeoutMs) || browserTimeoutMs < 1 || !Number.isFinite(modelTimeoutMs) || modelTimeoutMs < 1) {
    throw new Error("production_live_qa_config_invalid");
  }
  if (!String(accountId).trim()) throw new Error("verification_account_id_required");
  if (!String(expectedModel).trim()) throw new Error("production_live_qa_expected_model_required");

  const owner = verificationOwnerFromSeed(authUsersJson, accountId);
  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  const verifierOptions = {
    origin: normalizedOrigin,
    authUsersJson,
    accountId: owner.accountId,
    runId,
    slotId,
    slotDescriptor,
    workspaceUrlAttempts,
    retryDelayMs,
    requestTimeoutMs,
    now,
    signal,
    fetchImpl
  };
  const before = await verifyProductionChain(verifierOptions);
  if (before.status === "provider_acceptance_required") throw new Error("provider_acceptance_required");
  if (!before.ok || before.status !== "reused") throw new Error("production_live_qa_reusable_slot_required");
  const beforeIds = resourceIds(before);

  const requestOptions = { fetchImpl, origin: normalizedOrigin, signal, timeoutMs: requestTimeoutMs };
  const auth = await login({ ...requestOptions, email: owner.email, password: owner.password });
  if (auth.user?.accountId !== owner.accountId || !auth.csrfToken) throw new Error("production_live_qa_console_login_failed");
  const runtime = sourceEnvelope(await requestJson({
    ...requestOptions,
    auth,
    path: `/api/workspaces/${encodeURIComponent(before.workspaceId)}/runtime-status`
  }), "fabric").data;
  if (Object.hasOwn(runtime?.access || {}, "password") || Object.hasOwn(runtime?.access || {}, "secretRef")) throw new Error("runtime_status_secret_forbidden");
  if (runtime?.ready !== true || runtime?.access?.credentialStatus !== "configured" || !runtime?.access?.username) {
    throw new Error("production_live_qa_runtime_credentials_required");
  }

  const revealed = await requestJson({
    ...requestOptions,
    auth,
    path: `/api/workspaces/${encodeURIComponent(before.workspaceId)}/runtime-credentials/reveal`,
    method: "POST",
    body: {}
  });
  if (revealed.response.headers.get("cache-control") !== "private, no-store") throw new Error("runtime_credentials_cache_control_invalid");
  const credentials = revealed.payload;
  if (credentials?.workspaceId !== before.workspaceId || credentials?.access?.credentialStatus !== "configured" || credentials?.access?.username !== runtime.access.username || !credentials?.access?.password) {
    throw new Error("production_live_qa_runtime_credentials_required");
  }

  const walletBefore = walletFact(sourceEnvelope(await requestJson({ ...requestOptions, auth, path: "/api/gateway/wallet" }), "sub2api"), owner.sub2apiUserId);
  const keyBefore = dedicatedWorkspaceKey(sourceEnvelope(await requestJson({ ...requestOptions, auth, path: "/api/gateway/keys" }), "sub2api", true));
  const usageBefore = await gatewayUsageSnapshot(requestOptions, auth, keyBefore.id);
  const statsBefore = await gatewayUsageStats(requestOptions, auth, keyBefore.id);
  mutationApprovalFromJson(mutationApprovalJson, {
    approvalId: mutationApprovalId,
    accountId: owner.accountId,
    workspaceId: before.workspaceId,
    resourceIds: [slotId, keyBefore.id]
  }, "production_live_qa");
  const workspace = await verifyWorkspaceBrowserQa({
    url: runtime.url || before.url,
    username: credentials.access.username,
    password: credentials.access.password,
    runId,
    browserTimeoutMs,
    modelTimeoutMs,
    browserFactory
  });

  let usageAfter;
  let requestUsage;
  let statsAfter;
  let walletAfter;
  let usageReadAttempts = 0;
  let statsMismatch = false;
  let balanceMismatch = false;
  for (let attempt = 1; attempt <= usageAttempts; attempt += 1) {
    usageReadAttempts = attempt;
    dedicatedWorkspaceKey(sourceEnvelope(await requestJson({ ...requestOptions, auth, path: "/api/gateway/keys" }), "sub2api", true), keyBefore.id);
    usageAfter = await gatewayUsageSnapshot(requestOptions, auth, keyBefore.id);
    requestUsage = exactUsageRecord(usageBefore, usageAfter, expectedModel, keyBefore.id);
    if (requestUsage) {
      statsAfter = await gatewayUsageStats(requestOptions, auth, keyBefore.id);
      walletAfter = walletFact(sourceEnvelope(await requestJson({ ...requestOptions, auth, path: "/api/gateway/wallet" }), "sub2api"), owner.sub2apiUserId);
      const statsMatch = statsMatchRequest(statsBefore, statsAfter, requestUsage);
      const balanceMatch = walletBefore.usdMicros - walletAfter.usdMicros === requestUsage.actualCostUsdMicros;
      if (statsMatch && balanceMatch) break;
      statsMismatch ||= !statsMatch;
      balanceMismatch ||= !balanceMatch;
    }
    if (attempt < usageAttempts) await sleep(usageRetryDelayMs);
  }
  if (!requestUsage) throw new Error("exact_gateway_request_not_found");
  if (!statsAfter || !statsMatchRequest(statsBefore, statsAfter, requestUsage)) throw new Error(statsMismatch ? "gateway_usage_stats_mismatch" : "gateway_usage_stats_invalid");
  if (!walletAfter || walletBefore.usdMicros - walletAfter.usdMicros !== requestUsage.actualCostUsdMicros) {
    throw new Error(balanceMismatch ? "gateway_balance_delta_mismatch" : "gateway_wallet_invalid");
  }

  const after = await verifyProductionChain(verifierOptions);
  if (!after.ok || after.status !== "reused") throw new Error("production_live_qa_reusable_slot_required");
  const afterIds = resourceIds(after);
  if (JSON.stringify(beforeIds) !== JSON.stringify(afterIds)) throw new Error("production_live_qa_resource_ids_changed");
  if (JSON.stringify(before.ledgerReceipt) !== JSON.stringify(after.ledgerReceipt)) throw new Error("production_live_qa_ledger_receipt_changed");
  if (JSON.stringify(before.runtimeOperations) !== JSON.stringify(after.runtimeOperations)) throw new Error("production_live_qa_runtime_operations_changed");

  const delta = statsDelta(statsBefore, statsAfter);

  const result = {
    ok: true,
    status: "passed",
    runId,
    accountId: owner.accountId,
    workspaceId: before.workspaceId,
    slotId,
    keyId: keyBefore.id,
    workspace,
    resourceIds: { before: beforeIds, after: afterIds, unchanged: true },
    balance: { before: walletBefore, after: walletAfter },
    ledgerReceipt: before.ledgerReceipt,
    runtimeOperations: { before: before.runtimeOperations, after: after.runtimeOperations, unchanged: true },
    usage: { request: requestUsage, stats: { before: statsBefore, after: statsAfter, delta }, readAttempts: usageReadAttempts }
  };
  await writeVerificationManifest(manifestPath, result);
  return result;
}

function cliArgs(argv) {
  const args = {};
  for (let index = 0; index < argv.length; index += 1) {
    const item = argv[index];
    if (!item.startsWith("--")) continue;
    args[item.slice(2)] = argv[index + 1] && !argv[index + 1].startsWith("--") ? argv[++index] : "true";
  }
  return args;
}

export async function runProductionLiveQaCli({
  argv = process.argv.slice(2),
  env = process.env,
  stdout = process.stdout,
  stderr = process.stderr,
  fetchImpl = globalThis.fetch,
  browserFactory
} = {}) {
  if (argv.includes("--help") || argv.includes("-h")) {
    stdout.write("Usage: node tools/production-live-qa.ts --read-only\nA model request additionally requires --allow-gateway-write --allow-model-write --approval-id <id> and OPL_VERIFY_MUTATION_APPROVAL_JSON.\n");
    return 0;
  }
  try {
    if (env.OPL_VERIFY_MODEL_ACCESS_KEY) throw new Error("production_live_qa_raw_key_forbidden");
    const args = cliArgs(argv);
    if (args["read-only"] === "true") {
      if (args["allow-gateway-write"] || args["allow-model-write"] || args["approval-id"]) throw new Error("production_live_qa_read_only_conflict");
      const result = await verifyProductionReadOnlyRollout({
        origin: args.origin || env.OPL_CONSOLE_ORIGIN,
        authUsersJson: env.OPL_VERIFY_AUTH_USERS_JSON,
        accountId: args.account || env.OPL_VERIFY_ACCOUNT_ID || "",
        requestTimeoutMs: Number(args["request-timeout-ms"] || env.OPL_VERIFY_REQUEST_TIMEOUT_MS || DEFAULT_REQUEST_TIMEOUT_MS),
        browserFactory,
        fetchImpl
      });
      stdout.write(`${JSON.stringify(result, null, 2)}\n`);
      return 0;
    }
    if (args["allow-gateway-write"] !== "true" || args["allow-model-write"] !== "true") throw new Error("production_live_qa_write_allow_flags_required");
    const accountId = args.account || env.OPL_VERIFY_ACCOUNT_ID || "";
    const slotId = env.OPL_VERIFY_SLOT_ID || FIXED_VERIFICATION_SLOT_ID;
    mutationApprovalFromJson(env.OPL_VERIFY_MUTATION_APPROVAL_JSON, {
      approvalId: args["approval-id"] || "",
      accountId,
      resourceIds: [slotId]
    }, "production_live_qa");
    const result = await verifyProductionLiveQa({
      origin: args.origin || env.OPL_CONSOLE_ORIGIN,
      authUsersJson: env.OPL_VERIFY_AUTH_USERS_JSON,
      accountId,
      runId: args["run-id"] || env.OPL_VERIFY_RUN_ID,
      confirmation: env.OPL_VERIFY_LIVE_QA_CONFIRMATION,
      slotId,
      slotDescriptor: env.OPL_VERIFY_SLOT_DESCRIPTOR_JSON,
      workspaceUrlAttempts: Number(env.OPL_VERIFY_URL_ATTEMPTS || 3),
      retryDelayMs: Number(env.OPL_VERIFY_RETRY_DELAY_MS || 10_000),
      usageAttempts: Number(env.OPL_VERIFY_USAGE_ATTEMPTS || DEFAULT_USAGE_ATTEMPTS),
      usageRetryDelayMs: Number(env.OPL_VERIFY_USAGE_RETRY_DELAY_MS || DEFAULT_USAGE_RETRY_DELAY_MS),
      browserTimeoutMs: Number(env.OPL_VERIFY_BROWSER_TIMEOUT_MS || DEFAULT_BROWSER_TIMEOUT_MS),
      modelTimeoutMs: Number(env.OPL_VERIFY_MODEL_TIMEOUT_MS || DEFAULT_MODEL_TIMEOUT_MS),
      expectedModel: env.OPL_VERIFY_EXPECTED_MODEL || "",
      requestTimeoutMs: Number(env.OPL_VERIFY_REQUEST_TIMEOUT_MS || DEFAULT_REQUEST_TIMEOUT_MS),
      manifestPath: env.OPL_VERIFY_MANIFEST_PATH || "",
      mutationApprovalJson: env.OPL_VERIFY_MUTATION_APPROVAL_JSON,
      mutationApprovalId: args["approval-id"] || "",
      browserFactory,
      fetchImpl
    });
    stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return 0;
  } catch (error) {
    stderr.write(`${JSON.stringify({ ok: false, error: error.message }, null, 2)}\n`);
    return 1;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] || "").href) {
  runProductionLiveQaCli().then((code) => { process.exitCode = code; });
}
