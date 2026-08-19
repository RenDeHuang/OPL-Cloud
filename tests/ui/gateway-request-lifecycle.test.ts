import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { afterEach, test } from "node:test";

import * as authApi from "../../apps/console-ui/src/api/auth-api.ts";
import * as readApi from "../../apps/console-ui/src/api/console-read-api.ts";
import { maskGatewayKey } from "../../apps/console-ui/src/console-model.ts";
import { isSensitiveConsoleRoute } from "../../apps/console-ui/src/app/console-router.ts";

const root = new URL("../../", import.meta.url);
const source = (path: string) => readFile(new URL(path, root), "utf8");
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
    return new Response(JSON.stringify({ ok: true }), { status: 200, headers: { "content-type": "application/json" } });
  };

  assert.deepEqual(await authApi.logoutAndConfirm("csrf-alpha"), { state: "confirmed", via: "logout" });
  assert.deepEqual(requests.map(({ url }) => url), ["/api/auth/logout"]);
  assert.equal(new Headers(requests[0]?.init?.headers).get("x-opl-csrf"), "csrf-alpha");
});

test("logout closes an already invalid Session only after auth me returns 401", async () => {
  const requests: string[] = [];
  globalThis.fetch = async (input) => {
    requests.push(String(input));
    return new Response(JSON.stringify({ error: "not_authenticated" }), {
      status: 401,
      headers: { "content-type": "application/json" }
    });
  };

  assert.deepEqual(await authApi.logoutAndConfirm("csrf-expired"), { state: "confirmed", via: "session_readback" });
  assert.deepEqual(requests, ["/api/auth/logout", "/api/auth/me"]);
});

test("logout does not accept an ambiguous auth me 401 as Session revocation proof", async () => {
  let requestCount = 0;
  globalThis.fetch = async () => {
    requestCount += 1;
    return new Response(JSON.stringify({ error: requestCount === 1 ? "state_persist_failed" : "authentication_unavailable" }), {
      status: requestCount === 1 ? 500 : 401,
      headers: { "content-type": "application/json" }
    });
  };

  assert.deepEqual(await authApi.logoutAndConfirm("csrf-alpha"), {
    state: "unconfirmed",
    reason: "readback_unavailable",
    session: null
  });
  assert.equal(requestCount, 2);
});

test("network failure and timeout remain unconfirmed while auth me still sees the Session", async (t) => {
  for (const [name, failure] of [
    ["network", new TypeError("fetch failed")],
    ["timeout", new DOMException("timed out", "TimeoutError")]
  ] as const) {
    await t.test(name, async () => {
      let requestCount = 0;
      globalThis.fetch = async () => {
        requestCount += 1;
        if (requestCount === 1) throw failure;
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

test("5xx and CSRF failures remain unconfirmed while the server Session survives", async (t) => {
  for (const [name, status, error] of [
    ["server", 503, "state_persist_failed"],
    ["csrf", 403, "csrf_invalid"]
  ] as const) {
    await t.test(name, async () => {
      let requestCount = 0;
      globalThis.fetch = async () => {
        requestCount += 1;
        if (requestCount === 1) {
          return new Response(JSON.stringify({ error }), {
            status,
            headers: { "content-type": "application/json" }
          });
        }
        return activeSessionResponse();
      };

      const result = await authApi.logoutAndConfirm("csrf-alpha");
      assert.equal(result.state, "unconfirmed");
      assert.equal(result.reason, "session_still_active");
      assert.equal(requestCount, 2);
    });
  }
});

test("a lost logout response closes successfully when auth me proves the Session is gone", async () => {
  let requestCount = 0;
  globalThis.fetch = async () => {
    requestCount += 1;
    if (requestCount === 1) throw new TypeError("response lost");
    return new Response(JSON.stringify({ error: "not_authenticated" }), {
      status: 401,
      headers: { "content-type": "application/json" }
    });
  };

  assert.deepEqual(await authApi.logoutAndConfirm("csrf-alpha"), { state: "confirmed", via: "session_readback" });
  assert.equal(requestCount, 2);
});

test("logout never reports success when the authoritative readback still has a Session", async () => {
  let requestCount = 0;
  globalThis.fetch = async () => {
    requestCount += 1;
    if (requestCount === 1) {
      return new Response(JSON.stringify({ error: "state_persist_failed" }), {
        status: 500,
        headers: { "content-type": "application/json" }
      });
    }
    return activeSessionResponse("csrf-refreshed");
  };

  const result = await authApi.logoutAndConfirm("csrf-alpha");
  assert.equal(result.state, "unconfirmed");
  assert.equal(result.reason, "session_still_active");
  assert.equal(result.session.csrfToken, "csrf-refreshed");
});

test("logout remains unconfirmed when both revocation and authoritative readback are unavailable", async () => {
  let requestCount = 0;
  globalThis.fetch = async () => {
    requestCount += 1;
    throw new TypeError(requestCount === 1 ? "logout network failed" : "auth readback network failed");
  };

  assert.deepEqual(await authApi.logoutAndConfirm("csrf-alpha"), {
    state: "unconfirmed",
    reason: "readback_unavailable",
    session: null
  });
  assert.equal(requestCount, 2);
});

test("API Key cleanup removes the raw value", () => {
  const revealed = { id: "41", name: "opl-workspace", status: "active" as const, value: "sk-raw" };
  assert.deepEqual(maskGatewayKey(revealed), { ...revealed, value: "" });
});

test("balance history adapter requests exactly one explicit page", async () => {
  let requestedUrl = "";
  globalThis.fetch = async (input) => {
    requestedUrl = String(input);
    return new Response(JSON.stringify({
      source: "sub2api", status: "available", available: true, fetchedAt: "2026-07-24T00:00:00Z",
      data: { items: [], total: 41, page: 3, pageSize: 20, pages: 3 }
    }), { status: 200, headers: { "content-type": "application/json" } });
  };

  const result = await readApi.getGatewayBalanceHistory(3, 20);
  assert.equal(requestedUrl, "/api/gateway/balance-history?page=3&pageSize=20");
  assert.equal(result.data.page, 3);
  assert.equal(result.data.pageSize, 20);
  assert.equal(result.data.pages, 3);
});

test("API and Workspace navigation clear secrets for direct and popstate routes", async () => {
  const [controller, router] = await Promise.all([
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("apps/console-ui/src/app/console-router.ts")
  ]);
  assert.equal(isSensitiveConsoleRoute("/console/api"), true);
  assert.equal(isSensitiveConsoleRoute("/console/api/keys"), true);
  assert.equal(isSensitiveConsoleRoute("/console/workspaces/ws-1"), true);
  assert.equal(isSensitiveConsoleRoute("/console/billing"), false);
  assert.match(controller, /const clearSecrets = \(\) => \{[\s\S]+secretRequestGeneration\.current \+= 1/);
  assert.match(controller, /useEffect\(\(\) => \{[\s\S]+clearSecrets\(\);[\s\S]+\}, \[path\]\)/);
  assert.match(router, /window\.addEventListener\("popstate", onPopState\)/);
  assert.match(router, /pathname\.replace\("\/console\/gateway", "\/console\/api"\)/);
});

test("general API Key writes keep one full-input intent until authoritative readback", async () => {
  const panel = await source("apps/console-ui/src/components/keys/KeysPanel.tsx");
  assert.match(panel, /const createIntent = useRef<\{ signature: string; input: CreateGatewayKeyRequest; key: string \} \| null>/);
  assert.match(panel, /const signature = JSON\.stringify\(input\)/);
  assert.match(panel, /createGatewayKey\(createIntent\.current\.input, token, createIntent\.current\.key\)/);
  assert.match(panel, /const readback = await getGatewayKey\(created\.data\.id\)/);
  assert.match(panel, /keyMatchesCreate\(readback\.data, input\)/);
  assert.match(panel, /createIntent\.current = null/);
  assert.match(panel, /const updateIntents = useRef\(new Map/);
  assert.match(panel, /const deleteIntents = useRef\(new Map/);
  assert.match(panel, /revealed && useKey && revealed\.id === useKey\.id \? revealed\.value : "已隐藏"/);
});

test("session replacement invalidates late reads and clears route state", async () => {
  const [controller, panel] = await Promise.all([
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("apps/console-ui/src/components/keys/KeysPanel.tsx")
  ]);
  assert.match(controller, /requestGeneration\.current \+= 1/);
  assert.match(controller, /sessionGeneration\.current \+= 1/);
  assert.match(controller, /const isRequestCurrent = \(generation: number, userId\?: string\)/);
  assert.match(controller, /setReceiptCursor\(""\)/);
  assert.match(controller, /setReceiptCursorStack\(\[\]\)/);
  assert.match(controller, /setBalanceHistoryPage\(1\)/);
  assert.match(controller, /setSelectedUsageKeyId\(""\)/);
  assert.match(controller, /workspaceBudget: emptyRemote\(\)/);
  assert.match(controller, /setWorkspaceBudgetBusy\(false\)/);
  assert.match(controller, /workspaceBudgetBusyClaim\.current = null/);
  assert.match(controller, /workspaceBudgetIntents\.current\.clear\(\)/);
  assert.match(panel, /sessionGeneration\.current \+= 1/);
  assert.match(panel, /listGeneration\.current \+= 1/);
  assert.match(panel, /requestIsCurrent\(session, token\)/);
});

test("logout enters a protected hidden state before awaiting authority", async () => {
  const [controller, app, publicPages] = await Promise.all([
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("apps/console-ui/src/App.tsx"),
    source("apps/console-ui/src/pages/PublicPages.tsx")
  ]);
  const signOut = controller.slice(controller.indexOf("const signOut"), controller.indexOf("const refreshCurrentPage"));
  assert.match(controller, /logoutAndConfirm/);
  assert.doesNotMatch(controller, /logoutLocalFirst/);
  assert.match(signOut, /requestGeneration\.current \+= 1/);
  assert.match(signOut, /sessionGeneration\.current \+= 1/);
  assert.match(signOut, /resetConsoleState\(\)/);
  assert.match(signOut, /setAuthStatus\("logout_pending"\)/);
  assert.match(signOut, /setAuthStatus\("logout_unconfirmed"\)/);
  assert.ok(signOut.indexOf('setAuthStatus("logout_pending")') < signOut.indexOf("await logoutAndConfirm"));
  assert.match(app, /logout_pending[\s\S]+LogoutRecovery|LogoutRecovery[\s\S]+logout_pending/);
  assert.match(publicPages, /退出未确认/);
  assert.match(publicPages, /重试退出/);
});

test("Workspace detail, Runtime, and budget reads reject stale route readback", async () => {
  const controller = await source("apps/console-ui/src/app/use-console-controller.ts");
  assert.match(controller, /findWorkspaceInPages\(workspaceId\)/);
  assert.match(controller, /workspaceIdFromPath\(window\.location\.pathname\) === workspaceId/);
  assert.match(controller, /getWorkspaceRuntimeStatus\(workspaceId\)/);
  assert.match(controller, /getWorkspaceGatewayBudget\(workspaceId, workspaceKeyId\)/);
  assert.match(controller, /isRequestCurrent\(generation, activeSession\.user\.id\)/);
  assert.match(controller, /workspaceDetailGeneration === workspaceDetailRequestGeneration\.current/);
});

test("customer routes load only page-owned sources", async () => {
  const controller = await source("apps/console-ui/src/app/use-console-controller.ts");
  const routeLoader = controller.slice(controller.indexOf("const loadRoute"), controller.indexOf("useEffect(() =>", controller.indexOf("const loadRoute")));
  for (const route of [
    "/console/overview", "/console/workspaces", "/console/workspaces/new", "/console/api",
    "/console/api/usage", "/console/billing", "/console/announcements",
    "/admin/overview", "/admin/accounts", "/admin/billing", "/admin/resources", "/admin/system"
  ]) assert.match(routeLoader, new RegExp(route.replaceAll("/", "\\/")));
  assert.match(routeLoader, /\/console\/api["']\)[\s\S]+loadWallet[\s\S]+loadBalanceHistory[\s\S]+loadEndpoint/);
  assert.match(routeLoader, /\/console\/billing["']\)[\s\S]+loadWorkspaces[\s\S]+loadReceipts/);
});

test("per-Key usage rejects late key, period, and page responses", async () => {
  const controller = await source("apps/console-ui/src/app/use-console-controller.ts");
  assert.match(controller, /const usageRequestGeneration = useRef\(0\)/);
  assert.match(controller, /const usageGeneration = \+\+usageRequestGeneration\.current/);
  assert.match(controller, /usageGeneration !== usageRequestGeneration\.current/);
  assert.match(controller, /selectedUsageKeyIdRef\.current !== keyId/);
  assert.match(controller, /usageRequestGeneration\.current \+= 1/);
  assert.match(controller, /keys\.available && keys\.data\.items\.length === 0[\s\S]+setSelectedUsageKeyId\(""\)/);
});

test("account aggregate remains monthly when the per-Key period changes", async () => {
  const controller = await source("apps/console-ui/src/app/use-console-controller.ts");
  const aggregate = controller.slice(controller.indexOf("const loadAccountUsage"), controller.indexOf("const loadBalanceHistory"));
  const period = controller.slice(controller.indexOf("const chooseUsagePeriod"), controller.indexOf("const changeUsagePage"));
  assert.match(aggregate, /getGatewayAccountUsageSummary\("month"\)/);
  assert.doesNotMatch(aggregate, /usagePeriod/);
  assert.match(period, /loadUsage/);
  assert.doesNotMatch(period, /loadAccountUsage/);
});

test("Billing receipt pages preserve opaque cursor history and reject late responses", async () => {
  const [controller, pages] = await Promise.all([
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("apps/console-ui/src/pages/CustomerPages.tsx")
  ]);
  assert.match(controller, /const receiptCursorRef = useRef\(""\)/);
  assert.match(controller, /const receiptRequestGeneration = useRef\(0\)/);
  assert.match(controller, /const receiptGeneration = \+\+receiptRequestGeneration\.current/);
  assert.match(controller, /receiptGeneration === receiptRequestGeneration\.current && cursor === receiptCursorRef\.current/);
  assert.match(controller, /setReceiptCursorStack\(\(current\) => \[\.\.\.current, receiptCursorRef\.current\]\)/);
  assert.match(controller, /setReceiptCursorStack\(\(current\) => current\.slice\(0, -1\)\)/);
  assert.match(pages, /aria-label="账单收据分页"/);
});

test("Billing receipt detail validates identity and clears on page changes", async () => {
  const controller = await source("apps/console-ui/src/app/use-console-controller.ts");
  assert.match(controller, /const receiptDetailRequestGeneration = useRef\(0\)/);
  assert.match(controller, /const detailGeneration = \+\+receiptDetailRequestGeneration\.current/);
  assert.match(controller, /result\.available && result\.data\.receiptId !== receiptId/);
  assert.match(controller, /billing_receipt_identity_mismatch/);
  assert.match(controller, /const clearReceiptDetail = \(\) =>/);
  assert.match(controller, /clearReceiptDetail\(\);[\s\S]+getBillingReceipts\(cursor, limit\)/);
});

test("all Console mutations reject late shared-state writes after session replacement", async () => {
  const controller = await source("apps/console-ui/src/app/use-console-controller.ts");
  assert.match(controller, /const currentMutationRequest = \(\) =>/);
  for (const name of [
    "submitWorkspaceLaunch",
    "updateWorkspaceBudget",
    "rotateWorkspacePassword",
    "createSupportMapping",
    "markRead",
    "disableOperatorAccount",
    "submitWalletAdjustment",
    "refreshWalletOperation",
    "recoverWalletOperation",
    "provisionAccount",
    "createAnnouncement",
    "publishAnnouncement",
    "withdrawAnnouncement"
  ]) {
    const start = controller.indexOf(`const ${name}`);
    const next = controller.indexOf("\n  const ", start + 10);
    const body = controller.slice(start, next === -1 ? controller.length : next);
    assert.match(body, /const requestStillCurrent = currentMutationRequest\(\)/, `${name} must bind the mutation to its session`);
    assert.match(body, /if \(!requestStillCurrent\(\)\) return/, `${name} must reject late success`);
  }
});

test("Workspace detail admission precedes independent Runtime and budget reads", async () => {
  const controller = await source("apps/console-ui/src/app/use-console-controller.ts");
  const body = controller.slice(controller.indexOf("const loadWorkspaceDetail"), controller.indexOf("const loadUsage"));
  assert.ok(body.indexOf("findWorkspaceInPages(workspaceId)") < body.indexOf("getWorkspaceRuntimeStatus(workspaceId)"));
  assert.ok(body.indexOf("findWorkspaceInPages(workspaceId)") < body.indexOf("getWorkspaceGatewayBudget(workspaceId, workspaceKeyId)"));
  assert.match(body, /workspaceKeyId = detail\.data\.workspaceApiKeyId \|\| ""/);
  assert.match(body, /Promise\.allSettled\(\[\s*getWorkspaceRuntimeStatus\(workspaceId\),\s*getWorkspaceGatewayBudget\(workspaceId, workspaceKeyId\)\s*\]\)/);
  assert.match(body, /runtimeResult\.status === "fulfilled"[\s\S]+failSource\("runtime"/);
  assert.match(body, /budgetResult\.status === "fulfilled"[\s\S]+failSource\("workspaceBudget"/);
  assert.doesNotMatch(body, /Promise\.all\(\[\s*getWorkspaceRuntimeStatus/);
});

test("Workspace budget writes keep one exact-input intent and reject late writes", async () => {
  const controller = await source("apps/console-ui/src/app/use-console-controller.ts");
  const body = controller.slice(controller.indexOf("const updateWorkspaceBudget"), controller.indexOf("\n  const ", controller.indexOf("const updateWorkspaceBudget") + 10));
  assert.match(controller, /const workspaceBudgetIntents = useRef\(new Map/);
  assert.match(controller, /workspaceBudgetRequestSignature\(input\)/);
  assert.match(body, /const requestStillCurrent = currentMutationRequest\(\)/);
  assert.match(body, /sources\.workspaceBudget\.value\?\.available/);
  assert.match(body, /budget\.workspaceId !== workspace\.id/);
  assert.match(body, /workspaceBudgetBusyClaim\.current !== null/);
  assert.match(body, /const busyClaim = Symbol\("workspace-budget"\)/);
  assert.match(body, /workspaceBudgetBusyClaim\.current = busyClaim/);
  assert.match(body, /workspaceBudgetIntents\.current\.get\(workspace\.id\)/);
  assert.match(body, /intent\.keyId !== budget\.keyId/);
  assert.match(body, /intent\.signature !== signature/);
  assert.match(body, /上次模型预算更新结果待确认/);
  assert.match(body, /workspaceIdFromPath\(window\.location\.pathname\) === workspace\.id/);
  assert.match(body, /workspaceDetailGeneration === workspaceDetailRequestGeneration\.current/);
  assert.match(body, /updateWorkspaceGatewayBudget\([\s\S]+intent\.input[\s\S]+intent\.idempotencyKey/);
  assert.match(body, /updateSource\("workspaceBudget", \{ value: result, loading: false, error: "" \}\)/);
  assert.match(body, /result\.data\.keyId !== budget\.keyId/);
  assert.match(body, /workspaceBudgetResultMatchesInput\(result\.data, intent\.input\)/);
  assert.match(body, /模型预算已变化，请按最新状态重新提交/);
  assert.ok((body.match(/updateSource\("workspaceBudget", \{ value: result, loading: false, error: "" \}\)/g) || []).length >= 2);
  assert.match(body, /workspaceBudgetIntents\.current\.delete\(workspace\.id\)/);
  assert.match(body, /workspaceBudgetBusyClaim\.current === busyClaim/);
  assert.match(body, /workspaceBudgetBusyClaim\.current = null/);
  assert.doesNotMatch(body, /getWorkspaceGatewayBudget/);
});

test("request usage and usage summary settle independently", async () => {
  const controller = await source("apps/console-ui/src/app/use-console-controller.ts");
  const body = controller.slice(controller.indexOf("const loadUsage"), controller.indexOf("const loadUsageKeys"));
  assert.match(body, /Promise\.allSettled/);
  assert.match(body, /usageResult\.status === "fulfilled"/);
  assert.match(body, /summaryResult\.status === "fulfilled"/);
  assert.doesNotMatch(body, /Promise\.all\(\[\s*getGatewayKeyUsage/);
});

test("general API Key writes carry CSRF and opaque idempotency keys", async () => {
  const requests: Array<{ url: string; init?: RequestInit }> = [];
  globalThis.fetch = async (input, init) => {
    requests.push({ url: String(input), init });
    const status = init?.method === "DELETE" ? "deleted" : "active";
    return new Response(JSON.stringify({
      source: "sub2api", status: "available", available: true, fetchedAt: "2026-07-20T00:00:00Z",
      data: init?.method === "DELETE"
        ? { operationId: "op-delete", status }
        : { id: "41", name: "personal", kind: "general", status, groupId: "101", ipWhitelist: [], ipBlacklist: [],
            quotaUsdMicros: 1_000_000, quotaUsedUsdMicros: 0, rateLimit5hUsdMicros: 0, rateLimit1dUsdMicros: 0,
            rateLimit7dUsdMicros: 0, usage5hUsdMicros: 0, usage1dUsdMicros: 0, usage7dUsdMicros: 0,
            currentConcurrency: 0, lastUsedAt: null, lastUsedIp: null, expiresAt: "2026-08-19T00:00:00Z",
            createdAt: "2026-07-20T00:00:00Z", updatedAt: "2026-07-20T00:00:00Z", manageable: true, deletable: true }
    }), { status: 200, headers: { "content-type": "application/json" } });
  };

  await readApi.createGatewayKey({ name: "personal", groupId: "101", quotaUsdMicros: 1_000_000, expiresInDays: 30 }, "csrf-key", "key-create:opaque");
  await readApi.updateGatewayKey("41", { enabled: false }, "csrf-key", "key-toggle:opaque");
  await readApi.deleteGatewayKey("41", "csrf-key", "key-delete:opaque");

  assert.deepEqual(requests.map(({ url }) => url), ["/api/gateway/keys", "/api/gateway/keys/41", "/api/gateway/keys/41"]);
  assert.deepEqual(requests.map(({ init }) => init?.method), ["POST", "PATCH", "DELETE"]);
  assert.deepEqual(requests.map(({ init }) => new Headers(init?.headers).get("x-opl-csrf")), ["csrf-key", "csrf-key", "csrf-key"]);
  assert.deepEqual(requests.map(({ init }) => new Headers(init?.headers).get("Idempotency-Key")), ["key-create:opaque", "key-toggle:opaque", "key-delete:opaque"]);
});
