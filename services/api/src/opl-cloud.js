const PACKAGES = {
  basic: {
    id: "basic",
    name: "Basic Workspace",
    server: "2c4g",
    diskGb: 10
  },
  pro: {
    id: "pro",
    name: "Pro Workspace",
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
  const gbMonth = pricing.diskGbMonth ?? 0.2;
  const markup = pricing.markup ?? 0.1;
  const daily = (packagePlan.diskGb * gbMonth * (1 + markup)) / 30;
  return money(daily * 7);
}

function hourlyStorageAmount({ packagePlan, pricing, hours }) {
  const gbMonth = pricing.diskGbMonth ?? 0.2;
  const markup = pricing.markup ?? 0.1;
  return money((packagePlan.diskGb * gbMonth * (1 + markup) / 30 / 24) * hours);
}

function hourlyServerAmount({ packagePlan, pricing, hours }) {
  const hourly = pricing.serverHourly?.[packagePlan.id] ?? 0;
  const markup = pricing.markup ?? 0.1;
  return money(hourly * (1 + markup) * hours);
}

export function createOplCloud({ store, runtimeProvider, pricing }) {
  return new OplCloudService({ store, runtimeProvider, pricing });
}

export class OplCloudService {
  constructor({ store, runtimeProvider, pricing }) {
    this.store = store;
    this.runtimeProvider = runtimeProvider;
    this.pricing = pricing;
  }

  packages() {
    return Object.values(PACKAGES).map(clone);
  }

  async creditAccount({ accountId, amount, reason }) {
    if (!accountId) throw new Error("account_required");
    const credit = Number(amount);
    if (!Number.isFinite(credit) || credit <= 0) throw new Error("positive_credit_required");

    return this.store.update((state) => {
      const account = ensureAccount(state, accountId);
      account.balance = money(account.balance + credit);
      const entry = this.ledgerEntry({
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

  async createWorkspace({ accountId, workspaceName, packageId }) {
    const packagePlan = getPackage(packageId);
    const workspaceId = makeId("ws", accountId, workspaceName, packageId);
    const token = makeToken(workspaceId);
    const holdAmount = storageHoldAmount({ packagePlan, pricing: this.pricing });

    return this.store.update(async (state) => {
      const account = ensureAccount(state, accountId);
      if (accountAvailable(account) < holdAmount) {
        throw new Error("insufficient_storage_hold_balance");
      }
      if (state.workspaces[workspaceId]) return clone(state.workspaces[workspaceId]);

      account.frozen = money(account.frozen + holdAmount);
      state.billingLedger.push(this.ledgerEntry({
        workspaceId,
        accountId,
        type: "storage_hold",
        amount: holdAmount,
        sourceEventId: "open_workspace"
      }));

      const runtime = await this.runtimeProvider.createWorkspaceRuntime({
        workspaceId,
        ownerAccountId: accountId,
        workspaceName,
        packagePlan,
        token
      });

      const workspace = {
        id: workspaceId,
        ownerAccountId: accountId,
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
          requiresLogin: false,
          token,
          tokenStatus: "active"
        },
        createdAt: now(),
        updatedAt: now()
      };
      state.workspaces[workspaceId] = workspace;
      state.audit.push(this.auditEvent({ accountId, workspaceId, type: "workspace.created", sourceEventId: workspaceId }));
      return clone(workspace);
    });
  }

  async stopServer({ accountId, workspaceId, confirm }) {
    if (confirm !== true) throw new Error("server_stop_confirmation_required");
    return this.store.update(async (state) => {
      const workspace = latestWorkspaceForAccount(state, accountId, workspaceId);
      workspace.state = "stopping_server";
      workspace.server = await this.runtimeProvider.stopServer({ workspace: clone(workspace) });
      workspace.state = "stopped_server_disk_retained";
      workspace.disk.status = workspace.disk.status === "destroyed" ? "destroyed" : "attached_retained";
      workspace.updatedAt = now();
      state.billingLedger.push(this.ledgerEntry({
        workspaceId,
        accountId,
        type: "server_billing_stopped",
        amount: 0,
        sourceEventId: "stop_server"
      }));
      state.audit.push(this.auditEvent({ accountId, workspaceId, type: "server.stopped", sourceEventId: "stop_server" }));
      return clone(workspace);
    });
  }

  async restartServer({ accountId, workspaceId }) {
    return this.store.update(async (state) => {
      const workspace = latestWorkspaceForAccount(state, accountId, workspaceId);
      const packagePlan = getPackage(workspace.packageId);
      const account = ensureAccount(state, accountId);
      const requiredHold = storageHoldAmount({ packagePlan, pricing: this.pricing });
      if (account.frozen < requiredHold && accountAvailable(account) < requiredHold - account.frozen) {
        throw new Error("insufficient_storage_hold_balance");
      }
      if (account.frozen < requiredHold) {
        const delta = money(requiredHold - account.frozen);
        account.frozen = money(account.frozen + delta);
        state.billingLedger.push(this.ledgerEntry({
          workspaceId,
          accountId,
          type: "storage_hold",
          amount: delta,
          sourceEventId: "resume_workspace"
        }));
      }
      workspace.state = "restarting_server";
      workspace.server = await this.runtimeProvider.restartServer({ workspace: clone(workspace) });
      workspace.docker.status = "running";
      workspace.disk.status = "attached_retained";
      workspace.disk.billingStatus = "active";
      workspace.state = "running";
      workspace.updatedAt = now();
      state.audit.push(this.auditEvent({ accountId, workspaceId, type: "server.restarted", sourceEventId: "restart_server" }));
      return clone(workspace);
    });
  }

  async destroyServer({ accountId, workspaceId, confirm }) {
    if (confirm !== true) throw new Error("server_destroy_confirmation_required");
    return this.store.update(async (state) => {
      const workspace = latestWorkspaceForAccount(state, accountId, workspaceId);
      workspace.state = "destroying_server";
      workspace.server = await this.runtimeProvider.destroyServer({ workspace: clone(workspace) });
      workspace.docker.status = "destroyed";
      workspace.disk.status = workspace.disk.status === "destroyed" ? "destroyed" : "detached_retained";
      workspace.state = workspace.disk.status === "destroyed" ? "destroyed" : "server_destroyed_disk_retained";
      workspace.updatedAt = now();
      state.billingLedger.push(this.ledgerEntry({
        workspaceId,
        accountId,
        type: "server_destroyed",
        amount: 0,
        sourceEventId: "destroy_server"
      }));
      state.audit.push(this.auditEvent({ accountId, workspaceId, type: "server.destroyed", sourceEventId: "destroy_server" }));
      return clone(workspace);
    });
  }

  async destroyDisk({ accountId, workspaceId, confirmDataLoss }) {
    if (confirmDataLoss !== true) throw new Error("disk_destroy_confirmation_required");
    return this.store.update(async (state) => {
      const workspace = latestWorkspaceForAccount(state, accountId, workspaceId);
      workspace.state = "destroying_disk";
      workspace.disk = await this.runtimeProvider.destroyDisk({ workspace: clone(workspace) });
      workspace.server.status = "destroyed";
      workspace.server.billingStatus = "stopped";
      workspace.docker.status = "destroyed";
      workspace.state = "destroyed";
      workspace.updatedAt = now();
      state.billingLedger.push(this.ledgerEntry({
        workspaceId,
        accountId,
        type: "storage_destroyed",
        amount: 0,
        sourceEventId: "destroy_disk"
      }));
      state.audit.push(this.auditEvent({ accountId, workspaceId, type: "disk.destroyed", sourceEventId: "destroy_disk" }));
      return clone(workspace);
    });
  }

  async resetWorkspaceToken({ accountId, workspaceId }) {
    return this.store.update((state) => {
      const workspace = latestWorkspaceForAccount(state, accountId, workspaceId);
      workspace.access.token = makeToken(workspaceId, `reset-${Date.now()}`);
      workspace.access.tokenStatus = "active";
      workspace.url = this.runtimeProvider.workspaceUrl({
        slug: workspace.slug,
        token: workspace.access.token
      });
      workspace.updatedAt = now();
      state.billingLedger.push(this.ledgerEntry({ workspaceId, accountId, type: "token_reset", amount: 0, sourceEventId: "reset_token" }));
      return clone(workspace);
    });
  }

  async deleteWorkspaceToken({ accountId, workspaceId }) {
    return this.store.update((state) => {
      const workspace = latestWorkspaceForAccount(state, accountId, workspaceId);
      workspace.access.tokenStatus = "deleted";
      workspace.updatedAt = now();
      state.billingLedger.push(this.ledgerEntry({ workspaceId, accountId, type: "token_deleted", amount: 0, sourceEventId: "delete_token" }));
      return clone(workspace);
    });
  }

  async settleBilling({ accountId, workspaceId, hours = 1, sourceEventId = "meter_tick" }) {
    const billHours = Number(hours);
    if (!Number.isFinite(billHours) || billHours <= 0) throw new Error("positive_hours_required");

    return this.store.update((state) => {
      const workspace = latestWorkspaceForAccount(state, accountId, workspaceId);
      const account = ensureAccount(state, accountId);
      const packagePlan = getPackage(workspace.packageId);
      const entries = [];

      if (workspace.server.status === "running" && workspace.server.billingStatus === "active") {
        entries.push(this.ledgerEntry({
          workspaceId,
          accountId,
          type: "server_debit",
          amount: -hourlyServerAmount({ packagePlan, pricing: this.pricing, hours: billHours }),
          sourceEventId
        }));
      }

      if (workspace.disk.status !== "destroyed" && workspace.disk.billingStatus === "active") {
        entries.push(this.ledgerEntry({
          workspaceId,
          accountId,
          type: "storage_debit",
          amount: -hourlyStorageAmount({ packagePlan, pricing: this.pricing, hours: billHours }),
          sourceEventId
        }));
      }

      for (const entry of entries) {
        account.balance = money(account.balance + entry.amount);
        state.billingLedger.push(entry);
      }
      if (entries.length > 0) {
        state.audit.push(this.auditEvent({ accountId, workspaceId, type: "billing.settled", sourceEventId }));
      }
      return { entries: entries.map(clone), account: clone(account) };
    });
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
      packages: this.packages(),
      account: clone(state.accounts[accountId] ?? { id: accountId, balance: 0, frozen: 0 }),
      workspaces: Object.values(state.workspaces).filter((workspace) => workspace.ownerAccountId === accountId).map(clone),
      billingLedger: state.billingLedger.filter((entry) => entry.accountId === accountId).map(clone),
      audit: state.audit.filter((entry) => entry.accountId === accountId).map(clone)
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

  ledgerEntry({ workspaceId, accountId, type, amount, sourceEventId }) {
    return {
      id: makeId("ledger", accountId, workspaceId, type, sourceEventId, String(Date.now())),
      workspaceId,
      accountId,
      type,
      amount: money(Number(amount)),
      currency: "CNY",
      sourceEventId,
      createdAt: now()
    };
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
