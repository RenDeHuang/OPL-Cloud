import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  uiuxDemoAccounts,
  uiuxDemoAuthSeedJson,
  uiuxDemoPublicUrl
} from "../../tools/uiux-demo-fixture.js";

const repoRoot = new URL("../../", import.meta.url);

async function source(relativePath) {
  return readFile(new URL(relativePath, repoRoot), "utf8");
}

test("UIUX demo fixture provides stable owner and admin accounts", () => {
  assert.deepEqual(uiuxDemoAccounts.map((account) => `${account.label}:${account.email}:${account.password}`), [
    "Lab Owner:owner@opl.local:OplOwnerPass2026!",
    "Admin:admin@opl.local:OplAdminPass2026!"
  ]);

  const authSeed = JSON.parse(uiuxDemoAuthSeedJson());
  assert.deepEqual(authSeed.map((account) => account.role), ["pi", "admin"]);
  assert.equal(authSeed[0].accountId, "acct-owner-uiux");
  assert.equal(authSeed[1].accountId, "admin");
});

test("login page keeps one credential form without role shortcuts", async () => {
  const loginSource = await source("packages/console/ui/pages/LoginPage.jsx");
  const demoUiSource = await source("tools/start-uiux-demo-ui.js");
  const packageSource = JSON.parse(await source("package.json"));

  assert.doesNotMatch(loginSource, /demoAccounts|demoLoginPanel|UserRound|runtimeConfig/, "login must not expose role/demo account shortcuts");
  assert.doesNotMatch(demoUiSource, /VITE_OPL_DEMO_ACCOUNTS_JSON|VITE_OPL_DEMO_MODE/, "demo UI must not inject account choices into the browser");
  assert.equal(packageSource.scripts["demo:api"], "node tools/start-uiux-demo-api.js");
  assert.equal(packageSource.scripts["demo:ui"], "node tools/start-uiux-demo-ui.js");
});

test("UIUX demo Workspace URLs default to a network-reachable API origin", () => {
  const url = uiuxDemoPublicUrl({
    env: {},
    port: "8791",
    networkInterfaces: {
      lo: [{ family: "IPv4", address: "127.0.0.1", internal: true }],
      eth0: [{ family: "IPv4", address: "172.30.55.158", internal: false }]
    }
  });

  assert.equal(url, "http://172.30.55.158:8791");
  assert.equal(uiuxDemoPublicUrl({ env: { OPL_PUBLIC_URL: "https://workspace.example.com" }, port: "8791" }), "https://workspace.example.com");
  assert.equal(uiuxDemoPublicUrl({ env: {}, port: "8791", networkInterfaces: {} }), "http://127.0.0.1:8791");
});

test("UIUX demo API seeds the current compute storage attachment business chain", async () => {
  const demoApiSource = await source("tools/start-uiux-demo-api.js");

  for (const call of [
    "createComputeAllocation",
    "createStorageVolume",
    "attachStorage",
    "createWorkspace"
  ]) {
    assert.match(demoApiSource, new RegExp(`service\\.${call}\\(`), `demo API must seed via ${call}`);
  }
  assert.match(demoApiSource, /attachmentId: attachment\.id/, "Workspace seed must use an attachmentId");
  assert.doesNotMatch(demoApiSource, /createWorkspace\(\{[\s\S]*packageId: "basic"[\s\S]*\}\)/, "demo seed must not create Workspace directly from packageId");
});

test("UIUX demo API does not reset real TKE state unless explicitly requested", async () => {
  const demoApiSource = await source("tools/start-uiux-demo-api.js");

  assert.match(demoApiSource, /OPL_RUNTIME_PROVIDER/, "demo API must branch on runtime provider");
  assert.match(demoApiSource, /OPL_UIUX_DEMO_RESET === "1"/, "real runtime reset must require explicit opt-in");
  assert.match(demoApiSource, /runtimeReadiness/, "demo API must preflight runtime readiness before real TKE provisioning");
});
