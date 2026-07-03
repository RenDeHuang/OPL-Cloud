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

test("packages expose only production-ready CPU choices from the pricing catalog", async () => {
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

test("manual top-up writes wallet transaction and top-up audit records", async () => {
  const service = createTestService({
    name: "manual-topup-provider"
  });

  const account = await service.manualTopUp({
    accountId: "pi-alpha",
    amount: 250,
    reason: "owner_credit",
    operatorUserId: "usr-admin",
    operatorAccountId: "admin"
  });

  const persisted = await service.store.read();
  assert.equal(account.balance, 250);
  assert.equal(persisted.manualTopups.length, 1);
  assert.equal(persisted.walletTransactions.length, 1);
  assert.deepEqual(persisted.manualTopups.map((topup) => ({
    operatorUserId: topup.operatorUserId,
    operatorAccountId: topup.operatorAccountId,
    targetUserId: topup.targetUserId,
    targetAccountId: topup.targetAccountId,
    amount: topup.amount,
    reason: topup.reason,
    status: topup.status,
    balanceBefore: topup.balanceBefore,
    balanceAfter: topup.balanceAfter
  })), [
    {
      operatorUserId: "usr-admin",
      operatorAccountId: "admin",
      targetUserId: "usr-pi-alpha",
      targetAccountId: "pi-alpha",
      amount: 250,
      reason: "owner_credit",
      status: "completed",
      balanceBefore: 0,
      balanceAfter: 250
    }
  ]);
  assert.deepEqual(persisted.walletTransactions.map((transaction) => ({
    userId: transaction.userId,
    accountId: transaction.accountId,
    type: transaction.type,
    amount: transaction.amount,
    balanceBefore: transaction.balanceBefore,
    balanceAfter: transaction.balanceAfter,
    sourceEventId: transaction.sourceEventId
  })), [
    {
      userId: "usr-pi-alpha",
      accountId: "pi-alpha",
      type: "credit",
      amount: 250,
      balanceBefore: 0,
      balanceAfter: 250,
      sourceEventId: "owner_credit"
    }
  ]);
  assert.equal(persisted.billingLedger.some((entry) => entry.type === "credit" && entry.userId === "usr-pi-alpha"), true);
  assert.equal(persisted.audit.some((entry) => entry.type === "account.credit_granted"), true);
});

test("opening a Workspace freezes seven days of compute and storage and charges the first hour from available balance", async () => {
  const service = createTestService({
    name: "billing-provider",
    async createWorkspaceRuntime(input) {
      return runtimeFixture(input);
    }
  });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Prepaid Lab",
    packageId: "basic"
  });

  const state = await service.getState("pi-alpha");
  assert.equal(state.account.balance, 248.7967);
  assert.equal(state.account.frozen, 202.16);
  assert.equal(state.user.id, "usr-pi-alpha");
  assert.equal(state.user.balance, 248.7967);
  assert.equal(state.user.frozen, 202.16);
  assert.equal(state.user.totalRecharged, 250);
  assert.equal(state.wallet.balance, 248.7967);
  assert.equal(state.wallet.frozen, 202.16);
  assert.equal(state.billingLedger[0].userId, "usr-pi-alpha");
  assert.equal(state.billingLedger.every((entry) => entry.userId === "usr-pi-alpha"), true);
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

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
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

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
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
  assert.deepEqual(state.resourceUsageLogs.filter((log) => log.sourceEventId === "billing_tick_hold_exhausted").map((log) => log.resourceType), ["compute", "storage"]);
  const persisted = await service.store.read();
  const usageLogs = persisted.resourceUsageLogs.filter((log) => log.sourceEventId === "billing_tick_hold_exhausted");
  assert.deepEqual(usageLogs.map((log) => log.resourceType), ["compute", "storage"]);
  assert.equal(usageLogs[0].unit, "hour");
  assert.equal(usageLogs[1].unit, "gb_hour");
  assert.equal(usageLogs.every((log) => log.userId === "usr-pi-alpha"), true);
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

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
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

  await service.manualTopUp({ accountId: "pi-alpha", amount: 204, reason: "owner_credit" });
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

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
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

test("request usage charges the user wallet and records request logs", async () => {
  const service = createTestService({
    name: "request-usage-provider",
    async createWorkspaceRuntime(input) {
      return runtimeFixture(input);
    }
  });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Request Usage Lab",
    packageId: "basic"
  });

  const usage = await service.recordRequestUsage({
    accountId: "pi-alpha",
    workspaceId: workspace.id,
    requestId: "req-alpha",
    provider: "openai",
    model: "gpt-5",
    inputTokens: 1000,
    outputTokens: 500,
    amount: 0.25,
    sourceEventId: "gateway_req_alpha"
  });

  const state = await service.getState("pi-alpha");
  const persisted = await service.store.read();
  assert.equal(usage.userId, "usr-pi-alpha");
  assert.equal(state.wallet.balance, 248.5467);
  assert.equal(state.requestUsageLogs.length, 1);
  assert.equal(state.requestUsageLogs[0].requestId, "req-alpha");
  assert.deepEqual(persisted.requestUsageLogs.map((log) => ({
    requestId: log.requestId,
    userId: log.userId,
    amount: log.amount,
    sourceEventId: log.sourceEventId
  })), [
    {
      requestId: "req-alpha",
      userId: "usr-pi-alpha",
      amount: 0.25,
      sourceEventId: "gateway_req_alpha"
    }
  ]);
  assert.equal(state.billingLedger.some((entry) => entry.type === "request_debit" && entry.userId === "usr-pi-alpha"), true);
});

test("request usage deduplicates same fingerprint and rejects conflicting replay", async () => {
  const service = createTestService({
    name: "request-dedup-provider",
    async createWorkspaceRuntime(input) {
      return runtimeFixture(input);
    }
  });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Request Dedup Lab",
    packageId: "basic"
  });

  const input = {
    accountId: "pi-alpha",
    workspaceId: workspace.id,
    requestId: "req-dedup",
    provider: "openai",
    model: "gpt-5",
    inputTokens: 1000,
    outputTokens: 500,
    amount: 0.25,
    sourceEventId: "gateway_req_dedup"
  };
  const first = await service.recordRequestUsage(input);
  const replay = await service.recordRequestUsage(input);
  assert.equal(replay.id, first.id);

  const afterReplay = await service.getState("pi-alpha");
  assert.equal(afterReplay.requestUsageLogs.length, 1);
  assert.equal(afterReplay.requestUsageDedup.length, 1);
  assert.equal(afterReplay.walletTransactions.filter((transaction) => transaction.type === "request_debit").length, 1);
  assert.equal(afterReplay.billingLedger.filter((entry) => entry.type === "request_debit").length, 1);
  assert.equal(afterReplay.requestUsageLogs[0].requestFingerprint, first.requestFingerprint);

  await assert.rejects(
    () => service.recordRequestUsage({ ...input, amount: 0.5 }),
    /request_usage_fingerprint_conflict/
  );

  const afterConflict = await service.getState("pi-alpha");
  assert.equal(afterConflict.requestUsageLogs.length, 1);
  assert.equal(afterConflict.walletTransactions.filter((transaction) => transaction.type === "request_debit").length, 1);
});

test("request usage quota rejects billing before wallet mutation", async () => {
  const service = createTestService({
    name: "request-quota-provider",
    async createWorkspaceRuntime(input) {
      return runtimeFixture(input);
    }
  });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Request Quota Lab",
    packageId: "basic"
  });
  await service.store.update((state) => {
    state.users["usr-pi-alpha"].requestQuota = {
      limit: 1,
      used: 1,
      windowLimit: 1,
      windowUsed: 1,
      windowSeconds: 3600,
      windowStartedAt: "2026-07-02T00:00:00.000Z"
    };
  });
  const before = await service.getState("pi-alpha");

  await assert.rejects(
    () => service.recordRequestUsage({
      accountId: "pi-alpha",
      workspaceId: workspace.id,
      requestId: "req-quota",
      provider: "openai",
      model: "gpt-5",
      inputTokens: 100,
      outputTokens: 50,
      amount: 0.1,
      sourceEventId: "gateway_req_quota"
    }),
    /request_quota_exceeded/
  );

  const after = await service.getState("pi-alpha");
  assert.equal(after.wallet.balance, before.wallet.balance);
  assert.equal(after.requestUsageLogs.length, 0);
  assert.equal(after.requestUsageDedup.length, 0);
  assert.equal(after.walletTransactions.filter((transaction) => transaction.type === "request_debit").length, 0);
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

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
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
