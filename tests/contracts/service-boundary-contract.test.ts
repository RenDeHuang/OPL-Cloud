import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("Fabric mutations use a scoped capability distinct from transport identity", async () => {
  const contract = JSON.parse(await readFile("packages/contracts/opl-cloud-service-boundary-contract.json", "utf8"));
  const authorization = contract.physicalBoundaries.fabricMutationAuthorization;

  assert.equal(authorization.issuer, "services/control-plane");
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
