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

function resourceForAccount(resources, accountId, resourceId, errorName) {
  const resource = (resources || []).find((item) => item.id === resourceId && item.ownerAccountId === accountId);
  if (!resource) throw new Error(errorName);
  return resource;
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
          url: this.runtimeProvider.workspaceUrl({ workspaceId, slug, token }),
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
          mountPath: attachment.mountPath,
          localPath: storage.localPath,
          storageClass: storage.storageClass
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
