
import {
  addMembershipRecord,
  createOrganizationRecord,
  createUserRecord,
  managementSnapshot
} from "../management-model.js";
import { fabricCatalogReadiness } from "../../../fabric/src/index.js";
import { hashPassword, normalizeEmail } from "../auth-credentials.js";
import { clone, makeId, money, now } from "./core-utils.js";
import {
  accountSnapshotForState,
  appendWalletTransaction,
  ensureAccount,
  ensureUserWallet,
  publicWalletUser,
  userIdForAccount,
  walletSnapshot
} from "./wallet-service.js";
import { billingPolicy } from "./pricing-service.js";
import { latestBillingReconciliationReport, operatorNotificationInScope } from "./workspace-service.js";
import { OplDomainService } from "./opl-domain-service.js";

function computePoolsFromPackages(packages) {
  return packages.map((plan) => ({
    id: `pool-${plan.id}-${plan.server}`,
    packageId: plan.id,
    name: `${plan.name} pool`,
    instanceType: plan.instanceType || plan.server,
    cpu: plan.cpu,
    memoryGb: plan.memoryGb,
    nodePoolId: plan.nodePoolId || "",
    status: plan.available ? "ready" : "missing",
    hourlyPrice: plan.price?.computeHourly || 0,
    provider: "tencent-tke"
  }));
}

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
    const email = normalizeEmail(input.email);
    const loginUserRequested = Boolean(input.accountId || input.password || input.passwordHash || input.initialBalance);
    if (loginUserRequested && !input.accountId) throw new Error("account_required");
    if (loginUserRequested && !input.passwordHash && !input.password) throw new Error("user_password_required");
    const normalized = {
      ...input,
      email,
      role: input.role || "pi",
      passwordHash: input.passwordHash || (input.password ? await hashPassword(input.password) : "")
    };
    return this.store.update((state) => {
      const targetUserId = normalized.userId || makeId("usr", email || normalized.name || "user");
      const duplicateEmail = Object.values(state.users || {}).find((user) =>
        normalizeEmail(user.email) === email &&
        user.id !== targetUserId
      );
      if (duplicateEmail) throw new Error("user_email_exists");
      const user = createUserRecord(state, normalized);
      const persisted = state.users[user.id];
      if (normalized.accountId) {
        const account = ensureUserWallet(state, {
          userId: user.id,
          accountId: normalized.accountId,
          email
        });
        Object.assign(account, {
          email,
          name: normalized.name || account.name || "",
          role: normalized.role || account.role || "pi",
          accountId: normalized.accountId,
          organizationId: normalized.organizationId || account.organizationId || null,
          status: normalized.status || account.status || "active",
          passwordHash: normalized.passwordHash,
          updatedAt: now()
        });
        const credit = money(Number(normalized.initialBalance || 0));
        const sourceEventId = `user_initial_balance:${user.id}`;
        const alreadyCredited = (state.manualTopups || []).some((topup) =>
          topup.targetAccountId === normalized.accountId &&
          topup.reason === sourceEventId
        );
        if (credit > 0 && !alreadyCredited) {
          const balanceBefore = money(Number(account.balance || 0));
          const frozenBefore = money(Number(account.frozen || 0));
          account.balance = money(balanceBefore + credit);
          account.totalRecharged = money(Number(account.totalRecharged || 0) + credit);
          const entry = this.ledgerEntry({
            state,
            workspaceId: "account",
            accountId: normalized.accountId,
            type: "credit",
            amount: credit,
            sourceEventId,
            metadata: {
              operatorUserId: normalized.operatorUserId || "",
              operatorAccountId: normalized.operatorAccountId || ""
            }
          });
          state.billingLedger.push(entry);
          const transaction = appendWalletTransaction(state, {
            user: account,
            accountId: normalized.accountId,
            type: "credit",
            amount: credit,
            sourceEventId,
            ledgerEntryId: entry.id,
            balanceBefore,
            balanceAfter: account.balance,
            frozenBefore,
            frozenAfter: account.frozen,
            metadata: {
              operatorUserId: normalized.operatorUserId || "",
              operatorAccountId: normalized.operatorAccountId || ""
            }
          });
          state.manualTopups.push({
            id: makeId("manual-topup", account.id, normalized.accountId, sourceEventId, String(state.manualTopups.length)),
            operatorUserId: normalized.operatorUserId || "",
            operatorAccountId: normalized.operatorAccountId || "",
            targetUserId: account.id,
            targetAccountId: normalized.accountId,
            amount: credit,
            currency: "CNY",
            reason: sourceEventId,
            status: "completed",
            balanceBefore,
            balanceAfter: money(Number(account.balance || 0)),
            ledgerEntryId: entry.id,
            walletTransactionId: transaction.id,
            createdAt: now()
          });
        }
      }
      state.audit.push(this.auditEvent({
        accountId: normalized.accountId || "management",
        type: "user.created",
        sourceEventId: user.id
      }));
      return publicWalletUser(persisted);
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

  async managementState({ organizationId } = {}) {
    const state = await this.store.read();
    if (!organizationId) {
      const accountIds = [...new Set(Object.values(state.users || {}).map((user) => user.accountId).filter(Boolean))];
      return {
        organization: null,
        users: Object.values(state.users || {}).map(publicWalletUser),
        memberships: (state.memberships || []).map(clone),
        accounts: accountIds.map((accountId) => accountSnapshotForState(state, accountId)),
        packages: this.packages(),
        computePools: computePoolsFromPackages(this.packages()),
        workspaces: Object.values(state.workspaces || {}).map(clone),
        computeAllocations: (state.computeAllocations || []).map(clone),
        storageVolumes: (state.storageVolumes || []).map(clone),
        storageAttachments: (state.storageAttachments || []).map(clone),
        walletTransactions: (state.walletTransactions || []).map(clone),
        manualTopups: (state.manualTopups || []).map(clone)
      };
    }
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
      computePools: computePoolsFromPackages(this.packages()),
      account: accountSnapshotForState(state, resolvedAccountId),
      user: publicWalletUser(user),
      wallet,
      computeAllocations: (state.computeAllocations || []).filter((resource) => resource.ownerAccountId === resolvedAccountId).map(clone),
      storageVolumes: (state.storageVolumes || []).filter((resource) => resource.ownerAccountId === resolvedAccountId).map(clone),
      storageAttachments: (state.storageAttachments || []).filter((resource) => resource.ownerAccountId === resolvedAccountId).map(clone),
      workspaces: Object.values(state.workspaces).filter((workspace) => workspace.ownerAccountId === resolvedAccountId).map(clone),
      billingLedger: state.billingLedger.filter((entry) => entry.accountId === resolvedAccountId).map(clone),
      resourceUsageLogs: (state.resourceUsageLogs || []).filter((entry) => entry.accountId === resolvedAccountId).map(clone),
      requestUsageLogs: (state.requestUsageLogs || []).filter((entry) => entry.accountId === resolvedAccountId).map(clone),
      walletTransactions: (state.walletTransactions || []).filter((entry) => entry.accountId === resolvedAccountId).map(clone),
      manualTopups: (state.manualTopups || []).filter((entry) => entry.targetAccountId === resolvedAccountId).map(clone),
      requestUsageDedup: (state.requestUsageDedup || []).filter((entry) => entry.accountId === resolvedAccountId).map(clone),
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
    const computeAllocations = (state.computeAllocations || []).filter((allocation) => !accountId || allocation.ownerAccountId === accountId);
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
        urlActive: workspaces.filter((workspace) => workspace.access?.tokenStatus === "active").length,
        destroyed: workspaces.filter((workspace) => workspace.state === "destroyed").length,
        needsAttention: attentionWorkspaces.length
      },
      computeAllocations: {
        total: computeAllocations.length,
        running: computeAllocations.filter((allocation) => allocation.status === "running").length,
        failed: computeAllocations.filter((allocation) => allocation.status === "failed").length
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
