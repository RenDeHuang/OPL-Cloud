import { clone, makeId, money, now } from "./core-utils.js";
import {
  accountAvailable,
  addHold,
  ensureUserWallet,
  releaseHold
} from "./wallet-service.js";
import {
  computeHourlyBase,
  packageHoldAmount,
  pricedComputeHourly,
  pricedStorageGbMonth,
  pricingMarkup,
  storageGbMonthBase
} from "./pricing-service.js";
import { OplDomainService } from "./opl-domain-service.js";

function ensureResourceCollections(state) {
  state.computeResources ??= [];
  state.storageVolumes ??= [];
  state.storageAttachments ??= [];
  state.resourceUsageLogs ??= [];
}

function findOwnedResource(items, accountId, id, errorName) {
  const resource = (items || []).find((item) => item.id === id && item.ownerAccountId === accountId);
  if (!resource) throw new Error(errorName);
  return resource;
}

function addResourceIds(target, { computeId = "", storageId = "", attachmentId = "" } = {}) {
  if (computeId) target.computeId = computeId;
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

export class ResourceProvisioningService extends OplDomainService {
  async createComputeResource({ accountId, userId = "", packageId, name = "" }) {
    const packagePlan = this.getPackage(packageId);
    const hold = packageHoldAmount({ packagePlan, pricing: this.pricing });
    let computeId = "";

    const reservation = await this.store.update((state) => {
      ensureResourceCollections(state);
      this.assertBillingReconciliationAllowsProvisioning(state);
      const account = ensureUserWallet(state, { accountId });
      computeId = makeId("compute", accountId, packageId, name || packagePlan.name, String(state.computeResources.length));
      const existing = state.computeResources.find((item) => item.id === computeId);
      if (existing) return { existing: true, compute: clone(existing) };
      if (accountAvailable(account) < hold.compute) throw new Error("insufficient_compute_hold_balance");

      addHold(account, "compute", hold.compute);
      const sourceEventId = `compute_resource:${computeId}:created`;
      const ledger = addResourceIds(this.ledgerEntry({
        state,
        workspaceId: "resource",
        accountId,
        type: "compute_hold",
        amount: hold.compute,
        sourceEventId,
        holdType: "compute",
        metadata: {
          computeId,
          packageId,
          holdDays: 7,
          baseHourly: computeHourlyBase({ packagePlan, pricing: this.pricing }),
          markup: pricingMarkup(this.pricing)
        }
      }), { computeId });
      state.billingLedger.push(ledger);

      const compute = {
        id: computeId,
        ownerAccountId: accountId,
        ownerUserId: account.id,
        name: name || packagePlan.name,
        packageId,
        provider: this.runtimeProvider.name || "unknown",
        providerResourceId: "",
        status: "provisioning",
        billingStatus: "active",
        spec: packagePlan.server,
        image: "",
        createdAt: now(),
        updatedAt: now()
      };
      state.computeResources.push(compute);
      this.recordProvisioningUsage({
        state,
        account,
        accountId,
        resourceType: "compute",
        computeId,
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
      state.audit.push(this.auditEvent({ accountId, type: "compute.created", sourceEventId: computeId }));
      return { existing: false, compute: clone(compute) };
    });

    if (reservation.existing) return reservation.compute;

    try {
      const providerCompute = await this.createProviderCompute({ compute: reservation.compute, packagePlan });
      return this.store.update((state) => {
        const compute = findOwnedResource(state.computeResources, accountId, computeId, "compute_resource_not_found");
        Object.assign(compute, {
          providerResourceId: providerCompute.providerResourceId || providerCompute.id || compute.providerResourceId,
          status: providerCompute.status || "running",
          billingStatus: providerCompute.billingStatus || compute.billingStatus,
          spec: providerCompute.spec || compute.spec,
          image: providerCompute.image || compute.image,
          localPath: providerCompute.localPath || compute.localPath,
          composePath: providerCompute.composePath || compute.composePath,
          runtime: providerCompute.runtime ? clone(providerCompute.runtime) : compute.runtime,
          providerData: clone(providerCompute),
          updatedAt: now()
        });
        return clone(compute);
      });
    } catch (error) {
      await this.markResourceFailed({ collection: "computeResources", accountId, resourceId: computeId, error, auditType: "compute.create_failed" });
      throw error;
    }
  }

  async destroyComputeResource({ accountId, computeId, confirm }) {
    if (confirm !== true) throw new Error("compute_destroy_confirmation_required");
    const compute = await this.store.update((state) => {
      ensureResourceCollections(state);
      const current = findOwnedResource(state.computeResources, accountId, computeId, "compute_resource_not_found");
      const activeAttachment = state.storageAttachments.find((item) =>
        item.ownerAccountId === accountId &&
        item.computeId === computeId &&
        item.status === "attached"
      );
      if (activeAttachment) throw new Error("compute_has_attached_storage");
      current.status = "destroying";
      current.updatedAt = now();
      return clone(current);
    });

    if (typeof this.runtimeProvider.destroyComputeResource === "function") {
      await this.runtimeProvider.destroyComputeResource({ compute });
    }

    return this.store.update((state) => {
      const current = findOwnedResource(state.computeResources, accountId, computeId, "compute_resource_not_found");
      const account = ensureUserWallet(state, { accountId, userId: current.ownerUserId });
      const released = releaseHold(account, "compute");
      const sourceEventId = `compute_resource:${computeId}:destroyed`;
      if (released > 0) {
        state.billingLedger.push(addResourceIds(this.ledgerEntry({
          state,
          workspaceId: "resource",
          accountId,
          type: "compute_hold_released",
          amount: -released,
          sourceEventId,
          holdType: "compute",
          metadata: { computeId }
        }), { computeId }));
      }
      current.status = "destroyed";
      current.billingStatus = "closed";
      current.destroyedAt = now();
      current.updatedAt = now();
      state.billingLedger.push(addResourceIds(this.ledgerEntry({
        state,
        workspaceId: "resource",
        accountId,
        type: "compute_destroyed",
        amount: 0,
        sourceEventId,
        metadata: { computeId }
      }), { computeId }));
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

      addHold(account, "storage", hold.storage);
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
          packageId,
          sizeGb: normalizedSizeGb,
          holdDays: 7,
          baseGbMonth: storageGbMonthBase(this.pricing),
          markup: pricingMarkup(this.pricing)
        }
      }), { storageId });
      state.billingLedger.push(ledger);

      const storage = {
        id: storageId,
        ownerAccountId: accountId,
        ownerUserId: account.id,
        name: name || `${normalizedSizeGb}GB workspace storage`,
        packageId,
        provider: this.runtimeProvider.name || "unknown",
        providerResourceId: "",
        status: "provisioning",
        billingStatus: "active",
        sizeGb: normalizedSizeGb,
        storageClassId: packagePlan.storageClassId,
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
          status: providerStorage.status || "available",
          billingStatus: providerStorage.billingStatus || storage.billingStatus,
          localPath: providerStorage.localPath || storage.localPath,
          storageClass: providerStorage.storageClass || storage.storageClass,
          providerData: clone(providerStorage),
          updatedAt: now()
        });
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
      const account = ensureUserWallet(state, { accountId, userId: current.ownerUserId });
      const released = releaseHold(account, "storage");
      const sourceEventId = `storage_volume:${storageId}:destroyed`;
      if (released > 0) {
        state.billingLedger.push(addResourceIds(this.ledgerEntry({
          state,
          workspaceId: "resource",
          accountId,
          type: "storage_hold_released",
          amount: -released,
          sourceEventId,
          holdType: "storage",
          metadata: { storageId }
        }), { storageId }));
      }
      current.status = "destroyed";
      current.billingStatus = "closed";
      current.destroyedAt = now();
      current.updatedAt = now();
      state.billingLedger.push(addResourceIds(this.ledgerEntry({
        state,
        workspaceId: "resource",
        accountId,
        type: "storage_destroyed",
        amount: 0,
        sourceEventId,
        metadata: { storageId }
      }), { storageId }));
      return clone(current);
    });
  }

  async attachStorage({ accountId, computeId, storageId, mountPath = "/data" }) {
    let attachmentId = "";
    const reservation = await this.store.update((state) => {
      ensureResourceCollections(state);
      const compute = findOwnedResource(state.computeResources, accountId, computeId, "compute_resource_not_found");
      const storage = findOwnedResource(state.storageVolumes, accountId, storageId, "storage_volume_not_found");
      if (compute.status === "destroyed") throw new Error("compute_resource_destroyed");
      if (storage.status === "destroyed") throw new Error("storage_volume_destroyed");
      const active = state.storageAttachments.find((item) =>
        item.ownerAccountId === accountId &&
        item.storageId === storageId &&
        item.status === "attached"
      );
      if (active) throw new Error("storage_already_attached");

      attachmentId = makeId("attach", accountId, computeId, storageId, mountPath, String(state.storageAttachments.length));
      const attachment = {
        id: attachmentId,
        ownerAccountId: accountId,
        computeId,
        storageId,
        mountPath,
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
        metadata: { computeId, storageId, attachmentId, mountPath }
      }), { computeId, storageId, attachmentId }));
      this.recordProvisioningUsage({
        state,
        account: ensureUserWallet(state, { accountId, userId: compute.ownerUserId }),
        accountId,
        resourceType: "attachment",
        computeId,
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
        const compute = findOwnedResource(state.computeResources, accountId, computeId, "compute_resource_not_found");
        const storage = findOwnedResource(state.storageVolumes, accountId, storageId, "storage_volume_not_found");
        Object.assign(attachment, {
          providerAttachmentId: providerAttachment.providerAttachmentId || providerAttachment.id || attachment.providerAttachmentId,
          status: providerAttachment.status || "attached",
          localPath: providerAttachment.localPath || attachment.localPath,
          composePath: providerAttachment.composePath || attachment.composePath,
          providerData: clone(providerAttachment),
          updatedAt: now()
        });
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
      const compute = findOwnedResource(state.computeResources, accountId, current.computeId, "compute_resource_not_found");
      const storage = findOwnedResource(state.storageVolumes, accountId, current.storageId, "storage_volume_not_found");
      current.status = "detached";
      current.detachedAt = now();
      current.updatedAt = now();
      compute.attachedStorageIds = (compute.attachedStorageIds || []).filter((id) => id !== current.storageId);
      storage.attachmentIds = (storage.attachmentIds || []).filter((id) => id !== current.id);
      if (storage.status === "attached") storage.status = "available";
      state.billingLedger.push(addResourceIds(this.ledgerEntry({
        state,
        workspaceId: "resource",
        accountId,
        type: "storage_detached",
        amount: 0,
        sourceEventId: "detach_storage",
        metadata: {
          computeId: current.computeId,
          storageId: current.storageId,
          attachmentId
        }
      }), { computeId: current.computeId, storageId: current.storageId, attachmentId }));
      return clone(current);
    });
  }

  recordProvisioningUsage({
    state,
    account,
    accountId,
    resourceType,
    computeId = "",
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
      id: makeId("usage-resource", accountId, workspaceId || computeId || storageId || attachmentId, resourceType, sourceEventId, String(state.resourceUsageLogs.length)),
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
    }, { computeId, storageId, attachmentId });
    state.resourceUsageLogs.push(usage);
    return usage;
  }

  async createProviderCompute({ compute, packagePlan }) {
    if (typeof this.runtimeProvider.createComputeResource === "function") {
      return this.runtimeProvider.createComputeResource({
        computeId: compute.id,
        accountId: compute.ownerAccountId,
        packagePlan,
        compute
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
        resource.updatedAt = now();
      }
      this.notify({
        state,
        accountId,
        workspaceId: "",
        type: auditType,
        severity: "error",
        message: error.message,
        sourceEventId: resourceId
      });
      return true;
    });
  }
}
