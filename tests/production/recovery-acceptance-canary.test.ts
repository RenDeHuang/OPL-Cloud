import test from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";

import { verifyRecoveryAcceptanceCanary, type RecoveryAcceptanceCanaryApproval } from "../../tools/recovery-acceptance-canary.ts";

const mergedMainSha = "a".repeat(40);
const cloudImageDigest = `sha256:${"b".repeat(64)}`;
const accountId = "acct-recovery-canary";
const launchOperationId = "workspace-launch-recovery-canary";

function approval(nonce = "c".repeat(32)): RecoveryAcceptanceCanaryApproval {
  const material = JSON.stringify({ accountId, launchOperationId, mergedMainSha, cloudImageDigest, nonce });
  return {
    schemaVersion: 1,
    operationMode: "recovery_acceptance_canary",
    accountId,
    launchOperationId,
    mergedMainSha,
    cloudImageDigest,
    approvalDigest: createHash("sha256").update(material).digest("hex"),
    nonce
  };
}

function response(payload: unknown, status = 200, headers: Record<string, string> = {}) {
  return new Response(JSON.stringify(payload), { status, headers: { "content-type": "application/json", ...headers } });
}

function options(fetchImpl: typeof fetch, override: Partial<Parameters<typeof verifyRecoveryAcceptanceCanary>[0]> = {}) {
  return {
    origin: "https://cloud.medopl.cn",
    adminEmail: "admin@medopl.cn",
    adminPassword: "admin-password",
    approvalJson: JSON.stringify(approval()),
    mergedSha: mergedMainSha,
    cloudImageDigest,
    accountId,
    launchOperationId,
    enabled: true,
    accountAllowlist: accountId,
    fetchImpl,
    ...override
  };
}

test("Recovery Acceptance canary is default-off before any request", async () => {
  let calls = 0;
  await assert.rejects(
    () => verifyRecoveryAcceptanceCanary(options(async () => { calls += 1; return response({}); }, { enabled: false })),
    /recovery_acceptance_canary_disabled/
  );
  assert.equal(calls, 0);
});

test("Recovery Acceptance canary rejects a digest drift before login", async () => {
  let calls = 0;
  const value = approval();
  value.nonce = "d".repeat(32);
  await assert.rejects(
    () => verifyRecoveryAcceptanceCanary(options(async () => { calls += 1; return response({}); }, { approvalJson: JSON.stringify({ ...value, approvalDigest: "0".repeat(64) }) })),
    /recovery_acceptance_canary_approval_invalid/
  );
  assert.equal(calls, 0);
});

test("Recovery Acceptance canary converges one original launch and emits redacted evidence", async () => {
  const calls: Array<{ method: string; path: string }> = [];
  const fetchImpl: typeof fetch = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = init.method || "GET";
    calls.push({ method, path: url.pathname });
    if (url.pathname === "/api/auth/login") {
      return response({ user: { accountId: "acct-admin", role: "admin" } }, 200, { "set-cookie": "opl_session=session; Path=/", "x-opl-csrf-token": "csrf" });
    }
    if (method === "GET") {
      return response({ operationId: launchOperationId, accountId, status: "preparing", phase: "storage_fulfilling", workspaceId: "ws-canary" });
    }
    return response({
      schemaVersion: 1,
      operationMode: "recovery_acceptance_canary",
      status: "manual_review",
      phase: "storage_fulfilling",
      operationId: launchOperationId,
      workspaceId: "ws-canary",
      approvalDigest: approval().approvalDigest,
      errorCode: "recovery_acceptance_canary_manual_review",
      controlPlaneMutationCounts: { database: 1, sub2api: 0, tencent: 0, kubernetes: 0 }
    });
  };
  const result = await verifyRecoveryAcceptanceCanary(options(fetchImpl));
  assert.equal(result.status, "proven");
  assert.equal(result.recoveryEligible, true);
  assert.equal(result.runnerDirectMutationCounts.tencent, 0);
  assert.equal(result.controlPlaneMutationCounts.database, 1);
  assert.equal(result.target.launchOperationIdSha256, createHash("sha256").update(launchOperationId).digest("hex"));
  assert.equal(result.target.workspaceIdSha256, createHash("sha256").update("ws-canary").digest("hex"));
  assert.doesNotMatch(JSON.stringify(result), new RegExp(launchOperationId));
  assert.doesNotMatch(JSON.stringify(result), /ws-canary/);
  assert.equal(calls.filter((call) => call.method === "POST" && call.path.includes("recovery-acceptance")).length, 1);
  assert.doesNotMatch(JSON.stringify(result), /password|secret|token|cookie|nonce/);
});

test("Recovery Acceptance canary reconciles an unknown POST without retry", async () => {
  const calls: Array<{ method: string; path: string }> = [];
  const accepted = approval();
  const fetchImpl: typeof fetch = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = init.method || "GET";
    calls.push({ method, path: url.pathname });
    if (url.pathname === "/api/auth/login") {
      return response({ user: { accountId: "acct-admin", role: "admin" } }, 200, { "set-cookie": "opl_session=session; Path=/", "x-opl-csrf-token": "csrf" });
    }
    if (method === "GET" && calls.filter((call) => call.method === "GET").length === 1) {
      return response({ operationId: launchOperationId, accountId, status: "preparing", phase: "storage_fulfilling", workspaceId: "ws-canary" });
    }
    if (method === "POST") throw new TypeError("socket closed after commit");
    return response({
      operationId: launchOperationId,
      accountId,
      workspaceId: "ws-canary",
      status: "manual_review",
      phase: "storage_fulfilling",
      errorCode: "recovery_acceptance_canary_manual_review",
      recoveryAcceptance: { approvalDigest: accepted.approvalDigest }
    });
  };
  const result = await verifyRecoveryAcceptanceCanary(options(fetchImpl, { approvalJson: JSON.stringify(accepted) }));
  assert.equal(result.status, "proven");
  assert.equal(result.manualReview.reconciled, true);
  assert.equal(calls.filter((call) => call.method === "POST" && call.path.includes("recovery-acceptance")).length, 1);
});
