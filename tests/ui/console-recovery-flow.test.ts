import assert from "node:assert/strict";
import { afterEach, test } from "node:test";
import { readFile } from "node:fs/promises";

import { currentSession } from "../../apps/console-ui/src/api/auth-api.ts";
import { customerSafeMessage } from "../../apps/console-ui/src/api/console-api.ts";

const root = new URL("../../", import.meta.url);
const originalFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = originalFetch;
});

async function source(path) {
  return readFile(new URL(path, root), "utf8");
}

function jsonResponse(payload, status) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "content-type": "application/json" }
  });
}

test("session bootstrap treats only HTTP 401 as signed out and sends a timeout signal", async () => {
  let signal;
  globalThis.fetch = async (_url, init) => {
    signal = init?.signal;
    return jsonResponse({ error: "not_authenticated" }, 401);
  };

  assert.equal(await currentSession(), null);
  assert.ok(signal instanceof AbortSignal);

  globalThis.fetch = async () => jsonResponse({ error: "auth_backend_unavailable" }, 503);
  await assert.rejects(currentSession(), /auth_backend_unavailable/);

  globalThis.fetch = async () => jsonResponse({}, 200);
  await assert.rejects(currentSession(), /session_check_failed/);

  globalThis.fetch = async () => new Response("not-json", { status: 200 });
  await assert.rejects(currentSession(), /session_check_failed/);
});

test("all Console requests use the native ten-second timeout", async () => {
  const [authSource, apiSource] = await Promise.all([
    source("apps/console-ui/src/api/auth-api.ts"),
    source("apps/console-ui/src/api/console-api.ts")
  ]);

  assert.match(authSource, /AbortSignal\.timeout\(10_000\)/);
  assert.equal((apiSource.match(/AbortSignal\.timeout\(10_000\)/g) || []).length, 2);
});

test("only Workspace readiness errors use the Docker distribution message", () => {
  assert.equal(customerSafeMessage({ error: "workspace_runtime_not_ready" }), "正在分发 Docker，预计 3-5 分钟，请稍后再打开 URL。");
  assert.equal(customerSafeMessage({ error: "gateway_upstream_unavailable" }), "gateway_upstream_unavailable");
});

test("auth, lazy route, and account state expose distinct recovery states", async () => {
  const [mainSource, consoleSource] = await Promise.all([
    source("apps/console-ui/src/main.tsx"),
    source("apps/console-ui/src/pages/ConsolePage.tsx")
  ]);

  assert.match(mainSource, /正在验证登录/);
  assert.match(mainSource, /无法验证登录状态/);
  assert.match(mainSource, /重试/);
  assert.match(mainSource, /正在加载 Console 界面/);
  assert.match(consoleSource, /正在加载账号数据/);
  assert.match(mainSource, /redirectToLogin\(window\.location\.pathname \+ window\.location\.search \+ window\.location\.hash\)/);
  assert.match(mainSource, /authRedirectTarget\(\)/);
});

test("Workspace runtime polling stops after thirty attempts and exposes manual retry", async () => {
  const sourceText = await source("apps/console-ui/src/pages/workspaces/WorkspaceDetailPage.tsx");

  assert.match(sourceText, /RUNTIME_POLL_INTERVAL_MS\s*=\s*10_000/);
  assert.match(sourceText, /RUNTIME_POLL_MAX_ATTEMPTS\s*=\s*30/);
  assert.match(sourceText, /terminalRuntimeStates/);
  assert.match(sourceText, /setPollError\(err\.message/);
  assert.match(sourceText, /等待已超过 5 分钟/);
  assert.match(sourceText, /手动重试/);
  assert.match(sourceText, /setPollRun\(\(value\) => value \+ 1\)/);
});
