import assert from "node:assert/strict";
import { afterEach, test } from "node:test";

import * as workspaceApi from "../../apps/console-ui/src/api/workspaces-api.ts";

const originalFetch = globalThis.fetch;

afterEach(() => { globalThis.fetch = originalFetch; });

function response(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), { status, headers: { "content-type": "application/json" } });
}

test("Workspace launch uses one durable request and caller idempotency key", async () => {
  const requests: Array<{ url: string; init?: RequestInit }> = [];
  globalThis.fetch = async (input, init) => {
    requests.push({ url: String(input), init });
    return response({
      operationId: "launch-alpha", status: "preparing", phase: "compute", accountId: "acct-alpha",
      name: "Alpha", packageId: "basic", sizeGb: 10, autoRenew: true,
      priceVersion: "pilot-usd-2026-07-v1", currency: "USD", totalChargeUsdMicros: 52_580_000
    }, 202);
  };

  const input = { name: "Alpha", packageId: "basic", sizeGb: 10, autoRenew: true } as const;
  await workspaceApi.launchWorkspace(input, "csrf-alpha", "launch-once");
  await workspaceApi.launchWorkspace(input, "csrf-alpha", "launch-once");

  assert.deepEqual(requests.map(({ url }) => url), ["/api/workspace-launches", "/api/workspace-launches"]);
  assert.deepEqual(requests.map(({ init }) => new Headers(init?.headers).get("Idempotency-Key")), ["launch-once", "launch-once"]);
  assert.deepEqual(requests.map(({ init }) => JSON.parse(String(init?.body))), [input, input]);
});

test("Workspace launch alone uses a sixty second client deadline", async () => {
  const deadlines: number[] = [];
  const originalTimeout = AbortSignal.timeout;
  AbortSignal.timeout = ((milliseconds: number) => {
    deadlines.push(milliseconds);
    return originalTimeout(milliseconds);
  }) as typeof AbortSignal.timeout;
  try {
    globalThis.fetch = async () => response({ operationId: "launch-alpha", status: "preparing", phase: "debit_pending" }, 202);
    await workspaceApi.launchWorkspace({ name: "Alpha", packageId: "basic", sizeGb: 10, autoRenew: false }, "csrf-alpha", "launch-once");
  } finally {
    AbortSignal.timeout = originalTimeout;
  }
  assert.deepEqual(deadlines, [60_000]);
});

test("Workspace launch transport failure remains unknown for safe replay", async () => {
  let request: RequestInit | undefined;
  globalThis.fetch = async (_input, init) => {
    request = init;
    throw new DOMException("timed out", "TimeoutError");
  };

  await assert.rejects(
    workspaceApi.launchWorkspace({ name: "Alpha", packageId: "basic", sizeGb: 10, autoRenew: false }, "csrf-alpha", "launch-once"),
    (error: any) => error?.payload?.status === "unknown" && error?.payload?.retryable === true
  );
  assert.equal(new Headers(request?.headers).get("Idempotency-Key"), "launch-once");
});

test("Workspace launch polling addresses the exact operation", async () => {
  let url = "";
  globalThis.fetch = async (input) => {
    url = String(input);
    return response({ operationId: "launch-alpha", status: "succeeded", phase: "completed" });
  };
  const result = await workspaceApi.getWorkspaceLaunch("launch-alpha");
  assert.equal(url, "/api/workspace-launches/launch-alpha");
  assert.equal(result.status, "succeeded");
  assert.equal(workspaceApi.isTerminalWorkspaceLaunch(result.status), true);
});

test("Workspace launch recovery lists the current account operations", async () => {
  let url = "";
  globalThis.fetch = async (input) => {
    url = String(input);
    return response([{ operationId: "launch-alpha", status: "preparing", phase: "compute" }]);
  };

  const launches = await workspaceApi.getWorkspaceLaunches();
  assert.equal(url, "/api/workspace-launches");
  assert.equal(launches[0]?.operationId, "launch-alpha");
});

test("Workspace credential and renewal commands use explicit routes and mutation keys", async () => {
  const requests: Array<{ url: string; init?: RequestInit }> = [];
  globalThis.fetch = async (input, init) => {
    requests.push({ url: String(input), init });
    if (String(input).endsWith("/auto-renew")) {
      return response({ autoRenew: true, effectiveAfter: "2026-07-31T00:00:00Z", nextRenewalAt: "2026-07-31T00:00:00Z", paidThrough: "2026-08-01T00:00:00Z", renewalStatus: "scheduled" });
    }
    return response({ workspaceId: "workspace-alpha", access: { account: "owner", username: "owner", password: "secret", credentialStatus: "active", credentialVersion: "v2" } });
  };

  await workspaceApi.revealWorkspaceCredentials("workspace-alpha", "csrf-alpha");
  await workspaceApi.rotateWorkspaceCredentials("workspace-alpha", "csrf-alpha", "rotate-once");
  const renewal = await workspaceApi.updateWorkspaceRenewal("workspace-alpha", { autoRenew: true }, "csrf-alpha", "renew-once");

  assert.deepEqual(requests.map(({ url }) => url), [
    "/api/workspaces/workspace-alpha/runtime-credentials/reveal",
    "/api/workspaces/workspace-alpha/runtime-credentials/rotate",
    "/api/workspaces/workspace-alpha/auto-renew"
  ]);
  const keys = requests.map(({ init }) => new Headers(init?.headers).get("Idempotency-Key"));
  assert.match(keys[0] ?? "", /^runtime-credential-reveal:/);
  assert.deepEqual(keys.slice(1), ["rotate-once", "renew-once"]);
  assert.deepEqual(JSON.parse(String(requests[2]?.init?.body)), { autoRenew: true });
  assert.equal(renewal.autoRenew, true);
});
