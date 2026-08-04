import { createHash } from "node:crypto";
import { writeFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";

import {
  assertPublicHttpsUrl,
  login,
  requestJson,
  sourceEnvelope,
  walletFact
} from "./production-verifier.ts";
import { readBasicCanaryRuntimePodEvidence } from "./production-live-qa.ts";

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

export const PRODUCTION_BASIC_ACCEPTANCE_B_ACCOUNT_PREPARE_OPERATION = Object.freeze({
  schemaVersion: 1,
  operationMode: "acceptance_b_account_prepare",
  packageId: "basic",
  sizeGb: 10,
  autoRenew: false,
  rechargeUsdMicros: "60000000",
  confirmation: "PREPARE_ONE_ACCEPTANCE_B_ACCOUNT_WITH_ONE_PROVISION_AND_ONE_RECHARGE",
  forbiddenWrites: Object.freeze([
    "workspace_launch",
    "sub2api_debit",
    "cvm_create",
    "node_claim",
    "cbs_create",
    "attachment_create",
    "gateway_secret",
    "runtime_create",
    "workspace_activate",
    "workspace_receipt",
    "model_request",
    "refund",
    "renew",
    "delete",
    "replace"
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

const ACCEPTANCE_B_ACCOUNT_PAGE_SIZE = 50;
const MAX_ACCEPTANCE_B_ACCOUNT_PAGES = 1000;
const ACCEPTANCE_B_PREPARE_REASON = "production Basic Acceptance B account preparation";
const PREPARE_WRITE_COUNT_KEYS = [
  "accountProvisionPosts", "walletAdjustmentPosts", "workspaceLaunchPosts", "sub2apiDebits", "tencentCvmCreates",
  "kubernetesNodeClaims", "tencentCbsCreates", "runtimeCreates", "receiptCreates", "modelRequests", "refunds", "renewals", "deletes", "replacements"
];
const PREPARE_ZERO_WRITE_FIELDS = PREPARE_WRITE_COUNT_KEYS.filter((key) => !["accountProvisionPosts", "walletAdjustmentPosts"].includes(key));

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

function completeAcceptanceBAccountItems(pages) {
  if (!Array.isArray(pages) || pages.length === 0) {
    throw new Error("production_basic_acceptance_b_account_readback_invalid");
  }
  let total = null;
  let expectedPages = null;
  const items = [];
  for (let index = 0; index < pages.length; index += 1) {
    const page = pages[index];
    const pageNumber = index + 1;
    if (!Array.isArray(page?.items) || !Number.isSafeInteger(page?.total) || page.total < 0 ||
      page.page !== pageNumber || page.pageSize !== ACCEPTANCE_B_ACCOUNT_PAGE_SIZE || page.items.length > ACCEPTANCE_B_ACCOUNT_PAGE_SIZE) {
      throw new Error("production_basic_acceptance_b_account_readback_invalid");
    }
    if (total === null) total = page.total;
    if (page.total !== total) throw new Error("production_basic_acceptance_b_account_readback_invalid");
    expectedPages = Math.max(1, Math.ceil(total / ACCEPTANCE_B_ACCOUNT_PAGE_SIZE));
    if (expectedPages > MAX_ACCEPTANCE_B_ACCOUNT_PAGES || pageNumber > expectedPages) {
      throw new Error("production_basic_acceptance_b_account_readback_invalid");
    }
    const expectedItems = pageNumber < expectedPages ? ACCEPTANCE_B_ACCOUNT_PAGE_SIZE : total - (pageNumber - 1) * ACCEPTANCE_B_ACCOUNT_PAGE_SIZE;
    if (page.items.length !== Math.max(0, expectedItems)) throw new Error("production_basic_acceptance_b_account_readback_invalid");
    items.push(...page.items);
  }
  if (expectedPages === null || pages.length !== expectedPages) {
    throw new Error("production_basic_acceptance_b_account_readback_invalid");
  }
  return items;
}

/**
 * Select exactly one approved account identity from the complete paginated
 * operator account readback. The production account total is deliberately not
 * part of the identity contract: other operator-provisioned accounts may
 * exist. Every row matching either approved identity component is a candidate;
 * exactly one candidate must contain both components, otherwise the readback
 * fails closed.
 */
export function findUniqueProductionBasicAcceptanceBAccount(pages, accountId, email) {
  const normalizedAccountId = String(accountId || "");
  const normalizedEmail = String(email || "").trim().toLowerCase();
  if (!Array.isArray(pages) || pages.length === 0 || !normalizedAccountId || !normalizedEmail) {
    throw new Error("production_basic_acceptance_b_account_readback_invalid");
  }
  const candidates = completeAcceptanceBAccountItems(pages).filter((item) =>
    item?.accountId === normalizedAccountId || String(item?.email || "").trim().toLowerCase() === normalizedEmail);
  if (candidates.length !== 1 || candidates[0]?.accountId !== normalizedAccountId ||
    String(candidates[0]?.email || "").trim().toLowerCase() !== normalizedEmail) {
    throw new Error("production_basic_acceptance_b_account_readback_invalid");
  }
  return candidates[0];
}

/** Select one account by approved email while the account ID is not known yet. */
export function findUniqueProductionBasicAcceptanceBEmailAccount(pages, email) {
  const normalizedEmail = String(email || "").trim().toLowerCase();
  if (!normalizedEmail) throw new Error("production_basic_acceptance_b_account_readback_invalid");
  const matches = completeAcceptanceBAccountItems(pages).filter((item) => String(item?.email || "").trim().toLowerCase() === normalizedEmail);
  if (matches.length === 0) return null;
  if (matches.length !== 1) throw new Error("production_basic_acceptance_b_account_readback_invalid");
  const account = matches[0];
  if (!/^acct-[A-Za-z0-9-]+$/.test(String(account?.accountId || "")) ||
    !/^usr-[A-Za-z0-9-]+$/.test(String(account?.consoleUserId || "")) ||
    !/^[1-9][0-9]*$/.test(String(account?.sub2apiUserId || "")) || account?.role !== "owner" || account?.status !== "active") {
    throw new Error("production_basic_acceptance_b_account_readback_invalid");
  }
  return account;
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

const PREPARE_ARTIFACT_DIGEST_KEYS = new Set(["customerIdentitySha256", "accountProvisionIdentitySha256", "rechargeIdentitySha256"]);
const PREPARE_FORBIDDEN_ARTIFACT_KEYS = /(?:password|secret|token|cookie|csrf|email|accountid|userid|operationid|workspaceid|resourceid|name|providerrequestid|redeemcode)/i;

function assertPrepareArtifactRedacted(value) {
  if (!value || typeof value !== "object") return;
  if (Array.isArray(value)) {
    for (const item of value) assertPrepareArtifactRedacted(item);
    return;
  }
  for (const [key, nested] of Object.entries(value)) {
    if (PREPARE_ARTIFACT_DIGEST_KEYS.has(key)) {
      if (!/^[0-9a-f]{64}$/.test(String(nested || ""))) throw new Error("production_basic_acceptance_b_prepare_readback_invalid");
      continue;
    }
    if (PREPARE_FORBIDDEN_ARTIFACT_KEYS.test(key)) throw new Error("production_basic_acceptance_b_prepare_readback_invalid");
    assertPrepareArtifactRedacted(nested);
  }
}

function exactPrepareWriteCounts(value) {
  if (!exactObjectKeys(value, PREPARE_WRITE_COUNT_KEYS) || PREPARE_WRITE_COUNT_KEYS.some((key) => !Number.isSafeInteger(value[key]) || value[key] < 0) ||
    PREPARE_ZERO_WRITE_FIELDS.some((key) => value[key] !== 0) || value.accountProvisionPosts > 1 || value.walletAdjustmentPosts > 1) {
    throw new Error("production_basic_acceptance_b_prepare_readback_invalid");
  }
  return { ...value };
}

export function validateProductionBasicAcceptanceBPrepareReadback(value, options = {}) {
  try {
    assertPrepareArtifactRedacted(value);
    const topLevelKeys = ["schemaVersion", "operationMode", "status", "mergedMainSha", "customerIdentitySha256", "identity", "baseline", "quote", "wallet", "writeCounts"];
    if (!exactObjectKeys(value, topLevelKeys) || value.schemaVersion !== 1 || value.operationMode !== PRODUCTION_BASIC_ACCEPTANCE_B_ACCOUNT_PREPARE_OPERATION.operationMode ||
      value.status !== "succeeded" || value.mergedMainSha !== options.mergedSha || !/^[0-9a-f]{40}$/.test(String(value.mergedMainSha || "")) ||
      !/^[0-9a-f]{64}$/.test(String(value.customerIdentitySha256 || "")) ||
      !exactObjectKeys(value.identity, ["accountProvisionIdentitySha256", "status"]) || !/^[0-9a-f]{64}$/.test(String(value.identity.accountProvisionIdentitySha256 || "")) || value.identity.status !== "active" ||
      !exactObjectKeys(value.baseline, ["workspaceCount", "workspaceLaunchCount", "workspaceKeyCount", "workspaceReceiptCount"]) || Object.values(value.baseline).some((count) => count !== 0) ||
      !exactObjectKeys(value.quote, ["packageId", "sizeGb", "totalChargeUsdMicros", "currency"]) || value.quote.packageId !== "basic" || value.quote.sizeGb !== 10 || value.quote.currency !== "USD" ||
      !positiveSafeInteger(value.quote.totalChargeUsdMicros) || value.quote.totalChargeUsdMicros >= Number(PRODUCTION_BASIC_ACCEPTANCE_B_ACCOUNT_PREPARE_OPERATION.rechargeUsdMicros) ||
      !exactObjectKeys(value.wallet, ["beforeUsdMicros", "afterUsdMicros", "rechargeIdentitySha256", "rechargeCount"]) ||
      !/^(0|[1-9][0-9]*)$/.test(String(value.wallet.beforeUsdMicros || "")) || !/^(0|[1-9][0-9]*)$/.test(String(value.wallet.afterUsdMicros || "")) ||
      !/^[0-9a-f]{64}$/.test(String(value.wallet.rechargeIdentitySha256 || "")) || ![0, 1].includes(value.wallet.rechargeCount) ||
      (value.wallet.rechargeCount === 1 && BigInt(value.wallet.afterUsdMicros) - BigInt(value.wallet.beforeUsdMicros) !== BigInt(PRODUCTION_BASIC_ACCEPTANCE_B_ACCOUNT_PREPARE_OPERATION.rechargeUsdMicros)) ||
      (value.wallet.rechargeCount === 0 && value.wallet.afterUsdMicros !== value.wallet.beforeUsdMicros)) {
      throw new Error("production_basic_acceptance_b_prepare_readback_invalid");
    }
    exactPrepareWriteCounts(value.writeCounts);
    if (value.wallet.rechargeCount !== value.writeCounts.walletAdjustmentPosts || BigInt(value.wallet.beforeUsdMicros) > 9_223_372_036_854_775_807n ||
      BigInt(value.wallet.afterUsdMicros) > 9_223_372_036_854_775_807n || BigInt(value.wallet.afterUsdMicros) <= BigInt(value.quote.totalChargeUsdMicros)) {
      throw new Error("production_basic_acceptance_b_prepare_readback_invalid");
    }
    return cloneJson(value);
  } catch (error) {
    if (error?.message === "production_basic_acceptance_b_prepare_readback_invalid") throw error;
    throw new Error("production_basic_acceptance_b_prepare_readback_invalid");
  }
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

function sourceData(result, expectedSource, allowEmpty = false) {
  return sourceEnvelope(result, expectedSource, allowEmpty).data;
}

function listPayload(result) {
  const payload = result?.payload;
  if (Array.isArray(payload)) return payload;
  if (Array.isArray(payload?.items)) return payload.items;
  throw new Error("production_basic_acceptance_b_list_invalid");
}

function availableFact(value, source, name) {
  if (!value || value.source !== source || value.available !== true || value.status !== "available") {
    throw new Error(`production_basic_acceptance_b_${name}_fact_invalid`);
  }
  return value.data;
}

function resourceFact(detail, resourceType) {
  const resources = Array.isArray(detail?.resources) ? detail.resources : [];
  const matches = resources.filter((item) => availableFact(item?.resourceType, "fabric", `${resourceType}_type`) === resourceType);
  if (matches.length !== 1) throw new Error(`production_basic_acceptance_b_${resourceType}_fact_invalid`);
  return matches[0];
}

function positiveMicros(value, name) {
  if (!Number.isSafeInteger(value) || value <= 0) throw new Error(`production_basic_acceptance_b_${name}_invalid`);
  return value;
}

function canonicalWorkspaceUrl(workspaceId) {
  return `https://workspace.medopl.cn/w/${workspaceId}/`;
}

function basicAcceptanceBReceipt(data, launch, totalChargeUsdMicros) {
  const components = data?.components;
  const compute = components?.compute;
  const storage = components?.storage;
  const fulfillment = data?.fulfillment;
  if (data?.receiptId !== launch.receiptId || data?.type !== "billing.workspace_purchased.v1" || data?.status !== "completed" ||
    data?.workspaceId !== launch.workspaceId || data?.totalUsdMicros !== totalChargeUsdMicros ||
    compute?.resourceType !== "compute" || compute?.resourceId !== launch.computeAllocationId ||
    storage?.resourceType !== "storage" || storage?.resourceId !== launch.storageId || storage?.sizeGb !== 10 ||
    fulfillment?.computeAllocationId !== launch.computeAllocationId || fulfillment?.storageId !== launch.storageId ||
    fulfillment?.attachmentId !== launch.attachmentId || fulfillment?.runtimeId !== launch.runtimeId) {
    throw new Error("production_basic_acceptance_b_receipt_invalid");
  }
  return {
    id: data.receiptId,
    type: data.type,
    status: data.status,
    workspaceId: data.workspaceId,
    computeAllocationId: compute.resourceId,
    storageId: storage.resourceId,
    runtimeId: fulfillment.runtimeId,
    totalChargeUsdMicros
  };
}

function prepareDigest(...parts) {
  const hash = createHash("sha256");
  for (const part of parts) {
    hash.update(String(part));
    hash.update(Buffer.from([0]));
  }
  return hash.digest("hex");
}

function usdMicrosToDecimal(value) {
  const micros = BigInt(String(value));
  return `${micros / 1_000_000n}.${String(micros % 1_000_000n).padStart(6, "0")}`;
}

function prepareNestedWalletBalance(value) {
  if (!value || value.source !== "sub2api" || value.available !== true || value.status !== "available" || !value.data ||
    !Number.isFinite(Date.parse(value.fetchedAt)) || value.data.currency !== "USD" || !/^(0|[1-9][0-9]*)$/.test(String(value.data.usdMicros || ""))) {
    throw new Error("production_basic_acceptance_b_recharge_readback_invalid");
  }
  return { usdMicros: String(value.data.usdMicros) };
}

function validatePrepareWalletAdjustment(payload, operationId, accountId) {
  if (payload?.operationId !== operationId || payload?.accountId !== accountId || payload?.kind !== "recharge" ||
    payload?.amountUsd !== usdMicrosToDecimal(PRODUCTION_BASIC_ACCEPTANCE_B_ACCOUNT_PREPARE_OPERATION.rechargeUsdMicros) ||
    payload?.reason !== ACCEPTANCE_B_PREPARE_REASON || payload?.status !== "succeeded" || payload?.phase !== "complete") {
    throw new Error("production_basic_acceptance_b_recharge_readback_invalid");
  }
  const before = prepareNestedWalletBalance(payload.beforeBalance);
  const after = prepareNestedWalletBalance(payload.afterBalance);
  if (BigInt(after.usdMicros) - BigInt(before.usdMicros) !== BigInt(PRODUCTION_BASIC_ACCEPTANCE_B_ACCOUNT_PREPARE_OPERATION.rechargeUsdMicros)) {
    throw new Error("production_basic_acceptance_b_recharge_readback_invalid");
  }
  return { before, after };
}

async function prepareAuthoritativeGet(options) {
  try {
    return await requestJson(options);
  } catch (error) {
    if (String(error?.message || "").startsWith(`request_failed:GET:${options.path}:404:`)) return null;
    throw error;
  }
}

async function readPrepareAccount(requestOptions, adminAuth, customerEmail) {
  const pages = [];
  for (let page = 1; ; page += 1) {
    if (page > MAX_ACCEPTANCE_B_ACCOUNT_PAGES) throw new Error("production_basic_acceptance_b_account_readback_invalid");
    const data = sourceData(await requestJson({
      ...requestOptions,
      auth: adminAuth,
      path: `/api/operator/accounts?page=${page}&pageSize=${ACCEPTANCE_B_ACCOUNT_PAGE_SIZE}`
    }), "control-plane+sub2api", true);
    pages.push(data);
    if (!Number.isSafeInteger(data?.total) || data.total < 0 || data.page !== page || data.pageSize !== ACCEPTANCE_B_ACCOUNT_PAGE_SIZE) {
      throw new Error("production_basic_acceptance_b_account_readback_invalid");
    }
    const expectedPages = Math.max(1, Math.ceil(data.total / ACCEPTANCE_B_ACCOUNT_PAGE_SIZE));
    if (expectedPages > MAX_ACCEPTANCE_B_ACCOUNT_PAGES || page >= expectedPages) break;
  }
  return findUniqueProductionBasicAcceptanceBEmailAccount(pages, customerEmail);
}

async function readPrepareBaseline(requestOptions, customerAuth) {
  const workspacePage = sourceData(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/workspaces?page=1&pageSize=50" }), "control-plane", true);
  const launches = listPayload(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/workspace-launches" }));
  const keyPages = [];
  for (let page = 1; ; page += 1) {
    if (page > MAX_ACCEPTANCE_B_ACCOUNT_PAGES) throw new Error("production_basic_acceptance_b_baseline_not_fresh");
    const keys = sourceData(await requestJson({ ...requestOptions, auth: customerAuth, path: `/api/gateway/keys?page=${page}&pageSize=50` }), "sub2api", true);
    if (!Array.isArray(keys?.items) || !Number.isSafeInteger(keys?.total) || keys.total < 0 || keys.page !== page || keys.pageSize !== 50) {
      throw new Error("production_basic_acceptance_b_baseline_not_fresh");
    }
    keyPages.push(keys);
    const expectedPages = Math.max(1, Math.ceil(keys.total / 50));
    const expectedItems = page < expectedPages ? 50 : keys.total - (page - 1) * 50;
    if (keys.items.length !== Math.max(0, expectedItems)) throw new Error("production_basic_acceptance_b_baseline_not_fresh");
    if (page >= expectedPages) break;
  }
  const keyTotal = keyPages[0]?.total;
  if (keyPages.some((page, index) => page.total !== keyTotal || page.page !== index + 1)) throw new Error("production_basic_acceptance_b_baseline_not_fresh");
  const keys = { items: keyPages.flatMap((page) => page.items), total: keyTotal };
  const receipts = sourceData(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/billing/receipts?limit=50" }), "ledger", true);
  const workspaceKeys = (keys?.items || []).filter((key) => key?.kind === "workspace");
  const workspaceReceipts = (receipts?.receipts || []).filter((receipt) => receipt?.type === "billing.workspace_purchased.v1" || receipt?.workspaceId);
  if (!Array.isArray(workspacePage?.items) || workspacePage.total !== 0 || workspacePage.items.length !== 0 || launches.length !== 0 || workspaceKeys.length !== 0 ||
    !Array.isArray(receipts?.receipts) || workspaceReceipts.length !== 0 || receipts.hasMore !== false) {
    throw new Error("production_basic_acceptance_b_baseline_not_fresh");
  }
  return { workspaceCount: 0, workspaceLaunchCount: 0, workspaceKeyCount: 0, workspaceReceiptCount: 0 };
}

/**
 * Prepare the independent Acceptance B account. This mode intentionally stops
 * after authoritative identity, empty-workspace and funded-wallet readback;
 * it has no launch, provider, runtime, receipt or model capability.
 */
export async function prepareProductionBasicAcceptanceBAccount(options = {}) {
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
    throw new Error("production_basic_acceptance_b_prepare_config_invalid");
  }
  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  const requestOptions = { fetchImpl, origin: normalizedOrigin, timeoutMs: requestTimeoutMs };
  const adminAuth = await login({ ...requestOptions, email: adminEmail, password: adminPassword });
  if (adminAuth.user?.accountId !== "acct-admin" || adminAuth.user?.role !== "admin") throw new Error("production_basic_acceptance_b_admin_login_failed");

  let account = await readPrepareAccount(requestOptions, adminAuth, normalizedEmail);
  let accountProvisionPosts = 0;
  const customerEmailSha256 = prepareDigest(normalizedEmail);
  const accountProvisionIdempotencyKey = `acceptance-b-account-provision-v1:${customerEmailSha256}`;
  const expectedAccountId = `acct-${stableId("account", normalizedEmail).slice(0, 18)}`;
  const expectedAccountOperationId = `account-provision-${stableId(accountProvisionIdempotencyKey, normalizedEmail).slice(0, 18)}`;
  if (account && account.accountId !== expectedAccountId) throw new Error("production_basic_acceptance_b_account_identity_drift");
  if (!account) {
    accountProvisionPosts = 1;
    let postResponseInvalid = false;
    try {
      const provision = await requestJson({
        ...requestOptions,
        auth: adminAuth,
        path: "/api/operator/accounts",
        method: "POST",
        headers: { "Idempotency-Key": accountProvisionIdempotencyKey },
        body: { email: normalizedEmail, password: String(customerPassword), name: "Acceptance B Basic Customer" }
      });
      if (provision.response.status !== 201 || provision.payload?.status !== "succeeded" || provision.payload?.accountId !== expectedAccountId || provision.payload?.operationId !== expectedAccountOperationId) {
        postResponseInvalid = true;
        throw new Error("production_basic_acceptance_b_account_provision_failed");
      }
    } catch (error) {
      // A lost/failed POST is reconciled by the complete account page readback;
      // it is never retried because the provider result may already be applied.
      account = await readPrepareAccount(requestOptions, adminAuth, normalizedEmail);
      if (postResponseInvalid) throw error;
      if (!account) throw new Error(error?.message === "production_basic_acceptance_b_account_provision_failed"
        ? error.message
        : "production_basic_acceptance_b_account_provision_unknown");
    }
    account ||= await readPrepareAccount(requestOptions, adminAuth, normalizedEmail);
    if (!account) throw new Error("production_basic_acceptance_b_account_readback_invalid");
    if (account.accountId !== expectedAccountId) throw new Error("production_basic_acceptance_b_account_identity_drift");
  }

  const customerAuth = await login({ ...requestOptions, email: normalizedEmail, password: String(customerPassword) });
  const identity = sourceData(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/auth/me" }), "sub2api");
  if (identity?.accountId !== account.accountId || identity?.email !== normalizedEmail || identity?.role !== "owner" || identity?.status !== "active" ||
    !/^usr-[A-Za-z0-9-]+$/.test(String(identity?.consoleUserId || "")) || !/^[1-9][0-9]*$/.test(String(identity?.sub2apiUserId || ""))) {
    throw new Error("production_basic_acceptance_b_customer_identity_invalid");
  }
  const baseline = await readPrepareBaseline(requestOptions, customerAuth);
  const quoteRaw = (await requestJson({
    ...requestOptions,
    auth: customerAuth,
    path: "/api/pricing/preview",
    method: "POST",
    body: { resourceType: "workspace", packageId: "basic", sizeGb: 10 }
  })).payload;
  if (quoteRaw?.resourceType !== "workspace" || quoteRaw?.packageId !== "basic" || quoteRaw?.currency !== "USD" || quoteRaw?.storage?.priceSnapshot?.sizeGb !== 10 ||
    !String(quoteRaw?.priceVersion || "")) throw new Error("production_basic_acceptance_b_quote_invalid");
  const totalChargeUsdMicros = positiveMicros(quoteRaw.totalChargeUsdMicros, "quote");
  if (BigInt(totalChargeUsdMicros) >= BigInt(PRODUCTION_BASIC_ACCEPTANCE_B_ACCOUNT_PREPARE_OPERATION.rechargeUsdMicros)) throw new Error("production_basic_acceptance_b_prepare_budget_invalid");
  const walletBefore = walletFact(sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/gateway/wallet" }), "sub2api"), identity.sub2apiUserId);
  let walletAfter = walletBefore;
  let walletAdjustmentPosts = 0;
  const walletAdjustmentIdempotencyKey = `acceptance-b-wallet-recharge-v1:${account.accountId}:${customerEmailSha256}`;
  const walletOperationId = `wallet-adjustment-${stableId(account.accountId, walletAdjustmentIdempotencyKey).slice(0, 18)}`;
  if (BigInt(walletBefore.usdMicros) <= BigInt(totalChargeUsdMicros)) {
    let walletAuthority = await prepareAuthoritativeGet({ ...requestOptions, auth: adminAuth, path: `/api/operator/wallet-adjustments/${encodeURIComponent(walletOperationId)}` });
    if (!walletAuthority) {
      walletAdjustmentPosts = 1;
      let postResponseInvalid = false;
      try {
        const adjustment = await requestJson({
          ...requestOptions,
          auth: adminAuth,
          path: `/api/operator/accounts/${encodeURIComponent(account.accountId)}/wallet-adjustments`,
          method: "POST",
          headers: { "Idempotency-Key": walletAdjustmentIdempotencyKey },
          body: {
            kind: "recharge",
            amountUsd: usdMicrosToDecimal(PRODUCTION_BASIC_ACCEPTANCE_B_ACCOUNT_PREPARE_OPERATION.rechargeUsdMicros),
            reason: ACCEPTANCE_B_PREPARE_REASON,
            confirmationAccountId: account.accountId
          }
        });
        if (![201, 200].includes(adjustment.response.status)) {
          postResponseInvalid = true;
          throw new Error("production_basic_acceptance_b_recharge_failed");
        }
      } catch (error) {
        walletAuthority = await prepareAuthoritativeGet({ ...requestOptions, auth: adminAuth, path: `/api/operator/wallet-adjustments/${encodeURIComponent(walletOperationId)}` });
        if (postResponseInvalid) throw error;
        if (!walletAuthority) throw new Error(error?.message === "production_basic_acceptance_b_recharge_failed" ? error.message : "production_basic_acceptance_b_recharge_unknown");
      }
      walletAuthority ||= await prepareAuthoritativeGet({ ...requestOptions, auth: adminAuth, path: `/api/operator/wallet-adjustments/${encodeURIComponent(walletOperationId)}` });
      if (!walletAuthority) throw new Error("production_basic_acceptance_b_recharge_readback_invalid");
      const adjustment = validatePrepareWalletAdjustment(walletAuthority.payload, walletOperationId, account.accountId);
      walletAfter = walletFact(sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/gateway/wallet" }), "sub2api"), identity.sub2apiUserId);
      if (BigInt(walletAfter.usdMicros) - BigInt(walletBefore.usdMicros) !== BigInt(PRODUCTION_BASIC_ACCEPTANCE_B_ACCOUNT_PREPARE_OPERATION.rechargeUsdMicros) ||
        BigInt(walletAfter.usdMicros) !== BigInt(adjustment.after.usdMicros)) throw new Error("production_basic_acceptance_b_recharge_readback_invalid");
    } else {
      validatePrepareWalletAdjustment(walletAuthority.payload, walletOperationId, account.accountId);
    }
  }
  if (BigInt(walletAfter.usdMicros) <= BigInt(totalChargeUsdMicros)) throw new Error("production_basic_acceptance_b_wallet_insufficient");
  const readback = {
    schemaVersion: 1,
    operationMode: PRODUCTION_BASIC_ACCEPTANCE_B_ACCOUNT_PREPARE_OPERATION.operationMode,
    status: "succeeded",
    mergedMainSha: String(mergedSha),
    customerIdentitySha256: prepareDigest(account.accountId, identity.consoleUserId, identity.sub2apiUserId, normalizedEmail, identity.status),
    identity: { accountProvisionIdentitySha256: prepareDigest(accountProvisionIdempotencyKey), status: identity.status },
    baseline,
    quote: { packageId: "basic", sizeGb: 10, totalChargeUsdMicros, currency: "USD" },
    wallet: { beforeUsdMicros: walletBefore.usdMicros, afterUsdMicros: walletAfter.usdMicros, rechargeIdentitySha256: prepareDigest(account.accountId, walletOperationId), rechargeCount: walletAdjustmentPosts },
    writeCounts: {
      accountProvisionPosts,
      walletAdjustmentPosts,
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
    }
  };
  return validateProductionBasicAcceptanceBPrepareReadback(readback, { mergedSha });
}

/**
 * Execute the one fresh Basic order. The launch POST is intentionally isolated
 * here; every other business interaction is GET/readback and the final DTO is
 * passed through the strict validator before it can be uploaded.
 */
export async function runProductionBasicAcceptanceB(options = {}) {
  const {
    origin,
    fabricOrigin,
    internalServiceToken,
    adminEmail,
    adminPassword,
    customerPassword,
    approvalJson,
    approvalId,
    mergedSha,
    kubeconfigPath,
    namespace = "opl-cloud",
    launchPollAttempts = 180,
    launchPollDelayMs = 10_000,
    requestTimeoutMs = 30_000,
    fetchImpl = globalThis.fetch,
    execFileImpl,
    now = new Date()
  } = options;
  const approval = parseProductionBasicAcceptanceBApproval(approvalJson, { approvalId, now });
  if (mergedSha !== approval.release.mergedMainSha || !String(customerPassword || "") ||
    !String(internalServiceToken || "") || !String(kubeconfigPath || "").startsWith("/") ||
    !/^https:\/\/workspace\.medopl\.cn\/w\//.test(canonicalWorkspaceUrl(approval.launch.workspaceId))) {
    throw new Error("production_basic_acceptance_b_config_invalid");
  }
  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  if (!String(fabricOrigin || "").startsWith("http://127.0.0.1:")) throw new Error("production_basic_acceptance_b_fabric_origin_invalid");
  const requestOptions = { fetchImpl, origin: normalizedOrigin, timeoutMs: requestTimeoutMs };
  const fabricOptions = { fetchImpl, origin: String(fabricOrigin), timeoutMs: requestTimeoutMs, headers: { authorization: `Bearer ${internalServiceToken}` } };

  const adminAuth = await login({ ...requestOptions, email: adminEmail, password: adminPassword });
  if (adminAuth.user?.accountId !== "acct-admin" || adminAuth.user?.role !== "admin") throw new Error("production_basic_acceptance_b_admin_login_failed");
  const customerAuth = await login({ ...requestOptions, email: approval.customer.email, password: customerPassword });
  const identity = sourceData(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/auth/me" }), "sub2api");
  if (identity?.accountId !== approval.customer.accountId || identity?.email !== approval.customer.email || identity?.status !== "active") {
    throw new Error("production_basic_acceptance_b_customer_identity_invalid");
  }

  const accountPages = [];
  for (let page = 1; ; page += 1) {
    if (page > MAX_ACCEPTANCE_B_ACCOUNT_PAGES) throw new Error("production_basic_acceptance_b_account_readback_invalid");
    const accountPage = sourceData(await requestJson({
      ...requestOptions,
      auth: adminAuth,
      path: `/api/operator/accounts?page=${page}&pageSize=${ACCEPTANCE_B_ACCOUNT_PAGE_SIZE}`
    }), "control-plane+sub2api", true);
    accountPages.push(accountPage);
    if (!Number.isSafeInteger(accountPage?.total) || accountPage.total < 0 || accountPage.page !== page || accountPage.pageSize !== ACCEPTANCE_B_ACCOUNT_PAGE_SIZE) {
      throw new Error("production_basic_acceptance_b_account_readback_invalid");
    }
    const pages = Math.max(1, Math.ceil(accountPage.total / ACCEPTANCE_B_ACCOUNT_PAGE_SIZE));
    if (pages > MAX_ACCEPTANCE_B_ACCOUNT_PAGES) throw new Error("production_basic_acceptance_b_account_readback_invalid");
    if (page >= pages) break;
  }
  findUniqueProductionBasicAcceptanceBAccount(accountPages, approval.customer.accountId, approval.customer.email);

  const workspacePage = sourceData(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/workspaces?page=1&pageSize=20" }), "control-plane", true);
  const launchesBefore = listPayload(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/workspace-launches" }));
  const keysBefore = sourceData(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/gateway/keys?page=1&pageSize=50" }), "sub2api", true);
  const receiptsBefore = sourceData(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/billing/receipts?limit=50" }), "ledger", true);
  const workspaceKeysBefore = (keysBefore?.items || []).filter((key) => key?.kind === "workspace");
  const workspaceReceiptsBefore = (receiptsBefore?.receipts || []).filter((receipt) => receipt?.type === "billing.workspace_purchased.v1");
  const baseline = {
    workspaceCount: workspacePage?.total,
    workspaceLaunchCount: launchesBefore.length,
    workspaceKeyCount: workspaceKeysBefore.length,
    workspaceReceiptCount: workspaceReceiptsBefore.length
  };
  if (Object.values(baseline).some((count) => count !== 0)) throw new Error("production_basic_acceptance_b_baseline_not_fresh");

  const quoteRaw = (await requestJson({
    ...requestOptions,
    auth: customerAuth,
    path: "/api/pricing/preview",
    method: "POST",
    body: { resourceType: "workspace", packageId: "basic", sizeGb: 10 }
  })).payload;
  if (quoteRaw?.resourceType !== "workspace" || quoteRaw?.packageId !== "basic" || quoteRaw?.currency !== "USD" ||
    quoteRaw?.storage?.priceSnapshot?.sizeGb !== 10 || !String(quoteRaw?.priceVersion || "")) {
    throw new Error("production_basic_acceptance_b_quote_invalid");
  }
  const totalChargeUsdMicros = positiveMicros(quoteRaw.totalChargeUsdMicros, "quote");
  const quote = { packageId: "basic", sizeGb: 10, priceVersion: quoteRaw.priceVersion, currency: "USD", totalChargeUsdMicros };
  const wallet = walletFact(sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/gateway/wallet" }), "sub2api"), identity.sub2apiUserId);
  if (BigInt(wallet.usdMicros) <= BigInt(totalChargeUsdMicros)) throw new Error("production_basic_acceptance_b_wallet_insufficient");

  const launchResponse = await requestJson({
    ...requestOptions,
    auth: customerAuth,
    path: "/api/workspace-launches",
    method: "POST",
    headers: { "Idempotency-Key": approval.launch.idempotencyKey },
    body: { name: approval.launch.name, packageId: "basic", sizeGb: 10, autoRenew: false }
  });
  if (launchResponse.response.status !== 202 || launchResponse.payload?.operationId !== approval.launch.operationId ||
    launchResponse.payload?.workspaceId !== approval.launch.workspaceId || launchResponse.payload?.accountId !== approval.customer.accountId) {
    throw new Error("production_basic_acceptance_b_launch_not_accepted");
  }
  let launch = launchResponse.payload;
  for (let attempt = 1; attempt <= launchPollAttempts && (launch.status !== "succeeded" || launch.phase !== "succeeded"); attempt += 1) {
    if (attempt > 1 && launchPollDelayMs > 0) await new Promise((resolve) => setTimeout(resolve, launchPollDelayMs));
    launch = (await requestJson({ ...requestOptions, auth: customerAuth, path: `/api/workspace-launches/${encodeURIComponent(approval.launch.operationId)}` })).payload;
    if (launch?.status === "manual_review" || launch?.status === "failed" || launch?.status === "refunded") throw new Error("production_basic_acceptance_b_launch_failed");
  }
  if (launch.status !== "succeeded" || launch.phase !== "succeeded" || launch.accountId !== approval.customer.accountId || launch.workspaceId !== approval.launch.workspaceId) {
    throw new Error("production_basic_acceptance_b_launch_timeout");
  }
  const expectedUrl = canonicalWorkspaceUrl(approval.launch.workspaceId);
  if (launch.url !== expectedUrl || !launch.computeAllocationId || !launch.storageId || !launch.attachmentId || !launch.runtimeServiceName || !launch.receiptId) {
    throw new Error("production_basic_acceptance_b_launch_result_invalid");
  }

  const runtimeData = sourceData(await requestJson({ ...requestOptions, auth: customerAuth, path: `/api/workspaces/${encodeURIComponent(launch.workspaceId)}/runtime-status` }), "fabric");
  const runtimeId = String(runtimeData?.runtimeId || "");
  if (!runtimeId || runtimeData?.serviceName !== launch.runtimeServiceName || runtimeData?.workspaceId !== launch.workspaceId || runtimeData?.status !== "running" || runtimeData?.ready !== true || runtimeData?.url !== expectedUrl) {
    throw new Error("production_basic_acceptance_b_runtime_invalid");
  }
  // Launch responses expose the service name; runtime-status is authoritative for the runtime entity ID.
  launch = { ...launch, runtimeId };
  const detail = sourceData(await requestJson({ ...requestOptions, auth: adminAuth, path: `/api/operator/workspaces/${encodeURIComponent(launch.workspaceId)}` }), "control-plane+fabric+ledger");
  const workspace = availableFact(detail?.workspace, "control-plane", "workspace");
  if (workspace?.id !== launch.workspaceId || workspace?.ownerAccountId !== approval.customer.accountId || workspace?.state !== "active" || workspace?.packageId !== "basic") {
    throw new Error("production_basic_acceptance_b_workspace_invalid");
  }
  const computeResource = resourceFact(detail, "compute");
  const storageResource = resourceFact(detail, "storage");
  const attachmentResource = resourceFact(detail, "attachment");
  const runtimeResource = resourceFact(detail, "runtime");
  const computeAllocation = (await requestJson({ ...fabricOptions, path: `/fabric/compute-allocations/${encodeURIComponent(launch.computeAllocationId)}` })).payload;
  const ownership = (await requestJson({ ...fabricOptions, path: `/fabric/machine-ownerships/${encodeURIComponent(launch.computeAllocationId)}` })).payload;
  const truth = (await requestJson({ ...fabricOptions, path: `/fabric/monthly-provider-truth?computeAllocationId=${encodeURIComponent(launch.computeAllocationId)}&storageVolumeId=${encodeURIComponent(launch.storageId)}` })).payload;
  const operations = listPayload(await requestJson({ ...fabricOptions, path: "/fabric/operations" })).filter((operation) => operation?.workspaceId === launch.workspaceId && operation?.status === "succeeded");
  const countAction = (action) => operations.filter((operation) => operation.action === action).length;
  const operationCounts = {
    compute: countAction("create_compute_allocation"),
    storage: countAction("create_storage_volume"),
    attachment: countAction("create_storage_attachment"),
    secret: countAction("upsert_gateway_secret"),
    runtime: countAction("create_workspace_runtime")
  };
  if (operationCounts.compute !== 1 || operationCounts.storage !== 1 || operationCounts.attachment !== 1 || operationCounts.secret !== 1 || operationCounts.runtime !== 1) {
    throw new Error("production_basic_acceptance_b_fabric_operation_counts_invalid");
  }
  const cvmInstanceId = String(computeAllocation?.cvmInstanceId || computeAllocation?.instanceId || "");
  const nodeName = String(computeAllocation?.nodeName || "");
  const instanceType = String(computeAllocation?.instanceType || computeAllocation?.providerData?.instanceType || "");
  const nodePoolId = String(computeAllocation?.nodePoolId || "");
  if (computeAllocation?.id !== launch.computeAllocationId || computeAllocation?.accountId !== approval.customer.accountId || computeAllocation?.workspaceId !== launch.workspaceId ||
    computeAllocation?.packageId !== "basic" || computeAllocation?.status !== "running" || computeAllocation?.cvmStatus !== "RUNNING" || !/^ins-/.test(cvmInstanceId) ||
    computeAllocation?.chargeType !== "PREPAID" || computeAllocation?.renewFlag !== "NOTIFY_AND_MANUAL_RENEW" || instanceType !== approval.expected.resolvedInstanceType || nodePoolId !== approval.expected.nodePoolId || !nodeName ||
    ownership?.resourceId !== launch.computeAllocationId || ownership?.accountId !== approval.customer.accountId || ownership?.workspaceId !== launch.workspaceId || ownership?.nodeName !== nodeName || ownership?.status !== "active" ||
    truth?.computeState !== "ready" || truth?.storageState !== "ready" || truth?.compute?.providerResourceId !== cvmInstanceId || truth?.storage?.id !== launch.storageId || truth?.storage?.sizeGb !== 10 ||
    computeResource.providerId !== cvmInstanceId || storageResource.providerId !== truth.storage.providerResourceId || attachmentResource.status !== "attached" || runtimeResource.status !== "running") {
    throw new Error("production_basic_acceptance_b_provider_truth_invalid");
  }
  const pod = await readBasicCanaryRuntimePodEvidence({ workspaceId: launch.workspaceId, expectedDigest: approval.release.workspaceImageDigest, expectedNodeName: nodeName, kubeconfigPath, namespace, execFileImpl });

  const receiptsAfter = sourceData(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/billing/receipts?limit=50" }), "ledger", true);
  const receiptData = (receiptsAfter?.receipts || []).find((receipt) => receipt?.receiptId === launch.receiptId);
  if ((receiptsAfter?.receipts || []).filter((receipt) => receipt?.type === "billing.workspace_purchased.v1").length !== 1 || !receiptData) throw new Error("production_basic_acceptance_b_receipt_cardinality_invalid");
  const receipt = basicAcceptanceBReceipt(receiptData, launch, totalChargeUsdMicros);
  const balanceHistory = sourceData(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/gateway/balance-history?page=1&pageSize=50" }), "sub2api", true);
  const matchingDebits = (balanceHistory?.items || []).filter((item) => item?.type === "balance" && item?.status === "used" && item?.valueUsdMicros === `-${totalChargeUsdMicros}`);
  if (matchingDebits.length !== 1) throw new Error("production_basic_acceptance_b_debit_invalid");
  const keysAfter = sourceData(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/gateway/keys?page=1&pageSize=50" }), "sub2api", true);
  const workspaceKeysAfter = (keysAfter?.items || []).filter((key) => key?.kind === "workspace");
  const usage = sourceData(await requestJson({ ...requestOptions, auth: customerAuth, path: `/api/gateway/keys/${encodeURIComponent(launch.workspaceApiKeyId)}/usage?page=1&pageSize=20` }), "sub2api", true);
  if (workspaceKeysAfter.length !== 1 || workspaceKeysAfter[0]?.id !== launch.workspaceApiKeyId || usage?.total !== 0) throw new Error("production_basic_acceptance_b_key_or_model_invalid");
  const workspaceResponse = await fetchImpl(expectedUrl, { method: "GET" });
  if (workspaceResponse.status !== 200) throw new Error("production_basic_acceptance_b_workspace_url_invalid");

  const readback = {
    schemaVersion: 1,
    operationMode: PRODUCTION_BASIC_ACCEPTANCE_B_OPERATION.operationMode,
    status: "succeeded",
    approvalId: approval.approvalId,
    approvalDigest: productionBasicAcceptanceBApprovalDigest(approval),
    release: { ...approval.release },
    baseline,
    quote,
    debit: { operationId: `${launch.operationId}:charge`, count: 1, amountUsdMicros: totalChargeUsdMicros },
    launch: {
      operationId: launch.operationId, accountId: launch.accountId, workspaceId: launch.workspaceId, name: launch.name,
      packageId: launch.packageId, sizeGb: launch.sizeGb, autoRenew: launch.autoRenew, priceVersion: launch.priceVersion,
      currency: launch.currency, totalChargeUsdMicros: launch.totalChargeUsdMicros, status: launch.status, phase: launch.phase,
      computeAllocationId: launch.computeAllocationId, storageId: launch.storageId, attachmentId: launch.attachmentId,
      runtimeId, receiptId: launch.receiptId, url: launch.url
    },
    compute: { allocationId: launch.computeAllocationId, nodePoolId, instanceType, cvmInstanceId, nodeName, chargeType: computeAllocation.chargeType, periodMonths: 1, renewFlag: computeAllocation.renewFlag },
    storage: { id: launch.storageId, providerResourceId: truth.storage.providerResourceId, sizeGb: 10, chargeType: truth.storage.chargeType, periodMonths: 1, renewFlag: truth.storage.renewFlag },
    attachment: { id: launch.attachmentId, status: attachmentResource.status },
    runtime: { id: runtimeId, status: runtimeData.status, ready: runtimeData.ready, url: runtimeData.url, podImageId: pod.imageID },
    receipt,
    workspaceUrl: { url: expectedUrl, statusCode: workspaceResponse.status },
    writeCounts: {
      workspaceLaunchPosts: 1,
      sub2apiDebits: 1,
      tencentCvmCreates: operationCounts.compute,
      kubernetesNodeClaims: operationCounts.compute,
      tencentCbsCreates: operationCounts.storage,
      runtimeCreates: operationCounts.runtime,
      receiptCreates: 1,
      accountProvisionPosts: 0,
      walletAdjustmentPosts: 0,
      modelRequests: 0,
      refunds: 0,
      renewals: 0,
      deletes: 0,
      replacements: 0
    }
  };
  return validateProductionBasicAcceptanceBReadback(readback, approval);
}

function blockedProductionBasicAcceptanceBArtifact(errorCode = "production_basic_acceptance_b_failed") {
  return {
    schemaVersion: 1,
    operationMode: PRODUCTION_BASIC_ACCEPTANCE_B_OPERATION.operationMode,
    status: "blocked",
    errorCode,
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 }
  };
}

export function blockedProductionBasicAcceptanceBPrepareArtifact(errorCode = "production_basic_acceptance_b_prepare_failed") {
  const rawCode = String(errorCode);
  const safeCode = /^production_basic_acceptance_b_[a-z0-9_]+$/.test(rawCode) ? rawCode : "production_basic_acceptance_b_prepare_failed";
  return {
    schemaVersion: 1,
    operationMode: PRODUCTION_BASIC_ACCEPTANCE_B_ACCOUNT_PREPARE_OPERATION.operationMode,
    status: "blocked",
    errorCode: safeCode,
    // A failure can happen after a provision or recharge POST has been accepted
    // but before its response/readback is trustworthy. Keep all mutation facts
    // unknown so a later run must reconcile the deterministic identities first.
    mutationLedgerState: "unknown",
    runnerDirectMutationCounts: { sub2api: "unknown", tencent: "unknown", kubernetes: "unknown" },
    reconciliationRequired: ["account_provision", "wallet_recharge"]
  };
}

export async function runProductionBasicAcceptanceBCli({
  argv = process.argv.slice(2),
  env = process.env,
  stdout = process.stdout,
  stderr = process.stderr,
  fetchImpl = globalThis.fetch,
  execFileImpl,
  now = new Date()
} = {}) {
  if (argv.includes("--prepare-account")) {
    try {
      const result = await prepareProductionBasicAcceptanceBAccount({
        origin: env.OPL_CONSOLE_ORIGIN,
        adminEmail: env.OPL_SUB2API_ADMIN_EMAIL,
        adminPassword: env.OPL_SUB2API_ADMIN_PASSWORD,
        customerEmail: env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_EMAIL,
        customerPassword: env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_PASSWORD,
        mergedSha: env.OPL_MERGED_SHA,
        requestTimeoutMs: Number(env.OPL_VERIFY_REQUEST_TIMEOUT_MS || 30_000),
        fetchImpl,
        now
      });
      const artifactPath = String(env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_PREPARE_ARTIFACT_PATH || "");
      if (artifactPath) await writeFile(artifactPath, `${JSON.stringify(result, null, 2)}\n`, { mode: 0o600 });
      stdout.write(`${JSON.stringify(result, null, 2)}\n`);
      return 0;
    } catch (error) {
      const artifact = blockedProductionBasicAcceptanceBPrepareArtifact(error?.message || undefined);
      const artifactPath = String(env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_PREPARE_ARTIFACT_PATH || "");
      if (artifactPath) await writeFile(artifactPath, `${JSON.stringify(artifact, null, 2)}\n`, { mode: 0o600 });
      stdout.write(`${JSON.stringify(artifact, null, 2)}\n`);
      stderr.write(`${JSON.stringify({ ok: false, errorCode: artifact.errorCode })}\n`);
      return 1;
    }
  }
  if (!argv.includes("--run")) return 2;
  try {
    const result = await runProductionBasicAcceptanceB({
      origin: env.OPL_CONSOLE_ORIGIN,
      fabricOrigin: env.OPL_FABRIC_INTERNAL_ORIGIN,
      internalServiceToken: env.OPL_INTERNAL_SERVICE_TOKEN,
      adminEmail: env.OPL_SUB2API_ADMIN_EMAIL,
      adminPassword: env.OPL_SUB2API_ADMIN_PASSWORD,
      customerPassword: env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_PASSWORD,
      approvalJson: env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_APPROVAL_JSON,
      approvalId: env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_APPROVAL_ID,
      mergedSha: env.OPL_MERGED_SHA,
      kubeconfigPath: env.TENCENT_DEPLOY_KUBECONFIG_PATH,
      namespace: env.OPL_K8S_NAMESPACE || "opl-cloud",
      launchPollAttempts: Number(env.OPL_VERIFY_LAUNCH_POLL_ATTEMPTS || 180),
      launchPollDelayMs: Number(env.OPL_VERIFY_LAUNCH_POLL_DELAY_MS || 10_000),
      requestTimeoutMs: Number(env.OPL_VERIFY_REQUEST_TIMEOUT_MS || 30_000),
      fetchImpl,
      execFileImpl,
      now
    });
    const artifactPath = String(env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_ARTIFACT_PATH || "");
    if (artifactPath) await writeFile(artifactPath, `${JSON.stringify(result, null, 2)}\n`, { mode: 0o600 });
    stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return 0;
  } catch (error) {
    const artifact = blockedProductionBasicAcceptanceBArtifact(error?.message || undefined);
    const artifactPath = String(env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_ARTIFACT_PATH || "");
    if (artifactPath) await writeFile(artifactPath, `${JSON.stringify(artifact, null, 2)}\n`, { mode: 0o600 });
    stdout.write(`${JSON.stringify(artifact, null, 2)}\n`);
    stderr.write(`${JSON.stringify({ ok: false, errorCode: artifact.errorCode })}\n`);
    return 1;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] || "").href) {
  runProductionBasicAcceptanceBCli().then((code) => { process.exitCode = code; });
}
