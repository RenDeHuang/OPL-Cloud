import assert from "node:assert/strict";
import { mkdtemp, readFile, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  candidateReceiptDigest,
  canonicalJson,
  decodeCandidateReceipt,
  main,
  validateCloudCandidateReceipt,
  writeCandidateReceipt
} from "../../tools/cloud-candidate-receipt.ts";

const productSha = "a".repeat(40);
const productTree = "b".repeat(40);
const cloudDigest = `sha256:${"c".repeat(64)}`;
const workspaceDigest = `sha256:${"d".repeat(64)}`;
const cloudRepository = "ghcr.io/gaofeng21cn/one-person-lab-cloud";

function candidate(overrides: Record<string, unknown> = {}) {
  return {
    schemaVersion: 1,
    kind: "opl_cloud_candidate",
    product: {
      repository: "gaofeng21cn/one-person-lab-cloud",
      sha: productSha,
      tree: productTree
    },
    platform: "linux/amd64",
    cloudImage: {
      repository: cloudRepository,
      ref: `${cloudRepository}@${cloudDigest}`,
      digest: cloudDigest,
      revision: productSha
    },
    workspaceImage: {
      ref: `registry.example.com/opl/workspace@${workspaceDigest}`,
      digest: workspaceDigest
    },
    provenance: {
      workflowRepository: "gaofeng21cn/one-person-lab-cloud",
      workflowSha: "e".repeat(40),
      workflowRunId: "12345",
      workflowRunAttempt: "1"
    },
    ...overrides
  };
}

test("candidate canonical JSON and receipt digest are stable by key order", () => {
  const value = candidate();
  const reordered = {
    provenance: value.provenance,
    workspaceImage: value.workspaceImage,
    cloudImage: value.cloudImage,
    platform: value.platform,
    product: value.product,
    kind: value.kind,
    schemaVersion: value.schemaVersion
  };
  assert.equal(canonicalJson(value), canonicalJson(reordered));
  assert.equal(candidateReceiptDigest(value), candidateReceiptDigest(reordered));
  assert.match(candidateReceiptDigest(value), /^sha256:[0-9a-f]{64}$/);
});

test("candidate validator accepts exactly one neutral immutable identity", () => {
  assert.deepEqual(validateCloudCandidateReceipt(candidate()), candidate());
});

test("candidate validator rejects unknown keys and identity drift", () => {
  const invalid = [
    candidate({ unexpected: true }),
    candidate({ schemaVersion: 2 }),
    candidate({ kind: "build_cloud_source_to_tcr" }),
    candidate({ platform: "linux/arm64" }),
    candidate({ product: { ...candidate().product, sha: "A".repeat(40) } }),
    candidate({ cloudImage: { ...candidate().cloudImage, revision: "f".repeat(40) } }),
    candidate({ cloudImage: { ...candidate().cloudImage, ref: `${cloudRepository}:candidate` } }),
    candidate({ workspaceImage: { ...candidate().workspaceImage, ref: "registry.example.com/opl/workspace:latest" } }),
    candidate({ provenance: { ...candidate().provenance, workflowRunId: "0" } })
  ];
  for (const value of invalid) {
    assert.throws(() => validateCloudCandidateReceipt(value), /cloud_candidate_receipt_invalid/);
  }
});

test("candidate validator rejects sensitive and legacy fields recursively", () => {
  for (const key of ["accountId", "operationId", "providerId", "password", "secret", "kubeconfig", "j2Receipt"]) {
    assert.throws(
      () => validateCloudCandidateReceipt(candidate({ workspaceImage: { ...candidate().workspaceImage, [key]: "value" } })),
      /cloud_candidate_receipt_invalid|cloud_candidate_receipt_sensitive/
    );
  }
});

test("candidate Base64 decoder requires a strict round trip", () => {
  const encoded = Buffer.from(`${JSON.stringify(candidate())}\n`, "utf8").toString("base64");
  assert.deepEqual(decodeCandidateReceipt(encoded), candidate());
  for (const value of ["", "not-base64", `${encoded}\n`, Buffer.from("{}", "utf8").toString("base64")]) {
    assert.throws(() => decodeCandidateReceipt(value), /cloud_candidate_receipt_encoding_invalid|cloud_candidate_receipt_invalid/);
  }
});

test("candidate writer preserves validated canonical bytes with mode 0600", async () => {
  const directory = await mkdtemp(join(tmpdir(), "opl-cloud-candidate-"));
  const path = join(directory, "candidate.json");
  await writeCandidateReceipt(path, candidate());
  assert.equal((await stat(path)).mode & 0o777, 0o600);
  assert.equal(await readFile(path, "utf8"), `${canonicalJson(candidate())}\n`);
});

test("candidate export-env exposes only exact immutable consumer facts", async () => {
  const directory = await mkdtemp(join(tmpdir(), "opl-cloud-candidate-env-"));
  const receiptPath = join(directory, "candidate.json");
  const envPath = join(directory, "github-env");
  await writeCandidateReceipt(receiptPath, candidate());
  await main(["export-env", "--receipt", receiptPath, "--github-env", envPath]);
  const lines = (await readFile(envPath, "utf8")).trim().split("\n");
  assert.deepEqual(lines.map((line) => line.slice(0, line.indexOf("="))), [
    "OPL_CANDIDATE_RECEIPT_DIGEST",
    "OPL_PRODUCT_SHA",
    "OPL_PRODUCT_TREE",
    "OPL_CLOUD_IMAGE",
    "OPL_CLOUD_IMAGE_DIGEST",
    "OPL_WORKSPACE_IMAGE",
    "OPL_WORKSPACE_IMAGE_DIGEST"
  ]);
  assert.equal(lines[1], `OPL_PRODUCT_SHA=${productSha}`);
  assert.equal(lines[2], `OPL_PRODUCT_TREE=${productTree}`);
  assert.equal(lines[3], `OPL_CLOUD_IMAGE=${cloudRepository}@${cloudDigest}`);
});
