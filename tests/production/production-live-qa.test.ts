import assert from "node:assert/strict";
import test from "node:test";

import {
  LIVE_QA_CONFIRMATION,
  runProductionLiveQaCli,
  verifyProductionLiveQa
} from "../../tools/production-live-qa.ts";

const fixedSlotDescriptor = {
  id: "verification-slot-01",
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
const ownerSeed = JSON.stringify([{
  id: "usr-verifier",
  email: "owner@example.com",
  password: "console-password",
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

function liveFixture({ changedResourceIds = false, frames = true, responseSuffix = "", slotMissing = false, usageStuck = false } = {}) {
  const state = { modelRequests: 0, stateReads: 0 };
  const calls = [];
  const deadline = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString();
  const resourceState = () => {
    state.stateReads += 1;
    const suffix = changedResourceIds && state.stateReads > 1 ? "changed" : "1";
    const result = {
      balance: { source: "sub2api", currency: "USD", userId: 41, usdMicros: 500_000_000 },
      computeAllocations: [{
        id: "compute-slot-1",
        accountId: "acct-alpha",
        workspaceId: "workspace-slot-1",
        providerResourceId: "ins-slot-1",
        nodePoolId: `np-slot-${suffix}`,
        status: "running",
        costTags: { opl_account_id: "acct-alpha", opl_workspace_id: "workspace-slot-1", opl_resource_id: "compute-slot-1" },
        providerData: { instanceType: "SA5.MEDIUM4", zone: "ap-guangzhou-3", chargeType: "PREPAID", periodMonths: "1", renewFlag: "NOTIFY_AND_MANUAL_RENEW", deadline }
      }],
      storageVolumes: [{
        id: "storage-slot-1",
        accountId: "acct-alpha",
        workspaceId: "workspace-slot-1",
        providerResourceId: "disk-slot-1",
        sizeGb: 10,
        status: "available",
        costTags: { opl_account_id: "acct-alpha", opl_workspace_id: "workspace-slot-1", opl_resource_id: "storage-slot-1" },
        providerData: { diskChargeType: "PREPAID", periodMonths: "1", renewFlag: "NOTIFY_AND_MANUAL_RENEW", deadline, zone: "ap-guangzhou-3", pvName: "pv-slot-1" }
      }],
      workspaces: [{
        id: "workspace-slot-1",
        accountId: "acct-alpha",
        ownerAccountId: "acct-alpha",
        verificationSlotId: "verification-slot-01",
        customerProduct: false,
        currentComputeAllocationId: "compute-slot-1",
        storageId: "storage-slot-1",
        state: "running",
        openable: true,
        url: "https://workspace.medopl.cn/w/workspace-slot-1/"
      }]
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
    calls.push({ method, path: url.pathname, signal: init.signal });
    if (url.hostname === "workspace.medopl.cn") return new Response("<main>workspace</main>", { status: 200 });
    if (url.pathname === "/api/production/readiness") return json({ ready: true });
    if (url.pathname === "/api/auth/login") {
      return json({ user: { accountId: "acct-alpha", role: "owner" } }, 200, {
        "set-cookie": "opl_session=session-alpha; Path=/; HttpOnly",
        "x-opl-csrf-token": "csrf-alpha"
      });
    }
    assert.match(headers.get("cookie") || "", /opl_session=session-alpha/);
    if (url.pathname === "/api/pricing/catalog") {
      return json({
        storagePer10GbMonthly: { cnyCents: 1800, usdMicros: 2_571_429 },
        packages: [
          { id: "basic", price: { monthlyPriceCnyCents: 35_000, chargeUsdMicros: 50_000_000 } },
          { id: "pro", price: { monthlyPriceCnyCents: 150_000, chargeUsdMicros: 214_285_715 } }
        ]
      });
    }
    if (url.pathname === "/api/state") return json(resourceState());
    if (url.pathname === "/api/gateway/summary") {
      const increment = usageStuck ? 0 : state.modelRequests;
      return json({
        apiKey: { id: "key-9", name: "opl-workspace", status: "active", maskedValue: "sk-****", revealed: false },
        usage: { quotaUsedUsdMicros: 1000 + increment, usage1dUsdMicros: 500 + increment }
      });
    }
    if (url.pathname === "/api/workspaces/runtime-status") {
      assert.equal(method, "POST");
      assert.equal(headers.get("x-opl-csrf"), "csrf-alpha");
      assert.deepEqual(JSON.parse(init.body), { workspaceId: "workspace-slot-1" });
      return json({
        ready: true,
        url: "https://workspace.medopl.cn/w/workspace-slot-1/",
        access: { username: "opl", password: "workspace-password", credentialStatus: "configured" }
      });
    }
    return json({ error: "not_found" }, 404);
  };

  return { browserFactory: browserFactory(state, { frames, responseSuffix }), calls, fetchImpl, state };
}

function options(fixture) {
  return {
    origin: "https://cloud.medopl.cn",
    authUsersJson: ownerSeed,
    accountId: "acct-alpha",
    runId: "rollout-qa-1",
    confirmation: LIVE_QA_CONFIRMATION,
    slotDescriptor: fixedSlotDescriptor,
    workspaceUrlAttempts: 1,
    retryDelayMs: 0,
    usageAttempts: 2,
    usageRetryDelayMs: 0,
    browserTimeoutMs: 20,
    modelTimeoutMs: 20,
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
  assert.equal(result.resourceIds.unchanged, true);
  assert.deepEqual(result.resourceIds.before, result.resourceIds.after);
  assert.equal(result.usage.after.quotaUsedUsdMicros > result.usage.before.quotaUsedUsdMicros, true);
  assert.equal(fixture.state.modelRequests, 1);
  assert.doesNotMatch(JSON.stringify(result), /console-password|workspace-password|sk-\*\*\*\*|OPL_QA_/);
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
  await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /dedicated_key_usage_not_increased/);
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
    env: {},
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
    env: {
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_VERIFY_AUTH_USERS_JSON: ownerSeed,
      OPL_VERIFY_ACCOUNT_ID: "acct-alpha",
      OPL_VERIFY_LIVE_QA_CONFIRMATION: LIVE_QA_CONFIRMATION,
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
