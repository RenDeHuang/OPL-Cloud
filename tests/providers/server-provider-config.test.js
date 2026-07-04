import assert from "node:assert/strict";
import test from "node:test";

import { createStoreFromEnv } from "../../packages/console/api/server.js";
import { createRuntimeProvider } from "../../packages/fabric/src/runtime-provider-factory.js";
import { TencentTkeProvider } from "../../packages/fabric/src/runtime-providers/tencent-tke.js";
import { JsonFileStore, PostgresStore } from "../../packages/console/src/store.js";

test("server default runtime provider is Tencent TKE", () => {
  const provider = createRuntimeProvider({
    env: {},
    rootDir: ".runtime/test-provider"
  });

  assert.ok(provider instanceof TencentTkeProvider);
  assert.equal(provider.name, "tencent-tke");
});

test("unknown provider is not selectable for runtime execution", () => {
  assert.throws(
    () => createRuntimeProvider({ env: { OPL_RUNTIME_PROVIDER: "simulation" }, rootDir: ".runtime/test-provider" }),
    /unknown_runtime_provider:simulation/
  );
});

test("Tencent TKE provider is selectable for production runtime execution", () => {
  const provider = createRuntimeProvider({
    env: { OPL_RUNTIME_PROVIDER: "tencent-tke" },
    rootDir: ".runtime/test-provider"
  });

  assert.ok(provider instanceof TencentTkeProvider);
  assert.equal(provider.name, "tencent-tke");
});

test("non-TKE production provider names are not selectable for runtime execution", () => {
  assert.throws(
    () => createRuntimeProvider({ env: { OPL_RUNTIME_PROVIDER: "unsupported-production-runtime" }, rootDir: ".runtime/test-provider" }),
    /unknown_runtime_provider:unsupported-production-runtime/
  );
});

test("local Docker is not selectable as a runtime provider", () => {
  assert.throws(
    () => createRuntimeProvider({ env: { OPL_RUNTIME_PROVIDER: "local-docker" }, rootDir: ".runtime/test-provider" }),
    /unknown_runtime_provider:local-docker/
  );
});

test("server uses PostgreSQL store only when DATABASE_URL is configured", () => {
  assert.ok(createStoreFromEnv({}) instanceof JsonFileStore);
  assert.ok(createStoreFromEnv({ DATABASE_URL: "postgres://opl:secret@127.0.0.1:5432/opl_cloud" }) instanceof PostgresStore);
});
