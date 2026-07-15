import { mkdir, rename, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";

export const FIXED_VERIFICATION_SLOT_ID = "verification-slot-01";

const FIXED_VERIFICATION_SLOT_DESCRIPTOR = {
  id: FIXED_VERIFICATION_SLOT_ID,
  customerProduct: false,
  instanceType: "SA5.MEDIUM4",
  chargeType: "PREPAID",
  periodMonths: 1,
  renewFlag: "NOTIFY_AND_MANUAL_RENEW",
  storageSizeGb: 10
};
const DEFAULT_PURCHASE_BUDGET_REMAINING = 1;
const DEFAULT_URL_ATTEMPTS = 3;
const DEFAULT_RETRY_DELAY_MS = 10_000;

function sleep(ms) {
  return ms > 0 ? new Promise((resolve) => setTimeout(resolve, ms)) : Promise.resolve();
}

function assertIdentity(value, pattern, error) {
  if (!pattern.test(String(value || ""))) throw new Error(error);
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
    .filter(([key]) => !/(cookie|password|secret|csrf|apiKey|maskedValue)/i.test(key))
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

export async function requestJson({ fetchImpl, origin, path, method = "GET", auth, body }) {
  const response = await fetchImpl(`${origin}${path}`, {
    method,
    headers: {
      ...(body !== undefined ? { "content-type": "application/json" } : {}),
      ...(auth?.cookie ? { cookie: auth.cookie } : {}),
      ...(auth?.csrfToken ? { "x-opl-csrf": auth.csrfToken } : {})
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

export async function login({ fetchImpl, origin, email, password }) {
  const { payload, response } = await requestJson({ fetchImpl, origin, path: "/api/auth/login", method: "POST", body: { email, password } });
  const cookie = responseCookie(response.headers);
  if (!cookie || payload?.user?.accountId === undefined) throw new Error("verification_login_failed");
  return { cookie, csrfToken: response.headers.get("x-opl-csrf-token") || "", user: payload.user };
}

function verifyCatalog(checks, catalog) {
  const basic = catalog?.packages?.find((row) => row.id === "basic");
  const pro = catalog?.packages?.find((row) => row.id === "pro");
  addCheck(checks, "basic_catalog_price", basic?.price?.monthlyPriceCnyCents === 35_000 && basic?.price?.chargeUsdMicros === 50_000_000);
  addCheck(checks, "pro_catalog_price", pro?.price?.monthlyPriceCnyCents === 150_000 && pro?.price?.chargeUsdMicros === 214_285_715);
  addCheck(checks, "storage_catalog_price", catalog?.storagePer10GbMonthly?.cnyCents === 1_800 && catalog?.storagePer10GbMonthly?.usdMicros === 2_571_429);
}

function providerFact(row, keys, optional = false) {
  const values = [row, row?.providerData]
    .flatMap((source) => keys.map((key) => source?.[key]))
    .filter((value) => value !== undefined && value !== null && String(value).trim() !== "")
    .map((value) => String(value).trim());
  if (values.length === 0) return { ok: optional, value: "" };
  return { ok: new Set(values).size === 1, value: values[0] };
}

function deadlineAfter(value, nowMs) {
  const text = /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/.test(value)
    ? `${value.replace(" ", "T")}Z`
    : value;
  const deadline = Date.parse(text);
  return Number.isFinite(deadline) && deadline > nowMs;
}

function verificationSlotDescriptor(raw) {
  if (raw === undefined || raw === null || raw === "") throw new Error("verification_slot_descriptor_required");
  let descriptor = raw;
  if (typeof raw === "string") {
    try {
      descriptor = JSON.parse(raw);
    } catch {
      throw new Error("verification_slot_descriptor_invalid");
    }
  }
  if (!descriptor || typeof descriptor !== "object" || Array.isArray(descriptor) || Object.entries(FIXED_VERIFICATION_SLOT_DESCRIPTOR).some(([key, value]) => !Object.hasOwn(descriptor, key) || descriptor[key] !== value)) {
    throw new Error("verification_slot_descriptor_invalid");
  }
  return FIXED_VERIFICATION_SLOT_DESCRIPTOR;
}

function fixedSlotFromState(state, { slotId, slotDescriptor, purchaseBudgetRemaining, accountId, nowMs }) {
  const workspaces = (state?.workspaces || []).filter((row) => row?.verificationSlotId === slotId || row?.id === slotId || row?.name === slotId);
  if (workspaces.length === 0) {
    if (purchaseBudgetRemaining === 0) throw new Error("verification_slot_purchase_budget_exhausted");
    return null;
  }
  if (workspaces.length > 1) throw new Error("verification_slot_multiple");

  const workspace = workspaces[0];
  const workspaceCompute = providerFact(workspace, ["currentComputeAllocationId", "computeAllocationId"]);
  const workspaceStorage = providerFact(workspace, ["storageId"]);
  const computeId = workspaceCompute.value;
  const storageId = workspaceStorage.value;
  const computes = (state.computeAllocations || []).filter((row) => row?.id === computeId);
  const storages = (state.storageVolumes || []).filter((row) => row?.id === storageId);
  if (!workspaceCompute.ok || !workspaceStorage.ok || computes.length !== 1 || storages.length !== 1) {
    throw new Error("verification_slot_ambiguous");
  }

  const compute = computes[0];
  const storage = storages[0];
  const computeProviderId = compute.cvmInstanceId || compute.instanceId || compute.providerResourceId;
  const storageProviderId = storage.providerResourceId;
  const computeInstanceType = providerFact(compute, ["instanceType"]);
  const nodePoolId = providerFact(compute, ["nodePoolId"]);
  const computeZone = providerFact(compute, ["zone"]);
  const computeChargeType = providerFact(compute, ["chargeType"]);
  const computePeriod = providerFact(compute, ["requestedPeriodMonths", "periodMonths"]);
  const computeRenewFlag = providerFact(compute, ["renewFlag"]);
  const computeDeadline = providerFact(compute, ["deadline"]);
  const storageZone = providerFact(storage, ["zone"]);
  const storageChargeType = providerFact(storage, ["chargeType", "diskChargeType"]);
  const storagePeriod = providerFact(storage, ["requestedPeriodMonths", "periodMonths"]);
  const storageRenewFlag = providerFact(storage, ["renewFlag"]);
  const storageDeadline = providerFact(storage, ["deadline"]);
  const storageSize = providerFact(storage, ["sizeGb"]);
  const persistentVolumeId = providerFact(storage, ["pvName", "persistentVolumeName"]);
  const computeTags = compute.costTags || {};
  const storageTags = storage.costTags || {};

  const compliant =
    workspace.accountId === accountId &&
    workspace.ownerAccountId === accountId &&
    compute.accountId === accountId &&
    storage.accountId === accountId &&
    compute.workspaceId === workspace.id &&
    storage.workspaceId === workspace.id &&
    computeTags.opl_account_id === accountId &&
    computeTags.opl_workspace_id === workspace.id &&
    computeTags.opl_resource_id === compute.id &&
    storageTags.opl_account_id === accountId &&
    storageTags.opl_workspace_id === workspace.id &&
    storageTags.opl_resource_id === storage.id &&
    /^ins-/.test(computeProviderId || "") &&
    nodePoolId.ok && /^np-/.test(nodePoolId.value) &&
    /^disk-/.test(storageProviderId || "") &&
    persistentVolumeId.ok && Boolean(persistentVolumeId.value) &&
    ["running", "ready", "active"].includes(compute.status) &&
    ["available", "ready", "active"].includes(storage.status) &&
    workspace.openable === true &&
    Boolean(workspace.url) &&
    computeInstanceType.ok && computeInstanceType.value === slotDescriptor.instanceType &&
    computeZone.ok && storageZone.ok && computeZone.value === storageZone.value &&
    computeChargeType.ok && computeChargeType.value === slotDescriptor.chargeType &&
    storageChargeType.ok && storageChargeType.value === slotDescriptor.chargeType &&
    computePeriod.ok && Number(computePeriod.value) === slotDescriptor.periodMonths &&
    storagePeriod.ok && Number(storagePeriod.value) === slotDescriptor.periodMonths &&
    computeRenewFlag.ok && computeRenewFlag.value === slotDescriptor.renewFlag &&
    storageRenewFlag.ok && storageRenewFlag.value === slotDescriptor.renewFlag &&
    computeDeadline.ok && deadlineAfter(computeDeadline.value, nowMs) &&
    storageDeadline.ok && deadlineAfter(storageDeadline.value, nowMs) &&
    storageSize.ok && Number(storageSize.value) === slotDescriptor.storageSizeGb;
  if (!compliant) throw new Error("verification_slot_ambiguous");

  return {
    id: slotId,
    workspace,
    compute,
    storage,
    computeProviderId,
    storageProviderId,
    nodePoolId: nodePoolId.value,
    persistentVolumeId: persistentVolumeId.value
  };
}

async function requestWorkspaceUrl({ fetchImpl, url, attempts, retryDelayMs }) {
  const parsed = assertPublicHttpsUrl(url, "public_workspace_url_required", { hostname: "workspace.medopl.cn" });
  let lastStatus = 0;
  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    const response = await fetchImpl(parsed.toString(), { method: "GET", redirect: "follow" });
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

export async function verifyProductionChain(options = {}) {
  for (const key of ["paidConfirmation", "packageId", "workspaceName", "cleanupOnFailure"]) {
    if (Object.hasOwn(options, key)) throw new Error("production_verifier_read_only");
  }
  const {
    origin,
    authUsersJson,
    accountId = "",
    runId = new Date().toISOString().replace(/[^0-9TZ]/g, ""),
    slotId = FIXED_VERIFICATION_SLOT_ID,
    slotDescriptor: rawSlotDescriptor,
    purchaseBudgetRemaining = DEFAULT_PURCHASE_BUDGET_REMAINING,
    now = new Date(),
    workspaceUrlAttempts = DEFAULT_URL_ATTEMPTS,
    retryDelayMs = DEFAULT_RETRY_DELAY_MS,
    manifestPath = "",
    browserE2E = false,
    screenshotDir = "",
    browserFactory,
    fetchImpl = globalThis.fetch
  } = options;
  const slotDescriptor = verificationSlotDescriptor(rawSlotDescriptor);
  if (slotId !== FIXED_VERIFICATION_SLOT_ID) throw new Error("verification_slot_id_fixed");
  if (!Number.isInteger(purchaseBudgetRemaining) || purchaseBudgetRemaining < 0 || purchaseBudgetRemaining > 1) throw new Error("verification_slot_purchase_budget_invalid");
  if (!Number.isInteger(workspaceUrlAttempts) || workspaceUrlAttempts < 1 || !Number.isFinite(retryDelayMs) || retryDelayMs < 0) throw new Error("verification_retry_config_invalid");
  const nowMs = new Date(now).getTime();
  if (!Number.isFinite(nowMs)) throw new Error("verification_time_invalid");
  assertIdentity(runId, /^[A-Za-z0-9._-]{1,80}$/, "production_verification_run_id_invalid");

  const owner = verificationOwnerFromSeed(authUsersJson, accountId);
  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  const checks = [];
  const readiness = await requestJson({ fetchImpl, origin: normalizedOrigin, path: "/api/production/readiness" });
  addCheck(checks, "production_readiness", readiness.payload?.ready === true);

  const auth = await login({ fetchImpl, origin: normalizedOrigin, email: owner.email, password: owner.password });
  addCheck(checks, "console_login", auth.user?.accountId === owner.accountId);

  const catalog = (await requestJson({ fetchImpl, origin: normalizedOrigin, auth, path: "/api/pricing/catalog" })).payload;
  verifyCatalog(checks, catalog);
  const state = (await requestJson({ fetchImpl, origin: normalizedOrigin, auth, path: "/api/state" })).payload;
  addCheck(checks, "live_sub2api_balance", state?.balance?.source === "sub2api" && state?.balance?.currency === "USD" && state?.balance?.userId === owner.sub2apiUserId && Number.isSafeInteger(state?.balance?.usdMicros));
  const gateway = (await requestJson({ fetchImpl, origin: normalizedOrigin, auth, path: "/api/gateway/summary" })).payload;
  addCheck(checks, "gateway_key_masked", gateway?.apiKey?.revealed !== true && gateway?.apiKey?.value === undefined && Boolean(gateway?.apiKey?.maskedValue));

  const slot = fixedSlotFromState(state, { slotId, slotDescriptor, purchaseBudgetRemaining, accountId: owner.accountId, nowMs });
  if (!slot) {
    const result = { ok: false, status: "provider_acceptance_required", slotId, purchaseBudgetRemaining, runId, checks };
    await writeVerificationManifest(manifestPath, result);
    return result;
  }

  const workspaceAccess = await requestWorkspaceUrl({ fetchImpl, url: slot.workspace.url, attempts: workspaceUrlAttempts, retryDelayMs });
  addCheck(checks, "verification_slot_workspace_ready", workspaceAccess.status >= 200 && workspaceAccess.status < 300, { attempts: workspaceAccess.attempts });
  if (browserE2E) {
    await verifyWorkspaceBrowser({ url: slot.workspace.url, screenshotDir, browserFactory });
    addCheck(checks, "workspace_browser_access", true);
  }

  const result = {
    ok: true,
    status: "reused",
    runId,
    accountId: owner.accountId,
    workspaceId: slot.workspace.id,
    url: redactSensitiveUrl(slot.workspace.url),
    slot: {
      id: slotId,
      computeAllocationId: slot.compute.id,
      computeProviderResourceId: slot.computeProviderId,
      nodePoolId: slot.nodePoolId,
      storageId: slot.storage.id,
      storageProviderResourceId: slot.storageProviderId,
      persistentVolumeId: slot.persistentVolumeId
    },
    checks
  };
  await writeVerificationManifest(manifestPath, result);
  return result;
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
  if (["paid-confirmation", "package", "workspace"].some((key) => Object.hasOwn(args, key)) || env.OPL_VERIFY_PAID_CONFIRMATION || env.OPL_VERIFY_MODEL_ACCESS_KEY) {
    throw new Error("production_verifier_read_only");
  }
  return {
    origin: args.origin || env.OPL_CONSOLE_ORIGIN,
    authUsersJson: env.OPL_VERIFY_AUTH_USERS_JSON,
    accountId: args.account || env.OPL_VERIFY_ACCOUNT_ID || "",
    runId: args["run-id"] || env.OPL_VERIFY_RUN_ID || defaultRunId(),
    slotId: args.slot || env.OPL_VERIFY_SLOT_ID || FIXED_VERIFICATION_SLOT_ID,
    slotDescriptor: env.OPL_VERIFY_SLOT_DESCRIPTOR_JSON,
    purchaseBudgetRemaining: Number(env.OPL_VERIFY_PURCHASE_BUDGET_REMAINING ?? DEFAULT_PURCHASE_BUDGET_REMAINING),
    workspaceUrlAttempts: Number(args["url-attempts"] || env.OPL_VERIFY_URL_ATTEMPTS || DEFAULT_URL_ATTEMPTS),
    retryDelayMs: Number(args["retry-delay-ms"] || env.OPL_VERIFY_RETRY_DELAY_MS || DEFAULT_RETRY_DELAY_MS),
    manifestPath: args["manifest-path"] || env.OPL_VERIFY_MANIFEST_PATH || "",
    browserE2E: ["1", "true", "yes"].includes(String(args["browser-e2e"] || env.OPL_VERIFY_BROWSER_E2E || "").toLowerCase()),
    screenshotDir: args["screenshot-dir"] || env.OPL_VERIFY_SCREENSHOT_DIR || "",
    fetchImpl
  };
}

function errorPayload(error) {
  return withoutSecrets({ ok: false, error: error.message, accountId: error.accountId, runId: error.runId, checks: error.checks });
}

export async function runProductionVerifierCli({
  argv = process.argv.slice(2),
  env = process.env,
  stdout = process.stdout,
  stderr = process.stderr,
  fetchImpl = globalThis.fetch
} = {}) {
  if (argv.includes("--help") || argv.includes("-h")) {
    stdout.write(`Usage: npm run verify:production -- --origin <https-url> [--account <id>] [--run-id <id>] [--browser-e2e]\nRequires OPL_VERIFY_SLOT_DESCRIPTOR_JSON; read-only smoke reuses ${FIXED_VERIFICATION_SLOT_ID}.\n`);
    return 0;
  }
  try {
    const result = await verifyProductionChain(verifierOptionsFromArgs({ argv, env, fetchImpl }));
    stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return result.ok ? 0 : 2;
  } catch (error) {
    stderr.write(`${JSON.stringify(errorPayload(error), null, 2)}\n`);
    return 1;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] || "").href) {
  runProductionVerifierCli().then((code) => { process.exitCode = code; });
}
