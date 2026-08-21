import assert from "node:assert/strict";
import { afterEach, test } from "node:test";

import * as authApi from "../../apps/console-ui/src/api/auth-api.ts";
import * as readApi from "../../apps/console-ui/src/api/console-read-api.ts";
import { isSensitiveConsoleRoute } from "../../apps/console-ui/src/app/console-router.ts";
import { maskGatewayKey } from "../../apps/console-ui/src/console-model.ts";

const originalFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = originalFetch;
});

function activeSessionResponse(csrfToken = "csrf-readback") {
  return new Response(JSON.stringify({
    source: "sub2api",
    status: "available",
    available: true,
    fetchedAt: "2026-07-31T00:00:00Z",
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
    headers: { "content-type": "application/json", "x-opl-csrf-token": csrfToken }
  });
}

test("logout confirms immediately only after the server accepts revocation", async () => {
  const requests: Array<{ url: string; init?: RequestInit }> = [];
  globalThis.fetch = async (input, init) => {
    requests.push({ url: String(input), init });
    return new Response(JSON.stringify({ ok: true }), {
      status: 200,
      headers: { "content-type": "application/json" }
    });
  };

  assert.deepEqual(await authApi.logoutAndConfirm("csrf-alpha"), { state: "confirmed", via: "logout" });
  assert.deepEqual(requests.map(({ url }) => url), ["/api/auth/logout"]);
  assert.equal(new Headers(requests[0]?.init?.headers).get("x-opl-csrf"), "csrf-alpha");
});

test("logout closes an invalid Session only after authoritative readback", async () => {
  const requests: string[] = [];
  globalThis.fetch = async (input) => {
    requests.push(String(input));
    return new Response(JSON.stringify({ error: "not_authenticated" }), {
      status: 401,
      headers: { "content-type": "application/json" }
    });
  };

  assert.deepEqual(await authApi.logoutAndConfirm("csrf-expired"), {
    state: "confirmed",
    via: "session_readback"
  });
  assert.deepEqual(requests, ["/api/auth/logout", "/api/auth/me"]);
});

test("ambiguous revocation stays unconfirmed", async (t) => {
  const cases: Array<[string, () => Promise<Response>]> = [
    ["network failure", async () => { throw new TypeError("fetch failed"); }],
    ["server failure", async () => new Response(JSON.stringify({ error: "state_persist_failed" }), {
      status: 503,
      headers: { "content-type": "application/json" }
    })],
    ["csrf failure", async () => new Response(JSON.stringify({ error: "csrf_invalid" }), {
      status: 403,
      headers: { "content-type": "application/json" }
    })]
  ];

  for (const [name, firstResponse] of cases) {
    await t.test(name, async () => {
      let requestCount = 0;
      globalThis.fetch = async () => {
        requestCount += 1;
        if (requestCount === 1) return firstResponse();
        return activeSessionResponse();
      };

      const result = await authApi.logoutAndConfirm("csrf-alpha");
      assert.equal(result.state, "unconfirmed");
      assert.equal(result.reason, "session_still_active");
      assert.equal(result.session.user.accountId, "acct-alpha");
      assert.equal(requestCount, 2);
    });
  }
});

test("a lost logout response closes when readback proves the Session is gone", async () => {
  let requestCount = 0;
  globalThis.fetch = async () => {
    requestCount += 1;
    if (requestCount === 1) throw new TypeError("response lost");
    return new Response(JSON.stringify({ error: "not_authenticated" }), {
      status: 401,
      headers: { "content-type": "application/json" }
    });
  };

  assert.deepEqual(await authApi.logoutAndConfirm("csrf-alpha"), {
    state: "confirmed",
    via: "session_readback"
  });
});

test("logout remains unconfirmed when readback is unavailable", async () => {
  globalThis.fetch = async () => { throw new TypeError("network unavailable"); };

  assert.deepEqual(await authApi.logoutAndConfirm("csrf-alpha"), {
    state: "unconfirmed",
    reason: "readback_unavailable",
    session: null
  });
});

test("API Key cleanup removes the raw value", () => {
  const revealed = { id: "41", name: "personal", status: "active" as const, value: "sk-raw" };
  assert.deepEqual(maskGatewayKey(revealed), { ...revealed, value: "" });
});

test("sensitive routes are classified by their public route behavior", () => {
  assert.equal(isSensitiveConsoleRoute("/console/api"), true);
  assert.equal(isSensitiveConsoleRoute("/console/api/keys"), true);
  assert.equal(isSensitiveConsoleRoute("/console/workspaces/ws-1"), true);
  assert.equal(isSensitiveConsoleRoute("/console/billing"), false);
});

test("balance history requests one explicit page", async () => {
  let requestedUrl = "";
  globalThis.fetch = async (input) => {
    requestedUrl = String(input);
    return new Response(JSON.stringify({
      source: "sub2api",
      status: "available",
      available: true,
      fetchedAt: "2026-07-24T00:00:00Z",
      data: { items: [], total: 41, page: 3, pageSize: 20, pages: 3 }
    }), { status: 200, headers: { "content-type": "application/json" } });
  };

  const result = await readApi.getGatewayBalanceHistory(3, 20);
  assert.equal(requestedUrl, "/api/gateway/balance-history?page=3&pageSize=20");
  assert.equal(result.data.page, 3);
  assert.equal(result.data.pageSize, 20);
  assert.equal(result.data.pages, 3);
});

test("general API Key writes carry CSRF and caller idempotency keys", async () => {
  const requests: Array<{ url: string; init?: RequestInit }> = [];
  globalThis.fetch = async (input, init) => {
    requests.push({ url: String(input), init });
    const status = init?.method === "DELETE" ? "deleted" : "active";
    return new Response(JSON.stringify({
      source: "sub2api",
      status: "available",
      available: true,
      fetchedAt: "2026-07-20T00:00:00Z",
      data: init?.method === "DELETE"
        ? { operationId: "op-delete", status }
        : {
            id: "41", name: "personal", kind: "general", status, groupId: "101",
            ipWhitelist: [], ipBlacklist: [], quotaUsdMicros: 1_000_000,
            quotaUsedUsdMicros: 0, rateLimit5hUsdMicros: 0,
            rateLimit1dUsdMicros: 0, rateLimit7dUsdMicros: 0,
            usage5hUsdMicros: 0, usage1dUsdMicros: 0, usage7dUsdMicros: 0,
            currentConcurrency: 0, lastUsedAt: null, lastUsedIp: null,
            expiresAt: "2026-08-19T00:00:00Z", createdAt: "2026-07-20T00:00:00Z",
            updatedAt: "2026-07-20T00:00:00Z", manageable: true, deletable: true
          }
    }), { status: 200, headers: { "content-type": "application/json" } });
  };

  await readApi.createGatewayKey(
    { name: "personal", groupId: "101", quotaUsdMicros: 1_000_000, expiresInDays: 30 },
    "csrf-key",
    "key-create:opaque"
  );
  await readApi.updateGatewayKey("41", { enabled: false }, "csrf-key", "key-toggle:opaque");
  await readApi.deleteGatewayKey("41", "csrf-key", "key-delete:opaque");

  assert.deepEqual(requests.map(({ url }) => url), [
    "/api/gateway/keys",
    "/api/gateway/keys/41",
    "/api/gateway/keys/41"
  ]);
  assert.deepEqual(requests.map(({ init }) => init?.method), ["POST", "PATCH", "DELETE"]);
  assert.deepEqual(requests.map(({ init }) => new Headers(init?.headers).get("x-opl-csrf")), [
    "csrf-key", "csrf-key", "csrf-key"
  ]);
  assert.deepEqual(requests.map(({ init }) => new Headers(init?.headers).get("Idempotency-Key")), [
    "key-create:opaque", "key-toggle:opaque", "key-delete:opaque"
  ]);
});
