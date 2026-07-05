import assert from "node:assert/strict";
import test from "node:test";

import { createOplCloud } from "../../packages/console/src/opl-cloud.js";
import { MemoryStore } from "../../packages/console/src/store.js";
import { createFakeRuntimeProvider } from "../helpers/fake-runtime-provider.js";

const TEST_PRICING = {
  serverHourly: {
    basic: 1,
    pro: 4
  },
  diskGbMonth: 0.2,
  markup: 0.2
};

function createService(runtimeProviderOverrides = {}) {
  return createOplCloud({
    store: new MemoryStore(),
    pricing: TEST_PRICING,
    runtimeProvider: {
      name: "test-resource-provider",
      workspaceUrl({ slug, token }) {
        return `https://workspace.example.test/w/${slug}?token=${token}`;
      },
      async createComputeAllocation({ computeAllocationId, packagePlan }) {
        return {
          providerResourceId: `node/node-${computeAllocationId}`,
          operationId: `op-${computeAllocationId}`,
          poolId: `pool-${packagePlan.id}-${packagePlan.server}`,
          nodePoolId: `np-${packagePlan.id}`,
          instanceId: `ins-${computeAllocationId}`,
          nodeName: `node-${computeAllocationId}`,
          privateIp: "10.0.0.21",
          publicIp: "",
          status: "running",
          billingStatus: "active",
          spec: packagePlan.server,
          image: "ghcr.io/gaofeng21cn/one-person-lab-app:latest"
        };
      },
      async createStorageVolume({ storageId }) {
        return {
          providerResourceId: `provider-${storageId}`,
          status: "available",
          billingStatus: "active"
        };
      },
      async attachStorage({ attachmentId }) {
        return {
          providerAttachmentId: `provider-${attachmentId}`,
          status: "attached"
        };
      },
      async createWorkspaceEntry({ workspaceId, slug, token }) {
        return {
          slug,
          url: `https://workspace.example.test/w/${workspaceId}?token=${token}`,
          status: "ready"
        };
      },
      ...runtimeProviderOverrides
    }
  });
}

test("account provisions compute, storage, attachment, then a Workspace URL entry", async () => {
  const service = createService();

  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });

  const compute = await service.createComputeAllocation({
    accountId: "pi-alpha",
    userId: "usr-alpha",
    packageId: "basic",
    name: "CPU analysis node"
  });
  const storage = await service.createStorageVolume({
    accountId: "pi-alpha",
    userId: "usr-alpha",
    packageId: "basic",
    sizeGb: 20,
    name: "Grant data volume"
  });
  const attachment = await service.attachStorage({
    accountId: "pi-alpha",
    computeAllocationId: compute.id,
    storageId: storage.id,
    mountPath: "/data"
  });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    userId: "usr-alpha",
    workspaceName: "Grant Lab",
    attachmentId: attachment.id
  });

  assert.equal(compute.ownerAccountId, "pi-alpha");
  assert.equal(compute.packageId, "basic");
  assert.equal(compute.status, "running");
  assert.equal(storage.ownerAccountId, "pi-alpha");
  assert.equal(storage.sizeGb, 20);
  assert.equal(storage.status, "available");
  assert.equal(attachment.computeAllocationId, compute.id);
  assert.equal(attachment.storageId, storage.id);
  assert.equal(attachment.status, "attached");
  assert.equal(workspace.attachmentId, attachment.id);
  assert.equal(workspace.computeAllocationId, compute.id);
  assert.equal(workspace.storageId, storage.id);
  assert.match(workspace.url, /^https:\/\/workspace\.example\.test\/w\//);

  const state = await service.getState("pi-alpha");
  assert.deepEqual(state.computeAllocations.map((item) => item.id), [compute.id]);
  assert.deepEqual(state.storageVolumes.map((item) => item.id), [storage.id]);
  assert.deepEqual(state.storageAttachments.map((item) => item.id), [attachment.id]);
  assert.deepEqual(state.workspaces.map((item) => item.id), [workspace.id]);
  assert.equal(state.billingLedger.some((entry) => entry.computeAllocationId === compute.id), true);
  assert.equal(state.billingLedger.some((entry) => entry.storageId === storage.id), true);
  assert.equal(state.billingLedger.some((entry) => entry.attachmentId === attachment.id), true);
  assert.equal(state.resourceUsageLogs.some((entry) => entry.computeAllocationId === compute.id), true);
  assert.equal(state.resourceUsageLogs.some((entry) => entry.storageId === storage.id), true);
  assert.equal(state.resourceUsageLogs.some((entry) => entry.attachmentId === attachment.id), true);
});

test("compute allocation persists provisioner ownership, pricing, hold, and operation state", async () => {
  const service = createService({
    async createComputeAllocation({ computeAllocationId }) {
      return {
        providerResourceId: "node/node-created",
        operationId: "op-create-alpha",
        poolId: "pool-basic-2c4g",
        nodePoolId: "np-basic",
        instanceId: "ins-created",
        nodeName: "node-created",
        privateIp: "10.0.0.12",
        publicIp: "",
        status: "running",
        billingStatus: "active",
        spec: "2c4g",
        image: "ghcr.io/gaofeng21cn/one-person-lab-app:latest",
        providerRequestId: "req-scale",
        providerData: { scaleNodePoolRequestId: "req-scale", nodeName: "node-created", privateIp: "10.0.0.12", computeAllocationId }
      };
    }
  });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });
  const compute = await service.createComputeAllocation({
    accountId: "pi-alpha",
    userId: "usr-alpha",
    packageId: "basic",
    name: "CPU analysis node"
  });
  const state = await service.getState("pi-alpha");
  const operation = state.runtimeOperations.find((item) => item.resourceId === compute.id);

  assert.equal(compute.providerResourceId, "node/node-created");
  assert.equal(compute.operationId, "op-create-alpha");
  assert.equal(compute.poolId, "pool-basic-2c4g");
  assert.equal(compute.nodePoolId, "np-basic");
  assert.equal(compute.instanceId, "ins-created");
  assert.equal(compute.nodeName, "node-created");
  assert.equal(compute.privateIp, "10.0.0.12");
  assert.equal(compute.publicIp, "");
  assert.equal(compute.lastProviderSyncAt.length > 0, true);
  assert.equal(compute.hourlyPrice, 1.2);
  assert.equal(compute.holdAmount, 201.6);
  assert.deepEqual(compute.balanceImpact, {
    balanceBefore: 300,
    frozenBefore: 0,
    frozenAfter: 201.6,
    availableAfter: 98.4
  });
  assert.ok(operation, "expected a resource operation row");
  assert.equal(operation.operationType, "create_compute_allocation");
  assert.equal(operation.resourceType, "compute_allocation");
  assert.equal(operation.status, "completed");
  assert.equal(operation.providerRequestId, "req-scale");
});

test("compute allocation cannot complete without a dedicated CVM or node identity", async () => {
  const service = createService({
    async createComputeAllocation() {
      return {
        providerResourceId: "nodepool/np-basic",
        operationId: "op-create-alpha",
        poolId: "pool-basic-2c4g",
        nodePoolId: "np-basic",
        status: "provisioning",
        billingStatus: "active",
        spec: "2c4g",
        image: "ghcr.io/gaofeng21cn/one-person-lab-app:latest",
        providerRequestId: "req-scale"
      };
    }
  });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });
  await assert.rejects(
    service.createComputeAllocation({
      accountId: "pi-alpha",
      userId: "usr-alpha",
      packageId: "basic",
      name: "CPU analysis node"
    }),
    /compute_allocation_node_identity_required/
  );

  const state = await service.getState("pi-alpha");
  const compute = state.computeAllocations[0];
  const operation = state.runtimeOperations.find((item) => item.resourceId === compute.id);
  assert.equal(compute.status, "failed");
  assert.equal(compute.safeMessage, "计算资源未返回独占节点，请重试或联系支持。");
  assert.equal(operation.status, "failed");
});

test("failed compute allocation remains visible with safe provider failure state", async () => {
  const service = createService({
    async createComputeAllocation() {
      const error = new Error("tencent_permission_denied");
      error.safeMessage = "CAM denied ScaleNodePool";
      error.providerRequestId = "req-denied";
      error.retryable = false;
      error.providerData = { action: "create_compute_allocation" };
      throw error;
    }
  });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });
  await assert.rejects(
    service.createComputeAllocation({
      accountId: "pi-alpha",
      userId: "usr-alpha",
      packageId: "basic",
      name: "CPU analysis node"
    }),
    /tencent_permission_denied/
  );

  const state = await service.getState("pi-alpha");
  const compute = state.computeAllocations[0];
  const operation = state.runtimeOperations.find((item) => item.resourceId === compute.id);

  assert.equal(compute.status, "failed");
  assert.equal(compute.error, "tencent_permission_denied");
  assert.equal(compute.safeMessage, "CAM denied ScaleNodePool");
  assert.equal(compute.providerRequestId, "req-denied");
  assert.equal(compute.retryable, false);
  assert.equal(compute.providerData, undefined);
  assert.equal(operation.status, "failed");
  assert.equal(operation.safeMessage, "CAM denied ScaleNodePool");
  assert.equal(operation.providerRequestId, "req-denied");
});

test("Workspace URL creation requires an attached storage and compute pair", async () => {
  const service = createService();

  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });
  const compute = await service.createComputeAllocation({
    accountId: "pi-alpha",
    packageId: "basic",
    name: "CPU analysis node"
  });
  const storage = await service.createStorageVolume({
    accountId: "pi-alpha",
    packageId: "basic",
    sizeGb: 10,
    name: "Grant data volume"
  });

  await assert.rejects(
    service.createWorkspace({
      accountId: "pi-alpha",
      workspaceName: "Unattached Lab",
      attachmentId: "attach-missing"
    }),
    /storage_attachment_not_found/
  );

  await assert.rejects(
    service.createWorkspace({
      accountId: "pi-alpha",
      workspaceName: "Package Only Lab",
      packageId: "basic"
    }),
    /workspace_attachment_required/
  );

  const attachment = await service.attachStorage({
    accountId: "pi-alpha",
    computeAllocationId: compute.id,
    storageId: storage.id,
    mountPath: "/data"
  });
  await service.detachStorage({
    accountId: "pi-alpha",
    attachmentId: attachment.id,
    confirm: true
  });

  await assert.rejects(
    service.createWorkspace({
      accountId: "pi-alpha",
      workspaceName: "Detached Lab",
      attachmentId: attachment.id
    }),
    /storage_attachment_not_attached/
  );
});

test("storage detach retry completes when a previous provider attempt left the attachment detaching", async () => {
  let failDetach = true;
  const service = createService({
    async detachStorage() {
      if (failDetach) throw new Error("provider_detach_transient_failure");
      return { status: "detached" };
    }
  });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });
  const compute = await service.createComputeAllocation({
    accountId: "pi-alpha",
    packageId: "basic",
    name: "CPU analysis node"
  });
  const storage = await service.createStorageVolume({
    accountId: "pi-alpha",
    packageId: "basic",
    sizeGb: 10,
    name: "Grant data volume"
  });
  const attachment = await service.attachStorage({
    accountId: "pi-alpha",
    computeAllocationId: compute.id,
    storageId: storage.id,
    mountPath: "/data"
  });

  await assert.rejects(
    service.detachStorage({ accountId: "pi-alpha", attachmentId: attachment.id, confirm: true }),
    /provider_detach_transient_failure/
  );
  assert.equal((await service.getState("pi-alpha")).storageAttachments[0].status, "detaching");

  failDetach = false;
  const detached = await service.detachStorage({
    accountId: "pi-alpha",
    attachmentId: attachment.id,
    confirm: true
  });

  assert.equal(detached.status, "detached");
  const state = await service.getState("pi-alpha");
  assert.equal(state.storageAttachments[0].status, "detached");
  assert.deepEqual(state.computeAllocations[0].attachedStorageIds, []);
  assert.deepEqual(state.storageVolumes[0].attachmentIds, []);
});

test("storage cannot attach across accounts", async () => {
  const service = createService();

  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });
  await service.manualTopUp({ accountId: "pi-beta", amount: 300, reason: "owner_credit" });
  const compute = await service.createComputeAllocation({
    accountId: "pi-alpha",
    packageId: "basic",
    name: "Alpha compute"
  });
  const storage = await service.createStorageVolume({
    accountId: "pi-beta",
    packageId: "basic",
    sizeGb: 10,
    name: "Beta storage"
  });

  await assert.rejects(
    service.attachStorage({
      accountId: "pi-alpha",
      computeAllocationId: compute.id,
      storageId: storage.id,
      mountPath: "/data"
    }),
    /storage_volume_not_found/
  );
});

test("resource service preserves provider handles for one-person-lab-app runtime", async () => {
  const service = createOplCloud({
    store: new MemoryStore(),
    pricing: TEST_PRICING,
    runtimeProvider: createFakeRuntimeProvider()
  });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });
  const compute = await service.createComputeAllocation({
    accountId: "pi-alpha",
    packageId: "basic",
    name: "Dedicated compute"
  });
  const storage = await service.createStorageVolume({
    accountId: "pi-alpha",
    packageId: "basic",
    sizeGb: 10,
    name: "Persistent storage"
  });
  const attachment = await service.attachStorage({
    accountId: "pi-alpha",
    computeAllocationId: compute.id,
    storageId: storage.id,
    mountPath: "/data"
  });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Cloud WebUI Lab",
    attachmentId: attachment.id
  });
  const state = await service.getState("pi-alpha");
  const persistedCompute = state.computeAllocations[0];
  const persistedStorage = state.storageVolumes[0];
  const persistedAttachment = state.storageAttachments[0];

  assert.equal(persistedCompute.provider, "tencent-tke");
  assert.equal(persistedCompute.providerResourceId, `node/node-${compute.id}`);
  assert.equal(persistedCompute.instanceId, `ins-${compute.id}`);
  assert.equal(persistedCompute.nodeName, `node-${compute.id}`);
  assert.equal(persistedCompute.privateIp, "10.0.0.21");
  assert.equal(persistedCompute.runtime.service, `service/opl-runtime-${compute.id}`);
  assert.equal(persistedStorage.providerResourceId, `pvc/${storage.id}`);
  assert.equal(persistedAttachment.providerAttachmentId, `mount/${compute.id}:${storage.id}:/data`);
  assert.equal(workspace.docker.service, `service/${persistedCompute.runtime.serviceName}`);
  assert.equal(workspace.disk.id, `pvc/${storage.id}`);
  assert.equal(workspace.url, `https://workspace.example.test/w/${workspace.slug}?token=${workspace.access.token}`);
});
