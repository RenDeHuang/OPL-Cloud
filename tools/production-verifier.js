import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

const DEFAULT_ACCOUNT_ID = "pi-production-verifier";
const DEFAULT_WORKSPACE_NAME = "Production Verification Lab";
const DEFAULT_PACKAGE_ID = "basic";
const DEFAULT_CREDIT_AMOUNT = 1000;
const DEFAULT_WORKSPACE_URL_ATTEMPTS = 12;
const DEFAULT_RETRY_DELAY_MS = 5000;
const DEFAULT_MOUNT_PATH = "/data";
const WORKSPACE_PERSISTENCE_ROOT = "/data";
const DEFAULT_REQUEST_USAGE_AMOUNT = 0.42;

function defaultRunId() {
  const stamp = new Date().toISOString().replace(/[-:]/g, "").replace(/\..+$/, "Z");
  const suffix = Math.random().toString(36).slice(2, 8);
  return `${stamp}-${suffix}`;
}

function normalizeOrigin(origin) {
  if (!origin) throw new Error("origin_required");
  return origin.replace(/\/$/, "");
}

function isPrivateIpv4(hostname) {
  const parts = String(hostname || "").split(".").map((part) => Number(part));
  if (parts.length !== 4 || parts.some((part) => !Number.isInteger(part) || part < 0 || part > 255)) return false;
  const [first, second] = parts;
  return (
    first === 10 ||
    first === 127 ||
    (first === 172 && second >= 16 && second <= 31) ||
    (first === 192 && second === 168) ||
    (first === 169 && second === 254) ||
    first === 0
  );
}

function isNonPublicHostname(hostname) {
  const normalized = String(hostname || "").toLowerCase();
  return (
    normalized === "localhost" ||
    normalized.endsWith(".localhost") ||
    normalized === "::1" ||
    normalized === "0:0:0:0:0:0:0:1" ||
    normalized.startsWith("fc") ||
    normalized.startsWith("fd") ||
    normalized.startsWith("fe80") ||
    isPrivateIpv4(normalized)
  );
}

function assertPublicHttpsUrl(url, errorName) {
  let parsed = null;
  try {
    parsed = new URL(url);
  } catch {
    throw new Error(errorName);
  }
  if (parsed.protocol !== "https:" || isNonPublicHostname(parsed.hostname)) {
    throw new Error(errorName);
  }
  return parsed;
}

function assertConsoleOrigin(url, { allowPrivateConsoleOrigin = false } = {}) {
  if (!allowPrivateConsoleOrigin) return assertPublicHttpsUrl(url, "public_origin_required");
  let parsed = null;
  try {
    parsed = new URL(url);
  } catch {
    throw new Error("console_origin_required");
  }
  if (!["http:", "https:"].includes(parsed.protocol)) throw new Error("console_origin_required");
  return parsed;
}

function endpoint(origin, path) {
  return `${normalizeOrigin(origin)}${path}`;
}

async function readResponse(response) {
  const contentType = response.headers?.get?.("content-type") || "";
  if (contentType.includes("application/json")) return response.json();
  return response.text();
}

function authHeaderValues(auth = null) {
  const headers = {};
  if (auth?.cookie) headers.cookie = auth.cookie;
  if (auth?.csrf) headers["x-opl-csrf-token"] = auth.csrf;
  return headers;
}

function requestHeaders({ body = null, auth = null } = {}) {
  const headers = {
    ...(body ? { "content-type": "application/json" } : {}),
    ...authHeaderValues(auth)
  };
  return Object.keys(headers).length > 0 ? headers : undefined;
}

async function requestJsonWithResponse({ fetchImpl, origin, path, method = "GET", body = null, auth = null }) {
  const response = await fetchImpl(endpoint(origin, path), {
    method,
    headers: requestHeaders({ body, auth }),
    body: body ? JSON.stringify(body) : undefined
  });
  const payload = await readResponse(response);
  if (!response.ok) {
    const message = typeof payload === "string" ? payload : payload.error || JSON.stringify(payload);
    const error = new Error(`request_failed:${method}:${path}:${response.status}:${message}`);
    if (payload && typeof payload === "object") {
      error.safeMessage = payload.safeMessage || "";
      error.providerRequestId = payload.providerRequestId || payload.provider?.requestId || "";
      if (typeof payload.retryable === "boolean") error.retryable = payload.retryable;
      if (Array.isArray(payload.missingEnv)) error.missingEnv = payload.missingEnv;
    }
    throw error;
  }
  return { payload, response };
}

async function requestJson(args) {
  const { payload } = await requestJsonWithResponse(args);
  return payload;
}

function cookieHeaderFromSetCookie(setCookie = "") {
  return String(setCookie)
    .split(/,(?=[^;,]+=)/)
    .map((cookie) => cookie.split(";")[0]?.trim())
    .filter(Boolean)
    .join("; ");
}

async function requestOperatorSession({ fetchImpl, origin, operatorToken }) {
  if (!operatorToken) return null;
  const { payload, response } = await requestJsonWithResponse({
    fetchImpl,
    origin,
    path: "/api/auth/operator-login",
    method: "POST",
    body: { operatorToken }
  });
  return {
    cookie: cookieHeaderFromSetCookie(response.headers?.get?.("set-cookie") || ""),
    csrf: response.headers?.get?.("x-opl-csrf-token") || payload?.csrfToken || ""
  };
}

function sleep(ms) {
  if (ms <= 0) return Promise.resolve();
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function requestWorkspaceUrl({ fetchImpl, url, attempts, retryDelayMs }) {
  let lastError = null;
  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    const response = await fetchImpl(url, { method: "GET" });
    const body = await response.text();
    if (response.ok) return { body, attempts: attempt };
    lastError = new Error(`workspace_url_failed:${response.status}:${body}`);
    if (attempt < attempts) await sleep(retryDelayMs);
  }
  throw lastError;
}

function workspaceApiUrl(workspaceUrl, path) {
  const parsed = new URL(workspaceUrl);
  const normalizedPath = String(path || "").replace(/^\//, "");
  parsed.pathname = `${parsed.pathname.replace(/\/$/, "")}/${normalizedPath}`;
  return parsed.toString();
}

async function requestWorkspaceJson({ fetchImpl, workspaceUrl, path, method = "GET", body = null }) {
  const response = await fetchImpl(workspaceApiUrl(workspaceUrl, path), {
    method,
    headers: body ? { "content-type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined
  });
  const payload = await readResponse(response);
  if (!response.ok) {
    const message = typeof payload === "string" ? payload : payload.error || JSON.stringify(payload);
    throw new Error(`workspace_api_failed:${method}:${path}:${response.status}:${message}`);
  }
  return payload;
}

function runtimePayloadData(payload) {
  if (payload && typeof payload === "object" && Object.hasOwn(payload, "data")) return payload.data;
  return payload;
}

async function verifyWorkspaceRuntimeFile({ fetchImpl, checks, workspaceUrl, runId }) {
  const filePath = `${WORKSPACE_PERSISTENCE_ROOT}/opl-e2e-${runId}.txt`;
  const content = `opl persistence ${runId}`;
  const user = await requestWorkspaceJson({
    fetchImpl,
    workspaceUrl,
    path: "/api/auth/user"
  });
  addCheck(checks, "workspace_runtime_auth", Boolean(
    user?.success === true ||
    user?.user?.id ||
    user?.data?.user?.id
  ));

  const written = await requestWorkspaceJson({
    fetchImpl,
    workspaceUrl,
    path: "/api/fs/write",
    method: "POST",
    body: { path: filePath, data: content }
  });
  addCheck(checks, "workspace_file_written", Boolean(
    written?.success === true ||
    runtimePayloadData(written) === true
  ), { path: filePath });

  const read = await requestWorkspaceJson({
    fetchImpl,
    workspaceUrl,
    path: "/api/fs/read",
    method: "POST",
    body: { path: filePath, workspace: WORKSPACE_PERSISTENCE_ROOT }
  });
  addCheck(checks, "workspace_file_read", runtimePayloadData(read) === content, { path: filePath });
  return { filePath, content };
}

async function verifyWorkspacePersistedFile({ fetchImpl, checks, workspaceUrl, fileProof }) {
  const read = await requestWorkspaceJson({
    fetchImpl,
    workspaceUrl,
    path: "/api/fs/read",
    method: "POST",
    body: { path: fileProof.filePath, workspace: WORKSPACE_PERSISTENCE_ROOT }
  });
  addCheck(checks, "workspace_persisted_file_read", runtimePayloadData(read) === fileProof.content, { path: fileProof.filePath });
}

async function defaultBrowserFactory() {
  try {
    return await import("playwright");
  } catch {
    throw new Error("playwright_required_for_browser_e2e");
  }
}

async function writeBrowserUploadFixture({ runId }) {
  const dir = await mkdtemp(join(tmpdir(), "opl-browser-e2e-"));
  const fileName = `opl-browser-e2e-${runId}.txt`;
  const filePath = join(dir, fileName);
  const content = `OPL_BROWSER_FILE_${runId}`;
  await writeFile(filePath, content, "utf8");
  return { fileName, filePath, content };
}

async function captureBrowserScreenshot({ page, screenshotDir, runId, suffix }) {
  if (!page || !screenshotDir) return "";
  const screenshotPath = join(screenshotDir, `workspace-browser-e2e-${runId}-${suffix}.png`);
  try {
    await mkdir(screenshotDir, { recursive: true });
    await page.screenshot({ path: screenshotPath, fullPage: true });
    return screenshotPath;
  } catch {
    return "";
  }
}

async function runBrowserCheck({ page, checks, name, screenshotDir, runId, successDetails = {}, recordSuccess = true, task }) {
  try {
    const result = await task();
    if (recordSuccess) addCheck(checks, name, true, successDetails);
    return result;
  } catch (cause) {
    const screenshotPath = await captureBrowserScreenshot({ page, screenshotDir, runId, suffix: "failure" });
    const details = {
      stage: name,
      ...(screenshotPath ? { screenshotPath } : {})
    };
    checks.push({
      name,
      ok: false,
      ...details,
      error: cause?.message || String(cause)
    });
    const error = new Error(`${name}_failed:${cause?.message || String(cause)}`);
    error.cause = cause;
    error.details = details;
    throw error;
  }
}

async function requireFirstFileInput(page) {
  const input = page.locator('input[type="file"]').first();
  if (await input.count() < 1) throw new Error("workspace_browser_file_input_missing");
  return input;
}

async function clickSendControl(page) {
  try {
    await page.getByRole("button", { name: /发送|Send|提交|运行|Ask/i }).first().click({ timeout: 15_000 });
  } catch (error) {
    if (page.keyboard?.press) {
      await page.keyboard.press("Enter");
      return;
    }
    throw error;
  }
}

export async function verifyWorkspaceBrowserUi({
  workspaceUrl,
  runId,
  checks,
  browserFactory = null,
  screenshotDir = "",
  launchOptions = { headless: true }
} = {}) {
  if (!workspaceUrl) throw new Error("workspace_url_required");
  if (!runId) throw new Error("run_id_required");
  const factory = browserFactory || await defaultBrowserFactory();
  if (!factory?.chromium?.launch) throw new Error("playwright_chromium_required");
  const browser = await factory.chromium.launch(launchOptions);
  try {
    const page = await browser.newPage();
    await runBrowserCheck({
      page,
      checks,
      name: "workspace_browser_opened",
      screenshotDir,
      runId,
      successDetails: { url: workspaceUrl },
      task: () => page.goto(workspaceUrl, { waitUntil: "networkidle", timeout: 120_000 })
    });

    const fixture = await writeBrowserUploadFixture({ runId });
    const fileInput = await runBrowserCheck({
      page,
      checks,
      name: "workspace_browser_file_input",
      screenshotDir,
      runId,
      recordSuccess: false,
      task: () => requireFirstFileInput(page)
    });
    await runBrowserCheck({
      page,
      checks,
      name: "workspace_browser_file_uploaded",
      screenshotDir,
      runId,
      successDetails: { fileName: fixture.fileName },
      task: () => fileInput.setInputFiles(fixture.filePath)
    });

    await runBrowserCheck({
      page,
      checks,
      name: "workspace_browser_file_read",
      screenshotDir,
      runId,
      successDetails: { fileName: fixture.fileName },
      task: () => page.waitForFunction(({ fileName, content }) => {
        const text = document.body?.innerText || "";
        return text.includes(fileName) || text.includes(content);
      }, { fileName: fixture.fileName, content: fixture.content }, { timeout: 60_000 })
    });

    const marker = `OPL_BROWSER_E2E_${runId}`;
    const prompt = `请只回复：${marker}`;
    await runBrowserCheck({
      page,
      checks,
      name: "workspace_browser_message_sent",
      screenshotDir,
      runId,
      task: async () => {
        await page.getByRole("textbox").first().fill(prompt);
        await clickSendControl(page);
      }
    });

    await runBrowserCheck({
      page,
      checks,
      name: "workspace_browser_reply_seen",
      screenshotDir,
      runId,
      successDetails: { marker },
      task: () => page.waitForFunction(({ marker: expected }) => {
        return (document.body?.innerText || "").includes(expected);
      }, { marker }, { timeout: 180_000 })
    });

    await captureBrowserScreenshot({ page, screenshotDir, runId, suffix: "success" });
  } finally {
    await browser.close();
  }
}

async function requestRuntimeStatus({ fetchImpl, origin, accountId, workspaceId, attempts, retryDelayMs, auth = null }) {
  let lastStatus = null;
  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    const status = await requestJson({
      fetchImpl,
      origin,
      path: "/api/workspaces/runtime-status",
      method: "POST",
      auth,
      body: { accountId, workspaceId }
    });
    lastStatus = { ...status, attempts: attempt };
    if (
      status?.ready === true &&
      Array.isArray(status.checks) &&
      status.checks.length > 0 &&
      status.checks.every((check) => check.ok === true)
    ) {
      return lastStatus;
    }
    if (attempt < attempts) await sleep(retryDelayMs);
  }
  return lastStatus;
}

function addCheck(checks, name, ok, details = {}) {
  const check = { name, ok: Boolean(ok), ...details };
  checks.push(check);
  if (!check.ok) {
    const error = new Error(`${name}_failed`);
    error.details = details;
    throw error;
  }
  return check;
}

function assertReady({ checks, name, payload }) {
  if (!payload.ready) {
    const failed = payload.failedChecks?.length ? payload.failedChecks.join(",") : "unknown";
    throw new Error(`${name}_not_ready:${failed}`);
  }
  addCheck(checks, name, true);
}

function assertComputeShape(checks, compute, name = "compute_created") {
  addCheck(checks, name, Boolean(
    compute?.id &&
    compute?.provider === "tencent-tke" &&
    compute?.nodeName &&
    (compute?.instanceId || compute?.providerResourceId) &&
    compute?.status === "running" &&
    compute?.billingStatus === "active"
  ), { computeAllocationId: compute?.id });
}

async function waitForComputeReady({
  fetchImpl,
  origin,
  accountId,
  compute,
  attempts,
  retryDelayMs,
  auth = null,
  checks,
  name = "compute_created"
}) {
  let current = compute;
  for (let attempt = 0; attempt <= attempts; attempt += 1) {
    if (
      current?.id &&
      current?.provider === "tencent-tke" &&
      current?.nodeName &&
      (current?.instanceId || current?.providerResourceId) &&
      current?.status === "running" &&
      current?.billingStatus === "active"
    ) {
      addCheck(checks, name, true, {
        computeAllocationId: current.id,
        nodeName: current.nodeName,
        attempts: attempt + 1
      });
      return current;
    }
    if (!current?.id) break;
    if (attempt >= attempts) break;
    if (attempt > 0) await sleep(retryDelayMs);
    current = await requestJson({
      fetchImpl,
      origin,
      path: `/api/compute-allocations/${encodeURIComponent(current.id)}?accountId=${encodeURIComponent(accountId)}`,
      auth
    });
  }
  assertComputeShape(checks, current, name);
  return current;
}

function assertStorageShape(checks, storage) {
  addCheck(checks, "storage_created", Boolean(
    storage?.id &&
    storage?.provider === "tencent-tke" &&
    storage?.providerResourceId?.startsWith("pvc/") &&
    storage?.status === "available" &&
    storage?.billingStatus === "active" &&
    Number(storage?.sizeGb || 0) > 0
  ), { storageId: storage?.id });
}

function assertAttachmentShape(checks, attachment, { compute, storage }, name = "storage_attached") {
  addCheck(checks, name, Boolean(
    attachment?.id &&
    attachment?.provider === "tencent-tke" &&
    attachment?.computeAllocationId === compute?.id &&
    attachment?.storageId === storage?.id &&
    attachment?.mountPath === DEFAULT_MOUNT_PATH &&
    attachment?.status === "attached"
  ), { attachmentId: attachment?.id });
}

function assertWorkspaceShape(checks, workspace, { compute, storage, attachment }, name = "workspace_created") {
  addCheck(checks, name, Boolean(
    workspace?.id &&
    workspace?.provider === "tencent-tke" &&
    workspace?.computeAllocationId === compute?.id &&
    workspace?.storageId === storage?.id &&
    workspace?.attachmentId === attachment?.id &&
    workspace?.url &&
    workspace?.access?.tokenStatus === "active"
  ), { workspaceId: workspace?.id });
}

function assertRuntimeStatus(checks, runtimeStatus, name = "workspace_runtime_status") {
  const runtimeChecks = (runtimeStatus?.checks || []).map((check) => ({
    name: check?.name,
    ok: check?.ok === true
  }));
  const failedChecks = runtimeChecks.filter((check) => !check.ok).map((check) => check.name);
  addCheck(checks, name, Boolean(
    runtimeStatus?.ready === true &&
    Array.isArray(runtimeStatus.checks) &&
    runtimeStatus.checks.length > 0 &&
    runtimeStatus.checks.every((check) => check.ok === true)
  ), {
    runtimeChecks,
    failedChecks,
    attempts: runtimeStatus?.attempts
  });
}

function assertRequestUsage(checks, usage, workspace) {
  addCheck(checks, "request_usage_recorded", Boolean(
    usage?.id &&
    usage?.accountId === workspace?.ownerAccountId &&
    usage?.workspaceId === workspace?.id &&
    usage?.requestId
  ), { usageId: usage?.id });
}

function assertResourceBillingSettlement(checks, settlement, { accountId, compute, storage }) {
  const entries = settlement?.entries || [];
  const hasComputeDebit = entries.some((entry) =>
    entry.accountId === accountId &&
    entry.computeAllocationId === compute?.id &&
    entry.type === "compute_debit"
  );
  const hasStorageDebit = entries.some((entry) =>
    entry.accountId === accountId &&
    entry.storageId === storage?.id &&
    entry.type === "storage_debit"
  );

  addCheck(checks, "resource_billing_settled", Boolean(hasComputeDebit && hasStorageDebit), {
    entryTypes: entries.map((entry) => entry.type)
  });
}

function assertLedgerAndUsage(checks, state, { accountId, compute, storage, attachment, workspace, requestUsage }) {
  const ledger = state?.billingLedger || [];
  const resourceUsage = state?.resourceUsageLogs || [];
  const requestUsageLogs = state?.requestUsageLogs || [];
  const walletTransactions = state?.walletTransactions || [];

  const hasComputeLedger = ledger.some((entry) =>
    entry.accountId === accountId &&
    entry.computeAllocationId === compute?.id &&
    entry.type === "compute_debit"
  );
  const hasStorageLedger = ledger.some((entry) =>
    entry.accountId === accountId &&
    entry.storageId === storage?.id &&
    entry.type === "storage_debit"
  );
  const hasAttachmentLedger = ledger.some((entry) => entry.accountId === accountId && entry.attachmentId === attachment?.id);
  const hasRequestLedger = ledger.some((entry) =>
    entry.accountId === accountId &&
    entry.workspaceId === workspace?.id &&
    entry.type === "request_debit"
  );
  const hasComputeUsage = resourceUsage.some((entry) =>
    entry.accountId === accountId &&
    entry.computeAllocationId === compute?.id &&
    entry.resourceType === "compute"
  );
  const hasStorageUsage = resourceUsage.some((entry) =>
    entry.accountId === accountId &&
    entry.storageId === storage?.id &&
    entry.resourceType === "storage"
  );
  const hasAttachmentUsage = resourceUsage.some((entry) => entry.accountId === accountId && entry.attachmentId === attachment?.id);
  const hasRequestUsage = requestUsageLogs.some((entry) =>
    entry.accountId === accountId &&
    entry.workspaceId === workspace?.id &&
    (entry.id === requestUsage?.id || entry.requestId === requestUsage?.requestId)
  );
  const hasComputeWalletTransaction = walletTransactions.some((entry) =>
    entry.accountId === accountId &&
    entry.metadata?.computeAllocationId === compute?.id &&
    entry.type === "compute_debit"
  );
  const hasStorageWalletTransaction = walletTransactions.some((entry) =>
    entry.accountId === accountId &&
    entry.metadata?.storageId === storage?.id &&
    entry.type === "storage_debit"
  );
  const hasRequestWalletTransaction = walletTransactions.some((entry) =>
    entry.accountId === accountId &&
    entry.workspaceId === workspace?.id &&
    entry.type === "request_debit"
  );
  const missingChecks = [
    [hasComputeLedger, "compute_ledger"],
    [hasStorageLedger, "storage_ledger"],
    [hasAttachmentLedger, "attachment_ledger"],
    [hasRequestLedger, "request_ledger"],
    [hasComputeUsage, "compute_usage"],
    [hasStorageUsage, "storage_usage"],
    [hasAttachmentUsage, "attachment_usage"],
    [hasRequestUsage, "request_usage"],
    [hasComputeWalletTransaction, "compute_wallet_transaction"],
    [hasStorageWalletTransaction, "storage_wallet_transaction"],
    [hasRequestWalletTransaction, "request_wallet_transaction"]
  ].filter(([ok]) => !ok).map(([, name]) => name);

  addCheck(checks, "ledger_and_usage_verified", Boolean(
    state?.wallet?.accountId === accountId &&
    hasComputeLedger &&
    hasStorageLedger &&
    hasAttachmentLedger &&
    hasRequestLedger &&
    hasComputeUsage &&
    hasStorageUsage &&
    hasAttachmentUsage &&
    hasRequestUsage &&
    hasComputeWalletTransaction &&
    hasStorageWalletTransaction &&
    hasRequestWalletTransaction
  ), { missingChecks });
}

function compactObject(payload) {
  return Object.fromEntries(Object.entries(payload).filter(([, value]) => value));
}

function verificationResourceIds({ compute, storage, attachment, workspace, replacementCompute, replacementAttachment, replacementWorkspace }) {
  return compactObject({
    computeAllocationId: compute?.id,
    storageId: storage?.id,
    attachmentId: attachment?.id,
    workspaceId: workspace?.id,
    replacementComputeAllocationId: replacementCompute?.id,
    replacementAttachmentId: replacementAttachment?.id,
    replacementWorkspaceId: replacementWorkspace?.id
  });
}

async function cleanupVerificationResources({ fetchImpl, origin, accountId, computeAllocationId, storageId, attachmentId, checks = null, auth = null }) {
  const cleanupErrors = [];

  if (attachmentId) {
    try {
      const detached = await requestJson({
        fetchImpl,
        origin,
        path: "/api/storage-attachments/detach",
        method: "POST",
        auth,
        body: { accountId, attachmentId, confirm: true }
      });
      if (checks) {
        addCheck(checks, "verification_storage_detached", Boolean(detached?.status === "detached"));
      }
    } catch (error) {
      cleanupErrors.push(`detach_storage:${error.message}`);
    }
  }

  if (computeAllocationId) {
    try {
      const destroyed = await requestJson({
        fetchImpl,
        origin,
        path: `/api/compute-allocations/${encodeURIComponent(computeAllocationId)}/destroy`,
        method: "POST",
        auth,
        body: { accountId, computeAllocationId, confirm: true }
      });
      if (checks) {
        addCheck(checks, "verification_compute_destroyed", Boolean(
          destroyed?.status === "destroyed" &&
          destroyed?.billingStatus === "stopped"
        ));
      }
    } catch (error) {
      cleanupErrors.push(`destroy_compute:${error.message}`);
    }
  }

  if (storageId) {
    try {
      const destroyed = await requestJson({
        fetchImpl,
        origin,
        path: "/api/storage-volumes/destroy",
        method: "POST",
        auth,
        body: { accountId, storageId, confirmDataLoss: true }
      });
      if (checks) {
        addCheck(checks, "verification_storage_destroyed", Boolean(
          destroyed?.status === "destroyed" &&
          destroyed?.billingStatus === "stopped"
        ));
      }
    } catch (error) {
      cleanupErrors.push(`destroy_storage:${error.message}`);
    }
  }

  return cleanupErrors;
}

export async function verifyProductionChain({
  origin,
  accountId = DEFAULT_ACCOUNT_ID,
  workspaceName,
  runId = defaultRunId(),
  packageId = DEFAULT_PACKAGE_ID,
  creditAmount = DEFAULT_CREDIT_AMOUNT,
  workspaceUrlAttempts = DEFAULT_WORKSPACE_URL_ATTEMPTS,
  retryDelayMs = DEFAULT_RETRY_DELAY_MS,
  operatorToken = "",
  allowPrivateConsoleOrigin = false,
  browserE2E = false,
  browserFactory = null,
  screenshotDir = "",
  fetchImpl = globalThis.fetch
} = {}) {
  if (typeof fetchImpl !== "function") throw new Error("fetch_required");
  const checks = [];
  const normalizedOrigin = normalizeOrigin(origin);
  assertConsoleOrigin(normalizedOrigin, { allowPrivateConsoleOrigin });
  const effectiveWorkspaceName = workspaceName || `${DEFAULT_WORKSPACE_NAME} ${runId}`;
  const creditSourceEventId = `production_verification_credit:${runId}`;
  const requestUsageSourceEventId = `production_verification_request_usage:${runId}`;
  const requestId = `production-verification-request:${runId}`;
  const computeName = `${effectiveWorkspaceName} compute ${runId}`;
  const storageName = `${effectiveWorkspaceName} storage ${runId}`;
  let compute = null;
  let storage = null;
  let attachment = null;
  let workspace = null;
  let replacementCompute = null;
  let replacementAttachment = null;
  let replacementWorkspace = null;
  let auth = null;
  let firstComputeForLedger = null;
  let firstAttachmentForLedger = null;

  try {
    const productionReadiness = await requestJson({ fetchImpl, origin: normalizedOrigin, path: "/api/production/readiness" });
    assertReady({ checks, name: "production_readiness", payload: productionReadiness });

    const runtimeReadiness = await requestJson({ fetchImpl, origin: normalizedOrigin, path: "/api/runtime/readiness" });
    assertReady({ checks, name: "runtime_readiness", payload: runtimeReadiness });

    auth = await requestOperatorSession({ fetchImpl, origin: normalizedOrigin, operatorToken });

    await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/billing/topups",
      method: "POST",
      auth,
      body: { accountId, amount: creditAmount, reason: creditSourceEventId }
    });

    compute = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/compute-allocations",
      method: "POST",
      auth,
      body: { accountId, packageId, name: computeName }
    });
    compute = await waitForComputeReady({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      compute,
      attempts: workspaceUrlAttempts,
      retryDelayMs,
      auth,
      checks
    });

    storage = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/storage-volumes",
      method: "POST",
      auth,
      body: { accountId, packageId, name: storageName }
    });
    assertStorageShape(checks, storage);

    attachment = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/storage-attachments",
      method: "POST",
      auth,
      body: {
        accountId,
        computeAllocationId: compute.id,
        storageId: storage.id,
        mountPath: DEFAULT_MOUNT_PATH
      }
    });
    assertAttachmentShape(checks, attachment, { compute, storage });

    workspace = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/workspaces",
      method: "POST",
      auth,
      body: { accountId, workspaceName: effectiveWorkspaceName, attachmentId: attachment.id }
    });
    assertWorkspaceShape(checks, workspace, { compute, storage, attachment });

    const runtimeStatus = await requestRuntimeStatus({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      workspaceId: workspace.id,
      attempts: workspaceUrlAttempts,
      retryDelayMs,
      auth
    });
    assertRuntimeStatus(checks, runtimeStatus);

    assertPublicHttpsUrl(workspace.url, "public_workspace_url_required");
    const workspaceUrlResult = await requestWorkspaceUrl({
      fetchImpl,
      url: workspace.url,
      attempts: workspaceUrlAttempts,
      retryDelayMs
    });
    addCheck(checks, "workspace_url", true, { url: workspace.url, attempts: workspaceUrlResult.attempts });
    if (browserE2E) {
      await verifyWorkspaceBrowserUi({
        workspaceUrl: workspace.url,
        runId,
        checks,
        browserFactory,
        screenshotDir
      });
    }
    const fileProof = await verifyWorkspaceRuntimeFile({ fetchImpl, checks, workspaceUrl: workspace.url, runId });

    firstComputeForLedger = compute;
    firstAttachmentForLedger = attachment;
    const firstCleanupErrors = await cleanupVerificationResources({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      computeAllocationId: compute.id,
      attachmentId: attachment.id,
      checks,
      auth
    });
    if (firstCleanupErrors.length > 0) {
      const error = new Error(`production_verification_cleanup_failed:${firstCleanupErrors.join("|")}`);
      error.cleanupErrors = firstCleanupErrors;
      throw error;
    }
    compute = null;
    attachment = null;

    replacementCompute = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/compute-allocations",
      method: "POST",
      auth,
      body: { accountId, packageId, name: `${effectiveWorkspaceName} replacement compute ${runId}` }
    });
    replacementCompute = await waitForComputeReady({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      compute: replacementCompute,
      attempts: workspaceUrlAttempts,
      retryDelayMs,
      auth,
      checks,
      name: "replacement_compute_created"
    });

    replacementAttachment = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/storage-attachments",
      method: "POST",
      auth,
      body: {
        accountId,
        computeAllocationId: replacementCompute.id,
        storageId: storage.id,
        mountPath: DEFAULT_MOUNT_PATH
      }
    });
    assertAttachmentShape(checks, replacementAttachment, { compute: replacementCompute, storage }, "replacement_storage_attached");

    replacementWorkspace = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/workspaces",
      method: "POST",
      auth,
      body: { accountId, workspaceName: effectiveWorkspaceName, attachmentId: replacementAttachment.id }
    });
    assertWorkspaceShape(checks, replacementWorkspace, {
      compute: replacementCompute,
      storage,
      attachment: replacementAttachment
    }, "replacement_workspace_created");

    const replacementRuntimeStatus = await requestRuntimeStatus({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      workspaceId: replacementWorkspace.id,
      attempts: workspaceUrlAttempts,
      retryDelayMs,
      auth
    });
    assertRuntimeStatus(checks, replacementRuntimeStatus, "replacement_workspace_runtime_status");

    assertPublicHttpsUrl(replacementWorkspace.url, "public_workspace_url_required");
    const replacementWorkspaceUrlResult = await requestWorkspaceUrl({
      fetchImpl,
      url: replacementWorkspace.url,
      attempts: workspaceUrlAttempts,
      retryDelayMs
    });
    addCheck(checks, "replacement_workspace_url", true, {
      url: replacementWorkspace.url,
      attempts: replacementWorkspaceUrlResult.attempts
    });
    await verifyWorkspacePersistedFile({
      fetchImpl,
      checks,
      workspaceUrl: replacementWorkspace.url,
      fileProof
    });

    const requestUsage = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/billing/request-usage",
      method: "POST",
      auth,
      body: {
        accountId,
        workspaceId: workspace.id,
        requestId,
        provider: "sub2api",
        model: "production-verification",
        inputTokens: 1,
        outputTokens: 1,
        amount: DEFAULT_REQUEST_USAGE_AMOUNT,
        sourceEventId: requestUsageSourceEventId
      }
    });
    assertRequestUsage(checks, requestUsage, workspace);

    const resourceSettlement = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/billing/resource-settlements",
      method: "POST",
      auth,
      body: {
        accountId,
        hours: 1,
        sourceEventId: `production_verification_resource_settlement:${runId}`
      }
    });
    assertResourceBillingSettlement(checks, resourceSettlement, {
      accountId,
      compute: replacementCompute,
      storage
    });

    const state = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: `/api/state?accountId=${encodeURIComponent(accountId)}`,
      auth
    });
    assertLedgerAndUsage(checks, state, {
      accountId,
      compute: replacementCompute,
      storage,
      attachment: firstAttachmentForLedger || attachment,
      workspace,
      requestUsage
    });

    const cleanupErrors = await cleanupVerificationResources({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      computeAllocationId: replacementCompute.id,
      storageId: storage.id,
      attachmentId: replacementAttachment.id,
      checks,
      auth
    });
    if (cleanupErrors.length > 0) {
      const error = new Error(`production_verification_cleanup_failed:${cleanupErrors.join("|")}`);
      error.cleanupErrors = cleanupErrors;
      throw error;
    }

    return {
      ok: true,
      accountId,
      runId,
      workspaceId: workspace.id,
      workspaceName: effectiveWorkspaceName,
      url: workspace.url,
      checks
    };
  } catch (error) {
    if (!compute?.id && !storage?.id && !attachment?.id && !replacementCompute?.id && !replacementAttachment?.id) throw error;
    error.accountId = accountId;
    error.runId = runId;
    error.workspaceName = effectiveWorkspaceName;
    error.resourceIds = verificationResourceIds({
      compute,
      storage,
      attachment,
      workspace,
      replacementCompute,
      replacementAttachment,
      replacementWorkspace
    });
    error.checks = checks;
    const cleanupErrors = [];
    if (replacementCompute?.id || replacementAttachment?.id) {
      const replacementCleanupErrors = await cleanupVerificationResources({
        fetchImpl,
        origin: normalizedOrigin,
        accountId,
        computeAllocationId: replacementCompute?.id,
        attachmentId: replacementAttachment?.id,
        auth
      });
      cleanupErrors.push(...replacementCleanupErrors);
    }
    if (compute?.id || attachment?.id) {
      const primaryCleanupErrors = await cleanupVerificationResources({
        fetchImpl,
        origin: normalizedOrigin,
        accountId,
        computeAllocationId: compute?.id,
        attachmentId: attachment?.id,
        auth
      });
      cleanupErrors.push(...primaryCleanupErrors);
    }
    if (storage?.id) {
      const storageCleanupErrors = await cleanupVerificationResources({
        fetchImpl,
        origin: normalizedOrigin,
        accountId,
        storageId: storage.id,
        auth
      });
      cleanupErrors.push(...storageCleanupErrors);
    }
    if (cleanupErrors.length > 0) error.cleanupErrors = [...(error.cleanupErrors || []), ...cleanupErrors];
    throw error;
  }
}

function cliArgs(argv) {
  const args = {};
  for (let index = 0; index < argv.length; index += 1) {
    const item = argv[index];
    if (!item.startsWith("--")) continue;
    const key = item.slice(2);
    const value = argv[index + 1] && !argv[index + 1].startsWith("--") ? argv[++index] : "true";
    args[key] = value;
  }
  return args;
}

function verifierOptionsFromArgs({ argv, env = process.env, fetchImpl = globalThis.fetch }) {
  const args = cliArgs(argv);
  return {
    origin: args.origin || env.OPL_CONSOLE_ORIGIN,
    accountId: args.account || env.OPL_VERIFY_ACCOUNT_ID || DEFAULT_ACCOUNT_ID,
    workspaceName: args.workspace || env.OPL_VERIFY_WORKSPACE_NAME,
    runId: args["run-id"] || env.OPL_VERIFY_RUN_ID,
    packageId: args.package || env.OPL_VERIFY_PACKAGE_ID || DEFAULT_PACKAGE_ID,
    creditAmount: Number(args.credit || env.OPL_VERIFY_CREDIT_AMOUNT || DEFAULT_CREDIT_AMOUNT),
    workspaceUrlAttempts: Number(args["url-attempts"] || env.OPL_VERIFY_URL_ATTEMPTS || DEFAULT_WORKSPACE_URL_ATTEMPTS),
    retryDelayMs: Number(args["retry-delay-ms"] || env.OPL_VERIFY_RETRY_DELAY_MS || DEFAULT_RETRY_DELAY_MS),
    operatorToken: args["operator-token"] || env.OPL_VERIFY_OPERATOR_TOKEN || "",
    browserE2E: ["1", "true"].includes(String(args["browser-e2e"] || env.OPL_VERIFY_BROWSER_E2E || "").toLowerCase()),
    screenshotDir: args["screenshot-dir"] || env.OPL_VERIFY_SCREENSHOT_DIR || "",
    fetchImpl
  };
}

function errorPayload(error) {
  return {
    ok: false,
    error: error.message,
    ...(error.safeMessage ? { safeMessage: error.safeMessage } : {}),
    ...(error.providerRequestId ? { providerRequestId: error.providerRequestId } : {}),
    ...(typeof error.retryable === "boolean" ? { retryable: error.retryable } : {}),
    ...(Array.isArray(error.missingEnv) ? { missingEnv: error.missingEnv } : {}),
    ...(error.details ? { details: error.details } : {}),
    ...(error.resourceIds ? { resourceIds: error.resourceIds } : {}),
    ...(error.accountId ? { accountId: error.accountId } : {}),
    ...(error.runId ? { runId: error.runId } : {}),
    ...(error.workspaceName ? { workspaceName: error.workspaceName } : {}),
    ...(error.checks ? { checks: error.checks } : {}),
    ...(error.cleanupErrors ? { cleanupErrors: error.cleanupErrors } : {})
  };
}

export async function runProductionVerifierCli({
  argv = process.argv.slice(2),
  env = process.env,
  stdout = process.stdout,
  stderr = process.stderr,
  fetchImpl = globalThis.fetch
} = {}) {
  try {
    const result = await verifyProductionChain(verifierOptionsFromArgs({ argv, env, fetchImpl }));
    stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return 0;
  } catch (error) {
    stderr.write(`${JSON.stringify(errorPayload(error), null, 2)}\n`);
    return 1;
  }
}

if (import.meta.url === `file://${process.argv[1]}`) {
  runProductionVerifierCli().then((code) => {
    process.exitCode = code;
  });
}
