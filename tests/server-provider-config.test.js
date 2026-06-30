import assert from "node:assert/strict";
import test from "node:test";

import { createRuntimeProvider } from "../services/api/src/runtime-provider-factory.js";
import { TencentCvmProvider } from "../services/api/src/runtime-providers/tencent-cvm.js";

test("server default runtime provider is local Docker", () => {
  const provider = createRuntimeProvider({
    env: {},
    rootDir: ".runtime/test-provider"
  });

  assert.equal(provider.name, "local-docker");
});

test("simulation provider is not selectable for runtime execution", () => {
  assert.throws(
    () => createRuntimeProvider({ env: { OPL_RUNTIME_PROVIDER: "fake" }, rootDir: ".runtime/test-provider" }),
    /fake_provider_disabled/
  );
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
