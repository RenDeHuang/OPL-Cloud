import { createHash } from "node:crypto";

import {
  assertPublicHttpsUrl,
  login,
  requestJson,
  sourceEnvelope,
  walletFact
} from "./production-verifier.ts";

export const RECOVERY_ACCEPTANCE_ORIGINAL_LAUNCH_MODE = "recovery_acceptance_original_launch";
export const RECOVERY_ACCEPTANCE_FUNDING_MODE = "recovery_acceptance_funding_prepare";
export const RECOVERY_ACCEPTANCE_EXTRA_FUNDING_MODE = "recovery_acceptance_extra_funding_prepare";
export const RECOVERY_ACCEPTANCE_ORIGINAL_LAUNCH_CONFIRMATION = "RUN_ONE_RECOVERY_ACCEPTANCE_ORIGINAL_BASIC_LAUNCH";
export const RECOVERY_ACCEPTANCE_FUNDING_CONFIRMATION = "RECOVER_ONE_EXISTING_ACCEPTANCE_B_WALLET_ADJUSTMENT";
export const RECOVERY_ACCEPTANCE_EXTRA_FUNDING_CONFIRMATION = "PREPARE_ONE_RECOVERY_ACCEPTANCE_BASIC_EXTRA_FUNDING";
export const RECOVERY_ACCEPTANCE_FUNDING_RECHARGE_USD_MICROS = "60000000";
export const RECOVERY_ACCEPTANCE_FUNDING_REASON = "Recovery Acceptance funding prepare";
export const RECOVERY_ACCEPTANCE_EXTRA_FUNDING_REASON = "Recovery Acceptance extra funding prepare";
export const ACCEPTANCE_B_PREPARE_REASON = "production Basic Acceptance B account preparation";

export const RECOVERY_ACCEPTANCE_ORIGINAL_LAUNCH_ALLOWED_WRITES = Object.freeze([
  "submit_one_workspace_launch",
  "debit_one_basic_month",
  "create_one_cvm",
  "claim_one_node",
  "persist_original_launch_manual_review"
]);
export const RECOVERY_ACCEPTANCE_ORIGINAL_LAUNCH_FORBIDDEN_WRITES = Object.freeze([
  "provision_account",
  "adjust_wallet",
  "submit_second_workspace_launch",
  "create_second_cvm",
  "create_one_cbs",
  "create_second_cbs",
  "create_one_attachment",
  "create_one_gateway_secret",
  "create_one_runtime",
  "activate_one_workspace",
  "record_one_purchase_receipt",
  "refund",
  "renew",
  "delete",
  "replace",
  "send_model_request"
]);
export const RECOVERY_ACCEPTANCE_FUNDING_ALLOWED_WRITES = Object.freeze(["recover_one_existing_wallet_adjustment"]);
export const RECOVERY_ACCEPTANCE_FUNDING_FORBIDDEN_WRITES = Object.freeze([
  "provision_account",
  "adjust_new_wallet",
  "submit_workspace_launch",
  "submit_second_workspace_launch",
  "debit_one_basic_month",
  "create_one_cvm",
  "claim_one_node",
  "create_one_cbs",
  "create_one_runtime",
  "record_one_purchase_receipt",
  "refund",
  "renew",
  "delete",
  "replace",
  "send_model_request"
]);
export const RECOVERY_ACCEPTANCE_EXTRA_FUNDING_ALLOWED_WRITES = Object.freeze(["adjust_one_wallet"]);
export const RECOVERY_ACCEPTANCE_EXTRA_FUNDING_FORBIDDEN_WRITES = Object.freeze([
  "provision_account", "recover_existing_wallet_adjustment", "submit_workspace_launch", "submit_second_workspace_launch",
  "debit_one_basic_month", "create_one_cvm", "claim_one_node", "create_one_cbs", "create_one_runtime", "record_one_purchase_receipt",
  "refund", "renew", "delete", "replace", "send_model_request"
]);

const ORIGINAL_KEYS = [
  "schemaVersion", "operationMode", "approvalId", "expiresAt", "confirmation", "nonce", "release", "customer", "launch", "expected", "allowedWrites", "forbiddenWrites", "approvalDigest"
];
const FUNDING_KEYS = [
  "schemaVersion", "operationMode", "approvalId", "expiresAt", "confirmation", "nonce", "release", "customer", "rechargeUsdMicros", "walletOperationId", "allowedWrites", "forbiddenWrites", "approvalDigest"
];
const EXTRA_FUNDING_KEYS = [...FUNDING_KEYS];
const RELEASE_KEYS = ["mergedMainSha", "cloudImageDigest", "workspaceImageDigest"];
const CUSTOMER_KEYS = ["email", "accountId"];
const LAUNCH_KEYS = ["idempotencyKey", "operationId", "workspaceId", "name", "packageId", "sizeGb", "autoRenew"];
const EXPECTED_KEYS = ["nodePoolId", "resolvedInstanceType"];
const ZERO_MUTATION_COUNTS = Object.freeze({
  accountProvisionPosts: 0,
  walletAdjustmentPosts: 0,
  workspaceLaunchPosts: 0,
  sub2apiDebits: 0,
  tencentCvmCreates: 0,
  kubernetesNodeClaims: 0,
  tencentCbsCreates: 0,
  attachmentCreates: 0,
  runtimeCreates: 0,
  receiptCreates: 0,
  modelRequests: 0,
  refunds: 0,
  renewals: 0,
  deletes: 0,
  replacements: 0,
  walletRecoveryPosts: 0
});

export interface RecoveryAcceptanceOriginalLaunchApproval {
  schemaVersion: number;
  operationMode: string;
  approvalId: string;
  expiresAt: string;
  confirmation: string;
  nonce: string;
  release: { mergedMainSha: string; cloudImageDigest: string; workspaceImageDigest: string };
  customer: { email: string; accountId: string };
  launch: { idempotencyKey: string; operationId: string; workspaceId: string; name: string; packageId: string; sizeGb: number; autoRenew: boolean };
  expected: { nodePoolId: string; resolvedInstanceType: string };
  allowedWrites: string[];
  forbiddenWrites: string[];
  approvalDigest: string;
}

export interface RecoveryAcceptanceFundingApproval {
  schemaVersion: number;
  operationMode: string;
  approvalId: string;
  expiresAt: string;
  confirmation: string;
  nonce: string;
  release: { mergedMainSha: string; cloudImageDigest: string; workspaceImageDigest: string };
  customer: { email: string; accountId: string };
  rechargeUsdMicros: string;
  walletOperationId: string;
  allowedWrites: string[];
  forbiddenWrites: string[];
  approvalDigest: string;
}

export type RecoveryAcceptanceExtraFundingApproval = RecoveryAcceptanceFundingApproval;

function exactKeys(value: unknown, keys: string[]): boolean {
  return value !== null && typeof value === "object" && !Array.isArray(value) &&
    JSON.stringify(Object.keys(value as Record<string, unknown>).sort()) === JSON.stringify([...keys].sort());
}

function canonicalJson(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(",")}]`;
  if (!value || typeof value !== "object") return JSON.stringify(value);
  return `{${Object.keys(value as Record<string, unknown>).sort().map((key) => `${JSON.stringify(key)}:${canonicalJson((value as Record<string, unknown>)[key])}`).join(",")}}`;
}

function digestMaterial(value: Record<string, unknown>): Record<string, unknown> {
  const copy = JSON.parse(JSON.stringify(value)) as Record<string, unknown>;
  delete copy.approvalDigest;
  return copy;
}

export function recoveryAcceptanceApprovalDigest(value: Record<string, unknown>): string {
  return createHash("sha256").update(canonicalJson(digestMaterial(value))).digest("hex");
}

function stableId(...parts: string[]): string {
  const hash = createHash("sha1");
  for (const part of parts) {
    hash.update(part);
    hash.update(Buffer.from([0]));
  }
  return hash.digest("hex");
}

export function recoveryAcceptanceLaunchIdentities(accountId: string, idempotencyKey: string) {
  const operationId = `workspace-launch-${stableId(accountId, idempotencyKey).slice(0, 18)}`;
  return { operationId, workspaceId: `ws-${stableId("workspace-launch-v2", accountId, operationId).slice(0, 18)}` };
}

function validExpiry(expiresAt: unknown, now: Date): boolean {
  if (typeof expiresAt !== "string" || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{3})?Z$/.test(expiresAt)) return false;
  const timestamp = Date.parse(expiresAt);
  return Number.isFinite(timestamp) && timestamp > now.getTime() && new Date(timestamp).toISOString().replace(".000Z", "Z") === expiresAt;
}

function validRelease(release: unknown): release is RecoveryAcceptanceOriginalLaunchApproval["release"] {
  const value = release as Record<string, unknown>;
  return exactKeys(value, RELEASE_KEYS) && /^[a-f0-9]{40}$/.test(String(value?.mergedMainSha || "")) &&
    /^sha256:[a-f0-9]{64}$/.test(String(value?.cloudImageDigest || "")) && /^sha256:[a-f0-9]{64}$/.test(String(value?.workspaceImageDigest || ""));
}

function validCustomer(customer: unknown): customer is RecoveryAcceptanceOriginalLaunchApproval["customer"] {
  const value = customer as Record<string, unknown>;
  const email = String(value?.email || "");
  return exactKeys(value, CUSTOMER_KEYS) && email === email.trim().toLowerCase() && /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email) && /^acct-[A-Za-z0-9-]+$/.test(String(value?.accountId || ""));
}

function validWriteBoundary(value: Record<string, unknown>, allowed: readonly string[], forbidden: readonly string[]): boolean {
  return Array.isArray(value.allowedWrites) && JSON.stringify(value.allowedWrites) === JSON.stringify(allowed) &&
    Array.isArray(value.forbiddenWrites) && JSON.stringify(value.forbiddenWrites) === JSON.stringify(forbidden);
}

function acceptanceBWalletOperationId(accountId: string, email: string): string {
  const emailDigest = createHash("sha256").update(email).update(Buffer.from([0])).digest("hex");
  const idempotencyKey = `acceptance-b-wallet-recharge-v1:${accountId}:${emailDigest}`;
  return `wallet-adjustment-${stableId(accountId, idempotencyKey).slice(0, 18)}`;
}

function parseApproval(value: string | Record<string, unknown>, options: { approvalId?: string; mergedSha?: string; now?: Date }, mode: string, keys: string[]): Record<string, unknown> {
  let parsed: unknown = value;
  if (typeof value === "string") {
    try { parsed = JSON.parse(value); } catch { parsed = null; }
  }
  const now = options.now instanceof Date ? options.now : new Date(options.now || Date.now());
  const record = parsed as Record<string, unknown>;
  if (!exactKeys(record, keys) || record.schemaVersion !== 1 || record.operationMode !== mode ||
    (options.approvalId !== undefined && record.approvalId !== options.approvalId) ||
    !String(record.approvalId || "") || !validExpiry(record.expiresAt, now) ||
    record.approvalDigest !== recoveryAcceptanceApprovalDigest(record)) throw new Error("recovery_acceptance_approval_invalid");
  if (options.mergedSha && (record.release as Record<string, unknown>)?.mergedMainSha !== options.mergedSha) throw new Error("recovery_acceptance_approval_invalid");
  return record;
}

export function parseRecoveryAcceptanceOriginalLaunchApproval(value: string | Record<string, unknown>, options: { approvalId?: string; mergedSha?: string; now?: Date } = {}): RecoveryAcceptanceOriginalLaunchApproval {
  const record = parseApproval(value, options, RECOVERY_ACCEPTANCE_ORIGINAL_LAUNCH_MODE, ORIGINAL_KEYS);
  const release = record.release as Record<string, unknown>;
  const customer = record.customer as Record<string, unknown>;
  const launch = record.launch as Record<string, unknown>;
  const expected = record.expected as Record<string, unknown>;
  const identities = recoveryAcceptanceLaunchIdentities(String(customer.accountId), String(launch.idempotencyKey));
  if (record.confirmation !== RECOVERY_ACCEPTANCE_ORIGINAL_LAUNCH_CONFIRMATION || !/^[a-f0-9]{32,128}$/.test(String(record.nonce || "")) ||
    !validRelease(release) || !validCustomer(customer) || !exactKeys(launch, LAUNCH_KEYS) || !exactKeys(expected, EXPECTED_KEYS) ||
    !validWriteBoundary(record, RECOVERY_ACCEPTANCE_ORIGINAL_LAUNCH_ALLOWED_WRITES, RECOVERY_ACCEPTANCE_ORIGINAL_LAUNCH_FORBIDDEN_WRITES) ||
    !/^[A-Za-z0-9][A-Za-z0-9._:-]{7,199}$/.test(String(launch.idempotencyKey || "")) || launch.operationId !== identities.operationId || launch.workspaceId !== identities.workspaceId ||
    String(launch.name || "") !== String(launch.name || "").trim() || !launch.name || launch.packageId !== "basic" || launch.sizeGb !== 10 || launch.autoRenew !== false ||
    !/^np-[A-Za-z0-9-]+$/.test(String(expected.nodePoolId || "")) || !/^[A-Za-z0-9][A-Za-z0-9.-]{1,63}$/.test(String(expected.resolvedInstanceType || ""))) {
    throw new Error("recovery_acceptance_approval_invalid");
  }
  return record as unknown as RecoveryAcceptanceOriginalLaunchApproval;
}

export function parseRecoveryAcceptanceFundingApproval(value: string | Record<string, unknown>, options: { approvalId?: string; mergedSha?: string; now?: Date } = {}): RecoveryAcceptanceFundingApproval {
  const record = parseApproval(value, options, RECOVERY_ACCEPTANCE_FUNDING_MODE, FUNDING_KEYS);
  const release = record.release as Record<string, unknown>;
  const customer = record.customer as Record<string, unknown>;
  const expectedWalletOperationID = acceptanceBWalletOperationId(String(customer?.accountId || ""), String(customer?.email || ""));
  if (record.confirmation !== RECOVERY_ACCEPTANCE_FUNDING_CONFIRMATION || !/^[a-f0-9]{32,128}$/.test(String(record.nonce || "")) ||
    !validRelease(release) || !validCustomer(customer) || record.rechargeUsdMicros !== RECOVERY_ACCEPTANCE_FUNDING_RECHARGE_USD_MICROS || record.walletOperationId !== expectedWalletOperationID ||
    !validWriteBoundary(record, RECOVERY_ACCEPTANCE_FUNDING_ALLOWED_WRITES, RECOVERY_ACCEPTANCE_FUNDING_FORBIDDEN_WRITES)) throw new Error("recovery_acceptance_approval_invalid");
  return record as unknown as RecoveryAcceptanceFundingApproval;
}

export function parseRecoveryAcceptanceExtraFundingApproval(value: string | Record<string, unknown>, options: { approvalId?: string; mergedSha?: string; now?: Date } = {}): RecoveryAcceptanceExtraFundingApproval {
  const record = parseApproval(value, options, RECOVERY_ACCEPTANCE_EXTRA_FUNDING_MODE, EXTRA_FUNDING_KEYS);
  const release = record.release as Record<string, unknown>;
  const customer = record.customer as Record<string, unknown>;
  const expectedWalletOperationID = `wallet-adjustment-${stableId(String(customer?.accountId || ""), `recovery-acceptance-extra-funding-v1:${String(customer?.accountId || "")}:${String(record.nonce || "")}`).slice(0, 18)}`;
  if (record.confirmation !== RECOVERY_ACCEPTANCE_EXTRA_FUNDING_CONFIRMATION || !/^[a-f0-9]{32,128}$/.test(String(record.nonce || "")) ||
    !validRelease(release) || !validCustomer(customer) || record.rechargeUsdMicros !== RECOVERY_ACCEPTANCE_FUNDING_RECHARGE_USD_MICROS || record.walletOperationId !== expectedWalletOperationID ||
    !validWriteBoundary(record, RECOVERY_ACCEPTANCE_EXTRA_FUNDING_ALLOWED_WRITES, RECOVERY_ACCEPTANCE_EXTRA_FUNDING_FORBIDDEN_WRITES)) throw new Error("recovery_acceptance_approval_invalid");
  return record as unknown as RecoveryAcceptanceExtraFundingApproval;
}

function sha(value: unknown): string { return createHash("sha256").update(String(value)).digest("hex"); }
function listPayload(result: { payload?: unknown }): unknown[] {
  const value = result?.payload;
  if (Array.isArray(value)) return value;
  if (Array.isArray((value as Record<string, unknown>)?.items)) return (value as Record<string, unknown>).items as unknown[];
  throw new Error("recovery_acceptance_readback_invalid");
}
function sourceData(result: { payload?: unknown; response?: Response }, source: string, allowEmpty = false): any {
  return sourceEnvelope(result as any, source, allowEmpty).data;
}
async function readLaunch(requestOptions: any, operationId: string): Promise<Record<string, any> | null> {
  try { return (await requestJson({ ...requestOptions, path: `/api/workspace-launches/${encodeURIComponent(operationId)}` })).payload as Record<string, any>; }
  catch (error) { if (String(error?.message || "").startsWith(`request_failed:GET:/api/workspace-launches/${encodeURIComponent(operationId)}:404:`)) return null; throw error; }
}
function assertLaunchIdentity(launch: Record<string, any>, approval: RecoveryAcceptanceOriginalLaunchApproval) {
  if (launch?.operationId !== approval.launch.operationId || launch?.workspaceId !== approval.launch.workspaceId || launch?.accountId !== approval.customer.accountId ||
    launch?.packageId !== "basic" || launch?.sizeGb !== 10 || launch?.autoRenew !== false) throw new Error("recovery_acceptance_launch_identity_invalid");
}
function sleep(ms: number) { return ms > 0 ? new Promise((resolve) => setTimeout(resolve, ms)) : Promise.resolve(); }
function exactBudget(value: unknown): boolean {
  const budget = value as Record<string, unknown>;
  return !!budget && budget.max === 1 && budget.attempted === 1 && budget.confirmed === 1 && budget.unknown === 0;
}
function exactZeroBudget(value: unknown): boolean {
  const budget = value as Record<string, unknown>;
  return !!budget && budget.max === 1 && budget.attempted === 0 && budget.confirmed === 0 && budget.unknown === 0;
}
function verifyComputeBudgets(operation: Record<string, any>): boolean {
  const budgets = operation?.redactedProviderPayload?.normalLaunchMutationBudget;
  return exactBudget(budgets?.compute_create) && exactBudget(budgets?.compute_claim_cvm) && exactBudget(budgets?.compute_claim_node);
}
function verifyComputeIdentity(compute: Record<string, any>, ownership: Record<string, any>, approval: RecoveryAcceptanceOriginalLaunchApproval, launch: Record<string, any>): boolean {
  const instanceType = String(compute?.instanceType || compute?.providerData?.instanceType || "");
  return compute?.id === launch.computeAllocationId && compute?.accountId === approval.customer.accountId && compute?.workspaceId === approval.launch.workspaceId &&
    compute?.packageId === "basic" && compute?.status === "running" && compute?.cvmStatus === "RUNNING" &&
    compute?.nodePoolId === approval.expected.nodePoolId && instanceType === approval.expected.resolvedInstanceType &&
    /^ins-[A-Za-z0-9-]+$/.test(String(compute?.cvmInstanceId || compute?.instanceId || "")) &&
    ownership?.resourceId === launch.computeAllocationId && ownership?.accountId === approval.customer.accountId && ownership?.workspaceId === approval.launch.workspaceId &&
    ownership?.status === "active" && Boolean(ownership?.nodeName);
}
function verifyContinuationBudgets(launch: Record<string, any>): boolean {
  const budgets = launch?.continuationAttemptBudgets;
  if (!budgets || typeof budgets !== "object") return false;
  const stages = ["storage", "attachment", "secret", "runtime", "activation", "receipt"];
  return stages.every((stage) => exactZeroBudget(budgets[stage]));
}
async function readBalanceHistory(requestOptions: any, auth: any): Promise<any[]> {
  const data = sourceData(await requestJson({ ...requestOptions, auth, path: "/api/gateway/balance-history?page=1&pageSize=100" }), "sub2api", true);
  if (!Array.isArray(data?.items)) throw new Error("recovery_acceptance_debit_readback_invalid");
  return data.items;
}
function matchingDebit(history: any[], amount: number, startedAt: number): any[] {
  return history.filter((entry) => entry?.type === "balance" && entry?.status === "used" && entry?.valueUsdMicros === `-${amount}` && Number.isFinite(Date.parse(String(entry?.createdAt || ""))) && Date.parse(String(entry.createdAt)) >= startedAt);
}
function canonicalWorkspaceURL(workspaceId: string): string { return `https://workspace.medopl.cn/w/${workspaceId}/`; }

export interface RecoveryAcceptanceOriginalLaunchOptions {
  origin: string;
  customerEmail: string;
  customerPassword: string;
  adminEmail: string;
  adminPassword: string;
  approvalJson: string | RecoveryAcceptanceOriginalLaunchApproval;
  approvalId: string;
  mergedSha: string;
  fabricOrigin: string;
  internalServiceToken: string;
  launchPollAttempts?: number;
  launchPollDelayMs?: number;
  requestTimeoutMs?: number;
  fetchImpl?: typeof fetch;
  now?: Date;
}

export function assertManualReviewResourceAbsence(detail: Record<string, any>) {
  if (!detail || !Array.isArray(detail.resources) || !detail.receipt) throw new Error("recovery_acceptance_resource_authority_invalid");
  if (detail.receipt?.status === "available") throw new Error("recovery_acceptance_orphan_receipt_invalid");
  for (const resource of detail.resources) {
    const resourceType = nestedSourceData(resource?.resourceType, "fabric");
    if (["storage", "attachment", "runtime"].includes(String(resourceType))) throw new Error("recovery_acceptance_orphan_resource_invalid");
    if (resource?.receiptRef?.status === "available") throw new Error("recovery_acceptance_orphan_receipt_invalid");
  }
}

export async function runRecoveryAcceptanceOriginalLaunch(options: RecoveryAcceptanceOriginalLaunchOptions) {
  const { origin, customerEmail, customerPassword, adminEmail, adminPassword, approvalJson, approvalId, mergedSha, fabricOrigin, internalServiceToken, fetchImpl = globalThis.fetch, launchPollAttempts = 180, launchPollDelayMs = 10_000, requestTimeoutMs = 30_000, now = new Date() } = options;
  const approval = parseRecoveryAcceptanceOriginalLaunchApproval(approvalJson, { approvalId, mergedSha, now });
  if (String(customerEmail || "").trim().toLowerCase() !== approval.customer.email || !String(customerPassword || "") || !String(adminEmail || "") || !String(adminPassword || "") || !/^http:\/\/127\.0\.0\.1:\d+$/.test(String(fabricOrigin || "")) || !String(internalServiceToken || "")) throw new Error("recovery_acceptance_config_invalid");
  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  const requestOptions = { fetchImpl, origin: normalizedOrigin, timeoutMs: requestTimeoutMs };
  const fabricOptions = { fetchImpl, origin: fabricOrigin, timeoutMs: requestTimeoutMs, headers: { authorization: `Bearer ${internalServiceToken}` } };
  const customerAuth = await login({ ...requestOptions, email: approval.customer.email, password: customerPassword });
  const adminAuth = await login({ ...requestOptions, email: adminEmail, password: adminPassword });
  if (adminAuth.user?.accountId !== "acct-admin" || adminAuth.user?.role !== "admin") throw new Error("recovery_acceptance_admin_login_failed");
  const identity = sourceData(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/auth/me" }), "sub2api");
  if (identity?.accountId !== approval.customer.accountId || identity?.email !== approval.customer.email || identity?.role !== "owner" || identity?.status !== "active") throw new Error("recovery_acceptance_customer_identity_invalid");
  const quoteRaw = (await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/pricing/preview", method: "POST", body: { resourceType: "workspace", packageId: "basic", sizeGb: 10 } })).payload;
  if (quoteRaw?.packageId !== "basic" || quoteRaw?.sizeGb !== 10 || quoteRaw?.currency !== "USD" || !Number.isSafeInteger(quoteRaw?.totalChargeUsdMicros) || quoteRaw.totalChargeUsdMicros <= 0) throw new Error("recovery_acceptance_quote_invalid");
  const wallet = walletFact(sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/gateway/wallet" }), "sub2api"), identity.sub2apiUserId);
  if (BigInt(wallet.usdMicros) <= BigInt(quoteRaw.totalChargeUsdMicros)) throw new Error("recovery_acceptance_wallet_insufficient");
  const startedAt = Date.now();
  const historyBefore = await readBalanceHistory(requestOptions, customerAuth);
  let launch = await readLaunch({ ...requestOptions, auth: customerAuth }, approval.launch.operationId);
  let workspaceLaunchPosts = 0;
  let reconciled = false;
  if (launch) {
    assertLaunchIdentity(launch, approval);
  } else {
    try {
      workspaceLaunchPosts = 1;
      const posted = await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/workspace-launches", method: "POST", headers: { "Idempotency-Key": approval.launch.idempotencyKey }, body: { name: approval.launch.name, packageId: "basic", sizeGb: 10, autoRenew: false } });
      if (posted.response.status !== 202) throw new Error("recovery_acceptance_launch_not_accepted");
      launch = posted.payload as Record<string, any>;
      assertLaunchIdentity(launch, approval);
    } catch (error) {
      launch = await readLaunch({ ...requestOptions, auth: customerAuth }, approval.launch.operationId);
      if (!launch) throw new Error(String(error?.message || "recovery_acceptance_launch_unknown"));
      assertLaunchIdentity(launch, approval);
      reconciled = true;
    }
  }
  for (let attempt = 1; attempt <= launchPollAttempts; attempt += 1) {
    if (launch.status === "manual_review") break;
    if (["failed", "refunded", "succeeded"].includes(String(launch.status))) throw new Error(`recovery_acceptance_launch_${launch.status}`);
    if (attempt > 1 || launchPollDelayMs > 0) await sleep(launchPollDelayMs);
    const next = await readLaunch({ ...requestOptions, auth: customerAuth }, approval.launch.operationId);
    if (!next) throw new Error("recovery_acceptance_launch_readback_invalid");
    assertLaunchIdentity(next, approval);
    launch = next;
  }
  if (launch.status !== "manual_review" || launch.phase !== "storage_fulfilling" || launch.errorCode !== "recovery_acceptance_canary_manual_review" || launch.storageId || launch.runtimeServiceName || launch.receiptId || !launch.computeAllocationId || !verifyContinuationBudgets(launch)) throw new Error("recovery_acceptance_manual_review_invalid");
  const operations = listPayload(await requestJson({ ...fabricOptions, path: "/fabric/operations" })).filter((operation: any) => operation?.workspaceId === approval.launch.workspaceId);
  const computeOperations = operations.filter((operation: any) => operation?.action === "create_compute_allocation" && operation?.status === "succeeded" && operation?.resourceId === launch.computeAllocationId);
  const forbiddenOperations = operations.filter((operation: any) => ["create_storage_volume", "create_storage_attachment", "upsert_gateway_secret", "create_workspace_runtime"].includes(operation?.action));
  if (computeOperations.length !== 1 || forbiddenOperations.length !== 0 || !verifyComputeBudgets(computeOperations[0])) throw new Error("recovery_acceptance_fabric_operation_counts_invalid");
  const computeAllocation = (await requestJson({ ...fabricOptions, path: `/fabric/compute-allocations/${encodeURIComponent(launch.computeAllocationId)}` })).payload as Record<string, any>;
  const ownership = (await requestJson({ ...fabricOptions, path: `/fabric/machine-ownerships/${encodeURIComponent(launch.computeAllocationId)}` })).payload as Record<string, any>;
  if (!verifyComputeIdentity(computeAllocation, ownership, approval, launch)) throw new Error("recovery_acceptance_compute_identity_invalid");
  const workspaceDetail = sourceData(await requestJson({ ...requestOptions, auth: adminAuth, path: `/api/operator/workspaces/${encodeURIComponent(approval.launch.workspaceId)}` }), "control-plane+fabric+ledger");
  assertManualReviewResourceAbsence(workspaceDetail);
  const historyAfter = await readBalanceHistory(requestOptions, customerAuth);
  const debitMatchesBefore = matchingDebit(historyBefore, quoteRaw.totalChargeUsdMicros, startedAt);
  const debitMatchesAfter = matchingDebit(historyAfter, quoteRaw.totalChargeUsdMicros, startedAt);
  if (debitMatchesAfter.length !== debitMatchesBefore.length + (reconciled ? 0 : 1) || (workspaceLaunchPosts === 1 && debitMatchesAfter.length < 1)) throw new Error("recovery_acceptance_debit_readback_invalid");
  return {
    schemaVersion: 1,
    operationMode: RECOVERY_ACCEPTANCE_ORIGINAL_LAUNCH_MODE,
    status: "succeeded",
    approvalId: approval.approvalId,
    approvalDigest: approval.approvalDigest,
    release: { ...approval.release },
    target: { accountIdSha256: sha(approval.customer.accountId), emailSha256: sha(approval.customer.email), launchOperationIdSha256: sha(approval.launch.operationId), workspaceIdSha256: sha(approval.launch.workspaceId) },
    manualReview: { status: launch.status, phase: launch.phase, errorCode: launch.errorCode, storageState: "storage_not_started", reconciled },
    writeCounts: { ...ZERO_MUTATION_COUNTS, workspaceLaunchPosts, sub2apiDebits: 1, tencentCvmCreates: 1, kubernetesNodeClaims: 1 },
    providerEvidence: { cvm: { attempted: 1, confirmed: 1, unknown: 0, missing: [] }, node: { attempted: 1, confirmed: 1, unknown: 0, missing: [] }, failureStage: "", providerErrorClass: "" },
    verifiedAt: now.toISOString()
  };
}

export interface RecoveryAcceptanceFundingOptions {
  origin: string;
  customerEmail: string;
  customerPassword: string;
  adminEmail: string;
  adminPassword: string;
  approvalJson: string | RecoveryAcceptanceFundingApproval;
  approvalId: string;
  mergedSha: string;
  confirmWalletRecharge: boolean;
  requestTimeoutMs?: number;
  fetchImpl?: typeof fetch;
  now?: Date;
}

export type RecoveryAcceptanceExtraFundingOptions = RecoveryAcceptanceFundingOptions;

async function readWalletAdjustment(requestOptions: any, adminAuth: any, operationId: string) {
  try { return (await requestJson({ ...requestOptions, auth: adminAuth, path: `/api/operator/wallet-adjustments/${encodeURIComponent(operationId)}` })).payload as Record<string, any>; }
  catch (error) { if (String(error?.message || "").includes(`request_failed:GET:/api/operator/wallet-adjustments/${encodeURIComponent(operationId)}:404:`)) return null; throw error; }
}
function decimalMicros(value: string): bigint {
  const match = /^([0-9]+)(?:\.([0-9]{1,6}))?$/.exec(String(value || ""));
  if (!match) throw new Error("recovery_acceptance_funding_readback_invalid");
  return BigInt(match[1]) * 1_000_000n + BigInt((match[2] || "").padEnd(6, "0") || "0");
}
function nestedSourceData(value: unknown, expectedSource: string): any {
  const envelope = (value as Record<string, any>)?.payload || value as Record<string, any>;
  if (envelope?.source !== expectedSource || envelope?.available !== true || envelope?.status !== "available" ||
    !Number.isFinite(Date.parse(String(envelope?.fetchedAt || ""))) || envelope.data === undefined || envelope.data === null) {
    throw new Error("recovery_acceptance_funding_readback_invalid");
  }
  return envelope.data as Record<string, any>;
}
function validateWalletAdjustment(value: Record<string, any>, approval: RecoveryAcceptanceFundingApproval, expectedReason: string) {
  if (value?.operationId !== approval.walletOperationId || value?.accountId !== approval.customer.accountId || value?.kind !== "recharge" || decimalMicros(value?.amountUsd) !== BigInt(approval.rechargeUsdMicros) || value?.reason !== expectedReason || value?.status !== "succeeded" || value?.phase !== "complete") throw new Error("recovery_acceptance_funding_readback_invalid");
  const before = nestedSourceData(value.beforeBalance, "sub2api");
  const after = nestedSourceData(value.afterBalance, "sub2api");
  if (!/^(0|[1-9][0-9]*)$/.test(String(before?.usdMicros || "")) || !/^(0|[1-9][0-9]*)$/.test(String(after?.usdMicros || "")) || BigInt(after.usdMicros) - BigInt(before.usdMicros) !== BigInt(approval.rechargeUsdMicros)) throw new Error("recovery_acceptance_funding_readback_invalid");
  return { beforeUsdMicros: String(before.usdMicros), afterUsdMicros: String(after.usdMicros) };
}

export async function runRecoveryAcceptanceFundingPrepare(options: RecoveryAcceptanceFundingOptions) {
  const { origin, customerEmail, customerPassword, adminEmail, adminPassword, approvalJson, approvalId, mergedSha, confirmWalletRecharge, fetchImpl = globalThis.fetch, requestTimeoutMs = 30_000, now = new Date() } = options;
  const approval = parseRecoveryAcceptanceFundingApproval(approvalJson, { approvalId, mergedSha, now });
  if (!confirmWalletRecharge || String(customerEmail || "").trim().toLowerCase() !== approval.customer.email || !customerPassword || !adminEmail || !adminPassword) throw new Error("recovery_acceptance_funding_confirmation_required");
  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  const requestOptions = { fetchImpl, origin: normalizedOrigin, timeoutMs: requestTimeoutMs };
  const adminAuth = await login({ ...requestOptions, email: adminEmail, password: adminPassword });
  if (adminAuth.user?.accountId !== "acct-admin" || adminAuth.user?.role !== "admin") throw new Error("recovery_acceptance_admin_login_failed");
  const customerAuth = await login({ ...requestOptions, email: approval.customer.email, password: customerPassword });
  const identity = sourceData(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/auth/me" }), "sub2api");
  if (identity?.accountId !== approval.customer.accountId || identity?.email !== approval.customer.email || identity?.role !== "owner" || identity?.status !== "active") throw new Error("recovery_acceptance_customer_identity_invalid");
  const beforeWallet = walletFact(sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/gateway/wallet" }), "sub2api"), identity.sub2apiUserId);
  let operation = await readWalletAdjustment(requestOptions, adminAuth, approval.walletOperationId);
  let walletRecoveryPosts = 0;
  let reconciled = false;
  if (!operation) throw new Error("recovery_acceptance_funding_operation_missing");
  if (operation.status === "manual_review") {
    if (!Array.isArray(operation.allowedActions) || !operation.allowedActions.includes("recover_wallet_adjustment")) throw new Error("recovery_acceptance_funding_recovery_not_allowed");
    const recoveryIdempotencyKey = `wallet-adjustment-recovery-${approval.approvalDigest.slice(0, 32)}`;
    try {
      walletRecoveryPosts = 1;
      await requestJson({
        ...requestOptions,
        auth: adminAuth,
        path: `/api/operator/wallet-adjustments/${encodeURIComponent(approval.walletOperationId)}/recover`,
        method: "POST",
        headers: { "Idempotency-Key": recoveryIdempotencyKey },
        body: { accountId: approval.customer.accountId, evidenceRef: "case-20260804-acceptb" }
      });
    } catch (error) {
      operation = await readWalletAdjustment(requestOptions, adminAuth, approval.walletOperationId);
      if (!operation) throw new Error(String(error?.message || "recovery_acceptance_funding_unknown"));
      if (operation.status !== "succeeded") throw new Error("recovery_acceptance_funding_unknown");
      reconciled = true;
    }
    operation = await readWalletAdjustment(requestOptions, adminAuth, approval.walletOperationId);
    if (!operation || operation.status !== "succeeded") throw new Error("recovery_acceptance_funding_unknown");
  }
  if (operation.status !== "succeeded") throw new Error("recovery_acceptance_funding_unknown");
  const adjustment = validateWalletAdjustment(operation, approval, ACCEPTANCE_B_PREPARE_REASON);
  const afterWallet = walletFact(sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/gateway/wallet" }), "sub2api"), identity.sub2apiUserId);
  if (afterWallet.usdMicros !== adjustment.afterUsdMicros) throw new Error("recovery_acceptance_funding_balance_invalid");
  if (BigInt(adjustment.afterUsdMicros) < BigInt(beforeWallet.usdMicros)) throw new Error("recovery_acceptance_funding_balance_invalid");
  return {
    schemaVersion: 1,
    operationMode: RECOVERY_ACCEPTANCE_FUNDING_MODE,
    status: "succeeded",
    approvalId: approval.approvalId,
    approvalDigest: approval.approvalDigest,
    release: { ...approval.release },
    customerIdentitySha256: sha(approval.customer.email),
    walletOperationIdSha256: sha(approval.walletOperationId),
    wallet: { beforeUsdMicros: adjustment.beforeUsdMicros, afterUsdMicros: adjustment.afterUsdMicros, rechargeUsdMicros: approval.rechargeUsdMicros, rechargeCount: 1, reconciled },
    writeCounts: { ...ZERO_MUTATION_COUNTS, walletRecoveryPosts },
    verifiedAt: now.toISOString()
  };
}

export async function runRecoveryAcceptanceExtraFundingPrepare(options: RecoveryAcceptanceExtraFundingOptions) {
  const { origin, customerEmail, customerPassword, adminEmail, adminPassword, approvalJson, approvalId, mergedSha, confirmWalletRecharge, fetchImpl = globalThis.fetch, requestTimeoutMs = 30_000, now = new Date() } = options;
  const approval = parseRecoveryAcceptanceExtraFundingApproval(approvalJson, { approvalId, mergedSha, now });
  if (!confirmWalletRecharge || String(customerEmail || "").trim().toLowerCase() !== approval.customer.email || !customerPassword || !adminEmail || !adminPassword) throw new Error("recovery_acceptance_extra_funding_confirmation_required");
  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  const requestOptions = { fetchImpl, origin: normalizedOrigin, timeoutMs: requestTimeoutMs };
  const adminAuth = await login({ ...requestOptions, email: adminEmail, password: adminPassword });
  if (adminAuth.user?.accountId !== "acct-admin" || adminAuth.user?.role !== "admin") throw new Error("recovery_acceptance_admin_login_failed");
  const customerAuth = await login({ ...requestOptions, email: approval.customer.email, password: customerPassword });
  const identity = sourceData(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/auth/me" }), "sub2api");
  if (identity?.accountId !== approval.customer.accountId || identity?.email !== approval.customer.email || identity?.role !== "owner" || identity?.status !== "active") throw new Error("recovery_acceptance_customer_identity_invalid");
  const beforeWallet = walletFact(sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/gateway/wallet" }), "sub2api"), identity.sub2apiUserId);
  let operation = await readWalletAdjustment(requestOptions, adminAuth, approval.walletOperationId);
  let walletAdjustmentPosts = 0;
  let reconciled = false;
  if (!operation) {
    try {
      walletAdjustmentPosts = 1;
      await requestJson({
        ...requestOptions,
        auth: adminAuth,
        path: `/api/operator/accounts/${encodeURIComponent(approval.customer.accountId)}/wallet-adjustments`,
        method: "POST",
        headers: { "Idempotency-Key": `recovery-acceptance-extra-funding-v1:${approval.customer.accountId}:${approval.nonce}` },
        body: { kind: "recharge", amountUsd: "60.000000", reason: RECOVERY_ACCEPTANCE_EXTRA_FUNDING_REASON, confirmationAccountId: approval.customer.accountId }
      });
    } catch (error) {
      operation = await readWalletAdjustment(requestOptions, adminAuth, approval.walletOperationId);
      if (!operation) throw new Error(String(error?.message || "recovery_acceptance_extra_funding_unknown"));
      reconciled = true;
    }
    operation ||= await readWalletAdjustment(requestOptions, adminAuth, approval.walletOperationId);
  }
  if (!operation || operation.status !== "succeeded") throw new Error("recovery_acceptance_extra_funding_readback_invalid");
  const adjustment = validateWalletAdjustment(operation, approval, RECOVERY_ACCEPTANCE_EXTRA_FUNDING_REASON);
  const afterWallet = walletFact(sourceEnvelope(await requestJson({ ...requestOptions, auth: customerAuth, path: "/api/gateway/wallet" }), "sub2api"), identity.sub2apiUserId);
  if (afterWallet.usdMicros !== adjustment.afterUsdMicros || BigInt(adjustment.afterUsdMicros) < BigInt(beforeWallet.usdMicros)) throw new Error("recovery_acceptance_extra_funding_balance_invalid");
  return {
    schemaVersion: 1,
    operationMode: RECOVERY_ACCEPTANCE_EXTRA_FUNDING_MODE,
    status: "succeeded",
    approvalId: approval.approvalId,
    approvalDigest: approval.approvalDigest,
    release: { ...approval.release },
    customerIdentitySha256: sha(approval.customer.email),
    walletOperationIdSha256: sha(approval.walletOperationId),
    wallet: { beforeUsdMicros: adjustment.beforeUsdMicros, afterUsdMicros: adjustment.afterUsdMicros, rechargeUsdMicros: approval.rechargeUsdMicros, rechargeCount: 1, reconciled },
    writeCounts: { ...ZERO_MUTATION_COUNTS, walletAdjustmentPosts },
    verifiedAt: now.toISOString()
  };
}

function parseArgs(argv: string[]) {
  const args: Record<string, string> = {};
  for (let index = 0; index < argv.length; index += 1) {
    const value = argv[index];
    if (!value.startsWith("--")) continue;
    const [key, inline] = value.slice(2).split("=", 2);
    args[key] = inline ?? argv[index + 1] ?? "";
    if (inline === undefined) index += 1;
  }
  return args;
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const args = parseArgs(process.argv.slice(2));
  const env = process.env;
  const run = args["recovery-acceptance-original-launch"] ? runRecoveryAcceptanceOriginalLaunch({
    origin: env.OPL_CONSOLE_ORIGIN || "https://cloud.medopl.cn", customerEmail: env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_EMAIL || "", customerPassword: env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_PASSWORD || "", adminEmail: env.OPL_SUB2API_ADMIN_EMAIL || "", adminPassword: env.OPL_SUB2API_ADMIN_PASSWORD || "", approvalJson: env.OPL_PRODUCTION_BASIC_RECOVERY_ACCEPTANCE_APPROVAL_JSON || "", approvalId: args["approval-id"] || "", mergedSha: env.OPL_MERGED_SHA || "", fabricOrigin: env.OPL_FABRIC_INTERNAL_ORIGIN || "", internalServiceToken: env.OPL_INTERNAL_SERVICE_TOKEN || "", launchPollAttempts: Number(env.OPL_VERIFY_LAUNCH_POLL_ATTEMPTS || "180"), launchPollDelayMs: Number(env.OPL_VERIFY_LAUNCH_POLL_DELAY_MS || "10000"), requestTimeoutMs: Number(env.OPL_VERIFY_REQUEST_TIMEOUT_MS || "30000")
  }) : args["recovery-acceptance-extra-funding-prepare"] ? runRecoveryAcceptanceExtraFundingPrepare({
    origin: env.OPL_CONSOLE_ORIGIN || "https://cloud.medopl.cn", customerEmail: env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_EMAIL || "", customerPassword: env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_PASSWORD || "", adminEmail: env.OPL_SUB2API_ADMIN_EMAIL || "", adminPassword: env.OPL_SUB2API_ADMIN_PASSWORD || "", approvalJson: env.OPL_PRODUCTION_BASIC_RECOVERY_ACCEPTANCE_EXTRA_FUNDING_APPROVAL_JSON || "", approvalId: args["approval-id"] || "", mergedSha: env.OPL_MERGED_SHA || "", confirmWalletRecharge: env.OPL_RECOVERY_ACCEPTANCE_EXTRA_FUNDING_CONFIRMATION === RECOVERY_ACCEPTANCE_EXTRA_FUNDING_CONFIRMATION, requestTimeoutMs: Number(env.OPL_VERIFY_REQUEST_TIMEOUT_MS || "30000")
  }) : args["recovery-acceptance-funding-prepare"] ? runRecoveryAcceptanceFundingPrepare({
    origin: env.OPL_CONSOLE_ORIGIN || "https://cloud.medopl.cn", customerEmail: env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_EMAIL || "", customerPassword: env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_PASSWORD || "", adminEmail: env.OPL_SUB2API_ADMIN_EMAIL || "", adminPassword: env.OPL_SUB2API_ADMIN_PASSWORD || "", approvalJson: env.OPL_PRODUCTION_BASIC_RECOVERY_ACCEPTANCE_FUNDING_APPROVAL_JSON || env.OPL_PRODUCTION_BASIC_RECOVERY_ACCEPTANCE_APPROVAL_JSON || "", approvalId: args["approval-id"] || "", mergedSha: env.OPL_MERGED_SHA || "", confirmWalletRecharge: env.OPL_RECOVERY_ACCEPTANCE_FUNDING_CONFIRMATION === RECOVERY_ACCEPTANCE_FUNDING_CONFIRMATION, requestTimeoutMs: Number(env.OPL_VERIFY_REQUEST_TIMEOUT_MS || "30000")
  }) : Promise.reject(new Error("recovery_acceptance_mode_required"));
  run.then((result) => process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)).catch((error) => { process.stderr.write(`${String(error?.message || error)}\n`); process.exitCode = 1; });
}
