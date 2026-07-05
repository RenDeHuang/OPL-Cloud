import assert from "node:assert/strict";
import test from "node:test";

import { createOplCloud } from "../../packages/console/src/opl-cloud.js";
import { MemoryStore } from "../../packages/console/src/store.js";
import { createFakeRuntimeProvider } from "../helpers/fake-runtime-provider.js";

const TEST_PRICING = {
  computeHourly: { basic: 1, pro: 4 },
  storageGbMonth: 0.2,
  markup: 0.2
};

function createTestService() {
  return createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: createFakeRuntimeProvider({
      name: "test-provider",
      workspaceUrl({ slug, token }) {
        return `https://workspace.example.com/w/${slug}?token=${token}`;
      }
    }),
    pricing: TEST_PRICING
  });
}

async function createWorkspaceEntry(service, { accountId, workspaceName, packageId = "basic" }) {
  const storage = await service.createStorageVolume({ accountId, packageId, name: `${workspaceName} storage` });
  const compute = await service.createComputeAllocation({ accountId, packageId, name: `${workspaceName} compute` });
  await service.processPendingResourceProvisioning({ limit: 1 });
  const attachment = await service.attachStorage({
    accountId,
    computeAllocationId: compute.id,
    storageId: storage.id,
    mountPath: "/data"
  });
  return service.createWorkspace({ accountId, workspaceName, attachmentId: attachment.id });
}

test("Workspace access uses a long-lived URL token that can be deleted and reset after leakage", async () => {
  const service = createTestService();
  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await createWorkspaceEntry(service, {
    accountId: "pi-alpha",
    workspaceName: "Token Lab"
  });

  assert.equal(workspace.access.mode, "long_lived_url_token");
  assert.equal(workspace.access.requiresLogin, false);
  assert.equal(workspace.access.tokenStatus, "active");
  assert.equal(workspace.access.rotationPolicy, "reset_or_delete_on_leak");

  const resolved = await service.resolveWorkspaceAccess({
    slug: workspace.slug,
    token: workspace.access.token
  });
  assert.equal(resolved.id, workspace.id);

  const deleted = await service.deleteWorkspaceToken({
    accountId: "pi-alpha",
    workspaceId: workspace.id
  });
  assert.equal(deleted.access.tokenStatus, "deleted");
  await assert.rejects(
    service.resolveWorkspaceAccess({ slug: workspace.slug, token: workspace.access.token }),
    /workspace_token_inactive/
  );

  const reset = await service.resetWorkspaceToken({
    accountId: "pi-alpha",
    workspaceId: workspace.id
  });
  assert.equal(reset.access.tokenStatus, "active");
  assert.notEqual(reset.access.token, workspace.access.token);
  assert.match(reset.url, new RegExp(`token=${reset.access.token}$`));

  const resolvedAfterReset = await service.resolveWorkspaceAccess({
    slug: reset.slug,
    token: reset.access.token
  });
  assert.equal(resolvedAfterReset.id, workspace.id);

  const state = await service.getState("pi-alpha");
  assert.deepEqual(state.billingLedger.map((entry) => entry.type).filter((type) => type.startsWith("token_")), [
    "token_deleted",
    "token_reset"
  ]);
});

test("operator cleanup keeps suspended compute URLs active and storage destroy makes Workspace unavailable", async () => {
  const service = createTestService();
  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await createWorkspaceEntry(service, {
    accountId: "pi-alpha",
    workspaceName: "Cleanup Lab"
  });

  await service.destroyComputeAllocation({
    accountId: "pi-alpha",
    computeAllocationId: workspace.computeAllocationId,
    confirm: true
  });

  const suspended = await service.getState("pi-alpha");
  assert.equal(suspended.workspaces.find((item) => item.id === workspace.id).access.tokenStatus, "active");
  assert.equal(suspended.workspaces.find((item) => item.id === workspace.id).state, "suspended");

  const cleanupWhileSuspended = await service.cleanupWorkspaceAccess({
    accountId: "pi-alpha",
    reason: "operator_cleanup"
  });
  assert.deepEqual(cleanupWhileSuspended.cleaned, []);

  await service.destroyStorageVolume({
    accountId: "pi-alpha",
    storageId: workspace.storageId,
    confirmDataLoss: true
  });

  const before = await service.getState("pi-alpha");
  assert.equal(before.workspaces.find((item) => item.id === workspace.id).access.tokenStatus, "unavailable");
  assert.equal(before.workspaces.find((item) => item.id === workspace.id).state, "destroyed");

  const cleanup = await service.cleanupWorkspaceAccess({
    accountId: "pi-alpha",
    reason: "operator_cleanup"
  });
  assert.deepEqual(cleanup.cleaned, []);
  assert.equal(cleanup.skipped[0].reason, "token_not_active");
  assert.equal(cleanup.activeResources.compute.length, 0);
  assert.equal(cleanup.activeResources.storage.length, 0);
  assert.equal(cleanup.activeResources.attachments.length, 0);

  await assert.rejects(
    service.resolveWorkspaceAccess({ slug: workspace.slug, token: workspace.access.token }),
    /workspace_token_inactive/
  );

  const after = await service.getState("pi-alpha");
  assert.equal(after.workspaces.find((item) => item.id === workspace.id).access.tokenStatus, "unavailable");
  assert.equal(after.workspaces.find((item) => item.id === workspace.id).state, "destroyed");
});

test("operator cleanup retires active legacy Workspace entries without stable storage identity", async () => {
  const service = createTestService();
  await service.store.update((state) => {
    state.workspaces["ws-legacy"] = {
      id: "ws-legacy",
      ownerAccountId: "pi-alpha",
      packageId: "basic",
      name: "Legacy URL",
      slug: "legacy-url",
      url: "https://workspace.example.test/w/legacy-url/?token=share_legacy",
      access: {
        token: "share_legacy",
        tokenStatus: "active"
      },
      state: "running",
      storageVolumeId: "storage-old",
      currentComputeAllocationId: "",
      currentAttachmentId: "",
      createdAt: "2026-07-01T00:00:00.000Z",
      updatedAt: "2026-07-01T00:00:00.000Z"
    };
  });

  const cleanup = await service.cleanupWorkspaceAccess({
    accountId: "pi-alpha",
    reason: "legacy_shape_retired"
  });

  assert.deepEqual(cleanup.cleaned, [{
    workspaceId: "ws-legacy",
    accountId: "pi-alpha",
    tokenStatus: "unavailable",
    unavailableBecause: ["stable_storage_identity_missing"]
  }]);

  await assert.rejects(
    service.resolveWorkspaceAccess({ slug: "legacy-url", token: "share_legacy" }),
    /workspace_token_inactive/
  );
});

test("operator cleanup can stop legacy compute records after cloud machine cleanup is confirmed", async () => {
  const service = createTestService();
  await service.store.update((state) => {
    state.computeAllocations = [{
      id: "compute-legacy",
      ownerAccountId: "pi-alpha",
      ownerUserId: "usr-alpha",
      packageId: "basic",
      poolId: "pool-basic-2c4g",
      nodePoolId: "np-basic",
      status: "destroying",
      billingStatus: "active",
      providerResourceId: "node/np-basic-old",
      nodeName: "np-basic-old",
      machineName: "",
      attachedStorageIds: [],
      createdAt: "2026-07-01T00:00:00.000Z",
      updatedAt: "2026-07-01T00:05:00.000Z"
    }];
    state.storageVolumes = [{
      id: "storage-legacy",
      ownerAccountId: "pi-alpha",
      ownerUserId: "usr-alpha",
      packageId: "basic",
      status: "available",
      billingStatus: "active",
      attachmentIds: [],
      createdAt: "2026-07-01T00:00:00.000Z",
      updatedAt: "2026-07-01T00:00:00.000Z"
    }];
    state.storageAttachments = [{
      id: "attach-legacy",
      ownerAccountId: "pi-alpha",
      computeAllocationId: "compute-legacy",
      storageId: "storage-legacy",
      status: "detached",
      createdAt: "2026-07-01T00:00:00.000Z",
      updatedAt: "2026-07-01T00:10:00.000Z"
    }];
    state.workspaces["ws-legacy-compute"] = {
      id: "ws-legacy-compute",
      ownerAccountId: "pi-alpha",
      packageId: "basic",
      name: "Legacy Compute URL",
      slug: "legacy-compute-url",
      url: "https://workspace.example.test/w/legacy-compute-url/?token=share_legacy_compute",
      access: {
        token: "share_legacy_compute",
        tokenStatus: "active"
      },
      state: "running",
      storageId: "storage-legacy",
      currentComputeAllocationId: "compute-legacy",
      currentAttachmentId: "attach-legacy",
      runtimeStatus: "running",
      createdAt: "2026-07-01T00:00:00.000Z",
      updatedAt: "2026-07-01T00:10:00.000Z"
    };
  });

  const withoutConfirmation = await service.cleanupWorkspaceAccess({
    accountId: "pi-alpha",
    legacyComputeAllocationIds: ["compute-legacy"],
    reason: "legacy_compute_cleanup"
  });
  assert.deepEqual(withoutConfirmation.legacyComputeCleaned, []);
  assert.equal(withoutConfirmation.legacyComputeSkipped[0].reason, "cloud_cleanup_not_confirmed");

  const cleanup = await service.cleanupWorkspaceAccess({
    accountId: "pi-alpha",
    legacyComputeAllocationIds: ["compute-legacy"],
    cloudCleanupConfirmed: true,
    reason: "legacy_compute_cleanup"
  });

  assert.deepEqual(cleanup.legacyComputeCleaned, [{
    computeAllocationId: "compute-legacy",
    accountId: "pi-alpha",
    status: "destroyed",
    billingStatus: "stopped"
  }]);

  const state = await service.getState("pi-alpha");
  const compute = state.computeAllocations.find((item) => item.id === "compute-legacy");
  const workspace = state.workspaces.find((item) => item.id === "ws-legacy-compute");
  assert.equal(compute.status, "destroyed");
  assert.equal(compute.billingStatus, "stopped");
  assert.equal(workspace.state, "suspended");
  assert.equal(workspace.access.tokenStatus, "active");
  assert.equal(workspace.currentComputeAllocationId, "");
  assert.ok(state.billingLedger.some((entry) => entry.type === "compute_legacy_cleaned"));
});
