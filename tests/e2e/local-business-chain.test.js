import assert from "node:assert/strict";
import test from "node:test";

import { createOplCloud } from "../../packages/console/src/opl-cloud.js";
import { MemoryStore } from "../../packages/console/src/store.js";
import { createFakeRuntimeProvider } from "../helpers/fake-runtime-provider.js";

test("business chain keeps storage independent while dedicated compute is replaced", async () => {
  const service = createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: createFakeRuntimeProvider(),
    pricing: {
      serverHourly: { basic: 1, pro: 4 },
      diskGbMonth: 0.2,
      markup: 0.2
    }
  });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 500, reason: "e2e_credit" });

  const storage = await service.createStorageVolume({
    accountId: "pi-alpha",
    userId: "usr-alpha",
    packageId: "basic",
    sizeGb: 20,
    name: "Persistent research data"
  });
  assert.equal(storage.status, "available");
  assert.equal(storage.providerResourceId, `pvc/${storage.id}`);

  const firstCompute = await service.createComputeAllocation({
    accountId: "pi-alpha",
    userId: "usr-alpha",
    packageId: "basic",
    name: "Analysis node A"
  });
  await service.processPendingResourceProvisioning({ limit: 1 });
  const firstAttachment = await service.attachStorage({
    accountId: "pi-alpha",
    computeAllocationId: firstCompute.id,
    storageId: storage.id,
    mountPath: "/data"
  });
  const firstWorkspace = await service.createWorkspace({
    accountId: "pi-alpha",
    userId: "usr-alpha",
    workspaceName: "Persistent Lab A",
    attachmentId: firstAttachment.id
  });

  assert.equal(firstWorkspace.computeAllocationId, firstCompute.id);
  assert.equal(firstWorkspace.storageId, storage.id);
  assert.equal(firstWorkspace.url, `https://workspace.example.test/w/${firstWorkspace.slug}?token=${firstWorkspace.access.token}`);
  assert.equal(firstWorkspace.docker.service, `service/opl-runtime-${firstCompute.id}`);

  await service.detachStorage({
    accountId: "pi-alpha",
    attachmentId: firstAttachment.id,
    confirm: true
  });
  const suspendedWorkspace = await service.resolveWorkspaceAccess({
    slug: firstWorkspace.slug,
    token: firstWorkspace.access.token
  });
  assert.equal(suspendedWorkspace.id, firstWorkspace.id);
  assert.equal(suspendedWorkspace.state, "suspended");
  assert.equal(suspendedWorkspace.runtime.status, "suspended");
  assert.equal(suspendedWorkspace.currentComputeAllocationId, "");
  assert.equal(suspendedWorkspace.currentAttachmentId, "");
  assert.equal(suspendedWorkspace.url, firstWorkspace.url);

  const destroyed = await service.destroyComputeAllocation({
    accountId: "pi-alpha",
    computeAllocationId: firstCompute.id,
    confirm: true
  });
  assert.equal(destroyed.status, "destroyed");

  const secondCompute = await service.createComputeAllocation({
    accountId: "pi-alpha",
    userId: "usr-alpha",
    packageId: "basic",
    name: "Analysis node B"
  });
  await service.processPendingResourceProvisioning({ limit: 1 });
  const secondAttachment = await service.attachStorage({
    accountId: "pi-alpha",
    computeAllocationId: secondCompute.id,
    storageId: storage.id,
    mountPath: "/data"
  });
  const secondWorkspace = await service.createWorkspace({
    accountId: "pi-alpha",
    userId: "usr-alpha",
    workspaceName: "Persistent Lab A",
    attachmentId: secondAttachment.id
  });

  const state = await service.getState("pi-alpha");
  const persistedStorage = state.storageVolumes[0];

  assert.equal(secondWorkspace.computeAllocationId, secondCompute.id);
  assert.equal(secondWorkspace.storageId, storage.id);
  assert.equal(secondWorkspace.id, firstWorkspace.id);
  assert.equal(secondWorkspace.url, firstWorkspace.url);
  assert.equal(secondWorkspace.access.token, firstWorkspace.access.token);
  assert.notEqual(secondWorkspace.computeAllocationId, firstWorkspace.computeAllocationId);
  assert.equal(persistedStorage.providerResourceId, storage.providerResourceId);
  assert.equal(persistedStorage.status, "attached");
  assert.equal(state.computeAllocations.length, 2);
  assert.equal(state.computeAllocations.find((item) => item.id === firstCompute.id).status, "destroyed");
  assert.equal(state.computeAllocations.find((item) => item.id === secondCompute.id).status, "running");
  assert.deepEqual(state.storageVolumes.map((item) => item.id), [storage.id]);
  assert.equal(state.workspaces.length, 1);
  assert.equal(state.workspaces[0].computeAllocationId, secondCompute.id);
  assert.equal(state.workspaces[0].attachmentId, secondAttachment.id);
  assert.equal(state.billingLedger.some((entry) => entry.computeAllocationId === firstCompute.id), true);
  assert.equal(state.billingLedger.some((entry) => entry.computeAllocationId === secondCompute.id), true);
  assert.equal(state.billingLedger.some((entry) => entry.storageId === storage.id), true);
  assert.equal(state.billingLedger.some((entry) => entry.attachmentId === secondAttachment.id), true);
  assert.equal(state.billingLedger.some((entry) => entry.workspaceId === secondWorkspace.id), true);
  assert.deepEqual(
    state.runtimeOperations
      .filter((entry) => entry.workspaceId === "resource")
      .map((entry) => `${entry.resourceType}:${entry.operationType}:${entry.status}`),
    [
      "storage_volume:create_storage_volume:completed",
      "compute_allocation:create_compute_allocation:completed",
      "storage_attachment:attach_storage:completed",
      "compute_allocation:destroy_compute_allocation:completed",
      "compute_allocation:create_compute_allocation:completed",
      "storage_attachment:attach_storage:completed"
    ]
  );
});
