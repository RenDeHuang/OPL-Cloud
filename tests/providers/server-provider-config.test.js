import assert from "node:assert/strict";
import test from "node:test";

import { createStoreFromEnv } from "../../packages/console/api/server.js";
import { createRuntimeProvider } from "../../packages/fabric/src/runtime-provider-factory.js";
import { TencentCvmProvider } from "../../packages/fabric/src/runtime-providers/tencent-cvm.js";
import { TencentTkeProvider } from "../../packages/fabric/src/runtime-providers/tencent-tke.js";
import { JsonFileStore, PostgresStore } from "../../packages/console/src/store.js";

test("server default runtime provider is local Docker", () => {
  const provider = createRuntimeProvider({
    env: {},
    rootDir: ".runtime/test-provider"
  });

  assert.equal(provider.name, "local-docker");
});

test("unknown provider is not selectable for runtime execution", () => {
  assert.throws(
    () => createRuntimeProvider({ env: { OPL_RUNTIME_PROVIDER: "simulation" }, rootDir: ".runtime/test-provider" }),
    /unknown_runtime_provider:simulation/
  );
});

test("Tencent CVM provider is selectable for cloud runtime execution", () => {
  const provider = createRuntimeProvider({
    env: { OPL_RUNTIME_PROVIDER: "tencent-cvm" },
    rootDir: ".runtime/test-provider"
  });

  assert.equal(provider.name, "tencent-cvm");
});

test("Tencent TKE provider is selectable for production runtime execution", () => {
  const provider = createRuntimeProvider({
    env: { OPL_RUNTIME_PROVIDER: "tencent-tke" },
    rootDir: ".runtime/test-provider"
  });

  assert.ok(provider instanceof TencentTkeProvider);
  assert.equal(provider.name, "tencent-tke");
});

test("Tencent CVM provider fails closed until required cloud environment is present", async () => {
  const provider = new TencentCvmProvider({ env: {} });
  await assert.rejects(
    provider.createWorkspaceRuntime({
      workspaceId: "ws-test",
      workspaceName: "Cloud Lab",
      packagePlan: { id: "basic", server: "2c4g", diskGb: 10 },
      token: "share_test"
    }),
    /tencent_cvm_provider_missing_env/
  );
});

test("server uses PostgreSQL store only when DATABASE_URL is configured", () => {
  assert.ok(createStoreFromEnv({}) instanceof JsonFileStore);
  assert.ok(createStoreFromEnv({ DATABASE_URL: "postgres://opl:secret@127.0.0.1:5432/opl_cloud" }) instanceof PostgresStore);
});
