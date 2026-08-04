import assert from "node:assert/strict";
import test from "node:test";

import {
  PRODUCTION_BASIC_ACCEPTANCE_B_RECONCILE_MODE,
  reconcileProductionBasicAcceptanceBAccount,
  validateProductionBasicAcceptanceBReconcileReadback
} from "../../tools/production-basic-acceptance-b-reconcile.ts";

const MERGED_SHA = "a".repeat(40);
const DIGEST = "b".repeat(64);
const EMAIL = "reconcile@example.com";
const ADMIN_EMAIL = "admin@medopl.cn";

const ZERO_COUNTS = {
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
};

function routeData(overrides = {}) {
  return {
    schemaVersion: 1,
    operationMode: PRODUCTION_BASIC_ACCEPTANCE_B_RECONCILE_MODE,
    status: "prepared",
    customerIdentitySha256: DIGEST,
    accountProvisionIdentitySha256: DIGEST,
    walletAdjustmentIdentitySha256: DIGEST,
    localGraph: "complete",
    remoteIdentity: "active",
    customerLogin: "not_attempted",
    wallet: "available",
    walletUsdMicros: "60000000",
    walletAdjustment: "succeeded",
    workspaceCount: 0,
    launchCount: 0,
    keyCount: 0,
    receiptCount: 0,
    ...overrides
  };
}

function envelope(source, data, status = "available") {
  return { source, status, available: true, fetchedAt: "2026-08-04T00:00:00.000Z", data };
}

function response(payload, headers = {}) {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { "cache-control": "private, no-store", ...headers }
  });
}

function loginPayload(accountId, role) {
  return response({ user: { accountId, role } }, { "set-cookie": `opl_session=${role}`, "x-opl-csrf-token": "csrf" });
}

function baseOptions(fetchImpl) {
  return {
    origin: "https://cloud.medopl.cn",
    adminEmail: ADMIN_EMAIL,
    adminPassword: "admin-password",
    customerEmail: EMAIL,
    customerPassword: "customer-password",
    mergedSha: MERGED_SHA,
    fetchImpl,
    now: new Date("2026-08-04T00:00:00.000Z")
  };
}

test("reconcile readback accepts prepared, absent, and unknown states with zero writes", () => {
  for (const status of ["prepared", "safe_to_retry_absent", "unknown"]) {
    const value = {
      schemaVersion: 1,
      operationMode: PRODUCTION_BASIC_ACCEPTANCE_B_RECONCILE_MODE,
      status,
      mergedMainSha: MERGED_SHA,
      customerIdentitySha256: DIGEST,
      accountProvisionIdentitySha256: DIGEST,
      walletAdjustmentIdentitySha256: DIGEST,
      localGraph: status === "safe_to_retry_absent" ? "absent" : status === "unknown" ? "unknown" : "complete",
      remoteIdentity: status === "safe_to_retry_absent" ? "absent" : status === "unknown" ? "unknown" : "active",
      customerLogin: status === "prepared" ? "active" : status === "safe_to_retry_absent" ? "not_attempted" : "unknown",
      wallet: status === "prepared" ? "available" : status === "safe_to_retry_absent" ? "absent" : "unknown",
      walletUsdMicros: status === "prepared" ? "60000000" : "",
      walletAdjustment: status === "prepared" ? "succeeded" : status === "safe_to_retry_absent" ? "absent" : "unknown",
      workspaceCount: 0,
      launchCount: 0,
      keyCount: 0,
      receiptCount: 0,
      writeCounts: { ...ZERO_COUNTS },
      runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 }
    };
    assert.deepEqual(validateProductionBasicAcceptanceBReconcileReadback(value, { mergedSha: MERGED_SHA }), value);
  }
});

test("reconcile account mode uses only login and GET readbacks for an active account", async () => {
  const requests = [];
  const fetchImpl = async (url, init = {}) => {
    const parsed = new URL(url);
    requests.push({ method: init.method || "GET", path: parsed.pathname, headers: init.headers || {} });
    if (parsed.pathname === "/api/auth/login") {
      const body = JSON.parse(init.body);
      return loginPayload(body.email === ADMIN_EMAIL ? "acct-admin" : "acct-reconcile", body.email === ADMIN_EMAIL ? "admin" : "owner");
    }
    if (parsed.pathname === "/api/operator/account-reconciliation") {
      assert.equal(init.headers["X-OPL-Account-Reconcile-Email"], EMAIL);
      return response(envelope("control-plane+sub2api+ledger", routeData()));
    }
    if (parsed.pathname === "/api/auth/me") return response(envelope("sub2api", { email: EMAIL, role: "owner", status: "active" }));
    if (parsed.pathname === "/api/workspaces") return response(envelope("control-plane", { items: [], total: 0, page: 1, pageSize: 50 }));
    if (parsed.pathname === "/api/workspace-launches") return response([]);
    if (parsed.pathname === "/api/gateway/keys") return response(envelope("sub2api", { items: [], total: 0, page: 1, pageSize: 50 }));
    if (parsed.pathname === "/api/billing/receipts") return response(envelope("ledger", { receipts: [], nextCursor: "", hasMore: false }, "empty"));
    throw new Error(`unexpected_request:${parsed.pathname}`);
  };

  const result = await reconcileProductionBasicAcceptanceBAccount(baseOptions(fetchImpl));
  assert.equal(result.status, "prepared");
  assert.deepEqual(result.writeCounts, ZERO_COUNTS);
  assert.deepEqual(requests.map((request) => request.method), ["POST", "GET", "POST", "GET", "GET", "GET", "GET", "GET"]);
  assert.equal(requests.filter((request) => request.method === "POST").length, 2);
});

test("reconcile preserves an authority unknown without attempting customer login or mutation", async () => {
  const requests = [];
  const fetchImpl = async (url, init = {}) => {
    const parsed = new URL(url);
    requests.push({ method: init.method || "GET", path: parsed.pathname });
    if (parsed.pathname === "/api/auth/login") return loginPayload("acct-admin", "admin");
    if (parsed.pathname === "/api/operator/account-reconciliation") {
      return response(envelope("control-plane+sub2api+ledger", routeData({
        status: "unknown", localGraph: "unknown", remoteIdentity: "unknown", customerLogin: "not_attempted",
        wallet: "unknown", walletUsdMicros: "", walletAdjustment: "unknown", readbackError: "sub2api_authority_unavailable"
      })));
    }
    throw new Error(`unexpected_request:${parsed.pathname}`);
  };

  const result = await reconcileProductionBasicAcceptanceBAccount(baseOptions(fetchImpl));
  assert.equal(result.status, "unknown");
  assert.deepEqual(result.runnerDirectMutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
  assert.deepEqual(requests.map((request) => request.method), ["POST", "GET"]);
  assert.equal(requests.some((request) => request.path === "/api/auth/login" && request.method === "POST"), true);
  assert.equal(requests.length, 2);
});

test("reconcile validator rejects a nonzero mutation count or sensitive field", () => {
  const base = {
    schemaVersion: 1,
    operationMode: PRODUCTION_BASIC_ACCEPTANCE_B_RECONCILE_MODE,
    status: "prepared",
    mergedMainSha: MERGED_SHA,
    customerIdentitySha256: DIGEST,
    accountProvisionIdentitySha256: DIGEST,
    walletAdjustmentIdentitySha256: DIGEST,
    localGraph: "complete",
    remoteIdentity: "active",
    customerLogin: "active",
    wallet: "available",
    walletUsdMicros: "60000000",
    walletAdjustment: "succeeded",
    workspaceCount: 0,
    launchCount: 0,
    keyCount: 0,
    receiptCount: 0,
    writeCounts: { ...ZERO_COUNTS },
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 }
  };
  assert.throws(() => validateProductionBasicAcceptanceBReconcileReadback({ ...base, writeCounts: { ...ZERO_COUNTS, receiptCreates: 1 } }, { mergedSha: MERGED_SHA }), /acceptance_b_account_reconcile_readback_invalid/);
  assert.throws(() => validateProductionBasicAcceptanceBReconcileReadback({ ...base, customerEmail: EMAIL }, { mergedSha: MERGED_SHA }), /acceptance_b_account_reconcile_readback_invalid/);
});
