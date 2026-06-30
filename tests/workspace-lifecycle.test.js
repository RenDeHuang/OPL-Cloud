import assert from "node:assert/strict";
import test from "node:test";

import { createOplCloud } from "../services/api/src/opl-cloud.js";
import { MemoryStore } from "../services/api/src/store.js";
import { FakeRuntimeProvider } from "../services/api/src/runtime-providers/fake.js";

function createService() {
  return createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: new FakeRuntimeProvider(),
    pricing: {
      serverHourly: {
        basic: 1,
        pro: 4
      },
      diskGbMonth: 0.2,
      markup: 0.1
    }
  });
}

test("creates one workspace with one server, one Docker runtime, one disk, and one stable URL token", async () => {
  const service = createService();

  await service.creditAccount({
    accountId: "pi-alpha",
    amount: 200,
    reason: "owner_credit"
  });

  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Grant Lab",
    packageId: "basic"
  });

  assert.equal(workspace.ownerAccountId, "pi-alpha");
  assert.equal(workspace.packageId, "basic");
  assert.equal(workspace.state, "running");
  assert.match(workspace.server.id, /^server-/);
  assert.match(workspace.docker.id, /^docker-/);
  assert.match(workspace.disk.id, /^disk-/);
  assert.equal(workspace.disk.sizeGb, 10);
  assert.match(workspace.url, /^https:\/\/grant-lab-[a-z0-9]+\.oplcloud\.cn\/\?token=share_/);
  assert.equal(workspace.access.requiresLogin, false);
  assert.equal(workspace.access.tokenStatus, "active");

  const state = await service.getState("pi-alpha");
  assert.equal(state.workspaces.length, 1);
  assert.equal(state.workspaces[0].id, workspace.id);
});

test("stopping and destroying the server never destroys the cloud disk or URL", async () => {
  const service = createService();
  await service.creditAccount({ accountId: "pi-alpha", amount: 200, reason: "owner_credit" });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Disk Safe Lab",
    packageId: "basic"
  });

  const stopped = await service.stopServer({
    accountId: "pi-alpha",
    workspaceId: workspace.id,
    confirm: true
  });

  assert.equal(stopped.state, "stopped_server_disk_retained");
  assert.equal(stopped.server.status, "stopped");
  assert.equal(stopped.server.billingStatus, "stopped");
  assert.equal(stopped.disk.status, "attached_retained");
  assert.equal(stopped.disk.billingStatus, "active");
  assert.equal(stopped.url, workspace.url);

  const serverDestroyed = await service.destroyServer({
    accountId: "pi-alpha",
    workspaceId: workspace.id,
    confirm: true
  });

  assert.equal(serverDestroyed.state, "server_destroyed_disk_retained");
  assert.equal(serverDestroyed.server.status, "destroyed");
  assert.equal(serverDestroyed.disk.status, "detached_retained");
  assert.equal(serverDestroyed.disk.billingStatus, "active");
  assert.equal(serverDestroyed.url, workspace.url);

  await assert.rejects(
    service.destroyDisk({
      accountId: "pi-alpha",
      workspaceId: workspace.id,
      confirmDataLoss: false
    }),
    /disk_destroy_confirmation_required/
  );
});

test("destroying disk requires explicit confirmation and is the only action that stops storage billing", async () => {
  const service = createService();
  await service.creditAccount({ accountId: "pi-alpha", amount: 200, reason: "owner_credit" });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Archive Lab",
    packageId: "basic"
  });
  await service.destroyServer({ accountId: "pi-alpha", workspaceId: workspace.id, confirm: true });

  const destroyed = await service.destroyDisk({
    accountId: "pi-alpha",
    workspaceId: workspace.id,
    confirmDataLoss: true
  });

  assert.equal(destroyed.state, "destroyed");
  assert.equal(destroyed.disk.status, "destroyed");
  assert.equal(destroyed.disk.billingStatus, "stopped");

  const ledger = await service.billingLedger("pi-alpha");
  assert.ok(ledger.some((entry) => entry.type === "storage_destroyed"));
  assert.ok(ledger.some((entry) => entry.type === "server_destroyed"));
});

test("opening and restarting require a seven-day storage hold and preserve token until reset or delete", async () => {
  const service = createService();

  await service.creditAccount({ accountId: "pi-low", amount: 1, reason: "owner_credit" });
  await assert.rejects(
    service.createWorkspace({
      accountId: "pi-low",
      workspaceName: "No Hold Lab",
      packageId: "pro"
    }),
    /insufficient_storage_hold_balance/
  );

  await service.creditAccount({ accountId: "pi-alpha", amount: 200, reason: "owner_credit" });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Token Lab",
    packageId: "basic"
  });
  const originalUrl = workspace.url;
  const originalToken = workspace.access.token;

  await service.stopServer({ accountId: "pi-alpha", workspaceId: workspace.id, confirm: true });
  const restarted = await service.restartServer({
    accountId: "pi-alpha",
    workspaceId: workspace.id
  });

  assert.equal(restarted.state, "running");
  assert.equal(restarted.url, originalUrl);
  assert.equal(restarted.access.token, originalToken);

  const reset = await service.resetWorkspaceToken({
    accountId: "pi-alpha",
    workspaceId: workspace.id
  });
  assert.notEqual(reset.access.token, originalToken);
  assert.match(reset.url, /^https:\/\/token-lab-[a-z0-9]+\.oplcloud\.cn\/\?token=share_/);

  const deleted = await service.deleteWorkspaceToken({
    accountId: "pi-alpha",
    workspaceId: workspace.id
  });
  assert.equal(deleted.access.tokenStatus, "deleted");
});
