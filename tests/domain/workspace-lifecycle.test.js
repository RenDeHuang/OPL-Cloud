import assert from "node:assert/strict";
import test from "node:test";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { createOplCloud } from "../../packages/console/src/opl-cloud.js";
import { MemoryStore } from "../../packages/console/src/store.js";
import { LocalDockerProvider } from "../../packages/fabric/src/runtime-providers/local-docker.js";

const TEST_PRICING = {
  serverHourly: {
    basic: 1,
    pro: 4
  },
  diskGbMonth: 0.2,
  markup: 0.2
};

function runtimeFixture({ workspaceId, workspaceName, packagePlan, token, provider = "test-provider" }) {
  return {
    provider,
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
      status: "attached_retained",
      billingStatus: "active",
      sizeGb: packagePlan.diskGb,
      mountPath: "/data"
    },
    url: `http://127.0.0.1:8787/workspaces/${workspaceName}?token=${token}`,
    slug: workspaceName
  };
}

function createTestService(runtimeProvider) {
  return createOplCloud({
    store: new MemoryStore(),
    runtimeProvider,
    pricing: TEST_PRICING
  });
}

async function createService() {
  const root = await mkdtemp(join(tmpdir(), "opl-cloud-lifecycle-"));
  const service = createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: new LocalDockerProvider({
      rootDir: root,
      baseUrl: "http://127.0.0.1:8787",
      execute: false
    }),
    pricing: TEST_PRICING
  });
  return {
    service,
    cleanup: () => rm(root, { recursive: true, force: true })
  };
}

test("creates one workspace with one server, one Docker runtime, one disk, and one stable URL token", async () => {
  const { service, cleanup } = await createService();
  try {
    await service.creditAccount({
      accountId: "pi-alpha",
      amount: 250,
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
    assert.match(workspace.server.id, /^local-server-/);
    assert.match(workspace.docker.id, /^local-docker-/);
    assert.match(workspace.disk.id, /^local-disk-/);
    assert.equal(workspace.disk.sizeGb, 10);
    assert.match(workspace.url, /^http:\/\/127\.0\.0\.1:8787\/workspaces\/grant-lab-[a-z0-9]+/);
    assert.match(workspace.url, /\?token=share_/);
    assert.equal(workspace.access.requiresLogin, false);
    assert.equal(workspace.access.tokenStatus, "active");

    const state = await service.getState("pi-alpha");
    assert.equal(state.workspaces.length, 1);
    assert.equal(state.workspaces[0].id, workspace.id);
    assert.deepEqual(state.runtimeOperations.map((operation) => `${operation.operationType}:${operation.status}`), [
      "create_workspace:succeeded"
    ]);
  } finally {
    await cleanup();
  }
});

test("records failed runtime operations for retry and audit", async () => {
  const service = createTestService({
    name: "failing-provider",
    async createWorkspaceRuntime() {
      throw new Error("runtime_create_failed");
    }
  });

  await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  await assert.rejects(
    service.createWorkspace({
      accountId: "pi-alpha",
      workspaceName: "Failure Lab",
      packageId: "basic"
    }),
    /runtime_create_failed/
  );

  const state = await service.getState("pi-alpha");
  assert.equal(state.runtimeOperations.length, 1);
  assert.equal(state.runtimeOperations[0].operationType, "create_workspace");
  assert.equal(state.runtimeOperations[0].status, "failed");
  assert.equal(state.runtimeOperations[0].error, "runtime_create_failed");

  const summary = await service.operatorSummary({ accountId: "pi-alpha" });
  assert.equal(summary.product, "OPL Console");
  assert.equal(summary.notifications.error, 1);
  assert.equal(summary.notifications.recent[0].type, "workspace.create_failed");
  assert.equal(summary.runtimeOperations.failed, 1);
  assert.equal(summary.workspaces.total, 0);
  assert.equal(JSON.stringify(summary).includes("share_"), false);
});

test("records failed lifecycle runtime operations without mutating the Workspace", async () => {
  const scenarios = [
    {
      operationType: "stop_server",
      error: "runtime_stop_failed",
      invoke: (service, workspaceId) => service.stopServer({ accountId: "pi-alpha", workspaceId, confirm: true }),
      providerMethod: "stopServer"
    },
    {
      operationType: "restart_server",
      error: "runtime_restart_failed",
      invoke: async (service, workspaceId) => {
        await service.stopServer({ accountId: "pi-alpha", workspaceId, confirm: true });
        return service.restartServer({ accountId: "pi-alpha", workspaceId });
      },
      providerMethod: "restartServer"
    },
    {
      operationType: "destroy_server",
      error: "runtime_destroy_server_failed",
      invoke: (service, workspaceId) => service.destroyServer({ accountId: "pi-alpha", workspaceId, confirm: true }),
      providerMethod: "destroyServer"
    },
    {
      operationType: "destroy_disk",
      error: "runtime_destroy_disk_failed",
      invoke: (service, workspaceId) => service.destroyDisk({ accountId: "pi-alpha", workspaceId, confirmDataLoss: true }),
      providerMethod: "destroyDisk"
    }
  ];

  for (const scenario of scenarios) {
    const runtimeProvider = {
      name: "partial-failing-provider",
      async createWorkspaceRuntime(input) {
        return runtimeFixture({ ...input, provider: "partial-failing-provider" });
      },
      async stopServer({ workspace }) {
        if (scenario.providerMethod === "stopServer") throw new Error(scenario.error);
        return { ...workspace.server, status: "stopped", billingStatus: "stopped" };
      },
      async restartServer({ workspace }) {
        if (scenario.providerMethod === "restartServer") throw new Error(scenario.error);
        return { ...workspace.server, status: "running", billingStatus: "active" };
      },
      async destroyServer({ workspace }) {
        if (scenario.providerMethod === "destroyServer") throw new Error(scenario.error);
        return { ...workspace.server, status: "destroyed", billingStatus: "stopped" };
      },
      async destroyDisk({ workspace }) {
        if (scenario.providerMethod === "destroyDisk") throw new Error(scenario.error);
        return { ...workspace.disk, status: "destroyed", billingStatus: "stopped" };
      }
    };
    const service = createTestService(runtimeProvider);

    await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
    const workspace = await service.createWorkspace({
      accountId: "pi-alpha",
      workspaceName: `${scenario.operationType} Lab`,
      packageId: "basic"
    });

    await assert.rejects(
      scenario.invoke(service, workspace.id),
      new RegExp(scenario.error)
    );

    const state = await service.getState("pi-alpha");
    assert.notEqual(state.workspaces[0].state, "failed");
    assert.equal(state.runtimeOperations.at(-1).operationType, scenario.operationType);
    assert.equal(state.runtimeOperations.at(-1).status, "failed");
    assert.equal(state.runtimeOperations.at(-1).error, scenario.error);
  }
});

test("does not record runtime operations for lifecycle validation failures", async () => {
  const { service, cleanup } = await createService();
  try {
    await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
    const workspace = await service.createWorkspace({
      accountId: "pi-alpha",
      workspaceName: "Validation Lab",
      packageId: "basic"
    });

    await assert.rejects(
      service.stopServer({ accountId: "pi-alpha", workspaceId: workspace.id, confirm: false }),
      /server_stop_confirmation_required/
    );
    await assert.rejects(
      service.restartServer({ accountId: "pi-alpha", workspaceId: "ws-missing" }),
      /workspace_not_found/
    );

    const state = await service.getState("pi-alpha");
    assert.deepEqual(state.runtimeOperations.map((operation) => `${operation.operationType}:${operation.status}`), [
      "create_workspace:succeeded"
    ]);
  } finally {
    await cleanup();
  }
});

test("records separate runtime operation ids for rapid retries of the same action", async () => {
  const service = createTestService({
    name: "retry-failing-provider",
    async createWorkspaceRuntime(input) {
      return runtimeFixture({ ...input, provider: "retry-failing-provider" });
    },
    async stopServer() {
      throw new Error("runtime_stop_failed");
    }
  });
  const originalNow = Date.now;

  try {
    Date.now = () => 1777777777777;
    await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
    const workspace = await service.createWorkspace({
      accountId: "pi-alpha",
      workspaceName: "Retry Lab",
      packageId: "basic"
    });

    await assert.rejects(
      service.stopServer({ accountId: "pi-alpha", workspaceId: workspace.id, confirm: true }),
      /runtime_stop_failed/
    );
    await assert.rejects(
      service.stopServer({ accountId: "pi-alpha", workspaceId: workspace.id, confirm: true }),
      /runtime_stop_failed/
    );

    const failedOperations = (await service.getState("pi-alpha")).runtimeOperations.filter((operation) => operation.operationType === "stop_server");
    assert.equal(failedOperations.length, 2);
    assert.notEqual(failedOperations[0].id, failedOperations[1].id);
  } finally {
    Date.now = originalNow;
  }
});

test("stopping and destroying the server never destroys the cloud disk or URL", async () => {
  const { service, cleanup } = await createService();
  try {
    await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
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

    const state = await service.getState("pi-alpha");
    assert.deepEqual(state.runtimeOperations.map((operation) => `${operation.operationType}:${operation.status}`), [
      "create_workspace:succeeded",
      "stop_server:succeeded",
      "destroy_server:succeeded"
    ]);

    await assert.rejects(
      service.destroyDisk({
        accountId: "pi-alpha",
        workspaceId: workspace.id,
        confirmDataLoss: false
      }),
      /disk_destroy_confirmation_required/
    );
  } finally {
    await cleanup();
  }
});

test("destroying disk requires explicit confirmation and is the only action that stops storage billing", async () => {
  const { service, cleanup } = await createService();
  try {
    await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
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
    assert.equal(destroyed.access.tokenStatus, "unavailable");

    await assert.rejects(
      service.resetWorkspaceToken({
        accountId: "pi-alpha",
        workspaceId: workspace.id
      }),
      /workspace_storage_destroyed/
    );
    await assert.rejects(
      service.resolveWorkspaceAccess({
        slug: workspace.slug,
        token: workspace.access.token
      }),
      /workspace_token_inactive/
    );

    const ledger = await service.billingLedger("pi-alpha");
    assert.ok(ledger.some((entry) => entry.type === "storage_destroyed"));
    assert.ok(ledger.some((entry) => entry.type === "server_destroyed"));

    const state = await service.getState("pi-alpha");
    assert.ok(state.runtimeOperations.some((operation) => `${operation.operationType}:${operation.status}` === "destroy_disk:succeeded"));
  } finally {
    await cleanup();
  }
});

test("destroying disk from a running Workspace releases server compute before deleting storage", async () => {
  const calls = [];
  const service = createTestService({
    name: "ordered-destroy-provider",
    async createWorkspaceRuntime(input) {
      return runtimeFixture({ ...input, provider: "ordered-destroy-provider" });
    },
    async destroyServer({ workspace }) {
      calls.push(`destroy-server:${workspace.server.status}:${workspace.disk.status}`);
      return { ...workspace.server, status: "destroyed", billingStatus: "stopped" };
    },
    async destroyDisk({ workspace }) {
      calls.push(`destroy-disk:${workspace.server.status}:${workspace.disk.status}`);
      return { ...workspace.disk, status: "destroyed", billingStatus: "stopped" };
    }
  });

  await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Immediate Archive Lab",
    packageId: "basic"
  });

  const destroyed = await service.destroyDisk({
    accountId: "pi-alpha",
    workspaceId: workspace.id,
    confirmDataLoss: true
  });

  assert.deepEqual(calls, [
    "destroy-server:running:attached_retained",
    "destroy-disk:destroyed:detached_retained"
  ]);
  assert.equal(destroyed.state, "destroyed");
  assert.equal(destroyed.server.status, "destroyed");
  assert.equal(destroyed.server.billingStatus, "stopped");
  assert.equal(destroyed.disk.status, "destroyed");
  assert.equal(destroyed.disk.billingStatus, "stopped");
  assert.equal(destroyed.access.tokenStatus, "unavailable");
  assert.deepEqual((await service.getState("pi-alpha")).runtimeOperations.map((operation) => `${operation.operationType}:${operation.status}`), [
    "create_workspace:succeeded",
    "destroy_disk:succeeded"
  ]);
});

test("restarting a server-destroyed Workspace recreates compute from the retained disk and preserves URL", async () => {
  const calls = [];
  const service = createTestService({
    name: "recreate-provider",
    async createWorkspaceRuntime(input) {
      return runtimeFixture({ ...input, provider: "recreate-provider" });
    },
    async destroyServer({ workspace }) {
      return { ...workspace.server, status: "destroyed", billingStatus: "stopped" };
    },
    async restartServer() {
      throw new Error("restart_should_not_start_destroyed_server");
    },
    async recreateServer({ workspace }) {
      calls.push(`recreate:${workspace.id}:${workspace.disk.id}`);
      return {
        ...workspace.server,
        id: `server-recreated-${workspace.id}`,
        status: "running",
        billingStatus: "active"
      };
    }
  });

  await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Recreate Lab",
    packageId: "basic"
  });
  const destroyed = await service.destroyServer({ accountId: "pi-alpha", workspaceId: workspace.id, confirm: true });

  const recreated = await service.restartServer({ accountId: "pi-alpha", workspaceId: workspace.id });

  assert.equal(destroyed.state, "server_destroyed_disk_retained");
  assert.equal(recreated.state, "running");
  assert.equal(recreated.server.id, `server-recreated-${workspace.id}`);
  assert.equal(recreated.disk.id, workspace.disk.id);
  assert.equal(recreated.disk.status, "attached_retained");
  assert.equal(recreated.docker.status, "running");
  assert.equal(recreated.url, workspace.url);
  assert.equal(recreated.access.token, workspace.access.token);
  assert.deepEqual(calls, [`recreate:${workspace.id}:${workspace.disk.id}`]);
  assert.ok((await service.getState("pi-alpha")).runtimeOperations.some((operation) => `${operation.operationType}:${operation.status}` === "recreate_server:succeeded"));
});

test("opening and restarting require a seven-day storage hold and preserve token until reset or delete", async () => {
  const { service, cleanup } = await createService();
  try {
    await service.creditAccount({ accountId: "pi-low", amount: 1, reason: "owner_credit" });
    await assert.rejects(
      service.createWorkspace({
        accountId: "pi-low",
        workspaceName: "No Hold Lab",
        packageId: "pro"
      }),
      /insufficient_prepaid_hold_balance/
    );

    await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
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
    assert.ok((await service.getState("pi-alpha")).runtimeOperations.some((operation) => `${operation.operationType}:${operation.status}` === "restart_server:succeeded"));

    const reset = await service.resetWorkspaceToken({
      accountId: "pi-alpha",
      workspaceId: workspace.id
    });
    assert.notEqual(reset.access.token, originalToken);
    assert.match(reset.url, /^http:\/\/127\.0\.0\.1:8787\/workspaces\/token-lab-[a-z0-9]+/);
    assert.match(reset.url, /\?token=share_/);

    const deleted = await service.deleteWorkspaceToken({
      accountId: "pi-alpha",
      workspaceId: workspace.id
    });
    assert.equal(deleted.access.tokenStatus, "deleted");
  } finally {
    await cleanup();
  }
});

test("hourly billing settlement debits server only while running and storage until disk destroy", async () => {
  const { service, cleanup } = await createService();
  try {
    await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
    const workspace = await service.createWorkspace({
      accountId: "pi-alpha",
      workspaceName: "Billing Lab",
      packageId: "basic"
    });

    const first = await service.settleBilling({
      accountId: "pi-alpha",
      workspaceId: workspace.id,
      hours: 2,
      sourceEventId: "billing_tick_1"
    });
    assert.equal(first.entries.length, 2);
    assert.equal(first.entries.find((entry) => entry.type === "compute_debit").amount, -2.4);
    assert.equal(first.entries.find((entry) => entry.type === "storage_debit").amount, -0.0067);

    await service.stopServer({ accountId: "pi-alpha", workspaceId: workspace.id, confirm: true });
    const second = await service.settleBilling({
      accountId: "pi-alpha",
      workspaceId: workspace.id,
      hours: 2,
      sourceEventId: "billing_tick_2"
    });
    assert.deepEqual(second.entries.map((entry) => entry.type), ["storage_debit"]);
    assert.equal(second.entries[0].amount, -0.0067);

    await service.destroyDisk({ accountId: "pi-alpha", workspaceId: workspace.id, confirmDataLoss: true });
    const third = await service.settleBilling({
      accountId: "pi-alpha",
      workspaceId: workspace.id,
      hours: 2,
      sourceEventId: "billing_tick_3"
    });
    assert.deepEqual(third.entries, []);
  } finally {
    await cleanup();
  }
});
