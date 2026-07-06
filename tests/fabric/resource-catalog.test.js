import assert from "node:assert/strict";
import test from "node:test";

import {
  availableWorkspacePackages,
  defaultFabricResourceCatalog,
  fabricCatalogReadiness,
  selectWorkspacePackage
} from "../../packages/fabric/src/resource-catalog.js";

test("Fabric resource catalog exposes verified CPU packages and keeps GPU unavailable until a node pool is verified", () => {
  const catalog = defaultFabricResourceCatalog({
    env: {
      OPL_WORKSPACE_STORAGE_CLASS: "cbs",
      OPL_WORKSPACE_IMAGE: "ccr.ccs.tencentyun.com/oplcloud/one-person-lab-app:20260702",
      OPL_WORKSPACE_DOMAIN: "workspace.medopl.cn"
    }
  });

  assert.deepEqual(availableWorkspacePackages(catalog).map((plan) => ({
    id: plan.id,
    accelerator: plan.accelerator,
    cpu: plan.cpu,
    memoryGb: plan.memoryGb,
    gpu: plan.gpu,
    storageClassId: plan.storageClassId,
    workspaceImageId: plan.workspaceImageId,
    environmentTemplateId: plan.environmentTemplateId
  })), [
    {
      id: "basic",
      accelerator: "cpu",
      cpu: 2,
      memoryGb: 4,
      gpu: 0,
      storageClassId: "workspace-cbs",
      workspaceImageId: "one-person-lab-app",
      environmentTemplateId: "opl-app-webui"
    },
    {
      id: "pro",
      accelerator: "cpu",
      cpu: 8,
      memoryGb: 16,
      gpu: 0,
      storageClassId: "workspace-cbs",
      workspaceImageId: "one-person-lab-app",
      environmentTemplateId: "opl-app-webui"
    }
  ]);
  assert.equal(selectWorkspacePackage(catalog, "gpu").available, false);
  assert.throws(
    () => selectWorkspacePackage(catalog, "gpu", { requireAvailable: true }),
    /package_unavailable:gpu:gpu_node_pool_not_verified/
  );
  assert.equal(catalog.storageClasses[0].storageClassName, "cbs");
  assert.equal(catalog.workspaceImages[0].image, "ccr.ccs.tencentyun.com/oplcloud/one-person-lab-app:20260702");
  assert.equal(catalog.ingressDomains[0].host, "workspace.medopl.cn");
  assert.deepEqual(catalog.environmentTemplates[0].persistentMounts, ["/data", "/projects"]);
  assert.equal(catalog.connectors[0].available, false);
  assert.equal(catalog.agentPackages[0].available, false);
});

test("Fabric catalog readiness summarizes available and unavailable resource classes", () => {
  const readiness = fabricCatalogReadiness(defaultFabricResourceCatalog());

  assert.equal(readiness.ready, true);
  assert.deepEqual(readiness.workspacePackages.available, ["basic", "pro"]);
  assert.deepEqual(readiness.workspacePackages.unavailable, []);
  assert.equal(readiness.workspacePackages.hiddenUnavailable, 1);
  assert.deepEqual(readiness.storageClasses.available, ["workspace-cbs"]);
  assert.deepEqual(readiness.workspaceImages.available, ["one-person-lab-app"]);
  assert.deepEqual(readiness.ingressDomains.available, ["workspace"]);
});

test("Fabric resource catalog maps product packages to Tencent instance types and node pools", () => {
  const catalog = defaultFabricResourceCatalog({
    env: {
      OPL_BASIC_COMPUTE_INSTANCE_TYPE: "SA5.MEDIUM4",
      OPL_BASIC_COMPUTE_NODE_POOL_ID: "np-basic-2c4g",
      OPL_PRO_COMPUTE_INSTANCE_TYPE: "SA5.2XLARGE16",
      OPL_PRO_COMPUTE_NODE_POOL_ID: "np-pro-8c16g"
    }
  });

  assert.deepEqual(catalog.workspacePackages.map((plan) => ({
    id: plan.id,
    server: plan.server,
    instanceType: plan.instanceType || "",
    nodePoolId: plan.nodePoolId || ""
  })), [
    {
      id: "basic",
      server: "2c4g",
      instanceType: "SA5.MEDIUM4",
      nodePoolId: "np-basic-2c4g"
    },
    {
      id: "pro",
      server: "8c16g",
      instanceType: "SA5.2XLARGE16",
      nodePoolId: "np-pro-8c16g"
    },
    {
      id: "gpu",
      server: "16c64g-1gpu",
      instanceType: "",
      nodePoolId: ""
    }
  ]);
});
