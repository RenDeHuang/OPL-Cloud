import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);

async function text(path: string) {
  return readFile(new URL(path, root), "utf8");
}

async function json(path: string) {
  return JSON.parse(await text(path));
}

test("Current contracts hard cut Gateway keys and source envelopes", async () => {
  const [freeze, sourceTruth, boundary, dtos] = await Promise.all([
    json("packages/contracts/opl-cloud-launch-freeze-contract.json"),
    json("packages/contracts/opl-cloud-console-source-truth-contract.json"),
    json("packages/contracts/opl-cloud-service-boundary-contract.json"),
    text("apps/console-ui/src/api/dtos.ts")
  ]);

  assert.deepEqual(freeze.deliveryEvidence, {
    required: true,
    codeComplete: false,
    pilotReady: false,
    productionProven: false,
    saleable: false
  });
  assert.equal(freeze.gateway.publicEndpoint.route, "GET /api/gateway/endpoint");
  assert.deepEqual(freeze.gateway.customerMutationApis, ["create_general_key", "update_general_key", "delete_general_key", "reveal_owned_key", "change_group", "reset_quota", "reset_rate_limit_usage"]);
  assert.equal(freeze.gateway.createKeyRequest.groupRequired, true);
  assert.equal(freeze.gateway.createKeyRequest.expiryField, "expiresInDays");
  assert.equal(freeze.gateway.createKeyRequest.responseExpiryField, "expiresAt");
  assert.equal(freeze.gateway.createKeyRequest.createThenUpdate, false);

  assert.equal(sourceTruth.envelope.typeName, "SourceEnvelope<T>");
  assert.equal(sourceTruth.envelope.serverWriter, "writeSourceEnvelope");
  assert.equal(sourceTruth.envelope.fetchedAtMaySubstituteSourceUpdatedAt, false);
  assert.equal(sourceTruth.sources.gateway.endpoint.authority, "existing_sub2api_base_url_plus_v1");
  assert.equal(sourceTruth.sources.gateway.groups.authority, "live_sub2api_readback");
  assert.equal(sourceTruth.sources.gateway.keys.createRequest.expiryField, "expiresInDays");
  assert.equal(sourceTruth.sources.gateway.keys.revealRoute, "POST /api/gateway/keys/{keyId}/reveal");
  assert.equal(sourceTruth.sources.gateway.usage.route, "GET /api/gateway/keys/{keyId}/usage");
  assert.equal(sourceTruth.sources.gateway.usageStats.route, "GET /api/gateway/keys/{keyId}/usage-summary");
  assert.equal(sourceTruth.sources.gateway.accountUsageStats.route, "GET /api/gateway/usage-summary");

  assert.deepEqual(boundary.customerMutationBoundary, { payment: false, topUp: false, keyCreate: true, keyRevoke: true });
  assert.ok(boundary.externalServices.gateway.controlPlaneApi.includes("mutate owned general keys including group quota IP expiry rate limits and resets with delegated user credentials"));
  assert.doesNotMatch(dtos, /ProductSourceEnvelope/);
  assert.match(dtos, /type SourceEnvelope<T>/);
  for (const name of [
    "MoneyDTO", "OperationStatusDTO", "SessionDTO", "CurrentAccountDTO", "GatewayWalletDTO", "GatewayBalanceHistoryPageDTO",
    "GatewayEndpointDTO", "GatewayGroupPageDTO", "GatewayKeyPageDTO", "GatewayKeySummaryDTO", "CreateGatewayKeyRequest",
    "UpdateGatewayKeyRequest", "GatewayKeySecretDTO", "GatewayKeyUsagePageDTO",
    "GatewayUsageSummaryDTO", "GatewayAccountUsageSummaryDTO", "WorkspaceDTO",
    "WorkspaceLaunchRequest", "WorkspaceLaunchOperationDTO", "WorkspaceKeyRotationDTO",
    "WorkspaceRuntimeDTO", "WorkspaceFileEntryDTO", "WorkspaceFilePageDTO",
    "WorkspaceFilesystemUsageDTO", "BillingReceiptPageDTO", "WorkspaceBillingReceiptDTO",
    "AnnouncementPageDTO", "AnnouncementDTO", "AnnouncementReadDTO", "OperatorOverviewDTO", "OperatorUsageCostDTO",
    "OperatorAccountPageDTO", "OperatorAccountDTO",
    "OperatorAccountCommandDTO", "WalletAdjustmentRequest", "WalletAdjustmentRecoveryRequest", "WalletAdjustmentOperationDTO",
    "OperatorWorkspacePageDTO", "OperatorWorkspaceDTO", "WorkspaceRuntimeCredentialDTO",
    "WorkspaceAutoRenewRequest", "WorkspaceAutoRenewCommandDTO", "OperatorReconciliationPageDTO",
    "BillingReviewResolutionRequest", "OperatorHealthDTO", "OperatorAnnouncementPageDTO",
    "AnnouncementDraftRequest", "AnnouncementScheduleRequest"
  ]) {
    assert.match(dtos, new RegExp(`export (?:interface|type) ${name}\\b`), `missing ${name}`);
  }
  assert.match(dtos, /interface CreateGatewayKeyRequest[\s\S]*expiresInDays/);
  const rotationDTO = dtos.match(/export interface WorkspaceKeyRotationDTO[\s\S]*?\n}/)?.[0] ?? "";
  assert.match(rotationDTO, /workspaceApiKeyId:\s*string;/);
  assert.doesNotMatch(rotationDTO, /\n\s+keyId:\s*string;/);
});

test("Current contracts hard cut Workspace purchase, access, and Runtime facts", async () => {
  const [freeze, billing, pricing, business, product, evidence, sourceTruth] = await Promise.all([
    json("packages/contracts/opl-cloud-launch-freeze-contract.json"),
    json("packages/contracts/opl-cloud-billing-ledger-contract.json"),
    json("packages/contracts/opl-cloud-pricing-contract.json"),
    json("packages/contracts/opl-cloud-business-object-contract.json"),
    json("packages/contracts/opl-cloud-product-contract.json"),
    json("packages/contracts/opl-cloud-evidence-ledger-contract.json"),
    json("packages/contracts/opl-cloud-console-source-truth-contract.json")
  ]);

  assert.equal(freeze.workspaceLaunch.customerDebitCardinality, 1);
  assert.equal(freeze.workspaceLaunch.persistence, "control_plane_runtime_operations with action=workspace.launch.v2 and result.schemaVersion=2");
  assert.equal(freeze.workspaceLaunch.codeCompleteThroughPhase, undefined);
  assert.equal(freeze.workspaceLaunch.legacyNonTerminalPolicy, "manual_review_compute_fulfilling_is_read_only_candidate_normalized_only_after_debit_identity_storage_zero_and_compute_proof_via_postgresql_cas");
  assert.equal(freeze.workspaceLaunch.backgroundProgression, "non_review_and_manual_review_recovery_integrated_local_fake_verified");
  assert.equal(freeze.workspaceLaunch.nextBlockedStage, undefined);
  assert.deepEqual(freeze.workspaceLaunch.fulfillmentResources, ["compute", "storage", "attachment", "gateway_secret", "runtime"]);
  assert.deepEqual(freeze.gateway.workspaceKeyLifecycle, {
    scopeIdentity: ["workspaceId", "workspaceApiKeyId"],
    launchConvergence: "one_reserved_key_per_workspace",
    rotationApi: "POST /api/workspaces/{workspaceId}/workspace-key/rotate",
    mutationCredential: "session_delegated_user_bearer",
    workspacePersistence: "workspace_api_key_id_only",
    operationPersistence: "control_plane_runtime_operations_non_secret_phases",
    phases: ["replacement_check", "replacement_create", "secret_write", "runtime_bind", "runtime_readback", "workspace_commit", "retire_old", "promote_new", "delete_old", "receipt", "complete"],
    runtimeCredentialInvariant: "key_rotation_does_not_change_username_password_or_credential_version",
    oldKeyRetirementGate: "only_after_runtime_authoritative_readback_and_atomic_workspace_commit",
    receiptType: "workspace.gateway_key_rotated.v1",
    currentImplementation: "code_complete_local_focused_tests_only"
  });
  assert.equal(billing.chargePolicy.customerObject, "workspace");
  assert.equal(billing.chargePolicy.debitCardinalityPerPeriod, 1);
  assert.equal(billing.chargePolicy.launchOperationAction, "workspace.launch.v2");
  assert.equal(billing.chargePolicy.launchOperationSchemaVersion, 2);
  assert.deepEqual(billing.chargePolicy.exactBalanceEvidence, {
    precondition: "preBalanceUsdMicros > totalChargeUsdMicros",
    postcondition: "postBalanceUsdMicros == preBalanceUsdMicros - totalChargeUsdMicros",
    mismatchStatus: "manual_review",
    fabricWriteCountOnMismatch: 0
  });
  assert.deepEqual(billing.chargePolicy.providerPreflight, {
    timing: "before_first_charge_attempt",
    runWhen: "ChargeAttempted=false_and_ChargeConfirmation_absent",
    skipOnRecoveryWhenAnyPresent: ["ChargeAttempted", "ChargeConfirmation"],
    writes: "none"
  });
  assert.deepEqual(billing.workspaceLaunchFulfillment.customerChargeOperations, ["workspace_debit", "workspace_refund"]);
  assert.equal(billing.workspaceLaunchFulfillment.resourcePurchasePath, "fabric_create_and_sync_without_customer_debit");
  assert.equal(billing.workspaceLaunchFulfillment.successReceiptType, "billing.workspace_purchased.v1");
  assert.deepEqual(billing.workspaceLaunchFulfillment.activationReadback, {
    apis: ["SyncMonthlyCompute", "SyncMonthlyStorage"],
    timing: "immediately_before_workspace_activation",
    sharedRequiredFacts: ["resource_identity", "account_identity", "workspace_identity", "zone", "chargeType=PREPAID", "renewFlag=NOTIFY_AND_MANUAL_RENEW", "deadline"],
    computeRequiredFacts: ["sku"],
    storageRequiredFacts: ["capacity"],
    mismatch: "manual_review_without_activation"
  });
  assert.equal(billing.workspaceLaunchFulfillment.manualReviewRecoveryContract, "opl-cloud-launch-freeze-contract.json#workspaceLaunch.manualReviewRecovery");
  assert.equal(freeze.workspaceLaunch.manualReviewRecovery.providerTruthContract, "opl-cloud-service-boundary-contract.json#services.fabric.workspaceLaunchManualReviewProviderTruth");
  assert.equal(billing.workspaceLaunchFulfillment.implementation, "non_review_path_local_tests_manual_review_recovery_pending_integration_verification");
  assert.equal(billing.workspaceLaunchFulfillment.realEnvironmentEvidence, "pending_owner_approved_acceptance");
  assert.equal(pricing.workspaceCharge.launchOperationAction, "workspace.launch.v2");
  assert.equal(pricing.workspaceCharge.codeCompleteThroughPhase, undefined);
  assert.equal(pricing.workspaceCharge.implementation, "non_review_price_and_charge_contract_local_tests_manual_review_recovery_pending_integration");
  assert.equal(pricing.workspaceCharge.nextBlockedStage, undefined);
  assert.equal(pricing.workspaceCharge.catalogAvailabilityMeaning, "product_open_not_tencent_capacity");
  assert.equal(pricing.workspaceCharge.capacityAuthority, "Tencent MonthlyPreflight immediately before first debit");
  assert.ok(pricing.rules.includes("Pricing preview and Workspace launch require available=true from the live Fabric catalog; unavailable packages return package_unavailable before Gateway, balance, debit, Ledger, or Tencent calls."));
  assert.equal(billing.ledgerEvidencePolicy.workspaceReceiptTypes.purchased, "billing.workspace_purchased.v1");
  assert.deepEqual(billing.ledgerEvidencePolicy.workspaceFulfillmentReceiptTypes, ["billing.workspace_purchased.v1", "billing.workspace_renewed.v1"]);
  assert.deepEqual(billing.ledgerEvidencePolicy.workspacePurchasedAdditionalCostFields, ["sub2apiUserId", "sub2apiRedeemCode", "postChargeBalanceUsdMicros"]);
  assert.equal(billing.ledgerEvidencePolicy.realEnvironmentEvidence, "pending_owner_approved_acceptance");
  assert.equal(billing.entitlementPolicy.resourceCompatibility.customerChargeOwner, false);
  assert.ok(evidence.receiptTypes.includes("billing.workspace_purchased.v1"));
  assert.ok(evidence.receiptTypes.includes("workspace.gateway_key_rotated.v1"));
  assert.ok(evidence.receiptTypes.includes("gateway.wallet_adjustment.v1"));
  assert.deepEqual(evidence.workspaceMonthlyBillingReceiptV1.purchasedAdditionalCostFields, ["sub2apiUserId", "sub2apiRedeemCode", "postChargeBalanceUsdMicros"]);
  assert.deepEqual(evidence.workspaceMonthlyBillingReceiptV1.purchasedRequiredExecutionFields, ["computeAllocationId", "storageId"]);
  assert.equal(evidence.workspaceMonthlyBillingReceiptV1.implementation, "validator_writer_and_customer_projection_code_complete_local_tests_only");
  assert.equal(evidence.workspaceMonthlyBillingReceiptV1.realEnvironmentEvidence, "pending_owner_approved_acceptance");

  const workspace = business.objectKinds.find((entry: { kind: string }) => entry.kind === "Workspace");
  const compute = business.objectKinds.find((entry: { kind: string }) => entry.kind === "ComputeAllocation");
  const storage = business.objectKinds.find((entry: { kind: string }) => entry.kind === "StorageVolume");
  assert.deepEqual(compute.requiredBillingFields, []);
  assert.deepEqual(storage.requiredBillingFields, []);
  assert.equal(compute.customerChargeOwner, false);
  assert.equal(storage.customerChargeOwner, false);
  assert.ok(workspace.requiredFields.includes("workspaceApiKeyId"));
  assert.equal(workspace.customerChargeOwner, true);
  assert.equal(workspace.purchaseReceiptType, "billing.workspace_purchased.v1");
  assert.deepEqual(workspace.accessQuestions, ["url", "username", "passwordRevealCopy", "workspaceKeyRevealCopy"]);
  assert.equal(workspace.workspaceKeyRevealRoute, "POST /api/gateway/keys/{keyId}/reveal");
	assert.equal(workspace.workspaceKeyRotationRoute, "POST /api/workspaces/{workspaceId}/workspace-key/rotate");
	assert.equal(workspace.workspaceKeyPersistence, "workspace_api_key_id_only");
	assert.deepEqual(workspace.workspaceKeyRotationDTOFields, ["operationId", "workspaceId", "status", "workspaceApiKeyId", "fingerprint", "updatedAt", "receiptId"]);
	assert.equal(evidence.workspaceGatewayKeyRotationReceipt.implementation, "validator_and_control_plane_exact_readback_code_complete_local_only");
  assert.deepEqual(sourceTruth.sources.ledger.billingReceipts.moneyFieldsByType.workspacePurchased, ["totalUsdMicros"]);
  assert.deepEqual(sourceTruth.sources.ledger.billingReceipts.workspaceFulfillmentFields, ["computeAllocationId", "storageId", "attachmentId", "workspaceApiKeyId", "runtimeId"]);
  assert.equal(sourceTruth.sources.ledger.billingReceipts.rawProviderReadback, false);
  assert.deepEqual(product.workspaceRuntimeFacts, {
    launchStatus: "paused_not_in_release",
    fileMetadataAuthority: "workspace_runtime_projects_mount",
    filesystemUsageAuthority: "workspace_runtime_statfs",
    apiRoutes: [],
    consolePresentation: "absent",
    persistence: "none",
    releaseValidation: "direct_runtime_pod_sha256_markers_only"
  });
});

test("Current Fabric contracts require dedicated package NodePools without weakening Workspace renewal", async () => {
  const [boundary, catalog, deployment, freeze, provisioner] = await Promise.all([
    json("packages/contracts/opl-cloud-service-boundary-contract.json"),
    json("packages/contracts/opl-cloud-fabric-resource-catalog-contract.json"),
    json("packages/contracts/opl-cloud-deployment-contract.json"),
    json("packages/contracts/opl-cloud-launch-freeze-contract.json"),
    text("services/fabric/cmd/opl-tencent-provisioner/main.go")
  ]);

  assert.deepEqual(boundary.services.fabric.computeProcurement, {
    api: "CreateComputeAllocation",
    scope: "allocation",
    packageNodePools: { basic: "explicit_configured_pool", pro: "explicit_configured_pool" },
    packageResourceContracts: {
      basic: { cpu: 2, memoryGb: 4 },
      pro: { cpu: 8, memoryGb: 16 }
    },
    resolvedInstanceTypeSource: "release_owner_approved_bootstrap_and_production_configuration",
    admission: "persisted_fifo_by_exact_node_pool",
    admissionLock: "short_postgresql_transaction_advisory_lock_only",
    headExecution: "only_started_head_can_prepare_scale_bounded_poll_and_claim",
    executionFence: "short_lease_without_provider_call_holding_postgresql_connection",
    crossPoolConcurrency: "independent_node_pools_parallel",
    scale: "persist_baseline_and_absolute_target_N_plus_1_then_scale_once",
    replay: "same_absolute_target",
    claim: "unique_ready_after_minus_before_machine_only",
    machineIdentityReadback: {
      readyStates: ["Ready", "Running"],
      missingOrUnknownState: "fail_closed_never_default_running",
      privateIpCvmMatches: "exactly_one",
      exactIdentityChain: ["nodePoolId", "machineName", "cvmInstanceId", "privateIp", "vpcId", "subnetId"],
      instanceTypeConsistency: ["node_pool", "machine", "native", "cvm"],
      resourceShapeRequired: ["machine_cpu_memory", "native_cpu_memory", "cvm_cpu_memory"],
      zeroMultipleMissingOrMismatch: "fail_closed"
    },
    existingMachineAllocation: false,
    nodePoolDiscoveryFallback: false,
    customerLaunchCreateNodePool: false
  });
  assert.deepEqual(boundary.services.fabric.workspaceRenewal, {
    primitives: ["RenewComputeAllocation", "RenewStorageVolume"],
    resources: "same_existing_cvm_and_cbs",
    chargeType: "PREPAID",
    periodMonths: 1,
    renewFlag: "NOTIFY_AND_MANUAL_RENEW",
    idempotentReadback: true,
    customerBillingOwner: "control_plane_workspace_single_operation",
    currentBranchScope: "preserved_not_rewritten"
  });
  assert.equal(catalog.workspacePackageNodePools.basic.poolName, "pool-basic-2c4g");
  assert.deepEqual(catalog.workspacePackageNodePools.basic.resourceContract, { cpu: 2, memoryGb: 4 });
  assert.equal(catalog.workspacePackageNodePools.basic.resolvedInstanceTypeSource, "deterministic_zone_prepaid_sell_shape_price_inventory_then_bootstrap_registration");
  assert.equal(Object.hasOwn(catalog.workspacePackageNodePools.basic, "instanceType"), false);
  assert.doesNotMatch(JSON.stringify(catalog.workspacePackageNodePools.basic), /SA5\.MEDIUM4/);
  assert.equal(catalog.workspacePackageNodePools.pro.poolName, "pool-pro-8c16g");
  assert.deepEqual(catalog.workspacePackageNodePools.pro.resourceContract, { cpu: 8, memoryGb: 16 });
  assert.equal(catalog.workspacePackageNodePools.pro.resolvedInstanceTypeSource, "deterministic_zone_prepaid_sell_shape_price_inventory_then_bootstrap_registration");
  assert.equal(Object.hasOwn(catalog.workspacePackageNodePools.pro, "instanceType"), false);
  assert.doesNotMatch(JSON.stringify(catalog.workspacePackageNodePools.pro), /SA5\.2XLARGE16/);
  assert.equal(catalog.workspacePackageNodePools.basic.replicasMayBeZero, true);
  assert.equal(catalog.workspacePackageNodePools.pro.replicasMayBeZero, true);
  assert.equal(catalog.workspacePackageNodePools.basic.maxReplicas, 50);
  assert.equal(catalog.workspacePackageNodePools.pro.maxReplicas, 50);
  assert.equal(catalog.workspacePackageNodePools.maxReplicasPolicy, "required_explicit_configuration_no_default");
  assert.deepEqual(catalog.workspacePackageNodePools.bootstrapInventoryPolicy, {
    evidence: "sorted_complete_node_pool_ids_before_mutation",
    unknownNodePool: "fail_closed_before_create"
  });
  assert.deepEqual(deployment.workspaceNodePoolBootstrap, {
    file: ".github/workflows/bootstrap-tke-workspace-nodepools.yml",
    mode: "manual_deterministic_inventory_then_optional_create_missing_zero_replica_pools",
    environment: "production",
    credentials: "existing_production_tencent_credentials_and_kubeconfig",
    packagePools: ["basic", "pro"],
    skuInventoryAction: "workspace_sku_inventory",
    skuRecommendation: "lowest_monthly_price_then_instance_type_for_exact_package_shape",
    skuMutationGate: "selected_candidate_revalidated_immediately_before_create",
    requiredCapacity: 1,
    capacityGates: ["prepaid_quota", "subnet_available_ip", "tke_cluster_node_limit"],
    inventoryEvidence: "sorted_complete_node_pool_ids_before_mutation",
    unknownNodePool: "fail_closed_before_create",
    mutationSource: "exact_merged_origin_main_sha",
    resolvedInstanceTypeRegistration: ["bootstrap_report", "node_pool_label", "tke_native_instance_types", "production_configuration"],
    maxReplicas: { basic: 50, pro: 50, source: "release_owner_approved_workflow_configuration_no_code_default_independent_pool_limits" },
    mutationConfirmation: "CREATE_MISSING_WORKSPACE_NODEPOOLS",
    dryRunMutationCount: 0,
    idempotency: "register_running_wait_exact_creating_create_missing_only",
    partialFailure: "preserve_created_pool_retry_missing_only",
    createNodePoolOutsideBootstrap: false
  });
  assert.deepEqual(deployment.protectedSystemResources, {
    nodePoolIdEnv: "OPL_SYSTEM_COMPUTE_NODE_POOL_ID",
    machineIdEnv: "OPL_SYSTEM_COMPUTE_MACHINE_ID",
    nodeNameEnv: "OPL_SYSTEM_COMPUTE_NODE_NAME",
    machineTypeEnv: "OPL_SYSTEM_COMPUTE_MACHINE_TYPE",
    cvmIdEnv: "OPL_SYSTEM_COMPUTE_CVM_ID",
    cvmApplicability: {
      NativeCVM: "required_unique_ins_id_resolved_by_machine_lan_ip",
      Native: "not_applicable_empty_cvm_id",
      CXM: "not_applicable_empty_cvm_id",
      unknown: "fail_closed"
    },
    guardCallers: ["fabric_tencent_mutations", "fabric_kubernetes_mutations", "cleanup_tke_compute_residual", "cleanup_tke_nodepool_machines"]
  });
  assert.deepEqual(freeze.verification.customerBasicCanary, {
    runner: "tools/production-live-qa.ts --basic-customer-canary",
    workflow: ".github/workflows/production-basic-customer-operation.yml",
    authority: "manual_release_owner_explicit_write_approval_only",
    runnerIsolation: {
      prepare: "self_hosted_tke_vpc_revision_fabric_kubernetes_and_business_authority",
      complete: "ubuntu_latest_public_browser_websocket_and_single_model_request",
      sharedConcurrency: "production-resource-verification",
      hostedRunnerKubeconfig: false,
      vpcRunnerBrowser: false
    },
    handoff: "same_run_redacted_runtime_ready_evidence_plus_authoritative_public_readback_no_checkpoint_authority",
    publicTestMode: false,
    accountProvisionApi: "POST /api/operator/accounts",
    walletRechargeApi: "POST /api/operator/accounts/{accountId}/wallet-adjustments",
    workspaceLaunchApi: "POST /api/workspace-launches",
    workspaceLaunchPostCount: 1,
    launchReplay: false,
    approvalExpected: ["mergedSha", "cloudImageDigest", "nodePoolId", "resolvedInstanceType", "workspaceImageDigest", "model"],
    preBusinessPostRevisionGate: {
      releaseSource: "exact_merged_origin_main_sha",
      remoteMainVerification: "live_git_ls_remote_before_each_business_write",
      deploySource: "opl-cloud-config_OPL_CLOUD_IMAGE",
      services: ["control-plane", "fabric", "ledger"],
      ownerChain: "deployment_uid_current_revision_to_replicaset_uid_to_ready_pod",
      imageMatch: "deployment_replicaset_and_ready_pod_image_id_equal_approved_cloud_digest",
      failure: "fail_closed_zero_business_posts",
      productionReadinessBooleanAccepted: false
    },
    fabricInternalAccess: {
      tokenSource: "current_kubernetes_secret_opl-cloud-internal-service",
      tokenHandling: "step_memory_only_immediate_mask_no_github_secret",
      podScope: "current_ready_fabric_revision_owner_chain",
      transport: "localhost_kubectl_port_forward",
      cleanup: "exit_trap_kills_port_forward_and_temp_files_removed"
    },
    basicResourceContract: { cpu: 2, memoryGb: 4 },
    skuConsistency: ["node_pool_allocation_plan", "fabric_compute_allocation", "tencent_provider_truth", "operator_provider_fact"],
    resourceEvidence: ["fabric_catalog_cpu_memory", "runtime_pod_limits"],
    recovery: "checkpoint_hint_only_deterministic_account_operation_workspace_authoritative_readback",
    checkpointAuthority: false,
    modelResultUnknown: "attempted_or_ready_without_authoritative_request_identity_never_resend",
    attemptAccounting: "http_attempts_separate_from_authoritative_mutation_counts",
    evidence: ["wallet_recharge_delta", "basic_purchase_delta", "model_usage_delta", "dedicated_pool_N_plus_1", "resolved_instance_type", "basic_2c4g", "new_cvm", "new_cbs", "attachment", "runtime", "workspace_image_id", "password_login", "websocket_101_bidirectional_frames", "single_model_response", "single_usage_record", "billing.workspace_purchased.v1"],
    redaction: "no_password_token_secret_redeem_code_or_provider_request_id",
    currentState: "orchestration_and_fake_tests_only_external_mutation_count_zero"
  });
  assert.deepEqual(deployment.productionBasicCustomerCanary, {
    runner: "tools/production-live-qa.ts",
    mode: "--basic-customer-canary",
    execution: "manual_release_owner_only_not_ci_rollout_or_e2e",
    runnerIsolation: {
      prepare: "self_hosted_tke_vpc_revision_fabric_kubernetes_and_business_authority",
      complete: "ubuntu_latest_public_browser_websocket_and_single_model_request",
      sharedConcurrency: "production-resource-verification",
      hostedRunnerKubeconfig: false,
      vpcRunnerBrowser: false
    },
    handoff: "same_run_redacted_runtime_ready_evidence_plus_authoritative_public_readback_no_checkpoint_authority",
    publicApiAdditions: 0,
    workspaceLaunchPostCount: 1,
    launchPolling: "same_operation_get_only",
    workflow: ".github/workflows/production-basic-customer-operation.yml",
    approvalExpected: ["mergedSha", "cloudImageDigest", "nodePoolId", "resolvedInstanceType", "workspaceImageDigest", "model"],
    preBusinessPostRevisionGate: {
      releaseSource: "exact_merged_origin_main_sha",
      remoteMainVerification: "live_git_ls_remote_before_each_business_write",
      deploySource: "opl-cloud-config_OPL_CLOUD_IMAGE",
      services: ["control-plane", "fabric", "ledger"],
      ownerChain: "deployment_uid_current_revision_to_replicaset_uid_to_ready_pod",
      imageMatch: "deployment_replicaset_and_ready_pod_image_id_equal_approved_cloud_digest",
      failure: "fail_closed_zero_business_posts",
      productionReadinessBooleanAccepted: false
    },
    fabricInternalAccess: {
      tokenSource: "current_kubernetes_secret_opl-cloud-internal-service",
      tokenHandling: "step_memory_only_immediate_mask_no_github_secret",
      podScope: "current_ready_fabric_revision_owner_chain",
      transport: "localhost_kubectl_port_forward",
      cleanup: "exit_trap_kills_port_forward_and_temp_files_removed"
    },
    basicResourceContract: { cpu: 2, memoryGb: 4 },
    skuConsistency: ["node_pool_allocation_plan", "fabric_compute_allocation", "tencent_provider_truth", "operator_provider_fact"],
    requiredWriteApprovals: ["account_provision", "wallet_recharge", "workspace_purchase", "single_model_request"],
    recovery: "checkpoint_hint_only_deterministic_account_operation_workspace_authoritative_readback",
    checkpointAuthority: false,
    modelResultUnknown: "attempted_or_ready_without_authoritative_request_identity_never_resend",
    attemptAccounting: "http_attempts_separate_from_authoritative_mutation_counts",
    output: "redacted_evidence_only",
    currentState: "implemented_and_fake_tested_not_executed"
  });
  assert.equal(deployment.schemaVersion, 21);
  assert.deepEqual(deployment.productionComputeClaimRecovery, {
    workflow: ".github/workflows/production-basic-customer-operation.yml",
    execution: "manual_release_owner_only_not_ci_release_rollout_or_e2e",
    sharedConcurrency: "production-resource-verification",
    runner: ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"],
    modes: {
      diagnose: {
        input: "operation_mode=compute_claim_diagnose",
        environment: "production",
        route: "POST /fabric/compute-claim-recovery/proof",
        credentials: "current_ready_fabric_pod_process_only",
        mutationApproval: false,
        requiredProofMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 }
      },
      recover: {
        input: "operation_mode=compute_claim_recover",
        environment: "production",
        packageId: "basic",
        route: "POST /api/operator/workspace-launches/{operationId}/compute-claim-recovery/claim",
        claimTransport: {
          redirect: "manual",
          acceptedStatusCode: 200,
          redirectStatusCodes: "all_3xx_blocked",
          crossOriginCapabilityForwarding: false
        },
        approvalSecret: "OPL_COMPUTE_CLAIM_RECOVERY_APPROVAL_JSON",
        adminSecrets: ["OPL_SUB2API_ADMIN_EMAIL", "OPL_SUB2API_ADMIN_PASSWORD"],
        customerReadOnlyPasswordSecret: "OPL_BASIC_CANARY_CUSTOMER_PASSWORD",
        customerEmailSource: "existing_account_workflow_value",
        serverCapability: {
          source: "current_kubernetes_secret_opl-cloud-internal-service",
          header: "x-opl-compute-claim-capability",
          ordinaryOperatorSessionAccepted: false,
          handling: "step_memory_only_immediate_mask"
        },
        confirmation: "CLAIM_PROVEN_COMPUTE_RESOURCE",
        continuation: {
          mode: "GET_only_same_launch_after_claim",
          launchRoute: "GET /api/workspace-launches/{operationId}",
          runtimeRoute: "GET /api/workspaces/{workspaceId}/runtime-status",
          terminal: ["status=succeeded", "phase=succeeded", "attachmentId", "receiptId", "launch.url=https://workspace.medopl.cn/w/{workspaceId}/", "receipt.workspaceId=workspaceId", "receipt.type=billing.workspace_purchased.v1", "receipt.storage.resourceId=storageId", "receipt.storage.sizeGb=10", "receipt.fulfillment.attachmentId=attachmentId", "receipt.fulfillment.runtimeId=runtimeId", "runtime.ready=true", "runtime.status=running", "runtime.url=launch.url"],
          forbiddenWrites: ["second_launch", "second_claim", "debit", "recharge", "scale", "create_compute", "create_storage", "delete", "replace"],
          runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
          backgroundMutationCountsState: "unknown"
        }
      }
    },
    artifact: {
      schemaVersion: 2,
      manualReviewSchemaVersion: 1,
      runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
      providerMutationAuthority: ["proof.sub2apiMutationCount", "proof.tencentMutationCount", "proof.kubernetesMutationCount", "proof.evidence"],
      failure: "non_empty_redacted_blocked_json_with_allowlisted_error_code",
      successApprovalFields: ["approvalId", "approvalDigest"],
      blockedArtifactFieldsByMode: {
        compute_claim_diagnose: ["schemaVersion", "operationMode", "status", "recoveryEligible", "errorCode", "runnerDirectMutationCounts"],
        compute_claim_recover: ["schemaVersion", "operationMode", "status", "recoveryEligible", "errorCode", "runnerDirectMutationCounts"],
        compute_claim_recover_continuation: ["schemaVersion", "operationMode", "status", "recoveryEligible", "errorCode", "runnerDirectMutationCounts", "backgroundMutationCountsState"]
      }
    },
    releaseBinding: ["exact_merged_origin_main_sha", "immutable_cloud_digest", "control_plane_fabric_ledger_ready_pod_image_id", "approval_id_and_idempotency_key"],
    targetBinding: ["launch", "account", "workspace", "compute", "machine", "node", "cvm", "pool", "node_pool", "sku", "zone", "private_ip", "billing_facts"],
    mutationBounds: { sub2api: 0, tencent: { min: 0, max: 5 }, kubernetes: { min: 0, max: 1 } },
    mutationLedger: {
      persistence: "original_create_compute_allocation_operation_payload_cas_before_provider_call",
      states: ["reserved", "observed"],
      legacyBindingUpgrade: "binding_without_mutation_ledger_may_reserve_once_after_exact_read_only_proof",
      replayAfterReservation: "authoritative_readback_only_zero_incremental_external_mutation",
      missingOutcome: "conservative_unknown_at_full_bound",
      replayProofUnavailable: "return_persisted_mutation_evidence_zero_incremental_external_mutation",
      observedSuccessReadbackMismatch: "fail_closed_identity_mismatch_claim_final_readback"
    },
    artifactGate: ["exact_target_owned_readback", "proof_sub2api_zero", "proof_tencent_zero_to_five", "proof_kubernetes_zero_to_one", "attempted_equals_count", "confirmed_equals_attempted", "unknown_zero", "missing_empty"],
    claimForbidden: ["debit", "refund", "recharge", "scale", "create_compute", "create_storage", "delete", "replace"],
    currentState: "code_complete_local_focused_and_postgresql_verified_not_merged_released_deployed_or_executed"
  });
  assert.doesNotMatch(await text("tools/production-live-qa.ts"), /SA5\.MEDIUM4/);
  assert.equal(freeze.providerProcurement.workspaceRenewal.tencentRenewFlag, "NOTIFY_AND_MANUAL_RENEW");
  assert.deepEqual(freeze.providerProcurement.workspaceRenewal.fabricPrimitives, ["RenewComputeAllocation", "RenewStorageVolume"]);
  for (const retiredSymbol of [
    "discoverNativeNodePool",
    "matchesPackageNodePool",
    "isDeletingNodePool",
    "waitForNewPoolMachine",
    "selectNewReadyMachine"
  ]) {
    assert.doesNotMatch(provisioner, new RegExp(`\\b${retiredSymbol}\\b`), retiredSymbol);
  }
  assert.match(provisioner, /\bexactNewReadyMachine\b/);
});

test("Current contracts hard cut operator resources, wallet adjustments, and announcements", async () => {
  const [management, sourceTruth, business, boundary, evidence, billing] = await Promise.all([
    json("packages/contracts/opl-cloud-management-contract.json"),
    json("packages/contracts/opl-cloud-console-source-truth-contract.json"),
    json("packages/contracts/opl-cloud-business-object-contract.json"),
    json("packages/contracts/opl-cloud-service-boundary-contract.json"),
    json("packages/contracts/opl-cloud-evidence-ledger-contract.json"),
    json("packages/contracts/opl-cloud-billing-ledger-contract.json")
  ]);

  assert.deepEqual(management.api.operatorAccounts, {
    list: "GET /api/operator/accounts",
    provision: "POST /api/operator/accounts",
    disable: "POST /api/operator/accounts/{accountId}/disable",
    delete: false
  });
  assert.equal(management.identityProvisioning.requestType, "ProvisionAccountRequest");
  assert.deepEqual(management.identityProvisioning.semantics, {
    command: "provision",
    operatorLanguage: "open",
    auditAction: "account.provision",
    operationIdPrefix: "account-provision"
  });
  assert.deepEqual(management.api.operatorReads, {
    overview: "GET /api/operator/overview",
    accounts: "GET /api/operator/accounts",
    workspaces: "GET /api/operator/workspaces",
    workspaceDetail: "GET /api/operator/workspaces/{workspaceId}",
    reconciliation: "GET /api/operator/reconciliation",
    health: "GET /api/operator/health"
  });
  assert.deepEqual(management.operatorProjection.sub2apiReads, {
    currentPageUsers: "GET /api/v1/admin/users/{userId}",
    usersUsage: "POST /api/v1/admin/dashboard/users-usage",
    apiKeysUsage: "POST /api/v1/admin/dashboard/api-keys-usage",
    currentPageKeyCounts: "GET /api/v1/admin/users/{userId}/api-keys?page=1&page_size=1",
    batchSizeMax: 50
  });
  assert.equal("perAccountUserOrUsageNPlusOne" in management.operatorProjection, false);
  assert.equal(management.operatorProjection.pageSizeDefault, 20);
  assert.equal(management.operatorProjection.userAndBalanceRead, "current_page_exact_id_bounded_concurrency_max_4");
  assert.equal(management.operatorProjection.userReadConcurrencyMax, 4);
  assert.equal(management.operatorProjection.usageRead, "current_page_user_ids_batch_required");
  assert.equal(management.operatorProjection.workspaceCountRead, "single_control_plane_group_by_for_current_page");
  assert.equal(management.operatorProjection.usersPagination, "control_plane_order_limit_offset_count_then_current_page_sub2api_reads");
  assert.equal(management.operatorProjection.remoteReadScope, "current_control_plane_page_only");
  assert.equal(management.operatorProjection.scaleInvariant, "same_page_size_request_count_equal_for_100_and_1000_accounts");
  assert.equal(management.operatorProjection.persistence, "none_request_join_only");
  assert.equal(management.operatorProjection.keyCountRead, "current_page_exact_user_id_page_1_size_1_total_only_bounded_concurrency_max_4");
  assert.equal(management.operatorProjection.keyCountDecodedItemsMax, 1);
  assert.equal(management.operatorProjection.readReplica, false);
  assert.equal(management.operatorProjection.partialFailure, "affected_nested_source_unavailable_without_zero_data");
  assert.equal(management.workspaceOwnership.cardinality, "many_workspaces_per_account");
  assert.equal(management.operatorAuthPolicy.defaultRoute, "/console/overview");
  assert.equal(management.operatorAuthPolicy.consoleRouteBehavior, "owner_console_access");
  assert.equal(management.operatorAuthPolicy.navigation, "customer_routes_then_admin_routes");
  assert.equal(management.operatorAuthPolicy.accountPageLabel, "客户与计费账户");
  assert.equal(management.operatorAuthPolicy.reservedAdminAccountInCustomerRows, true);
  assert.equal(management.operatorAuthPolicy.reservedAdminSelfDisable, "forbidden_frontend_and_backend");
  assert.equal(management.operatorAuthPolicy.otherAccountSecretAccess, "forbidden");
  assert.equal(management.operatorBillingReviewProjection.nonBillingRuntimeOperations, "excluded");
  assert.equal(management.operatorBillingReviewProjection.mismatchRecoveryAction, false);
  assert.deepEqual(management.walletAdjustments.kinds, ["recharge", "debit", "business_refund"]);
  assert.equal(management.walletAdjustments.balanceAuthority, "sub2api");
  assert.deepEqual(management.walletAdjustments.routes, {
    create: "POST /api/operator/accounts/{accountId}/wallet-adjustments",
    read: "GET /api/operator/wallet-adjustments/{operationId}",
    recover: "POST /api/operator/wallet-adjustments/{operationId}/recover"
  });
  assert.equal(management.walletAdjustments.unknownResult, "manual_review_without_automatic_replay");
  assert.equal(management.walletAdjustments.serializationContract, "opl-cloud-service-boundary-contract.json#services.controlPlane.walletMutationSerialization");
  assert.deepEqual(management.walletAdjustments.manualReviewRecovery, {
    eligibleStatus: "manual_review",
    allowedAction: "recover_wallet_adjustment",
    requestFields: ["accountId", "evidenceRef"],
    identityReuse: ["original_operation_id", "stable_recovery_intent"],
    legacyV1Identity: "read_only_history_identity_never_payload_or_idempotency_key",
    canonicalV2Identity: `"opl:" + stableID("sub2api-wallet-adjustment-v2", operationID)[:28]`,
    canonicalV2Length: 32,
    preWriteAuthority: "legacy_and_v2_history_absent_unchanged_before_balance_empty_receipt_and_balance_history_ref_no_prior_recovery_write",
    persistBeforeWrite: ["canonical_v2_identity", "identity_version", "legacy_supersession_status", "operator", "authorized_at", "evidence_ref", "stable_recovery_intent"],
    maximumRecoveryMoneyWrites: 1,
    unknownRecoveryResult: "manual_review_without_second_v2_write"
  });
  assert.deepEqual(management.walletAdjustments.upstreamFailureProjection, {
    fields: ["phase", "httpStatus", "errorCode", "requestId"],
    rawBody: false,
    message: false
  });
  assert.equal(management.walletAdjustments.implementation, "code_complete_local_focused_tests");
  assert.deepEqual(evidence.gatewayWalletAdjustmentReceipt.commonRequiredRefs, ["operationId", "kind", "amountUsdMicros", "balanceHistoryRef", "actor"]);
  assert.deepEqual(evidence.gatewayWalletAdjustmentReceipt.businessRefundAdditionalRequiredRefs, ["relatedOperationId"]);
  assert.equal(evidence.gatewayWalletAdjustmentReceipt.implementation, "validator_and_control_plane_exact_readback_code_complete_local_only");
  assert.equal(billing.walletAdjustmentEvidence.balanceAuthority, "sub2api");
  assert.equal(billing.walletAdjustmentEvidence.controlPlaneState, "runtime_operation_non_authoritative");
  assert.equal(billing.walletAdjustmentEvidence.ledgerState, "append_only_reference_non_authoritative");
  assert.equal(billing.walletAdjustmentEvidence.localBalancePersistence, false);
  assert.deepEqual(billing.walletAdjustmentEvidence.redeemCode, {
    version: "v2",
    format: `"opl:" + stableID("sub2api-wallet-adjustment-v2", operationID)[:28]`,
    length: 32,
    pattern: "^opl:[0-9a-f]{28}$",
    legacyV1Length: 49,
    legacyV1Policy: "read_only_history_identity_never_payload_or_idempotency_key"
  });
  assert.equal(billing.walletAdjustmentEvidence.manualReviewRecovery, "explicit_operator_original_operation_v2_supersession_maximum_one_money_write");
  assert.deepEqual(billing.walletAdjustmentEvidence.upstreamFailureFields, ["phase", "httpStatus", "errorCode", "requestId"]);
  assert.equal(billing.walletAdjustmentEvidence.rawUpstreamResponsePersistence, false);
  assert.deepEqual(sourceTruth.sources.operator.walletAdjustmentReview, {
    readRoute: "GET /api/operator/wallet-adjustments/{operationId}",
    recoveryRoute: "POST /api/operator/wallet-adjustments/{operationId}/recover",
    recoveryRequestFields: ["accountId", "evidenceRef"],
    upstreamFailureFields: ["phase", "httpStatus", "errorCode", "requestId"],
    manualReviewAllowedActions: ["recover_wallet_adjustment"],
    rawUpstreamResponse: false,
    upstreamMessage: false
  });
  assert.equal(management.announcements.owner, "control_plane_postgresql");
  assert.deepEqual(management.announcements.tables, ["control_plane_announcements", "control_plane_announcement_reads"]);
  assert.equal(management.announcements.implementation, "code_complete_local_focused_tests");
  assert.equal(boundary.services.controlPlane.owns.includes("announcements"), true);

  const resource = sourceTruth.sources.operator.resources;
  assert.deepEqual(resource.requiredFields, [
    "ownerAccount", "ownerUser", "workspace", "resourceType", "packageOrSpec", "providerId", "zone",
    "status", "createdAt", "expiresAt", "lastReadAt", "operationRef", "receiptRef"
  ]);
  assert.equal(resource.fabricAndLedgerPersistenceInControlPlane, false);
  assert.equal(sourceTruth.sources.identity.operatorAccounts.pagination, "control_plane_order_limit_offset_count_then_current_page_sub2api_reads");
  assert.equal(sourceTruth.sources.identity.operatorAccounts.remoteReadScope, "current_control_plane_page_only");
  assert.equal(sourceTruth.sources.identity.operatorAccounts.userAndBalanceRead, "current_page_exact_id_bounded_concurrency_max_4");
  assert.equal(sourceTruth.sources.identity.operatorAccounts.usageRead, "current_page_user_ids_batch_required");
  assert.equal(sourceTruth.sources.identity.operatorAccounts.workspaceCountRead, "single_control_plane_group_by_for_current_page");
  assert.equal(sourceTruth.sources.identity.operatorAccounts.keyCountRead, "current_page_exact_user_id_page_1_size_1_total_only_bounded_concurrency_max_4");
  assert.equal(sourceTruth.sources.identity.operatorAccounts.keyCountDecodedItemsMax, 1);
  assert.equal(sourceTruth.sources.operator.resources.providerAuthority, "live_fabric_batch_readback_only");
  assert.equal(sourceTruth.sources.operator.resources.controlPlaneProviderSnapshotFallback, false);
  assert.deepEqual(boundary.services.fabric.providerFactsBatchRead, {
    endpoint: "POST /fabric/provider-facts/batch",
    requestDto: { items: ["accountId", "workspaceId", "resourceType", "resourceId"] },
    responseDto: { items: ["accountId", "workspaceId", "resourceType", "resourceId", "available", "facts", "errorCode"] },
    batchSizeMax: 50,
    readOnly: true,
    computeAndStorageAuthority: "tencent_describe",
    attachmentAndRuntimeAuthority: "fabric_and_kubernetes_live_readback",
    independentTimeouts: true,
    unavailableFallback: "none",
    tencentMutationCount: 0
  });
  assert.deepEqual(boundary.services.fabric.runtimeHealthSummaryRead, {
    endpoint: "GET /fabric/runtime-health-summary",
    responseDto: ["total", "ready", "unready"],
    scope: "operator_dashboard_only",
    providerAuthority: "single_kubernetes_deployment_and_pod_aggregate_read",
    deadlineSeconds: 5,
    readOnly: true,
    perWorkspaceItems: false,
    unavailableFallback: "none",
    tencentMutationCount: 0
  });
  assert.deepEqual(boundary.services.fabric.gatewaySecretWrite.requestFields, ["accountId", "workspaceId", "workspaceApiKeyId", "fingerprint", "gatewayApiKey"]);
  assert.equal(sourceTruth.sources.identity.operatorAccounts.failure, "affected_nested_source_unavailable_without_zero_data");
  assert.deepEqual(sourceTruth.sources.operator.routes, {
    overview: "GET /api/operator/overview",
    workspaces: "GET /api/operator/workspaces",
    workspaceDetail: "GET /api/operator/workspaces/{workspaceId}",
    reconciliation: "GET /api/operator/reconciliation",
    health: "GET /api/operator/health"
  });
  assert.equal(boundary.services.controlPlane.operatorProjection.persistence, "none_request_join_only");
  assert.deepEqual(boundary.services.controlPlane.operatorProjection.authorities, ["control_plane", "sub2api", "fabric", "ledger", "runtime"]);
  assert.equal(boundary.services.controlPlane.operatorProjection.userAndBalanceRead, "current_page_exact_id_bounded_concurrency_max_4");
  assert.equal(boundary.services.controlPlane.operatorProjection.usageRead, "current_page_user_ids_batch_required");
  assert.equal(boundary.services.controlPlane.operatorProjection.keyCountRead, "current_page_exact_user_id_page_1_size_1_total_only_bounded_concurrency_max_4");
  assert.equal(boundary.services.controlPlane.operatorProjection.keyCountDecodedItemsMax, 1);
  assert.deepEqual(boundary.services.controlPlane.accountOwnerAuthorization, {
    authority: "active_account_owner_graph",
    reservedAdminOwnerAccount: "acct-admin",
    operatorCapabilityDoesNotGrantCrossAccountOwnership: true
  });
  assert.deepEqual(boundary.services.controlPlane.walletMutationSerialization, {
    deployment: "single_control_plane_replica",
    scope: "process_local",
    primitive: "lockResource(\"sub2api-wallet\", accountId)",
    operations: ["operator_wallet_adjustment", "workspace.launch.v2_debit", "workspace.launch.v2_refund", "workspace_renewal_debit", "workspace_renewal_refund"],
    additionalLockService: false,
    multiReplicaPolicy: "forbidden_until_approved_distributed_serialization"
  });
  assert.deepEqual(boundary.services.fabric.workspaceLaunchManualReviewProviderTruth, {
    endpoint: "GET /fabric/monthly-provider-truth?computeAllocationId=<id>&storageVolumeId=<id>",
    scope: "workspace.launch.v2_manual_review_recovery_only",
    providerAction: "provider_truth",
    providerAuthority: "existing_tencent_provisioner_describe_only",
    localIdentityInputs: ["computeAllocationId", "storageVolumeId"],
    outcomes: ["present", "absent", "unknown"],
    exactVerificationFacts: ["provider_identity", "sku", "zone", "ownership", "chargeType=PREPAID", "renewFlag=NOTIFY_AND_MANUAL_RENEW", "deadline"],
    unknownWhen: "either_local_identity_missing_or_any_exact_fact_unverifiable",
    unknownPolicy: "remain_manual_review_never_absent_never_refund",
    absentRequires: "both_local_identities_and_exact_provider_describe_absence",
    forbiddenSideEffects: ["sync", "tag", "kubectl_apply", "delete", "label", "purchase", "renew", "destroy"]
  });
  assert.equal(boundary.schemaVersion, 15);
  assert.deepEqual(boundary.services.fabric.workspaceComputeClaimRecovery, {
    proofEndpoint: "POST /fabric/compute-claim-recovery/proof",
    claimEndpoint: "POST /fabric/compute-claim-recovery/claim",
    scope: "workspace.launch.v2_compute_claim_pending_before_storage_create",
    proofAuthority: ["launch_operation", "compute_allocation", "allocation_plan", "machine_ownership", "tencent_describe", "kubernetes_get"],
    packages: ["basic", "pro"],
    storageGate: "zero_create_storage_volume_operations_returns_storage_not_started",
    uniqueComputeRule: "one_ready_or_running_machine_in_after_minus_before",
    exactIdentity: ["account", "workspace", "compute_operation", "pool", "node_pool", "machine", "node", "private_ip", "cvm", "sku", "zone"],
    billingFacts: { chargeType: "PREPAID", periodMonths: 1, renewFlag: "NOTIFY_AND_MANUAL_RENEW", deadline: "exact" },
    ownershipProof: { node: ["unallocated", "target_owned"], cvm: ["recoverable", "target_owned"] },
    reasons: ["local_identity", "provider_describe", "iam_rbac", "multiple_candidate", "identity_mismatch", "node_ownership_conflict", "storage_already_started"],
    proofMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    idempotencyBinding: ["launch_operation_id", "idempotency_key", "target_hash", "request_hash"],
    bindingPersistence: "original_create_compute_allocation_operation_payload_cas",
    malformedOrDriftedBinding: "fail_closed_conflict_zero_provider_mutation",
    mutationLedger: {
      persistence: "original_create_compute_allocation_operation_payload_cas_before_provider_call",
      states: ["reserved", "observed"],
      legacyBindingUpgrade: "binding_without_mutation_ledger_may_reserve_once_after_exact_read_only_proof",
      replayAfterReservation: "authoritative_readback_only_zero_incremental_external_mutation",
      missingOutcome: "conservative_unknown_at_full_bound",
      replayProofUnavailable: "return_persisted_mutation_evidence_zero_incremental_external_mutation",
      observedSuccessReadbackMismatch: "fail_closed_identity_mismatch_claim_final_readback"
    },
    replay: "same_key_and_target_returns_same_proof_zero_incremental_external_mutation",
    claimWrites: ["same_cvm_instance_name", "same_cvm_four_ownership_tags", "same_node_single_labels_and_taint_patch", "same_machine_ownership_active"],
    claimMutationBounds: {
      sub2api: 0,
      tencent: { min: 0, max: 5, meaning: "one_instance_name_plus_four_ownership_tags" },
      kubernetes: { min: 0, max: 1, meaning: "one_exact_node_json_patch" }
    },
    mutationEvidence: {
      fields: ["attempted", "confirmed", "unknown", "missing"],
      cardinality: "confirmed_lte_attempted_unknown_lte_attempted_confirmed_plus_unknown_lte_attempted",
      success: "attempted_equals_count_confirmed_equals_attempted_unknown_zero_missing_empty",
      omittedMissing: "normalize_to_empty_only_when_attempted_equals_confirmed_and_unknown_zero",
      missingAllowlist: {
        cvm: ["instance", "instance_name", "opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id"],
        node: ["node_ownership"]
      },
      unstructuredTransport: "bounded_conservative_attempted_equals_count_equals_unknown_and_node_write_forbidden"
    },
    strictReadback: ["cvm_target_owned", "node_target_owned", "machine_ownership_active"],
    forbiddenCalls: ["CreateComputeAllocation", "scale", "debit", "refund", "recharge", "create_storage_volume", "delete", "replace_cvm"],
    fullMonthlyProviderTruthUnchanged: true
  });
  assert.deepEqual(boundary.services.fabric.monthlyPreflightDiagnostics, {
    endpoint: "GET /fabric/monthly-preflight-report?zone=<zone>",
    authentication: "internal_service_token",
    evaluator: "shared_with_normal_monthly_preflight",
    packages: ["basic", "pro"],
    stages: ["launch_permission", "credentials", "node_pool_discovery", "tke_cluster_capacity", "node_pool_contract", "subnet", "zone", "cvm_prepaid_quota", "cvm_sku_price", "cbs_prepaid_quota", "cbs_price"],
    independentChecks: "run_all_without_mutation",
    dependentChecks: "blocked_with_blockedBy",
    safeFactsOnly: true,
    forbiddenData: ["secret", "token", "raw_tencent_response", "provider_request_id", "wallet", "user_data"],
    mutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 }
  });
  assert.equal(billing.chargePolicy.walletMutationSerializationContract, "opl-cloud-service-boundary-contract.json#services.controlPlane.walletMutationSerialization");
  assert.deepEqual(sourceTruth.sources.operator.workspaceLaunchReconciliation, {
    route: "GET /api/operator/reconciliation",
    action: "workspace.launch.v2",
    requiredItemFields: ["accountId", "billingOperationId", "phase", "errorCode", "allowedActions"],
    billingOperationIdentity: "billingOperationId_equals_workspace_launch_operation_id",
    allowedActions: {
      manual_review: ["recover_workspace_launch"],
      allOtherStatuses: []
    },
    recoveryRoute: "POST /api/operator/workspace-launches/{operationId}/recover",
    recoveryRequestFields: ["accountId", "billingOperationId", "evidenceRef"],
    implementation: "integrated_local_fake_verified"
  });
  assert.equal(boundary.externalServices.gateway.currentImplementation, "exact_id_current_page_users_batch_usage_bounded_key_counts_and_full_delegated_key_parity_code_complete_local_only");
  const announcement = business.objectKinds.find((entry: { kind: string }) => entry.kind === "Announcement");
  assert.equal(Boolean(announcement), true);
  assert.equal(announcement.implementation, "code_complete_local_focused_tests");
});

test("Current Console binds delegated Gateway credentials to process-local Console sessions", async () => {
  const [management, boundary, deployment] = await Promise.all([
    json("packages/contracts/opl-cloud-management-contract.json"),
    json("packages/contracts/opl-cloud-service-boundary-contract.json"),
    json("packages/contracts/opl-cloud-deployment-contract.json")
  ]);

  assert.deepEqual(management.identitySecurity.delegatedGatewayCredential, {
    authority: "sub2api_login_access_token",
    storage: "control_plane_process_memory_only",
    index: "hashed_session_lookup_key",
    lifetime: "bounded_by_console_session",
    missingOrExpired: "401_reauthentication_required_and_cookie_clear",
    forbidden: ["browser", "postgresql", "ledger", "logs"]
  });
  assert.equal(boundary.services.controlPlane.sessionDelegatedGatewayCredential.persistence, "process_memory_only");
  assert.equal(boundary.services.controlPlane.sessionDelegatedGatewayCredential.lookupKey, "hashed_console_session_key");
  assert.deepEqual(deployment.controlPlaneSessionCredentialVault, {
    replicas: 1,
    strategyType: "Recreate",
    persistence: "none",
    restartBehavior: "reauthentication_required",
    horizontalScaling: "blocked_pending_secure_shared_vault"
  });
});

test("Current human truth preserves public entry points and evidence levels", async () => {
  const [invariants, architecture, status, consoleProduct, runbook, readme, devGuide, decisions, project] = await Promise.all([
    text("docs/invariants.md"),
    text("docs/architecture.md"),
    text("docs/status.md"),
    text("docs/product/console-workspace-v1.md"),
    text("docs/runtime/production-runbook.md"),
    text("README.md"),
    text("DEV_GUIDE.md"),
    text("docs/decisions.md"),
    text("docs/project.md")
  ]);

  for (const document of [invariants, architecture, status, consoleProduct, runbook, readme, devGuide, decisions, project]) {
    assert.doesNotMatch(document, /OPL_GATEWAY_PUBLIC_BASE_URL|GET \/api\/gateway\/endpoint/);
  }
  for (const document of [invariants, architecture, status, consoleProduct]) {
    assert.match(document, /code-complete/i);
    assert.match(document, /pilot-ready/i);
    assert.match(document, /production-proven/i);
  }
  assert.match(consoleProduct, /Home.*Login.*Logo/is);
  assert.match(consoleProduct, /URL.*用户名.*密码.*Workspace Key/is);
  assert.match(invariants, /Dedicated `workspace\.launch\.v2` review recovery[\s\S]{0,800}integrated local fake evidence/i);
  assert.doesNotMatch(invariants, /pending integrated verification/i);
  assert.doesNotMatch(invariants, /stops at\s+`debited`[\s\S]{0,300}S8/i);
  assert.doesNotMatch(invariants, /durable `workspace\.launch` RuntimeOperation/);
  assert.doesNotMatch(invariants, /manual[- ]review[^.\n]{0,160}code-complete/i);
  assert.match(runbook, /OPL_POSTGRES_TESTS=1/);
  assert.match(runbook, /OPL_CAPACITY_TESTS=1/);
  assert.match(runbook, /Action=skip/);
});
