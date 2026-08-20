import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  candidateReceiptDigest,
  canonicalJson,
  decodeCandidateReceipt,
  main,
  validateCandidateBundle,
  validateCloudCandidateReceipt,
  writeCandidateReceipt
} from "../../tools/cloud-candidate-receipt.ts";

const productSha = "a".repeat(40);
const productTree = "b".repeat(40);
const indexDigest = `sha256:${"c".repeat(64)}`;
const amd64Digest = `sha256:${"d".repeat(64)}`;
const arm64Digest = `sha256:${"e".repeat(64)}`;
const cloudRepository = "ghcr.io/gaofeng21cn/one-person-lab-cloud";
const manifestName = "opl-cloud-candidate.json";
const assetNames = [
  "compose.yaml",
  "compose.deployment-platform-owned.yaml",
  "compose.deployment-managed-tke.yaml",
  "compose.deployment-customer-owned.yaml",
  "compose.fabric-local-docker.yaml",
  "compose.fabric-tencent-tke.yaml",
  "compose.local-workspace.yaml",
  "opl-cloud.env.example"
];

function sha256(value: string | Buffer) {
  return createHash("sha256").update(value).digest("hex");
}

function candidate(overrides: Record<string, unknown> = {}) {
  return {
    schemaVersion: 2,
    kind: "opl_cloud_candidate",
    product: {
      repository: "gaofeng21cn/one-person-lab-cloud",
      sha: productSha,
      tree: productTree
    },
    cloudImage: {
      repository: cloudRepository,
      indexRef: `${cloudRepository}@${indexDigest}`,
      indexDigest,
      revision: productSha,
      platforms: [
        { platform: "linux/amd64", digest: amd64Digest },
        { platform: "linux/arm64", digest: arm64Digest }
      ]
    },
    assets: assetNames.map((name) => ({ name, sha256: sha256(`${name}\n`) })),
    provenance: {
      workflowRepository: "gaofeng21cn/one-person-lab-cloud",
      workflowSha: "f".repeat(40),
      workflowRunId: "12345",
      workflowRunAttempt: "1"
    },
    ...overrides
  };
}

async function createBundle() {
  const directory = await mkdtemp(join(tmpdir(), "opl-cloud-candidate-bundle-"));
  for (const name of assetNames) await writeFile(join(directory, name), `${name}\n`);
  const receipt = candidate();
  await writeCandidateReceipt(join(directory, manifestName), receipt);
  const manifestSha = sha256(await readFile(join(directory, manifestName)));
  const sums = [
    ...(receipt.assets as Array<{ name: string; sha256: string }>).map(({ name, sha256: digest }) => `${digest}  ${name}`),
    `${manifestSha}  ${manifestName}`,
    ""
  ].join("\n");
  await writeFile(join(directory, "SHA256SUMS"), sums);
  return { directory, receipt };
}

test("candidate canonical JSON and receipt digest are stable by key order", () => {
  const value = candidate();
  const reordered = {
    provenance: value.provenance,
    assets: value.assets,
    cloudImage: value.cloudImage,
    product: value.product,
    kind: value.kind,
    schemaVersion: value.schemaVersion
  };
  assert.equal(canonicalJson(value), canonicalJson(reordered));
  assert.equal(candidateReceiptDigest(value), candidateReceiptDigest(reordered));
  assert.match(candidateReceiptDigest(value), /^sha256:[0-9a-f]{64}$/);
});

test("candidate validator accepts one portable multi-architecture identity", () => {
  assert.deepEqual(validateCloudCandidateReceipt(candidate()), candidate());
});

test("candidate validator rejects image, platform, revision, asset, and path drift", () => {
  const cloudImage = candidate().cloudImage as Record<string, unknown>;
  const platforms = cloudImage.platforms as Array<Record<string, unknown>>;
  const assets = candidate().assets as Array<Record<string, unknown>>;
  const invalid = [
    candidate({ unexpected: true }),
    candidate({ schemaVersion: 1 }),
    candidate({ kind: "build_cloud_source_to_tcr" }),
    candidate({ product: { ...candidate().product, sha: "A".repeat(40) } }),
    candidate({ cloudImage: { ...cloudImage, revision: "f".repeat(40) } }),
    candidate({ cloudImage: { ...cloudImage, indexRef: `${cloudRepository}:candidate` } }),
    candidate({ cloudImage: { ...cloudImage, indexDigest: amd64Digest, indexRef: `${cloudRepository}@${amd64Digest}` } }),
    candidate({ cloudImage: { ...cloudImage, platforms: [platforms[0]] } }),
    candidate({ cloudImage: { ...cloudImage, platforms: [...platforms].reverse() } }),
    candidate({ cloudImage: { ...cloudImage, platforms: [{ ...platforms[0], platform: "linux/s390x" }, platforms[1]] } }),
    candidate({ assets: assets.slice(1) }),
    candidate({ assets: [{ ...assets[0], name: "../compose.yaml" }, ...assets.slice(1)] }),
    candidate({ assets: [{ ...assets[0], sha256: `sha256:${"1".repeat(64)}` }, ...assets.slice(1)] }),
    candidate({ provenance: { ...candidate().provenance, workflowRunId: "0" } })
  ];
  for (const value of invalid) {
    assert.throws(() => validateCloudCandidateReceipt(value), /cloud_candidate_receipt_invalid/);
  }
});

test("candidate validator rejects Workspace, Provider, domain, release, and sensitive fields recursively", () => {
  for (const key of [
    "workspaceImage", "providerProfile", "domain", "releaseTag", "accountId", "operationId",
    "providerId", "password", "secret", "kubeconfig", "j2Receipt"
  ]) {
    assert.throws(
      () => validateCloudCandidateReceipt(candidate({ cloudImage: { ...candidate().cloudImage, [key]: "value" } })),
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
  const path = join(directory, manifestName);
  await writeCandidateReceipt(path, candidate());
  assert.equal((await stat(path)).mode & 0o777, 0o600);
  assert.equal(await readFile(path, "utf8"), `${canonicalJson(candidate())}\n`);
});

test("candidate bundle validator recomputes every asset and the canonical manifest", async () => {
  const { directory, receipt } = await createBundle();
  assert.deepEqual(await validateCandidateBundle(directory), receipt);
  await main(["validate-bundle", "--bundle", directory]);
});

test("candidate bundle validator rejects tampered, missing, extra, and malformed checksum inputs", async () => {
  const tampered = await createBundle();
  await writeFile(join(tampered.directory, assetNames[0]), "tampered\n");
  await assert.rejects(validateCandidateBundle(tampered.directory), /cloud_candidate_bundle_invalid/);

  const missing = await createBundle();
  await rm(join(missing.directory, assetNames[0]));
  await assert.rejects(validateCandidateBundle(missing.directory), /cloud_candidate_bundle_invalid/);

  const extra = await createBundle();
  await writeFile(join(extra.directory, "unexpected.txt"), "unexpected\n");
  await assert.rejects(validateCandidateBundle(extra.directory), /cloud_candidate_bundle_invalid/);

  const malformed = await createBundle();
  const sumsPath = join(malformed.directory, "SHA256SUMS");
  await writeFile(sumsPath, (await readFile(sumsPath, "utf8")).replace("  compose.yaml", " *compose.yaml"));
  await assert.rejects(validateCandidateBundle(malformed.directory), /cloud_candidate_bundle_invalid/);

  const noncanonical = await createBundle();
  const manifestPath = join(noncanonical.directory, manifestName);
  await writeFile(manifestPath, `${JSON.stringify(noncanonical.receipt, null, 2)}\n`);
  const noncanonicalSumsPath = join(noncanonical.directory, "SHA256SUMS");
  const noncanonicalSums = (await readFile(noncanonicalSumsPath, "utf8")).trimEnd().split("\n");
  noncanonicalSums[noncanonicalSums.length - 1] = `${sha256(await readFile(manifestPath))}  ${manifestName}`;
  await writeFile(noncanonicalSumsPath, `${noncanonicalSums.join("\n")}\n`);
  await assert.rejects(validateCandidateBundle(noncanonical.directory), /cloud_candidate_bundle_invalid/);
});

test("candidate export-env exposes only exact immutable Cloud consumer facts", async () => {
  const directory = await mkdtemp(join(tmpdir(), "opl-cloud-candidate-env-"));
  const receiptPath = join(directory, manifestName);
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
    "OPL_CLOUD_IMAGE_AMD64_DIGEST",
    "OPL_CLOUD_IMAGE_ARM64_DIGEST"
  ]);
  assert.equal(lines[1], `OPL_PRODUCT_SHA=${productSha}`);
  assert.equal(lines[2], `OPL_PRODUCT_TREE=${productTree}`);
  assert.equal(lines[3], `OPL_CLOUD_IMAGE=${cloudRepository}@${indexDigest}`);
});
