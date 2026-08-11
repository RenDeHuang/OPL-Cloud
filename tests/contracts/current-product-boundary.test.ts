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
    "ensure_storage",
    "ensure_attachment",
    "ensure_gateway_secret",
    "ensure_runtime",
    "activation",
    "receipt",
    "succeeded"
  ]);
  assert.equal(contract.stageDecision.fabricOperationBinding, "opl-cloud-fabric-launch-binding-contract.json");
  assert.equal(contract.recovery.operation, "continue_original_workspace_launch");
  assert.equal(contract.recovery.resourceIdentityInput, "forbidden_server_authoritative_readback_only");
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
    { stage: "ensure_storage", action: "ensure_storage" },
    { stage: "ensure_attachment", action: "ensure_attachment" },
    { stage: "ensure_gateway_secret", action: "ensure_gateway_secret" },
    { stage: "ensure_runtime", action: "ensure_runtime" }
  ]);
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
