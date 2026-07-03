
import { resolveWorkspaceOwner } from "../management-model.js";
import { clone, makeId, makeToken, now } from "./core-utils.js";
import {
  accountAvailable,
  addHold,
  ensureAccount,
  ensureUserWallet
} from "./wallet-service.js";
import {
  computeHourlyBase,
  packageHoldAmount,
  pricingMarkup,
  storageGbMonthBase
} from "./pricing-service.js";
import {
  backupRetentionPolicy,
  defaultStorageBackupPolicy,
  latestStorageBackupForAccount,
  latestWorkspaceForAccount,
  storageDestroyed,
  workspaceBySlug
} from "./workspace-service.js";
import { OplDomainService } from "./opl-domain-service.js";

function compactId(value) {
  return String(value || "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 48) || "workspace";
}

function workspaceEntrySlug(workspaceName, workspaceId) {
  return `${compactId(workspaceName)}-${workspaceId.slice(-6)}`;
}

function resourceForAccount(resources, accountId, resourceId, errorName) {
  const resource = (resources || []).find((item) => item.id === resourceId && item.ownerAccountId === accountId);
  if (!resource) throw new Error(errorName);
  return resource;
}

export class WorkspaceLifecycleService extends OplDomainService {
  async createWorkspace(input) {
    if (!input?.attachmentId && typeof this.runtimeProvider.createWorkspaceRuntime === "function") {
      return this.createLegacyWorkspace(input);
    }
    if (!input?.attachmentId) throw new Error("workspace_attachment_required");
    return this.createWorkspaceEntry(input);
  }

  async createWorkspaceEntry({ accountId, organizationId, userId, workspaceName, attachmentId }) {
    let workspaceId = "";
    let token = "";
    let owner = null;
    let attachmentSnapshot = null;
    let computeSnapshot = null;
    let storageSnapshot = null;
    let packagePlan = null;

    const reservation = await this.store.update((state) => {
      this.assertBillingReconciliationAllowsProvisioning(state);
      state.computeResources ??= [];
      state.storageVolumes ??= [];
      state.storageAttachments ??= [];
      const resolvedOwner = resolveWorkspaceOwner(state, { accountId, organizationId, userId });
      accountId = resolvedOwner.accountId;
      const walletUserId = resolvedOwner.owner?.type === "organization"
        ? ""
        : resolvedOwner.owner?.userId || userId;
      const account = ensureUserWallet(state, { accountId, userId: walletUserId });
      owner = {
        ...resolvedOwner.owner,
        userId: resolvedOwner.owner?.userId || account.id
      };
      const attachment = resourceForAccount(state.storageAttachments, accountId, attachmentId, "storage_attachment_not_found");
      if (attachment.status !== "attached") throw new Error("storage_attachment_not_attached");
      const compute = resourceForAccount(state.computeResources, accountId, attachment.computeId, "compute_resource_not_found");
      const storage = resourceForAccount(state.storageVolumes, accountId, attachment.storageId, "storage_volume_not_found");
      if (compute.status === "destroyed") throw new Error("compute_resource_destroyed");
      if (storage.status === "destroyed") throw new Error("storage_volume_destroyed");

      packagePlan = this.getPackage(compute.packageId);
      workspaceId = makeId("ws", accountId, workspaceName, attachmentId);
      token = makeToken(workspaceId);
      if (state.workspaces[workspaceId]) return { existing: true, workspace: clone(state.workspaces[workspaceId]) };
      const operation = this.startRuntimeOperation({ state, accountId, workspaceId, operationType: "create_workspace" });
      attachmentSnapshot = clone(attachment);
      computeSnapshot = clone(compute);
      storageSnapshot = clone(storage);
      return { existing: false, operationId: operation.id };
    });

    if (reservation.existing) return reservation.workspace;

    const slug = workspaceEntrySlug(workspaceName, workspaceId);
    let entry;
    try {
      entry = typeof this.runtimeProvider.createWorkspaceEntry === "function"
        ? await this.runtimeProvider.createWorkspaceEntry({
          workspaceId,
          ownerAccountId: accountId,
          workspaceName,
          slug,
          token,
          attachment: attachmentSnapshot,
          compute: computeSnapshot,
          storage: storageSnapshot,
          packagePlan
        })
        : {
          slug,
          url: this.runtimeProvider.workspaceUrl({
            workspaceId,
            slug,
            token
          }),
          status: "ready"
        };
    } catch (error) {
      await this.recordCreateWorkspaceFailure({ accountId, workspaceId, operationId: reservation.operationId, error });
      throw error;
    }

    return this.store.update((state) => {
      const account = ensureUserWallet(state, {
        accountId,
        userId: owner?.type === "organization" ? "" : owner?.userId
      });
      const operation = state.runtimeOperations.find((item) => item.id === reservation.operationId);
      if (operation) this.finishRuntimeOperation(operation, "succeeded");
      const attachment = resourceForAccount(state.storageAttachments, accountId, attachmentId, "storage_attachment_not_found");
      const compute = resourceForAccount(state.computeResources, accountId, attachment.computeId, "compute_resource_not_found");
      const storage = resourceForAccount(state.storageVolumes, accountId, attachment.storageId, "storage_volume_not_found");
      const workspace = {
        id: workspaceId,
        ownerAccountId: accountId,
        ownerUserId: account.id,
        owner,
        name: workspaceName,
        packageId: compute.packageId,
        attachmentId: attachment.id,
        computeId: compute.id,
        storageId: storage.id,
        state: "running",
        provider: entry.provider || compute.provider || storage.provider,
        server: {
          id: compute.providerResourceId || compute.id,
          status: compute.status === "running" ? "running" : compute.status,
          billingStatus: compute.billingStatus,
          spec: compute.spec
        },
        docker: {
          id: compute.runtime?.dockerId || `runtime-${workspaceId}`,
          image: compute.image || "ghcr.io/gaofeng21cn/one-person-lab-app:latest",
          status: "running",
          service: compute.runtime?.service || (compute.runtime?.serviceName ? `service/${compute.runtime.serviceName}` : undefined),
          composePath: compute.composePath || attachment.composePath,
          localPath: compute.localPath
        },
        disk: {
          id: storage.providerResourceId || storage.id,
          status: "attached_retained",
          billingStatus: storage.billingStatus,
          sizeGb: storage.sizeGb,
          mountPath: attachment.mountPath
        },
        slug: entry.slug || slug,
        url: entry.url,
        access: {
          mode: "long_lived_url_token",
          requiresLogin: false,
          token,
          tokenStatus: "active",
          rotationPolicy: "reset_or_delete_on_leak"
        },
        runtime: {
          kind: "one-person-lab-app",
          webui: "one-person-lab-app",
          status: entry.status || "ready"
        },
        billing: {
          model: "resource_scoped",
          computeId: compute.id,
          storageId: storage.id,
          attachmentId: attachment.id,
          minimumBillableHours: 1,
          priceMarkup: pricingMarkup(this.pricing)
        },
        createdAt: now(),
        updatedAt: now()
      };
      state.workspaces[workspaceId] = workspace;
      const sourceEventId = `workspace_entry:${workspaceId}:created`;
      state.billingLedger.push({
        ...this.ledgerEntry({
          state,
          workspaceId,
          accountId,
          type: "workspace_entry_created",
          amount: 0,
          sourceEventId,
          metadata: {
            computeId: compute.id,
            storageId: storage.id,
            attachmentId: attachment.id
          }
        }),
        computeId: compute.id,
        storageId: storage.id,
        attachmentId: attachment.id
      });
      state.audit.push(this.auditEvent({ accountId, workspaceId, type: "workspace.created", sourceEventId: workspaceId }));
      this.recordEvidence({
        state,
        type: "workspace.created",
        accountId,
        workspace,
        packagePlan,
        billingRefs: state.billingLedger.filter((entry) =>
          entry.accountId === accountId &&
          (entry.computeId === compute.id || entry.storageId === storage.id || entry.attachmentId === attachment.id)
        ),
        continuation: {
          action: "open_workspace_url",
          uri: workspace.url
        }
      });
      return clone(workspace);
    });
  }

  async createLegacyWorkspace({ accountId, organizationId, userId, workspaceName, packageId }) {
    const packagePlan = this.getPackage(packageId);
    const hold = packageHoldAmount({ packagePlan, pricing: this.pricing });
    let workspaceId = null;
    let token = null;
    let owner = null;

    const reservation = await this.store.update((state) => {
      this.assertBillingReconciliationAllowsProvisioning(state);
      const resolvedOwner = resolveWorkspaceOwner(state, { accountId, organizationId, userId });
      accountId = resolvedOwner.accountId;
      const walletUserId = resolvedOwner.owner?.type === "organization"
        ? ""
        : resolvedOwner.owner?.userId || userId;
      const account = ensureUserWallet(state, {
        accountId,
        userId: walletUserId
      });
      owner = {
        ...resolvedOwner.owner,
        userId: resolvedOwner.owner?.userId || account.id
      };
      workspaceId = makeId("ws", accountId, workspaceName, packageId);
      token = makeToken(workspaceId);
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
      const account = ensureUserWallet(state, {
        accountId,
        userId: owner?.type === "organization" ? "" : owner?.userId
      });
      const operation = state.runtimeOperations.find((item) => item.id === reservation.operationId);
      if (operation) this.finishRuntimeOperation(operation, "succeeded");

      const workspace = {
        id: workspaceId,
        ownerAccountId: accountId,
        ownerUserId: account.id,
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
    const packagePlan = this.getPackage(packageId);
    const hold = packageHoldAmount({ packagePlan, pricing: this.pricing });
    let workspaceId = null;
    let token = null;
    let backupSnapshot = null;

    const reservation = await this.store.update((state) => {
      this.assertBillingReconciliationAllowsProvisioning(state);
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
        ownerUserId: account.id,
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
        const packagePlan = this.getPackage(workspace.packageId);
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
          packagePlan: this.getPackage(workspace.packageId),
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

  async resolveWorkspaceAccess({ slug, token }) {
    const state = await this.store.read();
    const workspace = workspaceBySlug(state, slug);
    if (!workspace) throw new Error("workspace_not_found");
    if (workspace.access.tokenStatus !== "active") throw new Error("workspace_token_inactive");
    if (workspace.access.token !== token) throw new Error("workspace_token_invalid");
    return clone(workspace);
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
}
