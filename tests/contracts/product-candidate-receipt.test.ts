import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const contractPath = "packages/contracts/opl-cloud-candidate-receipt-contract.json";

test("Cloud owns one neutral non-Release candidate receipt contract", async () => {
  const source = await readFile(contractPath, "utf8");
  const contract = JSON.parse(source);

  assert.deepEqual(Object.keys(contract).sort(), [
    "candidateReceiptV2",
    "lifecycle",
    "machineBoundary",
    "owner",
    "purpose",
    "schemaVersion",
    "state"
  ]);
  assert.equal(contract.schemaVersion, 2);
  assert.equal(contract.owner, "OPL Cloud");
  assert.equal(contract.state, "current");
  assert.equal(contract.lifecycle.type, "long_term_contract");

  const receipt = contract.candidateReceiptV2;
  assert.equal(receipt.schemaVersion, 2);
  assert.equal(receipt.kind, "opl_cloud_candidate");
  assert.equal(receipt.productRepository, "gaofeng21cn/one-person-lab-cloud");
  assert.equal(receipt.cloudImageRepository, "ghcr.io/gaofeng21cn/one-person-lab-cloud");
  assert.deepEqual(receipt.platforms, ["linux/amd64", "linux/arm64"]);
  assert.deepEqual(receipt.exactKeys, [
    "schemaVersion",
    "kind",
    "product",
    "cloudImage",
    "assets",
    "provenance"
  ]);
  assert.deepEqual(receipt.productExactKeys, ["repository", "sha", "tree"]);
  assert.deepEqual(receipt.cloudImageExactKeys, ["repository", "indexRef", "indexDigest", "revision", "platforms"]);
  assert.deepEqual(receipt.platformExactKeys, ["platform", "digest"]);
  assert.deepEqual(receipt.assetExactKeys, ["name", "sha256"]);
  assert.deepEqual(receipt.portableAssets, [
    "compose.yaml",
    "compose.deployment-platform-owned.yaml",
    "compose.deployment-managed-tke.yaml",
    "compose.deployment-customer-owned.yaml",
    "compose.fabric-local-docker.yaml",
    "compose.fabric-tencent-tke.yaml",
    "compose.local-workspace.yaml",
    "opl-cloud.env.example"
  ]);
  assert.equal(receipt.manifest, "opl-cloud-candidate.json");
  assert.equal(receipt.checksumManifest, "SHA256SUMS");
  assert.equal(receipt.checksumCoverage, "portable_assets_and_candidate_manifest");
  assert.deepEqual(receipt.provenanceExactKeys, [
    "workflowRepository",
    "workflowSha",
    "workflowRunId",
    "workflowRunAttempt"
  ]);
  assert.equal(receipt.receiptDigest, "sha256_of_canonical_json_bytes");
  assert.equal(receipt.qualificationFactsOwner, "instance_or_installer_receipt");

  const receiptSource = JSON.stringify(receipt);
  for (const forbidden of [
    "J2", "J4", "J5", "build_source", "reconcile", "registryMutationAttempts",
    "medopl.cn", "tencentyun.com", "accountId", "operationId", "providerId",
    "kubeconfig", "password", "secret", "releaseTag", "workspaceImage",
    "providerProfile", "domain"
  ]) {
    assert.doesNotMatch(receiptSource, new RegExp(forbidden, "i"));
  }
});

test("distribution contract locates a V2 Candidate bundle without duplicating its schema", async () => {
  const contract = JSON.parse(await readFile("packages/contracts/opl-cloud-distribution-contract.json", "utf8"));
  assert.equal(contract.schemaVersion, 2);
  assert.deepEqual(contract.candidate, {
    workflow: ".github/workflows/build-opl-cloud-candidate.yml",
    contract: "packages/contracts/opl-cloud-candidate-receipt-contract.json#candidateReceiptV2",
    registry: "ghcr.io/gaofeng21cn/one-person-lab-cloud",
    artifactLocator: {
      kind: "github_actions_artifact",
      exactKeys: ["workflowRunId", "artifactName"],
      workflowRunIdField: "provenance.workflowRunId",
      artifactNameTemplate: "opl-cloud-candidate-{product.sha}",
      artifactNameProductShaField: "product.sha"
    },
    formalPublication: false,
    instanceDeployment: false
  });
  assert.deepEqual(contract.instanceHandoff.inputs, ["candidate_manifest_b64"]);
  assert.equal(
    contract.instanceHandoff.artifactLocatorContract,
    "packages/contracts/opl-cloud-distribution-contract.json#candidate.artifactLocator"
  );
  assert.equal(
    contract.instanceHandoff.contract,
    "packages/contracts/opl-cloud-candidate-receipt-contract.json#candidateReceiptV2"
  );
  const manifest = { product: { sha: "a".repeat(40) }, provenance: { workflowRunId: "12345" } };
  const locator = contract.candidate.artifactLocator;
  assert.deepEqual({
    workflowRunId: manifest.provenance.workflowRunId,
    artifactName: locator.artifactNameTemplate.replace("{product.sha}", manifest.product.sha)
  }, {
    workflowRunId: "12345",
    artifactName: `opl-cloud-candidate-${"a".repeat(40)}`
  });
  assert.equal(contract.distribution.workflow, ".github/workflows/release-opl-cloud-image.yml");
});
