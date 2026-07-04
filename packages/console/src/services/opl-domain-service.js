
export class OplDomainService {
  constructor(root) {
    this.root = root;
  }

  get store() {
    return this.root.store;
  }

  get runtimeProvider() {
    return this.root.runtimeProvider;
  }

  get pricing() {
    return this.root.pricing;
  }

  get productionReadinessCheck() {
    return this.root.productionReadinessCheck;
  }

  get fabricCatalog() {
    return this.root.fabricCatalog;
  }

  get runtimeOperationSequence() {
    return this.root.runtimeOperationSequence;
  }

  set runtimeOperationSequence(value) {
    this.root.runtimeOperationSequence = value;
  }

  getPackage(...args) {
    return this.root.getPackage(...args);
  }

  packages(...args) {
    return this.root.packages(...args);
  }

  ledgerEntry(...args) {
    return this.root.ledgerEntry(...args);
  }

  recordEvidence(...args) {
    return this.root.recordEvidence(...args);
  }

  auditEvent(...args) {
    return this.root.auditEvent(...args);
  }

  notify(...args) {
    return this.root.notify(...args);
  }

  runRuntimeOperation(...args) {
    return this.root.runRuntimeOperation(...args);
  }

  startRuntimeOperation(...args) {
    return this.root.startRuntimeOperation(...args);
  }

  finishRuntimeOperation(...args) {
    return this.root.finishRuntimeOperation(...args);
  }

  recordFailedRuntimeOperation(...args) {
    return this.root.recordFailedRuntimeOperation(...args);
  }

  debitWorkspaceUsage(...args) {
    return this.root.debitWorkspaceUsage(...args);
  }

  ensureHold(...args) {
    return this.root.ensureHold(...args);
  }

  releaseHoldToLedger(...args) {
    return this.root.releaseHoldToLedger(...args);
  }

  assertBillingReconciliationAllowsProvisioning(...args) {
    return this.root.assertBillingReconciliationAllowsProvisioning(...args);
  }

  recordCreateWorkspaceFailure(...args) {
    return this.root.recordCreateWorkspaceFailure(...args);
  }
}
