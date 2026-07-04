import assert from "node:assert/strict";
import { access, readFile } from "node:fs/promises";
import test from "node:test";

const repoRoot = new URL("../../", import.meta.url);

async function source(relativePath) {
  return readFile(new URL(relativePath, repoRoot), "utf8");
}

async function exists(relativePath) {
  try {
    await access(new URL(relativePath, repoRoot));
    return true;
  } catch {
    return false;
  }
}

test("package scripts expose only staging-local and cloud verifier entrypoints", async () => {
  const packageSource = JSON.parse(await source("package.json"));

  assert.equal(packageSource.scripts["demo:api"], undefined);
  assert.equal(packageSource.scripts["demo:ui"], undefined);
  assert.equal(packageSource.scripts["staging:local"], "node tools/start-staging-local-api.js");
  assert.equal(packageSource.scripts["staging:ui"], "node tools/start-staging-local-ui.js");
  assert.equal(packageSource.scripts["staging:readiness"], "node tools/staging-readiness.js");
  assert.equal(packageSource.scripts["staging:e2e"], "node tools/staging-local-verifier.js");
  assert.equal(packageSource.scripts["verify:production"], "node tools/production-verifier.js");
});

test("local demo tooling is removed so local operation always targets staging cloud resources", async () => {
  assert.equal(await exists("tools/start-uiux-demo-api.js"), false);
  assert.equal(await exists("tools/start-uiux-demo-ui.js"), false);
  assert.equal(await exists("tools/uiux-demo-fixture.js"), false);
  assert.equal(await exists("packages/fabric/src/runtime-providers/local-docker.js"), false);
});

test("staging-local scripts load ignored staging env files and require explicit paid E2E confirmation", async () => {
  const localApiSource = await source("tools/start-staging-local-api.js");
  const readinessSource = await source("tools/staging-readiness.js");
  const verifierSource = await source("tools/staging-local-verifier.js");
  const envExample = await source("deploy/tke/opl-cloud-staging.local.env.example");

  assert.match(localApiSource, /loadEnvFile/, "local staging API must load .env.staging.local");
  assert.match(localApiSource, /validateStagingLocalEnv/, "local staging API must validate cloud env before serving");
  assert.match(readinessSource, /validateStagingLocalEnv/, "readiness must use the same env contract");
  assert.match(verifierSource, /OPL_CONFIRM_REAL_CLOUD_E2E/, "real cloud E2E must require explicit confirmation");
  assert.match(verifierSource, /allowPrivateConsoleOrigin: true/, "local-to-staging verifier may use a private local Console origin");
  assert.match(envExample, /OPL_RUNTIME_PROVIDER=tencent-tke/);
  assert.match(envExample, /OPL_CONFIRM_REAL_CLOUD_E2E=0/);
});
