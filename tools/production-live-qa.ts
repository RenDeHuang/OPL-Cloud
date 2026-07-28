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
    storage?.resourceType !== "storage" || storage?.resourceId !== expected.storageId || storage?.sizeGb !== 10 ||
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
    totalUsdMicros: quote.totalChargeUsdMicros
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
  browserFactory
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
    stdout.write("Usage: node tools/production-live-qa.ts --read-only\nA fixed-slot model request requires --allow-gateway-write --allow-model-write. A real customer canary requires --basic-customer-canary, an exact funding mode, and its explicit approvals.\n");
    return 0;
  }
  try {
    if (env.OPL_VERIFY_MODEL_ACCESS_KEY) throw new Error("production_live_qa_raw_key_forbidden");
    const args = cliArgs(argv);
    if (args["read-only"] === "true") {
      if (args["basic-customer-canary"] || args["allow-gateway-write"] || args["allow-model-write"] || args["approval-id"]) throw new Error("production_live_qa_read_only_conflict");
      const result = await verifyProductionReadOnlyRollout({
        origin: args.origin || env.OPL_CONSOLE_ORIGIN,
        adminEmail: env.OPL_SUB2API_ADMIN_EMAIL,
        adminPassword: env.OPL_SUB2API_ADMIN_PASSWORD,
        requestTimeoutMs: Number(args["request-timeout-ms"] || env.OPL_VERIFY_REQUEST_TIMEOUT_MS || DEFAULT_REQUEST_TIMEOUT_MS),
        browserFactory,
        fetchImpl
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
    stderr.write(`${JSON.stringify({ ok: false, error: error.message }, null, 2)}\n`);
    return 1;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] || "").href) {
  runProductionLiveQaCli().then((code) => { process.exitCode = code; });
}
