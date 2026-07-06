import {
  availableWorkspacePackages,
  defaultFabricResourceCatalog,
  selectWorkspacePackage
} from "../../fabric/src/index.js";
import { clone } from "./services/core-utils.js";
import {
  packageHoldAmount,
  pricedComputeHourly,
  pricedStorageGbMonth,
  storageHoldAmount
} from "./services/pricing-service.js";
import { BillingService } from "./services/billing-service.js";
import { ConsoleReadModelService } from "./services/console-read-model-service.js";
import { LedgerEvidenceService } from "./services/ledger-evidence-service.js";
import { ResourceProvisioningService } from "./services/resource-provisioning-service.js";
import { RuntimeOperationService } from "./services/runtime-operation-service.js";
import { WorkspaceLifecycleService } from "./services/workspace-lifecycle-service.js";

export { packageHoldAmount, storageHoldAmount };

export function createOplCloud({ store, runtimeProvider, pricing, productionReadiness = null, fabricCatalog = defaultFabricResourceCatalog() }) {
  return new OplCloudService({ store, runtimeProvider, pricing, productionReadiness, fabricCatalog });
}

export class OplCloudService {
  constructor({ store, runtimeProvider, pricing, productionReadiness = null, fabricCatalog = defaultFabricResourceCatalog() }) {
    this.store = store;
    this.runtimeProvider = runtimeProvider;
    this.pricing = pricing;
    this.productionReadinessCheck = productionReadiness;
    this.fabricCatalog = clone(fabricCatalog);
    this.runtimeOperationSequence = 0;
    this.runtimeOperations = new RuntimeOperationService(this);
    this.ledgerEvidence = new LedgerEvidenceService(this);
    this.billing = new BillingService(this);
    this.resourceProvisioning = new ResourceProvisioningService(this);
    this.workspaceLifecycle = new WorkspaceLifecycleService(this);
    this.consoleReadModel = new ConsoleReadModelService(this);
  }

  resourceCatalog() {
    return clone(this.fabricCatalog);
  }

  getPackage(packageId, { requireAvailable = true } = {}) {
    return selectWorkspacePackage(this.fabricCatalog, packageId, { requireAvailable });
  }

  packages() {
    return availableWorkspacePackages(this.fabricCatalog).map((plan) => ({
      ...clone(plan),
      price: {
        currency: "CNY",
        computeHourly: pricedComputeHourly({ packagePlan: plan, pricing: this.pricing }),
        storageGbMonth: pricedStorageGbMonth(this.pricing),
        priceBasis: this.pricing.priceBasis || "opl_user_price_catalog",
        source: "opl_user_price_catalog"
      }
    }));
  }

  async manualTopUp(...args) {
    return this.billing.manualTopUp(...args);
  }

  async createOrganization(...args) {
    return this.consoleReadModel.createOrganization(...args);
  }

  async createUser(...args) {
    return this.consoleReadModel.createUser(...args);
  }

  async disableUser(...args) {
    return this.consoleReadModel.disableUser(...args);
  }

  async deleteUser(...args) {
    return this.consoleReadModel.deleteUser(...args);
  }

  async addOrganizationMember(...args) {
    return this.consoleReadModel.addOrganizationMember(...args);
  }

  async managementState(...args) {
    return this.consoleReadModel.managementState(...args);
  }

  async supportTickets(...args) {
    return this.consoleReadModel.supportTickets(...args);
  }

  async createSupportTicket(...args) {
    return this.consoleReadModel.createSupportTicket(...args);
  }

  async createWorkspace(...args) {
    return this.workspaceLifecycle.createWorkspace(...args);
  }

  async computePools(...args) {
    return this.resourceProvisioning.computePools(...args);
  }

  async computeAllocations(...args) {
    return this.resourceProvisioning.computeAllocations(...args);
  }

  async computeAllocation(...args) {
    return this.resourceProvisioning.computeAllocation(...args);
  }

  async createComputeAllocation(...args) {
    return this.resourceProvisioning.createComputeAllocation(...args);
  }

  async processPendingResourceProvisioning(...args) {
    return this.resourceProvisioning.processPendingResourceProvisioning(...args);
  }

  async destroyComputeAllocation(...args) {
    return this.resourceProvisioning.destroyComputeAllocation(...args);
  }

  async createStorageVolume(...args) {
    return this.resourceProvisioning.createStorageVolume(...args);
  }

  async destroyStorageVolume(...args) {
    return this.resourceProvisioning.destroyStorageVolume(...args);
  }

  async attachStorage(...args) {
    return this.resourceProvisioning.attachStorage(...args);
  }

  async detachStorage(...args) {
    return this.resourceProvisioning.detachStorage(...args);
  }

  async resetWorkspaceToken(...args) {
    return this.workspaceLifecycle.resetWorkspaceToken(...args);
  }

  async deleteWorkspaceToken(...args) {
    return this.workspaceLifecycle.deleteWorkspaceToken(...args);
  }

  async cleanupWorkspaceAccess(...args) {
    return this.workspaceLifecycle.cleanupWorkspaceAccess(...args);
  }

  async settleBilling(...args) {
    return this.billing.settleBilling(...args);
  }

  async settleResourceBilling(...args) {
    return this.billing.settleResourceBilling(...args);
  }

  async billingLedger(...args) {
    return this.billing.billingLedger(...args);
  }

  async recordTaskEvidenceReceipt(...args) {
    return this.ledgerEvidence.recordTaskEvidenceReceipt(...args);
  }

  async taskEvidenceReceipts(...args) {
    return this.ledgerEvidence.taskEvidenceReceipts(...args);
  }

  async recordBillingReconciliation(...args) {
    return this.billing.recordBillingReconciliation(...args);
  }

  async aggregateResourceUsage(...args) {
    return this.billing.aggregateResourceUsage(...args);
  }

  async archiveResourceUsageLogs(...args) {
    return this.billing.archiveResourceUsageLogs(...args);
  }

  async resolveWorkspaceAccess(...args) {
    return this.workspaceLifecycle.resolveWorkspaceAccess(...args);
  }

  async getState(...args) {
    return this.consoleReadModel.getState(...args);
  }

  async operatorSummary(...args) {
    return this.consoleReadModel.operatorSummary(...args);
  }

  async runtimeReadiness(...args) {
    return this.consoleReadModel.runtimeReadiness(...args);
  }

  async runtimeStatus(...args) {
    return this.runtimeOperations.runtimeStatus(...args);
  }

  async productionReadiness(...args) {
    return this.consoleReadModel.productionReadiness(...args);
  }

  existingSettlementEntries(...args) {
    return this.billing.existingSettlementEntries(...args);
  }

  recordResourceUsage(...args) {
    return this.billing.recordResourceUsage(...args);
  }

  appendDebitEntries(...args) {
    return this.billing.appendDebitEntries(...args);
  }

  debitWorkspaceUsage(...args) {
    return this.billing.debitWorkspaceUsage(...args);
  }

  ensureHold(...args) {
    return this.billing.ensureHold(...args);
  }

  releaseHoldToLedger(...args) {
    return this.billing.releaseHoldToLedger(...args);
  }

  async recordCreateWorkspaceFailure(...args) {
    return this.workspaceLifecycle.recordCreateWorkspaceFailure(...args);
  }

  notify(...args) {
    return this.ledgerEvidence.notify(...args);
  }

  async runRuntimeOperation(...args) {
    return this.runtimeOperations.runRuntimeOperation(...args);
  }

  startRuntimeOperation(...args) {
    return this.runtimeOperations.startRuntimeOperation(...args);
  }

  finishRuntimeOperation(...args) {
    return this.runtimeOperations.finishRuntimeOperation(...args);
  }

  async recordFailedRuntimeOperation(...args) {
    return this.runtimeOperations.recordFailedRuntimeOperation(...args);
  }

  ledgerEntry(...args) {
    return this.ledgerEvidence.ledgerEntry(...args);
  }

  recordEvidence(...args) {
    return this.ledgerEvidence.recordEvidence(...args);
  }

  auditEvent(...args) {
    return this.ledgerEvidence.auditEvent(...args);
  }

  assertBillingReconciliationAllowsProvisioning(...args) {
    return this.billing.assertBillingReconciliationAllowsProvisioning(...args);
  }
}
