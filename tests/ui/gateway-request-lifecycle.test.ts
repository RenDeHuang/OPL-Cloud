import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { afterEach, test } from "node:test";

import * as authApi from "../../apps/console-ui/src/api/auth-api.ts";
import { maskGatewayKey } from "../../apps/console-ui/src/console-model.ts";

const originalFetch = globalThis.fetch;
const appSource = () => readFile(new URL("../../apps/console-ui/src/App.vue", import.meta.url), "utf8");

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

test("API Key cleanup removes the raw value", () => {
  const revealed = { id: "41", name: "opl-workspace", status: "active" as const, value: "sk-raw" };
  assert.deepEqual(maskGatewayKey(revealed), { ...revealed, value: "" });
});

test("late secret responses cannot repopulate credentials after route cleanup", async () => {
  const app = await appSource();
  assert.match(app, /let secretRequestGeneration = 0/);
  assert.match(app, /function clearSecrets\(\) \{\s*secretRequestGeneration \+= 1;/);
  assert.equal((app.match(/if \(!secretResponseStillCurrent\([^)]+\)\) return;/g) || []).length, 3);
  assert.match(app, /watch\(path,[\s\S]+clearSecrets\(\);[\s\S]+handleRoute/);
});
