import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
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
export const BASIC_CUSTOMER_CANARY_CONFIRMATION = "I_UNDERSTAND_THIS_PROVISIONS_ONE_REAL_BASIC_WORKSPACE_AND_SENDS_ONE_MODEL_REQUEST";
export const COMPUTE_CLAIM_RECOVERY_CONFIRMATION = "RECOVER_PROVEN_COMPUTE_AND_CONTINUE_ORIGINAL_LAUNCH";
export const WORKSPACE_LAUNCH_READBACK_RECOVERY_CONFIRMATION = "RECOVER_UNKNOWN_WORKSPACE_LAUNCH_STAGE_FROM_AUTHORITATIVE_READBACK";
export const RECOVERED_WORKSPACE_E2E_CONFIRMATION = "CONFIRM_SINGLE_MODEL_REQUEST_FOR_RECOVERED_WORKSPACE";

const DEFAULT_USAGE_ATTEMPTS = 24;
const DEFAULT_USAGE_RETRY_DELAY_MS = 5_000;
const DEFAULT_BROWSER_TIMEOUT_MS = 45_000;
const DEFAULT_MODEL_TIMEOUT_MS = 180_000;
const DEFAULT_REQUEST_TIMEOUT_MS = 30_000;
const MAX_USAGE_ITEMS = 10_000;
const MAX_USAGE_PAGES = 100;
const MAX_CANARY_KEY_ITEMS = 10_000;
const PRODUCTION_ADMIN = Object.freeze({ email: "admin@medopl.cn", consoleUserId: "usr-admin", accountId: "acct-admin", role: "admin" });
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

function existingAdminCredentials(email, password) {
  const normalizedEmail = String(email || "").trim().toLowerCase();
  if (normalizedEmail !== PRODUCTION_ADMIN.email || !String(password || "")) {
    throw new Error("existing_admin_verifier_credentials_unavailable");
  }
  return { email: normalizedEmail, password: String(password) };
}

function readOnlyPage(result, expectedSource, { page = 1, pageSize = 20, pagesRequired = false } = {}) {
  const envelope = sourceEnvelope(result, expectedSource, true);
  const data = envelope.data;
  if (!Array.isArray(data?.items) || !Number.isSafeInteger(data?.total) || data.total < 0 || data.page !== page || data.pageSize !== pageSize ||
    envelope.status !== (data.items.length === 0 ? "empty" : "available")) {
    throw new Error("production_read_only_page_invalid");
  }
  if (pagesRequired) {
    const expectedPages = data.total === 0 ? 1 : Math.ceil(data.total / pageSize);
    if (!Number.isSafeInteger(data.pages) || data.pages !== expectedPages) throw new Error("production_read_only_page_invalid");
  }
  return data;
}

function readOnlyNestedSource(value, expectedSource) {
  if (!value || value.source !== expectedSource || value.available !== true || value.status !== "available" ||
    !value.data || !Number.isFinite(Date.parse(value.fetchedAt))) {
    throw new Error(`production_nested_source_invalid:${expectedSource}`);
  }
  return value.data;
}

export async function verifyProductionReadOnlyRollout(options = {}) {
  const {
    origin,
    adminEmail,
    adminPassword,
    requestTimeoutMs = DEFAULT_REQUEST_TIMEOUT_MS,
    fetchImpl = globalThis.fetch,
    browserFactory,
    signal
  } = options;
  const credentials = existingAdminCredentials(adminEmail, adminPassword);
  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  const requestOptions = { fetchImpl, origin: normalizedOrigin, signal, timeoutMs: requestTimeoutMs };

  const health = (await requestJson({ ...requestOptions, path: "/api/healthz" })).payload;
  if (health?.status !== "ok" || Object.keys(health).length !== 1) throw new Error("production_health_invalid");
  const readiness = (await requestJson({ ...requestOptions, path: "/api/production/readiness" })).payload;
  if (readiness?.cloudImagesReady !== true) throw new Error("production_cloud_readiness_invalid");

  const auth = await login({ ...requestOptions, ...credentials });
  if (auth.user?.accountId !== PRODUCTION_ADMIN.accountId || auth.user?.role !== PRODUCTION_ADMIN.role) throw new Error("production_read_only_login_failed");
  const identity = sourceEnvelope(await requestJson({ ...requestOptions, auth, path: "/api/auth/me" }), "sub2api").data;
  const sub2apiUserId = String(identity?.sub2apiUserId || "");
  if (identity?.consoleUserId !== PRODUCTION_ADMIN.consoleUserId || identity?.accountId !== PRODUCTION_ADMIN.accountId ||
    identity?.role !== PRODUCTION_ADMIN.role || identity?.email !== PRODUCTION_ADMIN.email || identity?.status !== "active" ||
    !/^[1-9][0-9]*$/.test(sub2apiUserId) || !Number.isSafeInteger(Number(sub2apiUserId))) {
    throw new Error("production_admin_identity_invalid");
  }
  const endpoint = sourceEnvelope(await requestJson({ ...requestOptions, auth, path: "/api/gateway/endpoint" }), "sub2api").data;
  if (endpoint?.baseUrl !== "https://gflabtoken.cn/v1") throw new Error("production_gateway_endpoint_invalid");
  walletFact(sourceEnvelope(await requestJson({ ...requestOptions, auth, path: "/api/gateway/wallet" }), "sub2api"), sub2apiUserId);

  const keys = readOnlyPage(await requestJson({ ...requestOptions, auth, path: "/api/gateway/keys?page=1&pageSize=20" }), "sub2api", { pagesRequired: true });
  if (keys.items.some((key) => !/^[1-9][0-9]*$/.test(String(key?.id || "")) || ["key", "value"].some((field) => Object.hasOwn(key || {}, field)))) {
    throw new Error("production_gateway_key_page_invalid");
  }
  let usage = "not_applicable_no_key";
  if (keys.items[0]) {
    readOnlyPage(await requestJson({
      ...requestOptions,
      auth,
      path: `/api/gateway/keys/${encodeURIComponent(keys.items[0].id)}/usage?page=1&pageSize=20`
    }), "sub2api", { pagesRequired: true });
    usage = "available";
  }
  readOnlyPage(await requestJson({ ...requestOptions, auth, path: "/api/gateway/balance-history?page=1&pageSize=20" }), "sub2api", { pagesRequired: true });

  const overview = sourceEnvelope(await requestJson({ ...requestOptions, auth, path: "/api/operator/overview" }), "control-plane").data;
  readOnlyNestedSource(overview?.accounts, "control-plane");
  const workspaceSummary = readOnlyNestedSource(overview?.workspaces, "control-plane");
  if (!Number.isSafeInteger(workspaceSummary.total) || workspaceSummary.total < 0) throw new Error("production_workspace_summary_invalid");
  readOnlyPage(await requestJson({ ...requestOptions, auth, path: "/api/operator/accounts?page=1&pageSize=20" }), "control-plane+sub2api");
  readOnlyPage(await requestJson({ ...requestOptions, auth, path: "/api/operator/workspaces?page=1&pageSize=20" }), "control-plane+fabric+sub2api");

  const workspaces = readOnlyPage(await requestJson({ ...requestOptions, auth, path: "/api/workspaces?page=1&pageSize=20" }), "control-plane");
  const receipts = sourceEnvelope(await requestJson({ ...requestOptions, auth, path: "/api/billing/receipts?limit=20" }), "ledger", true);
  if (!Array.isArray(receipts.data?.receipts) || typeof receipts.data?.hasMore !== "boolean") throw new Error("production_ledger_source_invalid");

  let fabric = "not_applicable_no_workspace";
  if (workspaceSummary.total > 0) {
    readOnlyNestedSource(overview?.resources, "fabric");
    fabric = "available";
  }
  const workspace = workspaces.items[0];
  if (workspace?.id) {
    const runtime = sourceEnvelope(await requestJson({
      ...requestOptions,
      auth,
      path: `/api/workspaces/${encodeURIComponent(workspace.id)}/runtime-status`
    }), "fabric").data;
    if (runtime?.workspaceId && runtime.workspaceId !== workspace.id) throw new Error("production_fabric_source_invalid");
  }

  const retiredRoutes = await verifyRetiredRoutes({ origin: normalizedOrigin, fetchImpl, signal, requestTimeoutMs });
  const viewports = await verifyConsoleViewports({ origin: normalizedOrigin, browserFactory });
  return {
    ok: true,
    mode: "read-only",
    evidenceLevel: "read-only",
    writesPerformed: 0,
    accountId: PRODUCTION_ADMIN.accountId,
    consoleUserId: PRODUCTION_ADMIN.consoleUserId,
    sub2apiUserId,
    checks: {
      health: "ok",
      readiness: {
        cloudImagesReady: true,
        systemReady: readiness?.ready === true,
        workspaceImagesReady: readiness?.workspaceImagesReady === true,
        immutableImagesReady: readiness?.immutableImagesReady === true
      },
      identity: "authoritative",
      wallet: "available",
      keys: "page_1_size_20",
      usage,
      balanceHistory: "page_1_size_20",
      operator: ["overview", "accounts_page_1_size_20", "workspaces_page_1_size_20"],
      ledger: "available",
      fabric,
      retiredRoutes,
      viewports
    },
    viewports
  };
}

function exactObjectKeys(value, keys) {
  return value && typeof value === "object" && !Array.isArray(value) &&
    Object.keys(value).sort().join("\u0000") === [...keys].sort().join("\u0000");
}

const BASIC_CANARY_OPERATOR_PRECHARGE_MODE = "operator_precharge";
const BASIC_CANARY_PRECHARGE_RECOVERY_MODE = "operator_precharge_recovery";

function basicCanaryUsesPrechargeRecovery(approval) {
  return approval?.fundingMode === BASIC_CANARY_PRECHARGE_RECOVERY_MODE;
}

function basicCustomerCanaryApproval(value, approvalId, confirmation, now, requestedFundingMode = BASIC_CANARY_OPERATOR_PRECHARGE_MODE) {
  if (confirmation !== BASIC_CUSTOMER_CANARY_CONFIRMATION) throw new Error("production_basic_canary_confirmation_required");
  let approval;
  try {
    approval = typeof value === "string" ? JSON.parse(value) : value;
  } catch {
    throw new Error("production_basic_canary_approval_invalid");
  }
  const hasFundingMode = Object.hasOwn(approval || {}, "fundingMode");
  const fundingMode = hasFundingMode ? approval.fundingMode : BASIC_CANARY_OPERATOR_PRECHARGE_MODE;
  const prechargeRecovery = fundingMode === BASIC_CANARY_PRECHARGE_RECOVERY_MODE;
  const explicitOperatorPrecharge = fundingMode === BASIC_CANARY_OPERATOR_PRECHARGE_MODE && hasFundingMode;
  const approvalKeys = prechargeRecovery
    ? ["approvalId", "expiresAt", "fundingMode", "customer", "prechargeOperationId", "rechargeUsdMicros", "idempotencyKeys", "launch", "expected"]
    : explicitOperatorPrecharge
      ? ["approvalId", "expiresAt", "fundingMode", "customer", "rechargeUsdMicros", "idempotencyKeys", "launch", "expected"]
      : ["approvalId", "expiresAt", "customer", "rechargeUsdMicros", "idempotencyKeys", "launch", "expected"];
  const customerKeys = prechargeRecovery ? ["email", "accountId"] : ["email", "name"];
  const idempotencyKeyNames = prechargeRecovery ? ["workspaceLaunch"] : ["accountProvision", "walletAdjustment", "workspaceLaunch"];
  const expectedKeys = prechargeRecovery
    ? ["mergedSha", "cloudImageDigest", "nodePoolId", "resolvedInstanceType", "workspaceImageDigest", "model", "launchOperationId", "workspaceId"]
    : ["mergedSha", "cloudImageDigest", "nodePoolId", "resolvedInstanceType", "workspaceImageDigest", "model"];
  if (!exactObjectKeys(approval, approvalKeys) ||
    fundingMode !== requestedFundingMode || ![BASIC_CANARY_OPERATOR_PRECHARGE_MODE, BASIC_CANARY_PRECHARGE_RECOVERY_MODE].includes(fundingMode) ||
    approval.approvalId !== approvalId || !Number.isFinite(Date.parse(approval.expiresAt)) || Date.parse(approval.expiresAt) <= now.getTime() ||
    !exactObjectKeys(approval.customer, customerKeys) || !exactObjectKeys(approval.idempotencyKeys, idempotencyKeyNames) ||
    !exactObjectKeys(approval.launch, ["name", "packageId", "sizeGb", "autoRenew"]) ||
    !exactObjectKeys(approval.expected, expectedKeys)) {
    throw new Error("production_basic_canary_approval_invalid");
  }
  approval.customer.email = String(approval.customer.email || "").trim().toLowerCase();
  approval.expected.resolvedInstanceType = String(approval.expected.resolvedInstanceType || "").trim();
  const recharge = String(approval.rechargeUsdMicros || "");
  const keys = Object.values(approval.idempotencyKeys);
  if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(approval.customer.email) ||
    (prechargeRecovery
      ? !/^acct-[A-Za-z0-9-]+$/.test(String(approval.customer.accountId || "")) || recharge !== "60000000" ||
        !/^wallet-adjustment-[A-Za-z0-9-]+$/.test(String(approval.prechargeOperationId || ""))
      : !String(approval.customer.name || "").trim() || !/^[1-9][0-9]*$/.test(recharge)) ||
    keys.some((key) => typeof key !== "string" || key.trim() !== key || key.length < 8 || key.length > 200) || new Set(keys).size !== keys.length ||
    approval.launch.packageId !== "basic" || approval.launch.sizeGb !== 10 || approval.launch.autoRenew !== false || !String(approval.launch.name || "").trim() ||
    !/^[a-f0-9]{40}$/.test(String(approval.expected.mergedSha || "")) || !/^sha256:[a-f0-9]{64}$/.test(String(approval.expected.cloudImageDigest || "")) ||
    !/^np-[A-Za-z0-9-]+$/.test(approval.expected.nodePoolId) || !/^sha256:[a-f0-9]{64}$/.test(approval.expected.workspaceImageDigest) ||
    !/^[A-Za-z0-9][A-Za-z0-9.-]{1,63}$/.test(approval.expected.resolvedInstanceType) || !String(approval.expected.model || "").trim() ||
    (prechargeRecovery && (!/^workspace-launch-[A-Za-z0-9-]+$/.test(String(approval.expected.launchOperationId || "")) ||
      !/^ws-[A-Za-z0-9-]+$/.test(String(approval.expected.workspaceId || ""))))) {
    throw new Error("production_basic_canary_approval_invalid");
  }
  if (prechargeRecovery) {
    const launchOperationId = `workspace-launch-${stableCanaryId(approval.customer.accountId, approval.idempotencyKeys.workspaceLaunch).slice(0, 18)}`;
    const workspaceId = `ws-${stableCanaryId("workspace-launch-v2", approval.customer.accountId, launchOperationId).slice(0, 18)}`;
    if (approval.expected.launchOperationId !== launchOperationId || approval.expected.workspaceId !== workspaceId) {
      throw new Error("production_basic_canary_approval_invalid");
    }
  }
  return approval;
}

function usdMicrosToDecimal(value) {
  const micros = BigInt(value);
  return `${micros / 1_000_000n}.${String(micros % 1_000_000n).padStart(6, "0")}`;
}

function usdDecimalMicros(value) {
  const match = String(value || "").match(/^(0|[1-9][0-9]{0,12})(?:\.([0-9]{1,6}))?$/);
  if (!match) return null;
  const micros = BigInt(match[1]) * 1_000_000n + BigInt((match[2] || "").padEnd(6, "0") || "0");
  return micros <= 9_223_372_036_854_775_807n ? micros : null;
}

function internalFabricOrigin(value) {
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    throw new Error("production_basic_canary_internal_fabric_origin_required");
  }
  const privateHost = parsed.hostname === "localhost" || parsed.hostname === "127.0.0.1" || parsed.hostname.endsWith(".svc") ||
    /^(10\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.)/.test(parsed.hostname);
  if (!privateHost || !["http:", "https:"].includes(parsed.protocol) || parsed.pathname !== "/" || parsed.search || parsed.hash || parsed.username || parsed.password) {
    throw new Error("production_basic_canary_internal_fabric_origin_required");
  }
  return parsed.origin;
}

const MANUAL_REVIEW_DIAGNOSE_MODE = "manual_review_diagnose";
const MANUAL_REVIEW_FABRIC_POD_GET_SCRIPT = String.raw`
const http = require("node:http");
const path = process.argv[1];
if (!/^\/fabric\/[A-Za-z0-9_?=.&/%-]{1,1024}$/.test(path || "")) {
  process.stderr.write("manual_review_fabric_path_invalid\\n");
  process.exit(1);
}
const token = process.env.OPL_INTERNAL_SERVICE_TOKEN;
if (!token) {
  process.stderr.write("manual_review_fabric_auth_unavailable\\n");
  process.exit(1);
}
const request = http.get({
  hostname: "127.0.0.1",
  port: 8082,
  path,
  headers: { Authorization: "Bearer " + token }
}, (response) => {
  let body = "";
  response.setEncoding("utf8");
  response.on("data", (chunk) => {
    body += chunk;
    if (Buffer.byteLength(body) > 4 * 1024 * 1024) request.destroy(new Error("manual_review_fabric_response_too_large"));
  });
  response.on("end", () => {
    if (response.statusCode === 200) {
      try {
        process.stdout.write(JSON.stringify({ statusCode: 200, payload: JSON.parse(body), errorCode: "none" }));
      } catch {
        process.stdout.write(JSON.stringify({ statusCode: 0, payload: null, errorCode: "manual_review_fabric_response_invalid" }));
      }
      return;
    }
    let errorCode = "manual_review_fabric_get_http_error";
    try {
      const parsed = JSON.parse(body);
      if (/^[a-z0-9_]{1,80}$/.test(String(parsed?.error || ""))) errorCode = parsed.error;
    } catch {}
    process.stdout.write(JSON.stringify({ statusCode: Number(response.statusCode || 0), payload: null, errorCode }));
  });
});
request.setTimeout(120000, () => request.destroy(new Error("manual_review_fabric_get_timeout")));
request.on("error", () => {
  process.stderr.write("manual_review_fabric_get_unavailable\\n");
  process.exitCode = 1;
});
`;

function manualReviewDiagnoseTarget(value) {
  let target;
  try {
    target = typeof value === "string" ? JSON.parse(value) : value;
  } catch {
    throw new Error("manual_review_diagnose_target_invalid");
  }
  const targetKeys = [
    "accountId", "launchOperationId", "workspaceId", "computeAllocationId", "storageId",
    "nodePoolId", "machineId", "nodeName", "cvmInstanceId"
  ];
  if (!exactObjectKeys(target, targetKeys) ||
    !/^acct-[A-Za-z0-9-]+$/.test(String(target.accountId || "")) ||
    !/^workspace-launch-[A-Za-z0-9-]+$/.test(String(target.launchOperationId || "")) ||
    !/^ws-[A-Za-z0-9-]+$/.test(String(target.workspaceId || "")) ||
    !/^ca_[A-Za-z0-9-]+$/.test(String(target.computeAllocationId || "")) ||
    !/^vol_[A-Za-z0-9-]+$/.test(String(target.storageId || "")) ||
    !/^np-[A-Za-z0-9-]+$/.test(String(target.nodePoolId || "")) ||
    !/^np-[A-Za-z0-9-]+$/.test(String(target.machineId || "")) ||
    !/^[0-9]{1,3}(?:\.[0-9]{1,3}){3}$/.test(String(target.nodeName || "")) ||
    !/^ins-[A-Za-z0-9-]+$/.test(String(target.cvmInstanceId || ""))) {
    throw new Error("manual_review_diagnose_target_invalid");
  }
  return target;
}

function safeManualReviewStatus(value) {
  const status = String(value || "");
  return /^[a-z][a-z0-9_]{0,63}$/.test(status) ? status : "unknown";
}

function safeManualReviewErrorCode(value, fallback) {
  const explicit = String(value?.errorCode || "");
  const match = String(value?.message || value || "").match(/^request_failed:GET:[^:]+:[0-9]+:([a-z0-9_]+)$/);
  const code = explicit || match?.[1] || "";
  return new Set([
    "compute_allocation_not_found",
    "machine_ownership_not_found",
    "monthly_provider_truth_unavailable",
    "invalid_monthly_provider_truth"
  ]).has(code) ? code : fallback;
}

function manualReviewFabricPodName(value) {
  const pod = String(value || "");
  if (!/^[a-z0-9](?:[-a-z0-9]{0,251}[a-z0-9])?$/.test(pod)) throw new Error("manual_review_diagnose_config_invalid");
  return pod;
}

function manualReviewFabricNamespace(value) {
  const namespace = String(value || "");
  if (!/^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$/.test(namespace)) throw new Error("manual_review_diagnose_config_invalid");
  return namespace;
}

async function manualReviewFabricPodGet({ kubeconfigPath, fabricNamespace, fabricPod, execFileImpl }, path) {
  const result = await execFileImpl("kubectl", [
    "--kubeconfig", String(kubeconfigPath), "-n", fabricNamespace, "exec", fabricPod, "-c", "fabric", "--",
    "node", "-e", MANUAL_REVIEW_FABRIC_POD_GET_SCRIPT, path
  ], {
    encoding: "utf8",
    maxBuffer: 4 * 1024 * 1024
  });
  let response;
  try {
    response = JSON.parse(result.stdout);
  } catch {
    throw new Error("manual_review_fabric_response_invalid");
  }
  if (!response || !Number.isInteger(response.statusCode) || response.statusCode < 0 || response.statusCode > 599 ||
    !Object.hasOwn(response, "payload") || !/^[a-z0-9_]{1,80}$/.test(String(response.errorCode || ""))) {
    throw new Error("manual_review_fabric_response_invalid");
  }
  return response;
}

async function manualReviewFabricGet(options, path, absentCode, unavailableCode) {
  try {
    const response = options.fabricPod
      ? await manualReviewFabricPodGet(options, path)
      : { statusCode: 200, payload: (await requestJson({ ...options, path, method: "GET" })).payload, errorCode: "none" };
    if (response.statusCode === 200 && response.errorCode === "none") {
      return { state: "present", payload: response.payload, errorCode: "none" };
    }
    if (response.statusCode === 404) return { state: "absent", payload: null, errorCode: absentCode };
    if (response.statusCode === 401) return { state: "unavailable", payload: null, errorCode: "fabric_get_unauthorized" };
    if (response.statusCode === 403) return { state: "unavailable", payload: null, errorCode: "fabric_get_forbidden" };
    return { state: "unavailable", payload: null, errorCode: safeManualReviewErrorCode(response, unavailableCode) };
  } catch (error) {
    const message = String(error?.message || "");
    if (message.startsWith(`request_failed:GET:${path}:404:`)) {
      return { state: "absent", payload: null, errorCode: absentCode };
    }
    return { state: "unavailable", payload: null, errorCode: safeManualReviewErrorCode(error, unavailableCode) };
  }
}

function manualReviewComputeOperation(operations, target) {
  const matches = Array.isArray(operations)
    ? operations.filter((operation) => operation?.action === "create_compute_allocation" && operation?.resourceId === target.computeAllocationId)
    : [];
  if (matches.length === 0) return { state: "absent", status: "unknown", operation: null };
  if (matches.length !== 1) return { state: "multiple", status: "unknown", operation: null };
  return { state: "present", status: safeManualReviewStatus(matches[0]?.status), operation: matches[0] };
}

function manualReviewStorageState(operations, truth, target) {
  const matches = Array.isArray(operations)
    ? operations.filter((operation) => operation?.action === "create_storage_volume" && operation?.resourceId === target.storageId)
    : [];
  if (matches.length === 0) {
    if (truth?.state === "present" && ["ready", "absent"].includes(truth.payload?.storageState)) return "attempted_unknown";
    return "not_started";
  }
  if (matches.length !== 1) return "attempted_unknown";
  const operation = matches[0];
  const storage = truth?.payload?.storage;
  const exactOperation = operation?.accountId === target.accountId && operation?.workspaceId === target.workspaceId &&
    operation?.operationId === `${target.launchOperationId}:storage`;
  const exactStorage = storage?.id === target.storageId && storage?.accountId === target.accountId && storage?.workspaceId === target.workspaceId &&
    /^disk-/.test(String(storage?.providerResourceId || "")) && storage?.sizeGb === 10 && storage?.chargeType === "PREPAID" &&
    storage?.renewFlag === "NOTIFY_AND_MANUAL_RENEW";
  if (truth?.state === "present" && exactOperation && exactStorage && truth.payload?.storageState === "ready") return "present";
  if (truth?.state === "present" && exactOperation && exactStorage && truth.payload?.storageState === "absent") return "absent";
  return "attempted_unknown";
}

function manualReviewNodeEvidence(node, target, allocation) {
  const labels = node?.metadata?.labels || {};
  const taints = Array.isArray(node?.spec?.taints) ? node.spec.taints : [];
  const workspaceTaints = taints.filter((taint) => taint?.key === "oplcloud.cn/workspace-id");
  const privateIPs = (node?.status?.addresses || []).filter((address) => address?.type === "InternalIP").map((address) => String(address.address || ""));
  return {
    resourceIdLabelMatches: labels["oplcloud.cn/resource-id"] === target.computeAllocationId,
    accountIdLabelMatches: labels["oplcloud.cn/account-id"] === target.accountId,
    workspaceIdLabelMatches: labels["oplcloud.cn/workspace-id"] === target.workspaceId,
    unallocatedTaint: workspaceTaints.length === 1 && workspaceTaints[0]?.value === "unallocated" && workspaceTaints[0]?.effect === "NoSchedule",
    nodeMatches: node?.metadata?.name === target.nodeName,
    privateIpMatches: privateIPs.length === 1 && privateIPs[0] === allocation?.privateIp
  };
}

export async function diagnoseManualReviewRecovery({
  fabricOrigin,
  internalServiceToken,
  fabricPod,
  fabricNamespace,
  target: rawTarget,
  kubeconfigPath,
  fetchImpl = globalThis.fetch,
  execFileImpl = defaultExecFile
} = {}) {
  const target = manualReviewDiagnoseTarget(rawTarget);
  if (!String(kubeconfigPath || "").startsWith("/")) {
    throw new Error("manual_review_diagnose_config_invalid");
  }
  const podTransport = Boolean(fabricPod || fabricNamespace);
  const options = podTransport
    ? {
        kubeconfigPath: String(kubeconfigPath),
        fabricPod: manualReviewFabricPodName(fabricPod),
        fabricNamespace: manualReviewFabricNamespace(fabricNamespace),
        execFileImpl
      }
    : (() => {
        const origin = internalFabricOrigin(fabricOrigin);
        if (!String(internalServiceToken || "")) throw new Error("manual_review_diagnose_config_invalid");
        return { fetchImpl, origin, headers: { authorization: `Bearer ${internalServiceToken}` } };
      })();
  const computeRead = await manualReviewFabricGet(
    options,
    `/fabric/compute-allocations/${encodeURIComponent(target.computeAllocationId)}`,
    "compute_allocation_not_found",
    "compute_allocation_unavailable"
  );
  const ownershipRead = await manualReviewFabricGet(
    options,
    `/fabric/machine-ownerships/${encodeURIComponent(target.computeAllocationId)}`,
    "machine_ownership_not_found",
    "machine_ownership_unavailable"
  );
  const operationsRead = await manualReviewFabricGet(options, "/fabric/operations", "fabric_operations_not_found", "fabric_operations_unavailable");
  const providerTruthRead = await manualReviewFabricGet(
    options,
    `/fabric/monthly-provider-truth?computeAllocationId=${encodeURIComponent(target.computeAllocationId)}&storageVolumeId=${encodeURIComponent(target.storageId)}`,
    "monthly_provider_truth_unavailable",
    "monthly_provider_truth_unavailable"
  );

  let nodeRead;
  try {
    const result = await execFileImpl("kubectl", ["--kubeconfig", String(kubeconfigPath), "get", "node", target.nodeName, "-o", "json"], {
      encoding: "utf8",
      maxBuffer: 4 * 1024 * 1024
    });
    nodeRead = { state: "present", payload: JSON.parse(result.stdout), errorCode: "none" };
  } catch {
    nodeRead = { state: "unavailable", payload: null, errorCode: "node_get_unavailable" };
  }

  const allocation = computeRead.payload;
  const ownership = ownershipRead.payload;
  const computeOperation = manualReviewComputeOperation(operationsRead.payload, target);
  const operation = computeOperation.operation;
  const plan = operation?.redactedProviderPayload?.allocationPlan;
  const truth = providerTruthRead.payload;
  const truthCompute = truth?.compute;
  const node = manualReviewNodeEvidence(nodeRead.payload, target, allocation);
  const truthReady = providerTruthRead.state === "present" && truth?.computeState === "ready";
  const identity = {
    accountMatches: computeRead.state === "present" && ownershipRead.state === "present" && allocation?.accountId === target.accountId && ownership?.accountId === target.accountId &&
      truthReady && truthCompute?.accountId === target.accountId,
    workspaceMatches: computeRead.state === "present" && ownershipRead.state === "present" && allocation?.workspaceId === target.workspaceId && ownership?.workspaceId === target.workspaceId &&
      truthReady && truthCompute?.workspaceId === target.workspaceId,
    launchOperationMatches: computeOperation.state === "present" && operation?.operationId === `${target.launchOperationId}:compute` && operation?.accountId === target.accountId && operation?.workspaceId === target.workspaceId,
    poolMatches: allocation?.nodePoolId === target.nodePoolId && ownership?.nodePoolId === target.nodePoolId && plan?.nodePoolId === target.nodePoolId &&
      truthReady && truthCompute?.nodePoolId === target.nodePoolId,
    machineMatches: allocation?.machineName === target.machineId && ownership?.machineId === target.machineId && truthReady && truthCompute?.machineName === target.machineId,
    cvmMatches: String(allocation?.cvmInstanceId || allocation?.instanceId || "") === target.cvmInstanceId && ownership?.instanceId === target.cvmInstanceId &&
      truthReady && String(truthCompute?.providerResourceId || truthCompute?.cvmInstanceId || truthCompute?.instanceId || "") === target.cvmInstanceId,
    nodeMatches: allocation?.nodeName === target.nodeName && ownership?.nodeName === target.nodeName && truthReady && truthCompute?.nodeName === target.nodeName && node.nodeMatches,
    privateIpMatches: node.privateIpMatches && truthReady && truthCompute?.privateIp === allocation?.privateIp,
    skuMatches: Boolean(allocation?.instanceType) && allocation?.instanceType === plan?.instanceType && allocation?.instanceType === allocation?.providerData?.instanceType &&
      truthReady && truthCompute?.instanceType === allocation?.instanceType && truthCompute?.providerData?.instanceType === allocation?.instanceType,
    zoneMatches: Boolean(allocation?.zone) && allocation?.zone === allocation?.providerData?.zone && truthReady && truthCompute?.zone === allocation?.zone && truthCompute?.providerData?.zone === allocation?.zone,
    prepaidMatches: allocation?.chargeType === "PREPAID" && allocation?.providerData?.chargeType === "PREPAID" && truthReady && truthCompute?.chargeType === "PREPAID" && truthCompute?.providerData?.chargeType === "PREPAID",
    manualRenewMatches: allocation?.renewFlag === "NOTIFY_AND_MANUAL_RENEW" && allocation?.providerData?.renewFlag === "NOTIFY_AND_MANUAL_RENEW" &&
      truthReady && truthCompute?.renewFlag === "NOTIFY_AND_MANUAL_RENEW" && truthCompute?.providerData?.renewFlag === "NOTIFY_AND_MANUAL_RENEW"
  };
  const storage = { state: manualReviewStorageState(operationsRead.payload, providerTruthRead, target) };
  const nodeProjection = {
    resourceIdLabelMatches: node.resourceIdLabelMatches,
    accountIdLabelMatches: node.accountIdLabelMatches,
    workspaceIdLabelMatches: node.workspaceIdLabelMatches,
    unallocatedTaint: node.unallocatedTaint
  };
  let errorCode = "none";
  if (computeRead.state !== "present") errorCode = computeRead.errorCode;
  else if (ownershipRead.state !== "present") errorCode = ownershipRead.errorCode;
  else if (operationsRead.state !== "present") errorCode = operationsRead.errorCode;
  else if (computeOperation.state !== "present") errorCode = "compute_allocation_operation_identity_invalid";
  else if (providerTruthRead.state !== "present") errorCode = providerTruthRead.errorCode;
  else if (nodeRead.state !== "present") errorCode = nodeRead.errorCode;
  else if (Object.values(identity).some((matches) => matches !== true)) errorCode = "compute_identity_mismatch";
  else if (!Object.values(nodeProjection).every((matches) => matches === true)) errorCode = "node_ownership_or_taint_mismatch";
  else if (storage.state === "attempted_unknown") errorCode = "storage_create_attempt_unknown";
  else if (storage.state === "absent") errorCode = "storage_absent_recovery_not_authorized";
  return {
    schemaVersion: 1,
    operationMode: MANUAL_REVIEW_DIAGNOSE_MODE,
    status: "diagnosed",
    recoveryEligible: errorCode === "none" && storage.state === "not_started",
    errorCode,
    allocation: { state: computeRead.state, status: safeManualReviewStatus(allocation?.status) },
    ownership: { state: ownershipRead.state, status: safeManualReviewStatus(ownership?.status) },
    computeOperation: { state: computeOperation.state, status: computeOperation.status },
    providerTruth: {
      state: providerTruthRead.state === "present" ? "available" : providerTruthRead.state,
      computeState: ["ready", "absent", "unknown"].includes(truth?.computeState) ? truth.computeState : "unknown",
      storageState: ["ready", "absent", "unknown"].includes(truth?.storageState) ? truth.storageState : "unknown",
      errorCode: providerTruthRead.state === "present" ? "none" : providerTruthRead.errorCode
    },
    identity,
    node: nodeProjection,
    storage,
    mutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 }
  };
}

const COMPUTE_CLAIM_DIAGNOSE_MODE = "compute_claim_diagnose";
const COMPUTE_CLAIM_RECOVER_MODE = "compute_claim_recover";
const COMPUTE_CLAIM_REASONS = new Set([
  "none", "local_identity", "provider_describe", "iam_rbac", "multiple_candidate",
  "identity_mismatch", "node_ownership_conflict", "storage_already_started"
]);
const COMPUTE_CLAIM_BLOCKED_ERROR_CODES = new Set([...COMPUTE_CLAIM_REASONS, "identity_mismatch"]);
const COMPUTE_CLAIM_FAILURE_STAGES = new Set([
  "", "cvm_pre_read", "cvm_conflict_check", "cvm_mutation_precondition", "cvm_rename_readback", "cvm_tag_readback", "cvm_final_readback",
  "cvm_provisioner_transport", "cvm_mutation_evidence", "node_pre_cvm_read", "node_pre_read", "node_conflict_check", "node_patch_build",
  "node_patch_readback", "node_final_readback", "claim_final_readback"
]);
const COMPUTE_CLAIM_PROVIDER_ERROR_CLASSES = new Set([
  "", "client_unavailable", "malformed_readback", "ownership_conflict", "readback_mismatch", "timeout", "iam_rbac", "provider_error",
  "transport_error", "evidence_incomplete"
]);
const COMPUTE_CLAIM_CVM_MISSING_FIELDS = new Set([
  "instance", "instance_name", "opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id"
]);
const COMPUTE_CLAIM_NODE_MISSING_FIELDS = new Set(["node_ownership"]);
const COMPUTE_CLAIM_TARGET_KEYS = [
  "launchOperationId", "accountId", "workspaceId", "computeAllocationId", "storageId", "packageId", "poolId", "nodePoolId",
  "machineName", "nodeName", "cvmInstanceId", "privateIp", "instanceType", "zone", "chargeType", "periodMonths", "renewFlag", "deadline"
];
const COMPUTE_CLAIM_RECOVERY_FORBIDDEN_WRITES = Object.freeze([
  "create_launch", "debit", "recharge", "refund", "scale", "create_cvm", "create_second_cbs", "delete", "replace"
]);
const COMPUTE_CLAIM_RECOVERY_STAGE_LIMITS = Object.freeze({
  claim: Object.freeze({ sub2api: 0, tencent: 5, kubernetes: 1 }),
  storage: 1,
  attachment: 1,
  secret: 1,
  runtime: 1,
  activation: 1,
  receipt: 1
});
const WORKSPACE_LAUNCH_READBACK_DIAGNOSE_MODE = "workspace_launch_readback_diagnose";
const WORKSPACE_LAUNCH_READBACK_RECOVER_MODE = "workspace_launch_readback_recover";
const WORKSPACE_LAUNCH_READBACK_STAGES = Object.freeze([
  "storage", "attachment", "secret", "runtime", "activation", "receipt"
]);
const WORKSPACE_LAUNCH_READBACK_PHASES = Object.freeze({
  storage: "storage_fulfilling",
  attachment: "attaching",
  secret: "secret_writing",
  runtime: "runtime_starting",
  activation: "activating",
  receipt: "receipt_pending"
});
const WORKSPACE_LAUNCH_READBACK_REMAINING_WRITES = Object.freeze({
  storage: Object.freeze(["create_original_pv_pvc_attachment", "upsert_original_gateway_secret", "create_original_workspace_runtime", "activate_original_workspace", "record_original_purchase_receipt"]),
  attachment: Object.freeze(["upsert_original_gateway_secret", "create_original_workspace_runtime", "activate_original_workspace", "record_original_purchase_receipt"]),
  secret: Object.freeze(["create_original_workspace_runtime", "activate_original_workspace", "record_original_purchase_receipt"]),
  runtime: Object.freeze(["activate_original_workspace", "record_original_purchase_receipt"]),
  activation: Object.freeze(["record_original_purchase_receipt"]),
  receipt: Object.freeze([])
});
const WORKSPACE_LAUNCH_READBACK_FORBIDDEN_WRITES = Object.freeze([
  "create_launch", "debit", "recharge", "refund", "scale", "create_cvm", "create_second_cbs", "delete", "replace", "retry_unknown_stage_write"
]);
const RECOVERED_WORKSPACE_E2E_ALLOWED_WRITES = Object.freeze([
  "control_plane_e2e_attempt_reservation",
  "single_workspace_model_request",
  "control_plane_e2e_attempt_completion"
]);
const RECOVERED_WORKSPACE_E2E_FORBIDDEN_WRITES = Object.freeze([
  "launch", "debit", "recharge", "refund", "scale", "create_cvm", "create_cbs", "tencent", "kubernetes"
]);
const COMPUTE_CLAIM_FABRIC_POD_PROOF_SCRIPT = String.raw`
const http = require("node:http");
const path = process.argv[1];
const body = process.argv[2];
if (path !== "/fabric/compute-claim-recovery/proof" || !body || Buffer.byteLength(body) > 16384) {
  process.stderr.write("compute_claim_fabric_request_invalid\\n");
  process.exit(1);
}
const token = process.env.OPL_INTERNAL_SERVICE_TOKEN;
if (!token) {
  process.stderr.write("compute_claim_fabric_auth_unavailable\\n");
  process.exit(1);
}
const request = http.request({
  hostname: "127.0.0.1",
  port: 8082,
  path,
  method: "POST",
  headers: {
    Authorization: "Bearer " + token,
    "Content-Type": "application/json",
    "Content-Length": Buffer.byteLength(body)
  }
}, (response) => {
  let responseBody = "";
  response.setEncoding("utf8");
  response.on("data", (chunk) => {
    responseBody += chunk;
    if (Buffer.byteLength(responseBody) > 1024 * 1024) request.destroy(new Error("compute_claim_fabric_response_too_large"));
  });
  response.on("end", () => {
    let payload = null;
    try { payload = JSON.parse(responseBody); } catch {}
    const reason = String(payload?.reason || payload?.error || "provider_describe");
    process.stdout.write(JSON.stringify({ statusCode: Number(response.statusCode || 0), payload, errorCode: /^[a-z0-9_]{1,80}$/.test(reason) ? reason : "provider_describe" }));
  });
});
request.setTimeout(120000, () => request.destroy(new Error("compute_claim_fabric_proof_timeout")));
request.on("error", () => {
  process.stderr.write("compute_claim_fabric_proof_unavailable\\n");
  process.exitCode = 1;
});
request.end(body);
`;

function validIPv4(value) {
  const parts = String(value || "").split(".");
  return parts.length === 4 && parts.every((part) => /^(?:0|[1-9][0-9]{0,2})$/.test(part) && Number(part) <= 255);
}

function computeClaimTarget(value, allowedPackages = new Set(["basic"])) {
  let target;
  try {
    target = typeof value === "string" ? JSON.parse(value) : value;
  } catch {
    throw new Error("compute_claim_recovery_target_invalid");
  }
  if (!exactObjectKeys(target, COMPUTE_CLAIM_TARGET_KEYS) ||
    !/^workspace-launch-[A-Za-z0-9-]+$/.test(String(target.launchOperationId || "")) ||
    !/^acct-[A-Za-z0-9-]+$/.test(String(target.accountId || "")) ||
    !/^ws-[A-Za-z0-9-]+$/.test(String(target.workspaceId || "")) ||
    !/^ca_[A-Za-z0-9_\-]+$/.test(String(target.computeAllocationId || "")) ||
    !/^vol_[A-Za-z0-9_\-]+$/.test(String(target.storageId || "")) ||
    !allowedPackages.has(target.packageId) ||
    !/^pool-[A-Za-z0-9-]+$/.test(String(target.poolId || "")) ||
    !/^np-[A-Za-z0-9-]+$/.test(String(target.nodePoolId || "")) ||
    !/^[A-Za-z0-9][A-Za-z0-9.-]{1,127}$/.test(String(target.machineName || "")) ||
    !validIPv4(target.nodeName) || !validIPv4(target.privateIp) ||
    !/^ins-[A-Za-z0-9-]+$/.test(String(target.cvmInstanceId || "")) ||
    !/^[A-Za-z0-9][A-Za-z0-9.-]{1,63}$/.test(String(target.instanceType || "")) ||
    !/^[a-z][a-z0-9-]{2,63}$/.test(String(target.zone || "")) || target.chargeType !== "PREPAID" || target.periodMonths !== 1 ||
    target.renewFlag !== "NOTIFY_AND_MANUAL_RENEW" || !Number.isFinite(Date.parse(target.deadline))) {
    throw new Error("compute_claim_recovery_target_invalid");
  }
  return { ...target };
}

function computeClaimBaseRequest(target) {
  return {
    launchOperationId: target.launchOperationId,
    accountId: target.accountId,
    workspaceId: target.workspaceId,
    computeAllocationId: target.computeAllocationId,
    storageVolumeId: target.storageId,
    packageId: target.packageId,
    poolId: target.poolId,
    nodePoolId: target.nodePoolId
  };
}

function computeClaimControlPlaneRequest(target) {
  return {
    accountId: target.accountId,
    workspaceId: target.workspaceId,
    computeAllocationId: target.computeAllocationId,
    storageId: target.storageId,
    packageId: target.packageId,
    poolId: target.poolId,
    nodePoolId: target.nodePoolId,
    machineName: target.machineName,
    nodeName: target.nodeName,
    cvmInstanceId: target.cvmInstanceId,
    privateIp: target.privateIp,
    instanceType: target.instanceType,
    zone: target.zone
  };
}

function computeClaimStableSuffix(...parts) {
  return createHash("sha256").update(parts.join(":"), "utf8").digest("hex");
}

function computeClaimStorageBindingValid(storageState, storageProviderResourceId) {
  return storageState === "storage_not_started" && storageProviderResourceId === "" ||
    storageState === "storage_existing_exact" && /^disk-[a-z0-9-]{1,80}$/.test(storageProviderResourceId);
}

function computeClaimRecoveryAllowedWrites(storageState) {
  const storageWrite = storageState === "storage_existing_exact" ? "reuse_original_cbs" : "create_original_cbs";
  return [
    "claim_existing_cvm_node",
    storageWrite,
    "create_original_pv_pvc_attachment",
    "upsert_original_gateway_secret",
    "create_original_workspace_runtime",
    "activate_original_workspace",
    "record_original_purchase_receipt"
  ];
}

function computeClaimExpectedRecoveryResources(target, workspaceApiKeyId, storageState, storageProviderResourceId) {
  const attachmentOperationId = `${target.launchOperationId}:attachment`;
  const runtimeOperationId = `${target.launchOperationId}:workspace:runtime`;
  return {
    computeOperationId: `${target.launchOperationId}:compute`,
    storageOperationId: `${target.launchOperationId}:storage`,
    storageState,
    storageProviderResourceId,
    attachmentId: `att_${computeClaimStableSuffix(attachmentOperationId).slice(0, 18)}`,
    attachmentOperationId,
    workspaceApiKeyId,
    gatewaySecretRef: `opl-gateway-${computeClaimStableSuffix(target.workspaceId).slice(0, 16)}`,
    secretOperationId: `${target.launchOperationId}:workspace:secret:gateway-secret`,
    runtimeId: `rt_${computeClaimStableSuffix(target.workspaceId, runtimeOperationId).slice(0, 18)}`,
    runtimeOperationId,
    receiptOperationId: `${target.launchOperationId}:purchase-receipt`
  };
}

function computeClaimProofProjection(value) {
  const integer = (field) => Number.isInteger(value?.[field]) && value[field] >= 0 ? value[field] : -1;
  const mutationEvidence = (source, missingFields) => {
    const attempted = Number.isInteger(source?.attempted) && source.attempted >= 0 ? source.attempted : -1;
    const confirmed = Number.isInteger(source?.confirmed) && source.confirmed >= 0 ? source.confirmed : -1;
    const unknown = Number.isInteger(source?.unknown) && source.unknown >= 0 ? source.unknown : -1;
    let missing = null;
    if (Array.isArray(source?.missing) && source.missing.every((field) => typeof field === "string" && missingFields.has(field))) {
      missing = [...source.missing];
    } else if (source && !Object.hasOwn(source, "missing") && attempted === confirmed && attempted >= 0 && unknown === 0) {
      missing = [];
    }
    return { attempted, confirmed, unknown, missing };
  };
  const failureStage = String(value?.failureStage || "");
  const providerErrorClass = String(value?.providerErrorClass || "");
  const storageProviderResourceId = String(value?.storageProviderResourceId || "");
  return {
    schemaVersion: Number(value?.schemaVersion || 0),
    eligible: value?.eligible === true,
    reason: COMPUTE_CLAIM_REASONS.has(String(value?.reason || "")) ? String(value.reason) : "provider_describe",
    storageState: new Set(["storage_not_started", "storage_existing_exact", "unknown"]).has(String(value?.storageState || "")) ? String(value.storageState) : "unknown",
    storageProviderResourceId: /^disk-[a-z0-9-]{1,80}$/.test(storageProviderResourceId) ? storageProviderResourceId : "",
    launchOperationId: String(value?.launchOperationId || ""),
    accountId: String(value?.accountId || ""),
    workspaceId: String(value?.workspaceId || ""),
    computeAllocationId: String(value?.computeAllocationId || ""),
    storageVolumeId: String(value?.storageVolumeId || ""),
    packageId: String(value?.packageId || ""),
    poolId: String(value?.poolId || ""),
    nodePoolId: String(value?.nodePoolId || ""),
    machineName: String(value?.machineName || ""),
    nodeName: String(value?.nodeName || ""),
    cvmInstanceId: String(value?.cvmInstanceId || ""),
    privateIp: String(value?.privateIp || ""),
    instanceType: String(value?.instanceType || ""),
    zone: String(value?.zone || ""),
    chargeType: String(value?.chargeType || ""),
    periodMonths: integer("periodMonths"),
    renewFlag: String(value?.renewFlag || ""),
    deadline: String(value?.deadline || ""),
    nodeOwnershipState: String(value?.nodeOwnershipState || ""),
    cvmOwnershipState: String(value?.cvmOwnershipState || ""),
    sub2apiMutationCount: integer("sub2apiMutationCount"),
    tencentMutationCount: integer("tencentMutationCount"),
    kubernetesMutationCount: integer("kubernetesMutationCount"),
    failureStage: COMPUTE_CLAIM_FAILURE_STAGES.has(failureStage) ? failureStage : "invalid",
    providerErrorClass: COMPUTE_CLAIM_PROVIDER_ERROR_CLASSES.has(providerErrorClass) ? providerErrorClass : "invalid",
    evidence: {
      cvm: mutationEvidence(value?.evidence?.cvm, COMPUTE_CLAIM_CVM_MISSING_FIELDS),
      node: mutationEvidence(value?.evidence?.node, COMPUTE_CLAIM_NODE_MISSING_FIELDS)
    }
  };
}

function computeClaimMutationEvidenceMatches(evidence, count, maximum, confirmed) {
  if (!evidence || !Number.isInteger(count) || count < 0 || count > maximum || evidence.attempted !== count ||
    !Number.isInteger(evidence.confirmed) || evidence.confirmed < 0 || evidence.confirmed > evidence.attempted ||
    !Number.isInteger(evidence.unknown) || evidence.unknown < 0 || evidence.unknown > evidence.attempted ||
    evidence.confirmed + evidence.unknown > evidence.attempted || !Array.isArray(evidence.missing) ||
    new Set(evidence.missing).size !== evidence.missing.length) return false;
  return !confirmed || evidence.confirmed === evidence.attempted && evidence.unknown === 0 && evidence.missing.length === 0;
}

function computeClaimEvidenceMatches(proof, confirmed) {
  return COMPUTE_CLAIM_FAILURE_STAGES.has(proof.failureStage) && COMPUTE_CLAIM_PROVIDER_ERROR_CLASSES.has(proof.providerErrorClass) &&
    computeClaimMutationEvidenceMatches(proof.evidence?.cvm, proof.tencentMutationCount, 5, confirmed) &&
    computeClaimMutationEvidenceMatches(proof.evidence?.node, proof.kubernetesMutationCount, 1, confirmed);
}

function computeClaimProofBaseMatches(proof, target) {
  return proof.schemaVersion === 1 && proof.launchOperationId === target.launchOperationId && proof.accountId === target.accountId &&
    proof.workspaceId === target.workspaceId && proof.computeAllocationId === target.computeAllocationId && proof.storageVolumeId === target.storageId &&
    proof.packageId === target.packageId && proof.poolId === target.poolId && proof.nodePoolId === target.nodePoolId &&
	proof.sub2apiMutationCount === 0 && computeClaimEvidenceMatches(proof, false);
}

function computeClaimProofMatchesTarget(proof, target, claimed = false, expectedStorage = null) {
  const storageMatches = computeClaimStorageBindingValid(proof.storageState, proof.storageProviderResourceId) &&
    (!expectedStorage || proof.storageState === expectedStorage.storageState && proof.storageProviderResourceId === expectedStorage.storageProviderResourceId);
  return computeClaimProofBaseMatches(proof, target) && proof.eligible && proof.reason === "none" && storageMatches &&
    proof.machineName === target.machineName && proof.nodeName === target.nodeName && proof.cvmInstanceId === target.cvmInstanceId &&
    proof.privateIp === target.privateIp && proof.instanceType === target.instanceType && proof.zone === target.zone &&
    proof.chargeType === target.chargeType && proof.periodMonths === target.periodMonths && proof.renewFlag === target.renewFlag && proof.deadline === target.deadline &&
    (claimed ? proof.nodeOwnershipState === "target_owned" : new Set(["unallocated", "target_owned"]).has(proof.nodeOwnershipState)) &&
    (claimed ? proof.cvmOwnershipState === "target_owned" : new Set(["recoverable", "target_owned"]).has(proof.cvmOwnershipState)) &&
    computeClaimEvidenceMatches(proof, true) && proof.failureStage === "" && proof.providerErrorClass === "";
}

function computeClaimReleaseEvidence(evidence) {
  return {
    mergedSha: String(evidence.mergedSha),
    cloudImageDigest: String(evidence.cloudDigest),
    revisions: {
      controlPlane: String(evidence.services.controlPlane.revision),
      fabric: String(evidence.services.fabric.revision),
      ledger: String(evidence.services.ledger.revision)
    }
  };
}

async function currentComputeClaimCloudRevision({ mergedSha, cloudImageDigest, kubeconfigPath, namespace, cloudRevisionEvidenceReader, execFileImpl }) {
  const reader = cloudRevisionEvidenceReader || ((input) => readBasicCanaryCloudRevisionEvidence({
    ...input,
    kubeconfigPath,
    namespace,
    execFileImpl
  }));
  const evidence = await reader({ expectedMergedSha: mergedSha, expectedCloudDigest: cloudImageDigest });
  return validateBasicCanaryCloudRevisionEvidence(evidence, mergedSha, cloudImageDigest);
}

async function computeClaimFabricPodProof({ target, fabricPod, kubeconfigPath, namespace, execFileImpl }) {
  const body = JSON.stringify(computeClaimBaseRequest(target));
  const result = await execFileImpl("kubectl", [
    "--kubeconfig", String(kubeconfigPath), "-n", namespace, "exec", manualReviewFabricPodName(fabricPod), "-c", "fabric", "--",
    "node", "-e", COMPUTE_CLAIM_FABRIC_POD_PROOF_SCRIPT, "/fabric/compute-claim-recovery/proof", body
  ], { encoding: "utf8", maxBuffer: 1024 * 1024 });
  const response = parseExecJson(result.stdout, "compute_claim_recovery_fabric_response_invalid");
  if (!response || !Number.isInteger(response.statusCode) || response.statusCode < 100 || response.statusCode > 599 ||
    !Object.hasOwn(response, "payload") || !/^[a-z0-9_]{1,80}$/.test(String(response.errorCode || ""))) {
    throw new Error("compute_claim_recovery_fabric_response_invalid");
  }
  return response;
}

function computeClaimArtifact(mode, target, release, proof, eligible, errorCode, approval = null) {
  if (!eligible) return blockedComputeClaimArtifact(mode, errorCode);
  const artifact = {
    schemaVersion: 2,
    operationMode: mode,
    status: eligible ? (mode === COMPUTE_CLAIM_DIAGNOSE_MODE ? "proven" : "claimed") : "blocked",
    recoveryEligible: eligible,
    errorCode,
    release: computeClaimReleaseEvidence(release),
    target: { ...target },
    proof,
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 }
  };
  if (approval) {
    artifact.approval = {
      approvalId: approval.approvalId,
      approvalDigest: createHash("sha256").update(canonicalJson(approval)).digest("hex")
    };
  }
  return artifact;
}

function workspaceLaunchReadbackTarget(value) {
	try {
		return computeClaimTarget(value, new Set(["basic", "pro"]));
	} catch {
		throw new Error("workspace_launch_readback_target_invalid");
	}
}

function workspaceLaunchReadbackAllowedWrites(stage) {
  const remaining = WORKSPACE_LAUNCH_READBACK_REMAINING_WRITES[stage];
  return remaining ? [`confirm_original_${stage}_from_authoritative_readback`, ...remaining] : [];
}

const WORKSPACE_LAUNCH_READBACK_PROOF_TARGET_KEYS = Object.freeze([
  ...COMPUTE_CLAIM_TARGET_KEYS, "storageGb", "autoRenew", "priceVersion", "totalChargeUsdMicros",
  "periodStart", "paidThrough", "billingAnchorDay"
]);
const WORKSPACE_LAUNCH_READBACK_RESOURCE_KEYS = Object.freeze([
  "computeAllocationId", "computeProviderResourceId", "storageVolumeId", "storageProviderResourceId", "storageZone",
  "storageSizeGb", "storageChargeType", "storageRenewFlag", "storageDeadline", "attachmentId", "attachmentProviderId",
  "gatewaySecretRef", "gatewaySecretFingerprint", "workspaceApiKeyId", "runtimeId", "runtimeServiceName", "receiptId"
]);
const WORKSPACE_LAUNCH_READBACK_OPERATION_KEYS = Object.freeze([
  "launchOperationId", "launchRequestHash", "machineOwnershipId", "compute", "storage", "attachment", "secret", "runtime",
  "activationOperationId", "receiptOperationId"
]);
const WORKSPACE_LAUNCH_READBACK_OPERATION_IDENTITY_KEYS = Object.freeze([
  "idempotencyKey", "fabricRecordId", "fabricOperationId", "requestHash", "resourceOperationId", "providerOperationId"
]);

function workspaceLaunchReadbackProofTarget(value, expected) {
  const storageGb = Number(value?.storageGb);
  const totalChargeUsdMicros = Number(value?.totalChargeUsdMicros);
  const billingAnchorDay = Number(value?.billingAnchorDay);
  let baseTarget;
  try {
    baseTarget = computeClaimTarget(Object.fromEntries(COMPUTE_CLAIM_TARGET_KEYS.map((key) => [key, value?.[key]])), new Set(["basic", "pro"]));
  } catch {
    throw new Error("workspace_launch_readback_proof_invalid");
  }
  if (!exactObjectKeys(value, WORKSPACE_LAUNCH_READBACK_PROOF_TARGET_KEYS) || JSON.stringify(baseTarget) !== JSON.stringify(expected) ||
    !Number.isSafeInteger(storageGb) || storageGb <= 0 || !Number.isSafeInteger(totalChargeUsdMicros) || totalChargeUsdMicros <= 0 ||
    !Number.isInteger(billingAnchorDay) || billingAnchorDay < 1 || billingAnchorDay > 31 || typeof value.autoRenew !== "boolean" ||
    !/^[A-Za-z0-9._-]{3,80}$/.test(String(value.priceVersion || "")) || !Number.isFinite(Date.parse(value.periodStart)) ||
    !Number.isFinite(Date.parse(value.paidThrough)) || Date.parse(value.paidThrough) <= Date.parse(value.periodStart)) {
    throw new Error("workspace_launch_readback_proof_invalid");
  }
  return value;
}

function workspaceLaunchReadbackOperationIdentity(value, idempotencyKey, required, providerRequired) {
  const text = (key) => String(value?.[key] || "");
  if (!exactObjectKeys(value, WORKSPACE_LAUNCH_READBACK_OPERATION_IDENTITY_KEYS) || text("idempotencyKey") !== idempotencyKey) {
    throw new Error("workspace_launch_readback_proof_invalid");
  }
  const authority = ["fabricRecordId", "fabricOperationId", "requestHash", "resourceOperationId"];
  if (required) {
    if (authority.some((key) => !text(key)) || (providerRequired && !text("providerOperationId"))) {
      throw new Error("workspace_launch_readback_proof_invalid");
    }
  } else if (authority.some((key) => text(key) !== "") || text("providerOperationId") !== "") {
    throw new Error("workspace_launch_readback_proof_invalid");
  }
  return value;
}

function workspaceLaunchReadbackProof(value, target) {
  const customerKeys = ["email", "accountId", "ownerUserId"];
  const budgetKeys = ["attempted", "confirmed", "unknown", "max"];
  const keys = [
    "schemaVersion", "eligible", "reason", "stage", "customer", "target", "resources", "operationIds", "workspaceImageDigest",
    "attemptBudget", "allowedWrites", "forbiddenWrites", "sub2apiMutationCount", "tencentMutationCount", "kubernetesMutationCount"
  ];
  const stage = String(value?.stage || "");
  const customerEmail = String(value?.customer?.email || "").trim().toLowerCase();
  const expectedAllowedWrites = workspaceLaunchReadbackAllowedWrites(stage);
  const resources = value?.resources;
  const operations = value?.operationIds;
  const currentIndex = WORKSPACE_LAUNCH_READBACK_STAGES.indexOf(stage);
  const resourceID = (name, pattern, optional = false) => {
    const item = String(resources?.[name] || "");
    return (optional && item === "") || pattern.test(item);
  };
  if (!exactObjectKeys(value, keys) || value.schemaVersion !== 1 || value.eligible !== true || value.reason !== "none" ||
    !WORKSPACE_LAUNCH_READBACK_STAGES.includes(stage) || !exactObjectKeys(value.customer, customerKeys) ||
    !exactObjectKeys(resources, WORKSPACE_LAUNCH_READBACK_RESOURCE_KEYS) || !exactObjectKeys(operations, WORKSPACE_LAUNCH_READBACK_OPERATION_KEYS) ||
    !exactObjectKeys(value.attemptBudget, budgetKeys) || customerEmail !== value.customer.email || !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(customerEmail) ||
    value.customer.accountId !== target.accountId || !/^usr-[A-Za-z0-9-]+$/.test(String(value.customer.ownerUserId || "")) ||
    !resourceID("computeAllocationId", /^ca_[A-Za-z0-9_-]+$/) ||
    !resourceID("computeProviderResourceId", /^ins-[A-Za-z0-9-]+$/) || !resourceID("storageVolumeId", /^vol_[A-Za-z0-9_-]+$/) ||
    !resourceID("storageProviderResourceId", /^disk-[A-Za-z0-9-]+$/) || resources.computeAllocationId !== target.computeAllocationId ||
    resources.computeProviderResourceId !== target.cvmInstanceId || resources.storageVolumeId !== target.storageId || resources.storageZone !== target.zone ||
    resources.storageSizeGb !== value?.target?.storageGb || resources.storageChargeType !== target.chargeType || resources.storageRenewFlag !== target.renewFlag ||
    resources.storageDeadline !== target.deadline || !resourceID("attachmentId", /^[A-Za-z0-9_-]{3,128}$/, stage === "storage") ||
    !resourceID("attachmentProviderId", /^[A-Za-z0-9_./:-]{3,256}$/, stage === "storage") ||
    !resourceID("gatewaySecretRef", /^opl-gateway-[a-f0-9]{16}$/) || !resourceID("gatewaySecretFingerprint", /^sha256:[a-f0-9]{64}$/, new Set(["storage", "attachment"]).has(stage)) ||
    !Number.isSafeInteger(resources.workspaceApiKeyId) || resources.workspaceApiKeyId <= 0 ||
    !resourceID("runtimeId", /^[A-Za-z0-9_-]{3,128}$/, new Set(["storage", "attachment", "secret"]).has(stage)) ||
    !resourceID("runtimeServiceName", /^[A-Za-z0-9-]{3,128}$/, new Set(["storage", "attachment", "secret"]).has(stage)) ||
    !resourceID("receiptId", /^[A-Za-z0-9_-]{3,128}$/, stage !== "receipt") ||
    operations.launchOperationId !== target.launchOperationId || !operations.launchRequestHash || !operations.machineOwnershipId ||
    operations.activationOperationId !== `${target.launchOperationId}:activation` || operations.receiptOperationId !== `${target.launchOperationId}:purchase-receipt` ||
    !/^sha256:[a-f0-9]{64}$/.test(String(value.workspaceImageDigest || "")) ||
    JSON.stringify(value.attemptBudget) !== JSON.stringify({ attempted: 1, confirmed: 0, unknown: 1, max: 1 }) ||
    JSON.stringify(value.allowedWrites) !== JSON.stringify(expectedAllowedWrites) ||
    JSON.stringify(value.forbiddenWrites) !== JSON.stringify(WORKSPACE_LAUNCH_READBACK_FORBIDDEN_WRITES) ||
    value.sub2apiMutationCount !== 0 || value.tencentMutationCount !== 0 || value.kubernetesMutationCount !== 0) {
    throw new Error("workspace_launch_readback_proof_invalid");
  }
  workspaceLaunchReadbackProofTarget(value.target, target);
  const identities = [
    ["compute", `${target.launchOperationId}:compute`, true, true],
    ["storage", `${target.launchOperationId}:storage`, true, true],
    ["attachment", `${target.launchOperationId}:attachment`, currentIndex >= 1, true],
    ["secret", `${target.launchOperationId}:workspace:secret:gateway-secret`, currentIndex >= 2, false],
    ["runtime", `${target.launchOperationId}:workspace:runtime`, currentIndex >= 3, true]
  ];
  for (const [name, idempotencyKey, required, providerRequired] of identities) {
    workspaceLaunchReadbackOperationIdentity(operations[name], idempotencyKey, required, providerRequired);
  }
  if (operations.compute.providerOperationId !== operations.machineOwnershipId) throw new Error("workspace_launch_readback_proof_invalid");
  return JSON.parse(JSON.stringify(value));
}

function workspaceLaunchReadbackApproval(value, expected, now) {
  let approval;
  try {
    approval = typeof value === "string" ? JSON.parse(value) : value;
  } catch {
    throw new Error("workspace_launch_readback_approval_invalid");
  }
  const proof = expected.proof;
  const keys = [
    "schemaVersion", "approvalId", "expiresAt", "mergedMainSha", "cloudImageDigest", "workspaceImageDigest", "confirmation",
    "idempotencyKey", "recoveryKey", "stage", "customer", "target", "resources", "operationIds", "attemptBudget", "allowedWrites", "forbiddenWrites"
  ];
  const opaque = (item) => /^[a-z0-9][a-z0-9-]{2,47}$/.test(String(item || "")) &&
    !/(?:api-?key|bearer|credential|password|secret|token)/.test(String(item));
  const binding = {
    customer: proof.customer,
    target: proof.target,
    resources: proof.resources,
    operationIds: proof.operationIds,
    attemptBudget: proof.attemptBudget,
    allowedWrites: proof.allowedWrites,
    forbiddenWrites: proof.forbiddenWrites
  };
  if (!exactObjectKeys(approval, keys) || approval.schemaVersion !== 1 || approval.approvalId !== expected.approvalId ||
    !opaque(approval.approvalId) || !opaque(approval.idempotencyKey) || !opaque(approval.recoveryKey) ||
    !Number.isFinite(Date.parse(approval.expiresAt)) || Date.parse(approval.expiresAt) <= now.getTime() ||
    approval.mergedMainSha !== expected.mergedSha || approval.cloudImageDigest !== expected.cloudImageDigest ||
    approval.confirmation !== WORKSPACE_LAUNCH_READBACK_RECOVERY_CONFIRMATION) {
    throw new Error("workspace_launch_readback_approval_invalid");
  }
  if (approval.workspaceImageDigest !== proof.workspaceImageDigest || approval.stage !== proof.stage ||
    Object.entries(binding).some(([key, item]) => JSON.stringify(approval[key]) !== JSON.stringify(item))) {
    throw new Error("workspace_launch_readback_proof_drift");
  }
  return JSON.parse(JSON.stringify(approval));
}

function workspaceLaunchReadbackStaticApproval(value, expected, now) {
  let approval;
  try {
    approval = typeof value === "string" ? JSON.parse(value) : value;
  } catch {
    throw new Error("workspace_launch_readback_approval_invalid");
  }
  const keys = [
    "schemaVersion", "approvalId", "expiresAt", "mergedMainSha", "cloudImageDigest", "workspaceImageDigest", "confirmation",
    "idempotencyKey", "recoveryKey", "stage", "customer", "target", "resources", "operationIds", "attemptBudget", "allowedWrites", "forbiddenWrites"
  ];
  if (!exactObjectKeys(approval, keys) || approval.schemaVersion !== 1 || approval.approvalId !== expected.approvalId ||
    approval.mergedMainSha !== expected.mergedSha || approval.cloudImageDigest !== expected.cloudImageDigest ||
    approval.confirmation !== WORKSPACE_LAUNCH_READBACK_RECOVERY_CONFIRMATION || !WORKSPACE_LAUNCH_READBACK_STAGES.includes(approval.stage) ||
    !Number.isFinite(Date.parse(approval.expiresAt)) || Date.parse(approval.expiresAt) <= now.getTime() ||
    !exactObjectKeys(approval.customer, ["email", "accountId", "ownerUserId"]) ||
    String(approval.customer.email || "").trim().toLowerCase() !== String(expected.customerEmail || "").trim().toLowerCase() ||
    approval.customer.accountId !== expected.target.accountId || JSON.stringify(approval.target) !== JSON.stringify({ ...expected.target,
      storageGb: approval.target?.storageGb, autoRenew: approval.target?.autoRenew, priceVersion: approval.target?.priceVersion,
      totalChargeUsdMicros: approval.target?.totalChargeUsdMicros, periodStart: approval.target?.periodStart,
      paidThrough: approval.target?.paidThrough, billingAnchorDay: approval.target?.billingAnchorDay
    })) {
    throw new Error("workspace_launch_readback_approval_invalid");
  }
  workspaceLaunchReadbackProofTarget(approval.target, expected.target);
  return approval;
}

async function workspaceLaunchReadbackSession({ fetchImpl, origin, adminEmail, adminPassword, requestTimeoutMs }) {
  const credentials = existingAdminCredentials(adminEmail, adminPassword);
  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  const auth = await login({ fetchImpl, origin: normalizedOrigin, ...credentials, timeoutMs: requestTimeoutMs });
  if (auth.user?.accountId !== PRODUCTION_ADMIN.accountId || auth.user?.role !== PRODUCTION_ADMIN.role || !auth.csrfToken) {
    throw new Error("workspace_launch_readback_admin_login_failed");
  }
  return { auth, normalizedOrigin };
}

async function readWorkspaceLaunchReadbackProof({ fetchImpl, origin, auth, target, requestTimeoutMs }) {
  const response = await requestJson({
    fetchImpl,
    origin,
    auth,
    path: `/api/operator/workspace-launches/${encodeURIComponent(target.launchOperationId)}/readback-recovery-proof`,
    timeoutMs: requestTimeoutMs
  });
  return workspaceLaunchReadbackProof(response.payload, target);
}

export async function diagnoseWorkspaceLaunchReadbackRecovery({
  target: rawTarget,
  mergedSha,
  cloudImageDigest,
  origin,
  adminEmail,
  adminPassword,
  customerEmail,
  kubeconfigPath,
  namespace,
  requestTimeoutMs = DEFAULT_REQUEST_TIMEOUT_MS,
  cloudRevisionEvidenceReader,
  execFileImpl = defaultExecFile,
  fetchImpl = globalThis.fetch,
  now = new Date()
} = {}) {
  const target = workspaceLaunchReadbackTarget(rawTarget);
  if (!String(kubeconfigPath || "").startsWith("/") || !/^[a-z0-9][a-z0-9-]{0,62}$/.test(String(namespace || "")) ||
    !Number.isInteger(requestTimeoutMs) || requestTimeoutMs < 1 || requestTimeoutMs > 300_000) {
    throw new Error("workspace_launch_readback_config_invalid");
  }
  const release = await currentComputeClaimCloudRevision({ mergedSha, cloudImageDigest, kubeconfigPath, namespace, cloudRevisionEvidenceReader, execFileImpl });
  const { auth, normalizedOrigin } = await workspaceLaunchReadbackSession({ fetchImpl, origin, adminEmail, adminPassword, requestTimeoutMs });
  const proof = await readWorkspaceLaunchReadbackProof({ fetchImpl, origin: normalizedOrigin, auth, target, requestTimeoutMs });
  if (String(customerEmail || "").trim().toLowerCase() !== proof.customer.email) throw new Error("workspace_launch_readback_customer_identity_mismatch");
  return {
    schemaVersion: 1,
    operationMode: WORKSPACE_LAUNCH_READBACK_DIAGNOSE_MODE,
    status: "proven",
    recoveryEligible: true,
    errorCode: "none",
    release: computeClaimReleaseEvidence(release),
    target,
    proof,
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    verifiedAt: now.toISOString()
  };
}

export async function recoverWorkspaceLaunchReadbackRecovery({
  target: rawTarget,
  approvalJson,
  approvalId,
  mergedSha,
  cloudImageDigest,
  origin,
  adminEmail,
  adminPassword,
  customerEmail,
  internalServiceToken,
  kubeconfigPath,
  namespace,
  requestTimeoutMs = DEFAULT_REQUEST_TIMEOUT_MS,
  cloudRevisionEvidenceReader,
  execFileImpl = defaultExecFile,
  fetchImpl = globalThis.fetch,
  now = new Date()
} = {}) {
  const target = workspaceLaunchReadbackTarget(rawTarget);
  if (!internalServiceToken || internalServiceToken !== String(internalServiceToken).trim()) throw new Error("workspace_launch_readback_capability_required");
  if (!String(kubeconfigPath || "").startsWith("/") || !/^[a-z0-9][a-z0-9-]{0,62}$/.test(String(namespace || "")) ||
    !Number.isInteger(requestTimeoutMs) || requestTimeoutMs < 1 || requestTimeoutMs > 300_000) {
    throw new Error("workspace_launch_readback_config_invalid");
  }
  workspaceLaunchReadbackStaticApproval(approvalJson, { approvalId, mergedSha, cloudImageDigest, customerEmail, target }, now);
  const release = await currentComputeClaimCloudRevision({ mergedSha, cloudImageDigest, kubeconfigPath, namespace, cloudRevisionEvidenceReader, execFileImpl });
  const { auth, normalizedOrigin } = await workspaceLaunchReadbackSession({ fetchImpl, origin, adminEmail, adminPassword, requestTimeoutMs });
  const proof = await readWorkspaceLaunchReadbackProof({ fetchImpl, origin: normalizedOrigin, auth, target, requestTimeoutMs });
  if (String(customerEmail || "").trim().toLowerCase() !== proof.customer.email) throw new Error("workspace_launch_readback_customer_identity_mismatch");
  const approval = workspaceLaunchReadbackApproval(approvalJson, { approvalId, mergedSha, cloudImageDigest, proof }, now);
  const approvalDigest = createHash("sha256").update(canonicalJson(approval)).digest("hex");
  const response = await computeClaimControlPlanePost({
    fetchImpl,
    origin: normalizedOrigin,
    auth,
    path: `/api/operator/workspace-launches/${encodeURIComponent(target.launchOperationId)}/recover`,
    idempotencyKey: approval.idempotencyKey,
    capability: internalServiceToken,
    requestTimeoutMs,
    body: {
      accountId: target.accountId,
      billingOperationId: target.launchOperationId,
      evidenceRef: `readback-${approval.recoveryKey}`,
      approval: { ...approval, approvalDigest }
    }
  });
  const operation = response.payload;
  const budget = operation?.continuationAttemptBudgets?.[proof.stage];
  if (response.statusCode !== 200 || operation?.operationId !== target.launchOperationId || operation?.accountId !== target.accountId ||
    operation?.workspaceId !== target.workspaceId || operation?.status === "manual_review" ||
    JSON.stringify(budget) !== JSON.stringify({ attempted: 1, confirmed: 1, unknown: 0, max: 1 })) {
    throw new Error("workspace_launch_readback_recovery_unconfirmed");
  }
  return {
    schemaVersion: 1,
    operationMode: WORKSPACE_LAUNCH_READBACK_RECOVER_MODE,
    status: "converged",
    recoveryEligible: true,
    errorCode: "none",
    release: computeClaimReleaseEvidence(release),
    target,
    stage: proof.stage,
    proof,
    approval: { approvalId: approval.approvalId, approvalDigest },
    operation: {
      operationId: operation.operationId,
      accountId: operation.accountId,
      workspaceId: operation.workspaceId,
      status: String(operation.status || ""),
      phase: String(operation.phase || ""),
      attemptBudget: budget
    },
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    backgroundMutationCountsState: "unknown",
    verifiedAt: now.toISOString()
  };
}

export async function diagnoseComputeClaimRecovery({
  target: rawTarget,
  mergedSha,
  cloudImageDigest,
  kubeconfigPath,
  namespace,
  cloudRevisionEvidenceReader,
  execFileImpl = defaultExecFile
} = {}) {
  const target = computeClaimTarget(rawTarget);
  if (!/^[a-f0-9]{40}$/.test(String(mergedSha || "")) || !/^sha256:[a-f0-9]{64}$/.test(String(cloudImageDigest || "")) ||
    !String(kubeconfigPath || "").startsWith("/") || !/^[a-z0-9][a-z0-9-]{0,62}$/.test(String(namespace || ""))) {
    throw new Error("compute_claim_recovery_config_invalid");
  }
  const release = await currentComputeClaimCloudRevision({ mergedSha, cloudImageDigest, kubeconfigPath, namespace, cloudRevisionEvidenceReader, execFileImpl });
  const response = await computeClaimFabricPodProof({
    target,
    fabricPod: release.services.fabric.pod,
    kubeconfigPath,
    namespace,
    execFileImpl
  });
  const proof = computeClaimProofProjection(response.payload);
  const eligible = response.statusCode === 200 && computeClaimProofMatchesTarget(proof, target, false) &&
    proof.tencentMutationCount === 0 && proof.kubernetesMutationCount === 0;
  let errorCode = eligible ? "none" : proof.reason;
  if (!computeClaimProofBaseMatches(proof, target) || proof.tencentMutationCount !== 0 || proof.kubernetesMutationCount !== 0) {
    errorCode = "identity_mismatch";
  }
  return computeClaimArtifact(COMPUTE_CLAIM_DIAGNOSE_MODE, target, release, proof, eligible, errorCode);
}

function computeClaimRecoveryApproval(value, expected, now) {
  let approval;
  try {
    approval = typeof value === "string" ? JSON.parse(value) : value;
  } catch {
    throw new Error("compute_claim_recovery_approval_invalid");
  }
  const keys = [
    "schemaVersion", "approvalId", "expiresAt", "mergedMainSha", "cloudImageDigest", "workspaceImageDigest", "confirmation",
    "idempotencyKey", "recoveryKey", "customer", "target", "resources", "attemptLimits", "allowedWrites", "forbiddenWrites"
  ];
  let approvedTarget;
  try {
    approvedTarget = computeClaimTarget(approval?.target);
  } catch {
    throw new Error("compute_claim_recovery_approval_invalid");
  }
  const validOpaque = (value) => /^[a-z0-9][a-z0-9-]{2,47}$/.test(String(value || "")) &&
    !/(?:api-?key|bearer|credential|password|secret|token)/.test(String(value));
  const customerEmail = String(approval?.customer?.email || "").trim().toLowerCase();
  const workspaceApiKeyId = String(approval?.resources?.workspaceApiKeyId || "");
  const storageState = String(approval?.resources?.storageState || "");
  const storageProviderResourceId = String(approval?.resources?.storageProviderResourceId || "");
  const expectedResources = computeClaimExpectedRecoveryResources(approvedTarget, workspaceApiKeyId, storageState, storageProviderResourceId);
  const expectedAllowedWrites = computeClaimRecoveryAllowedWrites(storageState);
  if (!exactObjectKeys(approval, keys) || approval.schemaVersion !== 2 || approval.approvalId !== expected.approvalId ||
    !validOpaque(approval.approvalId) || !validOpaque(approval.idempotencyKey) || !validOpaque(approval.recoveryKey) ||
    !Number.isFinite(Date.parse(approval.expiresAt)) ||
    Date.parse(approval.expiresAt) <= now.getTime() || approval.mergedMainSha !== expected.mergedSha || approval.cloudImageDigest !== expected.cloudImageDigest ||
    !/^sha256:[a-f0-9]{64}$/.test(String(approval.workspaceImageDigest || "")) || approval.confirmation !== COMPUTE_CLAIM_RECOVERY_CONFIRMATION ||
    JSON.stringify(approvedTarget) !== JSON.stringify(expected.target) || !exactObjectKeys(approval.customer, ["email", "accountId"]) ||
    customerEmail !== approval.customer.email || !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(customerEmail) || approval.customer.accountId !== approvedTarget.accountId ||
    !exactObjectKeys(approval.resources, Object.keys(expectedResources)) || !/^[1-9][0-9]*$/.test(workspaceApiKeyId) ||
    !computeClaimStorageBindingValid(storageState, storageProviderResourceId) ||
    JSON.stringify(approval.resources) !== JSON.stringify(expectedResources) ||
    !exactObjectKeys(approval.attemptLimits, Object.keys(COMPUTE_CLAIM_RECOVERY_STAGE_LIMITS)) ||
    !exactObjectKeys(approval.attemptLimits.claim, Object.keys(COMPUTE_CLAIM_RECOVERY_STAGE_LIMITS.claim)) ||
    JSON.stringify(approval.attemptLimits) !== JSON.stringify(COMPUTE_CLAIM_RECOVERY_STAGE_LIMITS) ||
    JSON.stringify(approval.allowedWrites) !== JSON.stringify(expectedAllowedWrites) ||
    JSON.stringify(approval.forbiddenWrites) !== JSON.stringify(COMPUTE_CLAIM_RECOVERY_FORBIDDEN_WRITES)) {
    throw new Error("compute_claim_recovery_approval_invalid");
  }
  return { ...approval, customer: { ...approval.customer, email: customerEmail }, target: approvedTarget };
}

async function computeClaimControlPlanePost({ fetchImpl, origin, auth, path, body, idempotencyKey, capability, requestTimeoutMs }) {
  const response = await fetchImpl(`${origin}${path}`, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      cookie: auth.cookie,
      "x-opl-csrf": auth.csrfToken,
      "x-opl-compute-claim-capability": capability,
      ...(idempotencyKey ? { "Idempotency-Key": idempotencyKey } : {})
    },
    body: JSON.stringify(body),
    redirect: "manual",
    signal: readOnlyRequestSignal(undefined, requestTimeoutMs)
  });
  const text = await response.text();
  let payload;
  try {
    payload = text ? JSON.parse(text) : {};
  } catch {
    throw new Error("compute_claim_recovery_control_plane_response_invalid");
  }
  return { statusCode: response.status, payload };
}

export async function recoverComputeClaim({
  target: rawTarget,
  approvalJson,
  approvalId,
  mergedSha,
  cloudImageDigest,
  origin,
  adminEmail,
  adminPassword,
  internalServiceToken,
  kubeconfigPath,
  namespace,
  requestTimeoutMs = DEFAULT_REQUEST_TIMEOUT_MS,
  cloudRevisionEvidenceReader,
  execFileImpl = defaultExecFile,
  fetchImpl = globalThis.fetch,
  now = new Date()
} = {}) {
  const target = computeClaimTarget(rawTarget);
  const approval = computeClaimRecoveryApproval(approvalJson, { approvalId, mergedSha, cloudImageDigest, target }, now);
  if (!internalServiceToken || internalServiceToken !== String(internalServiceToken).trim()) {
    throw new Error("compute_claim_recovery_capability_required");
  }
  const credentials = existingAdminCredentials(adminEmail, adminPassword);
  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  if (!String(kubeconfigPath || "").startsWith("/") || !/^[a-z0-9][a-z0-9-]{0,62}$/.test(String(namespace || "")) ||
    !Number.isInteger(requestTimeoutMs) || requestTimeoutMs < 1 || requestTimeoutMs > 300_000) {
    throw new Error("compute_claim_recovery_config_invalid");
  }
  const release = await currentComputeClaimCloudRevision({ mergedSha, cloudImageDigest, kubeconfigPath, namespace, cloudRevisionEvidenceReader, execFileImpl });
  const auth = await login({ fetchImpl, origin: normalizedOrigin, ...credentials, timeoutMs: requestTimeoutMs });
  if (auth.user?.accountId !== PRODUCTION_ADMIN.accountId || auth.user?.role !== PRODUCTION_ADMIN.role || !auth.csrfToken) {
    throw new Error("compute_claim_recovery_admin_login_failed");
  }
  try {
    const accountAuthority = await readBasicCanaryAccountAuthority(
      { fetchImpl, origin: normalizedOrigin, timeoutMs: requestTimeoutMs }, auth, approval, target.accountId
    );
    if (!accountAuthority.found) throw new Error("account_not_found");
  } catch {
    throw new Error("compute_claim_recovery_customer_identity_mismatch");
  }
  const path = `/api/operator/workspace-launches/${encodeURIComponent(target.launchOperationId)}/compute-claim-recovery`;
  const requestBody = computeClaimControlPlaneRequest(target);
  const approvalDigest = createHash("sha256").update(canonicalJson(approval)).digest("hex");
  const claimResponse = await computeClaimControlPlanePost({
    fetchImpl,
    origin: normalizedOrigin,
    auth,
    path: `${path}/claim`,
    idempotencyKey: approval.idempotencyKey,
    capability: internalServiceToken,
    requestTimeoutMs,
    body: {
      ...requestBody,
      approvalId: approval.approvalId,
      approvalDigest,
      expiresAt: approval.expiresAt,
      mergedMainSha: approval.mergedMainSha,
      cloudImageDigest: approval.cloudImageDigest,
      workspaceImageDigest: approval.workspaceImageDigest,
      customerEmail: approval.customer.email,
      recoveryKey: approval.recoveryKey,
      resources: approval.resources,
      attemptLimits: approval.attemptLimits,
      allowedWrites: approval.allowedWrites,
      forbiddenWrites: approval.forbiddenWrites,
      confirm: approval.confirmation
    }
  });
  const claimed = computeClaimProofProjection(claimResponse.payload);
  const eligible = claimResponse.statusCode === 200 && computeClaimProofMatchesTarget(claimed, target, true, approval.resources);
  const errorCode = eligible ? "none" : computeClaimProofBaseMatches(claimed, target) && claimed.reason !== "none" ? claimed.reason : "identity_mismatch";
  return computeClaimArtifact(COMPUTE_CLAIM_RECOVER_MODE, target, release, claimed, eligible, errorCode, approval);
}

const COMPUTE_CLAIM_CONTINUATION_MODE = "compute_claim_recover_continuation";
const COMPUTE_CLAIM_CONTINUATION_PHASES = new Set([
  "storage_fulfilling", "attaching", "secret_writing", "runtime_starting", "activating", "receipt_pending"
]);
const COMPUTE_CLAIM_CONTINUATION_TERMINAL_STATUSES = new Set(["manual_review", "failed", "refunded"]);

function computeClaimContinuationCredentials(email, password) {
  const normalizedEmail = String(email || "").trim().toLowerCase();
  if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(normalizedEmail) || !String(password || "")) {
    throw new Error("compute_claim_continuation_customer_credentials_unavailable");
  }
  return { email: normalizedEmail, password: String(password) };
}

function computeClaimWorkspaceUrl(value, workspaceId) {
  const errorCode = "compute_claim_continuation_workspace_url_invalid";
  const expected = `https://workspace.medopl.cn/w/${workspaceId}/`;
  if (value !== expected) throw new Error(errorCode);
  const parsed = assertPublicHttpsUrl(value, errorCode, { hostname: "workspace.medopl.cn" });
  return parsed.toString();
}

function computeClaimContinuationLaunch(value, target) {
	const expectedStorageGb = target.packageId === "basic" ? 10 : target.packageId === "pro" ? 100 : 0;
  const launch = {
    operationId: String(value?.operationId || ""),
    accountId: String(value?.accountId || ""),
    workspaceId: String(value?.workspaceId || ""),
    status: String(value?.status || ""),
    phase: String(value?.phase || ""),
    packageId: String(value?.packageId || ""),
    sizeGb: Number(value?.sizeGb),
    autoRenew: value?.autoRenew,
    priceVersion: String(value?.priceVersion || ""),
    currency: String(value?.currency || ""),
    totalChargeUsdMicros: Number(value?.totalChargeUsdMicros),
    computeAllocationId: String(value?.computeAllocationId || ""),
    storageId: String(value?.storageId || ""),
    attachmentId: String(value?.attachmentId || ""),
    workspaceApiKeyId: String(value?.workspaceApiKeyId || ""),
    receiptId: String(value?.receiptId || ""),
    runtimeServiceName: String(value?.runtimeServiceName || ""),
    url: String(value?.url || ""),
    errorCode: String(value?.errorCode || "")
  };
  if (COMPUTE_CLAIM_CONTINUATION_TERMINAL_STATUSES.has(launch.status)) {
    throw new Error(`compute_claim_continuation_${launch.status}`);
  }
  if (launch.errorCode) throw new Error("compute_claim_continuation_error_code");
  if (launch.status === "succeeded" || launch.phase === "succeeded") {
    if (launch.status !== "succeeded" || launch.phase !== "succeeded") throw new Error("compute_claim_continuation_phase_invalid");
  } else if (!COMPUTE_CLAIM_CONTINUATION_PHASES.has(launch.phase)) {
    throw new Error("compute_claim_continuation_phase_invalid");
  }
	if (launch.operationId !== target.launchOperationId || launch.accountId !== target.accountId || launch.workspaceId !== target.workspaceId ||
		launch.packageId !== target.packageId || launch.sizeGb !== expectedStorageGb || typeof launch.autoRenew !== "boolean" || launch.currency !== "USD" ||
    !launch.priceVersion || !Number.isSafeInteger(launch.totalChargeUsdMicros) || launch.totalChargeUsdMicros <= 0 ||
    launch.computeAllocationId !== target.computeAllocationId || launch.storageId !== target.storageId) {
    throw new Error("compute_claim_continuation_identity_mismatch");
  }
  if (launch.status === "succeeded" && (!launch.attachmentId || !launch.receiptId || !launch.runtimeServiceName || !launch.url)) {
    throw new Error("compute_claim_continuation_launch_result_incomplete");
  }
  if (launch.status === "succeeded") launch.url = computeClaimWorkspaceUrl(launch.url, target.workspaceId);
  return launch;
}

function computeClaimContinuationRecovery(value) {
  const source = value?.recovery;
  const recovery = {
    approvalId: String(source?.approvalId || ""),
    approvalDigest: String(source?.approvalDigest || ""),
    recoveryKey: String(source?.recoveryKey || ""),
    workspaceImageDigest: String(source?.workspaceImageDigest || "")
  };
  if (!exactObjectKeys(source, ["approvalId", "approvalDigest", "recoveryKey", "workspaceImageDigest"]) ||
    !/^[a-z0-9][a-z0-9-]{2,47}$/.test(recovery.approvalId) || !/^[a-f0-9]{64}$/.test(recovery.approvalDigest) ||
    !/^[a-z0-9][a-z0-9-]{2,47}$/.test(recovery.recoveryKey) || !/^sha256:[a-f0-9]{64}$/.test(recovery.workspaceImageDigest)) {
    throw new Error("compute_claim_continuation_recovery_binding_invalid");
  }
  return recovery;
}

function computeClaimContinuationRuntime(value, target, launch) {
  const runtime = {
    workspaceId: String(value?.workspaceId || ""),
    runtimeId: String(value?.runtimeId || ""),
    serviceName: String(value?.serviceName || ""),
    status: String(value?.status || ""),
    ready: value?.ready === true,
    url: String(value?.url || "")
  };
  if (runtime.workspaceId !== target.workspaceId || !runtime.runtimeId || runtime.serviceName !== launch.runtimeServiceName ||
    runtime.status !== "running" || runtime.ready !== true || !runtime.url || runtime.url !== launch.url) {
    throw new Error("compute_claim_continuation_runtime_invalid");
  }
  return runtime;
}

function computeClaimContinuationReceipt(value, target, launch, runtime) {
  try {
    const receipt = canaryReceipt(value, {
      receiptId: launch.receiptId,
      workspaceId: target.workspaceId,
      computeAllocationId: target.computeAllocationId,
      storageId: target.storageId,
		attachmentId: launch.attachmentId,
		runtimeId: runtime.runtimeId,
		workspaceApiKeyId: launch.workspaceApiKeyId,
		totalUsdMicros: launch.totalChargeUsdMicros,
		storageSizeGb: launch.sizeGb
    });
    return { ...receipt, workspaceId: target.workspaceId };
  } catch {
    throw new Error("compute_claim_continuation_receipt_invalid");
  }
}

export async function continueComputeClaimWorkspace({
  target: rawTarget,
  mergedSha,
  cloudImageDigest,
  origin,
  customerEmail,
  customerPassword,
  kubeconfigPath,
  namespace,
  launchPollAttempts = 180,
  launchPollDelayMs = 10_000,
  requestTimeoutMs = DEFAULT_REQUEST_TIMEOUT_MS,
  cloudRevisionEvidenceReader,
  fetchImpl = globalThis.fetch,
  execFileImpl = defaultExecFile,
  signal,
  now = new Date()
} = {}) {
	const target = computeClaimTarget(rawTarget, new Set(["basic", "pro"]));
  if (!/^[a-f0-9]{40}$/.test(String(mergedSha || "")) || !/^sha256:[a-f0-9]{64}$/.test(String(cloudImageDigest || "")) ||
    !String(kubeconfigPath || "").startsWith("/") || !/^[a-z0-9][a-z0-9-]{0,62}$/.test(String(namespace || "")) ||
    !Number.isInteger(launchPollAttempts) || launchPollAttempts < 1 || launchPollAttempts > 1000 ||
    !Number.isFinite(launchPollDelayMs) || launchPollDelayMs < 0 || launchPollDelayMs > 300_000 ||
    !Number.isInteger(requestTimeoutMs) || requestTimeoutMs < 1 || requestTimeoutMs > 300_000) {
    throw new Error("compute_claim_continuation_config_invalid");
  }
  const credentials = computeClaimContinuationCredentials(customerEmail, customerPassword);
  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  const release = await currentComputeClaimCloudRevision({
    mergedSha,
    cloudImageDigest,
    kubeconfigPath,
    namespace,
    cloudRevisionEvidenceReader,
    execFileImpl
  });
  const requestOptions = { fetchImpl, origin: normalizedOrigin, signal, timeoutMs: requestTimeoutMs };
  const customerAuth = await login({ ...requestOptions, ...credentials });
  if (customerAuth.user?.accountId !== target.accountId || customerAuth.user?.role !== "owner" || !customerAuth.csrfToken) {
    throw new Error("compute_claim_continuation_customer_login_failed");
  }
  const identity = sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/auth/me" }), "sub2api").data;
  if (identity?.accountId !== target.accountId || String(identity?.email || "").trim().toLowerCase() !== credentials.email ||
    identity?.role !== "owner" || identity?.status !== "active") {
    throw new Error("compute_claim_continuation_customer_identity_mismatch");
  }

  const launchPath = `/api/workspace-launches/${encodeURIComponent(target.launchOperationId)}`;
  let launch;
  let recovery;
  for (let attempt = 1; attempt <= launchPollAttempts; attempt += 1) {
    if (attempt > 1 && launch?.status !== "succeeded") await sleep(launchPollDelayMs);
    const authority = await authoritativeGet({ ...requestOptions, auth: customerAuth, path: launchPath });
    if (!authority.found) throw new Error("compute_claim_continuation_launch_readback_failed");
    launch = computeClaimContinuationLaunch(authority.payload, target);
    recovery = computeClaimContinuationRecovery(authority.payload);
    if (launch.status === "succeeded" && launch.phase === "succeeded") break;
    if (attempt === launchPollAttempts) throw new Error("compute_claim_continuation_timeout");
  }
  if (launch.status !== "succeeded" || launch.phase !== "succeeded") throw new Error("compute_claim_continuation_timeout");
  const runtimeEnvelope = await requestJson({
    ...requestOptions,
    auth: customerAuth,
    path: `/api/workspaces/${encodeURIComponent(target.workspaceId)}/runtime-status`
  });
  const runtime = computeClaimContinuationRuntime(sourceEnvelope(runtimeEnvelope, "fabric").data, target, launch);
  const receipt = computeClaimContinuationReceipt(await requestJson({
    ...requestOptions,
    auth: customerAuth,
    path: `/api/billing/receipts/${encodeURIComponent(launch.receiptId)}`
  }), target, launch, runtime);
  return {
    schemaVersion: 2,
    operationMode: COMPUTE_CLAIM_CONTINUATION_MODE,
    status: "succeeded",
    recoveryEligible: true,
    errorCode: "none",
    release: computeClaimReleaseEvidence(release),
    target: { ...target },
    launch,
    runtime,
    receipt,
    recovery,
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    backgroundMutationCountsState: "unknown",
    verifiedAt: now.toISOString()
  };
}

function recoveredWorkspaceE2EApproval(value, expected, now) {
  let approval;
  try {
    approval = typeof value === "string" ? JSON.parse(value) : value;
  } catch {
    throw new Error("recovered_workspace_e2e_approval_invalid");
  }
  const keys = [
    "schemaVersion", "approvalId", "expiresAt", "confirmation", "mergedMainSha", "cloudImageDigest", "workspaceImageDigest",
    "recoveryApprovalId", "recoveryApprovalDigest", "recoveryKey", "customer", "launchOperationId", "workspaceId", "resources",
    "expectedModel", "modelRequestKey", "allowedWrites", "forbiddenWrites"
  ];
  const resourceKeys = [
    "computeAllocationId", "storageId", "attachmentId", "runtimeId", "receiptId", "workspaceApiKeyId", "runtimeServiceName", "workspaceUrl"
  ];
  const validOpaque = (item) => /^[a-z0-9][a-z0-9._:-]{2,127}$/.test(String(item || "")) &&
    !/(?:api-?key|bearer|credential|password|secret|token)/.test(String(item));
  const email = String(approval?.customer?.email || "").trim().toLowerCase();
  if (!exactObjectKeys(approval, keys) || approval.schemaVersion !== 1 || approval.approvalId !== expected.approvalId ||
    !validOpaque(approval.approvalId) || !validOpaque(approval.recoveryApprovalId) || !validOpaque(approval.recoveryKey) ||
    !validOpaque(approval.modelRequestKey) || !Number.isFinite(Date.parse(approval.expiresAt)) || Date.parse(approval.expiresAt) <= now.getTime() ||
    approval.confirmation !== RECOVERED_WORKSPACE_E2E_CONFIRMATION || approval.confirmation !== expected.confirmation ||
    !/^[a-f0-9]{40}$/.test(String(expected.mergedSha || "")) || approval.mergedMainSha !== expected.mergedSha ||
    !/^sha256:[a-f0-9]{64}$/.test(String(approval.cloudImageDigest || "")) ||
    !/^sha256:[a-f0-9]{64}$/.test(String(approval.workspaceImageDigest || "")) || !/^[a-f0-9]{64}$/.test(String(approval.recoveryApprovalDigest || "")) ||
    !exactObjectKeys(approval.customer, ["email", "accountId"]) || approval.customer.email !== email ||
    !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email) || approval.customer.email !== expected.customerEmail ||
    !exactObjectKeys(approval.resources, resourceKeys) || !/^[1-9][0-9]*$/.test(String(approval.resources.workspaceApiKeyId || "")) ||
    !validOpaque(approval.expectedModel) || JSON.stringify(approval.allowedWrites) !== JSON.stringify(RECOVERED_WORKSPACE_E2E_ALLOWED_WRITES) ||
    JSON.stringify(approval.forbiddenWrites) !== JSON.stringify(RECOVERED_WORKSPACE_E2E_FORBIDDEN_WRITES)) {
    throw new Error("recovered_workspace_e2e_approval_invalid");
  }
  return { ...approval, customer: { ...approval.customer, email } };
}

function recoveredWorkspaceE2EContinuation(value, approval) {
  const target = value?.target;
  const launch = value?.launch;
  const runtime = value?.runtime;
  const receipt = value?.receipt;
  const recovery = value?.recovery;
  const expectedResources = {
    computeAllocationId: target?.computeAllocationId,
    storageId: target?.storageId,
    attachmentId: launch?.attachmentId,
    runtimeId: runtime?.runtimeId,
    receiptId: launch?.receiptId,
    workspaceApiKeyId: String(launch?.workspaceApiKeyId || ""),
    runtimeServiceName: runtime?.serviceName,
    workspaceUrl: runtime?.url
  };
  if (value?.schemaVersion !== 2 || value?.operationMode !== COMPUTE_CLAIM_CONTINUATION_MODE || value?.status !== "succeeded" ||
    value?.recoveryEligible !== true || value?.errorCode !== "none" || value?.release?.mergedSha !== approval.mergedMainSha ||
    value?.release?.cloudImageDigest !== approval.cloudImageDigest || launch?.status !== "succeeded" || launch?.phase !== "succeeded" ||
    target?.accountId !== approval.customer.accountId || target?.workspaceId !== approval.workspaceId || target?.launchOperationId !== approval.launchOperationId ||
    runtime?.workspaceId !== approval.workspaceId || runtime?.status !== "running" || runtime?.ready !== true || receipt?.workspaceId !== approval.workspaceId ||
    recovery?.approvalId !== approval.recoveryApprovalId || recovery?.approvalDigest !== approval.recoveryApprovalDigest ||
    recovery?.recoveryKey !== approval.recoveryKey || recovery?.workspaceImageDigest !== approval.workspaceImageDigest ||
    JSON.stringify(expectedResources) !== JSON.stringify(approval.resources) ||
    value?.runnerDirectMutationCounts?.sub2api !== 0 || value?.runnerDirectMutationCounts?.tencent !== 0 ||
    value?.runnerDirectMutationCounts?.kubernetes !== 0 || value?.backgroundMutationCountsState !== "unknown") {
    throw new Error("recovered_workspace_e2e_resource_closure_required");
  }
  return { target, launch, runtime, receipt, recovery };
}

function recoveredWorkspaceE2EKey(snapshot, approval) {
  const ids = snapshot.items.map((key) => key.id);
  const matches = snapshot.items.filter((key) => key.id === approval.resources.workspaceApiKeyId);
  const key = matches[0];
  if (new Set(ids).size !== ids.length || matches.length !== 1 || key.kind !== "workspace" || key.status !== "active" ||
    key.name !== canonicalWorkspaceKeyName(approval.workspaceId)) {
    throw new Error("recovered_workspace_e2e_workspace_key_invalid");
  }
  return key;
}

function recoveredWorkspaceE2EAttemptBody(approval) {
  return {
    approval,
    approvalDigest: createHash("sha256").update(canonicalJson(approval)).digest("hex")
  };
}

function recoveredWorkspaceE2EAttemptResponse(result, expectedStatus, expectedDigest) {
  if (result.response.headers.get("cache-control") !== "private, no-store" || result.payload?.status !== expectedStatus ||
    result.payload?.approvalDigest !== expectedDigest || !/^[a-z0-9][a-z0-9._:-]{2,127}$/.test(String(result.payload?.attemptId || ""))) {
    throw new Error("recovered_workspace_e2e_attempt_response_invalid");
  }
  return { attemptId: result.payload.attemptId, status: result.payload.status };
}

export async function verifyRecoveredWorkspaceE2E({
  origin,
  mergedSha,
  customerEmail,
  customerPassword,
  approvalJson,
  approvalId,
  confirmation,
  continuationEvidence,
  usageAttempts = DEFAULT_USAGE_ATTEMPTS,
  usageRetryDelayMs = DEFAULT_USAGE_RETRY_DELAY_MS,
  browserTimeoutMs = DEFAULT_BROWSER_TIMEOUT_MS,
  modelTimeoutMs = DEFAULT_MODEL_TIMEOUT_MS,
  requestTimeoutMs = DEFAULT_REQUEST_TIMEOUT_MS,
  fetchImpl = globalThis.fetch,
  browserFactory,
  signal,
  now = new Date()
} = {}) {
  const normalizedEmail = String(customerEmail || "").trim().toLowerCase();
  const approval = recoveredWorkspaceE2EApproval(approvalJson, { approvalId, confirmation, customerEmail: normalizedEmail, mergedSha }, now);
  const continuation = recoveredWorkspaceE2EContinuation(continuationEvidence, approval);
  if (!String(customerPassword || "") || !Number.isInteger(usageAttempts) || usageAttempts < 1 || usageAttempts > 1000 ||
    !Number.isFinite(usageRetryDelayMs) || usageRetryDelayMs < 0 || usageRetryDelayMs > 300_000 ||
    !Number.isInteger(browserTimeoutMs) || browserTimeoutMs < 1 || browserTimeoutMs > 300_000 ||
    !Number.isInteger(modelTimeoutMs) || modelTimeoutMs < 1 || modelTimeoutMs > 300_000 ||
    !Number.isInteger(requestTimeoutMs) || requestTimeoutMs < 1 || requestTimeoutMs > 300_000) {
    throw new Error("recovered_workspace_e2e_config_invalid");
  }
  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  const requestOptions = { fetchImpl, origin: normalizedOrigin, signal, timeoutMs: requestTimeoutMs };
  const auth = await login({ ...requestOptions, email: normalizedEmail, password: String(customerPassword) });
  if (auth.user?.accountId !== approval.customer.accountId || auth.user?.role !== "owner" || !auth.csrfToken) {
    throw new Error("recovered_workspace_e2e_customer_login_failed");
  }
  const identity = sourceEnvelope(await requestJson({ ...requestOptions, auth, path: "/api/auth/me" }), "sub2api").data;
  if (identity?.accountId !== approval.customer.accountId || String(identity?.email || "").trim().toLowerCase() !== normalizedEmail ||
    identity?.role !== "owner" || identity?.status !== "active" || !/^[1-9][0-9]*$/.test(String(identity?.sub2apiUserId || ""))) {
    throw new Error("recovered_workspace_e2e_customer_identity_mismatch");
  }

  const runtime = sourceEnvelope(await requestJson({
    ...requestOptions,
    auth,
    path: `/api/workspaces/${encodeURIComponent(approval.workspaceId)}/runtime-status`
  }), "fabric").data;
  if (runtime?.workspaceId !== approval.workspaceId || runtime?.runtimeId !== approval.resources.runtimeId ||
    runtime?.serviceName !== approval.resources.runtimeServiceName || runtime?.status !== "running" || runtime?.ready !== true ||
    runtime?.url !== approval.resources.workspaceUrl || runtime?.access?.credentialStatus !== "configured" || !runtime?.access?.username ||
    Object.hasOwn(runtime?.access || {}, "password") || Object.hasOwn(runtime?.access || {}, "secretRef")) {
    throw new Error("recovered_workspace_e2e_runtime_invalid");
  }
  const revealed = await requestJson({
    ...requestOptions,
    auth,
    path: `/api/workspaces/${encodeURIComponent(approval.workspaceId)}/runtime-credentials/reveal`,
    method: "POST",
    body: {}
  });
  if (revealed.response.headers.get("cache-control") !== "private, no-store" || revealed.payload?.workspaceId !== approval.workspaceId ||
    revealed.payload?.access?.credentialStatus !== "configured" || revealed.payload?.access?.username !== runtime.access.username ||
    !revealed.payload?.access?.password) {
    throw new Error("recovered_workspace_e2e_runtime_credentials_invalid");
  }

  const keySnapshot = await readGatewayCanaryKeySnapshot(requestOptions, auth);
  const key = recoveredWorkspaceE2EKey(keySnapshot, approval);
  const walletBefore = walletFact(sourceEnvelope(await requestJson({ ...requestOptions, auth, path: "/api/gateway/wallet" }), "sub2api"), identity.sub2apiUserId);
  const usageBefore = await gatewayUsageSnapshot(requestOptions, auth, key.id);
  const statsBefore = await gatewayUsageStats(requestOptions, auth, key.id);
  const attemptBody = recoveredWorkspaceE2EAttemptBody(approval);
  const attemptPath = `/api/workspaces/${encodeURIComponent(approval.workspaceId)}/recovered-e2e-attempt`;
  let reserved;

  const workspace = await verifyWorkspaceBrowserQa({
    url: runtime.url,
    username: revealed.payload.access.username,
    password: revealed.payload.access.password,
    runId: approval.modelRequestKey,
    browserTimeoutMs,
    modelTimeoutMs,
    browserFactory,
    beforeModelRequest: async () => {
      reserved = recoveredWorkspaceE2EAttemptResponse(await requestJson({
        ...requestOptions,
        auth,
        path: attemptPath,
        method: "POST",
        body: attemptBody
      }), "attempted", attemptBody.approvalDigest);
    }
  });
  if (!reserved) throw new Error("recovered_workspace_e2e_attempt_response_invalid");
  let usageAfter;
  let requestUsage;
  let statsAfter;
  let walletAfter;
  let usageReadAttempts = 0;
  let statsMismatch = false;
  let balanceMismatch = false;
  for (let attempt = 1; attempt <= usageAttempts; attempt += 1) {
    usageReadAttempts = attempt;
    recoveredWorkspaceE2EKey(await readGatewayCanaryKeySnapshot(requestOptions, auth), approval);
    usageAfter = await gatewayUsageSnapshot(requestOptions, auth, key.id);
    requestUsage = exactUsageRecord(usageBefore, usageAfter, approval.expectedModel, key.id);
    if (requestUsage) {
      statsAfter = await gatewayUsageStats(requestOptions, auth, key.id);
      walletAfter = walletFact(sourceEnvelope(await requestJson({ ...requestOptions, auth, path: "/api/gateway/wallet" }), "sub2api"), identity.sub2apiUserId);
      const statsMatch = statsMatchRequest(statsBefore, statsAfter, requestUsage);
      const balanceMatch = walletDebitMatches(walletBefore, walletAfter, requestUsage.actualCostUsdMicros);
      if (statsMatch && balanceMatch) break;
      statsMismatch ||= !statsMatch;
      balanceMismatch ||= !balanceMatch;
    }
    if (attempt < usageAttempts) await sleep(usageRetryDelayMs);
  }
  if (!requestUsage) throw new Error("exact_gateway_request_not_found");
  if (!statsAfter || !statsMatchRequest(statsBefore, statsAfter, requestUsage)) {
    throw new Error(statsMismatch ? "gateway_usage_stats_mismatch" : "gateway_usage_stats_invalid");
  }
  if (!walletAfter || !walletDebitMatches(walletBefore, walletAfter, requestUsage.actualCostUsdMicros)) {
    throw new Error(balanceMismatch ? "gateway_balance_delta_mismatch" : "gateway_wallet_invalid");
  }
  const completed = recoveredWorkspaceE2EAttemptResponse(await requestJson({
    ...requestOptions,
    auth,
    path: `${attemptPath}/complete`,
    method: "POST",
    body: attemptBody
  }), "passed", attemptBody.approvalDigest);

  return {
    schemaVersion: 1,
    operationMode: "recovered_workspace_e2e",
    ok: true,
    status: "passed",
    approval: { approvalId: approval.approvalId, approvalDigest: attemptBody.approvalDigest },
    release: { mergedSha: approval.mergedMainSha, cloudImageDigest: approval.cloudImageDigest, workspaceImageDigest: approval.workspaceImageDigest },
    customer: { accountId: approval.customer.accountId },
    launch: { operationId: approval.launchOperationId, workspaceId: approval.workspaceId },
    resources: { ...approval.resources },
    marker: { attemptId: reserved.attemptId, reserved: reserved.status, completed: completed.status },
    workspace,
    balance: { before: walletBefore, after: walletAfter },
    usage: { request: requestUsage, stats: { before: statsBefore, after: statsAfter, delta: statsDelta(statsBefore, statsAfter) }, readAttempts: usageReadAttempts },
    continuation: { verifiedAt: continuationEvidence.verifiedAt, receiptId: continuation.receipt.receiptId },
    writeCounts: {
      controlPlaneE2EAttemptReservations: 1,
      modelRequests: 1,
      controlPlaneE2EAttemptCompletions: 1,
      workspaceLaunches: 0,
      workspacePurchaseDebits: 0,
      walletAdjustments: 0,
      tencentMutations: 0,
      kubernetesMutations: 0
    },
    verifiedAt: now.toISOString()
  };
}

async function defaultExecFile(command, args, options) {
  const { execFile } = await import("node:child_process");
  return new Promise((resolve, reject) => {
    execFile(command, args, options, (error, stdout, stderr) => {
      if (error) {
        reject(error);
        return;
      }
      resolve({ stdout, stderr });
    });
  });
}

function parseExecJson(stdout, error) {
  try {
    return JSON.parse(stdout);
  } catch {
    throw new Error(error);
  }
}

function immutableDigestFromImageId(value) {
  const match = String(value || "").match(/(?:@|^[a-z-]+:\/\/)(sha256:[a-f0-9]{64})$/);
  return match?.[1] || "";
}

function validateBasicCanaryCloudRevisionEvidence(evidence, expectedMergedSha, expectedCloudDigest) {
  const services = evidence?.services;
  if (evidence?.mergedSha !== expectedMergedSha || evidence?.cloudDigest !== expectedCloudDigest ||
    !exactObjectKeys(services, ["controlPlane", "fabric", "ledger"])) {
    throw new Error("production_basic_canary_cloud_revision_invalid");
  }
  for (const service of Object.values(services)) {
    if (!/^[1-9][0-9]*$/.test(String(service?.revision || "")) || immutableDigestFromImageId(service?.imageID) !== expectedCloudDigest) {
      throw new Error("production_basic_canary_cloud_revision_invalid");
    }
  }
  return evidence;
}

export async function readBasicCanaryCloudRevisionEvidence({
  expectedMergedSha,
  expectedCloudDigest,
  kubeconfigPath,
  namespace,
  execFileImpl = defaultExecFile
}) {
  if (!/^[a-f0-9]{40}$/.test(String(expectedMergedSha || "")) || !/^sha256:[a-f0-9]{64}$/.test(String(expectedCloudDigest || "")) ||
    !String(kubeconfigPath || "").startsWith("/") || !/^[a-z0-9][a-z0-9-]{0,62}$/.test(String(namespace || ""))) {
    throw new Error("production_basic_canary_cloud_revision_config_invalid");
  }
  const options = { encoding: "utf8", maxBuffer: 8 * 1024 * 1024 };
  const [head, originMain, remoteMain, configMapResult, deploymentsResult, replicaSetsResult, podsResult] = await Promise.all([
    execFileImpl("git", ["rev-parse", "HEAD"], options),
    execFileImpl("git", ["rev-parse", "refs/remotes/origin/main"], options),
    execFileImpl("git", ["ls-remote", "--exit-code", "origin", "refs/heads/main"], options),
    execFileImpl("kubectl", ["--kubeconfig", String(kubeconfigPath), "-n", String(namespace), "get", "configmap", "opl-cloud-config", "-o", "json"], options),
    execFileImpl("kubectl", ["--kubeconfig", String(kubeconfigPath), "-n", String(namespace), "get", "deployments", "opl-cloud-control-plane", "opl-cloud-fabric", "opl-cloud-ledger", "-o", "json"], options),
    execFileImpl("kubectl", ["--kubeconfig", String(kubeconfigPath), "-n", String(namespace), "get", "replicasets", "-l", "app.kubernetes.io/name=opl-cloud", "-o", "json"], options),
    execFileImpl("kubectl", ["--kubeconfig", String(kubeconfigPath), "-n", String(namespace), "get", "pods", "-l", "app.kubernetes.io/name=opl-cloud", "-o", "json"], options)
  ]);
  const remoteMainFields = String(remoteMain.stdout || "").trim().split(/\s+/);
  if (String(head.stdout || "").trim() !== expectedMergedSha || String(originMain.stdout || "").trim() !== expectedMergedSha ||
    remoteMainFields.length !== 2 || remoteMainFields[0] !== expectedMergedSha || remoteMainFields[1] !== "refs/heads/main") {
    throw new Error("production_basic_canary_cloud_revision_invalid");
  }
  const configMap = parseExecJson(configMapResult.stdout, "production_basic_canary_cloud_revision_invalid");
  const deploymentsDocument = parseExecJson(deploymentsResult.stdout, "production_basic_canary_cloud_revision_invalid");
  const replicaSetsDocument = parseExecJson(replicaSetsResult.stdout, "production_basic_canary_cloud_revision_invalid");
  const podsDocument = parseExecJson(podsResult.stdout, "production_basic_canary_cloud_revision_invalid");
  const expectedImage = String(configMap?.data?.OPL_CLOUD_IMAGE || "");
  if (configMap?.metadata?.name !== "opl-cloud-config" || !expectedImage.endsWith(`@${expectedCloudDigest}`) ||
    deploymentsDocument?.kind !== "List" || replicaSetsDocument?.kind !== "List" || podsDocument?.kind !== "List" ||
    !Array.isArray(deploymentsDocument.items) || !Array.isArray(replicaSetsDocument.items) || !Array.isArray(podsDocument.items)) {
    throw new Error("production_basic_canary_cloud_revision_invalid");
  }

  const revisionKey = "deployment.kubernetes.io/revision";
  const definitions = [
    ["controlPlane", "opl-cloud-control-plane", "control-plane"],
    ["fabric", "opl-cloud-fabric", "fabric"],
    ["ledger", "opl-cloud-ledger", "ledger"]
  ];
  const containerImage = (item, name) => (item?.spec?.template?.spec?.containers || []).find((container) => container?.name === name)?.image;
  const ownedBy = (item, kind, name, uid) => Boolean(name && uid) && (item?.metadata?.ownerReferences || []).some((owner) =>
    owner?.controller === true && owner?.kind === kind && owner?.name === name && owner?.uid === uid);
  const services = {};
  for (const [key, deploymentName, containerName] of definitions) {
    const deploymentMatches = deploymentsDocument.items.filter((item) => item?.metadata?.name === deploymentName);
    const deployment = deploymentMatches[0];
    const deploymentUid = String(deployment?.metadata?.uid || "");
    const revision = String(deployment?.metadata?.annotations?.[revisionKey] || "");
    const desired = Number(deployment?.spec?.replicas ?? 1);
    const status = deployment?.status || {};
    if (deploymentMatches.length !== 1 || !deploymentUid || !/^[1-9][0-9]*$/.test(revision) || !Number.isInteger(desired) || desired < 1 ||
      containerImage(deployment, containerName) !== expectedImage || Number(status.observedGeneration || 0) < Number(deployment?.metadata?.generation || 0) ||
      Number(status.updatedReplicas || 0) !== desired || Number(status.readyReplicas || 0) !== desired ||
      Number(status.availableReplicas || 0) !== desired || Number(status.unavailableReplicas || 0) !== 0) {
      throw new Error("production_basic_canary_cloud_revision_invalid");
    }
    const currentReplicaSets = replicaSetsDocument.items.filter((replicaSet) =>
      ownedBy(replicaSet, "Deployment", deploymentName, deploymentUid) &&
      String(replicaSet?.metadata?.annotations?.[revisionKey] || "") === revision &&
      containerImage(replicaSet, containerName) === expectedImage);
    const replicaSet = currentReplicaSets[0];
    const replicaSetName = String(replicaSet?.metadata?.name || "");
    const replicaSetUid = String(replicaSet?.metadata?.uid || "");
    if (currentReplicaSets.length !== 1 || !replicaSetName || !replicaSetUid) {
      throw new Error("production_basic_canary_cloud_revision_invalid");
    }
    const currentPods = podsDocument.items.filter((pod) => !pod?.metadata?.deletionTimestamp && ownedBy(pod, "ReplicaSet", replicaSetName, replicaSetUid));
    if (currentPods.length !== desired) throw new Error("production_basic_canary_cloud_revision_invalid");
    let imageID = "";
    for (const pod of currentPods) {
      const ready = (pod?.status?.conditions || []).some((condition) => condition?.type === "Ready" && condition?.status === "True");
      const containerStatus = (pod?.status?.containerStatuses || []).find((item) => item?.name === containerName);
      if (pod?.status?.phase !== "Running" || !ready || containerStatus?.ready !== true || immutableDigestFromImageId(containerStatus?.imageID || containerStatus?.imageId) !== expectedCloudDigest) {
        throw new Error("production_basic_canary_cloud_revision_invalid");
      }
      imageID = String(containerStatus.imageID || containerStatus.imageId);
    }
    services[key] = {
      deployment: deploymentName,
      deploymentUid,
      revision,
      replicaSet: replicaSetName,
      replicaSetUid,
      pod: String(currentPods[0]?.metadata?.name || ""),
      podUid: String(currentPods[0]?.metadata?.uid || ""),
      imageID
    };
  }
  return validateBasicCanaryCloudRevisionEvidence({ mergedSha: expectedMergedSha, cloudDigest: expectedCloudDigest, cloudImage: expectedImage, services }, expectedMergedSha, expectedCloudDigest);
}

export async function readBasicCanaryRuntimePodEvidence({
  workspaceId,
  expectedDigest,
  kubeconfigPath,
  namespace,
  execFileImpl = defaultExecFile
}) {
  if (!/^[A-Za-z0-9][A-Za-z0-9-]{1,99}$/.test(String(workspaceId || "")) || !/^sha256:[a-f0-9]{64}$/.test(String(expectedDigest || "")) ||
    !String(kubeconfigPath || "").startsWith("/") || !/^[a-z0-9][a-z0-9-]{0,62}$/.test(String(namespace || ""))) {
    throw new Error("production_basic_canary_runtime_pod_config_invalid");
  }
  const args = [
    "--kubeconfig", String(kubeconfigPath),
    "-n", String(namespace),
    "get", "pods",
    "-l", `oplcloud.cn/workspace-id=${workspaceId}`,
    "-o", "json"
  ];
  const { stdout } = await execFileImpl("kubectl", args, { encoding: "utf8", maxBuffer: 4 * 1024 * 1024 });
  let list;
  try {
    list = JSON.parse(stdout);
  } catch {
    throw new Error("production_basic_canary_runtime_pod_invalid");
  }
  if (list?.kind !== "List" || !Array.isArray(list.items)) throw new Error("production_basic_canary_runtime_pod_invalid");
  const matches = list.items.flatMap((pod) => {
    const labels = pod?.metadata?.labels;
    const ready = (pod?.status?.conditions || []).some((condition) => condition?.type === "Ready" && condition?.status === "True");
    const owners = (pod?.metadata?.ownerReferences || []).filter((owner) => owner?.controller === true && owner?.kind === "ReplicaSet" && owner?.name && owner?.uid);
    const nodeName = String(pod?.spec?.nodeName || "");
    const containers = (pod?.status?.containerStatuses || []).filter((container) => container?.name === "workspace" && container?.ready === true &&
      String(container?.imageID || "").endsWith(expectedDigest));
    const specs = (pod?.spec?.containers || []).filter((container) => container?.name === "workspace" &&
      String(container?.resources?.limits?.cpu || "") === "2" && String(container?.resources?.limits?.memory || "") === "4Gi");
    if (pod?.metadata?.deletionTimestamp || labels?.["oplcloud.cn/workspace-id"] !== workspaceId || pod?.status?.phase !== "Running" ||
      !ready || owners.length !== 1 || containers.length !== 1 || specs.length !== 1 || !pod?.metadata?.name || !nodeName) {
      return [];
    }
    return [{
      podName: pod.metadata.name,
      nodeName,
      containerName: "workspace",
      ready: true,
      imageID: containers[0].imageID,
      resources: { cpu: 2, memoryGb: 4 },
      ownerReference: { kind: "ReplicaSet", name: owners[0].name, uid: owners[0].uid }
    }];
  });
  if (matches.length !== 1) throw new Error("production_basic_canary_runtime_pod_invalid");
  return matches[0];
}

function basicCanaryResourceContract(catalog) {
  const basics = Array.isArray(catalog?.workspacePackages) ? catalog.workspacePackages.filter((item) => item?.id === "basic") : [];
  const basic = basics[0];
  if (basics.length !== 1 || basic?.cpu !== 2 || basic?.memoryGb !== 4 || basic?.diskGb !== 10 || basic?.provider !== "tencent-tke" || basic?.available !== true) {
    throw new Error("production_basic_canary_resource_contract_invalid");
  }
  return { cpu: 2, memoryGb: 4 };
}

function controlPlaneCanaryPage(result, pageSize = 20) {
  const envelope = sourceEnvelope(result, "control-plane", true);
  const data = envelope.data;
  if (!Array.isArray(data?.items) || !Number.isSafeInteger(data?.total) || data.total < 0 || data.page !== 1 || data.pageSize !== pageSize ||
    Object.hasOwn(data, "pages") || data.items.length !== Math.min(data.total, pageSize)) {
    throw new Error("production_basic_canary_page_invalid");
  }
  return data;
}

function gatewayCanaryPage(result, pageSize = 20, expectedPage = 1) {
  const envelope = sourceEnvelope(result, "sub2api", true);
  const data = envelope.data;
  const expectedItems = data?.total === 0 ? 0 : Math.min(pageSize, Math.max(0, data?.total - (expectedPage - 1) * pageSize));
  if (!Array.isArray(data?.items) || !Number.isSafeInteger(data?.total) || data.total < 0 || data.page !== expectedPage || data.pageSize !== pageSize ||
    !Number.isSafeInteger(data.pages) || data.pages !== Math.max(1, Math.ceil(data.total / pageSize)) || data.items.length !== expectedItems) {
    throw new Error("production_basic_canary_page_invalid");
  }
  return data;
}

function canaryKeySummary(item) {
  if (!item || typeof item !== "object" || ["key", "value", "maskedValue"].some((field) => Object.hasOwn(item, field))) {
    throw new Error("production_basic_canary_key_invalid");
  }
  const id = String(item.id || "");
  const kind = String(item.kind || "");
  const name = String(item.name || "");
  const status = String(item.status || "");
  if (!/^[1-9][0-9]*$/.test(id) || !["general", "workspace"].includes(kind) || !name || !status) {
    throw new Error("production_basic_canary_key_invalid");
  }
  return { id, kind, name, status };
}

function sortedCanaryKeys(items) {
  return items.map(canaryKeySummary).sort((left, right) => left.id.localeCompare(right.id));
}

function sameCanaryKeyCollection(left, right) {
  return canonicalJson(sortedCanaryKeys(left || [])) === canonicalJson(sortedCanaryKeys(right || []));
}

async function readGatewayCanaryKeySnapshot(requestOptions, customerAuth) {
  const pageSize = 20;
  const items = [];
  let total = null;
  let pages = null;
  for (let page = 1; ; page += 1) {
    if (page > MAX_CANARY_KEY_ITEMS) throw new Error("production_basic_canary_key_page_limit_exceeded");
    const data = gatewayCanaryPage(await requestJson({
      ...requestOptions,
      auth: customerAuth,
      path: `/api/gateway/keys?page=${page}&pageSize=${pageSize}`
    }), pageSize, page);
    if (total === null) {
      total = data.total;
      pages = data.pages;
      if (total > MAX_CANARY_KEY_ITEMS) throw new Error("production_basic_canary_key_page_limit_exceeded");
    } else if (data.total !== total || data.pages !== pages) {
      throw new Error("production_basic_canary_key_pagination_changed");
    }
    items.push(...data.items.map(canaryKeySummary));
    if (page >= pages) break;
  }
  if (items.length !== total) throw new Error("production_basic_canary_key_pagination_incomplete");
  const generalKeys = items.filter((item) => item.kind === "general");
  const workspaceKeys = items.filter((item) => item.kind === "workspace");
  return {
    total,
    pages,
    items: sortedCanaryKeys(items),
    generalKeys: sortedCanaryKeys(generalKeys),
    workspaceKeys: sortedCanaryKeys(workspaceKeys)
  };
}

function canonicalWorkspaceKeyName(workspaceId) {
  return `opl-workspace-${stableCanaryId(workspaceId).slice(0, 12)}`;
}

function basicCanaryKeyEvidence(snapshot, baseline, workspaceId, workspaceApiKeyId) {
  const canonicalName = canonicalWorkspaceKeyName(workspaceId);
  const baselineWorkspaceKeys = baseline?.workspaceKeys || [];
  const expectedExistingWorkspace = baselineWorkspaceKeys.length === 1 &&
    baselineWorkspaceKeys[0]?.id === String(workspaceApiKeyId) && baselineWorkspaceKeys[0]?.kind === "workspace" &&
    baselineWorkspaceKeys[0]?.name === canonicalName && baselineWorkspaceKeys[0]?.status === "active";
  if (baselineWorkspaceKeys.length > 1 || baselineWorkspaceKeys.length === 1 && !expectedExistingWorkspace) {
    throw new Error("production_basic_canary_baseline_not_empty");
  }
  if (snapshot.workspaceKeys.length !== 1) throw new Error("production_basic_canary_workspace_or_key_invalid");
  const workspaceKey = snapshot.workspaceKeys[0];
  if (workspaceKey.id !== String(workspaceApiKeyId) || workspaceKey.kind !== "workspace" || workspaceKey.name !== canonicalName || workspaceKey.status !== "active" ||
    !sameCanaryKeyCollection(snapshot.generalKeys, baseline.generalKeys)) {
    throw new Error("production_basic_canary_workspace_or_key_invalid");
  }
  return {
    generalKeysUnchanged: true,
    generalKeys: baseline.generalKeys,
    generalKeyIds: baseline.generalKeys.map((key) => key.id),
    workspaceKey,
    workspaceKeysCreated: 1
  };
}

function basicCanaryQuote(value) {
  const totalChargeUsdMicros = value?.totalChargeUsdMicros;
  const storage = value?.storage;
  const priceVersion = String(value?.priceVersion || "");
  if (value?.resourceType !== "workspace" || value?.packageId !== "basic" || value?.currency !== "USD" ||
    !priceVersion || storage?.resourceType !== "storage" || storage?.packageId !== "basic" ||
    storage?.priceSnapshot?.sizeGb !== 10 || !Number.isSafeInteger(totalChargeUsdMicros) || totalChargeUsdMicros <= 0) {
    throw new Error("production_basic_canary_quote_invalid");
  }
  return { totalChargeUsdMicros, priceVersion, currency: "USD" };
}

function canaryReceipt(result, expected) {
  const receipt = sourceEnvelope(result, "ledger").data;
  const compute = receipt?.components?.compute;
  const storage = receipt?.components?.storage;
  const fulfillment = receipt?.fulfillment;
  const receiptAmounts = [receipt?.totalUsdMicros, compute?.chargeUsdMicros, storage?.chargeUsdMicros];
  const receiptAmountsAreSafe = receiptAmounts.every((amount) => Number.isSafeInteger(amount) && amount > 0);
  if (receipt?.receiptId !== expected.receiptId || receipt?.type !== "billing.workspace_purchased.v1" || receipt?.status !== "completed" ||
    receipt?.workspaceId !== expected.workspaceId || !receiptAmountsAreSafe || receipt?.totalUsdMicros !== expected.totalUsdMicros ||
    BigInt(compute.chargeUsdMicros) + BigInt(storage.chargeUsdMicros) !== BigInt(receipt.totalUsdMicros) ||
    compute?.resourceType !== "compute" || compute?.resourceId !== expected.computeAllocationId ||
		storage?.resourceType !== "storage" || storage?.resourceId !== expected.storageId || storage?.sizeGb !== expected.storageSizeGb ||
    fulfillment?.computeAllocationId !== expected.computeAllocationId || fulfillment?.storageId !== expected.storageId ||
    fulfillment?.attachmentId !== expected.attachmentId || fulfillment?.runtimeId !== expected.runtimeId || fulfillment?.workspaceApiKeyId !== expected.workspaceApiKeyId) {
    throw new Error("production_basic_canary_receipt_invalid");
  }
  return {
    receiptId: receipt.receiptId,
    type: receipt.type,
    status: receipt.status,
    totalUsdMicros: receipt.totalUsdMicros,
    components: {
      compute: { resourceId: compute.resourceId, chargeUsdMicros: compute.chargeUsdMicros },
      storage: { resourceId: storage.resourceId, sizeGb: storage.sizeGb, chargeUsdMicros: storage.chargeUsdMicros }
    },
    fulfillment: {
      computeAllocationId: fulfillment.computeAllocationId,
      storageId: fulfillment.storageId,
      attachmentId: fulfillment.attachmentId,
      runtimeId: fulfillment.runtimeId,
      workspaceApiKeyId: fulfillment.workspaceApiKeyId
    }
  };
}

function operatorResource(detail, resourceType) {
  const matches = (detail?.resources || []).filter((item) => readOnlyNestedSource(item?.resourceType, "fabric") === resourceType);
  if (matches.length !== 1) throw new Error(`production_basic_canary_${resourceType}_facts_invalid`);
  const item = matches[0];
  return {
    type: resourceType,
    packageOrSpec: readOnlyNestedSource(item.packageOrSpec, "fabric"),
    providerId: readOnlyNestedSource(item.providerId, "fabric"),
    zone: readOnlyNestedSource(item.zone, "fabric"),
    status: readOnlyNestedSource(item.status, "fabric"),
    expiresAt: readOnlyNestedSource(item.expiresAt, "fabric")
  };
}

function validateFabricCanaryEvidence({ operations, allocation, ownership, truth, launch, approval }) {
  if (!Array.isArray(operations)) throw new Error("production_basic_canary_fabric_operations_invalid");
  const workspaceOperations = operations.filter((operation) => operation?.workspaceId === launch.workspaceId);
  const actionCount = (action) => workspaceOperations.filter((operation) => operation?.action === action && operation?.status === "succeeded").length;
  for (const action of ["create_compute_allocation", "create_storage_volume", "create_storage_attachment", "upsert_gateway_secret", "create_workspace_runtime"]) {
    if (actionCount(action) !== 1) throw new Error(`production_basic_canary_fabric_operation_cardinality:${action}`);
  }
  const computeOperation = workspaceOperations.find((operation) => operation.action === "create_compute_allocation" && operation.resourceId === launch.computeAllocationId);
  const plan = computeOperation?.redactedProviderPayload?.allocationPlan;
  const resolvedInstanceType = approval.expected.resolvedInstanceType;
  if (!plan || plan.packageId !== "basic" || plan.poolId !== "pool-basic-2c4g" || plan.nodePoolId !== approval.expected.nodePoolId ||
    plan.instanceType !== resolvedInstanceType || !Number.isSafeInteger(plan.baselineReplicas) || plan.baselineReplicas < 0 ||
    plan.targetReplicas !== plan.baselineReplicas + 1 || !Array.isArray(plan.beforeMachineNames) || plan.beforeMachineNames.length !== plan.baselineReplicas ||
    new Set(plan.beforeMachineNames).size !== plan.beforeMachineNames.length) {
    throw new Error("production_basic_canary_compute_plan_invalid");
  }
  const instanceId = String(allocation?.cvmInstanceId || allocation?.instanceId || "");
  if (allocation?.id !== launch.computeAllocationId || allocation?.accountId !== launch.accountId || allocation?.workspaceId !== launch.workspaceId ||
    allocation?.packageId !== "basic" || allocation?.nodePoolId !== approval.expected.nodePoolId || allocation?.instanceType !== resolvedInstanceType ||
    allocation?.providerData?.instanceType !== resolvedInstanceType || String(allocation?.providerData?.cpu || "") !== "2" || String(allocation?.providerData?.memoryGb || "") !== "4" ||
    allocation?.status !== "running" || allocation?.cvmStatus !== "RUNNING" || !/^ins-/.test(instanceId) || !allocation?.machineName ||
    plan.beforeMachineNames.includes(allocation.machineName) || allocation?.chargeType !== "PREPAID" || allocation?.renewFlag !== "NOTIFY_AND_MANUAL_RENEW" || !allocation?.deadline) {
    throw new Error("production_basic_canary_compute_allocation_invalid");
  }
  if (ownership?.resourceId !== allocation.id || ownership?.accountId !== launch.accountId || ownership?.workspaceId !== launch.workspaceId ||
    ownership?.packageId !== "basic" || ownership?.nodePoolId !== approval.expected.nodePoolId || ownership?.machineId !== allocation.machineName ||
    ownership?.instanceId !== instanceId || ownership?.nodeName !== allocation.nodeName || ownership?.status !== "active") {
    throw new Error("production_basic_canary_machine_ownership_invalid");
  }
  if (truth?.computeState !== "ready" || truth?.storageState !== "ready" || truth?.compute?.id !== allocation.id ||
    truth?.compute?.accountId !== launch.accountId || truth?.compute?.workspaceId !== launch.workspaceId || truth?.compute?.packageId !== "basic" ||
    truth?.compute?.providerResourceId !== instanceId || truth?.compute?.machineName !== allocation.machineName || truth?.compute?.instanceId !== instanceId ||
    truth?.compute?.nodePoolId !== approval.expected.nodePoolId || truth?.compute?.instanceType !== resolvedInstanceType || truth?.compute?.zone !== allocation.zone ||
    truth?.compute?.providerData?.instanceType !== resolvedInstanceType || String(truth?.compute?.providerData?.cpu || "") !== "2" || String(truth?.compute?.providerData?.memoryGb || "") !== "4" ||
    truth?.compute?.chargeType !== "PREPAID" || truth?.compute?.renewFlag !== "NOTIFY_AND_MANUAL_RENEW" || truth?.compute?.deadline !== allocation.deadline ||
    truth?.storage?.id !== launch.storageId || truth?.storage?.accountId !== launch.accountId || truth?.storage?.workspaceId !== launch.workspaceId ||
    !/^disk-/.test(String(truth?.storage?.providerResourceId || "")) || truth?.storage?.sizeGb !== 10 || truth?.storage?.zone !== allocation.zone ||
    truth?.storage?.chargeType !== "PREPAID" || truth?.storage?.renewFlag !== "NOTIFY_AND_MANUAL_RENEW" || !truth?.storage?.deadline) {
    throw new Error("production_basic_canary_provider_truth_invalid");
  }
  return {
    instanceId,
    machineId: allocation.machineName,
    nodeName: allocation.nodeName,
    zone: allocation.zone,
    sku: allocation.instanceType,
    deadline: allocation.deadline,
    procurement: {
      nodePoolId: plan.nodePoolId,
      baselineReplicas: plan.baselineReplicas,
      targetReplicas: plan.targetReplicas,
      beforeMachineCount: plan.beforeMachineNames.length,
      machineWasNew: true
    },
    actionCount
  };
}

function canaryUsageSnapshot(result) {
  const page = gatewayCanaryPage(result);
  const ids = new Set();
  for (const item of page.items) {
    const id = String(item?.requestId || "");
    if (!id || ids.has(id)) throw new Error("production_basic_canary_usage_invalid");
    ids.add(id);
  }
  return { total: page.total, ids, items: page.items };
}

const BASIC_CANARY_CHECKPOINT_STAGES = Object.freeze([
  "initial",
  "account_provision_attempted",
  "account_provisioned",
  "baseline_ready",
  "wallet_adjustment_attempted",
  "wallet_recharged",
  "workspace_launch_attempted",
  "launch_accepted",
  "launch_succeeded",
  "runtime_ready",
  "model_request_attempted",
  "completed"
]);

const BASIC_CANARY_PRECHARGE_RECOVERY_CHECKPOINT_STAGES = Object.freeze([
  "initial",
  "account_verified",
  "precharge_verified",
  "workspace_launch_attempted",
  "launch_accepted",
  "launch_succeeded",
  "runtime_ready",
  "model_request_attempted",
  "completed"
]);

function stableCanaryId(...parts) {
  const hash = createHash("sha1");
  for (const part of parts) {
    hash.update(String(part));
    hash.update(Buffer.from([0]));
  }
  return hash.digest("hex");
}

function canonicalJson(value) {
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(",")}]`;
  if (!value || typeof value !== "object") return JSON.stringify(value);
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJson(value[key])}`).join(",")}}`;
}

function basicCanaryApprovalDigest(approval) {
  return createHash("sha256").update(canonicalJson(approval)).digest("hex");
}

function basicCanaryCheckpointIdentity(approval) {
  const prechargeRecovery = basicCanaryUsesPrechargeRecovery(approval);
  const accountId = prechargeRecovery
    ? String(approval.customer.accountId)
    : `acct-${stableCanaryId("account", approval.customer.email).slice(0, 18)}`;
  const derivedLaunchOperationId = `workspace-launch-${stableCanaryId(accountId, approval.idempotencyKeys.workspaceLaunch).slice(0, 18)}`;
  const launchOperationId = prechargeRecovery ? approval.expected.launchOperationId : derivedLaunchOperationId;
  const identities = {
    accountId,
    launchOperationId,
    workspaceId: prechargeRecovery ? approval.expected.workspaceId : `ws-${stableCanaryId("workspace-launch-v2", accountId, launchOperationId).slice(0, 18)}`
  };
  if (prechargeRecovery) {
    identities.prechargeOperationId = String(approval.prechargeOperationId);
  } else {
    identities.accountOperationId = `account-provision-${stableCanaryId(approval.idempotencyKeys.accountProvision, approval.customer.email).slice(0, 18)}`;
    identities.walletOperationId = `wallet-adjustment-${stableCanaryId(accountId, approval.idempotencyKeys.walletAdjustment).slice(0, 18)}`;
  }
  return identities;
}

function basicCanaryCheckpointStages(checkpoint) {
  return checkpoint?.fundingMode === BASIC_CANARY_PRECHARGE_RECOVERY_MODE
    ? BASIC_CANARY_PRECHARGE_RECOVERY_CHECKPOINT_STAGES
    : BASIC_CANARY_CHECKPOINT_STAGES;
}

function checkpointAtLeast(checkpoint, stage) {
  const stages = basicCanaryCheckpointStages(checkpoint);
  return stages.indexOf(checkpoint.stage) >= stages.indexOf(stage);
}

async function loadBasicCanaryCheckpoint(path, approval) {
  const prechargeRecovery = basicCanaryUsesPrechargeRecovery(approval);
  const identities = basicCanaryCheckpointIdentity(approval);
  const initial = prechargeRecovery
    ? {
      schemaVersion: 3,
      fundingMode: BASIC_CANARY_PRECHARGE_RECOVERY_MODE,
      approvalDigest: basicCanaryApprovalDigest(approval),
      stage: "initial",
      identities,
      httpAttempts: { workspaceLaunch: null, modelRequest: null }
    }
    : {
      schemaVersion: 2,
      approvalDigest: basicCanaryApprovalDigest(approval),
      stage: "initial",
      identities,
      httpAttempts: { accountProvision: null, walletAdjustment: null, workspaceLaunch: null, modelRequest: null },
      baseline: { walletBeforeRechargeUsdMicros: "", walletAfterRechargeUsdMicros: "" }
    };
  if (!path) return { checkpoint: initial, present: false };
  let parsed;
  try {
    parsed = JSON.parse(await readFile(path, "utf8"));
  } catch (error) {
    if (error?.code === "ENOENT") return { checkpoint: initial, present: false };
    throw new Error("production_basic_canary_checkpoint_invalid");
  }
  const attempts = parsed?.httpAttempts;
  const baseline = parsed?.baseline;
  const stages = basicCanaryCheckpointStages(initial);
  if (!exactObjectKeys(parsed, Object.keys(initial)) ||
    parsed.schemaVersion !== initial.schemaVersion || parsed.approvalDigest !== initial.approvalDigest ||
    (prechargeRecovery && parsed.fundingMode !== BASIC_CANARY_PRECHARGE_RECOVERY_MODE) ||
    !stages.includes(parsed.stage) || JSON.stringify(parsed.identities) !== JSON.stringify(identities) ||
    !exactObjectKeys(attempts, Object.keys(initial.httpAttempts)) ||
    Object.values(attempts).some((value) => value !== null && (!Number.isInteger(value) || value < 0 || value > 1)) ||
    (!prechargeRecovery && (!exactObjectKeys(baseline, ["walletBeforeRechargeUsdMicros", "walletAfterRechargeUsdMicros"]) ||
      (checkpointAtLeast(parsed, "baseline_ready") && !/^[0-9]+$/.test(baseline.walletBeforeRechargeUsdMicros)) ||
      (checkpointAtLeast(parsed, "wallet_recharged") && !/^[0-9]+$/.test(baseline.walletAfterRechargeUsdMicros))))) {
    throw new Error("production_basic_canary_checkpoint_invalid");
  }
  if (parsed.stage === "completed") throw new Error("production_basic_canary_already_completed");
  return { checkpoint: parsed, present: true };
}

function validateBasicCanaryKeyEvidence(evidence, workspaceId, workspaceApiKeyId) {
  if (!evidence || evidence.generalKeysUnchanged !== true || evidence.workspaceKeysCreated !== 1 || !Array.isArray(evidence.generalKeys) ||
    !Array.isArray(evidence.generalKeyIds) || evidence.generalKeyIds.length !== evidence.generalKeys.length || !evidence.workspaceKey) {
    throw new Error("production_basic_canary_prepared_evidence_invalid");
  }
  const generalKeys = sortedCanaryKeys(evidence.generalKeys);
  const workspaceKey = canaryKeySummary(evidence.workspaceKey);
  if (generalKeys.some((key) => key.kind !== "general") || canonicalJson(evidence.generalKeyIds) !== canonicalJson(generalKeys.map((key) => key.id)) ||
    workspaceKey.id !== String(workspaceApiKeyId) || workspaceKey.kind !== "workspace" || workspaceKey.name !== canonicalWorkspaceKeyName(workspaceId) || workspaceKey.status !== "active") {
    throw new Error("production_basic_canary_prepared_evidence_invalid");
  }
  return { generalKeys, generalKeyIds: generalKeys.map((key) => key.id), workspaceKey, generalKeysUnchanged: true, workspaceKeysCreated: 1 };
}

function validatePreparedBasicCanaryEvidence(prepared, approval, identities, runId) {
  const wallet = prepared?.wallet;
  const prechargeRecovery = basicCanaryUsesPrechargeRecovery(approval);
  const walletOperationId = prechargeRecovery ? identities.prechargeOperationId : identities.walletOperationId;
  if (!exactObjectKeys(prepared, [
    "schemaVersion", "ok", "status", "stage", "approvalDigest", "runId", "mergedSha", "cloudRevision", "identities",
    "resourceContract", "keyEvidence", "operationId", "workspaceId", "wallet", "compute", "storage", "attachment", "runtime", "receipt", "httpAttempts", "writeCounts"
  ]) || prepared.schemaVersion !== 1 || prepared.ok !== true || prepared.status !== "prepared" || prepared.stage !== "runtime_ready" ||
    prepared.approvalDigest !== basicCanaryApprovalDigest(approval) || prepared.runId !== runId || prepared.mergedSha !== approval.expected.mergedSha ||
    JSON.stringify(prepared.identities) !== JSON.stringify(identities) || prepared.operationId !== identities.launchOperationId || prepared.workspaceId !== identities.workspaceId ||
    prepared.resourceContract?.cpu !== 2 || prepared.resourceContract?.memoryGb !== 4 ||
    prepared.compute?.allocationId == null || prepared.compute?.sku !== approval.expected.resolvedInstanceType ||
    prepared.compute?.procurement?.nodePoolId !== approval.expected.nodePoolId || prepared.compute?.resources?.cpu !== 2 || prepared.compute?.resources?.memoryGb !== 4 ||
    prepared.runtime?.ready !== true || prepared.runtime?.pod?.ready !== true || prepared.runtime?.pod?.resources?.cpu !== 2 || prepared.runtime?.pod?.resources?.memoryGb !== 4 ||
    prepared.runtime?.pod?.nodeName !== prepared.compute?.nodeName ||
    (prechargeRecovery
      ? prepared.writeCounts?.accountProvisionPosts !== 0 || prepared.writeCounts?.walletAdjustmentPosts !== 0
      : ![0, 1].includes(prepared.writeCounts?.accountProvisionPosts) || ![0, 1].includes(prepared.writeCounts?.walletAdjustmentPosts)) ||
    prepared.writeCounts?.workspaceLaunchPosts !== 1 ||
    prepared.writeCounts?.modelRequests !== 0 || prepared.writeCounts?.workspaceKeysCreated !== 1 || prepared.writeCounts?.workspacePurchaseDebits !== 1 ||
    prepared.writeCounts?.tencentCvmPurchases !== 1 || prepared.writeCounts?.tencentCbsPurchases !== 1) {
    throw new Error("production_basic_canary_prepared_evidence_invalid");
  }
  if (!exactObjectKeys(wallet, ["operationId", "source", "beforeUsdMicros", "afterUsdMicros", "deltaUsdMicros"]) ||
      wallet.operationId !== walletOperationId || wallet.source !== "wallet_adjustment_authoritative_readback" ||
      ![wallet.beforeUsdMicros, wallet.afterUsdMicros, wallet.deltaUsdMicros].every((value) => /^(0|[1-9][0-9]*)$/.test(String(value || ""))) ||
      BigInt(wallet.afterUsdMicros) - BigInt(wallet.beforeUsdMicros) !== BigInt(approval.rechargeUsdMicros) ||
      BigInt(wallet.deltaUsdMicros) !== BigInt(approval.rechargeUsdMicros)) {
    throw new Error("production_basic_canary_prepared_evidence_invalid");
  }
  if (prechargeRecovery && !exactObjectKeys(prepared.httpAttempts, ["workspaceLaunch", "modelRequest"])) {
    throw new Error("production_basic_canary_prepared_evidence_invalid");
  }
  validateBasicCanaryKeyEvidence(prepared.keyEvidence, prepared.workspaceId, prepared.keyEvidence?.workspaceKey?.id);
  validateBasicCanaryCloudRevisionEvidence(prepared.cloudRevision, approval.expected.mergedSha, approval.expected.cloudImageDigest);
  return prepared;
}

async function saveBasicCanaryCheckpoint(checkpoint, stage, path, afterCheckpoint) {
  const stages = basicCanaryCheckpointStages(checkpoint);
  if (!stages.includes(stage) || stages.indexOf(stage) < stages.indexOf(checkpoint.stage)) {
    throw new Error("production_basic_canary_checkpoint_transition_invalid");
  }
  checkpoint.stage = stage;
  await writeVerificationManifest(path, checkpoint);
  if (typeof afterCheckpoint === "function") await afterCheckpoint(stage);
}

function recordBasicCanaryHttpAttempt(checkpoint, name) {
  const current = checkpoint.httpAttempts[name];
  checkpoint.httpAttempts[name] = current === 0 ? 1 : null;
}

async function authoritativeGet(options) {
  try {
    return { found: true, ...(await requestJson(options)) };
  } catch (error) {
    if (String(error?.message || "").startsWith(`request_failed:GET:${options.path}:404:`)) return { found: false };
    throw error;
  }
}

async function readBasicCanaryAccountAuthority(requestOptions, adminAuth, approval, expectedAccountId) {
  const pageSize = 50;
  let expectedTotal = null;
  const matches = [];
  for (let page = 1; ; page += 1) {
    const envelope = sourceEnvelope(await requestJson({
      ...requestOptions,
      auth: adminAuth,
      path: `/api/operator/accounts?page=${page}&pageSize=${pageSize}`
    }), "control-plane+sub2api", true);
    const data = envelope.data;
    if (!Array.isArray(data?.items) || !Number.isSafeInteger(data?.total) || data.total < 0 || data.page !== page || data.pageSize !== pageSize ||
      data.items.length > pageSize || (expectedTotal !== null && data.total !== expectedTotal)) {
      throw new Error("production_basic_canary_account_readback_failed");
    }
    expectedTotal ??= data.total;
    const pages = Math.max(1, Math.ceil(data.total / pageSize));
    const expectedItems = page < pages ? pageSize : data.total - (page - 1) * pageSize;
    if (data.items.length !== Math.max(0, expectedItems)) throw new Error("production_basic_canary_account_readback_failed");
    for (const item of data.items) {
      const email = String(item?.email || "").trim().toLowerCase();
      if (item?.accountId === expectedAccountId || email === approval.customer.email) matches.push(item);
    }
    if (page >= pages) break;
  }
  if (matches.length === 0) return { found: false };
  const account = matches[0];
  if (matches.length !== 1 || account?.accountId !== expectedAccountId || String(account?.email || "").trim().toLowerCase() !== approval.customer.email ||
    account?.role !== "owner" || account?.status !== "active" || !/^usr-/.test(String(account?.consoleUserId || "")) ||
    !/^[1-9][0-9]*$/.test(String(account?.sub2apiUserId || ""))) {
    throw new Error("production_basic_canary_account_readback_failed");
  }
  return { found: true, account };
}

function basicCanaryWalletAdjustment(payload, expectedOperationId, expectedAccountId, expectedAmountMicros) {
  if (payload?.operationId !== expectedOperationId || payload?.accountId !== expectedAccountId || payload?.kind !== "recharge" ||
    usdDecimalMicros(payload?.amountUsd) !== BigInt(expectedAmountMicros) || payload?.reason !== "production Basic customer canary precharge" ||
    payload?.status !== "succeeded" || payload?.phase !== "complete") {
    throw new Error("production_basic_canary_recharge_readback_failed");
  }
  const before = readOnlyNestedSource(payload.beforeBalance, "sub2api");
  const after = readOnlyNestedSource(payload.afterBalance, "sub2api");
  for (const balance of [before, after]) {
    if (balance?.currency !== "USD" || !/^(0|[1-9][0-9]*)$/.test(String(balance?.usdMicros || "")) ||
      BigInt(balance.usdMicros) > 9_223_372_036_854_775_807n) {
      throw new Error("production_basic_canary_recharge_readback_failed");
    }
  }
  if (BigInt(after.usdMicros) - BigInt(before.usdMicros) !== BigInt(expectedAmountMicros)) {
    throw new Error("production_basic_canary_recharge_readback_failed");
  }
  return { before: { usdMicros: String(before.usdMicros) }, after: { usdMicros: String(after.usdMicros) } };
}

function basicCanaryRechargeEvidence(operationId, before, after) {
  return {
    operationId,
    source: "wallet_adjustment_authoritative_readback",
    beforeUsdMicros: before.usdMicros,
    afterUsdMicros: after.usdMicros,
    deltaUsdMicros: String(BigInt(after.usdMicros) - BigInt(before.usdMicros))
  };
}

function basicCanaryLaunchIdentity(launch, approval, expected, quote) {
  const priceVersion = String(launch?.priceVersion || "");
  if (launch?.operationId !== expected.operationId || launch?.workspaceId !== expected.workspaceId || launch?.accountId !== expected.accountId ||
    launch?.name !== approval.launch.name || launch?.packageId !== "basic" || launch?.sizeGb !== 10 || launch?.autoRenew !== false ||
    !priceVersion || launch?.currency !== "USD" || !Number.isSafeInteger(launch?.totalChargeUsdMicros) || launch.totalChargeUsdMicros <= 0 ||
    quote && (priceVersion !== quote.priceVersion || launch.currency !== quote.currency || launch.totalChargeUsdMicros !== quote.totalChargeUsdMicros)) {
    throw new Error("production_basic_canary_launch_readback_failed");
  }
  return launch;
}

async function readEmptyBasicCanaryLaunchBaseline(requestOptions, customerAuth, { allowNonWorkspaceReceipts = false } = {}) {
  const workspaces = controlPlaneCanaryPage(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/workspaces?page=1&pageSize=20" }));
  const launches = (await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/workspace-launches" })).payload;
  const keySnapshot = await readGatewayCanaryKeySnapshot(requestOptions, customerAuth);
  const receipts = sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/billing/receipts?limit=20" }), "ledger", true).data;
  const receiptItems = receipts?.receipts;
  const workspaceReceipts = Array.isArray(receiptItems) ? receiptItems.filter((receipt) => receipt?.type === "billing.workspace_purchased.v1" || receipt?.workspaceId) : [];
  if (workspaces.total !== 0 || !Array.isArray(launches) || launches.length !== 0 || keySnapshot.workspaceKeys.length !== 0 || !Array.isArray(receiptItems) ||
    (!allowNonWorkspaceReceipts && receiptItems.length !== 0) || workspaceReceipts.length !== 0 || receipts.hasMore !== false) {
    throw new Error("production_basic_canary_baseline_not_empty");
  }
  return { keySnapshot };
}

export async function verifyProductionBasicCustomerCanary(options = {}) {
  const {
    origin,
    fabricOrigin,
    internalServiceToken,
    adminEmail,
    adminPassword,
    customerPassword,
    approvalJson,
    approvalId,
    fundingMode = BASIC_CANARY_OPERATOR_PRECHARGE_MODE,
    confirmation,
    mergedSha,
    runId,
    launchPollAttempts = 180,
    launchPollDelayMs = 10_000,
    usageAttempts = DEFAULT_USAGE_ATTEMPTS,
    usageRetryDelayMs = DEFAULT_USAGE_RETRY_DELAY_MS,
    browserTimeoutMs = DEFAULT_BROWSER_TIMEOUT_MS,
    modelTimeoutMs = DEFAULT_MODEL_TIMEOUT_MS,
    requestTimeoutMs = DEFAULT_REQUEST_TIMEOUT_MS,
    fetchImpl = globalThis.fetch,
    fabricFetchImpl = fetchImpl,
    browserFactory,
    cloudRevisionEvidenceReader,
    runtimePodEvidenceReader,
    phase = "all",
    preparedEvidence,
    checkpointPath,
    afterCheckpoint,
    signal,
    now = new Date()
  } = options;
  const approval = basicCustomerCanaryApproval(approvalJson, approvalId, confirmation, now, fundingMode);
  const prechargeRecovery = basicCanaryUsesPrechargeRecovery(approval);
  const credentials = existingAdminCredentials(adminEmail, adminPassword);
  const preparing = phase === "prepare";
  const completing = phase === "complete";
  if (!new Set(["all", "prepare", "complete"]).has(phase) || mergedSha !== approval.expected.mergedSha || !String(customerPassword || "") ||
    (!completing && (typeof cloudRevisionEvidenceReader !== "function" || typeof runtimePodEvidenceReader !== "function" || !String(internalServiceToken || ""))) ||
    !Number.isInteger(launchPollAttempts) || launchPollAttempts < 1 || !Number.isFinite(launchPollDelayMs) || launchPollDelayMs < 0 ||
    !Number.isInteger(usageAttempts) || usageAttempts < 1 || !Number.isFinite(usageRetryDelayMs) || usageRetryDelayMs < 0) {
    throw new Error("production_basic_canary_config_invalid");
  }
  const loadedCheckpoint = await loadBasicCanaryCheckpoint(checkpointPath, approval);
  const { checkpoint, present: checkpointPresent } = loadedCheckpoint;
  const initialCheckpointStage = checkpoint.stage;
  const expectedIdentities = checkpoint.identities;
  const prepared = completing ? validatePreparedBasicCanaryEvidence(preparedEvidence, approval, expectedIdentities, runId) : null;
  const businessPostCounts = {
    accountProvisionPosts: prepared?.writeCounts.accountProvisionPosts || 0,
    walletAdjustmentPosts: prepared?.writeCounts.walletAdjustmentPosts || 0,
    workspaceLaunchPosts: prepared?.writeCounts.workspaceLaunchPosts || 0
  };
  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  const normalizedFabricOrigin = completing ? "" : internalFabricOrigin(fabricOrigin);
  const requestOptions = { fetchImpl, origin: normalizedOrigin, signal, timeoutMs: requestTimeoutMs };
  const fabricOptions = completing ? null : {
    fetchImpl: fabricFetchImpl,
    origin: normalizedFabricOrigin,
    signal,
    timeoutMs: requestTimeoutMs,
    headers: { authorization: `Bearer ${internalServiceToken}` }
  };
  let latestCloudRevisionEvidence;
  const assertCloudRevision = async () => {
    latestCloudRevisionEvidence = completing
      ? validateBasicCanaryCloudRevisionEvidence(prepared.cloudRevision, approval.expected.mergedSha, approval.expected.cloudImageDigest)
      : validateBasicCanaryCloudRevisionEvidence(await cloudRevisionEvidenceReader({
        expectedMergedSha: approval.expected.mergedSha,
        expectedCloudDigest: approval.expected.cloudImageDigest,
        signal
      }), approval.expected.mergedSha, approval.expected.cloudImageDigest);
    return latestCloudRevisionEvidence;
  };
  await assertCloudRevision();
  const resourceContract = completing
    ? { ...prepared.resourceContract }
    : basicCanaryResourceContract((await requestJson({
      ...fabricOptions,
      path: "/fabric/catalog",
      headers: fabricOptions.headers
    })).payload);

  const adminAuth = await login({ ...requestOptions, ...credentials });
  if (adminAuth.user?.accountId !== PRODUCTION_ADMIN.accountId || adminAuth.user?.role !== PRODUCTION_ADMIN.role || !adminAuth.csrfToken) {
    throw new Error("production_basic_canary_admin_login_failed");
  }
  const accountAuthority = await readBasicCanaryAccountAuthority(requestOptions, adminAuth, approval, expectedIdentities.accountId);
  if (!accountAuthority.found) {
    if (prechargeRecovery) throw new Error("production_basic_canary_existing_account_required");
    if (completing || checkpointAtLeast(checkpoint, "account_provisioned")) throw new Error("production_basic_canary_account_readback_failed");
    await assertCloudRevision();
    recordBasicCanaryHttpAttempt(checkpoint, "accountProvision");
    await saveBasicCanaryCheckpoint(checkpoint, "account_provision_attempted", checkpointPath, afterCheckpoint);
    businessPostCounts.accountProvisionPosts += 1;
    const provision = await requestJson({
      ...requestOptions,
      auth: adminAuth,
      path: "/api/operator/accounts",
      method: "POST",
      headers: { "Idempotency-Key": approval.idempotencyKeys.accountProvision },
      body: { email: approval.customer.email, password: String(customerPassword), name: approval.customer.name }
    });
    if (provision.response.status !== 201 || provision.payload?.status !== "succeeded" ||
      provision.payload?.operationId !== expectedIdentities.accountOperationId || provision.payload?.accountId !== expectedIdentities.accountId) {
      throw new Error("production_basic_canary_account_provision_failed");
    }
  }
  const accountId = expectedIdentities.accountId;
  const customerAuth = await login({ ...requestOptions, email: approval.customer.email, password: String(customerPassword) });
  if (customerAuth.user?.accountId !== accountId || customerAuth.user?.role !== "owner" || !customerAuth.csrfToken) {
    throw new Error("production_basic_canary_customer_login_failed");
  }
  const identity = sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/auth/me" }), "sub2api").data;
  const sub2apiUserId = String(identity?.sub2apiUserId || "");
  if (identity?.accountId !== accountId || identity?.email !== approval.customer.email || identity?.role !== "owner" || identity?.status !== "active" ||
    !/^usr-/.test(String(identity?.consoleUserId || "")) || !/^[1-9][0-9]*$/.test(sub2apiUserId)) {
    throw new Error("production_basic_canary_identity_invalid");
  }
  const accountVerifiedStage = prechargeRecovery ? "account_verified" : "account_provisioned";
  if (!checkpointAtLeast(checkpoint, accountVerifiedStage)) {
    await saveBasicCanaryCheckpoint(checkpoint, accountVerifiedStage, checkpointPath, afterCheckpoint);
  }

  const fixedLaunchIdentity = {
    operationId: expectedIdentities.launchOperationId,
    accountId,
    workspaceId: expectedIdentities.workspaceId
  };
  const launchPath = `/api/workspace-launches/${encodeURIComponent(fixedLaunchIdentity.operationId)}`;
  let launchAuthority = await authoritativeGet({ ...requestOptions, auth: customerAuth, path: launchPath });
  let keyBaseline;
  if (completing) {
    keyBaseline = { generalKeys: prepared.keyEvidence.generalKeys, workspaceKeys: [] };
  } else if (launchAuthority.found) {
    basicCanaryLaunchIdentity(launchAuthority.payload, approval, fixedLaunchIdentity);
    const recoveredKeySnapshot = await readGatewayCanaryKeySnapshot(requestOptions, customerAuth);
    const canonicalName = canonicalWorkspaceKeyName(launchAuthority.payload.workspaceId);
    if (recoveredKeySnapshot.workspaceKeys.length > 1 ||
      recoveredKeySnapshot.workspaceKeys.some((key) => key.id !== String(launchAuthority.payload.workspaceApiKeyId) || key.name !== canonicalName || key.status !== "active")) {
      throw new Error("production_basic_canary_baseline_not_empty");
    }
    keyBaseline = recoveredKeySnapshot;
  } else if (!checkpointAtLeast(checkpoint, "launch_accepted")) {
    keyBaseline = (await readEmptyBasicCanaryLaunchBaseline(requestOptions, customerAuth, {
      allowNonWorkspaceReceipts: prechargeRecovery
    })).keySnapshot;
  } else {
    keyBaseline = await readGatewayCanaryKeySnapshot(requestOptions, customerAuth);
  }

  const reconciliation = sourceEnvelope(await requestJson({ ...requestOptions, auth: adminAuth, path: "/api/operator/reconciliation?page=1&pageSize=20" }), "control-plane", true).data;
  if (!Array.isArray(reconciliation?.items) || reconciliation.total !== 0) throw new Error("production_basic_canary_reconciliation_blocker");
  const quote = basicCanaryQuote((await requestJson({
    ...requestOptions,
    auth: customerAuth,
    path: "/api/pricing/preview",
    method: "POST",
    body: { resourceType: "workspace", packageId: "basic", sizeGb: 10 }
  })).payload);
  if (launchAuthority.found) {
    basicCanaryLaunchIdentity(launchAuthority.payload, approval, fixedLaunchIdentity, quote);
  }
  let walletEvidence;
  if (prechargeRecovery) {
    const walletAdjustmentPath = `/api/operator/wallet-adjustments/${encodeURIComponent(expectedIdentities.prechargeOperationId)}`;
    const walletAuthority = await authoritativeGet({ ...requestOptions, auth: adminAuth, path: walletAdjustmentPath });
    if (!walletAuthority.found) throw new Error("production_basic_canary_recharge_readback_failed");
    const adjustment = basicCanaryWalletAdjustment(walletAuthority.payload, expectedIdentities.prechargeOperationId, accountId, approval.rechargeUsdMicros);
    walletEvidence = basicCanaryRechargeEvidence(expectedIdentities.prechargeOperationId, adjustment.before, adjustment.after);
    if (!checkpointAtLeast(checkpoint, "precharge_verified")) {
      await saveBasicCanaryCheckpoint(checkpoint, "precharge_verified", checkpointPath, afterCheckpoint);
    }
  } else {
    const walletAdjustmentPath = `/api/operator/wallet-adjustments/${encodeURIComponent(expectedIdentities.walletOperationId)}`;
    let walletAuthority = await authoritativeGet({ ...requestOptions, auth: adminAuth, path: walletAdjustmentPath });
    let walletBeforeRecharge;
    let walletAfterRecharge;
    if (walletAuthority.found) {
      const adjustment = basicCanaryWalletAdjustment(walletAuthority.payload, expectedIdentities.walletOperationId, accountId, approval.rechargeUsdMicros);
      walletBeforeRecharge = adjustment.before;
      walletAfterRecharge = adjustment.after;
    } else {
      if (completing || checkpointAtLeast(checkpoint, "wallet_recharged")) throw new Error("production_basic_canary_recharge_readback_failed");
      walletBeforeRecharge = walletFact(sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/gateway/wallet" }), "sub2api"), sub2apiUserId);
      checkpoint.baseline.walletBeforeRechargeUsdMicros = walletBeforeRecharge.usdMicros;
      if (!checkpointAtLeast(checkpoint, "baseline_ready")) {
        await saveBasicCanaryCheckpoint(checkpoint, "baseline_ready", checkpointPath, afterCheckpoint);
      }
    }
    if (BigInt(approval.rechargeUsdMicros) <= BigInt(quote.totalChargeUsdMicros)) {
      throw new Error("production_basic_canary_recharge_insufficient");
    }
    if (!walletAuthority.found) {
      await assertCloudRevision();
      recordBasicCanaryHttpAttempt(checkpoint, "walletAdjustment");
      await saveBasicCanaryCheckpoint(checkpoint, "wallet_adjustment_attempted", checkpointPath, afterCheckpoint);
      businessPostCounts.walletAdjustmentPosts += 1;
      const adjustment = await requestJson({
        ...requestOptions,
        auth: adminAuth,
        path: `/api/operator/accounts/${encodeURIComponent(accountId)}/wallet-adjustments`,
        method: "POST",
        headers: { "Idempotency-Key": approval.idempotencyKeys.walletAdjustment },
        body: {
          kind: "recharge",
          amountUsd: usdMicrosToDecimal(approval.rechargeUsdMicros),
          reason: "production Basic customer canary precharge",
          confirmationAccountId: accountId
        }
      });
      if (adjustment.response.status !== 201) throw new Error("production_basic_canary_recharge_failed");
      walletAuthority = await authoritativeGet({ ...requestOptions, auth: adminAuth, path: walletAdjustmentPath });
      if (!walletAuthority.found) throw new Error("production_basic_canary_recharge_readback_failed");
      const recovered = basicCanaryWalletAdjustment(walletAuthority.payload, expectedIdentities.walletOperationId, accountId, approval.rechargeUsdMicros);
      walletBeforeRecharge = recovered.before;
      walletAfterRecharge = recovered.after;
    }
    if (!walletBeforeRecharge || !walletAfterRecharge) {
      throw new Error("production_basic_canary_recharge_readback_failed");
    }
    if (BigInt(walletAfterRecharge.usdMicros) - BigInt(walletBeforeRecharge.usdMicros) !== BigInt(approval.rechargeUsdMicros)) {
      throw new Error("production_basic_canary_recharge_delta_invalid");
    }
    walletEvidence = basicCanaryRechargeEvidence(expectedIdentities.walletOperationId, walletBeforeRecharge, walletAfterRecharge);
    checkpoint.baseline.walletBeforeRechargeUsdMicros = walletBeforeRecharge.usdMicros;
    checkpoint.baseline.walletAfterRechargeUsdMicros = walletAfterRecharge.usdMicros;
    if (!checkpointAtLeast(checkpoint, "wallet_recharged")) {
      await saveBasicCanaryCheckpoint(checkpoint, "wallet_recharged", checkpointPath, afterCheckpoint);
    }
  }

  const launchSucceededWithoutCheckpoint = !checkpointPresent && launchAuthority.found && launchAuthority.payload?.status === "succeeded";
  let launch;
  if (!launchAuthority.found) {
    if (completing || checkpointAtLeast(checkpoint, "launch_accepted")) throw new Error("production_basic_canary_launch_readback_failed");
    const currentWallet = walletFact(sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/gateway/wallet" }), "sub2api"), sub2apiUserId);
    if (BigInt(currentWallet.usdMicros) <= BigInt(quote.totalChargeUsdMicros)) throw new Error("production_basic_canary_live_wallet_insufficient");
    const launchBaseline = await readEmptyBasicCanaryLaunchBaseline(requestOptions, customerAuth, { allowNonWorkspaceReceipts: true });
    if (!sameCanaryKeyCollection(launchBaseline.keySnapshot.generalKeys, keyBaseline.generalKeys) || launchBaseline.keySnapshot.workspaceKeys.length !== 0) {
      throw new Error("production_basic_canary_workspace_or_key_invalid");
    }
    keyBaseline = launchBaseline.keySnapshot;
    await assertCloudRevision();
    recordBasicCanaryHttpAttempt(checkpoint, "workspaceLaunch");
    await saveBasicCanaryCheckpoint(checkpoint, "workspace_launch_attempted", checkpointPath, afterCheckpoint);
    businessPostCounts.workspaceLaunchPosts += 1;
    const launched = await requestJson({
      ...requestOptions,
      auth: customerAuth,
      path: "/api/workspace-launches",
      method: "POST",
      headers: { "Idempotency-Key": approval.idempotencyKeys.workspaceLaunch },
      body: approval.launch
    });
    if (launched.response.status !== 202) throw new Error("production_basic_canary_launch_not_accepted");
    basicCanaryLaunchIdentity(launched.payload, approval, fixedLaunchIdentity, quote);
    launchAuthority = await authoritativeGet({ ...requestOptions, auth: customerAuth, path: launchPath });
    if (!launchAuthority.found) throw new Error("production_basic_canary_launch_readback_failed");
  }
  if (!walletEvidence) throw new Error("production_basic_canary_live_wallet_insufficient");
  launch = basicCanaryLaunchIdentity(launchAuthority.payload, approval, fixedLaunchIdentity, quote);
  if (!checkpointAtLeast(checkpoint, "launch_accepted")) {
    await saveBasicCanaryCheckpoint(checkpoint, "launch_accepted", checkpointPath, afterCheckpoint);
  }
  for (let attempt = 1; attempt <= launchPollAttempts && launch.status !== "succeeded"; attempt += 1) {
    if (["manual_review", "refunded", "failed"].includes(launch.status)) throw new Error(`production_basic_canary_${launch.status}`);
    if (attempt > 1 || launchPollDelayMs > 0) await sleep(launchPollDelayMs);
    const poll = await authoritativeGet({ ...requestOptions, auth: customerAuth, path: launchPath });
    if (!poll.found) throw new Error("production_basic_canary_launch_readback_failed");
    launch = basicCanaryLaunchIdentity(poll.payload, approval, fixedLaunchIdentity, quote);
  }
  if (["manual_review", "refunded", "failed"].includes(launch.status)) throw new Error(`production_basic_canary_${launch.status}`);
  if (launch.status !== "succeeded" || launch.phase !== "succeeded") throw new Error("production_basic_canary_launch_timeout");
  const runtimeServiceName = String(launch.runtimeServiceName || "");
  if (!launch.computeAllocationId || !launch.storageId || !launch.attachmentId || !launch.workspaceApiKeyId || !runtimeServiceName || !launch.receiptId || !launch.url) {
    throw new Error("production_basic_canary_launch_result_incomplete");
  }
  if (!checkpointAtLeast(checkpoint, "launch_succeeded")) {
    await saveBasicCanaryCheckpoint(checkpoint, "launch_succeeded", checkpointPath, afterCheckpoint);
  }
  const runtime = sourceEnvelope(await requestJson({
    ...requestOptions,
    auth: customerAuth,
    path: `/api/workspaces/${encodeURIComponent(launch.workspaceId)}/runtime-status`
  }), "fabric").data;
  const runtimeId = String(runtime?.runtimeId || "");
  if (!runtimeId || runtime?.workspaceId !== launch.workspaceId || runtime?.serviceName !== runtimeServiceName || runtime?.url !== launch.url ||
    runtime?.ready !== true || runtime?.status !== "running" || runtime?.access?.credentialStatus !== "configured" || !runtime?.access?.username) {
    throw new Error("production_basic_canary_runtime_invalid");
  }
  if (initialCheckpointStage === "model_request_attempted" || launchSucceededWithoutCheckpoint) {
    checkpoint.httpAttempts.modelRequest = null;
    if (!checkpointAtLeast(checkpoint, "model_request_attempted")) {
      await saveBasicCanaryCheckpoint(checkpoint, "model_request_attempted", checkpointPath, afterCheckpoint);
    }
    throw new Error("production_basic_canary_model_result_unknown");
  }

  const workspacePage = controlPlaneCanaryPage(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/workspaces?page=1&pageSize=20" }));
  const keySnapshot = await readGatewayCanaryKeySnapshot(requestOptions, customerAuth);
  if (workspacePage.total !== 1 || workspacePage.items[0]?.id !== launch.workspaceId || workspacePage.items[0]?.state !== "active" ||
    keySnapshot.workspaceKeys.length !== 1) {
    throw new Error("production_basic_canary_workspace_or_key_invalid");
  }
  const keyEvidence = basicCanaryKeyEvidence(keySnapshot, keyBaseline, launch.workspaceId, launch.workspaceApiKeyId);
  const receiptPage = sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/billing/receipts?limit=20" }), "ledger").data;
  if (!Array.isArray(receiptPage?.receipts) || receiptPage.receipts.length !== 1 || receiptPage.hasMore !== false || receiptPage.receipts[0]?.receiptId !== launch.receiptId) {
    throw new Error("production_basic_canary_receipt_cardinality_invalid");
  }
  const receipt = canaryReceipt(await requestJson({
    ...requestOptions,
    auth: customerAuth,
    path: `/api/billing/receipts/${encodeURIComponent(launch.receiptId)}`
  }), {
    receiptId: launch.receiptId,
    workspaceId: launch.workspaceId,
    computeAllocationId: launch.computeAllocationId,
    storageId: launch.storageId,
		attachmentId: launch.attachmentId,
		runtimeId,
		workspaceApiKeyId: launch.workspaceApiKeyId,
		totalUsdMicros: quote.totalChargeUsdMicros,
		storageSizeGb: 10
	});

  const detail = sourceEnvelope(await requestJson({
    ...requestOptions,
    auth: adminAuth,
    path: `/api/operator/workspaces/${encodeURIComponent(launch.workspaceId)}`
  }), "control-plane+fabric+ledger").data;
  const workspaceFact = readOnlyNestedSource(detail?.workspace, "control-plane");
  const computeFact = operatorResource(detail, "compute");
  const storageFact = operatorResource(detail, "storage");
  const attachmentFact = operatorResource(detail, "attachment");
  const runtimeFact = operatorResource(detail, "runtime");
  if (workspaceFact?.id !== launch.workspaceId || workspaceFact?.state !== "active" || workspaceFact?.packageId !== "basic") {
    throw new Error("production_basic_canary_workspace_fact_invalid");
  }

  let compute;
  let pod;
  let storageEvidence;
  let ownershipNodeName;
  if (completing) {
    if (prepared.operationId !== launch.operationId || prepared.workspaceId !== launch.workspaceId || prepared.compute?.allocationId !== launch.computeAllocationId ||
      prepared.storage?.id !== launch.storageId || prepared.attachment?.id !== launch.attachmentId || prepared.runtime?.id !== runtimeId ||
      prepared.runtime?.providerId !== runtimeServiceName || prepared.runtime?.url !== (runtime.url || launch.url) ||
      canonicalJson(prepared.wallet) !== canonicalJson(walletEvidence) || canonicalJson(prepared.receipt) !== canonicalJson(receipt)) {
      throw new Error("production_basic_canary_prepared_evidence_mismatch");
    }
    compute = {
      ...prepared.compute,
      actionCount: (action) => ({ create_compute_allocation: prepared.writeCounts.tencentCvmPurchases, create_storage_volume: prepared.writeCounts.tencentCbsPurchases })[action] || 0
    };
    pod = prepared.runtime.pod;
    storageEvidence = { providerId: prepared.storage.providerId, zone: prepared.storage.zone, deadline: prepared.storage.expiresAt };
    ownershipNodeName = prepared.compute.nodeName;
  } else {
    const fabricHeaders = fabricOptions.headers;
    const operations = (await requestJson({ ...fabricOptions, path: "/fabric/operations", headers: fabricHeaders })).payload;
    const allocation = (await requestJson({ ...fabricOptions, path: `/fabric/compute-allocations/${encodeURIComponent(launch.computeAllocationId)}`, headers: fabricHeaders })).payload;
    const ownership = (await requestJson({ ...fabricOptions, path: `/fabric/machine-ownerships/${encodeURIComponent(launch.computeAllocationId)}`, headers: fabricHeaders })).payload;
    const truth = (await requestJson({
      ...fabricOptions,
      path: `/fabric/monthly-provider-truth?computeAllocationId=${encodeURIComponent(launch.computeAllocationId)}&storageVolumeId=${encodeURIComponent(launch.storageId)}`,
      headers: fabricHeaders
    })).payload;
    compute = validateFabricCanaryEvidence({ operations, allocation, ownership, truth, launch, approval });
    pod = await runtimePodEvidenceReader({ workspaceId: launch.workspaceId, expectedDigest: approval.expected.workspaceImageDigest, signal });
    storageEvidence = { providerId: truth.storage.providerResourceId, zone: truth.storage.zone, deadline: truth.storage.deadline };
    ownershipNodeName = ownership.nodeName;
  }
  if (computeFact.providerId !== compute.instanceId || computeFact.packageOrSpec !== approval.expected.resolvedInstanceType || computeFact.zone !== compute.zone ||
    storageFact.providerId !== storageEvidence.providerId || storageFact.zone !== storageEvidence.zone || storageFact.expiresAt !== storageEvidence.deadline || attachmentFact.status !== "attached" ||
    runtimeFact.providerId !== runtimeServiceName || runtimeFact.status !== "running") {
    throw new Error("production_basic_canary_operator_provider_facts_invalid");
  }

  if (pod?.ready !== true || pod?.containerName !== "workspace" || !pod?.podName || !String(pod?.imageID || "").endsWith(approval.expected.workspaceImageDigest) ||
    pod?.resources?.cpu !== resourceContract.cpu || pod?.resources?.memoryGb !== resourceContract.memoryGb || !pod?.nodeName ||
    pod.nodeName !== compute.nodeName || pod.nodeName !== ownershipNodeName) {
    throw new Error("production_basic_canary_runtime_pod_invalid");
  }
  if (!checkpointAtLeast(checkpoint, "runtime_ready")) {
    await saveBasicCanaryCheckpoint(checkpoint, "runtime_ready", checkpointPath, afterCheckpoint);
  }
  if (preparing) {
    const cloudRevision = await assertCloudRevision();
    return {
      schemaVersion: 1,
      ok: true,
      status: "prepared",
      stage: "runtime_ready",
      approvalDigest: basicCanaryApprovalDigest(approval),
      runId,
      mergedSha,
      cloudRevision,
      identities: { ...expectedIdentities },
      resourceContract: { ...resourceContract },
      keyEvidence,
      operationId: launch.operationId,
      workspaceId: launch.workspaceId,
      wallet: walletEvidence,
      compute: {
        allocationId: launch.computeAllocationId,
        instanceId: compute.instanceId,
        machineId: compute.machineId,
        nodeName: compute.nodeName,
        zone: compute.zone,
        sku: compute.sku,
        resources: { ...resourceContract },
        deadline: compute.deadline,
        procurement: compute.procurement
      },
      storage: { id: launch.storageId, providerId: storageFact.providerId, sizeGb: 10, zone: storageFact.zone, status: storageFact.status, expiresAt: storageFact.expiresAt },
      attachment: { id: launch.attachmentId, providerId: attachmentFact.providerId, status: attachmentFact.status },
      runtime: {
        id: runtimeId,
        providerId: runtimeFact.providerId,
        url: runtime.url || launch.url,
        ready: true,
        pod: { podName: pod.podName, nodeName: pod.nodeName, containerName: pod.containerName, ready: true, imageID: pod.imageID, resources: pod.resources }
      },
      receipt,
      httpAttempts: { ...checkpoint.httpAttempts },
      writeCounts: {
        ...businessPostCounts,
        modelRequests: 0,
        workspaceKeysCreated: keyEvidence.workspaceKeysCreated,
        workspacePurchaseDebits: 1,
        tencentCvmPurchases: compute.actionCount("create_compute_allocation"),
        tencentCbsPurchases: compute.actionCount("create_storage_volume")
      }
    };
  }
  const revealed = await requestJson({
    ...requestOptions,
    auth: customerAuth,
    path: `/api/workspaces/${encodeURIComponent(launch.workspaceId)}/runtime-credentials/reveal`,
    method: "POST",
    body: {}
  });
  if (revealed.response.headers.get("cache-control") !== "private, no-store" || revealed.payload?.workspaceId !== launch.workspaceId ||
    revealed.payload?.access?.username !== runtime.access.username || !revealed.payload?.access?.password) {
    throw new Error("production_basic_canary_runtime_credentials_invalid");
  }

  const usageBefore = canaryUsageSnapshot(await requestJson({
    ...requestOptions,
    auth: customerAuth,
    path: `/api/gateway/keys/${encodeURIComponent(launch.workspaceApiKeyId)}/usage?page=1&pageSize=20`
  }));
  const statsBefore = await gatewayUsageStats(requestOptions, customerAuth, launch.workspaceApiKeyId);
  if (usageBefore.total !== 0 || Object.values(statsBefore).some((value) => value !== 0)) {
    checkpoint.httpAttempts.modelRequest = null;
    await saveBasicCanaryCheckpoint(checkpoint, "model_request_attempted", checkpointPath, afterCheckpoint);
    throw new Error("production_basic_canary_model_result_unknown");
  }
  await assertCloudRevision();
  recordBasicCanaryHttpAttempt(checkpoint, "modelRequest");
  await saveBasicCanaryCheckpoint(checkpoint, "model_request_attempted", checkpointPath, afterCheckpoint);
  const browserEvidence = await verifyWorkspaceBrowserQa({
    url: runtime.url || launch.url,
    username: revealed.payload.access.username,
    password: revealed.payload.access.password,
    runId,
    browserTimeoutMs,
    modelTimeoutMs,
    browserFactory
  });
  let usageAfter;
  let usageRecord;
  let statsAfter;
  for (let attempt = 1; attempt <= usageAttempts; attempt += 1) {
    usageAfter = canaryUsageSnapshot(await requestJson({
      ...requestOptions,
      auth: customerAuth,
      path: `/api/gateway/keys/${encodeURIComponent(launch.workspaceApiKeyId)}/usage?page=1&pageSize=20`
    }));
    usageRecord = exactUsageRecord(usageBefore, usageAfter, approval.expected.model, launch.workspaceApiKeyId);
    if (usageRecord) {
      statsAfter = await gatewayUsageStats(requestOptions, customerAuth, launch.workspaceApiKeyId);
      if (statsMatchRequest(statsBefore, statsAfter, usageRecord)) break;
    }
    if (attempt < usageAttempts) await sleep(usageRetryDelayMs);
  }
  if (!usageRecord || !statsAfter || !statsMatchRequest(statsBefore, statsAfter, usageRecord)) {
    throw new Error("production_basic_canary_usage_evidence_invalid");
  }
  const readiness = (await requestJson({ ...requestOptions, path: "/api/production/readiness" })).payload;
  if (readiness?.ready !== true || readiness?.cloudImagesReady !== true || readiness?.workspaceImagesReady !== true || readiness?.immutableImagesReady !== true) {
    throw new Error("production_basic_canary_readiness_invalid");
  }

  const result = {
    ok: true,
    status: "passed",
    evidenceLevel: "real_customer_basic_canary",
    accountId,
    consoleUserId: identity.consoleUserId,
    sub2apiUserId,
    operationId: launch.operationId,
    workspaceId: launch.workspaceId,
    workspaceApiKeyId: launch.workspaceApiKeyId,
    keyEvidence,
    wallet: walletEvidence,
    compute: {
      allocationId: launch.computeAllocationId,
      instanceId: compute.instanceId,
      machineId: compute.machineId,
      nodeName: compute.nodeName,
      zone: compute.zone,
      sku: compute.sku,
      resources: resourceContract,
      deadline: compute.deadline,
      procurement: compute.procurement
    },
    storage: { id: launch.storageId, providerId: storageFact.providerId, sizeGb: 10, zone: storageFact.zone, status: storageFact.status, expiresAt: storageFact.expiresAt },
    attachment: { id: launch.attachmentId, providerId: attachmentFact.providerId, status: attachmentFact.status },
    runtime: {
      id: runtimeId,
      providerId: runtimeFact.providerId,
      url: runtime.url || launch.url,
      ready: true,
      pod: { podName: pod.podName, nodeName: pod.nodeName, containerName: pod.containerName, ready: true, imageID: pod.imageID, resources: pod.resources },
      login: browserEvidence.login,
      websocket: browserEvidence.websocket,
      modelResponse: browserEvidence.modelResponse
    },
    usage: {
      requestId: usageRecord.requestId,
      apiKeyId: usageRecord.apiKeyId,
      model: usageRecord.model,
      inputTokens: usageRecord.inputTokens,
      outputTokens: usageRecord.outputTokens,
      actualCostUsdMicros: usageRecord.actualCostUsdMicros
    },
    receipt,
    readiness: { ready: true, cloudImagesReady: true, workspaceImagesReady: true, immutableImagesReady: true },
    httpAttempts: { ...checkpoint.httpAttempts },
    writeCounts: {
      ...businessPostCounts,
      modelRequests: 1,
      workspaceKeysCreated: keyEvidence.workspaceKeysCreated,
      workspacePurchaseDebits: 1,
      tencentCvmPurchases: compute.actionCount("create_compute_allocation"),
      tencentCbsPurchases: compute.actionCount("create_storage_volume")
    }
  };
  await saveBasicCanaryCheckpoint(checkpoint, "completed", checkpointPath, afterCheckpoint);
  return result;
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

function walletDebitMatches(before, after, actualCostUsdMicros) {
  return BigInt(before.usdMicros) - BigInt(after.usdMicros) === BigInt(actualCostUsdMicros);
}

export async function verifyWorkspaceBrowserQa({
  url,
  username,
  password,
  runId,
  browserTimeoutMs = DEFAULT_BROWSER_TIMEOUT_MS,
  modelTimeoutMs = DEFAULT_MODEL_TIMEOUT_MS,
  browserFactory,
  beforeModelRequest
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
    if (beforeModelRequest) await beforeModelRequest();
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
      const balanceMatch = walletDebitMatches(walletBefore, walletAfter, requestUsage.actualCostUsdMicros);
      if (statsMatch && balanceMatch) break;
      statsMismatch ||= !statsMatch;
      balanceMismatch ||= !balanceMatch;
    }
    if (attempt < usageAttempts) await sleep(usageRetryDelayMs);
  }
  if (!requestUsage) throw new Error("exact_gateway_request_not_found");
  if (!statsAfter || !statsMatchRequest(statsBefore, statsAfter, requestUsage)) throw new Error(statsMismatch ? "gateway_usage_stats_mismatch" : "gateway_usage_stats_invalid");
  if (!walletAfter || !walletDebitMatches(walletBefore, walletAfter, requestUsage.actualCostUsdMicros)) {
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

function computeClaimCliMode(argv) {
  if (argv.includes("--compute-claim-diagnose")) return COMPUTE_CLAIM_DIAGNOSE_MODE;
  if (argv.includes("--compute-claim-recover")) return COMPUTE_CLAIM_RECOVER_MODE;
  if (argv.includes("--compute-claim-continue")) return COMPUTE_CLAIM_CONTINUATION_MODE;
  return "";
}

function workspaceLaunchReadbackCliMode(argv) {
  if (argv.includes("--workspace-launch-readback-diagnose")) return WORKSPACE_LAUNCH_READBACK_DIAGNOSE_MODE;
  if (argv.includes("--workspace-launch-readback-recover")) return WORKSPACE_LAUNCH_READBACK_RECOVER_MODE;
  return "";
}

function blockedComputeClaimArtifact(operationMode, rawErrorCode = "") {
  const fallbackErrorCode = operationMode === COMPUTE_CLAIM_DIAGNOSE_MODE
    ? "compute_claim_diagnosis_failed"
    : operationMode === COMPUTE_CLAIM_RECOVER_MODE
      ? "compute_claim_recovery_failed"
      : "compute_claim_continuation_failed";
  const errorCode = rawErrorCode !== "none" && COMPUTE_CLAIM_BLOCKED_ERROR_CODES.has(rawErrorCode)
    ? rawErrorCode
    : fallbackErrorCode;
  return {
    schemaVersion: 2,
    operationMode,
    status: "blocked",
    recoveryEligible: false,
    errorCode,
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    ...(operationMode === COMPUTE_CLAIM_CONTINUATION_MODE ? { backgroundMutationCountsState: "unknown" } : {})
  };
}

function blockedWorkspaceLaunchReadbackArtifact(operationMode) {
  return {
    schemaVersion: 1,
    operationMode,
    status: "blocked",
    recoveryEligible: false,
    errorCode: operationMode === WORKSPACE_LAUNCH_READBACK_DIAGNOSE_MODE
      ? "workspace_launch_readback_diagnosis_failed"
      : "workspace_launch_readback_recovery_failed",
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    ...(operationMode === WORKSPACE_LAUNCH_READBACK_RECOVER_MODE ? { backgroundMutationCountsState: "unknown" } : {})
  };
}

export async function runProductionLiveQaCli({
  argv = process.argv.slice(2),
  env = process.env,
  stdout = process.stdout,
  stderr = process.stderr,
  fetchImpl = globalThis.fetch,
  fabricFetchImpl = fetchImpl,
  browserFactory,
  cloudRevisionEvidenceReader,
  runtimePodEvidenceReader,
  execFileImpl,
  now = new Date()
} = {}) {
  if (argv.includes("--help") || argv.includes("-h")) {
    stdout.write("Usage: node tools/production-live-qa.ts --read-only\nCompute claim modes use --compute-claim-diagnose, --compute-claim-recover, or --compute-claim-continue with a non-secret target JSON. A recovered Workspace E2E requires --recovered-workspace-e2e --allow-model-write, an independent approval, and an absolute continuation artifact path. A manual-review diagnosis requires --manual-review-diagnose and a non-secret target JSON. A fixed-slot model request requires --allow-gateway-write --allow-model-write. A real customer canary requires --basic-customer-canary, an exact funding mode, and its explicit approvals.\n");
    return 0;
  }
  const computeClaimMode = computeClaimCliMode(argv);
  const workspaceLaunchReadbackMode = workspaceLaunchReadbackCliMode(argv);
  try {
    if (env.OPL_VERIFY_MODEL_ACCESS_KEY) throw new Error("production_live_qa_raw_key_forbidden");
    const args = cliArgs(argv);
    if (workspaceLaunchReadbackMode && (args["read-only"] === "true" || args["manual-review-diagnose"] === "true")) {
      throw new Error("workspace_launch_readback_mode_conflict");
    }
    if (args["read-only"] === "true") {
      if (args["compute-claim-continue"] || args["basic-customer-canary"] || args["allow-gateway-write"] || args["allow-model-write"] || args["approval-id"]) throw new Error("production_live_qa_read_only_conflict");
      const result = await verifyProductionReadOnlyRollout({
        origin: args.origin || env.OPL_CONSOLE_ORIGIN,
        adminEmail: env.OPL_SUB2API_ADMIN_EMAIL,
        adminPassword: env.OPL_SUB2API_ADMIN_PASSWORD,
        internalServiceToken: env.OPL_INTERNAL_SERVICE_TOKEN,
        requestTimeoutMs: Number(args["request-timeout-ms"] || env.OPL_VERIFY_REQUEST_TIMEOUT_MS || DEFAULT_REQUEST_TIMEOUT_MS),
        browserFactory,
        fetchImpl
      });
      stdout.write(`${JSON.stringify(result, null, 2)}\n`);
      return 0;
    }
    if (args["manual-review-diagnose"] === "true") {
      if (args["compute-claim-continue"] || args["basic-customer-canary"] || args["read-only"] || args["allow-account-provision"] || args["allow-wallet-recharge"] ||
        args["allow-workspace-purchase"] || args["allow-model-write"] || args["allow-gateway-write"] || args["approval-id"] || args["funding-mode"] || args.phase) {
        throw new Error("manual_review_diagnose_conflict");
      }
      const result = await diagnoseManualReviewRecovery({
        fabricOrigin: env.OPL_FABRIC_INTERNAL_ORIGIN,
        fabricPod: args["fabric-pod"],
        fabricNamespace: args["fabric-namespace"],
        target: args["diagnose-target-json"] || env.OPL_BASIC_CANARY_DIAGNOSE_TARGET_JSON,
        kubeconfigPath: env.KUBECONFIG || env.TENCENT_DEPLOY_KUBECONFIG_PATH,
        fetchImpl: fabricFetchImpl,
        execFileImpl
      });
      stdout.write(`${JSON.stringify(result, null, 2)}\n`);
      return 0;
    }
    if (args["workspace-launch-readback-diagnose"] === "true") {
      if (args["workspace-launch-readback-recover"] || args["compute-claim-diagnose"] || args["compute-claim-recover"] || args["compute-claim-continue"] ||
        args["manual-review-diagnose"] || args["basic-customer-canary"] || args["recovered-workspace-e2e"] || args["read-only"] || args["approval-id"] ||
        args["allow-account-provision"] || args["allow-wallet-recharge"] || args["allow-workspace-purchase"] || args["allow-model-write"] || args["allow-gateway-write"]) {
        throw new Error("workspace_launch_readback_diagnose_conflict");
      }
      const result = await diagnoseWorkspaceLaunchReadbackRecovery({
        target: args["workspace-launch-target-json"] || env.OPL_WORKSPACE_LAUNCH_READBACK_TARGET_JSON,
        mergedSha: env.OPL_MERGED_SHA,
        cloudImageDigest: env.OPL_WORKSPACE_LAUNCH_READBACK_CLOUD_DIGEST,
        origin: args.origin || env.OPL_CONSOLE_ORIGIN,
        adminEmail: env.OPL_SUB2API_ADMIN_EMAIL,
        adminPassword: env.OPL_SUB2API_ADMIN_PASSWORD,
        customerEmail: env.OPL_WORKSPACE_LAUNCH_READBACK_CUSTOMER_EMAIL,
        kubeconfigPath: env.KUBECONFIG || env.TENCENT_DEPLOY_KUBECONFIG_PATH,
        namespace: env.OPL_K8S_NAMESPACE || "opl-cloud",
        requestTimeoutMs: Number(args["request-timeout-ms"] || env.OPL_VERIFY_REQUEST_TIMEOUT_MS || DEFAULT_REQUEST_TIMEOUT_MS),
        cloudRevisionEvidenceReader,
        execFileImpl,
        fetchImpl,
        now
      });
      stdout.write(`${JSON.stringify(result, null, 2)}\n`);
      return 0;
    }
    if (args["workspace-launch-readback-recover"] === "true") {
      if (!args["approval-id"] || args["workspace-launch-readback-diagnose"] || args["compute-claim-diagnose"] || args["compute-claim-recover"] ||
        args["compute-claim-continue"] || args["manual-review-diagnose"] || args["basic-customer-canary"] || args["recovered-workspace-e2e"] || args["read-only"] ||
        args["allow-account-provision"] || args["allow-wallet-recharge"] || args["allow-workspace-purchase"] || args["allow-model-write"] || args["allow-gateway-write"]) {
        throw new Error("workspace_launch_readback_recovery_approval_required");
      }
      const result = await recoverWorkspaceLaunchReadbackRecovery({
        target: args["workspace-launch-target-json"] || env.OPL_WORKSPACE_LAUNCH_READBACK_TARGET_JSON,
        approvalJson: env.OPL_WORKSPACE_LAUNCH_READBACK_APPROVAL_JSON,
        approvalId: args["approval-id"],
        mergedSha: env.OPL_MERGED_SHA,
        cloudImageDigest: env.OPL_WORKSPACE_LAUNCH_READBACK_CLOUD_DIGEST,
        origin: args.origin || env.OPL_CONSOLE_ORIGIN,
        adminEmail: env.OPL_SUB2API_ADMIN_EMAIL,
        adminPassword: env.OPL_SUB2API_ADMIN_PASSWORD,
        customerEmail: env.OPL_WORKSPACE_LAUNCH_READBACK_CUSTOMER_EMAIL,
        internalServiceToken: env.OPL_INTERNAL_SERVICE_TOKEN,
        kubeconfigPath: env.KUBECONFIG || env.TENCENT_DEPLOY_KUBECONFIG_PATH,
        namespace: env.OPL_K8S_NAMESPACE || "opl-cloud",
        requestTimeoutMs: Number(args["request-timeout-ms"] || env.OPL_VERIFY_REQUEST_TIMEOUT_MS || DEFAULT_REQUEST_TIMEOUT_MS),
        cloudRevisionEvidenceReader,
        execFileImpl,
        fetchImpl,
        now
      });
      stdout.write(`${JSON.stringify(result, null, 2)}\n`);
      return result.status === "converged" ? 0 : 1;
    }
    if (args["compute-claim-diagnose"] === "true") {
      if (args["compute-claim-recover"] || args["compute-claim-continue"] || args["manual-review-diagnose"] || args["basic-customer-canary"] || args["read-only"] ||
        args["allow-account-provision"] || args["allow-wallet-recharge"] || args["allow-workspace-purchase"] || args["allow-model-write"] ||
        args["allow-gateway-write"] || args["approval-id"] || args["funding-mode"] || args.phase) {
        throw new Error("compute_claim_diagnose_conflict");
      }
      const kubeconfigPath = env.KUBECONFIG || env.TENCENT_DEPLOY_KUBECONFIG_PATH;
      const namespace = env.OPL_K8S_NAMESPACE || "opl-cloud";
      const result = await diagnoseComputeClaimRecovery({
        target: args["compute-claim-target-json"] || env.OPL_COMPUTE_CLAIM_TARGET_JSON,
        mergedSha: env.OPL_MERGED_SHA,
        cloudImageDigest: env.OPL_COMPUTE_CLAIM_CLOUD_DIGEST,
        kubeconfigPath,
        namespace,
        cloudRevisionEvidenceReader,
        execFileImpl
      });
      stdout.write(`${JSON.stringify(result, null, 2)}\n`);
      return 0;
    }
    if (args["compute-claim-recover"] === "true") {
      if (!args["approval-id"] || args["compute-claim-diagnose"] || args["compute-claim-continue"] || args["manual-review-diagnose"] || args["basic-customer-canary"] || args["read-only"] ||
        args["allow-account-provision"] || args["allow-wallet-recharge"] || args["allow-workspace-purchase"] || args["allow-model-write"] ||
        args["allow-gateway-write"] || args["funding-mode"] || args.phase) {
        throw new Error("compute_claim_recovery_approval_required");
      }
      const kubeconfigPath = env.KUBECONFIG || env.TENCENT_DEPLOY_KUBECONFIG_PATH;
      const namespace = env.OPL_K8S_NAMESPACE || "opl-cloud";
      const result = await recoverComputeClaim({
        target: args["compute-claim-target-json"] || env.OPL_COMPUTE_CLAIM_TARGET_JSON,
        approvalJson: env.OPL_COMPUTE_CLAIM_RECOVERY_APPROVAL_JSON,
        approvalId: args["approval-id"],
        mergedSha: env.OPL_MERGED_SHA,
        cloudImageDigest: env.OPL_COMPUTE_CLAIM_CLOUD_DIGEST,
        origin: args.origin || env.OPL_CONSOLE_ORIGIN,
        adminEmail: env.OPL_SUB2API_ADMIN_EMAIL,
        adminPassword: env.OPL_SUB2API_ADMIN_PASSWORD,
		internalServiceToken: env.OPL_INTERNAL_SERVICE_TOKEN,
        kubeconfigPath,
        namespace,
        requestTimeoutMs: Number(args["request-timeout-ms"] || env.OPL_VERIFY_REQUEST_TIMEOUT_MS || DEFAULT_REQUEST_TIMEOUT_MS),
        cloudRevisionEvidenceReader,
        execFileImpl,
        fetchImpl,
        now
      });
      stdout.write(`${JSON.stringify(result, null, 2)}\n`);
      return result.status === "claimed" ? 0 : 1;
    }
    if (args["compute-claim-continue"] === "true") {
      if (args["compute-claim-recover"] || args["compute-claim-diagnose"] || args["manual-review-diagnose"] || args["basic-customer-canary"] || args["read-only"] ||
        args["allow-account-provision"] || args["allow-wallet-recharge"] || args["allow-workspace-purchase"] || args["allow-model-write"] ||
        args["allow-gateway-write"] || args["approval-id"] || args["funding-mode"] || args.phase) {
        throw new Error("compute_claim_continuation_conflict");
      }
      const kubeconfigPath = env.KUBECONFIG || env.TENCENT_DEPLOY_KUBECONFIG_PATH;
      const namespace = env.OPL_K8S_NAMESPACE || "opl-cloud";
      const result = await continueComputeClaimWorkspace({
        target: args["compute-claim-target-json"] || env.OPL_COMPUTE_CLAIM_TARGET_JSON,
        mergedSha: env.OPL_MERGED_SHA,
        cloudImageDigest: env.OPL_COMPUTE_CLAIM_CLOUD_DIGEST,
        origin: args.origin || env.OPL_CONSOLE_ORIGIN,
        customerEmail: env.OPL_BASIC_CANARY_CUSTOMER_EMAIL,
        customerPassword: env.OPL_BASIC_CANARY_CUSTOMER_PASSWORD,
        kubeconfigPath,
        namespace,
        launchPollAttempts: Number(args["launch-poll-attempts"] || env.OPL_VERIFY_LAUNCH_POLL_ATTEMPTS || 180),
        launchPollDelayMs: Number(args["launch-poll-delay-ms"] || env.OPL_VERIFY_LAUNCH_POLL_DELAY_MS || 10_000),
        requestTimeoutMs: Number(args["request-timeout-ms"] || env.OPL_VERIFY_REQUEST_TIMEOUT_MS || DEFAULT_REQUEST_TIMEOUT_MS),
        cloudRevisionEvidenceReader,
        execFileImpl,
        fetchImpl,
        now
      });
      stdout.write(`${JSON.stringify(result, null, 2)}\n`);
      return result.status === "succeeded" ? 0 : 1;
    }
    if (args["recovered-workspace-e2e"] === "true") {
      const conflictingArgs = [
        "read-only", "manual-review-diagnose", "compute-claim-diagnose", "compute-claim-recover", "compute-claim-continue",
        "basic-customer-canary", "allow-account-provision", "allow-wallet-recharge", "allow-workspace-purchase",
        "allow-gateway-write", "allow-existing-precharge-recovery", "funding-mode", "phase"
      ];
      const forbiddenEnv = [
        "KUBECONFIG", "TENCENT_DEPLOY_KUBECONFIG_PATH", "TENCENT_DEPLOY_KUBECONFIG_B64", "TENCENT_DEPLOY_KUBECONFIG",
        "TENCENTCLOUD_SECRET_ID", "TENCENTCLOUD_SECRET_KEY", "OPL_INTERNAL_SERVICE_TOKEN",
        "OPL_SUB2API_ADMIN_EMAIL", "OPL_SUB2API_ADMIN_PASSWORD"
      ];
      if (args["allow-model-write"] !== "true" || !args["approval-id"] || conflictingArgs.some((name) => args[name]) ||
        forbiddenEnv.some((name) => String(env[name] || ""))) {
        throw new Error("recovered_workspace_e2e_cli_conflict");
      }
      const continuationPath = String(args["continuation-evidence"] || "");
      if (!continuationPath.startsWith("/")) throw new Error("recovered_workspace_e2e_cli_continuation_evidence_invalid");
      let continuationEvidence;
      try {
        continuationEvidence = JSON.parse(await readFile(continuationPath, "utf8"));
      } catch {
        throw new Error("recovered_workspace_e2e_cli_continuation_evidence_invalid");
      }
      const result = await verifyRecoveredWorkspaceE2E({
        origin: args.origin || env.OPL_CONSOLE_ORIGIN,
        mergedSha: env.OPL_MERGED_SHA,
        customerEmail: env.OPL_RECOVERED_WORKSPACE_CUSTOMER_EMAIL,
        customerPassword: env.OPL_RECOVERED_WORKSPACE_CUSTOMER_PASSWORD,
        approvalJson: env.OPL_RECOVERED_WORKSPACE_E2E_APPROVAL_JSON,
        approvalId: args["approval-id"],
        confirmation: env.OPL_RECOVERED_WORKSPACE_E2E_CONFIRMATION,
        continuationEvidence,
        usageAttempts: Number(env.OPL_VERIFY_USAGE_ATTEMPTS || DEFAULT_USAGE_ATTEMPTS),
        usageRetryDelayMs: Number(env.OPL_VERIFY_USAGE_RETRY_DELAY_MS || DEFAULT_USAGE_RETRY_DELAY_MS),
        browserTimeoutMs: Number(env.OPL_VERIFY_BROWSER_TIMEOUT_MS || DEFAULT_BROWSER_TIMEOUT_MS),
        modelTimeoutMs: Number(env.OPL_VERIFY_MODEL_TIMEOUT_MS || DEFAULT_MODEL_TIMEOUT_MS),
        requestTimeoutMs: Number(env.OPL_VERIFY_REQUEST_TIMEOUT_MS || DEFAULT_REQUEST_TIMEOUT_MS),
        fetchImpl,
        browserFactory,
        now
      });
      stdout.write(`${JSON.stringify(result, null, 2)}\n`);
      return 0;
    }
    if (args["basic-customer-canary"] === "true") {
      const fundingMode = args["funding-mode"] || BASIC_CANARY_OPERATOR_PRECHARGE_MODE;
      if (![BASIC_CANARY_OPERATOR_PRECHARGE_MODE, BASIC_CANARY_PRECHARGE_RECOVERY_MODE].includes(fundingMode)) {
        throw new Error("production_basic_canary_funding_mode_invalid");
      }
      for (const flag of ["allow-workspace-purchase", "allow-model-write"]) {
        if (args[flag] !== "true") throw new Error("production_basic_canary_write_allow_flags_required");
      }
      if (fundingMode === BASIC_CANARY_OPERATOR_PRECHARGE_MODE) {
        for (const flag of ["allow-account-provision", "allow-wallet-recharge"]) {
          if (args[flag] !== "true") throw new Error("production_basic_canary_write_allow_flags_required");
        }
        if (args["allow-existing-precharge-recovery"]) throw new Error("production_basic_canary_write_allow_flags_required");
      } else if (args["allow-existing-precharge-recovery"] !== "true" || args["allow-account-provision"] || args["allow-wallet-recharge"]) {
        throw new Error("production_basic_canary_write_allow_flags_required");
      }
      const phase = args.phase || "all";
      if (!new Set(["all", "prepare", "complete"]).has(phase)) throw new Error("production_basic_canary_phase_invalid");
      let preparedEvidence;
      if (phase === "complete") {
        const preparedPath = String(args["prepared-evidence"] || "");
        if (!preparedPath.startsWith("/")) throw new Error("production_basic_canary_prepared_evidence_invalid");
        try {
          preparedEvidence = JSON.parse(await readFile(preparedPath, "utf8"));
        } catch {
          throw new Error("production_basic_canary_prepared_evidence_invalid");
        }
      }
      const kubeconfigPath = env.KUBECONFIG || env.TENCENT_DEPLOY_KUBECONFIG_PATH;
      const namespace = env.OPL_K8S_NAMESPACE || "opl-cloud";
      let cloudEvidenceReader = cloudRevisionEvidenceReader;
      let podEvidenceReader = runtimePodEvidenceReader;
      if (phase !== "complete" && (!cloudEvidenceReader || !podEvidenceReader)) {
        if (!String(kubeconfigPath || "").startsWith("/")) throw new Error("production_basic_canary_runtime_pod_config_invalid");
      }
      if (phase !== "complete" && !cloudEvidenceReader) {
        cloudEvidenceReader = (input) => readBasicCanaryCloudRevisionEvidence({ ...input, kubeconfigPath, namespace, execFileImpl });
      }
      if (phase !== "complete" && !podEvidenceReader) {
        podEvidenceReader = (input) => readBasicCanaryRuntimePodEvidence({ ...input, kubeconfigPath, namespace, execFileImpl });
      }
      const result = await verifyProductionBasicCustomerCanary({
        origin: args.origin || env.OPL_CONSOLE_ORIGIN,
        fabricOrigin: env.OPL_FABRIC_INTERNAL_ORIGIN,
        internalServiceToken: env.OPL_INTERNAL_SERVICE_TOKEN,
        adminEmail: env.OPL_SUB2API_ADMIN_EMAIL,
        adminPassword: env.OPL_SUB2API_ADMIN_PASSWORD,
        customerPassword: env.OPL_BASIC_CANARY_CUSTOMER_PASSWORD,
        approvalJson: env.OPL_BASIC_CANARY_APPROVAL_JSON,
        approvalId: args["approval-id"] || "",
        fundingMode,
        confirmation: env.OPL_BASIC_CANARY_CONFIRMATION,
        mergedSha: env.OPL_MERGED_SHA,
        runId: args["run-id"] || env.OPL_VERIFY_RUN_ID,
        launchPollAttempts: Number(env.OPL_VERIFY_LAUNCH_POLL_ATTEMPTS || 180),
        launchPollDelayMs: Number(env.OPL_VERIFY_LAUNCH_POLL_DELAY_MS || 10_000),
        usageAttempts: Number(env.OPL_VERIFY_USAGE_ATTEMPTS || DEFAULT_USAGE_ATTEMPTS),
        usageRetryDelayMs: Number(env.OPL_VERIFY_USAGE_RETRY_DELAY_MS || DEFAULT_USAGE_RETRY_DELAY_MS),
        browserTimeoutMs: Number(env.OPL_VERIFY_BROWSER_TIMEOUT_MS || DEFAULT_BROWSER_TIMEOUT_MS),
        modelTimeoutMs: Number(env.OPL_VERIFY_MODEL_TIMEOUT_MS || DEFAULT_MODEL_TIMEOUT_MS),
        requestTimeoutMs: Number(env.OPL_VERIFY_REQUEST_TIMEOUT_MS || DEFAULT_REQUEST_TIMEOUT_MS),
        fetchImpl,
        fabricFetchImpl,
        browserFactory,
        cloudRevisionEvidenceReader: cloudEvidenceReader,
        runtimePodEvidenceReader: podEvidenceReader,
        phase,
        preparedEvidence,
        checkpointPath: env.OPL_BASIC_CANARY_CHECKPOINT_PATH,
        now
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
    if (workspaceLaunchReadbackMode) {
      const artifact = blockedWorkspaceLaunchReadbackArtifact(workspaceLaunchReadbackMode);
      stdout.write(`${JSON.stringify(artifact, null, 2)}\n`);
      stderr.write(`${JSON.stringify({ ok: false, errorCode: artifact.errorCode }, null, 2)}\n`);
      return 1;
    }
    if (computeClaimMode) {
      const artifact = blockedComputeClaimArtifact(computeClaimMode);
      stdout.write(`${JSON.stringify(artifact, null, 2)}\n`);
      stderr.write(`${JSON.stringify({ ok: false, errorCode: artifact.errorCode }, null, 2)}\n`);
      return 1;
    }
    stderr.write(`${JSON.stringify({ ok: false, error: error.message }, null, 2)}\n`);
    return 1;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] || "").href) {
  runProductionLiveQaCli().then((code) => { process.exitCode = code; });
}
