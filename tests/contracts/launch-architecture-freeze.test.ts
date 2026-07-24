import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);
const candidateAppSha = "6b334ef7f239eb01c40578159e6df9ed2e7f97dc";
const candidateShellSha = "dbd9d68115604673df85033d7a0ab323d65a79a2";
const candidateFrameworkSha = "51d16f0e93aebf3fd5ccf96082490395fcbb8711";

async function text(path) {
  return readFile(new URL(path, root), "utf8");
}

async function json(path) {
  return JSON.parse(await text(path));
}

test("root agent instructions require the launch invariants", async () => {
  const [agents, gitignore] = await Promise.all([text("AGENTS.md"), text(".gitignore")]);

  assert.match(agents, /docs\/invariants\.md/);
  assert.match(agents, /Before changing billing, Fabric, Workspace, Gateway, Ledger, deployment, or E2E/i);
  assert.match(gitignore, /^\.codegraph\/$/m);
});

test("launch freeze fixes the V2 products, owner lanes, settlement, and verification slot", async () => {
  const freeze = await json("packages/contracts/opl-cloud-launch-freeze-contract.json");

  assert.equal(freeze.architectureAuthority.repository, "https://github.com/gaofeng21cn/one-person-lab-cloud");
  assert.equal(freeze.architectureAuthority.reviewedRevision, "c349a41d860e706ed43a4090b9e75abb0b130971");
  assert.deepEqual(Object.keys(freeze.productSurfaces), ["gateway", "workspace", "serve", "console", "fabric", "ledger"]);
  assert.deepEqual(freeze.productSurfaces.serve, { product: "OPL Serve", launchStatus: "planned_not_in_launch" });
  assert.match(freeze.machineBoundary, /Six product surfaces.*OPL Serve.*planned_not_in_launch/);
  assert.match(freeze.machineBoundary, /two paused production verification slots/i);
  assert.deepEqual(Object.keys(freeze.ownerLanes), ["console", "fabric", "gateway", "ledger"]);
  assert.deepEqual(freeze.customerProducts.basic, {
    priceVersion: "pilot-usd-2026-07-v1",
    currency: "USD",
    compute: { cpu: 2, memoryGb: 4, usdMicros: 50000000 },
    storage: { sizeGb: 10, usdMicros: 2580000 },
    totalUsdMicros: 52580000,
    targetSaleable: true,
    productionCatalogAvailable: true,
    realSubscriptionEvidence: "pending",
    productionProven: false
  });
  assert.deepEqual(freeze.customerProducts.pro, {
    priceVersion: "pilot-usd-2026-07-v1",
    currency: "USD",
    compute: { cpu: 8, memoryGb: 16, usdMicros: 214280000 },
    storage: { sizeGb: 100, usdMicros: 25800000 },
    totalUsdMicros: 240080000,
    targetSaleable: true,
    productionCatalogAvailable: true,
    realSubscriptionEvidence: "not_executed_by_scope",
    productionProven: false
  });
  assert.equal(freeze.deliveryEvidence.productionProven, false);
  assert.deepEqual(freeze.internalProviderCostEvidence, {
    currency: "CNY",
    computeMonthlyCnyCents: { basic: 35000, pro: 150000 },
    storageMonthlyCnyCents: { "10": 1800, "100": 18000 },
    customerChargeDerivation: "forbidden"
  });
  assert.equal(freeze.workspaceLaunch.priceVersion, "pilot-usd-2026-07-v1");
  assert.equal(freeze.workspaceLaunch.currency, "USD");
  assert.deepEqual(freeze.workspaceLaunch.requiredCreateFields, ["name", "packageId", "sizeGb", "autoRenew"]);
  assert.deepEqual(freeze.workspaceLaunch.validPackageStoragePairs, [
    { packageId: "basic", sizeGb: 10 },
    { packageId: "pro", sizeGb: 100 }
  ]);
  assert.equal(freeze.workspaceLaunch.packageAvailabilitySource, "live_fabric_catalog");
  assert.equal(freeze.workspaceLaunch.unavailablePackageBehavior, "package_unavailable_before_gateway_balance_debit_ledger_or_tencent_calls");
  assert.deepEqual(freeze.workspaceLaunch.requestHashFields, ["accountId", "ownerUserId", "name", "packageId", "sizeGb", "autoRenew", "priceVersion"]);
  assert.deepEqual(freeze.workspaceLaunch.workspaceIdentity, {
    derivation: "stable_from_account_id_and_launch_operation_idempotency_identity",
    sameIdempotencyRequest: "same_workspace",
    newIdempotencyRequest: "new_workspace",
    accountConcurrentPaidLaunches: 1
  });
  assert.equal(freeze.workspaceLaunch.autoRenew.submission, "required_false_boolean");
  assert.equal(freeze.workspaceLaunch.autoRenew.defaultProductIntent, false);
  assert.equal(freeze.workspaceLaunch.autoRenew.customerMutable, false);
  assert.equal(freeze.workspaceLaunch.autoRenew.enablementGate, "hidden_until_real_renewal_evidence");
  assert.equal(freeze.workspaceLaunch.autoRenew.childProjection, "read_only_compute_and_storage_compatibility_until_workspace_canonical_state");
  assert.deepEqual(freeze.workspaceLaunch.customerResponsePricingFields, ["priceVersion", "currency", "totalChargeUsdMicros"]);
  assert.deepEqual(freeze.workspaceLaunch.manualReviewRecovery, {
    route: "POST /api/operator/workspace-launches/{operationId}/recover",
    requestFields: ["accountId", "billingOperationId", "evidenceRef"],
    eligibleStatus: "manual_review",
    allowedAction: "recover_workspace_launch",
    nonEligibleStatuses: "no_allowed_actions",
    providerTruthContract: "opl-cloud-service-boundary-contract.json#services.fabric.workspaceLaunchManualReviewProviderTruth",
    matrix: {
      computeReadyStorageAbsent: "resume_storage_fulfilling_with_original_operation_identity",
      computeReadyStorageReady: "resume_attaching_with_original_operation_identity",
      computeAbsentStorageAbsent: "one_idempotent_workspace_refund",
      computeAbsentStorageReady: "remain_manual_review",
      providerUnknown: "remain_manual_review",
      receiptPending: "retry_purchase_receipt_only",
      refundConfirmedReceiptPending: "retry_refund_receipt_only"
    },
    implementation: "integrated_local_fake_verified"
  });

  assert.deepEqual(freeze.monthlySettlement.protocol, ["debit", "fabric_fulfillment", "claim", "activate", "record_workspace_receipt"]);
  assert.equal(freeze.monthlySettlement.confirmedNoResourceAfterDebit, "idempotent_refund");
  assert.equal(freeze.monthlySettlement.partialOrUnknownProviderResult, "manual_review_without_refund");
  assert.deepEqual(freeze.monthlySettlement.exactBalanceEvidence, {
    precondition: "preBalanceUsdMicros > totalChargeUsdMicros",
    postcondition: "postBalanceUsdMicros == preBalanceUsdMicros - totalChargeUsdMicros",
    mismatchStatus: "manual_review",
    fabricWriteCountOnMismatch: 0
  });
  assert.deepEqual(freeze.workspaceLaunch.providerPreflightRecovery, {
    timing: "before_first_charge_attempt",
    runWhen: "ChargeAttempted=false_and_ChargeConfirmation_absent",
    skipOnRecoveryWhenAnyPresent: ["ChargeAttempted", "ChargeConfirmation"],
    writes: "none"
  });
  assert.equal(freeze.providerProcurement.chargeType, "PREPAID");
  assert.equal(freeze.providerProcurement.periodMonths, 1);
  assert.equal(freeze.providerProcurement.renewFlag, "NOTIFY_AND_MANUAL_RENEW");
  assert.deepEqual(freeze.providerProcurement.forbiddenChargeTypes, ["POSTPAID_BY_HOUR"]);
  assert.deepEqual(freeze.providerProcurement.mutationPermissionGate, {
    env: "RUN_TENCENT_CREATE_RELEASE_EXECUTION",
    requiredValue: "1",
    check: "shared_tencent_monthly_preflight_before_sub2api_debit",
    failure: "zero_charge_zero_fabric_mutation"
  });
  assert.deepEqual(freeze.providerProcurement.nodePoolDiscovery, {
    api: "DescribeNodePools",
    matchLabels: ["oplcloud.cn/pool-id", "oplcloud.cn/package-id", "oplcloud.cn/instance-type"],
    requiredMatchCount: 1,
    zeroOrMultipleMatches: "preflight_failure_before_debit",
    actualNodePoolIdPersistence: "workspace.launch.v2 operation before debit",
    repeatPreflightMismatch: "preflight_failure_before_debit"
  });
  assert.deepEqual(freeze.providerProcurement.activationReadback, {
    apis: ["SyncMonthlyCompute", "SyncMonthlyStorage"],
    timing: "immediately_before_workspace_activation",
    sharedRequiredFacts: ["resource_identity", "account_identity", "workspace_identity", "zone", "chargeType=PREPAID", "renewFlag=NOTIFY_AND_MANUAL_RENEW", "deadline"],
    computeRequiredFacts: ["sku"],
    storageRequiredFacts: ["capacity"],
    mismatch: "manual_review_without_activation"
  });
  assert.deepEqual(freeze.providerProcurement.unpaidExpiry, {
    workspaceAccess: "deny_immediately",
    autoRenew: false,
    providerAction: "none_expire_by_provider",
    fabricMutationCount: 0,
    tencentMutationCount: 0
  });
  assert.equal(freeze.workspaceLaunch.codeCompleteThroughPhase, undefined);
  assert.equal(freeze.workspaceLaunch.nextBlockedStage, undefined);
  assert.match(freeze.workspaceLaunch.currentImplementation, /manual-review recovery.*integrated local fake evidence/i);
  assert.doesNotMatch(freeze.workspaceLaunch.currentImplementation, /pending integrated verification/i);
  assert.doesNotMatch(freeze.workspaceLaunch.currentImplementation, /S9|manual review.*code-complete/i);
  assert.deepEqual(freeze.workspaceRuntime.sourceImage, {
    appRepository: "https://github.com/gaofeng21cn/one-person-lab-app.git",
    activeShellRepository: "https://github.com/gaofeng21cn/opl-aion-shell.git",
    frameworkRepository: "https://github.com/gaofeng21cn/one-person-lab.git",
    candidateAppMainSha: candidateAppSha,
    candidateActiveShellMainSha: candidateShellSha,
    candidateFrameworkMainSha: candidateFrameworkSha,
    candidateRequirements: ["40_character_git_sha", "merged_into_repository_main"]
  });
  assert.deepEqual(freeze.workspaceRuntime.releaseEvidence, {
    immutableTcrDigest: null,
    immutableTcrDigestStatus: "pending_publication_readback",
    readyPodImageId: null,
    readyPodImageIdStatus: "pending_deployment_readback"
  });
  assert.deepEqual(freeze.workspaceRuntime.projectFacts, {
    launchStatus: "paused_not_in_release",
    authority: "workspace_runtime_projects_mount_and_statfs",
    apiRoutes: [],
    consolePresentation: "absent",
    persistence: "none",
    releaseValidation: "direct_runtime_pod_sha256_markers_only",
    correspondingEvidence: "not_claimed"
  });
  assert.equal(freeze.workspaceRuntime.workspaceCardinality, "many_per_account");
  assert.deepEqual(freeze.workspaceRuntime.gatewaySecret, {
    scope: "workspace",
    writeIdentityFields: ["accountId", "workspaceId", "workspaceApiKeyId", "fingerprint"],
    newWritePolicy: "workspace_scoped_deterministic_identity_only",
    legacyReadCompatibility: "explicit_persisted_scope_or_ref_only",
    legacyMigration: "first_target_workspace_key_rotation_without_automatic_runtime_restart",
    scopeInferenceForbidden: ["workspace_count", "key_name"]
  });
  assert.equal(freeze.workspaceRuntime.statusContainsPassword, false);
  assert.equal(freeze.workspaceRuntime.runtimeRequestIdentityBinding, "not_in_pilot_requires_sso");
  assert.deepEqual(freeze.workspaceRuntime.credentialCommands, {
    reveal: "POST /api/workspaces/{workspaceId}/runtime-credentials/reveal",
    rotate: "POST /api/workspaces/{workspaceId}/runtime-credentials/rotate",
    authorization: "workspace.ownerUserId_equals_session_user.id",
    cacheControl: "private, no-store",
    passwordPersistence: "none",
    rotationReceiptType: "workspace.access_token_reset"
  });
  assert.equal(freeze.gateway.sub2apiMutable, false);
  assert.equal(freeze.gateway.backend, "Sub2API");
  assert.equal(freeze.gateway.compatibilityGate, "required_capabilities");
  assert.equal(freeze.gateway.versionRole, "diagnostic_only");
  assert.equal("compatibleVersions" in freeze.gateway, false);
  assert.deepEqual(freeze.gateway.compatibilityEvidence, ["contract_tests", "read_only_production_probe"]);
  assert.equal(freeze.productSurfaces.gateway.backend, freeze.gateway.backend);
  assert.equal(freeze.gateway.legacyWorkspaceKeyName, "opl-workspace");
  assert.equal(freeze.gateway.newWorkspaceKeyName, "stable_reserved_name_derived_from_workspace_id");
  assert.equal(freeze.gateway.workspaceKeyActiveCardinality, "one_per_workspace");
  assert.deepEqual(freeze.gateway.workspaceKeyLifecycle.scopeIdentity, ["workspaceId", "workspaceApiKeyId"]);
  assert.equal(freeze.gateway.workspaceKeyLifecycle.oldKeyRetirementGate, "only_after_runtime_authoritative_readback_and_atomic_workspace_commit");
  assert.equal(freeze.gateway.workspaceKeyLifecycle.runtimeCredentialInvariant, "key_rotation_does_not_change_username_password_or_credential_version");
  assert.deepEqual(freeze.gateway.usageScope, ["user_id", "api_key_id"]);
  assert.equal(freeze.gateway.usageMoneySource, "actual_cost");
  assert.equal(freeze.gateway.usageMoneyRepresentation, "integer_usd_micros");
  assert.equal(freeze.gateway.rawAdminDTOForwarding, false);
  assert.equal(freeze.gateway.missingCapabilityBehavior, "dependent_surface_unavailable_never_zero");
  assert.equal(freeze.gateway.summaryApi, undefined);
  assert.equal(freeze.gateway.customerReadContract, "opl-cloud-console-source-truth-contract.json");
  assert.deepEqual(freeze.gateway.emptyListPaginationRule, {
    appliesTo: ["Keys", "UserKeys", "Usage", "BalanceHistory"], total: 0, page: 1, pages: 1, items: [], otherShapes: "reject"
  });
  assert.deepEqual(freeze.gateway.customerMutationApis, [
    "create_general_key",
    "update_general_key",
    "delete_general_key",
    "reveal_owned_key",
    "change_group",
    "reset_quota",
    "reset_rate_limit_usage"
  ]);
  assert.equal(freeze.consoleFinancialProjection.mode, "read_only_projection");
  assert.deepEqual(freeze.consoleFinancialProjection.authorities, {
    balanceApiKeysAndRequestUsage: "Sub2API",
    resourceBillingHistory: "Ledger receipts",
    workspaceAndEntitlements: "Control Plane"
  });
  assert.deepEqual(freeze.consoleFinancialProjection.prohibitions, [
    "second_wallet",
    "usage_database",
    "billing_fact_table",
    "raw_admin_dto",
    "browser_to_sub2api",
    "frontend_financial_derivation",
    "payment_or_topup_ui",
    "prompt_or_response_content"
  ]);

  assert.deepEqual(freeze.verification.slots, [
    {
      id: "verification-slot-basic-01", packageId: "basic", computeInstanceType: "SA5.MEDIUM4",
      resources: { cpu: 2, memoryGb: 4, cbsGb: 10 }, customerProduct: false,
      chargeType: "PREPAID", periodMonths: 1, renewFlag: "NOTIFY_AND_MANUAL_RENEW", reuseForBillingPeriod: true, concurrency: 1
    },
    {
      id: "verification-slot-pro-01", packageId: "pro", computeInstanceType: "SA5.2XLARGE16",
      resources: { cpu: 8, memoryGb: 16, cbsGb: 100 }, customerProduct: false,
      chargeType: "PREPAID", periodMonths: 1, renewFlag: "NOTIFY_AND_MANUAL_RENEW", reuseForBillingPeriod: true, concurrency: 1
    }
  ]);
  assert.equal(freeze.verification.purchaseBudget, 2);
  assert.equal("purchaseBudgetRemaining" in freeze.verification, false);
  assert.deepEqual(freeze.verification.providerAcceptance, {
    launchStatus: "paused",
    releaseGate: false,
    operatorOnly: true,
    approvalEnvironment: "production-provider-acceptance",
    credentialEnv: "OPL_PROVIDER_ACCEPTANCE_TOKEN",
    credentialHeader: "x-opl-provider-acceptance-token",
    operatorSessionAccepted: false,
    genericOperatorTokenAccepted: false,
    operationCardinality: 2,
    operationCardinalityPerSlot: 1,
    fixedSlotOperationReplayable: true,
    slotExistenceSource: ["workspace", "compute", "storage"]
  });
  assert.equal(freeze.verification.perRunTencentPurchase, false);
  assert.equal(freeze.verification.monthlyBillingBackend, "fake");
  assert.equal(freeze.verification.gatewayRequest, "real_dedicated_test_key");
  assert.deepEqual(freeze.verification.providerResourcesDeletedPerRun, []);
  assert.equal(freeze.launchStages.length, 10);
  assert.equal("slides" in freeze, false);
  assert.equal(freeze.deliveryPhases.length, 6);
});

test("human launch contract pins the approved architecture authority revision", async () => {
  const [freeze, invariants] = await Promise.all([
    json("packages/contracts/opl-cloud-launch-freeze-contract.json"),
    text("docs/invariants.md")
  ]);

  assert.match(invariants, new RegExp(freeze.architectureAuthority.reviewedRevision));
  for (const sha of [candidateAppSha, candidateShellSha, candidateFrameworkSha]) assert.match(invariants, new RegExp(sha));
  assert.doesNotMatch(invariants, /13ae5d1410e1a4349c14dc76e7c3446ff200cfdb/);
  assert.match(invariants, /metadata\/statfs API and Console presentation are paused/i);
});

test("public Workspace contract permits multiple independent Workspaces", async () => {
  const readme = await text("README.md");

  assert.match(readme, /one Account\/Wallet may own\s+multiple independent Workspaces/i);
  assert.match(readme, /new identity creates another Workspace/i);
  assert.doesNotMatch(readme, /one account owns exactly one\s+primary Workspace|second Workspace.*409/i);
});

test("every launch stage declares business, current state, deliverables, and evidence", async () => {
  const freeze = await json("packages/contracts/opl-cloud-launch-freeze-contract.json");
  const expected = [
    "offer_identity",
    "wallet_quote",
    "balance_debit",
    "prepaid_fulfillment",
    "claim_and_activate",
    "workspace_access",
    "gateway_usage",
    "renewal_expiry_recovery",
    "reusable_verification",
    "production_release"
  ];

  assert.deepEqual(freeze.launchStages.map((stage) => stage.id), expected);
  for (const stage of freeze.launchStages) {
    assert.ok(stage.business, `${stage.id} business`);
    assert.ok(Array.isArray(stage.owners) && stage.owners.length > 0, `${stage.id} owners`);
    assert.ok(stage.currentState, `${stage.id} currentState`);
    assert.ok(Array.isArray(stage.requiredDeliverables) && stage.requiredDeliverables.length > 0, `${stage.id} requiredDeliverables`);
    assert.ok(Array.isArray(stage.completionEvidence) && stage.completionEvidence.length > 0, `${stage.id} completionEvidence`);
  }
});

test("human invariants reject paid per-run resource verification", async () => {
  const invariants = await text("docs/invariants.md");

  for (const heading of ["Console", "Fabric", "Ledger", "Gateway", "Launch Stages", "Verification Slot"]) {
    assert.match(invariants, new RegExp(`## ${heading}`));
  }
  assert.match(invariants, /SA5\.MEDIUM4/);
  assert.match(invariants, /debit.*provision.*claim.*activate/is);
  assert.match(invariants, /confirmed.*no billable resource.*refund/is);
  assert.match(invariants, /POSTPAID_BY_HOUR.*forbidden/is);
  assert.doesNotMatch(invariants, /monthly settlement requires Sub2API-owned `reserve`/i);
  assert.match(invariants, /version is diagnostic metadata and never blocks/i);
  assert.match(invariants, /actual_cost.*integer USD micros/is);
  assert.match(invariants, /Raw Sub2API admin responses.*never reach Console/is);
  assert.doesNotMatch(invariants, /Cloud accepts the API-compatible v0\.1\./i);
  assert.doesNotMatch(invariants, /Production E2E requires explicit confirmation that it spends real balance/);
  assert.doesNotMatch(invariants, /Fabric prepares before charge/);
  assert.match(invariants, /POST \/api\/operator\/accounts.*ProvisionAccountRequest/is);
  assert.match(invariants, /lockResource\("sub2api-wallet", accountId\)/);
  assert.match(invariants, /preBalanceUsdMicros > totalChargeUsdMicros/);
  assert.match(invariants, /ChargeAttempted.*ChargeConfirmation.*skip/is);
  assert.match(invariants, /SyncMonthlyCompute.*SyncMonthlyStorage.*activation/is);
  assert.match(invariants, /GET \/fabric\/monthly-provider-truth\?computeAllocationId=<id>&storageVolumeId=<id>/);
  assert.match(invariants, /provider_truth.*Describe-only/is);
  assert.match(invariants, /local\s+identit.*unknown.*absent.*refund/is);
  assert.match(invariants, /does not run.*Sync.*Tag.*kubectl apply.*delete.*label.*purchase.*renew.*destroy/is);
  assert.match(invariants, /recover_workspace_launch/);
  assert.doesNotMatch(invariants, /manual[- ]review[^.\n]{0,160}code-complete|S9/i);
});

test("paused fixed-slot verification does not gate the Basic rollout", async () => {
  const deployment = await json("packages/contracts/opl-cloud-deployment-contract.json");
  const [architecture, decisions, project, readme, runbook, status] = await Promise.all([
    text("docs/architecture.md"),
    text("docs/decisions.md"),
    text("docs/project.md"),
    text("README.md"),
    text("docs/runtime/production-runbook.md"),
    text("docs/status.md")
  ]);

  assert.equal(deployment.productionVerificationWorkflow.launchStatus, "paused");
  assert.equal(deployment.productionVerificationWorkflow.releaseGate, false);
  assert.equal(deployment.productionVerificationWorkflow.mode, "read_only_dual_fixed_slots");
  assert.equal(deployment.productionLiveQaJob, undefined);
  assert.equal(deployment.providerAcceptanceWorkflow.launchStatus, "paused");
  assert.equal(deployment.providerAcceptanceWorkflow.releaseGate, false);
  assert.doesNotMatch(JSON.stringify(deployment), /paid_confirmation|OPL_VERIFY_PAID_CONFIRMATION|OPL_VERIFY_MODEL_ACCESS_KEY/);
  assert.equal(deployment.workspaceImage.candidateAppMainSha, candidateAppSha);
  assert.equal(deployment.workspaceImage.candidateActiveShellMainSha, candidateShellSha);
  assert.equal(deployment.workspaceImage.candidateFrameworkMainSha, candidateFrameworkSha);
  assert.deepEqual(deployment.workspaceImage.candidateRequirements, ["40_character_git_sha", "merged_into_repository_main"]);
  assert.equal(deployment.workspaceImage.immutableTcrDigest, null);
  assert.equal(deployment.workspaceImage.immutableTcrDigestStatus, "pending_publication_readback");
  assert.equal(deployment.workspaceImage.readyPodImageId, null);
  assert.equal(deployment.workspaceImage.readyPodImageIdStatus, "pending_deployment_readback");
  assert.equal(deployment.workspaceImage.productionReference, "repository@sha256");
  assert.match(runbook, /Do not run the legacy paid\s+verifier/);
  assert.match(architecture, /debit.*before\s+Fabric.*activate/is);
  assert.doesNotMatch(architecture, /Fabric preparation happens before the external charge/);
  for (const document of [architecture, decisions, project, readme, status]) {
    assert.doesNotMatch(document, /single paid verifier|one paid production verifier|explicitly confirmed paid E2E/i);
  }
});

test("current rollout truth contains no legacy Workspace image evidence", async () => {
  const paths = [
    ".github/workflows/release-opl-cloud-image.yml",
    ".env.example",
    "docs/invariants.md",
    "docs/architecture.md",
    "packages/contracts/opl-cloud-launch-freeze-contract.json",
    "packages/contracts/opl-cloud-deployment-contract.json"
  ];

  for (const path of paths) {
    const source = await text(path);
    assert.doesNotMatch(source, /v?26\.7\.1[23]/, path);
    assert.doesNotMatch(source, /9d867fe0fc9db48b6efa27371d77770e46fc8cd97d26ef85a81fbdac7e96ca76/, path);
    assert.doesNotMatch(source, /6e1491a3693a820a37b81ab9a26f8efc4262fb9581f981641c6de084b0fa654f/, path);
  }
});
