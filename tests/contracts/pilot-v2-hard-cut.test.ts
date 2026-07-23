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

test("Pilot V2 contracts hard cut Gateway keys and source envelopes", async () => {
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

test("Pilot V2 contracts hard cut Workspace purchase, access, and Runtime facts", async () => {
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
  assert.equal(freeze.workspaceLaunch.legacyNonTerminalPolicy, "clear_or_manual_handle_before_cutover_never_resume_in_v2_worker");
  assert.equal(freeze.workspaceLaunch.backgroundProgression, "non_review_and_manual_review_recovery_integrated_local_fake_verified");
  assert.equal(freeze.workspaceLaunch.nextBlockedStage, undefined);
  assert.deepEqual(freeze.workspaceLaunch.fulfillmentResources, ["compute", "storage", "attachment", "gateway_secret", "runtime"]);
	assert.deepEqual(freeze.gateway.workspaceKeyLifecycle, {
		launchConvergence: "zero_create_one_active_reuse_other_fail_closed",
		rotationApi: "POST /api/workspaces/{workspaceId}/workspace-key/rotate",
		mutationCredential: "session_delegated_user_bearer",
		workspacePersistence: "workspace_api_key_id_only",
		operationPersistence: "control_plane_runtime_operations_non_secret_phases",
		phases: ["replacement_check", "replacement_create", "secret_write", "retire_old", "promote_replacement", "workspace_commit", "runtime_apply", "delete_old", "receipt", "complete"],
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

test("Pilot V2 contracts hard cut operator resources, wallet adjustments, and announcements", async () => {
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
    users: "GET /api/v1/admin/users",
    usersUsage: "POST /api/v1/admin/dashboard/users-usage",
    apiKeysUsage: "POST /api/v1/admin/dashboard/api-keys-usage",
    currentPageKeyCounts: "GET /api/v1/admin/users/{userId}/api-keys",
    batchSizeMax: 50
  });
  assert.equal(management.operatorProjection.perAccountUserOrUsageNPlusOne, false);
  assert.equal(management.operatorProjection.usersPagination, "collect_all_coherent_sub2api_user_pages");
  assert.equal(management.operatorProjection.persistence, "none_request_join_only");
  assert.equal(management.operatorProjection.keyCountRead, "selected_control_plane_page_only_bounded_concurrency_max_4");
  assert.equal(management.operatorProjection.readReplica, false);
  assert.equal(management.operatorProjection.partialFailure, "affected_nested_source_unavailable_without_zero_data");
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
  assert.equal(sourceTruth.sources.identity.operatorAccounts.pagination, "collect_all_coherent_sub2api_user_pages_then_control_plane_page_with_key_counts_only_for_selected_page");
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
  assert.equal(boundary.externalServices.gateway.currentImplementation, "paginated_users_batch_usage_current_page_bounded_key_counts_and_full_delegated_key_parity_code_complete_local_only");
  const announcement = business.objectKinds.find((entry: { kind: string }) => entry.kind === "Announcement");
  assert.equal(Boolean(announcement), true);
  assert.equal(announcement.implementation, "code_complete_local_focused_tests");
});

test("Pilot V2 binds delegated Gateway credentials to process-local Console sessions", async () => {
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

test("Pilot V2 current human truth preserves public entry points and evidence levels", async () => {
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
  assert.doesNotMatch(invariants, /S9|manual[- ]review[^.\n]{0,160}code-complete/i);
  assert.match(runbook, /OPL_POSTGRES_TESTS=1/);
  assert.match(runbook, /OPL_CAPACITY_TESTS=1/);
  assert.match(runbook, /Action=skip/);
});
