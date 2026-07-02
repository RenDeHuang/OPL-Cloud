const DEFAULT_ACCOUNT_ID = "pi-production-verifier";
const DEFAULT_WORKSPACE_NAME = "Production Verification Lab";
const DEFAULT_PACKAGE_ID = "basic";
const DEFAULT_CREDIT_AMOUNT = 1000;
const DEFAULT_WORKSPACE_URL_ATTEMPTS = 12;
const DEFAULT_RETRY_DELAY_MS = 5000;

function defaultRunId() {
  const stamp = new Date().toISOString().replace(/[-:]/g, "").replace(/\..+$/, "Z");
  const suffix = Math.random().toString(36).slice(2, 8);
  return `${stamp}-${suffix}`;
}

function normalizeOrigin(origin) {
  if (!origin) throw new Error("origin_required");
  return origin.replace(/\/$/, "");
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

function providerResourceShape(workspace) {
  if (workspace?.provider === "tencent-tke") {
    let url = null;
    try {
      url = workspace?.url ? new URL(workspace.url) : null;
    } catch {
      url = null;
    }
    return Boolean(
      workspace?.server?.id?.startsWith("deployment/") &&
      workspace?.docker?.id === workspace.server.id &&
      workspace?.docker?.service?.startsWith("service/") &&
      workspace?.docker?.image &&
      workspace?.disk?.id?.startsWith("pvc/") &&
      workspace?.disk?.mountPath === "/data" &&
      workspace?.disk?.storageClass &&
      url?.pathname === `/w/${workspace.id}` &&
      url?.searchParams.get("token") === workspace?.access?.token
    );
  }
  return false;
}

function assertWorkspaceShape(checks, workspace) {
  addCheck(checks, "workspace_created", Boolean(
    workspace?.id &&
    providerResourceShape(workspace) &&
    workspace?.server?.status === "running" &&
    workspace?.docker?.status === "running" &&
    workspace?.disk?.status === "attached_retained" &&
    workspace?.access?.tokenStatus === "active"
  ), { workspaceId: workspace?.id });
}

function assertBillingSettlement(checks, settlement) {
  const entryTypes = new Set((settlement.entries || []).map((entry) => entry.type));
  addCheck(checks, "billing_settlement", Boolean(
    (entryTypes.has("compute_debit") || entryTypes.has("server_debit")) &&
    entryTypes.has("storage_debit")
  ));
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

async function cleanupVerificationWorkspace({ fetchImpl, origin, accountId, workspaceId, workspaceDiskId, checks = null, auth = null }) {
  const cleanupErrors = [];

  try {
    const cleanupServerDestroyed = await requestJson({
      fetchImpl,
      origin,
      path: "/api/workspaces/destroy-server",
      method: "POST",
      auth,
      body: { accountId, workspaceId, confirm: true }
    });
    if (checks) {
      addCheck(checks, "verification_server_destroyed", Boolean(
        cleanupServerDestroyed.server?.status === "destroyed" &&
        cleanupServerDestroyed.server?.billingStatus === "stopped" &&
        cleanupServerDestroyed.disk?.id === workspaceDiskId &&
        cleanupServerDestroyed.disk?.billingStatus === "active"
      ));
    }
  } catch (error) {
    cleanupErrors.push(`destroy_server:${error.message}`);
  }

  try {
    const cleanupDiskDestroyed = await requestJson({
      fetchImpl,
      origin,
      path: "/api/workspaces/destroy-disk",
      method: "POST",
      auth,
      body: { accountId, workspaceId, confirmDataLoss: true }
    });
    if (checks) {
      addCheck(checks, "verification_disk_destroyed", Boolean(
        cleanupDiskDestroyed.state === "destroyed" &&
        cleanupDiskDestroyed.server?.status === "destroyed" &&
        cleanupDiskDestroyed.server?.billingStatus === "stopped" &&
        cleanupDiskDestroyed.disk?.status === "destroyed" &&
        cleanupDiskDestroyed.disk?.billingStatus === "stopped"
      ));
    }
  } catch (error) {
    cleanupErrors.push(`destroy_disk:${error.message}`);
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
  const effectiveWorkspaceName = workspaceName || `${DEFAULT_WORKSPACE_NAME} ${runId}`;
  const creditSourceEventId = `production_verification_credit:${runId}`;
  const settlementSourceEventId = `production_verification_tick:${runId}`;
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
      path: "/api/accounts/credit",
      method: "POST",
      auth,
      body: { accountId, amount: creditAmount, reason: creditSourceEventId }
    });

    workspace = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/workspaces",
      method: "POST",
      auth,
      body: { accountId, workspaceName: effectiveWorkspaceName, packageId }
    });
    assertWorkspaceShape(checks, workspace);

    if (workspace.provider === "tencent-tke") {
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
    }

    const workspaceUrlResult = await requestWorkspaceUrl({
      fetchImpl,
      url: workspace.url,
      attempts: workspaceUrlAttempts,
      retryDelayMs
    });
    addCheck(checks, "workspace_url", true, { url: workspace.url, attempts: workspaceUrlResult.attempts });

    const stopped = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/workspaces/stop-server",
      method: "POST",
      auth,
      body: { accountId, workspaceId: workspace.id, confirm: true }
    });
    addCheck(checks, "server_stopped_storage_retained", Boolean(
      stopped.server?.status === "stopped" &&
      stopped.server?.billingStatus === "stopped" &&
      stopped.disk?.status === "attached_retained" &&
      stopped.disk?.billingStatus === "active"
    ));

    const restarted = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/workspaces/restart-server",
      method: "POST",
      auth,
      body: { accountId, workspaceId: workspace.id }
    });
    addCheck(checks, "server_restarted", Boolean(
      restarted.state === "running" &&
      restarted.server?.status === "running" &&
      restarted.disk?.id === workspace.disk.id &&
      restarted.url === workspace.url &&
      restarted.access?.token === workspace.access.token
    ));

    const serverDestroyed = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/workspaces/destroy-server",
      method: "POST",
      auth,
      body: { accountId, workspaceId: workspace.id, confirm: true }
    });
    addCheck(checks, "server_destroyed_storage_retained", Boolean(
      serverDestroyed.state === "server_destroyed_disk_retained" &&
      serverDestroyed.server?.status === "destroyed" &&
      serverDestroyed.disk?.id === workspace.disk.id &&
      serverDestroyed.disk?.status === "detached_retained" &&
      serverDestroyed.disk?.billingStatus === "active"
    ));

    const recreated = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/workspaces/restart-server",
      method: "POST",
      auth,
      body: { accountId, workspaceId: workspace.id }
    });
    addCheck(checks, "server_recreated_from_retained_disk", Boolean(
      recreated.state === "running" &&
      recreated.server?.status === "running" &&
      recreated.disk?.id === workspace.disk.id &&
      recreated.disk?.status === "attached_retained" &&
      recreated.url === workspace.url &&
      recreated.access?.token === workspace.access.token
    ));

    const recreatedUrlResult = await requestWorkspaceUrl({
      fetchImpl,
      url: workspace.url,
      attempts: workspaceUrlAttempts,
      retryDelayMs
    });
    addCheck(checks, "workspace_url_after_recreate", true, {
      url: workspace.url,
      attempts: recreatedUrlResult.attempts
    });

    const settlement = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/billing/settle",
      method: "POST",
      auth,
      body: { accountId, workspaceId: workspace.id, hours: 1, sourceEventId: settlementSourceEventId }
    });
    assertBillingSettlement(checks, settlement);

    const cleanupErrors = await cleanupVerificationWorkspace({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      workspaceId: workspace.id,
      workspaceDiskId: workspace.disk.id,
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
    if (!workspace?.id) throw error;
    const cleanupErrors = await cleanupVerificationWorkspace({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      workspaceId: workspace.id,
      workspaceDiskId: workspace.disk?.id,
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
