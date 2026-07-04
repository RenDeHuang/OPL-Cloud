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

function createTestService(runtimeProvider = { name: "test-provider" }) {
  return createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: {
      workspaceUrl({ workspaceId, token }) {
        return `https://workspace.example.com/w/${workspaceId}?token=${token}`;
      },
      ...runtimeProvider
    },
    pricing: TEST_PRICING
  });
}

async function createWorkspaceEntry(service, { accountId, workspaceName, packageId = "basic" }) {
  const storage = await service.createStorageVolume({ accountId, packageId, name: `${workspaceName} storage` });
  const compute = await service.createComputeAllocation({ accountId, packageId, name: `${workspaceName} compute` });
  const attachment = await service.attachStorage({
    accountId,
    computeAllocationId: compute.id,
    storageId: storage.id,
    mountPath: "/data"
  });
  return service.createWorkspace({ accountId, workspaceName, attachmentId: attachment.id });
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

test("opening compute and storage freezes seven days of prepaid hold before creating the Workspace URL", async () => {
  const service = createTestService({ name: "billing-provider" });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await createWorkspaceEntry(service, {
    accountId: "pi-alpha",
    workspaceName: "Prepaid Lab"
  });

  const state = await service.getState("pi-alpha");
  assert.equal(state.account.balance, 250);
  assert.equal(state.account.frozen, 202.16);
  assert.equal(state.user.id, "usr-pi-alpha");
  assert.equal(state.user.balance, 250);
  assert.equal(state.user.frozen, 202.16);
  assert.equal(state.user.totalRecharged, 250);
  assert.equal(state.wallet.balance, 250);
  assert.equal(state.wallet.frozen, 202.16);
  assert.equal(state.billingLedger[0].userId, "usr-pi-alpha");
  assert.equal(state.billingLedger.every((entry) => entry.userId === "usr-pi-alpha"), true);
  assert.equal(workspace.billing.model, "resource_scoped");
  assert.deepEqual({
    computeAllocationId: workspace.billing.computeAllocationId,
    storageId: workspace.billing.storageId,
    attachmentId: workspace.billing.attachmentId,
    minimumBillableHours: workspace.billing.minimumBillableHours,
    priceMarkup: workspace.billing.priceMarkup
  }, {
    computeAllocationId: workspace.computeAllocationId,
    storageId: workspace.storageId,
    attachmentId: workspace.attachmentId,
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
    { type: "storage_hold", amount: 0.56, holdType: "storage", sourceEventId: state.storageVolumes[0].id === workspace.storageId ? `storage_volume:${workspace.storageId}:created` : undefined },
    { type: "compute_hold", amount: 201.6, holdType: "compute", sourceEventId: state.computeAllocations[0].id === workspace.computeAllocationId ? `compute_allocation:${workspace.computeAllocationId}:created` : undefined },
    { type: "storage_attached", amount: 0, holdType: undefined, sourceEventId: `storage_attachment:${workspace.attachmentId}:created` },
    { type: "workspace_entry_created", amount: 0, holdType: undefined, sourceEventId: `workspace_entry:${workspace.id}:created` }
  ]);
});

test("Workspace URL creation failure keeps independent resource holds and records an operator-visible notification", async () => {
  const service = createTestService({
    name: "failing-provider",
    async createWorkspaceEntry() {
      throw new Error("image_pull_failed");
    }
  });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  await assert.rejects(
    createWorkspaceEntry(service, {
      accountId: "pi-alpha",
      workspaceName: "Broken Lab"
    }),
    /image_pull_failed/
  );

  const state = await service.getState("pi-alpha");
  assert.equal(state.account.balance, 250);
  assert.equal(state.account.frozen, 202.16);
  assert.equal(state.workspaces.length, 0);
  assert.deepEqual(state.billingLedger.map((entry) => entry.type), [
    "credit",
    "storage_hold",
    "compute_hold",
    "storage_attached"
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

test("billing settlement rounds up to full hours, consumes available balance first, and records exhausted compute hold", async () => {
  const service = createTestService({ name: "hold-exhaustion-provider" });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await createWorkspaceEntry(service, {
    accountId: "pi-alpha",
    workspaceName: "Hold Exhaustion Lab"
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
    { type: "compute_debit", amount: -47.84, billableHours: 210, holdType: "compute", fundingSource: "available_balance" },
    { type: "compute_debit", amount: -201.6, billableHours: 210, holdType: "compute", fundingSource: "compute_hold" },
    { type: "storage_debit", amount: -0.56, billableHours: 210, holdType: "storage", fundingSource: "storage_hold" },
    { type: "compute_hold_exhausted", amount: 0, billableHours: undefined, holdType: "compute", fundingSource: undefined }
  ]);

  const state = await service.getState("pi-alpha");
  assert.equal(state.account.balance, 0);
  assert.equal(state.account.frozen, 0);
  assert.equal(state.workspaces[0].server.status, "running");
  assert.equal(state.workspaces[0].disk.billingStatus, "hold_exhausted");
  assert.equal(state.workspaces[0].state, "storage_hold_exhausted");
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
    "workspace.compute_hold_exhausted"
  ]);
});

test("prepaid billing uses available balance first and never debits beyond available plus frozen hold pools", async () => {
  const service = createTestService({ name: "bounded-debit-provider" });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await createWorkspaceEntry(service, {
    accountId: "pi-alpha",
    workspaceName: "Bounded Debit Lab"
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
  const service = createTestService({ name: "low-balance-provider" });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 204, reason: "owner_credit" });
  const workspace = await createWorkspaceEntry(service, {
    accountId: "pi-alpha",
    workspaceName: "Low Balance Lab"
  });

  await service.settleBilling({
    accountId: "pi-alpha",
    workspaceId: workspace.id,
    hours: 2,
    sourceEventId: "billing_tick_available_exhausted"
  });

  const state = await service.getState("pi-alpha");
  assert.equal(state.account.balance, 201.5933);
  assert.equal(state.account.frozen, 201.5933);
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
  const service = createTestService({ name: "idempotent-billing-provider" });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await createWorkspaceEntry(service, {
    accountId: "pi-alpha",
    workspaceName: "Idempotent Billing Lab"
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
  const service = createTestService({ name: "request-usage-provider" });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await createWorkspaceEntry(service, {
    accountId: "pi-alpha",
    workspaceName: "Request Usage Lab"
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
  assert.equal(state.wallet.balance, 249.75);
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
  const service = createTestService({ name: "request-dedup-provider" });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await createWorkspaceEntry(service, {
    accountId: "pi-alpha",
    workspaceName: "Request Dedup Lab"
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
  const service = createTestService({ name: "request-quota-provider" });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await createWorkspaceEntry(service, {
    accountId: "pi-alpha",
    workspaceName: "Request Quota Lab"
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

test("destroying compute allocation and storage volume releases unused prepaid holds", async () => {
  const service = createTestService({ name: "destroy-provider" });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await createWorkspaceEntry(service, {
    accountId: "pi-alpha",
    workspaceName: "Release Lab"
  });
  await service.detachStorage({ accountId: "pi-alpha", attachmentId: workspace.attachmentId, confirm: true });
  await service.destroyComputeAllocation({ accountId: "pi-alpha", computeAllocationId: workspace.computeAllocationId, confirm: true });
  await service.destroyStorageVolume({ accountId: "pi-alpha", storageId: workspace.storageId, confirmDataLoss: true });

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
