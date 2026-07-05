
import { resolveWorkspaceOwner } from "../management-model.js";
import { clone, makeId, makeToken, now } from "./core-utils.js";
import { ensureUserWallet } from "./wallet-service.js";
import { pricingMarkup } from "./pricing-service.js";
import {
  latestWorkspaceForAccount,
  storageDestroyed,
  workspaceByIdOrSlug
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

function workspaceRuntimeSnapshot({ workspaceId, entry = {}, compute, storage, attachment }) {
  return {
    provider: entry.provider || compute.provider || storage.provider,
    packageId: compute.packageId,
    attachmentId: attachment.id,
    computeAllocationId: compute.id,
    storageId: storage.id,
    currentAttachmentId: attachment.id,
    currentComputeAllocationId: compute.id,
    state: "running",
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
      mountPath: attachment.mountPath,
      localPath: storage.localPath
    },
    runtime: {
      kind: "one-person-lab-app",
      webui: "one-person-lab-app",
      status: entry.status || "ready"
    },
    billing: {
      model: "resource_scoped",
      computeAllocationId: compute.id,
      storageId: storage.id,
      attachmentId: attachment.id,
      minimumBillableHours: 1
    }
  };
}

function resourceForAccount(resources, accountId, resourceId, errorName) {
  const resource = (resources || []).find((item) => item.id === resourceId && item.ownerAccountId === accountId);
  if (!resource) throw new Error(errorName);
  return resource;
}

function resourceIsDestroyed(resource) {
  return !resource || resource.status === "destroyed" || resource.billingStatus === "stopped";
}

function suspendLegacyWorkspaceRuntime(workspace, computeAllocationId) {
  workspace.state = "suspended";
  workspace.runtimeStatus = "suspended";
  workspace.currentComputeAllocationId = "";
  workspace.computeAllocationId = "";
  workspace.currentAttachmentId = "";
  workspace.attachmentId = "";
  workspace.updatedAt = now();
  workspace.server = {
    ...(workspace.server || {}),
    status: "suspended",
    billingStatus: "stopped",
    id: computeAllocationId
  };
  workspace.docker = {
    ...(workspace.docker || {}),
    status: "unavailable"
  };
  workspace.runtime = {
    ...(workspace.runtime || {}),
    status: "suspended"
  };
}

function legacyComputeCleanupEligible(compute) {
  if (!compute) return false;
  if (compute.status === "destroyed" || compute.billingStatus === "stopped") return false;
  const machineName = String(compute.machineName || compute.providerData?.machineName || "").trim();
  if (machineName) return false;
  return compute.status === "destroying" || compute.status === "failed";
}

export class WorkspaceLifecycleService extends OplDomainService {
  async createWorkspace(input) {
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
      state.computeAllocations ??= [];
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
      const compute = resourceForAccount(state.computeAllocations, accountId, attachment.computeAllocationId, "compute_allocation_not_found");
      const storage = resourceForAccount(state.storageVolumes, accountId, attachment.storageId, "storage_volume_not_found");
      if (compute.status === "destroyed") throw new Error("compute_allocation_destroyed");
      if (storage.status === "destroyed") throw new Error("storage_volume_destroyed");

      packagePlan = this.getPackage(compute.packageId);
      workspaceId = makeId("ws", accountId, workspaceName, storage.id);
      const existing = state.workspaces[workspaceId];
      token = existing?.access?.token || makeToken(workspaceId);
      const operation = this.startRuntimeOperation({ state, accountId, workspaceId, operationType: "create_workspace" });
      attachmentSnapshot = clone(attachment);
      computeSnapshot = clone(compute);
      storageSnapshot = clone(storage);
      return { existing: Boolean(existing), operationId: operation.id, workspace: existing ? clone(existing) : null };
    });

    const slug = reservation.workspace?.slug || workspaceEntrySlug(workspaceName, workspaceId);
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
      const compute = resourceForAccount(state.computeAllocations, accountId, attachment.computeAllocationId, "compute_allocation_not_found");
      const storage = resourceForAccount(state.storageVolumes, accountId, attachment.storageId, "storage_volume_not_found");
      const runtimeSnapshot = workspaceRuntimeSnapshot({ workspaceId, entry, compute, storage, attachment });
      runtimeSnapshot.billing.priceMarkup = pricingMarkup(this.pricing);
      const existing = state.workspaces[workspaceId];
      const workspace = existing || {
        id: workspaceId,
        ownerAccountId: accountId,
        ownerUserId: account.id,
        owner,
        name: workspaceName,
        slug: entry.slug || slug,
        url: entry.url || this.runtimeProvider.workspaceUrl({ workspaceId, slug, token }),
        access: {
          mode: "long_lived_url_token",
          requiresLogin: false,
          token,
          tokenStatus: "active",
          rotationPolicy: "reset_or_delete_on_leak"
        },
        createdAt: now(),
        updatedAt: now()
      };
      Object.assign(workspace, runtimeSnapshot, {
        ownerAccountId: accountId,
        ownerUserId: account.id,
        owner,
        name: workspaceName,
        slug: existing?.slug || entry.slug || slug,
        url: existing?.url || entry.url || this.runtimeProvider.workspaceUrl({ workspaceId, slug, token }),
        access: {
          ...(existing?.access || {}),
          mode: "long_lived_url_token",
          requiresLogin: false,
          token,
          tokenStatus: "active",
          rotationPolicy: "reset_or_delete_on_leak"
        },
        createdAt: existing?.createdAt || workspace.createdAt || now(),
        updatedAt: now()
      });
      state.workspaces[workspaceId] = workspace;
      const sourceEventId = `workspace_entry:${workspaceId}:${existing ? "runtime_rebound" : "created"}`;
      state.billingLedger.push({
        ...this.ledgerEntry({
          state,
          workspaceId,
          accountId,
          type: existing ? "workspace_runtime_rebound" : "workspace_entry_created",
          amount: 0,
          sourceEventId,
          metadata: {
            computeAllocationId: compute.id,
            storageId: storage.id,
            attachmentId: attachment.id
          }
        }),
        computeAllocationId: compute.id,
        storageId: storage.id,
        attachmentId: attachment.id
      });
      state.audit.push(this.auditEvent({ accountId, workspaceId, type: existing ? "workspace.runtime_rebound" : "workspace.created", sourceEventId: workspaceId }));
      this.recordEvidence({
        state,
        type: existing ? "workspace.runtime_rebound" : "workspace.created",
        accountId,
        workspace,
        packagePlan,
        billingRefs: state.billingLedger.filter((entry) =>
          entry.accountId === accountId &&
          (entry.computeAllocationId === compute.id || entry.storageId === storage.id || entry.attachmentId === attachment.id)
        ),
        continuation: {
          action: "open_workspace_url",
          uri: workspace.url
        }
      });
      return clone(workspace);
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

  async cleanupWorkspaceAccess({
    accountId = "",
    workspaceIds = [],
    reason = "operator_cleanup",
    legacyComputeAllocationIds = [],
    cloudCleanupConfirmed = false
  } = {}) {
    return this.store.update((state) => {
      state.computeAllocations ??= [];
      state.storageVolumes ??= [];
      state.storageAttachments ??= [];
      const targetWorkspaceIds = new Set((workspaceIds || []).filter(Boolean));
      const targetLegacyComputeIds = new Set((legacyComputeAllocationIds || []).filter(Boolean));
      const storageById = new Map(state.storageVolumes.map((resource) => [resource.id, resource]));
      const cleaned = [];
      const skipped = [];
      const legacyComputeCleaned = [];
      const legacyComputeSkipped = [];

      for (const computeAllocationId of targetLegacyComputeIds) {
        const compute = state.computeAllocations.find((item) => item.id === computeAllocationId && (!accountId || item.ownerAccountId === accountId));
        if (!compute) {
          legacyComputeSkipped.push({ computeAllocationId, reason: "compute_allocation_not_found" });
          continue;
        }
        if (!cloudCleanupConfirmed) {
          legacyComputeSkipped.push({ computeAllocationId, reason: "cloud_cleanup_not_confirmed" });
          continue;
        }
        if (!legacyComputeCleanupEligible(compute)) {
          legacyComputeSkipped.push({ computeAllocationId, reason: "not_legacy_cleanup_eligible" });
          continue;
        }
        ensureUserWallet(state, { accountId: compute.ownerAccountId, userId: compute.ownerUserId });
        compute.status = "destroyed";
        compute.billingStatus = "stopped";
        compute.error = "";
        compute.safeMessage = "";
        compute.retryable = false;
        compute.destroyedAt = now();
        compute.updatedAt = now();
        compute.attachedStorageIds = [];

        for (const attachment of state.storageAttachments || []) {
          if (attachment.ownerAccountId !== compute.ownerAccountId) continue;
          if (attachment.computeAllocationId !== computeAllocationId) continue;
          if (attachment.status !== "attached" && attachment.status !== "detaching") continue;
          attachment.status = "detached";
          attachment.detachedAt = now();
          attachment.updatedAt = now();
        }
        for (const workspace of Object.values(state.workspaces || {})) {
          if (workspace.ownerAccountId !== compute.ownerAccountId) continue;
          if ((workspace.currentComputeAllocationId || workspace.computeAllocationId || "") !== computeAllocationId) continue;
          suspendLegacyWorkspaceRuntime(workspace, computeAllocationId);
        }
        const sourceEventId = `legacy_compute_cleanup:${computeAllocationId}:${reason}`;
        state.billingLedger.push(this.ledgerEntry({
          state,
          workspaceId: "resource",
          accountId: compute.ownerAccountId,
          type: "compute_legacy_cleaned",
          amount: 0,
          sourceEventId,
          metadata: {
            reason,
            computeAllocationId,
            cloudCleanupConfirmed: true,
            providerResourceId: compute.providerResourceId || "",
            nodeName: compute.nodeName || ""
          }
        }));
        legacyComputeCleaned.push({
          computeAllocationId,
          accountId: compute.ownerAccountId,
          status: compute.status,
          billingStatus: compute.billingStatus
        });
      }

      for (const workspace of Object.values(state.workspaces || {})) {
        if (accountId && workspace.ownerAccountId !== accountId) continue;
        if (targetWorkspaceIds.size && !targetWorkspaceIds.has(workspace.id)) continue;
        const tokenStatus = workspace.access?.tokenStatus || "unknown";
        if (tokenStatus !== "active") {
          skipped.push({ workspaceId: workspace.id, reason: "token_not_active", tokenStatus });
          continue;
        }
        const stableStorageId = String(workspace.storageId || "").trim();
        const storage = stableStorageId ? storageById.get(stableStorageId) : null;
        const unavailableBecause = [
          workspace.ownerAccountId ? "" : "owner_account_missing",
          stableStorageId ? "" : "stable_storage_identity_missing",
          stableStorageId && resourceIsDestroyed(storage) ? "storage_unavailable" : ""
        ].filter(Boolean);

        if (!unavailableBecause.length) {
          skipped.push({ workspaceId: workspace.id, reason: "resources_still_active", tokenStatus });
          continue;
        }

        workspace.access.tokenStatus = "unavailable";
        workspace.updatedAt = now();
        const sourceEventId = `workspace_access_cleanup:${workspace.id}:${reason}`;
        state.billingLedger.push(this.ledgerEntry({
          state,
          workspaceId: workspace.id,
          accountId: workspace.ownerAccountId,
          type: "workspace_access_cleaned",
          amount: 0,
          sourceEventId,
          metadata: {
            reason,
            unavailableBecause,
            computeAllocationId: workspace.computeAllocationId,
            storageId: workspace.storageId,
            attachmentId: workspace.attachmentId
          }
        }));
        this.recordEvidence({
          state,
          type: "workspace.access_cleanup",
          accountId: workspace.ownerAccountId,
          workspace,
          continuation: { action: "create_workspace_entry" }
        });
        cleaned.push({
          workspaceId: workspace.id,
          accountId: workspace.ownerAccountId,
          tokenStatus: workspace.access.tokenStatus,
          unavailableBecause
        });
      }

      const activeStatus = new Set(["creating", "running", "ready", "attaching", "attached", "provisioning", "pending"]);
      return {
        cleaned,
        skipped,
        legacyComputeCleaned,
        legacyComputeSkipped,
        activeResources: {
          compute: state.computeAllocations
            .filter((item) => (!accountId || item.ownerAccountId === accountId) && (activeStatus.has(item.status) || item.billingStatus === "running"))
            .map(({ id, ownerAccountId, packageId, spec, status, billingStatus }) => ({ id, ownerAccountId, packageId, spec, status, billingStatus })),
          storage: state.storageVolumes
            .filter((item) => (!accountId || item.ownerAccountId === accountId) && (activeStatus.has(item.status) || item.billingStatus === "running"))
            .map(({ id, ownerAccountId, packageId, sizeGb, status, billingStatus }) => ({ id, ownerAccountId, packageId, sizeGb, status, billingStatus })),
          attachments: state.storageAttachments
            .filter((item) => (!accountId || item.ownerAccountId === accountId) && activeStatus.has(item.status))
            .map(({ id, ownerAccountId, computeAllocationId, storageId, status }) => ({ id, ownerAccountId, computeAllocationId, storageId, status }))
        }
      };
    });
  }

  async resolveWorkspaceAccess({ slug, workspaceId, token }) {
    const state = await this.store.read();
    const workspace = workspaceByIdOrSlug(state, workspaceId || slug);
    if (!workspace) throw new Error("workspace_not_found");
    if (workspace.access.tokenStatus !== "active") throw new Error("workspace_token_inactive");
    if (workspace.access.token !== token) throw new Error("workspace_token_invalid");
    return clone(workspace);
  }

  async recordCreateWorkspaceFailure({ accountId, workspaceId, operationId, error }) {
    return this.store.update((state) => {
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
}
