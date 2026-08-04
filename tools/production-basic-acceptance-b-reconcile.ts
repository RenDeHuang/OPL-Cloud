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

function validDigest(value) {
  return /^[0-9a-f]{64}$/.test(String(value || ""));
}

function validStatus(value) {
  return ["prepared", "safe_to_retry_absent", "partial", "manual_review", "unknown"].includes(value);
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
    "receiptCount", "writeCounts", "runnerDirectMutationCounts"
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
  return cloneJson(value);
}

function blockedProductionBasicAcceptanceBReconcileArtifact(errorCode = "acceptance_b_account_reconcile_failed", mergedSha = "") {
  const safeCode = /^acceptance_b_account_reconcile_[a-z0-9_]+$/.test(String(errorCode)) ? String(errorCode) : "acceptance_b_account_reconcile_failed";
  return {
    schemaVersion: 1,
    operationMode: PRODUCTION_BASIC_ACCEPTANCE_B_RECONCILE_MODE,
    status: "unknown",
    mergedMainSha: mergedSha,
    customerIdentitySha256: "0".repeat(64),
    accountProvisionIdentitySha256: "0".repeat(64),
    walletAdjustmentIdentitySha256: "0".repeat(64),
    localGraph: "unknown",
    remoteIdentity: "unknown",
    customerLogin: "unknown",
    wallet: "unknown",
    walletAdjustment: "unknown",
    workspaceCount: 0,
    launchCount: 0,
    keyCount: 0,
    receiptCount: 0,
    writeCounts: { ...ZERO_WRITE_COUNTS },
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    errorCode: safeCode
  };
}

function withoutSensitiveError(error) {
  const code = String(error?.message || "acceptance_b_account_reconcile_failed").split(":", 1)[0];
  return /^acceptance_b_account_reconcile_[a-z0-9_]+$/.test(code) ? code : "acceptance_b_account_reconcile_failed";
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

async function readZeroBaseline(requestOptions, customerAuth) {
  const workspaces = sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/workspaces?page=1&pageSize=50" }), "control-plane", true).data;
  const launchesRaw = (await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/workspace-launches" })).payload;
  const keys = await readFullKeys(requestOptions, customerAuth);
  const receipts = sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/billing/receipts?limit=100" }), "ledger", true).data;
  if (!Array.isArray(workspaces?.items) || workspaces.total !== 0 || workspaces.items.length !== 0 ||
    !Array.isArray(launchesRaw) || launchesRaw.length !== 0 || keys.length !== 0 ||
    !Array.isArray(receipts?.receipts) || receipts.receipts.length !== 0 || receipts.hasMore !== false) {
    throw new Error("acceptance_b_account_reconcile_baseline_not_zero");
  }
  return { workspaceCount: 0, launchCount: 0, keyCount: 0, receiptCount: 0 };
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
    throw new Error("acceptance_b_account_reconcile_config_invalid");
  }
  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  const requestOptions = { fetchImpl, origin: normalizedOrigin, timeoutMs: requestTimeoutMs };
  const adminAuth = await login({ ...requestOptions, email: adminEmail, password: adminPassword });
  if (adminAuth.user?.accountId !== "acct-admin" || adminAuth.user?.role !== "admin") throw new Error("acceptance_b_account_reconcile_admin_login_failed");
  const route = sourceEnvelope(await requestJson({
    ...requestOptions,
    auth: adminAuth,
    path: "/api/operator/account-reconciliation",
    headers: { [PRODUCTION_BASIC_ACCEPTANCE_B_RECONCILE_HEADER]: normalizedEmail }
  }), "control-plane+sub2api+ledger");
  const data = redactedRouteData(route.data);
  const baseline = { workspaceCount: Number(data.workspaceCount || 0), launchCount: Number(data.launchCount || 0), keyCount: Number(data.keyCount || 0), receiptCount: Number(data.receiptCount || 0) };
  let customerLogin = data.status === "safe_to_retry_absent" ? "not_attempted" : "unknown";
  if (data.remoteIdentity === "active") {
    try {
      const customerAuth = await login({ ...requestOptions, email: normalizedEmail, password: String(customerPassword) });
      const identity = sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/auth/me" }), "sub2api").data;
      if (identity?.email !== normalizedEmail || identity?.role !== "owner" || identity?.status !== "active") throw new Error("acceptance_b_account_reconcile_customer_identity_invalid");
      customerLogin = "active";
      const fresh = await readZeroBaseline(requestOptions, customerAuth);
      baseline.workspaceCount = fresh.workspaceCount;
      baseline.launchCount = fresh.launchCount;
      baseline.keyCount = fresh.keyCount;
      baseline.receiptCount = fresh.receiptCount;
    } catch {
      customerLogin = "failed";
    }
  }
  let status = data.status;
  if (customerLogin === "failed") status = "unknown";
  if (status === "prepared" && (customerLogin !== "active" || Object.values(baseline).some((count) => count !== 0))) status = "unknown";
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
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 }
  };
  // The optional balance field is intentionally accepted only as a decimal
  // amount; no account, user, operation, or credential identity is emitted.
  if (result.wallet === "available" && !/^(0|[1-9][0-9]*)$/.test(result.walletUsdMicros)) throw new Error("acceptance_b_account_reconcile_wallet_invalid");
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
    return result.status === "prepared" || result.status === "safe_to_retry_absent" ? 0 : 1;
  } catch (error) {
    const artifact = blockedProductionBasicAcceptanceBReconcileArtifact(withoutSensitiveError(error), mergedSha);
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
