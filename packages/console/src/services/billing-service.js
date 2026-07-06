
import { billingReconciliationGuard } from "../../../ledger/src/index.js";
import { clone, makeId, money, now } from "./core-utils.js";
import {
  accountAvailable,
  accountHold,
  accountSnapshotForState,
  addHold,
  appendWalletTransaction,
  chargeAccount,
  chargeResourceAccount,
  ensureAccount,
  ensureBillingCollections,
  releaseHold,
  releaseResourceHold
} from "./wallet-service.js";
import {
  billableHours,
  computePriceSnapshot,
  hourlyComputeAmount,
  hourlyStorageAmount,
  pricedComputeHourly,
  storageGbHourPrice,
  storagePriceSnapshot
} from "./pricing-service.js";
import { latestBillingReconciliationReport, latestWorkspaceForAccount } from "./workspace-service.js";
import { OplDomainService } from "./opl-domain-service.js";

function addResourceIds(target, { computeAllocationId = "", storageId = "", attachmentId = "" } = {}) {
  if (computeAllocationId) target.computeAllocationId = computeAllocationId;
  if (storageId) target.storageId = storageId;
  if (attachmentId) target.attachmentId = attachmentId;
  return target;
}

function bucketIso(timestamp, granularity) {
  const date = new Date(timestamp);
  if (Number.isNaN(date.getTime())) return "";
  date.setUTCMinutes(granularity === "day" ? 0 : 0, 0, 0);
  if (granularity === "day") date.setUTCHours(0, 0, 0, 0);
  return date.toISOString();
}

function usageGroupKey(log, bucket) {
  return [bucket, log.accountId || "", log.workspaceId || "", log.resourceType || ""].join("|");
}

function aggregateUsageRows(logs, granularity, sourceEventId) {
  const rows = new Map();
  for (const log of logs) {
    const bucket = bucketIso(log.createdAt, granularity);
    if (!bucket) continue;
    const key = usageGroupKey(log, bucket);
    const existing = rows.get(key) || {
      id: `usage-${granularity === "day" ? "daily" : "hourly"}-${log.accountId || "account"}-${log.workspaceId || "workspace"}-${log.resourceType || "resource"}-${bucket}`,
      bucket,
      accountId: log.accountId || "",
      workspaceId: log.workspaceId || "",
      resourceType: log.resourceType || "",
      quantity: 0,
      amount: 0,
      currency: log.currency || "CNY",
      sourceEventId,
      sourceLogIds: [],
      computeAllocationIds: [],
      storageIds: [],
      attachmentIds: [],
      createdAt: now()
    };
    existing.quantity = money(Number(existing.quantity || 0) + Number(log.quantity || 0));
    existing.amount = money(Number(existing.amount || 0) + Number(log.amount || 0));
    existing.sourceLogIds.push(log.id);
    if (log.computeAllocationId && !existing.computeAllocationIds.includes(log.computeAllocationId)) existing.computeAllocationIds.push(log.computeAllocationId);
    if (log.storageId && !existing.storageIds.includes(log.storageId)) existing.storageIds.push(log.storageId);
    if (log.attachmentId && !existing.attachmentIds.includes(log.attachmentId)) existing.attachmentIds.push(log.attachmentId);
    rows.set(key, existing);
  }
  return [...rows.values()];
}

function upsertUsageAggregate(collection, row) {
  const existingIndex = collection.findIndex((item) => item.id === row.id);
  if (existingIndex >= 0) {
    collection[existingIndex] = row;
  } else {
    collection.push(row);
  }
}

export class BillingService extends OplDomainService {
  async manualTopUp({ accountId, amount, reason, operatorUserId = "", operatorAccountId = "" }) {
    if (!accountId) throw new Error("account_required");
    const credit = Number(amount);
    if (!Number.isFinite(credit) || credit <= 0) throw new Error("positive_credit_required");

    return this.store.update((state) => {
      ensureBillingCollections(state);
      const account = ensureAccount(state, accountId);
      const sourceEventId = reason || "owner_credit";
      const balanceBefore = money(Number(account.balance || 0));
      const frozenBefore = money(Number(account.frozen || 0));
      account.balance = money(account.balance + credit);
      account.totalRecharged = money(Number(account.totalRecharged || 0) + credit);
      const entry = this.ledgerEntry({ state,
        workspaceId: "account",
        accountId,
        type: "credit",
        amount: credit,
        sourceEventId,
        metadata: {
          operatorUserId,
          operatorAccountId
        }
      });
      state.billingLedger.push(entry);
      const transaction = appendWalletTransaction(state, {
        user: account,
        accountId,
        type: "credit",
        amount: credit,
        sourceEventId,
        ledgerEntryId: entry.id,
        balanceBefore,
        balanceAfter: account.balance,
        frozenBefore,
        frozenAfter: account.frozen,
        metadata: {
          operatorUserId,
          operatorAccountId
        }
      });
      const topup = {
        id: makeId("manual-topup", account.id, accountId, sourceEventId, String(state.manualTopups.length)),
        operatorUserId,
        operatorAccountId,
        targetUserId: account.id,
        targetAccountId: accountId,
        amount: money(credit),
        currency: "CNY",
        reason: sourceEventId,
        status: "completed",
        balanceBefore,
        balanceAfter: money(Number(account.balance || 0)),
        ledgerEntryId: entry.id,
        walletTransactionId: transaction.id,
        createdAt: now()
      };
      state.manualTopups.push(topup);
      state.audit.push(this.auditEvent({ accountId, type: "account.credit_granted", sourceEventId: topup.id }));
      return accountSnapshotForState(state, accountId);
    });
  }

  async settleBilling({ accountId, workspaceId, hours = 1, sourceEventId = "billing_tick" }) {
    const requestedBillHours = billableHours(hours);

    const settlement = await this.store.update((state) => {
      const workspace = latestWorkspaceForAccount(state, accountId, workspaceId);
      const account = ensureAccount(state, accountId);
      const packagePlan = this.getPackage(workspace.packageId);
      const existingEntries = this.existingSettlementEntries({ state, accountId, workspaceId, sourceEventId });
      if (existingEntries.length > 0) {
        return {
          entries: existingEntries.map(clone),
          account: accountSnapshotForState(state, accountId)
        };
      }
      const entries = this.debitWorkspaceUsage({
        state,
        account,
        workspace,
        packagePlan,
        hours: requestedBillHours,
        sourceEventId,
        billableHours: requestedBillHours
      });
      if (entries.length > 0) {
        state.audit.push(this.auditEvent({ accountId, workspaceId, type: "billing.settled", sourceEventId }));
      }
      return {
        entries: entries.map(clone),
        account: accountSnapshotForState(state, accountId)
      };
    });
    return {
      entries: settlement.entries,
      account: settlement.account
    };
  }

  async settleResourceBilling({ accountId = "", hours = 1, sourceEventId = "" } = {}) {
    const requestedBillHours = billableHours(hours);
    const tickId = sourceEventId || this.currentHourlyBillingTick();
    return this.store.update((state) => {
      ensureBillingCollections(state);
      state.computeAllocations ??= [];
      state.storageVolumes ??= [];
      const entries = [];
      const computeAllocations = state.computeAllocations.filter((compute) =>
        (!accountId || compute.ownerAccountId === accountId) &&
        compute.status === "running" &&
        compute.billingStatus === "active"
      );
      const storageVolumes = state.storageVolumes.filter((storage) =>
        (!accountId || storage.ownerAccountId === accountId) &&
        ["available", "attached"].includes(storage.status) &&
        storage.billingStatus === "active"
      );

      for (const compute of computeAllocations) {
        const account = ensureAccount(state, compute.ownerAccountId);
        const packagePlan = this.getPackage(compute.packageId);
        entries.push(...this.debitComputeAllocationUsage({
          state,
          account,
          compute,
          packagePlan,
          hours: requestedBillHours,
          sourceEventId: `${tickId}:compute:${compute.id}`
        }));
      }
      for (const storage of storageVolumes) {
        const account = ensureAccount(state, storage.ownerAccountId);
        const packagePlan = { ...this.getPackage(storage.packageId), diskGb: storage.sizeGb };
        entries.push(...this.debitStorageResourceUsage({
          state,
          account,
          storage,
          packagePlan,
          hours: requestedBillHours,
          sourceEventId: `${tickId}:storage:${storage.id}`
        }));
      }

      if (entries.length > 0) {
        state.audit.push(this.auditEvent({
          accountId: accountId || "all",
          workspaceId: "resource",
          type: "billing.resources_settled",
          sourceEventId: tickId
        }));
      }
      return {
        entries: entries.map(clone),
        account: accountId ? accountSnapshotForState(state, accountId) : null
      };
    });
  }

  async billingLedger(accountId) {
    const state = await this.store.read();
    return state.billingLedger.filter((entry) => entry.accountId === accountId).map(clone);
  }

  async recordBillingReconciliation({ report, source = "manual" }) {
    if (!report || typeof report !== "object") throw new Error("billing_reconciliation_report_required");
    return this.store.update((state) => {
      state.billingReconciliationReports ??= [];
      const guard = billingReconciliationGuard({
        latestReport: report,
        now: report.generatedAt || now(),
        requireRecentReport: true
      });
      const record = {
        ...clone(report),
        id: report.id || makeId("recon", source, report.generatedAt || now(), String(state.billingReconciliationReports.length)),
        source,
        guard,
        createdAt: now()
      };
      state.billingReconciliationReports.push(record);
      state.audit.push(this.auditEvent({
        accountId: "billing",
        type: guard.blockNewWorkspaces ? "billing.reconciliation_guard_blocked" : "billing.reconciliation_recorded",
        sourceEventId: record.id
      }));
      if (guard.blockNewWorkspaces) {
        this.notify({
          state,
          accountId: "billing",
          workspaceId: "billing",
          type: "billing.reconciliation_guard_blocked",
          severity: "error",
          message: guard.reason,
          sourceEventId: record.id
        });
      }
      return clone(record);
    });
  }

  existingSettlementEntries({ state, accountId, workspaceId, sourceEventId }) {
    const settlementTypes = new Set(["compute_debit", "storage_debit", "compute_hold_exhausted"]);
    return state.billingLedger.filter((entry) =>
      entry.accountId === accountId &&
      entry.workspaceId === workspaceId &&
      entry.sourceEventId === sourceEventId &&
      settlementTypes.has(entry.type)
    );
  }

  existingResourceSettlementEntries({ state, accountId, sourceEventId, type }) {
    return state.billingLedger.filter((entry) =>
      entry.accountId === accountId &&
      entry.sourceEventId === sourceEventId &&
      entry.type === type
    );
  }

  currentHourlyBillingTick(date = new Date()) {
    const hour = new Date(date);
    hour.setUTCMinutes(0, 0, 0);
    return `resource_billing_tick:${hour.toISOString()}`;
  }

  recordResourceUsage({
    state,
    account,
    workspace,
    resourceType,
    quantity,
    unit,
    unitPrice,
    amount,
    requestedAmount,
    sourceEventId,
    metadata = {}
  }) {
    ensureBillingCollections(state);
    const existing = state.resourceUsageLogs.find((log) =>
      log.workspaceId === workspace.id &&
      log.resourceType === resourceType &&
      log.sourceEventId === sourceEventId
    );
    if (existing) return existing;
    const log = {
      id: makeId("usage-resource", workspace.ownerAccountId, workspace.id, resourceType, sourceEventId, String(state.resourceUsageLogs.length)),
      userId: account.id,
      accountId: workspace.ownerAccountId,
      workspaceId: workspace.id,
      resourceType,
      quantity: money(Number(quantity || 0)),
      unit,
      unitPrice: money(Number(unitPrice || 0)),
      amount: money(Number(amount || 0)),
      requestedAmount: money(Number(requestedAmount ?? amount ?? 0)),
      currency: "CNY",
      sourceEventId,
      metadata: clone(metadata),
      createdAt: now()
    };
    state.resourceUsageLogs.push(log);
    return log;
  }

  appendDebitEntries({ state, entries, workspaceId, accountId, type, holdType, charge, sourceEventId, billableHours, metadata, account = null, resourceIds = {} }) {
    const debits = [
      { amount: charge.available, fundingSource: "available_balance" },
      { amount: charge.hold, fundingSource: `${holdType}_hold` }
    ];
    for (const debit of debits) {
      if (debit.amount <= 0) continue;
      const balanceBefore = metadata?.balanceBefore;
      const frozenBefore = metadata?.frozenBefore;
      const entry = addResourceIds(this.ledgerEntry({ state,
        workspaceId,
        accountId,
        type,
        amount: -debit.amount,
        sourceEventId,
        holdType,
        billableHours,
        metadata: {
          ...metadata,
          fundingSource: debit.fundingSource
        }
      }), resourceIds);
      entries.push(entry);
      state.billingLedger.push(entry);
      if (account) {
        appendWalletTransaction(state, {
          user: account,
          accountId,
          workspaceId,
          type,
          amount: -debit.amount,
          sourceEventId,
          ledgerEntryId: entry.id,
          fundingSource: debit.fundingSource,
          balanceBefore,
          balanceAfter: account.balance,
          frozenBefore,
          frozenAfter: account.frozen,
          metadata: {
            ...resourceIds,
            ...metadata,
            fundingSource: debit.fundingSource
          }
        });
      }
    }
  }

  debitComputeAllocationUsage({ state, account, compute, packagePlan, hours, sourceEventId }) {
    const existing = this.existingResourceSettlementEntries({
      state,
      accountId: compute.ownerAccountId,
      sourceEventId,
      type: "compute_debit"
    });
    if (existing.length > 0) return existing.map(clone);
    const requestedAmount = hourlyComputeAmount({ packagePlan, pricing: this.pricing, hours });
    const balanceBefore = money(Number(account.balance || 0));
    const frozenBefore = money(Number(account.frozen || 0));
    const charge = chargeResourceAccount(account, "compute", compute.id, requestedAmount);
    const entries = [];
    const metadata = {
      computeAllocationId: compute.id,
      ownerAccountId: compute.ownerAccountId,
      ownerUserId: compute.ownerUserId || account.id,
      workspaceIds: compute.workspaceIds || [],
      nodePoolId: compute.nodePoolId || "",
      cvmInstanceId: compute.cvmInstanceId || compute.instanceId || "",
      machineName: compute.machineName || "",
      nodeName: compute.nodeName || "",
      privateIp: compute.privateIp || "",
      publicIp: compute.publicIp || "",
      requestedHours: hours,
      balanceBefore,
      frozenBefore,
      ...computePriceSnapshot({ packagePlan, pricing: this.pricing })
    };
    this.appendDebitEntries({
      state,
      entries,
      workspaceId: "resource",
      accountId: compute.ownerAccountId,
      type: "compute_debit",
      holdType: "compute",
      charge,
      sourceEventId,
      billableHours: hours,
      metadata,
      account,
      resourceIds: { computeAllocationId: compute.id }
    });
    this.recordAccountResourceUsage({
      state,
      account,
      accountId: compute.ownerAccountId,
      resourceType: "compute",
      computeAllocationId: compute.id,
      quantity: hours,
      unit: "hour",
      unitPrice: pricedComputeHourly({ packagePlan, pricing: this.pricing }),
      amount: charge.charged,
      requestedAmount,
      sourceEventId,
      metadata
    });
    return entries;
  }

  debitStorageResourceUsage({ state, account, storage, packagePlan, hours, sourceEventId }) {
    const existing = this.existingResourceSettlementEntries({
      state,
      accountId: storage.ownerAccountId,
      sourceEventId,
      type: "storage_debit"
    });
    if (existing.length > 0) return existing.map(clone);
    const requestedAmount = hourlyStorageAmount({ packagePlan, pricing: this.pricing, hours });
    const balanceBefore = money(Number(account.balance || 0));
    const frozenBefore = money(Number(account.frozen || 0));
    const charge = chargeResourceAccount(account, "storage", storage.id, requestedAmount);
    const entries = [];
    const metadata = {
      storageId: storage.id,
      ownerAccountId: storage.ownerAccountId,
      ownerUserId: storage.ownerUserId || account.id,
      workspaceIds: storage.workspaceIds || [],
      providerResourceId: storage.providerResourceId || "",
      storageClassId: storage.storageClassId || "",
      requestedHours: hours,
      sizeGb: storage.sizeGb,
      balanceBefore,
      frozenBefore,
      ...storagePriceSnapshot({ pricing: this.pricing, sizeGb: storage.sizeGb })
    };
    this.appendDebitEntries({
      state,
      entries,
      workspaceId: "resource",
      accountId: storage.ownerAccountId,
      type: "storage_debit",
      holdType: "storage",
      charge,
      sourceEventId,
      billableHours: hours,
      metadata,
      account,
      resourceIds: { storageId: storage.id }
    });
    this.recordAccountResourceUsage({
      state,
      account,
      accountId: storage.ownerAccountId,
      resourceType: "storage",
      storageId: storage.id,
      quantity: storage.sizeGb * hours,
      unit: "gb_hour",
      unitPrice: storageGbHourPrice(this.pricing),
      amount: charge.charged,
      requestedAmount,
      sourceEventId,
      metadata
    });
    return entries;
  }

  recordAccountResourceUsage({
    state,
    account,
    accountId,
    resourceType,
    computeAllocationId = "",
    storageId = "",
    attachmentId = "",
    quantity,
    unit,
    unitPrice,
    amount,
    requestedAmount,
    sourceEventId,
    metadata = {}
  }) {
    ensureBillingCollections(state);
    const existing = state.resourceUsageLogs.find((log) =>
      log.accountId === accountId &&
      log.resourceType === resourceType &&
      log.sourceEventId === sourceEventId
    );
    if (existing) return existing;
    const log = addResourceIds({
      id: makeId("usage-resource", accountId, computeAllocationId || storageId || attachmentId || "resource", resourceType, sourceEventId, String(state.resourceUsageLogs.length)),
      userId: account.id,
      accountId,
      workspaceId: "resource",
      resourceType,
      quantity: money(Number(quantity || 0)),
      unit,
      unitPrice: money(Number(unitPrice || 0)),
      amount: money(Number(amount || 0)),
      requestedAmount: money(Number(requestedAmount ?? amount ?? 0)),
      currency: "CNY",
      sourceEventId,
      metadata: clone(metadata),
      createdAt: now()
    }, { computeAllocationId, storageId, attachmentId });
    state.resourceUsageLogs.push(log);
    return log;
  }

  async aggregateResourceUsage({ olderThan, sourceEventId = "resource_usage_rollup" } = {}) {
    if (!olderThan) throw new Error("usage_rollup_cutoff_required");
    return this.store.update((state) => {
      ensureBillingCollections(state);
      const cutoff = new Date(olderThan).getTime();
      if (Number.isNaN(cutoff)) throw new Error("usage_rollup_cutoff_invalid");
      const logs = (state.resourceUsageLogs || []).filter((log) =>
        new Date(log.createdAt || 0).getTime() < cutoff
      );
      const hourlyRows = aggregateUsageRows(logs, "hour", sourceEventId);
      const dailyRows = aggregateUsageRows(logs, "day", sourceEventId);
      for (const row of hourlyRows) upsertUsageAggregate(state.resourceUsageHourly, row);
      for (const row of dailyRows) upsertUsageAggregate(state.resourceUsageDaily, row);
      return {
        sourceLogCount: logs.length,
        hourlyRows: hourlyRows.length,
        dailyRows: dailyRows.length
      };
    });
  }

  async archiveResourceUsageLogs({ olderThan, limit = 10000, sourceEventId = "resource_usage_archive" } = {}) {
    if (!olderThan) throw new Error("usage_archive_cutoff_required");
    return this.store.update((state) => {
      ensureBillingCollections(state);
      const cutoff = new Date(olderThan).getTime();
      if (Number.isNaN(cutoff)) throw new Error("usage_archive_cutoff_invalid");
      const candidates = (state.resourceUsageLogs || [])
        .filter((log) => new Date(log.createdAt || 0).getTime() < cutoff)
        .slice(0, Math.max(0, Number(limit || 0)));
      const archivedIds = new Set(candidates.map((log) => log.id));
      const archiveId = `usage-archive-${sourceEventId}`;
      const existing = state.resourceUsageArchive.find((entry) => entry.id === archiveId);
      if (!existing && candidates.length > 0) {
        state.resourceUsageArchive.push({
          id: archiveId,
          sourceEventId,
          archivedLogCount: candidates.length,
          logs: candidates.map(clone),
          createdAt: now()
        });
        state.resourceUsageCleanupTasks.push({
          id: `usage-cleanup-${sourceEventId}`,
          type: "resource_usage_archive",
          sourceEventId,
          archivedLogCount: candidates.length,
          createdAt: now()
        });
        state.resourceUsageLogs = state.resourceUsageLogs.filter((log) => !archivedIds.has(log.id));
      }
      return {
        archivedLogCount: candidates.length,
        remainingLogCount: state.resourceUsageLogs.length,
        archiveId
      };
    });
  }

  debitWorkspaceUsage({ state, account, workspace, packagePlan, hours, sourceEventId, billableHours: billedHours = billableHours(hours) }) {
    const entries = [];
    const workspaceId = workspace.id;
    const accountId = workspace.ownerAccountId;

    if (workspace.server.status === "running" && workspace.server.billingStatus === "active") {
      const requestedAmount = hourlyComputeAmount({ packagePlan, pricing: this.pricing, hours: billedHours });
      const charge = chargeAccount(account, "compute", requestedAmount);
      const metadata = {
        requestedHours: billedHours,
        ownerAccountId: accountId,
        ownerUserId: account.id,
        computeAllocationId: workspace.computeAllocationId || "",
        workspaceIds: [workspaceId],
        nodePoolId: workspace.server?.nodePoolId || "",
        cvmInstanceId: workspace.server?.cvmInstanceId || workspace.server?.instanceId || "",
        nodeName: workspace.server?.nodeName || "",
        privateIp: workspace.server?.privateIp || "",
        publicIp: workspace.server?.publicIp || "",
        ...computePriceSnapshot({ packagePlan, pricing: this.pricing })
      };
      this.appendDebitEntries({
        state,
        entries,
        workspaceId,
        accountId,
        type: "compute_debit",
        holdType: "compute",
        charge,
        sourceEventId,
        billableHours: billedHours,
        metadata,
        account
      });
      this.recordResourceUsage({
        state,
        account,
        workspace,
        resourceType: "compute",
        quantity: billedHours,
        unit: "hour",
        unitPrice: pricedComputeHourly({ packagePlan, pricing: this.pricing }),
        amount: charge.charged,
        requestedAmount,
        sourceEventId,
        metadata
      });
      if (charge.usedHold) {
        this.notify({
          state,
          accountId,
          workspaceId,
          type: "account.available_balance_exhausted",
          severity: "warning",
          message: "available_balance_exhausted_using_frozen_hold",
          sourceEventId
        });
      }
    }

    if (workspace.disk.status !== "destroyed" && workspace.disk.billingStatus === "active") {
      const requestedStorageAmount = hourlyStorageAmount({ packagePlan, pricing: this.pricing, hours: billedHours });
      const charge = chargeAccount(account, "storage", requestedStorageAmount);
      const metadata = {
        requestedHours: billedHours,
        ownerAccountId: accountId,
        ownerUserId: account.id,
        storageId: workspace.storageId || "",
        workspaceIds: [workspaceId],
        sizeGb: packagePlan.diskGb,
        ...storagePriceSnapshot({ pricing: this.pricing, sizeGb: packagePlan.diskGb })
      };
      this.appendDebitEntries({
        state,
        entries,
        workspaceId,
        accountId,
        type: "storage_debit",
        holdType: "storage",
        charge,
        sourceEventId,
        billableHours: billedHours,
        metadata,
        account
      });
      this.recordResourceUsage({
        state,
        account,
        workspace,
        resourceType: "storage",
        quantity: packagePlan.diskGb * billedHours,
        unit: "gb_hour",
        unitPrice: storageGbHourPrice(this.pricing),
        amount: charge.charged,
        requestedAmount: requestedStorageAmount,
        sourceEventId,
        metadata
      });
      if (charge.usedHold && !entries.some((entry) =>
        entry.type === "compute_debit" &&
        entry.sourceEventId === sourceEventId &&
        entry.metadata?.fundingSource === "compute_hold"
      )) {
        this.notify({
          state,
          accountId,
          workspaceId,
          type: "account.available_balance_exhausted",
          severity: "warning",
          message: "available_balance_exhausted_using_frozen_hold",
          sourceEventId
        });
      }
      if (charge.unpaid > 0 || charge.exhaustedHold) {
        workspace.state = workspace.server.status === "running" ? "storage_hold_exhausted" : "stopped_storage_hold_exhausted";
        workspace.disk.billingStatus = "hold_exhausted";
        workspace.updatedAt = now();
        this.notify({
          state,
          accountId,
          workspaceId,
          type: "workspace.storage_hold_exhausted",
          severity: "warning",
          message: "storage_hold_exhausted",
          sourceEventId
        });
      }
    }

    if (workspace.server.status === "running" && workspace.server.billingStatus === "active") {
      if (accountHold(account, "compute") <= 0) {
        const holdExhaustedEntry = this.ledgerEntry({ state,
          workspaceId,
          accountId,
          type: "compute_hold_exhausted",
          amount: 0,
          sourceEventId,
          holdType: "compute",
          metadata: { reason: "compute_hold_exhausted", requestedHours: billedHours }
        });
        entries.push(holdExhaustedEntry);
        state.billingLedger.push(holdExhaustedEntry);
        state.audit.push(this.auditEvent({ accountId, workspaceId, type: "compute.hold_exhausted", sourceEventId }));
        this.notify({
          state,
          accountId,
          workspaceId,
          type: "workspace.compute_hold_exhausted",
          severity: "warning",
          message: "compute_hold_exhausted",
          sourceEventId
        });
      }
    }

    return entries;
  }

  ensureHold({ state, account, accountId, workspaceId, holdType, requiredAmount, sourceEventId }) {
    const current = accountHold(account, holdType);
    if (current >= requiredAmount) return;
    const delta = money(requiredAmount - current);
    if (accountAvailable(account) < delta) throw new Error("insufficient_prepaid_hold_balance");
    addHold(account, holdType, delta);
    state.billingLedger.push(this.ledgerEntry({ state,
      workspaceId,
      accountId,
      type: holdType === "compute" ? "compute_hold" : "storage_hold",
      amount: delta,
      sourceEventId,
      holdType,
      metadata: { holdDays: 7 }
    }));
  }

  releaseHoldToLedger({ state, accountId, workspaceId, holdType, resourceId = "", sourceEventId }) {
    const account = ensureAccount(state, accountId);
    const balanceBefore = money(Number(account.balance || 0));
    const frozenBefore = money(Number(account.frozen || 0));
    const released = resourceId
      ? releaseResourceHold(account, holdType, resourceId)
      : releaseHold(account, holdType);
    if (released <= 0) return null;
    const resourceIds = holdType === "compute" ? { computeAllocationId: resourceId } : { storageId: resourceId };
    const entry = addResourceIds(this.ledgerEntry({ state,
      workspaceId,
      accountId,
      type: holdType === "compute" ? "compute_hold_released" : "storage_hold_released",
      amount: -released,
      sourceEventId,
      holdType
    }), resourceIds);
    state.billingLedger.push(entry);
    appendWalletTransaction(state, {
      user: account,
      accountId,
      workspaceId,
      type: entry.type,
      amount: 0,
      sourceEventId,
      ledgerEntryId: entry.id,
      balanceBefore,
      balanceAfter: account.balance,
      frozenBefore,
      frozenAfter: account.frozen,
      metadata: {
        ...resourceIds,
        holdAmount: released
      }
    });
    return entry;
  }

  assertBillingReconciliationAllowsProvisioning(state) {
    const guard = latestBillingReconciliationReport(state)?.guard;
    if (guard?.blockNewWorkspaces) {
      throw new Error(`billing_reconciliation_guard_blocked:${guard.reason}`);
    }
  }
}
