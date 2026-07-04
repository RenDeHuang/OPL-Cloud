import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { parse } from "yaml";

import { createOplCloud } from "../../packages/console/src/opl-cloud.js";
import { MemoryStore } from "../../packages/console/src/store.js";
import { LocalDockerProvider } from "../../packages/fabric/src/runtime-providers/local-docker.js";

async function exists(path) {
  try {
    await stat(path);
    return true;
  } catch {
    return false;
  }
}

test("local business chain keeps storage independent while compute is replaced", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-cloud-local-chain-"));
  try {
    const service = createOplCloud({
      store: new MemoryStore(),
      runtimeProvider: new LocalDockerProvider({
        rootDir: root,
        baseUrl: "http://127.0.0.1:8787",
        execute: false
      }),
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
    assert.equal(await exists(storage.localPath), true);

    const firstCompute = await service.createComputeAllocation({
      accountId: "pi-alpha",
      userId: "usr-alpha",
      packageId: "basic",
      name: "Analysis node A"
    });
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

    const persistedFile = join(storage.localPath, "projects", "analysis.txt");
    await writeFile(persistedFile, "result: first compute wrote this\n");

    assert.equal(firstWorkspace.computeAllocationId, firstCompute.id);
    assert.equal(firstWorkspace.storageId, storage.id);
    assert.equal(firstWorkspace.url, `http://127.0.0.1:8787/workspaces/${firstWorkspace.slug}?token=${firstWorkspace.access.token}`);
    assert.equal(await exists(firstWorkspace.docker.composePath), true);

    await service.detachStorage({
      accountId: "pi-alpha",
      attachmentId: firstAttachment.id,
      confirm: true
    });
    const destroyed = await service.destroyComputeAllocation({
      accountId: "pi-alpha",
      computeAllocationId: firstCompute.id,
      confirm: true
    });
    assert.equal(destroyed.status, "destroyed");
    assert.equal(await exists(storage.localPath), true);

    const secondCompute = await service.createComputeAllocation({
      accountId: "pi-alpha",
      userId: "usr-alpha",
      packageId: "basic",
      name: "Analysis node B"
    });
    const secondAttachment = await service.attachStorage({
      accountId: "pi-alpha",
      computeAllocationId: secondCompute.id,
      storageId: storage.id,
      mountPath: "/data"
    });
    const secondWorkspace = await service.createWorkspace({
      accountId: "pi-alpha",
      userId: "usr-alpha",
      workspaceName: "Persistent Lab B",
      attachmentId: secondAttachment.id
    });

    const compose = parse(await readFile(secondWorkspace.docker.composePath, "utf8"));
    const composeService = Object.values(compose.services)[0];
    assert.equal(secondWorkspace.computeAllocationId, secondCompute.id);
    assert.equal(secondWorkspace.storageId, storage.id);
    assert.notEqual(secondWorkspace.computeAllocationId, firstWorkspace.computeAllocationId);
    assert.equal(await readFile(persistedFile, "utf8"), "result: first compute wrote this\n");
    assert.deepEqual(composeService.volumes, [
      `${join(storage.localPath, "data")}:/data`,
      `${join(storage.localPath, "projects")}:/projects`
    ]);

    const state = await service.getState("pi-alpha");
    assert.equal(state.computeAllocations.length, 2);
    assert.equal(state.computeAllocations.find((item) => item.id === firstCompute.id).status, "destroyed");
    assert.equal(state.computeAllocations.find((item) => item.id === secondCompute.id).status, "running");
    assert.deepEqual(state.storageVolumes.map((item) => item.id), [storage.id]);
    assert.equal(state.storageVolumes[0].status, "attached");
    assert.equal(state.workspaces.length, 2);
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
        "compute_allocation:create_compute_allocation:completed",
        "storage_attachment:attach_storage:completed"
      ]
    );
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
