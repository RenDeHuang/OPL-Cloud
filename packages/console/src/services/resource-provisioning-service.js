import { clone, makeId, money, now } from "./core-utils.js";
import {
  accountAvailable,
  addResourceHold,
  appendWalletTransaction,
  ensureUserWallet
} from "./wallet-service.js";
import {
  computePriceSnapshot,
  hourlyStorageAmount,
  packageHoldAmount,
  pricedComputeHourly,
  pricedStorageGbMonth,
  storagePriceSnapshot
} from "./pricing-service.js";
import { OplDomainService } from "./opl-domain-service.js";

function ensureResourceCollections(state) {
  state.computeAllocations ??= [];
  state.storageVolumes ??= [];
  state.storageAttachments ??= [];
  state.resourceUsageLogs ??= [];
}

function findOwnedResource(items, accountId, id, errorName) {
  const resource = (items || []).find((item) => item.id === id && item.ownerAccountId === accountId);
  if (!resource) throw new Error(errorName);
  return resource;
}

function addResourceIds(target, { computeAllocationId = "", storageId = "", attachmentId = "" } = {}) {
  if (computeAllocationId) target.computeAllocationId = computeAllocationId;
  if (storageId) target.storageId = storageId;
  if (attachmentId) target.attachmentId = attachmentId;
  return target;
}

function storagePlanFromSize(packagePlan, sizeGb) {
  return {
    ...packagePlan,
    diskGb: sizeGb
  };
}

function computePoolFromPackage(plan, pricing) {
  return {
    id: `pool-${plan.id}-${plan.server}`,
    packageId: plan.id,
    name: `${plan.name} pool`,
    instanceType: plan.instanceType || plan.server,
    cpu: plan.cpu,
    memoryGb: plan.memoryGb,
    nodePoolId: plan.nodePoolId || "",
    status: plan.available ? "ready" : "missing",
    hourlyPrice: pricedComputeHourly({ packagePlan: plan, pricing }),
    provider: "tencent-tke"
  };
}

function providerRequestId(providerResult = {}) {
  return providerResult.providerRequestId ||
    providerResult.providerData?.createRequestId ||
    providerResult.providerData?.requestId ||
    "";
}

function latestRuntimeOperationForResource(operations = [], resourceId) {
  for (let index = operations.length - 1; index >= 0; index -= 1) {
    if (operations[index]?.resourceId === resourceId) return operations[index];
  }
  return null;
}

function providerNodeIdentity(providerCompute = {}) {
  return String(providerCompute.nodeName || providerCompute.providerData?.machineName || providerCompute.cvmInstanceId || providerCompute.instanceId || "").trim();
}

function computeNodeIdentityError(providerCompute = {}) {
  const error = new Error("compute_allocation_node_identity_required");
  error.safeMessage = "计算资源未返回独占节点，请重试或联系支持。";
  error.providerRequestId = providerRequestId(providerCompute);
  error.retryable = true;
  return error;
}

function suspendWorkspaceRuntime(workspace) {
  workspace.state = "suspended";
  workspace.currentComputeAllocationId = "";
  workspace.currentAttachmentId = "";
  workspace.computeAllocationId = "";
  workspace.attachmentId = "";
  workspace.server = {
    ...(workspace.server || {}),
    status: "suspended",
    billingStatus: "stopped"
  };
  workspace.docker = {
    ...(workspace.docker || {}),
    status: "suspended",
    service: "",
    localUrl: ""
  };
  workspace.runtime = {
    ...(workspace.runtime || {}),
    status: "suspended"
  };
  if (workspace.disk) {
    workspace.disk.status = workspace.disk.status === "destroyed" ? "destroyed" : "detached_retained";
  }
  if (workspace.access) workspace.access.tokenStatus = "active";
  workspace.updatedAt = now();
}

function destroyWorkspaceStorage(workspace) {
  workspace.state = "destroyed";
  workspace.currentComputeAllocationId = "";
  workspace.currentAttachmentId = "";
  workspace.computeAllocationId = "";
  workspace.attachmentId = "";
  workspace.server = {
    ...(workspace.server || {}),
    status: "destroyed",
    billingStatus: "stopped"
  };
  workspace.docker = {
    ...(workspace.docker || {}),
    status: "destroyed",
    service: "",
    localUrl: ""
  };
  workspace.disk = {
    ...(workspace.disk || {}),
    status: "destroyed",
    billingStatus: "stopped"
  };
  workspace.runtime = {
    ...(workspace.runtime || {}),
    status: "destroyed"
  };
  if (workspace.access) workspace.access.tokenStatus = "unavailable";
  workspace.updatedAt = now();
}

function workspaceCurrentAttachmentId(workspace) {
  return workspace.currentAttachmentId ?? workspace.attachmentId ?? "";
}

function workspaceCurrentComputeAllocationId(workspace) {
  return workspace.currentComputeAllocationId ?? workspace.computeAllocationId ?? "";
}

function resourceOperationId(accountId, resourceId, operationType, sequence) {
  return makeId("op", accountId, resourceId, operationType, String(sequence));
}

function isOperationLockExpired(operation, lockTimeoutMs, timestamp = Date.now()) {
  const updatedAt = Date.parse(operation?.updatedAt || operation?.createdAt || "");
  if (!Number.isFinite(updatedAt)) return true;
  return timestamp - updatedAt > lockTimeoutMs;
}

export class ResourceProvisioningService extends OplDomainService {
  computePools() {
    return this.packages().map((plan) => computePoolFromPackage(plan, this.pricing));
  }

  async computeAllocations({ accountId }) {
    const state = await this.store.read();
    ensureResourceCollections(state);
    return (state.computeAllocations || [])
      .filter((item) => !accountId || item.ownerAccountId === accountId)
      .map(clone);
  }

  async computeAllocation({ accountId, computeAllocationId }) {
    const state = await this.store.read();
    ensureResourceCollections(state);
    return clone(findOwnedResource(state.computeAllocations, accountId, computeAllocationId, "compute_allocation_not_found"));
  }

  async createComputeAllocation({ accountId, userId = "", packageId, name = "" }) {
    const packagePlan = this.getPackage(packageId);
    const hold = packageHoldAmount({ packagePlan, pricing: this.pricing });
    let allocationId = "";

    const reservation = await this.store.update((state) => {
      ensureResourceCollections(state);
      this.assertBillingReconciliationAllowsProvisioning(state);
      const account = ensureUserWallet(state, { accountId });
      allocationId = makeId("compute", accountId, packageId, name || packagePlan.name, String(state.computeAllocations.length));
      const existing = state.computeAllocations.find((item) => item.id === allocationId);
      if (existing) return { existing: true, compute: clone(existing) };
      if (accountAvailable(account) < hold.compute) throw new Error("insufficient_compute_hold_balance");

      const balanceBefore = money(Number(account.balance || 0));
      const frozenBefore = money(Number(account.frozen || 0));
      const operationId = resourceOperationId(accountId, allocationId, "create_compute_allocation", state.runtimeOperations.length);
      addResourceHold(account, "compute", allocationId, hold.compute);
      state.runtimeOperations.push({
        id: operationId,
        accountId,
        workspaceId: "resource",
        resourceType: "compute_allocation",
        resourceId: allocationId,
        operationType: "create_compute_allocation",
        status: "queued",
        attempts: 0,
        createdAt: now(),
        updatedAt: now()
      });
      const sourceEventId = `compute_allocation:${allocationId}:created`;
      const ledger = addResourceIds(this.ledgerEntry({
        state,
        workspaceId: "resource",
        accountId,
        type: "compute_hold",
        amount: hold.compute,
        sourceEventId,
        holdType: "compute",
        metadata: {
          computeAllocationId: allocationId,
          ownerAccountId: accountId,
          ownerUserId: account.id,
          packageId,
          holdDays: 7,
          ...computePriceSnapshot({ packagePlan, pricing: this.pricing })
        }
      }), { computeAllocationId: allocationId });
      state.billingLedger.push(ledger);
      appendWalletTransaction(state, {
        user: account,
        accountId,
        workspaceId: "resource",
        type: "compute_hold",
        amount: 0,
        sourceEventId,
        ledgerEntryId: ledger.id,
        balanceBefore,
        balanceAfter: account.balance,
        frozenBefore,
        frozenAfter: account.frozen,
        metadata: {
          computeAllocationId: allocationId,
          holdAmount: hold.compute,
          packageId
        }
      });

      const compute = {
        id: allocationId,
        ownerAccountId: accountId,
        ownerUserId: account.id,
        name: name || packagePlan.name,
        packageId,
        poolId: computePoolFromPackage(packagePlan, this.pricing).id,
        nodePoolId: packagePlan.nodePoolId || "",
        operationId,
        provider: this.runtimeProvider.name || "unknown",
        providerResourceId: "",
        status: "provisioning",
        billingStatus: "active",
        spec: packagePlan.server,
        image: "",
        hourlyPrice: pricedComputeHourly({ packagePlan, pricing: this.pricing }),
        holdAmount: hold.compute,
        balanceImpact: {
          balanceBefore,
          frozenBefore,
          frozenAfter: money(Number(account.frozen || 0)),
          availableAfter: accountAvailable(account)
        },
        createdAt: now(),
        updatedAt: now()
      };
      state.computeAllocations.push(compute);
      this.recordProvisioningUsage({
        state,
        account,
        accountId,
        resourceType: "compute",
        computeAllocationId: allocationId,
        quantity: 1,
        unit: "resource",
        unitPrice: pricedComputeHourly({ packagePlan, pricing: this.pricing }),
        amount: 0,
        sourceEventId,
        metadata: {
          packageId,
          status: "provisioning"
        }
      });
      state.audit.push(this.auditEvent({ accountId, type: "compute.created", sourceEventId: allocationId }));
      return { existing: false, compute: clone(compute) };
    });

    if (reservation.existing) return reservation.compute;

    return reservation.compute;
  }

  async processPendingResourceProvisioning({ limit = 1, lockTimeoutMs = 600_000 } = {}) {
    const claims = await this.claimPendingComputeAllocations({ limit, lockTimeoutMs });
    const completed = [];
    const failed = [];

    for (const claim of claims) {
      try {
        const packagePlan = this.getPackage(claim.packageId);
        const providerCompute = await this.createProviderCompute({ compute: claim, packagePlan });
        if (!providerNodeIdentity(providerCompute)) {
          throw computeNodeIdentityError(providerCompute);
        }
        await this.completeProviderCompute({
          accountId: claim.ownerAccountId,
          allocationId: claim.id,
          providerCompute
        });
        completed.push(claim.id);
      } catch (error) {
        await this.markResourceFailed({
          collection: "computeAllocations",
          accountId: claim.ownerAccountId,
          resourceId: claim.id,
          error,
          auditType: "compute.create_failed"
        });
        failed.push({ id: claim.id, error: error.message });
      }
    }

    return { processed: claims.length, completed, failed };
  }

  async claimPendingComputeAllocations({ limit = 1, lockTimeoutMs = 600_000 } = {}) {
    const timestamp = Date.now();
    return this.store.update((state) => {
      ensureResourceCollections(state);
      const claims = [];
      for (const compute of state.computeAllocations || []) {
        if (claims.length >= limit) break;
        if (compute.status !== "provisioning") continue;
        const operation = state.runtimeOperations.find((item) => item.resourceId === compute.id);
        if (operation?.status === "running" && !isOperationLockExpired(operation, lockTimeoutMs, timestamp)) continue;
        compute.updatedAt = now();
        compute.provisioningStage = "cloud_resource_creating";
        if (operation) {
          operation.status = "running";
          operation.attempts = Number(operation.attempts || 0) + 1;
          operation.updatedAt = now();
        }
        claims.push(clone(compute));
      }
      return claims;
    });
  }

  async completeProviderCompute({ accountId, allocationId, providerCompute }) {
    return this.store.update((state) => {
      const compute = findOwnedResource(state.computeAllocations, accountId, allocationId, "compute_allocation_not_found");
      Object.assign(compute, {
        providerResourceId: providerCompute.providerResourceId || providerCompute.id || compute.providerResourceId,
        operationId: providerCompute.operationId || compute.operationId,
        poolId: providerCompute.poolId || compute.poolId,
        nodePoolId: providerCompute.nodePoolId || compute.nodePoolId,
        cvmInstanceId: providerCompute.cvmInstanceId || providerCompute.instanceId || compute.cvmInstanceId,
        instanceId: providerCompute.instanceId || compute.instanceId,
        machineName: providerCompute.machineName || providerCompute.providerData?.machineName || compute.machineName,
        nodeName: providerCompute.nodeName || compute.nodeName,
        privateIp: providerCompute.privateIp || compute.privateIp || "",
        publicIp: providerCompute.publicIp || compute.publicIp || "",
        status: providerCompute.status || "running",
        billingStatus: providerCompute.billingStatus || compute.billingStatus,
        spec: providerCompute.spec || compute.spec,
        image: providerCompute.image || compute.image,
        localPath: providerCompute.localPath || compute.localPath,
        composePath: providerCompute.composePath || compute.composePath,
        runtime: providerCompute.runtime ? clone(providerCompute.runtime) : compute.runtime,
        providerData: clone(providerCompute.providerData || providerCompute),
        providerRequestId: providerRequestId(providerCompute),
        provisioningStage: "runtime_ready",
        lastProviderSyncAt: now(),
        updatedAt: now()
      });
      const operation = state.runtimeOperations.find((item) => item.id === compute.operationId || item.resourceId === allocationId);
      if (operation) {
        operation.id = compute.operationId || operation.id;
        operation.status = "completed";
        operation.providerRequestId = providerRequestId(providerCompute);
        operation.updatedAt = now();
      }
      return clone(compute);
    });
  }

  async destroyComputeAllocation({ accountId, computeAllocationId, confirm }) {
    if (confirm !== true) throw new Error("compute_destroy_confirmation_required");
    const compute = await this.store.update((state) => {
      ensureResourceCollections(state);
      const current = findOwnedResource(state.computeAllocations, accountId, computeAllocationId, "compute_allocation_not_found");
      const operationId = resourceOperationId(accountId, computeAllocationId, "destroy_compute_allocation", state.runtimeOperations.length);
      state.runtimeOperations.push({
        id: operationId,
        accountId,
        workspaceId: "resource",
        resourceType: "compute_allocation",
        resourceId: computeAllocationId,
        operationType: "destroy_compute_allocation",
        status: "running",
        attempts: 1,
        createdAt: now(),
        updatedAt: now()
      });
      current.operationId = operationId;
      current.status = "destroying";
      current.updatedAt = now();
      return clone(current);
    });

    try {
      if (typeof this.runtimeProvider.destroyComputeAllocation === "function") {
        await this.runtimeProvider.destroyComputeAllocation({ computeAllocation: compute });
      }
    } catch (error) {
      await this.markResourceFailed({ collection: "computeAllocations", accountId, resourceId: computeAllocationId, error, auditType: "compute.destroy_failed" });
      throw error;
    }

    return this.store.update((state) => {
      const current = findOwnedResource(state.computeAllocations, accountId, computeAllocationId, "compute_allocation_not_found");
      const operation = latestRuntimeOperationForResource(state.runtimeOperations, computeAllocationId);
      ensureUserWallet(state, { accountId, userId: current.ownerUserId });
      this.releaseHoldToLedger({ state, accountId, workspaceId: "resource", holdType: "compute", resourceId: computeAllocationId, sourceEventId: "destroy_compute" });
      current.status = "destroyed";
      current.billingStatus = "stopped";
      current.error = "";
      current.safeMessage = "";
      current.providerRequestId = "";
      current.retryable = false;
      current.destroyedAt = now();
      current.updatedAt = now();
      current.attachedStorageIds = [];
      for (const attachment of state.storageAttachments || []) {
        if (attachment.ownerAccountId !== accountId) continue;
        if (attachment.computeAllocationId !== computeAllocationId) continue;
        if (attachment.status !== "attached" && attachment.status !== "detaching") continue;
        attachment.status = "detached";
        attachment.detachedAt = now();
        attachment.updatedAt = now();
        const storage = state.storageVolumes.find((item) => item.id === attachment.storageId && item.ownerAccountId === accountId);
        if (storage) {
          storage.attachmentIds = (storage.attachmentIds || []).filter((id) => id !== attachment.id);
          if (storage.status === "attached") storage.status = "available";
          storage.updatedAt = now();
        }
        state.billingLedger.push(addResourceIds(this.ledgerEntry({
          state,
          workspaceId: "resource",
          accountId,
          type: "storage_detached",
          amount: 0,
          sourceEventId: "destroy_compute_detach_storage",
          metadata: {
            computeAllocationId,
            storageId: attachment.storageId,
            attachmentId: attachment.id
          }
        }), { computeAllocationId, storageId: attachment.storageId, attachmentId: attachment.id }));
      }
      for (const workspace of Object.values(state.workspaces || {})) {
        if (workspace.ownerAccountId !== accountId) continue;
        if (workspaceCurrentComputeAllocationId(workspace) !== computeAllocationId) continue;
        suspendWorkspaceRuntime(workspace);
      }
      state.billingLedger.push(addResourceIds(this.ledgerEntry({
        state,
        workspaceId: "resource",
        accountId,
        type: "compute_destroyed",
        amount: 0,
        sourceEventId: "destroy_compute",
        metadata: { computeAllocationId }
      }), { computeAllocationId }));
      if (operation) {
        operation.status = "completed";
        operation.updatedAt = now();
      }
      return clone(current);
    });
  }

  async createStorageVolume({ accountId, userId = "", packageId, sizeGb = null, name = "" }) {
    const packagePlan = this.getPackage(packageId);
    const normalizedSizeGb = Math.max(1, Number(sizeGb || packagePlan.diskGb));
    const storagePlan = storagePlanFromSize(packagePlan, normalizedSizeGb);
    const hold = packageHoldAmount({ packagePlan: storagePlan, pricing: this.pricing });
    let storageId = "";

    const reservation = await this.store.update((state) => {
      ensureResourceCollections(state);
      this.assertBillingReconciliationAllowsProvisioning(state);
      const account = ensureUserWallet(state, { accountId });
      storageId = makeId("storage", accountId, packageId, String(normalizedSizeGb), name || "volume", String(state.storageVolumes.length));
      const existing = state.storageVolumes.find((item) => item.id === storageId);
      if (existing) return { existing: true, storage: clone(existing) };
      if (accountAvailable(account) < hold.storage) throw new Error("insufficient_storage_hold_balance");

      const balanceBefore = money(Number(account.balance || 0));
      const frozenBefore = money(Number(account.frozen || 0));
      const operationId = resourceOperationId(accountId, storageId, "create_storage_volume", state.runtimeOperations.length);
      addResourceHold(account, "storage", storageId, hold.storage);
      state.runtimeOperations.push({
        id: operationId,
        accountId,
        workspaceId: "resource",
        resourceType: "storage_volume",
        resourceId: storageId,
        operationType: "create_storage_volume",
        status: "running",
        attempts: 1,
        createdAt: now(),
        updatedAt: now()
      });
      const sourceEventId = `storage_volume:${storageId}:created`;
      const ledger = addResourceIds(this.ledgerEntry({
        state,
        workspaceId: "resource",
        accountId,
        type: "storage_hold",
        amount: hold.storage,
        sourceEventId,
        holdType: "storage",
        metadata: {
          storageId,
          ownerAccountId: accountId,
          ownerUserId: account.id,
          packageId,
          sizeGb: normalizedSizeGb,
          holdDays: 7,
          ...storagePriceSnapshot({ pricing: this.pricing, sizeGb: normalizedSizeGb })
        }
      }), { storageId });
      state.billingLedger.push(ledger);
      appendWalletTransaction(state, {
        user: account,
        accountId,
        workspaceId: "resource",
        type: "storage_hold",
        amount: 0,
        sourceEventId,
        ledgerEntryId: ledger.id,
        balanceBefore,
        balanceAfter: account.balance,
        frozenBefore,
        frozenAfter: account.frozen,
        metadata: {
          storageId,
          holdAmount: hold.storage,
          packageId,
          sizeGb: normalizedSizeGb
        }
      });

      const storage = {
        id: storageId,
        ownerAccountId: accountId,
        ownerUserId: account.id,
        name: name || `${normalizedSizeGb}GB workspace storage`,
        packageId,
        operationId,
        provider: this.runtimeProvider.name || "unknown",
        providerResourceId: "",
        status: "provisioning",
        billingStatus: "active",
        sizeGb: normalizedSizeGb,
        storageClassId: packagePlan.storageClassId,
        gbMonthPrice: pricedStorageGbMonth(this.pricing),
        hourlyEstimate: hourlyStorageAmount({ packagePlan: storagePlan, pricing: this.pricing, hours: 1 }),
        holdAmount: hold.storage,
        balanceImpact: {
          balanceBefore,
          frozenBefore,
          frozenAfter: money(Number(account.frozen || 0)),
          availableAfter: accountAvailable(account)
        },
        createdAt: now(),
        updatedAt: now()
      };
      state.storageVolumes.push(storage);
      this.recordProvisioningUsage({
        state,
        account,
        accountId,
        resourceType: "storage",
        storageId,
        quantity: normalizedSizeGb,
        unit: "gb",
        unitPrice: pricedStorageGbMonth(this.pricing),
        amount: 0,
        sourceEventId,
        metadata: {
          packageId,
          sizeGb: normalizedSizeGb,
          status: "provisioning"
        }
      });
      state.audit.push(this.auditEvent({ accountId, type: "storage.created", sourceEventId: storageId }));
      return { existing: false, storage: clone(storage) };
    });

    if (reservation.existing) return reservation.storage;

    try {
      const providerStorage = await this.createProviderStorage({ storage: reservation.storage, packagePlan: storagePlan });
      return this.store.update((state) => {
        const storage = findOwnedResource(state.storageVolumes, accountId, storageId, "storage_volume_not_found");
        Object.assign(storage, {
          providerResourceId: providerStorage.providerResourceId || providerStorage.id || storage.providerResourceId,
          operationId: providerStorage.operationId || storage.operationId,
          status: providerStorage.status || "available",
          billingStatus: providerStorage.billingStatus || storage.billingStatus,
          localPath: providerStorage.localPath || storage.localPath,
          storageClass: providerStorage.storageClass || storage.storageClass,
          providerData: clone(providerStorage),
          updatedAt: now()
        });
        const operation = state.runtimeOperations.find((item) => item.id === storage.operationId || item.resourceId === storageId);
        if (operation) {
          operation.id = storage.operationId || operation.id;
          operation.status = "completed";
          operation.providerRequestId = providerRequestId(providerStorage);
          operation.updatedAt = now();
        }
        return clone(storage);
      });
    } catch (error) {
      await this.markResourceFailed({ collection: "storageVolumes", accountId, resourceId: storageId, error, auditType: "storage.create_failed" });
      throw error;
    }
  }

  async destroyStorageVolume({ accountId, storageId, confirmDataLoss }) {
    if (confirmDataLoss !== true) throw new Error("storage_destroy_confirmation_required");
    const storage = await this.store.update((state) => {
      ensureResourceCollections(state);
      const current = findOwnedResource(state.storageVolumes, accountId, storageId, "storage_volume_not_found");
      const activeAttachment = state.storageAttachments.find((item) =>
        item.ownerAccountId === accountId &&
        item.storageId === storageId &&
        item.status === "attached"
      );
      if (activeAttachment) throw new Error("storage_has_active_attachment");
      current.status = "destroying";
      current.updatedAt = now();
      return clone(current);
    });

    if (typeof this.runtimeProvider.destroyStorageVolume === "function") {
      await this.runtimeProvider.destroyStorageVolume({ storage });
    }

    return this.store.update((state) => {
      const current = findOwnedResource(state.storageVolumes, accountId, storageId, "storage_volume_not_found");
      ensureUserWallet(state, { accountId, userId: current.ownerUserId });
      this.releaseHoldToLedger({ state, accountId, workspaceId: "resource", holdType: "storage", resourceId: storageId, sourceEventId: "destroy_storage" });
      current.status = "destroyed";
      current.billingStatus = "stopped";
      current.destroyedAt = now();
      current.updatedAt = now();
      for (const workspace of Object.values(state.workspaces || {})) {
        if (workspace.ownerAccountId !== accountId) continue;
        if (workspace.storageId !== storageId) continue;
        destroyWorkspaceStorage(workspace);
      }
      state.billingLedger.push(addResourceIds(this.ledgerEntry({
        state,
        workspaceId: "resource",
        accountId,
        type: "storage_destroyed",
        amount: 0,
        sourceEventId: "destroy_storage",
        metadata: { storageId }
      }), { storageId }));
      return clone(current);
    });
  }

  async attachStorage({ accountId, computeAllocationId, storageId, mountPath = "/data" }) {
    let attachmentId = "";
    const reservation = await this.store.update((state) => {
      ensureResourceCollections(state);
      const compute = findOwnedResource(state.computeAllocations, accountId, computeAllocationId, "compute_allocation_not_found");
      const storage = findOwnedResource(state.storageVolumes, accountId, storageId, "storage_volume_not_found");
      if (compute.status === "destroyed") throw new Error("compute_allocation_destroyed");
      if (compute.status !== "running") throw new Error("compute_allocation_not_running");
      if (storage.status === "destroyed") throw new Error("storage_volume_destroyed");
      const active = state.storageAttachments.find((item) =>
        item.ownerAccountId === accountId &&
        item.storageId === storageId &&
        item.status === "attached"
      );
      if (active) throw new Error("storage_already_attached");

      attachmentId = makeId("attach", accountId, computeAllocationId, storageId, mountPath, String(state.storageAttachments.length));
      const operationId = resourceOperationId(accountId, attachmentId, "attach_storage", state.runtimeOperations.length);
      state.runtimeOperations.push({
        id: operationId,
        accountId,
        workspaceId: "resource",
        resourceType: "storage_attachment",
        resourceId: attachmentId,
        operationType: "attach_storage",
        status: "running",
        attempts: 1,
        createdAt: now(),
        updatedAt: now()
      });
      const attachment = {
        id: attachmentId,
        ownerAccountId: accountId,
        computeAllocationId,
        storageId,
        mountPath,
        operationId,
        provider: this.runtimeProvider.name || "unknown",
        providerAttachmentId: "",
        status: "attaching",
        createdAt: now(),
        updatedAt: now()
      };
      state.storageAttachments.push(attachment);
      const sourceEventId = `storage_attachment:${attachmentId}:created`;
      state.billingLedger.push(addResourceIds(this.ledgerEntry({
        state,
        workspaceId: "resource",
        accountId,
        type: "storage_attached",
        amount: 0,
        sourceEventId,
        metadata: { computeAllocationId, storageId, attachmentId, mountPath }
      }), { computeAllocationId, storageId, attachmentId }));
      this.recordProvisioningUsage({
        state,
        account: ensureUserWallet(state, { accountId, userId: compute.ownerUserId }),
        accountId,
        resourceType: "attachment",
        computeAllocationId,
        storageId,
        attachmentId,
        quantity: 1,
        unit: "attachment",
        unitPrice: 0,
        amount: 0,
        sourceEventId,
        metadata: { mountPath }
      });
      state.audit.push(this.auditEvent({ accountId, type: "storage.attached", sourceEventId: attachmentId }));
      return { attachment: clone(attachment), compute: clone(compute), storage: clone(storage) };
    });

    try {
      const providerAttachment = await this.attachProviderStorage(reservation);
      return this.store.update((state) => {
        const attachment = findOwnedResource(state.storageAttachments, accountId, attachmentId, "storage_attachment_not_found");
        const compute = findOwnedResource(state.computeAllocations, accountId, computeAllocationId, "compute_allocation_not_found");
        const storage = findOwnedResource(state.storageVolumes, accountId, storageId, "storage_volume_not_found");
        Object.assign(attachment, {
          providerAttachmentId: providerAttachment.providerAttachmentId || providerAttachment.id || attachment.providerAttachmentId,
          operationId: providerAttachment.operationId || attachment.operationId,
          status: providerAttachment.status || "attached",
          localPath: providerAttachment.localPath || attachment.localPath,
          composePath: providerAttachment.composePath || attachment.composePath,
          providerData: clone(providerAttachment),
          updatedAt: now()
        });
        const operation = state.runtimeOperations.find((item) => item.id === attachment.operationId || item.resourceId === attachmentId);
        if (operation) {
          operation.id = attachment.operationId || operation.id;
          operation.status = "completed";
          operation.providerRequestId = providerRequestId(providerAttachment);
          operation.updatedAt = now();
        }
        compute.attachedStorageIds = [...new Set([...(compute.attachedStorageIds || []), storageId])];
        storage.attachmentIds = [...new Set([...(storage.attachmentIds || []), attachmentId])];
        if (providerAttachment.computeStatus) compute.status = providerAttachment.computeStatus;
        storage.status = providerAttachment.storageStatus || "attached";
        compute.updatedAt = now();
        storage.updatedAt = now();
        return clone(attachment);
      });
    } catch (error) {
      await this.markResourceFailed({ collection: "storageAttachments", accountId, resourceId: attachmentId, error, auditType: "storage.attach_failed" });
      throw error;
    }
  }

  async detachStorage({ accountId, attachmentId, confirm }) {
    if (confirm !== true) throw new Error("storage_detach_confirmation_required");
    const attachment = await this.store.update((state) => {
      ensureResourceCollections(state);
      const current = findOwnedResource(state.storageAttachments, accountId, attachmentId, "storage_attachment_not_found");
      if (current.status === "detached") return clone(current);
      if (!["attached", "detaching"].includes(current.status)) throw new Error("storage_attachment_not_attached");
      if (current.status === "attached") {
        current.status = "detaching";
        current.updatedAt = now();
      }
      return clone(current);
    });

    if (attachment.status === "detached") return attachment;

    if (typeof this.runtimeProvider.detachStorage === "function") {
      await this.runtimeProvider.detachStorage({ attachment });
    }

    return this.store.update((state) => {
      const current = findOwnedResource(state.storageAttachments, accountId, attachmentId, "storage_attachment_not_found");
      const compute = findOwnedResource(state.computeAllocations, accountId, current.computeAllocationId, "compute_allocation_not_found");
      const storage = findOwnedResource(state.storageVolumes, accountId, current.storageId, "storage_volume_not_found");
      current.status = "detached";
      current.detachedAt = now();
      current.updatedAt = now();
      compute.attachedStorageIds = (compute.attachedStorageIds || []).filter((id) => id !== current.storageId);
      storage.attachmentIds = (storage.attachmentIds || []).filter((id) => id !== current.id);
      if (storage.status === "attached") storage.status = "available";
      for (const workspace of Object.values(state.workspaces || {})) {
        if (workspace.ownerAccountId !== accountId) continue;
        if (workspaceCurrentAttachmentId(workspace) !== attachmentId) continue;
        suspendWorkspaceRuntime(workspace);
      }
      state.billingLedger.push(addResourceIds(this.ledgerEntry({
        state,
        workspaceId: "resource",
        accountId,
        type: "storage_detached",
        amount: 0,
        sourceEventId: "detach_storage",
        metadata: {
          computeAllocationId: current.computeAllocationId,
          storageId: current.storageId,
          attachmentId
        }
      }), { computeAllocationId: current.computeAllocationId, storageId: current.storageId, attachmentId }));
      return clone(current);
    });
  }

  recordProvisioningUsage({
    state,
    account,
    accountId,
    resourceType,
    computeAllocationId = "",
    storageId = "",
    attachmentId = "",
    workspaceId = "",
    quantity,
    unit,
    unitPrice,
    amount,
    sourceEventId,
    metadata = {}
  }) {
    const usage = addResourceIds({
      id: makeId("usage-resource", accountId, workspaceId || computeAllocationId || storageId || attachmentId, resourceType, sourceEventId, String(state.resourceUsageLogs.length)),
      userId: account.id,
      accountId,
      workspaceId,
      resourceType,
      quantity: money(Number(quantity || 0)),
      unit,
      unitPrice: money(Number(unitPrice || 0)),
      amount: money(Number(amount || 0)),
      requestedAmount: money(Number(amount || 0)),
      currency: "CNY",
      sourceEventId,
      metadata: clone(metadata),
      createdAt: now()
    }, { computeAllocationId, storageId, attachmentId });
    state.resourceUsageLogs.push(usage);
    return usage;
  }

  async createProviderCompute({ compute, packagePlan }) {
    if (typeof this.runtimeProvider.createComputeAllocation === "function") {
      return this.runtimeProvider.createComputeAllocation({
        computeAllocationId: compute.id,
        accountId: compute.ownerAccountId,
        packagePlan,
        computeAllocation: compute
      });
    }
    return {
      providerResourceId: compute.id,
      status: "running",
      billingStatus: "active",
      spec: packagePlan.server
    };
  }

  async createProviderStorage({ storage, packagePlan }) {
    if (typeof this.runtimeProvider.createStorageVolume === "function") {
      return this.runtimeProvider.createStorageVolume({
        storageId: storage.id,
        accountId: storage.ownerAccountId,
        packagePlan,
        storage
      });
    }
    return {
      providerResourceId: storage.id,
      status: "available",
      billingStatus: "active"
    };
  }

  async attachProviderStorage({ attachment, compute, storage }) {
    if (typeof this.runtimeProvider.attachStorage === "function") {
      return this.runtimeProvider.attachStorage({ attachmentId: attachment.id, attachment, compute, storage });
    }
    return {
      providerAttachmentId: attachment.id,
      status: "attached"
    };
  }

  async markResourceFailed({ collection, accountId, resourceId, error, auditType }) {
    return this.store.update((state) => {
      ensureResourceCollections(state);
      const resource = (state[collection] || []).find((item) => item.id === resourceId && item.ownerAccountId === accountId);
      if (resource) {
        resource.status = "failed";
        resource.error = error.message;
        resource.safeMessage = error.safeMessage || error.message;
        resource.providerRequestId = error.providerRequestId || "";
        resource.retryable = Boolean(error.retryable);
        resource.providerData = clone(error.providerData || resource.providerData || {});
        resource.updatedAt = now();
      }
      const operation = latestRuntimeOperationForResource(state.runtimeOperations, resourceId);
      if (operation) {
        operation.status = "failed";
        operation.error = error.message;
        operation.safeMessage = error.safeMessage || error.message;
        operation.providerRequestId = error.providerRequestId || "";
        operation.retryable = Boolean(error.retryable);
        operation.providerData = clone(error.providerData || {});
        operation.updatedAt = now();
      }
      this.notify({
        state,
        accountId,
        workspaceId: "",
        type: auditType,
        severity: "error",
        message: error.safeMessage || error.message,
        sourceEventId: resourceId
      });
      return true;
    });
  }
}
