import {
  addMembershipRecord,
  createOrganizationRecord,
  createUserRecord,
  managementSnapshot,
  resolveWorkspaceOwner
} from "./management-model.js";
import { appendEvidenceReceipt, createEvidenceReceipt } from "../../ledger/src/evidence-ledger.js";

const PACKAGES = {
  basic: {
    id: "basic",
    name: "Basic Workspace",
    accelerator: "cpu",
    cpu: 2,
    memoryGb: 4,
    gpu: 0,
    server: "2c4g",
    diskGb: 10
  },
  pro: {
    id: "pro",
    name: "Pro Workspace",
    accelerator: "cpu",
    cpu: 8,
    memoryGb: 16,
    gpu: 0,
    server: "8c16g",
    diskGb: 100
  }
};

function now() {
  return new Date().toISOString();
}

function stableHash(input) {
  let hash = 0;
  for (const char of input) {
    hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  }
  return hash.toString(36).padStart(6, "0");
}

function makeId(prefix, ...parts) {
  return `${prefix}-${stableHash(parts.join(":"))}`;
}

function makeToken(workspaceId, sequence = "initial") {
  return `share_${stableHash(`${workspaceId}:${sequence}`)}${stableHash(`${sequence}:${workspaceId}`).slice(0, 6)}`;
}

function money(value) {
  return Number(value.toFixed(4));
}

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

function getPackage(packageId) {
  const packagePlan = PACKAGES[packageId];
  if (!packagePlan) throw new Error("unknown_package");
  return packagePlan;
}

function ensureAccount(state, accountId) {
  state.accounts[accountId] ??= {
    id: accountId,
    balance: 0,
    frozen: 0,
    createdAt: now()
  };
  return state.accounts[accountId];
}

function accountAvailable(account) {
  return money(account.balance - account.frozen);
}

function accountHold(account, holdType) {
  account.holds ??= {};
  account.holds[holdType] = money(Number(account.holds[holdType] || 0));
  account.frozen = money(Object.values(account.holds).reduce((total, amount) => total + Number(amount || 0), 0));
  return account.holds[holdType];
}

function addHold(account, holdType, amount) {
  const current = accountHold(account, holdType);
  account.holds[holdType] = money(current + amount);
  account.frozen = money(account.frozen + amount);
}

function releaseHold(account, holdType, amount = accountHold(account, holdType)) {
  const current = accountHold(account, holdType);
  const released = money(Math.min(current, Math.max(0, Number(amount || 0))));
  if (released <= 0) return 0;
  account.holds[holdType] = money(current - released);
  account.frozen = money(account.frozen - released);
  return released;
}

function debitAccount(account, holdType, amount) {
  const debit = money(Math.max(0, Number(amount || 0)));
  if (debit <= 0) return 0;
  const currentHold = accountHold(account, holdType);
  const captured = money(Math.min(currentHold, debit));
  if (captured <= 0) return 0;
  account.holds[holdType] = money(currentHold - captured);
  account.frozen = money(Math.max(0, account.frozen - captured));
  account.balance = money(account.balance - captured);
  return captured;
}

function debitAvailableBalance(account, amount) {
  const debit = money(Math.max(0, Number(amount || 0)));
  if (debit <= 0) return 0;
  const captured = money(Math.min(accountAvailable(account), debit));
  if (captured <= 0) return 0;
  account.balance = money(account.balance - captured);
  return captured;
}

function chargeAccount(account, holdType, amount) {
  const requested = money(Math.max(0, Number(amount || 0)));
  const available = debitAvailableBalance(account, requested);
  const remainingAfterAvailable = money(requested - available);
  const hold = debitAccount(account, holdType, remainingAfterAvailable);
  return {
    requested,
    available,
    hold,
    charged: money(available + hold),
    unpaid: money(requested - available - hold),
    usedHold: hold > 0,
    exhaustedHold: hold > 0 && accountHold(account, holdType) <= 0
  };
}

function latestWorkspaceForAccount(state, accountId, workspaceId) {
  const workspace = state.workspaces[workspaceId];
  if (!workspace || workspace.ownerAccountId !== accountId) {
    throw new Error("workspace_not_found");
  }
  return workspace;
}

function workspaceBySlug(state, slug) {
  return Object.values(state.workspaces).find((workspace) => workspace.slug === slug);
}

export function storageHoldAmount({ packagePlan, pricing }) {
  return packageHoldAmount({ packagePlan, pricing }).storage;
}

function pricingMarkup(pricing) {
  return pricing.markup ?? 0.2;
}

function computeHourlyBase({ packagePlan, pricing }) {
  return pricing.computeHourly?.[packagePlan.id] ?? pricing.serverHourly?.[packagePlan.id] ?? 0;
}

function storageGbMonthBase(pricing) {
  return pricing.storageGbMonth ?? pricing.diskGbMonth ?? 0.2;
}

function pricedComputeHourly({ packagePlan, pricing }) {
  return money(computeHourlyBase({ packagePlan, pricing }) * (1 + pricingMarkup(pricing)));
}

function pricedStorageGbMonth(pricing) {
  return money(storageGbMonthBase(pricing) * (1 + pricingMarkup(pricing)));
}

export function packageHoldAmount({ packagePlan, pricing }) {
  const compute = money(pricedComputeHourly({ packagePlan, pricing }) * 24 * 7);
  const storage = money((packagePlan.diskGb * pricedStorageGbMonth(pricing) / 30) * 7);
  return {
    compute,
    storage,
    total: money(compute + storage)
  };
}

function hourlyStorageAmount({ packagePlan, pricing, hours }) {
  const gbMonth = storageGbMonthBase(pricing);
  const markup = pricingMarkup(pricing);
  return money((packagePlan.diskGb * gbMonth * (1 + markup) / 30 / 24) * hours);
}

function hourlyComputeAmount({ packagePlan, pricing, hours }) {
  const hourly = computeHourlyBase({ packagePlan, pricing });
  const markup = pricingMarkup(pricing);
  return money(hourly * (1 + markup) * hours);
}

function billableHours(hours) {
  const value = Number(hours);
  if (!Number.isFinite(value) || value <= 0) throw new Error("positive_hours_required");
  return Math.ceil(value);
}

function billingPolicy(pricing) {
  return {
    currency: "CNY",
    markup: pricingMarkup(pricing),
    prepaidHoldDays: 7,
    minimumBillableHours: 1,
    billingCadence: "hourly",
    fundingOrder: ["available_balance", "frozen_hold"],
    computeHoldExhaustion: "stop_compute",
    storageHoldExhaustion: "freeze_workspace_until_top_up_or_storage_destroy",
    storageDestroyConfirmation: "required"
  };
}

function storageDestroyed(workspace) {
  return workspace?.state === "destroyed" || workspace?.disk?.status === "destroyed";
}

function defaultStorageBackupPolicy() {
  return {
    name: "daily_7_weekly_4",
    retainDaily: 7,
    retainWeekly: 4,
    retainLast: 11
  };
}

function backupRetentionPolicy(inputPolicy = null) {
  return {
    ...defaultStorageBackupPolicy(),
    ...(inputPolicy || {})
  };
}

function latestStorageBackupForAccount(state, accountId, backupId) {
  const backup = (state.storageBackups || []).find((item) => item.id === backupId && item.accountId === accountId);
  if (!backup) throw new Error("storage_backup_not_found");
  return backup;
}

export function createOplCloud({ store, runtimeProvider, pricing, productionReadiness = null }) {
  return new OplCloudService({ store, runtimeProvider, pricing, productionReadiness });
}

export class OplCloudService {
  constructor({ store, runtimeProvider, pricing, productionReadiness = null }) {
    this.store = store;
    this.runtimeProvider = runtimeProvider;
    this.pricing = pricing;
    this.productionReadinessCheck = productionReadiness;
    this.runtimeOperationSequence = 0;
  }

  packages() {
    return Object.values(PACKAGES).map((plan) => ({
      ...clone(plan),
      price: {
        currency: "CNY",
        computeHourly: pricedComputeHourly({ packagePlan: plan, pricing: this.pricing }),
        storageGbMonth: pricedStorageGbMonth(this.pricing),
        markup: pricingMarkup(this.pricing),
        source: "tencent_price_catalog_snapshot"
      }
    }));
  }

  async creditAccount({ accountId, amount, reason }) {
    if (!accountId) throw new Error("account_required");
    const credit = Number(amount);
    if (!Number.isFinite(credit) || credit <= 0) throw new Error("positive_credit_required");

    return this.store.update((state) => {
      const account = ensureAccount(state, accountId);
      account.balance = money(account.balance + credit);
      const entry = this.ledgerEntry({ state,
        workspaceId: "account",
        accountId,
        type: "credit",
        amount: credit,
        sourceEventId: reason || "owner_credit"
      });
      state.billingLedger.push(entry);
      state.audit.push(this.auditEvent({ accountId, type: "account.credit_granted", sourceEventId: entry.id }));
      return clone(account);
    });
  }

  async createOrganization(input) {
    return this.store.update((state) => {
      const organization = createOrganizationRecord(state, input);
      ensureAccount(state, organization.billingAccountId);
      state.audit.push(this.auditEvent({
        accountId: organization.billingAccountId,
        type: "organization.created",
        sourceEventId: organization.id
      }));
      return organization;
    });
  }

  async createUser(input) {
    return this.store.update((state) => {
      const user = createUserRecord(state, input);
      state.audit.push(this.auditEvent({
        accountId: "management",
        type: "user.created",
        sourceEventId: user.id
      }));
      return user;
    });
  }

  async addOrganizationMember(input) {
    return this.store.update((state) => {
      const membership = addMembershipRecord(state, input);
      const organization = state.organizations[membership.organizationId];
      state.audit.push(this.auditEvent({
        accountId: organization.billingAccountId,
        type: "organization.member_added",
        sourceEventId: membership.id
      }));
      return membership;
    });
  }

  async managementState({ organizationId }) {
    const state = await this.store.read();
    const organization = state.organizations?.[organizationId];
    if (!organization) throw new Error("organization_not_found");
    const billingAccount = state.accounts[organization.billingAccountId] ?? {
      id: organization.billingAccountId,
      balance: 0,
      frozen: 0,
      holds: {}
    };
    const workspaces = Object.values(state.workspaces)
      .filter((workspace) => workspace.owner?.organizationId === organizationId || workspace.ownerAccountId === organization.billingAccountId);
    return managementSnapshot(state, {
      organizationId,
      packages: this.packages(),
      account: billingAccount,
      workspaces
    });
  }

  async createWorkspace({ accountId, organizationId, userId, workspaceName, packageId }) {
    const packagePlan = getPackage(packageId);
    const hold = packageHoldAmount({ packagePlan, pricing: this.pricing });
    let workspaceId = null;
    let token = null;
    let owner = null;

    const reservation = await this.store.update((state) => {
      const resolvedOwner = resolveWorkspaceOwner(state, { accountId, organizationId, userId });
      accountId = resolvedOwner.accountId;
      owner = resolvedOwner.owner;
      workspaceId = makeId("ws", accountId, workspaceName, packageId);
      token = makeToken(workspaceId);
      const account = ensureAccount(state, accountId);
      if (state.workspaces[workspaceId]) return { existing: true, workspace: clone(state.workspaces[workspaceId]) };
      if (accountAvailable(account) < hold.total) {
        throw new Error("insufficient_prepaid_hold_balance");
      }

      addHold(account, "compute", hold.compute);
      addHold(account, "storage", hold.storage);
      state.billingLedger.push(this.ledgerEntry({ state,
        workspaceId,
        accountId,
        type: "compute_hold",
        amount: hold.compute,
        sourceEventId: "open_workspace",
        holdType: "compute",
        metadata: {
          holdDays: 7,
          baseHourly: computeHourlyBase({ packagePlan, pricing: this.pricing }),
          markup: pricingMarkup(this.pricing)
        }
      }));
      state.billingLedger.push(this.ledgerEntry({ state,
        workspaceId,
        accountId,
        type: "storage_hold",
        amount: hold.storage,
        sourceEventId: "open_workspace",
        holdType: "storage",
        metadata: {
          holdDays: 7,
          baseGbMonth: storageGbMonthBase(this.pricing),
          markup: pricingMarkup(this.pricing)
        }
      }));

      const operation = this.startRuntimeOperation({ state, accountId, workspaceId, operationType: "create_workspace" });
      return { existing: false, operationId: operation.id };
    });

    if (reservation.existing) return reservation.workspace;

    let runtime;
    try {
      runtime = await this.runtimeProvider.createWorkspaceRuntime({
        workspaceId,
        ownerAccountId: accountId,
        workspaceName,
        packagePlan,
        token
      });
    } catch (error) {
      await this.recordCreateWorkspaceFailure({ accountId, workspaceId, operationId: reservation.operationId, error });
      throw error;
    }

    return this.store.update((state) => {
      const account = ensureAccount(state, accountId);
      const operation = state.runtimeOperations.find((item) => item.id === reservation.operationId);
      if (operation) this.finishRuntimeOperation(operation, "succeeded");

      const workspace = {
        id: workspaceId,
        ownerAccountId: accountId,
        owner,
        name: workspaceName,
        packageId,
        state: "running",
        provider: runtime.provider,
        server: runtime.server,
        docker: runtime.docker,
        disk: runtime.disk,
        slug: runtime.slug,
        url: runtime.url,
        access: {
          mode: "long_lived_url_token",
          requiresLogin: false,
          token,
          tokenStatus: "active",
          rotationPolicy: "reset_or_delete_on_leak"
        },
        billing: {
          holdPolicy: "seven_day_prepaid",
          minimumBillableHours: 1,
          priceMarkup: pricingMarkup(this.pricing)
        },
        createdAt: now(),
        updatedAt: now()
      };
      state.workspaces[workspaceId] = workspace;
      const firstHourEntries = this.debitWorkspaceUsage({
        state,
        account,
        workspace,
        packagePlan,
        hours: 1,
        sourceEventId: "open_workspace_initial_hour",
        billableHours: 1
      });
      state.audit.push(this.auditEvent({ accountId, workspaceId, type: "workspace.created", sourceEventId: workspaceId }));
      state.audit.push(this.auditEvent({
        accountId,
        workspaceId,
        type: "billing.first_hour_charged",
        sourceEventId: "open_workspace_initial_hour"
      }));
      this.recordEvidence({
        state,
        type: "workspace.created",
        accountId,
        workspace,
        packagePlan,
        billingRefs: [
          ...state.billingLedger.filter((entry) =>
            entry.accountId === accountId &&
            entry.workspaceId === workspaceId &&
            ["compute_hold", "storage_hold"].includes(entry.type)
          ),
          ...firstHourEntries
        ],
        continuation: {
          action: "open_workspace_url",
          uri: workspace.url
        }
      });
      return {
        ...clone(workspace),
        initialBilling: firstHourEntries.map(clone)
      };
    });
  }

  async createStorageBackup({ accountId, workspaceId, reason = "manual", retentionPolicy = null }) {
    if (typeof this.runtimeProvider.createStorageBackup !== "function") throw new Error("storage_backup_unsupported");
    return this.runRuntimeOperation({
      accountId,
      workspaceId,
      operationType: "create_storage_backup",
      mutate: async (state, workspace, operation) => {
        if (storageDestroyed(workspace)) throw new Error("workspace_storage_destroyed");
        state.storageBackups ??= [];
        const policy = backupRetentionPolicy(retentionPolicy);
        const backupId = makeId("backup", accountId, workspaceId, reason, String(Date.now()), String(state.storageBackups.length));
        const providerBackup = await this.runtimeProvider.createStorageBackup({
          workspace: clone(workspace),
          backupId,
          retentionPolicy: policy
        });
        const backup = {
          ...providerBackup,
          id: backupId,
          accountId,
          workspaceId,
          status: providerBackup.status || "available",
          retentionPolicy: policy,
          reason,
          createdAt: now(),
          updatedAt: now()
        };
        state.storageBackups.push(backup);
        this.finishRuntimeOperation(operation, "succeeded");
        state.audit.push(this.auditEvent({ accountId, workspaceId, type: "storage.backup_created", sourceEventId: backupId }));
        this.recordEvidence({
          state,
          type: "workspace.storage_backup_created",
          accountId,
          workspace,
          continuation: { action: "restore_workspace_from_backup", backupId }
        });
        return clone(backup);
      }
    });
  }

  async restoreWorkspaceFromBackup({ accountId, backupId, workspaceName, packageId }) {
    if (typeof this.runtimeProvider.createWorkspaceRuntime !== "function") throw new Error("runtime_provider_missing_create");
    const packagePlan = getPackage(packageId);
    const hold = packageHoldAmount({ packagePlan, pricing: this.pricing });
    let workspaceId = null;
    let token = null;
    let backupSnapshot = null;

    const reservation = await this.store.update((state) => {
      const backup = latestStorageBackupForAccount(state, accountId, backupId);
      if (backup.status !== "available") throw new Error("storage_backup_not_available");
      backupSnapshot = clone(backup);
      workspaceId = makeId("ws", accountId, workspaceName, packageId, backupId);
      token = makeToken(workspaceId);
      const account = ensureAccount(state, accountId);
      if (state.workspaces[workspaceId]) return { existing: true, workspace: clone(state.workspaces[workspaceId]) };
      if (accountAvailable(account) < hold.total) {
        throw new Error("insufficient_prepaid_hold_balance");
      }

      addHold(account, "compute", hold.compute);
      addHold(account, "storage", hold.storage);
      state.billingLedger.push(this.ledgerEntry({ state,
        workspaceId,
        accountId,
        type: "compute_hold",
        amount: hold.compute,
        sourceEventId: "restore_workspace_from_backup",
        holdType: "compute",
        metadata: {
          holdDays: 7,
          backupId,
          baseHourly: computeHourlyBase({ packagePlan, pricing: this.pricing }),
          markup: pricingMarkup(this.pricing)
        }
      }));
      state.billingLedger.push(this.ledgerEntry({ state,
        workspaceId,
        accountId,
        type: "storage_hold",
        amount: hold.storage,
        sourceEventId: "restore_workspace_from_backup",
        holdType: "storage",
        metadata: {
          holdDays: 7,
          backupId,
          baseGbMonth: storageGbMonthBase(this.pricing),
          markup: pricingMarkup(this.pricing)
        }
      }));
      const operation = this.startRuntimeOperation({ state, accountId, workspaceId, operationType: "restore_workspace_from_backup" });
      return { existing: false, operationId: operation.id };
    });

    if (reservation.existing) return reservation.workspace;

    let runtime;
    try {
      runtime = await this.runtimeProvider.createWorkspaceRuntime({
        workspaceId,
        ownerAccountId: accountId,
        workspaceName,
        packagePlan,
        token,
        restoreFromBackup: backupSnapshot
      });
    } catch (error) {
      await this.recordCreateWorkspaceFailure({ accountId, workspaceId, operationId: reservation.operationId, error });
      throw error;
    }

    return this.store.update((state) => {
      const account = ensureAccount(state, accountId);
      const backup = latestStorageBackupForAccount(state, accountId, backupId);
      const operation = state.runtimeOperations.find((item) => item.id === reservation.operationId);
      if (operation) this.finishRuntimeOperation(operation, "succeeded");

      const workspace = {
        id: workspaceId,
        ownerAccountId: accountId,
        name: workspaceName,
        packageId,
        state: "running",
        provider: runtime.provider,
        server: runtime.server,
        docker: runtime.docker,
        disk: {
          ...runtime.disk,
          restoredFromBackupId: backupId
        },
        slug: runtime.slug,
        url: runtime.url,
        restoredFromBackupId: backupId,
        storageRestore: {
          backupId,
          sourceWorkspaceId: backup.workspaceId,
          restoredAt: now()
        },
        access: {
          mode: "long_lived_url_token",
          requiresLogin: false,
          token,
          tokenStatus: "active",
          rotationPolicy: "reset_or_delete_on_leak"
        },
        billing: {
          holdPolicy: "seven_day_prepaid",
          minimumBillableHours: 1,
          priceMarkup: pricingMarkup(this.pricing)
        },
        createdAt: now(),
        updatedAt: now()
      };
      state.workspaces[workspaceId] = workspace;
      backup.restoreCount = Number(backup.restoreCount || 0) + 1;
      backup.lastRestoredAt = workspace.storageRestore.restoredAt;
      backup.restoredWorkspaceIds = [...new Set([...(backup.restoredWorkspaceIds || []), workspaceId])];
      backup.updatedAt = now();
      const firstHourEntries = this.debitWorkspaceUsage({
        state,
        account,
        workspace,
        packagePlan,
        hours: 1,
        sourceEventId: "restore_workspace_initial_hour",
        billableHours: 1
      });
      state.audit.push(this.auditEvent({ accountId, workspaceId, type: "workspace.restored_from_backup", sourceEventId: backupId }));
      this.recordEvidence({
        state,
        type: "workspace.storage_restored",
        accountId,
        workspace,
        packagePlan,
        billingRefs: firstHourEntries,
        continuation: {
          action: "open_workspace_url",
          uri: workspace.url,
          backupId
        }
      });
      return {
        ...clone(workspace),
        initialBilling: firstHourEntries.map(clone)
      };
    });
  }

  async pruneStorageBackups({ accountId, workspaceId }) {
    if (typeof this.runtimeProvider.deleteStorageBackup !== "function") throw new Error("storage_backup_delete_unsupported");
    const prunePlan = await this.store.update((state) => {
      latestWorkspaceForAccount(state, accountId, workspaceId);
      state.storageBackups ??= [];
      const available = state.storageBackups
        .map((backup, index) => ({ backup, index }))
        .filter(({ backup }) => backup.accountId === accountId && backup.workspaceId === workspaceId && backup.status === "available")
        .sort((a, b) => a.index - b.index)
        .map(({ backup }) => backup);
      const retainLast = Math.max(1, Number(available.at(-1)?.retentionPolicy?.retainLast || defaultStorageBackupPolicy().retainLast));
      const deletable = available.slice(0, Math.max(0, available.length - retainLast));
      for (const backup of deletable) {
        backup.status = "deleting";
        backup.updatedAt = now();
      }
      return deletable.map(clone);
    });

    const deletedBackupIds = [];
    for (const backup of prunePlan) {
      try {
        await this.runtimeProvider.deleteStorageBackup({ backup });
        await this.store.update((state) => {
          const current = latestStorageBackupForAccount(state, accountId, backup.id);
          current.status = "deleted";
          current.deletedAt = now();
          current.updatedAt = now();
          state.audit.push(this.auditEvent({ accountId, workspaceId, type: "storage.backup_deleted", sourceEventId: backup.id }));
          return true;
        });
        deletedBackupIds.push(backup.id);
      } catch (error) {
        await this.store.update((state) => {
          const current = latestStorageBackupForAccount(state, accountId, backup.id);
          current.status = "delete_failed";
          current.error = error.message;
          current.updatedAt = now();
          this.notify({
            state,
            accountId,
            workspaceId,
            type: "storage.backup_delete_failed",
            severity: "error",
            message: error.message,
            sourceEventId: backup.id
          });
          return true;
        });
        throw error;
      }
    }

    return { deletedBackupIds };
  }

  async stopServer({ accountId, workspaceId, confirm }) {
    if (confirm !== true) throw new Error("server_stop_confirmation_required");
    return this.runRuntimeOperation({
      accountId,
      workspaceId,
      operationType: "stop_server",
      mutate: async (state, workspace, operation) => {
        workspace.state = "stopping_server";
        workspace.server = await this.runtimeProvider.stopServer({ workspace: clone(workspace) });
        this.finishRuntimeOperation(operation, "succeeded");
        workspace.state = workspace.disk.billingStatus === "hold_exhausted"
          ? "stopped_storage_hold_exhausted"
          : "stopped_server_disk_retained";
        workspace.disk.status = workspace.disk.status === "destroyed" ? "destroyed" : "attached_retained";
        workspace.updatedAt = now();
        state.billingLedger.push(this.ledgerEntry({ state,
          workspaceId,
          accountId,
          type: "server_billing_stopped",
          amount: 0,
          sourceEventId: "stop_server"
        }));
        this.releaseHoldToLedger({ state, accountId, workspaceId, holdType: "compute", sourceEventId: "stop_server" });
        state.audit.push(this.auditEvent({ accountId, workspaceId, type: "server.stopped", sourceEventId: "stop_server" }));
        this.recordEvidence({
          state,
          type: "workspace.compute_stopped",
          accountId,
          workspace,
          continuation: { action: "restart_workspace_compute" }
        });
        return clone(workspace);
      }
    });
  }

  async restartServer({ accountId, workspaceId }) {
    const operationType = await this.restartOperationType({ accountId, workspaceId });
    return this.runRuntimeOperation({
      accountId,
      workspaceId,
      operationType,
      prepare: (state, workspace) => {
        const packagePlan = getPackage(workspace.packageId);
        const account = ensureAccount(state, accountId);
        const requiredHold = packageHoldAmount({ packagePlan, pricing: this.pricing });
        this.ensureHold({ state, account, accountId, workspaceId, holdType: "compute", requiredAmount: requiredHold.compute, sourceEventId: "resume_workspace" });
        this.ensureHold({ state, account, accountId, workspaceId, holdType: "storage", requiredAmount: requiredHold.storage, sourceEventId: "resume_workspace" });
      },
      mutate: async (state, workspace, operation) => {
        const recreate = workspace.server.status === "destroyed" || workspace.state === "server_destroyed_disk_retained";
        workspace.state = recreate ? "recreating_server" : "restarting_server";
        workspace.server = recreate
          ? await this.runtimeProvider.recreateServer({ workspace: clone(workspace) })
          : await this.runtimeProvider.restartServer({ workspace: clone(workspace) });
        this.finishRuntimeOperation(operation, "succeeded");
        workspace.docker.status = "running";
        workspace.disk.status = "attached_retained";
        workspace.disk.billingStatus = "active";
        workspace.state = "running";
        workspace.updatedAt = now();
        this.debitWorkspaceUsage({
          state,
          account: ensureAccount(state, accountId),
          workspace,
          packagePlan: getPackage(workspace.packageId),
          hours: 1,
          sourceEventId: "resume_workspace_initial_hour",
          billableHours: 1
        });
        state.audit.push(this.auditEvent({
          accountId,
          workspaceId,
          type: recreate ? "server.recreated" : "server.restarted",
          sourceEventId: operationType
        }));
        this.recordEvidence({
          state,
          type: recreate ? "workspace.compute_recreated" : "workspace.compute_restarted",
          accountId,
          workspace,
          continuation: { action: "open_workspace_url", uri: workspace.url }
        });
        return clone(workspace);
      }
    });
  }

  async restartOperationType({ accountId, workspaceId }) {
    const state = await this.store.read();
    const workspace = latestWorkspaceForAccount(state, accountId, workspaceId);
    return workspace.server.status === "destroyed" || workspace.state === "server_destroyed_disk_retained"
      ? "recreate_server"
      : "restart_server";
  }

  async destroyServer({ accountId, workspaceId, confirm }) {
    if (confirm !== true) throw new Error("server_destroy_confirmation_required");
    return this.runRuntimeOperation({
      accountId,
      workspaceId,
      operationType: "destroy_server",
      mutate: async (state, workspace, operation) => {
        workspace.state = "destroying_server";
        workspace.server = await this.runtimeProvider.destroyServer({ workspace: clone(workspace) });
        this.finishRuntimeOperation(operation, "succeeded");
        workspace.docker.status = "destroyed";
        workspace.disk.status = workspace.disk.status === "destroyed" ? "destroyed" : "detached_retained";
        workspace.state = workspace.disk.status === "destroyed" ? "destroyed" : "server_destroyed_disk_retained";
        workspace.updatedAt = now();
        state.billingLedger.push(this.ledgerEntry({ state,
          workspaceId,
          accountId,
          type: "server_destroyed",
          amount: 0,
          sourceEventId: "destroy_server"
        }));
        this.releaseHoldToLedger({ state, accountId, workspaceId, holdType: "compute", sourceEventId: "destroy_server" });
        state.audit.push(this.auditEvent({ accountId, workspaceId, type: "server.destroyed", sourceEventId: "destroy_server" }));
        this.recordEvidence({
          state,
          type: "workspace.compute_destroyed",
          accountId,
          workspace,
          continuation: { action: "restart_workspace_compute_from_retained_storage" }
        });
        return clone(workspace);
      }
    });
  }

  async destroyDisk({ accountId, workspaceId, confirmDataLoss }) {
    if (confirmDataLoss !== true) throw new Error("disk_destroy_confirmation_required");
    return this.runRuntimeOperation({
      accountId,
      workspaceId,
      operationType: "destroy_disk",
      mutate: async (state, workspace, operation) => {
        workspace.state = "destroying_disk";
        if (workspace.server.status !== "destroyed") {
          workspace.server = await this.runtimeProvider.destroyServer({ workspace: clone(workspace) });
          workspace.docker.status = "destroyed";
          workspace.disk.status = workspace.disk.status === "destroyed" ? "destroyed" : "detached_retained";
        }
        workspace.disk = await this.runtimeProvider.destroyDisk({ workspace: clone(workspace) });
        this.finishRuntimeOperation(operation, "succeeded");
        workspace.server.status = "destroyed";
        workspace.server.billingStatus = "stopped";
        workspace.docker.status = "destroyed";
        workspace.access.tokenStatus = "unavailable";
        workspace.state = "destroyed";
        workspace.updatedAt = now();
        state.billingLedger.push(this.ledgerEntry({ state,
          workspaceId,
          accountId,
          type: "storage_destroyed",
          amount: 0,
          sourceEventId: "destroy_disk"
        }));
        this.releaseHoldToLedger({ state, accountId, workspaceId, holdType: "compute", sourceEventId: "destroy_disk" });
        this.releaseHoldToLedger({ state, accountId, workspaceId, holdType: "storage", sourceEventId: "destroy_disk" });
        state.audit.push(this.auditEvent({ accountId, workspaceId, type: "disk.destroyed", sourceEventId: "destroy_disk" }));
        this.recordEvidence({
          state,
          type: "workspace.storage_destroyed",
          accountId,
          workspace,
          continuation: { action: "workspace_deleted" }
        });
        return clone(workspace);
      }
    });
  }

  async resetWorkspaceToken({ accountId, workspaceId }) {
    return this.store.update((state) => {
      const workspace = latestWorkspaceForAccount(state, accountId, workspaceId);
      if (storageDestroyed(workspace)) throw new Error("workspace_storage_destroyed");
      workspace.access.token = makeToken(workspaceId, `reset-${Date.now()}`);
      workspace.access.tokenStatus = "active";
      workspace.url = this.runtimeProvider.workspaceUrl({
        workspaceId: workspace.id,
        slug: workspace.slug,
        token: workspace.access.token
      });
      workspace.updatedAt = now();
      state.billingLedger.push(this.ledgerEntry({ state, workspaceId, accountId, type: "token_reset", amount: 0, sourceEventId: "reset_token" }));
      this.recordEvidence({
        state,
        type: "workspace.access_token_reset",
        accountId,
        workspace,
        continuation: { action: "open_workspace_url", uri: workspace.url }
      });
      return clone(workspace);
    });
  }

  async deleteWorkspaceToken({ accountId, workspaceId }) {
    return this.store.update((state) => {
      const workspace = latestWorkspaceForAccount(state, accountId, workspaceId);
      workspace.access.tokenStatus = storageDestroyed(workspace) ? "unavailable" : "deleted";
      workspace.updatedAt = now();
      state.billingLedger.push(this.ledgerEntry({ state, workspaceId, accountId, type: "token_deleted", amount: 0, sourceEventId: "delete_token" }));
      this.recordEvidence({
        state,
        type: "workspace.access_token_deleted",
        accountId,
        workspace,
        continuation: { action: "reset_workspace_token" }
      });
      return clone(workspace);
    });
  }

  async settleBilling({ accountId, workspaceId, hours = 1, sourceEventId = "billing_tick" }) {
    const requestedBillHours = billableHours(hours);
    let autoStopRequested = false;

    const settlement = await this.store.update((state) => {
      const workspace = latestWorkspaceForAccount(state, accountId, workspaceId);
      const account = ensureAccount(state, accountId);
      const packagePlan = getPackage(workspace.packageId);
      const existingEntries = this.existingSettlementEntries({ state, accountId, workspaceId, sourceEventId });
      if (existingEntries.length > 0) {
        return {
          entries: existingEntries.map(clone),
          account: clone(account)
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
        account: clone(account)
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

  async resolveWorkspaceAccess({ slug, token }) {
    const state = await this.store.read();
    const workspace = workspaceBySlug(state, slug);
    if (!workspace) throw new Error("workspace_not_found");
    if (workspace.access.tokenStatus !== "active") throw new Error("workspace_token_inactive");
    if (workspace.access.token !== token) throw new Error("workspace_token_invalid");
    return clone(workspace);
  }

  async getState(accountId = "pi-alpha") {
    const state = await this.store.read();
    return {
      product: {
        name: "OPL Cloud",
        console: "OPL Console",
        workspace: "OPL Workspace"
      },
      billingPolicy: billingPolicy(this.pricing),
      packages: this.packages(),
      account: clone(state.accounts[accountId] ?? { id: accountId, balance: 0, frozen: 0, holds: {} }),
      workspaces: Object.values(state.workspaces).filter((workspace) => workspace.ownerAccountId === accountId).map(clone),
      billingLedger: state.billingLedger.filter((entry) => entry.accountId === accountId).map(clone),
      storageBackups: (state.storageBackups || []).filter((entry) => entry.accountId === accountId).map(clone),
      evidenceLedger: (state.evidenceLedger || []).filter((entry) => entry.accountId === accountId).map(clone),
      audit: state.audit.filter((entry) => entry.accountId === accountId).map(clone),
      notifications: (state.notifications || []).filter((entry) => entry.accountId === accountId).map(clone),
      runtimeOperations: state.runtimeOperations.filter((entry) => entry.accountId === accountId).map(clone)
    };
  }

  async operatorSummary({ accountId = null } = {}) {
    const state = await this.store.read();
    const workspaces = Object.values(state.workspaces).filter((workspace) => !accountId || workspace.ownerAccountId === accountId);
    const notifications = (state.notifications || []).filter((event) => !accountId || event.accountId === accountId);
    const runtimeOperations = state.runtimeOperations.filter((operation) => !accountId || operation.accountId === accountId);
    const accounts = Object.values(state.accounts).filter((account) => !accountId || account.id === accountId);
    const storageBackups = (state.storageBackups || []).filter((backup) => !accountId || backup.accountId === accountId);
    const failedOperations = runtimeOperations.filter((operation) => operation.status === "failed");
    const attentionWorkspaces = workspaces.filter((workspace) =>
      workspace.state === "failed" ||
      workspace.state === "storage_hold_exhausted" ||
      workspace.state === "stopped_storage_hold_exhausted" ||
      workspace.server?.routeCleanupStatus === "failed"
    );

    return {
      product: "OPL Console",
      generatedAt: now(),
      accountScope: accountId || "all",
      accounts: {
        total: accounts.length,
        frozen: money(accounts.reduce((sum, account) => sum + Number(account.frozen || 0), 0)),
        balance: money(accounts.reduce((sum, account) => sum + Number(account.balance || 0), 0))
      },
      workspaces: {
        total: workspaces.length,
        running: workspaces.filter((workspace) => workspace.state === "running").length,
        stopped: workspaces.filter((workspace) => workspace.state === "stopped_server_disk_retained").length,
        computeDestroyedStorageRetained: workspaces.filter((workspace) => workspace.state === "server_destroyed_disk_retained").length,
        destroyed: workspaces.filter((workspace) => workspace.state === "destroyed").length,
        needsAttention: attentionWorkspaces.length
      },
      notifications: {
        total: notifications.length,
        error: notifications.filter((event) => event.severity === "error").length,
        warning: notifications.filter((event) => event.severity === "warning").length,
        recent: notifications.slice(-10).reverse().map((event) => ({
          id: event.id,
          accountId: event.accountId,
          workspaceId: event.workspaceId,
          type: event.type,
          severity: event.severity,
          message: event.message,
          createdAt: event.createdAt
        }))
      },
      runtimeOperations: {
        total: runtimeOperations.length,
        failed: failedOperations.length,
        recentFailed: failedOperations.slice(-10).reverse().map((operation) => ({
          id: operation.id,
          accountId: operation.accountId,
          workspaceId: operation.workspaceId,
          operationType: operation.operationType,
          error: operation.error,
          updatedAt: operation.updatedAt
        }))
      },
      storageBackups: {
        total: storageBackups.length,
        available: storageBackups.filter((backup) => backup.status === "available").length,
        failed: storageBackups.filter((backup) => String(backup.status).endsWith("_failed")).length
      },
      billingPolicy: billingPolicy(this.pricing)
    };
  }

  async runtimeReadiness() {
    if (typeof this.runtimeProvider.readiness === "function") {
      return this.runtimeProvider.readiness();
    }
    return {
      provider: this.runtimeProvider.name,
      ready: true,
      missingEnv: [],
      missingTools: []
    };
  }

  async runtimeStatus({ accountId, workspaceId }) {
    const state = await this.store.read();
    const workspace = latestWorkspaceForAccount(state, accountId, workspaceId);
    if (typeof this.runtimeProvider.runtimeStatus === "function") {
      return this.runtimeProvider.runtimeStatus({ workspace: clone(workspace) });
    }
    return {
      provider: workspace.provider,
      workspaceId: workspace.id,
      ready: workspace.state === "running" &&
        workspace.server.status === "running" &&
        workspace.docker.status === "running" &&
        workspace.disk.status === "attached_retained",
      checks: [
        {
          name: "workspace_runtime_running",
          ok: workspace.state === "running" &&
            workspace.server.status === "running" &&
            workspace.docker.status === "running"
        },
        {
          name: "workspace_storage_attached",
          ok: workspace.disk.status === "attached_retained"
        }
      ]
    };
  }

  async productionReadiness() {
    if (!this.productionReadinessCheck) {
      return {
        ready: false,
        missingEnv: [],
        missingTools: [],
        failedChecks: ["production_readiness_not_configured"],
        checks: []
      };
    }
    return this.productionReadinessCheck();
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
        metadata: {
          requestedHours: billedHours,
          baseHourly: computeHourlyBase({ packagePlan, pricing: this.pricing }),
          markup: pricingMarkup(this.pricing)
        }
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
        metadata: {
          requestedHours: billedHours,
          baseGbMonth: storageGbMonthBase(this.pricing),
          markup: pricingMarkup(this.pricing)
        }
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

  releaseHoldToLedger({ state, accountId, workspaceId, holdType, sourceEventId }) {
    const account = ensureAccount(state, accountId);
    const released = releaseHold(account, holdType);
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

  async releaseWorkspaceHoldsAfterCreateFailure({ accountId, workspaceId, error }) {
    return this.store.update((state) => {
      this.releaseHoldToLedger({ state, accountId, workspaceId, holdType: "compute", sourceEventId: "create_workspace_failed" });
      this.releaseHoldToLedger({ state, accountId, workspaceId, holdType: "storage", sourceEventId: "create_workspace_failed" });
      this.notify({
        state,
        accountId,
        workspaceId,
        type: "workspace.create_failed",
        severity: "error",
        message: error.message,
        sourceEventId: "create_workspace_failed"
      });
      return true;
    });
  }

  async recordCreateWorkspaceFailure({ accountId, workspaceId, operationId, error }) {
    return this.store.update((state) => {
      this.releaseHoldToLedger({ state, accountId, workspaceId, holdType: "compute", sourceEventId: "create_workspace_failed" });
      this.releaseHoldToLedger({ state, accountId, workspaceId, holdType: "storage", sourceEventId: "create_workspace_failed" });
      const operation = state.runtimeOperations.find((item) => item.id === operationId);
      if (operation) this.finishRuntimeOperation(operation, "failed", error);
      this.notify({
        state,
        accountId,
        workspaceId,
        type: "workspace.create_failed",
        severity: "error",
        message: error.message,
        sourceEventId: "create_workspace_failed"
      });
      return true;
    });
  }

  async stopRuntimeAfterHoldExhausted({ accountId, workspaceId, sourceEventId }) {
    return this.runRuntimeOperation({
      accountId,
      workspaceId,
      operationType: "auto_stop_compute",
      mutate: async (state, workspace, operation) => {
        if (workspace.server.status !== "running") {
          this.finishRuntimeOperation(operation, "succeeded");
          return clone(workspace);
        }
        workspace.state = "stopping_server";
        workspace.server = await this.runtimeProvider.stopServer({ workspace: clone(workspace) });
        this.finishRuntimeOperation(operation, "succeeded");
        workspace.state = workspace.disk.billingStatus === "hold_exhausted"
          ? "stopped_storage_hold_exhausted"
          : "stopped_server_disk_retained";
        workspace.disk.status = workspace.disk.status === "destroyed" ? "destroyed" : "attached_retained";
        workspace.updatedAt = now();
        state.audit.push(this.auditEvent({ accountId, workspaceId, type: "server.auto_stopped", sourceEventId }));
        return clone(workspace);
      }
    });
  }

  notify({ state, accountId, workspaceId, type, severity, message, sourceEventId }) {
    state.notifications ??= [];
    const event = {
      id: makeId("notification", accountId, workspaceId, type, sourceEventId, String(state.notifications.length)),
      accountId,
      workspaceId,
      type,
      severity,
      message,
      sourceEventId,
      createdAt: now()
    };
    state.notifications.push(event);
    return event;
  }

  async runRuntimeOperation({ accountId, workspaceId, operationType, prepare = null, mutate }) {
    let runtimeOperationStarted = false;
    try {
      return await this.store.update(async (state) => {
        const workspace = latestWorkspaceForAccount(state, accountId, workspaceId);
        if (prepare) prepare(state, workspace);
        const operation = this.startRuntimeOperation({ state, accountId, workspaceId, operationType });
        runtimeOperationStarted = true;
        try {
          return await mutate(state, workspace, operation);
        } catch (error) {
          this.finishRuntimeOperation(operation, "failed", error);
          throw error;
        }
      });
    } catch (error) {
      if (runtimeOperationStarted) {
        await this.recordFailedRuntimeOperation({ accountId, workspaceId, operationType, error });
      }
      throw error;
    }
  }

  startRuntimeOperation({ state, accountId, workspaceId, operationType }) {
    this.runtimeOperationSequence += 1;
    const operation = {
      id: makeId("op", accountId, workspaceId, operationType, String(Date.now()), String(this.runtimeOperationSequence)),
      accountId,
      workspaceId,
      operationType,
      status: "running",
      attempts: 1,
      createdAt: now(),
      updatedAt: now()
    };
    state.runtimeOperations.push(operation);
    return operation;
  }

  finishRuntimeOperation(operation, status, error = null) {
    operation.status = status;
    operation.updatedAt = now();
    if (error) operation.error = error.message;
    return operation;
  }

  async recordFailedRuntimeOperation({ accountId, workspaceId, operationType, error }) {
    return this.store.update((state) => {
      const operation = this.startRuntimeOperation({ state, accountId, workspaceId, operationType });
      return clone(this.finishRuntimeOperation(operation, "failed", error));
    });
  }

  ledgerEntry({ state, workspaceId, accountId, type, amount, sourceEventId, holdType, billableHours, metadata }) {
    const sequence = state?.billingLedger?.length ?? 0;
    return {
      id: makeId("ledger", accountId, workspaceId, type, sourceEventId, String(sequence)),
      workspaceId,
      accountId,
      type,
      amount: money(Number(amount)),
      currency: "CNY",
      sourceEventId,
      ...(holdType ? { holdType } : {}),
      ...(billableHours ? { billableHours } : {}),
      ...(metadata ? { metadata: clone(metadata) } : {}),
      createdAt: now()
    };
  }

  recordEvidence({ state, type, accountId, workspace, packagePlan = null, billingRefs = [], continuation = null }) {
    const effectivePackagePlan = packagePlan || getPackage(workspace.packageId);
    const receipt = createEvidenceReceipt({
      state,
      type,
      accountId,
      workspaceId: workspace.id,
      actor: workspace.owner?.userId
        ? { type: "user", id: workspace.owner.userId, organizationId: workspace.owner.organizationId }
        : { type: "account", id: accountId },
      plan: {
        workspaceName: workspace.name,
        packageId: workspace.packageId,
        computeProfile: effectivePackagePlan.server,
        storageGb: effectivePackagePlan.diskGb
      },
      approval: { status: "implicit_console_policy" },
      environment: {
        runtimeProvider: workspace.provider,
        workspaceImage: workspace.docker?.image
      },
      resourceRefs: {
        serverId: workspace.server?.id,
        dockerId: workspace.docker?.id,
        storageId: workspace.disk?.id,
        storageMountPath: workspace.disk?.mountPath,
        urlTokenMode: workspace.access?.mode || "long_lived_url_token",
        tokenStatus: workspace.access?.tokenStatus
      },
      billingRefs: billingRefs.map((entry) => ({
        id: entry.id,
        type: entry.type,
        amount: entry.amount,
        currency: entry.currency
      })),
      continuation
    });
    appendEvidenceReceipt(state, receipt);
    return receipt;
  }

  auditEvent({ accountId, workspaceId = "", type, sourceEventId }) {
    return {
      id: makeId("audit", accountId, workspaceId, type, sourceEventId, String(Date.now())),
      accountId,
      workspaceId,
      type,
      sourceEventId,
      createdAt: now()
    };
  }
}
