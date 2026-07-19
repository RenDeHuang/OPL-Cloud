import assert from "node:assert/strict";
import { afterEach, test } from "node:test";
import { currentSession } from "../../apps/console-ui/src/api/auth-api.ts";
import { customerSafeMessage } from "../../apps/console-ui/src/api/console-api.ts";

const originalFetch = globalThis.fetch;
const originalTimeout = AbortSignal.timeout;

afterEach(() => {
  globalThis.fetch = originalFetch;
  AbortSignal.timeout = originalTimeout;
});

function jsonResponse(payload, status) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "content-type": "application/json" }
  });
}

test("session bootstrap treats only HTTP 401 as signed out and sends a timeout signal", async () => {
  let signal;
  let timeoutMs;
  AbortSignal.timeout = (milliseconds) => {
    timeoutMs = milliseconds;
    return originalTimeout(milliseconds);
  };
  globalThis.fetch = async (_url, init) => {
    signal = init?.signal;
    return jsonResponse({ error: "not_authenticated" }, 401);
  };

  assert.equal(await currentSession(), null);
  assert.ok(signal instanceof AbortSignal);
  assert.equal(timeoutMs, 3_000);

  globalThis.fetch = async () => jsonResponse({ error: "auth_backend_unavailable" }, 503);
  await assert.rejects(currentSession(), /auth_backend_unavailable/);

  globalThis.fetch = async () => jsonResponse({}, 200);
  await assert.rejects(currentSession(), /session_check_failed/);

  globalThis.fetch = async () => new Response("not-json", { status: 200 });
  await assert.rejects(currentSession(), /session_check_failed/);
});

test("only Workspace readiness errors use the Docker distribution message", () => {
  assert.equal(customerSafeMessage({ error: "workspace_runtime_not_ready" }), "正在分发 Docker，预计 3-5 分钟，请稍后再打开 URL。");
  assert.equal(customerSafeMessage({ error: "gateway_upstream_unavailable" }), "gateway_upstream_unavailable");
});

test("session recovery restores the mutation CSRF token from the response header", async () => {
  globalThis.fetch = async () => new Response(JSON.stringify({
    source: "sub2api",
    status: "available",
    available: true,
    fetchedAt: "2026-07-19T00:00:00Z",
    data: {
      consoleUserId: "usr-alpha",
      accountId: "acct-alpha",
      role: "owner",
      sub2apiUserId: "41",
      email: "owner@example.com",
      status: "active"
    }
  }), {
    status: 200,
    headers: { "content-type": "application/json", "x-opl-csrf-token": "csrf-recovered" }
  });

  assert.equal((await currentSession())?.csrfToken, "csrf-recovered");
});
