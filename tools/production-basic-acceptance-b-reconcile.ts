import { createHash } from "node:crypto";
import { writeFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";

import {
  assertPublicHttpsUrl,
  login,
  requestJson,
  sourceEnvelope
} from "./production-verifier.ts";

export const PRODUCTION_BASIC_ACCEPTANCE_B_RECONCILE_MODE = "acceptance_b_account_reconcile";
export const PRODUCTION_BASIC_ACCEPTANCE_B_RECONCILE_HEADER = "X-OPL-Account-Reconcile-Email";

const PAGE_SIZE = 50;
const MAX_PAGES = 1000;
const ZERO_DIGEST = "0".repeat(64);
const FAILURE_STAGES = Object.freeze([
  "none",
  "config",
  "admin_login",
  "route_request",
  "response_envelope",
  "customer_login",
  "local_graph",
  "remote_identity",
  "wallet",
  "wallet_adjustment",
  "baseline",
  "artifact_schema"
]);
const FAILURE_STAGE_SET = new Set(FAILURE_STAGES);
export const SUCCESS_STATUSES = new Set(["prepared", "safe_to_retry_absent"]);
const SAFE_READBACK_ERROR = /^(?:none|[a-z0-9_]{1,80})$/;
const SAFE_ERROR_CODE = /^(?:none|acceptance_b_account_reconcile_[a-z0-9_]{1,80})$/;
const ZERO_WRITE_COUNTS = Object.freeze({
  accountProvisionPosts: 0,
  walletAdjustmentPosts: 0,
  workspaceLaunchPosts: 0,
  sub2apiDebits: 0,
  tencentCvmCreates: 0,
  kubernetesNodeClaims: 0,
  tencentCbsCreates: 0,
  runtimeCreates: 0,
  receiptCreates: 0,
  modelRequests: 0,
  refunds: 0,
  renewals: 0,
  deletes: 0,
  replacements: 0
});

function cloneJson(value) {
  return JSON.parse(JSON.stringify(value));
}

function sha256(value) {
  return createHash("sha256").update(String(value || "")).digest("hex");
}

function validDigest(value) {
  return /^[0-9a-f]{64}$/.test(String(value || ""));
}

function validStatus(value) {
  return ["prepared", "safe_to_retry_absent", "partial", "manual_review", "unknown"].includes(value);
}

function validFailureStage(value) {
  return FAILURE_STAGE_SET.has(String(value || ""));
}

function validReadbackError(value) {
  return SAFE_READBACK_ERROR.test(String(value || ""));
}

function validErrorCode(value) {
  return SAFE_ERROR_CODE.test(String(value || ""));
}

function inferFailureStage(readbackError) {
  const stageByError = {
    local_authority_unavailable: "local_graph",
    sub2api_authority_unavailable: "remote_identity",
    wallet_authority_unavailable: "wallet",
    wallet_adjustment_authority_invalid: "wallet_adjustment",
    workspace_authority_unavailable: "baseline",
    launch_authority_unavailable: "baseline",
    key_authority_unavailable: "baseline",
    ledger_authority_unavailable: "baseline"
  };
  return stageByError[String(readbackError || "")] || "artifact_schema";
}

function failureStageForRouteData(data, readbackError) {
  if (validFailureStage(data?.failureStage) && (data.failureStage !== "none" || SUCCESS_STATUSES.has(data?.status))) {
    return String(data.failureStage);
  }
  if (readbackError !== "none") return inferFailureStage(readbackError);
  if (SUCCESS_STATUSES.has(data?.status)) return "none";
  if (data?.status === "manual_review" && data?.walletAdjustment !== "succeeded") return "wallet_adjustment";
  if (data?.localGraph !== "complete") return "local_graph";
  if (data?.remoteIdentity !== "active") return "remote_identity";
  if (["workspaceCount", "launchCount", "keyCount", "receiptCount"].some((key) => Number(data?.[key] || 0) !== 0)) return "baseline";
  return "artifact_schema";
}

function errorCodeFor(status, failureStage, preferred = "") {
  if (SUCCESS_STATUSES.has(status)) return "none";
  if (validErrorCode(preferred) && preferred !== "none") return String(preferred);
  if (status === "unknown") return "acceptance_b_account_reconcile_unknown";
  const stage = validFailureStage(failureStage) && failureStage !== "none" ? failureStage : "unknown";
  return `acceptance_b_account_reconcile_${stage}`;
}

function reconcileFailure(message, failureStage, { readbackError = "none", responseReceived = false } = {}) {
  const error = new Error(message);
  error.failureStage = validFailureStage(failureStage) ? failureStage : "artifact_schema";
  error.readbackError = validReadbackError(readbackError) ? String(readbackError) : "none";
  error.responseReceived = responseReceived;
  return error;
}

function redactedRouteData(value) {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("acceptance_b_account_reconcile_readback_invalid");
  const forbidden = /(?:email|accountid|userid|operationid|workspaceid|resourceid|password|secret|token|cookie|csrf|providerrequestid)/i;
  const walk = (nested) => {
    if (!nested || typeof nested !== "object") return;
    for (const [key, child] of Object.entries(nested)) {
      if (forbidden.test(key)) throw new Error("acceptance_b_account_reconcile_readback_invalid");
      walk(child);
    }
  };
  walk(value);
  return value;
}

export function validateProductionBasicAcceptanceBReconcileReadback(value, { mergedSha } = {}) {
  const keys = [
    "schemaVersion", "operationMode", "status", "mergedMainSha", "customerIdentitySha256",
    "accountProvisionIdentitySha256", "walletAdjustmentIdentitySha256", "localGraph", "remoteIdentity",
    "customerLogin", "wallet", "walletUsdMicros", "walletAdjustment", "workspaceCount", "launchCount", "keyCount",
    "receiptCount", "writeCounts", "runnerDirectMutationCounts", "failureStage", "readbackError", "errorCode"
  ];
  redactedRouteData(value);
  if (!value || typeof value !== "object" || Array.isArray(value) ||
    Object.keys(value).sort().join("\0") !== [...keys].sort().join("\0") ||
    value.schemaVersion !== 1 || value.operationMode !== PRODUCTION_BASIC_ACCEPTANCE_B_RECONCILE_MODE ||
    !validStatus(value.status) || value.mergedMainSha !== mergedSha || !/^[0-9a-f]{40}$/.test(value.mergedMainSha) ||
    !validDigest(value.customerIdentitySha256) || !validDigest(value.accountProvisionIdentitySha256) ||
    !validDigest(value.walletAdjustmentIdentitySha256) ||
    !["absent", "partial", "complete", "unknown"].includes(value.localGraph) ||
    !["absent", "active", "disabled", "ambiguous", "unknown"].includes(value.remoteIdentity) ||
    !["not_attempted", "active", "failed", "unknown"].includes(value.customerLogin) ||
    !["available", "absent", "unknown"].includes(value.wallet) ||
    !["succeeded", "absent", "manual_review", "unknown"].includes(value.walletAdjustment) ||
    !validFailureStage(value.failureStage) || !validReadbackError(value.readbackError) || !validErrorCode(value.errorCode) ||
    !Number.isSafeInteger(value.workspaceCount) || value.workspaceCount < 0 ||
    !Number.isSafeInteger(value.launchCount) || value.launchCount < 0 ||
    !Number.isSafeInteger(value.keyCount) || value.keyCount < 0 ||
    !Number.isSafeInteger(value.receiptCount) || value.receiptCount < 0 ||
    JSON.stringify(value.writeCounts) !== JSON.stringify(ZERO_WRITE_COUNTS) ||
    JSON.stringify(value.runnerDirectMutationCounts) !== JSON.stringify({ sub2api: 0, tencent: 0, kubernetes: 0 })) {
    throw new Error("acceptance_b_account_reconcile_readback_invalid");
  }
  if (value.wallet === "available" && (!/^(0|[1-9][0-9]*)$/.test(String(value.walletUsdMicros || "")))) {
    throw new Error("acceptance_b_account_reconcile_readback_invalid");
  }
  if (SUCCESS_STATUSES.has(value.status) && (value.failureStage !== "none" || value.readbackError !== "none" || value.errorCode !== "none")) {
    throw new Error("acceptance_b_account_reconcile_readback_invalid");
  }
  if (!SUCCESS_STATUSES.has(value.status) && value.errorCode === "none") {
    throw new Error("acceptance_b_account_reconcile_readback_invalid");
  }
  if (!SUCCESS_STATUSES.has(value.status) && value.failureStage === "none") {
    throw new Error("acceptance_b_account_reconcile_readback_invalid");
  }
  return cloneJson(value);
}

function blockedProductionBasicAcceptanceBReconcileArtifact({
  errorCode = "acceptance_b_account_reconcile_failed",
  failureStage = "artifact_schema",
  readbackError = "none",
  mergedSha = "",
  zeroDigests = true,
  digests = {},
  routeData = null,
  responseDigest = ""
} = {}) {
  const safeCode = validErrorCode(errorCode) && errorCode !== "none" ? String(errorCode) : "acceptance_b_account_reconcile_failed";
  const safeStage = validFailureStage(failureStage) && failureStage !== "none" ? failureStage : "artifact_schema";
  const safeReadbackError = validReadbackError(readbackError) ? String(readbackError) : "none";
  const source = routeData && typeof routeData === "object" && !Array.isArray(routeData) ? routeData : {};
  const summaryDigest = validDigest(responseDigest) ? responseDigest : "";
  const digest = (key) => {
    if (zeroDigests) return ZERO_DIGEST;
    if (validDigest(digests[key])) return String(digests[key]);
    if (validDigest(source[key])) return String(source[key]);
    return summaryDigest;
  };
  const status = validStatus(source.status) ? String(source.status) : "unknown";
  const artifactStatus = safeStage !== "none" && SUCCESS_STATUSES.has(status) ? "unknown" : status;
  const localGraph = ["absent", "partial", "complete", "unknown"].includes(source.localGraph) ? source.localGraph : "unknown";
  const remoteIdentity = ["absent", "active", "disabled", "ambiguous", "unknown"].includes(source.remoteIdentity) ? source.remoteIdentity : "unknown";
  const customerLogin = ["not_attempted", "active", "failed", "unknown"].includes(source.customerLogin) ? source.customerLogin : "unknown";
  const wallet = ["available", "absent", "unknown"].includes(source.wallet) ? source.wallet : "unknown";
  const walletAdjustment = ["succeeded", "absent", "manual_review", "unknown"].includes(source.walletAdjustment) ? source.walletAdjustment : "unknown";
  const count = (key) => Number.isSafeInteger(source[key]) && source[key] >= 0 ? source[key] : 0;
  const walletUsdMicros = wallet === "available" && /^(0|[1-9][0-9]*)$/.test(String(source.walletUsdMicros || ""))
    ? String(source.walletUsdMicros) : "";
  return {
    schemaVersion: 1,
    operationMode: PRODUCTION_BASIC_ACCEPTANCE_B_RECONCILE_MODE,
    status: artifactStatus,
    mergedMainSha: mergedSha,
    customerIdentitySha256: digest("customerIdentitySha256"),
    accountProvisionIdentitySha256: digest("accountProvisionIdentitySha256"),
    walletAdjustmentIdentitySha256: digest("walletAdjustmentIdentitySha256"),
    localGraph,
    remoteIdentity,
    customerLogin,
    wallet,
    walletUsdMicros,
    walletAdjustment,
    workspaceCount: count("workspaceCount"),
    launchCount: count("launchCount"),
    keyCount: count("keyCount"),
    receiptCount: count("receiptCount"),
    writeCounts: { ...ZERO_WRITE_COUNTS },
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    failureStage: safeStage,
    readbackError: safeReadbackError,
    errorCode: safeCode
  };
}

function withoutSensitiveError(error) {
  const code = String(error?.message || "acceptance_b_account_reconcile_failed").split(":", 1)[0];
  return validErrorCode(code) && code !== "none" ? code : "acceptance_b_account_reconcile_failed";
}

async function readFullKeys(requestOptions, customerAuth) {
  const pages = [];
  for (let page = 1; page <= MAX_PAGES; page += 1) {
    const data = sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: `/api/gateway/keys?page=${page}&pageSize=${PAGE_SIZE}` }), "sub2api", true).data;
    if (!Array.isArray(data?.items) || !Number.isSafeInteger(data?.total) || data.total < 0 || data.page !== page || data.pageSize !== PAGE_SIZE) {
      throw new Error("acceptance_b_account_reconcile_keys_invalid");
    }
    pages.push(data);
    const expectedPages = Math.max(1, Math.ceil(data.total / PAGE_SIZE));
    const expectedItems = page < expectedPages ? PAGE_SIZE : data.total - (page - 1) * PAGE_SIZE;
    if (data.items.length !== Math.max(0, expectedItems)) throw new Error("acceptance_b_account_reconcile_keys_invalid");
    if (page >= expectedPages) return pages.flatMap((item) => item.items);
  }
  throw new Error("acceptance_b_account_reconcile_keys_invalid");
}

async function readBaseline(requestOptions, customerAuth) {
  const workspaces = sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/workspaces?page=1&pageSize=50" }), "control-plane", true).data;
  const launchesRaw = (await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/workspace-launches" })).payload;
  const keys = await readFullKeys(requestOptions, customerAuth);
  const receipts = sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/billing/receipts?limit=100" }), "ledger", true).data;
  if (!Array.isArray(workspaces?.items) || !Number.isSafeInteger(workspaces.total) || workspaces.total < 0 ||
    !Array.isArray(launchesRaw) || !Array.isArray(receipts?.receipts) || receipts.hasMore !== false) {
    throw new Error("acceptance_b_account_reconcile_baseline_unavailable");
  }
  return { workspaceCount: workspaces.total, launchCount: launchesRaw.length, keyCount: keys.length, receiptCount: receipts.receipts.length };
}

export async function reconcileProductionBasicAcceptanceBAccount(options = {}) {
  const {
    origin,
    adminEmail,
    adminPassword,
    customerEmail,
    customerPassword,
    mergedSha,
    requestTimeoutMs = 30_000,
    fetchImpl = globalThis.fetch,
    now = new Date()
  } = options;
  const normalizedEmail = String(customerEmail || "").trim().toLowerCase();
  if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(normalizedEmail) || !String(customerPassword || "") ||
    !String(adminEmail || "") || !String(adminPassword || "") || !/^[0-9a-f]{40}$/.test(String(mergedSha || "")) || !(now instanceof Date) || Number.isNaN(now.getTime())) {
    throw reconcileFailure("acceptance_b_account_reconcile_config_invalid", "config");
  }
  let normalizedOrigin;
  try {
    normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  } catch {
    throw reconcileFailure("acceptance_b_account_reconcile_config_invalid", "config");
  }
  const requestOptions = { fetchImpl, origin: normalizedOrigin, timeoutMs: requestTimeoutMs };
  let adminAuth;
  try {
    adminAuth = await login({ ...requestOptions, email: adminEmail, password: adminPassword });
    if (adminAuth.user?.accountId !== "acct-admin" || adminAuth.user?.role !== "admin") {
      throw new Error("acceptance_b_account_reconcile_admin_login_failed");
    }
  } catch (error) {
    if (error?.failureStage) throw error;
    throw reconcileFailure("acceptance_b_account_reconcile_admin_login_failed", "admin_login");
  }

  let routeResponseReceived = false;
  let routeResponseDigest = "";
  let routeResponsePayload = null;
  let route;
  try {
    const routeFetchImpl = async (...args) => {
      const response = await fetchImpl(...args);
      routeResponseReceived = true;
      try {
        const body = await response.clone().text();
        routeResponseDigest = sha256(body);
        routeResponsePayload = body ? JSON.parse(body) : null;
      } catch {
        routeResponseDigest = sha256(`${response.status || 0}`);
        routeResponsePayload = null;
      }
      return response;
    };
    route = sourceEnvelope(await requestJson({
      ...requestOptions,
      fetchImpl: routeFetchImpl,
      auth: adminAuth,
      path: "/api/operator/account-reconciliation",
      headers: { [PRODUCTION_BASIC_ACCEPTANCE_B_RECONCILE_HEADER]: normalizedEmail }
    }), "control-plane+sub2api+ledger");
  } catch (error) {
    const failureStage = routeResponseReceived ? "response_envelope" : "route_request";
    const errorCode = routeResponseReceived
      ? "acceptance_b_account_reconcile_response_envelope_invalid"
      : "acceptance_b_account_reconcile_route_request_failed";
    const candidateReadbackError = routeResponsePayload?.data?.readbackError;
    const failure = reconcileFailure(errorCode, failureStage, {
      readbackError: validReadbackError(candidateReadbackError) ? candidateReadbackError : "none",
      responseReceived: routeResponseReceived
    });
    failure.routeData = routeResponsePayload?.data;
    failure.responseDigest = routeResponseDigest;
    throw failure;
  }
  let data;
  try {
    data = redactedRouteData(route.data);
  } catch {
    const failure = reconcileFailure("acceptance_b_account_reconcile_response_envelope_invalid", "response_envelope", {
      readbackError: validReadbackError(route.data?.readbackError) ? route.data.readbackError : "none",
      responseReceived: true
    });
    failure.routeData = route.data;
    failure.responseDigest = routeResponseDigest;
    throw failure;
  }
  const serverReadbackError = data.readbackError === undefined || data.readbackError === "" ? "none" : String(data.readbackError);
  const serverFailureStage = failureStageForRouteData(data, serverReadbackError);
  if (!validReadbackError(serverReadbackError) || !validFailureStage(serverFailureStage)) {
    const failure = reconcileFailure("acceptance_b_account_reconcile_response_envelope_invalid", "response_envelope", {
      readbackError: "none",
      responseReceived: true
    });
    failure.routeData = data;
    failure.responseDigest = routeResponseDigest;
    throw failure;
  }
  const baseline = { workspaceCount: Number(data.workspaceCount || 0), launchCount: Number(data.launchCount || 0), keyCount: Number(data.keyCount || 0), receiptCount: Number(data.receiptCount || 0) };
  let customerLogin = data.status === "safe_to_retry_absent" ? "not_attempted" : "unknown";
  let failureStage = serverFailureStage;
  let readbackError = serverReadbackError;
  let baselineReadbackUnknown = false;
  if (data.status !== "unknown" && data.remoteIdentity === "active") {
    let customerAuth;
    try {
      customerAuth = await login({ ...requestOptions, email: normalizedEmail, password: String(customerPassword) });
      const identity = sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/auth/me" }), "sub2api").data;
      if (identity?.email !== normalizedEmail || identity?.role !== "owner" || identity?.status !== "active") throw new Error("acceptance_b_account_reconcile_customer_identity_invalid");
      customerLogin = "active";
    } catch {
      customerLogin = "failed";
      failureStage = "customer_login";
      readbackError = "customer_login_failed";
    }
    if (customerLogin === "active") {
      try {
        const current = await readBaseline(requestOptions, customerAuth);
        baseline.workspaceCount = current.workspaceCount;
        baseline.launchCount = current.launchCount;
        baseline.keyCount = current.keyCount;
        baseline.receiptCount = current.receiptCount;
      } catch {
        baselineReadbackUnknown = true;
        failureStage = "baseline";
        readbackError = "baseline_authority_unavailable";
      }
    }
  }
  let status = data.status;
  if (customerLogin === "failed") status = "unknown";
  if (baselineReadbackUnknown) status = "unknown";
  if (customerLogin === "active" && Object.values(baseline).some((count) => count !== 0)) {
    failureStage = "baseline";
    readbackError = "baseline_not_zero";
  }
  if (status === "manual_review" && data.localGraph === "complete" && data.remoteIdentity === "active" &&
    customerLogin === "active" && data.wallet === "available" && data.walletAdjustment === "absent" &&
    Object.values(baseline).every((count) => count === 0) && readbackError === "none") {
    status = "safe_to_retry_absent";
  }
  if (status === "prepared" && (customerLogin !== "active" || Object.values(baseline).some((count) => count !== 0))) {
    status = "unknown";
  }
  if (SUCCESS_STATUSES.has(status)) {
    failureStage = "none";
    readbackError = "none";
  }
  const result = {
    schemaVersion: 1,
    operationMode: PRODUCTION_BASIC_ACCEPTANCE_B_RECONCILE_MODE,
    status,
    mergedMainSha: String(mergedSha),
    customerIdentitySha256: String(data.customerIdentitySha256 || ""),
    accountProvisionIdentitySha256: String(data.accountProvisionIdentitySha256 || ""),
    walletAdjustmentIdentitySha256: String(data.walletAdjustmentIdentitySha256 || ""),
    localGraph: String(data.localGraph || "unknown"),
    remoteIdentity: String(data.remoteIdentity || "unknown"),
    customerLogin,
    wallet: String(data.wallet || "unknown"),
    walletUsdMicros: String(data.walletUsdMicros || ""),
    walletAdjustment: String(data.walletAdjustment || "unknown"),
    workspaceCount: baseline.workspaceCount,
    launchCount: baseline.launchCount,
    keyCount: baseline.keyCount,
    receiptCount: baseline.receiptCount,
    writeCounts: { ...ZERO_WRITE_COUNTS },
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    failureStage,
    readbackError,
    errorCode: errorCodeFor(status, failureStage, data.errorCode)
  };
  // The optional balance field is intentionally accepted only as a decimal
  // amount; no account, user, operation, or credential identity is emitted.
  if (result.wallet === "available" && !/^(0|[1-9][0-9]*)$/.test(result.walletUsdMicros)) {
    result.status = "unknown";
    result.wallet = "unknown";
    result.walletUsdMicros = "";
    result.failureStage = "wallet";
    result.readbackError = "wallet_invalid";
    result.errorCode = "acceptance_b_account_reconcile_unknown";
  }
  try {
    validateProductionBasicAcceptanceBReconcileReadback(result, { mergedSha });
  } catch (error) {
    error.responseReceived = true;
    error.failureStage = "artifact_schema";
    error.readbackError = validReadbackError(result.readbackError) ? result.readbackError : "none";
    error.routeData = result;
    error.responseDigest = routeResponseDigest;
    throw error;
  }
  return result;
}

export async function runProductionBasicAcceptanceBReconcileCli({
  argv = process.argv.slice(2),
  env = process.env,
  stdout = process.stdout,
  stderr = process.stderr,
  fetchImpl = globalThis.fetch,
  now = new Date()
} = {}) {
  if (!argv.includes("--reconcile-account")) return 2;
  const mergedSha = String(env.OPL_MERGED_SHA || "");
  try {
    const result = await reconcileProductionBasicAcceptanceBAccount({
      origin: env.OPL_CONSOLE_ORIGIN,
      adminEmail: env.OPL_SUB2API_ADMIN_EMAIL,
      adminPassword: env.OPL_SUB2API_ADMIN_PASSWORD,
      customerEmail: env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_EMAIL,
      customerPassword: env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_PASSWORD,
      mergedSha,
      requestTimeoutMs: Number(env.OPL_VERIFY_REQUEST_TIMEOUT_MS || 30_000),
      fetchImpl,
      now
    });
    validateProductionBasicAcceptanceBReconcileReadback(result, { mergedSha });
    const artifactPath = String(env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_RECONCILE_ARTIFACT_PATH || "");
    if (artifactPath) await writeFile(artifactPath, `${JSON.stringify(result, null, 2)}\n`, { mode: 0o600 });
    stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return SUCCESS_STATUSES.has(result.status) ? 0 : 1;
  } catch (error) {
    const hasServerResponse = error?.responseReceived === true;
    const artifact = blockedProductionBasicAcceptanceBReconcileArtifact({
      errorCode: withoutSensitiveError(error),
      failureStage: error?.failureStage || (hasServerResponse ? "response_envelope" : "artifact_schema"),
      readbackError: error?.readbackError || "none",
      mergedSha,
      zeroDigests: !hasServerResponse,
      routeData: error?.routeData,
      responseDigest: error?.responseDigest
    });
    validateProductionBasicAcceptanceBReconcileReadback(artifact, { mergedSha });
    const artifactPath = String(env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_RECONCILE_ARTIFACT_PATH || "");
    if (artifactPath) await writeFile(artifactPath, `${JSON.stringify(artifact, null, 2)}\n`, { mode: 0o600 });
    stdout.write(`${JSON.stringify(artifact, null, 2)}\n`);
    stderr.write(`${JSON.stringify({ ok: false, errorCode: artifact.errorCode })}\n`);
    return 1;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] || "").href) {
  runProductionBasicAcceptanceBReconcileCli().then((code) => { process.exitCode = code; });
}
