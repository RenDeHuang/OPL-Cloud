import assert from "node:assert/strict";
import test from "node:test";

import { packageHoldAmount } from "../../packages/console/src/opl-cloud.js";
import {
  createCurrentTestService,
  provisionWorkspace,
  TEST_PRICING
} from "../helpers/current-resource-chain.js";

test("packages expose only production-ready CPU choices from the pricing catalog", async () => {
  const service = createCurrentTestService();

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
  const service = createCurrentTestService();

  const account = await service.manualTopUp({
    accountId: "pi-alpha",
    amount: 300,
    reason: "owner_credit",
    operatorUserId: "usr-admin",
    operatorAccountId: "admin"
  });

  const persisted = await service.store.read();
  assert.equal(account.balance, 300);
  assert.equal(persisted.manualTopups.length, 1);
  assert.equal(persisted.walletTransactions.length, 1);
  assert.equal(persisted.manualTopups[0].targetAccountId, "pi-alpha");
  assert.equal(persisted.manualTopups[0].amount, 300);
  assert.equal(persisted.walletTransactions[0].type, "credit");
  assert.equal(persisted.billingLedger.some((entry) => entry.type === "credit" && entry.userId === "usr-pi-alpha"), true);
  assert.equal(persisted.audit.some((entry) => entry.type === "account.credit_granted"), true);
});

test("resource provisioning freezes seven days of compute and storage before Workspace URL entry", async () => {
  const service = createCurrentTestService();

  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });
  const { compute, storage, attachment, workspace } = await provisionWorkspace(service, {
    accountId: "pi-alpha",
    userId: "usr-alpha",
    workspaceName: "Prepaid Lab",
    packageId: "basic"
  });

  const state = await service.getState("pi-alpha");
  assert.equal(state.account.balance, 300);
  assert.equal(state.account.frozen, 202.16);
  assert.equal(state.wallet.available, 97.84);
  assert.equal(state.user.totalRecharged, 300);
  assert.equal(workspace.billing.model, "resource_scoped");
  assert.equal(workspace.billing.computeId, compute.id);
  assert.equal(workspace.billing.storageId, storage.id);
  assert.equal(workspace.billing.attachmentId, attachment.id);
  assert.deepEqual(state.billingLedger.map((entry) => entry.type), [
    "credit",
    "compute_hold",
    "storage_hold",
    "storage_attached",
    "workspace_entry_created"
  ]);
  assert.equal(state.billingLedger.some((entry) => entry.computeId === compute.id), true);
  assert.equal(state.billingLedger.some((entry) => entry.storageId === storage.id), true);
  assert.equal(state.billingLedger.some((entry) => entry.attachmentId === attachment.id), true);
});

test("Workspace billing settlement consumes available balance and marks compute hold exhaustion", async () => {
  const service = createCurrentTestService();

  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });
  const { workspace } = await provisionWorkspace(service, {
    accountId: "pi-alpha",
    userId: "usr-alpha",
    workspaceName: "Hold Exhaustion Lab",
    packageId: "basic"
  });

  const settlement = await service.settleBilling({
    accountId: "pi-alpha",
    workspaceId: workspace.id,
    hours: 400,
    sourceEventId: "billing_tick_hold_exhausted"
  });

  assert.equal(settlement.entries.some((entry) => entry.type === "compute_hold_exhausted"), true);
  const state = await service.getState("pi-alpha");
  const settledWorkspace = state.workspaces[0];
  assert.equal(settledWorkspace.server.status, "running");
  assert.equal(settledWorkspace.server.billingStatus, "hold_exhausted");
  assert.equal(settledWorkspace.state, "compute_hold_exhausted");
  assert.equal(state.notifications.some((event) => event.type === "workspace.compute_hold_exhausted"), true);
});

test("billing settlement is idempotent for the same source event", async () => {
  const service = createCurrentTestService();

  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });
  const { workspace } = await provisionWorkspace(service, {
    accountId: "pi-alpha",
    userId: "usr-alpha",
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
  const service = createCurrentTestService();

  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });
  const { workspace } = await provisionWorkspace(service, {
    accountId: "pi-alpha",
    userId: "usr-alpha",
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
  assert.equal(usage.userId, "usr-pi-alpha");
  assert.equal(state.requestUsageLogs.length, 1);
  assert.equal(state.requestUsageLogs[0].requestId, "req-alpha");
  assert.equal(state.billingLedger.some((entry) => entry.type === "request_debit" && entry.userId === "usr-pi-alpha"), true);
});

test("request usage deduplicates same fingerprint and rejects conflicting replay", async () => {
  const service = createCurrentTestService();

  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });
  const { workspace } = await provisionWorkspace(service, {
    accountId: "pi-alpha",
    userId: "usr-alpha",
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

  await assert.rejects(
    () => service.recordRequestUsage({ ...input, amount: 0.5 }),
    /request_usage_fingerprint_conflict/
  );
});

test("request usage quota rejects billing before wallet mutation", async () => {
  const service = createCurrentTestService();

  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });
  const { workspace } = await provisionWorkspace(service, {
    accountId: "pi-alpha",
    userId: "usr-alpha",
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

test("destroying compute and storage releases unused prepaid holds after detach", async () => {
  const service = createCurrentTestService();

  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });
  const { compute, storage, attachment } = await provisionWorkspace(service, {
    accountId: "pi-alpha",
    userId: "usr-alpha",
    workspaceName: "Release Lab",
    packageId: "basic"
  });

  await service.detachStorage({ accountId: "pi-alpha", attachmentId: attachment.id, confirm: true });
  await service.destroyComputeResource({ accountId: "pi-alpha", computeId: compute.id, confirm: true });
  await service.destroyStorageVolume({ accountId: "pi-alpha", storageId: storage.id, confirmDataLoss: true });

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
