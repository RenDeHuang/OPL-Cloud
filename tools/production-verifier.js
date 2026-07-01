const DEFAULT_ACCOUNT_ID = "pi-production-verifier";
const DEFAULT_WORKSPACE_NAME = "Production Verification Lab";
const DEFAULT_PACKAGE_ID = "basic";
const DEFAULT_CREDIT_AMOUNT = 1000;
const DEFAULT_WORKSPACE_URL_ATTEMPTS = 12;
const DEFAULT_RETRY_DELAY_MS = 5000;

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

async function requestJson({ fetchImpl, origin, path, method = "GET", body = null }) {
  const response = await fetchImpl(endpoint(origin, path), {
    method,
    headers: body ? { "content-type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined
  });
  const payload = await readResponse(response);
  if (!response.ok) {
    const message = typeof payload === "string" ? payload : payload.error || JSON.stringify(payload);
    throw new Error(`request_failed:${method}:${path}:${response.status}:${message}`);
  }
  return payload;
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

function assertWorkspaceShape(checks, workspace) {
  addCheck(checks, "workspace_created", Boolean(
    workspace?.id &&
    workspace?.provider === "tencent-cvm" &&
    workspace?.server?.status === "running" &&
    workspace?.docker?.status === "running" &&
    workspace?.disk?.status === "attached_retained" &&
    workspace?.url &&
    workspace?.access?.tokenStatus === "active"
  ), { workspaceId: workspace?.id });
}

async function cleanupVerificationWorkspace({ fetchImpl, origin, accountId, workspaceId, workspaceDiskId, checks = null }) {
  const cleanupErrors = [];

  try {
    const cleanupServerDestroyed = await requestJson({
      fetchImpl,
      origin,
      path: "/api/workspaces/destroy-server",
      method: "POST",
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
  workspaceName = DEFAULT_WORKSPACE_NAME,
  packageId = DEFAULT_PACKAGE_ID,
  creditAmount = DEFAULT_CREDIT_AMOUNT,
  workspaceUrlAttempts = DEFAULT_WORKSPACE_URL_ATTEMPTS,
  retryDelayMs = DEFAULT_RETRY_DELAY_MS,
  fetchImpl = globalThis.fetch
} = {}) {
  if (typeof fetchImpl !== "function") throw new Error("fetch_required");
  const checks = [];
  const normalizedOrigin = normalizeOrigin(origin);
  let workspace = null;

  try {
    const productionReadiness = await requestJson({ fetchImpl, origin: normalizedOrigin, path: "/api/production/readiness" });
    assertReady({ checks, name: "production_readiness", payload: productionReadiness });

    const runtimeReadiness = await requestJson({ fetchImpl, origin: normalizedOrigin, path: "/api/runtime/readiness" });
    assertReady({ checks, name: "runtime_readiness", payload: runtimeReadiness });

    await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/accounts/credit",
      method: "POST",
      body: { accountId, amount: creditAmount, reason: "production_verification_credit" }
    });

    workspace = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/workspaces",
      method: "POST",
      body: { accountId, workspaceName, packageId }
    });
    assertWorkspaceShape(checks, workspace);

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
      body: { accountId, workspaceId: workspace.id, hours: 1, sourceEventId: "production_verification_tick" }
    });
    addCheck(checks, "billing_settlement", Boolean(
      Array.isArray(settlement.entries) &&
      settlement.entries.length > 0 &&
      Array.isArray(settlement.metering)
    ));

    const cleanupErrors = await cleanupVerificationWorkspace({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      workspaceId: workspace.id,
      workspaceDiskId: workspace.disk.id,
      checks
    });
    if (cleanupErrors.length > 0) {
      const error = new Error(`production_verification_cleanup_failed:${cleanupErrors.join("|")}`);
      error.cleanupErrors = cleanupErrors;
      throw error;
    }

    return {
      ok: true,
      accountId,
      workspaceId: workspace.id,
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
      workspaceDiskId: workspace.disk?.id
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

async function main() {
  const args = cliArgs(process.argv.slice(2));
  const result = await verifyProductionChain({
    origin: args.origin || process.env.OPL_CONSOLE_ORIGIN,
    accountId: args.account || process.env.OPL_VERIFY_ACCOUNT_ID || DEFAULT_ACCOUNT_ID,
    workspaceName: args.workspace || process.env.OPL_VERIFY_WORKSPACE_NAME || DEFAULT_WORKSPACE_NAME,
    packageId: args.package || process.env.OPL_VERIFY_PACKAGE_ID || DEFAULT_PACKAGE_ID,
    creditAmount: Number(args.credit || process.env.OPL_VERIFY_CREDIT_AMOUNT || DEFAULT_CREDIT_AMOUNT),
    workspaceUrlAttempts: Number(args["url-attempts"] || process.env.OPL_VERIFY_URL_ATTEMPTS || DEFAULT_WORKSPACE_URL_ATTEMPTS),
    retryDelayMs: Number(args["retry-delay-ms"] || process.env.OPL_VERIFY_RETRY_DELAY_MS || DEFAULT_RETRY_DELAY_MS)
  });
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
}

if (import.meta.url === `file://${process.argv[1]}`) {
  main().catch((error) => {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  });
}
