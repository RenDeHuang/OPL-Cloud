const DEFAULT_ACCOUNT_ID = "pi-production-verifier";
const DEFAULT_WORKSPACE_NAME = "Production Verification Lab";
const DEFAULT_PACKAGE_ID = "basic";
const DEFAULT_CREDIT_AMOUNT = 1000;
const DEFAULT_WORKSPACE_URL_ATTEMPTS = 12;
const DEFAULT_RETRY_DELAY_MS = 5000;
const DEFAULT_MOUNT_PATH = "/data";
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
    throw new Error(`request_failed:${method}:${path}:${response.status}:${message}`);
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
  if (!check.ok) throw new Error(`${name}_failed`);
  return check;
}

function assertReady({ checks, name, payload }) {
  if (!payload.ready) {
    const failed = payload.failedChecks?.length ? payload.failedChecks.join(",") : "unknown";
    throw new Error(`${name}_not_ready:${failed}`);
  }
  addCheck(checks, name, true);
}

function assertComputeShape(checks, compute) {
  addCheck(checks, "compute_created", Boolean(
    compute?.id &&
    compute?.provider === "tencent-tke" &&
    compute?.providerResourceId?.startsWith("deployment/") &&
    compute?.status === "running" &&
    compute?.billingStatus === "active" &&
    compute?.image
  ), { computeId: compute?.id });
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

function assertAttachmentShape(checks, attachment, { compute, storage }) {
  addCheck(checks, "storage_attached", Boolean(
    attachment?.id &&
    attachment?.provider === "tencent-tke" &&
    attachment?.computeId === compute?.id &&
    attachment?.storageId === storage?.id &&
    attachment?.mountPath === DEFAULT_MOUNT_PATH &&
    attachment?.status === "attached"
  ), { attachmentId: attachment?.id });
}

function assertWorkspaceShape(checks, workspace, { compute, storage, attachment }) {
  addCheck(checks, "workspace_created", Boolean(
    workspace?.id &&
    workspace?.provider === "tencent-tke" &&
    workspace?.computeId === compute?.id &&
    workspace?.storageId === storage?.id &&
    workspace?.attachmentId === attachment?.id &&
    workspace?.url &&
    workspace?.access?.tokenStatus === "active"
  ), { workspaceId: workspace?.id });
}

function assertRuntimeStatus(checks, runtimeStatus) {
  addCheck(checks, "workspace_runtime_status", Boolean(
    runtimeStatus?.ready === true &&
    Array.isArray(runtimeStatus.checks) &&
    runtimeStatus.checks.length > 0 &&
    runtimeStatus.checks.every((check) => check.ok === true)
  ), {
    runtimeChecks: (runtimeStatus?.checks || []).map((check) => check.name),
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

function assertLedgerAndUsage(checks, state, { accountId, compute, storage, attachment, workspace, requestUsage }) {
  const ledger = state?.billingLedger || [];
  const resourceUsage = state?.resourceUsageLogs || [];
  const requestUsageLogs = state?.requestUsageLogs || [];

  const hasComputeLedger = ledger.some((entry) => entry.accountId === accountId && entry.computeId === compute?.id);
  const hasStorageLedger = ledger.some((entry) => entry.accountId === accountId && entry.storageId === storage?.id);
  const hasAttachmentLedger = ledger.some((entry) => entry.accountId === accountId && entry.attachmentId === attachment?.id);
  const hasRequestLedger = ledger.some((entry) =>
    entry.accountId === accountId &&
    entry.workspaceId === workspace?.id &&
    entry.type === "request_debit"
  );
  const hasComputeUsage = resourceUsage.some((entry) => entry.accountId === accountId && entry.computeId === compute?.id);
  const hasStorageUsage = resourceUsage.some((entry) => entry.accountId === accountId && entry.storageId === storage?.id);
  const hasAttachmentUsage = resourceUsage.some((entry) => entry.accountId === accountId && entry.attachmentId === attachment?.id);
  const hasRequestUsage = requestUsageLogs.some((entry) =>
    entry.accountId === accountId &&
    entry.workspaceId === workspace?.id &&
    (entry.id === requestUsage?.id || entry.requestId === requestUsage?.requestId)
  );

  addCheck(checks, "ledger_and_usage_verified", Boolean(
    state?.wallet?.accountId === accountId &&
    hasComputeLedger &&
    hasStorageLedger &&
    hasAttachmentLedger &&
    hasRequestLedger &&
    hasComputeUsage &&
    hasStorageUsage &&
    hasAttachmentUsage &&
    hasRequestUsage
  ));
}

async function cleanupVerificationResources({ fetchImpl, origin, accountId, computeId, storageId, attachmentId, checks = null, auth = null }) {
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

  if (computeId) {
    try {
      const destroyed = await requestJson({
        fetchImpl,
        origin,
        path: "/api/compute-resources/destroy",
        method: "POST",
        auth,
        body: { accountId, computeId, confirm: true }
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
  fetchImpl = globalThis.fetch
} = {}) {
  if (typeof fetchImpl !== "function") throw new Error("fetch_required");
  const checks = [];
  const normalizedOrigin = normalizeOrigin(origin);
  assertPublicHttpsUrl(normalizedOrigin, "public_origin_required");
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
  let auth = null;

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
      path: "/api/compute-resources",
      method: "POST",
      auth,
      body: { accountId, packageId, name: computeName }
    });
    assertComputeShape(checks, compute);

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
        computeId: compute.id,
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

    const state = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/state",
      auth
    });
    assertLedgerAndUsage(checks, state, { accountId, compute, storage, attachment, workspace, requestUsage });

    const cleanupErrors = await cleanupVerificationResources({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      computeId: compute.id,
      storageId: storage.id,
      attachmentId: attachment.id,
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
    if (!compute?.id && !storage?.id && !attachment?.id) throw error;
    const cleanupErrors = await cleanupVerificationResources({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      computeId: compute?.id,
      storageId: storage?.id,
      attachmentId: attachment?.id,
      auth
    });
    if (cleanupErrors.length > 0) error.cleanupErrors = cleanupErrors;
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
    fetchImpl
  };
}

function errorPayload(error) {
  return {
    ok: false,
    error: error.message,
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
