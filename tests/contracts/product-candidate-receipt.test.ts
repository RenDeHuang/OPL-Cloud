import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const contractPath = "packages/contracts/opl-cloud-candidate-receipt-contract.json";

test("Cloud owns one neutral non-Release candidate receipt contract", async () => {
  const source = await readFile(contractPath, "utf8");
  const contract = JSON.parse(source);

  assert.deepEqual(Object.keys(contract).sort(), [
    "candidateReceiptV1",
    "lifecycle",
    "machineBoundary",
    "owner",
    "purpose",
    "schemaVersion",
    "state"
  ]);
  assert.equal(contract.schemaVersion, 1);
  assert.equal(contract.owner, "OPL Cloud");
  assert.equal(contract.state, "current");
  assert.equal(contract.lifecycle.type, "long_term_contract");

  const receipt = contract.candidateReceiptV1;
  assert.equal(receipt.kind, "opl_cloud_candidate");
  assert.equal(receipt.productRepository, "gaofeng21cn/one-person-lab-cloud");
  assert.equal(receipt.cloudImageRepository, "ghcr.io/gaofeng21cn/one-person-lab-cloud");
  assert.equal(receipt.platform, "linux/amd64");
  assert.deepEqual(receipt.exactKeys, [
    "schemaVersion",
    "kind",
    "product",
    "platform",
    "cloudImage",
    "workspaceImage",
    "provenance"
  ]);
  assert.deepEqual(receipt.productExactKeys, ["repository", "sha", "tree"]);
  assert.deepEqual(receipt.cloudImageExactKeys, ["repository", "ref", "digest", "revision"]);
  assert.deepEqual(receipt.workspaceImageExactKeys, ["ref", "digest"]);
  assert.deepEqual(receipt.provenanceExactKeys, [
    "workflowRepository",
    "workflowSha",
    "workflowRunId",
    "workflowRunAttempt"
  ]);
  assert.equal(receipt.receiptDigest, "sha256_of_canonical_json_bytes");
  assert.equal(receipt.workspaceRegistryReadbackOwner, "instance_or_installer");

  for (const forbidden of [
    "J2", "J4", "J5", "build_source", "reconcile", "registryMutationAttempts",
    "medopl.cn", "tencentyun.com", "accountId", "operationId", "providerId",
    "kubeconfig", "password", "secret", "releaseTag"
  ]) {
    assert.doesNotMatch(source, new RegExp(forbidden, "i"));
  }
});

test("distribution contract exposes candidate handoff without changing release ownership", async () => {
  const contract = JSON.parse(await readFile("packages/contracts/opl-cloud-distribution-contract.json", "utf8"));
  assert.deepEqual(contract.candidate, {
    workflow: ".github/workflows/build-opl-cloud-candidate.yml",
    contract: "packages/contracts/opl-cloud-candidate-receipt-contract.json#candidateReceiptV1",
    registry: "ghcr.io/gaofeng21cn/one-person-lab-cloud",
    platform: "linux/amd64",
    formalPublication: false,
    instanceDeployment: false
  });
  assert.deepEqual(contract.instanceHandoff.inputs, ["candidate_receipt_b64"]);
  assert.equal(
    contract.instanceHandoff.contract,
    "packages/contracts/opl-cloud-candidate-receipt-contract.json#candidateReceiptV1"
  );
  assert.equal(contract.distribution.workflow, ".github/workflows/release-opl-cloud-image.yml");
});
