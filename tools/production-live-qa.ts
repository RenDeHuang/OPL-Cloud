import { pathToFileURL } from "node:url";

import {
  FIXED_VERIFICATION_SLOT_ID,
  assertPublicHttpsUrl,
  login,
  requestJson,
  verificationOwnerFromSeed,
  verifyProductionChain,
  writeVerificationManifest
} from "./production-verifier.ts";

export const LIVE_QA_CONFIRMATION = "I_UNDERSTAND_THIS_SENDS_ONE_REAL_MODEL_REQUEST";

const DEFAULT_USAGE_ATTEMPTS = 24;
const DEFAULT_USAGE_RETRY_DELAY_MS = 5_000;
const DEFAULT_BROWSER_TIMEOUT_MS = 45_000;
const DEFAULT_MODEL_TIMEOUT_MS = 180_000;

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

function usageSnapshot(gateway) {
  const usage = {
    quotaUsedUsdMicros: gateway?.usage?.quotaUsedUsdMicros,
    usage1dUsdMicros: gateway?.usage?.usage1dUsdMicros
  };
  if (!Object.values(usage).every((value) => Number.isSafeInteger(value) && value >= 0)) {
    throw new Error("dedicated_key_usage_required");
  }
  return usage;
}

function dedicatedKey(gateway, expectedId = "") {
  const key = gateway?.apiKey || {};
  if (!key.id || key.name !== "opl-workspace" || key.status !== "active" || key.revealed === true || key.value !== undefined || (expectedId && key.id !== expectedId)) {
    throw new Error("dedicated_workspace_key_required");
  }
  return { id: String(key.id), usage: usageSnapshot(gateway) };
}

function usageIncreased(before, after) {
  return after.quotaUsedUsdMicros > before.quotaUsedUsdMicros || after.usage1dUsdMicros > before.usage1dUsdMicros;
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
    const authUser = await page.evaluate(async () => {
      const response = await fetch("/api/auth/user", { credentials: "include" });
      return { status: response.status, payload: await response.json() };
    });
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
    purchaseBudgetRemaining = 1,
    workspaceUrlAttempts = 3,
    retryDelayMs = 10_000,
    usageAttempts = DEFAULT_USAGE_ATTEMPTS,
    usageRetryDelayMs = DEFAULT_USAGE_RETRY_DELAY_MS,
    browserTimeoutMs = DEFAULT_BROWSER_TIMEOUT_MS,
    modelTimeoutMs = DEFAULT_MODEL_TIMEOUT_MS,
    manifestPath = "",
    browserFactory,
    fetchImpl = globalThis.fetch,
    now = new Date()
  } = options;
  if (confirmation !== LIVE_QA_CONFIRMATION) throw new Error("production_live_qa_confirmation_required");
  if (!Number.isInteger(usageAttempts) || usageAttempts < 1 || !Number.isFinite(usageRetryDelayMs) || usageRetryDelayMs < 0 || !Number.isFinite(browserTimeoutMs) || browserTimeoutMs < 1 || !Number.isFinite(modelTimeoutMs) || modelTimeoutMs < 1) {
    throw new Error("production_live_qa_config_invalid");
  }

  const owner = verificationOwnerFromSeed(authUsersJson, accountId);
  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  const verifierOptions = {
    origin: normalizedOrigin,
    authUsersJson,
    accountId: owner.accountId,
    runId,
    slotId,
    slotDescriptor,
    purchaseBudgetRemaining,
    workspaceUrlAttempts,
    retryDelayMs,
    now,
    fetchImpl
  };
  const before = await verifyProductionChain(verifierOptions);
  if (before.status === "provider_acceptance_required") throw new Error("provider_acceptance_required");
  if (!before.ok || before.status !== "reused") throw new Error("production_live_qa_reusable_slot_required");
  const beforeIds = resourceIds(before);

  const auth = await login({ fetchImpl, origin: normalizedOrigin, email: owner.email, password: owner.password });
  if (auth.user?.accountId !== owner.accountId || !auth.csrfToken) throw new Error("production_live_qa_console_login_failed");
  const runtime = (await requestJson({
    fetchImpl,
    origin: normalizedOrigin,
    auth,
    path: "/api/workspaces/runtime-status",
    method: "POST",
    body: { workspaceId: before.workspaceId }
  })).payload;
  if (runtime?.ready !== true || runtime?.access?.credentialStatus !== "configured" || !runtime?.access?.username || !runtime?.access?.password) {
    throw new Error("production_live_qa_runtime_credentials_required");
  }

  const gatewayBefore = (await requestJson({ fetchImpl, origin: normalizedOrigin, auth, path: "/api/gateway/summary" })).payload;
  const keyBefore = dedicatedKey(gatewayBefore);
  const workspace = await verifyWorkspaceBrowserQa({
    url: runtime.url || before.url,
    username: runtime.access.username,
    password: runtime.access.password,
    runId,
    browserTimeoutMs,
    modelTimeoutMs,
    browserFactory
  });

  let keyAfter;
  let usageReadAttempts = 0;
  for (let attempt = 1; attempt <= usageAttempts; attempt += 1) {
    usageReadAttempts = attempt;
    const gatewayAfter = (await requestJson({ fetchImpl, origin: normalizedOrigin, auth, path: "/api/gateway/summary" })).payload;
    keyAfter = dedicatedKey(gatewayAfter, keyBefore.id);
    if (usageIncreased(keyBefore.usage, keyAfter.usage)) break;
    if (attempt < usageAttempts) await sleep(usageRetryDelayMs);
  }
  if (!keyAfter || !usageIncreased(keyBefore.usage, keyAfter.usage)) throw new Error("dedicated_key_usage_not_increased");

  const after = await verifyProductionChain(verifierOptions);
  if (!after.ok || after.status !== "reused") throw new Error("production_live_qa_reusable_slot_required");
  const afterIds = resourceIds(after);
  if (JSON.stringify(beforeIds) !== JSON.stringify(afterIds)) throw new Error("production_live_qa_resource_ids_changed");

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
    usage: { before: keyBefore.usage, after: keyAfter.usage, increased: true, readAttempts: usageReadAttempts }
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
    stdout.write("Usage: node tools/production-live-qa.ts [--origin <https-url>] [--account <id>]\nRuns one explicitly confirmed Workspace model request after rollout; never buys, renews, or deletes provider resources.\n");
    return 0;
  }
  try {
    if (env.OPL_VERIFY_MODEL_ACCESS_KEY) throw new Error("production_live_qa_raw_key_forbidden");
    const args = cliArgs(argv);
    const result = await verifyProductionLiveQa({
      origin: args.origin || env.OPL_CONSOLE_ORIGIN,
      authUsersJson: env.OPL_VERIFY_AUTH_USERS_JSON,
      accountId: args.account || env.OPL_VERIFY_ACCOUNT_ID || "",
      runId: args["run-id"] || env.OPL_VERIFY_RUN_ID,
      confirmation: env.OPL_VERIFY_LIVE_QA_CONFIRMATION,
      slotId: env.OPL_VERIFY_SLOT_ID || FIXED_VERIFICATION_SLOT_ID,
      slotDescriptor: env.OPL_VERIFY_SLOT_DESCRIPTOR_JSON,
      purchaseBudgetRemaining: Number(env.OPL_VERIFY_PURCHASE_BUDGET_REMAINING ?? 1),
      workspaceUrlAttempts: Number(env.OPL_VERIFY_URL_ATTEMPTS || 3),
      retryDelayMs: Number(env.OPL_VERIFY_RETRY_DELAY_MS || 10_000),
      usageAttempts: Number(env.OPL_VERIFY_USAGE_ATTEMPTS || DEFAULT_USAGE_ATTEMPTS),
      usageRetryDelayMs: Number(env.OPL_VERIFY_USAGE_RETRY_DELAY_MS || DEFAULT_USAGE_RETRY_DELAY_MS),
      browserTimeoutMs: Number(env.OPL_VERIFY_BROWSER_TIMEOUT_MS || DEFAULT_BROWSER_TIMEOUT_MS),
      modelTimeoutMs: Number(env.OPL_VERIFY_MODEL_TIMEOUT_MS || DEFAULT_MODEL_TIMEOUT_MS),
      manifestPath: env.OPL_VERIFY_MANIFEST_PATH || "",
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
