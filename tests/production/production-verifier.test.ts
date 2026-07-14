import assert from "node:assert/strict";
import test from "node:test";

import {
  PAID_CONFIRMATION,
  productionVerificationMutationKey,
  runProductionVerifierCli,
  verificationOwnerFromSeed,
  verifyProductionChain
} from "../../tools/production-verifier.ts";

const ownerSeed = JSON.stringify([{
  id: "usr-verifier",
  email: "owner@example.com",
  password: "correct horse battery staple",
  role: "owner",
  accountId: "acct-alpha",
  sub2apiUserId: 41
}]);

function json(payload, status = 200, headers = {}) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "content-type": "application/json", ...headers }
  });
}

function monthlyChainFixture({ failWorkspaceUrl = false } = {}) {
  const initialBalance = 500_000_000;
  const calls = [];
  const keys = new Map();
  const state = {
    compute: null,
    storage: null,
    attachment: null,
    workspace: null
  };

  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = init.method || "GET";
    const body = init.body ? JSON.parse(String(init.body)) : {};
    const key = new Headers(init.headers).get("Idempotency-Key") || "";
    calls.push({ method, path: url.pathname, key, body });

    if (url.hostname === "workspace.medopl.cn") {
      return failWorkspaceUrl
        ? new Response("unavailable", { status: 503 })
        : new Response("<!doctype html><title>OPL Workspace</title><main>ready</main>", { status: 200, headers: { "content-type": "text/html" } });
    }
    if (url.pathname === "/api/production/readiness") return json({ ready: true });
    if (url.pathname === "/api/auth/login") {
      return json({ user: { id: "usr-verifier", accountId: "acct-alpha", role: "owner" } }, 200, {
        "set-cookie": "opl_session=session-alpha; Path=/; HttpOnly",
        "x-opl-csrf-token": "csrf-alpha"
      });
    }

    const headers = new Headers(init.headers);
    assert.match(headers.get("cookie") || "", /opl_session=session-alpha/);
    if (method === "POST") assert.equal(headers.get("x-opl-csrf"), "csrf-alpha");
    if (key) {
      const identity = `${method}:${url.pathname}:${JSON.stringify(body)}`;
      if (keys.has(key)) assert.equal(keys.get(key), identity, `mutation key ${key} changed payload`);
      keys.set(key, identity);
    }

    if (url.pathname === "/api/state") {
      const spent = (state.compute ? 50_000_000 : 0) + (state.storage ? 2_571_429 : 0);
      return json({
        balance: { source: "sub2api", currency: "USD", userId: 41, usdMicros: initialBalance - spent },
        computeAllocations: state.compute ? [state.compute] : [],
        storageVolumes: state.storage ? [state.storage] : [],
        storageAttachments: state.attachment ? [state.attachment] : [],
        workspaces: state.workspace ? [state.workspace] : []
      });
    }
    if (method === "POST" && url.pathname === "/api/compute-allocations") {
      state.compute ||= {
        id: "compute-alpha", accountId: "acct-alpha", packageId: "basic", status: "running", billingStatus: "active",
        monthlyPriceCnyCents: 35_000, chargeUsdMicros: 50_000_000, paidThrough: "2026-08-14T00:00:00Z",
        sub2apiRedeemCode: "opl:production:compute-charge:v1", lastReceiptId: "receipt-compute"
      };
      return json(state.compute, 202);
    }
    if (url.pathname === "/api/compute-allocations/compute-alpha") return json(state.compute);
    if (method === "POST" && url.pathname === "/api/storage-volumes") {
      state.storage ||= {
        id: "storage-alpha", accountId: "acct-alpha", sizeGb: 10, status: "available", billingStatus: "active",
        monthlyPriceCnyCents: 1_800, chargeUsdMicros: 2_571_429, paidThrough: "2026-08-14T00:00:00Z",
        sub2apiRedeemCode: "opl:production:storage-charge:v1", lastReceiptId: "receipt-storage"
      };
      return json(state.storage, 202);
    }
    if (url.pathname === "/api/storage-volumes/storage-alpha/sync") return json(state.storage);
    if (method === "POST" && url.pathname === "/api/storage-attachments") {
      state.attachment ||= { id: "attachment-alpha", accountId: "acct-alpha", computeAllocationId: "compute-alpha", storageId: "storage-alpha", status: "attached" };
      return json(state.attachment, 202);
    }
    if (method === "POST" && url.pathname === "/api/workspaces") {
      state.workspace ||= { id: "workspace-alpha", accountId: "acct-alpha", currentComputeAllocationId: "compute-alpha", currentAttachmentId: "attachment-alpha", storageId: "storage-alpha", state: "running", url: "https://workspace.medopl.cn/w/workspace-alpha/?token=share-secret" };
      return json(state.workspace, 201);
    }
    if (url.pathname === "/api/workspaces/runtime-status") return json({ workspaceId: "workspace-alpha", status: "running", ready: true, checks: [{ name: "runtime", ok: true }] });
    if (url.pathname === "/api/billing/receipts/receipt-compute") {
      return json({ receiptId: "receipt-compute", type: "billing.resource_purchased.v1", status: "completed", accountId: "acct-alpha", cost: { chargeUsdMicros: 50_000_000, sub2apiRedeemCode: "opl:production:compute-charge:v1" } });
    }
    if (url.pathname === "/api/billing/receipts/receipt-storage") {
      return json({ receiptId: "receipt-storage", type: "billing.resource_purchased.v1", status: "completed", accountId: "acct-alpha", cost: { chargeUsdMicros: 2_571_429, sub2apiRedeemCode: "opl:production:storage-charge:v1" } });
    }
    if (url.pathname === "/api/storage-attachments/detach") {
      state.attachment = { ...state.attachment, status: "detached" };
      return json(state.attachment);
    }
    if (url.pathname === "/api/compute-allocations/compute-alpha/destroy") {
      state.compute = { ...state.compute, status: "destroyed", billingStatus: "stopped" };
      state.workspace = { ...state.workspace, state: "suspended", status: "suspended", currentComputeAllocationId: "", computeAllocationId: "", runtimeId: "", runtime: {} };
      return json(state.compute);
    }
    if (url.pathname === "/api/storage-volumes/destroy") {
      state.storage = { ...state.storage, status: "destroyed", billingStatus: "stopped" };
      return json(state.storage);
    }
    return json({ error: "not_found" }, 404);
  };

  return { fetchImpl, calls, initialBalance };
}

test("production verifier requires one mapped owner", () => {
  assert.deepEqual(verificationOwnerFromSeed(ownerSeed, "acct-alpha"), {
    accountId: "acct-alpha",
    email: "owner@example.com",
    password: "correct horse battery staple",
    sub2apiUserId: 41
  });
  assert.throws(() => verificationOwnerFromSeed(JSON.stringify([{ role: "owner", accountId: "acct-alpha", email: "a", password: "b" }])), /verification_owner_mapping_required/);
  assert.throws(() => verificationOwnerFromSeed(JSON.stringify([
    { role: "owner", accountId: "acct-alpha", email: "a", password: "b", sub2apiUserId: 1 },
    { role: "owner", accountId: "acct-alpha", email: "c", password: "d", sub2apiUserId: 2 }
  ])), /verification_owner_credentials_required/);
});

test("production verifier refuses all requests without explicit paid confirmation", async () => {
  let calls = 0;
  await assert.rejects(() => verifyProductionChain({
    origin: "https://cloud.medopl.cn",
    authUsersJson: ownerSeed,
    runId: "paid-gate",
    fetchImpl: async () => { calls += 1; return json({}); }
  }), /paid_confirmation_required/);
  assert.equal(calls, 0);
});

test("production verifier proves exact monthly charges, receipts, workspace access and cleanup", async () => {
  const fixture = monthlyChainFixture();
  const result = await verifyProductionChain({
    origin: "https://cloud.medopl.cn",
    authUsersJson: ownerSeed,
    accountId: "acct-alpha",
    runId: "monthly-chain",
    slot: "01",
    packageId: "basic",
    paidConfirmation: PAID_CONFIRMATION,
    workspaceUrlAttempts: 1,
    retryDelayMs: 0,
    fetchImpl: fixture.fetchImpl
  });

  assert.equal(result.ok, true);
  assert.equal(result.workspaceId, "workspace-alpha");
  assert.equal(result.url, "https://workspace.medopl.cn/w/workspace-alpha/");
  assert.doesNotMatch(JSON.stringify(result), /share-secret|token=/);
  assert.equal(result.balance.beforeUsdMicros - result.balance.afterUsdMicros, 52_571_429);
  assert.deepEqual(result.redeemCodes, ["opl:production:compute-charge:v1", "opl:production:storage-charge:v1"]);
  assert.deepEqual(result.receiptIds, ["receipt-compute", "receipt-storage"]);
  assert.equal(result.cleanup.complete, true);
  assert.ok(result.checks.some((check) => check.name === "balance_delta_matches_exact_monthly_charges" && check.ok));
  assert.ok(result.checks.some((check) => check.name === "verification_workspace_runtime_removed" && check.ok));
  assert.equal(fixture.calls.some((call) => call.path.includes("topups") || call.path.includes("resource-settlements")), false);

  const computeCreates = fixture.calls.filter((call) => call.path === "/api/compute-allocations");
  const storageCreates = fixture.calls.filter((call) => call.path === "/api/storage-volumes");
  assert.ok(computeCreates.length >= 2);
  assert.ok(storageCreates.length >= 2);
  assert.equal(new Set(computeCreates.map((call) => call.key)).size, 1);
  assert.equal(new Set(storageCreates.map((call) => call.key)).size, 1);
});

test("production verifier cleans only resources created by a failed run", async () => {
  const fixture = monthlyChainFixture({ failWorkspaceUrl: true });
  await assert.rejects(() => verifyProductionChain({
    origin: "https://cloud.medopl.cn",
    authUsersJson: ownerSeed,
    runId: "cleanup-failure",
    paidConfirmation: PAID_CONFIRMATION,
    workspaceUrlAttempts: 1,
    retryDelayMs: 0,
    fetchImpl: fixture.fetchImpl
  }), (error) => {
    assert.deepEqual(error.cleanupErrors, []);
    return true;
  });
  assert.deepEqual(fixture.calls.filter((call) => call.path.endsWith("/destroy") || call.path.endsWith("/detach")).map((call) => call.path), [
    "/api/storage-attachments/detach",
    "/api/compute-allocations/compute-alpha/destroy",
    "/api/storage-volumes/destroy"
  ]);
});

test("production verifier mutation keys reject ambiguous components", () => {
  assert.equal(productionVerificationMutationKey("run-1", "01", "create-compute"), "production-verification:run-1:01:create-compute");
  assert.throws(() => productionVerificationMutationKey("run:1", "01", "create-compute"), /production_verification_run_id_invalid/);
});

test("production verifier CLI help is read only", async () => {
  let stdout = "";
  let calls = 0;
  const code = await runProductionVerifierCli({
    argv: ["--help"],
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: () => {} },
    fetchImpl: async () => { calls += 1; return json({}); }
  });
  assert.equal(code, 0);
  assert.match(stdout, /OPL_VERIFY_PAID_CONFIRMATION/);
  assert.equal(calls, 0);
});
