import assert from "node:assert/strict";
import test from "node:test";

import { createOplCloud } from "../../packages/console/src/opl-cloud.js";
import { MemoryStore } from "../../packages/console/src/store.js";

const TEST_PRICING = {
  serverHourly: {
    basic: 1,
    pro: 4
  },
  diskGbMonth: 0.2,
  markup: 0.2
};

function runtimeFixture({ workspaceId, workspaceName, packagePlan, token, restoredFromBackupId = null }) {
  return {
    provider: "test-provider",
    server: {
      id: `server-${workspaceId}`,
      status: "running",
      billingStatus: "active",
      spec: packagePlan.server
    },
    docker: {
      id: `docker-${workspaceId}`,
      image: "test-image",
      status: "running"
    },
    disk: {
      id: `disk-${workspaceId}`,
      status: restoredFromBackupId ? "restored_retained" : "attached_retained",
      billingStatus: "active",
      sizeGb: packagePlan.diskGb,
      mountPath: "/data",
      ...(restoredFromBackupId ? { restoredFromBackupId } : {})
    },
    url: `http://127.0.0.1:8787/workspaces/${workspaceName}?token=${token}`,
    slug: workspaceName
  };
}

function createService({ backupFailures = new Set() } = {}) {
  const providerCalls = [];
  const runtimeProvider = {
    name: "test-provider",
    async createWorkspaceRuntime(input) {
      providerCalls.push(["createWorkspaceRuntime", input]);
      return runtimeFixture({
        ...input,
        restoredFromBackupId: input.restoreFromBackup?.id || null
      });
    },
    async createStorageBackup({ workspace, backupId, retentionPolicy }) {
      providerCalls.push(["createStorageBackup", { workspace, backupId, retentionPolicy }]);
      if (backupFailures.has("create")) throw new Error("snapshot_create_failed");
      return {
        id: backupId,
        provider: "test-provider",
        status: "available",
        workspaceId: workspace.id,
        sourcePvc: workspace.disk.id,
        snapshotName: backupId,
        restoreSize: `${workspace.disk.sizeGb}Gi`,
        retentionPolicy
      };
    },
    async deleteStorageBackup({ backup }) {
      providerCalls.push(["deleteStorageBackup", { backup }]);
      if (backupFailures.has("delete")) throw new Error("snapshot_delete_failed");
      return { ...backup, status: "deleted" };
    },
    workspaceUrl({ workspaceId, token }) {
      return `http://127.0.0.1:8787/workspaces/${workspaceId}?token=${token}`;
    }
  };
  return {
    providerCalls,
    service: createOplCloud({
      store: new MemoryStore(),
      runtimeProvider,
      pricing: TEST_PRICING
    })
  };
}

test("creates a retained storage backup and restores it into a new billable Workspace", async () => {
  const { service, providerCalls } = createService();
  await service.creditAccount({ accountId: "pi-alpha", amount: 500, reason: "owner_credit" });
  const source = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Source Lab",
    packageId: "basic"
  });

  const backup = await service.createStorageBackup({
    accountId: "pi-alpha",
    workspaceId: source.id,
    reason: "manual"
  });
  const restored = await service.restoreWorkspaceFromBackup({
    accountId: "pi-alpha",
    backupId: backup.id,
    workspaceName: "Restored Lab",
    packageId: "basic"
  });
  const state = await service.getState("pi-alpha");

  assert.equal(backup.status, "available");
  assert.equal(backup.workspaceId, source.id);
  assert.deepEqual(backup.retentionPolicy, {
    name: "daily_7_weekly_4",
    retainDaily: 7,
    retainWeekly: 4,
    retainLast: 11
  });
  assert.equal(restored.restoredFromBackupId, backup.id);
  assert.equal(restored.disk.restoredFromBackupId, backup.id);
  assert.equal(restored.disk.status, "restored_retained");
  assert.equal(state.storageBackups.length, 1);
  assert.equal(state.workspaces.length, 2);
  assert.deepEqual(
    state.evidenceLedger.map((entry) => entry.type).filter((type) => type.includes("storage")),
    ["workspace.storage_backup_created", "workspace.storage_restored"]
  );
  assert.deepEqual(providerCalls.map(([name]) => name), [
    "createWorkspaceRuntime",
    "createStorageBackup",
    "createWorkspaceRuntime"
  ]);
  assert.equal(providerCalls[2][1].restoreFromBackup.id, backup.id);
  const storageEvidence = state.evidenceLedger.filter((entry) => entry.type.startsWith("workspace.storage_"));
  assert.equal(JSON.stringify(state.storageBackups).includes(source.access.token), false);
  assert.equal(JSON.stringify(storageEvidence).includes(source.access.token), false);
});

test("prunes retained storage backups beyond the retention window and records failures", async () => {
  const { service, providerCalls } = createService();
  await service.creditAccount({ accountId: "pi-alpha", amount: 500, reason: "owner_credit" });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Retention Lab",
    packageId: "basic"
  });

  const backups = [];
  for (let index = 0; index < 3; index += 1) {
    backups.push(await service.createStorageBackup({
      accountId: "pi-alpha",
      workspaceId: workspace.id,
      reason: `manual-${index}`,
      retentionPolicy: { name: "test-retain-two", retainLast: 2 }
    }));
  }

  const result = await service.pruneStorageBackups({
    accountId: "pi-alpha",
    workspaceId: workspace.id
  });
  const state = await service.getState("pi-alpha");

  assert.deepEqual(result.deletedBackupIds, [backups[0].id]);
  assert.deepEqual(
    state.storageBackups.map((backup) => `${backup.id}:${backup.status}`),
    [
      `${backups[0].id}:deleted`,
      `${backups[1].id}:available`,
      `${backups[2].id}:available`
    ]
  );
  assert.deepEqual(providerCalls.map(([name]) => name), [
    "createWorkspaceRuntime",
    "createStorageBackup",
    "createStorageBackup",
    "createStorageBackup",
    "deleteStorageBackup"
  ]);
});
