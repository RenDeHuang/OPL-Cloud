import assert from "node:assert/strict";
import test from "node:test";

import { defaultFabricResourceCatalog } from "../../packages/fabric/src/resource-catalog.js";
import {
  createCurrentTestService,
  currentResourceProvider,
  TEST_PRICING
} from "../helpers/current-resource-chain.js";

function createService({ catalog = defaultFabricResourceCatalog() } = {}) {
  return createCurrentTestService({
    fabricCatalog: catalog,
    runtimeProvider: currentResourceProvider({
      async readiness() {
        return {
          provider: "catalog-test-provider",
          ready: true,
          missingEnv: [],
          missingTools: []
        };
      }
    }),
    pricing: TEST_PRICING
  });
}

test("Console package choices come from Fabric catalog and exclude unavailable GPU packages", async () => {
  const service = createService();

  assert.deepEqual(service.packages().map((plan) => plan.id), ["basic", "pro"]);
  assert.equal(service.resourceCatalog().workspacePackages.find((plan) => plan.id === "gpu").available, false);
  await service.manualTopUp({ accountId: "pi-alpha", amount: 5000, reason: "owner_credit" });
  await assert.rejects(
    service.createComputeResource({
      accountId: "pi-alpha",
      userId: "usr-alpha",
      name: "GPU node",
      packageId: "gpu"
    }),
    /package_unavailable:gpu:gpu_node_pool_not_verified/
  );
});

test("runtime readiness includes Fabric resource catalog state", async () => {
  const service = createService();

  const readiness = await service.runtimeReadiness();
  assert.equal(readiness.ready, true);
  assert.deepEqual(readiness.resourceCatalog.workspacePackages.available, ["basic", "pro"]);
  assert.deepEqual(readiness.resourceCatalog.workspacePackages.unavailable, [
    { id: "gpu", reason: "gpu_node_pool_not_verified" }
  ]);
});
