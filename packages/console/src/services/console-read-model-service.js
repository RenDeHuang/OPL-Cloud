
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

const INTERNAL_VERIFIER_ACCOUNT_IDS = new Set(["pi-production-verifier"]);

function isInternalVerifierAccountId(accountId = "") {
  return INTERNAL_VERIFIER_ACCOUNT_IDS.has(String(accountId || ""));
}

function recordAccountIds(record = {}) {
  return [
    record.accountId,
    record.ownerAccountId,
    record.targetAccountId
  ].filter(Boolean);
}

function isInternalVerifierRecord(record = {}) {
  return (
    recordAccountIds(record).some(isInternalVerifierAccountId) ||
    String(record.sourceEventId || "").startsWith("production_verification_")
  );
}

function businessRecord(record, accountId = null) {
  if (accountId) return recordAccountIds(record).includes(accountId);
  return !isInternalVerifierRecord(record);
}

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

function publicResourceRecord(record) {
  const next = clone(record);
  delete next.providerData;
  return next;
}

function publicResourceRecords(records = []) {
  return records.map(publicResourceRecord);
}

function defaultAccountIdForState(state) {
  return Object.values(state.users || {}).find((user) => user.accountId && !isInternalVerifierAccountId(user.accountId))?.accountId
    || Object.values(state.workspaces || {}).find((workspace) => workspace.ownerAccountId && !isInternalVerifierAccountId(workspace.ownerAccountId))?.ownerAccountId
    || "local-account";
}

function productionE2EFromState(state) {
  const runs = new Map();
  function ensureRun(runId) {
    if (!runs.has(runId)) {
      runs.set(runId, {
        runId,
        accountId: "",
        workspaceId: "",
        status: "running",
        checks: new Set(),
        lastSeenAt: ""
      });
    }
    return runs.get(runId);
  }
  function touch(run, timestamp = "") {
    if (timestamp && (!run.lastSeenAt || timestamp > run.lastSeenAt)) run.lastSeenAt = timestamp;
  }

  for (const entry of state.billingLedger || []) {
    const sourceEventId = String(entry.sourceEventId || "");
    const match = sourceEventId.match(/^production_verification_(credit|resource_settlement):(.+)$/);
    if (!match) continue;
    const run = ensureRun(match[2]);
    run.accountId ||= entry.accountId || "";
    run.workspaceId ||= entry.workspaceId && entry.workspaceId !== "account" ? entry.workspaceId : "";
    run.checks.add(match[1] === "credit" ? "credit" : "resource_settlement");
    touch(run, entry.createdAt);
  }

  for (const operation of state.runtimeOperations || []) {
    const hasVerificationName = /production verification/i.test(String(operation.name || operation.workspaceName || ""));
    const workspaceMatch = String(operation.workspaceId || "").match(/production-verification|prod/i);
    const matchingRuns = [...runs.values()].filter((run) =>
      run.accountId &&
      run.accountId === operation.accountId &&
      (!run.workspaceId || !operation.workspaceId || run.workspaceId === operation.workspaceId)
    );
    if (!hasVerificationName && !workspaceMatch && matchingRuns.length === 0) continue;
    if (matchingRuns.length > 0) {
      for (const run of matchingRuns) {
        run.workspaceId ||= operation.workspaceId || "";
        run.checks.add("runtime_operation");
        if (operation.status === "failed") run.status = "failed";
        touch(run, operation.updatedAt || operation.createdAt);
      }
      continue;
    }
    const runId = String(operation.id || operation.operationId || operation.workspaceId || "").split(":").at(-1) || "unknown";
    const run = ensureRun(runId);
    run.accountId ||= operation.accountId || "";
    run.workspaceId ||= operation.workspaceId || "";
    run.checks.add("runtime_operation");
    if (operation.status === "failed") run.status = "failed";
    touch(run, operation.updatedAt || operation.createdAt);
  }

  for (const run of runs.values()) {
    if (run.checks.has("credit") && run.checks.has("resource_settlement")) run.status = run.status === "failed" ? "failed" : "passed";
  }

  const recent = [...runs.values()]
    .map((run) => ({
      runId: run.runId,
      accountId: run.accountId,
      workspaceId: run.workspaceId,
      status: run.status,
      checks: [...run.checks].sort(),
      lastSeenAt: run.lastSeenAt
    }))
    .sort((a, b) => String(b.lastSeenAt).localeCompare(String(a.lastSeenAt)))
    .slice(0, 8);
  return {
    total: runs.size,
    passed: recent.filter((run) => run.status === "passed").length,
    failed: recent.filter((run) => run.status === "failed").length,
    recent
  };
}

function resourceAnomaliesFromState(state, accountId = null) {
  const computeById = new Map((state.computeAllocations || []).map((item) => [item.id, item]));
  const storageById = new Map((state.storageVolumes || []).map((item) => [item.id, item]));
  const attachmentById = new Map((state.storageAttachments || []).map((item) => [item.id, item]));
  const anomalies = [];
  for (const workspace of Object.values(state.workspaces || {})) {
    if (accountId && workspace.ownerAccountId !== accountId) continue;
    if (!accountId && isInternalVerifierAccountId(workspace.ownerAccountId)) continue;
    const compute = computeById.get(workspace.computeAllocationId);
    const storage = storageById.get(workspace.storageId);
    const attachment = attachmentById.get(workspace.attachmentId);
    if (!compute || compute.status === "destroyed" || compute.status === "failed") {
      anomalies.push({ type: "compute", accountId: workspace.ownerAccountId, workspaceId: workspace.id, status: compute?.status || "missing" });
    }
    if (!storage || storage.status === "destroyed" || storage.status === "failed") {
      anomalies.push({ type: "storage", accountId: workspace.ownerAccountId, workspaceId: workspace.id, status: storage?.status || "missing" });
    }
    if (!attachment || attachment.status === "detached" || attachment.status === "failed") {
      anomalies.push({ type: "attachment", accountId: workspace.ownerAccountId, workspaceId: workspace.id, status: attachment?.status || "missing" });
    }
  }
  return anomalies.slice(0, 12);
}

function uniqueSorted(values = []) {
  return [...new Set(values.filter(Boolean))].sort();
}

function ledgerMatchesResource(entry = {}, resourceType, resourceId) {
  const metadata = entry.metadata || {};
  if (resourceType === "compute") {
    return entry.computeAllocationId === resourceId || metadata.computeAllocationId === resourceId;
  }
  return entry.storageId === resourceId || metadata.storageId === resourceId;
}

function walletMatchesResource(transaction = {}, resourceType, resourceId, ledgerEntryIds = []) {
  const metadata = transaction.metadata || {};
  if (ledgerEntryIds.includes(transaction.ledgerEntryId)) return true;
  if (resourceType === "compute") {
    return transaction.computeAllocationId === resourceId || metadata.computeAllocationId === resourceId;
  }
  return transaction.storageId === resourceId || metadata.storageId === resourceId;
}

function resourceWorkspaceIds(state, resourceType, resource = {}) {
  const explicit = Array.isArray(resource.workspaceIds) ? resource.workspaceIds : [];
  const fromWorkspaces = Object.values(state.workspaces || {})
    .filter((workspace) => {
      if (resourceType === "compute") {
        return workspace.currentComputeAllocationId === resource.id || workspace.computeAllocationId === resource.id;
      }
      return workspace.storageId === resource.id;
    })
    .map((workspace) => workspace.id);
  return uniqueSorted([...explicit, ...fromWorkspaces]);
}

function resourceLedgerEvidenceFromState(state, accountId = null) {
  const rows = [];
  const resources = [
    ...(state.computeAllocations || []).map((resource) => ({ resourceType: "compute", resource })),
    ...(state.storageVolumes || []).map((resource) => ({ resourceType: "storage", resource }))
  ];
  for (const { resourceType, resource } of resources) {
    if (!businessRecord(resource, accountId)) continue;
    const resourceId = resource.id;
    const ownerAccountId = resource.ownerAccountId || resource.accountId || "";
    if (accountId && ownerAccountId !== accountId) continue;
    const ledgerEntries = (state.billingLedger || []).filter((entry) => ledgerMatchesResource(entry, resourceType, resourceId));
    const ledgerEntryIds = uniqueSorted(ledgerEntries.map((entry) => entry.id));
    const walletTransactions = (state.walletTransactions || []).filter((transaction) =>
      walletMatchesResource(transaction, resourceType, resourceId, ledgerEntryIds)
    );
    const workspaceIds = uniqueSorted([
      ...resourceWorkspaceIds(state, resourceType, resource),
      ...ledgerEntries.map((entry) => entry.workspaceId).filter((workspaceId) => workspaceId && workspaceId !== "account" && workspaceId !== "resource"),
      ...walletTransactions.map((transaction) => transaction.workspaceId).filter((workspaceId) => workspaceId && workspaceId !== "account" && workspaceId !== "resource")
    ]);
    rows.push({
      id: resourceId,
      resourceType,
      ownerAccountId,
      ownerUserId: resource.ownerUserId || userIdForAccount(state, ownerAccountId),
      computeAllocationId: resourceType === "compute" ? resourceId : "",
      storageId: resourceType === "storage" ? resourceId : "",
      workspaceIds,
      ledgerEntryIds,
      walletTransactionIds: uniqueSorted(walletTransactions.map((transaction) => transaction.id)),
      nodePoolId: resource.nodePoolId || "",
      cvmInstanceId: resource.cvmInstanceId || resource.instanceId || "",
      nodeName: resource.nodeName || resource.machineName || "",
      privateIp: resource.privateIp || "",
      publicIp: resource.publicIp || "",
      providerResourceId: resource.providerResourceId || "",
      storageProviderId: resourceType === "storage" ? resource.providerResourceId || "" : "",
      billingStatus: resource.billingStatus || "",
      status: resource.status || "",
      providerRequestId: resource.providerRequestId || resource.operationId || "",
      issue: resource.safeMessage || resource.error || resource.failureReason || "暂无失败"
    });
  }
  return rows;
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

  async disableUser(input) {
    return this.updateUserStatus({
      ...input,
      status: "disabled",
      auditType: "user.disabled"
    });
  }

  async deleteUser(input) {
    return this.updateUserStatus({
      ...input,
      status: "deleted",
      auditType: "user.deleted"
    });
  }

  async updateUserStatus({ userId, status, reason = "", operatorUserId = "", operatorAccountId = "", auditType }) {
    if (!userId) throw new Error("user_required");
    return this.store.update((state) => {
      const user = state.users?.[userId];
      if (!user) throw new Error("user_not_found");
      if (user.role === "admin" && status !== "active") {
        const activeAdmins = Object.values(state.users || {}).filter((item) =>
          item.role === "admin" &&
          item.id !== userId &&
          !["disabled", "deleted"].includes(item.status)
        );
        if (activeAdmins.length === 0) throw new Error("last_admin_user_required");
      }
      user.status = status;
      user.disabledReason = status === "disabled" ? reason : user.disabledReason || "";
      user.deletedReason = status === "deleted" ? reason : user.deletedReason || "";
      user.updatedAt = now();
      state.audit.push(this.auditEvent({
        accountId: user.accountId || operatorAccountId || "management",
        type: auditType,
        sourceEventId: `${userId}:${reason || status}`
      }));
      return publicWalletUser(user);
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
      const users = Object.values(state.users || {}).filter((user) => businessRecord(user));
      const accountIds = [...new Set(users.map((user) => user.accountId).filter(Boolean))];
      return {
        organization: null,
        users: users.map(publicWalletUser),
        memberships: (state.memberships || []).map(clone),
        accounts: accountIds.map((accountId) => accountSnapshotForState(state, accountId)),
        packages: this.packages(),
        computePools: computePoolsFromPackages(this.packages()),
        workspaces: Object.values(state.workspaces || {}).filter((workspace) => businessRecord(workspace)).map(clone),
        computeAllocations: publicResourceRecords((state.computeAllocations || []).filter((resource) => businessRecord(resource))),
        storageVolumes: publicResourceRecords((state.storageVolumes || []).filter((resource) => businessRecord(resource))),
        storageAttachments: publicResourceRecords((state.storageAttachments || []).filter((resource) => businessRecord(resource))),
        resourceLedgerEvidence: resourceLedgerEvidenceFromState(state),
        walletTransactions: (state.walletTransactions || []).filter((entry) => businessRecord(entry)).map(clone),
        manualTopups: (state.manualTopups || []).filter((entry) => businessRecord(entry)).map(clone)
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
      computeAllocations: publicResourceRecords((state.computeAllocations || []).filter((resource) => resource.ownerAccountId === resolvedAccountId)),
      storageVolumes: publicResourceRecords((state.storageVolumes || []).filter((resource) => resource.ownerAccountId === resolvedAccountId)),
      storageAttachments: publicResourceRecords((state.storageAttachments || []).filter((resource) => resource.ownerAccountId === resolvedAccountId)),
      workspaces: Object.values(state.workspaces).filter((workspace) => workspace.ownerAccountId === resolvedAccountId).map(clone),
      billingLedger: state.billingLedger.filter((entry) => entry.accountId === resolvedAccountId).map(clone),
      resourceUsageLogs: (state.resourceUsageLogs || []).filter((entry) => entry.accountId === resolvedAccountId).map(clone),
      walletTransactions: (state.walletTransactions || []).filter((entry) => entry.accountId === resolvedAccountId).map(clone),
      manualTopups: (state.manualTopups || []).filter((entry) => entry.targetAccountId === resolvedAccountId).map(clone),
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
    const workspaces = Object.values(state.workspaces).filter((workspace) => businessRecord(workspace, accountId));
    const notifications = (state.notifications || []).filter((event) =>
      operatorNotificationInScope(event, accountId) &&
      (accountId || !isInternalVerifierRecord(event))
    );
    const runtimeOperations = state.runtimeOperations.filter((operation) => businessRecord(operation, accountId));
    const accountIds = new Set([
      ...Object.values(state.users || {}).filter((user) => businessRecord(user, accountId)).map((user) => user.accountId).filter(Boolean)
    ]);
    const accounts = [...accountIds]
      .filter((id) => !accountId || id === accountId)
      .map((id) => accountSnapshotForState(state, id));
    const computeAllocations = (state.computeAllocations || []).filter((allocation) => businessRecord(allocation, accountId));
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
      failedOperations: failedOperations.slice(-10).reverse().map((operation) => ({
        id: operation.id,
        accountId: operation.accountId,
        workspaceId: operation.workspaceId,
        resourceId: operation.resourceId,
        operationType: operation.operationType,
        status: operation.status,
        error: operation.error || operation.safeMessage || "",
        updatedAt: operation.updatedAt
      })),
      resourceAnomalies: resourceAnomaliesFromState(state, accountId),
      resourceLedgerEvidence: {
        total: resourceLedgerEvidenceFromState(state, accountId).length,
        recent: resourceLedgerEvidenceFromState(state, accountId).slice(-10)
      },
      productionE2E: productionE2EFromState(state),
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
