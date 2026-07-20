import assert from "node:assert/strict";
import test from "node:test";

import {
  LIVE_QA_CONFIRMATION,
  runProductionLiveQaCli,
  verifyProductionLiveQa
} from "../../tools/production-live-qa.ts";

const fixedSlotDescriptor = {
  id: "verification-slot-basic-01",
  customerProduct: false,
  instanceType: "SA5.MEDIUM4",
  server: "2c4g",
  cpu: 2,
  memoryGb: 4,
  cbsGb: 10,
  chargeType: "PREPAID",
  periodMonths: 1,
  renewFlag: "NOTIFY_AND_MANUAL_RENEW"
};
const BASIC_ACCOUNT_ID = "acct-verification-slot-basic-01";
const ownerSeed = JSON.stringify([{
  id: "usr-verifier",
  email: "owner@example.com",
  password: "console-password",
  role: "owner",
  accountId: BASIC_ACCOUNT_ID,
  sub2apiUserId: 41
}]);
const mutationApprovalJson = JSON.stringify({
  approvalId: "approval-pilot-v2",
  expiresAt: "2099-07-19T00:00:00Z",
  accountIds: [BASIC_ACCOUNT_ID],
  workspaceIds: ["workspace-slot-1"],
  resourceIds: [fixedSlotDescriptor.id, "9"]
});

function json(payload, status = 200, headers = {}) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "content-type": "application/json", ...headers }
  });
}

function source(payload, sourceName = "sub2api", status = "available", headers = {}) {
  return json({ source: sourceName, status, available: true, fetchedAt: new Date().toISOString(), data: payload }, 200, {
    "cache-control": "private, no-store",
    ...headers
  });
}

class FakeEmitter {
  handlers = new Map();

  on(name, handler) {
    const handlers = this.handlers.get(name) || [];
    handlers.push(handler);
    this.handlers.set(name, handlers);
  }

  emit(name, payload) {
    for (const handler of this.handlers.get(name) || []) handler(payload);
  }
}

function browserFactory(state, { frames = true, responseSuffix = "" } = {}) {
  return async () => {
    const cdp = new FakeEmitter();
    cdp.send = async () => {};
    const page = new FakeEmitter();
    const socket = new FakeEmitter();
    socket.url = () => "wss://workspace.medopl.cn/ws";
    let qaToken = "";

    const assistant = {
      waitFor: async () => {},
      textContent: async () => `${qaToken}${responseSuffix}`
    };
    page.locator = (selector) => {
      if (selector === "[data-testid='guid-input']") {
        return {
          waitFor: async () => {},
          fill: async (value) => { qaToken = value.match(/OPL_QA_[A-Z0-9_]+/)?.[0] || ""; }
        };
      }
      if (selector === "[data-testid='guid-send-btn']") {
        return { click: async () => { state.modelRequests += 1; } };
      }
      return { filter: () => ({ last: () => assistant }) };
    };
    page.goto = async () => ({ ok: () => true, status: () => 200 });
    page.evaluate = async () => ({ status: 200, payload: { success: true, user: { username: "opl" } } });
    page.reload = async () => {
      page.emit("websocket", socket);
      cdp.emit("Network.webSocketCreated", { requestId: "ws-1", url: socket.url() });
      cdp.emit("Network.webSocketHandshakeResponseReceived", {
        requestId: "ws-1",
        response: { status: 101, url: socket.url() }
      });
      if (frames) {
        socket.emit("framereceived", { payload: "ping" });
        socket.emit("framesent", { payload: "pong" });
      }
    };
    page.waitForURL = async () => {};

    const apiResponse = (payload, status = 200) => ({
      ok: () => status >= 200 && status < 300,
      status: () => status,
      json: async () => payload
    });
    const context = {
      request: {
        post: async (url, options) => {
          assert.equal(new URL(url).pathname, "/login");
          assert.deepEqual(options.data, { username: "opl", password: "workspace-password", remember: true });
          return apiResponse({ success: true, user: { username: "opl" } });
        },
        get: async () => { throw new Error("auth_user_must_be_checked_in_page_context"); }
      },
      newPage: async () => page,
      newCDPSession: async () => cdp,
      close: async () => {}
    };
    return { newContext: async () => context, close: async () => {} };
  };
}

function liveFixture({
  changedResourceIds = false,
  changedProviderOperations = false,
  changedLaunchOperation = false,
  changedRuntimeOperation = false,
  changedUntrackedOperation = false,
  changedMutationAction = "",
  changedReceipt = false,
  frames = true,
  responseSuffix = "",
  slotMissing = false,
  usageStuck = false,
  ambiguousUsage = false,
  invalidUsageRecord = false,
  usageOverrides = {},
  usageSnapshotTooLarge = false,
  emptyUsageBaseline = false,
  statsStuck = false,
  balanceMismatch = false,
  usageKeyId = "9",
  duplicateKey = false,
  statusLeaksPassword = false,
  revealCacheControl = "private, no-store"
} = {}) {
  const state = { modelRequests: 0, stateReads: 0 };
  const calls = [];
  const deadline = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString();
  const periodStart = new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString();
  const liveUsage = {
    apiKeyId: usageKeyId, requestId: "req-rollout-qa-1", createdAt: new Date().toISOString(), model: "gpt-5.5", inboundEndpoint: "/v1/responses", requestType: "sync",
    inputTokens: 8, outputTokens: 1, cacheCreationTokens: 0, cacheReadTokens: 0, actualCostUsdMicros: invalidUsageRecord ? 12.5 : 120,
    ...usageOverrides
  };
  const usageItems = () => {
    const items = emptyUsageBaseline ? [] : [{
      apiKeyId: "9", requestId: "req-before-1", createdAt: periodStart, model: "gpt-5.5", inboundEndpoint: "/v1/responses", requestType: "sync",
      inputTokens: 10, outputTokens: 5, cacheCreationTokens: 0, cacheReadTokens: 0, actualCostUsdMicros: 100
    }];
    if (state.modelRequests > 0 && !usageStuck) {
      items.unshift(liveUsage);
      if (ambiguousUsage) items.unshift({ ...liveUsage, requestId: "req-concurrent-2" });
    }
    return items;
  };
  const resourceState = () => {
    state.stateReads += 1;
    const suffix = changedResourceIds && state.stateReads > 1 ? "changed" : "1";
    const result = {
      computeAllocations: [{
        id: "compute-slot-1",
        accountId: BASIC_ACCOUNT_ID,
        workspaceId: "workspace-slot-1",
        providerResourceId: "ins-slot-1",
        nodePoolId: `np-slot-${suffix}`,
        status: "running",
        costTags: { opl_account_id: BASIC_ACCOUNT_ID, opl_workspace_id: "workspace-slot-1", opl_resource_id: "compute-slot-1" },
        providerData: { instanceType: "SA5.MEDIUM4", zone: "ap-guangzhou-3", chargeType: "PREPAID", periodMonths: "1", renewFlag: "NOTIFY_AND_MANUAL_RENEW", deadline }
      }],
      storageVolumes: [{
        id: "storage-slot-1",
        accountId: BASIC_ACCOUNT_ID,
        workspaceId: "workspace-slot-1",
        providerResourceId: "disk-slot-1",
        sizeGb: 10,
        status: "available",
        costTags: { opl_account_id: BASIC_ACCOUNT_ID, opl_workspace_id: "workspace-slot-1", opl_resource_id: "storage-slot-1" },
        providerData: { diskChargeType: "PREPAID", periodMonths: "1", renewFlag: "NOTIFY_AND_MANUAL_RENEW", deadline, zone: "ap-guangzhou-3", pvName: "pv-slot-1" }
      }],
      workspaces: [{
        id: "workspace-slot-1",
        accountId: BASIC_ACCOUNT_ID,
        ownerAccountId: BASIC_ACCOUNT_ID,
        verificationSlotId: "verification-slot-basic-01",
        customerProduct: false,
        currentComputeAllocationId: "compute-slot-1",
        storageId: "storage-slot-1",
        state: "running",
        openable: true,
        receiptId: changedReceipt && state.stateReads > 1 ? "receipt-current-2" : "receipt-current-1",
        url: "https://workspace.medopl.cn/w/workspace-slot-1/"
      }],
      runtimeOperations: [
        { id: "provider-op-compute-1", accountId: BASIC_ACCOUNT_ID, workspaceId: "workspace-slot-1", action: "create_compute_allocation", status: "succeeded", providerRequestId: "ins-slot-1", result: '{"resource":"compute-slot-1"}' },
        { id: "provider-op-storage-1", accountId: BASIC_ACCOUNT_ID, workspaceId: "workspace-slot-1", action: "create_storage_volume", status: "succeeded", providerRequestId: "disk-slot-1", result: '{"resource":"storage-slot-1"}' },
        { id: "workspace-launch-1", accountId: BASIC_ACCOUNT_ID, workspaceId: "workspace-slot-1", action: "workspace.launch", status: "succeeded", providerRequestId: "",
          result: changedLaunchOperation && state.stateReads > 1 ? '{"phase":"changed"}' : '{"phase":"completed","credential":"must-not-emit"}' },
        { id: "workspace-renewal-1", accountId: BASIC_ACCOUNT_ID, workspaceId: "workspace-slot-1", action: "workspace.renewal", status: "succeeded", providerRequestId: "",
          result: changedRuntimeOperation && state.stateReads > 1 ? '{"phase":"changed"}' : '{"phase":"completed"}' },
        { id: "job-progress-1", accountId: BASIC_ACCOUNT_ID, workspaceId: "workspace-slot-1", action: "job.execute", status: changedUntrackedOperation && state.stateReads > 1 ? "succeeded" : "running",
          result: { internalCredential: "ignored-job-secret" } },
        ...(changedProviderOperations && state.stateReads > 1
          ? [{ id: "provider-op-renew-2", accountId: BASIC_ACCOUNT_ID, workspaceId: "workspace-slot-1", action: "renew_compute_allocation", status: "succeeded", providerRequestId: "ins-slot-1", result: '{"resource":"compute-slot-1"}' }]
          : []),
        ...(changedMutationAction && state.stateReads > 1
          ? [{ id: `mutation-${changedMutationAction.replaceAll(".", "-")}`, accountId: BASIC_ACCOUNT_ID, workspaceId: "workspace-slot-1", action: changedMutationAction, status: "succeeded", providerRequestId: "provider-mutation-1", result: "{}" }]
          : [])
      ]
    };
    if (slotMissing) {
      result.computeAllocations = [];
      result.storageVolumes = [];
      result.workspaces = [];
    }
    return result;
  };

  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = init.method || "GET";
    const headers = new Headers(init.headers);
    calls.push({ method, path: url.pathname, search: url.search, signal: init.signal });
    if (url.hostname === "workspace.medopl.cn") return new Response("<main>workspace</main>", { status: 200 });
    if (url.pathname === "/api/production/readiness") return json({ ready: true, cloudImagesReady: true, workspaceImagesReady: true, immutableImagesReady: true });
    if (url.pathname === "/api/auth/login") {
      return json({ user: { accountId: BASIC_ACCOUNT_ID, role: "owner" } }, 200, {
        "set-cookie": "opl_session=session-alpha; Path=/; HttpOnly",
        "x-opl-csrf-token": "csrf-alpha"
      });
    }
    assert.match(headers.get("cookie") || "", /opl_session=session-alpha/);
    if (url.pathname === "/api/pricing/catalog") {
      return json({
        priceVersion: "pilot-usd-2026-07-v1", currency: "USD", displayCurrency: "USD", walletCurrency: "USD",
        storagePer10GbMonthly: { priceVersion: "pilot-usd-2026-07-v1", currency: "USD", displayCurrency: "USD", usdMicros: 2_580_000 },
        packages: [
          { id: "basic", price: { priceVersion: "pilot-usd-2026-07-v1", currency: "USD", displayCurrency: "USD", chargeUsdMicros: 50_000_000 } },
          { id: "pro", price: { priceVersion: "pilot-usd-2026-07-v1", currency: "USD", displayCurrency: "USD", chargeUsdMicros: 214_280_000 } }
        ]
      });
    }
    if (url.pathname === "/api/state") return json(resourceState());
    if (url.pathname === "/api/gateway/wallet") {
      const charged = state.modelRequests > 0 && !usageStuck;
      const delta = charged ? liveUsage.actualCostUsdMicros + (balanceMismatch ? 1 : 0) : 0;
      return source({ userId: "41", currency: "USD", usdMicros: 500_000_000 - delta, status: "active" });
    }
    if (url.pathname === "/api/gateway/keys") {
      const keys = [{ id: "9", name: "opl-workspace", status: "active", quotaUsdMicros: 1_000_000, quotaUsedUsdMicros: 1_000 }];
      if (duplicateKey) keys.push({ ...keys[0], id: "10" });
      return source({ items: keys, total: keys.length });
    }
    if (url.pathname === "/api/gateway/keys/9/usage") {
      if (usageSnapshotTooLarge) return source({ items: [], total: 10_001, page: 1, pageSize: 100, pages: 101 });
      const items = usageItems();
      const page = Number(url.searchParams.get("page") || 1);
      const pageSize = Number(url.searchParams.get("pageSize") || 50);
      return source({ items: items.slice((page - 1) * pageSize, page * pageSize), total: items.length, page, pageSize, pages: items.length === 0 ? 0 : Math.ceil(items.length / pageSize) }, "sub2api", items.length === 0 ? "empty" : "available");
    }
    if (url.pathname === "/api/gateway/keys/9/usage-summary") {
      const includeLive = state.modelRequests > 0 && !usageStuck && !statsStuck;
      const count = includeLive ? (ambiguousUsage ? 2 : 1) : 0;
      const baselineRequests = emptyUsageBaseline ? 0 : 1;
      const baselineInputTokens = emptyUsageBaseline ? 0 : 10;
      const baselineOutputTokens = emptyUsageBaseline ? 0 : 5;
      const baselineCost = emptyUsageBaseline ? 0 : 100;
      return source({
        totalRequests: baselineRequests + count,
        totalInputTokens: baselineInputTokens + count * liveUsage.inputTokens,
        totalOutputTokens: baselineOutputTokens + count * liveUsage.outputTokens,
        totalTokens: baselineInputTokens + baselineOutputTokens + count * (liveUsage.inputTokens + liveUsage.outputTokens + liveUsage.cacheCreationTokens + liveUsage.cacheReadTokens),
        totalActualCostUsdMicros: baselineCost + count * liveUsage.actualCostUsdMicros
      });
    }
    if (/^\/api\/billing\/receipts\/receipt-current-[12]$/.test(url.pathname)) {
      return source({
        receiptId: url.pathname.endsWith("-2") ? "receipt-current-2" : "receipt-current-1",
        type: "workspace.created", status: "completed", workspaceId: "workspace-slot-1", createdAt: periodStart
      }, "ledger");
    }
    if (url.pathname === "/api/workspaces/workspace-slot-1/runtime-status") {
      assert.equal(method, "GET");
      assert.equal(init.body, undefined);
      return source({
        ready: true,
        url: "https://workspace.medopl.cn/w/workspace-slot-1/",
        access: { username: "opl", credentialStatus: "configured", ...(statusLeaksPassword ? { password: "workspace-password" } : {}) }
      }, "fabric");
    }
    if (url.pathname === "/api/workspaces/workspace-slot-1/runtime-credentials/reveal") {
      assert.equal(method, "POST");
      assert.equal(headers.get("x-opl-csrf"), "csrf-alpha");
      assert.deepEqual(JSON.parse(init.body), {});
      return json({
        workspaceId: "workspace-slot-1",
        access: { username: "opl", password: "workspace-password", credentialStatus: "configured" }
      }, 200, { "cache-control": revealCacheControl });
    }
    return json({ error: "not_found" }, 404);
  };

  return { browserFactory: browserFactory(state, { frames, responseSuffix }), calls, fetchImpl, state };
}

function options(fixture) {
  return {
    origin: "https://cloud.medopl.cn",
    authUsersJson: ownerSeed,
    accountId: BASIC_ACCOUNT_ID,
    runId: "rollout-qa-1",
    confirmation: LIVE_QA_CONFIRMATION,
    slotDescriptor: fixedSlotDescriptor,
    workspaceUrlAttempts: 1,
    retryDelayMs: 0,
    usageAttempts: 2,
    usageRetryDelayMs: 0,
    browserTimeoutMs: 20,
    modelTimeoutMs: 20,
    expectedModel: "gpt-5.5",
    mutationApprovalJson,
    mutationApprovalId: "approval-pilot-v2",
    browserFactory: fixture.browserFactory,
    fetchImpl: fixture.fetchImpl
  };
}

test("rollout QA proves Workspace login, WebSocket frames, one model response, usage growth, and stable resource ids", async () => {
  const fixture = liveFixture();
  const result = await verifyProductionLiveQa(options(fixture));

  assert.equal(result.ok, true);
  assert.equal(result.workspace.login, true);
  assert.equal(result.workspace.authUser, true);
  assert.equal(result.workspace.websocket.status, 101);
  assert.equal(result.workspace.websocket.framesSent > 0, true);
  assert.equal(result.workspace.websocket.framesReceived > 0, true);
  assert.equal(result.workspace.modelResponse, true);
  assert.equal(result.usage.request.requestId, "req-rollout-qa-1");
  assert.equal(result.usage.request.apiKeyId, "9");
  assert.equal(result.usage.request.model, "gpt-5.5");
  assert.equal(result.usage.request.requestType, "sync");
  assert.equal(result.usage.request.inboundEndpoint, "/v1/responses");
  assert.equal(result.usage.request.inputTokens + result.usage.request.outputTokens > 0, true);
  assert.equal(result.usage.request.actualCostUsdMicros, 120);
  assert.equal(result.usage.stats.delta.totalRequests, 1);
  assert.equal(result.balance.before.usdMicros - result.balance.after.usdMicros, 120);
  assert.equal(result.ledgerReceipt.receiptId, "receipt-current-1");
  assert.equal(result.ledgerReceipt.type, "workspace.created");
  assert.equal(result.runtimeOperations.unchanged, true);
  assert.equal(result.resourceIds.unchanged, true);
  assert.deepEqual(result.resourceIds.before, result.resourceIds.after);
  assert.equal(fixture.state.modelRequests, 1);
  assert.doesNotMatch(JSON.stringify(result), /console-password|workspace-password|sk-\*\*\*\*|OPL_QA_|must-not-emit/);
  assert.equal(fixture.calls.some((call) => call.path === "/api/billing/receipts/receipt-current-1"), true);
  assert.equal(fixture.calls.some((call) => call.path === "/api/billing/receipts"), false);
  assert.equal(fixture.calls.some((call) => call.path === "/api/gateway/summary" || /^\/api\/workspaces\/[^/]+\/receipt$/.test(call.path)), false);
  assert.equal(fixture.calls.some((call) => call.path === "/api/gateway/usage" || call.path === "/api/gateway/usage/stats" || call.path === "/api/workspaces/runtime-status"), false);
  assert.equal(fixture.calls.some((call) => call.path === "/api/gateway/keys/9/usage"), true);
  assert.equal(fixture.calls.some((call) => call.path === "/api/gateway/keys/9/usage-summary"), true);
  assert.equal(fixture.calls.some((call) => call.path === "/api/workspaces/workspace-slot-1/runtime-status"), true);
  assert.equal(fixture.calls.some((call) => /create|destroy|detach|renew/i.test(call.path)), false);
  assert.equal(fixture.calls.every((call) => call.signal instanceof AbortSignal), true);
});

test("rollout QA fails before the model request when WebSocket frames are missing", async () => {
  const fixture = liveFixture({ frames: false });
  await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /workspace_websocket_frames_required/);
  assert.equal(fixture.state.modelRequests, 0);
});

test("rollout QA requires the model response to contain only the unique token", async () => {
  const fixture = liveFixture({ responseSuffix: " extra text" });
  await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /workspace_model_response_required/);
  assert.equal(fixture.state.modelRequests, 1);
});

test("rollout QA reports Provider Acceptance without starting a browser when the fixed slot is absent", async () => {
  const fixture = liveFixture({ slotMissing: true });
  await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /provider_acceptance_required/);
  assert.equal(fixture.state.modelRequests, 0);
});

test("rollout QA never retries the model request when usage does not increase", async () => {
  const fixture = liveFixture({ usageStuck: true });
  await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /exact_gateway_request_not_found/);
  assert.equal(fixture.state.modelRequests, 1);
});

test("rollout QA fails closed unless exactly one new request id and matching stats appear", async () => {
  for (const [fixture, error] of [
    [liveFixture({ ambiguousUsage: true }), /gateway_request_cardinality_mismatch/],
    [liveFixture({ invalidUsageRecord: true }), /gateway_request_usage_invalid/],
    [liveFixture({ usageKeyId: "10" }), /gateway_request_usage_invalid/],
    [liveFixture({ balanceMismatch: true }), /gateway_balance_delta_mismatch/],
    [liveFixture({ statsStuck: true }), /gateway_usage_stats_mismatch/]
  ]) {
    await assert.rejects(() => verifyProductionLiveQa(options(fixture)), error);
    assert.equal(fixture.state.modelRequests, 1);
  }

  const duplicateKey = liveFixture({ duplicateKey: true });
  await assert.rejects(() => verifyProductionLiveQa(options(duplicateKey)), /dedicated_workspace_key_required/);
  assert.equal(duplicateKey.state.modelRequests, 0);
});

test("rollout QA accepts the Control Plane empty usage page before the one model request", async () => {
  const fixture = liveFixture({ emptyUsageBaseline: true });
  const result = await verifyProductionLiveQa(options(fixture));
  assert.equal(fixture.state.modelRequests, 1);
  assert.equal(result.usage.request.requestId, "req-rollout-qa-1");
  assert.equal(result.usage.stats.before.totalRequests, 0);
  assert.equal(result.usage.stats.delta.totalRequests, 1);
});

test("rollout QA requires the exact model request contract, positive cost, and a bounded usage snapshot", async () => {
  for (const fixture of [
    liveFixture({ usageOverrides: { model: "gpt-4.1" } }),
    liveFixture({ usageOverrides: { requestType: "stream" } }),
    liveFixture({ usageOverrides: { inboundEndpoint: "/v1/chat/completions" } }),
    liveFixture({ usageOverrides: { actualCostUsdMicros: 0 } })
  ]) {
    await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /gateway_request_usage_invalid/);
    assert.equal(fixture.state.modelRequests, 1);
  }

  const oversized = liveFixture({ usageSnapshotTooLarge: true });
  await assert.rejects(() => verifyProductionLiveQa(options(oversized)), /gateway_usage_snapshot_limit_exceeded/);
  assert.equal(oversized.state.modelRequests, 0);

  const missingModel = liveFixture();
  const missingModelOptions = options(missingModel);
  delete missingModelOptions.expectedModel;
  await assert.rejects(() => verifyProductionLiveQa(missingModelOptions), /production_live_qa_expected_model_required/);
  assert.equal(missingModel.calls.length, 0);
});

test("rollout QA obtains credentials only from private no-store reveal", async () => {
  await assert.rejects(() => verifyProductionLiveQa(options(liveFixture({ statusLeaksPassword: true }))), /runtime_status_secret_forbidden/);
  await assert.rejects(() => verifyProductionLiveQa(options(liveFixture({ revealCacheControl: "no-store" }))), /runtime_credentials_cache_control_invalid/);
});

test("rollout QA rejects any provider addition or same-id launch and renewal result change", async () => {
  for (const fixture of [
    liveFixture({ changedProviderOperations: true }),
    liveFixture({ changedLaunchOperation: true }),
    liveFixture({ changedRuntimeOperation: true })
  ]) {
    await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /production_live_qa_runtime_operations_changed/);
    assert.equal(fixture.state.modelRequests, 1);
  }
});

test("rollout QA rejects every provider write operation while ignoring read-only sync", async () => {
  for (const action of [
    "tag_compute_machine",
    "create_storage_attachment", "detach_storage_attachment",
    "create_workspace_runtime", "destroy_workspace_runtime",
    "upsert_gateway_secret", "workspace.gateway_secret.rotate",
    "create_storage_snapshot", "restore_storage_snapshot", "destroy_storage_snapshot"
  ]) {
    const fixture = liveFixture({ changedMutationAction: action });
    await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /production_live_qa_runtime_operations_changed/, action);
    assert.equal(fixture.state.modelRequests, 1);
  }
});

test("rollout QA rejects changes to any account RuntimeOperation without a static action allowlist", async () => {
  const fixture = liveFixture({ changedUntrackedOperation: true });
  await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /production_live_qa_runtime_operations_changed/);
  assert.equal(fixture.state.modelRequests, 1);
});

test("rollout QA requires the same safe Workspace receipt before and after the request", async () => {
  const fixture = liveFixture({ changedReceipt: true });
  await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /production_live_qa_ledger_receipt_changed/);
  assert.equal(fixture.state.modelRequests, 1);
});

test("rollout QA fails closed when any retained provider resource id changes", async () => {
  const fixture = liveFixture({ changedResourceIds: true });
  await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /production_live_qa_resource_ids_changed/);
  assert.equal(fixture.state.modelRequests, 1);
});

test("rollout QA CLI requires explicit one-request confirmation before network access", async () => {
  let stderr = "";
  let calls = 0;
  const code = await runProductionLiveQaCli({
    argv: ["--allow-gateway-write", "--allow-model-write", "--approval-id", "approval-pilot-v2"],
    env: {
      OPL_VERIFY_ACCOUNT_ID: BASIC_ACCOUNT_ID,
      OPL_VERIFY_MUTATION_APPROVAL_JSON: mutationApprovalJson
    },
    stdout: { write: () => {} },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: async () => { calls += 1; return json({}); }
  });
  assert.equal(code, 1);
  assert.match(stderr, /production_live_qa_confirmation_required/);
  assert.equal(calls, 0);
});

test("rollout QA CLI rejects an invalid slot descriptor before network access", async () => {
  let stderr = "";
  let calls = 0;
  const code = await runProductionLiveQaCli({
    argv: ["--allow-gateway-write", "--allow-model-write", "--approval-id", "approval-pilot-v2"],
    env: {
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_VERIFY_AUTH_USERS_JSON: ownerSeed,
      OPL_VERIFY_ACCOUNT_ID: BASIC_ACCOUNT_ID,
      OPL_VERIFY_LIVE_QA_CONFIRMATION: LIVE_QA_CONFIRMATION,
      OPL_VERIFY_EXPECTED_MODEL: "gpt-5.5",
      OPL_VERIFY_MUTATION_APPROVAL_JSON: mutationApprovalJson,
      OPL_VERIFY_SLOT_DESCRIPTOR_JSON: "{"
    },
    stdout: { write: () => {} },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: async () => { calls += 1; return json({}); }
  });
  assert.equal(code, 1);
  assert.match(stderr, /verification_slot_descriptor_invalid/);
  assert.equal(calls, 0);
});

test("rollout QA read-only evidence level performs no model or Gateway write", async () => {
  let stdout = "";
  let stderr = "";
  let calls = 0;
  const code = await runProductionLiveQaCli({
    argv: ["--read-only"],
    env: {},
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: async () => { calls += 1; return json({}); }
  });
  assert.equal(code, 0, stderr);
  assert.equal(calls, 0);
  assert.deepEqual(JSON.parse(stdout), {
    ok: true,
    mode: "read-only",
    evidenceLevel: "read-only",
    writesPerformed: 0
  });

  stderr = "";
  const denied = await runProductionLiveQaCli({
    argv: ["--allow-gateway-write", "--allow-model-write", "--approval-id", "approval-pilot-v2"],
    env: {},
    stdout: { write: () => {} },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: async () => { calls += 1; return json({}); }
  });
  assert.equal(denied, 1);
  assert.match(stderr, /production_live_qa_approval_manifest_required/);
  assert.equal(calls, 0);
});
