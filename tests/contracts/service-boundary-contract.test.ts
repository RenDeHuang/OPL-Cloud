import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("Fabric mutations use a scoped capability distinct from transport identity", async () => {
  const contract = JSON.parse(await readFile("packages/contracts/opl-cloud-service-boundary-contract.json", "utf8"));
  const authorization = contract.physicalBoundaries.fabricMutationAuthorization;

  assert.equal(authorization.defaultIssuer, "services/control-plane");
  assert.deepEqual(authorization.operatorExceptions, [
    {
      issuer: "opl-instance-medopl_protected_production_workflow",
      route: "POST /fabric/compute-pool-head/terminalization",
      caller: "operator",
      action: "terminalize_compute_pool_head",
      scopeSource: "fabric_persisted_candidate_or_exact_terminal_replay",
      deploymentRequiredForUse: true
    }
  ]);
  assert.equal(authorization.verifier, "services/fabric");
  assert.equal(authorization.integrity, "hmac_sha256");
  assert.equal(authorization.shortLived, true);
  assert.deepEqual(authorization.boundFields, [
    "caller",
    "accountId",
    "workspaceId",
    "resourceKind",
    "resourceId",
    "action",
    "operationId",
    "expiresAt",
    "bodySha256"
  ]);
  assert.equal(authorization.verificationOrder, "before_operation_store_or_provider_mutation");
  assert.equal(authorization.transportCredentialReuse, "forbidden");
  assert.equal(authorization.runnerScope, "job_read_and_lease_mutations_only");
});

test("credential and Ledger reads verify capability before sensitive owner lookup", async () => {
  const contract = JSON.parse(await readFile("packages/contracts/opl-cloud-service-boundary-contract.json", "utf8"));
  const fabric = contract.physicalBoundaries.fabricCredentialReadAuthorization;
  const ledger = contract.physicalBoundaries.ledgerCapabilityAuthorization;

  assert.equal(fabric.ordinaryStatusPassword, "always_redacted");
  assert.equal(fabric.issuer, "services/control-plane_after_workspace_owner_check");
  assert.equal(fabric.ownerScopeSource, "fabric_persisted_workspace_runtime_operation");
  assert.equal(fabric.transportTokenOnly, "forbidden");
  assert.equal(ledger.preverification, "signature_caller_resource_action_operation_expiry_and_body_digest_before_owner_lookup");
  assert.equal(ledger.finalVerification, "persisted_owner_account_and_workspace_match");
  assert.deepEqual(ledger.indexedOwnerLookups, ["receipt_id", "artifact_id", "review_id", "review_policy_id"]);
  assert.equal(ledger.reviewGateLookup, "bounded_indexed_review_id_set");
  assert.equal(ledger.transportTokenOnly, "forbidden");
});
