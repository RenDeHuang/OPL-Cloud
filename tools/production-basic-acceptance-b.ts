import { createHash } from "node:crypto";

export const PRODUCTION_BASIC_ACCEPTANCE_B_CONFIRMATION = "RUN_ONE_INDEPENDENT_FRESH_BASIC_ORDER_FOR_ACCEPTANCE_B";

export const PRODUCTION_BASIC_ACCEPTANCE_B_ALLOWED_WRITES = Object.freeze([
  "submit_one_workspace_launch",
  "debit_one_basic_month",
  "create_one_workspace_key",
  "create_one_cvm",
  "claim_one_node",
  "create_one_cbs",
  "create_one_attachment",
  "upsert_one_gateway_secret",
  "create_one_runtime",
  "activate_one_workspace",
  "record_one_purchase_receipt"
]);

export const PRODUCTION_BASIC_ACCEPTANCE_B_FORBIDDEN_WRITES = Object.freeze([
  "provision_account",
  "adjust_wallet",
  "submit_second_workspace_launch",
  "create_second_cvm",
  "create_second_cbs",
  "refund",
  "renew",
  "delete",
  "replace",
  "send_model_request"
]);

export const PRODUCTION_BASIC_ACCEPTANCE_B_OPERATION = Object.freeze({
  schemaVersion: 1,
  operationMode: "acceptance_b_fresh_order",
  confirmation: PRODUCTION_BASIC_ACCEPTANCE_B_CONFIRMATION,
  packageId: "basic",
  sizeGb: 10,
  autoRenew: false,
  workspaceLaunchPostCount: 1,
  exactWrites: Object.freeze({
    sub2apiDebit: 1,
    cvmCreate: 1,
    nodeClaim: 1,
    cbsCreate: 1,
    runtimeCreate: 1,
    receiptCreate: 1
  }),
  terminalEvidence: Object.freeze([
    "launch_succeeded",
    "runtime_ready",
    "receipt_completed",
    "pod_image_id_equals_approved_workspace_image_digest",
    "workspace_url_http_200"
  ])
});

const APPROVAL_KEYS = [
  "schemaVersion",
  "operationMode",
  "approvalId",
  "expiresAt",
  "confirmation",
  "release",
  "customer",
  "launch",
  "expected",
  "allowedWrites",
  "forbiddenWrites"
];

const WRITE_COUNT_KEYS = [
  "workspaceLaunchPosts",
  "sub2apiDebits",
  "tencentCvmCreates",
  "kubernetesNodeClaims",
  "tencentCbsCreates",
  "runtimeCreates",
  "receiptCreates",
  "accountProvisionPosts",
  "walletAdjustmentPosts",
  "modelRequests",
  "refunds",
  "renewals",
  "deletes",
  "replacements"
];

const EXACT_WRITE_COUNTS = Object.freeze({
  workspaceLaunchPosts: 1,
  sub2apiDebits: 1,
  tencentCvmCreates: 1,
  kubernetesNodeClaims: 1,
  tencentCbsCreates: 1,
  runtimeCreates: 1,
  receiptCreates: 1,
  accountProvisionPosts: 0,
  walletAdjustmentPosts: 0,
  modelRequests: 0,
  refunds: 0,
  renewals: 0,
  deletes: 0,
  replacements: 0
});

function exactObjectKeys(value, keys) {
  return value && typeof value === "object" && !Array.isArray(value) &&
    Object.keys(value).sort().join("\u0000") === [...keys].sort().join("\u0000");
}

function exactArray(value, expected) {
  return Array.isArray(value) && value.length === expected.length && value.every((item, index) => item === expected[index]);
}

function cloneJson(value) {
  try {
    return JSON.parse(JSON.stringify(value));
  } catch {
    return null;
  }
}

function stableId(...parts) {
  const hash = createHash("sha1");
  for (const part of parts) {
    hash.update(String(part));
    hash.update(Buffer.from([0]));
  }
  return hash.digest("hex");
}

function expectedLaunchIdentities(accountId, idempotencyKey) {
  const operationId = `workspace-launch-${stableId(accountId, idempotencyKey).slice(0, 18)}`;
  return {
    operationId,
    workspaceId: `ws-${stableId("workspace-launch-v2", accountId, operationId).slice(0, 18)}`
  };
}

function canonicalJson(value) {
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(",")}]`;
  if (!value || typeof value !== "object") return JSON.stringify(value);
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJson(value[key])}`).join(",")}}`;
}

export function productionBasicAcceptanceBApprovalDigest(approval) {
  return createHash("sha256").update(canonicalJson(approval)).digest("hex");
}

export function parseProductionBasicAcceptanceBApproval(value, options = {}) {
  let approval;
  try {
    approval = typeof value === "string" ? JSON.parse(value) : cloneJson(value);
  } catch {
    approval = null;
  }
  const now = options.now instanceof Date ? options.now : new Date(options.now ?? Date.now());
  const expiresAt = Date.parse(String(approval?.expiresAt || ""));
  if (!exactObjectKeys(approval, APPROVAL_KEYS) || Number.isNaN(now.getTime()) || !Number.isFinite(expiresAt) || expiresAt <= now.getTime() ||
    approval.schemaVersion !== 1 || approval.operationMode !== PRODUCTION_BASIC_ACCEPTANCE_B_OPERATION.operationMode ||
    approval.confirmation !== PRODUCTION_BASIC_ACCEPTANCE_B_CONFIRMATION || approval.approvalId !== options.approvalId ||
    !/^[A-Za-z0-9][A-Za-z0-9._:-]{7,199}$/.test(String(approval.approvalId || "")) ||
    !exactObjectKeys(approval.release, ["mergedMainSha", "cloudImageDigest", "workspaceImageDigest"]) ||
    !exactObjectKeys(approval.customer, ["email", "accountId"]) ||
    !exactObjectKeys(approval.launch, ["idempotencyKey", "operationId", "workspaceId", "name", "packageId", "sizeGb", "autoRenew"]) ||
    !exactObjectKeys(approval.expected, ["nodePoolId", "resolvedInstanceType"]) ||
    !exactArray(approval.allowedWrites, PRODUCTION_BASIC_ACCEPTANCE_B_ALLOWED_WRITES) ||
    !exactArray(approval.forbiddenWrites, PRODUCTION_BASIC_ACCEPTANCE_B_FORBIDDEN_WRITES)) {
    throw new Error("production_basic_acceptance_b_approval_invalid");
  }

  const email = String(approval.customer.email || "");
  const accountId = String(approval.customer.accountId || "");
  const idempotencyKey = String(approval.launch.idempotencyKey || "");
  const expected = expectedLaunchIdentities(accountId, idempotencyKey);
  if (email !== email.trim().toLowerCase() || !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email) ||
    !/^acct-[A-Za-z0-9-]+$/.test(accountId) || idempotencyKey !== idempotencyKey.trim() || idempotencyKey.length < 8 || idempotencyKey.length > 200 ||
    approval.launch.operationId !== expected.operationId || approval.launch.workspaceId !== expected.workspaceId ||
    String(approval.launch.name || "") !== String(approval.launch.name || "").trim() || !String(approval.launch.name || "") ||
    approval.launch.packageId !== "basic" || approval.launch.sizeGb !== 10 || approval.launch.autoRenew !== false ||
    !/^[a-f0-9]{40}$/.test(String(approval.release.mergedMainSha || "")) ||
    !/^sha256:[a-f0-9]{64}$/.test(String(approval.release.cloudImageDigest || "")) ||
    !/^sha256:[a-f0-9]{64}$/.test(String(approval.release.workspaceImageDigest || "")) ||
    !/^np-[A-Za-z0-9-]+$/.test(String(approval.expected.nodePoolId || "")) ||
    !/^[A-Za-z0-9][A-Za-z0-9.-]{1,63}$/.test(String(approval.expected.resolvedInstanceType || ""))) {
    throw new Error("production_basic_acceptance_b_approval_invalid");
  }
  return approval;
}

export function validateProductionBasicAcceptanceBWriteCounts(value) {
  if (!exactObjectKeys(value, WRITE_COUNT_KEYS) || WRITE_COUNT_KEYS.some((key) => value[key] !== EXACT_WRITE_COUNTS[key])) {
    throw new Error("production_basic_acceptance_b_write_counts_invalid");
  }
  return { ...value };
}

function positiveSafeInteger(value) {
  return Number.isSafeInteger(value) && value > 0;
}

export function validateProductionBasicAcceptanceBReadback(value, approval) {
  const fail = () => { throw new Error("production_basic_acceptance_b_readback_invalid"); };
  const topLevelKeys = [
    "schemaVersion", "operationMode", "status", "approvalId", "approvalDigest", "release", "baseline", "quote", "debit",
    "launch", "compute", "storage", "attachment", "runtime", "receipt", "workspaceUrl", "writeCounts"
  ];
  if (!exactObjectKeys(value, topLevelKeys) || value.schemaVersion !== 1 ||
    value.operationMode !== PRODUCTION_BASIC_ACCEPTANCE_B_OPERATION.operationMode || value.status !== "succeeded" ||
    value.approvalId !== approval?.approvalId || value.approvalDigest !== productionBasicAcceptanceBApprovalDigest(approval) ||
    !exactObjectKeys(value.release, ["mergedMainSha", "cloudImageDigest", "workspaceImageDigest"]) ||
    canonicalJson(value.release) !== canonicalJson(approval?.release) ||
    !exactObjectKeys(value.baseline, ["workspaceCount", "workspaceLaunchCount", "workspaceKeyCount", "workspaceReceiptCount"]) ||
    Object.values(value.baseline).some((count) => count !== 0) ||
    !exactObjectKeys(value.quote, ["packageId", "sizeGb", "priceVersion", "currency", "totalChargeUsdMicros"]) ||
    !exactObjectKeys(value.debit, ["operationId", "count", "amountUsdMicros"]) ||
    !exactObjectKeys(value.launch, [
      "operationId", "accountId", "workspaceId", "name", "packageId", "sizeGb", "autoRenew", "priceVersion", "currency",
      "totalChargeUsdMicros", "status", "phase", "computeAllocationId", "storageId", "attachmentId", "runtimeId", "receiptId", "url"
    ]) ||
    !exactObjectKeys(value.compute, ["allocationId", "nodePoolId", "instanceType", "cvmInstanceId", "nodeName", "chargeType", "periodMonths", "renewFlag"]) ||
    !exactObjectKeys(value.storage, ["id", "providerResourceId", "sizeGb", "chargeType", "periodMonths", "renewFlag"]) ||
    !exactObjectKeys(value.attachment, ["id", "status"]) ||
    !exactObjectKeys(value.runtime, ["id", "status", "ready", "url", "podImageId"]) ||
    !exactObjectKeys(value.receipt, ["id", "type", "status", "workspaceId", "computeAllocationId", "storageId", "runtimeId", "totalChargeUsdMicros"]) ||
    !exactObjectKeys(value.workspaceUrl, ["url", "statusCode"])) {
    fail();
  }

  try {
    validateProductionBasicAcceptanceBWriteCounts(value.writeCounts);
  } catch {
    fail();
  }

  const expectedUrl = `https://workspace.medopl.cn/w/${approval.launch.workspaceId}/`;
  const total = value.quote.totalChargeUsdMicros;
  if (value.quote.packageId !== "basic" || value.quote.sizeGb !== 10 || !String(value.quote.priceVersion || "") ||
    value.quote.currency !== "USD" || !positiveSafeInteger(total) ||
    value.debit.operationId !== `${approval.launch.operationId}:charge` || value.debit.count !== 1 || value.debit.amountUsdMicros !== total ||
    value.launch.operationId !== approval.launch.operationId || value.launch.accountId !== approval.customer.accountId ||
    value.launch.workspaceId !== approval.launch.workspaceId || value.launch.name !== approval.launch.name || value.launch.packageId !== "basic" ||
    value.launch.sizeGb !== 10 || value.launch.autoRenew !== false || value.launch.priceVersion !== value.quote.priceVersion ||
    value.launch.currency !== "USD" || value.launch.totalChargeUsdMicros !== total || value.launch.status !== "succeeded" || value.launch.phase !== "succeeded" ||
    !String(value.launch.computeAllocationId || "") || !String(value.launch.storageId || "") || !String(value.launch.attachmentId || "") ||
    !String(value.launch.runtimeId || "") || !String(value.launch.receiptId || "") || value.launch.url !== expectedUrl ||
    value.compute.allocationId !== value.launch.computeAllocationId || value.compute.nodePoolId !== approval.expected.nodePoolId ||
    value.compute.instanceType !== approval.expected.resolvedInstanceType || !/^ins-[A-Za-z0-9-]+$/.test(String(value.compute.cvmInstanceId || "")) ||
    !String(value.compute.nodeName || "") || value.compute.chargeType !== "PREPAID" || value.compute.periodMonths !== 1 ||
    value.compute.renewFlag !== "NOTIFY_AND_MANUAL_RENEW" ||
    value.storage.id !== value.launch.storageId || !/^disk-[A-Za-z0-9-]+$/.test(String(value.storage.providerResourceId || "")) ||
    value.storage.sizeGb !== 10 || value.storage.chargeType !== "PREPAID" || value.storage.periodMonths !== 1 ||
    value.storage.renewFlag !== "NOTIFY_AND_MANUAL_RENEW" ||
    value.attachment.id !== value.launch.attachmentId || value.attachment.status !== "attached" ||
    value.runtime.id !== value.launch.runtimeId || value.runtime.status !== "running" || value.runtime.ready !== true || value.runtime.url !== expectedUrl ||
    !String(value.runtime.podImageId || "").endsWith(approval.release.workspaceImageDigest) ||
    value.receipt.id !== value.launch.receiptId || value.receipt.type !== "billing.workspace_purchased.v1" || value.receipt.status !== "completed" ||
    value.receipt.workspaceId !== value.launch.workspaceId || value.receipt.computeAllocationId !== value.launch.computeAllocationId ||
    value.receipt.storageId !== value.launch.storageId || value.receipt.runtimeId !== value.launch.runtimeId || value.receipt.totalChargeUsdMicros !== total ||
    value.workspaceUrl.url !== expectedUrl || value.workspaceUrl.statusCode !== 200) {
    fail();
  }
  return cloneJson(value);
}
