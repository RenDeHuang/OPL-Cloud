import { createHash } from "node:crypto";
import { spawn } from "node:child_process";

const DEFAULT_ACCOUNT_ID = "pi-production-persistence-e2e";
const DEFAULT_PACKAGE_ID = "basic";
const DEFAULT_CREDIT_AMOUNT = 2000;
const DEFAULT_WORKSPACE_URL_ATTEMPTS = 60;
const DEFAULT_RETRY_DELAY_MS = 10000;
const DEFAULT_NODE_TIMEOUT_MS = 900000;
const DEFAULT_POD_TIMEOUT_MS = 900000;
const DEFAULT_KUBE_POLL_MS = 10000;
const DEFAULT_MOUNT_PATH = "/data";
const DEFAULT_PROJECTS_ROOT = "/projects";
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

function compactId(value) {
  return String(value || "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 48);
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

function setCookieHeaderValues(headers) {
  if (typeof headers?.getSetCookie === "function") return headers.getSetCookie();
  const value = headers?.get?.("set-cookie") || "";
  return value ? [value] : [];
}

function cookieHeaderFromSetCookie(headers) {
  return setCookieHeaderValues(headers)
    .flatMap((value) => String(value).split(/,(?=[^;,]+=)/))
    .map((cookie) => cookie.split(";")[0]?.trim())
    .filter(Boolean)
    .join("; ");
}

async function requestOperatorSession({ fetchImpl, origin, operatorToken }) {
  if (!operatorToken) throw new Error("operator_token_required");
  const { payload, response } = await requestJsonWithResponse({
    fetchImpl,
    origin,
    path: "/api/auth/operator-login",
    method: "POST",
    body: { operatorToken }
  });
  return {
    cookie: cookieHeaderFromSetCookie(response.headers),
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

async function requestRuntimeStatus({ fetchImpl, origin, accountId, workspaceId, attempts, retryDelayMs, auth }) {
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

function assertStateReferences(checks, state, { accountId, computeA, computeB, storage, attachmentA, attachmentB, workspaceB, requestUsage }) {
  const ledger = state?.billingLedger || [];
  const resourceUsage = state?.resourceUsageLogs || [];
  const requestUsageLogs = state?.requestUsageLogs || [];
  const hasLedger = (...predicates) => predicates.every((predicate) => ledger.some(predicate));
  const hasResourceUsage = (...predicates) => predicates.every((predicate) => resourceUsage.some(predicate));
  const hasRequestUsage = requestUsageLogs.some((entry) =>
    entry.accountId === accountId &&
    entry.workspaceId === workspaceB?.id &&
    (entry.id === requestUsage?.id || entry.requestId === requestUsage?.requestId)
  );

  addCheck(checks, "ledger_and_usage_verified", Boolean(
    state?.wallet?.accountId === accountId &&
    hasLedger(
      (entry) => entry.accountId === accountId && entry.computeId === computeA?.id,
      (entry) => entry.accountId === accountId && entry.computeId === computeB?.id,
      (entry) => entry.accountId === accountId && entry.storageId === storage?.id,
      (entry) => entry.accountId === accountId && entry.attachmentId === attachmentA?.id,
      (entry) => entry.accountId === accountId && entry.attachmentId === attachmentB?.id,
      (entry) => entry.accountId === accountId && entry.workspaceId === workspaceB?.id && entry.type === "request_debit"
    ) &&
    hasResourceUsage(
      (entry) => entry.accountId === accountId && entry.computeId === computeA?.id,
      (entry) => entry.accountId === accountId && entry.computeId === computeB?.id,
      (entry) => entry.accountId === accountId && entry.storageId === storage?.id,
      (entry) => entry.accountId === accountId && entry.attachmentId === attachmentA?.id,
      (entry) => entry.accountId === accountId && entry.attachmentId === attachmentB?.id
    ) &&
    hasRequestUsage
  ));
}

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

function nodeScriptWriteFile() {
  return `
const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");
const [filePath, content] = process.argv.slice(1);
fs.mkdirSync(path.dirname(filePath), { recursive: true });
fs.writeFileSync(filePath, content);
process.stdout.write(JSON.stringify({
  filePath,
  sha256: crypto.createHash("sha256").update(content).digest("hex")
}));
`.trim();
}

function nodeScriptReadFile() {
  return `
const fs = require("node:fs");
const crypto = require("node:crypto");
const [filePath] = process.argv.slice(1);
const content = fs.readFileSync(filePath, "utf8");
process.stdout.write(JSON.stringify({
  filePath,
  content,
  sha256: crypto.createHash("sha256").update(content).digest("hex")
}));
`.trim();
}

function parseJsonOutput(raw, label) {
  try {
    return JSON.parse(raw || "{}");
  } catch (error) {
    throw new Error(`${label}_invalid_json:${error.message}:${raw}`);
  }
}

function defaultKube({
  env = process.env,
  runner = runCommand,
  namespace = env.OPL_K8S_NAMESPACE || "opl-cloud",
  kubeconfig = env.KUBECONFIG || env.TENCENT_DEPLOY_KUBECONFIG_REF || ""
} = {}) {
  if (!kubeconfig) throw new Error("kubeconfig_required");

  async function kubectl(args, { timeoutMs = 120000 } = {}) {
    const fullArgs = ["--kubeconfig", kubeconfig, "--namespace", namespace, ...args];
    return runner({ command: "kubectl", args: fullArgs, timeoutMs });
  }

  async function pollJson({ label, timeoutMs, pollMs, load, ready }) {
    const deadline = Date.now() + timeoutMs;
    let payload = null;
    let lastError = null;
    while (Date.now() <= deadline) {
      try {
        payload = await load();
        const result = ready(payload);
        if (result) return result;
      } catch (error) {
        lastError = error;
      }
      await sleep(pollMs);
    }
    if (lastError) throw new Error(`${label}_timeout:${lastError.message}`);
    throw new Error(`${label}_timeout`);
  }

  return {
    async waitForComputeNodes(compute, { timeoutMs = DEFAULT_NODE_TIMEOUT_MS, pollMs = DEFAULT_KUBE_POLL_MS } = {}) {
      const selector = `oplcloud.cn/compute-id=${compactId(compute.id)}`;
      const desiredNodes = Math.max(1, Number(compute.desiredNodes || 1));
      return pollJson({
        label: "compute_nodes_ready",
        timeoutMs,
        pollMs,
        load: async () => parseJsonOutput(await kubectl(["get", "nodes", "-l", selector, "-o", "json"], { timeoutMs: 60000 }), "nodes"),
        ready: (payload) => {
          const readyNodes = (payload.items || []).filter((node) =>
            (node.status?.conditions || []).some((condition) => condition.type === "Ready" && condition.status === "True")
          );
          return readyNodes.length >= desiredNodes
            ? { readyNodes: readyNodes.map((node) => node.metadata?.name).filter(Boolean), selector }
            : null;
        }
      });
    },

    async waitForRuntimePod(workspace, { timeoutMs = DEFAULT_POD_TIMEOUT_MS, pollMs = DEFAULT_KUBE_POLL_MS } = {}) {
      const selector = [
        "app.kubernetes.io/name=opl-workspace-runtime",
        `oplcloud.cn/compute-id=${workspace.computeId}`
      ].join(",");
      return pollJson({
        label: "runtime_pod_ready",
        timeoutMs,
        pollMs,
        load: async () => parseJsonOutput(await kubectl(["get", "pods", "-l", selector, "-o", "json"], { timeoutMs: 60000 }), "pods"),
        ready: (payload) => {
          const pod = (payload.items || []).find((item) =>
            item.status?.phase === "Running" &&
            (item.status?.conditions || []).some((condition) => condition.type === "Ready" && condition.status === "True")
          );
          return pod ? { podName: pod.metadata?.name, selector } : null;
        }
      });
    },

    async writeFile({ podName, filePath, content }) {
      const raw = await kubectl([
        "exec",
        podName,
        "--",
        "node",
        "-e",
        nodeScriptWriteFile(),
        filePath,
        content
      ]);
      return parseJsonOutput(raw, "write_file");
    },

    async readFile({ podName, filePath }) {
      const raw = await kubectl([
        "exec",
        podName,
        "--",
        "node",
        "-e",
        nodeScriptReadFile(),
        filePath
      ]);
      return parseJsonOutput(raw, "read_file");
    }
  };
}

async function runCommand({ command, args, timeoutMs }) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { stdio: "pipe" });
    const timer = timeoutMs > 0
      ? setTimeout(() => {
        child.kill("SIGTERM");
        reject(new Error(`${command}_timeout`));
      }, timeoutMs)
      : null;
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => {
      stdout += chunk.toString();
    });
    child.stderr.on("data", (chunk) => {
      stderr += chunk.toString();
    });
    child.on("error", (error) => {
      if (timer) clearTimeout(timer);
      reject(error);
    });
    child.on("close", (code) => {
      if (timer) clearTimeout(timer);
      if (code === 0) resolve(stdout.trim());
      else reject(new Error(`${command} ${args.join(" ")} failed:${stderr.trim()}`));
    });
  });
}

async function cleanupResources({ fetchImpl, origin, accountId, auth, resources, checks = null }) {
  const cleanupErrors = [];

  async function attempt(label, fn) {
    try {
      const result = await fn();
      if (checks) addCheck(checks, label, true);
      return result;
    } catch (error) {
      cleanupErrors.push(`${label}:${error.message}`);
      return null;
    }
  }

  if (resources.attachmentB?.id && !resources.detachedAttachmentIds.has(resources.attachmentB.id)) {
    await attempt("cleanup_detach_second_attachment", async () => requestJson({
      fetchImpl,
      origin,
      path: "/api/storage-attachments/detach",
      method: "POST",
      auth,
      body: { accountId, attachmentId: resources.attachmentB.id, confirm: true }
    }));
  }
  if (resources.computeB?.id && !resources.destroyedComputeIds.has(resources.computeB.id)) {
    await attempt("cleanup_destroy_second_compute", async () => requestJson({
      fetchImpl,
      origin,
      path: "/api/compute-resources/destroy",
      method: "POST",
      auth,
      body: { accountId, computeId: resources.computeB.id, confirm: true }
    }));
  }
  if (resources.attachmentA?.id && !resources.detachedAttachmentIds.has(resources.attachmentA.id)) {
    await attempt("cleanup_detach_first_attachment", async () => requestJson({
      fetchImpl,
      origin,
      path: "/api/storage-attachments/detach",
      method: "POST",
      auth,
      body: { accountId, attachmentId: resources.attachmentA.id, confirm: true }
    }));
  }
  if (resources.computeA?.id && !resources.destroyedComputeIds.has(resources.computeA.id)) {
    await attempt("cleanup_destroy_first_compute", async () => requestJson({
      fetchImpl,
      origin,
      path: "/api/compute-resources/destroy",
      method: "POST",
      auth,
      body: { accountId, computeId: resources.computeA.id, confirm: true }
    }));
  }
  if (resources.storage?.id && !resources.destroyedStorage) {
    await attempt("cleanup_destroy_storage", async () => requestJson({
      fetchImpl,
      origin,
      path: "/api/storage-volumes/destroy",
      method: "POST",
      auth,
      body: { accountId, storageId: resources.storage.id, confirmDataLoss: true }
    }));
  }

  return cleanupErrors;
}

export async function verifyProductionPersistenceChain({
  origin,
  accountId = DEFAULT_ACCOUNT_ID,
  runId = defaultRunId(),
  packageId = DEFAULT_PACKAGE_ID,
  creditAmount = DEFAULT_CREDIT_AMOUNT,
  workspaceUrlAttempts = DEFAULT_WORKSPACE_URL_ATTEMPTS,
  retryDelayMs = DEFAULT_RETRY_DELAY_MS,
  operatorToken = "",
  fetchImpl = globalThis.fetch,
  kube = null,
  nodeTimeoutMs = DEFAULT_NODE_TIMEOUT_MS,
  podTimeoutMs = DEFAULT_POD_TIMEOUT_MS,
  kubePollMs = DEFAULT_KUBE_POLL_MS
} = {}) {
  if (typeof fetchImpl !== "function") throw new Error("fetch_required");
  const normalizedOrigin = normalizeOrigin(origin);
  assertPublicHttpsUrl(normalizedOrigin, "public_origin_required");
  const kubeClient = kube || defaultKube();
  const checks = [];
  const resources = {
    computeA: null,
    computeB: null,
    storage: null,
    attachmentA: null,
    attachmentB: null,
    detachedAttachmentIds: new Set(),
    destroyedComputeIds: new Set(),
    destroyedStorage: false
  };
  let auth = null;
  const workspaceNameA = `OPL Persistence E2E A ${runId}`;
  const workspaceNameB = `OPL Persistence E2E B ${runId}`;
  const filePath = `${DEFAULT_PROJECTS_ROOT}/opl-e2e/${runId}.txt`;
  const fileContent = `OPL Cloud persistence E2E ${runId}\n`;
  const expectedSha = sha256(fileContent);
  const requestId = `production-persistence-e2e:${runId}`;

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
      body: { accountId, amount: creditAmount, reason: `production_persistence_e2e_credit:${runId}` }
    });
    addCheck(checks, "wallet_topped_up", true);

    resources.computeA = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/compute-resources",
      method: "POST",
      auth,
      body: { accountId, packageId, name: `${workspaceNameA} compute` }
    });
    addCheck(checks, "first_compute_created", Boolean(resources.computeA?.id && resources.computeA?.providerResourceId?.startsWith("nodepool/")), {
      computeId: resources.computeA?.id,
      nodePoolId: resources.computeA?.nodePoolId
    });
    await kubeClient.waitForComputeNodes(resources.computeA, { timeoutMs: nodeTimeoutMs, pollMs: kubePollMs });
    addCheck(checks, "first_compute_nodes_ready", true);

    resources.storage = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/storage-volumes",
      method: "POST",
      auth,
      body: { accountId, packageId, name: `${workspaceNameA} retained storage` }
    });
    addCheck(checks, "storage_created", Boolean(resources.storage?.id && resources.storage?.providerResourceId?.startsWith("pvc/")), {
      storageId: resources.storage?.id
    });

    resources.attachmentA = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/storage-attachments",
      method: "POST",
      auth,
      body: {
        accountId,
        computeId: resources.computeA.id,
        storageId: resources.storage.id,
        mountPath: DEFAULT_MOUNT_PATH
      }
    });
    addCheck(checks, "first_storage_attached", Boolean(resources.attachmentA?.id && resources.attachmentA?.status === "attached"), {
      attachmentId: resources.attachmentA?.id
    });

    const workspaceA = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/workspaces",
      method: "POST",
      auth,
      body: { accountId, workspaceName: workspaceNameA, attachmentId: resources.attachmentA.id }
    });
    addCheck(checks, "first_workspace_created", Boolean(workspaceA?.id && workspaceA?.url), { workspaceId: workspaceA?.id });

    const runtimeStatusA = await requestRuntimeStatus({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      workspaceId: workspaceA.id,
      attempts: workspaceUrlAttempts,
      retryDelayMs,
      auth
    });
    assertRuntimeStatus(checks, runtimeStatusA);
    assertPublicHttpsUrl(workspaceA.url, "public_workspace_url_required");
    const workspaceUrlA = await requestWorkspaceUrl({ fetchImpl, url: workspaceA.url, attempts: workspaceUrlAttempts, retryDelayMs });
    addCheck(checks, "first_workspace_url", true, { url: workspaceA.url, attempts: workspaceUrlA.attempts });

    const podA = await kubeClient.waitForRuntimePod(workspaceA, { timeoutMs: podTimeoutMs, pollMs: kubePollMs });
    const written = await kubeClient.writeFile({ podName: podA.podName, filePath, content: fileContent });
    addCheck(checks, "file_written_to_retained_storage", written?.sha256 === expectedSha, { filePath, sha256: written?.sha256 });

    await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/storage-attachments/detach",
      method: "POST",
      auth,
      body: { accountId, attachmentId: resources.attachmentA.id, confirm: true }
    });
    resources.detachedAttachmentIds.add(resources.attachmentA.id);
    addCheck(checks, "first_storage_detached", true);

    await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/compute-resources/destroy",
      method: "POST",
      auth,
      body: { accountId, computeId: resources.computeA.id, confirm: true }
    });
    resources.destroyedComputeIds.add(resources.computeA.id);
    addCheck(checks, "first_compute_destroyed", true);

    resources.computeB = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/compute-resources",
      method: "POST",
      auth,
      body: { accountId, packageId, name: `${workspaceNameB} compute` }
    });
    addCheck(checks, "second_compute_created", Boolean(resources.computeB?.id && resources.computeB?.id !== resources.computeA?.id), {
      computeId: resources.computeB?.id,
      nodePoolId: resources.computeB?.nodePoolId
    });
    await kubeClient.waitForComputeNodes(resources.computeB, { timeoutMs: nodeTimeoutMs, pollMs: kubePollMs });
    addCheck(checks, "second_compute_nodes_ready", true);

    resources.attachmentB = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/storage-attachments",
      method: "POST",
      auth,
      body: {
        accountId,
        computeId: resources.computeB.id,
        storageId: resources.storage.id,
        mountPath: DEFAULT_MOUNT_PATH
      }
    });
    addCheck(checks, "second_storage_attached", Boolean(resources.attachmentB?.id && resources.attachmentB?.status === "attached"), {
      attachmentId: resources.attachmentB?.id
    });

    const workspaceB = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/workspaces",
      method: "POST",
      auth,
      body: { accountId, workspaceName: workspaceNameB, attachmentId: resources.attachmentB.id }
    });
    addCheck(checks, "second_workspace_created", Boolean(workspaceB?.id && workspaceB?.url), { workspaceId: workspaceB?.id });

    const runtimeStatusB = await requestRuntimeStatus({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      workspaceId: workspaceB.id,
      attempts: workspaceUrlAttempts,
      retryDelayMs,
      auth
    });
    assertRuntimeStatus(checks, runtimeStatusB);
    assertPublicHttpsUrl(workspaceB.url, "public_workspace_url_required");
    const workspaceUrlB = await requestWorkspaceUrl({ fetchImpl, url: workspaceB.url, attempts: workspaceUrlAttempts, retryDelayMs });
    addCheck(checks, "second_workspace_url", true, { url: workspaceB.url, attempts: workspaceUrlB.attempts });

    const podB = await kubeClient.waitForRuntimePod(workspaceB, { timeoutMs: podTimeoutMs, pollMs: kubePollMs });
    const read = await kubeClient.readFile({ podName: podB.podName, filePath });
    addCheck(checks, "file_persisted_after_compute_recreation", read?.content === fileContent && read?.sha256 === expectedSha, {
      filePath,
      sha256: read?.sha256
    });

    const requestUsage = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: "/api/billing/request-usage",
      method: "POST",
      auth,
      body: {
        accountId,
        workspaceId: workspaceB.id,
        requestId,
        provider: "sub2api",
        model: "production-persistence-e2e",
        inputTokens: 1,
        outputTokens: 1,
        amount: DEFAULT_REQUEST_USAGE_AMOUNT,
        sourceEventId: `production_persistence_e2e_request_usage:${runId}`
      }
    });
    addCheck(checks, "request_usage_recorded", Boolean(requestUsage?.id && requestUsage?.workspaceId === workspaceB.id));

    const state = await requestJson({
      fetchImpl,
      origin: normalizedOrigin,
      path: `/api/state?accountId=${encodeURIComponent(accountId)}`,
      auth
    });
    assertStateReferences(checks, state, {
      accountId,
      computeA: resources.computeA,
      computeB: resources.computeB,
      storage: resources.storage,
      attachmentA: resources.attachmentA,
      attachmentB: resources.attachmentB,
      workspaceB,
      requestUsage
    });

    const cleanupErrors = await cleanupResources({
      fetchImpl,
      origin: normalizedOrigin,
      accountId,
      auth,
      resources,
      checks
    });
    if (cleanupErrors.length > 0) {
      const error = new Error(`production_persistence_e2e_cleanup_failed:${cleanupErrors.join("|")}`);
      error.cleanupErrors = cleanupErrors;
      throw error;
    }
    resources.destroyedStorage = true;

    return {
      ok: true,
      accountId,
      runId,
      firstComputeId: resources.computeA.id,
      secondComputeId: resources.computeB.id,
      storageId: resources.storage.id,
      firstWorkspaceId: workspaceA.id,
      secondWorkspaceId: workspaceB.id,
      url: workspaceB.url,
      file: {
        path: filePath,
        sha256: expectedSha
      },
      checks
    };
  } catch (error) {
    if (auth && (resources.computeA?.id || resources.computeB?.id || resources.storage?.id || resources.attachmentA?.id || resources.attachmentB?.id)) {
      const cleanupErrors = await cleanupResources({
        fetchImpl,
        origin: normalizedOrigin,
        accountId,
        auth,
        resources
      });
      if (cleanupErrors.length > 0) error.cleanupErrors = cleanupErrors;
    }
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

function optionsFromArgs({ argv, env = process.env, fetchImpl = globalThis.fetch }) {
  const args = cliArgs(argv);
  return {
    origin: args.origin || env.OPL_CONSOLE_ORIGIN,
    accountId: args.account || env.OPL_E2E_ACCOUNT_ID || DEFAULT_ACCOUNT_ID,
    runId: args["run-id"] || env.OPL_E2E_RUN_ID,
    packageId: args.package || env.OPL_E2E_PACKAGE_ID || DEFAULT_PACKAGE_ID,
    creditAmount: Number(args.credit || env.OPL_E2E_CREDIT_AMOUNT || DEFAULT_CREDIT_AMOUNT),
    workspaceUrlAttempts: Number(args["url-attempts"] || env.OPL_E2E_URL_ATTEMPTS || DEFAULT_WORKSPACE_URL_ATTEMPTS),
    retryDelayMs: Number(args["retry-delay-ms"] || env.OPL_E2E_RETRY_DELAY_MS || DEFAULT_RETRY_DELAY_MS),
    nodeTimeoutMs: Number(args["node-timeout-ms"] || env.OPL_E2E_NODE_TIMEOUT_MS || DEFAULT_NODE_TIMEOUT_MS),
    podTimeoutMs: Number(args["pod-timeout-ms"] || env.OPL_E2E_POD_TIMEOUT_MS || DEFAULT_POD_TIMEOUT_MS),
    kubePollMs: Number(args["kube-poll-ms"] || env.OPL_E2E_KUBE_POLL_MS || DEFAULT_KUBE_POLL_MS),
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

export async function runProductionPersistenceE2eCli({
  argv = process.argv.slice(2),
  env = process.env,
  stdout = process.stdout,
  stderr = process.stderr,
  fetchImpl = globalThis.fetch
} = {}) {
  try {
    const result = await verifyProductionPersistenceChain(optionsFromArgs({ argv, env, fetchImpl }));
    stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return 0;
  } catch (error) {
    stderr.write(`${JSON.stringify(errorPayload(error), null, 2)}\n`);
    return 1;
  }
}

if (import.meta.url === `file://${process.argv[1]}`) {
  runProductionPersistenceE2eCli().then((code) => {
    process.exitCode = code;
  });
}
