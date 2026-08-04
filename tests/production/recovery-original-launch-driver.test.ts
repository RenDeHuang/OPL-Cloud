import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import test from "node:test";

import {
  RECOVERY_ACCEPTANCE_FUNDING_ALLOWED_WRITES,
  RECOVERY_ACCEPTANCE_FUNDING_CONFIRMATION,
  RECOVERY_ACCEPTANCE_FUNDING_FORBIDDEN_WRITES,
  RECOVERY_ACCEPTANCE_FUNDING_MODE,
  RECOVERY_ACCEPTANCE_EXTRA_FUNDING_ALLOWED_WRITES,
  RECOVERY_ACCEPTANCE_EXTRA_FUNDING_CONFIRMATION,
  RECOVERY_ACCEPTANCE_EXTRA_FUNDING_FORBIDDEN_WRITES,
  RECOVERY_ACCEPTANCE_EXTRA_FUNDING_MODE,
  RECOVERY_ACCEPTANCE_ORIGINAL_LAUNCH_ALLOWED_WRITES,
  RECOVERY_ACCEPTANCE_ORIGINAL_LAUNCH_CONFIRMATION,
  RECOVERY_ACCEPTANCE_ORIGINAL_LAUNCH_FORBIDDEN_WRITES,
  RECOVERY_ACCEPTANCE_ORIGINAL_LAUNCH_MODE,
  assertManualReviewResourceAbsence,
  runRecoveryAcceptanceOriginalLaunch,
  runRecoveryAcceptanceFundingPrepare,
  runRecoveryAcceptanceExtraFundingPrepare,
  parseRecoveryAcceptanceFundingApproval,
  parseRecoveryAcceptanceOriginalLaunchApproval,
  recoveryAcceptanceApprovalDigest
} from "../../tools/recovery-original-launch-driver.ts";

const mergedMainSha = "a".repeat(40);
const cloudImageDigest = `sha256:${"b".repeat(64)}`;
const workspaceImageDigest = `sha256:${"c".repeat(64)}`;
const accountId = "acct-acceptance-b";
const email = "acceptance-b@example.com";
const nonce = "d".repeat(32);

function stableId(...parts: string[]) {
  const hash = createHash("sha1");
  for (const part of parts) {
    hash.update(part);
    hash.update(Buffer.from([0]));
  }
  return hash.digest("hex");
}

function launchIdentities(idempotencyKey: string) {
  const operationId = `workspace-launch-${stableId(accountId, idempotencyKey).slice(0, 18)}`;
  return { operationId, workspaceId: `ws-${stableId("workspace-launch-v2", accountId, operationId).slice(0, 18)}` };
}

function launchApproval(overrides: Record<string, unknown> = {}) {
  const launch = { idempotencyKey: "recovery-acceptance-launch-20260804-01", ...launchIdentities("recovery-acceptance-launch-20260804-01"), name: "Recovery Acceptance Basic", packageId: "basic", sizeGb: 10, autoRenew: false };
  const value = {
    schemaVersion: 1,
    operationMode: RECOVERY_ACCEPTANCE_ORIGINAL_LAUNCH_MODE,
    approvalId: "recovery-acceptance-original-launch-01",
    expiresAt: "2099-08-04T00:00:00Z",
    confirmation: RECOVERY_ACCEPTANCE_ORIGINAL_LAUNCH_CONFIRMATION,
    nonce,
    release: { mergedMainSha, cloudImageDigest, workspaceImageDigest },
    customer: { email, accountId },
    launch,
    expected: { nodePoolId: "np-basic-acceptance-b", resolvedInstanceType: "SA5.MEDIUM4" },
    allowedWrites: [...RECOVERY_ACCEPTANCE_ORIGINAL_LAUNCH_ALLOWED_WRITES],
    forbiddenWrites: [...RECOVERY_ACCEPTANCE_ORIGINAL_LAUNCH_FORBIDDEN_WRITES],
    approvalDigest: "",
    ...overrides
  } as Record<string, unknown>;
  value.approvalDigest = recoveryAcceptanceApprovalDigest(value);
  return value;
}

function fundingApproval(overrides: Record<string, unknown> = {}) {
  const value = {
    schemaVersion: 1,
    operationMode: RECOVERY_ACCEPTANCE_FUNDING_MODE,
    approvalId: "recovery-acceptance-funding-01",
    expiresAt: "2099-08-04T00:00:00Z",
    confirmation: RECOVERY_ACCEPTANCE_FUNDING_CONFIRMATION,
    nonce,
    release: { mergedMainSha, cloudImageDigest, workspaceImageDigest },
    customer: { email, accountId },
    rechargeUsdMicros: "60000000",
    walletOperationId: `wallet-adjustment-${stableId(accountId, `acceptance-b-wallet-recharge-v1:${accountId}:${createHash("sha256").update(email).update(Buffer.from([0])).digest("hex")}`).slice(0, 18)}`,
    allowedWrites: [...RECOVERY_ACCEPTANCE_FUNDING_ALLOWED_WRITES],
    forbiddenWrites: [...RECOVERY_ACCEPTANCE_FUNDING_FORBIDDEN_WRITES],
    approvalDigest: "",
    ...overrides
  } as Record<string, unknown>;
  value.approvalDigest = recoveryAcceptanceApprovalDigest(value);
  return value;
}

function sourcePayload(source: string, data: Record<string, unknown>) {
  return { source, available: true, status: "available", fetchedAt: "2026-08-04T00:00:00.000Z", data };
}

function response(payload: unknown, status = 200, headers: Record<string, string> = {}) {
  return new Response(JSON.stringify(payload), { status, headers: { "cache-control": "private, no-store", ...headers } });
}

function originalLaunchFixture(computeOverrides: Record<string, unknown> = {}) {
  const approval = launchApproval();
  const launch = {
    ...approval.launch,
    accountId,
    computeAllocationId: "compute-recovery-acceptance",
    status: "manual_review",
    phase: "storage_fulfilling",
    errorCode: "recovery_acceptance_canary_manual_review",
    storageId: null,
    runtimeServiceName: null,
    receiptId: null,
    continuationAttemptBudgets: Object.fromEntries(["storage", "attachment", "secret", "runtime", "activation", "receipt"].map((stage) => [stage, { max: 1, attempted: 0, confirmed: 0, unknown: 0 }]))
  };
  const computeOperation = {
    workspaceId: approval.launch.workspaceId,
    action: "create_compute_allocation",
    status: "succeeded",
    resourceId: launch.computeAllocationId,
    redactedProviderPayload: {
      normalLaunchMutationBudget: Object.fromEntries(["compute_create", "compute_claim_cvm", "compute_claim_node"].map((stage) => [stage, { max: 1, attempted: 1, confirmed: 1, unknown: 0 }]))
    }
  };
  const compute = {
    id: launch.computeAllocationId,
    accountId,
    workspaceId: approval.launch.workspaceId,
    packageId: "basic",
    status: "running",
    cvmStatus: "RUNNING",
    nodePoolId: approval.expected.nodePoolId,
    instanceType: approval.expected.resolvedInstanceType,
    cvmInstanceId: "ins-recovery-acceptance",
    ...computeOverrides
  };
  const ownership = { resourceId: launch.computeAllocationId, accountId, workspaceId: approval.launch.workspaceId, status: "active", nodeName: "node-recovery-acceptance" };
  let launchReads = 0;
  let historyReads = 0;
  const calls: Array<{ method: string; path: string }> = [];
  const fetchImpl = async (input: string | URL, init: RequestInit = {}) => {
    const url = new URL(String(input));
    const method = String(init.method || "GET").toUpperCase();
    calls.push({ method, path: url.pathname });
    if (url.pathname === "/api/auth/login" && method === "POST") {
      const body = JSON.parse(String(init.body || "{}")) as Record<string, unknown>;
      const admin = body.email === "admin@example.com";
      return response({ user: admin ? { accountId: "acct-admin", role: "admin" } : { accountId, role: "owner" } }, 200, { "set-cookie": `${admin ? "admin" : "customer"}=fixture`, "x-opl-csrf-token": "csrf-fixture" });
    }
    if (url.pathname === "/api/auth/me") return response(sourcePayload("sub2api", { accountId, email, role: "owner", status: "active", sub2apiUserId: "41" }));
    if (url.pathname === "/api/pricing/preview" && method === "POST") return response({ packageId: "basic", sizeGb: 10, currency: "USD", totalChargeUsdMicros: 52_580_000 });
    if (url.pathname === "/api/gateway/wallet") return response(sourcePayload("sub2api", { userId: "41", currency: "USD", usdMicros: "100000000", status: "active" }));
    if (url.pathname === "/api/gateway/balance-history") return response(sourcePayload("sub2api", { items: historyReads++ === 0 ? [] : [{ type: "balance", status: "used", valueUsdMicros: "-52580000", createdAt: "2099-08-04T00:00:00.000Z" }] }));
    if (url.pathname === `/api/workspace-launches/${approval.launch.operationId}`) {
      if (launchReads++ === 0) return response({ error: "not_found" }, 404);
      return response(launch);
    }
    if (url.pathname === "/api/workspace-launches" && method === "POST") return response(launch, 202);
    if (url.pathname === "/fabric/operations") return response([computeOperation]);
    if (url.pathname === `/fabric/compute-allocations/${launch.computeAllocationId}`) return response(compute);
    if (url.pathname === `/fabric/machine-ownerships/${launch.computeAllocationId}`) return response(ownership);
    if (url.pathname === `/api/operator/workspaces/${approval.launch.workspaceId}`) return response(sourcePayload("control-plane+fabric+ledger", { resources: [{ resourceType: sourcePayload("fabric", "compute") }], receipt: { status: "not_available" } }));
    throw new Error(`unexpected_request:${method}:${url.pathname}`);
  };
  return { approval, calls, fetchImpl };
}

function fundingFixture({ initialStatus = "succeeded", recoverStatus = "succeeded", recoverThrows = false } = {}) {
  const approval = fundingApproval();
  const operation = {
    operationId: approval.walletOperationId,
    accountId,
    kind: "recharge",
    amountUsd: "60.000000",
    reason: "production Basic Acceptance B account preparation",
    status: initialStatus,
    phase: initialStatus === "succeeded" ? "complete" : "review",
    allowedActions: initialStatus === "manual_review" ? ["recover_wallet_adjustment"] : [],
    beforeBalance: sourcePayload("sub2api", { currency: "USD", usdMicros: "0" }),
    afterBalance: sourcePayload("sub2api", { currency: "USD", usdMicros: "60000000" })
  };
  const calls: Array<{ method: string; path: string; body?: Record<string, unknown> }> = [];
  let walletReads = 0;
  const fetchImpl = async (input: string | URL, init: RequestInit = {}) => {
    const url = new URL(String(input));
    const method = init.method || "GET";
    const body = init.body ? JSON.parse(String(init.body)) as Record<string, unknown> : undefined;
    calls.push({ method, path: url.pathname, body });
    if (url.pathname === "/api/auth/login") {
      const isAdmin = body?.email === "admin@example.com";
      return new Response(JSON.stringify({ user: isAdmin ? { accountId: "acct-admin", role: "admin" } : { accountId, role: "owner" } }), { status: 200, headers: { "set-cookie": `${isAdmin ? "admin" : "customer"}=fixture`, "x-opl-csrf-token": "csrf-fixture" } });
    }
    if (url.pathname === "/api/auth/me") return new Response(JSON.stringify(sourcePayload("sub2api", { accountId, email, role: "owner", status: "active", sub2apiUserId: "41" })), { status: 200, headers: { "cache-control": "private, no-store" } });
    if (url.pathname === "/api/gateway/wallet") {
      const usdMicros = walletReads++ === 0 ? "0" : "60000000";
      return new Response(JSON.stringify(sourcePayload("sub2api", { userId: "41", currency: "USD", usdMicros, status: "active" })), { status: 200, headers: { "cache-control": "private, no-store" } });
    }
    if (url.pathname === `/api/operator/wallet-adjustments/${approval.walletOperationId}`) {
      return new Response(JSON.stringify(operation), { status: 200, headers: { "cache-control": "private, no-store" } });
    }
    if (url.pathname === `/api/operator/wallet-adjustments/${approval.walletOperationId}/recover` && method === "POST") {
      if (recoverThrows) return new Response(JSON.stringify({ error: "provider_result_unknown" }), { status: 502, headers: { "cache-control": "private, no-store" } });
      operation.status = recoverStatus;
      operation.phase = recoverStatus === "succeeded" ? "complete" : "review";
      return new Response(JSON.stringify(operation), { status: 200, headers: { "cache-control": "private, no-store" } });
    }
    throw new Error(`unexpected_request:${method}:${url.pathname}`);
  };
  return { approval, calls, fetchImpl };
}

test("original launch approval rejects release or identity drift and keeps exact write boundary", () => {
  const approval = launchApproval();
  assert.equal(parseRecoveryAcceptanceOriginalLaunchApproval(JSON.stringify(approval), { approvalId: approval.approvalId, mergedSha: mergedMainSha }).approvalDigest, approval.approvalDigest);
  assert.throws(() => parseRecoveryAcceptanceOriginalLaunchApproval(JSON.stringify({ ...approval, release: { ...approval.release, cloudImageDigest: `sha256:${"e".repeat(64)}` } }), { approvalId: approval.approvalId, mergedSha: mergedMainSha }), /approval_invalid/);
  assert.deepEqual(approval.allowedWrites, ["submit_one_workspace_launch", "debit_one_basic_month", "create_one_cvm", "claim_one_node", "persist_original_launch_manual_review"]);
  assert.ok(approval.forbiddenWrites.includes("submit_second_workspace_launch"));
  assert.ok(approval.forbiddenWrites.includes("create_one_cbs"));
});

test("original launch validates admin configuration before any request", async () => {
  const approval = launchApproval();
  await assert.rejects(() => runRecoveryAcceptanceOriginalLaunch({
    origin: "https://cloud.medopl.cn",
    customerEmail: email,
    customerPassword: "customer-secret",
    adminEmail: "",
    adminPassword: "",
    approvalJson: JSON.stringify(approval),
    approvalId: approval.approvalId as string,
    mergedSha: mergedMainSha,
    fabricOrigin: "http://127.0.0.1:3000",
    internalServiceToken: "fixture-token",
    fetchImpl: async () => { throw new Error("unexpected_request"); },
    now: new Date("2026-08-04T00:00:00Z")
  }), /recovery_acceptance_config_invalid/);
});

for (const [label, computeOverrides] of [
  ["wrong NodePool", { nodePoolId: "np-other-acceptance" }],
  ["wrong instance type", { instanceType: "SA5.LARGE8" }]
] as const) {
  test(`original launch rejects ${label} at run-level identity readback`, async () => {
    const fixture = originalLaunchFixture(computeOverrides);
    await assert.rejects(() => runRecoveryAcceptanceOriginalLaunch({
      origin: "https://cloud.medopl.cn",
      customerEmail: email,
      customerPassword: "customer-secret",
      adminEmail: "admin@example.com",
      adminPassword: "admin-secret",
      approvalJson: JSON.stringify(fixture.approval),
      approvalId: fixture.approval.approvalId as string,
      mergedSha: mergedMainSha,
      fabricOrigin: "http://127.0.0.1:3000",
      internalServiceToken: "fixture-token",
      launchPollAttempts: 1,
      launchPollDelayMs: 0,
      fetchImpl: fixture.fetchImpl,
      now: new Date("2026-08-04T00:00:00Z")
    }), /recovery_acceptance_compute_identity_invalid/);
  });
}

test("funding approval binds the existing Acceptance B deterministic wallet operation", () => {
  const approval = fundingApproval();
  const parsed = parseRecoveryAcceptanceFundingApproval(JSON.stringify(approval), { approvalId: approval.approvalId, mergedSha: mergedMainSha });
  assert.equal(parsed.walletOperationId, approval.walletOperationId);
  assert.equal(parsed.rechargeUsdMicros, "60000000");
  assert.equal(parsed.confirmation, RECOVERY_ACCEPTANCE_FUNDING_CONFIRMATION);
  assert.notEqual(parsed.walletOperationId, "wallet-adjustment-acceptance-b-wallet-recharge-v1");
  assert.throws(() => parseRecoveryAcceptanceFundingApproval(JSON.stringify({ ...approval, rechargeUsdMicros: "52580000" }), { approvalId: approval.approvalId, mergedSha: mergedMainSha }), /approval_invalid/);
});

test("funding prepare reads a succeeded old operation without posting", async () => {
  const fixture = fundingFixture();
  const result = await runRecoveryAcceptanceFundingPrepare({
    origin: "https://cloud.medopl.cn", customerEmail: email, customerPassword: "customer-secret", adminEmail: "admin@example.com", adminPassword: "admin-secret",
    approvalJson: JSON.stringify(fixture.approval), approvalId: fixture.approval.approvalId, mergedSha: mergedMainSha, confirmWalletRecharge: true, fetchImpl: fixture.fetchImpl,
    now: new Date("2026-08-04T00:00:00Z")
  });
  assert.equal(result.status, "succeeded");
  assert.equal(result.writeCounts.walletAdjustmentPosts, 0);
  assert.equal(result.writeCounts.walletRecoveryPosts, 0);
  assert.equal(fixture.calls.filter((call) => call.method === "POST" && call.path.endsWith("/recover")).length, 0);
});

test("manual-review old operation permits one recover POST and succeeds on same identity", async () => {
  const fixture = fundingFixture({ initialStatus: "manual_review" });
  const result = await runRecoveryAcceptanceFundingPrepare({
    origin: "https://cloud.medopl.cn", customerEmail: email, customerPassword: "customer-secret", adminEmail: "admin@example.com", adminPassword: "admin-secret",
    approvalJson: JSON.stringify(fixture.approval), approvalId: fixture.approval.approvalId, mergedSha: mergedMainSha, confirmWalletRecharge: true, fetchImpl: fixture.fetchImpl,
    now: new Date("2026-08-04T00:00:00Z")
  });
  const recoveryPosts = fixture.calls.filter((call) => call.method === "POST" && call.path.endsWith("/recover"));
  assert.equal(recoveryPosts.length, 1);
  assert.deepEqual(recoveryPosts[0].body, { accountId, evidenceRef: "case-20260804-acceptb" });
  assert.equal(result.writeCounts.walletRecoveryPosts, 1);
  assert.equal(result.writeCounts.walletAdjustmentPosts, 0);
});

test("unknown old-operation recovery is fail-closed and never retries the POST", async () => {
  const fixture = fundingFixture({ initialStatus: "manual_review", recoverThrows: true });
  await assert.rejects(() => runRecoveryAcceptanceFundingPrepare({
    origin: "https://cloud.medopl.cn", customerEmail: email, customerPassword: "customer-secret", adminEmail: "admin@example.com", adminPassword: "admin-secret",
    approvalJson: JSON.stringify(fixture.approval), approvalId: fixture.approval.approvalId, mergedSha: mergedMainSha, confirmWalletRecharge: true, fetchImpl: fixture.fetchImpl,
    now: new Date("2026-08-04T00:00:00Z")
  }), /recovery_acceptance_funding_unknown/);
  assert.equal(fixture.calls.filter((call) => call.method === "POST" && call.path.endsWith("/recover")).length, 1);
});

test("manual-review resource authority rejects orphan storage, runtime, or receipt facts", () => {
  const noReceipt = { source: "ledger", available: false, status: "unavailable", fetchedAt: "2026-08-04T00:00:00.000Z", data: null };
  const detail = { resources: [{ resourceType: sourcePayload("fabric", "compute") }], receipt: noReceipt };
  assert.doesNotThrow(() => assertManualReviewResourceAbsence(detail));
  for (const orphan of [
    { resources: [{ resourceType: sourcePayload("fabric", "storage") }], receipt: noReceipt },
    { resources: [{ resourceType: sourcePayload("fabric", "runtime") }], receipt: noReceipt },
    { resources: [{ resourceType: sourcePayload("fabric", "compute") }], receipt: sourcePayload("ledger", { receiptId: "receipt-orphan" }) }
  ]) assert.throws(() => assertManualReviewResourceAbsence(orphan), /recovery_acceptance_orphan_/);
});

test("extra funding requires a distinct operation mode and approval boundary", async () => {
  const operationKey = `recovery-acceptance-extra-funding-v1:${accountId}:${nonce}`;
  const value: Record<string, unknown> = {
    schemaVersion: 1,
    operationMode: RECOVERY_ACCEPTANCE_EXTRA_FUNDING_MODE,
    approvalId: "recovery-acceptance-extra-funding-01",
    expiresAt: "2099-08-04T00:00:00Z",
    confirmation: RECOVERY_ACCEPTANCE_EXTRA_FUNDING_CONFIRMATION,
    nonce,
    release: { mergedMainSha, cloudImageDigest, workspaceImageDigest },
    customer: { email, accountId },
    rechargeUsdMicros: "60000000",
    walletOperationId: `wallet-adjustment-${stableId(accountId, operationKey).slice(0, 18)}`,
    allowedWrites: [...RECOVERY_ACCEPTANCE_EXTRA_FUNDING_ALLOWED_WRITES],
    forbiddenWrites: [...RECOVERY_ACCEPTANCE_EXTRA_FUNDING_FORBIDDEN_WRITES],
    approvalDigest: ""
  };
  value.approvalDigest = recoveryAcceptanceApprovalDigest(value);
  const { parseRecoveryAcceptanceExtraFundingApproval } = await import("../../tools/recovery-original-launch-driver.ts");
  assert.equal(parseRecoveryAcceptanceExtraFundingApproval(JSON.stringify(value), { approvalId: value.approvalId as string, mergedSha: mergedMainSha }).operationMode, RECOVERY_ACCEPTANCE_EXTRA_FUNDING_MODE);
  assert.ok((value.forbiddenWrites as string[]).includes("recover_existing_wallet_adjustment"));
});
