import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);

async function json(path: string) {
  return JSON.parse(await readFile(new URL(path, root), "utf8"));
}

test("current launch and settlement facts have four focused owners", async () => {
  const [billing, controlPlane, fabric, ledger, boundary] = await Promise.all([
    json("packages/contracts/opl-cloud-billing-ledger-contract.json"),
    json("packages/contracts/opl-cloud-control-plane-launch-contract.json"),
    json("packages/contracts/opl-cloud-fabric-launch-binding-contract.json"),
    json("packages/contracts/opl-cloud-evidence-ledger-contract.json"),
    json("packages/contracts/opl-cloud-service-boundary-contract.json")
  ]);

  assert.deepEqual(boundary.focusedOwners, {
    settlement: "opl-cloud-billing-ledger-contract.json",
    controlPlaneLaunchRecovery: "opl-cloud-control-plane-launch-contract.json",
    fabricLaunchBinding: "opl-cloud-fabric-launch-binding-contract.json",
    ledgerReceiptEvidence: "opl-cloud-evidence-ledger-contract.json"
  });
  assert.equal(billing.owner, "OPL Control Plane");
  assert.equal(controlPlane.owner, "services/control-plane");
  assert.equal(fabric.owner, "services/fabric");
  assert.equal(ledger.owner, "OPL Ledger");
  assert.equal(
    billing.workspaceLaunchFulfillment.manualReviewRecoveryContract,
    "opl-cloud-control-plane-launch-contract.json#recovery"
  );
  assert.equal(billing.workspaceLaunchFulfillment.manualReviewRecoveryOwnerContract, undefined);

  const files = await readdir(new URL("packages/contracts/", root));
  assert.equal(files.includes("opl-cloud-launch-freeze-contract.json"), false);
});

test("Control Plane durable launch chain keeps preflight outside mutation stages", async () => {
  const contract = await json("packages/contracts/opl-cloud-control-plane-launch-contract.json");

  assert.equal(contract.schemaVersion, 3);
  assert.equal(contract.launchOperation.resultSchemaVersion, 3);
  assert.deepEqual(contract.launchOperation.identityFields, [
    "launchOperationId", "accountId", "ownerUserId", "workspaceId", "requestHash"
  ]);
  assert.deepEqual(contract.stageDecision.preflightAdmission, {
    timing: "before_first_external_write",
    mode: "read_only",
    durableStage: false
  });
  assert.deepEqual(contract.stageDecision.orderedStages, [
    "key",
    "debit",
    "ensure_compute_allocation",
    "storage",
    "attachment",
    "secret",
    "runtime",
    "activation",
    "receipt",
    "succeeded"
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
  assert.deepEqual(contract.stageDecision.attemptPersistence.goFields, [
    "Attempted", "Confirmed", "Unknown", "Max", "Status", "IdempotencyKey", "PendingReadbacks", "MaxPendingReadbacks"
  ]);
  assert.equal(contract.stageDecision.attemptPersistence.maxPerStage, 1);
  assert.deepEqual(contract.stageDecision.attemptPersistence.forbiddenLegacyFields, ["ChargeAttempted"]);
  assert.equal(contract.stageDecision.fabricOperationBinding, "opl-cloud-fabric-launch-binding-contract.json");
  assert.equal(contract.recovery.route, "POST /api/operator/workspace-launches/{operationId}/resume");
  assert.deepEqual(contract.recovery.requestFields, [
    "launchVersion", "authorizedStage", "reason", "mutationBudget", "idempotentReplayBudget", "authoritativeReadBudget"
  ]);
  assert.equal(contract.recovery.authorizationId, "Idempotency-Key request header");
  assert.equal(contract.recovery.authorizationIdFormat,
    "single_Idempotency-Key_matching_the_shared_compact_non_secret_opaque_id_predicate");
  assert.equal(contract.recovery.authorizedBy, "control_plane_operator_session_user_id");
  assert.equal(contract.recovery.authorizedAt, "control_plane_server_time_or_exact_authorization_replay");
  assert.equal(contract.recovery.idempotentReplayBudget, 1);
  assert.equal(contract.recovery.authoritativeReadBudget, 3);
  assert.match(contract.recovery.readBudgetSemantics, /operator_authorization/);
  assert.equal(contract.recovery.budgetExhaustion, "unknown_manual_review_never_absent_and_never_automatic_replay");
  assert.match(contract.recovery.legacyV3MissingReplayAndReadFields, /zero_budget/);
  assert.equal(contract.recovery.authorizationReadback.route,
    "GET /api/operator/workspace-launches/{operationId}/resume-authorizations/{authorizationId}");
  assert.deepEqual(contract.recovery.authorizationReadback.statuses, ["active", "consumed"]);
  assert.deepEqual(contract.recovery.authorizationReadback.attemptFields,
    ["attempted", "confirmed", "unknown", "max", "status", "idempotencyKey", "pendingReadbacks", "maxPendingReadbacks"]);
  assert.equal(contract.recovery.acceptanceBResumeExisting.operationMode, "acceptance_b_resume_existing");
  assert.deepEqual(contract.recovery.acceptanceBResumeExisting.approvalSchema.identityDigestFields, [
    "accountIdentitySha256", "operationIdentitySha256", "workspaceIdentitySha256", "keyIdentitySha256",
    "debitIdentitySha256", "quoteIdentitySha256", "providerIdentitySha256"
  ]);
  assert.equal(contract.recovery.acceptanceBResumeExisting.releaseAuthority.canonicalCloudTree, "instance_approval_binding_not_control_plane_runtime_fact");
});

test("Fabric uses explicit immutable launch-stage binding and typed routes", async () => {
  const [contract, boundary] = await Promise.all([
    json("packages/contracts/opl-cloud-fabric-launch-binding-contract.json"),
    json("packages/contracts/opl-cloud-service-boundary-contract.json")
  ]);

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
  assert.deepEqual(contract.stageInput.fields, [
    "binding", "providerProfileRef", "preflightBindingRef", "packageId", "sizeGb",
    "workspaceImageDigest", "resources", "gatewayCredential"
  ]);
  assert.deepEqual(contract.stageInput.forbiddenFields, ["resumeAuthorizationDigest", "mutationBudget"]);
  assert.deepEqual(contract.launchBinding.fields, [
    "schemaVersion",
    "launchOperationId",
    "accountId",
    "workspaceId",
    "stage",
    "action",
    "fabricOperationId",
    "idempotencyKey",
    "requestHash",
    "expectedResourceBinding"
  ]);
  assert.equal(contract.launchBinding.immutability, "all_fields_are_immutable_for_one_fabricOperationId");
  assert.equal(contract.launchBinding.readProtocol, "lookup_by_explicit_fabricOperationId_and_require_exact_binding_match");
  assert.deepEqual(contract.stageOperations, [
    { stage: "ensure_compute_allocation", action: "ensure_compute_allocation" },
    { stage: "storage", action: "ensure_storage" },
    { stage: "attachment", action: "ensure_attachment" },
    { stage: "secret", action: "ensure_gateway_secret" },
    { stage: "runtime", action: "ensure_runtime" }
  ]);
  assert.equal(contract.launchBinding.stageSemantics, "control_plane_durable_cursor");
  assert.equal(contract.launchBinding.actionSemantics, "fabric_mutation_command");
  assert.deepEqual(contract.stageRequestHash.payloadFields, [
    "launchRequestHash", "action", "packageId", "sizeGb", "imageDigest", "resources"
  ]);
  assert.deepEqual(contract.stageRequestHash.excludedBindingFields, [
    "schemaVersion", "launchOperationId", "accountId", "workspaceId", "stage", "fabricOperationId",
    "idempotencyKey", "requestHash", "expectedResourceBinding"
  ]);
  assert.deepEqual(contract.stageRequestHash.excludedStageInputFields, [
    "providerProfileRef", "preflightBindingRef", "gatewayCredential"
  ]);
  assert.deepEqual(contract.stageRequestHash.consumerModules, ["services/control-plane", "services/fabric"]);
  assert.equal(contract.stageRequestHash.goldenVectors.length, 5);
  assert.equal(contract.notOwned.includes("preflight_binding_truth"), false);
  assert.deepEqual(contract.readback.matchFields, contract.launchBinding.fields);
  assert.deepEqual(contract.readback.forbiddenInference, [
    "idempotency_suffix", "unscoped_operation_list", "provider_tag", "provider_resource_name"
  ]);
  assert.deepEqual(boundary.services.fabric.workspaceLaunch.routes, [
    contract.workspaceLaunchApi.preflightRoute,
    contract.workspaceLaunchApi.stageReadRoute,
    contract.workspaceLaunchApi.stageEnsureRoute
  ]);

  const boundaryText = JSON.stringify(boundary);
  for (const legacy of [
    "workspace-launch-stage-readback",
    "proofRoute",
    "convergeRoute",
    "fabricConvergenceRoute",
    "workspaceLaunchManualReviewProviderTruth",
    "workspaceComputeClaimRecovery",
    "computeProcurement",
    "computePoolHeadTerminalization",
    "normalWorkspaceLaunch"
  ]) {
    assert.equal(boundaryText.includes(legacy), false, legacy);
  }
});
