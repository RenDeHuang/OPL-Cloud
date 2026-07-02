
import { billingReconciliationGuard } from "../../../ledger/src/index.js";
import { clone, makeId, money, now } from "./core-utils.js";
import {
  accountAvailable,
  accountHold,
  accountSnapshotForState,
  addHold,
  appendWalletTransaction,
  chargeAccount,
  debitAvailableBalance,
  ensureAccount,
  ensureBillingCollections,
  ensureUserWallet,
  releaseHold,
  syncAccountWallet
} from "./wallet-service.js";
import { incrementRequestQuota, requestUsageFingerprint } from "./usage-billing-service.js";
import {
  billableHours,
  computeHourlyBase,
  hourlyComputeAmount,
  hourlyStorageAmount,
  pricedComputeHourly,
  pricingMarkup,
  storageGbHourPrice,
  storageGbMonthBase
} from "./pricing-service.js";
import { latestBillingReconciliationReport, latestWorkspaceForAccount } from "./workspace-service.js";
import { OplDomainService } from "./opl-domain-service.js";

export class BillingService extends OplDomainService {
  async creditAccount({ accountId, amount, reason, operatorUserId = "", operatorAccountId = "" }) {
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
      syncAccountWallet(state, account);
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
    let autoStopRequested = false;

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
      autoStopRequested = entries.some((entry) => entry.type === "compute_auto_stopped");
      if (entries.length > 0) {
        state.audit.push(this.auditEvent({ accountId, workspaceId, type: "billing.settled", sourceEventId }));
      }
      return {
        entries: entries.map(clone),
        account: accountSnapshotForState(state, accountId)
      };
    });
    if (autoStopRequested) {
      await this.stopRuntimeAfterHoldExhausted({ accountId, workspaceId, sourceEventId });
    }
    return {
      entries: settlement.entries,
      account: settlement.account
    };
  }

  async billingLedger(accountId) {
    const state = await this.store.read();
    return state.billingLedger.filter((entry) => entry.accountId === accountId).map(clone);
  }

  async recordRequestUsage({
    accountId = "",
    userId = "",
    workspaceId,
    requestId,
    provider,
    model,
    inputTokens = 0,
    outputTokens = 0,
    amount = 0,
    sourceEventId = ""
  }) {
    if (!workspaceId) throw new Error("workspace_required");
    if (!requestId) throw new Error("request_required");
    return this.store.update((state) => {
      ensureBillingCollections(state);
      const workspace = accountId
        ? latestWorkspaceForAccount(state, accountId, workspaceId)
        : state.workspaces[workspaceId];
      if (!workspace) throw new Error("workspace_not_found");
      const resolvedAccountId = accountId || workspace.ownerAccountId;
      const eventId = sourceEventId || `gateway_request:${requestId}`;
      const requestedAmount = money(Math.max(0, Number(amount || 0)));
      const normalizedInputTokens = Number(inputTokens || 0);
      const normalizedOutputTokens = Number(outputTokens || 0);
      const fingerprint = requestUsageFingerprint({
        provider,
        model,
        inputTokens: normalizedInputTokens,
        outputTokens: normalizedOutputTokens,
        requestedAmount,
        sourceEventId: eventId
      });
      const existing = state.requestUsageLogs.find((log) =>
        log.workspaceId === workspaceId &&
        (log.sourceEventId === eventId || log.requestId === requestId)
      );
      const existingDedup = state.requestUsageDedup.find((dedup) =>
        dedup.workspaceId === workspaceId &&
        (dedup.sourceEventId === eventId || dedup.requestId === requestId)
      );
      const existingFingerprint = existing?.requestFingerprint || existingDedup?.requestFingerprint;
      if (existingFingerprint && existingFingerprint !== fingerprint) {
        throw new Error("request_usage_fingerprint_conflict");
      }
      if (existing) return clone(existing);
      if (existingDedup?.usageLogId) {
        const existingLog = state.requestUsageLogs.find((log) => log.id === existingDedup.usageLogId);
        if (existingLog) return clone(existingLog);
      }

      const user = ensureUserWallet(state, {
        accountId: resolvedAccountId,
        userId: userId || workspace.ownerUserId || workspace.owner?.userId
      });
      const quota = incrementRequestQuota(user, 1);
      const balanceBefore = money(Number(user.balance || 0));
      const frozenBefore = money(Number(user.frozen || 0));
      const charged = debitAvailableBalance(user, requestedAmount);
      syncAccountWallet(state, user);
      const logId = makeId("usage-request", resolvedAccountId, workspaceId, requestId, eventId, String(state.requestUsageLogs.length));
      let ledgerEntry = null;
      if (charged > 0) {
        ledgerEntry = this.ledgerEntry({ state,
          workspaceId,
          accountId: resolvedAccountId,
          type: "request_debit",
          amount: -charged,
          sourceEventId: eventId,
          metadata: {
            requestId,
            provider,
            model,
            inputTokens: normalizedInputTokens,
            outputTokens: normalizedOutputTokens,
            requestedAmount,
            fundingSource: "available_balance",
            requestFingerprint: fingerprint,
            usageLogId: logId
          }
        });
        state.billingLedger.push(ledgerEntry);
      }
      const log = {
        id: logId,
        userId: user.id,
        accountId: resolvedAccountId,
        workspaceId,
        requestId,
        provider,
        model,
        inputTokens: normalizedInputTokens,
        outputTokens: normalizedOutputTokens,
        amount: charged,
        requestedAmount,
        unpaid: money(requestedAmount - charged),
        currency: "CNY",
        sourceEventId: eventId,
        requestFingerprint: fingerprint,
        ...(ledgerEntry ? { ledgerEntryId: ledgerEntry.id } : {}),
        ...(quota ? { quota } : {}),
        createdAt: now()
      };
      state.requestUsageLogs.push(log);
      if (charged > 0) {
        appendWalletTransaction(state, {
          user,
          accountId: resolvedAccountId,
          workspaceId,
          type: "request_debit",
          amount: -charged,
          sourceEventId: eventId,
          ledgerEntryId: ledgerEntry.id,
          usageLogId: log.id,
          fundingSource: "available_balance",
          balanceBefore,
          balanceAfter: user.balance,
          frozenBefore,
          frozenAfter: user.frozen,
          metadata: {
            requestId,
            provider,
            model,
            requestFingerprint: fingerprint
          }
        });
      }
      const dedup = {
        id: makeId("dedup", resolvedAccountId, workspaceId, eventId, requestId),
        userId: user.id,
        accountId: resolvedAccountId,
        workspaceId,
        requestId,
        sourceEventId: eventId,
        requestFingerprint: fingerprint,
        usageLogId: log.id,
        createdAt: now()
      };
      state.requestUsageDedup.push(dedup);
      state.audit.push(this.auditEvent({
        accountId: resolvedAccountId,
        workspaceId,
        type: "billing.request_usage_recorded",
        sourceEventId: eventId
      }));
      return clone(log);
    });
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
    const settlementTypes = new Set(["compute_debit", "storage_debit", "compute_auto_stopped"]);
    return state.billingLedger.filter((entry) =>
      entry.accountId === accountId &&
      entry.workspaceId === workspaceId &&
      entry.sourceEventId === sourceEventId &&
      settlementTypes.has(entry.type)
    );
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

  appendDebitEntries({ state, entries, workspaceId, accountId, type, holdType, charge, sourceEventId, billableHours, metadata }) {
    const debits = [
      { amount: charge.available, fundingSource: "available_balance" },
      { amount: charge.hold, fundingSource: `${holdType}_hold` }
    ];
    for (const debit of debits) {
      if (debit.amount <= 0) continue;
      const entry = this.ledgerEntry({ state,
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
      });
      entries.push(entry);
      state.billingLedger.push(entry);
    }
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
        baseHourly: computeHourlyBase({ packagePlan, pricing: this.pricing }),
        markup: pricingMarkup(this.pricing)
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
        metadata
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
        diskGb: packagePlan.diskGb,
        baseGbMonth: storageGbMonthBase(this.pricing),
        markup: pricingMarkup(this.pricing)
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
        metadata
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
        const autoStopEntry = this.ledgerEntry({ state,
          workspaceId,
          accountId,
          type: "compute_auto_stopped",
          amount: 0,
          sourceEventId,
          holdType: "compute",
          metadata: { reason: "compute_hold_exhausted", requestedHours: billedHours }
        });
        entries.push(autoStopEntry);
        state.billingLedger.push(autoStopEntry);
        state.audit.push(this.auditEvent({ accountId, workspaceId, type: "compute.auto_stop_requested", sourceEventId }));
        this.notify({
          state,
          accountId,
          workspaceId,
          type: "workspace.compute_auto_stopped",
          severity: "warning",
          message: "compute_hold_exhausted",
          sourceEventId
        });
      }
    }

    syncAccountWallet(state, account);
    return entries;
  }

  ensureHold({ state, account, accountId, workspaceId, holdType, requiredAmount, sourceEventId }) {
    const current = accountHold(account, holdType);
    if (current >= requiredAmount) return;
    const delta = money(requiredAmount - current);
    if (accountAvailable(account) < delta) throw new Error("insufficient_prepaid_hold_balance");
    addHold(account, holdType, delta);
    syncAccountWallet(state, account);
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

  releaseHoldToLedger({ state, accountId, workspaceId, holdType, sourceEventId }) {
    const account = ensureAccount(state, accountId);
    const released = releaseHold(account, holdType);
    syncAccountWallet(state, account);
    if (released <= 0) return null;
    const entry = this.ledgerEntry({ state,
      workspaceId,
      accountId,
      type: holdType === "compute" ? "compute_hold_released" : "storage_hold_released",
      amount: -released,
      sourceEventId,
      holdType
    });
    state.billingLedger.push(entry);
    return entry;
  }

  assertBillingReconciliationAllowsProvisioning(state) {
    const guard = latestBillingReconciliationReport(state)?.guard;
    if (guard?.blockNewWorkspaces) {
      throw new Error(`billing_reconciliation_guard_blocked:${guard.reason}`);
    }
  }
}
