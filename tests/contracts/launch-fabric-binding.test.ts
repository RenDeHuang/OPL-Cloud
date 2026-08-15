import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);
const text = (path: string) => readFile(new URL(path, root), "utf8");
const json = async (path: string) => JSON.parse(await text(path));

test("Control Plane owns the launch identity and recovery authorization", async () => {
  const contract = await json("packages/contracts/opl-cloud-control-plane-launch-contract.json");

  assert.equal(contract.owner, "services/control-plane");
  assert.equal(contract.schemaVersion, 4);
  assert.equal(contract.launchOperation.action, "workspace.launch.v2");
  assert.equal(contract.launchOperation.resultSchemaVersion, 3);
  assert.deepEqual(contract.launchOperation.identityFields, [
    "launchOperationId", "accountId", "ownerUserId", "workspaceId", "requestHash"
  ]);
  assert.equal(contract.stageDecision.fabricOperationBinding, "opl-cloud-fabric-launch-binding-contract.json");
  assert.deepEqual(contract.stageDecision.preflightAdmission, {
    timing: "before_first_external_write",
    mode: "read_only",
    durableStage: false
  });
  assert.deepEqual(contract.stageDecision.orderedStages, [
    "key", "debit", "ensure_compute_allocation", "storage", "attachment",
    "secret", "runtime", "activation", "receipt", "succeeded"
  ]);
  assert.deepEqual(contract.stageDecision.persistence, {
    row: "control_plane_runtime_operation",
    statusField: "status",
    durableResultControlFields: [
      "schemaVersion", "version", "stage", "attempts", "observations", "consumedResumeAuthorizations",
      "resumeAuthorization", "resumeAuthorizationConsumedAt", "idempotentReplayClaims",
      "freshContinuationAuthorizations", "continuationReadClaims"
    ],
    forbiddenResultFields: ["phase", "currentDecision"],
    cas: "exact_prior_result_and_launch_identity_single_winner"
  });
  assert.deepEqual(contract.stageDecision.attemptPersistence, {
    reconcilerSource: "services/control-plane/internal/server/workspace_launch_reconciler.go",
    stageAdapterSources: [
      "services/control-plane/internal/server/workspace_launch_account_stages.go",
      "services/control-plane/internal/server/workspace_launch_fabric_stages.go",
      "services/control-plane/internal/server/workspace_launch_activation.go"
    ],
    goFields: ["Attempted", "Confirmed", "Unknown", "Max", "Status", "IdempotencyKey", "PendingReadbacks", "MaxPendingReadbacks"],
    jsonFields: ["attempted", "confirmed", "unknown", "max", "status", "idempotencyKey", "pendingReadbacks", "maxPendingReadbacks"],
    maxPerStage: 1,
    idempotentReplayEffectOnAttempt: "does_not_increment_attempted_or_max_and_reuses_the_exact_persisted_idempotency_key",
    reservationOrder: [
      "increment_attempted_and_set_idempotency_key", "persist_exact_result_cas", "invoke_stage_mutation"
    ],
    forbiddenLegacyFields: ["ChargeAttempted"]
  });
  assert.deepEqual(contract.stageDecision.freshTypedPendingContinuation, {
    authority: "services/control-plane",
    authorizationClass: "fresh_typed_pending_system",
    trigger: "same_CAS_after_first_stage_mutation_exact_owner_typed_pending",
    authorizationFields: [
      "schemaVersion", "authorizationId", "authorizationClass", "accountId", "operationId", "workspaceId", "stage",
      "idempotencyKey", "attempt", "operationVersion", "mutationBudget", "idempotentReplayBudget",
      "authoritativeReadBudget", "readbacksAtAuthorization", "status", "consumedAt"
    ],
    bindingFields: ["accountId", "operationId", "workspaceId", "stage", "idempotencyKey", "attempt", "operationVersion"],
    mutationBudget: 0,
    idempotentReplayBudget: 0,
    mandatoryPostMutationReadbacks: 1,
    readbacksAtAuthorization: 1,
    authoritativeReadBudget: 2,
    maximumPostMutationOwnerReadbacks: 3,
    readClaimFields: [
      "schemaVersion", "authorizationId", "stage", "idempotencyKey", "readback", "status", "leaseExpiresAt", "completedAt"
    ],
    readClaimCAS: "increment_and_persist_exact_result_before_owner_GET_single_winner",
    concurrentLoser: "stop_before_owner_GET",
    claimCrash: "claimed_slot_is_consumed_and_never_refunded_or_reissued",
    claimExpiry: "expire_consumed_slot_before_claiming_a_distinct_remaining_slot_without_reissuing_the_expired_readback",
    ready: "confirm_exact_attempt_consume_authorization_and_advance_same_operation",
    pending: "complete_claim_and_authorize_next_read_only_below_exact_budget",
    budgetExhaustion: "unknown_manual_review_without_absence_or_mutation",
    unknownConflictError: "fail_closed_without_new_mutation",
    legacyV3MissingAuthorizationAndClaimFields:
      "explicit_zero_system_authorization_and_zero_read_claim_compatibility_that_creates_no_external_fact_read_or_mutation",
    forbidden: ["operator_resume_impersonation", "attempt_or_max_increase", "second_mutation", "background_timer_or_poll", "unbounded_poll", "heuristic_ready"]
  });
  assert.deepEqual(contract.recovery, {
    authority: "services/control-plane",
    operation: "resume_original_workspace_launch_stage",
    route: "POST /api/operator/workspace-launches/{operationId}/resume",
    requestFields: ["launchVersion", "authorizedStage", "reason", "mutationBudget", "idempotentReplayBudget", "authoritativeReadBudget"],
    authorizationFields: [
      "authorizationId", "launchVersion", "authorizedStage", "authorizedBy", "authorizedAt", "reason", "mutationBudget",
      "idempotentReplayBudget", "authoritativeReadBudget", "readbacksAtAuthorization", "acceptanceBResumeExisting"
    ],
    authorizationId: "Idempotency-Key request header",
    authorizationIdFormat: "single_Idempotency-Key_matching_the_shared_compact_non_secret_opaque_id_predicate",
    authorizedBy: "control_plane_operator_session_user_id",
    authorizedAt: "control_plane_server_time_or_exact_authorization_replay",
    admission: "manual_review_with_exact_launch_version_current_stage_and_independent_mutation_replay_and_read_budgets",
    immutability: "same_authorizationId_requires_exact_authorization",
    idempotentReplayBudget: 1,
    authoritativeReadBudget: 3,
    readBudgetSemantics: "persisted_operator_authorization_for_owner_typed_continuation_reads_not_a_background_poll_or_provider_failure_heuristic",
    readbacksAtAuthorization: "server_bound_persisted_baseline_never_supplied_by_the_operator",
    pendingContinuation: "only_owner_typed_pending_evidence_may_consume_the_authorized_read_budget",
    budgetExhaustion: "unknown_manual_review_never_absent_and_never_automatic_replay",
    legacyV3MissingReplayAndReadFields: "explicit_zero_budget_compatibility_that_creates_no_external_fact_read_or_mutation",
    replayCAS: "persist_authorization_then_single_winner_claim_for_the_same_stage_and_exact_original_idempotency_key",
    secondLaunch: "forbidden",
    authorizationReadback: {
      route: "GET /api/operator/workspace-launches/{operationId}/resume-authorizations/{authorizationId}",
      lookup: "exact_current_or_consumed_authorization_id",
      fields: ["schemaVersion", "operationId", "operationVersion", "authorizationId", "authorizationVersion", "authorizedStage", "authorizedBy", "status", "consumedAt", "singleUse", "attempt", "convergence", "acceptanceBResumeExisting"],
      statuses: ["active", "consumed"],
      attemptFields: ["attempted", "confirmed", "unknown", "max", "status", "idempotencyKey", "pendingReadbacks", "maxPendingReadbacks"],
      convergenceFields: ["operationStatus", "stage", "version"],
      singleUse: true
    },
    acceptanceBResumeExisting: {
      operationMode: "acceptance_b_resume_existing",
      approvalSecretEnv: "OPL_PRODUCTION_BASIC_ACCEPTANCE_B_RESUME_EXISTING_APPROVAL_JSON",
      capabilityHeader: "x-opl-acceptance-b-capability",
      approvalIdHeader: "x-opl-acceptance-b-approval-id",
      requestContract: "same_exact_six_field_recovery_request",
      approvalSchema: {
        schemaVersion: 1,
        exactTopLevelFields: ["schemaVersion", "operationMode", "approvalId", "expiresAt", "release", "authorization", "reconciliation", "identityDigests"],
        releaseFields: ["canonicalCloudSha", "canonicalCloudTree", "deployedCloudImageDigest"],
        authorizationFields: ["authorizationId", "operationId", "launchVersion", "authorizedStage", "reasonSha256", "mutationBudget", "idempotentReplayBudget", "authoritativeReadBudget"],
        reconciliationFields: ["operationStatus", "authoritativeStageState", "attempt"],
        attemptFields: ["attempted", "confirmed", "unknown", "max", "status", "idempotencyKeySha256"],
        identityDigestFields: ["accountIdentitySha256", "operationIdentitySha256", "workspaceIdentitySha256", "keyIdentitySha256", "debitIdentitySha256", "quoteIdentitySha256", "providerIdentitySha256"]
      },
      releaseAuthority: {
        canonicalCloudSha: "control_plane_OPL_RELEASE_SHA_exact_match",
        canonicalCloudTree: "instance_approval_binding_not_control_plane_runtime_fact",
        deployedCloudImageDigest: "control_plane_OPL_CLOUD_IMAGE_exact_digest_match"
      },
      requestIntent: "either_dedicated_header_requires_exactly_one_approval_id_and_one_capability_header_server_secret_configuration_alone_never_selects_dedicated_mode",
      reasonBinding: "reasonSha256_equals_control_plane_canonical_null_delimited_sha256_of_the_exact_request_reason",
      admissionOrder: ["parse_exact_approval", "load_exact_persisted_operation", "bind_server_owned_operator_identity", "fresh_owner_stage_read", "validate_attempt_and_identity_digests", "persist_authorization_CAS"],
      persistedBindingFields: ["schemaVersion", "approvalId", "approvalSha256", "canonicalCloudSha", "canonicalCloudTree", "deployedCloudImageDigest", "authoritativeState", "identityDigests"],
      unknown: "fail_closed_before_authorization_persistence",
      exactReplay: "same_authorizationId_and_approval_digest_returns_persisted_single_use_readback_without_new_authority_budget"
    }
  });
});

test("Fabric launch binding freezes only the typed successor seam", async () => {
  const contract = await json("packages/contracts/opl-cloud-fabric-launch-binding-contract.json");

  assert.equal(contract.owner, "services/fabric");
  assert.deepEqual(contract.workspaceLaunchApi, {
    preflightRoute: "POST /fabric/workspace-launches/preflight",
    stageReadRoute: "POST /fabric/workspace-launches/stages/read",
    stageEnsureRoute: "POST /fabric/workspace-launches/stages/ensure",
    bindingType: "WorkspaceLaunchStageBinding",
    ensureIdempotencyHeader: "Idempotency-Key equals binding.idempotencyKey"
  });
  assert.deepEqual(contract.preflight, {
    mode: "read_only_admission",
    configuredProfileSelectionOwner: "instance_repository",
    implementationScope: "opl-instance-medopl is one concrete Tencent profile instance, not the unique owner of the public Cloud contract.",
    admittedBindingReadbackOwner: "services/fabric",
    requestFields: [
      "schemaVersion", "launchOperationId", "accountId", "workspaceId", "packageId", "sizeGb",
      "workspaceImageDigest", "requestHash"
    ],
    forbiddenRequestFields: ["resources"],
    responseIdentityFields: ["schemaVersion", "launchOperationId", "requestHash", "providerProfileRef", "bindingRef"]
  });
  assert.deepEqual(contract.stageInput, {
    fields: [
      "binding", "providerProfileRef", "preflightBindingRef", "packageId", "sizeGb",
      "workspaceImageDigest", "resources", "gatewayCredential"
    ],
    forbiddenFields: ["resumeAuthorizationDigest", "mutationBudget"],
    gatewayCredential: "optional_secret_stage_transport_only_never_hashed_or_persisted"
  });
  assert.deepEqual(contract.launchBinding.fields, [
    "schemaVersion", "launchOperationId", "accountId", "workspaceId", "stage", "action",
    "fabricOperationId", "idempotencyKey", "requestHash", "expectedResourceBinding"
  ]);
  assert.equal(
    contract.launchBinding.writeProtocol,
    "control_plane_submits_complete_binding_and_fabric_persists_it_before_provider_write"
  );
  assert.equal(
    contract.launchBinding.readProtocol,
    "lookup_by_explicit_fabricOperationId_and_require_exact_binding_match"
  );
  assert.equal(contract.launchBinding.stageSemantics, "control_plane_durable_cursor");
  assert.equal(contract.launchBinding.actionSemantics, "fabric_mutation_command");
  assert.deepEqual(contract.stageRequestHash.payloadFields, [
    "launchRequestHash", "action", "packageId", "sizeGb", "imageDigest", "resources"
  ]);
  assert.deepEqual(contract.stageRequestHash.jsonEncoding.payloadFieldOrder, contract.stageRequestHash.payloadFields);
  assert.equal(contract.stageRequestHash.algorithm, "sha256");
  assert.equal(contract.stageRequestHash.digestEncoding, "lowercase_hex");
  assert.equal(contract.stageRequestHash.bindingProjection, "action_only");
  assert.deepEqual(contract.stageRequestHash.excludedBindingFields, [
    "schemaVersion", "launchOperationId", "accountId", "workspaceId", "stage", "fabricOperationId",
    "idempotencyKey", "requestHash", "expectedResourceBinding"
  ]);
  assert.deepEqual(contract.stageRequestHash.excludedStageInputFields, [
    "providerProfileRef", "preflightBindingRef", "gatewayCredential"
  ]);
  assert.deepEqual(contract.stageRequestHash.consumerModules, ["services/control-plane", "services/fabric"]);
  assert.equal(
    contract.stageRequestHash.testFixturePolicy,
    "read_this_goldenVectors_array_without_a_copied_caller_hash_helper"
  );
  assert.deepEqual(contract.readback.matchFields, contract.launchBinding.fields);
  assert.deepEqual(contract.readback.forbiddenInference, [
    "idempotency_suffix", "unscoped_operation_list", "provider_tag", "provider_resource_name"
  ]);
  assert.deepEqual(contract.readback.typedStateReasonMatrix, {
    ready: ["none"],
    pending: ["provider_provisioning"],
    absent: ["no_stage_record", "started_no_resource", "failed_no_resource"],
    unknown: ["failed_no_resource_unproven", "resource_absence_status_conflict"]
  });
  assert.deepEqual(contract.readback.adapterReplayPolicy.order, [
    "owner_authoritative_read", "durable_same_operation_child_replay_cas", "second_owner_authoritative_read",
    "exact_original_idempotency_transport_replay_only_if_still_absent"
  ]);
  assert.match(contract.readback.adapterReplayPolicy.childTransportLease, /fabric_local_replay_epoch/);
  assert.match(contract.readback.adapterReplayPolicy.childTransportLease, /not_control_plane_authorization_or_a_business_attempt_budget/);

  const expectedStages = [
    ["ensure_compute_allocation", "ensure_compute_allocation"],
    ["storage", "ensure_storage"],
    ["attachment", "ensure_attachment"],
    ["secret", "ensure_gateway_secret"],
    ["runtime", "ensure_runtime"]
  ];
  assert.deepEqual(
    contract.stageOperations.map((stage: Record<string, string>) => [stage.stage, stage.action]),
    expectedStages
  );
  assert.equal(contract.stageRequestHash.goldenVectors.length, expectedStages.length);
  for (const [index, vector] of contract.stageRequestHash.goldenVectors.entries()) {
    assert.deepEqual([vector.stage, vector.payload.action], expectedStages[index]);
    assert.deepEqual(Object.keys(vector.payload), contract.stageRequestHash.payloadFields);
    const resourceOrder = Object.keys(vector.payload.resources).map((field) =>
      contract.stageRequestHash.jsonEncoding.resourceFieldOrder.indexOf(field)
    );
    assert.equal(resourceOrder.every((position) => position >= 0), true, `${vector.stage}:resource fields`);
    assert.deepEqual(resourceOrder, [...resourceOrder].sort((left, right) => left - right), `${vector.stage}:resource order`);
    const digest = createHash("sha256").update(JSON.stringify(vector.payload), "utf8").digest("hex");
    assert.equal(digest, vector.sha256, vector.stage);
    const serialized = JSON.stringify(vector.payload);
    for (const excluded of [
      ...contract.stageRequestHash.excludedBindingFields,
      ...contract.stageRequestHash.excludedStageInputFields
    ]) assert.equal(serialized.includes(`\"${excluded}\"`), false, `${vector.stage}:${excluded}`);
  }
  assert.equal(contract.notOwned.includes("preflight_binding_truth"), false);

  const serialized = JSON.stringify(contract);
  for (const legacy of [
    "stageReadbackApi", "workspace-launch-stage-readback", "proofRoute", "convergeRoute",
    "fabricRecordId", "sub2apiMutationCount", "tencentMutationCount", "kubernetesMutationCount",
    "fabricOperationMutationCount", "idempotencyIdentity", "<launchOperationId>"
  ]) {
    assert.equal(serialized.includes(legacy), false, legacy);
  }
});

test("service boundary delegates launch truth to focused owners and typed routes", async () => {
  const [boundary, fabric] = await Promise.all([
    json("packages/contracts/opl-cloud-service-boundary-contract.json"),
    json("packages/contracts/opl-cloud-fabric-launch-binding-contract.json")
  ]);

  assert.equal(boundary.services.controlPlane.workspaceLaunchContract, "opl-cloud-control-plane-launch-contract.json");
  assert.equal(boundary.services.fabric.workspaceLaunch.contract, "opl-cloud-fabric-launch-binding-contract.json");
  assert.deepEqual(boundary.services.fabric.workspaceLaunch.routes, [
    fabric.workspaceLaunchApi.preflightRoute,
    fabric.workspaceLaunchApi.stageReadRoute,
    fabric.workspaceLaunchApi.stageEnsureRoute
  ]);

  const serialized = JSON.stringify(boundary);
  for (const legacy of [
    "workspace-launch-stage-readback", "proofRoute", "convergeRoute", "fabricConvergenceRoute",
    "workspaceLaunchManualReviewProviderTruth", "workspaceComputeClaimRecovery", "computeProcurement",
    "computePoolHeadTerminalization", "normalWorkspaceLaunch"
  ]) {
    assert.equal(serialized.includes(legacy), false, legacy);
  }
});
