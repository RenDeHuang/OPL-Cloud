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
      "resumeAuthorization", "resumeAuthorizationConsumedAt", "idempotentReplayClaims"
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
    goFields: ["Attempted", "Confirmed", "Unknown", "Max", "IdempotencyKey", "PendingReadbacks", "MaxPendingReadbacks"],
    jsonFields: ["attempted", "confirmed", "unknown", "max", "idempotencyKey", "pendingReadbacks", "maxPendingReadbacks"],
    maxPerStage: 1,
    idempotentReplayEffectOnAttempt: "does_not_increment_attempted_or_max_and_reuses_the_exact_persisted_idempotency_key",
    reservationOrder: [
      "increment_attempted_and_set_idempotency_key", "persist_exact_result_cas", "invoke_stage_mutation"
    ],
    forbiddenLegacyFields: ["ChargeAttempted"]
  });
  assert.deepEqual(contract.recovery, {
    authority: "services/control-plane",
    operation: "resume_original_workspace_launch_stage",
    route: "POST /api/operator/workspace-launches/{operationId}/resume",
    requestFields: ["launchVersion", "authorizedStage", "reason", "mutationBudget", "idempotentReplayBudget", "authoritativeReadBudget"],
    authorizationFields: [
      "authorizationId", "launchVersion", "authorizedStage", "authorizedBy", "authorizedAt", "reason", "mutationBudget",
      "idempotentReplayBudget", "authoritativeReadBudget", "readbacksAtAuthorization"
    ],
    authorizationId: "Idempotency-Key request header",
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
    secondLaunch: "forbidden"
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
  assert.match(contract.readback.adapterReplayPolicy.childTransportLease, /one_logical_replay_authorization/);

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
