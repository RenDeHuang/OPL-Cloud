import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import test from "node:test";

function envKeys(source) {
  return source
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line && !line.startsWith("#") && /^[A-Z0-9_]+=/.test(line))
    .map((line) => line.slice(0, line.indexOf("=")));
}

function envValue(source, key) {
  const line = source
    .split("\n")
    .map((item) => item.trim())
    .find((item) => item.startsWith(`${key}=`));
  return line ? line.slice(line.indexOf("=") + 1) : undefined;
}

test("production env contract has one TKE template and no obsolete local inputs", async () => {
  assert.equal(existsSync(".env.production.inputs.example"), false);

  const gitignore = await readFile(".gitignore", "utf8");
  assert.equal(gitignore.includes("!.env.preproduction.inputs.example"), false);
  assert.equal(gitignore.includes("!.env.production.inputs.example"), false);
});

test("TKE production env template exposes only consumed production inputs", async () => {
  const source = await readFile("deploy/tke/opl-cloud-production.env.example", "utf8");
  const keys = envKeys(source);
  const obsoleteKeys = [
    "OPL_PRODUCT_NAME",
    "OPL_ENV",
    "OPL_GPU_COMPUTE_HOURLY_CNY",
    "OPL_INGRESS_CLB_DNS_NAME",
    "OPL_INGRESS_CLB_IP",
    "OPL_WORKSPACE_URL_MODE",
    "OPL_WORKSPACE_URL_TEMPLATE",
    "OPL_WORKSPACE_STORAGE_SIZE_GB"
  ];

  for (const key of obsoleteKeys) {
    assert.equal(keys.includes(key), false, `${key} should not be part of the production env contract`);
  }
});

test("price catalog defaults stay aligned with the production Tencent snapshot", async () => {
  const expected = {
    OPL_BASIC_COMPUTE_HOURLY_CNY: "0.39",
    OPL_PRO_COMPUTE_HOURLY_CNY: "3.09",
    OPL_STORAGE_GB_MONTH_CNY: "0.36",
    OPL_BILLING_MARKUP: "0.2"
  };
  const template = await readFile("deploy/tke/opl-cloud-production.env.example", "utf8");
  const localExample = await readFile(".env.example", "utf8");
  const server = await readFile("packages/console/api/server.js", "utf8");

  for (const [key, value] of Object.entries(expected)) {
    assert.equal(envValue(template, key), value, `${key} should match the TKE template`);
    assert.equal(envValue(localExample, key), value, `${key} should match the local env example`);
  }

  assert.match(server, /OPL_BASIC_COMPUTE_HOURLY_CNY", 0\.39\)/);
  assert.match(server, /OPL_PRO_COMPUTE_HOURLY_CNY", 3\.09\)/);
  assert.match(server, /OPL_STORAGE_GB_MONTH_CNY", 0\.36\)/);
  assert.match(server, /OPL_BILLING_MARKUP", 0\.2\)/);
});
