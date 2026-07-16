import assert from "node:assert/strict";
import { afterEach, test } from "node:test";

import * as authApi from "../../apps/console-ui/src/api/auth-api.ts";
import { getGatewaySummary } from "../../apps/console-ui/src/api/console-read-api.ts";

const originalFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = originalFetch;
});

test("logout clears the local session before the remote request settles", async () => {
  assert.equal(typeof authApi.logoutLocalFirst, "function");

  let settle: (response: Response) => void = () => {};
  const remote = new Promise<Response>((resolve) => { settle = resolve; });
  globalThis.fetch = async () => remote;
  const events: string[] = [];

  const pending = authApi.logoutLocalFirst(
    "csrf-alpha",
    () => events.push("local-cleared"),
    () => events.push("navigated")
  );
  assert.deepEqual(events, ["local-cleared", "navigated"]);

  settle(new Response(JSON.stringify({ ok: true }), {
    status: 200,
    headers: { "content-type": "application/json" }
  }));
  await pending;
});

test("Gateway cleanup aborts requests, removes raw keys, and ignores late responses", async () => {
  const gatewayLifecycle = await import("../../apps/console-ui/src/pages/gateway/gateway-request.ts").catch(() => null);
  assert.ok(gatewayLifecycle, "Gateway request lifecycle helper is required");

  let settle: (response: Response) => void = () => {};
  let fetchSignal: AbortSignal | undefined;
  globalThis.fetch = async (_input, init = {}) => {
    fetchSignal = init.signal || undefined;
    return new Promise<Response>((resolve) => { settle = resolve; });
  };

  const lifecycle = gatewayLifecycle.createGatewayRequestLifecycle();
  const controller = lifecycle.start();
  const updates: any[] = [];
  const pending = getGatewaySummary(true, controller.signal).then((payload) => {
    if (lifecycle.isCurrent(controller)) updates.push(payload);
  });
  await Promise.resolve();

  lifecycle.dispose();
  assert.equal(controller.signal.aborted, true);
  assert.equal(fetchSignal?.aborted, true);

  const revealed = { apiKey: { id: "key-alpha", revealed: true, value: "sk-raw", maskedValue: "sk-****" } };
  settle(new Response(JSON.stringify(revealed), {
    status: 200,
    headers: { "content-type": "application/json" }
  }));
  await pending;
  assert.deepEqual(updates, []);
  assert.deepEqual(gatewayLifecycle.maskGatewaySummary(revealed), {
    apiKey: { id: "key-alpha", revealed: false, maskedValue: "sk-****" }
  });
});
