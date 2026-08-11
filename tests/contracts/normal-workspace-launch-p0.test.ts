import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);

async function contract(path: string) {
  return JSON.parse(await readFile(new URL(path, root), "utf8"));
}

test("normal Workspace launch freezes one POST safety authorities and restart budgets", async () => {
  const [freeze, boundary] = await Promise.all([
    contract("packages/contracts/opl-cloud-launch-freeze-contract.json"),
    contract("packages/contracts/opl-cloud-service-boundary-contract.json")
  ]);

  assert.deepEqual(freeze.workspaceLaunch.normalLaunchSafety, {
    entry: "one_authenticated_POST_/api/workspace-launches",
    sharedOrchestrator: "workspace.launch.v2_from_debit_through_receipt_and_url",
    packages: ["basic", "pro"],
    keyPendingReplay: {
      exactIdentity: ["accountId", "ownerUserId", "packageId", "requestHash"],
      sameIdentity: "resume_original_launch_and_reserved_workspace_key",
      originalIdempotencyKey: "preserved",
      drift: "409_idempotency_conflict",
      secondLaunchOrKey: 0,
      rawCredentialPersistence: false
    },
    workspaceImagePreflight: {
      required: "immutable_repository_at_sha256_digest",
      timing: "before_launch_persistence_debit_or_any_provider_write",
      invalidOrDrift: "fail_closed",
      mutationCounts: { database: 0, sub2api: 0, tencent: 0, kubernetes: 0 }
    },
    debitConfirmation: {
      authority: "unique_matching_Sub2API_Redeem_Code_history",
      balanceSnapshots: "preflight_and_projection_only",
      concurrentUsage: "allowed_without_exact_balance_delta",
      monthlyDebitMaximum: 1
    },
    billingPeriod: {
      freezeAt: "first_authoritative_debit_confirmation",
      fields: ["periodStart", "paidThrough", "billingAnchorDay"],
      replay: "reuse_persisted_values_never_recalculate"
    }
  });

  assert.deepEqual(boundary.services.controlPlane.normalWorkspaceLaunch, {
    entryPostMaximum: 1,
    keyPendingReplay: "same_owner_package_and_request_hash_resumes_original_launch_with_original_idempotency_key",
    keyPendingDrift: "409_before_new_launch_or_key",
    chargeAuthority: "one_exact_redeem_history_entry",
    concurrentUsageBalanceDeltaRequired: false,
    periodFreeze: "first_authoritative_charge_confirmation_persisted_once",
    providerMutationBeforeValidWorkspaceDigest: 0
  });

  assert.deepEqual(boundary.services.fabric.normalWorkspaceLaunch, {
    computeStages: ["compute_create", "compute_claim_cvm"],
    nodeClaimOwner: "control_plane_persisted_decision_authorized_compute_stage_executor",
    storageStages: ["cbs_create", "static_binding_apply"],
    reservation: "fabric_postgresql_cas_before_each_external_write",
    restartAfterReservedOrUnknown: "authoritative_describe_get_only",
    externalWriteMaximumPerStage: 1,
    unknown: "manual_review_without_replay",
    storageIdentityConfirmed: "CreateDisks_permanently_zero_then_original_PV_PVC_apply_or_readback",
    activePaidPendingStorage: "converge_original_identity_or_manual_review",
    forbiddenPendingStorageActions: ["delete_pv", "delete_pvc", "retained", "replacement_cbs"]
  });

  assert.equal(
    freeze.workspaceLaunch.computeClaimAutomaticContinuation.fabricCreateBoundary,
    "compute_create_and_compute_claim_cvm_only_then_claim_pending_without_node_patch"
  );
  assert.equal(
    freeze.workspaceLaunch.computeClaimAutomaticContinuation.decisionAuthority,
    "control_plane_phase_status_currentDecision_atomic_cas_then_exact_decision_readback"
  );
  assert.equal(
    boundary.services.controlPlane.workspaceComputeClaimAutomaticContinuation.authorization,
    "persisted_and_read_back_currentDecision_plus_internal_service_capability_without_operator_approval"
  );
  assert.deepEqual(boundary.services.controlPlane.workspaceComputeClaimAutomaticContinuation.allowedWrites, {
    tencent: 0,
    kubernetesNodePatchMax: 1
  });
});
