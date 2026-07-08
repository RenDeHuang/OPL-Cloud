const DEFAULT_ORIGIN = "https://cloud.medopl.cn";
const DEFAULT_ACCOUNT_ID = "pi-manual-workspace";
const DEFAULT_PACKAGE_ID = "basic";
const DEFAULT_CREDIT_AMOUNT = 1000;
const DEFAULT_URL_ATTEMPTS = 36;
const DEFAULT_RETRY_DELAY_MS = 10000;
const DEFAULT_MOUNT_PATH = "/data";

function defaultRunId() {
  return new Date().toISOString().replace(/[-:]/g, "").replace(/\..+$/, "Z");
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

function cookieHeaderFromSetCookie(setCookie = "") {
  return String(setCookie)
    .split(/,(?=[^;,]+=)/)
    .map((cookie) => cookie.split(";")[0]?.trim())
    .filter(Boolean)
    .join("; ");
}

function authHeaders(auth = null) {
  return {
    ...(auth?.cookie ? { cookie: auth.cookie } : {}),
    ...(auth?.csrf ? { "x-opl-csrf": auth.csrf } : {})
  };
}

async function requestJson({ origin, path, method = "GET", body = null, auth = null, headers: extraHeaders = {} }) {
	const response = await fetch(endpoint(origin, path), {
		method,
		headers: {
			...(body ? { "content-type": "application/json" } : {}),
			...authHeaders(auth),
			...extraHeaders
		},
		body: body ? JSON.stringify(body) : undefined
  });
  const payload = await readResponse(response);
  if (!response.ok) {
    const message = typeof payload === "string" ? payload : payload.error || JSON.stringify(payload);
    const error = new Error(`request_failed:${method}:${path}:${response.status}:${message}`);
    if (payload && typeof payload === "object") {
      error.safeMessage = payload.safeMessage || "";
      error.providerRequestId = payload.providerRequestId || "";
      error.retryable = payload.retryable;
    }
    throw error;
  }
  return { payload, response };
}

async function operatorSession({ origin, operatorToken }) {
  if (!operatorToken) throw new Error("operator_token_required");
  const { payload, response } = await requestJson({
		origin,
		path: "/api/auth/operator-login",
		method: "POST",
		body: {},
		headers: { "x-opl-operator-token": operatorToken }
	});
  return {
    cookie: cookieHeaderFromSetCookie(response.headers?.get?.("set-cookie") || ""),
    csrf: response.headers?.get?.("x-opl-csrf-token") || payload?.csrfToken || ""
  };
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function waitForRuntime({ origin, auth, accountId, workspaceId, attempts, retryDelayMs }) {
  let lastStatus = null;
  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    const { payload } = await requestJson({
      origin,
      path: "/api/workspaces/runtime-status",
      method: "POST",
      auth,
      body: { accountId, workspaceId }
    });
    lastStatus = { ...payload, attempts: attempt };
    if (
      payload?.ready === true &&
      Array.isArray(payload.checks) &&
      payload.checks.length > 0 &&
      payload.checks.every((check) => check.ok === true)
    ) {
      return lastStatus;
    }
    if (attempt < attempts) await sleep(retryDelayMs);
  }
  throw new Error(`workspace_runtime_not_ready:${JSON.stringify(lastStatus)}`);
}

async function waitForWorkspaceUrl({ url, attempts, retryDelayMs }) {
  let lastStatus = 0;
  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    const response = await fetch(url);
    lastStatus = response.status;
    if (response.ok) return { attempts: attempt, status: response.status };
    if (attempt < attempts) await sleep(retryDelayMs);
  }
  throw new Error(`workspace_url_not_ready:${lastStatus}`);
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

function optionsFromEnv({ argv = process.argv.slice(2), env = process.env } = {}) {
  const args = cliArgs(argv);
  return {
    origin: args.origin || env.OPL_CONSOLE_ORIGIN || DEFAULT_ORIGIN,
    accountId: args.account || env.OPL_MANUAL_ACCOUNT_ID || DEFAULT_ACCOUNT_ID,
    packageId: args.package || env.OPL_MANUAL_PACKAGE_ID || DEFAULT_PACKAGE_ID,
    runId: args["run-id"] || env.OPL_MANUAL_RUN_ID || defaultRunId(),
    creditAmount: Number(args.credit || env.OPL_MANUAL_CREDIT_AMOUNT || DEFAULT_CREDIT_AMOUNT),
    operatorToken: args["operator-token"] || env.OPL_VERIFY_OPERATOR_TOKEN || "",
    attempts: Number(args.attempts || env.OPL_MANUAL_URL_ATTEMPTS || DEFAULT_URL_ATTEMPTS),
    retryDelayMs: Number(args["retry-delay-ms"] || env.OPL_MANUAL_RETRY_DELAY_MS || DEFAULT_RETRY_DELAY_MS)
  };
}

export async function provisionManualWorkspace(options = {}) {
  const {
    origin: rawOrigin,
    accountId,
    packageId,
    runId,
    creditAmount,
    operatorToken,
    attempts,
    retryDelayMs
  } = { ...optionsFromEnv(), ...options };
  const origin = normalizeOrigin(rawOrigin);
  const auth = await operatorSession({ origin, operatorToken });
  const workspaceName = `人工测试工作区 ${runId}`;
  const resourceName = `manual-${runId}`;

  if (creditAmount > 0) {
    await requestJson({
      origin,
      path: "/api/billing/topups",
      method: "POST",
      auth,
      body: {
        accountId,
        amount: creditAmount,
        reason: `manual_workspace_credit:${runId}`,
        confirm: true
      }
    });
  }

  const { payload: compute } = await requestJson({
    origin,
    path: "/api/compute-allocations",
    method: "POST",
    auth,
    body: { accountId, packageId, name: `${resourceName}-compute` }
  });

  const { payload: storage } = await requestJson({
    origin,
    path: "/api/storage-volumes",
    method: "POST",
    auth,
    body: { accountId, packageId, name: `${resourceName}-storage` }
  });

  const { payload: attachment } = await requestJson({
    origin,
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

  const { payload: workspace } = await requestJson({
    origin,
    path: "/api/workspaces",
    method: "POST",
    auth,
    body: {
      accountId,
      workspaceName,
      attachmentId: attachment.id
    }
  });

  const runtimeStatus = await waitForRuntime({
    origin,
    auth,
    accountId,
    workspaceId: workspace.id,
    attempts,
    retryDelayMs
  });
  const workspaceUrlStatus = await waitForWorkspaceUrl({
    url: workspace.url,
    attempts,
    retryDelayMs
  });

  return {
    ok: true,
    origin,
    accountId,
    packageId,
    runId,
    workspaceUrl: workspace.url,
    workspaceId: workspace.id,
    computeAllocationId: compute.id,
    computeProviderResourceId: compute.providerResourceId || "",
    computeInstanceId: compute.instanceId || "",
    computeNodeName: compute.nodeName || "",
    storageId: storage.id,
    storageProviderResourceId: storage.providerResourceId || "",
    attachmentId: attachment.id,
    mountPath: attachment.mountPath,
    runtimeStatus,
    workspaceUrlStatus,
    billing: {
      computeHourlyPrice: compute.hourlyPrice,
      storageHoldAmount: storage.holdAmount,
      computeHoldAmount: compute.holdAmount
    }
  };
}

if (import.meta.url === `file://${process.argv[1]}`) {
  provisionManualWorkspace()
    .then((result) => {
      process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    })
    .catch((error) => {
      process.stderr.write(`${JSON.stringify({
        ok: false,
        error: error.message,
        ...(error.safeMessage ? { safeMessage: error.safeMessage } : {}),
        ...(error.providerRequestId ? { providerRequestId: error.providerRequestId } : {}),
        ...(typeof error.retryable === "boolean" ? { retryable: error.retryable } : {})
      }, null, 2)}\n`);
      process.exitCode = 1;
    });
}
