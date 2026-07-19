import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { afterEach, test } from "node:test";

import * as workspaceApi from "../../apps/console-ui/src/api/workspaces-api.ts";

const originalFetch = globalThis.fetch;
const appSource = () => readFile(new URL("../../apps/console-ui/src/App.vue", import.meta.url), "utf8");

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
      name: "Alpha", packageId: "basic", sizeGb: 10, autoRenew: false,
      priceVersion: "pilot-usd-2026-07-v1", currency: "USD", totalChargeUsdMicros: 52_580_000
    }, 202);
  };

  const input = { name: "Alpha", packageId: "basic", sizeGb: 10, autoRenew: false } as const;
  await workspaceApi.launchWorkspace(input, "csrf-alpha", "launch-once");
  await workspaceApi.launchWorkspace(input, "csrf-alpha", "launch-once");

  assert.deepEqual(requests.map(({ url }) => url), ["/api/workspace-launches", "/api/workspace-launches"]);
  assert.deepEqual(requests.map(({ init }) => new Headers(init?.headers).get("Idempotency-Key")), ["launch-once", "launch-once"]);
  assert.deepEqual(requests.map(({ init }) => JSON.parse(String(init?.body))), [input, input]);
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

test("Workspace launch keeps one submission intent and exposes bounded polling recovery", async () => {
  const app = await appSource();
  assert.match(app, /let workspaceLaunchIntent:/);
  assert.match(app, /launchWorkspace\(input,[^,]+, workspaceLaunchIntent\.idempotencyKey\)/);
  assert.match(app, /getWorkspaceLaunches\(\)/);
  assert.match(app, /const workspaceLaunchPollIntervalMs = 10_000/);
  assert.match(app, /const workspaceLaunchPollAttempts = 30/);
  assert.match(app, /retryWorkspaceLaunchPoll/);
});

test("Workspace credential rotation reuses its intent key until a confirmed success", async () => {
  const app = await appSource();
  assert.match(app, /let runtimeRotationIntent:/);
  assert.match(app, /rotateWorkspaceCredentials\([^,]+,[^,]+, runtimeRotationIntent\.idempotencyKey\)/);
  assert.match(app, /runtimeRotationIntent = null;\s*if \(!secretResponseStillCurrent/);
});

test("Workspace credential and renewal commands use explicit routes and mutation keys", async () => {
  const requests: Array<{ url: string; init?: RequestInit }> = [];
  globalThis.fetch = async (input, init) => {
    requests.push({ url: String(input), init });
    if (String(input).endsWith("/auto-renew")) {
      return response({ autoRenew: false, effectiveAfter: "2026-08-01T00:00:00Z", nextRenewalAt: "2026-07-31T00:00:00Z", paidThrough: "2026-08-01T00:00:00Z", renewalStatus: "cancelled" });
    }
    return response({ workspaceId: "workspace-alpha", access: { account: "owner", username: "owner", password: "secret", credentialStatus: "active", credentialVersion: "v2" } });
  };

  await workspaceApi.revealWorkspaceCredentials("workspace-alpha", "csrf-alpha");
  await workspaceApi.rotateWorkspaceCredentials("workspace-alpha", "csrf-alpha", "rotate-once");
  await workspaceApi.updateWorkspaceRenewal("workspace-alpha", { autoRenew: false }, "csrf-alpha", "renew-once");

  assert.deepEqual(requests.map(({ url }) => url), [
    "/api/workspaces/workspace-alpha/runtime-credentials/reveal",
    "/api/workspaces/workspace-alpha/runtime-credentials/rotate",
    "/api/workspaces/workspace-alpha/auto-renew"
  ]);
  assert.deepEqual(requests.map(({ init }) => new Headers(init?.headers).get("Idempotency-Key")), [null, "rotate-once", "renew-once"]);
});
