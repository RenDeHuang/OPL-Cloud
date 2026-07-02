import assert from "node:assert/strict";
import test from "node:test";

import { createOplCloud, packageHoldAmount } from "../../packages/console/src/opl-cloud.js";
import { MemoryStore } from "../../packages/console/src/store.js";

const TEST_PRICING = {
  computeHourly: {
    basic: 1,
    pro: 4
  },
  storageGbMonth: 0.2,
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
    url: `https://workspace.example.com/w/${workspaceId}?token=${token}`,
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

test("packages expose only production-ready CPU choices with 20 percent Tencent markup price snapshots", async () => {
  const service = createTestService({
    name: "packages-only"
  });

  assert.deepEqual(service.packages().map((plan) => ({
    id: plan.id,
    accelerator: plan.accelerator,
    cpu: plan.cpu,
    memoryGb: plan.memoryGb,
    gpu: plan.gpu,
    computeHourly: plan.price.computeHourly,
    storageGbMonth: plan.price.storageGbMonth,
    markup: plan.price.markup
  })), [
    {
      id: "basic",
      accelerator: "cpu",
      cpu: 2,
      memoryGb: 4,
      gpu: 0,
      computeHourly: 1.2,
      storageGbMonth: 0.24,
      markup: 0.2
    },
    {
      id: "pro",
      accelerator: "cpu",
      cpu: 8,
      memoryGb: 16,
      gpu: 0,
      computeHourly: 4.8,
      storageGbMonth: 0.24,
      markup: 0.2
    }
  ]);
});

test("opening a Workspace freezes seven days of compute and storage and charges the first hour from available balance", async () => {
  const service = createTestService({
    name: "billing-provider",
    async createWorkspaceRuntime(input) {
      return runtimeFixture(input);
    }
  });

  await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Prepaid Lab",
    packageId: "basic"
  });

  const state = await service.getState("pi-alpha");
  assert.equal(state.account.balance, 248.7967);
  assert.equal(state.account.frozen, 202.16);
  assert.deepEqual(workspace.billing, {
    holdPolicy: "seven_day_prepaid",
    minimumBillableHours: 1,
    priceMarkup: 0.2
  });
  assert.deepEqual(state.billingLedger.map((entry) => ({
    type: entry.type,
    amount: entry.amount,
    holdType: entry.holdType,
    sourceEventId: entry.sourceEventId
  })), [
    { type: "credit", amount: 250, holdType: undefined, sourceEventId: "owner_credit" },
    { type: "compute_hold", amount: 201.6, holdType: "compute", sourceEventId: "open_workspace" },
    { type: "storage_hold", amount: 0.56, holdType: "storage", sourceEventId: "open_workspace" },
    { type: "compute_debit", amount: -1.2, holdType: "compute", sourceEventId: "open_workspace_initial_hour" },
    { type: "storage_debit", amount: -0.0033, holdType: "storage", sourceEventId: "open_workspace_initial_hour" }
  ]);
});

test("Workspace creation failure releases holds and records an operator-visible notification", async () => {
  const service = createTestService({
    name: "failing-provider",
    async createWorkspaceRuntime() {
      throw new Error("image_pull_failed");
    }
  });

  await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  await assert.rejects(
    service.createWorkspace({
      accountId: "pi-alpha",
      workspaceName: "Broken Lab",
      packageId: "basic"
    }),
    /image_pull_failed/
  );

  const state = await service.getState("pi-alpha");
  assert.equal(state.account.balance, 250);
  assert.equal(state.account.frozen, 0);
  assert.equal(state.workspaces.length, 0);
  assert.deepEqual(state.billingLedger.map((entry) => entry.type), [
    "credit",
    "compute_hold",
    "storage_hold",
    "compute_hold_released",
    "storage_hold_released"
  ]);
  assert.deepEqual(state.notifications.map((event) => ({
    type: event.type,
    severity: event.severity,
    message: event.message
  })), [
    {
      type: "workspace.create_failed",
      severity: "error",
      message: "image_pull_failed"
    }
  ]);
});

test("billing settlement rounds up to full hours, consumes available balance first, and auto-stops compute when compute hold is exhausted", async () => {
  const stopCalls = [];
  const service = createTestService({
    name: "auto-stop-provider",
    async createWorkspaceRuntime(input) {
      return runtimeFixture(input);
    },
    async stopServer({ workspace }) {
      stopCalls.push(workspace.id);
      return { ...workspace.server, status: "stopped", billingStatus: "stopped" };
    }
  });

  await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Auto Stop Lab",
    packageId: "basic"
  });

  const settlement = await service.settleBilling({
    accountId: "pi-alpha",
    workspaceId: workspace.id,
    hours: 210,
    sourceEventId: "billing_tick_hold_exhausted"
  });

  assert.deepEqual(settlement.entries.map((entry) => ({
    type: entry.type,
    amount: entry.amount,
    billableHours: entry.billableHours,
    holdType: entry.holdType,
    fundingSource: entry.metadata?.fundingSource
  })), [
    { type: "compute_debit", amount: -46.6367, billableHours: 210, holdType: "compute", fundingSource: "available_balance" },
    { type: "compute_debit", amount: -201.6, billableHours: 210, holdType: "compute", fundingSource: "compute_hold" },
    { type: "storage_debit", amount: -0.56, billableHours: 210, holdType: "storage", fundingSource: "storage_hold" },
    { type: "compute_auto_stopped", amount: 0, billableHours: undefined, holdType: "compute", fundingSource: undefined }
  ]);
  assert.deepEqual(stopCalls, [workspace.id]);

  const state = await service.getState("pi-alpha");
  assert.equal(state.account.balance, 0);
  assert.equal(state.account.frozen, 0);
  assert.equal(state.workspaces[0].server.status, "stopped");
  assert.equal(state.workspaces[0].disk.billingStatus, "hold_exhausted");
  assert.equal(state.workspaces[0].state, "stopped_storage_hold_exhausted");
  assert.deepEqual(state.notifications.map((event) => event.type), [
    "account.available_balance_exhausted",
    "workspace.storage_hold_exhausted",
    "workspace.compute_auto_stopped"
  ]);
});

test("prepaid billing uses available balance first and never debits beyond available plus frozen hold pools", async () => {
  const service = createTestService({
    name: "bounded-debit-provider",
    async createWorkspaceRuntime(input) {
      return runtimeFixture(input);
    },
    async stopServer({ workspace }) {
      return { ...workspace.server, status: "stopped", billingStatus: "stopped" };
    }
  });

  await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Bounded Debit Lab",
    packageId: "basic"
  });

  await service.settleBilling({
    accountId: "pi-alpha",
    workspaceId: workspace.id,
    hours: 1000,
    sourceEventId: "billing_tick_far_past_hold"
  });

  const state = await service.getState("pi-alpha");
  const totalDebited = state.billingLedger
    .filter((entry) => entry.type === "compute_debit" || entry.type === "storage_debit")
    .reduce((sum, entry) => Number((sum + Math.abs(entry.amount)).toFixed(4)), 0);

  assert.equal(totalDebited, 250);
  assert.equal(state.account.balance, 0);
  assert.equal(state.account.frozen, 0);
  assert.equal(state.account.balance >= 0, true);
});

test("prepaid billing warns when available balance is exhausted before consuming frozen holds", async () => {
  const service = createTestService({
    name: "low-balance-provider",
    async createWorkspaceRuntime(input) {
      return runtimeFixture(input);
    }
  });

  await service.creditAccount({ accountId: "pi-alpha", amount: 204, reason: "owner_credit" });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Low Balance Lab",
    packageId: "basic"
  });

  await service.settleBilling({
    accountId: "pi-alpha",
    workspaceId: workspace.id,
    hours: 2,
    sourceEventId: "billing_tick_available_exhausted"
  });

  const state = await service.getState("pi-alpha");
  assert.equal(state.account.balance, 200.39);
  assert.equal(state.account.frozen, 200.39);
  assert.deepEqual(state.notifications.map((event) => ({
    type: event.type,
    severity: event.severity,
    sourceEventId: event.sourceEventId
  })), [
    {
      type: "account.available_balance_exhausted",
      severity: "warning",
      sourceEventId: "billing_tick_available_exhausted"
    }
  ]);
});

test("billing settlement is idempotent for the same source event", async () => {
  const service = createTestService({
    name: "idempotent-billing-provider",
    async createWorkspaceRuntime(input) {
      return runtimeFixture(input);
    }
  });

  await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Idempotent Billing Lab",
    packageId: "basic"
  });

  await service.settleBilling({
    accountId: "pi-alpha",
    workspaceId: workspace.id,
    hours: 2,
    sourceEventId: "billing_tick_retry_safe"
  });
  const afterFirst = await service.getState("pi-alpha");

  const retry = await service.settleBilling({
    accountId: "pi-alpha",
    workspaceId: workspace.id,
    hours: 2,
    sourceEventId: "billing_tick_retry_safe"
  });
  const afterRetry = await service.getState("pi-alpha");

  assert.deepEqual(retry.entries.map((entry) => entry.type), ["compute_debit", "storage_debit"]);
  assert.equal(afterRetry.account.balance, afterFirst.account.balance);
  assert.equal(afterRetry.account.frozen, afterFirst.account.frozen);
  assert.equal(
    afterRetry.billingLedger.filter((entry) => entry.sourceEventId === "billing_tick_retry_safe").length,
    2
  );
});

test("destroying compute and storage releases unused prepaid holds", async () => {
  const service = createTestService({
    name: "destroy-provider",
    async createWorkspaceRuntime(input) {
      return runtimeFixture(input);
    },
    async destroyServer({ workspace }) {
      return { ...workspace.server, status: "destroyed", billingStatus: "stopped" };
    },
    async destroyDisk({ workspace }) {
      return { ...workspace.disk, status: "destroyed", billingStatus: "stopped" };
    }
  });

  await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Release Lab",
    packageId: "basic"
  });
  await service.destroyDisk({ accountId: "pi-alpha", workspaceId: workspace.id, confirmDataLoss: true });

  const state = await service.getState("pi-alpha");
  assert.equal(state.account.frozen, 0);
  assert.equal(state.billingLedger.filter((entry) => entry.type === "compute_hold_released").at(-1).amount, -201.6);
  assert.equal(state.billingLedger.filter((entry) => entry.type === "storage_hold_released").at(-1).amount, -0.56);
});

test("hold calculation uses seven days of Tencent cost plus 20 percent markup", () => {
  const hold = packageHoldAmount({
    packagePlan: {
      id: "pro",
      diskGb: 100
    },
    pricing: TEST_PRICING
  });

  assert.deepEqual(hold, {
    compute: 806.4,
    storage: 5.6,
    total: 812
  });
});
