import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);
const text = (path: string) => readFile(new URL(path, root), "utf8");
const json = async (path: string) => JSON.parse(await text(path));

function collectKeys(value: unknown, keys: string[] = []): string[] {
  if (!value || typeof value !== "object") return keys;
  if (Array.isArray(value)) {
    for (const item of value) collectKeys(item, keys);
    return keys;
  }
  for (const [key, child] of Object.entries(value)) {
    keys.push(key);
    collectKeys(child, keys);
  }
  return keys;
}

test("launch freeze is a bounded migration contract without status ledgers", async () => {
  const freeze = await json("packages/contracts/opl-cloud-launch-freeze-contract.json");

  assert.equal(freeze.schemaVersion, 32);
  assert.equal(freeze.state, "migration");
  assert.equal(freeze.lifecycle.type, "migration_guard");
  assert.deepEqual(Object.keys(freeze), [
    "schemaVersion", "owner", "purpose", "state", "machineBoundary", "lifecycle",
    "monthlySettlement", "workspaceLaunch", "providerProcurement"
  ]);

  const keys = new Set(collectKeys(freeze));
  for (const forbidden of [
    "deliveryEvidence", "launchStages", "currentImplementation", "currentState",
    "currentBranchScope", "productionEvidence", "releaseEvidence"
  ]) assert.equal(keys.has(forbidden), false, forbidden);
});

test("monthly settlement keeps one debit and fail-closed unknown-result bounds", async () => {
  const freeze = await json("packages/contracts/opl-cloud-launch-freeze-contract.json");
  const settlement = freeze.monthlySettlement;

  assert.equal(settlement.balanceOwner, "Sub2API");
  assert.deepEqual(settlement.protocol, ["debit", "fabric_fulfillment", "claim", "activate", "record_workspace_receipt"]);
  assert.equal(settlement.doubleDebitForbidden, true);
  assert.equal(settlement.confirmedNoResourceAfterDebit, "idempotent_refund");
  assert.equal(settlement.partialOrUnknownProviderResult, "manual_review_without_refund");
  assert.equal(settlement.chargeConfirmationEvidence.monthlyDebitMaximum, 1);
  assert.equal(settlement.chargeConfirmationEvidence.balanceDeltaMismatchAlone, "not_manual_review");
  assert.equal(settlement.ledgerFailureAfterActivation, "retry_receipt_only");
});

test("Workspace launch preserves debit-first ordering and deterministic replay", async () => {
  const freeze = await json("packages/contracts/opl-cloud-launch-freeze-contract.json");
  const launch = freeze.workspaceLaunch;

  assert.equal(launch.customerDebitCardinality, 1);
  assert.deepEqual(launch.submissionOrder, [
    "workspace_quote", "compute_read_only_preflight", "storage_read_only_preflight",
    "workspace_key_preflight", "sub2api_total_balance_preflight", "persist_launch"
  ]);
  assert.equal(launch.unavailablePackageBehavior, "package_unavailable_before_gateway_balance_debit_ledger_or_tencent_calls");
  assert.equal(launch.totalBalanceSemantics, "read_only_preflight_not_hold_or_reservation");
  assert.equal(launch.persistence, "control_plane_runtime_operations with action=workspace.launch.v2 and result.schemaVersion=2");
  assert.equal(launch.providerPreflightRecovery.writes, "none");
  assert.equal(launch.continuationAttemptBudgets.maxPerStage, 1);
  assert.equal(launch.continuationAttemptBudgets.restart, "remaining_budget_loaded_from_persisted_launch");
});

test("manual review recovery cannot become a second launch or second purchase", async () => {
  const freeze = await json("packages/contracts/opl-cloud-launch-freeze-contract.json");
  const recovery = freeze.workspaceLaunch.manualReviewRecovery;

  assert.equal(recovery.operatorAuthorization, "authenticated_reserved_operator_session_plus_csrf");
  assert.equal(recovery.resourceIdentityInput, "forbidden_server_authoritative_readback_only");
  assert.deepEqual(recovery.requestFields, ["planId", "planDigest", "decision", "confirmation"]);
  assert.equal(recovery.allowedAction, "continue_original_workspace_launch");
  assert.equal(recovery.matrix.providerUnknown, "remain_manual_review");
  assert.equal(recovery.matrix.computeAbsentStorageAbsent, "one_idempotent_workspace_refund");
});

test("provider procurement remains prepaid, gated and protected", async () => {
  const freeze = await json("packages/contracts/opl-cloud-launch-freeze-contract.json");
  const provider = freeze.providerProcurement;

  assert.equal(provider.chargeType, "PREPAID");
  assert.equal(provider.periodMonths, 1);
  assert.equal(provider.renewFlag, "NOTIFY_AND_MANUAL_RENEW");
  assert.deepEqual(provider.forbiddenChargeTypes, ["POSTPAID_BY_HOUR"]);
  assert.deepEqual(provider.mutationPermissionGate, {
    env: "RUN_TENCENT_CREATE_RELEASE_EXECUTION",
    requiredValue: "1",
    check: "shared_tencent_monthly_preflight_before_sub2api_debit",
    failure: "zero_charge_zero_fabric_mutation"
  });
  assert.deepEqual(provider.protectedResourceGuard.appliesTo, [
    "tencent_mutation", "kubernetes_mutation", "cleanup_workflows"
  ]);
  assert.equal(provider.protectedResourceGuard.failure, "reject_before_provider_client_or_kubectl_mutation");
  assert.deepEqual(provider.activationReadback.mutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
  assert.equal(provider.activationReadback.mismatch, "manual_review_without_activation");
  assert.equal(provider.unpaidExpiry.fabricMutationCount, 0);
  assert.equal(provider.unpaidExpiry.tencentMutationCount, 0);
});

test("runtime implementation follows the remaining side-effect bounds", async () => {
  const [launchSource, fabricSource] = await Promise.all([
    text("services/control-plane/internal/server/workspace_launch.go"),
    text("services/fabric/internal/fabric/service.go")
  ]);

  assert.match(launchSource, /ChargeAttempted/);
  assert.match(launchSource, /manual_review/);
  assert.match(launchSource, /IdempotencyKey/);
  assert.doesNotMatch(fabricSource, /POSTPAID_BY_HOUR/);
});
