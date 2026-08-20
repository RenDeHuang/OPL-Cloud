import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);
const text = (path: string) => readFile(new URL(path, root), "utf8");

test("Workspace Runtime ABI has one fixed cross-module WebUI port owner", async () => {
  const contract = JSON.parse(await text("packages/contracts/opl-cloud-workspace-runtime-abi-contract.json"));

  assert.equal(contract.schemaVersion, 1);
  assert.equal(contract.owner, "OPL Cloud Platform Architecture");
  assert.deepEqual(contract.workspaceWebUI, {
    protocol: "http",
    port: 3000,
    portSemantics: "fixed_cross_module_compatibility_abi",
    configuration: {
      instanceSelectable: false,
      environmentOverride: "forbidden"
    },
    projections: [
      "control_plane_proxy_target",
      "fabric_local_docker_container_target_and_healthcheck",
      "fabric_tencent_tke_container_probe_service_and_network_policy"
    ]
  });
});

test("production readiness does not expose the fixed Runtime ABI as environment configuration", async () => {
  const [implementation, readinessTest] = await Promise.all([
    text("services/fabric/ops/production-readiness.ts"),
    text("tests/production/production-readiness.test.ts")
  ]);

  assert.doesNotMatch(implementation, /OPL_WORKSPACE_WEBUI_PORT/);
  assert.doesNotMatch(readinessTest, /OPL_WORKSPACE_WEBUI_PORT/);
});
