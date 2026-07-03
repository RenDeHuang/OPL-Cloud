import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  uiuxDemoAccounts,
  uiuxDemoAccountsJson,
  uiuxDemoAuthSeedJson
} from "../../tools/uiux-demo-fixture.js";

const repoRoot = new URL("../../", import.meta.url);

async function source(relativePath) {
  return readFile(new URL(relativePath, repoRoot), "utf8");
}

test("UIUX demo fixture provides stable owner and admin accounts", () => {
  assert.deepEqual(uiuxDemoAccounts.map((account) => `${account.label}:${account.email}:${account.password}`), [
    "Lab Owner:owner@opl.local:owner-demo-2026",
    "Admin:admin@opl.local:admin-demo-2026"
  ]);

  const authSeed = JSON.parse(uiuxDemoAuthSeedJson());
  assert.deepEqual(authSeed.map((account) => account.role), ["pi", "admin"]);
  assert.equal(authSeed[0].accountId, "acct-owner-uiux");
  assert.equal(authSeed[1].accountId, "admin");

  const uiAccounts = JSON.parse(uiuxDemoAccountsJson());
  assert.deepEqual(Object.keys(uiAccounts[0]).sort(), ["email", "label", "password"]);
});

test("demo account shortcuts are gated behind demo runtime config", async () => {
  const runtimeSource = await source("packages/console/ui/config/runtime-config.js");
  const loginSource = await source("packages/console/ui/pages/LoginPage.jsx");
  const packageSource = JSON.parse(await source("package.json"));

  assert.match(runtimeSource, /VITE_OPL_DEMO_MODE/, "demo mode must require explicit Vite env");
  assert.match(loginSource, /config\.demoAccounts\.length > 0/, "login shortcuts must come from runtime demo config");
  assert.equal(packageSource.scripts["demo:api"], "node tools/start-uiux-demo-api.js");
  assert.equal(packageSource.scripts["demo:ui"], "node tools/start-uiux-demo-ui.js");
});
