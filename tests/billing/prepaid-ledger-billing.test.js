import assert from "node:assert/strict";
import test from "node:test";

import { createOplCloud, packageHoldAmount } from "../../packages/console/src/opl-cloud.js";
import { MemoryStore } from "../../packages/console/src/store.js";
import { createFakeRuntimeProvider } from "../helpers/fake-runtime-provider.js";

const TEST_PRICING = {
  computeHourly: {
    basic: 1,
    pro: 4
  },
  storageGbMonth: 0.2
};

const FINAL_USER_PRICING = {
  priceBasis: "opl_user_price_catalog",
  providerCostBasis: "internal_estimate_only",
  currency: "CNY",
  computeHourly: {
    basic: 0.468,
    pro: 1.38
  },
  storageGbMonth: 0.432,
  providerCostEstimate: {
    billingUse: "internal_reconciliation_only",
    source: "tencent_public_price_snapshot",
    sourceRegion: "na-siliconvalley",
    computeHourly: {
      basic: {
        instanceType: "SA5.MEDIUM4",
        estimatedHourly: 0.27
      },
      pro: {
        instanceType: "SA5.2XLARGE16",
        estimatedHourly: 1.15
      }
    },
    storageGbMonth: {
      storageClass: "premium_cbs",
      estimatedGbMonth: 0.34
    }
  }
};

function createTestService(runtimeProvider = { name: "test-provider" }) {
  return createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: createFakeRuntimeProvider({
      workspaceUrl({ workspaceId, token }) {
        return `https://workspace.example.com/w/${workspaceId}?token=${token}`;
      },
      ...runtimeProvider
    }),
    pricing: TEST_PRICING
  });
}

function createFinalPricingService(runtimeProvider = { name: "final-pricing-provider" }) {
  return createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: createFakeRuntimeProvider(runtimeProvider),
    pricing: FINAL_USER_PRICING
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
    priceBasis: plan.price.priceBasis
  })), [
    {
      id: "basic",
      accelerator: "cpu",
      cpu: 2,
      memoryGb: 4,
      gpu: 0,
      computeHourly: 1,
      storageGbMonth: 0.2,
      priceBasis: "opl_user_price_catalog"
    },
    {
      id: "pro",
      accelerator: "cpu",
      cpu: 8,
      memoryGb: 16,
      gpu: 0,
      computeHourly: 4,
      storageGbMonth: 0.2,
      priceBasis: "opl_user_price_catalog"
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

test("manual top-up replays with the same idempotency key return the original receipt without double crediting", async () => {
  const service = createTestService({
    name: "manual-topup-idempotency-provider"
  });

  const first = await service.manualTopUp({
    accountId: "pi-alpha",
    amount: 250,
    reason: "owner_credit",
    idempotencyKey: "admin-topup-pi-alpha-001",
    operatorUserId: "usr-admin",
    operatorAccountId: "admin"
  });
  const replay = await service.manualTopUp({
    accountId: "pi-alpha",
    amount: 250,
    reason: "owner_credit",
    idempotencyKey: "admin-topup-pi-alpha-001",
    operatorUserId: "usr-admin",
    operatorAccountId: "admin"
  });

  const persisted = await service.store.read();
  assert.equal(first.balance, 250);
  assert.deepEqual(replay, first);
  assert.equal(persisted.manualTopups.length, 1);
  assert.equal(persisted.walletTransactions.length, 1);
  assert.equal(persisted.billingLedger.filter((entry) => entry.type === "credit").length, 1);
  assert.equal(persisted.manualTopups[0].idempotencyKey, "admin-topup-pi-alpha-001");
  assert.equal(persisted.manualTopups[0].sourceEventId, "manual_topup:admin-topup-pi-alpha-001");

  await assert.rejects(
    () => service.manualTopUp({
      accountId: "pi-alpha",
      amount: 300,
      reason: "owner_credit",
      idempotencyKey: "admin-topup-pi-alpha-001",
      operatorUserId: "usr-admin",
      operatorAccountId: "admin"
    }),
    /manual_topup_idempotency_conflict/
  );
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
  assert.equal(state.account.frozen, 168.4667);
  assert.equal(state.user.id, "usr-pi-alpha");
  assert.equal(state.user.balance, 250);
  assert.equal(state.user.frozen, 168.4667);
  assert.equal(state.user.totalRecharged, 250);
  assert.equal(state.wallet.balance, 250);
  assert.equal(state.wallet.frozen, 168.4667);
  assert.equal(state.billingLedger[0].userId, "usr-pi-alpha");
  assert.equal(state.billingLedger.every((entry) => entry.userId === "usr-pi-alpha"), true);
  assert.equal(workspace.billing.model, "resource_scoped");
  assert.deepEqual({
    computeAllocationId: workspace.billing.computeAllocationId,
    storageId: workspace.billing.storageId,
    attachmentId: workspace.billing.attachmentId,
    minimumBillableHours: workspace.billing.minimumBillableHours,
    priceBasis: workspace.billing.priceBasis,
    computeHourly: workspace.billing.computeHourly,
    storageGbMonth: workspace.billing.storageGbMonth
  }, {
    computeAllocationId: workspace.computeAllocationId,
    storageId: workspace.storageId,
    attachmentId: workspace.attachmentId,
    minimumBillableHours: 1,
    priceBasis: "opl_user_price_catalog",
    computeHourly: 1,
    storageGbMonth: 0.2
  });
  assert.deepEqual(state.billingLedger.map((entry) => ({
    type: entry.type,
    amount: entry.amount,
    holdType: entry.holdType,
    sourceEventId: entry.sourceEventId
  })), [
    { type: "credit", amount: 250, holdType: undefined, sourceEventId: "owner_credit" },
    { type: "storage_hold", amount: 0.4667, holdType: "storage", sourceEventId: state.storageVolumes[0].id === workspace.storageId ? `storage_volume:${workspace.storageId}:created` : undefined },
    { type: "compute_hold", amount: 168, holdType: "compute", sourceEventId: state.computeAllocations[0].id === workspace.computeAllocationId ? `compute_allocation:${workspace.computeAllocationId}:created` : undefined },
    { type: "storage_attached", amount: 0, holdType: undefined, sourceEventId: `storage_attachment:${workspace.attachmentId}:created` },
    { type: "workspace_entry_created", amount: 0, holdType: undefined, sourceEventId: `workspace_entry:${workspace.id}:created` }
  ]);
  assert.deepEqual(state.walletTransactions.map((transaction) => ({
    type: transaction.type,
    amount: transaction.amount,
    balanceBefore: transaction.balanceBefore,
    balanceAfter: transaction.balanceAfter,
    frozenBefore: transaction.frozenBefore,
    frozenAfter: transaction.frozenAfter,
    resourceId: transaction.metadata?.computeAllocationId || transaction.metadata?.storageId || ""
  })), [
    {
      type: "credit",
      amount: 250,
      balanceBefore: 0,
      balanceAfter: 250,
      frozenBefore: 0,
      frozenAfter: 0,
      resourceId: ""
    },
    {
      type: "storage_hold",
      amount: 0,
      balanceBefore: 250,
      balanceAfter: 250,
      frozenBefore: 0,
      frozenAfter: 0.4667,
      resourceId: workspace.storageId
    },
    {
      type: "compute_hold",
      amount: 0,
      balanceBefore: 250,
      balanceAfter: 250,
      frozenBefore: 0.4667,
      frozenAfter: 168.4667,
      resourceId: workspace.computeAllocationId
    }
  ]);
  assert.equal(state.wallet.resourceHolds.compute[workspace.computeAllocationId].remaining, 168);
  assert.equal(state.wallet.resourceHolds.storage[workspace.storageId].remaining, 0.4667);
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
  assert.equal(state.account.frozen, 168.4667);
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

test("billing settlement rounds up to full hours and consumes available balance before frozen holds", async () => {
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
    { type: "compute_debit", amount: -81.5333, billableHours: 210, holdType: "compute", fundingSource: "available_balance" },
    { type: "compute_debit", amount: -128.4667, billableHours: 210, holdType: "compute", fundingSource: "compute_hold" },
    { type: "storage_debit", amount: -0.4667, billableHours: 210, holdType: "storage", fundingSource: "storage_hold" }
  ]);

  const state = await service.getState("pi-alpha");
  assert.equal(state.account.balance, 39.5333);
  assert.equal(state.account.frozen, 39.5333);
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
    "workspace.storage_hold_exhausted"
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

test("resource billing charges active compute and storage even before a Workspace URL exists", async () => {
  const service = createTestService({ name: "resource-billing-provider" });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const compute = await service.createComputeAllocation({
    accountId: "pi-alpha",
    packageId: "basic",
    name: "Billing compute"
  });
  await service.processPendingResourceProvisioning({ limit: 1 });
  const storage = await service.createStorageVolume({
    accountId: "pi-alpha",
    packageId: "basic",
    sizeGb: 10,
    name: "Billing storage"
  });

  const settlement = await service.settleResourceBilling({
    accountId: "pi-alpha",
    hours: 1,
    sourceEventId: "resource_billing_tick_1"
  });

  assert.deepEqual(settlement.entries.map((entry) => ({
    type: entry.type,
    amount: entry.amount,
    sourceEventId: entry.sourceEventId,
    computeAllocationId: entry.computeAllocationId,
    storageId: entry.storageId,
    fundingSource: entry.metadata?.fundingSource
  })), [
    {
      type: "compute_debit",
      amount: -1,
      sourceEventId: `resource_billing_tick_1:compute:${compute.id}`,
      computeAllocationId: compute.id,
      storageId: undefined,
      fundingSource: "available_balance"
    },
    {
      type: "storage_debit",
      amount: -0.0028,
      sourceEventId: `resource_billing_tick_1:storage:${storage.id}`,
      computeAllocationId: undefined,
      storageId: storage.id,
      fundingSource: "available_balance"
    }
  ]);

  const state = await service.getState("pi-alpha");
  assert.equal(state.workspaces.length, 0);
  assert.deepEqual(state.resourceUsageLogs
    .filter((log) => log.sourceEventId.startsWith("resource_billing_tick_1"))
    .map((log) => ({
      resourceType: log.resourceType,
      computeAllocationId: log.computeAllocationId || "",
      storageId: log.storageId || "",
      amount: log.amount
    })), [
    {
      resourceType: "compute",
      computeAllocationId: compute.id,
      storageId: "",
      amount: 1
    },
    {
      resourceType: "storage",
      computeAllocationId: "",
      storageId: storage.id,
      amount: 0.0028
    }
  ]);
  assert.deepEqual(state.walletTransactions
    .filter((transaction) => transaction.sourceEventId.startsWith("resource_billing_tick_1"))
    .map((transaction) => ({
      type: transaction.type,
      amount: transaction.amount,
      resourceId: transaction.metadata?.computeAllocationId || transaction.metadata?.storageId || ""
    })), [
    { type: "compute_debit", amount: -1, resourceId: compute.id },
    { type: "storage_debit", amount: -0.0028, resourceId: storage.id }
  ]);
});

test("resource ledger entries snapshot final user price and provider cost separately", async () => {
  const service = createFinalPricingService();
  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });

  const compute = await service.createComputeAllocation({
    accountId: "pi-alpha",
    userId: "usr-alpha",
    packageId: "basic",
    name: "billing node"
  });
  await service.processPendingResourceProvisioning({ limit: 1 });

  await service.settleResourceBilling({ accountId: "pi-alpha", hours: 1, sourceEventId: "tick-user-price" });
  const state = await service.getState("pi-alpha");
  const debit = state.billingLedger.find((entry) => entry.type === "compute_debit" && entry.computeAllocationId === compute.id);

  assert.equal(debit.metadata.priceBasis, "opl_user_price_catalog");
  assert.equal(debit.metadata.userPrice.computeHourly, 0.468);
  assert.equal(debit.metadata.providerCostEstimate.instanceType, "SA5.MEDIUM4");
  assert.equal(debit.metadata.providerCostEstimate.billingUse, "internal_reconciliation_only");
  assert.equal(debit.metadata.markup, undefined);
});

test("resource billing is idempotent per resource and billing hour", async () => {
  const service = createTestService({ name: "resource-billing-idempotent-provider" });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  await service.createComputeAllocation({ accountId: "pi-alpha", packageId: "basic", name: "Billing compute" });
  await service.processPendingResourceProvisioning({ limit: 1 });

  await service.settleResourceBilling({
    accountId: "pi-alpha",
    hours: 1,
    sourceEventId: "resource_billing_tick_retry"
  });
  const afterFirst = await service.getState("pi-alpha");

  await service.settleResourceBilling({
    accountId: "pi-alpha",
    hours: 1,
    sourceEventId: "resource_billing_tick_retry"
  });
  const afterRetry = await service.getState("pi-alpha");

  assert.equal(afterRetry.wallet.balance, afterFirst.wallet.balance);
  assert.equal(afterRetry.billingLedger.filter((entry) => entry.sourceEventId.startsWith("resource_billing_tick_retry")).length, 1);
  assert.equal(afterRetry.walletTransactions.filter((entry) => entry.sourceEventId.startsWith("resource_billing_tick_retry")).length, 1);
});

test("prepaid billing warns when available balance is exhausted before consuming frozen holds", async () => {
  const service = createTestService({ name: "low-balance-provider" });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 170, reason: "owner_credit" });
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
  assert.equal(state.account.balance, 167.9944);
  assert.equal(state.account.frozen, 167.9944);
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
  assert.equal(state.billingLedger.filter((entry) => entry.type === "compute_hold_released").at(-1).amount, -168);
  assert.equal(state.billingLedger.filter((entry) => entry.type === "storage_hold_released").at(-1).amount, -0.4667);
});

test("destroying one storage volume releases only that volume hold", async () => {
  const service = createTestService({ name: "per-resource-release-provider" });

  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const first = await service.createStorageVolume({
    accountId: "pi-alpha",
    packageId: "basic",
    sizeGb: 10,
    name: "First volume"
  });
  const second = await service.createStorageVolume({
    accountId: "pi-alpha",
    packageId: "basic",
    sizeGb: 10,
    name: "Second volume"
  });

  await service.destroyStorageVolume({ accountId: "pi-alpha", storageId: first.id, confirmDataLoss: true });

  const state = await service.getState("pi-alpha");
  assert.equal(state.account.frozen, 0.4667);
  assert.equal(state.wallet.resourceHolds.storage[first.id], undefined);
  assert.equal(state.wallet.resourceHolds.storage[second.id].remaining, 0.4667);
  assert.deepEqual(state.billingLedger
    .filter((entry) => entry.type === "storage_hold_released")
    .map((entry) => ({
      amount: entry.amount,
      storageId: entry.storageId,
      sourceEventId: entry.sourceEventId
    })), [
    {
      amount: -0.4667,
      storageId: first.id,
      sourceEventId: "destroy_storage"
    }
  ]);
});

test("hold calculation uses seven days of final OPL user prices", () => {
  const hold = packageHoldAmount({
    packagePlan: {
      id: "pro",
      diskGb: 100
    },
    pricing: TEST_PRICING
  });

  assert.deepEqual(hold, {
    compute: 672,
    storage: 4.6667,
    total: 676.6667
  });
});

test("resource usage logs can be aggregated and archived without losing billing evidence", async () => {
  const service = createFinalPricingService();
  await service.store.write({
    ...await service.store.read(),
    resourceUsageLogs: [
      {
        id: "usage-old-compute-1",
        userId: "usr-owner",
        accountId: "acct-owner",
        workspaceId: "ws-owned",
        computeAllocationId: "compute-owned",
        resourceType: "compute",
        quantity: 1,
        unit: "hour",
        unitPrice: 0.468,
        amount: 0.468,
        currency: "CNY",
        sourceEventId: "resource_billing_tick:2026-07-01T01:00:00.000Z",
        createdAt: "2026-07-01T01:05:00.000Z"
      },
      {
        id: "usage-old-compute-2",
        userId: "usr-owner",
        accountId: "acct-owner",
        workspaceId: "ws-owned",
        computeAllocationId: "compute-owned",
        resourceType: "compute",
        quantity: 1,
        unit: "hour",
        unitPrice: 0.468,
        amount: 0.468,
        currency: "CNY",
        sourceEventId: "resource_billing_tick:2026-07-01T01:30:00.000Z",
        createdAt: "2026-07-01T01:35:00.000Z"
      },
      {
        id: "usage-recent-storage",
        userId: "usr-owner",
        accountId: "acct-owner",
        workspaceId: "ws-owned",
        storageId: "storage-owned",
        resourceType: "storage",
        quantity: 1,
        unit: "gb_hour",
        unitPrice: 0.0006,
        amount: 0.0006,
        currency: "CNY",
        sourceEventId: "resource_billing_tick:2026-07-03T01:00:00.000Z",
        createdAt: "2026-07-03T01:05:00.000Z"
      }
    ]
  });

  const aggregate = await service.aggregateResourceUsage({
    olderThan: "2026-07-02T00:00:00.000Z",
    sourceEventId: "usage_rollup_test"
  });
  assert.deepEqual(aggregate, {
    sourceLogCount: 2,
    hourlyRows: 1,
    dailyRows: 1
  });

  let persisted = await service.store.read();
  assert.deepEqual(persisted.resourceUsageHourly.map((row) => ({
    bucket: row.bucket,
    accountId: row.accountId,
    workspaceId: row.workspaceId,
    resourceType: row.resourceType,
    quantity: row.quantity,
    amount: row.amount,
    sourceLogIds: row.sourceLogIds
  })), [
    {
      bucket: "2026-07-01T01:00:00.000Z",
      accountId: "acct-owner",
      workspaceId: "ws-owned",
      resourceType: "compute",
      quantity: 2,
      amount: 0.936,
      sourceLogIds: ["usage-old-compute-1", "usage-old-compute-2"]
    }
  ]);
  assert.deepEqual(persisted.resourceUsageDaily.map((row) => ({
    bucket: row.bucket,
    accountId: row.accountId,
    workspaceId: row.workspaceId,
    resourceType: row.resourceType,
    quantity: row.quantity,
    amount: row.amount,
    sourceLogIds: row.sourceLogIds
  })), [
    {
      bucket: "2026-07-01T00:00:00.000Z",
      accountId: "acct-owner",
      workspaceId: "ws-owned",
      resourceType: "compute",
      quantity: 2,
      amount: 0.936,
      sourceLogIds: ["usage-old-compute-1", "usage-old-compute-2"]
    }
  ]);

  const archive = await service.archiveResourceUsageLogs({
    olderThan: "2026-07-02T00:00:00.000Z",
    sourceEventId: "usage_archive_test"
  });
  assert.deepEqual(archive, {
    archivedLogCount: 2,
    remainingLogCount: 1,
    archiveId: "usage-archive-usage_archive_test"
  });

  persisted = await service.store.read();
  assert.deepEqual(persisted.resourceUsageLogs.map((log) => log.id), ["usage-recent-storage"]);
  assert.deepEqual(persisted.resourceUsageArchive.map((entry) => ({
    id: entry.id,
    sourceEventId: entry.sourceEventId,
    logIds: entry.logs.map((log) => log.id)
  })), [
    {
      id: "usage-archive-usage_archive_test",
      sourceEventId: "usage_archive_test",
      logIds: ["usage-old-compute-1", "usage-old-compute-2"]
    }
  ]);
  assert.deepEqual(persisted.resourceUsageCleanupTasks.map((task) => ({
    type: task.type,
    sourceEventId: task.sourceEventId,
    archivedLogCount: task.archivedLogCount
  })), [
    {
      type: "resource_usage_archive",
      sourceEventId: "usage_archive_test",
      archivedLogCount: 2
    }
  ]);
});
