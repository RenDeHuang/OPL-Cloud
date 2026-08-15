import assert from "node:assert/strict";
import test from "node:test";

import {
  PRODUCTION_BASIC_ACCEPTANCE_B_RECONCILE_MODE,
  reconcileProductionBasicAcceptanceBAccount,
  runProductionBasicAcceptanceBReconcileCli,
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
    failureStage: "none",
    readbackError: "none",
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
      failureStage: status === "unknown" ? "remote_identity" : "none",
      readbackError: status === "unknown" ? "sub2api_authority_unavailable" : "none",
      errorCode: status === "unknown" ? "acceptance_b_account_reconcile_unknown" : "none",
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
  assert.equal(result.failureStage, "none");
  assert.equal(result.readbackError, "none");
  assert.equal(result.errorCode, "none");
  assert.deepEqual(result.writeCounts, ZERO_COUNTS);
  assert.deepEqual(requests.map((request) => request.method), ["POST", "GET", "POST", "GET", "GET", "GET", "GET", "GET"]);
  assert.equal(requests.filter((request) => request.method === "POST").length, 2);
});

test("reconcile treats non-Workspace gateway keys as a fresh Acceptance B baseline", async () => {
  const keys = Array.from({ length: 5 }, (_, index) => ({
    id: `general-${index + 1}`,
    kind: "general",
    status: "active"
  }));
  const fetchImpl = async (url, init = {}) => {
    const parsed = new URL(url);
    if (parsed.pathname === "/api/auth/login") {
      const body = JSON.parse(init.body);
      return loginPayload(body.email === ADMIN_EMAIL ? "acct-admin" : "acct-reconcile", body.email === ADMIN_EMAIL ? "admin" : "owner");
    }
    if (parsed.pathname === "/api/operator/account-reconciliation") {
      return response(envelope("control-plane+sub2api+ledger", routeData()));
    }
    if (parsed.pathname === "/api/auth/me") return response(envelope("sub2api", { email: EMAIL, role: "owner", status: "active" }));
    if (parsed.pathname === "/api/workspaces") return response(envelope("control-plane", { items: [], total: 0, page: 1, pageSize: 50 }));
    if (parsed.pathname === "/api/workspace-launches") return response([]);
    if (parsed.pathname === "/api/gateway/keys") return response(envelope("sub2api", { items: keys, total: keys.length, page: 1, pageSize: 50 }));
    if (parsed.pathname === "/api/billing/receipts") return response(envelope("ledger", { receipts: [], nextCursor: "", hasMore: false }, "empty"));
    throw new Error(`unexpected_request:${parsed.pathname}`);
  };

  const result = await reconcileProductionBasicAcceptanceBAccount(baseOptions(fetchImpl));
  assert.equal(result.status, "prepared");
  assert.equal(result.keyCount, 0);
  assert.equal(result.failureStage, "none");
  assert.equal(result.readbackError, "none");
  assert.equal(result.errorCode, "none");
  assert.deepEqual(validateProductionBasicAcceptanceBReconcileReadback(result, { mergedSha: MERGED_SHA }), result);
});

test("reconcile fails closed when a gateway key kind cannot be classified", async () => {
  const fetchImpl = async (url, init = {}) => {
    const parsed = new URL(url);
    if (parsed.pathname === "/api/auth/login") {
      const body = JSON.parse(init.body);
      return loginPayload(body.email === ADMIN_EMAIL ? "acct-admin" : "acct-reconcile", body.email === ADMIN_EMAIL ? "admin" : "owner");
    }
    if (parsed.pathname === "/api/operator/account-reconciliation") {
      return response(envelope("control-plane+sub2api+ledger", routeData()));
    }
    if (parsed.pathname === "/api/auth/me") return response(envelope("sub2api", { email: EMAIL, role: "owner", status: "active" }));
    if (parsed.pathname === "/api/workspaces") return response(envelope("control-plane", { items: [], total: 0, page: 1, pageSize: 50 }));
    if (parsed.pathname === "/api/workspace-launches") return response([]);
    if (parsed.pathname === "/api/gateway/keys") return response(envelope("sub2api", { items: [{ id: "unknown-1" }], total: 1, page: 1, pageSize: 50 }));
    if (parsed.pathname === "/api/billing/receipts") return response(envelope("ledger", { receipts: [], nextCursor: "", hasMore: false }, "empty"));
    throw new Error(`unexpected_request:${parsed.pathname}`);
  };

  const result = await reconcileProductionBasicAcceptanceBAccount(baseOptions(fetchImpl));
  assert.equal(result.status, "unknown");
  assert.equal(result.failureStage, "baseline");
  assert.equal(result.readbackError, "baseline_authority_unavailable");
  assert.equal(result.errorCode, "acceptance_b_account_reconcile_unknown");
});

test("reconcile preserves an authority unknown without attempting customer login or mutation", async () => {
  const requests = [];
  const fetchImpl = async (url, init = {}) => {
    const parsed = new URL(url);
    requests.push({ method: init.method || "GET", path: parsed.pathname });
    if (parsed.pathname === "/api/auth/login") return loginPayload("acct-admin", "admin");
    if (parsed.pathname === "/api/operator/account-reconciliation") {
      return response(envelope("control-plane+sub2api+ledger", routeData({
        status: "unknown", localGraph: "complete", remoteIdentity: "active", customerLogin: "not_attempted",
        wallet: "unknown", walletUsdMicros: "", walletAdjustment: "unknown",
        customerIdentitySha256: DIGEST,
        accountProvisionIdentitySha256: DIGEST,
        walletAdjustmentIdentitySha256: DIGEST,
        failureStage: "remote_identity", readbackError: "sub2api_authority_unavailable"
      })));
    }
    throw new Error(`unexpected_request:${parsed.pathname}`);
  };

  const result = await reconcileProductionBasicAcceptanceBAccount(baseOptions(fetchImpl));
  assert.equal(result.status, "unknown");
  assert.equal(result.failureStage, "remote_identity");
  assert.equal(result.readbackError, "sub2api_authority_unavailable");
  assert.equal(result.errorCode, "acceptance_b_account_reconcile_unknown");
  assert.equal(result.customerIdentitySha256, DIGEST);
  assert.equal(result.accountProvisionIdentitySha256, DIGEST);
  assert.equal(result.walletAdjustmentIdentitySha256, DIGEST);
  assert.deepEqual(result.runnerDirectMutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
  assert.doesNotMatch(JSON.stringify(result), /reconcile@example\.com|acct-reconcile|operationId|customer-password|admin-password/i);
  assert.deepEqual(requests.map((request) => request.method), ["POST", "GET"]);
  assert.equal(requests.some((request) => request.path === "/api/auth/login" && request.method === "POST"), true);
  assert.equal(requests.length, 2);
});

test("request failure before the account-reconcile response uses a zero-digest fallback that still validates but fails the business gate", async () => {
  const fetchImpl = async (url, init = {}) => {
    const parsed = new URL(url);
    if (parsed.pathname === "/api/auth/login") return loginPayload("acct-admin", "admin");
    if (parsed.pathname === "/api/operator/account-reconciliation") throw new Error("socket_closed");
    throw new Error(`unexpected_request:${parsed.pathname}:${init.method || "GET"}`);
  };
  let stdout = "";
  const code = await runProductionBasicAcceptanceBReconcileCli({
    argv: ["--reconcile-account"],
    env: {
      OPL_MERGED_SHA: MERGED_SHA,
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_SUB2API_ADMIN_EMAIL: ADMIN_EMAIL,
      OPL_SUB2API_ADMIN_PASSWORD: "admin-password",
      OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_EMAIL: EMAIL,
      OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_PASSWORD: "customer-password",
      OPL_PRODUCTION_BASIC_ACCEPTANCE_B_RECONCILE_ARTIFACT_PATH: ""
    },
    stdout: { write: (value) => { stdout += value; } },
    stderr: { write: () => {} },
    fetchImpl,
    now: new Date("2026-08-04T00:00:00.000Z")
  });
  assert.equal(code, 1);
  const artifact = JSON.parse(stdout);
  assert.equal(artifact.status, "unknown");
  assert.equal(artifact.failureStage, "route_request");
  assert.equal(artifact.readbackError, "none");
  assert.notEqual(artifact.errorCode, "none");
  assert.equal(artifact.customerIdentitySha256, "0".repeat(64));
  assert.equal(artifact.accountProvisionIdentitySha256, "0".repeat(64));
  assert.equal(artifact.walletAdjustmentIdentitySha256, "0".repeat(64));
  assert.deepEqual(validateProductionBasicAcceptanceBReconcileReadback(artifact, { mergedSha: MERGED_SHA }), artifact);
  assert.equal(["prepared", "safe_to_retry_absent"].includes(artifact.status), false);
  assert.doesNotMatch(stdout, /reconcile@example\.com|acct-admin|operationId|password|secret|token/i);
});

test("only prepared passes the CLI business gate while safe_to_retry_absent and unknown remain validator-valid but rejected", async () => {
  const base = {
    OPL_MERGED_SHA: MERGED_SHA,
    OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
    OPL_SUB2API_ADMIN_EMAIL: ADMIN_EMAIL,
    OPL_SUB2API_ADMIN_PASSWORD: "admin-password",
    OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_EMAIL: EMAIL,
    OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_PASSWORD: "customer-password",
    OPL_PRODUCTION_BASIC_ACCEPTANCE_B_RECONCILE_ARTIFACT_PATH: ""
  };
  for (const status of ["safe_to_retry_absent", "unknown"]) {
    const fetchImpl = async (url) => {
      const parsed = new URL(url);
      if (parsed.pathname === "/api/auth/login") return loginPayload("acct-admin", "admin");
      if (parsed.pathname === "/api/operator/account-reconciliation") {
        return response(envelope("control-plane+sub2api+ledger", routeData({
          status,
          localGraph: status === "safe_to_retry_absent" ? "absent" : "unknown",
          remoteIdentity: status === "safe_to_retry_absent" ? "absent" : "unknown",
          customerLogin: status === "safe_to_retry_absent" ? "not_attempted" : "unknown",
          wallet: status === "safe_to_retry_absent" ? "absent" : "unknown",
          walletUsdMicros: "",
          walletAdjustment: status === "safe_to_retry_absent" ? "absent" : "unknown",
          failureStage: status === "safe_to_retry_absent" ? "route_request" : "remote_identity",
          readbackError: status === "safe_to_retry_absent" ? "none" : "sub2api_authority_unavailable"
        })));
      }
      throw new Error(`unexpected_request:${parsed.pathname}`);
    };
    let stdout = "";
    const code = await runProductionBasicAcceptanceBReconcileCli({
      argv: ["--reconcile-account"], env: base, stdout: { write: (value) => { stdout += value; } }, stderr: { write: () => {} }, fetchImpl,
      now: new Date("2026-08-04T00:00:00.000Z")
    });
    assert.equal(code, 1);
    const artifact = JSON.parse(stdout);
    assert.equal(artifact.status, status);
    assert.deepEqual(validateProductionBasicAcceptanceBReconcileReadback(artifact, { mergedSha: MERGED_SHA }), artifact);
  }
});

test("an HTTP response without a valid DTO uses a nonzero response digest, never a zero fallback, and validates", async () => {
  const fetchImpl = async (url) => {
    const parsed = new URL(url);
    if (parsed.pathname === "/api/auth/login") return loginPayload("acct-admin", "admin");
    if (parsed.pathname === "/api/operator/account-reconciliation") {
      return new Response(JSON.stringify({ error: "bad_envelope" }), {
        status: 500,
        headers: { "cache-control": "private, no-store" }
      });
    }
    throw new Error(`unexpected_request:${parsed.pathname}`);
  };
  let stdout = "";
  const code = await runProductionBasicAcceptanceBReconcileCli({
    argv: ["--reconcile-account"],
    env: {
      OPL_MERGED_SHA: MERGED_SHA,
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_SUB2API_ADMIN_EMAIL: ADMIN_EMAIL,
      OPL_SUB2API_ADMIN_PASSWORD: "admin-password",
      OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_EMAIL: EMAIL,
      OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_PASSWORD: "customer-password",
      OPL_PRODUCTION_BASIC_ACCEPTANCE_B_RECONCILE_ARTIFACT_PATH: ""
    },
    stdout: { write: (value) => { stdout += value; } },
    stderr: { write: () => {} },
    fetchImpl,
    now: new Date("2026-08-04T00:00:00.000Z")
  });
  assert.equal(code, 1);
  const artifact = JSON.parse(stdout);
  assert.equal(artifact.failureStage, "response_envelope");
  assert.notEqual(artifact.customerIdentitySha256, "0".repeat(64));
  assert.notEqual(artifact.accountProvisionIdentitySha256, "0".repeat(64));
  assert.notEqual(artifact.walletAdjustmentIdentitySha256, "0".repeat(64));
  assert.deepEqual(validateProductionBasicAcceptanceBReconcileReadback(artifact, { mergedSha: MERGED_SHA }), artifact);
});

test("a customer-login failure after a valid server DTO keeps the server digests and becomes an unknown artifact", async () => {
  const fetchImpl = async (url, init = {}) => {
    const parsed = new URL(url);
    if (parsed.pathname === "/api/auth/login") {
      const body = JSON.parse(init.body);
      if (body.email === ADMIN_EMAIL) return loginPayload("acct-admin", "admin");
      throw new Error("customer_login_socket_closed");
    }
    if (parsed.pathname === "/api/operator/account-reconciliation") {
      return response(envelope("control-plane+sub2api+ledger", routeData()));
    }
    throw new Error(`unexpected_request:${parsed.pathname}`);
  };
  const result = await reconcileProductionBasicAcceptanceBAccount(baseOptions(fetchImpl));
  assert.equal(result.status, "unknown");
  assert.equal(result.failureStage, "customer_login");
  assert.equal(result.readbackError, "customer_login_failed");
  assert.equal(result.customerIdentitySha256, DIGEST);
  assert.equal(result.accountProvisionIdentitySha256, DIGEST);
  assert.equal(result.walletAdjustmentIdentitySha256, DIGEST);
  assert.deepEqual(validateProductionBasicAcceptanceBReconcileReadback(result, { mergedSha: MERGED_SHA }), result);
});

test("a nonzero Fresh baseline remains a baseline failure after customer login succeeds", async () => {
  const requests = [];
  const fetchImpl = async (url, init = {}) => {
    const parsed = new URL(url);
    requests.push({ method: init.method || "GET", path: parsed.pathname });
    if (parsed.pathname === "/api/auth/login") {
      const body = JSON.parse(init.body);
      return loginPayload(body.email === ADMIN_EMAIL ? "acct-admin" : "acct-reconcile", body.email === ADMIN_EMAIL ? "admin" : "owner");
    }
    if (parsed.pathname === "/api/operator/account-reconciliation") {
      return response(envelope("control-plane+sub2api+ledger", routeData({
        status: "partial",
        walletUsdMicros: "7420000",
        workspaceCount: 0,
        launchCount: 1,
        keyCount: 1,
        receiptCount: 0,
        failureStage: "baseline",
        readbackError: "baseline_not_zero"
      })));
    }
    if (parsed.pathname === "/api/auth/me") return response(envelope("sub2api", { email: EMAIL, role: "owner", status: "active" }));
    if (parsed.pathname === "/api/workspaces") return response(envelope("control-plane", { items: [], total: 0, page: 1, pageSize: 50 }));
    if (parsed.pathname === "/api/workspace-launches") return response([{ operationId: "redacted-by-artifact" }]);
    if (parsed.pathname === "/api/gateway/keys") return response(envelope("sub2api", { items: [{ kind: "workspace" }], total: 1, page: 1, pageSize: 50 }));
    if (parsed.pathname === "/api/billing/receipts") return response(envelope("ledger", { receipts: [], nextCursor: "", hasMore: false }, "empty"));
    throw new Error(`unexpected_request:${parsed.pathname}`);
  };

  const result = await reconcileProductionBasicAcceptanceBAccount(baseOptions(fetchImpl));
  assert.equal(result.status, "partial");
  assert.equal(result.customerLogin, "active");
  assert.equal(result.failureStage, "baseline");
  assert.equal(result.readbackError, "baseline_not_zero");
  assert.equal(result.errorCode, "acceptance_b_account_reconcile_baseline");
  assert.equal(result.launchCount, 1);
  assert.equal(result.keyCount, 1);
  assert.deepEqual(validateProductionBasicAcceptanceBReconcileReadback(result, { mergedSha: MERGED_SHA }), result);
  assert.equal(requests.some((request) => request.method === "POST" && request.path === "/api/workspace-launches"), false);
});

test("an unavailable Fresh baseline fails closed after customer login succeeds", async () => {
  const fetchImpl = async (url, init = {}) => {
    const parsed = new URL(url);
    if (parsed.pathname === "/api/auth/login") {
      const body = JSON.parse(init.body);
      return loginPayload(body.email === ADMIN_EMAIL ? "acct-admin" : "acct-reconcile", body.email === ADMIN_EMAIL ? "admin" : "owner");
    }
    if (parsed.pathname === "/api/operator/account-reconciliation") {
      return response(envelope("control-plane+sub2api+ledger", routeData()));
    }
    if (parsed.pathname === "/api/auth/me") return response(envelope("sub2api", { email: EMAIL, role: "owner", status: "active" }));
    if (parsed.pathname === "/api/workspaces") throw new Error("connection_reset");
    throw new Error(`unexpected_request:${parsed.pathname}`);
  };

  const result = await reconcileProductionBasicAcceptanceBAccount(baseOptions(fetchImpl));
  assert.equal(result.status, "unknown");
  assert.equal(result.customerLogin, "active");
  assert.equal(result.failureStage, "baseline");
  assert.equal(result.readbackError, "baseline_authority_unavailable");
  assert.equal(result.errorCode, "acceptance_b_account_reconcile_unknown");
  assert.deepEqual(validateProductionBasicAcceptanceBReconcileReadback(result, { mergedSha: MERGED_SHA }), result);
});

test("manual-review server DTOs get a wallet-adjustment failure stage without losing the readback shape", async () => {
  const fetchImpl = async (url, init = {}) => {
    const parsed = new URL(url);
    if (parsed.pathname === "/api/auth/login") {
      const body = JSON.parse(init.body);
      return loginPayload(body.email === ADMIN_EMAIL ? "acct-admin" : "acct-reconcile", body.email === ADMIN_EMAIL ? "admin" : "owner");
    }
    if (parsed.pathname === "/api/operator/account-reconciliation") {
      return response(envelope("control-plane+sub2api+ledger", routeData({
        status: "manual_review", walletAdjustment: "absent", failureStage: "none", readbackError: "none"
      })));
    }
    if (parsed.pathname === "/api/auth/me") return response(envelope("sub2api", { email: EMAIL, role: "owner", status: "active" }));
    if (parsed.pathname === "/api/workspaces") return response(envelope("control-plane", { items: [], total: 0, page: 1, pageSize: 50 }));
    if (parsed.pathname === "/api/workspace-launches") return response([]);
    if (parsed.pathname === "/api/gateway/keys") return response(envelope("sub2api", { items: [], total: 0, page: 1, pageSize: 50 }));
    if (parsed.pathname === "/api/billing/receipts") return response(envelope("ledger", { receipts: [], nextCursor: "", hasMore: false }, "empty"));
    throw new Error(`unexpected_request:${parsed.pathname}`);
  };
  const result = await reconcileProductionBasicAcceptanceBAccount(baseOptions(fetchImpl));
  assert.equal(result.status, "manual_review");
  assert.equal(result.failureStage, "wallet_adjustment");
  assert.equal(result.errorCode, "acceptance_b_account_reconcile_wallet_adjustment");
  assert.deepEqual(validateProductionBasicAcceptanceBReconcileReadback(result, { mergedSha: MERGED_SHA }), result);
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
    failureStage: "none",
    readbackError: "none",
    errorCode: "none",
    writeCounts: { ...ZERO_COUNTS },
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 }
  };
  assert.throws(() => validateProductionBasicAcceptanceBReconcileReadback({ ...base, writeCounts: { ...ZERO_COUNTS, receiptCreates: 1 } }, { mergedSha: MERGED_SHA }), /acceptance_b_account_reconcile_readback_invalid/);
  assert.throws(() => validateProductionBasicAcceptanceBReconcileReadback({ ...base, customerEmail: EMAIL }, { mergedSha: MERGED_SHA }), /acceptance_b_account_reconcile_readback_invalid/);
  assert.throws(() => validateProductionBasicAcceptanceBReconcileReadback({ ...base, failureStage: "provider" }, { mergedSha: MERGED_SHA }), /acceptance_b_account_reconcile_readback_invalid/);
});
