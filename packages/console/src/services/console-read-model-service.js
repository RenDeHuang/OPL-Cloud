
import {
  addMembershipRecord,
  createOrganizationRecord,
  createUserRecord,
  managementSnapshot
} from "../management-model.js";
import { fabricCatalogReadiness } from "../../../fabric/src/index.js";
import { clone, makeId, money, now } from "./core-utils.js";
import {
  accountSnapshotForState,
  ensureAccount,
  publicWalletUser,
  userIdForAccount,
  walletSnapshot
} from "./wallet-service.js";
import { billingPolicy } from "./pricing-service.js";
import { latestBillingReconciliationReport, operatorNotificationInScope } from "./workspace-service.js";
import { OplDomainService } from "./opl-domain-service.js";

function defaultAccountIdForState(state) {
  return Object.values(state.users || {}).find((user) => user.accountId)?.accountId
    || Object.values(state.workspaces || {}).find((workspace) => workspace.ownerAccountId)?.ownerAccountId
    || "local-account";
}

export class ConsoleReadModelService extends OplDomainService {
  async supportTickets({ accountId = null } = {}) {
    const state = await this.store.read();
    return (state.supportTickets || [])
      .filter((ticket) => !accountId || ticket.accountId === accountId)
      .slice()
      .reverse()
      .map(clone);
  }

  async createSupportTicket(input) {
    return this.store.update((state) => {
      state.supportTickets ??= [];
      if (!input.accountId) throw new Error("support_ticket_account_required");
      if (!input.title) throw new Error("support_ticket_title_required");
      if (input.workspaceId) {
        const workspace = state.workspaces?.[input.workspaceId];
        if (!workspace || workspace.ownerAccountId !== input.accountId) {
          throw new Error("support_ticket_workspace_not_in_account");
        }
      }
      const timestamp = now();
      const id = makeId("ticket", input.accountId, input.title, timestamp, state.supportTickets.length);
      const ticket = {
        id,
        accountId: input.accountId,
        userId: input.userId || "",
        workspaceId: input.workspaceId || "",
        title: input.title,
        category: input.category || "Workspace",
        priority: input.priority || "normal",
        status: "open",
        createdAt: timestamp,
        updatedAt: timestamp,
        messages: [
          {
            author: input.author || input.userId || "Lab Owner",
            text: input.description || "Created from OPL Console",
            createdAt: timestamp
          }
        ]
      };
      state.supportTickets.push(ticket);
      state.audit.push(this.auditEvent({
        accountId: ticket.accountId,
        workspaceId: ticket.workspaceId,
        type: "support.ticket_created",
        sourceEventId: ticket.id
      }));
      return clone(ticket);
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
    const billingAccount = accountSnapshotForState(state, organization.billingAccountId);
    const workspaces = Object.values(state.workspaces)
      .filter((workspace) => workspace.owner?.organizationId === organizationId || workspace.ownerAccountId === organization.billingAccountId);
    return managementSnapshot(state, {
      organizationId,
      packages: this.packages(),
      account: billingAccount,
      workspaces
    });
  }

  async getState(accountId = null) {
    const state = await this.store.read();
    const resolvedAccountId = accountId || defaultAccountIdForState(state);
    const user = Object.values(state.users || {}).find((item) => item.accountId === resolvedAccountId) || {
      id: userIdForAccount(state, resolvedAccountId),
      accountId: resolvedAccountId,
      role: "pi",
      status: "active",
      balance: 0,
      frozen: 0,
      holds: {},
      totalRecharged: 0
    };
    const wallet = walletSnapshot(user, resolvedAccountId);
    return {
      product: {
        name: "OPL Cloud",
        console: "OPL Console",
        workspace: "OPL Workspace"
      },
      billingPolicy: billingPolicy(this.pricing),
      packages: this.packages(),
      account: accountSnapshotForState(state, resolvedAccountId),
      user: publicWalletUser(user),
      wallet,
      workspaces: Object.values(state.workspaces).filter((workspace) => workspace.ownerAccountId === resolvedAccountId).map(clone),
      billingLedger: state.billingLedger.filter((entry) => entry.accountId === resolvedAccountId).map(clone),
      resourceUsageLogs: (state.resourceUsageLogs || []).filter((entry) => entry.accountId === resolvedAccountId).map(clone),
      requestUsageLogs: (state.requestUsageLogs || []).filter((entry) => entry.accountId === resolvedAccountId).map(clone),
      walletTransactions: (state.walletTransactions || []).filter((entry) => entry.accountId === resolvedAccountId).map(clone),
      manualTopups: (state.manualTopups || []).filter((entry) => entry.targetAccountId === resolvedAccountId).map(clone),
      requestUsageDedup: (state.requestUsageDedup || []).filter((entry) => entry.accountId === resolvedAccountId).map(clone),
      storageBackups: (state.storageBackups || []).filter((entry) => entry.accountId === resolvedAccountId).map(clone),
      billingReconciliation: {
        latestReport: clone(latestBillingReconciliationReport(state)),
        guard: clone(latestBillingReconciliationReport(state)?.guard || {
          status: "not_required",
          blockNewWorkspaces: false,
          reason: "billing_reconciliation_not_required"
        })
      },
      evidenceLedger: (state.evidenceLedger || []).filter((entry) => entry.accountId === resolvedAccountId).map(clone),
      audit: state.audit.filter((entry) => entry.accountId === resolvedAccountId).map(clone),
      notifications: (state.notifications || []).filter((entry) => entry.accountId === resolvedAccountId).map(clone),
      runtimeOperations: state.runtimeOperations.filter((entry) => entry.accountId === resolvedAccountId).map(clone)
    };
  }

  async operatorSummary({ accountId = null } = {}) {
    const state = await this.store.read();
    const workspaces = Object.values(state.workspaces).filter((workspace) => !accountId || workspace.ownerAccountId === accountId);
    const notifications = (state.notifications || []).filter((event) => operatorNotificationInScope(event, accountId));
    const runtimeOperations = state.runtimeOperations.filter((operation) => !accountId || operation.accountId === accountId);
    const accountIds = new Set([
      ...Object.values(state.users || {}).map((user) => user.accountId).filter(Boolean)
    ]);
    const accounts = [...accountIds]
      .filter((id) => !accountId || id === accountId)
      .map((id) => accountSnapshotForState(state, id));
    const storageBackups = (state.storageBackups || []).filter((backup) => !accountId || backup.accountId === accountId);
    const latestReconciliation = latestBillingReconciliationReport(state);
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
      billingReconciliation: {
        reports: state.billingReconciliationReports?.length || 0,
        guard: clone(latestReconciliation?.guard || {
          status: "not_required",
          blockNewWorkspaces: false,
          reason: "billing_reconciliation_not_required"
        })
      },
      billingPolicy: billingPolicy(this.pricing)
    };
  }

  async runtimeReadiness() {
    const resourceCatalog = fabricCatalogReadiness(this.fabricCatalog);
    if (typeof this.runtimeProvider.readiness === "function") {
      const providerReadiness = await this.runtimeProvider.readiness();
      return {
        ...providerReadiness,
        ready: Boolean(providerReadiness.ready && resourceCatalog.ready),
        resourceCatalog
      };
    }
    return {
      provider: this.runtimeProvider.name,
      ready: resourceCatalog.ready,
      missingEnv: [],
      missingTools: [],
      resourceCatalog
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
}
