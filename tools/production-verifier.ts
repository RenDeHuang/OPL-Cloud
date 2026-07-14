import { mkdir, rename, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";

export const PAID_CONFIRMATION = "I_UNDERSTAND_THIS_SPENDS_REAL_BALANCE";

const BASIC_COMPUTE_CHARGE_USD_MICROS = 50_000_000;
const STORAGE_10_GB_CHARGE_USD_MICROS = 2_571_429;
const DEFAULT_SLOT = "01";
const DEFAULT_ATTEMPTS = 90;
const DEFAULT_RETRY_DELAY_MS = 10_000;

function sleep(ms) {
  return ms > 0 ? new Promise((resolve) => setTimeout(resolve, ms)) : Promise.resolve();
}

function assertIdentity(value, pattern, error) {
  if (!pattern.test(String(value || ""))) throw new Error(error);
}

export function productionVerificationMutationKey(runId, slot = DEFAULT_SLOT, stage) {
  assertIdentity(runId, /^[A-Za-z0-9._-]{1,80}$/, "production_verification_run_id_invalid");
  assertIdentity(slot, /^[A-Za-z0-9._-]{1,16}$/, "production_verification_slot_invalid");
  assertIdentity(stage, /^[A-Za-z0-9._-]{1,48}$/, "production_verification_stage_invalid");
  return `production-verification:${runId}:${slot}:${stage}`;
}

function privateIpv4(hostname) {
  const parts = String(hostname).split(".").map(Number);
  if (parts.length !== 4 || parts.some((part) => !Number.isInteger(part) || part < 0 || part > 255)) return false;
  const [first, second] = parts;
  return first === 10 || first === 127 || first === 0 || (first === 172 && second >= 16 && second <= 31) || (first === 192 && second === 168) || (first === 169 && second === 254);
}

function privateHostname(hostname) {
  const value = String(hostname || "").toLowerCase();
  return value === "localhost" || value.endsWith(".localhost") || value === "::1" || value.startsWith("fc") || value.startsWith("fd") || value.startsWith("fe80") || privateIpv4(value);
}

function redactSensitiveUrl(value) {
  if (typeof value !== "string" || !/^https?:\/\//i.test(value)) return value;
  const parsed = new URL(value);
  for (const key of [...parsed.searchParams.keys()]) {
    if (/(?:token|key|secret|password|credential)/i.test(key)) parsed.searchParams.delete(key);
  }
  return parsed.toString();
}

export function assertPublicHttpsUrl(value, errorName, { hostname = "" } = {}) {
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    throw new Error(errorName);
  }
  if (parsed.protocol !== "https:" || parsed.username || parsed.password || parsed.port || privateHostname(parsed.hostname) || (hostname && parsed.hostname.toLowerCase() !== hostname.toLowerCase())) {
    throw new Error(errorName);
  }
  return parsed;
}

function normalizeOrigin(origin, allowPrivateConsoleOrigin) {
  if (allowPrivateConsoleOrigin) {
    const parsed = new URL(origin);
    if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password) throw new Error("public_console_origin_required");
    return parsed.origin;
  }
  return assertPublicHttpsUrl(origin, "public_console_origin_required").origin;
}

export function verificationOwnerFromSeed(raw, accountId = "") {
  let users;
  try {
    users = JSON.parse(raw || "null");
  } catch {
    throw new Error("verification_owner_credentials_required");
  }
  const owners = Array.isArray(users) ? users.filter((user) =>
    user?.role === "owner" && (!accountId || user.accountId === accountId) && user.accountId && user.email && user.password
  ) : [];
  if (owners.length !== 1) throw new Error("verification_owner_credentials_required");
  const owner = owners[0];
  if (!Number.isSafeInteger(owner.sub2apiUserId) || owner.sub2apiUserId <= 0) throw new Error("verification_owner_mapping_required");
  return { accountId: owner.accountId, email: owner.email, password: owner.password, sub2apiUserId: owner.sub2apiUserId };
}

function withoutSecrets(value) {
  if (typeof value === "string") return redactSensitiveUrl(value);
  if (Array.isArray(value)) return value.map(withoutSecrets);
  if (!value || typeof value !== "object") return value;
  return Object.fromEntries(Object.entries(value)
    .filter(([key]) => !/(cookie|password|secret|csrf|modelAccessKey)/i.test(key))
    .map(([key, nested]) => [key, withoutSecrets(nested)]));
}

export async function writeVerificationManifest(path, manifest) {
  if (!path) return;
  await mkdir(dirname(path), { recursive: true });
  const temporary = `${path}.${process.pid}.tmp`;
  await writeFile(temporary, `${JSON.stringify(withoutSecrets(manifest), null, 2)}\n`, { mode: 0o600 });
  await rename(temporary, path);
}

function addCheck(checks, name, ok, details = {}) {
  const check = { name, ok: Boolean(ok), ...details };
  checks.push(check);
  if (!check.ok) throw new Error(name);
  return check;
}

function responseCookie(headers) {
  const values = typeof headers.getSetCookie === "function" ? headers.getSetCookie() : [headers.get("set-cookie")].filter(Boolean);
  return values.map((value) => value.split(";", 1)[0]).join("; ");
}

async function requestJson({ fetchImpl, origin, path, method = "GET", auth, idempotencyKey, body }) {
  const response = await fetchImpl(`${origin}${path}`, {
    method,
    headers: {
      ...(body !== undefined ? { "content-type": "application/json" } : {}),
      ...(auth?.cookie ? { cookie: auth.cookie } : {}),
      ...(auth?.csrf && method !== "GET" ? { "x-opl-csrf": auth.csrf } : {}),
      ...(idempotencyKey ? { "Idempotency-Key": idempotencyKey } : {})
    },
    body: body !== undefined ? JSON.stringify(body) : undefined
  });
  const text = await response.text();
  let payload = {};
  try {
    payload = text ? JSON.parse(text) : {};
  } catch {
    throw new Error(`invalid_json_response:${method}:${path}`);
  }
  if (!response.ok) throw new Error(`request_failed:${method}:${path}:${response.status}:${payload?.error || "unknown"}`);
  return { payload, response };
}

async function login({ fetchImpl, origin, email, password }) {
  const { payload, response } = await requestJson({ fetchImpl, origin, path: "/api/auth/login", method: "POST", body: { email, password } });
  const cookie = responseCookie(response.headers);
  const csrf = response.headers.get("x-opl-csrf-token") || "";
  if (!cookie || !csrf || payload?.user?.accountId === undefined) throw new Error("verification_login_failed");
  return { cookie, csrf, user: payload.user };
}

function activeCompute(row) {
  return row?.billingStatus === "active" && row?.status === "running" && row?.id;
}

function activeStorage(row) {
  return row?.billingStatus === "active" && ["available", "ready"].includes(row?.status) && row?.id;
}

function activeRuntime(row) {
  return row?.ready === true && ["running", "ready", "available", "active"].includes(row?.status);
}

async function waitForResource({ attempts, retryDelayMs, current, ready, refresh }) {
  let resource = current;
  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    if (ready(resource)) return resource;
    if (attempt < attempts) await sleep(retryDelayMs);
    resource = await refresh();
  }
  throw new Error("monthly_resource_readiness_timeout");
}

function assertMonthlyResource(checks, row, { kind, chargeUsdMicros, monthlyPriceCnyCents }) {
  addCheck(checks, `${kind}_monthly_entitlement_active`, row?.billingStatus === "active" && Boolean(row?.paidThrough));
  addCheck(checks, `${kind}_monthly_charge_exact`, row?.chargeUsdMicros === chargeUsdMicros && row?.monthlyPriceCnyCents === monthlyPriceCnyCents);
  addCheck(checks, `${kind}_redeem_code_present`, Boolean(row?.sub2apiRedeemCode));
  addCheck(checks, `${kind}_receipt_present`, Boolean(row?.lastReceiptId));
}

async function receiptForResource({ fetchImpl, origin, auth, accountId, resource, expectedCharge }) {
  const { payload } = await requestJson({ fetchImpl, origin, auth, path: `/api/billing/receipts/${encodeURIComponent(resource.lastReceiptId)}` });
  if (payload?.receiptId !== resource.lastReceiptId || payload?.accountId !== accountId || payload?.type !== "billing.resource_purchased.v1" || payload?.status !== "completed") {
    throw new Error("billing_receipt_identity_mismatch");
  }
  if (payload?.cost?.chargeUsdMicros !== expectedCharge || payload?.cost?.sub2apiRedeemCode !== resource.sub2apiRedeemCode) {
    throw new Error("billing_receipt_charge_mismatch");
  }
  return payload;
}

async function requestWorkspaceUrl({ fetchImpl, url, attempts, retryDelayMs }) {
  const parsed = assertPublicHttpsUrl(url, "public_workspace_url_required", { hostname: "workspace.medopl.cn" });
  let lastStatus = 0;
  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    const response = await fetchImpl(parsed.toString(), { redirect: "follow" });
    lastStatus = response.status;
    const body = response.ok ? await response.text() : "";
    if (response.ok && body.trim()) return { attempts: attempt, status: response.status };
    if (attempt < attempts) await sleep(retryDelayMs);
  }
  throw new Error(`workspace_url_not_ready:${lastStatus}`);
}

async function verifyWorkspaceBrowser({ url, screenshotDir, browserFactory }) {
  const createBrowser = browserFactory || (async () => {
    const { chromium } = await import("playwright");
    return chromium.launch({ headless: true });
  });
  const browser = await createBrowser();
  try {
    const page = await browser.newPage();
    await page.goto(url, { waitUntil: "domcontentloaded" });
    if (!(await page.locator("body").innerText()).trim()) throw new Error("workspace_browser_blank");
    if (screenshotDir) {
      await mkdir(screenshotDir, { recursive: true });
      await page.screenshot({ path: join(screenshotDir, "workspace.png"), fullPage: true });
    }
  } finally {
    await browser.close();
  }
}

function terminal(row) {
  return ["destroyed", "external_deleted", "deleted", "missing"].includes(row?.status);
}

export async function cleanupVerificationResources({
  fetchImpl,
  origin,
  auth,
  computeAllocationId = "",
  storageId = "",
  attachmentId = "",
  workspaceId = "",
  attempts = DEFAULT_ATTEMPTS,
  retryDelayMs = DEFAULT_RETRY_DELAY_MS,
  checks = null
}) {
  const errors = [];
  if (attachmentId) {
    try {
      const { payload } = await requestJson({ fetchImpl, origin, auth, path: "/api/storage-attachments/detach", method: "POST", idempotencyKey: `cleanup:${attachmentId}:detach`, body: { attachmentId } });
      if (payload?.id !== attachmentId || payload?.status !== "detached") throw new Error("attachment_not_detached");
      checks?.push({ name: "verification_attachment_detached", ok: true });
    } catch (error) {
      errors.push(`detach:${attachmentId}:${error.message}`);
    }
  }
  if (computeAllocationId) {
    try {
      let { payload } = await requestJson({ fetchImpl, origin, auth, path: `/api/compute-allocations/${encodeURIComponent(computeAllocationId)}/destroy`, method: "POST", idempotencyKey: `cleanup:${computeAllocationId}:destroy`, body: { confirm: true } });
      for (let attempt = 1; !terminal(payload) && attempt <= attempts; attempt += 1) {
        if (attempt < attempts) await sleep(retryDelayMs);
        ({ payload } = await requestJson({ fetchImpl, origin, auth, path: `/api/compute-allocations/${encodeURIComponent(computeAllocationId)}` }));
      }
      if (payload?.id !== computeAllocationId || !terminal(payload)) throw new Error("compute_not_destroyed");
      checks?.push({ name: "verification_compute_destroyed", ok: true });
    } catch (error) {
      errors.push(`compute:${computeAllocationId}:${error.message}`);
    }
  }
  if (storageId) {
    try {
      let { payload } = await requestJson({ fetchImpl, origin, auth, path: "/api/storage-volumes/destroy", method: "POST", idempotencyKey: `cleanup:${storageId}:destroy`, body: { storageId, confirmDataLoss: true } });
      for (let attempt = 1; !terminal(payload) && attempt <= attempts; attempt += 1) {
        if (attempt < attempts) await sleep(retryDelayMs);
        ({ payload } = await requestJson({ fetchImpl, origin, auth, path: `/api/storage-volumes/${encodeURIComponent(storageId)}/sync`, method: "POST", idempotencyKey: `cleanup:${storageId}:sync`, body: {} }));
      }
      if (payload?.id !== storageId || !terminal(payload)) throw new Error("storage_not_destroyed");
      checks?.push({ name: "verification_storage_destroyed", ok: true });
    } catch (error) {
      errors.push(`storage:${storageId}:${error.message}`);
    }
  }
  if (workspaceId) {
    try {
      const { payload } = await requestJson({ fetchImpl, origin, auth, path: "/api/state" });
      const workspace = payload?.workspaces?.find((row) => row.id === workspaceId);
      const runtimeRef = workspace?.runtimeId || workspace?.runtimeServiceName || workspace?.serviceName || workspace?.runtime?.serviceName;
      if (runtimeRef) throw new Error("workspace_runtime_not_removed");
      checks?.push({ name: "verification_workspace_runtime_removed", ok: true });
    } catch (error) {
      errors.push(`runtime:${workspaceId}:${error.message}`);
    }
  }
  return errors;
}

export async function verifyProductionChain({
  origin,
  authUsersJson,
  accountId = "",
  runId,
  slot = DEFAULT_SLOT,
  packageId = "basic",
  workspaceName = "Production Verification Lab",
  paidConfirmation = "",
  workspaceUrlAttempts = DEFAULT_ATTEMPTS,
  retryDelayMs = DEFAULT_RETRY_DELAY_MS,
  manifestPath = "",
  browserE2E = false,
  screenshotDir = "",
  browserFactory,
  cleanupOnFailure = true,
  allowPrivateConsoleOrigin = false,
  fetchImpl = globalThis.fetch
} = {}) {
  if (paidConfirmation !== PAID_CONFIRMATION) throw new Error("paid_confirmation_required");
  const owner = verificationOwnerFromSeed(authUsersJson, accountId);
  const normalizedOrigin = normalizeOrigin(origin, allowPrivateConsoleOrigin);
  const effectiveRunId = runId || new Date().toISOString().replace(/[^0-9TZ]/g, "");
  productionVerificationMutationKey(effectiveRunId, slot, "validate");
  if (packageId !== "basic") throw new Error("production_verifier_basic_package_required");
  if (!Number.isInteger(workspaceUrlAttempts) || workspaceUrlAttempts < 1 || !Number.isFinite(retryDelayMs) || retryDelayMs < 0) throw new Error("verification_retry_config_invalid");

  const checks = [];
  const manifest = { runId: effectiveRunId, slot, accountId: owner.accountId, sub2apiUserId: owner.sub2apiUserId, ids: {}, redeemCodes: {}, receiptIds: {} };
  const persist = () => writeVerificationManifest(manifestPath, manifest);
  let auth;
  let compute;
  let storage;
  let attachment;
  let workspace;

  try {
    const readiness = await requestJson({ fetchImpl, origin: normalizedOrigin, path: "/api/production/readiness" });
    addCheck(checks, "production_readiness", readiness.payload?.ready === true);
    auth = await login({ fetchImpl, origin: normalizedOrigin, email: owner.email, password: owner.password });
    addCheck(checks, "console_login", auth.user?.accountId === owner.accountId);

    const initial = (await requestJson({ fetchImpl, origin: normalizedOrigin, auth, path: "/api/state" })).payload;
    addCheck(checks, "live_sub2api_balance", initial?.balance?.source === "sub2api" && initial?.balance?.currency === "USD" && initial?.balance?.userId === owner.sub2apiUserId && Number.isSafeInteger(initial?.balance?.usdMicros));
    const beforeUsdMicros = initial.balance.usdMicros;

    const computeKey = productionVerificationMutationKey(effectiveRunId, slot, "create-compute");
    const computeBody = { accountId: owner.accountId, packageId, name: `${workspaceName} compute ${effectiveRunId}` };
    compute = (await requestJson({ fetchImpl, origin: normalizedOrigin, auth, path: "/api/compute-allocations", method: "POST", idempotencyKey: computeKey, body: computeBody })).payload;
    const computeReplay = (await requestJson({ fetchImpl, origin: normalizedOrigin, auth, path: "/api/compute-allocations", method: "POST", idempotencyKey: computeKey, body: computeBody })).payload;
    if (computeReplay?.id !== compute?.id) throw new Error("compute_idempotency_mismatch");
    compute = await waitForResource({
      attempts: workspaceUrlAttempts,
      retryDelayMs,
      current: computeReplay,
      ready: activeCompute,
      refresh: async () => (await requestJson({ fetchImpl, origin: normalizedOrigin, auth, path: `/api/compute-allocations/${encodeURIComponent(compute.id)}` })).payload
    });
    const computeStable = (await requestJson({ fetchImpl, origin: normalizedOrigin, auth, path: "/api/compute-allocations", method: "POST", idempotencyKey: computeKey, body: computeBody })).payload;
    if (computeStable?.id !== compute.id || computeStable?.sub2apiRedeemCode !== compute.sub2apiRedeemCode) throw new Error("compute_redeem_replay_mismatch");
    assertMonthlyResource(checks, compute, { kind: "compute", chargeUsdMicros: BASIC_COMPUTE_CHARGE_USD_MICROS, monthlyPriceCnyCents: 35_000 });
    Object.assign(manifest.ids, { computeAllocationId: compute.id });
    manifest.redeemCodes.compute = compute.sub2apiRedeemCode;
    manifest.receiptIds.compute = compute.lastReceiptId;
    await persist();

    const storageKey = productionVerificationMutationKey(effectiveRunId, slot, "create-storage");
    const storageBody = { accountId: owner.accountId, sizeGb: 10, name: `${workspaceName} storage ${effectiveRunId}` };
    storage = (await requestJson({ fetchImpl, origin: normalizedOrigin, auth, path: "/api/storage-volumes", method: "POST", idempotencyKey: storageKey, body: storageBody })).payload;
    const storageReplay = (await requestJson({ fetchImpl, origin: normalizedOrigin, auth, path: "/api/storage-volumes", method: "POST", idempotencyKey: storageKey, body: storageBody })).payload;
    if (storageReplay?.id !== storage?.id) throw new Error("storage_idempotency_mismatch");
    storage = await waitForResource({
      attempts: workspaceUrlAttempts,
      retryDelayMs,
      current: storageReplay,
      ready: activeStorage,
      refresh: async () => (await requestJson({ fetchImpl, origin: normalizedOrigin, auth, path: `/api/storage-volumes/${encodeURIComponent(storage.id)}/sync`, method: "POST", idempotencyKey: productionVerificationMutationKey(effectiveRunId, slot, "sync-storage"), body: {} })).payload
    });
    const storageStable = (await requestJson({ fetchImpl, origin: normalizedOrigin, auth, path: "/api/storage-volumes", method: "POST", idempotencyKey: storageKey, body: storageBody })).payload;
    if (storageStable?.id !== storage.id || storageStable?.sub2apiRedeemCode !== storage.sub2apiRedeemCode) throw new Error("storage_redeem_replay_mismatch");
    assertMonthlyResource(checks, storage, { kind: "storage", chargeUsdMicros: STORAGE_10_GB_CHARGE_USD_MICROS, monthlyPriceCnyCents: 1_800 });
    Object.assign(manifest.ids, { storageId: storage.id });
    manifest.redeemCodes.storage = storage.sub2apiRedeemCode;
    manifest.receiptIds.storage = storage.lastReceiptId;
    await persist();

    const receipts = await Promise.all([
      receiptForResource({ fetchImpl, origin: normalizedOrigin, auth, accountId: owner.accountId, resource: compute, expectedCharge: BASIC_COMPUTE_CHARGE_USD_MICROS }),
      receiptForResource({ fetchImpl, origin: normalizedOrigin, auth, accountId: owner.accountId, resource: storage, expectedCharge: STORAGE_10_GB_CHARGE_USD_MICROS })
    ]);
    addCheck(checks, "monthly_billing_receipts_verified", receipts.length === 2);

    attachment = (await requestJson({
      fetchImpl, origin: normalizedOrigin, auth, path: "/api/storage-attachments", method: "POST",
      idempotencyKey: productionVerificationMutationKey(effectiveRunId, slot, "create-attachment"),
      body: { accountId: owner.accountId, computeAllocationId: compute.id, storageId: storage.id, mountPath: "/data" }
    })).payload;
    addCheck(checks, "storage_attachment_ready", attachment?.id && attachment?.computeAllocationId === compute.id && attachment?.storageId === storage.id && attachment?.status === "attached");
    manifest.ids.attachmentId = attachment.id;
    await persist();

    workspace = (await requestJson({
      fetchImpl, origin: normalizedOrigin, auth, path: "/api/workspaces", method: "POST",
      idempotencyKey: productionVerificationMutationKey(effectiveRunId, slot, "create-workspace"),
      body: { accountId: owner.accountId, workspaceName: `${workspaceName} ${effectiveRunId}`, attachmentId: attachment.id }
    })).payload;
    addCheck(checks, "workspace_created", workspace?.id && workspace?.url);
    manifest.ids.workspaceId = workspace.id;
    manifest.workspaceUrl = workspace.url;
    await persist();

    const runtimeStatus = async () => (await requestJson({
      fetchImpl, origin: normalizedOrigin, auth, path: "/api/workspaces/runtime-status", method: "POST",
      idempotencyKey: productionVerificationMutationKey(effectiveRunId, slot, "runtime-status"), body: { workspaceId: workspace.id }
    })).payload;
    const runtime = await waitForResource({ attempts: workspaceUrlAttempts, retryDelayMs, current: await runtimeStatus(), ready: activeRuntime, refresh: runtimeStatus });
    addCheck(checks, "workspace_runtime_ready", activeRuntime(runtime));
    const workspaceAccess = await requestWorkspaceUrl({ fetchImpl, url: workspace.url, attempts: workspaceUrlAttempts, retryDelayMs });
    addCheck(checks, "workspace_url_ready", workspaceAccess.status >= 200 && workspaceAccess.status < 300, { attempts: workspaceAccess.attempts });
    if (browserE2E) {
      await verifyWorkspaceBrowser({ url: workspace.url, screenshotDir, browserFactory });
      addCheck(checks, "workspace_browser_access", true);
    }

    const finalState = (await requestJson({ fetchImpl, origin: normalizedOrigin, auth, path: "/api/state" })).payload;
    const afterUsdMicros = finalState?.balance?.usdMicros;
    const expectedChargeUsdMicros = BASIC_COMPUTE_CHARGE_USD_MICROS + STORAGE_10_GB_CHARGE_USD_MICROS;
    addCheck(checks, "balance_delta_matches_exact_monthly_charges", beforeUsdMicros - afterUsdMicros === expectedChargeUsdMicros, { beforeUsdMicros, afterUsdMicros, expectedChargeUsdMicros });
    addCheck(checks, "stable_sub2api_redeem_codes", new Set([compute.sub2apiRedeemCode, storage.sub2apiRedeemCode]).size === 2);
    addCheck(checks, "fabric_resources_visible", finalState.computeAllocations?.some((row) => row.id === compute.id && activeCompute(row)) && finalState.storageVolumes?.some((row) => row.id === storage.id && activeStorage(row)));

    const cleanupErrors = await cleanupVerificationResources({
      fetchImpl, origin: normalizedOrigin, auth, computeAllocationId: compute.id, storageId: storage.id, attachmentId: attachment.id, workspaceId: workspace.id,
      attempts: workspaceUrlAttempts, retryDelayMs, checks
    });
    if (cleanupErrors.length) {
      const error = new Error("production_verification_cleanup_failed");
      error.cleanupErrors = cleanupErrors;
      throw error;
    }

    const result = {
      ok: true,
      accountId: owner.accountId,
      sub2apiUserId: owner.sub2apiUserId,
      runId: effectiveRunId,
      workspaceId: workspace.id,
      url: redactSensitiveUrl(workspace.url),
      balance: { beforeUsdMicros, afterUsdMicros, chargedUsdMicros: expectedChargeUsdMicros },
      redeemCodes: [compute.sub2apiRedeemCode, storage.sub2apiRedeemCode],
      receiptIds: [compute.lastReceiptId, storage.lastReceiptId],
      resources: withoutSecrets(manifest.ids),
      cleanup: { complete: true },
      checks
    };
    manifest.result = result;
    await persist();
    return result;
  } catch (error) {
    error.accountId = owner.accountId;
    error.runId = effectiveRunId;
    error.resourceIds = withoutSecrets(manifest.ids);
    error.checks = checks;
    if (!cleanupOnFailure || !auth) throw error;
    error.cleanupErrors = await cleanupVerificationResources({
      fetchImpl,
      origin: normalizedOrigin,
      auth,
      computeAllocationId: compute?.id,
      storageId: storage?.id,
      attachmentId: attachment?.id,
      workspaceId: workspace?.id,
      attempts: workspaceUrlAttempts,
      retryDelayMs
    });
    throw error;
  }
}

function cliArgs(argv) {
  const args = {};
  for (let index = 0; index < argv.length; index += 1) {
    const item = argv[index];
    if (!item.startsWith("--")) continue;
    const key = item.slice(2);
    args[key] = argv[index + 1] && !argv[index + 1].startsWith("--") ? argv[++index] : "true";
  }
  return args;
}

function defaultRunId() {
  return new Date().toISOString().replace(/[-:.]/g, "");
}

function verifierOptionsFromArgs({ argv, env, fetchImpl }) {
  const args = cliArgs(argv);
  return {
    origin: args.origin || env.OPL_CONSOLE_ORIGIN,
    authUsersJson: env.OPL_VERIFY_AUTH_USERS_JSON,
    accountId: args.account || env.OPL_VERIFY_ACCOUNT_ID || "",
    runId: args["run-id"] || env.OPL_VERIFY_RUN_ID || defaultRunId(),
    slot: args.slot || env.OPL_VERIFY_SLOT || DEFAULT_SLOT,
    packageId: args.package || env.OPL_VERIFY_PACKAGE_ID || "basic",
    workspaceName: args.workspace || env.OPL_VERIFY_WORKSPACE_NAME || "Production Verification Lab",
    paidConfirmation: args["paid-confirmation"] || env.OPL_VERIFY_PAID_CONFIRMATION || "",
    workspaceUrlAttempts: Number(args["url-attempts"] || env.OPL_VERIFY_URL_ATTEMPTS || DEFAULT_ATTEMPTS),
    retryDelayMs: Number(args["retry-delay-ms"] || env.OPL_VERIFY_RETRY_DELAY_MS || DEFAULT_RETRY_DELAY_MS),
    manifestPath: args["manifest-path"] || env.OPL_VERIFY_MANIFEST_PATH || "",
    browserE2E: ["1", "true", "yes"].includes(String(args["browser-e2e"] || env.OPL_VERIFY_BROWSER_E2E || "").toLowerCase()),
    screenshotDir: args["screenshot-dir"] || env.OPL_VERIFY_SCREENSHOT_DIR || "",
    cleanupOnFailure: !["0", "false", "no"].includes(String(env.OPL_VERIFY_CLEANUP_ON_FAILURE || "true").toLowerCase()),
    fetchImpl
  };
}

function errorPayload(error) {
  return withoutSecrets({
    ok: false,
    error: error.message,
    accountId: error.accountId,
    runId: error.runId,
    resourceIds: error.resourceIds,
    cleanupErrors: error.cleanupErrors,
    checks: error.checks
  });
}

export async function runProductionVerifierCli({
  argv = process.argv.slice(2),
  env = process.env,
  stdout = process.stdout,
  stderr = process.stderr,
  fetchImpl = globalThis.fetch
} = {}) {
  if (argv.includes("--help") || argv.includes("-h")) {
    stdout.write(`Usage: npm run verify:production -- --origin <https-url> [--account <id>] [--run-id <id>] [--browser-e2e]\nRequired: OPL_VERIFY_AUTH_USERS_JSON and OPL_VERIFY_PAID_CONFIRMATION=${PAID_CONFIRMATION}\n`);
    return 0;
  }
  try {
    const result = await verifyProductionChain(verifierOptionsFromArgs({ argv, env, fetchImpl }));
    stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return 0;
  } catch (error) {
    stderr.write(`${JSON.stringify(errorPayload(error), null, 2)}\n`);
    return 1;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] || "").href) {
  runProductionVerifierCli().then((code) => { process.exitCode = code; });
}
