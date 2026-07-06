import assert from "node:assert/strict";
import test from "node:test";

import { createOplCloud } from "../../packages/console/src/opl-cloud.js";
import { MemoryStore } from "../../packages/console/src/store.js";
import { defaultFabricResourceCatalog } from "../../packages/fabric/src/resource-catalog.js";

const TEST_PRICING = {
  computeHourly: {
    basic: 1,
    pro: 4,
    gpu: 20
  },
  storageGbMonth: 0.2,
  markup: 0.2
};

function createService({ catalog = defaultFabricResourceCatalog() } = {}) {
  return createOplCloud({
    store: new MemoryStore(),
    fabricCatalog: catalog,
    runtimeProvider: {
      name: "catalog-test-provider",
      async readiness() {
        return {
          provider: "catalog-test-provider",
          ready: true,
          missingEnv: [],
          missingTools: []
        };
      }
    },
    pricing: TEST_PRICING
  });
}

test("Console package choices come from Fabric catalog and exclude unavailable GPU packages", async () => {
  const service = createService();

  assert.deepEqual(service.packages().map((plan) => plan.id), ["basic", "pro"]);
  assert.equal(service.resourceCatalog().workspacePackages.find((plan) => plan.id === "gpu").available, false);
  await service.manualTopUp({ accountId: "pi-alpha", amount: 5000, reason: "owner_credit" });
  await assert.rejects(
    service.createComputeAllocation({
      accountId: "pi-alpha",
      packageId: "gpu",
      name: "GPU compute"
    }),
    /package_unavailable:gpu:gpu_node_pool_not_verified/
  );
});

test("runtime readiness includes Fabric resource catalog state", async () => {
  const service = createService();

  const readiness = await service.runtimeReadiness();
  assert.equal(readiness.ready, true);
  assert.deepEqual(readiness.resourceCatalog.workspacePackages.available, ["basic", "pro"]);
  assert.deepEqual(readiness.resourceCatalog.workspacePackages.unavailable, []);
  assert.equal(readiness.resourceCatalog.workspacePackages.hiddenUnavailable, 1);
});
